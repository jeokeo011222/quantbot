package tools

import (
	"context"
	"fmt"
	"log"

	"github.com/quantpilot/quantpilot/internal/data"
	"github.com/quantpilot/quantpilot/internal/tdx"
)

// ==================== TDXDataTool ====================

// TDXDataTool 通达信数据工具 - 从本地通达信软件读取A股数据
type TDXDataTool struct {
	reader *tdx.TDXReader
}

// NewTDXDataTool 创建通达信数据工具
func NewTDXDataTool(tdxPath string) *TDXDataTool {
	return &TDXDataTool{
		reader: tdx.NewTDXReader(tdxPath),
	}
}

func (t *TDXDataTool) Name() string {
	return "get_tdx_stock_data"
}

func (t *TDXDataTool) Description() string {
	return "从本地通达信软件获取A股个股日线数据（K线），包含开盘价、收盘价、最高价、最低价、成交量等信息"
}

func (t *TDXDataTool) GetDefinition() ToolDefinition {
	return ToolDefinition{
		Type: "function",
		Function: FunctionDef{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"exchange": map[string]interface{}{
						"type":        "string",
						"description": "交易所：sh(上海)、sz(深圳)、bj(北京)",
						"enum":        []string{"sh", "sz", "bj"},
					},
					"code": map[string]interface{}{
						"type":        "string",
						"description": "股票代码，如 600519（贵州茅台）、000858（五粮液）",
					},
					"days": map[string]interface{}{
						"type":        "integer",
						"description": "获取最近多少个交易日的数据，默认60",
					},
					"include_indicators": map[string]interface{}{
						"type":        "boolean",
						"description": "是否包含技术指标（均线、MACD、RSI等），默认true",
					},
				},
				"required": []string{"exchange", "code"},
			},
		},
	}
}

func (t *TDXDataTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	exchangeStr, ok := args["exchange"].(string)
	if !ok || exchangeStr == "" {
		return nil, fmt.Errorf("exchange 参数必填（sh/sz/bj）")
	}

	code, ok := args["code"].(string)
	if !ok || code == "" {
		return nil, fmt.Errorf("code 参数必填")
	}

	days := 60
	if d, ok := args["days"].(float64); ok && d > 0 {
		days = int(d)
	}

	includeIndicators := true
	if inc, ok := args["include_indicators"].(bool); ok {
		includeIndicators = inc
	}

	exchange := tdx.Exchange(exchangeStr)
	bars, err := t.reader.GetRecentBars(exchange, code, days)
	if err != nil {
		return nil, fmt.Errorf("读取 %s%s 数据失败: %w", exchange, code, err)
	}

	// 转换格式
	dataPoints := make([]map[string]interface{}, len(bars))
	for i, bar := range bars {
		dataPoints[i] = map[string]interface{}{
			"date":   formatDateInt(bar.Date),
			"open":   bar.Open,
			"high":   bar.High,
			"low":    bar.Low,
			"close":  bar.Close,
			"volume": bar.Volume,
			"amount": bar.Amount,
		}
	}

	result := map[string]interface{}{
		"exchange": exchangeStr,
		"code":     code,
		"days":     len(bars),
		"data":     dataPoints,
	}

	if includeIndicators && len(bars) >= 20 {
		indicators := tdx.CalculateIndicators(bars)
		if indicators != nil {
			result["indicators"] = map[string]interface{}{
				"ma5":           indicators.MA5,
				"ma10":          indicators.MA10,
				"ma20":          indicators.MA20,
				"ma60":          indicators.MA60,
				"rsi_14":        indicators.RSI14,
				"macd_dif":      indicators.MACD_DIF,
				"macd_dea":      indicators.MACD_DEA,
				"macd":          indicators.MACD,
				"volatility_20": indicators.Volatility20,
				"change_pct":    indicators.ChangePct,
				"change_5d":     indicators.Change5d,
				"change_20d":    indicators.Change20d,
				"high_20":       indicators.High20,
				"low_20":        indicators.Low20,
				"volume_ratio":  indicators.VolumeRatio,
			}
		}
	}

	return result, nil
}

// ==================== TDXSearchTool ====================

// TDXSearchTool 通达信股票搜索工具
type TDXSearchTool struct {
	reader *tdx.TDXReader
}

// NewTDXSearchTool 创建通达信搜索工具
func NewTDXSearchTool(tdxPath string) *TDXSearchTool {
	return &TDXSearchTool{
		reader: tdx.NewTDXReader(tdxPath),
	}
}

func (t *TDXSearchTool) Name() string {
	return "search_tdx_stocks"
}

func (t *TDXSearchTool) Description() string {
	return "在通达信本地数据库中搜索A股股票，返回股票代码、最新价格、涨跌幅等信息"
}

func (t *TDXSearchTool) GetDefinition() ToolDefinition {
	return ToolDefinition{
		Type: "function",
		Function: FunctionDef{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"exchange": map[string]interface{}{
						"type":        "string",
						"description": "交易所：sh(上海)、sz(深圳)、bj(北京)、all(全部)",
						"enum":        []string{"sh", "sz", "bj", "all"},
					},
					"code_prefix": map[string]interface{}{
						"type":        "string",
						"description": "股票代码前缀，如 600、000、300。留空则返回全部",
					},
					"limit": map[string]interface{}{
						"type":        "integer",
						"description": "最多返回多少条，默认20",
					},
				},
				"required": []string{},
			},
		},
	}
}

func (t *TDXSearchTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	exchangeStr := "all"
	if ex, ok := args["exchange"].(string); ok && ex != "" {
		exchangeStr = ex
	}

	codePrefix := ""
	if cp, ok := args["code_prefix"].(string); ok {
		codePrefix = cp
	}

	limit := 20
	if l, ok := args["limit"].(float64); ok && l > 0 {
		limit = int(l)
	}

	var exchanges []tdx.Exchange
	if exchangeStr == "all" {
		exchanges = []tdx.Exchange{tdx.ExchangeSH, tdx.ExchangeSZ}
	} else {
		exchanges = []tdx.Exchange{tdx.Exchange(exchangeStr)}
	}

	var allResults []map[string]interface{}
	for _, ex := range exchanges {
		stocks, err := t.reader.SearchStocks(ex, codePrefix, limit)
		if err != nil {
			continue
		}
		for _, s := range stocks {
			item := map[string]interface{}{
				"code":       s.Code,
				"exchange":   s.Exchange,
				"last_close": s.LastClose,
				"last_date":  s.LastDate,
				"total_bars": s.TotalBars,
				"latest_vol": s.LatestVolume,
			}

			// 计算技术指标
			bars, err := t.reader.GetRecentBars(ex, s.Code, 60)
			if err == nil && len(bars) >= 20 {
				indicators := tdx.CalculateIndicators(bars)
				if indicators != nil {
					item["change_pct"] = indicators.ChangePct
					item["rsi_14"] = indicators.RSI14
					item["ma20"] = indicators.MA20
					item["volume_ratio"] = indicators.VolumeRatio
				}
			}

			allResults = append(allResults, item)
			if len(allResults) >= limit {
				break
			}
		}
		if len(allResults) >= limit {
			break
		}
	}

	return map[string]interface{}{
		"total":   len(allResults),
		"results": allResults,
	}, nil
}

// ==================== TDXMarketStatsTool ====================

// TDXMarketStatsTool 通达信市场统计工具
type TDXMarketStatsTool struct {
	reader *tdx.TDXReader
}

// NewTDXMarketStatsTool 创建通达信市场统计工具
func NewTDXMarketStatsTool(tdxPath string) *TDXMarketStatsTool {
	return &TDXMarketStatsTool{
		reader: tdx.NewTDXReader(tdxPath),
	}
}

func (t *TDXMarketStatsTool) Name() string {
	return "get_tdx_market_stats"
}

func (t *TDXMarketStatsTool) Description() string {
	return "获取A股市场统计数据，包括涨跌家数、涨停跌停数量、板块表现等"
}

func (t *TDXMarketStatsTool) GetDefinition() ToolDefinition {
	return ToolDefinition{
		Type: "function",
		Function: FunctionDef{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"exchange": map[string]interface{}{
						"type":        "string",
						"description": "交易所：sh(上海)、sz(深圳)、all(全部)",
						"enum":        []string{"sh", "sz", "all"},
					},
					"sample_size": map[string]interface{}{
						"type":        "integer",
						"description": "采样股票数量（为保证性能，默认最多200只）",
					},
				},
				"required": []string{},
			},
		},
	}
}

func (t *TDXMarketStatsTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	exchangeStr := "all"
	if ex, ok := args["exchange"].(string); ok && ex != "" {
		exchangeStr = ex
	}

	sampleSize := 200
	if s, ok := args["sample_size"].(float64); ok && s > 0 {
		sampleSize = int(s)
	}

	var exchanges []tdx.Exchange
	if exchangeStr == "all" {
		exchanges = []tdx.Exchange{tdx.ExchangeSH, tdx.ExchangeSZ}
	} else {
		exchanges = []tdx.Exchange{tdx.Exchange(exchangeStr)}
	}

	var totalAdvancers, totalDecliners, totalFlat, totalLimitUp, totalLimitDown int
	var sampledStocks []map[string]interface{}

	for _, ex := range exchanges {
		overview, err := t.reader.GetMarketOverview(ex, sampleSize)
		if err != nil {
			continue
		}
		totalAdvancers += overview.Advancers
		totalDecliners += overview.Decliners
		totalFlat += overview.Flat
		totalLimitUp += overview.LimitUp
		totalLimitDown += overview.LimitDown

		for _, s := range overview.Stocks {
			sampledStocks = append(sampledStocks, map[string]interface{}{
				"code":         s.Code,
				"last_close":   s.LastClose,
				"change_pct":   s.ChangePct,
				"volume":       s.Volume,
				"amount":       s.Amount,
				"limit_up":     s.LimitUp,
				"limit_down":   s.LimitDown,
				"ma5":          s.MA5,
				"ma20":         s.MA20,
				"rsi_14":       s.RSI14,
				"volume_ratio": s.VolumeRatio,
			})
		}
	}

	// 行业板块表现
	var sectorPerfs []map[string]interface{}
	for _, ex := range exchanges {
		perfs, err := t.reader.GetSectorPerformance(ex)
		if err != nil {
			continue
		}
		for _, p := range perfs {
			sectorPerfs = append(sectorPerfs, map[string]interface{}{
				"sector":      p.Name,
				"stock_count": p.StockCount,
				"avg_change":  p.AvgChange,
				"max_change":  p.MaxChange,
				"min_change":  p.MinChange,
			})
		}
	}

	// 按平均涨幅排序
	sortSectorsByChange(sectorPerfs)

	return map[string]interface{}{
		"market":         exchangeStr,
		"advancers":      totalAdvancers,
		"decliners":      totalDecliners,
		"flat":           totalFlat,
		"limit_up":       totalLimitUp,
		"limit_down":     totalLimitDown,
		"breadth":        fmt.Sprintf("上涨%d/下跌%d/平盘%d", totalAdvancers, totalDecliners, totalFlat),
		"limit_status":   fmt.Sprintf("涨停%d/跌停%d", totalLimitUp, totalLimitDown),
		"sectors":        sectorPerfs[:min(len(sectorPerfs), 10)],
		"sampled_stocks": sampledStocks[:min(len(sampledStocks), 50)],
	}, nil
}

// ==================== TDXCommonStocksTool ====================

// TDXCommonStocksTool 常见A股快速查询工具
type TDXCommonStocksTool struct {
	reader *tdx.TDXReader
}

// NewTDXCommonStocksTool 创建常见股票查询工具
func NewTDXCommonStocksTool(tdxPath string) *TDXCommonStocksTool {
	return &TDXCommonStocksTool{
		reader: tdx.NewTDXReader(tdxPath),
	}
}

func (t *TDXCommonStocksTool) Name() string {
	return "get_tdx_common_stocks"
}

func (t *TDXCommonStocksTool) Description() string {
	return "获取A股核心蓝筹/白马股的最新行情数据和技术指标（贵州茅台、五粮液、招商银行、平安银行等）"
}

func (t *TDXCommonStocksTool) GetDefinition() ToolDefinition {
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

func (t *TDXCommonStocksTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	commonStocks := tdx.CommonAStockCodes()
	var results []map[string]interface{}

	for _, stock := range commonStocks {
		bars, err := t.reader.GetRecentBars(tdx.Exchange(stock.Exchange), stock.Code, 60)
		if err != nil || len(bars) < 2 {
			continue
		}

		latest := bars[len(bars)-1]
		prevClose := bars[len(bars)-2].Close

		var changePct float64
		if prevClose > 0 {
			changePct = (latest.Close/prevClose - 1) * 100
		}

		item := map[string]interface{}{
			"code":       stock.Code,
			"name":       stock.Name,
			"exchange":   stock.Exchange,
			"last_close": latest.Close,
			"open":       latest.Open,
			"high":       latest.High,
			"low":        latest.Low,
			"change_pct": roundTo2(changePct),
			"volume":     latest.Volume,
			"amount":     latest.Amount,
			"date":       formatDateInt(latest.Date),
		}

		indicators := tdx.CalculateIndicators(bars)
		if indicators != nil {
			item["indicators"] = map[string]interface{}{
				"ma5":          indicators.MA5,
				"ma10":         indicators.MA10,
				"ma20":         indicators.MA20,
				"ma60":         indicators.MA60,
				"rsi_14":       indicators.RSI14,
				"macd_dif":     indicators.MACD_DIF,
				"macd_dea":     indicators.MACD_DEA,
				"macd":         indicators.MACD,
				"change_5d":    indicators.Change5d,
				"change_20d":   indicators.Change20d,
				"volume_ratio": indicators.VolumeRatio,
			}
		}

		results = append(results, item)
	}

	return map[string]interface{}{
		"total":  len(results),
		"stocks": results,
	}, nil
}

// ========== 辅助函数 ==========

func formatDateInt(dateInt int) string {
	s := fmt.Sprintf("%08d", dateInt)
	if len(s) == 8 {
		return s[:4] + "-" + s[4:6] + "-" + s[6:8]
	}
	return s
}

func roundTo2(v float64) float64 {
	if v == 0 {
		return 0
	}
	if v > 0 {
		return float64(int64(v*100+0.5)) / 100
	}
	return float64(int64(v*100-0.5)) / 100
}

func sortSectorsByChange(sectors []map[string]interface{}) {
	for i := 0; i < len(sectors); i++ {
		for j := i + 1; j < len(sectors); j++ {
			vi := sectors[i]["avg_change"].(float64)
			vj := sectors[j]["avg_change"].(float64)
			if vi < vj {
				sectors[i], sectors[j] = sectors[j], sectors[i]
			}
		}
	}
}

// ==================== 新版：基于 TDXServiceManager 的 Agent 工具 ====================

// TDXDataToolV2 支持多数据源的通达信数据工具（推荐）
type TDXDataToolV2 struct {
	manager *TDXServiceManager
}

// NewTDXDataToolV2 创建支持多数据源的通达信数据工具
func NewTDXDataToolV2(manager *TDXServiceManager) *TDXDataToolV2 {
	return &TDXDataToolV2{
		manager: manager,
	}
}

func (t *TDXDataToolV2) Name() string {
	return "get_tdx_stock_data"
}

func (t *TDXDataToolV2) Description() string {
	return "从通达信数据源获取A股个股数据（支持Go原生TDX/Python TDX/本地读取三种模式），包含日线、分钟线、实时行情、逐笔成交和F10资讯"
}

func (t *TDXDataToolV2) GetDefinition() ToolDefinition {
	return ToolDefinition{
		Type: "function",
		Function: FunctionDef{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"exchange": map[string]interface{}{
						"type":        "string",
						"description": "交易所：sh(上海)、sz(深圳)、bj(北京)",
						"enum":        []string{"sh", "sz", "bj"},
					},
					"code": map[string]interface{}{
						"type":        "string",
						"description": "股票代码，如 600519（贵州茅台）、000858（五粮液）",
					},
					"data_type": map[string]interface{}{
						"type":        "string",
						"description": "数据类型：kline(日线)、quote(实时快照)、trades(逐笔成交)、minute(分时数据)",
						"enum":        []string{"kline", "quote", "trades", "minute"},
					},
					"period": map[string]interface{}{
						"type":        "string",
						"description": "K线周期：1min/5min/15min/30min/60min/day/week/month（仅data_type=kline时有效）",
					},
					"days": map[string]interface{}{
						"type":        "integer",
						"description": "获取最近多少根K线，默认60",
					},
				},
				"required": []string{"exchange", "code"},
			},
		},
	}
}

func (t *TDXDataToolV2) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	if t.manager == nil {
		return nil, fmt.Errorf("TDXServiceManager not initialized")
	}

	exchangeStr, ok := args["exchange"].(string)
	if !ok || exchangeStr == "" {
		return nil, fmt.Errorf("exchange 参数必填（sh/sz/bj）")
	}

	code, ok := args["code"].(string)
	if !ok || code == "" {
		return nil, fmt.Errorf("code 参数必填")
	}

	dataType := "kline"
	if dt, ok := args["data_type"].(string); ok && dt != "" {
		dataType = dt
	}

	switch dataType {
	case "quote":
		return t.manager.GetRealTimeQuote(exchangeStr, code)
	case "trades":
		return t.manager.GetTrades(exchangeStr, code)
	case "minute":
		days := 240
		if d, ok := args["days"].(float64); ok && d > 0 {
			days = int(d)
		}
		return t.manager.GetMinuteBars(exchangeStr, code, days)
	default:
		period := "day"
		if p, ok := args["period"].(string); ok && p != "" {
			period = p
		}
		days := 60
		if d, ok := args["days"].(float64); ok && d > 0 {
			days = int(d)
		}
		// 支持非标准的days参数给GetStockData
		if period == "day" {
			includeIndicators := true
			if inc, ok := args["include_indicators"].(bool); ok {
				includeIndicators = inc
			}
			return t.manager.GetStockData(exchangeStr, code, days, includeIndicators)
		}
		return t.manager.GetKline(exchangeStr, code, period, days, "none")
	}
}

// TDXSearchToolV2 支持多数据源的通达信股票搜索工具
type TDXSearchToolV2 struct {
	manager *TDXServiceManager
}

// NewTDXSearchToolV2 创建支持多数据源的通达信搜索工具
func NewTDXSearchToolV2(manager *TDXServiceManager) *TDXSearchToolV2 {
	return &TDXSearchToolV2{
		manager: manager,
	}
}

func (t *TDXSearchToolV2) Name() string {
	return "search_tdx_stocks"
}

func (t *TDXSearchToolV2) Description() string {
	return "在通达信数据源中搜索A股股票，返回股票代码、最新价格、涨跌幅等信息"
}

func (t *TDXSearchToolV2) GetDefinition() ToolDefinition {
	return ToolDefinition{
		Type: "function",
		Function: FunctionDef{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"exchange": map[string]interface{}{
						"type":        "string",
						"description": "交易所：sh(上海)、sz(深圳)、bj(北京)、all(全部)",
						"enum":        []string{"sh", "sz", "bj", "all"},
					},
					"code_prefix": map[string]interface{}{
						"type":        "string",
						"description": "股票代码前缀，如 600、000、300。留空则返回全部",
					},
					"limit": map[string]interface{}{
						"type":        "integer",
						"description": "最多返回多少条，默认20",
					},
				},
				"required": []string{},
			},
		},
	}
}

func (t *TDXSearchToolV2) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	if t.manager == nil {
		return nil, fmt.Errorf("TDXServiceManager not initialized")
	}

	exchangeStr := "all"
	if ex, ok := args["exchange"].(string); ok && ex != "" {
		exchangeStr = ex
	}

	codePrefix := ""
	if cp, ok := args["code_prefix"].(string); ok {
		codePrefix = cp
	}

	limit := 20
	if l, ok := args["limit"].(float64); ok && l > 0 {
		limit = int(l)
	}

	// 使用 manager 搜索
	stocks, err := t.manager.SearchStocks(exchangeStr, codePrefix, limit)
	if err != nil {
		return nil, err
	}

	var allResults []map[string]interface{}
	for _, s := range stocks {
		item := map[string]interface{}{
			"code":       s.Code,
			"exchange":   s.Exchange,
			"last_close": s.LastClose,
			"last_date":  s.LastDate,
			"total_bars": s.TotalBars,
			"latest_vol": s.LatestVolume,
		}

		// 尝试获取实时行情
		if t.manager.GetActiveProvider() == data.DataProviderNativeName {
			if quote, err := t.manager.GetRealTimeQuote(s.Exchange, s.Code); err == nil {
				item["change_pct"] = quote["change_pct"]
				item["current_price"] = quote["price"]
			}
		}

		allResults = append(allResults, item)
		if len(allResults) >= limit {
			break
		}
	}

	return map[string]interface{}{
		"total":   len(allResults),
		"results": allResults,
		"source":  t.manager.GetActiveProvider(),
	}, nil
}

// TDXMarketStatsToolV2 支持多数据源的通达信市场统计工具
type TDXMarketStatsToolV2 struct {
	manager *TDXServiceManager
}

// NewTDXMarketStatsToolV2 创建支持多数据源的通达信市场统计工具
func NewTDXMarketStatsToolV2(manager *TDXServiceManager) *TDXMarketStatsToolV2 {
	return &TDXMarketStatsToolV2{
		manager: manager,
	}
}

func (t *TDXMarketStatsToolV2) Name() string {
	return "get_tdx_market_stats"
}

func (t *TDXMarketStatsToolV2) Description() string {
	return "获取A股市场统计数据，包括涨跌家数、涨停跌停数量、板块表现等"
}

func (t *TDXMarketStatsToolV2) GetDefinition() ToolDefinition {
	return ToolDefinition{
		Type: "function",
		Function: FunctionDef{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"exchange": map[string]interface{}{
						"type":        "string",
						"description": "交易所：sh(上海)、sz(深圳)、all(全部)",
						"enum":        []string{"sh", "sz", "all"},
					},
					"sample_size": map[string]interface{}{
						"type":        "integer",
						"description": "采样股票数量（为保证性能，默认最多200只）",
					},
				},
				"required": []string{},
			},
		},
	}
}

func (t *TDXMarketStatsToolV2) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	if t.manager == nil {
		return nil, fmt.Errorf("TDXServiceManager not initialized")
	}

	exchangeStr := "all"
	if ex, ok := args["exchange"].(string); ok && ex != "" {
		exchangeStr = ex
	}

	sampleSize := 200
	if s, ok := args["sample_size"].(float64); ok && s > 0 {
		sampleSize = int(s)
	}

	// 优先使用 market stats
	result, err := t.manager.GetMarketStats(exchangeStr, sampleSize)
	if err == nil {
		result["source"] = t.manager.GetActiveProvider()
		return result, nil
	}

	// 降级：尝试从 local reader 获取
	log.Printf("[TDXMarketStatsToolV2] GetMarketStats failed: %v, trying local reader", err)

	// 获取市场统计
	var totalAdvancers, totalDecliners, totalFlat, totalLimitUp, totalLimitDown int
	var sampledStocks []map[string]interface{}

	var exchanges []tdx.Exchange
	if exchangeStr == "all" {
		exchanges = []tdx.Exchange{tdx.ExchangeSH, tdx.ExchangeSZ}
	} else {
		exchanges = []tdx.Exchange{tdx.Exchange(exchangeStr)}
	}

	for _, ex := range exchanges {
		overview, err := tdx.NewTDXReader("").GetMarketOverview(ex, sampleSize)
		if err != nil {
			continue
		}
		totalAdvancers += overview.Advancers
		totalDecliners += overview.Decliners
		totalFlat += overview.Flat
		totalLimitUp += overview.LimitUp
		totalLimitDown += overview.LimitDown

		for _, s := range overview.Stocks {
			sampledStocks = append(sampledStocks, map[string]interface{}{
				"code":         s.Code,
				"last_close":   s.LastClose,
				"change_pct":   s.ChangePct,
				"volume":       s.Volume,
				"amount":       s.Amount,
				"limit_up":     s.LimitUp,
				"limit_down":   s.LimitDown,
				"ma5":          s.MA5,
				"ma20":         s.MA20,
				"rsi_14":       s.RSI14,
				"volume_ratio": s.VolumeRatio,
			})
		}
	}

	return map[string]interface{}{
		"market":         exchangeStr,
		"advancers":      totalAdvancers,
		"decliners":      totalDecliners,
		"flat":           totalFlat,
		"limit_up":       totalLimitUp,
		"limit_down":     totalLimitDown,
		"breadth":        fmt.Sprintf("上涨%d/下跌%d/平盘%d", totalAdvancers, totalDecliners, totalFlat),
		"limit_status":   fmt.Sprintf("涨停%d/跌停%d", totalLimitUp, totalLimitDown),
		"sampled_stocks": sampledStocks[:min(len(sampledStocks), 50)],
		"source":         "local_reader",
	}, nil
}

// TDXCommonStocksToolV2 支持多数据源的常见A股快速查询工具
type TDXCommonStocksToolV2 struct {
	manager *TDXServiceManager
}

// NewTDXCommonStocksToolV2 创建支持多数据源的通达信搜索工具
func NewTDXCommonStocksToolV2(manager *TDXServiceManager) *TDXCommonStocksToolV2 {
	return &TDXCommonStocksToolV2{
		manager: manager,
	}
}

func (t *TDXCommonStocksToolV2) Name() string {
	return "get_tdx_common_stocks"
}

func (t *TDXCommonStocksToolV2) Description() string {
	return "获取A股核心蓝筹/白马股的最新行情数据和技术指标（贵州茅台、五粮液、招商银行、平安银行等）"
}

func (t *TDXCommonStocksToolV2) GetDefinition() ToolDefinition {
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

func (t *TDXCommonStocksToolV2) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	if t.manager == nil {
		return nil, fmt.Errorf("TDXServiceManager not initialized")
	}

	commonStocks := tdx.CommonAStockCodes()
	var results []map[string]interface{}

	for _, stock := range commonStocks {
		// 使用 manager 获取数据
		data, err := t.manager.GetStockData(stock.Exchange, stock.Code, 60, true)
		if err != nil {
			continue
		}

		// 计算技术指标
		var changePct float64
		if dataMap, ok := data["data"].([]interface{}); ok && len(dataMap) >= 2 {
			latest := dataMap[len(dataMap)-1].(map[string]interface{})
			prev := dataMap[len(dataMap)-2].(map[string]interface{})
			if prevClose, ok := prev["close"].(float64); ok && prevClose > 0 {
				if close, ok := latest["close"].(float64); ok {
					changePct = (close/prevClose - 1) * 100
				}
			}
		}

		item := map[string]interface{}{
			"code":       stock.Code,
			"name":       stock.Name,
			"exchange":   stock.Exchange,
			"change_pct": roundTo2(changePct),
			"volume":     data["data"], // 简化处理
			"date":       data,
		}

		if indicators, ok := data["indicators"].(map[string]interface{}); ok {
			item["indicators"] = indicators
		}

		results = append(results, item)
	}

	return map[string]interface{}{
		"total":  len(results),
		"stocks": results,
		"source": t.manager.GetActiveProvider(),
	}, nil
}
