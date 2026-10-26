package optimization

import (
	"encoding/json"
	"fmt"
	"log"
	"time"

	"gorm.io/gorm"
)

// Ledger 封装对 experiment_ledger 表的 CRUD 操作。
// 设计原则：
//   - 所有实验变更（调参、影子运行、回退）都必须写一条记录，无论 APPROVED/REJECTED。
//   - 写操作异步化（SafeGo）但主调参流程不等待——日志不能阻塞主业务。
//   - 提供读取接口供 evaluator / meta_learner 查询历史实验。
type Ledger struct {
	db *gorm.DB
}

// NewLedger 创建实验日志记录器
func NewLedger(db *gorm.DB) *Ledger {
	return &Ledger{db: db}
}

// Record 写入一条实验记录。verdict 可以是任意值，建议用常量 VerdictApproved 等。
// 所有数值参数为零值时不会影响写入（GORM 会写零值），调用方应确保关键指标有实值。
func (l *Ledger) Record(entry *ExperimentLedger) error {
	if l.db == nil {
		return fmt.Errorf("Ledger: db 未初始化")
	}
	if entry.ExperimentDate == "" {
		entry.ExperimentDate = time.Now().Format("2006-01-02")
	}
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now()
	}
	return l.db.Create(entry).Error
}

// RecordAsync 异步写入，失败仅打日志不阻塞主流程。
func (l *Ledger) RecordAsync(entry *ExperimentLedger) {
	go func() {
		if err := l.Record(entry); err != nil {
			log.Printf("[Optimization.Ledger] 写入实验日志失败: %v", err)
		}
	}()
}

// ListByStrategy 查询某策略最近 N 条实验记录（按创建时间倒序）。
func (l *Ledger) ListByStrategy(strategyID uint, limit int) ([]ExperimentLedger, error) {
	var entries []ExperimentLedger
	q := l.db.Where("strategy_id = ?", strategyID).Order("created_at DESC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	if err := q.Find(&entries).Error; err != nil {
		return nil, err
	}
	return entries, nil
}

// ListRecent 查询某日期范围内（含端点）的实验记录。
func (l *Ledger) ListRecent(fromDate, toDate string) ([]ExperimentLedger, error) {
	var entries []ExperimentLedger
	q := l.db.Where("experiment_date >= ? AND experiment_date <= ?", fromDate, toDate).
		Order("experiment_date DESC")
	if err := q.Find(&entries).Error; err != nil {
		return nil, err
	}
	return entries, nil
}

// CountByStrategySince 计算某策略自给定日期起的实验次数。
func (l *Ledger) CountByStrategySince(strategyID uint, sinceDate string) (int64, error) {
	var n int64
	err := l.db.Model(&ExperimentLedger{}).
		Where("strategy_id = ? AND experiment_date >= ?", strategyID, sinceDate).
		Count(&n).Error
	return n, err
}

// LastApprovedConfig 取出某策略最近一次 APPROVED 记录的 NewConfig（作为 baseline）。
// 若没有 APPROVED 记录，则返回空串。
func (l *Ledger) LastApprovedConfig(strategyID uint) (string, error) {
	var entry ExperimentLedger
	err := l.db.Where("strategy_id = ? AND verdict = ?", strategyID, VerdictApproved).
		Order("created_at DESC").Limit(1).
		First(&entry).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return "", nil
		}
		return "", err
	}
	return entry.NewConfig, nil
}

// UpdateShadowResult 回填影子运行结果到已有记录。
func (l *Ledger) UpdateShadowResult(entryID uint, resultJSON string) error {
	return l.db.Model(&ExperimentLedger{}).
		Where("id = ?", entryID).
		Updates(map[string]interface{}{
			"shadow_result": resultJSON,
			"verdict":       VerdictApproved, // 影子运行通过后才算真正 APPROVED
		}).Error
}

// BuildBaselineCompare 构造 baseline vs candidate 的 head-to-head 对比 JSON。
// 纯工具函数，无 DB 操作。
func BuildBaselineCompare(baseline, candidate *SampleMetrics) string {
	if baseline == nil || candidate == nil {
		return ""
	}
	diff := map[string]float64{
		"sharpe_diff":  candidate.Sharpe - baseline.Sharpe,
		"return_diff":  candidate.Return - baseline.Return,
		"maxdd_diff":   candidate.MaxDD - baseline.MaxDD, // 负值更好（DD 更小）
		"winrate_diff": candidate.WinRate - baseline.WinRate,
		"turnover_diff": candidate.Turnover - baseline.Turnover,
	}
	out, _ := json.Marshal(diff)
	return string(out)
}
