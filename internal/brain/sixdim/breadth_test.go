package sixdim

import (
	"testing"
	"time"

	"github.com/quantpilot/quantpilot/internal/brain/port"
)

// buildBars 构造升序日期的 bars（仅用于聚合测试，日期只保证递增），PreClose 默认=Close。
func buildBars(closes ...float64) []port.FactorBar {
	bars := make([]port.FactorBar, len(closes))
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i, c := range closes {
		bars[i] = port.FactorBar{
			Symbol:   "sh600000",
			Date:     base.AddDate(0, 0, i),
			Close:    c,
			High:     c*1.01 + 0.5,
			Low:      c * 0.99,
			PreClose: c,
		}
	}
	return bars
}

// 聚合统计：涨/跌家数、20日新高/新低、涨跌停计数基于代表样本真实K线/实时覆盖价。
func TestAggregateSampleStatsCounts(t *testing.T) {
	// 标的A：21日升序，最新=21 为20日新高且上涨（前收需低于最新）
	highBars := buildBars(1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21)
	highBars[len(highBars)-1].PreClose = 20 // 最新21 > 前收20 → 上涨
	highBars[len(highBars)-1].Symbol = "sh600000"

	// 标的B：21日降序，最新=1 为20日新低且下跌
	lowBars := buildBars(21, 20, 19, 18, 17, 16, 15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1)
	lowBars[len(lowBars)-1].PreClose = 2 // 最新1 < 前收2 → 下跌
	lowBars[len(lowBars)-1].Symbol = "sz000001"

	stats := aggregateSampleStats(map[string][]port.FactorBar{
		"sh600000": highBars,
		"sz000001": lowBars,
	})
	if stats.N != 2 {
		t.Fatalf("有效样本应为2，实际 %d", stats.N)
	}
	if stats.RiseCount != 1 || stats.DownCount != 1 {
		t.Fatalf("应涨1跌1，实际 涨=%d 跌=%d", stats.RiseCount, stats.DownCount)
	}
	if stats.High20 != 1 || stats.Low20 != 1 {
		t.Fatalf("应新高1新低1，实际 新高=%d 新低=%d", stats.High20, stats.Low20)
	}
}

// 涨跌停比：分母为0时用分子代理强度（防除零）。
func TestLimitRatio(t *testing.T) {
	cases := []struct {
		up, down int
		want     float64
	}{
		{3, 1, 3},
		{2, 2, 1},
		{2, 0, 2}, // 无跌停，用涨停数代理
		{0, 0, 0}, // 双零回0
		{0, 2, 0},
	}
	for _, c := range cases {
		if got := limitRatio(c.up, c.down); got != c.want {
			t.Errorf("limitRatio(%d,%d) 应=%.2f 实际=%.2f", c.up, c.down, c.want, got)
		}
	}
}

// 20日新高/新低 off-by-one 判别：第「21根倒数的历史高点」应真正挡住新高判定。
// 旧实现 window=bars[len-20:] 把当前bar也纳入（恒等比较无效）导致实际只比对19根，
// 会漏掉第20根历史（bars[len-21]）的高点，误判为新高；修复后 window 取前20根历史(bars[len-21..len-2])应正确判为否。
func TestAggregateSampleStatsNewHighWindowOffByOne(t *testing.T) {
	closes := make([]float64, 25)
	for i := range closes {
		closes[i] = 5
	}
	closes[4] = 100 // bars[len-21]，即第20根倒数的高点，远高于最新
	closes[24] = 10 // 最新=10，低于 bars[4]=100，故不应计为20日新高
	bars := buildBars(closes...)
	// buildBars 默认 PreClose==Close；这里最新=10 相对前收=10 为平盘，不影响新高判定
	bars[len(bars)-1].Symbol = "sh600000"

	stats := aggregateSampleStats(map[string][]port.FactorBar{"sh600000": bars})
	if stats.N != 1 {
		t.Fatalf("应1个样本，实际 %d", stats.N)
	}
	if stats.High20 != 0 {
		t.Fatalf("存在20根历史内更高价(bars[4]=100>10)，最新10不应计为20日新高；实际 High20=%d（off-by-one）", stats.High20)
	}
	if stats.Low20 != 0 {
		t.Fatalf("最新10高于众多5，不应计为20日新低；实际 Low20=%d", stats.Low20)
	}
}

// fakeRealtimeSource 仅实现 SnapSource（Source + GetStockSnapshots），用于验证实时覆盖逻辑。
type fakeRealtimeSource struct{ snaps []port.StockSnapshot }

func (f *fakeRealtimeSource) Source() string { return "fake" }
func (f *fakeRealtimeSource) GetStockSnapshots(_ []string) ([]port.StockSnapshot, error) {
	return f.snaps, nil
}

// 实时覆盖：最新价/前收/最高被真实快照覆盖，Mock 与无报价样本跳过，key 归一化为 sh600519。
func TestApplyRealtimeOverlay(t *testing.T) {
	over := &fakeRealtimeSource{snaps: []port.StockSnapshot{
		{Market: "sh", Code: "600519", CurrentPrice: 15, PrevClose: 10, High: 16},
		{Market: "sz", Code: "000001", CurrentPrice: 0, PrevClose: 0},               // 无效报价，跳过
		{Market: "sh", Code: "600036", CurrentPrice: 8, PrevClose: 7, IsMock: true}, // Mock，跳过
	}}
	f := &Fetcher{snap: over}

	base := time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)
	barsMap := map[string][]port.FactorBar{
		"sh600519": {{Symbol: "sh600519", Date: base, Close: 11, PreClose: 10, High: 13}}, // date DESC 最新在0
		"sz000001": {{Symbol: "sz000001", Date: base, Close: 5, PreClose: 5, High: 6}},
		"sh600036": {{Symbol: "sh600036", Date: base, Close: 9, PreClose: 8, High: 10}},
	}
	codes := []string{"sh600519", "sz000001", "sh600036"}
	rt := f.applyRealtimeOverlay(barsMap, codes)

	if rt.queries != 3 || rt.covered != 1 {
		t.Fatalf("应查询3覆盖1，实际 查询=%d 覆盖=%d", rt.queries, rt.covered)
	}
	got := barsMap["sh600519"][0]
	if got.Close != 15 || got.PreClose != 10 || got.High != 16 {
		t.Fatalf("sh600519 未正确覆盖：close=%.1f pre=%.1f high=%.1f", got.Close, got.PreClose, got.High)
	}
	if barsMap["sz000001"][0].Close != 5 {
		t.Fatal("无效报价不应被覆盖")
	}
	if barsMap["sh600036"][0].Close != 9 {
		t.Fatal("Mock 快照不应被覆盖")
	}
}