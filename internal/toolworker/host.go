package toolworker

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/quantpilot/quantpilot/internal/port"
)

// Host 承载宿主兑现 handler（map[kind][op]func → result JSON）。
//
// 本阶段把「确定性兜底」升级为「可绑定真实实现的注册式宿主」。LLM 由 DLL 自持
// （配置经 AgentInit 传入），故本桥只对 tool/store/context 三类请求经总线兑现。
//   - 未调用 BindReal 时，各维度退回确定性兜底（无网络/无数据库），便于 cmd/dllrunner 离线验证；
//   - 调用 BindReal 后，tool/store/context 请求由宿主侧真实实现兑现（真实工具 / 真实
//     Persistence / 真实上下文），经 AgentRespond 推回 DLL 内决策脑。
type Host struct {
	mu sync.Mutex

	// 兑现统计（Runner 据此打印「宿主兑现了多少条…请求」）。
	CntTool    int
	CntStore   int
	CntContext int

	// 真实实现兑现统计（BindReal 绑定后的各维度真实处理数）。
	RealTool    int
	RealStore   int
	RealContext int

	taskSeq uint64

	// 真实宿主实现（BindReal 注入）。任一为 nil 即该维度退回确定性兜底。
	realTools   map[string]port.ToolExecutor // name → 真实工具执行器（经总线执行）
	realStore   port.Persistence
	realContext port.ContextProvider
}

// Counters 一次运行后各 kind 的兑现计数（含真实实现计数）。
func (h *Host) Counters() map[string]int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return map[string]int{
		"tool":         h.CntTool,
		"store":        h.CntStore,
		"context":      h.CntContext,
		"tool_real":    h.RealTool,
		"store_real":   h.RealStore,
		"context_real": h.RealContext,
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
	}
}

// BindReal 把真实宿主实现绑定到 Host，使总线请求由真实实现兑现。
//
//	tools:       真实工具执行器列表（宿主装配点收集为 []port.ToolExecutor），
//	             据此建立 name→真实工具执行器；同时可用 BuildCatalog 生成发给 DLL 的 catalog。
//	             nil 则不建立工具绑定（工具维度保留确定性兜底）。
//	store:       真实持久化（brainhost.NewPersistenceAdapter(sqliteManager)）。
//	ctxProvider: 真实系统上下文构建器（port.ContextProvider）。
//
// 任一参数为 nil 时对应维度保留确定性兜底（便于无 API key / 无数据库离线验证）。
func (h *Host) BindReal(tools []port.ToolExecutor, store port.Persistence, ctxProvider port.ContextProvider) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if tools != nil {
		h.realTools = make(map[string]port.ToolExecutor)
		for _, t := range tools {
			if t == nil {
				continue
			}
			if _, exists := h.realTools[t.Name()]; !exists {
				h.realTools[t.Name()] = t
			}
		}
	}
	h.realStore = store
	h.realContext = ctxProvider
}

// BuildCatalog 依据真实工具集合生成发送给 agent.dll 的 catalog，作为 AgentInit.catalog。
// toolsByRole 以角色为键（总线约定的小写标签：cio/planner/quant/risk/trader，DLL 侧 roleMap 兼容）。
func BuildCatalog(toolsByRole map[string][]port.ToolExecutor) []map[string]interface{} {
	var catalog []map[string]interface{}
	for role, tools := range toolsByRole {
		if role == "" || len(tools) == 0 {
			continue
		}
		var tl []map[string]interface{}
		for _, t := range tools {
			if t == nil {
				continue
			}
			def := t.GetDefinition()
			tl = append(tl, map[string]interface{}{
				"name":        def.Function.Name,
				"description": def.Function.Description,
				"parameters":  def.Function.Parameters,
			})
		}
		if len(tl) == 0 {
			continue
		}
		catalog = append(catalog, map[string]interface{}{"role": role, "tools": tl})
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
// kind ∈ "tool"|"store"|"context"。已绑定真实实现在先，否则确定性兜底。
// "llm" 请求已在架构上取消（LLM 归 DLL 自持），故返回错误回执。
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
		return mustJSON(map[string]interface{}{"error": "llm 维度已归 DLL 自持，宿主不再经总线兑现"})
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
