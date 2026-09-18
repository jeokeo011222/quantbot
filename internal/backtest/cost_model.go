package backtest

import "math"

// CostBreakdown 单笔交易成本分解（元）。
//
// 对标 cvxportfolio 的 TcostModel 三因子模型：
//
//	cost = a·|x| + b·σ·|x|^1.5/√V + c·x
//
//   - a·|x|   ：比例费用 —— A股为佣金（买卖都收，最低5元）+ 印花税（仅卖出 0.05%）
//   - b·σ·|x|^1.5/√V：市场冲击（Almgren-Chriss 平方根冲击律），σ=日波动率、V=日成交额
//   - c·x     ：收盘 bias —— 收盘价成交 + close-to-close 收益口径一致时默认 c=0（无需 bias）
type CostBreakdown struct {
	Commission float64 `json:"commission"` // 佣金（a 项）
	StampDuty  float64 `json:"stampDuty"`  // 印花税（a 项，仅卖出）
	Impact     float64 `json:"impact"`     // 市场冲击（b 项）
	Bias       float64 `json:"bias"`       // 收盘 bias（c 项）
}

// Total 单笔交易总成本（元）
func (c CostBreakdown) Total() float64 {
	return c.Commission + c.StampDuty + c.Impact + c.Bias
}

// CostModel 交易成本三因子模型配置（对标 cvxportfolio TcostModel / StocksTransactionCost）
type CostModel struct {
	// CommissionRate 佣金率（比例，买卖都收），A股默认万三 0.0003
	CommissionRate float64
	// MinCommission 单笔佣金最低收费（元），A股默认 5
	MinCommission float64
	// StampDutyRate 印花税率（仅卖出收取），A股默认 0.0005（0.05%）
	StampDutyRate float64
	// ImpactCoeff 市场冲击系数 b（平方根冲击律），默认 1.0；设为 0 关闭冲击项
	ImpactCoeff float64
	// ImpactExponent 冲击指数，默认 1.5（Almgren-Chriss 平方根律）
	ImpactExponent float64
	// BiasCoeff 收盘 bias 系数 c。收盘价成交 + close-to-close 收益口径一致时默认 0；
	// 若以开盘价成交、按 open-to-close 收益计，则需设为 -1 以扣除当日盘中收益
	BiasCoeff float64
	// VolWindow 日波动率 σ 估计窗口（根K线，用 trailing 收益标准差），默认 60
	VolWindow int
}

// DefaultCostModel 返回 A 股默认成本模型（佣金万三最低5元 + 卖出印花税0.05% + 市场冲击 b=1.0）
func DefaultCostModel() *CostModel {
	return &CostModel{
		CommissionRate: 0.0003,
		MinCommission:  5,
		StampDutyRate:  0.0005,
		ImpactCoeff:    1.0,
		ImpactExponent: 1.5,
		BiasCoeff:      0.0,
		VolWindow:      60,
	}
}

// BuyCost 买入交易成本（元）：佣金（最低5元）+ 冲击 + bias，无印花税
func (m *CostModel) BuyCost(tradedAmount, dailyVol, sigma float64) CostBreakdown {
	commission := tradedAmount * m.CommissionRate
	if commission < m.MinCommission {
		commission = m.MinCommission
	}
	return CostBreakdown{
		Commission: commission,
		Impact:     m.impact(tradedAmount, dailyVol, sigma),
		Bias:       m.BiasCoeff * tradedAmount,
	}
}

// SellCost 卖出交易成本（元）：佣金（最低5元）+ 印花税 + 冲击 + bias
func (m *CostModel) SellCost(tradedAmount, dailyVol, sigma float64) CostBreakdown {
	commission := tradedAmount * m.CommissionRate
	if commission < m.MinCommission {
		commission = m.MinCommission
	}
	return CostBreakdown{
		Commission: commission,
		StampDuty:  tradedAmount * m.StampDutyRate,
		Impact:     m.impact(tradedAmount, dailyVol, sigma),
		Bias:       m.BiasCoeff * tradedAmount,
	}
}

// impact 市场冲击项 b·σ·|x|^1.5/√V（x=单笔成交额元，V=当日成交额元，σ=日波动率）
func (m *CostModel) impact(tradedAmount, dailyVol, sigma float64) float64 {
	if m.ImpactCoeff <= 0 || tradedAmount <= 0 || dailyVol <= 0 || sigma <= 0 {
		return 0
	}
	impact := m.ImpactCoeff * sigma * math.Pow(tradedAmount, m.ImpactExponent) / math.Sqrt(dailyVol)
	if impact < 0 {
		return 0
	}
	return impact
}
