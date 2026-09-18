package data

import (
	"context"
	"fmt"
	"time"

	"github.com/quantpilot/quantpilot/internal/thssdk"
)

// ==================== 同花顺官方交易日历 ====================
//
// 数据来源：同花顺官方交易日历端点（GET /api/a-share/calendar/trading-days），
// 返回近一年 A 股交易日序列（Asia/Shanghai）。非交易日不在列表中属正常行为。
// 写入 stock.trading_calendar(date)，供程序精确判断交易日（含节假日），替代周末假设。

const thsLoc = "Asia/Shanghai"

// TradingCalendarSyncResult 交易日历同步结果摘要。
type TradingCalendarSyncResult struct {
	Count      int    `json:"count"`      // 同步交易日数
	DateMin    string `json:"date_min"`   // 最早交易日
	DateMax    string `json:"date_max"`   // 最晚交易日
	Configured bool   `json:"configured"` // 是否已配置官方源
	Message    string `json:"message"`
}

// SyncTHSTradingCalendar 从同花顺官方拉取近一年交易日序列，写入 stock.trading_calendar。
// 全量重建（近一年窗口），重复拉取幂等。未配置官方 API Key 时直接报错（严禁伪造/跳过）。
func (dm *DuckDBManager) SyncTHSTradingCalendar(ctx context.Context) (*TradingCalendarSyncResult, error) {
	c := thssdk.Default()
	if c == nil {
		return nil, fmt.Errorf("同花顺官方数据源未配置（请在设置-数据源中启用并填写 API Key）")
	}
	if dm == nil || dm.db == nil {
		return nil, fmt.Errorf("DuckDB 未初始化")
	}

	days, err := c.TradingCalendarTradingDays(ctx)
	if err != nil {
		return nil, fmt.Errorf("拉取同花顺交易日历失败: %w", err)
	}

	dm.mu.Lock()
	defer dm.mu.Unlock()
	if _, err := dm.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS stock.trading_calendar (
		date DATE PRIMARY KEY
	)`); err != nil {
		return nil, fmt.Errorf("创建交易日历表失败: %w", err)
	}

	tx, err := dm.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("启动事务失败: %w", err)
	}
	defer tx.Rollback()
	for _, d := range days {
		date := msToDate(d.DateMs)
		if _, err := tx.ExecContext(ctx, `INSERT INTO stock.trading_calendar(date) VALUES (?) ON CONFLICT(date) DO NOTHING`, date); err != nil {
			return nil, fmt.Errorf("插入交易日 %s 失败: %w", date, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("提交事务失败: %w", err)
	}

	res := &TradingCalendarSyncResult{Count: len(days), Configured: true}
	if len(days) > 0 {
		res.DateMin = msToDate(days[0].DateMs)
		res.DateMax = msToDate(days[len(days)-1].DateMs)
	}
	res.Message = fmt.Sprintf("交易日历同步完成：近一年 %d 个交易日（%s ~ %s）", res.Count, res.DateMin, res.DateMax)
	return res, nil
}

// msToDate 毫秒时间戳(Asia/Shanghai 00:00) → yyyy-MM-dd。
func msToDate(ms int64) string {
	loc, err := time.LoadLocation(thsLoc)
	if err != nil {
		loc = time.FixedZone("CST", 8*3600)
	}
	return time.Unix(ms/1000, (ms%1000)*int64(time.Millisecond)).In(loc).Format("2006-01-02")
}

// TradingCalendarLastDay 返回交易日历中 <= todayStr 的最近一个交易日；skipToday=true 且最近一日恰为 today 时回退到前一交易日。
// 表为空或查询异常时返回 ok=false，由调用方回退周末规则。
func (dm *DuckDBManager) TradingCalendarLastDay(ctx context.Context, todayStr string, skipToday bool) (string, bool) {
	if dm == nil || dm.db == nil {
		return "", false
	}
	rows, err := dm.db.QueryContext(ctx, `SELECT date FROM stock.trading_calendar WHERE date <= CAST(? AS DATE) ORDER BY date DESC LIMIT 2`, todayStr)
	if err != nil {
		return "", false
	}
	defer rows.Close()
	var days []string
	for rows.Next() {
		var s string
		if rows.Scan(&s) == nil {
			days = append(days, s)
		}
	}
	if len(days) == 0 {
		return "", false
	}
	pick := days[0]
	if skipToday && pick == todayStr {
		if len(days) >= 2 {
			pick = days[1]
		} else {
			return "", false
		}
	}
	return pick, true
}

// THSTradingCalendarStatus 交易日历同步状态（供前端展示）。
func (dm *DuckDBManager) THSTradingCalendarStatus() map[string]interface{} {
	out := map[string]interface{}{
		"configured": thssdk.Enabled(),
		"has_data":   false,
		"count":      int64(0),
	}
	if dm == nil || dm.db == nil {
		return out
	}
	ctx := context.Background()
	var n int64
	if err := dm.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM stock.trading_calendar").Scan(&n); err == nil {
		out["count"] = n
		out["has_data"] = n > 0
	}
	var dateMin, dateMax, latest string
	if err := dm.db.QueryRowContext(ctx, "SELECT MIN(CAST(date AS VARCHAR)), MAX(CAST(date AS VARCHAR)) FROM stock.trading_calendar").Scan(&dateMin, &dateMax); err == nil {
		out["date_min"] = dateMin
		out["date_max"] = dateMax
	}
	if err := dm.db.QueryRowContext(ctx, "SELECT MAX(CAST(date AS VARCHAR)) FROM stock.trading_calendar WHERE date <= CURRENT_DATE").Scan(&latest); err == nil {
		out["latest"] = latest
	}
	return out
}