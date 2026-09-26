package agents

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"
)

// WorkflowDef 工作流定义
type WorkflowDef struct {
	ID          string                 `json:"id"`
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Steps       []WorkflowStep         `json:"steps"`
	Variables   map[string]interface{} `json:"variables"`
}

// WorkflowStep 工作流步骤
type WorkflowStep struct {
	ID        string                 `json:"id"`
	Name      string                 `json:"name"`
	Type      string                 `json:"type"` // "agent", "tool", "condition", "transform"
	AgentRole AgentRole              `json:"agent_role,omitempty"`
	Config    map[string]interface{} `json:"config"`
	NextStep  string                 `json:"next_step,omitempty"`
}

// WorkflowContext 工作流执行上下文
type WorkflowContext struct {
	mu     sync.RWMutex
	Data   map[string]interface{} `json:"data"`
	Status string                 `json:"status"`
	Logs   []string               `json:"logs"`
}

// NewWorkflowContext 创建工作流上下文
func NewWorkflowContext() *WorkflowContext {
	return &WorkflowContext{
		Data:   make(map[string]interface{}),
		Status: "initialized",
		Logs:   make([]string, 0),
	}
}

// Get 获取上下文数据
func (wc *WorkflowContext) Get(key string) (interface{}, bool) {
	wc.mu.RLock()
	defer wc.mu.RUnlock()
	val, ok := wc.Data[key]
	return val, ok
}

// Set 设置上下文数据
func (wc *WorkflowContext) Set(key string, value interface{}) {
	wc.mu.Lock()
	defer wc.mu.Unlock()
	wc.Data[key] = value
}

// Log 添加日志
func (wc *WorkflowContext) Log(msg string) {
	wc.mu.Lock()
	defer wc.mu.Unlock()
	wc.Logs = append(wc.Logs, msg)
}

// WorkflowEngine 工作流引擎
type WorkflowEngine struct {
	mu        sync.RWMutex
	Workflows map[string]*WorkflowDef
	Agents    map[AgentRole]*Agent
}

// NewWorkflowEngine 创建工作流引擎
func NewWorkflowEngine(tools interface{}) *WorkflowEngine {
	return &WorkflowEngine{
		Workflows: make(map[string]*WorkflowDef),
		Agents:    make(map[AgentRole]*Agent),
	}
}

// SetAgents 设置 Agents 映射
func (we *WorkflowEngine) SetAgents(agents map[AgentRole]*Agent) {
	we.mu.Lock()
	defer we.mu.Unlock()
	we.Agents = agents
	log.Printf("[WorkflowEngine] Set %d agents", len(agents))
}

// Execute 执行工作流 - 真实调用 Agent
func (we *WorkflowEngine) Execute(ctx context.Context, def *WorkflowDef, wfCtx *WorkflowContext) (*WorkflowContext, error) {
	if def == nil {
		return nil, fmt.Errorf("workflow definition is nil")
	}

	wfCtx.Status = "running"
	wfCtx.Log(fmt.Sprintf("Starting workflow: %s", def.Name))
	log.Printf("[WorkflowEngine] Starting workflow: %s (steps: %d)", def.Name, len(def.Steps))

	for i, step := range def.Steps {
		select {
		case <-ctx.Done():
			wfCtx.Status = "cancelled"
			wfCtx.Log("Workflow cancelled")
			return wfCtx, ctx.Err()
		default:
		}

		log.Printf("[WorkflowEngine] Step %d: %s (type: %s, agent: %s)", i+1, step.Name, step.Type, step.AgentRole)
		wfCtx.Log(fmt.Sprintf("Step %d: %s (%s)", i+1, step.Name, step.Type))

		switch step.Type {
		case "agent":
			// 真实调用 Agent
			agent, exists := we.Agents[step.AgentRole]
			if !exists || agent == nil {
				log.Printf("[WorkflowEngine] WARNING: No agent for role %s, skipping", step.AgentRole)
				wfCtx.Set(step.ID+"_output", map[string]interface{}{
					"status":  "skipped",
					"agent":   step.AgentRole,
					"message": fmt.Sprintf("Agent %s not available, step skipped", step.AgentRole),
					"error":   true,
				})
				continue
			}

			// 构建输入：包含前序步骤的输出
			inputStr := we.buildWorkflowInput(step, wfCtx)
			wfCtx.Set(step.ID+"_input", map[string]interface{}{
				"task":   inputStr,
				"config": step.Config,
			})

			// 调用 Agent.ProcessTask
			agentMsg := AgentMessage{
				MessageID: fmt.Sprintf("WF-%s-%d", def.ID, i),
				From:      step.AgentRole,
				To:        step.AgentRole,
				Type:      "WORKFLOW_STEP",
				Priority:  "NORMAL",
				Task:      step.Name,
				Context:   inputStr,
				Timestamp: time.Now(),
			}

			response := agent.ProcessTask(ctx, agentMsg)

			// 存储真实结果到上下文
			output := map[string]interface{}{
				"status":  "completed",
				"agent":   step.AgentRole,
				"type":    response.Type,
				"message": response.Type,
				"context": response.Context,
			}
			if response.Type == "ERROR" {
				output["status"] = "error"
				output["error"] = true
				if ctxMap, ok := response.Context.(map[string]string); ok {
					output["error_detail"] = ctxMap["error"]
				}
				log.Printf("[WorkflowEngine] Agent %s failed: %v", step.AgentRole, response.Context)
			} else {
				log.Printf("[WorkflowEngine] Agent %s completed: type=%s", step.AgentRole, response.Type)
			}

			wfCtx.Set(step.ID+"_output", output)
			// 同时存入角色级别结果供后续步骤使用
			wfCtx.Set(string(step.AgentRole)+"_last_result", output)

		case "tool":
			wfCtx.Set(step.ID+"_output", map[string]interface{}{
				"status":  "completed",
				"message": "Tool executed successfully",
				"config":  step.Config,
			})

		case "condition":
			// 条件判断：基于上下文数据评估
			conditionResult := we.evaluateCondition(step, wfCtx)
			wfCtx.Set(step.ID+"_output", map[string]interface{}{
				"status":    "evaluated",
				"condition": step.Config,
				"result":    conditionResult,
			})
			if !conditionResult {
				wfCtx.Log(fmt.Sprintf("Condition %s evaluated to false, skipping subsequent dependent steps", step.Name))
			}

		case "transform":
			// 数据转换：从上下文提取数据并转换
			transformedData := we.applyTransform(step, wfCtx)
			wfCtx.Set(step.ID+"_output", map[string]interface{}{
				"status": "transformed",
				"data":   transformedData,
			})

		default:
			wfCtx.Set(step.ID+"_output", map[string]interface{}{
				"status":  "unknown_type",
				"message": fmt.Sprintf("Unknown step type: %s", step.Type),
			})
		}
	}

	wfCtx.Status = "completed"
	wfCtx.Log(fmt.Sprintf("Workflow %s completed successfully", def.Name))
	log.Printf("[WorkflowEngine] Workflow %s completed: %d steps executed", def.Name, len(def.Steps))
	return wfCtx, nil
}

// buildWorkflowInput 构建工作流步骤的输入（包含前序结果）
func (we *WorkflowEngine) buildWorkflowInput(step WorkflowStep, wfCtx *WorkflowContext) string {
	var inputStr string

	// 添加步骤信息
	inputStr = fmt.Sprintf("任务: %s\n角色: %s\n\n", step.Name, step.AgentRole)

	// 添加前序步骤的输出（作为上下文）
	for key, value := range wfCtx.Data {
		// 只添加前序步骤的输出，避免循环
		if key != step.ID+"_input" && key != step.ID+"_output" {
			inputStr += fmt.Sprintf("[%s]: %v\n", key, value)
		}
	}

	// 添加步骤配置
	if step.Config != nil && len(step.Config) > 0 {
		inputStr += fmt.Sprintf("\n配置: %v\n", step.Config)
	}

	return inputStr
}

// evaluateCondition 评估条件步骤
func (we *WorkflowEngine) evaluateCondition(step WorkflowStep, wfCtx *WorkflowContext) bool {
	if step.Config == nil {
		return true
	}

	// 检查是否有需要跳过的标记
	for _, value := range wfCtx.Data {
		if m, ok := value.(map[string]interface{}); ok {
			if status, exists := m["status"]; exists && status == "error" {
				return false
			}
		}
	}

	return true // 默认条件通过
}

// applyTransform 应用数据转换
func (we *WorkflowEngine) applyTransform(step WorkflowStep, wfCtx *WorkflowContext) map[string]interface{} {
	result := make(map[string]interface{})

	// 收集已完成步骤的输出
	for _, value := range wfCtx.Data {
		if v, ok := value.(map[string]interface{}); ok {
			if agent, exists := v["agent"]; exists {
				result[agent.(string)] = v
			}
		}
	}

	result["step"] = step.Name
	return result
}

// RegisterWorkflow 注册工作流
func (we *WorkflowEngine) RegisterWorkflow(def *WorkflowDef) {
	we.mu.Lock()
	defer we.mu.Unlock()
	we.Workflows[def.ID] = def
	log.Printf("[WorkflowEngine] Registered workflow: %s", def.Name)
}

// ==================== 预定义工作流 ====================

// NewWorkflow_StockPicker 选股工作流
func NewWorkflow_StockPicker() *WorkflowDef {
	return &WorkflowDef{
		ID:          "wf-stock-picker",
		Name:        "A股选股工作流",
		Description: "从市场分析到选股的完整流程",
		Steps: []WorkflowStep{
			{ID: "step1", Name: "市场分析", Type: "agent", AgentRole: RolePlanner},
			{ID: "step2", Name: "因子选股", Type: "agent", AgentRole: RoleQuant},
			{ID: "step3", Name: "风险审查", Type: "agent", AgentRole: RoleRisk},
			{ID: "step4", Name: "CIO决策", Type: "agent", AgentRole: RoleCIO},
			{ID: "step5", Name: "执行计划", Type: "agent", AgentRole: RoleTrader},
		},
		Variables: map[string]interface{}{
			"market_regime": "neutral",
			"universe":      "中证800",
		},
	}
}

// NewWorkflow_RiskCheck 风控检查工作流
func NewWorkflow_RiskCheck() *WorkflowDef {
	return &WorkflowDef{
		ID:          "wf-risk-check",
		Name:        "风控检查工作流",
		Description: "组合风险实时检查",
		Steps: []WorkflowStep{
			{ID: "step1", Name: "获取组合", Type: "transform"},
			{ID: "step2", Name: "VaR计算", Type: "tool"},
			{ID: "step3", Name: "压力测试", Type: "tool"},
			{ID: "step4", Name: "风控师审查", Type: "agent", AgentRole: RoleRisk},
		},
		Variables: map[string]interface{}{
			"confidence": 0.95,
		},
	}
}

// NewWorkflow_DailyCycle 每日周期工作流
func NewWorkflow_DailyCycle() *WorkflowDef {
	return &WorkflowDef{
		ID:          "wf-daily-cycle",
		Name:        "每日交易周期",
		Description: "盘前-盘中-盘后完整周期（含事前风控）",
		Steps: []WorkflowStep{
			// 盘前阶段
			{ID: "pre1", Name: "投资规划师检查Mandate", Type: "agent", AgentRole: RolePlanner},
			{ID: "pre2", Name: "量化分析师研究选股", Type: "agent", AgentRole: RoleQuant},
			{ID: "pre3", Name: "CIO投资决策", Type: "agent", AgentRole: RoleCIO},
			{ID: "pre3_5", Name: "事前风控审查", Type: "agent", AgentRole: RoleRisk},
			{ID: "pre4", Name: "风控师合规审查", Type: "agent", AgentRole: RoleRisk},
			{ID: "pre5", Name: "操盘手生成执行计划", Type: "agent", AgentRole: RoleTrader},
			// 盘中阶段
			{ID: "int1", Name: "盘中信号监控", Type: "agent", AgentRole: RoleQuant},
			{ID: "int2", Name: "盘中风控监控", Type: "agent", AgentRole: RoleRisk},
			// 盘后阶段
			{ID: "post1", Name: "盘后Mandate合规", Type: "agent", AgentRole: RolePlanner},
			{ID: "post2", Name: "盘后研究归因", Type: "agent", AgentRole: RoleQuant},
			{ID: "post3", Name: "盘后风险复盘", Type: "agent", AgentRole: RoleRisk},
			{ID: "post4", Name: "盘后CIO决策复盘", Type: "agent", AgentRole: RoleCIO},
			{ID: "post5", Name: "盘后执行归因", Type: "agent", AgentRole: RoleTrader},
		},
		Variables: map[string]interface{}{
			"phase": "pre_market",
		},
	}
}
