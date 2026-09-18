package screener

import (
	"context"
	"fmt"
	"log"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/quantpilot/quantpilot/internal/data"
	"github.com/quantpilot/quantpilot/internal/orderbook"
)

// ==================== Pro版因子计算引擎 ====================

// ProFactorEngine 专业版多因子引擎
// 基于 DuckDB 历史数据的多因子模型，支持：
// 1. 多周期动量因子（1月/3月/6月/12月）
// 2. 低波动因子（历史波动率）
// 3. 成交量因子（流动性）
// 4. 振幅因子（风险）
// 5. 智能因子组合（用户画像+市场状态+因子健康度+历史表现）
// 6. 因子健康度监控
type ProFactorEngine struct {
	duckdbMgr *data.DuckDBManager
	ctx       context.Context
	barsCache map[string][]data.FactorBar
	// finCache 最近一次选股时预取的财务报告（key=ohlc规范代码，如 sh600519）。
	// 用于把真实财务数据接入价值/质量因子评分，避免用技术代理代替基本面。
	finCache map[string]*data.FinancialReport
	// orderBookCache 最近一个交易日收盘后落库的盘口日频摘要（key=规范代码小写，如 sh600519）。
	// 盘口因子（量比/内外盘/封板状态）数据源；order_book_daily 无数据时因子自动降级跳过。
	orderBookCache map[string]data.OrderBookDailyRow
	// benchByDate 配对基准指数（沪深300→上证指数）的日期→收盘价映射。
	// 协整/配对因子（相对市场残差 z-score）数据源；基准不可用时因子自动降级跳过。
	benchByDate   map[string]float64
	marketState   *MarketState
	smartComposer *SmartFactorComposer
}

// NewProFactorEngine 创建Pro版因子引擎
func NewProFactorEngine(duckdbMgr *data.DuckDBManager) *ProFactorEngine {
	return &ProFactorEngine{
		duckdbMgr:      duckdbMgr,
		ctx:            context.Background(),
		barsCache:      make(map[string][]data.FactorBar),
		finCache:       make(map[string]*data.FinancialReport),
		orderBookCache: make(map[string]data.OrderBookDailyRow),
		smartComposer:  NewSmartFactorComposer(),
	}
}

// MarketState 市场状态
type MarketState struct {
	Regime      string  `json:"regime"`      // bull/bear/range
	TrendScore  float64 `json:"trendScore"`  // 趋势得分
	Volatility  float64 `json:"volatility"`  // 市场波动率
	Breadth     float64 `json:"breadth"`     // 上涨比例
	Description string  `json:"description"` // 状态描述
}

// ProScreeningRequest Pro版选股请求
type ProScreeningRequest struct {
	StrategyID       string                `json:"strategyId"`
	CustomWeights    map[FactorID]float64  `json:"customWeights,omitempty"`
	Market           string                `json:"market"`
	MaxResults       int                   `json:"maxResults"`
	MinScore         float64               `json:"minScore"`
	LookbackDays     int                   `json:"lookbackDays"`              // 回看天数，默认252
	UseDynamicWeight bool                  `json:"useDynamicWeight"`          // 是否使用动态权重
	InvestorProfile  *data.InvestorProfile `json:"investorProfile,omitempty"` // 用户画像（智能权重时使用）
	// DiversifySeed 去同质化种子（>0 时启用）：确定性权重微扰，随用户 uid 变化，生成略不同的Top候选
	DiversifySeed int64 `json:"diversifySeed,omitempty"`
}

// ProScreeningResponse Pro版选股响应
type ProScreeningResponse struct {
	StrategyName    string                    `json:"strategyName"`
	TotalCount      int                       `json:"totalCount"`
	Results         []ProStockScore           `json:"results"`
	GeneratedAt     string                    `json:"generatedAt"`
	Formula         string                    `json:"formula"`        // 策略模板公式
	DynamicFormula  string                    `json:"dynamicFormula"` // 实际使用的动态公式
	Tier            string                    `json:"tier"`
	MarketState     *MarketState              `json:"marketState"`
	FactorHealth    map[FactorID]FactorHealth `json:"factorHealth"`
	DynamicWeights  map[FactorID]float64      `json:"dynamicWeights"`
	TemplateWeights map[FactorID]float64      `json:"templateWeights"` // 策略模板原始权重
	ProfileSummary  string                    `json:"profileSummary"`
	UsedSmartEngine bool                      `json:"usedSmartEngine"`
}

// ProStockScore Pro版股票评分（包含更详细的因子分解）
type ProStockScore struct {
	Code         string                   `json:"code"`
	Name         string                   `json:"name"`
	Market       string                   `json:"market"`
	Price        float64                  `json:"price"`
	ChangePct    float64                  `json:"changePct"`
	TotalScore   float64                  `json:"totalScore"`
	FactorScores map[FactorID]FactorScore `json:"factorScores"`
	Ranking      int                      `json:"ranking"`
	Reasons      []string                 `json:"reasons"`
	Warnings     []string                 `json:"warnings"`
	Momentum1M   float64                  `json:"momentum1m"`   // 1月动量
	Momentum3M   float64                  `json:"momentum3m"`   // 3月动量
	Momentum6M   float64                  `json:"momentum6m"`   // 6月动量
	Momentum12M  float64                  `json:"momentum12m"`  // 12月动量
	Volatility   float64                  `json:"volatility"`   // 历史波动率
	Amplitude    float64                  `json:"amplitude"`    // 平均振幅
	TurnoverRate float64                  `json:"turnoverRate"` // 换手率
	Liquidity    float64                  `json:"liquidity"`    // 流动性指标
	// AI评分拆解（对标 PanWatch AI Score）：总分映射为1-10，因子拆成正向(提升)/负向(拖累)
	AIScore       int           `json:"aiScore"`       // 1-10 AI评分（TotalScore/10 四舍五入后 clamp）
	FactorExplain FactorExplain `json:"factorExplain"` // 因子正负拆解（利好因子/风险因子）
}

// FactorContribution 单因子对总分的贡献拆解（相对中性50分）
type FactorContribution struct {
	FactorID     FactorID `json:"factorId"`
	FactorName   string   `json:"factorName"`
	Score        float64  `json:"score"`        // 因子得分 0-100
	Contribution float64  `json:"contribution"` // 相对中性50的贡献分（正=提升总分，负=拖累总分）
}

// FactorExplain AI评分拆解：正向(提升)与负向(拖累)因子贡献，各按贡献绝对值排序取前5。
// 设计参考 PanWatch signal_explain：把总分拆成「利好因子(绿) / 风险因子(红)」两组展示。
type FactorExplain struct {
	Positive []FactorContribution `json:"positive"`
	Negative []FactorContribution `json:"negative"`
}

// FactorHealth 因子健康度
type FactorHealth struct {
	FactorID     FactorID `json:"factorId"`
	FactorName   string   `json:"factorName"`
	IC           float64  `json:"ic"`           // 信息系数
	ICIR         float64  `json:"icir"`         // 信息系数比率
	WinRate      float64  `json:"winRate"`      // 胜率
	RecentReturn float64  `json:"recentReturn"` // 近期收益
	Status       string   `json:"status"`       // HEALTHY/WEAK/DEAD
	Description  string   `json:"description"`
}

// ==================== 核心选股流程 ====================

// RunProScreening 执行Pro版选股
func (pe *ProFactorEngine) RunProScreening(req ProScreeningRequest, symbols []data.StockSymbolInfo) (ProScreeningResponse, error) {
	startTime := time.Now()

	// 1. 获取历史数据
	lookbackDays := req.LookbackDays
	if lookbackDays <= 0 {
		lookbackDays = 252 // 默认1年
	}

	log.Printf("[ProEngine] Fetching %d stocks with %d days lookback...", len(symbols), lookbackDays)

	barsBySymbol, err := pe.fetchBarsForSymbols(symbols, lookbackDays)
	if err != nil {
		return ProScreeningResponse{}, fmt.Errorf("获取历史数据失败: %w", err)
	}
	pe.barsCache = barsBySymbol

	log.Printf("[ProEngine] Fetched data for %d symbols in %v", len(barsBySymbol), time.Since(startTime))

	// 预取全部标的最新的已披露财务报告，用于价值/质量因子的真实基本面评分
	pe.finCache = pe.fetchFinancialForSymbols(symbols)
	log.Printf("[ProEngine] Loaded financial reports for %d/%d symbols", len(pe.finCache), len(symbols))

	// 预取最近交易日收盘后的盘口日频摘要（order_book_daily），用于盘口因子评分；
	// 落库数据不足时因子自动降级跳过（不改变原有因子行为）。
	pe.orderBookCache = pe.loadOrderBookCache()

	// 预取配对基准指数（沪深300→上证指数）收盘价，用于协整/配对因子（相对市场残差z-score）。
	// 基准不可用时该因子自动降级跳过（不改变原有因子行为）。
	pe.benchByDate = pe.loadBenchmarkCache()

	// 2. 检测市场状态
	marketState, err := pe.detectMarketState()
	if err != nil {
		log.Printf("[ProEngine] Market state detection failed: %v, using default", err)
		marketState = &MarketState{Regime: "range", Description: "未知市场状态"}
	}
	pe.marketState = marketState

	log.Printf("[ProEngine] Market state: %s (trend=%.2f, vol=%.2f)",
		marketState.Regime, marketState.TrendScore, marketState.Volatility)

	// 3. 计算因子健康度
	factorHealth := pe.calculateFactorHealth(barsBySymbol)

	// 4. 确定因子权重
	var weights map[FactorID]float64
	strategyTemplate := GetStrategyTemplate(req.StrategyID)
	if strategyTemplate == nil {
		strategyTemplate = GetStrategyTemplate("balanced")
	}

	if req.CustomWeights != nil {
		weights = req.CustomWeights
	} else if req.InvestorProfile != nil {
		// 智能组合：策略模板权重作为基础，叠加用户画像+市场状态+因子健康度
		weights = pe.smartComposer.CalculateSmartWeights(req.InvestorProfile, marketState, factorHealth, strategyTemplate.Weights)
		log.Printf("[ProEngine] Smart weights for profile [%s] with strategy [%s]: %v",
			FormatProfileSummary(req.InvestorProfile), strategyTemplate.Name, weights)
	} else if req.UseDynamicWeight {
		weights = pe.calculateDynamicWeights(strategyTemplate.Weights, marketState, factorHealth)
	} else {
		weights = strategyTemplate.Weights
	}

	log.Printf("[ProEngine] Using weights: %v", weights)

	// 去同质化：用户个性化种子 >0 时，在策略/智能权重基础上做确定性微扰，
	// 使不同用户命中略不同的选股子集（多策略池分组），彻底避免"千人一面"。
	if req.DiversifySeed > 0 {
		weights = diversifyWeights(weights, req.DiversifySeed)
		log.Printf("[ProEngine] Diversified weights (seed=%d): %v", req.DiversifySeed, weights)
	}

	// 5. 计算所有股票的因子得分
	stockScores := pe.computeAllProFactorScores(barsBySymbol, pe.finCache)

	// 6. 计算加权总分并排序
	results := pe.computeWeightedProScores(stockScores, weights, req.MinScore, symbols)

	// 7. 排序并截取 Top N
	sort.Slice(results, func(i, j int) bool {
		return results[i].TotalScore > results[j].TotalScore
	})

	maxResults := req.MaxResults
	if maxResults <= 0 || maxResults > 5000 {
		maxResults = 5000
	}
	if len(results) > maxResults {
		results = results[:maxResults]
	}

	// 8. 添加排名
	for i := range results {
		results[i].Ranking = i + 1
	}

	// 9. 构建响应
	template := GetStrategyTemplate(req.StrategyID)
	if template == nil {
		template = GetStrategyTemplate("balanced")
	}

	usedSmart := req.InvestorProfile != nil
	profileSummary := ""
	if usedSmart {
		profileSummary = FormatProfileSummary(req.InvestorProfile)
	}

	response := ProScreeningResponse{
		StrategyName: template.Name,
		TotalCount:   len(results),
		Results:      results,
		GeneratedAt:  time.Now().Format("2006-01-02 15:04:05"),
		// 使用策略模板原始权重展示公式（让用户清楚看到不同策略的配置差异）
		Formula: buildFormulaString(template.Weights),
		// 使用实际使用的动态权重展示调整后的公式
		DynamicFormula:  buildFormulaString(weights),
		Tier:            "unified",
		MarketState:     marketState,
		FactorHealth:    factorHealth,
		DynamicWeights:  weights,
		TemplateWeights: template.Weights,
		ProfileSummary:  profileSummary,
		UsedSmartEngine: usedSmart,
	}

	log.Printf("[ProEngine] Completed in %v, found %d results", time.Since(startTime), len(results))

	return response, nil
}

// ==================== 数据获取 ====================

// fetchBarsForSymbols 批量获取股票K线数据
func (pe *ProFactorEngine) fetchBarsForSymbols(symbols []data.StockSymbolInfo, days int) (map[string][]data.FactorBar, error) {
	if pe.duckdbMgr == nil {
		return nil, fmt.Errorf("DuckDB not available")
	}

	if len(symbols) == 0 {
		return nil, fmt.Errorf("股票列表为空")
	}

	batchSize := 500
	result := make(map[string][]data.FactorBar)
	failedBatches := 0
	successfulBatches := 0

	for i := 0; i < len(symbols); i += batchSize {
		end := i + batchSize
		if end > len(symbols) {
			end = len(symbols)
		}

		batch := symbols[i:end]
		symbolStrings := make([]string, len(batch))
		for j, s := range batch {
			symbolStrings[j] = getOhlcSymbol(s.Symbol, s.Market)
		}

		bars, err := pe.duckdbMgr.BatchGetFactorBars(pe.ctx, symbolStrings, days)
		if err != nil {
			failedBatches++
			log.Printf("[ProEngine] Batch %d-%d FAILED: %v", i, end, err)
			continue
		}

		successfulBatches++
		for k, v := range bars {
			result[k] = v
		}

		log.Printf("[ProEngine] Loaded batch %d-%d: %d symbols with data (total: %d/%d)",
			i, end, len(bars), len(result), len(symbols))
	}

	if len(result) == 0 {
		return nil, fmt.Errorf("所有批次数据获取失败: %d 只股票, %d 个批次全部失败", len(symbols), failedBatches)
	}

	if failedBatches > 0 {
		log.Printf("[ProEngine] 部分批次失败: 成功 %d, 失败 %d, 获取 %d/%d 只股票数据",
			successfulBatches, failedBatches, len(result), len(symbols))
	}

	return result, nil
}

// fetchFinancialForSymbols 预取全部标的最新已披露财务报告（无未来函数，按披露日取数）。
// 并发受限，避免对 DuckDB 造成瞬时大压力。返回 map key = ohlc规范代码（sh600519）。
func (pe *ProFactorEngine) fetchFinancialForSymbols(symbols []data.StockSymbolInfo) map[string]*data.FinancialReport {
	result := make(map[string]*data.FinancialReport)
	if pe.duckdbMgr == nil || len(symbols) == 0 {
		return result
	}

	asOf := time.Now().Format("2006-01-02")

	const workers = 8
	sem := make(chan struct{}, workers)
	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, s := range symbols {
		key := getOhlcSymbol(s.Symbol, s.Market)
		wg.Add(1)
		go func(key string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			rpt, err := pe.duckdbMgr.GetFinancialAsOf(key, asOf)
			if err != nil {
				return // 查询出错视为无财务数据，静默跳过（严禁回退伪造）
			}
			if rpt == nil {
				return
			}
			mu.Lock()
			result[key] = rpt
			mu.Unlock()
		}(key)
	}

	wg.Wait()
	return result
}

// loadOrderBookCache 加载最近一个交易日的盘口日频摘要（order_book_daily）。
// 空串取最近数据日；查询失败/表未落库时返回空 map（盘口因子自动降级跳过，不影响其他因子）。
func (pe *ProFactorEngine) loadOrderBookCache() map[string]data.OrderBookDailyRow {
	result := make(map[string]data.OrderBookDailyRow)
	if pe.duckdbMgr == nil {
		return result
	}
	rows, err := data.GetOrderBookDailyRows(pe.ctx, pe.duckdbMgr, "")
	if err != nil {
		log.Printf("[ProEngine] 盘口日频摘要加载失败（盘口因子降级跳过）: %v", err)
		return result
	}
	for k, v := range rows {
		result[k] = v
	}
	if len(result) > 0 {
		log.Printf("[ProEngine] 盘口日频摘要已加载 %d 条（因子数据日期=%s）", len(result), firstOrderBookDate(rows))
	}
	return result
}

// firstOrderBookDate 取任意一条记录的数据日期（用于日志）
func firstOrderBookDate(rows map[string]data.OrderBookDailyRow) string {
	for _, r := range rows {
		return r.TradeDate
	}
	return ""
}

// loadBenchmarkCache 加载配对基准指数（优先沪深300，回退上证指数）的日期→收盘价映射。
// 基准不可用时返回空 map（协整/配对因子自动降级跳过，不影响其他因子）。
func (pe *ProFactorEngine) loadBenchmarkCache() map[string]float64 {
	result := make(map[string]float64)
	if pe.duckdbMgr == nil {
		return result
	}
	for _, idx := range []string{"sh000300", "sh000001"} {
		raw, err := pe.duckdbMgr.GetIndexKlineFromStock(pe.ctx, idx, 260)
		if err != nil || len(raw) == 0 {
			continue
		}
		for _, r := range raw {
			if r.Close > 0 {
				result[r.Date.Format("2006-01-02")] = r.Close
			}
		}
		if len(result) > 0 {
			log.Printf("[ProEngine] 配对基准指数 %s 已加载 %d 根（协整/配对因子可用）", idx, len(result))
		}
		return result
	}
	log.Printf("[ProEngine] 配对基准指数不可用（sh000300/sh000001），协整/配对因子降级跳过")
	return result
}

// getOhlcSymbol 将 stock_basic 格式的代码转换为 ohlc 表格式
func getOhlcSymbol(symbol string, market string) string {
	if len(symbol) >= 2 {
		prefix := strings.ToLower(symbol[:2])
		if prefix == "sh" || prefix == "sz" || prefix == "bj" {
			return strings.ToLower(symbol)
		}
	}

	switch strings.ToUpper(market) {
	case "SH":
		return "sh" + symbol
	case "SZ":
		return "sz" + symbol
	case "BJ":
		return "bj" + symbol
	default:
		firstChar := symbol[0]
		switch firstChar {
		case '6', '5':
			return "sh" + symbol
		case '0', '2', '3':
			return "sz" + symbol
		case '4', '8', '9':
			return "bj" + symbol
		default:
			return "sh" + symbol
		}
	}
}

// ==================== 市场状态检测 ====================

// detectMarketState 检测当前市场状态
func (pe *ProFactorEngine) detectMarketState() (*MarketState, error) {
	if pe.duckdbMgr == nil {
		return nil, fmt.Errorf("DuckDB not available")
	}

	// 获取市场指数K线数据（252个交易日）
	indexBars, err := pe.duckdbMgr.GetMarketIndexBars(pe.ctx, 252)
	if err != nil || len(indexBars) == 0 {
		return nil, fmt.Errorf("无法获取指数数据: %w", err)
	}

	// 大盘强弱采用 上证指数(sh000001, 40%) + 深证成指(sz399001, 60%) 加权衡量
	sh := indexBars["sh000001"] // 上证指数
	sz := indexBars["sz399001"] // 深证成指
	hasSH := len(sh) >= 60
	hasSZ := len(sz) >= 60

	if !hasSH && !hasSZ {
		return &MarketState{
			Regime:      "range",
			TrendScore:  50,
			Volatility:  15,
			Breadth:     50,
			Description: "指数数据不足，默认震荡市",
		}, nil
	}

	// 计算各指数趋势得分与波动率，按 40%/60% 加权（某一指数缺失时用另一只补齐）
	var trendScore, volatility float64
	switch {
	case hasSH && hasSZ:
		trendScore = 0.4*pe.calculateTrendScore(sh) + 0.6*pe.calculateTrendScore(sz)
		volatility = 0.4*pe.calculateVolatility(sh, 20) + 0.6*pe.calculateVolatility(sz, 20)
	case hasSH:
		trendScore = pe.calculateTrendScore(sh)
		volatility = pe.calculateVolatility(sh, 20)
	default:
		trendScore = pe.calculateTrendScore(sz)
		volatility = pe.calculateVolatility(sz, 20)
	}

	// 计算市场广度（上涨股票比例）
	breadth := pe.calculateMarketBreadth()

	// 判断市场状态
	regime := "range"
	description := "震荡市"

	if trendScore > 70 && volatility < 25 {
		regime = "bull"
		description = "牛市 - 趋势向上，波动率适中"
	} else if trendScore < 30 && volatility > 25 {
		regime = "bear"
		description = "熊市 - 趋势向下，波动率较高"
	} else if trendScore > 60 {
		regime = "bull_range"
		description = "偏强震荡 - 趋势向上但波动率较高"
	} else if trendScore < 40 {
		regime = "bear_range"
		description = "偏弱震荡 - 趋势向下但波动率较低"
	}

	return &MarketState{
		Regime:      regime,
		TrendScore:  trendScore,
		Volatility:  volatility,
		Breadth:     breadth,
		Description: description,
	}, nil
}

// calculateTrendScore 计算趋势得分
func (pe *ProFactorEngine) calculateTrendScore(bars []data.FactorBar) float64 {
	if len(bars) < 60 {
		return 50
	}

	// 注意：bars 按日期降序排列
	// 计算20日均线
	sma20 := pe.calculateSMA(bars, 20)
	// 计算60日均线
	sma60 := pe.calculateSMA(bars, 60)
	// 计算120日均线
	sma120 := pe.calculateSMA(bars, 120)

	latestClose := bars[0].Close

	// 趋势得分：基于价格与均线的关系
	score := 50.0

	// 短期趋势（价格 vs 20日均线）
	if latestClose > sma20 {
		score += 15
	} else {
		score -= 15
	}

	// 中期趋势（20日均线 vs 60日均线）
	if sma20 > sma60 {
		score += 10
	} else {
		score -= 10
	}

	// 长期趋势（60日均线 vs 120日均线）
	if sma60 > sma120 {
		score += 10
	} else {
		score -= 10
	}

	// 近期动量（20天涨幅）
	return20 := pe.calculateReturn(bars, 20)
	if return20 > 0.05 {
		score += 10
	} else if return20 < -0.05 {
		score -= 10
	}

	// 限制在0-100范围
	if score < 0 {
		score = 0
	} else if score > 100 {
		score = 100
	}

	return score
}

// calculateSMA 计算简单移动平均
func (pe *ProFactorEngine) calculateSMA(bars []data.FactorBar, period int) float64 {
	if len(bars) < period {
		// 如果数据不足，使用可用数据
		period = len(bars)
	}

	sum := 0.0
	for i := 0; i < period; i++ {
		sum += bars[i].Close
	}

	return sum / float64(period)
}

// calculateReturn 计算N天收益率
// bars 按日期降序排列（bars[0]为最新）
// 返回实际期间的收益率，不做年化缩放
func (pe *ProFactorEngine) calculateReturn(bars []data.FactorBar, days int) float64 {
	if len(bars) < 2 {
		return 0
	}

	availableDays := len(bars) - 1
	if availableDays < days {
		// 数据不足时返回实际可用期间的收益率（不做天数缩放）
		currentPrice := bars[0].Close
		pastPrice := bars[availableDays].Close
		if pastPrice <= 0 {
			return 0
		}
		return (currentPrice - pastPrice) / pastPrice
	}

	currentPrice := bars[0].Close
	pastPrice := bars[days].Close

	if pastPrice <= 0 {
		return 0
	}

	return (currentPrice - pastPrice) / pastPrice
}

// calculateVolatility 计算历史波动率
func (pe *ProFactorEngine) calculateVolatility(bars []data.FactorBar, period int) float64 {
	if len(bars) < period+1 {
		return 15 // 默认波动率
	}

	// 计算日收益率
	returns := make([]float64, period)
	for i := 0; i < period; i++ {
		if bars[i+1].Close > 0 {
			returns[i] = (bars[i].Close - bars[i+1].Close) / bars[i+1].Close
		}
	}

	// 计算标准差
	mean := 0.0
	for _, r := range returns {
		mean += r
	}
	mean /= float64(len(returns))

	variance := 0.0
	for _, r := range returns {
		diff := r - mean
		variance += diff * diff
	}

	stdDev := math.Sqrt(variance / float64(len(returns)-1))

	// 年化波动率
	return stdDev * math.Sqrt(252) * 100
}

// calculateMarketBreadth 计算市场广度
func (pe *ProFactorEngine) calculateMarketBreadth() float64 {
	// 简化计算：基于最近一天的上涨/下跌比例
	// 实际应计算上涨家数/总家数
	totalSymbols := len(pe.barsCache)
	if totalSymbols == 0 {
		return 50
	}

	upCount := 0
	for _, bars := range pe.barsCache {
		if len(bars) > 0 && bars[0].PctChg > 0 {
			upCount++
		}
	}

	return float64(upCount) / float64(totalSymbols) * 100
}

// ==================== 因子健康度计算 ====================

// calculateFactorHealth 计算因子健康度
func (pe *ProFactorEngine) calculateFactorHealth(barsBySymbol map[string][]data.FactorBar) map[FactorID]FactorHealth {
	result := make(map[FactorID]FactorHealth)

	factorConfigs := []struct {
		id       FactorID
		name     string
		calcFunc func(bars []data.FactorBar) float64
	}{
		{FactorValue, "价值 (Value)", pe.calcValueFactorValue},
		{FactorMomentum, "动量 (Momentum)", pe.calcMomentumFactorValue},
		{FactorLowVolatility, "低波动 (Low Vol)", pe.calcLowVolatilityFactorValue},
		{FactorLiquidity, "流动性 (Liquidity)", pe.calcLiquidityFactorValue},
	}

	for _, fc := range factorConfigs {
		// 计算所有股票的因子值
		var factorValues []float64
		for _, bars := range barsBySymbol {
			if len(bars) < 20 {
				continue
			}
			fv := fc.calcFunc(bars)
			if !math.IsNaN(fv) && !math.IsInf(fv, 0) {
				factorValues = append(factorValues, fv)
			}
		}

		// 计算IC（信息系数）近似值
		ic := pe.calculateApproximateIC(factorValues)

		// 计算胜率
		winRate := pe.calculateWinRate(factorValues)

		// 判断健康状态
		status := "HEALTHY"
		description := "因子表现良好"

		if ic < 0.02 {
			status = "DEAD"
			description = "因子失效，预测能力很弱"
		} else if ic < 0.05 {
			status = "WEAK"
			description = "因子效果减弱"
		}

		result[fc.id] = FactorHealth{
			FactorID:     fc.id,
			FactorName:   fc.name,
			IC:           ic,
			ICIR:         ic / 0.05, // 简化的ICIR
			WinRate:      winRate,
			RecentReturn: ic * 100,
			Status:       status,
			Description:  description,
		}
	}

	return result
}

// calculateApproximateIC 计算近似IC
func (pe *ProFactorEngine) calculateApproximateIC(values []float64) float64 {
	if len(values) < 30 {
		return 0.03 // 默认IC
	}

	// 简化的IC计算：将因子值排序后，分组计算收益差
	sorted := make([]float64, len(values))
	copy(sorted, values)
	sort.Float64s(sorted)

	// 分组
	n := len(sorted)
	groupSize := n / 5

	// 计算第一组和第五组的均值差
	topMean := 0.0
	bottomMean := 0.0

	for i := 0; i < groupSize; i++ {
		bottomMean += sorted[i]
		topMean += sorted[n-1-i]
	}

	topMean /= float64(groupSize)
	bottomMean /= float64(groupSize)

	// IC ≈ (top_mean - bottom_mean) / total_std
	stdDev := 0.0
	mean := 0.0
	for _, v := range values {
		mean += v
	}
	mean /= float64(len(values))
	for _, v := range values {
		diff := v - mean
		stdDev += diff * diff
	}
	stdDev = math.Sqrt(stdDev / float64(len(values)))

	if stdDev < 1e-10 {
		return 0
	}

	return (topMean - bottomMean) / stdDev * 0.1 // 缩放因子
}

// calculateWinRate 计算因子胜率
func (pe *ProFactorEngine) calculateWinRate(values []float64) float64 {
	if len(values) < 10 {
		return 50
	}

	winCount := 0
	for _, v := range values {
		if v > 0 {
			winCount++
		}
	}

	return float64(winCount) / float64(len(values)) * 100
}

// ==================== 动态权重计算 ====================

// calculateDynamicWeights 基于策略模板权重 + 市场状态 + 因子健康度 计算动态权重
// 保守调整：市场状态和因子健康度仅做微调，保持策略模板特征主导
func (pe *ProFactorEngine) calculateDynamicWeights(baseWeights map[FactorID]float64, ms *MarketState, health map[FactorID]FactorHealth) map[FactorID]float64 {
	weights := make(map[FactorID]float64)
	for k, v := range baseWeights {
		weights[k] = v
	}

	// 保守调整：每个因子的调整幅度不超过其基础权重的15%
	const maxAdjustRatio = 0.15

	switch ms.Regime {
	case "bull":
		adjustDynamicWeight(weights, FactorMomentum, 0.03, maxAdjustRatio)
		adjustDynamicWeight(weights, FactorValue, 0.02, maxAdjustRatio)
		adjustDynamicWeight(weights, FactorLowVolatility, -0.03, maxAdjustRatio)
	case "bear":
		adjustDynamicWeight(weights, FactorLowVolatility, 0.03, maxAdjustRatio)
		adjustDynamicWeight(weights, FactorQuality, 0.02, maxAdjustRatio)
		adjustDynamicWeight(weights, FactorMomentum, -0.04, maxAdjustRatio)
	case "bull_range":
		adjustDynamicWeight(weights, FactorMomentum, 0.02, maxAdjustRatio)
		adjustDynamicWeight(weights, FactorLowVolatility, -0.02, maxAdjustRatio)
	case "bear_range":
		adjustDynamicWeight(weights, FactorLowVolatility, 0.02, maxAdjustRatio)
		adjustDynamicWeight(weights, FactorQuality, 0.01, maxAdjustRatio)
		adjustDynamicWeight(weights, FactorMomentum, -0.02, maxAdjustRatio)
	default:
	}

	// 因子健康度保守调整
	for id, h := range health {
		if h.Status == "DEAD" {
			if w, ok := weights[id]; ok {
				weights[id] = w * 0.75
			}
		} else if h.Status == "WEAK" {
			if w, ok := weights[id]; ok {
				weights[id] = w * 0.90
			}
		}
	}

	total := 0.0
	for _, w := range weights {
		if w < 0 {
			w = 0
		}
		total += w
	}

	if total > 0 {
		for id := range weights {
			weights[id] = weights[id] / total
		}
	} else {
		equalWeight := 1.0 / float64(len(weights))
		for id := range weights {
			weights[id] = equalWeight
		}
	}

	return weights
}

// adjustDynamicWeight 对动态权重做受限调整
func adjustDynamicWeight(weights map[FactorID]float64, id FactorID, delta float64, maxRatio float64) {
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

// ==================== Pro版因子计算 ====================

// computeAllProFactorScores 计算所有股票的Pro版因子得分
func (pe *ProFactorEngine) computeAllProFactorScores(barsBySymbol map[string][]data.FactorBar, financial map[string]*data.FinancialReport) map[string]map[FactorID]FactorScore {
	result := make(map[string]map[FactorID]FactorScore)

	// 收集原始因子值用于Z-score
	rawValues := pe.collectProFactorValues(barsBySymbol, financial)

	// 计算统计量
	stats := computeStats(rawValues)

	// 盘口因子样本数：仅当足够多标的（≥20）有盘口数据时才参与评分，
	// 避免落库初期样本稀疏导致横截面Z-score失真（数据不足自动降级）。
	orderBookSamples := len(rawValues[FactorOrderBook])
	useOrderBookFactor := orderBookSamples >= 20

	// 协整/配对因子样本数：仅当配对基准可用且全池有足够样本时才参与评分（否则自动降级）。
	cointegrationSamples := len(rawValues[FactorCointegration])
	useCointegrationFactor := len(pe.benchByDate) > 0 && cointegrationSamples >= 20

	// 计算每只股票的因子得分
	processed := 0
	startCompute := time.Now()
	for symbol, bars := range barsBySymbol {
		if len(bars) < 20 {
			continue
		}
		processed++
		// 全市场逐只打分阶段，定期输出进度，避免"看似卡住"
		if processed%200 == 0 {
			log.Printf("[ProEngine] 因子评分进度: %d/%d 只 (累计耗时 %v)",
				processed, len(barsBySymbol), time.Since(startCompute).Round(time.Second))
		}

		factorScores := make(map[FactorID]FactorScore)

		fin := financial[symbol]
		factorScores[FactorValue] = pe.calcProValueFactor(bars, stats, fin)
		factorScores[FactorQuality] = pe.calcProQualityFactor(bars, stats, fin)
		factorScores[FactorMomentum] = pe.calcProMomentumFactor(bars, stats)
		factorScores[FactorLowVolatility] = pe.calcProLowVolatilityFactor(bars, stats)
		factorScores[FactorEarningsStability] = pe.calcProStabilityFactor(bars, stats)
		factorScores[FactorLiquidity] = pe.calcProLiquidityFactor(bars, stats)

		// 盘口因子：仅当有该标的盘口数据且全池样本足够时计入评分
		if useOrderBookFactor {
			if ob, ok := pe.orderBookCache[symbol]; ok {
				factorScores[FactorOrderBook] = pe.calcProOrderBookFactor(bars, stats, &ob)
			}
		}

		// 协整/配对因子：仅当基准可用、全池样本足够且该标的可计算时计入评分
		if useCointegrationFactor {
			if z, ok := pe.calcCointegrationFactorValue(bars); ok {
				factorScores[FactorCointegration] = pe.calcProCointegrationFactor(stats, z)
			}
		}

		result[symbol] = factorScores
	}

	log.Printf("[ProEngine] 因子评分完成: %d/%d 只有效, 总耗时 %v",
		processed, len(barsBySymbol), time.Since(startCompute).Round(time.Millisecond))
	return result
}

// collectProFactorValues 收集Pro版原始因子值
func (pe *ProFactorEngine) collectProFactorValues(barsBySymbol map[string][]data.FactorBar, financial map[string]*data.FinancialReport) map[FactorID][]float64 {
	raw := make(map[FactorID][]float64)

	for symbol, bars := range barsBySymbol {
		if len(bars) < 20 {
			continue
		}

		// 价值因子：优先真实财务 EP/BP，财务缺失时回退技术代理
		raw[FactorValue] = append(raw[FactorValue], pe.calcFinancialValueFactorValue(bars, financial[symbol]))

		// 质量因子：优先真实财务 ROE/毛利率/净利率/负债率，财务缺失时回退技术代理
		raw[FactorQuality] = append(raw[FactorQuality], pe.calcFinancialQualityFactorValue(bars, financial[symbol]))

		// Momentum proxy: 使用12月动量
		raw[FactorMomentum] = append(raw[FactorMomentum], pe.calcMomentumFactorValue(bars))

		// Low Volatility proxy: 使用历史波动率
		raw[FactorLowVolatility] = append(raw[FactorLowVolatility], pe.calcLowVolatilityFactorValue(bars))

		// Earnings Stability proxy: 使用成交量稳定性
		raw[FactorEarningsStability] = append(raw[FactorEarningsStability], pe.calcStabilityFactorValue(bars))

		// Liquidity proxy: 使用平均成交额
		raw[FactorLiquidity] = append(raw[FactorLiquidity], pe.calcLiquidityFactorValue(bars))

		// 盘口因子：仅当该标的有 order_book_daily 落库数据时收集原始值
		if ob, ok := pe.orderBookCache[symbol]; ok {
			raw[FactorOrderBook] = append(raw[FactorOrderBook], pe.calcOrderBookFactorValue(&ob))
		}

		// 协整/配对因子：仅当配对基准可用时收集原始值（个股相对市场的近期残差z-score）
		if len(pe.benchByDate) > 0 {
			if z, ok := pe.calcCointegrationFactorValue(bars); ok {
				raw[FactorCointegration] = append(raw[FactorCointegration], z)
			}
		}
	}

	return raw
}

// calcOrderBookFactorValue 计算盘口因子原始值（越高越有吸引力）
// 合成 量比（放量活跃度，正向、封顶5）与 内外盘（内盘占比越低=买盘越强，取1-占比），
// 叠加涨跌停状态风险罚分：跌停（封死/打开）与巨量跌停 罚分最大，涨停封死（买不进）次之。
// 数据来自 order_book_daily（真实盘口落库）；无数据时由上层降级跳过。
func (pe *ProFactorEngine) calcOrderBookFactorValue(ob *data.OrderBookDailyRow) float64 {
	if ob == nil {
		return 0
	}
	vr := math.Min(math.Max(ob.VolumeRatio, 0), 5) / 5 // 量比 → 0~1
	inOut := 1 - ob.InOutRatio                         // 内盘占比 0~1 → 买盘强势 0~1
	penalty := 0.0
	switch orderbook.State(ob.State) {
	case orderbook.StateSealedLimitDown, orderbook.StateOpenedLimitDown:
		penalty = 1.0
	case orderbook.StateSealedLimitUp:
		penalty = 0.6 // 封死涨停买不进，入选无意义
	}
	if ob.HugeVolumeDown && penalty < 1.0 {
		penalty = 1.0
	}
	return (0.5*vr + 0.5*inOut) * (1 - penalty)
}

// calcProOrderBookFactor 计算盘口因子得分（0-100）
func (pe *ProFactorEngine) calcProOrderBookFactor(bars []data.FactorBar, stats map[FactorID]FactorStats, ob *data.OrderBookDailyRow) FactorScore {
	st, ok := stats[FactorOrderBook]
	if !ok {
		st = FactorStats{Mean: 0, StdDev: 1}
	}
	raw := pe.calcOrderBookFactorValue(ob)
	score := zScoreTo0100(raw, st.Mean, st.StdDev)
	return FactorScore{
		FactorID:    FactorOrderBook,
		FactorName:  "盘口 (Order Book)",
		Score:       roundScore(score),
		Contributor: "量比/内外盘/封板状态",
		Breakdown: map[string]float64{
			"volume_ratio":  roundScore(ob.VolumeRatio),
			"in_out_ratio":  roundScore(ob.InOutRatio),
			"seal_vol":      roundScore(ob.SealVol),
			"state_penalty": penaltyForState(ob),
		},
	}
}

// calcProCointegrationFactor 计算协整/配对因子得分（0-100）。
// 个股相对市场（沪深300）的近期残差 z-score 越低（相对超卖），越具均值回归吸引力，
// 故用 inverse（值越低分越高）。仅由上层在全池样本与基准可用时调用。
func (pe *ProFactorEngine) calcProCointegrationFactor(stats map[FactorID]FactorStats, z float64) FactorScore {
	st, ok := stats[FactorCointegration]
	if !ok {
		st = FactorStats{Mean: 0, StdDev: 1}
	}
	score := inverseZScoreTo0100(z, st.Mean, st.StdDev)
	return FactorScore{
		FactorID:    FactorCointegration,
		FactorName:  "配对/协整 (Cointegration)",
		Score:       roundScore(score),
		Contributor: "相对沪深300残差均值回归",
		Breakdown: map[string]float64{
			"market_relative_z": roundScore(z),
			"score":             roundScore(score),
		},
	}
}

// calcCointegrationFactorValue 计算个股相对配对基准（沪深300）的近期残差 z-score。
// 返回 (平均z, true) 仅当：配对基准可用、个股与基准对齐样本足够。
// 逻辑与配对交易策略一致：滚动OLS(log个股 ~ log基准)，残差滚动 z-score；
// 取最近 min(horizon, 对齐样本数) 根的对残差 z 均值作为「相对市场偏离度」。
// 无未来函数：每点只用截至当天的数据，绝不伪造/回退。
func (pe *ProFactorEngine) calcCointegrationFactorValue(bars []data.FactorBar) (float64, bool) {
	const lookback = 60 // 滚动OLS与残差z的窗口（根K线）
	const horizon = 20  // 取最近多少根的残差z均值
	if len(pe.benchByDate) == 0 || len(bars) < lookback+1 {
		return 0, false
	}

	// 对齐
	logX := make([]float64, 0, len(bars)) // log(基准)
	logY := make([]float64, 0, len(bars)) // log(个股)
	for _, b := range bars {
		bc, ok := pe.benchByDate[b.Date.Format("2006-01-02")]
		if !ok || b.Close <= 0 || bc <= 0 {
			continue
		}
		logX = append(logX, math.Log(bc))
		logY = append(logY, math.Log(b.Close))
	}
	m := len(logX)
	if m < lookback+1 {
		return 0, false
	}

	// 逐点滚动残差
	res := make([]float64, m)
	for j := lookback - 1; j < m; j++ {
		lo := j - lookback + 1
		beta, alpha := olsLogBetaAlpha(logX[lo:j+1], logY[lo:j+1])
		if beta >= 0 {
			res[j] = logY[j] - (beta*logX[j] + alpha)
		} else {
			res[j] = 0 // 负相关（罕见）→ 置0避免误判
		}
	}

	// 残差滚动z-score
	zs := make([]float64, 0, horizon)
	for j := lookback - 1; j < m; j++ {
		lo := j - lookback + 1
		window := res[lo : j+1]
		var sum float64
		for _, v := range window {
			sum += v
		}
		mean := sum / float64(len(window))
		var s float64
		for _, v := range window {
			d := v - mean
			s += d * d
		}
		sd := 0.0
		if len(window) > 1 {
			sd = math.Sqrt(s / float64(len(window)-1))
		}
		if sd <= 1e-12 {
			continue
		}
		zs = append(zs, (res[j]-mean)/sd)
	}
	if len(zs) == 0 {
		return 0, false
	}
	// 仅取最近 horizon 根
	if len(zs) > horizon {
		zs = zs[len(zs)-horizon:]
	}
	var avg float64
	for _, z := range zs {
		avg += z
	}
	return avg / float64(len(zs)), true
}

// penaltyForState 返回盘口状态风险罚分（用于展示）
func penaltyForState(ob *data.OrderBookDailyRow) float64 {
	if ob == nil {
		return 0
	}
	switch orderbook.State(ob.State) {
	case orderbook.StateSealedLimitDown, orderbook.StateOpenedLimitDown:
		return 1.0
	case orderbook.StateSealedLimitUp:
		return 0.6
	}
	if ob.HugeVolumeDown {
		return 1.0
	}
	return 0
}

// olsLogBetaAlpha 计算 y = beta*x + alpha 的普通最小二乘估计（协整/配对因子用）。
func olsLogBetaAlpha(xs, ys []float64) (beta, alpha float64) {
	m := len(xs)
	if m < 2 {
		return 0, 0
	}
	var sx, sy, sxx, sxy float64
	for i := 0; i < m; i++ {
		sx += xs[i]
		sy += ys[i]
		sxx += xs[i] * xs[i]
		sxy += xs[i] * ys[i]
	}
	n := float64(m)
	den := n*sxx - sx*sx
	if math.Abs(den) < 1e-12 {
		return 0, sy / n
	}
	beta = (n*sxy - sx*sy) / den
	alpha = (sy - beta*sx) / n
	return beta, alpha
}

// calcValueFactorValue 计算价值因子原始值（技术代理，仅当无财务数据时使用）
func (pe *ProFactorEngine) calcValueFactorValue(bars []data.FactorBar) float64 {
	// 使用近期收益率作为价值代理
	return pe.calculateReturn(bars, 60) * -1 // 低涨幅 = 可能低估值
}

// calcFinancialValueFactorValue 计算价值因子原始值（真实财务，方案2）
// 价值即"便宜程度"：EP（盈利收益率=EPS/现价）与 BP（账面市值比=BPS/现价）越高 → 估值越低 → 越有吸引力。
// 无财务数据时回退到技术代理（低涨幅≈低估值），保证字段始终有值、可参与横截面标准化。
func (pe *ProFactorEngine) calcFinancialValueFactorValue(bars []data.FactorBar, fin *data.FinancialReport) float64 {
	if fin != nil && len(bars) > 0 && bars[0].Close > 0 {
		price := bars[0].Close
		ep := 0.0
		if fin.EPS > 0 {
			ep = fin.EPS / price
		}
		bp := 0.0
		if fin.BPS > 0 {
			bp = fin.BPS / price
		}
		if ep > 0 || bp > 0 {
			count := 0
			sum := 0.0
			if ep > 0 {
				sum += ep
				count++
			}
			if bp > 0 {
				sum += bp
				count++
			}
			return sum / float64(count)
		}
	}
	return pe.calcValueFactorValue(bars)
}

// calcQualityFactorValue 计算质量因子原始值（技术代理，仅当无财务数据时使用）
func (pe *ProFactorEngine) calcQualityFactorValue(bars []data.FactorBar) float64 {
	if len(bars) < 20 {
		return 0
	}

	// 使用成交额稳定性作为质量代理
	recentAmount := 0.0
	olderAmount := 0.0

	for i := 0; i < 10 && i < len(bars); i++ {
		recentAmount += bars[i].Amount
	}
	for i := 10; i < 20 && i < len(bars); i++ {
		olderAmount += bars[i].Amount
	}

	if olderAmount < 1e-10 {
		return 0
	}

	// 成交额稳定或增长 = 质量好
	return (recentAmount/10)/(olderAmount/10) - 1
}

// calcFinancialQualityFactorValue 计算质量因子原始值（真实财务，方案1）
// 质量 = 高ROE + 高毛利率 + 高净利率 + 低资产负债率（反向）。以上指标均为%，直接相加取平均。
// 无财务数据时回退到技术代理，保证字段始终有值、可参与横截面标准化。
func (pe *ProFactorEngine) calcFinancialQualityFactorValue(bars []data.FactorBar, fin *data.FinancialReport) float64 {
	if fin != nil {
		sum := 0.0
		count := 0
		if fin.Roe != 0 {
			sum += fin.Roe
			count++
		}
		if fin.GrossMargin != 0 {
			sum += fin.GrossMargin
			count++
		}
		if fin.NetMargin != 0 {
			sum += fin.NetMargin
			count++
		}
		if fin.DebtRatio > 0 {
			sum -= fin.DebtRatio
			count++
		}
		if count > 0 {
			return sum / float64(count)
		}
	}
	return pe.calcQualityFactorValue(bars)
}

// calcMomentumFactorValue 计算动量因子原始值
func (pe *ProFactorEngine) calcMomentumFactorValue(bars []data.FactorBar) float64 {
	// 使用12月动量
	return pe.calculateReturn(bars, 120)
}

// calcLowVolatilityFactorValue 计算低波动因子原始值
func (pe *ProFactorEngine) calcLowVolatilityFactorValue(bars []data.FactorBar) float64 {
	// 使用历史波动率（取负值，波动越低值越高）
	return -pe.calculateVolatility(bars, 60)
}

// calcStabilityFactorValue 计算稳定性因子原始值
func (pe *ProFactorEngine) calcStabilityFactorValue(bars []data.FactorBar) float64 {
	if len(bars) < 20 {
		return 0
	}

	// 使用成交量稳定性
	volumes := make([]float64, 20)
	for i := 0; i < 20 && i < len(bars); i++ {
		volumes[i] = bars[i].Volume
	}

	// 计算变异系数
	mean := 0.0
	for _, v := range volumes {
		mean += v
	}
	mean /= float64(len(volumes))

	if mean < 1e-10 {
		return 0
	}

	variance := 0.0
	for _, v := range volumes {
		diff := v - mean
		variance += diff * diff
	}

	stdDev := math.Sqrt(variance / float64(len(volumes)-1))
	cv := stdDev / mean

	// CV越低，稳定性越好
	return -cv
}

// calcLiquidityFactorValue 计算流动性因子原始值
func (pe *ProFactorEngine) calcLiquidityFactorValue(bars []data.FactorBar) float64 {
	if len(bars) < 10 {
		return 0
	}

	// 使用平均成交额
	totalAmount := 0.0
	for i := 0; i < 10 && i < len(bars); i++ {
		totalAmount += bars[i].Amount
	}

	return totalAmount / 10
}

// calcProValueFactor 计算Pro版价值因子得分
func (pe *ProFactorEngine) calcProValueFactor(bars []data.FactorBar, stats map[FactorID]FactorStats, fin *data.FinancialReport) FactorScore {
	s := stats[FactorValue]
	// 价值因子：真实财务下越高（越便宜）分越高；技术代理下同样如此。
	value := pe.calcFinancialValueFactorValue(bars, fin)
	score := zScoreTo0100(value, s.Mean, s.StdDev)

	breakdown := map[string]float64{
		"估值信号": roundScore(score),
	}
	if fin != nil {
		if fin.EPS > 0 {
			breakdown["EP(盈利率%)"] = roundScore((fin.EPS / bars[0].Close) * 100)
		}
		if fin.BPS > 0 {
			breakdown["BP(账面比%)"] = roundScore((fin.BPS / bars[0].Close) * 100)
		}
	}

	return FactorScore{
		FactorID:   FactorValue,
		FactorName: "价值 (Value)",
		Score:      roundScore(score),
		Breakdown:  breakdown,
	}
}

// calcProQualityFactor 计算Pro版质量因子得分
func (pe *ProFactorEngine) calcProQualityFactor(bars []data.FactorBar, stats map[FactorID]FactorStats, fin *data.FinancialReport) FactorScore {
	s := stats[FactorQuality]
	quality := pe.calcFinancialQualityFactorValue(bars, fin)
	score := zScoreTo0100(quality, s.Mean, s.StdDev)

	breakdown := map[string]float64{
		"质量指标": roundScore(score),
	}
	if fin != nil {
		if fin.Roe != 0 {
			breakdown["ROE"] = roundScore(fin.Roe)
		}
		if fin.GrossMargin != 0 {
			breakdown["毛利率"] = roundScore(fin.GrossMargin)
		}
		if fin.NetMargin != 0 {
			breakdown["净利率"] = roundScore(fin.NetMargin)
		}
		if fin.DebtRatio > 0 {
			breakdown["负债率"] = roundScore(fin.DebtRatio)
		}
	}

	return FactorScore{
		FactorID:   FactorQuality,
		FactorName: "质量 (Quality)",
		Score:      roundScore(score),
		Breakdown:  breakdown,
	}
}

// calcProMomentumFactor 计算Pro版动量因子得分
func (pe *ProFactorEngine) calcProMomentumFactor(bars []data.FactorBar, stats map[FactorID]FactorStats) FactorScore {
	s := stats[FactorMomentum]

	// 多周期动量加权
	mom1M := pe.calculateReturn(bars, 20)
	mom3M := pe.calculateReturn(bars, 60)
	mom6M := pe.calculateReturn(bars, 120)

	// 加权合成：短期权重低，长期权重高
	momentum := mom1M*0.2 + mom3M*0.3 + mom6M*0.5
	score := zScoreTo0100(momentum, s.Mean, s.StdDev)

	return FactorScore{
		FactorID:   FactorMomentum,
		FactorName: "动量 (Momentum)",
		Score:      roundScore(score),
		Breakdown: map[string]float64{
			"1月动量": roundScore(zScoreTo0100(mom1M, s.Mean, s.StdDev)),
			"3月动量": roundScore(zScoreTo0100(mom3M, s.Mean, s.StdDev)),
			"6月动量": roundScore(zScoreTo0100(mom6M, s.Mean, s.StdDev)),
		},
	}
}

// calcProLowVolatilityFactor 计算Pro版低波动因子得分
func (pe *ProFactorEngine) calcProLowVolatilityFactor(bars []data.FactorBar, stats map[FactorID]FactorStats) FactorScore {
	s := stats[FactorLowVolatility]
	volatility := pe.calculateVolatility(bars, 60)
	score := inverseZScoreTo0100(volatility, s.Mean, s.StdDev)

	return FactorScore{
		FactorID:   FactorLowVolatility,
		FactorName: "低波动 (Low Volatility)",
		Score:      roundScore(score),
		Breakdown: map[string]float64{
			"历史波动率": roundScore(score),
		},
	}
}

// calcProStabilityFactor 计算Pro版稳定性因子得分
func (pe *ProFactorEngine) calcProStabilityFactor(bars []data.FactorBar, stats map[FactorID]FactorStats) FactorScore {
	s := stats[FactorEarningsStability]
	score := zScoreTo0100(pe.calcStabilityFactorValue(bars), s.Mean, s.StdDev)

	return FactorScore{
		FactorID:   FactorEarningsStability,
		FactorName: "稳定性 (Stability)",
		Score:      roundScore(score),
		Breakdown: map[string]float64{
			"成交稳定性": roundScore(score),
		},
	}
}

// calcProLiquidityFactor 计算Pro版流动性因子得分
func (pe *ProFactorEngine) calcProLiquidityFactor(bars []data.FactorBar, stats map[FactorID]FactorStats) FactorScore {
	s := stats[FactorLiquidity]
	score := zScoreTo0100(pe.calcLiquidityFactorValue(bars), s.Mean, s.StdDev)

	return FactorScore{
		FactorID:   FactorLiquidity,
		FactorName: "流动性 (Liquidity)",
		Score:      roundScore(score),
		Breakdown: map[string]float64{
			"成交活跃度": roundScore(score),
		},
	}
}

// ==================== 加权评分 ====================

// 中小盘流通市值硬筛选区间（元）。用于剔除超大盘(弹性小)与微盘(流动性差、波动剧)。
const (
	minFloatCap = 20e8  // 20 亿元
	maxFloatCap = 200e8 // 200 亿元
)

// floatShares 返回标的可用的股本（优先流通股本，缺失时以总股本兜底；均缺失返回 0）。
func (pe *ProFactorEngine) floatShares(symbol string) float64 {
	fin := pe.finCache[symbol]
	if fin == nil {
		return 0
	}
	if fin.FloatShares > 0 {
		return fin.FloatShares
	}
	return fin.TotalShares
}

// computeWeightedProScores 计算Pro版加权总分
func (pe *ProFactorEngine) computeWeightedProScores(
	allScores map[string]map[FactorID]FactorScore,
	weights map[FactorID]float64,
	minScore float64,
	symbols []data.StockSymbolInfo,
) []ProStockScore {
	var results []ProStockScore

	for _, sym := range symbols {
		bars := pe.barsCache[sym.Symbol]
		if len(bars) < 20 {
			continue
		}

		// 流通市值硬筛选（中小盘 20~200 亿）：最新收盘价 × 流通股本（无股本则以总股本兜底）。
		// 无股本/无价格资料时放行（不因缺数据误伤正常选股），只过滤能明确判断市值区间的票。
		price := bars[0].Close
		if shares := pe.floatShares(sym.Symbol); shares > 0 && price > 0 {
			cap := price * shares
			if cap < minFloatCap || cap > maxFloatCap {
				continue
			}
		}

		factorScores, ok := allScores[sym.Symbol]
		if !ok {
			continue
		}

		// 加权计算总分
		var totalScore float64
		for factorID, weight := range weights {
			if fs, ok := factorScores[factorID]; ok {
				totalScore += fs.Score * weight
			}
		}
		totalScore = roundScore(totalScore)

		// AI评分拆解：总分→1-10 AI Score，因子拆成正向(提升)/负向(拖累)贡献
		aiScore := toAIScore(totalScore)
		explain := explainFactors(factorScores, weights)

		// 过滤低于门槛的
		if minScore > 0 && totalScore < minScore {
			continue
		}

		// 计算Pro版特有指标
		mom1M := pe.calculateReturn(bars, 20) * 100
		mom3M := pe.calculateReturn(bars, 60) * 100
		mom6M := pe.calculateReturn(bars, 120) * 100
		mom12M := pe.calculateReturn(bars, 252) * 100
		volatility := pe.calculateVolatility(bars, 60)
		amplitude := pe.calculateAverageAmplitude(bars)
		turnoverRate := pe.calculateAverageTurnoverRate(bars)
		liquidity := pe.calcLiquidityFactorValue(bars)

		// 生成选股理由和警告
		reasons := pe.generateProReasons(factorScores)
		warnings := pe.generateProWarnings(factorScores)

		results = append(results, ProStockScore{
			Code:          sym.Symbol,
			Name:          sym.Name,
			Market:        sym.Market,
			Price:         bars[0].Close,
			ChangePct:     bars[0].PctChg,
			TotalScore:    totalScore,
			FactorScores:  factorScores,
			Ranking:       0,
			Reasons:       reasons,
			Warnings:      warnings,
			Momentum1M:    roundScore(mom1M),
			Momentum3M:    roundScore(mom3M),
			Momentum6M:    roundScore(mom6M),
			Momentum12M:   roundScore(mom12M),
			Volatility:    roundScore(volatility),
			Amplitude:     roundScore(amplitude),
			TurnoverRate:  roundScore(turnoverRate),
			Liquidity:     roundScore(liquidity),
			AIScore:       aiScore,
			FactorExplain: explain,
		})
	}

	return results
}

// ==================== AI Score + 因子正负拆解（对标 PanWatch signal_explain） ====================

// toAIScore 把 0-100 总分映射为 1-10 的 AI 评分（TotalScore/10 四舍五入后 clamp 到 [1,10]）。
func toAIScore(total float64) int {
	s := int(math.Round(total / 10.0))
	if s < 1 {
		return 1
	}
	if s > 10 {
		return 10
	}
	return s
}

// explainFactors 把因子得分拆成正向(高于中性50=提升总分)与负向(低于中性50=拖累总分)两组。
// 贡献 = (Score-50)×权重，衡量该因子把总分从中性50推离的幅度；当权重和为1时 Σ贡献 = TotalScore-50。
// 对标 PanWatch：正向=利好因子(绿)，负向=风险因子(红)，各按贡献绝对值排序取前5。
func explainFactors(fs map[FactorID]FactorScore, weights map[FactorID]float64) FactorExplain {
	const eps = 0.3 // 低于该绝对值的贡献视为噪声，不展示
	var pos, neg []FactorContribution
	for id, f := range fs {
		w := weights[id]
		if w <= 0 {
			continue
		}
		c := (f.Score - 50) * w
		item := FactorContribution{
			FactorID:     id,
			FactorName:   f.FactorName,
			Score:        f.Score,
			Contribution: roundScore(c),
		}
		if c > eps {
			pos = append(pos, item)
		} else if c < -eps {
			neg = append(neg, item)
		}
	}
	sort.Slice(pos, func(i, j int) bool { return pos[i].Contribution > pos[j].Contribution })
	sort.Slice(neg, func(i, j int) bool { return neg[i].Contribution < neg[j].Contribution })
	if len(pos) > 5 {
		pos = pos[:5]
	}
	if len(neg) > 5 {
		neg = neg[:5]
	}
	return FactorExplain{Positive: pos, Negative: neg}
}

// calculateAverageAmplitude 计算平均振幅
func (pe *ProFactorEngine) calculateAverageAmplitude(bars []data.FactorBar) float64 {
	if len(bars) < 10 {
		return 0
	}

	total := 0.0
	count := 0
	for i := 0; i < 10 && i < len(bars); i++ {
		if bars[i].Amplitude > 0 {
			total += bars[i].Amplitude
			count++
		}
	}

	if count == 0 {
		return 0
	}
	return total / float64(count)
}

// calculateAverageTurnoverRate 计算平均换手率
func (pe *ProFactorEngine) calculateAverageTurnoverRate(bars []data.FactorBar) float64 {
	if len(bars) < 10 {
		return 0
	}

	totalVolume := 0.0
	totalAmount := 0.0
	for i := 0; i < 10 && i < len(bars); i++ {
		totalVolume += bars[i].Volume
		totalAmount += bars[i].Amount
	}

	if totalAmount < 1e-10 {
		return 0
	}

	// 简化换手率 = 成交量/成交额 * 价格
	avgPrice := totalAmount / totalVolume
	if avgPrice < 1e-10 {
		return 0
	}

	return totalVolume / 10
}

// generateProReasons 生成Pro版选股理由
func (pe *ProFactorEngine) generateProReasons(factorScores map[FactorID]FactorScore) []string {
	reasons := make([]string, 0)

	for id, fs := range factorScores {
		if fs.Score >= 75 {
			switch id {
			case FactorValue:
				reasons = append(reasons, "估值具有吸引力")
			case FactorQuality:
				reasons = append(reasons, "基本面质量优秀")
			case FactorMomentum:
				reasons = append(reasons, "价格动量强劲")
			case FactorLowVolatility:
				reasons = append(reasons, "波动性较低")
			case FactorEarningsStability:
				reasons = append(reasons, "成交稳定性好")
			case FactorLiquidity:
				reasons = append(reasons, "流动性充足")
			}
		}
	}

	if len(reasons) > 5 {
		reasons = reasons[:5]
	}
	return reasons
}

// generateProWarnings 生成Pro版风险提示
func (pe *ProFactorEngine) generateProWarnings(factorScores map[FactorID]FactorScore) []string {
	warnings := make([]string, 0)

	for id, fs := range factorScores {
		if fs.Score < 40 {
			switch id {
			case FactorValue:
				warnings = append(warnings, "估值相对偏高")
			case FactorQuality:
				warnings = append(warnings, "质量指标偏弱")
			case FactorMomentum:
				warnings = append(warnings, "动量信号较弱")
			case FactorLowVolatility:
				warnings = append(warnings, "波动性偏高")
			case FactorEarningsStability:
				warnings = append(warnings, "成交稳定性不足")
			case FactorLiquidity:
				warnings = append(warnings, "流动性偏低")
			}
		}
	}

	if len(warnings) > 4 {
		warnings = warnings[:4]
	}
	return warnings
}

// ==================== 因子评估与学习 ====================

// RecordScreeningEvaluation 记录因子选股评估结果，用于后续智能权重学习
// topResults: 本次选股的 Top N 结果
// factorScores: 所有股票的因子得分
func (pe *ProFactorEngine) RecordScreeningEvaluation(
	topResults []ProStockScore,
	allFactorScores map[string]map[FactorID]FactorScore,
) {
	if len(topResults) == 0 {
		return
	}

	// 计算每个因子的 Top Decile 选股得分表现
	factorPerf := make(map[FactorID][]float64)

	for _, result := range topResults {
		for factorID, fs := range result.FactorScores {
			factorPerf[factorID] = append(factorPerf[factorID], fs.Score)
		}
	}

	// 记录评估到智能组合器
	for factorID, scores := range factorPerf {
		if len(scores) == 0 {
			continue
		}

		avgScore := 0.0
		for _, s := range scores {
			avgScore += s
		}
		avgScore /= float64(len(scores))

		hitRate := 0.0
		for _, s := range scores {
			if s >= 60 {
				hitRate++
			}
		}
		hitRate /= float64(len(scores))

		pe.smartComposer.RecordEvaluation(FactorEvaluation{
			FactorID:        factorID,
			AvgScore:        avgScore,
			TopDecileReturn: (avgScore - 50) / 100, // 简化映射
			HitRate:         hitRate,
			EvaluatedAt:     time.Now(),
			MarketState:     pe.marketStateString(),
		})
	}

	log.Printf("[ProEngine] Recorded evaluation for %d factors", len(factorPerf))
}

// GetSmartComposer 获取智能因子组合器
func (pe *ProFactorEngine) GetSmartComposer() *SmartFactorComposer {
	return pe.smartComposer
}

// marketStateString 返回当前市场状态字符串
func (pe *ProFactorEngine) marketStateString() string {
	if pe.marketState != nil {
		return pe.marketState.Regime
	}
	return "unknown"
}
