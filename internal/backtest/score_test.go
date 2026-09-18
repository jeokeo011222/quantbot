package backtest

import (
	"testing"

	"github.com/quantpilot/quantpilot/internal/data"
	"github.com/quantpilot/quantpilot/internal/tdx"
)

// StrategySelectionScore：空指针、全零指标返回0；正常指标按 0.4×收益+0.3×夏普+0.3×胜率 计算。
func TestStrategySelectionScore(t *testing.T) {
	if got := StrategySelectionScore(nil); got != 0 {
		t.Fatalf("nil 策略评分应为0, got %.4f", got)
	}
	zero := &data.Strategy{}
	if got := StrategySelectionScore(zero); got != 0 {
		t.Fatalf("全零指标评分应为0, got %.4f", got)
	}
	st := &data.Strategy{TotalReturn: 10, SharpeRatio: 1.5, WinRate: 60}
	want := 0.4*10 + 0.3*1.5 + 0.3*60
	if got := StrategySelectionScore(st); got != want {
		t.Fatalf("评分=%.4f, expect %.4f", got, want)
	}
}

// SelectBestStrategy：仅考虑活跃且有指标的策略，选出综合分最高者。
func TestSelectBestStrategy(t *testing.T) {
	list := []data.Strategy{
		{Name: "A", StrategyType: "kdj_golden", IsActive: 1, TotalReturn: 10, SharpeRatio: 1.0, WinRate: 55},
		{Name: "B", StrategyType: "ma_cross", IsActive: 1, TotalReturn: 25, SharpeRatio: 2.0, WinRate: 70}, // 最优
		{Name: "C", StrategyType: "turtle_breakout", IsActive: 0, TotalReturn: 99, SharpeRatio: 9.9, WinRate: 99}, // 非活跃，排除
		{Name: "D", StrategyType: "boll", IsActive: 1, TotalReturn: 0, SharpeRatio: 0, WinRate: 0},          // 无指标，排除
	}
	best, typ := SelectBestStrategy(list)
	if typ != "ma_cross" || best.Name != "B" {
		t.Fatalf("应选中 B(ma_cross), got %s/%s", best.Name, typ)
	}
	// 全候选不可用时返回空
	if _, typ2 := SelectBestStrategy([]data.Strategy{{Name: "D", StrategyType: "boll", IsActive: 1}}); typ2 != "" {
		t.Fatalf("无可选策略应返回空类型, got %s", typ2)
	}
}

// EnsureOneLotCapital：资金不足时抬升到1手最高价×1.05缓冲；足够/空数据时原样返回。
func TestEnsureOneLotCapital(t *testing.T) {
	bars := []tdx.KlineBar{{Close: 100}, {Close: 200}, {Close: 150}} // 最高200
	// 资金10万 < 100*200*1.05=21000? 否，21000<10万 → 无需抬升
	if got := EnsureOneLotCapital(bars, 300000); got != 300000 {
		t.Fatalf("资金充足应原样返回, got %.0f", got)
	}
	// 高价股：short 场景最高价2000 → min=210000；10万不足→210000
	hi := []tdx.KlineBar{{Close: 2000}}
	if got := EnsureOneLotCapital(hi, 100000); got != 210000 {
		t.Fatalf("应抬升至210000, got %.0f", got)
	}
	if got := EnsureOneLotCapital(nil, 100000); got != 100000 {
		t.Fatalf("空数据应原样返回, got %.0f", got)
	}
}