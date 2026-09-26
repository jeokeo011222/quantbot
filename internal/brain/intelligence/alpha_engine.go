package intelligence

import (
	"math"
	"sort"
)

type AlphaEngine struct{}

func NewAlphaEngine() *AlphaEngine {
	return &AlphaEngine{}
}

type AlphaResult struct {
	Symbol         string             `json:"symbol"`
	AlphaScore     float64            `json:"alpha_score"`
	SignalStrength float64            `json:"signal_strength"`
	Direction      string             `json:"direction"`
	Confidence     float64            `json:"confidence"`
	Timeframe      string             `json:"timeframe"`
	Factors        map[string]float64 `json:"factors"`
	Reason         string             `json:"reason"`
}

type factorWeightConfig struct {
	momentum   float64
	value      float64
	quality    float64
	volatility float64
	liquidity  float64
}

var defaultWeights = factorWeightConfig{
	momentum:   0.25,
	value:      0.25,
	quality:    0.20,
	volatility: 0.15,
	liquidity:  0.15,
}

func (ae *AlphaEngine) DiscoverAlpha(factorScores map[string][]float64, marketRegime string) []AlphaResult {
	var results []AlphaResult

	if len(factorScores) == 0 {
		return results
	}

	regimeFactor := computeRegimeFactor(marketRegime)

	for factorName, scores := range factorScores {
		if len(scores) < 2 {
			continue
		}

		currentScore := recentMean(scores, 5)
		trend := computeFactorTrend(scores)
		compatibility := computeFactorRegimeCompatibility(factorName, marketRegime)

		factorCategory := inferFactorCategory(factorName)
		weight := getFactorWeight(factorCategory)

		alphaComponent := currentScore * weight * compatibility * regimeFactor
		signalStrength := math.Abs(trend) * compatibility
		direction := determineAlphaDirection(currentScore, trend)

		confidence := computeAlphaConfidence(scores, compatibility)

		result := AlphaResult{
			Symbol:         factorName,
			AlphaScore:     roundTo4(alphaComponent),
			SignalStrength: roundTo4(signalStrength),
			Direction:      direction,
			Confidence:     roundTo4(confidence),
			Timeframe:      "medium_term",
			Factors: map[string]float64{
				"current_score":   roundTo4(currentScore),
				"trend":           roundTo4(trend),
				"compatibility":   roundTo4(compatibility),
				"weight":          roundTo4(weight),
				"regime_factor":   roundTo4(regimeFactor),
				"factor_category": roundTo4(float64(len(factorCategory))),
			},
			Reason: buildAlphaReason(factorName, direction, confidence, compatibility),
		}

		results = append(results, result)
	}

	return results
}

func (ae *AlphaEngine) GenerateSignal(alpha AlphaResult) AlphaResult {
	if alpha.AlphaScore > 0.15 && alpha.SignalStrength > 0.3 {
		alpha.Direction = "LONG"
		alpha.Confidence = math.Min(1.0, alpha.Confidence+0.1)
		alpha.Reason = "信号触发: AlphaScore=" + formatFloatStr(alpha.AlphaScore) +
			" SignalStrength=" + formatFloatStr(alpha.SignalStrength)
	} else if alpha.AlphaScore < -0.15 && alpha.SignalStrength > 0.3 {
		alpha.Direction = "SHORT"
		alpha.Confidence = math.Min(1.0, alpha.Confidence+0.05)
		alpha.Reason = "反向信号: AlphaScore=" + formatFloatStr(alpha.AlphaScore) +
			" SignalStrength=" + formatFloatStr(alpha.SignalStrength)
	} else {
		alpha.Direction = "HOLD"
		alpha.Reason = "信号不足: AlphaScore=" + formatFloatStr(alpha.AlphaScore) +
			" 未达到阈值 0.15"
	}

	return alpha
}

func (ae *AlphaEngine) RankAlphas(alphas []AlphaResult, topN int) []AlphaResult {
	if len(alphas) == 0 || topN <= 0 {
		return nil
	}

	sorted := make([]AlphaResult, len(alphas))
	copy(sorted, alphas)

	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].AlphaScore > sorted[j].AlphaScore
	})

	if topN > len(sorted) {
		topN = len(sorted)
	}

	return sorted[:topN]
}

func computeRegimeFactor(regime string) float64 {
	factors := map[string]float64{
		"BULLISH":    1.2,
		"BEARISH":    0.6,
		"NEUTRAL":    1.0,
		"HIGH_VOL":   0.8,
		"LOW_VOL":    1.1,
		"TRANSITION": 0.7,
	}
	if f, ok := factors[regime]; ok {
		return f
	}
	return 1.0
}

func computeFactorTrend(scores []float64) float64 {
	if len(scores) < 5 {
		return 0
	}
	recent := scores[len(scores)-5:]
	earlier := scores[len(scores)-10:]
	if len(earlier) < 5 {
		earlier = scores[:5]
	}
	recentAvg := mean(recent)
	earlierAvg := mean(earlier)
	return recentAvg - earlierAvg
}

func determineAlphaDirection(score, trend float64) string {
	if score > 0.1 && trend > 0 {
		return "LONG"
	}
	if score < -0.1 && trend < 0 {
		return "SHORT"
	}
	return "HOLD"
}

func computeAlphaConfidence(scores []float64, compatibility float64) float64 {
	if len(scores) < 5 {
		return 0.3
	}
	recent := scores[len(scores)-5:]
	m := mean(recent)
	variance := 0.0
	for _, v := range recent {
		diff := v - m
		variance += diff * diff
	}
	std := math.Sqrt(variance / float64(len(recent)))
	if std < 1e-8 {
		return 0.9 * compatibility
	}
	return math.Min(1.0, (1.0-std/math.Abs(m+1e-8))*compatibility)
}

func recentMean(values []float64, window int) float64 {
	if len(values) < window {
		return mean(values)
	}
	return mean(values[len(values)-window:])
}

func inferFactorCategory(name string) string {
	categories := []struct {
		keywords []string
		category string
	}{
		{[]string{"momentum", "reversal", "trend"}, "momentum"},
		{[]string{"value", "ep_ratio", "bp_ratio", "dividend", "yield"}, "value"},
		{[]string{"quality", "roe", "gross_margin", "profit"}, "quality"},
		{[]string{"volatility", "beta", "low_vol", "vol"}, "volatility"},
		{[]string{"liquidity", "turnover", "amihud", "volume"}, "liquidity"},
	}
	for _, cat := range categories {
		for _, kw := range cat.keywords {
			if len(name) >= len(kw) && searchStr(name, kw) {
				return cat.category
			}
		}
	}
	return "value"
}

func getFactorWeight(category string) float64 {
	switch category {
	case "momentum":
		return defaultWeights.momentum
	case "value":
		return defaultWeights.value
	case "quality":
		return defaultWeights.quality
	case "volatility":
		return defaultWeights.volatility
	case "liquidity":
		return defaultWeights.liquidity
	default:
		return 0.2
	}
}

func computeFactorRegimeCompatibility(factorName, regime string) float64 {
	category := inferFactorCategory(factorName)
	matrix := map[string]map[string]float64{
		"momentum": {
			"BULLISH": 0.9, "BEARISH": 0.2, "NEUTRAL": 0.5,
			"HIGH_VOL": 0.4, "LOW_VOL": 0.6, "TRANSITION": 0.3,
		},
		"value": {
			"BULLISH": 0.6, "BEARISH": 0.8, "NEUTRAL": 0.7,
			"HIGH_VOL": 0.5, "LOW_VOL": 0.75, "TRANSITION": 0.65,
		},
		"quality": {
			"BULLISH": 0.7, "BEARISH": 0.9, "NEUTRAL": 0.75,
			"HIGH_VOL": 0.8, "LOW_VOL": 0.7, "TRANSITION": 0.75,
		},
		"volatility": {
			"BULLISH": 0.3, "BEARISH": 0.7, "NEUTRAL": 0.5,
			"HIGH_VOL": 0.9, "LOW_VOL": 0.2, "TRANSITION": 0.6,
		},
		"liquidity": {
			"BULLISH": 0.8, "BEARISH": 0.4, "NEUTRAL": 0.6,
			"HIGH_VOL": 0.3, "LOW_VOL": 0.85, "TRANSITION": 0.4,
		},
	}
	if m, ok := matrix[category]; ok {
		if score, ok := m[regime]; ok {
			return score
		}
	}
	return 0.5
}

func buildAlphaReason(factor, direction string, confidence, compatibility float64) string {
	dirCN := "中性"
	switch direction {
	case "LONG":
		dirCN = "做多"
	case "SHORT":
		dirCN = "做空"
	case "HOLD":
		dirCN = "持有"
	}
	return "因子[" + factor + "] 建议" + dirCN +
		" 置信度=" + formatFloatStr(confidence) +
		" 状态兼容=" + formatFloatStr(compatibility)
}

func formatFloatStr(v float64) string {
	return floatToStr(v)
}

func floatToStr(v float64) string {
	if v >= 100 || v <= -100 {
		return floatToStrLarge(v)
	}
	return floatToStrSmall(v)
}

func floatToStrSmall(v float64) string {
	intPart := int64(v)
	decPart := int64(math.Abs(v-float64(intPart))*100 + 0.5)
	if decPart >= 100 {
		intPart++
		decPart -= 100
	}
	if decPart < 0 {
		decPart = 0
	}
	return intToStr(intPart) + "." + intToStr2(decPart)
}

func floatToStrLarge(v float64) string {
	intPart := int64(v)
	return intToStr(intPart)
}

func intToStr(v int64) string {
	if v == 0 {
		return "0"
	}
	negative := false
	if v < 0 {
		negative = true
		v = -v
	}
	var buf [20]byte
	pos := len(buf)
	for v > 0 {
		pos--
		buf[pos] = byte('0' + v%10)
		v /= 10
	}
	if negative {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

func intToStr2(v int64) string {
	if v == 0 {
		return "00"
	}
	var buf [2]byte
	pos := len(buf)
	for i := 0; i < 2; i++ {
		pos--
		buf[pos] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[:])
}

func roundTo4(v float64) float64 {
	return float64(int64(v*10000+0.5*sign(v))) / 10000
}

func sign(v float64) float64 {
	if v >= 0 {
		return 1
	}
	return -1
}
