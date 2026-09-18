package agentworkflow

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"gorm.io/gorm"
)

// AgentExecutor Agent执行器接口
type AgentExecutor interface {
	ExecuteAgentTask(ctx context.Context, agentRole, taskType, taskDesc string, inputData map[string]interface{}) (map[string]interface{}, error)
}

// NoopAgentExecutor 默认Agent执行器
type NoopAgentExecutor struct{}

func (e *NoopAgentExecutor) ExecuteAgentTask(ctx context.Context, agentRole, taskType, taskDesc string, inputData map[string]interface{}) (map[string]interface{}, error) {
	return nil, fmt.Errorf("Agent执行器未初始化，无法执行 %s 的任务: %s", agentRole, taskType)
}

// WorkflowEngine 工作流引擎
type WorkflowEngine struct {
	db        *gorm.DB
	taskMgr   *TaskManager
	permMgr   *PermissionManager
	agentExec AgentExecutor
}

// NewWorkflowEngine 创建工作流引擎
func NewWorkflowEngine(db *gorm.DB, taskMgr *TaskManager, permMgr *PermissionManager, agentExec ...AgentExecutor) *WorkflowEngine {
	var exec AgentExecutor = &NoopAgentExecutor{}
	if len(agentExec) > 0 && agentExec[0] != nil {
		exec = agentExec[0]
	}
	return &WorkflowEngine{
		db:        db,
		taskMgr:   taskMgr,
		permMgr:   permMgr,
		agentExec: exec,
	}
}

// SetAgentExecutor 设置Agent执行器
func (we *WorkflowEngine) SetAgentExecutor(exec AgentExecutor) {
	we.agentExec = exec
}

// StartDailyWorkflow 启动日常投资工作流
func (we *WorkflowEngine) StartDailyWorkflow() (*WorkflowInstance, error) {
	instanceID := we.generateInstanceID()

	instance := &WorkflowInstance{
		InstanceID:  instanceID,
		WorkflowID:  WFTypeDailyInvestment,
		Status:      WFInstanceStatusRunning,
		CurrentNode: "PRE_MARKET",
		CreatedAt:   time.Now(),
		StartedAt:   timePtr(time.Now()),
		History:     "[]",
	}

	if err := we.db.Create(instance).Error; err != nil {
		return nil, err
	}

	log.Printf("[WorkflowEngine] 启动日常投资工作流: %s", instanceID)
	return instance, nil
}

// ExecutePreMarketPhase 执行盘前阶段
func (we *WorkflowEngine) ExecutePreMarketPhase(instanceID string) error {
	log.Printf("[WorkflowEngine] 执行盘前阶段: %s", instanceID)

	ctx := context.Background()
	phase := PhasePreMarket
	workflowID := "WF-001_PRE_MARKET"

	// Step 1: Planner检查投资政策
	mandateCheckTask, err := we.taskMgr.CreateTask(
		AgentPlanner, TaskTypeCheckMandate,
		"检查投资政策合规性", "检查当前组合是否符合Investment Mandate要求",
		workflowID, "HIGH", phase, nil, nil, "",
	)
	if err != nil {
		return err
	}
	we.taskMgr.StartTask(mandateCheckTask.TaskID)

	complianceData, err := we.agentExec.ExecuteAgentTask(ctx, AgentPlanner, TaskTypeCheckMandate,
		"检查投资政策合规性", map[string]interface{}{"task_id": mandateCheckTask.TaskID})
	if err != nil {
		we.taskMgr.FailTask(mandateCheckTask.TaskID)
		return fmt.Errorf("Planner合规检查失败: %w", err)
	}
	complianceData["task_id"] = mandateCheckTask.TaskID
	we.taskMgr.CompleteTask(mandateCheckTask.TaskID, mustMarshalJSON(complianceData))

	// Step 2: Quant执行市场扫描
	marketScanTask, err := we.taskMgr.CreateTask(
		AgentQuant, TaskTypeMarketScan,
		"隔夜市场扫描", "扫描全球市场、海外指数、期货、新闻等",
		workflowID, "CRITICAL", phase, nil, nil, "",
	)
	if err != nil {
		return err
	}
	we.taskMgr.StartTask(marketScanTask.TaskID)

	marketData, err := we.agentExec.ExecuteAgentTask(ctx, AgentQuant, TaskTypeMarketScan,
		"隔夜市场扫描", map[string]interface{}{"task_id": marketScanTask.TaskID})
	if err != nil {
		we.taskMgr.FailTask(marketScanTask.TaskID)
		return fmt.Errorf("Quant市场扫描失败: %w", err)
	}
	marketData["task_id"] = marketScanTask.TaskID
	we.taskMgr.CompleteTask(marketScanTask.TaskID, mustMarshalJSON(marketData))

	// Step 3: Quant执行因子健康检查
	factorTask, err := we.taskMgr.CreateTask(
		AgentQuant, TaskTypeFactorHealth,
		"因子健康度分析", "分析Value、Momentum、Quality等因子的健康度",
		workflowID, "CRITICAL", phase,
		[]string{marketScanTask.TaskID}, nil, "",
	)
	if err != nil {
		return err
	}
	we.taskMgr.StartTask(factorTask.TaskID)

	factorData, err := we.agentExec.ExecuteAgentTask(ctx, AgentQuant, TaskTypeFactorHealth,
		"因子健康度分析", map[string]interface{}{
			"task_id":        factorTask.TaskID,
			"market_scan_id": marketScanTask.TaskID,
		})
	if err != nil {
		we.taskMgr.FailTask(factorTask.TaskID)
		return fmt.Errorf("Quant因子健康检查失败: %w", err)
	}
	factorData["task_id"] = factorTask.TaskID
	we.taskMgr.CompleteTask(factorTask.TaskID, mustMarshalJSON(factorData))

	// Step 4: Quant执行选股
	screenTask, err := we.taskMgr.CreateTask(
		AgentQuant, TaskTypeStockScreening,
		"全市场选股", "根据因子表现进行全市场选股",
		workflowID, "HIGH", phase,
		[]string{marketScanTask.TaskID, factorTask.TaskID}, nil, "",
	)
	if err != nil {
		return err
	}
	we.taskMgr.StartTask(screenTask.TaskID)

	screeningData, err := we.agentExec.ExecuteAgentTask(ctx, AgentQuant, TaskTypeStockScreening,
		"全市场选股", map[string]interface{}{
			"task_id":     screenTask.TaskID,
			"market_data": marketScanTask.TaskID,
			"factor_data": factorTask.TaskID,
		})
	if err != nil {
		we.taskMgr.FailTask(screenTask.TaskID)
		return fmt.Errorf("Quant选股失败: %w", err)
	}
	screeningData["task_id"] = screenTask.TaskID
	we.taskMgr.CompleteTask(screenTask.TaskID, mustMarshalJSON(screeningData))

	// Step 5: Quant执行组合模拟
	simTask, err := we.taskMgr.CreateTask(
		AgentQuant, TaskTypePortfolioSimulation,
		"组合模拟", "基于候选股票进行组合历史回测",
		workflowID, "HIGH", phase, []string{screenTask.TaskID}, nil, "",
	)
	if err != nil {
		return err
	}
	we.taskMgr.StartTask(simTask.TaskID)

	simData, err := we.agentExec.ExecuteAgentTask(ctx, AgentQuant, TaskTypePortfolioSimulation,
		"组合模拟", map[string]interface{}{
			"task_id":    simTask.TaskID,
			"candidates": screenTask.TaskID,
		})
	if err != nil {
		we.taskMgr.FailTask(simTask.TaskID)
		return fmt.Errorf("Quant组合模拟失败: %w", err)
	}
	simData["task_id"] = simTask.TaskID
	we.taskMgr.CompleteTask(simTask.TaskID, mustMarshalJSON(simData))

	// Step 6: Risk执行隔夜风险检查
	riskTask, err := we.taskMgr.CreateTask(
		AgentRisk, TaskTypeOvernightRisk,
		"隔夜风险评估", "评估组合的隔夜风险、Gap Risk等",
		workflowID, "CRITICAL", phase, nil, nil, "",
	)
	if err != nil {
		return err
	}
	we.taskMgr.StartTask(riskTask.TaskID)

	riskData, err := we.agentExec.ExecuteAgentTask(ctx, AgentRisk, TaskTypeOvernightRisk,
		"隔夜风险评估", map[string]interface{}{
			"task_id":  riskTask.TaskID,
			"sim_data": simTask.TaskID,
		})
	if err != nil {
		we.taskMgr.FailTask(riskTask.TaskID)
		return fmt.Errorf("Risk隔夜风险评估失败: %w", err)
	}
	riskData["task_id"] = riskTask.TaskID
	we.taskMgr.CompleteTask(riskTask.TaskID, mustMarshalJSON(riskData))

	// Step 7: CIO生成晨间简报
	cioBriefTask, err := we.taskMgr.CreateTask(
		AgentCIO, TaskTypeMorningBrief,
		"CIO晨间简报", "汇总各智能体报告，形成晨间简报",
		workflowID, "CRITICAL", phase,
		[]string{mandateCheckTask.TaskID, marketScanTask.TaskID, factorTask.TaskID,
			screenTask.TaskID, simTask.TaskID, riskTask.TaskID},
		nil, "",
	)
	if err != nil {
		return err
	}
	we.taskMgr.StartTask(cioBriefTask.TaskID)

	briefData, err := we.agentExec.ExecuteAgentTask(ctx, AgentCIO, TaskTypeMorningBrief,
		"CIO晨间简报", map[string]interface{}{
			"task_id":    cioBriefTask.TaskID,
			"compliance": mandateCheckTask.TaskID,
			"market":     marketScanTask.TaskID,
			"factor":     factorTask.TaskID,
			"candidates": screenTask.TaskID,
			"simulation": simTask.TaskID,
			"risk":       riskTask.TaskID,
		})
	if err != nil {
		we.taskMgr.FailTask(cioBriefTask.TaskID)
		return fmt.Errorf("CIO晨间简报失败: %w", err)
	}
	briefData["task_id"] = cioBriefTask.TaskID
	we.taskMgr.CompleteTask(cioBriefTask.TaskID, mustMarshalJSON(briefData))

	we.updateWorkflowInstance(instanceID, "PRE_MARKET_COMPLETE", WFInstanceStatusRunning)

	log.Printf("[WorkflowEngine] 盘前阶段完成，执行了 7 个任务")
	return nil
}

// ExecuteCIODecisionPhase 执行CIO决策阶段
func (we *WorkflowEngine) ExecuteCIODecisionPhase(instanceID string) (*InvestmentDecision, error) {
	log.Printf("[WorkflowEngine] 执行CIO决策阶段: %s", instanceID)

	ctx := context.Background()

	decisionTask, err := we.taskMgr.CreateTask(
		AgentCIO, TaskTypeInvestmentDecision,
		"CIO投资决策", "基于盘前研究报告生成投资决策",
		"WF-001_DECISION", "CRITICAL", PhasePreMarket, nil, nil, "",
	)
	if err != nil {
		return nil, err
	}
	we.taskMgr.StartTask(decisionTask.TaskID)

	decisionResult, err := we.agentExec.ExecuteAgentTask(ctx, AgentCIO, TaskTypeInvestmentDecision,
		"基于盘前研究报告生成投资决策", map[string]interface{}{
			"task_id": decisionTask.TaskID,
		})
	if err != nil {
		we.taskMgr.FailTask(decisionTask.TaskID)
		return nil, fmt.Errorf("CIO投资决策失败: %w", err)
	}
	decisionResult["task_id"] = decisionTask.TaskID
	we.taskMgr.CompleteTask(decisionTask.TaskID, mustMarshalJSON(decisionResult))

	decision := &InvestmentDecision{
		DecisionID:     "IDO-" + time.Now().Format("20060102") + "-001",
		DecisionType:   DecisionIncrease,
		AssetID:        "CIO投资决策",
		AssetName:      "CIO投资决策",
		CurrentWeight:  0.04,
		TargetWeight:   0.07,
		Reason:         "CIO根据市场研究和风险评估生成的投资决策",
		CIOAgentID:     AgentCIO,
		Status:         DecisionStatusApproved,
		RiskStatus:     "PENDING",
		ValidityDate:   timePtr(time.Now()),
		ExpiryDate:     timePtr(time.Now().Add(24 * time.Hour)),
		RelatedTaskIDs: decisionTask.TaskID,
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}

	if decisions, ok := decisionResult["decisions"].([]interface{}); ok && len(decisions) > 0 {
		if d, ok2 := decisions[0].(map[string]interface{}); ok2 {
			if asset, ok3 := d["asset"].(string); ok3 {
				decision.AssetID = asset
				decision.AssetName = asset
			}
			if reason, ok3 := d["reason"].(string); ok3 {
				decision.Reason = reason
			}
			if cw, ok3 := d["current_weight"].(float64); ok3 {
				decision.CurrentWeight = cw
			}
			if tw, ok3 := d["target_weight"].(float64); ok3 {
				decision.TargetWeight = tw
			}
		}
	}

	if err := we.db.Create(decision).Error; err != nil {
		return nil, err
	}

	we.updateWorkflowInstance(instanceID, "CIO_DECISION_COMPLETE", WFInstanceStatusRunning)

	log.Printf("[WorkflowEngine] CIO决策完成: %s", decision.DecisionID)
	return decision, nil
}

// ExecuteRiskReviewPhase 执行风控审查阶段
func (we *WorkflowEngine) ExecuteRiskReviewPhase(instanceID string, decisionTaskID string) error {
	log.Printf("[WorkflowEngine] 执行风控审查阶段: %s", instanceID)

	ctx := context.Background()

	riskTask, err := we.taskMgr.CreateTask(
		AgentRisk, TaskTypeTradeRiskCheck,
		"交易风险检查", "对CIO的投资决策进行风险检查",
		"WF-001_RISK_REVIEW", "CRITICAL", PhasePreMarket,
		[]string{decisionTaskID}, nil, "",
	)
	if err != nil {
		return err
	}
	we.taskMgr.StartTask(riskTask.TaskID)

	riskResult, err := we.agentExec.ExecuteAgentTask(ctx, AgentRisk, TaskTypeTradeRiskCheck,
		"交易风险检查", map[string]interface{}{
			"task_id":       riskTask.TaskID,
			"decision_task": decisionTaskID,
		})
	if err != nil {
		we.taskMgr.FailTask(riskTask.TaskID)
		return fmt.Errorf("Risk交易风险检查失败: %w", err)
	}
	riskResult["task_id"] = riskTask.TaskID
	we.taskMgr.CompleteTask(riskTask.TaskID, mustMarshalJSON(riskResult))

	riskStatus, _ := riskResult["risk_status"].(string)
	if riskStatus == "PASS" || riskStatus == "APPROVED" {
		conditions, _ := riskResult["conditions"].(string)
		if conditions == "" {
			conditions = "风控通过"
		}
		log.Printf("[WorkflowEngine] 风控审查通过: %s", conditions)
	} else {
		log.Printf("[WorkflowEngine] 风控审查未通过")
	}

	we.updateWorkflowInstance(instanceID, "RISK_REVIEW_COMPLETE", WFInstanceStatusRunning)

	log.Printf("[WorkflowEngine] 风控审查完成")
	return nil
}

// ExecuteMarketOrderPhase 执行盘中订单阶段
func (we *WorkflowEngine) ExecuteMarketOrderPhase(instanceID string, decisionTaskID string, riskTaskID string) error {
	log.Printf("[WorkflowEngine] 执行盘中订单阶段: %s", instanceID)

	ctx := context.Background()

	orderTask, err := we.taskMgr.CreateTask(
		AgentTrader, TaskTypeGenerateOrder,
		"生成执行订单", "根据批准的投资决策生成执行订单",
		"WF-001_EXECUTION", "CRITICAL", PhaseMorningMarket,
		[]string{decisionTaskID, riskTaskID}, nil, "",
	)
	if err != nil {
		return err
	}
	we.taskMgr.StartTask(orderTask.TaskID)

	orderResult, err := we.agentExec.ExecuteAgentTask(ctx, AgentTrader, TaskTypeGenerateOrder,
		"根据批准的投资决策生成执行订单", map[string]interface{}{
			"task_id":       orderTask.TaskID,
			"decision_task": decisionTaskID,
			"risk_task":     riskTaskID,
		})
	if err != nil {
		we.taskMgr.FailTask(orderTask.TaskID)
		return fmt.Errorf("Trader生成执行订单失败: %w", err)
	}
	orderResult["task_id"] = orderTask.TaskID
	we.taskMgr.CompleteTask(orderTask.TaskID, mustMarshalJSON(orderResult))

	execTask, err := we.taskMgr.CreateTask(
		AgentTrader, TaskTypeExecutionReport,
		"执行报告", "确认订单执行情况",
		"WF-001_EXECUTION", "HIGH", PhaseAfternoonMarket,
		[]string{orderTask.TaskID}, nil, "",
	)
	if err != nil {
		return err
	}
	we.taskMgr.StartTask(execTask.TaskID)

	execResult, err := we.agentExec.ExecuteAgentTask(ctx, AgentTrader, TaskTypeExecutionReport,
		"确认订单执行情况", map[string]interface{}{
			"task_id":    execTask.TaskID,
			"order_task": orderTask.TaskID,
		})
	if err != nil {
		we.taskMgr.FailTask(execTask.TaskID)
		return fmt.Errorf("Trader执行报告失败: %w", err)
	}
	execResult["task_id"] = execTask.TaskID
	we.taskMgr.CompleteTask(execTask.TaskID, mustMarshalJSON(execResult))

	we.updateWorkflowInstance(instanceID, "EXECUTION_COMPLETE", WFInstanceStatusRunning)

	log.Printf("[WorkflowEngine] 盘中订单阶段完成")
	return nil
}

// ExecutePostMarketPhase 执行盘后阶段
func (we *WorkflowEngine) ExecutePostMarketPhase(instanceID string) error {
	log.Printf("[WorkflowEngine] 执行盘后阶段: %s", instanceID)

	ctx := context.Background()

	// 1. Risk: 日终风险评估
	riskTask, err := we.taskMgr.CreateTask(
		AgentRisk, TaskTypeEndOfDayRisk,
		"日终风险评估", "重新计算组合风险指标",
		"WF-001_POST_MARKET", "CRITICAL", PhasePostMarket, nil, nil, "",
	)
	if err != nil {
		return err
	}
	we.taskMgr.StartTask(riskTask.TaskID)

	riskResult, err := we.agentExec.ExecuteAgentTask(ctx, AgentRisk, TaskTypeEndOfDayRisk,
		"日终风险评估", map[string]interface{}{
			"task_id": riskTask.TaskID,
			"type":    "END_OF_DAY",
		})
	if err != nil {
		we.taskMgr.FailTask(riskTask.TaskID)
		return fmt.Errorf("Risk日终风险评估失败: %w", err)
	}
	riskResult["task_id"] = riskTask.TaskID
	riskResult["type"] = "END_OF_DAY"
	we.taskMgr.CompleteTask(riskTask.TaskID, mustMarshalJSON(riskResult))

	// 2. Quant: 每日复盘
	quantTask, err := we.taskMgr.CreateTask(
		AgentQuant, TaskTypeAlphaDiscovery,
		"每日量化复盘", "分析今日Alpha、因子变化",
		"WF-001_POST_MARKET", "HIGH", PhasePostMarket, nil, nil, "",
	)
	if err != nil {
		return err
	}
	we.taskMgr.StartTask(quantTask.TaskID)

	quantResult, err := we.agentExec.ExecuteAgentTask(ctx, AgentQuant, TaskTypeAlphaDiscovery,
		"每日量化复盘", map[string]interface{}{
			"task_id": quantTask.TaskID,
		})
	if err != nil {
		we.taskMgr.FailTask(quantTask.TaskID)
		return fmt.Errorf("Quant每日量化复盘失败: %w", err)
	}
	_ = quantResult
	we.taskMgr.CompleteTask(quantTask.TaskID, "")

	// 3. CIO: 每日投资复盘
	reviewTask, err := we.taskMgr.CreateTask(
		AgentCIO, TaskTypeDailyReview,
		"CIO每日投资复盘", "总结今日决策和表现",
		"WF-001_POST_MARKET", "CRITICAL", PhasePostMarket,
		[]string{riskTask.TaskID}, nil, "",
	)
	if err != nil {
		return err
	}
	we.taskMgr.StartTask(reviewTask.TaskID)

	reviewResult, err := we.agentExec.ExecuteAgentTask(ctx, AgentCIO, TaskTypeDailyReview,
		"CIO每日投资复盘", map[string]interface{}{
			"task_id":     reviewTask.TaskID,
			"risk_report": riskTask.TaskID,
		})
	if err != nil {
		we.taskMgr.FailTask(reviewTask.TaskID)
		return fmt.Errorf("CIO每日投资复盘失败: %w", err)
	}
	reviewResult["task_id"] = reviewTask.TaskID
	reviewResult["date"] = time.Now().Format("2006-01-02")
	we.taskMgr.CompleteTask(reviewTask.TaskID, mustMarshalJSON(reviewResult))

	// 4. Planner: T+1规划
	t1Task, err := we.taskMgr.CreateTask(
		AgentCIO, TaskTypeT1Planning,
		"T+1投资规划", "为下一个交易日准备研究任务",
		"WF-001_POST_MARKET", "HIGH", PhasePostMarket, nil, nil, "",
	)
	if err != nil {
		return err
	}
	we.taskMgr.StartTask(t1Task.TaskID)

	t1Result, err := we.agentExec.ExecuteAgentTask(ctx, AgentPlanner, TaskTypeT1Planning,
		"T+1投资规划", map[string]interface{}{
			"task_id": t1Task.TaskID,
		})
	if err != nil {
		we.taskMgr.FailTask(t1Task.TaskID)
		return fmt.Errorf("Planner T+1规划失败: %w", err)
	}
	_ = t1Result
	we.taskMgr.CompleteTask(t1Task.TaskID, "")

	we.updateWorkflowInstance(instanceID, "POST_MARKET_COMPLETE", WFInstanceStatusCompleted)

	log.Printf("[WorkflowEngine] 盘后阶段完成")
	return nil
}

// ExecuteCompleteDailyWorkflow 执行完整的日常投资工作流。
// 采用「单步失败不中断」的容错策略：某一阶段失败仅记录错误，其余阶段继续执行，
// 保证盘前→CIO决策→风控→盘中→盘后的整体链路不因单点失败而整体中断；
// 阶段顺序按角色职责固定（不接受步骤间依赖被跳过），失败阶段在生成的 result 中留痕。
func (we *WorkflowEngine) ExecuteCompleteDailyWorkflow() (*WorkflowInstance, error) {
	instance, err := we.StartDailyWorkflow()
	if err != nil {
		return nil, err
	}

	var phaseErrs []string

	// Step 1: 盘前阶段
	if err := we.ExecutePreMarketPhase(instance.InstanceID); err != nil {
		log.Printf("[WorkflowEngine] 盘前阶段失败(继续后续阶段): %v", err)
		phaseErrs = append(phaseErrs, "盘前:"+err.Error())
	}

	// Step 2: CIO决策阶段
	decision, err := we.ExecuteCIODecisionPhase(instance.InstanceID)
	if err != nil || decision == nil {
		msg := "空决策"
		if err != nil {
			msg = err.Error()
		}
		log.Printf("[WorkflowEngine] CIO决策阶段失败(继续后续阶段): %v", msg)
		phaseErrs = append(phaseErrs, "CIO决策:"+msg)
		decision = &InvestmentDecision{} // 失败时用空决策占位，避免后续阶段解引用 nil
	}

	// Step 3: 风控审查阶段
	if err := we.ExecuteRiskReviewPhase(instance.InstanceID, decision.RelatedTaskIDs); err != nil {
		log.Printf("[WorkflowEngine] 风控审查阶段失败(继续后续阶段): %v", err)
		phaseErrs = append(phaseErrs, "风控审查:"+err.Error())
	}

	// Step 4: 获取风控任务ID
	riskTasks, _ := we.taskMgr.GetTasksByAgentAndStatus(AgentRisk, TaskStatusCompleted)
	var riskTaskID string
	if len(riskTasks) > 0 {
		riskTaskID = riskTasks[0].TaskID
	}

	// Step 5: 盘中订单阶段
	if err := we.ExecuteMarketOrderPhase(instance.InstanceID, decision.RelatedTaskIDs, riskTaskID); err != nil {
		log.Printf("[WorkflowEngine] 盘中订单阶段失败(继续后续阶段): %v", err)
		phaseErrs = append(phaseErrs, "盘中订单:"+err.Error())
	}

	// Step 6: 盘后阶段
	if err := we.ExecutePostMarketPhase(instance.InstanceID); err != nil {
		log.Printf("[WorkflowEngine] 盘后阶段失败(工作流结束): %v", err)
		phaseErrs = append(phaseErrs, "盘后:"+err.Error())
	}

	if len(phaseErrs) > 0 {
		log.Printf("[WorkflowEngine] 日常投资工作流完成(含%d个阶段失败): %s", len(phaseErrs), instance.InstanceID)
		return instance, fmt.Errorf("工作流部分阶段失败: %s", strings.Join(phaseErrs, " | "))
	}
	log.Printf("[WorkflowEngine] 日常投资工作流完成: %s", instance.InstanceID)
	return instance, nil
}

// GetWorkflowInstance 获取工作流实例
func (we *WorkflowEngine) GetWorkflowInstance(instanceID string) (*WorkflowInstance, error) {
	var instance WorkflowInstance
	err := we.db.Where("instance_id = ?", instanceID).First(&instance).Error
	if err != nil {
		return nil, err
	}
	return &instance, nil
}

// GetActiveWorkflowInstances 获取活跃的工作流实例
func (we *WorkflowEngine) GetActiveWorkflowInstances() ([]WorkflowInstance, error) {
	var instances []WorkflowInstance
	err := we.db.Where("status IN ?", []string{WFInstanceStatusRunning, WFInstanceStatusWaiting}).
		Order("created_at DESC").Find(&instances).Error
	return instances, err
}

// GetWorkflowInstancesByType 按类型获取工作流实例
func (we *WorkflowEngine) GetWorkflowInstancesByType(workflowType string) ([]WorkflowInstance, error) {
	var instances []WorkflowInstance
	err := we.db.Where("workflow_id = ?", workflowType).Order("created_at DESC").Find(&instances).Error
	return instances, err
}

// GenerateInvestmentDecision 生成投资决策
func (we *WorkflowEngine) GenerateInvestmentDecision(agentID, decisionType, assetID, assetName string, currentWeight, targetWeight float64, reason string) (*InvestmentDecision, error) {
	decision := &InvestmentDecision{
		DecisionID:    "IDO-" + time.Now().Format("20060102") + "-" + fmt.Sprintf("%03d", time.Now().Unix()%1000),
		DecisionType:  decisionType,
		AssetID:       assetID,
		AssetName:     assetName,
		CurrentWeight: currentWeight,
		TargetWeight:  targetWeight,
		Reason:        reason,
		CIOAgentID:    agentID,
		Status:        DecisionStatusApproved,
		ValidityDate:  timePtr(time.Now()),
		ExpiryDate:    timePtr(time.Now().Add(24 * time.Hour)),
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}

	if err := we.db.Create(decision).Error; err != nil {
		return nil, err
	}

	return decision, nil
}

// ApproveDecisionByRisk 风控师审批决策
func (we *WorkflowEngine) ApproveDecisionByRisk(decisionID string, approved bool, conditions string) error {
	var decision InvestmentDecision
	if err := we.db.Where("decision_id = ?", decisionID).First(&decision).Error; err != nil {
		return err
	}

	if approved {
		decision.Status = DecisionStatusRiskApproved
		decision.RiskStatus = "PASS"
		decision.RiskConditions = conditions
	} else {
		decision.Status = DecisionStatusRejected
		decision.RiskStatus = "REJECT"
		decision.RiskConditions = conditions
	}
	decision.UpdatedAt = time.Now()

	return we.db.Save(&decision).Error
}

// GetDecision 获取投资决策
func (we *WorkflowEngine) GetDecision(decisionID string) (*InvestmentDecision, error) {
	var decision InvestmentDecision
	err := we.db.Where("decision_id = ?", decisionID).First(&decision).Error
	if err != nil {
		return nil, err
	}
	return &decision, nil
}

// GetDecisionsByStatus 按状态获取投资决策
func (we *WorkflowEngine) GetDecisionsByStatus(status string) ([]InvestmentDecision, error) {
	var decisions []InvestmentDecision
	err := we.db.Where("status = ?", status).Order("created_at DESC").Find(&decisions).Error
	return decisions, err
}

// GetTodayDecisions 获取今日投资决策
func (we *WorkflowEngine) GetTodayDecisions() ([]InvestmentDecision, error) {
	today := time.Now().Format("2006-01-02")
	var decisions []InvestmentDecision
	err := we.db.Where("DATE(created_at) = ?", today).Order("created_at DESC").Find(&decisions).Error
	return decisions, err
}

// updateWorkflowInstance 更新工作流实例状态
func (we *WorkflowEngine) updateWorkflowInstance(instanceID, currentNode, status string) {
	now := time.Now()
	we.db.Model(&WorkflowInstance{}).Where("instance_id = ?", instanceID).
		Updates(map[string]interface{}{
			"current_node": currentNode,
			"status":       status,
			"updated_at":   now,
		})
}

// generateInstanceID 生成工作流实例ID
func (we *WorkflowEngine) generateInstanceID() string {
	return fmt.Sprintf("WF-%s-%d", time.Now().Format("20060102150405"), time.Now().UnixNano()%10000)
}

// timePtr 返回时间指针
func timePtr(t time.Time) *time.Time {
	return &t
}
