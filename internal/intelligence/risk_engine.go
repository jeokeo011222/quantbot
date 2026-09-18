package intelligence

import (
	"math"
	"sort"
)

// RiskEngine 风控引擎 - 独立于 Agent 的计算中台
// Agent 仅负责调用本引擎获取结果，不直接进行风险计算
type RiskEngine struct{}

// NewRiskEngine 创建风控引擎
func NewRiskEngine() *RiskEngine {
	return &RiskEngine{}
}

// PortfolioRiskReport 组合风险报告
type PortfolioRiskReport struct {
	VaR95         float64          `json:"var_95"`
	VaR99         float64          `json:"var_99"`
	CVaR95        float64          `json:"cvar_95"`
	Volatility    float64          `json:"volatility"`
	MaxDrawdown   float64          `json:"max_drawdown"`
	Beta          float64          `json:"beta"`
	Correlation   float64          `json:"correlation"`
	Concentration float64          `json:"concentration"`
	EMD           float64          `json:"emd"`
	Liquidity     float64          `json:"liquidity"`
	StressTests   []StressScenario `json:"stress_tests"`
	OverallScore  float64          `json:"overall_score"`
	Status        string           `json:"status"` // NORMAL / WATCH / WARNING / CRITICAL
}

// StressScenario 压力测试情景
type StressScenario struct {
	Scenario      string  `json:"scenario"`
	Description   string  `json:"description"`
	VolMultiplier float64 `json:"vol_multiplier"`
	Shock         float64 `json:"shock"`
	EstimatedLoss float64 `json:"estimated_loss"`
	Probability   float64 `json:"probability"`
}

// StructuralRiskReport 结构风险报告
type StructuralRiskReport struct {
	GeometricRisk   float64 `json:"geometric_risk"`
	TopologicalRisk float64 `json:"topological_risk"`
	ContagionRisk   float64 `json:"contagion_risk"`
	NetworkRisk     float64 `json:"network_risk"`
	ManifoldRisk    float64 `json:"manifold_risk"`
	OverallScore    float64 `json:"overall_score"`
}

// FactorRiskReport 因子风险报告
type FactorRiskReport struct {
	FactorExposure map[string]float64 `json:"factor_exposure"`
	FactorCrowding map[string]float64 `json:"factor_crowding"`
	FactorHealth   map[string]float64 `json:"factor_health"`
	FactorDecay    map[string]float64 `json:"factor_decay"`
	OverallScore   float64            `json:"overall_score"`
}

// ComputePortfolioRisk 计算组合风险
func (re *RiskEngine) ComputePortfolioRisk(
	returns []float64,
	benchmarkReturns []float64,
	weights map[string]float64,
) PortfolioRiskReport {
	report := PortfolioRiskReport{}

	// VaR 计算
	report.VaR95 = computeHistoricalVaR(returns, 0.95)
	report.VaR99 = computeHistoricalVaR(returns, 0.99)
	report.CVaR95 = computeHistoricalCVaR(returns, 0.95)

	// 波动率
	report.Volatility = annualizedVolatility(returns)

	// 最大回撤
	report.MaxDrawdown = computeMaxDrawdown(returns)

	// Beta
	report.Beta = computeBeta(returns, benchmarkReturns)

	// 相关性
	report.Correlation = computeCorrelation(returns, benchmarkReturns)

	// 集中度 (HHI)
	report.Concentration = computeHHI(weights)

	// 流动性代理
	report.Liquidity = computeLiquidityScore(returns)

	// 压力测试
	report.StressTests = generateStressScenarios(returns)

	// 综合评分
	report.OverallScore = computeRiskScore(report)
	report.Status = determineRiskStatus(report.OverallScore)

	return report
}

// ComputeStructuralRisk 计算结构风险
func (re *RiskEngine) ComputeStructuralRisk(
	priceSeries [][]float64,
) StructuralRiskReport {
	report := StructuralRiskReport{}

	if len(priceSeries) < 2 {
		report.GeometricRisk = 0.5
		report.TopologicalRisk = 0.5
		report.ContagionRisk = 0.5
		report.NetworkRisk = 0.5
		report.ManifoldRisk = 0.5
		report.OverallScore = 0.5
		return report
	}

	// 几何风险：基于收益曲线的曲率
	returns := make([][]float64, len(priceSeries))
	for i, prices := range priceSeries {
		returns[i] = computeReturns(prices)
	}

	report.GeometricRisk = computeGeometricRisk(returns)
	report.TopologicalRisk = computeTopologicalRisk(returns)
	report.ContagionRisk = computeContagionRisk(returns)
	report.NetworkRisk = computeNetworkRisk(returns)
	report.ManifoldRisk = computeManifoldRisk(returns)

	report.OverallScore = (report.GeometricRisk + report.TopologicalRisk +
		report.ContagionRisk + report.NetworkRisk + report.ManifoldRisk) / 5.0

	return report
}

// ComputeFactorRisk 计算因子风险
func (re *RiskEngine) ComputeFactorRisk(
	factorExposures map[string]float64,
	factorHealth map[string]float64,
	factorCrowding map[string]float64,
	factorDecay map[string]float64,
) FactorRiskReport {
	report := FactorRiskReport{
		FactorExposure: factorExposures,
		FactorCrowding: factorCrowding,
		FactorHealth:   factorHealth,
		FactorDecay:    factorDecay,
	}

	// 综合因子风险评分
	var totalRisk float64
	count := 0
	for factor := range factorExposures {
		exposure := factorExposures[factor]
		health := 0.5
		crowding := 0.3
		decay := 0.1

		if h, ok := factorHealth[factor]; ok {
			health = h
		}
		if c, ok := factorCrowding[factor]; ok {
			crowding = c
		}
		if d, ok := factorDecay[factor]; ok {
			decay = d
		}

		// 调整后因子风险 = 暴露度 × (1 - 健康度调整) × (1 + 拥挤惩罚) × (1 + 衰减惩罚)
		adjustedRisk := exposure * (1.0 - health*0.5) * (1.0 + crowding*0.5) * (1.0 + decay)
		totalRisk += adjustedRisk
		count++
	}

	if count > 0 {
		report.OverallScore = math.Min(totalRisk/float64(count), 1.0)
	} else {
		report.OverallScore = 0.3
	}

	return report
}

// ---------- 内部计算函数 ----------

func computeHistoricalVaR(returns []float64, confidence float64) float64 {
	if len(returns) == 0 {
		return 0
	}
	sorted := make([]float64, len(returns))
	copy(sorted, returns)
	sort.Float64s(sorted)

	idx := int(float64(len(sorted)) * (1 - confidence))
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return math.Abs(sorted[idx])
}

func computeHistoricalCVaR(returns []float64, confidence float64) float64 {
	if len(returns) == 0 {
		return 0
	}
	sorted := make([]float64, len(returns))
	copy(sorted, returns)
	sort.Float64s(sorted)

	idx := int(float64(len(sorted)) * (1 - confidence))
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}

	tail := sorted[:idx+1]
	if len(tail) == 0 {
		return 0
	}
	sum := 0.0
	for _, r := range tail {
		sum += math.Abs(r)
	}
	return sum / float64(len(tail))
}

func annualizedVolatility(returns []float64) float64 {
	if len(returns) < 2 {
		return 0
	}
	m := mean(returns)
	variance := 0.0
	for _, r := range returns {
		diff := r - m
		variance += diff * diff
	}
	std := math.Sqrt(variance / float64(len(returns)-1))
	return std * math.Sqrt(252)
}

func computeMaxDrawdown(returns []float64) float64 {
	if len(returns) < 2 {
		return 0
	}
	cumulative := make([]float64, len(returns)+1)
	cumulative[0] = 1.0
	for i, r := range returns {
		cumulative[i+1] = cumulative[i] * (1 + r)
	}

	peak := cumulative[0]
	maxDD := 0.0
	for _, v := range cumulative {
		if v > peak {
			peak = v
		}
		dd := (peak - v) / peak
		if dd > maxDD {
			maxDD = dd
		}
	}
	return maxDD
}

func computeBeta(assetReturns, marketReturns []float64) float64 {
	minLen := len(assetReturns)
	if len(marketReturns) < minLen {
		minLen = len(marketReturns)
	}
	if minLen < 2 {
		return 1.0
	}

	ar := assetReturns[len(assetReturns)-minLen:]
	mr := marketReturns[len(marketReturns)-minLen:]

	meanAR := mean(ar)
	meanMR := mean(mr)

	cov := 0.0
	variance := 0.0
	for i := 0; i < minLen; i++ {
		cov += (ar[i] - meanAR) * (mr[i] - meanMR)
		variance += (mr[i] - meanMR) * (mr[i] - meanMR)
	}

	if variance < 1e-8 {
		return 1.0
	}
	return cov / variance
}

func computeCorrelation(returns1, returns2 []float64) float64 {
	minLen := len(returns1)
	if len(returns2) < minLen {
		minLen = len(returns2)
	}
	if minLen < 2 {
		return 0
	}

	r1 := returns1[len(returns1)-minLen:]
	r2 := returns2[len(returns2)-minLen:]

	m1 := mean(r1)
	m2 := mean(r2)

	cov := 0.0
	v1 := 0.0
	v2 := 0.0
	for i := 0; i < minLen; i++ {
		d1 := r1[i] - m1
		d2 := r2[i] - m2
		cov += d1 * d2
		v1 += d1 * d1
		v2 += d2 * d2
	}

	if v1 < 1e-8 || v2 < 1e-8 {
		return 0
	}
	return cov / math.Sqrt(v1*v2)
}

func computeHHI(weights map[string]float64) float64 {
	if len(weights) == 0 {
		return 0
	}
	hhi := 0.0
	for _, w := range weights {
		hhi += w * w
	}
	return hhi
}

func computeLiquidityScore(returns []float64) float64 {
	if len(returns) < 5 {
		return 0.5
	}
	turnover := 0.0
	for _, r := range returns {
		turnover += math.Abs(r)
	}
	avgTurnover := turnover / float64(len(returns))
	return math.Max(0, math.Min(1, 1.0-avgTurnover*10))
}

func computeRiskScore(report PortfolioRiskReport) float64 {
	var score float64
	weights := map[string]float64{
		"VaR":           0.20,
		"CVaR":          0.15,
		"Volatility":    0.15,
		"MaxDD":         0.15,
		"Concentration": 0.10,
		"EMD":           0.10,
		"Liquidity":     0.15,
	}

	score += report.VaR95 / 0.05 * weights["VaR"]
	score += report.CVaR95 / 0.08 * weights["CVaR"]
	score += report.Volatility / 0.25 * weights["Volatility"]
	score += report.MaxDrawdown / 0.15 * weights["MaxDD"]
	score += report.Concentration * weights["Concentration"]
	emdPenalty := 0.0
	if report.EMD > 0 {
		emdPenalty = 1.0 / report.EMD
	}
	score += emdPenalty * weights["EMD"]
	score += (1.0 - report.Liquidity) * weights["Liquidity"]

	return math.Max(0, math.Min(1, score))
}

func determineRiskStatus(score float64) string {
	if score < 0.3 {
		return "NORMAL"
	} else if score < 0.5 {
		return "WATCH"
	} else if score < 0.7 {
		return "WARNING"
	}
	return "CRITICAL"
}

func computeGeometricRisk(returns [][]float64) float64 {
	if len(returns) < 2 {
		return 0.5
	}

	var avgCurvature float64
	for _, r := range returns {
		if len(r) < 5 {
			continue
		}
		// 估计收益曲线曲率
		n := len(r)
		if n >= 3 {
			curvature := math.Abs(r[n-1] - 2*r[n/2] + r[0])
			avgCurvature += curvature
		}
	}

	if len(returns) > 0 {
		avgCurvature /= float64(len(returns))
	}

	return math.Min(avgCurvature*10+0.3, 1.0)
}

func computeTopologicalRisk(returns [][]float64) float64 {
	if len(returns) < 2 {
		return 0.5
	}

	// 估计收益序列之间的拓扑相似度
	var avgDistance float64
	count := 0
	for i := 0; i < len(returns); i++ {
		for j := i + 1; j < len(returns); j++ {
			minLen := len(returns[i])
			if len(returns[j]) < minLen {
				minLen = len(returns[j])
			}
			if minLen < 2 {
				continue
			}
			dist := 0.0
			for k := 0; k < minLen; k++ {
				diff := returns[i][k] - returns[j][k]
				dist += diff * diff
			}
			dist = math.Sqrt(dist / float64(minLen))
			avgDistance += dist
			count++
		}
	}

	if count > 0 {
		avgDistance /= float64(count)
	}

	return math.Min(avgDistance*5+0.3, 1.0)
}

func computeContagionRisk(returns [][]float64) float64 {
	if len(returns) < 2 {
		return 0.5
	}

	// 估计收益之间的共振/传染效应
	var avgCorr float64
	count := 0
	for i := 0; i < len(returns); i++ {
		for j := i + 1; j < len(returns); j++ {
			minLen := len(returns[i])
			if len(returns[j]) < minLen {
				minLen = len(returns[j])
			}
			if minLen < 2 {
				continue
			}
			corr := computeCorrelation(returns[i][:minLen], returns[j][:minLen])
			avgCorr += math.Abs(corr)
			count++
		}
	}

	if count > 0 {
		avgCorr /= float64(count)
	}

	// 高相关性意味着高传染风险
	return avgCorr
}

func computeNetworkRisk(returns [][]float64) float64 {
	if len(returns) < 2 {
		return 0.5
	}

	// 基于相关性矩阵估计网络风险
	var density float64
	for i := 0; i < len(returns); i++ {
		for j := 0; j < len(returns); j++ {
			if i == j {
				continue
			}
			minLen := len(returns[i])
			if len(returns[j]) < minLen {
				minLen = len(returns[j])
			}
			if minLen < 2 {
				continue
			}
			corr := computeCorrelation(returns[i][:minLen], returns[j][:minLen])
			if math.Abs(corr) > 0.5 {
				density++
			}
		}
	}

	total := float64(len(returns) * (len(returns) - 1))
	if total > 0 {
		density /= total
	}

	return density
}

func computeManifoldRisk(returns [][]float64) float64 {
	if len(returns) < 2 {
		return 0.5
	}

	// 基于流形维度估计
	var totalVariance float64
	var variances []float64
	for _, r := range returns {
		if len(r) < 2 {
			continue
		}
		m := mean(r)
		v := 0.0
		for _, x := range r {
			v += (x - m) * (x - m)
		}
		v /= float64(len(r))
		variances = append(variances, v)
		totalVariance += v
	}

	if totalVariance < 1e-8 {
		return 0.5
	}

	// 低有效维度 = 高集中风险
	var sumRatio float64
	for _, v := range variances {
		sumRatio += (v / totalVariance) * (v / totalVariance)
	}

	return math.Min(sumRatio*2+0.2, 1.0)
}

func generateStressScenarios(returns []float64) []StressScenario {
	baseVol := annualizedVolatility(returns)

	scenarioDefs := []struct {
		name        string
		description string
		volMult     float64
		shock       float64
	}{
		{"市场暴跌", "市场系统性下跌，指数重挫", 3.0, -0.15},
		{"波动率飙升", "波动率异常放大，剧烈震荡", 2.5, -0.05},
		{"利率冲击", "无风险利率大幅上行", 1.5, -0.08},
		{"流动性枯竭", "市场流动性急剧萎缩", 2.0, -0.10},
		{"板块崩盘", "重仓板块集中崩盘", 2.0, -0.12},
		{"相关性破裂", "资产相关性结构断裂", 2.2, -0.07},
	}

	var scenarios []StressScenario
	for _, def := range scenarioDefs {
		estimatedLoss := baseVol * def.volMult * math.Abs(def.shock) * 10
		if estimatedLoss > 1 {
			estimatedLoss = 1
		}
		scenarios = append(scenarios, StressScenario{
			Scenario:      def.name,
			Description:   def.description,
			VolMultiplier: def.volMult,
			Shock:         def.shock,
			EstimatedLoss: estimatedLoss,
			Probability:   roundToFloat(baseVol*0.5 + 0.05),
		})
	}

	return scenarios
}

func computeReturns(prices []float64) []float64 {
	returns := make([]float64, 0, len(prices)-1)
	for i := 1; i < len(prices); i++ {
		if prices[i-1] > 0 {
			returns = append(returns, (prices[i]-prices[i-1])/prices[i-1])
		}
	}
	return returns
}

func mean(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range values {
		sum += v
	}
	return sum / float64(len(values))
}

func roundToFloat(v float64) float64 {
	return float64(int64(v*10000+0.5)) / 10000
}
