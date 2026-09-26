// 六维判势数据访问抽象：决策脑（sixdim）只依赖本包定义的窄接口与 DTO，
// 宿主在装配点将 internal/data、internal/marketinfo、internal/thssdk 适配为这些
// 接口后注入，从而让决策脑彻底不依赖任何宿主包（可编译为独立 DLL）。
package port

import (
	"context"
	"time"
)

// ==================== 六维判势领域 DTO（对齐宿主 data/marketinfo/thssdk 的六维用字段） ====================

// KlineBar 指数/个股K线（对齐 data.KlineBarFromDuckDB）。
type KlineBar struct {
	Symbol string
	Date   time.Time
	Open   float64
	High   float64
	Low    float64
	Close  float64
	Volume float64
	Amount float64
}

// FactorBar 因子Bar（对齐 data.FactorBar）。
type FactorBar struct {
	Symbol    string
	Date      time.Time
	Open      float64
	High      float64
	Low       float64
	Close     float64
	PreClose  float64
	Volume    float64
	Amount    float64
	Turnover  float64
	PctChg    float64
	Amplitude float64
}

// SymbolInfo 股票代码信息（对齐 data.StockSymbolInfo）。
type SymbolInfo struct {
	Symbol string
	Name   string
	Market string
}

// MarketAmountDay 全市场每日成交额（对齐 data.MarketAmountDay）。
type MarketAmountDay struct {
	Date   time.Time
	Amount float64
}

// MarketLimitStats 全市场涨跌停统计（对齐 data.MarketLimitStats）。
type MarketLimitStats struct {
	LimitUp        int
	LimitDown      int
	SealedLimitUp  int
	BlownUp        int
	TouchedLimitUp int
}

// SectorChangeStat 行业平均涨跌幅统计（对齐 data.SectorChangeStat）。
type SectorChangeStat struct {
	Sector     string
	StockCount int
	AvgChgPct  float64
}

// SectorPerformanceRow 行业成交额单行（对齐 data.GetSectorPerformance 返回的 map 行）。
type SectorPerformanceRow map[string]interface{}

// MarginPoint 融资余额数据点（对齐 marketinfo.MarginPoint）。
type MarginPoint struct {
	Date    string
	Balance float64
}

// LimitUpBoard 涨停/跌停/炸板池（对齐 marketinfo.LimitUpBoard 的六维用字段）。
type LimitUpBoard struct {
	LimitUpCnt   int
	LimitDownCnt int
	ZhabanCnt    int
}

// GlobalMarketPoint 隔夜外围市场点（对齐 marketinfo.GlobalMarketPoint 的六维用字段）。
type GlobalMarketPoint struct {
	Code      string
	Name      string
	Current   float64
	ChangePct float64
	Change    float64
}

// SentimentSnapshot 同花顺官方情绪面快照（对齐 thssdk.SentimentSnapshot）。
type SentimentSnapshot struct {
	LimitUpCnt     int
	LimitDownCnt   int
	BlowUpCnt      int
	BlowUpRate     float64
	MaxBoardHeight int
}

// MarketSixDimRow 六维判势历史记录（对齐 data.MarketSixDimRow）。
type MarketSixDimRow struct {
	TradeDate          string
	DimScoresJSON      string
	RawTotalScore      float64
	ConflictCount      int
	AdjustedTotalScore float64
	PositionRate       float64
	MarketTag          string
	SourcesJSON        string
	CreatedAt          time.Time
}

// ==================== 六维判势依赖接口 ====================

// MarketDataStore 六维判势读取本地行情库（DuckDB）数据的抽象（对齐 data.DuckDBManager）。
type MarketDataStore interface {
	HasStockDB() bool
	GetKlineFromStock(ctx context.Context, symbol string, days int) ([]KlineBar, error)
	ListRepresentativeSymbols(ctx context.Context, n int) ([]SymbolInfo, error)
	ListAllSymbolsFromStock(ctx context.Context, market string, limit int) ([]SymbolInfo, error)
	BatchGetFactorBars(ctx context.Context, symbols []string, days int) (map[string][]FactorBar, error)
	GetSectorChangeStats(ctx context.Context) ([]SectorChangeStat, error)
	GetSectorPerformance(ctx context.Context) ([]SectorPerformanceRow, error)
	GetMarketAmountHistory(ctx context.Context, days int) ([]MarketAmountDay, error)
	GetMarketLimitStats(ctx context.Context) (*MarketLimitStats, error)
	SaveMarketSixDimRow(ctx context.Context, tradeDate, dimsJSON, sourcesJSON string, rawTotal, adjusted float64, conflict int, positionRate float64, tag string) error
	GetMarketSixDimRows(ctx context.Context, limit int) ([]MarketSixDimRow, error)
}

// SnapSource 活跃实时行情源（对齐 data.UnifiedDataSource，调用时动态取当前活跃 provider）。
type SnapSource interface {
	Source() string
	GetStockSnapshots(codes []string) ([]StockSnapshot, error)
}

// MarketExternalSources 外部实时源（对齐 internal/marketinfo 的六维用函数集合）。
type MarketExternalSources interface {
	FetchNorthboundRealtime(ctx context.Context) (float64, error)
	FetchMarginHistory(ctx context.Context, days int) ([]MarginPoint, error)
	FetchLimitUpBoard(ctx context.Context) (*LimitUpBoard, error)
	FetchExternalMarkets(ctx context.Context) ([]GlobalMarketPoint, string, error)
}

// THSSentimentSource 同花顺官方情绪面源（对齐 internal/thssdk）。
type THSSentimentSource interface {
	// Enabled 该官方源是否已配置（未配置则调用方直接回退东财/代理源）。
	Enabled() bool
	// FetchSentimentSnapshot 拉取同花顺官方情绪面快照。
	FetchSentimentSnapshot(ctx context.Context) (*SentimentSnapshot, error)
}
