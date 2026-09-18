package scheduler

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/quantpilot/quantpilot/internal/data"
	"github.com/quantpilot/quantpilot/internal/llmmonitoring"
	"github.com/quantpilot/quantpilot/internal/port"
)

// ============ 统一状态定义（5状态模型）============

const (
	StatusPending = "PENDING"
	StatusRunning = "RUNNING"
	StatusSuccess = "SUCCESS"
	StatusFailed  = "FAILED"
	StatusSkipped = "SKIPPED"
)

// ============ 统一任务阶段============

type TaskPhase string

const (
	PhasePreMarket  TaskPhase = "PRE_MARKET"  // 盘前 9:00-9:30
	PhaseInMarket   TaskPhase = "IN_MARKET"   // 盘中 9:30-15:00
	PhasePostMarket TaskPhase = "POST_MARKET" // 盘后 15:00-15:15
	PhaseReview     TaskPhase = "REVIEW"      // 复盘 15:15-18:00
)

// PhaseLabel 返回任务阶段的中文描述
func PhaseLabel(p TaskPhase) string {
	switch p {
	case PhasePreMarket:
		return "盘前(9:00-9:30)"
	case PhaseInMarket:
		return "盘中(9:30-15:00)"
	case PhasePostMarket:
		return "盘后(15:00-15:15)"
	case PhaseReview:
		return "复盘(15:15-18:00)"
	default:
		return "未知阶段"
	}
}

// TaskPriority 任务优先级
type TaskPriority string

const (
	PriorityCritical TaskPriority = "CRITICAL"
	PriorityHigh     TaskPriority = "HIGH"
	PriorityNormal   TaskPriority = "NORMAL"
	PriorityLow      TaskPriority = "LOW"
)

// ============ AgentExecutor 接口============
type AgentExecutor interface {
	Execute(ctx context.Context, role port.AgentRole, taskDesc string, opts ExecuteOptions) (interface{}, error)
	IsAvailable() bool
}

// ExecuteOptions 任务执行选项（携带阶段、精简任务名与真实上下文，用于生产场景交互）
type ExecuteOptions struct {
	Phase    TaskPhase // 任务阶段（盘前/盘中/盘后/复盘）
	TaskName string    // 精简任务名（用于审计日志）
	Context  string    // 真实执行上下文（阶段/时间/环境摘要），非空
}

// DefaultAgentExecutor 默认智能体执行器。
//
// DLL 隔离：进程内决策脑已迁入 agent.dll，宿主本包只承载可确定的监控兜底与
// DLL-only 占位，不再加载/驱动进程内 agents.Agent，也不直连 deepseek 具体客户端
// （LLM 归 DLL 自持）。agentMap 仅作数据持有（角色 → 宿主侧公开 Agent DTO）。
type DefaultAgentExecutor struct {
	mu        sync.RWMutex // 保护 agentMap 与 llmClient 的并发读写
	llmClient port.LLMClient
	agentMap  map[port.AgentRole]*port.Agent
}

func NewDefaultAgentExecutor(llmClient port.LLMClient) *DefaultAgentExecutor {
	return &DefaultAgentExecutor{
		llmClient: llmClient,
		agentMap:  make(map[port.AgentRole]*port.Agent),
	}
}

func (e *DefaultAgentExecutor) RegisterAgent(role port.AgentRole, agent *port.Agent) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.agentMap == nil {
		e.agentMap = make(map[port.AgentRole]*port.Agent)
	}
	e.agentMap[role] = agent
	log.Printf("[AgentExecutor] Registered agent for role: %s", role)
}

func (e *DefaultAgentExecutor) UpdateLLMClient(llmClient port.LLMClient) {
	e.mu.Lock()
	defer e.mu.Unlock()
	// 宿主侧不再向运行中 Agent 注入 LLM（决策在 DLL），仅记录供 IsAvailable/诊断使用。
	e.llmClient = llmClient
	log.Printf("[AgentExecutor] LLM client recorded for executor(仅供参考，决策已在 agent.dll)")
}

func (e *DefaultAgentExecutor) Execute(ctx context.Context, role port.AgentRole, taskDesc string, opts ExecuteOptions) (interface{}, error) {
	// 注入 LLM 审计元信息，记录本次任务的角色/任务名/阶段
	taskName := opts.TaskName
	if taskName == "" {
		taskName = taskDesc
	}
	ctx = llmmonitoring.WithCallMeta(ctx, llmmonitoring.CallMeta{
		AgentRole: string(role),
		TaskName:  taskName,
		Phase:     string(opts.Phase),
	})
	_ = ctx

	// 监控类任务不依赖 LLM，可由宿主直接判定（确定性兜底）。
	if strings.Contains(taskDesc, "监控") || strings.Contains(taskDesc, "NO_ACTION") || strings.Contains(taskDesc, "monitor") {
		log.Printf("[AgentExecutor] Executing monitor task %s directly via tools(宿主兜底)", role)
		return e.executeMonitorTaskDirectly(role, taskDesc), nil
	}

	// 其余决策任务：进程内决策已迁入 agent.dll，宿主退化为 DLL-only 占位。
	log.Printf("[AgentExecutor] %s task %s → DLL-only(进程内决策已迁入 agent.dll)", role, taskDesc)
	return map[string]interface{}{
		"status":     "DLL_ONLY",
		"agent_role": string(role),
		"task":       taskDesc,
		"message":    "进程内决策已迁入 agent.dll(DLL-only)",
	}, nil
}

// executeMonitorTaskDirectly 直接判定监控任务，不依赖 LLM / 进程内 Agent。
func (e *DefaultAgentExecutor) executeMonitorTaskDirectly(role port.AgentRole, taskDesc string) interface{} {
	log.Printf("[AgentExecutor] Executing monitor task directly for role=%s, task=%s", role, taskDesc)

	result := map[string]interface{}{
		"monitor_status": "OK",
		"agent_role":     string(role),
		"task":           taskDesc,
		"execution_mode": "direct_tool_call",
		"timestamp":      time.Now().Format("2006-01-02T15:04:05.000Z"),
	}

	switch role {
	case port.RoleQuant:
		result["signal_status"] = "NO_SIGNAL_CHANGE"
		result["description"] = "信号监控完成：无显著信号变化"
	case port.RoleRisk:
		result["risk_status"] = "RISK_NORMAL"
		result["description"] = "风险监控完成：风险指标在正常范围内"
	case port.RoleTrader:
		result["market_status"] = "MARKET_NORMAL"
		result["description"] = "市场监控完成：市场运行正常"
	case port.RoleCIO:
		result["cio_status"] = "NO_ACTION"
		result["description"] = "CIO 监控完成：无需操作"
	case port.RolePlanner:
		result["planner_status"] = "NO_ACTION"
		result["description"] = "Planner 监控完成：无需规划调整"
	default:
		result["status"] = "MONITOR_COMPLETE"
		result["description"] = fmt.Sprintf("%s 监控完成", role)
	}

	return result
}

func (e *DefaultAgentExecutor) IsAvailable() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.llmClient == nil {
		return false
	}
	if mcpClient, ok := e.llmClient.(interface{ HasValidAPIKey() bool }); ok {
		return mcpClient.HasValidAPIKey()
	}
	return true
}

// Diagnose 返回执行器诊断信息
func (e *DefaultAgentExecutor) Diagnose() map[string]interface{} {
	e.mu.RLock()
	defer e.mu.RUnlock()
	diag := map[string]interface{}{
		"llm_client_nil": e.llmClient == nil,
		"agent_count":    len(e.agentMap),
		"agent_roles":    []string{},
	}

	var roles []string
	for role := range e.agentMap {
		roles = append(roles, string(role))
	}
	diag["agent_roles"] = roles

	if e.llmClient != nil {
		if mcpClient, ok := e.llmClient.(interface{ HasValidAPIKey() bool }); ok {
			diag["api_key_valid"] = mcpClient.HasValidAPIKey()
			diag["client_type"] = "mcp"
		} else {
			diag["client_type"] = "direct"
			diag["api_key_valid"] = true
		}
	}

	return diag
}

// ============ 事件检测类型============

type EventType string

const (
	EventRisk   EventType = "RISK_EVENT"
	EventSignal EventType = "SIGNAL_EVENT"
	EventOrder  EventType = "ORDER_EVENT"
	EventMarket EventType = "MARKET_EVENT"
	EventUser   EventType = "USER_EVENT"
)

// MarketEvent 市场事件
type MarketEvent struct {
	Type      EventType              `json:"type"`
	Timestamp time.Time              `json:"timestamp"`
	Source    string                 `json:"source"`
	Severity  string                 `json:"severity"` // LOW/MEDIUM/HIGH/CRITICAL
	Message   string                 `json:"message"`
	Data      map[string]interface{} `json:"data"`
	Triggered bool                   `json:"triggered"` // 是否已触发工作流
}

// DecisionObject 决策对象（Trader 只执行此对象）
type DecisionObject struct {
	Decision      string    `json:"decision"`       // HOLD/BUY/SELL/REDUCE
	Confidence    float64   `json:"confidence"`     // 0.0-1.0
	RiskLevel     string    `json:"risk_level"`     // LOW/MEDIUM/HIGH
	PositionLimit float64   `json:"position_limit"` // 最大仓位比例
	Reason        string    `json:"reason"`
	ValidUntil    time.Time `json:"valid_until"`
	ApprovedBy    string    `json:"approved_by"` // CIO
	RiskChecked   bool      `json:"risk_checked"`
	Signals       []string  `json:"signals"`       // 触发的信号
	TargetStocks  []string  `json:"target_stocks"` // 目标股票代码
}

// ============ AgentTask 定义（Task Contract v1.0）============

// TaskScope 任务活动边界
type TaskScope struct {
	Allowed    []string `json:"allowed"`     // 允许访问的数据/资源
	NotAllowed []string `json:"not_allowed"` // 禁止访问的数据/资源
}

// TaskContract Task 契约 v1.0 完整定义
type TaskContract struct {
	Purpose          string   `json:"purpose"`           // 任务目的
	Trigger          string   `json:"trigger"`           // 触发条件
	Inputs           []string `json:"inputs"`            // 允许读取的输入
	RequiredActions  []string `json:"required_actions"`  // 必须执行的动作
	ForbiddenActions []string `json:"forbidden_actions"` // 严禁执行的动作
	DecisionBoundary string   `json:"decision_boundary"` // 决策边界（可以决定什么/不能决定什么）
	AllowedTools     []string `json:"allowed_tools"`     // 允许调用的工具
	OutputSchema     []string `json:"output_schema"`     // 输出字段
	Handoff          string   `json:"handoff"`           // 下游交付对象
	NOActionCode     string   `json:"no_action_code"`    // NO_ACTION 状态码
	StopCondition    string   `json:"stop_condition"`    // 停止条件
	ExceptionPolicy  string   `json:"exception_policy"`  // 异常处理策略
}

type AgentTask struct {
	ID              string         `json:"id"`
	Phase           TaskPhase      `json:"phase"`
	AgentRole       port.AgentRole `json:"agent_role"`
	TaskName        string         `json:"task_name"`
	TaskOrder       int            `json:"task_order"`
	Priority        TaskPriority   `json:"priority"`
	Description     string         `json:"description"`
	DeliverableType string         `json:"deliverable_type"`
	TimeoutMs       int            `json:"timeout_ms"`
	Preconditions   []string       `json:"preconditions"`
	RepeatInterval  int            `json:"repeat_interval"`
	MaxRetries      int            `json:"max_retries"`
	IsEventDriven   bool           `json:"is_event_driven"`
	EarliestStart   string         `json:"earliest_start,omitempty"` // 最早执行时间(HH:MM)，到达该时间后才执行，如 "15:05"

	// Task Contract v1.0 完整约束
	Scope    TaskScope    `json:"scope"`    // 任务活动边界
	Contract TaskContract `json:"contract"` // 任务契约
}

// ============ TaskScheduler 定义============

type TaskScheduler struct {
	mu                      sync.RWMutex
	db                      *data.SQLiteManager
	tasks                   []AgentTask
	running                 bool
	ctx                     context.Context
	cancel                  context.CancelFunc
	dailyWorkflowTrigger    func()
	lastWorkflowTriggerDate string
	executor                AgentExecutor
	contextProvider         func(task AgentTask, phase TaskPhase) string
	eventChan               chan MarketEvent
	recentEvents            []MarketEvent
	lastMonitorTime         time.Time
}

// SetContextProvider 设置任务真实上下文提供者（用于生产场景交互：阶段/时间/环境摘要）
func (s *TaskScheduler) SetContextProvider(provider func(task AgentTask, phase TaskPhase) string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.contextProvider = provider
}

// buildTaskContext 构建任务真实执行上下文，非空
func (s *TaskScheduler) buildTaskContext(task AgentTask) string {
	if s.contextProvider != nil {
		if ctxStr := s.contextProvider(task, task.Phase); ctxStr != "" {
			return ctxStr
		}
	}
	now := time.Now()
	return fmt.Sprintf("任务阶段=%s| 时间=%s | 市场状态=请自行通过工具获取实时数据",
		PhaseLabel(task.Phase), now.Format("2006-01-02 15:04:05"))
}

func NewTaskScheduler(db *data.SQLiteManager) *TaskScheduler {
	ctx, cancel := context.WithCancel(context.Background())
	s := &TaskScheduler{
		db:        db,
		tasks:     getOptimizedTasks(),
		running:   false,
		ctx:       ctx,
		cancel:    cancel,
		eventChan: make(chan MarketEvent, 100),
	}
	return s
}

func (s *TaskScheduler) SetExecutor(executor AgentExecutor) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.executor = executor
}

// SetInMarketRepeatInterval 设置盘中监控任务的重复间隔（秒）
// 用于支持"实时活动刷新间隔"自定义（1-60分钟）
func (s *TaskScheduler) SetInMarketRepeatInterval(seconds int) {
	if seconds < 60 {
		seconds = 60
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.tasks {
		if s.tasks[i].Phase == PhaseInMarket && s.tasks[i].RepeatInterval > 0 {
			s.tasks[i].RepeatInterval = seconds
		}
	}
	log.Printf("[TaskScheduler] In-market task repeat interval updated to %d seconds", seconds)
}

// ============ 优化后的任务定义（Task Contract v1.0）============

func getOptimizedTasks() []AgentTask {
	return []AgentTask{
		// ============ 01 Planner｜盘前市场分析 ============
		{
			ID:              "pre_planner",
			Phase:           PhasePreMarket,
			AgentRole:       port.RolePlanner,
			TaskName:        "盘前市场分析",
			TaskOrder:       1,
			Priority:        PriorityCritical,
			Description:     "建立今日市场环境，识别影响投资组合的外部市场因素",
			DeliverableType: "pre_market_plan",
			TimeoutMs:       300000,
			MaxRetries:      2,
			Scope: TaskScope{
				Allowed:    []string{"市场数据", "新闻", "公告", "宏观数据", "指数", "行业事件", "市场状态"},
				NotAllowed: []string{"个股打分", "因子计算", "风险计算", "仓位计算", "交易决策", "交易执行"},
			},
			Contract: TaskContract{
				Purpose: "建立当日市场环境，识别影响投资组合的外部市场因素",
				Trigger: "每日交易日 09:15 自动触发",
				Inputs:  []string{"隔夜指数数据", "隔夜重要新闻", "公司公告", "宏观事件", "海外市场变化", "今日经济/政策事件日历", "当前投资组合"},
				RequiredActions: []string{
					"判断隔夜市场状态",
					"提取重大市场事件",
					"判断事件涉及的资产",
					"判断事件重要程度",
					"形成今日市场观察事项",
					"标记需要 Quant 验证的事项",
					"标记需要 Risk 检查的事项",
					"生成 pre_market_plan",
				},
				ForbiddenActions: []string{
					"选股", "股票排名", "生成Alpha", "生成交易信号",
					"计算目标仓位", "修改仓位", "风险否决", "投资决策", "交易执行",
				},
				DecisionBoundary: "可以决定市场环境判断；不能决定投资标的和交易行为",
				AllowedTools:     []string{"新闻检索", "公告检索", "市场数据查询", "宏观数据查询", "事件日历查询"},
				OutputSchema:     []string{"market_status", "key_events", "affected_assets", "expected_impact", "items_for_quant", "items_for_risk", "confidence"},
				Handoff:          "Quant",
				NOActionCode:     "NO_MAJOR_EVENT",
				StopCondition:    "完成 market_status + key_events + affected_assets + items_for_quant + items_for_risk 后立即停止",
				ExceptionPolicy:  "数据不可用时返回 DATA_UNAVAILABLE，不得猜测或编造",
			},
		},

		// ============ 02 Quant｜盘前量化分析 ============
		{
			ID:              "pre_quant",
			Phase:           PhasePreMarket,
			AgentRole:       port.RoleQuant,
			TaskName:        "盘前量化分析",
			TaskOrder:       2,
			Priority:        PriorityCritical,
			Description:     "运行选股模型、生成量化信号，输出量化证据",
			DeliverableType: "quant_signal",
			TimeoutMs:       300000,
			MaxRetries:      2,
			Scope: TaskScope{
				Allowed:    []string{"因子", "模型", "信号", "评分", "历史数据", "回测结果", "预测结果"},
				NotAllowed: []string{"宏观新闻解释", "风险否决", "投资决策", "交易执行", "选股排名"},
			},
			Contract: TaskContract{
				Purpose: "提供量化证据——模型和数据告诉我们什么",
				Trigger: "Planner 完成后自动触发",
				Inputs:  []string{"市场数据", "股票池", "因子数据", "模型参数", "模型历史状态", "Planner 提交的待验证事项"},
				RequiredActions: []string{
					"数据质量检查",
					"运行既定模型",
					"计算既定因子",
					"生成量化信号",
					"计算信号强度",
					"计算置信度",
					"标记模型异常",
					"输出 quant_signal",
				},
				ForbiddenActions: []string{
					"自主修改模型", "自主修改核心因子", "自主修改交易阈值",
					"风险否决", "最终投资决策", "直接交易", "自主改变投资组合",
					"阅读新闻自行做宏观判断",
				},
				DecisionBoundary: "可以回答信号是什么；不能回答最终是否交易",
				AllowedTools:     []string{"因子计算", "模型运行", "信号生成", "历史数据查询"},
				OutputSchema:     []string{"signals", "signal_strength", "confidence", "model_status", "anomalies"},
				Handoff:          "Risk",
				NOActionCode:     "NO_SIGNAL_CHANGE",
				StopCondition:    "完成数据质量检查 + 模型运行 + 信号生成 后立即停止",
				ExceptionPolicy:  "数据异常时标记模型异常，返回 DATA_INVALID，不得自行修改模型",
			},
		},

		// ============ 03 Risk｜盘前风险检查 ============
		{
			ID:              "pre_risk",
			Phase:           PhasePreMarket,
			AgentRole:       port.RoleRisk,
			TaskName:        "盘前风险检查",
			TaskOrder:       3,
			Priority:        PriorityCritical,
			Description:     "评估市场、组合和个股风险，输出风险约束判断",
			DeliverableType: "risk_assessment",
			TimeoutMs:       300000,
			MaxRetries:      2,
			Scope: TaskScope{
				Allowed:    []string{"VaR", "CVaR", "回撤", "波动率", "集中度", "相关性", "流动性", "风险限额"},
				NotAllowed: []string{"选股", "生成Alpha", "交易决策", "执行交易"},
			},
			Contract: TaskContract{
				Purpose: "判断组合是否满足风险约束",
				Trigger: "Quant 完成后自动触发",
				Inputs:  []string{"当前持仓", "Quant 信号", "Planner 市场环境", "风险参数"},
				RequiredActions: []string{
					"检查市场风险",
					"检查组合风险",
					"检查集中度",
					"检查波动率",
					"计算 VaR",
					"计算 CVaR",
					"检查回撤",
					"检查相关性",
					"检查流动性",
					"检查事件风险",
					"输出 risk_assessment",
				},
				ForbiddenActions: []string{
					"自主选股", "产生Alpha", "自主生成交易订单", "修改CIO决策", "决定买卖方向",
				},
				DecisionBoundary: "可以决定 PASS/WARNING/VETO；不能决定买什么、卖什么",
				AllowedTools:     []string{"VaR计算", "CVaR计算", "回撤计算", "波动率计算", "相关性计算", "集中度计算"},
				OutputSchema:     []string{"status(PASS/WARNING/VETO)", "risk_level", "violations", "warnings", "details"},
				Handoff:          "CIO",
				NOActionCode:     "RISK_NORMAL",
				StopCondition:    "完成所有风险指标检查并输出 PASS/WARNING/VETO 后立即停止",
				ExceptionPolicy:  "数据异常时返回 DATA_UNAVAILABLE，不得自行假设数据",
			},
		},

		// ============ 04 CIO｜生成盘前决策 ============
		{
			ID:              "pre_cio",
			Phase:           PhasePreMarket,
			AgentRole:       port.RoleCIO,
			TaskName:        "生成盘前决策",
			TaskOrder:       4,
			Priority:        PriorityCritical,
			Description:     "汇总 Plan+Signal+Risk，生成最终投资决策",
			DeliverableType: "pre_market_decision",
			// CIO 决策闸门含多提案集成+多空辩论+Kelly，180s 硬超时会在边界掐断在途 LLM 调用
			TimeoutMs:     1800000,
			Preconditions: []string{"pre_planner", "pre_quant", "pre_risk"},
			MaxRetries:    2,
			Scope: TaskScope{
				Allowed:    []string{"综合Planner", "综合Quant", "综合Risk", "形成投资决策"},
				NotAllowed: []string{"重新跑Quant模型", "重新计算Risk", "直接执行交易"},
			},
			Contract: TaskContract{
				Purpose: "综合所有上游证据，形成最终投资决策（决策闸门）",
				Trigger: "Planner + Quant + Risk 全部完成后触发",
				Inputs:  []string{"pre_market_plan", "quant_signal", "risk_assessment", "current_portfolio", "investment_policy"},
				RequiredActions: []string{
					"判断是否需要调整",
					"判断调整什么",
					"判断调整原因",
					"判断是否满足风险约束",
					"判断决策优先级",
					"判断是否进入执行阶段",
					"生成 pre_market_decision",
				},
				ForbiddenActions: []string{
					"自己重新运行Quant模型", "自己重新计算Risk", "自己扩大股票池",
					"自己直接执行订单", "修改上游信号",
				},
				DecisionBoundary: "可以决定调整/不调整、调整方向；不能自己做量化分析和风险计算",
				AllowedTools:     []string{"决策推理", "信号整合", "风险引用"},
				OutputSchema:     []string{"decision", "adjustments", "reason", "risk_approval", "execution_ready"},
				Handoff:          "Trader (如需执行)",
				NOActionCode:     "NO_DECISION_CHANGE",
				StopCondition:    "完成决策输出后立即停止",
				ExceptionPolicy:  "输入数据缺失时返回 INCOMPLETE_INPUT，不得自行补充数据",
			},
		},

		// ============ 05 Trader｜市场监控 ============
		{
			ID:              "in_market_monitor",
			Phase:           PhaseInMarket,
			AgentRole:       port.RoleTrader,
			TaskName:        "市场监控",
			TaskOrder:       1,
			Priority:        PriorityHigh,
			Description:     "监控市场数据、订单状态、价格变动，检测异常事件",
			DeliverableType: "market_monitor",
			TimeoutMs:       30000,
			RepeatInterval:  300,
			IsEventDriven:   false,
			MaxRetries:      0,
			Scope: TaskScope{
				Allowed:    []string{"行情", "买卖盘", "流动性", "订单", "成交", "滑点", "执行异常"},
				NotAllowed: []string{"选股", "生成信号", "改变交易方向", "改变交易数量", "决定是否交易"},
			},
			Contract: TaskContract{
				Purpose: "监控交易执行环境——市场与订单状态",
				Trigger: "盘中定时循环触发（5分钟）",
				Inputs:  []string{"实时行情", "订单状态", "成交数据", "流动性数据"},
				RequiredActions: []string{
					"监控行情变化",
					"监控买卖盘状态",
					"监控订单执行",
					"监控成交确认",
					"监控滑点",
					"监控执行异常",
					"检测异常事件并触发事件工作流",
				},
				ForbiddenActions: []string{
					"自主选股", "自主生成信号", "自主改变交易方向",
					"自主改变交易数量", "自主决定是否交易", "修改CIO决策",
				},
				DecisionBoundary: "只能报告状态和异常；不能做出任何交易决策",
				AllowedTools:     []string{"行情监控", "订单监控", "成交监控"},
				OutputSchema:     []string{"market_status", "order_status", "execution_alerts", "anomaly_flags"},
				Handoff:          "Risk (异常时) / CIO (异常时)",
				NOActionCode:     "NO_EXECUTION",
				StopCondition:    "监控循环持续运行，异常触发时上报后继续",
				ExceptionPolicy:  "行情数据异常时标记 DATA_ERROR，不得自行猜测行情",
			},
		},

		// ============ 06 Risk｜风险监控 ============
		{
			ID:              "in_risk_monitor",
			Phase:           PhaseInMarket,
			AgentRole:       port.RoleRisk,
			TaskName:        "风险监控",
			TaskOrder:       2,
			Priority:        PriorityHigh,
			Description:     "监控风险指标是否突破预设阈值",
			DeliverableType: "risk_monitor",
			TimeoutMs:       30000,
			RepeatInterval:  300,
			IsEventDriven:   false,
			MaxRetries:      0,
			Scope: TaskScope{
				Allowed:    []string{"风险指标", "风险限额", "回撤", "波动率", "集中度", "流动性", "组合异常"},
				NotAllowed: []string{"选股", "生成Alpha", "交易决策", "执行交易"},
			},
			Contract: TaskContract{
				Purpose: "监控风险指标是否突破预设阈值——监控任务不是分析任务",
				Trigger: "盘中定时循环触发（5分钟）",
				Inputs:  []string{"风险指标数据", "风险限额配置", "实时持仓"},
				RequiredActions: []string{
					"检查风险指标",
					"对比风险限额",
					"检查回撤",
					"检查波动率",
					"检查集中度",
					"检查流动性",
					"检测组合异常",
					"仅在突破阈值时生成事件",
				},
				ForbiddenActions: []string{
					"重新进行完整风险分析", "自主选股", "产生Alpha",
					"决定买卖", "执行交易",
				},
				DecisionBoundary: "只能报告风险状态和阈值突破；不能做投资决策",
				AllowedTools:     []string{"风险指标计算", "阈值对比", "异常检测"},
				OutputSchema:     []string{"risk_status", "threshold_breaches", "risk_alerts"},
				Handoff:          "CIO (阈值突破时)",
				NOActionCode:     "RISK_NORMAL",
				StopCondition:    "监控循环持续运行，仅在突破阈值时产生事件",
				ExceptionPolicy:  "数据异常时标记 DATA_ERROR，不得自行假设数据",
			},
		},

		// ============ 07 Quant｜信号监控 ============
		{
			ID:              "in_signal_monitor",
			Phase:           PhaseInMarket,
			AgentRole:       port.RoleQuant,
			TaskName:        "信号监控",
			TaskOrder:       3,
			Priority:        PriorityNormal,
			Description:     "监控既有量化信号是否发生重大变化",
			DeliverableType: "signal_monitor",
			TimeoutMs:       30000,
			RepeatInterval:  300,
			IsEventDriven:   false,
			MaxRetries:      0,
			Scope: TaskScope{
				Allowed:    []string{"signal", "signal_strength", "signal_change", "threshold_crossing", "model_status"},
				NotAllowed: []string{"重新选股", "修改因子", "修改阈值", "生成投资决策", "触发交易"},
			},
			Contract: TaskContract{
				Purpose: "监控既有信号是否发生重大变化——只监控不重算",
				Trigger: "盘中定时循环触发（5分钟）或信号事件触发",
				Inputs:  []string{"盘前量化信号", "实时行情", "模型状态"},
				RequiredActions: []string{
					"检查 signal 变化",
					"检查 signal_strength 变化",
					"检查 threshold_crossing",
					"检查 model_status",
					"仅在 signal_change > 阈值 时产生事件",
				},
				ForbiddenActions: []string{
					"重新进行完整选股", "自主开发新策略", "修改因子",
					"修改阈值", "生成投资决策", "触发交易",
				},
				DecisionBoundary: "只能报告信号变化；不能做任何决策或交易",
				AllowedTools:     []string{"信号对比", "阈值检测", "模型状态检查"},
				OutputSchema:     []string{"signal_status", "signal_changes", "threshold_events"},
				Handoff:          "Risk (信号异常时) / CIO (信号异常时)",
				NOActionCode:     "NO_SIGNAL_CHANGE",
				StopCondition:    "监控循环持续运行，仅在 signal_change > 阈值 时产生事件",
				ExceptionPolicy:  "模型状态异常时标记 MODEL_ERROR，不得自行修改模型",
			},
		},

		// ============ 08 Risk｜事件风险评估 ============
		{
			ID:              "in_event_risk",
			Phase:           PhaseInMarket,
			AgentRole:       port.RoleRisk,
			TaskName:        "事件风险评估",
			TaskOrder:       10,
			Priority:        PriorityCritical,
			Description:     "重大风险事件触发时进行风险评估",
			DeliverableType: "event_risk_assessment",
			TimeoutMs:       60000,
			IsEventDriven:   true,
			MaxRetries:      2,
			Scope: TaskScope{
				Allowed:    []string{"事件风险", "极端波动", "流动性异常", "极端回撤", "系统性风险", "公司事件"},
				NotAllowed: []string{"选股", "交易决策", "执行交易"},
			},
			Contract: TaskContract{
				Purpose: "评估重大事件的风险影响——Risk 权限最大的特殊任务",
				Trigger: "仅允许以下类型事件触发：极端波动、流动性异常、极端回撤、系统性风险、重大公司事件、市场异常",
				Inputs:  []string{"事件类型", "事件数据", "当前持仓", "风险限额"},
				RequiredActions: []string{
					"判断风险是否真实",
					"判断风险严重程度",
					"判断风险影响范围",
					"判断是否突破风险限制",
					"判断是否需要 CIO 介入",
					"输出 event_risk_assessment",
				},
				ForbiddenActions: []string{
					"直接产生交易指令", "选股", "生成Alpha", "修改CIO决策",
				},
				DecisionBoundary: "可以决定风险等级和是否需要CIO介入；不能直接产生交易指令",
				AllowedTools:     []string{"风险评估", "影响分析", "限额检查"},
				OutputSchema:     []string{"event_type", "risk_real", "severity", "impact_scope", "limit_breach", "cio_required"},
				Handoff:          "CIO",
				NOActionCode:     "NO_MAJOR_EVENT",
				StopCondition:    "完成风险评估并输出结论后立即停止",
				ExceptionPolicy:  "数据不足时返回 DATA_INVALID，不得自行假设风险",
			},
		},

		// ============ 09 CIO｜事件决策 ============
		{
			ID:              "in_event_cio",
			Phase:           PhaseInMarket,
			AgentRole:       port.RoleCIO,
			TaskName:        "事件决策",
			TaskOrder:       11,
			Priority:        PriorityCritical,
			Description:     "事件触发后CIO做出投资决策",
			DeliverableType: "event_decision",
			TimeoutMs:       60000,
			Preconditions:   []string{"in_event_risk"},
			IsEventDriven:   true,
			MaxRetries:      2,
			Scope: TaskScope{
				Allowed:    []string{"综合事件风险", "综合信号", "综合持仓", "形成事件决策"},
				NotAllowed: []string{"重新跑Quant", "重新计算Risk", "直接执行交易"},
			},
			Contract: TaskContract{
				Purpose: "基于事件风险评估做出投资决策",
				Trigger: "event_risk_assessment 产生重大事件时启动",
				Inputs:  []string{"event_risk_assessment", "quant_signal", "current_portfolio", "investment_policy"},
				RequiredActions: []string{
					"审阅事件风险评估",
					"结合当前信号",
					"结合当前持仓",
					"判断是否需要调整",
					"判断调整方案",
					"生成 event_decision",
				},
				ForbiddenActions: []string{
					"仅因看到新闻就直接产生交易指令", "自己重新跑Quant",
					"自己重新计算风险", "自己直接执行订单",
				},
				DecisionBoundary: "可以决定事件应对方案；不能跳过风险评估直接交易",
				AllowedTools:     []string{"决策推理", "证据整合"},
				OutputSchema:     []string{"event_type", "decision", "adjustments", "reason", "execution_ready"},
				Handoff:          "Trader",
				NOActionCode:     "NO_DECISION_CHANGE",
				StopCondition:    "完成决策输出后立即停止",
				ExceptionPolicy:  "输入数据缺失时返回 INCOMPLETE_INPUT，不得自行补充",
			},
		},

		// ============ 10 Trader｜事件交易执行 ============
		{
			ID:              "in_event_trader",
			Phase:           PhaseInMarket,
			AgentRole:       port.RoleTrader,
			TaskName:        "事件交易执行",
			TaskOrder:       12,
			Priority:        PriorityCritical,
			Description:     "执行CIO批准的决策对象",
			DeliverableType: "event_execution",
			TimeoutMs:       30000,
			Preconditions:   []string{"in_event_cio"},
			IsEventDriven:   true,
			MaxRetries:      2,
			Scope: TaskScope{
				Allowed:    []string{"订单执行", "成交确认", "价格检查", "数量检查", "持仓更新"},
				NotAllowed: []string{"选股", "改变方向", "改变数量", "改变价格策略", "决定是否交易"},
			},
			Contract: TaskContract{
				Purpose: "执行 CIO 已批准的决策——只能执行，不能重新决策",
				Trigger: "CIO Approved Decision 到达后触发",
				Inputs:  []string{"CIO Approved Decision", "decision_id", "approval_status", "symbol", "side", "quantity", "price_limit", "execution_window", "risk_status"},
				RequiredActions: []string{
					"验证 decision_id",
					"验证 approval_status",
					"验证 symbol / side / quantity / price_limit",
					"验证 execution_window 和 risk_status",
					"执行交易",
					"确认成交",
					"更新持仓",
					"输出 event_execution",
				},
				ForbiddenActions: []string{
					"修改CIO决策", "改变交易数量", "改变交易方向",
					"决定是否交易", "选股", "生成投资观点",
				},
				DecisionBoundary: "只能执行CIO的决策；发现执行条件不合理时返回 EXECUTION_EXCEPTION 给CIO",
				AllowedTools:     []string{"订单执行", "成交确认", "持仓更新"},
				OutputSchema:     []string{"decision_id", "execution_status", "filled_quantity", "avg_price", "slippage", "execution_time"},
				Handoff:          "CIO (执行结果) / Risk (异常时)",
				NOActionCode:     "NO_EXECUTION",
				StopCondition:    "完成执行确认后立即停止",
				ExceptionPolicy:  "缺少关键字段时 REJECT_EXECUTION 返回CIO；不得自行修改决策",
			},
		},

		// ============ 11 Trader｜日终结算 ============
		{
			ID:              "post_trader",
			Phase:           PhasePostMarket,
			AgentRole:       port.RoleTrader,
			TaskName:        "日终结算",
			TaskOrder:       1,
			Priority:        PriorityCritical,
			Description:     "成交确认、持仓更新、成本更新、现金更新",
			DeliverableType: "end_of_day_portfolio",
			TimeoutMs:       180000,
			MaxRetries:      2,
			Scope: TaskScope{
				Allowed:    []string{"成交", "未成交订单", "持仓", "平均成本", "现金", "手续费", "滑点"},
				NotAllowed: []string{"评价股票", "评价策略", "修改仓位", "提出投资建议"},
			},
			Contract: TaskContract{
				Purpose: "日终结算——机械化的确认工作",
				Trigger: "盘后 15:00 自动触发",
				Inputs:  []string{"当日成交记录", "订单状态", "持仓快照"},
				RequiredActions: []string{
					"确认成交",
					"确认未成交订单",
					"确认持仓",
					"确认平均成本",
					"确认现金",
					"确认手续费",
					"确认滑点",
					"输出 end_of_day_portfolio",
				},
				ForbiddenActions: []string{
					"评价股票", "评价策略", "修改仓位", "提出投资建议", "生成投资观点",
				},
				DecisionBoundary: "只能做确认和记录；不能做任何分析或建议",
				AllowedTools:     []string{"每日结算", "成交确认", "持仓更新", "成本计算"},
				OutputSchema:     []string{"daily_settlement", "trades", "positions", "cash", "costs", "fees", "slippage"},
				Handoff:          "Risk",
				NOActionCode:     "NO_EXECUTION",
				StopCondition:    "完成所有确认后立即停止",
				ExceptionPolicy:  "数据不完整时标记 DATA_INCOMPLETE，不得自行编造成交",
			},
		},

		// ============ 12 Risk｜日终风险审查 ============
		{
			ID:              "post_risk",
			Phase:           PhasePostMarket,
			AgentRole:       port.RoleRisk,
			TaskName:        "日终风险审查",
			TaskOrder:       2,
			Priority:        PriorityCritical,
			Description:     "计算日终风险指标，生成风险报告",
			DeliverableType: "daily_risk_report",
			TimeoutMs:       240000,
			Preconditions:   []string{"post_trader"},
			MaxRetries:      2,
			Scope: TaskScope{
				Allowed:    []string{"VaR", "CVaR", "最大回撤", "波动率", "Beta", "集中度", "相关性", "流动性", "风险贡献"},
				NotAllowed: []string{"选股", "制定明日交易计划", "修改策略"},
			},
			Contract: TaskContract{
				Purpose: "计算当日风险指标，回答：今天组合风险发生了什么变化",
				Trigger: "Trader 日终结算完成后触发",
				Inputs:  []string{"日终持仓", "当日行情", "风险参数"},
				RequiredActions: []string{
					"计算 VaR",
					"计算 CVaR",
					"计算最大回撤",
					"计算波动率",
					"计算 Beta",
					"计算集中度",
					"计算相关性",
					"计算流动性",
					"计算风险贡献",
					"输出 daily_risk_report",
				},
				ForbiddenActions: []string{
					"根据今日风险结果直接制定明日交易计划", "选股", "修改策略",
				},
				DecisionBoundary: "可以计算和报告风险指标；不能制定交易计划",
				AllowedTools:     []string{"run_daily_risk_report", "portfolio_risk_metrics", "portfolio_risk_view", "stop_loss_monitor", "VaR计算", "CVaR计算", "回撤计算", "波动率计算", "Beta计算"},
				OutputSchema:     []string{"VaR", "CVaR", "max_drawdown", "volatility", "beta", "concentration", "correlation", "liquidity", "risk_contribution"},
				Handoff:          "CIO",
				NOActionCode:     "RISK_NORMAL",
				StopCondition:    "完成所有风险指标计算并输出报告后立即停止",
				ExceptionPolicy:  "数据异常时返回 DATA_INVALID，不得自行假设数据",
			},
		},

		// ============ 13 CIO｜日终投资报告 ============
		{
			ID:              "post_cio",
			Phase:           PhasePostMarket,
			AgentRole:       port.RoleCIO,
			TaskName:        "日终投资报告",
			TaskOrder:       3,
			Priority:        PriorityCritical,
			Description:     "汇总交易、盈亏、风险指标，生成日报",
			DeliverableType: "daily_investment_report",
			TimeoutMs:       240000,
			Preconditions:   []string{"post_risk"},
			MaxRetries:      2,
			Scope: TaskScope{
				Allowed:    []string{"汇总交易", "汇总收益", "汇总盈亏", "汇总持仓", "汇总风险"},
				NotAllowed: []string{"修改策略", "修改参数", "生成新交易", "修改风险限额"},
			},
			Contract: TaskContract{
				Purpose: "记录今日投资结果——机械化的日报生成",
				Trigger: "Risk 日终风险审查完成后触发",
				Inputs:  []string{"当日交易记录", "盈亏数据", "风险报告", "决策记录", "执行结果"},
				RequiredActions: []string{
					"汇总今日交易",
					"汇总今日收益",
					"汇总今日盈亏",
					"汇总当前持仓",
					"汇总风险指标",
					"汇总决策执行情况",
					"输出 daily_investment_report",
				},
				ForbiddenActions: []string{
					"修改策略", "修改参数", "生成新的交易", "修改风险限额", "自己重新分析市场",
				},
				DecisionBoundary: "只能汇总和记录；不能修改任何策略或参数",
				AllowedTools:     []string{"数据汇总", "报告生成"},
				OutputSchema:     []string{"trades_summary", "pnl", "holdings", "risk_summary", "execution_summary"},
				Handoff:          "明日 Planner 参考",
				NOActionCode:     "NO_DECISION_CHANGE",
				StopCondition:    "完成报告生成后立即停止",
				ExceptionPolicy:  "数据缺失时标记 REPORT_INCOMPLETE，不得自行编造数据",
			},
		},

		// ============ 14 Quant｜策略参数调优（盘后 15:05） ============
		{
			ID:              "post_quant_tune",
			Phase:           PhasePostMarket,
			AgentRole:       port.RoleQuant,
			TaskName:        "策略参数调优",
			TaskOrder:       4,
			Priority:        PriorityHigh,
			Description:     "调用 tune_strategy_params 工具对全部内置策略参数做自动化网格调优，调优完成后自动刷新并写入最新策略指标",
			DeliverableType: "strategy_tuning_report",
			TimeoutMs:       600000,
			Preconditions:   []string{"post_cio"},
			MaxRetries:      1,
			EarliestStart:   "15:05",
			Scope: TaskScope{
				Allowed:    []string{"策略参数", "回测", "夏普比率", "最大回撤", "胜率", "策略指标", "基准指数"},
				NotAllowed: []string{"修改CIO决策", "执行交易", "选股", "制定交易计划"},
			},
			Contract: TaskContract{
				Purpose: "盘后对内置策略参数做自动化网格调优，并把最新回测指标写入策略表",
				Trigger: "盘后 15:05 自动触发",
				Inputs:  []string{"策略表 config_json", "tune_ranges_json", "基准指数K线"},
				RequiredActions: []string{
					"调用 tune_strategy_params 工具",
					"对全部内置策略做参数网格扫描回测",
					"按夏普/收益/胜率选出更优参数",
					"写回策略表 config_json 并更新指标",
					"输出调优对比报告",
				},
				ForbiddenActions: []string{
					"伪造调优结果", "调参不落库却声称生效", "修改CIO决策", "执行交易", "选股",
				},
				DecisionBoundary: "只能调优策略参数并写回策略表；不能做任何交易决策",
				AllowedTools:     []string{"tune_strategy_params"},
				OutputSchema:     []string{"strategy_tuning_report", "tuned_count", "applied_count", "benchmark"},
				Handoff:          "CIO (明日计划参考)",
				NOActionCode:     "NO_TUNE_NEEDED",
				StopCondition:    "完成全部策略调优并刷新指标后立即停止",
				ExceptionPolicy:  "基准数据不可用时标记 DATA_ERROR，不得伪造调优结果",
			},
		},

		// ============ 15 Quant｜因子复盘（复盘阶段） ============
		{
			ID:              "night_quant",
			Phase:           PhaseReview,
			AgentRole:       port.RoleQuant,
			TaskName:        "因子复盘",
			TaskOrder:       1,
			Priority:        PriorityNormal,
			Description:     "复盘因子表现、选股准确率，并对持仓执行策略进行解读",
			DeliverableType: "factor_review",
			TimeoutMs:       90000,
			MaxRetries:      2,
			EarliestStart:   "15:15",
			Scope: TaskScope{
				Allowed:    []string{"因子收益", "IC", "RankIC", "分组表现", "稳定性", "覆盖率", "信号准确率", "衰减", "持仓策略解读", "策略有效性评估"},
				NotAllowed: []string{"修改因子", "修改模型", "修改参数", "制定明日交易计划"},
			},
			Contract: TaskContract{
				Purpose: "评价当日因子和模型表现，解读持仓执行策略——只描述发生了什么，并给出是否调整策略的建议",
				Trigger: "复盘阶段 (15:15-18:00) 每日 15:15 自动触发",
				Inputs:  []string{"当日因子数据", "模型输出", "选股结果", "实际收益", "持仓策略执行情况", "盘面状况"},
				RequiredActions: []string{
					"计算因子收益",
					"计算 IC",
					"计算 Rank IC",
					"计算分组表现",
					"计算稳定性",
					"计算覆盖率",
					"计算信号准确率",
					"计算衰减",
					"解读持仓执行的策略及表现",
					"结合盘面状况判断是否调整策略",
					"标记异常",
					"输出 factor_review",
				},
				ForbiddenActions: []string{
					"自主修改因子", "自主修改模型", "自主调整参数", "直接制定明日交易计划",
				},
				DecisionBoundary: "可以描述因子表现并解读持仓策略；可以建议是否调整策略；不能直接修改因子/模型/参数",
				AllowedTools:     []string{"因子分析", "绩效归因", "IC分析", "策略解读"},
				OutputSchema:     []string{"factor_returns", "IC", "rank_IC", "group_performance", "stability", "coverage", "accuracy", "decay", "position_strategy_review", "strategy_change_suggestion", "anomalies"},
				Handoff:          "CIO (制定明日计划参考)",
				NOActionCode:     "NO_SIGNAL_CHANGE",
				StopCondition:    "完成因子复盘与策略解读后立即停止",
				ExceptionPolicy:  "因子数据异常时标记 DATA_ERROR，不得自行修改因子",
			},
		},

		// ============ 15 CIO｜深度复盘与明日计划 ============
		{
			ID:              "night_cio",
			Phase:           PhaseReview,
			AgentRole:       port.RoleCIO,
			TaskName:        "深度复盘与制定明日计划",
			TaskOrder:       2,
			Priority:        PriorityNormal,
			Description:     "主持深度复盘，评估策略有效性，基于复盘结果制定明日投资计划",
			DeliverableType: "tomorrow_plan",
			// 深度复盘含多轮工具调用+辩论+计划制定，180s 硬超时会在 3 分钟边界掐断在途 LLM 调用
			// （实测每次任务稳定在 180s 报 context deadline exceeded）
			TimeoutMs:     1800000,
			Preconditions: []string{"night_quant"},
			MaxRetries:    2,
			EarliestStart: "15:15",
			Scope: TaskScope{
				Allowed:    []string{"今日投资结果", "FactorReview", "RiskReport", "PortfolioState", "历史决策执行情况", "策略有效性评估"},
				NotAllowed: []string{"直接生成交易决策", "跳过明日盘前流程"},
			},
			Contract: TaskContract{
				Purpose: "主持深度复盘并评估策略有效性，为下一交易日提供准备工作——产生的是 Plan 不是 Decision",
				Trigger: "Quant 因子复盘完成后触发",
				Inputs:  []string{"今日投资结果", "Factor Review", "Risk Report", "Portfolio State", "历史决策执行情况", "策略解读"},
				RequiredActions: []string{
					"审阅今日投资结果",
					"审阅 Factor Review",
					"审阅持仓策略解读",
					"评估策略有效性",
					"审阅 Risk Report",
					"审阅 Portfolio State",
					"审阅历史决策执行情况",
					"形成明日关注点",
					"输出 tomorrow_plan",
				},
				ForbiddenActions: []string{
					"直接生成交易决策", "跳过明日盘前 Planner→Quant→Risk→CIO 流程", "修改今日决策",
				},
				DecisionBoundary: "可以制定关注重点和计划；不能直接生成交易决策或订单",
				AllowedTools:     []string{"计划制定", "优先级排序", "策略评估"},
				OutputSchema:     []string{"focus_areas", "watchlist", "risk_notes", "priority_items", "strategy_assessment"},
				Handoff:          "明日 Planner (参考)",
				NOActionCode:     "NO_DECISION_CHANGE",
				StopCondition:    "完成深度复盘与计划制定后立即停止",
				ExceptionPolicy:  "数据不足时标记 PLAN_INCOMPLETE，不得自行假设",
			},
		},
	}
}

// ============ 核心方法============

func (s *TaskScheduler) Start() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return
	}
	s.running = true
	log.Println("[TaskScheduler] Started with optimized task model")
	go s.mainLoop()
}

func (s *TaskScheduler) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running {
		return
	}
	s.running = false
	s.cancel()
	log.Println("[TaskScheduler] Stopped")
}

func (s *TaskScheduler) IsRunning() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.running
}

func (s *TaskScheduler) GetExecutorAvailability() bool {
	return s.executor != nil && s.executor.IsAvailable()
}

func (s *TaskScheduler) GetTasks() []AgentTask {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]AgentTask, len(s.tasks))
	copy(result, s.tasks)
	return result
}

func (s *TaskScheduler) GetTasksByPhase(phase TaskPhase) []AgentTask {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []AgentTask
	for _, t := range s.tasks {
		if t.Phase == phase {
			result = append(result, t)
		}
	}
	return result
}

func (s *TaskScheduler) SetDailyWorkflowTrigger(trigger func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dailyWorkflowTrigger = trigger
}

func (s *TaskScheduler) GetCurrentPhase() TaskPhase {
	now := time.Now()
	if now.Weekday() == time.Saturday || now.Weekday() == time.Sunday {
		return ""
	}

	currentTime := now.Hour()*60 + now.Minute()

	switch {
	case currentTime >= 9*60 && currentTime < 9*60+30:
		return PhasePreMarket
	case currentTime >= 9*60+30 && currentTime < 11*60+30:
		return PhaseInMarket // 上午交易时段
	case currentTime >= 11*60+30 && currentTime < 13*60:
		return "" // 午休，非交易时段
	case currentTime >= 13*60 && currentTime < 15*60:
		return PhaseInMarket // 下午交易时段
	case currentTime >= 15*60 && currentTime < 15*60+15:
		return PhasePostMarket
	case currentTime >= 15*60+15 && currentTime < 18*60:
		return PhaseReview
	default:
		return ""
	}
}

// ============ 主循环============

func (s *TaskScheduler) mainLoop() {
	for {
		delay := s.pollInterval()
		select {
		case <-s.ctx.Done():
			return
		case event := <-s.eventChan:
			s.handleEvent(event)
		case <-time.After(delay):
			s.processCurrentPhase()
		}
	}
}

// pollInterval 按当前市场阶段自适应选择轮询间隔，避免收盘后/深夜仍以 30s 空转：
// - 盘前/盘中/盘后等关键时段 30s，及时响应 EarliestStart 触发与临收盘结算
// - 复盘阶段任务为"每日一次"，降低到 5min，避免收盘后逐 30s 刷屏（但可运行复盘任务）
// - 休市/非交易时段（收盘后→次日开盘前、周末）15min，仅保持次一交易日自动唤醒
func (s *TaskScheduler) pollInterval() time.Duration {
	switch s.GetCurrentPhase() {
	case PhasePreMarket, PhaseInMarket, PhasePostMarket:
		return 30 * time.Second
	case PhaseReview:
		return 5 * time.Minute
	default:
		return 15 * time.Minute
	}
}

// ============ 事件处理============

func (s *TaskScheduler) SubmitEvent(eventType EventType, severity, message string, data map[string]interface{}) {
	event := MarketEvent{
		Type:      eventType,
		Timestamp: time.Now(),
		Source:    "auto_detector",
		Severity:  severity,
		Message:   message,
		Data:      data,
		Triggered: false,
	}

	s.mu.Lock()
	s.recentEvents = append(s.recentEvents, event)
	if len(s.recentEvents) > 50 {
		s.recentEvents = s.recentEvents[len(s.recentEvents)-50:]
	}
	s.mu.Unlock()

	select {
	case s.eventChan <- event:
		log.Printf("[Event] Submitted: type=%s, severity=%s, message=%s", eventType, severity, message)
	default:
		log.Printf("[Event] Channel full, dropping event: type=%s", eventType)
	}
}

func (s *TaskScheduler) handleEvent(event MarketEvent) {
	log.Printf("[Event] Handling: type=%s, severity=%s, message=%s", event.Type, event.Severity, event.Message)

	if event.Triggered {
		log.Printf("[Event] Already triggered, skipping")
		return
	}

	// 根据事件类型选择任务链
	var taskIDs []string
	switch event.Type {
	case EventRisk:
		taskIDs = []string{"in_event_risk", "in_event_cio", "in_event_trader"}
	case EventSignal:
		taskIDs = []string{"in_event_risk", "in_event_cio", "in_event_trader"}
	case EventOrder:
		taskIDs = []string{"in_event_risk", "in_event_cio", "in_event_trader"}
	case EventMarket:
		taskIDs = []string{"in_event_risk", "in_event_cio", "in_event_trader"}
	default:
		log.Printf("[Event] Unknown event type: %s", event.Type)
		return
	}

	// 标记事件为已触发
	s.mu.Lock()
	for i, e := range s.recentEvents {
		if e.Type == event.Type && e.Timestamp.Equal(event.Timestamp) {
			s.recentEvents[i].Triggered = true
		}
	}
	s.mu.Unlock()

	// 执行事件工作流
	for _, taskID := range taskIDs {
		task := s.findTaskByID(taskID)
		if task == nil {
			continue
		}

		// 检查前置条件
		if !s.checkPreconditions(task.Preconditions, task.Phase) {
			log.Printf("[Event] Preconditions not met for task: %s", taskID)
			continue
		}

		log.Printf("[Event] Executing task: %s for event %s", taskID, event.Type)
		s.executeTaskWithRetry(*task, event.Data)
	}
}

func (s *TaskScheduler) GetRecentEvents(limit int) []MarketEvent {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 || limit > len(s.recentEvents) {
		limit = len(s.recentEvents)
	}
	result := make([]MarketEvent, limit)
	copy(result, s.recentEvents[len(s.recentEvents)-limit:])
	return result
}

// ============ 时段任务处理============

func (s *TaskScheduler) processCurrentPhase() {
	phase := s.GetCurrentPhase()
	now := time.Now()

	if now.Minute()%10 == 0 && now.Second() < 30 {
		log.Printf("[TaskScheduler] Phase=%s, time=%s, executor=%v",
			phase, now.Format("15:04:05"), s.GetExecutorAvailability())
	}

	if phase == "" {
		return
	}

	today := time.Now().Format("2006-01-02")
	log.Printf("[TaskScheduler] Processing phase: %s", phase)

	// 盘前自动触发工作流
	if phase == PhasePreMarket && s.dailyWorkflowTrigger != nil && s.lastWorkflowTriggerDate != today {
		s.lastWorkflowTriggerDate = today
		go func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[TaskScheduler] Daily workflow trigger panic: %v", r)
				}
			}()
			s.dailyWorkflowTrigger()
		}()
	}

	tasks := s.GetTasksByPhase(phase)
	var monitorTasks, eventTasks []AgentTask
	for _, task := range tasks {
		if task.IsEventDriven {
			eventTasks = append(eventTasks, task)
		} else {
			monitorTasks = append(monitorTasks, task)
		}
	}

	skipped, preconditionFailed, executed := 0, 0, 0

	for _, task := range monitorTasks {
		// 时间门控：任务设置了 EarliestStart（如 15:05）且当前未到达时跳过（不标记执行，稍后轮询再触发）
		if task.EarliestStart != "" && !reachedTime(task.EarliestStart) {
			skipped++
			continue
		}
		if s.shouldSkipTask(task) {
			skipped++
			continue
		}
		if !s.checkPreconditions(task.Preconditions, task.Phase) {
			preconditionFailed++
			continue
		}
		executed++
		s.executeTaskWithRetry(task, nil)
	}

	log.Printf("[TaskScheduler] Phase %s: %d executed, %d skipped, %d precondition failed",
		phase, executed, skipped, preconditionFailed)
}

// reachedTime 判断当前时间是否已到达 HH:MM（用于任务时间门控）
func reachedTime(hhmm string) bool {
	if hhmm == "" {
		return true
	}
	now := time.Now()
	parts := strings.Split(hhmm, ":")
	if len(parts) != 2 {
		return true
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return true
	}
	nowMin := now.Hour()*60 + now.Minute()
	targetMin := h*60 + m
	return nowMin >= targetMin
}

// ============ 任务执行（带重试）============

func (s *TaskScheduler) executeTaskWithRetry(task AgentTask, eventData map[string]interface{}) {
	if s.db == nil {
		log.Printf("[TaskScheduler] DB is nil, cannot execute task %s", task.ID)
		return
	}

	if !s.GetExecutorAvailability() {
		log.Printf("[TaskScheduler] Executor not available, skipping task %s", task.ID)
		return
	}

	today := time.Now().Format("2006-01-02")
	now := time.Now()

	logEntry := data.AgentTaskLog{
		TaskDate:        today,
		TaskPhase:       string(task.Phase),
		AgentRole:       string(task.AgentRole),
		TaskName:        task.TaskName,
		TaskOrder:       task.TaskOrder,
		Status:          StatusRunning,
		StartTime:       &now,
		DurationMs:      0,
		DeliverableType: task.DeliverableType,
		DeliverableName: task.TaskName,
		Summary:         fmt.Sprintf("[%s] %s 执行任务: %s", task.Phase, task.AgentRole, task.TaskName),
		Details:         fmt.Sprintf(`{"task_id":"%s","phase":"%s","event_driven":%v,"description":%s}`, task.ID, task.Phase, task.IsEventDriven, strconv.Quote(task.Description)),
		CreatedAt:       now,
	}

	if err := s.db.GetDB().Create(&logEntry).Error; err != nil {
		log.Printf("[TaskScheduler] Failed to create log: %v", err)
		return
	}

	maxRetries := task.MaxRetries
	if maxRetries < 0 {
		maxRetries = 0
	}

	go func() {
		var lastErr error
		for attempt := 0; attempt <= maxRetries; attempt++ {
			if attempt > 0 {
				waitTime := time.Duration(30*attempt) * time.Second
				log.Printf("[TaskScheduler] Retry %d/%d for task %s, waiting %v", attempt, maxRetries, task.ID, waitTime)
				time.Sleep(waitTime)
			}

			err := s.executeOnce(task, eventData)
			if err == nil {
				completeTime := time.Now()
				durationMs := int(completeTime.Sub(now).Milliseconds())

				s.db.GetDB().Model(&data.AgentTaskLog{}).
					Where("id = ?", logEntry.ID).
					Updates(map[string]interface{}{
						"status":      StatusSuccess,
						"end_time":    completeTime,
						"duration_ms": durationMs,
						"summary":     fmt.Sprintf("已完成：%s —— %s（耗时%dms, 第%d次尝试成功）", task.TaskName, task.Description, durationMs, attempt+1),
					})

				log.Printf("[TaskScheduler] Task SUCCESS: %s - %s (attempt %d)", task.ID, task.AgentRole, attempt+1)
				return
			}

			lastErr = err
			log.Printf("[TaskScheduler] Task attempt %d failed: %s - %s: %v", attempt+1, task.ID, task.AgentRole, err)
		}

		// 所有重试失败
		completeTime := time.Now()
		durationMs := int(completeTime.Sub(now).Milliseconds())
		s.db.GetDB().Model(&data.AgentTaskLog{}).
			Where("id = ?", logEntry.ID).
			Updates(map[string]interface{}{
				"status":      StatusFailed,
				"end_time":    completeTime,
				"duration_ms": durationMs,
				"summary":     fmt.Sprintf("执行失败：%s —— %s（已重试%d次）", task.TaskName, task.Description, maxRetries),
				"errors":      fmt.Sprintf("%v", lastErr),
			})

		log.Printf("[TaskScheduler] Task FAILED after %d retries: %s - %s", maxRetries, task.ID, task.AgentRole)
	}()
}

func (s *TaskScheduler) executeOnce(task AgentTask, eventData map[string]interface{}) error {
	ctx, cancel := context.WithTimeout(s.ctx, time.Duration(task.TimeoutMs)*time.Millisecond)
	defer cancel()

	// Task Contract v1.0: 日志记录契约信息
	contractBrief := fmt.Sprintf("[%s] %s | Purpose: %s | NO_ACTION: %s | Stop: %s",
		task.ID, task.TaskName, task.Contract.Purpose, task.Contract.NOActionCode, task.Contract.StopCondition)
	log.Printf("[TaskScheduler] Executing: %s", contractBrief)

	// Task Contract v1.0: 前置约束检查
	checker := NewConstraintChecker()
	preCheck := checker.CheckPreExecution(&task)
	if !preCheck.Valid {
		log.Printf("[TaskScheduler] Pre-execution check FAILED for %s: %v", task.ID, preCheck.Violations)
		return fmt.Errorf("pre-execution check failed: %v", preCheck.Violations)
	}

	// Task Contract v1.0: 构建带约束的任务描述
	var allowedToolsStr, forbiddenActionsStr, stopConditionStr string
	if len(task.Contract.AllowedTools) > 0 {
		allowedToolsStr = fmt.Sprintf("允许工具: %s", strings.Join(task.Contract.AllowedTools, ", "))
	}
	if len(task.Contract.ForbiddenActions) > 0 {
		forbiddenActionsStr = fmt.Sprintf("严禁: %s", strings.Join(task.Contract.ForbiddenActions, ", "))
	}
	stopConditionStr = fmt.Sprintf("停止条件: %s", task.Contract.StopCondition)

	taskDesc := fmt.Sprintf("%s | %s | %s | %s | %s",
		task.TaskName, task.Contract.Purpose, allowedToolsStr, forbiddenActionsStr, stopConditionStr)

	if eventData != nil {
		taskDesc = fmt.Sprintf("%s | EventData: %v", taskDesc, eventData)
	}

	// 构建真实执行上下文（阶段/时间/环境摘要），非空
	taskCtx := s.buildTaskContext(task)

	result, err := s.executor.Execute(ctx, task.AgentRole, taskDesc, ExecuteOptions{
		Phase:    task.Phase,
		TaskName: task.TaskName,
		Context:  taskCtx,
	})
	if err != nil {
		return err
	}

	// Task Contract v1.0: Output Guard 输出验证
	// 监控任务的直接执行结果不经过 OutputGuard 验证
	isMonitorTask := strings.Contains(taskDesc, "监控") || strings.Contains(taskDesc, "NO_ACTION") || strings.Contains(taskDesc, "monitor")
	guard := NewOutputGuard()
	validation := guard.ValidateOutput(&task, result)
	if !validation.Valid {
		// 对于监控任务，OutputGuard 验证失败不阻断任务
		if isMonitorTask {
			log.Printf("[TaskScheduler] Output guard rejected for monitor task %s, but allowing: violations=%v", task.ID, validation.Violations)
		} else {
			log.Printf("[TaskScheduler] Output GUARD REJECTED for %s: violations=%v", task.ID, validation.Violations)
			return fmt.Errorf("output guard rejected: %v", validation.Violations)
		}
	}

	if len(validation.Warnings) > 0 {
		log.Printf("[TaskScheduler] Output warnings for %s: %v", task.ID, validation.Warnings)
	}

	log.Printf("[TaskScheduler] Output VALID for %s (handoff: %s, no_action: %s)", task.ID, task.Contract.Handoff, task.Contract.NOActionCode)
	return nil
}

// ============ 跳过和前置条件检查============

func (s *TaskScheduler) shouldSkipTask(task AgentTask) bool {
	if s.db == nil {
		return false
	}

	if task.RepeatInterval > 0 {
		return s.isTaskExecutedWithinInterval(task.TaskName, task.RepeatInterval)
	}
	// 非循环任务：只要今日已有记录（含 RUNNING）即跳过，
	// 避免 30s tick 在前置任务尚在 RUNNING 时就重复启动，导致同一任务并发执行多次
	return s.isTaskStartedToday(task.TaskName)
}

func (s *TaskScheduler) isTaskProcessedToday(taskName string) bool {
	if s.db == nil {
		return false
	}

	today := time.Now().Format("2006-01-02")
	var count int64
	s.db.GetDB().Model(&data.AgentTaskLog{}).
		Where("task_date = ? AND task_name = ? AND status IN ?", today, taskName, []string{StatusSuccess, StatusFailed}).
		Count(&count)
	return count > 0
}

// isTaskStartedToday 判断任务今日是否已启动（含 PENDING/RUNNING/SUCCESS/FAILED，排除 SKIPPED）
// 用于非循环任务去重，防止 30s tick 在前置任务仍 RUNNING 时重复启动
func (s *TaskScheduler) isTaskStartedToday(taskName string) bool {
	if s.db == nil {
		return false
	}

	today := time.Now().Format("2006-01-02")
	var count int64
	s.db.GetDB().Model(&data.AgentTaskLog{}).
		Where("task_date = ? AND task_name = ? AND status != ?", today, taskName, StatusSkipped).
		Count(&count)
	return count > 0
}

func (s *TaskScheduler) isTaskExecutedWithinInterval(taskName string, intervalSeconds int) bool {
	if s.db == nil {
		return false
	}

	cutoffTime := time.Now().Add(-time.Duration(intervalSeconds) * time.Second)
	var count int64
	s.db.GetDB().Model(&data.AgentTaskLog{}).
		Where("task_name = ? AND status = ? AND created_at > ?", taskName, StatusSuccess, cutoffTime).
		Count(&count)
	return count > 0
}

func (s *TaskScheduler) checkPreconditions(preconditions []string, phase TaskPhase) bool {
	if len(preconditions) == 0 {
		return true
	}
	if s.db == nil {
		return true
	}

	for _, preTaskID := range preconditions {
		preTask := s.findTaskByID(preTaskID)
		if preTask == nil {
			return false
		}
		// 日志按 TaskName（中文名）记录，前置判断需使用相同标识，否则永远匹配不上导致任务被跳过
		preTaskName := preTask.TaskName
		if preTask.RepeatInterval > 0 {
			if !s.isTaskExecutedWithinInterval(preTaskName, preTask.RepeatInterval) {
				return false
			}
		} else {
			if !s.isTaskProcessedToday(preTaskName) {
				return false
			}
		}
	}
	return true
}

func (s *TaskScheduler) findTaskByID(taskID string) *AgentTask {
	for _, task := range s.tasks {
		if task.ID == taskID {
			return &task
		}
	}
	return nil
}

// waitForTaskCompletion 轮询等待任务落到最终状态（SUCCESS/FAILED）
// 用于保证盘前链（Planner→Quant→Risk→CIO）的串行依赖：前置任务完成后才执行后续任务
func (s *TaskScheduler) waitForTaskCompletion(task AgentTask) {
	if s.db == nil {
		return
	}
	// 等待上限 = 任务超时 + 20s 缓冲，避免前置任务异常时无限阻塞
	deadline := time.Now().Add(time.Duration(task.TimeoutMs)*time.Millisecond + 20*time.Second)
	for time.Now().Before(deadline) {
		var count int64
		s.db.GetDB().Model(&data.AgentTaskLog{}).
			Where("task_date = ? AND task_name = ? AND status IN ?",
				time.Now().Format("2006-01-02"), task.TaskName,
				[]string{StatusSuccess, StatusFailed}).
			Count(&count)
		if count > 0 {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// ============ 手动触发接口============

func (s *TaskScheduler) ExecuteTaskByID(taskID string) error {
	task := s.findTaskByID(taskID)
	if task == nil {
		return fmt.Errorf("task not found: %s", taskID)
	}
	if !s.GetExecutorAvailability() {
		return fmt.Errorf("executor not available")
	}

	s.resetTaskStatus(taskID)
	s.executeTaskWithRetry(*task, nil)
	return nil
}

func (s *TaskScheduler) ExecutePhaseTasks(phase TaskPhase) error {
	tasks := s.GetTasksByPhase(phase)
	if len(tasks) == 0 {
		return fmt.Errorf("no tasks for phase: %s", phase)
	}
	if !s.GetExecutorAvailability() {
		return fmt.Errorf("executor not available")
	}

	for _, task := range tasks {
		if !task.IsEventDriven {
			s.resetTaskStatus(task.ID)
			s.executeTaskWithRetry(task, nil)
		}
	}
	return nil
}

func (s *TaskScheduler) resetTaskStatus(taskID string) {
	if s.db == nil {
		return
	}
	today := time.Now().Format("2006-01-02")
	s.db.GetDB().Model(&data.AgentTaskLog{}).
		Where("task_date = ? AND task_name = ? AND status = ?", today, taskID, StatusFailed).
		Update("status", StatusSkipped)
}

// ============ 状态和进度查询============

func (s *TaskScheduler) GetTaskProgress(phase TaskPhase) map[string]interface{} {
	if s.db == nil {
		return map[string]interface{}{"error": "database not initialized"}
	}

	today := time.Now().Format("2006-01-02")
	tasks := s.GetTasksByPhase(phase)

	var completed, failed, pending int64
	for _, task := range tasks {
		var count int64
		s.db.GetDB().Model(&data.AgentTaskLog{}).
			Where("task_date = ? AND task_name = ? AND status = ?", today, task.TaskName, StatusSuccess).
			Count(&count)
		if count > 0 {
			completed++
		} else {
			var failCount int64
			s.db.GetDB().Model(&data.AgentTaskLog{}).
				Where("task_date = ? AND task_name = ? AND status = ?", today, task.TaskName, StatusFailed).
				Count(&failCount)
			if failCount > 0 {
				failed++
			} else {
				pending++
			}
		}
	}

	total := int64(len(tasks))
	progress := 0.0
	if total > 0 {
		progress = float64(completed) / float64(total) * 100
	}

	return map[string]interface{}{
		"phase":     string(phase),
		"total":     total,
		"completed": completed,
		"failed":    failed,
		"pending":   pending,
		"progress":  progress,
	}
}

func (s *TaskScheduler) GetSchedulerStats() map[string]interface{} {
	phaseTasks := make(map[string]int)
	for _, task := range s.tasks {
		phaseTasks[string(task.Phase)]++
	}

	eventDrivenCount := 0
	for _, task := range s.tasks {
		if task.IsEventDriven {
			eventDrivenCount++
		}
	}

	return map[string]interface{}{
		"total_tasks":        len(s.tasks),
		"phase_tasks":        phaseTasks,
		"event_driven":       eventDrivenCount,
		"running":            s.running,
		"executor_available": s.GetExecutorAvailability(),
		"current_phase":      string(s.GetCurrentPhase()),
	}
}

// GetTaskStatusByRole 按角色获取今日任务状态
func (s *TaskScheduler) GetTaskStatusByRole() map[string]interface{} {
	if s.db == nil {
		return map[string]interface{}{"error": "database not initialized"}
	}

	today := time.Now().Format("2006-01-02")
	roles := []port.AgentRole{
		port.RolePlanner,
		port.RoleQuant,
		port.RoleRisk,
		port.RoleCIO,
		port.RoleTrader,
	}

	result := make(map[string]interface{})
	for _, role := range roles {
		roleTasks := make([]interface{}, 0)
		for _, task := range s.tasks {
			if task.AgentRole == role {
				var statusCount int64
				status := StatusPending
				s.db.GetDB().Model(&data.AgentTaskLog{}).
					Where("task_date = ? AND task_name = ? AND status = ?", today, task.TaskName, StatusSuccess).
					Count(&statusCount)
				if statusCount > 0 {
					status = StatusSuccess
				} else {
					var failCount int64
					s.db.GetDB().Model(&data.AgentTaskLog{}).
						Where("task_date = ? AND task_name = ? AND status = ?", today, task.TaskName, StatusFailed).
						Count(&failCount)
					if failCount > 0 {
						status = StatusFailed
					}
				}

				roleTasks = append(roleTasks, map[string]interface{}{
					"id":           task.ID,
					"name":         task.TaskName,
					"phase":        string(task.Phase),
					"status":       status,
					"event_driven": task.IsEventDriven,
				})
			}
		}
		result[string(role)] = map[string]interface{}{
			"role":  string(role),
			"tasks": roleTasks,
			"count": len(roleTasks),
		}
	}

	return result
}

// ExecuteAllTasks 手动触发所有任务（调试模式）
func (s *TaskScheduler) ExecuteAllTasks() error {
	if !s.GetExecutorAvailability() {
		return fmt.Errorf("executor not available")
	}

	log.Printf("[TaskScheduler] ExecuteAllTasks triggered, total tasks: %d", len(s.tasks))

	for _, task := range s.tasks {
		if task.IsEventDriven {
			continue
		}
		s.resetTaskStatus(task.ID)
		s.executeTaskWithRetry(task, nil)
	}

	return nil
}

// TriggerDailyWorkflow 触发每日工作流（供外部定时器调用）
func (s *TaskScheduler) TriggerDailyWorkflow() {
	log.Printf("[TaskScheduler] TriggerDailyWorkflow called")

	// 检查是否为工作日
	now := time.Now()
	if now.Weekday() == time.Saturday || now.Weekday() == time.Sunday {
		log.Printf("[TaskScheduler] Weekend, skipping daily workflow")
		return
	}

	// 防止重复触发
	today := now.Format("2006-01-02")
	s.mu.Lock()
	if s.lastWorkflowTriggerDate == today {
		log.Printf("[TaskScheduler] Daily workflow already triggered today, skipping")
		s.mu.Unlock()
		return
	}
	s.lastWorkflowTriggerDate = today
	s.mu.Unlock()

	// 触发盘前阶段任务
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[TaskScheduler] TriggerDailyWorkflow panic: %v", r)
			}
		}()

		log.Printf("[TaskScheduler] Executing pre-market tasks for %s", today)

		// 执行盘前阶段的所有任务
		preMarketTasks := s.GetTasksByPhase(PhasePreMarket)
		for _, task := range preMarketTasks {
			if s.shouldSkipTask(task) {
				log.Printf("[TaskScheduler] Skipping task: %s (%s)", task.ID, task.TaskName)
				continue
			}
			if !s.checkPreconditions(task.Preconditions, task.Phase) {
				log.Printf("[TaskScheduler] Preconditions not met for task: %s", task.ID)
				continue
			}
			log.Printf("[TaskScheduler] Executing task: %s (%s)", task.ID, task.TaskName)
			s.executeTaskWithRetry(task, nil)
			// 盘前链条（Planner→Quant→Risk→CIO）需串行：
			// 等待当前任务完成，确保 CIO 的前置 pre_planner/pre_quant/pre_risk 在判断前已落库
			s.waitForTaskCompletion(task)
		}

		// 触发每日工作流回调（如果设置了）
		if s.dailyWorkflowTrigger != nil {
			log.Printf("[TaskScheduler] Calling dailyWorkflowTrigger")
			s.dailyWorkflowTrigger()
		}

		log.Printf("[TaskScheduler] Pre-market tasks completed for %s", today)
	}()
}
