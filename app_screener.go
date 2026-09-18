package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	brainhost "github.com/quantpilot/quantpilot/internal/brainhost"
	"github.com/quantpilot/quantpilot/internal/data"
	"github.com/quantpilot/quantpilot/internal/screener"
	"github.com/quantpilot/quantpilot/internal/util"
)

// ScreenStock 执行选股
func (a *App) ScreenStock(strategyID string, market string, maxResults int, minScore float64, customWeightsJSON string) (interface{}, error) {
	if a.screenerService == nil {
		return nil, fmt.Errorf("Screener service not initialized")
	}

	req := screener.ScreeningRequest{
		StrategyID: strategyID,
		Market:     market,
		MaxResults: maxResults,
		MinScore:   minScore,
	}

	// 加载用户画像并传递给选股引擎，启用智能因子组合
	if a.plannerAgent != nil {
		profile, err := a.plannerAgent.GetProfile()
		if err == nil && profile != nil {
			req.InvestorProfile = brainhost.DataProfileFromPort(profile)
			log.Printf("[ScreenStock] 使用用户画像: 风险=%s, 风格=%s, 期限=%s",
				profile.RiskTolerance, profile.InvestmentStyle, profile.InvestmentHorizon)
		}
	}

	// 解析自定义权重
	if customWeightsJSON != "" {
		var weights map[screener.FactorID]float64
		if err := json.Unmarshal([]byte(customWeightsJSON), &weights); err != nil {
			return nil, fmt.Errorf("自定义权重解析失败: %w", err)
		}
		req.CustomWeights = weights
	}

	result, err := a.screenerService.ScreenStock(req)
	if err != nil {
		if a.auditService != nil {
			a.auditService.LogScreening(strategyID, market, 0, false)
		}
		return nil, err
	}

	if a.auditService != nil {
		a.auditService.LogScreening(strategyID, market, len(result.Results), true)
	}

	// 转换为可序列化的格式
	var factorScoresList []interface{}
	for _, r := range result.Results {
		var factorList []interface{}
		for _, fs := range r.FactorScores {
			factorList = append(factorList, map[string]interface{}{
				"factorId":    string(fs.FactorID),
				"factorName":  fs.FactorName,
				"score":       fs.Score,
				"breakdown":   fs.Breakdown,
				"contributor": fs.Contributor,
			})
		}

		reasons := r.Reasons
		if reasons == nil {
			reasons = make([]string, 0)
		}
		warnings := r.Warnings
		if warnings == nil {
			warnings = make([]string, 0)
		}

		factorScoresList = append(factorScoresList, map[string]interface{}{
			"code":         r.Code,
			"name":         r.Name,
			"market":       r.Market,
			"price":        r.Price,
			"changePct":    r.ChangePct,
			"totalScore":   r.TotalScore,
			"ranking":      r.Ranking,
			"reasons":      reasons,
			"warnings":     warnings,
			"factorScores": factorList,
			// AI评分拆解（对标 PanWatch）：1-10 AI Score + 利好因子/风险因子
			"aiScore":       r.AIScore,
			"factorExplain": r.FactorExplain,
			// Pro版扩展 - 收益率和风险指标
			"momentum1m":   r.Momentum1M,
			"momentum3m":   r.Momentum3M,
			"momentum6m":   r.Momentum6M,
			"momentum12m":  r.Momentum12M,
			"volatility":   r.Volatility,
			"amplitude":    r.Amplitude,
			"turnoverRate": r.TurnoverRate,
			"liquidity":    r.Liquidity,
		})
	}

	upgrades := result.Upgrades
	if upgrades == nil {
		upgrades = make([]string, 0)
	}

	// 转换动态权重为可序列化格式
	dynamicWeights := make(map[string]float64)
	for k, v := range result.DynamicWeights {
		dynamicWeights[string(k)] = v
	}

	// 转换因子健康度为可序列化格式
	var factorHealthList []interface{}
	for id, fh := range result.FactorHealth {
		factorHealthList = append(factorHealthList, map[string]interface{}{
			"factorId":     string(id),
			"factorName":   fh.FactorName,
			"ic":           fh.IC,
			"icir":         fh.ICIR,
			"winRate":      fh.WinRate,
			"recentReturn": fh.RecentReturn,
			"status":       fh.Status,
			"description":  fh.Description,
		})
	}

	// 市场状态
	var marketStateMap map[string]interface{}
	if result.MarketState != nil {
		marketStateMap = map[string]interface{}{
			"regime":      result.MarketState.Regime,
			"trendScore":  result.MarketState.TrendScore,
			"volatility":  result.MarketState.Volatility,
			"breadth":     result.MarketState.Breadth,
			"description": result.MarketState.Description,
		}
	}

	return map[string]interface{}{
		"strategyName":    result.StrategyName,
		"totalCount":      result.TotalCount,
		"results":         factorScoresList,
		"generatedAt":     result.GeneratedAt,
		"formula":         result.Formula,        // 策略模板公式
		"dynamicFormula":  result.DynamicFormula, // 实际使用的动态公式
		"tier":            result.Tier,
		"upgrades":        upgrades,
		"dynamicWeights":  dynamicWeights,
		"templateWeights": result.TemplateWeights,
		"factorHealth":    factorHealthList,
		"marketState":     marketStateMap,
		"profileSummary":  result.ProfileSummary,
		"usedSmartEngine": result.UsedSmartEngine,
	}, nil
}

// SubmitScreenResult 提交选股结果到可交易股票池
func (a *App) SubmitScreenResult(strategyID string, market string, maxResults int, minScore float64, submitterID string, submitterName string) (interface{}, error) {
	if a.screenerService == nil || a.tradeablePool == nil {
		return nil, fmt.Errorf("服务未初始化")
	}

	req := screener.ScreeningRequest{
		StrategyID: strategyID,
		Market:     market,
		MaxResults: maxResults,
		MinScore:   minScore,
	}

	// 加载用户画像并传递给选股引擎
	if a.plannerAgent != nil {
		profile, err := a.plannerAgent.GetProfile()
		if err == nil && profile != nil {
			req.InvestorProfile = brainhost.DataProfileFromPort(profile)
		}
	}

	result, err := a.screenerService.ScreenStock(req)
	if err != nil {
		return nil, err
	}

	count, err := a.tradeablePool.SubmitScreenerResult(&result, submitterID, submitterName)
	if err != nil {
		return nil, err
	}

	if a.auditService != nil {
		a.auditService.LogAuditEvent(
			data.AuditEventScreening,
			fmt.Sprintf("提交选股结果到可交易股票池: 策略=%s, 市场=%s, 提交数量=%d", strategyID, market, count),
			"tradeable_pool", strategyID,
			submitterID, submitterName,
			"success",
			map[string]interface{}{
				"strategy_id":       strategyID,
				"market":            market,
				"submitted_count":   count,
				"used_smart_engine": result.UsedSmartEngine,
			},
		)
	}

	return map[string]interface{}{
		"status":          "ok",
		"submitted_count": count,
		"total_results":   len(result.Results),
		"usedSmartEngine": result.UsedSmartEngine,
		"profileSummary":  result.ProfileSummary,
		"dynamicWeights":  result.DynamicWeights,
	}, nil
}

// GetTradeableStocks 获取可交易股票池列表
func (a *App) GetTradeableStocks(status string, market string, selectionSource string, sortBy string, limit int) (interface{}, error) {
	if a.tradeablePool == nil {
		return nil, fmt.Errorf("可交易股票池未初始化")
	}

	stocks, err := a.tradeablePool.GetAllStocksWithFilter(status, market, selectionSource, sortBy, limit)
	if err != nil {
		return nil, err
	}

	var result []interface{}
	for _, s := range stocks {
		result = append(result, map[string]interface{}{
			"id":               s.ID,
			"stockCode":        s.StockCode,
			"stockName":        s.StockName,
			"market":           s.Market,
			"currentPrice":     s.CurrentPrice,
			"targetPrice":      s.TargetPrice,
			"stopLossPrice":    s.StopLossPrice,
			"compositeScore":   s.CompositeScore,
			"factorScoresJSON": s.FactorScoresJSON,
			"selectionReason":  s.SelectionReason,
			"riskWarning":      s.RiskWarning,
			"selectionSource":  s.SelectionSource,
			"strategyID":       s.StrategyID,
			"status":           s.Status,
			"priority":         s.Priority,
			"suggestedWeight":  s.SuggestedWeight,
			"maxPositionPct":   s.MaxPositionPct,
			"minPositionPct":   s.MinPositionPct,
			"orderType":        s.OrderType,
			"submitterID":      s.SubmitterID,
			"submitterName":    s.SubmitterName,
			"submittedAt":      s.SubmittedAt.Format(time.RFC3339),
			"reviewedAt":       formatTimePtr(s.ReviewedAt),
			"reviewerID":       s.ReviewerID,
			"reviewerName":     s.ReviewerName,
			"reviewComment":    s.ReviewComment,
			"approvedQuantity": s.ApprovedQuantity,
			"boughtQuantity":   s.BoughtQuantity,
			"boughtPrice":      s.BoughtPrice,
			"boughtAt":         formatTimePtr(s.BoughtAt),
			"targetDate":       formatTimePtr(s.TargetDate),
			"expireAt":         formatTimePtr(s.ExpireAt),
			"tags":             s.Tags,
			"createdAt":        s.CreatedAt.Format(time.RFC3339),
		})
	}

	return map[string]interface{}{
		"stocks":     result,
		"totalCount": len(result),
	}, nil
}

// GetPendingStocks 获取待审核股票
func (a *App) GetPendingStocks() (interface{}, error) {
	if a.tradeablePool == nil {
		return nil, fmt.Errorf("可交易股票池未初始化")
	}

	stocks, err := a.tradeablePool.GetPendingStocks()
	if err != nil {
		return nil, err
	}

	var result []interface{}
	for _, s := range stocks {
		result = append(result, map[string]interface{}{
			"id":              s.ID,
			"stockCode":       s.StockCode,
			"stockName":       s.StockName,
			"market":          s.Market,
			"currentPrice":    s.CurrentPrice,
			"compositeScore":  s.CompositeScore,
			"selectionReason": s.SelectionReason,
			"riskWarning":     s.RiskWarning,
			"priority":        s.Priority,
			"submittedAt":     s.SubmittedAt.Format(time.RFC3339),
			"submitterName":   s.SubmitterName,
		})
	}

	return map[string]interface{}{
		"stocks": result,
		"count":  len(result),
	}, nil
}

// GetApprovedStocks 获取已批准股票（可买卖）
func (a *App) GetApprovedStocks() (interface{}, error) {
	if a.tradeablePool == nil {
		return nil, fmt.Errorf("可交易股票池未初始化")
	}

	stocks, err := a.tradeablePool.GetApprovedStocks()
	if err != nil {
		return nil, err
	}

	var result []interface{}
	for _, s := range stocks {
		result = append(result, map[string]interface{}{
			"id":                s.ID,
			"stockCode":         s.StockCode,
			"stockName":         s.StockName,
			"market":            s.Market,
			"currentPrice":      s.CurrentPrice,
			"compositeScore":    s.CompositeScore,
			"priority":          s.Priority,
			"approvedQuantity":  s.ApprovedQuantity,
			"boughtQuantity":    s.BoughtQuantity,
			"remainingQuantity": s.ApprovedQuantity - s.BoughtQuantity,
			"reviewedAt":        formatTimePtr(s.ReviewedAt),
			"reviewerName":      s.ReviewerName,
		})
	}

	return map[string]interface{}{
		"stocks": result,
		"count":  len(result),
	}, nil
}

// GetBuyableStocks 获取可买入股票
func (a *App) GetBuyableStocks() (interface{}, error) {
	if a.tradeablePool == nil {
		return nil, fmt.Errorf("可交易股票池未初始化")
	}

	stocks, err := a.tradeablePool.GetBuyableStocks()
	if err != nil {
		return nil, err
	}

	var result []interface{}
	for _, s := range stocks {
		result = append(result, map[string]interface{}{
			"id":                s.ID,
			"stockCode":         s.StockCode,
			"stockName":         s.StockName,
			"market":            s.Market,
			"currentPrice":      s.CurrentPrice,
			"compositeScore":    s.CompositeScore,
			"priority":          s.Priority,
			"approvedQuantity":  s.ApprovedQuantity,
			"boughtQuantity":    s.BoughtQuantity,
			"remainingQuantity": s.ApprovedQuantity - s.BoughtQuantity,
			"targetPrice":       s.TargetPrice,
			"stopLossPrice":     s.StopLossPrice,
		})
	}

	return map[string]interface{}{
		"stocks": result,
		"count":  len(result),
	}, nil
}

// ApproveStock 审核批准股票
func (a *App) ApproveStock(stockCode string, reviewerID string, reviewerName string, comment string, approvedQuantity int) (interface{}, error) {
	if a.tradeablePool == nil {
		return nil, fmt.Errorf("可交易股票池未初始化")
	}

	err := a.tradeablePool.ApproveStock(stockCode, reviewerID, reviewerName, comment, approvedQuantity)
	if err != nil {
		return nil, err
	}

	if a.auditService != nil {
		a.auditService.LogAuditEvent(
			data.AuditEventAIAnalysis,
			fmt.Sprintf("批准可交易股票: %s, 数量=%d", stockCode, approvedQuantity),
			"tradeable_pool", stockCode,
			reviewerID, reviewerName,
			"success",
			map[string]interface{}{
				"stock_code":        stockCode,
				"approved_quantity": approvedQuantity,
				"comment":           comment,
			},
		)
	}

	return map[string]interface{}{
		"status": "ok",
	}, nil
}

// RejectStock 审核拒绝股票
func (a *App) RejectStock(stockCode string, reviewerID string, reviewerName string, comment string) (interface{}, error) {
	if a.tradeablePool == nil {
		return nil, fmt.Errorf("可交易股票池未初始化")
	}

	err := a.tradeablePool.RejectStock(stockCode, reviewerID, reviewerName, comment)
	if err != nil {
		return nil, err
	}

	if a.auditService != nil {
		a.auditService.LogAuditEvent(
			data.AuditEventAIAnalysis,
			fmt.Sprintf("拒绝可交易股票: %s", stockCode),
			"tradeable_pool", stockCode,
			reviewerID, reviewerName,
			"success",
			map[string]interface{}{
				"stock_code": stockCode,
				"comment":    comment,
			},
		)
	}

	return map[string]interface{}{
		"status": "ok",
	}, nil
}

// BatchApproveStocks 批量批准股票
func (a *App) BatchApproveStocks(stockCodesJSON string, reviewerID string, reviewerName string) (interface{}, error) {
	if a.tradeablePool == nil {
		return nil, fmt.Errorf("可交易股票池未初始化")
	}

	var stockCodes []string
	if err := json.Unmarshal([]byte(stockCodesJSON), &stockCodes); err != nil {
		return nil, fmt.Errorf("股票代码列表解析失败: %w", err)
	}

	count, err := a.tradeablePool.BatchApprove(stockCodes, reviewerID, reviewerName)
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"status":         "ok",
		"approved_count": count,
	}, nil
}

// BatchRejectStocks 批量拒绝股票
func (a *App) BatchRejectStocks(stockCodesJSON string, reviewerID string, reviewerName string) (interface{}, error) {
	if a.tradeablePool == nil {
		return nil, fmt.Errorf("可交易股票池未初始化")
	}

	var stockCodes []string
	if err := json.Unmarshal([]byte(stockCodesJSON), &stockCodes); err != nil {
		return nil, fmt.Errorf("股票代码列表解析失败: %w", err)
	}

	count, err := a.tradeablePool.BatchReject(stockCodes, reviewerID, reviewerName)
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"status":         "ok",
		"rejected_count": count,
	}, nil
}

// DeleteTradeableStock 删除可交易股票
func (a *App) DeleteTradeableStock(stockCode string) (interface{}, error) {
	if a.tradeablePool == nil {
		return nil, fmt.Errorf("可交易股票池未初始化")
	}

	err := a.tradeablePool.DeleteStock(stockCode)
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"status": "ok",
	}, nil
}

// GetTradeableStockLogs 获取股票操作日志
func (a *App) GetTradeableStockLogs(stockCode string) (interface{}, error) {
	if a.tradeablePool == nil {
		return nil, fmt.Errorf("可交易股票池未初始化")
	}

	logs, err := a.tradeablePool.GetStockLogs(stockCode)
	if err != nil {
		return nil, err
	}

	var result []interface{}
	for _, l := range logs {
		result = append(result, map[string]interface{}{
			"id":           l.ID,
			"stockCode":    l.StockCode,
			"action":       l.Action,
			"fromStatus":   l.FromStatus,
			"toStatus":     l.ToStatus,
			"operatorID":   l.OperatorID,
			"operatorName": l.OperatorName,
			"comment":      l.Comment,
			"detailsJSON":  l.DetailsJSON,
			"createdAt":    l.CreatedAt.Format(time.RFC3339),
		})
	}

	return map[string]interface{}{
		"logs":  result,
		"count": len(result),
	}, nil
}

// GetTradeablePoolSummary 获取股票池统计信息
func (a *App) GetTradeablePoolSummary() (interface{}, error) {
	if a.tradeablePool == nil {
		return nil, fmt.Errorf("可交易股票池未初始化")
	}

	return a.tradeablePool.GetPoolSummary(), nil
}

// BuyFromTradeablePool 从可交易股票池买入股票
func (a *App) BuyFromTradeablePool(stockCode string, quantity int) (interface{}, error) {
	if a.tradeablePool == nil || a.portfolioEngine == nil {
		return nil, fmt.Errorf("服务未初始化")
	}

	// 获取股票信息
	stock, err := a.tradeablePool.GetStock(stockCode)
	if err != nil {
		return nil, err
	}

	if stock.Status != "APPROVED" {
		return nil, fmt.Errorf("股票状态不是APPROVED，无法买入")
	}

	if stock.BoughtQuantity+quantity > stock.ApprovedQuantity {
		return nil, fmt.Errorf("买入数量超过批准数量")
	}

	// 执行买入
	trade, err := a.portfolioEngine.Buy(stockCode, stock.StockName, stock.Market, quantity, stock.CurrentPrice, "从可交易股票池买入", "")
	if err != nil {
		return nil, fmt.Errorf("买入失败: %w", err)
	}

	// 更新股票池状态
	if err := a.tradeablePool.UpdateStockAfterBuy(stockCode, quantity, stock.CurrentPrice); err != nil {
		log.Printf("[App] Failed to update tradeable stock after buy: %v", err)
	}

	if a.auditService != nil {
		a.auditService.LogAuditEvent(
			data.AuditEventOrder,
			fmt.Sprintf("从可交易股票池买入: %s, 数量=%d", stockCode, quantity),
			"trade", stockCode,
			"system", "QuantBot",
			"success",
			map[string]interface{}{
				"stock_code": stockCode,
				"quantity":   quantity,
				"price":      stock.CurrentPrice,
				"trade_id":   trade.TradeID,
			},
		)
	}

	return map[string]interface{}{
		"trade_id":   trade.TradeID,
		"stock_code": stockCode,
		"quantity":   quantity,
		"price":      stock.CurrentPrice,
		"net_amount": trade.NetAmount,
	}, nil
}

// TradeableStockQuickSubmit 快速添加股票到可交易池（手动）
func (a *App) TradeableStockQuickSubmit(stockCode string, stockName string, market string, currentPrice float64, compositeScore float64, submitterID string, submitterName string) (interface{}, error) {
	if a.tradeablePool == nil {
		return nil, fmt.Errorf("可交易股票池未初始化")
	}

	stock := &data.TradeableStock{
		StockCode:       stockCode,
		StockName:       stockName,
		Market:          market,
		CurrentPrice:    currentPrice,
		CompositeScore:  compositeScore,
		SelectionSource: "MANUAL",
		Status:          "PENDING",
		Priority:        3,
		SuggestedWeight: 0.05,
		MaxPositionPct:  0.10,
		MinPositionPct:  0.01,
		OrderType:       "LIMIT",
		SubmitterID:     submitterID,
		SubmitterName:   submitterName,
		SubmittedAt:     time.Now(),
		SelectionReason: "手动添加",
		Tags:            "manual",
	}

	err := a.tradeablePool.SubmitStock(stock)
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"status":     "ok",
		"stock_code": stockCode,
	}, nil
}

func formatTimePtr(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format(time.RFC3339)
}

// ProcessTradeablePool 实现 TradeablePoolProcessor 接口
// 自动处理可交易股票池的买入和卖出
func (a *App) ProcessTradeablePool(ctx context.Context) error {
	if a.cioEngine == nil || a.portfolioEngine == nil {
		return fmt.Errorf("服务未初始化")
	}

	// 紧急停止门控：停止状态下跳过可交易股票池的止损/审核/自动买卖，
	// 确保"紧急停止 → 所有智能体停止工作"覆盖 AutoScheduler 每5分钟触发的这条路径。
	if a.policyEngine != nil && a.policyEngine.IsEmergencyStopped() {
		log.Printf("[TradeablePool] 紧急停止中，跳过可交易股票池处理")
		return nil
	}

	log.Printf("[TradeablePool] 开始处理可交易股票池...")

	// 0. 盘中止损执行（先于买卖：刷新价格并检查所有持仓止损，触发则自动卖出）
	//    硬止损属风险控制，不受交易时间纪律限制，任何时候都执行。
	stopLossCount := a.cioEngine.ExecuteStopLossIfTriggered(ctx)
	if stopLossCount > 0 {
		log.Printf("[TradeablePool] 盘中止损执行: %d 只持仓触发止损并卖出", stopLossCount)
	}

	// 交易时间纪律：与仓位管理工具共用同一套时段规则
	// 10:00前只观察不操作 / 10:00-11:20建底仓 / 13:30-14:20优化做T / 14:40后只止盈减仓
	tradingWin := util.TradingWindowAt(util.DefaultTradingWindows(), time.Now())

	// 1. 先审核待处理股票
	_, err := a.cioEngine.ReviewPendingTradeableStocks("cio_auto", "CIO自动审核")
	if err != nil {
		log.Printf("[TradeablePool] 审核待处理股票失败: %v", err)
	}

	// 2. 自动卖出检查（先卖出止盈止损）—— 受时段纪律限制（硬止损已在步骤0无条件执行）
	if tradingWin.AllowSell {
		_, err = a.cioEngine.AutoSellFromTradeablePool(ctx)
		if err != nil {
			log.Printf("[TradeablePool] 自动卖出检查失败: %v", err)
		}
	} else {
		log.Printf("[TradeablePool] 当前时段[%s]禁止减仓/止盈自动卖出，跳过: %s", tradingWin.Name, tradingWin.Note)
	}

	// 3. 自动买入（使用部分可用资金买入可交易股票）—— 受时段纪律限制
	portfolioSnapshot := a.portfolioEngine.GetSnapshot()
	availableCash := portfolioSnapshot.Cash * 0.1 // 使用10%的现金买入

	if tradingWin.AllowBuy && availableCash > 0 {
		_, err = a.cioEngine.AutoBuyFromTradeablePool(ctx, availableCash)
		if err != nil {
			log.Printf("[TradeablePool] 自动买入失败: %v", err)
		}
	} else if !tradingWin.AllowBuy {
		log.Printf("[TradeablePool] 当前时段[%s]禁止自动买入，跳过: %s", tradingWin.Name, tradingWin.Note)
	}

	log.Printf("[TradeablePool] 可交易股票池处理完成（当前时段[%s]）", tradingWin.Name)
	return nil
}

// MonitorIntradayInvestmentPlan 实现 CIOIntradayPlanProcessor 接口
// 交易时段内由 AutoScheduler 周期调用，让首席投资官(CIO)实时监控投资方案。
func (a *App) MonitorIntradayInvestmentPlan(ctx context.Context) error {
	if a.cioEngine == nil {
		return fmt.Errorf("服务未初始化")
	}
	return a.cioEngine.MonitorIntradayInvestmentPlan(ctx)
}

// GenerateFactorHealthReport 生成因子健康度报告（Pro功能）
func (a *App) GenerateFactorHealthReport() (interface{}, error) {
	if a.screenerService == nil {
		return nil, fmt.Errorf("Screener service not initialized")
	}

	return a.screenerService.GenerateFactorHealthReport()
}

// CalculateStructureRisk 计算结构风险（Pro功能）
func (a *App) CalculateStructureRisk(stockCodes []string) (interface{}, error) {
	if a.screenerService == nil {
		return nil, fmt.Errorf("Screener service not initialized")
	}

	return a.screenerService.CalculateStructureRisk(stockCodes)
}
