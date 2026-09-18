package portfolio

import (
	"math"
	"testing"
)

// 构造一个 4 资产协方差（两个高相关、两个相对独立），验证各算法输出合法权重。
func testCov() [][]float64 {
	return [][]float64{
		{0.0004, 0.00032, 0.0001, 0.00005},
		{0.00032, 0.00036, 0.00012, 0.00004},
		{0.0001, 0.00012, 0.0008, 0.0002},
		{0.00005, 0.00004, 0.0002, 0.0012},
	}
}

func TestOptimizeRiskParityValid(t *testing.T) {
	rets := []float64{0.0003, 0.0002, 0.0004, 0.0001}
	res, err := Optimize(OptimizeRequest{
		Method:      MethodRiskParity,
		Cov:         testCov(),
		Returns:     rets,
		TotalWeight: 0.9,
	})
	if err != nil || res == nil {
		t.Fatalf("Optimize risk_parity error: %v", err)
	}
	sum := 0.0
	for _, w := range res.Weights {
		if w < -1e-9 {
			t.Fatalf("negative weight %f", w)
		}
		sum += w
	}
	if math.Abs(sum-0.9) > 1e-6 {
		t.Fatalf("weights sum=%f, want 0.9", sum)
	}
	if math.IsNaN(res.Volatility) || res.Volatility <= 0 {
		t.Fatalf("invalid volatility %v", res.Volatility)
	}
	if math.IsNaN(res.Sharpe) || math.IsInf(res.Sharpe, 0) {
		t.Fatalf("invalid sharpe %v", res.Sharpe)
	}
	// 风险贡献占比应为正数之和≈1
	rc := 0.0
	for _, p := range res.RiskCtrPercent {
		rc += p
	}
	if math.Abs(rc-1) > 0.1 {
		t.Fatalf("risk percent sum=%f deviation too large", rc)
	}
}

func TestOptimizeAllMethodsDeterministic(t *testing.T) {
	cov := testCov()
	rets := []float64{0.0003, 0.0002, 0.0004, 0.0001}
	for _, m := range []OptimizeMethod{MethodMVO, MethodRiskParity, MethodRiskBudget} {
		r1, err := Optimize(OptimizeRequest{Method: m, Cov: cov, Returns: rets, TotalWeight: 1})
		if err != nil || r1 == nil {
			t.Fatalf("%s err: %v", m, err)
		}
		r2, _ := Optimize(OptimizeRequest{Method: m, Cov: cov, Returns: rets, TotalWeight: 1})
		s1, s2 := 0.0, 0.0
		for i := range r1.Weights {
			s1 += r1.Weights[i]
			s2 += r2.Weights[i]
		}
		if math.Abs(s1-1) > 1e-6 || math.Abs(s1-s2) > 1e-9 {
			t.Fatalf("%s not deterministic/valid: s1=%f s2=%f", m, s1, s2)
		}
	}
}

func TestSelectBestStrategy(t *testing.T) {
	res, strategy := SelectBestStrategy(OptimizationInput{
		Assets:          []string{"a", "b", "c", "d"},
		ExpectedReturns: map[string]float64{"a": 0.0003, "b": 0.0002, "c": 0.0004, "d": 0.0001},
		CovMatrix:       testCov(),
		Constraints: OptimizationConstraints{
			MaxWeight: 0.4, MaxIndustryWeight: 0.5, MaxStocksPerIndustry: 3,
		},
	})
	if strategy == "" {
		t.Fatalf("empty strategy")
	}
	_sum := 0.0
	for _, w := range res.Weights {
		if w < -1e-9 {
			t.Fatalf("neg weight %f", w)
		}
		_sum += w
	}
	if math.Abs(_sum-1) > 1e-6 {
		t.Fatalf("weights sum=%f", _sum)
	}
	if math.IsNaN(res.ExpectedVolatility) || res.ExpectedVolatility <= 0 {
		t.Fatalf("invalid vol %v", res.ExpectedVolatility)
	}
}