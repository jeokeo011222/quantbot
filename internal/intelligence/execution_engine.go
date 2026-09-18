package intelligence

import (
	"math"
)

type ExecutionEngine struct{}

func NewExecutionEngine() *ExecutionEngine {
	return &ExecutionEngine{}
}

type ExecutionPlan struct {
	Symbol             string  `json:"symbol"`
	Side               string  `json:"side"`
	TargetWeight       float64 `json:"target_weight"`
	Strategy           string  `json:"strategy"`
	Duration           string  `json:"duration"`
	LimitPrice         float64 `json:"limit_price"`
	EstSlippage        float64 `json:"est_slippage"`
	EstMarketImpact    float64 `json:"est_market_impact"`
	EstTransactionCost float64 `json:"est_transaction_cost"`
	Urgency            string  `json:"urgency"`
	LiquidityScore     float64 `json:"liquidity_score"`
}

type MarketMicrostructure struct {
	Spread     float64 `json:"spread"`
	Depth      float64 `json:"depth"`
	Liquidity  float64 `json:"liquidity"`
	ImpactCost float64 `json:"impact_cost"`
	OrderFlow  float64 `json:"order_flow"`
}

func (ee *ExecutionEngine) SelectExecutionStrategy(liquidityScore float64, urgency string) string {
	switch urgency {
	case "CRITICAL":
		if liquidityScore >= 0.5 {
			return "MARKET"
		}
		return "LIMIT"
	case "URGENT":
		if liquidityScore >= 0.6 {
			return "MARKET"
		}
		if liquidityScore >= 0.3 {
			return "TWAP"
		}
		return "LIMIT"
	}

	if liquidityScore >= 0.7 {
		return "MARKET"
	}
	if liquidityScore >= 0.4 {
		return "VWAP"
	}
	if liquidityScore >= 0.2 {
		return "TWAP"
	}
	return "LIMIT"
}

func (ee *ExecutionEngine) GenerateExecutionPlan(
	order InvestmentDecision,
	marketData MarketMicrostructure,
) ExecutionPlan {
	plan := ExecutionPlan{
		Symbol:         order.Symbol,
		Side:           mapActionToSide(order.Action),
		TargetWeight:   order.TargetWeight,
		LiquidityScore: marketData.Liquidity,
		Urgency:        "NORMAL",
	}

	if order.Confidence > 0.8 && order.Action != "HOLD" {
		plan.Urgency = "URGENT"
	}
	if order.Confidence > 0.95 {
		plan.Urgency = "CRITICAL"
	}

	plan.Strategy = ee.SelectExecutionStrategy(marketData.Liquidity, plan.Urgency)

	plan.Duration = estimateDuration(plan.Strategy, plan.Urgency)

	plan.LimitPrice = computeLimitPrice(plan.Side, marketData.Spread, plan.Strategy)

	plan.EstSlippage = ee.CalculateSlippage(marketData.Liquidity, order.TargetWeight)

	plan.EstMarketImpact = estimateMarketImpact(marketData, order.TargetWeight)

	plan.EstTransactionCost = ee.EstimateTransactionCost(plan)

	return plan
}

func (ee *ExecutionEngine) EstimateTransactionCost(plan ExecutionPlan) float64 {
	commission := 0.0005

	spreadCost := 0.0
	if plan.Strategy == "MARKET" {
		spreadCost = 0.0003
	} else if plan.Strategy == "VWAP" {
		spreadCost = 0.0001
	} else if plan.Strategy == "TWAP" {
		spreadCost = 0.00015
	} else {
		spreadCost = 0.00005
	}

	slippageCost := plan.EstSlippage
	impactCost := plan.EstMarketImpact

	opportunityCost := 0.0
	switch plan.Urgency {
	case "CRITICAL":
		opportunityCost = 0.0002
	case "URGENT":
		opportunityCost = 0.0001
	default:
		opportunityCost = 0.00005
	}

	total := commission + spreadCost + slippageCost + impactCost + opportunityCost
	return roundTo4(total)
}

func (ee *ExecutionEngine) CalculateSlippage(liquidityScore float64, orderSize float64) float64 {
	if liquidityScore <= 0 {
		liquidityScore = 0.01
	}

	baseSlippage := 0.0001
	liquidityPenalty := 1.0 / liquidityScore
	sizeImpact := orderSize * 0.01

	slippage := baseSlippage * liquidityPenalty * (1.0 + sizeImpact)

	return roundTo4(math.Min(slippage, 0.05))
}

func mapActionToSide(action string) string {
	switch action {
	case "BUY":
		return "BUY"
	case "SELL":
		return "SELL"
	case "REBALANCE":
		return "BUY"
	case "WAIT":
		return "BUY"
	default:
		return "BUY"
	}
}

func estimateDuration(strategy, urgency string) string {
	if urgency == "CRITICAL" {
		return "IMMEDIATE"
	}

	switch strategy {
	case "MARKET":
		return "1_session"
	case "VWAP":
		return "1_day"
	case "TWAP":
		return "2_days"
	case "LIMIT":
		return "3_days"
	default:
		return "1_day"
	}
}

func computeLimitPrice(side string, spread float64, strategy string) float64 {
	switch strategy {
	case "LIMIT":
		if side == "BUY" {
			return 1.0 + spread*0.3
		}
		return 1.0 - spread*0.3
	case "VWAP":
		if side == "BUY" {
			return 1.0 + spread*0.5
		}
		return 1.0 - spread*0.5
	case "TWAP":
		if side == "BUY" {
			return 1.0 + spread*0.2
		}
		return 1.0 - spread*0.2
	default:
		return 1.0
	}
}

func estimateMarketImpact(data MarketMicrostructure, orderSize float64) float64 {
	baseImpact := data.ImpactCost
	if baseImpact <= 0 {
		baseImpact = 0.001
	}

	liquidityFactor := 1.0
	if data.Liquidity > 0 {
		liquidityFactor = 1.0 / math.Sqrt(data.Liquidity)
	}

	sizeFactor := math.Sqrt(math.Max(orderSize, 0.01))

	impact := baseImpact * liquidityFactor * sizeFactor

	return roundTo4(math.Min(impact, 0.03))
}
