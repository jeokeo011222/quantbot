package orchestrator

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/quantpilot/quantpilot/internal/data"
	"github.com/quantpilot/quantpilot/internal/port"
)

// RolePermission 角色权限
type RolePermission string

const (
	PermPropose  RolePermission = "PROPOSE"  // Planner: 提出方案
	PermEvidence RolePermission = "EVIDENCE" // Quant: 提供证据
	PermVeto     RolePermission = "VETO"     // Risk: 可以否决
	PermDecide   RolePermission = "DECIDE"   // CIO: 最终决定
	PermExecute  RolePermission = "EXECUTE"  // Trader: 只能执行
)

// Orchestrator 任务编排引擎
type Orchestrator struct {
	mu        sync.RWMutex
	db        *data.SQLiteManager
	tasks     map[string]*WorkflowTask
	templates map[string]*TaskTemplate
	handlers  map[string]TaskHandler
	running   bool
	idSeq     uint64 // 任务ID单调计数器，避免同纳秒创建多个任务时 ID 冲突
}

// TaskHandler 任务处理器接口
type TaskHandler interface {
	Handle(ctx context.Context, task *WorkflowTask, step *TaskStep) error
	GetRole() string
	GetPermission() RolePermission
}

// NewOrchestrator 创建任务编排引擎
func NewOrchestrator(db *data.SQLiteManager) *Orchestrator {
	o := &Orchestrator{
		db:        db,
		tasks:     make(map[string]*WorkflowTask),
		templates: make(map[string]*TaskTemplate),
		handlers:  make(map[string]TaskHandler),
		running:   false,
	}
	o.registerTemplates()
	return o
}

// RegisterHandler 注册任务处理器
func (o *Orchestrator) RegisterHandler(handler TaskHandler) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.handlers[handler.GetRole()] = handler
	log.Printf("[Orchestrator] Registered handler: role=%s, permission=%s", handler.GetRole(), handler.GetPermission())
}

// RegisterDefaultHandlers 注册默认处理器（从agentTeam映射创建）
func (o *Orchestrator) RegisterDefaultHandlers(agentTeam map[port.AgentRole]*port.Agent) {
	if agentTeam == nil {
		log.Println("[Orchestrator] RegisterDefaultHandlers: agentTeam is nil, skipping")
		return
	}

	if plannerAgent, ok := agentTeam[port.RolePlanner]; ok {
		o.RegisterHandler(NewPlannerHandler(o, plannerAgent))
	}
	if quantAgent, ok := agentTeam[port.RoleQuant]; ok {
		o.RegisterHandler(NewQuantHandler(o, quantAgent))
	}
	if riskAgent, ok := agentTeam[port.RoleRisk]; ok {
		o.RegisterHandler(NewRiskHandler(o, riskAgent))
	}
	if cioAgent, ok := agentTeam[port.RoleCIO]; ok {
		o.RegisterHandler(NewCIOHandler(o, cioAgent))
	}
	if traderAgent, ok := agentTeam[port.RoleTrader]; ok {
		o.RegisterHandler(NewTraderHandler(o, traderAgent))
	}

	log.Printf("[Orchestrator] Default handlers registered: %d handlers", len(o.handlers))
}

// Start 启动编排引擎
func (o *Orchestrator) Start() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.running = true
	log.Println("[Orchestrator] Started - Permission-based task orchestration")
}

// Stop 停止编排引擎
func (o *Orchestrator) Stop() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.running = false
	log.Println("[Orchestrator] Stopped")
}

// IsRunning 检查是否运行中
func (o *Orchestrator) IsRunning() bool {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.running
}

// ============ 模板管理（3+1工作流模型）============

func (o *Orchestrator) registerTemplates() {
	// Workflow 01: DailyInvestment（主流程）
	// 权限链: Planner(PROPOSE) → Quant(EVIDENCE) → Risk(VETO) → CIO(DECIDE) → Trader(EXECUTE)
	o.templates["daily_investment"] = &TaskTemplate{
		ID:           "daily_investment",
		Name:         "每日投资决策流程",
		Description:  "主流程：Planner提出方案 → Quant提供证据 → Risk可以否决 → CIO最终决定 → Trader执行",
		WorkflowType: "DAILY_INVESTMENT",
		Steps: []TaskStepTemplate{
			{
				ID: "step1_planner", Name: "盘前市场分析", Assignee: "planner",
				Required: true, CanPropose: true, RolePermission: "PROPOSE",
			},
			{
				ID: "step2_quant", Name: "盘前量化分析", Assignee: "quant",
				Required: true, RolePermission: "EVIDENCE",
			},
			{
				ID: "step3_risk", Name: "盘前风险检查", Assignee: "risk",
				Required: true, CanVeto: true, RolePermission: "VETO",
			},
			{
				ID: "step4_cio", Name: "生成盘前决策", Assignee: "cio",
				Required: true, CanApprove: true, RolePermission: "DECIDE",
			},
			{
				ID: "step5_trader", Name: "执行交易", Assignee: "trader",
				Required: true, CanExecute: true, RolePermission: "EXECUTE",
			},
		},
	}

	// Workflow 02: DailyReview（日终审查）
	// Trader → Risk → CIO
	o.templates["daily_review"] = &TaskTemplate{
		ID:           "daily_review",
		Name:         "日终审查流程",
		Description:  "Trader结算 → Risk审查 → CIO报告",
		WorkflowType: "DAILY_REVIEW",
		Steps: []TaskStepTemplate{
			{
				ID: "step1_trader", Name: "日终结算", Assignee: "trader",
				Required: true, CanExecute: true, RolePermission: "EXECUTE",
			},
			{
				ID: "step2_risk", Name: "日终风险审查", Assignee: "risk",
				Required: true, CanVeto: true, RolePermission: "VETO",
			},
			{
				ID: "step3_cio", Name: "日终投资报告", Assignee: "cio",
				Required: true, CanApprove: true, RolePermission: "DECIDE",
			},
		},
	}

	// Workflow 03: Research（市场研究）
	// Planner → Quant → CIO(可选)
	o.templates["research"] = &TaskTemplate{
		ID:           "research",
		Name:         "市场研究流程",
		Description:  "Planner规划 → Quant分析 → CIO审阅（可选）",
		WorkflowType: "RESEARCH",
		Steps: []TaskStepTemplate{
			{
				ID: "step1_planner", Name: "市场研究规划", Assignee: "planner",
				Required: true, CanPropose: true, RolePermission: "PROPOSE",
			},
			{
				ID: "step2_quant", Name: "深度量化分析", Assignee: "quant",
				Required: true, RolePermission: "EVIDENCE",
			},
			{
				ID: "step3_cio", Name: "CIO审阅", Assignee: "cio",
				Required: false, CanApprove: true, RolePermission: "DECIDE",
			},
		},
	}

	// Event Workflow: 动态事件工作流
	// Event → Risk → CIO → Trader
	o.templates["event_workflow"] = &TaskTemplate{
		ID:           "event_workflow",
		Name:         "事件响应流程",
		Description:  "事件触发 → Risk评估 → CIO决策 → Trader执行",
		WorkflowType: "EVENT",
		Steps: []TaskStepTemplate{
			{
				ID: "step1_risk", Name: "事件风险评估", Assignee: "risk",
				Required: true, CanVeto: true, RolePermission: "VETO",
			},
			{
				ID: "step2_cio", Name: "事件决策", Assignee: "cio",
				Required: true, CanApprove: true, RolePermission: "DECIDE",
			},
			{
				ID: "step3_trader", Name: "事件交易执行", Assignee: "trader",
				Required: true, CanExecute: true, RolePermission: "EXECUTE",
			},
		},
	}

	// Workflow 04: InvestmentPlanning（投资规划）
	// 由"投资管理 → 生成投资方案"触发，关联投资规划师（Planner）任务
	o.templates["investment_planning"] = &TaskTemplate{
		ID:           "investment_planning",
		Name:         "投资规划流程",
		Description:  "投资规划师生成投资方案（PROPOSE）",
		WorkflowType: "INVESTMENT_PLANNING",
		Steps: []TaskStepTemplate{
			{
				ID: "step1_planner", Name: "投资方案规划", Assignee: "planner",
				Required: true, CanPropose: true, RolePermission: "PROPOSE",
			},
		},
	}

	log.Printf("[Orchestrator] Registered %d templates", len(o.templates))
}

// GetTemplate 获取模板
func (o *Orchestrator) GetTemplate(templateID string) *TaskTemplate {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.templates[templateID]
}

// ListTemplates 列出所有模板
func (o *Orchestrator) ListTemplates() []*TaskTemplate {
	o.mu.RLock()
	defer o.mu.RUnlock()
	result := make([]*TaskTemplate, 0, len(o.templates))
	for _, t := range o.templates {
		result = append(result, t)
	}
	return result
}

// ============ 任务管理（幂等）============

// CreateTask 创建任务（基于模板，幂等：相同模板+日期只创建一次，用于每日定时工作流）
func (o *Orchestrator) CreateTask(templateID string, createdBy string, name string, description string) (*WorkflowTask, error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	template, ok := o.templates[templateID]
	if !ok {
		return nil, fmt.Errorf("template not found: %s", templateID)
	}

	// 幂等检查：同一天同一模板只创建一个任务
	today := time.Now().Format("2006-01-02")
	for _, existing := range o.tasks {
		if existing.TemplateID == templateID && existing.CreatedAt.Format("2006-01-02") == today {
			log.Printf("[Orchestrator] Idempotent: task already exists for template %s today, returning existing", templateID)
			return existing, nil
		}
	}

	return o.createTaskLocked(template, createdBy, name, description)
}

// CreateUserTask 创建用户触发的一次性任务（不幂等：每次调用都新建任务）。
// 适用于"生成投资方案"等用户手动触发且可能同日多次执行的动作，避免被每日幂等拦截。
func (o *Orchestrator) CreateUserTask(templateID string, createdBy string, name string, description string) (*WorkflowTask, error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	template, ok := o.templates[templateID]
	if !ok {
		return nil, fmt.Errorf("template not found: %s", templateID)
	}

	return o.createTaskLocked(template, createdBy, name, description)
}

// createTaskLocked 实际创建任务（须在持锁状态下调用）
func (o *Orchestrator) createTaskLocked(template *TaskTemplate, createdBy string, name string, description string) (*WorkflowTask, error) {
	now := time.Now()
	o.idSeq++
	task := &WorkflowTask{
		ID:            fmt.Sprintf("wf_%s_%d_%d", template.ID, now.UnixNano(), o.idSeq),
		TemplateID:    template.ID,
		Name:          name,
		Status:        StatusPending,
		CurrentStep:   0,
		CreatedBy:     createdBy,
		CreatedAt:     now,
		UpdatedAt:     now,
		IsEventDriven: template.WorkflowType == "EVENT",
	}

	for i, stepTpl := range template.Steps {
		task.Steps = append(task.Steps, TaskStep{
			ID:       fmt.Sprintf("%s_step%d", task.ID, i),
			Name:     stepTpl.Name,
			Assignee: stepTpl.Assignee,
			Status:   StatusPending,
		})
	}

	o.tasks[task.ID] = task
	log.Printf("[Orchestrator] Created task: %s (template: %s, type: %s)", task.ID, template.ID, template.WorkflowType)
	return task, nil
}

// CreateEventTask 创建事件任务（每次事件都创建新任务）
func (o *Orchestrator) CreateEventTask(eventData interface{}) (*WorkflowTask, error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	template := o.templates["event_workflow"]
	if template == nil {
		return nil, fmt.Errorf("event_workflow template not found")
	}

	now := time.Now()
	task := &WorkflowTask{
		ID:            fmt.Sprintf("evt_%d", now.UnixNano()),
		TemplateID:    "event_workflow",
		Name:          fmt.Sprintf("事件响应 - %s", now.Format("15:04:05")),
		Status:        StatusPending,
		CurrentStep:   0,
		CreatedBy:     "system",
		CreatedAt:     now,
		UpdatedAt:     now,
		IsEventDriven: true,
		EventData:     eventData,
	}

	for i, stepTpl := range template.Steps {
		task.Steps = append(task.Steps, TaskStep{
			ID:       fmt.Sprintf("%s_step%d", task.ID, i),
			Name:     stepTpl.Name,
			Assignee: stepTpl.Assignee,
			Status:   StatusPending,
		})
	}

	o.tasks[task.ID] = task
	log.Printf("[Orchestrator] Created event task: %s", task.ID)
	return task, nil
}

// GetTask 获取任务（锁内深拷贝，外部读取安全，不受异步执行写影响）
func (o *Orchestrator) GetTask(taskID string) *WorkflowTask {
	o.mu.RLock()
	task, ok := o.tasks[taskID]
	if !ok {
		o.mu.RUnlock()
		return nil
	}
	cp := deepCopyTask(task)
	o.mu.RUnlock()
	return cp
}

// ListTasks 列出任务（返回深拷贝，外部遍历安全，不受异步执行写影响）
func (o *Orchestrator) ListTasks(status TaskStatus) []*WorkflowTask {
	o.mu.RLock()
	defer o.mu.RUnlock()
	var result []*WorkflowTask
	for _, t := range o.tasks {
		if status == "" || t.Status == status {
			result = append(result, deepCopyTask(t))
		}
	}
	return result
}

// deepCopyTask 深拷贝任务实例（须在持锁状态下调用）
func deepCopyTask(task *WorkflowTask) *WorkflowTask {
	cp := *task
	cp.Steps = make([]TaskStep, len(task.Steps))
	for i, s := range task.Steps {
		cp.Steps[i] = s
		cp.Steps[i].Input = deepCopy(s.Input)
		cp.Steps[i].Output = deepCopy(s.Output)
	}
	if task.Decision != nil {
		d := *task.Decision
		cp.Decision = &d
	}
	if task.VetoResult != nil {
		v := *task.VetoResult
		cp.VetoResult = &v
	}
	return &cp
}

// ============ 工作流执行============

// StartTask 开始执行任务
func (o *Orchestrator) StartTask(ctx context.Context, taskID string) error {
	o.mu.Lock()
	defer o.mu.Unlock()

	task, ok := o.tasks[taskID]
	if !ok {
		return fmt.Errorf("task not found: %s", taskID)
	}

	if task.Status != StatusPending {
		return fmt.Errorf("task cannot be started (status: %s)", task.Status)
	}

	task.Status = StatusRunning
	task.CurrentStep = 0
	task.UpdatedAt = time.Now()

	log.Printf("[Orchestrator] Started task: %s", taskID)
	return nil
}

// ExecuteNextStep 执行下一步（权限检查）
func (o *Orchestrator) ExecuteNextStep(ctx context.Context, taskID string) error {
	o.mu.Lock()
	task, ok := o.tasks[taskID]
	if !ok {
		o.mu.Unlock()
		return fmt.Errorf("task not found: %s", taskID)
	}

	if task.Status != StatusRunning {
		o.mu.Unlock()
		return fmt.Errorf("task is not running (status: %s)", task.Status)
	}

	if task.CurrentStep >= len(task.Steps) {
		o.mu.Unlock()
		return fmt.Errorf("no more steps")
	}

	step := &task.Steps[task.CurrentStep]
	if step.Status != StatusPending {
		o.mu.Unlock()
		return fmt.Errorf("step is not pending (status: %s)", step.Status)
	}

	// 获取模板中的步骤定义以检查权限
	template := o.templates[task.TemplateID]
	var stepTpl *TaskStepTemplate
	if template != nil {
		for i := range template.Steps {
			if i == task.CurrentStep {
				stepTpl = &template.Steps[i]
				break
			}
		}
	}

	// 权限检查
	handler, ok := o.handlers[step.Assignee]
	if !ok {
		o.mu.Unlock()
		return fmt.Errorf("no handler for role: %s", step.Assignee)
	}

	if stepTpl != nil {
		handlerPerm := handler.GetPermission()
		templatePerm := RolePermission(stepTpl.RolePermission)
		if !hasPermission(handlerPerm, templatePerm) {
			o.mu.Unlock()
			return fmt.Errorf("permission denied: handler has %s, step requires %s", handlerPerm, templatePerm)
		}
	}

	// 锁内设置运行态（避免与外部读取/完成逻辑并发写）
	step.Status = StatusRunning
	now := time.Now()
	step.StartedAt = &now
	step.Input = buildStepInput(task, task.CurrentStep)
	o.mu.Unlock()

	// 异步执行
	go func() {
		err := handler.Handle(ctx, task, step)
		if err != nil {
			o.completeStepWithError(taskID, step.ID, err.Error())
			return
		}
		o.applyStepResult(ctx, taskID, step.ID, stepTpl)
	}()

	return nil
}

// setStepOutput 在锁内写入步骤输出（供 handler 使用，保证与外部读取同步）
func (o *Orchestrator) setStepOutput(taskID, stepID string, output interface{}) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if task, ok := o.tasks[taskID]; ok {
		for i := range task.Steps {
			if task.Steps[i].ID == stepID {
				task.Steps[i].Output = output
				return
			}
		}
	}
}

// applyStepResult 在锁内完成步骤结果落定：否决判定/决策落库/执行前置校验/标记成功，
// 然后锁外推进到下一步（自动触发后续步骤执行）。
func (o *Orchestrator) applyStepResult(ctx context.Context, taskID, stepID string, stepTpl *TaskStepTemplate) {
	o.mu.Lock()
	task, ok := o.tasks[taskID]
	if !ok {
		o.mu.Unlock()
		return
	}
	var step *TaskStep
	for i := range task.Steps {
		if task.Steps[i].ID == stepID {
			step = &task.Steps[i]
			break
		}
	}
	if step == nil {
		o.mu.Unlock()
		return
	}

	// 否决判定：Risk 输出 *VetoResult 且 Vetoed → 任务终止
	if stepTpl != nil && stepTpl.CanVeto {
		if vr, ok := step.Output.(*VetoResult); ok && vr.Vetoed {
			step.Status = StatusFailed
			step.Error = fmt.Sprintf("VETO: %s", vr.Reason)
			task.VetoResult = vr
			task.Status = StatusFailed
			task.UpdatedAt = time.Now()
			o.mu.Unlock()
			log.Printf("[Orchestrator] VETO triggered for task %s: %s", taskID, vr.Reason)
			return
		}
	}

	// 决策步骤：CIO 输出 *DecisionObject → 落库到任务
	if stepTpl != nil && stepTpl.CanApprove {
		if decision, ok := step.Output.(*DecisionObject); ok {
			task.Decision = decision
			step.Output = decision
		}
	}

	// 执行步骤前置校验：无 CIO 决策不允许执行
	if stepTpl != nil && stepTpl.CanExecute && task.Decision == nil {
		step.Status = StatusFailed
		step.Error = "cannot execute without approved decision"
		task.Status = StatusFailed
		task.UpdatedAt = time.Now()
		o.mu.Unlock()
		log.Printf("[Orchestrator] Step failed: %s (task: %s, error: cannot execute without approved decision)", stepID, taskID)
		return
	}

	// 标记步骤成功
	step.Status = StatusSuccess
	now := time.Now()
	step.EndedAt = &now
	task.UpdatedAt = now
	o.mu.Unlock()
	log.Printf("[Orchestrator] Step completed: %s (task: %s)", stepID, taskID)

	// 推进到下一步（advanceStep 内部自行加锁并自动触发后续步骤）
	o.advanceStep(ctx, taskID)
}

func (o *Orchestrator) completeStepWithError(taskID, stepID, errMsg string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	task, ok := o.tasks[taskID]
	if !ok {
		return
	}
	for i := range task.Steps {
		if task.Steps[i].ID == stepID {
			task.Steps[i].Status = StatusFailed
			task.Steps[i].Error = errMsg
			task.Status = StatusFailed
			now := time.Now()
			task.Steps[i].EndedAt = &now
			task.UpdatedAt = now
			log.Printf("[Orchestrator] Step failed: %s (task: %s, error: %s)", stepID, taskID, errMsg)
			break
		}
	}
}

func (o *Orchestrator) advanceStep(ctx context.Context, taskID string) {
	// 先加锁推进 CurrentStep 与任务终态判断
	o.mu.Lock()
	task, ok := o.tasks[taskID]
	if !ok {
		o.mu.Unlock()
		return
	}

	task.CurrentStep++
	if task.CurrentStep >= len(task.Steps) {
		task.Status = StatusSuccess
		now := time.Now()
		task.CompletedAt = &now
		log.Printf("[Orchestrator] Task completed: %s", taskID)
		o.mu.Unlock()
		return
	}

	nextStep := &task.Steps[task.CurrentStep]
	if nextStep.Status != StatusPending {
		o.mu.Unlock()
		return
	}

	// 可选步骤：跳过并继续
	template := o.templates[task.TemplateID]
	if template != nil {
		for i, st := range template.Steps {
			if i == task.CurrentStep && !st.Required {
				nextStep.Status = StatusSkipped
				log.Printf("[Orchestrator] Optional step skipped: %s", nextStep.Name)
				o.mu.Unlock()
				o.advanceStep(ctx, taskID) // 递归跳过可选步骤
				return
			}
		}
	}
	nextStep.Status = StatusPending
	log.Printf("[Orchestrator] Advanced to step %d: %s (assignee: %s)", task.CurrentStep, nextStep.Name, nextStep.Assignee)
	o.mu.Unlock()

	// 自动推进：启动下一步的执行（锁外调用，避免与 ExecuteNextStep 的 RLock 死锁）
	if err := o.ExecuteNextStep(ctx, taskID); err != nil {
		log.Printf("[Orchestrator] Auto-advance to step %d failed: %v", task.CurrentStep+1, err)
	}
}

// CancelTask 取消任务
func (o *Orchestrator) CancelTask(taskID string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	task, ok := o.tasks[taskID]
	if !ok {
		return fmt.Errorf("task not found: %s", taskID)
	}
	task.Status = StatusFailed
	now := time.Now()
	task.CompletedAt = &now
	task.UpdatedAt = now
	log.Printf("[Orchestrator] Task cancelled: %s", taskID)
	return nil
}

// ============ 查询方法============

// GetTaskProgress 获取任务进度（锁内快照，避免与异步执行写冲突）
func (o *Orchestrator) GetTaskProgress(taskID string) map[string]interface{} {
	o.mu.RLock()
	task, ok := o.tasks[taskID]
	if !ok {
		o.mu.RUnlock()
		return nil
	}

	// 收集字段快照（锁内完成，防止与 handler goroutine 并发写）
	id := task.ID
	name := task.Name
	status := string(task.Status)
	currentStep := task.CurrentStep
	isEvent := task.IsEventDriven
	decision := task.Decision
	vetoResult := task.VetoResult
	createdAt := task.CreatedAt.Format(time.RFC3339)
	updatedAt := task.UpdatedAt.Format(time.RFC3339)

	completed := 0
	total := len(task.Steps)
	for _, step := range task.Steps {
		if step.Status == StatusSuccess || step.Status == StatusSkipped {
			completed++
		}
	}
	o.mu.RUnlock()

	return map[string]interface{}{
		"task_id":      id,
		"name":         name,
		"status":       status,
		"progress":     float64(completed) / float64(total) * 100,
		"completed":    completed,
		"total":        total,
		"current_step": currentStep,
		"is_event":     isEvent,
		"has_decision": decision != nil,
		"has_veto":     vetoResult != nil && vetoResult.Vetoed,
		"decision":     decision,
		"veto_result":  vetoResult,
		"created_at":   createdAt,
		"updated_at":   updatedAt,
	}
}

// GetOrchestratorStats 获取编排引擎统计
func (o *Orchestrator) GetOrchestratorStats() map[string]interface{} {
	o.mu.RLock()
	defer o.mu.RUnlock()

	stats := map[string]interface{}{
		"total_tasks":      len(o.tasks),
		"total_templates":  len(o.templates),
		"registered_roles": len(o.handlers),
		"task_status":      make(map[string]int),
		"workflow_types":   make(map[string]int),
	}

	for _, task := range o.tasks {
		status := string(task.Status)
		stats["task_status"].(map[string]int)[status]++
		if task.IsEventDriven {
			stats["workflow_types"].(map[string]int)["EVENT"]++
		} else {
			stats["workflow_types"].(map[string]int)[task.TemplateID]++
		}
	}

	return stats
}

// ============ 辅助函数============

// deepCopy 深拷贝接口值，避免返回的拷贝与任务内部共享底层数据导致竞态。
// 支持 map/slice 递归拷贝以及 *VetoResult/*DecisionObject 值拷贝，其余类型原样返回。
func deepCopy(v interface{}) interface{} {
	if v == nil {
		return nil
	}
	switch t := v.(type) {
	case map[string]interface{}:
		m := make(map[string]interface{}, len(t))
		for k, val := range t {
			m[k] = deepCopy(val)
		}
		return m
	case []interface{}:
		s := make([]interface{}, len(t))
		for i, val := range t {
			s[i] = deepCopy(val)
		}
		return s
	case []string:
		s := make([]string, len(t))
		copy(s, t)
		return s
	case *VetoResult:
		if t == nil {
			return nil
		}
		c := *t
		return &c
	case *DecisionObject:
		if t == nil {
			return nil
		}
		c := *t
		c.Signals = append([]string{}, t.Signals...)
		c.TargetStocks = append([]string{}, t.TargetStocks...)
		return &c
	default:
		return v
	}
}

func hasPermission(handlerPerm, templatePerm RolePermission) bool {
	// 权限链：PROPOSE → EVIDENCE → VETO → DECIDE → EXECUTE
	// Handler 必须匹配步骤要求的权限
	return handlerPerm == templatePerm
}

func buildStepInput(task *WorkflowTask, stepIndex int) interface{} {
	input := map[string]interface{}{
		"task_id":     task.ID,
		"template_id": task.TemplateID,
		"step_index":  stepIndex,
		"task_name":   task.Name,
	}

	// 收集前序步骤的输出
	var previousOutputs []interface{}
	for i := 0; i < stepIndex; i++ {
		if task.Steps[i].Output != nil {
			previousOutputs = append(previousOutputs, task.Steps[i].Output)
		}
	}
	if len(previousOutputs) > 0 {
		input["previous_outputs"] = previousOutputs
	}

	// 添加已有决策（如果有）
	if task.Decision != nil {
		input["existing_decision"] = task.Decision
	}

	// 添加事件数据（如果是事件驱动）
	if task.IsEventDriven && task.EventData != nil {
		input["event_data"] = task.EventData
	}

	return input
}
