// ==================== StrategyDiagnosisProcessor ====================
// 每日策略失效诊断处理器——在 PhasePostMarket 调优前执行一次，
// 用**昨日已落库的** Strategy 指标 + FactorQuality + 六维判势 构建 DiagnosisInput。
// 诊断结果写入 daily_diagnosis_log，供后续 StrategyTunerTool 决定"要不要调参"。
//
// 时序说明：
//
//	PhasePostMarket (15:05) → 先诊断 → 再调优（post_quant_tune）
//	PhaseReview    (15:15) → 因子复盘（night_quant）→ 深度复盘（night_cio）
//	诊断用昨日数据（已落库），因子复盘产出今日数据，次日诊断再用。
package scheduler

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/quantpilot/quantpilot/internal/data"
	"github.com/quantpilot/quantpilot/internal/optimization"
	"gorm.io/gorm"
)

// StrategyDiagnosisProcessor 每日策略失效诊断处理器接口
type StrategyDiagnosisProcessor interface {
	TriggerStrategyDiagnosis(ctx context.Context) error
}

// ==================== 默认实现 ====================

type defaultStrategyDiagnosisProcessor struct {
	db     *gorm.DB
	duckdb *data.DuckDBManager // 用于拉最近六维判势结果（可选）
}

// NewStrategyDiagnosisProcessor 创建默认诊断处理器
func NewStrategyDiagnosisProcessor(db *gorm.DB, duckdb *data.DuckDBManager) StrategyDiagnosisProcessor {
	return &defaultStrategyDiagnosisProcessor{db: db, duckdb: duckdb}
}

// TriggerStrategyDiagnosis 对全部内置策略做一次失效诊断并落库。
// 永远返回 nil 或非致命 error——诊断失败不应阻断后续调优流程。
func (p *defaultStrategyDiagnosisProcessor) TriggerStrategyDiagnosis(ctx context.Context) error {
	if p.db == nil {
		return fmt.Errorf("StrategyDiagnosisProcessor: db 未初始化")
	}

	// 幂等检查：今日已跑则跳过
	today := time.Now().Format("2006-01-02")
	var count int64
	p.db.Model(&optimization.DailyDiagnosisLog{}).
		Where("diagnose_date = ?", today).Count(&count)
	if count > 0 {
		log.Printf("[StrategyDiagnosis] 今日诊断已完成(%d 条)，跳过", count)
		return nil
	}

	// 读取全部内置策略
	var strategies []data.Strategy
	if err := p.db.Where("is_builtin = ?", 1).Find(&strategies).Error; err != nil {
		return fmt.Errorf("查询内置策略失败: %w", err)
	}
	if len(strategies) == 0 {
		log.Println("[StrategyDiagnosis] 无内置策略，跳过诊断")
		return nil
	}

	// 当前市场状态（从 DuckDB 读最近一次六维判势记录）
	marketRegime := p.readLatestMarketRegime()

	// 诊断引擎
	diagnoser := optimization.NewDiagnoser()

	// 对每个策略做诊断
	log.Printf("[StrategyDiagnosis] ========== 开始每日策略失效诊断（%d 策略, regime=%s）==========", len(strategies), marketRegime)
	healthyCount, degradedCount, failingCount := 0, 0, 0

	for i := range strategies {
		st := &strategies[i]
		in := p.buildDiagnosisInput(st, marketRegime)

		result := diagnoser.Diagnose(in)

		// 落库
		if err := p.db.Create(result).Error; err != nil {
			log.Printf("[StrategyDiagnosis] 写入诊断日志失败 strategy=%s: %v", st.StrategyType, err)
			continue
		}

		switch result.Conclusion {
		case optimization.ConclusionHealthy:
			healthyCount++
		case optimization.ConclusionDegraded:
			degradedCount++
		case optimization.ConclusionFailing:
			failingCount++
			log.Printf("[StrategyDiagnosis] ⚠️ 策略 %s(%s) 诊断 FAILING: %s", st.Name, st.StrategyType, result.RecommendDetail)
		}

		log.Printf("[StrategyDiagnosis] %s → %s [%s]", st.StrategyType, result.Conclusion, result.Recommendation)
	}

	log.Printf("[StrategyDiagnosis] ========== 诊断完成: HEALTHY=%d DEGRADED=%d FAILING=%d ==========",
		healthyCount, degradedCount, failingCount)

	return nil
}

// readLatestMarketRegime 从 DuckDB 读最近一次六维判势的市场标签。
// 失败时返回 NEUTRAL（保守默认值，不阻断诊断）。
func (p *defaultStrategyDiagnosisProcessor) readLatestMarketRegime() string {
	if p.duckdb != nil {
		rows, err := data.GetMarketSixDimRows(context.Background(), p.duckdb, 1)
		if err == nil && len(rows) > 0 {
			tag := rows[0].MarketTag
			switch tag {
			case "bull":
				return optimization.RegimeBull
			case "bear":
				return optimization.RegimeBear
			case "range", "volatile":
				return optimization.RegimeRange
			}
		}
	}
	return optimization.RegimeNeu
}

// buildDiagnosisInput 从 Strategy 表 + FactorQuality 表构建 DiagnosisInput。
// 全部从 DB 读昨日已落库数据，零实时计算。
func (p *defaultStrategyDiagnosisProcessor) buildDiagnosisInput(st *data.Strategy, marketRegime string) *optimization.DiagnosisInput {
	in := &optimization.DiagnosisInput{
		StrategyID:    st.ID,
		StrategyType:  st.StrategyType,
		MarketRegime:  marketRegime,
		DataQuality:   optimization.DataQualityOK,
		BacktestMaxDD: st.MaxDrawdown,
	}

	// 从 Strategy 表直接拿已落库的回测指标（代表最近 30 天表现）
	in.Last30dSharpe = st.SharpeRatio
	in.Last30dWinRate = st.WinRate
	in.Last30dMaxDD = st.MaxDrawdown
	in.Last30dTurnover = st.Turnover

	// --- 市场状态突变检测 ---
	// 对比今日 regime 与 3 日前的六维判势记录（若可用）
	in.RegimeShift3d = p.detectRegimeShift(marketRegime)

	// 读 FactorQuality——取 IC 最低的前几个因子作为策略依赖因子的"代表性最差情况"
	if p.db != nil {
		var qualities []data.FactorQuality
		if err := p.db.Where("quality > 0").Order("rank_ic ASC").Limit(5).Find(&qualities).Error; err == nil && len(qualities) > 0 {
			worstIC := 1.0
			for _, q := range qualities {
				if q.RankIC < worstIC {
					worstIC = q.RankIC
					in.SplitHalfStable = q.SplitHalfStable
				}
			}
			in.FactorRankIC = worstIC
		}
	}

	return in
}

// detectRegimeShift 对比今日 regime 与 3 日前的市场标签，若跨了 Bull↔Bear 或 Range↔趋势 则标记突变。
func (p *defaultStrategyDiagnosisProcessor) detectRegimeShift(todayRegime string) bool {
	if p.duckdb == nil {
		return false
	}
	rows, err := data.GetMarketSixDimRows(context.Background(), p.duckdb, 5)
	if err != nil || len(rows) < 2 {
		return false
	}
	prevIdx := 3
	if prevIdx >= len(rows) {
		prevIdx = len(rows) - 1
	}
	prevTag := rows[prevIdx].MarketTag
	prevRegime := optimization.RegimeNeu
	switch prevTag {
	case "bull":
		prevRegime = optimization.RegimeBull
	case "bear":
		prevRegime = optimization.RegimeBear
	case "range", "volatile":
		prevRegime = optimization.RegimeRange
	}

	// 两端 regime 不同且都不是 NEUTRAL → 视为突变
	if todayRegime == prevRegime {
		return false
	}
	if todayRegime == optimization.RegimeNeu || prevRegime == optimization.RegimeNeu {
		return false
	}
	return true
}
