package agentworkflow

import (
	"time"

	"gorm.io/gorm"
)

// AgentID 智能体ID常量
const (
	AgentCIO     = "AGT-001" // 首席投资官
	AgentPlanner = "AGT-002" // 投资规划师
	AgentQuant   = "AGT-003" // 量化分析师
	AgentRisk    = "AGT-004" // 风控师
	AgentTrader  = "AGT-005" // 操盘手
)

// AgentRoleName 智能体角色名称
var AgentRoleName = map[string]string{
	AgentCIO:     "CIO·首席投资官",
	AgentPlanner: "Investment Planner·投资规划师",
	AgentQuant:   "Quant Analyst·量化分析师",
	AgentRisk:    "Risk Manager·风控师",
	AgentTrader:  "Trader·操盘手",
}

// AgentWorkbench 智能体工作台信息
type AgentWorkbench struct {
	AgentID        string   `json:"agent_id"`
	AgentName      string   `json:"agent_name"`
	PendingTasks   int      `json:"pending_tasks"`
	RunningTasks   int      `json:"running_tasks"`
	CompletedToday int      `json:"completed_today"`
	TeamMembers    []string `json:"team_members"`
}

// ==================== 权限模型 ====================

// AgentPermission 智能体权限表
type AgentPermission struct {
	ID          uint   `gorm:"primaryKey"`
	AgentID     string `gorm:"uniqueIndex:idx_agent_perm;size:20;not null"`
	Permission  string `gorm:"uniqueIndex:idx_agent_perm;size:50;not null"`
	Description string `gorm:"size:200"`
	IsAllowed   bool   `gorm:"not null;default:true"`
	CreatedAt   time.Time
}

// 权限常量
const (
	// Planner权限
	PermCreateInvestmentProfile = "CREATE_INVESTMENT_PROFILE"
	PermModifyInvestmentGoal    = "MODIFY_INVESTMENT_GOAL"
	PermCreateInvestmentPlan    = "CREATE_INVESTMENT_PLAN" // 新增：创建投资方案

	// Quant权限
	PermRecommendAsset          = "RECOMMEND_ASSET"
	PermRecommendFactor         = "RECOMMEND_FACTOR"
	PermRecommendPortfolio      = "RECOMMEND_PORTFOLIO"
	PermGenerateStockCandidates = "GENERATE_STOCK_CANDIDATES"

	// CIO权限
	PermSelectAsset              = "SELECT_ASSET"
	PermSetTargetWeight          = "SET_TARGET_WEIGHT"
	PermIncreasePosition         = "INCREASE_POSITION"
	PermReducePosition           = "REDUCE_POSITION"
	PermExitPosition             = "EXIT_POSITION"
	PermApproveInvestment        = "APPROVE_INVESTMENT"
	PermChangeInvestmentDecision = "CHANGE_INVESTMENT_DECISION"
	PermApproveInvestmentPlan    = "APPROVE_INVESTMENT_PLAN" // 新增：批准投资方案
	PermRejectInvestmentPlan     = "REJECT_INVESTMENT_PLAN"  // 新增：拒绝投资方案

	// Risk权限
	PermRiskAssess      = "RISK_ASSESS"
	PermRiskApprove     = "RISK_APPROVE"
	PermRiskConditional = "RISK_CONDITIONAL"
	PermRiskReject      = "RISK_REJECT"
	PermEmergencyFreeze = "EMERGENCY_FREEZE"

	// Trader权限
	PermCreateOrder  = "CREATE_ORDER"
	PermSubmitOrder  = "SUBMIT_ORDER"
	PermMonitorOrder = "MONITOR_ORDER"
	PermCancelOrder  = "CANCEL_ORDER"
	PermReconcile    = "RECONCILE"

	// 禁止权限
	PermForbidApproveInvestment  = "FORBID_APPROVE_INVESTMENT"
	PermForbidExecuteOrder       = "FORBID_EXECUTE_ORDER"
	PermForbidSelectStock        = "FORBID_SELECT_STOCK"
	PermForbidChangeTargetWeight = "FORBID_CHANGE_TARGET_WEIGHT"
	PermForbidOverrideRisk       = "FORBID_OVERRIDE_RISK"
)

// PermissionMatrix 权限矩阵定义
var PermissionMatrix = map[string]map[string]bool{
	AgentPlanner: {
		PermCreateInvestmentProfile: true,
		PermModifyInvestmentGoal:    true,
		PermCreateInvestmentPlan:    true, // 投资规划师可以创建投资方案
		PermRiskApprove:             true,
		PermForbidApproveInvestment: true, // 投资规划师不能批准投资
		PermForbidExecuteOrder:      true,
	},
	AgentQuant: {
		PermRecommendAsset:          true,
		PermRecommendFactor:         true,
		PermRecommendPortfolio:      true,
		PermGenerateStockCandidates: true,
		PermForbidApproveInvestment: true,
		PermForbidExecuteOrder:      true,
	},
	AgentCIO: {
		PermSelectAsset:              true,
		PermSetTargetWeight:          true,
		PermIncreasePosition:         true,
		PermReducePosition:           true,
		PermExitPosition:             true,
		PermApproveInvestment:        true,
		PermChangeInvestmentDecision: true,
		PermApproveInvestmentPlan:    true, // CIO可以批准投资方案
		PermRejectInvestmentPlan:     true, // CIO可以拒绝投资方案
	},
	AgentRisk: {
		PermRiskAssess:              true,
		PermRiskApprove:             true,
		PermRiskConditional:         true,
		PermRiskReject:              true,
		PermEmergencyFreeze:         true,
		PermForbidApproveInvestment: true,
		PermForbidExecuteOrder:      true,
	},
	AgentTrader: {
		PermCreateOrder:              true,
		PermSubmitOrder:              true,
		PermMonitorOrder:             true,
		PermCancelOrder:              true,
		PermReconcile:                true,
		PermForbidSelectStock:        true,
		PermForbidChangeTargetWeight: true,
		PermForbidOverrideRisk:       true,
	},
}

// ==================== 任务模型 ====================

// Task 智能体任务表
type Task struct {
	ID           uint   `gorm:"primaryKey"`
	TaskID       string `gorm:"uniqueIndex;size:50;not null"`
	AgentID      string `gorm:"index;size:20;not null"`
	TaskType     string `gorm:"index;size:50;not null"`
	Title        string `gorm:"size:200;not null"`
	Description  string `gorm:"type:text"`
	Trigger      string `gorm:"index;size:50;not null"`
	Priority     string `gorm:"index;size:20;not null"`
	Status       string `gorm:"index;size:30;not null"`
	Phase        string `gorm:"index;size:30;not null"`
	InputTaskIDs string `gorm:"type:text"`
	InputData    string `gorm:"type:text"`
	OutputData   string `gorm:"type:text"`
	ParentTaskID string `gorm:"index;size:50"`
	Market       string `gorm:"size:20"`
	PortfolioID  string `gorm:"size:50"`
	Deadline     *time.Time
	CreatedAt    time.Time `gorm:"index;not null"`
	StartedAt    *time.Time
	CompletedAt  *time.Time
	UpdatedAt    time.Time
}

// Task状态常量
const (
	TaskStatusCreated   = "CREATED"
	TaskStatusQueued    = "QUEUED"
	TaskStatusRunning   = "RUNNING"
	TaskStatusWaiting   = "WAITING"
	TaskStatusCompleted = "COMPLETED"
	TaskStatusApproved  = "APPROVED"
	TaskStatusClosed    = "CLOSED"
	TaskStatusFailed    = "FAILED"
	TaskStatusBlocked   = "BLOCKED" // 数据缺失/前置条件不满足，任务被阻断
	TaskStatusCancelled = "CANCELLED"
	TaskStatusExpired   = "EXPIRED"
)

// 时间阶段常量
const (
	PhasePreMarket       = "PRE_MARKET"
	PhaseMorningMarket   = "MORNING_MARKET"
	PhaseAfternoonMarket = "AFTERNOON_MARKET"
	PhasePostMarket      = "POST_MARKET"
	PhaseReview          = "REVIEW"
	PhaseEmergency       = "EMERGENCY"
)

// 任务类型常量
const (
	TaskTypeUpdateProfile    = "UPDATE_PROFILE"
	TaskTypeCheckMandate     = "CHECK_MANDATE"
	TaskTypeComplianceReport = "COMPLIANCE_REPORT"

	TaskTypeMarketScan          = "MARKET_SCAN"
	TaskTypeFactorHealth        = "FACTOR_HEALTH"
	TaskTypeStockScreening      = "STOCK_SCREENING"
	TaskTypePortfolioSimulation = "PORTFOLIO_SIMULATION"
	TaskTypeAlphaDiscovery      = "ALPHA_DISCOVERY"
	TaskTypeRegimeDetection     = "REGIME_DETECTION"
	TaskTypeFactorBreak         = "FACTOR_BREAK_ANALYSIS"

	TaskTypeMorningBrief       = "MORNING_BRIEF"
	TaskTypeInvestmentDecision = "INVESTMENT_DECISION"
	TaskTypePortfolioReview    = "PORTFOLIO_REVIEW"
	TaskTypeEmergencyDecision  = "EMERGENCY_DECISION"
	TaskTypeDailyReview        = "DAILY_REVIEW"
	TaskTypeT1Planning         = "T1_PLANNING"

	TaskTypeOvernightRisk  = "OVERNIGHT_RISK"
	TaskTypeEndOfDayRisk   = "END_OF_DAY_RISK"
	TaskTypeTradeRiskCheck = "TRADE_RISK_CHECK"
	TaskTypeStressTest     = "STRESS_TEST"
	TaskTypeRiskAssessment = "RISK_ASSESSMENT"
	TaskTypeRiskAlert      = "RISK_ALERT"

	TaskTypeGenerateOrder   = "GENERATE_ORDER"
	TaskTypeExecuteOrder    = "EXECUTE_ORDER"
	TaskTypeMonitorOrder    = "MONITOR_ORDER"
	TaskTypeExecutionReport = "EXECUTION_REPORT"
	TaskTypeReconciliation  = "RECONCILIATION"
)

// ==================== 事件模型 ====================

// Event 系统事件表
type Event struct {
	ID             uint      `gorm:"primaryKey"`
	EventID        string    `gorm:"uniqueIndex;size:50;not null"`
	EventType      string    `gorm:"index;size:50;not null"`
	Source         string    `gorm:"index;size:50"`
	Description    string    `gorm:"type:text"`
	Payload        string    `gorm:"type:text"`
	Status         string    `gorm:"index;size:20;not null"`
	TriggeredTasks string    `gorm:"type:text"`
	CreatedAt      time.Time `gorm:"index;not null"`
}

// 事件类型常量
const (
	EventSystemStart     = "SYSTEM_START"
	EventMarketPreOpen   = "MARKET_PRE_OPEN"
	EventMarketOpen      = "MARKET_OPEN"
	EventMarketClose     = "MARKET_CLOSE"
	EventMarketShock     = "MARKET_SHOCK"
	EventPriceAlert      = "PRICE_ALERT"
	EventVolatilitySpike = "VOLATILITY_SPIKE"
	EventFactorBreak     = "FACTOR_BREAK"
	EventRiskLimit       = "RISK_LIMIT"
	EventPortfolioDrift  = "PORTFOLIO_DRIFT"
	EventNewsEvent       = "NEWS_EVENT"
	EventOrderFilled     = "ORDER_FILLED"
	EventOrderFailed     = "ORDER_FAILED"
	EventUserUpdate      = "USER_UPDATE"
	EventMandateChanged  = "MANDATE_CHANGED"
	EventEmergencyStop   = "EMERGENCY_STOP"
)

// Event状态常量
const (
	EventStatusPending    = "PENDING"
	EventStatusProcessing = "PROCESSING"
	EventStatusCompleted  = "COMPLETED"
	EventStatusFailed     = "FAILED"
)

// ==================== 工作流模型 ====================

// Workflow 工作流定义表
type Workflow struct {
	ID           uint   `gorm:"primaryKey"`
	WorkflowID   string `gorm:"uniqueIndex;size:50;not null"`
	WorkflowType string `gorm:"index;size:50;not null"`
	Name         string `gorm:"size:100;not null"`
	Description  string `gorm:"type:text"`
	TriggerEvent string `gorm:"size:50"`
	IsActive     bool   `gorm:"not null;default:true"`
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// WorkflowInstance 工作流实例表
type WorkflowInstance struct {
	ID            uint      `gorm:"primaryKey"`
	InstanceID    string    `gorm:"uniqueIndex;size:50;not null"`
	WorkflowID    string    `gorm:"index;size:50;not null"`
	Status        string    `gorm:"index;size:30;not null"`
	CurrentNode   string    `gorm:"index;size:50"`
	CurrentAgent  string    `gorm:"index;size:20"`
	InputTaskIDs  string    `gorm:"type:text"`
	OutputTaskIDs string    `gorm:"type:text"`
	History       string    `gorm:"type:text"`
	ErrorMsg      string    `gorm:"type:text"`
	CreatedAt     time.Time `gorm:"index;not null"`
	StartedAt     *time.Time
	CompletedAt   *time.Time
}

// Workflow节点状态
const (
	WFInstanceStatusPending   = "PENDING"
	WFInstanceStatusRunning   = "RUNNING"
	WFInstanceStatusWaiting   = "WAITING"
	WFInstanceStatusCompleted = "COMPLETED"
	WFInstanceStatusFailed    = "FAILED"
	WFInstanceStatusCancelled = "CANCELLED"
)

// 工作流类型
const (
	WFTypeDailyInvestment = "WF-001"
	WFTypePortfolioAdjust = "WF-002"
	WFTypeRiskEvent       = "WF-003"
	WFTypeUserGoalChange  = "WF-004"
)

// ==================== 投资决策模型 ====================

// InvestmentDecision 投资决策表
type InvestmentDecision struct {
	ID             uint   `gorm:"primaryKey"`
	DecisionID     string `gorm:"uniqueIndex;size:50;not null"`
	DecisionType   string `gorm:"index;size:30;not null"`
	AssetID        string `gorm:"index;size:30;not null"`
	AssetName      string `gorm:"size:100"`
	CurrentWeight  float64
	TargetWeight   float64
	Reason         string `gorm:"type:text"`
	CIOAgentID     string `gorm:"index;size:20;not null"`
	Status         string `gorm:"index;size:30;not null"`
	RiskStatus     string `gorm:"index;size:30"`
	RiskConditions string `gorm:"type:text"`
	ValidityDate   *time.Time
	ExpiryDate     *time.Time
	RelatedTaskIDs string    `gorm:"type:text"`
	CreatedAt      time.Time `gorm:"index;not null"`
	UpdatedAt      time.Time
}

// 决策类型
const (
	DecisionIncrease = "INCREASE"
	DecisionReduce   = "REDUCE"
	DecisionHold     = "HOLD"
	DecisionExit     = "EXIT"
	DecisionNoAction = "NO_ACTION"
)

// 决策状态
const (
	DecisionStatusDraft        = "DRAFT"
	DecisionStatusPendingRisk  = "PENDING_RISK" // 待Risk审查
	DecisionStatusApproved     = "APPROVED"
	DecisionStatusCIOApproved  = "CIO_APPROVED" // CIO最终批准（与APPROVED等价）
	DecisionStatusRiskReview   = "RISK_REVIEW"
	DecisionStatusRiskApproved = "RISK_APPROVED"
	DecisionStatusExecuting    = "EXECUTING"
	DecisionStatusExecuted     = "EXECUTED"
	DecisionStatusRejected     = "REJECTED"
	DecisionStatusExpired      = "EXPIRED" // 决策过期
	DecisionStatusFailed       = "FAILED"  // 执行失败
)

// ==================== AI Investment Team 成员 ====================

// TeamMember 团队成员信息
type TeamMember struct {
	ID             uint   `gorm:"primaryKey"`
	AgentID        string `gorm:"uniqueIndex;size:20;not null"`
	Name           string `gorm:"size:50;not null"`
	Title          string `gorm:"size:100;not null"`
	Role           string `gorm:"size:50"`
	Description    string `gorm:"type:text"`
	AvatarURL      string `gorm:"size:500"`
	Signature      string `gorm:"type:text"`
	IsActive       bool   `gorm:"not null;default:true"`
	LastActiveTime *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// AgentPerformance 智能体绩效表
type AgentPerformance struct {
	ID                   uint      `gorm:"primaryKey"`
	AgentID              string    `gorm:"index;size:20;not null"`
	PeriodStart          time.Time `gorm:"index"`
	PeriodEnd            time.Time `gorm:"index"`
	PredictionAccuracy   float64
	AlphaIC              float64
	CandidateSuccessRate float64
	ResearchCoverage     float64
	AlertHitRate         float64
	FalsePositiveRate    float64
	MissRate             float64
	AlertLeadTime        float64
	ExecutionAlpha       float64
	Slippage             float64
	FillRate             float64
	DecisionAlpha        float64
	PortfolioReturn      float64
	RiskAdjustedReturn   float64
	DecisionHitRate      float64
	CreatedAt            time.Time
}

// ==================== 初始化函数 ====================

// MigrateAgentWorkflow 迁移Agent工作流相关表
func MigrateAgentWorkflow(db *gorm.DB) error {
	return db.AutoMigrate(
		&AgentPermission{},
		&Task{},
		&Event{},
		&Workflow{},
		&WorkflowInstance{},
		&InvestmentDecision{},
		&TeamMember{},
		&AgentPerformance{},
	)
}

// InitDefaultAgentData 初始化默认Agent数据
func InitDefaultAgentData(db *gorm.DB) {
	members := []TeamMember{
		{
			AgentID:     AgentCIO,
			Name:        "CIO",
			Title:       "首席投资官 · Chief Investment Officer",
			Role:        "投资决策中心",
			Description: "负责最终投资决策，协调其他智能体工作。拥有持仓股票的最终选择权，决定买入、卖出、仓位调整。",
			Signature:   "市场永远是对的，我们只有不断学习和适应。",
			IsActive:    true,
		},
		{
			AgentID:     AgentPlanner,
			Name:        "Planner",
			Title:       "投资规划师 · Investment Planner",
			Role:        "用户目标翻译器",
			Description: "将用户投资目标转化为可执行的投资政策，生成Investment Mandate，确保所有智能体遵循。",
			Signature:   "好的规划是成功的一半，让我们先定义目标，再寻找路径。",
			IsActive:    true,
		},
		{
			AgentID:     AgentQuant,
			Name:        "Quant",
			Title:       "量化分析师 · Quant Analyst",
			Role:        "市场研究引擎",
			Description: "执行市场扫描、因子分析、选股、组合模拟，为CIO提供候选股票和研究报告。只有推荐权，没有决策权。",
			Signature:   "数据不会说谎，让我们用数字说话。",
			IsActive:    true,
		},
		{
			AgentID:     AgentRisk,
			Name:        "Risk",
			Title:       "风控师 · Risk Manager",
			Role:        "独立风险闸门",
			Description: "独立评估投资风险，拥有否决权。可以阻止任何投资决策，即使CIO已批准。",
			Signature:   "宁可错过，不可做错。风险管理是我们的底线。",
			IsActive:    true,
		},
		{
			AgentID:     AgentTrader,
			Name:        "Trader",
			Title:       "操盘手 · Trader/Operator",
			Role:        "执行层",
			Description: "仅执行已获CIO和风控师双批准的订单。不能选择股票、修改投资决策、绕过风控。",
			Signature:   "精准执行，减少滑点，保护客户利益。",
			IsActive:    true,
		},
	}

	for _, m := range members {
		var count int64
		db.Model(&TeamMember{}).Where("agent_id = ?", m.AgentID).Count(&count)
		if count == 0 {
			db.Create(&m)
		}
	}

	for agentID, perms := range PermissionMatrix {
		for perm, allowed := range perms {
			var count int64
			db.Model(&AgentPermission{}).Where("agent_id = ? AND permission = ?", agentID, perm).Count(&count)
			if count == 0 {
				db.Create(&AgentPermission{
					AgentID:    agentID,
					Permission: perm,
					IsAllowed:  allowed,
				})
			}
		}
	}
}
