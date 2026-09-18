package data

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"time"

	"github.com/quantpilot/quantpilot/internal/thssdk"
)

// ==================== 同花顺官方全市场日K导入 ====================
//
// 数据来源：同花顺官方全市场日K Parquet（daily-k / daily-k-10d dump），
// 原始未复权 OHLC，合并写入 stock.stock_daily 表（stock.ohlc 前向视图的底层）。
// 按 (symbol, date) ANTI JOIN 去重，已存在数据自动跳过，才可重复增量导入。
// 全部为官方真实行情，未配置 API Key 时直接报错，严禁伪造。
//
// Parquet schema（官方文档）：
//   thscode, currency, interval, adjusted, date_ms(毫秒), open_price,
//   high_price, low_price, close_price, volume(股), turnover(成交额)

// DailyKImportResult 日K导入结果摘要。
type DailyKImportResult struct {
	Mode       string `json:"mode"`       // full=全量近3年 / incr=近10交易日
	Downloaded int64  `json:"downloaded"` // 源 Parquet 行数
	Inserted   int64  `json:"inserted"`   // 实际新增入库行数
	Symbols    int64  `json:"symbols"`    // 覆盖股票数
	DateMin    string `json:"date_min"`   // 本批次最早日期
	DateMax    string `json:"date_max"`   // 本批次最晚日期
	Skipped    int64  `json:"skipped"`    // 因重复跳过行数（含 parquet 内部去重）
	Message    string `json:"message"`
}

// ImportTHSDailyK 从同花顺官方下载全市场日K Parquet，增量合并到 stock.stock_daily。
// mode: "full" 全量近3年 / "incr" 近10交易日增量。按 (symbol,date) 去重，已存在跳过。
// 未配置官方 API Key 时直接返回错误（严禁伪造/跳过）。
func (dm *DuckDBManager) ImportTHSDailyK(ctx context.Context, mode string) (*DailyKImportResult, error) {
	c := thssdk.Default()
	if c == nil {
		return nil, fmt.Errorf("同花顺官方数据源未配置（请在设置-数据源中启用并填写 API Key）")
	}
	if dm == nil || dm.db == nil {
		return nil, fmt.Errorf("DuckDB 未初始化")
	}

	// 1. 确定 dump 类型
	dumpType := thssdk.DumpDailyK
	if mode == "incr" {
		dumpType = thssdk.DumpDailyK10d
		mode = "incr"
	} else {
		mode = "full"
	}

	// 2. 获取预签名地址并下载日K Parquet（有效期约5分钟，须立即下载）
	dlURL, err := c.DumpDownloadURL(ctx, dumpType)
	if err != nil {
		return nil, fmt.Errorf("获取同花顺日K导出地址失败: %w", err)
	}
	pqPath := filepath.Join(dm.getDataDir(), "ths_daily_k.parquet")
	t0 := time.Now()
	if err := c.Download(ctx, dlURL, pqPath); err != nil {
		return nil, fmt.Errorf("下载同花顺日K Parquet 失败: %w", err)
	}
	log.Printf("[THS] 日K Parquet 下载完成(%s, %.1fs)", pqPath, time.Since(t0).Seconds())

	dm.mu.Lock()
	defer dm.mu.Unlock()
	pq := filepath.ToSlash(pqPath)

	// 3. 建暂存表并从 Parquet 加载：thscode→symbol、date_ms→date、*_price 映射，内部按 (symbol,date) 去重
	// 全量导入默认仅保留近3年（数据太多无意义）；增量近10交易日在窗口内无需过滤。dateFilter 恒为近3年/无的窗口
	dateFilter := ""
	if mode == "full" {
		dateFilter = " AND date >= CAST(now() - INTERVAL '3 years' AS DATE)"
	}
	createSQL := fmt.Sprintf(`
		CREATE OR REPLACE TABLE stock.daily_k_new AS
		SELECT symbol, date, open, high, low, close, volume, amount
		FROM (
			SELECT
				lower(split_part(thscode, '.', 2)) || split_part(thscode, '.', 1) AS symbol,
				CAST(epoch_ms(date_ms) AS DATE) AS date,
				open_price::DOUBLE  AS open,
				high_price::DOUBLE  AS high,
				low_price::DOUBLE   AS low,
				close_price::DOUBLE AS close,
				volume::DOUBLE      AS volume,
				turnover::DOUBLE    AS amount,
				ROW_NUMBER() OVER (
					PARTITION BY lower(split_part(thscode, '.', 2)) || split_part(thscode, '.', 1), CAST(epoch_ms(date_ms) AS DATE)
					ORDER BY date_ms DESC
				) AS rn
			FROM read_parquet('%s')
		) WHERE rn = 1%s
	`, pq, dateFilter)
	if _, err := dm.db.ExecContext(ctx, createSQL); err != nil {
		return nil, fmt.Errorf("创建日K暂存表失败: %w", err)
	}
	defer dm.db.ExecContext(ctx, "DROP TABLE IF EXISTS stock.daily_k_new")

	// 4. 清理 stock_daily 历史遗留重复 (symbol,date)，仅保留一条
	if _, err := dm.db.ExecContext(ctx, `
		DELETE FROM stock.stock_daily
		WHERE rowid IN (
			SELECT rowid FROM (
				SELECT rowid, ROW_NUMBER() OVER (PARTITION BY symbol, date ORDER BY date DESC) AS rn
				FROM stock.stock_daily
			) WHERE rn > 1
		)
	`); err != nil {
		log.Printf("[THS] 清理 stock_daily 历史重复数据失败(继续执行): %v", err)
	}

	// 5. 合并：仅插入 (symbol,date) 不存在的行，已存在数据自动跳过
	res, err := dm.db.ExecContext(ctx, `
		INSERT INTO stock.stock_daily (symbol, date, open, high, low, close, volume, amount)
		SELECT n.symbol, n.date, n.open, n.high, n.low, n.close, n.volume, n.amount
		FROM stock.daily_k_new n
		ANTI JOIN stock.stock_daily d ON n.symbol = d.symbol AND n.date = d.date
	`)
	if err != nil {
		return nil, fmt.Errorf("合并日K到 stock_daily 失败: %w", err)
	}
	inserted, _ := res.RowsAffected()

	// 6. 重建索引
	if _, err := dm.db.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS idx_stock_daily_symbol_date ON stock.stock_daily(symbol, date)"); err != nil {
		log.Printf("[THS] 创建索引失败(继续执行): %v", err)
	}

	// 7. 汇总结果
	var out DailyKImportResult
	out.Mode = mode
	out.Inserted = inserted
	row := dm.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM stock.daily_k_new")
	_ = row.Scan(&out.Downloaded)
	row = dm.db.QueryRowContext(ctx, "SELECT COUNT(DISTINCT symbol) FROM stock.daily_k_new")
	_ = row.Scan(&out.Symbols)
	if err := dm.db.QueryRowContext(ctx, "SELECT MIN(CAST(date AS VARCHAR)), MAX(CAST(date AS VARCHAR)) FROM stock.daily_k_new").Scan(&out.DateMin, &out.DateMax); err != nil {
		// 空批次时忽略日期汇总
	}
	out.Skipped = out.Downloaded - inserted
	if out.Skipped < 0 {
		out.Skipped = 0
	}
	out.Message = fmt.Sprintf("同花顺日K(%s) 下载 %d 行，新增入库 %d 行（覆盖 %d 只股票，区间 %s ~ %s，重复跳过 %d 行）",
		mode, out.Downloaded, out.Inserted, out.Symbols, out.DateMin, out.DateMax, out.Skipped)
	log.Printf("[THS] %s", out.Message)
	return &out, nil
}

// THSDailyKStatus 日K同步数据状态摘要（供前端展示：最新日期/行数/覆盖股票数/是否配置）。
func (dm *DuckDBManager) THSDailyKStatus() map[string]interface{} {
	out := map[string]interface{}{
		"configured": thssdk.Enabled(),
		"has_data":   false,
		"rows":       int64(0),
		"symbols":    int64(0),
	}
	if dm == nil || dm.db == nil {
		return out
	}
	ctx := context.Background()
	var n int64
	if err := dm.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM stock.stock_daily").Scan(&n); err == nil {
		out["rows"] = n
		out["has_data"] = n > 0
	}
	if err := dm.db.QueryRowContext(ctx, "SELECT COUNT(DISTINCT symbol) FROM stock.stock_daily").Scan(&n); err == nil {
		out["symbols"] = n
	}
	var latestSQLNull string
	if err := dm.db.QueryRowContext(ctx, "SELECT MAX(CAST(date AS VARCHAR)) FROM stock.stock_daily").Scan(&latestSQLNull); err == nil {
		out["latest_date"] = latestSQLNull
	}
	return out
}
