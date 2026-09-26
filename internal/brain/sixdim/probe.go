// 数据测试 + 后台强制刷新：供「设置-数据源-市场六维判势数据源」页面使用。
//
//   - ProbeDataSources：逐个探测配置的全部六维数据源连通性（绕过 TTL 缓存做真实探活），
//     帮助用户诊断「某高优先级源为何总是失败、应否调整优先级或降级」，全程真实请求、严禁伪造。
//   - FlushCaches：清空全部外部源缓存（TTL 缓存 + 隔夜外围），使下次取数强制走真实源。
//   - Detect：执行一次完整判势（fetch + evaluate + 可选落库），供前端「后台强制刷新」调用。
package sixdim

import (
	"context"
	"fmt"
	"time"

	"github.com/quantpilot/quantpilot/internal/brain/port"
)

// SourceProbe 单个六维数据源连通性测试结果。
type SourceProbe struct {
	Key       string `json:"key"`
	Name      string `json:"name"`
	Enabled   bool   `json:"enabled"`
	Priority  int    `json:"priority"`
	Kind      string `json:"kind"` // base=本地依赖 / external=外部实时源 / duckdb=本地兜底源
	Ok        bool   `json:"ok"`
	Message   string `json:"message"`
	LatencyMs int64  `json:"latency_ms"`
}

// ProbeDataSources 逐个探测配置的全部六维数据源（绕过 TTL 缓存真实探活）。
// 外部实时源带 6s 超时 + 最小请求间隔限流，避免探活触发 IP 封禁；本地 DuckDB 源即时返回。
// mkt 未就绪时作为全局失败返回并在各 DuckDB 源上体现，绝不伪造。
func ProbeDataSources(ctx context.Context, mkt port.MarketDataStore, ext port.MarketExternalSources, ths port.THSSentimentSource, cfg SourceConfig) []SourceProbe {
	probes := make([]SourceProbe, 0, 8)

	// 0. 本地依赖就绪性：DuckDB 是否挂载（六维判势根基）
	duckOk := mkt != nil && mkt.HasStockDB()
	probes = append(probes, SourceProbe{
		Key: "duckdb", Name: "本地行情库 stock.duckdb", Kind: "base", Enabled: true,
		Ok:        duckOk,
		Message:   map[bool]string{true: "已挂载（技术/广度/量能/兜底数据均可用）", false: "未挂载，六维判势不可用"}[duckOk],
		LatencyMs: 0,
	})

	// 外部实时源（绕过 TTL 缓存真实探活，带限流 + 6s 超时）
	probeSource(&probes, "northbound", "北向资金（同花顺 hsgtApi）", "external",
		cfg.NorthboundEnabled, cfg.NorthboundPriority, func(ctx context.Context) error {
			_, err := ext.FetchNorthboundRealtime(ctx)
			return err
		})

	probeSource(&probes, "margin", "两融余额（东财数据中心）", "external",
		cfg.MarginEnabled, cfg.MarginPriority, func(ctx context.Context) error {
			_, err := ext.FetchMarginHistory(ctx, 15)
			return err
		})

	probeSource(&probes, "ths_boards", "涨跌停/炸板池（同花顺官方）", "external",
		cfg.THSBoardsEnabled, cfg.THSBoardsPriority, func(ctx context.Context) error {
			if ths == nil || !ths.Enabled() {
				return fmt.Errorf("同花顺官方数据源未配置（请在设置中启用并填写 API Key）")
			}
			_, err := ths.FetchSentimentSnapshot(ctx)
			return err
		})

	probeSource(&probes, "limit_board", "涨跌停/炸板池（东财 push2ex）", "external",
		cfg.LimitBoardEnabled, cfg.LimitBoardPriority, func(ctx context.Context) error {
			_, err := ext.FetchLimitUpBoard(ctx)
			return err
		})

	probeSource(&probes, "overnight", "隔夜外围（腾讯全球指数）", "external",
		cfg.OvernightEnabled, cfg.OvernightPriority, func(ctx context.Context) error {
			_, _, err := ext.FetchExternalMarkets(ctx)
			return err
		})

	// 本地 DuckDB 兜底源（无网络请求，不限流）
	probeSource(&probes, "volume_expansion", "成交额放量（DuckDB 兜底）", "duckdb",
		cfg.VolumeExpansionEnabled, cfg.VolumeExpansionPriority, func(ctx context.Context) error {
			if mkt == nil {
				return fmt.Errorf("DuckDB 不可用")
			}
			_, err := mkt.GetMarketAmountHistory(ctx, 30)
			return err
		})

	probeSource(&probes, "full_market_stats", "全市场收盘统计（DuckDB 兜底）", "duckdb",
		cfg.FullMarketStatsEnabled, cfg.FullMarketStatsPriority, func(ctx context.Context) error {
			if mkt == nil {
				return fmt.Errorf("DuckDB 不可用")
			}
			_, err := mkt.GetMarketLimitStats(ctx)
			return err
		})

	return probes
}

// probeSource 探测单个数据源并追加到结果。external 源带限流与 6s 超时；duckdb 源即时返回。
func probeSource(probes *[]SourceProbe, key, name, kind string, enabled bool, priority int, fn func(ctx context.Context) error) {
	p := SourceProbe{Key: key, Name: name, Kind: kind, Enabled: enabled, Priority: priority}
	if !enabled {
		p.Ok, p.Message = false, "已禁用（未参与判势）"
		*probes = append(*probes, p)
		return
	}
	if kind == "external" {
		throttleExternal()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	t0 := time.Now()
	err := fn(ctx)
	cancel()
	p.LatencyMs = time.Since(t0).Milliseconds()
	if err != nil {
		p.Ok, p.Message = false, "连接失败: "+err.Error()
	} else {
		p.Ok, p.Message = true, "连通正常"
	}
	*probes = append(*probes, p)
}

// FlushCaches 清空六维判势全部外部数据源缓存（TTL 缓存 + 隔夜外围），使下一次取数强制走真实源。
// 用于「后台强制刷新」：穿透缓存拉取最新真实数据。手动触发、非高频，故不加额外限流放宽保护。
func FlushCaches() {
	northboundCache.mu.Lock()
	northboundCache.ok = false
	northboundCache.mu.Unlock()

	thsBoardCache.mu.Lock()
	thsBoardCache.ok = false
	thsBoardCache.mu.Unlock()

	limitBoardCache.mu.Lock()
	limitBoardCache.ok = false
	limitBoardCache.mu.Unlock()

	marginCache.mu.Lock()
	marginCache.ok = false
	marginCache.mu.Unlock()

	realtimeSnapCache.mu.Lock()
	realtimeSnapCache.ok = false
	realtimeSnapCache.mu.Unlock()

	realOvernightMu.Lock()
	realOvernightOK = false
	realOvernightMu.Unlock()
}

// Detect 执行一次完整六维判势（fetch + evaluate + 可选落库），与 market_sixdim_detect 同口径。
// refresh=true 时先清空外部源缓存强制拉取真实最新数据；save=true 时落库 market_sixdim_daily。
// 供「设置-数据源」页面的后台强制刷新离线调用，返回完整判势报告。
func Detect(ctx context.Context, mkt port.MarketDataStore, ext port.MarketExternalSources, ths port.THSSentimentSource, snap port.SnapSource, cfg SourceConfig, save bool, refresh bool) (*MarketReport, error) {
	if mkt == nil || !mkt.HasStockDB() {
		return nil, fmt.Errorf("DuckDB 不可用，无法执行市场六维判势")
	}
	if refresh {
		FlushCaches()
	}
	f := NewFetcherWithConfig(mkt, cfg)
	f.SetExternalSources(ext)
	f.SetTHSSource(ths)
	f.SetSnapSource(snap)
	in, err := f.Fetch(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取六维判势输入数据失败: %w", err)
	}
	rep := Evaluate(in)
	if save {
		if err := SaveReport(ctx, mkt, TradeDateOf(time.Now()), rep); err != nil {
			return nil, fmt.Errorf("六维判势结果落库失败: %w", err)
		}
	}
	return rep, nil
}
