package tools

import (
	"context"
	"fmt"
	"log"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/quantpilot/quantpilot/internal/data"
	"github.com/quantpilot/quantpilot/internal/tdx"
	"github.com/quantpilot/quantpilot/internal/tdxterm"
)

// min 返回两个值中的较小值
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// 数据源常量统一收口于 internal/data 包（DataProviderNativeName / DataProviderMCPName /
// DataProviderTerminalName / DataProviderTencentName），本包一律通过 data.DataProviderXName 引用，
// 不再各自定义，确保全程序数据源命名唯一。

// TDXServiceManager TDX 数据服务管理器，支持多数据源切换
type TDXServiceManager struct {
	provider        string
	nativeService   *tdx.TDXService
	mcpClient       *tdx.MCPClient
	terminalService *tdxterm.Service
	tdxPath         string // 通达信安装目录，用于定位 TPythClient.dll
	mcpURL          string
	mcpAPIKey       string
	duckdbMgr       *data.DuckDBManager
}

// NewTDXServiceManager 创建 TDX 服务管理器
func NewTDXServiceManager(provider, mcpURL, mcpAPIKey, tdxPath string) *TDXServiceManager {
	m := &TDXServiceManager{
		provider:  provider,
		mcpURL:    mcpURL,
		mcpAPIKey: mcpAPIKey,
		tdxPath:   tdxPath,
	}

	// 初始化 Go Native TDX 客户端（异步连接，避免阻塞启动流程导致错过盘前任务）。
	// 无论主数据源为何种均初始化：各数据源（native / terminal / MCP）并行共存，
	// native 在指数等场景提供实时兜底数据（gotdx 实时K线），避免终端取不到指数时无数据。
	if m.nativeService == nil {
		m.nativeService = tdx.NewTDXService()
		log.Printf("[TDX] Native TDX client initializing (async connect)...")
		go func() {
			// 网络层 panic 不得拖垮主程序，捕获后转入按需重连
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[TDX] Native TDX connect panicked (recovered): %v", r)
				}
			}()
			if err := m.nativeService.Connect(); err != nil {
				log.Printf("[TDX] Native TDX auto-connect failed: %v (will retry on demand)", err)
			} else {
				log.Printf("[TDX] Native TDX client connected successfully")
			}
		}()
	}

	// 初始化 MCP 客户端
	if provider == data.DataProviderMCPName && mcpURL != "" {
		m.mcpClient = tdx.NewMCPClient(mcpURL, mcpAPIKey)
		log.Printf("[TDX] MCP client initialized: %s", mcpURL)
	}

	// 初始化终端接口客户端（通达信主程序必须已登录）
	if provider == data.DataProviderTerminalName {
		if svc, err := tdxterm.NewService(tdxPath, "QuantBot"); err != nil {
			log.Printf("[TDX] Terminal interface init failed: %v", err)
		} else {
			m.terminalService = svc
			log.Printf("[TDX] Terminal interface service initialized (dll dir: %s)", tdxPath)
		}
	}

	return m
}

// SetProvider 切换数据源
func (m *TDXServiceManager) SetProvider(provider string) {
	oldProvider := m.provider
	m.provider = provider
	log.Printf("[TDX] Data provider switched from %s to: %s", oldProvider, provider)

	// 切换到 native_tdx 时自动初始化和连接（即使已存在也要重连）
	if provider == data.DataProviderNativeName {
		if m.nativeService == nil {
			m.nativeService = tdx.NewTDXService()
			log.Printf("[TDX] Native TDX client initializing on switch...")
		} else {
			log.Printf("[TDX] Native TDX client already exists, attempting reconnect...")
			// 关闭旧连接以便重新连接
			m.nativeService.Close()
		}
		if err := m.nativeService.Connect(); err != nil {
			log.Printf("[TDX] Native TDX auto-connect failed: %v (will retry on demand)", err)
		} else {
			log.Printf("[TDX] Native TDX client connected successfully")
		}
	}

	// 切换到终端接口时初始化服务（通达信主程序必须已登录）
	if provider == data.DataProviderTerminalName && m.terminalService == nil {
		if svc, err := tdxterm.NewService(m.tdxPath, "QuantBot"); err != nil {
			log.Printf("[TDX] Terminal interface init failed on switch: %v", err)
		} else {
			m.terminalService = svc
			log.Printf("[TDX] Terminal interface service initialized on switch")
		}
	}
}

// SetMCPConfig 设置 MCP 配置
func (m *TDXServiceManager) SetMCPConfig(url, apiKey string) {
	m.mcpURL = url
	m.mcpAPIKey = apiKey
	if url != "" {
		m.mcpClient = tdx.NewMCPClient(url, apiKey)
	}
}

// SetDuckDBManager 设置 DuckDB 管理器，用于板块/广度等需要历史数据的功能
func (m *TDXServiceManager) SetDuckDBManager(dm *data.DuckDBManager) {
	m.duckdbMgr = dm
}

// GetActiveProvider 获取当前活跃的数据源
func (m *TDXServiceManager) GetActiveProvider() string {
	return m.provider
}

// GetProvider 获取当前数据源（别名）
func (m *TDXServiceManager) GetProvider() string {
	return m.provider
}

// HasMCPClient 检查是否有 MCP 客户端
func (m *TDXServiceManager) HasMCPClient() bool {
	return m.mcpClient != nil
}

// HasNativeService 检查是否有 Native TDX 服务
func (m *TDXServiceManager) HasNativeService() bool {
	return m.nativeService != nil
}

// ConnectNativeTDX 连接 Go Native TDX 服务
func (m *TDXServiceManager) ConnectNativeTDX() error {
	if m.nativeService == nil {
		m.nativeService = tdx.NewTDXService()
	}
	return m.nativeService.Connect()
}

// SetNativeTDXServers 自定义 TDX 服务器列表
func (m *TDXServiceManager) SetNativeTDXServers(servers []tdx.TDXServer) {
	if m.nativeService == nil {
		m.nativeService = tdx.NewTDXService()
	}
	m.nativeService.SetServers(servers)
}

// TestNativeTDXConnection 测试原生 TDX 连接
func (m *TDXServiceManager) TestNativeTDXConnection() (bool, string) {
	if m.nativeService == nil {
		m.nativeService = tdx.NewTDXService()
	}
	return m.nativeService.TestConnection()
}

// CloseNativeTDX 关闭原生 TDX 连接
func (m *TDXServiceManager) CloseNativeTDX() {
	if m.nativeService != nil {
		m.nativeService.Close()
	}
}

// ==================== 数据获取方法 ====================

// GetStockData 获取个股日线数据（根据当前数据源直接获取，失败则报错）
func (m *TDXServiceManager) GetStockData(exchange, code string, days int, includeIndicators bool) (map[string]interface{}, error) {
	switch m.provider {
	case data.DataProviderNativeName:
		if m.nativeService != nil {
			return m.getStockDataFromNative(exchange, code, days)
		}
		return nil, fmt.Errorf("native_tdx service not initialized")
	case data.DataProviderMCPName:
		if m.mcpClient != nil {
			return m.getStockDataFromMCP(exchange, code, days)
		}
		return nil, fmt.Errorf("mcp service not initialized")
	case data.DataProviderTerminalName:
		if m.terminalService != nil {
			return m.getStockDataFromTerminal(exchange, code, days)
		}
		return nil, fmt.Errorf("tdx_terminal service not initialized")
	case data.DataProviderTencentName:
		return tencentStockDataMap(exchange, code, days)
	default:
		return nil, fmt.Errorf("unknown provider: %s", m.provider)
	}
}

// GetRealTimeQuote 获取实时快照（根据当前数据源直接获取，失败则报错）
func (m *TDXServiceManager) GetRealTimeQuote(exchange, code string) (map[string]interface{}, error) {
	switch m.provider {
	case data.DataProviderNativeName:
		if m.nativeService == nil {
			return nil, fmt.Errorf("native_tdx service not initialized")
		}
		quotes, err := m.nativeService.GetQuotes(exchange, []string{code})
		if err != nil {
			return nil, fmt.Errorf("native_tdx GetQuotes failed: %w", err)
		}
		if len(quotes) > 0 {
			q := quotes[0]
			changePct := 0.0
			if q.LastClose > 0 {
				changePct = (q.Price/q.LastClose - 1) * 100
			}
			return map[string]interface{}{
				"price":       q.Price,
				"open":        q.Open,
				"high":        q.High,
				"low":         q.Low,
				"volume":      q.Volume,
				"amount":      q.Amount,
				"last_close":  q.LastClose,
				"change_pct":  changePct,
				"is_realtime": true,
				"source":      "native_tdx",
				"bid_prices":  q.BidPrices,
				"ask_prices":  q.AskPrices,
				"bid_volumes": q.BidVolumes,
				"ask_volumes": q.AskVolumes,
			}, nil
		}
		return nil, fmt.Errorf("no quote data returned from native_tdx")
	case data.DataProviderMCPName:
		if m.mcpClient != nil {
			return m.mcpClient.GetTDXQuote(exchange, code)
		}
		return nil, fmt.Errorf("mcp service not initialized")
	case data.DataProviderTerminalName:
		if m.terminalService == nil {
			return nil, fmt.Errorf("tdx_terminal service not initialized")
		}
		return m.getQuoteFromTerminal(exchange, code)
	case data.DataProviderTencentName:
		return tencentQuoteMap(exchange, code)
	default:
		return nil, fmt.Errorf("unknown provider: %s", m.provider)
	}
}

// GetKline 获取K线数据（支持分钟级，根据当前数据源直接获取，失败则报错）
func (m *TDXServiceManager) GetKline(exchange, code, period string, count int, adjust string) (map[string]interface{}, error) {
	switch m.provider {
	case data.DataProviderNativeName:
		if m.nativeService == nil {
			return nil, fmt.Errorf("native_tdx service not initialized")
		}
		bars, err := m.nativeService.GetKline(exchange, code, period, uint16(count))
		if err != nil {
			return nil, fmt.Errorf("native_tdx GetKline failed: %w", err)
		}
		data := make([]interface{}, len(bars))
		for i, b := range bars {
			data[i] = map[string]interface{}{
				"date":   b.Date,
				"open":   b.Open,
				"high":   b.High,
				"low":    b.Low,
				"close":  b.Close,
				"volume": b.Volume,
				"amount": b.Amount,
			}
		}
		return map[string]interface{}{
			"exchange": exchange,
			"code":     code,
			"period":   period,
			"count":    len(bars),
			"adjust":   adjust,
			"klines":   data,
			"source":   "native_tdx",
		}, nil
	case data.DataProviderMCPName:
		if m.mcpClient != nil {
			return m.mcpClient.GetTDXKline(exchange, code, period, count, adjust)
		}
		return nil, fmt.Errorf("mcp service not initialized")
	case data.DataProviderTerminalName:
		if m.terminalService == nil {
			return nil, fmt.Errorf("tdx_terminal service not initialized")
		}
		bars, err := m.getKlinesFromTerminal(exchange, code, period, count)
		if err != nil {
			return nil, err
		}
		data := make([]interface{}, len(bars))
		for i, b := range bars {
			data[i] = b
		}
		return map[string]interface{}{
			"exchange": exchange,
			"code":     code,
			"period":   period,
			"count":    len(bars),
			"adjust":   adjust,
			"klines":   data,
			"source":   "tdx_terminal",
		}, nil
	case data.DataProviderTencentName:
		return tencentKlineMap(exchange, code, period, count, adjust)
	default:
		return nil, fmt.Errorf("unknown provider: %s", m.provider)
	}
}

// GetTrades 获取逐笔成交
func (m *TDXServiceManager) GetTrades(exchange, code string) (map[string]interface{}, error) {
	switch m.provider {
	case data.DataProviderNativeName:
		if m.nativeService != nil {
			transactions, err := m.nativeService.GetTransactions(exchange, code, 500)
			if err != nil {
				return nil, err
			}
			data := make([]interface{}, len(transactions))
			for i, t := range transactions {
				data[i] = map[string]interface{}{
					"price":  t.Price,
					"volume": t.Volume,
					"time":   t.Time,
					"bs":     t.BS,
				}
			}
			return map[string]interface{}{
				"exchange":     exchange,
				"code":         code,
				"count":        len(transactions),
				"transactions": data,
				"source":       "native_tdx",
			}, nil
		}
	case data.DataProviderMCPName:
		if m.mcpClient != nil {
			return m.mcpClient.GetTDXTrades(exchange, code)
		}
	}

	return nil, fmt.Errorf("trades require native_tdx or mcp provider")
}

// GetF10 获取F10资讯
func (m *TDXServiceManager) GetF10(exchange, code string) (map[string]interface{}, error) {
	switch m.provider {
	case data.DataProviderMCPName:
		if m.mcpClient != nil {
			return m.mcpClient.GetTDXF10(exchange, code)
		}
	}

	return nil, fmt.Errorf("F10 requires mcp provider")
}

// SearchStocks 搜索股票（基于 DuckDB 行情数据库）
func (m *TDXServiceManager) SearchStocks(exchange, query string, limit int) ([]tdx.StockInfo, error) {
	if m.duckdbMgr == nil || !m.duckdbMgr.HasStockDB() {
		return nil, fmt.Errorf("DuckDB 行情数据库未挂载")
	}

	symbols, err := m.duckdbMgr.ListAllSymbolsFromStock(context.Background(), exchange, limit)
	if err != nil {
		return nil, fmt.Errorf("从 DuckDB 获取股票列表失败: %w", err)
	}

	var results []tdx.StockInfo
	for _, s := range symbols {
		code := normalizeStockCode(s.Symbol)
		if query != "" && !strings.Contains(strings.ToLower(code), strings.ToLower(query)) {
			continue
		}
		results = append(results, tdx.StockInfo{
			Code:     code,
			Exchange: strings.ToLower(s.Market),
		})
	}
	return results, nil
}

// ==================== 板块数据 ====================

// GetBoardList 获取板块列表（基于 DictLoader 行业分类）
func (m *TDXServiceManager) GetBoardList(boardType string) ([]map[string]interface{}, error) {
	return m.getBoardListFromLocal(boardType)
}

// getBoardListFromLocal 从本地数据获取板块列表
func (m *TDXServiceManager) getBoardListFromLocal(boardType string) ([]map[string]interface{}, error) {
	dictLoader := data.GetDictLoader()
	if dictLoader == nil {
		return nil, fmt.Errorf("dict loader not available")
	}

	// 获取行业分类
	industries := make(map[string]int) // 行业名称 -> 成分股数量
	allStocks := dictLoader.GetAllStocks()

	for _, stock := range allStocks {
		industry := dictLoader.GetIndustryByStock(stock.Code)
		if industry != "" && industry != "通用" {
			industries[industry]++
		}
	}

	// 构建板块列表
	boards := make([]map[string]interface{}, 0)
	for name, count := range industries {
		boards = append(boards, map[string]interface{}{
			"code":  name, // 使用行业名称作为标识
			"name":  name,
			"count": count,
			"type":  "industry",
		})
	}

	// 按名称排序
	sort.Slice(boards, func(i, j int) bool {
		return boards[i]["name"].(string) < boards[j]["name"].(string)
	})

	log.Printf("[TDXManager] Board list loaded from local: %d industries", len(boards))
	return boards, nil
}

// GetBoardSummary 获取板块行情汇总
func (m *TDXServiceManager) GetBoardSummary(boardCode string, includeMembers bool) (map[string]interface{}, error) {
	return m.getBoardSummaryFromLocal(boardCode, includeMembers)
}

// getBoardSummaryFromLocal 从本地数据获取板块行情汇总
func (m *TDXServiceManager) getBoardSummaryFromLocal(boardCode string, includeMembers bool) (map[string]interface{}, error) {
	dictLoader := data.GetDictLoader()
	if dictLoader == nil {
		return nil, fmt.Errorf("dict loader not available")
	}

	// 获取该行业的所有成分股
	stocks := dictLoader.GetStocksByIndustry(boardCode)
	if len(stocks) == 0 {
		// 尝试反向查找：boardCode可能是行业名称
		for _, stock := range dictLoader.GetAllStocks() {
			industry := dictLoader.GetIndustryByStock(stock.Code)
			if industry == boardCode {
				stocks = append(stocks, stock)
			}
		}
	}

	if len(stocks) == 0 {
		return nil, fmt.Errorf("no stocks found for board: %s", boardCode)
	}

	// 获取成分股行情
	members := make([]map[string]interface{}, 0)
	totalChange := 0.0
	advancers := 0
	decliners := 0

	for _, stock := range stocks {
		// 获取行情
		quote, err := m.GetRealTimeQuote("", stock.Code)
		if err != nil {
			continue
		}

		if quote != nil {
			price := 0.0
			if p, ok := quote["price"].(float64); ok {
				price = p
			}
			changePct := 0.0
			if cp, ok := quote["change_pct"].(float64); ok {
				changePct = cp
			}
			lastClose := 0.0
			if lc, ok := quote["last_close"].(float64); ok {
				lastClose = lc
			}

			if lastClose > 0 && price > 0 {
				changePct = (price/lastClose - 1) * 100
			}

			totalChange += changePct
			if changePct > 0 {
				advancers++
			} else if changePct < 0 {
				decliners++
			}

			member := map[string]interface{}{
				"code":        stock.Code,
				"name":        stock.Name,
				"price":       price,
				"change_pct":  changePct,
				"volume":      quote["volume"],
				"change_sign": map[bool]string{true: "up", false: "down"}[changePct >= 0],
			}
			members = append(members, member)
		}
	}

	avgChange := 0.0
	if len(stocks) > 0 {
		avgChange = totalChange / float64(len(stocks))
	}

	result := map[string]interface{}{
		"code":       boardCode,
		"name":       boardCode,
		"count":      len(stocks),
		"advancers":  advancers,
		"decliners":  decliners,
		"avg_change": avgChange,
		"net_flow":   0,
		"source":     "industry_classification",
	}

	if includeMembers {
		result["members"] = members[:min(len(members), 50)] // 最多返回50只
	}

	return result, nil
}

// ==================== 资金流向 ====================

// GetFundFlow 获取历史资金流向（从K线数据估算）
func (m *TDXServiceManager) GetFundFlow(exchange, code string) ([]map[string]interface{}, error) {
	return m.estimateFundFlowFromKlines(exchange, code)
}

// estimateFundFlowFromKlines 从K线数据估算资金流向
func (m *TDXServiceManager) estimateFundFlowFromKlines(exchange, code string) ([]map[string]interface{}, error) {
	// 获取最近60天的K线
	bars, err := m.GetKline(exchange, code, "day", 60, "none")
	if err != nil {
		return nil, fmt.Errorf("failed to get klines for fund flow estimation: %w", err)
	}

	klines, ok := bars["klines"].([]interface{})
	if !ok || len(klines) == 0 {
		return nil, fmt.Errorf("no kline data for fund flow")
	}

	flows := make([]map[string]interface{}, 0, len(klines))
	for i, k := range klines {
		if klineMap, ok := k.(map[string]interface{}); ok {
			volume := 0.0
			amount := 0.0
			close := 0.0
			if v, ok := klineMap["volume"].(float64); ok {
				volume = v
			}
			if a, ok := klineMap["amount"].(float64); ok {
				amount = a
			}
			if c, ok := klineMap["close"].(float64); ok {
				close = c
			}

			// 简易资金流向估算：上涨日资金流入，下跌日资金流出
			moneyFlow := 0.0
			if i > 0 {
				prevClose := 0.0
				if pc, ok := klines[i-1].(map[string]interface{}); ok {
					if c, ok := pc["close"].(float64); ok {
						prevClose = c
					}
				}
				if prevClose > 0 {
					if close >= prevClose {
						moneyFlow = amount * 0.6 // 上涨日约60%成交额视为流入
					} else {
						moneyFlow = -amount * 0.4 // 下跌日约40%成交额视为流出
					}
				}
			}

			date := ""
			if d, ok := klineMap["date"].(string); ok {
				date = d
			}

			flows = append(flows, map[string]interface{}{
				"date":       date,
				"close":      close,
				"volume":     volume,
				"amount":     amount,
				"money_flow": moneyFlow,
				"source":     "estimated_from_klines",
			})
		}
	}

	return flows, nil
}

// GetCapitalFlow 获取实时资金流向
func (m *TDXServiceManager) GetCapitalFlow(exchange, code string) (map[string]interface{}, error) {
	// 估算今日资金流向
	quote, err := m.GetRealTimeQuote(exchange, code)
	if err != nil {
		return nil, fmt.Errorf("failed to get quote for capital flow: %w", err)
	}

	price := 0.0
	volume := 0.0
	amount := 0.0
	lastClose := 0.0

	if p, ok := quote["price"].(float64); ok {
		price = p
	}
	if v, ok := quote["volume"].(int64); ok {
		volume = float64(v)
	}
	if a, ok := quote["amount"].(float64); ok {
		amount = a
	}
	if lc, ok := quote["last_close"].(float64); ok {
		lastClose = lc
	}

	moneyFlow := 0.0
	if lastClose > 0 && amount > 0 {
		if price >= lastClose {
			moneyFlow = amount * 0.5
		} else {
			moneyFlow = -amount * 0.3
		}
	}

	return map[string]interface{}{
		"code":       code,
		"exchange":   exchange,
		"price":      price,
		"volume":     volume,
		"amount":     amount,
		"money_flow": moneyFlow,
		"source":     "estimated_from_quote",
	}, nil
}

// ==================== 公告 ====================

// GetAnnouncement 获取公告
func (m *TDXServiceManager) GetAnnouncement(code string, count int) ([]map[string]interface{}, error) {
	// 公告数据需通过巨潮资讯网（cninfo）等专用数据源获取
	log.Printf("[TDXManager] Announcement requested for %s but not available in current data source", code)
	return nil, fmt.Errorf("announcements are not available in current data source; use cninfo service")
}

// ==================== 技术指标 ====================

// GetIndicator 计算技术指标（根据当前数据源直接获取，失败则报错）
func (m *TDXServiceManager) GetIndicator(exchange, code, indicator, period string, count int) (map[string]interface{}, error) {
	switch m.provider {
	case data.DataProviderNativeName:
		if m.nativeService == nil {
			return nil, fmt.Errorf("native_tdx service not initialized")
		}
		bars, err := m.nativeService.GetKline(exchange, code, period, uint16(count))
		if err != nil {
			return nil, fmt.Errorf("native_tdx GetKline for indicator failed: %w", err)
		}
		if len(bars) == 0 {
			return nil, fmt.Errorf("no kline data from native_tdx for indicator calculation")
		}
		klineBars := make([]tdx.DayBar, len(bars))
		for i, b := range bars {
			klineBars[i] = tdx.DayBar{
				Date:   parseDateToInt(b.Date),
				Open:   b.Open,
				High:   b.High,
				Low:    b.Low,
				Close:  b.Close,
				Amount: b.Amount,
				Volume: b.Volume,
			}
		}
		return m.calculateIndicator(exchange, code, indicator, period, count, klineBars, "native_tdx_calculated")

	default:
		return nil, fmt.Errorf("unknown provider: %s", m.provider)
	}
}

// calculateIndicator 根据K线数据计算技术指标
func (m *TDXServiceManager) calculateIndicator(exchange, code, indicator, period string, count int, klineBars []tdx.DayBar, dataSource string) (map[string]interface{}, error) {
	indicators := tdx.CalculateIndicators(klineBars)
	if indicators == nil {
		return nil, fmt.Errorf("failed to calculate indicators")
	}

	result := map[string]interface{}{
		"exchange":  exchange,
		"code":      code,
		"indicator": indicator,
		"period":    period,
		"count":     count,
		"source":    dataSource,
	}

	switch indicator {
	case "ma":
		result["ma5"] = indicators.MA5
		result["ma10"] = indicators.MA10
		result["ma20"] = indicators.MA20
		result["ma60"] = indicators.MA60
	case "macd":
		result["macd_dif"] = indicators.MACD_DIF
		result["macd_dea"] = indicators.MACD_DEA
		result["macd"] = indicators.MACD
	case "rsi":
		result["rsi_14"] = indicators.RSI14
	case "all":
		result["ma5"] = indicators.MA5
		result["ma10"] = indicators.MA10
		result["ma20"] = indicators.MA20
		result["ma60"] = indicators.MA60
		result["rsi_14"] = indicators.RSI14
		result["macd_dif"] = indicators.MACD_DIF
		result["macd_dea"] = indicators.MACD_DEA
		result["macd"] = indicators.MACD
		result["volatility_20"] = indicators.Volatility20
		result["change_pct"] = indicators.ChangePct
		result["change_5d"] = indicators.Change5d
		result["change_20d"] = indicators.Change20d
		result["high_20"] = indicators.High20
		result["low_20"] = indicators.Low20
		result["volume_ratio"] = indicators.VolumeRatio
	default:
		return nil, fmt.Errorf("unknown indicator type: %s", indicator)
	}

	return result, nil
}

// ==================== 分时数据 ====================

// GetMinuteBars 获取分时数据
func (m *TDXServiceManager) GetMinuteBars(exchange, code string, count int) ([]map[string]interface{}, error) {
	switch m.provider {
	case data.DataProviderNativeName:
		if m.nativeService != nil {
			bars, err := m.nativeService.GetMinuteBars(exchange, code, uint16(count))
			if err != nil {
				log.Printf("[TDXManager] Native TDX minute bars failed: %v", err)
			} else {
				data := make([]map[string]interface{}, len(bars))
				for i, b := range bars {
					data[i] = map[string]interface{}{
						"time":      b.Time,
						"price":     b.Price,
						"volume":    b.Volume,
						"avg_price": b.AvgPrice,
					}
				}
				return data, nil
			}
		}
	}

	return nil, fmt.Errorf("minute bars only available with native_tdx provider")
}

// ==================== 市场统计 ====================

// GetMarketStats 获取市场统计（基于 DuckDB 行情数据库）
func (m *TDXServiceManager) GetMarketStats(exchange string, sampleSize int) (map[string]interface{}, error) {
	if m.duckdbMgr == nil || !m.duckdbMgr.HasStockDB() {
		return nil, fmt.Errorf("DuckDB 行情数据库未挂载")
	}

	ctx := context.Background()
	breadth, err := m.duckdbMgr.GetMarketBreadth(ctx, exchange)
	if err != nil {
		return nil, fmt.Errorf("获取市场统计失败: %w", err)
	}

	return map[string]interface{}{
		"market":       exchange,
		"total":        breadth.Total,
		"advancers":    breadth.Advancers,
		"decliners":    breadth.Decliners,
		"flat":         breadth.Flat,
		"limit_up":     0,
		"limit_down":   0,
		"breadth":      fmt.Sprintf("上涨%d/下跌%d/平盘%d", breadth.Advancers, breadth.Decliners, breadth.Flat),
		"limit_status": "涨停0/跌停0",
		"source":       "duckdb",
	}, nil
}

// ==================== 内部数据获取方法 ====================

func (m *TDXServiceManager) getStockDataFromNative(exchange, code string, days int) (map[string]interface{}, error) {
	if m.nativeService == nil {
		return nil, fmt.Errorf("native service not available")
	}

	bars, err := m.nativeService.GetKline(exchange, code, "day", uint16(days))
	if err != nil {
		return nil, fmt.Errorf("native TDX 获取 %s%s 数据失败: %w", exchange, code, err)
	}

	if len(bars) == 0 {
		return nil, fmt.Errorf("no data for %s%s from native TDX", exchange, code)
	}

	dataPoints := make([]map[string]interface{}, len(bars))
	for i, bar := range bars {
		dataPoints[i] = map[string]interface{}{
			"date":   bar.Date,
			"open":   bar.Open,
			"high":   bar.High,
			"low":    bar.Low,
			"close":  bar.Close,
			"volume": bar.Volume,
			"amount": bar.Amount,
		}
	}

	// 计算技术指标
	var indicatorsMap map[string]interface{}
	if len(bars) >= 20 {
		tdxBars := make([]tdx.DayBar, len(bars))
		for i, b := range bars {
			tdxBars[i] = tdx.DayBar{
				Date:   parseDateToInt(b.Date),
				Open:   b.Open,
				High:   b.High,
				Low:    b.Low,
				Close:  b.Close,
				Amount: b.Amount,
				Volume: b.Volume,
			}
		}
		indicators := tdx.CalculateIndicators(tdxBars)
		if indicators != nil {
			indicatorsMap = map[string]interface{}{
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

	result := map[string]interface{}{
		"exchange": exchange,
		"code":     code,
		"days":     len(bars),
		"data":     dataPoints,
		"source":   "native_tdx",
	}

	if indicatorsMap != nil {
		result["indicators"] = indicatorsMap
	}

	return result, nil
}

// parseDateToInt 将 "YYYY-MM-DD" 格式字符串转为 YYYYMMDD 整数
func parseDateToInt(dateStr string) int {
	var year, month, day int
	fmt.Sscanf(dateStr, "%d-%d-%d", &year, &month, &day)
	return year*10000 + month*100 + day
}

func (m *TDXServiceManager) getStockDataFromMCP(exchange, code string, days int) (map[string]interface{}, error) {
	if m.mcpClient == nil {
		return nil, fmt.Errorf("mcp client not available")
	}

	result, err := m.mcpClient.GetTDXKline(exchange, code, "day", days, "none")
	if err != nil {
		return nil, fmt.Errorf("MCP error: %w", err)
	}

	return map[string]interface{}{
		"exchange": exchange,
		"code":     code,
		"data":     result,
		"source":   "mcp",
	}, nil
}

// ==================== 终端接口（tdx_terminal）数据获取 ====================

// mapTerminalPeriod 将系统常用周期名映射为终端接口周期。
func mapTerminalPeriod(period string) string {
	switch period {
	case "day", "1d", "d":
		return "1d"
	case "week", "1w", "w":
		return "1w"
	case "month", "1mon", "mon":
		return "1mon"
	case "1m":
		return "1m"
	case "5m":
		return "5m"
	case "15m":
		return "15m"
	case "30m":
		return "30m"
	case "60m", "1h":
		return "1h"
	default:
		return period
	}
}

// toTerminalCode 构建终端接口使用的完整证券代码（如 "600000.SH"）。
func toTerminalCode(exchange, code string) string {
	if exchange != "" {
		// 识别 sh/sz/bj 前缀，统一大写后缀
		switch strings.ToLower(exchange) {
		case "sh", "上海":
			return strings.ToUpper(code) + ".SH"
		case "sz", "深圳":
			return strings.ToUpper(code) + ".SZ"
		case "bj", "北京":
			return strings.ToUpper(code) + ".BJ"
		default:
			return code
		}
	}
	return code
}

// terminalSymbolForIndex 返回指数在终端接口中的专用代码；非指数或无需特例时返回空串。
// 通达信脚本行情接口对上证指数等不能用公开代码（如 000001.SH），需用专用代码（999999.SH）。
func terminalSymbolForIndex(exchange, code string) string {
	switch strings.ToLower(exchange) + ":" + code {
	case "sh:000001": // 上证指数
		return "999999.SH"
	case "sz:399001": // 深证成指
		return "399001.SZ"
	case "sh:000300": // 沪深300
		return "000300.SH"
	case "sh:000905": // 中证500
		return "000905.SH"
	case "sh:000016": // 上证50
		return "000016.SH"
	}
	return ""
}

// safeGetKline 调用 gotdx 获取K线并捕获可能的 panic（gotdx 解析部分指数时存在 slice bounds 越界），
// 确保单个指数失败不会拖垮整个实时指数快照批次。
func safeGetKline(svc *tdx.TDXService, exchange, code string) (bars []tdx.KlineBar, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("gotdx GetKline panic: %v", r)
			bars = nil
		}
	}()
	return svc.GetKline(exchange, code, "day", 2)
}

// fieldArray 读取终端 K 线结果中某字段的数组。
func fieldArray(stockData map[string]interface{}, field string) []interface{} {
	// 通达信终端返回大写字段（Date/Time/Open/High/Low/Close/Volume/Amount），
	// 本函数做大小写不敏感匹配，兼容大小写差异。
	if v, ok := stockData[field].([]interface{}); ok {
		return v
	}
	for k, v := range stockData {
		if strings.EqualFold(k, field) {
			if arr, ok := v.([]interface{}); ok {
				return arr
			}
		}
	}
	return nil
}

// fieldNum 取字段数组指定下标处的数值。
func fieldNum(arr []interface{}, i int) float64 {
	if arr == nil || i < 0 || i >= len(arr) {
		return 0
	}
	switch n := arr[i].(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case string:
		var f float64
		if _, err := fmt.Sscanf(n, "%f", &f); err == nil {
			return f
		}
	}
	return 0
}

// getStockDataFromTerminal 通过终端接口获取个股日线数据（含技术指标）。
func (m *TDXServiceManager) getStockDataFromTerminal(exchange, code string, days int) (map[string]interface{}, error) {
	bars, err := m.getKlinesFromTerminal(exchange, code, "day", days)
	if err != nil {
		return nil, fmt.Errorf("终端接口获取 %s%s 数据失败: %w", exchange, code, err)
	}
	if len(bars) == 0 {
		return nil, fmt.Errorf("no data for %s%s from tdx_terminal", exchange, code)
	}

	dataPoints := make([]map[string]interface{}, len(bars))
	for i, bar := range bars {
		dataPoints[i] = bar
	}

	var indicatorsMap map[string]interface{}
	if len(bars) >= 20 {
		tdxBars := make([]tdx.DayBar, len(bars))
		for i, b := range bars {
			tdxBars[i] = tdx.DayBar{
				Date:   parseDateToInt(b["date"].(string)),
				Open:   b["open"].(float64),
				High:   b["high"].(float64),
				Low:    b["low"].(float64),
				Close:  b["close"].(float64),
				Amount: b["amount"].(float64),
				Volume: int64(b["volume"].(float64)),
			}
		}
		if indicators := tdx.CalculateIndicators(tdxBars); indicators != nil {
			indicatorsMap = map[string]interface{}{
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

	result := map[string]interface{}{
		"exchange": exchange,
		"code":     code,
		"days":     len(bars),
		"data":     dataPoints,
		"source":   "tdx_terminal",
	}
	if indicatorsMap != nil {
		result["indicators"] = indicatorsMap
	}
	return result, nil
}

// getKlinesFromTerminal 通过终端接口获取 K 线数组（[]map，字段含 date/open/high/low/close/volume/amount）。
func (m *TDXServiceManager) getKlinesFromTerminal(exchange, code, period string, count int) ([]map[string]interface{}, error) {
	if m.terminalService == nil {
		return nil, fmt.Errorf("tdx_terminal service not initialized")
	}
	// 指数在终端有专用代码（如上证指数 sh000001 需用 999999.SH，直接用 000001.SH 取不到数据）
	fq := terminalSymbolForIndex(exchange, code)
	if fq == "" {
		fq = toTerminalCode(exchange, code)
	}
	value, err := m.terminalService.GetMarketData([]string{fq}, nil, mapTerminalPeriod(period), "", "", count, "none")
	if err != nil {
		return nil, err
	}

	// 单只股票请求，返回 Value 仅含一个 key
	var stockData map[string]interface{}
	for _, v := range value {
		stockData, _ = v.(map[string]interface{})
		break
	}
	if stockData == nil {
		return nil, fmt.Errorf("终端接口未返回 %s 的K线数据", fq)
	}

	dates := fieldArray(stockData, "date")
	opens := fieldArray(stockData, "open")
	highs := fieldArray(stockData, "high")
	lows := fieldArray(stockData, "low")
	closes := fieldArray(stockData, "close")
	volumes := fieldArray(stockData, "volume")
	amounts := fieldArray(stockData, "amount")

	n := len(dates)
	if n == 0 {
		n = len(closes)
	}

	bars := make([]map[string]interface{}, 0, n)
	for i := 0; i < n; i++ {
		date := ""
		if dates != nil && i < len(dates) {
			date, _ = dates[i].(string)
		}
		bars = append(bars, map[string]interface{}{
			"date":   date,
			"open":   fieldNum(opens, i),
			"high":   fieldNum(highs, i),
			"low":    fieldNum(lows, i),
			"close":  fieldNum(closes, i),
			"volume": fieldNum(volumes, i),
			"amount": fieldNum(amounts, i),
		})
	}
	return bars, nil
}

// getQuoteFromTerminal 通过终端接口获取实时快照（以最近两日收盘估算涨跌幅）。
func (m *TDXServiceManager) getQuoteFromTerminal(exchange, code string) (map[string]interface{}, error) {
	bars, err := m.getKlinesFromTerminal(exchange, code, "day", 3)
	if err != nil {
		return nil, fmt.Errorf("终端接口获取快照失败: %w", err)
	}
	if len(bars) == 0 {
		return nil, fmt.Errorf("终端接口未返回 %s 的行情数据", code)
	}

	last := bars[len(bars)-1]
	lastClose := 0.0
	if len(bars) >= 2 {
		lastClose = bars[len(bars)-2]["close"].(float64)
	}
	price := last["close"].(float64)
	changePct := 0.0
	if lastClose > 0 {
		changePct = (price/lastClose - 1) * 100
	}

	return map[string]interface{}{
		"price":       price,
		"open":        last["open"],
		"high":        last["high"],
		"low":         last["low"],
		"volume":      last["volume"],
		"amount":      last["amount"],
		"last_close":  lastClose,
		"change_pct":  changePct,
		"is_realtime": false,
		"source":      "tdx_terminal",
	}, nil
}

// TestTerminalConnection 测试终端接口连接。
// 探测标的须选择行情接口必定返回的有流动性 A 股，避免使用上证指数（1#000001
// 在脚本行情接口下常取不到数据导致误判连接失败）。这里用浦发银行 600000.SH。
func (m *TDXServiceManager) TestTerminalConnection() (bool, string) {
	if m.terminalService == nil {
		if svc, err := tdxterm.NewService(m.tdxPath, "QuantBot"); err != nil {
			return false, fmt.Sprintf("终端接口初始化失败: %v", err)
		} else {
			m.terminalService = svc
		}
	}
	bars, err := m.getKlinesFromTerminal("sh", "600000", "day", 1)
	if err != nil {
		return false, fmt.Sprintf("终端接口读取数据失败（请确认通达信主程序已登录）: %v", err)
	}
	return len(bars) > 0, fmt.Sprintf("终端接口连接成功，读取到 %d 条K线", len(bars))
}

// Health 检查所有数据源状态
func (m *TDXServiceManager) Health() map[string]interface{} {
	status := map[string]interface{}{
		"current_provider": m.provider,
		"native_available": m.nativeService != nil,
	}

	if m.mcpClient != nil {
		status["mcp_available"] = true
	}
	if m.terminalService != nil {
		status["terminal_available"] = m.terminalService.Connected()
	}

	return status
}

// ==================== 通达信专业财务数据（GetProDataInStr / GetFinancialData） ====================

// tdxFinanceFields 通达信专业财务字段（FN 编码）到本系统财务指标的精简映射。
// 字段含义参考通达信官方 get_financial_data 文档（help.tdx.com.cn）。
// 注意：需先在通达信客户端中下载「专业财务数据」后这些 FN 字段才有值。
var tdxFinanceFields = []string{
	"tag_time", "announce_time",
	"FN1", "FN4", "FN6", "FN40", "FN63", "FN183", "FN184",
	"FN199", "FN201", "FN202", "FN206", "FN210", "FN230", "FN232", "FN233",
	"FN234", "FN238", "FN239",
}

// NewTDXFinancialFetcher 构造通达信终端财务拉取器，签名与 data.FinancialFetcher 一致，
// 供 data.DuckDBManager.SetFinancialFetcher 注入使用。返回 nil 时表示终端不可用。
// 通过通达信主程序 RPC（非 HTTP）读取专业财务数据，不触发东财频率封禁。
func (m *TDXServiceManager) NewTDXFinancialFetcher() data.FinancialFetcher {
	// 复用/惰性创建终端服务连接
	if m.terminalService == nil {
		svc, err := tdxterm.NewService(m.tdxPath, "QuantBot")
		if err != nil {
			log.Printf("[TDX] 终端服务初始化失败（财务拉取不可用）: %v", err)
			return nil
		}
		m.terminalService = svc
	}
	return func(ctx context.Context, symbol, startReport string) ([]data.FinancialReport, error) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		if m.terminalService == nil {
			return nil, fmt.Errorf("通达信终端服务未初始化")
		}
		secu := symbolToDotCode(symbol)
		if secu == "" {
			return nil, fmt.Errorf("无效股票代码: %s", symbol)
		}
		// 通达信字段均按报告期返回。start_time 官方默认可传空串''（表示不限起点，由客户端返回本地已有
		// 专业财务数据）；若传非报告期基准日（0311/0630/0930/1231）的日期，DLL 会判为非法 stime error。
		// 因此：增量时显式传起始报告期；全量时传空串''，让通达信返回本地全部已下载报告期。
		startYMD := ""
		if startReport != "" {
			startYMD = strings.ReplaceAll(startReport, "-", "")
		}
		raw, err := m.terminalService.GetFinancialData(
			[]string{secu}, tdxFinanceFields, startYMD, time.Now().Format("20060102"), "tag_time",
		)
		if err != nil {
			return nil, fmt.Errorf("通达信财务拉取失败: %w", err)
		}
		reports := parseTDXFinancial(symbol, secu, raw)
		// 增量过滤：只保留报告期 >= startReport 的条目
		if startReport != "" {
			kept := reports[:0]
			for _, r := range reports {
				if r.ReportDate >= startReport {
					kept = append(kept, r)
				}
			}
			reports = kept
		}
		sort.Slice(reports, func(i, j int) bool {
			return reports[i].ReportDate < reports[j].ReportDate
		})
		return reports, nil
	}
}

// symbolToDotCode 把标准代码（sh600519）转为通达信点号格式（600519.SH）。
func symbolToDotCode(symbol string) string {
	pure := data.PureCodeFromCode(symbol)
	if pure == "" {
		return ""
	}
	switch data.DetectMarketFromCode(symbol) {
	case "sz":
		return pure + ".SZ"
	case "bj":
		return pure + ".BJ"
	default:
		return pure + ".SH"
	}
}

// parseTDXFinancial 解析通达信 get_financial_data 返回值。
// 返回结构兼容两种形态：Value[股票代码]{FN:[]}·数组对齐各报告期，或 Value[股票代码]=单期对象。
func parseTDXFinancial(symbol, secu string, raw map[string]any) []data.FinancialReport {
	// 定位单只股票的数据：优先用点号代码键，其次任意键
	var entry any
	if v, ok := raw[secu]; ok {
		entry = v
	} else {
		for _, v := range raw {
			entry = v
			break
		}
	}
	obj, _ := entry.(map[string]any)
	if obj == nil {
		return nil
	}
	lists := map[string][]any{}
	for k, v := range obj {
		if arr, ok := v.([]any); ok {
			lists[k] = arr
		}
	}
	n := len(lists["tag_time"])
	if n == 0 && len(lists["announce_time"]) > 0 {
		n = len(lists["announce_time"])
	}
	if n == 0 {
		// 无 tag_time/announce_time 数组——可能是单期对象：仅取非数组字段
		single := map[string]any{}
		for k, v := range obj {
			if _, isArr := v.([]any); !isArr {
				single[k] = v
			}
		}
		if len(single) == 0 {
			return nil
		}
		reportsSingle := tdxEntryToReport(symbol, single)
		if reportsSingle != nil {
			return []data.FinancialReport{*reportsSingle}
		}
		return nil
	}

	reports := make([]data.FinancialReport, 0, n)
	for i := 0; i < n; i++ {
		row := map[string]any{}
		for k, arr := range lists {
			if i < len(arr) {
				row[k] = arr[i]
			}
		}
		if r := tdxEntryToReport(symbol, row); r != nil {
			reports = append(reports, *r)
		}
	}
	return reports
}

// tdxEntryToReport 将单期财务对象映射为 FinancialReport。TagTime/AnnounceTime
// 为 YYYYMMDD 整数，转换为 YYYY-MM-DD 字符串。
func tdxEntryToReport(symbol string, row map[string]any) *data.FinancialReport {
	tag := intStr(row["tag_time"])
	if tag == "" {
		return nil
	}
	rep := &data.FinancialReport{
		Symbol:       symbol,
		ReportDate:   intToDateStr(tag),
		AnnDate:      intToDateStr(intStr(row["announce_time"])),
		TotalRevenue: num(row["FN230"]),
		RevenueYOY:   num(row["FN183"]),
		NetProfit:    num(row["FN232"]),
		ProfitYOY:    num(row["FN184"]),
		NetProfitDed: num(row["FN233"]),
		Roe:          num(row["FN6"]),
		GrossMargin:  num(row["FN202"]),
		NetMargin:    num(row["FN199"]),
		TotalAssets:  num(row["FN40"]),
		TotalLiab:    num(row["FN63"]),
		DebtRatio:    num(row["FN210"]),
		OperCashflow: num(row["FN234"]),
		TotalShares:  num(row["FN238"]),
		FloatShares:  num(row["FN239"]),
		EPS:          num(row["FN1"]),
		BPS:          num(row["FN4"]),
	}
	if rep.NetMargin == 0 && num(row["FN201"]) != 0 {
		rep.NetMargin = num(row["FN201"])
	}
	if rep.NetProfitDed == 0 && num(row["FN206"]) != 0 {
		rep.NetProfitDed = num(row["FN206"])
	}
	return rep
}

// intStr 读取整数(可能为 float64)为字符串。
func intStr(v any) string {
	switch d := v.(type) {
	case float64:
		return fmt.Sprintf("%d", int64(d))
	case int:
		return fmt.Sprintf("%d", d)
	case int64:
		return fmt.Sprintf("%d", d)
	case string:
		return d
	default:
		return ""
	}
}

// intToDateStr 把 YYYYMMDD 字符串转为 YYYY-MM-DD；空串/非法返回空串。
func intToDateStr(s string) string {
	s = strings.TrimSpace(s)
	if len(s) != 8 {
		return ""
	}
	return s[0:4] + "-" + s[4:6] + "-" + s[6:8]
}

// num 读取 float64/数值/数字字符串为 float64。
func num(v any) float64 {
	switch d := v.(type) {
	case float64:
		return d
	case int:
		return float64(d)
	case int64:
		return float64(d)
	case int32:
		return float64(d)
	case string:
		f, _ := strconv.ParseFloat(strings.TrimSpace(d), 64)
		return f
	default:
		return 0
	}
}

// ==================== MarketDataProvider 实现 ====================

// TDXMarketDataProvider 实现 data.MarketDataProvider 接口
type TDXMarketDataProvider struct {
	manager *TDXServiceManager
}

// NewTDXMarketDataProvider 创建 TDX 行情数据提供者
func NewTDXMarketDataProvider(manager *TDXServiceManager) *TDXMarketDataProvider {
	return &TDXMarketDataProvider{manager: manager}
}

// Source 返回当前活跃数据源名称
func (p *TDXMarketDataProvider) Source() string {
	if p.manager == nil {
		return data.DataProviderNativeName
	}
	return p.manager.GetProvider()
}

// GetQuote 获取单只实时报价（含盘口），实现统一数据源接口
func (p *TDXMarketDataProvider) GetQuote(code string) (quote data.RealTimeQuote, retErr error) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[TDX Provider] GetQuote PANIC 已捕获: %v\n%s", r, debug.Stack())
			retErr = fmt.Errorf("GetQuote 内部异常: %v", r)
		}
	}()
	if p.manager == nil {
		return quote, fmt.Errorf("TDX manager not initialized")
	}
	exchange, pure := parseCode(code)
	raw, err := p.manager.GetRealTimeQuote(exchange, pure)
	if err != nil {
		return quote, err
	}
	price := getFloat(raw, "price")
	if price <= 0 {
		return quote, fmt.Errorf("no usable quote for %s", code)
	}
	quote = data.RealTimeQuote{
		Code:       pure,
		Name:       getString(raw, "name"),
		Price:      price,
		Open:       getFloat(raw, "open"),
		High:       getFloat(raw, "high"),
		Low:        getFloat(raw, "low"),
		PrevClose:  getFloat(raw, "last_close"),
		Volume:     getFloat(raw, "volume"),
		Amount:     getFloat(raw, "amount"),
		ChangePct:  getFloat(raw, "change_pct"),
		BidPrices:  getFloats(raw, "bid_prices"),
		AskPrices:  getFloats(raw, "ask_prices"),
		BidVolumes: getFloats(raw, "bid_volumes"),
		AskVolumes: getFloats(raw, "ask_volumes"),
		Source:     p.Source(),
		IsRealtime: true,
	}
	quote.Change = quote.Price - quote.PrevClose
	if quote.PrevClose > 0 && quote.ChangePct == 0 {
		quote.ChangePct = (quote.Change / quote.PrevClose) * 100
	}
	return quote, nil
}

// GetStockSnapshots 从 TDX 获取股票快照（支持所有数据源类型）
// 实时行情路径会随盘中自动任务/前端实时展示被高频调用，任何一处未捕获 panic
// 都会直接终止整个进程。这里兜底恢复并输出调用栈，避免程序自动退出且便于定位。
func (p *TDXMarketDataProvider) GetStockSnapshots(codes []string) (snapshots []data.StockSnapshot, retErr error) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[TDX Provider] GetStockSnapshots PANIC 已捕获: %v\n%s", r, debug.Stack())
			retErr = fmt.Errorf("GetStockSnapshots 内部异常: %v", r)
		}
	}()

	if p.manager == nil {
		return nil, fmt.Errorf("TDX manager not initialized")
	}

	for _, code := range codes {
		code = strings.TrimSpace(code)
		if code == "" {
			continue
		}

		// 解析代码和市场
		exchange, pureCode := parseCode(code)

		// 使用 TDXServiceManager.GetRealTimeQuote 获取数据（支持所有数据源）
		quote, err := p.manager.GetRealTimeQuote(exchange, pureCode)
		if err != nil {
			log.Printf("[TDX Provider] Failed to get quote for %s: %v", code, err)
			continue
		}

		snapshot := convertQuoteMapToSnapshot(pureCode, exchange, quote)
		snapshots = append(snapshots, snapshot)
	}

	if len(snapshots) == 0 {
		return nil, fmt.Errorf("tdx returned empty data")
	}

	return snapshots, nil
}

// GetIndexSnapshots 从 TDX 获取指数快照（支持所有数据源类型）
func (p *TDXMarketDataProvider) GetIndexSnapshots(codes []string) (snapshots []data.MarketIndexSnapshot, retErr error) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[TDX Provider] GetIndexSnapshots PANIC 已捕获: %v\n%s", r, debug.Stack())
			retErr = fmt.Errorf("GetIndexSnapshots 内部异常: %v", r)
		}
	}()

	if p.manager == nil {
		return nil, fmt.Errorf("TDX manager not initialized")
	}

	for _, code := range codes {
		code = strings.TrimSpace(code)
		if code == "" {
			continue
		}

		// 统一归一化交易所与纯净代码，避免 "SHsh000001"/"szsh000001" 这类双重或大写前缀
		// 传入 gotdx 后被 DetectMarket 解析出非 6 位 symbol 而报 code count error
		exch, pure := parseCode(code)
		if pure == "" {
			continue
		}

		// 先按识别出的市场查询，失败则回退另一市场（指数归属偶有歧义）
		quote, err := p.manager.GetRealTimeQuote(exch, pure)
		if err != nil {
			alt := "sh"
			if exch == "sh" {
				alt = "sz"
			}
			quote, err = p.manager.GetRealTimeQuote(alt, pure)
		}

		current := 0.0
		lastClose := 0.0
		if err == nil {
			current = getFloat(quote, "price")
			lastClose = getFloat(quote, "last_close")
		}

		// 实时报价为空（native gotdx 的报价接口 GetSnapshot 对指数返回 0 条）时，
		// 改从 gotdx 实时K线（GetKLine 支持指数，且取的是当日盘中实时 bar）构建快照，
		// 数据为真实实时行情，非离线库、非模拟/伪造
		if current <= 0 {
			if p.manager.nativeService != nil {
				// gotdx 库解析指数 K 线时存在 slice bounds 越界 panic（internal/time.go GetDatetime），
				// 此处单指数兜底加 recover，panic 时仅记录日志跳过该指数，不拖垮整个批次。
				bars, kerr := safeGetKline(p.manager.nativeService, exch, pure)
				if kerr == nil && len(bars) >= 2 {
					last := bars[len(bars)-1]
					prev := bars[len(bars)-2]
					current = last.Close
					lastClose = prev.Close
				} else {
					log.Printf("[TDX Provider] 指数 %s 实时报价为空且实时K线兜底失败: %v", code, kerr)
				}
			}
		}

		if current <= 0 {
			log.Printf("[TDX Provider] Failed to get index %s: no usable price data", code)
			continue
		}

		change := current - lastClose
		changePercent := 0.0
		if lastClose > 0 {
			changePercent = (change / lastClose) * 100
		}

		snapshots = append(snapshots, data.MarketIndexSnapshot{
			Code:          code,
			Name:          code,
			Current:       current,
			Change:        change,
			ChangePercent: changePercent,
			IsMock:        false,
		})
	}

	if len(snapshots) == 0 {
		return nil, fmt.Errorf("tdx returned empty index data")
	}

	return snapshots, nil
}

// GetStockData 个股日线（转发到 TDXServiceManager，按当前活跃 provider 流转）
func (p *TDXMarketDataProvider) GetStockData(exchange, code string, days int, includeIndicators bool) (map[string]interface{}, error) {
	if p.manager == nil {
		return nil, fmt.Errorf("TDX manager not initialized")
	}
	return p.manager.GetStockData(exchange, code, days, includeIndicators)
}

// GetKline K线数据（转发到 TDXServiceManager，按当前活跃 provider 流转）
func (p *TDXMarketDataProvider) GetKline(exchange, code, period string, count int, adjust string) (map[string]interface{}, error) {
	if p.manager == nil {
		return nil, fmt.Errorf("TDX manager not initialized")
	}
	return p.manager.GetKline(exchange, code, period, count, adjust)
}

// parseCode 解析代码，返回交易所前缀和纯代码
func parseCode(code string) (exchange, pureCode string) {
	// 统一收敛到 data 包的市场推断与数字提取，
	// 支持 sh601700 / SH:601700 / 601700 / 601700.SH，并正确处理北交所 8/4 代码。
	exchange = data.DetectMarketFromCode(code)
	pureCode = data.PureCodeFromCode(code)
	return exchange, pureCode
}

// convertQuoteMapToSnapshot 将 quote map 转换为 StockSnapshot
func convertQuoteMapToSnapshot(code, exchange string, quote map[string]interface{}) data.StockSnapshot {
	price := getFloat(quote, "price")
	lastClose := getFloat(quote, "last_close")
	open := getFloat(quote, "open")
	high := getFloat(quote, "high")
	low := getFloat(quote, "low")
	volume := getFloat(quote, "volume")
	amount := getFloat(quote, "amount")
	changePct := getFloat(quote, "change_pct")
	changeAmt := price - lastClose
	// 优先使用快照自带的股票名；TDX 普通报价不含名称，回退为代码占位（避免在逐只快照热路径里做昂贵的字典回填）。
	name := getString(quote, "name")
	if name == "" {
		name = code
	}

	return data.StockSnapshot{
		Code:          code,
		Name:          name,
		Market:        exchange,
		CurrentPrice:  price,
		PrevClose:     lastClose,
		Open:          open,
		High:          high,
		Low:           low,
		Volume:        volume,
		Turnover:      amount,
		ChangePercent: changePct,
		ChangeAmount:  changeAmt,
		Timestamp:     time.Now().UnixMilli(),
		IsMock:        false,
	}
}

// getFloat 从 map 中提取 float64 值
func getFloat(m map[string]interface{}, key string) float64 {
	if v, ok := m[key]; ok {
		switch val := v.(type) {
		case float64:
			return val
		case int:
			return float64(val)
		case int64:
			return float64(val)
		case string:
			var f float64
			fmt.Sscanf(val, "%f", &f)
			return f
		}
	}
	return 0.0
}

// getString 从 map 读取字符串字段
func getString(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok {
		switch val := v.(type) {
		case string:
			return val
		case float64:
			return fmt.Sprintf("%v", val)
		case int:
			return fmt.Sprintf("%v", val)
		}
	}
	return ""
}

// getFloats 从 map 读取 []float64 字段（盘口价量）
func getFloats(m map[string]interface{}, key string) []float64 {
	v, ok := m[key]
	if !ok {
		return nil
	}
	switch val := v.(type) {
	case []float64:
		return val
	case []interface{}:
		out := make([]float64, 0, len(val))
		for _, item := range val {
			switch it := item.(type) {
			case float64:
				out = append(out, it)
			case int:
				out = append(out, float64(it))
			case string:
				var f float64
				fmt.Sscanf(it, "%f", &f)
				out = append(out, f)
			}
		}
		return out
	}
	return nil
}

// IsNativeAvailable 检查原生 TDX 服务是否可用
func (p *TDXMarketDataProvider) IsNativeAvailable() bool {
	if p.manager == nil || p.manager.nativeService == nil {
		return false
	}
	return p.manager.nativeService.IsConnected()
}

// convertTDXQuoteToSnapshot 将 TDX Quote 转换为 data.StockSnapshot
func convertTDXQuoteToSnapshot(q tdx.Quote) data.StockSnapshot {
	changeAmount := q.Price - q.LastClose
	changePercent := 0.0
	if q.LastClose > 0 {
		changePercent = (changeAmount / q.LastClose) * 100
	}

	market := "sh"
	code := q.Code
	if strings.HasPrefix(q.Code, "sz") {
		market = "sz"
		code = strings.TrimPrefix(q.Code, "sz")
	} else if strings.HasPrefix(q.Code, "sh") {
		market = "sh"
		code = strings.TrimPrefix(q.Code, "sh")
	}

	return data.StockSnapshot{
		Code:          code,
		Name:          code, // TDX Quote 不含名称，使用代码作为占位
		Market:        market,
		CurrentPrice:  q.Price,
		PrevClose:     q.LastClose,
		Open:          q.Open,
		High:          q.High,
		Low:           q.Low,
		Volume:        float64(q.Volume),
		Turnover:      q.Amount,
		ChangePercent: changePercent,
		ChangeAmount:  changeAmount,
		Timestamp:     time.Now().UnixMilli(),
		IsMock:        false,
	}
}

// joinStrings 拼接字符串数组
func joinStrings(parts []string, sep string) string {
	result := ""
	for i, p := range parts {
		if i > 0 {
			result += sep
		}
		result += p
	}
	return result
}

// boolVal 从 interface{} 获取布尔值
func boolVal(v interface{}) bool {
	if v == nil {
		return false
	}
	if b, ok := v.(bool); ok {
		return b
	}
	return false
}
