package intelligence

import (
	"math"
)

type RegimeEngine struct{}

func NewRegimeEngine() *RegimeEngine {
	return &RegimeEngine{}
}

type MarketRegime struct {
	Name        string  `json:"name"`
	Trend       float64 `json:"trend"`
	Volatility  float64 `json:"volatility"`
	Breadth     float64 `json:"breadth"`
	Confidence  float64 `json:"confidence"`
	Description string  `json:"description"`
}

type RegimeChangeSignal struct {
	FromRegime  string  `json:"from_regime"`
	ToRegime    string  `json:"to_regime"`
	Transition  string  `json:"transition"`
	Confidence  float64 `json:"confidence"`
	ImpactLevel string  `json:"impact_level"`
	Description string  `json:"description"`
}

func (re *RegimeEngine) IdentifyRegime(priceSeries []float64) MarketRegime {
	regime := MarketRegime{}

	if len(priceSeries) < 5 {
		regime.Name = "NEUTRAL"
		regime.Confidence = 0.3
		regime.Description = "数据不足，默认中性"
		return regime
	}

	returns := computeReturns(priceSeries)
	if len(returns) < 3 {
		regime.Name = "NEUTRAL"
		regime.Confidence = 0.35
		regime.Description = "收益率数据不足"
		return regime
	}

	regime.Trend = computeTrend(priceSeries)
	regime.Volatility = computeVolatility(returns)
	regime.Breadth = computeBreadth(returns)

	regime.Name = classifyRegime(regime.Trend, regime.Volatility, regime.Breadth)
	regime.Confidence = computeRegimeConfidence(regime.Trend, regime.Volatility, regime.Breadth)
	regime.Description = buildRegimeDescription(regime)

	return regime
}

func (re *RegimeEngine) DetectRegimeChange(current, previous MarketRegime) RegimeChangeSignal {
	signal := RegimeChangeSignal{
		FromRegime: previous.Name,
		ToRegime:   current.Name,
	}

	if current.Name == previous.Name {
		signal.Transition = "NO_CHANGE"
		signal.Confidence = 0.8
		signal.ImpactLevel = "LOW"
		signal.Description = "市场状态稳定，无明显变化"
		return signal
	}

	signal.Transition = previous.Name + "_TO_" + current.Name

	confidenceDelta := math.Abs(current.Confidence - previous.Confidence)
	trendDelta := math.Abs(current.Trend - previous.Trend)
	volDelta := math.Abs(current.Volatility - previous.Volatility)

	signal.Confidence = math.Min(1.0, 0.5+confidenceDelta*0.3+trendDelta*2+volDelta)

	signal.ImpactLevel = determineImpactLevel(previous.Name, current.Name, signal.Confidence)

	signal.Description = buildChangeDescription(previous, current, signal)

	return signal
}

func (re *RegimeEngine) GetRegimeCompatibility(regime string, factorCategory string) float64 {
	matrix := map[string]map[string]float64{
		"momentum": {
			"BULLISH": 0.90, "BEARISH": 0.20, "NEUTRAL": 0.50,
			"HIGH_VOL": 0.40, "LOW_VOL": 0.60, "TRANSITION": 0.30,
		},
		"value": {
			"BULLISH": 0.60, "BEARISH": 0.80, "NEUTRAL": 0.70,
			"HIGH_VOL": 0.50, "LOW_VOL": 0.75, "TRANSITION": 0.65,
		},
		"quality": {
			"BULLISH": 0.70, "BEARISH": 0.90, "NEUTRAL": 0.75,
			"HIGH_VOL": 0.80, "LOW_VOL": 0.70, "TRANSITION": 0.75,
		},
		"volatility": {
			"BULLISH": 0.30, "BEARISH": 0.70, "NEUTRAL": 0.50,
			"HIGH_VOL": 0.90, "LOW_VOL": 0.20, "TRANSITION": 0.60,
		},
		"liquidity": {
			"BULLISH": 0.80, "BEARISH": 0.40, "NEUTRAL": 0.60,
			"HIGH_VOL": 0.30, "LOW_VOL": 0.85, "TRANSITION": 0.40,
		},
		"reversal": {
			"BULLISH": 0.40, "BEARISH": 0.60, "NEUTRAL": 0.70,
			"HIGH_VOL": 0.80, "LOW_VOL": 0.50, "TRANSITION": 0.75,
		},
		"dividend": {
			"BULLISH": 0.50, "BEARISH": 0.85, "NEUTRAL": 0.70,
			"HIGH_VOL": 0.75, "LOW_VOL": 0.80, "TRANSITION": 0.70,
		},
		"size": {
			"BULLISH": 0.80, "BEARISH": 0.30, "NEUTRAL": 0.55,
			"HIGH_VOL": 0.40, "LOW_VOL": 0.60, "TRANSITION": 0.35,
		},
	}

	if m, ok := matrix[factorCategory]; ok {
		if score, ok := m[regime]; ok {
			return score
		}
	}
	return 0.5
}

func computeTrend(prices []float64) float64 {
	if len(prices) < 5 {
		return 0
	}

	n := len(prices)
	recentStart := n - int(math.Min(float64(10), float64(n)/3))
	if recentStart < 1 {
		recentStart = 1
	}

	recentPrices := prices[recentStart:]
	earlierPrices := prices[:recentStart]

	if len(recentPrices) < 2 || len(earlierPrices) < 1 {
		slope := linearSlope(prices)
		if math.Abs(slope) < 1e-8 {
			return 0
		}
		return math.Atan(slope) / (math.Pi / 4)
	}

	recentAvg := mean(recentPrices)
	earlierAvg := mean(earlierPrices)

	if math.Abs(earlierAvg) < 1e-8 {
		return 0
	}

	return (recentAvg - earlierAvg) / math.Abs(earlierAvg)
}

func computeVolatility(returns []float64) float64 {
	if len(returns) < 2 {
		return 0
	}
	m := mean(returns)
	variance := 0.0
	for _, r := range returns {
		diff := r - m
		variance += diff * diff
	}
	return math.Sqrt(variance / float64(len(returns)-1))
}

func computeBreadth(returns []float64) float64 {
	if len(returns) == 0 {
		return 0.5
	}
	upDays := 0
	for _, r := range returns {
		if r > 0 {
			upDays++
		}
	}
	return float64(upDays) / float64(len(returns))
}

func linearSlope(values []float64) float64 {
	n := len(values)
	if n < 2 {
		return 0
	}
	xMean := float64(n-1) / 2.0
	yMean := mean(values)

	numerator := 0.0
	denominator := 0.0
	for i := 0; i < n; i++ {
		numerator += (float64(i) - xMean) * (values[i] - yMean)
		denominator += (float64(i) - xMean) * (float64(i) - xMean)
	}

	if denominator < 1e-8 {
		return 0
	}
	return numerator / denominator
}

func classifyRegime(trend, volatility, breadth float64) string {
	volThreshold := 0.015
	highVolThreshold := 0.025
	trendThreshold := 0.01

	if volatility > highVolThreshold {
		if trend > trendThreshold {
			return "HIGH_VOL"
		}
		return "HIGH_VOL"
	}

	if volatility < volThreshold*0.5 {
		if math.Abs(trend) < trendThreshold {
			return "LOW_VOL"
		}
	}

	if math.Abs(trend) < trendThreshold*0.5 && volatility < volThreshold {
		return "TRANSITION"
	}

	if trend > trendThreshold && breadth > 0.55 {
		return "BULLISH"
	}

	if trend < -trendThreshold && breadth < 0.45 {
		return "BEARISH"
	}

	if trend > trendThreshold*0.5 {
		return "BULLISH"
	}

	if trend < -trendThreshold*0.5 {
		return "BEARISH"
	}

	return "NEUTRAL"
}

func computeRegimeConfidence(trend, volatility, breadth float64) float64 {
	confidence := 0.5

	trendStrength := math.Min(1.0, math.Abs(trend)*20)
	confidence += trendStrength * 0.25

	breadthExtreme := math.Abs(breadth - 0.5) * 2
	confidence += breadthExtreme * 0.15

	if volatility > 0.03 {
		confidence -= 0.1
	} else if volatility < 0.005 {
		confidence += 0.05
	}

	return math.Max(0.1, math.Min(1.0, confidence))
}

func buildRegimeDescription(r MarketRegime) string {
	descriptions := map[string]string{
		"BULLISH":    "市场处于上涨趋势，多头占优",
		"BEARISH":    "市场处于下跌趋势，空头占优",
		"NEUTRAL":    "市场方向不明，震荡格局",
		"HIGH_VOL":   "市场波动率高，风险加剧",
		"LOW_VOL":    "市场波动率低，可能酝酿变盘",
		"TRANSITION": "市场处于方向转换期",
	}

	desc, ok := descriptions[r.Name]
	if !ok {
		desc = "未知市场状态"
	}

	return desc + " | 趋势=" + formatFloatStr(r.Trend) +
		" 波动=" + formatFloatStr(r.Volatility) +
		" 宽度=" + formatFloatStr(r.Breadth)
}

func determineImpactLevel(from, to string, confidence float64) string {
	highImpact := map[string]bool{
		"BULLISH_TO_BEARISH": true,
		"BEARISH_TO_BULLISH": true,
		"NEUTRAL_TO_BULLISH": true,
		"NEUTRAL_TO_BEARISH": true,
		"HIGH_VOL_TO_BULLISH": true,
		"HIGH_VOL_TO_BEARISH": true,
	}

	transition := from + "_TO_" + to
	if highImpact[transition] && confidence > 0.6 {
		return "HIGH"
	}

	if confidence > 0.7 {
		return "MEDIUM"
	}

	return "LOW"
}

func buildChangeDescription(prev, curr MarketRegime, signal RegimeChangeSignal) string {
	return "市场状态转换: " + prev.Name + " → " + curr.Name +
		" 置信度=" + formatFloatStr(signal.Confidence) +
		" 影响等级=" + signal.ImpactLevel
}