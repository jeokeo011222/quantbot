package brainhost

import (
	"errors"
	"time"

	"github.com/quantpilot/quantpilot/internal/data"
	"github.com/quantpilot/quantpilot/internal/port"
	"gorm.io/gorm"
)

// plannerStoreAdapter 将宿主 internal/data.SQLiteManager 适配为决策脑抽象 port.PlannerStore。
// 内部做 data ↔ port 同名实体的字段双向映射，使决策脑规划模块不反向依赖宿主数据层。
type plannerStoreAdapter struct {
	sm *data.SQLiteManager
}

// AdaptPlannerStore 将 data.SQLiteManager 包装为 port.PlannerStore。
func AdaptPlannerStore(sm *data.SQLiteManager) port.PlannerStore {
	return &plannerStoreAdapter{sm: sm}
}

// ---------- 画像 ----------

func (a *plannerStoreAdapter) GetOrCreateProfile(userID uint) (*port.InvestorProfile, error) {
	var p data.InvestorProfile
	err := a.sm.GetDB().Where("user_id = ?", userID).First(&p).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		p = data.InvestorProfile{
			UserID:           userID,
			Capital:          100000,
			Currency:         "CNY",
			MarketPreference: "CN",
			CurrentStep:      "WELCOME",
		}
		if err := a.sm.GetDB().Create(&p).Error; err != nil {
			return nil, err
		}
	}
	return dataToPortProfile(&p), nil
}

func (a *plannerStoreAdapter) UpdateProfile(profile *port.InvestorProfile) error {
	d := portToDataProfile(profile)
	if d.ID == 0 {
		// 未落库的画像按 user_id 更新
		return a.sm.GetDB().Model(&data.InvestorProfile{}).Where("user_id = ?", profile.UserID).Updates(map[string]interface{}{
			"profile_json":          d.ProfileJSON,
			"capital":               d.Capital,
			"currency":              d.Currency,
			"investment_experience": d.InvestmentExperience,
			"investment_style":      d.InvestmentStyle,
			"investment_horizon":    d.InvestmentHorizon,
			"investment_objective":  d.InvestmentObjective,
			"risk_tolerance":        d.RiskTolerance,
			"drawdown_tolerance":    d.DrawdownTolerance,
			"loss_tolerance":        d.LossTolerance,
			"liquidity_requirement": d.LiquidityRequirement,
			"trading_frequency":     d.TradingFrequency,
			"market_preference":     d.MarketPreference,
			"profile_completeness":  d.ProfileCompleteness,
			"current_step":          d.CurrentStep,
			"step_progress":         d.StepProgress,
			"updated_at":            d.UpdatedAt,
		}).Error
	}
	return a.sm.GetDB().Save(d).Error
}

// ---------- 对话 ----------

func (a *plannerStoreAdapter) CreateConversation(conv *port.Conversation) error {
	return a.sm.GetDB().Create(portToDataConversation(conv)).Error
}

func (a *plannerStoreAdapter) GetActiveConversation(userID uint) (*port.Conversation, error) {
	var c data.Conversation
	err := a.sm.GetDB().Where("user_id = ? AND status = ?", userID, "active").
		Order("updated_at DESC").First(&c).Error
	if err != nil {
		return nil, err
	}
	return dataToPortConversation(&c), nil
}

func (a *plannerStoreAdapter) AddMessage(msg *port.ConversationMessage, conversationID string) error {
	d := portToDataMessage(msg)
	if err := a.sm.GetDB().Create(d).Error; err != nil {
		return err
	}
	// 同步对话的消息计数与更新时间
	return a.sm.GetDB().Model(&data.Conversation{}).
		Where("conversation_id = ?", conversationID).
		Updates(map[string]interface{}{
			"message_count": gorm.Expr("message_count + 1"),
			"updated_at":    msg.CreatedAt,
		}).Error
}

func (a *plannerStoreAdapter) UpdateConversationStep(conversationID, currentStep string) error {
	return a.sm.GetDB().Model(&data.Conversation{}).
		Where("conversation_id = ?", conversationID).
		Update("current_step", currentStep).Error
}

func (a *plannerStoreAdapter) GetConversationMessages(conversationID string) ([]port.ConversationMessage, error) {
	var rows []data.ConversationMessage
	if err := a.sm.GetDB().Where("conversation_id = ?", conversationID).
		Order("created_at ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]port.ConversationMessage, 0, len(rows))
	for i := range rows {
		out = append(out, *dataToPortMessage(&rows[i]))
	}
	return out, nil
}

func (a *plannerStoreAdapter) GetConversation(conversationID string) (*port.Conversation, error) {
	var c data.Conversation
	if err := a.sm.GetDB().Where("conversation_id = ?", conversationID).First(&c).Error; err != nil {
		return nil, err
	}
	return dataToPortConversation(&c), nil
}

// ---------- 计划 ----------

func (a *plannerStoreAdapter) CreatePlan(plan *port.InvestmentPlan) error {
	return a.sm.GetDB().Create(portToDataPlan(plan)).Error
}

func (a *plannerStoreAdapter) CreateCandidates(candidates []*port.StrategyCandidate) error {
	rows := make([]data.StrategyCandidate, 0, len(candidates))
	for _, c := range candidates {
		rows = append(rows, *portToDataCandidate(c))
	}
	return a.sm.GetDB().Create(&rows).Error
}

func (a *plannerStoreAdapter) UpdatePlanStatus(plan *port.InvestmentPlan, status string) error {
	return a.sm.GetDB().Model(&data.InvestmentPlan{}).
		Where("plan_id = ?", plan.PlanID).Update("status", status).Error
}

func (a *plannerStoreAdapter) UpdatePlanJSONByPlanID(planID, planJSON string) error {
	return a.sm.GetDB().Model(&data.InvestmentPlan{}).
		Where("plan_id = ?", planID).Update("plan_json", planJSON).Error
}

func (a *plannerStoreAdapter) ListReviewingPlans(userID uint) ([]port.InvestmentPlan, error) {
	var rows []data.InvestmentPlan
	if err := a.sm.GetDB().Where("user_id = ? AND status = ?", userID, "REVIEWING").
		Order("created_at DESC").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]port.InvestmentPlan, 0, len(rows))
	for i := range rows {
		out = append(out, *dataToPortPlan(&rows[i]))
	}
	return out, nil
}

func (a *plannerStoreAdapter) GetCurrentPlan(userID uint) (*port.InvestmentPlan, error) {
	var p data.InvestmentPlan
	err := a.sm.GetDB().Where("user_id = ? AND status = ?", userID, "ACTIVE").
		Order("created_at DESC").First(&p).Error
	if err == nil {
		return dataToPortPlan(&p), nil
	}
	// 无 ACTIVE 方案时回退到进行中的方案
	err = a.sm.GetDB().Where("user_id = ? AND status IN ?", userID, []string{"DRAFT", "GENERATING", "REVIEWING", "PENDING_APPROVAL"}).
		Order("created_at DESC").First(&p).Error
	if err != nil {
		return nil, err
	}
	return dataToPortPlan(&p), nil
}

func (a *plannerStoreAdapter) ListPlans(userID uint) ([]port.InvestmentPlan, error) {
	var rows []data.InvestmentPlan
	if err := a.sm.GetDB().Where("user_id = ?", userID).
		Order("created_at DESC").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]port.InvestmentPlan, 0, len(rows))
	for i := range rows {
		out = append(out, *dataToPortPlan(&rows[i]))
	}
	return out, nil
}

func (a *plannerStoreAdapter) GetPlan(planID string) (*port.InvestmentPlan, error) {
	var p data.InvestmentPlan
	if err := a.sm.GetDB().Where("plan_id = ?", planID).First(&p).Error; err != nil {
		return nil, err
	}
	return dataToPortPlan(&p), nil
}

func (a *plannerStoreAdapter) GetPlanCandidates(planID string) ([]port.StrategyCandidate, error) {
	var rows []data.StrategyCandidate
	if err := a.sm.GetDB().Where("plan_id = ?", planID).
		Order("label ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]port.StrategyCandidate, 0, len(rows))
	for i := range rows {
		out = append(out, *dataToPortCandidate(&rows[i]))
	}
	return out, nil
}

// ---------- 审批 ----------

// ApprovePlan 创建批准记录，强制唯一 ACTIVE：若干涉用户其他生效方案先降级为 ARCHIVED，
// 再将目标方案置为 ACTIVE（与决策脑原始 ApprovePlan 语义一致）。
func (a *plannerStoreAdapter) ApprovePlan(approval *port.PlanApproval, activatedPlanID string, userID uint) error {
	if err := a.sm.GetDB().Create(portToDataApproval(approval)).Error; err != nil {
		return err
	}
	// 将其他 ACTIVE/APPROVED/RUNNING 方案降级为 ARCHIVED
	if err := a.sm.GetDB().Model(&data.InvestmentPlan{}).
		Where("user_id = ? AND plan_id <> ? AND status IN ?", userID, activatedPlanID, []string{"ACTIVE", "APPROVED", "RUNNING"}).
		Update("status", "ARCHIVED").Error; err != nil {
		return err
	}
	return a.sm.GetDB().Model(&data.InvestmentPlan{}).
		Where("plan_id = ?", activatedPlanID).Update("status", "ACTIVE").Error
}

func (a *plannerStoreAdapter) RejectPlan(approval *port.PlanApproval, planID string) error {
	if err := a.sm.GetDB().Create(portToDataApproval(approval)).Error; err != nil {
		return err
	}
	return a.sm.GetDB().Model(&data.InvestmentPlan{}).
		Where("plan_id = ?", planID).Update("status", "REJECTED").Error
}

// ==================== data ↔ port 字段映射 ====================

func dataToPortProfile(p *data.InvestorProfile) *port.InvestorProfile {
	return &port.InvestorProfile{
		ID:                   p.ID,
		UserID:               p.UserID,
		ProfileJSON:          p.ProfileJSON,
		Capital:              p.Capital,
		Currency:             p.Currency,
		InvestmentExperience: p.InvestmentExperience,
		InvestmentStyle:      p.InvestmentStyle,
		InvestmentHorizon:    p.InvestmentHorizon,
		InvestmentObjective:  p.InvestmentObjective,
		RiskTolerance:        p.RiskTolerance,
		DrawdownTolerance:    p.DrawdownTolerance,
		LossTolerance:        p.LossTolerance,
		LiquidityRequirement: p.LiquidityRequirement,
		TradingFrequency:     p.TradingFrequency,
		MarketPreference:     p.MarketPreference,
		ProfileCompleteness:  p.ProfileCompleteness,
		CurrentStep:          p.CurrentStep,
		StepProgress:         p.StepProgress,
		CreatedAt:            p.CreatedAt,
		UpdatedAt:            p.UpdatedAt,
	}
}

func portToDataProfile(p *port.InvestorProfile) *data.InvestorProfile {
	return &data.InvestorProfile{
		ID:                   p.ID,
		UserID:               p.UserID,
		ProfileJSON:          p.ProfileJSON,
		Capital:              p.Capital,
		Currency:             p.Currency,
		InvestmentExperience: p.InvestmentExperience,
		InvestmentStyle:      p.InvestmentStyle,
		InvestmentHorizon:    p.InvestmentHorizon,
		InvestmentObjective:  p.InvestmentObjective,
		RiskTolerance:        p.RiskTolerance,
		DrawdownTolerance:    p.DrawdownTolerance,
		LossTolerance:        p.LossTolerance,
		LiquidityRequirement: p.LiquidityRequirement,
		TradingFrequency:     p.TradingFrequency,
		MarketPreference:     p.MarketPreference,
		ProfileCompleteness:  p.ProfileCompleteness,
		CurrentStep:          p.CurrentStep,
		StepProgress:         p.StepProgress,
		CreatedAt:            p.CreatedAt,
		UpdatedAt:            p.UpdatedAt,
	}
}

// DataProfileFromPort 导出画像 DTO 的 data 映射（宿主把它注入 screener 的智能选股请求）。
func DataProfileFromPort(p *port.InvestorProfile) *data.InvestorProfile {
	if p == nil {
		return nil
	}
	return portToDataProfile(p)
}

func dataToPortConversation(c *data.Conversation) *port.Conversation {
	return &port.Conversation{
		ID:             c.ID,
		ConversationID: c.ConversationID,
		UserID:         c.UserID,
		ProfileID:      c.ProfileID,
		Title:          c.Title,
		Status:         c.Status,
		CurrentStep:    c.CurrentStep,
		MessageCount:   c.MessageCount,
		StartedAt:      c.StartedAt,
		UpdatedAt:      c.UpdatedAt,
	}
}

func portToDataConversation(c *port.Conversation) *data.Conversation {
	return &data.Conversation{
		ID:             c.ID,
		ConversationID: c.ConversationID,
		UserID:         c.UserID,
		ProfileID:      c.ProfileID,
		Title:          c.Title,
		Status:         c.Status,
		CurrentStep:    c.CurrentStep,
		MessageCount:   c.MessageCount,
		StartedAt:      c.StartedAt,
		UpdatedAt:      c.UpdatedAt,
	}
}

func dataToPortMessage(m *data.ConversationMessage) *port.ConversationMessage {
	return &port.ConversationMessage{
		ID:             m.ID,
		ConversationID: m.ConversationID,
		Role:           m.Role,
		Content:        m.Content,
		MessageType:    m.MessageType,
		MetadataJSON:   m.MetadataJSON,
		CreatedAt:      m.CreatedAt,
	}
}

func portToDataMessage(m *port.ConversationMessage) *data.ConversationMessage {
	createdAt := m.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now()
	}
	return &data.ConversationMessage{
		ID:             m.ID,
		ConversationID: m.ConversationID,
		Role:           m.Role,
		Content:        m.Content,
		MessageType:    m.MessageType,
		MetadataJSON:   m.MetadataJSON,
		CreatedAt:      createdAt,
	}
}

func dataToPortPlan(p *data.InvestmentPlan) *port.InvestmentPlan {
	return &port.InvestmentPlan{
		ID:               p.ID,
		PlanID:           p.PlanID,
		UserID:           p.UserID,
		ProfileID:        p.ProfileID,
		Name:             p.Name,
		Objective:        p.Objective,
		RiskLevel:        p.RiskLevel,
		TargetReturn:     p.TargetReturn,
		TargetVolatility: p.TargetVolatility,
		MaxDrawdown:      p.MaxDrawdown,
		StrategyType:     p.StrategyType,
		Status:           p.Status,
		Version:          p.Version,
		PlanJSON:         p.PlanJSON,
		CreatedAt:        p.CreatedAt,
		UpdatedAt:        p.UpdatedAt,
	}
}

func portToDataPlan(p *port.InvestmentPlan) *data.InvestmentPlan {
	return &data.InvestmentPlan{
		ID:               p.ID,
		PlanID:           p.PlanID,
		UserID:           p.UserID,
		ProfileID:        p.ProfileID,
		Name:             p.Name,
		Objective:        p.Objective,
		RiskLevel:        p.RiskLevel,
		TargetReturn:     p.TargetReturn,
		TargetVolatility: p.TargetVolatility,
		MaxDrawdown:      p.MaxDrawdown,
		StrategyType:     p.StrategyType,
		Status:           p.Status,
		Version:          p.Version,
		PlanJSON:         p.PlanJSON,
		CreatedAt:        p.CreatedAt,
		UpdatedAt:        p.UpdatedAt,
	}
}

func dataToPortCandidate(c *data.StrategyCandidate) *port.StrategyCandidate {
	return &port.StrategyCandidate{
		ID:                 c.ID,
		CandidateID:        c.CandidateID,
		PlanID:             c.PlanID,
		Name:               c.Name,
		Description:        c.Description,
		Label:              c.Label,
		Universe:           c.Universe,
		SignalDefinition:   c.SignalDefinition,
		ExpectedReturn:     c.ExpectedReturn,
		ExpectedVolatility: c.ExpectedVolatility,
		MaxDrawdown:        c.MaxDrawdown,
		Sharpe:             c.Sharpe,
		Calmar:             c.Calmar,
		BacktestStart:      c.BacktestStart,
		BacktestEnd:        c.BacktestEnd,
		TransactionCost:    c.TransactionCost,
		Turnover:           c.Turnover,
		RobustnessScore:    c.RobustnessScore,
		RiskScore:          c.RiskScore,
		RiskOSStatus:       c.RiskOSStatus,
		Status:             c.Status,
		CandidateJSON:      c.CandidateJSON,
		CreatedAt:          c.CreatedAt,
		UpdatedAt:          c.UpdatedAt,
	}
}

func portToDataCandidate(c *port.StrategyCandidate) *data.StrategyCandidate {
	return &data.StrategyCandidate{
		ID:                 c.ID,
		CandidateID:        c.CandidateID,
		PlanID:             c.PlanID,
		Name:               c.Name,
		Description:        c.Description,
		Label:              c.Label,
		Universe:           c.Universe,
		SignalDefinition:   c.SignalDefinition,
		ExpectedReturn:     c.ExpectedReturn,
		ExpectedVolatility: c.ExpectedVolatility,
		MaxDrawdown:        c.MaxDrawdown,
		Sharpe:             c.Sharpe,
		Calmar:             c.Calmar,
		BacktestStart:      c.BacktestStart,
		BacktestEnd:        c.BacktestEnd,
		TransactionCost:    c.TransactionCost,
		Turnover:           c.Turnover,
		RobustnessScore:    c.RobustnessScore,
		RiskScore:          c.RiskScore,
		RiskOSStatus:       c.RiskOSStatus,
		Status:             c.Status,
		CandidateJSON:      c.CandidateJSON,
		CreatedAt:          c.CreatedAt,
		UpdatedAt:          c.UpdatedAt,
	}
}

func dataToPortApproval(a *data.PlanApproval) *port.PlanApproval {
	return &port.PlanApproval{
		ID:               a.ID,
		PlanID:           a.PlanID,
		UserID:           a.UserID,
		IsApproved:       a.IsApproved,
		RiskAcknowledged: a.RiskAcknowledged,
		Notes:            a.Notes,
		ApprovedAt:       a.ApprovedAt,
		CreatedAt:        a.CreatedAt,
	}
}

func portToDataApproval(a *port.PlanApproval) *data.PlanApproval {
	createdAt := a.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now()
	}
	return &data.PlanApproval{
		ID:               a.ID,
		PlanID:           a.PlanID,
		UserID:           a.UserID,
		IsApproved:       a.IsApproved,
		RiskAcknowledged: a.RiskAcknowledged,
		Notes:            a.Notes,
		ApprovedAt:       a.ApprovedAt,
		CreatedAt:        createdAt,
	}
}
