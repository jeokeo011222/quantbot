package screener

import (
	"fmt"
	"log"
	"math/rand"
	"time"

	"github.com/quantpilot/quantpilot/internal/data"
)

// ==================== 智能因子组合器 ====================

// SmartFactorComposer 智能因子组合器
// 根据用户画像、市场状态、因子健康度和历史表现动态调整因子权重
type SmartFactorComposer struct {
	factorHistory map[FactorID][]FactorEvaluation
}

// FactorEvaluation 因子历史评估记录
type FactorEvaluation struct {
	FactorID        FactorID  `json:"factorId"`
	AvgScore        float64   `json:"avgScore"`        // 该因子的平均选股得分
	TopDecileReturn float64   `json:"topDecileReturn"` // 该因子选出的Top 10%股票的收益率
	HitRate         float64   `json:"hitRate"`         // 胜率
	EvaluatedAt     time.Time `json:"evaluatedAt"`
	MarketState     string    `json:"marketState"`
}

// NewSmartFactorComposer 创建智能因子组合器
func NewSmartFactorComposer() *SmartFactorComposer {
	return &SmartFactorComposer{
		factorHistory: make(map[FactorID][]FactorEvaluation),
	}
}

// RecordEvaluation 记录因子评估结果
func (sc *SmartFactorComposer) RecordEvaluation(eval FactorEvaluation) {
	sc.factorHistory[eval.FactorID] = append(sc.factorHistory[eval.FactorID], eval)

	// 只保留最近50条记录
	if len(sc.factorHistory[eval.FactorID]) > 50 {
		sc.factorHistory[eval.FactorID] = sc.factorHistory[eval.FactorID][len(sc.factorHistory[eval.FactorID])-50:]
	}
}

// ClearHistory 清空因子历史
func (sc *SmartFactorComposer) ClearHistory() {
	sc.factorHistory = make(map[FactorID][]FactorEvaluation)
}

// ==================== 核心智能权重计算 ====================

// CalculateSmartWeights 综合计算智能因子权重
// 融合：策略模板基础权重 + 用户画像 + 市场状态 + 因子健康度 + 因子历史表现
// 策略模板权重为主要基础，其他因素仅做小幅微调
func (sc *SmartFactorComposer) CalculateSmartWeights(
	profile *data.InvestorProfile,
	marketState *MarketState,
	factorHealth map[FactorID]FactorHealth,
	baseWeights ...map[FactorID]float64,
) map[FactorID]float64 {
	// 1. 确定基础权重和来源
	var weights map[FactorID]float64
	hasStrategyTemplate := false
	if len(baseWeights) > 0 && baseWeights[0] != nil {
		weights = copyWeights(baseWeights[0])
		hasStrategyTemplate = true
		log.Printf("[SmartComposer] 使用策略模板权重作为基础: %v", weights)
	} else {
		weights = sc.calculateProfileWeights(profile)
	}

	// 2. 根据市场状态调整（仅微调，幅度不超过基础权重的15%）
	if hasStrategyTemplate {
		weights = sc.adjustByMarketStateConservative(weights, marketState)
	} else {
		weights = sc.adjustByMarketState(weights, marketState)
	}

	// 3. 根据因子健康度调整（仅在策略模板基础上做保守调整）
	if hasStrategyTemplate {
		weights = sc.adjustByFactorHealthConservative(weights, factorHealth)
	} else {
		weights = sc.adjustByFactorHealth(weights, factorHealth)
	}

	// 4. 根据因子历史表现调整
	weights = sc.adjustByFactorHistory(weights)

	// 5. 归一化
	weights = normalizeWeights(weights)

	// 6. 如果使用了策略模板，确保最终权重与模板有最低相似度
	if hasStrategyTemplate {
		weights = sc.ensureStrategyCoherence(weights, baseWeights[0])
	}

	log.Printf("[SmartComposer] Final weights: %v", weights)
	return weights
}

// copyWeights 复制权重map
func copyWeights(src map[FactorID]float64) map[FactorID]float64 {
	dst := make(map[FactorID]float64, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

// calculateProfileWeights 根据用户画像微调权重（仅做小幅调整，不替换基础权重）
func (sc *SmartFactorComposer) calculateProfileWeights(profile *data.InvestorProfile) map[FactorID]float64 {
	if profile == nil {
		return defaultWeights()
	}

	weights := defaultWeights()

	// 风险承受能力：小幅调整
	switch profile.RiskTolerance {
	case "R1":
		weights[FactorLowVolatility] += 0.10
		weights[FactorQuality] += 0.05
		weights[FactorMomentum] -= 0.08
	case "R2":
		weights[FactorLowVolatility] += 0.05
		weights[FactorQuality] += 0.03
		weights[FactorMomentum] -= 0.04
	case "R3":
		// 保持默认
	case "R4":
		weights[FactorMomentum] += 0.05
		weights[FactorLiquidity] += 0.03
		weights[FactorLowVolatility] -= 0.04
	case "R5":
		weights[FactorMomentum] += 0.10
		weights[FactorLiquidity] += 0.05
		weights[FactorLowVolatility] -= 0.09
	// 投资规划画像的风险承受能力为 LOW/MODERATE/HIGH（与 R1-R5 等价映射）
	case "LOW":
		weights[FactorLowVolatility] += 0.08
		weights[FactorQuality] += 0.04
		weights[FactorMomentum] -= 0.06
	case "MODERATE":
		// 保持默认（中性偏好）
	case "HIGH":
		weights[FactorMomentum] += 0.08
		weights[FactorLiquidity] += 0.04
		weights[FactorLowVolatility] -= 0.07
	}

	// 投资风格：小幅调整
	switch profile.InvestmentStyle {
	case "VALUE":
		weights[FactorValue] += 0.08
		weights[FactorQuality] += 0.03
		weights[FactorMomentum] -= 0.05
	case "GROWTH":
		weights[FactorMomentum] += 0.08
		weights[FactorValue] -= 0.05
	case "DIVIDEND":
		weights[FactorQuality] += 0.05
		weights[FactorEarningsStability] += 0.05
		weights[FactorMomentum] -= 0.05
	case "QUALITY":
		weights[FactorQuality] += 0.08
		weights[FactorEarningsStability] += 0.03
	}

	// 投资期限：小幅调整
	switch profile.InvestmentHorizon {
	case "SHORT":
		weights[FactorMomentum] += 0.05
		weights[FactorLiquidity] += 0.05
		weights[FactorLowVolatility] -= 0.05
	case "LONG":
		weights[FactorValue] += 0.05
		weights[FactorQuality] += 0.03
		weights[FactorMomentum] -= 0.04
	case "VERY_LONG":
		weights[FactorValue] += 0.08
		weights[FactorEarningsStability] += 0.05
		weights[FactorMomentum] -= 0.08
	}

	// 投资目标：小幅调整
	switch profile.InvestmentObjective {
	case "GROWTH":
		weights[FactorMomentum] += 0.05
	case "INCOME":
		weights[FactorQuality] += 0.05
	case "PRESERVATION":
		weights[FactorLowVolatility] += 0.08
		weights[FactorQuality] += 0.03
		weights[FactorMomentum] -= 0.06
	}

	return weights
}

// adjustByMarketState 根据市场状态调整权重（激进模式，无策略模板时使用）
func (sc *SmartFactorComposer) adjustByMarketState(weights map[FactorID]float64, ms *MarketState) map[FactorID]float64 {
	if ms == nil {
		return weights
	}

	switch ms.Regime {
	case "bull":
		weights[FactorMomentum] += 0.10
		weights[FactorValue] += 0.05
		weights[FactorLowVolatility] -= 0.10
		weights[FactorLiquidity] -= 0.05
	case "bear":
		weights[FactorLowVolatility] += 0.10
		weights[FactorQuality] += 0.05
		weights[FactorValue] += 0.05
		weights[FactorMomentum] -= 0.15
		weights[FactorEarningsStability] -= 0.05
	case "bull_range":
		weights[FactorMomentum] += 0.05
		weights[FactorLowVolatility] -= 0.05
	case "bear_range":
		weights[FactorLowVolatility] += 0.05
		weights[FactorQuality] += 0.03
		weights[FactorMomentum] -= 0.08
	default:
	}

	return weights
}

// adjustByMarketStateConservative 根据市场状态保守调整权重（有策略模板时使用）
// 调整幅度不超过基础权重的15%，保持策略特征
func (sc *SmartFactorComposer) adjustByMarketStateConservative(weights map[FactorID]float64, ms *MarketState) map[FactorID]float64 {
	if ms == nil {
		return weights
	}

	const maxAdjustRatio = 0.15

	switch ms.Regime {
	case "bull":
		adjustWeight(weights, FactorMomentum, 0.03, maxAdjustRatio)
		adjustWeight(weights, FactorValue, 0.02, maxAdjustRatio)
		adjustWeight(weights, FactorLowVolatility, -0.03, maxAdjustRatio)
	case "bear":
		adjustWeight(weights, FactorLowVolatility, 0.03, maxAdjustRatio)
		adjustWeight(weights, FactorQuality, 0.02, maxAdjustRatio)
		adjustWeight(weights, FactorMomentum, -0.04, maxAdjustRatio)
	case "bull_range":
		adjustWeight(weights, FactorMomentum, 0.02, maxAdjustRatio)
		adjustWeight(weights, FactorLowVolatility, -0.02, maxAdjustRatio)
	case "bear_range":
		adjustWeight(weights, FactorLowVolatility, 0.02, maxAdjustRatio)
		adjustWeight(weights, FactorQuality, 0.01, maxAdjustRatio)
		adjustWeight(weights, FactorMomentum, -0.02, maxAdjustRatio)
	default:
	}

	return weights
}

// adjustWeight 对单个因子权重做受限调整
func adjustWeight(weights map[FactorID]float64, id FactorID, delta float64, maxRatio float64) {
	current := weights[id]
	maxDelta := current * maxRatio
	if delta > 0 && delta > maxDelta {
		delta = maxDelta
	}
	if delta < 0 && -delta > maxDelta {
		delta = -maxDelta
	}
	weights[id] = current + delta
}

// ensureStrategyCoherence 确保最终权重与策略模板有最低相似度
func (sc *SmartFactorComposer) ensureStrategyCoherence(finalWeights map[FactorID]float64, templateWeights map[FactorID]float64) map[FactorID]float64 {
	dotProduct := 0.0
	templateNorm := 0.0
	finalNorm := 0.0

	for id, tw := range templateWeights {
		fw := finalWeights[id]
		dotProduct += tw * fw
		templateNorm += tw * tw
		finalNorm += fw * fw
	}

	if templateNorm < 1e-10 || finalNorm < 1e-10 {
		return finalWeights
	}

	cosineSim := dotProduct / (templateNorm * finalNorm)

	if cosineSim < 0.6 {
		log.Printf("[SmartComposer] 策略相似度 %.3f 过低，进行混合调整", cosineSim)
		blended := make(map[FactorID]float64)
		for id, fw := range finalWeights {
			blended[id] = fw*0.6 + templateWeights[id]*0.4
		}
		return normalizeWeights(blended)
	}

	return finalWeights
}

// adjustByFactorHealth 根据因子健康度调整权重（激进模式）
func (sc *SmartFactorComposer) adjustByFactorHealth(weights map[FactorID]float64, health map[FactorID]FactorHealth) map[FactorID]float64 {
	if health == nil {
		return weights
	}

	for id, h := range health {
		if h.Status == "DEAD" {
			weights[id] *= 0.5
		} else if h.Status == "WEAK" {
			weights[id] *= 0.8
		}

		if h.ICIR > 1.0 {
			weights[id] *= 1.1
		}
	}

	return weights
}

// adjustByFactorHealthConservative 根据因子健康度保守调整权重（有策略模板时使用）
// 调整幅度减半，避免因子健康度完全覆盖策略特征
func (sc *SmartFactorComposer) adjustByFactorHealthConservative(weights map[FactorID]float64, health map[FactorID]FactorHealth) map[FactorID]float64 {
	if health == nil {
		return weights
	}

	for id, h := range health {
		if h.Status == "DEAD" {
			// 因子失效，减少25%（而非50%）
			weights[id] *= 0.75
		} else if h.Status == "WEAK" {
			// 因子减弱，减少10%（而非20%）
			weights[id] *= 0.90
		}

		// ICIR > 1 的因子给予小幅加权
		if h.ICIR > 1.0 {
			weights[id] *= 1.05
		}
	}

	return weights
}

// adjustByFactorHistory 根据因子历史表现调整权重
func (sc *SmartFactorComposer) adjustByFactorHistory(weights map[FactorID]float64) map[FactorID]float64 {
	for id, history := range sc.factorHistory {
		if len(history) < 5 {
			continue // 历史数据不足，不调整
		}

		// 计算该因子的平均Top Decile收益率
		totalReturn := 0.0
		totalHitRate := 0.0
		for _, eval := range history {
			totalReturn += eval.TopDecileReturn
			totalHitRate += eval.HitRate
		}
		avgReturn := totalReturn / float64(len(history))
		avgHitRate := totalHitRate / float64(len(history))

		// 如果因子历史表现优秀 (Top Decile年化收益 > 5%)
		if avgReturn > 0.05 && avgHitRate > 0.55 {
			boost := 0.1 + avgReturn*0.5 // 最多增加约25%
			weights[id] += boost
			log.Printf("[SmartComposer] Boosting factor %v: avgReturn=%.2f, hitRate=%.2f, boost=%.2f",
				id, avgReturn, avgHitRate, boost)
		} else if avgReturn < 0 || avgHitRate < 0.45 {
			// 如果因子历史表现差，减少权重
			weights[id] *= 0.7
			log.Printf("[SmartComposer] Reducing factor %v: avgReturn=%.2f, hitRate=%.2f",
				id, avgReturn, avgHitRate)
		}
	}

	return weights
}

// ==================== 辅助函数 ====================

// defaultWeights 返回默认权重配置
func defaultWeights() map[FactorID]float64 {
	return map[FactorID]float64{
		FactorValue:             0.20,
		FactorQuality:           0.20,
		FactorMomentum:          0.20,
		FactorLowVolatility:     0.15,
		FactorEarningsStability: 0.15,
		FactorLiquidity:         0.10,
		FactorOrderBook:         0.05, // 盘口因子（量比/内外盘/封板），数据不足时自动降级不参与
		FactorCointegration:     0.04, // 协整/配对因子（相对市场超卖吸引），无市场基准时自动降级不参与
	}
}

// normalizeWeights 归一化权重
func normalizeWeights(weights map[FactorID]float64) map[FactorID]float64 {
	total := 0.0
	for _, w := range weights {
		if w < 0 {
			w = 0
		}
		total += w
	}

	if total <= 0 {
		// 等权重
		equalWeight := 1.0 / float64(len(weights))
		for id := range weights {
			weights[id] = equalWeight
		}
		return weights
	}

	for id := range weights {
		w := weights[id]
		if w < 0 {
			w = 0
		}
		weights[id] = w / total
	}

	return weights
}

// diversifyWeights 去同质化权重微扰（用户个性化种子）。
// 以 uid 哈希 seed 做「确定性的小幅扰动」（每因子在 ±10% 幅度内颤动）后重新归一化，
// 使不同用户的选股在保持同一策略风险家族的前提下命中略不同的候选子集，
// 解决"多人使用选出的股票高度雷同"（策略池分组 + 用户个性化）。seed=0 时不扰动。
func diversifyWeights(weights map[FactorID]float64, seed int64) map[FactorID]float64 {
	if seed == 0 || len(weights) == 0 {
		return weights
	}
	rng := rand.New(rand.NewSource(seed))
	out := make(map[FactorID]float64, len(weights))
	for id, w := range weights {
		if w <= 0 {
			out[id] = w
			continue
		}
		// 确定性颤动：[-10%, +10%]，保证风险特征不跑偏，又让总分排名随用户而变化
		jitter := (rng.Float64()*2 - 1) * 0.10 * w
		out[id] = w + jitter
	}
	return normalizeWeights(out)
}

// FormatProfileSummary 格式化用户画像摘要
func FormatProfileSummary(profile *data.InvestorProfile) string {
	if profile == nil {
		return "默认配置"
	}
	return fmt.Sprintf("风险:%s 风格:%s 期限:%s 目标:%s",
		profile.RiskTolerance,
		profile.InvestmentStyle,
		profile.InvestmentHorizon,
		profile.InvestmentObjective,
	)
}
