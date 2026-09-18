package backtest

import (
	"context"
	"fmt"
	"log"
	"math"
	"strings"
	"time"

	"github.com/quantpilot/quantpilot/internal/data"
	"github.com/quantpilot/quantpilot/internal/tdx"
)

// TradeRecord 交易记录
type TradeRecord struct {
	Index    int     `json:"index"`
	Date     string  `json:"date"`
	Side     string  `json:"side"` // "buy"/"sell"/"stop_loss"/"take_profit"/"close_position"
	Price    float64 `json:"price"`
	Quantity float64 `json:"quantity"`
	PnL      float64 `json:"pnl"`
	PnLPct   float64 `json:"pnl_pct"`
	// 单笔成本分解（元）：佣金/印花税/市场冲击/收盘bias（对标 cvxportfolio 成本模型）
	Commission float64 `json:"commission"`
	StampDuty  float64 `json:"stampDuty"`
	Impact     float64 `json:"impact"`
	Bias       float64 `json:"bias"`
	Cost       float64 `json:"cost"` // 单笔总成本
}

// 回测成交时点模型：决定买卖信号在哪个价位成交。
const (
	// ExecSameClose 信号当日收盘价成交（默认，回测偏乐观）
	ExecSameClose = "same_close"
	// ExecNextOpen 信号次日开盘价成交（更贴近实际，避免当日看到收盘信号后仍以收盘价成交的前视偏差）
	ExecNextOpen = "next_open"
)

// BacktestEngine 回测引擎
type BacktestEngine struct {
	Bars           []BarData
	Dates          []string
	Strategy       Strategy
	InitialCapital float64
	// RequestedCapital 调用方期望的初始资金（EnsureOneLotCapital 抬升前的原始值），供结果/日志核对
	RequestedCapital float64
	// InitialCapitalAdjusted 初始资金是否被 EnsureOneLotCapital 自动抬升至可买1手最高价标的，
	// 以便上层明确告知用户原始资金约束未被严格使用（不再"悄悄"修改）
	InitialCapitalAdjusted bool
	// CostModel 交易成本三因子模型（佣金/印花税 + 市场冲击 + 收盘bias），nil 时用默认 A 股模型
	CostModel  *CostModel
	Slippage   float64
	StopLoss   float64
	TakeProfit float64
	// ExecutionModel 买卖信号成交时点模型：ExecSameClose(当日收盘) 或 ExecNextOpen(次日开盘)，默认 ExecSameClose
	ExecutionModel string
	// LimitPct 涨跌停幅度（A股真实交易约束），默认 0.10（主板 ±10%）；
	// 创业板/科创板 ±20%、北交所 ±30%、ST ±5% 可由调用方按标的设置。
	LimitPct float64
	// FinancialProvider 可选财务数据提供者（DuckDB 实现）。非 nil 时，在生成信号前
	// 将 symbol 的财务数据按每根K线披露日对齐注入 BarData.Fin，供基本面因子策略使用。
	FinancialProvider FinancialProvider
	// FinancialSymbol 注入财务数据时使用的股票代码（通常与回测标的一致）
	FinancialSymbol string
	// FinancialCoverage 财务数据覆盖率（0~1），Run 执行后填充，供日志/诊断
	FinancialCoverage float64

	// volSeries 每根K线可用的日波动率 σ（trailing 窗口收益标准差），Run 前预计算（无前视偏差）
	volSeries []float64
}

// NewBacktestEngine 创建回测引擎
func NewBacktestEngine(
	bars []tdx.KlineBar,
	strategy Strategy,
	initialCapital float64,
) *BacktestEngine {
	barData := toBarData(bars)
	dates := make([]string, len(bars))
	for i, b := range bars {
		dates[i] = b.Date
	}

	if initialCapital <= 0 {
		initialCapital = 100000
	}
	requestedCapital := initialCapital

	// 高价股/指数（如沪深300、茅台）1手（100股）成本可能超过初始资金，
	// 若资金不足1手，executeBuy 中 maxShares<=0 会直接跳过，导致"有信号但0成交"、
	// 回测指标全为0。这里动态抬升初始资金到"最高价×100股×1.05滑点缓冲"，
	// 保证至少能成交1手（与策略指标刷新的基准资金口径一致）。
	// 抬升与否由 InitialCapitalAdjusted 标记，便于上层向用户明示资金约束被放宽。
	initialCapital = EnsureOneLotCapital(bars, initialCapital)

	return &BacktestEngine{
		Bars:                   barData,
		Dates:                  dates,
		Strategy:               strategy,
		InitialCapital:         initialCapital,
		RequestedCapital:       requestedCapital,
		InitialCapitalAdjusted: initialCapital != requestedCapital,
		CostModel:              DefaultCostModel(),
		Slippage:               0.001,
		ExecutionModel:         ExecSameClose,
		StopLoss:               0.05,
		TakeProfit:             0.20,
		LimitPct:               0.10,
	}
}

// costModel 返回成本模型（nil 时兜底默认 A 股模型）
func (e *BacktestEngine) costModel() *CostModel {
	if e.CostModel != nil {
		return e.CostModel
	}
	return DefaultCostModel()
}

// prevClose 返回 index i 的昨收价（即上一根 K 线收盘价）
func (e *BacktestEngine) prevClose(i int) float64 {
	if i <= 0 || i >= len(e.Bars) {
		return 0
	}
	return e.Bars[i-1].Close
}

// limitUpPrice 返回 index i 的涨停价（昨收×(1+limitPct) 四舍五入到分）
func (e *BacktestEngine) limitUpPrice(i int) float64 {
	pc := e.prevClose(i)
	if pc <= 0 {
		return 0
	}
	return math.Round(pc*(1+e.LimitPct)*100) / 100
}

// limitDownPrice 返回 index i 的跌停价（昨收×(1-limitPct) 四舍五入到分）
func (e *BacktestEngine) limitDownPrice(i int) float64 {
	pc := e.prevClose(i)
	if pc <= 0 {
		return 0
	}
	return math.Round(pc*(1-e.LimitPct)*100) / 100
}

// isSuspended 判断 index i 是否停牌（成交量近似为 0 视为无交易，当日无法成交）
func (e *BacktestEngine) isSuspended(i int) bool {
	return i >= 0 && i < len(e.Bars) && e.Bars[i].Volume <= 0
}

// dailyAmount 返回 index i 的当日成交额（元）；Amount 缺失时用 成交量×收盘价 兜底
func (e *BacktestEngine) dailyAmount(i int) float64 {
	if i < 0 || i >= len(e.Bars) {
		return 0
	}
	b := e.Bars[i]
	if b.Amount > 0 {
		return b.Amount
	}
	return float64(b.Volume) * b.Close
}

// precomputeVolSeries 预计算每根K线可用的日波动率 σ（trailing 窗口收益标准差，无前视偏差）。
// 数据不足窗口时退回可用样本，样本<2 时取 0（关闭冲击项）。
func (e *BacktestEngine) precomputeVolSeries() {
	n := len(e.Bars)
	if n == 0 {
		e.volSeries = nil
		return
	}
	window := e.costModel().VolWindow
	if window < 2 {
		window = 2
	}
	e.volSeries = make([]float64, n)
	for i := 1; i < n; i++ {
		start := i - window
		if start < 0 {
			start = 0
		}
		// 收集 [start, i-1] 区间的日收益率
		var rets []float64
		for j := start; j < i; j++ {
			pc := e.Bars[j].Close
			nc := e.Bars[j+1].Close
			if pc > 0 && nc > 0 {
				rets = append(rets, nc/pc-1)
			}
		}
		if len(rets) < 2 {
			continue
		}
		mean := 0.0
		for _, r := range rets {
			mean += r
		}
		mean /= float64(len(rets))
		v := 0.0
		for _, r := range rets {
			d := r - mean
			v += d * d
		}
		std := math.Sqrt(v / float64(len(rets)-1))
		if std > 0 {
			e.volSeries[i] = std
		}
	}
}

// dailyVol 返回 index i 成交时的日波动率 σ（用 [0,i-1] 已收盘数据估计，无前视偏差）
func (e *BacktestEngine) dailyVol(i int) float64 {
	if i < 0 || i >= len(e.volSeries) {
		return 0
	}
	return e.volSeries[i]
}

// EnsureOneLotCapital 确保初始资金至少能买入1手（100股）区间内最高价标的。
// 高价股/指数1手成本可能超过默认初始资金(10万)，若不处理会导致回测0成交、指标全为0。
// 返回 max(capital, 最高价×100股×1.05滑点缓冲)。供回测服务与工具入口复用，保证口径一致。
func EnsureOneLotCapital(bars []tdx.KlineBar, capital float64) float64 {
	if len(bars) == 0 {
		return capital
	}
	maxClose := 0.0
	for _, b := range bars {
		if b.Close > maxClose {
			maxClose = b.Close
		}
	}
	minCapital := 100 * maxClose * 1.05 // 1手 + 5%滑点/费用缓冲
	if capital < minCapital {
		log.Printf("[BacktestEngine] 初始资金 %.0f 不足以买入1手最高价标的(%.2f元/股)，自动抬升至 %.0f",
			capital, maxClose, minCapital)
		return minCapital
	}
	return capital
}

// BacktestResult 回测结果
type BacktestResult struct {
	FinalCapital float64
	AnnualReturn float64
	SharpeRatio  float64
	MaxDrawdown  float64
	WinRate      float64
	ProfitFactor float64
	TotalTrades  int
	TotalBars    int
	WinTrades    int
	LossTrades   int
	Trades       []TradeRecord
	EquityCurve  []float64
	Dates        []string
	StartDate    string
	EndDate      string
	BarsCount    int
	// Turnover 年化换手率：单位资本在一年内的累计成交额（成交金额/平均资金/年数），
	// 反映策略买入卖出频率，横向对比时用于衡量交易成本敏感性。
	Turnover float64
	// 成本分解汇总（对标 cvxportfolio 成本时间序列）
	TotalCommission float64 `json:"totalCommission"` // 累计佣金
	TotalStampDuty  float64 `json:"totalStampDuty"`  // 累计印花税
	TotalImpact     float64 `json:"totalImpact"`     // 累计市场冲击
	TotalBias       float64 `json:"totalBias"`       // 累计收盘bias
	TotalCost       float64 `json:"totalCost"`       // 累计总成本
	CostRatio       float64 `json:"costRatio"`       // 总成本/初始资金 %
	CostPerTrade    float64 `json:"costPerTrade"`    // 单笔平均成本（元）
	// CostCurve 每日累计成本序列（元），与 Dates 对齐，用于成本吞噬收益的可视化
	CostCurve []float64 `json:"costCurve"`
}

// 挂起的收盘买/卖信号（ExecNextOpen 模型下，于次日开盘执行）
type pendingTrade struct {
	side string // "buy" | "sell"
}

// backtestState 回测运行状态（信号执行/止损止盈/权益计算共享）
type backtestState struct {
	capital     float64
	position    float64
	entryPrice  float64
	pending     *pendingTrade
	trades      []TradeRecord
	equityCurve []float64
	costCurve   []float64 // 每根K线的成本累计（元）
}

// Run 执行回测
// 拆分为四个组件：权益计算(processBar) / 止损止盈(executeStop) / 信号执行(executeBuy/executeSell) / 绩效报告(calculatePerformance)
func (e *BacktestEngine) Run() *BacktestResult {
	n := len(e.Bars)
	if n < 20 {
		log.Printf("[BacktestEngine] Not enough bars: %d, at least 20 required", n)
		return &BacktestResult{
			FinalCapital: e.InitialCapital,
			AnnualReturn: 0,
			SharpeRatio:  0,
			MaxDrawdown:  0,
			WinRate:      0,
			ProfitFactor: 0,
			TotalTrades:  0,
			BarsCount:    n,
		}
	}

	// 打印策略详细信息
	log.Printf("[BacktestEngine] Using strategy: %s (type: %T)", e.Strategy.Name(), e.Strategy)

	// 若策略确实依赖财务数据且配置了提供者，按披露日把财务数据注入每根K线（无未来函数），
	// 供基本面因子策略使用。只有显式实现 FinancialAware 的策略才注入——历史上这里对所有策略
	// 都逐根K线发起 DuckDB 查询（N+1），而内置策略从不读取 Fin，是回测耗时(~2h)的主因。
	// 借鉴 vectorbt 思想：数据按需装载（lazy/selective loading），只在真正消费时才批量取数。
	if e.FinancialProvider != nil && strategyNeedsFinancialData(e.Strategy) {
		sym := e.FinancialSymbol
		if sym == "" {
			sym = "unknown"
		}
		e.FinancialCoverage = InjectFinancialData(e.Bars, sym, e.FinancialProvider)
		log.Printf("[BacktestEngine] 财务数据注入完成: symbol=%s 覆盖率=%.1f%%", sym, e.FinancialCoverage*100)
	}

	// 生成信号
	signals := e.Strategy.GenerateSignals(e.Bars)

	// 统计信号类型
	buySignals := 0
	sellSignals := 0
	holdSignals := 0
	for _, s := range signals {
		if s == SignalBuy {
			buySignals++
		} else if s == SignalSell {
			sellSignals++
		} else {
			holdSignals++
		}
	}
	log.Printf("[BacktestEngine] Strategy=%s generated signals: buy=%d, sell=%d, hold=%d, total=%d",
		e.Strategy.Name(), buySignals, sellSignals, holdSignals, len(signals))

	// 打印前10个信号用于诊断
	signalDesc := ""
	for i := 0; i < min(10, len(signals)); i++ {
		s := signals[i]
		if s == SignalBuy {
			signalDesc += "B"
		} else if s == SignalSell {
			signalDesc += "S"
		} else {
			signalDesc += "H"
		}
		if i < min(10, len(signals))-1 {
			signalDesc += ","
		}
	}
	log.Printf("[BacktestEngine] First %d signals: [%s]", min(10, len(signals)), signalDesc)

	// 回测主循环：权益计算 + 止损止盈 + 信号执行
	// 先预计算每根K线的日波动率（无前视偏差），供冲击成本估计
	e.precomputeVolSeries()
	st := &backtestState{
		capital:     e.InitialCapital,
		equityCurve: make([]float64, n),
		costCurve:   make([]float64, n),
	}
	for i := 0; i < n; i++ {
		e.processBar(i, signals[i], st)
	}

	// 最后持仓平仓
	e.closeRemainingPosition(n-1, st)

	return e.calculatePerformance(st, n)
}

// processBar 处理单根K线：先结算上一交易日前置的 next_open 挂单（次日开盘成交）、
// 再检查止损止盈、再推演买卖信号，最后在当日成交后重算权益。
func (e *BacktestEngine) processBar(i int, signal StrategySignal, st *backtestState) {
	bar := e.Bars[i]
	price := bar.Close

	// next_open 模型下，先执行上一交易日的挂单（用本日开盘价）
	if st.pending != nil {
		e.executePending(i, st)
	}

	// 止损/止盈检查
	if st.position > 0 && st.entryPrice > 0 {
		if price <= st.entryPrice*(1-e.StopLoss) {
			e.executeStop(i, "stop_loss", st)
			e.updateEquity(i, st)
			return
		}
		if price >= st.entryPrice*(1+e.TakeProfit) {
			e.executeStop(i, "take_profit", st)
			e.updateEquity(i, st)
			return
		}
	}

	// 信号触发交易
	if signal == SignalBuy && st.position == 0 {
		e.executeBuy(i, st)
	} else if signal == SignalSell && st.position > 0 {
		e.executeSell(i, st)
	}

	// 权益曲线计入当日成交后的实际权益
	e.updateEquity(i, st)
}

// updateEquity 按当前现金+持仓市值写入第 i 根的权益曲线。
func (e *BacktestEngine) updateEquity(i int, st *backtestState) {
	st.equityCurve[i] = st.capital + st.position*e.Bars[i].Close
}

// executeBuy 买入信号。按成交模型决定成交时点与价格：
// same_close 当日收盘成交；next_open 挂起到下一根开盘成交（最后一根无次日则当日收盘成交）。
func (e *BacktestEngine) executeBuy(i int, st *backtestState) {
	// 停牌：当日无成交，无法买入
	if e.isSuspended(i) {
		log.Printf("[BacktestEngine] %s BUY跳过：%s 停牌/无成交量", e.Dates[i], e.Strategy.Name())
		return
	}
	if e.ExecutionModel == ExecNextOpen && i+1 < len(e.Bars) {
		st.pending = &pendingTrade{side: "buy"}
		return
	}
	e.executeBuyNow(i, e.Bars[i].Close, st)
}

// executePending 结算 next_open 挂单：在 index i（次日）以开盘价执行上一交易日的挂单。
func (e *BacktestEngine) executePending(i int, st *backtestState) {
	p := st.pending
	st.pending = nil
	if p == nil {
		return
	}
	log.Printf("[BacktestEngine] %s 次日开盘执行 %s 挂单（模型=next_open @开盘%.2f）", e.Dates[i], p.side, e.Bars[i].Open)
	switch p.side {
	case "buy":
		if st.position == 0 {
			e.executeBuyNow(i, e.Bars[i].Open, st)
		}
	case "sell":
		if st.position > 0 {
			e.executeSellNow(i, e.Bars[i].Open, st)
		}
	}
}

// executeSell 卖出信号。按成交模型决定成交时点与价格（同 executeBuy 语义）。
func (e *BacktestEngine) executeSell(i int, st *backtestState) {
	// 停牌：当日无法卖出，保留持仓
	if e.isSuspended(i) {
		log.Printf("[BacktestEngine] %s SELL跳过：%s 停牌/无成交量，保留持仓", e.Dates[i], e.Strategy.Name())
		return
	}
	if e.ExecutionModel == ExecNextOpen && i+1 < len(e.Bars) {
		st.pending = &pendingTrade{side: "sell"}
		return
	}
	e.executeSellNow(i, e.Bars[i].Close, st)
}

// executeBuyNow 以 index i 与指定基准价执行整手买入（A股100股整数倍）。
// 加入真实约束：停牌当日无法买入；开盘/收盘一字涨停封板时买不进则放弃本次买入。
// 资金扣减采用三因子成本模型：成交额 + 佣金(最低5元) + 市场冲击 + 收盘bias。
func (e *BacktestEngine) executeBuyNow(i int, price float64, st *backtestState) {
	// 一字涨停封死（价格触及涨停价）：无法买入，放弃本次信号
	if up := e.limitUpPrice(i); up > 0 && price >= up-1e-6 {
		log.Printf("[BacktestEngine] %s BUY跳过：%s 涨停封板(%.2f>=%.2f)，买不进",
			e.Dates[i], e.Strategy.Name(), price, up)
		return
	}
	buyPrice := price * (1 + e.Slippage)
	// 预扣成本缓冲（佣金+印花税+冲击粗估约0.2%），避免整手买入后现金为负
	maxShares := math.Floor(st.capital / (buyPrice * (1 + e.costModel().CommissionRate + 0.002)))
	maxShares = math.Floor(maxShares/100) * 100
	if maxShares <= 0 {
		return
	}
	cm := e.costModel()
	sigma := e.dailyVol(i)
	vol := e.dailyAmount(i)
	tradedAmount := maxShares * buyPrice
	bd := cm.BuyCost(tradedAmount, vol, sigma)
	outflow := tradedAmount + bd.Total()
	// 含全部成本后若超资金，按比例回退整手数，保证现金不为负
	if outflow > st.capital {
		shrink := st.capital / outflow
		maxShares = math.Floor(maxShares*shrink/100) * 100
		if maxShares <= 0 {
			return
		}
		tradedAmount = maxShares * buyPrice
		bd = cm.BuyCost(tradedAmount, vol, sigma)
		outflow = tradedAmount + bd.Total()
	}
	// 二次缩减（整手取整）后再次确认现金不为负：若仍超资金则放弃本次买入，绝不产生负现金
	if outflow > st.capital {
		return
	}
	st.capital -= outflow
	st.position = maxShares
	st.entryPrice = buyPrice
	st.costCurve[i] += bd.Total()
	st.trades = append(st.trades, TradeRecord{
		Index:      i,
		Date:       e.Dates[i],
		Side:       "buy",
		Price:      buyPrice,
		Quantity:   maxShares,
		PnL:        0,
		PnLPct:     0,
		Commission: bd.Commission,
		StampDuty:  bd.StampDuty,
		Impact:     bd.Impact,
		Bias:       bd.Bias,
		Cost:       bd.Total(),
	})
}

// executeStop 止损/止盈：按下一根K线开盘价成交（更贴近实际），最后一根用当前收盘价。
// 加入真实约束：成交当日停牌 或 低开/触及跌停封死时无法卖出，保留持仓等待下一根K线。
func (e *BacktestEngine) executeStop(i int, side string, st *backtestState) {
	execIdx := i
	price := e.Bars[i].Close
	if i+1 < len(e.Bars) {
		execIdx = i + 1
		price = e.Bars[i+1].Open
	}
	// 成交当日停牌 → 卖不出，保留持仓
	if e.isSuspended(execIdx) {
		log.Printf("[BacktestEngine] %s %s跳过：%s 停牌/无成交量，保留持仓", e.Dates[i], side, e.Strategy.Name())
		return
	}
	// 低开触及跌停 → 无法卖出，保留持仓
	if down := e.limitDownPrice(execIdx); down > 0 && price <= down+1e-6 {
		log.Printf("[BacktestEngine] %s %s跳过：%s 跌停封板(%.2f<=%.2f)，卖不出", e.Dates[i], side, e.Strategy.Name(), price, down)
		return
	}
	e.recordSell(execIdx, side, price, st)
}

// executeSellNow 以 index i 与指定基准价执行卖出（清仓）。
// 加入真实约束：成交当日停牌 或 触及跌停封死时无法卖出，保留持仓。
func (e *BacktestEngine) executeSellNow(i int, price float64, st *backtestState) {
	// 一字跌停封死（价格触及跌停价）：无法卖出，保留持仓
	if down := e.limitDownPrice(i); down > 0 && price <= down+1e-6 {
		log.Printf("[BacktestEngine] %s SELL跳过：%s 跌停封板(%.2f<=%.2f)，卖不出",
			e.Dates[i], e.Strategy.Name(), price, down)
		return
	}
	e.recordSell(i, "sell", price, st)
}

// recordSell 记录卖出成交并更新状态（止损/止盈/卖出/平仓共用）。
// 资金入账采用三因子成本模型：卖出净额 = 成交额 − 佣金(最低5元) − 印花税 − 冲击 − bias。
func (e *BacktestEngine) recordSell(i int, side string, execPrice float64, st *backtestState) {
	sellPrice := execPrice * (1 - e.Slippage)
	tradePnL := (sellPrice - st.entryPrice) * st.position
	tradePnLPct := (sellPrice - st.entryPrice) / st.entryPrice * 100

	cm := e.costModel()
	tradedAmount := st.position * sellPrice
	bd := cm.SellCost(tradedAmount, e.dailyAmount(i), e.dailyVol(i))

	st.capital += tradedAmount - bd.Total()
	st.costCurve[i] += bd.Total()
	st.trades = append(st.trades, TradeRecord{
		Index:      i,
		Date:       e.Dates[i],
		Side:       side,
		Price:      sellPrice,
		Quantity:   st.position,
		PnL:        tradePnL,
		PnLPct:     tradePnLPct,
		Commission: bd.Commission,
		StampDuty:  bd.StampDuty,
		Impact:     bd.Impact,
		Bias:       bd.Bias,
		Cost:       bd.Total(),
	})
	st.position = 0
	st.entryPrice = 0
}

// closeRemainingPosition 回测结束仍有持仓时按最后价格平仓（标记为 close_position 以便追溯）
func (e *BacktestEngine) closeRemainingPosition(lastIdx int, st *backtestState) {
	if st.position <= 0 {
		return
	}
	// 末根若停牌（无成交/成交量为0），回退到最近一个有成交的K线收盘价估值平仓，避免用0价结算
	lastIdx = e.lastTradableIndex(lastIdx)
	lastPrice := e.Bars[lastIdx].Close
	e.recordSell(lastIdx, "close_position", lastPrice, st)
	// 计入最终平仓：把平仓发生之后的每根K线权益结算为平仓后的现金（此时已无持仓），
	// 保证权益曲线末值与实际终值（FinalCapital）一致，不被平仓前的持仓市值拖尾。
	for k := lastIdx; k < len(st.equityCurve); k++ {
		st.equityCurve[k] = st.capital
	}
}

// lastTradableIndex 从 idx 向前返回最近一个有成交量（未停牌）的K线下标；若无则返回 idx。
func (e *BacktestEngine) lastTradableIndex(idx int) int {
	for i := idx; i >= 0; i-- {
		if e.Bars[i].Volume > 0 {
			return i
		}
	}
	return idx
}

// calculatePerformance 计算绩效指标（年化收益/最大回撤/夏普/胜率/盈亏比）
func (e *BacktestEngine) calculatePerformance(st *backtestState, n int) *BacktestResult {
	finalCapital := st.capital

	winTrades := 0
	lossTrades := 0
	totalPnL := 0.0
	grossProfit := 0.0
	grossLoss := 0.0

	for _, t := range st.trades {
		if t.PnL > 0 {
			winTrades++
			grossProfit += t.PnL
		} else if t.PnL < 0 {
			lossTrades++
			grossLoss += -t.PnL
		}
		totalPnL += t.PnL
	}

	totalTrades := winTrades + lossTrades
	winRate := 0.0
	if totalTrades > 0 {
		winRate = float64(winTrades) / float64(totalTrades) * 100
	}

	profitFactor := 0.0
	if grossLoss > 0 {
		profitFactor = grossProfit / grossLoss
	} else if grossProfit > 0 {
		profitFactor = 999.0
	}

	// 年化收益率
	annualReturn := 0.0
	days := float64(n)
	if days > 0 && e.InitialCapital > 0 && finalCapital > 0 {
		years := days / 252.0
		if years > 0 {
			annualReturn = (math.Pow(finalCapital/e.InitialCapital, 1.0/years) - 1) * 100
		}
	}

	// 最大回撤
	maxDrawdown := calculateMaxDrawdown(st.equityCurve)

	// 夏普比率
	sharpeRatio := calculateSharpeRatio(st.equityCurve)

	// 年化换手率：总成交金额 / 平均资金 / 年数（单位资本的年度成交倍数）
	turnover := 0.0
	{
		totalTraded := 0.0
		for _, t := range st.trades {
			totalTraded += math.Abs(t.Price * t.Quantity)
		}
		meanCapital := (e.InitialCapital + finalCapital) / 2
		years := days / 252.0
		if meanCapital > 0 && years > 0 {
			turnover = totalTraded / meanCapital / years
		}
	}

	// 成本分解汇总：累计佣金/印花税/冲击/bias（对标 cvxportfolio 成本时间序列）
	var totalCommission, totalStampDuty, totalImpact, totalBias float64
	for _, t := range st.trades {
		totalCommission += t.Commission
		totalStampDuty += t.StampDuty
		totalImpact += t.Impact
		totalBias += t.Bias
	}
	totalCost := totalCommission + totalStampDuty + totalImpact + totalBias
	costRatio := 0.0
	if e.InitialCapital > 0 {
		costRatio = totalCost / e.InitialCapital * 100
	}
	costPerTrade := 0.0
	if totalTrades > 0 {
		costPerTrade = totalCost / float64(totalTrades)
	}

	// 累计成本曲线（与 Dates 对齐，单调递增）
	costCurve := st.costCurve
	if costCurve != nil {
		cum := 0.0
		for i, c := range costCurve {
			cum += c
			costCurve[i] = cum
		}
	}

	startDate := ""
	endDate := ""
	if len(e.Dates) > 0 {
		startDate = e.Dates[0]
		endDate = e.Dates[len(e.Dates)-1]
	}

	result := &BacktestResult{
		FinalCapital:    finalCapital,
		AnnualReturn:    annualReturn,
		SharpeRatio:     sharpeRatio,
		MaxDrawdown:     maxDrawdown,
		WinRate:         winRate,
		ProfitFactor:    profitFactor,
		TotalTrades:     totalTrades,
		WinTrades:       winTrades,
		LossTrades:      lossTrades,
		Trades:          st.trades,
		EquityCurve:     st.equityCurve,
		Dates:           e.Dates,
		StartDate:       startDate,
		EndDate:         endDate,
		BarsCount:       n,
		Turnover:        turnover,
		TotalCommission: totalCommission,
		TotalStampDuty:  totalStampDuty,
		TotalImpact:     totalImpact,
		TotalBias:       totalBias,
		TotalCost:       totalCost,
		CostRatio:       costRatio,
		CostPerTrade:    costPerTrade,
		CostCurve:       costCurve,
	}

	log.Printf("[BacktestEngine] Strategy=%s completed: annualReturn=%.2f%%, sharpe=%.4f, maxDD=%.2f%%, totalTrades=%d, winRate=%.2f%%, turnover=%.2fx",
		e.Strategy.Name(), result.AnnualReturn, result.SharpeRatio, result.MaxDrawdown, result.TotalTrades, result.WinRate, result.Turnover)

	return result
}

// calculateMaxDrawdown 计算最大回撤
func calculateMaxDrawdown(equity []float64) float64 {
	if len(equity) < 2 {
		return 0
	}

	peak := equity[0]
	maxDD := 0.0
	for _, v := range equity {
		if v > peak {
			peak = v
		}
		dd := 0.0
		if peak > 0 {
			dd = (peak - v) / peak * 100
		}
		if dd > maxDD {
			maxDD = dd
		}
	}
	return maxDD
}

// calculateSharpeRatio 计算夏普比率
func calculateSharpeRatio(equity []float64) float64 {
	if len(equity) < 2 {
		return 0
	}

	returns := make([]float64, 0, len(equity)-1)
	for i := 1; i < len(equity); i++ {
		if equity[i-1] > 0 {
			ret := (equity[i] - equity[i-1]) / equity[i-1]
			returns = append(returns, ret)
		}
	}

	if len(returns) < 2 {
		return 0
	}

	meanReturn := 0.0
	for _, r := range returns {
		meanReturn += r
	}
	meanReturn /= float64(len(returns))

	stdDev := 0.0
	for _, r := range returns {
		diff := r - meanReturn
		stdDev += diff * diff
	}
	stdDev = math.Sqrt(stdDev / float64(len(returns)-1))

	if stdDev < 1e-8 {
		return 0
	}

	// 年化夏普比率（假设 252 个交易日）
	sharpe := meanReturn / stdDev * math.Sqrt(252)
	return sharpe
}

// RunBacktestWithData 使用 K 线数据直接运行回测（使用默认止损止盈 5%/20%）
func RunBacktestWithData(
	bars []tdx.KlineBar,
	strategyType string,
	initialCapital float64,
) *BacktestResult {
	return RunBacktestWithParams(bars, strategyType, initialCapital, 0.05, 0.20)
}

// FilterBarsByDate 按日期范围过滤 K 线
func FilterBarsByDate(bars []tdx.KlineBar, startDate, endDate string) []tdx.KlineBar {
	if startDate == "" && endDate == "" {
		return bars
	}

	var startT, endT time.Time
	if startDate != "" {
		startT, _ = time.Parse("2006-01-02", startDate)
	}
	if endDate != "" {
		endT, _ = time.Parse("2006-01-02", endDate)
	}

	result := make([]tdx.KlineBar, 0)
	for _, bar := range bars {
		barDate, err := time.Parse("2006-01-02", bar.Date)
		if err != nil {
			continue
		}
		if !startT.IsZero() && barDate.Before(startT) {
			continue
		}
		if !endT.IsZero() && barDate.After(endT) {
			continue
		}
		result = append(result, bar)
	}
	return result
}

// GetKlineFromDuckDB 从离线DuckDB获取K线数据作为TDX的fallback
func GetKlineFromDuckDB(duckdbMgr *data.DuckDBManager, symbol string, days int) ([]tdx.KlineBar, error) {
	if duckdbMgr == nil || !duckdbMgr.HasStockDB() {
		log.Printf("[Backtest] DuckDB not available: mgr=%v, hasStock=%v", duckdbMgr, duckdbMgr != nil && duckdbMgr.HasStockDB())
		return nil, fmt.Errorf("DuckDB manager not available")
	}

	ctx := context.Background()

	// 尝试多种symbol格式
	symbolFormats := []string{symbol}
	// 转换为大写
	symbolFormats = append(symbolFormats, strings.ToUpper(symbol))
	// 尝试不带前缀的格式
	if len(symbol) >= 2 {
		prefix := symbol[:2]
		code := symbol[2:]
		if prefix == "sh" || prefix == "SH" {
			symbolFormats = append(symbolFormats, code+".SH")
			symbolFormats = append(symbolFormats, "SH"+code)
			symbolFormats = append(symbolFormats, code) // 纯数字代码
		} else if prefix == "sz" || prefix == "SZ" {
			symbolFormats = append(symbolFormats, code+".SZ")
			symbolFormats = append(symbolFormats, "SZ"+code)
			symbolFormats = append(symbolFormats, code) // 纯数字代码
		}
	}

	log.Printf("[Backtest] Trying %d symbol formats for DuckDB query: %v", len(symbolFormats), symbolFormats)

	var lastErr error
	foundData := false
	for _, sym := range symbolFormats {
		bars, err := duckdbMgr.GetKlineFromStock(ctx, sym, days)
		if err != nil {
			lastErr = err
			log.Printf("[Backtest] DuckDB query failed for format %s: %v", sym, err)
			continue
		}

		if len(bars) == 0 {
			log.Printf("[Backtest] No data found in DuckDB for format: %s", sym)
			continue
		}

		foundData = true
		log.Printf("[Backtest] Found %d bars in DuckDB for format: %s", len(bars), sym)

		// 转换为tdx.KlineBar格式
		result := make([]tdx.KlineBar, len(bars))
		for i, bar := range bars {
			result[i] = tdx.KlineBar{
				Date:   bar.Date.Format("2006-01-02"),
				Open:   bar.Open,
				High:   bar.High,
				Low:    bar.Low,
				Close:  bar.Close,
				Volume: int64(bar.Volume),
				Amount: bar.Amount,
			}
		}

		// 按日期正序排列
		for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
			result[i], result[j] = result[j], result[i]
		}

		log.Printf("[Backtest] Successfully got %d bars from DuckDB for %s (using format: %s)", len(result), symbol, sym)
		return result, nil
	}

	if !foundData {
		log.Printf("[Backtest] No data found in DuckDB for any format of symbol: %s", symbol)
	}

	if lastErr != nil {
		return nil, fmt.Errorf("failed to get kline from DuckDB for %s: %w", symbol, lastErr)
	}
	return nil, nil
}
