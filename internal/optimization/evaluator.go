package optimization

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"reflect"
	"strconv"
	"time"
)

// ==================== Evaluator 多目标约束验证 ====================
//
// 职责：
//   1. 对候选参数做独立验证——多目标约束（不能只看单一 metric）。
//   2. 与 baseline（当前线上参数）做 head-to-head 对比。
//   3. 检查数字参数变化幅度不超过 50%（防参数漂移）。
//
// 输入：候选参数的回测指标 + baseline 的回测指标 + old/new config JSON。
// 输出：Verdict + 判定理由 + baseline 对比 JSON。

// SampleMetrics 一组回测/样本外绩效指标（从回测引擎或 StrategyTuner 传入）。
type SampleMetrics struct {
	Sharpe   float64
	Return   float64  // 总收益率 %
	MaxDD    float64  // 最大回撤 %
	WinRate  float64  // 胜率 %
	Turnover float64  // 年化换手率
	Trades   int
}

// EvaluationConfig 多目标约束参数（可在 config/config.json 中调整）。
type EvaluationConfig struct {
	// 样本外门槛
	MinSampleOutSharpe float64 `json:"min_sample_out_sharpe"`  // 最小样本外夏普
	MinSampleOutTrades int     `json:"min_sample_out_trades"`  // 最小交易次数

	// Baseline 约束（候选不得比 baseline 差超过这些比例）
	MaxDDWorsePct       float64 `json:"max_dd_worse_pct"`       // 回撤允许比 baseline 差 X%（120% = 1.2）
	MaxTurnoverWorsePct float64 `json:"max_turnover_worse_pct"` // 换手率允许比 baseline 高 X%（150% = 1.5）
	MinSharpeImproveAbs float64 `json:"min_sharpe_improve_abs"` // 最小夏普绝对提升（0 = 不强制）

	// 参数变化约束
	MaxParamDeltaPct float64 `json:"max_param_delta_pct"` // 数字参数最大变化幅度（相对原值，0.5 = 50%）

	// 变更频率约束
	MinDaysBetweenTune int `json:"min_days_between_tune"` // 同一策略两次调优最小间隔天数
}

// DefaultEvaluationConfig 默认多目标约束参数
func DefaultEvaluationConfig() EvaluationConfig {
	return EvaluationConfig{
		MinSampleOutSharpe:   0.3,
		MinSampleOutTrades:   8,
		MaxDDWorsePct:        1.20, // 回撤允许比 baseline 大 20%
		MaxTurnoverWorsePct:  1.50, // 换手率允许比 baseline 高 50%
		MinSharpeImproveAbs:  0.05, // 夏普至少提升 0.05 才算真改进
		MaxParamDeltaPct:     0.50, // 数字参数变化 ≤ 原值 50%
		MinDaysBetweenTune:   3,
	}
}

// EvaluationResult evaluator 的判定结果
type EvaluationResult struct {
	Verdict       string            // VerdictApproved / VerdictRejected / VerdictShadowRun
	Reason        string            // 一句话理由（供日志和前端展示）
	ParamDeltaPct float64           // 数字参数实际变化幅度（取绝对值最大者）
	DeltaExceeded bool              // 是否触发了参数幅度超限
	Checks        map[string]string // 各维度检查结果（"PASS"/"FAIL" + 数值）
	Timestamp     time.Time
}

// Evaluator 多目标约束验证器
type Evaluator struct {
	cfg EvaluationConfig
}

// NewEvaluator 创建验证器（使用默认配置）
func NewEvaluator() *Evaluator {
	return &Evaluator{cfg: DefaultEvaluationConfig()}
}

// NewEvaluatorWithConfig 创建验证器（自定义配置）
func NewEvaluatorWithConfig(cfg EvaluationConfig) *Evaluator {
	return &Evaluator{cfg: cfg}
}

// Evaluate 执行完整的多目标约束验证流程。
//   - candidate: walk-forward 样本外指标
//   - baseline : 当前线上参数的指标（同窗口回测得到），可为 nil 表示无 baseline
//   - oldCfgJSON / newCfgJSON : 变更前后配置（用于参数幅度检查）
func (e *Evaluator) Evaluate(
	candidate *SampleMetrics,
	baseline *SampleMetrics,
	oldCfgJSON, newCfgJSON string,
) *EvaluationResult {
	result := &EvaluationResult{
		Verdict:   VerdictRejected, // 默认拒绝，通过所有检查才放行
		Checks:    make(map[string]string),
		Timestamp: time.Now(),
	}

	// ---------- 检查 1: 样本外基础门槛 ----------
	if candidate.Sharpe < e.cfg.MinSampleOutSharpe {
		result.Checks["sample_out_sharpe"] = fmt.Sprintf("FAIL (%.3f < %.3f)", candidate.Sharpe, e.cfg.MinSampleOutSharpe)
		result.Reason = fmt.Sprintf("样本外夏普 %.3f 低于门槛 %.3f", candidate.Sharpe, e.cfg.MinSampleOutSharpe)
		return result
	}
	result.Checks["sample_out_sharpe"] = fmt.Sprintf("PASS (%.3f)", candidate.Sharpe)

	if candidate.Trades < e.cfg.MinSampleOutTrades {
		result.Checks["sample_out_trades"] = fmt.Sprintf("FAIL (%d < %d)", candidate.Trades, e.cfg.MinSampleOutTrades)
		result.Reason = fmt.Sprintf("交易次数 %d 低于门槛 %d（胜率可能失真）", candidate.Trades, e.cfg.MinSampleOutTrades)
		return result
	}
	result.Checks["sample_out_trades"] = fmt.Sprintf("PASS (%d)", candidate.Trades)

	// ---------- 检查 2: Baseline head-to-head ----------
	if baseline != nil && baseline.Sharpe > 0 {
		// 回撤不能比 baseline 差太多
		if candidate.MaxDD > baseline.MaxDD*e.cfg.MaxDDWorsePct {
			result.Checks["maxdd_vs_baseline"] = fmt.Sprintf(
				"FAIL (candidate DD %.2f%% > baseline %.2f%% × %.2f)",
				candidate.MaxDD, baseline.MaxDD, e.cfg.MaxDDWorsePct)
			result.Reason = fmt.Sprintf("回撤 %.2f%% 超过 baseline %.2f%% 的 %.0f%% 容忍度",
				candidate.MaxDD, baseline.MaxDD, e.cfg.MaxDDWorsePct*100)
			return result
		}
		result.Checks["maxdd_vs_baseline"] = fmt.Sprintf("PASS (candidate %.2f%% / baseline %.2f%%)",
			candidate.MaxDD, baseline.MaxDD)

		// 换手率不能比 baseline 高太多
		if candidate.Turnover > baseline.Turnover*e.cfg.MaxTurnoverWorsePct {
			result.Checks["turnover_vs_baseline"] = fmt.Sprintf(
				"FAIL (candidate turnover %.2f > baseline %.2f × %.2f)",
				candidate.Turnover, baseline.Turnover, e.cfg.MaxTurnoverWorsePct)
			result.Reason = fmt.Sprintf("换手率 %.2f 超过 baseline %.2f 的 %.0f%% 容忍度（交易成本将上升）",
				candidate.Turnover, baseline.Turnover, e.cfg.MaxTurnoverWorsePct*100)
			return result
		}
		result.Checks["turnover_vs_baseline"] = fmt.Sprintf("PASS (candidate %.2f / baseline %.2f)",
			candidate.Turnover, baseline.Turnover)

		// 夏普必须有至少小幅提升
		sharpeDelta := candidate.Sharpe - baseline.Sharpe
		if sharpeDelta < e.cfg.MinSharpeImproveAbs {
			result.Checks["sharpe_improvement"] = fmt.Sprintf(
				"FAIL (delta %.3f < min %.3f)", sharpeDelta, e.cfg.MinSharpeImproveAbs)
			result.Reason = fmt.Sprintf("夏普仅提升 %.3f，未达最小改进 %.3f", sharpeDelta, e.cfg.MinSharpeImproveAbs)
			return result
		}
		result.Checks["sharpe_improvement"] = fmt.Sprintf("PASS (delta %.3f)", sharpeDelta)
	} else {
		result.Checks["baseline"] = "SKIP (no valid baseline)"
	}

	// ---------- 检查 3: 参数变化幅度约束 ----------
	result.ParamDeltaPct, result.DeltaExceeded = checkParamDelta(oldCfgJSON, newCfgJSON, e.cfg.MaxParamDeltaPct)
	if result.DeltaExceeded {
		result.Checks["param_delta"] = fmt.Sprintf(
			"FAIL (max delta %.1f%% > limit %.0f%%)", result.ParamDeltaPct*100, e.cfg.MaxParamDeltaPct*100)
		result.Reason = fmt.Sprintf("数字参数变化幅度 %.1f%% 超过容忍度 %.0f%%",
			result.ParamDeltaPct*100, e.cfg.MaxParamDeltaPct*100)
		return result
	}
	result.Checks["param_delta"] = fmt.Sprintf("PASS (max delta %.1f%%)", result.ParamDeltaPct*100)

	// ---------- 全部通过 ----------
	result.Verdict = VerdictShadowRun // 默认先进影子运行
	result.Reason = fmt.Sprintf("所有检查通过，进入影子运行观察期（%d 天）", 3)
	return result
}

// ForceApprove 跳过影子运行，直接 APPROVED（供手动触发或紧急场景使用）。
func (r *EvaluationResult) ForceApprove() {
	r.Verdict = VerdictApproved
	r.Reason = fmt.Sprintf("%s [FORCE_APPROVE]", r.Reason)
}

// checkParamDelta 比较 old/new config JSON 的数字参数变化幅度。
// 返回：最大变化百分比（相对原值，绝对值），以及是否超限。
func checkParamDelta(oldJSON, newJSON string, maxDeltaPct float64) (float64, bool) {
	var oldCfg, newCfg map[string]interface{}
	if err := json.Unmarshal([]byte(oldJSON), &oldCfg); err != nil {
		return 0, false // 解析失败不阻断
	}
	if err := json.Unmarshal([]byte(newJSON), &newCfg); err != nil {
		return 0, false
	}

	maxDelta := 0.0
	exceeded := false

	for key, newVal := range newCfg {
		oldVal, ok := oldCfg[key]
		if !ok {
			continue // 新参数不在旧配置里（新增字段，不算变化）
		}

		oldNum, ok1 := toFloat64(oldVal)
		newNum, ok2 := toFloat64(newVal)
		if !ok1 || !ok2 {
			continue // 非数字参数（字符串、布尔）不检查
		}

		if oldNum == 0 {
			continue // 旧值为 0 时百分比无意义
		}

		deltaPct := math.Abs(newNum-oldNum) / math.Abs(oldNum)
		if deltaPct > maxDelta {
			maxDelta = deltaPct
		}
		if deltaPct > maxDeltaPct {
			exceeded = true
		}
	}

	return maxDelta, exceeded
}

// toFloat64 将 interface{} 尝试转为 float64（兼容 JSON 解析的数字类型）。
func toFloat64(v interface{}) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		if err != nil {
			return 0, false
		}
		return f, true
	case string:
		f, err := strconv.ParseFloat(n, 64)
		if err != nil {
			return 0, false
		}
		return f, true
	}
	// 通过 reflect 兜底
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Int, reflect.Int32, reflect.Int64, reflect.Float32, reflect.Float64:
		return rv.Float(), true
	}
	return 0, false
}

// LogEvaluation 将验证结果打印到日志（异步）。
func LogEvaluation(strategyType string, r *EvaluationResult) {
	go func() {
		log.Printf("[Optimization.Evaluator] strategy=%s verdict=%s reason=%s param_delta=%.1f%%",
			strategyType, r.Verdict, r.Reason, r.ParamDeltaPct*100)
	}()
}
