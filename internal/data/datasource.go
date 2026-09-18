package data

import "sync"

// 数据源名称常量（与设置-数据源选项一一对应）
const (
	DataProviderNativeName   = "native_tdx"
	DataProviderMCPName      = "tdx_mcp"
	DataProviderTerminalName = "tdx_terminal"
	DataProviderTencentName  = "tencent"
)

// RealTimeQuote 统一的单只实时报价（前端/交易/盘口共用，与具体数据源解耦）
type RealTimeQuote struct {
	Code       string    `json:"code"`
	Name       string    `json:"name"`
	Price      float64   `json:"price"`
	Open       float64   `json:"open"`
	High       float64   `json:"high"`
	Low        float64   `json:"low"`
	PrevClose  float64   `json:"prevClose"`
	Volume     float64   `json:"volume"`
	Amount     float64   `json:"amount"`
	Change     float64   `json:"change"`
	ChangePct  float64   `json:"changePercent"`
	BidPrices  []float64 `json:"bidPrices"`
	AskPrices  []float64 `json:"askPrices"`
	BidVolumes []float64 `json:"bidVolumes"`
	AskVolumes []float64 `json:"askVolumes"`
	Source     string    `json:"source"`
	IsRealtime bool      `json:"isRealtime"`
}

// UnifiedDataSource 统一实时行情数据源接口。
//
// 数据源调用规范：程序任何需要"读实时数据"的地方，一律通过本接口获取，
// 由全局注册表 GetDataSource() 返回当前用户在设置-数据源中选择的 provider
// （native_tdx / tdx_mcp / tdx_terminal / tencent），调用方不得自行按 provider switch。
// codes 统一使用"带交易所小写前缀"格式（sh600519 / sz399001），由实现内部归一化。
//
// 实时数据能力全集（快照 / 报价 / 日线 / K线）统一收敛在本接口，由各 provider
// 实现；调用方一律通过 GetDataSource() 取得当前活跃 provider 后调用，禁止直接耦合具体实现。
type UnifiedDataSource interface {
	// Source 数据源名称（native_tdx / tdx_mcp / tdx_terminal / tencent）
	Source() string
	// GetStockSnapshots 批量股票/ETF 实时快照
	GetStockSnapshots(codes []string) ([]StockSnapshot, error)
	// GetIndexSnapshots 批量指数实时快照
	GetIndexSnapshots(codes []string) ([]MarketIndexSnapshot, error)
	// GetQuote 单只实时报价（含盘口）
	GetQuote(code string) (RealTimeQuote, error)
	// GetStockData 个股日线/历史数据（含技术指标；days<=0 默认 120）
	GetStockData(exchange, code string, days int, includeIndicators bool) (map[string]interface{}, error)
	// GetKline K线数据（period: day/week/month/m5/m15/m30/m60；count<=0 默认 300）
	GetKline(exchange, code, period string, count int, adjust string) (map[string]interface{}, error)
}

var (
	srcMu   sync.RWMutex
	active  UnifiedDataSource
	srcInit bool
)

// SetDataSource 注册/切换当前活跃的实时行情数据源
func SetDataSource(d UnifiedDataSource) {
	srcMu.Lock()
	active = d
	srcInit = true
	srcMu.Unlock()
}

// GetDataSource 获取当前活跃的实时行情数据源
func GetDataSource() UnifiedDataSource {
	srcMu.RLock()
	defer srcMu.RUnlock()
	return active
}

// GotDataSource 是否存在已注册的实时行情数据源
func GotDataSource() bool {
	srcMu.RLock()
	defer srcMu.RUnlock()
	return srcInit && active != nil
}

// DataSourceName 获取当前活跃数据源名称（无则返回 "none"）
func DataSourceName() string {
	srcMu.RLock()
	defer srcMu.RUnlock()
	if active == nil {
		return "none"
	}
	return active.Source()
}
