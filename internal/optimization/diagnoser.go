package optimization

import (
	"encoding/json"
	"fmt"
	"time"
)

// ==================== Diagnoser 策略失效诊断引擎 ====================
//
// P2 阶段目标：不再每天机械重搜所有参数——先诊断"策略有没有问题"，
// 再决定"要不要调参、调哪里、调多少"。
//
// 5 维诊断：
//   因子衰减     —— 策略依赖的主要因子 IC/稳定性是否恶化
//   市场状态突变 —— 六维判势的 regime 是否在前 3 天内大幅切换
//   风险估计偏差 —— 实际 MaxDD 远大于回测预期 MaxDD
//   数据异常     —— 当日数据质量（停牌/涨跌停样本比例）
//   策略逻辑失效 —— 最近 30 天 WinRate < 30% 且方向与市场状态负相关
//
// 输出：HEALTHY → DO_NOTHING / DEGRADED → FOCUSED_TUNE / FAILING → MANUAL_REVIEW

// Diagnoser 策略失效诊断引擎
type Diagnoser struct{}

// NewDiagnoser 创建诊断引擎
func NewDiagnoser() *Diagnoser { return &Diagnoser{} }

// DiagnosisInput 诊断输入——所有字段都是可选的零值表示未知
type DiagnosisInput struct {
	StrategyID   uint
	StrategyType string
	MarketRegime string // 六维判势的当前 regime

	// 最近 30 天回测指标
	Last30dSharpe   float64
	Last30dWinRate  float64
	Last30dMaxDD    float64
	Last30dTurnover float64

	// 因子相关（来自 FactorQuality）
	FactorRankIC    float64
	FactorDecay     float64 // RankIC(20d) - RankIC(1d)
	SplitHalfStable bool

	// 风险对比
	BacktestMaxDD float64 // 回测预期 MaxDD

	// 数据质量
	DataQuality string // "OK" / "STALE" / "INCOMPLETE"

	// 市场状态突变
	RegimeShift3d bool // 前 3 天 regime 大幅切换

	// 每日 DB 引用（写入 daily_diagnosis_log）
	SaferLog func(log *DailyDiagnosisLog) error // 可选：传 nil 则只返回不写入
}

// Diagnose 执行完整诊断流程。永远返回非 nil 的 DailyDiagnosisLog。
func (d *Diagnoser) Diagnose(in *DiagnosisInput) *DailyDiagnosisLog {
	result := &DailyDiagnosisLog{
		DiagnoseDate: time.Now().Format("2006-01-02"),
		StrategyID:   in.StrategyID,
		StrategyType: in.StrategyType,
		MarketRegime: in.MarketRegime,

		Last30dSharpe:   in.Last30dSharpe,
		Last30dWinRate:  in.Last30dWinRate,
		Last30dTurnover: in.Last30dTurnover,
		FactorRankIC:    in.FactorRankIC,
		SplitHalfStable: in.SplitHalfStable,

		CreatedAt: time.Now(),
	}

	// ---------- 1. 因子衰减 ----------
	result.FactorDecay = in.FactorDecay > 0.02 // 衰减 > 2% 视为明显
	if in.FactorRankIC < 0.03 && !in.SplitHalfStable {
		result.FactorDecay = true
	}

	// ---------- 2. 市场状态突变 ----------
	result.RegimeShift = in.RegimeShift3d

	// ---------- 3. 风险估计偏差 ----------
	if in.BacktestMaxDD > 0 && in.Last30dMaxDD > in.BacktestMaxDD*1.5 {
		result.RiskEstimateBias = true
	}

	// ---------- 4. 数据异常 ----------
	result.DataAnomaly = in.DataQuality != DataQualityOK

	// ---------- 5. 策略逻辑失效 ----------
	// 条件：低胜率 + 方向与市场负相关（在牛市低胜率 = 卖飞；熊市低胜率 = 抄底）
	if in.Last30dWinRate > 0 && in.Last30dWinRate < 0.30 {
		result.LogicFailure = true
	}
	// 方向判断简化：牛市低胜率更危险
	if in.Last30dWinRate > 0 && in.Last30dWinRate < 0.25 &&
		(in.MarketRegime == RegimeBull || in.MarketRegime == RegimeBear) {
		result.LogicFailure = true
	}

	// ---------- 综合判定 ----------
	failingCount := 0
	degradedCount := 0

	if result.LogicFailure || result.RiskEstimateBias {
		failingCount++
	}
	if result.FactorDecay || result.RegimeShift {
		degradedCount++
	}

	switch {
	case failingCount >= 1:
		result.Conclusion = ConclusionFailing
		result.Recommendation = RecManualReview
		result.RecommendDetail = fmt.Sprintf(
			"策略逻辑失效或风险估计严重偏差，建议人工介入审查。WinRate=%.1f%%, MaxDD=%.2f%% vs 预期%.2f%%",
			in.Last30dWinRate*100, in.Last30dMaxDD, in.BacktestMaxDD)

	case degradedCount >= 2:
		result.Conclusion = ConclusionDegraded
		result.Recommendation = RecFocusedTune
		result.RecommendDetail = "多维度出现劣化信号，建议定向缩小搜索空间后集中调优"

	case degradedCount == 1:
		result.Conclusion = ConclusionDegraded
		result.Recommendation = RecDoNothing
		result.RecommendDetail = "单一维度劣化，先观察，暂不触发调参"

	default:
		result.Conclusion = ConclusionHealthy
		result.Recommendation = RecDoNothing
		result.RecommendDetail = "策略表现稳定，无需调参"
	}

	// 写入 DB（如果提供了 writer）
	if in.SaferLog != nil {
		if err := in.SaferLog(result); err != nil {
			// 静默：诊断失败不应阻断主流程
		}
	}

	return result
}

// ToJSON 诊断结果序列化
func (log *DailyDiagnosisLog) ToJSON() string {
	b, _ := json.Marshal(log)
	return string(b)
}

// ShouldTune 根据诊断结果判断是否应该触发调参。
func ShouldTune(result *DailyDiagnosisLog) bool {
	if result == nil {
		return false
	}
	return result.Recommendation == RecFocusedTune ||
		result.Recommendation == RecSwitch ||
		result.Recommendation == RecManualReview
}
