package data

import (
	"context"
	"encoding/binary"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
)

// ==================== 股票时序数据维护（TDX .day → stock.duckdb） ====================

// StockSyncJob TDX 数据同步任务状态
type StockSyncJob struct {
	mu             sync.Mutex
	Running        bool      `json:"running"`
	Done           bool      `json:"done"`
	Error          string    `json:"error"`
	TotalFiles     int       `json:"total_files"`
	ProcessedFiles int       `json:"processed_files"`
	TotalRows      int64     `json:"total_rows"`
	InsertedRows   int64     `json:"inserted_rows"`
	Message        string    `json:"message"`
	StartTime      time.Time `json:"start_time"`
	EndTime        time.Time `json:"end_time"`
}

// NewStockSyncJob 创建同步任务
func NewStockSyncJob() *StockSyncJob {
	return &StockSyncJob{}
}

// startSync 标记任务开始
func (j *StockSyncJob) startSync(totalFiles int) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.Running = true
	j.Done = false
	j.Error = ""
	j.TotalFiles = totalFiles
	j.ProcessedFiles = 0
	j.TotalRows = 0
	j.InsertedRows = 0
	j.Message = "准备中..."
	j.StartTime = time.Now()
	j.EndTime = time.Time{}
}

// setMessage 更新状态消息
func (j *StockSyncJob) setMessage(msg string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.Message = msg
}

// setProgress 更新进度
func (j *StockSyncJob) setProgress(processed int, inserted int64) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.ProcessedFiles = processed
	j.InsertedRows = inserted
}

// finishSync 标记任务完成/失败
func (j *StockSyncJob) finishSync(err error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.Running = false
	j.Done = true
	j.EndTime = time.Now()
	if err != nil {
		j.Error = err.Error()
		j.Message = "同步失败: " + err.Error()
	} else {
		j.Message = "同步完成"
	}
}

// Snapshot 获取任务状态快照
func (j *StockSyncJob) Snapshot() map[string]interface{} {
	j.mu.Lock()
	defer j.mu.Unlock()
	duration := 0.0
	if !j.StartTime.IsZero() {
		if j.EndTime.IsZero() {
			duration = time.Since(j.StartTime).Seconds()
		} else {
			duration = j.EndTime.Sub(j.StartTime).Seconds()
		}
	}
	return map[string]interface{}{
		"running":         j.Running,
		"done":            j.Done,
		"error":           j.Error,
		"total_files":     j.TotalFiles,
		"processed_files": j.ProcessedFiles,
		"total_rows":      j.TotalRows,
		"inserted_rows":   j.InsertedRows,
		"message":         j.Message,
		"start_time":      j.StartTime.Format("2006-01-02 15:04:05"),
		"end_time":        j.EndTime.Format("2006-01-02 15:04:05"),
		"duration_sec":    duration,
	}
}

// tdxDayBar TDX 日线记录（解析用）
type tdxDayBar struct {
	Symbol string
	Date   string
	Open   float64
	High   float64
	Low    float64
	Close  float64
	Volume float64
	Amount float64
}

// tdxDayRecordSize TDX .day 单条记录字节数
const tdxDayRecordSize = 32

// parseTDXDayFile 解析单个通达信 .day 文件
// 记录格式(32字节，小端): 日期(uint32 YYYYMMDD)、开/高/低/收(uint32 分)、成交额(uint32 元)、成交量(uint32 股)、保留(4字节)
func parseTDXDayFile(path string, symbol string) ([]tdxDayBar, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var bars []tdxDayBar
	for offset := 0; offset+tdxDayRecordSize <= len(raw); offset += tdxDayRecordSize {
		rec := raw[offset : offset+tdxDayRecordSize]
		dateInt := int(binary.LittleEndian.Uint32(rec[0:4]))

		// 验证日期合理性 (1990-2035)
		if dateInt < 19900101 || dateInt > 20351231 {
			continue
		}
		day := fmt.Sprintf("%04d-%02d-%02d", dateInt/10000, (dateInt/100)%100, dateInt%100)

		open := float64(binary.LittleEndian.Uint32(rec[4:8])) / 100.0
		high := float64(binary.LittleEndian.Uint32(rec[8:12])) / 100.0
		low := float64(binary.LittleEndian.Uint32(rec[12:16])) / 100.0
		closeV := float64(binary.LittleEndian.Uint32(rec[16:20])) / 100.0
		amount := float64(binary.LittleEndian.Uint32(rec[20:24]))
		volume := float64(binary.LittleEndian.Uint32(rec[24:28]))

		// 跳过无效记录
		if closeV <= 0 && open <= 0 && high <= 0 && low <= 0 {
			continue
		}

		bars = append(bars, tdxDayBar{
			Symbol: symbol,
			Date:   day,
			Open:   open,
			High:   high,
			Low:    low,
			Close:  closeV,
			Volume: volume,
			Amount: amount,
		})
	}
	return bars, nil
}

// enumerateTDXDayFiles 枚举 TDX 目录下所有 .day 文件
// 目录结构: {tdxPath}/vipdoc/{bj|sh|sz}/lday/{market}{code}.day
func enumerateTDXDayFiles(tdxPath string) ([]string, error) {
	var files []string
	for _, market := range []string{"bj", "sh", "sz"} {
		folder := filepath.Join(tdxPath, "vipdoc", market, "lday")
		entries, err := os.ReadDir(folder)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() || !strings.HasSuffix(name, ".day") {
				continue
			}
			// 文件名形如 sh600519.day（市场前缀2位+代码6位+扩展名4位=12字符）
			if len(name) != 12 || !strings.HasPrefix(name, market) {
				continue
			}
			files = append(files, filepath.Join(folder, name))
		}
	}
	sort.Strings(files)
	return files, nil
}

// StartStockSync 启动 TDX 日线数据同步到 stock.duckdb（异步执行）
// tdxPath: 通达信安装目录，如 D:\tdx
func (dm *DuckDBManager) StartStockSync(tdxPath string) error {
	if dm == nil || dm.db == nil {
		return fmt.Errorf("DuckDB 未初始化")
	}
	if dm.stockSyncRunning() {
		return fmt.Errorf("已有同步任务正在运行，请稍候")
	}

	if dm.stockSync == nil {
		dm.stockSync = NewStockSyncJob()
	}

	go dm.runStockSync(tdxPath)
	return nil
}

// stockSyncRunning 判断是否正在同步
func (dm *DuckDBManager) stockSyncRunning() bool {
	if dm.stockSync == nil {
		return false
	}
	dm.stockSync.mu.Lock()
	defer dm.stockSync.mu.Unlock()
	return dm.stockSync.Running
}

// GetStockSyncStatus 获取同步任务状态
func (dm *DuckDBManager) GetStockSyncStatus() map[string]interface{} {
	if dm.stockSync == nil {
		return map[string]interface{}{
			"running": false, "done": false, "error": "", "total_files": 0,
			"processed_files": 0, "total_rows": 0, "inserted_rows": 0,
			"message": "尚未执行过同步", "start_time": "", "end_time": "", "duration_sec": 0,
		}
	}
	return dm.stockSync.Snapshot()
}

// runStockSync 执行同步（后台 goroutine）
func (dm *DuckDBManager) runStockSync(tdxPath string) {
	job := dm.stockSync
	job.startSync(0)
	job.setMessage("正在扫描 TDX 数据文件...")

	ctx := context.Background()

	// 1. 枚举所有 .day 文件
	files, err := enumerateTDXDayFiles(tdxPath)
	if err != nil {
		job.finishSync(err)
		return
	}
	if len(files) == 0 {
		job.finishSync(fmt.Errorf("未在 TDX 日线数据目录（%s\\vipdoc\\sh\\lday、sz\\lday、bj\\lday）下找到任何 .day 文件，请检查 TDX 路径", tdxPath))
		return
	}
	job.mu.Lock()
	job.TotalFiles = len(files)
	job.mu.Unlock()
	job.setMessage(fmt.Sprintf("共发现 %d 个 .day 文件，开始解析...", len(files)))

	// 2. 解析到临时 CSV 文件
	tmpDir, err := os.MkdirTemp("", "tdx-sync-")
	if err != nil {
		job.finishSync(fmt.Errorf("创建临时目录失败: %w", err))
		return
	}
	defer os.RemoveAll(tmpDir)

	csvPath := filepath.Join(tmpDir, "ohlc.csv")
	csvFile, err := os.Create(csvPath)
	if err != nil {
		job.finishSync(fmt.Errorf("创建临时CSV失败: %w", err))
		return
	}
	writer := newCSVWriter(csvFile)

	var totalRows int64
	parsed := 0
	for _, path := range files {
		// 从文件名提取 symbol，如 sh600519.day -> sh600519
		base := filepath.Base(path)
		symbol := strings.TrimSuffix(base, ".day")
		bars, err := parseTDXDayFile(path, symbol)
		if err != nil {
			job.setMessage(fmt.Sprintf("跳过文件 %s: %v", base, err))
			continue
		}
		for _, b := range bars {
			writer.write([]string{
				b.Symbol, b.Date,
				trimFloat(b.Open), trimFloat(b.High), trimFloat(b.Low), trimFloat(b.Close),
				trimFloat(b.Volume), trimFloat(b.Amount),
			})
			totalRows++
		}
		parsed++
		job.setProgress(parsed, totalRows)
		if parsed%250 == 0 || parsed == len(files) {
			job.setMessage(fmt.Sprintf("已解析 %d/%d 个文件，%d 行", parsed, len(files), totalRows))
			log.Printf("[StockSync] parsed %d/%d files, %d rows", parsed, len(files), totalRows)
		}
	}
	if err := writer.close(); err != nil {
		job.finishSync(fmt.Errorf("写入CSV失败: %w", err))
		return
	}
	job.mu.Lock()
	job.TotalRows = totalRows
	job.mu.Unlock()
	job.setMessage(fmt.Sprintf("解析完成，共 %d 行，开始导入 DuckDB...", totalRows))

	// 3. 批量合并到 DuckDB（stock_daily 表，去重：重复数据自动跳过）
	inserted, err := dm.mergeStockDaily(ctx, csvPath)
	if err != nil {
		job.finishSync(err)
		return
	}

	job.setMessage(fmt.Sprintf("导入完成：共 %d 只股票 %d 行解析，新增 %d 行（重复已自动跳过）", len(files), totalRows, inserted))
	job.finishSync(nil)
	log.Printf("[StockSync] 同步完成: %d files, %d rows parsed, %d inserted", len(files), totalRows, inserted)
}

// mergeStockDaily 将 TDX 日线数据合并进 stock.stock_daily 表
// 采用"去重合并"策略：先清理历史重复，再仅插入 (symbol,date) 不存在的行，重复数据自动跳过。
// 保持 stock.ohlc / stock.stock_basic 视图（基于 stock_daily）不变。
func (dm *DuckDBManager) mergeStockDaily(ctx context.Context, csvPath string) (int64, error) {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	csvPosix := strings.ReplaceAll(csvPath, "\\", "/")

	// 清理历史遗留的 ohlc_new 暂存表（旧方案残留）
	dm.db.ExecContext(ctx, "DROP TABLE IF EXISTS stock.ohlc_new")

	// 1. 创建暂存表并从 CSV 加载（文件内先按 symbol+date 去重，重复自动跳过）
	createSQL := fmt.Sprintf(`
		CREATE OR REPLACE TABLE stock.stock_daily_new AS
		SELECT symbol, date, open, high, low, close, volume, amount
		FROM (
			SELECT
				symbol::VARCHAR AS symbol,
				date::DATE AS date,
				open::DOUBLE AS open,
				high::DOUBLE AS high,
				low::DOUBLE AS low,
				close::DOUBLE AS close,
				volume::DOUBLE AS volume,
				amount::DOUBLE AS amount,
				ROW_NUMBER() OVER (PARTITION BY symbol, date ORDER BY date DESC) AS rn
			FROM read_csv('%s',
				header=true,
				columns={
					'symbol': 'VARCHAR', 'date': 'DATE', 'open': 'DOUBLE',
					'high': 'DOUBLE', 'low': 'DOUBLE', 'close': 'DOUBLE',
					'volume': 'DOUBLE', 'amount': 'DOUBLE'
				},
				ignore_errors=false)
		) WHERE rn = 1
	`, csvPosix)
	if _, err := dm.db.ExecContext(ctx, createSQL); err != nil {
		return 0, fmt.Errorf("创建暂存表失败: %w", err)
	}
	defer dm.db.ExecContext(ctx, "DROP TABLE IF EXISTS stock.stock_daily_new")

	// 2. 清理 stock_daily 中历史遗留的重复 (symbol,date)，仅保留一条
	if _, err := dm.db.ExecContext(ctx, `
		DELETE FROM stock.stock_daily
		WHERE rowid IN (
			SELECT rowid FROM (
				SELECT rowid, ROW_NUMBER() OVER (PARTITION BY symbol, date ORDER BY date DESC) AS rn
				FROM stock.stock_daily
			) WHERE rn > 1
		)
	`); err != nil {
		log.Printf("[StockSync] 清理 stock_daily 历史重复数据失败(继续执行): %v", err)
	}

	// 3. 合并：仅插入 (symbol,date) 不存在的行，已存在的数据自动跳过
	res, err := dm.db.ExecContext(ctx, `
		INSERT INTO stock.stock_daily (symbol, date, open, high, low, close, volume, amount)
		SELECT n.symbol, n.date, n.open, n.high, n.low, n.close, n.volume, n.amount
		FROM stock.stock_daily_new n
		ANTI JOIN stock.stock_daily d ON n.symbol = d.symbol AND n.date = d.date
	`)
	if err != nil {
		return 0, fmt.Errorf("合并到 stock_daily 失败: %w", err)
	}
	inserted, _ := res.RowsAffected()

	// 4. 重建索引
	if _, err := dm.db.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS idx_stock_daily_symbol_date ON stock.stock_daily(symbol, date)"); err != nil {
		log.Printf("[StockSync] 创建索引失败: %v", err)
	}

	return inserted, nil
}

// ==================== 系统初始化（保留系统信息与表结构，清理用户数据） ====================

// resetPreservedTables 系统初始化时保留的表（基础字典/系统配置/内置策略目录，不随用户数据删除）
// 注：这里按「表名」判定并清空其余所有表。相比原先手写模型清单，可避免新增表（如
// agent_documents / llm_call_logs / daily_strategy_plans / agent_permissions / team_members /
// workflows 等）因为漏登记而残留用户日志；agents/workflows/team_members 等团队模板会在系统
// 启动时按默认配置重新播种，因此不属于「保留表」。
var resetPreservedTables = map[string]bool{
	"system_configs": true, // 系统配置
	"settings":       true, // 设置
	"market_indices": true, // 市场指数（行情缓存）
	"watch_stocks":   true, // 监控股票（用户自选列表）
	"instruments":    true, // 股票标的字典（不能清理，否则股票列表丢失）
	"strategies":     true, // 内置策略目录（启动时按 BuiltinStrategies 重建）
}

// ResetUserData 系统初始化：保留系统基本信息与表结构，清空所有用户数据。
// 采用「枚举全部业务表并清空 + 白名单跳过系统保留表」的方式，避免智能体日志等
// 新增用户表因漏登记在初始化后被残留。
func ResetUserData(db *gorm.DB) (map[string]int64, error) {
	if db == nil {
		return nil, fmt.Errorf("数据库未初始化")
	}

	results := make(map[string]int64)
	var tables []string
	if err := db.Raw(
		"SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'",
	).Scan(&tables).Error; err != nil {
		return nil, fmt.Errorf("枚举数据库表失败: %w", err)
	}

	for _, table := range tables {
		if resetPreservedTables[strings.ToLower(table)] {
			continue
		}
		res := db.Exec(fmt.Sprintf("DELETE FROM %q", table))
		if res.Error != nil {
			log.Printf("[Reset] 清空表 %s 失败: %v", table, res.Error)
			continue
		}
		results[table] = res.RowsAffected
	}

	return results, nil
}

// ==================== 基础数据维护（清理半年以上的日志与审计数据） ====================

// CleanupOldData 清理超过 months 个月的日志文件和审计数据
// 返回被清理的统计信息
func CleanupOldData(db *gorm.DB, months int) (map[string]interface{}, error) {
	if months <= 0 {
		months = 6
	}
	cutoff := time.Now().AddDate(0, -months, 0)
	result := make(map[string]interface{})

	// 1. 清理日志文件
	logDir := getExeLogDir()
	deletedLogs := 0
	var freedBytes int64
	if entries, err := os.ReadDir(logDir); err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				continue
			}
			if info.ModTime().Before(cutoff) {
				path := filepath.Join(logDir, entry.Name())
				if os.Remove(path) == nil {
					deletedLogs++
					freedBytes += info.Size()
				}
			}
		}
	}
	result["deleted_log_files"] = deletedLogs
	result["freed_log_bytes"] = freedBytes

	// 2. 清理审计数据（AuditLog 与相关活动日志）
	if db != nil {
		var deletedAudit int64
		res := db.Where("created_at < ?", cutoff).Delete(&AuditLog{})
		if res.Error == nil {
			deletedAudit = res.RowsAffected
		}
		result["deleted_audit_rows"] = deletedAudit

		// 清理其它活动/决策日志
		for _, model := range []interface{}{&AgentActivity{}, &CIODecisionLog{}, &PolicyLog{}, &AgentTaskLog{}, &DecisionTrace{}} {
			tableName := db.NamingStrategy.TableName(reflect.TypeOf(model).Elem().Name())
			r := db.Where("created_at < ?", cutoff).Delete(model)
			if r.Error == nil {
				result["deleted_"+tableName+"_rows"] = r.RowsAffected
			}
		}
	}

	result["cutoff_date"] = cutoff.Format("2006-01-02")
	return result, nil
}

// getExeLogDir 获取日志目录（可执行文件同级 log 文件夹）
func getExeLogDir() string {
	exePath, err := os.Executable()
	if err != nil {
		return "log"
	}
	return filepath.Join(filepath.Dir(exePath), "log")
}

// ==================== CSV 写入辅助 ====================

// csvWriter 简易 CSV 写入器（带缓冲与定时落盘，避免内存占用过大）
type csvWriter struct {
	f       *os.File
	buf     []byte
	flushed int64
}

func newCSVWriter(f *os.File) *csvWriter {
	return &csvWriter{f: f}
}

func (w *csvWriter) write(fields []string) {
	for i, field := range fields {
		if i > 0 {
			w.buf = append(w.buf, ',')
		}
		w.buf = append(w.buf, field...)
	}
	w.buf = append(w.buf, '\n')
	// 每约 4MB 落盘一次
	if len(w.buf) >= 4<<20 {
		w.flush()
	}
}

func (w *csvWriter) flush() {
	if len(w.buf) == 0 {
		return
	}
	if _, err := w.f.Write(w.buf); err == nil {
		w.flushed += int64(len(w.buf))
	}
	w.buf = w.buf[:0]
}

func (w *csvWriter) close() error {
	w.flush()
	return w.f.Close()
}

// trimFloat 格式化浮点数，去除多余尾零
func trimFloat(v float64) string {
	s := fmt.Sprintf("%.2f", v)
	s = strings.TrimRight(s, "0")
	s = strings.TrimRight(s, ".")
	if s == "" || s == "-" {
		return "0"
	}
	return s
}
