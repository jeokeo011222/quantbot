// Package port 定义宿主（开源侧）面向决策脑 DLL 的窄抽象接口与 DTO。
//
// 该包为「DLL 隔离重构」的开源类型层：从闭源决策脑（brain 子树）的 port/toolkit 类型层
// 复刻而来，宿主在装配点将真实实现（internal/data、internal/tools 等）适配为本包接口后，
// 供给决策脑 DLL。宿主代码只依赖本包，不再 import 闭源的 internal/brain。
package port

import (
	"context"
	"strings"
	"time"
)

// ==================== 工具契约（复刻自内部 brain/toolkit，自包含） ====================

// ToolFunction 函数定义（用于 LLM function calling，语义对齐 internal/tools.FunctionDef）。
type ToolFunction struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Parameters  interface{} `json:"parameters"`
}

// ToolDefinition 工具定义（语义对齐 internal/tools.ToolDefinition）。
type ToolDefinition struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

// ToolExecutor 工具执行接口：方法集 = 决策脑实际调用 internal/tools.Tool 的方法。
// 宿主将真实工具（internal/tools.*Tool）适配为该接口后供给 DLL。
type ToolExecutor interface {
	Name() string
	Description() string
	GetDefinition() ToolDefinition
	Execute(ctx context.Context, args map[string]interface{}) (interface{}, error)
}

// ToolSpec 工具元数据（纯元数据，供工具目录使用）。
type ToolSpec struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Params      map[string]interface{} `json:"params,omitempty"`
	JsonSchema  interface{}            `json:"json_schema,omitempty"`
	Category    string                 `json:"category"`
	Roles       []string               `json:"roles"`
	RiskLevel   string                 `json:"risk_level"`
	Version     string                 `json:"version"`
}

// ToolCallRecord 工具调用记录（纯数据，语义对齐 internal/tools.ToolCallRecord）。
type ToolCallRecord struct {
	ToolName   string      `json:"tool_name"`
	ToolCallID string      `json:"tool_call_id"`
	Arguments  interface{} `json:"arguments"`
	Result     interface{} `json:"result,omitempty"`
	Error      string      `json:"error,omitempty"`
	Success    bool        `json:"success"`
	Timestamp  time.Time   `json:"timestamp"`
}

// ContextProvider 由宿主注入：为决策脑提供大盘行情摘要、组合快照、持仓明细等系统真实上下文。
// 返回空字符串表示无上下文。可为 nil。
type ContextProvider func(ctx context.Context, role string, date string) string

// TaskLog 任务日志条目视图（对齐 data.AgentTaskLog，决策脑仅需 ID 以回填状态）。
type TaskLog struct {
	ID uint
}

// Persistence 决策脑持久化抽象：抽象 data.SQLiteManager / data.AgentTaskLogger。
type Persistence interface {
	ClearTaskLogs(taskDate string) error
	LogTaskStart(taskDate, taskPhase, agentRole, taskName string, taskOrder int) *TaskLog
	LogTaskComplete(taskID uint, deliverableType, deliverableName string, deliverableData interface{}, summary string)
	LogTaskFailed(taskID uint, errMsg string)
	WriteAudit(eventID, eventType, userID, userName, action, targetType, targetID, result, detailsJSON string, ts time.Time) error
	SaveCIODecision(decisionID, portfolioID, decision, reason, riskApproval, policyStatus, marketState string, marketConfidence float64, ts time.Time) error
}

// Tracer 决策脑透明追踪抽象：宿主将 internal/transparency.Tracker 适配后注入。
type Tracer interface {
	StartSession(sessionID, taskDate string)
	AddDataSource(sessionID, source, status string, items []string)
	AddAlgorithm(sessionID, name, input, output, status string, durationMs int64)
	AddDecision(sessionID, role, action, reason string, dataUsed []string)
}

// ==================== 规划 / 画像 / 对话 / 计划 DTO（复刻自 brain/port/planner.go） ====================

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
	GetOrCreateProfile(userID uint) (*InvestorProfile, error)
	UpdateProfile(profile *InvestorProfile) error

	CreateConversation(conv *Conversation) error
	GetActiveConversation(userID uint) (*Conversation, error)
	AddMessage(msg *ConversationMessage, conversationID string) error
	UpdateConversationStep(conversationID, currentStep string) error
	GetConversationMessages(conversationID string) ([]ConversationMessage, error)
	GetConversation(conversationID string) (*Conversation, error)

	CreatePlan(plan *InvestmentPlan) error
	CreateCandidates(candidates []*StrategyCandidate) error
	UpdatePlanStatus(plan *InvestmentPlan, status string) error
	UpdatePlanJSONByPlanID(planID, planJSON string) error
	ListReviewingPlans(userID uint) ([]InvestmentPlan, error)
	GetCurrentPlan(userID uint) (*InvestmentPlan, error)
	ListPlans(userID uint) ([]InvestmentPlan, error)
	GetPlan(planID string) (*InvestmentPlan, error)
	GetPlanCandidates(planID string) ([]StrategyCandidate, error)

	ApprovePlan(approval *PlanApproval, activatedPlanID string, userID uint) error
	RejectPlan(approval *PlanApproval, planID string) error
}

// ==================== 选股领域模型（复刻自 brain/port/planner.go） ====================

// FactorID 因子ID
type FactorID string

// 因子常量
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
	Regime      string
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

// defaultStrategies 默认3个策略模板（与闭源决策脑 port 语义一致，取自 screener 默认策略）
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

// ResolveTemplateID 将风险等级/风格/策略类型归类到统一的策略模板ID。
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

// CallMeta LLM 调用的审计元信息
type CallMeta struct {
	AgentRole string
	TaskName  string
	Phase     string
}

// CallMetaDecorator 决策脑调用 LLM 前给 ctx 附加审计元信息。宿主以
// internal/llmmonitoring.WithCallMeta 适配后注入；为 nil 时返回原 ctx。
type CallMetaDecorator func(ctx context.Context, meta CallMeta) context.Context

// ==================== LLM 配置契约（跨边界 JSON，供 AgentInit 传给 DLL） ====================

// LLMConfig LLM 客户端配置：宿主在 AgentInit 时经 config.llm 传入，
// DLL 决策脑据此自建 llm.Client（LLM 归 DLL 自持，不再经总线代理）。
type LLMConfig struct {
	Provider string `json:"provider"` // 如 "deepseek" / "openai" / "custom"
	APIKey   string `json:"api_key"`  // 明文 API key（与 config.AIAPIKey 语义一致）
	BaseURL  string `json:"base_url"` // 空则按 provider 取默认
	Model    string `json:"model"`    // 空则按 provider 取默认
}

// ==================== 六维判势 DTO（复刻自 brain/port/marketdata.go） ====================

// KlineBar 指数/个股K线（对齐 data.KlineBarFromDuckDB）。
type KlineBar struct {
	Symbol string
	Date   time.Time
	Open   float64
	High   float64
	Low    float64
	Close  float64
	Volume float64
	Amount float64
}

// FactorBar 因子Bar（对齐 data.FactorBar）。
type FactorBar struct {
	Symbol    string
	Date      time.Time
	Open      float64
	High      float64
	Low       float64
	Close     float64
	PreClose  float64
	Volume    float64
	Amount    float64
	Turnover  float64
	PctChg    float64
	Amplitude float64
}

// SymbolInfo 股票代码信息（对齐 data.StockSymbolInfo）。
type SymbolInfo struct {
	Symbol string
	Name   string
	Market string
}

// MarketAmountDay 全市场每日成交额（对齐 data.MarketAmountDay）。
type MarketAmountDay struct {
	Date   time.Time
	Amount float64
}

// MarketLimitStats 全市场涨跌停统计（对齐 data.MarketLimitStats）。
type MarketLimitStats struct {
	LimitUp        int
	LimitDown      int
	SealedLimitUp  int
	BlownUp        int
	TouchedLimitUp int
}

// SectorChangeStat 行业平均涨跌幅统计（对齐 data.SectorChangeStat）。
type SectorChangeStat struct {
	Sector     string
	StockCount int
	AvgChgPct  float64
}

// SectorPerformanceRow 行业成交额单行（对齐 data.GetSectorPerformance 返回的 map 行）。
type SectorPerformanceRow map[string]interface{}

// MarginPoint 融资余额数据点（对齐 marketinfo.MarginPoint）。
type MarginPoint struct {
	Date    string
	Balance float64
}

// LimitUpBoard 涨停/跌停/炸板池（对齐 marketinfo.LimitUpBoard 的六维用字段）。
type LimitUpBoard struct {
	LimitUpCnt   int
	LimitDownCnt int
	ZhabanCnt    int
}

// GlobalMarketPoint 隔夜外围市场点（对齐 marketinfo.GlobalMarketPoint 的六维用字段）。
type GlobalMarketPoint struct {
	Code      string
	Name      string
	Current   float64
	ChangePct float64
	Change    float64
}

// SentimentSnapshot 同花顺官方情绪面快照（对齐 thssdk.SentimentSnapshot）。
type SentimentSnapshot struct {
	LimitUpCnt     int
	LimitDownCnt   int
	BlowUpCnt      int
	BlowUpRate     float64
	MaxBoardHeight int
}

// MarketSixDimRow 六维判势历史记录（对齐 data.MarketSixDimRow）。
type MarketSixDimRow struct {
	TradeDate          string
	DimScoresJSON      string
	RawTotalScore      float64
	ConflictCount      int
	AdjustedTotalScore float64
	PositionRate       float64
	MarketTag          string
	SourcesJSON        string
	CreatedAt          time.Time
}

// MarketDataStore 六维判势读取本地行情库（DuckDB）数据的抽象（对齐 data.DuckDBManager）。
type MarketDataStore interface {
	HasStockDB() bool
	GetKlineFromStock(ctx context.Context, symbol string, days int) ([]KlineBar, error)
	ListRepresentativeSymbols(ctx context.Context, n int) ([]SymbolInfo, error)
	ListAllSymbolsFromStock(ctx context.Context, market string, limit int) ([]SymbolInfo, error)
	BatchGetFactorBars(ctx context.Context, symbols []string, days int) (map[string][]FactorBar, error)
	GetSectorChangeStats(ctx context.Context) ([]SectorChangeStat, error)
	GetSectorPerformance(ctx context.Context) ([]SectorPerformanceRow, error)
	GetMarketAmountHistory(ctx context.Context, days int) ([]MarketAmountDay, error)
	GetMarketLimitStats(ctx context.Context) (*MarketLimitStats, error)
	SaveMarketSixDimRow(ctx context.Context, tradeDate, dimsJSON, sourcesJSON string, rawTotal, adjusted float64, conflict int, positionRate float64, tag string) error
	GetMarketSixDimRows(ctx context.Context, limit int) ([]MarketSixDimRow, error)
}

// SnapSource 活跃实时行情源（对齐 data.UnifiedDataSource，调用时动态取当前活跃 provider）。
type SnapSource interface {
	Source() string
	GetStockSnapshots(codes []string) ([]StockSnapshot, error)
}

// MarketExternalSources 外部实时源（对齐 internal/marketinfo 的六维用函数集合）。
type MarketExternalSources interface {
	FetchNorthboundRealtime(ctx context.Context) (float64, error)
	FetchMarginHistory(ctx context.Context, days int) ([]MarginPoint, error)
	FetchLimitUpBoard(ctx context.Context) (*LimitUpBoard, error)
	FetchExternalMarkets(ctx context.Context) ([]GlobalMarketPoint, string, error)
}

// THSSentimentSource 同花顺官方情绪面源（对齐 internal/thssdk）。
type THSSentimentSource interface {
	Enabled() bool
	FetchSentimentSnapshot(ctx context.Context) (*SentimentSnapshot, error)
}

// ==================== 每日交易周期 DTO（复刻自 brain/port/workflow.go + tracer.go） ====================

// WatchStock 监控股票（对齐 data.WatchStock，决策脑仅需 Code/Name/Market）。
type WatchStock struct {
	Code   string
	Name   string
	Market string
}

// StockSnapshot 股票实时快照（对齐 data.StockSnapshot，决策脑实际使用的字段）。
type StockSnapshot struct {
	Code          string
	Name          string
	Market        string
	CurrentPrice  float64
	PrevClose     float64
	Open          float64
	High          float64
	Low           float64
	Volume        float64
	Turnover      float64
	ChangePercent float64
	ChangeAmount  float64
	Timestamp     int64
	IsMock        bool
}

// MarketIndex 市场指数配置（对齐 data.MarketIndex 中决策脑实际使用的字段）。
type MarketIndex struct {
	Code   string
	Market string
}

// IndexSnapshot 指数实时快照（对齐 data.MarketIndexSnapshot 中决策脑实际使用的字段）。
type IndexSnapshot struct {
	Code    string
	Current float64
	Change  float64
}

// FactorResult 因子计算结果（对齐 data.FactorResult 中决策脑实际使用的字段）。
type FactorResult struct {
	Code       string
	Sector     string
	FactorName string
	Category   string
	Score      float64
}

// Portfolio 组合资金信息（对齐 data.Portfolio 中决策脑实际使用的字段）。
type Portfolio struct {
	InitialCapital float64
	CurrentCapital float64
}

// DictLoader 数据字典加载器抽象：宿主将 internal/data.DictLoader 适配注入。
type DictLoader interface {
	GetIndustryByStock(code string) string
	GetStockName(code string) string
	GetStockCode(name string) string
}

// WorkflowStore 决策脑每日交易周期（DailyCycle）的数据访问抽象。
type WorkflowStore interface {
	WatchStocks() []WatchStock
	FetchStockSnapshots(codes []string) ([]StockSnapshot, error)
	ActiveMarketIndices() []MarketIndex
	FetchIndexSnapshots(codes []string) ([]IndexSnapshot, error)
	ActivePortfolio() (Portfolio, bool)
	DictLoader() DictLoader
}
