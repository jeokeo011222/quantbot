package sixdim

import (
	"math"
	"testing"
	"time"

	"github.com/quantpilot/quantpilot/internal/port"
)

// 权重总和必须为 1.0，保证加权总分落在 [0,100] 与维度得分同量纲。
func TestDimWeightsSumToOne(t *testing.T) {
	var sum float64
	for _, k := range DimOrder {
		sum += DimWeights[k]
	}
	if math.Abs(sum-1.0) > 1e-9 {
		t.Fatalf("维度权重之和应为1.0，实际 %.4f", sum)
	}
	// DimOrder 与 DimWeights 键一致
	for _, k := range DimOrder {
		if _, ok := DimWeights[k]; !ok {
			t.Fatalf("DimOrder 含未配置权重维度: %s", k)
		}
	}
}

// 每个维度评分应限制在 [0,100]，中性输入不应极端。
func TestScoreClampedAndNeutral(t *testing.T) {
	cases := []struct {
		name string
		got  func() float64
	}{
		{"tech_neutral", func() float64 { return dim1TechScore(DimTech{false, false, 0}) }},
		{"volume_neutral", func() float64 { return dim3VolumeScore(DimVolume{TotalAmtRatio: 1, PriceVolumeMatch: 0}) }},
		{"capital_neutral", func() float64 { return dim4CapitalScore(DimCapital{NorthContinuous: 0, CapitalConcentrate: false}) }},
		{"sentiment_neutral", func() float64 { return dim5SentimentScore(DimSentiment{}) }},
		{"external_neutral", func() float64 { return dim6ExternalScore(DimExternal{}) }},
	}
	for _, c := range cases {
		v := c.got()
		if v < 0 || v > 100 {
			t.Errorf("%s 得分越界: %.2f", c.name, v)
		}
	}
}

// 技术趋势：全面向好应满分，全面走弱应0分；逐项对方向性负责。
func TestDim1TechScore(t *testing.T) {
	if v := dim1TechScore(DimTech{IndexAboveMA20: true, IndexAboveMA60: true, TrendState: 1}); v != 100 {
		t.Fatalf("全面多头应100，实际 %.2f", v)
	}
	if v := dim1TechScore(DimTech{false, false, -1}); v != 0 {
		t.Fatalf("全面空头应0，实际 %.2f", v)
	}
	// 仅站上MA20未站上MA60(45) < 站上MA20+MA60(85)，MA60提供增量
	onlyMA20 := dim1TechScore(DimTech{IndexAboveMA20: true, IndexAboveMA60: false, TrendState: 0})
	ma20ma60 := dim1TechScore(DimTech{IndexAboveMA20: true, IndexAboveMA60: true, TrendState: 0})
	if !(onlyMA20 < ma20ma60) {
		t.Fatalf("站上MA60应增加得分：onlyMA20=%.2f ma20ma60=%.2f", onlyMA20, ma20ma60)
	}
	// 明确下行(空头)应低于默认50
	if v := dim1TechScore(DimTech{false, false, -1}); v >= 50 {
		t.Fatalf("空头趋势不应≥50，实际 %.2f", v)
	}
}

// 市场广度：上涨占比越高、新高越多、涨跌停比越高、主线越多 → 分越高。
func TestDim2BreadthScore(t *testing.T) {
	weak := dim2BreadthScore(DimBreadth{RisePct: 0.1, HighLowRatio: 0.2, LimitUpLimitDownRatio: 0.1, HotlineCount: 0})
	strong := dim2BreadthScore(DimBreadth{RisePct: 0.8, HighLowRatio: 2, LimitUpLimitDownRatio: 2, HotlineCount: 5})
	if !(weak < 50 && strong > 60) {
		t.Fatalf("广度弱=%v应<50，强=%v应>60", weak, strong)
	}
	if v := dim2BreadthScore(DimBreadth{RisePct: 1, HighLowRatio: 3, LimitUpLimitDownRatio: 3, HotlineCount: 5}); v > 100 {
		t.Fatalf("广度得分越界: %.2f", v)
	}
}

// 量能：价涨量增加分、价跌放量减分、极端放/缩量被clamp封顶。
func TestDim3VolumeScore(t *testing.T) {
	if v := dim3VolumeScore(DimVolume{TotalAmtRatio: 3, PriceVolumeMatch: 1}); v != 100 {
		t.Fatalf("极端放量+量价齐升应100，实际 %.2f", v)
	}
	if v := dim3VolumeScore(DimVolume{TotalAmtRatio: 3, PriceVolumeMatch: -1}); v != 100 {
		t.Fatalf("极端放量(±80)即使量价背离也被 clamp 到100，实际 %.2f", v)
	}
	if v := dim3VolumeScore(DimVolume{TotalAmtRatio: 0, PriceVolumeMatch: -1}); v != 0 {
		t.Fatalf("极端缩量+量价背离应0，实际 %.2f", v)
	}
}

// 资金结构：连续流入且资金集中 → 偏多；集中度下降会改善评分。
func TestDim4CapitalScore(t *testing.T) {
	ctrl := dim4CapitalScore(DimCapital{NorthContinuous: 6, CapitalConcentrate: true})
	if v := dim4CapitalScore(DimCapital{NorthContinuous: -6, CapitalConcentrate: false}); v != 25 {
		t.Fatalf("连续流出+资金游击应为基准下限25(50-20-5)，实际 %.2f", v)
	}
	if ctrl <= 50 {
		t.Fatalf("资金集中+连续流入应>50，实际 %.2f", ctrl)
	}
}

// 情绪：涨停越多分越高、炸板率/跌停越多分越低、越界被clamp。
func TestDim5SentimentScore(t *testing.T) {
	hot := dim5SentimentScore(DimSentiment{LimitUpCnt: 100, BlowUpRate: 0, NonStLimitDown: 0})
	if v := dim5SentimentScore(DimSentiment{LimitUpCnt: 0, BlowUpRate: 1, NonStLimitDown: 10}); v != 0 {
		t.Fatalf("情绪冰点(无涨停/高炸板/多跌停)应0，实际 %.2f", v)
	}
	if hot != 80 {
		t.Fatalf("情绪极热应80(50+30)，实际 %.2f", hot)
	}
}

// 外部约束：事件风险显著压分；缺口连续化被clamp且受方向微调叠加。
func TestDim6ExternalScore(t *testing.T) {
	if v := dim6ExternalScore(DimExternal{OvernightGapPct: 2, OvernightUS: 1}); v != 75 {
		t.Fatalf("高开+外围向好应75(50+20+5)，实际 %.2f", v)
	}
	// 事件风险从75压到45
	if v := dim6ExternalScore(DimExternal{OvernightGapPct: 2, OvernightUS: 1, EventRisk: true}); v != 45 {
		t.Fatalf("事件风险应压分30到45，实际 %.2f", v)
	}
	if v := dim6ExternalScore(DimExternal{OvernightGapPct: -2, OvernightUS: -1}); v != 25 {
		t.Fatalf("低开+外围利空应25(50-20-5)，实际 %.2f", v)
	}
}

// CheckDimConflict：仅当高分(≥75)与低分(≤40)同现时计数。
func TestCheckDimConflict(t *testing.T) {
	if c := CheckDimConflict(map[string]float64{"tech": 50, "breadth": 60, "volume": 55, "capital": 50, "sentiment": 65, "external": 50}); c != 0 {
		t.Fatalf("无冲突应0，实际 %d", c)
	}
	if c := CheckDimConflict(map[string]float64{"tech": 90, "breadth": 50, "volume": 50, "capital": 50, "sentiment": 50, "external": 50}); c != 0 {
		t.Fatalf("仅一抹高分不算冲突，实际 %d", c)
	}
	if c := CheckDimConflict(map[string]float64{"tech": 90, "breadth": 30, "volume": 50, "capital": 50, "sentiment": 50, "external": 50}); c != 2 {
		t.Fatalf("高分+低分并存应2，实际 %d", c)
	}
	// 两高分+三低分
	if c := CheckDimConflict(map[string]float64{"tech": 80, "breadth": 85, "volume": 35, "capital": 30, "sentiment": 20, "external": 50}); c != 5 {
		t.Fatalf("2高分+3低分应5，实际 %d", c)
	}
}

// ScoreToPosition 分数-仓位-标签映射。
func TestScoreToPosition(t *testing.T) {
	cases := []struct {
		adj  float64
		rate float64
		tag  string
	}{
		{100, 1.0, "强势"},
		{90, 0.9, "强势"},
		{80, 0.8, "强势"},
		{70, 0.5, "结构性震荡"},
		{60, 0.4, "结构性震荡"},
		{50, 0.25, "偏弱"},
		{40, 0.2, "偏弱"},
		{30, 0.08, "退潮风险"},
		{0, 0, "退潮风险"},
	}
	for _, c := range cases {
		g, tag := ScoreToPosition(c.adj)
		if tag != c.tag {
			t.Errorf("adj=%.0f 标签应%s，实际%s", c.adj, c.tag, tag)
		}
		if math.Abs(g-c.rate) > 0.001 {
			t.Errorf("adj=%.0f 仓位应%.3f，实际%.3f", c.adj, c.rate, g)
		}
	}
}

// 端到端：中性市场 → 总分接近50分附近；强势市场 → 高分+强势+高仓位；弱势市场 → 退潮风险+低仓位。
func TestEvaluateEndToEnd(t *testing.T) {
	neutral := Evaluate(&MarketInput{
		Dim1Tech:      DimTech{false, false, 0},
		Dim2Breadth:   DimBreadth{RisePct: 0.5},
		Dim3Volume:    DimVolume{TotalAmtRatio: 1},
		Dim5Sentiment: DimSentiment{},
	})
	if neutral.RawTotalScore < 40 || neutral.RawTotalScore > 60 {
		t.Fatalf("中性市场总分应在40-60，实际 %.2f", neutral.RawTotalScore)
	}

	bull := Evaluate(&MarketInput{
		Dim1Tech:      DimTech{true, true, 1},
		Dim2Breadth:   DimBreadth{RisePct: 0.7, HighLowRatio: 2, LimitUpLimitDownRatio: 2, HotlineCount: 4},
		Dim3Volume:    DimVolume{TotalAmtRatio: 1.5, PriceVolumeMatch: 1},
		Dim4Capital:   DimCapital{NorthContinuous: 5, CapitalConcentrate: true},
		Dim5Sentiment: DimSentiment{LimitUpCnt: 80, BlowUpRate: 0.1},
		Dim6External:  DimExternal{OvernightGapPct: 2, OvernightUS: 1},
	})
	if bull.MarketTag != "强势" || bull.PositionRate < 0.8 {
		t.Fatalf("强势市场应标签强势且仓位≥0.8，实际标签=%s 仓位=%.2f 总分=%.2f", bull.MarketTag, bull.PositionRate, bull.AdjustedTotalScore)
	}

	bear := Evaluate(&MarketInput{
		Dim1Tech:      DimTech{false, false, -1},
		Dim2Breadth:   DimBreadth{RisePct: 0.2, HighLowRatio: 0.3, LimitUpLimitDownRatio: 0.2},
		Dim3Volume:    DimVolume{TotalAmtRatio: 0.6, PriceVolumeMatch: -1},
		Dim4Capital:   DimCapital{NorthContinuous: -5},
		Dim5Sentiment: DimSentiment{LimitUpCnt: 5, BlowUpRate: 0.6, NonStLimitDown: 8},
		Dim6External:  DimExternal{OvernightGapPct: -2, OvernightUS: -1, EventRisk: true},
	})
	if bear.MarketTag != "退潮风险" || bear.PositionRate > 0.1 {
		t.Fatalf("弱势市场应退潮风险且仓位≤0.1，实际标签=%s 仓位=%.2f 总分=%.2f", bear.MarketTag, bear.PositionRate, bear.AdjustedTotalScore)
	}
}

// 冲突修正：存在明显矛盾(高热度+高情绪 vs 低广度)时折价20%，调整后<原始。
func TestEvaluateConflictDiscount(t *testing.T) {
	in := &MarketInput{
		Dim1Tech:      DimTech{true, true, 1},    // 高分
		Dim2Breadth:   DimBreadth{RisePct: 0.05}, // 低分（广度极差）
		Dim3Volume:    DimVolume{TotalAmtRatio: 1},
		Dim4Capital:   DimCapital{},
		Dim5Sentiment: DimSentiment{},
	}
	rep := Evaluate(in)
	if rep.ConflictCount < 2 {
		t.Fatalf("应存在矛盾维度，实际冲突数=%d", rep.ConflictCount)
	}
	if rep.AdjustedTotalScore >= rep.RawTotalScore {
		t.Fatalf("冲突折价后调整分应<原始分：raw=%.2f adj=%.2f", rep.RawTotalScore, rep.AdjustedTotalScore)
	}
	if math.Abs(rep.RawTotalScore*0.8-rep.AdjustedTotalScore) > 0.01 {
		t.Fatalf("冲突时应折价20%%：raw=%.2f adj=%.2f", rep.RawTotalScore, rep.AdjustedTotalScore)
	}
}

// ==================== 实时取数映射（tradex-hub 参考实现） ====================

// 北向当日净流入 → 连续流入口径：流入为正、流出为负、量级越大越强，±100亿封顶±5。
func TestNorthboundToContinuous(t *testing.T) {
	cases := []struct {
		net  float64
		want int
	}{
		{370, 5},   // 当日大幅净流入 → 封顶 +5
		{100, 5},   // 临界封顶
		{60, 3},    // 60/20=3
		{10, 0},    // 10/20=0.5 → int 截断 0（方向中性偏多但量级不足1档）
		{-30, -1},  // 流出30亿 → -1
		{-150, -5}, // 大幅流出 → 封顶 -5
		{0, 0},
	}
	for _, c := range cases {
		if got := northboundToContinuous(c.net); got != c.want {
			t.Errorf("northboundToContinuous(%.0f) 应=%d 实际=%d", c.net, c.want, got)
		}
	}
	// 净流入映射后得分应高于中性50；净流出应低于中性
	inflow := dim4CapitalScore(DimCapital{NorthContinuous: northboundToContinuous(60), CapitalConcentrate: true})
	outflow := dim4CapitalScore(DimCapital{NorthContinuous: northboundToContinuous(-60), CapitalConcentrate: true})
	if !(inflow > 60 && outflow < 60) {
		t.Fatalf("北向流入应推高资金结构分(%.1f)，流出应压低(%.1f)", inflow, outflow)
	}
}

// 融资余额连续增减天数：乱序输入先排序，方向以最新一天为准，封顶±5。
func TestMarginTrendDays(t *testing.T) {
	asc := func(vals ...float64) []port.MarginPoint {
		pts := make([]port.MarginPoint, 0, len(vals))
		for i, v := range vals {
			pts = append(pts, port.MarginPoint{Date: fmtDate(20260101 + i), Balance: v})
		}
		return pts
	}
	desc := func(pts []port.MarginPoint) []port.MarginPoint { // 东财接口按日期倒序
		out := make([]port.MarginPoint, len(pts))
		for i, p := range pts {
			out[len(pts)-1-i] = p
		}
		return out
	}

	if got := marginTrendDays(asc(100, 101, 102, 103, 104, 105)); got != 5 {
		t.Fatalf("连续上升6天应=5(封顶)，实际 %d", got)
	}
	if got := marginTrendDays(asc(105, 104, 103, 102)); got != -3 {
		t.Fatalf("连续下降应=-3，实际 %d", got)
	}
	if got := marginTrendDays(asc(100, 101, 102, 100, 101)); got != 1 {
		t.Fatalf("最新一日上升(前日回落中断)应=1，实际 %d", got)
	}
	// 倒序输入（东财真实返回顺序）结果一致
	base := asc(100, 101, 102, 103, 104, 105)
	if got := marginTrendDays(desc(base)); got != 5 {
		t.Fatalf("倒序输入连续上升应=5，实际 %d", got)
	}
	if got := marginTrendDays(asc(100)); got != 0 {
		t.Fatalf("单日数据应=0(中性)，实际 %d", got)
	}
	if got := marginTrendDays(nil); got != 0 {
		t.Fatalf("空数据应=0(中性)，实际 %d", got)
	}
}

func fmtDate(ymd int) string {
	return time.Date(ymd/10000, time.Month((ymd/100)%100), ymd%100, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
}

// ttlCache：未过期命中、过期失效、并发安全。
func TestTTLCache(t *testing.T) {
	c := newTTLCache[int](50 * time.Millisecond)
	if _, ok := c.Get(); ok {
		t.Fatal("未缓存应返回 ok=false")
	}
	c.Set(42)
	if v, ok := c.Get(); !ok || v != 42 {
		t.Fatalf("缓存命中应返回42，实际 %d ok=%v", v, ok)
	}
	time.Sleep(60 * time.Millisecond)
	if _, ok := c.Get(); ok {
		t.Fatal("过期后应返回 ok=false")
	}
	c.Set(7)
	if v, ok := c.Get(); !ok || v != 7 {
		t.Fatalf("重新写入后应命中7，实际 %d ok=%v", v, ok)
	}
}

// 实时资金/情绪数据注入 Evaluate 后总分仍落在 [0,100]，且方向与预期一致。
func TestEvaluateWithRealtimeSources(t *testing.T) {
	// 北向大幅净流入 + 实时涨停池热度高 → 资金/情绪维度明显偏多
	rep := Evaluate(&MarketInput{
		Dim1Tech:      DimTech{true, true, 1},
		Dim2Breadth:   DimBreadth{RisePct: 0.6},
		Dim3Volume:    DimVolume{TotalAmtRatio: 1.2},
		Dim4Capital:   DimCapital{NorthContinuous: northboundToContinuous(80), CapitalConcentrate: true},
		Dim5Sentiment: DimSentiment{LimitUpCnt: 90, BlowUpRate: 0.15},
	})
	if rep.DimScores["capital"] < 60 || rep.DimScores["sentiment"] < 60 {
		t.Fatalf("实时流入+高热度应资金/情绪均>60：capital=%.1f sentiment=%.1f",
			rep.DimScores["capital"], rep.DimScores["sentiment"])
	}
	if rep.AdjustedTotalScore < 0 || rep.AdjustedTotalScore > 100 {
		t.Fatalf("总分越界: %.2f", rep.AdjustedTotalScore)
	}
}

// 环境变量可覆盖数据源启用开关与优先级（部署可配置，对标 PanWatch marketdata config）。
func TestSourceConfigEnvOverride(t *testing.T) {
	t.Setenv("SIXDIM_NORTHBOUND_ENABLED", "false")
	t.Setenv("SIXDIM_MARGIN_PRIORITY", "1")
	t.Setenv("SIXDIM_OVERNIGHT_PRIORITY", "badvalue") // 非法值应被忽略，保持默认
	cfg := DefaultSourceConfig()
	if cfg.NorthboundEnabled {
		t.Fatal("SIXDIM_NORTHBOUND_ENABLED=false 应关闭北向源")
	}
	if cfg.MarginPriority != 1 {
		t.Fatalf("SIXDIM_MARGIN_PRIORITY=1 应生效，实际 %d", cfg.MarginPriority)
	}
	if cfg.OvernightPriority != 1 {
		t.Fatalf("非法优先级值应被忽略保持默认1，实际 %d", cfg.OvernightPriority)
	}
	if !cfg.LimitBoardEnabled || cfg.LimitBoardPriority != 1 {
		t.Fatalf("未设置环境变量的源应保持默认: limit_board enabled=%v priority=%d",
			cfg.LimitBoardEnabled, cfg.LimitBoardPriority)
	}
}
