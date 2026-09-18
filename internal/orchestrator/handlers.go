package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/quantpilot/quantpilot/internal/port"
)

// PlannerHandler Planner智能体处理器 - 权限: PROPOSE（提出方案）
// DLL 隔离：进程内 Agent 决策已迁入 agent.dll，宿主持有 port.Agent 数据侧仅作占位，
// Handle 产出 DLL-only 标记，工作流保持可推进。
type PlannerHandler struct {
	orchestrator *Orchestrator
	agent        *port.Agent
}

func NewPlannerHandler(o *Orchestrator, agent *port.Agent) *PlannerHandler {
	return &PlannerHandler{orchestrator: o, agent: agent}
}

func (h *PlannerHandler) GetRole() string               { return "planner" }
func (h *PlannerHandler) GetPermission() RolePermission { return PermPropose }

func (h *PlannerHandler) Handle(ctx context.Context, task *WorkflowTask, step *TaskStep) error {
	log.Printf("[Planner] PROPOSE: %s (task: %s) → DLL-only", step.Name, task.ID)

	h.orchestrator.setStepOutput(task.ID, step.ID, map[string]interface{}{
		"role":         "PLANNER",
		"permission":   "PROPOSE",
		"plan":         map[string]interface{}{"status": "DLL_ONLY", "note": "进程内 Agent 决策已迁入 agent.dll"},
		"has_proposed": true,
		"generated_at": time.Now().Format(time.RFC3339),
	})

	log.Printf("[Planner] PROPOSE completed: %s", step.ID)
	return nil
}

// InvestmentPlanGenerator 投资方案生成回调（由 App 层注入，避免 orchestrator 反向依赖 App）。
// 返回结构化结果作为步骤输出；返回 error 时该步骤标记失败。
type InvestmentPlanGenerator func(ctx context.Context, task *WorkflowTask, step *TaskStep) (interface{}, error)

// InvestmentPlanHandler 投资方案规划处理器 - 角色: planner，权限: PROPOSE
// 处理"投资方案规划"步骤时调用注入的生成器生成投资方案；其他 planner 步骤委托默认 PlannerHandler。
type InvestmentPlanHandler struct {
	orchestrator *Orchestrator
	fallback     TaskHandler // 默认 planner handler（盘前市场分析等）
	generator    InvestmentPlanGenerator
}

func NewInvestmentPlanHandler(o *Orchestrator, fallback TaskHandler, gen InvestmentPlanGenerator) *InvestmentPlanHandler {
	return &InvestmentPlanHandler{orchestrator: o, fallback: fallback, generator: gen}
}

func (h *InvestmentPlanHandler) GetRole() string               { return "planner" }
func (h *InvestmentPlanHandler) GetPermission() RolePermission { return PermPropose }

func (h *InvestmentPlanHandler) Handle(ctx context.Context, task *WorkflowTask, step *TaskStep) error {
	log.Printf("[InvestmentPlan] PROPOSE: %s (task: %s)", step.Name, task.ID)

	if step.Name == "投资方案规划" {
		if h.generator == nil {
			return fmt.Errorf("investment plan generator not configured")
		}
		output, err := h.generator(ctx, task, step)
		if err != nil {
			return err
		}
		h.orchestrator.setStepOutput(task.ID, step.ID, output)
		log.Printf("[InvestmentPlan] 投资方案生成完成: %s", step.ID)
		return nil
	}

	// 其他 planner 步骤委托默认处理（如盘前市场分析）
	if h.fallback != nil {
		return h.fallback.Handle(ctx, task, step)
	}
	return fmt.Errorf("planner fallback handler not configured")
}

// QuantHandler Quant智能体处理器 - 权限: EVIDENCE（提供证据）
type QuantHandler struct {
	orchestrator *Orchestrator
	agent        *port.Agent
}

func NewQuantHandler(o *Orchestrator, agent *port.Agent) *QuantHandler {
	return &QuantHandler{orchestrator: o, agent: agent}
}

func (h *QuantHandler) GetRole() string               { return "quant" }
func (h *QuantHandler) GetPermission() RolePermission { return PermEvidence }

func (h *QuantHandler) Handle(ctx context.Context, task *WorkflowTask, step *TaskStep) error {
	log.Printf("[Quant] EVIDENCE: %s (task: %s) → DLL-only", step.Name, task.ID)

	h.orchestrator.setStepOutput(task.ID, step.ID, map[string]interface{}{
		"role":         "QUANT",
		"permission":   "EVIDENCE",
		"signals":      map[string]interface{}{"status": "DLL_ONLY", "note": "进程内 Agent 决策已迁入 agent.dll"},
		"has_evidence": true,
		"generated_at": time.Now().Format(time.RFC3339),
	})

	log.Printf("[Quant] EVIDENCE completed: %s", step.ID)
	return nil
}

// RiskHandler Risk智能体处理器 - 权限: VETO（可以否决）
type RiskHandler struct {
	orchestrator *Orchestrator
	agent        *port.Agent
}

func NewRiskHandler(o *Orchestrator, agent *port.Agent) *RiskHandler {
	return &RiskHandler{orchestrator: o, agent: agent}
}

func (h *RiskHandler) GetRole() string               { return "risk" }
func (h *RiskHandler) GetPermission() RolePermission { return PermVeto }

func (h *RiskHandler) Handle(ctx context.Context, task *WorkflowTask, step *TaskStep) error {
	log.Printf("[Risk] VETO: %s (task: %s) → DLL-only(不否决)", step.Name, task.ID)

	h.orchestrator.setStepOutput(task.ID, step.ID, map[string]interface{}{
		"role":         "RISK",
		"permission":   "VETO",
		"risk_report":  map[string]interface{}{"status": "DLL_ONLY", "note": "进程内 Agent 决策已迁入 agent.dll"},
		"can_veto":     true,
		"vetoed":       false,
		"generated_at": time.Now().Format(time.RFC3339),
	})

	log.Printf("[Risk] VETO check completed: %s", step.ID)
	return nil
}

// parseRiskVeto 解析 AI Risk 报告（JSON字符串），当 decision==REJECT 时返回 VetoResult(Vetoed=true)。
// 解析失败或非 REJECT 返回 nil（表示未否决）。预留供 DLL 决策结果解码使用。
func parseRiskVeto(ctx interface{}) *VetoResult {
	s, ok := ctx.(string)
	if !ok || s == "" {
		return nil
	}
	var r struct {
		Decision   string   `json:"decision"`
		RiskNotes  string   `json:"risk_notes"`
		Violations []string `json:"violations"`
		RiskLevel  string   `json:"risk_level"`
	}
	if err := json.Unmarshal([]byte(s), &r); err != nil {
		return nil
	}
	if !strings.EqualFold(r.Decision, "REJECT") {
		return nil
	}
	reason := strings.TrimSpace(r.RiskNotes)
	if len(r.Violations) > 0 {
		reason = strings.Join(r.Violations, "; ")
	}
	if reason == "" {
		reason = "AI 风控决策：REJECT"
	}
	level := r.RiskLevel
	if level == "" {
		level = "HIGH"
	}
	return &VetoResult{
		Vetoed:    true,
		Reason:    reason,
		RiskLevel: level,
	}
}

// CIOHandler CIO智能体处理器 - 权限: DECIDE（最终决定）
type CIOHandler struct {
	orchestrator *Orchestrator
	agent        *port.Agent
}

func NewCIOHandler(o *Orchestrator, agent *port.Agent) *CIOHandler {
	return &CIOHandler{orchestrator: o, agent: agent}
}

func (h *CIOHandler) GetRole() string               { return "cio" }
func (h *CIOHandler) GetPermission() RolePermission { return PermDecide }

func (h *CIOHandler) Handle(ctx context.Context, task *WorkflowTask, step *TaskStep) error {
	log.Printf("[CIO] DECIDE: %s (task: %s) → DLL-only(默认 HOLD)", step.Name, task.ID)

	// DLL 隔离：进程内 CIO 决策已迁入 agent.dll；宿主默认安全 HOLD，避免误交易。
	decision := &DecisionObject{
		Decision:      "HOLD",
		Confidence:    0.50,
		RiskLevel:     "MEDIUM",
		PositionLimit: 0.30,
		Reason:        fmt.Sprintf("进程内 CIO 决策已迁入 agent.dll(DLL-only)，默认 HOLD（task %s）", task.ID),
		ValidUntil:    time.Now().Add(24 * time.Hour).Format(time.RFC3339),
		ApprovedBy:    "CIO",
		RiskChecked:   true,
		Signals:       []string{},
		TargetStocks:  []string{},
	}

	h.orchestrator.setStepOutput(task.ID, step.ID, decision)
	log.Printf("[CIO] DECIDE completed: %s, decision=%s", step.ID, decision.Decision)
	return nil
}

// parseCIODecision 解析 AI CIO 报告（JSON字符串），映射到 DecisionObject。
// 解析失败或字段缺失时返回 nil（调用方回退）。预留供 DLL 决策结果解码使用。
func parseCIODecision(ctx interface{}) *DecisionObject {
	s, ok := ctx.(string)
	if !ok || s == "" {
		return nil
	}
	var r struct {
		Decision       string  `json:"decision"`
		Reason         string  `json:"reason"`
		Confidence     float64 `json:"confidence"`
		TargetPosition float64 `json:"target_position"`
		Notes          string  `json:"notes"`
	}
	if err := json.Unmarshal([]byte(s), &r); err != nil {
		return nil
	}
	if strings.TrimSpace(r.Decision) == "" {
		return nil
	}
	reason := strings.TrimSpace(r.Reason)
	if reason == "" {
		reason = strings.TrimSpace(r.Notes)
	}
	confidence := r.Confidence
	if confidence <= 0 || confidence > 1 {
		confidence = 0.5
	}
	positionLimit := r.TargetPosition
	if positionLimit < 0 {
		positionLimit = 0
	}
	return &DecisionObject{
		Decision:      strings.ToUpper(strings.TrimSpace(r.Decision)),
		Confidence:    confidence,
		RiskLevel:     "MEDIUM",
		PositionLimit: positionLimit,
		Reason:        reason,
		ApprovedBy:    "CIO",
		RiskChecked:   true,
		Signals:       []string{},
		TargetStocks:  []string{},
	}
}

// TraderHandler Trader智能体处理器 - 权限: EXECUTE（只能执行）
type TraderHandler struct {
	orchestrator *Orchestrator
	agent        *port.Agent
}

func NewTraderHandler(o *Orchestrator, agent *port.Agent) *TraderHandler {
	return &TraderHandler{orchestrator: o, agent: agent}
}

func (h *TraderHandler) GetRole() string               { return "trader" }
func (h *TraderHandler) GetPermission() RolePermission { return PermExecute }

func (h *TraderHandler) Handle(ctx context.Context, task *WorkflowTask, step *TaskStep) error {
	log.Printf("[Trader] EXECUTE: %s (task: %s) → DLL-only", step.Name, task.ID)

	// Trader只能执行CIO的决策对象
	if task.Decision == nil {
		return fmt.Errorf("Trader cannot execute without DecisionObject from CIO")
	}

	h.orchestrator.setStepOutput(task.ID, step.ID, map[string]interface{}{
		"role":        "TRADER",
		"permission":  "EXECUTE",
		"decision":    task.Decision,
		"executed":    false,
		"result":      map[string]interface{}{"status": "DLL_ONLY", "note": "进程内 Agent 决策已迁入 agent.dll"},
		"executed_at": time.Now().Format(time.RFC3339),
	})

	log.Printf("[Trader] EXECUTE completed: %s", step.ID)
	return nil
}

// RegisterAllHandlers 注册所有处理器（权限链：PROPOSE→EVIDENCE→VETO→DECIDE→EXECUTE）
func RegisterAllHandlers(o *Orchestrator, plannerAgent, quantAgent, riskAgent, cioAgent, traderAgent *port.Agent) {
	if plannerAgent != nil {
		o.RegisterHandler(NewPlannerHandler(o, plannerAgent))
	}
	if quantAgent != nil {
		o.RegisterHandler(NewQuantHandler(o, quantAgent))
	}
	if riskAgent != nil {
		o.RegisterHandler(NewRiskHandler(o, riskAgent))
	}
	if cioAgent != nil {
		o.RegisterHandler(NewCIOHandler(o, cioAgent))
	}
	if traderAgent != nil {
		o.RegisterHandler(NewTraderHandler(o, traderAgent))
	}
	log.Println("[Orchestrator] All handlers registered with permission chain")
}