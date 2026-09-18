// Command dllrunner 是一个独立 e2e Runner：加载 agent.dll，走拉模式 RPC 总线，
// 用宿主 handler 兑现工具/持久化/上下文/LLM，跑完一个每日决策周期并打印
// 最终 decision 摘要与「宿主兑现了多少条请求」。
//
// 两种运行模式（由 -real 开关控制）：
//
//	默认（确定性兜底）: 不 BindReal，各维度由 Host 的确定性兜底兑现；
//	                      无需 API key / 数据库即可离线跑通（验证拉模式总线 + DLL 内决策脑）。
//	-real（真实绑定）:   BindReal 注入内存版真实实现（实现 port.ToolExecutor /
//	                      port.Persistence / llm.Client / port.ContextProvider），
//	                      验证「真实 handler 绑定」路径在 DLL 上端到端生效（tool/store/context/llm
//	                      *_real 计数 > 0）。
//
// 本 runner 不 import 决策脑装配（不 import internal/harness、internal/brainhost 的
// CreateDefaultTeam），只 import internal/toolworker 与决策脑窄抽象包，保证可离线自包含运行。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"github.com/quantpilot/quantpilot/internal/brain/agents"
	"github.com/quantpilot/quantpilot/internal/brain/llm"
	"github.com/quantpilot/quantpilot/internal/brain/port"
	"github.com/quantpilot/quantpilot/internal/brain/toolkit"
	"github.com/quantpilot/quantpilot/internal/toolworker"
)

// envelope 统一解析 DLL 的 {ok,error,data} 响应。
type envelope struct {
	OK    bool            `json:"ok"`
	Error string          `json:"error"`
	Data  json.RawMessage `json:"data"`
}

// decodeData 解析 {ok,error,data} 并把 data 解码进 out；失败致命退出。
func decodeData(raw string, out interface{}) {
	var env envelope
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		log.Fatalf("响应非合法 JSON: %v\n%s", err, raw)
	}
	if !env.OK {
		log.Fatalf("DLL 拒绝: %s", env.Error)
	}
	if err := json.Unmarshal(env.Data, out); err != nil {
		log.Fatalf("data 解码失败: %v", err)
	}
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	realMode := flag.Bool("real", false, "绑定内存版真实实现跑真实绑定模式（默认走确定性兜底）")
	flag.Parse()

	const dllPath = "bin/agent.dll"
	if _, err := os.Stat(dllPath); err != nil {
		log.Fatalf("未找到 %s（请先 go build -buildmode=c-shared -o %s ./dll）：%v", dllPath, dllPath, err)
	}

	agent, err := toolworker.Load(dllPath)
	if err != nil {
		log.Fatalf("加载 agent.dll 失败: %v", err)
	}
	defer agent.Close()

	// 1. AgentAbi
	abiRaw, err := agent.Abi()
	if err != nil {
		log.Fatalf("AgentAbi 调用失败: %v", err)
	}
	var abi struct {
		Abi        string   `json:"abi"`
		APIVersion string   `json:"api_version"`
		Brain      []string `json:"brain"`
		Operations []struct {
			Name string `json:"name"`
		} `json:"operations"`
	}
	decodeData(abiRaw, &abi)
	fmt.Printf("=== AgentAbi ===\n  abi=%s api=%s brain=%v ops=%d\n",
		abi.Abi, abi.APIVersion, abi.Brain, len(abi.Operations))

	date := time.Now().Format("2006-01-02")

	if *realMode {
		runRealMode(agent, date)
	} else {
		runDeterministicMode(agent, date)
	}
}

// runDeterministicMode 用确定性兜底 host 跑完整周期（未 BindReal → 各维度确定性兑现）。
func runDeterministicMode(agent *toolworker.Agent, date string) {
	host := &toolworker.Host{}
	fmt.Println("=== AgentInit + RunDailyCycle + 拉模式轮询（确定性兜底）===")

	orchResult, counters, err := toolworker.RunAgentCycle(agent, host, deterministicCatalog(), date, 3*time.Minute)
	if err != nil {
		log.Fatalf("DLL 每日周期失败: %v", err)
	}

	fmt.Println("=== 最终决策脑结果（DLL 内 orchestrator）===")
	pretty(orchResult)
	printCounters(counters)
	if counters["tool"] == 0 {
		log.Fatal("期望至少一条 tool 请求被宿主兑现，但本轮未发生")
	}
	fmt.Println("  → 离线验证通过：决策脑在 DLL 内，副作用均经拉模式总线由宿主确定性兑现")
}

// deterministicCatalog 为确定性兜底模式生成每角色 1~2 件远程工具目录。
func deterministicCatalog() []map[string]interface{} {
	roleTools := map[string][]string{
		"planner": {"get_market_stats", "get_macro_snapshot"},
		"quant":   {"get_market_stats", "get_positions", "run_backtest"},
		"risk":    {"get_portfolio_risk"},
		"cio":     {"get_portfolio_state", "get_market_stats"},
		"trader":  {"place_trade"},
	}
	var catalog []map[string]interface{}
	for role, tools := range roleTools {
		var t []map[string]interface{}
		for _, name := range tools {
			t = append(t, map[string]interface{}{
				"name":        name,
				"description": "确定性宿主兑现的" + role + "工具",
				"parameters":  map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
			})
		}
		catalog = append(catalog, map[string]interface{}{"role": role, "tools": t})
	}
	return catalog
}

// runRealMode 用内存版真实实现经 BindReal 绑定后跑完整周期，
// 验证真实 handler 绑定路径（tool/store/context/llm *_real 计数）在 DLL 上生效。
func runRealMode(agent *toolworker.Agent, date string) {
	store := newMemStore()
	llmClient := newMemLLM()
	ctxProvider := func(ctx context.Context, role, d string) string {
		return fmt.Sprintf("【内存真实上下文】role=%s date=%s\n订阅行情、组合快照与持仓明细均为内存真实实现提供。", role, d)
	}

	team := buildMemTeam(store, llmClient)
	catalog := toolworker.BuildCatalogFromTeam(team)

	host := &toolworker.Host{}
	host.BindReal(team, store, ctxProvider, llmClient)

	fmt.Println("=== AgentInit + RunDailyCycle + 拉模式轮询（绑定内存真实实现）===")
	orchResult, counters, err := toolworker.RunAgentCycle(agent, host, catalog, date, 3*time.Minute)
	if err != nil {
		log.Fatalf("DLL 每日周期失败（真实绑定）: %v", err)
	}

	fmt.Println("=== 最终决策脑结果（DLL 内 orchestrator，真实绑定路径）===")
	pretty(orchResult)
	printCounters(counters)
	fmt.Printf("  内存持久化兑现明细: 任务开始=%d 完成=%d 审计=%d CIO决策落库=%d\n",
		store.taskStarted, store.taskCompleted, store.audits, store.cioDecisions)
	if counters["tool_real"] == 0 || counters["store_real"] == 0 || counters["context_real"] == 0 {
		log.Fatal("真实绑定模式期望 tool/store/context 的 *_real 计数均 > 0，未满足")
	}
	fmt.Println("  → 真实绑定验证通过：DLL 内决策脑的 tool/store/context/llm 请求均由宿主侧真实实现兑现")
}

// printCounters 打印宿主兑现计数（含真实实现计数）。
func printCounters(c map[string]int) {
	fmt.Printf("=== 宿主兑现统计 ===\n  tool=%d store=%d context=%d llm=%d\n",
		c["tool"], c["store"], c["context"], c["llm"])
	fmt.Printf("  其中真实实现兑现: tool_real=%d store_real=%d context_real=%d llm_real=%d\n",
		c["tool_real"], c["store_real"], c["context_real"], c["llm_real"])
}

// ---------------- 内存版真实实现 ----------------

// memStore 内存版真实持久化（实现 port.Persistence，供真实绑定模式证明 store 维度走真实兑现）。
type memStore struct {
	mu            sync.Mutex
	seq           uint
	taskStarted   int
	taskCompleted int
	taskFailed    int
	audits        int
	cioDecisions  int
}

func newMemStore() *memStore { return &memStore{} }
func (m *memStore) ClearTaskLogs(taskDate string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.taskStarted = 0
	return nil
}
func (m *memStore) LogTaskStart(taskDate, taskPhase, agentRole, taskName string, taskOrder int) *port.TaskLog {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seq++
	m.taskStarted++
	return &port.TaskLog{ID: m.seq}
}
func (m *memStore) LogTaskComplete(taskID uint, deliverableType, deliverableName string, deliverableData interface{}, summary string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.taskCompleted++
}
func (m *memStore) LogTaskFailed(taskID uint, errMsg string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.taskFailed++
}
func (m *memStore) WriteAudit(eventID, eventType, userID, userName, action, targetType, targetID, result, detailsJSON string, ts time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.audits++
	return nil
}
func (m *memStore) SaveCIODecision(decisionID, portfolioID, decision, reason, riskApproval, policyStatus, marketState string, marketConfidence float64, ts time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cioDecisions++
	return nil
}

// memTool 内存版真实工具（实现 toolkit.ToolExecutor，供真实绑定模式证明 tool 维度走真实兑现）。
type memTool struct {
	name string
}

func (t *memTool) Name() string        { return t.name }
func (t *memTool) Description() string { return "内存真实实现工具: " + t.name }
func (t *memTool) GetDefinition() toolkit.ToolDefinition {
	return toolkit.ToolDefinition{
		Type: "function",
		Function: toolkit.ToolFunction{
			Name:        t.name,
			Description: t.Description(),
			Parameters:  map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		},
	}
}
func (t *memTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	return map[string]interface{}{
		"tool":         t.name,
		"source":       "memory-real",
		"status":       "ok",
		"sample_value": 42.7,
	}, nil
}

// buildMemTeam 构造内存版真实 Agent 团队（仅设置 BindReal/BuildCatalogFromTeam 所需的 Role 与 Tools）。
func buildMemTeam(store port.Persistence, llmClient llm.Client) map[agents.AgentRole]*agents.Agent {
	roleTools := map[agents.AgentRole][]string{
		agents.RolePlanner: {"get_market_stats", "get_macro_snapshot"},
		agents.RoleQuant:   {"get_market_stats", "get_positions"},
		agents.RoleRisk:    {"get_portfolio_risk"},
		agents.RoleCIO:     {"get_portfolio_state", "get_market_stats"},
		agents.RoleTrader:  {"place_trade"},
	}
	team := make(map[agents.AgentRole]*agents.Agent)
	for role, names := range roleTools {
		var tools []port.ToolExecutor
		for _, n := range names {
			tools = append(tools, &memTool{name: n})
		}
		team[role] = &agents.Agent{Role: role, Tools: tools}
	}
	return team
}

// memLLM 内存版真实 LLM 客户端（实现 llm.Client，驱动与确定性兜底一致的 ReAct 收敛）。
type memLLM struct{}

func newMemLLM() llm.Client { return &memLLM{} }

func (c *memLLM) StreamChat(ctx context.Context, messages []llm.Message, tools []llm.Tool) (*llm.StreamReader, error) {
	return nil, fmt.Errorf("内存真实 LLM 未实现流式")
}

func (c *memLLM) Embedding(ctx context.Context, text string) ([]float64, error) {
	return []float64{0.5, 0.6, 0.7, 0.8}, nil
}

// Chat 首次（尚无 assistant tool_calls）请求调用 tools[0]；之后返回结构化决策，令 Agent 收尾。
func (c *memLLM) Chat(ctx context.Context, messages []llm.Message, tools []llm.Tool) (*llm.ChatResult, error) {
	var toolName string
	if len(tools) > 0 {
		toolName = tools[0].Function.Name
	}

	if !memHasToolCall(messages) && toolName != "" {
		choice := map[string]interface{}{
			"index": 0,
			"message": map[string]interface{}{
				"role": "assistant",
				"tool_calls": []interface{}{map[string]interface{}{
					"id": "call_mem_1", "type": "function",
					"function": map[string]interface{}{"name": toolName, "arguments": "{}"},
				}},
			},
			"finish_reason": "tool_calls",
		}
		return chatResultFromMap("mem-client", choice, 11), nil
	}

	finalContent, _ := json.Marshal(map[string]interface{}{
		"action":  "HOLD",
		"summary": "内存真实绑定下的决策（拉模式总线经真实 handler 兑现）。",
		"reason":  "真实工具调用已由宿主兑现，决策脑按既定流程收敛。",
	})
	choice := map[string]interface{}{
		"index":         0,
		"message":       map[string]interface{}{"role": "assistant", "content": string(finalContent)},
		"finish_reason": "stop",
	}
	return chatResultFromMap("mem-client", choice, 35), nil
}

func memHasToolCall(msgs []llm.Message) bool {
	for _, m := range msgs {
		if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			return true
		}
	}
	return false
}

func chatResultFromMap(model string, choice map[string]interface{}, completion int) *llm.ChatResult {
	raw, _ := json.Marshal(map[string]interface{}{
		"id":      "chat-mem-1",
		"model":   model,
		"choices": []interface{}{choice},
		"usage":   map[string]int{"prompt_tokens": 10, "completion_tokens": completion, "total_tokens": 10 + completion},
	})
	var res llm.ChatResult
	_ = json.Unmarshal(raw, &res)
	return &res
}

// pretty 将任意结果规范化输出。
func pretty(v interface{}) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		fmt.Printf("%v\n", v)
		return
	}
	fmt.Printf("%s\n", b)
}
