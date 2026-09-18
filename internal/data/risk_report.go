package data

import "time"

// MarketRiskReport 组合风险报告（"研究中心-风险管理"界面数据）。
// 指标口径与风控师同源：intelligence.RiskEngine.ComputePortfolioRisk（VaR/CVaR/波动/回撤/结构/压力）
// + policy.GetHardLimits 硬限制合规校验。StressTests/LimitsJSON/Violations 为 JSON 文本列。
type MarketRiskReport struct {
	ID            uint      `gorm:"primaryKey" json:"id"`
	ReportDate    string    `json:"report_date"` // 2006-01-02
	Source        string    `json:"source"`      // manual / pre_market / intraday / post_mortem
	PositionCount int       `json:"position_count"`
	TotalExposure float64   `json:"total_exposure"`

	// 组合风险指标（与风控师同引擎同口径）
	VaR95         float64 `json:"var_95"`
	VaR99         float64 `json:"var_99"`
	CVaR95        float64 `json:"cvar_95"`
	Volatility    float64 `json:"volatility"`
	MaxDrawdown   float64 `json:"max_drawdown"`
	Beta          float64 `json:"beta"`
	Correlation   float64 `json:"correlation"`
	Concentration float64 `json:"concentration"`
	EMD           float64 `json:"emd"`
	Liquidity     float64 `json:"liquidity"`
	OverallScore  float64 `json:"overall_score"`
	Status        string  `json:"status"` // NORMAL / WATCH / WARNING / CRITICAL

	// 压力测试情景（JSON 数组）、硬限制校验结果（JSON）、违规项（JSON 数组）
	StressTests string `json:"stress_tests"`
	LimitsJSON  string `json:"limits_json"`
	Violations  string `json:"violations"`

	CreatedAt time.Time `json:"created_at"`
}

// TableName 指定表名（与 GORM 默认复数表名一致）
func (MarketRiskReport) TableName() string {
	return "market_risk_reports"
}