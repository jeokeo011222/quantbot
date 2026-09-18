package data

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"time"

	"github.com/quantpilot/quantpilot/internal/thssdk"
)

// ==================== 同花顺官方复权因子导入 + 前复权视图 ====================
//
// 数据来源：同花顺官方全市场复权事件 Parquet（adjustment-factors dump），
// 包含现金分红/送股/配股事件，与本机 stock.ohlc 真实日K 合并推算日频复权因子
// （算法与官方 marketdb 一致），构建 stock.adj_factor 表与 stock.ohlc_qfq 前复权视图。
// 全部为官方真实事件 + 本机真实行情，严禁伪造。
//
// 算法（官方 marketdb calculations/adjustment.py 移植）：
//   每只股票每个除权事件在「有效交易日」(除权日后首个交易日)生效，
//   ratio = prev_close*(1 + 送股比例 + 配股比例) / (prev_close - 每股红利 + 配股价*配股比例)
//   backward_factor[t] = 累计连乘到 t 的 ratio（无事件日为 1）
//   forward_factor[t]  = backward_factor[t] / backward_factor[最新日]   （前复权，最新日=1）
// 效果：qfq_close[t] = raw_close[t] * forward_factor[t]。

// AdjFactorRow 日频复权因子行。
type AdjFactorRow struct {
	Symbol         string
	Date           string
	BackwardFactor float64
	ForwardFactor  float64
}

// AdjFactorImportResult 复权因子导入结果摘要。
type AdjFactorImportResult struct {
	EventCount       int64  `json:"event_count"`        // 官方复权事件条数
	FactorRows       int64  `json:"factor_rows"`        // 推算出的日频因子行数
	FactorSymbols    int64  `json:"factor_symbols"`     // 覆盖股票数
	WithEventSymbols int64  `json:"with_event_symbols"` // 至少含一个除权事件的股票数
	ViewCreated      bool   `json:"view_created"`       // 前复权视图是否已创建
	Message          string `json:"message"`
}

// ensureAdjFactorTables 确保复权因子相关表存在（幂等；不存在时建空表以便视图引用）。
func (dm *DuckDBManager) ensureAdjFactorTables() error {
	_, err := dm.db.ExecContext(context.Background(), `
		CREATE TABLE IF NOT EXISTS stock.adj_event (
			symbol VARCHAR NOT NULL,
			ex_date DATE NOT NULL,
			d DOUBLE,  -- 每股现金红利(税前)
			s DOUBLE,  -- 每股送股比例
			r DOUBLE,  -- 配股比例
			p DOUBLE,  -- 配股价
			PRIMARY KEY (symbol, ex_date)
		)`)
	if err != nil {
		return fmt.Errorf("创建复权事件表失败: %w", err)
	}
	_, err = dm.db.ExecContext(context.Background(), `
		CREATE TABLE IF NOT EXISTS stock.adj_factor (
			symbol VARCHAR NOT NULL,
			date DATE NOT NULL,
			backward_factor DOUBLE,
			forward_factor DOUBLE,
			PRIMARY KEY (symbol, date)
		)`)
	if err != nil {
		return fmt.Errorf("创建复权因子表失败: %w", err)
	}
	_, err = dm.db.ExecContext(context.Background(), `
		CREATE INDEX IF NOT EXISTS idx_adj_factor_symbol_date ON stock.adj_factor(symbol, date)`)
	if err != nil {
		return fmt.Errorf("创建复权因子索引失败: %w", err)
	}
	return nil
}

// ensureAdjView 创建/刷新 stock.ohlc_qfq 前复权视图（基于 stock.ohlc × stock.adj_factor）。
// 无因子的行（如未覆盖股票）按原价返回（因子=1），避免视图出现空洞。
func (dm *DuckDBManager) ensureAdjView() error {
	_, err := dm.db.ExecContext(context.Background(), `
		CREATE OR REPLACE VIEW stock.ohlc_qfq AS
		SELECT
			o.symbol,
			o.date,
			o.open    * COALESCE(af.forward_factor, 1.0) AS open,
			o.high    * COALESCE(af.forward_factor, 1.0) AS high,
			o.low     * COALESCE(af.forward_factor, 1.0) AS low,
			o.close   * COALESCE(af.forward_factor, 1.0) AS close,
			o.volume,
			o.amount
		FROM stock.ohlc o
		LEFT JOIN stock.adj_factor af ON af.symbol = o.symbol AND af.date = o.date`)
	if err != nil {
		return fmt.Errorf("创建前复权视图失败: %w", err)
	}
	return nil
}

// ImportTHSAdjFactors 从同花顺官方下载全市场复权事件 Parquet，推算日频复权因子
// 并重建 stock.adj_event / stock.adj_factor 表与 stock.ohlc_qfq 前复权视图。
// 未配置官方 API Key 时直接返回错误（严禁伪造/跳过）。
func (dm *DuckDBManager) ImportTHSAdjFactors(ctx context.Context) (*AdjFactorImportResult, error) {
	c := thssdk.Default()
	if c == nil {
		return nil, fmt.Errorf("同花顺官方数据源未配置（请在设置-数据源中启用并填写 API Key）")
	}
	if dm == nil || dm.db == nil {
		return nil, fmt.Errorf("DuckDB 未初始化")
	}

	// 1. 获取预签名下载地址并下载复权事件 Parquet（有效期约5分钟，须立即下载）
	dlURL, err := c.DumpDownloadURL(ctx, thssdk.DumpAdjFactors)
	if err != nil {
		return nil, fmt.Errorf("获取同花顺复权因子导出地址失败: %w", err)
	}
	pqPath := filepath.Join(dm.getDataDir(), "ths_adj_factors.parquet")
	t0 := time.Now()
	if err := c.Download(ctx, dlURL, pqPath); err != nil {
		return nil, fmt.Errorf("下载同花顺复权因子 Parquet 失败: %w", err)
	}
	log.Printf("[THS] 复权因子 Parquet 下载完成(%s, %.1fs)", pqPath, time.Since(t0).Seconds())

	dm.mu.Lock()
	defer dm.mu.Unlock()
	if err := dm.ensureAdjFactorTables(); err != nil {
		return nil, err
	}
	// Parquet 文件路径反斜杠转正斜杠（DuckDB read_parquet 路径要求）
	pq := filepath.ToSlash(pqPath)

	// 2. 全量重建复权事件表（thscode 600519.SH → symbol sh600519）。
	//   同一股票同一天可能出现多条事件（分红+送配同日叠加），按 (symbol, ex_date) 去重：
	//   d/s/r 求和（总量守恒），p 按 r 加权平均保持配股等价。默认仅保留近3年事件（与日K窗口一致）。
	if _, err := dm.db.ExecContext(ctx, "DELETE FROM stock.adj_event"); err != nil {
		return nil, fmt.Errorf("清空复权事件表失败: %w", err)
	}
	_, err = dm.db.ExecContext(ctx, `
		INSERT INTO stock.adj_event (symbol, ex_date, d, s, r, p)
		SELECT symbol, ex_date,
			SUM(d) AS d,
			SUM(s) AS s,
			SUM(r) AS r,
			CASE WHEN SUM(r) > 0 THEN SUM(r * p) / SUM(r) ELSE 0 END AS p
		FROM (
			SELECT
				lower(split_part(thscode, '.', 2)) || split_part(thscode, '.', 1) AS symbol,
				CAST(epoch_ms(ex_date_ms) AS DATE) AS ex_date,
				COALESCE(dividend_per_share, 0.0) AS d,
				COALESCE(per_share_bonus, 0.0) AS s,
				COALESCE(allotment_ratio, 0.0) AS r,
				COALESCE(allotment_price, 0.0) AS p
			FROM read_parquet(?)
		)
		WHERE ex_date >= CAST(now() - INTERVAL '3 years' AS DATE)
		GROUP BY symbol, ex_date`,
		pq)
	if err != nil {
		return nil, fmt.Errorf("导入复权事件失败: %w", err)
	}

	// 3. 推算日频复权因子（官方算法：ratio → 累计连乘 backward → 归一化 forward）
	if _, err := dm.db.ExecContext(ctx, "DELETE FROM stock.adj_factor"); err != nil {
		return nil, fmt.Errorf("清空复权因子表失败: %w", err)
	}
	_, err = dm.db.ExecContext(ctx, `
		INSERT INTO stock.adj_factor (symbol, date, backward_factor, forward_factor)
		WITH eff AS (
			SELECT e.symbol, e.ex_date, e.d, e.s, e.r, e.p,
				(SELECT MIN(k.date) FROM stock.ohlc k WHERE k.symbol = e.symbol AND k.date >= e.ex_date) AS eff_date
			FROM stock.adj_event e
		),
		kp AS (
			SELECT symbol, date, close,
				LAG(close) OVER (PARTITION BY symbol ORDER BY date) AS prev_close
			FROM stock.ohlc
		),
		ratios AS (
			SELECT eff.symbol, eff.eff_date AS date,
				(kp.prev_close * (1.0 + eff.s + eff.r))
					/ NULLIF(kp.prev_close - eff.d + eff.r * eff.p, 0) AS ratio
			FROM eff
			JOIN kp ON kp.symbol = eff.symbol AND kp.date = eff.eff_date
			WHERE eff.eff_date IS NOT NULL AND kp.prev_close IS NOT NULL
		),
		day_ratio AS (
			SELECT symbol, date, EXP(SUM(LN(ratio))) AS ratio
			FROM ratios
			WHERE ratio IS NOT NULL AND ratio > 0
			GROUP BY symbol, date
		),
		backward AS (
			SELECT k.symbol, k.date,
				EXP(SUM(LN(COALESCE(dr.ratio, 1.0))) OVER (
					PARTITION BY k.symbol ORDER BY k.date
					ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW
				)) AS backward_factor
			FROM stock.ohlc k
			LEFT JOIN day_ratio dr ON dr.symbol = k.symbol AND dr.date = k.date
		),
		norm AS (
			SELECT symbol, date, backward_factor,
				LAST_VALUE(backward_factor) OVER (
					PARTITION BY symbol ORDER BY date
					ROWS BETWEEN UNBOUNDED PRECEDING AND UNBOUNDED FOLLOWING
				) AS last_backward
			FROM backward
		)
		SELECT symbol, date, backward_factor, backward_factor / NULLIF(last_backward, 0) AS forward_factor
		FROM norm`)
	if err != nil {
		return nil, fmt.Errorf("推算复权因子失败: %w", err)
	}

	// 4. 刷新前复权视图
	if err := dm.ensureAdjView(); err != nil {
		return nil, err
	}

	// 5. 汇总结果
	var res AdjFactorImportResult
	row := dm.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM stock.adj_event")
	_ = row.Scan(&res.EventCount)
	row = dm.db.QueryRowContext(ctx, "SELECT COUNT(*), COUNT(DISTINCT symbol) FROM stock.adj_factor")
	_ = row.Scan(&res.FactorRows, &res.FactorSymbols)
	row = dm.db.QueryRowContext(ctx, "SELECT COUNT(DISTINCT symbol) FROM stock.adj_event")
	_ = row.Scan(&res.WithEventSymbols)
	res.ViewCreated = true
	res.Message = fmt.Sprintf("导入 %d 条复权事件，推算 %d 行因子（覆盖 %d 只股票，其中 %d 只有除权事件），前复权视图 stock.ohlc_qfq 已就绪",
		res.EventCount, res.FactorRows, res.FactorSymbols, res.WithEventSymbols)
	log.Printf("[THS] %s", res.Message)
	return &res, nil
}

// AdjFactorStatus 复权因子数据状态摘要（供前端展示）。
func (dm *DuckDBManager) AdjFactorStatus() map[string]interface{} {
	out := map[string]interface{}{
		"configured":     thssdk.Enabled(),
		"has_event":      false,
		"event_rows":     int64(0),
		"factor_symbols": int64(0),
		"view_ready":     false,
	}
	if dm == nil || dm.db == nil {
		return out
	}
	ctx := context.Background()
	var n int64
	if err := dm.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM stock.adj_event").Scan(&n); err == nil {
		out["event_rows"] = n
		out["has_event"] = n > 0
	}
	if err := dm.db.QueryRowContext(ctx, "SELECT COUNT(DISTINCT symbol) FROM stock.adj_factor").Scan(&n); err == nil {
		out["factor_symbols"] = n
	}
	var v int
	if err := dm.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.views WHERE table_name='ohlc_qfq' AND table_schema='stock'").Scan(&v); err == nil {
		out["view_ready"] = v > 0
	}
	return out
}
