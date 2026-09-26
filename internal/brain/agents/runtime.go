package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/quantpilot/quantpilot/internal/brain/brainutil"
	"github.com/quantpilot/quantpilot/internal/brain/llm"
	"github.com/quantpilot/quantpilot/internal/brain/port"
)

// AgentState Agent状态机状态
type AgentState string

const (
	StateIdle         AgentState = "IDLE"
	StateThinking     AgentState = "THINKING"
	StateToolCalling  AgentState = "TOOL_CALLING"
	StateWaiting      AgentState = "WAITING"
	StateProposing    AgentState = "PROPOSING"
	StateReviewing    AgentState = "REVIEWING"
	StateCompleted    AgentState = "COMPLETED"
	StateFailed       AgentState = "FAILED"
	StateBlocked      AgentState = "BLOCKED"  // 数据缺失/前置条件不满足，任务被阻断
	StateSleeping     AgentState = "SLEEPING" // 非交易时段休眠
	StateApproved     AgentState = "APPROVED"
	StateOrderCreated AgentState = "ORDER_CREATED"
	StateSubmitted    AgentState = "SUBMITTED"
	StatePartialFill  AgentState = "PARTIAL_FILLED"
	StateFilled       AgentState = "FILLED"
	StateVerified     AgentState = "VERIFIED"
)

// AgentRole Agent角色
type AgentRole string

const (
	RoleCIO     AgentRole = "CIO"
	RolePlanner AgentRole = "PLANNER"
	RoleQuant   AgentRole = "QUANT"
	RoleRisk    AgentRole = "RISK"
	RoleTrader  AgentRole = "TRADER"
)

// DecisionType CIO决策类型
type DecisionType string

const (
	DecisionNoAction          DecisionType = "NO_ACTION"
	DecisionBuild             DecisionType = "BUILD"    // 建仓：新建仓位
	DecisionIncrease          DecisionType = "INCREASE" // 加仓：增持现有持仓
	DecisionReduce            DecisionType = "REDUCE"   // 减仓：降低现有持仓
	DecisionHold              DecisionType = "HOLD"     // 持有：维持现有仓位
	DecisionRebalance         DecisionType = "REBALANCE"
	DecisionReduceRisk        DecisionType = "REDUCE_RISK"
	DecisionIncreaseRisk      DecisionType = "INCREASE_RISK"
	DecisionChangeStrategy    DecisionType = "CHANGE_STRATEGY"
	DecisionPauseStrategy     DecisionType = "PAUSE_STRATEGY"
	DecisionPauseTrading      DecisionType = "PAUSE_TRADING"
	DecisionResumeTrading     DecisionType = "RESUME_TRADING"
	DecisionRequestResearch   DecisionType = "REQUEST_RESEARCH"
	DecisionRequestRiskReview DecisionType = "REQUEST_RISK_REVIEW"
)

// RiskDecisionType 风控决策类型
type RiskDecisionType string

const (
	RiskApprove          RiskDecisionType = "APPROVE"
	RiskApproveWithLimit RiskDecisionType = "APPROVE_WITH_LIMIT"
	RiskReviewRequired   RiskDecisionType = "REVIEW_REQUIRED"
	RiskReject           RiskDecisionType = "REJECT"
	RiskEmergencyStop    RiskDecisionType = "EMERGENCY_STOP"
)

// AgentMessage Agent间消息协议
type AgentMessage struct {
	MessageID string      `json:"message_id"`
	From      AgentRole   `json:"from"`
	To        AgentRole   `json:"to"`
	Type      string      `json:"type"`
	Priority  string      `json:"priority"`
	Task      interface{} `json:"task"`
	Context   interface{} `json:"context"`
	Timestamp time.Time   `json:"timestamp"`
}

// OrderIntent 订单意图（不直接产生Broker订单）
type OrderIntent struct {
	Symbol       string  `json:"symbol"`
	TargetWeight float64 `json:"target_weight"`
	Side         string  `json:"side"`
	MaxNotional  float64 `json:"max_notional"`
	Reason       string  `json:"reason"`
	DecisionID   string  `json:"decision_id"`
	// SignalSource 策略信号来源：即该订单的买入/卖出信号来自哪套策略（如"lowBollKDJ"）
	// 或择优器（"multi-factor-optimizer"）。确保每一笔买入订单都带明确的信号归属，
	// 供审计与"与所选策略一致才执行"校验使用。
	SignalSource string `json:"signal_source"`
}

// ResearchReport 研究报告
type ResearchReport struct {
	ResearchID     string      `json:"research_id"`
	Objective      string      `json:"objective"`
	MarketState    interface{} `json:"market_state"`
	Findings       []string    `json:"findings"`
	Strategies     interface{} `json:"strategies"`
	Risks          []string    `json:"risks"`
	Limitations    []string    `json:"limitations"`
	Confidence     float64     `json:"confidence"`
	Recommendation string      `json:"recommendation"`
}

// RiskReport 风控报告
type RiskReport struct {
	RiskID         string      `json:"risk_id"`
	PortfolioRisk  interface{} `json:"portfolio_risk"`
	StructuralRisk interface{} `json:"structural_risk"`
	FactorHealth   interface{} `json:"factor_health"`
	StressResults  interface{} `json:"stress_results"`
	Violations     []string    `json:"violations"`
	Decision       string      `json:"decision"`
	Confidence     float64     `json:"confidence"`
}

// CIODecision CIO决策
type CIODecision struct {
	DecisionID       string        `json:"decision_id"`
	PortfolioID      string        `json:"portfolio_id"`
	Decision         DecisionType  `json:"decision"`
	Reason           string        `json:"reason"`
	Orders           []OrderIntent `json:"orders"`
	RiskApproval     string        `json:"risk_approval"`
	PolicyStatus     string        `json:"policy_status"`
	MarketState      string        `json:"market_state"`
	MarketConfidence float64       `json:"market_confidence"`
	Timestamp        time.Time     `json:"timestamp"`
	// Optimization 组合优化结果（真实协方差均值-方差/风险平价），供复盘展示，可为 nil。
	Optimization interface{} `json:"optimization,omitempty"`
	// SixDim 市场六维判势结果（六维得分/冲突修正总分/仓位系数 position_rate/市场标签），
	// 盘前决策与盘中监控前置输出，供复盘展示；策略信号仓位 = 原始信号仓位 × position_rate，可为 nil。
	SixDim interface{} `json:"sixdim,omitempty"`
	// LLMReview 大模型复议层输出（结构化审批 JSON）：规则候选决策经 LLM 独立复议后的
	// 动作/评分/意见，供审计与复盘展示；未启用复议或复议跳过时为 nil。
	LLMReview interface{} `json:"llm_review,omitempty"`
	// Evidence 决策证据链：决策主张 Agent 在得出结论前采集并引用的证据（六维判势/因子评分/
	// 策略信号/组合归因等）、及其结构化决策主张，供审计与归因复盘；未启用决策主张时为 nil。
	Evidence interface{} `json:"evidence,omitempty"`
}

// PermissionMatrix 权限矩阵
type PermissionMatrix struct {
	CanReadProfile           bool `json:"can_read_profile"`
	CanModifyObjective       bool `json:"can_modify_objective"`
	CanCreateStrategy        bool `json:"can_create_strategy"`
	CanRunBacktest           bool `json:"can_run_backtest"`
	CanAnalyzeRisk           bool `json:"can_analyze_risk"`
	CanVetoDecision          bool `json:"can_veto_decision"`
	CanMakePortfolioDecision bool `json:"can_make_portfolio_decision"`
	CanCreateOrderIntent     bool `json:"can_create_order_intent"`
	CanCreateRealOrder       bool `json:"can_create_real_order"`
	CanModifyHardLimits      bool `json:"can_modify_hard_limits"`
	CanEmergencyStop         bool `json:"can_emergency_stop"`
}

// GetPermissionMatrix 获取Agent权限矩阵
func GetPermissionMatrix(role AgentRole) PermissionMatrix {
	matrices := map[AgentRole]PermissionMatrix{
		RoleCIO: {
			CanReadProfile:           true,  // 查看市场数据✓
			CanModifyObjective:       false, // 不修改目标
			CanCreateStrategy:        false, // 不生成策略
			CanRunBacktest:           false, // 不运行回测
			CanAnalyzeRisk:           true,  // 风险审查✓
			CanVetoDecision:          true,  // 否决权✓
			CanMakePortfolioDecision: true,  // 最终投资决策✓
			CanCreateOrderIntent:     false, // 不创建订单意图（决策后由Trader执行）
			CanCreateRealOrder:       false, // 不执行交易✗
			CanModifyHardLimits:      true,  // 修改风控参数✓
			CanEmergencyStop:         true,  // 紧急熔断✓
		},
		RolePlanner: {
			CanReadProfile:           true,  // 查看市场数据✓
			CanModifyObjective:       true,  // 修改投资目标/调整Mandate✓
			CanCreateStrategy:        true,  // 生成Investment Mandate✓
			CanRunBacktest:           false, // 不运行回测
			CanAnalyzeRisk:           false, // 不分析风险
			CanVetoDecision:          false, // 无否决权
			CanMakePortfolioDecision: false, // 无最终决策
			CanCreateOrderIntent:     false, // 不创建订单
			CanCreateRealOrder:       false, // 不执行交易
			CanModifyHardLimits:      false, // 不修改风控参数
			CanEmergencyStop:         false, // 无紧急熔断
		},
		RoleQuant: {
			CanReadProfile:           true,  // 查看市场数据✓
			CanModifyObjective:       false, // 不修改目标
			CanCreateStrategy:        true,  // 生成选股策略✓
			CanRunBacktest:           true,  // 运行回测✓
			CanAnalyzeRisk:           false, // 不分析风险
			CanVetoDecision:          false, // 无否决权
			CanMakePortfolioDecision: false, // 无最终决策
			CanCreateOrderIntent:     false, // 不创建订单
			CanCreateRealOrder:       false, // 不执行交易
			CanModifyHardLimits:      false, // 不修改风控参数
			CanEmergencyStop:         false, // 无紧急熔断
		},
		RoleRisk: {
			CanReadProfile:           true,  // 查看市场数据✓
			CanModifyObjective:       false, // 不修改目标
			CanCreateStrategy:        false, // 不生成策略
			CanRunBacktest:           false, // 不运行回测
			CanAnalyzeRisk:           true,  // 风险审查✓
			CanVetoDecision:          true,  // 否决权✓
			CanMakePortfolioDecision: false, // 无最终决策
			CanCreateOrderIntent:     false, // 不创建订单
			CanCreateRealOrder:       false, // 不执行交易
			CanModifyHardLimits:      true,  // 修改风控参数✓
			CanEmergencyStop:         true,  // 紧急熔断✓
		},
		RoleTrader: {
			CanReadProfile:           true,  // 查看市场数据✓
			CanModifyObjective:       false, // 不修改目标
			CanCreateStrategy:        false, // 不生成策略
			CanRunBacktest:           false, // 不运行回测
			CanAnalyzeRisk:           false, // 不分析风险
			CanVetoDecision:          false, // 无否决权
			CanMakePortfolioDecision: false, // 无最终决策
			CanCreateOrderIntent:     true,  // 创建订单意图
			CanCreateRealOrder:       true,  // 执行交易下单✓
			CanModifyHardLimits:      false, // 不修改风控参数
			CanEmergencyStop:         false, // 无紧急熔断
		},
	}
	return matrices[role]
}

// Agent Base Agent基础结构
type Agent struct {
	ID           string
	Role         AgentRole
	Name         string
	State        AgentState
	Store        port.Persistence
	LLM          llm.Client
	Tools        []port.ToolExecutor
	ToolRegistry *ToolRegistry
	Tracker      port.Tracer
	SessionID    string
	mu           sync.RWMutex
	messageCh    chan AgentMessage
	emergencyCh  chan struct{}
	stopped      bool // 紧急停止标志：为 true 时该智能体停止一切工作（紧急停止/恢复工作开关）
}

// NewAgent 创建Agent
// tracker 为决策脑追踪抽象（port.Tracer），可为 nil；宿主通过 brainhost.AdaptTracker 注入。
func NewAgent(id string, role AgentRole, store port.Persistence, llmClient llm.Client, tracker port.Tracer) *Agent {
	return &Agent{
		ID:          id,
		Role:        role,
		Name:        agentName(role),
		State:       StateIdle,
		Store:       store,
		LLM:         llmClient,
		Tools:       make([]port.ToolExecutor, 0),
		Tracker:     tracker,
		messageCh:   make(chan AgentMessage, 100),
		emergencyCh: make(chan struct{}),
	}
}

// SetSessionID 设置当前会话ID
func (a *Agent) SetSessionID(sessionID string) {
	a.SessionID = sessionID
}

// TrackDataSource 记录数据源读取
func (a *Agent) TrackDataSource(source, status string, items []string) {
	if a.Tracker != nil && a.SessionID != "" {
		a.Tracker.AddDataSource(a.SessionID, source, status, items)
	}
}

// TrackAlgorithm 记录算法执行
func (a *Agent) TrackAlgorithm(name, input, output, status string, durationMs int64) {
	if a.Tracker != nil && a.SessionID != "" {
		a.Tracker.AddAlgorithm(a.SessionID, name, input, output, status, durationMs)
	}
}

// TrackDecision 记录决策步骤
func (a *Agent) TrackDecision(action, reason string, dataUsed []string) {
	if a.Tracker != nil && a.SessionID != "" {
		a.Tracker.AddDecision(a.SessionID, string(a.Role), action, reason, dataUsed)
	}
}

// RegisterTool 注册工具到Agent
func (a *Agent) RegisterTool(tool port.ToolExecutor) {
	a.Tools = append(a.Tools, tool)
}

// SetToolRegistry 设置中央工具注册表（用于运行时权限强制校验）
func (a *Agent) SetToolRegistry(registry *ToolRegistry) {
	a.ToolRegistry = registry
}

// GetTools 获取可用工具列表
func (a *Agent) GetTools() []llm.Tool {
	tools := make([]llm.Tool, 0)
	for _, t := range a.Tools {
		td := t.GetDefinition()
		tools = append(tools, llm.Tool{
			Type: "function",
			Function: llm.ToolFunction{
				Name:        td.Function.Name,
				Description: td.Function.Description,
				Parameters:  td.Function.Parameters,
			},
		})
	}
	return tools
}

// ExecuteTool 执行指定工具（运行时强制权限校验）
func (a *Agent) ExecuteTool(ctx context.Context, toolName string, args map[string]interface{}) (interface{}, error) {
	// Runtime Enforcement: 权限强制校验
	if a.ToolRegistry != nil {
		allowed, err := a.ToolRegistry.CheckToolPermission(a.Role, toolName)
		if err != nil {
			log.Printf("[Agent] ACTION_DENIED: %s (%s) 尝试调用工具 %s 失败: %v", a.Name, a.Role, toolName, err)
			return nil, fmt.Errorf("ACTION_DENIED: Agent %s (%s) 无权使用工具 %s: %v", a.Name, a.Role, toolName, err)
		}
		if !allowed {
			log.Printf("[Agent] ACTION_DENIED: %s (%s) 尝试调用工具 %s 被拒绝", a.Name, a.Role, toolName)
			return nil, fmt.Errorf("ACTION_DENIED: Agent %s (%s) 无权使用工具 %s", a.Name, a.Role, toolName)
		}
	}

	for _, tool := range a.Tools {
		td := tool.GetDefinition()
		if td.Function.Name == toolName {
			return tool.Execute(ctx, args)
		}
	}
	return nil, fmt.Errorf("tool not found: %s", toolName)
}

func agentName(role AgentRole) string {
	names := map[AgentRole]string{
		RoleCIO:     "首席投资官",
		RolePlanner: "投资规划师",
		RoleQuant:   "量化分析师",
		RoleRisk:    "风控师",
		RoleTrader:  "操盘手",
	}
	return names[role]
}

// GetState 获取当前状态
func (a *Agent) GetState() AgentState {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.State
}

// SetState 设置状态
func (a *Agent) SetState(state AgentState) {
	a.mu.Lock()
	defer a.mu.Unlock()
	oldState := a.State
	a.State = state

	log.Printf("[%s] State: %s → %s", a.Name, oldState, state)

	// 审计日志
	a.writeAudit("state_change", map[string]interface{}{
		"old_state": oldState,
		"new_state": state,
	})
}

// CanPerform 检查权限
func (a *Agent) CanPerform(permission string) bool {
	matrix := GetPermissionMatrix(a.Role)
	switch permission {
	case "read_profile":
		return matrix.CanReadProfile
	case "modify_objective":
		return matrix.CanModifyObjective
	case "create_strategy":
		return matrix.CanCreateStrategy
	case "run_backtest":
		return matrix.CanRunBacktest
	case "analyze_risk":
		return matrix.CanAnalyzeRisk
	case "veto_decision":
		return matrix.CanVetoDecision
	case "portfolio_decision":
		return matrix.CanMakePortfolioDecision
	case "create_order_intent":
		return matrix.CanCreateOrderIntent
	case "create_real_order":
		return matrix.CanCreateRealOrder
	case "modify_hard_limits":
		return matrix.CanModifyHardLimits
	case "emergency_stop":
		return matrix.CanEmergencyStop
	default:
		return false
	}
}

// SendMessage 发送消息给其他Agent
func (a *Agent) SendMessage(to AgentRole, msgType string, task interface{}, context interface{}) AgentMessage {
	msg := AgentMessage{
		MessageID: fmt.Sprintf("MSG-%s-%d", a.ID, time.Now().UnixNano()),
		From:      a.Role,
		To:        to,
		Type:      msgType,
		Priority:  "NORMAL",
		Task:      task,
		Context:   context,
		Timestamp: time.Now(),
	}

	log.Printf("[%s] → [%s]: %s", a.Name, agentName(to), msgType)

	// 审计日志
	a.writeAudit("message_sent", map[string]interface{}{
		"to":      to,
		"type":    msgType,
		"task":    task,
		"context": context,
	})

	return msg
}

// ReceiveMessage 接收消息
func (a *Agent) ReceiveMessage(msg AgentMessage) {
	log.Printf("[%s] ← [%s]: %s", a.Name, agentName(msg.From), msg.Type)
	a.writeAudit("message_received", map[string]interface{}{
		"from": msg.From,
		"type": msg.Type,
	})
}

// EmergencyStop 紧急停止
func (a *Agent) EmergencyStop() {
	a.mu.Lock()
	a.stopped = true
	a.State = StateFailed
	a.mu.Unlock()

	log.Printf("[%s] EMERGENCY STOP triggered", a.Name)

	a.writeAudit("emergency_stop", nil)

	select {
	case a.emergencyCh <- struct{}{}:
	default:
	}
}

// Resume 恢复工作：清除紧急停止标志，智能体回到空闲状态
func (a *Agent) Resume() {
	a.mu.Lock()
	if a.stopped {
		a.stopped = false
		if a.State == StateFailed || a.State == StateBlocked {
			a.State = StateIdle
		}
	}
	a.mu.Unlock()

	log.Printf("[%s] RESUME work", a.Name)

	a.writeAudit("resume", nil)

	// 清除历史紧急停止信号
	select {
	case <-a.emergencyCh:
	default:
	}
}

// IsStopped 是否处于紧急停止状态（停止一切工作）
func (a *Agent) IsStopped() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.stopped
}

// IsEmergencStopped 检查是否已紧急停止
func (a *Agent) IsEmergencStopped() bool {
	select {
	case <-a.emergencyCh:
		return true
	default:
		return false
	}
}

// writeAudit 写入审计日志
func (a *Agent) writeAudit(action string, details interface{}) {
	if a.Store == nil {
		return
	}

	detailsJSON, _ := json.Marshal(details)
	// EventID 与时间戳在重试闭包外只计算一次，避免每次重试生成不同值
	eventID := fmt.Sprintf("%s-%d", action, time.Now().UnixNano())
	ts := time.Now()

	brainutil.SafeGoWithRetry("Agent.WriteAuditLog", 3, func() error {
		return a.Store.WriteAudit(eventID, "LIVE_ACTIVITY", a.ID, string(a.Role), action, "agent", a.ID, "success", string(detailsJSON), ts)
	})
}

// CallLLM 调用LLM
func (a *Agent) CallLLM(ctx context.Context, messages []llm.Message) (*llm.ChatResult, error) {
	if a.LLM == nil {
		return nil, fmt.Errorf("LLM not initialized for %s", a.Name)
	}

	a.SetState(StateToolCalling)

	response, err := a.LLM.Chat(ctx, messages, nil)
	if err != nil {
		a.SetState(StateFailed)
		return nil, err
	}

	a.SetState(StateProposing)
	return response, nil
}

// GetStatusInfo 获取状态信息（供UI展示）
func (a *Agent) GetStatusInfo() map[string]interface{} {
	return map[string]interface{}{
		"id":    a.ID,
		"role":  string(a.Role),
		"name":  a.Name,
		"state": string(a.State),
	}
}

// ==================== Section 1: Role Responsibilities ====================

// RoleDescription 角色描述（产品经理+基金经理双视角）
type RoleDescription struct {
	Role          AgentRole
	Title         string   // 中文职位
	PMDescription string   // 产品经理视角：工作职责
	FMDescription string   // 基金经理视角：投资职责
	WorkItems     []string // 具体工作内容
	KeyAlgorithms []string // 核心算法
	CurrentIssues []string // 存在的问题
	Optimizations []string // 优化方案
}

// GetRoleDescriptions 获取所有角色的详细描述（产品经理+基金经理双视角）
func GetRoleDescriptions() []RoleDescription {
	return []RoleDescription{
		{
			Role:          RoleCIO,
			Title:         "首席投资官",
			PMDescription: "系统最高决策者，负责整合所有Agent的分析结果，做出最终投资决策。管理投资组合的整体方向，决定买入、卖出和持仓策略。",
			FMDescription: "作为基金经理的核心代表，承担投资组合的最终责任。需要平衡收益与风险，在不确定性中做出战略性判断，对组合业绩负总责。",
			WorkItems: []string{
				"整合PLANNER的市场规划、QUANT的选股建议、RISK的风控评估",
				"根据当前市场状态和组合状况做出最终投资决策",
				"决定组合整体风险敞口和再平衡时机",
				"审批或否决其他Agent的建议方案",
				"触发紧急止损和暂停交易",
				"向TRADER下达具体交易指令",
			},
			KeyAlgorithms: []string{
				"多因子决策框架",
				"市场状态识别",
				"组合风险评估",
				"再平衡触发算法",
			},
			CurrentIssues: []string{
				"决策依赖硬编码市场状态，无组合优化",
				"缺少再平衡触发规则",
				"无决策置信度量化评估",
				"缺少历史决策回溯验证",
			},
			Optimizations: []string{
				"引入多因子决策框架，支持多维度信号融合",
				"集成组合优化器实现风险调整后收益最大化",
				"增加定期+阈值双触发再平衡机制",
				"增加决策置信度量化和历史回溯验证",
			},
		},
		{
			Role:          RolePlanner,
			Title:         "投资规划师",
			PMDescription: "负责分析宏观市场环境，制定投资计划和策略方向。为投资组合设定目标和约束条件，指导QUANT进行具体的选股和策略开发。",
			FMDescription: "作为基金的策略规划者，需要研判市场大方向，决定组合的风格和暴露。通过前瞻性的市场判断，为投资组合定下基调。",
			WorkItems: []string{
				"分析宏观经济指标和市场趋势",
				"识别当前市场状态（牛市、熊市、震荡市）",
				"制定投资计划和Investment Mandate",
				"设定组合目标收益和风险约束",
				"规划资产配置比例（股票、债券、现金）",
				"评估市场情绪和资金流向",
			},
			KeyAlgorithms: []string{
				"市场趋势识别",
				"周期分析（康波周期/美林时钟）",
				"资产配置模型",
				"情绪指标合成",
			},
			CurrentIssues: []string{
				"缺少资产配置模型，依赖主观判断",
				"缺少目标风险匹配机制",
				"无动态目标调整能力",
				"缺少多周期分析框架",
			},
			Optimizations: []string{
				"引入Markowitz均值-方差模型进行优化配置",
				"基于风险预算的资产配置方法",
				"动态目标调整机制（根据市场状态切换）",
				"增加多周期分析框架（周/月/季/年）",
			},
		},
		{
			Role:          RoleQuant,
			Title:         "量化分析师",
			PMDescription: "负责Alpha因子的挖掘和选股信号的生成。通过量化模型从大量标的中筛选出具备超额收益潜力的股票，为CIO提供投资建议。",
			FMDescription: "作为基金的Alpha来源核心，需要不断寻找市场非有效性带来的超额收益机会。通过系统化的量化方法，将主观投资理念转化为可执行的模型。",
			WorkItems: []string{
				"开发和维护多因子选股模型",
				"计算股票池的因子得分和排名",
				"生成买入/卖出信号",
				"进行因子有效性分析（IC/IR）",
				"监控策略表现和衰减",
				"探索新的Alpha来源",
			},
			KeyAlgorithms: []string{
				"多因子模型",
				"因子正交化",
				"截面回归",
				"机器学习（LightGBM/XGBoost）",
				"行业轮动信号",
			},
			CurrentIssues: []string{
				"使用硬编码因子得分而非实时计算",
				"缺少因子衰减分析",
				"缺少机器学习模型支持",
				"缺少行业轮动信号",
			},
			Optimizations: []string{
				"集成FactorEngine进行真实因子计算",
				"增加因子IC/IR分析和衰减监控",
				"增加LightGBM/XGBoost模型支持",
				"增加行业热度分析和轮动信号",
			},
		},
		{
			Role:          RoleRisk,
			Title:         "风控师",
			PMDescription: "负责实时监控投资组合的风险状况，确保所有操作符合预设的风控规则。在风险超标时有权否决决策并触发紧急措施。",
			FMDescription: "作为基金的风险守护者，需要在追求收益的同时守住风险底线。通过科学的风险量化和监控，避免灾难性亏损的发生。",
			WorkItems: []string{
				"计算组合风险指标（VaR、波动率、最大回撤）",
				"监控持仓集中度和相关性",
				"执行压力测试和情景分析",
				"检查是否突破风控阈值",
				"在必要时否决交易决策",
				"触发紧急止损和熔断机制",
			},
			KeyAlgorithms: []string{
				"VaR/ES计算",
				"GARCH波动率模型",
				"蒙特卡洛模拟",
				"压力测试",
				"相关性分析",
			},
			CurrentIssues: []string{
				"风险指标简单（只有VaR）",
				"缺少情景分析和压力测试",
				"缺少动态止损机制",
				"缺少流动性风险评估",
			},
			Optimizations: []string{
				"增加CVaR/ES（期望短缺）指标",
				"增加6种压力测试情景（历史模拟/参数法/极值理论等）",
				"增加ATR动态止损机制",
				"增加Amihud非流动性指标评估",
			},
		},
		{
			Role:          RoleTrader,
			Title:         "操盘手",
			PMDescription: "负责将CIO的决策转化为具体的交易执行计划。优化执行路径，降低交易成本和市场冲击，确保交易意图的忠实执行。",
			FMDescription: "作为基金的交易执行者，需要在市场中精准执行投资决策。优秀的执行能显著提升实际收益，是策略成功的最后一公里。",
			WorkItems: []string{
				"接收CIO的交易指令",
				"分析目标股票的流动性和盘口",
				"制定最优执行计划（拆单、择时）",
				"执行交易并监控成交情况",
				"管理交易成本（滑点、手续费、冲击成本）",
				"反馈执行结果给CIO",
			},
			KeyAlgorithms: []string{
				"TWAP（时间加权平均价格）",
				"VWAP（成交量加权平均价格）",
				"滑点估算模型",
				"智能订单路由",
				"市场冲击模型",
			},
			CurrentIssues: []string{
				"无智能订单路由",
				"无TWAP/VWAP算法",
				"无滑点控制",
				"无冲击成本评估",
			},
			Optimizations: []string{
				"增加大单拆分算法（TWAP）",
				"增加成交量加权执行（VWAP）",
				"增加滑点估算模型",
				"增加流动性筛选和冲击成本评估",
			},
		},
	}
}

// ==================== Section 2: Enhanced Agent Workflow ====================

// GetRoleDescription 获取当前Agent角色的详细描述
func (a *Agent) GetRoleDescription() RoleDescription {
	descriptions := GetRoleDescriptions()
	for _, desc := range descriptions {
		if desc.Role == a.Role {
			return desc
		}
	}
	return RoleDescription{
		Role:          a.Role,
		Title:         agentName(a.Role),
		PMDescription: "未知角色",
		FMDescription: "未知角色",
		WorkItems:     []string{},
		KeyAlgorithms: []string{},
		CurrentIssues: []string{},
		Optimizations: []string{},
	}
}

// ProcessTask 处理分配给当前Agent的任务
func (a *Agent) ProcessTask(ctx context.Context, task AgentMessage) AgentMessage {
	// 生成并设置会话ID
	sessionID := fmt.Sprintf("session-%s-%d", a.Role, time.Now().UnixNano())
	a.SetSessionID(sessionID)

	// 如果有追踪器，开始追踪会话
	if a.Tracker != nil {
		a.Tracker.StartSession(sessionID, time.Now().Format("2006-01-02"))
		log.Printf("[Agent] Tracker session started: %s", sessionID)
	}

	log.Printf("[Agent] ProcessTask: role=%s, task_type=%s, task=%v, llm_nil=%v", a.Role, task.Type, task.Task, a.LLM == nil)

	// 紧急停止门控：处于停止状态时拒绝一切任务，实现"紧急停止 → 所有智能体停止工作"
	if a.IsStopped() {
		log.Printf("[Agent] %s 处于紧急停止状态，拒绝处理任务: %v", a.Role, task.Task)
		return AgentMessage{
			MessageID: fmt.Sprintf("MSG-%s-%d", a.ID, time.Now().UnixNano()),
			From:      a.Role,
			To:        task.From,
			Type:      "BLOCKED",
			Priority:  task.Priority,
			Context: map[string]interface{}{
				"error":  "紧急停止中，所有智能体已暂停工作",
				"status": "EMERGENCY_STOP",
			},
			Timestamp: time.Now(),
		}
	}

	a.SetState(StateThinking)

	response := AgentMessage{
		MessageID: fmt.Sprintf("MSG-%s-%d", a.ID, time.Now().UnixNano()),
		From:      a.Role,
		To:        task.From,
		Type:      "TASK_RESULT",
		Priority:  task.Priority,
		Timestamp: time.Now(),
	}

	switch a.Role {
	case RoleCIO:
		log.Printf("[Agent] Processing CIO task...")
		response = a.processCIOTask(ctx, task)
	case RolePlanner:
		log.Printf("[Agent] Processing Planner task...")
		response = a.processPlannerTask(ctx, task)
	case RoleQuant:
		log.Printf("[Agent] Processing Quant task...")
		response = a.processQuantTask(ctx, task)
	case RoleRisk:
		log.Printf("[Agent] Processing Risk task...")
		response = a.processRiskTask(ctx, task)
	case RoleTrader:
		log.Printf("[Agent] Processing Trader task...")
		response = a.processTraderTask(ctx, task)
	default:
		response.Type = "ERROR"
		response.Context = map[string]string{"error": "unknown role"}
		log.Printf("[Agent] Unknown role: %s", a.Role)
	}

	a.SetState(StateCompleted)

	log.Printf("[Agent] ProcessTask completed: role=%s, response_type=%s", a.Role, response.Type)

	a.writeAudit("task_processed", map[string]interface{}{
		"task_type": task.Type,
		"from":      task.From,
		"response":  response.Type,
	})

	return response
}

// taskCtxStr 提取任务真实执行上下文（生产场景交由调度器注入，非nil）
func taskCtxStr(task AgentMessage) string {
	if v, ok := task.Context.(string); ok && v != "" {
		return v
	}
	return "暂无预置上下文，请自行通过工具获取实时数据。"
}

// taskIsMonitor 判断是否为监控类任务（只监控不上报分析/不做决策）
func taskIsMonitor(task AgentMessage) bool {
	t := fmt.Sprintf("%v", task.Task)
	return strings.Contains(t, "监控") || strings.Contains(t, "NO_ACTION") || strings.Contains(t, "monitor") || strings.Contains(t, "Monitor")
}

// buildAgentUserPrompt 构造生产场景的统一用户提示（携带真实上下文 + 任务指令）
func buildAgentUserPrompt(task AgentMessage) string {
	return fmt.Sprintf(
		"当前环境/上下文:\n%s\n\n任务:\n%s\n\n请严格按上方 OUTPUT SCHEMA 输出一个 JSON 对象，不要输出除 JSON 以外的任何文本（含思考过程、markdown）。",
		taskCtxStr(task), fmt.Sprintf("%v", task.Task))
}

func (a *Agent) processCIOTask(ctx context.Context, task AgentMessage) AgentMessage {
	response := AgentMessage{
		MessageID: fmt.Sprintf("MSG-%s-%d", a.ID, time.Now().UnixNano()),
		From:      a.Role,
		To:        task.From,
		Type:      "CIO_DECISION",
		Priority:  "HIGH",
		Timestamp: time.Now(),
	}

	if a.LLM == nil {
		response.Type = "ERROR"
		response.Context = map[string]string{"error": "LLM client not configured for CIO"}
		return response
	}

	systemPrompt := `You are a senior CIO (Chief Investment Officer). Your role: final investment decision maker.

REQUIRED TOOLS:
- get_portfolio_state: Get total assets, cash, positions, P&L
- get_positions: Get real-time position details
- get_market_data: Get market data
- screen_market: Run intelligent stock screening
- optimize_portfolio: Optimize portfolio allocation
- position_manager: Generate precise build/add/reduce plans (batched entry + pyramiding, dynamic take-profit/stop-loss, ATR-based sizing)

CRITICAL RULES:
1. ONLY output a single valid JSON object. NO markdown, NO explanations, NO text outside JSON.
2. ALL fields are required. Use exact enum values.
3. Be concise. NO verbose reasoning. Put minimal reason in "reason" field.
4. Use tools to get real data. NEVER fabricate numbers.
5. If you cannot decide, set confidence low and decision to NO_ACTION.
6. POSITION SIZING: when deciding to build/add/reduce any position, MUST call position_manager (action=plan) with symbol/current_price/existing_shares/avg_cost/portfolio_value/cash_available to get concrete share quantities and trigger prices. Do NOT use simple fixed percentages.

OUTPUT SCHEMA:
{
  "decision": "NO_ACTION|INCREASE_RISK|REDUCE_RISK|REBALANCE|CHANGE_STRATEGY|PAUSE_STRATEGY|PAUSE_TRADING|RESUME_TRADING",
  "reason": "brief reason, max 100 chars",
  "confidence": 0.0,
  "target_position": 0.0,
  "notes": "brief note, max 50 chars"
}`

	userPrompt := buildAgentUserPrompt(task)

	result, err := a.CallLLMWithTools(ctx, systemPrompt, userPrompt)
	if err != nil {
		log.Printf("[Agent] CIO LLM call failed: %v", err)
		response.Type = "ERROR"
		response.Context = map[string]string{"error": err.Error()}
		return response
	}

	response.Context = result
	log.Printf("[Agent] CIO decision made: %s", result[:min(100, len(result))])
	return response
}

// stripThinkingBlocks 去除 DeepSeek 思考模式在 Content 中混入的思考标记块，
// 避免思考文本在后续工具轮次中作为历史重复回传（显著节省输入 token，不影响工具数据有效）。
func stripThinkingBlocks(s string) string {
	if s == "" {
		return s
	}
	// 若存在回答标记，仅保留其之后的内容
	if idx := strings.LastIndex(s, "【回答】"); idx >= 0 {
		return strings.TrimSpace(s[idx+len("【回答】"):])
	}
	// 否则移除开头的思考标记部分（工具轮前的思考不需要回传）
	for _, marker := range []string{"【思考过程】", "【分析】", "【反思】", "思考过程:", "思考："} {
		if i := strings.Index(s, marker); i >= 0 {
			s = s[:i]
		}
	}
	return strings.TrimSpace(s)
}

// isHeavyMonitorTool 监控任务中应剔除的重负载工具：schema 庞大且会在监控中被浪费触发
// （回测/组合优化/全市场筛选），剔除后显著降低工具定义 token 占用与延迟。
func isHeavyMonitorTool(name string) bool {
	switch name {
	case "run_backtest", "optimize_portfolio", "build_stock_pool", "screen_market":
		return true
	}
	return false
}

// CallLLMWithTools 调用LLM并处理工具调用
func (a *Agent) CallLLMWithTools(ctx context.Context, systemPrompt string, userPrompt string) (string, error) {
	if a.LLM == nil {
		log.Printf("[Agent] ERROR: LLM client is nil! Cannot call LLM.")
		return "", fmt.Errorf("LLM client not configured")
	}

	log.Printf("[Agent] CallLLMWithTools: llm_type=%T, role=%s, sessionID=%s", a.LLM, a.Role, a.SessionID)
	log.Printf("[Agent] System prompt (first 100 chars): %s", truncateString(systemPrompt, 100))
	log.Printf("[Agent] User prompt (first 100 chars): %s", truncateString(userPrompt, 100))

	messages := []llm.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userPrompt},
	}

	tools := a.GetTools()

	// 监控任务优化（监控只读数据、不跑重任务）：
	// ① 剔除重负载工具（回测/组合优化/全市场选股），其 schema 庞大且监控根本无需触发；
	// ② 限制轮数为 2，避免多轮推理 + 工具结果在上下文中成倍累积导致 token/延迟膨胀。
	isMonitorTask := strings.Contains(systemPrompt, "[MONITOR MODE]")
	if isMonitorTask {
		kept := make([]llm.Tool, 0, len(tools))
		for _, t := range tools {
			if !isHeavyMonitorTool(t.Function.Name) {
				kept = append(kept, t)
			}
		}
		tools = kept
	}
	log.Printf("[Agent] Available tools: %d (monitor=%v)", len(tools), isMonitorTask)
	for _, tool := range tools {
		log.Printf("[Agent]   Tool: %s", tool.Function.Name)
	}

	agentStartTime := time.Now()

	a.TrackDecision("开始 LLM 调用", fmt.Sprintf("角色: %s, 可用工具: %d", a.Name, len(tools)), []string{"system_prompt", "user_prompt"})

	maxToolRounds := 10
	if isMonitorTask {
		maxToolRounds = 2
	}

	for round := 0; round <= maxToolRounds; round++ {
		log.Printf("[Agent] Tool round %d/%d, messages: %d", round, maxToolRounds, len(messages))
		llmCallStart := time.Now()

		log.Printf("[Agent] Calling LLM.Chat() - round %d", round)
		result, err := a.LLM.Chat(ctx, messages, tools)
		llmDuration := time.Since(llmCallStart).Milliseconds()

		if err != nil {
			log.Printf("[Agent] LLM call FAILED in round %d: %v (duration: %dms)", round, err, llmDuration)
			log.Printf("[Agent] Error details: type=%T, message=%s", err, err.Error())
			a.TrackAlgorithm(fmt.Sprintf("LLM Chat (Round %d)", round), fmt.Sprintf("messages: %d, tools: %d", len(messages), len(tools)), "", "失败", llmDuration)
			return "", fmt.Errorf("LLM call failed: %w", err)
		}

		log.Printf("[Agent] LLM call SUCCESS in round %d: duration=%dms", round, llmDuration)

		if len(result.Choices) == 0 {
			log.Printf("[Agent] LLM returned empty response (no choices)")
			return "", fmt.Errorf("LLM returned empty response")
		}

		assistantMsg := result.Choices[0].Message

		log.Printf("[Agent] Round %d: content_len=%d, tool_calls=%d, finish_reason=%s",
			round, len(assistantMsg.Content), len(assistantMsg.ToolCalls), result.Choices[0].FinishReason)
		if len(assistantMsg.Content) > 0 {
			log.Printf("[Agent] Response content (first 200 chars): %s", truncateString(assistantMsg.Content, 200))
		}

		a.TrackAlgorithm(
			fmt.Sprintf("LLM Chat (Round %d)", round),
			fmt.Sprintf("输入: %d messages, %d tools", len(messages), len(tools)),
			fmt.Sprintf("输出: content_len=%d, tool_calls=%d", len(assistantMsg.Content), len(assistantMsg.ToolCalls)),
			"成功",
			llmDuration,
		)

		if len(assistantMsg.ToolCalls) == 0 {
			rawContent := assistantMsg.Content
			cleanedJSON := brainutil.ExtractJSON(rawContent)
			if cleanedJSON != rawContent {
				log.Printf("[Agent] JSON extracted from LLM output: raw_len=%d, clean_len=%d", len(rawContent), len(cleanedJSON))
			}
			log.Printf("[Agent] No tool calls, returning content (len=%d)", len(cleanedJSON))
			a.TrackDecision("LLM 返回最终结果", fmt.Sprintf("content length: %d, total duration: %dms", len(cleanedJSON), time.Since(agentStartTime).Milliseconds()), []string{"llm_response"})
			return cleanedJSON, nil
		}

		log.Printf("[Agent] Executing %d tool calls", len(assistantMsg.ToolCalls))

		// 执行所有工具调用
		for _, tc := range assistantMsg.ToolCalls {
			var args map[string]interface{}
			if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
				log.Printf("[Agent] Tool args parse failed: %v", err)
				continue
			}

			toolStartTime := time.Now()
			toolResult, err := a.ExecuteTool(ctx, tc.Function.Name, args)
			toolDuration := time.Since(toolStartTime).Milliseconds()

			if err != nil {
				log.Printf("[Agent] Tool %s failed: %v", tc.Function.Name, err)
				toolResult = map[string]string{"error": err.Error()}
				a.TrackDataSource(tc.Function.Name, "失败", []string{fmt.Sprintf("error: %v", err)})
			} else {
				var dataItems []string
				if resultMap, ok := toolResult.(map[string]interface{}); ok {
					for key, val := range resultMap {
						dataItems = append(dataItems, fmt.Sprintf("%s: %v", key, val))
					}
				}
				if len(dataItems) == 0 {
					dataItems = append(dataItems, tc.Function.Arguments)
				}
				a.TrackDataSource(tc.Function.Name, "成功", dataItems)
			}

			toolResultJSON, _ := json.Marshal(toolResult)

			// 限制工具结果长度回传到 LLM 上下文：大结果（如整表行情/选股清单）
			// 会让每轮 token 成倍膨胀，瘦身后再作为 tool 消息回传。
			const maxToolResultLen = 3000
			if len(toolResultJSON) > maxToolResultLen {
				log.Printf("[Agent] Truncating tool result for %s: %d -> %d bytes", tc.Function.Name, len(toolResultJSON), maxToolResultLen)
				toolResultJSON = toolResultJSON[:maxToolResultLen]
			}

			a.TrackAlgorithm(
				fmt.Sprintf("工具: %s", tc.Function.Name),
				tc.Function.Arguments,
				string(toolResultJSON),
				map[bool]string{true: "成功", false: "失败"}[err == nil],
				toolDuration,
			)

			// 转换 ToolCallInfo 为 Message.ToolCalls 类型
			toolCalls := make([]struct {
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			}, 1)
			toolCalls[0].ID = tc.ID
			toolCalls[0].Type = tc.Type
			toolCalls[0].Function.Name = tc.Function.Name
			toolCalls[0].Function.Arguments = tc.Function.Arguments

			// 追加 assistant 消息：剥离 Content 中混入的思考标记（节省 token），
			// 但必须原样回传 reasoning_content —— DeepSeek thinking 模式契约要求
			// 上一轮 assistant 的 reasoning_content 后续必须带回，否则 API 报 400。
			messages = append(messages, llm.Message{
				Role:             "assistant",
				Content:          stripThinkingBlocks(assistantMsg.Content),
				ReasoningContent: assistantMsg.ReasoningContent,
				ToolCalls:        toolCalls,
			})

			messages = append(messages, llm.Message{
				Role:       "tool",
				Content:    string(toolResultJSON),
				ToolCallID: tc.ID,
			})
		}
	}

	a.TrackDecision("LLM 调用超出最大轮次", fmt.Sprintf("达到最大轮次: %d", maxToolRounds), []string{"max_rounds_exceeded"})
	return "", fmt.Errorf("max tool rounds exceeded")
}

// CallLLMText 简单调用LLM（不带工具）
func (a *Agent) CallLLMText(ctx context.Context, systemPrompt string, userPrompt string) (string, error) {
	if a.LLM == nil {
		return "", fmt.Errorf("LLM client not configured")
	}

	messages := []llm.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userPrompt},
	}

	result, err := a.LLM.Chat(ctx, messages, nil)
	if err != nil {
		return "", fmt.Errorf("LLM call failed: %w", err)
	}

	if len(result.Choices) == 0 {
		return "", fmt.Errorf("LLM returned empty response")
	}

	return brainutil.ExtractJSON(result.Choices[0].Message.Content), nil
}

func (a *Agent) processMarketState(marketState interface{}, decision *CIODecision) {
	switch fmt.Sprintf("%v", marketState) {
	case "bull":
		decision.Decision = DecisionIncreaseRisk
		decision.Reason = "牛市状态，建议增加风险敞口"
	case "bear":
		decision.Decision = DecisionReduceRisk
		decision.Reason = "熊市状态，建议降低风险敞口"
	case "sideways":
		decision.Decision = DecisionRebalance
		decision.Reason = "震荡市，建议优化组合结构"
	case "volatile":
		decision.Decision = DecisionReduceRisk
		decision.Reason = "高波动状态，建议降低仓位"
	default:
		decision.Decision = DecisionNoAction
		decision.Reason = "市场状态不明，建议观望"
	}
}

func (a *Agent) processPlannerTask(ctx context.Context, task AgentMessage) AgentMessage {
	response := AgentMessage{
		MessageID: fmt.Sprintf("MSG-%s-%d", a.ID, time.Now().UnixNano()),
		From:      a.Role,
		To:        task.From,
		Type:      "PLANNER_PLAN",
		Priority:  "NORMAL",
		Timestamp: time.Now(),
	}

	systemPrompt := `You are an Investment Planner. Your role: create investment plans and asset allocation.

REQUIRED TOOLS:
- get_portfolio_state: Get current portfolio state
- get_market_data: Get market data and index quotes
- search_market: Search market and industry data
- screen_market: Run intelligent stock screening

CRITICAL RULES:
1. ONLY output a single valid JSON object. NO markdown, NO explanations.
2. ALL fields required. Use exact enum values.
3. Be concise. "strategy_notes" max 80 chars.
4. Use tools for real data. NEVER fabricate.

OUTPUT SCHEMA:
{
  "plan_id": "string",
  "market_regime": "BULL|BEAR|NEUTRAL|VOLATILE",
  "asset_allocation": {"equity": 0.0, "bond": 0.0, "cash": 0.0},
  "target_return": 0.0,
  "target_volatility": 0.0,
  "strategy_notes": "brief note"
}`

	userPrompt := buildAgentUserPrompt(task)

	result, err := a.CallLLMWithTools(ctx, systemPrompt, userPrompt)
	if err != nil {
		log.Printf("[Agent] Planner LLM call failed: %v", err)
		response.Type = "ERROR"
		response.Context = map[string]string{"error": err.Error()}
		return response
	}

	response.Context = result
	log.Printf("[Agent] Planner plan created")
	return response
}

func (a *Agent) processQuantTask(ctx context.Context, task AgentMessage) AgentMessage {
	response := AgentMessage{
		MessageID: fmt.Sprintf("MSG-%s-%d", a.ID, time.Now().UnixNano()),
		From:      a.Role,
		To:        task.From,
		Type:      "QUANT_SIGNAL",
		Priority:  "NORMAL",
		Timestamp: time.Now(),
	}

	isMonitor := taskIsMonitor(task)

	// 监控模式：只监控既有信号是否发生重大变化，不重选股、不重算因子
	monitorPrompt := `You are a Quantitative Analyst [MONITOR MODE].
You are checking whether existing quantitative signals changed significantly.
Do NOT re-select stocks, do NOT recompute factors from scratch.

REQUIRED TOOLS:
- get_position / get_positions: Check current positions
- get_market_data: Get real-time market data

CRITICAL RULES:
1. ONLY output a single valid JSON object. NO markdown, NO explanations, NO thinking block.
2. If no significant signal change, set signal_status = "NO_SIGNAL_CHANGE" and empty arrays.
3. Use tools for real data. NEVER fabricate.

OUTPUT SCHEMA:
{
  "signal_status": "NO_SIGNAL_CHANGE | SIGNAL_CHANGE | MODEL_ERROR",
  "signal_changes": ["string"],
  "threshold_events": ["string"],
  "analysis_notes": "brief note, max 80 chars"
}`

	// 分析模式：因子选股与信号生成（盘前量化分析、因子复盘等）
	analysisPrompt := `You are a Quantitative Analyst. Your role: factor-based stock selection and signal generation.

REQUIRED TOOLS:
- run_backtest: Run strategy backtesting
- screen_market: Run intelligent multi-factor screening
- get_market_data: Get market data
- get_positions: Check current positions

CRITICAL RULES:
1. ONLY output a single valid JSON object. NO markdown, NO explanations, NO thinking block.
2. ALL fields required. "top_picks" max 10 items.
3. Be concise. "analysis_notes" max 80 chars.
4. Use tools for real data. NEVER fabricate.
5. Factor scores must sum to 100 or be individual 0-100 per factor.

OUTPUT SCHEMA:
{
  "signal_id": "string",
  "model": "string",
  "factor_scores": {"factor": 0.0},
  "top_picks": ["code"],
  "confidence": 0.0,
  "analysis_notes": "brief note"
}`

	systemPrompt := analysisPrompt
	if isMonitor {
		systemPrompt = monitorPrompt
	}

	userPrompt := buildAgentUserPrompt(task)

	result, err := a.CallLLMWithTools(ctx, systemPrompt, userPrompt)
	if err != nil {
		log.Printf("[Agent] Quant LLM call failed: %v", err)
		response.Type = "ERROR"
		response.Context = map[string]string{"error": err.Error()}
		return response
	}

	response.Context = result
	log.Printf("[Agent] Quant signal generated")
	return response
}

func (a *Agent) processRiskTask(ctx context.Context, task AgentMessage) AgentMessage {
	response := AgentMessage{
		MessageID: fmt.Sprintf("MSG-%s-%d", a.ID, time.Now().UnixNano()),
		From:      a.Role,
		To:        task.From,
		Type:      "RISK_REPORT",
		Priority:  "HIGH",
		Timestamp: time.Now(),
	}

	isMonitor := taskIsMonitor(task)

	// 监控模式：只对比阈值，不重算完整风险指标（针对每5分钟的"风险监控"任务）
	monitorPrompt := `You are a Risk Manager [MONITOR MODE].
You are doing routine risk monitoring, NOT a full risk analysis.
Do NOT recompute VaR, volatility, max drawdown or beta from scratch.
Compare the already-available risk indicators against their preset thresholds; report only what changes.

REQUIRED TOOLS:
- get_portfolio_state: Get portfolio state and risk metrics
- get_positions: Get position details for risk check

CRITICAL RULES:
1. ONLY output a single valid JSON object. NO markdown, NO explanations, NO thinking block.
2. If nothing breached thresholds, set risk_status = "RISK_NORMAL" and empty arrays.
3. Use tools for real data. NEVER fabricate.

OUTPUT SCHEMA:
{
  "risk_status": "RISK_NORMAL | THRESHOLD_BREACH | DATA_ERROR",
  "threshold_breaches": ["string"],
  "risk_alerts": ["string"],
  "risk_notes": "brief note, max 80 chars"
}`

	// 分析/事件模式：完整风险分析（如事件风险评估）
	analysisPrompt := `You are a Risk Manager. Your role: full portfolio risk analysis.

REQUIRED TOOLS:
- get_portfolio_state: Get portfolio state and risk metrics
- get_positions: Get position details for risk check
- get_market_data: Get market data for risk assessment
- get_stock_pool: Check stock pool status

CRITICAL RULES:
1. ONLY output a single valid JSON object. NO markdown, NO explanations, NO thinking block.
2. ALL fields required. "decision" must be APPROVE/REJECT/REVIEW.
3. "violations" is array of strings, empty if none.
4. Be concise. "risk_notes" max 80 chars.
5. Use tools for real data. NEVER fabricate risk metrics.

OUTPUT SCHEMA:
{
  "risk_id": "string",
  "var_95": 0.0,
  "var_99": 0.0,
  "volatility": 0.0,
  "max_dd": 0.0,
  "beta": 0.0,
  "decision": "APPROVE|REJECT|REVIEW",
  "violations": ["string"],
  "risk_notes": "brief note"
}
`

	systemPrompt := analysisPrompt
	if isMonitor {
		systemPrompt = monitorPrompt
	}

	userPrompt := buildAgentUserPrompt(task)

	result, err := a.CallLLMWithTools(ctx, systemPrompt, userPrompt)
	if err != nil {
		log.Printf("[Agent] Risk LLM call failed: %v", err)
		response.Type = "ERROR"
		response.Context = map[string]string{"error": err.Error()}
		return response
	}

	response.Context = result
	log.Printf("[Agent] Risk review completed")
	return response
}

func (a *Agent) processTraderTask(ctx context.Context, task AgentMessage) AgentMessage {
	response := AgentMessage{
		MessageID: fmt.Sprintf("MSG-%s-%d", a.ID, time.Now().UnixNano()),
		From:      a.Role,
		To:        task.From,
		Type:      "TRADER_EXECUTION",
		Priority:  "NORMAL",
		Timestamp: time.Now(),
	}

	systemPrompt := `You are a Trader. Your role: execute trading plans and order management.

REQUIRED TOOLS:
- get_positions: Check current positions before trading
- place_trade: Execute buy/sell orders
- get_market_data: Get real-time market data
- get_portfolio_state: Check portfolio state

CRITICAL RULES:
1. ONLY output a single valid JSON object. NO markdown, NO explanations.
2. ALL fields required. Use exact enum values.
3. Be concise. "execution_notes" max 80 chars.
4. Use tools for real data. NEVER fabricate.
5. Always check positions/portfolio before placing trades.

OUTPUT SCHEMA:
{
  "exec_id": "string",
  "strategy": "TWAP|VWAP|MARKET|LIMIT",
  "order_type": "MARKET|LIMIT|STOP",
  "slippage_est": 0.0,
  "volume_part": 0.0,
  "status": "PENDING|EXECUTING|COMPLETED",
  "execution_notes": "brief note"
}`

	userPrompt := buildAgentUserPrompt(task)

	result, err := a.CallLLMWithTools(ctx, systemPrompt, userPrompt)
	if err != nil {
		log.Printf("[Agent] Trader LLM call failed: %v", err)
		response.Type = "ERROR"
		response.Context = map[string]string{"error": err.Error()}
		return response
	}

	response.Context = result
	log.Printf("[Agent] Trader execution plan created")
	return response
}

// ValidateDecision 验证决策是否符合当前Agent角色的权限
func (a *Agent) ValidateDecision(decision *CIODecision) error {
	if decision == nil {
		return fmt.Errorf("决策不能为空")
	}

	matrix := GetPermissionMatrix(a.Role)

	switch decision.Decision {
	case DecisionNoAction:
		return nil

	case DecisionRebalance:
		if !matrix.CanMakePortfolioDecision {
			return fmt.Errorf("角色 %s 无权执行再平衡决策", a.Role)
		}

	case DecisionIncreaseRisk, DecisionReduceRisk:
		if !matrix.CanMakePortfolioDecision {
			return fmt.Errorf("角色 %s 无权调整风险水平", a.Role)
		}

	case DecisionChangeStrategy:
		if !matrix.CanCreateStrategy {
			return fmt.Errorf("角色 %s 无权变更策略", a.Role)
		}

	case DecisionPauseStrategy, DecisionPauseTrading, DecisionResumeTrading:
		if !matrix.CanMakePortfolioDecision && !matrix.CanEmergencyStop {
			return fmt.Errorf("角色 %s 无权暂停/恢复交易", a.Role)
		}

	case DecisionRequestResearch:
		if !matrix.CanCreateStrategy && !matrix.CanReadProfile {
			return fmt.Errorf("角色 %s 无权请求研究", a.Role)
		}

	case DecisionRequestRiskReview:
		if !matrix.CanAnalyzeRisk && !matrix.CanVetoDecision {
			return fmt.Errorf("角色 %s 无权请求风控复核", a.Role)
		}

	default:
		return fmt.Errorf("未知的决策类型: %s", decision.Decision)
	}

	if len(decision.Orders) > 0 && !matrix.CanCreateOrderIntent {
		return fmt.Errorf("角色 %s 无权创建订单意图", a.Role)
	}

	return nil
}

// GetWorkflowSequence 根据市场状态获取Agent执行顺序
func GetWorkflowSequence(marketRegime string) []AgentRole {
	switch marketRegime {
	case "bull":
		return []AgentRole{RolePlanner, RoleQuant, RoleRisk, RoleCIO, RoleTrader}
	case "bear":
		return []AgentRole{RoleRisk, RolePlanner, RoleQuant, RoleCIO, RoleTrader}
	case "sideways":
		return []AgentRole{RolePlanner, RoleRisk, RoleQuant, RoleCIO, RoleTrader}
	case "volatile":
		return []AgentRole{RoleRisk, RoleQuant, RolePlanner, RoleCIO, RoleTrader}
	case "recovery":
		return []AgentRole{RolePlanner, RoleQuant, RoleRisk, RoleCIO, RoleTrader}
	default:
		return []AgentRole{RolePlanner, RoleQuant, RoleRisk, RoleCIO, RoleTrader}
	}
}

// ==================== Investment Mandate ====================

// InvestmentMandate 用户投资约束（由PLANNER管理）
type InvestmentMandate struct {
	MandateID         string             `json:"mandate_id"`
	UserID            uint               `json:"user_id"`
	InvestmentGoal    string             `json:"investment_goal"`     // 长期资本增值 / 稳健收益
	RiskLevel         string             `json:"risk_level"`          // LOW / MEDIUM / HIGH
	InvestmentHorizon string             `json:"investment_horizon"`  // SHORT / MEDIUM / LONG
	MaxDrawdownPct    float64            `json:"max_drawdown_pct"`    // 最大可接受回撤
	LiquidityNeed     string             `json:"liquidity_need"`      // LOW / MEDIUM / HIGH
	SectorLimitPct    float64            `json:"sector_limit_pct"`    // 行业集中限制
	MaxSinglePosition float64            `json:"max_single_position"` // 单股最大仓位
	MinCashRatio      float64            `json:"min_cash_ratio"`      // 现金最低比例
	TargetAllocation  map[string]float64 `json:"target_allocation"`   // 目标资产配置
	LastUpdated       time.Time          `json:"last_updated"`
	Status            string             `json:"status"` // ACTIVE / EXPIRED / MODIFIED
}

// MandateChange 约束变更记录
type MandateChange struct {
	ChangeID     string    `json:"change_id"`
	MandateID    string    `json:"mandate_id"`
	Field        string    `json:"field"`
	OldValue     string    `json:"old_value"`
	NewValue     string    `json:"new_value"`
	ChangeReason string    `json:"change_reason"`
	Timestamp    time.Time `json:"timestamp"`
}

// MandateDrift 约束偏离检测结果
type MandateDrift struct {
	Field      string  `json:"field"`
	Current    float64 `json:"current"`
	Limit      float64 `json:"limit"`
	Deviation  float64 `json:"deviation"`
	Status     string  `json:"status"` // OK / WARNING / VIOLATION
	Suggestion string  `json:"suggestion"`
}

// ==================== Candidate Pool ====================

// CandidateStock 候选股票
type CandidateStock struct {
	Symbol         string             `json:"symbol"`
	Name           string             `json:"name"`
	Sector         string             `json:"sector"`
	CurrentPrice   float64            `json:"current_price"`
	FactorScores   map[string]float64 `json:"factor_scores"`
	CompositeScore float64            `json:"composite_score"`
	Rank           int                `json:"rank"`
	Signal         string             `json:"signal"` // BUY / HOLD / SELL
	TargetWeight   float64            `json:"target_weight"`
	Reason         string             `json:"reason"`
}

// CandidatePool 候选股票池（由QUANT生成）
type CandidatePool struct {
	PoolID        string             `json:"pool_id"`
	Date          time.Time          `json:"date"`
	MarketRegime  string             `json:"market_regime"`
	FactorWeights map[string]float64 `json:"factor_weights"`
	Stocks        []CandidateStock   `json:"stocks"`
	TopPicks      []CandidateStock   `json:"top_picks"`
	Methodology   string             `json:"methodology"`
	Confidence    float64            `json:"confidence"`
}

// ==================== Order Plan ====================

// OrderPlanItem 单笔订单计划
type OrderPlanItem struct {
	Symbol             string  `json:"symbol"`
	Side               string  `json:"side"` // BUY / SELL
	TargetWeight       float64 `json:"target_weight"`
	CurrentWeight      float64 `json:"current_weight"`
	RequiredTrade      float64 `json:"required_trade"`
	Strategy           string  `json:"strategy"`   // VWAP / TWAP / LIMIT / MARKET
	OrderType          string  `json:"order_type"` // MARKET / LIMIT
	LimitPrice         float64 `json:"limit_price"`
	EstSlippage        float64 `json:"est_slippage"`
	EstImpactCost      float64 `json:"est_impact_cost"`
	EstTransactionCost float64 `json:"est_transaction_cost"`
	LiquidityScore     float64 `json:"liquidity_score"`
	ExecutionWindow    string  `json:"execution_window"` // OPEN / MORNING / AFTERNOON / CLOSE
	Status             string  `json:"status"`           // PENDING / APPROVED / EXECUTING / DONE / FAILED
}

// OrderPlan 交易执行计划（由TRADER生成）
type OrderPlan struct {
	PlanID           string          `json:"plan_id"`
	DecisionID       string          `json:"decision_id"`
	MandateID        string          `json:"mandate_id"`
	Items            []OrderPlanItem `json:"items"`
	TotalEstCost     float64         `json:"total_est_cost"`
	TotalEstSlippage float64         `json:"total_est_slippage"`
	RiskApproval     string          `json:"risk_approval"`
	GeneratedAt      time.Time       `json:"generated_at"`
}

// ExecutionAttribution 执行归因（盘后）
type ExecutionAttribution struct {
	Symbol           string  `json:"symbol"`
	Side             string  `json:"side"`
	TargetWeight     float64 `json:"target_weight"`
	ActualWeight     float64 `json:"actual_weight"`
	VWAPDeviation    float64 `json:"vwap_deviation"`
	Slippage         float64 `json:"slippage"`
	TransactionCost  float64 `json:"transaction_cost"`
	MarketImpact     float64 `json:"market_impact"`
	FillRate         float64 `json:"fill_rate"`
	ExecutionQuality string  `json:"execution_quality"` // GOOD / ACCEPTABLE / POOR
	Reasoning        string  `json:"reasoning"`
}

// ==================== Factor Health ====================

// FactorHealth 因子健康度（QUANT盘后分析）
type FactorHealth struct {
	FactorName     string  `json:"factor_name"`
	CurrentHealth  float64 `json:"current_health"` // 0-100
	PreviousHealth float64 `json:"previous_health"`
	IC             float64 `json:"ic"`               // Information Coefficient
	IR             float64 `json:"ir"`               // Information Ratio
	HitRate        float64 `json:"hit_rate"`         // Signal hit rate
	DecayRate      float64 `json:"decay_rate"`       // Factor decay
	Status         string  `json:"status"`           // HEALTHY / DETERIORATING / COLLAPSING
	KalmanEstimate float64 `json:"kalman_estimate"`  // Kalman filter estimate
	SurvivalScore  float64 `json:"survival_score"`   // Survival analysis score
	CoxHazardRatio float64 `json:"cox_hazard_ratio"` // Cox model hazard ratio
}

// FactorHealthReport 因子健康报告
type FactorHealthReport struct {
	ReportID   string         `json:"report_id"`
	Date       time.Time      `json:"date"`
	Factors    []FactorHealth `json:"factors"`
	Overall    float64        `json:"overall"`
	Warnings   []string       `json:"warnings"`
	Conclusion string         `json:"conclusion"`
}

// ==================== Daily Review ====================

// CIODailyReview CIO盘后复盘
type CIODailyReview struct {
	ReviewID              string    `json:"review_id"`
	Date                  time.Time `json:"date"`
	MarketRegimeCorrect   bool      `json:"market_regime_correct"`
	StockSelectionCorrect bool      `json:"stock_selection_correct"`
	PositionSizingOK      bool      `json:"position_sizing_ok"`
	RiskWithinLimits      bool      `json:"risk_within_limits"`
	DecisionAccuracy      float64   `json:"decision_accuracy"`
	PortfolioReturn       float64   `json:"portfolio_return"`
	BenchmarkReturn       float64   `json:"benchmark_return"`
	AlphaGenerated        float64   `json:"alpha_generated"`
	TomorrowView          string    `json:"tomorrow_view"`
	ActionPlan            []string  `json:"action_plan"`
}

// RiskPostMortem 风控盘后复盘
type RiskPostMortem struct {
	ReportID            string             `json:"report_id"`
	Date                time.Time          `json:"date"`
	OverallRisk         float64            `json:"overall_risk"`
	MarketRisk          float64            `json:"market_risk"`
	FactorRisk          float64            `json:"factor_risk"`
	ConcentrationRisk   float64            `json:"concentration_risk"`
	LiquidityRisk       float64            `json:"liquidity_risk"`
	CorrelationRisk     float64            `json:"correlation_risk"`
	TailRisk            float64            `json:"tail_risk"`
	GeometricRisk       float64            `json:"geometric_risk"`
	TopologicalRisk     float64            `json:"topological_risk"`
	StructuralRisk      float64            `json:"structural_risk"`
	ExpectedRisk        float64            `json:"expected_risk"`
	RealizedRisk        float64            `json:"realized_risk"`
	RiskDeviation       float64            `json:"risk_deviation"`
	DeviationReason     string             `json:"deviation_reason"`
	TomorrowConstraints map[string]float64 `json:"tomorrow_constraints"`
}

// ==================== Daily Cycle ====================

// DailyPhase 每日周期阶段
type DailyPhase string

const (
	PhasePreMarket  DailyPhase = "PRE_MARKET"
	PhaseIntraday   DailyPhase = "INTRADAY"
	PhasePostMarket DailyPhase = "POST_MARKET"
	PhaseComplete   DailyPhase = "COMPLETE"
)

// DailyCycleResult 每日周期执行结果
type DailyCycleResult struct {
	Date           time.Time              `json:"date"`
	Phase          DailyPhase             `json:"phase"`
	Mandate        *InvestmentMandate     `json:"mandate"`
	MandateDrift   []MandateDrift         `json:"mandate_drift"`
	CandidatePool  *CandidatePool         `json:"candidate_pool"`
	RiskReport     *RiskReport            `json:"risk_report"`
	CIODecision    *CIODecision           `json:"cio_decision"`
	OrderPlan      *OrderPlan             `json:"order_plan"`
	FactorHealth   *FactorHealthReport    `json:"factor_health"`
	CIOReview      *CIODailyReview        `json:"cio_review"`
	RiskPostMortem *RiskPostMortem        `json:"risk_post_mortem"`
	Attributions   []ExecutionAttribution `json:"attributions"`
	Events         []string               `json:"events"`
	Errors         []string               `json:"errors"`
	StartedAt      time.Time              `json:"started_at"`
	CompletedAt    time.Time              `json:"completed_at"`
}

// ValidateCrossRolePermissions 验证跨角色通信权限
func ValidateCrossRolePermissions(from, to AgentRole) bool {
	if from == to {
		return true
	}

	communicationMatrix := map[AgentRole][]AgentRole{
		RoleCIO:     {RolePlanner, RoleQuant, RoleRisk, RoleTrader},
		RolePlanner: {RoleCIO, RoleQuant, RoleRisk},
		RoleQuant:   {RoleCIO, RolePlanner, RoleRisk, RoleTrader},
		RoleRisk:    {RoleCIO, RolePlanner, RoleQuant, RoleTrader},
		RoleTrader:  {RoleCIO, RoleRisk, RoleQuant},
	}

	allowedTargets, ok := communicationMatrix[from]
	if !ok {
		return false
	}

	for _, allowed := range allowedTargets {
		if allowed == to {
			return true
		}
	}

	return false
}

// ==================== Agent State Management ====================

// PlannerState 投资规划师状态
type PlannerState struct {
	UserProfile UserProfile        `json:"user_profile"`
	Mandate     *InvestmentMandate `json:"mandate"`
	Goals       []Goal             `json:"goals"`
	Constraints []Constraint       `json:"constraints"`
	RiskProfile RiskProfile        `json:"risk_profile"`
	LastUpdated time.Time          `json:"last_updated"`
}

// UserProfile 用户画像
type UserProfile struct {
	UserID               uint    `json:"user_id"`
	AgeGroup             string  `json:"age_group"`
	InvestmentExperience string  `json:"investment_experience"`
	AnnualIncome         float64 `json:"annual_income"`
	TotalAssets          float64 `json:"total_assets"`
	InvestmentRatio      float64 `json:"investment_ratio"`
	LossTolerance        string  `json:"loss_tolerance"`
	InvestmentHorizon    string  `json:"investment_horizon"`
}

// Goal 投资目标
type Goal struct {
	ID          string  `json:"id"`
	Description string  `json:"description"`
	TargetValue float64 `json:"target_value"`
	Priority    int     `json:"priority"`
}

// Constraint 投资约束
type Constraint struct {
	ID          string  `json:"id"`
	Type        string  `json:"type"`
	Description string  `json:"description"`
	MaxValue    float64 `json:"max_value"`
	MinValue    float64 `json:"min_value"`
}

// RiskProfile 风险画像
type RiskProfile struct {
	RiskScore        float64 `json:"risk_score"`
	RiskCategory     string  `json:"risk_category"` // Conservative/Moderate/Balanced/Growth/Aggressive
	MaxDrawdownPct   float64 `json:"max_drawdown_pct"`
	TargetVolatility float64 `json:"target_volatility"`
	RiskBudget       float64 `json:"risk_budget"`
}

// QuantState 量化分析师状态
type QuantState struct {
	MarketState     MarketState         `json:"market_state"`
	FactorState     FactorState         `json:"factor_state"`
	AlphaState      AlphaState          `json:"alpha_state"`
	CandidatePool   *CandidatePool      `json:"candidate_pool"`
	FactorHealth    *FactorHealthReport `json:"factor_health"`
	ResearchReports []ResearchReport    `json:"research_reports"`
	LastUpdated     time.Time           `json:"last_updated"`
}

// MarketState 市场状态
type MarketState struct {
	Regime          string    `json:"regime"` // BULLISH/BEARISH/NEUTRAL/HIGH_VOL
	Confidence      float64   `json:"confidence"`
	MarketTrend     float64   `json:"market_trend"`
	VolatilityIndex float64   `json:"volatility_index"`
	Breadth         float64   `json:"breadth"`
	Correlation     float64   `json:"correlation"`
	LastUpdated     time.Time `json:"last_updated"`
}

// FactorState 因子状态
type FactorState struct {
	Scores      map[string][]float64 `json:"scores"`
	Weights     map[string]float64   `json:"weights"`
	Crowding    map[string]float64   `json:"crowding"`
	Decay       map[string]float64   `json:"decay"`
	LastUpdated time.Time            `json:"last_updated"`
}

// AlphaState Alpha状态
type AlphaState struct {
	AlphaValue    float64            `json:"alpha_value"`
	Confidence    float64            `json:"confidence"`
	SourceFactors map[string]float64 `json:"source_factors"`
	LastUpdated   time.Time          `json:"last_updated"`
}

// CIOState CIO状态
type CIOState struct {
	Mandate           *InvestmentMandate `json:"mandate"`
	MarketState       *MarketState       `json:"market_state"`
	QuantResearch     *QuantState        `json:"quant_research"`
	CurrentPortfolio  map[string]float64 `json:"current_portfolio"`
	RiskState         *RiskState         `json:"risk_state"`
	InvestmentThesis  string             `json:"investment_thesis"`
	PreviousDecisions []CIODecision      `json:"previous_decisions"`
	LastDecision      *CIODecision       `json:"last_decision"`
	LastUpdated       time.Time          `json:"last_updated"`
}

// RiskState 风控状态
type RiskState struct {
	PortfolioRisk  map[string]interface{}   `json:"portfolio_risk"`
	FactorRisk     map[string]interface{}   `json:"factor_risk"`
	StructuralRisk map[string]interface{}   `json:"structural_risk"`
	StressResults  []map[string]interface{} `json:"stress_results"`
	WarningState   string                   `json:"warning_state"` // NORMAL/WATCH/WARNING/CRITICAL
	RiskLimits     map[string]float64       `json:"risk_limits"`
	VetoReason     string                   `json:"veto_reason"`
	LastUpdated    time.Time                `json:"last_updated"`
}

// TraderState 操盘手状态
type TraderState struct {
	OrderIntent          *OrderIntent          `json:"order_intent"`
	ExecutionPlan        *OrderPlan            `json:"execution_plan"`
	MarketMicrostructure MarketMicrostructure  `json:"market_microstructure"`
	ExecutionState       string                `json:"execution_state"` // IDLE/EXECUTING/COMPLETED/FAILED
	TransactionCost      float64               `json:"transaction_cost"`
	Slippage             float64               `json:"slippage"`
	LastExecution        *ExecutionAttribution `json:"last_execution"`
	LastUpdated          time.Time             `json:"last_updated"`
}

// MarketMicrostructure 市场微观结构
type MarketMicrostructure struct {
	Spread     float64 `json:"spread"`
	Depth      float64 `json:"depth"`
	Liquidity  float64 `json:"liquidity"`
	ImpactCost float64 `json:"impact_cost"`
	OrderFlow  string  `json:"order_flow"`
}

// ==================== Structured Decision Objects ====================

// CIOInvestmentDecision CIO结构化投资决策
type CIOInvestmentDecision struct {
	Agent            string            `json:"agent"` // "CIO"
	DecisionID       string            `json:"decision_id"`
	Action           string            `json:"action"` // REBALANCE/BUY/SELL/HOLD/WAIT
	PortfolioChanges []PortfolioChange `json:"portfolio_changes"`
	Thesis           string            `json:"thesis"`
	ExpectedReturn   float64           `json:"expected_return"`
	ExpectedVol      float64           `json:"expected_volatility"`
	MaxDDEst         float64           `json:"max_drawdown_estimate"`
	Confidence       float64           `json:"confidence"`
	Horizon          string            `json:"horizon"`
	RiskConditions   []string          `json:"risk_conditions"`
	Reason           DecisionReason    `json:"reason"`
	Timestamp        time.Time         `json:"timestamp"`
}

// PortfolioChange 组合变更
type PortfolioChange struct {
	Symbol        string  `json:"symbol"`
	CurrentWeight float64 `json:"current_weight"`
	TargetWeight  float64 `json:"target_weight"`
	Action        string  `json:"action"` // BUY/SELL/HOLD
	AlphaScore    float64 `json:"alpha_score"`
}

// DecisionReason 决策原因
type DecisionReason struct {
	Alpha           float64 `json:"alpha"`
	Regime          string  `json:"regime"`
	FactorHealth    float64 `json:"factor_health"`
	RiskLevel       string  `json:"risk_level"`
	MarketSentiment float64 `json:"market_sentiment"`
}

// RiskDecision 风控结构化决策
type RiskDecision struct {
	Agent       string           `json:"agent"` // "RISK"
	DecisionID  string           `json:"decision_id"`
	Status      string           `json:"status"` // PASS/PASS_WITH_CONDITION/REJECT/EMERGENCY_STOP
	Conditions  []string         `json:"conditions"`
	VetoReason  string           `json:"veto_reason"`
	RiskDetails RiskReviewDetail `json:"risk_details"`
	Confidence  float64          `json:"confidence"`
	Timestamp   time.Time        `json:"timestamp"`
}

// RiskReviewDetail 风控审查详情
type RiskReviewDetail struct {
	PortfolioCVaR    float64 `json:"portfolio_cvar"`
	PortfolioVaR     float64 `json:"portfolio_var"`
	Concentration    float64 `json:"concentration"`
	FactorCrowding   float64 `json:"factor_crowding"`
	StructuralRisk   float64 `json:"structural_risk"`
	LiquidityRisk    float64 `json:"liquidity_risk"`
	StressTestPassed bool    `json:"stress_test_passed"`
}

// ExecutionDecision 执行结构化决策
type ExecutionDecision struct {
	Agent          string              `json:"agent"` // "TRADER"
	DecisionID     string              `json:"decision_id"`
	CIODecisionID  string              `json:"cio_decision_id"`
	RiskDecisionID string              `json:"risk_decision_id"`
	Executions     []ExecutionPlanItem `json:"executions"`
	TotalEstCost   float64             `json:"total_est_cost"`
	TotalSlippage  float64             `json:"total_slippage"`
	Timestamp      time.Time           `json:"timestamp"`
}

// ExecutionPlanItem 执行计划项
type ExecutionPlanItem struct {
	Symbol         string  `json:"symbol"`
	Side           string  `json:"side"`
	TargetWeight   float64 `json:"target_weight"`
	Method         string  `json:"method"` // VWAP/TWAP/POV/LIMIT/MARKET
	Duration       string  `json:"duration"`
	LimitPrice     float64 `json:"limit_price"`
	EstSlippage    float64 `json:"est_slippage"`
	EstImpact      float64 `json:"est_impact"`
	LiquidityScore float64 `json:"liquidity_score"`
	Urgency        string  `json:"urgency"` // NORMAL/URGENT
}

// ==================== Agent State Manager ====================

// AgentStateManager Agent状态管理器
type AgentStateManager struct {
	plannerState PlannerState
	quantState   QuantState
	cioState     CIOState
	riskState    RiskState
	traderState  TraderState
	mu           sync.RWMutex
}

// NewAgentStateManager 创建Agent状态管理器
func NewAgentStateManager() *AgentStateManager {
	return &AgentStateManager{
		plannerState: PlannerState{
			RiskProfile: RiskProfile{
				RiskCategory:     "MODERATE",
				RiskScore:        0.5,
				MaxDrawdownPct:   0.15,
				TargetVolatility: 0.12,
			},
		},
		quantState: QuantState{
			MarketState: MarketState{
				Regime: "NEUTRAL",
			},
		},
		cioState: CIOState{
			InvestmentThesis: "等待量化研究结果",
		},
		riskState: RiskState{
			WarningState: "NORMAL",
		},
		traderState: TraderState{
			ExecutionState: "IDLE",
		},
	}
}

// GetPlannerState 获取投资规划师状态
func (m *AgentStateManager) GetPlannerState() PlannerState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.plannerState
}

// SetPlannerState 更新投资规划师状态
func (m *AgentStateManager) SetPlannerState(state PlannerState) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state.LastUpdated = time.Now()
	m.plannerState = state
}

// GetQuantState 获取量化分析师状态
func (m *AgentStateManager) GetQuantState() QuantState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.quantState
}

// SetQuantState 更新量化分析师状态
func (m *AgentStateManager) SetQuantState(state QuantState) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state.LastUpdated = time.Now()
	m.quantState = state
}

// GetCIOState 获取CIO状态
func (m *AgentStateManager) GetCIOState() CIOState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cioState
}

// SetCIOState 更新CIO状态
func (m *AgentStateManager) SetCIOState(state CIOState) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state.LastUpdated = time.Now()
	m.cioState = state
}

// GetRiskState 获取风控师状态
func (m *AgentStateManager) GetRiskState() RiskState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.riskState
}

// SetRiskState 更新风控师状态
func (m *AgentStateManager) SetRiskState(state RiskState) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state.LastUpdated = time.Now()
	m.riskState = state
}

// GetTraderState 获取操盘手状态
func (m *AgentStateManager) GetTraderState() TraderState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.traderState
}

// SetTraderState 更新操盘手状态
func (m *AgentStateManager) SetTraderState(state TraderState) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state.LastUpdated = time.Now()
	m.traderState = state
}

// GetStateForRole 根据角色获取状态
func (m *AgentStateManager) GetStateForRole(role AgentRole) interface{} {
	switch role {
	case RolePlanner:
		return m.GetPlannerState()
	case RoleQuant:
		return m.GetQuantState()
	case RoleCIO:
		return m.GetCIOState()
	case RoleRisk:
		return m.GetRiskState()
	case RoleTrader:
		return m.GetTraderState()
	default:
		return nil
	}
}

// truncateString 截断字符串到指定长度
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
