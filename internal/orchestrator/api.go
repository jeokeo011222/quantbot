package orchestrator

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/quantpilot/quantpilot/internal/port"
)

// OrchestratorAPI 编排引擎API
type OrchestratorAPI struct {
	engine *Orchestrator
}

func NewOrchestratorAPI(engine *Orchestrator) *OrchestratorAPI {
	return &OrchestratorAPI{engine: engine}
}

// StartDailyWorkflow 启动每日投资决策工作流
func (api *OrchestratorAPI) StartDailyWorkflow() (interface{}, error) {
	if !api.engine.IsRunning() {
		api.engine.Start()
	}

	task, err := api.engine.CreateTask("daily_investment", "system",
		fmt.Sprintf("每日投资决策 - %s", time.Now().Format("2006-01-02")),
		"主流程：Planner提出方案 → Quant提供证据 → Risk可以否决 → CIO最终决定 → Trader执行")
	if err != nil {
		return nil, err
	}

	ctx := context.Background()
	if err := api.engine.StartTask(ctx, task.ID); err != nil {
		return nil, err
	}

	if err := api.engine.ExecuteNextStep(ctx, task.ID); err != nil {
		return nil, err
	}

	log.Printf("[OrchestratorAPI] Daily workflow started: task=%s", task.ID)

	return map[string]interface{}{
		"task_id":          task.ID,
		"status":           task.Status,
		"type":             "DAILY_INVESTMENT",
		"permission_chain": "PROPOSE→EVIDENCE→VETO→DECIDE→EXECUTE",
		"message":          "每日投资决策工作流已启动",
	}, nil
}

// GetTaskStatus 获取任务状态
func (api *OrchestratorAPI) GetTaskStatus(taskID string) (interface{}, error) {
	progress := api.engine.GetTaskProgress(taskID)
	if progress == nil {
		return nil, fmt.Errorf("task not found: %s", taskID)
	}
	return progress, nil
}

// ListActiveTasks 列出活跃任务
func (api *OrchestratorAPI) ListActiveTasks() (interface{}, error) {
	tasks := api.engine.ListTasks(StatusRunning)
	result := make([]interface{}, 0)
	for _, task := range tasks {
		progress := api.engine.GetTaskProgress(task.ID)
		result = append(result, progress)
	}
	return map[string]interface{}{
		"active_tasks": result,
		"count":        len(result),
	}, nil
}

// ListPendingTasks 列出待执行任务
func (api *OrchestratorAPI) ListPendingTasks() (interface{}, error) {
	tasks := api.engine.ListTasks(StatusPending)
	result := make([]interface{}, 0)
	for _, task := range tasks {
		progress := api.engine.GetTaskProgress(task.ID)
		result = append(result, progress)
	}
	return map[string]interface{}{
		"pending_tasks": result,
		"count":         len(result),
	}, nil
}

// ListCompletedTasks 列出已完成任务
func (api *OrchestratorAPI) ListCompletedTasks() (interface{}, error) {
	tasks := api.engine.ListTasks(StatusSuccess)
	result := make([]interface{}, 0)
	for _, task := range tasks {
		progress := api.engine.GetTaskProgress(task.ID)
		result = append(result, progress)
	}
	return map[string]interface{}{
		"completed_tasks": result,
		"count":           len(result),
	}, nil
}

// GetAgentTasks 获取智能体待办任务（含执行中的步骤，状态标 EXECUTING）
func (api *OrchestratorAPI) GetAgentTasks(role string) (interface{}, error) {
	role = strings.ToLower(strings.TrimSpace(role))
	agentTasks := api.collectAgentTasks(role)
	return map[string]interface{}{
		"role":        role,
		"agent_tasks": agentTasks,
		"count":       len(agentTasks),
	}, nil
}

// collectAgentTasks 收集某角色的待办/执行中步骤（模板步骤 Assignee 为小写角色名，与 agents 角色大写区分）。
// 统一返回带任务字段的对象：既含 task_* 字段，也含 id/title/priority 别名，兼容 AI团队页与 Workflow 页两处前端。
func (api *OrchestratorAPI) collectAgentTasks(role string) []interface{} {
	tasks := api.engine.ListTasks("")
	var agentTasks []interface{}
	for _, task := range tasks {
		if task.Status != StatusPending && task.Status != StatusRunning {
			continue
		}
		for _, step := range task.Steps {
			if step.Assignee != role {
				continue
			}
			if step.Status != StatusPending && step.Status != StatusRunning {
				continue
			}
			stepStatus := string(step.Status)
			if step.Status == StatusRunning {
				stepStatus = "EXECUTING"
			}
			// 获取该步骤的权限
			var permission string
			if tpl := api.engine.GetTemplate(task.TemplateID); tpl != nil {
				for _, st := range tpl.Steps {
					if st.Assignee == role {
						permission = st.RolePermission
						break
					}
				}
			}
			agentTasks = append(agentTasks, map[string]interface{}{
				"id":         step.ID,
				"task_id":    task.ID,
				"task_name":  task.Name,
				"step_name":  step.Name,
				"title":      fmt.Sprintf("%s - %s", task.Name, step.Name),
				"type":       "TASK",
				"priority":   "HIGH",
				"permission": permission,
				"assignee":   step.Assignee,
				"status":     stepStatus,
				"is_event":   task.IsEventDriven,
				"created_at": task.CreatedAt.Format(time.RFC3339),
			})
		}
	}
	return agentTasks
}

// GetAllAgentTasks 获取所有智能体的待办任务（含执行中的步骤，状态标 EXECUTING）。
// 返回 { planner: [...], quant: [...], ... }，各角色值为任务数组（供 AI团队页 / Workflow 页直接消费）。
func (api *OrchestratorAPI) GetAllAgentTasks() (interface{}, error) {
	roles := []struct {
		key  string
		role port.AgentRole
	}{
		{"planner", port.RolePlanner},
		{"quant", port.RoleQuant},
		{"cio", port.RoleCIO},
		{"risk", port.RoleRisk},
		{"trader", port.RoleTrader},
	}

	result := make(map[string]interface{}, len(roles))
	for _, r := range roles {
		result[r.key] = api.collectAgentTasks(r.key)
	}
	return result, nil
}

// GetEngineStats 获取引擎统计
func (api *OrchestratorAPI) GetEngineStats() (interface{}, error) {
	return api.engine.GetOrchestratorStats(), nil
}

// ListTemplates 列出任务模板（3+1模型）
func (api *OrchestratorAPI) ListTemplates() (interface{}, error) {
	templates := api.engine.ListTemplates()
	result := make([]interface{}, 0)
	for _, t := range templates {
		steps := make([]interface{}, 0)
		for _, s := range t.Steps {
			steps = append(steps, map[string]interface{}{
				"id":          s.ID,
				"name":        s.Name,
				"assignee":    s.Assignee,
				"required":    s.Required,
				"permission":  s.RolePermission,
				"can_veto":    s.CanVeto,
				"can_propose": s.CanPropose,
				"can_execute": s.CanExecute,
				"can_approve": s.CanApprove,
			})
		}
		result = append(result, map[string]interface{}{
			"id":            t.ID,
			"name":          t.Name,
			"description":   t.Description,
			"workflow_type": t.WorkflowType,
			"steps":         steps,
		})
	}
	return map[string]interface{}{
		"templates": result,
		"count":     len(result),
		"model":     "3+1 (DailyInvestment + DailyReview + Research + Event)",
	}, nil
}

// RunWorkflow 手动触发工作流
func (api *OrchestratorAPI) RunWorkflow(templateID string) (interface{}, error) {
	template := api.engine.GetTemplate(templateID)
	if template == nil {
		return nil, fmt.Errorf("template not found: %s", templateID)
	}

	if !api.engine.IsRunning() {
		api.engine.Start()
	}

	task, err := api.engine.CreateTask(templateID, "system",
		fmt.Sprintf("手动触发 - %s", template.Name),
		template.Description)
	if err != nil {
		return nil, err
	}

	ctx := context.Background()
	if err := api.engine.StartTask(ctx, task.ID); err != nil {
		return nil, err
	}

	if err := api.engine.ExecuteNextStep(ctx, task.ID); err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"task_id":  task.ID,
		"template": templateID,
		"type":     template.WorkflowType,
		"message":  "工作流已启动",
	}, nil
}

// RunManualWorkflow 手动触发工作流（别名）
func (api *OrchestratorAPI) RunManualWorkflow(templateID string) (interface{}, error) {
	return api.RunWorkflow(templateID)
}

// SubmitEvent 提交事件
func (api *OrchestratorAPI) SubmitEvent(eventType string, severity string, message string, eventData interface{}) (interface{}, error) {
	task, err := api.engine.CreateEventTask(eventData)
	if err != nil {
		return nil, err
	}

	ctx := context.Background()
	if err := api.engine.StartTask(ctx, task.ID); err != nil {
		return nil, err
	}

	if err := api.engine.ExecuteNextStep(ctx, task.ID); err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"event_id": task.ID,
		"type":     eventType,
		"severity": severity,
		"task_id":  task.ID,
		"message":  message,
	}, nil
}

// ExecuteNextStep 推进工作流到下一步
func (api *OrchestratorAPI) ExecuteNextStep(taskID string) (interface{}, error) {
	ctx := context.Background()
	if err := api.engine.ExecuteNextStep(ctx, taskID); err != nil {
		return nil, err
	}
	return map[string]interface{}{
		"task_id": taskID,
		"message": "下一步已启动",
	}, nil
}

// GetPermissionChain 获取权限链说明
func (api *OrchestratorAPI) GetPermissionChain() map[string]interface{} {
	return map[string]interface{}{
		"description": "权限链：Planner(PROPOSE) → Quant(EVIDENCE) → Risk(VETO) → CIO(DECIDE) → Trader(EXECUTE)",
		"roles": map[string]interface{}{
			"planner": map[string]interface{}{
				"permission": "PROPOSE",
				"can_do":     []string{"提出投资方案", "市场规划", "客户服务"},
				"cannot_do":  []string{"否决风险决策", "直接交易执行"},
			},
			"quant": map[string]interface{}{
				"permission": "EVIDENCE",
				"can_do":     []string{"提供量化证据", "因子分析", "选股"},
				"cannot_do":  []string{"否决风险决策", "最终投资决策"},
			},
			"risk": map[string]interface{}{
				"permission": "VETO",
				"can_do":     []string{"否决高风险决策", "风险评估", "风险监控"},
				"cannot_do":  []string{"提出投资方案", "直接交易执行"},
			},
			"cio": map[string]interface{}{
				"permission": "DECIDE",
				"can_do":     []string{"最终投资决策", "审批交易", "生成决策对象"},
				"cannot_do":  []string{"否决风险决策", "直接交易执行"},
			},
			"trader": map[string]interface{}{
				"permission": "EXECUTE",
				"can_do":     []string{"执行CIO的决策对象", "订单管理"},
				"cannot_do":  []string{"自行决策", "否决风险", "修改决策对象"},
			},
		},
		"decision_object": map[string]interface{}{
			"description": "Trader只执行CIO批准的DecisionObject",
			"fields":      []string{"decision", "confidence", "risk_level", "position_limit", "reason", "approved_by", "risk_checked"},
		},
	}
}
