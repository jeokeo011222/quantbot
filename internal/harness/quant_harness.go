package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/quantpilot/quantpilot/internal/brain/agents"
	"github.com/quantpilot/quantpilot/internal/brain/llm"
	"github.com/quantpilot/quantpilot/internal/brain/port"
	brainhost "github.com/quantpilot/quantpilot/internal/brainhost"
	"github.com/quantpilot/quantpilot/internal/config"
	"github.com/quantpilot/quantpilot/internal/data"
	"github.com/quantpilot/quantpilot/internal/tools"
	"github.com/quantpilot/quantpilot/internal/transparency"
)

// buildAgentContext 构建提交给大模型分析 Agent 的真实系统上下文（大盘行情摘要 + 组合快照 + 持仓明细）。
// 数据全部来自真实数据库（DuckDB 指数K线 / SQLite 日结算与持仓表），严禁伪造或回退。
// 任何部分读取失败仅记录并跳过该段，绝不让上下文构建阻塞分析流程。
func buildAgentContext(ctx context.Context, sqliteManager *data.SQLiteManager, duckdbManager *data.DuckDBManager, date string) string {
	var parts []string

	// 1) 大盘指数行情摘要（DuckDB 真实日K线，最近两个交易日涨跌幅）
	if duckdbManager != nil && duckdbManager.HasStockDB() {
		indices := []struct{ code, name string }{
			{"sh000001", "上证指数"},
			{"sz399001", "深证成指"},
			{"sh000300", "沪深300"},
		}
		var idxLines []string
		for _, idx := range indices {
			bars, err := duckdbManager.GetIndexKlineFromStock(ctx, idx.code, 2)
			if err != nil || len(bars) < 2 {
				continue
			}
			prev, last := bars[1], bars[0]
			pct := 0.0
			if prev.Close > 0 {
				pct = (last.Close - prev.Close) / prev.Close * 100
			}
			idxLines = append(idxLines, fmt.Sprintf("%s(%s): 收盘 %.2f，涨跌 %+.2f%%（日期 %s）",
				idx.name, idx.code, last.Close, pct, last.Date.Format("2006-01-02")))
		}
		if len(idxLines) > 0 {
			parts = append(parts, "【大盘行情摘要】\n"+strings.Join(idxLines, "\n"))
		}
	}

	// 2) 组合资产快照 + 持仓明细（SQLite 最近一条日结算 + 当前 open 持仓）
	if sqliteManager != nil && sqliteManager.GetDB() != nil {
		var stat data.PortfolioDailyStat
		if err := sqliteManager.GetDB().Order("stat_date DESC").First(&stat).Error; err == nil {
			parts = append(parts, fmt.Sprintf("【组合快照】日期=%s 总资产=%.2f 现金=%.2f 持仓市值=%.2f 持仓数=%d 累计收益=%+.2f%% 当日收益=%+.2f%%",
				stat.StatDate, stat.TotalAssets, stat.Cash, stat.MarketValue, stat.PositionsCount,
				stat.TotalReturn*100, stat.DailyReturn*100))
		}

		var positions []data.Position
		if err := sqliteManager.GetDB().
			Where("position_status = ?", "open").
			Order("market_value DESC").
			Limit(10).
			Find(&positions).Error; err == nil && len(positions) > 0 {
			var posLines []string
			for _, p := range positions {
				posLines = append(posLines, fmt.Sprintf("%s 股数=%d 市值=%.0f 权重=%.1f%% 浮盈=%+.1f%%",
					p.InstrumentID, p.Quantity, p.MarketValue, p.Weight*100, p.UnrealizedReturn*100))
			}
			parts = append(parts, "【当前持仓(按市值前10)】\n"+strings.Join(posLines, "\n"))
		}
	}

	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "\n\n")
}

// QuantHarness 量化交易系统核心
type QuantHarness struct {
	configManager *config.ConfigManager
	sqliteManager *data.SQLiteManager
	duckdbManager *data.DuckDBManager
	llmClient     llm.Client
	tracker       *transparency.Tracker

	// TDX 数据服务管理器（支持多数据源）
	tdxManager *tools.TDXServiceManager

	// A股专用 TDX 工具（V2版本，支持多数据源）
	tdxDataTool   *tools.TDXDataToolV2
	tdxSearchTool *tools.TDXSearchToolV2
	tdxMarketTool *tools.TDXMarketStatsToolV2
	tdxCommonTool *tools.TDXCommonStocksToolV2

	// 巨潮资讯网信息披露工具（中国证监会指定上市公司信息披露平台）
	cninfoTool *tools.CninfoInfoTool

	// 新架构：编排器
	orchestrator *agents.Orchestrator

	// 状态
	mu               sync.RWMutex
	running          bool
	currentCycle     *DailyCycleResult
	currentOrchCycle *agents.OrchestratorResult
}

// DailyCycleResult 每日运行结果
type DailyCycleResult struct {
	Date            string                          `json:"date"`
	StartTime       time.Time                       `json:"start_time"`
	EndTime         time.Time                       `json:"end_time"`
	MarketView      interface{}                     `json:"market_view"`
	AlphaSignals    interface{}                     `json:"alpha_signals"`
	RiskAssessment  interface{}                     `json:"risk_assessment"`
	PortfolioPlan   interface{}                     `json:"portfolio_plan"`
	Decision        interface{}                     `json:"decision"`
	RiskChecks      interface{}                     `json:"risk_checks"`
	Executed        bool                            `json:"executed"`
	Errors          []string                        `json:"errors"`
	AgentSessions   map[string]*agents.SessionState `json:"agent_sessions"`
	Trajectories    map[string]*agents.Trajectory   `json:"trajectories,omitempty"`
	TokenUsage      agents.TokenUsage               `json:"token_usage,omitempty"`
	UseOrchestrator bool                            `json:"use_orchestrator"`
}

// ProgressCallback 进度回调函数类型
type ProgressCallback func(phase string, progress float64, message string)

// QuantHarnessOptions 初始化选项
type QuantHarnessOptions struct {
	ConfigManager *config.ConfigManager
	SQLiteManager *data.SQLiteManager
	DuckDBManager *data.DuckDBManager
	LLMClient     llm.Client
}

// NewQuantHarness 创建量化交易系统实例
// 如果传入 opts 中的已有管理器，则复用，避免重复连接
func NewQuantHarness(opts ...QuantHarnessOptions) (*QuantHarness, error) {
	var opt QuantHarnessOptions
	if len(opts) > 0 {
		opt = opts[0]
	}

	// 初始化配置管理器（优先使用传入的）
	var configManager *config.ConfigManager
	if opt.ConfigManager != nil {
		configManager = opt.ConfigManager
	} else {
		cm, err := config.NewConfigManager()
		if err != nil {
			return nil, fmt.Errorf("failed to init config: %w", err)
		}
		configManager = cm
	}

	cfg := configManager.GetConfig()

	// 初始化 LLM 客户端（优先使用传入的）
	var llmClient llm.Client
	if opt.LLMClient != nil {
		llmClient = opt.LLMClient
	} else {
		llmClient = llm.NewDeepSeekClient(cfg.AIAPIKey, cfg.AIBaseURL, cfg.AIModel)
	}

	// 初始化透明度追踪器
	tracker := transparency.NewTracker()

	// 初始化数据库（优先使用传入的）
	var sqliteManager *data.SQLiteManager
	if opt.SQLiteManager != nil {
		sqliteManager = opt.SQLiteManager
	} else {
		sm, err := data.NewSQLiteManager()
		if err != nil {
			return nil, fmt.Errorf("failed to init SQLite: %w", err)
		}
		sqliteManager = sm
	}

	var duckdbManager *data.DuckDBManager
	if opt.DuckDBManager != nil {
		duckdbManager = opt.DuckDBManager
	} else {
		dm, err := data.NewDuckDBManager()
		if err != nil {
			return nil, fmt.Errorf("failed to init DuckDB: %w", err)
		}
		duckdbManager = dm
	}

	tdxPath := cfg.TDXPath
	if tdxPath == "" {
		tdxPath = "D:\\tdx"
	}

	tdxManager := tools.NewTDXServiceManager(
		cfg.DataProvider, cfg.MCPURL, cfg.MCPAPIKey, tdxPath,
	)

	// 设置DuckDB管理器到TDXManager，用于板块/广度等需要历史数据的功能
	tdxManager.SetDuckDBManager(duckdbManager)

	log.Printf("[QuantBot] Data provider: %s, TDX path: %s", cfg.DataProvider, tdxPath)

	// 创建并统一注册实时行情数据源（统一调用规范：所有读实时数据一律走 data.GetDataSource()）。
	// 若配置数据源为腾讯财经，直接以腾讯财经为实时行情主源，不依赖 TDX。
	if cfg.DataProvider == data.DataProviderTencentName {
		data.SetDataSource(tools.NewTencentMarketDataProvider())
		log.Printf("[QuantBot] Global real-time market data provider set to: tencent")
	} else {
		tdxProvider := tools.NewTDXMarketDataProvider(tdxManager)
		data.SetDataSource(tdxProvider)
		log.Printf("[QuantBot] Global TDX market data provider set")
	}

	// 创建 A股专用 TDX 工具（使用 TDXServiceManager 支持多数据源）
	tdxDataTool := tools.NewTDXDataToolV2(tdxManager)
	tdxSearchTool := tools.NewTDXSearchToolV2(tdxManager)
	tdxMarketTool := tools.NewTDXMarketStatsToolV2(tdxManager)
	tdxCommonTool := tools.NewTDXCommonStocksToolV2(tdxManager)

	// 创建巨潮资讯网信息披露工具（中国证监会指定上市公司信息披露平台）
	cninfoTool := tools.NewCninfoInfoTool()

	// 创建通用工具（回测、组合优化、通用市场数据）
	backtestTool := tools.NewBacktestTool(sqliteManager, duckdbManager)
	portfolioTool := tools.NewPortfolioOptimizerTool(duckdbManager)
	marketDataTool := tools.NewMarketDataTool(duckdbManager)
	searchMarketTool := tools.NewSearchMarketTool(duckdbManager)

	// ====== 创建编排器 ======
	orchestrator := agents.NewOrchestrator(brainhost.NewPersistenceAdapter(sqliteManager), llmClient)

	// 注入真实系统上下文构建器：为盘前分析 Agent 提供大盘行情摘要、组合快照、持仓明细等
	// 真实数据（来自 DuckDB/SQLite），避免 LLM 反复调用工具取数，提升分析质量与响应速度。
	orchestrator.ContextProvider = func(ctx context.Context, role string, date string) string {
		return buildAgentContext(ctx, sqliteManager, duckdbManager, date)
	}

	// 注册A股工具到工具注册表
	agents.RegisterAShareTools(orchestrator.ToolReg,
		brainhost.AdaptTool(tdxDataTool), brainhost.AdaptTool(tdxSearchTool), brainhost.AdaptTool(tdxMarketTool), brainhost.AdaptTool(tdxCommonTool),
		brainhost.AdaptTool(backtestTool), brainhost.AdaptTool(portfolioTool), brainhost.AdaptTool(marketDataTool), brainhost.AdaptTool(searchMarketTool), brainhost.AdaptTool(cninfoTool))

	// ====== 基于Skill创建各角色Agent ======

	// PLANNER - 投资规划师
	plannerTools := []port.ToolExecutor{brainhost.AdaptTool(tdxMarketTool), brainhost.AdaptTool(cninfoTool)}
	plannerSkill := agents.NewAMarketSkill_MarketAnalysis(plannerTools)
	plannerAgent := agents.NewBaseAgentWithRole("planner", agents.RolePlanner, "")
	plannerAgent.SetLLMClient(llmClient)
	plannerAgent.SetToolRegistry(orchestrator.ToolReg)
	plannerAgent.LoadSkill(plannerSkill)
	plannerAgent.SetMode(agents.ModePTC)
	orchestrator.RegisterBaseAgent(plannerAgent)
	orchestrator.RegisterAgent(agents.NewAgent("planner", agents.RolePlanner, brainhost.NewPersistenceAdapter(sqliteManager), llmClient, tracker))
	if err := orchestrator.RegisterSkill(plannerSkill); err != nil {
		log.Printf("[QuantBot] Skill register warning: %v", err)
	}

	// QUANT - 量化分析师
	quantTools := []port.ToolExecutor{brainhost.AdaptTool(tdxDataTool), brainhost.AdaptTool(tdxSearchTool), brainhost.AdaptTool(backtestTool), brainhost.AdaptTool(cninfoTool)}
	quantSkill := agents.NewAMarketSkill_Picker(quantTools)
	quantAgent := agents.NewBaseAgentWithRole("quant", agents.RoleQuant, "")
	quantAgent.SetLLMClient(llmClient)
	quantAgent.SetToolRegistry(orchestrator.ToolReg)
	quantAgent.LoadSkill(quantSkill)
	quantAgent.SetMode(agents.ModePTC)
	orchestrator.RegisterBaseAgent(quantAgent)
	orchestrator.RegisterAgent(agents.NewAgent("quant", agents.RoleQuant, brainhost.NewPersistenceAdapter(sqliteManager), llmClient, tracker))
	if err := orchestrator.RegisterSkill(quantSkill); err != nil {
		log.Printf("[QuantBot] Skill register warning: %v", err)
	}

	// RISK - 风控师
	riskTools := []port.ToolExecutor{brainhost.AdaptTool(tdxDataTool), brainhost.AdaptTool(tdxMarketTool)}
	riskSkill := agents.NewAMarketSkill_Risk(riskTools)
	riskAgent := agents.NewBaseAgentWithRole("risk", agents.RoleRisk, "")
	riskAgent.SetLLMClient(llmClient)
	riskAgent.SetToolRegistry(orchestrator.ToolReg)
	riskAgent.LoadSkill(riskSkill)
	riskAgent.SetMode(agents.ModePTC)
	orchestrator.RegisterBaseAgent(riskAgent)
	orchestrator.RegisterAgent(agents.NewAgent("risk", agents.RoleRisk, brainhost.NewPersistenceAdapter(sqliteManager), llmClient, tracker))
	if err := orchestrator.RegisterSkill(riskSkill); err != nil {
		log.Printf("[QuantBot] Skill register warning: %v", err)
	}

	// CIO - 首席投资官
	cioTools := []port.ToolExecutor{brainhost.AdaptTool(tdxMarketTool), brainhost.AdaptTool(tdxCommonTool), brainhost.AdaptTool(portfolioTool), brainhost.AdaptTool(cninfoTool)}
	cioSkill := agents.NewCIOSkill(cioTools)
	cioAgent := agents.NewBaseAgentWithRole("cio", agents.RoleCIO, "")
	cioAgent.SetLLMClient(llmClient)
	cioAgent.SetToolRegistry(orchestrator.ToolReg)
	cioAgent.LoadSkill(cioSkill)
	cioAgent.SetMode(agents.ModePTC)
	orchestrator.RegisterBaseAgent(cioAgent)
	orchestrator.RegisterAgent(agents.NewAgent("cio", agents.RoleCIO, brainhost.NewPersistenceAdapter(sqliteManager), llmClient, tracker))
	if err := orchestrator.RegisterSkill(cioSkill); err != nil {
		log.Printf("[QuantBot] Skill register warning: %v", err)
	}

	// TRADER - 操盘手（仅使用市场统计工具，交易执行通过 place_trade 且需合法 DecisionObject）
	traderTools := []port.ToolExecutor{brainhost.AdaptTool(tdxMarketTool)}
	traderSkill := agents.NewAMarketSkill_Execution(traderTools)
	traderAgent := agents.NewBaseAgentWithRole("trader", agents.RoleTrader, "")
	traderAgent.SetLLMClient(llmClient)
	traderAgent.SetToolRegistry(orchestrator.ToolReg)
	traderAgent.LoadSkill(traderSkill)
	traderAgent.SetMode(agents.ModeStandard)
	orchestrator.RegisterBaseAgent(traderAgent)
	orchestrator.RegisterAgent(agents.NewAgent("trader", agents.RoleTrader, brainhost.NewPersistenceAdapter(sqliteManager), llmClient, tracker))
	if err := orchestrator.RegisterSkill(traderSkill); err != nil {
		log.Printf("[QuantBot] Skill register warning: %v", err)
	}

	// 初始化工作流引擎
	orchestrator.SetWorkflowEngine(nil)

	return &QuantHarness{
		configManager: configManager,
		sqliteManager: sqliteManager,
		duckdbManager: duckdbManager,
		llmClient:     llmClient,
		tracker:       tracker,
		tdxManager:    tdxManager,
		tdxDataTool:   tdxDataTool,
		tdxSearchTool: tdxSearchTool,
		tdxMarketTool: tdxMarketTool,
		tdxCommonTool: tdxCommonTool,
		orchestrator:  orchestrator,
	}, nil
}

// RunDailyCycle 运行每日分析周期
func (h *QuantHarness) RunDailyCycle(ctx context.Context) (*DailyCycleResult, error) {
	return h.RunDailyCycleWithMode(ctx, nil)
}

// RunDailyCycleWithMode 运行每日分析周期（使用编排器）
func (h *QuantHarness) RunDailyCycleWithMode(ctx context.Context, progressCB ProgressCallback) (*DailyCycleResult, error) {
	return h.runWithOrchestrator(ctx, progressCB)
}

// ProgressCallback wrapper
type progressAdapter struct {
	cb ProgressCallback
}

func (p *progressAdapter) send(phase string, progress float64, message string) {
	if p.cb != nil {
		p.cb(phase, progress, message)
	}
}

// runWithOrchestrator 使用编排器运行每日分析
func (h *QuantHarness) runWithOrchestrator(ctx context.Context, progressCB ProgressCallback) (*DailyCycleResult, error) {
	h.mu.Lock()
	if h.running {
		h.mu.Unlock()
		return nil, fmt.Errorf("分析周期正在运行中")
	}
	h.running = true
	h.mu.Unlock()

	defer func() {
		h.mu.Lock()
		h.running = false
		h.mu.Unlock()
	}()

	adapter := &progressAdapter{cb: progressCB}
	adapter.send("init", 0.05, "初始化编排器...")

	orchResult, err := h.orchestrator.RunDailyCycle(ctx)
	if err != nil {
		return nil, err
	}

	result := &DailyCycleResult{
		Date:            orchResult.Date,
		StartTime:       orchResult.StartTime,
		EndTime:         orchResult.EndTime,
		Errors:          orchResult.Errors,
		AgentSessions:   orchResult.Sessions,
		Trajectories:    orchResult.Trajectories,
		TokenUsage:      agents.TokenUsage{TotalTokens: orchResult.TotalTokens},
		UseOrchestrator: true,
	}

	// 映射编排器结果到标准字段
	if s, ok := orchResult.Sessions["PLANNER"]; ok {
		result.MarketView = s.Decision
	}
	if s, ok := orchResult.Sessions["QUANT"]; ok {
		result.AlphaSignals = s.Decision
	}
	if s, ok := orchResult.Sessions["RISK"]; ok {
		result.RiskAssessment = s.Decision
	}
	if s, ok := orchResult.Sessions["CIO"]; ok {
		result.Decision = s.Decision
	}
	result.PortfolioPlan = orchResult.Decision

	h.currentCycle = result
	h.currentOrchCycle = orchResult

	adapter.send("complete", 1.0, fmt.Sprintf("分析完成！耗时 %v", result.EndTime.Sub(result.StartTime)))
	return result, nil
}

// GetCurrentCycle 获取当前周期结果
func (h *QuantHarness) GetCurrentCycle() *DailyCycleResult {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.currentCycle
}

// GetOrchestrator 获取编排器
func (h *QuantHarness) GetOrchestrator() *agents.Orchestrator {
	return h.orchestrator
}

// GetTracker 获取透明度追踪器
func (h *QuantHarness) GetTracker() *transparency.Tracker {
	return h.tracker
}

// GetConfig 获取当前配置
func (h *QuantHarness) GetConfig() config.AppConfig {
	return h.configManager.GetConfig()
}

// GetSQLiteManager 获取 SQLite 管理器（供调度器等组件查询任务记录）
func (h *QuantHarness) GetSQLiteManager() *data.SQLiteManager {
	return h.sqliteManager
}

// UpdateConfig 更新配置
func (h *QuantHarness) UpdateConfig(updateFn func(cfg *config.AppConfig)) error {
	return h.configManager.UpdateConfig(updateFn)
}

// saveDailyResult 保存每日结果到数据库
func (h *QuantHarness) saveDailyResult(result *DailyCycleResult) {
	resultJSON, _ := json.Marshal(result)
	log.Printf("[QuantBot] 保存每日分析结果 (%d bytes)", len(resultJSON))
}

// Close 关闭系统
func (h *QuantHarness) Close() {
	h.sqliteManager.Close()
	h.duckdbManager.Close()
	log.Printf("[QuantBot] 系统已关闭")
}

// UpdateDataProvider 更新数据源
func (h *QuantHarness) UpdateDataProvider(provider string, cfg config.AppConfig) {
	// 腾讯财经直接作为实时行情主源：不依赖 TDX，统一注册腾讯实时行情数据源
	if provider == data.DataProviderTencentName {
		tencent := tools.NewTencentMarketDataProvider()
		data.SetDataSource(tencent)
		log.Printf("[QuantBot] Real-time market data provider set to: tencent")
		return
	}

	if h.tdxManager != nil {
		h.tdxManager.SetProvider(provider)
		h.tdxManager.SetMCPConfig(cfg.MCPURL, cfg.MCPAPIKey)
		log.Printf("[QuantBot] Data provider updated: %s", provider)

		// 统一注册实时行情数据源（TDX），确保 data.GetDataSource() 返回当前选中源
		tdxProvider := tools.NewTDXMarketDataProvider(h.tdxManager)
		data.SetDataSource(tdxProvider)
		log.Printf("[QuantBot] Global market data provider updated to: %s", provider)
	}
}

// TestDataProvider 测试数据源连接
func (h *QuantHarness) TestDataProvider() (map[string]interface{}, error) {
	if h.tdxManager == nil {
		return nil, fmt.Errorf("TDX manager not initialized")
	}

	status := h.tdxManager.Health()
	testResult := map[string]interface{}{
		"status":   "ok",
		"provider": h.tdxManager.GetActiveProvider(),
		"health":   status,
	}

	data, err := h.tdxManager.GetStockData("sh", "600519", 5, false)
	if err != nil {
		testResult["test_data"] = map[string]interface{}{
			"success": false, "error": err.Error(),
		}
	} else {
		testResult["test_data"] = map[string]interface{}{
			"success": true, "exchange": data["exchange"], "code": data["code"], "days": data["days"],
		}
	}

	return testResult, nil
}

// GetSkills 获取所有已注册技能
func (h *QuantHarness) GetSkills() []*agents.Skill {
	return h.orchestrator.GetSkills()
}

// GetTrajectories 获取轨迹列表
func (h *QuantHarness) GetTrajectories(limit int) []*agents.Trajectory {
	return h.orchestrator.GetTrajectories(limit)
}

// GetTrajectoryStats 获取轨迹统计
func (h *QuantHarness) GetTrajectoryStats() map[string]interface{} {
	return h.orchestrator.GetTrajectoryStats()
}

// ConnectNativeTDX 连接 Go 原生 TDX 服务
func (h *QuantHarness) ConnectNativeTDX() error {
	if h.tdxManager == nil {
		return fmt.Errorf("TDX manager not initialized")
	}
	return h.tdxManager.ConnectNativeTDX()
}

// TestNativeTDX 测试 Go 原生 TDX 连接
func (h *QuantHarness) TestNativeTDX() (bool, string) {
	if h.tdxManager == nil {
		return false, "TDX manager not initialized"
	}
	return h.tdxManager.TestNativeTDXConnection()
}

// SetDataProvider 切换数据源
func (h *QuantHarness) SetDataProvider(provider string) {
	if h.tdxManager != nil {
		h.tdxManager.SetProvider(provider)
	}
}

// GetBoardList 获取板块列表
func (h *QuantHarness) GetBoardList(boardType string) (map[string]interface{}, error) {
	if h.tdxManager == nil {
		return nil, fmt.Errorf("TDX manager not initialized")
	}

	boards, err := h.tdxManager.GetBoardList(boardType)
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"board_type": boardType,
		"count":      len(boards),
		"boards":     boards,
	}, nil
}

// GetBoardStocks 获取板块成分股
func (h *QuantHarness) GetBoardStocks(boardCode string) (map[string]interface{}, error) {
	if h.tdxManager == nil {
		return nil, fmt.Errorf("TDX manager not initialized")
	}

	return h.tdxManager.GetBoardSummary(boardCode, true)
}

// GetFundFlow 获取资金流向
func (h *QuantHarness) GetFundFlow(exchange, code string) (map[string]interface{}, error) {
	if h.tdxManager == nil {
		return nil, fmt.Errorf("TDX manager not initialized")
	}

	flows, err := h.tdxManager.GetFundFlow(exchange, code)
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"exchange": exchange,
		"code":     code,
		"count":    len(flows),
		"flows":    flows,
	}, nil
}

// GetAnnouncements 获取公告数据
func (h *QuantHarness) GetAnnouncements(exchange, code string, limit int) (map[string]interface{}, error) {
	if h.tdxManager == nil {
		return nil, fmt.Errorf("TDX manager not initialized")
	}

	if limit <= 0 {
		limit = 10
	}

	announcements, err := h.tdxManager.GetAnnouncement(code, limit)
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"exchange":      exchange,
		"code":          code,
		"count":         len(announcements),
		"announcements": announcements,
	}, nil
}
