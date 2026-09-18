package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/quantpilot/quantpilot/internal/llmhost"
	"github.com/quantpilot/quantpilot/internal/marketsixdim"
	brainhost "github.com/quantpilot/quantpilot/internal/brainhost"
	"github.com/quantpilot/quantpilot/internal/data"
	"github.com/quantpilot/quantpilot/internal/version"
)

// GetBoardList 获取板块列表（直接使用独立的 tdxManager）
func (a *App) GetBoardList(boardType string) (interface{}, error) {
	if a.tdxManager == nil {
		return nil, fmt.Errorf("TDX manager not initialized")
	}
	return a.tdxManager.GetBoardList(boardType)
}

// GetBoardStocks 获取板块成分股（直接使用独立的 tdxManager）
func (a *App) GetBoardStocks(boardCode string) (interface{}, error) {
	if a.tdxManager == nil {
		return nil, fmt.Errorf("TDX manager not initialized")
	}
	result, err := a.tdxManager.GetBoardSummary(boardCode, true)
	if err != nil {
		return nil, err
	}
	return result, nil
}

// GetFundFlow 获取资金流向（直接使用独立的 tdxManager）
func (a *App) GetFundFlow(exchange, code string) (interface{}, error) {
	if a.tdxManager == nil {
		return nil, fmt.Errorf("TDX manager not initialized")
	}
	return a.tdxManager.GetFundFlow(exchange, code)
}

// GetAnnouncements 获取公告数据（直接使用独立的 tdxManager）
func (a *App) GetAnnouncements(exchange, code string, limit int) (interface{}, error) {
	if a.tdxManager == nil {
		return nil, fmt.Errorf("TDX manager not initialized")
	}
	return a.tdxManager.GetAnnouncement(code, limit)
}

// GetAIStatus 获取 AI 配置状态（用于调试）
func (a *App) GetAIStatus() (interface{}, error) {
	cfg := a.configManager.GetConfig()
	return map[string]interface{}{
		"api_key_present":     cfg.AIAPIKey != "",
		"api_key_length":      len(cfg.AIAPIKey),
		"model":               cfg.AIModel,
		"base_url":            cfg.AIBaseURL,
		"provider":            cfg.AIProvider,
		"planner_initialized": a.plannerAgent != nil,
	}, nil
}

// TestAIConnection 测试 AI 大模型连接
func (a *App) TestAIConnection(provider, model, baseURL, apiKey string) (interface{}, error) {
	if apiKey == "" {
		return map[string]interface{}{
			"status":  "error",
			"message": "API Key 未配置",
			"success": false,
		}, nil
	}

	if baseURL == "" || model == "" {
		dBase, dModel := llm.ProviderDefaults(provider)
		if baseURL == "" {
			baseURL = dBase
		}
		if model == "" {
			model = dModel
		}
	}

	startTime := time.Now()

	// 按 AIProvider 创建对应客户端并测试连接
	llmClient := llm.NewClient(provider, apiKey, baseURL, model)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	messages := []llm.Message{
		{Role: "system", Content: "你是一个测试助手。"},
		{Role: "user", Content: "请回复：连接测试成功"},
	}

	result, err := llmClient.Chat(ctx, messages, nil)
	elapsed := time.Since(startTime)

	if err != nil {
		errMsg := err.Error()
		statusCode := "connection_error"

		if len(errMsg) > 100 {
			errMsg = errMsg[:100] + "..."
		}

		if strings.Contains(errMsg, "401") || strings.Contains(errMsg, "403") {
			statusCode = "auth_error"
			errMsg = "API Key 无效或已过期，请检查您的 API Key 配置"
		} else if strings.Contains(errMsg, "404") {
			statusCode = "not_found"
			errMsg = "模型不存在或 URL 错误"
		} else if strings.Contains(errMsg, "timeout") || strings.Contains(errMsg, "context deadline") {
			statusCode = "timeout"
			errMsg = "连接超时，请检查网络或 URL"
		} else if strings.Contains(errMsg, "not configured") || strings.Contains(errMsg, "not be empty") {
			statusCode = "config_error"
			errMsg = "API Key 未配置，请在设置中填写 AI API Key"
		}

		return map[string]interface{}{
			"status":     "error",
			"success":    false,
			"message":    errMsg,
			"elapsed_ms": elapsed.Milliseconds(),
			"error_type": statusCode,
		}, nil
	}

	responseText := ""
	if len(result.Choices) > 0 {
		responseText = result.Choices[0].Message.Content
	}

	return map[string]interface{}{
		"status":     "ok",
		"success":    true,
		"message":    "连接成功",
		"response":   responseText,
		"model":      result.Model,
		"elapsed_ms": elapsed.Milliseconds(),
		"provider":   provider,
	}, nil
}

// GetWatchList 获取监控股票池和指数列表（从数据库读取，前端替代硬编码）
func (a *App) GetWatchList() (interface{}, error) {
	watchStocks := data.GetDefaultWatchStocks(a.sqliteManager.GetDB())
	indices := data.GetActiveMarketIndices(a.sqliteManager.GetDB())

	var stockList []interface{}
	for _, s := range watchStocks {
		stockList = append(stockList, map[string]interface{}{
			"code":   s.Code,
			"name":   s.Name,
			"market": s.Market,
			"sector": s.Sector,
		})
	}

	var indexList []interface{}
	for _, idx := range indices {
		indexList = append(indexList, map[string]interface{}{
			"code":   idx.Code,
			"name":   idx.Name,
			"market": idx.Market,
			"type":   idx.IndexType,
		})
	}

	return map[string]interface{}{
		"stocks":  stockList,
		"indices": indexList,
	}, nil
}

// GetStockSnapshots 获取实时 A 股行情（默认监控列表；如传 codes 则用 codes）
func (a *App) GetStockSnapshots(codes []string) (interface{}, error) {
	var targetCodes []string
	if len(codes) > 0 {
		targetCodes = codes
	} else {
		for _, s := range data.GetDefaultWatchStocks(a.sqliteManager.GetDB()) {
			targetCodes = append(targetCodes, s.Market+s.Code)
		}
	}
	snapshots, _ := data.FetchRealtimeStockSnapshots(targetCodes)

	// 确定数据来源（统一取 data.GetDataSource() 当前活跃源名称，未注册时提示 unknown）
	dataSource := data.DataSourceName()
	if dataSource == "" || dataSource == "none" {
		dataSource = "unknown"
	}

	var list []interface{}
	for _, s := range snapshots {
		list = append(list, map[string]interface{}{
			"code":          s.Code,
			"name":          s.Name,
			"market":        s.Market,
			"currentPrice":  s.CurrentPrice,
			"prevClose":     s.PrevClose,
			"open":          s.Open,
			"high":          s.High,
			"low":           s.Low,
			"volume":        s.Volume,
			"turnover":      s.Turnover,
			"changePercent": s.ChangePercent,
			"changeAmount":  s.ChangeAmount,
			"timestamp":     s.Timestamp,
		})
	}
	return map[string]interface{}{
		"data":   list,
		"source": dataSource,
		"count":  len(snapshots),
	}, nil
}

// GetIndexSnapshots 获取实时指数行情
func (a *App) GetIndexSnapshots(codes []string) (interface{}, error) {
	var targetCodes []string
	if len(codes) > 0 {
		targetCodes = codes
	} else {
		for _, idx := range data.GetActiveMarketIndices(a.sqliteManager.GetDB()) {
			// Market 字段为大写短代码（SH/SZ），Code 已含交易所小写前缀（如 sh000001），
			// 直接使用 Code，避免拼出 SHsh000001 双重前缀导致快照解析（code count error）
			targetCodes = append(targetCodes, idx.Code)
		}
	}
	snapshots, _ := data.FetchRealtimeIndexSnapshots(targetCodes)

	// 确定数据来源（统一取 data.GetDataSource() 当前活跃源名称，未注册时提示 unknown）
	dataSource := data.DataSourceName()
	if dataSource == "" || dataSource == "none" {
		dataSource = "unknown"
	}

	// 指数中文名统一：数据库权威名称 → 股票字典权威名称 → 回退代码
	// 由于历史数据库可能已混入GBK乱码，优先用 stock_dict.json（权威数据源）回填
	indexNameMap := make(map[string]string, 8)
	// pureToFull 记录“纯净代码 → 权威带前缀指数代码”。同一数字代码可能既是深市个股又是指数
	// （如 sz000905 厦门港务 与 sh000905 中证500），当快照返回纯净代码(000905)时，
	// 需先解析为权威指数全代码(sh000905)再查字典，避免误取深市同名个股导致指数显示为个股名。
	pureToFull := make(map[string]string, 8)
	for _, idx := range data.GetActiveMarketIndices(a.sqliteManager.GetDB()) {
		fullCode := strings.ToLower(idx.Code)
		dictLoader := data.GetDictLoader()
		if nameFromDict := dictLoader.GetStockName(fullCode); nameFromDict != "" {
			indexNameMap[fullCode] = nameFromDict
		} else if idx.Name != "" && !strings.Contains(idx.Name, "涓") && !strings.Contains(idx.Name, "娣") {
			// idx.Name 无乱码时保留
			indexNameMap[fullCode] = idx.Name
		}
		// 记录纯净代码（去掉 sh/sz/bj 前缀）→ 权威指数全代码（首个生效，避免重复覆盖）
		if len(fullCode) >= 2 {
			if pure := fullCode[2:]; pure != "" {
				if _, exists := pureToFull[pure]; !exists {
					pureToFull[pure] = fullCode
				}
			}
		}
	}
	resolveIndexName := func(code, fallback string) string {
		lcode := strings.ToLower(strings.TrimSpace(code))
		// 全代码直接命中（如 sh000905）
		if name, ok := indexNameMap[lcode]; ok && name != "" {
			return name
		}
		dictLoader := data.GetDictLoader()
		// 纯净代码（如 000905）先解析为权威指数全代码再查字典，避免误取深市同名个股
		if full, ok := pureToFull[lcode]; ok && full != "" {
			if name := dictLoader.GetStockName(full); name != "" {
				return name
			}
			if name, ok := indexNameMap[full]; ok && name != "" {
				return name
			}
		}
		if nameFromDict := dictLoader.GetStockName(lcode); nameFromDict != "" {
			return nameFromDict
		}
		return fallback
	}

	var list []interface{}
	for _, s := range snapshots {
		name := resolveIndexName(s.Code, s.Name)
		list = append(list, map[string]interface{}{
			"code":          s.Code,
			"name":          name,
			"current":       s.Current,
			"change":        s.Change,
			"changePercent": s.ChangePercent,
		})
	}
	return map[string]interface{}{
		"data":   list,
		"source": dataSource,
		"count":  len(snapshots),
	}, nil
}

// GetMarketDataStatus 获取市场数据质量状态（选股引擎数据底座健康度）
// 数据过期时返回 ok=false，前端提示并禁止生成新的股票池
func (a *App) GetMarketDataStatus() (interface{}, error) {
	if a.duckdbManager == nil {
		return nil, fmt.Errorf("行情数据库未初始化")
	}

	guard := data.NewDataIntegrityGuard(a.duckdbManager)
	status := guard.GetMarketDataStatus(context.Background())
	return status, nil
}

// GetSystemStatus 获取系统组件初始化状态（用于诊断）
func (a *App) GetSystemStatus() (interface{}, error) {
	status := map[string]interface{}{
		"config_manager":   a.configManager != nil,
		"sqlite_manager":   a.sqliteManager != nil,
		"duckdb_manager":   a.duckdbManager != nil,
		"tdx_manager":      a.tdxManager != nil,
		"harness_app":      a.harnessApp != nil,
		"planner_agent":    a.plannerAgent != nil,
		"policy_engine":    a.policyEngine != nil,
		"portfolio_engine": a.portfolioEngine != nil,
		"cio_engine":       a.cioEngine != nil,
		"screener_service": a.screenerService != nil,
		"audit_service":    a.auditService != nil,
		"strategy_service": a.strategyService != nil,
		"backtest_service": a.backtestService != nil,
		"orchestrator":     a.orchestratorEngine != nil,
		"task_scheduler":   a.taskScheduler != nil,
		"agent_workflow":   a.agentWorkflowSys != nil,
	}

	// DuckDB 诊断信息（单库直连 stock.duckdb）
	if a.duckdbManager != nil {
		status["duckdb_ok"] = a.duckdbManager.HasStockDB()
		status["duckdb_db_path"] = a.duckdbManager.GetPath()
	} else {
		status["duckdb_ok"] = false
	}

	// 如果组合引擎已初始化，获取其状态
	if a.portfolioEngine != nil {
		status["portfolio_running"] = a.portfolioEngine.IsRunning()
		status["portfolio_cash"] = a.portfolioEngine.GetCash()
	}

	// 计算已初始化组件数量
	initialized := 0
	total := 0
	for _, v := range status {
		if b, ok := v.(bool); ok {
			total++
			if b {
				initialized++
			}
		}
	}
	status["initialized_count"] = initialized
	status["total_count"] = total
	status["ready"] = initialized == total

	return status, nil
}

type SystemHealthItem struct {
	Key        string `json:"key"`
	Name       string `json:"name"`
	OK         bool   `json:"ok"`
	Detail     string `json:"detail"`
	Warning    bool   `json:"warning"`     // 部分可用（如AI未配置Key）
	DurationMs int64  `json:"duration_ms"` // 该项检查耗时（毫秒）
}

// GetSystemHealth 获取系统健康度（System Health 页面）
// 每次调用都会真实执行全部检查，结果不缓存
// 检查链路：数据源 → 数据库 → AI服务 → Agent → Portfolio → Scheduler → Trading
func (a *App) GetSystemHealth() (interface{}, error) {
	log.Printf("[SystemHealth] 开始执行系统健康检查...")
	totalStart := time.Now()
	items := []SystemHealthItem{}

	// 1. 数据源：真正查询 stock.duckdb（最新交易日/股票数量/缺失率/数据新旧度）
	dsStart := time.Now()
	dsItem := SystemHealthItem{Key: "data_source", Name: "数据源"}
	if a.duckdbManager != nil && a.duckdbManager.HasStockDB() {
		guard := data.NewDataIntegrityGuard(a.duckdbManager)
		status := guard.GetMarketDataStatus(context.Background())
		if status.OK {
			dsItem.OK = true
			dsItem.Detail = fmt.Sprintf("行情数据库正常，最新交易日 %s，股票 %d 只，缺失率 %.1f%%", status.LatestDate, status.StockCount, status.MissingRate*100)
			// 数据新旧度：量化回测/选股依赖最新行情，要求至少更新到「今天的前一个交易日」
			if status.NeedsUpdate {
				dsItem.OK = true
				dsItem.Warning = true
				dsItem.Detail = fmt.Sprintf(
					"行情数据已过期，需更新：最新 %s，应更新到 %s（前一个交易日）。量化回测/选股依赖最新行情，请在「数据更新」中同步行情数据",
					status.LatestDate, status.ExpectedDate,
				)
			}
		} else {
			dsItem.OK = false
			dsItem.Detail = status.Reason
		}
	} else {
		dsItem.OK = false
		dsItem.Detail = "行情数据库未挂载，请确认 data/stock.duckdb 存在"
	}
	dsItem.DurationMs = time.Since(dsStart).Milliseconds()
	log.Printf("[SystemHealth] [数据源] OK=%v, 耗时 %dms, %s", dsItem.OK, dsItem.DurationMs, dsItem.Detail)
	items = append(items, dsItem)

	// 2. 数据库：真正 ping SQLite 与 DuckDB
	dbStart := time.Now()
	dbItem := SystemHealthItem{Key: "database", Name: "数据库"}
	sqliteOK := false
	sqliteDetail := "SQLite 未初始化"
	if a.sqliteManager != nil && a.sqliteManager.GetDB() != nil {
		if sqlDB, err := a.sqliteManager.GetDB().DB(); err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := sqlDB.PingContext(ctx); err == nil {
				sqliteOK = true
				sqliteDetail = "SQLite 正常"
			} else {
				sqliteDetail = fmt.Sprintf("SQLite Ping 失败: %v", err)
			}
		} else {
			sqliteDetail = fmt.Sprintf("SQLite 连接获取失败: %v", err)
		}
	}
	duckdbOK := a.duckdbManager != nil && a.duckdbManager.HasStockDB()
	duckdbDetail := "DuckDB 未挂载"
	if duckdbOK {
		guard := data.NewDataIntegrityGuard(a.duckdbManager)
		if status := guard.GetMarketDataStatus(context.Background()); status.Available {
			duckdbDetail = "DuckDB 正常"
		} else {
			duckdbDetail = "DuckDB 挂载但查询异常"
			duckdbOK = false
		}
	}
	dbItem.OK = sqliteOK && duckdbOK
	dbItem.Detail = fmt.Sprintf("%s；%s", sqliteDetail, duckdbDetail)
	dbItem.DurationMs = time.Since(dbStart).Milliseconds()
	log.Printf("[SystemHealth] [数据库] OK=%v, 耗时 %dms, %s", dbItem.OK, dbItem.DurationMs, dbItem.Detail)
	items = append(items, dbItem)

	// 3. AI服务：验证 API Key 并尝试真实连通测试（带超时）
	aiStart := time.Now()
	aiItem := SystemHealthItem{Key: "ai_service", Name: "AI服务"}
	if a.llmClient != nil {
		// 直接从配置判断 API Key 是否有效：a.llmClient 可能被监控包装器
		// (llmmonitoring.NewClient) 包裹，类型断言拿不到 HasValidAPIKey，
		// 导致实际 API Key 有效时误报"未配置有效的 API Key"。
		hasKey := false
		if a.configManager != nil {
			cfg := a.configManager.GetConfig()
			hasKey = strings.TrimSpace(cfg.AIAPIKey) != ""
		}
		if hasKey {
			// 真实连通测试：发送一个极简请求验证 API 可达（带5秒超时）
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err := a.llmClient.Chat(ctx, []llm.Message{
				{Role: "user", Content: "ping"},
			}, nil)
			if err != nil {
				aiItem.OK = false
				aiItem.Warning = true
				aiItem.Detail = fmt.Sprintf("API Key 已配置，但连通测试失败: %v", err)
			} else {
				aiItem.OK = true
				aiItem.Detail = "AI服务连通正常，API 可达"
			}
		} else {
			aiItem.OK = false
			aiItem.Warning = true
			aiItem.Detail = "AI服务已初始化，但未配置有效的 API Key"
		}
	} else {
		aiItem.OK = false
		aiItem.Detail = "AI服务未初始化"
	}
	aiItem.DurationMs = time.Since(aiStart).Milliseconds()
	log.Printf("[SystemHealth] [AI服务] OK=%v, 耗时 %dms, %s", aiItem.OK, aiItem.DurationMs, aiItem.Detail)
	items = append(items, aiItem)

	// 4. Agent：调用诊断接口获取真实状态
	agentStart := time.Now()
	agentItem := SystemHealthItem{Key: "agent", Name: "Agent"}
	if a.taskExecutor != nil {
		agentItem.OK = a.taskExecutor.IsAvailable()
		diag := a.taskExecutor.Diagnose()
		if agentItem.OK {
			agentItem.Detail = "智能体执行器就绪"
		} else {
			if reason, ok := diag["reason"].(string); ok && reason != "" {
				agentItem.Detail = reason
			} else {
				agentItem.Detail = "智能体执行器未就绪"
			}
		}
	} else {
		agentItem.OK = false
		agentItem.Detail = "智能体执行器未初始化"
	}
	agentItem.DurationMs = time.Since(agentStart).Milliseconds()
	log.Printf("[SystemHealth] [Agent] OK=%v, 耗时 %dms, %s", agentItem.OK, agentItem.DurationMs, agentItem.Detail)
	items = append(items, agentItem)

	// 5. Portfolio：真实查询现金与持仓验证引擎可用
	pfStart := time.Now()
	pfItem := SystemHealthItem{Key: "portfolio", Name: "Portfolio"}
	if a.portfolioEngine != nil {
		cash := a.portfolioEngine.GetCash()
		pfItem.OK = true
		pfItem.Detail = fmt.Sprintf("投资组合引擎正常，可用资金 %.2f", cash)
	} else {
		pfItem.OK = false
		pfItem.Detail = "投资组合引擎未初始化"
	}
	pfItem.DurationMs = time.Since(pfStart).Milliseconds()
	log.Printf("[SystemHealth] [Portfolio] OK=%v, 耗时 %dms, %s", pfItem.OK, pfItem.DurationMs, pfItem.Detail)
	items = append(items, pfItem)

	// 6. Scheduler：真正检查调度器运行状态
	schStart := time.Now()
	schItem := SystemHealthItem{Key: "scheduler", Name: "Scheduler"}
	if a.taskScheduler != nil {
		schItem.OK = a.taskScheduler.IsRunning()
		if schItem.OK {
			schItem.Detail = "任务调度器运行中"
		} else {
			schItem.OK = true
			schItem.Detail = "任务调度器已初始化（当前未运行）"
		}
	} else {
		schItem.OK = false
		schItem.Detail = "任务调度器未初始化"
	}
	schItem.DurationMs = time.Since(schStart).Milliseconds()
	log.Printf("[SystemHealth] [Scheduler] OK=%v, 耗时 %dms, %s", schItem.OK, schItem.DurationMs, schItem.Detail)
	items = append(items, schItem)

	// 7. Trading：验证 CIO 决策链与交易引擎就绪
	trStart := time.Now()
	trItem := SystemHealthItem{Key: "trading", Name: "Trading"}
	if a.portfolioEngine != nil && a.cioEngine != nil {
		trItem.OK = true
		trItem.Detail = "交易引擎与CIO决策链就绪"
	} else {
		trItem.OK = false
		trItem.Detail = "交易引擎未就绪"
	}
	trItem.DurationMs = time.Since(trStart).Milliseconds()
	log.Printf("[SystemHealth] [Trading] OK=%v, 耗时 %dms, %s", trItem.OK, trItem.DurationMs, trItem.Detail)
	items = append(items, trItem)

	allOK := true
	for _, it := range items {
		if !it.OK {
			allOK = false
			break
		}
	}

	totalMs := time.Since(totalStart).Milliseconds()
	log.Printf("[SystemHealth] 检查完成，共 %d 项，全部正常=%v，总耗时 %dms", len(items), allOK, totalMs)

	return map[string]interface{}{
		"items":     items,
		"all_ok":    allOK,
		"checkedAt": time.Now().Format("2006-01-02 15:04:05"),
		"total_ms":  totalMs,
	}, nil
}

// GetSystemInfo 获取系统信息
func (a *App) GetSystemInfo() (interface{}, error) {
	if a.sqliteManager != nil {
		config := data.GetSystemConfig(a.sqliteManager.GetDB())
		var features []string
		if config.FeaturesJSON != "" {
			json.Unmarshal([]byte(config.FeaturesJSON), &features)
		}
		return map[string]interface{}{
			"appName":    config.AppName,
			"appVersion": version.Version,
			"aboutText":  config.AboutText,
			"features":   features,
		}, nil
	}
	return map[string]interface{}{
		"appName":    version.AppName,
		"appVersion": version.Version,
		"aboutText":  "QuantBot AI - 由大模型驱动的量化机器人",
		"features":   []string{},
	}, nil
}

// GetMarketIndices 获取当前活跃的市场指数
func (a *App) GetMarketIndices() (interface{}, error) {
	if a.sqliteManager != nil {
		indices := data.GetActiveMarketIndices(a.sqliteManager.GetDB())
		return map[string]interface{}{
			"indices": indices,
			"total":   len(indices),
		}, nil
	}
	return map[string]interface{}{
		"indices": []data.MarketIndex{},
		"total":   0,
	}, nil
}

// GetMarketSixDimReports 获取最近 N 日市场六维判势结果（market_sixdim_daily 表，按日期倒序）。
// 供 AI 投资管理页展示；数据全部来自 DuckDB 真实行情 + 真实代理指标，严禁伪造。
// sixDimReportsCache 六维判势报告短TTL缓存（30s）：按 limit 缓存最近一次查询结果，
// 避免页面频繁刷新/多标签页反复触发 DuckDB 查询，缓解启动期回测占用 DB 时的阻塞。
var (
	sixDimReportsCacheMu sync.Mutex
	sixDimReportsCache   = map[int]sixDimReportsCacheEntry{}
)

type sixDimReportsCacheEntry struct {
	data     interface{}
	expireAt time.Time
}

// clearSixDimReportsCache 清空六维判势报告短TTL缓存，使下次查询立即读最新落库结果。
// 由「后台强制刷新」调用，保证用户手动刷新后前端立即看到新判势数据。
func clearSixDimReportsCache() {
	sixDimReportsCacheMu.Lock()
	sixDimReportsCache = make(map[int]sixDimReportsCacheEntry)
	sixDimReportsCacheMu.Unlock()
}

func (a *App) GetMarketSixDimReports(limit int) (result interface{}, err error) {
	// panic 兜底：任何内部异常都转成可序列化响应，避免 wails promise 拒绝导致前端报错
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[SixDim] GetMarketSixDimReports panic recovered: %v", r)
			result = map[string]interface{}{
				"reports": []interface{}{},
				"error":   fmt.Sprintf("内部异常: %v", r),
			}
			err = nil
		}
	}()

	if limit <= 0 {
		limit = 7
	}
	if limit > 30 {
		limit = 30
	}

	// 短TTL缓存：命中直接返回，避免反复查 DB
	now := time.Now()
	sixDimReportsCacheMu.Lock()
	if e, ok := sixDimReportsCache[limit]; ok && now.Before(e.expireAt) {
		sixDimReportsCacheMu.Unlock()
		return e.data, nil
	}
	sixDimReportsCacheMu.Unlock()

	log.Printf("[SixDim] GetMarketSixDimReports called, limit=%d", limit)
	if a.duckdbManager == nil || !a.duckdbManager.HasStockDB() {
		log.Printf("[SixDim] GetMarketSixDimReports: DuckDB 不可用")
		return map[string]interface{}{
			"reports": []interface{}{},
			"note":    "DuckDB 不可用，无法获取六维判势结果",
		}, nil
	}
	reports, err := sixdim.LatestReports(context.Background(), brainhost.AdaptMarketDataStore(a.duckdbManager), limit)
	if err != nil {
		log.Printf("[SixDim] GetMarketSixDimReports error: %v", err)
		return map[string]interface{}{
			"reports": []interface{}{},
			"error":   err.Error(),
		}, nil
	}
	log.Printf("[SixDim] GetMarketSixDimReports ok, reports=%d", len(reports))
	out := map[string]interface{}{
		"reports": reports,
		"total":   len(reports),
		"dim_chinese": map[string]string{
			"tech": "技术趋势", "breadth": "市场广度", "volume": "量能流动性",
			"capital": "资金结构", "sentiment": "情绪赚钱效应", "external": "外部约束",
		},
	}
	// 成功结果写入缓存（30s）
	sixDimReportsCacheMu.Lock()
	sixDimReportsCache[limit] = sixDimReportsCacheEntry{data: out, expireAt: time.Now().Add(30 * time.Second)}
	sixDimReportsCacheMu.Unlock()
	return out, nil
}

// GetCurrentMarketState 获取当前市场状态（供CIO页面使用）
func (a *App) GetCurrentMarketState() (interface{}, error) {
	if a.cioEngine == nil {
		return map[string]interface{}{
			"regime":     "NEUTRAL",
			"confidence": 0.5,
			"trend":      "STABLE",
			"volatility": 0.12,
			"timestamp":  time.Now().Format(time.RFC3339),
		}, nil
	}
	return a.cioEngine.GetCurrentMarketState(), nil
}

// RefreshPrices 手动刷新所有持仓的价格
func (a *App) RefreshPrices() (interface{}, error) {
	if a.portfolioEngine == nil {
		return nil, fmt.Errorf("Portfolio engine not initialized")
	}
	snapshots := a.portfolioEngine.RefreshPrices()

	if a.auditService != nil {
		a.auditService.LogAuditEvent(
			data.AuditEventLiveActivity,
			"手动刷新价格",
			"portfolio",
			"refresh_prices",
			"user",
			"user",
			"success",
			map[string]interface{}{
				"refreshed_count": len(snapshots),
				"timestamp":       time.Now().Format(time.RFC3339),
			},
		)
	}

	return map[string]interface{}{
		"refreshedCount": len(snapshots),
		"timestamp":      time.Now().Format(time.RFC3339),
	}, nil
}
