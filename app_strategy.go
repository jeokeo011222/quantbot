package main

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/quantpilot/quantpilot/internal/backtest"
	"github.com/quantpilot/quantpilot/internal/data"
	"github.com/quantpilot/quantpilot/internal/screener"
	"github.com/quantpilot/quantpilot/internal/strategy"
)

// GetStrategyList 获取策略模板列表
func (a *App) GetStrategyList() (interface{}, error) {
	if a.screenerService == nil {
		return nil, fmt.Errorf("Screener service not initialized")
	}

	templates := a.screenerService.GetStrategyList()
	var result []interface{}
	for _, t := range templates {
		weights := make(map[string]float64)
		for id, w := range t.Weights {
			weights[string(id)] = w
		}

		result = append(result, map[string]interface{}{
			"id":          t.ID,
			"name":        t.Name,
			"description": t.Description,
			"icon":        t.Icon,
			"riskLevel":   t.RiskLevel,
			"weights":     weights,
		})
	}

	return map[string]interface{}{
		"strategies": result,
	}, nil
}

// ValidateCustomWeights 验证自定义权重（Pro/Enterprise功能）
func (a *App) ValidateCustomWeights(weightsJSON string) (interface{}, error) {
	if a.screenerService == nil {
		return nil, fmt.Errorf("Screener service not initialized")
	}

	var weights map[screener.FactorID]float64
	if err := json.Unmarshal([]byte(weightsJSON), &weights); err != nil {
		return nil, fmt.Errorf("权重解析失败: %w", err)
	}

	err := a.screenerService.ValidateCustomWeights(weights)
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"valid":   true,
		"message": "权重验证通过",
	}, nil
}

// GetDailyStrategyPlans 获取最近 N 日策略日计划（daily_strategy_plans 表，按交易日倒序）。
// 量化分析师盘后选定次日交易策略，操盘手次日按该策略信号执行卖出（如 KDJ 死叉）。
// 供 AI 投资管理页「今日交易策略」卡片展示；数据来自真实策略选择落库，严禁伪造。
func (a *App) GetDailyStrategyPlans(limit int) (result interface{}, err error) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[DailyStrategy] GetDailyStrategyPlans panic recovered: %v", r)
			result = map[string]interface{}{
				"plans": []interface{}{},
				"error": fmt.Sprintf("内部异常: %v", r),
			}
			err = nil
		}
	}()

	if limit <= 0 {
		limit = 5
	}
	if limit > 30 {
		limit = 30
	}
	if a.sqliteManager == nil || a.sqliteManager.GetDB() == nil {
		return map[string]interface{}{
			"plans": []interface{}{},
			"note":  "数据库不可用，无法获取策略日计划",
		}, nil
	}
	var plans []data.DailyStrategyPlan
	// 只展示当天的交易策略（trade_date = 今日东八区日期），不展示历史策略。
	// 策略日计划按 apply 日（次日交易日）落库，故"今日策略"即 TradeDate == 今日的条目。
	today := time.Now().In(time.FixedZone("CST", 8*3600)).Format("2006-01-02")
	if err := a.sqliteManager.GetDB().Where("trade_date = ?", today).Order("id DESC").Find(&plans).Error; err != nil {
		log.Printf("[DailyStrategy] GetDailyStrategyPlans error: %v", err)
		return map[string]interface{}{
			"plans": []interface{}{},
			"error": err.Error(),
		}, nil
	}
	out := make([]map[string]interface{}, 0, len(plans))
	for i := range plans {
		p := plans[i]
		out = append(out, map[string]interface{}{
			"id":                p.ID,
			"trade_date":        p.TradeDate,
			"strategy_type":     p.StrategyType,
			"strategy_name":     p.StrategyName,
			"reason":            p.Reason,
			"status":            p.Status,
			"executed_sell_num": p.ExecutedSellNum,
			"created_by":        p.CreatedBy,
			"source":            p.Source,
			"created_at":        p.CreatedAt,
			"updated_at":        p.UpdatedAt,
		})
	}
	log.Printf("[DailyStrategy] GetDailyStrategyPlans ok, plans=%d", len(out))
	return map[string]interface{}{
		"plans": out,
		"total": len(out),
	}, nil
}

// GetStrategyStockBindings 查询每股当前绑定的策略与参数（strategy_stock_params 表）。
// 收盘后量化分析师（Quant）对每只持仓按该股自身近期日K线滚动调参，为每只个股选出一套最适配的
// 策略并写入本表（每股一行，code 唯一），次日 CIO/操盘手在个股上计算策略信号时按该绑定执行。
// 本接口返回当前全部个股绑定，含策略类型、策略名、调优后参数及在个股上的表现指标，
// 供「策略中心」展示。未绑定任何个股时返回空数组。
func (a *App) GetStrategyStockBindings() (result interface{}, err error) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[StrategyBinding] GetStrategyStockBindings panic: %v", r)
			result = map[string]interface{}{"bindings": []interface{}{}, "total": 0}
			err = nil
		}
	}()

	if a.sqliteManager == nil || a.sqliteManager.GetDB() == nil {
		return map[string]interface{}{"bindings": []interface{}{}, "total": 0, "note": "数据库不可用"}, nil
	}
	db := a.sqliteManager.GetDB()
	var rows []data.StrategyStockParam
	if err := db.Order("updated_at DESC").Find(&rows).Error; err != nil {
		return nil, err
	}

	// 建立股票代码→名称映射（TradeableStock 表），用于在策略中心展示可读股票名称。
	codeNames := map[string]string{}
	var cands []data.TradeableStock
	if err := db.Select("stock_code, stock_name, market").Find(&cands).Error; err == nil {
		for _, c := range cands {
			bare := strings.ToLower(c.StockCode)
			if bare != "" {
				codeNames[bare] = c.StockName
				codeNames[strings.ToLower(c.Market)+bare] = c.StockName
			}
		}
	}

	out := make([]map[string]interface{}, 0, len(rows))
	for i := range rows {
		r := rows[i]
		var params map[string]interface{}
		if r.ParamsJSON != "" {
			_ = json.Unmarshal([]byte(r.ParamsJSON), &params)
		}
		out = append(out, map[string]interface{}{
			"code":          r.Code,
			"stock_name":    codeNames[strings.ToLower(r.Code)],
			"strategy_type": r.StrategyType,
			"strategy_name": r.StrategyName,
			"params":        params,
			"params_json":   r.ParamsJSON,
			"window_days":   r.WindowDays,
			"sharpe_ratio":  r.SharpeRatio,
			"total_return":  r.TotalReturn,
			"win_rate":      r.WinRate,
			"max_drawdown":  r.MaxDrawdown,
			"trades":        r.Trades,
			"updated_at":    r.UpdatedAt.Format("2006-01-02 15:04:05"),
		})
	}
	return map[string]interface{}{"bindings": out, "total": len(out)}, nil
}

// RefreshStrategyMetrics 后台异步重新回测全部内置策略并写入最新指标（供前端「刷新」按钮触发）。
// 返回 immediately（启动 goroutine），避免长耗时阻塞 Wails 调用与前端；并发重入由互斥锁串行化，
// 已有一轮在跑时直接返回 started=false。刷新完成后前端重新拉取 GetStrategies 即可看到最新值。
func (a *App) RefreshStrategyMetrics() (interface{}, error) {
	if a.strategyService == nil {
		return nil, fmt.Errorf("策略服务未初始化")
	}
	if !a.strategyRefreshMu.TryLock() {
		return map[string]interface{}{"started": false, "message": "已有指标刷新正在进行中"}, nil
	}
	go func() {
		defer appRecover("手动策略指标刷新")
		defer a.strategyRefreshMu.Unlock()
		start := time.Now()
		if err := a.strategyService.RefreshStrategyMetrics(a.duckdbManager); err != nil {
			log.Printf("[QuantBot] 手动策略指标刷新失败: %v", err)
		} else {
			log.Printf("[QuantBot] 手动策略指标刷新完成，耗时 %v", time.Since(start))
		}
	}()
	return map[string]interface{}{"started": true, "message": "已开始后台刷新策略指标"}, nil
}

// GetStrategies 获取策略列表
func (a *App) GetStrategies(status string) (interface{}, error) {
	if a.strategyService == nil {
		return nil, fmt.Errorf("策略服务未初始化")
	}

	strategies, err := a.strategyService.GetStrategies(status)
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"strategies": strategies,
		"totalCount": len(strategies),
	}, nil
}

// GetStrategy 获取单个策略
func (a *App) GetStrategy(id uint) (interface{}, error) {
	if a.strategyService == nil {
		return nil, fmt.Errorf("策略服务未初始化")
	}

	strategy, err := a.strategyService.GetStrategy(id)
	if err != nil {
		return nil, err
	}

	return strategy, nil
}

// CreateStrategy 创建策略
func (a *App) CreateStrategy(name string, description string, strategyType string, initialCapital float64, maxPosition int, stopLossPct float64, takeProfitPct float64, operatorID string, operatorName string, configJSON ...string) (interface{}, error) {
	if a.strategyService == nil {
		return nil, fmt.Errorf("策略服务未初始化")
	}

	// 解析configJSON参数
	cfgJSON := "{}"
	if len(configJSON) > 0 && configJSON[0] != "" {
		cfgJSON = configJSON[0]
	}

	req := &strategy.CreateStrategyRequest{
		Name:           name,
		Description:    description,
		StrategyType:   strategyType,
		InitialCapital: initialCapital,
		MaxPosition:    maxPosition,
		StopLossPct:    stopLossPct,
		TakeProfitPct:  takeProfitPct,
		ConfigJSON:     cfgJSON,
		OperatorID:     operatorID,
		OperatorName:   operatorName,
	}

	strategy, err := a.strategyService.CreateStrategy(req)
	if err != nil {
		return nil, err
	}

	if a.auditService != nil {
		a.auditService.LogAuditEvent(
			data.AuditEventAIAnalysis,
			fmt.Sprintf("创建策略: %s", name),
			"strategy",
			"create",
			operatorID,
			operatorName,
			"success",
			map[string]interface{}{
				"strategy_id":   strategy.ID,
				"strategy_name": name,
				"strategy_type": strategyType,
			},
		)
	}

	return strategy, nil
}

// UpdateStrategy 更新策略
func (a *App) UpdateStrategy(id uint, name string, description string, strategyType string, initialCapital float64, maxPosition int, stopLossPct float64, takeProfitPct float64) (interface{}, error) {
	if a.strategyService == nil {
		return nil, fmt.Errorf("策略服务未初始化")
	}

	req := &strategy.UpdateStrategyRequest{
		ID:             id,
		Name:           name,
		Description:    description,
		StrategyType:   strategyType,
		InitialCapital: initialCapital,
		MaxPosition:    maxPosition,
		StopLossPct:    stopLossPct,
		TakeProfitPct:  takeProfitPct,
	}

	strategy, err := a.strategyService.UpdateStrategy(req)
	if err != nil {
		return nil, err
	}

	return strategy, nil
}

// DeleteStrategy 删除策略
func (a *App) DeleteStrategy(id uint) (interface{}, error) {
	if a.strategyService == nil {
		return nil, fmt.Errorf("策略服务未初始化")
	}

	if err := a.strategyService.DeleteStrategy(id); err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"status": "ok",
	}, nil
}

// ToggleStrategyStatus 切换策略状态
func (a *App) ToggleStrategyStatus(id uint) (interface{}, error) {
	if a.strategyService == nil {
		return nil, fmt.Errorf("策略服务未初始化")
	}

	strategy, err := a.strategyService.ToggleStrategyStatus(id)
	if err != nil {
		return nil, err
	}

	return strategy, nil
}

// RunBacktestGrid 策略超参网格扫描（吸收 vectorbt bruteforce 理念）：
// 参数在样本内(前70%)评分选优、样本外(后30%)验证，避免用全样本选参产生前视/过拟合。
// gridJSON 为可选 JSON 数组（例：[{"FastPeriod":5,"SlowPeriod":20}, ...]），为空时用该策略默认网格。
// 返回排序后的参数组合 + 最优参数及其样本外真实绩效，本操作不写库、不改策略表。
func (a *App) RunBacktestGrid(strategyType, stockCode, stockName, market, period, startDate, endDate string, initialCapital float64, objective string, gridJSON string) (interface{}, error) {
	if a.backtestService == nil {
		return nil, fmt.Errorf("回测服务未初始化")
	}

	var grid []map[string]float64
	if strings.TrimSpace(gridJSON) != "" {
		if err := json.Unmarshal([]byte(gridJSON), &grid); err != nil {
			return nil, fmt.Errorf("参数网格 gridJSON 解析失败: %w", err)
		}
	}

	req := &backtest.RunBacktestRequest{
		StrategyType:   strategyType,
		StockCode:      stockCode,
		StockName:      stockName,
		Market:         market,
		Period:         period,
		StartDate:      startDate,
		EndDate:        endDate,
		InitialCapital: initialCapital,
	}
	return a.backtestService.RunBacktestGrid(req, objective, grid)
}

// RunBacktest 运行回测
func (a *App) RunBacktest(strategyID string, strategyName string, strategyType string, startDate string, endDate string, initialCapital float64, operatorID string, operatorName string, stockCode string, stockName string, market string, period string) (interface{}, error) {
	if a.backtestService == nil {
		return nil, fmt.Errorf("回测服务未初始化")
	}

	req := &backtest.RunBacktestRequest{
		StrategyID:     strategyID,
		StrategyName:   strategyName,
		StrategyType:   strategyType,
		StockCode:      stockCode,
		StockName:      stockName,
		Market:         market,
		Period:         period,
		StartDate:      startDate,
		EndDate:        endDate,
		InitialCapital: initialCapital,
		OperatorID:     operatorID,
		OperatorName:   operatorName,
	}

	result, err := a.backtestService.RunBacktest(req)
	if err != nil {
		return nil, err
	}

	// 更新策略表现指标
	if a.strategyService != nil {
		// 将strategyID转换为uint（假设是数字格式）
		var strategyIDUint uint
		if _, err := fmt.Sscanf(strategyID, "%d", &strategyIDUint); err == nil {
			_ = a.strategyService.UpdateStrategyMetrics(strategyIDUint, result.SharpeRatio, result.MaxDrawdown, result.AnnualReturn, result.WinRate, result.Turnover)
		}
	}

	if a.auditService != nil {
		a.auditService.LogAuditEvent(
			data.AuditEventAIAnalysis,
			fmt.Sprintf("运行回测: %s, 收益=%.2f%%", strategyName, result.AnnualReturn),
			"backtest",
			"run",
			operatorID,
			operatorName,
			"success",
			map[string]interface{}{
				"strategy_id":     strategyID,
				"strategy_name":   strategyName,
				"annual_return":   result.AnnualReturn,
				"sharpe_ratio":    result.SharpeRatio,
				"max_drawdown":    result.MaxDrawdown,
				"win_rate":        result.WinRate,
				"total_trades":    result.TotalTrades,
				"initial_capital": initialCapital,
				"final_capital":   result.FinalCapital,
			},
		)
	}

	return result, nil
}

// GetBacktestResults 获取回测结果列表
func (a *App) GetBacktestResults(strategyID string, status string, limit int, offset int) (interface{}, error) {
	if a.backtestService == nil {
		return nil, fmt.Errorf("回测服务未初始化")
	}

	req := &backtest.GetBacktestResultsRequest{
		StrategyID: strategyID,
		Status:     status,
		Limit:      limit,
		Offset:     offset,
	}

	results, totalCount, err := a.backtestService.GetBacktestResults(req)
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"results":    results,
		"totalCount": totalCount,
	}, nil
}

// GetBacktestResult 获取单个回测结果
func (a *App) GetBacktestResult(id uint) (interface{}, error) {
	if a.backtestService == nil {
		return nil, fmt.Errorf("回测服务未初始化")
	}

	result, err := a.backtestService.GetBacktestResult(id)
	if err != nil {
		return nil, err
	}

	return result, nil
}

// DeleteBacktestResult 删除回测结果
func (a *App) DeleteBacktestResult(id uint) (interface{}, error) {
	if a.backtestService == nil {
		return nil, fmt.Errorf("回测服务未初始化")
	}

	if err := a.backtestService.DeleteBacktestResult(id); err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"status": "ok",
	}, nil
}

// GetBacktestStats 获取回测统计信息
func (a *App) GetBacktestStats() (interface{}, error) {
	if a.backtestService == nil {
		return nil, fmt.Errorf("回测服务未初始化")
	}

	return a.backtestService.GetBacktestStats()
}
