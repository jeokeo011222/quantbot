package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/quantpilot/quantpilot/internal/port"
)

// TestE2EDailyWorkflow_DLLOnly 端到端：宿主持有的 port.Agent（数据侧占位）注册默认处理器，
// 在进程内 Agent 决策已迁入 agent.dll 的隔离前提下，验证 planner→quant→risk→cio→trader
// 权限链自动推进，且：
//   - CIO 步骤产出 DLL-only 默认 HOLD 决策并被落库、传递到 Trader；
//   - Risk 未否决（DLL-only 不否决）；
//   - Trader 步骤产出执行输出的 DLL-only 标记。
// 覆盖 Orchestrator 的权限编排骨架（不含进程内 LLM 决策路径）。
func TestE2EDailyWorkflow_DLLOnly(t *testing.T) {
	o := newOrchForTest(t)

	// 用宿主持有的 port.Agent（数据侧占位）走 RegisterDefaultHandlers 注册
	team := map[port.AgentRole]*port.Agent{
		port.RolePlanner: port.NewAgent("planner-1", port.RolePlanner),
		port.RoleQuant:   port.NewAgent("quant-1", port.RoleQuant),
		port.RoleRisk:    port.NewAgent("risk-1", port.RoleRisk),
		port.RoleCIO:     port.NewAgent("cio-1", port.RoleCIO),
		port.RoleTrader:  port.NewAgent("trader-1", port.RoleTrader),
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
			// DLL-only 下 CIO 决策应为默认 HOLD 并被传递到任务
			if tk.Decision == nil {
				t.Fatal("CIO 决策未传递到任务")
			}
			if tk.Decision.Decision != "HOLD" {
				t.Fatalf("CIO 决策应为默认 HOLD, got %s", tk.Decision.Decision)
			}
			// Risk DLL-only 不否决
			if tk.VetoResult != nil {
				t.Fatalf("Risk 不应触发否决: %+v", tk.VetoResult)
			}
			// Trader 步骤应有 DLL-only 输出
			if last := tk.Steps[len(tk.Steps)-1]; last.Output == nil {
				t.Fatal("Trader 步骤缺少输出")
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("端到端链路超时未完成（权限编排未跑通）")
}