package data

import "time"

// PortfolioOptimization 投资组合优化的单次运行记录（"投资组合中心"数据）。
// 记录优化算法、生成的目标权重、风险贡献与年化指标，供历史回看。
type PortfolioOptimization struct {
	ID            uint      `gorm:"primaryKey" json:"id"`
	Method        string    `json:"method"`     // mvo / risk_parity / risk_budget
	Symbols       string    `json:"symbols"`    // JSON 数组：参与优化的资产代码
	Weights       string    `json:"weights"`    // JSON 数组：[{symbol,name,weight,risk_pct}]
	TotalWeight   float64   `json:"total_weight"` // 分配给风险资产的合计比重
	AnnualReturn  float64   `json:"annual_return"`
	Volatility    float64   `json:"volatility"`
	Sharpe        float64   `json:"sharpe"`
	IsCurrentHold bool      `json:"is_current_hold"` // 是否基于当前持仓
	Remark        string    `json:"remark"`
	CreatedAt     time.Time `json:"created_at"`
}

// TableName 指定表名
func (PortfolioOptimization) TableName() string {
	return "portfolio_optimizations"
}