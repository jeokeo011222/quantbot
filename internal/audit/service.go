package audit

import (
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/quantpilot/quantpilot/internal/data"
)

const (
	// DefaultRetentionDays 审计日志默认保留天数（6个月）
	DefaultRetentionDays = 180

	// MaxWriteRetries 写入失败最大重试次数
	MaxWriteRetries = 3

	// RetryInterval 重试间隔
	RetryInterval = 100 * time.Millisecond

	// BatchFlushInterval 批量刷新间隔
	BatchFlushInterval = 5 * time.Second

	// MaxBatchSize 最大批量写入大小
	MaxBatchSize = 100

	// CleanupInterval 清理检查间隔
	CleanupInterval = 24 * time.Hour
)

// AuditService 统一审计服务
type AuditService struct {
	db             *data.SQLiteManager
	retentionDays  int

	// 批量写入相关
	mu             sync.Mutex
	batch          []data.AuditLog
	lastFlushTime  time.Time
	lastCleanupTime time.Time

	// 状态统计
	totalWritten   int64
	totalFailed    int64
	totalCleaned   int64
}

// NewAuditService 创建审计服务
func NewAuditService(db *data.SQLiteManager) *AuditService {
	s := &AuditService{
		db:             db,
		retentionDays:  DefaultRetentionDays,
		batch:          make([]data.AuditLog, 0, MaxBatchSize),
		lastFlushTime:  time.Now(),
		lastCleanupTime: time.Now(),
	}

	go s.flushLoop()
	go s.cleanupLoop()

	return s
}

// SetRetentionDays 设置日志保留天数
func (s *AuditService) SetRetentionDays(days int) {
	if days < 30 {
		days = 30
	}
	if days > 3650 {
		days = 3650
	}
	s.retentionDays = days
}

// GetRetentionDays 获取日志保留天数
func (s *AuditService) GetRetentionDays() int {
	return s.retentionDays
}

// LogAuditEvent 记录审计事件（异步批量写入，不阻塞主流程）
func (s *AuditService) LogAuditEvent(eventType, action, targetType, targetID, userID, userName string, result string, details interface{}) {
	if s.db == nil {
		return
	}

	detailsJSON, _ := json.Marshal(details)

	event := data.AuditLog{
		EventID:     uuid.New().String(),
		EventType:   eventType,
		UserID:      userID,
		UserName:    userName,
		Action:      action,
		TargetType:  targetType,
		TargetID:    targetID,
		Result:      result,
		DetailsJSON: string(detailsJSON),
		Timestamp:   time.Now(),
	}

	s.addToBatch(event)
}

// LogAuditEventSync 同步记录审计事件（重要操作使用）
func (s *AuditService) LogAuditEventSync(eventType, action, targetType, targetID, userID, userName string, result string, details interface{}) error {
	if s.db == nil {
		return fmt.Errorf("数据库未初始化")
	}

	detailsJSON, _ := json.Marshal(details)

	event := data.AuditLog{
		EventID:     uuid.New().String(),
		EventType:   eventType,
		UserID:      userID,
		UserName:    userName,
		Action:      action,
		TargetType:  targetType,
		TargetID:    targetID,
		Result:      result,
		DetailsJSON: string(detailsJSON),
		Timestamp:   time.Now(),
	}

	for i := 0; i < MaxWriteRetries; i++ {
		if err := s.db.GetDB().Create(&event).Error; err != nil {
			log.Printf("[Audit] Failed to write audit event (attempt %d/%d): %v", i+1, MaxWriteRetries, err)
			if i < MaxWriteRetries-1 {
				time.Sleep(RetryInterval * time.Duration(i+1))
				continue
			}
			s.totalFailed++
			return fmt.Errorf("写入审计日志失败（重试%d次后）: %w", MaxWriteRetries, err)
		}
		s.totalWritten++
		return nil
	}
	return nil
}

// addToBatch 添加到批量写入队列
func (s *AuditService) addToBatch(event data.AuditLog) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.batch = append(s.batch, event)

	// 如果达到批量大小，立即刷新
	if len(s.batch) >= MaxBatchSize {
		s.flushBatchLocked()
	}
}

// flushBatchLocked 刷新批量写入（调用方需持有锁）
func (s *AuditService) flushBatchLocked() {
	if len(s.batch) == 0 {
		return
	}

	batch := make([]data.AuditLog, len(s.batch))
	copy(batch, s.batch)
	s.batch = s.batch[:0]
	s.lastFlushTime = time.Now()

	go s.writeBatch(batch)
}

// writeBatch 批量写入数据库
func (s *AuditService) writeBatch(batch []data.AuditLog) {
	if s.db == nil || len(batch) == 0 {
		return
	}

	for i := 0; i < MaxWriteRetries; i++ {
		result := s.db.GetDB().Create(&batch)
		if result.Error == nil {
			s.totalWritten += int64(len(batch))
			log.Printf("[Audit] Batch write success: %d events", len(batch))
			return
		}
		log.Printf("[Audit] Batch write failed (attempt %d/%d): %v", i+1, MaxWriteRetries, result.Error)
		if i < MaxWriteRetries-1 {
			time.Sleep(RetryInterval * time.Duration(i+1))
		}
	}

	s.totalFailed += int64(len(batch))
	log.Printf("[Audit] Batch write permanently failed: %d events lost", len(batch))
}

// flushLoop 定期刷新批量写入
func (s *AuditService) flushLoop() {
	ticker := time.NewTicker(BatchFlushInterval)
	defer ticker.Stop()

	for range ticker.C {
		s.mu.Lock()
		if time.Since(s.lastFlushTime) >= BatchFlushInterval && len(s.batch) > 0 {
			s.flushBatchLocked()
		}
		s.mu.Unlock()
	}
}

// cleanupLoop 定期清理过期日志
func (s *AuditService) cleanupLoop() {
	ticker := time.NewTicker(CleanupInterval)
	defer ticker.Stop()

	for range ticker.C {
		s.cleanupExpiredLogs()
	}
}

// cleanupExpiredLogs 清理过期日志
func (s *AuditService) cleanupExpiredLogs() {
	if s.db == nil {
		return
	}

	cutoffDate := time.Now().AddDate(0, 0, -s.retentionDays)

	var count int64
	result := s.db.GetDB().Model(&data.AuditLog{}).
		Where("timestamp < ?", cutoffDate).
		Count(&count)

	if result.Error != nil {
		log.Printf("[Audit] Cleanup count failed: %v", result.Error)
		return
	}

	if count == 0 {
		s.lastCleanupTime = time.Now()
		return
	}

	log.Printf("[Audit] Starting cleanup: %d events older than %d days will be removed", count, s.retentionDays)

	// 分批删除，每批最多1000条
	deletedCount := int64(0)
	for {
		result := s.db.GetDB().
			Where("timestamp < ?", cutoffDate).
			Limit(1000).
			Delete(&data.AuditLog{})

		if result.Error != nil {
			log.Printf("[Audit] Cleanup delete failed: %v", result.Error)
			break
		}

		deletedCount += result.RowsAffected
		if result.RowsAffected == 0 {
			break
		}
	}

	if deletedCount > 0 {
		s.totalCleaned += deletedCount
		log.Printf("[Audit] Cleanup completed: %d events removed (total removed: %d)", deletedCount, s.totalCleaned)
	}

	s.lastCleanupTime = time.Now()
}

// ForceCleanup 强制执行清理（忽略时间间隔限制）
func (s *AuditService) ForceCleanup() (int64, error) {
	if s.db == nil {
		return 0, fmt.Errorf("数据库未初始化")
	}

	// 先刷新待写入的批次
	s.mu.Lock()
	if len(s.batch) > 0 {
		s.flushBatchLocked()
	}
	s.mu.Unlock()

	cutoffDate := time.Now().AddDate(0, 0, -s.retentionDays)

	var deletedCount int64
	for {
		result := s.db.GetDB().
			Where("timestamp < ?", cutoffDate).
			Limit(1000).
			Delete(&data.AuditLog{})

		if result.Error != nil {
			return deletedCount, fmt.Errorf("删除失败: %w", result.Error)
		}

		deletedCount += result.RowsAffected
		if result.RowsAffected == 0 {
			break
		}
	}

	s.totalCleaned += deletedCount
	s.lastCleanupTime = time.Now()

	return deletedCount, nil
}

// Flush 强制刷新所有待写入的日志
func (s *AuditService) Flush() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.batch) > 0 {
		s.flushBatchLocked()
	}
}

// GetStats 获取审计服务统计信息
func (s *AuditService) GetStats() map[string]interface{} {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 统计数据库中的日志数量
	var totalLogs int64
	var todayLogs int64
	cutoffDate := time.Now().AddDate(0, 0, -s.retentionDays)

	if s.db != nil {
		s.db.GetDB().Model(&data.AuditLog{}).Count(&totalLogs)
		todayStart := time.Date(time.Now().Year(), time.Now().Month(), time.Now().Day(), 0, 0, 0, 0, time.UTC)
		s.db.GetDB().Model(&data.AuditLog{}).Where("timestamp >= ?", todayStart).Count(&todayLogs)
	}

	return map[string]interface{}{
		"retention_days":   s.retentionDays,
		"expiry_date":      cutoffDate.Format("2006-01-02"),
		"total_logs":       totalLogs,
		"today_logs":        todayLogs,
		"pending_batch":     len(s.batch),
		"total_written":    s.totalWritten,
		"total_failed":     s.totalFailed,
		"total_cleaned":    s.totalCleaned,
		"last_flush_time":  s.lastFlushTime.Format(time.RFC3339),
		"last_cleanup_time": s.lastCleanupTime.Format(time.RFC3339),
	}
}

// LogLogin 记录登录事件
func (s *AuditService) LogLogin(userID, userName string) {
	s.LogAuditEvent(data.AuditEventLogin, "用户登录", "user", userID, userID, userName, "success", nil)
}

// LogLogout 记录登出事件
func (s *AuditService) LogLogout(userID, userName string) {
	s.LogAuditEvent(data.AuditEventLogout, "用户登出", "user", userID, userID, userName, "success", nil)
}

// LogScreening 记录选股事件
func (s *AuditService) LogScreening(strategyID, market string, resultCount int, success bool) {
	result := "success"
	if !success {
		result = "failed"
	}
	s.LogAuditEvent(data.AuditEventScreening,
		fmt.Sprintf("执行选股: 策略=%s, 市场=%s, 结果数=%d", strategyID, market, resultCount),
		"screening", strategyID,
		"system", "QuantBot",
		result,
		map[string]interface{}{
			"strategy_id":  strategyID,
			"market":       market,
			"result_count": resultCount,
		})
}

// LogPlanner 记录投资规划事件
func (s *AuditService) LogPlanner(action string, planID string, userID string, details interface{}) {
	s.LogAuditEvent(data.AuditEventPlanner, action, "plan", planID, userID, "user", "success", details)
}

// LogBacktest 记录回测事件
func (s *AuditService) LogBacktest(strategyID string, success bool, details interface{}) {
	result := "success"
	if !success {
		result = "failed"
	}
	s.LogAuditEvent(data.AuditEventBacktest,
		fmt.Sprintf("执行回测: 策略=%s", strategyID),
		"backtest", strategyID,
		"system", "QuantBot",
		result, details)
}

// LogAIAnalysis 记录AI分析事件
func (s *AuditService) LogAIAnalysis(analysisType string, targetID string, success bool, details interface{}) {
	result := "success"
	if !success {
		result = "failed"
	}
	s.LogAuditEvent(data.AuditEventAIAnalysis,
		fmt.Sprintf("AI分析: 类型=%s", analysisType),
		"analysis", targetID,
		"system", "QuantBot",
		result, details)
}

// LogCIODailyReview 记录CIO每日复盘事件
func (s *AuditService) LogCIODailyReview(date string, reviewID string, summary string, details interface{}) {
	s.LogAuditEvent(data.AuditEventCIODailyReview,
		fmt.Sprintf("CIO每日复盘: %s", date),
		"cio_review", reviewID,
		"cio", "CIO Agent",
		"success",
		map[string]interface{}{
			"review_date": date,
			"summary":     summary,
			"details":     details,
		})
}

// LogLiveActivity 记录实时活动事件
func (s *AuditService) LogLiveActivity(agentRole, activityType, description string, details interface{}) {
	s.LogAuditEvent(data.AuditEventLiveActivity,
		fmt.Sprintf("实时活动: %s - %s", agentRole, description),
		"agent_activity", agentRole,
		"system", "QuantBot",
		"success",
		map[string]interface{}{
			"agent_role":    agentRole,
			"activity_type": activityType,
			"description":   description,
			"details":       details,
		})
}

// LogConfigChange 记录配置变更事件
func (s *AuditService) LogConfigChange(configKey string, oldValue, newValue string, userID string) {
	s.LogAuditEvent(data.AuditEventConfigChange,
		fmt.Sprintf("配置变更: %s", configKey),
		"config", configKey,
		userID, "user",
		"success",
		map[string]interface{}{
			"old_value": oldValue,
			"new_value": newValue,
		})
}

// LogTierChange 记录版本升级事件
func (s *AuditService) LogTierChange(oldTier, newTier string, userID string) {
	s.LogAuditEvent(data.AuditEventTierChange,
		fmt.Sprintf("版本升级: %s -> %s", oldTier, newTier),
		"tier", newTier,
		userID, "user",
		"success",
		map[string]interface{}{
			"old_tier": oldTier,
			"new_tier": newTier,
		})
}

// QueryAuditLogs 查询审计日志（支持筛选+分页）
type AuditQuery struct {
	EventType string `json:"eventType"`
	StartDate string `json:"startDate"`
	EndDate   string `json:"endDate"`
	UserID    string `json:"userID"`
	Page      int    `json:"page"`
	PageSize  int    `json:"pageSize"`
}

type AuditLogItem struct {
	ID          uint      `json:"id"`
	EventID     string    `json:"eventId"`
	EventType   string    `json:"eventType"`
	UserID      string    `json:"userId"`
	UserName    string    `json:"userName"`
	Action      string    `json:"action"`
	TargetType  string    `json:"targetType"`
	TargetID    string    `json:"targetId"`
	Result      string    `json:"result"`
	DetailsJSON string    `json:"detailsJson"`
	Timestamp   time.Time `json:"timestamp"`
}

type AuditQueryResult struct {
	Total    int64          `json:"total"`
	Page     int            `json:"page"`
	PageSize int            `json:"pageSize"`
	Items    []AuditLogItem `json:"items"`
}

// QueryLogs 按条件查询审计日志
func (s *AuditService) QueryLogs(query AuditQuery) (*AuditQueryResult, error) {
	if s.db == nil {
		return &AuditQueryResult{Total: 0, Items: []AuditLogItem{}}, nil
	}

	if query.Page <= 0 {
		query.Page = 1
	}
	if query.PageSize <= 0 || query.PageSize > 100 {
		query.PageSize = 20
	}

	db := s.db.GetDB().Model(&data.AuditLog{})

	if query.EventType != "" {
		db = db.Where("event_type = ?", query.EventType)
	}
	if query.UserID != "" {
		db = db.Where("user_id = ?", query.UserID)
	}
	if query.StartDate != "" {
		db = db.Where("timestamp >= ?", query.StartDate)
	}
	if query.EndDate != "" {
		db = db.Where("timestamp <= ?", query.EndDate+" 23:59:59")
	}

	var total int64
	if err := db.Count(&total).Error; err != nil {
		return nil, err
	}

	var items []data.AuditLog
	offset := (query.Page - 1) * query.PageSize
	if err := db.Order("timestamp DESC").Offset(offset).Limit(query.PageSize).Find(&items).Error; err != nil {
		return nil, err
	}

	result := make([]AuditLogItem, len(items))
	for i, item := range items {
		result[i] = AuditLogItem{
			ID:          item.ID,
			EventID:     item.EventID,
			EventType:   item.EventType,
			UserID:      item.UserID,
			UserName:    item.UserName,
			Action:      item.Action,
			TargetType:  item.TargetType,
			TargetID:    item.TargetID,
			Result:      item.Result,
			DetailsJSON: item.DetailsJSON,
			Timestamp:   item.Timestamp,
		}
	}

	return &AuditQueryResult{
		Total:    total,
		Page:     query.Page,
		PageSize: query.PageSize,
		Items:    result,
	}, nil
}
