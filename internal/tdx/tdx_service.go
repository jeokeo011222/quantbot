package tdx

import (
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/quant1x/exchange"
	"github.com/quant1x/gotdx"
	"github.com/quant1x/gotdx/proto"
	"github.com/quant1x/gotdx/quotes"
)

// TDX 连接配置
const (
	defaultConnectTimeout = 10 * time.Second
	defaultReadTimeout    = 5 * time.Second
	defaultMaxRetries     = 3
	defaultRetryInterval  = 1 * time.Second
	pingTimeout           = 3 * time.Second
)

// 备用 TDX 服务器列表（当 gotdx 默认服务器不可用时使用）
var fallbackServers = []struct {
	Host string
	Port int
}{
	{"139.9.50.246", 7709},    // 广州BGP行情十
	{"139.9.90.169", 7709},    // 广州BGP行情十二
	{"139.9.38.206", 7709},    // 广州BGP行情七
	{"139.9.43.104", 7709},    // 广州BGP行情八
	{"139.9.52.158", 7709},    // 广州BGP行情十一
	{"139.159.143.228", 7709}, // 广州BGP行情一
	{"139.159.183.76", 7709},  // 广州BGP行情二
	{"139.159.193.118", 7709}, // 广州BGP行情三
	{"139.159.195.177", 7709}, // 广州BGP行情四
	{"139.159.202.253", 7709}, // 广州BGP行情五
	{"139.159.214.78", 7709},  // 广州BGP行情六
	{"124.70.176.52", 7709},   // 上海双线主站1
	{"47.100.236.28", 7709},   // 上海双线主站2
	{"123.60.186.45", 7709},   // 上海双线主站3
	{"123.60.164.122", 7709},  // 上海双线主站4
	{"47.116.105.28", 7709},   // 上海双线主站5
	{"124.70.199.56", 7709},   // 上海双线主站6
	{"124.70.183.173", 7709},  // 华泰证券(华东华为云一)
	{"124.71.163.106", 7709},  // 华泰证券(华东华为云二)
	{"121.36.54.217", 7709},   // 北京双线主站1
	{"121.36.81.195", 7709},   // 北京双线主站2
	{"123.249.15.60", 7709},   // 北京双线主站3
	{"110.41.147.114", 7709},  // 深圳双线主站1
	{"110.41.2.72", 7709},     // 深圳双线主站2
	{"110.41.4.4", 7709},      // 深圳双线主站3
	{"47.113.94.204", 7709},   // 深圳双线主站4
	{"8.129.174.169", 7709},   // 深圳双线主站5
	{"110.41.154.219", 7709},  // 深圳双线主站6
	{"124.71.85.110", 7709},   // 广州双线主站1
	{"139.9.51.18", 7709},     // 广州双线主站2
	{"139.159.239.163", 7709}, // 广州双线主站3
	{"180.153.18.170", 7709},  // 中信证券上海电信主站Z1
	{"180.153.18.171", 7709},  // 中信证券上海电信主站Z2
	{"202.108.253.130", 7709}, // 中信证券北京联通主站Z1
	{"60.191.117.167", 7709},  // 中信证券杭州电信主站J1
	{"114.80.63.12", 7709},    // 云行情上海电信Z1
	{"123.125.108.23", 7709},  // 云行情北京联通Z1
	{"121.201.83.106", 7709},  // 云行情广州电信Z1
	{"218.6.170.55", 7709},    // 云行情成都电信Z1
}

// initExchangeCalendar 预初始化交易日历缓存
func initExchangeCalendar() {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[TDX] Calendar init recovered: %v", r)
		}
	}()

	homeDir, err := os.UserHomeDir()
	if err != nil {
		log.Printf("[TDX] Cannot get home dir: %v", err)
		return
	}

	metaDir := filepath.Join(homeDir, ".quant1x", "meta")
	calendarFile := filepath.Join(metaDir, "calendar")

	if err := os.MkdirAll(metaDir, 0755); err != nil {
		log.Printf("[TDX] Cannot create meta dir: %v", err)
		return
	}

	var existingDates map[string]bool = make(map[string]bool)
	if data, err := os.ReadFile(calendarFile); err == nil && len(data) > 0 {
		lines := strings.Split(string(data), "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "date,") || strings.HasPrefix(line, "Date,") {
				continue
			}
			parts := strings.SplitN(line, ",", 2)
			if len(parts) >= 1 {
				existingDates[parts[0]] = true
			}
		}
	}

	now := time.Now()
	minYear := 2020
	maxYear := now.Year() + 1

	newDates := make(map[string]bool)
	for year := minYear; year <= maxYear; year++ {
		for month := 1; month <= 12; month++ {
			for day := 1; day <= 31; day++ {
				d := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
				if d.Month() != time.Month(month) {
					continue
				}
				weekday := d.Weekday()
				if weekday == time.Saturday || weekday == time.Sunday {
					continue
				}
				dateStr := fmt.Sprintf("%04d-%02d-%02d", year, month, day)
				newDates[dateStr] = true
			}
		}
	}

	allDates := make([]string, 0)
	for date := range existingDates {
		allDates = append(allDates, date)
	}
	for date := range newDates {
		if !existingDates[date] {
			allDates = append(allDates, date)
		}
	}

	todayStr := now.Format("2006-01-02")
	if existingDates[todayStr] {
		return
	}

	sort.Strings(allDates)

	var content strings.Builder
	content.WriteString("date,source\n")
	for _, d := range allDates {
		content.WriteString(fmt.Sprintf("%s,tdx\n", d))
	}

	if err := os.WriteFile(calendarFile, []byte(content.String()), 0644); err != nil {
		log.Printf("[TDX] Cannot write calendar file: %v", err)
		return
	}

	log.Printf("[TDX] Initialized exchange calendar with %d trading days", len(allDates))
}

// TDXService 统一 TDX 数据服务（基于 gotdx 库实现）
type TDXService struct {
	mu              sync.RWMutex
	connected       bool
	customServers   []TDXServer
	connectionCount int
	lastConnectTime time.Time
	fallbackMode    bool // 是否使用降级模式
}

// NewTDXService 创建 TDX 服务
func NewTDXService() *TDXService {
	return &TDXService{}
}

// TestNetworkConnectivity 测试网络连通性
func (s *TDXService) TestNetworkConnectivity() map[string]interface{} {
	result := map[string]interface{}{
		"reachable_servers": []map[string]interface{}{},
		"total_servers":     len(fallbackServers),
		"reachable_count":   0,
		"timestamp":         time.Now().Format("2006-01-02 15:04:05"),
	}

	var reachable []map[string]interface{}
	sem := make(chan struct{}, 10) // 并发限制
	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, srv := range fallbackServers {
		wg.Add(1)
		sem <- struct{}{}
		go func(host string, port int) {
			defer wg.Done()
			defer func() { <-sem }()

			start := time.Now()
			conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", host, port), 2*time.Second)
			elapsed := time.Since(start)

			mu.Lock()
			defer mu.Unlock()

			if err == nil {
				conn.Close()
				reachable = append(reachable, map[string]interface{}{
					"host":       host,
					"port":       port,
					"latency_ms": elapsed.Milliseconds(),
					"reachable":  true,
				})
			}
		}(srv.Host, srv.Port)
	}

	wg.Wait()

	result["reachable_servers"] = reachable
	result["reachable_count"] = len(reachable)

	log.Printf("[TDX] Network diagnostic: %d/%d servers reachable", len(reachable), len(fallbackServers))
	return result
}

// preConnectCheck 连接前预检查
func (s *TDXService) preConnectCheck() error {
	log.Printf("[TDX] Running pre-connect network check...")

	// 快速测试前几个服务器的连通性
	reachable := 0
	for i, srv := range fallbackServers {
		if i >= 10 { // 只测试前10个
			break
		}
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", srv.Host, srv.Port), 2*time.Second)
		if err == nil {
			conn.Close()
			reachable++
		}
	}

	if reachable == 0 {
		log.Printf("[TDX] WARNING: No TDX servers reachable via network check")
		return fmt.Errorf("no TDX servers reachable (checked %d servers)", 10)
	}

	log.Printf("[TDX] Network check passed: %d/10 servers reachable", reachable)
	return nil
}

// Connect 连接 TDX 服务器
func (s *TDXService) Connect() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.connected {
		return nil
	}

	// 预初始化交易日历
	initExchangeCalendar()

	// 预检查网络连通性
	if err := s.preConnectCheck(); err != nil {
		log.Printf("[TDX] Network pre-check failed, will try anyway: %v", err)
		s.fallbackMode = true
	}

	// 尝试连接（带重试）
	var lastErr error
	for attempt := 1; attempt <= defaultMaxRetries; attempt++ {
		log.Printf("[TDX] Connection attempt %d/%d", attempt, defaultMaxRetries)

		// 关闭现有连接并重新初始化
		gotdx.ReOpen()
		time.Sleep(time.Duration(attempt) * 300 * time.Millisecond)

		api := gotdx.GetTdxApi()
		if api == nil {
			lastErr = fmt.Errorf("failed to get TDX API (attempt %d)", attempt)
			log.Printf("[TDX] %v", lastErr)
			continue
		}

		// 验证连接
		_, err := api.GetSecurityCount(exchange.MarketIdShangHai)
		if err != nil {
			lastErr = fmt.Errorf("connection verification failed (attempt %d): %w", attempt, err)
			log.Printf("[TDX] %v", lastErr)
			gotdx.ReOpen()
			continue
		}

		s.connected = true
		s.connectionCount++
		s.lastConnectTime = time.Now()
		s.fallbackMode = false

		log.Printf("[TDX] Connected successfully (attempt %d)", attempt)
		return nil
	}

	// 所有重试都失败
	s.fallbackMode = true
	log.Printf("[TDX] All connection attempts failed: %v", lastErr)
	return fmt.Errorf("TDX connection failed after %d attempts: %w", defaultMaxRetries, lastErr)
}

// Close 关闭连接
func (s *TDXService) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.connected = false
	gotdx.ReOpen()
	log.Printf("[TDX] Connection closed")
}

// IsConnected 检查连接状态
func (s *TDXService) IsConnected() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if !s.connected {
		return false
	}

	api := gotdx.GetTdxApi()
	if api == nil {
		s.connected = false
		return false
	}

	_, err := api.GetSecurityCount(exchange.MarketIdShangHai)
	if err != nil {
		s.connected = false
		log.Printf("[TDX] Connection check failed: %v", err)
		return false
	}
	return true
}

// GetConnectionInfo 获取连接信息
func (s *TDXService) GetConnectionInfo() map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return map[string]interface{}{
		"connected":        s.connected,
		"connection_count": s.connectionCount,
		"last_connect":     s.lastConnectTime.Format("2006-01-02 15:04:05"),
		"fallback_mode":    s.fallbackMode,
		"custom_servers":   len(s.customServers),
	}
}

// getAPI 获取 TDX API 实例（带重连逻辑）
func (s *TDXService) getAPI() (*quotes.StdApi, error) {
	s.mu.RLock()
	if s.connected {
		s.mu.RUnlock()
		api := gotdx.GetTdxApi()
		if api == nil {
			return nil, fmt.Errorf("TDX API not available")
		}
		return api, nil
	}
	s.mu.RUnlock()

	// 未连接，尝试连接
	if err := s.Connect(); err != nil {
		return nil, err
	}

	api := gotdx.GetTdxApi()
	if api == nil {
		return nil, fmt.Errorf("TDX API not available after connection")
	}
	return api, nil
}

// executeWithRetry 执行带重试的操作
func (s *TDXService) executeWithRetry(operation string, fn func() error) error {
	var lastErr error

	for attempt := 1; attempt <= 2; attempt++ {
		if attempt > 1 {
			log.Printf("[TDX] Retrying %s (attempt %d/2)", operation, attempt)
			gotdx.ReOpen()
			time.Sleep(500 * time.Millisecond)
		}

		if err := fn(); err != nil {
			lastErr = err
			if attempt == 1 {
				continue // 第一次失败后重试
			}
			return fmt.Errorf("%s failed after retry: %w", operation, err)
		}
		return nil
	}

	return fmt.Errorf("%s failed: %w", operation, lastErr)
}

// ==================== 行情数据接口 ====================

// GetKline 获取K线数据
func (s *TDXService) GetKline(market, code, period string, count uint16) ([]KlineBar, error) {
	api, err := s.getAPI()
	if err != nil {
		return nil, err
	}

	klineType, err := parseKlineType(period)
	if err != nil {
		return nil, err
	}

	fullCode := s.buildCode(market, code)
	var reply *quotes.SecurityBarsReply

	err = s.executeWithRetry("GetKLine", func() error {
		var e error
		reply, e = api.GetKLine(fullCode, klineType, 0, count)
		return e
	})

	if err != nil {
		return nil, fmt.Errorf("GetKLine failed: %w", err)
	}

	result := make([]KlineBar, len(reply.List))
	for i, b := range reply.List {
		result[i] = KlineBar{
			Date:   b.DateTime,
			Open:   b.Open,
			High:   b.High,
			Low:    b.Low,
			Close:  b.Close,
			Volume: int64(b.Vol),
			Amount: b.Amount,
		}
	}

	return result, nil
}

// GetMinuteBars 获取分时数据
func (s *TDXService) GetMinuteBars(market, code string, count uint16) ([]MinuteBar, error) {
	api, err := s.getAPI()
	if err != nil {
		return nil, err
	}

	fullCode := s.buildCode(market, code)
	today := uint32(time.Now().Year()*10000 + int(time.Now().Month())*100 + time.Now().Day())
	var reply *quotes.MinuteTimeReply

	err = s.executeWithRetry("GetHistoryMinuteTimeData", func() error {
		var e error
		reply, e = api.GetHistoryMinuteTimeData(fullCode, today)
		return e
	})

	if err != nil {
		return nil, fmt.Errorf("GetHistoryMinuteTimeData failed: %w", err)
	}

	result := make([]MinuteBar, len(reply.List))
	for i, b := range reply.List {
		result[i] = MinuteBar{
			Time:     fmt.Sprintf("%02d:%02d", 9+i/60, i%60),
			Price:    float64(b.Price),
			Volume:   int64(b.Vol),
			AvgPrice: float64(b.Price),
		}
	}

	return result, nil
}

// GetQuotes 获取实时行情
func (s *TDXService) GetQuotes(market string, codes []string) ([]Quote, error) {
	api, err := s.getAPI()
	if err != nil {
		return nil, err
	}

	fullCodes := make([]string, len(codes))
	for i, code := range codes {
		fullCodes[i] = s.buildCode(market, code)
	}

	var snapshots []quotes.Snapshot
	err = s.executeWithRetry("GetSnapshot", func() error {
		var e error
		snapshots, e = api.GetSnapshot(fullCodes)
		return e
	})

	if err != nil {
		return nil, fmt.Errorf("GetSnapshot failed: %w", err)
	}

	result := make([]Quote, len(snapshots))
	for i, snap := range snapshots {
		bidPrices := []float64{snap.Bid1, snap.Bid2, snap.Bid3, snap.Bid4, snap.Bid5}
		askPrices := []float64{snap.Ask1, snap.Ask2, snap.Ask3, snap.Ask4, snap.Ask5}
		bidVolumes := []int64{int64(snap.BidVol1), int64(snap.BidVol2), int64(snap.BidVol3), int64(snap.BidVol4), int64(snap.BidVol5)}
		askVolumes := []int64{int64(snap.AskVol1), int64(snap.AskVol2), int64(snap.AskVol3), int64(snap.AskVol4), int64(snap.AskVol5)}

		result[i] = Quote{
			Code:       snap.SecurityCode,
			Price:      snap.Price,
			LastClose:  snap.LastClose,
			Open:       snap.Open,
			High:       snap.High,
			Low:        snap.Low,
			Volume:     int64(snap.Vol),
			Amount:     snap.Amount,
			BidPrices:  bidPrices,
			AskPrices:  askPrices,
			BidVolumes: bidVolumes,
			AskVolumes: askVolumes,
		}
	}

	return result, nil
}

// GetTransactions 获取逐笔成交
func (s *TDXService) GetTransactions(market, code string, count uint16) ([]Transaction, error) {
	api, err := s.getAPI()
	if err != nil {
		return nil, err
	}

	fullCode := s.buildCode(market, code)
	var reply *quotes.TransactionReply

	err = s.executeWithRetry("GetTransactionData", func() error {
		var e error
		reply, e = api.GetTransactionData(fullCode, 0, count)
		return e
	})

	if err != nil {
		return nil, fmt.Errorf("GetTransactionData failed: %w", err)
	}

	result := make([]Transaction, len(reply.List))
	for i, t := range reply.List {
		bs := "B"
		if t.BuyOrSell == 1 {
			bs = "S"
		}
		result[i] = Transaction{
			Price:  t.Price,
			Volume: int64(t.Vol),
			Time:   t.Time,
			BS:     bs,
		}
	}

	return result, nil
}

// GetSecurityInfo 获取证券信息
func (s *TDXService) GetSecurityInfo(market, code string) (*SecurityInfo, error) {
	api, err := s.getAPI()
	if err != nil {
		return nil, err
	}

	mkt := s.marketToExchange(market)
	var reply *quotes.SecurityListReply

	err = s.executeWithRetry("GetSecurityList", func() error {
		var e error
		reply, e = api.GetSecurityList(mkt, 0)
		return e
	})

	if err != nil {
		return nil, fmt.Errorf("GetSecurityList failed: %w", err)
	}

	fullCode := s.buildCode(market, code)
	for _, item := range reply.List {
		if fullCode[2:] == item.Code || code == item.Code {
			return &SecurityInfo{
				Name: item.Name,
				Code: item.Code,
			}, nil
		}
	}

	return &SecurityInfo{Code: code}, nil
}

// GetSecurityList 获取证券列表
func (s *TDXService) GetSecurityList(market uint8, start, count uint16) ([]SecurityListItem, error) {
	api, err := s.getAPI()
	if err != nil {
		return nil, err
	}

	mkt := exchange.MarketType(market)
	var reply *quotes.SecurityListReply

	err = s.executeWithRetry("GetSecurityList", func() error {
		var e error
		reply, e = api.GetSecurityList(mkt, start)
		return e
	})

	if err != nil {
		return nil, fmt.Errorf("GetSecurityList failed: %w", err)
	}

	result := make([]SecurityListItem, len(reply.List))
	for i, item := range reply.List {
		result[i] = SecurityListItem{
			Code: item.Code,
			Name: item.Name,
		}
	}

	return result, nil
}

// GetMarketStat 获取市场统计
func (s *TDXService) GetMarketStat(market uint8) (*MarketStat, error) {
	api, err := s.getAPI()
	if err != nil {
		return nil, err
	}

	mkt := exchange.MarketType(market)
	reply, err := api.GetSecurityCount(mkt)
	if err != nil {
		return nil, fmt.Errorf("GetSecurityCount failed: %w", err)
	}

	return &MarketStat{
		UpCount:   int(reply.Count),
		DownCount: 0,
		FlatCount: 0,
		LimitUp:   0,
		LimitDown: 0,
	}, nil
}

// ==================== 辅助方法 ====================

// buildCode 构建带市场前缀的代码
func (s *TDXService) buildCode(market, code string) string {
	code = strings.TrimPrefix(code, "sh")
	code = strings.TrimPrefix(code, "sz")
	code = strings.TrimPrefix(code, "SH")
	code = strings.TrimPrefix(code, "SZ")

	switch strings.ToLower(market) {
	case "sh", "sse", "1":
		return "sh" + code
	case "sz", "szse", "0":
		return "sz" + code
	case "bj", "bse", "2":
		return "bj" + code
	default:
		return "sh" + code
	}
}

// marketToExchange 转换市场标识为 exchange.MarketType
func (s *TDXService) marketToExchange(market string) exchange.MarketType {
	switch strings.ToLower(market) {
	case "sh", "sse", "1":
		return exchange.MarketIdShangHai
	case "sz", "szse", "0":
		return exchange.MarketIdShenZhen
	case "bj", "bse", "2":
		return exchange.MarketIdBeiJing
	default:
		return exchange.MarketIdShangHai
	}
}

// parseKlineType 解析K线周期类型
func parseKlineType(period string) (uint16, error) {
	switch strings.ToLower(period) {
	case "1min":
		return proto.KLINE_TYPE_1MIN, nil
	case "5min":
		return proto.KLINE_TYPE_5MIN, nil
	case "15min":
		return proto.KLINE_TYPE_15MIN, nil
	case "30min":
		return proto.KLINE_TYPE_30MIN, nil
	case "60min", "1hour":
		return proto.KLINE_TYPE_1HOUR, nil
	case "day":
		return proto.KLINE_TYPE_DAILY, nil
	case "week":
		return proto.KLINE_TYPE_WEEKLY, nil
	case "month":
		return proto.KLINE_TYPE_MONTHLY, nil
	default:
		return 0, fmt.Errorf("unknown period: %s", period)
	}
}

// ==================== 服务器管理接口 ====================

// TDXServer TDX 服务器节点
type TDXServer struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

// GetServers 获取服务器列表
func (s *TDXService) GetServers() []TDXServer {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]TDXServer, len(s.customServers))
	copy(result, s.customServers)
	return result
}

// SetServers 自定义服务器列表
func (s *TDXService) SetServers(servers []TDXServer) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.customServers = servers
	log.Printf("[TDX] Custom servers set: %d servers", len(servers))
}

// TestConnection 测试连接
func (s *TDXService) TestConnection() (bool, string) {
	// 先测试网络连通性
	networkResult := s.TestNetworkConnectivity()
	reachableCount := networkResult["reachable_count"].(int)
	totalCount := networkResult["total_servers"].(int)

	if reachableCount == 0 {
		return false, fmt.Sprintf("网络诊断失败: %d/%d 服务器可达，请检查网络连接或防火墙设置", reachableCount, totalCount)
	}

	log.Printf("[TDX] Network check: %d/%d servers reachable", reachableCount, totalCount)

	// 尝试连接
	s.mu.Lock()
	if s.connected {
		s.mu.Unlock()
		return true, "OK - 已连接"
	}
	s.mu.Unlock()

	if err := s.Connect(); err != nil {
		return false, fmt.Sprintf("连接失败: %v (网络: %d/%d 服务器可达)", err, reachableCount, totalCount)
	}

	api := gotdx.GetTdxApi()
	if api == nil {
		return false, "TDX API not available after connection"
	}

	reply, err := api.GetSecurityCount(exchange.MarketIdShangHai)
	if err != nil {
		return false, fmt.Sprintf("连接验证失败: %v", err)
	}

	return true, fmt.Sprintf("OK - 连接成功，上海市场共 %d 只股票 (网络: %d/%d 服务器可达)", reply.Count, reachableCount, totalCount)
}

// WaitReady 等待服务就绪
func (s *TDXService) WaitReady(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		s.mu.RLock()
		if s.connected {
			s.mu.RUnlock()
			return nil
		}
		s.mu.RUnlock()

		api := gotdx.GetTdxApi()
		if api != nil {
			_, err := api.GetSecurityCount(exchange.MarketIdShangHai)
			if err == nil {
				s.mu.Lock()
				s.connected = true
				s.mu.Unlock()
				return nil
			}
		}
		time.Sleep(500 * time.Millisecond)
		gotdx.ReOpen()
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for TDX connection")
}

// ensureConnected 确保已连接
func (s *TDXService) ensureConnected() bool {
	if s.IsConnected() {
		return true
	}

	log.Printf("[TDX] Not connected, attempting to reconnect...")
	s.mu.Lock()
	defer s.mu.Unlock()

	gotdx.ReOpen()
	time.Sleep(500 * time.Millisecond)

	api := gotdx.GetTdxApi()
	if api == nil {
		log.Printf("[TDX] Reconnection failed: API not available")
		return false
	}

	_, err := api.GetSecurityCount(exchange.MarketIdShangHai)
	if err != nil {
		log.Printf("[TDX] Reconnection failed: %v", err)
		return false
	}

	s.connected = true
	s.connectionCount++
	s.lastConnectTime = time.Now()
	log.Printf("[TDX] Reconnected successfully")
	return true
}
