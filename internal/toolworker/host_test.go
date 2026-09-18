package toolworker

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/quantpilot/quantpilot/internal/port"
)

// ---- 记录型真实实现（实现开源抽象，证明「真实绑定」路由生效）----

// recTool 记录调用并返回固定结果的真实工具执行器。
type recTool struct {
	name   string
	called int
	lastID string
}

func (t *recTool) Name() string        { return t.name }
func (t *recTool) Description() string { return "真实绑定工具 " + t.name }
func (t *recTool) GetDefinition() port.ToolDefinition {
	return port.ToolDefinition{
		Type: "function",
		Function: port.ToolFunction{
			Name:        t.name,
			Description: t.Description(),
			Parameters:  map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		},
	}
}
func (t *recTool) Execute(_ context.Context, args map[string]interface{}) (interface{}, error) {
	t.called++
	id, _ := args["instrument_id"].(string)
	t.lastID = id
	return map[string]interface{}{"tool": t.name, "status": "realm", "instrument_id": id, "value": 42}, nil
}

// recStore 记录调用的真实 port.Persistence。
type recStore struct {
	logTaskStartCalled bool
	clearCalled        bool
}

func (s *recStore) ClearTaskLogs(taskDate string) error {
	s.clearCalled = true
	return nil
}
func (s *recStore) LogTaskStart(taskDate, taskPhase, agentRole, taskName string, taskOrder int) *port.TaskLog {
	s.logTaskStartCalled = true
	return &port.TaskLog{ID: 7}
}
func (s *recStore) LogTaskComplete(taskID uint, deliverableType, deliverableName string, deliverableData interface{}, summary string) {
}
func (s *recStore) LogTaskFailed(taskID uint, errMsg string) {}
func (s *recStore) WriteAudit(eventID, eventType, userID, userName, action, targetType, targetID, result, detailsJSON string, ts time.Time) error {
	return nil
}
func (s *recStore) SaveCIODecision(decisionID, portfolioID, decision, reason, riskApproval, policyStatus, marketState string, marketConfidence float64, ts time.Time) error {
	return nil
}

// TestBindRealRoutesToReal 验证 BindReal 后各维度请求由真实实现兑现（非确定性兜底）。
func TestBindRealRoutesToReal(t *testing.T) {
	tool := &recTool{name: "get_positions"}
	store := &recStore{}
	ctxFlag := false
	ctxProvider := func(ctx context.Context, role string, date string) string {
		ctxFlag = true
		if role == "CIO" && date == "2026-09-18" {
			return "系统上下文"
		}
		return ""
	}

	host := &Host{}
	host.BindReal([]port.ToolExecutor{tool}, store, ctxProvider)

	// tool → 真实执行器被调用，且返回它的结果（含 "realm" 标记，非确定性 "status ok" note）
	tr := host.HandleRequest("tool", `{"name":"get_positions","args":{"instrument_id":"600000"}}`)
	var tm map[string]interface{}
	if err := json.Unmarshal([]byte(tr), &tm); err != nil {
		t.Fatalf("tool 结果非 JSON: %v (%s)", err, tr)
	}
	if st, _ := tm["status"].(string); st != "realm" {
		t.Fatalf("期望真实工具 status=realm, got %q (%s)", st, tr)
	}
	if v, _ := tm["value"].(float64); v != 42 {
		t.Fatalf("期望真实工具 value=42, got %v", tm["value"])
	}
	if tool.called != 1 || tool.lastID != "600000" {
		t.Fatalf("真实工具未被正确路由: called=%d lastID=%q", tool.called, tool.lastID)
	}
	if host.RealTool != 1 {
		t.Fatalf("期望 tool_real=1, got %d", host.RealTool)
	}

	// store → 真实 Persistence 被调用，返回 task_id=7
	sr := host.HandleRequest("store", `{"op":"log_task_start","task_date":"2026-09-18","task_phase":"PRE_MARKET","agent_role":"CIO","task_name":"cio","task_order":4}`)
	var sres map[string]interface{}
	_ = json.Unmarshal([]byte(sr), &sres)
	if !store.logTaskStartCalled {
		t.Fatalf("真实 store 未被调用: %s", sr)
	}
	if tid, _ := sres["task_id"].(float64); tid != 7 {
		t.Fatalf("期望 task_id=7, got %v (%s)", sres["task_id"], sr)
	}

	// context → 注入 provider 被调用
	cr := host.HandleRequest("context", `{"role":"CIO","date":"2026-09-18"}`)
	var cval string
	if err := json.Unmarshal([]byte(cr), &cval); err != nil || cval != "系统上下文" {
		t.Fatalf("真实 context 未被正确兑现: got=%q err=%v", cval, err)
	}
	if !ctxFlag {
		t.Fatal("注入的 context provider 未被调用")
	}
}

// TestDeterministicFallback 验证未绑定真实实现时各维度仍走确定性兜底。
func TestDeterministicFallback(t *testing.T) {
	host := &Host{}
	if out := host.HandleRequest("context", `{"role":"CIO","date":"2026-09-18"}`); out != `""` {
		t.Fatalf("确定性 context 应返回空串: %s", out)
	}
	tr := host.HandleRequest("tool", `{"name":"get_positions","args":{}}`)
	var tm map[string]interface{}
	if err := json.Unmarshal([]byte(tr), &tm); err != nil {
		t.Fatalf("确定性 tool 应返回 JSON: %v (%s)", err, tr)
	}
	if note, _ := tm["note"].(string); note == "" {
		t.Fatalf("确定性 tool 应带 note 标记: %s", tr)
	}
	sr := host.HandleRequest("store", `{"op":"log_task_start","task_date":"d"}`)
	var sres map[string]interface{}
	_ = json.Unmarshal([]byte(sr), &sres)
	if tid, _ := sres["task_id"].(float64); tid != 1 {
		t.Fatalf("确定性 store 首个 task_id=1, got %v", sres["task_id"])
	}
	// 第二次递增
	host.HandleRequest("store", `{"op":"log_task_start","task_date":"d"}`)
	if c := host.Counters(); c["tool_real"] != 0 || c["store_real"] != 0 || c["context_real"] != 0 {
		t.Fatalf("确定性路径不应有 real 计数")
	}
}

// TestBuildCatalog 验证真实工具集合能生成 DLL catalog（role 小写 + 工具元数据）。
func TestBuildCatalog(t *testing.T) {
	tool := &recTool{name: "get_positions"}
	catalog := BuildCatalog(map[string][]port.ToolExecutor{"cio": {tool}})
	if len(catalog) != 1 {
		t.Fatalf("期望 1 个角色目录, got %d", len(catalog))
	}
	role, _ := catalog[0]["role"].(string)
	tools, _ := catalog[0]["tools"].([]map[string]interface{})
	if role != "cio" {
		t.Fatalf("期望 role=cio, got %q", role)
	}
	if len(tools) != 1 {
		t.Fatalf("期望 1 件工具, got %d", len(tools))
	}
	if name, _ := tools[0]["name"].(string); name != "get_positions" {
		t.Fatalf("期望工具名 get_positions, got %q", name)
	}
}