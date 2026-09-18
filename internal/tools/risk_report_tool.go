package tools

import (
	"context"
)

// RiskReportProvider 生成日终组合风险报告（由 main 注入）。
// 内部封装 riskcenter.ComputeReport + riskcenter.SaveReport，与"研究中心-风险管理"
// 页面走完全同一套引擎与持久化；返回可读的摊平 map。
//
// 之所以注入而不是在 tools 包内直接 import riskcenter，是因为
// riskcenter -> policy -> agents -> tools 构成导入环，故按本项目惯例
// （FundFlowProvider/CurrentPlanProvider 等）用函数式注入解耦。
type RiskReportProvider func() (interface{}, error)

// RiskReportTool 组合风险报告工具（AI 智能体日终风险审查专用）。
// 口径与风险中心页面一致，结果自动落库 market_risk_reports。
type RiskReportTool struct {
	provider RiskReportProvider
}

// NewRiskReportTool 创建组合风险报告工具。
func NewRiskReportTool(p RiskReportProvider) *RiskReportTool {
	return &RiskReportTool{provider: p}
}

func (t *RiskReportTool) Name() string { return "run_daily_risk_report" }

func (t *RiskReportTool) Description() string {
	return "基于组合当前真实持仓+DuckDB行情生成日终组合风险报告（与风险中心页面同一引擎并落库）：VaR95/99、CVaR95、年化波动率、最大回撤、Beta、集中度、相关性、流动性、综合风险评分与状态，并做压力测试与硬限制合规校验。RISK 日终风险审查必须调用本工具（勿自行估算指标）。"
}

func (t *RiskReportTool) GetDefinition() ToolDefinition {
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

// Execute 调用 main 注入的 provider 计算并持久化组合风险报告。
func (t *RiskReportTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	if t.provider == nil {
		return map[string]interface{}{
			"status":  "unavailable",
			"message": "daily risk report provider 未注入",
		}, nil
	}
	return t.provider()
}
