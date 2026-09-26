package port

import (
	"context"
	"strings"
	"time"
)

// ==================== 规划/画像/对话/计划 数据模型（决策脑本地 DTO）====================
//
// 以下类型与宿主 internal/data 同名实体字段对齐，宿主在 brainhost 适配器中做
// data ↔ port 双向映射后注入 store，使决策脑不反向依赖宿主数据层。

// InvestorProfile 投资者画像
type InvestorProfile struct {
	ID                   uint
	UserID               uint
	ProfileJSON          string
	Capital              float64
	Currency             string
	InvestmentExperience string
	InvestmentStyle      string
	InvestmentHorizon    string
	InvestmentObjective  string
	RiskTolerance        string
	DrawdownTolerance    float64
	LossTolerance        float64
	LiquidityRequirement string
	TradingFrequency     string
	MarketPreference     string
	ProfileCompleteness  float64
	CurrentStep          string
	StepProgress         int
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

// Conversation 对话
type Conversation struct {
	ID             uint
	ConversationID string
	UserID         uint
	ProfileID      *uint
	Title          string
	Status         string
	CurrentStep    string
	MessageCount   int
	StartedAt      time.Time
	UpdatedAt      time.Time
}

// ConversationMessage 对话消息
type ConversationMessage struct {
	ID             uint
	ConversationID string
	Role           string
	Content        string
	MessageType    string
	MetadataJSON   string
	CreatedAt      time.Time
}

// InvestmentPlan 投资计划
type InvestmentPlan struct {
	ID               uint
	PlanID           string
	UserID           uint
	ProfileID        uint
	Name             string
	Objective        string
	RiskLevel        string
	TargetReturn     float64
	TargetVolatility float64
	MaxDrawdown      float64
	StrategyType     string
	Status           string
	Version          int
	PlanJSON         string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// StrategyCandidate 策略候选方案
type StrategyCandidate struct {
	ID                 uint
	CandidateID        string
	PlanID             string
	Name               string
	Description        string
	Label              string
	Universe           string
	SignalDefinition   string
	ExpectedReturn     float64
	ExpectedVolatility float64
	MaxDrawdown        float64
	Sharpe             float64
	Calmar             float64
	BacktestStart      *time.Time
	BacktestEnd        *time.Time
	TransactionCost    float64
	Turnover           float64
	RobustnessScore    float64
	RiskScore          float64
	RiskOSStatus       string
	Status             string
	CandidateJSON      string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// PlanApproval 计划审批
type PlanApproval struct {
	ID               uint
	PlanID           string
	UserID           uint
	IsApproved       int
	RiskAcknowledged int
	Notes            string
	ApprovedAt       *time.Time
	CreatedAt        time.Time
}

// PlannerStore 决策脑规划器持久化抽象：宿主以 internal/data.SQLiteManager 适配后注入。
type PlannerStore interface {
	// --- 画像 ---
	GetOrCreateProfile(userID uint) (*InvestorProfile, error)
	UpdateProfile(profile *InvestorProfile) error

	// --- 对话 ---
	CreateConversation(conv *Conversation) error
	GetActiveConversation(userID uint) (*Conversation, error)
	AddMessage(msg *ConversationMessage, conversationID string) error
	UpdateConversationStep(conversationID, currentStep string) error
	GetConversationMessages(conversationID string) ([]ConversationMessage, error)
	GetConversation(conversationID string) (*Conversation, error)

	// --- 计划 ---
	CreatePlan(plan *InvestmentPlan) error
	CreateCandidates(candidates []*StrategyCandidate) error
	UpdatePlanStatus(plan *InvestmentPlan, status string) error
	UpdatePlanJSONByPlanID(planID, planJSON string) error
	ListReviewingPlans(userID uint) ([]InvestmentPlan, error)
	GetCurrentPlan(userID uint) (*InvestmentPlan, error)
	ListPlans(userID uint) ([]InvestmentPlan, error)
	GetPlan(planID string) (*InvestmentPlan, error)
	GetPlanCandidates(planID string) ([]StrategyCandidate, error)

	// --- 审批 ---
	ApprovePlan(approval *PlanApproval, activatedPlanID string, userID uint) error
	RejectPlan(approval *PlanApproval, planID string) error
}

// ==================== 选股领域模型（决策脑本地，取自 screener）====================

// FactorID 因子ID
type FactorID string

// 因子常量（与 screener 一致）
const (
	FactorValue             FactorID = "value"
	FactorQuality           FactorID = "quality"
	FactorMomentum          FactorID = "momentum"
	FactorLowVolatility     FactorID = "low_volatility"
	FactorEarningsStability FactorID = "earnings_stability"
	FactorLiquidity         FactorID = "liquidity"
	FactorOrderBook         FactorID = "order_book"
	FactorCointegration     FactorID = "cointegration_spread"
)

// FactorScore 单因子得分
type FactorScore struct {
	FactorID    FactorID
	FactorName  string
	Score       float64
	Contributor string
	Breakdown   map[string]float64
}

// StockScore 单只股票的综合评分
type StockScore struct {
	Code         string
	Name         string
	Market       string
	Price        float64
	ChangePct    float64
	TotalScore   float64
	FactorScores map[FactorID]FactorScore
	Ranking      int
	Reasons      []string
	Warnings     []string
}

// MarketState 市场状态
type MarketState struct {
	Regime      string // bull/bear/range
	TrendScore  float64
	Volatility  float64
	Breadth     float64
	Description string
}

// StrategyTemplate 策略模板
type StrategyTemplate struct {
	ID          string
	Name        string
	Description string
	Icon        string
	Weights     map[FactorID]float64
	RiskLevel   string
}

// ScreeningRequest 选股请求
type ScreeningRequest struct {
	StrategyID      string
	CustomWeights   map[FactorID]float64
	Market          string
	MaxResults      int
	MinScore        float64
	SmallCapital    bool
	InvestorProfile *InvestorProfile
	DiversifySeed   int64
}

// ScreeningResponse 选股结果（仅含决策脑所需字段）
type ScreeningResponse struct {
	StrategyName string
	TotalCount   int
	Results      []StockScore
	GeneratedAt  string
	MarketState  *MarketState
}

// StockScreener 真实选股服务抽象：宿主以 internal/screener.ScreenerService 适配后注入。
type StockScreener interface {
	ScreenStock(req ScreeningRequest) (ScreeningResponse, error)
}

// StockMeta 股票元信息提供者：宿主以 internal/data.DictLoader 适配后注入。
type StockMeta interface {
	GetStockName(code string) string
	GetIndustryByStock(code string) string
}

// defaultStrategies 默认3个策略模板（取自已脱宿主的 screener.DefaultStrategies）
func defaultStrategies() map[string]*StrategyTemplate {
	bal := &StrategyTemplate{
		ID:          "balanced",
		Name:        "均衡配置",
		Description: "适合长期投资、追求稳定增长的投资者，分散配置价值、质量、动量等多个因子",
		Icon:        "balanced",
		RiskLevel:   "中等",
		Weights: map[FactorID]float64{
			FactorValue:             0.20,
			FactorQuality:           0.20,
			FactorMomentum:          0.20,
			FactorLowVolatility:     0.15,
			FactorEarningsStability: 0.15,
			FactorLiquidity:         0.10,
			FactorOrderBook:         0.05,
			FactorCointegration:     0.04,
		},
	}
	def := &StrategyTemplate{
		ID:          "defensive",
		Name:        "红利价值多头",
		Description: "适合风险承受能力低但仍投资股票的投资者（股票私募·防御型多头）：聚焦高股息、低波动、高质量的价值蓝筹股，不配置债券",
		Icon:        "defensive",
		RiskLevel:   "低",
		Weights: map[FactorID]float64{
			FactorValue:             0.15,
			FactorQuality:           0.30,
			FactorMomentum:          0.10,
			FactorLowVolatility:     0.25,
			FactorEarningsStability: 0.20,
			FactorLiquidity:         0.00,
		},
	}
	grw := &StrategyTemplate{
		ID:          "growth",
		Name:        "成长型",
		Description: "适合追求高收益的投资者，重点关注动量、成长、质量因子，接受较高波动",
		Icon:        "growth",
		RiskLevel:   "高",
		Weights: map[FactorID]float64{
			FactorValue:             0.10,
			FactorQuality:           0.25,
			FactorMomentum:          0.30,
			FactorLowVolatility:     0.10,
			FactorEarningsStability: 0.15,
			FactorLiquidity:         0.10,
		},
	}
	return map[string]*StrategyTemplate{
		"balanced": bal, "defensive": def, "growth": grw,
	}
}

// GetStrategyTemplate 根据ID获取策略模板
func GetStrategyTemplate(id string) *StrategyTemplate {
	switch id {
	case "defensive":
		return defaultStrategies()["defensive"]
	case "growth":
		return defaultStrategies()["growth"]
	default:
		return defaultStrategies()["balanced"]
	}
}

// ResolveTemplateID 将风险等级/风格/策略类型归类到统一的策略模板ID（取自已脱宿主的 screener.ResolveTemplateID）。
func ResolveTemplateID(key string) string {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "defensive", "conservative", "income", "capital_preservation",
		"dividend_value", "value_factor", "quality_factor":
		return "defensive"
	case "growth", "aggressive", "growth_factor":
		return "growth"
	case "balanced", "multi_factor", "index_tracking":
		return "balanced"
	default:
		return "balanced"
	}
}

// ==================== LLM 审计元数据装饰 ====================

// CallMeta LLM 调用的审计元信息
type CallMeta struct {
	AgentRole string
	TaskName  string
	Phase     string
}

// CallMetaDecorator 决策脑调用 LLM 前给 ctx 附加审计元信息。宿主以
// internal/llmmonitoring.WithCallMeta 适配后注入；为 nil 时返回原 ctx。
type CallMetaDecorator func(ctx context.Context, meta CallMeta) context.Context
