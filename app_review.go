package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/quantpilot/quantpilot/internal/data"
)

// GetTeamActivity 获取AI团队活动
func (a *App) GetTeamActivity(limit int) (interface{}, error) {
	if a.cioEngine == nil {
		return nil, fmt.Errorf("CIO Engine not initialized")
	}

	if limit <= 0 {
		limit = 500
	}

	activities := a.cioEngine.GetTeamActivity(limit)
	return map[string]interface{}{
		"activities": activities,
	}, nil
}

// GetTodayTeamActivity 获取今日AI团队活动
func (a *App) GetTodayTeamActivity() (interface{}, error) {
	if a.cioEngine == nil {
		return map[string]interface{}{
			"date":       time.Now().Format("2006-01-02"),
			"activities": []interface{}{},
			"total":      0,
		}, nil
	}

	activities := a.cioEngine.GetTodayTeamActivity()
	return map[string]interface{}{
		"date":       time.Now().Format("2006-01-02"),
		"activities": activities,
		"total":      len(activities),
	}, nil
}

// GetTodayTrades 获取今日交易记录
func (a *App) GetTodayTrades() (interface{}, error) {
	if a.portfolioEngine == nil {
		return map[string]interface{}{
			"date":   time.Now().Format("2006-01-02"),
			"trades": []interface{}{},
			"total":  0,
		}, nil
	}

	trades, err := a.portfolioEngine.GetTodayTrades()
	if err != nil {
		log.Printf("[QuantBot] GetTodayTrades error: %v", err)
		return map[string]interface{}{
			"date":   time.Now().Format("2006-01-02"),
			"trades": []interface{}{},
			"total":  0,
		}, nil
	}

	return map[string]interface{}{
		"date":   time.Now().Format("2006-01-02"),
		"trades": trades,
		"total":  len(trades),
	}, nil
}

// GenerateDailyReview 生成每日复盘报告
func (a *App) GenerateDailyReview() (interface{}, error) {
	if a.cioEngine == nil {
		return nil, fmt.Errorf("CIO Engine not initialized")
	}

	review, err := a.cioEngine.GenerateDailyReview(a.ctx)
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"success":     true,
		"review_date": review.ReviewDate,
		"summary":     review.Summary,
	}, nil
}

// TriggerDailyReview 实现DailyReviewProcessor接口
func (a *App) TriggerDailyReview(ctx context.Context) error {
	if a.cioEngine == nil {
		return fmt.Errorf("CIO Engine not initialized")
	}

	_, err := a.cioEngine.GenerateDailyReview(ctx)
	return err
}

// RebuildInvestmentPlan 实现 PlanRefreshProcessor 接口：用最新全市场因子数据重建当前投资方案选股与持仓，
// 由调度器在复盘阶段每日自动调用，实现「每日定时自动重建方案」。
func (a *App) RebuildInvestmentPlan(ctx context.Context) error {
	if a.plannerAgent == nil {
		return fmt.Errorf("Planner not initialized")
	}
	start := time.Now()
	plan, err := a.plannerAgent.RebuildCurrentPlanPortfolio()
	if err != nil {
		return err
	}
	if a.screenerService != nil && a.tradeablePool != nil {
		// 同步刷新可交易股票池，保证选股池与方案持仓一致
		if _, perr := a.generatePlanStockPool(plan); perr != nil {
			log.Printf("[QuantBot] 每日重建方案时刷新可交易股票池失败（不影响持仓更新）: %v", perr)
		}
	}
	log.Printf("[QuantBot] 每日自动重建投资方案完成: plan=%s, 耗时=%v", plan.PlanID, time.Since(start).Round(time.Millisecond))
	return nil
}

// GetDailyReviews 获取每日复盘报告列表
func (a *App) GetDailyReviews(days int) (interface{}, error) {
	if a.cioEngine == nil {
		return nil, fmt.Errorf("CIO Engine not initialized")
	}

	if days <= 0 {
		days = 7
	}

	reviews, err := a.cioEngine.GetDailyReviews(days)
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"days":    days,
		"reviews": reviews,
	}, nil
}

// GetResearchTimeline 读取最近 N 日的跨日研究记忆时间轴（六维判势 + 判断主张 + 验证结果），
// 供复盘/前端回看"历史预期→次日验证"（借鉴 easy-stock 研究飞轮，本机保留判断历史）。
func (a *App) GetResearchTimeline(days int) (interface{}, error) {
	if a.cioEngine == nil {
		return nil, fmt.Errorf("CIO Engine not initialized")
	}
	timeline, summary, err := a.cioEngine.GetResearchTimeline(context.Background(), days)
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{
		"days":     days,
		"summary":  summary,
		"timeline": timeline,
	}, nil
}

// DeleteDailyReview 删除指定 ID 的每日复盘快照记录（投资管理页「减少每日记录」）。
func (a *App) DeleteDailyReview(id int) (interface{}, error) {
	if a.cioEngine == nil {
		return nil, fmt.Errorf("CIO Engine not initialized")
	}
	if id <= 0 {
		return nil, fmt.Errorf("非法复盘ID: %d", id)
	}
	deleted, err := a.cioEngine.DeleteDailyReview(uint(id))
	if err != nil {
		return nil, err
	}
	log.Printf("[QuantBot] DeleteDailyReview(id=%d) 已删除 %d 条", id, deleted)
	return map[string]interface{}{"success": true, "deleted": deleted, "id": id}, nil
}

// GetLatestDailyReview 获取最新每日复盘报告
func (a *App) GetLatestDailyReview() (interface{}, error) {
	if a.cioEngine == nil {
		return nil, fmt.Errorf("CIO Engine not initialized")
	}

	review, err := a.cioEngine.GetLatestDailyReview()
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"review": review,
	}, nil
}

// GetAgentTaskLogs 获取指定日期的智能体任务日志
func (a *App) GetAgentTaskLogs(taskDate string, taskPhase string) (interface{}, error) {
	if a.agentTaskLogger == nil {
		return map[string]interface{}{
			"date":  taskDate,
			"phase": taskPhase,
			"logs":  []data.AgentTaskLog{},
			"total": 0,
		}, nil
	}

	logs, err := a.agentTaskLogger.GetTaskLogs(taskDate, taskPhase)
	if err != nil {
		log.Printf("[QuantBot] GetAgentTaskLogs error: %v", err)
		return map[string]interface{}{
			"date":  taskDate,
			"phase": taskPhase,
			"logs":  []data.AgentTaskLog{},
			"total": 0,
		}, nil
	}

	return map[string]interface{}{
		"date":  taskDate,
		"phase": taskPhase,
		"logs":  logs,
		"total": len(logs),
	}, nil
}

// GetAgentTaskLogsByDays 获取最近N天的智能体任务日志
func (a *App) GetAgentTaskLogsByDays(days int) (interface{}, error) {
	if a.agentTaskLogger == nil {
		return map[string]interface{}{
			"days":  days,
			"logs":  []data.AgentTaskLog{},
			"total": 0,
		}, nil
	}

	logs, err := a.agentTaskLogger.GetTaskLogsByDays(days)
	if err != nil {
		log.Printf("[QuantBot] GetAgentTaskLogsByDays error: %v", err)
		return map[string]interface{}{
			"days":  days,
			"logs":  []data.AgentTaskLog{},
			"total": 0,
		}, nil
	}

	return map[string]interface{}{
		"days":  days,
		"logs":  logs,
		"total": len(logs),
	}, nil
}

// GetLatestAgentTaskLogs 获取最新一天的智能体任务日志
func (a *App) GetLatestAgentTaskLogs() (interface{}, error) {
	if a.agentTaskLogger == nil {
		return map[string]interface{}{
			"date":  time.Now().Format("2006-01-02"),
			"logs":  []data.AgentTaskLog{},
			"total": 0,
		}, nil
	}

	logs, err := a.agentTaskLogger.GetLatestTaskLogs()
	if err != nil {
		log.Printf("[QuantBot] GetLatestAgentTaskLogs error: %v", err)
		return map[string]interface{}{
			"date":  time.Now().Format("2006-01-02"),
			"logs":  []data.AgentTaskLog{},
			"total": 0,
		}, nil
	}

	return map[string]interface{}{
		"date":  time.Now().Format("2006-01-02"),
		"logs":  logs,
		"total": len(logs),
	}, nil
}

// GetAgentWorkDetails 获取当日各个智能体的工作详情
func (a *App) GetAgentWorkDetails() (interface{}, error) {
	if a.agentTaskLogger == nil {
		return map[string]interface{}{
			"date":    time.Now().Format("2006-01-02"),
			"details": []data.AgentWorkDetail{},
			"total":   0,
		}, nil
	}

	details, err := a.agentTaskLogger.GetAgentWorkDetails()
	if err != nil {
		log.Printf("[QuantBot] GetAgentWorkDetails error: %v", err)
		return map[string]interface{}{
			"date":    time.Now().Format("2006-01-02"),
			"details": []data.AgentWorkDetail{},
			"total":   0,
		}, nil
	}

	if details == nil {
		details = []data.AgentWorkDetail{}
	}

	return map[string]interface{}{
		"date":    time.Now().Format("2006-01-02"),
		"details": details,
		"total":   len(details),
	}, nil
}

// ClearAgentTaskLogs 清理指定日期的智能体任务日志
func (a *App) ClearAgentTaskLogs(taskDate string) (interface{}, error) {
	if a.agentTaskLogger == nil {
		return nil, fmt.Errorf("Agent Task Logger not initialized")
	}

	err := a.agentTaskLogger.ClearTaskLogs(taskDate)
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"date":    taskDate,
		"status":  "cleared",
		"message": "任务日志已清理",
	}, nil
}

// GetTransparencyData 获取 AI 决策透明度数据
func (a *App) GetTransparencyData(sessionID string) (interface{}, error) {
	if a.harnessApp == nil {
		return nil, fmt.Errorf("Harness not initialized")
	}

	tracker := a.harnessApp.GetTracker()
	if tracker == nil {
		return nil, fmt.Errorf("Transparency tracker not initialized")
	}

	if sessionID != "" {
		session := tracker.GetSession(sessionID)
		if session == nil {
			return map[string]interface{}{
				"error": "Session not found",
			}, nil
		}
		return session, nil
	}

	// 返回最新的会话
	session := tracker.GetLatestSession()
	if session == nil {
		return map[string]interface{}{
			"data_sources": []interface{}{},
			"algorithms":   []interface{}{},
			"decisions":    []interface{}{},
			"message":      "暂无透明度数据，请先执行智能体任务",
		}, nil
	}

	return session, nil
}

// GetTransparencyStats 获取透明度统计信息
func (a *App) GetTransparencyStats() (interface{}, error) {
	if a.harnessApp == nil {
		return nil, fmt.Errorf("Harness not initialized")
	}

	tracker := a.harnessApp.GetTracker()
	if tracker == nil {
		return nil, fmt.Errorf("Transparency tracker not initialized")
	}

	return tracker.GetStats(), nil
}
