package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/quantpilot/quantpilot/internal/audit"
	"github.com/quantpilot/quantpilot/internal/data"
	"github.com/quantpilot/quantpilot/internal/thssdk"
	"gorm.io/gorm"
)

// GetAuditLogs 查询审计日志
func (a *App) GetAuditLogs(eventType string, userID string, startDate string, endDate string, page int, pageSize int) (interface{}, error) {
	if a.auditService == nil {
		return nil, fmt.Errorf("Audit Service not initialized")
	}

	query := audit.AuditQuery{
		EventType: eventType,
		UserID:    userID,
		StartDate: startDate,
		EndDate:   endDate,
		Page:      page,
		PageSize:  pageSize,
	}

	result, err := a.auditService.QueryLogs(query)
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"total":    result.Total,
		"page":     result.Page,
		"pageSize": result.PageSize,
		"items":    result.Items,
	}, nil
}

// LogLiveActivity 记录实时活动到审计日志
func (a *App) LogLiveActivity(agentRole string, activityType string, description string, detailsJSON string) (interface{}, error) {
	if a.auditService == nil {
		return nil, fmt.Errorf("Audit Service not initialized")
	}

	var details interface{}
	if detailsJSON != "" {
		if err := json.Unmarshal([]byte(detailsJSON), &details); err != nil {
			details = map[string]interface{}{"raw": detailsJSON}
		}
	}

	a.auditService.LogLiveActivity(agentRole, activityType, description, details)

	return map[string]interface{}{
		"status": "ok",
	}, nil
}

// GetAuditEventTypes 获取审计事件类型列表
func (a *App) GetAuditEventTypes() (interface{}, error) {
	return map[string]interface{}{
		"eventTypes": []map[string]interface{}{
			{"key": data.AuditEventLogin, "label": "系统启动/登录"},
			{"key": data.AuditEventLogout, "label": "系统关闭/登出"},
			{"key": data.AuditEventScreening, "label": "选股"},
			{"key": data.AuditEventStrategy, "label": "策略"},
			{"key": data.AuditEventBacktest, "label": "回测"},
			{"key": data.AuditEventAIAnalysis, "label": "AI分析"},
			{"key": data.AuditEventPlanner, "label": "投资规划"},
			{"key": data.AuditEventLiveActivity, "label": "AI实时活动"},
			{"key": data.AuditEventCIODailyReview, "label": "CIO每日复盘"},
			{"key": data.AuditEventApproval, "label": "审批"},
			{"key": data.AuditEventOrder, "label": "交易订单"},
			{"key": data.AuditEventConfigChange, "label": "配置变更"},
			{"key": data.AuditEventTierChange, "label": "版本升级"},
		},
	}, nil
}

// GetAuditServiceStats 获取审计服务统计信息
func (a *App) GetAuditServiceStats() (interface{}, error) {
	if a.auditService == nil {
		return nil, fmt.Errorf("Audit Service not initialized")
	}
	return a.auditService.GetStats(), nil
}

// SetAuditRetentionDays 设置审计日志保留天数
func (a *App) SetAuditRetentionDays(days int) (interface{}, error) {
	if a.auditService == nil {
		return nil, fmt.Errorf("Audit Service not initialized")
	}
	a.auditService.SetRetentionDays(days)
	return map[string]interface{}{
		"status":         "ok",
		"retention_days": a.auditService.GetRetentionDays(),
	}, nil
}

// GetAuditRetentionDays 获取审计日志保留天数
func (a *App) GetAuditRetentionDays() (interface{}, error) {
	if a.auditService == nil {
		return nil, fmt.Errorf("Audit Service not initialized")
	}
	return map[string]interface{}{
		"retention_days": a.auditService.GetRetentionDays(),
	}, nil
}

// ForceAuditCleanup 强制执行审计日志清理
func (a *App) ForceAuditCleanup() (interface{}, error) {
	if a.auditService == nil {
		return nil, fmt.Errorf("Audit Service not initialized")
	}
	deleted, err := a.auditService.ForceCleanup()
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{
		"status":         "ok",
		"deleted_count":  deleted,
		"retention_days": a.auditService.GetRetentionDays(),
	}, nil
}

// FlushAuditLogs 强制刷新所有待写入的审计日志
func (a *App) FlushAuditLogs() (interface{}, error) {
	if a.auditService == nil {
		return nil, fmt.Errorf("Audit Service not initialized")
	}
	a.auditService.Flush()
	return map[string]interface{}{
		"status": "ok",
	}, nil
}

// StartStockSync 启动 TDX 日线数据同步到 stock.duckdb（异步执行）
func (a *App) StartStockSync(tdxPath string) (interface{}, error) {
	if a.duckdbManager == nil {
		return nil, fmt.Errorf("DuckDB 未初始化")
	}

	// 未传路径时使用配置中的 TDX 路径
	if strings.TrimSpace(tdxPath) == "" {
		if a.configManager != nil {
			cfg := a.configManager.GetConfig()
			tdxPath = cfg.TDXPath
		}
	}
	if strings.TrimSpace(tdxPath) == "" {
		return nil, fmt.Errorf("未配置 TDX 数据路径，请先在数据源设置中配置")
	}

	if err := a.duckdbManager.StartStockSync(tdxPath); err != nil {
		return nil, err
	}

	if a.auditService != nil {
		a.auditService.LogAuditEvent("DATA_MAINTENANCE", "start_sync", "stock_data", "", "user", "", "success", map[string]interface{}{
			"tdx_path": tdxPath,
		})
	}

	return map[string]interface{}{
		"status": "started",
		"tdx":    tdxPath,
	}, nil
}

// GetStockSyncStatus 获取 TDX 数据同步任务状态
func (a *App) GetStockSyncStatus() (interface{}, error) {
	if a.duckdbManager == nil {
		return nil, fmt.Errorf("DuckDB 未初始化")
	}
	return a.duckdbManager.GetStockSyncStatus(), nil
}

// StartFinancialSync 启动股票财务数据同步（异步执行）。
// mode: "full" 全量拉全历史；"incremental" 默认，只拉表内最新报告期之后的新增数据。
// source: "em"（东方财富，默认）/"tdx"（通达信终端，需主程序登录并已下载专业财务数据）/"ths"（同花顺官方，需配置 API Key）。
func (a *App) StartFinancialSync(mode, source string) (interface{}, error) {
	if a.duckdbManager == nil || !a.duckdbManager.HasStockDB() {
		return nil, fmt.Errorf("股票数据库未初始化")
	}
	// 数据源切换：tdx 注入通达信拉取器，ths 注入同花顺官方拉取器，em 恢复东财
	src := strings.TrimSpace(source)
	if src != "em" && src != "tdx" && src != "ths" {
		src = "em"
	}
	switch src {
	case "tdx":
		if a.tdxManager == nil {
			return nil, fmt.Errorf("TDX 服务未初始化")
		}
		fetcher := a.tdxManager.NewTDXFinancialFetcher()
		if fetcher == nil {
			return nil, fmt.Errorf("通达信终端不可用（请确认已登录并用 tdx_terminal 建立过连接）")
		}
		a.duckdbManager.SetFinancialFetcher(fetcher)
	case "ths":
		// 同花顺官方源：未启用/未配置 Key 时直接拒绝（严禁伪造）
		if !thssdk.Enabled() {
			return nil, fmt.Errorf("同花顺官方数据源未配置（请先在设置-数据源中启用并填写 API Key）")
		}
		a.duckdbManager.SetFinancialFetcher(data.NewTHSFinancialFetcher())
	default:
		a.duckdbManager.SetFinancialFetcher(nil)
	}
	if err := a.duckdbManager.StartFinancialSync(context.Background(), mode); err != nil {
		return nil, err
	}
	if a.auditService != nil {
		a.auditService.LogAuditEvent("DATA_MAINTENANCE", "start_financial_sync", "financial_data", "", "user", "", "success", map[string]interface{}{
			"mode":   mode,
			"source": src,
		})
	}
	return map[string]interface{}{
		"status": "started",
		"mode":   mode,
		"source": src,
	}, nil
}

// GetFinancialSyncStatus 获取财务数据同步任务状态
func (a *App) GetFinancialSyncStatus() (interface{}, error) {
	if a.duckdbManager == nil || !a.duckdbManager.HasStockDB() {
		return nil, fmt.Errorf("股票数据库未初始化")
	}
	return a.duckdbManager.GetFinancialSyncStatus(), nil
}

// GetFinancialReportAsOf 查询某股票在历史时点已披露的最新财务报告（供回测/展示，无未来函数）
func (a *App) GetFinancialReportAsOf(symbol, asOfDate string) (interface{}, error) {
	if a.duckdbManager == nil || !a.duckdbManager.HasStockDB() {
		return nil, fmt.Errorf("股票数据库未初始化")
	}
	rep, err := a.duckdbManager.GetFinancialAsOf(symbol, asOfDate)
	if err != nil {
		return nil, err
	}
	if rep == nil {
		return nil, nil
	}
	return map[string]interface{}{
		"symbol":         rep.Symbol,
		"report_date":    rep.ReportDate,
		"ann_date":       rep.AnnDate,
		"total_revenue":  rep.TotalRevenue,
		"revenue_yoy":    rep.RevenueYOY,
		"net_profit":     rep.NetProfit,
		"profit_yoy":     rep.ProfitYOY,
		"net_profit_ded": rep.NetProfitDed,
		"roe":            rep.Roe,
		"gross_margin":   rep.GrossMargin,
		"net_margin":     rep.NetMargin,
		"total_assets":   rep.TotalAssets,
		"total_liab":     rep.TotalLiab,
		"debt_ratio":     rep.DebtRatio,
		"oper_cashflow":  rep.OperCashflow,
		"total_shares":   rep.TotalShares,
		"float_shares":   rep.FloatShares,
		"eps":            rep.EPS,
		"bps":            rep.BPS,
	}, nil
}

// ResetUserData 系统初始化：保留系统基本信息与表结构，清空所有用户数据
// confirm 必须等于 "RESET" 才会执行，防止误操作
func (a *App) ResetUserData(confirm string) (interface{}, error) {
	if a.sqliteManager == nil {
		return nil, fmt.Errorf("数据库未初始化")
	}
	if strings.TrimSpace(confirm) != "RESET" {
		return nil, fmt.Errorf("确认信息不正确，系统初始化已取消")
	}

	// 先重置内存引擎（清空内存持仓、现金归零），再清空数据库表。
	// 若顺序颠倒，内存引擎的定时保存会在清库后把旧持仓/旧现金重新写回数据库，
	// 且重启时 NewEngine 用初始资金重建组合却不扣除持仓成本，导致现金与持仓叠加虚高。
	initialCapital := 0.0
	if a.configManager != nil {
		initialCapital = a.configManager.GetConfig().InitialCapital
	}
	if a.portfolioEngine != nil {
		a.portfolioEngine.Reset(initialCapital)
		log.Printf("[QuantBot] 系统初始化：内存组合引擎已重置 (cash=%.2f)", initialCapital)
	}

	results, err := data.ResetUserData(a.sqliteManager.GetDB())
	if err != nil {
		return nil, err
	}

	// RESET 本身写入审计事件（审计日志不随用户数据删除，可追溯）
	if a.auditService != nil {
		a.auditService.LogAuditEvent(
			"SYSTEM_RESET", "reset", "system", "", "user", "本地用户",
			"success",
			map[string]interface{}{
				"scope":   "USER_DATA",
				"detail":  "系统初始化：保留系统信息与表结构，清空用户数据",
				"cleared": results,
			},
		)
	}

	return map[string]interface{}{
		"status":           "ok",
		"cleared":          results,
		"restart_required": true,
		"restart_tip":      "用户数据已清空。为保证运行中的内存状态与已清空的数据库一致，请重启应用后再继续使用。",
	}, nil
}

// CleanupOldData 基础数据维护：清理半年以上的日志文件和审计数据
func (a *App) CleanupOldData() (interface{}, error) {
	var db *gorm.DB
	if a.sqliteManager != nil {
		db = a.sqliteManager.GetDB()
	}
	result, err := data.CleanupOldData(db, 6)
	if err != nil {
		return nil, err
	}

	if a.auditService != nil {
		a.auditService.LogAuditEvent("DATA_MAINTENANCE", "cleanup", "system", "", "user", "", "success", result)
	}

	return map[string]interface{}{
		"status": "ok",
		"result": result,
	}, nil
}
