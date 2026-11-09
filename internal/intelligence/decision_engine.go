package intelligence

import (
	"math"
	"time"

	"github.com/quantpilot/quantpilot/internal/reasoning"
)

type DecisionEngine struct{}

func NewDecisionEngine() *DecisionEngine {
	return &DecisionEngine{}
}

// VerificationStatus 诚实汇报的状态标签：
//  - draft      未经验证的草稿（AI 自动生成、未过风险/审批）
//  - verified   已验证（通过审批、风控复核确认）
//  - superseded 被更新决策取代
//  - rejected   被明确拒绝（人工驳回或风控二次审查否决）
type VerificationStatus string

const (
	VStatusDraft      VerificationStatus = "draft"
	VStatusVerified   VerificationStatus = "verified"
	VStatusSuperseded VerificationStatus = "superseded"
	VStatusRejected   VerificationStatus = "rejected"
)

// InvestmentDecision 决策结果。
//
// 改造说明（吸收 dsh-superagent 的两个原则）：
//  1. ReasonCodes 结构化原因码集合——取代单纯 Reasoning 自由文本作为审计主输入；
//     Reasoning 保留做人类可读兜底，但 UI/审计/风控程序判断一律读 ReasonCodes。
//  2. Status 诚实汇报标签——禁止把未验证决策伪装成已确认的；
//     draft 决策不能触发实盘执行路径，必须先变成 verified。
type InvestmentDecision struct {
	Action       string                    `json:"action"`
	Symbol       string                    `json:"symbol"`
	TargetWeight float64                   `json:"target_weight"`
	Confidence   float64                   `json:"confidence"`
	Reasoning    string                    `json:"reasoning"`
	RiskFlags    []string                  `json:"risk_flags"`
	Timeframe    string                    `json:"timeframe"`

	// ReasonCodes 结构化原因码（权威来源）
	ReasonCodes reasoning.ReasonSet `json:"reason_codes,omitempty"`
	// Status 诚实汇报标签（默认 draft；审批/风控确认后置 verified）
	Status VerificationStatus `json:"status"`
	// VerifiedAt 何时被标记为 verified（nil 表示未验证）
	VerifiedAt *time.Time `json:"verified_at,omitempty"`
	// DecisionID 唯一决策 ID（跨轮次关联审批/回测/成交回报）
	DecisionID string `json:"decision_id,omitempty"`
}

// IsVerified 是否可以进入实盘执行路径。
func (d InvestmentDecision) IsVerified() bool {
	return d.Status == VStatusVerified
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
		Status:    VStatusDraft, // 诚实汇报：所有自动决策起步都是 draft
	}

	riskFlags := assessRiskFlags(riskReport, mandate)
	decision.RiskFlags = riskFlags

	// 把 RiskFlags 同时翻译成结构化 ReasonCodes（RiskFlags 继续保留做字符串标签）
	for _, f := range riskFlags {
		if rc, ok := riskFlagToReasonCode(f); ok {
			decision.ReasonCodes = append(decision.ReasonCodes, reasoning.NewReason(rc))
		}
	}

	allowedByMandate := checkMandateAllowance(mandate, riskReport)

	alphaThresh := 0.15

	if alpha.AlphaScore > alphaThresh && allowedByMandate && len(riskFlags) == 0 {
		decision.Action = "BUY"
		decision.TargetWeight = computeTargetWeight(alpha, mandate, riskReport)
		decision.Confidence = math.Min(1.0, alpha.Confidence*0.9)
		decision.Reasoning = buildBuyReasoning(alpha, riskReport, mandate)
		decision.ReasonCodes = append(decision.ReasonCodes,
			reasoning.NewReasonValues(reasoning.CodeAlphaBuy,
				map[string]interface{}{"symbol": alpha.Symbol, "alpha_score": alpha.AlphaScore, "threshold": alphaThresh}),
		)
	} else if alpha.AlphaScore < -alphaThresh && riskReport.OverallScore > 0.4 {
		decision.Action = "SELL"
		decision.TargetWeight = 0
		decision.Confidence = math.Min(1.0, math.Abs(alpha.AlphaScore)*3)
		decision.Reasoning = buildSellReasoning(alpha, riskReport)
		decision.ReasonCodes = append(decision.ReasonCodes,
			reasoning.NewReasonValues(reasoning.CodeAlphaSell,
				map[string]interface{}{"symbol": alpha.Symbol, "alpha_score": alpha.AlphaScore, "threshold": -alphaThresh}),
		)
	} else if !allowedByMandate {
		decision.Action = "WAIT"
		decision.Confidence = 0.3
		decision.Reasoning = "投资指引限制: " + buildMandateRestrictionReason(riskReport, mandate)
		// 投资指引限制的具体原因码
		if mandate.MaxDrawdownPct > 0 && riskReport.MaxDrawdown > mandate.MaxDrawdownPct {
			decision.ReasonCodes = append(decision.ReasonCodes,
				reasoning.NewReasonValues(reasoning.CodeMandateDrawdown,
					map[string]interface{}{"limit": mandate.MaxDrawdownPct, "actual": riskReport.MaxDrawdown}))
		}
		if mandate.RiskLevel == "CONSERVATIVE" && riskReport.OverallScore > 0.5 {
			decision.ReasonCodes = append(decision.ReasonCodes,
				reasoning.NewReasonValues(reasoning.CodeMandateRiskLevel,
					map[string]interface{}{"level": mandate.RiskLevel, "score": riskReport.OverallScore}))
		}
	} else {
		decision.Action = "HOLD"
		decision.TargetWeight = 0
		decision.Confidence = 0.5
		decision.Reasoning = buildHoldReasoning(alpha)
		decision.ReasonCodes = append(decision.ReasonCodes,
			reasoning.NewReasonValues(reasoning.CodeAlphaWeak,
				map[string]interface{}{"symbol": alpha.Symbol, "alpha_score": alpha.AlphaScore, "threshold": alphaThresh}))
	}

	// 最后一道 Gate：组合整体风险太高 → BUY 强制降级 WAIT
	if riskReport.OverallScore > 0.7 && decision.Action == "BUY" {
		decision.Action = "WAIT"
		decision.Reasoning += " | 风险过高，暂缓买入"
		decision.Confidence *= 0.6
		decision.ReasonCodes = append(decision.ReasonCodes,
			reasoning.NewReasonValues(reasoning.CodeOverallRiskTooHigh,
				map[string]interface{}{"risk_score": riskReport.OverallScore, "threshold": 0.7}))
	}

	return decision
}

// riskFlagToReasonCode RiskFlags 字符串 → ReasonCode 的翻译表。
// assessRiskFlags 产出的大写 RiskFlag → reasoning 包的稳定码。
func riskFlagToReasonCode(flag string) (reasoning.ReasonCode, bool) {
	switch flag {
	case "MAX_DRAWDOWN_EXCEEDED":
		return reasoning.CodeMaxDrawdownExceeded, true
	case "VAR_HIGH":
		return reasoning.CodeVaRHigh, true
	case "HIGH_VOLATILITY":
		return reasoning.CodeHighVolatility, true
	case "LOW_LIQUIDITY":
		return reasoning.CodeLowLiquidity, true
	case "HIGH_CONCENTRATION":
		return reasoning.CodeHighConcentration, true
	case "CRITICAL_RISK_STATUS":
		return reasoning.CodeRiskStatusCritical, true
	default:
		return reasoning.CodeUnknown, false
	}
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