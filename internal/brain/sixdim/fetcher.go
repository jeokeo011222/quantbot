// 六维判势 Fetcher：从 DuckDB 拉取真实市场数据 + 外部实时源，填充 MarketInput。
//
// 数据来源原则（对应项目硬约束「严禁伪造数据」）：
//   - 技术趋势/市场广度/量能/资金集中度 均来自 DuckDB 真实 K 线与行业统计；
//   - 情绪 优先东财实时涨停/跌停/炸板池(push2ex，5分钟缓存)，缺失回退 DuckDB 全市场收盘统计/采样日线代理；
//   - 资金结构 优先同花顺 hsgtApi 北向实时净流入，其次东财数据中心融资余额趋势（真实杠杆资金），
//     再兜底「全市场成交额连续放量天数」代理，均 Sources 明确标注；
//   - 炸板率无盘中分时数据 → 用日线「盘中最高触及涨停价但收盘未封板」作代理，标注；
//   - 隔夜外围/事件风险无数据源 → 取中性默认(0/false)，标注，绝不猜测。
//
// 外部实时源（同花顺/东财）统一走 TTL 缓存 + 最小请求间隔限流（参考 tradex-hub anti_ban_client），
// 避免盘中高频监控触发 IP 封禁。
package sixdim

import (
	"context"
	"fmt"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/quantpilot/quantpilot/internal/brain/port"
)

// Fetcher 市场六维判势数据获取器
type Fetcher struct {
	mkt        port.MarketDataStore
	snap       port.SnapSource
	ext        port.MarketExternalSources
	ths        port.THSSentimentSource
	sampleSize int
	cfg        SourceConfig
}

// NewFetcher 创建六维判势数据获取器（默认数据源配置）
func NewFetcher(mkt port.MarketDataStore) *Fetcher {
	return NewFetcherWithConfig(mkt, DefaultSourceConfig())
}

// NewFetcherWithConfig 用自定义数据源配置创建获取器（优先级/启用开关，对标 PanWatch marketdata engine 的主备配置）
func NewFetcherWithConfig(mkt port.MarketDataStore, cfg SourceConfig) *Fetcher {
	return &Fetcher{mkt: mkt, sampleSize: 300, cfg: cfg}
}

// SetSnapSource 注入活跃实时行情源（可空；为空则回退 DuckDB 收盘价）。
func (f *Fetcher) SetSnapSource(s port.SnapSource) { f.snap = s }

// SetExternalSources 注入外部实时源（北向/两融/涨停池/隔夜外围；可空）。
func (f *Fetcher) SetExternalSources(s port.MarketExternalSources) { f.ext = s }

// SetTHSSource 注入同花顺官方情绪面源（可空）。
func (f *Fetcher) SetTHSSource(s port.THSSentimentSource) { f.ths = s }

// CurrentSourceConfig 返回当前生效的数据源配置（供工具输出/观测）
func (f *Fetcher) CurrentSourceConfig() SourceConfig {
	return f.cfg
}

// ==================== 数据源配置：可配置优先级 + 启用开关（对标 PanWatch marketdata engine） ====================
//
// 各维度外部实时源按「启用开关 + 优先级」决定尝试顺序（数值越小越优先），首个成功者生效，
// 其余自动回退并标注，绝不伪造。默认值与既有取数行为完全一致，调用方可按部署环境覆盖。

// SourceConfig 外部数据源可配置启用开关与优先级。
type SourceConfig struct {
	// 资金结构维度（fillCapital）
	NorthboundEnabled       bool // 同花顺 hsgtApi 北向实时净流入（盘中分钟级）
	NorthboundPriority      int
	MarginEnabled           bool // 东财数据中心融资余额连续增减天数（T-1 真实杠杆资金）
	MarginPriority          int
	VolumeExpansionEnabled  bool // 全市场成交额连续放量天数（DuckDB 兜底代理）
	VolumeExpansionPriority int
	// 情绪维度（fillSentiment）
	THSBoardsEnabled        bool // 同花顺官方涨停/跌停/炸板池 + 连板天梯（真实炸板率，替代日线代理）
	THSBoardsPriority       int
	LimitBoardEnabled       bool // 东财 push2ex 实时涨停/跌停/炸板池（盘中，5min缓存）
	LimitBoardPriority      int
	FullMarketStatsEnabled  bool // DuckDB 全市场收盘统计
	FullMarketStatsPriority int
	// 外部约束维度（realOvernightSign）
	OvernightEnabled  bool // 腾讯全球指数真实隔夜外围（4s超时+5min缓存）
	OvernightPriority int
}

// DefaultSourceConfig 默认数据源配置（与既有取数链完全一致）。
// 支持通过环境变量 SIXDIM_<源名>_ENABLED / SIXDIM_<源名>_PRIORITY 覆盖（部署可配置，对标 PanWatch marketdata config）：
//
//	如 SIXDIM_NORTHBOUND_ENABLED=false 关闭北向源、SIXDIM_MARGIN_PRIORITY=1 提升两融优先级。
func DefaultSourceConfig() SourceConfig {
	cfg := SourceConfig{
		NorthboundEnabled: true, NorthboundPriority: 1,
		MarginEnabled: true, MarginPriority: 2,
		VolumeExpansionEnabled: true, VolumeExpansionPriority: 3,
		THSBoardsEnabled: true, THSBoardsPriority: 1, // 同花顺官方情绪源（未配置Key时自动回退东财）
		LimitBoardEnabled: true, LimitBoardPriority: 1,
		FullMarketStatsEnabled: true, FullMarketStatsPriority: 2,
		OvernightEnabled: true, OvernightPriority: 1,
	}
	applyEnvOverride(&cfg)
	return cfg
}

// applyEnvOverride 用环境变量覆盖数据源启用开关与优先级（未设置的环境变量不改变默认值）。
func applyEnvOverride(cfg *SourceConfig) {
	// key: 环境变量名 -> (enabled 字段指针, priority 字段指针)
	type srcOverride struct {
		enabled  *bool
		priority *int
	}
	overrides := map[string]srcOverride{
		"SIXDIM_NORTHBOUND":        {&cfg.NorthboundEnabled, &cfg.NorthboundPriority},
		"SIXDIM_MARGIN":            {&cfg.MarginEnabled, &cfg.MarginPriority},
		"SIXDIM_VOLUME_EXPANSION":  {&cfg.VolumeExpansionEnabled, &cfg.VolumeExpansionPriority},
		"SIXDIM_THS_BOARDS":        {&cfg.THSBoardsEnabled, &cfg.THSBoardsPriority},
		"SIXDIM_LIMIT_BOARD":       {&cfg.LimitBoardEnabled, &cfg.LimitBoardPriority},
		"SIXDIM_FULL_MARKET_STATS": {&cfg.FullMarketStatsEnabled, &cfg.FullMarketStatsPriority},
		"SIXDIM_OVERNIGHT":         {&cfg.OvernightEnabled, &cfg.OvernightPriority},
	}
	for name, o := range overrides {
		if v := strings.TrimSpace(os.Getenv(name + "_ENABLED")); v != "" {
			if b, err := strconv.ParseBool(v); err == nil {
				*o.enabled = b
			}
		}
		if v := strings.TrimSpace(os.Getenv(name + "_PRIORITY")); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				*o.priority = n
			}
		}
	}
}

// ==================== 外部数据源运行指标（可观测性，对标 PanWatch marketdata metrics） ====================
//
// 每次外部实时源调用（成功/失败/耗时/错误）都累计到包级指标，供工具输出与日志观测，
// 帮助判断「高优先级源是否频繁失败、是否应调整优先级或降级」。

// sourceStat 单个外部数据源的累计运行指标
type sourceStat struct {
	okCount   int
	failCount int
	lastErr   string
	lastMs    int64 // 最近一次调用耗时(ms)
	updatedAt time.Time
}

var (
	sourceStatMu   sync.Mutex
	sourceStatList = map[string]*sourceStat{}
)

// recordSourceStat 记录一次外部数据源调用结果（成败 + 耗时 + 错误）
func recordSourceStat(name string, ok bool, ms time.Duration, err error) {
	sourceStatMu.Lock()
	defer sourceStatMu.Unlock()
	st := sourceStatList[name]
	if st == nil {
		st = &sourceStat{}
		sourceStatList[name] = st
	}
	if ok {
		st.okCount++
	} else {
		st.failCount++
		if err != nil {
			st.lastErr = err.Error()
		}
	}
	st.lastMs = ms.Milliseconds()
	st.updatedAt = time.Now()
}

// SourceStatsSnapshot 返回各外部数据源累计运行指标快照（供工具输出展示）
func SourceStatsSnapshot() map[string]map[string]interface{} {
	sourceStatMu.Lock()
	defer sourceStatMu.Unlock()
	out := make(map[string]map[string]interface{}, len(sourceStatList))
	for name, st := range sourceStatList {
		out[name] = map[string]interface{}{
			"ok":         st.okCount,
			"fail":       st.failCount,
			"last_ms":    st.lastMs,
			"last_err":   st.lastErr,
			"updated_at": st.updatedAt.Format("15:04:05"),
		}
	}
	return out
}

// ==================== 外部实时源：TTL 缓存 + 限流（参考 tradex-hub anti_ban_client） ====================
//
// CIO 每次盘前/盘中判势都会 new Fetcher，缓存必须放包级，否则跨调用失效。
// 同花顺/东财系接口有风控（每秒>5次/分钟≥200次会临时封IP），用「TTL 缓存」降请求频率，
// 用「最小请求间隔」限流，双保险避免封禁。缓存全失败时各维度自动回退，绝不影响判势主流程。

// ttlCache 简单并发安全 TTL 缓存（Go 1.18+ 泛型）。
type ttlCache[T any] struct {
	mu  sync.Mutex
	ttl time.Duration
	ok  bool
	val T
	at  time.Time
}

func newTTLCache[T any](ttl time.Duration) *ttlCache[T] { return &ttlCache[T]{ttl: ttl} }

// Get 未过期命中返回 true；过期/未缓存返回零值+false。
func (c *ttlCache[T]) Get() (T, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ok && time.Since(c.at) < c.ttl {
		return c.val, true
	}
	var zero T
	return zero, false
}

func (c *ttlCache[T]) Set(v T) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.val, c.ok, c.at = v, true, time.Now()
}

// 外部实时源缓存：北向(盘中分钟级变化，5min) / 同花顺官方情绪面(盘中变化，5min) / 涨停池(盘中变化，5min) / 两融(T+1，1h)。
var (
	northboundCache = newTTLCache[float64](5 * time.Minute)
	thsBoardCache   = newTTLCache[*port.SentimentSnapshot](5 * time.Minute)
	limitBoardCache = newTTLCache[port.LimitUpBoard](5 * time.Minute)
	marginCache     = newTTLCache[[]port.MarginPoint](time.Hour)
	// 批量实时快照缓存：广度维度每次判势会请求整组代表样本(~300只)的实时快照，是判势链路最贵的外部调用。
	// 盘前连续多次判势(智能体/调度/手动)会重复触发；用短 TTL(30s) 去抖，同时带源名区分，切源即失效。
	//（相较于 native_tdx 逐只串行，短缓存可显著降低请求数与延迟；腾讯等批量源天然高效，同受益。）
	realtimeSnapCache = newTTLCache[realtimeSnapsEntry](30 * time.Second)
)

// realtimeSnapsEntry 批量实时快照缓存条目：记录来源名以在切换数据源后自动失效。
type realtimeSnapsEntry struct {
	source string
	snaps  []port.StockSnapshot
}

// tradingSessionNow 当前是否处于 A 股交易时段（含盘前竞价 09:15 与收盘后 5 分钟内的准实时区间）。
// 只有交易时段内实时快照才有别于昨日收盘，非交易时段判势直接走 DuckDB 历史，避免浪费外部请求。
func tradingSessionNow() bool {
	now := time.Now()
	if now.Weekday() == time.Saturday || now.Weekday() == time.Sunday {
		return false
	}
	m := now.Hour()*60 + now.Minute()
	return m >= 9*60+15 && m <= 15*60+5
}

// externalMinInterval 相邻两次外部请求最小间隔，镜像 anti_ban_client 的 EM_MIN_INTERVAL=1.0。
const externalMinInterval = time.Second

var (
	externalMu     sync.Mutex
	externalLastAt time.Time
)

// throttleExternal 保证相邻外部请求间隔 ≥ externalMinInterval（串行限流，避免突发请求触发风控）。
func throttleExternal() {
	externalMu.Lock()
	defer externalMu.Unlock()
	if d := externalMinInterval - time.Since(externalLastAt); d > 0 {
		time.Sleep(d)
	}
	externalLastAt = time.Now()
}

// Fetch 拉取真实市场数据并填充六维判势输入
func (f *Fetcher) Fetch(ctx context.Context) (*MarketInput, error) {
	if f.mkt == nil || !f.mkt.HasStockDB() {
		return nil, fmt.Errorf("DuckDB 不可用，无法获取六维判势输入数据")
	}

	in := &MarketInput{Sources: map[string]string{}}

	// 指数K线（供技术趋势 + 价量配合复用，一次查询）
	indexBars, indexErr := f.mkt.GetKlineFromStock(ctx, "sh000001", 70)
	if indexErr != nil {
		indexBars = nil
		log.Printf("[SixDim] 上证指数K线获取失败: %v", indexErr)
	}

	// 采样个股K线（供市场广度/情绪维度复用，一次批量查询；活跃实时源可覆盖最新价）
	barsMap, asOf, rtOverride := f.fetchSampleBars(ctx)
	stats := aggregateSampleStats(barsMap)

	// 维度1 技术趋势
	f.fillTech(in, indexBars)
	// 维度2 市场广度
	f.fillBreadth(ctx, in, stats, rtOverride)
	// 维度3 量能流动性
	f.fillVolume(ctx, in, indexBars)
	// 维度4 资金结构
	f.fillCapital(ctx, in)
	// 维度5 情绪赚钱效应
	f.fillSentiment(ctx, in, stats)
	// 维度6 外部约束：优先取真实隔夜外围(腾讯全球指数，短期超时+缓存)，失败再回退
	// 「上证指数开盘跳空缺口」真实A股代理；事件风险无公开权威源取 false，绝不猜测。
	overnight := overnightProxy(indexBars)
	overnightSrc := overnightProxyDesc(indexBars)
	if f.cfg.OvernightEnabled {
		if real, ok, rsrc := f.realOvernightSign(ctx); ok {
			// 真实外围方向与指数缺口互相印证：均偏空取更保守，方向相反(外好内低开)取折中0
			switch {
			case real == -1 && overnight == -1:
				overnight = -1
			case real == 1 && overnight == 1:
				overnight = 1
			default:
				if real != 0 && overnight != 0 && real != overnight {
					overnight = 0 // 外围与缺口冲突，取中性
				} else if real != 0 {
					overnight = real
				}
			}
			overnightSrc = fmt.Sprintf("真实隔夜外围(%s:%s)融合指数跳空(%s)", rsrc, overnightSign(real), overnightProxyDesc(indexBars))
		}
	}
	in.Dim6External = DimExternal{OvernightUS: overnight, OvernightGapPct: overnightGapPct(indexBars), EventRisk: false}
	in.Sources["external"] = "外部维度来源: " + overnightSrc

	if !asOf.IsZero() {
		in.Sources["as_of"] = "数据日期: " + asOf.Format("2006-01-02")
	}
	return in, nil
}

// fetchSampleBars 采样一批普通A股（分层代表性抽样）并批量拉取最近K线（date DESC，最新在前）。
// 若活跃实时行情源可用，用其真实实时快照覆盖每只样本的最新价/前收/最高，使广度维度呈现「盘中/盘前实时」口径，
// 而 20 日新高/新低仍用 DuckDB 历史区间对照（新高/新低天然需要历史窗口）。全源失败时回退全宇宙截断。
func (f *Fetcher) fetchSampleBars(ctx context.Context) (map[string][]port.FactorBar, time.Time, realtimeOverlayStats) {
	var rt realtimeOverlayStats
	symbols, err := f.mkt.ListRepresentativeSymbols(ctx, f.sampleSize)
	if err != nil || len(symbols) == 0 {
		log.Printf("[SixDim] 代表性股票列表获取失败(回退字典序截断): %v", err)
		if symbols, err = f.mkt.ListAllSymbolsFromStock(ctx, "all", f.sampleSize); err != nil || len(symbols) == 0 {
			return nil, time.Time{}, rt
		}
	}
	codes := make([]string, 0, len(symbols))
	for _, s := range symbols {
		codes = append(codes, s.Symbol)
	}
	barsMap, err := f.mkt.BatchGetFactorBars(ctx, codes, 25)
	if err != nil {
		log.Printf("[SixDim] 批量K线获取失败: %v", err)
		return nil, time.Time{}, rt
	}

	// 实时覆盖：仅交易时段(含盘前竞价)内用活跃实时行情源的真实快照更新样本最新价/前收/最高，
	// 非交易时段实时报价即昨收，直接用 DuckDB 历史，避免对 ~300 只样本的无谓外部请求。
	if f.snap != nil && tradingSessionNow() {
		rt = f.applyRealtimeOverlay(barsMap, codes)
	}

	var asOf time.Time
	for _, bars := range barsMap {
		if len(bars) > 0 {
			asOf = bars[0].Date
			break
		}
	}
	return barsMap, asOf, rt
}

// realtimeOverlayStats 实时覆盖统计（用于 sources 标注）
type realtimeOverlayStats struct {
	covered int // 成功覆盖实时价的样本数
	queries int // 请求实时快照的总数
}

// applyRealtimeOverlay 用活跃实时源批量快照覆盖 barsMap 每只样本的最新收盘价/前收/最高价。
// barsMap 内切片为 date DESC（bars[0] 为最新）。快照 key 归一化为 ohlc 同款（sh600519）。
// 仅覆盖 CurrentPrice>0 且当前价与前收均为真实数据的快照，绝不伪造；北交所等实时源无报价的股票自然跳过。
// 批量快照按源名走 30s TTL 缓存：盘前连续多次判势复用，降低请求数与封禁/限流风险。
func (f *Fetcher) applyRealtimeOverlay(barsMap map[string][]port.FactorBar, codes []string) realtimeOverlayStats {
	rt := realtimeOverlayStats{queries: len(codes)}
	ds := f.snap
	if ds == nil || len(codes) == 0 {
		return rt
	}
	src := ds.Source()

	// 命中缓存则直接复用（需源名一致，切源自动失效）
	var snaps []port.StockSnapshot
	cached := false
	if e, ok := realtimeSnapCache.Get(); ok && e.source == src {
		snaps, cached = e.snaps, true
	} else {
		got, err := ds.GetStockSnapshots(codes)
		if err != nil {
			log.Printf("[SixDim] 实时快照获取失败(回退DuckDB收盘价): %v", err)
			return rt
		}
		snaps = got
		realtimeSnapCache.Set(realtimeSnapsEntry{source: src, snaps: got})
	}

	for _, sp := range snaps {
		if sp.IsMock || sp.CurrentPrice <= 0 || sp.PrevClose <= 0 {
			continue
		}
		key := strings.ToLower(sp.Market) + sp.Code
		bars, ok := barsMap[key]
		if !ok || len(bars) == 0 {
			continue
		}
		last := &bars[0] // date DESC，最新在前
		if sp.High > last.High {
			last.High = sp.High
		}
		last.Close = sp.CurrentPrice
		last.PreClose = sp.PrevClose
		rt.covered++
	}
	// 覆盖率监控：拉到了快照但一只都未匹配，说明 key 口径/源格式可能不一致，需告警，避免静默永远走历史。
	if !cached && len(snaps) > 0 && rt.covered == 0 {
		log.Printf("[SixDim] 警告: 实时快照(%d只)未匹配到任何代表样本(%d只)，key口径可能不一致(源=%s)，回退DuckDB",
			len(snaps), len(codes), src)
	}
	log.Printf("[SixDim] 实时快照覆盖 %d/%d 只代表样本 (源=%s,%s)", rt.covered, len(codes), src,
		map[bool]string{true: "缓存命中", false: "实时获取"}[cached])
	return rt
}

// sampleMarketStats 采样市场统计（供广度/情绪维度共用）
type sampleMarketStats struct {
	N              int // 有效样本数
	RiseCount      int // 上涨家数（实时价>前收）
	DownCount      int // 下跌家数
	High20         int // 20日新高家数
	Low20          int // 20日新低家数
	LimitUp        int // 收盘涨停家数
	LimitDown      int // 收盘跌停家数
	TouchedLimitUp int // 盘中触涨停价家数（含封板）
	SealedLimitUp  int // 收盘封死涨停家数
}

// aggregateSampleStats 从采样K线聚合统计（date DESC → 内部转升序处理）
func aggregateSampleStats(barsMap map[string][]port.FactorBar) sampleMarketStats {
	var s sampleMarketStats
	for _, bars := range barsMap {
		if len(bars) < 2 {
			continue
		}
		sort.SliceStable(bars, func(i, j int) bool { return bars[i].Date.Before(bars[j].Date) })
		last := bars[len(bars)-1]
		if last.PreClose <= 0 {
			continue
		}
		s.N++
		ratio := limitRatioFor(last.Symbol)
		limitPrice := last.PreClose * (1 + ratio)

		// 涨/跌家数（实时价 vs 前收）
		if last.Close > last.PreClose {
			s.RiseCount++
		} else if last.Close < last.PreClose {
			s.DownCount++
		}

		// 20日新高/新低：与「当前 bar 之前」的历史对比（取最近至多20个前收，不含当前，避免 off-by-one
		// 导致实际只比对19个历史收盘价）。当前价如被实时快照覆盖，仍用实时价与历史收盘对比，更贴近当下。
		if len(bars) >= 2 {
			prior := bars[:len(bars)-1]
			if np := len(prior); np > 20 {
				prior = prior[np-20:]
			}
			isHigh, isLow := true, true
			for _, w := range prior {
				if w.Close > last.Close {
					isHigh = false
				}
				if w.Close < last.Close {
					isLow = false
				}
			}
			if isHigh {
				s.High20++
			}
			if isLow {
				s.Low20++
			}
		}

		// 涨停/跌停/炸板（日线代理）
		if last.Close >= limitPrice-0.001 {
			s.LimitUp++
			s.SealedLimitUp++
			s.TouchedLimitUp++
		} else if last.High >= limitPrice-0.001 {
			s.TouchedLimitUp++ // 盘中触板但收盘未封 = 炸板
		}
		if last.Close <= last.PreClose*(1-ratio)+0.001 {
			s.LimitDown++
		}
	}
	return s
}

// limitRatioFor 按板块涨跌幅口径返回涨跌停比例（与 tools 层口径一致）
func limitRatioFor(code string) float64 {
	num := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(code)), "sh")
	num = strings.TrimPrefix(num, "sz")
	num = strings.TrimPrefix(num, "bj")
	num = strings.TrimPrefix(num, ".")
	if len(num) >= 3 {
		prefix := num[:3]
		if prefix == "688" || prefix == "300" || prefix == "301" {
			return 0.20
		}
		if strings.HasPrefix(num, "4") || strings.HasPrefix(num, "8") || strings.HasPrefix(num, "92") {
			return 0.30
		}
	}
	return 0.10
}

// ==================== 各维度填充 ====================

// fillTech 维度1 技术趋势：上证指数 MA20/MA60 + 5日/20日收益趋势
func (f *Fetcher) fillTech(in *MarketInput, bars []port.KlineBar) {
	if len(bars) < 20 {
		in.Sources["tech"] = fmt.Sprintf("上证指数K线不足(%d条)，技术趋势维度按中性处理", len(bars))
		in.Dim1Tech = DimTech{IndexAboveMA20: false, IndexAboveMA60: false, TrendState: 0}
		return
	}
	// bars 为 date DESC，bars[0] 最新
	closes := make([]float64, len(bars))
	for i, b := range bars {
		closes[i] = b.Close
	}
	latest := closes[0]
	ma20 := mean(closes[:20])
	n60 := 60
	if len(closes) < n60 {
		n60 = len(closes)
	}
	ma60 := mean(closes[:n60])

	day5Ret := 0.0
	if len(closes) > 5 && closes[5] > 0 {
		day5Ret = (latest - closes[5]) / closes[5] * 100
	}
	day20Ret := 0.0
	if len(closes) > 20 && closes[20] > 0 {
		day20Ret = (latest - closes[20]) / closes[20] * 100
	}
	trend := 0
	switch {
	case day5Ret > 0.5 && day20Ret > 1:
		trend = 1
	case day5Ret < -0.5 && day20Ret < -1:
		trend = -1
	}
	in.Dim1Tech = DimTech{
		IndexAboveMA20: latest > ma20,
		IndexAboveMA60: latest > ma60,
		TrendState:     trend,
	}
	in.Sources["tech"] = fmt.Sprintf("上证指数(sh000001)日K真实计算: MA20=%.1f MA60=%.1f 5日=%.2f%% 20日=%.2f%%",
		ma20, ma60, day5Ret, day20Ret)
}

// fillBreadth 维度2 市场广度：涨跌家数 + 新高新低 + 涨跌停比 + 主线板块数
func (f *Fetcher) fillBreadth(ctx context.Context, in *MarketInput, s sampleMarketStats, rt realtimeOverlayStats) {
	b := DimBreadth{}

	// 统一口径说明：广度四成分现在全部基于同一「分层代表性样本」（沪/深/创/科/北等比例），
	// 消除旧版 `ORDER BY symbol LIMIT 300` 全落北交所+沪600导致的「全市场涨跌家数 vs 采样新高新低/涨跌停」
	// 口径打架问题。若活跃实时源可用，最新价已用实时快照覆盖（盘中/盘前呈现实时广度）。
	if s.N > 0 {
		// 上涨家数占比（实时价 vs 前收）
		b.RisePct = float64(s.RiseCount) / float64(s.N)
		riseSrc := fmt.Sprintf("代表样本涨跌家数(涨%d/总%d)=%.0f%%", s.RiseCount, s.N, b.RisePct*100)
		if rt.covered > 0 {
			riseSrc += fmt.Sprintf("| 其中%d只已用实时快照覆盖", rt.covered)
		}
		// 低样本可信度提示：代表样本明显缩水(如K线缺失/重仓单只)时，广度的涨跌占比易被少数样本放大，如实标注。
		if s.N < 100 {
			riseSrc += fmt.Sprintf("| 样本数%d偏少(低可信度)", s.N)
		}
		in.Sources["breadth_rise"] = riseSrc

		// 20日新高/新低比（20日区间基于 DuckDB 历史，天然需历史窗口；最新价如为实时覆盖则更贴近当下）
		if s.Low20 > 0 {
			b.HighLowRatio = float64(s.High20) / float64(s.Low20)
		} else if s.High20 > 0 {
			b.HighLowRatio = float64(s.High20) // 无新低，新高越多比值越大
		}
		in.Sources["breadth_highlow"] = fmt.Sprintf("代表样本20日新高/新低(%d/%d)",
			s.High20, s.Low20)

		// 涨跌停比：优先东财 push2ex 实时【全市场】涨停/跌停池（盘中真实口径），失败回退代表样本统计。
		// 语义说明：广度维度的涨跌停比衡量「涨停/跌停家数之比」(广度两端)；情绪维度即便可能读到同一实时池，
		// 表达的是「赚钱效应热度」——两者量纲不同、不构成重复计数，来源均在对应 Sources 键中如实标注。
		b.LimitUpLimitDownRatio = limitRatio(s.LimitUp, s.LimitDown)
		limitSrc := fmt.Sprintf("代表样本涨停/跌停(%d/%d)", s.LimitUp, s.LimitDown)
		if f.cfg.LimitBoardEnabled {
			if board, ok := f.cachedLimitBoard(ctx); ok && board.LimitUpCnt+board.LimitDownCnt > 0 {
				b.LimitUpLimitDownRatio = limitRatio(board.LimitUpCnt, board.LimitDownCnt)
				limitSrc = fmt.Sprintf("实时全市场涨停池(东财push2ex)涨停/跌停(%d/%d)", board.LimitUpCnt, board.LimitDownCnt)
			}
		}
		in.Sources["breadth_limit"] = limitSrc
	}

	// 主线板块数（代理）：行业平均涨幅为正且成分股≥3家
	if sectors, err := f.mkt.GetSectorChangeStats(ctx); err == nil {
		for _, sec := range sectors {
			if sec.AvgChgPct > 0 && sec.StockCount >= 3 {
				b.HotlineCount++
			}
		}
		if b.HotlineCount > 5 {
			b.HotlineCount = 5
		}
		in.Sources["breadth_hotline"] = fmt.Sprintf("行业平均涨幅为正且成分股≥3家的板块数=%d(代理)", b.HotlineCount)
	}

	in.Dim2Breadth = b
}

// limitRatio 涨跌停比：分母为0时用分子作为强度代理（与旧逻辑一致，防止除零）。
func limitRatio(up, down int) float64 {
	if down > 0 {
		return float64(up) / float64(down)
	}
	if up > 0 {
		return float64(up)
	}
	return 0
}

// fillVolume 维度3 量能流动性：全市场成交额/20日均值 + 价量配合
func (f *Fetcher) fillVolume(ctx context.Context, in *MarketInput, indexBars []port.KlineBar) {
	amt, err := f.mkt.GetMarketAmountHistory(ctx, 30)
	if err != nil || len(amt) < 2 {
		in.Sources["volume"] = fmt.Sprintf("市场成交额历史不足(%v)，量能维度按中性处理", err)
		in.Dim3Volume = DimVolume{TotalAmtRatio: 1, PriceVolumeMatch: 0}
		return
	}
	latest := amt[len(amt)-1]
	window := 20
	if len(amt)-1 < window {
		window = len(amt) - 1
	}
	var sum float64
	for i := len(amt) - 1 - window; i < len(amt)-1; i++ {
		sum += amt[i].Amount
	}
	avg := sum / float64(window)
	totalAmtRatio := 1.0
	if avg > 0 {
		totalAmtRatio = latest.Amount / avg
	}

	// 量能趋势：近5日成交额均值 / 近20日成交额均值（反映资金持续介入/撤离，降低单日噪声）
	trendRatio := 1.0
	if len(amt) > 25 {
		var s5, s20 float64
		for i := len(amt) - 5; i < len(amt); i++ {
			s5 += amt[i].Amount
		}
		for i := len(amt) - 20; i < len(amt); i++ {
			s20 += amt[i].Amount
		}
		if s20 > 0 {
			trendRatio = (s5 / 5) / (s20 / 20)
		}
	}
	// 综合量能：当日比值(0.6) + 5/20日趋势(0.4)，使量能维度更平滑且对放量/缩量趋势灵敏
	combinedRatio := 0.6*totalAmtRatio + 0.4*trendRatio

	// 价量配合：上证当日涨跌 × 全市场成交额环比
	pvm := 0
	if len(indexBars) >= 2 && len(amt) >= 2 {
		idxChg := 0.0
		if indexBars[1].Close > 0 {
			idxChg = (indexBars[0].Close - indexBars[1].Close) / indexBars[1].Close * 100
		}
		amtChg := 0.0
		if amt[len(amt)-2].Amount > 0 {
			amtChg = latest.Amount/amt[len(amt)-2].Amount - 1
		}
		switch {
		case idxChg > 0.3 && amtChg > 0.05:
			pvm = 1
		case idxChg > 0.3 && amtChg < -0.05:
			pvm = -1
		case idxChg < -0.3 && amtChg > 0.05:
			pvm = -1
		default:
			pvm = 0
		}
	}

	in.Dim3Volume = DimVolume{TotalAmtRatio: round2(combinedRatio), PriceVolumeMatch: pvm}
	in.Sources["volume"] = fmt.Sprintf("全市场成交额%.0f亿/20日均值%.0f亿=%.2f，5日/20日量能趋势=%.2f(综合%.2f)，上证当日涨跌%.2f%%价量配合=%d",
		latest.Amount/1e8, avg/1e8, totalAmtRatio, trendRatio, combinedRatio, idxChangePct(indexBars), pvm)
}

// fillCapital 维度4 资金结构：按可配置优先级依次尝试 北向实时(主) → 两融余额趋势(备) → 成交额放量(兜底)，
// 首个成功源生效，其余回退并标注。另叠加资金集中度（行业成交额 Top3 占比，真实数据）。
// 参考 tradex-hub 取数：北向用同花顺 hsgtApi 当日实时累计净流入（northbound.py），
// 两融用东财数据中心融资余额历史（真实杠杆资金流向）；任一可用即替代原成交额代理。
func (f *Fetcher) fillCapital(ctx context.Context, in *MarketInput) {
	type capSrc struct {
		name     string
		priority int
	}
	var cands []capSrc
	if f.cfg.NorthboundEnabled {
		cands = append(cands, capSrc{"northbound", f.cfg.NorthboundPriority})
	}
	if f.cfg.MarginEnabled {
		cands = append(cands, capSrc{"margin", f.cfg.MarginPriority})
	}
	if f.cfg.VolumeExpansionEnabled {
		cands = append(cands, capSrc{"volume", f.cfg.VolumeExpansionPriority})
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].priority < cands[j].priority })

	northContinuous := 0
	src := "无可用数据源"
sourceLoop:
	for _, c := range cands {
		switch c.name {
		case "northbound":
			if net, ok := f.cachedNorthbound(ctx); ok {
				northContinuous = northboundToContinuous(net)
				src = fmt.Sprintf("北向实时(同花顺hsgtApi): 当日净流入%.0f亿→连续流入口径%d", net, northContinuous)
				break sourceLoop
			}
		case "margin":
			if days, ok := f.cachedMarginTrend(ctx); ok {
				northContinuous = days
				src = fmt.Sprintf("两融余额(东财datacenter): 融资余额连续增减%d天(真实杠杆资金)", days)
				break sourceLoop
			}
		case "volume":
			northContinuous = f.volumeExpansionDays(ctx)
			src = fmt.Sprintf("北向/两融实时源均不可用→成交额连续放量天数代理=%d", northContinuous)
			break sourceLoop
		}
	}

	// 资金集中度：行业成交额 Top3 占比（真实数据）
	concentrate := false
	if sectors, err := f.mkt.GetSectorPerformance(ctx); err == nil && len(sectors) > 0 {
		var total float64
		for _, sec := range sectors {
			if amt, ok := sec["total_amount"].(float64); ok {
				total += amt
			}
		}
		top3 := 0.0
		for i, sec := range sectors {
			if i >= 3 {
				break
			}
			if amt, ok := sec["total_amount"].(float64); ok {
				top3 += amt
			}
		}
		concentrate = total > 0 && top3/total >= 0.4
	}

	in.Dim4Capital = DimCapital{NorthContinuous: northContinuous, CapitalConcentrate: concentrate}
	in.Sources["capital"] = fmt.Sprintf("%s；资金集中度=行业Top3成交额占比≥40%%(%v)", src, concentrate)
}

// cachedNorthbound 取北向实时净流入（5分钟缓存 + 限流），失败返回 ok=false。
func (f *Fetcher) cachedNorthbound(ctx context.Context) (float64, bool) {
	if f.ext == nil {
		return 0, false
	}
	if v, ok := northboundCache.Get(); ok {
		return v, true
	}
	throttleExternal()
	t0 := time.Now()
	net, err := f.ext.FetchNorthboundRealtime(ctx)
	recordSourceStat("北向实时(同花顺hsgtApi)", err == nil, time.Since(t0), err)
	if err != nil {
		log.Printf("[SixDim] 北向实时获取失败(回退两融/成交额代理): %v", err)
		return 0, false
	}
	northboundCache.Set(net)
	return net, true
}

// cachedMarginTrend 取两融余额连续增减天数（1小时缓存 + 限流），失败返回 ok=false。
func (f *Fetcher) cachedMarginTrend(ctx context.Context) (int, bool) {
	if f.ext == nil {
		return 0, false
	}
	if v, ok := marginCache.Get(); ok {
		return marginTrendDays(v), true
	}
	throttleExternal()
	t0 := time.Now()
	margin, err := f.ext.FetchMarginHistory(ctx, 30)
	recordSourceStat("两融余额(东财datacenter)", err == nil, time.Since(t0), err)
	if err != nil {
		log.Printf("[SixDim] 两融余额获取失败(回退成交额代理): %v", err)
		return 0, false
	}
	marginCache.Set(margin)
	return marginTrendDays(margin), true
}

// volumeExpansionDays 全市场成交额连续放量天数（原北向代理，保留作兜底）。
func (f *Fetcher) volumeExpansionDays(ctx context.Context) int {
	n := 0
	if amt, err := f.mkt.GetMarketAmountHistory(ctx, 30); err == nil && len(amt) > 6 {
		for i := len(amt) - 1; i >= 6; i-- {
			var s float64
			for j := i - 5; j < i; j++ {
				s += amt[j].Amount
			}
			if amt[i].Amount > s/5 {
				n++
			} else {
				break
			}
		}
	}
	return n
}

// northboundToContinuous 北向当日净流入(亿) → 连续流入口径 [-5,5]。
// 语义沿用 DimCapital.NorthContinuous 的「资金方向信号」：流入为正、流出为负、量级越大越强。
// 每 20 亿一个台阶，±100 亿封顶 ±5（历史北向单日净流入大致落在 ±100 亿区间）。
func northboundToContinuous(net float64) int {
	return int(clampSigned(net/20, 5))
}

// marginTrendDays 融资余额连续上升(+)或下降(-)天数，封顶 ±5（对应资金连续口径）。
// 数据可能乱序，先按日期升序排，再从最新一天往前数连续同向变动的天数。
func marginTrendDays(margin []port.MarginPoint) int {
	if len(margin) < 2 {
		return 0
	}
	sorted := make([]port.MarginPoint, len(margin))
	copy(sorted, margin)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Date < sorted[j].Date })

	days := 0
	if sorted[len(sorted)-1].Balance >= sorted[len(sorted)-2].Balance {
		for i := len(sorted) - 1; i >= 1; i-- {
			if sorted[i].Balance >= sorted[i-1].Balance {
				days++
			} else {
				break
			}
		}
	} else {
		for i := len(sorted) - 1; i >= 1; i-- {
			if sorted[i].Balance <= sorted[i-1].Balance {
				days--
			} else {
				break
			}
		}
	}
	if days > 5 {
		return 5
	}
	if days < -5 {
		return -5
	}
	return days
}

// fillSentiment 维度5 情绪赚钱效应：按可配置优先级依次尝试 同花顺官方涨停/跌停/炸板池(真实炸板率+连板高度) →
// 东财实时涨停池(push2ex) → DuckDB 全市场收盘统计 → 采样日线代理，首个成功源生效。
// 参考 tradex-hub limit_up_board.py；同花顺官方特色数据为真实炸板池，替代原「日线触板未封」代理。
func (f *Fetcher) fillSentiment(ctx context.Context, in *MarketInput, s sampleMarketStats) {
	type sentiSrc struct {
		name     string
		priority int
	}
	var cands []sentiSrc
	if f.cfg.THSBoardsEnabled {
		cands = append(cands, sentiSrc{"ths_boards", f.cfg.THSBoardsPriority})
	}
	if f.cfg.LimitBoardEnabled {
		cands = append(cands, sentiSrc{"limit_board", f.cfg.LimitBoardPriority})
	}
	if f.cfg.FullMarketStatsEnabled {
		cands = append(cands, sentiSrc{"full_market", f.cfg.FullMarketStatsPriority})
	}
	cands = append(cands, sentiSrc{"sample", 99}) // 采样日线代理永远兜底
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].priority < cands[j].priority })

	for _, c := range cands {
		switch c.name {
		case "ths_boards":
			if snap, ok := f.cachedTHSSentiment(ctx); ok {
				sm := DimSentiment{
					LimitUpCnt:     snap.LimitUpCnt,
					NonStLimitDown: snap.LimitDownCnt,
					BlowUpRate:     snap.BlowUpRate,
				}
				in.Dim5Sentiment = sm
				extra := ""
				if snap.MaxBoardHeight >= 2 {
					extra = fmt.Sprintf("，最高连板高度%d", snap.MaxBoardHeight)
				}
				in.Sources["sentiment"] = fmt.Sprintf("同花顺官方特色数据(实时): 涨停%d/跌停%d/炸板率%.0f%%(炸板%d)%s",
					sm.LimitUpCnt, sm.NonStLimitDown, sm.BlowUpRate*100, snap.BlowUpCnt, extra)
				return
			}
		case "limit_board":
			if board, ok := f.cachedLimitBoard(ctx); ok {
				sm := DimSentiment{
					LimitUpCnt:     board.LimitUpCnt,
					NonStLimitDown: board.LimitDownCnt,
				}
				if board.ZhabanCnt >= 0 && board.LimitUpCnt+board.ZhabanCnt > 0 {
					sm.BlowUpRate = float64(board.ZhabanCnt) / float64(board.LimitUpCnt+board.ZhabanCnt)
				}
				in.Dim5Sentiment = sm
				in.Sources["sentiment"] = fmt.Sprintf("实时涨停池(东财push2ex): 涨停%d/跌停%d/炸板率%.0f%%(炸板%d)",
					sm.LimitUpCnt, sm.NonStLimitDown, sm.BlowUpRate*100, board.ZhabanCnt)
				return
			}
		case "full_market":
			if sm, touched, sealed, ok := f.fullMarketSentiment(ctx); ok {
				in.Dim5Sentiment = sm
				in.Sources["sentiment"] = fmt.Sprintf("全市场真实统计(实时涨停池不可用): 涨停%d/跌停%d/炸板率%.0f%%(触板%d封死%d)",
					sm.LimitUpCnt, sm.NonStLimitDown, sm.BlowUpRate*100, touched, sealed)
				return
			}
		case "sample":
			sm := DimSentiment{LimitUpCnt: s.LimitUp, NonStLimitDown: s.LimitDown}
			if s.TouchedLimitUp > 0 {
				sm.BlowUpRate = float64(s.TouchedLimitUp-s.SealedLimitUp) / float64(s.TouchedLimitUp)
			}
			in.Dim5Sentiment = sm
			in.Sources["sentiment"] = fmt.Sprintf("采样%d只日K(实时/全市场统计均不可用): 涨停%d/跌停%d/炸板率%.0f%%(日线代理)",
				s.N, sm.LimitUpCnt, sm.NonStLimitDown, sm.BlowUpRate*100)
			return
		}
	}
}

// fullMarketSentiment 从 DuckDB 全市场收盘统计取情绪数据；不可用时 ok=false。
func (f *Fetcher) fullMarketSentiment(ctx context.Context) (DimSentiment, int, int, bool) {
	if f.mkt == nil || !f.mkt.HasStockDB() {
		return DimSentiment{}, 0, 0, false
	}
	ls, err := f.mkt.GetMarketLimitStats(ctx)
	if err != nil {
		return DimSentiment{}, 0, 0, false
	}
	sm := DimSentiment{
		LimitUpCnt:     ls.LimitUp,
		NonStLimitDown: ls.LimitDown,
	}
	if ls.TouchedLimitUp > 0 {
		sm.BlowUpRate = float64(ls.BlownUp) / float64(ls.TouchedLimitUp)
	}
	return sm, ls.TouchedLimitUp, ls.SealedLimitUp, true
}

// cachedTHSSentiment 取同花顺官方情绪面（涨停/跌停/炸板池 + 连板天梯，5分钟缓存 + 限流）。
// 未配置官方 API Key 或调用失败时返回 ok=false，由调用方回退东财/代理源。
func (f *Fetcher) cachedTHSSentiment(ctx context.Context) (*port.SentimentSnapshot, bool) {
	if f.ths == nil || !f.ths.Enabled() {
		return nil, false
	}
	if v, ok := thsBoardCache.Get(); ok && v != nil {
		return v, true
	}
	throttleExternal()
	t0 := time.Now()
	snap, err := f.ths.FetchSentimentSnapshot(ctx)
	recordSourceStat("情绪面(同花顺官方涨停/跌停/炸板池)", err == nil, time.Since(t0), err)
	if err != nil {
		log.Printf("[SixDim] 同花顺官方情绪面获取失败(回退东财/代理): %v", err)
		return nil, false
	}
	thsBoardCache.Set(snap)
	return snap, true
}

// cachedLimitBoard 取东财实时涨停/跌停/炸板池（5分钟缓存 + 限流），失败返回 ok=false。
func (f *Fetcher) cachedLimitBoard(ctx context.Context) (port.LimitUpBoard, bool) {
	if f.ext == nil {
		return port.LimitUpBoard{}, false
	}
	if v, ok := limitBoardCache.Get(); ok {
		return v, true
	}
	throttleExternal()
	t0 := time.Now()
	board, err := f.ext.FetchLimitUpBoard(ctx)
	recordSourceStat("涨停池(东财push2ex)", err == nil, time.Since(t0), err)
	if err != nil {
		log.Printf("[SixDim] 实时涨停池获取失败(回退DuckDB统计): %v", err)
		return port.LimitUpBoard{}, false
	}
	limitBoardCache.Set(*board)
	return *board, true
}

// overnightProxy 用上证指数开盘跳空缺口推断隔夜外围对A股开盘的压力：1向好 / 0中性 / -1偏空。
// 低开绝对值越大越偏空，高开绝对值越大越偏多；缺数据返回0（中性，不猜测）。
func overnightProxy(bars []port.KlineBar) int {
	g := overnightGapPct(bars)
	switch {
	case g <= -0.4:
		return -1
	case g >= 0.4:
		return 1
	default:
		return 0
	}
}

// overnightProxyDesc 返回外部维度代理的描述文本（用于 sources 标注）。
func overnightProxyDesc(bars []port.KlineBar) string {
	g := overnightGapPct(bars)
	switch {
	case g <= -0.4:
		return fmt.Sprintf("指数低开%.2f%%(隔夜偏空)", g)
	case g >= 0.4:
		return fmt.Sprintf("指数高开%.2f%%(隔夜偏多)", g)
	case bars == nil || len(bars) < 2:
		return "指数K线不足，中性"
	default:
		return fmt.Sprintf("开盘缺口%.2f%%(中性)", g)
	}
}

// overnightGapPct 上证指数当日开盘相对前收盘的跳空幅度（%）。bars 为 date DESC，bars[0]最新。
func overnightGapPct(bars []port.KlineBar) float64 {
	if len(bars) < 2 || bars[1].Close <= 0 {
		return 0
	}
	return (bars[0].Open - bars[1].Close) / bars[1].Close * 100
}

// ==================== 辅助函数 ====================

func mean(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	var sum float64
	for _, x := range v {
		sum += x
	}
	return sum / float64(len(v))
}

func idxChangePct(bars []port.KlineBar) float64 {
	if len(bars) < 2 || bars[1].Close <= 0 {
		return 0
	}
	return (bars[0].Close - bars[1].Close) / bars[1].Close * 100
}

// ==================== 真实隔夜外围（短期超时 + 短期缓存） ====================

var (
	realOvernightMu  sync.Mutex
	realOvernightVal int
	realOvernightOK  bool
	realOvernightAt  time.Time
	realOvernightSrc string
)

// overnightCacheTTL 真实外围缓存时长：避免每次六维判势都拉外网，提升盘中稳定性
const overnightCacheTTL = 5 * time.Minute

// realOvernightSign 尝试用短期超时从腾讯全球指数拉取真实隔夜外围方向并缓存；
// 1 向好 / -1 偏空 / 0 中性。返回是否取到真实数据及数据来源。失败(含超时)时 ok=false。
func (f *Fetcher) realOvernightSign(ctx context.Context) (int, bool, string) {
	realOvernightMu.Lock()
	defer realOvernightMu.Unlock()
	if realOvernightOK && time.Since(realOvernightAt) < overnightCacheTTL {
		return realOvernightVal, true, realOvernightSrc
	}
	if f.ext == nil {
		return 0, false, ""
	}

	// 短期超时上下文，避免阻塞判势主流程
	fetchCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	t0 := time.Now()
	pts, src, err := f.ext.FetchExternalMarkets(fetchCtx)
	recordSourceStat("隔夜外围(腾讯全球指数)", err == nil, time.Since(t0), err)
	if err != nil {
		log.Printf("[SixDim] 真实隔夜外围获取失败(回退缺口代理): %v", err)
		return 0, false, ""
	}
	sign := 0
	for _, p := range pts {
		// 以富时A50期货为主要风向标，其次道指
		if strings.Contains(p.Code, "CHA50") || strings.Contains(p.Code, "DJI") {
			switch {
			case p.ChangePct <= -0.5:
				sign = -1
			case p.ChangePct >= 0.5:
				sign = 1
			default:
				sign = 0
			}
			if strings.Contains(p.Code, "CHA50") {
				break // 优先A50
			}
		}
	}
	realOvernightVal, realOvernightOK, realOvernightAt, realOvernightSrc = sign, true, time.Now(), src
	return sign, true, src
}

func overnightSign(v int) string {
	switch v {
	case 1:
		return "向好"
	case -1:
		return "偏空"
	default:
		return "中性"
	}
}
