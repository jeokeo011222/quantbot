package main

import (
	"context"
	"fmt"
)

// ==================== 同花顺官方数据源业务方法 ====================
// 复权因子导入/估值快照/集合竞价快照/日K导入/交易日历同步。
// 全部为官方真实数据，未配置 API Key 时直接返回错误，严禁伪造。

// ImportTHSAdjFactors 一键导入同花顺官方全市场复权事件，推算日频复权因子
// 并重建 stock.adj_factor 表 + stock.ohlc_qfq 前复权视图（数据维护区调用）。
func (a *App) ImportTHSAdjFactors() (interface{}, error) {
	if a.duckdbManager == nil || !a.duckdbManager.HasStockDB() {
		return nil, fmt.Errorf("股票数据库未初始化")
	}
	res, err := a.duckdbManager.ImportTHSAdjFactors(context.Background())
	if err != nil {
		return nil, err
	}
	if a.auditService != nil {
		a.auditService.LogAuditEvent("DATA_MAINTENANCE", "import_ths_adj_factors", "financial_data", "", "user", "", "success", map[string]interface{}{
			"event_rows":  res.EventCount,
			"factor_rows": res.FactorRows,
		})
	}
	return res, nil
}

// GetTHSAdjFactorStatus 复权因子数据状态（配置是否就绪/事件行数/视图是否就绪）
func (a *App) GetTHSAdjFactorStatus() (interface{}, error) {
	if a.duckdbManager == nil || !a.duckdbManager.HasStockDB() {
		return nil, fmt.Errorf("股票数据库未初始化")
	}
	return a.duckdbManager.AdjFactorStatus(), nil
}

// SyncTHSTradingCalendar 一键同步同花顺官方近一年交易日历到 stock.trading_calendar
// （数据维护-同花顺数据页签调用；同步后程序可精确判断交易日含节假日）。
func (a *App) SyncTHSTradingCalendar() (interface{}, error) {
	if a.duckdbManager == nil || !a.duckdbManager.HasStockDB() {
		return nil, fmt.Errorf("股票数据库未初始化")
	}
	res, err := a.duckdbManager.SyncTHSTradingCalendar(context.Background())
	if err != nil {
		return nil, err
	}
	if a.auditService != nil {
		a.auditService.LogAuditEvent("DATA_MAINTENANCE", "sync_ths_trading_calendar", "trading_calendar", "", "user", "", "success", map[string]interface{}{
			"count": res.Count,
		})
	}
	return res, nil
}

// GetTHSTradingCalendarStatus 交易日历同步状态摘要。
func (a *App) GetTHSTradingCalendarStatus() (interface{}, error) {
	return a.duckdbManager.THSTradingCalendarStatus(), nil
}

// ImportTHSDailyK 一键导入同花顺官方全市场日K到 stock.stock_daily（stock.ohlc 视图底层）。
// mode: "full" 全量近10年 / "incr" 近10交易日增量。按 (symbol,date) 去重，已存在跳过。
func (a *App) ImportTHSDailyK(mode string) (interface{}, error) {
	if a.duckdbManager == nil || !a.duckdbManager.HasStockDB() {
		return nil, fmt.Errorf("股票数据库未初始化")
	}
	if mode != "incr" {
		mode = "full"
	}
	res, err := a.duckdbManager.ImportTHSDailyK(context.Background(), mode)
	if err != nil {
		return nil, err
	}
	if a.auditService != nil {
		a.auditService.LogAuditEvent("DATA_MAINTENANCE", "import_ths_daily_k", "stock_ohlc", "", "user", "", "success", map[string]interface{}{
			"mode":     mode,
			"inserted": res.Inserted,
		})
	}
	return res, nil
}

// GetTHSDailyKStatus 同花顺日K同步状态（是否配置/总行数/覆盖股票数/最新日期）。
func (a *App) GetTHSDailyKStatus() (interface{}, error) {
	if a.duckdbManager == nil || !a.duckdbManager.HasStockDB() {
		return nil, fmt.Errorf("股票数据库未初始化")
	}
	return a.duckdbManager.THSDailyKStatus(), nil
}

// thsStr 解引用可空字符串（null → ""）。
func thsStr(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

// thsF64 解引用可空浮点（null → 0，表示未披露）。
func thsF64(v *float64) float64 {
	if v == nil {
		return 0
	}
	return *v
}
