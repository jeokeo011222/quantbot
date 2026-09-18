package screener

import (
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"time"

	"github.com/quantpilot/quantpilot/internal/data"
	"github.com/quantpilot/quantpilot/internal/util"
	"gorm.io/gorm"
)

// TradeablePool 可交易股票池管理
type TradeablePool struct {
	db *data.SQLiteManager
}

// NewTradeablePool 创建可交易股票池
func NewTradeablePool(db *data.SQLiteManager) *TradeablePool {
	return &TradeablePool{db: db}
}

// SubmitStock 提交选股结果到可交易股票池
func (tp *TradeablePool) SubmitStock(stock *data.TradeableStock) error {
	db := tp.getDB()
	if db == nil {
		return fmt.Errorf("数据库未初始化")
	}

	// 设置默认值
	if stock.SubmittedAt.IsZero() {
		stock.SubmittedAt = time.Now()
	}
	if stock.Status == "" {
		stock.Status = "PENDING"
	}
	if stock.Priority == 0 {
		stock.Priority = 3
	}
	if stock.SelectionSource == "" {
		stock.SelectionSource = "SCREENER"
	}

	// 检查是否已存在
	var existing data.TradeableStock
	result := db.Where("stock_code = ?", stock.StockCode).First(&existing)
	if result.Error == nil {
		// 更新已存在的记录
		existing.CurrentPrice = stock.CurrentPrice
		existing.CompositeScore = stock.CompositeScore
		existing.SelectionReason = stock.SelectionReason
		existing.RiskWarning = stock.RiskWarning
		existing.SubmittedAt = stock.SubmittedAt
		existing.Status = "PENDING"
		existing.Priority = stock.Priority
		existing.SuggestedWeight = stock.SuggestedWeight
		existing.TargetPrice = stock.TargetPrice
		existing.StopLossPrice = stock.StopLossPrice
		if stock.PlanID != "" {
			existing.PlanID = stock.PlanID
		}
		if stock.FactorVersion != "" {
			existing.FactorVersion = stock.FactorVersion
		}
		if stock.MarketDate != nil {
			existing.MarketDate = stock.MarketDate
		}
		existing.UpdatedAt = time.Now()

		if err := db.Save(&existing).Error; err != nil {
			return fmt.Errorf("更新已存在的股票失败: %w", err)
		}
		tp.addLog(existing.StockCode, "SUBMIT", existing.Status, existing.Status, stock.SubmitterID, stock.SubmitterName, "更新选股结果", nil)
		log.Printf("[TradeablePool] Updated stock: %s", stock.StockCode)
		return nil
	}

	// 创建新记录
	if err := db.Create(stock).Error; err != nil {
		return fmt.Errorf("创建股票记录失败: %w", err)
	}

	tp.addLog(stock.StockCode, "SUBMIT", "", stock.Status, stock.SubmitterID, stock.SubmitterName, "提交选股结果", nil)
	log.Printf("[TradeablePool] Submitted stock: %s (%s)", stock.StockCode, stock.StockName)

	return nil
}

// SubmitStocks 批量提交选股结果
func (tp *TradeablePool) SubmitStocks(stocks []*data.TradeableStock) (int, error) {
	successCount := 0
	for _, stock := range stocks {
		if err := tp.SubmitStock(stock); err != nil {
			log.Printf("[TradeablePool] Failed to submit %s: %v", stock.StockCode, err)
			continue
		}
		successCount++
	}
	return successCount, nil
}

// ApproveStock 审核批准股票
func (tp *TradeablePool) ApproveStock(stockCode, reviewerID, reviewerName, comment string, approvedQuantity int) error {
	db := tp.getDB()
	if db == nil {
		return fmt.Errorf("数据库未初始化")
	}

	var stock data.TradeableStock
	if err := db.Where("stock_code = ?", stockCode).First(&stock).Error; err != nil {
		return fmt.Errorf("股票不存在: %s", stockCode)
	}

	if stock.Status != "PENDING" {
		return fmt.Errorf("股票状态不是PENDING，无法审核: %s", stock.Status)
	}

	now := time.Now()
	oldStatus := stock.Status
	stock.Status = "APPROVED"
	stock.ReviewerID = reviewerID
	stock.ReviewerName = reviewerName
	stock.ReviewComment = comment
	stock.ReviewedAt = &now
	stock.ApprovedQuantity = approvedQuantity
	stock.UpdatedAt = now

	if err := db.Save(&stock).Error; err != nil {
		return fmt.Errorf("保存审核结果失败: %w", err)
	}

	tp.addLog(stockCode, "APPROVE", oldStatus, stock.Status, reviewerID, reviewerName, comment, nil)
	log.Printf("[TradeablePool] Approved stock: %s", stockCode)

	return nil
}

// RejectStock 审核拒绝股票
func (tp *TradeablePool) RejectStock(stockCode, reviewerID, reviewerName, comment string) error {
	db := tp.getDB()
	if db == nil {
		return fmt.Errorf("数据库未初始化")
	}

	var stock data.TradeableStock
	if err := db.Where("stock_code = ?", stockCode).First(&stock).Error; err != nil {
		return fmt.Errorf("股票不存在: %s", stockCode)
	}

	if stock.Status != "PENDING" {
		return fmt.Errorf("股票状态不是PENDING，无法审核: %s", stock.Status)
	}

	now := time.Now()
	oldStatus := stock.Status
	stock.Status = "REJECTED"
	stock.ReviewerID = reviewerID
	stock.ReviewerName = reviewerName
	stock.ReviewComment = comment
	stock.ReviewedAt = &now
	stock.UpdatedAt = now

	if err := db.Save(&stock).Error; err != nil {
		return fmt.Errorf("保存审核结果失败: %w", err)
	}

	tp.addLog(stockCode, "REJECT", oldStatus, stock.Status, reviewerID, reviewerName, comment, nil)
	log.Printf("[TradeablePool] Rejected stock: %s", stockCode)

	return nil
}

// BatchApprove 批量批准
func (tp *TradeablePool) BatchApprove(stockCodes []string, reviewerID, reviewerName string) (int, error) {
	count := 0
	for _, code := range stockCodes {
		// 检查当前价格
		var stock data.TradeableStock
		db := tp.getDB()
		if err := db.Where("stock_code = ?", code).First(&stock).Error; err != nil {
			continue
		}

		// 计算建议的批准数量
		approvedQty := 100
		if stock.CurrentPrice > 0 {
			// 默认批准100股
			approvedQty = 100
		}

		if err := tp.ApproveStock(code, reviewerID, reviewerName, "批量批准", approvedQty); err != nil {
			continue
		}
		count++
	}
	return count, nil
}

// BatchReject 批量拒绝
func (tp *TradeablePool) BatchReject(stockCodes []string, reviewerID, reviewerName string) (int, error) {
	count := 0
	for _, code := range stockCodes {
		if err := tp.RejectStock(code, reviewerID, reviewerName, "批量拒绝"); err != nil {
			continue
		}
		count++
	}
	return count, nil
}

// GetStock 获取单只股票
func (tp *TradeablePool) GetStock(stockCode string) (*data.TradeableStock, error) {
	db := tp.getDB()
	if db == nil {
		return nil, fmt.Errorf("数据库未初始化")
	}

	var stock data.TradeableStock
	if err := db.Where("stock_code = ?", stockCode).First(&stock).Error; err != nil {
		return nil, fmt.Errorf("股票不存在: %s", stockCode)
	}

	return &stock, nil
}

// GetStocksByStatus 按状态获取股票列表
func (tp *TradeablePool) GetStocksByStatus(status string) ([]*data.TradeableStock, error) {
	db := tp.getDB()
	if db == nil {
		return nil, fmt.Errorf("数据库未初始化")
	}

	var stocks []data.TradeableStock
	query := db.Model(&data.TradeableStock{})

	if status != "" {
		query = query.Where("status = ?", status)
	}

	if err := query.Order("priority DESC, composite_score DESC").Find(&stocks).Error; err != nil {
		return nil, err
	}

	result := make([]*data.TradeableStock, len(stocks))
	for i := range stocks {
		result[i] = &stocks[i]
	}

	return result, nil
}

// GetPendingStocks 获取待审核股票
func (tp *TradeablePool) GetPendingStocks() ([]*data.TradeableStock, error) {
	return tp.GetStocksByStatus("PENDING")
}

// GetApprovedStocks 获取已批准股票（可交易）
func (tp *TradeablePool) GetApprovedStocks() ([]*data.TradeableStock, error) {
	return tp.GetStocksByStatus("APPROVED")
}

// GetBuyableStocks 获取可买入股票（已批准且未完全买入）
func (tp *TradeablePool) GetBuyableStocks() ([]*data.TradeableStock, error) {
	db := tp.getDB()
	if db == nil {
		return nil, fmt.Errorf("数据库未初始化")
	}

	var stocks []data.TradeableStock
	err := db.Where("status = ?", "APPROVED").
		Where("approved_quantity > bought_quantity").
		Order("priority DESC, composite_score DESC").
		Find(&stocks).Error

	if err != nil {
		return nil, err
	}

	result := make([]*data.TradeableStock, len(stocks))
	for i := range stocks {
		result[i] = &stocks[i]
	}

	return result, nil
}

// UpdateStockAfterBuy 买入后更新股票状态
func (tp *TradeablePool) UpdateStockAfterBuy(stockCode string, quantity int, price float64) error {
	db := tp.getDB()
	if db == nil {
		return fmt.Errorf("数据库未初始化")
	}

	var stock data.TradeableStock
	if err := db.Where("stock_code = ?", stockCode).First(&stock).Error; err != nil {
		return fmt.Errorf("股票不存在: %s", stockCode)
	}

	stock.BoughtQuantity += quantity
	stock.BoughtPrice = price
	now := time.Now()
	stock.BoughtAt = &now
	stock.UpdatedAt = now

	// 如果已完全买入，更新状态
	if stock.BoughtQuantity >= stock.ApprovedQuantity {
		stock.Status = "BOUGHT"
	}

	if err := db.Save(&stock).Error; err != nil {
		return fmt.Errorf("更新买入状态失败: %w", err)
	}

	tp.addLog(stockCode, "BUY", "APPROVED", stock.Status, "system", "QuantBot",
		fmt.Sprintf("买入 %d 股 @ ¥%.2f", quantity, price),
		map[string]interface{}{"quantity": quantity, "price": price})

	log.Printf("[TradeablePool] Stock bought: %s, qty=%d, price=%.2f", stockCode, quantity, price)

	return nil
}

// CancelStock 取消股票
func (tp *TradeablePool) CancelStock(stockCode, operatorID, operatorName, reason string) error {
	db := tp.getDB()
	if db == nil {
		return fmt.Errorf("数据库未初始化")
	}

	var stock data.TradeableStock
	if err := db.Where("stock_code = ?", stockCode).First(&stock).Error; err != nil {
		return fmt.Errorf("股票不存在: %s", stockCode)
	}

	oldStatus := stock.Status
	stock.Status = "CANCELLED"
	stock.UpdatedAt = time.Now()

	if err := db.Save(&stock).Error; err != nil {
		return fmt.Errorf("取消失败: %w", err)
	}

	tp.addLog(stockCode, "CANCEL", oldStatus, stock.Status, operatorID, operatorName, reason, nil)
	log.Printf("[TradeablePool] Cancelled stock: %s", stockCode)

	return nil
}

// DeleteStock 删除股票（仅限管理员）
func (tp *TradeablePool) DeleteStock(stockCode string) error {
	db := tp.getDB()
	if db == nil {
		return fmt.Errorf("数据库未初始化")
	}

	result := db.Where("stock_code = ?", stockCode).Delete(&data.TradeableStock{})
	if result.Error != nil {
		return fmt.Errorf("删除失败: %w", result.Error)
	}

	// 删除相关日志
	db.Where("stock_code = ?", stockCode).Delete(&data.TradeableStockLog{})

	log.Printf("[TradeablePool] Deleted stock: %s, rows affected: %d", stockCode, result.RowsAffected)
	return nil
}

// GetStockLogs 获取股票操作日志
func (tp *TradeablePool) GetStockLogs(stockCode string) ([]*data.TradeableStockLog, error) {
	db := tp.getDB()
	if db == nil {
		return nil, fmt.Errorf("数据库未初始化")
	}

	var logs []data.TradeableStockLog
	query := db.Model(&data.TradeableStockLog{})
	if stockCode != "" {
		query = query.Where("stock_code = ?", stockCode)
	}

	if err := query.Order("created_at DESC").Limit(100).Find(&logs).Error; err != nil {
		return nil, err
	}

	result := make([]*data.TradeableStockLog, len(logs))
	for i := range logs {
		result[i] = &logs[i]
	}

	return result, nil
}

// GetPoolSummary 获取股票池统计信息
func (tp *TradeablePool) GetPoolSummary() *PoolSummary {
	db := tp.getDB()
	if db == nil {
		return &PoolSummary{}
	}

	var summary PoolSummary

	db.Model(&data.TradeableStock{}).Where("status = ?", "PENDING").Count(&summary.PendingCount)
	db.Model(&data.TradeableStock{}).Where("status = ?", "APPROVED").Count(&summary.ApprovedCount)
	db.Model(&data.TradeableStock{}).Where("status = ?", "BOUGHT").Count(&summary.BoughtCount)
	db.Model(&data.TradeableStock{}).Where("status = ?", "REJECTED").Count(&summary.RejectedCount)
	db.Model(&data.TradeableStock{}).Where("status = ?", "SOLD").Count(&summary.SoldCount)
	db.Model(&data.TradeableStock{}).Where("status = ?", "EXPIRED").Count(&summary.ExpiredCount)

	summary.TotalCount = summary.PendingCount + summary.ApprovedCount + summary.BoughtCount + summary.RejectedCount + summary.SoldCount + summary.ExpiredCount

	return &summary
}

// StockPoolMeta 股票池生命周期元数据（明确股票池来源和有效性）
type StockPoolMeta struct {
	PlanID        string     // 关联投资方案ID
	FactorVersion string     // 因子版本
	MarketDate    *time.Time // 市场数据日期
}

// SubmitScreenerResult 将选股引擎结果提交到股票池
func (tp *TradeablePool) SubmitScreenerResult(result *ScreeningResponse, submitterID, submitterName string) (int, error) {
	return tp.SubmitScreenerResultWithMeta(result, submitterID, submitterName, StockPoolMeta{})
}

// SubmitScreenerResultWithMeta 将选股引擎结果提交到股票池（携带生命周期元数据）
func (tp *TradeablePool) SubmitScreenerResultWithMeta(result *ScreeningResponse, submitterID, submitterName string, meta StockPoolMeta) (int, error) {
	var stocks []*data.TradeableStock

	for _, s := range result.Results {
		factorScoresJSON, _ := json.Marshal(s.FactorScores)

		stock := &data.TradeableStock{
			StockCode:        s.Market + s.Code,
			StockName:        s.Name,
			Market:           s.Market,
			CurrentPrice:     s.Price,
			CompositeScore:   s.TotalScore / 100.0,
			FactorScoresJSON: string(factorScoresJSON),
			SelectionReason:  joinStrings(s.Reasons),
			RiskWarning:      joinStrings(s.Warnings),
			SelectionSource:  "SCREENER",
			StrategyID:       result.StrategyName,
			PlanID:           meta.PlanID,
			FactorVersion:    meta.FactorVersion,
			MarketDate:       meta.MarketDate,
			Status:           "PENDING",
			Priority:         calculatePriority(s.TotalScore),
			SuggestedWeight:  0.05, // 默认5%
			MaxPositionPct:   0.10,
			MinPositionPct:   0.01,
			OrderType:        "LIMIT",
			SubmitterID:      submitterID,
			SubmitterName:    submitterName,
			SubmittedAt:      time.Now(),
			Tags:             result.StrategyName + "," + s.Market,
		}

		stocks = append(stocks, stock)
	}

	return tp.SubmitStocks(stocks)
}

// GetAllStocksWithFilter 带筛选的股票列表
func (tp *TradeablePool) GetAllStocksWithFilter(status, market, selectionSource string, sortBy string, limit int) ([]*data.TradeableStock, error) {
	db := tp.getDB()
	if db == nil {
		return nil, fmt.Errorf("数据库未初始化")
	}

	var stocks []data.TradeableStock
	query := db.Model(&data.TradeableStock{})

	if status != "" {
		query = query.Where("status = ?", status)
	}
	if market != "" {
		query = query.Where("market = ?", market)
	}
	if selectionSource != "" {
		query = query.Where("selection_source = ?", selectionSource)
	}

	// 排序
	switch sortBy {
	case "score":
		query = query.Order("composite_score DESC")
	case "priority":
		query = query.Order("priority DESC")
	case "price":
		query = query.Order("current_price DESC")
	case "submitted":
		query = query.Order("submitted_at DESC")
	default:
		query = query.Order("priority DESC, composite_score DESC")
	}

	if limit > 0 {
		query = query.Limit(limit)
	}

	if err := query.Find(&stocks).Error; err != nil {
		return nil, err
	}

	result := make([]*data.TradeableStock, len(stocks))
	for i := range stocks {
		result[i] = &stocks[i]
	}

	return result, nil
}

// 辅助函数

func (tp *TradeablePool) getDB() *gorm.DB {
	if tp.db == nil {
		return nil
	}
	return tp.db.GetDB()
}

func (tp *TradeablePool) addLog(stockCode, action, fromStatus, toStatus, operatorID, operatorName, comment string, details interface{}) {
	db := tp.getDB()
	if db == nil {
		return
	}

	detailsJSON, _ := json.Marshal(details)

	logEntry := data.TradeableStockLog{
		StockCode:    stockCode,
		Action:       action,
		FromStatus:   fromStatus,
		ToStatus:     toStatus,
		OperatorID:   operatorID,
		OperatorName: operatorName,
		Comment:      comment,
		DetailsJSON:  string(detailsJSON),
		CreatedAt:    time.Now(),
	}

	util.SafeGoWithRetry("TradeablePool.addLog", 3, func() error {
		return db.Create(&logEntry).Error
	})
}

func joinStrings(strs []string) string {
	if len(strs) == 0 {
		return ""
	}
	result := strs[0]
	for i := 1; i < len(strs); i++ {
		result += "; " + strs[i]
	}
	return result
}

func calculatePriority(score float64) int {
	switch {
	case score >= 85:
		return 1 // 最高优先级
	case score >= 70:
		return 2
	case score >= 55:
		return 3
	case score >= 40:
		return 4
	default:
		return 5
	}
}

// SortByPriority 按优先级排序股票列表
func SortByPriority(stocks []*data.TradeableStock) {
	sort.Slice(stocks, func(i, j int) bool {
		if stocks[i].Priority != stocks[j].Priority {
			return stocks[i].Priority < stocks[j].Priority
		}
		return stocks[i].CompositeScore > stocks[j].CompositeScore
	})
}

// PoolSummary 股票池汇总
type PoolSummary struct {
	PendingCount  int64 `json:"pending_count"`
	ApprovedCount int64 `json:"approved_count"`
	BoughtCount   int64 `json:"bought_count"`
	RejectedCount int64 `json:"rejected_count"`
	TotalCount    int64 `json:"total_count"`
	SoldCount     int64 `json:"sold_count"`
	ExpiredCount  int64 `json:"expired_count"`
}

// GetBoughtStocks 获取已买入股票
func (tp *TradeablePool) GetBoughtStocks() ([]*data.TradeableStock, error) {
	db := tp.getDB()
	if db == nil {
		return nil, fmt.Errorf("数据库未初始化")
	}

	var stocks []data.TradeableStock
	err := db.Where("status = ?", "BOUGHT").
		Where("bought_quantity > 0").
		Find(&stocks).Error

	if err != nil {
		return nil, err
	}

	result := make([]*data.TradeableStock, len(stocks))
	for i := range stocks {
		result[i] = &stocks[i]
	}

	return result, nil
}

// UpdateStockAfterSell 卖出后更新股票状态
func (tp *TradeablePool) UpdateStockAfterSell(stockCode string) error {
	db := tp.getDB()
	if db == nil {
		return fmt.Errorf("数据库未初始化")
	}

	var stock data.TradeableStock
	if err := db.Where("stock_code = ?", stockCode).First(&stock).Error; err != nil {
		return fmt.Errorf("股票不存在: %s", stockCode)
	}

	oldStatus := stock.Status
	stock.Status = "SOLD"
	stock.UpdatedAt = time.Now()

	if err := db.Save(&stock).Error; err != nil {
		return fmt.Errorf("更新卖出状态失败: %w", err)
	}

	tp.addLog(stockCode, "SELL", oldStatus, stock.Status, "system", "QuantBot", "自动卖出止盈/止损", nil)
	log.Printf("[TradeablePool] Stock sold: %s", stockCode)

	return nil
}

// UpdateStockPrice 更新股票价格
func (tp *TradeablePool) UpdateStockPrice(stockCode string, newPrice float64) error {
	db := tp.getDB()
	if db == nil {
		return fmt.Errorf("数据库未初始化")
	}

	result := db.Model(&data.TradeableStock{}).
		Where("stock_code = ?", stockCode).
		Updates(map[string]interface{}{
			"current_price": newPrice,
			"updated_at":    time.Now(),
		})

	if result.Error != nil {
		return fmt.Errorf("更新价格失败: %w", result.Error)
	}

	if result.RowsAffected > 0 {
		log.Printf("[TradeablePool] Updated price for %s: %.2f", stockCode, newPrice)
	}

	return nil
}
