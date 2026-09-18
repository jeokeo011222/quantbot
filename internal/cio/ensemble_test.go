package cio

import (
	"testing"
)

func TestEnsembleVote(t *testing.T) {
	cases := []struct {
		name      string
		prop      *llmDecisionProposal
		sk        *skepticVerdict
		db        *debateVerdict
		regime    string
		wantDir   string
		wantNeg   bool // 期望净多分为负（偏空）
		wantModel int
	}{
		{
			name:    "全看多但信心高",
			prop:    &llmDecisionProposal{MarketDirection: "BULLISH", Confidence: 90},
			db:      &debateVerdict{Direction: "BULLISH", Agreement: 85},
			regime:  "强势",
			wantDir: "BULLISH", wantNeg: false, wantModel: 3,
		},
		{
			name:    "主张看多但异议反对+辩论看空+规则退潮",
			prop:    &llmDecisionProposal{MarketDirection: "BULLISH", Confidence: 80},
			sk:      &skepticVerdict{Verdict: "RISK"},
			db:      &debateVerdict{Direction: "BEARISH", Agreement: 90},
			regime:  "退潮风险",
			wantDir: "BEARISH", wantNeg: true, wantModel: 4,
		},
		{
			name:    "主张中性但辩论显著看空 → 收敛为空(更保守)",
			prop:    &llmDecisionProposal{MarketDirection: "NEUTRAL", Confidence: 60},
			db:      &debateVerdict{Direction: "BEARISH", Agreement: 70},
			wantDir: "BEARISH", wantNeg: true, wantModel: 2,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ev := ensembleVote(c.prop, c.sk, c.db, c.regime)
			if ev == nil {
				t.Fatalf("ensembleVote 返回 nil")
			}
			if len(ev.Votes) != c.wantModel {
				t.Errorf("来源数 = %d, 期望 %d (%v)", len(ev.Votes), c.wantModel, ev.Votes)
			}
			if c.wantDir != "" && ev.Direction != c.wantDir {
				t.Errorf("方向 = %s, 期望 %s (净多=%.2f)", ev.Direction, c.wantDir, ev.NetScore)
			}
			if c.wantNeg && ev.NetScore >= 0 {
				t.Errorf("期望净多为负, 实得 %.2f", ev.NetScore)
			}
			if !c.wantNeg && ev.NetScore < 0 {
				t.Errorf("期望净多非负, 实得 %.2f", ev.NetScore)
			}
		})
	}
}

func TestEnsembleVoteNilSourcesNoPanic(t *testing.T) {
	// 所有来源均 nil → 返回非 nil 结果，净多=0，方向=NEUTRAL，不 panic
	ev := ensembleVote(nil, nil, nil, "")
	if ev == nil {
		t.Fatalf("全 nil 来源应返回结果而非 nil")
	}
	if ev.NetScore != 0 || ev.Direction != "NEUTRAL" {
		t.Errorf("期望净多0/NEUTRAL, 实得 %.2f/%s", ev.NetScore, ev.Direction)
	}
	if len(ev.Votes) != 0 {
		t.Errorf("无来源时 Votes 应为空, 实得 %v", ev.Votes)
	}
}

// medianFactorQuality：对极值更稳健的中位数（#6），空/奇偶个数边界。
func TestMedianFactorQuality(t *testing.T) {
	if got := medianFactorQuality(nil); got != 0 {
		t.Errorf("空 map 应返回 0, 实得 %v", got)
	}
	odd := map[string]float64{"a": 0.1, "b": 0.5, "c": 0.9}
	if got := medianFactorQuality(odd); got != 0.5 {
		t.Errorf("奇数个中位数应 0.5, 实得 %v", got)
	}
	even := map[string]float64{"a": 0.2, "b": 0.6, "c": 1.0, "d": 100.0}
	// 中位数=(0.6+1.0)/2=0.8，极大值100不影响（这正是中位数相对均值的价值）
	if got := medianFactorQuality(even); got != 0.8 {
		t.Errorf("偶数个中位数应 0.8(不受极值100影响), 实得 %v", got)
	}
}
