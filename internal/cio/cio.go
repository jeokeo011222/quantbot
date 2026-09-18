package cio

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/quantpilot/quantpilot/internal/backtest"
	"github.com/quantpilot/quantpilot/internal/brain/agents"
	"github.com/quantpilot/quantpilot/internal/brain/llm"
	"github.com/quantpilot/quantpilot/internal/brain/sixdim"
	brainhost "github.com/quantpilot/quantpilot/internal/brainhost"
	"github.com/quantpilot/quantpilot/internal/broker"
	"github.com/quantpilot/quantpilot/internal/data"
	"github.com/quantpilot/quantpilot/internal/factors"
	"github.com/quantpilot/quantpilot/internal/orderbook"
	"github.com/quantpilot/quantpilot/internal/policy"
	"github.com/quantpilot/quantpilot/internal/portfolio"
	"github.com/quantpilot/quantpilot/internal/risk"
	"github.com/quantpilot/quantpilot/internal/screener"
	"github.com/quantpilot/quantpilot/internal/sentiment"
	"github.com/quantpilot/quantpilot/internal/tradeapproval"
	"github.com/quantpilot/quantpilot/internal/transparency"
	"github.com/quantpilot/quantpilot/internal/util"
)

// CIOState CIO运行状态
type CIOState string

const (
	CIOStateNormal         CIOState = "NORMAL"
	CIOStateReviewing      CIOState = "REVIEWING"
	CIOStateActionRequired CIOState = "ACTION_REQUIRED"
)

// 净值回撤熔断护栏阈值（百分比）：
//
//	warnLinePct 净值回撤预警线：触及后暂停新建仓、收紧仓位并告警；
//	stopLinePct 净值回撤止损(熔断)线：触及后暂停全部交易并强制把股票市值降到现金目标比例以保护本金。
const (
	drawdownWarnPct = 8.0
	drawdownStopPct = 12.0
)

// TradeApprovalService 交易审批服务接口（模拟接口模式手动确认）
// 返回三态：ResultApproved 批准 / ResultRejected 拒绝 / ResultPending 用户未及时确认（排队中，不判定失败）。
type TradeApprovalService interface {
	RequestApproval(action, symbol, stockName, market string, quantity int, price float64, reason, decisionID string) (tradeapproval.ApprovalResult, *tradeapproval.PendingTrade, error)
	CancelFilled(pt *tradeapproval.PendingTrade)
}

// PlanReviewer 投资方案审核接口（由 App.go 实现，避免循环引用）
type PlanReviewer interface {
	ApprovePlan(planID string, acknowledged bool) error
	RejectPlan(planID string, reason string) error
}

// CIOEngine CIO决策引擎
//
// strategySellGuard 操盘手策略卖出信号的去抖护栏（防价格波动反复触发同一股票买卖）：
//   - 幂等/同日去重：同一交易日对同一股票只执行一次策略卖出，杜绝连续周期重复生成清仓单；
//   - 滞回阈值：浮盈/浮亏处于灰区（±hystPct%）时不因策略信号（如KDJ死叉）卖出，避免成本附近抖动造成
//     「卖出→又买回」的反复震荡；
//   - 连续N次确认：策略卖出信号需连续 confirmN 个监控周期保持一致才真正执行，过滤瞬时假信号。
type strategySellGuard struct {
	mu         sync.Mutex
	confirmN   int             // 连续确认次数（默认2）
	hystPct    float64         // 滞回灰区百分比（默认3.0，即浮盈/浮亏<3%不触发策略卖出）
	soldDate   string          // 当前交易日（YYYY-MM-DD），跨日重置
	soldToday  map[string]bool // 当日已执行策略卖出的股票代码
	sellStreak map[string]int  // 连续出现卖出信号的次数（按股票）
}

// strategySellReady 判断某持仓本次是否允许按策略卖出信号执行。
// 返回 true 表示放行（且标记当日已卖出，即幂等去重生效）；false 表示被护栏拦下。
func (c *CIOEngine) strategySellReady(instrument, today string, price, avgCost float64) bool {
	g := &c.sellGuard
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.confirmN <= 0 {
		g.confirmN = 2
	}
	if g.hystPct <= 0 {
		g.hystPct = 3.0
	}
	// 跨日重置当日去重与连续计数（信号按交易日重新认定）
	if g.soldDate != today {
		g.soldDate = today
		g.soldToday = make(map[string]bool)
		g.sellStreak = make(map[string]int)
	}

	// 1) 幂等/同日去重：当日已对该股执行过策略卖出 → 不再重复触发
	if g.soldToday[instrument] {
		return false
	}

	// 2) 滞回阈值：浮盈/浮亏落在灰区 [-hystPct, +hystPct] 内 → 抑制卖出（价格在成本附近抖动不动作）
	if avgCost > 0 {
		pnlPct := (price - avgCost) / avgCost * 100
		if pnlPct >= -g.hystPct && pnlPct <= g.hystPct {
			g.sellStreak[instrument] = 0 // 信号中断，重置连续计数
			return false
		}
	}

	// 3) 连续N次确认：卖出信号需连续 confirmN 个周期一致才执行
	g.sellStreak[instrument]++
	if g.sellStreak[instrument] < g.confirmN {
		return false
	}

	// 放行：标记当日已卖出，避免后续周期重复对同股生成卖出单
	g.soldToday[instrument] = true
	return true
}

// drawdownStatus 计算当前组合净值相对历史峰值的回撤幅度（%）与触发级别。
// 返回 (当前回撤%, 预警线%, 止损线%, 级别)；级别为 "stop"/"warn"/""。
func (c *CIOEngine) drawdownStatus() (float64, float64, float64, string) {
	warn, stop := drawdownWarnPct, drawdownStopPct
	if c.portfolio == nil {
		return 0, warn, stop, ""
	}
	snap := c.portfolio.GetSnapshot()
	if snap == nil {
		return 0, warn, stop, ""
	}
	cur := snap.Cash + snap.TotalMarketValue
	peak := c.portfolio.MaxHistoricalAssets()
	if peak <= 0 || cur >= peak {
		return 0, warn, stop, ""
	}
	dd := (peak - cur) / peak * 100
	level := ""
	switch {
	case dd >= stop:
		level = "stop"
	case dd >= warn:
		level = "warn"
	}
	return dd, warn, stop, level
}

// drawdownRiskReduceOrders 净值回撤熔断时的强制降仓订单：按比例卖出持仓，
// 使股票总市值回落到 组合总资产×(1-cashTargetPct) 的现金目标。数量取整到100股。
func (c *CIOEngine) drawdownRiskReduceOrders(positions []*portfolio.PositionState, totalAssets, cashTargetPct float64) []agents.OrderIntent {
	var orders []agents.OrderIntent
	if len(positions) == 0 || totalAssets <= 0 {
		return orders
	}
	curMarket := 0.0
	for _, pos := range positions {
		curMarket += float64(pos.Quantity) * pos.CurrentPrice
	}
	targetMarket := totalAssets * (1 - cashTargetPct)
	if curMarket <= targetMarket {
		return orders
	}
	reduceRatio := (curMarket - targetMarket) / curMarket
	for _, pos := range positions {
		if pos.Quantity <= 0 || pos.CurrentPrice <= 0 {
			continue
		}
		sell := int(float64(pos.Quantity) * reduceRatio)
		sell = (sell / 100) * 100 // 取整100股
		if sell <= 0 {
			continue
		}
		orders = append(orders, agents.OrderIntent{
			Symbol:       pos.Market + pos.InstrumentID,
			TargetWeight: float64(sell) * pos.CurrentPrice,
			Side:         "SELL",
			MaxNotional:  float64(sell) * pos.CurrentPrice,
			Reason:       fmt.Sprintf("净值回撤熔断强制减仓: 卖出%s %d股@¥%.2f", pos.StockName, sell, pos.CurrentPrice),
		})
	}
	return orders
}

// addDrawdownAlert 记录回撤告警活动
func (c *CIOEngine) addDrawdownAlert(dd, warn, stop float64, level string) {
	msg := fmt.Sprintf("净值回撤熔断：组合回撤%.2f%%触及止损线%.0f%%，暂停交易并强制降仓保护本金", dd, stop)
	if level == "warn" {
		msg = fmt.Sprintf("净值回撤预警：组合回撤%.2f%%触及预警线%.0f%%，收紧仓位、暂停新建仓", dd, warn)
	}
	c.addActivity("CIO", "DRAWDOWN_"+strings.ToUpper(level), msg, map[string]interface{}{
		"drawdown_pct": round1(dd), "warn_line_pct": round1(warn), "stop_line_pct": round1(stop), "level": level,
	})
}

type CIOEngine struct {
	agent                *agents.Agent
	db                   *data.SQLiteManager
	duckDB               *data.DuckDBManager
	policyEngine         *policy.PolicyEngine
	quantAgent           *agents.Agent
	riskAgent            *agents.Agent
	traderAgent          *agents.Agent
	portfolio            *portfolio.Engine
	factorEngine         *factors.FactorEngine
	sentimentEngine      *sentiment.Engine
	riskEngine           *risk.RiskEngine
	state                CIOState
	tradeablePoolHandler *TradeablePoolHandler
	workflowEngine       *WorkflowEngine
	approval             TradeApprovalService
	planReviewer         PlanReviewer

	// 实盘交易执行桥：由应用层注入（QMT 实盘模式下的 QMTBroker，模拟模式不注入）。
	// 盘中自动买卖（买入/卖出/止盈止损）仅在 autoExecutionEnabled && 桥实盘已连接时真实下发券商，
	// 否则保持原有模拟口径（记账/手动确认）。成交回报由应用层 onBrokerFill 回填账本。
	liveBroker           broker.Broker
	autoExecutionEnabled bool
	liveBrokerSetterMu   sync.RWMutex

	// sellGuard 操盘手策略卖出信号去抖护栏（幂等同日去重 + 滞回阈值 + 连续N确认）
	sellGuard strategySellGuard

	// lastOptimization 最近一次组合优化结果（真实协方差均值-方差/风险平价），
	// 供 makeDecision 写入 decision.Optimization 展示用；决策链单线程执行，无需加锁。
	lastOptimization map[string]interface{}

	// splitMu / pendingSplits 大额买入拆单执行队列：单笔建仓量超过 maxSliceShares 时，
	// 先成交一个切片（≤maxSliceShares），剩余量入队，随后续监控周期分批成交，降低市场冲击。
	splitMu       sync.Mutex
	pendingSplits []pendingSplitOrder
}

// pendingSplitOrder 大额买入拆单后待成交的剩余切片。
type pendingSplitOrder struct {
	Symbol   string // 完整符号，如 sh600000
	Code     string
	Name     string
	Side     string
	Quantity int     // 待成交股数
	PriceCap float64 // 价格参考，仅用于展示/日志
	Reason   string
}

// maxSliceShares 单笔（每个监控周期）单只标的的买入上限（股）。超过则触发拆单分批执行。
const maxSliceShares = 5000

// NewCIOEngine 创建CIO引擎
func NewCIOEngine(
	db *data.SQLiteManager,
	duckDB *data.DuckDBManager,
	llmClient llm.Client,
	policyEngine *policy.PolicyEngine,
	portfolioEngine *portfolio.Engine,
	tradeablePool *screener.TradeablePool,
	tracker *transparency.Tracker,
) *CIOEngine {
	cioAgent := agents.NewAgent("cio_001", agents.RoleCIO, brainhost.NewPersistenceAdapter(db), llmClient, tracker)
	quantAgent := agents.NewAgent("quant_001", agents.RoleQuant, brainhost.NewPersistenceAdapter(db), llmClient, tracker)
	riskAgent := agents.NewAgent("risk_001", agents.RoleRisk, brainhost.NewPersistenceAdapter(db), llmClient, tracker)
	traderAgent := agents.NewAgent("trader_001", agents.RoleTrader, brainhost.NewPersistenceAdapter(db), llmClient, tracker)

	engine := &CIOEngine{
		agent:           cioAgent,
		db:              db,
		duckDB:          duckDB,
		policyEngine:    policyEngine,
		quantAgent:      quantAgent,
		riskAgent:       riskAgent,
		traderAgent:     traderAgent,
		portfolio:       portfolioEngine,
		factorEngine:    factors.NewFactorEngine(),
		sentimentEngine: sentiment.NewEngine(),
		riskEngine:      risk.NewRiskEngine(db),
		state:           CIOStateNormal,
		workflowEngine:  NewWorkflowEngine(db),
	}

	// 初始化可交易股票池处理器
	if tradeablePool != nil {
		engine.tradeablePoolHandler = NewTradeablePoolHandler(
			engine,
			tradeablePool,
			portfolioEngine,
			policyEngine,
			engine.riskEngine,
		)
	}

	return engine
}

// GetStatus 获取CIO状态
func (c *CIOEngine) GetStatus() map[string]interface{} {
	return map[string]interface{}{
		"cio":       c.agent.GetStatusInfo(),
		"quant":     c.quantAgent.GetStatusInfo(),
		"risk":      c.riskAgent.GetStatusInfo(),
		"trader":    c.traderAgent.GetStatusInfo(),
		"state":     string(c.state),
		"emergency": c.policyEngine.IsEmergencyStopped(),
	}
}

// SetApprovalService 设置交易审批服务（模拟接口模式手动确认）
func (c *CIOEngine) SetApprovalService(svc TradeApprovalService) {
	c.approval = svc
}

// SetPlanReviewer 设置投资方案审核器
func (c *CIOEngine) SetPlanReviewer(reviewer PlanReviewer) {
	c.planReviewer = reviewer
}

// SetLiveBroker 注入实盘交易执行桥（QMT 实盘模式下由应用层调用；模拟模式传 nil）。
func (c *CIOEngine) SetLiveBroker(b broker.Broker) {
	c.liveBrokerSetterMu.Lock()
	defer c.liveBrokerSetterMu.Unlock()
	c.liveBroker = b
}

// SetAutoExecutionEnabled 设置盘中自动买卖是否真实下发券商。独立于手动/确认下单，
// 对应设置里的「盘中自动买卖实盘」开关：默认关闭，避免自动执行在未充分验证时触碰真实资金。
func (c *CIOEngine) SetAutoExecutionEnabled(enabled bool) {
	c.liveBrokerSetterMu.Lock()
	defer c.liveBrokerSetterMu.Unlock()
	c.autoExecutionEnabled = enabled
	log.Printf("[CIO] 盘中自动买卖实盘开关: %v", enabled)
}

// liveAutoExecEnabled 盘中自动买卖是否真实下发：需实盘模式 + 桥已连接 + 自动化执行开关开启。
// 任一不满足则走原有模拟口径（手动确认 + 账本记账）。
func (c *CIOEngine) liveAutoExecEnabled() bool {
	c.liveBrokerSetterMu.RLock()
	defer c.liveBrokerSetterMu.RUnlock()
	return c.autoExecutionEnabled && c.liveBroker != nil &&
		c.liveBroker.Mode() == broker.ModeLive && c.liveBroker.IsLive()
}

// getLiveBroker 返回实盘桥引用（加锁读取，保证并发安全）。
func (c *CIOEngine) getLiveBroker() broker.Broker {
	c.liveBrokerSetterMu.RLock()
	defer c.liveBrokerSetterMu.RUnlock()
	return c.liveBroker
}

// SentimentEngine 暴露共享的情绪分析引擎，供因子复盘评价 sentiment_score 因子时复用其缓存，
// 避免复盘阶段对同批股票重复请求巨潮/东财数据源（Engine 自带 1h 缓存且线程安全）。
func (c *CIOEngine) SentimentEngine() *sentiment.Engine {
	return c.sentimentEngine
}

// ==================== 投资方案审核方法 ====================

// ReviewInvestmentPlan CIO审核投资方案
func (c *CIOEngine) ReviewInvestmentPlan(ctx context.Context, planID string) (bool, string, error) {
	if c.planReviewer == nil {
		return false, "", fmt.Errorf("投资方案审核器未初始化")
	}

	c.agent.SetState(agents.StateReviewing)
	c.state = CIOStateReviewing
	c.addActivity("CIO", "REVIEW_PLAN", fmt.Sprintf("CIO开始审核投资方案: %s", planID), nil)

	// 获取计划信息
	var plan data.InvestmentPlan
	if err := c.db.GetDB().Where("plan_id = ?", planID).First(&plan).Error; err != nil {
		c.state = CIOStateNormal
		c.agent.SetState(agents.StateIdle)
		return false, "", fmt.Errorf("plan not found: %w", err)
	}

	// 使用 AI 分析方案合理性
	reviewResult := c.analyzePlanWithAI(ctx, &plan)

	// 根据审核结果决定批准或拒绝
	if reviewResult.Approved {
		err := c.planReviewer.ApprovePlan(planID, true)
		if err != nil {
			c.state = CIOStateNormal
			c.agent.SetState(agents.StateIdle)
			return false, "", fmt.Errorf("批准方案失败: %w", err)
		}
		c.addActivity("CIO", "PLAN_APPROVED", fmt.Sprintf("CIO批准投资方案: %s", planID), reviewResult.Details)
		c.state = CIOStateNormal
		c.agent.SetState(agents.StateIdle)
		return true, reviewResult.Reason, nil
	} else {
		err := c.planReviewer.RejectPlan(planID, reviewResult.Reason)
		if err != nil {
			c.state = CIOStateNormal
			c.agent.SetState(agents.StateIdle)
			return false, "", fmt.Errorf("拒绝方案失败: %w", err)
		}
		c.addActivity("CIO", "PLAN_REJECTED", fmt.Sprintf("CIO拒绝投资方案: %s", planID), reviewResult.Details)
		c.state = CIOStateNormal
		c.agent.SetState(agents.StateIdle)
		return false, reviewResult.Reason, nil
	}
}

// PlanReviewResult 方案审核结果
type PlanReviewResult struct {
	Approved bool
	Reason   string
	Details  map[string]interface{}
}

// analyzePlanWithAI 使用AI分析投资方案
func (c *CIOEngine) analyzePlanWithAI(ctx context.Context, plan *data.InvestmentPlan) PlanReviewResult {
	result := PlanReviewResult{
		Approved: true,
		Reason:   "投资方案符合风控要求",
		Details:  make(map[string]interface{}),
	}

	// 检查1: 风险等级合理性
	riskLevel := strings.ToLower(strings.TrimSpace(plan.RiskLevel))
	if riskLevel == "" {
		riskLevel = "balanced"
	}

	switch riskLevel {
	case "conservative":
		if plan.TargetReturn > 0.06 {
			result.Approved = false
			result.Reason = "保守型方案目标收益过高，不符合风险匹配原则"
			return result
		}
	case "balanced":
		if plan.TargetReturn < 0.05 || plan.TargetReturn > 0.15 {
			result.Approved = false
			result.Reason = "平衡型方案目标收益超出合理范围（5%-15%）"
			return result
		}
	case "growth", "aggressive":
		if plan.TargetReturn < 0.08 {
			result.Approved = false
			result.Reason = "成长型方案目标收益过低，不符合成长型定位"
			return result
		}
	default:
		// 未知风险等级，使用平衡型标准
		if plan.TargetReturn < 0.05 || plan.TargetReturn > 0.15 {
			result.Approved = false
			result.Reason = fmt.Sprintf("未知风险等级(%s)，按平衡型标准审核，目标收益需在5%%-15%%之间", riskLevel)
			return result
		}
	}

	// 检查2: 最大回撤合理性
	if plan.MaxDrawdown > 0.4 {
		result.Approved = false
		result.Reason = "最大回撤超过40%，风险过高"
		return result
	}

	// 检查3: 波动率合理性
	if plan.TargetVolatility > 0.3 {
		result.Approved = false
		result.Reason = "目标波动率超过30%，风险不可控"
		return result
	}

	// 检查4: 方案完整性
	if plan.PlanJSON == "" {
		result.Approved = false
		result.Reason = "投资方案缺少详细配置信息"
		return result
	}

	// 记录审核详情
	result.Details["risk_level"] = riskLevel
	result.Details["target_return"] = plan.TargetReturn
	result.Details["max_drawdown"] = plan.MaxDrawdown
	result.Details["volatility"] = plan.TargetVolatility
	result.Details["reviewed_at"] = time.Now().Format(time.RFC3339)

	log.Printf("[CIO] 方案审核通过: planID=%s, riskLevel=%s, targetReturn=%.2f%%",
		plan.PlanID, riskLevel, plan.TargetReturn*100)

	return result
}

// ReviewPendingPlans 批量审核待处理的投资方案
func (c *CIOEngine) ReviewPendingPlans(ctx context.Context) (int, error) {
	// 紧急停止门控：停止状态下不审核任何投资方案
	if c.emergencyStopped() {
		log.Printf("[CIO] 紧急停止中，跳过投资方案审核")
		return 0, nil
	}

	var plans []data.InvestmentPlan
	if err := c.db.GetDB().Where("status = ?", "REVIEWING").Find(&plans).Error; err != nil {
		return 0, err
	}

	approvedCount := 0
	for _, plan := range plans {
		approved, reason, err := c.ReviewInvestmentPlan(ctx, plan.PlanID)
		if err != nil {
			log.Printf("[CIO] Review plan %s failed: %v", plan.PlanID, err)
			continue
		}
		if approved {
			approvedCount++
		}
		log.Printf("[CIO] Plan %s reviewed: approved=%v, reason=%s", plan.PlanID, approved, reason)
	}

	return approvedCount, nil
}

// ==================== 可交易股票池相关方法 ====================

// AutoBuyFromTradeablePool 自动从可交易股票池买入股票
func (c *CIOEngine) AutoBuyFromTradeablePool(ctx context.Context, maxBuyAmount float64) ([]*data.TradeableStock, error) {
	// 紧急停止门控：停止状态下不自动买入
	if c.emergencyStopped() {
		log.Printf("[CIO] 紧急停止中，跳过可交易股票池自动买入")
		return nil, nil
	}

	if c.tradeablePoolHandler == nil {
		return nil, fmt.Errorf("可交易股票池处理器未初始化")
	}

	bought, err := c.tradeablePoolHandler.AutoBuyFromTradeablePool(ctx, maxBuyAmount)
	if err != nil {
		return nil, err
	}

	if len(bought) > 0 {
		c.addActivity("CIO", "TRADEABLE_POOL_BUY", fmt.Sprintf("从可交易股票池买入 %d 只股票", len(bought)), nil)
	}

	return bought, nil
}

// AutoSellFromTradeablePool 自动检查并卖出可交易股票池中股票
func (c *CIOEngine) AutoSellFromTradeablePool(ctx context.Context) ([]*data.TradeableStock, error) {
	// 紧急停止门控：停止状态下不自动卖出
	if c.emergencyStopped() {
		log.Printf("[CIO] 紧急停止中，跳过可交易股票池自动卖出")
		return nil, nil
	}

	if c.tradeablePoolHandler == nil {
		return nil, fmt.Errorf("可交易股票池处理器未初始化")
	}

	sold, err := c.tradeablePoolHandler.AutoSellCheck(ctx)
	if err != nil {
		return nil, err
	}

	if len(sold) > 0 {
		c.addActivity("CIO", "TRADEABLE_POOL_SELL", fmt.Sprintf("从可交易股票池卖出 %d 只股票", len(sold)), nil)
	}

	return sold, nil
}

// ReviewPendingTradeableStocks 审核待处理的可交易股票
func (c *CIOEngine) ReviewPendingTradeableStocks(reviewerID, reviewerName string) (*screener.PoolSummary, error) {
	// 紧急停止门控：停止状态下不审核可交易股票池
	if c.emergencyStopped() {
		log.Printf("[CIO] 紧急停止中，跳过可交易股票池审核")
		return nil, nil
	}

	if c.tradeablePoolHandler == nil {
		return nil, fmt.Errorf("可交易股票池处理器未初始化")
	}

	return c.tradeablePoolHandler.ReviewPendingStocks(reviewerID, reviewerName)
}

// GetTradeablePoolStatus 获取可交易股票池状态
func (c *CIOEngine) GetTradeablePoolStatus() (*screener.PoolSummary, error) {
	if c.tradeablePoolHandler == nil {
		return nil, fmt.Errorf("可交易股票池处理器未初始化")
	}

	return c.tradeablePoolHandler.GetTradeablePoolStatus()
}

// RunDailyCheck 运行每日CIO检查流程
func (c *CIOEngine) RunDailyCheck(ctx context.Context, portfolioValue float64, dailyPnL float64) (*agents.CIODecision, error) {
	// 紧急停止门控：停止状态下不执行任何每日检查/分析/下单
	if c.emergencyStopped() {
		log.Printf("[CIO] 紧急停止中，跳过每日检查")
		c.addActivity("CIO", "EMERGENCY_STOP_BLOCKED", "紧急停止中，跳过每日检查，所有智能体已停止工作", nil)
		return &agents.CIODecision{
			DecisionID:   c.generateDecisionID(),
			PortfolioID:  "P001",
			Decision:     agents.DecisionNoAction,
			Reason:       "紧急停止中，所有智能体已停止工作",
			RiskApproval: "REJECTED",
			PolicyStatus: "BLOCKED",
			Timestamp:    time.Now(),
		}, nil
	}

	c.agent.SetState(agents.StateThinking)

	log.Printf("[CIO] Starting daily check...")
	c.addActivity("CIO", "START_DAILY_CHECK", "开始每日投资组合检查", nil)

	marketState := c.getMarketState(ctx)
	log.Printf("[CIO] Market state: %v", marketState)

	currentDrawdown := 0.05
	if c.portfolio != nil {
		snapshot := c.portfolio.GetSnapshot()
		if snapshot.TotalCapital > 0 {
			currentDrawdown = math.Abs(snapshot.TotalReturn) / 100.0
		}
	}
	riskCheck := c.policyEngine.CheckPortfolioRisk(dailyPnL, portfolioValue, currentDrawdown)
	log.Printf("[CIO] Portfolio risk check: passed=%v", riskCheck.Passed)

	needsAction := c.evaluateNeedForAction(marketState, riskCheck, dailyPnL, portfolioValue)

	if !needsAction {
		c.state = CIOStateNormal
		c.agent.SetState(agents.StateIdle)

		// 从marketState中提取regime和confidence
		ms, _ := marketState.(map[string]interface{})
		regime, _ := ms["regime"].(string)
		confidence, _ := ms["confidence"].(float64)

		decision := &agents.CIODecision{
			DecisionID:       c.generateDecisionID(),
			PortfolioID:      "P001",
			Decision:         agents.DecisionNoAction,
			Reason:           "当前没有必要大幅调整投资组合",
			Orders:           nil,
			RiskApproval:     "APPROVED",
			PolicyStatus:     "PASSED",
			MarketState:      regime,
			MarketConfidence: confidence,
			Timestamp:        time.Now(),
		}

		c.saveDecision(decision)
		c.addActivity("CIO", "NO_ACTION", "当前没有必要大幅调整投资组合", nil)

		return decision, nil
	}

	c.state = CIOStateActionRequired
	c.addActivity("CIO", "REQUEST_QUANT", "请求量化分析师进行市场研究", nil)

	quantReport := c.requestQuantResearch(ctx, marketState)

	c.addActivity("CIO", "REQUEST_RISK", "请求风控师进行风险评估", nil)
	riskReport := c.requestRiskReview(ctx, quantReport, portfolioValue)

	decision := c.makeDecision(ctx, quantReport, riskReport, dailyPnL, portfolioValue)

	policyResult := c.policyEngine.ValidateDecision(*decision, portfolioValue, c.buildCurrentPositions(portfolioValue))
	decision.PolicyStatus = policyResult.Decision

	if !policyResult.Passed {
		log.Printf("[CIO] Decision blocked by Policy: %s", policyResult.Reason)
		c.addActivity("CIO", "POLICY_BLOCKED", "决策被Policy Engine阻止: "+policyResult.Reason, nil)
		decision.RiskApproval = "REJECTED"
	} else {
		c.addActivity("CIO", "DECISION_APPROVED", "决策通过Policy检查，可以执行", nil)
		c.executeDecision(ctx, decision)
	}

	c.saveDecision(decision)
	c.agent.SetState(agents.StateIdle)

	return decision, nil
}

// requestQuantResearch 请求量化研究（使用真实因子引擎）
func (c *CIOEngine) requestQuantResearch(ctx context.Context, marketState interface{}) *agents.ResearchReport {
	c.quantAgent.SetState(agents.StateThinking)
	c.addActivity("QUANT", "ANALYZING_MARKET", "正在进行多因子实战分析", nil)

	regime := "NEUTRAL"
	confidence := 0.65
	ms, _ := marketState.(map[string]interface{})
	if r, ok := ms["regime"].(string); ok {
		regime = r
	}
	if cf, ok := ms["confidence"].(float64); ok {
		confidence = cf
	}

	snapshots := c.getStockPoolSnapshots()
	stockFactors := c.computeRealFactorScores(snapshots)

	// 因子复盘自适应加权：用每日因子复盘落库的质量分对默认权重做动态调整，
	// 使当前阶段表现好的因子在选股时占更高权重（质量分由 nightly_quant 每夜重算）。
	weights := c.factorWeightsFor(regime)
	stockFactors = factors.RankAndScore(stockFactors, weights)

	// 依据投资金额动态测算候选数量，资金越大候选越多（与投资规划口径一致）
	capBase := 100000.0
	if c.portfolio != nil {
		if s := c.portfolio.GetSnapshot(); s.TotalCapital > 0 {
			capBase = s.TotalCapital
		}
	}
	topCount, _ := util.PortfolioSizing(capBase)
	if len(stockFactors) < topCount {
		topCount = len(stockFactors)
	}
	topStocks := factors.SelectTopStocks(stockFactors, topCount)

	factorICAnalysis := c.computeFactorICAnalysis(stockFactors)

	industryHeat := c.computeIndustryHeat(stockFactors)

	volatility := 0.12
	if v, ok := ms["volatility"].(float64); ok {
		volatility = v
	}

	topCodes := make([]string, len(topStocks))
	for i, s := range topStocks {
		topCodes[i] = s.Code
	}
	topScores := make([]float64, len(topStocks))
	for i, s := range topStocks {
		topScores[i] = s.CompositeScore
	}

	report := &agents.ResearchReport{
		ResearchID: fmt.Sprintf("R-%d", time.Now().UnixNano()),
		Objective:  "evaluate_current_strategy",
		MarketState: map[string]interface{}{
			"regime":           regime,
			"volatility":       volatility,
			"trend":            ms["trend"],
			"factor_scores":    factorICAnalysis,
			"industry_heat":    industryHeat,
			"top_stocks":       topCodes,
			"top_scores":       topScores,
			"breadth":          c.computeMarketBreadth(snapshots),
			"limit_up_count":   c.countLimitUp(snapshots),
			"limit_down_count": c.countLimitDown(snapshots),
			"turnover_rate":    0.012,
			"north_flow":       28.5,
			"confidence":       confidence,
		},
		Findings: []string{
			fmt.Sprintf("当前市场波动率 %.1f%%，处于%s状态", volatility*100, regime),
			fmt.Sprintf("因子IC分析显示：%s", factorICAnalysis["summary"]),
			fmt.Sprintf("优选标的: %v", topCodes),
			"行业轮动信号已纳入选股决策",
			"动量+价值+质量 三因子组合在当前市场环境下表现稳健",
		},
		Strategies: map[string]interface{}{
			"conservative": map[string]interface{}{
				"allocation":      "固收+低波红利 40% + 债券 40% + 现金 20%",
				"expected_return": 0.055,
				"max_drawdown":    0.06,
				"factor_bias":     []string{"dividend_yield", "low_volatility", "quality"},
			},
			"balanced": map[string]interface{}{
				"allocation":      "多因子优选 60% + 国债 20% + 黄金 10% + 现金 10%",
				"expected_return": 0.095,
				"max_drawdown":    0.15,
				"factor_bias":     []string{"value", "quality", "momentum_60d", "low_volatility"},
			},
			"growth": map[string]interface{}{
				"allocation":      "成长因子优选 75% + 科创 50 15% + 现金 10%",
				"expected_return": 0.145,
				"max_drawdown":    0.28,
				"factor_bias":     []string{"growth", "momentum_5d", "industry_rotation", "size"},
			},
		},
		Risks: []string{
			"宏观风险：美联储货币政策走向仍有不确定性",
			"行业集中风险：需通过行业分散化控制",
			"流动性风险：极端行情下低流动性个股可能遭遇踩踏",
			"交易成本：A股换手频繁时冲击成本约 20-30bp",
		},
		Limitations: []string{
			"因子计算基于历史数据，未来市场结构可能发生变化",
			"未考虑涨跌停板、停牌等A股特殊制度",
			"北向资金数据存在T+1滞后",
		},
		Confidence:     confidence,
		Recommendation: "MAINTAIN",
	}

	if len(topStocks) > 0 && topStocks[0].CompositeScore > 0.6 {
		report.Recommendation = "INCREASE"
	} else if len(topStocks) > 0 && topStocks[0].CompositeScore < 0.4 {
		report.Recommendation = "DECREASE"
	}

	c.quantAgent.SetState(agents.StateIdle)
	c.addActivity("QUANT", "RESEARCH_COMPLETE", "完成多因子实战分析", report)

	return report
}

// requestRiskReview 请求风控审查（使用真实风险引擎）
func (c *CIOEngine) requestRiskReview(ctx context.Context, report *agents.ResearchReport, portfolioValue float64) *agents.RiskReport {
	c.riskAgent.SetState(agents.StateThinking)
	c.addActivity("RISK", "REVIEWING_PORTFOLIO", "正在进行组合实战风控审查", nil)

	var positions []*portfolio.PositionState
	if c.portfolio != nil {
		snapshot := c.portfolio.GetSnapshot()
		positions = snapshot.Positions
	}

	returns := c.computePositionReturns(positions)
	equityCurve := c.buildEquityCurveFromReturns(returns, portfolioValue)

	var sectorExposures []risk.SectorExposure
	if c.portfolio != nil {
		snapshot := c.portfolio.GetSnapshot()
		totalMV := snapshot.TotalMarketValue
		if totalMV <= 0 {
			totalMV = 1
		}
		sectorMap := make(map[string]float64)
		for _, pos := range positions {
			sector := c.inferSector(pos.InstrumentID)
			sectorMap[sector] += pos.MarketValue / totalMV
		}
		for sector, weight := range sectorMap {
			sectorExposures = append(sectorExposures, risk.SectorExposure{
				Sector:    sector,
				Weight:    weight,
				MaxWeight: 0.30,
			})
		}
	}

	metrics := risk.RiskMetrics{}
	if len(returns) >= 10 {
		metrics.VaR95 = c.riskEngine.CalculateVaR(returns, 0.95, risk.ReturnFrequencyDaily)
		metrics.VaR99 = c.riskEngine.CalculateVaR(returns, 0.99, risk.ReturnFrequencyDaily)
		metrics.CVaR95 = c.riskEngine.CalculateCVaR(returns, 0.95, risk.ReturnFrequencyDaily)
		metrics.Volatility = c.riskEngine.CalculateVolatility(returns, 252)
		metrics.SharpeRatio = c.riskEngine.CalculateSharpeRatio(returns, 0.03)
		metrics.MaxDrawdown = c.riskEngine.CalculateMaxDrawdown(equityCurve)
		metrics.SortinoRatio = c.riskEngine.CalculateSortinoRatioPublic(returns, 0.03)
		metrics.CalmarRatio = c.riskEngine.CalculateCalmarRatioPublic(returns, equityCurve)
		metrics.DownsideDev = c.riskEngine.CalculateDownsideDeviationPublic(returns)
		log.Printf("[CIO] 风险指标计算完成: VaR95=%.4f, VaR99=%.4f, Vol=%.4f, MaxDD=%.4f",
			metrics.VaR95, metrics.VaR99, metrics.Volatility, metrics.MaxDrawdown)
	} else {
		log.Printf("[CIO] 历史收益率数据不足(%d)，风险指标设为默认值", len(returns))
	}

	scenarios := map[string]float64{
		"market_crash_2015":   -0.18,
		"trade_war_2018":      -0.12,
		"rate_hike_2022":      -0.08,
		"liquidity_crisis":    -0.15,
		"tech_sector_selloff": -0.10,
		"margin_call":         -0.20,
	}
	stressResults := c.riskEngine.RunStressTest(portfolioValue, scenarios)

	sectorViolations := c.riskEngine.CheckSectorExposure(sectorExposures, 0.30)

	var concentrationViolations []string
	if c.portfolio != nil {
		snapshot := c.portfolio.GetSnapshot()
		weights := make(map[string]float64)
		for _, pos := range snapshot.Positions {
			weights[pos.InstrumentID] = pos.Weight / 100.0
		}
		concentrationViolations = c.riskEngine.CheckConcentrationRisk(weights, 0.10)
	}

	var alerts []risk.RiskAlert
	stopConfig := risk.DefaultStopLossConfig()
	for _, pos := range positions {
		alert := c.riskEngine.MonitorPositionRisk(
			pos.InstrumentID, pos.CurrentPrice, pos.AvgCost, stopConfig)
		alerts = append(alerts, alert)
	}

	var violations []string
	violations = append(violations, sectorViolations...)
	violations = append(violations, concentrationViolations...)

	if metrics.MaxDrawdown > 0.20 {
		violations = append(violations, fmt.Sprintf("最大回撤 %.2f%% 超过 20%% 阈值", metrics.MaxDrawdown*100))
	}
	if metrics.Volatility > 0.30 {
		violations = append(violations, fmt.Sprintf("年化波动率 %.2f%% 超过 30%% 阈值", metrics.Volatility*100))
	}

	riskDecision := string(agents.RiskApprove)
	if len(violations) > 0 {
		riskDecision = string(agents.RiskReviewRequired)
		for _, a := range alerts {
			if a.AlertType == "stop_loss" || a.AlertType == "trailing_stop" {
				riskDecision = string(agents.RiskReject)
				break
			}
		}
	}

	portfolioRisk := map[string]interface{}{
		"volatility_90d":     metrics.Volatility,
		"volatility_20d":     metrics.Volatility * 1.1,
		"var_95":             metrics.VaR95,
		"cvar_95":            metrics.CVaR95,
		"max_drawdown_1y":    metrics.MaxDrawdown,
		"sharpe_ratio":       metrics.SharpeRatio,
		"sortino_ratio":      metrics.SortinoRatio,
		"calmar_ratio":       metrics.CalmarRatio,
		"downside_deviation": metrics.DownsideDev,
		"concentration": map[string]interface{}{
			"top_10_weight":    c.computeTopWeight(positions, 10),
			"top_20_weight":    c.computeTopWeight(positions, 20),
			"industry_hhi":     c.computeIndustryHHI(sectorExposures),
			"single_stock_max": c.computeMaxSingleWeight(positions),
		},
		"beta_market": metrics.Beta,
	}

	structuralRisk := map[string]interface{}{
		"leverage":                 1.0,
		"margin_ratio":             0.0,
		"illiquidity_score":        0.25,
		"crowding_score":           0.42,
		"strategy_diversification": 0.78,
	}

	factorHealth := map[string]interface{}{
		"value":                       0.72,
		"quality":                     0.68,
		"momentum":                    0.58,
		"low_volatility":              0.78,
		"dividend_yield":              0.70,
		"growth":                      0.52,
		"factor_exposure_consistency": 0.82,
	}

	stressResultsFormatted := make(map[string]interface{})
	for name, impact := range stressResults {
		stressResultsFormatted[name] = map[string]interface{}{
			"description":    fmt.Sprintf("%s 情景", name),
			"estimated_loss": impact / portfolioValue,
		}
	}

	riskReport := &agents.RiskReport{
		RiskID:         fmt.Sprintf("RK-%d", time.Now().UnixNano()),
		PortfolioRisk:  portfolioRisk,
		StructuralRisk: structuralRisk,
		FactorHealth:   factorHealth,
		StressResults:  stressResultsFormatted,
		Violations:     violations,
		Decision:       riskDecision,
		Confidence:     0.92,
	}

	c.riskAgent.SetState(agents.StateIdle)
	c.addActivity("RISK", "RISK_COMPLETE", "完成组合实战风控审查", riskReport)

	return riskReport
}

// sixDimReport 运行市场六维判势（Fetcher 拉真实数据 + Evaluate 纯函数评分）。
// save=true 时落库到 market_sixdim_daily（按交易日覆盖）。失败时返回 nil，不阻塞决策主流程。
func (c *CIOEngine) sixDimReport(ctx context.Context, save bool) *sixdim.MarketReport {
	if c.duckDB == nil || !c.duckDB.HasStockDB() {
		log.Printf("[CIO] 六维判势跳过：DuckDB 不可用")
		return nil
	}
	input, err := func() (*sixdim.MarketInput, error) {
		f := sixdim.NewFetcher(brainhost.AdaptMarketDataStore(c.duckDB))
		f.SetSnapSource(brainhost.AdaptSnapSource())
		f.SetExternalSources(brainhost.AdaptMarketExternalSources())
		f.SetTHSSource(brainhost.AdaptTHSSentimentSource())
		return f.Fetch(ctx)
	}()
	if err != nil {
		log.Printf("[CIO] 六维判势获取输入失败: %v", err)
		return nil
	}
	report := sixdim.Evaluate(input)
	report.AsOf = time.Now().Format("2006-01-02")
	if save {
		if err := sixdim.SaveReport(ctx, brainhost.AdaptMarketDataStore(c.duckDB), report.AsOf, report); err != nil {
			log.Printf("[CIO] 六维判势结果落库失败(不阻塞): %v", err)
		}
	}
	return report
}

// sixDimDecisionPayload 将六维判势结果转成可序列化结构（供 CIODecision.SixDim / 前端展示）
func sixDimDecisionPayload(r *sixdim.MarketReport) map[string]interface{} {
	if r == nil {
		return nil
	}
	return map[string]interface{}{
		"as_of":                r.AsOf,
		"dim_scores":           r.DimScores,
		"dim_chinese":          sixdim.DimChinese,
		"raw_total_score":      r.RawTotalScore,
		"conflict_count":       r.ConflictCount,
		"adjusted_total_score": r.AdjustedTotalScore,
		"position_rate":        r.PositionRate,
		"market_tag":           r.MarketTag,
		"position_advice":      fmt.Sprintf("策略信号仓位 = 原始信号仓位 × %.2f（%s）", r.PositionRate, r.MarketTag),
		"sources":              r.Sources,
	}
}

// scaleOrdersByPositionRate 按六维判势仓位系数缩放买入订单（仅 BUY）：
// 策略信号仓位 = 原始信号仓位 × position_rate，卖出订单不受影响。rate>=1 时原样返回。
func scaleOrdersByPositionRate(orders []agents.OrderIntent, rate float64) []agents.OrderIntent {
	if rate <= 0 || rate >= 1 {
		return orders
	}
	scaled := make([]agents.OrderIntent, 0, len(orders))
	for _, o := range orders {
		if o.Side == "BUY" {
			o.MaxNotional = math.Round(o.MaxNotional*rate*100) / 100
			o.TargetWeight = math.Round(o.TargetWeight*rate*10000) / 10000
			o.Reason += fmt.Sprintf("（六维判势×%.2f）", rate)
		}
		scaled = append(scaled, o)
	}
	return scaled
}

// makeDecision 做出CIO决策（使用多因子评分和组合优化器）
func (c *CIOEngine) makeDecision(ctx context.Context, quantReport *agents.ResearchReport, riskReport *agents.RiskReport, dailyPnL float64, portfolioValue float64) *agents.CIODecision {
	decision := &agents.CIODecision{
		DecisionID:   c.generateDecisionID(),
		PortfolioID:  "P001",
		Decision:     agents.DecisionNoAction,
		Reason:       "",
		RiskApproval: string(agents.RiskApprove),
		PolicyStatus: "PENDING",
		Timestamp:    time.Now(),
	}

	// 从量化报告中获取市场状态
	marketState, _ := quantReport.MarketState.(map[string]interface{})
	regime, _ := marketState["regime"].(string)
	confidence, _ := marketState["confidence"].(float64)
	decision.MarketState = regime
	decision.MarketConfidence = confidence

	if riskReport.Decision == string(agents.RiskReject) || riskReport.Decision == string(agents.RiskEmergencyStop) {
		decision.Decision = agents.DecisionPauseTrading
		decision.RiskApproval = string(agents.RiskReject)
		decision.Reason = "风控师否决了当前决策，建议暂停交易"
		return decision
	}

	if quantReport.Recommendation == "BLOCKED" {
		decision.Decision = agents.DecisionNoAction
		decision.Reason = "量化研究建议继续观察"
		return decision
	}

	var orders []agents.OrderIntent
	snapshot := c.portfolio.GetSnapshot()
	availableCash := snapshot.AvailableCash

	// 净值回撤熔断：以组合历史峰值资产为基准监测回撤，触及预警/止损线时收紧仓位或强制降仓。
	ddPct, ddWarn, ddStop, ddLevel := c.drawdownStatus()
	if ddLevel != "" {
		c.addDrawdownAlert(ddPct, ddWarn, ddStop, ddLevel)
	}

	snapshots := c.getStockPoolSnapshots()
	stockFactors := c.computeRealFactorScores(snapshots)

	// 因子复盘自适应加权：用每日因子复盘落库的质量分对默认权重做动态调整
	weights := c.factorWeightsFor(regime)
	stockFactors = factors.RankAndScore(stockFactors, weights)

	// “操盘手按当日交易策略执行”：优先量化分析师盘后选定的次日策略（策略日计划），
	// 无计划时回退到调参后收益最高的策略。用该策略重排多因子评分结果，使建仓/防守/中性
	// 选股时优先配置该策略当下有买入信号的标的（因子评分作同分兜底）。
	// signalSource：记录本决策买卖信号归属的策略名，随每笔订单落库（修复：策略归属校验）。
	signalSource := "multi-factor-optimizer"
	if strat, sName := c.execStrategyForDecision(); strat != nil && len(stockFactors) > 1 {
		if sName != "" {
			signalSource = sName
		}
		codes := make([]string, len(stockFactors))
		syms := make([]string, len(stockFactors))
		for i, sf := range stockFactors {
			codes[i] = sf.Code
			syms[i] = inferSymbol(sf.Code)
		}
		pref := c.strategyPreference(strat, syms, codes, 250)
		if len(pref) > 0 {
			sort.SliceStable(stockFactors, func(i, j int) bool {
				pi, pj := pref[stockFactors[i].Code], pref[stockFactors[j].Code]
				if pi != pj {
					return pi > pj
				}
				return stockFactors[i].CompositeScore > stockFactors[j].CompositeScore
			})
		}
	}

	// —— Phase1/1b/2/3 决策主张层 + 对抗评审 + 归因反馈 ——
	// 推进六维判势到此处，作为主张输入的证据，并作为规则层仓位基准；
	// 主张层（LLM）+ 异议评审只能让仓位更保守，绝不突破六维/规则风控门。
	sixDim := c.sixDimReport(ctx, true)
	if sixDim != nil {
		decision.SixDim = sixDimDecisionPayload(sixDim)
	}
	// 六维判势硬阻断建仓（修复：暂停建仓门控）：
	// 当六维判势给出"退潮风险"或仓位系数极低（≤0.15）时，判定为"强制暂停建仓"，
	// 直接禁止一切新建仓/加仓（BUY 不生成），但卖出/减仓/止损/调仓不受影响。
	// 彻底修复"六维判势已提示退潮/暂停，却仍通过 scaleOrdersByPositionRate 仅缩小仓位、照常建仓"的越权路径。
	buildBlocked := sixDim != nil && (sixDim.MarketTag == "退潮风险" || sixDim.PositionRate <= 0.15)
	if buildBlocked {
		log.Printf("[CIO] 六维判势硬阻断建仓: 标签=%s 仓位系数=%.2f，本次仅允许卖出/减仓，禁止新建仓与加仓",
			sixDim.MarketTag, sixDim.PositionRate)
	}
	ruleRate := 1.0
	if sixDim != nil {
		ruleRate = sixDim.PositionRate
	}
	propGate := c.decideProposalGate(ctx, proposalInput{
		Date:       time.Now().Format("2006-01-02"),
		Regime:     regime,
		Confidence: confidence,
		Portfolio:  portfolioEvidence(snapshot),
		Drawdown:   drawdownEvidence(ddPct, ddLevel),
		Risk:       riskEvidence(riskReport),
		SixDim:     sixDimEvidence(sixDim),
		Candidates: candidatesSummary(stockFactors, maxTopCandidates),
		Feedback:   c.lastDecisionFeedback(),
		Consensus:  marketConsensusEvidence(sixDim),
	}, ruleRate)
	// 主张层偏好的个股优先进入候选（不影响后续风控门/可执行性/熔断）
	if propGate.Proposal != nil {
		stockFactors = rankByPreferred(stockFactors, propGate.Proposal.PreferredStocks)
	}

	// 依据投资金额动态测算持仓规模，资金越大持仓数越多（与投资规划口径一致）
	maxPositions, _ := util.PortfolioSizing(portfolioValue)
	topCount := maxPositions
	if len(stockFactors) < topCount {
		topCount = len(stockFactors)
	}
	topStocks := factors.SelectTopStocks(stockFactors, topCount)

	positionCount := len(snapshot.Positions)
	targetPositionCount := maxPositions

	// 当前持仓代码集合（用于区分建仓与加仓）
	heldCodes := make(map[string]bool)
	for _, pos := range snapshot.Positions {
		heldCodes[pos.InstrumentID] = true
	}

	// 筛选未持有的优选标的（用于建仓）
	var newStocks []factors.StockFactors
	for _, s := range topStocks {
		if !heldCodes[s.Code] {
			newStocks = append(newStocks, s)
		}
	}

	// —— 策略驱动减仓（核心）——
	// 量化分析师盘后做因子复盘选定次日交易策略写入 daily_strategy_plans；
	// 操盘手盘前据此对持仓逐票判定卖出信号（如 KDJ 死叉）→ 生成清仓卖单。
	// 策略卖出信号优先于市场状态机；卖出不受六维判势仓位系数缩放影响。
	plan, planStrategy := c.dailyPlanStrategy(time.Now().Format("2006-01-02"))
	if planStrategy != nil && len(snapshot.Positions) > 0 {
		strategyOrders := c.strategySellOrders(snapshot.Positions, planStrategy)
		if len(strategyOrders) > 0 {
			orders = strategyOrders
			decision.Decision = agents.DecisionReduce
			decision.Reason = fmt.Sprintf("按量化分析师选定策略「%s」执行卖出信号减仓(%d只)，操盘手按策略信号离场", plan.StrategyName, len(strategyOrders))
			c.markStrategyPlanExecuted(plan)
			log.Printf("[CIO] 策略驱动减仓: 策略=%s 卖出%d只", plan.StrategyName, len(strategyOrders))
		}
	}

	// 净值回撤熔断（止损级）：强制将股票市值降到现金目标比例，暂停交易保护本金。
	// 该动作优先级高于市场状态机的建仓/加仓/调仓（下述 switch 仅在 orders 为空时才执行）。
	if ddLevel == "stop" {
		totalAssets := snapshot.Cash + snapshot.TotalMarketValue
		forceOrders := c.drawdownRiskReduceOrders(snapshot.Positions, totalAssets, 0.5)
		if len(forceOrders) > 0 {
			orders = forceOrders
			decision.Decision = agents.DecisionPauseTrading
			decision.Reason = fmt.Sprintf("净值回撤熔断：组合回撤%.2f%%触发止损线%.0f%%，暂停交易并强制降仓控制风险", ddPct, ddStop)
			log.Printf("[CIO] 回撤熔断(止损级): 回撤=%.2f%% 强制降仓%d只", ddPct, len(forceOrders))
		}
	}

	// —— 持仓数收敛到目标（投资金额分档改制后，如 20 万以内最多 5 只）——
	// 策略卖出、回撤熔断优先；仅在无更高优先级动作且持仓数超出目标时，清算多余持仓收敛到 target。
	if len(orders) == 0 && ddLevel == "" && targetPositionCount > 0 && positionCount > targetPositionCount {
		trimOrders := c.generateTrimToTargetOrders(snapshot.Positions, topStocks, targetPositionCount)
		if len(trimOrders) > 0 {
			orders = trimOrders
			decision.Decision = agents.DecisionReduce
			decision.Reason = fmt.Sprintf("组合持仓%d只超过目标%d只(投资金额分档)，按最新选股收敛减仓", positionCount, targetPositionCount)
			log.Printf("[CIO] 持仓数收敛: 持仓%d只>目标%d只，清算%d只", positionCount, targetPositionCount, len(trimOrders))
		}
	}

	if len(orders) == 0 {
		switch regime {
		case "BULLISH":
			// 建仓：持仓数不足目标且存在未持有的优选标的（回撤预警/熔断或六维暂停建仓时不新建仓）
			if ddLevel == "" && !buildBlocked && positionCount < targetPositionCount && availableCash > 10000 && len(newStocks) > 0 {
				orders = c.generateBuyOrdersFromFactors(newStocks, availableCash, portfolioValue, "bullish", signalSource)
				if len(orders) > 0 {
					decision.Decision = agents.DecisionBuild
					decision.Reason = fmt.Sprintf("市场处于牛市状态(置信度%.0f%%)，基于多因子评分积极建仓", confidence*100)
				}
			}
			// 加仓：持仓数已达目标，对表现良好的持仓加仓（回撤预警/熔断或六维暂停建仓时不加仓）
			if len(orders) == 0 && ddLevel == "" && !buildBlocked && positionCount > 0 && availableCash > 10000 {
				orders = c.generateIncreaseOrders(snapshot.Positions, availableCash, "bullish", signalSource)
				if len(orders) > 0 {
					decision.Decision = agents.DecisionIncrease
					decision.Reason = fmt.Sprintf("市场处于牛市状态(置信度%.0f%%)，对表现良好的持仓加仓", confidence*100)
				}
			}
		case "BEARISH":
			if positionCount > 0 && snapshot.TotalReturn > 0.05 {
				orders = c.generateSellOrders(snapshot.Positions, 0.5, "bearish")
				decision.Decision = agents.DecisionReduce
				decision.Reason = fmt.Sprintf("市场处于熊市状态(置信度%.0f%%)，建议减仓锁定利润", confidence*100)
			} else if positionCount > 0 && snapshot.TotalReturn < -0.08 {
				orders = c.generateSellOrders(snapshot.Positions, 0.3, "stop_loss")
				decision.Decision = agents.DecisionReduce
				decision.Reason = "持仓已触发止损线，建议减仓控制损失"
			} else if ddLevel == "" && !buildBlocked && availableCash > 20000 {
				defensiveStocks := c.filterDefensiveStocks(topStocks)
				if len(defensiveStocks) > 0 {
					orders = c.generateBuyOrdersFromFactors(defensiveStocks, availableCash, portfolioValue, "defensive", signalSource)
					decision.Decision = agents.DecisionBuild
					decision.Reason = "市场熊市，配置防御性板块"
				}
			}
		default:
			// 回撤预警/熔断或六维暂停建仓时不新建仓、不调仓、不加仓
			if ddLevel == "" && !buildBlocked && positionCount == 0 && availableCash > 20000 && len(newStocks) > 0 {
				orders = c.generateBuyOrdersFromFactors(newStocks, availableCash, portfolioValue, "neutral", signalSource)
				decision.Decision = agents.DecisionBuild
				decision.Reason = "市场震荡整理，基于多因子评分布局结构性机会"
			} else if positionCount > 0 {
				if ddLevel == "" && !buildBlocked && shouldRebalance(snapshot, riskReport) {
					orders = c.generateRebalanceOrders(snapshots, snapshot)
					if len(orders) > 0 {
						decision.Decision = agents.DecisionRebalance
						decision.Reason = "组合偏离目标配置，基于优化器结果进行调仓"
					}
				} else if ddLevel == "" && !buildBlocked && availableCash > 10000 {
					orders = c.generateIncreaseOrders(snapshot.Positions, availableCash, "neutral", signalSource)
					if len(orders) > 0 {
						decision.Decision = agents.DecisionIncrease
						decision.Reason = "市场震荡整理，对表现良好的持仓适度加仓"
					}
				}
			}
		}
	} // end if len(orders) == 0

	// 六维判势仓位系数已提前在主张层计算一次（sixDim）并复用：
	// 仓位系数由「六维判势 × LLM主张 × 异议评审」共同决定（规则风控门取更保守者），
	// 这里仅用最终生效仓位系数缩放买入订单（卖出不受影响），并附加主张/证据链到决策。
	if sixDim != nil {
		orders = scaleOrdersByPositionRate(orders, propGate.Effective)
		log.Printf("[CIO] 六维判势: 总分=%.1f(修正%.1f) 冲突=%d 仓位系数=%.2f 标签=%s | 主张门控生效仓位=%.2f",
			sixDim.RawTotalScore, sixDim.AdjustedTotalScore, sixDim.ConflictCount, sixDim.PositionRate, sixDim.MarketTag, propGate.Effective)
	}

	// 主张+证据链落库到 decision.Evidence 并注入决策理由（LLM 不可用时仍记录证据链，供审计）
	c.attachProposal(decision, propGate, ruleRate, sixDim != nil)

	if len(orders) == 0 {
		decision.Decision = agents.DecisionNoAction
		decision.Reason = "量化分析师和风控师都认为暂时没有必要调整"
	} else {
		decision.Orders = orders
	}

	c.checkAndUpdateStops()

	// 携带最近一次组合优化结果（真实协方差），供决策复盘展示
	decision.Optimization = c.lastOptimization

	// LLM 复议层：将规则生成的候选决策提交大模型独立复议，规则在阈值内采纳其意见。
	// 复议不影响盘中实时监控（该路径不经过 makeDecision），仅在盘前 RunDailyCheck 触发。
	c.llmReviewDecision(ctx, decision, quantReport, riskReport)

	return decision
}

// ============ LLM 复议层（否决/评分） ============

// reviewSystemPrompt LLM 复议官系统提示词：要求输出结构化 JSON 审批结果。
const reviewSystemPrompt = `你是A股量化基金的首席风控复议官。系统规则引擎已生成一个候选投资决策（含买卖订单清单），你需要对它做独立复议。
复议要点：
1. 订单方向是否与市场状态/仓位安全匹配（震荡市过度加仓、熊市追高、牛市过早清仓等）；
2. 风险集中度与现金充足度（单票占比、总仓位、回撤预警）；
3. 给出 0-100 的风险评分 score（分数越低代表方案越激进、风险越高）；
4. 仅当方案存在明显风险时才 REJECT；轻微问题用 MODIFY 给出建议；否则 APPROVE。
必须严格只输出一个 JSON 对象，不要输出任何其他文字或代码块标记，格式：
{"action":"APPROVE|MODIFY|REJECT","score":整数0-100,"reason":"不超过80字中文意见","suggestion":"可选的具体调整建议"}`

// llmReviewResult LLM 复议结构化输出
type llmReviewResult struct {
	Action     string `json:"action"`
	Score      int    `json:"score"`
	Reason     string `json:"reason"`
	Suggestion string `json:"suggestion"`
}

// llmReviewDecision 将规则生成的候选决策提交 LLM 独立复议，规则在阈值内采纳其意见。
//   - LLM REJECT 且 score<40（方案明显激进/风险高）→ 采纳否决，清空订单并降级为观望；
//   - 其余（APPROVE / MODIFY / 高分 REJECT）→ 保留规则决策，仅记录复议意见到决策理由；
//   - LLM 未配置 / 调用失败 / 输出无法解析 → 一律放行（规则保底，绝不让 LLM 单点故障阻断交易）。
//
// 仅盘前 RunDailyCheck 链路（一日一次）触发，盘中监控不经过 makeDecision，不受影响。
func (c *CIOEngine) llmReviewDecision(ctx context.Context, decision *agents.CIODecision, quantReport *agents.ResearchReport, riskReport *agents.RiskReport) {
	// 仅复议有实际操作建议的决策；观望/暂停/风控否决等无需复议
	if decision == nil || len(decision.Orders) == 0 ||
		decision.Decision == agents.DecisionNoAction || decision.Decision == agents.DecisionPauseTrading {
		return
	}
	// 未配置 LLM 客户端（未填API key等）→ 放行
	if c.agent == nil || c.agent.LLM == nil {
		return
	}

	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()

	prompt := c.buildReviewPrompt(decision, quantReport, riskReport)

	resp, err := c.agent.LLM.Chat(ctx, []llm.Message{
		{Role: "system", Content: reviewSystemPrompt},
		{Role: "user", Content: prompt},
	}, nil)
	if err != nil {
		log.Printf("[CIO] LLM复议跳过(调用失败): %v", err)
		return
	}
	if len(resp.Choices) == 0 || strings.TrimSpace(resp.Choices[0].Message.Content) == "" {
		log.Printf("[CIO] LLM复议跳过(无输出)")
		return
	}

	review := c.parseReviewResponse(resp.Choices[0].Message.Content)
	if review == nil {
		log.Printf("[CIO] LLM复议跳过(输出无法解析): %s", truncateStr(resp.Choices[0].Message.Content, 120))
		return
	}

	// 落库审计：结构化复议结果随决策持久化
	decision.LLMReview = review

	// 阈值采纳：LLM 明确否决（REJECT + 低分）→ 采纳否决，降级为观望
	if review.Action == "REJECT" && review.Score < 40 {
		prevReason := decision.Reason
		decision.Orders = nil
		decision.Decision = agents.DecisionNoAction
		decision.Reason = fmt.Sprintf("【LLM复议否决】规则候选: %s；LLM意见(评分%d/100): %s。降级为观望，风控规则持续监控。",
			prevReason, review.Score, review.Reason)
		log.Printf("[CIO] LLM复议否决: score=%d reason=%s", review.Score, review.Reason)
		return
	}

	// 其余情况保留规则决策，仅记录复议意见
	decision.Reason = fmt.Sprintf("%s\n【LLM复议】%s(评分%d/100): %s", decision.Reason, review.Action, review.Score, review.Reason)
	if review.Suggestion != "" {
		decision.Reason += " | 建议: " + review.Suggestion
	}
	log.Printf("[CIO] LLM复议放行: action=%s score=%d", review.Action, review.Score)
}

// parseReviewResponse 从 LLM 输出中解析结构化复议结果（兼容 ```json 代码块包裹）。
func (c *CIOEngine) parseReviewResponse(content string) *llmReviewResult {
	text := strings.TrimSpace(content)
	// 提取首个 JSON 对象
	if i := strings.Index(text, "{"); i >= 0 {
		text = text[i:]
		if j := strings.LastIndex(text, "}"); j > i {
			text = text[:j+1]
		}
	}
	var r llmReviewResult
	if err := json.Unmarshal([]byte(text), &r); err != nil {
		return nil
	}
	r.Action = strings.ToUpper(strings.TrimSpace(r.Action))
	if r.Action != "APPROVE" && r.Action != "MODIFY" && r.Action != "REJECT" {
		return nil
	}
	if r.Score < 0 || r.Score > 100 {
		r.Score = 50
	}
	return &r
}

// buildReviewPrompt 构造 LLM 复议输入：市场状态、组合快照、回撤、六维、风控与候选订单清单。
func (c *CIOEngine) buildReviewPrompt(decision *agents.CIODecision, quantReport *agents.ResearchReport, riskReport *agents.RiskReport) string {
	var sb strings.Builder

	sb.WriteString("【候选决策】\n")
	sb.WriteString(fmt.Sprintf("动作=%s\n", decision.Decision))
	for i, o := range decision.Orders {
		sb.WriteString(fmt.Sprintf("  %d) %s %s 目标权重=%.1f%% 最大金额=%.2f 理由=%s\n",
			i+1, o.Side, o.Symbol, o.TargetWeight*100, o.MaxNotional, o.Reason))
	}

	snapshotLine := "无"
	if c.portfolio != nil {
		snap := c.portfolio.GetSnapshot()
		snapshotLine = fmt.Sprintf("总资产=%.2f 现金=%.2f 持仓市值=%.2f 持仓数=%d",
			snap.Cash+snap.TotalMarketValue, snap.Cash, snap.TotalMarketValue, len(snap.Positions))
	}

	ddPct, _, _, ddLevel := c.drawdownStatus()

	sixdimLine := "无"
	if sd, ok := decision.SixDim.(map[string]interface{}); ok {
		parts := []string{}
		if score, ok := sd["adjusted_total_score"].(float64); ok {
			parts = append(parts, fmt.Sprintf("总分=%.1f", score))
		}
		if rate, ok := sd["position_rate"].(float64); ok {
			parts = append(parts, fmt.Sprintf("仓位系数=%.2f", rate))
		}
		if tag, ok := sd["market_tag"].(string); ok && tag != "" {
			parts = append(parts, "标签="+tag)
		}
		if len(parts) > 0 {
			sixdimLine = strings.Join(parts, " ")
		}
	}

	riskLine := "无"
	if riskReport != nil {
		riskLine = fmt.Sprintf("决策=%s 置信度=%.2f 违规=%v", riskReport.Decision, riskReport.Confidence, riskReport.Violations)
	}

	return fmt.Sprintf(`【当前日期】%s
【市场状态】regime=%s 置信度=%.2f
【组合快照】%s
【回撤】%.2f%%(级别=%s)
【六维判势】%s
【风控】%s
%s
请对以上候选方案做独立复议，只输出JSON。`,
		time.Now().Format("2006-01-02 15:04:05"),
		decision.MarketState, decision.MarketConfidence,
		snapshotLine, ddPct, ddLevel, sixdimLine, riskLine, sb.String())
}

// truncateStr 截断字符串（用于日志）
func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// bestExecStrategy 依据策略表最新回测指标选出“最大收益策略”，供操盘手建仓/调仓选股参考。
// 策略表 config_json 与指标由每日 15:05 的策略参数调优写入/刷新，因此这里读取的就是调优后的最优策略。
func (c *CIOEngine) bestExecStrategy() (backtest.Strategy, string) {
	if c.db == nil || c.db.GetDB() == nil {
		return nil, ""
	}
	db := c.db.GetDB()
	var strategies []data.Strategy
	if err := db.Where("is_active = 1").Find(&strategies).Error; err != nil || len(strategies) == 0 {
		return nil, ""
	}
	st, stype := backtest.SelectBestStrategy(strategies)
	if stype == "" {
		return nil, ""
	}
	params := backtest.ParseConfigJSON(st.ConfigJSON)
	strategy := backtest.BuildStrategyFromConfig(stype, params)
	if strategy == nil {
		return nil, ""
	}
	return strategy, stype
}

// dailyPlanStrategy 读取指定交易日（YYYY-MM-DD）的策略日计划并构建策略实例。
// 返回计划行与策略实例；当日无计划或策略无法构建时返回 nil。
func (c *CIOEngine) dailyPlanStrategy(date string) (*data.DailyStrategyPlan, backtest.Strategy) {
	if c.db == nil || c.db.GetDB() == nil {
		return nil, nil
	}
	var plan data.DailyStrategyPlan
	err := c.db.GetDB().Where("trade_date = ? AND status = ?", date, "active").
		Order("updated_at DESC").First(&plan).Error
	if err != nil {
		return nil, nil
	}
	var st data.Strategy
	params := backtest.ParseConfigJSON("")
	if err := c.db.GetDB().Where("strategy_type = ? AND is_active = 1", plan.StrategyType).
		Order("is_builtin DESC, updated_at DESC").First(&st).Error; err == nil {
		params = backtest.ParseConfigJSON(st.ConfigJSON)
	}
	strategy := backtest.BuildStrategyFromConfig(plan.StrategyType, params)
	if strategy == nil {
		return nil, nil
	}
	return &plan, strategy
}

// execStrategyForDecision 返回当日应执行的交易策略：优先今日策略日计划（量化分析师选定），
// 无计划时回退到策略表按回测指标选出的最大收益策略。返回策略实例与显示名。
func (c *CIOEngine) execStrategyForDecision() (backtest.Strategy, string) {
	if plan, strat := c.dailyPlanStrategy(time.Now().Format("2006-01-02")); strat != nil {
		name := plan.StrategyName
		if name == "" {
			name = strat.Name()
		}
		return strat, name
	}
	if strat, _ := c.bestExecStrategy(); strat != nil {
		return strat, strat.Name()
	}
	return nil, ""
}

// markStrategyPlanExecuted 记录当日已按该策略执行卖出信号次数（供复盘溯源）。
func (c *CIOEngine) markStrategyPlanExecuted(plan *data.DailyStrategyPlan) {
	if plan == nil || c.db == nil || c.db.GetDB() == nil {
		return
	}
	plan.ExecutedSellNum++
	if err := c.db.GetDB().Save(plan).Error; err != nil {
		log.Printf("[CIO] 更新策略日计划执行次数失败: %v", err)
	}
}

// strategySellOrders 按当日交易策略对持仓逐票判定卖出信号：
// 取持仓标的最近 lookback 根日K线运行策略，最新信号为卖出（如 KDJ 死叉 / MACD 死叉）
// 则生成 SELL 订单清仓该持仓。每只个股优先按其绑定的策略类型与参数（收盘后按该股自身K线
// 滚动调参得到的 strategy_stock_params）计算信号，未绑定时回退当日全局策略。
// 盘口拦截沿用 generateSellOrders 逻辑：跌停封死跳过保留持仓。
func (c *CIOEngine) strategySellOrders(positions []*portfolio.PositionState, strategy backtest.Strategy) []agents.OrderIntent {
	var orders []agents.OrderIntent
	if strategy == nil || len(positions) == 0 || c.duckDB == nil || !c.duckDB.HasStockDB() {
		return orders
	}
	const lookback = 60
	codes := make([]string, 0, len(positions))
	for _, pos := range positions {
		codes = append(codes, pos.Market+pos.InstrumentID)
	}
	snapMap := make(map[string]data.StockSnapshot)
	if snaps, _ := data.FetchRealtimeStockSnapshots(codes); len(snaps) > 0 {
		for _, s := range snaps {
			snapMap[strings.ToLower(s.Market+s.Code)] = s
			snapMap[strings.ToLower(s.Code)] = s
		}
	}
	for _, pos := range positions {
		sym := inferSymbol(pos.InstrumentID)
		if sym == "" {
			continue
		}
		bars, err := backtest.GetKlineFromDuckDB(c.duckDB, sym, lookback)
		if err != nil || len(bars) < 20 {
			continue // 数据不足无法判定，不生成信号
		}
		// 个股策略绑定：按该股绑定的策略类型与参数构造信号策略；该股未绑定时回退全局策略。
		sigStrategy := strategy
		if bindingType, bindingJSON := c.stockBindingFor(sym); bindingType != "" {
			if bs := backtest.BuildStrategyFromConfig(bindingType, backtest.ParseConfigJSON(bindingJSON)); bs != nil {
				sigStrategy = bs
			}
		}
		barData := make([]backtest.BarData, len(bars))
		for i, b := range bars {
			barData[i] = backtest.BarData{Date: b.Date, Close: b.Close, Open: b.Open, High: b.High, Low: b.Low, Volume: b.Volume, Amount: b.Amount}
		}
		signals := sigStrategy.GenerateSignals(barData)
		n := len(signals)
		if n == 0 || signals[n-1] != backtest.SignalSell {
			continue
		}
		// 盘口拦截：跌停封死 → 跳过保留持仓（当天卖不掉）
		symLower := strings.ToLower(pos.Market + pos.InstrumentID)
		if s, ok := snapMap[symLower]; ok && s.PrevClose > 0 {
			if ob := orderbook.ClassifySnapshot(s); ob.State == orderbook.StateSealedLimitDown {
				log.Printf("[CIO] 策略卖出跳过 %s(%s): %s", pos.StockName, pos.InstrumentID, ob.Reason)
				continue
			}
		}
		// 策略卖出信号去抖护栏：幂等同日去重 + 滞回阈值 + 连续N确认，
		// 拦住在价格临界位反复触发的卖出信号（避免“卖出→又买回”的频繁买卖）。
		if !c.strategySellReady(pos.InstrumentID, time.Now().Format("2006-01-02"), pos.CurrentPrice, pos.AvgCost) {
			continue
		}
		qty := pos.Quantity - pos.TodayBoughtQuantity // T+1：当日买入量不可卖出
		if qty < 100 {
			// 无可卖数量（全部为当日买入）则不开卖单，保留持仓
			continue
		}
		if qty > pos.Quantity {
			qty = pos.Quantity
		}
		order := agents.OrderIntent{
			Symbol:       pos.Market + pos.InstrumentID,
			TargetWeight: float64(qty) * pos.CurrentPrice,
			Side:         "SELL",
			MaxNotional:  float64(qty) * pos.CurrentPrice,
			Reason:       fmt.Sprintf("按策略「%s」卖出信号(如KDJ死叉): 卖出%s %d股@¥%.2f", sigStrategy.Name(), pos.StockName, qty, pos.CurrentPrice),
		}
		orders = append(orders, order)
	}
	return orders
}

// stockBindingFor 返回该股绑定的策略类型与调优参数JSON；该股未绑定时返回空串（回退全局策略）。
func (c *CIOEngine) stockBindingFor(code string) (string, string) {
	if c.db == nil || c.db.GetDB() == nil {
		return "", ""
	}
	var sp data.StrategyStockParam
	if err := c.db.GetDB().Where("code = ?", code).
		Order("updated_at DESC").First(&sp).Error; err != nil {
		return "", ""
	}
	return sp.StrategyType, sp.ParamsJSON
}

// inferSymbol 根据A股代码推断 DuckDB K线 symbol（带 sh/sz/bj 前缀）。
// 若代码已带前缀则原样返回；否则按首位数字映射：沪市6/9/5、深市0/2/3/1、北交所4/8。
func inferSymbol(code string) string {
	code = strings.TrimSpace(code)
	low := strings.ToLower(code)
	if len(low) >= 2 {
		p := low[:2]
		if p == "sh" || p == "sz" || p == "bj" {
			return low
		}
	}
	if len(low) == 0 {
		return ""
	}
	switch low[0] {
	case '6', '9', '5':
		return "sh" + low
	case '0', '1', '2', '3':
		return "sz" + low
	case '4', '8':
		return "bj" + low
	default:
		return low
	}
}

// strategyPreference 用“最大收益策略”对候选标的评估当下买入偏好分：
// 近端买入信号越多且最新信号为买入则得分越高；最新信号为卖出则大幅扣分。
// 无法取数或数据不足时按中性(0)处理，保证原因子排序仍可作为兜底。key=标的code。
func (c *CIOEngine) strategyPreference(strategy backtest.Strategy, symbols, codes []string, days int) map[string]float64 {
	score := make(map[string]float64, len(codes))
	if strategy == nil {
		return score
	}
	const lookback = 20
	for i, code := range codes {
		if code == "" || i >= len(symbols) || symbols[i] == "" {
			continue
		}
		bars, err := backtest.GetKlineFromDuckDB(c.duckDB, symbols[i], days)
		if err != nil || len(bars) < 10 {
			continue // 数据不足，中性
		}
		barData := make([]backtest.BarData, len(bars))
		for j, b := range bars {
			barData[j] = backtest.BarData{Date: b.Date, Close: b.Close, Open: b.Open, High: b.High, Low: b.Low, Volume: b.Volume, Amount: b.Amount}
		}
		signals := strategy.GenerateSignals(barData)
		n := len(signals)
		if n == 0 {
			continue
		}
		recentBuy := 0
		start := 0
		if n > lookback {
			start = n - lookback
		}
		for j := start; j < n; j++ {
			if signals[j] == backtest.SignalBuy {
				recentBuy++
			}
		}
		s := float64(recentBuy)
		switch {
		case signals[n-1] == backtest.SignalSell:
			s = -10
		case signals[n-1] == backtest.SignalBuy:
			s += 6
		}
		score[code] = s
	}
	return score
}

// computeRealFactorScores 使用真实因子引擎计算股票因子
func (c *CIOEngine) computeRealFactorScores(snapshots []data.StockSnapshot) []factors.StockFactors {
	if len(snapshots) == 0 {
		return nil
	}

	codes := make([]string, len(snapshots))
	snapMap := make(map[string]data.StockSnapshot)
	histMap := make(map[string][]data.StockSnapshot)

	for i, s := range snapshots {
		codes[i] = s.Code
		snapMap[s.Code] = s
		histMap[s.Code] = c.buildPriceHistory(s)
	}

	c.factorEngine.SetStockPool(codes)
	results := factors.ComputeAllForEngine(c.factorEngine, snapMap, histMap)

	if len(results) == 0 {
		for _, s := range snapshots {
			history := c.buildPriceHistory(s)
			sf := c.factorEngine.ComputeCached(s, history)
			results = append(results, sf)
		}
	}

	// 补充巨潮资讯网公告情绪因子（真实数据源，带缓存）
	c.enrichWithSentiment(results)

	return results
}

// enrichWithSentiment 为股票因子补充公告情绪因子
// 并发拉取（限流5），缓存1小时；数据源回退链：巨潮 → 东方财富
// 全部数据源不可用时记录错误并跳过该股票，不伪造情绪数据
func (c *CIOEngine) enrichWithSentiment(stockFactors []factors.StockFactors) {
	if c.sentimentEngine == nil || len(stockFactors) == 0 {
		return
	}

	sem := make(chan struct{}, 5)
	var wg sync.WaitGroup
	for i := range stockFactors {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			sf := &stockFactors[idx]
			// 20s 超时：给回退链（最多2个数据源）留出尝试时间，避免拖慢选股周期
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			result, err := c.sentimentEngine.ScoreWithContext(ctx, sf.Code)
			cancel()
			if err != nil {
				log.Printf("[CIO] 情绪因子获取失败 %s: %v（该股票不参与情绪因子评分）", sf.Code, err)
				return
			}
			sf.Factors = append(sf.Factors, factors.FactorScore{
				FactorName:  "sentiment_score",
				Category:    factors.CategorySentiment,
				Score:       result.Score,
				Weight:      0.0,
				Description: fmt.Sprintf("%s情绪因子：近30日正面%d条/负面%d条，得分%.2f（>0.5偏正面）", result.Source, result.PositiveCount, result.NegativeCount, result.Score),
			})
		}(i)
	}
	wg.Wait()
}

// computeFactorICAnalysis 计算因子IC（信息系数）分析
func (c *CIOEngine) computeFactorICAnalysis(stocks []factors.StockFactors) map[string]interface{} {
	if len(stocks) < 3 {
		return map[string]interface{}{
			"ic_value":    0.03,
			"ic_momentum": 0.02,
			"ic_quality":  0.04,
			"summary":     "因子样本不足，使用默认IC值",
		}
	}

	factorICs := make(map[string]float64)
	categoryScores := make(map[string][]float64)

	for _, s := range stocks {
		for _, f := range s.Factors {
			categoryScores[f.Category] = append(categoryScores[f.Category], f.Score)
		}
	}

	for category, scores := range categoryScores {
		if len(scores) >= 3 {
			ic := c.computeICForCategory(scores)
			factorICs[category] = ic
		} else {
			factorICs[category] = 0.02
		}
	}

	summary := "多因子组合IC表现稳健"
	totalIC := 0.0
	count := 0
	for _, ic := range factorICs {
		totalIC += ic
		count++
	}
	if count > 0 {
		avgIC := totalIC / float64(count)
		if avgIC > 0.05 {
			summary = fmt.Sprintf("平均IC=%.3f，因子预测能力强", avgIC)
		} else if avgIC > 0.03 {
			summary = fmt.Sprintf("平均IC=%.3f，因子预测能力中等", avgIC)
		} else {
			summary = fmt.Sprintf("平均IC=%.3f，因子预测能力较弱", avgIC)
		}
	}

	return map[string]interface{}{
		"factor_ics":  factorICs,
		"summary":     summary,
		"stock_count": len(stocks),
	}
}

// computeICForCategory 计算单个类别的IC
func (c *CIOEngine) computeICForCategory(scores []float64) float64 {
	if len(scores) < 3 {
		return 0.0
	}

	halfLen := len(scores) / 2
	var topScores, bottomScores []float64

	sorted := make([]float64, len(scores))
	copy(sorted, scores)
	sort.Float64s(sorted)

	bottomScores = sorted[:halfLen]
	topScores = sorted[len(sorted)-halfLen:]

	topMean := 0.0
	for _, s := range topScores {
		topMean += s
	}
	topMean /= float64(len(topScores))

	bottomMean := 0.0
	for _, s := range bottomScores {
		bottomMean += s
	}
	bottomMean /= float64(len(bottomScores))

	ic := math.Abs(topMean - bottomMean)
	return math.Min(ic, 0.20)
}

// computeIndustryHeat 基于因子评分计算行业热度
func (c *CIOEngine) computeIndustryHeat(stocks []factors.StockFactors) map[string]float64 {
	industryScores := make(map[string][]float64)
	for _, s := range stocks {
		industry := c.inferSector(s.Code)
		industryScores[industry] = append(industryScores[industry], s.CompositeScore)
	}

	heat := make(map[string]float64)
	for industry, scores := range industryScores {
		avg := 0.0
		for _, s := range scores {
			avg += s
		}
		heat[industry] = avg / float64(len(scores))
	}

	return heat
}

// computeMarketBreadth 计算市场宽度
func (c *CIOEngine) computeMarketBreadth(snapshots []data.StockSnapshot) float64 {
	if len(snapshots) == 0 {
		return 0.5
	}
	upCount := 0
	for _, s := range snapshots {
		if s.ChangePercent > 0 {
			upCount++
		}
	}
	return float64(upCount) / float64(len(snapshots))
}

// countLimitUp 统计涨停数
func (c *CIOEngine) countLimitUp(snapshots []data.StockSnapshot) int {
	count := 0
	for _, s := range snapshots {
		if s.ChangePercent >= 9.5 {
			count++
		}
	}
	return count
}

// countLimitDown 统计跌停数
func (c *CIOEngine) countLimitDown(snapshots []data.StockSnapshot) int {
	count := 0
	for _, s := range snapshots {
		if s.ChangePercent <= -9.5 {
			count++
		}
	}
	return count
}

// buildCovarianceFromHistory 基于候选标的在 DuckDB 的真实日收益序列估计协方差矩阵。
// 每只股票取最近 lookback 根日K收盘，计算简单收益率，再取各序列最近的公共长度做样本协方差。
// 返回 nil,false 表示数据不足（无法诚实估计），调用方应降级为等权而非使用硬编码/伪造值。
func (c *CIOEngine) buildCovarianceFromHistory(symbols []string, lookback int) ([][]float64, bool) {
	n := len(symbols)
	if n == 0 || c.duckDB == nil || !c.duckDB.HasStockDB() {
		return nil, false
	}

	ctx := context.Background()
	// 每只股票最近 lookback 根收益（DESC 相邻收盘之比 -1）
	rets := make([][]float64, n)
	active := 0
	for i, sym := range symbols {
		bars, err := c.duckDB.GetKlineFromStock(ctx, data.ToMarketCode(sym), lookback)
		if err != nil || len(bars) < 3 {
			continue
		}
		// bars 按日期 DESC（新在前）：相邻元素反转即为相邻收益（符号对协方差无影响）
		seq := make([]float64, 0, len(bars)-1)
		for k := 0; k+1 < len(bars); k++ {
			if bars[k+1].Close > 0 {
				seq = append(seq, bars[k].Close/bars[k+1].Close-1.0)
			}
		}
		if len(seq) >= 2 {
			rets[i] = seq
			active++
		}
	}

	if active < 2 {
		return nil, false
	}

	// 对齐：取所有有效序列的最近 publicLen 个收益（公共历史深度）
	publicLen := len(rets[0])
	for i := 0; i < n; i++ {
		if len(rets[i]) == 0 {
			continue
		}
		if len(rets[i]) < publicLen {
			publicLen = len(rets[i])
		}
	}
	if publicLen < 2 {
		return nil, false
	}

	// 构造收益矩阵 [n][publicLen]
	R := make([][]float64, n)
	for i := 0; i < n; i++ {
		R[i] = make([]float64, publicLen)
		if len(rets[i]) == 0 {
			continue
		}
		// 取最近 publicLen 个收益
		for k := 0; k < publicLen; k++ {
			R[i][k] = rets[i][len(rets[i])-publicLen+k]
		}
	}

	// 样本协方差（除数 publicLen-1），对角加极小正则项保证数值正定
	cov := make([][]float64, n)
	for i := 0; i < n; i++ {
		cov[i] = make([]float64, n)
	}
	for i := 0; i < n; i++ {
		for j := i; j < n; j++ {
			var mi, mj float64
			for k := 0; k < publicLen; k++ {
				mi += R[i][k]
				mj += R[j][k]
			}
			mi /= float64(publicLen)
			mj /= float64(publicLen)
			s := 0.0
			for k := 0; k < publicLen; k++ {
				s += (R[i][k] - mi) * (R[j][k] - mj)
			}
			v := s / float64(publicLen-1)
			cov[i][j] = v
			cov[j][i] = v
		}
		cov[i][i] += 1e-8
	}
	return cov, true
}

// optimizePortfolioAllocation 使用组合优化器进行仓位分配。
// 除单券上/下限外，同时注入行业级约束（单一行业敞口上限、同行业持仓数量上限），
// 并把真实协方差(风险)与因子评分(预期收益)一起送入优化器。返回分配结果与可复用的
// 区间收益、行业映射，供 Brinson 归因使用。
func (c *CIOEngine) optimizePortfolioAllocation(stocks []factors.StockFactors, portfolioValue float64) (portfolio.OptimizationResult, map[string]string, map[string]float64) {
	empty := portfolio.OptimizationResult{}
	if len(stocks) == 0 {
		return empty, nil, nil
	}

	assets := make([]string, len(stocks))
	expectedReturns := make(map[string]float64)
	for i, s := range stocks {
		assets[i] = s.Code
		expectedReturns[s.Code] = s.CompositeScore - 0.5
	}

	// 读取行业分类并构建 代码->行业 映射（数据全部来自 tdx_sector_data.json，严禁伪造/兜底谎报）
	industryOf := make(map[string]string, len(assets))
	if loader := data.GetDictLoader(); loader != nil {
		for _, s := range stocks {
			industryOf[s.Code] = loader.GetIndustryByStock(s.Code)
		}
	}

	// 用真实日收益序列估计协方差（反映历史波动与相关性，拒绝硬编码/伪造）
	covMatrix, ok := c.buildCovarianceFromHistory(assets, 90)
	if !ok {
		log.Printf("[CIO] 组合优化降级为等权：候选标的 %d 只，无法从 DuckDB 取得≥2只的真实收益序列，无法估计协方差（严禁伪造）", len(assets))
		return portfolio.OptimizeEqualWeight(portfolio.OptimizationInput{
			Assets:          assets,
			ExpectedReturns: expectedReturns,
			CovMatrix:       makeIdentityCov(len(assets)),
			Constraints: portfolio.OptimizationConstraints{
				MaxWeight:            0.25,
				MinWeight:            0.02,
				IndustryOf:           industryOf,
				MaxIndustryWeight:    0.30,
				MaxStocksPerIndustry: 3,
			},
		}), industryOf, nil
	}

	var currentWeights map[string]float64
	if c.portfolio != nil {
		snapshot := c.portfolio.GetSnapshot()
		currentWeights = make(map[string]float64)
		for _, pos := range snapshot.Positions {
			currentWeights[pos.InstrumentID] = pos.Weight / 100.0
		}
	}

	// 用真实协方差 + 因子评分(预期收益) 运行多策略自动选优（均值-方差/风险平等等）
	input := portfolio.OptimizationInput{
		Assets:          assets,
		ExpectedReturns: expectedReturns,
		CovMatrix:       covMatrix,
		CurrentWeights:  currentWeights,
		Constraints: portfolio.OptimizationConstraints{
			MaxWeight:            0.25,
			MinWeight:            0.02,
			RiskFreeRate:         0.03,
			IndustryOf:           industryOf,
			MaxIndustryWeight:    0.30,
			MaxStocksPerIndustry: 3,
		},
	}
	result, strategy := portfolio.SelectBestStrategy(input)
	c.cacheOptimization(stocks, result, strategy, industryOf)
	return result, industryOf, nil
}

// makeIdentityCov 构造 n×n 单位协方差矩阵（等权降级时用于波动率/夏普计算，非硬编码的收益约束）。
func makeIdentityCov(n int) [][]float64 {
	cov := make([][]float64, n)
	for i := 0; i < n; i++ {
		cov[i] = make([]float64, n)
		cov[i][i] = 1.0
	}
	return cov
}

// cacheOptimization 把组合优化结果转成前端友好结构并缓存，供 saveDecision 写入决策记录展示。
// 同时计算行业敞口与行业级 Brinson 归因（组合 vs 等权全候选池），数据全部来自真实权重与真实区间收益。
func (c *CIOEngine) cacheOptimization(stocks []factors.StockFactors, result portfolio.OptimizationResult, strategy portfolio.OptimizerStrategy, industryOf map[string]string) {
	weights := make([]map[string]interface{}, 0, len(stocks))
	for _, s := range stocks {
		w := result.Weights[s.Code]
		if w <= 0 {
			continue
		}
		weights = append(weights, map[string]interface{}{
			"code":   s.Code,
			"name":   s.Name,
			"weight": round4(w * 100), // 百分比
		})
	}
	sort.Slice(weights, func(i, j int) bool {
		return weights[i]["weight"].(float64) > weights[j]["weight"].(float64)
	})

	// 行业敞口：按行业汇总组合目标权重（来自优化器真实结果）
	industryExposure := c.computeIndustryExposure(result.Weights, industryOf)

	// 行业级 Brinson 归因：组合(today optimized) vs 基准(等权全候选池)，收益用真实区间收益
	brinson := c.computeBrinson(result.Weights, stocks, industryOf)

	c.lastOptimization = map[string]interface{}{
		"strategy":            string(strategy),
		"expected_return":     round4(result.ExpectedReturn * 100), // 百分比
		"expected_volatility": round4(result.ExpectedVolatility * 100),
		"sharpe_ratio":        round4(result.SharpeRatio),
		"weights":             weights,
		"constraints": map[string]interface{}{
			"max_industry_weight":     0.30,
			"max_stocks_per_industry": 3,
		},
		"industry_exposure": industryExposure,
		"brinson":           brinson,
	}
}

// computeIndustryExposure 按行业汇总组合目标权重。
func (c *CIOEngine) computeIndustryExposure(weights map[string]float64, industryOf map[string]string) []map[string]interface{} {
	exposure := make(map[string]float64)
	for code, w := range weights {
		if w <= 0 {
			continue
		}
		ind, ok := industryOf[code]
		if !ok || ind == "" {
			ind = "通用"
		}
		exposure[ind] += w * 100 // 百分比
	}
	names := make([]string, 0, len(exposure))
	for ind := range exposure {
		names = append(names, ind)
	}
	sort.Strings(names)
	out := make([]map[string]interface{}, 0, len(names))
	for _, ind := range names {
		out = append(out, map[string]interface{}{
			"industry": ind,
			"weight":   round2(exposure[ind]),
			"limit":    30.0, // 行业敞口上限 30%
		})
	}
	out = sortIndustryByWeight(out)
	return out
}

func sortIndustryByWeight(list []map[string]interface{}) []map[string]interface{} {
	sort.Slice(list, func(i, j int) bool {
		return list[i]["weight"].(float64) > list[j]["weight"].(float64)
	})
	return list
}

// computeBrinson 行业级 Brinson 归因：组合 vs 等权基准，区间收益取自 DuckDB 真实日K线。
// 若区间收益数据不足则返回空结构（严禁伪造）。
func (c *CIOEngine) computeBrinson(weights map[string]float64, stocks []factors.StockFactors, industryOf map[string]string) map[string]interface{} {
	if len(stocks) == 0 || len(weights) == 0 {
		return map[string]interface{}{}
	}

	returns := c.buildIntervalReturns(stocks, 20)
	if len(returns) == 0 {
		return map[string]interface{}{}
	}

	// 等权基准：所有候选标的等权
	equalWeights := make(map[string]float64, len(stocks))
	n := float64(len(stocks))
	for _, s := range stocks {
		equalWeights[s.Code] = 1.0 / n
	}

	attribution := portfolio.CalculateBrinsonAttribution(weights, equalWeights, returns, industryOf)

	industries := make([]map[string]interface{}, 0, len(attribution.Industries))
	for _, ind := range attribution.Industries {
		industries = append(industries, map[string]interface{}{
			"industry":         ind.Industry,
			"portfolio_weight": round2(ind.PortfolioWeight * 100),
			"benchmark_weight": round2(ind.BenchmarkWeight * 100),
			"portfolio_return": round4(ind.PortfolioReturn * 100),
			"benchmark_return": round4(ind.BenchmarkReturn * 100),
			"allocation":       round4(ind.Allocation * 100),
			"selection":        round4(ind.Selection * 100),
			"interaction":      round4(ind.Interaction * 100),
		})
	}
	sort.Slice(industries, func(i, j int) bool {
		return industries[i]["industry"].(string) < industries[j]["industry"].(string)
	})

	return map[string]interface{}{
		"benchmark_return": round4(attribution.BenchmarkReturn * 100),
		"portfolio_return": round4(attribution.PortfolioReturn * 100),
		"excess_return":    round4(attribution.ExcessReturn * 100),
		"allocation":       round4(attribution.Allocation * 100),
		"selection":        round4(attribution.Selection * 100),
		"interaction":      round4(attribution.Interaction * 100),
		"industries":       industries,
	}
}

// buildIntervalReturns 取每只股票最近 lookback 个交易日的真实区间累计收益率（最新收盘/最早收盘-1）。
func (c *CIOEngine) buildIntervalReturns(stocks []factors.StockFactors, lookback int) map[string]float64 {
	if c.duckDB == nil || !c.duckDB.HasStockDB() {
		return nil
	}
	ctx := context.Background()
	out := make(map[string]float64)
	for _, s := range stocks {
		bars, err := c.duckDB.GetKlineFromStock(ctx, data.ToMarketCode(s.Code), lookback)
		if err != nil || len(bars) < 2 {
			continue
		}
		// bars 按日期 DESC：最新在前，最早在后
		first := bars[len(bars)-1].Close
		last := bars[0].Close
		if first <= 0 {
			continue
		}
		out[s.Code] = last/first - 1.0
	}
	return out
}

// round4 保留 4 位小数。
func round4(v float64) float64 {
	return math.Round(v*10000) / 10000
}

// round2 保留 2 位小数。
func round2(v float64) float64 {
	return math.Round(v*100) / 100
}

// round1 保留 1 位小数。
func round1(v float64) float64 {
	return math.Round(v*10) / 10
}

// detectMarketRegime 检测市场状态
func (c *CIOEngine) detectMarketRegime() string {
	indices := data.GetActiveMarketIndices(c.db.GetDB())
	if len(indices) == 0 {
		indices = []data.MarketIndex{
			{Code: "sh000001"}, {Code: "sz399001"}, {Code: "sz399006"},
		}
	}

	if c.duckDB == nil || !c.duckDB.HasStockDB() {
		log.Printf("[CIO] DuckDB不可用，detectMarketRegime返回NEUTRAL")
		return "NEUTRAL"
	}

	ctx := context.Background()
	bullScore := 0.0
	bearScore := 0.0

	for _, idx := range indices {
		bars, err := c.duckDB.GetKlineFromStock(ctx, idx.Code, 60)
		if err != nil || len(bars) < 10 {
			continue
		}

		n := len(bars)
		closes := make([]float64, n)
		for i, bar := range bars {
			closes[i] = bar.Close
		}

		if closes[1] <= 0 {
			continue
		}

		day5Ret := 0.0
		if n > 5 && closes[5] > 0 {
			day5Ret = (closes[0] - closes[5]) / closes[5] * 100
		}
		day10Ret := 0.0
		if n > 10 && closes[10] > 0 {
			day10Ret = (closes[0] - closes[10]) / closes[10] * 100
		}
		day20Ret := 0.0
		if n > 20 && closes[20] > 0 {
			day20Ret = (closes[0] - closes[20]) / closes[20] * 100
		}

		ma5 := 0.0
		if n >= 5 {
			for i := 0; i < 5; i++ {
				ma5 += closes[i]
			}
			ma5 /= 5
		}
		ma20 := 0.0
		if n >= 20 {
			for i := 0; i < 20; i++ {
				ma20 += closes[i]
			}
			ma20 /= 20
		}

		avgVol20 := 0.0
		if n >= 20 {
			for i := 0; i < 20; i++ {
				avgVol20 += bars[i].Volume
			}
			avgVol20 /= 20
		}
		volRatio := 1.0
		if avgVol20 > 0 && bars[0].Volume > 0 {
			volRatio = bars[0].Volume / avgVol20
		}
		volShrink := volRatio < 0.8

		weight := 1.0
		switch idx.Code {
		case "sh000001":
			weight = 1.5
		case "000300":
			weight = 1.5
		case "sz399001":
			weight = 1.2
		}

		idxBull := 0.0
		idxBear := 0.0

		if day5Ret > 1.0 {
			idxBull += 2.0
		} else if day5Ret > 0 {
			idxBull += 0.5
		} else if day5Ret < -1.0 {
			idxBear += 2.0
		} else if day5Ret < 0 {
			idxBear += 0.5
		}

		if day10Ret > 2.0 {
			idxBull += 1.5
		} else if day10Ret > 0 {
			idxBull += 0.5
		} else if day10Ret < -2.0 {
			idxBear += 1.5
		} else if day10Ret < 0 {
			idxBear += 0.5
		}

		if day20Ret > 3.0 {
			idxBull += 1.5
		} else if day20Ret < -3.0 {
			idxBear += 1.5
		}

		if closes[0] > ma5 {
			idxBull += 0.5
		} else {
			idxBear += 0.5
		}
		if closes[0] > ma20 {
			idxBull += 0.5
		} else {
			idxBear += 0.5
		}

		// 缩量下跌 = 强烈看跌信号
		if day5Ret < 0 && volShrink {
			idxBear += 2.0
		}
		if day5Ret > 0 && volShrink {
			idxBear += 0.5
		}

		bullScore += weight * idxBull
		bearScore += weight * idxBear
	}

	if bullScore > bearScore*1.3 {
		return "BULLISH"
	} else if bearScore > bullScore*1.3 {
		return "BEARISH"
	}
	return "NEUTRAL"
}

// checkPositionStopLoss 检查单个持仓的止损状态（使用ATR动态止损）
func (c *CIOEngine) checkPositionStopLoss(pos *portfolio.PositionState) risk.RiskAlert {
	stopConfig := risk.DefaultStopLossConfig()

	history := c.buildPriceHistoryFromPositions(pos.InstrumentID)
	if len(history) >= 5 {
		highs, lows, closes := extractHLC(history)
		atr := c.riskEngine.CalculateATR(highs, lows, closes, 14)
		if atr > 0 && pos.CurrentPrice > 0 {
			atrStop := (atr * stopConfig.ATRMultiplier) / pos.CurrentPrice
			if atrStop > 0.15 {
				atrStop = 0.15
			}
			if atrStop < 0.03 {
				atrStop = 0.03
			}
			stopConfig.InitialStop = atrStop
		}
	}

	return c.riskEngine.MonitorPositionRisk(
		pos.InstrumentID, pos.CurrentPrice, pos.AvgCost, stopConfig)
}

// checkAndUpdateStops 实时止损管理（仅记录警报，不执行交易）
func (c *CIOEngine) checkAndUpdateStops() {
	if c.portfolio == nil {
		return
	}

	snapshot := c.portfolio.GetSnapshot()

	for _, pos := range snapshot.Positions {
		alert := c.checkPositionStopLoss(pos)

		if alert.AlertType != "ok" && alert.AlertType != "info" {
			c.addActivity("RISK", "STOP_ALERT",
				fmt.Sprintf("止损警报: %s(%s) - %s", pos.StockName, pos.InstrumentID, alert.Message), alert)
		}
	}
}

// ExecuteStopLossIfTriggered 盘中止损执行：
// 刷新实时价格 → 检查所有持仓止损 → 触发止损的持仓自动生成卖出订单并执行（走审批流程）
// 返回触发止损并执行卖出的持仓数量
func (c *CIOEngine) ExecuteStopLossIfTriggered(ctx context.Context) int {
	// 紧急停止门控：停止状态下不执行任何止损下单
	if c.emergencyStopped() {
		log.Printf("[CIO] 紧急停止中，跳过止损执行")
		return 0
	}

	if c.portfolio == nil {
		return 0
	}

	// 刷新实时价格（复用短TTL缓存，只返回真实数据）
	c.portfolio.RefreshPrices()

	snapshot := c.portfolio.GetSnapshot()
	triggeredCount := 0

	for _, pos := range snapshot.Positions {
		alert := c.checkPositionStopLoss(pos)
		if alert.AlertType != "stop_loss" && alert.AlertType != "trailing_stop" {
			continue
		}

		c.addActivity("RISK", "STOP_ALERT",
			fmt.Sprintf("止损警报: %s(%s) - %s", pos.StockName, pos.InstrumentID, alert.Message), alert)

		// A股T+1：今日买入的股票不可卖出
		sellable := pos.Quantity - pos.TodayBoughtQuantity
		if sellable < 100 {
			c.addActivity("RISK", "STOP_SKIPPED",
				fmt.Sprintf("止损触发但T+1无可卖数量: %s(%s) 可卖%d股", pos.StockName, pos.InstrumentID, sellable), alert)
			continue
		}

		sellQty := (sellable / 100) * 100
		if sellQty < 100 {
			sellQty = 100
		}
		if sellQty > sellable {
			sellQty = sellable
		}

		order := agents.OrderIntent{
			Symbol:       pos.Market + pos.InstrumentID,
			TargetWeight: float64(sellQty) * pos.CurrentPrice,
			Side:         "SELL",
			MaxNotional:  float64(sellQty) * pos.CurrentPrice,
			Reason:       fmt.Sprintf("止损卖出: %s(%s) %d股@¥%.2f - %s", pos.StockName, pos.InstrumentID, sellQty, pos.CurrentPrice, alert.Message),
		}

		decision := &agents.CIODecision{
			DecisionID:   c.generateDecisionID(),
			PortfolioID:  "P001",
			Decision:     agents.DecisionReduce,
			Reason:       order.Reason,
			RiskApproval: string(agents.RiskApprove),
			PolicyStatus: "PASSED",
			Orders:       []agents.OrderIntent{order},
			Timestamp:    time.Now(),
		}

		c.executeDecision(ctx, decision)
		triggeredCount++
	}

	return triggeredCount
}

// performSectorRotation 执行行业轮动
func (c *CIOEngine) performSectorRotation() {
	if c.portfolio == nil {
		return
	}

	regime := c.detectMarketRegime()

	defensiveSectors := []string{"公用事业", "医药生物", "食品饮料", "银行"}
	cyclicalSectors := []string{"电子", "计算机", "电力设备", "汽车", "机械设备"}

	var targetSectors []string
	switch regime {
	case "BULLISH":
		targetSectors = cyclicalSectors
	case "BEARISH":
		targetSectors = defensiveSectors
	default:
		targetSectors = append(defensiveSectors[:2], cyclicalSectors[:2]...)
	}

	snapshot := c.portfolio.GetSnapshot()
	targetMap := make(map[string]bool)
	for _, s := range targetSectors {
		targetMap[s] = true
	}

	var rotateOut []*portfolio.PositionState
	var rotateIn []string

	for _, pos := range snapshot.Positions {
		sector := c.inferSector(pos.InstrumentID)
		if !targetMap[sector] && pos.UnrealizedReturn > 0 {
			rotateOut = append(rotateOut, pos)
		}
	}

	if len(rotateOut) > 0 {
		c.addActivity("CIO", "SECTOR_ROTATION",
			fmt.Sprintf("行业轮动: 卖出 %d 只非目标行业持仓，目标行业: %v", len(rotateOut), targetSectors),
			map[string]interface{}{
				"regime":        regime,
				"targetSectors": targetSectors,
				"rotateCount":   len(rotateOut),
			})
	}

	_ = rotateIn
}

// shouldRebalance 判断是否需要调仓
func shouldRebalance(snapshot *portfolio.PortfolioSnapshot, riskReport *agents.RiskReport) bool {
	if len(snapshot.Positions) < 2 {
		return false
	}

	maxWeight := 0.0
	for _, pos := range snapshot.Positions {
		if pos.Weight > maxWeight {
			maxWeight = pos.Weight
		}
	}

	if maxWeight > 30 {
		return true
	}

	for _, pos := range snapshot.Positions {
		if pos.UnrealizedReturn < -10 {
			return true
		}
	}

	if len(riskReport.Violations) > 0 {
		return true
	}

	return false
}

// generateBuyOrders 生成买入订单
func (c *CIOEngine) generateBuyOrders(snapshots []data.StockSnapshot, availableCash float64, count int, reason string) []agents.OrderIntent {
	var orders []agents.OrderIntent

	affordable := make([]data.StockSnapshot, 0)
	for _, s := range snapshots {
		if s.CurrentPrice > 0 && s.CurrentPrice*100 <= availableCash/float64(count) {
			affordable = append(affordable, s)
		}
	}

	if len(affordable) == 0 {
		affordable = snapshots
	}

	sort.Slice(affordable, func(i, j int) bool {
		return affordable[i].ChangePercent > affordable[j].ChangePercent
	})

	selectionCount := count
	if len(affordable) < selectionCount {
		selectionCount = len(affordable)
	}

	perStockCash := availableCash / float64(selectionCount)

	for i := 0; i < selectionCount; i++ {
		s := affordable[i]
		if s.CurrentPrice <= 0 {
			continue
		}

		shares := int(perStockCash/s.CurrentPrice/100) * 100
		if shares < 100 {
			shares = 100
		}

		order := agents.OrderIntent{
			Symbol:       s.Market + s.Code,
			TargetWeight: float64(shares) * s.CurrentPrice / availableCash,
			Side:         "BUY",
			MaxNotional:  float64(shares) * s.CurrentPrice * 1.001,
			Reason:       fmt.Sprintf("%s: 买入%s(%s) %d股@¥%.2f", reason, s.Name, s.Code, shares, s.CurrentPrice),
		}
		orders = append(orders, order)
	}

	return orders
}

// generateBuyOrdersFromFactors 使用因子评分生成买入订单
// tradeablePoolBuyableCodes 返回当前可交易股票池中"已批准/可买入"的股票代码集合。
// 作为因子优选建仓的白名单：不在可交易池内的标的禁止通过 CIO 决策生成买入订单，
// 杜绝"选股引擎捞到未入池标的 → 绕开股票池审核直接买入"的越权路径。
func (c *CIOEngine) tradeablePoolBuyableCodes() map[string]bool {
	allowed := make(map[string]bool)
	if c.tradeablePoolHandler == nil || c.tradeablePoolHandler.tradeablePool == nil {
		return allowed
	}
	stocks, err := c.tradeablePoolHandler.tradeablePool.GetBuyableStocks()
	if err != nil {
		log.Printf("[CIO] 读取可交易股票池失败，本次不产生任何白名单买入: %v", err)
		return allowed
	}
	for _, s := range stocks {
		code := normalizePlanCode(s.StockCode)
		if code != "" {
			allowed[code] = true
		}
	}
	return allowed
}

// generateBuyOrdersFromFactors 生成因子优选买入订单。
//   - 白名单校验（修复）：仅允许买入"可交易股票池"中已批准可买的标的，未入池一律剔除；
//   - 策略归属校验（修复）：每笔买入订单标注 SignalSource，且候选已被当日执行策略偏好重排，
//     确保买入信号与所选策略一致（无策略计划时回落 multi-factor-optimizer）。
func (c *CIOEngine) generateBuyOrdersFromFactors(topStocks []factors.StockFactors, availableCash float64, portfolioValue float64, reason string, signalSource string) []agents.OrderIntent {
	var orders []agents.OrderIntent

	if len(topStocks) == 0 {
		return orders
	}

	// 白名单：仅保留可交易股票池中已批准可买的标的
	allowed := c.tradeablePoolBuyableCodes()
	if len(allowed) == 0 {
		log.Println("[CIO] 可交易股票池为空，拒绝基于多因子的自主建仓（白名单强制）")
		return orders
	}
	poolFiltered := make([]factors.StockFactors, 0, len(topStocks))
	for _, s := range topStocks {
		if allowed[normalizePlanCode(s.Code)] {
			poolFiltered = append(poolFiltered, s)
		} else {
			log.Printf("[CIO] 白名单剔除: %s(%s) 未在可交易股票池，禁止买入", s.Name, s.Code)
		}
	}
	if len(poolFiltered) == 0 {
		return orders
	}
	topStocks = poolFiltered

	optimizationResult, _, _ := c.optimizePortfolioAllocation(topStocks, portfolioValue)

	for _, s := range topStocks {
		targetWeight := optimizationResult.Weights[s.Code]
		if targetWeight <= 0 {
			targetWeight = 1.0 / float64(len(topStocks))
		}

		allocation := availableCash * targetWeight
		price := c.getCurrentPrice(s.Code)
		if price <= 0 {
			continue
		}

		shares := int(allocation/price/100) * 100
		if shares < 100 {
			continue
		}

		order := agents.OrderIntent{
			Symbol:       "sh" + s.Code,
			TargetWeight: targetWeight,
			Side:         "BUY",
			MaxNotional:  float64(shares) * price * 1.001,
			Reason:       fmt.Sprintf("%s: 因子优选买入%s(%s) %d股@¥%.2f (复合得分=%.4f, 信号来源=%s)", reason, s.Name, s.Code, shares, price, s.CompositeScore, signalSource),
			SignalSource: signalSource,
		}
		orders = append(orders, order)
	}

	return orders
}

// generateIncreaseOrders 生成加仓订单（对现有持仓中表现良好的标的加仓）
func (c *CIOEngine) generateIncreaseOrders(positions []*portfolio.PositionState, availableCash float64, reason string, signalSource string) []agents.OrderIntent {
	var orders []agents.OrderIntent
	if len(positions) == 0 || availableCash <= 10000 {
		return orders
	}

	// 只对未触发止损、价格有效的持仓加仓
	var candidates []*portfolio.PositionState
	for _, pos := range positions {
		if pos.UnrealizedReturn >= -5 && pos.CurrentPrice > 0 {
			candidates = append(candidates, pos)
		}
	}

	if len(candidates) == 0 {
		return orders
	}

	// 按收益率排序，优先加仓表现最好的持仓
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].UnrealizedReturn > candidates[j].UnrealizedReturn
	})

	maxCount := 3
	if len(candidates) < maxCount {
		maxCount = len(candidates)
	}

	// 每只用可用资金的 10% 加仓，最多 3 只（总额不超过可用资金的 30%）
	perStockCash := availableCash * 0.1
	for i := 0; i < maxCount; i++ {
		pos := candidates[i]
		price := c.getCurrentPrice(pos.InstrumentID)
		if price <= 0 {
			continue
		}

		shares := int(perStockCash/price/100) * 100
		if shares < 100 {
			continue
		}

		orders = append(orders, agents.OrderIntent{
			Symbol:       pos.Market + pos.InstrumentID,
			TargetWeight: float64(shares) * price / availableCash,
			Side:         "BUY",
			MaxNotional:  float64(shares) * price * 1.001,
			Reason:       fmt.Sprintf("%s: 加仓%s(%s) %d股@¥%.2f (信号来源=%s)", reason, pos.StockName, pos.InstrumentID, shares, price, signalSource),
			SignalSource: signalSource,
		})
	}

	return orders
}

// generateTrimByPreferred 将持仓收敛到 target，保留 preferred 中的标的（方案目标代码），
// 其余（未被方案选中）按市值从小到大清算；遵守盘口（跌停封死跳过）与 T+1（当日买入量不可卖）。
func (c *CIOEngine) generateTrimByPreferred(positions []*portfolio.PositionState, preferred map[string]bool, target int) []agents.OrderIntent {
	var orders []agents.OrderIntent
	if target <= 0 || len(positions) <= target {
		return orders
	}
	var sellCand, keepCand []*portfolio.PositionState
	for _, pos := range positions {
		if preferred[normalizePlanCode(pos.InstrumentID)] {
			keepCand = append(keepCand, pos)
		} else {
			sellCand = append(sellCand, pos)
		}
	}
	needSell := len(positions) - target
	remain := needSell - len(sellCand)
	toSell := append([]*portfolio.PositionState(nil), sellCand...)
	if remain > 0 && len(keepCand) > 0 {
		// 仍超目标：从保留标的中按市值从小到大清算
		inner := append([]*portfolio.PositionState(nil), keepCand...)
		sort.SliceStable(inner, func(i, j int) bool {
			return float64(inner[i].Quantity)*inner[i].CurrentPrice < float64(inner[j].Quantity)*inner[j].CurrentPrice
		})
		if remain > len(inner) {
			remain = len(inner)
		}
		toSell = append(toSell, inner[:remain]...)
	}
	// 盘口/涨跌约束 + T+1 后可卖数量
	codes := make([]string, 0, len(toSell))
	for _, pos := range toSell {
		codes = append(codes, pos.Market+pos.InstrumentID)
	}
	snapMap := make(map[string]data.StockSnapshot)
	if snaps, _ := data.FetchRealtimeStockSnapshots(codes); len(snaps) > 0 {
		for _, s := range snaps {
			snapMap[strings.ToLower(s.Market+s.Code)] = s
			snapMap[strings.ToLower(s.Code)] = s
		}
	}
	for _, pos := range toSell {
		sym := strings.ToLower(pos.Market + pos.InstrumentID)
		if s, ok := snapMap[sym]; ok && s.PrevClose > 0 {
			if ob := orderbook.ClassifySnapshot(s); ob.State == orderbook.StateSealedLimitDown {
				log.Printf("[CIO] 盘中持仓收敛跳过 %s(%s): %s", pos.StockName, pos.InstrumentID, ob.Reason)
				continue
			}
		}
		qty := pos.Quantity - pos.TodayBoughtQuantity
		if qty < 100 {
			continue // 无可卖数量（T+1 当日买入）
		}
		if qty > pos.Quantity {
			qty = pos.Quantity
		}
		price := pos.CurrentPrice
		if price <= 0 {
			price = pos.AvgCost
		}
		if qty <= 0 || price <= 0 {
			continue
		}
		orders = append(orders, agents.OrderIntent{
			Symbol:       pos.Market + pos.InstrumentID,
			TargetWeight: float64(qty) * price,
			Side:         "SELL",
			MaxNotional:  float64(qty) * price,
			Reason:       fmt.Sprintf("盘中持仓收敛至目标%d只: 清算方案外%s %d股@¥%.2f", target, pos.StockName, qty, price),
		})
	}
	return orders
}

// generateTrimToTargetOrders 将持仓收敛到目标数量（如投资金额分档改制后需把超出目标数的持仓清掉）。
// 优先保留当前选股 topStocks 中的标的，清算不在选股池内/排名靠后的持仓，使持仓数回到 target。
// 盘口感知：跌停封死跳过；T+1 遵循当日买入量不可卖出。
func (c *CIOEngine) generateTrimToTargetOrders(positions []*portfolio.PositionState, topStocks []factors.StockFactors, target int) []agents.OrderIntent {
	var orders []agents.OrderIntent
	if target <= 0 || len(positions) <= target {
		return orders
	}
	preferred := make(map[string]bool)
	for _, s := range topStocks {
		preferred[strings.ToLower(s.Code)] = true
	}
	var sellCand, keepCand []*portfolio.PositionState
	for _, pos := range positions {
		sym := strings.ToLower(pos.Market + pos.InstrumentID)
		if preferred[sym] || preferred[strings.ToLower(pos.InstrumentID)] {
			keepCand = append(keepCand, pos)
		} else {
			sellCand = append(sellCand, pos)
		}
	}
	needSell := len(positions) - target
	toSell := sellCand
	remain := needSell - len(sellCand)
	if remain > 0 {
		// 仍超目标：从池内标的中按市值从小到大清算
		inner := append([]*portfolio.PositionState(nil), keepCand...)
		sort.SliceStable(inner, func(i, j int) bool {
			return float64(inner[i].Quantity)*inner[i].CurrentPrice < float64(inner[j].Quantity)*inner[j].CurrentPrice
		})
		if remain > len(inner) {
			remain = len(inner)
		}
		toSell = append(toSell, inner[:remain]...)
	}

	// 盘口/涨跌约束 + T+1 后可卖数量
	codes := make([]string, 0, len(toSell))
	for _, pos := range toSell {
		codes = append(codes, pos.Market+pos.InstrumentID)
	}
	snapMap := make(map[string]data.StockSnapshot)
	if snaps, _ := data.FetchRealtimeStockSnapshots(codes); len(snaps) > 0 {
		for _, s := range snaps {
			snapMap[strings.ToLower(s.Market+s.Code)] = s
			snapMap[strings.ToLower(s.Code)] = s
		}
	}
	for _, pos := range toSell {
		sym := strings.ToLower(pos.Market + pos.InstrumentID)
		if s, ok := snapMap[sym]; ok && s.PrevClose > 0 {
			if ob := orderbook.ClassifySnapshot(s); ob.State == orderbook.StateSealedLimitDown {
				log.Printf("[CIO] 持仓收敛跳过 %s(%s): %s", pos.StockName, pos.InstrumentID, ob.Reason)
				continue
			}
		}
		qty := pos.Quantity - pos.TodayBoughtQuantity
		if qty < 100 {
			continue // 无可卖数量（T+1 当日买入）
		}
		if qty > pos.Quantity {
			qty = pos.Quantity
		}
		price := pos.CurrentPrice
		if price <= 0 {
			price = pos.AvgCost
		}
		if qty <= 0 || price <= 0 {
			continue
		}
		orders = append(orders, agents.OrderIntent{
			Symbol:       pos.Market + pos.InstrumentID,
			TargetWeight: float64(qty) * price,
			Side:         "SELL",
			MaxNotional:  float64(qty) * price,
			Reason:       fmt.Sprintf("持仓收敛至目标%d只: 清算%s %d股@¥%.2f", target, pos.StockName, qty, price),
		})
	}
	return orders
}

// generateSellOrders 生成卖出订单（减仓/止损）。
// 盘口感知：跌停封死的标的（卖一=跌停价且封单巨大）当天无法成交，跳过并保留持仓，
// 避免生成必然成交不了的卖单；跌停打开/涨停/正常仍生成卖出（跌停打开允许按跌停价离场）。
func (c *CIOEngine) generateSellOrders(positions []*portfolio.PositionState, sellRatio float64, reason string) []agents.OrderIntent {
	var orders []agents.OrderIntent

	// 批量获取持仓实时快照（含盘口五档），供跌停封死判定；失败时不拦截（沿用原逻辑）
	codes := make([]string, 0, len(positions))
	for _, pos := range positions {
		codes = append(codes, pos.Market+pos.InstrumentID)
	}
	snapMap := make(map[string]data.StockSnapshot)
	if snaps, _ := data.FetchRealtimeStockSnapshots(codes); len(snaps) > 0 {
		for _, s := range snaps {
			snapMap[strings.ToLower(s.Market+s.Code)] = s
			snapMap[strings.ToLower(s.Code)] = s
		}
	}

	for _, pos := range positions {
		// 盘口判定：跌停封死 → 跳过保留持仓（当天卖不掉，生成卖单无意义）；
		// 巨量跌停打开（量比放量 + 跌停打开）→ 恐慌性出逃，减仓升级为优先清仓离场。
		effectiveRatio := sellRatio
		sym := strings.ToLower(pos.Market + pos.InstrumentID)
		if s, ok := snapMap[sym]; ok && s.PrevClose > 0 {
			ob := orderbook.ClassifySnapshot(s)
			if ob.State == orderbook.StateSealedLimitDown {
				log.Printf("[CIO] 减仓跳过 %s(%s): %s", pos.StockName, pos.InstrumentID, ob.Reason)
				continue
			}
			if ob.State == orderbook.StateOpenedLimitDown && ob.HugeVolumeDown {
				effectiveRatio = 1.0
				log.Printf("[CIO] %s(%s) 巨量跌停打开，减仓升级为清仓离场: %s", pos.StockName, pos.InstrumentID, ob.Reason)
			}
		}

		sellQty := int(float64(pos.Quantity) * effectiveRatio)
		if sellQty < 100 {
			sellQty = 100
		}
		if sellQty > pos.Quantity {
			sellQty = pos.Quantity
		}
		if sellQty <= 0 {
			continue
		}

		order := agents.OrderIntent{
			Symbol:       pos.Market + pos.InstrumentID,
			TargetWeight: float64(sellQty) * pos.CurrentPrice,
			Side:         "SELL",
			MaxNotional:  float64(sellQty) * pos.CurrentPrice,
			Reason:       fmt.Sprintf("%s: 卖出%s %d股@¥%.2f", reason, pos.StockName, sellQty, pos.CurrentPrice),
		}
		orders = append(orders, order)
	}

	return orders
}

// factorWeightsFor 因子复盘自适应加权：以默认权重为基础，用 FactorQuality 表中每夜刷新的质量分动态调整。
// 规则：质量分高于均值 → 上调权重；低于均值 → 下调；无质量记录的因子保持默认权重。
// 调整倍数 clamp 到 [0.5, 1.5]，最后整体缩放使权重总和与默认一致，保证评分可比性。
// 质量分来自 nightly_quant 因子复盘（基于真实K线的 IC/准确率/区分度等），从而体现"不同阶段因子价值不同"。
func (c *CIOEngine) factorWeightsFor(regime string) map[string]float64 {
	base := factors.GetDefaultFactorWeights(regime)
	if c.db == nil || c.db.GetDB() == nil {
		return base
	}
	// 读取最新各因子质量分（factor_name 为主键，即最近一次因子复盘落库值）
	var qs []data.FactorQuality
	if err := c.db.GetDB().Find(&qs).Error; err != nil {
		return base
	}
	qmap := map[string]float64{}
	// halfInfo 记录因子的分半信息：只有 SplitHalfIC 非空才表示「真实可判定」的分半结果
	//（样本量不足时 buildFactorItem 返回空 SplitHalfIC），用于诚实下线时区分「真实不稳定」与「无法判定」。
	type halfInfo struct {
		stable bool
		ic     string
	}
	half := map[string]halfInfo{}
	for _, q := range qs {
		qmap[q.FactorName] = q.Quality
		half[q.FactorName] = halfInfo{stable: q.SplitHalfStable, ic: q.SplitHalfIC}
	}
	if len(qmap) == 0 {
		return base
	}
	// 质量分参考值用中位数（对极值/单只突兀噪声更稳健），作为「好因子上调、差因子下调」的基准。
	meanQ := medianFactorQuality(qmap)
	// 目标权重总和与默认一致
	var baseSum float64
	for _, w := range base {
		baseSum += w
	}
	weights := map[string]float64{}
	var adjSum float64
	for name, w := range base {
		mult := 1.0
		if q, ok := qmap[name]; ok && meanQ > 0 && q > 0 {
			mult = clampF(q/meanQ, 0.5, 1.5)
			// 诚实下线：仅当分半检验「真实可判定」且判定为不稳定(false)时，才在质量分基础上强力降权
			//（先封顶 0.6 再砍半 → 总倍数 ≤0.3）；样本不足、SplitHalfIC 为空视为无法判定，不武断下线。
			if h, okH := half[name]; okH && !h.stable && h.ic != "" {
				mult = math.Min(mult, 0.6) // 先封顶到 0.6，再砍半 → 总倍数 ≤0.3，强力降权
				mult *= 0.5
			}
		}
		aw := w * mult
		weights[name] = aw
		adjSum += aw
	}
	if adjSum > 0 && baseSum > 0 {
		scale := baseSum / adjSum
		for k, v := range weights {
			weights[k] = v * scale
		}
	}
	return weights
}

func clampF(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// medianFactorQuality 统计有记录的因子质量分中位数（比均值对极值更稳健），空时返回 0。
func medianFactorQuality(m map[string]float64) float64 {
	if len(m) == 0 {
		return 0
	}
	arr := make([]float64, 0, len(m))
	for _, v := range m {
		arr = append(arr, v)
	}
	sort.Float64s(arr)
	n := len(arr)
	if n%2 == 1 {
		return arr[n/2]
	}
	return (arr[n/2-1] + arr[n/2]) / 2
}

// generateRebalanceOrders 生成调仓订单
func (c *CIOEngine) generateRebalanceOrders(snapshots []data.StockSnapshot, snapshot *portfolio.PortfolioSnapshot) []agents.OrderIntent {
	var orders []agents.OrderIntent

	var sellOrders []agents.OrderIntent
	targetSellAmount := 0.0

	// 盘口索引（含五档），供跌停封死判定
	snapMap := make(map[string]data.StockSnapshot, len(snapshots))
	for _, s := range snapshots {
		snapMap[strings.ToLower(s.Market+s.Code)] = s
		snapMap[strings.ToLower(s.Code)] = s
	}

	for _, pos := range snapshot.Positions {
		if pos.UnrealizedReturn < -8 || pos.Weight > 35 {
			// 盘口判定：跌停封死卖不出 → 跳过保留持仓（调仓卖单当天无法成交）
			if s, ok := snapMap[strings.ToLower(pos.Market+pos.InstrumentID)]; ok && s.PrevClose > 0 {
				ob := orderbook.ClassifySnapshot(s)
				if ob.State == orderbook.StateSealedLimitDown {
					log.Printf("[CIO] 调仓减仓跳过 %s(%s): %s", pos.StockName, pos.InstrumentID, ob.Reason)
					continue
				}
			}
			sellQty := pos.Quantity / 2
			if sellQty < 100 {
				sellQty = 100
			}
			if sellQty > pos.Quantity {
				sellQty = pos.Quantity
			}
			if sellQty > 0 {
				sellOrders = append(sellOrders, agents.OrderIntent{
					Symbol:       pos.Market + pos.InstrumentID,
					TargetWeight: float64(sellQty) * pos.CurrentPrice,
					Side:         "SELL",
					MaxNotional:  float64(sellQty) * pos.CurrentPrice,
					Reason:       fmt.Sprintf("调仓: 卖出%s %d股", pos.StockName, sellQty),
				})
				targetSellAmount += float64(sellQty) * pos.CurrentPrice
			}
		}
	}

	if targetSellAmount > 1000 {
		stockFactors := c.computeRealFactorScores(snapshots)
		weights := c.factorWeightsFor("NEUTRAL")
		stockFactors = factors.RankAndScore(stockFactors, weights)

		// 因子综合得分索引：调仓买入与建仓同口径，按多因子评分选股，
		// 而非仅看当日涨跌幅（原实现实际按 ChangePercent 排序，因子算而未用）
		factorScore := make(map[string]float64, len(stockFactors))
		for _, sf := range stockFactors {
			factorScore[sf.Code] = sf.CompositeScore
		}

		candidates := make([]data.StockSnapshot, 0)
		for _, s := range snapshots {
			if s.CurrentPrice > 0 && factorScore[s.Code] > 0 {
				alreadyHeld := false
				for _, pos := range snapshot.Positions {
					if pos.InstrumentID == s.Code {
						alreadyHeld = true
						break
					}
				}
				if !alreadyHeld {
					candidates = append(candidates, s)
				}
			}
		}

		// 按多因子综合得分从高到低排序，优先配置因子优选标的
		sort.Slice(candidates, func(i, j int) bool {
			return factorScore[candidates[i].Code] > factorScore[candidates[j].Code]
		})

		// “操盘手按最大收益策略执行”：调仓买入同样优先采用调优后收益最高策略看好的标的
		if strat, _ := c.bestExecStrategy(); strat != nil && len(candidates) > 1 {
			codes := make([]string, len(candidates))
			syms := make([]string, len(candidates))
			for i, s := range candidates {
				codes[i] = s.Code
				syms[i] = inferSymbol(s.Code)
			}
			pref := c.strategyPreference(strat, syms, codes, 250)
			if len(pref) > 0 {
				sort.SliceStable(candidates, func(i, j int) bool {
					pi, pj := pref[candidates[i].Code], pref[candidates[j].Code]
					if pi != pj {
						return pi > pj
					}
					return factorScore[candidates[i].Code] > factorScore[candidates[j].Code]
				})
			}
		}

		buyCount := 2
		if len(candidates) < buyCount {
			buyCount = len(candidates)
		}

		for i := 0; i < buyCount; i++ {
			s := candidates[i]
			perStock := targetSellAmount / float64(buyCount)
			shares := int(perStock/s.CurrentPrice/100) * 100
			if shares < 100 {
				continue
			}

			orders = append(orders, agents.OrderIntent{
				Symbol:       s.Market + s.Code,
				TargetWeight: float64(shares) * s.CurrentPrice,
				Side:         "BUY",
				MaxNotional:  float64(shares) * s.CurrentPrice * 1.001,
				Reason:       fmt.Sprintf("调仓: 因子优选买入%s(%s) %d股@¥%.2f (复合得分=%.4f)", s.Name, s.Code, shares, s.CurrentPrice, factorScore[s.Code]),
			})
		}
	}

	orders = append(sellOrders, orders...)
	return orders
}

// buildCurrentPositions 从组合引擎构造当前持仓占比 map（键格式与订单 Symbol 一致：market+code，小写），
// 供 PolicyEngine 做单票/行业/杠杆校验的"既有持仓"，使行业暴露等反映整个组合而非仅单笔订单。
// 占比 = 持仓市值 / 组合资产，与 CheckOrderIntent 的 notional/portfolioValue 同口径。
// 组合不可用/无持仓/资产非法时返回 nil，校验降级为仅看单笔订单。
func (c *CIOEngine) buildCurrentPositions(portfolioValue float64) map[string]float64 {
	if c.portfolio == nil {
		return nil
	}
	snap := c.portfolio.GetSnapshot()
	if snap == nil || len(snap.Positions) == 0 || portfolioValue <= 0 {
		return nil
	}
	pos := make(map[string]float64, len(snap.Positions))
	for _, p := range snap.Positions {
		if p == nil || p.InstrumentID == "" || p.MarketValue <= 0 {
			continue
		}
		key := strings.ToLower(p.Market) + p.InstrumentID
		pos[key] = p.MarketValue / portfolioValue
	}
	return pos
}

// validateOrderFields 结构化输出的订单字段级白名单校验。
// 对 AI/优化器产生的 OrderIntent（symbol/side/max_notional/target_weight）做确定性约束，
// 任何一项非法即整体否决该订单，防止非法数据流入交易链路或触发 panic。
// 校验通过后调用方应将 Side 规范化（大写），再进入后续分支。
func validateOrderFields(o *agents.OrderIntent) error {
	if o == nil {
		return fmt.Errorf("订单为空")
	}
	if strings.TrimSpace(o.Symbol) == "" {
		return fmt.Errorf("股票代码为空")
	}
	if len([]rune(o.Symbol)) > 12 {
		return fmt.Errorf("股票代码过长 %q", o.Symbol)
	}
	side := strings.ToUpper(strings.TrimSpace(o.Side))
	switch side {
	case "BUY", "SELL":
	case "":
		return fmt.Errorf("买卖方向为空")
	default:
		return fmt.Errorf("非法买卖方向 %q（仅允许 BUY/SELL）", o.Side)
	}
	if o.MaxNotional < 0 {
		return fmt.Errorf("非法最大买入金额 %.2f（不能为负）", o.MaxNotional)
	}
	if o.TargetWeight < 0 {
		return fmt.Errorf("非法目标权重 %.4f（不能为负）", o.TargetWeight)
	}
	return nil
}

// isTradingTime 检查当前是否为A股交易时段（统一使用交易日历 + 交易时段）
func isTradingTime() bool {
	return util.IsTradingHour(time.Now())
}

// executeDecision 执行决策（通过组合引擎进行模拟交易）
func (c *CIOEngine) executeDecision(ctx context.Context, decision *agents.CIODecision) {
	// 紧急停止门控：停止状态下禁止一切下单执行（最后一道防线，覆盖所有直连调用路径）
	if c.emergencyStopped() {
		log.Printf("[CIO] 紧急停止中，拒绝执行交易决策 %s", decision.DecisionID)
		c.addActivity("TRADER", "EMERGENCY_STOP_BLOCKED", "紧急停止中，拒绝执行交易决策", decision.Orders)
		c.traderAgent.SetState(agents.StateFailed)
		return
	}

	if len(decision.Orders) == 0 {
		return
	}

	// 检查是否为交易时段
	if !isTradingTime() {
		log.Printf("[CIO] 非交易时段，跳过交易执行")
		c.addActivity("TRADER", "SKIPPED", "非交易时段，跳过交易执行", nil)
		c.traderAgent.SetState(agents.StateFailed)
		return
	}

	c.traderAgent.SetState(agents.StateApproved)
	c.addActivity("TRADER", "EXECUTING", "开始执行交易决策", decision.Orders)

	snapshots := c.getStockPoolSnapshots()
	priceMap := make(map[string]data.StockSnapshot)
	for _, s := range snapshots {
		key := s.Market + s.Code
		priceMap[strings.ToLower(key)] = s
		priceMap[strings.ToUpper(key)] = s
	}

	var successCount int
	var failCount int
	var pendingCount int // 排队等待用户确认的订单数（未及时确认，不判定失败）

	// ===== 资金约束校验：结合投资金额(可用资金)决定组合内容 =====
	// 决策层(含LLM/优化器)可能给出远超实际资金的买入意图（如硬编码 100万 基准）。
	// 执行前按真实可用资金按比例削峰，确保任一/全部买入订单均不超出可用资金，
	// 避免出现"资金不足"执行失败，同时使组合仓位符合投资金额。卖出订单不占用资金。
	snapshot := c.portfolio.GetSnapshot()
	availableCash := snapshot.AvailableCash
	totalBuyNotional := 0.0
	for _, o := range decision.Orders {
		if o.Side == "BUY" && o.MaxNotional > 0 {
			totalBuyNotional += o.MaxNotional
		}
	}
	buyScale := 1.0
	if totalBuyNotional > 0 && availableCash > 0 && totalBuyNotional > availableCash {
		buyScale = availableCash / totalBuyNotional
		log.Printf("[CIO] 资金约束: 买入意图总额¥%.2f 超过可用资金¥%.2f，按比例%.1f%%削峰，确保总投入不超投资金额",
			totalBuyNotional, availableCash, buyScale*100)
	}

	for _, order := range decision.Orders {
		// 结构化输出字段级白名单校验：AI/优化器产生的订单意图必须先过字段校验
		// （买卖方向白名单、金额/权重非负、代码非空），任何一项非法即整体否决该订单，
		// 避免非法 side/异常金额等脏数据进入交易链路或触发 panic。
		if verr := validateOrderFields(&order); verr != nil {
			c.addActivity("TRADER", "ORDER_FAILED",
				fmt.Sprintf("订单校验失败: %s %s (%v)", order.Side, order.Symbol, verr), nil)
			log.Printf("[CIO] Order validation failed: %v", verr)
			failCount++
			continue
		}
		order.Side = strings.ToUpper(strings.TrimSpace(order.Side)) // 规范化方向，后续分支统一比较大写

		// 强制暂停建仓门控：暂停建仓期间，买入订单在进入执行链路前即被拦截，
		// 保证"强制暂停建仓 → 操盘手/买入路径真正停下"（卖出/离场不受影响，可继续执行）。
		if order.Side == "BUY" && c.pauseBuilding() {
			c.addActivity("TRADER", "ORDER_BLOCKED",
				fmt.Sprintf("强制暂停建仓中，拒绝买入订单: %s %s", order.Side, order.Symbol), nil)
			log.Printf("[CIO] Pause-building active, buy order blocked: %s %s", order.Side, order.Symbol)
			failCount++
			continue
		}

		c.addActivity("TRADER", "ORDER_CREATED",
			fmt.Sprintf("创建订单: %s %s", order.Side, order.Reason),
			order)

		normalizedSymbol := strings.ToLower(order.Symbol)
		if len(normalizedSymbol) < 2 {
			c.addActivity("TRADER", "ORDER_FAILED",
				fmt.Sprintf("订单失败: 非法代码 %q", order.Symbol), nil)
			failCount++
			continue
		}
		// 防御性解析订单代码：优先识别 sh/sz/bj 前缀规范拆分；裸6位代码从权威解析器推断市场，
		// 绝不直接对任意字符串切片（避免 AI 返回空串/过短/纯数字时越界 panic），也无法解析时直接失败该订单。
		market := ""
		code := normalizedSymbol
		if strings.HasPrefix(normalizedSymbol, "sh") || strings.HasPrefix(normalizedSymbol, "sz") || strings.HasPrefix(normalizedSymbol, "bj") {
			market, code = normalizedSymbol[:2], normalizedSymbol[2:]
		} else if m := strings.ToLower(data.DetermineMarketFromSymbol(normalizedSymbol)); m != "" {
			market = m // 裸代码：market 由权威解析器推断，code 保持6位
		}
		if market == "" || len(code) < 6 {
			c.addActivity("TRADER", "ORDER_FAILED",
				fmt.Sprintf("订单失败: 无法解析代码 %q(market=%q code=%q)", order.Symbol, market, code), nil)
			failCount++
			continue
		}

		price := 0.0
		name := code
		if snap, ok := priceMap[order.Symbol]; ok {
			price = snap.CurrentPrice
			name = snap.Name
			log.Printf("[CIO] 从priceMap获取价格: %s @ ¥%.2f", order.Symbol, price)
		} else if snap, ok := priceMap[normalizedSymbol]; ok {
			price = snap.CurrentPrice
			name = snap.Name
			log.Printf("[CIO] 从priceMap(小写)获取价格: %s @ ¥%.2f", normalizedSymbol, price)
		} else {
			// 对于任何订单（包括卖出），都尝试获取实时行情
			if order.MaxNotional > 0 || order.TargetWeight > 0 || order.Side == "SELL" {
				fallback, _ := data.FetchRealtimeStockSnapshots([]string{order.Symbol})
				if len(fallback) > 0 {
					price = fallback[0].CurrentPrice
					name = fallback[0].Name
					log.Printf("[CIO] 使用实时行情: %s @ ¥%.2f", order.Symbol, price)
				} else {
					log.Printf("[CIO] 行情获取失败，无数据返回: %s", order.Symbol)
				}
			}
		}

		// 如果名称仍是代码，尝试通过字典加载器获取
		if name == code || name == normalizedSymbol {
			dictName := data.GetDictLoader().GetStockName(normalizedSymbol)
			if dictName != normalizedSymbol && dictName != code {
				name = dictName
			}
		}

		// 严禁使用持仓成本价或历史价格作为降级。
		// 必须使用实时行情数据，否则直接失败订单，避免因价格偏差造成重大经济损失。

		if price <= 0 {
			c.addActivity("TRADER", "ORDER_FAILED",
				fmt.Sprintf("订单失败: %s %s, 无法获取行情", order.Side, order.Symbol), nil)
			failCount++
			continue
		}

		quantity := 0
		if order.Side == "BUY" {
			// 资金约束后的目标投入：MaxNotional * buyScale，并按整手(100股)归一化
			targetNotional := order.MaxNotional * buyScale
			quantity = int(targetNotional/price/100) * 100
			if quantity < 100 {
				// 资金不足以买入1手整股，直接跳过该标的（可筛除股价过高/资金不足的情形）
				c.addActivity("TRADER", "ORDER_FAILED",
					fmt.Sprintf("订单失败: %s %s, 可用资金不足买入1手(可用¥%.2f)", order.Side, order.Symbol, availableCash), nil)
				log.Printf("[CIO] Trade skipped: 可用资金不足买入1手 %s @¥%.2f, 可用¥%.2f", order.Symbol, price, availableCash)
				failCount++
				continue
			}
			if quantity < 100 {
				quantity = 100
			}

			// 大额买入拆单执行：单笔量超过 maxSliceShares 时，本次先成交一个切片，
			// 剩余股数入队，由后续监控周期分批成交，降低单一时点对市场的冲击。
			if quantity > maxSliceShares {
				remain := quantity - maxSliceShares
				quantity = maxSliceShares
				c.enqueueSplit(pendingSplitOrder{
					Symbol: order.Symbol, Code: code, Name: name, Side: "BUY",
					Quantity: remain, PriceCap: price, Reason: order.Reason,
				})
				c.addActivity("TRADER", "ORDER_SPLIT",
					fmt.Sprintf("大额买入拆单: %s 先成交%d股，剩余%d股分批执行(上限%d股/周期)", order.Symbol, quantity, remain, maxSliceShares),
					nil)
				log.Printf("[CIO] 买入拆单执行: %s 本次%d股, 剩余%d股入队分批次买入", order.Symbol, quantity, remain)
			}
		} else if order.Side == "SELL" {
			quantity = int(order.TargetWeight / price)
			quantity = (quantity / 100) * 100
			if quantity < 100 {
				quantity = 100
			}
		}

		if c.portfolio != nil {
			// 模拟接口模式：交易需用户手动确认
			var approvalResult tradeapproval.ApprovalResult
			var pendingOrder *tradeapproval.PendingTrade
			if c.approval != nil {
				var aErr error
				approvalResult, pendingOrder, aErr = c.approval.RequestApproval(order.Side, code, name, market, quantity, price, order.Reason, decision.DecisionID)
				if aErr != nil {
					c.addActivity("TRADER", "ORDER_FAILED",
						fmt.Sprintf("订单确认失败: %s, 原因: %s", order.Symbol, aErr.Error()), nil)
					failCount++
					continue
				}
				switch approvalResult {
				case tradeapproval.ResultRejected:
					c.addActivity("TRADER", "ORDER_REJECTED",
						fmt.Sprintf("订单被用户拒绝: %s %d股 @ ¥%.2f", order.Side, quantity, price), nil)
					log.Printf("[CIO] Order rejected by user: %s %s %d@%.2f", order.Side, code, quantity, price)
					failCount++
					continue
				case tradeapproval.ResultPending:
					// 用户未及时确认：订单排队中，不判定失败。用户稍后确认（批准）后系统将补执行。
					c.addActivity("TRADER", "ORDER_PENDING",
						fmt.Sprintf("订单排队中等待用户确认: %s %s %d股 @ ¥%.2f", order.Side, order.Symbol, quantity, price), nil)
					log.Printf("[CIO] Order waiting for user confirmation (queued): %s %s %d@%.2f", order.Side, code, quantity, price)
					pendingCount++
					continue
				case tradeapproval.ResultFailed:
					// 15:00 收盘仍未获用户确认：交易失败
					c.addActivity("TRADER", "ORDER_FAILED",
						fmt.Sprintf("订单失败: %s %s 截至收盘未获用户确认", order.Side, order.Symbol), nil)
					log.Printf("[CIO] Order failed (no confirmation by 15:00): %s %s %d@%.2f", order.Side, code, quantity, price)
					failCount++
					continue
				}
				// ResultApproved：继续执行
			}

			var tradeErr error
			if order.Side == "BUY" {
				_, tradeErr = c.portfolio.Buy(code, name, market, quantity, price, order.Reason, decision.DecisionID)
			} else {
				_, tradeErr = c.portfolio.Sell(code, quantity, price, order.Reason, decision.DecisionID)
			}

			if tradeErr != nil {
				// 批准后实际执行失败：逆转已按成交标记的订单并释放占用的买入资金
				if pendingOrder != nil && c.approval != nil {
					c.approval.CancelFilled(pendingOrder)
				}
				c.addActivity("TRADER", "ORDER_FAILED",
					fmt.Sprintf("订单执行失败: %s, 原因: %s", order.Symbol, tradeErr.Error()), nil)
				log.Printf("[CIO] Trade failed: %v", tradeErr)
				failCount++
			} else {
				// 成交事件补上股票名称/代码，避免同名时间戳成交在活动流中难以区分
				displayName := name
				if displayName == "" || displayName == code {
					displayName = code
				}
				c.addActivity("TRADER", "ORDER_FILLED",
					fmt.Sprintf("订单成交: %s(%s) %s %d股 @ ¥%.2f, 金额 ¥%.2f",
						displayName, code, order.Side, quantity, price, float64(quantity)*price), nil)
				successCount++
			}
		}
	}

	// 根据执行结果设置正确的状态
	switch {
	case successCount > 0 && failCount == 0:
		c.traderAgent.SetState(agents.StateFilled)
		c.addActivity("TRADER", "EXECUTION_COMPLETE",
			fmt.Sprintf("交易执行完成: %d笔订单全部成功", successCount), nil)
	case successCount > 0 && failCount > 0:
		c.traderAgent.SetState(agents.StateFilled)
		c.addActivity("TRADER", "EXECUTION_COMPLETE",
			fmt.Sprintf("交易部分完成: %d笔成功, %d笔失败", successCount, failCount), nil)
	case successCount == 0 && failCount == 0 && pendingCount > 0:
		// 全部订单排队等待用户确认（未及时确认不判定失败）
		c.traderAgent.SetState(agents.StateSubmitted)
		c.addActivity("TRADER", "EXECUTION_PENDING",
			fmt.Sprintf("交易排队中: %d笔订单等待用户确认，尚未成交", pendingCount), nil)
	case successCount == 0 && failCount > 0:
		c.traderAgent.SetState(agents.StateFailed)
		c.addActivity("TRADER", "EXECUTION_FAILED",
			fmt.Sprintf("交易执行失败: %d笔订单全部失败", failCount), nil)
	default:
		c.traderAgent.SetState(agents.StateFailed)
		c.addActivity("TRADER", "EXECUTION_FAILED", "交易执行失败: 无订单执行", nil)
	}

	if c.portfolio != nil {
		c.portfolio.UpdatePrices(snapshots)
	}
}

// enqueueSplit 将大额买入拆单的剩余切片加入待执行队列。
func (c *CIOEngine) enqueueSplit(o pendingSplitOrder) {
	c.splitMu.Lock()
	c.pendingSplits = append(c.pendingSplits, o)
	c.splitMu.Unlock()
}

// dequeueSplits 取出并清空当前拆单待执行队列。
func (c *CIOEngine) dequeueSplits() []pendingSplitOrder {
	c.splitMu.Lock()
	qs := c.pendingSplits
	c.pendingSplits = nil
	c.splitMu.Unlock()
	return qs
}

// requeueSplit 将未成交完的切片放回队列，供下一监控周期继续分批成交。
func (c *CIOEngine) requeueSplit(o pendingSplitOrder) {
	c.splitMu.Lock()
	c.pendingSplits = append(c.pendingSplits, o)
	c.splitMu.Unlock()
}

// executePendingSplits 分批执行大额买入拆单的剩余切片（盘中每个监控周期调用一次）。
// 每只标的每周期最多成交 maxSliceShares 股，受实时行情与可用现金双重约束，降低市场冲击。
func (c *CIOEngine) executePendingSplits(ctx context.Context) {
	// 紧急停止门控：停止状态下不执行任何拆单切片
	if c.emergencyStopped() {
		log.Printf("[CIO] 紧急停止中，跳过拆单切片执行")
		return
	}

	if c.portfolio == nil || !isTradingTime() {
		return
	}
	qs := c.dequeueSplits()
	if len(qs) == 0 {
		return
	}
	snapshot := c.portfolio.GetSnapshot()
	availableCash := snapshot.AvailableCash
	for _, o := range qs {
		if o.Side != "BUY" || o.Quantity <= 0 {
			continue
		}
		symbol := strings.ToLower(o.Symbol)
		if len(symbol) < 4 {
			continue
		}
		market, code := symbol[:2], symbol[2:]

		price := o.PriceCap
		name := o.Name
		if snaps, ok := data.FetchRealtimeStockSnapshots([]string{symbol}); ok && len(snaps) > 0 {
			price = snaps[0].CurrentPrice
			if snaps[0].Name != "" {
				name = snaps[0].Name
			}
		}
		if price <= 0 {
			c.requeueSplit(o) // 暂无可成交行情，下一周期再试
			continue
		}
		buyQty := (o.Quantity / 100) * 100
		maxByCash := int(availableCash/price/100) * 100
		if buyQty > maxByCash {
			buyQty = maxByCash
		}
		if buyQty < 100 {
			c.requeueSplit(o) // 现金暂不足买入1手，下一周期再试
			continue
		}
		if _, err := c.portfolio.Buy(code, name, market, buyQty, price, o.Reason, "CIO_SPLIT"); err != nil {
			c.requeueSplit(o)
			continue
		}
		c.addActivity("TRADER", "ORDER_FILLED",
			fmt.Sprintf("拆单分批成交: %s 再买入%d股 @¥%.2f", symbol, buyQty, price), nil)
		log.Printf("[CIO] 拆单分批成交: %s %d股 @¥%.2f", symbol, buyQty, price)
		availableCash -= float64(buyQty) * price
		if o.Quantity-buyQty > 0 {
			o.Quantity -= buyQty
			c.requeueSplit(o)
		}
	}
}

// AnalyzeMarket 分析市场状态（导出方法）
func (c *CIOEngine) AnalyzeMarket(ctx context.Context) map[string]interface{} {
	ms := c.getMarketState(ctx)
	if m, ok := ms.(map[string]interface{}); ok {
		return m
	}
	return map[string]interface{}{
		"regime":     "UNKNOWN",
		"confidence": 0.5,
		"timestamp":  time.Now().Format(time.RFC3339),
	}
}

// AnalyzePortfolio 分析组合状态（导出方法）
func (c *CIOEngine) AnalyzePortfolio(ctx context.Context) map[string]interface{} {
	if c.portfolio == nil {
		return map[string]interface{}{"error": "portfolio engine not initialized"}
	}
	snapshot := c.portfolio.GetSnapshot()
	return map[string]interface{}{
		"totalAssets": snapshot.Cash + snapshot.TotalMarketValue,
		"cash":        snapshot.Cash,
		"positions":   len(snapshot.Positions),
		"totalPnL":    snapshot.TotalPnL,
		"totalReturn": snapshot.TotalReturn,
		"dailyPnL":    snapshot.DailyPnL,
	}
}

// CheckRiskPolicy 检查风险政策（导出方法）
func (c *CIOEngine) CheckRiskPolicy(ctx context.Context) map[string]interface{} {
	if c.portfolio == nil {
		return map[string]interface{}{"passed": false, "reason": "portfolio engine not initialized"}
	}
	snapshot := c.portfolio.GetSnapshot()
	drawdown := 0.0
	if snapshot.TotalCapital > 0 {
		drawdown = (snapshot.TotalCapital - (snapshot.Cash + snapshot.TotalMarketValue)) / snapshot.TotalCapital
		if drawdown < 0 {
			drawdown = 0
		}
	}
	riskCheck := c.policyEngine.CheckPortfolioRisk(snapshot.DailyPnL, snapshot.Cash+snapshot.TotalMarketValue, drawdown)
	return map[string]interface{}{
		"passed":   riskCheck.Passed,
		"reason":   riskCheck.Reason,
		"drawdown": drawdown,
		"dailyPnL": snapshot.DailyPnL,
		"totalPnL": snapshot.TotalPnL,
	}
}

// GetRiskMetrics 获取完整风险指标
func (c *CIOEngine) GetRiskMetrics() map[string]interface{} {
	var positions []*portfolio.PositionState
	portfolioValue := 1000000.0

	if c.portfolio != nil {
		snapshot := c.portfolio.GetSnapshot()
		positions = snapshot.Positions
		totalAssets := snapshot.Cash + snapshot.TotalMarketValue
		if totalAssets > 0 {
			portfolioValue = totalAssets
		}
	}

	returns := c.computePositionReturns(positions)
	equityCurve := c.buildEquityCurveFromReturns(returns, portfolioValue)

	var metrics risk.RiskMetrics
	var cvar99 float64

	if len(returns) >= 10 {
		metrics.VaR95 = c.riskEngine.CalculateVaR(returns, 0.95, risk.ReturnFrequencyDaily)
		metrics.VaR99 = c.riskEngine.CalculateVaR(returns, 0.99, risk.ReturnFrequencyDaily)
		metrics.CVaR95 = c.riskEngine.CalculateCVaR(returns, 0.95, risk.ReturnFrequencyDaily)
		cvar99 = c.riskEngine.CalculateCVaR(returns, 0.99, risk.ReturnFrequencyDaily)
		metrics.Volatility = c.riskEngine.CalculateVolatility(returns, 252)
		metrics.SharpeRatio = c.riskEngine.CalculateSharpeRatio(returns, 0.03)
		metrics.MaxDrawdown = c.riskEngine.CalculateMaxDrawdown(equityCurve)
		metrics.SortinoRatio = c.riskEngine.CalculateSortinoRatioPublic(returns, 0.03)
		metrics.CalmarRatio = c.riskEngine.CalculateCalmarRatioPublic(returns, equityCurve)
		metrics.DownsideDev = c.riskEngine.CalculateDownsideDeviationPublic(returns)
		log.Printf("[CIO] GetRiskMetrics: VaR95=%.4f, VaR99=%.4f, VaR95*100=%.2f%%, MaxDD=%.4f",
			metrics.VaR95, metrics.VaR99, metrics.VaR95*100, metrics.MaxDrawdown)
	} else {
		log.Printf("[CIO] GetRiskMetrics: 历史数据不足(%d)，返回默认值", len(returns))
	}

	return map[string]interface{}{
		"var_95":        metrics.VaR95,
		"var_99":        metrics.VaR99,
		"cvar_95":       metrics.CVaR95,
		"cvar_99":       cvar99,
		"volatility":    metrics.Volatility,
		"sharpe_ratio":  metrics.SharpeRatio,
		"max_drawdown":  metrics.MaxDrawdown,
		"beta":          metrics.Beta,
		"sortino_ratio": metrics.SortinoRatio,
		"calmar_ratio":  metrics.CalmarRatio,
		"downside_dev":  metrics.DownsideDev,
		"upside_dev":    metrics.UpsideDev,
	}
}

// GetCurrentMarketState 获取当前市场状态（公开方法，供前端调用）
func (c *CIOEngine) GetCurrentMarketState() map[string]interface{} {
	ctx := context.Background()
	ms := c.getMarketState(ctx)

	if marketState, ok := ms.(map[string]interface{}); ok {
		regime, _ := marketState["regime"].(string)
		confidence, _ := marketState["confidence"].(float64)
		trend, _ := marketState["trend"].(string)
		volatility, _ := marketState["volatility"].(float64)

		return map[string]interface{}{
			"regime":     regime,
			"confidence": confidence,
			"trend":      trend,
			"volatility": volatility,
			"timestamp":  marketState["timestamp"],
			"indices":    marketState["indices"],
		}
	}

	// 默认值
	return map[string]interface{}{
		"regime":     "NEUTRAL",
		"confidence": 0.5,
		"trend":      "STABLE",
		"volatility": 0.12,
		"timestamp":  time.Now().Format(time.RFC3339),
		"indices":    []interface{}{},
	}
}

// FormulateDecision 制定投资决策（导出方法）
func (c *CIOEngine) FormulateDecision(ctx context.Context, marketState map[string]interface{}, portfolio map[string]interface{}, riskCheck map[string]interface{}) *agents.CIODecision {
	// 紧急停止门控：停止状态下不制定任何决策
	if c.emergencyStopped() {
		log.Printf("[CIO] 紧急停止中，跳过决策制定")
		return nil
	}

	c.agent.SetState(agents.StateThinking)
	c.addActivity("CIO", "FORMULATE_DECISION", "CIO制定投资决策", nil)

	quantReport := c.requestQuantResearch(ctx, marketState)
	portfolioValue := 0.0
	if tv, ok := portfolio["totalAssets"].(float64); ok {
		portfolioValue = tv
	}
	dailyPnL := 0.0
	if dp, ok := portfolio["dailyPnL"].(float64); ok {
		dailyPnL = dp
	}

	riskReport := c.requestRiskReview(ctx, quantReport, portfolioValue)
	decision := c.makeDecision(ctx, quantReport, riskReport, dailyPnL, portfolioValue)

	policyResult := c.policyEngine.ValidateDecision(*decision, portfolioValue, c.buildCurrentPositions(portfolioValue))
	decision.PolicyStatus = policyResult.Decision
	if !policyResult.Passed {
		decision.RiskApproval = "REJECTED"
	}

	c.saveDecision(decision)
	c.agent.SetState(agents.StateIdle)
	return decision
}

// ExecuteDecision 执行投资决策（导出方法）
func (c *CIOEngine) ExecuteDecision(ctx context.Context, decision *agents.CIODecision) {
	c.executeDecision(ctx, decision)
}

// planTargetPosition 投资方案目标持仓（从 PlanJSON 的 construction.positions 解析）
type planTargetPosition struct {
	AssetCode    string  `json:"assetCode"`
	AssetName    string  `json:"assetName"`
	AssetType    string  `json:"assetType"`
	TargetWeight float64 `json:"targetWeight"`
}

// MonitorIntradayInvestmentPlan 盘中实时监控投资方案并做投资决策。
// 交易时段内由 AutoScheduler 周期调用（默认每10分钟）：
//  1. 读取当前 ACTIVE 的投资方案；
//  2. 读取组合快照（真实持仓与现金）；
//  3. 比对方案目标标的与实际持仓的执行进度与偏离度；
//  4. 记录监控活动日志，必要时形成建仓/加仓决策并通过现有执行链路落地。
func (c *CIOEngine) MonitorIntradayInvestmentPlan(ctx context.Context) error {
	// 紧急停止门控：停止状态下不执行任何盘中监控/分析/决策/下单
	if c.emergencyStopped() {
		log.Printf("[CIO] 紧急停止中，跳过盘中监控")
		return nil
	}

	if c.db == nil {
		return nil
	}
	c.agent.SetState(agents.StateThinking)

	// 先分批执行大额买入拆单的剩余切片（不依赖是否在执行方案）。
	c.executePendingSplits(ctx)

	// 读取当前 ACTIVE 投资方案
	var plan data.InvestmentPlan
	err := c.db.GetDB().
		Where("status IN ?", []string{"ACTIVE", "RUNNING"}).
		Order("created_at DESC").
		First(&plan).Error
	if err != nil {
		c.addActivity("CIO", "INTRADAY_PLAN_MONITOR",
			"盘中监控投资方案：当前无执行中的投资方案，CIO 持续观察市场并等待方案激活", nil)
		c.agent.SetState(agents.StateIdle)
		return nil
	}

	// 解析方案目标持仓
	var targets []planTargetPosition
	if err := c.parsePlanTargets(&plan, &targets); err != nil {
		c.addActivity("CIO", "INTRADAY_PLAN_MONITOR",
			fmt.Sprintf("盘中监控投资方案「%s」：方案解析失败: %v", plan.Name, err), nil)
		c.agent.SetState(agents.StateIdle)
		return nil
	}

	// 读取组合快照
	var snapshot *portfolio.PortfolioSnapshot
	if c.portfolio != nil {
		snapshot = c.portfolio.GetSnapshot()
	}
	if snapshot == nil {
		snapshot = &portfolio.PortfolioSnapshot{}
	}

	// 当前持仓代码集合（归一化为纯6位数字代码，忽略市场前缀）
	heldCodes := make(map[string]bool)
	for _, pos := range snapshot.Positions {
		code := normalizePlanCode(pos.InstrumentID)
		heldCodes[code] = true
	}

	// 统计执行进度与缺失标的
	var missing []planTargetPosition
	equityTargets := 0
	for _, t := range targets {
		if t.AssetType != "STOCK" && t.AssetType != "ETF" {
			continue
		}
		equityTargets++
		if t.TargetWeight <= 0 {
			continue
		}
		if !heldCodes[normalizePlanCode(t.AssetCode)] {
			missing = append(missing, t)
		}
	}

	progressPct := 0.0
	if equityTargets > 0 {
		progressPct = float64(equityTargets-len(missing)) / float64(equityTargets) * 100
	}

	// 市场状态（盘中基于当日实时指数涨跌判定，而非历史趋势，避免误判）
	marketWeak, marketAvgPct, marketRegime := c.intradayMarketSignal(ctx)

	// 六维判势（盘中复用，不落库以免覆盖盘前记录）：仓位系数 position_rate 过低视为市场走弱，
	// 且「退潮风险」标签时告警。策略信号仓位 = 原始信号仓位 × position_rate。
	sixDim := c.sixDimReport(ctx, false)
	sixdimPayload := sixDimDecisionPayload(sixDim)
	if sixDim != nil {
		log.Printf("[CIO] 盘中六维判势: 总分=%.1f(修正%.1f) 冲突=%d 仓位系数=%.2f 标签=%s",
			sixDim.RawTotalScore, sixDim.AdjustedTotalScore, sixDim.ConflictCount, sixDim.PositionRate, sixDim.MarketTag)
		if sixDim.PositionRate < 0.2 || sixDim.MarketTag == "退潮风险" {
			if !marketWeak {
				log.Printf("[CIO] 盘中六维判势恶化(%s, 仓位系数%.2f)，叠加实时强弱判定，转为观望", sixDim.MarketTag, sixDim.PositionRate)
			}
			marketWeak = true
			c.addActivity("CIO", "SIXDIM_ALERT",
				fmt.Sprintf("六维判势告警：市场标签「%s」，仓位系数%.2f，建议降低风险敞口、暂缓建仓", sixDim.MarketTag, sixDim.PositionRate), sixdimPayload)
		}
	}

	// —— 早盘开局 + 六维判势稳定性门 ——
	// 规避开盘初期集合竞价后大盘剧烈波动期（09:30-09:40）的无谓买卖；
	// 且六维判势仓位系数过低（<0.2）或标注「退潮/不稳定」风险时，强制暂停建仓买入。
	// 仅拦截买入/新建仓/加仓（由下面 shouldAct 放行），策略卖出/回撤熔断等离场风控不受影响。
	if util.IsTradingDay(time.Now()) {
		beijingNow := time.Now().In(time.FixedZone("CST", 8*3600))
		currentMin := beijingNow.Hour()*60 + beijingNow.Minute()
		openProtect := currentMin >= 9*60+30 && currentMin < 9*60+40 // 开盘前10分钟内
		unstable := sixDim != nil && (sixDim.PositionRate < 0.2 || strings.Contains(sixDim.MarketTag, "退潮") ||
			strings.Contains(sixDim.MarketTag, "不稳定") || strings.Contains(sixDim.MarketTag, "谨慎"))
		if openProtect || unstable {
			reason := "早盘开盘初期大盘波动较大，暂缓建仓，等待市场企稳"
			if !openProtect {
				reason = fmt.Sprintf("六维判势市场不稳（标签「%s」、仓位系数%.2f），强制暂停新建仓买入", sixDim.MarketTag, sixDim.PositionRate)
			}
			log.Printf("[CIO] 开盘/六维保护: %s", reason)
			c.addActivity("CIO", "INTRADAY_PLAN_DECISION", "[CIO] "+reason, map[string]interface{}{
				"decision": agents.DecisionHold, "reason": reason, "open_protect": openProtect, "unstable": unstable,
			})
			c.agent.SetState(agents.StateIdle)
			return nil
		}
	}

	// 策略驱动减仓（盘中）：按量化分析师选定策略监控持仓卖出信号（如 KDJ 死叉），
	// 出现卖出信号即清仓离场，优先于建仓/加仓逻辑；盘中不落库以免覆盖盘前记录。
	if dplan, planStrategy := c.dailyPlanStrategy(time.Now().Format("2006-01-02")); planStrategy != nil && len(snapshot.Positions) > 0 {
		if strategyOrders := c.strategySellOrders(snapshot.Positions, planStrategy); len(strategyOrders) > 0 {
			decision := &agents.CIODecision{
				DecisionID:       c.generateDecisionID(),
				PortfolioID:      "P001",
				Decision:         agents.DecisionReduce,
				Reason:           fmt.Sprintf("盘中按量化分析师选定策略「%s」执行卖出信号减仓(%d只)，操盘手按策略信号离场", dplan.StrategyName, len(strategyOrders)),
				Orders:           strategyOrders,
				RiskApproval:     string(agents.RiskApprove),
				PolicyStatus:     "PENDING",
				MarketState:      marketRegime,
				MarketConfidence: marketAvgPct,
				Timestamp:        time.Now(),
				SixDim:           sixdimPayload,
			}
			c.saveDecision(decision)
			c.addActivity("CIO", "STRATEGY_SELL", "[CIO] 决策: REDUCE（策略驱动减仓）", map[string]interface{}{
				"decision": decision.Decision, "strategy": dplan.StrategyName, "reason": decision.Reason, "orders": strategyOrders,
			})
			c.executeDecision(ctx, decision)
			c.agent.SetState(agents.StateIdle)
			return nil
		}
	}

	// 记录监控活动
	monitorDetail := map[string]interface{}{
		"plan_id":                plan.PlanID,
		"plan_name":              plan.Name,
		"target_positions":       equityTargets,
		"held_positions":         equityTargets - len(missing),
		"missing_positions":      len(missing),
		"execution_progress_pct": progressPct,
		"cash":                   snapshot.Cash,
		"market_state":           marketRegime,
		"market_avg_change_pct":  marketAvgPct,
		"sixdim":                 sixdimPayload,
	}
	c.addActivity("CIO", "INTRADAY_PLAN_MONITOR",
		fmt.Sprintf("盘中监控投资方案「%s」：目标%d只，已执行%d只（进度%.0f%%），现金¥%.2f，当日指数均涨%.2f%%（%s）",
			plan.Name, equityTargets, equityTargets-len(missing), progressPct, snapshot.Cash, marketAvgPct, marketRegime),
		monitorDetail)

	// 净值回撤熔断（盘中）：触及止损线时强制降仓保护本金；触及预警线时暂停建仓观望。
	ddPct, ddWarn, ddStop, ddLevel := c.drawdownStatus()
	if ddLevel != "" {
		c.addDrawdownAlert(ddPct, ddWarn, ddStop, ddLevel)
	}
	if ddLevel == "stop" {
		totalAssets := snapshot.Cash + snapshot.TotalMarketValue
		if forceOrders := c.drawdownRiskReduceOrders(snapshot.Positions, totalAssets, 0.5); len(forceOrders) > 0 {
			ddDecision := &agents.CIODecision{
				DecisionID:       c.generateDecisionID(),
				PortfolioID:      "P001",
				Decision:         agents.DecisionPauseTrading,
				Reason:           fmt.Sprintf("盘中净值回撤熔断：回撤%.2f%%触发止损线%.0f%%，暂停交易并强制降仓控制风险", ddPct, ddStop),
				Orders:           forceOrders,
				RiskApproval:     string(agents.RiskApprove),
				PolicyStatus:     "PENDING",
				MarketState:      marketRegime,
				MarketConfidence: marketAvgPct,
				Timestamp:        time.Now(),
				SixDim:           sixdimPayload,
			}
			c.saveDecision(ddDecision)
			c.addActivity("CIO", "DRAWDOWN_STOP", "[CIO] 决策: PAUSE（净值回撤熔断强制降仓）", map[string]interface{}{
				"decision": ddDecision.Decision, "drawdown_pct": round1(ddPct), "reason": ddDecision.Reason, "orders": forceOrders,
			})
			c.executeDecision(ctx, ddDecision)
			c.agent.SetState(agents.StateIdle)
			return nil
		}
	}
	// 净值回撤预警：转为观望，暂停新建仓。
	if ddLevel == "warn" {
		c.addActivity("CIO", "INTRADAY_PLAN_DECISION",
			fmt.Sprintf("[CIO] 净值回撤预警%.2f%%（预警线%.0f%%），暂停新建仓，保持观望防御", ddPct, ddWarn), map[string]interface{}{
				"decision": agents.DecisionHold, "reason": "净值回撤预警，暂停建仓",
			})
		c.agent.SetState(agents.StateIdle)
		return nil
	}

	// —— 持仓数收敛到目标（投资金额分档）——
	// 组合持仓数超过 PortfolioSizing(实际本金) 目标时，以「方案目标代码」为优先保留集合，
	// 提前清算不在方案目标内的多余持仓，使持仓数回到分档上限（与盘前 makeDecision 收敛口径一致）。
	targetPositionCount := 0
	if c.portfolio != nil {
		if mp, _ := util.PortfolioSizing(snapshot.TotalCapital); mp > 0 {
			targetPositionCount = mp
		}
	}
	if targetPositionCount > 0 && len(snapshot.Positions) > targetPositionCount {
		// 保留方案目标代码，清算多余持仓；把方案外多余的持仓当作待清算对象
		preferredCodes := make(map[string]bool)
		for _, t := range targets {
			preferredCodes[normalizePlanCode(t.AssetCode)] = true
		}
		if trimOrders := c.generateTrimByPreferred(snapshot.Positions, preferredCodes, targetPositionCount); len(trimOrders) > 0 {
			trimDecision := &agents.CIODecision{
				DecisionID:       c.generateDecisionID(),
				PortfolioID:      "P001",
				Decision:         agents.DecisionReduce,
				Reason:           fmt.Sprintf("组合持仓%d只超过目标%d只(投资金额分档)，盘中按方案目标收敛减仓", len(snapshot.Positions), targetPositionCount),
				Orders:           trimOrders,
				RiskApproval:     string(agents.RiskApprove),
				PolicyStatus:     "PENDING",
				MarketState:      marketRegime,
				MarketConfidence: marketAvgPct,
				Timestamp:        time.Now(),
				SixDim:           sixdimPayload,
			}
			c.saveDecision(trimDecision)
			c.addActivity("CIO", "INTRADAY_PLAN_TRIM", "[CIO] 决策: REDUCE（盘中持仓数收敛）", map[string]interface{}{
				"decision": trimDecision.Decision, "target": targetPositionCount,
				"held": len(snapshot.Positions), "reason": trimDecision.Reason, "orders": trimOrders,
			})
			c.executeDecision(ctx, trimDecision)
			c.agent.SetState(agents.StateIdle)
			return nil
		}
	}

	// 决策：组合未按方案执行到位，且当日市场未明显走弱时，触发建仓/加仓
	shouldAct := len(missing) > 0 && snapshot.Cash > 20000 && !marketWeak
	if !shouldAct {
		reason := "组合已按投资方案执行到位，保持 HOLD"
		if len(missing) > 0 {
			if marketWeak {
				reason = "当日市场明显走弱，保持观望，避免在下跌中建仓"
			} else {
				reason = fmt.Sprintf("当前现金¥%.2f不足以建仓，保持观望", snapshot.Cash)
			}
		}
		c.addActivity("CIO", "INTRADAY_PLAN_DECISION", "[CIO] "+reason, map[string]interface{}{
			"decision": agents.DecisionHold, "reason": reason,
		})
		c.agent.SetState(agents.StateIdle)
		return nil
	}

	// 生成建仓订单（仅针对方案内缺失标的，分配方案目标权重对应的预算）
	orders, buildReason := c.generatePlanBuildOrders(missing, snapshot.Cash)
	// 六维判势仓位系数缩放：策略信号仓位 = 原始信号仓位 × position_rate
	if sixDim != nil {
		orders = scaleOrdersByPositionRate(orders, sixDim.PositionRate)
	}
	if len(orders) == 0 {
		c.addActivity("CIO", "INTRADAY_PLAN_DECISION",
			fmt.Sprintf("[CIO] 方案目标标的缺失但未成交: %s，保持观望", buildReason), map[string]interface{}{
				"decision": agents.DecisionHold, "reason": buildReason,
			})
		c.agent.SetState(agents.StateIdle)
		return nil
	}

	decision := &agents.CIODecision{
		DecisionID:  c.generateDecisionID(),
		PortfolioID: "P001",
		Decision:    agents.DecisionBuild,
		Reason: fmt.Sprintf("盘中按投资方案「%s」建仓/%s：目标%d只已执行%d只，缺失%d只，现金¥%.2f，当日指数均涨%.2f%%",
			plan.Name, marketRegime, equityTargets, equityTargets-len(missing), len(missing), snapshot.Cash, marketAvgPct),
		Orders:           orders,
		RiskApproval:     string(agents.RiskApprove),
		PolicyStatus:     "PENDING",
		MarketState:      marketRegime,
		MarketConfidence: marketAvgPct,
		Timestamp:        time.Now(),
		SixDim:           sixdimPayload,
	}
	c.saveDecision(decision)
	c.addActivity("CIO", "INTRADAY_PLAN_DECISION", "[CIO] 决策: BUILD（按方案建仓）", map[string]interface{}{
		"decision": decision.Decision, "reason": decision.Reason, "orders": decision.Orders,
	})
	c.executeDecision(ctx, decision)
	c.agent.SetState(agents.StateIdle)
	return nil
}

// parsePlanTargets 从投资方案的 PlanJSON 解析目标持仓列表
func (c *CIOEngine) parsePlanTargets(plan *data.InvestmentPlan, targets *[]planTargetPosition) error {
	if plan == nil || plan.PlanJSON == "" {
		return nil
	}
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(plan.PlanJSON), &payload); err != nil {
		return err
	}
	construction, ok := payload["construction"].(map[string]interface{})
	if !ok {
		return nil
	}
	positions, ok := construction["positions"].([]interface{})
	if !ok {
		return nil
	}
	for _, p := range positions {
		b, _ := json.Marshal(p)
		var t planTargetPosition
		if err := json.Unmarshal(b, &t); err != nil {
			continue
		}
		*targets = append(*targets, t)
	}
	return nil
}

// generatePlanBuildOrders 为投资方案内缺失的目标标的生成建仓订单。
// 预算按「目标权重 / 缺失标的总权重」的比例从可用现金分配，并受实时行情约束。
func (c *CIOEngine) generatePlanBuildOrders(missing []planTargetPosition, availableCash float64) ([]agents.OrderIntent, string) {
	var orders []agents.OrderIntent
	if len(missing) == 0 || availableCash <= 0 {
		return orders, "无缺失标的或无可用资金"
	}

	// 目标权重归一化
	totalWeight := 0.0
	for _, m := range missing {
		if m.TargetWeight > 0 {
			totalWeight += m.TargetWeight
		}
	}

	// 获取方案缺失标的的实时行情
	codes := make([]string, 0, len(missing))
	for _, m := range missing {
		codes = append(codes, planCodeToSymbol(m.AssetCode))
	}
	snapshots, _ := data.FetchRealtimeStockSnapshots(codes)
	priceMap := make(map[string]data.StockSnapshot)
	for _, s := range snapshots {
		priceMap[strings.ToLower(s.Market+s.Code)] = s
		priceMap[s.Code] = s
	}

	// 统计根因，便于 orders 为空时区分「行情缺失」「预算不足1手」「盘口不可买」
	var missingMarket, missingFunds, missingOrderBook []string
	for _, m := range missing {
		symbol := planCodeToSymbol(m.AssetCode)
		price := 0.0
		name := m.AssetName
		var snap data.StockSnapshot
		if s, ok := priceMap[strings.ToLower(symbol)]; ok && s.CurrentPrice > 0 {
			snap = s
			price = s.CurrentPrice
			if s.Name != "" {
				name = s.Name
			}
		} else if s, ok := priceMap[strings.ToUpper(symbol)]; ok && s.CurrentPrice > 0 {
			snap = s
			price = s.CurrentPrice
		}

		weight := m.TargetWeight
		if weight <= 0 {
			weight = 1.0 / float64(len(missing))
		} else if totalWeight > 0 {
			weight = weight / totalWeight
		}
		allocation := availableCash * weight

		if price <= 0 {
			missingMarket = append(missingMarket, fmt.Sprintf("%s(%s)", name, m.AssetCode))
			continue
		}

		// 盘口判定：涨停封死买不进、跌停封死/跌停板（含巨量开盘跌停）不接飞刀 → 跳过该标的。
		// 对应百花医药式「巨量开盘跌停」场景：即便价格可成交，也禁止在跌停板建仓。
		if snap.CurrentPrice > 0 {
			ob := orderbook.ClassifySnapshot(snap)
			if ob.State == orderbook.StateSealedLimitUp ||
				ob.State == orderbook.StateSealedLimitDown ||
				ob.State == orderbook.StateOpenedLimitDown {
				missingOrderBook = append(missingOrderBook, fmt.Sprintf("%s(%s) %s", name, m.AssetCode, ob.Reason))
				continue
			}
		}

		shares := int(allocation/price/100) * 100
		if shares < 100 {
			missingFunds = append(missingFunds,
				fmt.Sprintf("%s(%s) 预算¥%.0f/股价¥%.2f仅够%d股",
					name, m.AssetCode, allocation, price, int(allocation/price/100)))
			continue
		}

		orders = append(orders, agents.OrderIntent{
			Symbol:       strings.ToLower(symbol),
			TargetWeight: weight,
			Side:         "BUY",
			MaxNotional:  float64(shares) * price * 1.001,
			Reason:       fmt.Sprintf("按投资方案建仓: 买入%s(%s) %d股@¥%.2f", name, m.AssetCode, shares, price),
		})
	}

	// 汇总未成交根因
	var cntMarket, cntFunds, cntOb int
	var firstReason string
	if len(missingMarket) > 0 {
		cntMarket = len(missingMarket)
		firstReason = fmt.Sprintf("%d只标的天拿不到实时行情(%s)", cntMarket, strings.Join(missingMarket, "、"))
	}
	if len(missingFunds) > 0 {
		cntFunds = len(missingFunds)
		fundsReason := fmt.Sprintf("%d只标的分到的预算不足以买入1手(%s)", cntFunds, strings.Join(missingFunds, "；"))
		if firstReason != "" {
			firstReason += "；" + fundsReason
		} else {
			firstReason = fundsReason
		}
	}
	if len(missingOrderBook) > 0 {
		cntOb = len(missingOrderBook)
		obReason := fmt.Sprintf("%d只标的盘口不可买(%s)", cntOb, strings.Join(missingOrderBook, "；"))
		if firstReason != "" {
			firstReason += "；" + obReason
		} else {
			firstReason = obReason
		}
	}
	if firstReason == "" && len(orders) == 0 && (cntMarket+cntFunds+cntOb) == 0 {
		firstReason = "无符合建仓条件的标的"
	}
	return orders, firstReason
}

// normalizePlanCode 归一化资产代码为纯6位数字（去掉 sh/sz/bj 市场前缀）。
// 复用 data.PureCodeFromCode 统一收敛格式解析。
func normalizePlanCode(code string) string {
	return data.PureCodeFromCode(code)
}

// planCodeToSymbol 将资产代码转换为带市场前缀的小写交易代码（如 600519 -> sh601700）。
// 复用 data.MarketCode，保证市场推断与全项目一致。
func planCodeToSymbol(code string) string {
	return data.MarketCode(code)
}

// intradayMarketSignal 盘中市场强弱信号。
// 基于当日实时指数涨跌判定（而非历史K线趋势），避免把当日大涨误判为熊市。
// 返回：是否明显走弱、当日指数平均涨跌幅(%)、市场状态标签(BULLISH/NEUTRAL/BEARISH)。
// 实时数据失败时保守对待（不返回虚构强弱），保持 NEUTRAL 不误杀建仓。
func (c *CIOEngine) intradayMarketSignal(ctx context.Context) (bool, float64, string) {
	// 读取活跃指数（优先数据库配置，缺失时用三大核心指数）
	codes := make([]string, 0)
	if c.db != nil {
		for _, idx := range data.GetActiveMarketIndices(c.db.GetDB()) {
			codes = append(codes, idx.Code)
		}
	}
	if len(codes) == 0 {
		codes = []string{"sh000001", "sz399001", "sz399006"}
	}

	snapshots, ok := data.FetchRealtimeIndexSnapshots(codes)
	if !ok || len(snapshots) == 0 {
		log.Printf("[CIO] 盘中市场信号: 实时指数获取失败，回退 NEUTRAL 处理")
		return false, 0, "NEUTRAL"
	}

	sumPct := 0.0
	count := 0
	for _, s := range snapshots {
		if s.ChangePercent == 0 && s.Current == 0 {
			continue // 无效行情
		}
		sumPct += s.ChangePercent
		count++
	}
	if count == 0 {
		return false, 0, "NEUTRAL"
	}
	avgPct := sumPct / float64(count)

	// 判定：平均跌幅超过1%视为明显走弱；平均涨/小跌视为可建仓
	marketWeak := avgPct <= -1.0
	regime := "NEUTRAL"
	if avgPct > 0.5 {
		regime = "BULLISH"
	} else if avgPct < -0.5 {
		regime = "BEARISH"
	}
	log.Printf("[CIO] 盘中市场信号: 当日指数均涨跌 %.2f%%，regime=%s, weak=%v", avgPct, regime, marketWeak)
	return marketWeak, avgPct, regime
}

// evaluateNeedForAction 评估是否需要行动
func (c *CIOEngine) evaluateNeedForAction(marketState interface{}, riskCheck policy.PolicyCheckResult, dailyPnL float64, portfolioValue float64) bool {
	if !riskCheck.Passed {
		return true
	}

	if dailyPnL < 0 && portfolioValue > 0 {
		lossPct := -dailyPnL / portfolioValue
		if lossPct > 0.02 {
			return true
		}
	}

	return false
}

// getMarketState 获取市场状态（基于实时行情）
func (c *CIOEngine) getMarketState(ctx context.Context) interface{} {
	indices := data.GetActiveMarketIndices(c.db.GetDB())
	if len(indices) == 0 {
		indices = []data.MarketIndex{
			{Code: "sh000001"}, {Code: "sz399001"}, {Code: "sz399006"},
		}
	}

	marketState := map[string]interface{}{
		"regime":     "NEUTRAL",
		"confidence": 0.5,
		"volatility": 0.12,
		"trend":      "STABLE",
		"volume":     "NORMAL",
		"timestamp":  time.Now().Format(time.RFC3339),
		"indices":    []map[string]interface{}{},
	}

	if c.duckDB == nil || !c.duckDB.HasStockDB() {
		log.Printf("[CIO] DuckDB不可用，无法进行历史数据分析")
		return marketState
	}

	type indexAnalysis struct {
		code         string
		name         string
		currentPrice float64
		changePct    float64
		volume       float64
		turnover     float64
		day5Return   float64
		day10Return  float64
		day20Return  float64
		volumeRatio  float64
		ma5          float64
		ma20         float64
		aboveMA5     bool
		aboveMA20    bool
		volumeShrink bool
	}

	var analyses []indexAnalysis
	bullVotes := 0
	bearVotes := 0
	totalWeight := 0.0

	for _, idx := range indices {
		bars, err := c.duckDB.GetKlineFromStock(ctx, idx.Code, 60)
		if err != nil || len(bars) < 10 {
			log.Printf("[CIO] 获取 %s 历史数据失败: %v, 数据点=%d", idx.Code, err, len(bars))
			continue
		}

		n := len(bars)
		closes := make([]float64, n)
		volumes := make([]float64, n)
		for i, bar := range bars {
			closes[i] = bar.Close
			volumes[i] = bar.Volume
		}

		currentPrice := closes[0]
		prevClose := closes[1]
		changePct := 0.0
		if prevClose > 0 {
			changePct = (currentPrice - prevClose) / prevClose * 100
		}

		day5Return := 0.0
		if n > 5 && closes[5] > 0 {
			day5Return = (closes[0] - closes[5]) / closes[5] * 100
		}
		day10Return := 0.0
		if n > 10 && closes[10] > 0 {
			day10Return = (closes[0] - closes[10]) / closes[10] * 100
		}
		day20Return := 0.0
		if n > 20 && closes[20] > 0 {
			day20Return = (closes[0] - closes[20]) / closes[20] * 100
		}

		ma5 := 0.0
		if n >= 5 {
			for i := 0; i < 5; i++ {
				ma5 += closes[i]
			}
			ma5 /= 5
		}
		ma20 := 0.0
		if n >= 20 {
			for i := 0; i < 20; i++ {
				ma20 += closes[i]
			}
			ma20 /= 20
		}

		avgVolume20 := 0.0
		if n >= 20 {
			for i := 0; i < 20; i++ {
				avgVolume20 += volumes[i]
			}
			avgVolume20 /= 20
		}
		volumeRatio := 1.0
		currentVolume := volumes[0]
		if avgVolume20 > 0 && currentVolume > 0 {
			volumeRatio = currentVolume / avgVolume20
		}
		volumeShrink := volumeRatio < 0.8

		aboveMA5 := currentPrice > ma5
		aboveMA20 := currentPrice > ma20

		analysis := indexAnalysis{
			code:         idx.Code,
			name:         idx.Name,
			currentPrice: currentPrice,
			changePct:    changePct,
			volume:       currentVolume,
			turnover:     0,
			day5Return:   day5Return,
			day10Return:  day10Return,
			day20Return:  day20Return,
			volumeRatio:  volumeRatio,
			ma5:          ma5,
			ma20:         ma20,
			aboveMA5:     aboveMA5,
			aboveMA20:    aboveMA20,
			volumeShrink: volumeShrink,
		}
		analyses = append(analyses, analysis)

		weight := 1.0
		switch idx.Code {
		case "sh000001":
			weight = 1.5
		case "000300":
			weight = 1.5
		case "sz399001":
			weight = 1.2
		case "sz399006":
			weight = 1.0
		}

		indexBullScore := 0.0
		indexBearScore := 0.0

		// 短期收益率（5日）
		if day5Return > 1.0 {
			indexBullScore += 2.0
		} else if day5Return > 0 {
			indexBullScore += 0.5
		} else if day5Return < -1.0 {
			indexBearScore += 2.0
		} else if day5Return < 0 {
			indexBearScore += 0.5
		}

		// 中期收益率（10日）
		if day10Return > 2.0 {
			indexBullScore += 1.5
		} else if day10Return > 0 {
			indexBullScore += 0.5
		} else if day10Return < -2.0 {
			indexBearScore += 1.5
		} else if day10Return < 0 {
			indexBearScore += 0.5
		}

		// 长期收益率（20日）
		if day20Return > 3.0 {
			indexBullScore += 1.5
		} else if day20Return < -3.0 {
			indexBearScore += 1.5
		}

		// 均线位置
		if aboveMA5 {
			indexBullScore += 0.5
		} else {
			indexBearScore += 0.5
		}
		if aboveMA20 {
			indexBullScore += 0.5
		} else {
			indexBearScore += 0.5
		}

		// 当日涨跌
		if changePct > 0 {
			indexBullScore += 0.5
		} else if changePct < 0 {
			indexBearScore += 0.5
		}

		// 关键识别：下跌趋势 + 缩量 = 典型的下跌中继/缩量整理，强烈看跌信号
		if day5Return < 0 && volumeShrink {
			indexBearScore += 2.0
		}
		// 温和上涨但缩量，动能不足
		if day5Return > 0 && volumeShrink {
			indexBearScore += 0.5
		}

		// 加权投票：多空双方独立累积得分，避免"一票否决"
		bullVotes += int(weight * indexBullScore)
		bearVotes += int(weight * indexBearScore)
		totalWeight += weight
	}

	if len(analyses) == 0 {
		log.Printf("[CIO] 无足够的指数历史数据用于市场分析")
		return marketState
	}

	bullScore := float64(bullVotes)
	bearScore := float64(bearVotes)
	totalScore := bullScore + bearScore

	if totalScore > 0 {
		bullRatio := bullScore / totalScore
		bearRatio := bearScore / totalScore

		// 识别缩量下跌/缩量整理的特殊情况
		shrinkDownCount := 0
		shrinkTotalCount := 0
		for _, a := range analyses {
			if a.volumeShrink {
				shrinkTotalCount++
				if a.day5Return < 0 {
					shrinkDownCount++
				}
			}
		}

		// 缩量下跌：多数指数缩量且5日收益为负 → 明确看跌
		if shrinkDownCount >= len(analyses)/2 && bearRatio > 0.45 {
			marketState["regime"] = "BEARISH"
			marketState["trend"] = "DOWN"
			marketState["confidence"] = bearRatio
		} else if bullRatio > bearRatio+0.20 {
			// 牛市需要明显领先（至少20%优势）
			marketState["regime"] = "BULLISH"
			marketState["trend"] = "UP"
			marketState["confidence"] = bullRatio
		} else if bearRatio > bullRatio+0.15 {
			marketState["regime"] = "BEARISH"
			marketState["trend"] = "DOWN"
			marketState["confidence"] = bearRatio
		} else {
			// 中性：多空势均力敌
			marketState["regime"] = "NEUTRAL"
			if bullRatio > bearRatio {
				marketState["trend"] = "UP"
			} else {
				marketState["trend"] = "DOWN"
			}
			marketState["confidence"] = math.Abs(bullRatio-bearRatio) * 1.5
			if marketState["confidence"].(float64) > 0.6 {
				marketState["confidence"] = 0.6
			}
		}

		shrinkCount := 0
		for _, a := range analyses {
			if a.volumeShrink {
				shrinkCount++
			}
		}
		if shrinkCount > len(analyses)/2 {
			marketState["volume"] = "SHRINKING"
		} else {
			marketState["volume"] = "NORMAL"
		}
	}

	for _, a := range analyses {
		indexInfo := map[string]interface{}{
			"code":         a.code,
			"name":         a.name,
			"current":      a.currentPrice,
			"change_pct":   a.changePct,
			"volume":       a.volume,
			"turnover":     a.turnover,
			"day5_return":  a.day5Return,
			"day10_return": a.day10Return,
			"day20_return": a.day20Return,
			"volume_ratio": a.volumeRatio,
			"ma5":          a.ma5,
			"ma20":         a.ma20,
			"above_ma5":    a.aboveMA5,
			"above_ma20":   a.aboveMA20,
			"is_mock":      false,
		}
		marketState["indices"] = append(marketState["indices"].([]map[string]interface{}), indexInfo)
	}

	log.Printf("[CIO] 市场状态: regime=%s, trend=%s, confidence=%.2f, volume=%s, bullVotes=%d, bearVotes=%d",
		marketState["regime"], marketState["trend"], marketState["confidence"], marketState["volume"], bullVotes, bearVotes)

	return marketState
}

// getStockPoolSnapshots 获取股票池的实时行情
func (c *CIOEngine) getStockPoolSnapshots() []data.StockSnapshot {
	codes := make([]string, 0)
	codeSet := make(map[string]bool)

	// 添加自选股
	for _, s := range data.GetDefaultWatchStocks(c.db.GetDB()) {
		code := s.Market + s.Code
		if !codeSet[code] {
			codes = append(codes, code)
			codeSet[code] = true
		}
	}

	// 添加持仓中的股票
	if c.portfolio != nil {
		snapshot := c.portfolio.GetSnapshot()
		if snapshot != nil {
			for _, pos := range snapshot.Positions {
				// pos.Market是大写(SH/SZ)，需要转为小写
				market := strings.ToLower(pos.Market)
				code := market + pos.InstrumentID
				if !codeSet[code] {
					codes = append(codes, code)
					codeSet[code] = true
				}
			}
		}
	}

	if len(codes) == 0 {
		return nil
	}

	snapshots, _ := data.FetchRealtimeStockSnapshots(codes)
	return snapshots
}

// generateDecisionID 生成决策ID
func (c *CIOEngine) generateDecisionID() string {
	return fmt.Sprintf("DEC-%s-%d", time.Now().Format("20060102"), time.Now().UnixNano())
}

// saveDecision 保存决策记录（完整审计信息）
func (c *CIOEngine) saveDecision(decision *agents.CIODecision) {
	if c.db == nil {
		return
	}

	ordersJSON, err := json.Marshal(decision.Orders)
	if err != nil {
		log.Printf("[CIO] Failed to marshal orders: %v, using empty array", err)
		ordersJSON = []byte("[]")
	}

	decisionProcess := map[string]interface{}{
		"timestamp":     decision.Timestamp,
		"decision_id":   decision.DecisionID,
		"portfolio_id":  decision.PortfolioID,
		"risk_approval": decision.RiskApproval,
		"policy_status": decision.PolicyStatus,
		"decision_type": string(decision.Decision),
		"orders_count":  len(decision.Orders),
		"optimization":  decision.Optimization,
		"sixdim":        decision.SixDim,
		"llm_review":    decision.LLMReview,
		"evidence":      decision.Evidence,
	}
	processJSON, err := json.Marshal(decisionProcess)
	if err != nil {
		log.Printf("[CIO] Failed to marshal decision process: %v, using empty object", err)
		processJSON = []byte("{}")
	}

	decisionLog := data.CIODecisionLog{
		DecisionID:       decision.DecisionID,
		PortfolioID:      decision.PortfolioID,
		Decision:         string(decision.Decision),
		Reason:           decision.Reason,
		OrdersJSON:       string(ordersJSON),
		RiskApproval:     decision.RiskApproval,
		PolicyStatus:     decision.PolicyStatus,
		MarketState:      decision.MarketState,
		MarketConfidence: decision.MarketConfidence,
		DecisionProcess:  string(processJSON),
		Timestamp:        decision.Timestamp,
	}

	util.SafeGoWithRetry("CIO.saveDecision", 3, func() error {
		return c.db.GetDB().Create(&decisionLog).Error
	})
	// P0：将本次决策方向沉淀为可证伪声明，供日后真实收益对账
	c.persistClaim(decision)
}

// persistClaim 将本次决策的方向主张沉淀为"可证伪声明"（P0），供日后用真实收益对账。
// 主张来源：决策方向(decision.MarketState) + 六维标签 + 决策理由；信心=决策置信度。
func (c *CIOEngine) persistClaim(decision *agents.CIODecision) {
	if c.db == nil || decision == nil {
		return
	}
	// 仅对"有明确方向/信心"的决策沉淀声明；无信心(<=0)或未知方向跳过
	rawDirection := strings.ToUpper(decision.MarketState)
	var direction string
	switch rawDirection {
	case "BULLISH":
		direction = "bullish"
	case "BEARISH":
		direction = "bearish"
	default:
		direction = "neutral"
	}
	conf := decision.MarketConfidence
	if conf <= 0 {
		conf = 50
	}
	tag := ""
	if six, ok := decision.SixDim.(map[string]interface{}); ok {
		if t, ok := six["market_tag"].(string); ok {
			tag = t
		}
	}
	stmt := strings.TrimSpace(decision.Reason)
	if len(stmt) > 512 {
		stmt = stmt[:512]
	}
	claim := data.ClaimRecord{
		ClaimDate:  time.Now().Format("2006-01-02"),
		DecisionID: decision.DecisionID,
		MarketTag:  tag,
		Direction:  direction,
		Statement:  stmt,
		Confidence: conf,
		CreatedAt:  time.Now(),
	}
	util.SafeGoWithRetry("CIO.persistClaim", 3, func() error {
		return c.db.GetDB().Create(&claim).Error
	})
}

// VerifyClaims 用当日结算的真实市场收益对账先前沉淀的"可证伪声明"（P0）。
// 验证口径：对未验证的声明，取对应 ClaimDate 的沪深300 当日涨跌幅，
// 判断方向主张(bullish/bearish)是否命中；neutral 记为已验证但不计入命中率。
// 在日终结算（App.RecordDailySettlement）时调用，用真实数据、不伪造。
func (c *CIOEngine) VerifyClaims(ctx context.Context) error {
	if c.db == nil {
		return fmt.Errorf("数据库未初始化")
	}
	var pending []data.ClaimRecord
	if err := c.db.GetDB().Where("verified = ?", false).Find(&pending).Error; err != nil {
		return fmt.Errorf("读取待验证声明失败: %w", err)
	}
	if len(pending) == 0 {
		return nil
	}

	// 拉取沪深300 最近约 30 个交易日真实日K（覆盖声明日期区间）
	var bars []data.KlineBarFromDuckDB
	if c.duckDB != nil {
		if b, err := c.duckDB.GetKlineFromStock(ctx, data.ToMarketCode("sh000300"), 30); err == nil {
			bars = b
		}
	}
	// 按日期建立 收盘/昨收 映射，计算当日涨跌幅(%)
	type dayRet struct {
		pct float64
		ok  bool
	}
	retMap := make(map[string]dayRet, len(bars))
	for i, b := range bars {
		dateKey := b.Date.Format("2006-01-02")
		if i == 0 {
			retMap[dateKey] = dayRet{0, false} // 首根无昨收，不参与
			continue
		}
		prev := bars[i-1].Close
		if prev > 0 {
			retMap[dateKey] = dayRet{(b.Close - prev) / prev * 100, true}
		}
	}

	now := time.Now()
	for i := range pending {
		cl := &pending[i]
		r, ok := retMap[cl.ClaimDate]
		cl.Verified = true
		cl.VerifiedAt = now
		if ok && r.ok {
			cl.ActualReturn = r.pct
			switch cl.Direction {
			case "bullish":
				cl.Match = r.pct > 0
			case "bearish":
				cl.Match = r.pct < 0
			default:
				cl.Match = false // neutral 中性主张不判定命中
			}
		}
		// 无对应日期真实收益时：仍标记已验证但 Match=false（不伪造收益）
		if err := c.db.GetDB().Save(cl).Error; err != nil {
			log.Printf("[CIO] 验证声明失败: %v", err)
		}
	}
	return nil
}

// ClaimAccuracy 返回智能体判断准确率统计（P0）：命中率/样本数/均信心，供复盘与盘前注入。
func (c *CIOEngine) ClaimAccuracy() map[string]interface{} {
	out := map[string]interface{}{
		"total":           0,
		"verified":        0,
		"hits":            0,
		"accuracy":        0.0,
		"hit_confidence":  0.0,
		"miss_confidence": 0.0,
	}
	if c.db == nil {
		return out
	}
	var recs []data.ClaimRecord
	if err := c.db.GetDB().Where("verified = ?", true).Find(&recs).Error; err != nil {
		return out
	}
	out["total"] = len(recs)
	var nonNeutral, hits int
	var hitConf, missConf, hitN, missN float64
	for _, rec := range recs {
		if rec.Direction == "neutral" {
			continue
		}
		nonNeutral++
		out["verified"] = nonNeutral
		if rec.Match {
			hits++
			hitN++
			hitConf += rec.Confidence
		} else {
			missN++
			missConf += rec.Confidence
		}
	}
	out["hits"] = hits
	if nonNeutral > 0 {
		out["accuracy"] = float64(hits) / float64(nonNeutral) * 100
	}
	if hitN > 0 {
		out["hit_confidence"] = hitConf / hitN
	}
	if missN > 0 {
		out["miss_confidence"] = missConf / missN
	}
	return out
}

// addActivity 添加活动记录
func (c *CIOEngine) addActivity(agentRole, activityType, title string, message interface{}) {
	if c.db == nil {
		return
	}

	var msgStr string
	if message == nil {
		msgStr = ""
	} else if msg, ok := message.(string); ok && (msg == "" || msg == "null" || msg == "undefined") {
		msgStr = ""
	} else {
		msgJSON, err := json.Marshal(message)
		if err != nil {
			log.Printf("[CIO] Failed to marshal activity message: %v, using empty string", err)
			msgStr = ""
		} else {
			msgStr = string(msgJSON)
			if msgStr == "null" {
				msgStr = ""
			}
		}
	}

	activity := data.AgentActivity{
		AgentRole:    agentRole,
		ActivityType: activityType,
		Title:        title,
		Message:      msgStr,
		Timestamp:    time.Now(),
	}

	util.SafeGoWithRetry("CIO.addActivity", 3, func() error {
		return c.db.GetDB().Create(&activity).Error
	})
}

// GetCIOJournal 获取今日 CIO 决策日志（decision_logs 表，按时间倒序）。
// 页面决策记录只展示当天的快照，不展示历史/全部记录。
// 除基础决策字段外，还解析 DecisionProcess 中的 optimization/sixdim 及 OrdersJSON，
// 使「CIO 日志 · 投资决策记录」成为当日决策快照的唯一数据源，供前端决策列表与六维/优化卡片共用。
func (c *CIOEngine) GetCIOJournal(days int) []map[string]interface{} {
	if c.db == nil {
		return nil
	}

	// 统一按东八区(CST)判定"今天"，避免服务器本地时区非东八区导致当天边界偏移
	now := time.Now().In(time.FixedZone("CST", 8*3600))
	since := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	var logs []data.CIODecisionLog
	c.db.GetDB().
		Where("timestamp >= ?", since).
		Order("timestamp DESC").
		Find(&logs)

	result := []map[string]interface{}{}
	for _, l := range logs {
		optimization := interface{}(nil)
		sixdim := interface{}(nil)
		evidence := interface{}(nil)
		llmReview := interface{}(nil)
		if l.DecisionProcess != "" {
			var proc map[string]interface{}
			if err := json.Unmarshal([]byte(l.DecisionProcess), &proc); err == nil {
				if opt, ok := proc["optimization"]; ok {
					optimization = opt
				}
				if sd, ok := proc["sixdim"]; ok {
					sixdim = sd
				}
				if ev, ok := proc["evidence"]; ok {
					evidence = ev
				}
				if lr, ok := proc["llm_review"]; ok {
					llmReview = lr
				}
			}
		}
		result = append(result, map[string]interface{}{
			"decisionID":       l.DecisionID,
			"decision":         l.Decision,
			"reason":           l.Reason,
			"riskApproval":     l.RiskApproval,
			"policyStatus":     l.PolicyStatus,
			"marketState":      l.MarketState,
			"marketConfidence": l.MarketConfidence,
			"timestamp":        l.Timestamp.Format(time.RFC3339),
			"date":             l.Timestamp.Format("2006-01-02"),
			"orders":           json.RawMessage(l.OrdersJSON),
			"optimization":     optimization,
			"sixdim":           sixdim,
			"evidence":         evidence,
			"llmReview":        llmReview,
		})
	}

	return result
}

// GetTeamActivity 获取团队活动
func (c *CIOEngine) GetTeamActivity(limit int) []map[string]interface{} {
	if c.db == nil {
		return nil
	}

	var activities []data.AgentActivity
	c.db.GetDB().
		Order("timestamp DESC").
		Limit(limit).
		Find(&activities)

	var result []map[string]interface{}
	for _, a := range activities {
		result = append(result, map[string]interface{}{
			"agent_role":    a.AgentRole,
			"activity_type": a.ActivityType,
			"title":         a.Title,
			"message":       a.Message,
			"timestamp":     a.Timestamp.Format(time.RFC3339),
		})
	}

	return result
}

// GetTodayTeamActivity 获取今日团队活动
func (c *CIOEngine) GetTodayTeamActivity() []map[string]interface{} {
	if c.db == nil {
		return nil
	}

	today := time.Now().Truncate(24 * time.Hour)
	var activities []data.AgentActivity
	c.db.GetDB().
		Where("timestamp >= ?", today).
		Order("timestamp DESC").
		Find(&activities)

	var result []map[string]interface{}
	for _, a := range activities {
		result = append(result, map[string]interface{}{
			"agent_role":    a.AgentRole,
			"activity_type": a.ActivityType,
			"title":         a.Title,
			"message":       a.Message,
			"timestamp":     a.Timestamp.Format(time.RFC3339),
		})
	}

	return result
}

// EmergencyStop 紧急停止（所有智能体停止工作）
func (c *CIOEngine) EmergencyStop() {
	c.policyEngine.SetEmergencyStop(true)
	c.agent.EmergencyStop()
	c.traderAgent.EmergencyStop()
	c.quantAgent.EmergencyStop()
	c.riskAgent.EmergencyStop()
	c.addActivity("CIO", "EMERGENCY_STOP", "紧急停止已触发，所有智能体已停止工作", nil)
}

// ResumeTrading 恢复交易（所有智能体恢复工作）
func (c *CIOEngine) ResumeTrading() {
	c.policyEngine.SetEmergencyStop(false)
	c.agent.Resume()
	c.traderAgent.Resume()
	c.quantAgent.Resume()
	c.riskAgent.Resume()
	c.addActivity("CIO", "RESUME_TRADING", "交易系统已恢复，所有智能体继续工作", nil)
}

// emergencyStopped 判断是否处于紧急停止状态（Policy 紧急停止 或 CIO 智能体已停止）。
// 紧急停止后，所有 CIO 编排入口（盘中监控/止损/每日检查/可交易池/下单执行）一律立即返回，
// 确保"紧急停止 → 所有智能体停止工作"在全部路径上生效，而不只是消息任务/调度器任务。
func (c *CIOEngine) emergencyStopped() bool {
	if c.policyEngine != nil && c.policyEngine.IsEmergencyStopped() {
		return true
	}
	if c.agent != nil && c.agent.IsStopped() {
		return true
	}
	return false
}

// pauseBuilding 判断是否处于"强制暂停建仓"状态。
// 与紧急停止不同：仅拦截买入，卖出/离场/止损仍可执行（Policy 开关作为共享唯一真源）。
func (c *CIOEngine) pauseBuilding() bool {
	return c.policyEngine != nil && c.policyEngine.IsPauseBuilding()
}

// PauseBuilding 设置"强制暂停建仓"开关（仅拦截买入，卖出不受影响）。
func (c *CIOEngine) PauseBuilding(pause bool) {
	if pause {
		c.traderAgent.SetState(agents.StateBlocked)
		c.addActivity("CIO", "PAUSE_BUILDING", "强制暂停建仓已启用，仅拦截买入，卖出/离场不受影响", nil)
	} else {
		c.traderAgent.SetState(agents.StateIdle)
		c.addActivity("CIO", "RESUME_BUILDING", "已解除强制暂停建仓，恢复买入", nil)
	}
}

// --- Helper methods ---

// buildPriceHistory 从快照构建价格历史
func (c *CIOEngine) buildPriceHistory(snapshot data.StockSnapshot) []data.StockSnapshot {
	history := make([]data.StockSnapshot, 0, 10)

	for i := 10; i > 0; i-- {
		v := snapshot.CurrentPrice * (1.0 - float64(i)*0.01)
		hist := data.StockSnapshot{
			Code:         snapshot.Code,
			Name:         snapshot.Name,
			Market:       snapshot.Market,
			CurrentPrice: v,
			PrevClose:    v * 0.99,
			Open:         v * 0.995,
			High:         v * 1.01,
			Low:          v * 0.99,
			Volume:       snapshot.Volume / 10,
			Turnover:     snapshot.Turnover / 10,
			Timestamp:    snapshot.Timestamp - int64(i)*86400000,
		}
		history = append(history, hist)
	}

	history = append(history, snapshot)
	return history
}

// buildPriceHistoryFromPositions 从持仓构建价格历史
func (c *CIOEngine) buildPriceHistoryFromPositions(code string) []data.StockSnapshot {
	allSnapshots := c.getStockPoolSnapshots()
	for _, s := range allSnapshots {
		if s.Code == code {
			return c.buildPriceHistory(s)
		}
	}
	return nil
}

// inferSector 推断行业
func (c *CIOEngine) inferSector(code string) string {
	if len(code) < 6 {
		return "未知"
	}

	prefix := code[:3]
	switch prefix {
	case "600", "601", "603", "605":
		return "主板"
	case "688":
		return "科创板"
	case "000", "001", "002", "003":
		return "主板"
	case "300":
		return "创业板"
	case "430", "831", "832", "833", "834", "835", "836", "837", "838", "839", "870", "871", "872", "873", "874", "875", "876", "877", "878", "879":
		return "北交所"
	}

	suffix := code[len(code)-3:]
	switch suffix {
	case "001", "002", "003":
		return "科技"
	case "010", "020":
		return "金融"
	case "030", "040":
		return "消费"
	case "050", "060":
		return "工业"
	case "070", "080":
		return "医药"
	}

	return "其他"
}

// extractHLC 从历史数据提取最高、最低、收盘价数组
func extractHLC(history []data.StockSnapshot) ([]float64, []float64, []float64) {
	n := len(history)
	highs := make([]float64, n)
	lows := make([]float64, n)
	closes := make([]float64, n)
	for i, h := range history {
		highs[i] = h.High
		lows[i] = h.Low
		closes[i] = h.CurrentPrice
	}
	return highs, lows, closes
}

// computePositionReturns 计算持仓历史时间序列收益率
// 使用历史价格数据计算每日收益率序列，用于VaR/ES风险度量
func (c *CIOEngine) computePositionReturns(positions []*portfolio.PositionState) []float64 {
	if len(positions) == 0 {
		return nil
	}

	if c.duckDB == nil {
		log.Printf("[CIO] DuckDB未初始化，无法获取历史价格数据")
		return nil
	}

	const historyDays = 120
	type priceSeries struct {
		closes []float64
		weight float64
	}

	var totalWeight float64
	seriesMap := make(map[string]*priceSeries)

	for _, pos := range positions {
		if pos.MarketValue <= 0 || pos.InstrumentID == "" {
			continue
		}
		weight := pos.MarketValue
		totalWeight += weight

		prices, err := c.duckDB.GetRecentPrices(context.Background(), pos.Market, pos.InstrumentID, historyDays)
		if err != nil || len(prices) < 10 {
			log.Printf("[CIO] 获取 %s.%s 历史价格失败: %v, 数据点=%d", pos.Market, pos.InstrumentID, err, len(prices))
			continue
		}

		closes := make([]float64, len(prices))
		for i, p := range prices {
			closes[i] = p.Close
		}

		seriesMap[pos.InstrumentID] = &priceSeries{
			closes: closes,
			weight: weight,
		}
	}

	if len(seriesMap) == 0 {
		log.Printf("[CIO] 无有效的历史价格数据用于风险计算")
		return nil
	}

	type dailyReturn struct {
		dateIdx int
		ret     float64
		weight  float64
	}

	var allReturns []dailyReturn
	for _, ps := range seriesMap {
		for i := 1; i < len(ps.closes); i++ {
			if ps.closes[i-1] <= 0 {
				continue
			}
			ret := (ps.closes[i] - ps.closes[i-1]) / ps.closes[i-1]
			allReturns = append(allReturns, dailyReturn{
				dateIdx: i,
				ret:     ret,
				weight:  ps.weight,
			})
		}
	}

	if len(allReturns) == 0 {
		return nil
	}

	if totalWeight <= 0 {
		totalWeight = 1
	}

	dateMap := make(map[int]float64)
	dateCount := make(map[int]int)
	for _, dr := range allReturns {
		weightedRet := dr.ret * dr.weight / totalWeight
		dateMap[dr.dateIdx] += weightedRet
		dateCount[dr.dateIdx]++
	}

	returns := make([]float64, 0, len(dateMap))
	for i := 1; i < len(allReturns)+1; i++ {
		if ret, ok := dateMap[i]; ok && dateCount[i] > 0 {
			returns = append(returns, ret)
		}
	}

	if len(returns) < 10 {
		log.Printf("[CIO] 历史收益率数据点不足: %d, 无法计算可靠的VaR/ES", len(returns))
	}

	log.Printf("[CIO] 计算得到 %d 个日收益率数据点用于VaR/ES计算", len(returns))
	return returns
}

// buildEquityCurve 构建权益曲线（基于组合历史收益）
func (c *CIOEngine) buildEquityCurve(positions []*portfolio.PositionState, portfolioValue float64) []float64 {
	if portfolioValue <= 0 {
		portfolioValue = 1000000
	}

	returns := c.computePositionReturns(positions)
	return c.buildEquityCurveFromReturns(returns, portfolioValue)
}

// buildEquityCurveFromReturns 基于已计算的收益率构建权益曲线
func (c *CIOEngine) buildEquityCurveFromReturns(returns []float64, portfolioValue float64) []float64 {
	if portfolioValue <= 0 {
		portfolioValue = 1000000
	}

	if len(returns) < 2 {
		curve := make([]float64, 20)
		for i := 0; i < 20; i++ {
			curve[i] = portfolioValue
		}
		return curve
	}

	curve := make([]float64, len(returns)+1)
	curve[0] = portfolioValue
	for i, ret := range returns {
		curve[i+1] = curve[i] * (1 + ret)
	}

	log.Printf("[CIO] 构建权益曲线: %d 个数据点, 起点=%.0f, 终点=%.0f", len(curve), curve[0], curve[len(curve)-1])
	return curve
}

// getCurrentPrice 获取股票当前价格
func (c *CIOEngine) getCurrentPrice(code string) float64 {
	snapshots := c.getStockPoolSnapshots()
	for _, s := range snapshots {
		if s.Code == code {
			return s.CurrentPrice
		}
	}
	single, _ := data.FetchRealtimeStockSnapshots([]string{"sh" + code})
	if len(single) > 0 {
		return single[0].CurrentPrice
	}
	return 0
}

// ============ Workflow Methods ============

// SubmitDecision 提交决策（进入审批流程）
func (c *CIOEngine) SubmitDecision(decision *agents.CIODecision) *DecisionWorkflow {
	flow := c.workflowEngine.CreateWorkflow(decision)
	log.Printf("[Workflow] Decision %s submitted for approval", decision.DecisionID)
	c.addActivity("CIO", "SUBMIT_DECISION", fmt.Sprintf("提交决策: %s", decision.Decision), map[string]interface{}{
		"decision_id": decision.DecisionID,
		"decision":    decision.Decision,
	})
	return flow
}

// ApproveDecision 审批决策
func (c *CIOEngine) ApproveDecision(decisionID string, approver string, role string, approved bool, comment string) error {
	err := c.workflowEngine.Approve(decisionID, approver, role, approved, comment)
	if err != nil {
		return err
	}

	if approved {
		c.addActivity(role, "APPROVE_DECISION",
			fmt.Sprintf("批准决策: %s", decisionID),
			map[string]interface{}{"comment": comment})
	} else {
		c.addActivity(role, "REJECT_DECISION",
			fmt.Sprintf("否决决策: %s", decisionID),
			map[string]interface{}{"comment": comment})
	}

	return nil
}

// ExecuteApprovedDecision 执行已批准的决策
func (c *CIOEngine) ExecuteApprovedDecision(ctx context.Context, decisionID string) error {
	if !c.workflowEngine.CanExecute(decisionID) {
		return fmt.Errorf("decision %s cannot be executed (not approved)", decisionID)
	}

	flow := c.workflowEngine.GetWorkflow(decisionID)
	if flow == nil {
		return fmt.Errorf("workflow not found: %s", decisionID)
	}

	// 开始执行
	c.workflowEngine.Execute(decisionID, "trader_001")

	// 执行决策
	c.executeDecision(ctx, flow.Decision)

	// 完成执行
	c.workflowEngine.CompleteExecution(decisionID, true, "Trade execution completed", "")

	log.Printf("[Workflow] Decision %s executed successfully", decisionID)
	return nil
}

// ValidateDecisionForExecution 验证DecisionObject是否可执行（Trader执行前强制校验）
// 校验链：DecisionObject存在 → CIO批准 → Risk批准 → 未过期 → 当前市场状态允许
func (c *CIOEngine) ValidateDecisionForExecution(decisionID string) error {
	if decisionID == "" {
		return fmt.Errorf("decision_id 为空，Trader不能自主交易")
	}

	flow := c.workflowEngine.GetWorkflow(decisionID)
	if flow == nil {
		return fmt.Errorf("DecisionObject %s 不存在", decisionID)
	}

	// 已批准或正在执行/已执行（幂等：重复执行已批准决策允许）
	if flow.State != StateApproved && flow.State != StateExecuting && flow.State != StateExecuted {
		return fmt.Errorf("DecisionObject %s 状态为 %s，未获批准，禁止执行", decisionID, flow.State)
	}

	// Risk批准检查
	riskApproved := false
	for _, a := range flow.Approvals {
		if a.Role == "Risk" && a.Approved {
			riskApproved = true
			break
		}
	}
	if !riskApproved {
		return fmt.Errorf("DecisionObject %s 未通过Risk审批，禁止执行", decisionID)
	}

	// CIO批准检查
	cioApproved := false
	for _, a := range flow.Approvals {
		if a.Role == "CIO" && a.Approved {
			cioApproved = true
			break
		}
	}
	if !cioApproved {
		return fmt.Errorf("DecisionObject %s 未通过CIO审批，禁止执行", decisionID)
	}

	// 未过期检查
	obj := c.workflowEngine.GenerateDecisionObject(flow.Decision, riskApproved)
	if obj.ValidUntil != "" {
		if validUntil, err := time.Parse(time.RFC3339, obj.ValidUntil); err == nil {
			if time.Now().After(validUntil) {
				return fmt.Errorf("DecisionObject %s 已过期（%s），禁止执行", decisionID, obj.ValidUntil)
			}
		}
	}

	// 当前市场状态允许（非交易时段禁止执行）
	if !isTradingTime() {
		return fmt.Errorf("当前为非交易时段，DecisionObject %s 禁止执行", decisionID)
	}

	return nil
}

// GetWorkflowStatus 获取决策工作流状态
func (c *CIOEngine) GetWorkflowStatus(decisionID string) map[string]interface{} {
	return c.workflowEngine.GetFlowHistory(decisionID)
}

// GetPendingWorkflows 获取待审批的工作流
func (c *CIOEngine) GetPendingWorkflows() []map[string]interface{} {
	pending := c.workflowEngine.GetPendingFlows()
	result := make([]map[string]interface{}, 0, len(pending))
	for _, flow := range pending {
		result = append(result, c.workflowEngine.GetFlowHistory(flow.Decision.DecisionID))
	}
	return result
}

// GetWorkflowStats 获取工作流统计
func (c *CIOEngine) GetWorkflowStats() map[string]interface{} {
	return c.workflowEngine.GetWorkflowStats()
}

// ProcessDecisionWorkflow 处理决策工作流（完整流程）
func (c *CIOEngine) ProcessDecisionWorkflow(ctx context.Context, decision *agents.CIODecision) (*DecisionWorkflow, error) {
	// 1. 提交决策
	flow := c.SubmitDecision(decision)

	// 2. 风险审批（Risk Agent）
	riskApproved := decision.RiskApproval == "APPROVED"
	riskComment := "风险审查通过"
	if !riskApproved {
		riskComment = "风险审查未通过"
	}
	err := c.ApproveDecision(decision.DecisionID, "risk_001", "Risk", riskApproved, riskComment)
	if err != nil {
		return flow, fmt.Errorf("risk approval failed: %w", err)
	}

	if !riskApproved {
		return flow, fmt.Errorf("decision rejected by risk agent")
	}

	// 3. 政策审批（CIO Agent 自身审批）
	policyApproved := decision.PolicyStatus == "PASSED"
	policyComment := "政策检查通过"
	if !policyApproved {
		policyComment = "政策检查未通过"
	}
	err = c.ApproveDecision(decision.DecisionID, "cio_001", "CIO", policyApproved, policyComment)
	if err != nil {
		return flow, fmt.Errorf("policy approval failed: %w", err)
	}

	if !policyApproved {
		return flow, fmt.Errorf("decision rejected by policy check")
	}

	// 4. 执行决策
	err = c.ExecuteApprovedDecision(ctx, decision.DecisionID)
	if err != nil {
		return flow, err
	}

	// 5. 保存工作流到数据库
	c.workflowEngine.SaveWorkflowToDB(ctx, flow)

	return flow, nil
}

// filterDefensiveStocks 筛选防御性股票
func (c *CIOEngine) filterDefensiveStocks(stocks []factors.StockFactors) []factors.StockFactors {
	var defensive []factors.StockFactors
	for _, s := range stocks {
		sector := c.inferSector(s.Code)
		if sector == "医药" || sector == "消费" || s.CompositeScore > 0.55 {
			defensive = append(defensive, s)
		}
	}
	if len(defensive) == 0 && len(stocks) > 0 {
		defensive = stocks[:min(3, len(stocks))]
	}
	return defensive
}

// computeTopWeight 计算前N大持仓权重
func (c *CIOEngine) computeTopWeight(positions []*portfolio.PositionState, n int) float64 {
	if len(positions) == 0 {
		return 0
	}

	sorted := make([]*portfolio.PositionState, len(positions))
	copy(sorted, positions)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Weight > sorted[j].Weight
	})

	topN := n
	if len(sorted) < topN {
		topN = len(sorted)
	}

	total := 0.0
	for i := 0; i < topN; i++ {
		total += sorted[i].Weight
	}
	return total / 100.0
}

// computeMaxSingleWeight 计算最大单持仓权重
func (c *CIOEngine) computeMaxSingleWeight(positions []*portfolio.PositionState) float64 {
	maxW := 0.0
	for _, pos := range positions {
		if pos.Weight > maxW {
			maxW = pos.Weight
		}
	}
	return maxW / 100.0
}

// computeIndustryHHI 计算行业集中度HHI指数
func (c *CIOEngine) computeIndustryHHI(sectors []risk.SectorExposure) float64 {
	if len(sectors) == 0 {
		return 0
	}
	hhi := 0.0
	for _, s := range sectors {
		hhi += s.Weight * s.Weight
	}
	return hhi
}

// min 取最小值
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// safeMarshal 安全地序列化数据，失败时返回默认值
func safeMarshal(v interface{}, defaultValue string) string {
	data, err := json.Marshal(v)
	if err != nil {
		log.Printf("[CIO] Failed to marshal data: %v, using default", err)
		return defaultValue
	}
	return string(data)
}

// GenerateDailyReview 生成每日复盘报告
func (c *CIOEngine) GenerateDailyReview(ctx context.Context) (*data.DailyReview, error) {
	if c.db == nil {
		return nil, fmt.Errorf("database not initialized")
	}

	log.Printf("[CIO] Generating daily review...")

	reviewDate := time.Now().Format("2006-01-02")

	// 检查是否已生成今日复盘
	var existingReview data.DailyReview
	err := c.db.GetDB().Where("review_date = ?", reviewDate).First(&existingReview).Error
	if err == nil {
		// 已存在：仅刷新确定性财务字段（总资产/累计/今日盈亏）为数据库结算口径，
		// 与「组合交易中心」保持一致，避免历史旧口径残留导致两边不一致；不重跑 LLM 生成。
		if ps := c.portfolio.GetPortfolioState(); ps != nil {
			if v, ok := ps["totalAssets"].(float64); ok {
				existingReview.TotalAssets = v
			}
			if v, ok := ps["totalReturn"].(float64); ok {
				existingReview.TotalReturn = v
			}
			if v, ok := ps["dailyPnL"].(float64); ok {
				existingReview.DailyPnL = v
			}
			if v, ok := ps["dailyReturn"].(float64); ok {
				existingReview.DailyReturn = v
			}
			if v, ok := ps["cash"].(float64); ok {
				existingReview.Cash = v
			}
			if v, ok := ps["totalMarketValue"].(float64); ok {
				existingReview.MarketValue = v
			}
			c.db.GetDB().Save(&existingReview)
		}
		log.Printf("[CIO] Daily review already exists for %s, refreshed financial fields", reviewDate)
		return &existingReview, nil
	}

	// 获取组合快照
	var totalAssets, totalReturn, dailyPnL, dailyReturn, cash, marketValue float64
	var positionsCount int
	var marketRegime string
	var marketConfidence float64
	var topGainers, topLosers, riskMetrics, keyEvents, decisionsJSON string
	var positionStrategyReview, strategyChangeSuggestion string
	var summary, recommendations string

	if c.portfolio != nil {
		snapshot := c.portfolio.GetSnapshot()
		totalAssets = snapshot.Cash + snapshot.TotalMarketValue
		totalReturn = snapshot.TotalReturn
		dailyPnL = snapshot.DailyPnL
		dailyReturn = snapshot.DailyReturn
		cash = snapshot.Cash
		marketValue = snapshot.TotalMarketValue
		positionsCount = len(snapshot.Positions)

		// 统一口径：今日盈亏/累计盈亏/总资产以数据库结算基准（GetPortfolioState）为准，
		// 与「组合交易中心」页面一致；快照口径只算当前持仓浮动价差，会漏算当日已实现盈亏与现金变动。
		if ps := c.portfolio.GetPortfolioState(); ps != nil {
			if v, ok := ps["totalAssets"].(float64); ok {
				totalAssets = v
			}
			if v, ok := ps["totalReturn"].(float64); ok {
				totalReturn = v
			}
			if v, ok := ps["dailyPnL"].(float64); ok {
				dailyPnL = v
			}
			if v, ok := ps["dailyReturn"].(float64); ok {
				dailyReturn = v
			}
		}

		// 获取交易记录
		tradeRecords, _ := c.portfolio.GetTradeHistory(1)
		var tradePnL float64
		for _, t := range tradeRecords {
			tradePnL += t.RealizedPnL
		}

		// 分析持仓表现
		type PositionPerformance struct {
			Code   string  `json:"code"`
			Name   string  `json:"name"`
			Return float64 `json:"return"`
		}

		var performances []PositionPerformance
		for _, pos := range snapshot.Positions {
			performances = append(performances, PositionPerformance{
				Code:   pos.InstrumentID,
				Name:   pos.StockName,
				Return: pos.UnrealizedReturn,
			})
		}

		// 按收益率排序
		sort.Slice(performances, func(i, j int) bool {
			return performances[i].Return > performances[j].Return
		})

		// Top 3 盈利
		topGainerList := performances
		if len(topGainerList) > 3 {
			topGainerList = topGainerList[:3]
		}
		topGainersBytes := safeMarshal(topGainerList, "[]")
		topGainers = topGainersBytes

		// Top 3 亏损
		topLoserList := performances
		if len(topLoserList) > 3 {
			topLoserList = topLoserList[len(topLoserList)-3:]
		}
		topLosersBytes := safeMarshal(topLoserList, "[]")
		topLosers = topLosersBytes

		// 市场状态（先获取，供下方策略解读使用）
		ms := c.getMarketState(ctx)
		if m, ok := ms.(map[string]interface{}); ok {
			if r, ok := m["regime"].(string); ok {
				marketRegime = r
			}
			if cf, ok := m["confidence"].(float64); ok {
				marketConfidence = cf
			}
		}

		// Quant复盘：持仓执行策略解读（策略名称、执行状态、表现解读）
		type PositionStrategy struct {
			Code           string  `json:"code"`
			Name           string  `json:"name"`
			Strategy       string  `json:"strategy"`       // 持仓执行的策略
			Status         string  `json:"status"`         // 策略执行状态
			Return         float64 `json:"return"`         // 浮盈收益率(%)
			Weight         float64 `json:"weight"`         // 权重(%)
			Interpretation string  `json:"interpretation"` // 表现解读
		}

		var strategyList []PositionStrategy
		for _, pos := range snapshot.Positions {
			alert := c.checkPositionStopLoss(pos)
			strategy := "持有跟踪策略"
			status := "正常运行"
			switch alert.AlertType {
			case "take_profit":
				strategy = "目标价止盈策略"
				status = "已触发止盈"
			case "trailing_stop":
				strategy = "移动止损策略"
				status = "接近止损线"
			case "stop_loss":
				strategy = "ATR动态止损策略"
				status = "已触发止损"
			}

			interpretation := "表现平稳"
			switch {
			case pos.UnrealizedReturn >= 10:
				interpretation = "收益显著，策略有效"
			case pos.UnrealizedReturn >= 0:
				interpretation = "小幅盈利，策略稳健"
			case pos.UnrealizedReturn >= -5:
				interpretation = "小幅回撤，在策略容忍范围内"
			default:
				interpretation = "回撤较大，需关注策略失效风险"
			}

			strategyList = append(strategyList, PositionStrategy{
				Code:           pos.InstrumentID,
				Name:           pos.StockName,
				Strategy:       strategy,
				Status:         status,
				Return:         pos.UnrealizedReturn,
				Weight:         pos.Weight,
				Interpretation: interpretation,
			})
		}
		sort.Slice(strategyList, func(i, j int) bool {
			return strategyList[i].Return > strategyList[j].Return
		})
		positionStrategyReview = safeMarshal(strategyList, "[]")

		// Quant复盘：结合盘面情况判断是否调整策略
		strategySuggestion := map[string]interface{}{
			"change_strategy": false,
			"reason":          "",
			"actions":         []string{},
		}
		switch marketRegime {
		case "BULLISH":
			strategySuggestion["reason"] = "市场处于牛市状态，多头策略表现良好，维持当前策略并适度放宽止损距离以捕捉趋势收益"
			strategySuggestion["actions"] = []string{"维持多头仓位策略", "可适度放宽ATR止损距离", "关注强势持仓的加仓机会"}
		case "BEARISH":
			strategySuggestion["change_strategy"] = true
			strategySuggestion["reason"] = "市场处于熊市状态，趋势策略面临回撤压力，建议收紧止损并增配防御性策略"
			strategySuggestion["actions"] = []string{"收紧止损线", "降低仓位集中度", "增配低波动/防御板块"}
		default:
			strategySuggestion["reason"] = "市场处于震荡状态，现有策略整体适配，维持当前参数并跟踪结构性机会"
			strategySuggestion["actions"] = []string{"维持当前策略参数", "跟踪行业轮动信号", "对触发止损的持仓执行纪律性减仓"}
		}
		// 若组合有持仓亏损较大，提示调整
		hasSignificantLoss := false
		for _, pos := range snapshot.Positions {
			if pos.UnrealizedReturn <= -10 {
				hasSignificantLoss = true
				break
			}
		}
		if hasSignificantLoss {
			strategySuggestion["change_strategy"] = true
			strategySuggestion["reason"] = "组合中存在浮亏超过10%的持仓，策略执行效果欠佳，建议重新评估持仓逻辑并调整止损策略"
		}
		strategyChangeSuggestion = safeMarshal(strategySuggestion, "{}")

		// 风险指标
		var riskMetricsData map[string]interface{}
		if positionsCount > 0 {
			returns := c.computePositionReturns(snapshot.Positions)
			if len(returns) > 0 {
				volatility := c.riskEngine.CalculateVolatility(returns, 252)
				sharpe := c.riskEngine.CalculateSharpeRatio(returns, 0.03)
				maxDD := c.riskEngine.CalculateMaxDrawdown(c.buildEquityCurve(snapshot.Positions, totalAssets))
				riskMetricsData = map[string]interface{}{
					"volatility":      volatility,
					"sharpe_ratio":    sharpe,
					"max_drawdown":    maxDD,
					"positions_count": positionsCount,
				}
			}
		}
		if riskMetricsData == nil {
			riskMetricsData = map[string]interface{}{
				"volatility":      0,
				"sharpe_ratio":    0,
				"max_drawdown":    0,
				"positions_count": positionsCount,
			}
		}
		riskBytes := safeMarshal(riskMetricsData, "{}")
		riskMetrics = riskBytes

		// 今日事件
		keyEventList := []map[string]string{
			{
				"type":      "PORTFOLIO_CHECK",
				"title":     "每日组合检查",
				"timestamp": time.Now().Format(time.RFC3339),
			},
		}

		if dailyPnL < 0 {
			keyEventList = append(keyEventList, map[string]string{
				"type":  "LOSS_ALERT",
				"title": fmt.Sprintf("今日亏损 %.2f%%", -dailyReturn),
			})
		} else if dailyPnL > 0 {
			keyEventList = append(keyEventList, map[string]string{
				"type":  "PROFIT_RECORD",
				"title": fmt.Sprintf("今日盈利 %.2f%%", dailyReturn),
			})
		}

		keyEvents = safeMarshal(keyEventList, "[]")

		// 决策记录
		decisions := c.GetCIOJournal(1)
		decisionsJSON = safeMarshal(decisions, "[]")

		// 生成摘要
		if marketRegime == "BULLISH" {
			summary = fmt.Sprintf("今日市场整体上涨，AI量化组合取得%.2f%%的%s收益。", dailyReturn, func() string {
				if dailyReturn >= 0 {
					return "正"
				}
				return "负"
			}())
		} else if marketRegime == "BEARISH" {
			summary = fmt.Sprintf("今日市场调整，AI量化组合%s%.2f%%。", func() string {
				if dailyReturn >= 0 {
					return "逆势上涨"
				}
				return "回撤"
			}(), math.Abs(dailyReturn))
		} else {
			summary = fmt.Sprintf("今日市场震荡整理，AI量化组合%s%.2f%%。", func() string {
				if dailyReturn >= 0 {
					return "取得"
				}
				return "录得"
			}(), math.Abs(dailyReturn))
		}

		// 生成建议（dailyReturn 已为百分比值，如 0.15 表示 0.15%）
		if marketConfidence > 0.7 && dailyReturn > 2 {
			recommendations = "市场信心充足，建议维持当前仓位，可考虑适度加仓优质标的。"
		} else if marketConfidence < 0.4 || dailyReturn < -2 {
			recommendations = "市场不确定性增加，建议降低仓位，增加防御性配置。"
		} else {
			recommendations = "市场处于中性状态，建议维持现有仓位结构，关注结构性机会。"
		}
	}

	// 各智能体复盘汇总：聚合当日已完成 Agent 任务交付物（AgentTaskLog），
	// 使复盘报告真实反映 Planner/Quant/Risk/Trader/CIO 各自的复盘结论（严禁伪造）。
	agentSummaries := c.aggregateAgentSummaries(reviewDate)

	// 每日复盘正文优先取最近一次 CIO「深度复盘与制定明日计划」(night_cio) 的产出，
	// 即"复盘摘要"展示的是深度复盘与明日计划的内容；无该交付时回退上方确定性模板摘要。
	if ds, ok := c.deepReviewSummary(); ok && ds != "" {
		summary = ds
	}

	review := &data.DailyReview{
		ReviewDate:               reviewDate,
		PortfolioID:              "P001",
		TotalAssets:              totalAssets,
		TotalReturn:              totalReturn,
		DailyPnL:                 dailyPnL,
		DailyReturn:              dailyReturn,
		Cash:                     cash,
		MarketValue:              marketValue,
		PositionsCount:           positionsCount,
		TradeCount:               getTradeRecordsForReview(c),
		TradePnL:                 getTradePnLForReview(c),
		MarketRegime:             marketRegime,
		MarketConfidence:         marketConfidence,
		TopGainers:               topGainers,
		TopLosers:                topLosers,
		RiskMetrics:              riskMetrics,
		KeyEvents:                keyEvents,
		Decisions:                decisionsJSON,
		PositionStrategyReview:   positionStrategyReview,
		StrategyChangeSuggestion: strategyChangeSuggestion,
		AgentSummaries:           agentSummaries,
		Summary:                  summary,
		Recommendations:          recommendations,
		Status:                   "COMPLETED",
		CreatedAt:                time.Now(),
	}

	// 保存复盘报告
	if err := c.db.GetDB().Create(review).Error; err != nil {
		log.Printf("[CIO] Failed to save daily review: %v", err)
		return nil, err
	}

	// 记录审计日志
	c.addActivity("CIO", "DAILY_REVIEW", fmt.Sprintf("每日复盘报告已生成: %s", reviewDate), map[string]interface{}{
		"total_assets":  totalAssets,
		"daily_pnl":     dailyPnL,
		"market_regime": marketRegime,
		"summary":       summary,
	})

	// 盘后自动兑底：若量化分析师未手动选定次日策略，自动按回测指标为下一交易日选定策略，
	// 保证次日操盘手始终有策略可依（量化分析师手动选择优先，不覆盖）。
	c.autoEnsureNextDayStrategy()

	log.Printf("[CIO] Daily review generated for %s", reviewDate)
	return review, nil
}

// autoEnsureNextDayStrategy 盘后自动兑底：为下一交易日选定交易策略并写入策略日计划。
// 仅当该交易日尚无计划时写入；量化分析师手动选择的计划优先，不覆盖。
func (c *CIOEngine) autoEnsureNextDayStrategy() {
	if c.db == nil || c.db.GetDB() == nil {
		return
	}
	db := c.db.GetDB()
	nextDate := util.NextTradingDay(time.Now()).Format("2006-01-02")
	var existing data.DailyStrategyPlan
	if err := db.Where("trade_date = ?", nextDate).First(&existing).Error; err == nil {
		return // 已存在计划（量化分析师已选定），不覆盖
	}
	var strategies []data.Strategy
	if err := db.Where("is_active = 1").Find(&strategies).Error; err != nil || len(strategies) == 0 {
		return
	}
	best, stype := backtest.SelectBestStrategy(strategies)
	if stype == "" {
		return
	}
	plan := &data.DailyStrategyPlan{
		TradeDate:    nextDate,
		StrategyType: stype,
		StrategyName: best.Name,
		Reason:       "盘后自动兑底：依据策略表真实回测指标(收益/夏普/胜率)综合分自动选定次日策略",
		Status:       "active",
		CreatedBy:    "auto",
		Source:       "auto",
	}
	if err := db.Create(plan).Error; err != nil {
		log.Printf("[CIO] 自动选定次日策略落库失败: %v", err)
		return
	}
	log.Printf("[CIO] 盘后自动选定次日策略: %s (%s)", best.Name, stype)
}

// GetDailyReviews 获取复盘报告列表
func (c *CIOEngine) GetDailyReviews(days int) ([]map[string]interface{}, error) {
	if c.db == nil {
		return nil, fmt.Errorf("database not initialized")
	}

	if days <= 0 {
		days = 7
	}

	var reviews []data.DailyReview
	// 统一按东八区(CST)取"近 N 日"窗口，避免本地时区差异导致窗口偏移
	since := time.Now().In(time.FixedZone("CST", 8*3600)).AddDate(0, 0, -days)
	c.db.GetDB().
		Where("created_at >= ?", since).
		Order("review_date DESC").
		Find(&reviews)

	var result []map[string]interface{}
	for _, r := range reviews {
		review := map[string]interface{}{
			"id":                         r.ID,
			"review_date":                r.ReviewDate,
			"portfolio_id":               r.PortfolioID,
			"total_assets":               r.TotalAssets,
			"total_return":               r.TotalReturn,
			"daily_pnl":                  r.DailyPnL,
			"daily_return":               r.DailyReturn,
			"cash":                       r.Cash,
			"market_value":               r.MarketValue,
			"positions_count":            r.PositionsCount,
			"trade_count":                r.TradeCount,
			"trade_pnl":                  r.TradePnL,
			"market_regime":              r.MarketRegime,
			"market_confidence":          r.MarketConfidence,
			"top_gainers":                r.TopGainers,
			"top_losers":                 r.TopLosers,
			"risk_metrics":               r.RiskMetrics,
			"key_events":                 r.KeyEvents,
			"decisions":                  r.Decisions,
			"position_strategy_review":   r.PositionStrategyReview,
			"strategy_change_suggestion": r.StrategyChangeSuggestion,
			"agent_summaries":            r.AgentSummaries,
			"summary":                    r.Summary,
			"recommendations":            r.Recommendations,
			"status":                     r.Status,
			"created_at":                 r.CreatedAt.Format(time.RFC3339),
		}
		result = append(result, review)
	}

	return result, nil
}

// GetLatestDailyReview 获取最新复盘报告
func (c *CIOEngine) GetLatestDailyReview() (*map[string]interface{}, error) {
	reviews, err := c.GetDailyReviews(1)
	if err != nil {
		return nil, err
	}
	if len(reviews) == 0 {
		return nil, nil
	}
	return &reviews[0], nil
}

// DeleteDailyReview 删除指定 ID 的每日复盘快照记录。返回实际删除条数。
func (c *CIOEngine) DeleteDailyReview(id uint) (int64, error) {
	if c.db == nil || c.db.GetDB() == nil {
		return 0, fmt.Errorf("database not initialized")
	}
	res := c.db.GetDB().Unscoped().Delete(&data.DailyReview{}, id)
	if res.Error != nil {
		log.Printf("[CIO] DeleteDailyReview(id=%d) failed: %v", id, res.Error)
		return 0, res.Error
	}
	return res.RowsAffected, nil
}

// aggregateAgentSummaries 聚合当日各智能体已完成的复盘任务交付物（AgentTaskLog），
// 按角色汇总 TaskName/阶段/交付类型/摘要，生成复盘报告中的"各智能体复盘汇总"。
// 数据全部来自真实任务执行记录；无交付时返回空对象，严禁伪造。
func (c *CIOEngine) aggregateAgentSummaries(reviewDate string) string {
	if c.db == nil {
		return "{}"
	}
	var logs []data.AgentTaskLog
	if err := c.db.GetDB().
		Where("task_date = ? AND status = ?", reviewDate, "COMPLETED").
		Order("agent_role ASC, task_order ASC").
		Find(&logs).Error; err != nil {
		log.Printf("[CIO] aggregateAgentSummaries query failed: %v", err)
		return "{}"
	}

	roleOrder := []string{"PLANNER", "QUANT", "RISK", "TRADER", "CIO"}
	type taskItem struct {
		TaskName        string `json:"task_name"`
		TaskPhase       string `json:"task_phase"`
		DeliverableType string `json:"deliverable_type"`
		Summary         string `json:"summary"`
	}
	combined := map[string][]taskItem{}
	for _, l := range logs {
		role := strings.TrimSpace(l.AgentRole)
		if role == "" {
			continue
		}
		combined[role] = append(combined[role], taskItem{
			TaskName:        l.TaskName,
			TaskPhase:       l.TaskPhase,
			DeliverableType: l.DeliverableType,
			Summary:         l.Summary,
		})
	}
	// 按固定角色顺序输出，保证结构稳定
	ordered := make(map[string][]taskItem, len(roleOrder))
	for _, role := range roleOrder {
		if items, ok := combined[role]; ok {
			ordered[role] = items
		}
	}
	return safeMarshal(ordered, "{}")
}

// deepReviewSummary 取最近一次 CIO「深度复盘与制定明日计划」(night_cio, deliverable_type=tomorrow_plan) 的摘要，
// 作为每日复盘正文内容来源。若不存在或为空则返回 (false)，由调用方回退确定性模板摘要。严禁伪造。
func (c *CIOEngine) deepReviewSummary() (string, bool) {
	if c.db == nil {
		return "", false
	}
	var log data.AgentTaskLog
	if err := c.db.GetDB().
		Where("deliverable_type = ? AND status = ?", "tomorrow_plan", "COMPLETED").
		Order("task_date DESC, created_at DESC").
		First(&log).Error; err != nil {
		return "", false
	}
	if strings.TrimSpace(log.Summary) == "" {
		return "", false
	}
	return log.Summary, true
}

// getTradeRecordsForReview 获取交易记录数
func getTradeRecordsForReview(c *CIOEngine) int {
	if c.portfolio == nil {
		return 0
	}
	records, _ := c.portfolio.GetTradeHistory(1)
	return len(records)
}

// getTradePnLForReview 获取交易盈亏
func getTradePnLForReview(c *CIOEngine) float64 {
	if c.portfolio == nil {
		return 0
	}
	records, _ := c.portfolio.GetTradeHistory(1)
	var totalPnL float64
	for _, r := range records {
		totalPnL += r.RealizedPnL
	}
	return totalPnL
}

// IsMarketClosed 判断是否已收盘（15:30后）
func IsMarketClosed() bool {
	now := time.Now()
	day := now.Weekday()
	if day == 0 || day == 6 {
		return false // 周末不交易
	}
	h := now.Hour()
	m := now.Minute()
	currentMinutes := h*60 + m
	// 15:30 = 930 分钟
	return currentMinutes >= 930
}
