package cio

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/quantpilot/quantpilot/internal/data"
	"github.com/quantpilot/quantpilot/internal/port"
)

// WorkflowState 决策工作流状态
type WorkflowState string

const (
	// 工作流状态
	StatePending   WorkflowState = "PENDING"   // 待审批
	StateApproved  WorkflowState = "APPROVED"  // 已批准
	StateRejected  WorkflowState = "REJECTED"  // 已否决
	StateExecuting WorkflowState = "EXECUTING" // 执行中
	StateExecuted  WorkflowState = "EXECUTED"  // 已执行
	StateFailed    WorkflowState = "FAILED"    // 执行失败
	StateCompleted WorkflowState = "COMPLETED" // 已完成
)

// WorkflowApproval 审批记录
type WorkflowApproval struct {
	Approver  string    `json:"approver"`  // 审批人（agent ID）
	Role      string    `json:"role"`      // 审批人角色
	Approved  bool      `json:"approved"`  // 是否批准
	Comment   string    `json:"comment"`   // 审批意见
	Timestamp time.Time `json:"timestamp"` // 审批时间
}

// WorkflowExecution 执行记录
type WorkflowExecution struct {
	Executor  string    `json:"executor"`  // 执行人（agent ID）
	Status    string    `json:"status"`    // 执行状态
	Result    string    `json:"result"`    // 执行结果
	Error     string    `json:"error"`     // 错误信息
	Timestamp time.Time `json:"timestamp"` // 执行时间
}

// DecisionWorkflow 决策工作流
type DecisionWorkflow struct {
	Decision   *port.CIODecision   // 决策
	State      WorkflowState       // 当前状态
	Approvals  []WorkflowApproval  // 审批记录
	Executions []WorkflowExecution // 执行记录
	CreatedAt  time.Time           // 创建时间
	UpdatedAt  time.Time           // 更新时间
}

// WorkflowEngine 工作流引擎
type WorkflowEngine struct {
	db    *data.SQLiteManager
	flows map[string]*DecisionWorkflow // decisionID -> workflow
}

// NewWorkflowEngine 创建工作流引擎
func NewWorkflowEngine(db *data.SQLiteManager) *WorkflowEngine {
	return &WorkflowEngine{
		db:    db,
		flows: make(map[string]*DecisionWorkflow),
	}
}

// CreateWorkflow 创建决策工作流
func (w *WorkflowEngine) CreateWorkflow(decision *port.CIODecision) *DecisionWorkflow {
	flow := &DecisionWorkflow{
		Decision:   decision,
		State:      StatePending,
		Approvals:  make([]WorkflowApproval, 0),
		Executions: make([]WorkflowExecution, 0),
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	w.flows[decision.DecisionID] = flow
	log.Printf("[Workflow] Created workflow for decision: %s", decision.DecisionID)
	return flow
}

// GetWorkflow 获取决策工作流
func (w *WorkflowEngine) GetWorkflow(decisionID string) *DecisionWorkflow {
	return w.flows[decisionID]
}

// Approve 审批决策
func (w *WorkflowEngine) Approve(decisionID string, approver string, role string, approved bool, comment string) error {
	flow, exists := w.flows[decisionID]
	if !exists {
		return fmt.Errorf("workflow not found: %s", decisionID)
	}

	if flow.State != StatePending {
		return fmt.Errorf("workflow is not in pending state: %s", flow.State)
	}

	approval := WorkflowApproval{
		Approver:  approver,
		Role:      role,
		Approved:  approved,
		Comment:   comment,
		Timestamp: time.Now(),
	}
	flow.Approvals = append(flow.Approvals, approval)

	if approved {
		flow.State = StateApproved
		log.Printf("[Workflow] Decision %s approved by %s (%s): %s", decisionID, approver, role, comment)
	} else {
		flow.State = StateRejected
		log.Printf("[Workflow] Decision %s rejected by %s (%s): %s", decisionID, approver, role, comment)
	}
	flow.UpdatedAt = time.Now()

	return nil
}

// Execute 执行决策
func (w *WorkflowEngine) Execute(decisionID string, executor string) error {
	flow, exists := w.flows[decisionID]
	if !exists {
		return fmt.Errorf("workflow not found: %s", decisionID)
	}

	if flow.State != StateApproved {
		return fmt.Errorf("workflow is not in approved state: %s", flow.State)
	}

	flow.State = StateExecuting
	log.Printf("[Workflow] Decision %s executing by %s", decisionID, executor)

	return nil
}

// CompleteExecution 完成执行
func (w *WorkflowEngine) CompleteExecution(decisionID string, success bool, result string, errMsg string) error {
	flow, exists := w.flows[decisionID]
	if !exists {
		return fmt.Errorf("workflow not found: %s", decisionID)
	}

	execution := WorkflowExecution{
		Executor:  "trader_001",
		Status:    "SUCCESS",
		Result:    result,
		Error:     errMsg,
		Timestamp: time.Now(),
	}
	if !success {
		execution.Status = "FAILED"
		flow.State = StateFailed
	} else {
		flow.State = StateExecuted
	}

	flow.Executions = append(flow.Executions, execution)
	flow.UpdatedAt = time.Now()

	if success {
		log.Printf("[Workflow] Decision %s executed successfully: %s", decisionID, result)
	} else {
		log.Printf("[Workflow] Decision %s execution failed: %s", decisionID, errMsg)
	}

	return nil
}

// IsApproved 检查是否已批准
func (w *WorkflowEngine) IsApproved(decisionID string) bool {
	flow, exists := w.flows[decisionID]
	if !exists {
		return false
	}
	return flow.State == StateApproved || flow.State == StateExecuting || flow.State == StateExecuted
}

// CanExecute 检查是否可以执行
func (w *WorkflowEngine) CanExecute(decisionID string) bool {
	flow, exists := w.flows[decisionID]
	if !exists {
		return false
	}
	return flow.State == StateApproved
}

// GetPendingFlows 获取待审批的工作流
func (w *WorkflowEngine) GetPendingFlows() []*DecisionWorkflow {
	pending := make([]*DecisionWorkflow, 0)
	for _, flow := range w.flows {
		if flow.State == StatePending {
			pending = append(pending, flow)
		}
	}
	return pending
}

// GetFlowHistory 获取工作流历史
func (w *WorkflowEngine) GetFlowHistory(decisionID string) map[string]interface{} {
	flow, exists := w.flows[decisionID]
	if !exists {
		return nil
	}

	approvals := make([]map[string]interface{}, 0, len(flow.Approvals))
	for _, a := range flow.Approvals {
		approvals = append(approvals, map[string]interface{}{
			"approver":  a.Approver,
			"role":      a.Role,
			"approved":  a.Approved,
			"comment":   a.Comment,
			"timestamp": a.Timestamp.Format(time.RFC3339),
		})
	}

	executions := make([]map[string]interface{}, 0, len(flow.Executions))
	for _, e := range flow.Executions {
		executions = append(executions, map[string]interface{}{
			"executor":  e.Executor,
			"status":    e.Status,
			"result":    e.Result,
			"error":     e.Error,
			"timestamp": e.Timestamp.Format(time.RFC3339),
		})
	}

	return map[string]interface{}{
		"decision_id":  flow.Decision.DecisionID,
		"state":        string(flow.State),
		"decision":     string(flow.Decision.Decision),
		"reason":       flow.Decision.Reason,
		"market_state": flow.Decision.MarketState,
		"confidence":   flow.Decision.MarketConfidence,
		"created_at":   flow.CreatedAt.Format(time.RFC3339),
		"updated_at":   flow.UpdatedAt.Format(time.RFC3339),
		"approvals":    approvals,
		"executions":   executions,
	}
}

// SaveWorkflowToDB 保存工作流到数据库
func (w *WorkflowEngine) SaveWorkflowToDB(ctx context.Context, flow *DecisionWorkflow) error {
	if w.db == nil {
		return nil
	}

	// 保存审批记录
	for _, approval := range flow.Approvals {
		approvalLog := data.CIODecisionLog{
			DecisionID:   flow.Decision.DecisionID,
			Decision:     fmt.Sprintf("APPROVAL_%s", map[bool]string{true: "APPROVED", false: "REJECTED"}[approval.Approved]),
			Reason:       approval.Comment,
			RiskApproval: approval.Role,
			Timestamp:    approval.Timestamp,
		}
		if err := w.db.GetDB().Create(&approvalLog).Error; err != nil {
			log.Printf("[Workflow] Failed to save approval log: %v", err)
		}
	}

	return nil
}

// DecisionObject 决策对象（Trader只执行此对象）
type DecisionObject struct {
	Decision      string   `json:"decision"`       // HOLD/BUY/SELL/REDUCE
	Confidence    float64  `json:"confidence"`     // 0.0-1.0
	RiskLevel     string   `json:"risk_level"`     // LOW/MEDIUM/HIGH
	PositionLimit float64  `json:"position_limit"` // 最大仓位比例
	Reason        string   `json:"reason"`
	ValidUntil    string   `json:"valid_until"`
	ApprovedBy    string   `json:"approved_by"`
	RiskChecked   bool     `json:"risk_checked"`
	Signals       []string `json:"signals"`
	TargetStocks  []string `json:"target_stocks"`
	DecisionID    string   `json:"decision_id"`
}

// GenerateDecisionObject 从CIO决策生成DecisionObject
func (w *WorkflowEngine) GenerateDecisionObject(decision *port.CIODecision, riskChecked bool) *DecisionObject {
	// 从Orders提取目标股票列表
	var targetStocks []string
	for _, order := range decision.Orders {
		targetStocks = append(targetStocks, order.Symbol)
	}

	obj := &DecisionObject{
		Decision:      string(decision.Decision),
		Confidence:    decision.MarketConfidence,
		RiskLevel:     w.calculateRiskLevel(decision),
		PositionLimit: w.calculatePositionLimit(decision),
		Reason:        decision.Reason,
		ValidUntil:    time.Now().Add(24 * time.Hour).Format(time.RFC3339),
		ApprovedBy:    "CIO",
		RiskChecked:   riskChecked,
		Signals:       []string{},
		TargetStocks:  targetStocks,
		DecisionID:    decision.DecisionID,
	}
	log.Printf("[Workflow] Generated DecisionObject for decision %s: %s (confidence=%.2f, targets=%d)",
		decision.DecisionID, obj.Decision, obj.Confidence, len(targetStocks))
	return obj
}

func (w *WorkflowEngine) calculateRiskLevel(decision *port.CIODecision) string {
	if decision.MarketConfidence < 0.3 {
		return "HIGH"
	} else if decision.MarketConfidence < 0.6 {
		return "MEDIUM"
	}
	return "LOW"
}

func (w *WorkflowEngine) calculatePositionLimit(decision *port.CIODecision) float64 {
	switch decision.Decision {
	case "BUY":
		return 0.5
	case "SELL":
		return 0.3
	case "REDUCE":
		return 0.2
	default:
		return 0.3
	}
}

// ValidateDecisionObject 验证DecisionObject
func (w *WorkflowEngine) ValidateDecisionObject(obj *DecisionObject) error {
	if obj == nil {
		return fmt.Errorf("DecisionObject is nil")
	}
	if obj.Decision == "" {
		return fmt.Errorf("DecisionObject.Decision is empty")
	}
	if obj.ApprovedBy == "" {
		return fmt.Errorf("DecisionObject.ApprovedBy is empty")
	}
	if !obj.RiskChecked {
		return fmt.Errorf("DecisionObject.RiskChecked is false, must be reviewed by Risk first")
	}
	if obj.Confidence < 0 || obj.Confidence > 1 {
		return fmt.Errorf("DecisionObject.Confidence must be between 0 and 1")
	}
	validDecisions := map[string]bool{"HOLD": true, "BUY": true, "SELL": true, "REDUCE": true}
	if !validDecisions[obj.Decision] {
		return fmt.Errorf("DecisionObject.Decision must be one of HOLD/BUY/SELL/REDUCE")
	}
	return nil
}

// GetWorkflowStats 获取工作流统计
func (w *WorkflowEngine) GetWorkflowStats() map[string]interface{} {
	stats := map[string]interface{}{
		"total":    len(w.flows),
		"pending":  0,
		"approved": 0,
		"rejected": 0,
		"executed": 0,
		"failed":   0,
	}

	for _, flow := range w.flows {
		switch flow.State {
		case StatePending:
			stats["pending"] = stats["pending"].(int) + 1
		case StateApproved:
			stats["approved"] = stats["approved"].(int) + 1
		case StateRejected:
			stats["rejected"] = stats["rejected"].(int) + 1
		case StateExecuted:
			stats["executed"] = stats["executed"].(int) + 1
		case StateFailed:
			stats["failed"] = stats["failed"].(int) + 1
		}
	}

	return stats
}
