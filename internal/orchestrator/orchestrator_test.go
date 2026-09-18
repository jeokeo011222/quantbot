package orchestrator

import (
	"context"
	"sync"
	"testing"
	"time"
)

// ==================== Mock Handler ====================
// mockHandler 实现 TaskHandler 接口，用可配置输出替代真实 AI agent，
// 用于隔离验证编排器状态机/权限/决策/否决逻辑。

type mockHandler struct {
	orch       *Orchestrator
	role       string
	perm       RolePermission
	mu         sync.Mutex
	veto       *VetoResult     // 非nil时输出 VetoResult（模拟 Risk 否决）
	decision   *DecisionObject // 非nil时输出 DecisionObject（模拟 CIO 决策）
	shouldFail bool
}

func (m *mockHandler) GetRole() string               { return m.role }
func (m *mockHandler) GetPermission() RolePermission { return m.perm }

func (m *mockHandler) Handle(ctx context.Context, task *WorkflowTask, step *TaskStep) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.shouldFail {
		return nil
	}
	// 与真实 handler 一致：通过 orchestrator.setStepOutput 加锁写入步骤输出，避免数据竞争。
	if m.veto != nil {
		m.orch.setStepOutput(task.ID, step.ID, m.veto)
		return nil
	}
	// 输出 decision：模拟 CIO 返回 *DecisionObject
	if m.decision != nil {
		m.orch.setStepOutput(task.ID, step.ID, m.decision)
		return nil
	}
	// 通用输出
	m.orch.setStepOutput(task.ID, step.ID, map[string]interface{}{"role": m.role, "ok": true})
	return nil
}

func newOrchForTest(t *testing.T) *Orchestrator {
	t.Helper()
	o := NewOrchestrator(nil)
	return o
}

func registerAllMock(o *Orchestrator, f func(role string) *mockHandler) {
	for _, r := range []struct {
		role string
		perm RolePermission
	}{
		{"planner", PermPropose},
		{"quant", PermEvidence},
		{"risk", PermVeto},
		{"cio", PermDecide},
		{"trader", PermExecute},
	} {
		h := f(r.role)
		h.orch = o
		o.RegisterHandler(h)
	}
}

// TestTemplatesRegistered 模板注册完整性（3+1 模型 + 投资规划）
func TestTemplatesRegistered(t *testing.T) {
	o := newOrchForTest(t)
	tpls := o.ListTemplates()
	expected := []string{"daily_investment", "daily_review", "research", "event_workflow", "investment_planning"}
	if len(tpls) != len(expected) {
		t.Fatalf("期望注册%d个模板, got %d", len(expected), len(tpls))
	}
	for _, id := range expected {
		if o.GetTemplate(id) == nil {
			t.Fatalf("模板 %s 未注册", id)
		}
	}
}

// TestCreateTaskIdempotent 同一天同一模板只创建一个任务（幂等）
func TestCreateTaskIdempotent(t *testing.T) {
	o := newOrchForTest(t)
	t1, err := o.CreateTask("daily_investment", "system", "任务1", "desc")
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	t2, err := o.CreateTask("daily_investment", "system", "任务2", "desc")
	if err != nil {
		t.Fatalf("第二次创建失败: %v", err)
	}
	if t1.ID != t2.ID {
		t.Fatalf("幂等失败: 同模板同日应返回同一任务, got %s vs %s", t1.ID, t2.ID)
	}
	// 不同模板不幂等
	t3, err := o.CreateTask("research", "system", "任务3", "desc")
	if err != nil {
		t.Fatalf("创建研究任务失败: %v", err)
	}
	if t3.ID == t1.ID {
		t.Fatal("不同模板不应幂等")
	}
}

// TestCreateUserTaskNotIdempotent 用户触发的一次性任务不幂等（每次调用新建任务，如"生成投资方案"）
func TestCreateUserTaskNotIdempotent(t *testing.T) {
	o := newOrchForTest(t)
	t1, err := o.CreateUserTask("investment_planning", "user", "生成投资方案", "d")
	if err != nil {
		t.Fatalf("创建投资规划任务失败: %v", err)
	}
	t2, err := o.CreateUserTask("investment_planning", "user", "生成投资方案", "d")
	if err != nil {
		t.Fatalf("第二次创建投资规划任务失败: %v", err)
	}
	if t1.ID == t2.ID {
		t.Fatal("CreateUserTask 不应幂等，每次应新建任务")
	}
	if t1.TemplateID != "investment_planning" || len(t1.Steps) != 1 || t1.Steps[0].Assignee != "planner" {
		t.Fatalf("投资规划任务结构错误: %+v", t1)
	}
}

// TestStartTaskStateTransition 任务只能从 PENDING 启动
func TestStartTaskStateTransition(t *testing.T) {
	o := newOrchForTest(t)
	task, _ := o.CreateTask("daily_investment", "system", "t", "d")
	ctx := context.Background()
	if err := o.StartTask(ctx, task.ID); err != nil {
		t.Fatalf("首次启动失败: %v", err)
	}
	if err := o.StartTask(ctx, task.ID); err == nil {
		t.Fatal("RUNNING 状态再次启动应报错")
	}
	if err := o.StartTask(ctx, "nonexistent"); err == nil {
		t.Fatal("不存在任务启动应报错")
	}
}

// TestFullDailyWorkflow 完整 AI 驱动链路：planner→quant→risk→cio→trader，
// 使用 mock handler，等待异步完成，验证步骤推进与最终 SUCCESS。
func TestFullDailyWorkflow(t *testing.T) {
	o := newOrchForTest(t)
	handlers := map[string]*mockHandler{}
	registerAllMock(o, func(role string) *mockHandler {
		h := &mockHandler{role: role}
		switch role {
		case "planner":
			h.perm = PermPropose
		case "quant":
			h.perm = PermEvidence
		case "risk":
			h.perm = PermVeto
		case "cio":
			h.perm = PermDecide
			h.decision = &DecisionObject{Decision: "HOLD", Confidence: 0.9}
		case "trader":
			h.perm = PermExecute
		}
		handlers[role] = h
		return h
	})

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

	// 等待异步执行完成（最多5秒）
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if task := o.GetTask(task.ID); task != nil && task.Status == StatusSuccess {
			// 全部5步应 SUCCESS，且 decision 应传递给 task
			for _, s := range task.Steps {
				if s.Status != StatusSuccess {
					t.Fatalf("步骤 %s 未完成: %s", s.ID, s.Status)
				}
			}
			if task.Decision == nil || task.Decision.Decision != "HOLD" {
				t.Fatalf("CIO 决策未正确传递到任务: %+v", task.Decision)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("任务超时未完成（AI 编排链路未跑通）")
}

// TestRiskVeto 验证 Risk 否决是否真正触发任务失败。
// 预期：mock 输出 *VetoResult 且 Vetoed=true 时，任务应转为 FAILED。
func TestRiskVeto(t *testing.T) {
	o := newOrchForTest(t)
	handlers := map[string]*mockHandler{}
	registerAllMock(o, func(role string) *mockHandler {
		h := &mockHandler{role: role}
		switch role {
		case "planner":
			h.perm = PermPropose
		case "quant":
			h.perm = PermEvidence
		case "risk":
			h.perm = PermVeto
			h.veto = &VetoResult{Vetoed: true, Reason: "portfolio over-risk", RiskLevel: "HIGH"}
		case "cio":
			h.perm = PermDecide
		case "trader":
			h.perm = PermExecute
		}
		handlers[role] = h
		return h
	})

	ctx := context.Background()
	task, _ := o.CreateTask("daily_investment", "system", "t", "d")
	o.StartTask(ctx, task.ID)
	if err := o.ExecuteNextStep(ctx, task.ID); err != nil {
		t.Fatalf("第一步执行失败: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tk := o.GetTask(task.ID)
		if tk != nil && (tk.Status == StatusFailed || tk.VetoResult != nil) {
			if tk.VetoResult == nil || !tk.VetoResult.Vetoed {
				t.Fatalf("Veto 未正确记录: %+v", tk.VetoResult)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("Risk 否决未生效（任务未进入 FAILED/VetoResult 未记录）")
}

// TestOptionalStepSkip research 模板第3步(CIO)可选，应被跳过
func TestOptionalStepSkip(t *testing.T) {
	o := newOrchForTest(t)
	registerAllMock(o, func(role string) *mockHandler {
		h := &mockHandler{role: role}
		switch role {
		case "planner":
			h.perm = PermPropose
		case "quant":
			h.perm = PermEvidence
		case "cio":
			h.perm = PermDecide
		}
		return h
	})

	ctx := context.Background()
	task, err := o.CreateTask("research", "system", "t", "d")
	if err != nil {
		t.Fatalf("创建研究任务失败: %v", err)
	}
	o.StartTask(ctx, task.ID)
	if err := o.ExecuteNextStep(ctx, task.ID); err != nil {
		t.Fatalf("第一步执行失败: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tk := o.GetTask(task.ID)
		if tk != nil && tk.Status == StatusSuccess {
			if tk.Steps[2].Status != StatusSkipped {
				t.Fatalf("可选步骤应被跳过, got %s", tk.Steps[2].Status)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("research 任务超时未完成")
}

// TestPermissionDenied 权限不匹配时应拒绝执行
func TestPermissionDenied(t *testing.T) {
	o := newOrchForTest(t)
	// planner 用错误权限注册，模拟 handler 权限不匹配
	o.RegisterHandler(&mockHandler{orch: o, role: "planner", perm: PermExecute})
	ctx := context.Background()
	task, _ := o.CreateTask("daily_investment", "system", "t", "d")
	o.StartTask(ctx, task.ID)
	if err := o.ExecuteNextStep(ctx, task.ID); err == nil {
		t.Fatal("权限不匹配应返回错误")
	}
}

// TestGetTaskDeepCopy 深拷贝隔离：修改 GetTask/ListTasks 返回的拷贝，
// 不应影响编排器内部任务状态（避免外部读取与异步执行写互相污染）。
func TestGetTaskDeepCopy(t *testing.T) {
	o := newOrchForTest(t)
	registerAllMock(o, func(role string) *mockHandler {
		return &mockHandler{role: role, perm: PermPropose}
	})

	ctx := context.Background()
	task, _ := o.CreateTask("daily_investment", "system", "t", "d")
	o.StartTask(ctx, task.ID)
	o.ExecuteNextStep(ctx, task.ID)

	// 等步骤完成写入 Output 后取拷贝
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		cp := o.GetTask(task.ID)
		if cp != nil && cp.Steps[0].Status == StatusSuccess {
			// 篡改拷贝，不应影响内部
			cp.Steps[0].Status = StatusFailed
			cp.Steps[0].Output = map[string]interface{}{"hacked": true}
			cp.Decision = &DecisionObject{Decision: "HACK"}
			internal := o.GetTask(task.ID)
			if internal.Steps[0].Status != StatusSuccess {
				t.Fatalf("深拷贝隔离失败: 内部步骤状态被外部修改: %s", internal.Steps[0].Status)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("步骤未完成，无法验证深拷贝")
}

// TestParseRiskVeto AI 风控 REJECT → 触发否决；APPROVE/解析失败 → 不触发。
func TestParseRiskVeto(t *testing.T) {
	// REJECT 触发否决
	v := parseRiskVeto(`{"decision":"REJECT","risk_notes":"集中度过高","violations":["单一持仓超限"],"risk_level":"HIGH"}`)
	if v == nil || !v.Vetoed {
		t.Fatal("REJECT 应触发否决")
	}
	if v.Reason == "" || v.RiskLevel != "HIGH" {
		t.Fatalf("否决理由/等级错误: %+v", v)
	}
	// APPROVE 不触发
	if v := parseRiskVeto(`{"decision":"APPROVE","risk_notes":"ok"}`); v != nil {
		t.Fatal("APPROVE 不应触发否决")
	}
	// 非法 JSON 不触发
	if v := parseRiskVeto(`not-json`); v != nil {
		t.Fatal("非法 JSON 不应触发否决")
	}
	// 非字符串不触发
	if v := parseRiskVeto(map[string]interface{}{"decision": "REJECT"}); v != nil {
		t.Fatal("非字符串输入不应触发否决")
	}
	// violations 为空时回退 risk_notes
	v2 := parseRiskVeto(`{"decision":"REJECT","risk_notes":"风控理由"}`)
	if v2 == nil || v2.Reason != "风控理由" {
		t.Fatalf("reason 应取自 risk_notes: %+v", v2)
	}
}

// TestParseCIODecision AI CIO 输出 → DecisionObject；缺失/非法 → 回退 HOLD。
func TestParseCIODecision(t *testing.T) {
	// 正常解析
	d := parseCIODecision(`{"decision":"REDUCE_RISK","confidence":0.85,"target_position":0.2,"reason":"回调风险"}`)
	if d == nil {
		t.Fatal("应解析出决策")
	}
	if d.Decision != "REDUCE_RISK" || d.Confidence != 0.85 || d.PositionLimit != 0.2 || d.Reason != "回调风险" {
		t.Fatalf("解析结果错误: %+v", d)
	}
	// 小写 decision 归一化为大写
	d2 := parseCIODecision(`{"decision":"no_action","confidence":0.9,"reason":"观望"}`)
	if d2 == nil || d2.Decision != "NO_ACTION" {
		t.Fatalf("decision 应转大写: %+v", d2)
	}
	// confidence 越界回退 0.5
	d3 := parseCIODecision(`{"decision":"HOLD","confidence":99,"reason":"x"}`)
	if d3 == nil || d3.Confidence != 0.5 {
		t.Fatalf("confidence 越界应回退 0.5: %+v", d3)
	}
	// 缺失 decision → 回退 nil（调用方给 HOLD）
	if d := parseCIODecision(`{"reason":"no decision field"}`); d != nil {
		t.Fatal("缺失 decision 应返回 nil")
	}
	if d := parseCIODecision(123); d != nil {
		t.Fatal("非字符串应返回 nil")
	}
	if d := parseCIODecision(""); d != nil {
		t.Fatal("空字符串应返回 nil")
	}
}
