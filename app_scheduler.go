package main

import (
	"context"
	"fmt"
	"log"
	"runtime/debug"
	"strings"
	"time"

	"github.com/quantpilot/quantpilot/internal/data"
	"github.com/quantpilot/quantpilot/internal/orderbook"
	"github.com/quantpilot/quantpilot/internal/scheduler"
	"github.com/quantpilot/quantpilot/internal/thssdk"
	"github.com/quantpilot/quantpilot/internal/tools"
	"github.com/quantpilot/quantpilot/internal/util"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// startDailySnapshotTimer 启动每日收盘价入库定时器
// 在每日15:00后自动执行收盘价入库
func (a *App) startDailySnapshotTimer(ctx context.Context) {
	go func() {
		// 添加 panic 恢复
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[QuantBot] Daily snapshot timer panic: %v", r)
				log.Printf("[QuantBot] Stack trace: %s", debug.Stack())
			}
		}()

		log.Println("[QuantBot] Starting daily snapshot timer (15:00 after market close)")

		for {
			now := time.Now()
			// 计算下一个15:00
			target := time.Date(now.Year(), now.Month(), now.Day(), 15, 0, 0, 0, now.Location())
			if now.After(target) {
				// 如果已经过了15:00，设置为明天15:00
				target = target.Add(24 * time.Hour)
			}

			// 计算等待时间
			waitDuration := target.Sub(now)
			log.Printf("[QuantBot] Next daily snapshot at: %s (in %v)", target.Format("2006-01-02 15:04:05"), waitDuration)

			// 等待到目标时间
			timer := time.NewTimer(waitDuration)
			select {
			case <-ctx.Done():
				timer.Stop()
				log.Println("[QuantBot] Daily snapshot timer stopped")
				return
			case <-timer.C:
				// 检查 portfolioEngine 是否就绪
				if a.portfolioEngine == nil {
					log.Println("[QuantBot] Portfolio engine not ready, skipping daily snapshot")
					continue
				}

				// 检查今天是否已经记录过快照
				today := time.Now().Format("2006-01-02")
				existingStat, err := a.portfolioEngine.GetLatestDailyStat()
				if err == nil && existingStat.StatDate == today {
					log.Printf("[QuantBot] Daily snapshot already recorded for %s", today)
					continue
				}

				// 执行收盘价入库
				log.Printf("[QuantBot] Executing daily snapshot at %s", time.Now().Format("15:04:05"))
				result, err := a.RecordDailySnapshot()
				if err != nil {
					log.Printf("[QuantBot] Daily snapshot error: %v", err)
				} else {
					log.Printf("[QuantBot] Daily snapshot completed: %v", result)
				}

				// 发送通知事件
				runtime.EventsEmit(ctx, "daily:snapshot", map[string]interface{}{
					"success":   err == nil,
					"timestamp": time.Now().Format(time.RFC3339),
					"message":   "每日收盘价快照已记录",
				})

				// 等待1分钟后再次检查（防止重复执行）
				time.Sleep(1 * time.Minute)
			}
		}
	}()
}

// startPreMarketAutoStartTimer 启动盘前自动启动定时器
// 在每日 9:15 自动执行盘前分析和工作流触发
func (a *App) startPreMarketAutoStartTimer(ctx context.Context) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[QuantBot] Pre-market auto-start timer panic: %v", r)
				log.Printf("[QuantBot] Stack trace: %s", debug.Stack())
			}
		}()

		log.Println("[QuantBot] Starting pre-market auto-start timer (09:15 daily)")

		for {
			now := time.Now()

			// 检查是否为工作日（周一至周五）
			weekday := now.Weekday()
			isWorkday := weekday >= time.Monday && weekday <= time.Friday

			// 计算下一个 9:15
			target := time.Date(now.Year(), now.Month(), now.Day(), 9, 15, 0, 0, now.Location())

			// 如果已经过了 9:15 或今天不是工作日，设置为下一个工作日
			for now.After(target) || !isWorkday {
				target = target.Add(24 * time.Hour)
				weekday = target.Weekday()
				isWorkday = weekday >= time.Monday && weekday <= time.Friday
			}

			// 计算等待时间
			waitDuration := target.Sub(now)
			log.Printf("[QuantBot] Next pre-market auto-start at: %s (in %v)", target.Format("2006-01-02 15:04:05"), waitDuration)

			// 等待到目标时间
			timer := time.NewTimer(waitDuration)
			select {
			case <-ctx.Done():
				timer.Stop()
				log.Println("[QuantBot] Pre-market auto-start timer stopped")
				return
			case <-timer.C:
				log.Printf("[QuantBot] === Pre-market auto-start triggered at %s ===", time.Now().Format("2006-01-02 15:04:05"))

				// 触发盘前工作流
				a.triggerPreMarketAutoWorkflow(ctx)

				// 等待 90 秒后再次检查（防止重复执行）
				time.Sleep(90 * time.Second)
			}
		}
	}()
}

// triggerPreMarketAutoWorkflow 触发盘前自动工作流
func (a *App) triggerPreMarketAutoWorkflow(ctx context.Context) {
	log.Println("[QuantBot] Triggering pre-market auto workflow...")

	// 发送系统事件通知
	runtime.EventsEmit(ctx, "premarket:auto_start", map[string]interface{}{
		"timestamp": time.Now().Format(time.RFC3339),
		"phase":     "PRE_MARKET",
		"message":   "9:15 盘前自动启动",
	})

	// 触发自动调度器的盘前分析
	if a.autoScheduler != nil {
		log.Println("[QuantBot] Triggering auto scheduler phase transition to PRE_OPEN")
		// 直接调用时段转换检查
		phase := util.PhasePreOpen
		a.autoScheduler.TriggerPhaseAction(phase)
	}

	// 触发任务调度器的盘前工作流
	if a.taskScheduler != nil {
		log.Println("[QuantBot] Triggering task scheduler daily workflow")
		a.taskScheduler.TriggerDailyWorkflow()
	}

	// 触发编排器的每日投资工作流
	if a.orchestratorAPI != nil {
		log.Println("[QuantBot] Triggering orchestrator daily investment workflow")
		go func() {
			defer appRecover("每日投资工作流")
			result, err := a.orchestratorAPI.StartDailyWorkflow()
			if err != nil {
				log.Printf("[QuantBot] Orchestrator daily workflow failed: %v", err)
				runtime.EventsEmit(ctx, "premarket:error", map[string]interface{}{
					"error":     err.Error(),
					"timestamp": time.Now().Format(time.RFC3339),
				})
			} else {
				log.Printf("[QuantBot] Orchestrator daily workflow started: %v", result)
				runtime.EventsEmit(ctx, "premarket:success", map[string]interface{}{
					"result":    result,
					"timestamp": time.Now().Format(time.RFC3339),
				})
			}
		}()
	}

	log.Println("[QuantBot] Pre-market auto workflow triggered successfully")
}

// StartContinuousTrading 启动持续交易监控（交易时段每5分钟刷新）
func (a *App) StartContinuousTrading() error {
	if a.portfolioEngine == nil {
		return fmt.Errorf("Portfolio engine not initialized")
	}
	a.portfolioEngine.StartContinuousTrading(a.ctx, 5*time.Minute)

	if a.auditService != nil {
		a.auditService.LogAuditEvent(
			data.AuditEventOrder,
			"启动持续交易监控",
			"system",
			"continuous_trading",
			"user",
			"user",
			"success",
			map[string]interface{}{
				"interval":  "30s",
				"timestamp": time.Now().Format(time.RFC3339),
			},
		)
	}

	return nil
}

// StopContinuousTrading 停止持续交易监控
func (a *App) StopContinuousTrading() error {
	if a.portfolioEngine == nil {
		return fmt.Errorf("Portfolio engine not initialized")
	}
	a.portfolioEngine.StopContinuousTrading()

	if a.auditService != nil {
		a.auditService.LogAuditEvent(
			data.AuditEventOrder,
			"停止持续交易监控",
			"system",
			"continuous_trading",
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

// GetContinuousTradingStatus 获取持续交易状态
func (a *App) GetContinuousTradingStatus() (interface{}, error) {
	if a.portfolioEngine == nil {
		return nil, fmt.Errorf("Portfolio engine not initialized")
	}
	return a.portfolioEngine.GetContinuousStatus(), nil
}

// GetAutoSchedulerStatus 获取自动调度器状态
func (a *App) GetAutoSchedulerStatus() (interface{}, error) {
	if a.autoScheduler == nil {
		return map[string]interface{}{
			"running":   false,
			"message":   "自动调度器未初始化",
			"timestamp": time.Now().Format(time.RFC3339),
		}, nil
	}
	return a.autoScheduler.GetStatus(), nil
}

// StartAutoScheduler 手动启动自动调度器
func (a *App) StartAutoScheduler() error {
	if a.autoScheduler == nil {
		if a.harnessApp == nil || a.auditService == nil {
			return fmt.Errorf("系统未初始化完成")
		}
		a.autoScheduler = scheduler.NewAutoScheduler(a.harnessApp, a.auditService)
	}
	a.autoScheduler.Start()
	return nil
}

// StopAutoScheduler 手动停止自动调度器
func (a *App) StopAutoScheduler() error {
	if a.autoScheduler == nil {
		return fmt.Errorf("自动调度器未初始化")
	}
	a.autoScheduler.Stop()
	return nil
}

// GetMarketPhaseInfo 获取当前市场时段信息
func (a *App) GetMarketPhaseInfo() (interface{}, error) {
	phase := util.GetMarketPhase(time.Now())
	return map[string]interface{}{
		"phase":           string(phase),
		"label":           util.PhaseLabels[phase],
		"is_working_hour": util.IsWorkingHour(time.Now()),
		"is_trading_hour": util.IsTradingHour(time.Now()),
		"is_weekday":      util.IsWeekday(time.Now()),
		"timestamp":       time.Now().Format(time.RFC3339),
	}, nil
}

// ExecuteTaskManually 手动触发指定任务（绕过时段限制）
func (a *App) ExecuteTaskManually(taskID string) (interface{}, error) {
	if a.taskScheduler == nil {
		return nil, fmt.Errorf("Task scheduler not initialized")
	}

	err := a.taskScheduler.ExecuteTaskByID(taskID)
	if err != nil {
		log.Printf("[QuantBot] ExecuteTaskManually failed: %v", err)
		return nil, err
	}

	return map[string]interface{}{
		"status":  "triggered",
		"task_id": taskID,
		"message": fmt.Sprintf("任务 %s 已触发执行", taskID),
	}, nil
}

// ExecutePhaseManually 手动触发指定时段的所有任务
func (a *App) ExecutePhaseManually(phase string) (interface{}, error) {
	if a.taskScheduler == nil {
		return nil, fmt.Errorf("Task scheduler not initialized")
	}

	var taskPhase scheduler.TaskPhase
	switch phase {
	case "pre_market", "PRE_MARKET":
		taskPhase = scheduler.PhasePreMarket
	case "in_market", "IN_MARKET":
		taskPhase = scheduler.PhaseInMarket
	case "post_market", "POST_MARKET":
		taskPhase = scheduler.PhasePostMarket
	case "review", "REVIEW":
		taskPhase = scheduler.PhaseReview
	default:
		return nil, fmt.Errorf("unknown phase: %s (valid: pre_market, in_market, post_market, review)", phase)
	}

	err := a.taskScheduler.ExecutePhaseTasks(taskPhase)
	if err != nil {
		log.Printf("[QuantBot] ExecutePhaseManually failed: %v", err)
		return nil, err
	}

	return map[string]interface{}{
		"status":  "triggered",
		"phase":   phase,
		"message": fmt.Sprintf("时段 %s 的所有任务已触发执行", phase),
	}, nil
}

// ExecuteAllTasksManually 手动触发所有任务（调试模式）
func (a *App) ExecuteAllTasksManually() (interface{}, error) {
	if a.taskScheduler == nil {
		return nil, fmt.Errorf("Task scheduler not initialized")
	}

	err := a.taskScheduler.ExecuteAllTasks()
	if err != nil {
		log.Printf("[QuantBot] ExecuteAllTasksManually failed: %v", err)
		return nil, err
	}

	return map[string]interface{}{
		"status":  "triggered",
		"message": "所有任务已触发执行（调试模式）",
	}, nil
}

// GetTaskSchedulerStatus 获取任务调度器状态
func (a *App) GetTaskSchedulerStatus() (interface{}, error) {
	if a.taskScheduler == nil {
		return nil, fmt.Errorf("Task scheduler not initialized")
	}

	return map[string]interface{}{
		"is_running":         a.taskScheduler.IsRunning(),
		"current_phase":      string(a.taskScheduler.GetCurrentPhase()),
		"available_tasks":    len(a.taskScheduler.GetTasks()),
		"executor_available": a.taskScheduler.GetExecutorAvailability(),
	}, nil
}

// RecordDailySnapshot 手动触发记录每日持仓快照
func (a *App) RecordDailySnapshot() (interface{}, error) {
	if a.portfolioEngine == nil {
		return nil, fmt.Errorf("Portfolio engine not initialized")
	}

	// 先刷新一次价格确保快照数据是最新的
	a.portfolioEngine.RefreshPrices()

	err := a.portfolioEngine.RecordDailySnapshot()
	if err != nil {
		return nil, err
	}

	if a.auditService != nil {
		a.auditService.LogAuditEvent(
			data.AuditEventLiveActivity,
			"记录每日持仓快照",
			"portfolio",
			"daily_snapshot",
			"system",
			"system",
			"success",
			map[string]interface{}{
				"date":      time.Now().Format("2006-01-02"),
				"timestamp": time.Now().Format(time.RFC3339),
			},
		)
	}

	return map[string]interface{}{
		"status": "ok",
		"date":   time.Now().Format("2006-01-02"),
	}, nil
}

// RecordDailySettlement 记录每日结算（供 AutoScheduler 每日收盘后调用）
func (a *App) RecordDailySettlement(ctx context.Context) error {
	if a.portfolioEngine == nil {
		return fmt.Errorf("Portfolio engine not initialized")
	}

	// 1) 日终结算：仍处于排队等待确认的交易自动判定失败（15:00 收盘未获确认信号）
	if a.tradeApproval != nil {
		if n := a.tradeApproval.ClosePending(); n > 0 {
			log.Printf("[App] 日终结算：%d 笔排队交易未获用户确认，已自动判定失败", n)
		}
	}

	// 2) 持仓数量 vs 成交数量校对：以 sqlite 成交记录（ΣBUY−ΣSELL + 最近收盘快照）为准
	//    对账持仓，修正漂移，确保结算快照与收益率/净值计算基于一致数据
	if err := a.portfolioEngine.ReconcileForSettlement(); err != nil {
		log.Printf("[App] 日终结算持仓校对失败: %v", err)
		return err
	}

	// 复盘/结算时段非交易，加载最近结算收盘价计价后写库
	if err := a.portfolioEngine.RecordDailySnapshot(); err != nil {
		return err
	}

	// P0：用当日结算的真实市场收益对账先前沉淀的"可证伪声明"，量化智能体判断命中率
	if a.cioEngine != nil {
		if err := a.cioEngine.VerifyClaims(a.ctx); err != nil {
			log.Printf("[App] 日终结算验证可证伪声明失败: %v", err)
		}
	}

	// 盘后个股滚动调参：对当前持仓用其自身近期日K线（默认250日）为活跃策略调优，
	// 写 strategy_stock_params 覆盖表；操盘手次日在该股上计算策略信号时优先采用，
	// 避免"指数基准调参"与低beta个股偏离导致的信号参数错配。
	if n, err := a.TuneStockParamsByHoldings(250); err != nil {
		log.Printf("[App] 日终结算个股滚动调参失败: %v", err)
	} else if n > 0 {
		log.Printf("[App] 日终结算个股滚动调参完成：覆盖 %d 组", n)
	}
	return nil
}

// TuneStockParamsByHoldings 个股滚动调参：按当前持仓自身近期日K线为所有活跃策略调参，
// 写入 strategy_stock_params 覆盖表。windowDays 为回看交易日窗口（默认250）。
// 返回成功写入覆盖的 (股票,策略) 组数。盘后自动结算(RecordDailySettlement)与经 Wails 前端均可调用。
func (a *App) TuneStockParamsByHoldings(windowDays int) (int, error) {
	if a.portfolioEngine == nil || a.sqliteManager == nil || a.duckdbManager == nil {
		return 0, fmt.Errorf("引擎未初始化，无法运行个股滚动调参")
	}
	if windowDays < 100 {
		windowDays = 250
	}
	snap := a.portfolioEngine.GetSnapshot()
	if snap == nil {
		return 0, nil
	}
	var codes []string
	for _, pos := range snap.Positions {
		if pos == nil || pos.InstrumentID == "" {
			continue
		}
		codes = append(codes, strings.ToLower(pos.Market)+pos.InstrumentID)
	}
	if len(codes) == 0 {
		log.Printf("[App] 盘后个股滚动调参：当前无持仓，跳过")
		return 0, nil
	}
	tuner := tools.NewStrategyTunerTool(a.sqliteManager, a.duckdbManager)
	done, err := tuner.TuneHeldStocks(codes, windowDays)
	if err != nil {
		return 0, err
	}
	return done, nil
}

// RecordOrderBookDaily 记录当日盘口日频摘要（供 AutoScheduler 每日收盘后调用）
// 将可交易股票池标的的实时盘口（涨跌停状态/量比/内外盘占比/封单量/换手率）写入 order_book_daily 表，
// 供盘口因子研究与回测真实性使用。数据来自实时行情（真实数据，无伪造）；
// 实时盘口获取失败或股票池为空时跳过本次落库。
func (a *App) RecordOrderBookDaily(ctx context.Context) error {
	if a.tradeablePool == nil || a.duckdbManager == nil {
		return fmt.Errorf("TradeablePool/DuckDB not initialized")
	}

	// 取股票池全部标的（含待审核/已买入/已卖出），构成盘口研究宇宙
	stocks, err := a.tradeablePool.GetAllStocksWithFilter("", "", "", "score", 0)
	if err != nil {
		return fmt.Errorf("读取可交易股票池失败: %w", err)
	}
	if len(stocks) == 0 {
		log.Printf("[App] 盘口日频摘要落库：股票池为空，跳过")
		return nil
	}

	// 构造带市场前缀代码（sh600519）
	codes := make([]string, 0, len(stocks))
	for _, st := range stocks {
		if st.StockCode == "" {
			continue
		}
		codes = append(codes, strings.ToLower(st.Market)+st.StockCode)
	}
	if len(codes) == 0 {
		return nil
	}

	snaps, _ := data.FetchRealtimeStockSnapshots(codes)
	if len(snaps) == 0 {
		log.Printf("[App] 盘口日频摘要落库：实时盘口获取失败，跳过本次")
		return nil
	}

	today := time.Now().Format("2006-01-02")
	rows := make([]data.OrderBookDailyRow, 0, len(snaps))
	for _, sn := range snaps {
		ob := orderbook.ClassifySnapshot(sn)
		rows = append(rows, data.OrderBookDailyRow{
			TradeDate:      today,
			Code:           strings.ToLower(sn.Market) + sn.Code,
			Name:           sn.Name,
			LimitUp:        ob.LimitUp,
			LimitDown:      ob.LimitDown,
			State:          string(ob.State),
			StateCN:        ob.Chinese,
			VolumeRatio:    ob.VolumeRatio,
			InOutRatio:     ob.InOutRatio,
			SealVol:        ob.SealVol,
			HugeVolumeDown: ob.HugeVolumeDown,
			TurnoverRate:   sn.TurnoverRate,
		})
	}
	if err := a.duckdbManager.SaveOrderBookDailyRows(ctx, rows); err != nil {
		return fmt.Errorf("盘口日频摘要落库失败: %w", err)
	}
	log.Printf("[App] 盘口日频摘要落库成功：%s 共 %d 条", today, len(rows))
	return nil
}

// ==================== 同花顺日K自动同步（定时 16:00 + 启动补拉，通达信本地兜底） ====================

// startTHSDailyKSyncTimer 启动每日日K自动同步定时器。
// 每个自然日 16:00 触发拉取当天 K 线（同花顺官方优先，失败/未配置时回退通达信本地补充）。
func (a *App) startTHSDailyKSyncTimer(ctx context.Context) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[QuantBot] THS daily-K sync timer panic: %v", r)
				log.Printf("[QuantBot] Stack trace: %s", debug.Stack())
			}
		}()

		log.Println("[QuantBot] Starting THS daily-K sync timer (16:00 daily)")

		for {
			now := time.Now()
			// 下一个 16:00
			target := time.Date(now.Year(), now.Month(), now.Day(), 16, 0, 0, 0, now.Location())
			if now.After(target) {
				target = target.Add(24 * time.Hour)
			}
			wait := target.Sub(now)
			log.Printf("[QuantBot] Next THS daily-K sync at: %s (in %v)", target.Format("2006-01-02 15:04:05"), wait)

			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				log.Println("[QuantBot] THS daily-K sync timer stopped")
				return
			case <-timer.C:
				var reasons []string
				if !a.syncDailyKlineAuto(ctx, "每日16:00定时", true) {
					reasons = append(reasons, "日K数据未更新到最新交易日")
				}
				// 财务数据不再随定时自动同步：占用时长较长，改为「数据维护-同花顺数据」手工触发。
				// 同步未成功则弹窗提醒：建议下个交易日 9 点前补充股票时序数据
				a.emitDailyKSyncWarning(ctx, reasons)
				// 等待以规避重复触发
				time.Sleep(1 * time.Minute)
			}
		}
	}()
}

// syncDailyKlineAuto 按需/定时拉取日K：仅由同花顺官方增量拉取。
// includeToday=true 表示期望覆盖"今天"（16:00 定时盘后场景）；false 用于启动补拉上一交易日。
// 通达信本地数据仅作手工补位（数据维护-通达信数据页签执行），此处不自动触发、不自动兜底。
// 返回 true 表示数据已到位（含拉取前已就绪）；false 表示拉取失败或官方数据尚未更新到目标交易日。
func (a *App) syncDailyKlineAuto(ctx context.Context, reason string, includeToday bool) bool {
	if a.duckdbManager == nil || !a.duckdbManager.HasStockDB() {
		log.Printf("[THS] %s：股票数据库未初始化，跳过", reason)
		return false
	}
	want := a.dailyTargetTradingDate(includeToday)
	if a.dailyKlineCaughtUp(want) {
		log.Printf("[THS] %s：%s 数据已就绪，无需拉取", reason, want.Format("2006-01-02"))
		return true
	}

	// 仅同花顺官方拉取（需已配置 API Key）
	if !thssdk.Enabled() {
		log.Printf("[THS] %s：同花顺官方源未配置，无法自动拉取 %s 日K；如数据缺失请在「数据维护-通达信数据」手工同步补位", reason, want.Format("2006-01-02"))
		return false
	}
	ctx2, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	res, err := a.duckdbManager.ImportTHSDailyK(ctx2, "incr")
	if err != nil {
		log.Printf("[THS] %s 同花顺日K拉取失败：%v（可用通达信本地数据手工补位）", reason, err)
		return false
	}
	log.Printf("[THS] %s 同花顺日K拉取完成：%s", reason, res.Message)
	if a.auditService != nil {
		a.auditService.LogAuditEvent("DATA_MAINTENANCE", "auto_daily_k_sync", "stock_ohlc", "", "system", "system", "success", map[string]interface{}{
			"reason":   reason,
			"inserted": res.Inserted,
		})
	}
	// 拉取后重新判位：官方 bulk 数据可能滞后于目标交易日
	return a.dailyKlineCaughtUp(want)
}

// emitDailyKSyncWarning 16:00 定时同步未成功时弹窗提醒：建议下个交易日 9 点前及时补充股票时序数据。
func (a *App) emitDailyKSyncWarning(ctx context.Context, reasons []string) {
	if len(reasons) == 0 {
		return
	}
	msg := "同花顺数据同步未完全成功：" + strings.Join(reasons, "；") + "。建议在下个交易日 9 点前及时补充股票时序数据（可在「数据维护-通达信数据」手工同步补位，或稍后在「数据维护-同花顺数据」重试全量日K导入）。"
	log.Printf("[QuantBot] [警示] %s", msg)
	runtime.EventsEmit(ctx, "data:syncwarning", map[string]interface{}{
		"message":   msg,
		"timestamp": time.Now().Format(time.RFC3339),
	})
}

// dailyTargetTradingDate 计算期望覆盖的最新交易日。
// includeToday=true（盘后定时）：若今天为交易日则期望今天；否则回溯到最近一个交易日。
// includeToday=false（启动补拉）：期望上一交易日（回溯最近一个已过去的交易日）。
// 优先使用同花顺交易日历（含节假日，精确）；未同步交易日历时回退工作日假设。
func (a *App) dailyTargetTradingDate(includeToday bool) time.Time {
	now := time.Now()
	loc := now.Location()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)

	if a.duckdbManager != nil && a.duckdbManager.HasStockDB() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if s, ok := a.duckdbManager.TradingCalendarLastDay(ctx, today.Format("2006-01-02"), !includeToday); ok {
			if t, err := time.ParseInLocation("2006-01-02", s, loc); err == nil {
				return t
			}
		}
	}

	// 回退：工作日假设（周末跳过；节假日由交易日历精确处理，未同步时不覆盖）
	for i := 0; i < 10; i++ {
		d := time.Date(now.Year(), now.Month(), now.Day()-i, 0, 0, 0, 0, loc)
		if d.Weekday() >= time.Monday && d.Weekday() <= time.Friday {
			if i == 0 && !includeToday {
				continue // 启动补拉场景跳过今天（今天盘前/盘中尚无收盘数据）
			}
			return d
		}
	}
	return today
}

// dailyKlineCaughtUp 判断 stock_daily 是否已覆盖到最近交易日 want。
func (a *App) dailyKlineCaughtUp(want time.Time) bool {
	st := a.duckdbManager.THSDailyKStatus()
	latestStr, ok := st["latest_date"].(string)
	if !ok || latestStr == "" {
		return false
	}
	latest, err := time.Parse("2006-01-02", latestStr)
	if err != nil {
		return false
	}
	return !latest.Before(want)
}
