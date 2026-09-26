package intelligence

import (
	"math"
	"sort"
)

// PortfolioEngine 组合优化引擎
// 提供多种组合优化算法，Agent 调用本引擎获取优化结果
type PortfolioEngine struct{}

// NewPortfolioEngine 创建组合优化引擎
func NewPortfolioEngine() *PortfolioEngine {
	return &PortfolioEngine{}
}

// PortfolioOptimizationResult 组合优化结果
// 与 portfolio.OptimizationResult 字段对齐（ExpectedVolatility），可通过 FromPortfolioResult 转换
type PortfolioOptimizationResult struct {
	Weights            map[string]float64 `json:"weights"`
	ExpectedReturn     float64            `json:"expected_return"`
	ExpectedVolatility float64            `json:"expected_volatility"`
	SharpeRatio        float64            `json:"sharpe_ratio"`
	MaxDrawdownEst     float64            `json:"max_drawdown_estimate"`
	VaR95              float64            `json:"var_95"`
	CVaR95             float64            `json:"cvar_95"`
	Concentration      float64            `json:"concentration"`
	AlgorithmUsed      string             `json:"algorithm_used"`
	Strategy           string             `json:"strategy,omitempty"`
	Confidence         float64            `json:"confidence"`
	Error              string             `json:"error,omitempty"`
}

// PortfolioResultInput 组合优化结果转换输入（与宿主 portfolio.OptimizationResult 字段对齐，
// 定义在决策脑本地以避免反向依赖宿主包；宿主在装配侧自行转换为本类型后调用 FromPortfolioResult）。
type PortfolioResultInput struct {
	Weights            map[string]float64
	ExpectedReturn     float64
	ExpectedVolatility float64
	SharpeRatio        float64
	Strategy           string
}

// FromPortfolioResult 将组合优化结果转换为统一的 PortfolioOptimizationResult
// 补充基于波动率的风险估算字段（MaxDrawdownEst/VaR95/CVaR95）与基于权重分散度的置信度
func FromPortfolioResult(r PortfolioResultInput) PortfolioOptimizationResult {
	res := PortfolioOptimizationResult{
		Weights:            r.Weights,
		ExpectedReturn:     r.ExpectedReturn,
		ExpectedVolatility: r.ExpectedVolatility,
		SharpeRatio:        r.SharpeRatio,
		AlgorithmUsed:      r.Strategy,
		Strategy:           r.Strategy,
	}
	res.MaxDrawdownEst = res.ExpectedVolatility * 2.0
	res.VaR95 = res.ExpectedVolatility * 1.65
	res.CVaR95 = res.ExpectedVolatility * 2.0
	res.Concentration = computeHHI(res.Weights)
	res.Confidence = computeConfidence(res.Weights, len(res.Weights))
	return res
}

// computeConfidence 基于权重分散度与资产数量计算结果置信度（替代硬编码）
// 权重越分散（HHI 越低）、资产数量越多，优化结果越稳健，置信度越高
func computeConfidence(weights map[string]float64, n int) float64 {
	if n <= 1 {
		return 0.3
	}
	hhi := computeHHI(weights)
	// HHI 范围 [1/n, 1]，归一化到 [0,1]：1 表示完全集中，0 表示完全分散
	concentration := (hhi - 1.0/float64(n)) / (1.0 - 1.0/float64(n))
	if concentration < 0 {
		concentration = 0
	}
	if concentration > 1 {
		concentration = 1
	}
	// 资产数量因子：资产越多置信度越高（1个资产0.5，10个资产0.9）
	assetFactor := 0.5 + 0.4*(1.0-1.0/float64(n))
	conf := assetFactor * (1.0 - 0.5*concentration)
	if conf > 0.95 {
		conf = 0.95
	}
	if conf < 0.3 {
		conf = 0.3
	}
	return conf
}

// OptimizeMeanVariance 均值-方差优化
// 目标: max_w w'μ - λ * w'Σw
func (pe *PortfolioEngine) OptimizeMeanVariance(
	expectedReturns map[string]float64,
	covarianceMatrix [][]float64,
	riskAversion float64,
	constraints OptimizationConstraints,
) PortfolioOptimizationResult {
	result := PortfolioOptimizationResult{
		AlgorithmUsed: "Mean-Variance",
	}

	symbols := make([]string, 0, len(expectedReturns))
	for s := range expectedReturns {
		symbols = append(symbols, s)
	}
	sort.Strings(symbols)

	n := len(symbols)
	if n == 0 {
		return result
	}

	// 均值-方差优化：带回溯线搜索的投影梯度上升
	// 目标函数 f(w) = w'μ - λ * w'Σw，梯度 ∇f = μ - 2λΣw
	weights := make(map[string]float64, n)
	equalWeight := 1.0 / float64(n)

	// 初始权重均匀分配
	for _, s := range symbols {
		weights[s] = equalWeight
	}

	// 目标函数值
	objectiveValue := func(w map[string]float64) float64 {
		ret := computeExpectedReturn(w, expectedReturns)
		vol := computePortfolioVolatility(w, symbols, covarianceMatrix)
		return ret - riskAversion*vol*vol
	}

	iterations := 200
	stepSize := 0.1
	curObj := objectiveValue(weights)

	for iter := 0; iter < iterations; iter++ {
		// 计算完整梯度: ∇f = μ - 2λΣw（一次性计算 Σw，避免重复矩阵乘法）
		sigmaW := make([]float64, n)
		for i := 0; i < n; i++ {
			sum := 0.0
			for j := 0; j < n; j++ {
				sum += covarianceMatrix[i][j] * weights[symbols[j]]
			}
			sigmaW[i] = sum
		}
		grad := make(map[string]float64, n)
		for i, s := range symbols {
			grad[s] = expectedReturns[s] - 2*riskAversion*sigmaW[i]
		}

		// 回溯线搜索：从 stepSize 开始，目标不增则减半
		alpha := stepSize
		improved := false
		for alpha > 1e-8 {
			candidate := make(map[string]float64, n)
			for _, s := range symbols {
				candidate[s] = weights[s] + alpha*grad[s]
				if candidate[s] < 0 {
					candidate[s] = 0
				}
			}
			candidate = normalizeWeights(candidate)
			candidate = applyConstraints(candidate, symbols, constraints)

			newObj := objectiveValue(candidate)
			if newObj > curObj+1e-12 {
				weights = candidate
				curObj = newObj
				improved = true
				break
			}
			alpha *= 0.5
		}

		// 无法改进则收敛
		if !improved {
			break
		}
	}

	result.Weights = weights
	result.ExpectedReturn = computeExpectedReturn(weights, expectedReturns)
	result.ExpectedVolatility = computePortfolioVolatility(weights, symbols, covarianceMatrix)
	result.SharpeRatio = 0
	if result.ExpectedVolatility > 1e-8 {
		result.SharpeRatio = result.ExpectedReturn / result.ExpectedVolatility
	}
	result.MaxDrawdownEst = result.ExpectedVolatility * 2.0
	result.VaR95 = result.ExpectedVolatility * 1.65
	result.CVaR95 = result.ExpectedVolatility * 2.0
	result.Concentration = computeHHI(weights)
	result.Confidence = computeConfidence(weights, n)

	return result
}

// OptimizeRiskParity 风险平价优化
// 目标: RC_i = w_i * (∂σ_p / ∂w_i)  所有资产风险贡献相等
func (pe *PortfolioEngine) OptimizeRiskParity(
	symbols []string,
	covarianceMatrix [][]float64,
	constraints OptimizationConstraints,
) PortfolioOptimizationResult {
	result := PortfolioOptimizationResult{
		AlgorithmUsed: "Risk Parity",
	}

	n := len(symbols)
	if n == 0 {
		return result
	}

	// 初始权重
	weights := make(map[string]float64, n)
	for _, s := range symbols {
		weights[s] = 1.0 / float64(n)
	}

	// Spinu (2015) 定点迭代求解风险平价，与 portfolio/optimizer.go 保持一致
	// 更新式: w_i = σ_p² / (n * (Σw)_i)，其中 σ_p = sqrt(w'Σw)
	maxIter := 2000
	tol := 1e-10
	for iter := 0; iter < maxIter; iter++ {
		// 计算 Σw
		covW := make([]float64, n)
		for i := 0; i < n; i++ {
			sum := 0.0
			for j := 0; j < n; j++ {
				sum += covarianceMatrix[i][j] * weights[symbols[j]]
			}
			covW[i] = sum
		}

		// 组合方差与波动率
		variance := 0.0
		for i := 0; i < n; i++ {
			variance += weights[symbols[i]] * covW[i]
		}
		if variance < 0 {
			variance = 0
		}
		sigmaP := math.Sqrt(variance)
		if sigmaP < 1e-12 {
			break
		}

		targetRC := sigmaP / float64(n)

		// 计算风险贡献并检查收敛
		maxDiff := 0.0
		for i := 0; i < n; i++ {
			rc := weights[symbols[i]] * covW[i] / sigmaP
			diff := math.Abs(rc - targetRC)
			if diff > maxDiff {
				maxDiff = diff
			}
		}
		if maxDiff < tol {
			break
		}

		// Spinu 更新：w_i = σ_p² / (n * (Σw)_i)
		newW := make(map[string]float64, n)
		for i := 0; i < n; i++ {
			if covW[i] < 1e-15 {
				newW[symbols[i]] = weights[symbols[i]]
			} else {
				newW[symbols[i]] = sigmaP * sigmaP / (float64(n) * covW[i])
			}
		}

		sum := 0.0
		for _, s := range symbols {
			sum += newW[s]
		}
		if sum < 1e-12 {
			break
		}
		for _, s := range symbols {
			newW[s] /= sum
		}

		weights = newW
		weights = applyConstraints(weights, symbols, constraints)
	}

	result.Weights = weights
	result.ExpectedVolatility = computePortfolioVolatility(weights, symbols, covarianceMatrix)
	// 风险平价算法未输入预期收益，无法计算真实夏普比率，置0避免伪造
	result.SharpeRatio = 0
	result.MaxDrawdownEst = result.ExpectedVolatility * 2.5
	result.VaR95 = result.ExpectedVolatility * 1.65
	result.CVaR95 = result.ExpectedVolatility * 2.0
	result.Concentration = computeHHI(weights)
	result.Confidence = computeConfidence(weights, n)

	return result
}

// OptimizeHRP 层次风险平价 (Hierarchical Risk Parity)
func (pe *PortfolioEngine) OptimizeHRP(
	symbols []string,
	returns [][]float64,
	constraints OptimizationConstraints,
) PortfolioOptimizationResult {
	result := PortfolioOptimizationResult{
		AlgorithmUsed: "HRP",
	}

	n := len(symbols)
	if n == 0 {
		return result
	}

	// 计算相关矩阵
	corrMatrix := buildCorrelationMatrix(returns)

	// 标准层次聚类（平均连接）生成二叉聚类树
	tree := hierarchicalClusterTree(symbols, corrMatrix)

	// 递归二分分配权重：簇间按等权组合方差逆比分配（naive risk parity）
	weights := make(map[string]float64, n)
	hrpAllocate(tree, symbols, returns, weights, 1.0)

	weights = normalizeWeights(weights)
	weights = applyConstraints(weights, symbols, constraints)

	result.Weights = weights
	result.ExpectedVolatility = estimatePortfolioVol(weights, returns)
	result.SharpeRatio = computeSharpeFromReturns(weights, symbols, returns)
	result.MaxDrawdownEst = result.ExpectedVolatility * 2.0
	result.VaR95 = result.ExpectedVolatility * 1.65
	result.CVaR95 = result.ExpectedVolatility * 2.0
	result.Concentration = computeHHI(weights)
	result.Confidence = computeConfidence(weights, n)

	return result
}

// OptimizeBlackLitterman Black-Litterman 模型
// 先验收益 π 基于历史收益均值（年化）计算，缺少历史收益时返回错误而非伪造先验
func (pe *PortfolioEngine) OptimizeBlackLitterman(
	symbols []string,
	views map[string]float64,
	confidences map[string]float64,
	covarianceMatrix [][]float64,
	historicalReturns map[string][]float64,
	constraints OptimizationConstraints,
) PortfolioOptimizationResult {
	result := PortfolioOptimizationResult{
		AlgorithmUsed: "Black-Litterman",
	}

	n := len(symbols)
	if n == 0 {
		return result
	}

	// 先验收益：基于历史收益均值年化（市场均衡收益的近似）
	pi := make(map[string]float64, n)
	hasPrior := false
	for _, s := range symbols {
		series, ok := historicalReturns[s]
		if !ok || len(series) < 2 {
			pi[s] = 0
			continue
		}
		meanRet := 0.0
		for _, r := range series {
			meanRet += r
		}
		meanRet /= float64(len(series))
		pi[s] = meanRet * 252 // 日频收益年化
		hasPrior = true
	}
	if !hasPrior {
		result.Error = "缺少历史收益数据，无法计算 Black-Litterman 市场均衡先验收益"
		return result
	}

	// 融入投资者观点（tau 为标准标量 0.05）
	for symbol, view := range views {
		confidence := 0.5
		if c, ok := confidences[symbol]; ok {
			confidence = c
		}
		tau := 0.05
		pi[symbol] = pi[symbol] + tau*confidence*(view-pi[symbol])
	}

	// 基于调整后收益的均值-方差优化
	weights := make(map[string]float64, n)
	for _, s := range symbols {
		weights[s] = 1.0 / float64(n)
	}

	// 简单迭代
	for iter := 0; iter < 50; iter++ {
		for _, s := range symbols {
			weights[s] = weights[s] + pi[s]*0.01
			if weights[s] < 0 {
				weights[s] = 0
			}
		}
		weights = normalizeWeights(weights)
		weights = applyConstraints(weights, symbols, constraints)
	}

	result.Weights = weights

	// 计算预期收益
	expRet := 0.0
	for s, w := range weights {
		expRet += w * pi[s]
	}
	result.ExpectedReturn = expRet
	result.ExpectedVolatility = computePortfolioVolatility(weights, symbols, covarianceMatrix)
	result.SharpeRatio = 0
	if result.ExpectedVolatility > 1e-8 {
		result.SharpeRatio = result.ExpectedReturn / result.ExpectedVolatility
	}
	result.MaxDrawdownEst = result.ExpectedVolatility * 2.0
	result.VaR95 = result.ExpectedVolatility * 1.65
	result.CVaR95 = result.ExpectedVolatility * 2.0
	result.Concentration = computeHHI(weights)
	result.Confidence = computeConfidence(weights, n)

	return result
}

// OptimizationConstraints 优化约束
type OptimizationConstraints struct {
	MaxSingleWeight float64
	MaxSectorWeight map[string]float64
	MinCashWeight   float64
	MaxTurnover     float64
	TargetReturn    float64
	MaxVolatility   float64
	AllowShort      bool
}

// MultiObjectiveOptimize 多目标组合优化
// max_w [E(R) - λ1*Risk(w) - λ2*Turnover(w) - λ3*Concentration(w) + λ4*Alpha(w)]
func (pe *PortfolioEngine) MultiObjectiveOptimize(
	symbols []string,
	expectedReturns map[string]float64,
	alphas map[string]float64,
	covarianceMatrix [][]float64,
	lambdaRisk float64,
	lambdaTurnover float64,
	lambdaConcentration float64,
	lambdaAlpha float64,
	constraints OptimizationConstraints,
) PortfolioOptimizationResult {
	result := PortfolioOptimizationResult{
		AlgorithmUsed: "Multi-Objective",
	}

	n := len(symbols)
	if n == 0 {
		return result
	}

	// 初始权重
	weights := make(map[string]float64, n)
	for _, s := range symbols {
		weights[s] = 1.0 / float64(n)
	}

	// 多目标迭代优化
	iterations := 150
	for iter := 0; iter < iterations; iter++ {
		// 一次性计算 Σw（避免每次迭代对每只股票重复矩阵乘法）
		sigmaW := make([]float64, n)
		for i := 0; i < n; i++ {
			sum := 0.0
			for j := 0; j < n; j++ {
				sum += covarianceMatrix[i][j] * weights[symbols[j]]
			}
			sigmaW[i] = sum
		}

		for i, s := range symbols {
			// 收益项
			retComponent := 0.0
			if r, ok := expectedReturns[s]; ok {
				retComponent = r
			}

			// 风险项（= (Σw)_i，即协方差贡献）
			riskComponent := sigmaW[i]

			// 换手惩罚
			turnoverComponent := weights[s] * 0.5

			// 集中度惩罚
			concentrationComponent := weights[s] * weights[s]

			// Alpha 奖励
			alphaComponent := 0.0
			if a, ok := alphas[s]; ok {
				alphaComponent = a
			}

			// 综合梯度
			gradient := retComponent -
				lambdaRisk*riskComponent -
				lambdaTurnover*turnoverComponent -
				lambdaConcentration*concentrationComponent +
				lambdaAlpha*alphaComponent

			stepSize := 0.005 * (1.0 - float64(iter)/float64(iterations))
			weights[s] += stepSize * gradient
			if weights[s] < 0 {
				weights[s] = 0
			}
		}

		weights = normalizeWeights(weights)
		weights = applyConstraints(weights, symbols, constraints)
	}

	result.Weights = weights
	result.ExpectedReturn = computeExpectedReturn(weights, expectedReturns)
	result.ExpectedVolatility = computePortfolioVolatility(weights, symbols, covarianceMatrix)
	result.SharpeRatio = 0
	if result.ExpectedVolatility > 1e-8 {
		result.SharpeRatio = result.ExpectedReturn / result.ExpectedVolatility
	}
	result.MaxDrawdownEst = result.ExpectedVolatility * 2.0
	result.VaR95 = result.ExpectedVolatility * 1.65
	result.CVaR95 = result.ExpectedVolatility * 2.0
	result.Concentration = computeHHI(weights)
	result.Confidence = computeConfidence(weights, n)

	return result
}

// ---------- 辅助函数 ----------

func normalizeWeights(weights map[string]float64) map[string]float64 {
	total := 0.0
	for _, w := range weights {
		total += math.Max(w, 0)
	}
	if total < 1e-8 {
		n := len(weights)
		if n > 0 {
			equal := 1.0 / float64(n)
			for k := range weights {
				weights[k] = equal
			}
		}
		return weights
	}

	for k, w := range weights {
		weights[k] = math.Max(w, 0) / total
	}
	return weights
}

func applyConstraints(weights map[string]float64, symbols []string, constraints OptimizationConstraints) map[string]float64 {
	if constraints.MaxSingleWeight > 0 {
		for s, w := range weights {
			if w > constraints.MaxSingleWeight {
				weights[s] = constraints.MaxSingleWeight
			}
		}
	}

	if !constraints.AllowShort {
		for s := range weights {
			if weights[s] < 0 {
				weights[s] = 0
			}
		}
	}

	return normalizeWeights(weights)
}

func computeExpectedReturn(weights map[string]float64, expectedReturns map[string]float64) float64 {
	ret := 0.0
	for s, w := range weights {
		if r, ok := expectedReturns[s]; ok {
			ret += w * r
		}
	}
	return ret
}

func computePortfolioVolatility(weights map[string]float64, symbols []string, covarianceMatrix [][]float64) float64 {
	n := len(symbols)
	if n == 0 || len(covarianceMatrix) < n {
		return 0
	}

	variance := 0.0
	for i := 0; i < n; i++ {
		for j := 0; j < n; j++ {
			if i < len(covarianceMatrix) && j < len(covarianceMatrix[i]) {
				wi := weights[symbols[i]]
				wj := weights[symbols[j]]
				variance += wi * wj * covarianceMatrix[i][j]
			}
		}
	}

	if variance < 0 {
		variance = 0
	}
	return math.Sqrt(variance)
}

func computeVarianceContribution(symbol string, symbols []string, weights map[string]float64,
	expectedReturns map[string]float64, covarianceMatrix [][]float64) float64 {
	idx := -1
	for i, s := range symbols {
		if s == symbol {
			idx = i
			break
		}
	}
	if idx < 0 || idx >= len(covarianceMatrix) {
		return 0
	}

	contribution := 0.0
	for j, s2 := range symbols {
		if j < len(covarianceMatrix[idx]) {
			contribution += weights[s2] * covarianceMatrix[idx][j]
		}
	}
	return contribution
}

func buildCorrelationMatrix(returns [][]float64) [][]float64 {
	n := len(returns)
	matrix := make([][]float64, n)
	for i := range matrix {
		matrix[i] = make([]float64, n)
		for j := 0; j <= i; j++ {
			if i == j {
				matrix[i][j] = 1.0
			} else {
				corr := computeCorrelation(returns[i], returns[j])
				matrix[i][j] = corr
				matrix[j][i] = corr
			}
		}
	}
	return matrix
}

// clusterNode 层次聚类二叉树的节点
type clusterNode struct {
	indices []int
	left    *clusterNode
	right   *clusterNode
}

// hierarchicalClusterTree 平均连接（average-linkage）层次聚类
// 距离 = 1 - |相关系数|，自底向上合并距离最近的簇，返回二叉聚类树
func hierarchicalClusterTree(symbols []string, corrMatrix [][]float64) *clusterNode {
	n := len(symbols)
	if n == 0 {
		return nil
	}

	// 簇间距离矩阵（平均连接）
	dist := make([][]float64, n)
	for i := range dist {
		dist[i] = make([]float64, n)
		for j := range dist[i] {
			if i == j {
				dist[i][j] = 0
				continue
			}
			corr := 0.0
			if i < len(corrMatrix) && j < len(corrMatrix[i]) {
				corr = corrMatrix[i][j]
			}
			d := 1.0 - math.Abs(corr)
			if d < 0 {
				d = 0
			}
			dist[i][j] = d
		}
	}

	clusters := make([]*clusterNode, n)
	for i := range clusters {
		clusters[i] = &clusterNode{indices: []int{i}}
	}
	active := make([]bool, n)
	for i := range active {
		active[i] = true
	}

	remaining := n
	for remaining > 1 {
		// 找距离最小的簇对
		minD := math.Inf(1)
		mi, mj := -1, -1
		for i := 0; i < n; i++ {
			if !active[i] {
				continue
			}
			for j := i + 1; j < n; j++ {
				if !active[j] {
					continue
				}
				if dist[i][j] < minD {
					minD = dist[i][j]
					mi, mj = i, j
				}
			}
		}
		if mi < 0 {
			break
		}

		// 合并 mi 与 mj
		merged := &clusterNode{
			indices: append(append([]int{}, clusters[mi].indices...), clusters[mj].indices...),
			left:    clusters[mi],
			right:   clusters[mj],
		}

		// 平均连接更新距离：新簇与 k 的距离 = 两簇距离的加权平均
		mergedSize := float64(len(merged.indices))
		miSize := float64(len(clusters[mi].indices))
		mjSize := float64(len(clusters[mj].indices))
		for k := 0; k < n; k++ {
			if !active[k] || k == mi || k == mj {
				continue
			}
			dist[mi][k] = (dist[mi][k]*miSize + dist[mj][k]*mjSize) / mergedSize
			dist[k][mi] = dist[mi][k]
		}

		clusters[mi] = merged
		active[mj] = false
		remaining--
	}

	for i := 0; i < n; i++ {
		if active[i] {
			return clusters[i]
		}
	}
	return nil
}

// hrpAllocate 递归二分分配权重：簇间按等权组合方差逆比分配（naive risk parity）
func hrpAllocate(node *clusterNode, symbols []string, returns [][]float64, weights map[string]float64, weight float64) {
	if node == nil {
		return
	}
	if node.left == nil && node.right == nil {
		if len(node.indices) == 1 {
			weights[symbols[node.indices[0]]] = weight
		}
		return
	}

	leftVar := clusterReturnVariance(node.left.indices, returns)
	rightVar := clusterReturnVariance(node.right.indices, returns)

	leftW, rightW := weight*0.5, weight*0.5
	invLeft, invRight := 0.0, 0.0
	if leftVar > 1e-12 {
		invLeft = 1.0 / leftVar
	}
	if rightVar > 1e-12 {
		invRight = 1.0 / rightVar
	}
	if invLeft+invRight > 1e-12 {
		leftW = weight * invLeft / (invLeft + invRight)
		rightW = weight * invRight / (invLeft + invRight)
	}

	hrpAllocate(node.left, symbols, returns, weights, leftW)
	hrpAllocate(node.right, symbols, returns, weights, rightW)
}

// clusterReturnVariance 计算簇内等权组合收益序列的方差
func clusterReturnVariance(indices []int, returns [][]float64) float64 {
	if len(indices) == 0 || len(returns) == 0 {
		return 0
	}
	minLen := len(returns[0])
	for _, idx := range indices {
		if idx < len(returns) && len(returns[idx]) < minLen {
			minLen = len(returns[idx])
		}
	}
	if minLen < 2 {
		return 0
	}

	series := make([]float64, 0, minLen)
	for t := 0; t < minLen; t++ {
		sum := 0.0
		for _, idx := range indices {
			if idx < len(returns) && t < len(returns[idx]) {
				sum += returns[idx][t]
			}
		}
		series = append(series, sum/float64(len(indices)))
	}

	m := mean(series)
	variance := 0.0
	for _, r := range series {
		diff := r - m
		variance += diff * diff
	}
	return variance / float64(len(series)-1)
}

func estimatePortfolioVol(weights map[string]float64, returns [][]float64) float64 {
	if len(returns) == 0 {
		return 0
	}

	// 用时间序列直接计算
	var portfolioReturns []float64
	minLen := len(returns[0])
	for _, r := range returns {
		if len(r) < minLen {
			minLen = len(r)
		}
	}

	for t := 0; t < minLen; t++ {
		portRet := 0.0
		for i, r := range returns {
			if t < len(r) {
				idx := 0
				for sym := range weights {
					if idx == i {
						if w, ok := weights[sym]; ok {
							portRet += w * r[t]
						}
						break
					}
					idx++
				}
			}
		}
		portfolioReturns = append(portfolioReturns, portRet)
	}

	if len(portfolioReturns) < 2 {
		return 0
	}

	m := mean(portfolioReturns)
	variance := 0.0
	for _, r := range portfolioReturns {
		diff := r - m
		variance += diff * diff
	}
	std := math.Sqrt(variance / float64(len(portfolioReturns)-1))
	return std * math.Sqrt(252)
}

// portfolioReturnSeries 按 symbols 顺序计算组合收益序列（避免 map 迭代顺序问题）
func portfolioReturnSeries(weights map[string]float64, symbols []string, returns [][]float64) []float64 {
	if len(symbols) == 0 || len(returns) == 0 {
		return nil
	}
	minLen := len(returns[0])
	for _, r := range returns {
		if len(r) < minLen {
			minLen = len(r)
		}
	}
	if minLen < 2 {
		return nil
	}
	series := make([]float64, 0, minLen)
	for t := 0; t < minLen; t++ {
		ret := 0.0
		for i, sym := range symbols {
			if i < len(returns) && t < len(returns[i]) {
				if w, ok := weights[sym]; ok {
					ret += w * returns[i][t]
				}
			}
		}
		series = append(series, ret)
	}
	return series
}

// computeSharpeFromReturns 基于组合收益序列计算年化夏普比率（无风险利率=0）
func computeSharpeFromReturns(weights map[string]float64, symbols []string, returns [][]float64) float64 {
	series := portfolioReturnSeries(weights, symbols, returns)
	if len(series) < 2 {
		return 0
	}
	m := mean(series)
	variance := 0.0
	for _, r := range series {
		diff := r - m
		variance += diff * diff
	}
	std := math.Sqrt(variance / float64(len(series)-1))
	if std < 1e-8 {
		return 0
	}
	return m / std * math.Sqrt(252)
}
