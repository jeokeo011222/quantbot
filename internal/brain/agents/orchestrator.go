package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/quantpilot/quantpilot/internal/brain/llm"
	"github.com/quantpilot/quantpilot/internal/brain/port"
)

// Orchestrator 编排器（升级版Runtime）：整合 Skill + Trajectory + Workflow + SubAgent
type Orchestrator struct {
	mu sync.RWMutex

	// 核心组件
	Store     port.Persistence
	LLM       llm.Client
	SkillReg  *SkillRegistry
	ToolReg   *ToolRegistry
	TrajStore *TrajectoryStore
	WFEngine  *WorkflowEngine

	// Agent实例
	Agents     map[AgentRole]*Agent
	BaseAgents map[string]*BaseAgent

	// ContextProvider 可选的真实系统上下文构建器：由宿主（harness）注入，
	// 为 buildAgentInput 提供大盘行情摘要、组合快照、持仓明细、因子/策略等真实数据，
	// 避免 LLM 分析 Agent 反复调用工具取数。返回空字符串表示无上下文。可为 nil。
	ContextProvider port.ContextProvider

	// 运行状态
	running      bool
	currentCycle *OrchestratorResult
}

// OrchestratorResult 编排执行结果
type OrchestratorResult struct {
	Date         string                   `json:"date"`
	StartTime    time.Time                `json:"start_time"`
	EndTime      time.Time                `json:"end_time"`
	Trajectories map[string]*Trajectory   `json:"trajectories"`
	Sessions     map[string]*SessionState `json:"sessions"`
	WorkflowCtx  *WorkflowContext         `json:"workflow_context"`
	Decision     interface{}              `json:"decision"`
	Errors       []string                 `json:"errors"`
	TotalTokens  int                      `json:"total_tokens"`
}

// NewOrchestrator 创建编排器
func NewOrchestrator(store port.Persistence, llmClient llm.Client) *Orchestrator {
	return &Orchestrator{
		Store:      store,
		LLM:        llmClient,
		SkillReg:   NewSkillRegistry(),
		ToolReg:    NewToolRegistry(),
		TrajStore:  NewTrajectoryStore(2000),
		WFEngine:   NewWorkflowEngine(nil),
		Agents:     make(map[AgentRole]*Agent),
		BaseAgents: make(map[string]*BaseAgent),
	}
}

// RegisterAgent 注册Agent（Runtime Agent）- 同时同步到 WorkflowEngine
func (o *Orchestrator) RegisterAgent(agent *Agent) {
	o.mu.Lock()
	defer o.mu.Unlock()

	o.Agents[agent.Role] = agent
	// 同步到 WorkflowEngine，确保工作流能真实调用 Agent
	o.WFEngine.SetAgents(o.Agents)
	log.Printf("[Orchestrator] Registered agent: %s (%s) → synced to WorkflowEngine", agent.Name, agent.Role)
}

// RegisterBaseAgent 注册BaseAgent
func (o *Orchestrator) RegisterBaseAgent(agent *BaseAgent) {
	o.mu.Lock()
	defer o.mu.Unlock()

	o.BaseAgents[agent.ID] = agent
	log.Printf("[Orchestrator] Registered base agent: %s", agent.Name)
}

// RegisterSkill 注册技能
func (o *Orchestrator) RegisterSkill(skill *Skill) error {
	return o.SkillReg.Register(skill)
}

// SetWorkflowEngine 设置工作流引擎（含工具）
func (o *Orchestrator) SetWorkflowEngine(tools []interface{}) {
	// 将interface{}切片转换为toolpkg.Tool切片
	// 同时确保 Agents 已同步到 WorkflowEngine
	o.WFEngine.SetAgents(o.Agents)
	log.Printf("[Orchestrator] Workflow engine updated with %d tools, %d agents synced", len(tools), len(o.Agents))
}

// RunDailyCycle 运行每日分析周期（基于Skill+Trajectory）
func (o *Orchestrator) RunDailyCycle(ctx context.Context) (*OrchestratorResult, error) {
	o.mu.Lock()
	if o.running {
		o.mu.Unlock()
		return nil, fmt.Errorf("分析周期正在运行中")
	}
	o.running = true
	o.mu.Unlock()

	defer func() {
		o.mu.Lock()
		o.running = false
		o.mu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	now := time.Now()
	today := now.Format("2006-01-02")
	isPreMarket := now.Hour() == 9 && now.Minute() >= 15
	isPostMarket := now.Hour() == 15 && now.Minute() < 15
	taskPhase := "PRE_MARKET"
	if isPostMarket {
		taskPhase = "POST_MARKET"
	} else if !isPreMarket {
		// 默认按盘前处理
		taskPhase = "PRE_MARKET"
	}

	result := &OrchestratorResult{
		Date:         today,
		StartTime:    now,
		Trajectories: make(map[string]*Trajectory),
		Sessions:     make(map[string]*SessionState),
		Errors:       make([]string, 0),
		WorkflowCtx:  NewWorkflowContext(),
	}

	log.Printf("[Orchestrator] ============ 开始A股每日分析周期: %s (阶段: %s) ============", result.Date, taskPhase)

	// 初始化任务日志记录器
	if o.Store != nil {
		// 清理今日旧的任务日志（重新生成时）
		o.Store.ClearTaskLogs(today)
	}

	// 按Skill运行各Agent
	roles := []AgentRole{RolePlanner, RoleQuant, RoleRisk, RoleCIO, RoleTrader}

	for order, role := range roles {
		agent, ok := o.Agents[role]
		if !ok {
			log.Printf("[Orchestrator] No agent for role: %s", role)
			continue
		}

		baseAgent, ok := o.BaseAgents[string(role)]
		if !ok {
			log.Printf("[Orchestrator] No base agent for role: %s", role)
			continue
		}

		// 记录任务开始
		var taskLog *port.TaskLog
		if o.Store != nil {
			taskLog = o.Store.LogTaskStart(today, taskPhase, string(role), agent.Name, order+1)
		}

		// 构建输入
		input := o.buildAgentInput(role, result)

		// 更新Agent状态
		agent.SetState(StateThinking)

		// 执行带轨迹的运行
		session, traj, err := baseAgent.RunWithTrajectory(ctx, input)

		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("%s失败: %v", agent.Name, err))
			log.Printf("[Orchestrator] %s error: %v", agent.Name, err)
			agent.SetState(StateFailed)

			// 记录任务失败
			if o.Store != nil && taskLog != nil {
				o.Store.LogTaskFailed(taskLog.ID, err.Error())
			}
			continue
		}

		// 保存轨迹
		o.TrajStore.Save(traj)
		result.Trajectories[string(role)] = traj
		result.Sessions[string(role)] = session
		result.TotalTokens += session.TokenUsage.TotalTokens

		// 将结果存入工作流上下文
		result.WorkflowCtx.Set(string(role)+"_result", session.Decision)

		// 更新Agent状态
		agent.SetState(StateCompleted)

		// 记录任务完成并保存交付物
		if o.Store != nil && taskLog != nil {
			deliverableName := agent.Name + "交付物"
			deliverableData := map[string]interface{}{
				"decision":   session.Decision,
				"confidence": session.Confidence,
				"tokens":     session.TokenUsage.TotalTokens,
			}
			summary := fmt.Sprintf("置信度=%.2f, Tokens=%d", session.Confidence, session.TokenUsage.TotalTokens)
			o.Store.LogTaskComplete(taskLog.ID, "AGENT_RESULT", deliverableName, deliverableData, summary)
		}

		log.Printf("[Orchestrator] %s完成: 置信度 %.2f, tokens %d",
			agent.Name, session.Confidence, session.TokenUsage.TotalTokens)

		// CIO 最终决策落库到 CIO_DECISION_LOGS 表，使「投资管理」页
		// (GetTodayDecisions 读该表)能读取到今日盘前决策，与实时活动页保持一致。
		if role == RoleCIO && session != nil && session.Decision != nil {
			o.persistOrchestratorCIODecision(today, taskPhase, session)
		}
	}

	// 生成最终决策
	result.Decision = o.buildFinalDecision(result)
	result.EndTime = time.Now()
	o.currentCycle = result

	log.Printf("[Orchestrator] ============ 分析完成 ============")
	log.Printf("[Orchestrator] 耗时: %v, 错误: %d, 总tokens: %d",
		result.EndTime.Sub(result.StartTime), len(result.Errors), result.TotalTokens)

	return result, nil
}

// RunWorkflow 运行指定工作流
func (o *Orchestrator) RunWorkflow(ctx context.Context, workflowID string) (*WorkflowContext, error) {
	workflows := map[string]*WorkflowDef{
		"wf-stock-picker": NewWorkflow_StockPicker(),
		"wf-risk-check":   NewWorkflow_RiskCheck(),
		"wf-daily-cycle":  NewWorkflow_DailyCycle(),
	}

	def, ok := workflows[workflowID]
	if !ok {
		return nil, fmt.Errorf("workflow not found: %s", workflowID)
	}

	wfCtx := NewWorkflowContext()
	return o.WFEngine.Execute(ctx, def, wfCtx)
}

// GetCurrentCycle 获取当前周期结果
func (o *Orchestrator) GetCurrentCycle() *OrchestratorResult {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.currentCycle
}

// GetTrajectories 获取轨迹列表
func (o *Orchestrator) GetTrajectories(limit int) []*Trajectory {
	return o.TrajStore.ListAll(limit)
}

// GetTrajectoryStats 获取轨迹统计
func (o *Orchestrator) GetTrajectoryStats() map[string]interface{} {
	return o.TrajStore.Stats()
}

// GetSkills 获取所有技能
func (o *Orchestrator) GetSkills() []*Skill {
	return o.SkillReg.List()
}

// GetTools 获取所有工具元数据
func (o *Orchestrator) GetTools() []*ToolMetadata {
	return o.ToolReg.List()
}

// OrchestrateSubAgent 子Agent调度入口
func (o *Orchestrator) OrchestrateSubAgent(ctx context.Context, parentRole, subAgentName string, task string) (*SessionState, error) {
	parentAgent, ok := o.BaseAgents[string(parentRole)]
	if !ok {
		return nil, fmt.Errorf("parent agent not found: %s", parentRole)
	}

	return parentAgent.DispatchSubAgent(ctx, subAgentName, task)
}

// buildAgentInput 构建Agent输入（基于前序结果）
func (o *Orchestrator) buildAgentInput(role AgentRole, result *OrchestratorResult) string {
	baseInput := fmt.Sprintf("日期: %s\n\n", result.Date)

	// 注入真实系统上下文（大盘行情/组合快照/持仓/因子等），让 LLM 基于真实数据分析，
	// 而不是反复调用工具去取数。上下文构建失败时为空字符串，不影响后续流程。
	if o.ContextProvider != nil {
		if ctxData := o.ContextProvider(context.Background(), string(role), result.Date); ctxData != "" {
			baseInput += "【实时系统上下文（真实数据，仅作参考，如需更细数据请调用对应工具）】\n" + ctxData + "\n\n"
		}
	}

	switch role {
	case RolePlanner:
		return baseInput + "请分析当前A股市场环境，制定今日投资规划和Investment Mandate。"
	case RoleQuant:
		plannerResult, _ := result.WorkflowCtx.Get("PLANNER_result")
		return baseInput + fmt.Sprintf("投资规划: %v\n\n请进行A股Alpha因子选股，寻找具备超额收益潜力的股票。", plannerResult)
	case RoleRisk:
		quantResult, _ := result.WorkflowCtx.Get("QUANT_result")
		return baseInput + fmt.Sprintf("选股信号: %v\n\n请评估组合风险，检查是否符合风控要求。", quantResult)
	case RoleCIO:
		plannerResult, _ := result.WorkflowCtx.Get("PLANNER_result")
		quantResult, _ := result.WorkflowCtx.Get("QUANT_result")
		riskResult, _ := result.WorkflowCtx.Get("RISK_result")
		return baseInput + fmt.Sprintf(
			"规划: %v\n选股: %v\n风控: %v\n\n请综合以上信息，做出最终投资决策。",
			plannerResult, quantResult, riskResult)
	case RoleTrader:
		cioResult, _ := result.WorkflowCtx.Get("CIO_result")
		return baseInput + fmt.Sprintf("CIO决策: %v\n\n请将决策转化为具体交易执行计划。", cioResult)
	default:
		return baseInput
	}
}

// persistOrchestratorCIODecision 将 CIO Agent 的决策持久化到 CIO_DECISION_LOGS 表。
// reportupdater 前端「投资管理」页(GetTodayDecisions 读 CIODecisionLog)需要读取今日盘前决策，
// 而 Orchestrator 决策链此刻才产出最终 CIO 决策。DecisionID 唯一，重复落库自动跳过。
func (o *Orchestrator) persistOrchestratorCIODecision(today, taskPhase string, session *SessionState) {
	if o.Store == nil {
		return
	}

	decisionText := "无"
	if s, ok := session.Decision.(string); ok && s != "" {
		decisionText = s
	} else if b, ok := session.Decision.([]byte); ok && len(b) > 0 {
		decisionText = string(b)
	}

	// 优先读取 CIO 的结构化输出（JSON 中的 action 字段），缺失时回退关键词推断。
	// 展示文本优先用结构化 summary/reason，否则退回完整决策原文。
	action, _ := parseStructuredDecision(session.Decision)
	timestamp := session.EndTime
	if timestamp.IsZero() {
		timestamp = time.Now()
	}

	decisionID := fmt.Sprintf("DEC-ORCH-%s-%s", today[:10], timestamp.Format("150405"))
	// Reason 统一使用决策展示文本 decisionText，与既有落库一致；DecisionID 唯一，重复落库由宿主去重跳过。
	if err := o.Store.SaveCIODecision(
		decisionID, "P001", action, decisionText, "APPROVED", "PASSED", taskPhase, session.Confidence, timestamp,
	); err != nil {
		log.Printf("[Orchestrator] 保存 CIO 决策到 CIO_DECISION_LOGS 失败: %v", err)
		return
	}
	log.Printf("[Orchestrator] ✓ CIO 决策已落库: %s (%s)", decisionID, action)
}

// parseStructuredDecision 优先从 CIO 的结构化 JSON 输出中直接读取 action 字段，
// 缺失或无法解析时回退到 inferDecisionAction 关键词推断。返回 (动作类型, 展示用决策文本)。
func parseStructuredDecision(decision interface{}) (string, string) {
	var text string
	switch d := decision.(type) {
	case string:
		text = d
	case []byte:
		text = string(d)
	default:
		text = fmt.Sprintf("%v", d)
	}

	if json.Valid([]byte(text)) {
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(text), &m); err == nil {
			// 展示文本：优先 summary + reason（结构化输出），避免前端直接看到整段 JSON
			var reason string
			if s, ok := m["summary"].(string); ok && s != "" {
				reason = s
			}
			if r, ok := m["reason"].(string); ok && r != "" {
				if reason != "" {
					reason += "；" + r
				} else {
					reason = r
				}
			}
			// 结构化动作：必须命中已定义枚举才采用
			if a, ok := m["action"].(string); ok {
				act := strings.ToUpper(strings.TrimSpace(a))
				switch act {
				case string(DecisionBuild), string(DecisionIncrease), string(DecisionReduce),
					string(DecisionHold), string(DecisionRebalance), string(DecisionNoAction),
					string(DecisionPauseTrading), string(DecisionReduceRisk), string(DecisionIncreaseRisk):
					return act, reason
				}
			}
		}
	}

	return inferDecisionAction(text), ""
}

// inferDecisionAction 从 CIO 决策文本推断动作类型（结构化输出缺失时的回退方案）
func inferDecisionAction(text string) string {
	lower := strings.ToLower(text)
	switch {
	case strings.Contains(lower, "卖出") || strings.Contains(lower, "减仓") ||
		strings.Contains(lower, "sell") || strings.Contains(lower, "reduce"):
		return string(DecisionReduce)
	case strings.Contains(lower, "建仓") || strings.Contains(lower, "买入") ||
		strings.Contains(lower, "加仓") || strings.Contains(lower, "buy") ||
		strings.Contains(lower, "increase"):
		return string(DecisionBuild)
	case strings.Contains(lower, "持有") || strings.Contains(lower, "保持") ||
		strings.Contains(lower, "hold") || strings.Contains(lower, "观望"):
		return string(DecisionNoAction)
	default:
		return string(DecisionNoAction)
	}
}

// buildFinalDecision 构建最终决策
func (o *Orchestrator) buildFinalDecision(result *OrchestratorResult) interface{} {
	cioSession, ok := result.Sessions[string(RoleCIO)]
	if ok && cioSession.Decision != nil {
		return map[string]interface{}{
			"source":     "CIO_decision",
			"decision":   cioSession.Decision,
			"confidence": cioSession.Confidence,
			"summary":    generateOrchestratorSummary(result),
			"timestamp":  time.Now().Format("2006-01-02T15:04:05Z07:00"),
		}
	}

	return map[string]interface{}{
		"source":     "auto_generated",
		"decision":   "review",
		"confidence": 0.5,
		"summary":    generateOrchestratorSummary(result),
		"timestamp":  time.Now().Format("2006-01-02T15:04:05Z07:00"),
	}
}

// generateOrchestratorSummary 生成摘要
func generateOrchestratorSummary(result *OrchestratorResult) string {
	summary := ""

	if len(result.Errors) > 0 {
		summary += fmt.Sprintf("注意: 有%d个分析环节出现错误; ", len(result.Errors))
	}

	if cioSession, ok := result.Sessions[string(RoleCIO)]; ok {
		summary += fmt.Sprintf("CIO决策置信度: %.0f%%; ", cioSession.Confidence*100)
	}

	if summary == "" {
		summary = "分析完成，建议人工审核后执行。"
	}
	return summary
}
