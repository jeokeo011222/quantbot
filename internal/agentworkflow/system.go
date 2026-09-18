package agentworkflow

import (
	"encoding/json"
	"fmt"
	"log"
	"time"

	"gorm.io/gorm"
)

// AgentWorkflowSystem 智能体工作编排系统
type AgentWorkflowSystem struct {
	db       *gorm.DB
	permMgr  *PermissionManager
	taskMgr  *TaskManager
	wfEngine *WorkflowEngine
	eventBus *EventBus
}

// NewAgentWorkflowSystem 创建智能体工作编排系统
func NewAgentWorkflowSystem(db *gorm.DB) *AgentWorkflowSystem {
	system := &AgentWorkflowSystem{
		db:      db,
		permMgr: NewPermissionManager(db),
		taskMgr: NewTaskManager(db),
	}
	system.wfEngine = NewWorkflowEngine(db, system.taskMgr, system.permMgr)
	system.eventBus = NewEventBus(db)

	log.Println("[AgentWorkflowSystem] 初始化完成")
	return system
}

// ==================== API接口 ====================

// GetAgentWorkbench 获取智能体工作台信息
func (aws *AgentWorkflowSystem) GetAgentWorkbench(agentID string) (*AgentWorkbench, error) {
	pendingTasks, _ := aws.taskMgr.GetTasksByAgentAndStatus(agentID, TaskStatusCreated)
	runningTasks, _ := aws.taskMgr.GetTasksByAgentAndStatus(agentID, TaskStatusRunning)

	allTodayTasks, _ := aws.taskMgr.GetTodayTasks()
	completedToday := 0
	for _, t := range allTodayTasks {
		if t.AgentID == agentID && t.Status == TaskStatusCompleted {
			completedToday++
		}
	}

	workbench := &AgentWorkbench{
		AgentID:        agentID,
		AgentName:      AgentRoleName[agentID],
		PendingTasks:   len(pendingTasks),
		RunningTasks:   len(runningTasks),
		CompletedToday: completedToday,
	}

	return workbench, nil
}

// GetAllWorkbenches 获取所有智能体工作台
func (aws *AgentWorkflowSystem) GetAllWorkbenches() ([]AgentWorkbench, error) {
	agentIDs := []string{AgentCIO, AgentPlanner, AgentQuant, AgentRisk, AgentTrader}
	var workbenches []AgentWorkbench

	for _, id := range agentIDs {
		wb, err := aws.GetAgentWorkbench(id)
		if err != nil {
			continue
		}
		workbenches = append(workbenches, *wb)
	}

	return workbenches, nil
}

// ExecuteDailyWorkflow 执行日常投资工作流
func (aws *AgentWorkflowSystem) ExecuteDailyWorkflow() (interface{}, error) {
	log.Println("[AgentWorkflowSystem] 开始执行日常投资工作流")

	instance, err := aws.wfEngine.ExecuteCompleteDailyWorkflow()
	if err != nil {
		return nil, fmt.Errorf("执行日常投资工作流失败: %w", err)
	}

	return map[string]interface{}{
		"workflow_instance_id": instance.InstanceID,
		"status":              instance.Status,
		"message":            "日常投资工作流执行完成",
	}, nil
}

// GetTodayTasks 获取今日任务列表
func (aws *AgentWorkflowSystem) GetTodayTasks() (interface{}, error) {
	tasks, err := aws.taskMgr.GetTodayTasks()
	if err != nil {
		return nil, err
	}

	var taskList []map[string]interface{}
	for _, t := range tasks {
		taskList = append(taskList, map[string]interface{}{
			"task_id":      t.TaskID,
			"agent_id":     t.AgentID,
			"agent_name":   AgentRoleName[t.AgentID],
			"task_type":    t.TaskType,
			"title":        t.Title,
			"status":       t.Status,
			"priority":     t.Priority,
			"phase":        t.Phase,
			"created_at":   t.CreatedAt.Format("2006-01-02 15:04:05"),
			"completed_at": func() string {
				if t.CompletedAt != nil {
					return t.CompletedAt.Format("2006-01-02 15:04:05")
				}
				return ""
			}(),
		})
	}

	return map[string]interface{}{
		"date":  time.Now().Format("2006-01-02"),
		"tasks": taskList,
		"total": len(taskList),
	}, nil
}

// GetTaskSummary 获取任务汇总
func (aws *AgentWorkflowSystem) GetTaskSummary() (interface{}, error) {
	return aws.taskMgr.GetTaskSummary()
}

// GetTodayDecisions 获取今日投资决策
func (aws *AgentWorkflowSystem) GetTodayDecisions() (interface{}, error) {
	decisions, err := aws.wfEngine.GetTodayDecisions()
	if err != nil {
		return nil, err
	}

	var decisionList []map[string]interface{}
	for _, d := range decisions {
		decisionList = append(decisionList, map[string]interface{}{
			"decision_id":    d.DecisionID,
			"decision_type":  d.DecisionType,
			"asset_id":       d.AssetID,
			"asset_name":     d.AssetName,
			"current_weight": d.CurrentWeight,
			"target_weight":  d.TargetWeight,
			"reason":         d.Reason,
			"status":         d.Status,
			"risk_status":    d.RiskStatus,
			"cio_agent_id":   d.CIOAgentID,
			"created_at":     d.CreatedAt.Format("2006-01-02 15:04:05"),
		})
	}

	return map[string]interface{}{
		"date":      time.Now().Format("2006-01-02"),
		"decisions": decisionList,
		"total":     len(decisionList),
	}, nil
}

// ApproveDecisionByCIO CIO审批决策
func (aws *AgentWorkflowSystem) ApproveDecisionByCIO(decisionID string) error {
	decision, err := aws.wfEngine.GetDecision(decisionID)
	if err != nil {
		return err
	}

	if decision.Status != DecisionStatusDraft {
		return fmt.Errorf("决策状态不是草稿，无法审批")
	}

	decision.Status = DecisionStatusApproved
	decision.UpdatedAt = time.Now()
	return aws.db.Save(decision).Error
}

// RiskReviewDecision 风控师审查决策
func (aws *AgentWorkflowSystem) RiskReviewDecision(decisionID string, approved bool, conditions string) error {
	return aws.wfEngine.ApproveDecisionByRisk(decisionID, approved, conditions)
}

// CreateInvestmentDecision 创建投资决策
func (aws *AgentWorkflowSystem) CreateInvestmentDecision(agentID, decisionType, assetID, assetName string, currentWeight, targetWeight float64, reason string) (interface{}, error) {
	if !aws.permMgr.CanApproveInvestment(agentID) {
		return nil, fmt.Errorf("智能体 %s 没有投资决策权", agentID)
	}

	decision, err := aws.wfEngine.GenerateInvestmentDecision(agentID, decisionType, assetID, assetName, currentWeight, targetWeight, reason)
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"decision_id": decision.DecisionID,
		"status":      decision.Status,
		"message":     "投资决策创建成功",
	}, nil
}

// GetTeamMembers 获取团队成员信息
func (aws *AgentWorkflowSystem) GetTeamMembers() (interface{}, error) {
	var members []TeamMember
	err := aws.db.Where("is_active = ?", true).Order("id").Find(&members).Error
	if err != nil {
		return nil, err
	}

	var memberList []map[string]interface{}
	for _, m := range members {
		memberList = append(memberList, map[string]interface{}{
			"agent_id":    m.AgentID,
			"name":        m.Name,
			"title":       m.Title,
			"role":        m.Role,
			"description": m.Description,
			"signature":   m.Signature,
		})
	}

	return map[string]interface{}{
		"members": memberList,
		"total":   len(memberList),
	}, nil
}

// GetAgentPermissions 获取智能体权限
func (aws *AgentWorkflowSystem) GetAgentPermissions(agentID string) (interface{}, error) {
	permissions, err := aws.permMgr.GetAgentPermissions(agentID)
	if err != nil {
		return nil, err
	}

	var permList []map[string]interface{}
	for _, p := range permissions {
		permList = append(permList, map[string]interface{}{
			"permission": p.Permission,
			"allowed":    p.IsAllowed,
		})
	}

	return map[string]interface{}{
		"agent_id":    agentID,
		"agent_name":  AgentRoleName[agentID],
		"permissions": permList,
		"total":       len(permList),
	}, nil
}

// GetAgentTasks 获取智能体任务
func (aws *AgentWorkflowSystem) GetAgentTasks(agentID string) (interface{}, error) {
	tasks, err := aws.taskMgr.GetTasksByAgent(agentID)
	if err != nil {
		return nil, err
	}

	var taskList []map[string]interface{}
	for _, t := range tasks {
		taskList = append(taskList, map[string]interface{}{
			"task_id":    t.TaskID,
			"task_type":  t.TaskType,
			"title":      t.Title,
			"status":     t.Status,
			"priority":   t.Priority,
			"phase":      t.Phase,
			"created_at": t.CreatedAt.Format("2006-01-02 15:04:05"),
		})
	}

	return map[string]interface{}{
		"agent_id": agentID,
		"tasks":    taskList,
		"total":    len(taskList),
	}, nil
}

// StartWorkflow 手动启动工作流
func (aws *AgentWorkflowSystem) StartWorkflow(workflowType string) (interface{}, error) {
	switch workflowType {
	case WFTypeDailyInvestment:
		return aws.ExecuteDailyWorkflow()

	case WFTypeRiskEvent:
		event, err := aws.eventBus.PublishEvent(EventRiskLimit, "MANUAL", "手动触发风险事件", nil)
		if err != nil {
			return nil, err
		}
		return map[string]interface{}{
			"event_id": event.EventID,
			"status":   "triggered",
			"message":  "风险事件已触发",
		}, nil

	default:
		return nil, fmt.Errorf("不支持的工作流类型: %s", workflowType)
	}
}

// GetActiveWorkflows 获取活跃的工作流
func (aws *AgentWorkflowSystem) GetActiveWorkflows() (interface{}, error) {
	instances, err := aws.wfEngine.GetActiveWorkflowInstances()
	if err != nil {
		return nil, err
	}

	var wfList []map[string]interface{}
	for _, inst := range instances {
		wfList = append(wfList, map[string]interface{}{
			"instance_id":  inst.InstanceID,
			"workflow_id":  inst.WorkflowID,
			"status":       inst.Status,
			"current_node": inst.CurrentNode,
			"created_at":   inst.CreatedAt.Format("2006-01-02 15:04:05"),
		})
	}

	return map[string]interface{}{
		"workflows": wfList,
		"total":    len(wfList),
	}, nil
}

// GetSystemStatus 获取系统状态
func (aws *AgentWorkflowSystem) GetSystemStatus() (interface{}, error) {
	taskSummary, _ := aws.taskMgr.GetTaskSummary()
	activeWFs, _ := aws.wfEngine.GetActiveWorkflowInstances()

	return map[string]interface{}{
		"task_summary":    taskSummary,
		"active_workflows": len(activeWFs),
		"agent_count":      5,
		"time":             time.Now().Format("2006-01-02 15:04:05"),
	}, nil
}

// PublishCustomEvent 发布自定义事件
func (aws *AgentWorkflowSystem) PublishCustomEvent(eventType, source, description string, payload map[string]interface{}) (interface{}, error) {
	event, err := aws.eventBus.PublishEvent(eventType, source, description, payload)
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"event_id":   event.EventID,
		"event_type": event.EventType,
		"status":     event.Status,
		"message":    "事件发布成功",
	}, nil
}

// SetAgentExecutor 设置Agent执行器
func (aws *AgentWorkflowSystem) SetAgentExecutor(exec AgentExecutor) {
	aws.wfEngine.SetAgentExecutor(exec)
}

// GetTaskManager 获取任务管理器
func (aws *AgentWorkflowSystem) GetTaskManager() *TaskManager {
	return aws.taskMgr
}

// GetWorkflowEngine 获取工作流引擎
func (aws *AgentWorkflowSystem) GetWorkflowEngine() *WorkflowEngine {
	return aws.wfEngine
}

// 确保 json 包被使用
var _ = json.Marshal