package factors

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/quantpilot/quantpilot/internal/data"
)

const (
	CategoryMomentum   = "momentum"
	CategoryValue      = "value"
	CategoryQuality    = "quality"
	CategoryVolatility = "volatility"
	CategoryLiquidity  = "liquidity"
	CategorySize       = "size"
	CategoryDividend   = "dividend"
	CategorySentiment  = "sentiment"
)

type FactorScore struct {
	FactorName  string  `json:"factorName"`
	Category    string  `json:"category"`
	Score       float64 `json:"score"`
	Weight      float64 `json:"weight"`
	Description string  `json:"description"`
}

type StockFactors struct {
	Code           string        `json:"code"`
	Name           string        `json:"name"`
	Factors        []FactorScore `json:"factors"`
	CompositeScore float64       `json:"compositeScore"`
	Rank           int           `json:"rank"`
}

// FactorCacheTTL 因子计算结果缓存有效期
// 交易时段行情每5分钟刷新一次，缓存5分钟可避免同一刷新周期内重复计算
const FactorCacheTTL = 5 * time.Minute

// cachedFactorResult 带过期时间的因子缓存项
type cachedFactorResult struct {
	factors   StockFactors
	expiresAt time.Time
}

type FactorEngine struct {
	stockPool []string
	computed  map[string]StockFactors
	cache     map[string]cachedFactorResult
	mu        sync.RWMutex
}

func NewFactorEngine() *FactorEngine {
	return &FactorEngine{
		stockPool: make([]string, 0),
		computed:  make(map[string]StockFactors),
		cache:     make(map[string]cachedFactorResult),
	}
}

func (fe *FactorEngine) SetStockPool(codes []string) {
	fe.mu.Lock()
	defer fe.mu.Unlock()
	fe.stockPool = codes
}

func (fe *FactorEngine) GetComputed() map[string]StockFactors {
	fe.mu.RLock()
	defer fe.mu.RUnlock()
	return fe.computed
}

// ComputeCached 计算股票因子并缓存（带过期时间），缓存未过期时直接返回
func (fe *FactorEngine) ComputeCached(snapshot data.StockSnapshot, history []data.StockSnapshot) StockFactors {
	fe.mu.RLock()
	if c, ok := fe.cache[snapshot.Code]; ok && time.Now().Before(c.expiresAt) {
		fe.mu.RUnlock()
		return c.factors
	}
	fe.mu.RUnlock()

	sf := ComputeStockFactors(snapshot, history)

	fe.mu.Lock()
	fe.cache[snapshot.Code] = cachedFactorResult{factors: sf, expiresAt: time.Now().Add(FactorCacheTTL)}
	fe.computed[snapshot.Code] = sf
	fe.mu.Unlock()
	return sf
}

// InvalidateCache 清空因子缓存（数据源更新后调用）
func (fe *FactorEngine) InvalidateCache() {
	fe.mu.Lock()
	defer fe.mu.Unlock()
	fe.cache = make(map[string]cachedFactorResult)
}

func ComputeStockFactors(snapshot data.StockSnapshot, history []data.StockSnapshot) StockFactors {
	factors := make([]FactorScore, 0, 16)

	prices := extractPrices(history)
	returns := computeReturns(prices)

	factors = append(factors, computeMomentumFactors(snapshot, prices, returns)...)
	factors = append(factors, computeValueFactors(snapshot, prices, returns)...)
	factors = append(factors, computeQualityFactors(snapshot, prices, returns)...)
	factors = append(factors, computeVolatilityFactors(snapshot, prices, returns)...)
	factors = append(factors, computeLiquidityFactors(snapshot, history, returns)...)
	factors = append(factors, computeSizeFactors(snapshot, prices, returns)...)
	factors = append(factors, computeDividendFactors(snapshot, prices, returns)...)

	sf := StockFactors{
		Code:    snapshot.Code,
		Name:    snapshot.Name,
		Factors: factors,
	}

	return sf
}

func RankAndScore(stocks []StockFactors, weights map[string]float64) []StockFactors {
	for i := range stocks {
		var composite float64
		for _, fs := range stocks[i].Factors {
			if w, ok := weights[fs.FactorName]; ok {
				composite += fs.Score * w
			} else if w, ok := weights[fs.Category]; ok {
				composite += fs.Score * w / float64(countFactorsInCategory(stocks[i].Factors, fs.Category))
			}
		}
		stocks[i].CompositeScore = roundTo4(composite)
	}

	sort.Slice(stocks, func(i, j int) bool {
		return stocks[i].CompositeScore > stocks[j].CompositeScore
	})

	for i := range stocks {
		stocks[i].Rank = i + 1
	}

	return stocks
}

func GetDefaultFactorWeights(marketRegime string) map[string]float64 {
	switch marketRegime {
	case "BULLISH":
		return map[string]float64{
			"momentum_5d":          0.08,
			"momentum_20d":         0.10,
			"momentum_60d":         0.07,
			"reversal_5d":          0.05,
			"ep_ratio":             0.05,
			"bp_ratio":             0.05,
			"roe_score":            0.05,
			"gross_margin_score":   0.05,
			"volatility_20d":       0.03,
			"idiosyncratic_vol":    0.03,
			"turnover_rate_score":  0.06,
			"amihud_illiq":         0.04,
			"log_market_cap":       0.10,
			"dividend_yield_score": 0.04,
			"beta":                 0.10,
			"sentiment_score":      0.06,
		}
	case "BEARISH":
		return map[string]float64{
			"momentum_5d":          0.03,
			"momentum_20d":         0.04,
			"momentum_60d":         0.03,
			"reversal_5d":          0.06,
			"ep_ratio":             0.10,
			"bp_ratio":             0.08,
			"roe_score":            0.08,
			"gross_margin_score":   0.07,
			"volatility_20d":       0.10,
			"idiosyncratic_vol":    0.06,
			"turnover_rate_score":  0.04,
			"amihud_illiq":         0.05,
			"log_market_cap":       0.04,
			"dividend_yield_score": 0.12,
			"beta":                 0.00,
			"sentiment_score":      0.10,
		}
	default:
		return map[string]float64{
			"momentum_5d":          0.06,
			"momentum_20d":         0.07,
			"momentum_60d":         0.06,
			"reversal_5d":          0.04,
			"ep_ratio":             0.07,
			"bp_ratio":             0.06,
			"roe_score":            0.07,
			"gross_margin_score":   0.06,
			"volatility_20d":       0.07,
			"idiosyncratic_vol":    0.05,
			"turnover_rate_score":  0.06,
			"amihud_illiq":         0.05,
			"log_market_cap":       0.07,
			"dividend_yield_score": 0.07,
			"beta":                 0.07,
			"sentiment_score":      0.08,
		}
	}
}

func SelectTopStocks(stocks []StockFactors, count int) []StockFactors {
	if count <= 0 || count >= len(stocks) {
		return stocks
	}
	return stocks[:count]
}

func ComputeAllForEngine(engine *FactorEngine, snapshots map[string]data.StockSnapshot, histories map[string][]data.StockSnapshot) []StockFactors {
	var results []StockFactors
	for _, code := range engine.stockPool {
		snap, ok := snapshots[code]
		if !ok {
			continue
		}
		hist := histories[code]
		sf := engine.ComputeCached(snap, hist)
		results = append(results, sf)
	}
	return results
}

func ToJSON(v interface{}) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprintf(`{"error":"%v"}`, err)
	}
	return string(b)
}

// --- Helper functions ---

func extractPrices(history []data.StockSnapshot) []float64 {
	if len(history) == 0 {
		return nil
	}
	prices := make([]float64, 0, len(history))
	for _, h := range history {
		if h.CurrentPrice > 0 {
			prices = append(prices, h.CurrentPrice)
		}
	}
	return prices
}

func computeReturns(prices []float64) []float64 {
	if len(prices) < 2 {
		return nil
	}
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
	var sum float64
	for _, v := range values {
		sum += v
	}
	return sum / float64(len(values))
}

func stdDev(values []float64) float64 {
	if len(values) < 2 {
		return 0
	}
	m := mean(values)
	var variance float64
	for _, v := range values {
		diff := v - m
		variance += diff * diff
	}
	return math.Sqrt(variance / float64(len(values)))
}

func minMax(values []float64) (float64, float64) {
	if len(values) == 0 {
		return 0, 0
	}
	mn, mx := values[0], values[0]
	for _, v := range values {
		if v < mn {
			mn = v
		}
		if v > mx {
			mx = v
		}
	}
	return mn, mx
}

func clampScore(s float64) float64 {
	if s < 0 {
		return 0
	}
	if s > 1 {
		return 1
	}
	return s
}

func roundTo4(v float64) float64 {
	return float64(int64(v*10000+0.5)) / 10000
}

func getReturnNDaysAgo(prices []float64, days int) float64 {
	if len(prices) <= days {
		return 0
	}
	current := prices[len(prices)-1]
	past := prices[len(prices)-1-days]
	if past <= 0 {
		return 0
	}
	return (current - past) / past
}

func countFactorsInCategory(factors []FactorScore, category string) int {
	count := 0
	for _, f := range factors {
		if f.Category == category {
			count++
		}
	}
	return count
}

func percentileRank(value float64, values []float64) float64 {
	if len(values) == 0 {
		return 0.5
	}
	below := 0
	for _, v := range values {
		if v < value {
			below++
		}
	}
	return float64(below) / float64(len(values))
}

// --- Factor computation functions ---

func computeMomentumFactors(snapshot data.StockSnapshot, prices []float64, returns []float64) []FactorScore {
	factors := make([]FactorScore, 0, 4)

	if len(prices) >= 2 {
		pricesWithCurrent := append([]float64{snapshot.CurrentPrice}, prices...)
		prices = pricesWithCurrent
	}

	ret5 := getReturnNDaysAgo(prices, 5)
	ret20 := getReturnNDaysAgo(prices, 20)
	ret60 := getReturnNDaysAgo(prices, 60)

	factors = append(factors, FactorScore{
		FactorName:  "momentum_5d",
		Category:    CategoryMomentum,
		Score:       clampScore(ret5*5 + 0.5),
		Weight:      0.0,
		Description: "5日动量因子：衡量过去5个交易日的价格收益率",
	})

	factors = append(factors, FactorScore{
		FactorName:  "momentum_20d",
		Category:    CategoryMomentum,
		Score:       clampScore(ret20*2 + 0.5),
		Weight:      0.0,
		Description: "20日动量因子：衡量过去20个交易日的价格收益率",
	})

	factors = append(factors, FactorScore{
		FactorName:  "momentum_60d",
		Category:    CategoryMomentum,
		Score:       clampScore(ret60 + 0.5),
		Weight:      0.0,
		Description: "60日动量因子：衡量过去60个交易日的价格收益率",
	})

	reversal5 := -ret5
	factors = append(factors, FactorScore{
		FactorName:  "reversal_5d",
		Category:    CategoryMomentum,
		Score:       clampScore(reversal5*5 + 0.5),
		Weight:      0.0,
		Description: "5日反转因子： negative momentum，均值回归信号",
	})

	return factors
}

func computeValueFactors(snapshot data.StockSnapshot, prices []float64, returns []float64) []FactorScore {
	factors := make([]FactorScore, 0, 2)

	if len(prices) < 2 {
		factors = append(factors, FactorScore{
			FactorName:  "ep_ratio",
			Category:    CategoryValue,
			Score:       0.5,
			Weight:      0.0,
			Description: "盈利收益率代理：价格相对历史均值的偏离",
		})
		factors = append(factors, FactorScore{
			FactorName:  "bp_ratio",
			Category:    CategoryValue,
			Score:       0.5,
			Weight:      0.0,
			Description: "账面价值收益率代理：价格相对历史中位数的偏离",
		})
		return factors
	}

	currentPrice := snapshot.CurrentPrice
	avgPrice := mean(prices)

	epScore := 0.5
	if avgPrice > 0 && currentPrice > 0 {
		epRatio := avgPrice / currentPrice
		epScore = clampScore((epRatio - 0.5) * 2)
	}

	factors = append(factors, FactorScore{
		FactorName:  "ep_ratio",
		Category:    CategoryValue,
		Score:       epScore,
		Weight:      0.0,
		Description: "盈利收益率代理：价格相对60日均价的偏离程度",
	})

	sortedPrices := make([]float64, len(prices))
	copy(sortedPrices, prices)
	sort.Float64s(sortedPrices)
	medianPrice := sortedPrices[len(sortedPrices)/2]

	bpScore := 0.5
	if medianPrice > 0 && currentPrice > 0 {
		bpRatio := medianPrice / currentPrice
		bpScore = clampScore((bpRatio - 0.5) * 2)
	}

	factors = append(factors, FactorScore{
		FactorName:  "bp_ratio",
		Category:    CategoryValue,
		Score:       bpScore,
		Weight:      0.0,
		Description: "账面价值收益率代理：价格相对历史中位数的偏离程度",
	})

	return factors
}

func computeQualityFactors(snapshot data.StockSnapshot, prices []float64, returns []float64) []FactorScore {
	factors := make([]FactorScore, 0, 2)

	if len(returns) < 5 {
		factors = append(factors, FactorScore{
			FactorName:  "roe_score",
			Category:    CategoryQuality,
			Score:       0.5,
			Weight:      0.0,
			Description: "ROE代理：收益率的风险调整后表现",
		})
		factors = append(factors, FactorScore{
			FactorName:  "gross_margin_score",
			Category:    CategoryQuality,
			Score:       0.5,
			Weight:      0.0,
			Description: "毛利率代理：价格波动与成交稳定性",
		})
		return factors
	}

	avgRet := mean(returns)
	retVol := stdDev(returns)

	roeScore := 0.5
	if retVol > 1e-8 {
		sharpeLike := avgRet / retVol
		roeScore = clampScore(sharpeLike*10 + 0.5)
	} else if avgRet > 0 {
		roeScore = 0.8
	}

	factors = append(factors, FactorScore{
		FactorName:  "roe_score",
		Category:    CategoryQuality,
		Score:       roeScore,
		Weight:      0.0,
		Description: "ROE代理：收益率/波动率（类Sharpe比率），衡量风险调整后收益",
	})

	grossMarginScore := 0.5
	if len(prices) >= 5 {
		rangeSum := 0.0
		for i := 1; i < len(prices); i++ {
			if prices[i-1] > 0 {
				rangeSum += math.Abs(prices[i]-prices[i-1]) / prices[i-1]
			}
		}
		avgRange := rangeSum / float64(len(prices)-1)
		priceLevel := math.Log(math.Max(snapshot.CurrentPrice, 1))
		grossMarginScore = clampScore(1.0 - avgRange*50 + priceLevel*0.05)
	}

	factors = append(factors, FactorScore{
		FactorName:  "gross_margin_score",
		Category:    CategoryQuality,
		Score:       grossMarginScore,
		Weight:      0.0,
		Description: "毛利率代理：价格稳定性与成交活跃度综合指标",
	})

	return factors
}

func computeVolatilityFactors(snapshot data.StockSnapshot, prices []float64, returns []float64) []FactorScore {
	factors := make([]FactorScore, 0, 3)

	if len(returns) < 5 {
		factors = append(factors, FactorScore{
			FactorName:  "volatility_20d",
			Category:    CategoryVolatility,
			Score:       0.5,
			Weight:      0.0,
			Description: "20日波动率：收益率的标准差（低波动=高分）",
		})
		factors = append(factors, FactorScore{
			FactorName:  "idiosyncratic_vol",
			Category:    CategoryVolatility,
			Score:       0.5,
			Weight:      0.0,
			Description: "特质波动率：剔除趋势后的残差波动",
		})
		factors = append(factors, FactorScore{
			FactorName:  "beta",
			Category:    CategoryVolatility,
			Score:       0.5,
			Weight:      0.0,
			Description: "Beta系数：系统性风险暴露",
		})
		return factors
	}

	vol20 := 0.0
	if len(returns) >= 20 {
		vol20 = stdDev(returns[len(returns)-20:])
	} else {
		vol20 = stdDev(returns)
	}

	volScore := clampScore(1.0 - vol20*5)
	factors = append(factors, FactorScore{
		FactorName:  "volatility_20d",
		Category:    CategoryVolatility,
		Score:       volScore,
		Weight:      0.0,
		Description: "20日波动率：收益率标准差，低波动=高得分",
	})

	idioVol := 0.0
	if len(returns) >= 20 {
		recent := returns[len(returns)-20:]
		trend := mean(recent)
		residuals := make([]float64, len(recent))
		for i, r := range recent {
			residuals[i] = r - trend
		}
		idioVol = stdDev(residuals)
	} else {
		trend := mean(returns)
		residuals := make([]float64, len(returns))
		for i, r := range returns {
			residuals[i] = r - trend
		}
		idioVol = stdDev(residuals)
	}

	idioScore := clampScore(1.0 - idioVol*5)
	factors = append(factors, FactorScore{
		FactorName:  "idiosyncratic_vol",
		Category:    CategoryVolatility,
		Score:       idioScore,
		Weight:      0.0,
		Description: "特质波动率：剔除均值趋势后的残差波动，低特质波动=高得分",
	})

	beta := 0.5
	if len(returns) >= 20 {
		recent := returns[len(returns)-20:]
		avgRet := mean(recent)
		marketProxy := 0.0
		for _, r := range recent {
			marketProxy += math.Abs(r)
		}
		marketProxy = marketProxy / float64(len(recent))
		if marketProxy > 1e-8 {
			beta = clampScore(math.Abs(avgRet) / marketProxy)
		}
	}

	factors = append(factors, FactorScore{
		FactorName:  "beta",
		Category:    CategoryVolatility,
		Score:       beta,
		Weight:      0.0,
		Description: "Beta系数代理：个股波动相对于市场波动的比率",
	})

	return factors
}

func computeLiquidityFactors(snapshot data.StockSnapshot, history []data.StockSnapshot, returns []float64) []FactorScore {
	factors := make([]FactorScore, 0, 2)

	turnoverRate := 0.0
	if snapshot.Volume > 0 && snapshot.CurrentPrice > 0 {
		turnoverRate = snapshot.Turnover / (snapshot.Volume * snapshot.CurrentPrice)
	}

	avgTurnover := 0.0
	turnoverCount := 0
	for _, h := range history {
		if h.Volume > 0 && h.CurrentPrice > 0 {
			avgTurnover += h.Turnover / (h.Volume * h.CurrentPrice)
			turnoverCount++
		}
	}
	if turnoverCount > 0 {
		avgTurnover /= float64(turnoverCount)
	}

	turnoverScore := 0.5
	if avgTurnover > 1e-8 {
		ratio := turnoverRate / avgTurnover
		turnoverScore = clampScore(ratio*0.5 + 0.5)
	}

	factors = append(factors, FactorScore{
		FactorName:  "turnover_rate_score",
		Category:    CategoryLiquidity,
		Score:       turnoverScore,
		Weight:      0.0,
		Description: "换手率因子：当前换手率相对历史平均水平的比值",
	})

	amihud := 0.0
	if len(history) >= 2 && len(returns) > 0 {
		absRetSum := 0.0
		turnoverSum := 0.0
		returnCount := 0

		for i := 1; i < len(history) && i <= len(returns); i++ {
			absRet := math.Abs(returns[i-1])
			turn := history[i].Turnover
			if turn > 1e-8 {
				absRetSum += absRet
				turnoverSum += turn
				returnCount++
			}
		}

		if returnCount > 0 && turnoverSum > 1e-8 {
			amihud = (absRetSum / float64(returnCount)) / (turnoverSum / float64(returnCount))
		}
	}

	amihudScore := clampScore(1.0 - amihud*10000)
	factors = append(factors, FactorScore{
		FactorName:  "amihud_illiq",
		Category:    CategoryLiquidity,
		Score:       amihudScore,
		Weight:      0.0,
		Description: "Amihud非流动性指标：|收益率|/成交额，低非流动性=高得分",
	})

	return factors
}

func computeSizeFactors(snapshot data.StockSnapshot, prices []float64, returns []float64) []FactorScore {
	factors := make([]FactorScore, 0, 1)

	sizeScore := 0.5
	if snapshot.CurrentPrice > 0 && snapshot.Volume > 0 {
		proxyMarketCap := snapshot.CurrentPrice * math.Max(snapshot.Volume, 1)
		logCap := math.Log(proxyMarketCap)
		sizeScore = clampScore(1.0 - (logCap-5)*0.15)
	}

	factors = append(factors, FactorScore{
		FactorName:  "log_market_cap",
		Category:    CategorySize,
		Score:       sizeScore,
		Weight:      0.0,
		Description: "市值因子代理：log(价格×成交量)，小市值溢价效应",
	})

	return factors
}

func computeDividendFactors(snapshot data.StockSnapshot, prices []float64, returns []float64) []FactorScore {
	factors := make([]FactorScore, 0, 1)

	dividendScore := 0.5

	if len(prices) >= 10 {
		priceVol := stdDev(prices)
		avgPrice := mean(prices)
		if avgPrice > 1e-8 {
			volRatio := priceVol / avgPrice
			priceStability := clampScore(1.0 - volRatio)

			retVol := stdDev(returns)
			returnStability := clampScore(1.0 - retVol*5)

			dividendScore = clampScore(priceStability*0.5 + returnStability*0.5)
		}
	}

	if snapshot.CurrentPrice > 50 {
		dividendScore = clampScore(dividendScore + 0.1)
	}

	factors = append(factors, FactorScore{
		FactorName:  "dividend_yield_score",
		Category:    CategoryDividend,
		Score:       dividendScore,
		Weight:      0.0,
		Description: "股息率代理：价格稳定性+收益率稳定性，高稳定性=高股息概率",
	})

	return factors
}
