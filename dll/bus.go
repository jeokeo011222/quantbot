package main

import (
	"context"
	"encoding/json"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/quantpilot/quantpilot/internal/brain/agents"
	"github.com/quantpilot/quantpilot/internal/brain/llm"
	"github.com/quantpilot/quantpilot/internal/brain/port"
)

// -------- 拉模式 RPC 总线（总线内核）--------
//
// 跨 c-shared 边界（同一进程两个 Go 运行时，仅允许 *C.char JSON）不能互相传函数指针，
// 因此决策脑每次需要外部能力（工具/持久化/上下文）时，先把请求 {kind,id,payload}
// 压入本会话的待处理队列（pendingReq）并阻塞等待；宿主循环调 AgentPoll 取走请求、
// 在宿主侧执行、调 AgentRespond(handle,id,resultJSON) 推回，被阻塞的脑 goroutine 据此继续。
//
// kind ∈ "tool" | "store" | "context"。payload/result 均为可 JSON 编解码结构。
// LLM 已归 DLL 自持（setupTeam 按 AgentInit 传入的配置自建 llm.Client），不再走总线。

// busRequest 一帧跨边界请求：宿主侧原样回传 id 以便 AgentRespond 定位等待方。
type busRequest struct {
	ID      string          `json:"id"`
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
}

// busReply 一帧对应请求的投递结果（result 为宿主返回的 JSON；err 非空表示宿主执行失败）。
type busReply struct {
	Result json.RawMessage
	Err    string
}

// 总线请求种类常量（跨边界仅传这些标签）。
const (
	kindTool    = "tool"    // 决策脑需宿主执行一个工具
	kindStore   = "store"   // 决策脑需宿主兑现一次持久化
	kindContext = "context" // 决策脑需宿主提供系统上下文
)

// requestSeq 全局递增请求号，保证同一会话内 id 唯一。
var requestSeq int64

// brainSession 一次 AgentInit 对应的会话元数据。承载 AgentInit 建立的决策脑团队
// （orchestrator + 各角色 Agent/BaseAgent）以及拉模式 RPC 总线的持久状态。
type brainSession struct {
	ID        int64
	Config    abiConfig
	ToolWired bool // 已接驳宿主工具 worker（完整 M3 置 true）：校验通过后由 setupTeam 置 true

	busMu        sync.Mutex
	pendingReq   []busRequest             // 待处理请求队列（宿主逐帧拉取）
	waiters      map[string]chan busReply // id → 阻塞中的响应 channel
	requestIDSeq int64

	// 决策脑装配
	orch          *agents.Orchestrator
	remotePersist port.Persistence
	remoteLLM     llm.Client
	ctxProvider   port.ContextProvider
	cancel        context.CancelFunc

	running bool // 是否正在运行每日周期
	done    bool // 周期是否已结束（成功或失败）
	result  *agents.OrchestratorResult
	err     error
}

// sendRequest 压入一帧请求并阻塞等待宿主兑现结果。ctx 取消（如 AgentClose / 超时）时返回 ctx.Err()。
func (s *brainSession) sendRequest(ctx context.Context, kind string, payload interface{}) (json.RawMessage, error) {
	pb, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	id := atomic.AddInt64(&s.requestIDSeq, 1)
	idStr := strconv.FormatInt(id, 10)
	ch := make(chan busReply, 1)
	req := busRequest{ID: idStr, Kind: kind, Payload: pb}

	s.busMu.Lock()
	s.pendingReq = append(s.pendingReq, req)
	s.waiters[idStr] = ch
	s.busMu.Unlock()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case rep := <-ch:
		if rep.Err != "" {
			return nil, &busError{rep.Err}
		}
		return rep.Result, nil
	}
}

// busError 携带宿主侧执行错误的普通错误。
type busError struct{ msg string }

func (e *busError) Error() string { return e.msg }

// pollResult 描述 AgentPoll 的一次结果。status ∈ awaiting_request|running|done|error。
type pollResult struct {
	Status  string          `json:"status"`
	Request *busRequest     `json:"request,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"` // 仅 done 时携带 OrchestratorResult
	Error   string          `json:"error,omitempty"`  // 仅 error 时携带
}

// poll 取一次可投递状态。优先完成态，其次待处理请求，最后 running。
func (s *brainSession) poll() pollResult {
	s.busMu.Lock()
	defer s.busMu.Unlock()

	if s.done {
		if s.err != nil {
			return pollResult{Status: "error", Error: s.err.Error()}
		}
		if s.result != nil {
			rb, _ := json.Marshal(s.result)
			return pollResult{Status: "done", Result: rb}
		}
		return pollResult{Status: "error", Error: "分析周期未产生结果"}
	}

	if len(s.pendingReq) > 0 {
		req := s.pendingReq[0]
		s.pendingReq = s.pendingReq[1:]
		return pollResult{Status: "awaiting_request", Request: &req}
	}

	return pollResult{Status: "running"}
}

// respondRequest 投递宿主结果到指定 id 的等待方。找不到该请求（已响应/已取消）返回 false。
func (s *brainSession) respondRequest(id, resultJSON string) bool {
	s.busMu.Lock()
	defer s.busMu.Unlock()
	ch, ok := s.waiters[id]
	if !ok {
		return false
	}
	delete(s.waiters, id)
	ch <- busReply{Result: json.RawMessage(resultJSON)}
	return true
}

// -------- 团队构造（AgentInit 阶段）--------

// busCatalog 单个角色的目录元数据（宿主把角色→工具清单传入，DLL 据此构造远程工具）。
type busCatalog struct {
	Role  string           `json:"role"`
	Tools []busCatalogTool `json:"tools"`
}

// busCatalogTool 单个工具的静态元数据（name/description/parameters），执行仍走总线。
type busCatalogTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// roleCatalog 收敛角色常量，兼容小写/大写输入。
var roleMap = map[string]agents.AgentRole{
	"cio":     agents.RoleCIO,
	"planner": agents.RolePlanner,
	"quant":   agents.RoleQuant,
	"risk":    agents.RoleRisk,
	"trader":  agents.RoleTrader,
}

// setupTeam 用 AgentInit 传入的 catalog 装配决策脑：远程持久化/LLM/上下文 + 每角色
// BaseAgent(带远程工具) + Agent，全部注册进 Orchestrator。catalog 可空（仍建空团队，
// RunDailyCycle 会因缺 LLM 工具优雅失败而不 panic）。
func (s *brainSession) setupTeam(catalog []busCatalog) {
	// 远程抽象（每次调用都经总线由宿主兑现）；LLM 由决策脑自持：
	// 按 AgentInit 传入的配置自建 llm.Client（空字段走 ProviderDefaults），不再经总线代理。
	s.remotePersist = newRemotePersistence(s)
	s.remoteLLM = llm.NewClient(s.Config.Provider, s.Config.APIKey, s.Config.BaseURL, s.Config.Model)
	ctxProvider := func(ctx context.Context, role string, date string) string {
		if ctx == nil {
			ctx = context.Background()
		}
		raw, err := s.sendRequest(ctx, kindContext, struct {
			Role string `json:"role"`
			Date string `json:"date"`
		}{Role: role, Date: date})
		if err != nil {
			return ""
		}
		var result string
		_ = json.Unmarshal(raw, &result)
		return result
	}
	s.ctxProvider = ctxProvider

	orch := agents.NewOrchestrator(s.remotePersist, s.remoteLLM)
	orch.ContextProvider = ctxProvider
	s.orch = orch

	for _, cat := range catalog {
		role := roleMap[cat.Role]
		if role == "" {
			continue
		}

		// 按目录构造该角色的远程工具集合
		var remoteTools []port.ToolExecutor
		for _, c := range cat.Tools {
			params := json.RawMessage(nil)
			if c.Parameters != nil {
				params = c.Parameters
			}
			remoteTools = append(remoteTools, newRemoteTool(s, c.Name, c.Description, params))
		}

		// BaseAgent：RunDailyCycle 实际用 BaseAgent.RunWithTrajectory 执行 ReAct 循环。
		// 注意 RegisterBaseAgent 以 agent.ID 为键，而 RunDailyCycle 以 string(Role)(大写) 查表，
		// 因此这里把 ID 设为 string(Role)，保证二者对齐。
		baseID := string(role)
		baseAgent := agents.NewBaseAgentWithRole(baseID, role, "")
		baseAgent.SetLLMClient(s.remoteLLM)
		baseAgent.SetMode(agents.ModeStandard)
		for _, t := range remoteTools {
			baseAgent.RegisterTool(t)
		}
		orch.RegisterBaseAgent(baseAgent)

		// Agent：状态簿记与注册表同步所需（RunDailyCycle 按 role 查找）。
		agent := agents.NewAgent(baseID, role, s.remotePersist, s.remoteLLM, nil)
		for _, t := range remoteTools {
			agent.RegisterTool(t)
		}
		orch.RegisterAgent(agent)
	}

	s.ToolWired = true
}
