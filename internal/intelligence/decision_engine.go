package intelligence

import (
	"math"
)

type DecisionEngine struct{}

func NewDecisionEngine() *DecisionEngine {
	return &DecisionEngine{}
}

type InvestmentDecision struct {
	Action       string   `json:"action"`
	Symbol       string   `json:"symbol"`
	TargetWeight float64  `json:"target_weight"`
	Confidence   float64  `json:"confidence"`
	Reasoning    string   `json:"reasoning"`
	RiskFlags    []string `json:"risk_flags"`
	Timeframe    string   `json:"timeframe"`
}

type InvestmentMandate struct {
	MaxDrawdownPct    float64 `json:"max_drawdown_pct"`
	MaxSinglePosition float64 `json:"max_single_position"`
	MinCashRatio      float64 `json:"min_cash_ratio"`
	SectorLimitPct    float64 `json:"sector_limit_pct"`
	RiskLevel         string  `json:"risk_level"`
	InvestmentHorizon string  `json:"investment_horizon"`
}

type DecisionValidation struct {
	Valid    bool     `json:"valid"`
	Errors   []string `json:"errors"`
	Warnings []string `json:"warnings"`
}

func (de *DecisionEngine) MakeDecision(
	alpha AlphaResult,
	riskReport PortfolioRiskReport,
	mandate InvestmentMandate,
) InvestmentDecision {
	decision := InvestmentDecision{
		Symbol:    alpha.Symbol,
		Timeframe: alpha.Timeframe,
	}

	riskFlags := assessRiskFlags(riskReport, mandate)
	decision.RiskFlags = riskFlags

	allowedByMandate := checkMandateAllowance(mandate, riskReport)

	if alpha.AlphaScore > 0.15 && allowedByMandate && len(riskFlags) == 0 {
		decision.Action = "BUY"
		decision.TargetWeight = computeTargetWeight(alpha, mandate, riskReport)
		decision.Confidence = math.Min(1.0, alpha.Confidence*0.9)
		decision.Reasoning = buildBuyReasoning(alpha, riskReport, mandate)
	} else if alpha.AlphaScore < -0.15 && riskReport.OverallScore > 0.4 {
		decision.Action = "SELL"
		decision.TargetWeight = 0
		decision.Confidence = math.Min(1.0, math.Abs(alpha.AlphaScore)*3)
		decision.Reasoning = buildSellReasoning(alpha, riskReport)
	} else if !allowedByMandate {
		decision.Action = "WAIT"
		decision.Confidence = 0.3
		decision.Reasoning = "投资指引限制: " + buildMandateRestrictionReason(riskReport, mandate)
	} else {
		decision.Action = "HOLD"
		decision.TargetWeight = 0
		decision.Confidence = 0.5
		decision.Reasoning = buildHoldReasoning(alpha)
	}

	if riskReport.OverallScore > 0.7 && decision.Action == "BUY" {
		decision.Action = "WAIT"
		decision.Reasoning += " | 风险过高，暂缓买入"
		decision.Confidence *= 0.6
	}

	return decision
}

func (de *DecisionEngine) GeneratePortfolioDecisions(
	alphas []AlphaResult,
	riskReport PortfolioRiskReport,
	mandate InvestmentMandate,
) []InvestmentDecision {
	var decisions []InvestmentDecision

	if len(alphas) == 0 {
		return decisions
	}

	acceptedAlphas := filterByThreshold(alphas, 0.1)

	for _, alpha := range acceptedAlphas {
		decision := de.MakeDecision(alpha, riskReport, mandate)
		decisions = append(decisions, decision)
	}

	decisions = applyPortfolioLevelAdjustments(decisions, riskReport, mandate)

	return decisions
}

func (de *DecisionEngine) ValidateDecision(
	decision InvestmentDecision,
	constraints OptimizationConstraints,
) DecisionValidation {
	validation := DecisionValidation{
		Valid:    true,
		Errors:   nil,
		Warnings: nil,
	}

	if decision.Action == "BUY" || decision.Action == "REBALANCE" {
		if decision.TargetWeight < 0 {
			validation.Valid = false
			validation.Errors = append(validation.Errors, "目标权重不能为负")
		}

		if constraints.MaxSingleWeight > 0 && decision.TargetWeight > constraints.MaxSingleWeight {
			validation.Valid = false
			validation.Errors = append(validation.Errors,
				"单标的权重 "+formatFloatStr(decision.TargetWeight)+
					" 超过限制 "+formatFloatStr(constraints.MaxSingleWeight))
		}

		if !constraints.AllowShort && decision.Action == "SELL" {
			validation.Valid = false
			validation.Errors = append(validation.Errors, "不允许做空操作")
		}
	}

	if decision.Confidence < 0.3 && (decision.Action == "BUY" || decision.Action == "SELL") {
		validation.Warnings = append(validation.Warnings,
			"置信度较低 ("+formatFloatStr(decision.Confidence)+")，建议谨慎操作")
	}

	if decision.Action == "BUY" && decision.TargetWeight > 0.2 {
		validation.Warnings = append(validation.Warnings,
			"目标权重 "+formatFloatStr(decision.TargetWeight)+" 偏高，注意集中度风险")
	}

	return validation
}

func assessRiskFlags(report PortfolioRiskReport, mandate InvestmentMandate) []string {
	var flags []string

	if mandate.MaxDrawdownPct > 0 && report.MaxDrawdown > mandate.MaxDrawdownPct {
		flags = append(flags, "MAX_DRAWDOWN_EXCEEDED")
	}

	if report.VaR99 > 0.10 {
		flags = append(flags, "VAR_HIGH")
	}

	if report.Volatility > 0.30 {
		flags = append(flags, "HIGH_VOLATILITY")
	}

	if report.Liquidity < 0.2 {
		flags = append(flags, "LOW_LIQUIDITY")
	}

	if report.Concentration > 0.25 {
		flags = append(flags, "HIGH_CONCENTRATION")
	}

	if report.Status == "CRITICAL" {
		flags = append(flags, "CRITICAL_RISK_STATUS")
	}

	return flags
}

func checkMandateAllowance(mandate InvestmentMandate, report PortfolioRiskReport) bool {
	if mandate.MaxDrawdownPct > 0 && report.MaxDrawdown > mandate.MaxDrawdownPct {
		return false
	}

	if mandate.RiskLevel == "CONSERVATIVE" && report.OverallScore > 0.5 {
		return false
	}

	if mandate.RiskLevel == "MODERATE" && report.OverallScore > 0.7 {
		return false
	}

	return true
}

func computeTargetWeight(alpha AlphaResult, mandate InvestmentMandate, report PortfolioRiskReport) float64 {
	baseWeight := 0.05

	alphaScoreComponent := math.Abs(alpha.AlphaScore) * 0.3
	signalComponent := alpha.SignalStrength * 0.2
	confidenceComponent := alpha.Confidence * 0.1

	targetWeight := baseWeight + alphaScoreComponent + signalComponent + confidenceComponent

	if mandate.MaxSinglePosition > 0 && targetWeight > mandate.MaxSinglePosition {
		targetWeight = mandate.MaxSinglePosition
	}

	if targetWeight > 0.30 {
		targetWeight = 0.30
	}

	if report.Liquidity < 0.3 {
		targetWeight *= 0.6
	}

	return roundTo4(targetWeight)
}

func buildBuyReasoning(alpha AlphaResult, risk PortfolioRiskReport, mandate InvestmentMandate) string {
	return "买入信号触发: AlphaScore=" + formatFloatStr(alpha.AlphaScore) +
		" SignalStrength=" + formatFloatStr(alpha.SignalStrength) +
		" 风险评分=" + formatFloatStr(risk.OverallScore) +
		" 授权级别=" + mandate.RiskLevel
}

func buildSellReasoning(alpha AlphaResult, risk PortfolioRiskReport) string {
	return "卖出信号: AlphaScore=" + formatFloatStr(alpha.AlphaScore) +
		" 风险评分=" + formatFloatStr(risk.OverallScore) +
		" 波动率=" + formatFloatStr(risk.Volatility)
}

func buildHoldReasoning(alpha AlphaResult) string {
	return "持有观望: AlphaScore=" + formatFloatStr(alpha.AlphaScore) +
		" 未达到买入/卖出阈值"
}

func buildMandateRestrictionReason(risk PortfolioRiskReport, mandate InvestmentMandate) string {
	reasons := ""
	if mandate.MaxDrawdownPct > 0 && risk.MaxDrawdown > mandate.MaxDrawdownPct {
		reasons += "回撤超限 "
	}
	if mandate.RiskLevel == "CONSERVATIVE" && risk.OverallScore > 0.5 {
		reasons += "风险等级不匹配 "
	}
	if reasons == "" {
		reasons = "当前市场环境不符合投资指引"
	}
	return reasons
}

func filterByThreshold(alphas []AlphaResult, threshold float64) []AlphaResult {
	var filtered []AlphaResult
	for _, a := range alphas {
		if math.Abs(a.AlphaScore) >= threshold {
			filtered = append(filtered, a)
		}
	}
	return filtered
}

func applyPortfolioLevelAdjustments(
	decisions []InvestmentDecision,
	risk PortfolioRiskReport,
	mandate InvestmentMandate,
) []InvestmentDecision {
	if len(decisions) == 0 {
		return decisions
	}

	targetSum := 0.0
	for _, d := range decisions {
		if d.Action == "BUY" {
			targetSum += d.TargetWeight
		}
	}

	if mandate.MinCashRatio > 0 {
		maxInvestable := 1.0 - mandate.MinCashRatio
		if targetSum > maxInvestable {
			scaleFactor := maxInvestable / targetSum
			for i := range decisions {
				if decisions[i].Action == "BUY" {
					decisions[i].TargetWeight *= scaleFactor
				}
			}
		}
	}

	if risk.Concentration > 0.25 {
		for i := range decisions {
			if decisions[i].Action == "BUY" {
				decisions[i].TargetWeight *= 0.85
			}
		}
	}

	return decisions
}