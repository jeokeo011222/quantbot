package planner

// InterviewResponse 访谈回复
type InterviewResponse struct {
	Message       string                 `json:"message"`
	IsComplete    bool                   `json:"isComplete"`
	NextQuestion  string                 `json:"nextQuestion,omitempty"`
	ProfileUpdate map[string]interface{} `json:"profileUpdate,omitempty"`
	Suggestions   []string               `json:"suggestions,omitempty"`
}

// PlanData 计划数据
type PlanData struct {
	Name             string  `json:"name"`
	Objective        string  `json:"objective"`
	RiskLevel        string  `json:"riskLevel"`
	TargetReturn     float64 `json:"targetReturn"`
	TargetVolatility float64 `json:"targetVolatility"`
	MaxDrawdown      float64 `json:"maxDrawdown"`
	StrategyType     string  `json:"strategyType"`
}

// ============ Investment Mandate 投资任务书 ============

// InvestmentMandate 投资任务书（CIO 必须遵守的投资宪法）
type InvestmentMandate struct {
	MandateID       string            `json:"mandateId"`
	Title           string            `json:"title"`
	Version         int               `json:"version"`
	InvestorProfile map[string]string `json:"investorProfile"`

	// 核心投资目标
	InvestmentObjective string     `json:"investmentObjective"` // 长期资本增长 / 稳定分红 / 平衡配置 / 资产保值
	RiskLevel           string     `json:"riskLevel"`           // conservative / balanced / growth
	TargetReturnRange   [2]float64 `json:"targetReturnRange"`   // 年化收益目标区间（如 0.08-0.12）
	TargetVolatility    float64    `json:"targetVolatility"`    // 目标波动率
	MaxDrawdown         float64    `json:"maxDrawdown"`         // 最大可接受回撤

	// 投资约束（Risk Budget）
	TotalRiskBudget float64             `json:"totalRiskBudget"` // 总风险预算 1.0
	PositionLimit   PositionConstraints `json:"positionLimit"`   // 个股/行业约束
	CashRequirement [2]float64          `json:"cashRequirement"` // 现金比例区间 [min, max]
	LeverageLimit   float64             `json:"leverageLimit"`   // 最大杠杆（A股通常为 1.0）

	// 投资范围
	MarketUniverse       []string `json:"marketUniverse"`       // 允许投资的市场（["CN_A"]）
	AllowedIndustries    []string `json:"allowedIndustries"`    // 允许的行业（空=全部）
	RestrictedIndustries []string `json:"restrictedIndustries"` // 限制的行业

	// 风格与因子
	StylePreferences StylePreferences `json:"stylePreferences"`
	RebalancePolicy  RebalancePolicy  `json:"rebalancePolicy"`

	// 生成元数据
	GeneratedAt string `json:"generatedAt"`
	GeneratedBy string `json:"generatedBy"` // "投资规划师"
}

// PositionConstraints 仓位约束
type PositionConstraints struct {
	MaxSingleStock    float64            `json:"maxSingleStock"`    // 单只股票最大权重
	MaxIndustry       map[string]float64 `json:"maxIndustry"`       // 各行业最大权重
	MaxFactorExposure map[string]float64 `json:"maxFactorExposure"` // 因子最大暴露
	HighVolLimit      float64            `json:"highVolLimit"`      // 高波动资产最大比例
}

// StylePreferences 风格偏好
type StylePreferences struct {
	PrimaryStyle        string             `json:"primaryStyle"`  // DIVIDEND/VALUE/GROWTH/QUALITY/INDEX/BALANCED
	FactorWeights       map[string]float64 `json:"factorWeights"` // 各因子权重
	PreferredIndustries []string           `json:"preferredIndustries"`
}

// RebalancePolicy 再平衡政策
type RebalancePolicy struct {
	Frequency      string  `json:"frequency"`      // MONTHLY / QUARTERLY / EVENT_DRIVEN
	MaxTurnover    float64 `json:"maxTurnover"`    // 最大换手率
	DriftThreshold float64 `json:"driftThreshold"` // 偏离阈值（触发再平衡）
}

// ============ Portfolio Construction 组合构建 ============

// PortfolioPosition 组合持仓（最终产物）
type PortfolioPosition struct {
	AssetCode       string             `json:"assetCode"`       // 股票代码（如 600519）
	AssetName       string             `json:"assetName"`       // 股票名称
	AssetType       string             `json:"assetType"`       // STOCK / ETF / BOND / CASH
	Industry        string             `json:"industry"`        // 行业
	TargetWeight    float64            `json:"targetWeight"`    // 目标权重（0-1）
	SuggestedAmount float64            `json:"suggestedAmount"` // 建议投资金额
	BaseWeight      float64            `json:"baseWeight"`      // 第一层：基础权重（等权）
	AlphaAdjust     float64            `json:"alphaAdjust"`     // 第二层：Alpha 调整
	RiskAdjust      float64            `json:"riskAdjust"`      // 第三层：风险调整
	FactorScore     float64            `json:"factorScore"`     // 综合因子得分
	RiskOSStatus    string             `json:"riskOsStatus"`    // PASSED/WARNING/BLOCKED
	Rationale       string             `json:"rationale"`       // 投资理由（极白语言）
	QuantEvidence   map[string]float64 `json:"quantEvidence"`   // 量化证据

	// A股交易可行性字段
	StockPrice   float64 `json:"stockPrice"`   // 参考股价（元）
	LotSize      int     `json:"lotSize"`      // 最小交易单位（A股=100）
	MinTradeCost float64 `json:"minTradeCost"` // 一手最低成本 = StockPrice × LotSize
	Lots         int     `json:"lots"`         // 建议买入手数
	Tradeable    bool    `json:"tradeable"`    // 是否满足一手交易约束
}

// PortfolioConstruction 组合构建过程与结果
type PortfolioConstruction struct {
	// 构建过程六步
	ProcessSteps []ConstructionStep `json:"processSteps"`

	// 最终组合
	Positions          []PortfolioPosition `json:"positions"`
	TotalAssets        float64             `json:"totalAssets"`  // 总资金
	EquityWeight       float64             `json:"equityWeight"` // 股票权重
	FundWeight         float64             `json:"fundWeight"`   // 基金(ETF)权重
	BondWeight         float64             `json:"bondWeight"`   // 债券类权重
	CashWeight         float64             `json:"cashWeight"`   // 现金权重
	IndustryDist       map[string]float64  `json:"industryDist"` // 行业分布
	ExpectedReturn     float64             `json:"expectedReturn"`
	ExpectedVolatility float64             `json:"expectedVolatility"`
	MaxDrawdown        float64             `json:"maxDrawdown"`
	SharpeRatio        float64             `json:"sharpeRatio"`

	// 状态
	Status      string `json:"status"` // GENERATING / READY / APPROVED
	GeneratedAt string `json:"generatedAt"`
}

// ConstructionStep 构建过程单步
type ConstructionStep struct {
	StepNo    int    `json:"stepNo"`
	Title     string `json:"title"`     // "理解你的投资目标"
	AgentRole string `json:"agentRole"` // 投资规划师 / Quant / Risk / CIO
	Status    string `json:"status"`    // PENDING / RUNNING / DONE / FAILED
	Message   string `json:"message"`   // 进度消息
	Detail    string `json:"detail"`    // 细节（如"已分析4,862支股票"）
}

// CandidateForPortfolio 候选资产（Quant 筛选结果）
type CandidateForPortfolio struct {
	AssetCode      string  `json:"assetCode"`
	AssetName      string  `json:"assetName"`
	AssetType      string  `json:"assetType"` // STOCK / ETF / BOND（来自股票池定义）
	Industry       string  `json:"industry"`
	FactorScore    float64 `json:"factorScore"`    // 综合因子分
	AlphaScore     float64 `json:"alphaScore"`     // Alpha 得分
	RiskScore      float64 `json:"riskScore"`      // 风险调整分
	LiquidityScore float64 `json:"liquidityScore"` // 流动性
	RiskOSStatus   string  `json:"riskOsStatus"`
	Reason         string  `json:"reason"` // 入选理由
}

// ============ 其他响应类型 ============

// ProfileResponse 画像响应
type ProfileResponse struct {
	ID                   uint        `json:"id"`
	Capital              float64     `json:"capital"`
	Currency             string      `json:"currency"`
	InvestmentExperience string      `json:"investmentExperience"`
	InvestmentStyle      string      `json:"investmentStyle"`
	InvestmentHorizon    string      `json:"investmentHorizon"`
	InvestmentObjective  string      `json:"investmentObjective"`
	RiskTolerance        string      `json:"riskTolerance"`
	DrawdownTolerance    float64     `json:"drawdownTolerance"`
	LossTolerance        float64     `json:"lossTolerance"`
	LiquidityRequirement string      `json:"liquidityRequirement"`
	TradingFrequency     string      `json:"tradingFrequency"`
	MarketPreference     string      `json:"marketPreference"`
	ProfileCompleteness  float64     `json:"profileCompleteness"`
	CurrentStep          string      `json:"currentStep"`
	ProfileJSON          interface{} `json:"profile"`
}

// ConversationResponse 对话响应
type ConversationResponse struct {
	ID             uint              `json:"id"`
	ConversationID string            `json:"conversationId"`
	Title          string            `json:"title"`
	Status         string            `json:"status"`
	CurrentStep    string            `json:"currentStep"`
	MessageCount   int               `json:"messageCount"`
	Messages       []MessageResponse `json:"messages,omitempty"`
}

// MessageResponse 消息响应
type MessageResponse struct {
	ID          uint   `json:"id"`
	Role        string `json:"role"`
	Content     string `json:"content"`
	MessageType string `json:"messageType"`
	CreatedAt   string `json:"createdAt"`
}

// PlanResponse 计划响应（含 Mandate 和 Construction）
type PlanResponse struct {
	ID               uint    `json:"id"`
	PlanID           string  `json:"planId"`
	Name             string  `json:"name"`
	Objective        string  `json:"objective"`
	RiskLevel        string  `json:"riskLevel"`
	TargetReturn     float64 `json:"targetReturn"`
	TargetVolatility float64 `json:"targetVolatility"`
	MaxDrawdown      float64 `json:"maxDrawdown"`
	StrategyType     string  `json:"strategyType"`
	Status           string  `json:"status"`

	// 新增：投资任务书与组合
	Mandate      *InvestmentMandate     `json:"mandate,omitempty"`
	Construction *PortfolioConstruction `json:"construction,omitempty"`

	Candidates []CandidateResponse `json:"candidates,omitempty"`
	CreatedAt  string              `json:"createdAt"`
}

// CandidateResponse 候选策略响应
type CandidateResponse struct {
	ID                 uint    `json:"id"`
	CandidateID        string  `json:"candidateId"`
	Name               string  `json:"name"`
	Label              string  `json:"label"`
	Description        string  `json:"description"`
	ExpectedReturn     float64 `json:"expectedReturn"`
	ExpectedVolatility float64 `json:"expectedVolatility"`
	MaxDrawdown        float64 `json:"maxDrawdown"`
	Sharpe             float64 `json:"sharpe"`
	Calmar             float64 `json:"calmar"`
	RobustnessScore    float64 `json:"robustnessScore"`
	RiskScore          float64 `json:"riskScore"`
	RiskOSStatus       string  `json:"riskOsStatus"`
	Status             string  `json:"status"`
}

// PlannerState 规划状态
type PlannerState struct {
	Step                string  `json:"step"`
	Progress            int     `json:"progress"`
	ProfileCompleteness float64 `json:"profileCompleteness"`
	HasActivePlan       bool    `json:"hasActivePlan"`
	HasMandate          bool    `json:"hasMandate"`
	HasPortfolio        bool    `json:"hasPortfolio"`
}
