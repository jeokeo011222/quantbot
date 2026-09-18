package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"runtime/debug"
	"time"

	"github.com/quantpilot/quantpilot/internal/plannerhost"
	"github.com/quantpilot/quantpilot/internal/port"
	brainhost "github.com/quantpilot/quantpilot/internal/brainhost"
	"github.com/quantpilot/quantpilot/internal/data"
	"github.com/quantpilot/quantpilot/internal/screener"
	"github.com/quantpilot/quantpilot/internal/transparency"
)

// GetPlannerState 获取规划状态
func (a *App) GetPlannerState() (interface{}, error) {
	if a.plannerAgent == nil {
		return nil, fmt.Errorf("Planner not initialized")
	}

	profile, err := a.plannerAgent.GetProfile()
	if err != nil {
		return nil, err
	}

	currentPlan, _ := a.plannerAgent.GetCurrentPlan()

	state := map[string]interface{}{
		"step":                profile.CurrentStep,
		"progress":            profile.StepProgress,
		"profileCompleteness": profile.ProfileCompleteness,
		"hasActivePlan":       currentPlan != nil,
		"hasMandate":          false,
		"hasPortfolio":        false,
	}

	if currentPlan != nil {
		state["hasMandate"] = a.plannerAgent.GetPlanMandate(currentPlan) != nil
		state["hasPortfolio"] = a.plannerAgent.GetPlanConstruction(currentPlan) != nil
	}

	return state, nil
}

// GetProfile 获取投资者画像
func (a *App) GetProfile() (interface{}, error) {
	if a.plannerAgent == nil {
		return nil, fmt.Errorf("Planner not initialized")
	}

	profile, err := a.plannerAgent.GetProfile()
	if err != nil {
		return nil, err
	}

	response := map[string]interface{}{
		"id":                   profile.ID,
		"capital":              profile.Capital,
		"currency":             profile.Currency,
		"investmentHorizon":    profile.InvestmentHorizon,
		"investmentObjective":  profile.InvestmentObjective,
		"riskTolerance":        profile.RiskTolerance,
		"drawdownTolerance":    profile.DrawdownTolerance,
		"lossTolerance":        profile.LossTolerance,
		"liquidityRequirement": profile.LiquidityRequirement,
		"tradingFrequency":     profile.TradingFrequency,
		"marketPreference":     profile.MarketPreference,
		"profileCompleteness":  profile.ProfileCompleteness,
		"currentStep":          profile.CurrentStep,
		"profile":              json.RawMessage(profile.ProfileJSON),
	}

	return response, nil
}

// SaveProfileAnswers 保存选择题答案到投资者画像
func (a *App) SaveProfileAnswers(answers map[string]string) error {
	if a.plannerAgent == nil {
		return fmt.Errorf("Planner not initialized")
	}

	err := a.plannerAgent.SaveProfileAnswers(answers)

	if a.auditService != nil {
		a.auditService.LogAuditEvent(
			data.AuditEventConfigChange,
			"保存投资者画像答案",
			"planner",
			"profile_answers",
			"user",
			"user",
			"success",
			map[string]interface{}{
				"answer_count": len(answers),
				"timestamp":    time.Now().Format(time.RFC3339),
			},
		)
	}

	return err
}

// StartInterview 开始/继续访谈
func (a *App) StartInterview(userInput string) (interface{}, error) {
	if a.plannerAgent == nil {
		return nil, fmt.Errorf("Planner not initialized")
	}

	// 检查 AI API Key 是否已配置
	cfg := a.configManager.GetConfig()
	log.Printf("[StartInterview] AIAPIKey length: %d, AIModel: %s, AIBaseURL: %s", len(cfg.AIAPIKey), cfg.AIModel, cfg.AIBaseURL)

	if cfg.AIAPIKey == "" {
		log.Printf("[StartInterview] API Key is empty, returning fallback response")
		return map[string]interface{}{
			"message":       "AI 功能尚未配置。请先在【设置】页面配置 AI API Key（推荐使用 DeepSeek API Key）。",
			"next_question": "⚠️ 未配置 AI API Key。请前往【设置】页面配置后使用完整 AI 功能。当前使用基础模式。",
			"is_complete":   false,
			"suggestions":   []string{"我有5万元，希望稳健一点", "我准备长期投资", "我可以接受一定波动"},
			"apiKeyMissing": true,
		}, nil
	}

	log.Printf("[StartInterview] Calling plannerAgent.StartInterview with input: %s", userInput)
	response, err := a.plannerAgent.StartInterview(userInput)
	if err != nil {
		log.Printf("[StartInterview] Error from plannerAgent: %v", err)
		return nil, err
	}

	log.Printf("[StartInterview] Response from plannerAgent: is_complete=%v, next_question=%s", response.IsComplete, response.NextQuestion)
	return response, nil
}

// GetActiveConversation 获取当前对话
func (a *App) GetActiveConversation() (interface{}, error) {
	if a.plannerAgent == nil {
		return nil, fmt.Errorf("Planner not initialized")
	}

	conv, err := a.plannerAgent.GetActiveConversation()
	if err != nil {
		return map[string]interface{}{
			"conversationId": "",
			"messages":       []interface{}{},
			"status":         "no_conversation",
		}, nil
	}

	messages, _ := a.plannerAgent.GetConversationMessages(conv.ConversationID)

	var msgResponses []interface{}
	for _, msg := range messages {
		msgResponses = append(msgResponses, map[string]interface{}{
			"id":          msg.ID,
			"role":        msg.Role,
			"content":     msg.Content,
			"messageType": msg.MessageType,
			"createdAt":   msg.CreatedAt.Format(time.RFC3339),
		})
	}

	return map[string]interface{}{
		"id":             conv.ID,
		"conversationId": conv.ConversationID,
		"title":          conv.Title,
		"status":         conv.Status,
		"currentStep":    conv.CurrentStep,
		"messageCount":   conv.MessageCount,
		"messages":       msgResponses,
	}, nil
}

// appTracker 安全获取透明度追踪器：harness 未初始化（如 DuckDB 打开失败）时，
// 返回一个兜底 Tracker，避免调用方 nil 解引用导致启动 panic。
func (a *App) appTracker() *transparency.Tracker {
	if a.harnessApp != nil {
		return a.harnessApp.GetTracker()
	}
	if a.fallbackTracker == nil {
		a.fallbackTracker = transparency.NewTracker()
	}
	return a.fallbackTracker
}

// runGeneratePlan 执行投资方案生成主体（含全市场选股），供异步任务调用。
// 全市场选股对数千只股票做因子打分耗时较长，故不再同步阻塞前端。
func (a *App) runGeneratePlan() (result interface{}, retErr error) {
	// 全市场选股在后台 goroutine 中执行，任何未捕获 panic 都会使整个进程退出。
	// 这里兜底恢复为错误，避免程序自动退出，并输出调用栈便于定位。
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[GeneratePlan] 🚨 PANIC 已捕获: %v\n%s", r, debug.Stack())
			a.setGenProgress(fmt.Sprintf("生成方案时发生异常：%v", r))
			result = nil
			retErr = fmt.Errorf("生成投资方案时发生内部异常: %v", r)
		}
	}()

	if a.plannerAgent == nil {
		return nil, fmt.Errorf("Planner not initialized")
	}

	// 记录智能体任务日志（agent_task_logs），使"实时活动"页任务日志与 Agent 工作明细能展示本次投资规划师活动。
	today := time.Now().Format("2006-01-02")
	var taskLog *data.AgentTaskLog
	if a.agentTaskLogger != nil {
		taskLog = a.agentTaskLogger.LogTaskStart(today, "PRE_MARKET", string(port.RolePlanner), "投资规划 - 生成投资方案", 1)
	}
	// 记录失败到任务日志（供上方提前 return 复用）
	failLog := func(err error) {
		if taskLog != nil {
			a.agentTaskLogger.LogTaskFailed(taskLog.ID, err.Error())
		}
	}

	a.setGenProgress("正在全市场选股并生成投资方案（约需一至数分钟），请稍候…")
	plan, err := a.plannerAgent.GeneratePlan()
	if err != nil {
		failLog(err)
		if a.auditService != nil {
			a.auditService.LogAuditEvent(
				data.AuditEventLiveActivity,
				"生成投资计划失败",
				"plan",
				"generate_plan",
				"user",
				"user",
				"failed",
				map[string]interface{}{
					"error": err.Error(),
				},
			)
		}
		return nil, err
	}

	// 生成成功：回写智能体任务日志完成状态
	if taskLog != nil {
		a.agentTaskLogger.LogTaskComplete(
			taskLog.ID,
			"INVESTMENT_PLAN",
			fmt.Sprintf("投资方案: %s", plan.Name),
			map[string]interface{}{
				"plan_id":    plan.PlanID,
				"plan_name":  plan.Name,
				"risk_level": plan.RiskLevel,
			},
			fmt.Sprintf("生成投资方案: %s（风险等级：%s）", plan.Name, plan.RiskLevel),
		)
	}

	if a.auditService != nil {
		a.auditService.LogAuditEvent(
			data.AuditEventPlanner,
			fmt.Sprintf("生成投资计划: %s", plan.Name),
			"plan",
			plan.PlanID,
			"user",
			"user",
			"success",
			map[string]interface{}{
				"plan_id":    plan.PlanID,
				"plan_name":  plan.Name,
				"risk_level": plan.RiskLevel,
			},
		)
	}

	candidates, _ := a.plannerAgent.GetPlanCandidates(plan.PlanID)
	mandate := a.plannerAgent.GetPlanMandate(plan)
	construction := a.plannerAgent.GetPlanConstruction(plan)

	// 根据投资规划自动调用选股引擎，生成股票池供智能体选用
	a.setGenProgress("方案结构已生成，正在根据投资规划筛选股票池…")
	stockPool, poolErr := a.generatePlanStockPool(plan)
	if poolErr != nil {
		log.Printf("[GeneratePlan] 自动生成股票池失败: %v", poolErr)
	} else if stockPool != nil && a.sqliteManager != nil {
		// 将股票池信息持久化到计划 JSON，刷新后可重新读取
		var planPayload map[string]interface{}
		if err := json.Unmarshal([]byte(plan.PlanJSON), &planPayload); err == nil {
			planPayload["stockPool"] = stockPool
			if planBytes, err := json.Marshal(planPayload); err == nil {
				a.sqliteManager.GetDB().Model(&data.InvestmentPlan{}).
					Where("plan_id = ?", plan.PlanID).
					Update("plan_json", string(planBytes))
			}
		}
	}

	var candidateResponses []interface{}
	for _, c := range candidates {
		candidateResponses = append(candidateResponses, map[string]interface{}{
			"id":                 c.ID,
			"candidateId":        c.CandidateID,
			"name":               c.Name,
			"label":              c.Label,
			"description":        c.Description,
			"expectedReturn":     c.ExpectedReturn,
			"expectedVolatility": c.ExpectedVolatility,
			"maxDrawdown":        c.MaxDrawdown,
			"sharpe":             c.Sharpe,
			"calmar":             c.Calmar,
			"robustnessScore":    c.RobustnessScore,
			"riskScore":          c.RiskScore,
			"riskOsStatus":       c.RiskOSStatus,
			"status":             c.Status,
		})
	}

	return map[string]interface{}{
		"id":               plan.ID,
		"planId":           plan.PlanID,
		"name":             plan.Name,
		"objective":        plan.Objective,
		"riskLevel":        plan.RiskLevel,
		"targetReturn":     plan.TargetReturn,
		"targetVolatility": plan.TargetVolatility,
		"maxDrawdown":      plan.MaxDrawdown,
		"strategyType":     plan.StrategyType,
		"status":           plan.Status,
		"mandate":          mandate,
		"construction":     construction,
		"candidates":       candidateResponses,
		"stockPool":        stockPool,
		"createdAt":        plan.CreatedAt.Format(time.RFC3339),
	}, nil
}

// setGenProgress 更新投资方案生成进度（供异步 goroutine 调用，线程安全），并输出日志便于观测
func (a *App) setGenProgress(msg string) {
	a.genPlanMu.Lock()
	a.genPlanProgress = msg
	a.genPlanMu.Unlock()
	log.Printf("[GeneratePlan] %s", msg)
}

// StartGeneratePlan 启动投资方案异步生成任务。
// 生成方案会触发全市场选股（数千只股票因子打分，耗时长），故后台 goroutine 执行，
// 前端通过 GetGeneratePlanProgress 轮询进度，避免同步等待看似卡死。
// 该生成动作作为"投资规划师（Planner）任务"通过编排引擎执行，与 AI 团队页任务状态关联。
func (a *App) StartGeneratePlan() (interface{}, error) {
	a.genPlanMu.Lock()
	if a.genPlanRunning {
		progress := a.genPlanProgress
		a.genPlanMu.Unlock()
		return map[string]interface{}{"running": true, "progress": progress}, nil
	}
	a.genPlanRunning = true
	a.genPlanDone = false
	a.genPlanErr = nil
	a.genPlanResult = nil
	a.genPlanProgress = "正在启动投资方案生成…"
	a.genPlanMu.Unlock()

	failStart := func(err error) (interface{}, error) {
		a.genPlanMu.Lock()
		a.genPlanErr = err
		a.genPlanDone = true
		a.genPlanRunning = false
		a.genPlanMu.Unlock()
		return nil, err
	}

	// 创建投资规划编排任务并启动；InvestmentPlanHandler 在"投资方案规划"步骤
	// 于后台 goroutine 中调用 runGeneratePlan，实际完成方案生成与进度回写。
	// 使用非幂等 CreateUserTask：用户手动触发，同日可多次重新生成。
	ctx := context.Background()
	task, err := a.orchestratorEngine.CreateUserTask("investment_planning", "user",
		"投资规划 - 生成投资方案",
		"投资规划师生成投资方案（PROPOSE）")
	if err != nil {
		return failStart(err)
	}
	if err := a.orchestratorEngine.StartTask(ctx, task.ID); err != nil {
		return failStart(err)
	}
	if err := a.orchestratorEngine.ExecuteNextStep(ctx, task.ID); err != nil {
		return failStart(err)
	}

	return map[string]interface{}{
		"running":  true,
		"task_id":  task.ID,
		"progress": "正在启动投资方案生成…",
	}, nil
}

// GetGeneratePlanProgress 查询投资方案异步生成任务的状态与进度
func (a *App) GetGeneratePlanProgress() (interface{}, error) {
	a.genPlanMu.Lock()
	defer a.genPlanMu.Unlock()

	resp := map[string]interface{}{
		"running":  a.genPlanRunning,
		"done":     a.genPlanDone,
		"progress": a.genPlanProgress,
	}
	if a.genPlanErr != nil {
		resp["error"] = a.genPlanErr.Error()
		resp["result"] = nil
	} else {
		resp["error"] = nil
		resp["result"] = a.genPlanResult
	}
	return resp, nil
}

// waitGeneratePlan 阻塞等待异步生成任务完成（供同步调用方使用）
func (a *App) waitGeneratePlan() (interface{}, error) {
	for {
		a.genPlanMu.Lock()
		done := a.genPlanDone
		err := a.genPlanErr
		res := a.genPlanResult
		a.genPlanMu.Unlock()
		if !done {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		if err != nil {
			return nil, err
		}
		return res, nil
	}
}

// generatePlanTask 在一个后台 goroutine 中运行投资方案生成任务（供 GeneratePlan 同步入口复用）
func (a *App) generatePlanTask() {
	defer appRecover("投资计划后台生成")
	result, err := a.runGeneratePlan()
	a.genPlanMu.Lock()
	if err != nil {
		a.genPlanErr = err
	} else if r, ok := result.(map[string]interface{}); ok {
		a.genPlanResult = r
	}
	a.genPlanDone = true
	a.genPlanRunning = false
	a.genPlanMu.Unlock()
}

// GeneratePlan 生成投资计划（同步入口：触发异步任务并等待结果，保持向后兼容）
func (a *App) GeneratePlan() (interface{}, error) {
	a.genPlanMu.Lock()
	if !a.genPlanRunning && !a.genPlanDone {
		a.genPlanRunning = true
		a.genPlanDone = false
		a.genPlanErr = nil
		a.genPlanResult = nil
		a.genPlanProgress = "正在启动投资方案生成…"
		a.genPlanMu.Unlock()
		go a.generatePlanTask()
		return a.waitGeneratePlan()
	}
	a.genPlanMu.Unlock()
	return a.waitGeneratePlan()
}

// planTotalAssetsForFilter 判断当前投资组合资金是否<50万，用于中小资金时收缩选股宇宙（剔除 ST/北交所/创业板/科创板）
func planTotalAssetsForFilter(plan *port.InvestmentPlan, plannerAgent *planner.Planner) bool {
	if plan == nil || plannerAgent == nil {
		return false
	}
	construction := plannerAgent.GetPlanConstruction(plan)
	return construction != nil && construction.TotalAssets > 0 && construction.TotalAssets < 500000
}

// generatePlanStockPool 根据投资计划自动调用选股引擎，生成股票池供智能体选用
func (a *App) generatePlanStockPool(plan *port.InvestmentPlan) (interface{}, error) {
	if a.screenerService == nil || a.tradeablePool == nil {
		return nil, fmt.Errorf("选股引擎或股票池未初始化")
	}

	// 根据投资计划风险等级映射选股策略模板
	strategyID := "balanced"
	switch plan.RiskLevel {
	case "conservative":
		strategyID = "defensive"
	case "growth":
		strategyID = "growth"
	default:
		strategyID = "balanced"
	}

	req := screener.ScreeningRequest{
		StrategyID:   strategyID,
		Market:       "all",
		MaxResults:   30,
		MinScore:     0,
		SmallCapital: planTotalAssetsForFilter(plan, a.plannerAgent),
	}

	// 传递用户画像，启用智能因子组合
	if a.plannerAgent != nil {
		profile, err := a.plannerAgent.GetProfile()
		if err == nil && profile != nil {
			req.InvestorProfile = brainhost.DataProfileFromPort(profile)
		}
	}

	result, err := a.screenerService.ScreenStock(req)
	if err != nil {
		return nil, err
	}

	// 携带生命周期元数据：关联投资方案、因子版本、市场数据日期
	now := time.Now()
	meta := screener.StockPoolMeta{
		PlanID:        plan.PlanID,
		FactorVersion: strategyID + "-" + now.Format("20060102"),
		MarketDate:    &now,
	}
	count, err := a.tradeablePool.SubmitScreenerResultWithMeta(&result, "planner", "投资规划引擎", meta)
	if err != nil {
		return nil, err
	}

	if a.auditService != nil {
		a.auditService.LogAuditEvent(
			data.AuditEventPlanner,
			fmt.Sprintf("投资规划自动生成股票池: 策略=%s, 计划=%s, 股票数=%d", strategyID, plan.Name, count),
			"tradeable_pool", plan.PlanID,
			"planner", "投资规划引擎",
			"success",
			map[string]interface{}{
				"plan_id":         plan.PlanID,
				"risk_level":      plan.RiskLevel,
				"strategy_id":     strategyID,
				"submitted_count": count,
			},
		)
	}

	return map[string]interface{}{
		"strategyId":     strategyID,
		"submittedCount": count,
		"totalResults":   len(result.Results),
		"generatedAt":    result.GeneratedAt,
		"profileSummary": result.ProfileSummary,
	}, nil
}

// buildPlanDetail 构建投资方案完整响应（含任务书、配置、候选、股票池）
func (a *App) buildPlanDetail(plan *port.InvestmentPlan) map[string]interface{} {
	candidates, _ := a.plannerAgent.GetPlanCandidates(plan.PlanID)
	mandate := a.plannerAgent.GetPlanMandate(plan)
	construction := a.plannerAgent.GetPlanConstruction(plan)

	var candidateResponses []interface{}
	for _, c := range candidates {
		candidateResponses = append(candidateResponses, map[string]interface{}{
			"id":                 c.ID,
			"candidateId":        c.CandidateID,
			"name":               c.Name,
			"label":              c.Label,
			"description":        c.Description,
			"expectedReturn":     c.ExpectedReturn,
			"expectedVolatility": c.ExpectedVolatility,
			"maxDrawdown":        c.MaxDrawdown,
			"sharpe":             c.Sharpe,
			"calmar":             c.Calmar,
			"robustnessScore":    c.RobustnessScore,
			"riskScore":          c.RiskScore,
			"riskOsStatus":       c.RiskOSStatus,
			"status":             c.Status,
		})
	}

	response := a.buildPlanSummary(plan)
	response["mandate"] = mandate
	response["construction"] = construction
	response["candidates"] = candidateResponses
	return response
}

// buildPlanSummary 构建投资方案列表摘要
func (a *App) buildPlanSummary(plan *port.InvestmentPlan) map[string]interface{} {
	var stockPool interface{}
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(plan.PlanJSON), &payload); err == nil {
		if sp, ok := payload["stockPool"]; ok {
			stockPool = sp
		}
	}

	return map[string]interface{}{
		"id":               plan.ID,
		"planId":           plan.PlanID,
		"name":             plan.Name,
		"objective":        plan.Objective,
		"riskLevel":        plan.RiskLevel,
		"targetReturn":     plan.TargetReturn,
		"targetVolatility": plan.TargetVolatility,
		"maxDrawdown":      plan.MaxDrawdown,
		"strategyType":     plan.StrategyType,
		"status":           plan.Status,
		"stockPool":        stockPool,
		"createdAt":        plan.CreatedAt.Format(time.RFC3339),
	}
}

// GetPlans 获取当前用户所有投资方案（我的投资方案）
func (a *App) GetPlans() (interface{}, error) {
	if a.plannerAgent == nil {
		return nil, fmt.Errorf("Planner not initialized")
	}

	plans, err := a.plannerAgent.ListPlans()
	if err != nil {
		return nil, err
	}

	result := make([]interface{}, 0, len(plans))
	for i := range plans {
		result = append(result, a.buildPlanSummary(&plans[i]))
	}
	return result, nil
}

// GetPlan 获取指定投资方案详情（我的投资方案查看）
func (a *App) GetPlan(planID string) (interface{}, error) {
	if a.plannerAgent == nil {
		return nil, fmt.Errorf("Planner not initialized")
	}

	plan, err := a.plannerAgent.GetPlan(planID)
	if err != nil {
		return nil, err
	}

	return a.buildPlanDetail(plan), nil
}

// GetCurrentPlan 获取当前投资计划
func (a *App) GetCurrentPlan() (interface{}, error) {
	if a.plannerAgent == nil {
		return nil, fmt.Errorf("Planner not initialized")
	}

	plan, err := a.plannerAgent.GetCurrentPlan()
	if err != nil {
		return nil, err
	}

	return a.buildPlanDetail(plan), nil
}

// GeneratePlanStockPool 为当前投资计划手动触发选股引擎，生成股票池供智能体选用
func (a *App) GeneratePlanStockPool() (interface{}, error) {
	if a.plannerAgent == nil {
		return nil, fmt.Errorf("Planner not initialized")
	}

	plan, err := a.plannerAgent.GetCurrentPlan()
	if err != nil {
		return nil, err
	}

	stockPool, err := a.generatePlanStockPool(plan)
	if err != nil {
		return nil, err
	}

	// 将股票池信息持久化到计划 JSON
	if a.sqliteManager != nil {
		var planPayload map[string]interface{}
		if err := json.Unmarshal([]byte(plan.PlanJSON), &planPayload); err == nil {
			planPayload["stockPool"] = stockPool
			if planBytes, err := json.Marshal(planPayload); err == nil {
				plan.PlanJSON = string(planBytes) // 同步更新内存，保证返回结果包含股票池
				a.sqliteManager.GetDB().Model(&data.InvestmentPlan{}).
					Where("plan_id = ?", plan.PlanID).
					Update("plan_json", string(planBytes))
			}
		}
	}

	// 返回更新后的计划（含股票池）
	return a.buildPlanDetail(plan), nil
}

// ApprovePlan 批准投资计划
func (a *App) ApprovePlan(planID string, acknowledged bool) error {
	if a.plannerAgent == nil {
		return fmt.Errorf("Planner not initialized")
	}

	err := a.plannerAgent.ApprovePlan(planID, acknowledged)

	if a.auditService != nil {
		result := "success"
		if err != nil {
			result = "failed"
		}
		a.auditService.LogAuditEvent(
			data.AuditEventApproval,
			fmt.Sprintf("批准投资计划: %s", planID),
			"plan",
			planID,
			"user",
			"",
			result,
			map[string]interface{}{
				"plan_id":      planID,
				"acknowledged": acknowledged,
			},
		)
	}

	return err
}

// RejectPlan 拒绝投资计划（CIO审核拒绝接口）
func (a *App) RejectPlan(planID string, reason string) error {
	if a.plannerAgent == nil {
		return fmt.Errorf("Planner not initialized")
	}

	err := a.plannerAgent.RejectPlan(planID, reason)

	if a.auditService != nil {
		result := "success"
		if err != nil {
			result = "failed"
		}
		a.auditService.LogAuditEvent(
			data.AuditEventApproval,
			fmt.Sprintf("CIO拒绝投资计划: %s, 原因: %s", planID, reason),
			"plan",
			planID,
			"cio",
			"cio_001",
			result,
			map[string]interface{}{
				"plan_id": planID,
				"reason":  reason,
			},
		)
	}

	return err
}

// CIOReviewPlan CIO审核投资计划（对外API）
func (a *App) CIOReviewPlan(planID string) (interface{}, error) {
	if a.cioEngine == nil {
		return nil, fmt.Errorf("CIO Engine not initialized")
	}

	approved, reason, err := a.cioEngine.ReviewInvestmentPlan(a.ctx, planID)
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"plan_id":  planID,
		"approved": approved,
		"reason":   reason,
		"reviewed": true,
	}, nil
}

// CIOReviewPendingPlans CIO批量审核待处理投资计划
func (a *App) CIOReviewPendingPlans() (interface{}, error) {
	if a.cioEngine == nil {
		return nil, fmt.Errorf("CIO Engine not initialized")
	}

	approvedCount, err := a.cioEngine.ReviewPendingPlans(a.ctx)
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"approved_count": approvedCount,
		"reviewed":       true,
	}, nil
}

// ResetPlanner 重置规划器
func (a *App) ResetPlanner() (interface{}, error) {
	if a.plannerAgent == nil {
		return nil, fmt.Errorf("Planner not initialized")
	}

	// 获取当前画像并重置进度
	profile, err := a.plannerAgent.GetProfile()
	if err != nil {
		return nil, err
	}

	profile.CurrentStep = "WELCOME"
	profile.StepProgress = 0
	profile.ProfileCompleteness = 0
	profile.ProfileJSON = "{}"

	if err := a.plannerAgent.UpdateProfile(profile); err != nil {
		return nil, err
	}

	if a.auditService != nil {
		a.auditService.LogAuditEvent(
			data.AuditEventConfigChange,
			"重置规划器",
			"planner",
			"reset_planner",
			"user",
			"user",
			"success",
			map[string]interface{}{
				"timestamp": time.Now().Format(time.RFC3339),
			},
		)
	}

	return map[string]interface{}{
		"success": true,
		"message": "规划器已重置",
	}, nil
}
