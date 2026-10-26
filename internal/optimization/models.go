// Package optimization 实现 QuantBot 递归自我改进（RSI）的实验记录、多目标验证、
// 策略失效诊断与优化器元评估能力。
//
// 对照 QuantBot_RSI_Upgrade_Proposal.md 三阶段蓝图：
//   P1 — 安全的每日优化：ExperimentLedger + Evaluator + ShadowRun
//   P2 — 策略失效诊断：DailyDiagnosisLog + Diagnoser
//   P3 — 改进优化器：OptimizerMetaEval + MetaLearner + CandidateGenerator
package optimization

import (
	"time"
)

// ==================== 实验日志表（P1 核心） ====================

// ExperimentLedger 每次调参/参数变更/版本切换都记录一条，形成可追溯链。
type ExperimentLedger struct {
	ID             uint      `gorm:"primaryKey"`
	ExperimentDate string    `gorm:"index;size:10;not null"` // YYYY-MM-DD 执行日期
	StrategyID     uint      `gorm:"index;not null"`
	StrategyType   string    `gorm:"index;size:30"` // "rsi_reversal" 等

	// 实验元信息
	TriggeredBy  string `gorm:"size:20"` // "daily_tune" / "diagnosis" / "manual"
	MarketRegime string `gorm:"size:20"` // BULLISH/NEUTRAL/BEARISH/RANGE
	DataQuality  string `gorm:"size:20"` // "OK" / "STALE" / "INCOMPLETE"

	// 候选参数与搜索方法
	SearchMethod string `gorm:"size:30"`   // "grid" / "intelligent" / "bayesian"
	SearchSpace  string `gorm:"type:text"`  // JSON: 实际搜索范围/采样策略
	OldConfig    string `gorm:"type:text"`  // JSON: 变更前配置
	NewConfig    string `gorm:"type:text"`  // JSON: 候选新配置

	// 独立验证结果（walk-forward）
	SampleInSharpe   float64 `gorm:"type:decimal(10,4)"`
	SampleOutSharpe  float64 `gorm:"type:decimal(10,4)"`
	SampleOutReturn  float64 `gorm:"type:decimal(10,4)"`
	SampleOutMaxDD   float64 `gorm:"type:decimal(10,4)"`
	SampleOutWinRate float64 `gorm:"type:decimal(6,4)"`
	SampleOutTrades  int     `gorm:"type:int"`
	Turnover         float64 `gorm:"type:decimal(8,4)"`
	RegimeBreakdown  string  `gorm:"type:text"` // JSON: 按市场状态分层的指标

	// 决策
	Verdict         string  `gorm:"size:10"`  // "APPROVED" / "REJECTED" / "SHADOW_RUN" / "DEFERRED"
	VerdictReason   string  `gorm:"type:text"`
	ParamDeltaPct   float64 `gorm:"type:decimal(6,4)"` // 数字参数变化幅度百分比（实际）
	BaselineCompare string  `gorm:"type:text"`         // JSON: 与 baseline 的 head-to-head 对比

	// 影子运行
	ShadowRunID  string `gorm:"size:50"`  // 影子运行会话 ID（若有）
	ShadowResult string `gorm:"type:text"` // JSON: 影子运行结果摘要（执行后回填）

	CreatedAt time.Time
}

// ==================== 优化器元评估日志（P3） ====================

// OptimizerMetaEval 记录不同搜索方法在不同市场状态下的表现。
type OptimizerMetaEval struct {
	ID       uint   `gorm:"primaryKey"`
	EvalDate string `gorm:"index;size:10;not null"`

	SearchMethod string `gorm:"index;size:30"` // "grid" / "intelligent" / "bayesian"
	MarketRegime string `gorm:"index;size:20"` // 评估覆盖的市场状态

	TotalExperiments    int     `gorm:"type:int"`
	ApprovalRate        float64 `gorm:"type:decimal(6,4)"`
	AvgSampleOutSharpe  float64 `gorm:"type:decimal(10,4)"`
	DiscoveryRate       float64 `gorm:"type:decimal(6,4)"`
	AvgSearchCombos     int     `gorm:"type:int"`

	Recommendation string `gorm:"size:20"` // "PREFERRED" / "FALLBACK" / "AVOID"
	Notes          string `gorm:"type:text"`

	CreatedAt time.Time
}

// ==================== 每日诊断日志（P2） ====================

// DailyDiagnosisLog 每日对"策略是否失效"的诊断结论。
type DailyDiagnosisLog struct {
	ID           uint   `gorm:"primaryKey"`
	DiagnoseDate string `gorm:"index;size:10;not null"`

	StrategyID   uint   `gorm:"index;not null"`
	StrategyType string `gorm:"index;size:30"`
	MarketRegime string `gorm:"size:20"`

	// 诊断维度（布尔标记）
	FactorDecay      bool
	RegimeShift      bool
	DataAnomaly      bool
	RiskEstimateBias bool
	LogicFailure     bool

	// 量化指标
	Last30dSharpe   float64 `gorm:"type:decimal(10,4)"`
	Last30dWinRate  float64 `gorm:"type:decimal(6,4)"`
	Last30dTurnover float64 `gorm:"type:decimal(8,4)"`
	FactorRankIC    float64 `gorm:"type:decimal(8,4)"`
	SplitHalfStable bool

	// 结论与建议
	Conclusion      string `gorm:"size:20"` // "HEALTHY" / "DEGRADED" / "FAILING"
	Recommendation  string `gorm:"size:20"` // "DO_NOTHING" / "FOCUSED_TUNE" / "SWITCH_STRATEGY" / "MANUAL_REVIEW"
	RecommendDetail string `gorm:"type:text"`

	CreatedAt time.Time
}

// ==================== 辅助常量 ====================

const (
	VerdictApproved  = "APPROVED"
	VerdictRejected  = "REJECTED"
	VerdictShadowRun = "SHADOW_RUN"
	VerdictDeferred  = "DEFERRED"

	ConclusionHealthy  = "HEALTHY"
	ConclusionDegraded = "DEGRADED"
	ConclusionFailing  = "FAILING"

	RecDoNothing    = "DO_NOTHING"
	RecFocusedTune  = "FOCUSED_TUNE"
	RecSwitch       = "SWITCH_STRATEGY"
	RecManualReview = "MANUAL_REVIEW"

	RegimeBull   = "BULLISH"
	RegimeNeu    = "NEUTRAL"
	RegimeBear   = "BEARISH"
	RegimeRange  = "RANGE"

	DataQualityOK       = "OK"
	DataQualityStale    = "STALE"
	DataQualityIncomp   = "INCOMPLETE"

	SearchMethodGrid       = "grid"
	SearchMethodIntelligent = "intelligent"
	SearchMethodBayesian   = "bayesian"

	TriggerDailyTune = "daily_tune"
	TriggerDiagnosis = "diagnosis"
	TriggerManual    = "manual"
)
