package screener

import "testing"

func TestClassifyStockPath_WeakRisk(t *testing.T) {
	r := ClassifyStockPath(PathInput{Momentum1M: 30, Momentum3M: 38, Momentum12M: 30})
	if r.Path != PathWeakRisk {
		t.Fatalf("期望 弱势风险，实际 %s", r.Path)
	}
}

func TestClassifyStockPath_MomentumLimitUp(t *testing.T) {
	r := ClassifyStockPath(PathInput{Momentum1M: 85, Momentum3M: 80, Momentum12M: 60, Volatility: 30})
	if r.Path != PathMomentumLimitUp {
		t.Fatalf("期望 情绪连板，实际 %s", r.Path)
	}
}

func TestClassifyStockPath_TrendCapacity(t *testing.T) {
	r := ClassifyStockPath(PathInput{Momentum1M: 62, Momentum3M: 70, Momentum12M: 72, Volatility: 18, Liquidity: 85})
	if r.Path != PathTrendCapacity {
		t.Fatalf("期望 趋势容量，实际 %s", r.Path)
	}
}

func TestClassifyStockPath_TrendGrowth(t *testing.T) {
	r := ClassifyStockPath(PathInput{Momentum1M: 60, Momentum3M: 68, Momentum12M: 65, Volatility: 20, Liquidity: 40})
	if r.Path != PathTrendGrowth {
		t.Fatalf("期望 趋势成长，实际 %s", r.Path)
	}
}

func TestClassifyStockPath_Oscillation(t *testing.T) {
	r := ClassifyStockPath(PathInput{Momentum1M: 50, Momentum3M: 50, Momentum12M: 50, Volatility: 15})
	if r.Path != PathOscillation {
		t.Fatalf("期望 震荡观察，实际 %s", r.Path)
	}
}
