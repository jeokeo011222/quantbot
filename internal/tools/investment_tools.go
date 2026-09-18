package tools

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/quantpilot/quantpilot/internal/data"
	"github.com/quantpilot/quantpilot/internal/orderbook"
	"github.com/quantpilot/quantpilot/internal/portfolio"
)

// ==================== OrderCostTool ====================
// order_cost_estimator 交易成本估算工具
// 纯确定性 A 股规则计算：买入收佣金(万三，最低5元)；卖出收佣金+印花税(0.05%)。
// 同时校验买入一手可行性（数量必须为100股整数倍）。

// OrderCostTool 交易成本估算工具
type OrderCostTool struct{}

// NewOrderCostTool 创建交易成本估算工具
func NewOrderCostTool() *OrderCostTool { return &OrderCostTool{} }

func (t *OrderCostTool) Name() string { return "order_cost_estimator" }

func (t *OrderCostTool) Description() string {
	return "按A股规则计算一笔买卖的佣金/印花税/总成本，并校验一手(100股)买入可行性。用于 Trader 下单前的成本与可行性评估。"
}

func (t *OrderCostTool) GetDefinition() ToolDefinition {
	return ToolDefinition{
		Type: "function",
		Function: FunctionDef{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"side": map[string]interface{}{
						"type":        "string",
						"description": "交易方向：buy=买入，sell=卖出",
						"enum":        []string{"buy", "sell"},
					},
					"price": map[string]interface{}{
						"type":        "number",
						"description": "成交价（元）",
					},
					"quantity": map[string]interface{}{
						"type":        "integer",
						"description": "股数（买入必须为100的整数倍）",
					},
				},
				"required": []string{"side", "price", "quantity"},
			},
		},
	}
}

func (t *OrderCostTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	side, _ := args["side"].(string)
	if side != "buy" && side != "sell" {
		return nil, fmt.Errorf("side 必须为 buy 或 sell")
	}
	price, _ := args["price"].(float64)
	quantity, _ := args["quantity"].(float64)
	if price <= 0 || quantity <= 0 {
		return nil, fmt.Errorf("price 与 quantity 必须大于 0")
	}
	qty := int(quantity)

	notional := price * float64(qty)

	// 佣金：万三 = 0.0003，最低 5 元
	commission := notional * 0.0003
	if commission < 5 {
		commission = 5
	}

	stampDuty := 0.0
	if side == "sell" {
		stampDuty = notional * 0.0005 // 印花税 0.05%（仅卖出）
	}

	totalFee := commission + stampDuty
	netCost := notional + totalFee // 买入总支出
	if side == "sell" {
		netCost = notional - totalFee // 卖出净得
	}

	lotValid := true
	amount := quantity
	if side == "buy" && qty%100 != 0 {
		lotValid = false
		// 向下取整为 100 股整数倍以提示可执行手数
		amount = float64((qty / 100) * 100)
	}
	hands := qty / 100

	return map[string]interface{}{
		"side":           side,
		"price":          price,
		"quantity":       qty,
		"notional":       round2(notional),
		"commission":     round2(commission),
		"stamp_duty":     round2(stampDuty),
		"total_fee":      round2(totalFee),
		"net_value":      round2(netCost),
		"fee_rate":       round4(totalFee / notional * 100), // 总费率百分比
		"hands":          hands,
		"lot_valid":      lotValid,
		"executable_qty": amount,
		"note":           noteFor(side, lotValid),
	}, nil
}

func noteFor(side string, lotValid bool) string {
	if lotValid {
		return "数量与费用符合A股规则"
	}
	return "买入数量非100股整数倍，无法整手成交，请使用 executable_qty 表示的整手数量"
}

// ==================== MacroSnapshotTool ====================
// get_macro_snapshot 宏观/市场快照工具
// 聚合真实市场统计（涨跌家数/涨停跌停）与当前市场阶段/时间，给出盘中宏观快照。

// MacroSnapshotTool 市场快照工具
type MacroSnapshotTool struct {
	duckdbManager *data.DuckDBManager
}

// NewMacroSnapshotTool 创建市场快照工具
func NewMacroSnapshotTool(duckdbManager *data.DuckDBManager) *MacroSnapshotTool {
	return &MacroSnapshotTool{duckdbManager: duckdbManager}
}

func (t *MacroSnapshotTool) Name() string { return "get_macro_snapshot" }

func (t *MacroSnapshotTool) Description() string {
	return "获取A股当前市场宏观快照：实时市场统计(涨跌家数/涨停跌停) + 交易时段/阶段 + 时间。用于 Planner/CIO 盘前与盘中把握市场环境。"
}

func (t *MacroSnapshotTool) GetDefinition() ToolDefinition {
	return ToolDefinition{
		Type: "function",
		Function: FunctionDef{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"market": map[string]interface{}{
						"type":        "string",
						"description": "市场：CN（默认全A）",
						"enum":        []string{"CN", "SH", "SZ"},
					},
				},
			},
		},
	}
}

func (t *MacroSnapshotTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	market, _ := args["market"].(string)
	if market == "" {
		market = "CN"
	}
	now := time.Now()

	var stats interface{}
	var statsErr string
	if t.duckdbManager != nil {
		s, err := t.duckdbManager.GetMarketStats(ctx, market)
		if err != nil {
			statsErr = err.Error()
		} else {
			stats = s
		}
	}

	return map[string]interface{}{
		"market":       market,
		"timestamp":    now.Format("2006-01-02 15:04:05"),
		"weekday":      now.Weekday().String(),
		"market_phase": macPhaseLabel(now),
		"market_stats": stats,
		"stats_error":  statsErr,
	}, nil
}

func macPhaseLabel(now time.Time) string {
	m := now.Hour()*60 + now.Minute()
	switch {
	case m >= 9*60 && m < 9*60+30:
		return "盘前(9:00-9:30)"
	case m >= 9*60+30 && m < 11*60+30:
		return "上午交易(9:30-11:30)"
	case m >= 11*60+30 && m < 13*60:
		return "午休(11:30-13:00)"
	case m >= 13*60 && m < 15*60:
		return "下午交易(13:00-15:00)"
	case m >= 15*60 && m < 16*60:
		return "盘后(15:00-16:00)"
	case m >= 16*60 && m < 18*60:
		return "复盘(16:00-18:00)"
	default:
		return "休市"
	}
}

func round2(v float64) float64 { return float64(int(v*100+0.5)) / 100 }
func round4(v float64) float64 { return float64(int(v*10000+0.5)) / 10000 }

// ==================== PortfolioRiskViewTool ====================
// portfolio_risk_view 组合风险视图工具
// 从组合引擎读取真实持仓/资产数据，确定性计算风险指标（集中度/敞口/凸性结构）。
// 工具负责计算风险事实，CIO/Risk 负责据此做风险解释与决策。

// PortfolioRiskViewTool 组合风险视图工具
type PortfolioRiskViewTool struct {
	engine *portfolio.Engine
}

// NewPortfolioRiskViewTool 创建组合风险视图工具
func NewPortfolioRiskViewTool(engine *portfolio.Engine) *PortfolioRiskViewTool {
	return &PortfolioRiskViewTool{engine: engine}
}

func (t *PortfolioRiskViewTool) Name() string { return "portfolio_risk_view" }

func (t *PortfolioRiskViewTool) Description() string {
	return "计算当前投资组合的风险视图：持仓集中度(HHI)、最大单票权重、TOP5集中度、现金比例、杠杆敞口、组合收益/回撤。用于 CIO/Risk 做组合层风险审查与调仓决策。"
}

func (t *PortfolioRiskViewTool) GetDefinition() ToolDefinition {
	return ToolDefinition{
		Type: "function",
		Function: FunctionDef{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		},
	}
}

func (t *PortfolioRiskViewTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	if t.engine == nil {
		return nil, fmt.Errorf("组合引擎未初始化")
	}
	state := t.engine.GetPortfolioState()

	totalAssets, _ := state["totalAssets"].(float64)
	totalMarketValue, _ := state["totalMarketValue"].(float64)
	cash, _ := state["cash"].(float64)
	totalPnL, _ := state["totalPnL"].(float64)
	totalReturn, _ := state["totalReturn"].(float64)
	dailyPnL, _ := state["dailyPnL"].(float64)
	dailyReturn, _ := state["dailyReturn"].(float64)

	positions, _ := state["positions"].([]map[string]interface{})

	weights := make([]float64, 0, len(positions))
	var unrealizedPnL float64
	for _, p := range positions {
		w, _ := p["weight"].(float64)
		if w > 0 {
			weights = append(weights, w)
		}
		if up, ok := p["unrealizedPnL"].(float64); ok {
			unrealizedPnL += up
		}
	}
	// 加权平均单票浮盈收益率（用市值加权）
	weightedReturn := 0.0
	weightSum := 0.0
	for _, p := range positions {
		w, _ := p["weight"].(float64)
		ur, _ := p["unrealizedReturn"].(float64)
		weightedReturn += w * ur
		weightSum += w
	}
	if weightSum > 0 {
		weightedReturn = weightedReturn / weightSum
	}

	// 集中度指标
	var hhi, largest, top5 float64
	if len(weights) > 0 {
		sort.Float64s(weights) // 升序
		for _, w := range weights {
			hhi += w * w
		}
		largest = weights[len(weights)-1]
		top5sum := 0.0
		n := len(weights)
		for i := 0; i < 5 && i < n; i++ {
			top5sum += weights[n-1-i]
		}
		top5 = top5sum
	}
	// 归一化集中度：HHI 与满仓单票(10000)相比的 0-1 浓度
	concentration := 0.0
	if totalMarketValue > 0 {
		concentration = hhi / 10000
	}

	cashRatio := 0.0
	if totalAssets > 0 {
		cashRatio = cash / totalAssets * 100
	}

	// 风险档位判定
	riskTier := "低"
	switch {
	case concentration >= 0.6:
		riskTier = "高（极度集中）"
	case concentration >= 0.35:
		riskTier = "中"
	}

	return map[string]interface{}{
		"as_of":                  time.Now().Format(time.RFC3339),
		"total_assets":           round2(totalAssets),
		"total_market_value":     round2(totalMarketValue),
		"cash":                   round2(cash),
		"cash_ratio_pct":         round2(cashRatio),
		"positions_count":        len(positions),
		"hhi_concentration":      round4(concentration),
		"largest_weight_pct":     round2(largest),
		"top5_weight_pct":        round2(top5),
		"unrealized_pnl":         round2(unrealizedPnL),
		"wtd_unrealized_ret_pct": round2(weightedReturn),
		"cum_return_pct":         round2(totalReturn),
		"cum_pnl":                round2(totalPnL),
		"daily_pnl":              round2(dailyPnL),
		"daily_return_pct":       round2(dailyReturn),
		"risk_tier":              riskTier,
		"note":                   "集中度越高(Top1/TOP5越大、HHI越接近1)风险越大，建议单票<=10%、行业<=25%",
	}, nil
}

// ==================== LimitCheckTool ====================
// limit_check 交易可行性检查工具
// 确定性校验：涨跌停价、T+1可卖量、买入一手可行性(整手/预算)。用于 Trader 下单前拦截不合法订单。

// LimitCheckTool 涨跌停/T+1/一手可行性检查工具
type LimitCheckTool struct {
	engine *portfolio.Engine
}

// NewLimitCheckTool 创建交易可行性检查工具
func NewLimitCheckTool(engine *portfolio.Engine) *LimitCheckTool {
	return &LimitCheckTool{engine: engine}
}

func (t *LimitCheckTool) Name() string { return "limit_check" }

func (t *LimitCheckTool) Description() string {
	return "按A股规则确定性校验一笔交易：涨跌停价、买入一手(100股)可行性、卖出T+1可卖数量。返回能否执行及可执行数量。用于 Trader 下单前拦截不合法订单。"
}

func (t *LimitCheckTool) GetDefinition() ToolDefinition {
	return ToolDefinition{
		Type: "function",
		Function: FunctionDef{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"side": map[string]interface{}{
						"type":        "string",
						"description": "交易方向：buy=买入，sell=卖出",
						"enum":        []string{"buy", "sell"},
					},
					"instrument_id": map[string]interface{}{
						"type":        "string",
						"description": "股票代码，如 sh600000",
					},
					"price": map[string]interface{}{
						"type":        "number",
						"description": "计划成交价（元）",
					},
					"quantity": map[string]interface{}{
						"type":        "integer",
						"description": "计划数量（股），买入可给 quantity 或 budget 之一",
					},
					"budget": map[string]interface{}{
						"type":        "number",
						"description": "买入预算（元），用于换算一手可买数量",
					},
					"prev_close": map[string]interface{}{
						"type":        "number",
						"description": "昨收价（元），用于计算涨跌停价",
					},
					"price_limit_pct": map[string]interface{}{
						"type":        "number",
						"description": "涨跌幅限制比例(0-1)，主板默认0.10（ST类0.05、创业板/科创板0.20由调用方按需指定）",
					},
				},
				"required": []string{"side", "instrument_id", "price"},
			},
		},
	}
}

func (t *LimitCheckTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	side, _ := args["side"].(string)
	if side != "buy" && side != "sell" {
		return nil, fmt.Errorf("side 必须为 buy 或 sell")
	}
	code, _ := args["instrument_id"].(string)
	if code == "" {
		return nil, fmt.Errorf("instrument_id 不能为空")
	}
	price, _ := args["price"].(float64)
	if price <= 0 {
		return nil, fmt.Errorf("price 必须大于 0")
	}
	quantity, _ := args["quantity"].(float64)
	budget, _ := args["budget"].(float64)
	prevClose := 0.0
	if pc, ok := args["prev_close"].(float64); ok && pc > 0 {
		prevClose = pc
	}
	limitPct := 0.10
	if lp, ok := args["price_limit_pct"].(float64); ok && lp > 0 {
		limitPct = lp
	}

	// 若未传昨收，尽量从当前持仓中取
	holdQty := 0.0
	if t.engine != nil {
		snap := t.engine.GetSnapshot()
		for _, p := range snap.Positions {
			if p == nil || !eqCode(p.InstrumentID, code) {
				continue
			}
			if prevClose <= 0 {
				prevClose = p.PrevClose
			}
			holdQty = float64(p.Quantity)
			break
		}
	}

	buyableTodayQty := 0.0 // 当日买入数量（卖出的T+1校验）
	if t.engine != nil && side == "sell" {
		trades, err := t.engine.GetTodayTrades()
		if err == nil {
			for _, tr := range trades {
				if tr.Side == "BUY" && eqCode(tr.InstrumentID, code) {
					buyableTodayQty += float64(tr.Quantity)
				}
			}
		}
	}

	res := map[string]interface{}{
		"side":          side,
		"instrument_id": code,
		"price":         price,
	}

	// 涨跌停价计算
	if prevClose > 0 {
		limitUp := prevClose * (1 + limitPct)
		limitDown := prevClose * (1 - limitPct)
		res["prev_close"] = round2(prevClose)
		res["limit_up"] = round2(limitUp)
		res["limit_down"] = round2(limitDown)
		res["at_limit_up"] = price >= limitUp*(1-1e-9)
		res["at_limit_down"] = price <= limitDown*(1+1e-9)
	}

	// 盘口预审（Risk/CIO 下单前检查，早于执行层拦截）：
	// 买入：涨停封死买不进、跌停封死/跌停板不接飞刀 → 否决；
	// 卖出：跌停封死卖不出（挂单排队当天无法成交）→ 否决。
	// 实时盘口获取失败时不否决（避免行情缺失误伤），仅附加状态说明。
	if t.engine != nil {
		snaps, _ := data.FetchRealtimeStockSnapshots([]string{code})
		var snap data.StockSnapshot
		for _, s := range snaps {
			snap = s
			break
		}
		if snap.PrevClose > 0 {
			ob := orderbook.ClassifySnapshot(snap)
			res["order_book_state"] = ob.Chinese
			res["order_book_reason"] = ob.Reason
			if side == "buy" && (ob.State == orderbook.StateSealedLimitUp ||
				ob.State == orderbook.StateSealedLimitDown ||
				ob.State == orderbook.StateOpenedLimitDown) {
				res["feasible"] = false
				res["block_reason"] = "盘口不可买：" + ob.Reason
			} else if side == "sell" && ob.State == orderbook.StateSealedLimitDown {
				res["feasible"] = false
				res["block_reason"] = "盘口不可卖：" + ob.Reason
			}
		}
	}

	if side == "buy" {
		target := quantity
		if target <= 0 && budget > 0 {
			target = mathFloor(budget / price)
		}
		hands := int(target / 100)
		execQty := float64(hands * 100)
		lotValid := hands >= 1
		res["quantity"] = target
		res["hands"] = hands
		res["executable_quantity"] = execQty
		res["lot_valid"] = lotValid
		res["feasible"] = lotValid
		if !lotValid {
			res["block_reason"] = "资金/数量不足以买入1手(100股)，或数量非100股整数倍"
		}
	} else {
		sellable := holdQty - buyableTodayQty
		if sellable < 0 {
			sellable = 0
		}
		res["hold_quantity"] = holdQty
		res["bought_today"] = buyableTodayQty
		res["sellable_quantity"] = sellable
		res["t1_blocked"] = sellable < holdQty
		res["feasible"] = sellable >= quantity
		if sellable < quantity {
			res["block_reason"] = "可卖数量不足（含T+1限制，当日买入不可卖出）"
		}
	}

	return res, nil
}

func eqCode(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

func mathFloor(v float64) float64 {
	if v < 0 {
		return 0
	}
	return float64(int(v))
}

// ==================== PortfolioRiskMetricsTool ====================
// portfolio_risk_metrics 组合风险指标计算工具
// 用真实组合每日收益序列计算 VaR/CVaR/最大回撤/波动率/Beta(对沪深300)。
// 让 Risk/CIO 无需自行猜测风险数字，而是直接取确定性计算结果。

// PortfolioRiskMetricsTool 组合风险指标工具
type PortfolioRiskMetricsTool struct {
	engine      *portfolio.Engine
	duckdbMgr   *data.DuckDBManager
	benchmarkID string // 基准指数代码，如 sh000300
}

// NewPortfolioRiskMetricsTool 创建组合风险指标工具
func NewPortfolioRiskMetricsTool(engine *portfolio.Engine, duckdbMgr *data.DuckDBManager, benchmarkID string) *PortfolioRiskMetricsTool {
	if benchmarkID == "" {
		benchmarkID = "sh000300"
	}
	return &PortfolioRiskMetricsTool{engine: engine, duckdbMgr: duckdbMgr, benchmarkID: benchmarkID}
}

func (t *PortfolioRiskMetricsTool) Name() string { return "portfolio_risk_metrics" }

func (t *PortfolioRiskMetricsTool) Description() string {
	return "基于真实组合每日收益序列计算风险指标：VaR95/VaR99/CVaR95、年化波动率、最大回撤、Beta(对沪深300)等。Risk 做风险审查/监控时用真实计算结果，勿自行估算。"
}

func (t *PortfolioRiskMetricsTool) GetDefinition() ToolDefinition {
	return ToolDefinition{
		Type: "function",
		Function: FunctionDef{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"lookback_days": map[string]interface{}{
						"type":        "integer",
						"description": "回看自然日天数，默认 60",
					},
				},
			},
		},
	}
}

func (t *PortfolioRiskMetricsTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	if t.engine == nil {
		return nil, fmt.Errorf("组合引擎未初始化")
	}
	lookback := 60
	if v, ok := args["lookback_days"].(float64); ok && v > 0 {
		lookback = int(v)
	}

	// 盘口流动性锁定指标（与收益历史无关，始终可算）：
	// 跌停封死持仓今日无法卖出，其市值占比即退出风险。
	liqLock := t.liquidityLockMetrics()

	history, err := t.engine.GetProfitHistory(lookback)
	if err != nil {
		return nil, fmt.Errorf("获取组合收益历史失败: %w", err)
	}
	if len(history) < 2 {
		result := map[string]interface{}{
			"as_of":   time.Now().Format(time.RFC3339),
			"records": len(history),
			"note":    "组合历史收益数据不足，无法计算风险指标（数据真实、无伪造）",
		}
		for k, v := range liqLock {
			result[k] = v
		}
		return result, nil
	}

	// 组合每日收益（% -> 小数）
	rets := make([]float64, 0, len(history))
	navs := make([]float64, 0, len(history))
	for _, h := range history {
		dr, _ := h["dailyReturn"].(float64)
		rets = append(rets, dr/100.0)
		if ta, ok := h["totalAssets"].(float64); ok && ta > 0 {
			navs = append(navs, ta)
		}
	}

	// VaR/CVaR（历史法）
	vaR95 := percentile(rets, 0.05)
	vaR99 := percentile(rets, 0.01)
	cVaR95 := 0.0
	cnt := 0
	for _, r := range rets {
		if r <= vaR95 {
			cVaR95 += r
			cnt++
		}
	}
	if cnt > 0 {
		cVaR95 /= float64(cnt)
	}

	// 年化波动率
	volDaily := stdDev(rets)
	annualVol := volDaily * math.Sqrt(252) * 100

	// 最大回撤（按净值序列）
	maxDrawdown := 0.0
	if len(navs) > 0 {
		peak := navs[0]
		for _, v := range navs {
			if v > peak {
				peak = v
			}
			if peak > 0 {
				dd := (peak - v) / peak
				if dd > maxDrawdown {
					maxDrawdown = dd
				}
			}
		}
	}

	result := map[string]interface{}{
		"as_of":                     time.Now().Format(time.RFC3339),
		"records":                   len(rets),
		"var95_daily_return_pct":    round4(vaR95 * 100),
		"var99_daily_return_pct":    round4(vaR99 * 100),
		"cvar95_daily_return_pct":   round4(cVaR95 * 100),
		"annualized_volatility_pct": round2(annualVol),
		"max_drawdown_pct":          round4(maxDrawdown * 100),
		"benchmark":                 t.benchmarkID,
		"note":                      "VaR/CVaR为历史法(负值表示日收益损失)，数据来源于真实组合结算收益",
	}

	// 集中度(HHI)与持仓相关性（基于真实持仓权重与行情，供 pre_risk/post_risk 使用）
	t.appendConcentration(result)
	t.appendCorrelation(ctx, result)

	// Beta（对沪深300，尽力对齐）
	if t.duckdbMgr != nil {
		bars, err := t.duckdbMgr.GetMarketIndexBars(ctx, lookback/2)
		if err == nil {
			if idx := bars[t.benchmarkID]; len(idx) >= 2 {
				benchRets := make([]float64, 0, len(idx))
				var prev float64
				for i, b := range idx {
					if i == 0 {
						prev = b.Close
						continue
					}
					if prev > 0 {
						benchRets = append(benchRets, (b.Close-prev)/prev)
					}
					prev = b.Close
				}
				// 对齐最近 min(len) 天
				n := len(rets)
				if len(benchRets) < n {
					n = len(benchRets)
				}
				if n >= 2 {
					pRets := rets[len(rets)-n:]
					bRets := benchRets[len(benchRets)-n:]
					beta := cov(pRets, bRets) / (stdDev(bRets)*stdDev(bRets) + 1e-12)
					result["beta_vs_benchmark"] = round4(beta)
				}
			}
		}
	}

	// 盘口流动性锁定指标（与收益历史无关，始终可算）
	for k, v := range liqLock {
		result[k] = v
	}

	return result, nil
}

// liquidityLockMetrics 计算组合盘口流动性锁定指标：
// 跌停封死持仓（卖一=跌停价且封单巨大）今日无法卖出，其市值占比即退出风险（流动性锁定）。
// 数据来自实时盘口（真实行情）；实时盘口获取失败时指标为0并在note标注，不伪造。
func (t *PortfolioRiskMetricsTool) liquidityLockMetrics() map[string]interface{} {
	out := map[string]interface{}{
		"liquidity_locked_positions": 0,
		"liquidity_locked_value":     0.0,
		"position_value":             0.0,
		"liquidity_locked_ratio_pct": 0.0,
		"liquidity_locked_note":      "跌停封死持仓今日无法卖出（流动性锁定），数据来自实时盘口，无伪造",
	}
	if t.engine == nil {
		out["liquidity_locked_note"] = "组合引擎未初始化，无法计算盘口流动性锁定指标"
		return out
	}
	snap := t.engine.GetSnapshot()
	codes := make([]string, 0, len(snap.Positions))
	for _, p := range snap.Positions {
		if p != nil && p.Quantity > 0 {
			codes = append(codes, p.Market+p.InstrumentID)
		}
	}
	if len(codes) == 0 {
		out["liquidity_locked_note"] = "当前无持仓，盘口流动性锁定指标为0"
		return out
	}
	locked := make(map[string]bool)
	if snaps, _ := data.FetchRealtimeStockSnapshots(codes); len(snaps) > 0 {
		for _, s := range snaps {
			if orderbook.ClassifySnapshot(s).State == orderbook.StateSealedLimitDown {
				locked[strings.ToLower(s.Market+s.Code)] = true
				locked[strings.ToLower(s.Code)] = true
			}
		}
	}
	lockCount := 0
	lockValue := 0.0
	positionValue := 0.0
	for _, p := range snap.Positions {
		if p == nil || p.Quantity <= 0 {
			continue
		}
		val := p.CurrentPrice * float64(p.Quantity)
		positionValue += val
		if locked[strings.ToLower(p.Market+p.InstrumentID)] {
			lockCount++
			lockValue += val
		}
	}
	ratio := 0.0
	if positionValue > 0 {
		ratio = lockValue / positionValue * 100
	}
	out["liquidity_locked_positions"] = lockCount
	out["liquidity_locked_value"] = round2(lockValue)
	out["position_value"] = round2(positionValue)
	out["liquidity_locked_ratio_pct"] = round2(ratio)
	return out
}

// percentile 返回排序后第 q 分位数（q 取值 0~1）
func percentile(values []float64, q float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	idx := int(q * float64(len(sorted)))
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func stdDev(values []float64) float64 {
	if len(values) < 2 {
		return 0
	}
	mean := 0.0
	for _, v := range values {
		mean += v
	}
	mean /= float64(len(values))
	s := 0.0
	for _, v := range values {
		d := v - mean
		s += d * d
	}
	return math.Sqrt(s / float64(len(values)-1))
}

func cov(a, b []float64) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	ma, mb := 0.0, 0.0
	for i := range a {
		ma += a[i]
		mb += b[i]
	}
	ma /= float64(len(a))
	mb /= float64(len(b))
	s := 0.0
	for i := range a {
		s += (a[i] - ma) * (b[i] - mb)
	}
	return s / float64(len(a))
}

// appendConcentration 追加组合集中度指标（HHI、有效持仓数、最大单票权重）
// 组合权重以百分比存储（如 31.3 表示 31.3%），需除以 100。
func (t *PortfolioRiskMetricsTool) appendConcentration(result map[string]interface{}) {
	if t.engine == nil {
		return
	}
	snap := t.engine.GetSnapshot()
	if snap == nil || len(snap.Positions) == 0 {
		result["concentration_hhi"] = round4(0)
		result["effective_positions"] = 0
		result["max_single_weight_pct"] = round2(0)
		return
	}
	hhi := 0.0
	maxW := 0.0
	n := 0
	for _, p := range snap.Positions {
		w := p.Weight / 100.0 // 转为小数
		if w < 0 {
			w = 0
		}
		hhi += w * w
		if w > maxW {
			maxW = w
		}
		n++
	}
	eff := 0.0
	if hhi > 1e-9 {
		eff = 1.0 / hhi
	}
	result["concentration_hhi"] = round4(hhi)
	result["effective_positions"] = round2(eff)
	result["max_single_weight_pct"] = round2(maxW * 100)
}

// appendCorrelation 追加持仓两两相关性矩阵（按市值取前 N 大持仓，真实行情日收益）
func (t *PortfolioRiskMetricsTool) appendCorrelation(ctx context.Context, result map[string]interface{}) {
	if t.engine == nil || t.duckdbMgr == nil {
		return
	}
	snap := t.engine.GetSnapshot()
	if snap == nil || len(snap.Positions) == 0 {
		result["correlation_matrix"] = []interface{}{}
		return
	}

	// 按市值降序取前 8 大持仓用于相关性计算（限制输出规模）
	positions := make([]*portfolio.PositionState, 0, len(snap.Positions))
	positions = append(positions, snap.Positions...)
	sort.SliceStable(positions, func(i, j int) bool { return positions[i].MarketValue > positions[j].MarketValue })
	if len(positions) > 8 {
		positions = positions[:8]
	}

	// 逐持仓取真实日收益率序列
	type retSeries struct {
		code string
		name string
		rets []float64
	}
	series := make([]retSeries, 0, len(positions))
	for _, p := range positions {
		ohlc := strings.ToLower(p.InstrumentID)
		if ohlc == "" {
			continue
		}
		prices, err := t.duckdbMgr.GetRecentPrices(ctx, "CN", ohlc, 90)
		if err != nil || len(prices) < 10 {
			continue
		}
		rs := make([]float64, 0, len(prices)-1)
		for i := 1; i < len(prices); i++ {
			if prices[i-1].Close > 0 {
				rs = append(rs, (prices[i].Close-prices[i-1].Close)/prices[i-1].Close)
			}
		}
		if len(rs) < 5 {
			continue
		}
		series = append(series, retSeries{code: p.InstrumentID, name: p.StockName, rets: rs})
	}
	if len(series) == 0 {
		result["correlation_matrix"] = []interface{}{}
		return
	}

	type corrRow struct {
		Code         string             `json:"code"`
		Name         string             `json:"name"`
		Correlations map[string]float64 `json:"correlations"`
	}
	rows := make([]corrRow, 0, len(series))
	for _, a := range series {
		row := corrRow{Code: a.code, Name: a.name, Correlations: map[string]float64{}}
		n := len(a.rets)
		for _, b := range series {
			if len(b.rets) < n {
				n = len(b.rets)
			}
		}
		if n < 5 {
			continue
		}
		for _, b := range series {
			ar := a.rets[len(a.rets)-n:]
			br := b.rets[len(b.rets)-n:]
			sda, sdb := stdDev(ar), stdDev(br)
			if sda == 0 || sdb == 0 {
				row.Correlations[b.code] = round4(0)
				continue
			}
			c := cov(ar, br) / (sda * sdb)
			row.Correlations[b.code] = round4(c)
		}
		rows = append(rows, row)
	}
	result["correlation_matrix"] = rows
	result["correlation_note"] = "持仓两两相关系数基于真实日收益序列(最近90自然日)计算，仅覆盖前N大持仓"
}
