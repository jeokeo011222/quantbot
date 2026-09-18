package main

import (
	"fmt"
	"time"
)

// GetCIOStatus 获取CIO和AI团队状态
func (a *App) GetCIOStatus() (interface{}, error) {
	if a.cioEngine == nil {
		return nil, fmt.Errorf("CIO Engine not initialized")
	}
	return a.cioEngine.GetStatus(), nil
}

// RunCIODailyCheck 运行CIO每日检查
func (a *App) RunCIODailyCheck() (interface{}, error) {
	if a.cioEngine == nil {
		return nil, fmt.Errorf("CIO Engine not initialized")
	}

	// 从 Portfolio 获取真实数据
	portfolioState := a.portfolioEngine.GetPortfolioState()
	portfolioValue := portfolioState["totalAssets"].(float64)
	dailyPnL := portfolioState["dailyPnL"].(float64)

	decision, err := a.cioEngine.RunDailyCheck(a.ctx, portfolioValue, dailyPnL)
	if err != nil {
		if a.auditService != nil {
			a.auditService.LogCIODailyReview(
				time.Now().Format("2006-01-02"),
				fmt.Sprintf("fail-%d", time.Now().UnixNano()),
				"CIO每日复盘执行失败",
				map[string]interface{}{"error": err.Error()},
			)
		}
		return nil, err
	}

	// 审计记录CIO每日复盘
	if a.auditService != nil {
		a.auditService.LogCIODailyReview(
			time.Now().Format("2006-01-02"),
			decision.DecisionID,
			fmt.Sprintf("决策: %s, 原因: %s", decision.Decision, decision.Reason),
			map[string]interface{}{
				"decision":        decision.Decision,
				"reason":          decision.Reason,
				"risk_approval":   decision.RiskApproval,
				"policy_status":   decision.PolicyStatus,
				"portfolio_value": portfolioValue,
				"daily_pnl":       dailyPnL,
			},
		)
	}

	return map[string]interface{}{
		"decision":      decision.Decision,
		"reason":        decision.Reason,
		"decision_id":   decision.DecisionID,
		"risk_approval": decision.RiskApproval,
		"policy_status": decision.PolicyStatus,
		"timestamp":     decision.Timestamp.Format(time.RFC3339),
	}, nil
}

// GetPendingWorkflows 获取待审批的决策单据
func (a *App) GetPendingWorkflows() (interface{}, error) {
	if a.cioEngine == nil {
		return nil, fmt.Errorf("CIO Engine not initialized")
	}
	return map[string]interface{}{
		"pending": a.cioEngine.GetPendingWorkflows(),
	}, nil
}

// ApproveWorkflow 审批决策单据
func (a *App) ApproveWorkflow(decisionID string, approver string, role string, approved bool, comment string) (interface{}, error) {
	if a.cioEngine == nil {
		return nil, fmt.Errorf("CIO Engine not initialized")
	}

	err := a.cioEngine.ApproveDecision(decisionID, approver, role, approved, comment)
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"success":    true,
		"decisionID": decisionID,
		"approved":   approved,
	}, nil
}

// ExecuteWorkflow 执行已批准的决策
func (a *App) ExecuteWorkflow(decisionID string) (interface{}, error) {
	if a.cioEngine == nil {
		return nil, fmt.Errorf("CIO Engine not initialized")
	}

	err := a.cioEngine.ExecuteApprovedDecision(a.ctx, decisionID)
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"success":    true,
		"decisionID": decisionID,
	}, nil
}

// GetWorkflowStatus 获取决策工作流状态
func (a *App) GetWorkflowStatus(decisionID string) (interface{}, error) {
	if a.cioEngine == nil {
		return nil, fmt.Errorf("CIO Engine not initialized")
	}
	return a.cioEngine.GetWorkflowStatus(decisionID), nil
}

// GetWorkflowStats 获取工作流统计
func (a *App) GetWorkflowStats() (interface{}, error) {
	if a.cioEngine == nil {
		return nil, fmt.Errorf("CIO Engine not initialized")
	}
	return a.cioEngine.GetWorkflowStats(), nil
}

// StartDailyWorkflow 启动每日投资决策工作流
func (a *App) StartDailyWorkflow() (interface{}, error) {
	if a.orchestratorAPI == nil {
		return nil, fmt.Errorf("Orchestrator not initialized")
	}
	return a.orchestratorAPI.StartDailyWorkflow()
}

// GetTaskStatus 获取任务状态
func (a *App) GetTaskStatus(taskID string) (interface{}, error) {
	if a.orchestratorAPI == nil {
		return nil, fmt.Errorf("Orchestrator not initialized")
	}
	return a.orchestratorAPI.GetTaskStatus(taskID)
}

// ListActiveTasks 列出活跃任务
func (a *App) ListActiveTasks() (interface{}, error) {
	if a.orchestratorAPI == nil {
		return nil, fmt.Errorf("Orchestrator not initialized")
	}
	return a.orchestratorAPI.ListActiveTasks()
}

// GetAgentTasks 获取智能体待办任务
func (a *App) GetAgentTasks(role string) (interface{}, error) {
	if a.orchestratorAPI == nil {
		return nil, fmt.Errorf("Orchestrator not initialized")
	}
	return a.orchestratorAPI.GetAgentTasks(role)
}

// GetAllAgentTasks 获取所有智能体待办任务
func (a *App) GetAllAgentTasks() (interface{}, error) {
	if a.orchestratorAPI == nil {
		return nil, fmt.Errorf("Orchestrator not initialized")
	}
	return a.orchestratorAPI.GetAllAgentTasks()
}

// GetOrchestratorStats 获取编排引擎统计
func (a *App) GetOrchestratorStats() (interface{}, error) {
	if a.orchestratorAPI == nil {
		return nil, fmt.Errorf("Orchestrator not initialized")
	}
	return a.orchestratorAPI.GetEngineStats()
}

// ListWorkflowTemplates 列出工作流模板
func (a *App) ListWorkflowTemplates() (interface{}, error) {
	if a.orchestratorAPI == nil {
		return nil, fmt.Errorf("Orchestrator not initialized")
	}
	return a.orchestratorAPI.ListTemplates()
}

// RunManualWorkflow 手动触发工作流
func (a *App) RunManualWorkflow(templateID string) (interface{}, error) {
	if a.orchestratorAPI == nil {
		return nil, fmt.Errorf("Orchestrator not initialized")
	}
	return a.orchestratorAPI.RunManualWorkflow(templateID)
}

// GetCIOJournal 获取CIO日志
func (a *App) GetCIOJournal(days int) (interface{}, error) {
	if a.cioEngine == nil {
		return nil, fmt.Errorf("CIO Engine not initialized")
	}

	if days <= 0 {
		days = 7
	}

	journal := a.cioEngine.GetCIOJournal(days)
	return map[string]interface{}{
		"days":    days,
		"entries": journal,
	}, nil
}

// GetClaimAccuracy 返回智能体（CIO）可证伪判断的命中率统计（P0），供前端/智能体量化自身判断质量。
func (a *App) GetClaimAccuracy() (interface{}, error) {
	if a.cioEngine == nil {
		return nil, fmt.Errorf("CIO Engine not initialized")
	}
	return a.cioEngine.ClaimAccuracy(), nil
}

// GetTaskStatusByRole 按角色获取今日任务状态
func (a *App) GetTaskStatusByRole() (interface{}, error) {
	if a.taskScheduler == nil {
		return nil, fmt.Errorf("Task Scheduler not initialized")
	}

	status := a.taskScheduler.GetTaskStatusByRole()
	return status, nil
}

// GetAgentWorkbench 获取智能体工作台信息
func (a *App) GetAgentWorkbench(agentID string) (interface{}, error) {
	if a.agentWorkflowSys == nil {
		return nil, fmt.Errorf("Agent Workflow System not initialized")
	}
	return a.agentWorkflowSys.GetAgentWorkbench(agentID)
}

// GetAllAgentWorkbenches 获取所有智能体工作台
func (a *App) GetAllAgentWorkbenches() (interface{}, error) {
	if a.agentWorkflowSys == nil {
		return nil, fmt.Errorf("Agent Workflow System not initialized")
	}
	return a.agentWorkflowSys.GetAllWorkbenches()
}

// ExecuteDailyWorkflow 执行日常投资工作流
func (a *App) ExecuteDailyWorkflow() (interface{}, error) {
	if a.agentWorkflowSys == nil {
		return nil, fmt.Errorf("Agent Workflow System not initialized")
	}
	return a.agentWorkflowSys.ExecuteDailyWorkflow()
}

// GetWorkflowTodayTasks 获取今日任务
func (a *App) GetWorkflowTodayTasks() (interface{}, error) {
	if a.agentWorkflowSys == nil {
		return nil, fmt.Errorf("Agent Workflow System not initialized")
	}
	return a.agentWorkflowSys.GetTodayTasks()
}

// GetWorkflowTaskSummary 获取任务汇总
func (a *App) GetWorkflowTaskSummary() (interface{}, error) {
	if a.agentWorkflowSys == nil {
		return nil, fmt.Errorf("Agent Workflow System not initialized")
	}
	return a.agentWorkflowSys.GetTaskSummary()
}

// GetWorkflowTodayDecisions 获取今日投资决策
func (a *App) GetWorkflowTodayDecisions() (interface{}, error) {
	if a.agentWorkflowSys == nil {
		return nil, fmt.Errorf("Agent Workflow System not initialized")
	}
	return a.agentWorkflowSys.GetTodayDecisions()
}

// ApproveDecisionByCIO CIO审批决策
func (a *App) ApproveDecisionByCIO(decisionID string) (interface{}, error) {
	if a.agentWorkflowSys == nil {
		return nil, fmt.Errorf("Agent Workflow System not initialized")
	}
	err := a.agentWorkflowSys.ApproveDecisionByCIO(decisionID)
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{"status": "approved"}, nil
}

// RiskReviewDecision 风控师审查决策
func (a *App) RiskReviewDecision(decisionID string, approved bool, conditions string) (interface{}, error) {
	if a.agentWorkflowSys == nil {
		return nil, fmt.Errorf("Agent Workflow System not initialized")
	}
	err := a.agentWorkflowSys.RiskReviewDecision(decisionID, approved, conditions)
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{"status": "reviewed"}, nil
}

// CreateInvestmentDecision 创建投资决策
func (a *App) CreateInvestmentDecision(agentID, decisionType, assetID, assetName string, currentWeight, targetWeight float64, reason string) (interface{}, error) {
	if a.agentWorkflowSys == nil {
		return nil, fmt.Errorf("Agent Workflow System not initialized")
	}
	return a.agentWorkflowSys.CreateInvestmentDecision(agentID, decisionType, assetID, assetName, currentWeight, targetWeight, reason)
}

// GetWorkflowTeamMembers 获取团队成员
func (a *App) GetWorkflowTeamMembers() (interface{}, error) {
	if a.agentWorkflowSys == nil {
		return nil, fmt.Errorf("Agent Workflow System not initialized")
	}
	return a.agentWorkflowSys.GetTeamMembers()
}

// GetAgentPermissions 获取智能体权限
func (a *App) GetAgentPermissions(agentID string) (interface{}, error) {
	if a.agentWorkflowSys == nil {
		return nil, fmt.Errorf("Agent Workflow System not initialized")
	}
	return a.agentWorkflowSys.GetAgentPermissions(agentID)
}

// GetWorkflowAgentTasks 获取智能体任务（新系统）
func (a *App) GetWorkflowAgentTasks(agentID string) (interface{}, error) {
	if a.agentWorkflowSys == nil {
		return nil, fmt.Errorf("Agent Workflow System not initialized")
	}
	return a.agentWorkflowSys.GetAgentTasks(agentID)
}

// StartWorkflow 手动启动工作流
func (a *App) StartWorkflow(workflowType string) (interface{}, error) {
	if a.agentWorkflowSys == nil {
		return nil, fmt.Errorf("Agent Workflow System not initialized")
	}
	return a.agentWorkflowSys.StartWorkflow(workflowType)
}

// GetActiveWorkflows 获取活跃的工作流
func (a *App) GetActiveWorkflows() (interface{}, error) {
	if a.agentWorkflowSys == nil {
		return nil, fmt.Errorf("Agent Workflow System not initialized")
	}
	return a.agentWorkflowSys.GetActiveWorkflows()
}

// GetWorkflowSystemStatus 获取系统状态
func (a *App) GetWorkflowSystemStatus() (interface{}, error) {
	if a.agentWorkflowSys == nil {
		return nil, fmt.Errorf("Agent Workflow System not initialized")
	}
	return a.agentWorkflowSys.GetSystemStatus()
}

// PublishWorkflowEvent 发布自定义事件
func (a *App) PublishWorkflowEvent(eventType, source, description string, payload map[string]interface{}) (interface{}, error) {
	if a.agentWorkflowSys == nil {
		return nil, fmt.Errorf("Agent Workflow System not initialized")
	}
	return a.agentWorkflowSys.PublishCustomEvent(eventType, source, description, payload)
}
