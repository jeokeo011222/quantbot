package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"time"

	"github.com/quantpilot/quantpilot/internal/backtest"
	"github.com/quantpilot/quantpilot/internal/data"
	"github.com/quantpilot/quantpilot/internal/tdx"
	"github.com/quantpilot/quantpilot/internal/util"
)

// ==================== PositionManagerTool 精细化仓位管理工具 ====================
// position_manager 供 CIO/Quant 智能体在决策时调用，替代简单粗暴的固定比例建仓/加仓/减仓。
//
// 内置三类精细化方法（均由数据库 position_manager_configs 表配置，不写死参数）：
//  1. 分批建仓 + 金字塔加仓：先建底仓，价格向上突破触发档位时分批加仓、逐级递减加仓量。
//  2. 动态减仓 / 移动止盈 / 止损：按利润分档减仓、移动止盈回撤触发减仓、跌破止损强制清仓。
//  3. ATR 波动率仓位：用真实 K 线计算 ATR，按"单笔风险预算 = 组合资产×风险比例"反推可买股数，
//     高波动自动降仓，避免高波动标的单票仓位过大。
//
// 禁止：伪造数据。ATR/最新价一律来自 DuckDB 真实 K 线；无数据时直接报错，不做任何估算。
// A股约束：买入数量按 100 股整数手向下取整；当日买入的仓位不参与当日减仓（T+1），工具在结果中标注。

// PositionManagerConfigParams 仓位管理工具配置参数（与 position_manager_configs.config_json 对应）
type PositionManagerConfigParams struct {
	// ATR 波动率仓位
	ATRPeriod      int     `json:"atr_period"`          // ATR 周期，默认14
	ATRStopMulti   float64 `json:"atr_stop_multiplier"` // 止损距离 = ATR×倍数，默认2.0
	RiskPerTrade   float64 `json:"risk_per_trade"`      // 单笔风险占组合资产比例，默认0.01
	MaxPositionPct float64 `json:"max_position_pct"`    // 单票最大仓位比例，默认0.15

	// 分批建仓 + 金字塔加仓
	BaseLotPct      []float64 `json:"base_lot_pct"`     // 底仓占目标仓位比例（首档），默认[0.5]
	PyramidTriggers []float64 `json:"pyramid_triggers"` // 加仓触发涨幅(%)，默认[3,6,10]
	PyramidLots     []float64 `json:"pyramid_lots"`     // 各档加仓量占目标仓位比例，默认[0.3,0.2,0.1]

	// 动态减仓 / 移动止盈 / 止损
	TakeProfitPct     float64   `json:"take_profit_pct"`     // 目标止盈（总收益率），默认0.20
	StopLossPct       float64   `json:"stop_loss_pct"`       // 止损（亏损率），默认0.05
	TrailStartPct     float64   `json:"trail_start_pct"`     // 移动止盈启动利润(%)，默认10
	TrailDrawdownPct  float64   `json:"trail_drawdown_pct"`  // 移动止盈回撤(%)，默认5
	TrailReduceRatio  float64   `json:"trail_reduce_ratio"`  // 移动止盈触发时减仓比例，默认0.3
	ReduceTierProfits []float64 `json:"reduce_tier_profits"` // 减仓档位利润(%)，默认[10,20,30]
	ReduceTierLots    []float64 `json:"reduce_tier_lots"`    // 各档减仓比例，默认[0.2,0.3,0.5]

	// 组合
	CashReservePct float64 `json:"cash_reserve_pct"` // 现金保留比例，默认0.10

	// 交易时间纪律（默认与规范一致，可配置）：按时段控制建仓/加仓/减仓
	TimeWindows []util.TradingWindowConfig `json:"time_windows"`
}

// defaultPositionManagerConfig 代码内置默认参数（数据库无配置时回退使用）
func defaultPositionManagerConfig() PositionManagerConfigParams {
	return PositionManagerConfigParams{
		ATRPeriod:         14,
		ATRStopMulti:      2.0,
		RiskPerTrade:      0.01,
		MaxPositionPct:    0.15,
		BaseLotPct:        []float64{0.5},
		PyramidTriggers:   []float64{3, 6, 10},
		PyramidLots:       []float64{0.3, 0.2, 0.1},
		TakeProfitPct:     0.20,
		StopLossPct:       0.05,
		TrailStartPct:     10,
		TrailDrawdownPct:  5,
		TrailReduceRatio:  0.3,
		ReduceTierProfits: []float64{10, 20, 30},
		ReduceTierLots:    []float64{0.2, 0.3, 0.5},
		CashReservePct:    0.10,
		// 交易时间纪律（与用户规范一致，见 util.DefaultTradingWindows）：
		// 10:00前只观察不操作；10:00-11:20企稳建底仓小幅加仓；
		// 13:30-14:20二次优化仓位低吸高抛做T；14:40后停止开仓加仓只分批止盈减弱势仓。
		TimeWindows: util.DefaultTradingWindows(),
	}
}

// PositionManagerTool 仓位管理工具
type PositionManagerTool struct {
	sqliteManager *data.SQLiteManager
	duckdbManager *data.DuckDBManager
}

// NewPositionManagerTool 创建仓位管理工具
func NewPositionManagerTool(sqliteManager *data.SQLiteManager, duckdbManager *data.DuckDBManager) *PositionManagerTool {
	return &PositionManagerTool{sqliteManager: sqliteManager, duckdbManager: duckdbManager}
}

func (t *PositionManagerTool) Name() string { return "position_manager" }

func (t *PositionManagerTool) Description() string {
	return "精细化仓位管理工具：基于真实ATR波动率做仓位缩放，分批建仓+金字塔加仓、利润分档减仓/移动止盈/止损。内置交易时间纪律：10:00前只观察不操作、10:00-11:20建底仓小幅加仓、13:30-14:20优化做T、14:40后停止开仓只止盈减仓（硬止损例外）。参数与时段规则来自数据库position_manager_configs（可用set_config更新）。CIO决策建仓/加仓/减仓时调用action=plan。"
}

func (t *PositionManagerTool) GetDefinition() ToolDefinition {
	return ToolDefinition{
		Type: "function",
		Function: FunctionDef{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"action": map[string]interface{}{
						"type":        "string",
						"description": "操作类型：plan=完整建仓+减仓方案(默认), entry=仅建仓/加仓, reduce=仅减仓/止盈止损, get_config=读取当前配置, set_config=更新配置",
						"enum":        []string{"plan", "entry", "reduce", "get_config", "set_config"},
					},
					"symbol": map[string]interface{}{
						"type":        "string",
						"description": "股票代码，如 sh600519 / sz000001（不区分大小写）",
					},
					"current_price": map[string]interface{}{
						"type":        "number",
						"description": "当前价格，不传则用 DuckDB 最近收盘价",
					},
					"target_value": map[string]interface{}{
						"type":        "number",
						"description": "目标仓位市值（元），不传则由风险预算与单票上限自动推导",
					},
					"existing_shares": map[string]interface{}{
						"type":        "integer",
						"description": "已有持仓股数（减仓/加仓必须传）",
					},
					"avg_cost": map[string]interface{}{
						"type":        "number",
						"description": "持仓平均成本（减仓必须传）",
					},
					"portfolio_value": map[string]interface{}{
						"type":        "number",
						"description": "组合总资产（元），用于风险预算与单票上限",
					},
					"cash_available": map[string]interface{}{
						"type":        "number",
						"description": "当前可用现金（元）",
					},
					"config": map[string]interface{}{
						"type":        "object",
						"description": "set_config 时的完整参数对象（字段与 get_config 返回一致）",
					},
				},
				"required": []string{"action"},
			},
		},
	}
}

// ==================== 结果结构 ====================

// PositionManagerReport 完整仓位方案报告
type PositionManagerReport struct {
	Symbol        string                      `json:"symbol"`
	CurrentPrice  float64                     `json:"current_price"`
	ATR           float64                     `json:"atr"`
	ATRPeriod     int                         `json:"atr_period"`
	VolatilityPct float64                     `json:"volatility_pct"`   // ATR/价格
	Window        *WindowInfo                 `json:"window,omitempty"` // 当前交易时段纪律
	Entry         *EntryPlan                  `json:"entry,omitempty"`
	Reduce        *ReducePlan                 `json:"reduce,omitempty"`
	Warnings      []string                    `json:"warnings,omitempty"`
	Config        PositionManagerConfigParams `json:"config"`
}

// WindowInfo 当前所处交易时段信息
type WindowInfo struct {
	Name      string `json:"name"`       // 时段名称
	InWindow  bool   `json:"in_window"`  // 是否落在已配置的主动时段
	Now       string `json:"now"`        // 当前时间 HH:MM
	AllowBuy  bool   `json:"allow_buy"`  // 允许建仓
	AllowAdd  bool   `json:"allow_add"`  // 允许加仓
	AllowSell bool   `json:"allow_sell"` // 允许减仓/止盈
	Note      string `json:"note"`       // 时段操作说明
}

// EntryPlan 建仓/加仓方案
type EntryPlan struct {
	Action        string         `json:"action"` // BUILD=建仓 / ADD=加仓 / HOLD=不买入
	TargetValue   float64        `json:"target_value"`
	RiskLimited   float64        `json:"risk_limited_value"` // 风险预算限制后的目标市值
	BaseLotValue  float64        `json:"base_lot_value"`
	BaseLotShares int            `json:"base_lot_shares"` // 底仓股数（100股整数倍）
	PyramidLevels []PyramidLevel `json:"pyramid_levels"`
	Reason        string         `json:"reason"`
}

// PyramidLevel 金字塔加仓档位
type PyramidLevel struct {
	Level        int     `json:"level"`
	TriggerPct   float64 `json:"trigger_pct"`   // 触发涨幅(%)
	TriggerPrice float64 `json:"trigger_price"` // 触发价格
	Shares       int     `json:"shares"`        // 该档加仓股数
	Value        float64 `json:"value"`         // 该档加仓金额
}

// ReducePlan 减仓/止盈/止损方案
type ReducePlan struct {
	Action         string       `json:"action"` // REDUCE=减仓 / HOLD=持有 / SELL_ALL=止损清仓
	ProfitPct      float64      `json:"profit_pct"`
	StopLossPrice  float64      `json:"stop_loss_price"`
	TrailStopPrice float64      `json:"trail_stop_price,omitempty"`
	ReduceShares   int          `json:"reduce_shares"` // 当前应减仓股数
	ReduceValue    float64      `json:"reduce_value"`
	Tiers          []ReduceTier `json:"tiers"`
	Reason         string       `json:"reason"`
}

// ReduceTier 减仓档位
type ReduceTier struct {
	Tier      int     `json:"tier"`
	ProfitPct float64 `json:"profit_pct"` // 该档触发利润(%)
	Price     float64 `json:"price"`      // 触发价格
	Ratio     float64 `json:"ratio"`      // 减仓比例
	Shares    int     `json:"shares"`     // 触发时减仓股数
}

// Execute 执行仓位管理工具
func (t *PositionManagerTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	if t.sqliteManager == nil || t.duckdbManager == nil {
		return nil, fmt.Errorf("SQLite/DuckDB 未初始化，无法执行仓位管理")
	}

	action, _ := args["action"].(string)
	if action == "" {
		action = "plan"
	}

	switch action {
	case "get_config":
		return t.getConfig()
	case "set_config":
		return t.setConfig(args)
	}

	// plan / entry / reduce 需要 symbol 与真实行情
	symbol, ok := args["symbol"].(string)
	if !ok || symbol == "" {
		return nil, fmt.Errorf("symbol 必填（如 sh600519）")
	}

	cfg, err := t.loadConfig()
	if err != nil {
		return nil, err
	}

	price := numArg(args, "current_price", 0)
	portfolioValue := numArg(args, "portfolio_value", 0)
	cashAvailable := numArg(args, "cash_available", 0)
	targetValue := numArg(args, "target_value", 0)
	existingShares := intArg(args, "existing_shares", 0)
	avgCost := numArg(args, "avg_cost", 0)

	// 从 DuckDB 取真实 K 线计算 ATR 与最新价（严禁伪造）
	bars, err := backtest.GetKlineFromDuckDB(t.duckdbManager, symbol, 60)
	if err != nil || len(bars) == 0 {
		return nil, fmt.Errorf("无法获取 %s 的真实K线数据（ATR 需真实数据，禁止估算）", symbol)
	}
	atr := computeATR(bars, cfg.ATRPeriod)
	lastClose := bars[len(bars)-1].Close
	if price <= 0 {
		price = lastClose
	}
	if atr <= 0 {
		return nil, fmt.Errorf("%s K线数据不足，ATR 无法计算（至少需 %d 根）", symbol, cfg.ATRPeriod+1)
	}

	report := &PositionManagerReport{
		Symbol:        symbol,
		CurrentPrice:  price,
		ATR:           atr,
		ATRPeriod:     cfg.ATRPeriod,
		VolatilityPct: atr / price * 100,
		Config:        cfg,
	}

	// 组合上下文缺失时的兜底：无法做风险预算/单票上限，仅给 ATR 波动提示
	if portfolioValue <= 0 {
		report.Warnings = append(report.Warnings, "未提供 portfolio_value，无法做风险预算与单票上限约束，仅给出价格/ATR 参考")
	}

	// 建仓/加仓方案
	if action == "plan" || action == "entry" {
		report.Entry = t.buildEntryPlan(cfg, price, atr, portfolioValue, cashAvailable, targetValue, existingShares)
	}

	// 减仓方案（需要持仓成本）
	if action == "plan" || action == "reduce" {
		if existingShares > 0 && avgCost > 0 {
			report.Reduce = t.buildReducePlan(cfg, price, existingShares, avgCost)
		} else {
			report.Warnings = append(report.Warnings, "未提供 existing_shares/avg_cost，跳过减仓方案（减仓需持仓成本）")
		}
	}

	// 交易时间纪律门控：按时段限制建仓/加仓/减仓（硬止损 SELL_ALL 不受时段限制）
	now := time.Now()
	win := util.TradingWindowAt(cfg.TimeWindows, now)
	report.Window = &WindowInfo{
		Name:      win.Name,
		InWindow:  win.InWindow,
		Now:       now.Format("15:04"),
		AllowBuy:  win.AllowBuy,
		AllowAdd:  win.AllowAdd,
		AllowSell: win.AllowSell,
		Note:      win.Note,
	}
	if report.Entry != nil {
		gateEntryByWindow(report.Entry, win)
	}
	if report.Reduce != nil {
		gateReduceByWindow(report.Reduce, win)
	}

	return report, nil
}

// ==================== 建仓 / 加仓 ====================

func (t *PositionManagerTool) buildEntryPlan(cfg PositionManagerConfigParams, price, atr, portfolioValue, cashAvailable, targetValue float64, existingShares int) *EntryPlan {
	plan := &EntryPlan{Action: "HOLD", PyramidLevels: []PyramidLevel{}}

	// 目标仓位市值：显式目标 > 风险预算 > 现金预算 > 单票上限
	target := targetValue
	if target <= 0 && portfolioValue > 0 {
		target = portfolioValue * cfg.MaxPositionPct
	}

	// 单票上限约束（含已有持仓）
	if portfolioValue > 0 {
		capValue := portfolioValue*cfg.MaxPositionPct - float64(existingShares)*price
		if target > capValue {
			target = capValue
		}
	}

	// 现金预算约束（保留现金比例）
	if cashAvailable > 0 {
		budget := cashAvailable * (1 - cfg.CashReservePct)
		if target > budget {
			target = budget
		}
	}

	// ATR 风险预算：单笔风险 = 组合资产×RiskPerTrade，可买股数 = 风险 / (ATR×ATRStopMulti)
	riskLimited := target
	if portfolioValue > 0 && atr > 0 {
		riskBudget := portfolioValue * cfg.RiskPerTrade
		sharesByRisk := riskBudget / (atr * cfg.ATRStopMulti)
		riskLimited = sharesByRisk * price
		if riskLimited < target {
			target = riskLimited
		}
	}

	// 不足1手或风险预算过小则不建仓
	if target < 100*price {
		plan.Reason = fmt.Sprintf("目标市值 %.0f 元不足1手(%.0f元)，放弃建仓", target, 100*price)
		return plan
	}

	// 底仓
	baseLotPct := 0.5
	if len(cfg.BaseLotPct) > 0 && cfg.BaseLotPct[0] > 0 {
		baseLotPct = cfg.BaseLotPct[0]
	}
	baseShares := floorLots(target * baseLotPct / price)
	if baseShares < 100 {
		baseShares = 0 // 底仓不足1手则整体放弃（避免零散建仓）
	}
	if baseShares == 0 {
		plan.Reason = fmt.Sprintf("底仓不足1手，放弃建仓（目标市值 %.0f 元）", target)
		return plan
	}

	plan.Action = "BUILD"
	if existingShares > 0 {
		plan.Action = "ADD"
	}
	plan.TargetValue = target
	plan.RiskLimited = riskLimited
	plan.BaseLotValue = float64(baseShares) * price
	plan.BaseLotShares = baseShares

	// 金字塔加仓档位
	triggers := cfg.PyramidTriggers
	lots := cfg.PyramidLots
	n := len(triggers)
	if len(lots) < n {
		n = len(lots)
	}
	for i := 0; i < n; i++ {
		if lots[i] <= 0 || triggers[i] <= 0 {
			continue
		}
		triggerPrice := price * (1 + triggers[i]/100)
		shares := floorLots(target * lots[i] / price)
		if shares < 100 {
			continue
		}
		plan.PyramidLevels = append(plan.PyramidLevels, PyramidLevel{
			Level:        i + 1,
			TriggerPct:   triggers[i],
			TriggerPrice: round2(triggerPrice),
			Shares:       shares,
			Value:        round2(float64(shares) * triggerPrice),
		})
	}

	if existingShares > 0 {
		plan.Reason = fmt.Sprintf("已有 %d 股，按金字塔加仓 %d 档，目标仓位市值 %.0f 元（含已有）",
			existingShares, len(plan.PyramidLevels), target+float64(existingShares)*price)
	} else {
		plan.Reason = fmt.Sprintf("分批建仓：底仓 %d 股 + 金字塔加仓 %d 档，目标仓位市值 %.0f 元",
			baseShares, len(plan.PyramidLevels), target)
	}
	return plan
}

// ==================== 减仓 / 止盈 / 止损 ====================

func (t *PositionManagerTool) buildReducePlan(cfg PositionManagerConfigParams, price float64, shares int, avgCost float64) *ReducePlan {
	plan := &ReducePlan{
		Action:        "HOLD",
		ProfitPct:     round2((price - avgCost) / avgCost * 100),
		StopLossPrice: round2(avgCost * (1 - cfg.StopLossPct)),
		Tiers:         []ReduceTier{},
	}

	// 1) 止损：跌破成本×（1-止损比例）→ 清仓
	if price <= plan.StopLossPrice {
		plan.Action = "SELL_ALL"
		plan.ReduceShares = shares
		plan.ReduceValue = round2(float64(shares) * price)
		plan.Reason = fmt.Sprintf("跌破止损价 %.2f（成本 %.2f×%.0f%%），清仓 %d 股",
			plan.StopLossPrice, avgCost, cfg.StopLossPct*100, shares)
		return plan
	}

	// 2) 减仓档位：利润达到档位触发价 → 按对应比例减仓
	profits := cfg.ReduceTierProfits
	ratios := cfg.ReduceTierLots
	n := len(profits)
	if len(ratios) < n {
		n = len(ratios)
	}
	bestShares := 0
	bestTier := 0
	bestProfit := 0.0
	for i := 0; i < n; i++ {
		tierProfit := profits[i]
		tierRatio := ratios[i]
		if tierProfit <= 0 || tierRatio <= 0 {
			continue
		}
		tierPrice := avgCost * (1 + tierProfit/100)
		tierShares := floorLots(float64(shares) * tierRatio)
		plan.Tiers = append(plan.Tiers, ReduceTier{
			Tier:      i + 1,
			ProfitPct: tierProfit,
			Price:     round2(tierPrice),
			Ratio:     tierRatio,
			Shares:    tierShares,
		})
		// 已触发且为最高档 → 采用该档
		if plan.ProfitPct >= tierProfit && tierShares > bestShares {
			bestShares = tierShares
			bestTier = i + 1
			bestProfit = tierProfit
		}
	}
	if bestShares > 0 {
		plan.Action = "REDUCE"
		plan.ReduceShares = bestShares
		plan.ReduceValue = round2(float64(bestShares) * price)
		plan.Reason = fmt.Sprintf("利润 %.2f%% 触发第%d档止盈（≥%.0f%%），减仓 %d 股",
			plan.ProfitPct, bestTier, bestProfit, bestShares)
		return plan
	}

	// 3) 移动止盈：利润≥启动线后，从当前价回撤达阈值 → 减仓
	if plan.ProfitPct >= cfg.TrailStartPct {
		trailStopPrice := price * (1 - cfg.TrailDrawdownPct/100)
		plan.TrailStopPrice = round2(trailStopPrice)
		trailShares := floorLots(float64(shares) * cfg.TrailReduceRatio)
		if trailShares > 0 {
			plan.Action = "REDUCE"
			plan.ReduceShares = trailShares
			plan.ReduceValue = round2(float64(trailShares) * price)
			plan.Reason = fmt.Sprintf("利润 %.2f%% 已达移动止盈启动线(%.0f%%)，回撤阈值 %.0f%%（跌破 %.2f 触发），当前建议减仓 %d 股",
				plan.ProfitPct, cfg.TrailStartPct, cfg.TrailDrawdownPct, trailStopPrice, trailShares)
			return plan
		}
	}

	// 4) 目标止盈：总收益率达到目标 → 全部止盈
	if plan.ProfitPct >= cfg.TakeProfitPct*100 {
		plan.Action = "REDUCE"
		plan.ReduceShares = shares
		plan.ReduceValue = round2(float64(shares) * price)
		plan.Reason = fmt.Sprintf("利润 %.2f%% 已达目标止盈(%.0f%%)，全部止盈 %d 股",
			plan.ProfitPct, cfg.TakeProfitPct*100, shares)
		return plan
	}

	plan.Reason = fmt.Sprintf("当前利润 %.2f%%，未触发止损/止盈/移动止盈，持有", plan.ProfitPct)
	return plan
}

// ==================== 配置管理 ====================

func (t *PositionManagerTool) loadConfig() (PositionManagerConfigParams, error) {
	cfg := defaultPositionManagerConfig()
	if t.sqliteManager == nil {
		return cfg, nil
	}
	var row data.PositionManagerConfig
	err := t.sqliteManager.GetDB().Where("name = ?", "default").First(&row).Error
	if err != nil {
		// 无配置则按默认参数入库（保证 config 有落库来源）
		if err := t.persistConfig(cfg); err != nil {
			log.Printf("[PositionManager] 初始化默认配置失败: %v", err)
		}
		return cfg, nil
	}
	if err := json.Unmarshal([]byte(row.ConfigJSON), &cfg); err != nil {
		log.Printf("[PositionManager] 配置JSON解析失败，回退默认: %v", err)
		return defaultPositionManagerConfig(), nil
	}
	cfg = normalizeConfig(cfg)
	return cfg, nil
}

func (t *PositionManagerTool) persistConfig(cfg PositionManagerConfigParams) error {
	b, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	var count int64
	t.sqliteManager.GetDB().Model(&data.PositionManagerConfig{}).Where("name = ?", "default").Count(&count)
	if count == 0 {
		return t.sqliteManager.GetDB().Create(&data.PositionManagerConfig{
			Name:       "default",
			ConfigJSON: string(b),
		}).Error
	}
	return t.sqliteManager.GetDB().Model(&data.PositionManagerConfig{}).
		Where("name = ?", "default").
		Updates(map[string]interface{}{"config_json": string(b), "updated_at": time.Now()}).Error
}

func (t *PositionManagerTool) getConfig() (interface{}, error) {
	cfg, err := t.loadConfig()
	if err != nil {
		return nil, err
	}
	updatedAt := ""
	var row data.PositionManagerConfig
	if err := t.sqliteManager.GetDB().Where("name = ?", "default").First(&row).Error; err == nil {
		updatedAt = row.UpdatedAt.Format("2006-01-02 15:04:05")
	}
	return map[string]interface{}{
		"config":     cfg,
		"updated_at": updatedAt,
		"source":     "position_manager_configs(default)",
	}, nil
}

func (t *PositionManagerTool) setConfig(args map[string]interface{}) (interface{}, error) {
	cfgRaw, ok := args["config"]
	if !ok || cfgRaw == nil {
		return nil, fmt.Errorf("set_config 需要传入 config 参数对象")
	}
	b, err := json.Marshal(cfgRaw)
	if err != nil {
		return nil, fmt.Errorf("config 序列化失败: %w", err)
	}
	var cfg PositionManagerConfigParams
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, fmt.Errorf("config 解析失败: %w", err)
	}
	cfg = normalizeConfig(cfg)
	if err := t.persistConfig(cfg); err != nil {
		return nil, fmt.Errorf("配置保存失败: %w", err)
	}
	log.Printf("[PositionManager] 配置已更新: %s", string(b))
	return map[string]interface{}{
		"ok":         true,
		"config":     cfg,
		"updated_at": time.Now().Format("2006-01-02 15:04:05"),
	}, nil
}

// ==================== 工具函数 ====================

// normalizeConfig 校验并归一化配置（禁止零/负/空数组破坏计算）
func normalizeConfig(cfg PositionManagerConfigParams) PositionManagerConfigParams {
	d := defaultPositionManagerConfig()
	if cfg.ATRPeriod < 2 {
		cfg.ATRPeriod = d.ATRPeriod
	}
	if cfg.ATRStopMulti <= 0 {
		cfg.ATRStopMulti = d.ATRStopMulti
	}
	if cfg.RiskPerTrade <= 0 || cfg.RiskPerTrade > 0.1 {
		cfg.RiskPerTrade = d.RiskPerTrade
	}
	if cfg.MaxPositionPct <= 0 || cfg.MaxPositionPct > 1 {
		cfg.MaxPositionPct = d.MaxPositionPct
	}
	if len(cfg.BaseLotPct) == 0 || cfg.BaseLotPct[0] <= 0 || cfg.BaseLotPct[0] > 1 {
		cfg.BaseLotPct = d.BaseLotPct
	}
	if len(cfg.PyramidTriggers) == 0 || len(cfg.PyramidLots) == 0 {
		cfg.PyramidTriggers = d.PyramidTriggers
		cfg.PyramidLots = d.PyramidLots
	}
	if cfg.TakeProfitPct <= 0 {
		cfg.TakeProfitPct = d.TakeProfitPct
	}
	if cfg.StopLossPct <= 0 {
		cfg.StopLossPct = d.StopLossPct
	}
	if cfg.TrailStartPct <= 0 {
		cfg.TrailStartPct = d.TrailStartPct
	}
	if cfg.TrailDrawdownPct <= 0 {
		cfg.TrailDrawdownPct = d.TrailDrawdownPct
	}
	if cfg.TrailReduceRatio <= 0 || cfg.TrailReduceRatio > 1 {
		cfg.TrailReduceRatio = d.TrailReduceRatio
	}
	if len(cfg.ReduceTierProfits) == 0 || len(cfg.ReduceTierLots) == 0 {
		cfg.ReduceTierProfits = d.ReduceTierProfits
		cfg.ReduceTierLots = d.ReduceTierLots
	}
	if cfg.CashReservePct < 0 {
		cfg.CashReservePct = d.CashReservePct
	}
	if len(cfg.TimeWindows) == 0 {
		cfg.TimeWindows = d.TimeWindows
	}
	return cfg
}

// ==================== 交易时间纪律门控 ====================
// 时段匹配逻辑见 util.TradingWindowAt（与可交易股票池等路径共用同一套规则）

// gateEntryByWindow 按时段门控建仓/加仓方案
func gateEntryByWindow(plan *EntryPlan, win util.TradingWindowState) {
	if plan == nil {
		return
	}
	switch plan.Action {
	case "BUILD":
		if !win.AllowBuy {
			plan.Reason = fmt.Sprintf("当前时段[%s]禁止建仓：%s", win.Name, win.Note)
			plan.Action = "HOLD"
			plan.BaseLotShares = 0
			plan.BaseLotValue = 0
			plan.PyramidLevels = nil
		}
	case "ADD":
		if !win.AllowAdd {
			plan.Reason = fmt.Sprintf("当前时段[%s]禁止加仓：%s", win.Name, win.Note)
			plan.Action = "HOLD"
			plan.PyramidLevels = nil
		}
	}
}

// gateReduceByWindow 按时段门控减仓方案。
// 硬止损（SELL_ALL）是风险控制，任何时候都放行；普通止盈减仓受时段限制。
func gateReduceByWindow(plan *ReducePlan, win util.TradingWindowState) {
	if plan == nil {
		return
	}
	if plan.Action == "SELL_ALL" {
		return // 止损清仓不受时段限制
	}
	if plan.Action == "REDUCE" && !win.AllowSell {
		plan.Reason = fmt.Sprintf("当前时段[%s]禁止减仓止盈：%s", win.Name, win.Note)
		plan.Action = "HOLD"
		plan.ReduceShares = 0
		plan.ReduceValue = 0
	}
}

// computeATR 计算 ATR（真实 K 线，与 risk 引擎同公式）
func computeATR(bars []tdx.KlineBar, period int) float64 {
	n := len(bars)
	if n < period+1 {
		return 0
	}
	trueRanges := make([]float64, 0, n-1)
	for i := 1; i < n; i++ {
		tr := bars[i].High - bars[i].Low
		hc := math.Abs(bars[i].High - bars[i-1].Close)
		lc := math.Abs(bars[i].Low - bars[i-1].Close)
		if hc > tr {
			tr = hc
		}
		if lc > tr {
			tr = lc
		}
		trueRanges = append(trueRanges, tr)
	}
	if len(trueRanges) < period {
		period = len(trueRanges)
	}
	sum := 0.0
	for i := len(trueRanges) - period; i < len(trueRanges); i++ {
		sum += trueRanges[i]
	}
	return sum / float64(period)
}

// floorLots 按 100 股整数手向下取整
func floorLots(shares float64) int {
	if shares <= 0 {
		return 0
	}
	lot := int(shares / 100)
	return lot * 100
}

func numArg(args map[string]interface{}, key string, def float64) float64 {
	if v, ok := args[key].(float64); ok {
		return v
	}
	return def
}

func intArg(args map[string]interface{}, key string, def int) int {
	switch v := args[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return def
}
