// Package agentownership 提供"用户追问归属"和 handback 机制。
//
// 设计借鉴 dsh-superagent 的 follow-up ownership：
//  - 每条用户追问默认归属于"直接收到这条追问"的 Agent
//  - Agent 有两个选项：handle-in-scope（在范围内自己答）或 handback（超范围 / 不属于自己，退还给主编排器重新路由）
//  - handback 必须携带 reason（reason code），让主编排器做二次判断
//  - 一个追问被所有候选 Agent handback 后，才升级为需要重新 Eval 的新工单
//
// 量化 AIQuant 的映射：
//  - 主编排器 = Orchestrator
//  - Worker Agent = Planner / Quant / Risk / CIO / Trader / Auditor
//  - 追问 = 用户在 UI 或事件总线里对某条决策/风控结果的追问
package agentownership

import (
	"fmt"
	"sync"
	"time"
)

// Ownership 一条用户追问的所有权记录。
type Ownership struct {
	// FollowupID 追问唯一标识
	FollowupID string

	// Content 追问自然语言（展示用）
	Content string

	// PrimaryAgent 首选归属 Agent（直接被问的那个）
	PrimaryAgent string

	// Handbacks 各候选 Agent 的 handback 记录（空 = 还在首轮归属）
	Handbacks []Handback

	// Status 生命周期
	Status Status

	// ResolvedAt 解决时间（nil 表示未解决）
	ResolvedAt *time.Time

	// CreatedAt 创建时间
	CreatedAt time.Time
}

// Handback 一次 handback 事件：Agent 声明"这条追问不归我管，主编排器请重新路由"。
type Handback struct {
	// Agent 声称 handback 的 Agent
	Agent string
	// ReasonCode 结构化原因码（见 reasoning 包）
	ReasonCode string
	// Detail 可选自由文本补充
	Detail string
	// At handback 时间
	At time.Time
}

// Status 追问状态。
type Status string

const (
	StatusOpen          Status = "open"           // 首轮归属，等待处理
	StatusReevaluating  Status = "reevaluating"   // 全部 handback → 主编排器重新 Eval 中
	StatusHandled       Status = "handled"        // 某 Agent 已在范围内答完
	StatusReassigned    Status = "reassigned"     // 主编排器已重新路由给新 Agent
	StatusDropped       Status = "dropped"        // 无 Agent 愿意且不属于任何自动处理范围
)

// Registry 追问所有权注册表（线程安全）。
// 单例即可；也可以在 Orchestrator 内注入。
type Registry struct {
	mu          sync.RWMutex
	ownerships  map[string]*Ownership   // followup_id → Ownership
	byAgent     map[string][]string     // agent → []followup_id
	maxReassign int                     // 最大重新路由次数（防无限震荡，默认 2）
}

// NewRegistry 创建注册表。
func NewRegistry() *Registry {
	return &Registry{
		ownerships:  make(map[string]*Ownership),
		byAgent:     make(map[string][]string),
		maxReassign: 2,
	}
}

// ---- API ----

// Register 登记一条新的追问归属。
func (r *Registry) Register(followupID, content, primaryAgent string) *Ownership {
	r.mu.Lock()
	defer r.mu.Unlock()

	o := &Ownership{
		FollowupID:   followupID,
		Content:      content,
		PrimaryAgent: primaryAgent,
		Status:       StatusOpen,
		CreatedAt:    time.Now(),
	}
	r.ownerships[followupID] = o
	r.byAgent[primaryAgent] = append(r.byAgent[primaryAgent], followupID)
	return o
}

// Handback 让 agent 对 followupID 发起 handback。
// 如果所有候选 Agent（PrimaryAgent + 已 handback 的）都 handback 完，Registry 自动把状态切到 Reevaluating 并返回 shouldReevaluate=true。
func (r *Registry) Handback(followupID, agent, reasonCode, detail string) (shouldReevaluate bool, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	o, ok := r.ownerships[followupID]
	if !ok {
		return false, fmt.Errorf("followup not found: %s", followupID)
	}
	if o.Status == StatusHandled || o.ResolvedAt != nil {
		return false, fmt.Errorf("followup already resolved: %s (%s)", followupID, o.Status)
	}

	// 幂等：同一个 agent 不能 handback 两次
	for _, h := range o.Handbacks {
		if h.Agent == agent {
			return false, nil
		}
	}
	o.Handbacks = append(o.Handbacks, Handback{
		Agent:      agent,
		ReasonCode: reasonCode,
		Detail:     detail,
		At:         time.Now(),
	})
	o.Status = StatusReevaluating
	return true, nil
}

// MarkHandled agent 声明已在自己范围内答完这条追问。
func (r *Registry) MarkHandled(followupID, agent string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	o, ok := r.ownerships[followupID]
	if !ok {
		return fmt.Errorf("followup not found: %s", followupID)
	}
	o.Status = StatusHandled
	t := time.Now()
	o.ResolvedAt = &t
	return nil
}

// Reassign 主编排器把追问重新路由给新 Agent。
func (r *Registry) Reassign(followupID, newAgent string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	o, ok := r.ownerships[followupID]
	if !ok {
		return fmt.Errorf("followup not found: %s", followupID)
	}
	o.PrimaryAgent = newAgent
	o.Status = StatusReassigned
	r.byAgent[newAgent] = append(r.byAgent[newAgent], followupID)
	return nil
}

// OwnedBy 返回指定 agent 当前持有的所有追问 ID。
func (r *Registry) OwnedBy(agent string) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]string(nil), r.byAgent[agent]...)
}

// Get 查一条追问。
func (r *Registry) Get(followupID string) (*Ownership, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	o, ok := r.ownerships[followupID]
	return o, ok
}

// Summary 返回当前活跃追问数量和各自状态分布。
func (r *Registry) Summary() map[Status]int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	counts := map[Status]int{
		StatusOpen:         0,
		StatusReevaluating: 0,
		StatusHandled:      0,
		StatusReassigned:   0,
		StatusDropped:       0,
	}
	for _, o := range r.ownerships {
		if _, ok := counts[o.Status]; ok {
			counts[o.Status]++
		}
	}
	return counts
}
