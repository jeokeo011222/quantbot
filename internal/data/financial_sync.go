package data

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// ==================== 股票财务数据维护（东财 datacenter → stock.duckdb） ====================
// 财务数据按「报告期」(report_date) 存储，并记录披露日(ann_date)，供回测按时间点取数，
// 保证历史回测只用「当时已披露」的数据，杜绝未来函数。数据全部来自东方财富真实财务摘要，严禁伪造。

const (
	// tableFinReport 财务报告表名（存于 stock.duckdb 命名空间 stock.financial_report）
	tableFinReport = "stock.financial_report"
	// urlEMFinancial 东方财富 datacenter 财务摘要接口
	urlEMFinancial = "https://datacenter-web.eastmoney.com/api/data/v1/get"
)

// FinancialReport 单期财务报告（报告期维度）
type FinancialReport struct {
	Symbol       string  // 规范代码（小写前缀+6位数字，如 sh600519）
	ReportDate   string  // 报告期 YYYY-MM-DD
	AnnDate      string  // 披露日 YYYY-MM-DD
	TotalRevenue float64 // 营业总收入（元）
	RevenueYOY   float64 // 营业总收入同比(%)
	NetProfit    float64 // 归母净利润（元）
	ProfitYOY    float64 // 归母净利润同比(%)
	NetProfitDed float64 // 扣非归母净利润（元）
	Roe          float64 // 摊薄净资产收益率(%)
	GrossMargin  float64 // 毛利率(%)
	NetMargin    float64 // 净利率(%)
	TotalAssets  float64 // 总资产（元）
	TotalLiab    float64 // 总负债（元）
	DebtRatio    float64 // 资产负债率(%)
	OperCashflow float64 // 经营活动现金流量净额（元）
	TotalShares  float64 // 总股本（股）
	FloatShares  float64 // 流通股本（股）
	EPS          float64 // 基本每股收益（元）
	BPS          float64 // 每股净资产（元）
}

// FinancialSyncJob 财务数据同步任务状态
type FinancialSyncJob struct {
	mu              sync.Mutex
	Running         bool   `json:"running"`
	Done            bool   `json:"done"`
	Error           string `json:"error"`
	TotalStocks     int    `json:"total_stocks"`
	ProcessedStocks int    `json:"processed_stocks"`
	TotalRecords    int64  `json:"total_records"`
	InsertedRecords int64  `json:"inserted_records"`
	FailedCount     int64  `json:"failed_count"`
	Message         string `json:"message"`
	LastResult      string `json:"last_result"`
	LastUpdate      string `json:"last_update"`
	Mode            string `json:"mode"` // full / incremental
}

// NewFinancialSyncJob 创建财务同步任务
func NewFinancialSyncJob() *FinancialSyncJob {
	return &FinancialSyncJob{}
}

func (j *FinancialSyncJob) startJob() {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.Running = true
	j.Done = false
	j.Error = ""
	j.TotalStocks = 0
	j.ProcessedStocks = 0
	j.TotalRecords = 0
	j.InsertedRecords = 0
	j.FailedCount = 0
	j.Message = "准备中..."
	j.LastResult = ""
	j.LastUpdate = time.Now().Format("2006-01-02 15:04:05")
}

func (j *FinancialSyncJob) setMessage(msg string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.Message = msg
}

func (j *FinancialSyncJob) setProgress(processed int, inserted int64, failed int64) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.ProcessedStocks = processed
	j.InsertedRecords = inserted
	j.FailedCount = failed
}

func (j *FinancialSyncJob) finishJob(err error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.Running = false
	j.Done = true
	j.LastUpdate = time.Now().Format("2006-01-02 15:04:05")
	if err != nil {
		j.Error = err.Error()
		j.Message = "同步失败: " + err.Error()
	} else {
		j.Message = "同步完成"
	}
}

// Snapshot 获取任务状态快照
func (j *FinancialSyncJob) Snapshot() map[string]interface{} {
	j.mu.Lock()
	defer j.mu.Unlock()
	return map[string]interface{}{
		"running":          j.Running,
		"done":             j.Done,
		"error":            j.Error,
		"total_stocks":     j.TotalStocks,
		"processed_stocks": j.ProcessedStocks,
		"total_records":    j.TotalRecords,
		"inserted_records": j.InsertedRecords,
		"failed_count":     j.FailedCount,
		"message":          j.Message,
		"last_result":      j.LastResult,
		"last_update":      j.LastUpdate,
		"mode":             j.Mode,
	}
}

func (dm *DuckDBManager) financialSyncRunning() bool {
	if dm.finSync == nil {
		return false
	}
	dm.finSync.mu.Lock()
	defer dm.finSync.mu.Unlock()
	return dm.finSync.Running
}

// ensureFinancialTable 创建财务报告表（幂等，列已包含全部 QI/VAL 所需指标；自愈恢复丢失的主键）
func (dm *DuckDBManager) ensureFinancialTable() error {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	if _, err := dm.db.Exec(`
		CREATE TABLE IF NOT EXISTS stock.financial_report (
			symbol         VARCHAR NOT NULL,
			report_date    DATE    NOT NULL,
			ann_date       DATE,
			total_revenue  DOUBLE,
			revenue_yoy    DOUBLE,
			net_profit     DOUBLE,
			profit_yoy     DOUBLE,
			net_profit_ded DOUBLE,
			roe            DOUBLE,
			gross_margin   DOUBLE,
			net_margin     DOUBLE,
			total_assets   DOUBLE,
			total_liab     DOUBLE,
			debt_ratio     DOUBLE,
			oper_cashflow  DOUBLE,
			total_shares   DOUBLE,
			float_shares   DOUBLE,
			eps            DOUBLE,
			bps            DOUBLE,
			update_time    TIMESTAMP,
			PRIMARY KEY (symbol, report_date)
		)`); err != nil {
		return fmt.Errorf("创建财务报告表失败: %w", err)
	}
	// 数据库经压缩重建可能丢失 (symbol, report_date) 主键约束，
	// 导致下方依赖 ON CONFLICT 的批量 upsert 报 Binder Error，自愈重建恢复。
	if err := dm.repairTablePrimaryKey("financial_report",
		`CREATE TABLE stock.financial_report_tmp (
			symbol         VARCHAR NOT NULL,
			report_date    DATE    NOT NULL,
			ann_date       DATE,
			total_revenue  DOUBLE,
			revenue_yoy    DOUBLE,
			net_profit     DOUBLE,
			profit_yoy     DOUBLE,
			net_profit_ded DOUBLE,
			roe            DOUBLE,
			gross_margin   DOUBLE,
			net_margin     DOUBLE,
			total_assets   DOUBLE,
			total_liab     DOUBLE,
			debt_ratio     DOUBLE,
			oper_cashflow  DOUBLE,
			total_shares   DOUBLE,
			float_shares   DOUBLE,
			eps            DOUBLE,
			bps            DOUBLE,
			update_time    TIMESTAMP,
			PRIMARY KEY (symbol, report_date)
		)`,
		`symbol, report_date, ann_date, total_revenue, revenue_yoy, net_profit, profit_yoy,
		 net_profit_ded, roe, gross_margin, net_margin, total_assets, total_liab, debt_ratio,
		 oper_cashflow, total_shares, float_shares, eps, bps, update_time`); err != nil {
		return err
	}
	// 索引：按 (symbol, report_date, ann_date) 加速「按披露日对齐」查询
	_, err := dm.db.Exec("CREATE INDEX IF NOT EXISTS idx_financial_symbol_date ON stock.financial_report(symbol, report_date, ann_date)")
	if err != nil {
		return fmt.Errorf("创建财务索引失败: %w", err)
	}
	return nil
}

// financialSymbolSet 获取需要同步财务数据的股票池（全部可交易A股）
func (dm *DuckDBManager) financialSymbolSet(ctx context.Context) ([]string, error) {
	rows, err := dm.db.QueryContext(ctx, `
		SELECT DISTINCT symbol FROM stock.stock_basic
		WHERE ((symbol LIKE 'sh6%') OR (symbol LIKE 'sz00%') OR (symbol LIKE 'sz30%') OR (symbol LIKE 'bj%'))
		ORDER BY symbol`)
	if err != nil {
		return nil, fmt.Errorf("查询股票池失败: %w", err)
	}
	defer rows.Close()

	var symbols []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			continue
		}
		if !IsTradableStock(s) {
			continue
		}
		symbols = append(symbols, s)
	}
	sort.Strings(symbols)
	return symbols, nil
}

// emFinResp 东财财务摘要接口响应结构（按 datacenter API 通用字段封装）
type emFinResp struct {
	Result *emFinPage `json:"result"`
}
type emFinPage struct {
	Pages int        `json:"pages"`
	Data  []emFinRow `json:"data"`
}
type emFinRow struct {
	REPORT_DATE        string  `json:"REPORT_DATE"`
	NOTICE_DATE        string  `json:"NOTICE_DATE"`
	TOTALOPERATEREVE   float64 `json:"TOTALOPERATEREVE"`   // 营业总收入（元）
	TOTALOPERATEREVETZ float64 `json:"TOTALOPERATEREVETZ"` // 营业总收入同比(%)
	PARENTNETPROFIT    float64 `json:"PARENTNETPROFIT"`    // 归母净利润（元）
	PARENTNETPROFITTZ  float64 `json:"PARENTNETPROFITTZ"`  // 归母净利润同比(%)
	KCFJCXSYJLR        float64 `json:"KCFJCXSYJLR"`        // 扣非归母净利润（元）
	ROEJQ              float64 `json:"ROEJQ"`              // 加权净资产收益率(%)
	XSMLL              float64 `json:"XSMLL"`              // 销售毛利率(%)
	XSJLL              float64 `json:"XSJLL"`              // 销售净利率(%)
	TOTAL_ASSETS_PK    float64 `json:"TOTAL_ASSETS_PK"`    // 总资产（元）
	LIABILITY          float64 `json:"LIABILITY"`          // 总负债（元）
	ZCFZL              float64 `json:"ZCFZL"`              // 资产负债率(%)
	NETCASH_OPERATE_PK float64 `json:"NETCASH_OPERATE_PK"` // 经营现金流净额（元）
	TOTAL_SHARE        float64 `json:"TOTAL_SHARE"`        // 总股本（股）
	A_FREE_SHARE       float64 `json:"A_FREE_SHARE"`       // 流通A股（股）
	EPSJB              float64 `json:"EPSJB"`              // 基本每股收益（元）
	BPS                float64 `json:"BPS"`                // 每股净资产（元）
}

// emToFin 把东财行 + 本股代码转换为内部 FinancialReport
func emToFin(symbol string, row emFinRow) FinancialReport {
	return FinancialReport{
		Symbol:       symbol,
		ReportDate:   first10(row.REPORT_DATE),
		AnnDate:      first10(row.NOTICE_DATE),
		TotalRevenue: row.TOTALOPERATEREVE,
		RevenueYOY:   row.TOTALOPERATEREVETZ,
		NetProfit:    row.PARENTNETPROFIT,
		ProfitYOY:    row.PARENTNETPROFITTZ,
		NetProfitDed: row.KCFJCXSYJLR,
		Roe:          row.ROEJQ,
		GrossMargin:  row.XSMLL,
		NetMargin:    row.XSJLL,
		TotalAssets:  row.TOTAL_ASSETS_PK,
		TotalLiab:    row.LIABILITY,
		DebtRatio:    row.ZCFZL,
		OperCashflow: row.NETCASH_OPERATE_PK,
		TotalShares:  row.TOTAL_SHARE,
		FloatShares:  row.A_FREE_SHARE,
		EPS:          row.EPSJB,
		BPS:          row.BPS,
	}
}

// first10 取日期字符串前10位（"2026-06-30 00:00:00" → "2026-06-30"）
func first10(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 10 {
		return s[:10]
	}
	return s
}

// symbolToSecucode 把规范代码转成东财 secucode（如 sh600519→600519.SH, bj8xxxxx→8xxxxx.BJ）
func symbolToSecucode(symbol string) string {
	pure := PureCodeFromCode(symbol)
	switch DetectMarketFromCode(symbol) {
	case "sz":
		return pure + ".SZ"
	case "bj":
		return pure + ".BJ"
	default:
		return pure + ".SH"
	}
}

// fetchOneFinancial 拉取单只股票财务报告：优先使用注入的拉取器（如通达信终端），
// 否则走东财 datacenter。保证 StartFinancialSync 主循环不感知数据源差异。
func (dm *DuckDBManager) fetchOneFinancial(ctx context.Context, symbol, startReport string) ([]FinancialReport, error) {
	dm.mu.RLock()
	f := dm.finFetcher
	dm.mu.RUnlock()
	if f != nil {
		return f(ctx, symbol, startReport)
	}
	return dm.fetchFinancialOne(ctx, symbol, startReport)
}

// fetchFinancialOne 拉取单只股票的全部财务报告（分页合并），起始报告期可选（增量用）。
func (dm *DuckDBManager) fetchFinancialOne(ctx context.Context, symbol string, startReport string) ([]FinancialReport, error) {
	secu := symbolToSecucode(symbol)
	var all []FinancialReport
	filter := fmt.Sprintf(`(SECUCODE="%s")`, secu)
	if startReport != "" {
		// 增量：只拉起始报告期之后（含）的数据
		filter = fmt.Sprintf(`(SECUCODE="%s")(REPORT_DATE>='%s')`, secu, startReport)
	}
	baseParams := []string{
		"reportName=RPT_F10_FINANCE_MAINFINADATA",
		"columns=ALL",
		"filter=" + url.QueryEscape(filter),
		"sortTypes=-1",
		"sortColumns=REPORT_DATE",
		"pageSize=100",
		"source=DataCenter",
		"client=WAP",
	}
	page := 1
	for page > 0 {
		params := append(append([]string{}, baseParams...), fmt.Sprintf("pageNumber=%d", page))
		url := urlEMFinancial + "?" + strings.Join(params, "&")
		body, err := emHTTPGet(ctx, url)
		if err != nil {
			return nil, err
		}
		var resp emFinResp
		if err := json.Unmarshal(body, &resp); err != nil {
			return nil, fmt.Errorf("东财财务响应解析失败: %w", err)
		}
		if resp.Result == nil || len(resp.Result.Data) == 0 {
			break
		}
		for _, r := range resp.Result.Data {
			// 丢弃空报告期，避免脏数据
			if r.REPORT_DATE == "" {
				continue
			}
			all = append(all, emToFin(symbol, r))
		}
		if page >= resp.Result.Pages {
			break
		}
		page++
	}
	sort.Slice(all, func(i, j int) bool {
		return all[i].ReportDate < all[j].ReportDate
	})
	return all, nil
}

// mergeFinancialBatch 把一批财务报告 upsert 进 stock.financial_report（事务提交，重复静默跳过）
func (dm *DuckDBManager) mergeFinancialBatch(ctx context.Context, reports []FinancialReport) (int64, error) {
	if len(reports) == 0 {
		return 0, nil
	}
	tx, err := dm.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO stock.financial_report (
			symbol, report_date, ann_date, total_revenue, revenue_yoy, net_profit,
			profit_yoy, net_profit_ded, roe, gross_margin, net_margin, total_assets,
			total_liab, debt_ratio, oper_cashflow, total_shares, float_shares, eps, bps, update_time
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT (symbol, report_date) DO UPDATE SET
			ann_date=excluded.ann_date, total_revenue=excluded.total_revenue,
			revenue_yoy=excluded.revenue_yoy, net_profit=excluded.net_profit,
			profit_yoy=excluded.profit_yoy, net_profit_ded=excluded.net_profit_ded,
			roe=excluded.roe, gross_margin=excluded.gross_margin, net_margin=excluded.net_margin,
			total_assets=excluded.total_assets, total_liab=excluded.total_liab,
			debt_ratio=excluded.debt_ratio, oper_cashflow=excluded.oper_cashflow,
			total_shares=excluded.total_shares, float_shares=excluded.float_shares,
			eps=excluded.eps, bps=excluded.bps, update_time=excluded.update_time`,
	)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	now := time.Now()
	var inserted int64
	for _, r := range reports {
		res, err := stmt.ExecContext(ctx,
			r.Symbol, r.ReportDate, r.AnnDate,
			r.TotalRevenue, r.RevenueYOY, r.NetProfit,
			r.ProfitYOY, r.NetProfitDed, r.Roe, r.GrossMargin, r.NetMargin,
			r.TotalAssets, r.TotalLiab, r.DebtRatio, r.OperCashflow,
			r.TotalShares, r.FloatShares, r.EPS, r.BPS, now,
		)
		if err != nil {
			return 0, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			inserted++
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return inserted, nil
}

// StartFinancialSync 启动财务数据同步（异步）
// mode: "full" 全量拉全历史；"incremental" 只拉表内最大报告期之后（默认）
func (dm *DuckDBManager) StartFinancialSync(ctx context.Context, mode string) error {
	if dm == nil || dm.db == nil {
		return fmt.Errorf("DuckDB 未初始化")
	}
	if dm.financialSyncRunning() {
		return fmt.Errorf("财务数据同步正在进行中")
	}
	if mode != "full" {
		mode = "incremental"
	}
	if dm.finSync == nil {
		dm.finSync = NewFinancialSyncJob()
	}
	job := dm.finSync
	job.startJob()
	job.Mode = mode

	go func() {
		// 财务全市场同步为后台长任务，不设「总时长」硬上限：
		// 逐请求超时已经生效（东财 emHTTPGet 15s / thssdk http.Client 15s + 指数退避），
		// 足以避免单个请求挂死。若再叠加全局 WithTimeout(30min)，数千只的大池
		// 容易在 thssdk 限流（QPS 令牌桶+分页）下超时中断，只跑一部分便报
		// context deadline exceeded，剩余股票被跳过。故直接沿用调用方 ctx（可随
		// 应用退出取消），直到全部符号处理完毕或上游 ctx 被取消。
		syncCtx := ctx

		if err := dm.ensureFinancialTable(); err != nil {
			job.finishJob(err)
			return
		}

		symbols, err := dm.financialSymbolSet(syncCtx)
		if err != nil {
			job.finishJob(err)
			return
		}
		job.mu.Lock()
		job.TotalStocks = len(symbols)
		job.mu.Unlock()
		job.setMessage(fmt.Sprintf("共 %d 只股票，开始拉取财务数据(%s)...", len(symbols), job.Mode))

		var inserted, failed int64
		for i, sym := range symbols {
			if syncCtx.Err() != nil {
				job.finishJob(syncCtx.Err())
				return
			}
			startReport := ""
			if job.Mode == "incremental" {
				startReport = dm.maxFinReportDate(sym)
			}
			reports, err := dm.fetchOneFinancial(syncCtx, sym, startReport)
			if err != nil {
				failed++
				log.Printf("[FinancialSync] %s 拉取失败: %v", sym, err)
				continue
			}
			if len(reports) > 0 {
				n, err := dm.mergeFinancialBatch(syncCtx, reports)
				if err != nil {
					failed++
					log.Printf("[FinancialSync] %s 写入失败: %v", sym, err)
					continue
				}
				inserted += n
			}
			job.mu.Lock()
			job.ProcessedStocks = i + 1
			job.InsertedRecords = inserted
			job.FailedCount = failed
			job.mu.Unlock()
			job.setMessage(fmt.Sprintf("已处理 %d/%d 只股票，写入 %d 条，失败 %d", i+1, len(symbols), inserted, failed))
			// 限速：仅对东财默认源做 150ms 间隔防频控；自研拉取器（通达信终端 / 同花顺官方）各自内部限流
			//（同花顺官方走 thssdk 并发令牌桶），不再叠加，避免财务全市场同步过慢。
			dm.mu.RLock()
			customFetcher := dm.finFetcher != nil
			dm.mu.RUnlock()
			if !customFetcher {
				select {
				case <-syncCtx.Done():
				case <-time.After(150 * time.Millisecond):
				}
			}
		}

		job.mu.Lock()
		job.LastResult = fmt.Sprintf("共 %d 只股票，写入 %d 条财务记录，失败 %d", len(symbols), inserted, failed)
		job.mu.Unlock()
		job.finishJob(nil)
		log.Printf("[FinancialSync] 完成: %s", job.LastResult)
	}()
	return nil
}

// GetFinancialSyncStatus 获取财务同步状态
func (dm *DuckDBManager) GetFinancialSyncStatus() map[string]interface{} {
	if dm.finSync == nil {
		dm.finSync = NewFinancialSyncJob()
	}
	return dm.finSync.Snapshot()
}

// sqlNullString 兼容 DuckDB 可空字符串/DATE 的扫描容器
type sqlNullString struct {
	V     string
	Valid bool
}

// Scan 实现 sql.Scanner（兼容 *string 扫描，空值标记 Valid=false）
func (n *sqlNullString) Scan(src interface{}) error {
	if src == nil {
		n.V = ""
		n.Valid = false
		return nil
	}
	switch v := src.(type) {
	case string:
		n.V = v
	case []byte:
		n.V = string(v)
	default:
		n.V = fmt.Sprintf("%v", v)
	}
	n.Valid = n.V != ""
	return nil
}

// maxFinReportDate 获取某股票在表中最大的报告期（增量起点）
func (dm *DuckDBManager) maxFinReportDate(symbol string) string {
	var d sqlNullString
	err := dm.db.QueryRowContext(context.Background(),
		"SELECT MAX(report_date) FROM stock.financial_report WHERE symbol=?", symbol).Scan(&d)
	_ = err
	if !d.Valid || d.V == "" {
		return ""
	}
	return d.V
}

// GetFinancialAsOf 无未来函数取数：返回 symbol 在 asOfDate 时点已披露的最新财务报告。
// 条件 report_date <= asOfDate 且 ann_date <= asOfDate，取 report_date 最大的一条。
// 返回 nil 表示无可用数据（严禁伪造）。
func (dm *DuckDBManager) GetFinancialAsOf(symbol string, asOfDate string) (*FinancialReport, error) {
	// stock.financial_report 统一以小写市场前缀规范代码存储（sh600721/sz002437/bj920000）。
	// 调用方（样本池/持仓）可能只传纯代码（600721），这里统一归一化，否则查不到导致基本面无信号。
	symbol = MarketCode(symbol)
	q := `
		SELECT symbol, report_date, ann_date, total_revenue, revenue_yoy, net_profit,
			profit_yoy, net_profit_ded, roe, gross_margin, net_margin, total_assets,
			total_liab, debt_ratio, oper_cashflow, total_shares, float_shares, eps, bps
		FROM stock.financial_report
		WHERE symbol=? AND report_date<=? AND (ann_date<=? OR ann_date IS NULL)
		ORDER BY report_date DESC LIMIT 1`
	var r FinancialReport
	var rd, ad sqlNullString
	err := dm.db.QueryRowContext(context.Background(), q, symbol, asOfDate, asOfDate).Scan(
		&r.Symbol, &rd, &ad, &r.TotalRevenue, &r.RevenueYOY, &r.NetProfit,
		&r.ProfitYOY, &r.NetProfitDed, &r.Roe, &r.GrossMargin, &r.NetMargin,
		&r.TotalAssets, &r.TotalLiab, &r.DebtRatio, &r.OperCashflow,
		&r.TotalShares, &r.FloatShares, &r.EPS, &r.BPS)
	if err != nil {
		if strings.Contains(err.Error(), "no rows") {
			return nil, nil
		}
		return nil, err
	}
	r.ReportDate = rd.V
	r.AnnDate = ad.V
	return &r, nil
}

// emHTTPGet 东财接口 GET（带 UserAgent/Referer、一次重试）
func emHTTPGet(ctx context.Context, url string) ([]byte, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(500 * time.Millisecond):
			}
		}
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) QuantBot/1.0")
		req.Header.Set("Referer", "https://data.eastmoney.com/")
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("东财接口返回状态 %d", resp.StatusCode)
			continue
		}
		return body, nil
	}
	return nil, lastErr
}
