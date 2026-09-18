package riskcenter

import (
	"encoding/json"
	"log"
	"time"

	"github.com/quantpilot/quantpilot/internal/data"
	"github.com/quantpilot/quantpilot/internal/brain/intelligence"
	"github.com/quantpilot/quantpilot/internal/policy"
)

// CheckItem 合规约束校验条目（"研究中心-风险管理"合规模块展示）
type CheckItem struct {
	Name    string  `json:"name"`
	Actual  float64 `json:"actual"`
	Limit   float64 `json:"limit,omitempty"` // 0 表示无上限（如现金下限用 direction 表示）
	Passed  bool    `json:"passed"`
	Message string  `json:"message"`
}

// Report 组合风险报告 DTO（前端 5 大板块渲染所需全部字段）
type Report struct {
	ReportDate    string             `json:"report_date"`
	Source        string             `json:"source"`
	PositionCount int                `json:"position_count"`
	TotalExposure float64            `json:"total_exposure"`
	VaR95         float64            `json:"var_95"`
	VaR99         float64            `json:"var_99"`
	CVaR95        float64            `json:"cvar_95"`
	Volatility    float64            `json:"volatility"`
	MaxDrawdown   float64            `json:"max_drawdown"`
	Beta          float64            `json:"beta"`
	Correlation   float64            `json:"correlation"`
	Concentration float64            `json:"concentration"`
	EMD           float64            `json:"emd"`
	Liquidity     float64            `json:"liquidity"`
	OverallScore  float64            `json:"overall_score"`
	Status        string             `json:"status"` // NORMAL / WATCH / WARNING / CRITICAL
	StressTests   []StressTestLine   `json:"stress_tests"`
	Limits        []CheckItem        `json:"limits"`
	Violations    []string           `json:"violations"`
	Weights       map[string]float64 `json:"weights,omitempty"`
}

// StressTestLine 压力测试情景单行（前端可读中文描述）
type StressTestLine struct {
	Scenario      string  `json:"scenario"`
	Description   string  `json:"description"`
	VolMultiplier float64 `json:"vol_multiplier"`
	Shock         float64 `json:"shock"`
	EstimatedLoss float64 `json:"estimated_loss"`
	Probability   float64 `json:"probability"`
	Passed        bool    `json:"passed"` // estimated_loss <= 15%
}

// ComputeReport 计算组合风险报告（顺序：快照->权重->风控引擎->合规校验）。
// returns/benchmark/weights 由调用方基于真实持仓与行情构建（口径与风控师 riskPreMarket 一致）。
// 返回可持久化/展示的 Report。
func ComputeReport(returns, benchmark []float64, weights map[string]float64) *Report {
	engine := intelligence.NewRiskEngine()
	var pr intelligence.PortfolioRiskReport
	if len(returns) > 0 {
		pr = engine.ComputePortfolioRisk(returns, benchmark, weights)
	}

	rep := &Report{
		ReportDate:    time.Now().Format("2006-01-02"),
		PositionCount: len(weights),
		TotalExposure: totalExposure(weights),
		VaR95:         pr.VaR95,
		VaR99:         pr.VaR99,
		CVaR95:        pr.CVaR95,
		Volatility:    pr.Volatility,
		MaxDrawdown:   pr.MaxDrawdown,
		Beta:          pr.Beta,
		Correlation:   pr.Correlation,
		Concentration: pr.Concentration,
		EMD:           pr.EMD,
		Liquidity:     pr.Liquidity,
		OverallScore:  pr.OverallScore,
		Status:        pr.Status,
		Weights:       weights,
	}
	if rep.Status == "" {
		rep.Status = "NORMAL"
	}

	// 压力测试：逐条判定预估损失是否超过风控师阈值 15%
	for _, s := range pr.StressTests {
		rep.StressTests = append(rep.StressTests, StressTestLine{
			Scenario:      s.Scenario,
			Description:   s.Description,
			VolMultiplier: s.VolMultiplier,
			Shock:         s.Shock,
			EstimatedLoss: s.EstimatedLoss,
			Probability:   s.Probability,
			Passed:        s.EstimatedLoss <= 0.15,
		})
	}

	// 合规约束校验：与风控师/PolicyEngine 同一套硬限制口径
	hl := policy.NewPolicyEngine(nil).GetHardLimits() // HardLimits 为纯值结构，无 db 依赖
	rep.Limits = buildCompliance(pr, weights, hl)
	for _, c := range rep.Limits {
		if !c.Passed {
			rep.Violations = append(rep.Violations, c.Message)
		}
	}

	return rep
}

// SaveReport 持久化一条组合风险报告到 market_risk_reports 表。
// 幂等：同一天同来源只保留最新一条（先删后写），避免重复累积。
func SaveReport(db *data.SQLiteManager, rep *Report) error {
	if db == nil {
		return nil
	}
	stressJSON, _ := json.Marshal(rep.StressTests)
	limitsJSON, _ := json.Marshal(rep.Limits)
	violJSON, _ := json.Marshal(rep.Violations)

	rec := data.MarketRiskReport{
		ReportDate:    rep.ReportDate,
		Source:        rep.Source,
		PositionCount: rep.PositionCount,
		TotalExposure: rep.TotalExposure,
		VaR95:         rep.VaR95,
		VaR99:         rep.VaR99,
		CVaR95:        rep.CVaR95,
		Volatility:    rep.Volatility,
		MaxDrawdown:   rep.MaxDrawdown,
		Beta:          rep.Beta,
		Correlation:   rep.Correlation,
		Concentration: rep.Concentration,
		EMD:           rep.EMD,
		Liquidity:     rep.Liquidity,
		OverallScore:  rep.OverallScore,
		Status:        rep.Status,
		StressTests:   string(stressJSON),
		LimitsJSON:    string(limitsJSON),
		Violations:    string(violJSON),
		CreatedAt:     time.Now(),
	}
	db.GetDB().Where("report_date = ? AND source = ?", rep.ReportDate, rep.Source).Delete(&data.MarketRiskReport{})
	if err := db.GetDB().Create(&rec).Error; err != nil {
		return err
	}
	log.Printf("[RiskCenter] 已保存组合风险报告 %s source=%s status=%s score=%.0f%%",
		rep.ReportDate, rep.Source, rep.Status, rep.OverallScore*100)
	return nil
}

// ToMap 把风险报告摊平为可读 map（供智能体工具返回、与历史报告 flatten 同构）。
func ToMap(rep *Report) map[string]interface{} {
	if rep == nil {
		return map[string]interface{}{"status": "NORMAL"}
	}
	m := map[string]interface{}{
		"report_date":    rep.ReportDate,
		"source":         rep.Source,
		"position_count": rep.PositionCount,
		"total_exposure": rep.TotalExposure,
		"var_95":         rep.VaR95,
		"var_99":         rep.VaR99,
		"cvar_95":        rep.CVaR95,
		"volatility":     rep.Volatility,
		"max_drawdown":   rep.MaxDrawdown,
		"beta":           rep.Beta,
		"correlation":    rep.Correlation,
		"concentration":  rep.Concentration,
		"emd":            rep.EMD,
		"liquidity":      rep.Liquidity,
		"overall_score":  rep.OverallScore,
		"status":         rep.Status,
		"stress_tests":   rep.StressTests,
		"limits":         rep.Limits,
		"violations":     rep.Violations,
	}
	if len(rep.Weights) > 0 {
		m["weights"] = rep.Weights
	}
	return m
}

// LoadReports 读取最近 limit 条组合风险报告（按报告日期倒序）。
func LoadReports(db *data.SQLiteManager, limit int) ([]data.MarketRiskReport, error) {
	if db == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 7
	}
	var recs []data.MarketRiskReport
	err := db.GetDB().Order("created_at DESC").Limit(limit).Find(&recs).Error
	return recs, err
}

// totalExposure 组合总多头暴露 = 权重之和
func totalExposure(weights map[string]float64) float64 {
	if len(weights) == 0 {
		return 0
	}
	var sum float64
	var count int
	for _, w := range weights {
		if w > 0 {
			sum += w
			count++
		}
	}
	if count == 0 {
		return 0
	}
	return sum
}

// buildCompliance 依据 PolicyEngine 硬限制与当前权重/风险指标构建合规校验条目。
// 各条与风控师风险审查口径一致（单股/行业/回撤/集中度/流动性/CVaR）。
func buildCompliance(pr intelligence.PortfolioRiskReport, weights map[string]float64, hl policy.HardLimits) []CheckItem {
	items := []CheckItem{}

	// 单股上限
	var maxSingle float64
	for _, w := range weights {
		if w > maxSingle {
			maxSingle = w
		}
	}
	items = append(items, CheckItem{
		Name: "单票持仓上限", Actual: maxSingle, Limit: hl.MaxSinglePosition,
		Passed:  maxSingle <= hl.MaxSinglePosition,
		Message: checkMsg("单票持仓", maxSingle, hl.MaxSinglePosition, 0.30),
	})

	// 组合集中度（HHI），≤ 0.5 视为分散良好
	concPass := pr.Concentration <= 0.5
	items = append(items, CheckItem{
		Name: "组合集中度(HHI)", Actual: pr.Concentration, Limit: 0.5,
		Passed: concPass, Message: checkMsg("集中度", pr.Concentration, 0.5, 0.5),
	})

	// 最大回撤
	items = append(items, CheckItem{
		Name: "最大回撤", Actual: pr.MaxDrawdown, Limit: hl.MaxDrawdown,
		Passed:  pr.MaxDrawdown <= hl.MaxDrawdown,
		Message: checkMsg("最大回撤", pr.MaxDrawdown, hl.MaxDrawdown, 0.10),
	})

	// 组合CVaR（风控师约束 ≤8%）
	items = append(items, CheckItem{
		Name: "组合CVaR(95)", Actual: pr.CVaR95, Limit: hl.MaxDailyLoss,
		Passed:  pr.CVaR95 <= hl.MaxDailyLoss,
		Message: checkMsg("组合CVaR", pr.CVaR95, hl.MaxDailyLoss, 0.05),
	})

	// 流动性（1-Liquidity 越小越安全，阈值取硬限制 MinLiquidity）。
	// 空组合（Liquidity 为 0 会被误判为无流动性）时跳过该条，避免空仓误报违规。
	if len(weights) > 0 {
		illiq := maxf(0, 1-pr.Liquidity)
		items = append(items, CheckItem{
			Name: "流动性风险", Actual: illiq, Limit: hl.MinLiquidity,
			Passed:  illiq <= hl.MinLiquidity,
			Message: checkMsg("流动性风险", illiq, hl.MinLiquidity, 0.10),
		})
	}

	return items
}

func checkMsg(name string, actual, limit, tipLimit float64) string {
	if limit <= 0 {
		return name + " 无硬限制"
	}
	state := "通过"
	if actual > limit {
		state = "超限"
	}
	return name + " " + state
}

func maxf(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
