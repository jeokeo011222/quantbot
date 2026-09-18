package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/quantpilot/quantpilot/internal/agentworkflow"
	"github.com/quantpilot/quantpilot/internal/audit"
	"github.com/quantpilot/quantpilot/internal/backtest"
	brainhost "github.com/quantpilot/quantpilot/internal/brainhost"
	"github.com/quantpilot/quantpilot/internal/broker"
	"github.com/quantpilot/quantpilot/internal/cio"
	"github.com/quantpilot/quantpilot/internal/config"
	"github.com/quantpilot/quantpilot/internal/data"
	"github.com/quantpilot/quantpilot/internal/harness"
	"github.com/quantpilot/quantpilot/internal/llmhost"
	"github.com/quantpilot/quantpilot/internal/llmstore"
	"github.com/quantpilot/quantpilot/internal/mcp"
	"github.com/quantpilot/quantpilot/internal/orchestrator"
	"github.com/quantpilot/quantpilot/internal/plannerhost"
	"github.com/quantpilot/quantpilot/internal/policy"
	"github.com/quantpilot/quantpilot/internal/port"
	"github.com/quantpilot/quantpilot/internal/portfolio"
	"github.com/quantpilot/quantpilot/internal/pricing"
	"github.com/quantpilot/quantpilot/internal/riskcenter"
	"github.com/quantpilot/quantpilot/internal/scheduler"
	"github.com/quantpilot/quantpilot/internal/screener"
	"github.com/quantpilot/quantpilot/internal/sentiment"
	"github.com/quantpilot/quantpilot/internal/strategy"
	"github.com/quantpilot/quantpilot/internal/thssdk"
	"github.com/quantpilot/quantpilot/internal/tools"
	"github.com/quantpilot/quantpilot/internal/toolworker"
	"github.com/quantpilot/quantpilot/internal/tradeapproval"
	"github.com/quantpilot/quantpilot/internal/transparency"
	"github.com/quantpilot/quantpilot/internal/updater"
	"github.com/quantpilot/quantpilot/internal/util"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// 全局日志路径
var currentLogPath string

// SetLogPath 设置日志路径
func SetLogPath(path string) {
	currentLogPath = path
}

// GetLogPath 获取日志路径
func GetLogPath() string {
	return currentLogPath
}

// getLogPath 获取日志路径（方法别名）
func getLogPath() string {
	return currentLogPath
}

type App struct {
	ctx                context.Context
	configManager      *config.ConfigManager
	sqliteManager      *data.SQLiteManager
	duckdbManager      *data.DuckDBManager
	harnessApp         *harness.QuantHarness
	plannerAgent       *planner.Planner
	cioEngine          *cio.CIOEngine
	policyEngine       *policy.PolicyEngine
	screenerService    *screener.ScreenerService
	tradeablePool      *screener.TradeablePool
	fallbackTracker    *transparency.Tracker
	auditService       *audit.AuditService
	portfolioEngine    *portfolio.Engine
	autoScheduler      *scheduler.AutoScheduler
	taskScheduler      *scheduler.TaskScheduler
	taskExecutor       *scheduler.DefaultAgentExecutor
	agentTeam          map[port.AgentRole]*port.Agent
	strategyService    *strategy.Service
	backtestService    *backtest.Service
	agentTaskLogger    *data.AgentTaskLogger
	tdxManager         *tools.TDXServiceManager
	tdxInitOnce        sync.Once
	strategyRefreshMu  sync.Mutex
	orchestratorEngine *orchestrator.Orchestrator
	orchestratorAPI    *orchestrator.OrchestratorAPI
	agentWorkflowSys   *agentworkflow.AgentWorkflowSystem
	mcpServer          *mcp.Server            // LLM MCP 服务器
	mcpClient          *mcp.Client            // LLM MCP 客户端
	llmClient          llm.Client             // 当前使用的 LLM 客户端（MCP 或直接）
	tradeApproval      *tradeapproval.Service // 交易审批服务（模拟接口模式手动确认）
	llmStore           *llmstore.Store        // LLM 调用日志文件存储（log 文件夹）
	broker             broker.Broker          // 交易执行桥：模拟(SIMULATED) / QMT 实盘(LIVE)
	ready              bool
	readyMu            sync.RWMutex

	// 启动连通性真实探测结果（数据库/行情/AI 各自真连通才置 true）。
	// 由 startup 中 runStartupConnectivityCheck 执行一次真实测试后缓存，
	// IsReady 轮询读缓存，避免每一次 IsReady 都对 API 发起真实请求。
	connDbOK     bool
	connDbDetail string
	connDsOK     bool
	connDsDetail string
	connAIReady  bool
	connAIErr    string

	// 投资方案异步生成任务
	// 生成最终投资方案会触发全市场选股（数千只股票），耗时较长；改为后台 goroutine 执行，
	// 通过 GetGeneratePlanProgress 轮询进度，避免前端一直同步等待看似卡死。
	genPlanMu       sync.Mutex
	genPlanRunning  bool
	genPlanDone     bool
	genPlanErr      error
	genPlanProgress string
	genPlanResult   map[string]interface{}

	// 投资组合数据预热状态（启动页显示，独立于因子回测/选股）
	portfolioWarm    bool
	portfolioWarmErr string

	// 自动升级服务
	updateService *updater.UpdateService
}

func NewApp() *App {
	return &App{}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	startTime := time.Now()
	log.Println("[QuantBot] ========================================")
	log.Println("[QuantBot] Starting up...")
	log.Printf("[QuantBot] Start time: %s", startTime.Format("2006-01-02 15:04:05.000"))

	// 启动超时看门狗：如果 25 秒内未完成初始化，强制标记系统就绪
	go func() {
		timer := time.NewTimer(25 * time.Second)
		defer timer.Stop()
		<-timer.C
		a.readyMu.Lock()
		if !a.ready {
			a.ready = true
			a.readyMu.Unlock()
			log.Println("[QuantBot] STARTUP TIMEOUT: Forced system ready after 25s (degraded mode)")
		} else {
			a.readyMu.Unlock()
		}
	}()

	// 添加 panic 恢复机制
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[QuantBot] PANIC RECOVERED: %v", r)
			log.Printf("[QuantBot] Stack trace: %s", debug.Stack())
			elapsed := time.Since(startTime)
			log.Printf("[QuantBot] Startup failed after %v, forcing ready", elapsed)
			a.readyMu.Lock()
			a.ready = true
			a.readyMu.Unlock()
		}
	}()

	// 启动时自动最大化窗口以自适应屏幕
	runtime.WindowToggleMaximise(ctx)

	var err error

	// Step 1: Config
	stepStart := time.Now()
	log.Println("[QuantBot] [Step 1/10] Initializing config...")
	a.configManager, err = config.NewConfigManager()
	if err != nil {
		log.Printf("[QuantBot] Config init error: %v", err)
	} else {
		log.Printf("[QuantBot] Config initialized (%v)", time.Since(stepStart))
	}

	// Step 1.1: 同花顺官方数据源（thssdk）初始化：按配置注入包级默认客户端
	if a.configManager != nil {
		cfg := a.configManager.GetConfig()
		if cfg.THS.Enabled && cfg.THS.APIKey != "" {
			thssdk.Configure(cfg.THS.APIKey, cfg.THS.BaseURL)
			if thssdk.Enabled() {
				log.Printf("[QuantBot] 同花顺官方数据源已启用（fuyao.aicubes.cn）")
			}
		} else {
			thssdk.Configure("", "")
		}
	}

	// Step 1.2: 交易日历外挂配置（外挂 trading_calendar.json）。
	// 数据源改用同花顺官方近一年交易日序列刷新（无入参、固定窗口 [今日-1年, 今日]），
	// 替代原 timor.tech 逐年抓取休市日（该源返回 HTTP 403 已失效）。
	if a.configManager != nil {
		configDir := filepath.Dir(a.configManager.GetConfigPath())
		calPath := filepath.Join(configDir, "trading_calendar.json")
		util.SetTradingCalendarFile(calPath)
		if err := util.LoadTradingCalendar(calPath); err != nil {
			log.Printf("[QuantBot] 交易日历配置加载:%v (使用内建兜底)", err)
		}
		// 非阻塞后台刷新：用同花顺近一年交易日序列刷新休市集合（尽力而为，失败仅记日志）
		go func() {
			defer appRecover("交易日历后台刷新")
			added, syncErr := a.refreshTHSTradingCalendarDays()
			if syncErr != nil {
				log.Printf("[QuantBot] 交易日历刷新失败(同花顺): %v", syncErr)
				return
			}
			if added > 0 {
				log.Printf("[QuantBot] 交易日历联网刷新新增 %d 个休市日", added)
			} else {
				log.Printf("[QuantBot] 交易日历已核对，无需新增休市日")
			}
		}()
	}

	// 自动升级服务（基于当前配置初始化）
	if a.configManager != nil {
		cfg := a.configManager.GetConfig()
		a.updateService = updater.NewUpdateService(cfg, GetExecDir())
		log.Printf("[QuantBot] Update service ready (repo=%s/%s, auto_update=%v)",
			cfg.UpdateRepoOwner, cfg.UpdateRepoName, cfg.AutoUpdate)
	}

	// Step 2: SQLite
	stepStart = time.Now()
	log.Println("[QuantBot] [Step 2/10] Initializing SQLite...")
	a.sqliteManager, err = data.NewSQLiteManager()
	if err != nil {
		log.Printf("[QuantBot] SQLite init error: %v", err)
	} else {
		log.Printf("[QuantBot] SQLite initialized (%v)", time.Since(stepStart))
		pricing.SetCurrentTier(pricing.Unified)
		log.Printf("[QuantBot] Unified tier enabled, all features open")
	}

	// LLM 调用日志采用文件存储（log 文件夹），不写入数据库
	a.llmStore = llmstore.NewStore()
	if a.llmStore != nil {
		log.Printf("[QuantBot] LLM call log store initialized (file-based)")
	}

	// Step 3: DuckDB
	stepStart = time.Now()
	log.Println("[QuantBot] [Step 3/10] Initializing DuckDB...")
	a.duckdbManager, err = data.NewDuckDBManager()
	if err != nil {
		log.Printf("[QuantBot] DuckDB init error: %v", err)
	} else {
		log.Printf("[QuantBot] DuckDB initialized (%v)", time.Since(stepStart))
	}

	if a.duckdbManager != nil {
		duckdbOK := a.duckdbManager.HasStockDB()
		log.Printf("[QuantBot] DuckDB 状态: 数据库=%s, 可用=%v", a.duckdbManager.GetPath(), duckdbOK)
		if !duckdbOK {
			log.Printf("[QuantBot] ⚠ 行情数据库(stock.duckdb)不可用，选股功能不可用")
			log.Printf("[QuantBot]   请确认数据文件位于程序目录的 data 文件夹下")
		}
		if err := a.duckdbManager.EnsureMarketTables(); err != nil {
			log.Printf("[QuantBot] DuckDB tables warning: %v", err)
		}
		if err := a.duckdbManager.CreateAllViews(); err != nil {
			log.Printf("[QuantBot] DuckDB views warning: %v", err)
		}
	}

	// Step 4: TDX Service
	// 注意：TDX 初始化可能触发第三方库（quant1x/exchange）的日历越界 panic，
	// 必须用局部 recover 保护，避免中断整个启动流程导致组合引擎等组件未初始化
	stepStart = time.Now()
	log.Println("[QuantBot] [Step 4/10] Initializing TDX service...")
	if a.configManager != nil {
		cfg := a.configManager.GetConfig()
		tdxPath := cfg.TDXPath
		if tdxPath == "" {
			tdxPath = "D:\\tdx"
			log.Printf("[WARNING] TDX path not configured, using default: %s", tdxPath)
		}
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[QuantBot] TDX init panic recovered: %v (TDX 服务不可用，不影响其他组件)", r)
				}
			}()
			a.tdxManager = tools.NewTDXServiceManager(cfg.DataProvider, cfg.MCPURL, cfg.MCPAPIKey, tdxPath)
			log.Printf("[QuantBot] TDX Service Manager initialized (provider: %s, path: %s)", cfg.DataProvider, tdxPath)

			if a.duckdbManager != nil {
				a.tdxManager.SetDuckDBManager(a.duckdbManager)
			}

			// 实时行情数据源统一由 harness 通过 data.SetDataSource(tools.NewTDXMarketDataProvider(tdxManager))
			// 注册，App 层不再单独维护/设置全局 provider，避免双注册表逸路（callData统一走 data.GetDataSource()）。
			log.Printf("[QuantBot] TDX manager init done (%v)", time.Since(stepStart))
		}()
	}

	// Step 5: Harness (reuse existing managers to avoid duplicate connections)
	stepStart = time.Now()
	log.Println("[QuantBot] [Step 5/10] Initializing Harness (reusing DB connections)...")
	a.harnessApp, err = harness.NewQuantHarness(harness.QuantHarnessOptions{
		ConfigManager: a.configManager,
		SQLiteManager: a.sqliteManager,
		DuckDBManager: a.duckdbManager,
	})
	if err != nil {
		log.Printf("[QuantBot] Harness init error: %v", err)
	} else {
		log.Printf("[QuantBot] Harness initialized (%v)", time.Since(stepStart))
	}

	// Step 6: LLM Client & MCP
	stepStart = time.Now()
	log.Println("[QuantBot] [Step 6/10] Initializing LLM client and MCP server...")

	// 获取配置并创建 LLM 客户端
	var cfg config.AppConfig
	if a.configManager != nil {
		cfg = a.configManager.GetConfig()
	} else {
		log.Println("[QuantBot] ConfigManager is nil, trying to reinitialize")
		cm, cmErr := config.NewConfigManager()
		if cmErr == nil && cm != nil {
			cfg = cm.GetConfig()
			a.configManager = cm
			log.Println("[QuantBot] ConfigManager reinitialized successfully")
		} else {
			log.Printf("[QuantBot] ConfigManager reinit failed: %v, using defaults", cmErr)
			cfg.InitialCapital = 1000000
			cfg.AIModel = "deepseek-chat"
			cfg.AIBaseURL = "https://api.deepseek.com/v1"
		}
	}
	log.Printf("[QuantBot] Initializing LLM with APIKey length: %d, Model: %s, BaseURL: %s",
		len(cfg.AIAPIKey), cfg.AIModel, cfg.AIBaseURL)

	if cfg.AIAPIKey == "" {
		log.Printf("[QuantBot] WARNING: AI API Key is empty! LLM features will not work.")
		log.Printf("[QuantBot] Please configure your API Key in Settings before using AI features.")
		log.Printf("[QuantBot] Config file path: %s", a.configManager.GetConfigPath())
	} else {
		log.Printf("[QuantBot] AI API Key configured successfully (length: %d)", len(cfg.AIAPIKey))
	}

	// 启动 LLM MCP 服务器
	log.Printf("[QuantBot] Starting LLM MCP server on %s", cfg.MCPURL)
	log.Printf("[QuantBot] MCP config: ai_api_key_len=%d, ai_model=%s, ai_base_url=%s",
		len(cfg.AIAPIKey), cfg.AIModel, cfg.AIBaseURL)

	a.mcpServer = mcp.NewServer(&cfg)
	if err := a.mcpServer.Start(); err != nil {
		log.Printf("[QuantBot] MCP server start error: %v, falling back to direct LLM", err)
		a.mcpServer = nil
	} else {
		// 等待 MCP 服务器就绪
		if a.mcpServer.WaitReady(3 * time.Second) {
			log.Printf("[QuantBot] MCP server started successfully, isRunning=%v", a.mcpServer.IsRunning())

			// 测试 MCP 服务器健康状态
			healthClient := mcp.NewClient(cfg.MCPURL)
			apiKeyValid := healthClient.HasValidAPIKey()
			log.Printf("[QuantBot] MCP server health check: api_key_valid=%v", apiKeyValid)
		} else {
			log.Printf("[QuantBot] MCP server not ready, falling back to direct LLM")
			a.mcpServer.Stop()
			a.mcpServer = nil
		}
	}

	// 创建 LLM 客户端（通过 MCP 或直接）
	var llmClient llm.Client
	useMCP := a.mcpServer != nil && a.mcpServer.IsRunning()

	if useMCP {
		a.mcpClient = mcp.NewClient(cfg.MCPURL)
		llmClient = a.mcpClient
		log.Printf("[QuantBot] Using MCP-based LLM client (MCP URL: %s)", cfg.MCPURL)
	} else {
		llmClient = llm.NewClient(cfg.AIProvider, cfg.AIAPIKey, cfg.AIBaseURL, cfg.AIModel)
		log.Printf("[QuantBot] Using direct LLM client (MCP unavailable), provider=%q", cfg.AIProvider)
	}
	llmClient = a.wrapLLMClient(llmClient)
	a.llmClient = llmClient

	log.Printf("[QuantBot] LLM client type: %T, init time: %v", llmClient, time.Since(stepStart))

	// Step 7: Task Executor
	stepStart = time.Now()
	log.Println("[QuantBot] [Step 7/10] Initializing task executor...")
	a.taskExecutor = scheduler.NewDefaultAgentExecutor(llmClient)
	log.Printf("[QuantBot] Task executor created (%v)", time.Since(stepStart))

	// Step 8: Agents & Engines
	stepStart = time.Now()
	log.Println("[QuantBot] [Step 8/10] Initializing agents and engines...")
	if a.sqliteManager != nil {
		a.plannerAgent = planner.NewPlanner(brainhost.AdaptPlannerStore(a.sqliteManager), llmClient, 1)
		log.Println("[QuantBot] Planner Agent initialized")

		a.policyEngine = policy.NewPolicyEngine(a.sqliteManager)
		log.Println("[QuantBot] Policy Engine initialized")

		log.Printf("[QuantBot] Creating Portfolio Engine with initial capital: %.0f", cfg.InitialCapital)
		a.portfolioEngine, err = portfolio.NewEngine(a.sqliteManager, cfg.InitialCapital)
		if err != nil {
			log.Printf("[QuantBot] Portfolio Engine init FAILED: %v", err)
		} else {
			log.Printf("[QuantBot] Portfolio Engine initialized (initial capital: %.0f)", cfg.InitialCapital)
			// 接线最终执行层紧急停止门控：紧急停止（App/CIO 都会先置位 Policy 紧急停止标志）期间，
			// 即使绕过 CIO 编排/审批补确认等入口，portfolio.Buy/Sell 也直接拒绝，保证"紧急停止→一切成交停止"。
			a.portfolioEngine.SetEmergencyGate(func() bool {
				return a.policyEngine != nil && a.policyEngine.IsEmergencyStopped()
			})
			// 接线最终执行层"强制暂停建仓"门控：仅拦截买入，卖出不受影响。
			// 由 CIO "暂停建仓"指令置位 Policy 开关，确保即使绕过 CIO 编排/审批补确认等入口，
			// 买入成交也直接拒绝，实现"强制暂停建仓 → 操盘手/买入路径真正停下"。
			a.portfolioEngine.SetBuyGate(func() bool {
				return a.policyEngine != nil && a.policyEngine.IsPauseBuilding()
			})
			// 启动页展示投资组合数据预热（独立于因子回测/选股）
			a.startPortfolioWarmUp()
		}

		a.screenerService = screener.NewScreenerService(a.duckdbManager)
		a.tradeablePool = screener.NewTradeablePool(a.sqliteManager)
		// 将真实选股服务注入投资规划师，用于候选池生成（替代硬编码股票池）
		if a.plannerAgent != nil {
			a.plannerAgent.SetScreener(brainhost.AdaptStockScreener(a.screenerService))
			// 注入股票元信息提供者（名称/行业字典）
			a.plannerAgent.SetStockMeta(brainhost.AdaptStockMeta(data.GetDictLoader()))
			// 注入 LLM 审计元数据装饰（llmmonitoring）
			a.plannerAgent.SetCallMetaDecorator(brainhost.AdaptCallMetaDecoratorLlmtrace())
			// 方案持仓数与单支预算以组合实际可用资金为准（而非问卷画像资金），
			// 确保「画像资本 vs 组合真实本金」不一致时方案贴合真实资金规模。
			a.plannerAgent.SetCapitalProvider(func() float64 {
				if a.portfolioEngine == nil {
					return 0
				}
				return a.portfolioEngine.GetSnapshot().TotalCapital
			})
		}
		log.Println("[QuantBot] Screener Service and Tradeable Pool initialized")

		// 初始化交易审批服务（模拟接口模式手动确认）
		a.tradeApproval = tradeapproval.NewService()
		a.tradeApproval.SetEmitFn(func(eventName string, data interface{}) {
			if a.ctx != nil {
				runtime.EventsEmit(a.ctx, eventName, data)
			}
		})
		a.tradeApproval.SetEnabled(cfg.TradingMode == "simulated")
		// 超时未决交易补确认（批准）后执行器：以 sqlite 权威成交为准执行真实买卖，
		// 确保「用户点击确认的结果」真正驱动成交，而非在用户未及时点击时判定交易失败。
		a.tradeApproval.SetExecutor(func(decisionID, action, symbol, stockName, market string, quantity int, price float64, reason string) error {
			if a.portfolioEngine == nil {
				return fmt.Errorf("portfolio engine not available")
			}
			// 与正常执行路径一致：仅执行仍合法（存在、已批准、未过期）的决策
			// 注意：自动「建仓/盘中调仓」决策通过 Policy 校验后是直连 executeDecision 执行的，
			// 不会注册内存工作流，ValidateDecisionForExecution 会误判「决策不存在」。
			// 而补确认的订单只能由系统本身写入 s.pending（合法排队），用户显式确认即有权成交，
			// 故此处校验未通过仅记录日志、不阻断，最终风控交由 Buy/Sell（紧急停止/交易时段/涨跌停/资金）。
			if decisionID != "" && a.cioEngine != nil {
				if err := a.cioEngine.ValidateDecisionForExecution(decisionID); err != nil {
					log.Printf("[TradeApproval] 补确认决策校验未通过，继续执行(订单由系统排队): %s %s %d@%.2f: %v", action, symbol, quantity, price, err)
				}
			}
			// 实盘模式：订单真实下发 QMT，成交回报经 onBrokerFill 异步记入账本
			if a.broker != nil && a.broker.Mode() == broker.ModeLive {
				side := broker.SideSell
				if action == "BUY" {
					side = broker.SideBuy
				}
				if _, err := a.submitOrderLive(symbol, side, quantity, price, reason, decisionID); err != nil {
					log.Printf("[TradeApproval] QMT 实盘下单被拒: %s %s %d@%.2f: %v", action, symbol, quantity, price, err)
					return err
				}
				return nil
			}
			if action == "BUY" {
				_, err := a.portfolioEngine.Buy(symbol, stockName, market, quantity, price, reason, decisionID)
				if err != nil {
					log.Printf("[TradeApproval] 补确认买入被拒: %s %s %d@%.2f: %v", action, symbol, quantity, price, err)
				}
				return err
			}
			_, err := a.portfolioEngine.Sell(symbol, quantity, price, reason, decisionID)
			if err != nil {
				log.Printf("[TradeApproval] 补确认卖出被拒: %s %s %d@%.2f: %v", action, symbol, quantity, price, err)
			}
			return err
		})
		// 交易确认生命周期持久化：
		//  操盘手决定交易 → OnPending 写入临时表 orders(排队) + 买入预占用资金(ReserveBuy)
		//  用户确认成交 → 写入正式成交表 trades(由 Buy/Sell 落库)，OnResolve 更新 orders 为 filled
		//  取消/失败   → OnResolve 更新 orders 为 cancelled，并释放占用的买入资金
		//  如此「交易记录」仅展示正式成交(trades)记录，待确认/未成交订单不进入成交列表。
		a.tradeApproval.SetPersist(&tradeapproval.PendingPersist{
			OnPending: func(pt *tradeapproval.PendingTrade) error {
				if a.sqliteManager == nil {
					return fmt.Errorf("sqlite not available")
				}
				// 买入先占用资金（防止重复分配导致超买）；占用失败则订单创建失败
				if pt.Action == "BUY" && a.portfolioEngine != nil {
					if err := a.portfolioEngine.ReserveBuy(pt.Amount); err != nil {
						return err
					}
				}
				// 写入临时表 orders（status=pending，等待用户确认）
				now := time.Now()
				key := strings.ToLower(pt.DecisionID) + "|" + strings.ToLower(pt.Symbol) + "|" + pt.Action + "|" + strconv.Itoa(pt.Quantity)
				order := data.Order{
					OrderID:         pt.ID,
					PortfolioID:     a.portfolioEngine.GetPortfolioID(),
					InstrumentID:    pt.Symbol,
					OrderType:       "MARKET",
					Side:            pt.Action,
					Quantity:        pt.Quantity,
					Price:           pt.Price,
					Status:          "pending",
					FilledQuantity:  0,
					FilledPrice:     pt.Price,
					DecisionTraceID: pt.DecisionID,
					IdempotencyKey:  key,
					SubmittedAt:     &now,
				}
				if err := a.sqliteManager.GetDB().Create(&order).Error; err != nil {
					// 回滚已占用的买入资金
					if pt.Action == "BUY" && a.portfolioEngine != nil {
						a.portfolioEngine.ReleaseBuyReserve(pt.Amount)
					}
					return err
				}
				return nil
			},
			OnResolve: func(pt *tradeapproval.PendingTrade, executed bool) {
				if a.sqliteManager == nil {
					return
				}
				newStatus := "filled"
				filledQty := pt.Quantity
				var filledAt *time.Time
				if executed {
					t := time.Now()
					filledAt = &t
				} else {
					newStatus = "cancelled"
					filledQty = 0
				}
				a.sqliteManager.GetDB().Model(&data.Order{}).
					Where("order_id = ?", pt.ID).
					Updates(map[string]interface{}{
						"status":          newStatus,
						"filled_quantity": filledQty,
						"filled_price":    pt.Price,
						"filled_at":       filledAt,
					})
				// 成交的买入由 Buy 内部扣减现金并释放占用；仅未成交(取消/失败)的买入需在此释放
				if !executed && pt.Action == "BUY" && a.portfolioEngine != nil {
					a.portfolioEngine.ReleaseBuyReserve(pt.Amount)
				}
			},
		})
		log.Printf("[QuantBot] Trade approval service initialized (manual confirmation: %v)", cfg.TradingMode == "simulated")
		// 重启恢复：把昨日/当日仍在排队(pending)的待确认订单挂回，重新占用买入资金等待用户确认。
		// 仅在「交易日且未收盘」时恢复；否则这些订单已应判失败，直接置 cancelled 并释放占用。
		a.recoverPendingOrders()
		a.initBroker()

		a.cioEngine = cio.NewCIOEngine(a.sqliteManager, a.duckdbManager, llmClient, a.policyEngine, a.portfolioEngine, a.tradeablePool, a.appTracker())
		a.cioEngine.SetApprovalService(a.tradeApproval)
		a.cioEngine.SetPlanReviewer(a)
		// 注入实盘交易桥与盘中自动买卖实盘开关
		a.cioEngine.SetLiveBroker(a.broker)
		a.cioEngine.SetAutoExecutionEnabled(cfg.QMTApplyAutoExecution)
		log.Println("[QuantBot] CIO Engine initialized")

		profileProvider := func() *data.InvestorProfile {
			if a.plannerAgent != nil {
				profile, err := a.plannerAgent.GetProfile()
				if err == nil && profile != nil {
					return brainhost.DataProfileFromPort(profile)
				}
			}
			return nil
		}
		// 策略最新指标由 Quant 智能体每日盘后调优完成后自动写入（替代启动时刷新）
		// 因子复盘工具复用CIO共享的情绪引擎（带1h缓存），避免复盘阶段重复请求数据源
		var reviewSentiment *sentiment.Engine
		if a.cioEngine != nil {
			reviewSentiment = a.cioEngine.SentimentEngine()
		}
		// 注入两个增强工具依赖：真实资金流向(TDX) 与 当前投资方案(planner 数据库)
		fundFlowProvider := func(ctx context.Context, symbol string) (interface{}, error) {
			if a.harnessApp == nil {
				return nil, fmt.Errorf("TDX harness 未初始化")
			}
			exchange := data.DetectMarketFromCode(symbol)
			code := strings.Map(func(r rune) rune {
				if r >= '0' && r <= '9' {
					return r
				}
				return -1
			}, symbol)
			return a.harnessApp.GetFundFlow(exchange, code)
		}
		currentPlanProvider := func() (interface{}, error) {
			if a.plannerAgent == nil {
				return nil, fmt.Errorf("planner 未初始化")
			}
			plan, err := a.plannerAgent.GetCurrentPlan()
			if err != nil || plan == nil {
				return nil, fmt.Errorf("暂无投资方案")
			}
			sum := map[string]interface{}{
				"plan_id":           plan.PlanID,
				"name":              plan.Name,
				"objective":         plan.Objective,
				"risk_level":        plan.RiskLevel,
				"target_return":     plan.TargetReturn,
				"target_volatility": plan.TargetVolatility,
				"max_drawdown":      plan.MaxDrawdown,
				"status":            plan.Status,
				"version":           plan.Version,
				"updated_at":        plan.UpdatedAt.Format("2006-01-02"),
			}
			if m := a.plannerAgent.GetPlanMandate(plan); m != nil {
				sum["mandate"] = m
			}
			if pc := a.plannerAgent.GetPlanConstruction(plan); pc != nil {
				sum["construction"] = pc
			}
			return sum, nil
		}
		riskReportProvider := func() (interface{}, error) {
			returns, benchmark, weights := a.buildPortfolioRiskInputs()
			rep := riskcenter.ComputeReport(returns, benchmark, weights)
			rep.Source = "risk_agent"
			if err := riskcenter.SaveReport(a.sqliteManager, rep); err != nil {
				log.Printf("[RiskCenter] AI日终风险审查落库失败: %v", err)
			}
			return riskcenter.ToMap(rep), nil
		}
		a.agentTeam = brainhost.CreateDefaultTeam(a.sqliteManager, a.duckdbManager, a.screenerService, a.tradeablePool, profileProvider, a.portfolioEngine, a.cioEngine, a.tradeApproval, func() error {
			if a.strategyService == nil {
				return fmt.Errorf("strategyService 未初始化")
			}
			return a.strategyService.RefreshStrategyMetrics(a.duckdbManager)
		}, reviewSentiment, fundFlowProvider, currentPlanProvider, func() map[string]interface{} {
			if a.cioEngine != nil {
				return a.cioEngine.ClaimAccuracy()
			}
			return map[string]interface{}{}
		}, riskReportProvider, func() config.AppConfig {
			return a.configManager.GetConfig()
		})
		log.Printf("[QuantBot] Agent team created with %d agents", len(a.agentTeam))

		for role, agent := range a.agentTeam {
			if agent != nil {
				a.taskExecutor.RegisterAgent(role, agent)
			}
		}
		log.Printf("[QuantBot] Agent executor initialized: %v agents registered", len(a.agentTeam))

		diag := a.taskExecutor.Diagnose()
		log.Printf("[QuantBot] Executor diagnostics: %v", diag)
		log.Printf("[QuantBot] Executor available: %v", a.taskExecutor.IsAvailable())

		if a.portfolioEngine != nil {
			refreshMin := a.configManager.GetConfig().ActivityRefreshMinutes
			if refreshMin < 1 || refreshMin > 60 {
				refreshMin = 5
			}
			a.portfolioEngine.StartContinuousTrading(a.ctx, time.Duration(refreshMin)*time.Minute)
			log.Printf("[QuantBot] Continuous trading monitor started (%dmin refresh)", refreshMin)
		}
	} else {
		log.Println("[QuantBot] SQLite manager is nil, skipping agent initialization")
	}

	if a.screenerService == nil {
		a.screenerService = screener.NewScreenerService(a.duckdbManager)
	}
	log.Printf("[QuantBot] Agents and engines initialized (%v)", time.Since(stepStart))

	// Step 9: Services & Auditing
	stepStart = time.Now()
	log.Println("[QuantBot] [Step 9/10] Initializing services...")
	if a.sqliteManager != nil {
		a.auditService = audit.NewAuditService(a.sqliteManager)
		log.Println("[QuantBot] Audit Service initialized")

		a.auditService.LogAuditEvent(
			data.AuditEventLogin,
			"系统启动",
			"system",
			"startup",
			"system",
			"QuantBot",
			"success",
			map[string]interface{}{
				"version": "2.0",
				"tier":    string(pricing.GetCurrentTier()),
			},
		)

		a.strategyService = strategy.NewService(a.sqliteManager)
		if err := a.strategyService.EnsureDefaultStrategies(); err != nil {
			log.Printf("[QuantBot] Failed to ensure default strategies: %v", err)
		} else {
			strategies, _ := a.strategyService.GetStrategies("")
			log.Printf("[QuantBot] Strategy Service initialized, %d strategies available", len(strategies))
		} // 策略指标由 Quant 智能体每日盘后调优完成后自动写入；如需立即校验可在策略页「刷新」按钮手动触发真实回测刷新。
		a.backtestService = backtest.NewService(a.sqliteManager)
		if a.duckdbManager != nil {
			a.backtestService.SetDuckDBManager(a.duckdbManager)
		}
		log.Println("[QuantBot] Backtest Service initialized")

		a.agentTaskLogger = data.NewAgentTaskLogger(a.sqliteManager)
		log.Println("[QuantBot] Agent Task Logger initialized")
	}

	if a.configManager != nil {
		cfg := a.configManager.GetConfig()
		if cfg.PricingTier != "" {
			pricing.SetCurrentTier(pricing.Tier(cfg.PricingTier))
			log.Printf("[QuantBot] Loaded tier from config: %s", cfg.PricingTier)
		}
	}
	log.Printf("[QuantBot] Services initialized, current tier: %s", pricing.GetCurrentTier())
	log.Printf("[QuantBot] Services init time: %v", time.Since(stepStart))

	// Step 10: Schedulers & Orchestration
	stepStart = time.Now()
	log.Println("[QuantBot] [Step 10/10] Initializing schedulers and orchestration...")

	if a.harnessApp != nil && a.auditService != nil {
		a.autoScheduler = scheduler.NewAutoScheduler(a.harnessApp, a.auditService)
		if a.cioEngine != nil {
			a.autoScheduler.SetTradeablePoolProcessor(a)
			a.autoScheduler.SetCIOIntradayPlanProcessor(a)
			a.autoScheduler.SetDailyReviewProcessor(a)
			a.autoScheduler.SetDailySettlementProcessor(a)
			a.autoScheduler.SetOrderBookDailyProcessor(a)
			a.autoScheduler.SetPlanRefreshProcessor(a)
			log.Println("[QuantBot] Tradeable Pool Processor, CIO Intraday Plan Processor, Daily Review Processor, Daily Settlement Processor, OrderBook Daily Processor and Plan Refresh Processor set to Auto Scheduler")
		}
		a.autoScheduler.Start()
		log.Println("[QuantBot] Auto Scheduler initialized and started")
	}

	a.orchestratorEngine = orchestrator.NewOrchestrator(a.sqliteManager)
	if a.agentTeam != nil {
		a.orchestratorEngine.RegisterDefaultHandlers(a.agentTeam)
	} else {
		log.Println("[QuantBot] WARNING: Agent team not available, using empty handlers")
		a.orchestratorEngine.RegisterDefaultHandlers(map[port.AgentRole]*port.Agent{})
	}
	// 投资管理页"生成投资方案" → 投资规划师（Planner）编排任务：覆盖 planner handler，
	// 仅在"投资方案规划"步骤调用方案生成器，其余 planner 步骤（如盘前市场分析）委托默认 handler。
	plannerFallback := orchestrator.NewPlannerHandler(a.orchestratorEngine, a.agentTeam[port.RolePlanner])
	a.orchestratorEngine.RegisterHandler(orchestrator.NewInvestmentPlanHandler(
		a.orchestratorEngine,
		plannerFallback,
		func(ctx context.Context, task *orchestrator.WorkflowTask, step *orchestrator.TaskStep) (interface{}, error) {
			result, err := a.runGeneratePlan()
			a.genPlanMu.Lock()
			if err != nil {
				a.genPlanErr = err
			} else if r, ok := result.(map[string]interface{}); ok {
				a.genPlanResult = r
			}
			a.genPlanDone = true
			a.genPlanRunning = false
			a.genPlanMu.Unlock()
			return result, err
		},
	))
	a.orchestratorAPI = orchestrator.NewOrchestratorAPI(a.orchestratorEngine)
	a.orchestratorEngine.Start()
	log.Println("[QuantBot] Task Orchestrator initialized and started")

	a.taskScheduler = scheduler.NewTaskScheduler(a.sqliteManager)

	// 应用实时活动刷新间隔配置（盘中监控任务重复间隔）
	refreshMin := a.configManager.GetConfig().ActivityRefreshMinutes
	if refreshMin < 1 || refreshMin > 60 {
		refreshMin = 5
	}
	a.taskScheduler.SetInMarketRepeatInterval(refreshMin * 60)
	log.Printf("[QuantBot] Task scheduler in-market repeat interval set to %dmin", refreshMin)

	// 生产场景交互：为任务注入真实执行上下文（阶段/时间/组合摘要），避免出现 Context: <nil>
	a.taskScheduler.SetContextProvider(func(task scheduler.AgentTask, phase scheduler.TaskPhase) string {
		phaseLabel := scheduler.PhaseLabel(phase)
		now := time.Now().Format("2006-01-02 15:04:05")
		var base string
		if a.portfolioEngine == nil {
			base = fmt.Sprintf("任务阶段=%s | 时间=%s | 组合状态=未初始化", phaseLabel, now)
		} else {
			snap := a.portfolioEngine.GetSnapshot()
			if snap == nil {
				base = fmt.Sprintf("任务阶段=%s | 时间=%s | 组合状态=暂无数据", phaseLabel, now)
			} else {
				base = fmt.Sprintf("任务阶段=%s | 时间=%s | 组合状态: 总资产=%.2f | 现金=%.2f | 持仓数=%d",
					phaseLabel, now, snap.TotalMarketValue+snap.Cash, snap.Cash, len(snap.Positions))
			}
		}
		// 盘前决策链注入最近一次晚间复盘/明日计划要点，使昨日复盘真实参与今日决策
		// （复盘报告由 CIO GenerateDailyReview 每日盘后确定性落库 DailyReview 表）
		if phase == scheduler.PhasePreMarket {
			if ref := a.reviewReferenceForToday(); ref != "" {
				base += "\n\n[昨日晚间复盘与明日建议参考]\n" + ref
			}
		}
		return base
	})

	if a.taskExecutor != nil && a.taskExecutor.IsAvailable() {
		a.taskScheduler.SetExecutor(a.taskExecutor)
		log.Println("[QuantBot] Task scheduler executor set successfully")
	} else {
		log.Println("[QuantBot] WARNING: Agent executor not available, tasks will be skipped")
	}

	a.taskScheduler.SetDailyWorkflowTrigger(func() {
		if a.orchestratorAPI != nil {
			result, err := a.orchestratorAPI.StartDailyWorkflow()
			if err != nil {
				log.Printf("[QuantBot] Auto daily workflow failed: %v", err)
			} else {
				log.Printf("[QuantBot] Auto daily workflow started: %v", result)
			}
		}
	})
	a.taskScheduler.Start()
	log.Println("[QuantBot] Task Scheduler initialized and started")

	if a.sqliteManager != nil {
		agentworkflow.MigrateAgentWorkflow(a.sqliteManager.GetDB())
		agentworkflow.InitDefaultAgentData(a.sqliteManager.GetDB())
		a.agentWorkflowSys = agentworkflow.NewAgentWorkflowSystem(a.sqliteManager.GetDB())
		log.Println("[QuantBot] Agent Workflow System initialized")
	}

	a.startDailySnapshotTimer(ctx)
	a.startPreMarketAutoStartTimer(ctx)
	a.startTHSDailyKSyncTimer(ctx)

	// 启动补拉：延迟等系统就绪后，检查上一交易日日K是否到位；缺失则自动补拉（同花顺优先，通达信补充）
	go func() {
		defer appRecover("启动补拉+新鲜度检查")
		time.Sleep(6 * time.Second)

		// 启动告警：同花顺官方数据源未配置（六维判势真实情绪 / 财务同步 / 全市场日K自动拉取将不可用）
		if !thssdk.Enabled() {
			msg := "同花顺官方金融数据服务未配置：六维判势真实情绪/财务同步/全市场日K自动拉取将不可用，手动行情不受影响。请在「设置-数据源-辅助数据源」启用并填写 API Key。"
			log.Printf("[QuantBot] [警示] %s", msg)
			runtime.EventsEmit(ctx, "ths:warning", map[string]interface{}{
				"message":   msg,
				"timestamp": time.Now().Format(time.RFC3339),
			})
		}

		time.Sleep(2 * time.Second)
		a.syncDailyKlineAuto(ctx, "启动补拉", false)

		// 启动数据新鲜度检查：行情未更新到上一交易日则告警，并引导接入同花顺自动补拉
		a.warnIfDailyKStale(ctx)
	}()

	// 启动连通性真实探测：数据库(SQLite) / 行情(DuckDB) / AI(API 5秒真实连通)。
	// 保证启动页面判断「是否放行」基于真实连通结果，而非仅凭初始化不报错的标志。
	a.runStartupConnectivityCheck()

	// 标记系统就绪
	a.readyMu.Lock()
	a.ready = true
	a.readyMu.Unlock()

	// 初始化系统托盘（隐藏窗口创建于主线程，复用 Wails 主消息泵；失败不影响使用）
	a.initSystemTray()

	totalElapsed := time.Since(startTime)
	log.Printf("[QuantBot] ========================================")
	log.Printf("[QuantBot] System ready! Total startup time: %v", totalElapsed)
	log.Printf("[QuantBot] ========================================")
	runtime.LogInfo(ctx, "System ready!")

	currentPhase := util.GetMarketPhase(time.Now())
	log.Printf("[QuantBot] Current market phase: %s", util.PhaseLabels[currentPhase])

	// 启动后台定时检查更新（固定周期自动检查 GitHub Releases）
	if a.configManager != nil && a.updateService != nil {
		go a.startPeriodicUpdateCheck(ctx)
	}
}

// updateCheckInterval 定时检查间隔（固定 24h，无需用户配置）
const updateCheckInterval = 24 * time.Hour

// startPeriodicUpdateCheck 后台定时检查更新：启动错峰检查一次，之后每 24h 循环一次。
// auto_update 开关实时读取，设置页修改后无需重启即可生效。
func (a *App) startPeriodicUpdateCheck(ctx context.Context) {
	defer appRecover("周期更新检查")
	time.Sleep(8 * time.Second) // 错开初始化高峰期
	ticker := time.NewTicker(30 * time.Minute)
	defer ticker.Stop()

	// 首次检查
	if a.configManager.GetConfig().AutoUpdate {
		a.silentCheckForUpdate(ctx)
	}

	lastCheck := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !a.configManager.GetConfig().AutoUpdate || time.Since(lastCheck) < updateCheckInterval {
				continue
			}
			a.silentCheckForUpdate(ctx)
			lastCheck = time.Now()
		}
	}
}

// silentCheckForUpdate 静默检查更新：发现新版本时推送 update:available 事件
func (a *App) silentCheckForUpdate(ctx context.Context) {
	cctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	info, err := a.updateService.Check(cctx)
	if err != nil {
		log.Printf("[Updater] 启动静默检查失败: %v", err)
		return
	}
	if info.HasUpdate {
		log.Printf("[Updater] 发现新版本 %s（当前 %s）", info.LatestVersion, info.CurrentVersion)
		runtime.EventsEmit(ctx, "update:available", map[string]interface{}{
			"latest_version":  info.LatestVersion,
			"current_version": info.CurrentVersion,
			"changelog":       info.Changelog,
			"size":            info.Size,
		})
	} else {
		log.Printf("[Updater] 已是最新版本 %s", info.CurrentVersion)
	}
}

// CheckForUpdate 检查是否有新版本（前端手动触发）
func (a *App) CheckForUpdate() (map[string]interface{}, error) {
	if a.updateService == nil {
		return nil, fmt.Errorf("更新服务未初始化")
	}
	ctx, cancel := a.updateCtx(40 * time.Second)
	defer cancel()
	info, err := a.updateService.Check(ctx)
	if err != nil {
		return map[string]interface{}{"error": err.Error()}, err
	}
	return map[string]interface{}{
		"has_update":      info.HasUpdate,
		"current_version": info.CurrentVersion,
		"latest_version":  info.LatestVersion,
		"changelog":       info.Changelog,
		"size":            info.Size,
		"published_at":    info.PublishedAt,
	}, nil
}

// DownloadUpdate 下载并校验升级包（进度经 update:progress 事件推送）
func (a *App) DownloadUpdate() error {
	if a.updateService == nil {
		return fmt.Errorf("更新服务未初始化")
	}
	a.updateService.SetProgress(func(downloaded, total int64) {
		if a.ctx != nil {
			runtime.EventsEmit(a.ctx, "update:progress", map[string]interface{}{
				"downloaded": downloaded,
				"total":      total,
				"percent":    updateProgressPercent(downloaded, total),
			})
		}
	})
	ctx, cancel := a.updateCtx(30 * time.Minute)
	defer cancel()
	return a.updateService.Download(ctx)
}

// GetUpdateStatus 获取当前更新状态（供前端轮询）
func (a *App) GetUpdateStatus() (map[string]interface{}, error) {
	if a.updateService == nil {
		return map[string]interface{}{"state": "idle"}, nil
	}
	return a.updateService.Status(), nil
}

// ApplyUpdate 应用更新：写入计划并拉起 --apply-update 子进程，主程序随即退出
func (a *App) ApplyUpdate() error {
	if a.updateService == nil {
		return fmt.Errorf("更新服务未初始化")
	}
	if err := a.updateService.Apply(); err != nil {
		return err
	}
	// 主程序退出，由升级子进程完成文件替换并重启新版
	go func() {
		time.Sleep(800 * time.Millisecond)
		os.Exit(0)
	}()
	return nil
}

// updateProgressPercent 计算下载进度百分比
func updateProgressPercent(done, total int64) int {
	if total <= 0 {
		return 0
	}
	p := done * 100 / total
	if p > 100 {
		p = 100
	}
	return int(p)
}

// updateCtx 返回带超时的更新上下文（a.ctx 未初始化时回退到 background）
func (a *App) updateCtx(timeout time.Duration) (context.Context, context.CancelFunc) {
	base := a.ctx
	if base == nil {
		base = context.Background()
	}
	return context.WithTimeout(base, timeout)
}

// reviewReferenceForToday 生成最近一个已生成复盘日期的复盘要点文本，供今日盘前决策链参考。
// 复盘报告由 CIO GenerateDailyReview 每日盘后在 DailyReview 表确定性落库（review_date 唯一），
// 因此这里取“最近一个早于今日的复盘”作为昨日晚间复盘/明日建议，避免与今日复盘混淆。
func (a *App) reviewReferenceForToday() string {
	if a.sqliteManager == nil || a.sqliteManager.GetDB() == nil {
		return ""
	}
	today := time.Now().Format("2006-01-02")
	var review data.DailyReview
	if err := a.sqliteManager.GetDB().
		Where("review_date < ? AND status = ?", today, "COMPLETED").
		Order("review_date DESC").
		First(&review).Error; err != nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "复盘日期: %s\n", review.ReviewDate)
	if v := strings.TrimSpace(review.Summary); v != "" {
		fmt.Fprintf(&b, "复盘摘要: %s\n", v)
	}
	if v := strings.TrimSpace(review.Recommendations); v != "" {
		fmt.Fprintf(&b, "明日建议: %s\n", v)
	}
	if v := strings.TrimSpace(review.StrategyChangeSuggestion); v != "" {
		fmt.Fprintf(&b, "策略调整建议: %s\n", v)
	}
	s := b.String()
	if strings.TrimSpace(s) == "" {
		return ""
	}
	return s
}

// startPortfolioWarmUp 在后台预热投资组合数据（持仓行情/昨收/结算）
// 仅处理组合数据，独立于因子回测与选股引擎，不复用其链路。
// 网络或数据异常时记录错误并上报 IsReady，前端启动页展示红色报错（不降级）。
func (a *App) startPortfolioWarmUp() {
	if a.portfolioEngine == nil {
		return
	}
	go func() {
		defer appRecover("组合数据预热")
		err := a.portfolioEngine.WarmUp()
		a.readyMu.Lock()
		if err != nil {
			a.portfolioWarmErr = err.Error()
		} else {
			a.portfolioWarm = true
			a.portfolioWarmErr = ""
		}
		a.readyMu.Unlock()
		if err != nil {
			log.Printf("[QuantBot] Portfolio warm-up FAILED: %v", err)
		} else {
			log.Println("[QuantBot] Portfolio data warm-up completed")
		}
	}()
}

// IsReady 检查系统是否已就绪
func (a *App) IsReady() map[string]interface{} {
	a.readyMu.RLock()
	defer a.readyMu.RUnlock()

	portfolioReady := a.portfolioEngine != nil
	configReady := a.configManager != nil
	sqliteReady := a.sqliteManager != nil
	duckdbReady := a.duckdbManager != nil
	agentExecutorReady := a.taskExecutor != nil && a.taskExecutor.IsAvailable()

	// 检查 LLM 连通性
	llmReady := false
	llmError := ""
	if a.taskExecutor != nil {
		llmReady = a.taskExecutor.IsAvailable()

		// 获取详细诊断信息
		if !llmReady {
			if a.configManager != nil {
				cfg := a.configManager.GetConfig()
				if cfg.AIAPIKey == "" {
					llmError = "API Key 未配置"
				} else if cfg.AIBaseURL == "" {
					llmError = "API Base URL 未配置"
				} else if cfg.AIModel == "" {
					llmError = "模型名称未配置"
				}
			}

			if llmError == "" && a.taskExecutor != nil {
				diag := a.taskExecutor.Diagnose()
				if diag["llm_client_nil"] == true {
					llmError = "LLM 客户端未初始化"
				} else if agentCount, ok := diag["agent_count"].(int); ok && agentCount == 0 {
					llmError = "无智能体注册到执行器"
				}
			}

			if llmError == "" {
				llmError = "未知错误"
			}
		}
	} else {
		llmError = "LLM 执行器未初始化"
	}

	result := map[string]interface{}{
		"ready":                a.ready && configReady && sqliteReady && a.connDbOK && a.connDsOK && a.connAIReady,
		"portfolio_ready":      portfolioReady,
		"config_ready":         configReady,
		"sqlite_ready":         sqliteReady,
		"duckdb_ready":         duckdbReady,
		"conn_db_ok":           a.connDbOK,
		"conn_db_detail":       a.connDbDetail,
		"conn_ds_ok":           a.connDsOK,
		"conn_ds_detail":       a.connDsDetail,
		"conn_ai_ready":        a.connAIReady,
		"conn_ai_error":        a.connAIErr,
		"harness_ready":        a.harnessApp != nil,
		"cio_ready":            a.cioEngine != nil,
		"planner_ready":        a.plannerAgent != nil,
		"strategy_ready":       a.strategyService != nil,
		"backtest_ready":       a.backtestService != nil,
		"agent_executor_ready": agentExecutorReady,
		"llm_ready":            llmReady,
		"llm_error":            llmError,
		"portfolio_warm":       a.portfolioWarm,
		"portfolio_warm_error": a.portfolioWarmErr,
		"log_path":             getLogPath(),
	}

	// 收集错误
	var errors []string
	if !configReady {
		errors = append(errors, "配置管理器未初始化")
	}
	if !sqliteReady {
		errors = append(errors, "SQLite 数据库未初始化")
	}
	if !a.connDbOK {
		errors = append(errors, fmt.Sprintf("数据库连接失败: %s", a.connDbDetail))
	}
	if !a.connDsOK {
		errors = append(errors, fmt.Sprintf("行情数据库不可用: %s", a.connDsDetail))
	}
	if !a.connAIReady {
		errors = append(errors, fmt.Sprintf("AI 服务不可用: %s", a.connAIErr))
	}
	if !agentExecutorReady {
		errors = append(errors, fmt.Sprintf("LLM 智能体执行器未就绪: %s", llmError))
	}
	if !llmReady {
		errors = append(errors, fmt.Sprintf("LLM 模型连接失败: %s", llmError))
	}
	if a.portfolioWarmErr != "" {
		errors = append(errors, fmt.Sprintf("投资组合数据预热失败: %s", a.portfolioWarmErr))
	}
	result["errors"] = errors

	if a.portfolioEngine != nil {
		result["portfolio_cash"] = a.portfolioEngine.GetCash()
	} else {
		result["portfolio_cash"] = 0
	}

	return result
}

// runStartupConnectivityCheck 启动时执行一次真实的连通性探测：
//  1. 数据库：SQLite Ping（真实往返）
//  2. 行情：DuckDB 可查询到最新交易日与股票数据
//  3. AI：向配置的真实 API 发起一次 5 秒超时的极简请求
//
// 结果缓存到 App 字段，供 IsReady 轮询读取（避免每次 IsReady 都打 API）。
// 任一项失败不阻塞启动流程（保留降级入口），但 IsReady 会如实上报 ready=false。
func (a *App) runStartupConnectivityCheck() {
	a.readyMu.Lock()
	defer a.readyMu.Unlock()

	// 1) 数据库：SQLite 真实 Ping
	a.connDbOK = false
	a.connDbDetail = "SQLite 未初始化"
	if a.sqliteManager != nil && a.sqliteManager.GetDB() != nil {
		if sqlDB, err := a.sqliteManager.GetDB().DB(); err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := sqlDB.PingContext(ctx); err == nil {
				a.connDbOK = true
				a.connDbDetail = "SQLite 正常"
			} else {
				a.connDbDetail = fmt.Sprintf("SQLite Ping 失败: %v", err)
			}
		} else {
			a.connDbDetail = fmt.Sprintf("SQLite 连接获取失败: %v", err)
		}
	}

	// 2) 行情：DuckDB 是否挂载并可查询到真实数据（含数据新旧度：需更新到前一天交易日）
	a.connDsOK = false
	a.connDsDetail = "行情数据库未挂载，请确认 data/stock.duckdb 存在"
	if a.duckdbManager != nil && a.duckdbManager.HasStockDB() {
		guard := data.NewDataIntegrityGuard(a.duckdbManager)
		status := guard.GetMarketDataStatus(context.Background())
		if status.Available && status.StockCount > 0 {
			a.connDsOK = true
			a.connDsDetail = fmt.Sprintf("行情正常，最新交易日 %s，股票 %d 只", status.LatestDate, status.StockCount)
			// 数据新旧度：量化回测/选股依赖最新行情，要求更新到「今天的前一个交易日」
			if status.NeedsUpdate {
				a.connDsOK = true
				a.connDsDetail = fmt.Sprintf(
					"行情数据需更新：最新 %s，应更新到 %s（前一个交易日）。请在「数据更新」中同步行情数据",
					status.LatestDate, status.ExpectedDate,
				)
				log.Printf("[QuantBot] [启动] 行情数据需更新: 最新=%s 期望=%s，请同步行情数据", status.LatestDate, status.ExpectedDate)
			}
		} else {
			a.connDsDetail = status.Reason
			if a.connDsDetail == "" {
				a.connDsDetail = "行情数据库挂载但查询异常"
			}
		}
	}

	// 3) AI：真实 API 连通测试（5 秒超时）
	a.connAIReady = false
	a.connAIErr = "AI 客户端未初始化"
	if a.configManager != nil {
		cfg := a.configManager.GetConfig()
		if strings.TrimSpace(cfg.AIAPIKey) == "" {
			a.connAIErr = "API Key 未配置"
		} else if strings.TrimSpace(cfg.AIBaseURL) == "" {
			a.connAIErr = "API Base URL 未配置"
		} else if a.llmClient == nil {
			a.connAIErr = "LLM 客户端未初始化"
		} else {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := a.llmClient.Chat(ctx, []llm.Message{
				{Role: "user", Content: "ping"},
			}, nil); err != nil {
				a.connAIErr = fmt.Sprintf("AI 连通测试失败: %v", err)
			} else {
				a.connAIReady = true
				a.connAIErr = ""
			}
		}
	}

	log.Printf("[QuantBot] 启动连通性探测: 数据库=%v(%s) 行情=%v(%s) AI=%v(%s)",
		a.connDbOK, a.connDbDetail, a.connDsOK, a.connDsDetail, a.connAIReady, a.connAIErr)
}

// warnIfDailyKStale 启动时检查股票行情(DuckDB stock.ohlc)是否已更新到最新交易日。
// 未更新则推送 data:stale 告警：未接入同花顺时引导接入（自动补拉）；已接入但拉取失败时引导通达信手工补位。
func (a *App) warnIfDailyKStale(ctx context.Context) {
	if a.duckdbManager == nil || !a.duckdbManager.HasStockDB() {
		return
	}
	want := a.dailyTargetTradingDate(false)
	if a.dailyKlineCaughtUp(want) {
		return
	}
	var msg string
	if thssdk.Enabled() {
		msg = fmt.Sprintf("指标行情数据未更新到上一交易日 %s（同花顺自动补拉失败，可用通达信本地数据在「数据维护-通达信数据」手工补位）", want.Format("2006-01-02"))
	} else {
		msg = fmt.Sprintf("指标行情数据未更新到上一交易日 %s，建议接入同花顺官方数据源以自动补拉当日K线（设置→数据源→辅助数据源，填写 API Key 保存后立即生效）", want.Format("2006-01-02"))
	}
	log.Printf("[QuantBot] [警示] %s", msg)
	runtime.EventsEmit(ctx, "data:stale", map[string]interface{}{
		"message":   msg,
		"timestamp": time.Now().Format(time.RFC3339),
	})
}

// CheckLLMConnectivity 检查 LLM 连通性
func (a *App) CheckLLMConnectivity() (bool, error) {
	if a.taskExecutor == nil {
		return false, fmt.Errorf("LLM 执行器未初始化")
	}

	if !a.taskExecutor.IsAvailable() {
		// 尝试获取更详细的错误信息
		if a.configManager != nil {
			cfg := a.configManager.GetConfig()
			if cfg.AIAPIKey == "" {
				return false, fmt.Errorf("API Key 未配置！请在系统设置中配置 AI API Key")
			}
		}
		return false, fmt.Errorf("LLM 客户端或智能体未注册")
	}

	return true, nil
}

func (a *App) shutdown(ctx context.Context) {
	log.Println("[QuantBot] Shutting down...")

	// 移除系统托盘图标（幂等，进程随 runtime.Quit 走 Wails 生命周期退出）
	stopTray()

	// 取消所有待确认交易（避免阻塞智能体执行）
	if a.tradeApproval != nil {
		a.tradeApproval.CancelAll()
	}

	// 停止自动调度器
	if a.autoScheduler != nil {
		a.autoScheduler.Stop()
	}

	// 记录系统关闭审计
	if a.auditService != nil {
		a.auditService.LogAuditEvent(
			data.AuditEventLogout,
			"系统关闭",
			"system",
			"shutdown",
			"system",
			"QuantBot",
			"success",
			map[string]interface{}{
				"timestamp": time.Now().Format(time.RFC3339),
			},
		)

		// 强制刷新所有待写入的审计日志
		a.auditService.Flush()

		// 等待日志写入完成
		time.Sleep(500 * time.Millisecond)
	}

	if a.harnessApp != nil {
		a.harnessApp.Close()
	}
	if a.sqliteManager != nil {
		a.sqliteManager.Close()
	}
	if a.duckdbManager != nil {
		a.duckdbManager.Close()
	}
	log.Println("[QuantBot] Shutdown complete")
}

// RunDailyCycle 异步运行每日分析
func (a *App) RunDailyCycle() error {
	if a.harnessApp == nil {
		return fmt.Errorf("Harness not initialized")
	}

	// 检查持仓状态
	portfolioState := a.portfolioEngine.GetPortfolioState()
	positionsCount := 0
	if count, ok := portfolioState["positionsCount"].(int); ok {
		positionsCount = count
	}

	// 如果没有持仓，返回提示
	if positionsCount == 0 {
		runtime.EventsEmit(a.ctx, "daily:error", map[string]interface{}{
			"error":     "NO_POSITIONS",
			"message":   "当前没有持仓数据，AI分析需要持仓作为基础。请先通过投资规划或手动交易建立持仓。",
			"timestamp": time.Now().Format(time.RFC3339),
		})
		return nil
	}

	// 记录每日周期启动审计
	if a.auditService != nil {
		a.auditService.LogAuditEvent(
			data.AuditEventLiveActivity,
			"启动每日交易周期",
			"system",
			"daily_cycle",
			"system",
			"QuantBot",
			"success",
			map[string]interface{}{
				"positions_count": positionsCount,
				"timestamp":       time.Now().Format(time.RFC3339),
			},
		)
	}

	// 立即发送开始事件，通知前端显示加载状态
	runtime.EventsEmit(a.ctx, "daily:progress", map[string]interface{}{
		"phase":     "starting",
		"progress":  0,
		"message":   "正在启动分析引擎...",
		"timestamp": time.Now().Format(time.RFC3339),
	})

	// 在 goroutine 中执行，不阻塞 Wails 事件循环
	go func() {
		defer appRecover("每日分析异步任务")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		// 传递进度回调到 Harness
		progressCallback := func(phase string, progress float64, message string) {
			runtime.EventsEmit(a.ctx, "daily:progress", map[string]interface{}{
				"phase":     phase,
				"progress":  progress,
				"message":   message,
				"timestamp": time.Now().Format(time.RFC3339),
			})
		}

		log.Printf("[QuantBot] 开始每日分析（异步模式）")

		// 记录当日目标交易日，优先尝试经 agent.dll 的决策路径（真实工具/持久化/上下文/LLM
		// 经拉模式总线由宿主兑现）；DLL 不可用/失败/超时 → 回退到进程内 harness 直连。
		runDate := time.Now().Format("2006-01-02")
		result, usedDLL := a.tryRunDailyCycleViaDLL(runDate, progressCallback)
		var err error
		if !usedDLL {
			log.Printf("[QuantBot] DLL 决策路径不可用，回退到进程内 harness 直连")
			result, err = a.harnessApp.RunDailyCycleWithMode(ctx, progressCallback)
		} else {
			log.Printf("[QuantBot] 经 agent.dll 决策路径完成（Sessions=%d, Errors=%d）",
				len(result.AgentSessions), len(result.Errors))
		}

		if err != nil {
			log.Printf("[QuantBot] 每日分析错误: %v", err)
			runtime.EventsEmit(a.ctx, "daily:error", map[string]interface{}{
				"error":     err.Error(),
				"message":   "分析过程中出现错误",
				"timestamp": time.Now().Format(time.RFC3339),
			})
			runtime.EventsEmit(a.ctx, "daily:complete", map[string]interface{}{
				"success":   false,
				"error":     err.Error(),
				"timestamp": time.Now().Format(time.RFC3339),
			})

			// 记录失败审计
			if a.auditService != nil {
				a.auditService.LogAuditEvent(
					data.AuditEventLiveActivity,
					"每日交易周期失败",
					"system",
					"daily_cycle",
					"system",
					"QuantBot",
					"failed",
					map[string]interface{}{
						"error":     err.Error(),
						"timestamp": time.Now().Format(time.RFC3339),
					},
				)
			}
			return
		}

		// 成功完成
		resultJSON, _ := json.Marshal(result)
		log.Printf("[QuantBot] 每日分析完成，结果大小: %d bytes", len(resultJSON))

		// 每日收盘后自动记录持仓快照
		if a.portfolioEngine != nil {
			snapshotErr := a.portfolioEngine.RecordDailySnapshot()
			if snapshotErr != nil {
				log.Printf("[QuantBot] 每日持仓快照记录失败: %v", snapshotErr)
			} else {
				log.Printf("[QuantBot] 每日持仓快照记录成功")
			}
		}

		runtime.EventsEmit(a.ctx, "daily:complete", map[string]interface{}{
			"success":   true,
			"result":    result,
			"timestamp": time.Now().Format(time.RFC3339),
		})

		// 记录成功审计
		if a.auditService != nil {
			a.auditService.LogAuditEvent(
				data.AuditEventLiveActivity,
				"每日交易周期完成",
				"system",
				"daily_cycle",
				"system",
				"QuantBot",
				"success",
				map[string]interface{}{
					"result_size": len(resultJSON),
					"timestamp":   time.Now().Format(time.RFC3339),
				},
			)
		}
	}()

	return nil
}

// dllPath 返回 agent.dll 路径。可用环境变量 QUANTBOT_DLL_PATH 覆盖；默认 bin/agent.dll（相对工作目录）。
func (dllc *App) dllPath() string {
	if p := os.Getenv("QUANTBOT_DLL_PATH"); p != "" {
		return p
	}
	// 1) 优先取可执行文件同目录下的 agent.dll（随 exe 分发，与启动时工作目录无关）。
	//    用户直接运行 build\bin\QuantBot.exe 时，DLL 就放在 exe 旁边，确保能命中。
	if exe, err := os.Executable(); err == nil {
		cand := filepath.Join(filepath.Dir(exe), "agent.dll")
		if _, statErr := os.Stat(cand); statErr == nil {
			return cand
		}
	}
	// 2) 回退到仓库布局的 bin/agent.dll（工作目录位于项目根时，如 dllrunner / 单元测试）
	return filepath.Join("bin", "agent.dll")
}

// dailyCycleUseDLL 判断每日决策是否优先走 agent.dll 路径。
// QUANTBOT_USE_DLL 显式覆盖（"0"/"false"/"off"/"no" 禁用，其余开启）；
// 未设置时默认「存在 agent.dll 即启用，缺失即回退 harness」——贴近最终目标。
func (dllc *App) dailyCycleUseDLL() bool {
	if v := strings.ToLower(strings.TrimSpace(os.Getenv("QUANTBOT_USE_DLL"))); v != "" {
		switch v {
		case "0", "false", "off", "no":
			return false
		default:
			return true
		}
	}
	if _, err := os.Stat(dllc.dllPath()); err != nil {
		return false
	}
	return true
}

// tryRunDailyCycleViaDLL 尝试经 agent.dll 决策路径（绑定真实工具/持久化/上下文/LLM）。
// 返回 (result, usedDLL)；usedDLL=false 时调用方应回退 harness 直连。
// DLL 不可加载 / 未启用 / 跑失败 / 超时 / 未产出任何 Agent 会话 → usedDLL=false。
func (dllc *App) tryRunDailyCycleViaDLL(date string, progressCB harness.ProgressCallback) (*harness.DailyCycleResult, bool) {
	if !dllc.dailyCycleUseDLL() {
		return nil, false
	}

	agent, err := toolworker.Load(dllc.dllPath())
	if err != nil {
		log.Printf("[QuantBot] agent.dll 加载失败（回退 harness）: %v", err)
		return nil, false
	}
	defer agent.Close()

	// 绑定真实宿主实现：
	//   - 工具：复用 App 已装配的真实 agentTeam（每个 Agent.Tools 为真实 port.ToolExecutor）；
	//   - 持久化：真实 SQLite（brainhost.NewPersistenceAdapter）；
	//   - 上下文：复用 harness orchestrator 注入的真实系统上下文构建器；
	//   - LLM：由 agent.dll 决策脑自持（配置经 AgentInit 传入），宿主不再注入。
	host := &toolworker.Host{}
	var ctxProvider port.ContextProvider
	if dllc.harnessApp != nil && dllc.harnessApp.GetOrchestrator() != nil {
		ctxProvider = dllc.harnessApp.GetOrchestrator().ContextProvider
	}
	host.BindReal(dllc.flattenTeamTools(),
		brainhost.NewPersistenceAdapter(dllc.sqliteManager),
		ctxProvider)

	catalog := toolworker.BuildCatalog(dllc.teamToolsByRole())
	if progressCB != nil {
		progressCB("dll_init", 0.1, fmt.Sprintf("经 agent.dll 决策脑启动（%d 角色目录）", len(catalog)))
	}
	log.Printf("[QuantBot] 经 agent.dll 决策路径启动：catalog=%d 角色", len(catalog))

	// LLM：配置经 AgentInit 传入 DLL，由决策脑自建 llm.Client（不再经总线代理）。
	llmCfg := port.LLMConfig{
		Provider: dllc.configManager.GetConfig().AIProvider,
		APIKey:   dllc.configManager.GetConfig().AIAPIKey,
		BaseURL:  dllc.configManager.GetConfig().AIBaseURL,
		Model:    dllc.configManager.GetConfig().AIModel,
	}
	orchResult, counters, err := toolworker.RunAgentCycle(agent, host, catalog, date, llmCfg, 4*time.Minute)
	if err != nil {
		log.Printf("[QuantBot] agent.dll 决策路径失败（回退 harness）: %v", err)
		return nil, false
	}

	log.Printf("[QuantBot] agent.dll 决策完成，宿主兑现统计: %v", counters)
	if len(orchResult.Sessions) == 0 {
		// 决策脑未产出任何 Agent 会话（通常为 LLM 不可用）→ 回退 harness 保持原行为。
		log.Printf("[QuantBot] agent.dll 决策未产出 Agent 会话（可能 LLM 未配置），回退 harness")
		return nil, false
	}
	if progressCB != nil {
		progressCB("complete", 1.0, "agent.dll 决策完成")
	}
	return dllc.mapOrchestratorResult(orchResult), true
}

// mapOrchestratorResult 把 DLL 内 orchestrator 的结果映射为与 harness.runWithOrchestrator
// 同构的 DailyCycleResult，保持 UI 事件流（daily:complete 的 result）与回退路径一致。
func (dllc *App) mapOrchestratorResult(r *port.OrchestratorResult) *harness.DailyCycleResult {
	result := &harness.DailyCycleResult{
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

// flattenTeamTools 将 agentTeam 中所有 Agent 的真实工具按名称去重扁平化，
// 供 agent.dll 决策路径绑定（与 harness.flattenRealTools 同构）。
func (dllc *App) flattenTeamTools() []port.ToolExecutor {
	var out []port.ToolExecutor
	seen := map[string]bool{}
	for _, agent := range dllc.agentTeam {
		if agent == nil {
			continue
		}
		for _, t := range agent.Tools {
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

// teamToolsByRole 按角色（小写标签）组织 agentTeam 的真实工具，
// 供 agent.dll 决策路径构建工具目录（BuildCatalog，与 harness.toolsByRole 同构）。
func (dllc *App) teamToolsByRole() map[string][]port.ToolExecutor {
	out := make(map[string][]port.ToolExecutor)
	for role, agent := range dllc.agentTeam {
		if agent == nil {
			continue
		}
		var tools []port.ToolExecutor
		for _, t := range agent.Tools {
			if t != nil {
				tools = append(tools, t)
			}
		}
		if len(tools) > 0 {
			out[strings.ToLower(string(role))] = tools
		}
	}
	return out
}

func (a *App) WindowMinimise() {
	runtime.WindowMinimise(a.ctx)
}

func (a *App) WindowToggleMaximise() {
	runtime.WindowToggleMaximise(a.ctx)
}

func (a *App) WindowQuit() {
	// 通过 runtime.Quit 走 Wails 生命周期，确保 OnShutdown（app.shutdown）执行：
	// 交易审批清理、调度器停止、审计日志刷新、数据库关闭、后台任务取消。
	// 严禁直接 os.Exit(0)，否则会跳过上述清理。
	runtime.Quit(a.ctx)
}

func (a *App) WindowIsMaximised() bool {
	return runtime.WindowIsMaximised(a.ctx)
}

// HideToTray 隐藏主窗口到系统托盘（标题栏×）：进程与后台任务（调度/回测/监控）继续运行，
// 由系统托盘图标恢复。走原生 Win32 Shell_NotifyIcon，非退出。
func (a *App) HideToTray() {
	runtime.WindowHide(a.ctx)
}

// ShowMainWindow 从系统托盘恢复并聚焦主界面（托盘左键/菜单「显示主界面」）。
func (a *App) ShowMainWindow() {
	runtime.WindowShow(a.ctx)
	runtime.WindowUnminimise(a.ctx)
	// Wails v2.14 无 WindowSetFocus，用 JS 聚焦 + 置顶回弹，确保窗口恢复后获得焦点
	runtime.WindowSetAlwaysOnTop(a.ctx, true)
	runtime.WindowExecJS(a.ctx, "window.focus();")
	runtime.WindowSetAlwaysOnTop(a.ctx, false)
	runtime.WindowExecJS(a.ctx, "window.focus();")
}

// initSystemTray 启动系统托盘（独立锁定线程 + 专属消息泵，接收托盘左/右键回调）。
func (a *App) initSystemTray() {
	if err := runTrayAsync(a.ShowMainWindow, a.WindowQuit); err != nil {
		log.Printf("[QuantBot] 系统托盘初始化失败（不影响正常使用）: %v", err)
		return
	}
	log.Println("[QuantBot] 系统托盘已启用：点×隐藏到托盘，托盘图标可恢复/退出")
}

func getFloat(v interface{}) float64 {
	if f, ok := v.(float64); ok {
		return f
	}
	return 0
}

func getBool(v interface{}) bool {
	if b, ok := v.(bool); ok {
		return b
	}
	return false
}

func getString(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// round2p 保留2位小数（百分比等）
func round2p(v float64) float64 { return math.Round(v*100) / 100 }

// round4p 保留4位小数（净值等）
func round4p(v float64) float64 { return math.Round(v*10000) / 10000 }
