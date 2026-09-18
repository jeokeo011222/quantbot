package screener

import (
	"fmt"
	"math"
)

// FactorStats 因子统计量
type FactorStats struct {
	Mean   float64
	StdDev float64
	Min    float64
	Max    float64
}

// computeStats 计算Z-score统计量
func computeStats(raw map[FactorID][]float64) map[FactorID]FactorStats {
	stats := make(map[FactorID]FactorStats)

	for id, values := range raw {
		if len(values) == 0 {
			stats[id] = FactorStats{Mean: 0, StdDev: 1, Min: 0, Max: 1}
			continue
		}

		var sum, min, max float64
		min = values[0]
		max = values[0]
		for _, v := range values {
			sum += v
			if v < min {
				min = v
			}
			if v > max {
				max = v
			}
		}
		mean := sum / float64(len(values))

		var variance float64
		for _, v := range values {
			diff := v - mean
			variance += diff * diff
		}
		stdDev := math.Sqrt(variance / float64(len(values)))
		if stdDev < 1e-10 {
			stdDev = 1.0
		}

		stats[id] = FactorStats{
			Mean:   mean,
			StdDev: stdDev,
			Min:    min,
			Max:    max,
		}
	}

	return stats
}

// zScoreTo0100 将Z-score转换为0-100分
func zScoreTo0100(value, mean, stdDev float64) float64 {
	z := (value - mean) / stdDev
	sigmoid := 1.0 / (1.0 + math.Exp(-z))
	return sigmoid * 100
}

// inverseZScoreTo0100 反向因子（值越低越好）
func inverseZScoreTo0100(value, mean, stdDev float64) float64 {
	z := (mean - value) / stdDev
	sigmoid := 1.0 / (1.0 + math.Exp(-z))
	return sigmoid * 100
}

// buildFormulaString 构建公式字符串用于展示
// 按固定顺序排序因子，确保公式展示的一致性
func buildFormulaString(weights map[FactorID]float64) string {
	// 固定因子展示顺序
	factorOrder := []FactorID{
		FactorValue,
		FactorQuality,
		FactorMomentum,
		FactorLowVolatility,
		FactorEarningsStability,
		FactorLiquidity,
		FactorOrderBook,
		FactorCointegration,
	}

	factorNames := map[FactorID]string{
		FactorValue:             "Value",
		FactorQuality:           "Quality",
		FactorMomentum:          "Momentum",
		FactorLowVolatility:     "LowVol",
		FactorEarningsStability: "Stability",
		FactorLiquidity:         "Liquidity",
		FactorOrderBook:         "OrderBook",
		FactorCointegration:     "Cointegration",
	}

	formula := "Score = "
	first := true

	for _, id := range factorOrder {
		weight, ok := weights[id]
		if !ok || weight <= 0 {
			continue
		}
		if !first {
			formula += " + "
		}
		formula += fmt.Sprintf("%.0f%%×%s", weight*100, factorNames[id])
		first = false
	}

	// 如果所有权重都为0，回退到遍历所有因子
	if first {
		for id, weight := range weights {
			if weight > 0 {
				if !first {
					formula += " + "
				}
				formula += fmt.Sprintf("%.0f%%×%s", weight*100, factorNames[id])
				first = false
			}
		}
	}

	return formula
}

// roundScore 保留1位小数
func roundScore(v float64) float64 {
	return float64(int64(v*10+0.5)) / 10
}
