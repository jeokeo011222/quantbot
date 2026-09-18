package data

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"
)

// AgentTaskLogger 智能体任务日志记录器
type AgentTaskLogger struct {
	db *SQLiteManager
}

// NewAgentTaskLogger 创建任务日志记录器
func NewAgentTaskLogger(db *SQLiteManager) *AgentTaskLogger {
	return &AgentTaskLogger{db: db}
}

// LogTaskStart 记录任务开始
func (l *AgentTaskLogger) LogTaskStart(taskDate, taskPhase, agentRole, taskName string, taskOrder int) *AgentTaskLog {
	if l.db == nil {
		return nil
	}

	now := time.Now()
	task := &AgentTaskLog{
		TaskDate:  taskDate,
		TaskPhase: taskPhase,
		AgentRole: agentRole,
		TaskName:  taskName,
		TaskOrder: taskOrder,
		Status:    "RUNNING",
		StartTime: &now,
		CreatedAt: now,
	}

	if err := l.db.GetDB().Create(task).Error; err != nil {
		log.Printf("[AgentTaskLogger] Failed to log task start: %v", err)
		return nil
	}

	return task
}

// LogTaskComplete 记录任务完成
func (l *AgentTaskLogger) LogTaskComplete(taskID uint, deliverableType, deliverableName string, deliverableData interface{}, summary string) {
	if l.db == nil {
		return
	}

	var dataJSON string
	if deliverableData != nil {
		bytes, err := json.Marshal(deliverableData)
		if err == nil {
			dataJSON = string(bytes)
		}
	}

	now := time.Now()
	var durationMs int64

	// 获取任务信息（包括开始时间和日期）
	var task AgentTaskLog
	if err := l.db.GetDB().First(&task, taskID).Error; err != nil {
		log.Printf("[AgentTaskLogger] Failed to get task: %v", err)
		return
	}

	if task.StartTime != nil {
		durationMs = now.Sub(*task.StartTime).Milliseconds()
	}

	updates := map[string]interface{}{
		"status":           "COMPLETED",
		"end_time":         now,
		"duration_ms":      durationMs,
		"deliverable_type": deliverableType,
		"deliverable_name": deliverableName,
		"deliverable_data": dataJSON,
		"summary":          summary,
	}

	if err := l.db.GetDB().Model(&AgentTaskLog{}).Where("id = ?", taskID).Updates(updates).Error; err != nil {
		log.Printf("[AgentTaskLogger] Failed to update task: %v", err)
		return
	}
}

// LogTaskFailed 记录任务失败
func (l *AgentTaskLogger) LogTaskFailed(taskID uint, errMsg string) {
	if l.db == nil {
		return
	}

	now := time.Now()
	var durationMs int64

	// 获取开始时间
	var task AgentTaskLog
	if err := l.db.GetDB().First(&task, taskID).Error; err == nil && task.StartTime != nil {
		durationMs = now.Sub(*task.StartTime).Milliseconds()
	}

	updates := map[string]interface{}{
		"status":      "FAILED",
		"end_time":    now,
		"duration_ms": durationMs,
		"errors":      errMsg,
	}

	if err := l.db.GetDB().Model(&AgentTaskLog{}).Where("id = ?", taskID).Updates(updates).Error; err != nil {
		log.Printf("[AgentTaskLogger] Failed to update task: %v", err)
	}
}

// GetTaskLogs 获取指定日期的任务日志
func (l *AgentTaskLogger) GetTaskLogs(taskDate string, taskPhase string) ([]AgentTaskLog, error) {
	if l.db == nil {
		return nil, nil
	}

	var logs []AgentTaskLog
	query := l.db.GetDB().Where("task_date = ?", taskDate)

	if taskPhase != "" {
		query = query.Where("task_phase = ?", taskPhase)
	}

	if err := query.Order("task_phase, task_order").Find(&logs).Error; err != nil {
		return nil, err
	}

	return logs, nil
}

// GetTaskLogsByDays 获取最近几天的任务日志
func (l *AgentTaskLogger) GetTaskLogsByDays(days int) ([]map[string]interface{}, error) {
	if l.db == nil {
		return nil, nil
	}

	since := time.Now().AddDate(0, 0, -days)
	var logs []AgentTaskLog
	if err := l.db.GetDB().
		Where("created_at >= ?", since).
		Order("task_date DESC, task_phase, task_order").
		Find(&logs).Error; err != nil {
		return nil, err
	}

	var result []map[string]interface{}
	for _, log := range logs {
		result = append(result, map[string]interface{}{
			"id":               log.ID,
			"task_date":        log.TaskDate,
			"task_phase":       log.TaskPhase,
			"agent_role":       log.AgentRole,
			"task_name":        log.TaskName,
			"task_order":       log.TaskOrder,
			"status":           log.Status,
			"start_time":       log.StartTime,
			"end_time":         log.EndTime,
			"duration_ms":      log.DurationMs,
			"deliverable_type": log.DeliverableType,
			"deliverable_name": log.DeliverableName,
			"deliverable_data": log.DeliverableData,
			"summary":          log.Summary,
			"details":          log.Details,
			"errors":           log.Errors,
			"created_at":       log.CreatedAt,
		})
	}

	return result, nil
}

// GetLatestTaskLogs 获取最新一天的任务日志
func (l *AgentTaskLogger) GetLatestTaskLogs() ([]map[string]interface{}, error) {
	if l.db == nil {
		return nil, nil
	}

	today := time.Now().Format("2006-01-02")
	logs, err := l.GetTaskLogs(today, "")
	if err != nil {
		return nil, err
	}

	var result []map[string]interface{}
	for _, log := range logs {
		result = append(result, map[string]interface{}{
			"id":               log.ID,
			"task_date":        log.TaskDate,
			"task_phase":       log.TaskPhase,
			"agent_role":       log.AgentRole,
			"task_name":        log.TaskName,
			"task_order":       log.TaskOrder,
			"status":           log.Status,
			"start_time":       log.StartTime,
			"end_time":         log.EndTime,
			"duration_ms":      log.DurationMs,
			"deliverable_type": log.DeliverableType,
			"deliverable_name": log.DeliverableName,
			"deliverable_data": log.DeliverableData,
			"summary":          log.Summary,
			"details":          log.Details,
			"errors":           log.Errors,
			"created_at":       log.CreatedAt,
		})
	}

	return result, nil
}

// ClearTaskLogs 清理指定日期的任务日志（用于重新生成）
func (l *AgentTaskLogger) ClearTaskLogs(taskDate string) error {
	if l.db == nil {
		return nil
	}

	return l.db.GetDB().Where("task_date = ?", taskDate).Delete(&AgentTaskLog{}).Error
}

// AgentWorkDetail 智能体工作详情
type AgentWorkDetail struct {
	Role        string   `json:"role"`
	State       string   `json:"state"`
	CurrentTask string   `json:"current_task"`
	Progress    int      `json:"progress"`
	TodayTasks  []string `json:"today_tasks"`
	TaskCount   int      `json:"task_count"`
	RunningTask string   `json:"running_task,omitempty"`
}

// DailyTaskPlan 每日任务计划（每个角色在各阶段的计划任务数）
type DailyTaskPlan struct {
	PreMarketTasks  int // 盘前任务数
	MorningTasks    int // 上午交易任务数
	AfternoonTasks  int // 下午交易任务数
	PostMarketTasks int // 盘后任务数
}

// getDailyTaskPlan 获取每个角色的每日任务计划
func getDailyTaskPlan(role string) DailyTaskPlan {
	plans := map[string]DailyTaskPlan{
		"CIO":     {PreMarketTasks: 4, MorningTasks: 3, AfternoonTasks: 3, PostMarketTasks: 3},
		"PLANNER": {PreMarketTasks: 2, MorningTasks: 4, AfternoonTasks: 4, PostMarketTasks: 2},
		"QUANT":   {PreMarketTasks: 3, MorningTasks: 2, AfternoonTasks: 2, PostMarketTasks: 2},
		"RISK":    {PreMarketTasks: 3, MorningTasks: 3, AfternoonTasks: 3, PostMarketTasks: 3},
		"TRADER":  {PreMarketTasks: 1, MorningTasks: 4, AfternoonTasks: 4, PostMarketTasks: 2},
	}

	if plan, ok := plans[role]; ok {
		return plan
	}
	return DailyTaskPlan{PreMarketTasks: 2, MorningTasks: 3, AfternoonTasks: 3, PostMarketTasks: 2}
}

// calculateProgressByPhase 根据当前市场阶段计算进度
func calculateProgressByPhase(phase MarketPhase, plan DailyTaskPlan, completedCount, runningCount int) int {
	// 计算全天总计划任务数
	totalDayPlan := plan.PreMarketTasks + plan.MorningTasks + plan.AfternoonTasks + plan.PostMarketTasks

	// 如果完成数已达到或超过全天计划，显示100%
	if totalDayPlan > 0 && completedCount >= totalDayPlan {
		return 100
	}

	switch phase {
	case PhasePreMarket:
		// 盘前准备阶段：任务尚未启动
		return 0

	case PhasePreOpen:
		// 盘前阶段：进度 = 已完成 / 盘前计划
		if plan.PreMarketTasks == 0 {
			return 0
		}
		return int(float64(completedCount)/float64(plan.PreMarketTasks)*100) + 10 // +10表示有正在进行的任务

	case PhaseMorningSession:
		// 上午交易阶段：进度 = 已完成 / (盘前计划 + 上午计划)
		totalPlan := plan.PreMarketTasks + plan.MorningTasks
		if totalPlan == 0 {
			return 0
		}
		// 上午进行中，基础进度基于已完成比例
		baseProgress := int(float64(completedCount) / float64(totalPlan) * 100)
		// 如果有运行中的任务，增加10%
		if runningCount > 0 {
			baseProgress += 10
		}
		if baseProgress > 95 {
			baseProgress = 95
		}
		return baseProgress

	case PhaseLunch:
		// 午休阶段：上午的任务应该显示完成
		// 如果完成的任务数达到了上午计划，显示100%
		morningTotal := plan.PreMarketTasks + plan.MorningTasks
		if completedCount >= morningTotal {
			return 100
		}
		// 否则按比例显示
		if morningTotal == 0 {
			return 0
		}
		progress := int(float64(completedCount) / float64(morningTotal) * 100)
		if progress < 30 {
			progress = 30 // 午休时段至少显示30%，表示上午任务已启动
		}
		return progress

	case PhaseAfternoonSession:
		// 下午交易阶段：进度 = 已完成 / 全天计划
		totalPlan := plan.PreMarketTasks + plan.MorningTasks + plan.AfternoonTasks
		if totalPlan == 0 {
			return 0
		}
		baseProgress := int(float64(completedCount) / float64(totalPlan) * 100)
		if runningCount > 0 {
			baseProgress += 10
		}
		if baseProgress > 95 {
			baseProgress = 95
		}
		return baseProgress

	case PhasePostMarket:
		// 盘后阶段：进度 = 已完成 / 全天计划
		totalPlan := plan.PreMarketTasks + plan.MorningTasks + plan.AfternoonTasks + plan.PostMarketTasks
		if totalPlan == 0 {
			return 0
		}
		baseProgress := int(float64(completedCount) / float64(totalPlan) * 100)
		if runningCount > 0 {
			baseProgress += 5
		}
		if baseProgress > 98 {
			baseProgress = 98 // 盘后还没完全结束
		}
		return baseProgress

	case PhaseReview:
		// 复盘阶段：进度 = 已完成 / 全天计划
		totalPlan := plan.PreMarketTasks + plan.MorningTasks + plan.AfternoonTasks + plan.PostMarketTasks
		if totalPlan == 0 {
			return 0
		}
		return int(float64(completedCount) / float64(totalPlan) * 100)

	case PhaseClosed:
		// 休市：显示已完成比例
		if completedCount == 0 {
			return 0
		}
		totalPlan := plan.PreMarketTasks + plan.MorningTasks + plan.AfternoonTasks + plan.PostMarketTasks
		if totalPlan == 0 {
			return 0
		}
		return int(float64(completedCount) / float64(totalPlan) * 100)

	default:
		return 0
	}
}

// GetAgentWorkDetails 获取当日各个智能体的工作详情
func (l *AgentTaskLogger) GetAgentWorkDetails() ([]AgentWorkDetail, error) {
	if l.db == nil {
		return nil, nil
	}

	today := time.Now().Format("2006-01-02")

	// 获取当天的任务日志（容错处理，查询失败不阻断）
	var logs []AgentTaskLog
	if err := l.db.GetDB().
		Where("task_date = ?", today).
		Order("task_phase, task_order").
		Find(&logs).Error; err != nil {
		log.Printf("[AgentTaskLogger] Failed to query agent_task_logs: %v, using empty data", err)
		logs = []AgentTaskLog{}
	}

	// 获取当天的活动记录（容错处理）
	var activities []AgentActivity
	activityErr := l.db.GetDB().
		Where("DATE(timestamp) = ?", today).
		Order("timestamp DESC").
		Find(&activities).Error
	if activityErr != nil {
		// 尝试另一种日期格式
		todayStart, _ := time.Parse("2006-01-02", today)
		todayEnd := todayStart.Add(24 * time.Hour)
		if err2 := l.db.GetDB().
			Where("timestamp >= ? AND timestamp < ?", todayStart, todayEnd).
			Order("timestamp DESC").
			Find(&activities).Error; err2 != nil {
			log.Printf("[AgentTaskLogger] Failed to get activities: %v, using empty data", err2)
			activities = []AgentActivity{}
		}
	}

	// 预定义智能体角色
	roles := []string{"CIO", "PLANNER", "QUANT", "RISK", "TRADER"}

	// 按角色聚合任务日志
	tasksByRole := make(map[string][]AgentTaskLog)
	runningTasksByRole := make(map[string]*AgentTaskLog)
	completedCountByRole := make(map[string]int)
	failedTasksByRole := make(map[string][]AgentTaskLog)

	for _, log := range logs {
		role := log.AgentRole
		tasksByRole[role] = append(tasksByRole[role], log)
		if log.Status == "RUNNING" {
			runningTasksByRole[role] = &log
		} else if log.Status == "COMPLETED" {
			completedCountByRole[role]++
		} else if log.Status == "FAILED" {
			failedTasksByRole[role] = append(failedTasksByRole[role], log)
		}
	}

	// 按角色聚合活动
	activitiesByRole := make(map[string][]AgentActivity)
	for _, act := range activities {
		activitiesByRole[act.AgentRole] = append(activitiesByRole[act.AgentRole], act)
	}

	// 查询今日创建的投资方案（用于回填 PLANNER 今日任务日志，容错处理）
	var todayPlans []InvestmentPlan
	if err := l.db.GetDB().
		Where("DATE(created_at) = ?", today).
		Order("created_at ASC").
		Find(&todayPlans).Error; err != nil {
		log.Printf("[AgentTaskLogger] Failed to query today investment plans: %v", err)
		todayPlans = []InvestmentPlan{}
	}

	// 获取当前市场阶段
	currentPhase := getMarketPhase()

	// 构建结果
	var results []AgentWorkDetail
	for _, role := range roles {
		roleLogs := tasksByRole[role]
		roleActivities := activitiesByRole[role]
		runningTask := runningTasksByRole[role]

		// 获取该角色的每日任务计划
		dailyPlan := getDailyTaskPlan(role)

		// 统计完成的任务数和运行中的任务数
		completedCount := completedCountByRole[role]
		runningCount := 0
		if runningTask != nil {
			runningCount = 1
		}

		detail := AgentWorkDetail{
			Role:        role,
			State:       "待机中",
			CurrentTask: "无任务",
			Progress:    0,
			TodayTasks:  []string{},
			TaskCount:   len(roleLogs),
		}

		// 根据当前阶段设置状态和进度
		switch currentPhase {
		case PhasePreMarket:
			// 交易日盘前准备阶段（开盘前）
			detail.State = "准备中"
			detail.CurrentTask = "盘前准备，等待开盘"
			detail.Progress = calculateProgressByPhase(currentPhase, dailyPlan, completedCount, runningCount)

		case PhasePreOpen:
			// 盘前阶段
			detail.State = "工作中"
			if runningTask != nil {
				detail.CurrentTask = runningTask.TaskName
				detail.RunningTask = runningTask.TaskName
			} else {
				detail.CurrentTask = "执行盘前准备任务"
			}
			detail.Progress = calculateProgressByPhase(currentPhase, dailyPlan, completedCount, runningCount)

		case PhaseMorningSession:
			// 上午交易阶段
			if runningTask != nil {
				detail.State = "工作中"
				detail.CurrentTask = runningTask.TaskName
				detail.RunningTask = runningTask.TaskName
			} else if completedCount > 0 {
				detail.State = "监控中"
				detail.CurrentTask = "盘中例行监控"
			} else {
				detail.State = "监控中"
				detail.CurrentTask = "盘中监控中"
			}
			detail.Progress = calculateProgressByPhase(currentPhase, dailyPlan, completedCount, runningCount)

		case PhaseLunch:
			// 午休阶段
			if runningTask != nil {
				detail.State = "工作中"
				detail.CurrentTask = runningTask.TaskName
				detail.RunningTask = runningTask.TaskName
			} else if completedCount > 0 {
				// 上午有完成的任务，显示休息中（上午任务已完成）
				detail.State = "休息中"
				detail.CurrentTask = "午间休息 - 上午任务已完成"
			} else {
				detail.State = "休息中"
				detail.CurrentTask = "午间休息"
			}
			detail.Progress = calculateProgressByPhase(currentPhase, dailyPlan, completedCount, runningCount)

		case PhaseAfternoonSession:
			// 下午交易阶段
			if runningTask != nil {
				detail.State = "工作中"
				detail.CurrentTask = runningTask.TaskName
				detail.RunningTask = runningTask.TaskName
			} else if completedCount > 0 {
				detail.State = "监控中"
				detail.CurrentTask = "下午盘中监控"
			} else {
				detail.State = "监控中"
				detail.CurrentTask = "下午盘中监控"
			}
			detail.Progress = calculateProgressByPhase(currentPhase, dailyPlan, completedCount, runningCount)

		case PhasePostMarket:
			// 盘后阶段
			if runningTask != nil {
				detail.State = "执行中"
				detail.CurrentTask = runningTask.TaskName
				detail.RunningTask = runningTask.TaskName
			} else {
				detail.State = "工作中"
				detail.CurrentTask = "执行盘后任务"
			}
			detail.Progress = calculateProgressByPhase(currentPhase, dailyPlan, completedCount, runningCount)

		case PhaseReview:
			// 复盘阶段 (15:15 - 18:00)
			currentTime := time.Now()
			currentHour := currentTime.Hour()
			totalPlan := dailyPlan.PreMarketTasks + dailyPlan.MorningTasks + dailyPlan.AfternoonTasks + dailyPlan.PostMarketTasks

			// 判断是否所有任务已完成
			allTasksCompleted := completedCount >= totalPlan && totalPlan > 0

			// 16:00之前仍在执行盘后任务
			if currentHour < 16 {
				if runningTask != nil {
					// 有运行中的任务
					detail.State = "执行中"
					detail.CurrentTask = runningTask.TaskName
					detail.RunningTask = runningTask.TaskName
				} else if completedCount > 0 {
					// 有已完成的任务，显示正在监控/继续执行
					detail.State = "工作中"
					detail.CurrentTask = "执行盘后任务"
				} else {
					// 15:15-16:00之间，即使没有任务记录，也显示正在执行
					detail.State = "工作中"
					detail.CurrentTask = "执行盘后任务"
				}
			} else if allTasksCompleted || currentHour >= 17 {
				// 16:00之后或所有任务完成
				if completedCount > 0 {
					detail.State = "已完成"
					detail.CurrentTask = "今日任务已完成，等待下一交易日"
				} else {
					detail.State = "休息中"
					detail.CurrentTask = "等待任务启动"
				}
			} else if runningTask != nil {
				detail.State = "工作中"
				detail.CurrentTask = runningTask.TaskName
				detail.RunningTask = runningTask.TaskName
			} else if completedCount > 0 {
				// 有完成任务但未全部完成
				detail.State = "监控中"
				detail.CurrentTask = "盘后监控 - 等待复盘任务启动"
			} else {
				detail.State = "工作中"
				detail.CurrentTask = "执行复盘任务"
			}
			detail.Progress = calculateProgressByPhase(currentPhase, dailyPlan, completedCount, runningCount)

		case PhaseClosed:
			// 休市
			totalPlan := dailyPlan.PreMarketTasks + dailyPlan.MorningTasks + dailyPlan.AfternoonTasks + dailyPlan.PostMarketTasks
			allTasksCompleted := completedCount >= totalPlan && totalPlan > 0

			if allTasksCompleted || completedCount > 0 {
				detail.State = "已完成"
				detail.CurrentTask = "今日任务已完成，等待下一交易日"
			} else {
				detail.State = "休息中"
				detail.CurrentTask = "等待下一个交易日"
			}
			detail.Progress = calculateProgressByPhase(currentPhase, dailyPlan, completedCount, runningCount)

		default:
			detail.Progress = calculateProgressByPhase(currentPhase, dailyPlan, completedCount, runningCount)
		}

		// AI失败原因分类：存在失败/阻断任务时，用人类可读的原因覆盖默认状态
		if failed := failedTasksByRole[role]; len(failed) > 0 {
			// 优先展示阻断类原因（数据缺失/过期/风控阻断），否则展示具体失败原因
			blockedMsg := ""
			for _, f := range failed {
				if msg := classifyAgentBlockedReason(f.Errors); msg != "" {
					blockedMsg = msg
					break
				}
			}
			if blockedMsg != "" {
				detail.State = "已阻断"
				detail.CurrentTask = blockedMsg
			} else {
				detail.State = "异常"
				detail.CurrentTask = humanizeAgentError(failed[0].TaskName, failed[0].Errors)
			}
			detail.Progress = 0
		}

		// 添加今日任务列表（更详细的单条描述：阶段/序号/状态/任务名/摘要/交付物/起止时间/耗时）
		for _, log := range roleLogs {
			detail.TodayTasks = append(detail.TodayTasks, buildTaskDescription(&log))
		}

		// 补充 PLANNER 今日创建的投资方案记录（部分方案生成未写入 agent_task_logs，从 investment_plans 表回填）
		if role == "PLANNER" && len(todayPlans) > 0 {
			for _, plan := range todayPlans {
				detail.TodayTasks = append(detail.TodayTasks,
					fmt.Sprintf("[已完成] 生成投资方案：%s · 交付:%s", truncateText(plan.Name, 40), plan.CreatedAt.Format("15:04")))
			}
			detail.TaskCount += len(todayPlans)
			// 若此前无任何任务/活动记录，避免被“默认待机”分支覆盖
			if detail.State == "待机中" && len(roleLogs) == 0 && len(roleActivities) == 0 {
				detail.State = "工作中"
				detail.CurrentTask = "生成投资方案"
			}
		}

		// 如果没有任务日志但有活动记录，添加活动摘要
		if len(roleLogs) == 0 && len(roleActivities) > 0 {
			// 取最近5条活动
			limit := 5
			if len(roleActivities) < limit {
				limit = len(roleActivities)
			}
			for i := 0; i < limit; i++ {
				act := roleActivities[i]
				taskDesc := act.Title
				if act.Timestamp.IsZero() == false {
					taskDesc = fmt.Sprintf("[%s] %s", act.Timestamp.Format("15:04:05"), act.Title)
				}
				detail.TodayTasks = append(detail.TodayTasks, taskDesc)
			}
		}

		// 如果既没有任务也没有活动，且当前状态是默认的"待机中"
		if len(roleLogs) == 0 && len(roleActivities) == 0 && detail.State == "待机中" {
			if isTradingTime() {
				// 交易时段，即使没有任务记录也显示为工作中
				detail.State = "工作中"
				detail.CurrentTask = "执行例行监控任务"
				detail.Progress = 10
				// 根据角色显示不同的当前任务
				switch role {
				case "PLANNER":
					detail.CurrentTask = "盘中客户咨询与服务"
					detail.TodayTasks = []string{
						"监控客户咨询",
						"检查投资规划合规性",
						"处理客户请求",
					}
				case "CIO":
					detail.CurrentTask = "盘中监控与决策审批"
					detail.TodayTasks = []string{
						"监控市场动向",
						"评估持仓状态",
						"审批交易指令",
					}
				case "QUANT":
					detail.CurrentTask = "实时因子监控"
					detail.TodayTasks = []string{
						"实时监控启动",
						"捕捉Alpha信号",
						"筛选候选股票",
					}
				case "RISK":
					detail.CurrentTask = "盘中实时风控"
					detail.TodayTasks = []string{
						"VaR实时计算",
						"监控风险指标",
						"生成风险报告",
					}
				case "TRADER":
					detail.CurrentTask = "监控委托订单状态"
					detail.TodayTasks = []string{
						"监控委托订单",
						"执行交易操作",
						"记录交易日志",
					}
				default:
					detail.TodayTasks = []string{"执行例行监控任务"}
				}
			} else {
				detail.State = "休息中"
				detail.CurrentTask = "等待任务"
				detail.TodayTasks = []string{"今日无任务记录"}
			}
		}

		results = append(results, detail)
	}

	return results, nil
}

// buildTaskDescription 生成单条今日任务的详细描述文本，
// 包含阶段/序号、状态、任务名、摘要、交付物、起止时间与耗时，供前端工作明细展示。
func buildTaskDescription(log *AgentTaskLog) string {
	var b strings.Builder

	// 状态
	switch log.Status {
	case "RUNNING":
		b.WriteString("[执行中]")
	case "COMPLETED":
		b.WriteString("[已完成]")
	case "FAILED":
		b.WriteString("[失败]")
	case "PENDING":
		b.WriteString("[待执行]")
	default:
		b.WriteString("[已记录]")
	}

	// 阶段 + 序号
	if log.TaskPhase != "" {
		b.WriteString(" ")
		b.WriteString(taskPhaseLabel(log.TaskPhase))
		if log.TaskOrder > 0 {
			fmt.Fprintf(&b, "·第%d项", log.TaskOrder)
		}
	}

	// 任务名
	b.WriteString(" ")
	b.WriteString(log.TaskName)

	// 摘要（若存在且与任务名不同，截断至 60 字）
	if log.Summary != "" {
		s := truncateText(log.Summary, 60)
		if s != "" && s != log.TaskName {
			b.WriteString("：")
			b.WriteString(s)
		}
	}

	// 交付物
	if log.DeliverableName != "" {
		b.WriteString(" · 交付:")
		b.WriteString(truncateText(log.DeliverableName, 40))
	}

	// 起止时间与耗时
	if log.StartTime != nil {
		fmt.Fprintf(&b, " · %s", log.StartTime.Format("15:04"))
	}
	if log.Status == "COMPLETED" && log.EndTime != nil {
		fmt.Fprintf(&b, "-%s", log.EndTime.Format("15:04"))
	}
	if log.DurationMs > 0 {
		secs := float64(log.DurationMs) / 1000.0
		if secs >= 60 {
			fmt.Fprintf(&b, " · 耗时%d分%.0f秒", int(secs)/60, secs-float64(int(secs)/60)*60)
		} else {
			fmt.Fprintf(&b, " · 耗时%.1f秒", secs)
		}
	}
	if log.Errors != "" {
		b.WriteString(" · 原因:")
		b.WriteString(truncateText(log.Errors, 40))
	}

	if b.Len() == 0 {
		return log.TaskName
	}
	return b.String()
}

// taskPhaseLabel 阶段代码转中文标签
func taskPhaseLabel(phase string) string {
	switch phase {
	case "PRE_MARKET", "PRE_MARKETING":
		return "盘前"
	case "PRE_OPEN":
		return "竞价"
	case "MORNING_SESSION", "IN_MARKET":
		return "盘中"
	case "LUNCH":
		return "午间"
	case "AFTERNOON_SESSION":
		return "午盘"
	case "POST_MARKET":
		return "盘后"
	case "REVIEW":
		return "复盘"
	default:
		return phase
	}
}

// truncateText 截断长文本至指定长度（按 rune 计数，超长追加省略号）
func truncateText(s string, maxLen int) string {
	runes := []rune(strings.TrimSpace(s))
	if len(runes) <= maxLen {
		return string(runes)
	}
	return string(runes[:maxLen]) + "..."
}

// classifyAgentBlockedReason 识别阻断类失败原因（数据缺失/过期/风控阻断等）
// 返回空字符串表示不属于阻断类，需走通用失败原因分类
func classifyAgentBlockedReason(errText string) string {
	if errText == "" {
		return ""
	}
	lower := strings.ToLower(errText)
	switch {
	case strings.Contains(lower, "task_blocked"):
		return "任务被数据完整性阻断，等待数据恢复"
	case strings.Contains(lower, "数据已过期"):
		return "等待行情数据更新"
	case strings.Contains(lower, "无任何行情数据"):
		return "等待行情数据更新"
	case strings.Contains(lower, "行情数据库") && strings.Contains(lower, "未挂载"):
		return "行情数据库未挂载，等待数据源恢复"
	case strings.Contains(lower, "非交易时段") || strings.Contains(lower, "非交易时间"):
		return "当前非交易时段，任务暂停"
	case strings.Contains(lower, "风控") || strings.Contains(lower, "risk") && strings.Contains(lower, "阻断"):
		return "风控阻断，等待风险审查通过"
	case strings.Contains(lower, "decision") && strings.Contains(lower, "过期"):
		return "决策已过期，等待CIO重新决策"
	default:
		return ""
	}
}

// humanizeAgentError 将技术错误转换为人类可读的失败原因
func humanizeAgentError(taskName, errText string) string {
	if errText == "" {
		return fmt.Sprintf("%s 执行失败", taskName)
	}
	lower := strings.ToLower(errText)
	switch {
	case strings.Contains(lower, "401") || strings.Contains(lower, "authentication") || strings.Contains(lower, "api key"):
		return "AI服务认证失败，请检查API Key配置"
	case strings.Contains(lower, "timeout") || strings.Contains(lower, "超时"):
		return "AI服务响应超时，请稍后重试"
	case strings.Contains(lower, "网络") || strings.Contains(lower, "network") || strings.Contains(lower, "connection"):
		return "网络连接异常，请检查网络后重试"
	case strings.Contains(lower, "模型") || strings.Contains(lower, "model not found"):
		return "AI模型不可用，请检查模型配置"
	case strings.Contains(lower, "行情数据库"):
		return "行情数据源异常，请检查数据维护状态"
	case strings.Contains(lower, "llm") || strings.Contains(lower, "大模型"):
		return "大模型调用失败，请检查AI服务连接"
	default:
		// 截断过长的原始错误，保留前80个字符
		msg := strings.TrimSpace(errText)
		if len(msg) > 80 {
			msg = msg[:80] + "..."
		}
		return fmt.Sprintf("%s 执行失败：%s", taskName, msg)
	}
}

// MarketPhase 市场时段类型
type MarketPhase string

const (
	PhasePreMarket        MarketPhase = "PRE_MARKET"        // 盘前准备: 开盘前-9:15
	PhasePreOpen          MarketPhase = "PRE_OPEN"          // 盘前: 9:15-9:30
	PhaseMorningSession   MarketPhase = "MORNING_SESSION"   // 上午交易: 9:30-11:30
	PhaseLunch            MarketPhase = "LUNCH"             // 午休: 11:30-13:00
	PhaseAfternoonSession MarketPhase = "AFTERNOON_SESSION" // 下午交易: 13:00-15:00
	PhasePostMarket       MarketPhase = "POST_MARKET"       // 盘后: 15:00-15:15
	PhaseReview           MarketPhase = "REVIEW"            // 复盘: 15:15+
	PhaseClosed           MarketPhase = "CLOSED"            // 休市
)

// getMarketPhase 获取当前市场时段
func getMarketPhase() MarketPhase {
	now := time.Now()
	weekday := now.Weekday()

	// 周末休市
	if weekday == time.Saturday || weekday == time.Sunday {
		return PhaseClosed
	}

	hour := now.Hour()
	minute := now.Minute()
	currentMinutes := hour*60 + minute

	// 盘前准备: 开盘前 - 9:15 (555)
	if currentMinutes < 555 {
		return PhasePreMarket
	}

	// 盘前: 9:15 (555) - 9:30 (570)
	if currentMinutes >= 555 && currentMinutes < 570 {
		return PhasePreOpen
	}

	// 上午交易: 9:30 (570) - 11:30 (690)
	if currentMinutes >= 570 && currentMinutes < 690 {
		return PhaseMorningSession
	}

	// 午休: 11:30 (690) - 13:00 (780)
	if currentMinutes >= 690 && currentMinutes < 780 {
		return PhaseLunch
	}

	// 下午交易: 13:00 (780) - 15:00 (900)
	if currentMinutes >= 780 && currentMinutes < 900 {
		return PhaseAfternoonSession
	}

	// 盘后: 15:00 (900) - 15:15 (915)
	if currentMinutes >= 900 && currentMinutes < 915 {
		return PhasePostMarket
	}

	// 复盘: 15:15 (915) - 18:00 (1080)
	if currentMinutes >= 915 && currentMinutes < 1080 {
		return PhaseReview
	}

	return PhaseClosed
}

// isTradingTime 判断当前是否是交易时段
// 包含: 盘前、上午交易、下午交易、盘后
func isTradingTime() bool {
	phase := getMarketPhase()
	return phase == PhasePreOpen || phase == PhaseMorningSession ||
		phase == PhaseAfternoonSession || phase == PhasePostMarket
}
