package exchange

import (
	"math"

	"github.com/quant1x/num"
)

// 协方差
func covariance(x, y []float64, meanX, meanY float64) float64 {
	lx := len(x)
	ly := len(y)
	if lx != ly || lx == 0 || ly == 0 {
		return 0 // 数据集长度必须相同
	}

	sum := 0.0
	for i := range x {
		sum += (x[i] - meanX) * (y[i] - meanY)
	}

	return sum / float64(len(x))
}

// 方差
func variance(x []float64, mean float64) float64 {
	if len(x) == 0 {
		return 0
	}

	sum := 0.0
	for _, v := range x {
		sum += math.Pow(v-mean, 2)
	}
	return sum / float64(len(x))
}

// EvaluateYields 评估收益率
func EvaluateYields(prices, markets []float64, riskFreeRate float64) (beta, alpha float64) {
	lx := len(prices)
	ly := len(markets)
	// 数据集长度必须相同
	if lx != ly || lx == 0 {
		return
	}
	meanPrice := num.Mean(prices)
	meanMarket := num.Mean(markets)

	cov := covariance(prices, markets, meanPrice, meanMarket)
	if cov == 0 {
		return
	}
	marketVariance := variance(markets, meanMarket)
	if marketVariance == 0 {
		return
	}
	// beta = 协方差 / 方差
	beta = cov / marketVariance
	// alpha = 个股平均收益率-(beta*市场组合平均收益率+无风险利率)
	alpha = meanPrice - (beta*meanMarket + riskFreeRate)
	return
}
