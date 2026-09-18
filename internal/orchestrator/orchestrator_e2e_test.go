package orchestrator

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/quantpilot/quantpilot/internal/brain/agents"
	"github.com/quantpilot/quantpilot/internal/brain/llm"
)

// mockLLM 实现 llm.Client 接口，按 system prompt 中的角色返回预置 JSON，
// 模拟真实 LLM 输出（与各 Agent 的 OUTPUT SCHEMA 字段一致）。
type mockLLM struct{}

func (m *mockLLM) Chat(ctx context.Context, messages []llm.Message, tools []llm.Tool) (*llm.ChatResult, error) {
	sys := ""
	for _, msg := range messages {
		if msg.Role == "system" {
			sys = msg.Content
		}
	}

	var content string
	switch {
	case strings.Contains(sys, "Investment Planner"):
		content = `{"plan_id":"P1","market_regime":"BULL","asset_allocation":{"equity":0.6,"bond":0.2,"cash":0.2},"target_return":0.2,"target_volatility":0.15,"strategy_notes":"均衡配置"}`
	case strings.Contains(sys, "Quantitative Analyst"):
		content = `{"signal_id":"Q1","model":"multi-factor","factor_scores":{"momentum":60,"value":40},"top_picks":["600519"],"confidence":0.8,"analysis_notes":"看多"}`
	case strings.Contains(sys, "Risk Manager"):
		content = `{"risk_id":"R1","var_95":0.02,"var_99":0.04,"volatility":0.18,"max_dd":0.1,"beta":0.9,"decision":"APPROVE","violations":[],"risk_notes":"风险可控"}`
	case strings.Contains(sys, "senior CIO"):
		content = `{"decision":"REDUCE_RISK","reason":"回调风险","confidence":0.85,"target_position":0.2,"notes":"降低仓位"}`
	case strings.Contains(sys, "Trader"):
		content = `{"order_id":"T1","status":"FILLED","message":"已按决策执行"}`
	default:
		content = `{}`
	}

	resp := map[string]interface{}{
		"choices": []interface{}{
			map[string]interface{}{
				"index":         0,
				"message":       map[string]interface{}{"role": "assistant", "content": content},
				"finish_reason": "stop",
			},
		},
	}
	b, _ := json.Marshal(resp)
	var r llm.ChatResult
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

func (m *mockLLM) StreamChat(ctx context.Context, messages []llm.Message, tools []llm.Tool) (*llm.StreamReader, error) {
	return nil, nil
}

func (m *mockLLM) Embedding(ctx context.Context, text string) ([]float64, error) {
	return nil, nil
}

// TestE2ERealAgentsDailyWorkflow 端到端：真实 Agent（mock LLM）驱动完整权限链，
// 验证 planner→quant→risk→cio→trader 自动推进，且 CIO 的 AI 决策被真实采纳并传递到 Trader。
func TestE2ERealAgentsDailyWorkflow(t *testing.T) {
	o := newOrchForTest(t)

	// 用真实 Agent（注入 mock LLM），走 RegisterDefaultHandlers 注册
	team := map[agents.AgentRole]*agents.Agent{
		agents.RolePlanner: agents.NewAgent("planner-1", agents.RolePlanner, nil, &mockLLM{}, nil),
		agents.RoleQuant:   agents.NewAgent("quant-1", agents.RoleQuant, nil, &mockLLM{}, nil),
		agents.RoleRisk:    agents.NewAgent("risk-1", agents.RoleRisk, nil, &mockLLM{}, nil),
		agents.RoleCIO:     agents.NewAgent("cio-1", agents.RoleCIO, nil, &mockLLM{}, nil),
		agents.RoleTrader:  agents.NewAgent("trader-1", agents.RoleTrader, nil, &mockLLM{}, nil),
	}
	o.RegisterDefaultHandlers(team)

	ctx := context.Background()
	task, err := o.CreateTask("daily_investment", "system", "每日投资决策", "desc")
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	if err := o.StartTask(ctx, task.ID); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	if err := o.ExecuteNextStep(ctx, task.ID); err != nil {
		t.Fatalf("第一步执行失败: %v", err)
	}

	// 等待异步链路跑完（5秒上限）
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tk := o.GetTask(task.ID)
		if tk != nil && tk.Status == StatusSuccess {
			// 5步全部成功
			for _, s := range tk.Steps {
				if s.Status != StatusSuccess {
					t.Fatalf("步骤 %s 未完成: %s", s.ID, s.Status)
				}
			}
			// CIO 的 AI 决策 REDUCE_RISK 应被真实解析并传递
			if tk.Decision == nil {
				t.Fatal("CIO 决策未传递到任务")
			}
			if tk.Decision.Decision != "REDUCE_RISK" {
				t.Fatalf("CIO 决策应为 REDUCE_RISK, got %s", tk.Decision.Decision)
			}
			if tk.Decision.Confidence != 0.85 || tk.Decision.PositionLimit != 0.2 {
				t.Fatalf("CIO 决策字段解析错误: %+v", tk.Decision)
			}
			// Risk 未否决（APPROVE）
			if tk.VetoResult != nil {
				t.Fatalf("Risk 不应触发否决: %+v", tk.VetoResult)
			}
			// Trader 步骤应有执行输出
			if last := tk.Steps[len(tk.Steps)-1]; last.Output == nil {
				t.Fatal("Trader 步骤缺少输出")
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("端到端链路超时未完成（真实 Agent 驱动编排未跑通）")
}
