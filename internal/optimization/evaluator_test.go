package optimization

import (
	"testing"
)

// ============ Evaluator 测试 ============

func TestEvaluator_AllPass(t *testing.T) {
	eval := NewEvaluator()
	candidate := &SampleMetrics{Sharpe: 1.2, Return: 15, MaxDD: 8, WinRate: 55, Trades: 15}
	baseline := &SampleMetrics{Sharpe: 0.8, Return: 10, MaxDD: 10, WinRate: 50, Trades: 12}

	oldCfg := `{"period": 14, "oversold": 30}`
	newCfg := `{"period": 14, "oversold": 32}` // +6.7%，未超 50%

	result := eval.Evaluate(candidate, baseline, oldCfg, newCfg)
	if result.Verdict != VerdictShadowRun {
		t.Errorf("期望 VerdictShadowRun, got %s", result.Verdict)
	}
}

func TestEvaluator_RejectBadSharpe(t *testing.T) {
	eval := NewEvaluator()
	candidate := &SampleMetrics{Sharpe: 0.2, Return: 3, MaxDD: 15, WinRate: 45, Trades: 12}
	baseline := &SampleMetrics{Sharpe: 0.8, Return: 10, MaxDD: 10, WinRate: 50, Trades: 12}

	result := eval.Evaluate(candidate, baseline, `{}`, `{}`)
	if result.Verdict != VerdictRejected {
		t.Errorf("期望 VerdictRejected (夏普过低), got %s", result.Verdict)
	}
}

func TestEvaluator_RejectBadDD(t *testing.T) {
	eval := NewEvaluator()
	candidate := &SampleMetrics{Sharpe: 1.2, Return: 15, MaxDD: 25, WinRate: 55, Trades: 15}
	baseline := &SampleMetrics{Sharpe: 0.8, Return: 10, MaxDD: 10, WinRate: 50, Trades: 12}

	result := eval.Evaluate(candidate, baseline, `{}`, `{}`)
	if result.Verdict != VerdictRejected {
		t.Errorf("期望 VerdictRejected (回撤过差), got %s", result.Verdict)
	}
}

func TestEvaluator_RejectParamDelta(t *testing.T) {
	eval := NewEvaluator()
	candidate := &SampleMetrics{Sharpe: 1.2, Return: 15, MaxDD: 8, WinRate: 55, Trades: 15}
	baseline := &SampleMetrics{Sharpe: 0.8, Return: 10, MaxDD: 10, WinRate: 50, Trades: 12}

	oldCfg := `{"period": 14, "oversold": 30}`
	newCfg := `{"period": 28, "oversold": 60}` // period +100%, oversold +100%

	result := eval.Evaluate(candidate, baseline, oldCfg, newCfg)
	if !result.DeltaExceeded {
		t.Error("期望参数幅度超限")
	}
	if result.Verdict != VerdictRejected {
		t.Errorf("期望 VerdictRejected (参数幅度), got %s", result.Verdict)
	}
}

func TestEvaluator_NoBaseline(t *testing.T) {
	// 没有 baseline 时只检查基础门槛
	eval := NewEvaluator()
	candidate := &SampleMetrics{Sharpe: 1.2, Return: 15, MaxDD: 8, WinRate: 55, Trades: 15}

	result := eval.Evaluate(candidate, nil, `{}`, `{}`)
	if result.Verdict != VerdictShadowRun {
		t.Errorf("期望 VerdictShadowRun (no baseline), got %s", result.Verdict)
	}
}

// ============ Diagnoser 测试 ============

func TestDiagnoser_Healthy(t *testing.T) {
	d := NewDiagnoser()
	in := &DiagnosisInput{
		Last30dSharpe: 1.0, Last30dWinRate: 0.55,
		FactorRankIC: 0.06, SplitHalfStable: true,
		DataQuality: DataQualityOK,
	}
	result := d.Diagnose(in)
	if result.Conclusion != ConclusionHealthy {
		t.Errorf("期望 HEALTHY, got %s", result.Conclusion)
	}
	if ShouldTune(result) {
		t.Error("HEALTHY 不应触发调参")
	}
}

func TestDiagnoser_FailingLogic(t *testing.T) {
	d := NewDiagnoser()
	in := &DiagnosisInput{
		StrategyType:  "rsi_reversal",
		Last30dSharpe: -0.3, Last30dWinRate: 0.20,
		MarketRegime: RegimeBull, // 牛市低胜率 = 卖飞
		FactorRankIC: 0.06, SplitHalfStable: true,
	}
	result := d.Diagnose(in)
	if result.Conclusion != ConclusionFailing {
		t.Errorf("期望 FAILING, got %s", result.Conclusion)
	}
	if !ShouldTune(result) {
		t.Error("FAILING 应触发人工审查")
	}
}

func TestDiagnoser_Degraded(t *testing.T) {
	d := NewDiagnoser()
	in := &DiagnosisInput{
		StrategyType:  "rsi_reversal",
		Last30dSharpe: 0.3, Last30dWinRate: 0.40,
		FactorRankIC: 0.02, SplitHalfStable: false, // 因子衰减
		RegimeShift3d: true, // 市场突变
	}
	result := d.Diagnose(in)
	if result.Conclusion != ConclusionDegraded {
		t.Errorf("期望 DEGRADED, got %s", result.Conclusion)
	}
}

// ============ ShadowRun 测试 ============

func TestShadowRun_AllSame(t *testing.T) {
	sr := NewShadowRun(1, "rsi_reversal", `{}`, `{}`)
	sr.MarkDay("2026-10-20", "BUY", "BUY", sr.WatchDays)
	sr.MarkDay("2026-10-21", "HOLD", "HOLD", sr.WatchDays)
	sr.MarkDay("2026-10-22", "SELL", "SELL", sr.WatchDays)

	verdict, rate := sr.Verdict()
	if verdict != VerdictApproved {
		t.Errorf("期望 APPROVED, got %s (rate=%.2f)", verdict, rate)
	}
}

func TestShadowRun_LowConsensus(t *testing.T) {
	sr := NewShadowRun(1, "rsi_reversal", `{}`, `{}`)
	sr.MarkDay("2026-10-20", "BUY", "SELL", sr.WatchDays)
	sr.MarkDay("2026-10-21", "BUY", "SELL", sr.WatchDays)
	sr.MarkDay("2026-10-22", "BUY", "BUY", sr.WatchDays) // 1/3 = 33%

	verdict, _ := sr.Verdict()
	if verdict != VerdictRejected {
		t.Errorf("期望 REJECTED, got %s", verdict)
	}
}
