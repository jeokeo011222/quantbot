package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/quantpilot/quantpilot/internal/broker"
	"github.com/quantpilot/quantpilot/internal/data"
	"github.com/quantpilot/quantpilot/internal/thssdk"
	"github.com/quantpilot/quantpilot/internal/tools"
	"github.com/quantpilot/quantpilot/internal/tradeapproval"
	"github.com/quantpilot/quantpilot/internal/util"
)

// EmergencyStop 紧急停止交易（所有智能体停止工作）
func (a *App) EmergencyStop() error {
	if a.cioEngine == nil {
		return fmt.Errorf("CIO Engine not initialized")
	}

	a.cioEngine.EmergencyStop()

	if a.auditService != nil {
		a.auditService.LogAuditEvent(
			data.AuditEventOrder,
			"紧急停止交易",
			"system",
			"emergency_stop",
			"user",
			"user",
			"success",
			map[string]interface{}{
				"timestamp": time.Now().Format(time.RFC3339),
				"reason":    "用户手动触发",
			},
		)
	}

	return nil
}

// ResumeTrading 恢复交易（所有智能体恢复工作）
func (a *App) ResumeTrading() error {
	if a.cioEngine == nil {
		return fmt.Errorf("CIO Engine not initialized")
	}

	a.cioEngine.ResumeTrading()

	if a.auditService != nil {
		a.auditService.LogAuditEvent(
			data.AuditEventOrder,
			"恢复交易",
			"system",
			"resume_trading",
			"user",
			"user",
			"success",
			map[string]interface{}{
				"timestamp": time.Now().Format(time.RFC3339),
			},
		)
	}

	return nil
}

// PauseBuilding 强制暂停建仓：仅禁止一切买入成交，卖出/离场/止损不受影响。
// 比紧急停止更轻量，适合"暂停建仓但保留卖出离场"的业务场景。
func (a *App) PauseBuilding() error {
	if a.policyEngine == nil {
		return fmt.Errorf("Policy Engine not initialized")
	}
	a.policyEngine.SetPauseBuilding(true)
	if a.cioEngine != nil {
		a.cioEngine.PauseBuilding(true)
	}
	if a.auditService != nil {
		a.auditService.LogAuditEvent(
			data.AuditEventOrder,
			"强制暂停建仓",
			"system",
			"pause_building",
			"user",
			"user",
			"success",
			map[string]interface{}{
				"timestamp": time.Now().Format(time.RFC3339),
				"reason":    "用户/CIO手动触发",
				"note":      "仅拦截买入，卖出不受影响",
			},
		)
	}
	log.Println("[QuantBot] 强制暂停建仓已启用（仅拦截买入，卖出不受影响）")
	return nil
}

// ResumeBuilding 恢复建仓：解除"强制暂停建仓"开关，恢复买入。
func (a *App) ResumeBuilding() error {
	if a.policyEngine == nil {
		return fmt.Errorf("Policy Engine not initialized")
	}
	a.policyEngine.SetPauseBuilding(false)
	if a.cioEngine != nil {
		a.cioEngine.PauseBuilding(false)
	}
	if a.auditService != nil {
		a.auditService.LogAuditEvent(
			data.AuditEventOrder,
			"恢复建仓",
			"system",
			"resume_building",
			"user",
			"user",
			"success",
			map[string]interface{}{
				"timestamp": time.Now().Format(time.RFC3339),
			},
		)
	}
	log.Println("[QuantBot] 强制暂停建仓已解除，恢复买入")
	return nil
}

// GetPolicyStatus获取Policy Engine状态
func (a *App) GetPolicyStatus() (interface{}, error) {
	if a.policyEngine == nil {
		return nil, fmt.Errorf("Policy Engine not initialized")
	}

	return map[string]interface{}{
		"emergency_stop": a.policyEngine.IsEmergencyStopped(),
		"pause_building": a.policyEngine.IsPauseBuilding(),
		"hard_limits":    a.policyEngine.GetHardLimits(),
	}, nil
}

// GetPortfolioState 获取组合状态概览
func (a *App) GetPortfolioState() (interface{}, error) {
	if a.portfolioEngine == nil {
		return nil, fmt.Errorf("Portfolio engine not initialized")
	}
	return a.portfolioEngine.GetPortfolioState(), nil
}

// GetPositions 获取当前持仓明细
func (a *App) GetPositions() (interface{}, error) {
	if a.portfolioEngine == nil {
		return nil, fmt.Errorf("Portfolio engine not initialized")
	}
	return a.portfolioEngine.GetPositions(), nil
}

// GetOrders 获取订单临时表(orders)记录，供前端「订单队列」展示待确认/已成交/已取消订单。
// includeFilled=false 时仅返回未成交（排队中）订单；true 返回全部。按创建时间倒序。
func (a *App) GetOrders(includeFilled bool) (interface{}, error) {
	if a.sqliteManager == nil {
		return []interface{}{}, nil
	}
	var orders []data.Order
	q := a.sqliteManager.GetDB().Model(&data.Order{}).Order("created_at DESC")
	if !includeFilled {
		q = q.Where("status != ?", "filled")
	}
	if err := q.Find(&orders).Error; err != nil {
		return nil, err
	}
	type orderView struct {
		OrderID      string     `json:"orderId"`
		Side         string     `json:"side"`
		InstrumentID string     `json:"instrumentId"`
		StockName    string     `json:"stockName"`
		Quantity     int        `json:"quantity"`
		Price        float64    `json:"price"`
		Amount       float64    `json:"amount"`
		Status       string     `json:"status"`
		SubmittedAt  *time.Time `json:"submittedAt"`
		FilledAt     *time.Time `json:"filledAt"`
		CreatedAt    time.Time  `json:"createdAt"`
	}
	dict := data.GetDictLoader()
	result := make([]orderView, 0, len(orders))
	for _, o := range orders {
		name := dict.GetStockName(o.InstrumentID)
		if name == "" || name == o.InstrumentID {
			name = o.InstrumentID // 找不到名称时用代码作为后备
		}
		result = append(result, orderView{
			OrderID:      o.OrderID,
			Side:         o.Side,
			InstrumentID: o.InstrumentID,
			StockName:    name,
			Quantity:     o.Quantity,
			Price:        o.Price,
			Amount:       o.Price * float64(o.Quantity),
			Status:       o.Status,
			SubmittedAt:  o.SubmittedAt,
			FilledAt:     o.FilledAt,
			CreatedAt:    o.CreatedAt,
		})
	}
	return result, nil
}

// refreshTHSTradingCalendarDays 用同花顺官方近一年交易日序列刷新 util 休市日历，
// 替代原 timor.tech 逐年抓取（该源 HTTP 403 失效）。THS 端点无入参、固定窗口
// [今日-1年, 今日]（Asia/Shanghai）。返回新增休市日数量。
func (a *App) refreshTHSTradingCalendarDays() (int, error) {
	if !thssdk.Enabled() {
		return 0, fmt.Errorf("同花顺数据源未启用（请在设置-数据源启用并填写 API Key）")
	}
	client := thssdk.Default()
	if client == nil {
		return 0, fmt.Errorf("同花顺数据源未配置")
	}
	days, err := client.TradingCalendarTradingDays(context.Background())
	if err != nil {
		return 0, fmt.Errorf("拉取同花顺交易日历失败: %w", err)
	}
	strs := make([]string, 0, len(days))
	for _, d := range days {
		if len(d.Date) == 8 {
			strs = append(strs, d.Date[:4]+"-"+d.Date[4:6]+"-"+d.Date[6:8])
		}
	}
	return util.SyncTHSTradingHoldays(strs)
}

// UpdateTradingCalendar 手动/智能体触发刷新A股交易日历并落盘到外挂配置文件。
// 数据源：同花顺官方近一年交易日序列（替代失效的 timor.tech 逐年抓取）。
// 返回新增休市日数量与覆盖情况。
func (a *App) UpdateTradingCalendar() (interface{}, error) {
	added, err := a.refreshTHSTradingCalendarDays()
	if err != nil {
		return map[string]interface{}{
			"status":  "error",
			"message": err.Error(),
		}, nil
	}
	return map[string]interface{}{
		"status":            "success",
		"added_holidays":    added,
		"last_year_covered": util.TradingCalendarLatestYear(),
		"message":           fmt.Sprintf("交易日历刷新完成（同花顺近一年），新增 %d 个休市日", added),
	}, nil
}

// PlaceTrade 执行交易（前端直接调用）
func (a *App) PlaceTrade(action string, symbol string, stockName string, price float64, quantity int, reason string) (interface{}, error) {
	if a.portfolioEngine == nil {
		return nil, fmt.Errorf("Portfolio engine not initialized")
	}

	if action == "" || symbol == "" || price <= 0 || quantity <= 0 {
		return nil, fmt.Errorf("无效的交易参数")
	}

	market := "SH"
	if len(symbol) >= 3 {
		prefix := symbol[:3]
		if prefix == "000" || prefix == "001" || prefix == "002" || prefix == "003" ||
			prefix == "200" || prefix == "300" || prefix == "301" {
			market = "SZ"
		} else if prefix == "430" || prefix == "831" || prefix == "870" || prefix == "871" || prefix == "872" || prefix == "873" {
			market = "BJ"
		}
	}

	switch action {
	case "BUY":
		// 实盘模式：真实下单 QMT，成交回报异步记账
		if a.broker != nil && a.broker.Mode() == broker.ModeLive {
			ext, err := a.submitOrderLive(symbol, broker.SideBuy, quantity, price, reason, "")
			if err != nil {
				return nil, err
			}
			return map[string]interface{}{
				"status":     "submitted",
				"externalID": ext,
				"action":     "BUY",
				"symbol":     symbol,
				"quantity":   quantity,
				"price":      price,
			}, nil
		}
		record, err := a.portfolioEngine.Buy(symbol, stockName, market, quantity, price, reason, "")
		if err != nil {
			return nil, err
		}
		return map[string]interface{}{
			"status":    "success",
			"tradeID":   record.TradeID,
			"action":    "BUY",
			"symbol":    symbol,
			"quantity":  record.Quantity,
			"price":     record.Price,
			"netAmount": record.NetAmount,
			"cashLeft":  a.portfolioEngine.GetCash(),
		}, nil

	case "SELL":
		// 实盘模式：真实下单 QMT，成交回报异步记账
		if a.broker != nil && a.broker.Mode() == broker.ModeLive {
			ext, err := a.submitOrderLive(symbol, broker.SideSell, quantity, price, reason, "")
			if err != nil {
				return nil, err
			}
			return map[string]interface{}{
				"status":     "submitted",
				"externalID": ext,
				"action":     "SELL",
				"symbol":     symbol,
				"quantity":   quantity,
				"price":      price,
			}, nil
		}
		record, err := a.portfolioEngine.Sell(symbol, quantity, price, reason, "")
		if err != nil {
			return nil, err
		}
		return map[string]interface{}{
			"status":      "success",
			"tradeID":     record.TradeID,
			"action":      "SELL",
			"symbol":      symbol,
			"quantity":    record.Quantity,
			"price":       record.Price,
			"netAmount":   record.NetAmount,
			"realizedPnL": record.RealizedPnL,
			"cashLeft":    a.portfolioEngine.GetCash(),
		}, nil

	default:
		return nil, fmt.Errorf("未知的交易方向: %s", action)
	}
}

// GetTradeHistory 获取交易历史
func (a *App) GetTradeHistory(limit int) (interface{}, error) {
	if a.portfolioEngine == nil {
		return nil, fmt.Errorf("Portfolio engine not initialized")
	}
	if limit <= 0 {
		limit = 500
	}
	records, err := a.portfolioEngine.GetTradeHistory(limit)
	if err != nil {
		return nil, err
	}
	trades := make([]map[string]interface{}, 0, len(records))
	for _, t := range records {
		trades = append(trades, map[string]interface{}{
			"tradeID":      t.TradeID,
			"side":         t.Side,
			"instrumentID": t.InstrumentID,
			"quantity":     t.Quantity,
			"price":        t.Price,
			"grossAmount":  t.GrossAmount,
			"commission":   t.Commission,
			"fees":         t.Fees,
			"netAmount":    t.NetAmount,
			"realizedPnL":  t.RealizedPnL,
			"tradeDate":    t.TradeDate.Format(time.RFC3339),
		})
	}
	return map[string]interface{}{
		"total":  len(records),
		"trades": trades,
		"limit":  limit,
	}, nil
}

// GetPortfolioRiskMetrics 获取组合风险评价（VaR95/VaR99/ES95 历史法），
// 供 AI 投资管理页展示。数据源自真实组合结算收益序列，严禁伪造；数据不足时明确提示。
func (a *App) GetPortfolioRiskMetrics() (interface{}, error) {
	if a.portfolioEngine == nil {
		return map[string]interface{}{
			"as_of": time.Now().Format(time.RFC3339),
			"note":  "组合引擎未初始化，无法计算风险指标",
		}, nil
	}
	toolpkg := tools.NewPortfolioRiskMetricsTool(a.portfolioEngine, a.duckdbManager, "sh000300")
	return toolpkg.Execute(context.Background(), map[string]interface{}{
		"lookback_days": float64(60),
	})
}

// ExecuteManualTrade 手动执行交易
func (a *App) ExecuteManualTrade(symbol, side string, quantity int, price float64, reason string) (interface{}, error) {
	if a.portfolioEngine == nil {
		return nil, fmt.Errorf("Portfolio engine not initialized")
	}

	// 解析 symbol，支持多种格式：
	// 1. "sh600519" 或 "SH600519" - 市场前缀 + 6位代码
	// 2. "600519" - 纯6位代码（自动判断市场）
	// 3. "sz000001" - 深市前缀
	symbol = strings.TrimSpace(symbol)
	market := ""
	code := ""

	if len(symbol) >= 8 && (symbol[:2] == "sh" || symbol[:2] == "SH" || symbol[:2] == "sz" || symbol[:2] == "SZ") {
		// 格式: sh600519 或 SH600519 或 sz000001
		market = strings.ToLower(symbol[:2])
		code = symbol[2:]
	} else if len(symbol) == 6 {
		// 纯6位代码，自动判断市场
		code = symbol
		// 根据代码前缀判断市场
		prefix := symbol[:3]
		if prefix == "000" || prefix == "001" || prefix == "002" || prefix == "003" ||
			prefix == "200" || prefix == "300" || prefix == "301" {
			market = "sz" // 深市主板、中小板、创业板
		} else if prefix == "430" || prefix == "831" || prefix == "870" || prefix == "871" || prefix == "872" || prefix == "873" {
			market = "bj" // 北交所
		} else {
			market = "sh" // 沪市主板、科创板
		}
	} else {
		return nil, fmt.Errorf("无效的股票代码格式: %s，请使用 sh600519 或 600519 格式", symbol)
	}

	// 验证代码格式
	if len(code) != 6 {
		return nil, fmt.Errorf("股票代码必须是6位数字，当前: %s", code)
	}

	// 从字典获取股票名称
	stockName := data.GetDictLoader().GetStockName(code)
	if stockName == "" || stockName == code {
		// 尝试带市场前缀查找
		stockName = data.GetDictLoader().GetStockName(market + code)
	}
	if stockName == "" || stockName == code {
		stockName = code // 如果找不到名称，使用代码作为后备
	}

	var result interface{}
	var err error

	if side == "BUY" {
		result, err = a.portfolioEngine.Buy(code, stockName, market, quantity, price, reason, "")
	} else if side == "SELL" {
		result, err = a.portfolioEngine.Sell(code, quantity, price, reason, "")
	} else {
		return nil, fmt.Errorf("Invalid side: %s, must be BUY or SELL", side)
	}

	if err != nil {
		return nil, err
	}

	// 记录审计
	if a.auditService != nil {
		a.auditService.LogAuditEvent(
			data.AuditEventOrder,
			fmt.Sprintf("手动交易: %s %s %d@%.2f", side, stockName, quantity, price),
			"trader", side, symbol, "Manual", "success", "",
		)
	}

	return result, nil
}

// GetPendingTradeApprovals 获取所有待确认交易
func (a *App) GetPendingTradeApprovals() (interface{}, error) {
	if a.tradeApproval == nil {
		return []interface{}{}, nil
	}
	return a.tradeApproval.GetPending(), nil
}

// recoverPendingOrders 重启恢复：把临时表 orders 中仍处于 pending 的待确认订单挂回审批服务，
// 并重新占用其买入资金，使用户可继续确认/拒绝。
// 仅在「交易日且未收盘(15:00)」时恢复；已过收盘/非交易日的残留排队订单判定失败并释放资金。
func (a *App) recoverPendingOrders() {
	if a.tradeApproval == nil || a.sqliteManager == nil || a.portfolioEngine == nil {
		return
	}
	var orders []data.Order
	if err := a.sqliteManager.GetDB().
		Where("status = ?", "pending").
		Find(&orders).Error; err != nil {
		log.Printf("[QuantBot] recoverPendingOrders: 查询待确认订单失败: %v", err)
		return
	}
	now := time.Now().In(time.FixedZone("CST", 8*3600))
	recoverable := util.IsTradingDay(now) && now.Before(time.Date(now.Year(), now.Month(), now.Day(), 15, 0, 0, 0, now.Location()))
	for _, o := range orders {
		pt := &tradeapproval.PendingTrade{
			ID:         o.OrderID,
			Action:     o.Side,
			Symbol:     o.InstrumentID,
			Quantity:   o.Quantity,
			Price:      o.Price,
			Amount:     o.Price * float64(o.Quantity),
			DecisionID: o.DecisionTraceID,
			CreatedAt:  now,
			Status:     "pending",
		}
		if !recoverable {
			// 已收盘/非交易日：判定失败，取消订单并释放占用的买入资金
			a.sqliteManager.GetDB().Model(&data.Order{}).
				Where("order_id = ?", o.OrderID).
				Update("status", "cancelled")
			if o.Side == "BUY" {
				a.portfolioEngine.ReleaseBuyReserve(pt.Amount)
			}
			if a.auditService != nil {
				a.auditService.LogAuditEvent(
					data.AuditEventOrder,
					fmt.Sprintf("重启恢复：收盘/非交易日的排队订单判定失败: %s %s", o.Side, o.InstrumentID),
					"system", "RECOVER", o.OrderID, "System", "success", "",
				)
			}
			continue
		}
		// 正常恢复：买入重新占用资金；挂回审批服务等待用户确认
		if o.Side == "BUY" {
			if err := a.portfolioEngine.ReserveBuy(pt.Amount); err != nil {
				a.sqliteManager.GetDB().Model(&data.Order{}).
					Where("order_id = ?", o.OrderID).
					Update("status", "cancelled")
				continue
			}
		}
		a.tradeApproval.AddRecovered(pt)
		log.Printf("[QuantBot] 重启恢复待确认订单: %s %s %d@%.2f", o.Side, o.InstrumentID, o.Quantity, o.Price)
	}
}

// ConfirmTradeApproval 确认交易（approve=true 批准，false 拒绝）
func (a *App) ConfirmTradeApproval(id string, approve bool) error {
	if a.tradeApproval == nil {
		return fmt.Errorf("交易审批服务未初始化")
	}
	if err := a.tradeApproval.Confirm(id, approve); err != nil {
		return err
	}
	if a.auditService != nil {
		action := "批准"
		if !approve {
			action = "拒绝"
		}
		a.auditService.LogAuditEvent(
			data.AuditEventOrder,
			fmt.Sprintf("用户%s智能体交易确认: %s", action, id),
			"user", "APPROVAL", id, "User", "success", "",
		)
	}
	return nil
}

// TriggerRebalance 触发组合再平衡
func (a *App) TriggerRebalance() (interface{}, error) {
	if a.cioEngine == nil {
		return nil, fmt.Errorf("CIO engine not initialized")
	}
	if a.portfolioEngine == nil {
		return nil, fmt.Errorf("Portfolio engine not initialized")
	}

	analysis := a.cioEngine.AnalyzeMarket(a.ctx)
	portfolio := a.cioEngine.AnalyzePortfolio(a.ctx)
	riskCheck := a.cioEngine.CheckRiskPolicy(a.ctx)
	decision := a.cioEngine.FormulateDecision(a.ctx, analysis, portfolio, riskCheck)
	a.cioEngine.ExecuteDecision(a.ctx, decision)

	if a.auditService != nil {
		a.auditService.LogAuditEvent(
			data.AuditEventStrategy,
			fmt.Sprintf("再平衡: %s, 订单数: %d", decision.Decision, len(decision.Orders)),
			"cio", "REBALANCE", "portfolio", "CIO", "success", "",
		)
	}

	trades, _ := a.portfolioEngine.GetTradeHistory(10)
	tradeList := make([]map[string]interface{}, 0, len(trades))
	for _, t := range trades {
		tradeList = append(tradeList, map[string]interface{}{
			"tradeID":      t.TradeID,
			"side":         t.Side,
			"instrumentID": t.InstrumentID,
			"quantity":     t.Quantity,
			"price":        t.Price,
			"grossAmount":  t.GrossAmount,
			"tradeDate":    t.TradeDate.Format(time.RFC3339),
		})
	}

	return map[string]interface{}{
		"decision":  decision,
		"portfolio": a.portfolioEngine.GetPortfolioState(),
		"trades":    tradeList,
	}, nil
}

// GetProfitHistory 获取历史收益数据
func (a *App) GetProfitHistory(days int) (interface{}, error) {
	if a.portfolioEngine == nil {
		return nil, fmt.Errorf("Portfolio engine not initialized")
	}

	if days <= 0 {
		days = 30
	}

	history, err := a.portfolioEngine.GetProfitHistory(days)
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"days":   days,
		"points": history,
	}, nil
}

// GenerateMonthlyReport 生成投资者的月度报告（Markdown 文件），
// year/month 缺省（<=0）时生成最近一个自然月的报告。返回报告文件绝对路径。
func (a *App) GenerateMonthlyReport(year, month int) (interface{}, error) {
	if a.portfolioEngine == nil {
		return nil, fmt.Errorf("Portfolio engine not initialized")
	}
	var dir string
	if exePath, err := os.Executable(); err == nil {
		dir = filepath.Join(filepath.Dir(exePath), "reports")
	}
	path, err := a.portfolioEngine.GenerateMonthlyReport(year, month, dir)
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{
		"file":   path,
		"report": path,
	}, nil
}

// GetPortfolioNavBenchmark 获取组合账面净值(NAV)序列与沪深300业绩基准的对比：
//
//	净值(NAV) = 1 + 累计收益率，基准净值 = 从组合首日按沪深300收盘归一化到 1。
//	同时计算组合净值最大回撤水位，供绩效评价与风险预算使用。
//
// 数据来源：组合日结算(portfolio_daily_stats) + 沪深300指数(sh000300)日K线(DuckDB)，均真实数据。
func (a *App) GetPortfolioNavBenchmark(days int) (interface{}, error) {
	if a.portfolioEngine == nil {
		return nil, fmt.Errorf("Portfolio engine not initialized")
	}
	if days <= 0 {
		days = 365
	}

	// 1) 组合每日结算序列（date, totalAssets, totalReturn）
	history, err := a.portfolioEngine.GetProfitHistory(days)
	if err != nil {
		return nil, err
	}
	if len(history) == 0 {
		return map[string]interface{}{
			"days": days, "series": []interface{}{},
			"summary": map[string]interface{}{"message": "暂无净值数据，待日终结算后生成"},
		}, nil
	}

	// 2) 沪深300基准日K（DuckDB，真实收盘价），按日期升序整理
	var bench []struct {
		Date  time.Time
		Close float64
	}
	if a.duckdbManager != nil {
		if bars, err := a.duckdbManager.GetIndexKlineFromStock(context.Background(), "sh000300", days+10); err == nil {
			for _, b := range bars {
				if b.Close > 0 {
					bench = append(bench, struct {
						Date  time.Time
						Close float64
					}{b.Date, b.Close})
				}
			}
			sort.Slice(bench, func(i, j int) bool { return bench[i].Date.Before(bench[j].Date) })
		}
	}

	// 3) 生成净值序列 + 基准对照 + 最大回撤
	type navPoint struct {
		Date        string  `json:"date"`
		Nav         float64 `json:"nav"`
		Benchmark   float64 `json:"benchmarkNav"`
		TotalReturn float64 `json:"totalReturn"`
		MaxDrawdown float64 `json:"maxDrawdown"`
	}
	series := make([]navPoint, 0, len(history))

	// 基准归一化基准点：以组合首日为 1
	benchBase := 0.0
	bi := 0
	baseAssigned := false

	for _, p := range history {
		dateStr, _ := p["date"].(string)
		tr, _ := p["totalReturn"].(float64)
		nav := 1.0 + tr/100.0

		// 取该日基准收盘（精确匹配，若缺失则沿用最近一个更早的、且不超过该日的收盘）
		var benchClose float64
		if len(bench) > 0 {
			for bi < len(bench) && bench[bi].Date.Format("2006-01-02") <= dateStr {
				benchClose = bench[bi].Close
				bi++
			}
			// 未越界且该日无记录时，回退最近一个已见基准值
			if benchClose == 0 && bi > 0 {
				benchClose = bench[bi-1].Close
			}
		}
		if !baseAssigned && benchClose > 0 {
			benchBase = benchClose
			baseAssigned = true
		}

		benchNav := 0.0
		if benchBase > 0 && benchClose > 0 {
			benchNav = benchClose / benchBase
		}

		series = append(series, navPoint{
			Date: dateStr, Nav: round2p(nav), Benchmark: round4p(benchNav),
			TotalReturn: tr, MaxDrawdown: 0,
		})
	}

	// 4) 组合净值最大回撤水位（逐点累计）
	maxDD := 0.0
	peak := 0.0
	for i := range series {
		if series[i].Nav > peak {
			peak = series[i].Nav
		}
		if peak > 0 {
			dd := (peak - series[i].Nav) / peak * 100
			if dd > maxDD {
				maxDD = dd
			}
		}
		series[i].MaxDrawdown = round2p(maxDD)
	}

	// 5) 汇总
	last := series[len(series)-1]
	benchNetReturn := 0.0
	if len(bench) > 0 && benchBase > 0 {
		if b := bench[len(bench)-1].Close; b > 0 {
			benchNetReturn = (b/benchBase - 1) * 100
		}
	}
	summary := map[string]interface{}{
		"date":            last.Date,
		"currentNav":      last.Nav,
		"totalReturn":     round2p(last.TotalReturn),
		"benchmarkReturn": round2p(benchNetReturn),
		"alpha":           round2p(last.TotalReturn - benchNetReturn),
		"maxDrawdown":     maxDD,
		"benchmark":       "沪深300(sh000300)",
	}

	return map[string]interface{}{
		"days": days, "series": series, "summary": summary,
	}, nil
}

func (a *App) GetPositionSnapshots(startDate, endDate string) (interface{}, error) {
	if a.portfolioEngine == nil {
		return nil, fmt.Errorf("Portfolio engine not initialized")
	}

	if startDate == "" {
		startDate = time.Now().AddDate(0, 0, -30).Format("2006-01-02")
	}
	if endDate == "" {
		endDate = time.Now().Format("2006-01-02")
	}

	snapshots, err := a.portfolioEngine.GetPositionSnapshots(startDate, endDate)
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"startDate": startDate,
		"endDate":   endDate,
		"snapshots": snapshots,
	}, nil
}

// GetLatestDailyStat 获取最新的每日统计
func (a *App) GetLatestDailyStat() (interface{}, error) {
	if a.portfolioEngine == nil {
		return nil, fmt.Errorf("Portfolio engine not initialized")
	}

	stat, err := a.portfolioEngine.GetLatestDailyStat()
	if err != nil {
		return map[string]interface{}{
			"totalAssets":    0,
			"totalPnL":       0,
			"totalReturn":    0,
			"dailyPnL":       0,
			"dailyReturn":    0,
			"cash":           0,
			"marketValue":    0,
			"positionsCount": 0,
			"message":        "暂无历史数据",
		}, nil
	}

	return map[string]interface{}{
		"date":             stat.StatDate,
		"totalAssets":      stat.TotalAssets,
		"totalPnL":         stat.TotalPnL,
		"totalReturn":      stat.TotalReturn,
		"dailyPnL":         stat.DailyPnL,
		"dailyReturn":      stat.DailyReturn,
		"cash":             stat.Cash,
		"marketValue":      stat.MarketValue,
		"positionsCount":   stat.PositionsCount,
		"totalDailyVolume": stat.TotalDailyVolume,
		"tradeCount":       stat.TradeCount,
	}, nil
}
