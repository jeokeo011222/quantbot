// 宿主侧自包含的决策产物 DTO 与轻量编排对象。
//
// 在 DLL 隔离重构后，宿主进程不得 import 科研决策脑包（internal/brain 子树）。
// 因此把仅供本包内部使用的决策产物（ResearchReport / RiskReport / 风控判定常量）
// 与轻量 agent 包装在此复刻；与 internal/port 对齐的通用 DTO（CIODecision /
// OrderIntent / DecisionType / AgentRole 等）则直接走 internal/port，见对应 import 别名。
package cio

import (
	"log"
	"sync"

	agents "github.com/quantpilot/quantpilot/internal/port"
)

// ==================== 决策产物 DTO（复刻自 agents，JSON 形状保持一致） ====================

// ResearchReport 研究报告
type ResearchReport struct {
	ResearchID     string      `json:"research_id"`
	Objective      string      `json:"objective"`
	MarketState    interface{} `json:"market_state"`
	Findings       []string    `json:"findings"`
	Strategies     interface{} `json:"strategies"`
	Risks          []string    `json:"risks"`
	Limitations    []string    `json:"limitations"`
	Confidence     float64     `json:"confidence"`
	Recommendation string      `json:"recommendation"`
}

// RiskReport 风控报告
type RiskReport struct {
	RiskID         string      `json:"risk_id"`
	PortfolioRisk  interface{} `json:"portfolio_risk"`
	StructuralRisk interface{} `json:"structural_risk"`
	FactorHealth   interface{} `json:"factor_health"`
	StressResults  interface{} `json:"stress_results"`
	Violations     []string    `json:"violations"`
	Decision       string      `json:"decision"`
	Confidence     float64     `json:"confidence"`
}

// 风控判定（对齐 agents.RiskDecision*，字符串值一致，供 RiskReport.Decision 使用）。
const (
	riskApprove        = "APPROVE"
	riskReviewRequired = "REVIEW_REQUIRED"
	riskReject         = "REJECT"
	riskEmergencyStop  = "EMERGENCY_STOP"
)

// cioAgentStateX CIO 决策链路的补充状态。port.AgentState 仅含
// IDLE/THINKING/COMPLETED/FAILED；交易执行相关的中间态在此本地扩展（值对齐 agents）。
const (
	cioAgentStateReviewing agents.AgentState = "REVIEWING"
	cioAgentStateApproved  agents.AgentState = "APPROVED"
	cioAgentStateSubmitted agents.AgentState = "SUBMITTED"
	cioAgentStateFilled    agents.AgentState = "FILLED"
	cioAgentStateBlocked   agents.AgentState = "BLOCKED"
)

// ==================== 轻量 agent 编排对象（替代 agents.Agent） ====================

// cioAgent 决策链路中各角色的轻量编排对象。仅承载状态标记、状态展示与 LLM 评审客户端；
// 真实决策逻辑（风控门、订单生成、仓位计算等）在本包各方法内直接完成，不走进程内 agent。
type cioAgent struct {
	ID      string
	Role    agents.AgentRole
	Name    string
	LLM     agents.LLMClient // 暴露为 LLM 以兼容既有 c.agent.LLM 访问
	state   agents.AgentState
	stopped bool
	mu      sync.Mutex
}

// newCIOAgent 创建轻量编排对象。
func newCIOAgent(id string, role agents.AgentRole, name string, llm agents.LLMClient) *cioAgent {
	return &cioAgent{ID: id, Role: role, Name: name, LLM: llm, state: agents.StateIdle}
}

// SetState 更新状态并记录日志。
func (a *cioAgent) SetState(state agents.AgentState) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	old := a.state
	a.state = state
	log.Printf("[%s] State: %s → %s", a.Name, old, state)
}

// State 返回当前状态。
func (a *cioAgent) State() agents.AgentState {
	if a == nil {
		return agents.StateIdle
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.state
}

// EmergencyStop 紧急停止（本包无进程内 stop 调度，等价于标记 stopped 供 emergencyStopped 判定）。
func (a *cioAgent) EmergencyStop() {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.stopped = true
}

// Resume 恢复工作。
func (a *cioAgent) Resume() {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.stopped = false
}

// IsStopped 是否处于紧急停止状态。
func (a *cioAgent) IsStopped() bool {
	if a == nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.stopped
}

// GetStatusInfo 返回状态展示信息（供 GetStatus 汇总到前端）。
func (a *cioAgent) GetStatusInfo() map[string]interface{} {
	if a == nil {
		return map[string]interface{}{"id": "", "role": "", "name": "", "state": "idle"}
	}
	return map[string]interface{}{
		"id":    a.ID,
		"role":  string(a.Role),
		"name":  a.Name,
		"state": string(a.State()),
	}
}
