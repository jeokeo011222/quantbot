package cio

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/quantpilot/quantpilot/internal/broker"
	"github.com/quantpilot/quantpilot/internal/data"
	"github.com/quantpilot/quantpilot/internal/orderbook"
	"github.com/quantpilot/quantpilot/internal/policy"
	"github.com/quantpilot/quantpilot/internal/portfolio"
	"github.com/quantpilot/quantpilot/internal/risk"
	"github.com/quantpilot/quantpilot/internal/screener"
	"github.com/quantpilot/quantpilot/internal/tradeapproval"
)

// TradeablePoolHandler 可交易股票池处理器
type TradeablePoolHandler struct {
	cioEngine     *CIOEngine
	tradeablePool *screener.TradeablePool
	portfolio     *portfolio.Engine
	policyEngine  *policy.PolicyEngine
	riskEngine    *risk.RiskEngine
}

// NewTradeablePoolHandler 创建可交易股票池处理器
func NewTradeablePoolHandler(
	cioEngine *CIOEngine,
	tradeablePool *screener.TradeablePool,
	portfolio *portfolio.Engine,
	policyEngine *policy.PolicyEngine,
	riskEngine *risk.RiskEngine,
) *TradeablePoolHandler {
	return &TradeablePoolHandler{
		cioEngine:     cioEngine,
		tradeablePool: tradeablePool,
		portfolio:     portfolio,
		policyEngine:  policyEngine,
		riskEngine:    riskEngine,
	}
}

// AutoBuyFromTradeablePool 自动从可交易股票池买入
func (h *TradeablePoolHandler) AutoBuyFromTradeablePool(ctx context.Context, maxBuyAmount float64) ([]*data.TradeableStock, error) {
	if h.tradeablePool == nil {
		return nil, fmt.Errorf("可交易股票池未初始化")
	}

	// 检查是否紧急停止
	if h.policyEngine != nil && h.policyEngine.IsEmergencyStopped() {
		log.Println("[TradeablePool] 紧急停止中，跳过自动买入")
		return nil, nil
	}

	// 获取可买入股票
	buyableStocks, err := h.tradeablePool.GetBuyableStocks()
	if err != nil {
		return nil, fmt.Errorf("获取可买入股票失败: %w", err)
	}

	if len(buyableStocks) == 0 {
		log.Println("[TradeablePool] 没有可买入的股票")
		return nil, nil
	}

	log.Printf("[TradeablePool] 发现 %d 只可买入股票", len(buyableStocks))

	// 盘口过滤：批量获取可买入标的的实时盘口，剔除「涨停封死买不进」「跌停封死/跌停板(巨量跌停)不接飞刀」标的，
	// 避免对必然成交不了的股票空转（对应百花医药式巨量开盘跌停场景）。
	obBlocked := make(map[string]string)
	obCodes := make([]string, 0, len(buyableStocks))
	for _, st := range buyableStocks {
		obCodes = append(obCodes, st.Market+st.StockCode)
	}
	if snaps, _ := data.FetchRealtimeStockSnapshots(obCodes); len(snaps) > 0 {
		for _, sn := range snaps {
			ob := orderbook.ClassifySnapshot(sn)
			if ob.State == orderbook.StateSealedLimitUp ||
				ob.State == orderbook.StateSealedLimitDown ||
				ob.State == orderbook.StateOpenedLimitDown {
				obBlocked[strings.ToLower(sn.Market+sn.Code)] = ob.Reason
				obBlocked[strings.ToLower(sn.Code)] = ob.Reason
			}
		}
	}

	var boughtStocks []*data.TradeableStock
	var totalBuyAmount float64

	for _, stock := range buyableStocks {
		// 盘口不可买：涨停封死买不进 / 跌停封死、跌停板不接飞刀 → 跳过
		if reason, blocked := obBlocked[strings.ToLower(stock.Market+stock.StockCode)]; blocked {
			log.Printf("[TradeablePool] 盘口不可买，跳过 %s: %s", stock.StockCode, reason)
			continue
		}

		if stock.BoughtQuantity >= stock.ApprovedQuantity {
			continue
		}

		remainingQty := stock.ApprovedQuantity - stock.BoughtQuantity
		// A股按100股（1手）整数倍交易，向下取整避免非整手导致买入失败
		if remainingQty%100 != 0 {
			remainingQty = remainingQty / 100 * 100
		}
		if remainingQty <= 0 {
			continue
		}
		buyAmount := float64(remainingQty) * stock.CurrentPrice

		if totalBuyAmount+buyAmount > maxBuyAmount {
			log.Printf("[TradeablePool] 超出最大买入金额限制，跳过 %s", stock.StockCode)
			continue
		}

		// 风控检查
		if err := h.checkRiskBeforeBuy(stock); err != nil {
			log.Printf("[TradeablePool] 风控检查未通过 %s: %v", stock.StockCode, err)
			continue
		}

		// 盘中自动买卖实盘开关：QMT 实盘模式下真实下发券商（成交回报由 onBrokerFill 回填账本）。
		if h.cioEngine.liveAutoExecEnabled() {
			orderID, err := h.submitLive(broker.SideBuy, stock, remainingQty, stock.CurrentPrice, "从可交易股票池自动买入")
			if err != nil {
				log.Printf("[TradeablePool] 实盘自动买入失败 %s: %v", stock.StockCode, err)
				h.cioEngine.addActivity("CIO", "AUTO_BUY_FAIL", fmt.Sprintf("实盘自动买入失败: %s, 原因: %v", stock.StockCode, err), nil)
				continue
			}
			totalBuyAmount += float64(remainingQty) * stock.CurrentPrice
			if err := h.tradeablePool.UpdateStockAfterBuy(stock.StockCode, remainingQty, stock.CurrentPrice); err != nil {
				log.Printf("[TradeablePool] 更新股票池状态失败: %v", err)
			}
			boughtStocks = append(boughtStocks, stock)
			h.cioEngine.addActivity("CIO", "AUTO_BUY_LIVE", fmt.Sprintf("实盘自动买入已下发: %s, 数量=%d, 金额=%.2f, 委托号=%s", stock.StockCode, remainingQty, float64(remainingQty)*stock.CurrentPrice, orderID), nil)
			log.Printf("[TradeablePool] 实盘自动买入已下发 %s, 数量=%d, 委托号=%s", stock.StockCode, remainingQty, orderID)
			continue
		}

		// 模拟接口模式：交易需用户手动确认
		var approvalResult tradeapproval.ApprovalResult
		var pendingOrder *tradeapproval.PendingTrade
		if h.cioEngine.approval != nil {
			var aErr error
			approvalResult, pendingOrder, aErr = h.cioEngine.approval.RequestApproval("BUY", stock.StockCode, stock.StockName, stock.Market, remainingQty, stock.CurrentPrice, "从可交易股票池自动买入", "")
			if aErr != nil {
				log.Printf("[TradeablePool] 买入确认失败 %s: %v", stock.StockCode, aErr)
				continue
			}
			if approvalResult == tradeapproval.ResultRejected {
				log.Printf("[TradeablePool] 买入被用户拒绝 %s", stock.StockCode)
				h.cioEngine.addActivity("CIO", "AUTO_BUY_REJECTED", fmt.Sprintf("买入被用户拒绝: %s", stock.StockCode), nil)
				continue
			}
			if approvalResult == tradeapproval.ResultPending {
				// 用户未及时确认：订单排队中，不判定失败，等待用户补确认（批准后将补执行）
				log.Printf("[TradeablePool] 买入排队等待用户确认 %s", stock.StockCode)
				h.cioEngine.addActivity("CIO", "AUTO_BUY_PENDING", fmt.Sprintf("买入排队等待用户确认: %s", stock.StockCode), nil)
				continue
			}
			if approvalResult == tradeapproval.ResultFailed {
				// 15:00 收盘仍未获确认：买入失败
				log.Printf("[TradeablePool] 买入失败(收盘未获确认) %s", stock.StockCode)
				h.cioEngine.addActivity("CIO", "AUTO_BUY_FAILED", fmt.Sprintf("买入失败: %s 截至收盘未获用户确认", stock.StockCode), nil)
				continue
			}
			// ResultApproved：继续执行买入
		}

		// 买入
		trade, err := h.portfolio.Buy(
			stock.StockCode,
			stock.StockName,
			stock.Market,
			remainingQty,
			stock.CurrentPrice,
			"从可交易股票池自动买入",
			"",
		)
		if err != nil {
			// 批准后实际执行失败：逆转已按成交标记的订单并释放占用的买入资金
			if pendingOrder != nil {
				h.cioEngine.approval.CancelFilled(pendingOrder)
			}
			log.Printf("[TradeablePool] 买入失败 %s: %v", stock.StockCode, err)
			h.cioEngine.addActivity("CIO", "AUTO_BUY_FAIL", fmt.Sprintf("自动买入失败: %s, 原因: %v", stock.StockCode, err), nil)
			continue
		}

		// 更新股票池状态
		if err := h.tradeablePool.UpdateStockAfterBuy(stock.StockCode, remainingQty, stock.CurrentPrice); err != nil {
			log.Printf("[TradeablePool] 更新股票池状态失败: %v", err)
		}

		boughtStocks = append(boughtStocks, stock)
		totalBuyAmount += trade.NetAmount

		h.cioEngine.addActivity("CIO", "AUTO_BUY", fmt.Sprintf("自动买入: %s, 数量=%d, 金额=%.2f", stock.StockCode, remainingQty, trade.NetAmount), nil)

		log.Printf("[TradeablePool] 成功买入 %s, 数量=%d, 金额=%.2f", stock.StockCode, remainingQty, trade.NetAmount)

		// 检查是否达到最大买入金额
		if totalBuyAmount >= maxBuyAmount {
			break
		}
	}

	return boughtStocks, nil
}

// checkRiskBeforeBuy 买入前风控检查
func (h *TradeablePoolHandler) checkRiskBeforeBuy(stock *data.TradeableStock) error {
	if h.riskEngine == nil {
		return nil
	}

	// 检查是否超过单只股票最大仓位
	if stock.MaxPositionPct > 0 {
		// 检查是否已持有该股票
		positions := h.portfolio.GetPositions()
		if posList, ok := positions["positions"].([]map[string]interface{}); ok {
			for _, pos := range posList {
				if pos["code"] == stock.StockCode {
					// 如果已持有该股，跳过避免重复买入
					return fmt.Errorf("已持有该股票")
				}
			}
		}
	}

	// 检查止损价
	if stock.StopLossPrice > 0 && stock.CurrentPrice < stock.StopLossPrice {
		return fmt.Errorf("当前价格低于止损价")
	}

	return nil
}

// submitLive 盘中自动买卖真实下发 QMT 券商。符号由 Market+StockCode 归一化为内部 sh/sz/bj 格式后，
// 经 CIOEngine 注入的实盘桥下单；成交回报由应用层 onBrokerFill 异步回填账本，此处不做 PORTFOLIO 记账。
func (h *TradeablePoolHandler) submitLive(side broker.Side, stock *data.TradeableStock, qty int, price float64, reason string) (string, error) {
	lb := h.cioEngine.getLiveBroker()
	if lb == nil || lb.Mode() != broker.ModeLive || !lb.IsLive() {
		return "", fmt.Errorf("实盘桥未就绪")
	}
	symbol := strings.ToLower(strings.TrimSpace(stock.Market)) + stock.StockCode
	return lb.SubmitOrder(broker.Order{
		Symbol:    symbol,
		StockName: stock.StockName,
		Side:      side,
		Quantity:  qty,
		Price:     price,
		Reason:    reason,
	})
}

// AutoSellCheck 自动卖出检查（检查止盈止损）
func (h *TradeablePoolHandler) AutoSellCheck(ctx context.Context) ([]*data.TradeableStock, error) {
	if h.tradeablePool == nil {
		return nil, fmt.Errorf("可交易股票池未初始化")
	}

	// 获取已买入股票
	boughtStocks, err := h.tradeablePool.GetBoughtStocks()
	if err != nil {
		return nil, fmt.Errorf("获取已买入股票失败: %w", err)
	}

	if len(boughtStocks) == 0 {
		return nil, nil
	}

	var sellStocks []*data.TradeableStock

	for _, stock := range boughtStocks {
		shouldSell := false
		sellReason := ""

		// 检查目标价（止盈）
		if stock.TargetPrice > 0 && stock.CurrentPrice >= stock.TargetPrice {
			shouldSell = true
			sellReason = "达到目标价止盈"
		}

		// 检查止损价
		if stock.StopLossPrice > 0 && stock.CurrentPrice <= stock.StopLossPrice {
			shouldSell = true
			sellReason = "触及止损价"
		}

		// 检查到期时间
		if stock.ExpireAt != nil && time.Now().After(*stock.ExpireAt) {
			shouldSell = true
			sellReason = "到期卖出"
		}

		if shouldSell && stock.BoughtQuantity > 0 {
			// 盘中自动买卖实盘开关：QMT 实盘模式下真实下发券商（成交回报由 onBrokerFill 回填账本）。
			if h.cioEngine.liveAutoExecEnabled() {
				orderID, err := h.submitLive(broker.SideSell, stock, stock.BoughtQuantity, stock.CurrentPrice, fmt.Sprintf("可交易股票池自动卖出: %s", sellReason))
				if err != nil {
					log.Printf("[TradeablePool] 实盘自动卖出失败 %s: %v", stock.StockCode, err)
					h.cioEngine.addActivity("CIO", "AUTO_SELL_FAIL", fmt.Sprintf("实盘自动卖出失败: %s, 原因: %v", stock.StockCode, err), nil)
					continue
				}
				if err := h.tradeablePool.UpdateStockAfterSell(stock.StockCode); err != nil {
					log.Printf("[TradeablePool] 更新股票池状态失败: %v", err)
				}
				sellStocks = append(sellStocks, stock)
				h.cioEngine.addActivity("CIO", "AUTO_SELL_LIVE", fmt.Sprintf("实盘自动卖出已下发: %s, 原因=%s, 数量=%d, 委托号=%s", stock.StockCode, sellReason, stock.BoughtQuantity, orderID), nil)
				log.Printf("[TradeablePool] 实盘自动卖出已下发 %s, 原因=%s, 数量=%d, 委托号=%s", stock.StockCode, sellReason, stock.BoughtQuantity, orderID)
				continue
			}

			// 模拟接口模式：交易需用户手动确认
			var approvalResult tradeapproval.ApprovalResult
			var pendingOrder *tradeapproval.PendingTrade
			if h.cioEngine.approval != nil {
				var aErr error
				approvalResult, pendingOrder, aErr = h.cioEngine.approval.RequestApproval("SELL", stock.StockCode, stock.StockName, stock.Market, stock.BoughtQuantity, stock.CurrentPrice, fmt.Sprintf("可交易股票池自动卖出: %s", sellReason), "")
				if aErr != nil {
					log.Printf("[TradeablePool] 卖出确认失败 %s: %v", stock.StockCode, aErr)
					continue
				}
				if approvalResult == tradeapproval.ResultRejected {
					log.Printf("[TradeablePool] 卖出被用户拒绝 %s", stock.StockCode)
					h.cioEngine.addActivity("CIO", "AUTO_SELL_REJECTED", fmt.Sprintf("卖出被用户拒绝: %s", stock.StockCode), nil)
					continue
				}
				if approvalResult == tradeapproval.ResultPending {
					// 用户未及时确认：卖出排队中，不判定失败，等待用户补确认（批准后将补执行）
					log.Printf("[TradeablePool] 卖出排队等待用户确认 %s", stock.StockCode)
					h.cioEngine.addActivity("CIO", "AUTO_SELL_PENDING", fmt.Sprintf("卖出排队等待用户确认: %s", stock.StockCode), nil)
					continue
				}
				if approvalResult == tradeapproval.ResultFailed {
					// 15:00 收盘仍未获确认：卖出失败
					log.Printf("[TradeablePool] 卖出失败(收盘未获确认) %s", stock.StockCode)
					h.cioEngine.addActivity("CIO", "AUTO_SELL_FAILED", fmt.Sprintf("卖出失败: %s 截至收盘未获用户确认", stock.StockCode), nil)
					continue
				}
				// ResultApproved：继续执行卖出
			}

			// 执行卖出
			trade, err := h.portfolio.Sell(
				stock.StockCode,
				stock.BoughtQuantity,
				stock.CurrentPrice,
				fmt.Sprintf("可交易股票池自动卖出: %s", sellReason),
				"",
			)
			if err != nil {
				if pendingOrder != nil {
					h.cioEngine.approval.CancelFilled(pendingOrder)
				}
				log.Printf("[TradeablePool] 卖出失败 %s: %v", stock.StockCode, err)
				continue
			}

			// 更新股票池状态
			if err := h.tradeablePool.UpdateStockAfterSell(stock.StockCode); err != nil {
				log.Printf("[TradeablePool] 更新股票池状态失败: %v", err)
			}

			sellStocks = append(sellStocks, stock)
			h.cioEngine.addActivity("CIO", "AUTO_SELL", fmt.Sprintf("自动卖出: %s, 原因=%s, 数量=%d, 金额=%.2f", stock.StockCode, sellReason, stock.BoughtQuantity, trade.NetAmount), nil)

			log.Printf("[TradeablePool] 成功卖出 %s, 原因=%s", stock.StockCode, sellReason)
		}
	}

	return sellStocks, nil
}

// ReviewPendingStocks 审核待处理股票
func (h *TradeablePoolHandler) ReviewPendingStocks(reviewerID, reviewerName string) (*screener.PoolSummary, error) {
	if h.tradeablePool == nil {
		return nil, fmt.Errorf("可交易股票池未初始化")
	}

	pendingStocks, err := h.tradeablePool.GetPendingStocks()
	if err != nil {
		return nil, err
	}

	if len(pendingStocks) == 0 {
		return nil, nil
	}

	log.Printf("[TradeablePool] 审核 %d 只待处理股票", len(pendingStocks))

	approvedCount := 0
	rejectedCount := 0

	for _, stock := range pendingStocks {
		// CIO审核逻辑：根据评分和风控决定是否批准
		if stock.CompositeScore >= 60 {
			// 高评分股票批准
			quantity := 1000 // 默认1000股
			if stock.Priority <= 2 {
				quantity = 2000 // 高优先级更多数量
			}
			if err := h.tradeablePool.ApproveStock(stock.StockCode, reviewerID, reviewerName, "CIO自动审核批准", quantity); err != nil {
				log.Printf("[TradeablePool] 批准失败 %s: %v", stock.StockCode, err)
			} else {
				approvedCount++
				h.cioEngine.addActivity("CIO", "APPROVE_STOCK", fmt.Sprintf("批准股票: %s, 数量=%d", stock.StockCode, quantity), nil)
			}
		} else if stock.CompositeScore >= 40 {
			// 中等评分，随机批准或拒绝
			if stock.Priority <= 3 {
				quantity := 500
				if err := h.tradeablePool.ApproveStock(stock.StockCode, reviewerID, reviewerName, "CIO审核批准-中等评分", quantity); err != nil {
					log.Printf("[TradeablePool] 批准失败 %s: %v", stock.StockCode, err)
				} else {
					approvedCount++
				}
			} else {
				if err := h.tradeablePool.RejectStock(stock.StockCode, reviewerID, reviewerName, "评分较低"); err != nil {
					log.Printf("[TradeablePool] 拒绝失败 %s: %v", stock.StockCode, err)
				} else {
					rejectedCount++
				}
			}
		} else {
			// 低评分拒绝
			if err := h.tradeablePool.RejectStock(stock.StockCode, reviewerID, reviewerName, "评分过低"); err != nil {
				log.Printf("[TradeablePool] 拒绝失败 %s: %v", stock.StockCode, err)
			} else {
				rejectedCount++
			}
		}
	}

	log.Printf("[TradeablePool] 审核完成: 批准=%d, 拒绝=%d", approvedCount, rejectedCount)

	return h.tradeablePool.GetPoolSummary(), nil
}

// UpdateTradeableStockPrices 更新可交易股票池中股票价格
func (h *TradeablePoolHandler) UpdateTradeableStockPrices(prices map[string]float64) error {
	if h.tradeablePool == nil {
		return fmt.Errorf("可交易股票池未初始化")
	}

	for code, price := range prices {
		if err := h.tradeablePool.UpdateStockPrice(code, price); err != nil {
			log.Printf("[TradeablePool] 更新价格失败 %s: %v", code, err)
		}
	}

	return nil
}

// GetTradeablePoolStatus 获取可交易股票池状态
func (h *TradeablePoolHandler) GetTradeablePoolStatus() (*screener.PoolSummary, error) {
	if h.tradeablePool == nil {
		return nil, fmt.Errorf("可交易股票池未初始化")
	}

	return h.tradeablePool.GetPoolSummary(), nil
}
