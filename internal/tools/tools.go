package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"strings"
	"time"

	"github.com/quantpilot/quantpilot/internal/backtest"
	"github.com/quantpilot/quantpilot/internal/data"
	"github.com/quantpilot/quantpilot/internal/portfolio"
	"github.com/quantpilot/quantpilot/internal/screener"
	"github.com/quantpilot/quantpilot/internal/tdx"
	"github.com/quantpilot/quantpilot/internal/tradeapproval"
	"github.com/quantpilot/quantpilot/internal/util"
)

// Tool 工具接口
type Tool interface {
	Name() string
	Description() string
	GetDefinition() ToolDefinition
	Execute(ctx context.Context, args map[string]interface{}) (interface{}, error)
}

// ToolDefinition 工具定义（用于 LLM function calling）
type ToolDefinition struct {
	Type     string      `json:"type"`
	Function FunctionDef `json:"function"`
}

// FunctionDef 函数定义
type FunctionDef struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Parameters  interface{} `json:"parameters"`
}

// ToolCallRecord 工具调用记录
type ToolCallRecord struct {
	ToolName   string      `json:"tool_name"`
	ToolCallID string      `json:"tool_call_id"`
	Arguments  interface{} `json:"arguments"`
	Result     interface{} `json:"result,omitempty"`
	Error      string      `json:"error,omitempty"`
	Success    bool        `json:"success"`
	Timestamp  time.Time   `json:"timestamp"`
}

// ==================== MarketDataTool ====================

// MarketDataTool 市场数据工具
type MarketDataTool struct {
	duckdbManager *data.DuckDBManager
}

// NewMarketDataTool 创建市场数据工具
func NewMarketDataTool(duckdbManager *data.DuckDBManager) *MarketDataTool {
	return &MarketDataTool{
		duckdbManager: duckdbManager,
	}
}

func (t *MarketDataTool) Name() string {
	return "get_market_data"
}

func (t *MarketDataTool) Description() string {
	return "Get historical market data for a given instrument including price, volume, and basic technical indicators"
}

func (t *MarketDataTool) GetDefinition() ToolDefinition {
	return ToolDefinition{
		Type: "function",
		Function: FunctionDef{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"market": map[string]interface{}{
						"type":        "string",
						"description": "Market identifier (US, CN, HK)",
						"enum":        []string{"US", "CN", "HK"},
					},
					"instrument_id": map[string]interface{}{
						"type":        "string",
						"description": "Instrument ID in format like US:AAPL",
					},
					"days": map[string]interface{}{
						"type":        "integer",
						"description": "Number of trading days to look back (default 30)",
					},
					"include_indicators": map[string]interface{}{
						"type":        "boolean",
						"description": "Include technical indicators (SMA, RSI, etc.)",
					},
				},
				"required": []string{"market", "instrument_id"},
			},
		},
	}
}

// MarketDataResult 市场数据返回结构
type MarketDataResult struct {
	Market       string               `json:"market"`
	InstrumentID string               `json:"instrument_id"`
	Period       string               `json:"period"`
	DataPoints   []MarketDataPoint    `json:"data_points"`
	Indicators   *TechnicalIndicators `json:"indicators,omitempty"`
}

// MarketDataPoint 数据点
type MarketDataPoint struct {
	Date   string  `json:"date"`
	Open   float64 `json:"open"`
	High   float64 `json:"high"`
	Low    float64 `json:"low"`
	Close  float64 `json:"close"`
	Volume int64   `json:"volume"`
}

// TechnicalIndicators 技术指标
type TechnicalIndicators struct {
	SMA20      float64 `json:"sma_20"`
	SMA50      float64 `json:"sma_50"`
	RSI14      float64 `json:"rsi_14"`
	Volatility float64 `json:"volatility"`
}

func (t *MarketDataTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	market, ok := args["market"].(string)
	if !ok || market == "" {
		return nil, fmt.Errorf("market is required (US, CN, HK)")
	}

	instrumentID, ok := args["instrument_id"].(string)
	if !ok || instrumentID == "" {
		return nil, fmt.Errorf("instrument_id is required")
	}

	days := 30
	if d, ok := args["days"].(float64); ok {
		days = int(d)
	}

	includeIndicators := false
	if inc, ok := args["include_indicators"].(bool); ok {
		includeIndicators = inc
	}

	// 从 DuckDB 获取数据
	prices, err := t.duckdbManager.GetRecentPrices(ctx, market, instrumentID, days)
	if err != nil {
		return nil, fmt.Errorf("failed to get market data: %w", err)
	}

	// Data Integrity Guard：数据缺失禁止AI推断
	// 无数据时返回 TASK_BLOCKED，严禁返回空数据让LLM自行推测
	if len(prices) == 0 {
		guard := data.NewDataIntegrityGuard(t.duckdbManager)
		status := guard.CheckMarketDataIntegrity(ctx, market, instrumentID, days)
		log.Printf("[MarketDataTool] TASK_BLOCKED: %s/%s 无行情数据: %s", market, instrumentID, status.Reason)
		return map[string]interface{}{
			"status":  "TASK_BLOCKED",
			"reason":  status.Reason,
			"message": "行情数据缺失，禁止AI推断。请先通过数据维护模块同步行情数据。",
		}, nil
	}

	// 转换为返回格式
	dataPoints := make([]MarketDataPoint, len(prices))
	for i, p := range prices {
		dataPoints[i] = MarketDataPoint{
			Date:   p.TradeDate.Format("2006-01-02"),
			Open:   p.Open,
			High:   p.High,
			Low:    p.Low,
			Close:  p.Close,
			Volume: p.Volume,
		}
	}

	result := &MarketDataResult{
		Market:       market,
		InstrumentID: instrumentID,
		Period:       fmt.Sprintf("last_%d_days", days),
		DataPoints:   dataPoints,
	}

	if includeIndicators && len(dataPoints) >= 20 {
		result.Indicators = calculateIndicators(dataPoints)
	}

	return result, nil
}

// ==================== SearchMarketTool ====================

// SearchMarketTool 市场搜索工具
type SearchMarketTool struct {
	duckdbManager *data.DuckDBManager
}

// NewSearchMarketTool 创建市场搜索工具
func NewSearchMarketTool(duckdbManager *data.DuckDBManager) *SearchMarketTool {
	return &SearchMarketTool{
		duckdbManager: duckdbManager,
	}
}

func (t *SearchMarketTool) Name() string {
	return "search_market"
}

func (t *SearchMarketTool) Description() string {
	return "Search for stocks by name, symbol, or sector in the market database"
}

func (t *SearchMarketTool) GetDefinition() ToolDefinition {
	return ToolDefinition{
		Type: "function",
		Function: FunctionDef{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"market": map[string]interface{}{
						"type":        "string",
						"description": "Market to search in",
						"enum":        []string{"US", "CN", "HK", "all"},
					},
					"query": map[string]interface{}{
						"type":        "string",
						"description": "Search query (company name, symbol, sector)",
					},
					"limit": map[string]interface{}{
						"type":        "integer",
						"description": "Maximum number of results (default 20)",
					},
				},
				"required": []string{"query"},
			},
		},
	}
}

// SearchResult 搜索结果
type SearchResult struct {
	Total   int          `json:"total"`
	Results []SearchItem `json:"results"`
}

// SearchItem 搜索项
type SearchItem struct {
	InstrumentID string  `json:"instrument_id"`
	Symbol       string  `json:"symbol"`
	Name         string  `json:"name"`
	Sector       string  `json:"sector"`
	LastClose    float64 `json:"last_close"`
}

func (t *SearchMarketTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	query, ok := args["query"].(string)
	if !ok || query == "" {
		return nil, fmt.Errorf("query is required")
	}

	market := "all"
	if m, ok := args["market"].(string); ok && m != "" {
		market = m
	}

	limit := 20
	if l, ok := args["limit"].(float64); ok && l > 0 {
		limit = int(l)
	}

	// 优先使用 SearchInstruments 搜索具体股票
	var items []SearchItem

	searchResults, err := t.duckdbManager.SearchInstruments(ctx, query, market, limit)
	if err != nil {
		log.Printf("[SearchMarketTool] SearchInstruments failed: %v", err)
		return nil, fmt.Errorf("search_market failed: %w", err)
	}

	if len(searchResults) == 0 {
		return &SearchResult{
			Total:   0,
			Results: []SearchItem{},
		}, nil
	}

	for _, r := range searchResults {
		if len(items) >= limit {
			break
		}
		items = append(items, SearchItem{
			InstrumentID: r.Symbol,
			Symbol:       r.Symbol,
			Name:         r.Name,
			Sector:       r.Industry,
			LastClose:    0,
		})
	}
	return &SearchResult{
		Total:   len(items),
		Results: items,
	}, nil
}

// ==================== PortfolioOptimizerTool ====================

// PortfolioOptimizerTool 投资组合优化工具
type PortfolioOptimizerTool struct {
	duckdbManager *data.DuckDBManager
}

// NewPortfolioOptimizerTool 创建投资组合优化工具
func NewPortfolioOptimizerTool(duckdbManager *data.DuckDBManager) *PortfolioOptimizerTool {
	return &PortfolioOptimizerTool{
		duckdbManager: duckdbManager,
	}
}

func (t *PortfolioOptimizerTool) Name() string {
	return "optimize_portfolio"
}

func (t *PortfolioOptimizerTool) Description() string {
	return "Optimize portfolio allocation using the shared portfolio engine (mvo / risk_parity / risk_budget / auto best-by-Sharpe) based on real return covariance"
}

func (t *PortfolioOptimizerTool) GetDefinition() ToolDefinition {
	return ToolDefinition{
		Type: "function",
		Function: FunctionDef{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"assets": map[string]interface{}{
						"type":        "array",
						"description": "参与优化的资产代码列表（至少2个，如 sh600000、sz000001）",
						"items":       map[string]interface{}{"type": "string"},
					},
					"method": map[string]interface{}{
						"type":        "string",
						"description": "优化算法：mvo（均值方差）/ risk_parity（风险平价，默认）/ risk_budget（风险预算）/ auto（多策略自动选优，按夏普取优）",
						"enum":        []string{"mvo", "risk_parity", "risk_budget", "auto"},
					},
					"days": map[string]interface{}{
						"type":        "number",
						"description": "收益回看窗口（交易日，默认90）",
					},
					"max_single": map[string]interface{}{
						"type":        "number",
						"description": "单资产权重上限（0-1，默认0.30）",
					},
				},
				"required": []string{"assets"},
			},
		},
	}
}

// PortfolioResult 组合优化结果
type PortfolioResult struct {
	Assets           []string           `json:"assets"`
	Weights          map[string]float64 `json:"weights"`
	ExpectedReturn   float64            `json:"expected_return"`
	ExpectedRisk     float64            `json:"expected_risk"`
	SharpeRatio      float64            `json:"sharpe_ratio"`
	OptimizationType string             `json:"optimization_type"`
}

func (t *PortfolioOptimizerTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	assetsRaw, ok := args["assets"].([]interface{})
	if !ok || len(assetsRaw) == 0 {
		return nil, fmt.Errorf("assets list is required")
	}

	// 转换资产列表
	assets := make([]string, 0, len(assetsRaw))
	for _, a := range assetsRaw {
		if s, ok := a.(string); ok && strings.TrimSpace(s) != "" {
			assets = append(assets, strings.TrimSpace(s))
		}
	}

	if len(assets) < 2 {
		return nil, fmt.Errorf("need at least 2 assets for optimization")
	}

	method := "risk_parity"
	if m, ok := args["method"].(string); ok && m != "" {
		method = strings.ToLower(m)
	}
	days := 90
	if d, ok := args["days"].(float64); ok && d > 0 {
		days = int(d)
	}
	maxSingle := 0.30
	if ms, ok := args["max_single"].(float64); ok && ms > 0 {
		maxSingle = ms
	}

	return t.optimizePortfolio(ctx, assets, method, days, maxSingle), nil
}

// optimizePortfolio 执行组合优化。
// 与"投资组合中心"页面共享同一算法层（internal/portfolio）：
// 输入来自真实日K（公共交易日对齐），显式算法走 portfolio.Optimize，
// auto 走 portfolio.SelectBestStrategy 多策略自动选优。严禁伪造数据。
func (t *PortfolioOptimizerTool) optimizePortfolio(ctx context.Context, codes []string, method string, days int, maxSingle float64) *PortfolioResult {
	// 归一化算法标识
	switch method {
	case "mvo", "risk_parity", "risk_budget":
	default:
		method = "risk_parity"
	}
	if maxSingle <= 0 || maxSingle > 1 {
		maxSingle = 0.30
	}

	// 1) 按代码推断市场，拉取真实日K并按公共交易日对齐
	markets := make([]string, len(codes))
	for i, c := range codes {
		markets[i] = portfolio.InferMarket(c)
	}
	aligned := portfolio.BuildAlignedSeries(ctx, t.duckdbManager, codes, markets, days)
	if len(aligned) < 2 {
		log.Printf("[PortfolioOptimizer] No valid assets with sufficient data, returning empty result")
		return &PortfolioResult{
			Assets:           []string{},
			Weights:          map[string]float64{},
			ExpectedReturn:   0,
			ExpectedRisk:     0,
			SharpeRatio:      0,
			OptimizationType: "no_data",
		}
	}

	n := len(aligned)
	codes2 := make([]string, n)
	rets := make([][]float64, n)
	expRet := make([]float64, n)
	expMap := map[string]float64{}
	for i, a := range aligned {
		codes2[i] = a.Code
		rets[i] = a.Series
		expRet[i] = a.Mean // 日均收益
		expMap[a.Code] = a.Mean
	}

	var optRes portfolio.OptimizationResult
	optType := method

	if method != "auto" {
		// 显式算法：与"投资组合中心"页面同入口 portfolio.Optimize
		lo := make([]float64, n)
		hi := make([]float64, n)
		for i := 0; i < n; i++ {
			hi[i] = maxSingle
		}
		res, err := portfolio.Optimize(portfolio.OptimizeRequest{
			Method:       portfolio.OptimizeMethod(method),
			Cov:          portfolio.EstimateCovariance(rets),
			Returns:      expRet,
			MinWeight:    lo,
			MaxWeight:    hi,
			TotalWeight:  0.9,
			RiskAversion: 4.0,
		})
		if err != nil || res == nil {
			log.Printf("[PortfolioOptimizer] optimization failed: %v", err)
			return &PortfolioResult{Assets: []string{}, Weights: map[string]float64{}, OptimizationType: "error"}
		}
		optRes = portfolio.ToMapResult(res, codes2)
	} else {
		// auto：多策略自动选优（与上层 API 共享），按夏普取优
		input := portfolio.OptimizationInput{
			Assets:          codes2,
			ExpectedReturns: expMap,
			CovMatrix:       portfolio.EstimateCovariance(rets),
			Constraints: portfolio.OptimizationConstraints{
				MaxWeight:    maxSingle,
				MinWeight:    0,
				RiskFreeRate: 0.02,
			},
		}
		var bestStrategy portfolio.OptimizerStrategy
		optRes, bestStrategy = portfolio.SelectBestStrategy(input)
		optType = string(bestStrategy)
	}

	return &PortfolioResult{
		Assets:           codes2,
		Weights:          toWeightMap(codes2, optRes.Weights),
		ExpectedReturn:   optRes.ExpectedReturn,
		ExpectedRisk:     optRes.ExpectedVolatility,
		SharpeRatio:      optRes.SharpeRatio,
		OptimizationType: optType,
	}
}

// toWeightMap 将代码->权重映射拷贝为独立 map。
func toWeightMap(codes []string, w map[string]float64) map[string]float64 {
	out := make(map[string]float64, len(w))
	for _, c := range codes {
		if v, ok := w[c]; ok {
			out[c] = v
		}
	}
	return out
}

// max 取最大值
func max(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

// ==================== GetMarketStatsTool ====================

// GetMarketStatsTool 市场统计工具
type GetMarketStatsTool struct {
	duckdbManager *data.DuckDBManager
}

// NewGetMarketStatsTool 创建市场统计工具
func NewGetMarketStatsTool(duckdbManager *data.DuckDBManager) *GetMarketStatsTool {
	return &GetMarketStatsTool{
		duckdbManager: duckdbManager,
	}
}

func (t *GetMarketStatsTool) Name() string {
	return "get_market_stats"
}

func (t *GetMarketStatsTool) Description() string {
	return "Get market statistics including sector performance, trading volume, and market breadth"
}

func (t *GetMarketStatsTool) GetDefinition() ToolDefinition {
	return ToolDefinition{
		Type: "function",
		Function: FunctionDef{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"market": map[string]interface{}{
						"type":        "string",
						"description": "Market identifier",
						"enum":        []string{"US", "CN", "HK"},
					},
				},
				"required": []string{"market"},
			},
		},
	}
}

func (t *GetMarketStatsTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	market, ok := args["market"].(string)
	if !ok || market == "" {
		return nil, fmt.Errorf("market is required")
	}

	stats, err := t.duckdbManager.GetMarketStats(ctx, market)
	if err != nil {
		return nil, fmt.Errorf("failed to get market stats: %w", err)
	}

	return map[string]interface{}{
		"market": market,
		"stats":  stats,
	}, nil
}

// ==================== ScreenMarketTool ====================

// ScreenMarketTool 智能选股工具 - 供Agent调用选股引擎
type ScreenMarketTool struct {
	screenerService *screener.ScreenerService
	tradeablePool   *screener.TradeablePool
	profileProvider func() *data.InvestorProfile
}

// NewScreenMarketTool 创建智能选股工具
func NewScreenMarketTool(ss *screener.ScreenerService, tp *screener.TradeablePool, profileProvider func() *data.InvestorProfile) *ScreenMarketTool {
	return &ScreenMarketTool{
		screenerService: ss,
		tradeablePool:   tp,
		profileProvider: profileProvider,
	}
}

func (t *ScreenMarketTool) Name() string {
	return "screen_market"
}

func (t *ScreenMarketTool) Description() string {
	return "Run intelligent stock screening using multi-factor models with dynamic weights based on investor profile and market conditions. Returns scored stocks with factor analysis."
}

func (t *ScreenMarketTool) GetDefinition() ToolDefinition {
	return ToolDefinition{
		Type: "function",
		Function: FunctionDef{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"strategy": map[string]interface{}{
						"type":        "string",
						"description": "Strategy template: balanced, defensive, growth",
						"enum":        []string{"balanced", "defensive", "growth"},
					},
					"market": map[string]interface{}{
						"type":        "string",
						"description": "Market to screen: CN for A-shares",
						"enum":        []string{"CN", "SH", "SZ", "all"},
					},
					"max_results": map[string]interface{}{
						"type":        "integer",
						"description": "Maximum number of results to return (default 50)",
					},
					"submit_to_pool": map[string]interface{}{
						"type":        "boolean",
						"description": "Whether to submit results to tradeable stock pool for review",
					},
				},
				"required": []string{"strategy", "market"},
			},
		},
	}
}

func (t *ScreenMarketTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	if t.screenerService == nil {
		return nil, fmt.Errorf("screener service not available")
	}

	strategy, _ := args["strategy"].(string)
	if strategy == "" {
		strategy = "balanced"
	}

	market, _ := args["market"].(string)
	if market == "" {
		market = "CN"
	}

	maxResults := 50
	if mr, ok := args["max_results"].(float64); ok && mr > 0 {
		maxResults = int(mr)
	}

	submitToPool := false
	if sp, ok := args["submit_to_pool"].(bool); ok {
		submitToPool = sp
	}

	req := screener.ScreeningRequest{
		StrategyID: strategy,
		Market:     market,
		MaxResults: maxResults,
		MinScore:   0,
	}

	// 使用用户画像启用智能因子组合
	if t.profileProvider != nil {
		profile := t.profileProvider()
		if profile != nil {
			req.InvestorProfile = profile
			log.Printf("[ScreenMarketTool] Using profile: risk=%s, style=%s",
				profile.RiskTolerance, profile.InvestmentStyle)
		}
	}

	result, err := t.screenerService.ScreenStock(req)
	if err != nil {
		return nil, fmt.Errorf("screening failed: %w", err)
	}

	// 提交到股票池
	var submittedCount int
	if submitToPool && t.tradeablePool != nil {
		submittedCount, _ = t.tradeablePool.SubmitScreenerResult(&result, "agent", "AI Agent")
	}

	// 构建动态权重响应
	dynamicWeights := make(map[string]float64)
	for k, v := range result.DynamicWeights {
		dynamicWeights[string(k)] = v
	}

	// 构建因子健康度响应
	var factorHealthList []interface{}
	for id, fh := range result.FactorHealth {
		factorHealthList = append(factorHealthList, map[string]interface{}{
			"factorId":    string(id),
			"factorName":  fh.FactorName,
			"ic":          fh.IC,
			"icir":        fh.ICIR,
			"status":      fh.Status,
			"description": fh.Description,
		})
	}

	// 构建市场状态响应
	var marketStateMap map[string]interface{}
	if result.MarketState != nil {
		marketStateMap = map[string]interface{}{
			"regime":      result.MarketState.Regime,
			"trendScore":  result.MarketState.TrendScore,
			"volatility":  result.MarketState.Volatility,
			"breadth":     result.MarketState.Breadth,
			"description": result.MarketState.Description,
		}
	}

	// 构建选股结果列表
	var stockList []interface{}
	for _, r := range result.Results {
		var factorScoresList []interface{}
		for _, fs := range r.FactorScores {
			factorScoresList = append(factorScoresList, map[string]interface{}{
				"factorId":   string(fs.FactorID),
				"factorName": fs.FactorName,
				"score":      fs.Score,
			})
		}
		stockList = append(stockList, map[string]interface{}{
			"code":         r.Code,
			"name":         r.Name,
			"market":       r.Market,
			"totalScore":   r.TotalScore,
			"reasons":      r.Reasons,
			"warnings":     r.Warnings,
			"factorScores": factorScoresList,
		})
	}

	return map[string]interface{}{
		"strategyName":    result.StrategyName,
		"totalCount":      result.TotalCount,
		"stocks":          stockList,
		"dynamicWeights":  dynamicWeights,
		"factorHealth":    factorHealthList,
		"marketState":     marketStateMap,
		"profileSummary":  result.ProfileSummary,
		"usedSmartEngine": result.UsedSmartEngine,
		"submittedToPool": submitToPool,
		"submittedCount":  submittedCount,
	}, nil
}

// ==================== GetStockPoolTool ====================

// GetStockPoolTool 股票池查询工具 - 供Agent查询可交易股票池
type GetStockPoolTool struct {
	tradeablePool *screener.TradeablePool
}

// NewGetStockPoolTool 创建股票池查询工具
func NewGetStockPoolTool(tp *screener.TradeablePool) *GetStockPoolTool {
	return &GetStockPoolTool{
		tradeablePool: tp,
	}
}

func (t *GetStockPoolTool) Name() string {
	return "get_stock_pool"
}

func (t *GetStockPoolTool) Description() string {
	return "Get stocks from the tradeable stock pool including pending, approved, and bought stocks with their scores, reasons, and risk warnings"
}

func (t *GetStockPoolTool) GetDefinition() ToolDefinition {
	return ToolDefinition{
		Type: "function",
		Function: FunctionDef{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"status": map[string]interface{}{
						"type":        "string",
						"description": "Filter by status: PENDING, APPROVED, BOUGHT, REJECTED",
						"enum":        []string{"", "PENDING", "APPROVED", "BOUGHT", "REJECTED"},
					},
					"market": map[string]interface{}{
						"type":        "string",
						"description": "Filter by market: CN, SH, SZ",
					},
					"limit": map[string]interface{}{
						"type":        "integer",
						"description": "Maximum number of stocks to return",
					},
				},
				"required": []string{},
			},
		},
	}
}

func (t *GetStockPoolTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	if t.tradeablePool == nil {
		return nil, fmt.Errorf("tradeable pool not available")
	}

	status, _ := args["status"].(string)
	market, _ := args["market"].(string)
	limit := 100
	if l, ok := args["limit"].(float64); ok && l > 0 {
		limit = int(l)
	}

	stocks, err := t.tradeablePool.GetAllStocksWithFilter(status, market, "", "score", limit)
	if err != nil {
		return nil, fmt.Errorf("failed to get stock pool: %w", err)
	}

	var stockList []interface{}
	for _, s := range stocks {
		factorScores := make(map[string]interface{})
		if s.FactorScoresJSON != "" {
			json.Unmarshal([]byte(s.FactorScoresJSON), &factorScores)
		}

		stockList = append(stockList, map[string]interface{}{
			"stockCode":       s.StockCode,
			"stockName":       s.StockName,
			"market":          s.Market,
			"currentPrice":    s.CurrentPrice,
			"compositeScore":  s.CompositeScore,
			"factorScores":    factorScores,
			"selectionReason": s.SelectionReason,
			"riskWarning":     s.RiskWarning,
			"status":          s.Status,
			"priority":        s.Priority,
			"selectionSource": s.SelectionSource,
			"strategyId":      s.StrategyID,
			"submittedAt":     s.SubmittedAt,
		})
	}

	// 获取池汇总
	summary := t.tradeablePool.GetPoolSummary()

	return map[string]interface{}{
		"totalCount":    len(stockList),
		"pendingCount":  summary.PendingCount,
		"approvedCount": summary.ApprovedCount,
		"boughtCount":   summary.BoughtCount,
		"stocks":        stockList,
	}, nil
}

// ==================== BacktestTool ====================

// BacktestTool 回测工具
type BacktestTool struct {
	sqliteManager *data.SQLiteManager
	duckdbManager *data.DuckDBManager
}

// NewBacktestTool 创建回测工具
func NewBacktestTool(sqliteManager *data.SQLiteManager, duckdbManager *data.DuckDBManager) *BacktestTool {
	return &BacktestTool{
		sqliteManager: sqliteManager,
		duckdbManager: duckdbManager,
	}
}

func (t *BacktestTool) Name() string {
	return "run_backtest"
}

func (t *BacktestTool) Description() string {
	return "Run a backtest for a trading strategy with specified parameters"
}

func (t *BacktestTool) GetDefinition() ToolDefinition {
	return ToolDefinition{
		Type: "function",
		Function: FunctionDef{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"strategy_id": map[string]interface{}{
						"type":        "integer",
						"description": "Strategy ID to backtest",
					},
					"market": map[string]interface{}{
						"type":        "string",
						"description": "Market identifier",
					},
					"start_date": map[string]interface{}{
						"type":        "string",
						"description": "Backtest start date (YYYY-MM-DD)",
					},
					"end_date": map[string]interface{}{
						"type":        "string",
						"description": "Backtest end date (YYYY-MM-DD)",
					},
					"initial_capital": map[string]interface{}{
						"type":        "number",
						"description": "Initial capital amount",
					},
				},
				"required": []string{"strategy_id", "market", "start_date", "end_date"},
			},
		},
	}
}

// BacktestResult 回测结果
type BacktestResult struct {
	StrategyID     int     `json:"strategy_id"`
	Market         string  `json:"market"`
	StartDate      string  `json:"start_date"`
	EndDate        string  `json:"end_date"`
	InitialCapital float64 `json:"initial_capital"`
	FinalCapital   float64 `json:"final_capital"`
	TotalReturn    float64 `json:"total_return"`
	AnnualReturn   float64 `json:"annual_return"`
	SharpeRatio    float64 `json:"sharpe_ratio"`
	MaxDrawdown    float64 `json:"max_drawdown"`
	TotalTrades    int     `json:"total_trades"`
	WinRate        float64 `json:"win_rate"`
	ProfitFactor   float64 `json:"profit_factor"`
}

func (t *BacktestTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	strategyID, ok := args["strategy_id"].(float64)
	if !ok {
		return nil, fmt.Errorf("strategy_id is required")
	}

	market, ok := args["market"].(string)
	if !ok || market == "" {
		return nil, fmt.Errorf("market is required")
	}

	startDate, ok := args["start_date"].(string)
	if !ok || startDate == "" {
		return nil, fmt.Errorf("start_date is required")
	}

	endDate, ok := args["end_date"].(string)
	if !ok || endDate == "" {
		return nil, fmt.Errorf("end_date is required")
	}

	// 记录回测开始审计
	if t.sqliteManager != nil {
		detailsJSON, _ := json.Marshal(map[string]interface{}{
			"strategy_id": int(strategyID),
			"market":      market,
			"start_date":  startDate,
			"end_date":    endDate,
			"tool":        "run_backtest",
			"timestamp":   time.Now().Format(time.RFC3339),
		})
		auditLog := &data.AuditLog{
			EventID:     fmt.Sprintf("backtest-%d", time.Now().UnixNano()),
			EventType:   data.AuditEventBacktest,
			UserID:      "system",
			Action:      "backtest_start",
			Result:      "success",
			DetailsJSON: string(detailsJSON),
			Timestamp:   time.Now(),
		}
		t.sqliteManager.GetDB().Create(auditLog)
	}

	initialCapital := 100000.0
	if ic, ok := args["initial_capital"].(float64); ok {
		initialCapital = ic
	}

	// 获取策略类型：优先按 strategy_id 从策略表读取真实策略类型（统一策略表）
	strategyType := "ma_cross"
	if t.sqliteManager != nil && t.sqliteManager.GetDB() != nil {
		var st struct {
			StrategyType string
		}
		t.sqliteManager.GetDB().Table("strategies").Select("strategy_type").Where("id = ?", int(strategyID)).Scan(&st)
		if st.StrategyType != "" {
			strategyType = st.StrategyType
		}
	}
	// 允许显式覆盖
	if st, ok := args["strategy_type"].(string); ok && st != "" {
		strategyType = st
	}

	// 获取标的代码
	instrumentID := "index"
	if inst, ok := args["instrument_id"].(string); ok && inst != "" {
		instrumentID = inst
	}

	// 计算回测天数
	startTime, err := time.Parse("2006-01-02", startDate)
	if err != nil {
		return nil, fmt.Errorf("invalid start_date format: %w", err)
	}
	endTime, err := time.Parse("2006-01-02", endDate)
	if err != nil {
		return nil, fmt.Errorf("invalid end_date format: %w", err)
	}
	days := int(endTime.Sub(startTime).Hours() / 24)
	if days < 30 {
		days = 252 // 默认1年
	}

	// 获取历史数据
	prices, err := t.duckdbManager.GetRecentPrices(ctx, market, instrumentID, days)
	if err != nil {
		return nil, fmt.Errorf("failed to get price data for backtest: %w", err)
	}

	if len(prices) < 20 {
		// Data Integrity Guard：数据不足禁止AI推断
		guard := data.NewDataIntegrityGuard(t.duckdbManager)
		status := guard.CheckMarketDataIntegrity(ctx, market, instrumentID, days)
		log.Printf("[BacktestTool] TASK_BLOCKED: %s/%s 数据不足(%d条): %s", market, instrumentID, len(prices), status.Reason)
		return map[string]interface{}{
			"status":  "TASK_BLOCKED",
			"reason":  status.Reason,
			"message": "回测数据不足，禁止AI推断。请先通过数据维护模块同步行情数据。",
		}, nil
	}

	// 执行回测
	result := t.runBacktest(prices, initialCapital, strategyType, int(strategyID), instrumentID)

	// 记录回测完成审计
	if t.sqliteManager != nil {
		detailsJSON, _ := json.Marshal(map[string]interface{}{
			"strategy_id":   int(strategyID),
			"market":        market,
			"total_return":  result.TotalReturn,
			"sharpe_ratio":  result.SharpeRatio,
			"max_drawdown":  result.MaxDrawdown,
			"win_rate":      result.WinRate,
			"final_capital": result.FinalCapital,
			"timestamp":     time.Now().Format(time.RFC3339),
		})
		auditLog := &data.AuditLog{
			EventID:     fmt.Sprintf("backtest-complete-%d", time.Now().UnixNano()),
			EventType:   data.AuditEventBacktest,
			UserID:      "system",
			Action:      "backtest_complete",
			Result:      "success",
			DetailsJSON: string(detailsJSON),
			Timestamp:   time.Now(),
		}
		t.sqliteManager.GetDB().Create(auditLog)
	}

	return result, nil
}

// runBacktest 执行回测逻辑
// 统一复用 backtest 引擎（internal/backtest），保证回撤/夏普/胜率算法与回测页面完全一致
func (t *BacktestTool) runBacktest(prices []data.PriceData, initialCapital float64, strategyType string, strategyID int, instrumentID string) *BacktestResult {
	if len(prices) < 20 {
		return &BacktestResult{
			StrategyID:     strategyID,
			Market:         "mixed",
			StartDate:      "",
			EndDate:        "",
			InitialCapital: initialCapital,
			FinalCapital:   initialCapital,
			TotalReturn:    0,
			AnnualReturn:   0,
			SharpeRatio:    0,
			MaxDrawdown:    0,
			TotalTrades:    0,
			WinRate:        0,
			ProfitFactor:   0,
		}
	}

	// 转换为引擎所需的 K 线格式
	bars := make([]tdx.KlineBar, len(prices))
	for i, p := range prices {
		bars[i] = tdx.KlineBar{
			Date:   p.TradeDate.Format("2006-01-02"),
			Open:   p.Open,
			High:   p.High,
			Low:    p.Low,
			Close:  p.Close,
			Volume: p.Volume,
			Amount: p.Turnover,
		}
	}

	// 高价股/指数（如沪深300、茅台）1手成本可能超过初始资金导致0成交，
	// 先抬升到"最高价×100股×1.05缓冲"，再以该有效资金口径回测与计算收益
	effectiveCapital := backtest.EnsureOneLotCapital(bars, initialCapital)
	result := backtest.RunBacktestWithParamsFin(bars, strategyType, effectiveCapital, t.duckdbManager, instrumentID)

	// 总收益率（%）
	totalReturn := 0.0
	if effectiveCapital > 0 {
		totalReturn = (result.FinalCapital - effectiveCapital) / effectiveCapital * 100
	}

	return &BacktestResult{
		StrategyID:     strategyID,
		Market:         "mixed",
		StartDate:      result.StartDate,
		EndDate:        result.EndDate,
		InitialCapital: effectiveCapital,
		FinalCapital:   result.FinalCapital,
		TotalReturn:    totalReturn,
		AnnualReturn:   result.AnnualReturn,
		SharpeRatio:    result.SharpeRatio,
		MaxDrawdown:    result.MaxDrawdown,
		TotalTrades:    result.TotalTrades,
		WinRate:        result.WinRate,
		ProfitFactor:   result.ProfitFactor,
	}
}

// calculateStdDev 计算标准差
func calculateStdDev(values []float64) float64 {
	if len(values) < 2 {
		return 0
	}

	avg := average(values)
	variance := 0.0
	for _, v := range values {
		diff := v - avg
		variance += diff * diff
	}
	variance /= float64(len(values))

	return math.Sqrt(variance)
}

// ==================== PortfolioStateTool ====================

// PortfolioStateTool 投资组合状态工具 - 供Agent查询组合整体状态
type PortfolioStateTool struct {
	engine *portfolio.Engine
}

func NewPortfolioStateTool(engine *portfolio.Engine) *PortfolioStateTool {
	return &PortfolioStateTool{engine: engine}
}

func (t *PortfolioStateTool) Name() string {
	return "get_portfolio_state"
}

func (t *PortfolioStateTool) Description() string {
	return "Get current portfolio state including total assets, cash, positions, P&L, and recent performance"
}

func (t *PortfolioStateTool) GetDefinition() ToolDefinition {
	return ToolDefinition{
		Type: "function",
		Function: FunctionDef{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
				"required":   []string{},
			},
		},
	}
}

func (t *PortfolioStateTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	if t.engine == nil {
		return nil, fmt.Errorf("portfolio engine not available")
	}

	state := t.engine.GetPortfolioState()
	return state, nil
}

// ==================== GetPositionsTool ====================

// GetPositionsTool 持仓查询工具 - 供Agent实时查询持仓明细
type GetPositionsTool struct {
	engine *portfolio.Engine
}

func NewGetPositionsTool(engine *portfolio.Engine) *GetPositionsTool {
	return &GetPositionsTool{engine: engine}
}

func (t *GetPositionsTool) Name() string {
	return "get_positions"
}

func (t *GetPositionsTool) Description() string {
	return "Get current portfolio positions with real-time prices, P&L, and position weights. Includes risk indicators."
}

func (t *GetPositionsTool) GetDefinition() ToolDefinition {
	return ToolDefinition{
		Type: "function",
		Function: FunctionDef{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"refresh": map[string]interface{}{
						"type":        "boolean",
						"description": "Whether to refresh prices with latest market data before returning",
					},
				},
				"required": []string{},
			},
		},
	}
}

func (t *GetPositionsTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	if t.engine == nil {
		return nil, fmt.Errorf("portfolio engine not available")
	}

	refresh := true
	if r, ok := args["refresh"].(bool); ok {
		refresh = r
	}

	if refresh {
		t.engine.RefreshPrices()
	}

	positions := t.engine.GetPositions()
	return positions, nil
}

// ==================== PlaceTradeTool ====================

// DecisionValidator 决策验证接口（Trader执行前必须验证DecisionObject合法性）
type DecisionValidator interface {
	// ValidateDecisionForExecution 验证决策对象是否可执行（存在、已批准、未过期、当前市场状态允许）
	ValidateDecisionForExecution(decisionID string) error
}

// TradeApprovalService 交易审批服务接口
// 模拟接口模式下，智能体交易需用户手动确认。
// 返回四态：ResultApproved 批准 / ResultRejected 拒绝 / ResultPending 用户未及时确认（排队中，不判定失败）/ ResultFailed 收盘未确认。
// CancelFilled 用于批准后实际执行失败时逆转：订单置取消并释放占用的买入资金。
type TradeApprovalService interface {
	RequestApproval(action, symbol, stockName, market string, quantity int, price float64, reason, decisionID string) (tradeapproval.ApprovalResult, *tradeapproval.PendingTrade, error)
	CancelFilled(pt *tradeapproval.PendingTrade)
}

// PlaceTradeTool 交易执行工具 - 供Agent执行买入/卖出操作
type PlaceTradeTool struct {
	engine    *portfolio.Engine
	validator DecisionValidator    // 决策验证器（Trader只执行合法DecisionObject）
	approval  TradeApprovalService // 交易审批服务（模拟接口模式手动确认）
}

func NewPlaceTradeTool(engine *portfolio.Engine) *PlaceTradeTool {
	return &PlaceTradeTool{engine: engine}
}

// SetDecisionValidator 设置决策验证器（Trader只执行合法DecisionObject）
func (t *PlaceTradeTool) SetDecisionValidator(v DecisionValidator) {
	t.validator = v
}

// SetApprovalService 设置交易审批服务（模拟接口模式手动确认）
func (t *PlaceTradeTool) SetApprovalService(svc TradeApprovalService) {
	t.approval = svc
}

func (t *PlaceTradeTool) Name() string {
	return "place_trade"
}

func (t *PlaceTradeTool) Description() string {
	return "Execute a buy or sell order on a stock. Validates trading rules (T+1, lot size, trading hours, available cash/position). Trader can only execute orders backed by an approved DecisionObject (decision_id)."
}

func (t *PlaceTradeTool) GetDefinition() ToolDefinition {
	return ToolDefinition{
		Type: "function",
		Function: FunctionDef{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"action": map[string]interface{}{
						"type":        "string",
						"description": "Trade action: BUY or SELL",
						"enum":        []string{"BUY", "SELL"},
					},
					"symbol": map[string]interface{}{
						"type":        "string",
						"description": "Stock code (e.g., 600519 for Kweichow Moutai)",
					},
					"stock_name": map[string]interface{}{
						"type":        "string",
						"description": "Stock name",
					},
					"price": map[string]interface{}{
						"type":        "number",
						"description": "Execution price",
					},
					"quantity": map[string]interface{}{
						"type":        "integer",
						"description": "Number of shares (must be multiple of 100 for A-shares)",
					},
					"reason": map[string]interface{}{
						"type":        "string",
						"description": "Reason for the trade",
					},
					"decision_id": map[string]interface{}{
						"type":        "string",
						"description": "Approved DecisionObject decision_id that authorizes this trade (Trader can only execute approved decisions)",
					},
				},
				"required": []string{"action", "symbol", "price", "quantity", "decision_id"},
			},
		},
	}
}

func (t *PlaceTradeTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	if t.engine == nil {
		return nil, fmt.Errorf("portfolio engine not available")
	}

	action, _ := args["action"].(string)
	symbol, _ := args["symbol"].(string)
	stockName, _ := args["stock_name"].(string)
	price, _ := args["price"].(float64)
	quantity := 0
	if q, ok := args["quantity"].(float64); ok {
		quantity = int(q)
	}
	reason, _ := args["reason"].(string)
	decisionID, _ := args["decision_id"].(string)

	if action == "" || symbol == "" || price <= 0 || quantity <= 0 {
		return nil, fmt.Errorf("invalid trade parameters: action=%s, symbol=%s, price=%.2f, quantity=%d",
			action, symbol, price, quantity)
	}

	// Runtime Enforcement: Trader只能执行合法DecisionObject
	// 没有合法DecisionObject（存在、CIO批准、Risk批准、未过期），禁止交易
	if t.validator != nil {
		if err := t.validator.ValidateDecisionForExecution(decisionID); err != nil {
			log.Printf("[PlaceTradeTool] ACTION_DENIED: Trader 尝试执行无合法DecisionObject的交易 %s %s: %v", action, symbol, err)
			return map[string]interface{}{
				"status":  "denied",
				"message": fmt.Sprintf("ACTION_DENIED: 交易被拒绝，缺少合法DecisionObject: %v", err),
			}, nil
		}
	} else if decisionID == "" {
		// 未配置验证器时，强制要求decision_id非空（Trader不能自主交易）
		return map[string]interface{}{
			"status":  "denied",
			"message": "ACTION_DENIED: Trader不能自主交易，必须提供已批准的decision_id",
		}, nil
	}

	market := "SH"
	if len(symbol) >= 3 {
		prefix := symbol[:3]
		if prefix == "000" || prefix == "001" || prefix == "002" || prefix == "003" ||
			prefix == "200" || prefix == "300" || prefix == "301" {
			market = "SZ"
		} else if prefix == "430" || prefix == "831" || prefix == "870" || prefix == "871" || prefix == "872" || prefix == "873" {
			market = "BJ"
		}
	}

	log.Printf("[PlaceTradeTool] %s %s %d@¥%.2f, reason=%s", action, symbol, quantity, price, reason)

	// 结合投资金额(可用资金)做整手与资金约束校验，确保买入手数合法、不超可用资金
	if action == "BUY" {
		qty := (quantity / 100) * 100 // A股整手归一下到100股整数倍
		if qty < 100 {
			qty = 100
		}
		snap := t.engine.GetSnapshot()
		affordable := int(snap.Cash/price) / 100 * 100 // 可用资金能负担的整手上限
		if affordable > 0 && affordable < qty {
			qty = affordable
		}
		if qty < 100 {
			log.Printf("[PlaceTradeTool] ACTION_DENIED: 可用资金(¥%.2f)不足以买入1手 %s", snap.Cash, symbol)
			return map[string]interface{}{
				"status":  "denied",
				"message": fmt.Sprintf("ACTION_DENIED: 可用资金¥%.2f不足以买入1手%s", snap.Cash, symbol),
			}, nil
		}
		if qty != quantity {
			log.Printf("[PlaceTradeTool] 数量归一化: %s %d -> %d 股(整手+资金约束)", symbol, quantity, qty)
			quantity = qty
		}
	}

	// 模拟接口模式下，交易需用户手动确认
	var confirmedPT *tradeapproval.PendingTrade
	if t.approval != nil {
		result, pendingOrder, err := t.approval.RequestApproval(action, symbol, stockName, market, quantity, price, reason, decisionID)
		confirmedPT = pendingOrder
		if err != nil {
			return map[string]interface{}{
				"status":  "error",
				"message": fmt.Sprintf("交易确认失败: %v", err),
			}, nil
		}
		switch result {
		case tradeapproval.ResultRejected:
			log.Printf("[PlaceTradeTool] %s %s %d@¥%.2f REJECTED by user", action, symbol, quantity, price)
			return map[string]interface{}{
				"status":  "rejected",
				"message": "用户拒绝执行该交易，订单已取消",
			}, nil
		case tradeapproval.ResultPending:
			// 用户未及时确认：订单排队中，不判定失败。用户稍后确认（批准）后系统将补执行。
			log.Printf("[PlaceTradeTool] %s %s %d@¥%.2f 排队等待用户确认(未超时拒绝)", action, symbol, quantity, price)
			return map[string]interface{}{
				"status":  "pending",
				"message": "订单排队中：等待用户手动确认，尚未成交。用户确认后系统将自动成交，无需重试。",
			}, nil
		case tradeapproval.ResultFailed:
			// 15:00 收盘仍未获确认：交易失败
			log.Printf("[PlaceTradeTool] %s %s %d@¥%.2f FAILED (15:00 未获确认)", action, symbol, quantity, price)
			return map[string]interface{}{
				"status":  "failed",
				"message": "交易失败：截至 15:00 收盘未获得用户确认，订单已取消",
			}, nil
		}
		// ResultApproved：继续执行交易
	}

	switch action {
	case "BUY":
		record, err := t.engine.Buy(symbol, stockName, market, quantity, price, reason, decisionID)
		if err != nil {
			log.Printf("[PlaceTradeTool] BUY failed: %v", err)
			// 批准后实际执行失败：逆转已按成交标记的订单并释放占用的买入资金
			if confirmedPT != nil && t.approval != nil {
				t.approval.CancelFilled(confirmedPT)
			}
			return map[string]interface{}{
				"status":  "error",
				"message": err.Error(),
			}, nil
		}
		return map[string]interface{}{
			"status":     "success",
			"action":     "BUY",
			"trade_id":   record.TradeID,
			"symbol":     symbol,
			"quantity":   record.Quantity,
			"price":      record.Price,
			"net_amount": record.NetAmount,
			"cash_left":  t.engine.GetCash(),
		}, nil

	case "SELL":
		record, err := t.engine.Sell(symbol, quantity, price, reason, decisionID)
		if err != nil {
			log.Printf("[PlaceTradeTool] SELL failed: %v", err)
			if confirmedPT != nil && t.approval != nil {
				t.approval.CancelFilled(confirmedPT)
			}
			return map[string]interface{}{
				"status":  "error",
				"message": err.Error(),
			}, nil
		}
		return map[string]interface{}{
			"status":       "success",
			"action":       "SELL",
			"trade_id":     record.TradeID,
			"quantity":     record.Quantity,
			"price":        record.Price,
			"net_amount":   record.NetAmount,
			"realized_pnl": record.RealizedPnL,
			"cash_left":    t.engine.GetCash(),
		}, nil

	default:
		return nil, fmt.Errorf("unknown action: %s", action)
	}
}

// ==================== PortfolioStateTool END ====================

// ==================== 辅助函数 ====================

// calculateIndicators 计算技术指标
func calculateIndicators(dataPoints []MarketDataPoint) *TechnicalIndicators {
	if len(dataPoints) < 20 {
		return nil
	}

	// SMA 计算
	closes := make([]float64, len(dataPoints))
	for i, dp := range dataPoints {
		closes[i] = dp.Close
	}

	sma20 := average(closes[len(closes)-20:])

	var sma50 float64
	if len(closes) >= 50 {
		sma50 = average(closes[len(closes)-50:])
	} else {
		sma50 = sma20
	}

	// RSI 计算（简化）
	rsi := calculateRSI(closes, 14)

	// 波动率
	volatility := calculateVolatility(closes, 20)

	return &TechnicalIndicators{
		SMA20:      sma20,
		SMA50:      sma50,
		RSI14:      rsi,
		Volatility: volatility,
	}
}

// average 计算平均值
func average(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range values {
		sum += v
	}
	return sum / float64(len(values))
}

// calculateRSI 计算 RSI
func calculateRSI(closes []float64, period int) float64 {
	if len(closes) <= period {
		return 50.0
	}

	var gains, losses float64
	for i := len(closes) - period; i < len(closes); i++ {
		diff := closes[i] - closes[i-1]
		if diff > 0 {
			gains += diff
		} else {
			losses -= diff
		}
	}

	if gains == 0 && losses == 0 {
		return 50.0
	}

	avgGain := gains / float64(period)
	avgLoss := losses / float64(period)

	if avgLoss == 0 {
		return 100.0
	}

	rs := avgGain / avgLoss
	return 100.0 - (100.0 / (1.0 + rs))
}

// calculateVolatility 计算波动率
func calculateVolatility(closes []float64, period int) float64 {
	if len(closes) <= period {
		return 0.0
	}

	// 计算收益率
	returns := make([]float64, 0)
	for i := len(closes) - period; i < len(closes); i++ {
		if closes[i-1] != 0 {
			returns = append(returns, (closes[i]/closes[i-1])-1)
		}
	}

	if len(returns) == 0 {
		return 0.0
	}

	// 计算标准差
	mean := average(returns)
	variance := 0.0
	for _, r := range returns {
		diff := r - mean
		variance += diff * diff
	}
	variance /= float64(len(returns))

	// 年化波动率
	return sqrt(variance * 252)
}

// sqrt 平方根计算
func sqrt(x float64) float64 {
	if x <= 0 {
		return 0
	}

	// 牛顿法
	z := x
	for i := 0; i < 100; i++ {
		z = (z + x/z) / 2
	}
	return z
}

// 辅助: 将参数序列化为 JSON（用于日志）
func serializeArgs(args map[string]interface{}) string {
	data, err := json.Marshal(args)
	if err != nil {
		return fmt.Sprintf("%v", args)
	}
	return string(data)
}

// ==================== TradingCalendarTool（联网刷新交易日历） ====================

// TradingCalendarTool 联网刷新A股交易日历到外挂配置文件并缓存。
// 无构造依赖：配置文件路径由 App 启动时经 util.SetTradingCalendarFile 注入。
type TradingCalendarTool struct{}

// NewTradingCalendarTool 创建交易日历刷新工具
func NewTradingCalendarTool() *TradingCalendarTool {
	return &TradingCalendarTool{}
}

func (t *TradingCalendarTool) Name() string {
	return "update_trading_calendar"
}

func (t *TradingCalendarTool) Description() string {
	return "联网刷新A股交易日历到外挂配置文件并缓存，返回本次新增的休市日数量。当需要确认未来节假日/休市安排时调用。"
}

func (t *TradingCalendarTool) GetDefinition() ToolDefinition {
	return ToolDefinition{
		Type: "function",
		Function: FunctionDef{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		},
	}
}

func (t *TradingCalendarTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	now := time.Now()
	years := []int{now.Year() - 1, now.Year(), now.Year() + 1}
	added, err := util.SyncTradingHolidaysFromWeb(years)
	if err != nil {
		log.Printf("[TradingCalendarTool] 联网刷新交易日历失败: %v", err)
		return map[string]interface{}{
			"status":  "error",
			"message": fmt.Sprintf("联网刷新交易日历失败: %v", err),
		}, nil
	}
	log.Printf("[TradingCalendarTool] 联网刷新交易日历成功: 新增 %d 个休市日（覆盖至 %d 年）", added, util.TradingCalendarLatestYear())
	return map[string]interface{}{
		"status":            "success",
		"source":            "timor.tech/api/holiday/year",
		"added_holidays":    added,
		"years_updated":     []int{years[0], years[1], years[2]},
		"last_year_covered": util.TradingCalendarLatestYear(),
		"message":           fmt.Sprintf("交易日历已刷新，新增 %d 个休市日", added),
	}, nil
}

// ==================== TradingDayCheckTool（交易日检查，CIO收盘调用） ====================

// TradingDayCheckTool 检查指定日期（默认今天）是否为A股交易日，返回休市/交易日判定及相邻交易日。
type TradingDayCheckTool struct{}

// NewTradingDayCheckTool 创建交易日检查工具
func NewTradingDayCheckTool() *TradingDayCheckTool {
	return &TradingDayCheckTool{}
}

func (t *TradingDayCheckTool) Name() string {
	return "check_trading_day"
}

func (t *TradingDayCheckTool) Description() string {
	return "检查指定日期（默认今天）是否为A股交易日，返回是否交易日/是否休市及前后相邻交易日。CIO收盘结算前确认当日是否交易日，避免在非交易日误执行结算。"
}

func (t *TradingDayCheckTool) GetDefinition() ToolDefinition {
	return ToolDefinition{
		Type: "function",
		Function: FunctionDef{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"date": map[string]string{
						"type":        "string",
						"description": "要检查的日期，格式 YYYY-MM-DD，可选，默认今天",
					},
				},
			},
		},
	}
}

func (t *TradingDayCheckTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	loc := time.FixedZone("CST", 8*3600)
	now := time.Now().In(loc)
	if ds, ok := args["date"].(string); ok && ds != "" {
		if parsed, err := time.ParseInLocation("2006-01-02", ds, loc); err == nil {
			now = parsed
		}
	}
	isTrading := util.IsTradingDay(now)
	return map[string]interface{}{
		"date":             now.Format("2006-01-02"),
		"is_trading_day":   isTrading,
		"is_holiday":       !isTrading,
		"next_trading_day": util.NextTradingDay(now).Format("2006-01-02"),
		"prev_trading_day": util.PrevTradingDay(now).Format("2006-01-02"),
	}, nil
}
