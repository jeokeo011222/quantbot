package tools

import (
	"context"
	"fmt"
	"math"

	"github.com/quantpilot/quantpilot/internal/data"
	"github.com/quantpilot/quantpilot/internal/marketinfo"
	"github.com/quantpilot/quantpilot/internal/portfolio"
)

// ==================== 注入型依赖 ====================

// FundFlowProvider 个股资金流向提供者（由 App 层注入，走真实 TDX 资金流向数据）。
type FundFlowProvider func(ctx context.Context, symbol string) (interface{}, error)

// CurrentPlanProvider 当前投资方案提供者（由 App 层注入，读 planner 数据库）。
type CurrentPlanProvider func() (interface{}, error)

// ClaimAccuracyProvider 可证伪声明命中率提供者（P0，由 App 层注 cio.ClaimAccuracy）。
type ClaimAccuracyProvider func() map[string]interface{}

// ==================== get_stock_fund_flow（个股资金流向） ====================

// StockFundFlowTool 获取单只股票真实主力/大单/小单资金流向（TDX 通达信终端）。
type StockFundFlowTool struct {
	fundFlowProvider FundFlowProvider
}

func NewStockFundFlowTool(p FundFlowProvider) *StockFundFlowTool {
	return &StockFundFlowTool{fundFlowProvider: p}
}

func (t *StockFundFlowTool) Name() string { return "get_stock_fund_flow" }

func (t *StockFundFlowTool) Description() string {
	return "获取单只股票的真实资金流向(TDX通达信终端)：主力/超大单/大单/中单/小单净流入与占成交比。建仓前判断主力资金动向、排查资金面风险。数据真实，来源已标注。"
}

func (t *StockFundFlowTool) GetDefinition() ToolDefinition {
	return ToolDefinition{
		Type: "function",
		Function: FunctionDef{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"symbol": map[string]interface{}{"type": "string", "description": "股票代码，如 600519 或 sh600519"},
				},
				"required": []string{"symbol"},
			},
		},
	}
}

func (t *StockFundFlowTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	symbol, _ := args["symbol"].(string)
	if symbol == "" {
		return nil, fmt.Errorf("缺少 symbol 参数")
	}

	// 主源：TDX 通达信终端；不可用/失败时自动降级到东方财富 push2his 公开接口。
	if t.fundFlowProvider != nil {
		flows, err := t.fundFlowProvider(ctx, symbol)
		if err == nil {
			return map[string]interface{}{
				"status": "ok", "symbol": symbol, "result": flows, "source": "TDX通达信终端 个股资金流向",
			}, nil
		}
	}

	// 降级源：东方财富 push2his（JSON 直达、无反爬门槛）
	flow, src, ferr := marketinfo.FetchEastMoneyFundFlow(ctx, symbol, 5)
	if ferr == nil && flow != nil {
		return map[string]interface{}{
			"status": "ok", "symbol": symbol, "result": flow, "source": "TDX→EastMoney 资金流(" + src + ")",
		}, nil
	}

	reason := "数据源(TDX)未注入"
	if t.fundFlowProvider != nil {
		reason = "TDX不可用"
	}
	return map[string]interface{}{
		"status":  "unavailable",
		"symbol":  symbol,
		"message": fmt.Sprintf("%s，东财降级失败: %v", reason, simpleErr(ferr)),
	}, nil
}

// ==================== calculate_structure_risk（组合结构风险） ====================

// StructureRiskTool 对一组股票按真实日K计算结构风险（波动率/最大回撤/数据充分性）。
type StructureRiskTool struct {
	duckDB *data.DuckDBManager
}

func NewStructureRiskTool(dm *data.DuckDBManager) *StructureRiskTool {
	return &StructureRiskTool{duckDB: dm}
}

func (t *StructureRiskTool) Name() string { return "calculate_structure_risk" }

func (t *StructureRiskTool) Description() string {
	return "对给定候选/持仓股票集合计算结构风险(DuckDB真实日K)：年化波动率、最大回撤、数据充分性，并给出每只及整体风险等级(LOW/MED/HIGH)。Risk审查候选池/计划持仓结构风险使用。"
}

func (t *StructureRiskTool) GetDefinition() ToolDefinition {
	return ToolDefinition{
		Type: "function",
		Function: FunctionDef{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"stock_codes": map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}, "description": "股票代码列表，如 600519/sz000858"},
					"days":        map[string]interface{}{"type": "integer", "description": "回看天数(默认60)"},
				},
				"required": []string{"stock_codes"},
			},
		},
	}
}

func (t *StructureRiskTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	if t.duckDB == nil || !t.duckDB.HasStockDB() {
		return nil, fmt.Errorf("DuckDB 不可用，无法计算结构风险")
	}
	codes := toStringSlice(args["stock_codes"])
	if len(codes) == 0 {
		return nil, fmt.Errorf("缺少 stock_codes 参数")
	}
	days, _ := args["days"].(int)
	if days < 20 {
		days = 60
	}

	details := make([]map[string]interface{}, 0, len(codes))
	for _, code := range codes {
		prices, err := t.duckDB.GetRecentPrices(ctx, "cn", code, days)
		if err != nil || len(prices) < 5 {
			details = append(details, map[string]interface{}{"code": code, "status": "insufficient_data", "data_points": len(prices)})
			continue
		}
		ret := make([]float64, 0, len(prices)-1)
		for i := 1; i < len(prices); i++ {
			if prices[i-1].Close > 0 {
				ret = append(ret, (prices[i].Close-prices[i-1].Close)/prices[i-1].Close)
			}
		}
		vol, mdd := retVolMaxDD(ret)

		// 风险等级：年化波动率阈值（LOW<25% MED<40% HIGH≥40%）
		lvl := "MED"
		if vol < 25 || len(ret) < 10 {
			lvl = "LOW"
		} else if vol >= 40 {
			lvl = "HIGH"
		}
		// 极端回撤强制高危
		if mdd < -30 {
			lvl = "HIGH"
		}
		details = append(details, map[string]interface{}{
			"code": code, "status": "ok", "data_points": len(prices),
			"annualized_vol_pct": round2(vol), "max_drawdown_pct": round2(mdd), "risk_level": lvl,
		})
	}

	// 整体风险等级：取最高档
	overall := "LOW"
	for _, d := range details {
		if d["status"] == "ok" {
			if d["risk_level"] == "HIGH" {
				overall = "HIGH"
				break
			}
			if d["risk_level"] == "MED" {
				overall = "MED"
			}
		}
	}
	return map[string]interface{}{
		"status":       "ok",
		"total_stocks": len(codes),
		"evaluated":    countStatusOk(details),
		"overall_risk": overall,
		"risk_details": details,
		"source":       "DuckDB stock 真实日K(年化波动率/最大回撤)",
	}, nil
}

func retVolMaxDD(ret []float64) (annVol, maxDD float64) {
	if len(ret) < 2 {
		return 0, 0
	}
	mean := 0.0
	for _, r := range ret {
		mean += r
	}
	mean /= float64(len(ret))
	var v float64
	for _, r := range ret {
		d := r - mean
		v += d * d
	}
	std := math.Sqrt(v / float64(len(ret)-1))
	annVol = std * math.Sqrt(252) * 100

	// 最大回撤
	peak := 1.0
	equity := 1.0
	for _, r := range ret {
		equity *= (1 + r)
		if equity > peak {
			peak = equity
		}
		dd := (equity - peak) / peak * 100
		if dd < maxDD {
			maxDD = dd
		}
	}
	return annVol, maxDD
}

// ==================== get_etf_monitor（ETF 共振判势） ====================

// ==================== get_performance_nav（组合绩效/净值） ====================

// PerformanceNavTool 读取组合每日结算绩效（净值、累计/当日收益、回撤、资产）。
type PerformanceNavTool struct {
	engine *portfolio.Engine
}

func NewPerformanceNavTool(e *portfolio.Engine) *PerformanceNavTool {
	return &PerformanceNavTool{engine: e}
}

func (t *PerformanceNavTool) Name() string { return "get_performance_nav" }

func (t *PerformanceNavTool) Description() string {
	return "读取组合真实结算绩效(sqlite每日结算)：最新净值(1+累计收益)、累计收益%、当日收益%、最大回撤%、总资产/现金/持仓市值/持仓数。CIO复盘与归因时核对自身真实表现使用。数据真实，来源已标注。"
}

func (t *PerformanceNavTool) GetDefinition() ToolDefinition {
	return ToolDefinition{
		Type: "function",
		Function: FunctionDef{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"days": map[string]interface{}{"type": "integer", "description": "回看天数(默认120，用于最大回撤)"},
				},
			},
		},
	}
}

func (t *PerformanceNavTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	if t.engine == nil {
		return nil, fmt.Errorf("组合引擎未初始化")
	}
	days, _ := args["days"].(int)
	if days < 20 {
		days = 120
	}
	hist, err := t.engine.GetProfitHistory(days)
	if err != nil {
		return nil, fmt.Errorf("获取绩效历史失败: %w", err)
	}
	out := map[string]interface{}{"source": "sqlite portfolio_daily_stats 每日结算"}

	// 最大回撤：基于总资产序列
	maxDD := 0.0
	peak := math.Inf(-1)
	latestReturn := 0.0
	for _, h := range hist {
		if ta, ok := h["totalAssets"].(float64); ok && ta > 0 {
			if ta > peak {
				peak = ta
			}
			dd := (ta - peak) / peak * 100
			if dd < maxDD {
				maxDD = dd
			}
		}
		if r, ok := h["totalReturn"].(float64); ok {
			latestReturn = r
		}
	}
	out["nav"] = round2(1 + latestReturn/100)
	out["cumulative_return_pct"] = round2(latestReturn)
	out["max_drawdown_pct"] = round2(maxDD)
	out["observations"] = len(hist)

	if stat, err := t.engine.GetLatestDailyStat(); err == nil && stat != nil {
		out["latest_date"] = stat.StatDate
		out["daily_return_pct"] = round2(stat.DailyReturn)
		out["total_assets"] = round2(stat.TotalAssets)
		out["cash"] = round2(stat.Cash)
		out["market_value"] = round2(stat.MarketValue)
		out["positions_count"] = stat.PositionsCount
	} else {
		out["latest_stat"] = "无当日结算数据"
	}
	return out, nil
}

// ==================== get_investment_plan（当前投资方案） ====================

// InvestmentPlanTool 读取当前生效的投资方案（planner 数据库）。
type InvestmentPlanTool struct {
	currentPlanProvider CurrentPlanProvider
}

func NewInvestmentPlanTool(p CurrentPlanProvider) *InvestmentPlanTool {
	return &InvestmentPlanTool{currentPlanProvider: p}
}

func (t *InvestmentPlanTool) Name() string { return "get_investment_plan" }

func (t *InvestmentPlanTool) Description() string {
	return "读取当前投资方案(investment_plans数据库)：计划名称、风险等级、目标收益/回撤、任务书(mandate)、股票池及各股配置权重。CIO/Planner核对计划与实际持仓是否一致使用。数据真实。"
}

func (t *InvestmentPlanTool) GetDefinition() ToolDefinition {
	return ToolDefinition{
		Type: "function",
		Function: FunctionDef{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters:  map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		},
	}
}

func (t *InvestmentPlanTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	if t.currentPlanProvider == nil {
		return map[string]interface{}{"status": "unavailable", "message": "投资方案提供者未注入"}, nil
	}
	plan, err := t.currentPlanProvider()
	if err != nil {
		return map[string]interface{}{"status": "unavailable", "message": fmt.Sprintf("投资方案不可用: %v", simpleErr(err))}, nil
	}
	return map[string]interface{}{"status": "ok", "plan": plan, "source": "investment_plans 数据库"}, nil
}

// ==================== get_claim_accuracy（可证伪判断命中率，P0） ====================

// ClaimAccuracyTool 返回智能体可证伪声明台账的命中率统计（P0）：总样本/已验证/命中数/准确率，
// 以及命中与未命中时的平均信心，供 CIO/Quant 盘前判断自身近期的判断质量并据此校准信心。
type ClaimAccuracyTool struct {
	provider ClaimAccuracyProvider
}

func NewClaimAccuracyTool(p ClaimAccuracyProvider) *ClaimAccuracyTool {
	return &ClaimAccuracyTool{provider: p}
}

func (t *ClaimAccuracyTool) Name() string { return "get_claim_accuracy" }

func (t *ClaimAccuracyTool) Description() string {
	return "返回智能体可证伪判断台账的命中率：总样本/已用真实收益验证数/命中数/准确率%，以及命中与未命中各自的平均信心。用于盘前复盘自己近期判断准不准，据此校准信心。数据来自 claim_store 真实对账。"
}

func (t *ClaimAccuracyTool) GetDefinition() ToolDefinition {
	return ToolDefinition{
		Type: "function",
		Function: FunctionDef{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters:  map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		},
	}
}

func (t *ClaimAccuracyTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	if t.provider == nil {
		return map[string]interface{}{"status": "unavailable", "message": "命中率提供者未注入"}, nil
	}
	stats := t.provider()
	stats["status"] = "ok"
	stats["source"] = "claim_store 可证伪声明台账（真实收益对账）"
	return stats, nil
}

// ==================== get_factor_health_report（因子健康度） ====================

// FactorHealthTool 读取最近一次 factor_review 持久化的真实因子质量画像(FactorQuality表)。
type FactorHealthTool struct {
	sqliteManager *data.SQLiteManager
}

func NewFactorHealthTool(m *data.SQLiteManager) *FactorHealthTool {
	return &FactorHealthTool{sqliteManager: m}
}

func (t *FactorHealthTool) Name() string { return "get_factor_health_report" }

func (t *FactorHealthTool) Description() string {
	return "读取最近一次 factor_review 复盘持久化的真实因子质量画像(FactorQuality表 [0,1])，按质量划分 HEALTHY/NORMAL/WEAK。" +
		"支持：report=查看因子质量画像与原始观测指标；set_pinned=固定/取消固定某因子(pin覆盖，固定后因子复盘不再自动校准其质量分)；history=查看因子校准历史审计。" +
		"Quant判断哪些因子可用使用。仅报告真实已复盘因子，未复盘则提示先运行 factor_review。"
}

func (t *FactorHealthTool) GetDefinition() ToolDefinition {
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
						"enum":        []string{"report", "set_pinned", "history"},
						"description": "操作：report=查看因子质量画像(默认)；set_pinned=固定/取消固定某因子；history=查看因子校准历史审计",
					},
					"factor": map[string]interface{}{
						"type":        "string",
						"description": "因子名（如 momentum_20d / sentiment_score），set_pinned 时必填",
					},
					"pinned": map[string]interface{}{
						"type":        "boolean",
						"description": "set_pinned 时指定是否固定该因子（true=固定不自动校准，false=取消固定恢复自动校准）",
					},
				},
				"required": []string{},
			},
		},
	}
}

func (t *FactorHealthTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	if t.sqliteManager == nil || t.sqliteManager.GetDB() == nil {
		return nil, fmt.Errorf("数据库未初始化")
	}
	action, _ := args["action"].(string)
	if action == "" {
		action = "report"
	}

	switch action {
	case "set_pinned":
		return t.setPinned(ctx, args)
	case "history":
		return t.calibrationHistory(ctx, args)
	default:
		return t.qualityReport(ctx)
	}
}

// qualityReport 查看因子质量画像（含最近一次真实观测指标与 pin 状态）
func (t *FactorHealthTool) qualityReport(ctx context.Context) (interface{}, error) {
	var recs []data.FactorQuality
	if err := t.sqliteManager.GetDB().Order("quality DESC").Find(&recs).Error; err != nil {
		return nil, fmt.Errorf("读取因子质量失败: %w", err)
	}
	if len(recs) == 0 {
		return map[string]interface{}{
			"status":  "no_data",
			"message": "FactorQuality 表暂无数据，请先运行 factor_review 工具进行因子复盘",
			"source":  "FactorQuality 表",
		}, nil
	}
	items := make([]map[string]interface{}, 0, len(recs))
	for _, r := range recs {
		status := "WEAK"
		if r.Quality >= 0.6 {
			status = "HEALTHY"
		} else if r.Quality >= 0.4 {
			status = "NORMAL"
		}
		items = append(items, map[string]interface{}{
			"factor": r.FactorName, "quality": r.Quality, "status": status,
			"detail":           r.Detail,
			"rank_ic":          r.RankIC,
			"accuracy":         r.Accuracy,
			"coverage":         r.Coverage,
			"stability":        r.Stability,
			"decay":            r.Decay,
			"pinned":           r.IsPinned,
			"auto_calibrate":   r.AutoCalibrate,
			"quality_smoothed": true,
			"updated_at":       r.UpdatedAt.Format("2006-01-02"),
		})
	}
	return map[string]interface{}{
		"status":        "ok",
		"factors":       items,
		"source":        "FactorQuality 表(factor_review 每夜真实计算，质量分经EMA平滑)",
		"quality_scale": "quality∈[0,1]，≥0.6=HEALTHY / ≥0.4=NORMAL / <0.4=WEAK",
		"note":          "rank_ic/accuracy/coverage/stability/decay 为最近一次真实复盘原始值；pinned=true 的因子不参与自动校准",
	}, nil
}

// setPinned 固定/取消固定某因子（pin 覆盖，对标 PanWatch FactorWeight.is_pinned）。
func (t *FactorHealthTool) setPinned(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	factor, _ := args["factor"].(string)
	if factor == "" {
		return nil, fmt.Errorf("set_pinned 需要指定 factor（因子名）")
	}
	pinned, _ := args["pinned"].(bool)

	db := t.sqliteManager.GetDB()
	var rec data.FactorQuality
	if err := db.Where("factor_name = ?", factor).First(&rec).Error; err != nil {
		return nil, fmt.Errorf("因子 %s 暂无质量记录，请先运行 factor_review 复盘后再固定", factor)
	}
	oldPinned := rec.IsPinned
	rec.IsPinned = pinned
	if err := db.Save(&rec).Error; err != nil {
		return nil, fmt.Errorf("更新 pin 状态失败: %w", err)
	}
	return map[string]interface{}{
		"status":          "ok",
		"factor":          factor,
		"pinned":          pinned,
		"changed":         oldPinned != pinned,
		"note":            "pinned=true 时 factor_review 将跳过该因子质量分的自动校准（保留当前质量分，仅刷新观测指标）",
		"current_quality": rec.Quality,
	}, nil
}

// calibrationHistory 查看因子校准历史审计（EMA平滑/权重变更轨迹，对标 PanWatch FactorWeightHistory）。
func (t *FactorHealthTool) calibrationHistory(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	db := t.sqliteManager.GetDB()
	q := db.Order("created_at DESC").Limit(50)
	if factor, _ := args["factor"].(string); factor != "" {
		q = q.Where("factor_name = ?", factor)
	}
	var recs []data.FactorCalibrationHistory
	if err := q.Find(&recs).Error; err != nil {
		return nil, fmt.Errorf("读取校准历史失败: %w", err)
	}
	items := make([]map[string]interface{}, 0, len(recs))
	for _, r := range recs {
		items = append(items, map[string]interface{}{
			"factor":      r.FactorName,
			"old_quality": r.OldQuality,
			"new_quality": r.NewQuality,
			"rank_ic":     r.RankIC,
			"accuracy":    r.Accuracy,
			"coverage":    r.Coverage,
			"stability":   r.Stability,
			"decay":       r.Decay,
			"reason":      r.Reason,
			"created_at":  r.CreatedAt.Format("2006-01-02 15:04"),
		})
	}
	return map[string]interface{}{
		"status":  "ok",
		"records": items,
		"source":  "FactorCalibrationHistory 表(每次实际校准变化自动审计)",
	}, nil
}

// ==================== 辅助 ====================

func toStringSlice(v interface{}) []string {
	raw, ok := v.([]interface{})
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, x := range raw {
		if s, ok := x.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

func countStatusOk(in []map[string]interface{}) int {
	n := 0
	for _, d := range in {
		if d["status"] == "ok" {
			n++
		}
	}
	return n
}
