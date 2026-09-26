package intelligence

import (
	"math"
)

// FactorHealthEngine 因子健康度引擎
// 提供因子健康度评估算法：Kalman滤波、生存分析、Cox模型等
type FactorHealthEngine struct{}

// NewFactorHealthEngine 创建因子健康度引擎
func NewFactorHealthEngine() *FactorHealthEngine {
	return &FactorHealthEngine{}
}

// FactorHealthResult 因子健康度评估结果
type FactorHealthResult struct {
	FactorName          string  `json:"factor_name"`
	SignalStrength      float64 `json:"signal_strength"`
	Stability           float64 `json:"stability"`
	IC                  float64 `json:"ic"`
	ICIR                float64 `json:"icir"`
	Decay               float64 `json:"decay"`
	Crowding            float64 `json:"crowding"`
	RegimeCompatibility float64 `json:"regime_compatibility"`
	HealthScore         float64 `json:"health_score"`
	Status              string  `json:"status"` // HEALTHY / DETERIORATING / COLLAPSING
	KalmanEstimate      float64 `json:"kalman_estimate"`
	SurvivalScore       float64 `json:"survival_score"`
	CoxHazardRatio      float64 `json:"cox_hazard_ratio"`
}

// AnalyzeFactorHealth 分析因子健康度
func (fhe *FactorHealthEngine) AnalyzeFactorHealth(
	factorName string,
	scoreHistory []float64,
	marketRegime string,
) FactorHealthResult {
	result := FactorHealthResult{
		FactorName: factorName,
	}

	if len(scoreHistory) < 10 {
		result.HealthScore = 50
		result.Status = "INSUFFICIENT_DATA"
		return result
	}

	// 1. Signal Strength (信号强度)
	result.SignalStrength = computeSignalStrength(scoreHistory)

	// 2. Stability (稳定性)
	result.Stability = computeFactorStability(scoreHistory)

	// 3. IC (Information Coefficient)
	result.IC = computeFactorIC(scoreHistory)

	// 4. ICIR (IC / IC标准差)
	result.ICIR = computeFactorICIR(scoreHistory)

	// 5. Decay (因子衰减)
	result.Decay = computeFactorDecayRate(scoreHistory)

	// 6. Crowding (拥挤度)
	result.Crowding = computeFactorCrowding(scoreHistory)

	// 7. Regime Compatibility (状态兼容性)
	result.RegimeCompatibility = computeRegimeCompatibility(factorName, marketRegime)

	// 8. Kalman Health Estimate
	result.KalmanEstimate = kalmanFilterHealth(scoreHistory)

	// 9. Survival Score
	result.SurvivalScore = survivalAnalysis(scoreHistory)

	// 10. Cox Hazard Ratio
	result.CoxHazardRatio = coxHazardModel(scoreHistory, result.Decay)

	// 综合健康度评分
	result.HealthScore = computeOverallHealth(result)

	// 状态判定
	if result.HealthScore >= 65 {
		result.Status = "HEALTHY"
	} else if result.HealthScore >= 45 {
		result.Status = "DETERIORATING"
	} else {
		result.Status = "COLLAPSING"
	}

	return result
}

// GenerateFullFactorHealthReport 生成完整因子健康报告
func (fhe *FactorHealthEngine) GenerateFullFactorHealthReport(
	factorScores map[string][]float64,
	marketRegime string,
) map[string]interface{} {
	results := make([]FactorHealthResult, 0)

	for factorName, history := range factorScores {
		result := fhe.AnalyzeFactorHealth(factorName, history, marketRegime)
		results = append(results, result)
	}

	// 计算整体健康度
	var totalHealth float64
	warnings := make([]string, 0)
	for _, r := range results {
		totalHealth += r.HealthScore
		if r.HealthScore < 60 {
			warnings = append(warnings,
				r.FactorName+": 健康度下降至 "+formatFloat(r.HealthScore)+"，状态: "+r.Status)
		}
	}

	overallHealth := 0.0
	if len(results) > 0 {
		overallHealth = totalHealth / float64(len(results))
	}

	return map[string]interface{}{
		"factors":        results,
		"overall_health": roundTo(overallHealth),
		"factor_count":   len(results),
		"warning_count":  len(warnings),
		"warnings":       warnings,
		"market_regime":  marketRegime,
		"timestamp":      getTimestamp(),
	}
}

// ---------- 算法实现 ----------

func computeSignalStrength(history []float64) float64 {
	if len(history) < 5 {
		return 0.5
	}
	recent := history[len(history)-5:]
	avg := meanVal(recent)
	return clampScore(avg)
}

func computeFactorStability(history []float64) float64 {
	if len(history) < 10 {
		return 0.5
	}

	// 滚动窗口标准差
	window := len(history) / 4
	var volatilities []float64
	for i := window; i <= len(history); i += window {
		segment := history[i-window : i]
		volatilities = append(volatilities, stdVal(segment))
	}

	if len(volatilities) < 2 {
		return 0.5
	}

	volMean := meanVal(volatilities)
	volStd := stdVal(volatilities)
	if volMean < 1e-8 {
		return 0.5
	}

	cv := volStd / volMean
	return clampScore(1.0 - cv)
}

func computeFactorIC(history []float64) float64 {
	if len(history) < 10 {
		return 0
	}

	// IC = 因子值与下期收益的相关系数
	// 这里用因子值的自相关作为IC代理
	n := len(history)
	if n < 3 {
		return 0
	}

	// 估计因子的预测能力
	lagged := history[:n-1]
	current := history[1:]

	minLen := len(lagged)
	if len(current) < minLen {
		minLen = len(current)
	}

	if minLen < 2 {
		return 0
	}

	l := lagged[:minLen]
	c := current[:minLen]

	m1 := meanVal(l)
	m2 := meanVal(c)

	cov := 0.0
	v1 := 0.0
	v2 := 0.0
	for i := 0; i < minLen; i++ {
		d1 := l[i] - m1
		d2 := c[i] - m2
		cov += d1 * d2
		v1 += d1 * d1
		v2 += d2 * d2
	}

	if v1 < 1e-8 || v2 < 1e-8 {
		return 0
	}
	return cov / math.Sqrt(v1*v2)
}

func computeFactorICIR(history []float64) float64 {
	ic := computeFactorIC(history)
	if len(history) < 10 {
		return 0
	}

	// 滚动IC序列
	window := 20
	if window > len(history) {
		window = len(history) / 2
	}

	var rollingICs []float64
	for i := window; i < len(history); i++ {
		segment := history[i-window : i]
		rollingICs = append(rollingICs, computeFactorIC(segment))
	}

	if len(rollingICs) < 2 {
		return ic
	}

	icStd := stdVal(rollingICs)
	if icStd < 1e-8 {
		return ic * 10
	}
	return ic / icStd
}

func computeFactorDecayRate(history []float64) float64 {
	if len(history) < 20 {
		return 0.1
	}

	halfLen := len(history) / 2
	firstHalf := history[:halfLen]
	secondHalf := history[halfLen:]

	// 因子强度衰减率
	firstStrength := math.Abs(meanVal(firstHalf))
	secondStrength := math.Abs(meanVal(secondHalf))

	if firstStrength < 1e-8 {
		return 0.5
	}

	decay := 1.0 - secondStrength/firstStrength
	return clampValue(decay, 0, 1)
}

func computeFactorCrowding(history []float64) float64 {
	if len(history) < 10 {
		return 0.3
	}

	// 同向运动比例
	var directions []float64
	for i := 1; i < len(history); i++ {
		if history[i]*history[i-1] > 0 {
			directions = append(directions, 1)
		} else {
			directions = append(directions, 0)
		}
	}

	if len(directions) == 0 {
		return 0.3
	}

	return meanVal(directions)
}

func computeRegimeCompatibility(factorName string, marketRegime string) float64 {
	// 因子与市场状态的兼容性矩阵
	compatibilityMatrix := map[string]map[string]float64{
		"momentum": {
			"BULLISH":  0.9,
			"BEARISH":  0.2,
			"NEUTRAL":  0.5,
			"HIGH_VOL": 0.4,
		},
		"value": {
			"BULLISH":  0.6,
			"BEARISH":  0.8,
			"NEUTRAL":  0.7,
			"HIGH_VOL": 0.5,
		},
		"quality": {
			"BULLISH":  0.7,
			"BEARISH":  0.9,
			"NEUTRAL":  0.75,
			"HIGH_VOL": 0.8,
		},
		"volatility": {
			"BULLISH":  0.3,
			"BEARISH":  0.7,
			"NEUTRAL":  0.5,
			"HIGH_VOL": 0.9,
		},
		"liquidity": {
			"BULLISH":  0.8,
			"BEARISH":  0.4,
			"NEUTRAL":  0.6,
			"HIGH_VOL": 0.3,
		},
		"low_volatility": {
			"BULLISH":  0.5,
			"BEARISH":  0.9,
			"NEUTRAL":  0.7,
			"HIGH_VOL": 0.95,
		},
		"dividend": {
			"BULLISH":  0.5,
			"BEARISH":  0.85,
			"NEUTRAL":  0.7,
			"HIGH_VOL": 0.75,
		},
		"size": {
			"BULLISH":  0.8,
			"BEARISH":  0.3,
			"NEUTRAL":  0.55,
			"HIGH_VOL": 0.4,
		},
		"reversal": {
			"BULLISH":  0.4,
			"BEARISH":  0.6,
			"NEUTRAL":  0.7,
			"HIGH_VOL": 0.8,
		},
	}

	// 尝试匹配因子类别
	category := inferCategory(factorName)
	if matrix, ok := compatibilityMatrix[category]; ok {
		if score, ok := matrix[marketRegime]; ok {
			return score
		}
	}

	return 0.5
}

func kalmanFilterHealth(history []float64) float64 {
	if len(history) < 10 {
		return 0.5
	}

	// 简化版 Kalman 滤波
	state := 0.5
	measurementNoise := 0.1
	processNoise := 0.01

	for _, v := range history {
		// 预测
		predictedState := state
		predictedVariance := measurementNoise + processNoise

		// 更新
		kalmanGain := predictedVariance / (predictedVariance + measurementNoise)
		state = predictedState + kalmanGain*(v-predictedState)
	}

	return clampScore(state)
}

func survivalAnalysis(history []float64) float64 {
	if len(history) < 10 {
		return 0.5
	}

	// 估计因子有效性的生存概率
	cutoff := 0.5
	survivalTime := 0
	totalTime := len(history)

	for _, v := range history {
		if v >= cutoff {
			survivalTime++
		}
	}

	return float64(survivalTime) / float64(totalTime)
}

func coxHazardModel(history []float64, decayRate float64) float64 {
	if len(history) < 10 {
		return 0.5
	}

	// Cox 比例风险模型（简化版）
	// h(t) = h0(t) * exp(β * X)
	// 这里 X = decay_rate
	hazard := math.Exp(decayRate*2 - 1)
	return clampValue(hazard, 0.1, 5.0)
}

func computeOverallHealth(result FactorHealthResult) float64 {
	weights := map[string]float64{
		"SignalStrength": 0.15,
		"Stability":      0.15,
		"IC":             0.10,
		"ICIR":           0.10,
		"Decay":          0.10,
		"Crowding":       0.10,
		"RegimeCompat":   0.10,
		"Survival":       0.10,
		"CoxHazard":      0.10,
	}

	score := 0.0
	score += result.SignalStrength * weights["SignalStrength"] * 100
	score += result.Stability * weights["Stability"] * 100
	score += clampValue(result.IC, 0, 1) * weights["IC"] * 100
	score += clampValue(math.Abs(result.ICIR)/5.0, 0, 1) * weights["ICIR"] * 100
	score += (1.0 - result.Decay) * weights["Decay"] * 100
	score += (1.0 - result.Crowding) * weights["Crowding"] * 100
	score += result.RegimeCompatibility * weights["RegimeCompat"] * 100
	score += result.SurvivalScore * weights["Survival"] * 100
	score += (1.0 - clampValue(result.CoxHazardRatio/5.0, 0, 1)) * weights["CoxHazard"] * 100

	return clampValue(score, 0, 100)
}

func inferCategory(factorName string) string {
	categories := []struct {
		keywords []string
		category string
	}{
		{[]string{"momentum", "reversal"}, "momentum"},
		{[]string{"value", "ep_ratio", "bp_ratio"}, "value"},
		{[]string{"quality", "roe", "gross_margin"}, "quality"},
		{[]string{"volatility", "beta", "low_vol"}, "volatility"},
		{[]string{"liquidity", "turnover", "amihud"}, "liquidity"},
		{[]string{"size", "market_cap"}, "size"},
		{[]string{"dividend", "yield"}, "dividend"},
	}

	for _, cat := range categories {
		for _, kw := range cat.keywords {
			if containsStr(factorName, kw) {
				return cat.category
			}
		}
	}
	return "value"
}

// ---------- 辅助函数 ----------

func meanVal(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range values {
		sum += v
	}
	return sum / float64(len(values))
}

func stdVal(values []float64) float64 {
	if len(values) < 2 {
		return 0
	}
	m := meanVal(values)
	variance := 0.0
	for _, v := range values {
		diff := v - m
		variance += diff * diff
	}
	return math.Sqrt(variance / float64(len(values)-1))
}

func clampScore(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func clampValue(v, min, max float64) float64 {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

func roundTo(v float64) float64 {
	return float64(int64(v*100+0.5)) / 100
}

func formatFloat(v float64) string {
	return formatFloat2(v)
}

func formatFloat2(v float64) string {
	return ""
}

func containsStr(s, substr string) bool {
	return len(s) >= len(substr) && searchStr(s, substr)
}

func searchStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func getTimestamp() int64 {
	return 0
}
