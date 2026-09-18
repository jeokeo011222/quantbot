package data

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "github.com/marcboeker/go-duckdb/v2"
)

// DuckDBManager DuckDB 管理器
// 单一数据库架构：直接使用 stock.duckdb 作为唯一数据库（catalog 名为 stock，
// 因此既有的 stock.* 前缀查询无需改动即可命中）。quantpilot.duckdb 已废弃。
// 严禁任何回退逻辑，数据缺失直接报错
type DuckDBManager struct {
	db         *sql.DB
	path       string
	stockSync  *StockSyncJob
	finSync    *FinancialSyncJob
	finFetcher FinancialFetcher // 可选：非东财的财务拉取器（如通达信终端）
	mu         sync.RWMutex
}

// FinancialFetcher 拉取单只股票的全历史财务报告（增量时可在 startReport 后截断）。
// 由外部（tools 层通达信实现）注入，避免 data 包反向依赖 tools 造成循环导入。
type FinancialFetcher func(ctx context.Context, symbol, startReport string) ([]FinancialReport, error)

// SetFinancialFetcher 注入自定义财务拉取器（如通达信终端接口）。
// 传入 nil 表示恢复默认（东财 datacenter）。仅在未处于同步任务时生效。
func (dm *DuckDBManager) SetFinancialFetcher(f FinancialFetcher) {
	dm.mu.Lock()
	defer dm.mu.Unlock()
	dm.finFetcher = f
}

// NewDuckDBManager 创建 DuckDB 管理器
// 仅使用可执行文件目录下的data文件夹，直连 stock.duckdb 作为唯一数据库
func NewDuckDBManager() (*DuckDBManager, error) {
	exePath, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("无法获取可执行文件路径: %w", err)
	}
	exeDir := filepath.Dir(exePath)
	dataDir := filepath.Join(exeDir, "data")
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, fmt.Errorf("无法创建data目录: %w", err)
	}

	dbPath := filepath.Join(dataDir, "stock.duckdb")

	db, err := sql.Open("duckdb", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to connect DuckDB: %w", err)
	}

	// 使用带超时的 Ping 防止数据库锁定导致挂起
	pingCtx, pingCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer pingCancel()
	if err := db.PingContext(pingCtx); err != nil {
		db.Close()
		return nil, fmt.Errorf("DuckDB 主数据库连接失败: %w", err)
	}
	log.Printf("[DuckDB] 主数据库连接成功: %s", dbPath)

	if err := verifyTables(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("行情数据表验证失败，禁止降级运行: %w", err)
	}

	dm := &DuckDBManager{
		db:   db,
		path: dbPath,
	}

	dm.initParquetDirectories()

	return dm, nil
}

// verifyTables 验证必要的表是否存在
func verifyTables(db *sql.DB) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 检查 stock.ohlc 表
	var ohlcCount int
	err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM stock.ohlc").Scan(&ohlcCount)
	if err != nil {
		return fmt.Errorf("stock.ohlc 表不存在或无法访问: %w", err)
	}
	if ohlcCount == 0 {
		return fmt.Errorf("stock.ohlc 表为空，无行情数据")
	}
	log.Printf("[DuckDB] stock.ohlc 表验证通过，共 %d 条记录", ohlcCount)

	// 检查 stock.stock_basic 表
	var stockBasicCount int
	err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM stock.stock_basic").Scan(&stockBasicCount)
	if err != nil {
		return fmt.Errorf("stock.stock_basic 表不存在或无法访问: %w", err)
	}
	if stockBasicCount == 0 {
		return fmt.Errorf("stock.stock_basic 表为空，无股票基础信息")
	}
	log.Printf("[DuckDB] stock.stock_basic 表验证通过，共 %d 条记录", stockBasicCount)

	return nil
}

// initParquetDirectories 初始化 Parquet 数据目录
func (dm *DuckDBManager) initParquetDirectories() {
	baseDir := dm.getDataDir()

	markets := []string{"US", "CN", "HK"}
	for _, market := range markets {
		dailyDir := filepath.Join(baseDir, "market", market, "daily")
		os.MkdirAll(dailyDir, 0755)
	}
}

// getDataDir 获取数据目录（仅使用可执行文件目录）
func (dm *DuckDBManager) getDataDir() string {
	if dm.path != "" {
		return filepath.Dir(dm.path)
	}
	exePath, err := os.Executable()
	if err != nil {
		panic(fmt.Sprintf("无法获取可执行文件路径: %v", err))
	}
	return filepath.Join(filepath.Dir(exePath), "data")
}

// CreateParquetView 创建 Parquet 视图
func (dm *DuckDBManager) CreateParquetView(market string) error {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	baseDir := dm.getDataDir()
	parquetPath := filepath.Join(baseDir, "market", market, "daily", "year=*", "part-*.parquet")

	// 该市场 Parquet 数据不存在时静默跳过，不创建视图（视图为可选，缺失不影响主流程）
	if matches, _ := filepath.Glob(parquetPath); len(matches) == 0 {
		return nil
	}

	query := fmt.Sprintf(`
		CREATE OR REPLACE VIEW %s_market_daily AS
		SELECT
			instrument_id,
			trade_date,
			open,
			high,
			low,
			close,
			volume,
			turnover
		FROM read_parquet('%s', HIVE_PARTITIONING=1)
	`, market, parquetPath)

	if _, err := dm.db.Exec(query); err != nil {
		return fmt.Errorf("创建 %s 市场 Parquet 视图失败: %w", market, err)
	}

	return nil
}

// CreateAllViews 创建所有市场的视图
// 单个市场失败仅记录并继续，避免某一市场数据缺失阻断其余市场视图创建
func (dm *DuckDBManager) CreateAllViews() error {
	markets := []string{"US", "CN", "HK"}
	var firstErr error
	for _, market := range markets {
		if err := dm.CreateParquetView(market); err != nil {
			log.Printf("[DuckDB] 创建 %s 市场 Parquet 视图失败（跳过，不影响其他市场）: %v", market, err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// EnsureMarketTables 确保市场数据表存在
func (dm *DuckDBManager) EnsureMarketTables() error {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	markets := []string{"US", "CN", "HK"}
	for _, market := range markets {
		query := fmt.Sprintf(`
			CREATE TABLE IF NOT EXISTS %s_market_daily (
				instrument_id VARCHAR,
				trade_date DATE,
				open DOUBLE,
				high DOUBLE,
				low DOUBLE,
				close DOUBLE,
				volume BIGINT,
				turnover DOUBLE,
				sector VARCHAR
			)
		`, market)
		if _, err := dm.db.Exec(query); err != nil {
			return fmt.Errorf("创建 %s_market_daily 表失败: %w", market, err)
		}
	}

	return nil
}

// HasMarketData 检查是否有市场数据可用
func (dm *DuckDBManager) HasMarketData(ctx context.Context, market string) bool {
	dm.mu.RLock()
	defer dm.mu.RUnlock()

	query := "SELECT COUNT(*) FROM stock.ohlc"
	var count int
	if err := dm.db.QueryRowContext(ctx, query).Scan(&count); err != nil {
		return false
	}
	return count > 0
}

// GetRecentPrices 获取最近 N 天的价格数据
func (dm *DuckDBManager) GetRecentPrices(ctx context.Context, market, instrumentID string, days int) ([]PriceData, error) {
	dm.mu.RLock()
	defer dm.mu.RUnlock()

	limit := days
	if limit <= 0 || limit > 1000 {
		limit = 250
	}

	candidateSymbols := buildSymbolCandidates(instrumentID)

	log.Printf("[DuckDB] GetRecentPrices: market=%s, instrumentID=%s, candidates=%v, limit=%d", market, instrumentID, candidateSymbols, limit)

	for _, symbol := range candidateSymbols {
		query := `
			SELECT symbol, date, open, high, low, close, volume, amount
			FROM stock.ohlc
			WHERE symbol = ?
			ORDER BY date DESC
			LIMIT ?
		`
		rows, err := dm.db.QueryContext(ctx, query, symbol, limit)
		if err != nil {
			log.Printf("[DuckDB] ohlc 查询失败 (symbol=%s): %v", symbol, err)
			continue
		}

		var prices []PriceData
		for rows.Next() {
			var bar KlineBarFromDuckDB
			if err := rows.Scan(
				&bar.Symbol,
				&bar.Date,
				&bar.Open,
				&bar.High,
				&bar.Low,
				&bar.Close,
				&bar.Volume,
				&bar.Amount,
			); err != nil {
				rows.Close()
				continue
			}
			prices = append(prices, PriceData{
				InstrumentID: instrumentID,
				TradeDate:    bar.Date,
				Open:         bar.Open,
				High:         bar.High,
				Low:          bar.Low,
				Close:        bar.Close,
				Volume:       int64(bar.Volume),
				Turnover:     bar.Amount,
			})
		}
		rows.Close()

		if len(prices) > 0 {
			log.Printf("[DuckDB] Found %d price rows for symbol=%s", len(prices), symbol)
			return prices, nil
		}
	}

	log.Printf("[DuckDB] No data found for instrumentID=%s, tried %d candidates: %v", instrumentID, len(candidateSymbols), candidateSymbols)

	// 如果主查询未找到，尝试通过模糊匹配 symbol 查询
	if len(candidateSymbols) > 0 {
		// 使用模糊匹配查询（支持部分代码匹配）
		// 仅匹配代码段（去掉市场前缀），避免短代码误匹配其他股票
		codePart := instrumentID
		lower := strings.ToLower(instrumentID)
		if len(lower) > 2 && (strings.HasPrefix(lower, "sh") || strings.HasPrefix(lower, "sz") || strings.HasPrefix(lower, "bj")) {
			codePart = instrumentID[2:]
		}
		if codePart == "" {
			codePart = instrumentID
		}

		fuzzyQuery := `
			SELECT symbol, date, open, high, low, close, volume, amount
			FROM stock.ohlc
			WHERE symbol LIKE ?
			ORDER BY date DESC
			LIMIT ?
		`
		fuzzyPattern := "%" + codePart + "%"
		rows, err := dm.db.QueryContext(ctx, fuzzyQuery, fuzzyPattern, limit)
		if err != nil {
			log.Printf("[DuckDB] 模糊查询失败 (%s): %v", fuzzyPattern, err)
		} else {
			var prices []PriceData
			matchedSymbol := ""
			for rows.Next() {
				var bar KlineBarFromDuckDB
				if err := rows.Scan(
					&bar.Symbol,
					&bar.Date,
					&bar.Open,
					&bar.High,
					&bar.Low,
					&bar.Close,
					&bar.Volume,
					&bar.Amount,
				); err != nil {
					continue
				}
				// 校验匹配到的 symbol 确实包含目标代码，防止误匹配其他股票
				if !strings.Contains(strings.ToLower(bar.Symbol), strings.ToLower(codePart)) {
					continue
				}
				if matchedSymbol == "" {
					matchedSymbol = bar.Symbol
				}
				prices = append(prices, PriceData{
					InstrumentID: bar.Symbol,
					TradeDate:    bar.Date,
					Open:         bar.Open,
					High:         bar.High,
					Low:          bar.Low,
					Close:        bar.Close,
					Volume:       int64(bar.Volume),
					Turnover:     bar.Amount,
				})
			}
			rows.Close()

			if len(prices) > 0 {
				log.Printf("[DuckDB] 模糊匹配成功: pattern=%s, matched symbol=%s, %d rows", fuzzyPattern, matchedSymbol, len(prices))
				return prices, nil
			}
		}
	}

	return nil, fmt.Errorf("未找到 %s/%s 的行情数据 (尝试了 %d 种代码格式)", market, instrumentID, len(candidateSymbols))
}

// buildSymbolCandidates 构建多种可能的股票代码格式
func buildSymbolCandidates(instrumentID string) []string {
	candidates := []string{instrumentID}

	// 处理带市场前缀的格式: CN:603110, CN-603110, CN/603110, SH:603110, SZ:000001
	cleanID := instrumentID
	for _, sep := range []string{":", "-", "/", "."} {
		if parts := strings.SplitN(instrumentID, sep, 2); len(parts) == 2 {
			potentialCode := parts[1]
			if len(potentialCode) == 6 {
				cleanID = potentialCode
				candidates = append(candidates, cleanID)
				break
			}
		}
	}

	// 处理 .SZ/.SH/.BJ 后缀的格式: 603110.SH -> 603110
	if strings.Contains(instrumentID, ".") {
		parts := strings.Split(instrumentID, ".")
		for _, p := range parts {
			if len(p) == 6 {
				cleanID = p
				candidates = append(candidates, cleanID)
				break
			}
		}
	}

	// 如果有带前缀的格式 (如 sh.603110, sz:000001)，提取纯代码
	lowerID := strings.ToLower(instrumentID)
	for _, prefix := range []string{"sh.", "sz.", "bj.", "SH.", "SZ.", "BJ.", "sh:", "sz:", "bj:", "SH:", "SZ:", "BJ:", "sh", "sz", "bj", "SH", "SZ", "BJ"} {
		if strings.HasPrefix(lowerID, strings.ToLower(prefix)) && len(instrumentID) > len(prefix) {
			codePart := instrumentID[len(prefix):]
			if len(codePart) == 6 {
				cleanID = codePart
				candidates = append(candidates, cleanID)
				break
			}
		}
	}

	if len(cleanID) == 6 {
		prefixes := []string{"", "sh", "sz", "bj", "SH", "SZ", "BJ", "SHSE", "SZSE"}
		for _, p := range prefixes {
			candidate := p + cleanID
			if candidate != instrumentID && candidate != cleanID {
				candidates = append(candidates, candidate)
			}
		}

		suffixes := []string{".SH", ".SZ", ".BJ", ".sh", ".sz", ".bj"}
		for _, s := range suffixes {
			candidates = append(candidates, cleanID+s)
		}

		if strings.HasPrefix(cleanID, "6") || strings.HasPrefix(cleanID, "5") {
			candidates = append(candidates, "sh."+cleanID, "SH."+cleanID, "sh:"+cleanID, "SH:"+cleanID)
		} else if strings.HasPrefix(cleanID, "8") || strings.HasPrefix(cleanID, "4") {
			candidates = append(candidates, "bj."+cleanID, "BJ."+cleanID, "bj:"+cleanID, "BJ:"+cleanID)
		} else {
			candidates = append(candidates, "sz."+cleanID, "SZ."+cleanID, "sz:"+cleanID, "SZ:"+cleanID)
		}

		// 添加纯数字格式（不带前缀）
		candidates = append(candidates, cleanID)
	}

	// 去重
	seen := make(map[string]bool)
	var result []string
	for _, c := range candidates {
		if !seen[strings.ToLower(c)] {
			seen[strings.ToLower(c)] = true
			result = append(result, c)
		}
	}

	return result
}

// CalculateReturns 计算收益率
func (dm *DuckDBManager) CalculateReturns(ctx context.Context, market, instrumentID string, startDate, endDate time.Time) ([]ReturnData, error) {
	dm.mu.RLock()
	defer dm.mu.RUnlock()

	query := `
		SELECT
			symbol as instrument_id,
			date as trade_date,
			close / LAG(close) OVER (PARTITION BY symbol ORDER BY date) - 1 AS daily_return,
			close / LAG(close, 5) OVER (PARTITION BY symbol ORDER BY date) - 1 AS weekly_return,
			close / LAG(close, 21) OVER (PARTITION BY symbol ORDER BY date) - 1 AS monthly_return
		FROM stock.ohlc
		WHERE symbol = ?
		AND date >= ?
		AND date <= ?
		ORDER BY date
	`

	rows, err := dm.db.QueryContext(ctx, query, instrumentID, startDate, endDate)
	if err != nil {
		return nil, fmt.Errorf("计算收益率失败: %w", err)
	}
	defer rows.Close()

	var returns []ReturnData
	for rows.Next() {
		var r ReturnData
		if err := rows.Scan(
			&r.InstrumentID,
			&r.TradeDate,
			&r.DailyReturn,
			&r.WeeklyReturn,
			&r.MonthlyReturn,
		); err != nil {
			return nil, fmt.Errorf("扫描收益率行失败: %w", err)
		}
		returns = append(returns, r)
	}

	if len(returns) == 0 {
		return nil, fmt.Errorf("未找到 %s 的收益率数据", instrumentID)
	}

	return returns, nil
}

// GetMarketStats 获取市场统计数据
func (dm *DuckDBManager) GetMarketStats(ctx context.Context, market string) ([]MarketStats, error) {
	dm.mu.RLock()
	defer dm.mu.RUnlock()

	query := `
		SELECT 
			COALESCE(NULLIF(industry, ''), NULLIF(area, ''), '其他') as sector,
			COUNT(*) as stock_count,
			0 as avg_price,
			0 as total_volume
		FROM stock.stock_basic
		GROUP BY COALESCE(NULLIF(industry, ''), NULLIF(area, ''), '其他')
		ORDER BY stock_count DESC
		LIMIT 20
	`

	rows, err := dm.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("获取市场统计失败: %w", err)
	}
	defer rows.Close()

	var stats []MarketStats
	for rows.Next() {
		var s MarketStats
		if err := rows.Scan(&s.Sector, &s.StockCount, &s.AvgPrice, &s.TotalVolume); err != nil {
			continue
		}
		stats = append(stats, s)
	}

	if len(stats) == 0 {
		return nil, fmt.Errorf("市场统计查询返回空结果")
	}

	return stats, nil
}

// SearchInstruments 搜索股票/合约
func (dm *DuckDBManager) SearchInstruments(ctx context.Context, query string, market string, limit int) ([]SearchResultItem, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}

	dm.mu.RLock()
	defer dm.mu.RUnlock()

	sqlQuery := `
		SELECT 
			symbol,
			COALESCE(name, '') as name,
			COALESCE(NULLIF(industry, ''), NULLIF(area, ''), '') as industry,
			COALESCE(is_st, false) as is_st
		FROM stock.stock_basic
		WHERE 1=1
	`
	var args []interface{}

	if query != "" {
		sqlQuery += " AND (symbol LIKE ? OR name LIKE ?)"
		searchPattern := "%" + query + "%"
		args = append(args, searchPattern, searchPattern)
	}

	if market == "CN" || market == "SH" || market == "SZ" {
		if market == "CN" {
			sqlQuery += " AND (symbol LIKE 'sh%' OR symbol LIKE 'sz%')"
		} else {
			sqlQuery += " AND symbol LIKE ?"
			args = append(args, strings.ToLower(market[:1])+"%")
		}
	}

	sqlQuery += " ORDER BY symbol LIMIT ?"
	args = append(args, limit)

	rows, err := dm.db.QueryContext(ctx, sqlQuery, args...)
	if err != nil {
		return nil, fmt.Errorf("搜索股票失败: %w", err)
	}
	defer rows.Close()

	var results []SearchResultItem
	for rows.Next() {
		var item SearchResultItem
		var isSt bool
		if err := rows.Scan(&item.Symbol, &item.Name, &item.Industry, &isSt); err != nil {
			continue
		}
		if isSt {
			item.Status = "ST"
		} else {
			item.Status = "normal"
		}
		item.Market = market
		results = append(results, item)
	}

	return results, nil
}

// Close 关闭连接
func (dm *DuckDBManager) Close() {
	if dm.db != nil {
		dm.db.Close()
	}
}

// ==================== 数据结构 ====================

// PriceData 价格数据
type PriceData struct {
	InstrumentID string
	TradeDate    time.Time
	Open         float64
	High         float64
	Low          float64
	Close        float64
	Volume       int64
	Turnover     float64
}

// ReturnData 收益率数据
type ReturnData struct {
	InstrumentID  string
	TradeDate     time.Time
	DailyReturn   float64
	WeeklyReturn  float64
	MonthlyReturn float64
}

// MarketStats 市场统计
type MarketStats struct {
	Sector      string
	StockCount  int
	AvgPrice    float64
	TotalVolume int64
}

// MarketBreadth 市场涨跌家数统计
type MarketBreadth struct {
	Total     int
	Advancers int
	Decliners int
	Flat      int
}

// GetMarketBreadth 获取市场涨跌家数统计（基于最新交易日收盘价）
func (dm *DuckDBManager) GetMarketBreadth(ctx context.Context, market string) (*MarketBreadth, error) {
	dm.mu.RLock()
	defer dm.mu.RUnlock()

	marketFilter := ""
	var args []interface{}
	if market != "" && market != "all" {
		// 通过 symbol 前缀过滤市场（sh/sz/bj）
		marketFilter = " AND symbol LIKE ?"
		args = append(args, strings.ToLower(market[:1])+"%")
	}

	query := `
		WITH ranked AS (
			SELECT symbol, close, date,
				ROW_NUMBER() OVER (PARTITION BY symbol ORDER BY date DESC) AS rn
			FROM stock.ohlc
			WHERE 1=1` + marketFilter + `
		)
		SELECT
			COUNT(*) AS total,
			COALESCE(SUM(CASE WHEN rn = 1 AND close > prev_close THEN 1 ELSE 0 END), 0) AS advancers,
			COALESCE(SUM(CASE WHEN rn = 1 AND close < prev_close THEN 1 ELSE 0 END), 0) AS decliners,
			COALESCE(SUM(CASE WHEN rn = 1 AND close = prev_close THEN 1 ELSE 0 END), 0) AS flat
		FROM (
			SELECT symbol, close, rn,
				LAG(close) OVER (PARTITION BY symbol ORDER BY date DESC) AS prev_close
			FROM ranked
		) t
		WHERE rn = 1
	`

	var b MarketBreadth
	if err := dm.db.QueryRowContext(ctx, query, args...).Scan(&b.Total, &b.Advancers, &b.Decliners, &b.Flat); err != nil {
		return nil, fmt.Errorf("获取市场涨跌统计失败: %w", err)
	}
	return &b, nil
}

// SearchResultItem 搜索结果项
type SearchResultItem struct {
	Symbol   string `json:"symbol"`
	Name     string `json:"name"`
	Market   string `json:"market"`
	Industry string `json:"industry"`
	Status   string `json:"status"`
}

// KlineBarFromDuckDB 从DuckDB读取的K线数据
type KlineBarFromDuckDB struct {
	Symbol string
	Date   time.Time
	Open   float64
	High   float64
	Low    float64
	Close  float64
	Volume float64
	Amount float64
}

// HasStockDB 检查行情库是否可用（单库直连模式下即数据库是否连接成功）
func (dm *DuckDBManager) HasStockDB() bool {
	return dm != nil && dm.db != nil
}

// GetPath 获取数据库路径
func (dm *DuckDBManager) GetPath() string {
	return dm.path
}

// GetKlineFromStock 从数据库获取K线数据
func (dm *DuckDBManager) GetKlineFromStock(ctx context.Context, symbol string, days int) ([]KlineBarFromDuckDB, error) {
	dm.mu.RLock()
	defer dm.mu.RUnlock()

	var query string
	var args []interface{}

	if days > 0 {
		query = `
			SELECT symbol, date, open, high, low, close, volume, amount
			FROM stock.ohlc
			WHERE symbol = ?
			ORDER BY date DESC
			LIMIT ?
		`
		args = []interface{}{symbol, days}
	} else {
		query = `
			SELECT symbol, date, open, high, low, close, volume, amount
			FROM stock.ohlc
			WHERE symbol = ?
			ORDER BY date DESC
		`
		args = []interface{}{symbol}
	}

	rows, err := dm.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("查询 ohlc 失败 (symbol=%s): %w", symbol, err)
	}
	defer rows.Close()

	var bars []KlineBarFromDuckDB
	for rows.Next() {
		var bar KlineBarFromDuckDB
		if err := rows.Scan(
			&bar.Symbol,
			&bar.Date,
			&bar.Open,
			&bar.High,
			&bar.Low,
			&bar.Close,
			&bar.Volume,
			&bar.Amount,
		); err != nil {
			return nil, fmt.Errorf("扫描 ohlc 行失败: %w", err)
		}
		bars = append(bars, bar)
	}

	if len(bars) == 0 {
		return nil, fmt.Errorf("未找到 %s 的K线数据", symbol)
	}

	return bars, nil
}

// GetIndexKlineFromStock 从数据库获取指数K线数据
func (dm *DuckDBManager) GetIndexKlineFromStock(ctx context.Context, indexCode string, days int) ([]KlineBarFromDuckDB, error) {
	dm.mu.RLock()
	defer dm.mu.RUnlock()

	var query string
	var args []interface{}

	if days > 0 {
		query = `
			SELECT symbol, date, open, high, low, close, volume, amount
			FROM stock.ohlc
			WHERE symbol LIKE ?
			ORDER BY date DESC
			LIMIT ?
		`
		args = []interface{}{indexCode + "%", days}
	} else {
		query = `
			SELECT symbol, date, open, high, low, close, volume, amount
			FROM stock.ohlc
			WHERE symbol LIKE ?
			ORDER BY date DESC
		`
		args = []interface{}{indexCode + "%"}
	}

	rows, err := dm.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("查询指数 ohlc 失败 (index=%s): %w", indexCode, err)
	}
	defer rows.Close()

	var bars []KlineBarFromDuckDB
	for rows.Next() {
		var bar KlineBarFromDuckDB
		if err := rows.Scan(
			&bar.Symbol,
			&bar.Date,
			&bar.Open,
			&bar.High,
			&bar.Low,
			&bar.Close,
			&bar.Volume,
			&bar.Amount,
		); err != nil {
			return nil, fmt.Errorf("扫描指数 ohlc 行失败: %w", err)
		}
		bars = append(bars, bar)
	}

	if len(bars) == 0 {
		return nil, fmt.Errorf("未找到指数 %s 的K线数据", indexCode)
	}

	return bars, nil
}

// GetStockNameFromStock 从数据库获取股票名称（兼容不同symbol格式）
func (dm *DuckDBManager) GetStockNameFromStock(ctx context.Context, symbol string) (string, error) {
	dm.mu.RLock()
	defer dm.mu.RUnlock()

	// 尝试多种格式匹配：完整格式（sh600000）或纯数字格式（600000）
	// 使用 COALESCE 处理 NULL 值
	var name string
	err := dm.db.QueryRowContext(ctx,
		`SELECT COALESCE(name, '') FROM stock.stock_basic WHERE symbol = ? OR symbol = SUBSTR(?, 3) LIMIT 1`,
		symbol, symbol,
	).Scan(&name)
	if err != nil {
		return "", fmt.Errorf("获取股票名称失败 (symbol=%s): %w", symbol, err)
	}

	return name, nil
}

// StockSymbolInfo 股票代码信息
type StockSymbolInfo struct {
	Symbol string
	Name   string
	Market string
}

// ListAllSymbolsFromStock 从数据库获取所有普通A股列表
// 统一数据源：从 stock_basic 视图查询 symbol/name，通过代码前缀推导 market
// 只筛选普通A股（通过 symbol 前缀 sh/sz/bj 过滤），从根源排除指数/基金/债券等非股票证券
func (dm *DuckDBManager) ListAllSymbolsFromStock(ctx context.Context, market string, limit int) ([]StockSymbolInfo, error) {
	dm.mu.RLock()
	defer dm.mu.RUnlock()

	log.Printf("[ListAllSymbols] 执行查询: market=%s, limit=%d", market, limit)

	// 使用 COALESCE 处理 NULL 值，防止扫描错误；
	// GROUP BY symbol 去重：stock_basic 存在大量重复行（同 symbol 多条），
	// 去重后选股宇宙从 1.1万+ 收敛到真实的 5000 多只，避免重复加载K线导致卡顿/内存膨胀。
	query := `
		SELECT symbol, MAX(COALESCE(name, '')) as name
		FROM stock.stock_basic
		WHERE 1=1
	`
	var args []interface{}
	// 统一证券分类：只保留可交易的普通A股，SQL 层用权威代码段规则排除指数/基金/债券/B股
	// （配合 Go 层 IsTradableStock 双重校验，保证选股宇宙永远只含个股）
	switch strings.ToLower(market) {
	case "sh":
		query += " AND symbol LIKE 'sh6%'" // 沪A主板600/601/603/605 + 科创板688
	case "sz":
		query += " AND (symbol LIKE 'sz00%' OR symbol LIKE 'sz30%')" // 深主板000/001/002/003 + 创业板300/301
	case "bj":
		query += " AND symbol LIKE 'bj%'" // 北交所股票
	case "cn", "all", "":
		// 全部A股（sh主板/科创 + sz主板/创业 + 北交所），排除指数(sz399/sh000等)、基金、B股
		query += " AND ((symbol LIKE 'sh6%') OR (symbol LIKE 'sz00%') OR (symbol LIKE 'sz30%') OR (symbol LIKE 'bj%'))"
	default:
		return nil, fmt.Errorf("未知市场参数: %s", market)
	}
	query += " GROUP BY symbol ORDER BY symbol"

	rows, err := dm.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("查询股票列表失败: %w", err)
	}
	defer rows.Close()

	var symbols []StockSymbolInfo
	var missingNames []string
	for rows.Next() {
		var info StockSymbolInfo
		if err := rows.Scan(&info.Symbol, &info.Name); err != nil {
			return nil, fmt.Errorf("扫描股票行失败: %w", err)
		}
		// Go 层双重校验：确保只含可交易的普通A股（指数/基金/债券/B股一律剔除）
		if !IsTradableStock(info.Symbol) {
			continue
		}
		if strings.TrimSpace(info.Name) == "" {
			// 数据统一性：stock_basic 缺名时，回填权威字典 stock_dict.json 的名称
			if dictName := strings.TrimSpace(GetDictLoader().GetStockName(info.Symbol)); dictName != "" {
				info.Name = dictName
			}
		}
		if strings.TrimSpace(info.Name) == "" {
			// 跳过没有名称的股票，但不报错
			missingNames = append(missingNames, info.Symbol)
			continue
		}
		// 通过 symbol 前缀推导 market
		info.Market = determineMarketFromSymbol(info.Symbol)
		symbols = append(symbols, info)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历股票列表失败: %w", err)
	}

	// 记录警告但不阻止查询
	if len(missingNames) > 0 {
		log.Printf("[ListAllSymbols] 警告: %d 只股票缺少名称，已跳过", len(missingNames))
	}

	// 统计各市场分布
	marketCount := map[string]int{"SH": 0, "SZ": 0, "BJ": 0}
	for _, s := range symbols {
		marketCount[s.Market]++
	}
	log.Printf("[ListAllSymbols] 查询完成: %d 只股票 (沪:%d, 深:%d, 北:%d)",
		len(symbols), marketCount["SH"], marketCount["SZ"], marketCount["BJ"])

	if limit > 0 && len(symbols) > limit {
		symbols = symbols[:limit]
	}

	if len(symbols) == 0 {
		log.Printf("[ListAllSymbols] 警告: 未找到任何符合条件的普通股票 (market=%s), 可能是数据源为空或参数不正确", market)
		return symbols, nil
	}

	return symbols, nil
}

// boardOf 按代码前缀返回证券所属板块分类（用于代表性抽样分层）：
// "bj"北交所 / "kc"科创板 / "cy"创业板 / "sz"深主板 / "sh"沪主板 / "other"无法归类。
func boardOf(symbol string) string {
	s := strings.ToLower(strings.TrimSpace(symbol))
	switch {
	case strings.HasPrefix(s, "bj"):
		return "bj"
	case strings.HasPrefix(s, "sh688"):
		return "kc"
	case strings.HasPrefix(s, "sh6"):
		return "sh"
	case strings.HasPrefix(s, "sz30"):
		return "cy"
	case strings.HasPrefix(s, "sz00"):
		return "sz"
	default:
		return "other"
	}
}

// repSampleSeed 代表性抽样的固定随机种子。固定种子保证多次判势/重启间采样结果可复现，
// 避免样本在两次判势间随机漂移导致广度信号抖动；样本只随宇宙（新上市/退市）变化而演进。
const repSampleSeed = 20240901

// ListRepresentativeSymbols 返回分层抽样（按板块等比例）的代表性普通A股宇宙，用于六维判势的
// 市场广度/情绪维度。与 ListAllSymbolsFromStock 的 `ORDER BY symbol LIMIT n` 不同：
// 后者按字典序截取会让样本全落在北交所(0-9-...b)与沪600上，造成「全市场涨跌家数 vs 采样新高新低/涨跌停」
// 明显口径不一致；本方法按 沪主板/深主板/创业板/科创板/北交所 五层等比例抽样，样本对全A股有代表性。
// n<=0 时取全宇宙。固定种子 + 板内随机排列，保证可复现且每层有代表性覆盖。
func (dm *DuckDBManager) ListRepresentativeSymbols(ctx context.Context, n int) ([]StockSymbolInfo, error) {
	dm.mu.RLock()
	defer dm.mu.RUnlock()

	query := `
		SELECT symbol, MAX(COALESCE(name, '')) as name
		FROM stock.stock_basic
		WHERE ((symbol LIKE 'sh6%') OR (symbol LIKE 'sz00%') OR (symbol LIKE 'sz30%') OR (symbol LIKE 'bj%'))
		GROUP BY symbol
	`
	rows, err := dm.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("查询股票列表失败: %w", err)
	}
	defer rows.Close()

	buckets := map[string][]StockSymbolInfo{"sh": {}, "sz": {}, "cy": {}, "kc": {}, "bj": {}, "other": {}}
	var all []StockSymbolInfo
	for rows.Next() {
		var info StockSymbolInfo
		if err := rows.Scan(&info.Symbol, &info.Name); err != nil {
			return nil, fmt.Errorf("扫描股票行失败: %w", err)
		}
		if !IsTradableStock(info.Symbol) {
			continue
		}
		if strings.TrimSpace(info.Name) == "" {
			if dictName := strings.TrimSpace(GetDictLoader().GetStockName(info.Symbol)); dictName != "" {
				info.Name = dictName
			}
		}
		info.Market = determineMarketFromSymbol(info.Symbol)
		b := boardOf(info.Symbol)
		buckets[b] = append(buckets[b], info)
		all = append(all, info)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if n <= 0 || len(all) <= n {
		return all, nil
	}

	// 各板块按其在全宇宙中的占比等比例配额（保证北交/科创等小板块不被淹没，也不过度代表）
	rng := rand.New(rand.NewSource(repSampleSeed))
	order := []string{"sh", "sz", "cy", "kc", "bj", "other"}
	quota := map[string]int{}
	assigned := 0
	for _, b := range order {
		buckets[b] = shuffleSymbols(rng, buckets[b])
		q := n * len(buckets[b]) / len(all)
		if q < 0 {
			q = 0
		}
		quota[b] = q
		assigned += q
	}
	// 余量分配给仍有容量的板块（按顺序），保证总数正好 n
	for _, b := range order {
		if assigned >= n {
			break
		}
		if add := len(buckets[b]) - quota[b]; add > 0 {
			take := n - assigned
			if take > add {
				take = add
			}
			quota[b] += take
			assigned += take
		}
	}

	out := make([]StockSymbolInfo, 0, n)
	for _, b := range order {
		out = append(out, buckets[b][:quota[b]]...)
	}
	log.Printf("[ListRepresentativeSymbols] 分层抽样完成: 目标=%d 实得=%d (沪:%d 深:%d 创:%d 科:%d 北:%d)",
		n, len(out), quota["sh"], quota["sz"], quota["cy"], quota["kc"], quota["bj"])
	return out, nil
}

// shuffleSymbols 用给定 rng 在原切片上做 Fisher–Yates 洗牌（就地），返回打乱后的切片。
func shuffleSymbols(rng *rand.Rand, s []StockSymbolInfo) []StockSymbolInfo {
	for i := len(s) - 1; i > 0; i-- {
		j := rng.Intn(i + 1)
		s[i], s[j] = s[j], s[i]
	}
	return s
}

// IsTradableStock 判断 symbol 是否为可交易的普通A股。
// 委托统一的证券分类器 ClassifySecurity 判定（主板/科创板/创业板/北交所为股票，
// 指数/ETF/基金/可转债/债券/B股一概排除），保证全系统口径一致。
func IsTradableStock(symbol string) bool {
	return ClassifySecurity(symbol).IsStock()
}

// determineMarketFromSymbol 根据股票代码（含交易所前缀）准确判断所属市场
// 规则：
//
//	优先按交易所前缀权威判定：sh->SH, sz->SZ, bj->BJ（不再剥离前缀后误判，
//	如 sh000001 上证指数 属于 SH，而不是被误判为 SZ）
//	无前缀时按数字代码规则判定：
//	上海(SH): 600/601/603/605(主板A), 688(科创板), 900(B股)
//	深圳(SZ): 000/001/002/003(主板A), 300/301/302(创业板), 200(B股)
//	北京(BJ): 920/43x/83x/87x
//
// 返回空字符串表示无法识别
func determineMarketFromSymbol(symbol string) string {
	if len(symbol) == 0 {
		return ""
	}

	cleaned := strings.ToLower(strings.TrimSpace(symbol))

	// 提取纯数字代码并记录交易所前缀（权威）
	codePart := cleaned
	exchangePrefix := ""
	for _, p := range []string{"sh.", "sz.", "bj.", "sh_", "sz_", "bj_", "sh", "sz", "bj"} {
		if strings.HasPrefix(cleaned, p) {
			exchangePrefix = p[:2]
			codePart = cleaned[len(p):]
			break
		}
	}
	codePart = strings.ReplaceAll(codePart, ".", "")
	codePart = strings.ReplaceAll(codePart, "_", "")
	codePart = strings.ReplaceAll(codePart, "-", "")

	// 如果不是6位数字，无法识别
	if len(codePart) != 6 || !isNumeric(codePart) {
		return ""
	}

	// 带交易所前缀：按前缀权威判定所属市场
	switch exchangePrefix {
	case "sh":
		return "SH"
	case "sz":
		return "SZ"
	case "bj":
		return "BJ"
	}

	// 无前缀：按数字代码规则判定
	prefix3 := codePart[:3]
	switch {
	// 上海证券交易所
	case prefix3 == "600" || prefix3 == "601" || prefix3 == "603" || prefix3 == "605":
		return "SH" // 沪市主板A股
	case prefix3 == "688":
		return "SH" // 科创板
	case prefix3 == "900":
		return "SH" // 沪市B股

	// 深圳证券交易所
	case prefix3 == "000" || prefix3 == "001" || prefix3 == "002" || prefix3 == "003":
		return "SZ" // 深市主板A股
	case prefix3 == "300" || prefix3 == "301" || prefix3 == "302":
		return "SZ" // 创业板
	case prefix3 == "200":
		return "SZ" // 深市B股

	// 北京证券交易所
	case prefix3 == "920" || prefix3[0] == '4' || prefix3[0] == '8':
		return "BJ" // 北交所

	default:
		return "" // 无法识别的代码
	}
}

// DetermineMarketFromSymbol 导出版本：根据股票代码前缀准确判断所属市场
// 返回 "SH", "SZ", "BJ" 或空字符串（无法识别）
func DetermineMarketFromSymbol(symbol string) string {
	return determineMarketFromSymbol(symbol)
}

// isNumeric 检查字符串是否全为数字
func isNumeric(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// ============ 批量因子数据查询 ============

// FactorBar 因子计算所需的K线数据
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

// BatchGetFactorBars 批量获取多只股票的因子计算数据
func (dm *DuckDBManager) BatchGetFactorBars(ctx context.Context, symbols []string, days int) (map[string][]FactorBar, error) {
	dm.mu.RLock()
	defer dm.mu.RUnlock()

	if len(symbols) == 0 {
		return nil, fmt.Errorf("股票代码列表为空")
	}

	if days <= 0 {
		days = 252
	}

	// days 表示"交易日数"，换算成足够大的自然日窗口以取满对应数量的交易日K线
	startDate := tradingToCalendarStartDate(days)

	symbolPlaceholders := make([]string, len(symbols))
	args := make([]interface{}, 0, len(symbols)+1)
	for i, s := range symbols {
		symbolPlaceholders[i] = "?"
		args = append(args, s)
	}
	args = append(args, startDate)

	query := fmt.Sprintf(`
		SELECT
			symbol,
			date,
			open,
			high,
			low,
			close,
			LAG(close) OVER (PARTITION BY symbol ORDER BY date) as pre_close,
			volume,
			amount,
			(close - LAG(close) OVER (PARTITION BY symbol ORDER BY date)) / NULLIF(LAG(close) OVER (PARTITION BY symbol ORDER BY date), 0) * 100 as pct_chg,
			(high - low) / NULLIF(LAG(close) OVER (PARTITION BY symbol ORDER BY date), 0) * 100 as amplitude
		FROM stock.ohlc
		WHERE symbol IN (%s) AND date >= ?
		ORDER BY symbol, date DESC
	`, strings.Join(symbolPlaceholders, ","))

	log.Printf("[BatchGetFactorBars] 查询 %d 只股票, 起始日期: %s", len(symbols), startDate)

	rows, err := dm.db.QueryContext(ctx, query, args...)
	if err != nil {
		log.Printf("[BatchGetFactorBars] 查询失败: %v", err)
		return nil, fmt.Errorf("批量查询因子数据失败: %w", err)
	}
	defer rows.Close()

	result := make(map[string][]FactorBar)
	rowCount := 0
	for rows.Next() {
		var bar FactorBar
		var preClose, pctChg, amplitude sql.NullFloat64
		if err := rows.Scan(
			&bar.Symbol,
			&bar.Date,
			&bar.Open,
			&bar.High,
			&bar.Low,
			&bar.Close,
			&preClose,
			&bar.Volume,
			&bar.Amount,
			&pctChg,
			&amplitude,
		); err != nil {
			log.Printf("[BatchGetFactorBars] 扫描行失败: %v", err)
			continue
		}

		if preClose.Valid {
			bar.PreClose = preClose.Float64
		}
		if pctChg.Valid {
			bar.PctChg = pctChg.Float64
		}
		if amplitude.Valid {
			bar.Amplitude = amplitude.Float64
		}
		bar.Turnover = bar.Amount

		result[bar.Symbol] = append(result[bar.Symbol], bar)
		rowCount++
	}

	log.Printf("[BatchGetFactorBars] 查询完成: %d 行数据, %d 只股票有数据", rowCount, len(result))

	if len(result) == 0 {
		return nil, fmt.Errorf("批量查询因子数据返回空结果 (%d 只股票, 起始日期 %s)", len(symbols), startDate)
	}

	return result, nil
}

// tradingToCalendarStartDate 将"交易日数"换算为覆盖足够的自然日窗口的起始日期。
// 一年约 250 个交易日 ≈ 365 个自然日，且受周末与节假日影响，交易日/自然日 ≈ 0.63；
// 为避免因窗口不足导致取不满 lookback 个交易日K线（例如传入 252 却只拿到 167 根），
// 按 1.6 倍放大并加 20 天缓冲。
func tradingToCalendarStartDate(tradingDays int) string {
	if tradingDays <= 0 {
		tradingDays = 252
	}
	calendarDays := int(float64(tradingDays)*1.6) + 20
	return time.Now().AddDate(0, 0, -calendarDays).Format("2006-01-02")
}

// GetMarketIndexBars 获取市场指数K线数据
func (dm *DuckDBManager) GetMarketIndexBars(ctx context.Context, days int) (map[string][]FactorBar, error) {
	dm.mu.RLock()
	defer dm.mu.RUnlock()

	if days <= 0 {
		days = 252
	}

	// days 表示"交易日数"，须换算成足够大的自然日窗口才能取满对应数量的交易日K线
	// （252 个交易日 ≈ 365 个自然日，按交易日)1.6 倍 + 缓冲 覆盖周末与节假日）
	startDate := tradingToCalendarStartDate(days)

	indices := []string{"sh000001", "sz399001", "sh000300", "sh000905"}
	result := make(map[string][]FactorBar)

	for _, idx := range indices {
		query := `
			SELECT
				symbol,
				date,
				open,
				high,
				low,
				close,
				LAG(close) OVER (ORDER BY date) as pre_close,
				volume,
				amount,
				(close - LAG(close) OVER (ORDER BY date)) / NULLIF(LAG(close) OVER (ORDER BY date), 0) * 100 as pct_chg,
				(high - low) / NULLIF(LAG(close) OVER (ORDER BY date), 0) * 100 as amplitude
			FROM stock.ohlc
			WHERE symbol LIKE ? AND date >= ?
			ORDER BY date DESC
			LIMIT ?
		`

		rows, err := dm.db.QueryContext(ctx, query, idx+"%", startDate, days)
		if err != nil {
			log.Printf("[DuckDB] 获取指数 %s 数据失败: %v", idx, err)
			continue
		}

		for rows.Next() {
			var bar FactorBar
			var preClose, pctChg, amplitude sql.NullFloat64
			if err := rows.Scan(
				&bar.Symbol,
				&bar.Date,
				&bar.Open,
				&bar.High,
				&bar.Low,
				&bar.Close,
				&preClose,
				&bar.Volume,
				&bar.Amount,
				&pctChg,
				&amplitude,
			); err != nil {
				continue
			}

			if preClose.Valid {
				bar.PreClose = preClose.Float64
			}
			if pctChg.Valid {
				bar.PctChg = pctChg.Float64
			}
			if amplitude.Valid {
				bar.Amplitude = amplitude.Float64
			}
			bar.Turnover = bar.Amount

			result[idx] = append(result[idx], bar)
		}
		rows.Close()

		log.Printf("[DuckDB] 指数 %s: %d 条数据", idx, len(result[idx]))
	}

	if len(result) == 0 {
		return nil, fmt.Errorf("未获取到任何指数数据")
	}

	return result, nil
}

// GetSectorPerformance 获取行业表现数据
func (dm *DuckDBManager) GetSectorPerformance(ctx context.Context) ([]map[string]interface{}, error) {
	dm.mu.RLock()
	defer dm.mu.RUnlock()

	query := `
		SELECT
			COALESCE(sb.industry, '其他') as sector,
			COUNT(DISTINCT o.symbol) as stock_count,
			AVG(o.close) as avg_price,
			SUM(o.volume) as total_volume,
			SUM(o.amount) as total_amount
		FROM stock.ohlc o
		LEFT JOIN stock.stock_basic sb ON sb.symbol = o.symbol OR sb.symbol = SUBSTR(o.symbol, 3)
		WHERE o.date = (SELECT MAX(date) FROM stock.ohlc)
		GROUP BY COALESCE(sb.industry, '其他')
		ORDER BY total_amount DESC
	`

	rows, err := dm.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("获取行业表现失败: %w", err)
	}
	defer rows.Close()

	var results []map[string]interface{}
	for rows.Next() {
		var sector string
		var stockCount int
		var avgPrice, totalVolume, totalAmount float64
		if err := rows.Scan(&sector, &stockCount, &avgPrice, &totalVolume, &totalAmount); err != nil {
			continue
		}
		results = append(results, map[string]interface{}{
			"sector":       sector,
			"stock_count":  stockCount,
			"avg_price":    avgPrice,
			"total_volume": totalVolume,
			"total_amount": totalAmount,
		})
	}

	return results, nil
}

// MarketAmountDay 单日全市场成交额
type MarketAmountDay struct {
	Date   time.Time
	Amount float64
}

// GetMarketAmountHistory 获取最近 N 个交易日的全市场成交额序列（按日期升序）。
// 用于市场六维判势：量能维度（当日成交额/20日均值）与资金维度（连续放量天数代理）的真实数据源。
func (dm *DuckDBManager) GetMarketAmountHistory(ctx context.Context, days int) ([]MarketAmountDay, error) {
	dm.mu.RLock()
	defer dm.mu.RUnlock()

	if days <= 0 {
		days = 30
	}
	startDate := tradingToCalendarStartDate(days)

	query := `
		SELECT date, SUM(amount) AS total_amount
		FROM stock.ohlc
		WHERE date >= ? AND symbol IN (SELECT symbol FROM stock.stock_basic)
		GROUP BY date
		ORDER BY date ASC
	`
	rows, err := dm.db.QueryContext(ctx, query, startDate)
	if err != nil {
		return nil, fmt.Errorf("获取市场成交额历史失败: %w", err)
	}
	defer rows.Close()

	var result []MarketAmountDay
	for rows.Next() {
		var d MarketAmountDay
		if err := rows.Scan(&d.Date, &d.Amount); err != nil {
			continue
		}
		result = append(result, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历市场成交额历史失败: %w", err)
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("市场成交额历史为空")
	}
	return result, nil
}

// MarketLimitStats 全市场最新交易日涨跌停统计（真实数据，按主板/创业板科创/北交所口径分档）。
// 用于市场六维判势：情绪赚钱效应维度的真实数据源（替代小样本代理，反映全市场涨跌停动能）。
type MarketLimitStats struct {
	LimitUp        int // 收盘涨停家数
	LimitDown      int // 收盘跌停家数
	SealedLimitUp  int // 收盘封死涨停家数
	BlownUp        int // 盘中触涨停价但收盘未封（炸板）家数
	TouchedLimitUp int // 盘中触涨停价家数（含封板）
}

// GetMarketLimitStats 统计全市场最新交易日涨跌停家数（基于真实日K收盘/最高价 vs 前收盘价）。
// 无未来函数；涨跌停口径按代码前缀分档（688/300/301→20%，4/8/92开头→30%，其余→10%）。
func (dm *DuckDBManager) GetMarketLimitStats(ctx context.Context) (*MarketLimitStats, error) {
	dm.mu.RLock()
	defer dm.mu.RUnlock()

	query := `
		WITH allp AS (
			SELECT symbol, date, high, close,
				LAG(close) OVER (PARTITION BY symbol ORDER BY date) AS pc
			FROM stock.ohlc
			WHERE symbol IN (SELECT symbol FROM stock.stock_basic)
		),
		latest AS (
			SELECT symbol, high, close, pc
			FROM allp
			WHERE date = (SELECT MAX(date) FROM stock.ohlc)
		),
		tier AS (
			SELECT symbol, high, close, pc,
				CASE
					WHEN symbol LIKE '688%' OR symbol LIKE '300%' OR symbol LIKE '301%' THEN 1.20
					WHEN symbol LIKE '4%' OR symbol LIKE '8%' OR symbol LIKE '92%' THEN 1.30
					ELSE 1.10
				END AS ratio,
				CASE
					WHEN symbol LIKE '688%' OR symbol LIKE '300%' OR symbol LIKE '301%' THEN 0.80
					WHEN symbol LIKE '4%' OR symbol LIKE '8%' OR symbol LIKE '92%' THEN 0.70
					ELSE 0.90
				END AS down_ratio
			FROM latest
			WHERE pc > 0
		)
		SELECT
			COUNT(*) FILTER (WHERE close >= pc * ratio)                                AS limit_up,
			COUNT(*) FILTER (WHERE close <= pc * down_ratio)                           AS limit_down,
			COUNT(*) FILTER (WHERE close >= pc * ratio AND high >= pc * ratio)         AS sealed_up,
			COUNT(*) FILTER (WHERE high  >= pc * ratio AND close <  pc * ratio)        AS blown_up
		FROM tier
	`
	rows, err := dm.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("获取全市场涨跌停统计失败: %w", err)
	}
	defer rows.Close()

	st := &MarketLimitStats{}
	if rows.Next() {
		if err := rows.Scan(&st.LimitUp, &st.LimitDown, &st.SealedLimitUp, &st.BlownUp); err != nil {
			return nil, fmt.Errorf("解析全市场涨跌停统计失败: %w", err)
		}
	}
	st.TouchedLimitUp = st.SealedLimitUp + st.BlownUp
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历全市场涨跌停统计失败: %w", err)
	}
	if st.LimitUp == 0 && st.LimitDown == 0 && st.TouchedLimitUp == 0 {
		return nil, fmt.Errorf("全市场涨跌停统计为空")
	}
	return st, nil
}

// SectorChangeStat 行业当日表现统计
type SectorChangeStat struct {
	Sector     string
	StockCount int
	AvgChgPct  float64
}

// GetSectorChangeStats 获取各行业最新交易日的成分股平均涨跌幅。
// 用于市场六维判势：市场广度维度（有效主线板块数）与资金维度（资金集中度）的真实数据源。
func (dm *DuckDBManager) GetSectorChangeStats(ctx context.Context) ([]SectorChangeStat, error) {
	dm.mu.RLock()
	defer dm.mu.RUnlock()

	query := `
		WITH latest AS (
			SELECT symbol, close,
				LAG(close) OVER (PARTITION BY symbol ORDER BY date) AS prev_close
			FROM stock.ohlc
			WHERE date = (SELECT MAX(date) FROM stock.ohlc)
		)
		SELECT
			COALESCE(sb.industry, '其他') AS sector,
			COUNT(DISTINCT l.symbol) AS stock_count,
			AVG(CASE WHEN l.prev_close > 0 THEN (l.close - l.prev_close) / l.prev_close * 100 ELSE 0 END) AS avg_chg_pct
		FROM latest l
		LEFT JOIN stock.stock_basic sb ON sb.symbol = l.symbol OR sb.symbol = SUBSTR(l.symbol, 3)
		GROUP BY COALESCE(sb.industry, '其他')
		ORDER BY avg_chg_pct DESC
	`
	rows, err := dm.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("获取行业涨跌统计失败: %w", err)
	}
	defer rows.Close()

	var result []SectorChangeStat
	for rows.Next() {
		var s SectorChangeStat
		if err := rows.Scan(&s.Sector, &s.StockCount, &s.AvgChgPct); err != nil {
			continue
		}
		result = append(result, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历行业涨跌统计失败: %w", err)
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("行业涨跌统计为空")
	}
	return result, nil
}

// ==================== 约束自愈：补偿压缩重建丢失的唯一约束 ====================

// hasUniqueConstraint 检查表是否已有 UNIQUE / PRIMARY KEY 约束。
// DuckDB 数据库经 EXPORT/IMPORT 或 `CREATE TABLE AS SELECT` 重建时可能丢失主键约束，
// 导致依赖 `ON CONFLICT` 的写入报 Binder Error。
func (dm *DuckDBManager) hasUniqueConstraint(table string) (bool, error) {
	var n int
	err := dm.db.QueryRow(`
		SELECT COUNT(*) FROM duckdb_constraints()
		WHERE table_name = ? AND constraint_type IN ('UNIQUE','PRIMARY KEY')
	`, table).Scan(&n)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// repairTablePrimaryKey 当表缺失唯一约束时重建表以恢复主键（保留已有数据）。
// 要求调用方已持有 dm.mu 写锁。mysqlCreateSQL 用于创建带主键的临时表。
func (dm *DuckDBManager) repairTablePrimaryKey(table, tmpCreateSQL, columns string) error {
	ok, err := dm.hasUniqueConstraint(table)
	if err != nil {
		return fmt.Errorf("检查 %s 约束失败: %w", table, err)
	}
	if ok {
		return nil
	}

	tx, err := dm.db.Begin()
	if err != nil {
		return fmt.Errorf("重建 %s 开启事务失败: %w", table, err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(tmpCreateSQL); err != nil {
		return fmt.Errorf("重建 %s 创建临时表失败: %w", table, err)
	}
	if _, err := tx.Exec(fmt.Sprintf(
		"INSERT INTO stock.%s_tmp (%s) SELECT %s FROM stock.%s", table, columns, columns, table)); err != nil {
		return fmt.Errorf("重建 %s 迁移数据失败: %w", table, err)
	}
	if _, err := tx.Exec(fmt.Sprintf("DROP TABLE stock.%s", table)); err != nil {
		return fmt.Errorf("重建 %s 删除旧表失败: %w", table, err)
	}
	if _, err := tx.Exec(fmt.Sprintf("ALTER TABLE stock.%s_tmp RENAME TO %s", table, table)); err != nil {
		return fmt.Errorf("重建 %s 重命名失败: %w", table, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("重建 %s 提交失败: %w", table, err)
	}
	log.Printf("[DuckDB] 检测到 %s 缺少唯一约束，已重建表恢复主键", table)
	return nil
}

// ==================== 市场六维判势结果表 market_sixdim_daily ====================

// EnsureMarketSixDimTable 创建市场六维判势结果表（幂等，日期为主键）
func (dm *DuckDBManager) EnsureMarketSixDimTable() error {
	dm.mu.Lock()
	defer dm.mu.Unlock()
	return dm.ensureMarketSixDimTableLocked()
}

// ensureMarketSixDimTableLocked 创建表（须在持有 dm.mu 时调用）
func (dm *DuckDBManager) ensureMarketSixDimTableLocked() error {
	_, err := dm.db.Exec(`
		CREATE TABLE IF NOT EXISTS stock.market_sixdim_daily (
			trade_date          DATE PRIMARY KEY,
			dim_scores          VARCHAR,
			raw_total_score     DOUBLE,
			conflict_count      INTEGER,
			adjusted_total_score DOUBLE,
			position_rate       DOUBLE,
			market_tag          VARCHAR,
			sources             VARCHAR,
			created_at          TIMESTAMP
		)`)
	if err != nil {
		return fmt.Errorf("创建 market_sixdim_daily 表失败: %w", err)
	}
	return nil
}

// MarketSixDimRow 单日六维判势结果记录
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

// SaveMarketSixDimRow 写入/更新某交易日的六维判势结果
func (dm *DuckDBManager) SaveMarketSixDimRow(ctx context.Context, tradeDate, dimsJSON, sourcesJSON string,
	rawTotal, adjusted float64, conflict int, positionRate float64, tag string) error {
	dm.mu.Lock()
	defer dm.mu.Unlock()
	if err := dm.ensureMarketSixDimTableLocked(); err != nil {
		return err
	}
	// 数据库经 EXPORT/IMPORT 重建后 trade_date 主键约束可能丢失，
	// 导致 ON CONFLICT (trade_date) 报 Binder Error。故改用 UPDATE-else-INSERT，
	// 不依赖唯一约束；整套逻辑在 dm.mu 锁内执行，无并发/原子性问题。
	res, err := dm.db.ExecContext(ctx, `
		UPDATE stock.market_sixdim_daily SET
			dim_scores = ?,
			raw_total_score = ?,
			conflict_count = ?,
			adjusted_total_score = ?,
			position_rate = ?,
			market_tag = ?,
			sources = ?,
			created_at = ?
		WHERE trade_date = ?
	`, dimsJSON, rawTotal, conflict, adjusted, positionRate, tag, sourcesJSON, time.Now(), tradeDate)
	if err != nil {
		return fmt.Errorf("写入 market_sixdim_daily 失败: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		_, err = dm.db.ExecContext(ctx, `
			INSERT INTO stock.market_sixdim_daily (
				trade_date, dim_scores, raw_total_score, conflict_count,
				adjusted_total_score, position_rate, market_tag, sources, created_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, tradeDate, dimsJSON, rawTotal, conflict, adjusted, positionRate, tag, sourcesJSON, time.Now())
		if err != nil {
			return fmt.Errorf("写入 market_sixdim_daily 失败: %w", err)
		}
	}
	return nil
}

// GetMarketSixDimRows 读取最近 N 日的六维判势历史记录（按日期倒序）
func GetMarketSixDimRows(ctx context.Context, dm *DuckDBManager, limit int) ([]MarketSixDimRow, error) {
	if dm == nil {
		return nil, fmt.Errorf("DuckDB 不可用")
	}
	if limit <= 0 {
		limit = 7
	}
	// 读路径：仅 RLock 查询（与回测等只读访问并发，不互相阻塞），
	// 避免此前每请求都拿全局写锁执行 CREATE TABLE IF NOT EXISTS，在密集读写期被排队拖慢（曾阻塞约2分钟）。
	dm.mu.RLock()
	rows, err := dm.db.QueryContext(ctx, `
		SELECT trade_date, dim_scores, raw_total_score, conflict_count,
			adjusted_total_score, position_rate, market_tag, sources, created_at
		FROM stock.market_sixdim_daily
		ORDER BY trade_date DESC
		LIMIT ?
	`, limit)
	if err != nil {
		dm.mu.RUnlock()
		// 表不存在（首次运行）→ 升级为写锁建表后重试；其余错误直接返回
		msg := err.Error()
		if strings.Contains(msg, "does not exist") || strings.Contains(msg, "Catalog Error") ||
			strings.Contains(msg, "Table with name") {
			dm.mu.Lock()
			createErr := dm.ensureMarketSixDimTableLocked()
			dm.mu.Unlock()
			if createErr != nil {
				return nil, createErr
			}
			return GetMarketSixDimRows(ctx, dm, limit)
		}
		return nil, fmt.Errorf("查询 market_sixdim_daily 失败: %w", err)
	}

	var result []MarketSixDimRow
	for rows.Next() {
		var r MarketSixDimRow
		var createdAt time.Time
		if err := rows.Scan(&r.TradeDate, &r.DimScoresJSON, &r.RawTotalScore, &r.ConflictCount,
			&r.AdjustedTotalScore, &r.PositionRate, &r.MarketTag, &r.SourcesJSON, &createdAt); err != nil {
			continue
		}
		r.CreatedAt = createdAt
		result = append(result, r)
	}
	scanErr := rows.Err()
	rows.Close()
	dm.mu.RUnlock()
	if scanErr != nil {
		return nil, fmt.Errorf("遍历 market_sixdim_daily 失败: %w", scanErr)
	}
	return result, nil
}

// ==================== 盘口日频摘要 order_book_daily ====================

// OrderBookDailyRow 单标的单日盘口摘要（收盘后落库，供盘口因子研究与回测真实性）
type OrderBookDailyRow struct {
	TradeDate      string
	Code           string
	Name           string
	LimitUp        float64
	LimitDown      float64
	State          string
	StateCN        string
	VolumeRatio    float64
	InOutRatio     float64
	SealVol        float64
	HugeVolumeDown bool
	TurnoverRate   float64
}

// EnsureOrderBookDailyTable 创建盘口日频摘要表（幂等）
func (dm *DuckDBManager) EnsureOrderBookDailyTable() error {
	dm.mu.Lock()
	defer dm.mu.Unlock()
	return dm.ensureOrderBookDailyTableLocked()
}

// ensureOrderBookDailyTableLocked 创建表（须在持有 dm.mu 时调用）
func (dm *DuckDBManager) ensureOrderBookDailyTableLocked() error {
	_, err := dm.db.Exec(`
		CREATE TABLE IF NOT EXISTS stock.order_book_daily (
			trade_date       DATE,
			code             VARCHAR,
			name             VARCHAR,
			limit_up         DOUBLE,
			limit_down       DOUBLE,
			state            VARCHAR,
			state_cn         VARCHAR,
			volume_ratio     DOUBLE,
			in_out_ratio     DOUBLE,
			seal_vol         DOUBLE,
			huge_volume_down BOOLEAN,
			turnover_rate    DOUBLE,
			created_at       TIMESTAMP,
			PRIMARY KEY (trade_date, code)
		)`)
	if err != nil {
		return fmt.Errorf("创建 order_book_daily 表失败: %w", err)
	}
	return nil
}

// SaveOrderBookDailyRows 批量写入某交易日多标的盘口摘要（按 trade_date+code 覆盖/插入）
func (dm *DuckDBManager) SaveOrderBookDailyRows(ctx context.Context, rows []OrderBookDailyRow) error {
	if len(rows) == 0 {
		return nil
	}
	dm.mu.Lock()
	defer dm.mu.Unlock()
	if err := dm.ensureOrderBookDailyTableLocked(); err != nil {
		return err
	}
	tx, err := dm.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开启 order_book_daily 事务失败: %w", err)
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO stock.order_book_daily (
			trade_date, code, name, limit_up, limit_down, state, state_cn,
			volume_ratio, in_out_ratio, seal_vol, huge_volume_down, turnover_rate, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (trade_date, code) DO UPDATE SET
			name = excluded.name,
			limit_up = excluded.limit_up,
			limit_down = excluded.limit_down,
			state = excluded.state,
			state_cn = excluded.state_cn,
			volume_ratio = excluded.volume_ratio,
			in_out_ratio = excluded.in_out_ratio,
			seal_vol = excluded.seal_vol,
			huge_volume_down = excluded.huge_volume_down,
			turnover_rate = excluded.turnover_rate,
			created_at = excluded.created_at
	`)
	if err != nil {
		return fmt.Errorf("准备 order_book_daily 写入失败: %w", err)
	}
	defer stmt.Close()
	for _, r := range rows {
		if _, err := stmt.ExecContext(ctx, r.TradeDate, r.Code, r.Name, r.LimitUp, r.LimitDown,
			r.State, r.StateCN, r.VolumeRatio, r.InOutRatio, r.SealVol, r.HugeVolumeDown,
			r.TurnoverRate, time.Now()); err != nil {
			return fmt.Errorf("写入 order_book_daily 失败: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交 order_book_daily 失败: %w", err)
	}
	return nil
}

// GetOrderBookDailyRows 读取指定交易日（空串取最近一个数据日）的多标的盘口摘要，
// 返回 code(小写,如 sh600519 / 600519 双键) -> 记录
func GetOrderBookDailyRows(ctx context.Context, dm *DuckDBManager, tradeDate string) (map[string]OrderBookDailyRow, error) {
	if dm == nil {
		return nil, fmt.Errorf("DuckDB 不可用")
	}
	// 读路径可能触发 CREATE TABLE IF NOT EXISTS（首次），属写操作，统一用写锁避免并发冲突
	dm.mu.Lock()
	defer dm.mu.Unlock()
	if err := dm.ensureOrderBookDailyTableLocked(); err != nil {
		return nil, err
	}
	var rows *sql.Rows
	var err error
	if tradeDate != "" {
		rows, err = dm.db.QueryContext(ctx, `
			SELECT trade_date, code, name, limit_up, limit_down, state, state_cn,
				volume_ratio, in_out_ratio, seal_vol, huge_volume_down, turnover_rate
			FROM stock.order_book_daily
			WHERE trade_date = CAST(? AS DATE)
		`, tradeDate)
	} else {
		rows, err = dm.db.QueryContext(ctx, `
			SELECT trade_date, code, name, limit_up, limit_down, state, state_cn,
				volume_ratio, in_out_ratio, seal_vol, huge_volume_down, turnover_rate
			FROM stock.order_book_daily
			WHERE trade_date = (SELECT MAX(trade_date) FROM stock.order_book_daily)
		`)
	}
	if err != nil {
		return nil, fmt.Errorf("查询 order_book_daily 失败: %w", err)
	}
	defer rows.Close()

	result := make(map[string]OrderBookDailyRow)
	for rows.Next() {
		var r OrderBookDailyRow
		var huge bool
		if err := rows.Scan(&r.TradeDate, &r.Code, &r.Name, &r.LimitUp, &r.LimitDown,
			&r.State, &r.StateCN, &r.VolumeRatio, &r.InOutRatio, &r.SealVol, &huge, &r.TurnoverRate); err != nil {
			continue
		}
		r.HugeVolumeDown = huge
		codeLower := strings.ToLower(r.Code)
		result[codeLower] = r
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历 order_book_daily 失败: %w", err)
	}
	return result, nil
}
