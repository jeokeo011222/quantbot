package backtest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/quantpilot/quantpilot/internal/models"
	"github.com/quantpilot/quantpilot/internal/tdx"
)

// BarData K线数据
type BarData struct {
	Date   string // K线日期 YYYY-MM-DD（供基本面因子按披露日对齐）
	Close  float64
	Open   float64
	High   float64
	Low    float64
	Volume int64
	Amount float64
	// Fin 该K线时点已披露的最新财务数据（无未来函数，由引擎按日期注入；无数据时为 nil）
	Fin *FinancialBar
}

// FinancialBar 单根K线时点可用的财务指标（全部来自 stock.financial_report 真实数据）
type FinancialBar struct {
	ReportDate   string  // 报告期
	AnnDate      string  // 披露日
	TotalRevenue float64 // 营业总收入（元）
	RevenueYOY   float64 // 营收同比(%)
	NetProfit    float64 // 归母净利润（元）
	ProfitYOY    float64 // 净利同比(%)
	NetProfitDed float64 // 扣非归母净利润（元）
	Roe          float64 // 摊薄ROE(%)
	GrossMargin  float64 // 毛利率(%)
	NetMargin    float64 // 净利率(%)
	TotalAssets  float64 // 总资产（元）
	TotalLiab    float64 // 总负债（元）
	DebtRatio    float64 // 资产负债率(%)
	OperCashflow float64 // 经营现金流净额（元）
	TotalShares  float64 // 总股本（股）
	FloatShares  float64 // 流通股本（股）
	EPS          float64 // 每股收益（元）
	BPS          float64 // 每股净资产（元）
}

// toBarData 转换 TDX KlineBar 为 BarData
func toBarData(bars []tdx.KlineBar) []BarData {
	result := make([]BarData, len(bars))
	for i, b := range bars {
		result[i] = BarData{
			Date:   b.Date,
			Close:  b.Close,
			Open:   b.Open,
			High:   b.High,
			Low:    b.Low,
			Volume: b.Volume,
			Amount: b.Amount,
		}
	}
	return result
}

// StrategySignal 策略信号
type StrategySignal int

const (
	SignalNone StrategySignal = 0
	SignalBuy  StrategySignal = 1
	SignalSell StrategySignal = -1
)

// Strategy 策略接口
type Strategy interface {
	// Name 策略名称
	Name() string
	// GenerateSignals 根据 K 线数据生成买卖信号序列
	GenerateSignals(bars []BarData) []StrategySignal
}

// FinancialProvider 财务数据提供者（由 DuckDB 实现）。
// backtest 只依赖该接口，不直接耦合 data 包结构，避免循环依赖并便于注入。
type FinancialProvider interface {
	// FinancialAsOf 返回 symbol 在 asOfDate 时点已披露的最新财务数据；无数据返回 nil。
	// 由回测引擎在生成信号前按每根K线日期对齐注入，保证无未来函数。
	FinancialAsOf(symbol, asOfDate string) (*FinancialBar, error)
}

// FinancialAware 需要基本面财务数据的策略实现该接口（如基本面因子策略）。
// 未实现此接口的策略视为不依赖财务数据，回测时跳过财务数据注入，
// 避免对每一根K线发起一次 DuckDB 查询（这是历史上回测慢、耗时约2h的主因）。
// 借鉴 vectorbt 的按需装载（lazy loading）思想：只在真正消费财报时才注入。
type FinancialAware interface {
	// NeedsFinancialData 返回该策略是否在生成信号时读取 BarData.Fin。
	NeedsFinancialData() bool
}

// strategyNeedsFinancialData 判断策略是否依赖基本面财务数据。
// 仅显式实现 FinancialAware 且返回 true 的策略需要注入；其余一律跳过。
func strategyNeedsFinancialData(st Strategy) bool {
	if a, ok := st.(FinancialAware); ok {
		return a.NeedsFinancialData()
	}
	return false
}

// InjectFinancialData 将 symbol 的财务数据按每根K线的日期对齐注入 bars（无未来函数）。
// 仅填充 BarData.Fin；财务表无数据或查询失败时保持 nil，绝不伪造。返回可用的数据覆盖率。
func InjectFinancialData(bars []BarData, symbol string, provider FinancialProvider) float64 {
	if provider == nil {
		return 0
	}
	covered := 0
	total := 0
	for i := range bars {
		if bars[i].Date == "" {
			continue
		}
		total++
		fin, err := provider.FinancialAsOf(symbol, bars[i].Date)
		if err != nil || fin == nil {
			continue
		}
		bars[i].Fin = fin
		covered++
	}
	if total == 0 {
		return 0
	}
	return float64(covered) / float64(total)
}

// MACrossStrategy 双均线交叉策略
type MACrossStrategy struct {
	FastPeriod int
	SlowPeriod int
}

func (s *MACrossStrategy) Name() string { return "双均线交叉策略" }

func (s *MACrossStrategy) GenerateSignals(bars []BarData) []StrategySignal {
	n := len(bars)
	closes := extractCloses(bars)
	maFast := MA(closes, s.FastPeriod)
	maSlow := MA(closes, s.SlowPeriod)

	signals := make([]StrategySignal, n)
	for i := 1; i < n; i++ {
		if Crossover(maFast, maSlow, i) {
			signals[i] = SignalBuy
		} else if Crossunder(maFast, maSlow, i) {
			signals[i] = SignalSell
		}
	}
	return signals
}

// EXPMAStrategy 指数均线交叉策略
type EXPMAStrategy struct {
	FastPeriod int
	SlowPeriod int
}

func (s *EXPMAStrategy) Name() string { return "EXPMA指数均线策略" }

func (s *EXPMAStrategy) GenerateSignals(bars []BarData) []StrategySignal {
	n := len(bars)
	closes := extractCloses(bars)
	emaFast := EMA(closes, s.FastPeriod)
	emaSlow := EMA(closes, s.SlowPeriod)

	signals := make([]StrategySignal, n)
	for i := 1; i < n; i++ {
		if Crossover(emaFast, emaSlow, i) {
			signals[i] = SignalBuy
		} else if Crossunder(emaFast, emaSlow, i) {
			signals[i] = SignalSell
		}
	}
	return signals
}

// BollingerStrategy 布林带突破策略
type BollingerStrategy struct {
	Period int
	K      float64
}

func (s *BollingerStrategy) Name() string { return "布林带突破策略" }

func (s *BollingerStrategy) GenerateSignals(bars []BarData) []StrategySignal {
	n := len(bars)
	closes := extractCloses(bars)
	upper, _, lower := BOLL(closes, s.Period, s.K)

	signals := make([]StrategySignal, n)
	for i := 1; i < n; i++ {
		if closes[i] <= lower[i] {
			signals[i] = SignalBuy
		} else if closes[i] >= upper[i] {
			signals[i] = SignalSell
		}
	}
	return signals
}

// RSIStrategy RSI 超买超卖策略
type RSIStrategy struct {
	Period     int
	OverSold   float64
	Overbought float64
}

func (s *RSIStrategy) Name() string { return "RSI超买超卖策略" }

func (s *RSIStrategy) GenerateSignals(bars []BarData) []StrategySignal {
	n := len(bars)
	closes := extractCloses(bars)
	rsi := RSI(closes, s.Period)

	signals := make([]StrategySignal, n)
	for i := 1; i < n; i++ {
		if rsi[i] < s.OverSold {
			signals[i] = SignalBuy
		} else if rsi[i] > s.Overbought {
			signals[i] = SignalSell
		}
	}
	return signals
}

// KDJStrategy KDJ 金叉策略
type KDJStrategy struct {
	KPeriod     int
	DPeriod     int
	JOverSold   float64
	JOverbought float64
}

func (s *KDJStrategy) Name() string { return "KDJ金叉策略" }

func (s *KDJStrategy) GenerateSignals(bars []BarData) []StrategySignal {
	n := len(bars)
	closes := extractCloses(bars)
	highs := extractHighs(bars)
	lows := extractLows(bars)
	k, d, j := KDJ(closes, highs, lows, s.KPeriod, 3, 3)

	signals := make([]StrategySignal, n)
	for i := 1; i < n; i++ {
		if Crossover(k, d, i) && j[i] < s.JOverSold {
			signals[i] = SignalBuy
		} else if Crossunder(k, d, i) && j[i] > s.JOverbought {
			signals[i] = SignalSell
		}
	}
	return signals
}

// TurtleStrategy 海龟交易法
type TurtleStrategy struct {
	EntryPeriod int
	ExitPeriod  int
}

func (s *TurtleStrategy) Name() string { return "海龟交易法" }

func (s *TurtleStrategy) GenerateSignals(bars []BarData) []StrategySignal {
	n := len(bars)
	highs := extractHighs(bars)
	lows := extractLows(bars)
	closes := extractCloses(bars)

	entryUpper, _, _ := TAQ(highs, lows, s.EntryPeriod)
	_, _, exitLower := TAQ(highs, lows, s.ExitPeriod)

	signals := make([]StrategySignal, n)
	for i := 1; i < n; i++ {
		if closes[i] >= entryUpper[i] {
			signals[i] = SignalBuy
		} else if closes[i] <= exitLower[i] {
			signals[i] = SignalSell
		}
	}
	return signals
}

// MFIVolumeStrategy MFI 量价反转策略
type MFIVolumeStrategy struct {
	Period     int
	OverSold   float64
	Overbought float64
}

func (s *MFIVolumeStrategy) Name() string { return "MFI量价反转策略" }

func (s *MFIVolumeStrategy) GenerateSignals(bars []BarData) []StrategySignal {
	n := len(bars)
	closes := extractCloses(bars)
	highs := extractHighs(bars)
	lows := extractLows(bars)
	volumes := extractVolumes(bars)
	mfi := MFI(closes, highs, lows, volumes, s.Period)

	signals := make([]StrategySignal, n)
	for i := 1; i < n; i++ {
		if mfi[i] < s.OverSold {
			signals[i] = SignalBuy
		} else if mfi[i] > s.Overbought {
			signals[i] = SignalSell
		}
	}
	return signals
}

// MTMMomentumStrategy MTM 动量策略
type MTMMomentumStrategy struct {
	Period int
}

func (s *MTMMomentumStrategy) Name() string { return "MTM动量策略" }

func (s *MTMMomentumStrategy) GenerateSignals(bars []BarData) []StrategySignal {
	n := len(bars)
	closes := extractCloses(bars)
	mtm, _ := MTM(closes, s.Period)

	zeros := make([]float64, n)
	signals := make([]StrategySignal, n)
	for i := 1; i < n; i++ {
		if Crossover(mtm, zeros, i) {
			signals[i] = SignalBuy
		} else if Crossunder(mtm, zeros, i) {
			signals[i] = SignalSell
		}
	}
	return signals
}

// DMITrendStrategy DMI 趋势跟踪策略
type DMITrendStrategy struct {
	Period int
}

func (s *DMITrendStrategy) Name() string { return "DMI趋势跟踪策略" }

func (s *DMITrendStrategy) GenerateSignals(bars []BarData) []StrategySignal {
	n := len(bars)
	closes := extractCloses(bars)
	highs := extractHighs(bars)
	lows := extractLows(bars)
	pdi, mdi, adx, _ := DMI(closes, highs, lows, s.Period)

	signals := make([]StrategySignal, n)
	for i := 1; i < n; i++ {
		if pdi[i] > mdi[i] && adx[i] > 25 {
			signals[i] = SignalBuy
		} else if pdi[i] < mdi[i] || adx[i] < 20 {
			signals[i] = SignalSell
		}
	}
	return signals
}

// CCIBreakoutStrategy CCI 区间突破策略
type CCIBreakoutStrategy struct {
	Period int
}

func (s *CCIBreakoutStrategy) Name() string { return "CCI区间突破策略" }

func (s *CCIBreakoutStrategy) GenerateSignals(bars []BarData) []StrategySignal {
	n := len(bars)
	closes := extractCloses(bars)
	highs := extractHighs(bars)
	lows := extractLows(bars)
	cci := CCI(closes, highs, lows, s.Period)

	signals := make([]StrategySignal, n)
	for i := 1; i < n; i++ {
		if cci[i] > 100 {
			signals[i] = SignalBuy
		} else if cci[i] < -100 {
			signals[i] = SignalSell
		}
	}
	return signals
}

// TRIXCrossStrategy TRIX 三重平滑策略
type TRIXCrossStrategy struct {
	Period int
}

func (s *TRIXCrossStrategy) Name() string { return "TRIX三重平滑策略" }

func (s *TRIXCrossStrategy) GenerateSignals(bars []BarData) []StrategySignal {
	n := len(bars)
	closes := extractCloses(bars)
	trix, trma := TRIX(closes, s.Period)

	signals := make([]StrategySignal, n)
	for i := 1; i < n; i++ {
		if Crossover(trix, trma, i) {
			signals[i] = SignalBuy
		} else if Crossunder(trix, trma, i) {
			signals[i] = SignalSell
		}
	}
	return signals
}

// ==================== 新增策略：SuperTrend / Aroon / HMA / CMF ====================
// 参考 cinar/indicator 的策略思路，但全部基于我方 BarData/Strategy 接口自实现指标，
// 复用现有回测引擎与「样本池回测」指标刷新，不引入任何外部 AGPL-3.0 依赖。

// SuperTrendStrategy 超级趋势策略：ATR 动态跟踪趋势通道，方向翻转即买卖信号。
// 相比固定均线，它能自适应市场波动并自带动态止损位，适合趋势行情下的持有与止盈防守。
type SuperTrendStrategy struct {
	Period     int     // ATR 周期（默认 10）
	Multiplier float64 // ATR 倍数（默认 3.0）
}

func (s *SuperTrendStrategy) Name() string { return "超级趋势策略" }

func (s *SuperTrendStrategy) GenerateSignals(bars []BarData) []StrategySignal {
	n := len(bars)
	closes := extractCloses(bars)
	highs := extractHighs(bars)
	lows := extractLows(bars)
	period := s.Period
	if period <= 0 {
		period = 10
	}
	mult := s.Multiplier
	if mult <= 0 {
		mult = 3.0
	}
	_, trend := SuperTrend(closes, highs, lows, period, mult)
	signals := make([]StrategySignal, n)
	for i := 1; i < n; i++ {
		if i < period {
			continue // ATR 未稳定，忽略前期占位方向
		}
		if i == period {
			// 预热结束后首个稳定方向：若为多头则在第一根直接建仓（SuperTrend 初始默认多头，
			// 但引擎仅在收到 SignalBuy 时才买入，故此处补上初始进场信号）
			if trend[i] == 1 {
				signals[i] = SignalBuy
			}
			continue
		}
		if trend[i] == 1 && trend[i-1] == -1 {
			signals[i] = SignalBuy // 由空翻多 → 买入
		} else if trend[i] == -1 && trend[i-1] == 1 {
			signals[i] = SignalSell // 由多翻空 → 卖出
		}
	}
	return signals
}

// AroonStrategy 阿隆趋势方向策略：AroonUp 上穿 AroonDown 视为新趋势强度占优（买入），
// 下穿视为趋势转弱（卖出），用于过滤震荡市中的假信号。
type AroonStrategy struct {
	Period int // 回看周期（默认 25）
}

func (s *AroonStrategy) Name() string { return "Aroon趋势方向策略" }

func (s *AroonStrategy) GenerateSignals(bars []BarData) []StrategySignal {
	n := len(bars)
	highs := extractHighs(bars)
	lows := extractLows(bars)
	period := s.Period
	if period <= 0 {
		period = 25
	}
	up, down := Aroon(highs, lows, period)
	signals := make([]StrategySignal, n)
	for i := 1; i < n; i++ {
		if i < period {
			continue // 周期内数据不足，避免预热期伪金叉
		}
		if Crossover(up, down, i) {
			signals[i] = SignalBuy
		} else if Crossunder(up, down, i) {
			signals[i] = SignalSell
		}
	}
	return signals
}

// HMAStrategy 赫尔均线策略：价格上穿 HMA 买入、下穿 HMA 卖出。
// HMA 低滞后、对趋势拐点更灵敏，适合趋势跟踪而减少均线钝化。
type HMAStrategy struct {
	Period int // HMA 周期（默认 20）
}

func (s *HMAStrategy) Name() string { return "HMA赫尔均线策略" }

func (s *HMAStrategy) GenerateSignals(bars []BarData) []StrategySignal {
	n := len(bars)
	closes := extractCloses(bars)
	period := s.Period
	if period <= 0 {
		period = 20
	}
	hma := HMA(closes, period)
	signals := make([]StrategySignal, n)
	for i := 1; i < n; i++ {
		if i < period {
			continue
		}
		if Crossover(closes, hma, i) {
			signals[i] = SignalBuy // 收盘上穿 HMA → 买入
		} else if Crossunder(closes, hma, i) {
			signals[i] = SignalSell // 收盘下穿 HMA → 卖出
		}
	}
	return signals
}

// CMFStrategy 蔡金资金流策略：CMF 上穿 +0.05（资金净流入确认）买入，
// 跌破 −0.05（资金净流出确认）卖出；中间 [−0.05, +0.05] 为观望带，减少噪声。
type CMFStrategy struct {
	Period        int // 周期（默认 20）
	BuyThreshold  float64
	SellThreshold float64
}

func (s *CMFStrategy) Name() string { return "CMF资金流向策略" }

func (s *CMFStrategy) GenerateSignals(bars []BarData) []StrategySignal {
	n := len(bars)
	closes := extractCloses(bars)
	highs := extractHighs(bars)
	lows := extractLows(bars)
	vols := extractVolumesFloat(bars)
	period := s.Period
	if period <= 0 {
		period = 20
	}
	buyTh := s.BuyThreshold
	if buyTh <= 0 {
		buyTh = 0.05
	}
	sellTh := s.SellThreshold
	if sellTh >= 0 {
		sellTh = -0.05
	}
	cmf := CMF(closes, highs, lows, vols, period)
	signals := make([]StrategySignal, n)
	for i := 1; i < n; i++ {
		if i < period-1 {
			continue
		}
		if cmf[i] > buyTh && cmf[i-1] <= buyTh {
			signals[i] = SignalBuy
		} else if cmf[i] < sellTh && cmf[i-1] >= sellTh {
			signals[i] = SignalSell
		}
	}
	return signals
}

// ZhuoyaoStrategy 捉妖大师策略
type ZhuoyaoStrategy struct {
	ShortPeriod int // 短期涨幅周期（默认20）
	MidPeriod   int // 中期涨幅周期（默认60）
	LongPeriod  int // 长期涨幅周期（默认120）
	TrendPeriod int // 趋势EMA周期（默认20）
	// ExitRequireBoth 卖出是否要求短期与趋势同时转弱（默认false=任一触发即卖出）
	ExitRequireBoth bool
}

func (s *ZhuoyaoStrategy) Name() string { return "捉妖大师策略" }

func (s *ZhuoyaoStrategy) GenerateSignals(bars []BarData) []StrategySignal {
	n := len(bars)
	if n < 20 {
		return make([]StrategySignal, n)
	}

	// 自适应周期：长周期(LongPeriod，默认180)需要足够K线热身，短区间回测时
	// 若按配置周期计算，前 longP 根指标全为0、剩余有效区间太少，极易0买入信号，
	// 导致"有信号却0成交"、指标全0。数据不足时按比例收缩各周期，保留原有结构。
	shortP, midP, longP, trendP := s.ShortPeriod, s.MidPeriod, s.LongPeriod, s.TrendPeriod
	if shortP <= 0 {
		shortP = 20
	}
	if midP <= 0 {
		midP = 90
	}
	if longP <= 0 {
		longP = 180
	}
	if trendP <= 0 {
		trendP = 20
	}
	// 长周期最多占 3/5 数据，留下至少 2/5 作为有效信号区间
	maxLong := n * 3 / 5
	if longP > maxLong {
		scale := float64(maxLong) / float64(longP)
		longP = maxLong
		if scale > 0 {
			shortP = int(float64(shortP) * scale)
			midP = int(float64(midP) * scale)
			trendP = int(float64(trendP) * scale)
		}
	}
	// 下限与有序性保护：shortP < midP < longP，且 longP 后仍有可交易区间
	if shortP < 2 {
		shortP = 2
	}
	if midP <= shortP {
		midP = shortP + 1
	}
	if longP <= midP {
		longP = midP + 1
	}
	if trendP < 2 {
		trendP = 2
	}
	if longP > n-20 {
		longP = n - 20
		if longP < 3 {
			longP = 3
		}
		if midP >= longP {
			midP = longP - 1
		}
		if shortP >= midP {
			shortP = midP - 1
		}
	}

	closes := extractCloses(bars)
	_, mid, short, trend := ZHUOYAO(closes, shortP, midP, longP, trendP)

	signals := make([]StrategySignal, n)
	for i := 1; i < n; i++ {
		// 买入：短期走强 + 中期趋势向上 + 短期强于中期（多周期共振）
		if short[i] > 0 && trend[i] > 0 && short[i] > mid[i] {
			signals[i] = SignalBuy
		} else if s.ExitRequireBoth {
			// 卖出：短期与趋势同时转弱（减少被短期回调震出）
			if short[i] < 0 && trend[i] < 0 {
				signals[i] = SignalSell
			}
		} else if short[i] < 0 || trend[i] < 0 {
			signals[i] = SignalSell
		}
	}
	return signals
}

// MlModelStrategy 机器学习策略
// 优先级：纯Go 多模型本地推理（无需Python）→ 纯Go XGBoost 本地推理 → Python XGBoost 服务 → 本地评分模型
type MlModelStrategy struct {
	MLServiceURL string // ML服务地址，默认 http://127.0.0.1:8766
	ModelName    string // 兼容：单模型名称（默认 xgboost_astock_v1）
	// ModelDir 模型目录覆盖（非空时优先使用，跳过 resolveXGBModelDir 探测）。
	// 便于测试/嵌入场景显式指定模型位置（如 build/bin/models）。
	ModelDir string
	// 多模型集成配置
	ModelNames     []string // 参与集成的模型名列表（从 models/ 目录加载）
	EnsembleMethod string   // voting=多数投票 / probability=概率平均；空=单模型
	// 本地评分回退参数
	MAWeight      float64
	RSIWeight     float64
	MACDWeight    float64
	VolumeWeight  float64
	KDJWeight     float64
	BuyThreshold  float64
	SellThreshold float64
	// 纯Go 多模型推理缓存
	ensembleModels []models.MLModel
	ensembleScaler []*models.StandardScaler
	ensembleReady  bool
	ensembleErr    error
	// 纯Go XGBoost 推理缓存
	xgbModel   *models.XGBoostModel
	xgbScaler  *models.StandardScaler
	xgbReady   bool
	xgbChecked bool
	xgbErr     error
	// 缓存
	mlAvailable   bool
	lastCheckTime time.Time
}

func (s *MlModelStrategy) Name() string { return "机器学习策略" }

// GenerateSignals 生成交易信号
func (s *MlModelStrategy) GenerateSignals(bars []BarData) []StrategySignal {
	n := len(bars)
	if n < 20 {
		log.Printf("[MlModelStrategy] Not enough bars: %d, need at least 20", n)
		return make([]StrategySignal, n)
	}

	// 1. 优先使用纯Go 多模型集成本地推理（配置了多模型且加载成功）
	if len(s.ModelNames) > 0 {
		if !s.ensembleReady && s.ensembleErr == nil {
			s.initEnsemble()
		}
		if s.ensembleReady {
			signals, err := s.predictViaEnsemble(bars)
			if err == nil && len(signals) == n {
				return signals
			}
			log.Printf("[MlModelStrategy] 多模型集成推理失败: %v, 继续回退", err)
		}
	}

	// 2. 纯Go XGBoost 本地推理（模型文件随程序分发，无需Python）
	if !s.xgbChecked {
		s.xgbChecked = true
		s.xgbReady, s.xgbErr = s.initGoXGBoost()
		if s.xgbReady {
			log.Printf("[MlModelStrategy] 纯Go XGBoost 本地推理已启用")
		} else if s.xgbErr != nil {
			log.Printf("[MlModelStrategy] 纯Go XGBoost 不可用，回退其它方式: %v", s.xgbErr)
		}
	}
	if s.xgbReady {
		signals, err := s.predictViaGoXGBoost(bars)
		if err == nil && len(signals) == n {
			return signals
		}
		log.Printf("[MlModelStrategy] Go XGBoost 推理失败: %v, 继续回退", err)
	}

	// 3. 检查ML服务是否可用（每30秒检查一次）
	if time.Since(s.lastCheckTime) > 30*time.Second {
		s.mlAvailable = s.checkMLService()
		s.lastCheckTime = time.Now()
	}

	// 4. 如果ML服务可用，调用ML服务获取信号
	if s.mlAvailable {
		signals, err := s.predictViaMLService(bars)
		if err == nil && len(signals) == n {
			return signals
		}
		log.Printf("[MlModelStrategy] ML service prediction failed: %v, falling back to local model", err)
	}

	// 5. 回退到本地评分模型
	return s.generateLocalSignals(bars)
}

// initEnsemble 加载多模型（读取各模型 _info.json 自动识别类型），跳过缺失/失败的模型
func (s *MlModelStrategy) initEnsemble() {
	modelDir, err := s.resolveModelDir()
	if err != nil {
		s.ensembleErr = err
		return
	}
	for _, name := range s.ModelNames {
		if name == "" {
			continue
		}
		m, err := models.LoadMLModelByName(modelDir, name)
		if err != nil {
			log.Printf("[MlModelStrategy] 加载模型 %s 失败（跳过）: %v", name, err)
			continue
		}
		sc, err := models.LoadStandardScalerForModel(modelDir, name)
		if err != nil {
			log.Printf("[MlModelStrategy] 加载模型 %s 的 scaler 失败（跳过）: %v", name, err)
			continue
		}
		s.ensembleModels = append(s.ensembleModels, m)
		s.ensembleScaler = append(s.ensembleScaler, sc)
	}
	if len(s.ensembleModels) == 0 {
		s.ensembleErr = fmt.Errorf("多模型全部加载失败")
		log.Printf("[MlModelStrategy] 多模型全部加载失败: %v", s.ensembleErr)
		return
	}
	s.ensembleReady = true
	names := strings.Join(s.ModelNames, ",")
	method := s.EnsembleMethod
	if method == "" {
		method = "voting"
	}
	log.Printf("[MlModelStrategy] 多模型集成已启用（%d 个模型: %s, 方法=%s）", len(s.ensembleModels), names, method)
}

// predictViaEnsemble 使用多模型投票/概率平均推理预测信号
func (s *MlModelStrategy) predictViaEnsemble(bars []BarData) ([]StrategySignal, error) {
	n := len(bars)
	if n < 60 {
		return nil, fmt.Errorf("bars too few for ML features: %d (need >=60)", n)
	}
	ohlc := models.XGBOHLCV{
		Open:   make([]float64, n),
		High:   make([]float64, n),
		Low:    make([]float64, n),
		Close:  make([]float64, n),
		Volume: make([]float64, n),
		Amount: make([]float64, n),
	}
	for i, b := range bars {
		ohlc.Open[i] = b.Open
		ohlc.High[i] = b.High
		ohlc.Low[i] = b.Low
		ohlc.Close[i] = b.Close
		ohlc.Volume[i] = float64(b.Volume)
		ohlc.Amount[i] = b.Amount
	}
	feats := models.ComputeXGBFeatures(ohlc)

	signals := make([]StrategySignal, n)
	numModels := len(s.ensembleModels)
	for i := 0; i < n; i++ {
		var cls int
		if s.EnsembleMethod == "probability" {
			// 概率平均
			acc := make([]float64, 3)
			for mi := 0; mi < numModels; mi++ {
				scaled := s.ensembleScaler[mi].Transform(feats[i])
				proba := s.ensembleModels[mi].PredictProba(scaled)
				for k := 0; k < 3 && k < len(proba); k++ {
					acc[k] += proba[k]
				}
			}
			cls = argmax(acc)
		} else {
			// 多数投票
			votes := [3]int{}
			for mi := 0; mi < numModels; mi++ {
				scaled := s.ensembleScaler[mi].Transform(feats[i])
				c := s.ensembleModels[mi].PredictClass(scaled)
				if c >= 0 && c < 3 {
					votes[c]++
				}
			}
			best, cnt := 0, votes[0]
			for k := 1; k < 3; k++ {
				if votes[k] > cnt {
					best, cnt = k, votes[k]
				}
			}
			// 平票（无绝对多数）视为观望
			majority := cnt > numModels/2
			if !majority {
				cls = 1
			} else {
				cls = best
			}
		}
		// 类别索引 0=卖出 1=观望 2=买入（对应训练标签 -1/0/1）
		switch cls {
		case 2:
			signals[i] = SignalBuy
		case 0:
			signals[i] = SignalSell
		default:
			signals[i] = SignalNone
		}
	}
	return signals, nil
}

// argmax 返回最大值的下标
func argmax(vals []float64) int {
	best, idx := vals[0], 0
	for k := 1; k < len(vals); k++ {
		if vals[k] > best {
			best, idx = vals[k], k
		}
	}
	return idx
}

// checkMLService 检查ML服务是否可用
func (s *MlModelStrategy) checkMLService() bool {
	url := s.MLServiceURL
	if url == "" {
		url = "http://127.0.0.1:8766"
	}

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(url + "/health")
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	return resp.StatusCode == 200
}

// predictViaMLService 通过ML服务预测信号
func (s *MlModelStrategy) predictViaMLService(bars []BarData) ([]StrategySignal, error) {
	url := s.MLServiceURL
	if url == "" {
		url = "http://127.0.0.1:8766"
	}

	// 构建请求数据
	klineData := make([]map[string]interface{}, len(bars))
	for i, bar := range bars {
		klineData[i] = map[string]interface{}{
			"open":   bar.Open,
			"high":   bar.High,
			"low":    bar.Low,
			"close":  bar.Close,
			"volume": int64(bar.Volume),
		}
	}

	modelName := s.ModelName
	if modelName == "" {
		modelName = "xgboost_astock_v1"
	}

	reqBody := map[string]interface{}{
		"model_name": modelName,
		"data":       klineData,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// 发送请求
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(url+"/api/predict", "application/json", bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to call ML service: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("ML service returned status %d", resp.StatusCode)
	}

	// 解析响应
	var result struct {
		Status  string `json:"status"`
		Signals []int  `json:"signals"`
		Error   string `json:"error"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	if result.Error != "" {
		return nil, fmt.Errorf("ML service error: %s", result.Error)
	}

	// 转换信号
	// XGBoost 返回类别索引 0/1/2，对应原始标签 sell(-1)/hold(0)/buy(1)
	signals := make([]StrategySignal, len(bars))
	for i, sig := range result.Signals {
		switch sig {
		case 2:
			signals[i] = SignalBuy
		case 0:
			signals[i] = SignalSell
		default:
			signals[i] = SignalNone
		}
	}

	return signals, nil
}

// initGoXGBoost 初始化纯Go XGBoost 本地推理（模型文件随程序分发）
func (s *MlModelStrategy) initGoXGBoost() (bool, error) {
	modelDir, err := s.resolveModelDir()
	if err != nil {
		return false, err
	}
	modelName := s.ModelName
	if modelName == "" {
		modelName = "xgboost_astock_v1"
	}
	xgbModel, err := models.LoadXGBoostModel(filepath.Join(modelDir, modelName+".json"))
	if err != nil {
		return false, err
	}
	scaler, err := models.LoadStandardScalerJSON(filepath.Join(modelDir, modelName+"_scaler.json"))
	if err != nil {
		return false, err
	}
	s.xgbModel = xgbModel
	s.xgbScaler = scaler
	return true, nil
}

// predictViaGoXGBoost 使用纯Go XGBoost 推理预测信号
func (s *MlModelStrategy) predictViaGoXGBoost(bars []BarData) ([]StrategySignal, error) {
	n := len(bars)
	if n < 60 {
		return nil, fmt.Errorf("bars too few for XGBoost features: %d (need >=60)", n)
	}
	ohlc := models.XGBOHLCV{
		Open:   make([]float64, n),
		High:   make([]float64, n),
		Low:    make([]float64, n),
		Close:  make([]float64, n),
		Volume: make([]float64, n),
		Amount: make([]float64, n),
	}
	for i, b := range bars {
		ohlc.Open[i] = b.Open
		ohlc.High[i] = b.High
		ohlc.Low[i] = b.Low
		ohlc.Close[i] = b.Close
		ohlc.Volume[i] = float64(b.Volume)
		ohlc.Amount[i] = b.Amount
	}
	feats := models.ComputeXGBFeatures(ohlc)
	signals := make([]StrategySignal, n)
	for i := 0; i < n; i++ {
		scaled := s.xgbScaler.Transform(feats[i])
		// 类别索引 0=卖出 1=观望 2=买入（对应训练标签 -1/0/1）
		switch s.xgbModel.PredictClass(scaled) {
		case 2:
			signals[i] = SignalBuy
		case 0:
			signals[i] = SignalSell
		default:
			signals[i] = SignalNone
		}
	}
	return signals, nil
}

// resolveModelDir 定位机器学习模型目录：显式 ModelDir 优先，否则走 resolveXGBModelDir 探测
func (s *MlModelStrategy) resolveModelDir() (string, error) {
	if s.ModelDir != "" {
		if info, err := os.Stat(s.ModelDir); err == nil && info.IsDir() {
			return filepath.Clean(s.ModelDir), nil
		}
		return "", fmt.Errorf("ModelDir 不存在: %s", s.ModelDir)
	}
	return resolveXGBModelDir()
}

// resolveXGBModelDir 定位机器学习模型目录
// 顺序：exeDir/models → exeDir/data/models → exeDir/../internal/models → cwd/internal/models
func resolveXGBModelDir() (string, error) {
	candidates := make([]string, 0, 4)
	exePath, err := os.Executable()
	if err == nil {
		exeDir := filepath.Dir(exePath)
		candidates = append(candidates,
			filepath.Join(exeDir, "models"),
			filepath.Join(exeDir, "data", "models"),
			filepath.Join(exeDir, "..", "internal", "models"),
		)
	}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(cwd, "internal", "models"))
	}
	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil && info.IsDir() {
			return filepath.Clean(c), nil
		}
	}
	return "", fmt.Errorf("未找到机器学习模型目录（models/），已检查: %v", candidates)
}

// generateLocalSignals 本地评分模型（回退方案）
func (s *MlModelStrategy) generateLocalSignals(bars []BarData) []StrategySignal {
	n := len(bars)
	if n < 30 {
		log.Printf("[MlModelStrategy] Not enough bars for local signals: %d", n)
		return make([]StrategySignal, n)
	}

	closes := extractCloses(bars)
	highs := extractHighs(bars)
	lows := extractLows(bars)
	volumes := extractVolumesFloat(bars)

	ma5 := MA(closes, 5)
	ma20 := MA(closes, 20)
	ma60 := MA(closes, 60)
	rsi := RSI(closes, 14)
	macd, signal, _ := MACD(closes, 12, 26, 9)
	k, d, j := KDJ(closes, highs, lows, 9, 3, 3)
	volMA := MA(volumes, 20)

	// 设置默认参数
	if s.MAWeight == 0 {
		s.MAWeight = 0.25
	}
	if s.RSIWeight == 0 {
		s.RSIWeight = 0.25
	}
	if s.MACDWeight == 0 {
		s.MACDWeight = 0.25
	}
	if s.VolumeWeight == 0 {
		s.VolumeWeight = 0.15
	}
	if s.KDJWeight == 0 {
		s.KDJWeight = 0.10
	}
	// 降低阈值，使策略更容易触发交易
	if s.BuyThreshold == 0 {
		s.BuyThreshold = 0.55
	}
	if s.SellThreshold == 0 {
		s.SellThreshold = 0.45
	}

	totalWeight := s.MAWeight + s.RSIWeight + s.MACDWeight + s.VolumeWeight + s.KDJWeight

	signals := make([]StrategySignal, n)
	minBarsForCalc := 30
	if n < minBarsForCalc {
		minBarsForCalc = n
	}

	for i := minBarsForCalc; i < n; i++ {

		maScore := 0.5
		if ma5[i] > ma20[i] && ma20[i] > ma60[i] {
			maScore = 0.8
		} else if ma5[i] < ma20[i] && ma20[i] < ma60[i] {
			maScore = 0.2
		} else if closes[i] > ma20[i] {
			maScore = 0.6
		} else {
			maScore = 0.4
		}

		rsiScore := 0.5
		if rsi[i] < 30 {
			rsiScore = 0.8
		} else if rsi[i] > 70 {
			rsiScore = 0.2
		} else if rsi[i] < 50 {
			rsiScore = 0.55
		} else {
			rsiScore = 0.45
		}

		macdScore := 0.5
		if macd[i] > signal[i] && macd[i] > 0 {
			macdScore = 0.8
		} else if macd[i] < signal[i] && macd[i] < 0 {
			macdScore = 0.2
		} else if macd[i] > signal[i] {
			macdScore = 0.6
		} else {
			macdScore = 0.4
		}

		volumeScore := 0.5
		if volMA[i] > 0 {
			volRatio := volumes[i] / volMA[i]
			if volRatio > 2.0 {
				if closes[i] > closes[i-1] {
					volumeScore = 0.85
				} else {
					volumeScore = 0.15
				}
			} else if volRatio > 1.5 {
				if closes[i] > closes[i-1] {
					volumeScore = 0.7
				} else {
					volumeScore = 0.3
				}
			}
		}

		kdjScore := 0.5
		if k[i] < 20 && d[i] < 20 {
			kdjScore = 0.8
		} else if k[i] > 80 && d[i] > 80 {
			kdjScore = 0.2
		} else if j[i] < 0 {
			kdjScore = 0.85
		} else if j[i] > 100 {
			kdjScore = 0.15
		}

		totalScore := (s.MAWeight*maScore +
			s.RSIWeight*rsiScore +
			s.MACDWeight*macdScore +
			s.VolumeWeight*volumeScore +
			s.KDJWeight*kdjScore) / totalWeight

		if totalScore >= s.BuyThreshold {
			signals[i] = SignalBuy
		} else if totalScore <= s.SellThreshold {
			signals[i] = SignalSell
		}
	}

	return signals
}

// extractCloses 提取收盘价
func extractCloses(bars []BarData) []float64 {
	result := make([]float64, len(bars))
	for i, b := range bars {
		result[i] = b.Close
	}
	return result
}

// extractHighs 提取最高价
func extractHighs(bars []BarData) []float64 {
	result := make([]float64, len(bars))
	for i, b := range bars {
		result[i] = b.High
	}
	return result
}

// extractLows 提取最低价
func extractLows(bars []BarData) []float64 {
	result := make([]float64, len(bars))
	for i, b := range bars {
		result[i] = b.Low
	}
	return result
}

// extractVolumes 提取成交量
func extractVolumes(bars []BarData) []int64 {
	result := make([]int64, len(bars))
	for i, b := range bars {
		result[i] = b.Volume
	}
	return result
}

// extractVolumesFloat 提取成交量(float64)
func extractVolumesFloat(bars []BarData) []float64 {
	result := make([]float64, len(bars))
	for i, b := range bars {
		result[i] = float64(b.Volume)
	}
	return result
}

// GetStrategyByType 根据类型获取策略实例
// 每种策略映射到不同的算法实现，确保产生差异化交易信号
func GetStrategyByType(strategyType string) Strategy {
	switch strategyType {
	case "ma_cross":
		log.Printf("[GetStrategyByType] strategy_type=%s -> MACrossStrategy", strategyType)
		return &MACrossStrategy{FastPeriod: 15, SlowPeriod: 20}
	case "expma_cross":
		log.Printf("[GetStrategyByType] strategy_type=%s -> EXPMAStrategy", strategyType)
		return &EXPMAStrategy{FastPeriod: 12, SlowPeriod: 50}
	case "bollinger_breakout":
		log.Printf("[GetStrategyByType] strategy_type=%s -> BollingerStrategy", strategyType)
		return &BollingerStrategy{Period: 20, K: 2.0}
	case "rsi_reversal":
		log.Printf("[GetStrategyByType] strategy_type=%s -> RSIStrategy", strategyType)
		return &RSIStrategy{Period: 14, OverSold: 30, Overbought: 70}
	case "kdj_golden":
		log.Printf("[GetStrategyByType] strategy_type=%s -> KDJStrategy", strategyType)
		return &KDJStrategy{KPeriod: 9, DPeriod: 3, JOverSold: 30, JOverbought: 70}
	case "turtle_breakout":
		log.Printf("[GetStrategyByType] strategy_type=%s -> TurtleStrategy", strategyType)
		return &TurtleStrategy{EntryPeriod: 40, ExitPeriod: 5}
	case "mfi_volume":
		log.Printf("[GetStrategyByType] strategy_type=%s -> MFIVolumeStrategy", strategyType)
		return &MFIVolumeStrategy{Period: 14, OverSold: 30, Overbought: 70}
	case "mtm_momentum":
		log.Printf("[GetStrategyByType] strategy_type=%s -> MTMMomentumStrategy", strategyType)
		return &MTMMomentumStrategy{Period: 30}
	case "dmi_trend":
		log.Printf("[GetStrategyByType] strategy_type=%s -> DMITrendStrategy", strategyType)
		return &DMITrendStrategy{Period: 14}
	case "cci_breakout":
		log.Printf("[GetStrategyByType] strategy_type=%s -> CCIBreakoutStrategy", strategyType)
		return &CCIBreakoutStrategy{Period: 14}
	case "trix_cross":
		log.Printf("[GetStrategyByType] strategy_type=%s -> TRIXCrossStrategy", strategyType)
		return &TRIXCrossStrategy{Period: 12}
	case "zhuoyao_momentum":
		log.Printf("[GetStrategyByType] strategy_type=%s -> ZhuoyaoStrategy", strategyType)
		return &ZhuoyaoStrategy{ShortPeriod: 20, MidPeriod: 90, LongPeriod: 180, TrendPeriod: 20}
	case "ml_model":
		log.Printf("[GetStrategyByType] strategy_type=%s -> MlModelStrategy (ML-based)", strategyType)
		return &MlModelStrategy{
			MLServiceURL:   "http://127.0.0.1:8766",
			ModelName:      "xgboost_astock_v1",
			ModelNames:     []string{"xgboost_astock_v1", "astock_lgbm_v1", "astock_rf_v1", "astock_logistic_v1", "astock_mlp_v1"},
			EnsembleMethod: "probability",
			MAWeight:       0.20,
			RSIWeight:      0.25,
			MACDWeight:     0.25,
			VolumeWeight:   0.15,
			KDJWeight:      0.15,
			BuyThreshold:   0.5,
			SellThreshold:  0.3,
		}
	case "super_trend":
		log.Printf("[GetStrategyByType] strategy_type=%s -> SuperTrendStrategy", strategyType)
		return &SuperTrendStrategy{Period: 10, Multiplier: 3.0}
	case "aroon":
		log.Printf("[GetStrategyByType] strategy_type=%s -> AroonStrategy", strategyType)
		return &AroonStrategy{Period: 25}
	case "hma":
		log.Printf("[GetStrategyByType] strategy_type=%s -> HMAStrategy", strategyType)
		return &HMAStrategy{Period: 20}
	case "cmf":
		log.Printf("[GetStrategyByType] strategy_type=%s -> CMFStrategy", strategyType)
		return &CMFStrategy{Period: 20, BuyThreshold: 0.05, SellThreshold: -0.05}
	// 兼容旧版策略类型 - 每种映射到不同的实现（保留映射但不再默认播种，避免重复策略）
	case "momentum":
		log.Printf("[GetStrategyByType] strategy_type=%s (legacy) -> MTMMomentumStrategy", strategyType)
		return &MTMMomentumStrategy{Period: 10}
	case "mean_reversion":
		log.Printf("[GetStrategyByType] strategy_type=%s (legacy) -> CCIBreakoutStrategy", strategyType)
		return &CCIBreakoutStrategy{Period: 14}
	case "quality":
		log.Printf("[GetStrategyByType] strategy_type=%s (legacy) -> RSIStrategy", strategyType)
		return &RSIStrategy{Period: 8, OverSold: 40, Overbought: 65}
	default:
		// 未知策略类型不得静默回退到均线策略——那会让用户误以为回测的是自己选择的策略。
		// 直接返回 nil，由调用方视为错误，避免「回测了另一个策略」的误导。
		log.Printf("[GetStrategyByType] 未知策略类型=%s (返回nil交由调用方报错)", strategyType)
		return nil
	}
}

// paramCombos 生成命名参数网格的笛卡尔积：每个参数名对应一组候选值（数字），
// 返回所有组合（map[string]float64），键按字典序保证输出顺序稳定可测。
// 这是 vectorbt bruteforce（参数网格扫描）的 Go 版最小实现——一次跑遍所有参数组合。
func paramCombos(spec map[string][]float64) []map[string]float64 {
	keys := make([]string, 0, len(spec))
	for k := range spec {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]map[string]float64, 0)
	cur := make(map[string]float64, len(keys))
	var rec func(idx int)
	rec = func(idx int) {
		if idx == len(keys) {
			cp := make(map[string]float64, len(cur))
			for k, v := range cur {
				cp[k] = v
			}
			out = append(out, cp)
			return
		}
		k := keys[idx]
		for _, v := range spec[k] {
			cur[k] = v
			rec(idx + 1)
		}
		delete(cur, keys[idx])
	}
	rec(0)
	return out
}

// DefaultParamGrid 各策略的默认参数网格（供参数扫描选优）。
// 只覆盖参数化类型较直观的策略；其余策略返回 nil 表示不支持网格扫描。
// 网格数值均取常见量级，用户无需配置即可跑，保持"全场默认也可用"。
func DefaultParamGrid(strategyType string) []map[string]float64 {
	switch strategyType {
	case "ma_cross":
		all := paramCombos(map[string][]float64{
			"FastPeriod": {5, 10, 15, 20},
			"SlowPeriod": {20, 30, 40, 60},
		})
		out := make([]map[string]float64, 0, len(all))
		for _, c := range all {
			if c["SlowPeriod"] > c["FastPeriod"] { // 双均线必须慢>快，否则无交叉意义
				out = append(out, c)
			}
		}
		return out
	case "bollinger_breakout":
		return paramCombos(map[string][]float64{
			"Period": {10, 20, 30, 40},
			"K":      {1.5, 2.0, 2.5},
		})
	case "rsi_reversal":
		return paramCombos(map[string][]float64{
			"Period":     {7, 14, 21},
			"OverSold":   {20, 30},
			"Overbought": {70, 80},
		})
	case "kdj_golden":
		return paramCombos(map[string][]float64{
			"KPeriod":     {5, 9, 14},
			"JOverSold":   {20, 30},
			"JOverbought": {70, 80},
		})
	case "turtle_breakout":
		return paramCombos(map[string][]float64{
			"EntryPeriod": {20, 40, 55},
			"ExitPeriod":  {5, 10},
		})
	}
	return nil
}

// NewStrategy 按策略类型 + 运行时参数构造策略实例（参数网格扫描的最小必要件）。
// params 中缺失的键采用该策略的默认参数；未知类型返回错误（不静默回退）。
func NewStrategy(strategyType string, params map[string]float64) (Strategy, error) {
	ip := func(key string, def int) int {
		if v, ok := params[key]; ok {
			return int(v)
		}
		return def
	}
	fp := func(key string, def float64) float64 {
		if v, ok := params[key]; ok {
			return v
		}
		return def
	}
	switch strategyType {
	case "ma_cross":
		fast, slow := ip("FastPeriod", 15), ip("SlowPeriod", 20)
		if slow <= fast {
			return nil, fmt.Errorf("ma_cross 参数非法: 慢周期(%d)必须大于快周期(%d)", slow, fast)
		}
		return &MACrossStrategy{FastPeriod: fast, SlowPeriod: slow}, nil
	case "expma_cross":
		fast, slow := ip("FastPeriod", 12), ip("SlowPeriod", 50)
		if slow <= fast {
			return nil, fmt.Errorf("expma_cross 参数非法: 慢周期(%d)必须大于快周期(%d)", slow, fast)
		}
		return &EXPMAStrategy{FastPeriod: fast, SlowPeriod: slow}, nil
	case "bollinger_breakout":
		return &BollingerStrategy{Period: ip("Period", 20), K: fp("K", 2.0)}, nil
	case "rsi_reversal":
		return &RSIStrategy{Period: ip("Period", 14), OverSold: fp("OverSold", 30), Overbought: fp("Overbought", 70)}, nil
	case "kdj_golden":
		return &KDJStrategy{KPeriod: ip("KPeriod", 9), DPeriod: ip("DPeriod", 3), JOverSold: fp("JOverSold", 30), JOverbought: fp("JOverbought", 70)}, nil
	case "turtle_breakout":
		return &TurtleStrategy{EntryPeriod: ip("EntryPeriod", 40), ExitPeriod: ip("ExitPeriod", 5)}, nil
	case "mfi_volume":
		return &MFIVolumeStrategy{Period: ip("Period", 14), OverSold: fp("OverSold", 30), Overbought: fp("Overbought", 70)}, nil
	case "mtm_momentum":
		return &MTMMomentumStrategy{Period: ip("Period", 30)}, nil
	case "dmi_trend":
		return &DMITrendStrategy{Period: ip("Period", 14)}, nil
	case "cci_breakout":
		return &CCIBreakoutStrategy{Period: ip("Period", 14)}, nil
	case "trix_cross":
		return &TRIXCrossStrategy{Period: ip("Period", 12)}, nil
	case "super_trend":
		return &SuperTrendStrategy{Period: ip("Period", 10), Multiplier: fp("Multiplier", 3.0)}, nil
	case "aroon":
		return &AroonStrategy{Period: ip("Period", 25)}, nil
	case "hma":
		return &HMAStrategy{Period: ip("Period", 20)}, nil
	case "cmf":
		return &CMFStrategy{Period: ip("Period", 20), BuyThreshold: fp("BuyThreshold", 0.05), SellThreshold: fp("SellThreshold", -0.05)}, nil
	}
	return nil, fmt.Errorf("未知策略类型 %s，不支持参数网格扫描", strategyType)
}

// BuiltinStrategyMeta 内置策略元信息（策略表单一数据源）
type BuiltinStrategyMeta struct {
	Name          string
	StrategyType  string
	Description   string
	ConfigJSON    string
	StopLossPct   float64
	TakeProfitPct float64
}

// BuiltinStrategies 内置策略目录
// 与 GetStrategyByType 严格一一对应，是策略表的唯一数据来源（统一策略表，从底层规范数据）。
// 不再携带任何硬编码指标，指标一律由真实回测写入（见 strategy.Service.RefreshStrategyMetrics）。
var BuiltinStrategies = []BuiltinStrategyMeta{
	{
		Name: "双均线交叉策略", StrategyType: "ma_cross",
		Description: "MA15 上穿 MA20（金叉）全仓买入，MA15 下穿 MA20（死叉）全部卖出。经典趋势跟踪策略，适合单边行情。",
		ConfigJSON:  `{"capital":100000,"max_position":10,"stop_loss":0.05,"take_profit":0.20,"fast_ma":15,"slow_ma":20}`,
		StopLossPct: 0.05, TakeProfitPct: 0.20,
	},
	{
		Name: "EXPMA指数均线策略", StrategyType: "expma_cross",
		Description: "EMA12 上穿 EMA50 买入，下穿卖出。比简单均线更灵敏的趋势跟踪策略。",
		ConfigJSON:  `{"capital":100000,"max_position":10,"stop_loss":0.05,"take_profit":0.20,"fast_ema":12,"slow_ema":50}`,
		StopLossPct: 0.05, TakeProfitPct: 0.20,
	},
	{
		Name: "布林带突破策略", StrategyType: "bollinger_breakout",
		Description: "收盘价跌破下轨买入，突破上轨卖出。适合横盘震荡行情的反转交易。",
		ConfigJSON:  `{"capital":100000,"max_position":10,"stop_loss":0.05,"take_profit":0.20,"period":20,"std_dev":2}`,
		StopLossPct: 0.05, TakeProfitPct: 0.20,
	},
	{
		Name: "RSI超买超卖策略", StrategyType: "rsi_reversal",
		Description: "RSI < 30 超卖买入，RSI > 70 超买卖出。适合震荡市的波段操作。",
		ConfigJSON:  `{"capital":100000,"max_position":10,"stop_loss":0.05,"take_profit":0.20,"period":14,"oversold":30,"overbought":70}`,
		StopLossPct: 0.05, TakeProfitPct: 0.20,
	},
	{
		Name: "KDJ金叉策略", StrategyType: "kdj_golden",
		Description: "K上穿D且J<30（低位金叉）买入，K下穿D且J>70（高位死叉）卖出。适合短线震荡。",
		ConfigJSON:  `{"capital":100000,"max_position":10,"stop_loss":0.05,"take_profit":0.20,"k_period":9,"d_period":3,"j_oversold":30,"j_overbought":70}`,
		StopLossPct: 0.05, TakeProfitPct: 0.20,
	},
	{
		Name: "海龟交易法", StrategyType: "turtle_breakout",
		Description: "收盘价突破40日高点买入，跌破5日低点卖出。经典趋势突破策略。",
		ConfigJSON:  `{"capital":100000,"max_position":10,"stop_loss":0.05,"take_profit":0.20,"entry_period":40,"exit_period":5}`,
		StopLossPct: 0.05, TakeProfitPct: 0.20,
	},
	{
		Name: "MFI量价反转策略", StrategyType: "mfi_volume",
		Description: "MFI<30（资金流量超卖）买入，MFI>70（资金流量超买）卖出。成交量版RSI。",
		ConfigJSON:  `{"capital":100000,"max_position":10,"stop_loss":0.05,"take_profit":0.20,"period":14,"oversold":30,"overbought":70}`,
		StopLossPct: 0.05, TakeProfitPct: 0.20,
	},
	{
		Name: "MTM动量策略", StrategyType: "mtm_momentum",
		Description: "MTM上穿0线作为买入信号，下穿0线作为卖出信号。动量趋势跟踪。",
		ConfigJSON:  `{"capital":100000,"max_position":10,"stop_loss":0.05,"take_profit":0.20,"mtm_ma_period":30}`,
		StopLossPct: 0.05, TakeProfitPct: 0.20,
	},
	{
		Name: "DMI趋势跟踪策略", StrategyType: "dmi_trend",
		Description: "PDI>MDI且ADX>25（趋势强度足够）买入。方向+趋势双重确认。",
		ConfigJSON:  `{"capital":100000,"max_position":10,"stop_loss":0.05,"take_profit":0.20,"adx_period":14,"trend_threshold":25}`,
		StopLossPct: 0.05, TakeProfitPct: 0.20,
	},
	{
		Name: "CCI区间突破策略", StrategyType: "cci_breakout",
		Description: "CCI上穿+100时买入（进入强势区间），下穿-100时卖出。顺势突破策略。",
		ConfigJSON:  `{"capital":100000,"max_position":10,"stop_loss":0.05,"take_profit":0.20,"period":14,"overbought":100,"oversold":-100}`,
		StopLossPct: 0.05, TakeProfitPct: 0.20,
	},
	{
		Name: "TRIX三重平滑策略", StrategyType: "trix_cross",
		Description: "TRIX上穿TRMA（金叉）买入，下穿（死叉）卖出。三重指数平滑趋势跟踪。",
		ConfigJSON:  `{"capital":100000,"max_position":10,"stop_loss":0.05,"take_profit":0.20,"period":12}`,
		StopLossPct: 0.05, TakeProfitPct: 0.20,
	},
	{
		Name: "捉妖大师策略", StrategyType: "zhuoyao_momentum",
		Description: "多周期共振策略，综合判断长中短期趋势。短线强势股捕捉。",
		ConfigJSON:  `{"capital":100000,"max_position":10,"stop_loss":0.05,"take_profit":0.20,"short_period":20,"mid_period":90,"long_period":180}`,
		StopLossPct: 0.05, TakeProfitPct: 0.20,
	},
	{
		Name: "机器学习策略", StrategyType: "ml_model",
		Description: "基于机器学习的AI选股策略：5模型集成（XGBoost/LightGBM/随机森林/逻辑回归/MLP）概率平均选股，纯Go本地推理无需Python；不可用时回退到多因子本地评分模型。",
		ConfigJSON:  `{"capital":100000,"max_position":10,"stop_loss":0.08,"take_profit":0.25,"model_names":["xgboost_astock_v1","astock_lgbm_v1","astock_rf_v1","astock_logistic_v1","astock_mlp_v1"],"ensemble_method":"probability","buy_threshold":0.5,"sell_threshold":0.3}`,
		StopLossPct: 0.08, TakeProfitPct: 0.25,
	},
	{
		Name: "超级趋势策略", StrategyType: "super_trend",
		Description: "ATR动态跟踪趋势通道，方向翻转即买卖信号。自适应市场波动并自带动态止损位，适合趋势行情下的持有与止盈防守。",
		ConfigJSON:  `{"capital":100000,"max_position":10,"stop_loss":0.05,"take_profit":0.20,"period":10,"multiplier":3.0}`,
		StopLossPct: 0.05, TakeProfitPct: 0.20,
	},
	{
		Name: "Aroon趋势方向策略", StrategyType: "aroon",
		Description: "AroonUp 上穿 AroonDown（新趋势走强）买入，下穿（趋势转弱）卖出。用于过滤震荡市假信号。",
		ConfigJSON:  `{"capital":100000,"max_position":10,"stop_loss":0.05,"take_profit":0.20,"period":25}`,
		StopLossPct: 0.05, TakeProfitPct: 0.20,
	},
	{
		Name: "HMA赫尔均线策略", StrategyType: "hma",
		Description: "收盘上穿 HMA（低滞后赫尔均线）买入，下穿卖出。对趋势拐点更灵敏、减少均线钝化。",
		ConfigJSON:  `{"capital":100000,"max_position":10,"stop_loss":0.05,"take_profit":0.20,"period":20}`,
		StopLossPct: 0.05, TakeProfitPct: 0.20,
	},
	{
		Name: "CMF资金流向策略", StrategyType: "cmf",
		Description: "蔡金资金流 CMF 上穿 +0.05（资金净流入确认）买入，跌破 −0.05（资金净流出确认）卖出。",
		ConfigJSON:  `{"capital":100000,"max_position":10,"stop_loss":0.05,"take_profit":0.20,"period":20,"buy_threshold":0.05,"sell_threshold":-0.05}`,
		StopLossPct: 0.05, TakeProfitPct: 0.20,
	},
}

// IsBuiltinStrategyType 判断是否为内置策略类型（与 BuiltinStrategies 目录一致）
func IsBuiltinStrategyType(strategyType string) bool {
	for _, meta := range BuiltinStrategies {
		if meta.StrategyType == strategyType {
			return true
		}
	}
	return false
}
