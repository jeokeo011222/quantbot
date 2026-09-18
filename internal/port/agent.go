package port

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"
)

// ==================== LLM 契约与数据模型（复刻自 brain/llm/client.go，自包含） ====================

// LLMClient LLM 客户端接口。宿主将真实 LLM 客户端（internal/llmmonitoring 包装或
// 直连提供者）适配为本接口后注入；决策脑经总线以 JSON 跨边界兑现 Chat/StreamChat/Embedding。
type LLMClient interface {
	Chat(ctx context.Context, messages []Message, tools []Tool) (*ChatResult, error)
	StreamChat(ctx context.Context, messages []Message, tools []Tool) (*StreamReader, error)
	Embedding(ctx context.Context, text string) ([]float64, error)
}

// Message LLM 聊天消息（对齐 llm.Message，JSON 形状一致，跨 DLL 互转）。
type Message struct {
	Role             string         `json:"role"`
	Content          string         `json:"content,omitempty"`
	ReasoningContent string         `json:"reasoning_content,omitempty"` // deepseek-chat thinking mode
	ToolCalls        []ToolCallInfo `json:"tool_calls,omitempty"`
	ToolCallID       string         `json:"tool_call_id,omitempty"`
}

// ToolCallFunction 工具调用函数参数（对齐 llm.ToolCallInfo.Function）。
type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ToolCallInfo 工具调用信息（来自 LLM 响应，对齐 llm.ToolCallInfo）。
type ToolCallInfo struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function ToolCallFunction `json:"function"`
}

// Tool LLM 工具定义（发送给 LLM 的 function calling 参数，对齐 llm.Tool）。
type Tool struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

// Usage LLM token 用量（对齐 llm.ChatResult.Usage）。
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ChoiceMessage 单条响应消息（对齐 llm.ChatResult.Choices[].Message）。
type ChoiceMessage struct {
	Content          string         `json:"content"`
	Role             string         `json:"role"`
	ReasoningContent string         `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCallInfo `json:"tool_calls,omitempty"`
}

// ChatChoice 单条响应选择（对齐 llm.ChatResult.Choices[]）。
type ChatChoice struct {
	Index        int           `json:"index"`
	Message      ChoiceMessage `json:"message"`
	FinishReason string        `json:"finish_reason"`
}

// ChatResult 聊天结果（对齐 llm.ChatResult）。
type ChatResult struct {
	ID      string       `json:"id"`
	Choices []ChatChoice `json:"choices"`
	Usage   Usage        `json:"usage"`
	Model   string       `json:"model"`
}

// StreamReader 流式响应读取器（对齐 llm.StreamReader）。
type StreamReader struct {
	reader io.ReadCloser
}

// Close 关闭底层读取器。
func (sr *StreamReader) Close() error {
	if sr.reader != nil {
		return sr.reader.Close()
	}
	return nil
}

// NewStreamReader 创建 StreamReader。
func NewStreamReader(r io.ReadCloser) *StreamReader {
	return &StreamReader{reader: r}
}

// ==================== Agent / 编排 DTO（复刻自 brain/agents，自包含） ====================

// AgentRole Agent 角色（对齐 agents.AgentRole）。
type AgentRole string

const (
	RoleCIO     AgentRole = "CIO"
	RolePlanner AgentRole = "PLANNER"
	RoleQuant   AgentRole = "QUANT"
	RoleRisk    AgentRole = "RISK"
	RoleTrader  AgentRole = "TRADER"
)

// AgentState Agent 状态机状态（对齐 agents.AgentState）。
type AgentState string

const (
	StateIdle      AgentState = "IDLE"
	StateThinking  AgentState = "THINKING"
	StateCompleted AgentState = "COMPLETED"
	StateFailed    AgentState = "FAILED"
)

// DecisionType CIO 决策类型（对齐 agents.DecisionType）。
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

// AgentMessage Agent 间消息协议（对齐 agents.AgentMessage）。
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

// OrderIntent 订单意图（不直接产生 Broker 订单，对齐 agents.OrderIntent）。
type OrderIntent struct {
	Symbol       string  `json:"symbol"`
	TargetWeight float64 `json:"target_weight"`
	Side         string  `json:"side"`
	MaxNotional  float64 `json:"max_notional"`
	Reason       string  `json:"reason"`
	DecisionID   string  `json:"decision_id"`
	SignalSource string  `json:"signal_source"`
}

// CIODecision CIO 决策（对齐 agents.CIODecision）。
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
	Optimization     interface{}   `json:"optimization,omitempty"`
	SixDim           interface{}   `json:"sixdim,omitempty"`
	LLMReview        interface{}   `json:"llm_review,omitempty"`
	Evidence         interface{}   `json:"evidence,omitempty"`
}

// TokenUsage Token 用量统计（对齐 agents.TokenUsage）。
type TokenUsage struct {
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	TotalTokens      int     `json:"total_tokens"`
	EstimatedCost    float64 `json:"estimated_cost"`
}

// DecisionStep 单步决策步骤（对齐 agents.DecisionStep）。
type DecisionStep struct {
	Step       int         `json:"step"`
	Type       string      `json:"type"`
	Content    string      `json:"content"`
	Timestamp  time.Time   `json:"timestamp"`
	ToolName   string      `json:"tool_name,omitempty"`
	ToolArgs   interface{} `json:"tool_args,omitempty"`
	ToolResult interface{} `json:"tool_result,omitempty"`
	Thought    string      `json:"thought,omitempty"`
	DurationMs int64       `json:"duration_ms,omitempty"`
}

// SessionState 会话状态（对齐 agents.SessionState）。
type SessionState struct {
	ID           string           `json:"id"`
	StartTime    time.Time        `json:"start_time"`
	EndTime      time.Time        `json:"end_time"`
	Confidence   float64          `json:"confidence"`
	Decision     interface{}      `json:"decision"`
	DecisionType string           `json:"decision_type"`
	ToolCalls    []ToolCallRecord `json:"tool_calls"`
	Steps        []DecisionStep   `json:"steps"`
	Status       string           `json:"status"`
	TrajectoryID string           `json:"trajectory_id"`
	TokenUsage   TokenUsage       `json:"token_usage"`
}

// StepType 轨迹步骤类型（对齐 agents.StepType）。
type StepType string

const (
	StepThink       StepType = "THINK"
	StepToolCall    StepType = "TOOL_CALL"
	StepToolResult  StepType = "TOOL_RESULT"
	StepSubAgent    StepType = "SUB_AGENT"
	StepCritique    StepType = "CRITIQUE"
	StepFinalAnswer StepType = "FINAL_ANSWER"
	StepError       StepType = "ERROR"
)

// AgentMode Agent 运行模式（对齐 agents.AgentMode）。
type AgentMode string

const (
	ModeStandard AgentMode = "STANDARD"
	ModePTC      AgentMode = "PTC"
	ModeMinimal  AgentMode = "MINIMAL"
	ModeCreator  AgentMode = "CREATOR"
)

// TrajectoryStep 轨迹中的单个步骤（对齐 agents.TrajectoryStep）。
type TrajectoryStep struct {
	Step        int           `json:"step"`
	Timestamp   time.Time     `json:"timestamp"`
	Type        StepType      `json:"type"`
	Thought     string        `json:"thought,omitempty"`
	Action      string        `json:"action,omitempty"`
	ActionInput interface{}   `json:"action_input,omitempty"`
	Observation interface{}   `json:"observation,omitempty"`
	Result      string        `json:"result,omitempty"`
	Duration    time.Duration `json:"duration"`
	TokenDelta  int           `json:"token_delta"`
}

// Trajectory 一次 Agent 运行的完整轨迹（对齐 agents.Trajectory）。
type Trajectory struct {
	ID         string                 `json:"id"`
	AgentID    string                 `json:"agent_id"`
	AgentName  string                 `json:"agent_name"`
	AgentRole  AgentRole              `json:"agent_role"`
	SkillID    string                 `json:"skill_id,omitempty"`
	StartTime  time.Time              `json:"start_time"`
	EndTime    time.Time              `json:"end_time"`
	Status     string                 `json:"status"`
	Mode       AgentMode              `json:"mode"`
	Input      string                 `json:"input"`
	Output     string                 `json:"output"`
	Steps      []TrajectoryStep       `json:"steps"`
	SubAgents  []string               `json:"sub_agents,omitempty"`
	ParentID   string                 `json:"parent_id,omitempty"`
	Confidence float64                `json:"confidence"`
	TokenUsage TokenUsage             `json:"token_usage"`
	Metadata   map[string]interface{} `json:"metadata,omitempty"`
}

// OrchestratorResult 编排执行结果（对齐 agents.OrchestratorResult，跨 DLL 以 JSON 传输）。
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

// ToolCategory 工具分类（对齐 agents.ToolCategory）。
type ToolCategory string

const (
	CategoryMarketData  ToolCategory = "market_data"
	CategoryStockSearch ToolCategory = "stock_search"
	CategoryMarketStats ToolCategory = "market_stats"
	CategoryBacktest    ToolCategory = "backtest"
	CategoryRisk        ToolCategory = "risk"
	CategoryExecution   ToolCategory = "execution"
	CategoryPortfolio   ToolCategory = "portfolio"
	CategoryResearch    ToolCategory = "research"
	CategorySystem      ToolCategory = "system"
)

// ToolMetadata 工具元数据（对齐 agents.ToolMetadata）。
type ToolMetadata struct {
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Category    ToolCategory `json:"category"`
	Roles       []AgentRole  `json:"roles"`
	RiskLevel   string       `json:"risk_level"`
	Enabled     bool         `json:"enabled"`
	Version     string       `json:"version"`
	Tool        ToolExecutor `json:"-"`
}

// ToolRegistry 工具注册表：宿主装配侧构建的「角色 → 工具元数据」表，用于供给
// 决策脑 DLL 做运行时权限校验与工具目录。纯元数据持有，不含执行逻辑。
type ToolRegistry struct {
	mu    sync.Mutex
	tools map[string]*ToolMetadata
}

// NewToolRegistry 创建工具注册表。
func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{tools: map[string]*ToolMetadata{}}
}

// Register 注册工具元数据（同名覆盖）。
func (r *ToolRegistry) Register(tm *ToolMetadata) error {
	if tm == nil {
		return fmt.Errorf("tool metadata is nil")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[tm.Name] = tm
	return nil
}

// Get 按工具名查询元数据。
func (r *ToolRegistry) Get(name string) (*ToolMetadata, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.tools[name]
	return t, ok
}

// All 返回全部工具元数据。
func (r *ToolRegistry) All() []*ToolMetadata {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*ToolMetadata, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	return out
}

// Agent 团队成员（数据侧，对齐 agents.Agent 的宿主可见字段）。宿主装配侧以
// 角色 → 工具 的目录形式经总线供给决策脑 DLL，不承载 DLL 内运行逻辑。
type Agent struct {
	ID           string
	Role         AgentRole
	Name         string
	State        AgentState
	Tools        []ToolExecutor
	ToolRegistry *ToolRegistry
}

// NewAgent 便捷构造器：仅填充宿主可见字段。
func NewAgent(id string, role AgentRole) *Agent {
	return &Agent{
		ID:    id,
		Role:  role,
		Tools: make([]ToolExecutor, 0),
	}
}

// RegisterTool 注册工具到 Agent。
func (a *Agent) RegisterTool(tool ToolExecutor) {
	a.Tools = append(a.Tools, tool)
}

// SetToolRegistry 注入工具注册表（运行时权限强制校验用元数据表）。
func (a *Agent) SetToolRegistry(r *ToolRegistry) {
	a.ToolRegistry = r
}

// Skill 技能封装（对齐 agents.Skill 的宿主可见字段）。
type Skill struct {
	ID           string              `json:"id"`
	Name         string              `json:"name"`
	Description  string              `json:"description"`
	Version      string              `json:"version"`
	Author       string              `json:"author"`
	Tags         []string            `json:"tags"`
	SystemPrompt string              `json:"system_prompt"`
	Knowledge    []KnowledgeResource `json:"knowledge"`
	Tools        []ToolExecutor      `json:"-"`
	ToolNames    []string            `json:"tool_names"`
	Workflow     *WorkflowDef        `json:"workflow,omitempty"`
	Enabled      bool                `json:"enabled"`
	CreatedAt    string              `json:"created_at"`
}

// KnowledgeResource 知识资源（对齐 agents.KnowledgeResource）。
type KnowledgeResource struct {
	Type     string `json:"type"`
	Content  string `json:"content"`
	Path     string `json:"path"`
	Priority int    `json:"priority"`
}

// WorkflowStep 工作流步骤（对齐 agents.WorkflowStep）。
type WorkflowStep struct {
	ID        string                 `json:"id"`
	Name      string                 `json:"name"`
	Type      string                 `json:"type"`
	AgentRole AgentRole              `json:"agent_role,omitempty"`
	Config    map[string]interface{} `json:"config"`
	NextStep  string                 `json:"next_step,omitempty"`
}

// WorkflowDef 工作流定义（对齐 agents.WorkflowDef）。
type WorkflowDef struct {
	ID          string                 `json:"id"`
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Steps       []WorkflowStep         `json:"steps"`
	Variables   map[string]interface{} `json:"variables"`
}

// WorkflowContext 工作流执行上下文（对齐 agents.WorkflowContext）。
type WorkflowContext struct {
	mu     sync.RWMutex
	Data   map[string]interface{} `json:"data"`
	Status string                 `json:"status"`
	Logs   []string               `json:"logs"`
}

// NewWorkflowContext 创建工作流上下文。
func NewWorkflowContext() *WorkflowContext {
	return &WorkflowContext{
		Data:   make(map[string]interface{}),
		Status: "initialized",
		Logs:   make([]string, 0),
	}
}

// Get 获取上下文数据。
func (wc *WorkflowContext) Get(key string) (interface{}, bool) {
	wc.mu.RLock()
	defer wc.mu.RUnlock()
	val, ok := wc.Data[key]
	return val, ok
}

// Set 设置上下文数据。
func (wc *WorkflowContext) Set(key string, value interface{}) {
	wc.mu.Lock()
	defer wc.mu.Unlock()
	wc.Data[key] = value
}

// Log 添加日志。
func (wc *WorkflowContext) Log(msg string) {
	wc.mu.Lock()
	defer wc.mu.Unlock()
	wc.Logs = append(wc.Logs, msg)
}
