package scheduler

import (
	"context"
	"fmt"
	"log"
	"runtime/debug"
	"sync"
	"time"

	"github.com/quantpilot/quantpilot/internal/audit"
	"github.com/quantpilot/quantpilot/internal/config"
	"github.com/quantpilot/quantpilot/internal/data"
	"github.com/quantpilot/quantpilot/internal/harness"
	"github.com/quantpilot/quantpilot/internal/util"
)

// TradeablePoolProcessor 可交易股票池处理器接口
// 用于在盘中交易时处理可交易股票池的自动买卖
type TradeablePoolProcessor interface {
	// ProcessTradeablePool 处理可交易股票池（买入/卖出）
	ProcessTradeablePool(ctx context.Context) error
}

// DailyReviewProcessor 每日复盘处理器接口
// 用于在盘后生成每日复盘报告
type DailyReviewProcessor interface {
	// TriggerDailyReview 触发生成每日复盘报告
	TriggerDailyReview(ctx context.Context) error
}

// PlanRefreshProcessor 每日投资方案自动重建处理器接口
// 用于每个交易日盘后，用最新全市场因子数据重建当前投资方案的选股与持仓并落库，
// 实现「每日定时自动重建方案」，使方案股票池随市场变化滚动更新。
type PlanRefreshProcessor interface {
	// RebuildInvestmentPlan 用最新市场数据的真实选股重建当前投资方案的持仓
	RebuildInvestmentPlan(ctx context.Context) error
}

// DailySettlementProcessor 每日结算处理器接口
// 用于每个交易日收盘后，将当日总资产/累计盈亏/今日盈亏写入数据库（PortfolioDailyStat）。
// 今日盈亏依赖“昨日结算总资产”作为基准，若无每日结算记录，今日盈亏将错误地等同累计盈亏。
type DailySettlementProcessor interface {
	// RecordDailySettlement 记录当日结算快照
	RecordDailySettlement(ctx context.Context) error
}

// OrderBookDailyProcessor 盘口日频摘要落库处理器接口
// 用于每个交易日收盘后，将可交易股票池的实时盘口（涨跌停状态/量比/内外盘比/封单量等）
// 摘要写入 order_book_daily 表，供盘口因子研究与回测真实性使用。
type OrderBookDailyProcessor interface {
	// RecordOrderBookDaily 记录当日盘口日频摘要
	RecordOrderBookDaily(ctx context.Context) error
}

// CIOIntradayPlanProcessor 盘中 CIO 投资方案实时监控处理器接口
// 交易时段内周期性地让首席投资官(CIO)检查投资方案、比对当前组合，
// 根据方案目标与实际执行的偏差做出投资决策，并记录审计活动日志。
type CIOIntradayPlanProcessor interface {
	// MonitorIntradayInvestmentPlan 盘中实时监控投资方案并决策
	MonitorIntradayInvestmentPlan(ctx context.Context) error
}

// AutoScheduler 自动交易调度器
// 根据市场时段自动触发智能体工作流程
type AutoScheduler struct {
	mu sync.RWMutex

	// 核心依赖
	harness  *harness.QuantHarness
	auditSvc *audit.AuditService
	ctx      context.Context
	cancel   context.CancelFunc

	// 可交易股票池处理器
	tradeablePoolProcessor TradeablePoolProcessor

	// 盘中 CIO 投资方案实时监控处理器
	cioIntradayPlanProcessor CIOIntradayPlanProcessor

	// 每日复盘处理器
	dailyReviewProcessor DailyReviewProcessor

	// 每日投资方案自动重建处理器
	planRefreshProcessor PlanRefreshProcessor
	lastPlanRefreshDate  string

	// 每日结算处理器
	dailySettlementProcessor DailySettlementProcessor

	// 盘口日频摘要落库处理器
	orderBookDailyProcessor OrderBookDailyProcessor

	// 调度状态
	running      bool
	lastPhase    util.MarketPhase
	lastRunTime  time.Time
	cycleRunning bool

	// 统计信息
	totalRuns      int
	successfulRuns int
	failedRuns     int

	// 可交易股票池处理相关
	lastTradeablePoolTime time.Time
	tradeablePoolInterval time.Duration

	// 盘中 CIO 投资方案监控相关
	lastCIOIntradayMonitorTime time.Time
	cioIntradayMonitorInterval time.Duration

	// 每日复盘相关
	lastDailyReviewDate string

	// 每日盘前任务补跑保障相关
	// 若程序晚启动或数据原因导致当日盘前决策未成功生成，只要在当天 15:00 前，
	// 盘中调度就周期性补跑盘前任务直至决策落库；15:00 后放弃补跑。
	lastDecisionEnsureTime time.Time
	decisionEnsureInterval time.Duration
	decisionEnsureCount    int
	decisionEnsureDate     string // 当日补跑计数归属的交易日
}

// 盘中补跑判定截止时刻（15:00），超过则放弃补跑当日盘前决策任务
const decisionEnsureDeadline = 15 * 60 // 分钟维度 15:00

// decisionEnsureMaxRun 每个交易日盘中补跑盘前决策的最大次数（约10分钟一次，15:00前最多约8次）
const decisionEnsureMaxRun = 8

// NewAutoScheduler 创建自动调度器
func NewAutoScheduler(h *harness.QuantHarness, auditSvc *audit.AuditService) *AutoScheduler {
	ctx, cancel := context.WithCancel(context.Background())
	s := &AutoScheduler{
		harness:                    h,
		auditSvc:                   auditSvc,
		ctx:                        ctx,
		cancel:                     cancel,
		lastPhase:                  util.PhaseClosed,
		tradeablePoolInterval:      5 * time.Minute,
		cioIntradayMonitorInterval: 10 * time.Minute,
		decisionEnsureInterval:     10 * time.Minute,
	}
	// 启动时从持久化配置恢复"当日某任务已执行"状态，避免重启后重跑当日已完成的
	// 每日复盘与投资方案自动重建（这两者由智能体按定时调度执行，非每次启动执行）。
	if h != nil {
		cfg := h.GetConfig()
		s.lastDailyReviewDate = cfg.LastDailyReviewDate
		s.lastPlanRefreshDate = cfg.LastPlanRefreshDate
		if cfg.LastDailyReviewDate != "" || cfg.LastPlanRefreshDate != "" {
			log.Printf("[AutoScheduler] 恢复调度状态: 复盘=%s, 方案重建=%s",
				cfg.LastDailyReviewDate, cfg.LastPlanRefreshDate)
		}
	}
	return s
}

// SetTradeablePoolProcessor 设置可交易股票池处理器
func (s *AutoScheduler) SetTradeablePoolProcessor(processor TradeablePoolProcessor) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tradeablePoolProcessor = processor
}

// SetCIOIntradayPlanProcessor 设置盘中 CIO 投资方案实时监控处理器
func (s *AutoScheduler) SetCIOIntradayPlanProcessor(processor CIOIntradayPlanProcessor) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cioIntradayPlanProcessor = processor
}

// SetDailyReviewProcessor 设置每日复盘处理器
func (s *AutoScheduler) SetDailyReviewProcessor(processor DailyReviewProcessor) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dailyReviewProcessor = processor
}

// SetPlanRefreshProcessor 设置每日投资方案自动重建处理器
func (s *AutoScheduler) SetPlanRefreshProcessor(processor PlanRefreshProcessor) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.planRefreshProcessor = processor
}

// SetDailySettlementProcessor 设置每日结算处理器
func (s *AutoScheduler) SetDailySettlementProcessor(processor DailySettlementProcessor) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dailySettlementProcessor = processor
}

// SetOrderBookDailyProcessor 设置盘口日频摘要落库处理器
func (s *AutoScheduler) SetOrderBookDailyProcessor(processor OrderBookDailyProcessor) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.orderBookDailyProcessor = processor
}

// Start 启动自动调度器
func (s *AutoScheduler) Start() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.running {
		return
	}

	s.running = true

	if s.auditSvc != nil {
		s.auditSvc.LogAuditEvent(
			data.AuditEventLiveActivity,
			"启动自动交易调度器",
			"system",
			"auto_scheduler_start",
			"system",
			"QuantBot",
			"success",
			map[string]interface{}{
				"message":   "智能体自动调度器已启动，将根据市场时段自动执行交易流程",
				"timestamp": time.Now().Format(time.RFC3339),
			},
		)
	}

	go s.schedulingLoop()
	log.Println("[AutoScheduler] 自动交易调度器已启动")
}

// Stop 停止自动调度器
func (s *AutoScheduler) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running {
		return
	}

	s.running = false
	s.cancel()

	if s.auditSvc != nil {
		s.auditSvc.LogAuditEvent(
			data.AuditEventLiveActivity,
			"停止自动交易调度器",
			"system",
			"auto_scheduler_stop",
			"system",
			"QuantBot",
			"success",
			map[string]interface{}{
				"total_runs":      s.totalRuns,
				"successful_runs": s.successfulRuns,
				"failed_runs":     s.failedRuns,
				"timestamp":       time.Now().Format(time.RFC3339),
			},
		)
	}

	log.Println("[AutoScheduler] 自动交易调度器已停止")
}

// GetStatus 获取调度器状态
func (s *AutoScheduler) GetStatus() map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()

	currentPhase := util.GetMarketPhase(time.Now())

	return map[string]interface{}{
		"running":         s.running,
		"current_phase":   string(currentPhase),
		"phase_label":     util.PhaseLabels[currentPhase],
		"is_working_hour": util.IsWorkingHour(time.Now()),
		"is_trading_hour": util.IsTradingHour(time.Now()),
		"last_phase":      string(s.lastPhase),
		"last_run_time":   s.lastRunTime.Format(time.RFC3339),
		"cycle_running":   s.cycleRunning,
		"total_runs":      s.totalRuns,
		"successful_runs": s.successfulRuns,
		"failed_runs":     s.failedRuns,
		"timestamp":       time.Now().Format(time.RFC3339),
	}
}

// schedulingLoop 主调度循环
func (s *AutoScheduler) schedulingLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	s.checkAndTrigger()
	// 启动较晚（已进入盘中时段）时补偿执行错过的盘前任务
	s.compensateMissedPreMarket()

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.checkAndTrigger()
		}
	}
}

// compensateMissedPreMarket 补偿错过的盘前任务
// 当系统启动较晚（已进入盘中时段）且当天尚未执行盘前分析时，补偿执行一次，
// 确保当日决策正常生成，避免智能体一直处于"等待盘前任务启动"状态
func (s *AutoScheduler) compensateMissedPreMarket() {
	now := time.Now()
	currentPhase := util.GetMarketPhase(now)

	// 仅在盘中时段补偿（盘前时段会正常触发，盘后/休市无需补偿）
	if currentPhase != util.PhaseMorningSession && currentPhase != util.PhaseAfternoonSession {
		return
	}

	// 当天已执行过盘前任务则不重复执行
	today := now.Format("2006-01-02")
	if s.hasRunPreMarketToday(today) {
		return
	}

	log.Printf("[AutoScheduler] 检测到盘前任务未执行（启动较晚），补偿执行盘前分析...")
	s.runPreMarketAnalysis()
}

// hasRunPreMarketToday 检查当天是否已执行过盘前任务
func (s *AutoScheduler) hasRunPreMarketToday(today string) bool {
	if s.harness == nil {
		return false
	}
	sm := s.harness.GetSQLiteManager()
	if sm == nil {
		return false
	}
	var count int64
	if err := sm.GetDB().Model(&data.AgentTaskLog{}).
		Where("task_date = ? AND task_phase = ?", today, "PRE_MARKET").
		Count(&count).Error; err != nil {
		log.Printf("[AutoScheduler] Failed to check pre-market tasks: %v", err)
		return false
	}
	return count > 0
}

// checkAndTrigger 检查市场时段并触发相应操作
func (s *AutoScheduler) checkAndTrigger() {
	now := time.Now()
	currentPhase := util.GetMarketPhase(now)

	s.mu.Lock()
	previousPhase := s.lastPhase
	s.mu.Unlock()

	// 检测时段转换
	if currentPhase != previousPhase {
		log.Printf("[AutoScheduler] 时段转换: %s -> %s", previousPhase, currentPhase)

		if s.auditSvc != nil {
			s.auditSvc.LogAuditEvent(
				data.AuditEventLiveActivity,
				fmt.Sprintf("市场时段转换: %s -> %s", previousPhase, currentPhase),
				"system",
				"phase_transition",
				"system",
				"QuantBot",
				"success",
				map[string]interface{}{
					"from_phase": string(previousPhase),
					"to_phase":   string(currentPhase),
					"label":      util.PhaseLabels[currentPhase],
					"timestamp":  now.Format(time.RFC3339),
				},
			)
		}

		s.mu.Lock()
		s.lastPhase = currentPhase
		s.mu.Unlock()

		s.triggerPhaseAction(currentPhase)
	}

	// 盘中时段定期执行可交易股票池处理（每5分钟）
	if currentPhase == util.PhaseMorningSession || currentPhase == util.PhaseAfternoonSession {
		s.processTradeablePool()
		// 盘中时段定期执行 CIO 投资方案实时监控与决策（每10分钟）
		s.processCIOIntradayPlanMonitor()
		// 盘中时段保障当日盘前决策已生成（晚启动/数据失败时在15:00前补跑）
		s.ensureDailyDecisionGenerated()
	}
}

// triggerPhaseAction 根据时段触发工作流
func (s *AutoScheduler) triggerPhaseAction(phase util.MarketPhase) {
	switch phase {
	case util.PhasePreOpen:
		s.runPreMarketAnalysis()
	case util.PhaseMorningSession, util.PhaseAfternoonSession:
		s.logIntradayStart()
	case util.PhasePostMarket:
		s.runPostMarketAnalysis()
	case util.PhaseReview:
		// 复盘阶段：确保每日复盘报告已生成（幂等，已生成则跳过）
		s.generateDailyReview()
		// 复盘阶段：每日自动重建投资方案股票池（幂等，每日一次）
		s.rebuildInvestmentPlanDay()
	case util.PhaseClosed:
		s.logIntradayEnd()
	}
}

// TriggerPhaseAction 公开的触发时段动作方法
// 供外部定时器调用
func (s *AutoScheduler) TriggerPhaseAction(phase util.MarketPhase) {
	s.mu.Lock()
	s.lastPhase = phase
	s.mu.Unlock()
	s.triggerPhaseAction(phase)
}

// runPreMarketAnalysis 盘前分析
func (s *AutoScheduler) runPreMarketAnalysis() {
	if s.cycleRunning {
		log.Println("[AutoScheduler] 分析周期正在运行中，跳过盘前分析")
		return
	}

	s.mu.Lock()
	s.cycleRunning = true
	s.totalRuns++
	s.lastRunTime = time.Now()
	s.mu.Unlock()

	log.Printf("[AutoScheduler] ========== 开始盘前智能体分析 ==========")

	s.logPhaseStart("PRE_MARKET", "盘前智能体分析")

	result, err := s.harness.RunDailyCycleWithMode(s.ctx, func(phase string, progress float64, message string) {
		log.Printf("[AutoScheduler] 盘前进度: %s %.0f%% %s", phase, progress*100, message)
	})

	s.mu.Lock()
	s.cycleRunning = false
	s.mu.Unlock()

	if err != nil {
		log.Printf("[AutoScheduler] 盘前分析失败: %v", err)
		s.mu.Lock()
		s.failedRuns++
		s.mu.Unlock()

		s.logPhaseEnd("PRE_MARKET", false, err.Error(), nil)
		return
	}

	s.mu.Lock()
	s.successfulRuns++
	s.mu.Unlock()

	if result != nil {
		s.logPhaseEnd("PRE_MARKET", true, "盘前分析完成", map[string]interface{}{
			"date":             result.Date,
			"errors":           result.Errors,
			"agent_count":      len(result.AgentSessions),
			"trajectory_count": len(result.Trajectories),
		})

		s.logAgentTrajectories(result)
	}

	log.Printf("[AutoScheduler] ========== 盘前分析完成 ==========")
}

// logIntradayStart 记录盘中开始并处理可交易股票池
func (s *AutoScheduler) logIntradayStart() {
	log.Printf("[AutoScheduler] 进入盘中交易时段")

	if s.auditSvc != nil {
		s.auditSvc.LogAuditEvent(
			data.AuditEventLiveActivity,
			"进入盘中交易时段",
			"system",
			"intraday_session",
			"system",
			"QuantBot",
			"success",
			map[string]interface{}{
				"phase":     string(util.GetMarketPhase(time.Now())),
				"timestamp": time.Now().Format(time.RFC3339),
			},
		)
	}

	// 盘中交易时处理可交易股票池
	s.processTradeablePool()
}

// processTradeablePool 处理可交易股票池
func (s *AutoScheduler) processTradeablePool() {
	s.mu.Lock()
	now := time.Now()
	// 检查是否需要处理（避免过于频繁）
	if !s.lastTradeablePoolTime.IsZero() && now.Sub(s.lastTradeablePoolTime) < s.tradeablePoolInterval {
		s.mu.Unlock()
		return
	}
	s.lastTradeablePoolTime = now
	processor := s.tradeablePoolProcessor
	s.mu.Unlock()

	if processor == nil {
		return
	}

	log.Printf("[AutoScheduler] 处理可交易股票池...")

	if err := processor.ProcessTradeablePool(s.ctx); err != nil {
		log.Printf("[AutoScheduler] 可交易股票池处理失败: %v", err)

		if s.auditSvc != nil {
			s.auditSvc.LogAuditEvent(
				data.AuditEventLiveActivity,
				fmt.Sprintf("可交易股票池处理失败: %v", err),
				"tradeable_pool",
				"process_failed",
				"system",
				"QuantBot",
				"failed",
				map[string]interface{}{
					"error":     err.Error(),
					"timestamp": now.Format(time.RFC3339),
				},
			)
		}
	} else {
		log.Printf("[AutoScheduler] 可交易股票池处理完成")
	}
}

// processCIOIntradayPlanMonitor 盘中定期执行 CIO 投资方案实时监控与决策
func (s *AutoScheduler) processCIOIntradayPlanMonitor() {
	s.mu.Lock()
	now := time.Now()
	// 检查是否需要处理（避免过于频繁）
	if !s.lastCIOIntradayMonitorTime.IsZero() && now.Sub(s.lastCIOIntradayMonitorTime) < s.cioIntradayMonitorInterval {
		s.mu.Unlock()
		return
	}
	s.lastCIOIntradayMonitorTime = now
	processor := s.cioIntradayPlanProcessor
	s.mu.Unlock()

	if processor == nil {
		return
	}

	log.Printf("[AutoScheduler] 盘中 CIO 投资方案实时监控...")

	// 盘中实时监控与建仓决策会拉起实时行情，任何未捕获 panic 都会使整个进程退出。
	// 这里兜底恢复并输出调用栈，避免程序自动退出且便于定位。
	func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[AutoScheduler] CIO 盘中监控 PANIC 已捕获: %v\n%s", r, debug.Stack())
			}
		}()
		if err := processor.MonitorIntradayInvestmentPlan(s.ctx); err != nil {
			log.Printf("[AutoScheduler] CIO 投资方案实时监控失败: %v", err)
			if s.auditSvc != nil {
				s.auditSvc.LogAuditEvent(
					data.AuditEventLiveActivity,
					fmt.Sprintf("CIO 投资方案实时监控失败: %v", err),
					"cio",
					"intraday_plan_monitor_failed",
					"system",
					"QuantBot",
					"failed",
					map[string]interface{}{
						"error":     err.Error(),
						"timestamp": now.Format(time.RFC3339),
					},
				)
			}
		}
	}()
}

// ensureDailyDecisionGenerated 保障当日盘前决策已成功生成
// 场景：程序晚启动错过盘前窗口、或盘前任务因数据/连接原因未执行成功，
// 导致当日 CIO 决策未落库。此时只要尚未到 15:00，就在盘中周期性补跑盘前分析，
// 直至决策落库；15:00 后放弃补跑（不再强行生成当日决策）。
func (s *AutoScheduler) ensureDailyDecisionGenerated() {
	if s.harness == nil {
		return
	}

	now := time.Now()

	// 15:00 后放弃补跑当日盘前决策
	currentMin := now.Hour()*60 + now.Minute()
	if currentMin > decisionEnsureDeadline {
		return
	}

	// 节流：避免每个 30s tick 反复触发补跑导致死循环或重复执行
	// 同时感知跨日：若补跑计数归属上个交易日，则重置当日计数
	s.mu.Lock()
	today := now.Format("2006-01-02")
	if s.decisionEnsureDate != today {
		s.decisionEnsureDate = today
		s.decisionEnsureCount = 0
	}
	if s.lastDecisionEnsureTime.IsZero() {
		s.lastDecisionEnsureTime = now
	} else if now.Sub(s.lastDecisionEnsureTime) < s.decisionEnsureInterval {
		s.mu.Unlock()
		return
	}
	s.lastDecisionEnsureTime = now
	s.mu.Unlock()

	// 今日是否有决策已落库（说明盘前决策已成功生成，无需补跑）
	if s.hasDecisionToday(now) {
		return
	}

	// 当日补跑达到上限则放弃，避免盘中反复触发 LLM 分析导致死循环/资源浪费
	s.mu.Lock()
	if s.decisionEnsureCount >= decisionEnsureMaxRun {
		s.mu.Unlock()
		log.Printf("[AutoScheduler] 今日盘前决策补跑已达到上限 %d 次，停止补跑（15:00 前仍未生成）", decisionEnsureMaxRun)
		return
	}
	s.decisionEnsureCount++
	s.mu.Unlock()

	// 未到截止时刻且今日无决策 → 补跑盘前分析
	log.Printf("[AutoScheduler] 今日盘中尚未生成盘前决策（晚启动或执行失败），距15:00尚有 %d 分钟，补跑盘前分析（第%d次）...", decisionEnsureDeadline-currentMin, s.decisionEnsureCount)
	// 补跑放入独立 goroutine，避免分析长时间阻塞/卡住时影响主调度循环
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[AutoScheduler] 盘前决策补跑 PANIC 已捕获: %v", r)
			}
		}()
		s.runPreMarketAnalysis()
	}()
}

// hasDecisionToday 判断当日是否已有 CIO 决策落库
func (s *AutoScheduler) hasDecisionToday(now time.Time) bool {
	sm := s.harness.GetSQLiteManager()
	if sm == nil {
		return false
	}
	today := now.Format("2006-01-02")
	var count int64
	if err := sm.GetDB().Model(&data.CIODecisionLog{}).
		Where("DATE(timestamp) = ?", today).
		Count(&count).Error; err != nil {
		log.Printf("[AutoScheduler] Failed to check today's decision: %v", err)
		return false
	}
	return count > 0
}

// logIntradayEnd 记录盘中结束
func (s *AutoScheduler) logIntradayEnd() {
	log.Printf("[AutoScheduler] 离开盘中交易时段")
}

// runPostMarketAnalysis 盘后分析
func (s *AutoScheduler) runPostMarketAnalysis() {
	if s.cycleRunning {
		log.Println("[AutoScheduler] 分析周期正在运行中，跳过盘后分析")
		return
	}

	s.mu.Lock()
	s.cycleRunning = true
	s.totalRuns++
	s.lastRunTime = time.Now()
	s.mu.Unlock()

	log.Printf("[AutoScheduler] ========== 开始盘后智能体分析 ==========")

	s.logPhaseStart("POST_MARKET", "盘后智能体分析")

	result, err := s.harness.RunDailyCycleWithMode(s.ctx, func(phase string, progress float64, message string) {
		log.Printf("[AutoScheduler] 盘后进度: %s %.0f%% %s", phase, progress*100, message)
	})

	s.mu.Lock()
	s.cycleRunning = false
	s.mu.Unlock()

	// 主分析周期失败也照常推进结算/盘口摘要/每日复盘：单点失败不中断后续盘后任务。
	cycleErr := err
	if cycleErr != nil {
		log.Printf("[AutoScheduler] 盘后分析主周期失败(继续执行落库/复盘): %v", cycleErr)
		s.mu.Lock()
		s.failedRuns++
		s.mu.Unlock()
		s.logPhaseEnd("POST_MARKET", false, cycleErr.Error(), nil)
	} else {
		s.mu.Lock()
		s.successfulRuns++
		s.mu.Unlock()

		if result != nil {
			s.logPhaseEnd("POST_MARKET", true, "盘后分析完成", map[string]interface{}{
				"date":             result.Date,
				"errors":           result.Errors,
				"agent_count":      len(result.AgentSessions),
				"trajectory_count": len(result.Trajectories),
			})
			s.logAgentTrajectories(result)
		}
	}

	log.Printf("[AutoScheduler] ========== 盘后分析完成 ==========")

	// 每日结算：将当日总资产/累计盈亏/今日盈亏写入数据库
	// 若缺失，次日 今日盈亏 将错误地等同累计盈亏（用户反馈的 +8.69万 bug 根因）
	s.runDailySettlement()

	// 盘口日频摘要落库：将可交易股票池当日盘口（涨跌停/量比/内外盘/封单）写入 order_book_daily
	s.runOrderBookDaily()

	// 触发每日复盘：复盘正文读取最近一次「深度复盘与制定明日计划」的内容，故维持 15:00 生成
	s.generateDailyReview()
}

// runOrderBookDaily 执行盘口日频摘要落库
func (s *AutoScheduler) runOrderBookDaily() {
	s.mu.RLock()
	processor := s.orderBookDailyProcessor
	s.mu.RUnlock()
	if processor == nil {
		log.Println("[AutoScheduler] 盘口日频摘要处理器未设置，跳过落库")
		return
	}
	if err := processor.RecordOrderBookDaily(s.ctx); err != nil {
		log.Printf("[AutoScheduler] 盘口日频摘要落库失败: %v", err)
		return
	}
	log.Println("[AutoScheduler] 盘口日频摘要落库成功")
}

// runDailySettlement 执行每日结算
func (s *AutoScheduler) runDailySettlement() {
	s.mu.RLock()
	processor := s.dailySettlementProcessor
	s.mu.RUnlock()
	if processor == nil {
		log.Println("[AutoScheduler] 每日结算处理器未设置，跳过结算记录")
		return
	}
	if err := processor.RecordDailySettlement(s.ctx); err != nil {
		log.Printf("[AutoScheduler] 每日结算记录失败: %v", err)
		return
	}
	log.Println("[AutoScheduler] 每日结算记录成功")
}

// generateDailyReview 生成每日复盘报告
func (s *AutoScheduler) generateDailyReview() {
	s.mu.RLock()
	processor := s.dailyReviewProcessor
	lastDate := s.lastDailyReviewDate
	s.mu.RUnlock()

	if processor == nil {
		log.Println("[AutoScheduler] 每日复盘处理器未设置，跳过每日复盘")
		return
	}

	today := time.Now().Format("2006-01-02")
	if lastDate == today {
		log.Printf("[AutoScheduler] 今日复盘已生成，跳过: %s", today)
		return
	}

	log.Printf("[AutoScheduler] ========== 开始每日复盘 ==========")

	if err := processor.TriggerDailyReview(s.ctx); err != nil {
		log.Printf("[AutoScheduler] 每日复盘生成失败: %v", err)
		if s.auditSvc != nil {
			s.auditSvc.LogAuditEvent(
				data.AuditEventLiveActivity,
				"每日复盘生成失败",
				"cio",
				"daily_review_failed",
				"system",
				"QuantBot",
				"failed",
				map[string]interface{}{
					"error": err.Error(),
					"date":  today,
				},
			)
		}
		return
	}

	s.mu.Lock()
	s.lastDailyReviewDate = today
	s.mu.Unlock()

	s.persistSchedulerState()

	log.Printf("[AutoScheduler] ========== 每日复盘完成: %s ==========", today)

	if s.auditSvc != nil {
		s.auditSvc.LogAuditEvent(
			data.AuditEventLiveActivity,
			"每日复盘报告已生成",
			"cio",
			"daily_review_completed",
			"system",
			"QuantBot",
			"success",
			map[string]interface{}{
				"date":      today,
				"timestamp": time.Now().Format(time.RFC3339),
			},
		)
	}
}

// rebuildInvestmentPlanDay 每日自动重建投资方案股票池（幂等：同一交易日仅执行一次）。
// 在复盘阶段调用，用最新全市场真实因子数据重建当前方案的选股与持仓并落库。
func (s *AutoScheduler) rebuildInvestmentPlanDay() {
	s.mu.RLock()
	processor := s.planRefreshProcessor
	lastDate := s.lastPlanRefreshDate
	s.mu.RUnlock()

	if processor == nil {
		log.Println("[AutoScheduler] 投资方案自动重建处理器未设置，跳过每日重建方案")
		return
	}

	today := time.Now().Format("2006-01-02")
	if lastDate == today {
		log.Printf("[AutoScheduler] 今日投资方案已重建，跳过: %s", today)
		return
	}

	log.Printf("[AutoScheduler] ========== 开始每日自动重建投资方案 ==========")
	if err := processor.RebuildInvestmentPlan(s.ctx); err != nil {
		log.Printf("[AutoScheduler] 每日自动重建投资方案失败: %v", err)
		if s.auditSvc != nil {
			s.auditSvc.LogAuditEvent(
				data.AuditEventLiveActivity,
				"每日自动重建投资方案失败",
				"planner",
				"plan_refresh_failed",
				"system",
				"QuantBot",
				"failed",
				map[string]interface{}{
					"error": err.Error(),
					"date":  today,
				},
			)
		}
		return
	}

	s.mu.Lock()
	s.lastPlanRefreshDate = today
	s.mu.Unlock()

	s.persistSchedulerState()

	log.Printf("[AutoScheduler] ========== 每日自动重建投资方案完成: %s ==========", today)
	if s.auditSvc != nil {
		s.auditSvc.LogAuditEvent(
			data.AuditEventLiveActivity,
			"投资方案股票池已自动重建",
			"planner",
			"plan_refresh_completed",
			"system",
			"QuantBot",
			"success",
			map[string]interface{}{
				"date":      today,
				"timestamp": time.Now().Format(time.RFC3339),
			},
		)
	}
}

// persistSchedulerState 把当日已执行的调度状态持久化到 config.json，
// 使程序重启后能正确恢复，避免在当天重复执行每日复盘与投资方案重建。
func (s *AutoScheduler) persistSchedulerState() {
	if s.harness == nil {
		return
	}
	s.mu.RLock()
	reviewDate := s.lastDailyReviewDate
	planDate := s.lastPlanRefreshDate
	s.mu.RUnlock()

	if err := s.harness.UpdateConfig(func(cfg *config.AppConfig) {
		cfg.LastDailyReviewDate = reviewDate
		cfg.LastPlanRefreshDate = planDate
	}); err != nil {
		log.Printf("[AutoScheduler] 持久化调度状态失败: %v", err)
	}
}

// logPhaseStart 记录阶段开始
func (s *AutoScheduler) logPhaseStart(phase, action string) {
	if s.auditSvc == nil {
		return
	}

	s.auditSvc.LogAuditEvent(
		data.AuditEventLiveActivity,
		fmt.Sprintf("智能体工作阶段开始: %s", action),
		"agents",
		phase,
		"system",
		"QuantBot",
		"success",
		map[string]interface{}{
			"phase":     phase,
			"action":    action,
			"timestamp": time.Now().Format(time.RFC3339),
		},
	)
}

// logPhaseEnd 记录阶段结束
func (s *AutoScheduler) logPhaseEnd(phase string, success bool, message string, details interface{}) {
	if s.auditSvc == nil {
		return
	}

	result := "success"
	if !success {
		result = "failed"
	}

	detailsMap := map[string]interface{}{
		"phase":     phase,
		"success":   success,
		"message":   message,
		"timestamp": time.Now().Format(time.RFC3339),
	}

	if details != nil {
		detailsMap["details"] = details
	}

	s.auditSvc.LogAuditEvent(
		data.AuditEventLiveActivity,
		fmt.Sprintf("智能体工作阶段完成: %s (%s)", phase, result),
		"agents",
		phase,
		"system",
		"QuantBot",
		result,
		detailsMap,
	)
}

// logAgentTrajectories 记录智能体轨迹
func (s *AutoScheduler) logAgentTrajectories(result interface{}) {
	if s.auditSvc == nil || result == nil {
		return
	}

	type cycleResult struct {
		AgentSessions map[string]interface{}
		Trajectories  map[string]interface{}
	}

	if r, ok := result.(*interface{}); ok {
		_ = r
		return
	}

	// 尝试获取智能体会话信息
	type sessionInfo struct {
		Confidence float64
		TokenUsage struct {
			TotalTokens int
		}
		Decision interface{}
	}

	// 由于无法直接访问具体类型，这里通过通用方式记录
	if s.auditSvc != nil {
		s.auditSvc.LogAuditEvent(
			data.AuditEventAIAnalysis,
			"智能体分析完成",
			"agent",
			"multi_agent",
			"system",
			"QuantBot",
			"success",
			map[string]interface{}{
				"timestamp": time.Now().Format(time.RFC3339),
				"note":      "详细轨迹信息请查看系统日志",
			},
		)
	}
}
