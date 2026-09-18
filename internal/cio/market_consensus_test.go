package cio

import (
	"testing"

	"github.com/quantpilot/quantpilot/internal/marketsixdim"
)

func TestComputeMarketConsensus_StrongBullish(t *testing.T) {
	rep := &sixdim.MarketReport{
		MarketTag: "强势", AdjustedTotalScore: 80, ConflictCount: 0, PositionRate: 0.8,
		DimScores: map[string]float64{"tech": 85, "breadth": 80, "volume": 75, "capital": 78, "sentiment": 82, "external": 60},
	}
	c := ComputeMarketConsensus(rep)
	if c.Direction != "bullish" {
		t.Fatalf("强分强势应 bullish，实际 %s", c.Direction)
	}
	if c.Strength < 0.5 {
		t.Fatalf("无冲突高分共识强度应较高，实际 %.2f", c.Strength)
	}
	if len(c.ValidateTomorrow) == 0 {
		t.Fatal("应生成次日待验证条件")
	}
}

func TestComputeMarketConsensus_Retreat(t *testing.T) {
	rep := &sixdim.MarketReport{
		MarketTag: "退潮风险", AdjustedTotalScore: 30, ConflictCount: 3, PositionRate: 0.2,
		DimScores: map[string]float64{"tech": 35, "breadth": 30, "volume": 25, "capital": 40, "sentiment": 20, "external": 45},
	}
	c := ComputeMarketConsensus(rep)
	if c.Direction != "bearish" {
		t.Fatalf("退潮风险应 bearish，实际 %s", c.Direction)
	}
	if c.Strength >= 0.5 {
		t.Fatalf("冲突多时共识强度应被折减，实际 %.2f", c.Strength)
	}
	if len(c.ValidateTomorrow) == 0 {
		t.Fatal("退潮也应生成待验证条件")
	}
}

func TestComputeMarketConsensus_Nil(t *testing.T) {
	c := ComputeMarketConsensus(nil)
	if c.Direction != "neutral" || len(c.ValidateTomorrow) != 0 {
		t.Fatalf("nil 应返回空 neutral 共识，实际 %+v", c)
	}
}

func TestComputeMarketConsensus_Divergence(t *testing.T) {
	rep := &sixdim.MarketReport{
		MarketTag: "结构性震荡", AdjustedTotalScore: 55, ConflictCount: 2, PositionRate: 0.5,
		DimScores: map[string]float64{"tech": 80, "breadth": 55, "volume": 50, "capital": 45, "sentiment": 50, "external": 48},
	}
	c := ComputeMarketConsensus(rep)
	if len(c.Divergences) == 0 {
		t.Fatal("反差明显时应有分歧记录")
	}
}
