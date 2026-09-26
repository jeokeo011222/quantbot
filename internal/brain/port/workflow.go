package port

// WorkflowData 定义每日交易周期（DailyCycle/workflow）所需的市场数据视图。
// 这些是纯值类型，宿主（internal/brainhost）在装配点将真实 internal/data 类型转换注入。

// WatchStock 监控股票（对齐 data.WatchStock，决策脑仅需 Code/Name/Market）。
type WatchStock struct {
	Code   string
	Name   string
	Market string
}

// StockSnapshot 股票实时快照（对齐 data.StockSnapshot，决策脑实际使用的字段）。
type StockSnapshot struct {
	Code          string
	Name          string
	Market        string
	CurrentPrice  float64
	PrevClose     float64
	Open          float64
	High          float64
	Low           float64
	Volume        float64
	Turnover      float64
	ChangePercent float64
	ChangeAmount  float64
	Timestamp     int64
	IsMock        bool
}

// MarketIndex 市场指数配置（对齐 data.MarketIndex 中决策脑实际使用的字段）。
type MarketIndex struct {
	Code   string
	Market string
}

// IndexSnapshot 指数实时快照（对齐 data.MarketIndexSnapshot 中决策脑实际使用的字段）。
type IndexSnapshot struct {
	Code    string
	Current float64
	Change  float64
}

// FactorResult 因子计算结果（对齐 data.FactorResult 中决策脑实际使用的字段）。
type FactorResult struct {
	Code       string
	Sector     string
	FactorName string
	Category   string
	Score      float64
}

// Portfolio 组合资金信息（对齐 data.Portfolio 中决策脑实际使用的字段）。
type Portfolio struct {
	InitialCapital float64
	CurrentCapital float64
}

// DictLoader 数据字典加载器抽象：宿主将 internal/data.DictLoader 适配注入。
type DictLoader interface {
	GetIndustryByStock(code string) string
	GetStockName(code string) string
	GetStockCode(name string) string
}

// WorkflowStore 决策脑每日交易周期（DailyCycle）的数据访问抽象。
// 宿主在装配点将 internal/data 的真实实现适配为本接口后注入，使 decision brain 不依赖 data 包。
type WorkflowStore interface {
	// WatchStocks 返回监控股票列表（对应 data.GetDefaultWatchStocks）。
	WatchStocks() []WatchStock
	// FetchStockSnapshots 获取实时股票快照（对应 data.FetchRealStockSnapshots）。
	FetchStockSnapshots(codes []string) ([]StockSnapshot, error)
	// ActiveMarketIndices 返回启用的市场指数（对应 data.GetActiveMarketIndices）。
	ActiveMarketIndices() []MarketIndex
	// FetchIndexSnapshots 获取实时指数快照（对应 data.FetchRealIndexSnapshots）。
	FetchIndexSnapshots(codes []string) ([]IndexSnapshot, error)
	// ActivePortfolio 返回当前启用的组合资金信息（对应 data.Portfolio 查询）。ok=false 表示无有效组合。
	ActivePortfolio() (Portfolio, bool)
	// DictLoader 返回数据字典加载器。
	DictLoader() DictLoader
}
