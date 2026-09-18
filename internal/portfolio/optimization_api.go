package portfolio

// 面向 Agent/CIO 的组合优化上层 API。
// 封装 Optimize，补充：
//   - 行业级约束（行业敞口上限、同行业最多持仓数）的后置矫正
//   - 多策略自动选优（SelectBestStrategy：在 mvo / risk_parity / risk_budget 中按夏普取优）
//   - 等权兜底（OptimizeEqualWeight）
//
// OptimizationResult 与 intelligence.PortfolioOptimizationResult 对齐（见 optimizer.go）。

// OptimizationInput 优化输入。所有映射均以代码为键。
type OptimizationInput struct {
	Assets          []string           // 参与优化的资产代码
	ExpectedReturns map[string]float64 // 代码 -> 预期收益（0-1 小数）
	CovMatrix       [][]float64        // 与 Assets 顺序一致的协方差矩阵（日收益口径）
	CurrentWeights  map[string]float64 // 当前权重（可选，保留接口）
	Constraints     OptimizationConstraints
}

// OptimizationConstraints 优化约束
type OptimizationConstraints struct {
	MaxWeight            float64           // 单券上限（比例）
	MinWeight            float64           // 单券下限（比例）
	RiskFreeRate         float64           // 无风险利率（年化）
	IndustryOf           map[string]string // 代码 -> 行业
	MaxIndustryWeight    float64           // 单行业敞口上限（比例）
	MaxStocksPerIndustry int               // 单行业最多持仓数量
}

// OptimizerStrategy 优化器选用的策略标识。
type OptimizerStrategy string

const (
	StrEqualWeight OptimizerStrategy = "equal_weight"
	StrMVO         OptimizerStrategy = "mvo"
	StrRiskParity  OptimizerStrategy = "risk_parity"
	StrRiskBudget  OptimizerStrategy = "risk_budget"
)

// OptimizeEqualWeight 等权兜底：满足单券与行业约束的最小方差化等权方案（不依赖预期收益）。
func OptimizeEqualWeight(input OptimizationInput) OptimizationResult {
	res := optimizeWithIndustry(input, MethodRiskParity, true)
	return res
}

// SelectBestStrategy 在多种策略上运行优化，按夏普比率（风险调整后收益）自动选优。
// 返回最优结果与其策略标识。数据来自真实协方差与预期收益，无硬编码。
func SelectBestStrategy(input OptimizationInput) (OptimizationResult, OptimizerStrategy) {
	n := len(input.Assets)
	if n == 0 {
		return OptimizationResult{Weights: map[string]float64{}}, StrEqualWeight
	}
	cands := []struct {
		m OptimizeMethod
		s OptimizerStrategy
	}{
		{MethodMVO, StrMVO},
		{MethodRiskParity, StrRiskParity},
		{MethodRiskBudget, StrRiskBudget},
	}
	bestRes := optimizeWithIndustry(input, MethodRiskParity, false)
	bestStrategy := StrRiskParity
	for _, c := range cands {
		r := optimizeWithIndustry(input, c.m, false)
		if r.SharpeRatio > bestRes.SharpeRatio {
			bestRes = r
			bestStrategy = c.s
		}
	}
	return bestRes, bestStrategy
}

// optimizeWithIndustry 运行优化并施加行业级约束。equal 为 true 时采用等权起点（兜底）。
func optimizeWithIndustry(input OptimizationInput, method OptimizeMethod, equal bool) OptimizationResult {
	empty := OptimizationResult{Weights: map[string]float64{}}
	n := len(input.Assets)
	if n == 0 || len(input.CovMatrix) != n {
		return empty
	}

	totalW := 1.0
	maxSingle := input.Constraints.MaxWeight
	if maxSingle <= 0 {
		maxSingle = 0.25
	}
	minW := input.Constraints.MinWeight
	if minW < 0 {
		minW = 0
	}
	maxInd := input.Constraints.MaxIndustryWeight
	if maxInd <= 0 {
		maxInd = 0.35
	}
	maxPerInd := input.Constraints.MaxStocksPerIndustry
	if maxPerInd <= 0 {
		maxPerInd = 3
	}

	rets := make([]float64, n)
	for i, code := range input.Assets {
		rets[i] = input.ExpectedReturns[code]
	}
	lo := make([]float64, n)
	hi := make([]float64, n)
	for i := 0; i < n; i++ {
		lo[i] = minW
		hi[i] = maxSingle
	}

	var weights []float64
	if equal {
		w := make([]float64, n)
		for i := range w {
			w[i] = 1.0 / float64(n)
		}
		weights = normalizeToTotal(projectBoxSum(w, lo, hi, totalW), totalW)
	} else {
		res, err := Optimize(OptimizeRequest{
			Method:       method,
			Cov:          input.CovMatrix,
			Returns:      rets,
			MinWeight:    lo,
			MaxWeight:    hi,
			TotalWeight:  totalW,
			RiskAversion: 4.0,
		})
		if err != nil || res == nil {
			return empty
		}
		weights = res.Weights
	}

	// 行业约束矫正
	weights = applyIndustryCaps(weights, input.Assets, input.Constraints.IndustryOf, maxInd, maxPerInd)
	weights = normalizeToTotal(weights, totalW)

	// 风险子组合（剔现金）
	raw := normalizeToTotal(weights, 1.0)
	cov := RegularizeCov(input.CovMatrix)
	vol := sqrtPortfolioVariance(cov, raw) * sqDay
	annualRet := 0.0
	for i, code := range input.Assets {
		annualRet += raw[i] * input.ExpectedReturns[code]
	}
	sharpe := calcSharpe(annualRet, vol, input.Constraints.RiskFreeRate)

	wmap := make(map[string]float64, n)
	for i, code := range input.Assets {
		if absf(raw[i]) < 1e-6 {
			continue
		}
		wmap[code] = raw[i]
	}
	return OptimizationResult{
		Weights:            wmap,
		ExpectedReturn:     annualRet,
		ExpectedVolatility: vol,
		SharpeRatio:        sharpe,
		Strategy:           OptimizeMethod(method),
	}
}

// applyIndustryCaps 行业级矫正：单行业敞口上限 + 单行业最多持仓数量。
func applyIndustryCaps(weights []float64, codes []string, ind map[string]string, maxInd float64, maxPerInd int) []float64 {
	n := len(weights)
	if n == 0 {
		return weights
	}
	// 1) 单行业最多持仓数：保留该行业权重 Top-N，其余置 0
	if maxPerInd > 0 {
		byInd := map[string][]int{}
		for i, code := range codes {
			indName := ind[code]
			if indName == "" {
				continue
			}
			byInd[indName] = append(byInd[indName], i)
		}
		for _, idxs := range byInd {
			if len(idxs) <= maxPerInd {
				continue
			}
			// 按权重降序保留前 maxPerInd
			sorted := make([]int, len(idxs))
			copy(sorted, idxs)
			sortIndicesDesc(sorted, func(i int) float64 { return weights[i] })
			keep := map[int]bool{}
			for k := 0; k < maxPerInd; k++ {
				keep[sorted[k]] = true
			}
			for _, i := range idxs {
				if !keep[i] {
					weights[i] = 0
				}
			}
		}
	}
	// 2) 单行业敞口上限：超出部分按比例缩放该行业全部持仓
	if maxInd > 0 {
		indSum := map[string]float64{}
		for i, code := range codes {
			if indName := ind[code]; indName != "" {
				indSum[indName] += weights[i]
			}
		}
		for indName, s := range indSum {
			if s <= maxInd {
				continue
			}
			scale := maxInd / s
			for i, code := range codes {
				if ind[code] == indName {
					weights[i] *= scale
				}
			}
		}
	}
	return weights
}

func sortIndicesDesc(idxs []int, val func(int) float64) {
	for i := 1; i < len(idxs); i++ {
		for j := i; j > 0 && val(idxs[j]) > val(idxs[j-1]); j-- {
			idxs[j], idxs[j-1] = idxs[j-1], idxs[j]
		}
	}
}

func calcSharpe(r, v, rf float64) float64 {
	if v > 1e-12 {
		return (r - rf) / v
	}
	return 0
}

func absf(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
