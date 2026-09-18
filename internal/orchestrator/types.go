package orchestrator

import (
	"time"
)

// TaskStatus 统一任务状态（5状态模型）
type TaskStatus string

const (
	StatusPending  TaskStatus = "PENDING"
	StatusRunning  TaskStatus = "RUNNING"
	StatusSuccess  TaskStatus = "SUCCESS"
	StatusFailed   TaskStatus = "FAILED"
	StatusSkipped  TaskStatus = "SKIPPED"
)

// Priority 优先级
type Priority string

const (
	PriorityLow     Priority = "LOW"
	PriorityNormal  Priority = "NORMAL"
	PriorityHigh    Priority = "HIGH"
	PriorityCritical Priority = "CRITICAL"
)

// DecisionObject 决策对象（Trader只执行此对象）
type DecisionObject struct {
	Decision      string   `json:"decision"`
	Confidence    float64  `json:"confidence"`
	RiskLevel     string   `json:"risk_level"`
	PositionLimit float64  `json:"position_limit"`
	Reason        string   `json:"reason"`
	ValidUntil    string   `json:"valid_until"`
	ApprovedBy    string   `json:"approved_by"`
	RiskChecked   bool     `json:"risk_checked"`
	Signals       []string `json:"signals"`
	TargetStocks  []string `json:"target_stocks"`
}

// VetoResult 风险否决结果
type VetoResult struct {
	Vetoed      bool   `json:"vetoed"`
	Reason      string `json:"reason"`
	RiskLevel   string `json:"risk_level"`
	MaxPosition float64 `json:"max_position"`
}

// TaskStep 任务步骤
type TaskStep struct {
	ID        string           `json:"id"`
	Name      string           `json:"name"`
	Assignee  string           `json:"assignee"`
	Status    TaskStatus       `json:"status"`
	Input     interface{}      `json:"input"`
	Output    interface{}      `json:"output"`
	Error     string           `json:"error"`
	StartedAt *time.Time       `json:"started_at,omitempty"`
	EndedAt   *time.Time       `json:"ended_at,omitempty"`
}

// TaskStepTemplate 任务步骤模板
type TaskStepTemplate struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Assignee    string `json:"assignee"`
	Required    bool   `json:"required"`
	CanVeto     bool   `json:"can_veto"`     // 是否可以否决
	CanPropose  bool   `json:"can_propose"`  // 是否可以提出方案
	CanExecute  bool   `json:"can_execute"`  // 是否可以执行
	CanApprove  bool   `json:"can_approve"`  // 是否可以批准
	RolePermission string `json:"role_permission"` // PROPOSE/EVIDENCE/VETO/DECIDE/EXECUTE
}

// TaskTemplate 任务模板（定义工作流）
type TaskTemplate struct {
	ID          string             `json:"id"`
	Name        string             `json:"name"`
	Description string             `json:"description"`
	WorkflowType string            `json:"workflow_type"` // DAILY_INVESTMENT/DAILY_REVIEW/RESEARCH/EVENT
	Steps       []TaskStepTemplate `json:"steps"`
}

// WorkflowTask 工作流任务实例
type WorkflowTask struct {
	ID           string           `json:"id"`
	TemplateID   string           `json:"template_id"`
	Name         string           `json:"name"`
	Status       TaskStatus       `json:"status"`
	CurrentStep  int              `json:"current_step"`
	Steps        []TaskStep       `json:"steps"`
	CreatedBy    string           `json:"created_by"`
	CreatedAt    time.Time        `json:"created_at"`
	UpdatedAt    time.Time        `json:"updated_at"`
	CompletedAt  *time.Time       `json:"completed_at,omitempty"`
	Decision     *DecisionObject  `json:"decision,omitempty"`
	VetoResult   *VetoResult      `json:"veto_result,omitempty"`
	IsEventDriven bool           `json:"is_event_driven"`
	EventData    interface{}      `json:"event_data,omitempty"`
}
