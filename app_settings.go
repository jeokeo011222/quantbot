package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/quantpilot/quantpilot/internal/brain/llm"
	"github.com/quantpilot/quantpilot/internal/brain/planner"
	"github.com/quantpilot/quantpilot/internal/brain/sixdim"
	brainhost "github.com/quantpilot/quantpilot/internal/brainhost"
	"github.com/quantpilot/quantpilot/internal/cio"
	"github.com/quantpilot/quantpilot/internal/config"
	"github.com/quantpilot/quantpilot/internal/data"
	"github.com/quantpilot/quantpilot/internal/llmmonitoring"
	"github.com/quantpilot/quantpilot/internal/llmstore"
	"github.com/quantpilot/quantpilot/internal/mcp"
	"github.com/quantpilot/quantpilot/internal/pricing"
	"github.com/quantpilot/quantpilot/internal/thssdk"
	"github.com/quantpilot/quantpilot/internal/tools"
	"github.com/quantpilot/quantpilot/internal/xtquant"
)

func (a *App) GetConfig() (interface{}, error) {
	if a.configManager == nil {
		return nil, fmt.Errorf("Config not initialized")
	}
	return a.configManager.GetConfig(), nil
}

// GetSixDimSourceConfig 获取市场六维判势数据源配置（设置-数据源页面展示当前生效配置）
func (a *App) GetSixDimSourceConfig() (interface{}, error) {
	if a.configManager == nil {
		return nil, fmt.Errorf("Config not initialized")
	}
	return a.configManager.GetConfig().SixDimSource, nil
}

// SetSixDimSourceConfig 保存市场六维判势数据源配置（启用开关+优先级），持久化到 config.json。
// 保存后下一次 market_sixdim_detect 判势即按新配置取数（工具每次执行实时读取），无需重启。
func (a *App) SetSixDimSourceConfig(sc config.SixDimSourceConfig) error {
	// 校验：启用的数据源优先级必须 >=1（越小越优先）
	for name, t := range map[string]config.SourceToggle{
		"northbound":        sc.Northbound,
		"margin":            sc.Margin,
		"volume_expansion":  sc.VolumeExpansion,
		"ths_boards":        sc.THSBoards,
		"limit_board":       sc.LimitBoard,
		"full_market_stats": sc.FullMarketStats,
		"overnight":         sc.Overnight,
	} {
		if t.Enabled && t.Priority < 1 {
			return fmt.Errorf("数据源 %s 优先级必须 >=1（当前: %d）", name, t.Priority)
		}
	}

	err := a.configManager.UpdateConfig(func(cfg *config.AppConfig) {
		cfg.SixDimSource = sc
	})
	if err != nil {
		return err
	}
	if a.auditService != nil {
		a.auditService.LogConfigChange("sixdim_source_config", "",
			fmt.Sprintf("northbound=%v/%d margin=%v/%d ths_boards=%v/%d limit_board=%v/%d overnight=%v/%d",
				sc.Northbound.Enabled, sc.Northbound.Priority,
				sc.Margin.Enabled, sc.Margin.Priority,
				sc.THSBoards.Enabled, sc.THSBoards.Priority,
				sc.LimitBoard.Enabled, sc.LimitBoard.Priority,
				sc.Overnight.Enabled, sc.Overnight.Priority), "user")
	}
	log.Printf("[QuantBot] 六维判势数据源配置已保存: %+v", sc)
	return nil
}

// TestSixDimSources 市场六维判势数据源「数据测试」：绕过 TTL 缓存逐个探测各数据源连通性。
// 返回每源启用状态 / 成败 / 耗时 / 失败原因，帮助诊断高优先级源健康度，全程真实请求、严禁伪造。
func (a *App) TestSixDimSources() (interface{}, error) {
	if a.configManager == nil {
		return nil, fmt.Errorf("Config not initialized")
	}
	if a.duckdbManager == nil {
		return nil, fmt.Errorf("DuckDB 未初始化")
	}
	cfg := tools.SourceConfigFromAppConfig(a.configManager.GetConfig().SixDimSource)
	probes := sixdim.ProbeDataSources(context.Background(), brainhost.AdaptMarketDataStore(a.duckdbManager), brainhost.AdaptMarketExternalSources(), brainhost.AdaptTHSSentimentSource(), cfg)
	okCnt := 0
	for _, p := range probes {
		if p.Ok {
			okCnt++
		}
	}
	log.Printf("[SixDim] 数据测试完成: %d/%d 源连通", okCnt, len(probes))
	return map[string]interface{}{
		"ok_count": okCnt,
		"total":    len(probes),
		"probes":   probes,
		"note":     "外部实时源已绕过缓存做真实探活（带6s超时+限流）；已禁用源不计入成功。",
	}, nil
}

// RefreshMarketSixDim 市场六维判势「后台强制刷新」：穿透缓存重新判势并落库至 market_sixdim_daily，
// 同时清空报告短TTL缓存，使前端随后查询立即读到最新结果。返回本次判势摘要。
func (a *App) RefreshMarketSixDim() (result interface{}, err error) {
	if a.configManager == nil {
		return nil, fmt.Errorf("Config not initialized")
	}
	if a.duckdbManager == nil {
		return nil, fmt.Errorf("DuckDB 未初始化")
	}
	cfg := tools.SourceConfigFromAppConfig(a.configManager.GetConfig().SixDimSource)
	rep, err := sixdim.Detect(context.Background(), brainhost.AdaptMarketDataStore(a.duckdbManager), brainhost.AdaptMarketExternalSources(), brainhost.AdaptTHSSentimentSource(), brainhost.AdaptSnapSource(), cfg, true, true)
	if err != nil {
		return nil, err
	}
	// 清空报告短缓存，确保前端下次 GetMarketSixDimReports 读到的是刚落库的最新数据
	clearSixDimReportsCache()
	log.Printf("[SixDim] 后台强制刷新完成: %s 总分=%.1f(调整后%.1f) 仓位=%.2f 标签=%s",
		sixdim.TradeDateOf(time.Now()), rep.RawTotalScore, rep.AdjustedTotalScore, rep.PositionRate, rep.MarketTag)
	return map[string]interface{}{
		"as_of":                sixdim.TradeDateOf(time.Now()),
		"dim_scores":           rep.DimScores,
		"raw_total_score":      rep.RawTotalScore,
		"conflict_count":       rep.ConflictCount,
		"adjusted_total_score": rep.AdjustedTotalScore,
		"position_rate":        rep.PositionRate,
		"market_tag":           rep.MarketTag,
		"sources":              rep.Sources,
		"position_advice":      fmt.Sprintf("策略信号仓位 = 原始信号仓位 × %.2f（%s）", rep.PositionRate, rep.MarketTag),
		"note":                 "已穿透缓存强制刷新并落库，前端判势卡片自动更新为最新结果。",
	}, nil
}

// GetTHSConfig 获取同花顺官方金融数据服务配置（fuyao.aicubes.cn）
func (a *App) GetTHSConfig() (interface{}, error) {
	if a.configManager == nil {
		return nil, fmt.Errorf("Config not initialized")
	}
	return a.configManager.GetConfig().THS, nil
}

// SetTHSConfig 保存同花顺官方金融数据服务配置（启用开关 + API Key + 服务地址），持久化到 config.json。
// 保存后立即重建包级 thssdk 客户端，无需重启；未启用或 Key 为空则禁用官方源。
func (a *App) SetTHSConfig(enabled bool, apiKey, baseURL string) error {
	if a.configManager == nil {
		return fmt.Errorf("Config not initialized")
	}
	if enabled && strings.TrimSpace(apiKey) == "" {
		return fmt.Errorf("启用同花顺官方数据源前，请先填写 API Key")
	}
	err := a.configManager.UpdateConfig(func(cfg *config.AppConfig) {
		cfg.THS = config.THSConfig{Enabled: enabled, APIKey: strings.TrimSpace(apiKey), BaseURL: strings.TrimSpace(baseURL)}
	})
	if err != nil {
		return err
	}
	// 立即重建包级 thssdk 客户端（六维判势/工具/财务同步下次调用即用新配置，无需重启）
	saved := a.configManager.GetConfig().THS
	if saved.Enabled && saved.APIKey != "" {
		thssdk.Configure(saved.APIKey, saved.BaseURL)
	} else {
		thssdk.Configure("", "")
	}
	if a.auditService != nil {
		a.auditService.LogConfigChange("ths_source", "", fmt.Sprintf("enabled=%v", enabled), "user")
	}
	log.Printf("[QuantBot] 同花顺官方数据源配置已保存: enabled=%v", enabled)
	return nil
}

func (a *App) UpdateConfig(updateFn func(cfg *config.AppConfig)) error {
	if a.configManager == nil {
		return fmt.Errorf("Config not initialized")
	}
	return a.configManager.UpdateConfig(updateFn)
}

func (a *App) GetCurrentCycle() (interface{}, error) {
	if a.harnessApp == nil {
		return nil, fmt.Errorf("Harness not initialized")
	}
	return a.harnessApp.GetCurrentCycle(), nil
}

func (a *App) SetMarket(market string) error {
	err := a.configManager.SetMarket(market)
	if a.auditService != nil {
		a.auditService.LogConfigChange("market", "", market, "user")
	}
	return err
}

func (a *App) SetTradingMode(mode string) error {
	// 兼容旧版 "paper" 模式，映射为 "simulated"
	if mode == "paper" {
		log.Printf("[App.SetTradingMode] Converting legacy 'paper' mode to 'simulated'")
		mode = "simulated"
	}

	// 验证交易模式
	if mode != "simulated" && mode != "live" {
		return fmt.Errorf("invalid trading mode: %s (must be 'simulated' or 'live')", mode)
	}

	err := a.configManager.SetTradingMode(mode)
	if err == nil && a.portfolioEngine != nil {
		// 同步更新Portfolio引擎的交易模式
		a.portfolioEngine.SetTradingMode(mode)
		log.Printf("[QuantBot] Portfolio engine trading mode updated to: %s", mode)
	}
	// 交易模式变化后重建执行桥（模拟<->QMT 实盘）
	if a.configManager != nil {
		a.initBroker()
	}
	// 重建执行桥后同步到 CIO 引擎，保证盘中自动买卖的 broker 引用与当前交易模式一致
	if a.cioEngine != nil {
		a.cioEngine.SetLiveBroker(a.broker)
	}
	// 同步交易审批服务启用状态（模拟接口模式启用手动确认）
	if a.tradeApproval != nil {
		a.tradeApproval.SetEnabled(mode == "simulated")
	}
	if a.auditService != nil {
		a.auditService.LogConfigChange("trading_mode", "", mode, "user")
	}
	return err
}

func (a *App) SetAISettings(provider, model, baseURL, apiKey string) error {
	err := a.configManager.SetAISettings(provider, model, baseURL, apiKey)
	if err == nil {
		// 保存成功后，用最新配置重建 Planner Agent 和 CIO Engine
		a.reinitAILayer()
	}
	if a.auditService != nil {
		a.auditService.LogConfigChange("ai_settings", "", provider+"/"+model, "user")
	}
	return err
}

// reinitAILayer 根据最新配置重建 AI 相关组件
func (a *App) reinitAILayer() {
	if a.sqliteManager == nil {
		return
	}

	cfg := a.configManager.GetConfig()

	// 停止旧的 MCP 服务器
	if a.mcpServer != nil && a.mcpServer.IsRunning() {
		a.mcpServer.Stop()
		time.Sleep(100 * time.Millisecond)
	}

	// 启动新的 MCP 服务器
	log.Printf("[QuantBot] Reinitializing MCP server on %s", cfg.MCPURL)
	a.mcpServer = mcp.NewServer(&cfg)
	if err := a.mcpServer.Start(); err != nil {
		log.Printf("[QuantBot] MCP server start error: %v, falling back to direct LLM", err)
		a.mcpServer = nil
	} else {
		if a.mcpServer.WaitReady(2 * time.Second) {
			log.Printf("[QuantBot] MCP server restarted successfully")
		} else {
			log.Printf("[QuantBot] MCP server not ready, falling back to direct LLM")
			a.mcpServer.Stop()
			a.mcpServer = nil
		}
	}

	// 创建 LLM 客户端（通过 MCP 或直接）
	var llmClient llm.Client
	if a.mcpServer != nil && a.mcpServer.IsRunning() {
		a.mcpClient = mcp.NewClient(cfg.MCPURL)
		llmClient = a.mcpClient
		log.Printf("[QuantBot] Using MCP-based LLM client (MCP URL: %s)", cfg.MCPURL)
	} else {
		llmClient = llm.NewDeepSeekClient(cfg.AIAPIKey, cfg.AIBaseURL, cfg.AIModel)
		log.Printf("[QuantBot] Using direct LLM client (MCP unavailable)")
	}
	llmClient = a.wrapLLMClient(llmClient)
	a.llmClient = llmClient

	// 重建 Planner Agent（保留用户画像和对话数据，因为它们在数据库中）
	a.plannerAgent = planner.NewPlanner(brainhost.AdaptPlannerStore(a.sqliteManager), llmClient, 1)
	if a.screenerService != nil {
		a.plannerAgent.SetScreener(brainhost.AdaptStockScreener(a.screenerService))
	}
	if data.GetDictLoader() != nil {
		a.plannerAgent.SetStockMeta(brainhost.AdaptStockMeta(data.GetDictLoader()))
	}
	a.plannerAgent.SetCallMetaDecorator(brainhost.AdaptCallMetaDecoratorLlmtrace())
	log.Printf("[QuantBot] Planner Agent reinitialized with new AI config")

	// 重建 CIO Engine
	if a.policyEngine != nil {
		a.cioEngine = cio.NewCIOEngine(a.sqliteManager, a.duckdbManager, llmClient, a.policyEngine, a.portfolioEngine, a.tradeablePool, a.appTracker())
		if a.tradeApproval != nil {
			a.cioEngine.SetApprovalService(a.tradeApproval)
		}
		a.cioEngine.SetLiveBroker(a.broker)
		a.cioEngine.SetAutoExecutionEnabled(cfg.QMTApplyAutoExecution)
		log.Printf("[QuantBot] CIO Engine reinitialized with new AI config")
	}

	// 更新 Agent Team 中所有 Agent 的 LLM 客户端
	if a.agentTeam != nil {
		for _, agent := range a.agentTeam {
			if agent != nil {
				agent.LLM = llmClient
			}
		}
		log.Printf("[QuantBot] Agent team LLM client updated for all roles")
	}

	// 更新 TaskExecutor 的 LLM 客户端
	if a.taskExecutor != nil {
		a.taskExecutor.UpdateLLMClient(llmClient)
		log.Printf("[QuantBot] Task executor LLM client updated")
	}
}

func (a *App) SetBrokerConfig(brokerType, baseURL, account, apiKey string) error {
	// 将 brokerType 映射为交易模式：live -> 实盘，其余 -> 模拟盘
	mode := "simulated"
	if brokerType == "live" {
		mode = "live"
	}

	// 更新券商配置（保留向后兼容）
	if err := a.configManager.SetBrokerConfig(brokerType, baseURL, account, apiKey); err != nil {
		log.Printf("[QuantBot] Warning: SetBrokerConfig config update failed: %v", err)
	}

	// 同步更新交易模式
	return a.SetTradingMode(mode)
}

// GetTradeJSONFiles 获取最近的交易JSON文件
func (a *App) GetTradeJSONFiles(n int) (interface{}, error) {
	if a.portfolioEngine == nil {
		return nil, fmt.Errorf("Portfolio engine not initialized")
	}

	return a.portfolioEngine.GetRecentTradeJSONFiles(n)
}

// GetTradingMode 获取当前交易模式
func (a *App) GetTradingMode() (interface{}, error) {
	if a.portfolioEngine == nil {
		return nil, fmt.Errorf("Portfolio engine not initialized")
	}

	return map[string]interface{}{
		"mode":   a.portfolioEngine.GetTradingMode(),
		"source": "portfolio_engine",
	}, nil
}

// SetQMTConfig 设置 QMT (迅投 XtQuant) 实盘交易接口配置
func (a *App) SetQMTConfig(enabled bool, path, account, accountType string, miniQMT bool, strategyName, strategyPath string) error {
	if accountType != "STOCK" && accountType != "CREDIT" {
		return fmt.Errorf("QMT 账号类型无效: %s (必须为 STOCK 或 CREDIT)", accountType)
	}
	if enabled && strings.TrimSpace(path) == "" {
		return fmt.Errorf("启用 QMT 实盘交易前，请先填写 XtQuant 路径")
	}

	err := a.configManager.SetQMTConfig(enabled, path, account, accountType, miniQMT, strategyName, strategyPath)
	if err != nil {
		return err
	}

	// 同步：启用 QMT 时强制切换为实盘交易模式
	if enabled && a.configManager.GetConfig().TradingMode != "live" {
		if err := a.SetTradingMode("live"); err != nil {
			log.Printf("[QuantBot] Warning: failed to switch to live mode: %v", err)
		}
	}

	// 将配置写入 XtQuant 文件夹下的 config.json
	if a.configManager.GetConfig().QMTPath != "" {
		_ = xtquant.WriteConfig(map[string]interface{}{
			"path":          path,
			"account":       account,
			"account_type":  accountType,
			"mini_qmt":      miniQMT,
			"strategy_name": strategyName,
			"strategy_path": strategyPath,
		})
	}

	if a.auditService != nil {
		a.auditService.LogConfigChange("qmt_config", "", fmt.Sprintf("enabled=%v account=%s", enabled, account), "user")
	}
	log.Printf("[QuantBot] QMT config updated: enabled=%v path=%s account=%s", enabled, path, account)
	return nil
}

// SetQMTApplyAutoExecution 设置「盘中自动买卖实盘」独立开关。
// 该开关仅在 QMT 实盘模式下生效：开启后盘中的自动买入/自动卖出/止盈止损也真实下发券商；
// 独立于手动/确认下单，默认关闭，避免自动执行在未充分验证时触碰真实资金。
func (a *App) SetQMTApplyAutoExecution(enabled bool) error {
	if a.configManager == nil {
		return fmt.Errorf("配置管理器未初始化")
	}
	if err := a.configManager.SetQMTApplyAutoExecution(enabled); err != nil {
		return err
	}
	// 立即生效：同步到 CIO 引擎（盘中自动买卖走 QMT 实盘下发）
	if a.cioEngine != nil {
		a.cioEngine.SetAutoExecutionEnabled(enabled)
	}
	if a.auditService != nil {
		a.auditService.LogConfigChange("qmt_auto_execution", "", fmt.Sprintf("enabled=%v", enabled), "user")
	}
	log.Printf("[QuantBot] 盘中自动买卖实盘开关已更新: %v", enabled)
	return nil
}

// GetQMTConfig 获取 QMT 配置与 XtQuant 文件夹状态
func (a *App) GetQMTConfig() (interface{}, error) {
	cfg := a.configManager.GetConfig()
	return map[string]interface{}{
		"enabled":        cfg.QMTEnabled,
		"path":           cfg.QMTPath,
		"account":        cfg.QMTAccount,
		"account_type":   cfg.QMTAccountType,
		"mini_qmt":       cfg.QMTMiniQMT,
		"strategy_name":  cfg.QMTStrategyName,
		"strategy_path":  cfg.QMTStrategyPath,
		"auto_execution": cfg.QMTApplyAutoExecution,
		"folder_status":  xtquant.CheckStatus(),
	}, nil
}

// TestQMTConnection 测试 QMT (XtQuant) 连接环境
func (a *App) TestQMTConnection() (interface{}, error) {
	st := xtquant.CheckStatus()
	result := map[string]interface{}{
		"success":         st.FolderExists && st.PythonExists,
		"folder_exists":   st.FolderExists,
		"folder_path":     st.FolderPath,
		"interface_files": st.InterfaceFiles,
		"python_exists":   st.PythonExists,
		"xtquant_exists":  st.XtQuantExists,
		"message":         st.Message,
	}
	log.Printf("[QuantBot] QMT connection test: %+v", result)
	return result, nil
}

// GetXtQuantFolderPath 获取 XtQuant 文件夹路径
func (a *App) GetXtQuantFolderPath() (interface{}, error) {
	return map[string]interface{}{
		"path":  xtquant.GetFolderPath(),
		"files": xtquant.GetInterfaceFiles(),
	}, nil
}

func (a *App) SetDataProvider(provider string) error {
	log.Printf("[App.SetDataProvider] Called with provider: '%s'", provider)

	// 验证 provider 值
	validProviders := map[string]bool{
		"native_tdx":   true,
		"tdx_local":    true,
		"python_tdx":   true,
		"tdx_mcp":      true,
		"mcp":          true,
		"tdx_terminal": true,
		"tencent":      true,
	}
	if !validProviders[provider] {
		log.Printf("[App.SetDataProvider] INVALID provider: '%s'", provider)
		return fmt.Errorf("invalid data provider: %s", provider)
	}

	err := a.configManager.SetDataProvider(provider)
	if err != nil {
		log.Printf("[App.SetDataProvider] configManager.SetDataProvider FAILED: %v", err)
		return fmt.Errorf("failed to save data provider config: %w", err)
	}
	log.Printf("[App.SetDataProvider] configManager.SetDataProvider OK")

	// 验证配置已保存
	cfg := a.configManager.GetConfig()
	log.Printf("[App.SetDataProvider] Config after save: data_provider='%s'", cfg.DataProvider)

	// 优先更新独立的 tdxManager
	if a.tdxManager != nil {
		log.Printf("[App.SetDataProvider] Updating tdxManager...")
		a.tdxManager.SetProvider(provider)
		log.Printf("[App.SetDataProvider] tdxManager updated OK")
	} else {
		log.Printf("[App.SetDataProvider] tdxManager is nil, skipping")
	}

	// 同时更新 harnessApp（如果存在）
	if a.harnessApp != nil {
		log.Printf("[App.SetDataProvider] Updating harnessApp...")
		a.harnessApp.UpdateDataProvider(provider, cfg)
	}

	if a.auditService != nil {
		a.auditService.LogConfigChange("data_provider", "", provider, "user")
	}

	log.Printf("[App.SetDataProvider] DONE. Provider set to: '%s'", provider)
	return nil
}

func (a *App) SetTDXPath(path string) error {
	err := a.configManager.SetTDXPath(path)
	if a.auditService != nil {
		a.auditService.LogConfigChange("tdx_path", "", path, "user")
	}
	return err
}

// ConnectNativeTDX 连接 Go 原生 TDX 服务（支持独立 tdxManager）
func (a *App) ConnectNativeTDX() (interface{}, error) {
	// 优先使用独立的 tdxManager（即使 Harness 未初始化也可用）
	if a.tdxManager != nil {
		err := a.tdxManager.ConnectNativeTDX()
		if err != nil {
			return map[string]interface{}{
				"status":  "error",
				"message": err.Error(),
			}, nil
		}
		return map[string]interface{}{
			"status":  "connected",
			"message": "Go Native TDX 连接成功",
		}, nil
	}

	// 降级：使用 harnessApp
	if a.harnessApp != nil {
		err := a.harnessApp.ConnectNativeTDX()
		if err != nil {
			return map[string]interface{}{
				"status":  "error",
				"message": err.Error(),
			}, nil
		}
		return map[string]interface{}{
			"status":  "connected",
			"message": "Go Native TDX 连接成功",
		}, nil
	}

	return map[string]interface{}{
		"status":  "error",
		"message": "TDX 服务管理器未初始化",
	}, nil
}

// TestNativeTDX 测试 Go 原生 TDX 连接（支持独立 tdxManager）
func (a *App) TestNativeTDX() (interface{}, error) {
	// 优先使用独立的 tdxManager
	if a.tdxManager != nil {
		ok, msg := a.tdxManager.TestNativeTDXConnection()
		return map[string]interface{}{
			"success": ok,
			"message": msg,
		}, nil
	}

	// 降级：使用 harnessApp
	if a.harnessApp != nil {
		ok, msg := a.harnessApp.TestNativeTDX()
		return map[string]interface{}{
			"success": ok,
			"message": msg,
		}, nil
	}

	return map[string]interface{}{
		"success": false,
		"message": "TDX 服务管理器未初始化",
	}, nil
}

func (a *App) SetMCPConfig(url, apiKey string) error {
	err := a.configManager.SetMCPConfig(url, apiKey)
	if a.auditService != nil {
		a.auditService.LogConfigChange("mcp_config", "", url, "user")
	}
	return err
}

func (a *App) SetInitialCapital(capital float64) error {
	// 初始资金上限 200 万
	if capital < 0 || capital > 2000000 {
		return fmt.Errorf("初始资金必须在 0~200万 之间，当前: %.2f", capital)
	}
	err := a.configManager.SetInitialCapital(capital)
	if err != nil {
		return err
	}
	// 同步重置内存组合引擎，使新期初资金立即生效
	if a.portfolioEngine != nil {
		a.portfolioEngine.Reset(capital)
		log.Printf("[QuantBot] 初始资金已更新为 %.2f，内存组合引擎已同步重置", capital)
	}
	if a.auditService != nil {
		a.auditService.LogConfigChange("initial_capital", "", fmt.Sprintf("%.2f", capital), "user")
	}
	return nil
}

// SetActivityRefreshMinutes 设置实时活动刷新间隔（分钟，1-60整数）
func (a *App) SetActivityRefreshMinutes(minutes int) error {
	if minutes < 1 || minutes > 60 {
		return fmt.Errorf("实时活动刷新间隔必须在1-60分钟之间（整数），当前: %d", minutes)
	}
	err := a.configManager.SetActivityRefreshMinutes(minutes)
	if err != nil {
		return err
	}
	// 同步应用到任务调度器（盘中监控任务重复间隔）
	if a.taskScheduler != nil {
		a.taskScheduler.SetInMarketRepeatInterval(minutes * 60)
	}
	// 同步应用到组合引擎持续交易监控
	if a.portfolioEngine != nil {
		a.portfolioEngine.StartContinuousTrading(a.ctx, time.Duration(minutes)*time.Minute)
	}
	if a.auditService != nil {
		a.auditService.LogConfigChange("activity_refresh_minutes", "", fmt.Sprintf("%d", minutes), "user")
	}
	log.Printf("[QuantBot] Activity refresh interval set to %d minutes", minutes)
	return nil
}

// wrapLLMClient 包装 LLM 客户端，实现可开关的大模型输入/输出内容审计记录
func (a *App) wrapLLMClient(inner llm.Client) llm.Client {
	if inner == nil || a.sqliteManager == nil || a.configManager == nil {
		return inner
	}
	return llmmonitoring.NewClient(inner,
		func() bool { return a.configManager.GetConfig().EnableLLMLogging },
		func(rec llmmonitoring.Record) {
			if a.llmStore == nil {
				return
			}
			_ = a.llmStore.Save(llmstore.Entry{
				TaskDate:         rec.TaskDate,
				AgentRole:        rec.AgentRole,
				TaskName:         rec.TaskName,
				Phase:            rec.Phase,
				Model:            rec.Model,
				InputMessages:    rec.InputMessages,
				OutputContent:    rec.OutputContent,
				FinishReason:     rec.FinishReason,
				ToolCallCount:    rec.ToolCallCount,
				PromptTokens:     rec.PromptTokens,
				CompletionTokens: rec.CompletionTokens,
				TotalTokens:      rec.TotalTokens,
				DurationMs:       rec.DurationMs,
				Status:           rec.Status,
				ErrorMessage:     rec.ErrorMessage,
				CreatedAt:        rec.CreatedAt,
			})
		},
	)
}

// SetLLMLoggingEnabled 设置是否记录大模型调用输入/输出内容（可开关）
func (a *App) SetLLMLoggingEnabled(enabled bool) error {
	err := a.configManager.SetEnableLLMLogging(enabled)
	if err != nil {
		return err
	}
	if a.auditService != nil {
		a.auditService.LogConfigChange("enable_llm_logging", "", fmt.Sprintf("%v", enabled), "user")
	}
	log.Printf("[QuantBot] LLM input/output logging enabled=%v", enabled)
	return nil
}

// GetLLMCallLogs 查询大模型调用日志（date/role/status 可为空表示全部，limit 默认100）
func (a *App) GetLLMCallLogs(date, role, status string, limit int) ([]llmstore.Entry, error) {
	if a.llmStore == nil {
		return nil, fmt.Errorf("LLM 日志存储未初始化")
	}
	return a.llmStore.Query(date, role, status, limit)
}

// GetLLMCallStats 统计某日大模型调用次数与 token 消耗（date 为空则统计全部）
func (a *App) GetLLMCallStats(date string) (map[string]interface{}, error) {
	if a.llmStore == nil {
		return nil, fmt.Errorf("LLM 日志存储未初始化")
	}
	if date == "" {
		date = time.Now().Format("2006-01-02")
	}
	count, tokens, err := a.llmStore.Stats(date)
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{
		"date":         date,
		"call_count":   count,
		"total_tokens": tokens,
	}, nil
}

func (a *App) ResetDefaults() error {
	err := a.configManager.ResetDefaults()
	if a.auditService != nil {
		a.auditService.LogConfigChange("reset_defaults", "", "", "user")
	}
	return err
}

func (a *App) TestDataProvider(provider string) (interface{}, error) {
	// 优先使用指定的 provider；为空时回退到当前激活的数据源
	if provider == "" {
		provider = a.tdxManager.GetProvider()
	}
	// 优先使用独立的 tdxManager
	if a.tdxManager != nil {
		switch provider {
		case "native_tdx":
			ok, msg := a.tdxManager.TestNativeTDXConnection()
			return map[string]interface{}{
				"provider": provider,
				"success":  ok,
				"message":  msg,
			}, nil
		case "tdx_mcp":
			if a.tdxManager.HasMCPClient() {
				return map[string]interface{}{
					"provider": provider,
					"success":  true,
					"message":  "MCP 客户端可用",
				}, nil
			}
			return map[string]interface{}{
				"provider": provider,
				"success":  false,
				"message":  "MCP 客户端不可用，请检查 MCP URL 配置",
			}, nil
		case "tdx_terminal":
			ok, msg := a.tdxManager.TestTerminalConnection()
			return map[string]interface{}{
				"provider": provider,
				"success":  ok,
				"message":  msg,
			}, nil
		case "tencent":
			// 腾讯财经无需本机通达信，直接用真实行情探测：抓取贵州茅台、浦发银行实时报价
			snaps, err := data.FetchRealStockSnapshots([]string{"sh600519", "sh600000"})
			if err != nil {
				return map[string]interface{}{
					"provider": provider,
					"success":  false,
					"message":  fmt.Sprintf("腾讯财经实时行情获取失败: %v", err),
				}, nil
			}
			if len(snaps) == 0 {
				return map[string]interface{}{
					"provider": provider,
					"success":  false,
					"message":  "腾讯财经实时行情返回为空",
				}, nil
			}
			return map[string]interface{}{
				"provider": provider,
				"success":  true,
				"message":  fmt.Sprintf("腾讯财经实时行情连接正常（示例：%s %s %.2f）", snaps[0].Name, snaps[0].Code, snaps[0].CurrentPrice),
			}, nil
		default:
			return map[string]interface{}{
				"provider": provider,
				"success":  false,
				"message":  fmt.Sprintf("未知数据源类型: %s，无法验证真实行情数据源", provider),
			}, nil
		}
	}

	// 降级：使用 harnessApp
	if a.harnessApp != nil {
		return a.harnessApp.TestDataProvider()
	}

	return map[string]interface{}{
		"provider": "unknown",
		"success":  false,
		"message":  "TDX 服务管理器未初始化",
	}, nil
}

// GetTierInfo 获取当前版本信息
func (a *App) GetTierInfo() (interface{}, error) {
	tier := pricing.GetCurrentTier()
	cfg := pricing.GetTierConfig(tier)

	return map[string]interface{}{
		"tier":        string(tier),
		"name":        cfg.Name,
		"displayName": cfg.DisplayName,
		"description": cfg.Description,
		"features":    cfg.Features,
		"limits": map[string]interface{}{
			"maxScreeningPerDay": cfg.Limits.MaxScreeningPerDay,
			"maxResults":         cfg.Limits.MaxResults,
			"allowCustomWeights": cfg.Limits.AllowCustomWeights,
			"allowFactorHealth":  cfg.Limits.AllowFactorHealth,
			"allowDynamicWeight": cfg.Limits.AllowDynamicWeight,
			"allowStructureRisk": cfg.Limits.AllowStructureRisk,
			"allowPortfolioOpt":  cfg.Limits.AllowPortfolioOpt,
			"allowAIAgent":       cfg.Limits.AllowAIAgent,
			"allowPrivateFactor": cfg.Limits.AllowPrivateFactor,
			"allowPrivateDeploy": cfg.Limits.AllowPrivateDeploy,
		},
	}, nil
}

// UpgradeTier 升级版本（统一版本，始终设置为 unified）
func (a *App) UpgradeTier(newTier string) error {
	pricing.SetCurrentTier(pricing.Unified)

	// 持久化到配置
	if a.configManager != nil {
		_ = a.configManager.UpdateConfig(func(cfg *config.AppConfig) {
			cfg.PricingTier = "unified"
		})
	}

	// 持久化到 SQLite 用户表
	if a.sqliteManager != nil {
		db := a.sqliteManager.GetDB()
		var user data.User
		if err := db.First(&user).Error; err == nil {
			user.Tier = "unified"
			user.UpdatedAt = time.Now()
			expireAt := time.Now().AddDate(0, 1, 0)
			user.ExpireAt = &expireAt
			db.Save(&user)
		}
	}

	if a.auditService != nil {
		a.auditService.LogAuditEvent(
			data.AuditEventTierChange,
			"版本已切换为统一版本",
			"system",
			"tier_upgrade",
			"user",
			"user",
			"success",
			map[string]interface{}{
				"new_tier":  "unified",
				"timestamp": time.Now().Format(time.RFC3339),
			},
		)
	}

	log.Printf("[QuantBot] Tier set to unified")
	return nil
}

// GetUpgradePath 获取升级路径（统一版本，返回全功能配置）
func (a *App) GetUpgradePath() (interface{}, error) {
	cfg := pricing.GetTierConfig(pricing.Unified)

	return map[string]interface{}{
		"unified": map[string]interface{}{
			"name":        cfg.DisplayName,
			"description": cfg.Description,
			"features":    cfg.Features,
			"price":       "全功能开放",
		},
	}, nil
}
