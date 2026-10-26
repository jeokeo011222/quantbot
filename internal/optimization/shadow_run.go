package optimization

import (
	"encoding/json"
	"fmt"
	"log"
	"time"
)

// ==================== ShadowRun 影子运行机制 ====================
//
// 设计目标：候选参数在 Evaluator 通过后不直接上线（Verdict=SHADOW_RUN），
// 而是进入 N 天观察期——每天收盘后用候选参数生成"影子"交易信号，
// 对比当天实际执行的交易信号，一致率 ≥ 80% 才算真正 APPROVED。
//
// 注意：P1 阶段影子运行为简化版——只记录信号方向（买/卖/观望）的一致率，
// 不对比精确成交和收益（实际 vs 影子有滑点差异）。
// P1.5 再升级为完整的假设回测（paper trade）。

// ShadowConfig 影子运行配置
type ShadowConfig struct {
	WatchDays        int     // 观察期天数（默认 3）
	MinConsensusRate float64 // 最小信号一致率（默认 0.80 = 80%）
}

// DefaultShadowConfig 默认配置
func DefaultShadowConfig() ShadowConfig {
	return ShadowConfig{
		WatchDays:        3,
		MinConsensusRate: 0.80,
	}
}

// ShadowRun 一次影子运行会话
type ShadowRun struct {
	ID             string    `json:"id"`
	StrategyID     uint      `json:"strategy_id"`
	StrategyType   string    `json:"strategy_type"`
	CandidateConfig string   `json:"candidate_config"` // JSON
	BaselineConfig  string   `json:"baseline_config"`  // JSON
	StartDate      string    `json:"start_date"` // YYYY-MM-DD
	WatchDays      int       `json:"watch_days"`
	Status         string    `json:"status"` // "WATCHING" / "APPROVED" / "REJECTED"
	DailyConsensus []DailyShadowResult `json:"daily_consensus"`
}

// DailyShadowResult 单日影子运行结果
type DailyShadowResult struct {
	Date             string  `json:"date"`
	ActualSignal     string  `json:"actual_signal"`   // 当日实际执行的信号方向
	ShadowSignal     string  `json:"shadow_signal"`   // 候选参数产生的信号方向
	Consensus        bool    `json:"consensus"`       // 是否一致
	ConsensusRate    float64 `json:"consensus_rate"`  // 累计一致率（到当日为止）
}

// Verdict 影子运行的最终结论
func (sr *ShadowRun) Verdict() (string, float64) {
	if len(sr.DailyConsensus) == 0 {
		return VerdictShadowRun, 0
	}
	cons := 0
	for _, d := range sr.DailyConsensus {
		if d.Consensus {
			cons++
		}
	}
	rate := float64(cons) / float64(len(sr.DailyConsensus))
	return sr.Status, rate
}

// MarkDay 记录单日影子运行结果并更新累计一致率。
func (sr *ShadowRun) MarkDay(date, actualSig, shadowSig string, totalDays int) {
	if len(sr.DailyConsensus) >= totalDays {
		return
	}
	consensus := actualSig == shadowSig
	sr.DailyConsensus = append(sr.DailyConsensus, DailyShadowResult{
		Date:         date,
		ActualSignal: actualSig,
		ShadowSignal: shadowSig,
		Consensus:    consensus,
	})

	// 更新累计一致率
	cons := 0
	for _, d := range sr.DailyConsensus {
		if d.Consensus {
			cons++
		}
	}
	sr.DailyConsensus[len(sr.DailyConsensus)-1].ConsensusRate =
		float64(cons) / float64(len(sr.DailyConsensus))

	// 检查是否达到观察期结束
	if len(sr.DailyConsensus) >= totalDays {
		// 一致率 ≥ MinConsensusRate 才算 APPROVED
		cfg := DefaultShadowConfig()
		finalRate := float64(cons) / float64(totalDays)
		if finalRate >= cfg.MinConsensusRate {
			sr.Status = VerdictApproved
		} else {
			sr.Status = VerdictRejected
		}
		log.Printf("[Optimization.ShadowRun] %s 影子运行结束: 一致率 %.1f%% → %s",
			sr.StrategyType, finalRate*100, sr.Status)
	}
}

// ToJSON 影子运行结果序列化为 JSON（写入 ExperimentLedger.ShadowResult）
func (sr *ShadowRun) ToJSON() string {
	b, _ := json.Marshal(sr)
	return string(b)
}

// NewShadowRun 创建新的影子运行会话
func NewShadowRun(strategyID uint, strategyType, oldConfig, newConfig string) *ShadowRun {
	cfg := DefaultShadowConfig()
	return &ShadowRun{
		ID:               fmt.Sprintf("shadow_%d_%s", strategyID, time.Now().Format("20060102150405")),
		StrategyID:       strategyID,
		StrategyType:     strategyType,
		CandidateConfig:  newConfig,
		BaselineConfig:   oldConfig,
		StartDate:        time.Now().Format("2006-01-02"),
		WatchDays:        cfg.WatchDays,
		Status:           VerdictShadowRun,
		DailyConsensus:   []DailyShadowResult{},
	}
}
