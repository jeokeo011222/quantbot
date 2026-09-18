package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/quantpilot/quantpilot/internal/brainhost"
	"github.com/quantpilot/quantpilot/internal/config"
	"github.com/quantpilot/quantpilot/internal/data"
	"github.com/quantpilot/quantpilot/internal/port"
	"github.com/quantpilot/quantpilot/internal/tools"
	"github.com/quantpilot/quantpilot/internal/toolworker"
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

// OrchestratorBridge 编排桥：宿主侧仅暴露给上层（App）读取上下文构建器等公开能力。
// DLL 隔离后进程内 agents.Orchestrator 已迁入 agent.dll，宿主仅保留此数据侧桥。
type OrchestratorBridge struct {
	ContextProvider port.ContextProvider
}

// QuantHarness 量化交易系统核心
type QuantHarness struct {
	configManager *config.ConfigManager
	sqliteManager *data.SQLiteManager
	duckdbManager *data.DuckDBManager
	llmClient     port.LLMClient
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

	// 新架构：编排桥（仅数据/上下文能力；进程内决策已迁入 agent.dll）
	orch *OrchestratorBridge

	// 真实工具集合（按角色分组，供 BindReal / BuildCatalog 供给 agent.dll）
	toolsByRole map[string][]port.ToolExecutor
	store       port.Persistence

	// 状态
	mu               sync.RWMutex
	running          bool
	currentCycle     *DailyCycleResult
	currentOrchCycle *port.OrchestratorResult
}

// DailyCycleResult 每日运行结果
type DailyCycleResult struct {
	Date            string                        `json:"date"`
	StartTime       time.Time                     `json:"start_time"`
	EndTime         time.Time                     `json:"end_time"`
	MarketView      interface{}                   `json:"market_view"`
	AlphaSignals    interface{}                   `json:"alpha_signals"`
	RiskAssessment  interface{}                   `json:"risk_assessment"`
	PortfolioPlan   interface{}                   `json:"portfolio_plan"`
	Decision        interface{}                   `json:"decision"`
	RiskChecks      interface{}                   `json:"risk_checks"`
	Executed        bool                          `json:"executed"`
	Errors          []string                      `json:"errors"`
	AgentSessions   map[string]*port.SessionState `json:"agent_sessions"`
	Trajectories    map[string]*port.Trajectory   `json:"trajectories,omitempty"`
	TokenUsage      port.TokenUsage               `json:"token_usage,omitempty"`
	UseOrchestrator bool                          `json:"use_orchestrator"`
}

// ProgressCallback 进度回调函数类型
type ProgressCallback func(phase string, progress float64, message string)

// QuantHarnessOptions 初始化选项
type QuantHarnessOptions struct {
	ConfigManager *config.ConfigManager
	SQLiteManager *data.SQLiteManager
	DuckDBManager *data.DuckDBManager
	LLMClient     port.LLMClient
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

	// ====== 装配真实工具（按角色分组，供给 agent.dll 决策脑） ======
	adapt := func(t tools.Tool) port.ToolExecutor { return brainhost.AdaptTool(t) }

	toolsByRole := map[string][]port.ToolExecutor{
		"planner": {adapt(tdxMarketTool), adapt(cninfoTool)},
		"quant":   {adapt(tdxDataTool), adapt(tdxSearchTool), adapt(backtestTool), adapt(cninfoTool)},
		"risk":    {adapt(tdxDataTool), adapt(tdxMarketTool)},
		"cio":     {adapt(tdxMarketTool), adapt(tdxCommonTool), adapt(portfolioTool), adapt(cninfoTool)},
		"trader":  {adapt(tdxMarketTool)},
	}

	// 注入真实系统上下文构建器：为盘前分析 Agent 提供大盘行情摘要、组合快照、持仓明细等
	// 真实数据（来自 DuckDB/SQLite），避免 LLM 反复调用工具取数，提升分析质量与响应速度。
	orch := &OrchestratorBridge{
		ContextProvider: func(ctx context.Context, role string, date string) string {
			return buildAgentContext(ctx, sqliteManager, duckdbManager, date)
		},
	}

	return &QuantHarness{
		configManager: configManager,
		sqliteManager: sqliteManager,
		duckdbManager: duckdbManager,
		tracker:       tracker,
		tdxManager:    tdxManager,
		tdxDataTool:   tdxDataTool,
		tdxSearchTool: tdxSearchTool,
		tdxMarketTool: tdxMarketTool,
		tdxCommonTool: tdxCommonTool,
		orch:          orch,
		toolsByRole:   toolsByRole,
		store:         brainhost.NewPersistenceAdapter(sqliteManager),
	}, nil
}

// RunDailyCycle 运行每日分析周期
func (h *QuantHarness) RunDailyCycle(ctx context.Context) (*DailyCycleResult, error) {
	return h.RunDailyCycleWithMode(ctx, nil)
}

// RunDailyCycleWithMode 运行每日分析周期（经 agent.dll 决策脑；无 DLL 时退化为降级结果）
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

// dllPath 返回 agent.dll 路径（与 App 同规则：exe 同目录优先，回退 bin/agent.dll）。
func (h *QuantHarness) dllPath() string {
	if p := os.Getenv("QUANTBOT_DLL_PATH"); p != "" {
		return p
	}
	if exe, err := os.Executable(); err == nil {
		cand := filepath.Join(filepath.Dir(exe), "agent.dll")
		if _, statErr := os.Stat(cand); statErr == nil {
			return cand
		}
	}
	return filepath.Join("bin", "agent.dll")
}

// runWithOrchestrator 经 agent.dll 决策脑运行每日分析周期。
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
	adapter.send("init", 0.05, "初始化 agent.dll 决策脑...")

	date := time.Now().Format("2006-01-02")

	agent, err := toolworker.Load(h.dllPath())
	if err != nil {
		log.Printf("[QuantBot] agent.dll 加载失败（harness 降级）: %v", err)
		return h.degradedResult(date, fmt.Sprintf("agent.dll 加载失败: %v", err)), nil
	}
	defer agent.Close()

	host := &toolworker.Host{}
	host.BindReal(h.flattenRealTools(), h.store, h.orch.ContextProvider)
	catalog := toolworker.BuildCatalog(h.toolsByRole)

	adapter.send("dll_init", 0.15, fmt.Sprintf("经 agent.dll 决策脑启动（%d 角色目录）", len(catalog)))

	// LLM：配置经 AgentInit 传入 DLL，由决策脑自建 llm.Client（不再经总线代理）。
	cfg := h.configManager.GetConfig()
	llmCfg := port.LLMConfig{
		Provider: cfg.AIProvider,
		APIKey:   cfg.AIAPIKey,
		BaseURL:  cfg.AIBaseURL,
		Model:    cfg.AIModel,
	}
	orchResult, counters, err := toolworker.RunAgentCycle(agent, host, catalog, date, llmCfg, 4*time.Minute)
	if err != nil {
		log.Printf("[QuantBot] agent.dll 决策路径失败（harness 降级）: %v", err)
		return h.degradedResult(date, fmt.Sprintf("agent.dll 决策失败: %v", err)), nil
	}
	log.Printf("[QuantBot] agent.dll 决策完成，宿主兑现统计: %v", counters)

	result := h.mapOrchestratorResult(orchResult)

	h.currentCycle = result
	h.currentOrchCycle = orchResult

	adapter.send("complete", 1.0, fmt.Sprintf("分析完成！耗时 %v", result.EndTime.Sub(result.StartTime)))
	return result, nil
}

// flattenRealTools 收集全部真实工具为扁平列表供 Host.BindReal 建立 name→工具映射。
func (h *QuantHarness) flattenRealTools() []port.ToolExecutor {
	var out []port.ToolExecutor
	seen := map[string]bool{}
	for _, roleTools := range h.toolsByRole {
		for _, t := range roleTools {
			if t == nil {
				continue
			}
			if seen[t.Name()] {
				continue
			}
			seen[t.Name()] = true
			out = append(out, t)
		}
	}
	return out
}

// degradedResult 构造降级结果（决策脑不可用时保持 DailyCycleResult 形状）。
func (h *QuantHarness) degradedResult(date, errMsg string) *DailyCycleResult {
	result := &DailyCycleResult{
		Date:            date,
		StartTime:       time.Now(),
		EndTime:         time.Now(),
		Errors:          []string{errMsg},
		AgentSessions:   map[string]*port.SessionState{},
		Trajectories:    map[string]*port.Trajectory{},
		UseOrchestrator: false,
	}
	h.currentCycle = result
	return result
}

// mapOrchestratorResult 把 agent.dll 结果的 orchestrator 结果映射为与历史
// runWithOrchestrator 同构的 DailyCycleResult，保持 UI 事件流与回退路径一致。
func (h *QuantHarness) mapOrchestratorResult(r *port.OrchestratorResult) *DailyCycleResult {
	result := &DailyCycleResult{
		Date:            r.Date,
		StartTime:       r.StartTime,
		EndTime:         r.EndTime,
		Errors:          r.Errors,
		AgentSessions:   r.Sessions,
		Trajectories:    r.Trajectories,
		TokenUsage:      port.TokenUsage{TotalTokens: r.TotalTokens},
		UseOrchestrator: true,
	}
	if s, ok := r.Sessions["PLANNER"]; ok {
		result.MarketView = s.Decision
	}
	if s, ok := r.Sessions["QUANT"]; ok {
		result.AlphaSignals = s.Decision
	}
	if s, ok := r.Sessions["RISK"]; ok {
		result.RiskAssessment = s.Decision
	}
	if s, ok := r.Sessions["CIO"]; ok {
		result.Decision = s.Decision
	}
	result.PortfolioPlan = r.Decision
	return result
}

// GetCurrentCycle 获取当前周期结果
func (h *QuantHarness) GetCurrentCycle() *DailyCycleResult {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.currentCycle
}

// GetOrchestrator 获取编排桥（仅承载 ContextProvider 等公开能力）。
func (h *QuantHarness) GetOrchestrator() *OrchestratorBridge {
	return h.orch
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

// GetSkills 获取所有已注册技能（DLL 决策脑内持有，宿主侧返回空集合）。
func (h *QuantHarness) GetSkills() []*port.Skill {
	return nil
}

// GetTrajectories 获取轨迹列表（最近一次 DLL 决策结果）。
func (h *QuantHarness) GetTrajectories(limit int) []*port.Trajectory {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.currentOrchCycle == nil {
		return nil
	}
	out := make([]*port.Trajectory, 0, len(h.currentOrchCycle.Trajectories))
	for _, t := range h.currentOrchCycle.Trajectories {
		out = append(out, t)
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// GetTrajectoryStats 获取轨迹统计。
func (h *QuantHarness) GetTrajectoryStats() map[string]interface{} {
	h.mu.RLock()
	defer h.mu.RUnlock()
	stats := map[string]interface{}{
		"total":         0,
		"trajectories":  []*port.Trajectory{},
		"byRole":        map[string]int{},
		"byStatus":      map[string]int{},
		"avgConfidence": 0.0,
		"avgTokenUsage": 0.0,
	}
	if h.currentOrchCycle == nil {
		return stats
	}
	trajectories := h.currentOrchCycle.Trajectories
	stats["total"] = len(trajectories)
	byRole := map[string]int{}
	byStatus := map[string]int{}
	var confSum, tokSum float64
	var list []*port.Trajectory
	for _, t := range trajectories {
		if t == nil {
			continue
		}
		list = append(list, t)
		byRole[string(t.AgentRole)]++
		byStatus[t.Status]++
		confSum += t.Confidence
		tokSum += float64(t.TokenUsage.TotalTokens)
	}
	stats["trajectories"] = list
	stats["byRole"] = byRole
	stats["byStatus"] = byStatus
	if len(list) > 0 {
		stats["avgConfidence"] = confSum / float64(len(list))
		stats["avgTokenUsage"] = tokSum / float64(len(list))
	}
	return stats
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
