package data

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/quantpilot/quantpilot/internal/util"
)

// DataIntegrityStatus 数据完整性检查结果
// Data Integrity Guard：任何需要数据的任务都必须先通过此检查
// 失败时返回 TASK_BLOCKED，严禁LLM在数据缺失时自行推断
type DataIntegrityStatus struct {
	OK           bool      `json:"ok"`            // 是否通过全部检查
	Available    bool      `json:"available"`     // 数据源可用
	Fresh        bool      `json:"fresh"`         // 数据新鲜
	Complete     bool      `json:"complete"`      // 数据完整
	Consistent   bool      `json:"consistent"`    // 数据一致
	LatestDate   string    `json:"latest_date"`   // 最新数据日期
	DataPoints   int       `json:"data_points"`   // 数据点数量
	RequiredDays int       `json:"required_days"` // 要求的数据天数
	MissingRate  float64   `json:"missing_rate"`  // 缺失率
	Reason       string    `json:"reason"`        // 失败原因
	CheckedAt    time.Time `json:"checked_at"`    // 检查时间
}

// BlockedError 数据完整性阻断错误
// 当数据缺失/过期/不完整时返回，禁止AI继续推断
type BlockedError struct {
	Reason string
	Status *DataIntegrityStatus
}

func (e *BlockedError) Error() string {
	return fmt.Sprintf("TASK_BLOCKED: %s", e.Reason)
}

// NewBlockedError 创建阻断错误
func NewBlockedError(reason string, status *DataIntegrityStatus) *BlockedError {
	return &BlockedError{Reason: reason, Status: status}
}

// DataIntegrityGuard 数据完整性守卫
type DataIntegrityGuard struct {
	dm *DuckDBManager
}

// NewDataIntegrityGuard 创建数据完整性守卫
func NewDataIntegrityGuard(dm *DuckDBManager) *DataIntegrityGuard {
	return &DataIntegrityGuard{dm: dm}
}

// CheckMarketDataIntegrity 检查市场数据完整性
// 检查链路：数据源可用 → 数据新鲜 → 数据完整 → 数据一致
func (g *DataIntegrityGuard) CheckMarketDataIntegrity(ctx context.Context, market, instrumentID string, requiredDays int) *DataIntegrityStatus {
	status := &DataIntegrityStatus{
		RequiredDays: requiredDays,
		CheckedAt:    time.Now(),
	}
	if requiredDays <= 0 {
		requiredDays = 30
		status.RequiredDays = requiredDays
	}

	// 1. 数据源可用
	if g.dm == nil || !g.dm.HasStockDB() {
		status.Reason = "行情数据库(stock.duckdb)未挂载，数据源不可用"
		return status
	}
	status.Available = true

	// 2. 数据完整 + 新鲜
	prices, err := g.dm.GetRecentPrices(ctx, market, instrumentID, requiredDays)
	if err != nil {
		status.Reason = fmt.Sprintf("无法获取 %s/%s 行情数据: %v", market, instrumentID, err)
		return status
	}
	if len(prices) == 0 {
		status.Reason = fmt.Sprintf("%s/%s 无任何行情数据，禁止AI推断", market, instrumentID)
		return status
	}

	status.DataPoints = len(prices)
	status.Complete = len(prices) >= requiredDays/2

	// 3. 数据新鲜（最新交易日距今不超过10个自然日）
	latest := prices[0].TradeDate
	status.LatestDate = latest.Format("2006-01-02")
	daysSince := int(time.Since(latest).Hours() / 24)
	status.Fresh = daysSince <= 10

	// 4. 数据一致（价格有效性检查：无零值/负值收盘价）
	validCount := 0
	for _, p := range prices {
		if p.Close > 0 && p.Open > 0 && p.High >= p.Low {
			validCount++
		}
	}
	status.MissingRate = 1.0 - float64(validCount)/float64(len(prices))
	status.Consistent = status.MissingRate < 0.2

	if !status.Fresh {
		status.Reason = fmt.Sprintf("行情数据已过期：最新交易日 %s，距今 %d 天", status.LatestDate, daysSince)
		return status
	}
	if !status.Complete {
		status.Reason = fmt.Sprintf("行情数据不完整：仅 %d 条，要求 %d 条", status.DataPoints, requiredDays)
		return status
	}
	if !status.Consistent {
		status.Reason = fmt.Sprintf("行情数据不一致：缺失率 %.1f%%", status.MissingRate*100)
		return status
	}

	status.OK = true
	return status
}

// CheckStockDataIntegrity 检查单只股票数据完整性
func (g *DataIntegrityGuard) CheckStockDataIntegrity(ctx context.Context, symbol string, requiredDays int) *DataIntegrityStatus {
	status := &DataIntegrityStatus{
		RequiredDays: requiredDays,
		CheckedAt:    time.Now(),
	}
	if requiredDays <= 0 {
		requiredDays = 30
		status.RequiredDays = requiredDays
	}

	if g.dm == nil || !g.dm.HasStockDB() {
		status.Reason = "行情数据库(stock.duckdb)未挂载，数据源不可用"
		return status
	}
	status.Available = true

	bars, err := g.dm.GetKlineFromStock(ctx, symbol, requiredDays)
	if err != nil {
		status.Reason = fmt.Sprintf("无法获取 %s 行情数据: %v", symbol, err)
		return status
	}
	if len(bars) == 0 {
		status.Reason = fmt.Sprintf("%s 无任何行情数据，禁止AI推断", symbol)
		return status
	}

	status.DataPoints = len(bars)
	status.Complete = len(bars) >= requiredDays/2

	latest := bars[0].Date
	status.LatestDate = latest.Format("2006-01-02")
	daysSince := int(time.Since(latest).Hours() / 24)
	status.Fresh = daysSince <= 10

	validCount := 0
	for _, b := range bars {
		if b.Close > 0 && b.Open > 0 && b.High >= b.Low {
			validCount++
		}
	}
	status.MissingRate = 1.0 - float64(validCount)/float64(len(bars))
	status.Consistent = status.MissingRate < 0.2

	if !status.Fresh {
		status.Reason = fmt.Sprintf("%s 行情数据已过期：最新交易日 %s，距今 %d 天", symbol, status.LatestDate, daysSince)
		return status
	}
	if !status.Complete {
		status.Reason = fmt.Sprintf("%s 行情数据不完整：仅 %d 条，要求 %d 条", symbol, status.DataPoints, requiredDays)
		return status
	}
	if !status.Consistent {
		status.Reason = fmt.Sprintf("%s 行情数据不一致：缺失率 %.1f%%", symbol, status.MissingRate*100)
		return status
	}

	status.OK = true
	return status
}

// GetLatestTradeDate 获取市场最新交易日
func (g *DataIntegrityGuard) GetLatestTradeDate(ctx context.Context, market string) (string, error) {
	if g.dm == nil || !g.dm.HasStockDB() {
		return "", fmt.Errorf("行情数据库(stock.duckdb)未挂载")
	}
	query := `SELECT MAX(date) FROM stock.ohlc`
	var latest time.Time
	if err := g.dm.db.QueryRowContext(ctx, query).Scan(&latest); err != nil {
		log.Printf("[DataIntegrity] 查询最新交易日失败: %v", err)
		return "", fmt.Errorf("查询最新交易日失败: %w", err)
	}
	return latest.Format("2006-01-02"), nil
}

// MarketDataStatus 市场数据质量状态（选股引擎数据底座健康度）
type MarketDataStatus struct {
	OK            bool    `json:"ok"`              // 数据是否可用且新鲜
	Available     bool    `json:"available"`       // 数据源可用
	Fresh         bool    `json:"fresh"`           // 数据新鲜（最新交易日距今<=10天）
	LatestDate    string  `json:"latest_date"`     // 最新数据日期
	CurrentDate   string  `json:"current_date"`    // 当前日期
	ExpectedDate  string  `json:"expected_date"`   // 期望更新到的最新日期（今天的前一个交易日）
	NeedsUpdate   bool    `json:"needs_update"`    // 数据是否需更新（最新日期未达到前一个交易日）
	StockCount    int     `json:"stock_count"`     // 股票数量
	MissingRate   float64 `json:"missing_rate"`    // 缺失率
	DataSource    string  `json:"data_source"`     // 数据源
	DaysSinceLast int     `json:"days_since_last"` // 距最新交易日天数
	Reason        string  `json:"reason"`          // 状态说明
	CheckedAt     string  `json:"checked_at"`      // 检查时间
}

// GetMarketDataStatus 获取市场数据质量状态
// 用于选股引擎/投资规划生成股票池前的数据新鲜度检查，数据过期时禁止生成股票池
func (g *DataIntegrityGuard) GetMarketDataStatus(ctx context.Context) *MarketDataStatus {
	now := time.Now()
	status := &MarketDataStatus{
		CurrentDate: now.Format("2006-01-02"),
		CheckedAt:   now.Format("2006-01-02 15:04:05"),
		DataSource:  "TDX",
	}

	if g.dm == nil || !g.dm.HasStockDB() {
		status.Reason = "行情数据库(stock.duckdb)未挂载，数据源不可用"
		return status
	}
	status.Available = true

	// 最新交易日
	var latest time.Time
	if err := g.dm.db.QueryRowContext(ctx, "SELECT MAX(date) FROM stock.ohlc").Scan(&latest); err != nil || latest.IsZero() {
		status.Reason = "无法获取最新交易日，行情数据不可用"
		return status
	}
	status.LatestDate = latest.Format("2006-01-02")
	status.DaysSinceLast = int(now.Sub(latest).Hours() / 24)

	// 数据新旧度：期望更新到「今天的前一个交易日」；未达到则标记需更新
	expected, _ := util.ExpectedLatestTradeDate(now)
	status.ExpectedDate = expected.Format("2006-01-02")
	status.NeedsUpdate = status.LatestDate < status.ExpectedDate

	// 放宽到20天，覆盖长假/周末导致的自然数据延迟
	// 正常工作日距上一交易日最多2天（周五到周一），20天覆盖最长的春节/国庆假期
	status.Fresh = status.DaysSinceLast <= 20

	// 股票数量（从 stock_basic 视图查询，该视图包含全部 A 股基础信息）
	if err := g.dm.db.QueryRowContext(ctx,
		"SELECT COUNT(DISTINCT symbol) FROM stock.stock_basic").
		Scan(&status.StockCount); err != nil {
		log.Printf("[DataIntegrity] 查询股票数量失败: %v", err)
	}

	// 缺失率：以最新交易日的有效记录占比估算
	var totalRows, validRows int
	if err := g.dm.db.QueryRowContext(ctx,
		"SELECT COUNT(*), COUNT(*) FILTER (WHERE close > 0 AND open > 0 AND high >= low) FROM stock.ohlc WHERE date = (SELECT MAX(date) FROM stock.ohlc)").
		Scan(&totalRows, &validRows); err == nil && totalRows > 0 {
		status.MissingRate = 1.0 - float64(validRows)/float64(totalRows)
	}

	if !status.Fresh {
		status.Reason = fmt.Sprintf("数据已过期：最新交易日 %s，应更新到 %s（前一个交易日，距今 %d 天），禁止生成新的股票池", status.LatestDate, status.ExpectedDate, status.DaysSinceLast)
		return status
	}
	if status.StockCount == 0 {
		status.Reason = "股票基础数据为空，数据源不可用"
		return status
	}

	status.OK = true
	status.Reason = "数据正常"
	return status
}
