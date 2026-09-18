package tools

import (
	"context"
	"sort"

	"github.com/quantpilot/quantpilot/internal/data"
)

// ==================== get_sector_rotation（行业/板块轮动，P5） ====================

// SectorRotationTool 基于 DuckDB 真实行情计算各行业最新交易日成分股平均涨跌幅，
// 输出板块强弱排名与市场轮动状态（普涨/普跌/结构性轮动/分化）。
type SectorRotationTool struct {
	duckdb *data.DuckDBManager
}

func NewSectorRotationTool(m *data.DuckDBManager) *SectorRotationTool {
	return &SectorRotationTool{duckdb: m}
}

func (t *SectorRotationTool) Name() string { return "get_sector_rotation" }

func (t *SectorRotationTool) Description() string {
	return "获取行业/板块轮动景气：分行业成分股平均涨跌幅排名(top/底部)、上涨行业占比、板块集中度，并判定当前是普涨/普跌/结构性轮动/分化。数据来自DuckDB真实最新交易日行情，供选股与仓位行业配置参考"
}

func (t *SectorRotationTool) GetDefinition() ToolDefinition {
	return ToolDefinition{
		Type: "function",
		Function: FunctionDef{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters: map[string]interface{}{"type": "object", "properties": map[string]interface{}{
				"top": map[string]interface{}{"type": "integer", "description": "返回前N强板块，默认5"},
			}},
		},
	}
}

func (t *SectorRotationTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	if t.duckdb == nil {
		return map[string]interface{}{"status": "unavailable", "message": "DuckDB 未初始化"}, nil
	}
	sectors, err := t.duckdb.GetSectorChangeStats(ctx)
	if err != nil || len(sectors) == 0 {
		return map[string]interface{}{"status": "unavailable", "message": "行业板块行情不可用（数据未更新到最新交易日）"}, nil
	}
	top := 5
	if v, ok := args["top"].(float64); ok && v > 0 {
		top = int(v)
	}

	type row struct {
		Sector     string  `json:"sector"`
		StockCount int     `json:"stock_count"`
		ChgPct     float64 `json:"chg_pct"`
	}
	valid := make([]row, 0, len(sectors))
	var sum float64
	var upCnt int
	for _, s := range sectors {
		if s.StockCount < 3 {
			continue // 成分过少不具代表性
		}
		valid = append(valid, row{s.Sector, s.StockCount, round2(s.AvgChgPct)})
		sum += s.AvgChgPct
		if s.AvgChgPct > 0 {
			upCnt++
		}
	}
	sort.SliceStable(valid, func(i, j int) bool { return valid[i].ChgPct > valid[j].ChgPct })
	if len(valid) == 0 {
		return map[string]interface{}{"status": "unavailable", "message": "无有效行业板块数据"}, nil
	}
	avg := sum / float64(len(valid))
	upRatio := float64(upCnt) / float64(len(valid)) * 100

	// 集中度：前3板块平均 vs 全体平均；判断资金主线是否集中
	topAgg := 0.0
	nTop := 3
	if len(valid) < nTop {
		nTop = len(valid)
	}
	for i := 0; i < nTop && i < len(valid); i++ {
		topAgg += valid[i].ChgPct
	}
	if nTop > 0 {
		topAgg /= float64(nTop)
	}
	concentrated := topAgg > avg+1.0

	verdict := "结构性轮动（涨跌互现）"
	switch {
	case upRatio >= 70 && len(valid) > 5:
		verdict = "普涨（多数板块走强）"
	case upRatio <= 30 && len(valid) > 5:
		verdict = "普跌（多数板块走弱，谨慎）"
	case concentrated && len(valid) > 5:
		verdict = "集中轮动（资金集中于少数主线板块）"
	}

	take := top
	if take > len(valid) {
		take = len(valid)
	}
	topRows := valid[:take]
	bottomRows := []row{}
	if len(valid)-3 > take {
		bottom := valid[len(valid)-3:]
		bottomRows = bottom
	}
	take2 := 3
	if take2 > len(bottomRows) {
		take2 = len(bottomRows)
	}
	bottomRows = bottomRows[:take2]

	return map[string]interface{}{
		"status":              "ok",
		"up_sector_ratio_pct": round2(upRatio),
		"avg_chg_pct":         round2(avg),
		"top_sectors":         topRows,
		"bottom_sectors":      bottomRows,
		"rotation_state":      verdict,
		"concentrated":        concentrated,
		"source":              "DuckDB 最新交易日行业成分股平均涨跌幅",
	}, nil
}

// ==================== get_skill_catalog（技能层目录，P4） ====================

// SkillCatalogTool 以"技能"维度组织本系统已注册的方向性能力，向智能体/前端呈现可组合的技能层，
// 便于按场景组合调用底层原子工具。MCP 服务器传输接入作为独立立项，不在本工具内实现。
type SkillCatalogTool struct{}

func NewSkillCatalogTool() *SkillCatalogTool { return &SkillCatalogTool{} }

func (t *SkillCatalogTool) Name() string { return "get_skill_catalog" }

func (t *SkillCatalogTool) Description() string {
	return "返回量化投研·技能目录：将系统已注册的原子工具按领域组合为可复用技能(每技能列出实现它的工具名)，供智能体按场景挑选技能组合。数据为静态目录"
}

func (t *SkillCatalogTool) GetDefinition() ToolDefinition {
	return ToolDefinition{
		Type: "function",
		Function: FunctionDef{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters:  map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		},
	}
}

func (t *SkillCatalogTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	skills := []map[string]interface{}{
		{"id": "skill_market_regime", "name": "市场状态判势", "tools": []string{"market_sixdim_detect", "get_index_technical", "get_market_breadth", "get_sector_rotation", "get_etf_monitor"}},
		{"id": "skill_capital_flow", "name": "资金流向分析", "tools": []string{"get_capital_flow", "get_turnover", "get_stock_fund_flow", "get_lhb"}},
		{"id": "skill_risk_control", "name": "风险与回撤控制", "tools": []string{"portfolio_risk_view", "portfolio_metrics", "calculate_structure_risk", "get_performance_nav"}},
		{"id": "skill_stock_screening", "name": "选股与因子", "tools": []string{"screen_stock", "factor_review", "get_factor_health_report", "get_alpha_by_regime", "get_alpha_top"}},
		{"id": "skill_strategy_research", "name": "策略研发与回测", "tools": []string{"select_next_day_strategy", "estimate_strategy_generalization", "rank_strategy_generalization", "backtest"}},
		{"id": "skill_external_news", "name": "信息与事件", "tools": []string{"get_market_news", "get_stock_announcement", "get_external_market", "get_lhb"}},
		{"id": "skill_self_calibration", "name": "智能体自校准确", "tools": []string{"get_claim_accuracy", "get_performance_nav"}},
		{"id": "skill_portfolio", "name": "组合与方案管理", "tools": []string{"get_investment_plan", "get_current_position", "portfolio_optimize", "rebalance"}},
	}
	return map[string]interface{}{
		"status": "ok",
		"skills": skills,
		"note":   "每项技能列出实现它的原子工具名；MCP 服务器传输接入另行立项",
		"source": "内置技能目录",
	}, nil
}
