package backtest

import (
	"math"
	"math/rand"
	"testing"

	"github.com/quantpilot/quantpilot/internal/tdx"
)

// genSyntheticBars 生成确定性合成的日K数组：带上升漂移的随机游走 + 周期性回撤，
// 保证双均线能产生足够买卖交叉（否则网格全0成交，无法验证排序逻辑）。
func genSyntheticBars(n int, seed int64) []tdx.KlineBar {
	r := rand.New(rand.NewSource(seed))
	bars := make([]tdx.KlineBar, n)
	close := 100.0
	for i := 0; i < n; i++ {
		// 趋势 + 波动 + 周期回撤，使均线穿越
		trend := 0.15
		cycle := math.Sin(float64(i)/22) * 1.2 // 周期性回撤/反弹
		close += trend + cycle + (r.Float64()-0.5)*3.0
		if close < 5 {
			close = 5
		}
		open := close - (r.Float64()-0.5)*2.0
		high := math.Max(open, close) + r.Float64()
		low := math.Min(open, close) - r.Float64()
		if low < 1 {
			low = 1
		}
		bars[i] = tdx.KlineBar{
			Date:   tdxDate(i),
			Open:   open,
			High:   high,
			Low:    low,
			Close:  close,
			Volume: int64(100000 + r.Int63n(50000)),
		}
	}
	return bars
}

func tdxDate(i int) string {
	// 生成 YYYY-MM-DD（仅用于排序一致性，具体日期不敏感）
	y, m, d := 2018+int(i/360), (i%360+360)/30, (i%30)+1
	return itoa4(y) + "-" + itoa2(m) + "-" + itoa2(d)
}

func itoa4(v int) string   { return pad(v, 4) }
func itoa2(v int) string   { return pad(v, 2) }
func pad(v, w int) string {
	s := ""
	for x := v; x > 0; x /= 10 {
		s = string(rune('0'+x%10)) + s
	}
	for len(s) < w {
		s = "0" + s
	}
	return s
}

func testRunbacktestReq(strategyType string) *RunBacktestRequest {
	return &RunBacktestRequest{
		StrategyType:   strategyType,
		StockCode:      "000001",
		StockName:      "平安银行",
		Market:         "SZ",
		InitialCapital: 100000,
	}
}

func TestParamCombosCount(t *testing.T) {
	combos := paramCombos(map[string][]float64{
		"FastPeriod": {5, 10, 15},
		"SlowPeriod": {20, 30},
	})
	if len(combos) != 6 {
		t.Fatalf("笛卡尔积应为6组，实际 %d", len(combos))
	}
	// 每组的键应一致且数量正确
	for _, c := range combos {
		if _, ok := c["FastPeriod"]; !ok {
			t.Fatalf("组合缺少键 FastPeriod: %v", c)
		}
		if _, ok := c["SlowPeriod"]; !ok {
			t.Fatalf("组合缺少键 SlowPeriod: %v", c)
		}
	}
}

func TestMaCrossDefaultGridFiltersInvalid(t *testing.T) {
	grid := DefaultParamGrid("ma_cross")
	// fast{5,10,15,20} × slow{20,30,40,60} = 16，扣除 slow<=fast 的1组(20/20) = 15
	if len(grid) != 15 {
		t.Fatalf("ma_cross 默认网格应为15组（排除慢<=快），实际 %d", len(grid))
	}
	for _, c := range grid {
		if c["SlowPeriod"] <= c["FastPeriod"] {
			t.Fatalf("出现慢周期<=快周期的非法组合: %v", c)
		}
	}
}

func TestNewStrategyParamOverride(t *testing.T) {
	st, err := NewStrategy("ma_cross", map[string]float64{"FastPeriod": 5, "SlowPeriod": 30})
	if err != nil {
		t.Fatalf("构造失败: %v", err)
	}
	ma, ok := st.(*MACrossStrategy)
	if !ok {
		t.Fatalf("类型断言失败: %T", st)
	}
	if ma.FastPeriod != 5 || ma.SlowPeriod != 30 {
		t.Fatalf("参数未生效: %+v", ma)
	}

	// 非法慢<=快应报错
	if _, err := NewStrategy("ma_cross", map[string]float64{"FastPeriod": 20, "SlowPeriod": 20}); err == nil {
		t.Fatalf("非法参数（慢=快）应返回错误，实际无错误")
	}

	// 未知类型应报错
	if _, err := NewStrategy("no_such_strategy", nil); err == nil {
		t.Fatalf("未知策略应返回错误，实际无错误")
	}
}

func TestScanGridWalkForward(t *testing.T) {
	bars := genSyntheticBars(360, 42)
	grid := DefaultParamGrid("ma_cross")
	req := testRunbacktestReq("ma_cross")

	res, err := scanGrid(grid, bars, req, nil, GridObjSharpe)
	if err != nil {
		t.Fatalf("scanGrid 失败: %v", err)
	}
	if res.CombosTested != len(grid) {
		t.Fatalf("combos_tested=%d 应等于网格组数 %d", res.CombosTested, len(grid))
	}
	if res.TrainBars+res.TestBars != len(bars) {
		t.Fatalf("训练+测试K线数 %d != 总K线 %d", res.TrainBars+res.TestBars, len(bars))
	}
	if res.BestParamMsg == "" {
		t.Fatalf("最优参数展示为空")
	}
	if res.BestParams == nil {
		t.Fatalf("最优参数缺失")
	}
	if len(res.Ranked) != len(grid) {
		t.Fatalf("排序结果数 %d != 网格组数 %d", len(res.Ranked), len(grid))
	}
	// 最优组必须是排序第一
	if res.Ranked[0].ParamMsg != res.BestParamMsg {
		t.Fatalf("最优组(%s)不是排序第一(%s)", res.BestParamMsg, res.Ranked[0].ParamMsg)
	}
	// 排序应按 sharpe 降序（或若含0成交组合，至少前一组有成交）
	if res.Ranked[0].IsTrades == 0 {
		t.Fatalf("最优组0成交，排序兜底失效: %+v", res.Ranked[0])
	}
	// 防前视说明已填充
	if res.Method == "" {
		t.Fatalf("防前视方法说明为空")
	}
}

func TestScanGridMaxComboCap(t *testing.T) {
	bars := genSyntheticBars(300, 7)
	// 构造 5×5×5×5×5 = 3125 组合，应被截断为200
	grid := paramCombos(map[string][]float64{
		"R1": {1, 2, 3, 4, 5},
		"R2": {1, 2, 3, 4, 5},
		"R3": {1, 2, 3, 4, 5},
		"R4": {1, 2, 3, 4, 5},
		"R5": {1, 2, 3, 4, 5},
	})
	if len(grid) != 3125 {
		t.Fatalf("构造组合数应为3125，实际 %d", len(grid))
	}
	// 用 bollinger 而非 ma_cross：bollinger 接受 R1..R5 等价未用键（会被忽略），不会因未知键报错
	req := testRunbacktestReq("bollinger_breakout")
	res, err := scanGrid(grid, bars, req, nil, GridObjReturn)
	if err != nil {
		t.Fatalf("scanGrid 失败: %v", err)
	}
	if res.CombosTested != 200 {
		t.Fatalf("组合数应被截断为200，实际 %d", res.CombosTested)
	}
}