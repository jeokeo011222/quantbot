package riskcenter

import (
	"math"
	"testing"

	"github.com/quantpilot/quantpilot/internal/data"
	"github.com/quantpilot/quantpilot/internal/intelligence"
	"github.com/quantpilot/quantpilot/internal/policy"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// synthReturns 生成一段确定性收益序列，保证 VaR/波动率/回撤等指标可计算。
// amp 控制波幅（收益为 ±amp 的震荡），返回 100 个样本。
func synthReturns(amp float64) []float64 {
	n := 100
	out := make([]float64, n)
	for i := 0; i < n; i++ {
		out[i] = amp * math.Sin(float64(i)*0.7) / 2.0
	}
	return out
}

// TestTotalExposureTotal 逐条验证组合总多头暴露口径（只看正权重，负权重/零不计）。
func TestTotalExposure(t *testing.T) {
	cases := []struct {
		name    string
		weights map[string]float64
		want    float64
	}{
		{"空组合", map[string]float64{}, 0},
		{"全部多头", map[string]float64{"A": 0.4, "B": 0.3, "C": 0.3}, 1.0},
		{"含负权重/零", map[string]float64{"A": 0.4, "B": 0.3, "C": -0.1, "D": 0}, 0.7},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := totalExposure(c.weights); math.Abs(got-c.want) > 1e-9 {
				t.Fatalf("totalExposure(%v) = %v, want %v", c.weights, got, c.want)
			}
		})
	}
}

// TestComputeReportEmptyInputs 验证无持仓/无行情时：状态兜底 NORMAL、无误报违规、压力测试仍生成。
func TestComputeReportEmptyInputs(t *testing.T) {
	rep := ComputeReport(nil, nil, map[string]float64{})
	if rep.Status != "NORMAL" {
		t.Fatalf("空输入 status = %q, want NORMAL", rep.Status)
	}
	if rep.PositionCount != 0 || rep.TotalExposure != 0 {
		t.Fatalf("空输入 position_count=%d total_exposure=%v, want 0/0", rep.PositionCount, rep.TotalExposure)
	}
	if len(rep.StressTests) != 0 {
		t.Fatalf("空输入无行情时不应有压力测试情景，got %d", len(rep.StressTests))
	}
	// 空输入所有计量为 0，任何违规都不能被误报
	if len(rep.Violations) != 0 {
		t.Fatalf("空输入误报违规: %v", rep.Violations)
	}
	if len(rep.Limits) == 0 {
		t.Fatalf("空输入应仍输出合规校验条目，got 0")
	}
	// 空输入全部硬限制通过
	for _, c := range rep.Limits {
		if !c.Passed {
			t.Fatalf("空输入合规项 %q 不应超限 (actual=%v limit=%v)", c.Name, c.Actual, c.Limit)
		}
	}
}

// TestComputeReportConcentratedFlagsViolations 验证：单票过度集中时，单票上限与集中度(HHI)两条硬限制触发。
func TestComputeReportConcentratedFlagsViolations(t *testing.T) {
	weights := map[string]float64{"600001": 0.9, "600002": 0.1}
	rep := ComputeReport(synthReturns(0.02), nil, weights)

	if rep.PositionCount != 2 {
		t.Fatalf("position_count = %d, want 2", rep.PositionCount)
	}
	// 单票 90% > 30% 硬限制，必然产生违规
	if len(rep.Violations) == 0 {
		t.Fatalf("高度集中组合未产生任何违规")
	}
	// 集中度 HHI = 0.9^2+0.1^2 = 0.82 > 0.5，Compliance 应标红
	var singlePassed, concPassed bool
	for _, c := range rep.Limits {
		switch c.Name {
		case "单票持仓上限":
			singlePassed = c.Passed
		case "组合集中度(HHI)":
			concPassed = c.Passed
		}
	}
	if singlePassed {
		t.Fatalf("单票 90%% 不应通过单票上限校验")
	}
	if concPassed {
		t.Fatalf("HHI 0.82 不应通过集中度校验")
	}
}

// TestBuildCompliance 单测合规校验算法：给定明确的指标读数，逐项断言通过/超限。
func TestBuildCompliance(t *testing.T) {
	// 各项指标皆踩线超限，应全部标红
	pr := intelligence.PortfolioRiskReport{
		Concentration: 0.82, // > 0.5
		MaxDrawdown:   0.25, // > 0.20
		CVaR95:        0.06, // > 0.05
		Liquidity:     0.30, // 1 - 0.30 = 0.70 > 0.10
	}
	weights := map[string]float64{"A": 0.5, "B": 0.5} // maxSingle = 0.5 > 0.30
	hl := policy.NewPolicyEngine(nil).GetHardLimits()
	items := buildCompliance(pr, weights, hl)

	expect := map[string]bool{
		"单票持仓上限":     false, // 0.5 > 0.30
		"组合集中度(HHI)": false, // 0.82 > 0.5
		"最大回撤":       false, // 0.25 > 0.20
		"组合CVaR(95)": false, // 0.06 > 0.05
		"流动性风险":      false, // 0.70 > 0.10
	}
	if len(items) != len(expect) {
		t.Fatalf("合规条目数 = %d, want %d", len(items), len(expect))
	}
	for _, c := range items {
		wantPassed, ok := expect[c.Name]
		if !ok {
			t.Fatalf("出现未预期合规项 %q", c.Name)
		}
		if c.Passed != wantPassed {
			t.Fatalf("合规项 %q passed = %v, want %v (actual=%v limit=%v)",
				c.Name, c.Passed, wantPassed, c.Actual, c.Limit)
		}
	}
}

// newTestDB 构造内存 SQLite，并迁移 MarketRiskReport 表，用于持久化测试。
func newTestDB(t *testing.T) *data.SQLiteManager {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	if err := gdb.AutoMigrate(&data.MarketRiskReport{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return data.NewSQLiteManagerFromDB(gdb)
}

// TestSaveReportRoundTrip 验证保存后可读回，且字段完整无丢失。
func TestSaveReportRoundTrip(t *testing.T) {
	db := newTestDB(t)
	rep := &Report{
		ReportDate:    "2026-09-10",
		Source:        "manual",
		PositionCount: 2,
		TotalExposure: 1.0,
		VaR95:         0.021,
		VaR99:         0.045,
		CVaR95:        0.038,
		Volatility:    0.11,
		MaxDrawdown:   0.09,
		Beta:          0.87,
		Correlation:   0.72,
		Concentration: 0.53,
		EMD:           0.31,
		Liquidity:     0.9,
		OverallScore:  0.24,
		Status:        "WATCH",
		StressTests:   []StressTestLine{{Scenario: "market_crash", EstimatedLoss: 0.12, Passed: true}},
		Limits:        []CheckItem{{Name: "单票持仓上限", Actual: 0.5, Limit: 0.3, Passed: false, Message: "单票持仓 超限"}},
		Violations:    []string{"单票持仓 超限"},
	}

	if err := SaveReport(db, rep); err != nil {
		t.Fatalf("SaveReport: %v", err)
	}
	recs, err := LoadReports(db, 7)
	if err != nil {
		t.Fatalf("LoadReports: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("LoadReports len = %d, want 1", len(recs))
	}
	r := recs[0]
	if r.ReportDate != "2026-09-10" || r.Source != "manual" || r.Status != "WATCH" {
		t.Fatalf("基本字段不符: %+v", r)
	}
	if r.PositionCount != 2 || math.Abs(r.TotalExposure-1.0) > 1e-9 {
		t.Fatalf("计数字段不符: %+v", r)
	}
	if r.OverallScore != 0.24 || r.CVaR95 != 0.038 {
		t.Fatalf("风险指标不符: overall=%v cvar=%v", r.OverallScore, r.CVaR95)
	}
	if r.StressTests == "" || r.LimitsJSON == "" || r.Violations == "" {
		t.Fatalf("JSON 文本列不应为空")
	}
}

// TestSaveReportIdempotent 验证同一天同一来源先删后写，重复保存不累积重复记录。
func TestSaveReportIdempotent(t *testing.T) {
	db := newTestDB(t)
	first := &Report{ReportDate: "2026-09-10", Source: "manual", Status: "NORMAL"}
	second := &Report{ReportDate: "2026-09-10", Source: "manual", Status: "WARNING", OverallScore: 0.6}

	if err := SaveReport(db, first); err != nil {
		t.Fatalf("first SaveReport: %v", err)
	}
	if err := SaveReport(db, second); err != nil {
		t.Fatalf("second SaveReport: %v", err)
	}
	recs, err := LoadReports(db, 7)
	if err != nil {
		t.Fatalf("LoadReports: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("重复保存后记录数 = %d, want 1（应覆盖）", len(recs))
	}
	// 最新一条应取第二次的值
	if recs[0].Status != "WARNING" || recs[0].OverallScore != 0.6 {
		t.Fatalf("覆盖未生效: %+v", recs[0])
	}
}
