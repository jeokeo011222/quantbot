package toolworker

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/quantpilot/quantpilot/internal/brain/agents"
	"github.com/quantpilot/quantpilot/internal/brain/llm"
	"github.com/quantpilot/quantpilot/internal/brain/port"
)

// Host 承载宿主兑现 handler（map[kind][op]func → result JSON）。
//
// 本阶段把「确定性兜底」升级为「可绑定真实实现的注册式宿主」：
//   - 未调用 BindReal 时，各维度退回确定性兜底（无网络/无数据库），便于 cmd/dllrunner 离线验证；
//   - 调用 BindReal 后，tool/store/context/llm 请求由宿主侧真实实现兑现（真实工具 / 真实
//     Persistence / 真实上下文 / 真实 LLM），经 AgentRespond 推回 DLL 内决策脑。
type Host struct {
	mu sync.Mutex

	// 兑现统计（Runner 据此打印「宿主兑现了多少条…请求」）。
	CntTool    int
	CntStore   int
	CntContext int
	CntLLM     int

	// 真实实现兑现统计（BindReal 绑定后的各维度真实处理数）。
	RealTool    int
	RealStore   int
	RealContext int
	RealLLM     int

	taskSeq uint64

	// 真实宿主实现（BindReal 注入）。任一为 nil 即该维度退回确定性兜底。
	realTools   map[string]port.ToolExecutor // name → 真实工具执行器（经总线执行）
	realStore   port.Persistence
	realContext port.ContextProvider
	realLLM     llm.Client
}

// Counters 一次运行后各 kind 的兑现计数（含真实实现计数）。
func (h *Host) Counters() map[string]int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return map[string]int{
		"tool":         h.CntTool,
		"store":        h.CntStore,
		"context":      h.CntContext,
		"llm":          h.CntLLM,
		"tool_real":    h.RealTool,
		"store_real":   h.RealStore,
		"context_real": h.RealContext,
		"llm_real":     h.RealLLM,
	}
}

func (h *Host) inc(kind string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	switch kind {
	case "tool":
		h.CntTool++
	case "store":
		h.CntStore++
	case "context":
		h.CntContext++
	case "llm":
		h.CntLLM++
	}
}

func (h *Host) incReal(kind string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	switch kind {
	case "tool":
		h.RealTool++
	case "store":
		h.RealStore++
	case "context":
		h.RealContext++
	case "llm":
		h.RealLLM++
	}
}

// BindReal 把真实宿主实现绑定到 Host，使总线请求由真实实现兑现。
//
//	team:       真实 agent 团队（App 侧 a.agentTeam），据此建立 name→真实工具执行器；
//	            同时可用 BuildCatalogFromTeam 生成发送给 DLL 的 catalog。传 nil 则工具维度保留确定性兜底。
//	store:      真实持久化（brainhost.NewPersistenceAdapter(sqliteManager)）。
//	ctxProvider:真实系统上下文构建器（port.ContextProvider）。
//	llmClient:  真实 LLM 客户端（llm.Client）。
//
// 任一参数为 nil 时对应维度保留确定性兜底（便于无 API key / 无数据库离线验证）。
func (h *Host) BindReal(team map[agents.AgentRole]*agents.Agent, store port.Persistence, ctxProvider port.ContextProvider, llmClient llm.Client) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if team != nil {
		h.realTools = make(map[string]port.ToolExecutor)
		for _, ag := range team {
			if ag == nil {
				continue
			}
			for _, t := range ag.Tools {
				if t == nil {
					continue
				}
				if _, exists := h.realTools[t.Name()]; !exists {
					h.realTools[t.Name()] = t
				}
			}
		}
	}
	h.realStore = store
	h.realContext = ctxProvider
	h.realLLM = llmClient
}

// BuildCatalogFromTeam 依据真实 Agent 团队生成发送给 agent.dll 的 catalog
// （role 用总线约定的全小写标签，DLL 侧 roleMap 兼容），作为 AgentInit.catalog。
func BuildCatalogFromTeam(team map[agents.AgentRole]*agents.Agent) []map[string]interface{} {
	roleLower := map[agents.AgentRole]string{
		agents.RoleCIO:     "cio",
		agents.RolePlanner: "planner",
		agents.RoleQuant:   "quant",
		agents.RoleRisk:    "risk",
		agents.RoleTrader:  "trader",
	}
	var catalog []map[string]interface{}
	for role, ag := range team {
		lower, ok := roleLower[role]
		if !ok || ag == nil {
			continue
		}
		var tools []map[string]interface{}
		for _, t := range ag.Tools {
			if t == nil {
				continue
			}
			def := t.GetDefinition()
			tools = append(tools, map[string]interface{}{
				"name":        def.Function.Name,
				"description": def.Function.Description,
				"parameters":  def.Function.Parameters,
			})
		}
		if len(tools) == 0 {
			continue
		}
		catalog = append(catalog, map[string]interface{}{"role": lower, "tools": tools})
	}
	return catalog
}

// nextTaskID 生成确定性的递增 task id。
func (h *Host) nextTaskID() uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.taskSeq++
	return h.taskSeq
}

// HandleRequest 兑现一帧总线请求，返回宿主侧的 result JSON 字符串。
// kind ∈ "tool"|"store"|"context"|"llm"。已绑定真实实现在先，否则确定性兜底。
func (h *Host) HandleRequest(kind, payload string) string {
	h.inc(kind)
	switch kind {
	case "tool":
		return h.handleTool(payload)
	case "store":
		return h.handleStore(payload)
	case "context":
		return h.handleContext(payload)
	case "llm":
		return h.handleLLM(payload)
	default:
		return mustJSON(map[string]interface{}{"error": fmt.Sprintf("未知请求种类: %s", kind)})
	}
}

// requestEnvelope 统一回执，便于目录可读。
func mustJSON(v interface{}) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// handleTool 真实工具：按 name 查绑定执行器并 tool.Execute(ctx,args)；未绑定则确定性兜底。
func (h *Host) handleTool(payload string) string {
	var req struct {
		Name string                 `json:"name"`
		Args map[string]interface{} `json:"args"`
	}
	_ = json.Unmarshal([]byte(payload), &req)

	h.mu.Lock()
	tool := h.realTools[req.Name]
	h.mu.Unlock()

	if tool != nil {
		h.incReal("tool")
		res, err := tool.Execute(context.Background(), req.Args)
		if err != nil {
			log.Printf("[toolworker] 真实工具 %s 执行失败: %v", req.Name, err)
			return mustJSON(map[string]interface{}{"tool": req.Name, "error": err.Error()})
		}
		log.Printf("[toolworker] 真实工具 %s 兑现 ok", req.Name)
		return mustJSON(res)
	}

	// 确定性兜底：打印调用、返回固定 JSON（该 JSON 即作为工具结果传回决策脑）。
	log.Printf("[toolworker] tool exec(确定性兜底): name=%s args=%v", req.Name, req.Args)
	return mustJSON(map[string]interface{}{
		"tool":    req.Name,
		"status":  "ok",
		"note":    "宿主确定性兑现（无网络/无数据库）",
		"metrics": map[string]interface{}{"value": 100, "confidence": 0.8},
	})
}

// handleStore 真实持久化：把远程 store 请求映射到真实 Persistence 方法；未绑定则确定性兜底。
func (h *Host) handleStore(payload string) string {
	var req map[string]interface{}
	_ = json.Unmarshal([]byte(payload), &req)
	op, _ := req["op"].(string)

	h.mu.Lock()
	store := h.realStore
	h.mu.Unlock()

	if store != nil {
		h.incReal("store")
		log.Printf("[toolworker] 真实 store op=%s", op)
		res, err := h.dispatchStore(store, op, req)
		if err != nil {
			return mustJSON(map[string]interface{}{"op": op, "error": err.Error()})
		}
		return mustJSON(res)
	}

	log.Printf("[toolworker] store op=%s(确定性兜底) args=%v", op, req)
	switch op {
	case "log_task_start":
		return mustJSON(map[string]interface{}{"task_id": h.nextTaskID()})
	default:
		return mustJSON(map[string]interface{}{"ok": true})
	}
}

// dispatchStore 把远程 store op 映射到真实 port.Persistence 方法。
func (h *Host) dispatchStore(store port.Persistence, op string, req map[string]interface{}) (interface{}, error) {
	// req 值均来自 JSON 反序列化（浮点数/字符串），做宽松类型收窄。
	switch op {
	case "clear_task_logs":
		return map[string]interface{}{"ok": store.ClearTaskLogs(str(req, "date")) == nil}, nil
	case "log_task_start":
		tl := store.LogTaskStart(str(req, "task_date"), str(req, "task_phase"),
			str(req, "agent_role"), str(req, "task_name"), int(num(req, "task_order")))
		if tl == nil {
			return map[string]interface{}{"ok": false}, nil
		}
		return map[string]interface{}{"task_id": tl.ID}, nil
	case "log_task_complete":
		store.LogTaskComplete(uintVal(req, "task_id"), str(req, "deliverable_type"),
			str(req, "deliverable_name"), req["deliverable_data"], str(req, "summary"))
		return map[string]interface{}{"ok": true}, nil
	case "log_task_failed":
		store.LogTaskFailed(uintVal(req, "task_id"), str(req, "err_msg"))
		return map[string]interface{}{"ok": true}, nil
	case "write_audit":
		err := store.WriteAudit(str(req, "event_id"), str(req, "event_type"), str(req, "user_id"),
			str(req, "user_name"), str(req, "action"), str(req, "target_type"), str(req, "target_id"),
			str(req, "result"), str(req, "details_json"), timeVal(req, "ts"))
		return map[string]interface{}{"ok": err == nil}, nil
	case "save_cio_decision":
		err := store.SaveCIODecision(str(req, "decision_id"), str(req, "portfolio_id"),
			str(req, "decision"), str(req, "reason"), str(req, "risk_approval"),
			str(req, "policy_status"), str(req, "market_state"), num(req, "market_confidence"), timeVal(req, "ts"))
		return map[string]interface{}{"ok": err == nil}, nil
	default:
		return map[string]interface{}{"ok": true}, nil
	}
}

func str(v map[string]interface{}, key string) string {
	if s, ok := v[key].(string); ok {
		return s
	}
	return ""
}

func num(v map[string]interface{}, key string) float64 {
	switch x := v[key].(type) {
	case float64:
		return x
	case int:
		return float64(x)
	}
	return 0
}

func uintVal(v map[string]interface{}, key string) uint {
	switch x := v[key].(type) {
	case float64:
		return uint(x)
	case int:
		return uint(x)
	case json.Number:
		if i, err := x.Int64(); err == nil {
			return uint(i)
		}
	}
	return 0
}

func timeVal(v map[string]interface{}, key string) time.Time {
	s := str(v, key)
	if s == "" {
		return time.Now()
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	return time.Now()
}

// handleContext 真实上下文：注入 provider 返回系统上下文；未绑定则确定性兜底（空串）。
func (h *Host) handleContext(payload string) string {
	var req struct {
		Role string `json:"role"`
		Date string `json:"date"`
	}
	_ = json.Unmarshal([]byte(payload), &req)

	h.mu.Lock()
	cp := h.realContext
	h.mu.Unlock()

	if cp != nil {
		h.incReal("context")
		s := cp(context.Background(), req.Role, req.Date)
		log.Printf("[toolworker] 真实 context role=%s date=%s 长度=%d", req.Role, req.Date, len(s))
		return mustJSON(s) // 决策脑把该 JSON 解码为 Go 字符串
	}

	log.Printf("[toolworker] context request(确定性兜底) role=%s date=%s → 空上下文", req.Role, req.Date)
	return `""` // JSON 字符串：空上下文
}

// hasAssistantToolCall 判断会话历史中是否已存在带 tool_calls 的 assistant 消息。
func hasAssistantToolCall(msgs []llm.Message) bool {
	for _, m := range msgs {
		if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			return true
		}
	}
	return false
}

// handleLLM 真实 LLM：把 {op,args} 映射到真实 llm.Ch/Embedding；未绑定则确定性兜底。
func (h *Host) handleLLM(payload string) string {
	var req struct {
		Op       string        `json:"op"`
		Messages []llm.Message `json:"messages"`
		Tools    []llm.Tool    `json:"tools"`
		Text     string        `json:"text"`
	}
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		return mustJSON(map[string]interface{}{"error": "llm 请求解码失败: " + err.Error()})
	}

	h.mu.Lock()
	lc := h.realLLM
	h.mu.Unlock()

	switch req.Op {
	case "embedding":
		if lc != nil {
			h.incReal("llm")
			vec, err := lc.Embedding(context.Background(), req.Text)
			if err != nil {
				log.Printf("[toolworker] 真实 embedding 失败: %v", err)
				return mustJSON(map[string]interface{}{"error": "embedding 失败: " + err.Error()})
			}
			log.Printf("[toolworker] 真实 embedding 兑现 dim=%d", len(vec))
			return mustJSON(vec)
		}
		return mustJSON([]float64{0.1, 0.2, 0.3, 0.4})
	case "chat":
		if lc != nil {
			h.incReal("llm")
			res, err := lc.Chat(context.Background(), req.Messages, req.Tools)
			if err != nil {
				log.Printf("[toolworker] 真实 chat 失败: %v", err)
				return mustJSON(map[string]interface{}{"error": "chat 失败: " + err.Error()})
			}
			log.Printf("[toolworker] 真实 chat 兑现 choices=%d", len(res.Choices))
			return mustJSON(res)
		}
		return h.chatResult(req.Messages, req.Tools) // 确定性 ReAct 收敛
	default:
		return mustJSON(map[string]interface{}{"error": "未知 llm op: " + req.Op})
	}
}

// chatResult 确定性 LLM（Agent 用 Chat 走 ReAct 循环）。
//
//	首次/尚无 assistant tool_calls：返回一个 tool_call（调用 tools[0]），触发一次工具兑现；
//	此后（已出现 assistant tool_calls 或未配工具）：返回一个结构化 JSON 决策内容，令 Agent 收尾。
func (h *Host) chatResult(messages []llm.Message, tools []llm.Tool) string {
	const model = "deterministic-host"

	toolName := ""
	if len(tools) > 0 {
		toolName = tools[0].Function.Name
	}

	usage := map[string]int{"prompt_tokens": 10, "completion_tokens": 1, "total_tokens": 11}

	// 若尚无任何 assistant tool_call、且至少配了一件工具 → 先调用工具（触发宿主兑现）。
	if !hasAssistantToolCall(messages) && toolName != "" {
		log.Printf("[toolworker] llm chat(确定性兜底): 首次响应 → 请求工具 %s", toolName)
		res := map[string]interface{}{
			"id":    "chat-tool-1",
			"model": model,
			"choices": []interface{}{map[string]interface{}{
				"index": 0,
				"message": map[string]interface{}{
					"role": "assistant",
					"tool_calls": []interface{}{map[string]interface{}{
						"id":   "call_host_1",
						"type": "function",
						"function": map[string]interface{}{
							"name":      toolName,
							"arguments": "{}",
						},
					}},
				},
				"finish_reason": "tool_calls",
			}},
			"usage": usage,
		}
		return mustJSON(res)
	}

	// 已有工具调用结果或未配工具 → 返回结构化 JSON 决策，Agent 收尾。
	log.Printf("[toolworker] llm chat(确定性兜底): 收尾响应 → 返回决策")
	finalContent := mustJSON(map[string]interface{}{
		"action":  "HOLD",
		"summary": "确定性宿主兑现下的决策（拉模式总线验证）。",
		"reason":  "工具调用已由宿主兑现，决策脑按既定流程收敛。",
	})
	res := map[string]interface{}{
		"id":    "chat-final-1",
		"model": model,
		"choices": []interface{}{map[string]interface{}{
			"index": 0,
			"message": map[string]interface{}{
				"role":    "assistant",
				"content": finalContent,
			},
			"finish_reason": "stop",
		}},
		"usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 25, "total_tokens": 35},
	}
	return mustJSON(res)
}
