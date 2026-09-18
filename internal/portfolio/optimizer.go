package portfolio

// 投资组合优化引擎（纯 Go 数学，无外部依赖）
//
// 提供三种可切换的权重算法：
//   - 均值-方差 MVO（Markowitz）：最大化 Sharpe，收益导向，对预期收益估计敏感
//   - 风险平价 Risk Parity：令各资产对组合波动的风险贡献相等，天然去等权/去集中，最稳健
//   - 风险预算 Risk Budget：按给定风险预算解权重，兼顾可控性
//
// 输入为资产收益率协方差矩阵（由调用方基于真实日K构造），算法在全局交互式
// 约束（Σw = TotalWeight、每资产上下界）下数值求解，确定性输出。

import "math"

// OptimizeMethod 优化算法标识
type OptimizeMethod string

const (
	MethodMVO        OptimizeMethod = "mvo"
	MethodRiskParity OptimizeMethod = "risk_parity"
	MethodRiskBudget OptimizeMethod = "risk_budget"
)

// OptimizationResult 旧版 intelligence.PortfolioEngine 的转换目标类型（按代码映射权重）。
// 与 intelligence.PortfolioOptimizationResult 字段对齐，供 FromPortfolioResult 使用。
type OptimizationResult struct {
	Weights            map[string]float64 `json:"weights"`
	ExpectedReturn     float64            `json:"expected_return"`
	ExpectedVolatility float64            `json:"expected_volatility"`
	SharpeRatio        float64            `json:"sharpe_ratio"`
	Strategy           OptimizeMethod     `json:"strategy"`
}

// ToMapResult 将本包 OptResult 转换为 OptimizeResult（按资产代码映射权重）。
func ToMapResult(res *OptimizeResult, codes []string) OptimizationResult {
	w := make(map[string]float64, len(codes))
	for i, c := range codes {
		if i < len(res.Weights) {
			w[c] = res.Weights[i]
		}
	}
	return OptimizationResult{
		Weights:            w,
		ExpectedReturn:     res.AnnualReturn,
		ExpectedVolatility: res.Volatility,
		SharpeRatio:        res.Sharpe,
		Strategy:           res.Method,
	}
}

const annualFactor = 252.0 // A股自然交易日年化系数

var sqDay = math.Sqrt(annualFactor)

// OptimizeRequest 优化请求。所有数组均与资产列表对齐。
type OptimizeRequest struct {
	Method       OptimizeMethod
	Cov          [][]float64 // n×n 日收益率协方差矩阵（0-1 小数）
	Returns      []float64   // n 个资产的日均收益率（0-1 小数），仅 MVO 使用；为空时退化为最小方差
	MinWeight    []float64   // 每资产生仓位下限（占总投资组合 0-1）
	MaxWeight    []float64   // 每资产生位上：上限
	TotalWeight  float64     // 分配给风险资产的合计比重（剩余为现金）。默认 0.9
	Budgets      []float64   // 风险预算（risk_budget 专用），长度 n 且和为 1
	RiskAversion float64     // MVO 风险厌恶系数 λ，默认 4.0
	MaxIter      int
	Epsilon      float64
}

// OptimizeResult 优化结果
type OptimizeResult struct {
	Method           OptimizeMethod `json:"method"`
	Weights          []float64      `json:"weights"`      // 每资产目标权重（占总投资组合，和=TotalWeight）
	RawWeights       []float64      `json:"rawWeights"`   // 风险子组合内归一化权重（和为1）
	Volatility       float64        `json:"volatility"`   // 年化波动率（0-1）
	AnnualReturn     float64        `json:"annualReturn"` // 年化预期收益（0-1）
	Sharpe           float64        `json:"sharpe"`
	RiskContribution []float64      `json:"riskContribution"` // 各资产年化风险贡献（0-1）
	RiskCtrPercent   []float64      `json:"riskCtrPercent"`   // 各资产风险贡献占比（和≈1）
	N                int            `json:"n"`
}

// RegularizeCov 对协方差矩阵做岭稳定化，保证数值可逆/正定。
func RegularizeCov(cov [][]float64) [][]float64 {
	n := len(cov)
	if n == 0 {
		return cov
	}
	// 以对角线均值×1e-4 作为岭参数，足够让近奇异矩阵稳定
	trace := 0.0
	for i := 0; i < n; i++ {
		if i < len(cov[i]) {
			trace += cov[i][i]
		}
	}
	ridge := (trace / float64(n)) * 1e-4
	if ridge <= 0 {
		ridge = 1e-6
	}
	out := make([][]float64, n)
	for i := 0; i < n; i++ {
		out[i] = make([]float64, n)
		for j := 0; j < n && j < len(cov[i]); j++ {
			out[i][j] = cov[i][j]
		}
		out[i][i] += ridge
	}
	return out
}

// EstimatePrices 依据 n 个资产的日收益时间序列构造样本协方差（列=资产，行=时间）。
func EstimateCovariance(returns [][]float64) [][]float64 {
	n := len(returns)
	if n == 0 {
		return nil
	}
	t := len(returns[0])
	means := make([]float64, n)
	for i := 0; i < n; i++ {
		s := 0.0
		cnt := 0
		for _, v := range returns[i] {
			s += v
			cnt++
		}
		if cnt == 0 {
			continue
		}
		means[i] = s / float64(cnt)
	}
	cov := make([][]float64, n)
	for i := 0; i < n; i++ {
		cov[i] = make([]float64, n)
		for j := 0; j < n; j++ {
			s := 0.0
			m := t
			if len(returns[j]) < m {
				m = len(returns[j])
			}
			if len(returns[i]) < m {
				m = len(returns[i])
			}
			if m == 0 {
				continue
			}
			for k := 0; k < m; k++ {
				di := 0.0
				if k < len(returns[i]) {
					di = returns[i][k]
				}
				dj := 0.0
				if k < len(returns[j]) {
					dj = returns[j][k]
				}
				s += (di - means[i]) * (dj - means[j])
			}
			cov[i][j] = s / float64(m-1)
		}
	}
	return RegularizeCov(cov)
}

// Optimize 执行权重优化，返回归一化结果。
func Optimize(req OptimizeRequest) (*OptimizeResult, error) {
	n := len(req.Cov)
	if n == 0 {
		return nil, nil
	}
	cov := RegularizeCov(req.Cov)

	method := req.Method
	if method == "" {
		method = MethodRiskParity
	}
	totalW := req.TotalWeight
	if totalW <= 0 || totalW > 1 {
		totalW = 0.9
	}
	maxIter := req.MaxIter
	if maxIter <= 0 {
		maxIter = 4000
	}
	eps := req.Epsilon
	if eps <= 0 {
		eps = 1e-9
	}

	// 每资产上下界（占投资组合比例），默认 [0.0, TotalWeight/多少？]。用宽限制避免无解。
	lo := make([]float64, n)
	hi := make([]float64, n)
	for i := 0; i < n; i++ {
		if i < len(req.MinWeight) && req.MinWeight[i] >= 0 {
			lo[i] = req.MinWeight[i]
		} else {
			lo[i] = 0
		}
		if i < len(req.MaxWeight) && req.MaxWeight[i] > 0 {
			hi[i] = req.MaxWeight[i]
		} else {
			hi[i] = totalW
		}
	}
	// 保证下界总和与上界可行
	sumLo, sumHi := 0.0, 0.0
	for i := 0; i < n; i++ {
		sumLo += lo[i]
		sumHi += hi[i]
	}
	if sumHi < totalW || sumLo > totalW {
		// 约束无解时放宽到等权可行解
		for i := 0; i < n; i++ {
			lo[i] = 0
			hi[i] = totalW
		}
	}

	returns := make([]float64, n)
	if len(req.Returns) >= n {
		copy(returns, req.Returns[:n])
	}
	lambda := req.RiskAversion
	if lambda <= 0 {
		lambda = 4.0
	}

	var (
		w         []float64
		annualRet float64
		annualVol float64
		riskCtr   []float64
	)
	switch method {
	case MethodMVO:
		w = solveMVO(cov, returns, lo, hi, totalW, lambda, float64(maxIter), eps)
	case MethodRiskBudget:
		w = solveRiskBudget(cov, req.Budgets, lo, hi, totalW, float64(maxIter), eps)
	default: // risk_parity
		w = solveRiskParity(cov, lo, hi, totalW, float64(maxIter), eps)
		// 兜底：若平行解退化（某些到0导致风险贡献无法均衡），回退 mini-var
	}

	w = normalizeToTotal(w, totalW)
	// 风险子组合内归一化（去现金）
	raw := normalizeToTotal(w, 1.0)

	annualVol = sqrtPortfolioVariance(cov, w) * sqDay
	if method == MethodMVO {
		r := 0.0
		for i := 0; i < n; i++ {
			r += w[i] * returns[i]
		}
		annualRet = r * annualFactor
	}
	rf := 0.02 // 无风险年化约2%
	sharpe := 0.0
	if annualVol > 1e-12 {
		sharpe = (annualRet - rf) / annualVol
	}
	riskCtr = dollarRiskContribution(cov, w)
	// 年化风险贡献 = 日度协方差贡献 × 252（日收益方差→年化）
	for i := range riskCtr {
		riskCtr[i] *= annualFactor
	}
	rp := make([]float64, n)
	vol2 := sqrtPortfolioVariance(cov, w) * sqrtPortfolioVariance(cov, w) // w'Σw（日度）
	if vol2 > 1e-16 {
		for i := 0; i < n; i++ {
			rp[i] = riskCtr[i] / (vol2 * annualFactor)
		}
	}

	return &OptimizeResult{
		Method:           method,
		Weights:          w,
		RawWeights:       raw,
		Volatility:       annualVol,
		AnnualReturn:     annualRet,
		Sharpe:           sharpe,
		RiskContribution: riskCtr,
		RiskCtrPercent:   rp,
		N:                n,
	}, nil
}

// ---------- 数值求解核心 ----------

// projectBoxSum 将 x 投影到 {lo ≤ w ≤ hi, Σw = T}。
// 通过二分 λ 使 Σ clamp(x_i + λ) = T（clamp 后随 λ 单调）。
func projectBoxSum(x, lo, hi []float64, T float64) []float64 {
	n := len(x)
	sumClamp := func(l float64) float64 {
		s := 0.0
		for i := 0; i < n; i++ {
			v := x[i] + l
			if v < lo[i] {
				v = lo[i]
			} else if v > hi[i] {
				v = hi[i]
			}
			s += v
		}
		return s
	}
	loL, hiL := -1e8, 1e8
	// 找一个 λ 使 sum 跨过 T
	for it := 0; it < 300; it++ {
		mid := (loL + hiL) / 2
		s := sumClamp(mid)
		if math.Abs(s-T) < 1e-10 || hiL-loL < 1e-14 {
			loL, hiL = mid, mid
			break
		}
		if s > T {
			hiL = mid
		} else {
			loL = mid
		}
	}
	lam := (loL + hiL) / 2
	w := make([]float64, n)
	for i := 0; i < n; i++ {
		v := x[i] + lam
		if v < lo[i] {
			v = lo[i]
		} else if v > hi[i] {
			v = hi[i]
		}
		w[i] = v
	}
	return w
}

// projectGradient 投影梯度下降统一框架（minimize objective）。
// grad 返回当前 w 处的梯度。step0 为初始步长。
func projectedGradient(n int, lo, hi []float64, totalW, step0, maxIter float64,
	obj func([]float64) float64, grad func([]float64) []float64) []float64 {

	w := normalizeToTotal(make([]float64, n), 0)
	// 从等权可行性出发
	eqw := totalW / float64(n)
	for i := 0; i < n; i++ {
		v := eqw
		if v < lo[i] {
			v = lo[i]
		} else if v > hi[i] {
			v = hi[i]
		}
		w[i] = v
	}
	// 投影到和=totalW
	w = projectBoxSum(w, lo, hi, totalW)

	step := step0
	prev := obj(w)
	iters := int(maxIter)
	for it := 0; it < iters; it++ {
		g := grad(w)
		// 回溯线搜索
		accepted := false
		st := step
		for ls := 0; ls < 40; ls++ {
			cand := make([]float64, n)
			for i := 0; i < n; i++ {
				cand[i] = w[i] - st*g[i]
			}
			cand = projectBoxSum(cand, lo, hi, totalW)
			if candVal := obj(cand); candVal < prev-1e-16 {
				w = cand
				prev = candVal
				step = st * 1.2
				accepted = true
				break
			}
			st *= 0.7
		}
		if !accepted {
			break
		}
		if it%200 == 0 && math.Abs(step) > 1e12 {
			break
		}
	}
	return w
}

// solveMVO 最大化 w·μ − (λ/2) w'Σw，等价于最小化 −(w·μ − λ/2 w'Σw)。
func solveMVO(cov [][]float64, mu, lo, hi []float64, totalW, lambda, maxIter, eps float64) []float64 {
	n := len(cov)
	obj := func(w []float64) float64 {
		// Σw 缓冲
		sw := matVec(cov, w)
		v := 0.0
		r := 0.0
		for i := 0; i < n; i++ {
			v += w[i] * sw[i]
			r += w[i] * mu[i]
		}
		return -(r - 0.5*lambda*v)
	}
	grad := func(w []float64) []float64 {
		sw := matVec(cov, w)
		g := make([]float64, n)
		for i := 0; i < n; i++ {
			g[i] = -mu[i] + lambda*sw[i]
		}
		return g
	}
	return projectedGradient(n, lo, hi, totalW, 1e-2*lambda*0.1+1e-3, maxIter, obj, grad)
}

// solveRiskParity 最小化 Σ_{i<j}(r_i−r_j)^2，r_i=w_i(Σw)_i。
func solveRiskParity(cov [][]float64, lo, hi []float64, totalW, maxIter, eps float64) []float64 {
	n := len(cov)
	obj := func(w []float64) float64 {
		rc := dollarRiskContribution(cov, w)
		f := 0.0
		for i := 0; i < n; i++ {
			for j := i + 1; j < n; j++ {
				d := rc[i] - rc[j]
				f += d * d
			}
		}
		return f
	}
	grad := func(w []float64) []float64 {
		sw := matVec(cov, w)
		rc := make([]float64, n)
		for i := range rc {
			rc[i] = w[i] * sw[i]
		}
		g := make([]float64, n)
		for k := 0; k < n; k++ {
			s := 0.0
			for i := 0; i < n; i++ {
				for j := i + 1; j < n; j++ {
					d := rc[i] - rc[j]
					// ∂r_i/∂w_k
					dri := 0.0
					if i == k {
						dri += sw[i]
					}
					dri += w[i] * cov[i][k]
					drj := 0.0
					if j == k {
						drj += sw[j]
					}
					drj += w[j] * cov[j][k]
					s += 2 * d * (dri - drj)
				}
			}
			g[k] = s
		}
		return g
	}
	return projectedGradient(n, lo, hi, totalW, 1e-2, maxIter, obj, grad)
}

// solveRiskBudget 最小化 Σ_i(r_i − b_i·T)^2，其中 T=w'Σw 为总方差，r_i 为风险贡献。
func solveRiskBudget(cov [][]float64, budgets, lo, hi []float64, totalW, maxIter, eps float64) []float64 {
	n := len(cov)
	if len(budgets) < n {
		budgets = make([]float64, n)
		for i := range budgets {
			budgets[i] = 1.0 / float64(n)
		}
	}
	// 归一化预算
	sumB := 0.0
	for _, b := range budgets {
		sumB += b
	}
	if sumB <= 0 {
		for i := range budgets {
			budgets[i] = 1.0 / float64(n)
		}
		sumB = 1.0
	}
	for i := range budgets {
		budgets[i] /= sumB
	}

	obj := func(w []float64) float64 {
		rc := dollarRiskContribution(cov, w)
		T := 0.0
		for i := range rc {
			T += rc[i]
		}
		f := 0.0
		for i := 0; i < n; i++ {
			d := rc[i] - budgets[i]*T
			f += d * d
		}
		return f
	}
	grad := func(w []float64) []float64 {
		sw := matVec(cov, w)
		T := 0.0
		rc := make([]float64, n)
		for i := range rc {
			rc[i] = w[i] * sw[i]
			T += rc[i]
		}
		g := make([]float64, n)
		for k := 0; k < n; k++ {
			s := 0.0
			for i := 0; i < n; i++ {
				d := rc[i] - budgets[i]*T
				// ∂r_i/∂w_k
				dri := 0.0
				if i == k {
					dri += sw[i]
				}
				dri += w[i] * cov[i][k]
				// ∂T/∂w_k = 2 (Σw)_k
				dT := 2 * sw[k]
				s += 2 * d * (dri - budgets[i]*dT)
			}
			g[k] = s
		}
		return g
	}
	return projectedGradient(n, lo, hi, totalW, 1e-2, maxIter, obj, grad)
}

// ---------- 数学小工具 ----------

func oneHot(n, idx int) []float64 {
	w := make([]float64, n)
	w[idx] = 1
	return w
}

func matVec(cov [][]float64, v []float64) []float64 {
	n := len(v)
	out := make([]float64, n)
	for i := 0; i < n; i++ {
		s := 0.0
		for j := 0; j < n && j < len(cov[i]); j++ {
			s += cov[i][j] * v[j]
		}
		out[i] = s
	}
	return out
}

func sqrtPortfolioVariance(cov [][]float64, w []float64) float64 {
	sw := matVec(cov, w)
	s := 0.0
	for i := range w {
		s += w[i] * sw[i]
	}
	if s < 0 {
		s = 0
	}
	return math.Sqrt(s)
}

// dollarRiskContribution 各资产风险贡献 r_i = w_i(Σw)_i（日度方差口径）。
func dollarRiskContribution(cov [][]float64, w []float64) []float64 {
	sw := matVec(cov, w)
	rc := make([]float64, len(w))
	for i := range w {
		rc[i] = w[i] * sw[i]
	}
	return rc
}

func normalizeToTotal(w []float64, total float64) []float64 {
	out := make([]float64, len(w))
	s := 0.0
	for _, v := range w {
		s += v
	}
	if s <= 1e-14 {
		for i := range out {
			out[i] = total / float64(len(w))
		}
		return out
	}
	scale := total / s
	for i := range w {
		out[i] = w[i] * scale
	}
	return out
}
