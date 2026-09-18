package cio

import "testing"

func TestMergeResearchTimeline(t *testing.T) {
	six := []ResearchSixDim{
		{TradeDate: "2026-09-08", MarketTag: "强势", AdjustedScore: 78, PositionRate: 0.7},
		{TradeDate: "2026-09-07", MarketTag: "强势", AdjustedScore: 82, PositionRate: 0.75},
		{TradeDate: "2026-09-04", MarketTag: "结构性震荡", AdjustedScore: 55, PositionRate: 0.5},
	}
	claims := []ResearchClaim{
		{ClaimDate: "2026-09-08", Direction: "bullish", Confidence: 72, Verified: true, Match: true, ActualReturn: 0.8},
		{ClaimDate: "2026-09-04", Direction: "neutral", Confidence: 50, Verified: true, Match: false, ActualReturn: -0.3},
	}
	days := mergeResearchTimeline(six, claims)
	if len(days) != 3 {
		t.Fatalf("期望 3 天，实际 %d", len(days))
	}
	// 同日期主张应被关联（merge 保持输入顺序：9-08 在 days[0]）
	if days[0].Claim == nil || days[0].Claim.Direction != "bullish" {
		t.Fatalf("9-08 应关联 bullish 主张，实际 %+v", days[0].Claim)
	}
	// 无主张的日期（9-07）Claim 应为 nil
	if days[1].Claim != nil {
		t.Fatalf("9-07 应无主张，实际 %+v", days[1].Claim)
	}
	// 9-04 应关联 neutral 主张
	if days[2].Claim == nil || days[2].Claim.Direction != "neutral" {
		t.Fatalf("9-04 应关联 neutral 主张，实际 %+v", days[2].Claim)
	}
}

func TestStatTimelineSummary_HitRate(t *testing.T) {
	days := []ResearchDay{
		{TradeDate: "09-06", Claim: &ResearchClaim{Verified: true, Match: true}},
		{TradeDate: "09-07", Claim: &ResearchClaim{Verified: true, Match: false}},
		{TradeDate: "09-08", Claim: &ResearchClaim{Verified: false}},
		{TradeDate: "09-09"}, // 无主张
	}
	got := statTimelineSummary(days)
	if got["days"].(int) != 4 {
		t.Fatalf("days 应为 4，实际 %v", got["days"])
	}
	if got["verified"].(int) != 2 {
		t.Fatalf("verified 应为 2，实际 %v", got["verified"])
	}
	if got["direction_hit_rate_pct"].(float64) != 50 {
		t.Fatalf("命中率应为 50，实际 %v", got["direction_hit_rate_pct"])
	}
}

func TestStatTimelineSummary_NoVerified(t *testing.T) {
	days := []ResearchDay{
		{TradeDate: "09-08", Claim: &ResearchClaim{Verified: false}},
	}
	got := statTimelineSummary(days)
	if got["direction_hit_rate_pct"].(float64) != 0 {
		t.Fatalf("无已验证主张时命中率应为 0，实际 %v", got["direction_hit_rate_pct"])
	}
}
