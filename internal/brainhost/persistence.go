package brainhost

import (
	"time"

	"github.com/quantpilot/quantpilot/internal/data"
	"github.com/quantpilot/quantpilot/internal/port"
)

// PersistenceAdapter 将宿主侧 data.SQLiteManager 适配为决策脑的 port.Persistence 抽象，
// 使决策脑模块只需依赖 port 接口即可完成任务日志 / 审计日志 / CIO 决策落库。
type PersistenceAdapter struct {
	db *data.SQLiteManager
}

// NewPersistenceAdapter 构造持久化适配器。
func NewPersistenceAdapter(db *data.SQLiteManager) port.Persistence {
	return &PersistenceAdapter{db: db}
}

// --- 任务日志（委托 data.AgentTaskLogger）---

// ClearTaskLogs 清理指定日期的任务日志。
func (p *PersistenceAdapter) ClearTaskLogs(taskDate string) error {
	logger := data.NewAgentTaskLogger(p.db)
	return logger.ClearTaskLogs(taskDate)
}

// LogTaskStart 记录任务开始，返回任务日志 ID 视图。
func (p *PersistenceAdapter) LogTaskStart(taskDate, taskPhase, agentRole, taskName string, taskOrder int) *port.TaskLog {
	logger := data.NewAgentTaskLogger(p.db)
	task := logger.LogTaskStart(taskDate, taskPhase, agentRole, taskName, taskOrder)
	if task == nil {
		return nil
	}
	return &port.TaskLog{ID: task.ID}
}

// LogTaskComplete 记录任务完成。
func (p *PersistenceAdapter) LogTaskComplete(taskID uint, deliverableType, deliverableName string, deliverableData interface{}, summary string) {
	logger := data.NewAgentTaskLogger(p.db)
	logger.LogTaskComplete(taskID, deliverableType, deliverableName, deliverableData, summary)
}

// LogTaskFailed 记录任务失败。
func (p *PersistenceAdapter) LogTaskFailed(taskID uint, errMsg string) {
	logger := data.NewAgentTaskLogger(p.db)
	logger.LogTaskFailed(taskID, errMsg)
}

// --- 审计日志（写 data.AuditLog）---

// WriteAudit 写入一条审计日志。
func (p *PersistenceAdapter) WriteAudit(eventID, eventType, userID, userName, action, targetType, targetID, result, detailsJSON string, ts time.Time) error {
	if p.db == nil || p.db.GetDB() == nil {
		return nil
	}
	audit := data.AuditLog{
		EventID:     eventID,
		EventType:   eventType,
		UserID:      userID,
		UserName:    userName,
		Action:      action,
		TargetType:  targetType,
		TargetID:    targetID,
		Result:      result,
		DetailsJSON: detailsJSON,
		Timestamp:   ts,
	}
	return p.db.GetDB().Create(&audit).Error
}

// --- CIO 决策落库（写 data.CIODecisionLog，DecisionID 唯一去重）---

// SaveCIODecision 保存一条 CIO 决策记录。DecisionID 已存在时静默跳过（保持去重语义）。
func (p *PersistenceAdapter) SaveCIODecision(decisionID, portfolioID, decision, reason, riskApproval, policyStatus, marketState string, marketConfidence float64, ts time.Time) error {
	if p.db == nil || p.db.GetDB() == nil {
		return nil
	}

	entry := data.CIODecisionLog{
		DecisionID:       decisionID,
		PortfolioID:      portfolioID,
		Decision:         decision,
		Reason:           reason,
		RiskApproval:     riskApproval,
		PolicyStatus:     policyStatus,
		MarketState:      marketState,
		MarketConfidence: marketConfidence,
		Timestamp:        ts,
	}

	var count int64
	if err := p.db.GetDB().Model(&data.CIODecisionLog{}).
		Where("decision_id = ?", entry.DecisionID).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}

	return p.db.GetDB().Create(&entry).Error
}
