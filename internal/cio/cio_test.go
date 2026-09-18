package cio

import (
	"context"
	"testing"

	"github.com/quantpilot/quantpilot/internal/brain/agents"
	"github.com/quantpilot/quantpilot/internal/policy"
)

// newTestCIOEngine 构造最小 CIOEngine（含 Policy 与四个智能体，其余依赖为 nil），
// 用于验证紧急停止门控：若门控失效，方法会因访问 nil db/portfolio 而 panic 或返回非预期结果。
func newTestCIOEngine() *CIOEngine {
	pe := policy.NewPolicyEngine(nil)
	return &CIOEngine{
		agent:       agents.NewAgent("cio_test", agents.RoleCIO, nil, nil, nil),
		quantAgent:  agents.NewAgent("quant_test", agents.RoleQuant, nil, nil, nil),
		riskAgent:   agents.NewAgent("risk_test", agents.RoleRisk, nil, nil, nil),
		traderAgent: agents.NewAgent("trader_test", agents.RoleTrader, nil, nil, nil),
		policyEngine: pe,
	}
}

// TestEmergencyStopBlocksOrchestration 验证紧急停止后所有 CIO 编排入口立即返回，智能体停止一切工作
func TestEmergencyStopBlocksOrchestration(t *testing.T) {
	eng := newTestCIOEngine()
	eng.EmergencyStop()
	ctx := context.Background()

	// 盘中监控：不执行任何分析/决策/下单
	if err := eng.MonitorIntradayInvestmentPlan(ctx); err != nil {
		t.Fatalf("MonitorIntradayInvestmentPlan should return nil when stopped, got err: %v", err)
	}

	// 每日检查：返回 BLOCKED 决策而非执行分析
	decision, err := eng.RunDailyCheck(ctx, 1_000_000, 0)
	if err != nil {
		t.Fatalf("RunDailyCheck should return nil err when stopped, got: %v", err)
	}
	if decision == nil || decision.PolicyStatus != "BLOCKED" {
		t.Fatalf("RunDailyCheck should return BLOCKED decision when stopped, got: %+v", decision)
	}

	// 决策制定：返回 nil
	if d := eng.FormulateDecision(ctx, nil, nil, nil); d != nil {
		t.Fatalf("FormulateDecision should return nil when stopped, got: %+v", d)
	}

	// 止损执行：返回 0（若门控失效，portfolio 为 nil 时会 panic）
	if n := eng.ExecuteStopLossIfTriggered(ctx); n != 0 {
		t.Fatalf("ExecuteStopLossIfTriggered should return 0 when stopped, got %d", n)
	}

	// 下单执行：portfolio 为 nil，若门控失效会 panic；门控后应直接返回
	eng.executeDecision(ctx, &agents.CIODecision{
		DecisionID: "test-dec",
		Orders:     []agents.OrderIntent{{Side: "BUY", Symbol: "sh600000", MaxNotional: 1000}},
	})

	// 拆单执行：portfolio 为 nil，若门控失效会 panic；门控后应直接返回
	eng.executePendingSplits(ctx)

	// 可交易池自动买卖/审核：返回空结果且无错误
	if bought, err := eng.AutoBuyFromTradeablePool(ctx, 1000); err != nil || bought != nil {
		t.Fatalf("AutoBuyFromTradeablePool should return nil,nil when stopped, got (%v, %v)", bought, err)
	}
	if sold, err := eng.AutoSellFromTradeablePool(ctx); err != nil || sold != nil {
		t.Fatalf("AutoSellFromTradeablePool should return nil,nil when stopped, got (%v, %v)", sold, err)
	}
	if sum, err := eng.ReviewPendingTradeableStocks("t", "t"); err != nil || sum != nil {
		t.Fatalf("ReviewPendingTradeableStocks should return nil,nil when stopped, got (%v, %v)", sum, err)
	}
	if n, err := eng.ReviewPendingPlans(ctx); err != nil || n != 0 {
		t.Fatalf("ReviewPendingPlans should return 0,nil when stopped, got (%d, %v)", n, err)
	}

	// 恢复工作：智能体全部恢复
	eng.ResumeTrading()
	if eng.agent.IsStopped() {
		t.Fatal("agent should NOT be stopped after ResumeTrading")
	}
	if eng.quantAgent.IsStopped() || eng.riskAgent.IsStopped() || eng.traderAgent.IsStopped() {
		t.Fatal("all agents should be resumed after ResumeTrading")
	}
	if eng.policyEngine.IsEmergencyStopped() {
		t.Fatal("policy should NOT be emergency stopped after ResumeTrading")
	}
}

// TestEmergencyStopFlags 验证紧急停止/恢复后状态标志正确翻转
func TestEmergencyStopFlags(t *testing.T) {
	eng := newTestCIOEngine()

	eng.EmergencyStop()
	if !eng.emergencyStopped() {
		t.Fatal("emergencyStopped() should be true after EmergencyStop")
	}

	eng.ResumeTrading()
	if eng.emergencyStopped() {
		t.Fatal("emergencyStopped() should be false after ResumeTrading")
	}
}
