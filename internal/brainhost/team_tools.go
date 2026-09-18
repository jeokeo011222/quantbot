// Package brainhost 决策脑（internal/brain）的宿主装配侧：承接从决策脑迁移出的
// 一次性工具构造逻辑。它依赖宿主包（internal/data、internal/tools 等）并把构造出的
// 工具注入决策脑的抽象（port.ToolExecutor），从而保证决策脑本身不反向依赖宿主包。
package brainhost

import (
	"fmt"
	"log"

	"github.com/quantpilot/quantpilot/internal/marketsixdim"
	"github.com/quantpilot/quantpilot/internal/config"
	"github.com/quantpilot/quantpilot/internal/data"
	"github.com/quantpilot/quantpilot/internal/port"
	"github.com/quantpilot/quantpilot/internal/portfolio"
	"github.com/quantpilot/quantpilot/internal/screener"
	"github.com/quantpilot/quantpilot/internal/sentiment"
	toolpkg "github.com/quantpilot/quantpilot/internal/tools"
)

// ==================== Section 3: Agent Orchestration Methods ====================

// CreateDefaultTeam 创建默认的完整Agent团队
func CreateDefaultTeam(db *data.SQLiteManager, duckdbMgr *data.DuckDBManager, screenerService *screener.ScreenerService, tradeablePool *screener.TradeablePool, profileProvider func() *data.InvestorProfile, portfolioEngine *portfolio.Engine, decisionValidator toolpkg.DecisionValidator, approvalService toolpkg.TradeApprovalService, refreshMetrics func() error, sentimentEngine *sentiment.Engine, fundFlowProvider toolpkg.FundFlowProvider, currentPlanProvider toolpkg.CurrentPlanProvider, claimAccuracyProvider toolpkg.ClaimAccuracyProvider, riskReportProvider toolpkg.RiskReportProvider, getConfig func() config.AppConfig) map[port.AgentRole]*port.Agent {
	team := make(map[port.AgentRole]*port.Agent)

	roles := []port.AgentRole{port.RoleCIO, port.RolePlanner, port.RoleQuant, port.RoleRisk, port.RoleTrader}

	for _, role := range roles {
		agent := port.NewAgent(
			fmt.Sprintf("agent-%s", string(role)),
			role,
		)
		team[role] = agent
	}

	// 为每个Agent注册对应角色的工具
	RegisterTeamTools(team, duckdbMgr, db, screenerService, tradeablePool, profileProvider, portfolioEngine, decisionValidator, approvalService, refreshMetrics, sentimentEngine, fundFlowProvider, currentPlanProvider, claimAccuracyProvider, riskReportProvider, getConfig)

	log.Printf("[Team] Created default team with %d agents", len(team))
	return team
}

// RegisterTeamTools 为Agent团队注册工具
func RegisterTeamTools(team map[port.AgentRole]*port.Agent, duckdbMgr *data.DuckDBManager, sqliteMgr *data.SQLiteManager, screenerService *screener.ScreenerService, tradeablePool *screener.TradeablePool, profileProvider func() *data.InvestorProfile, portfolioEngine *portfolio.Engine, decisionValidator toolpkg.DecisionValidator, approvalService toolpkg.TradeApprovalService, refreshMetrics func() error, sentimentEngine *sentiment.Engine, fundFlowProvider toolpkg.FundFlowProvider, currentPlanProvider toolpkg.CurrentPlanProvider, claimAccuracyProvider toolpkg.ClaimAccuracyProvider, riskReportProvider toolpkg.RiskReportProvider, getConfig func() config.AppConfig) {
	if duckdbMgr == nil {
		log.Printf("[Team] DuckDB manager is nil, skipping tool registration")
		return
	}

	// 创建工具实例
	marketDataTool := toolpkg.NewMarketDataTool(duckdbMgr)
	searchMarketTool := toolpkg.NewSearchMarketTool(duckdbMgr)
	portfolioOptimizerTool := toolpkg.NewPortfolioOptimizerTool(duckdbMgr)
	marketStatsTool := toolpkg.NewGetMarketStatsTool(duckdbMgr)

	var backtestTool *toolpkg.BacktestTool
	if sqliteMgr != nil {
		backtestTool = toolpkg.NewBacktestTool(sqliteMgr, duckdbMgr)
	}

	// 创建选股引擎工具
	var screenMarketTool *toolpkg.ScreenMarketTool
	if screenerService != nil {
		screenMarketTool = toolpkg.NewScreenMarketTool(screenerService, tradeablePool, profileProvider)
	}

	// 创建选股工具（StockPoolEngine：全市场硬过滤+因子打分+行业分散+审计）
	var stockPoolTool *toolpkg.StockPoolTool
	if screenerService != nil {
		stockPoolTool = toolpkg.NewStockPoolTool(screenerService, tradeablePool)
	}

	// 创建股票池查询工具
	var getStockPoolTool *toolpkg.GetStockPoolTool
	if tradeablePool != nil {
		getStockPoolTool = toolpkg.NewGetStockPoolTool(tradeablePool)
	}

	// 创建投资组合管理工具
	var portfolioStateTool *toolpkg.PortfolioStateTool
	var getPositionsTool *toolpkg.GetPositionsTool
	var placeTradeTool *toolpkg.PlaceTradeTool
	if portfolioEngine != nil {
		portfolioStateTool = toolpkg.NewPortfolioStateTool(portfolioEngine)
		getPositionsTool = toolpkg.NewGetPositionsTool(portfolioEngine)
		placeTradeTool = toolpkg.NewPlaceTradeTool(portfolioEngine)
		// Trader只能执行合法DecisionObject（注入决策验证器）
		if decisionValidator != nil {
			placeTradeTool.SetDecisionValidator(decisionValidator)
		}
		// 模拟接口模式：交易需用户手动确认
		if approvalService != nil {
			placeTradeTool.SetApprovalService(approvalService)
		}
	}

	// 工具化：执行成本估算 / 宏观快照 / 组合风险 / 涨跌停-T+1可行性（工具负责计算事实）
	var orderCostTool *toolpkg.OrderCostTool
	var macroSnapshotTool *toolpkg.MacroSnapshotTool
	var portfolioRiskViewTool *toolpkg.PortfolioRiskViewTool
	var limitCheckTool *toolpkg.LimitCheckTool
	var portfolioRiskMetricsTool *toolpkg.PortfolioRiskMetricsTool
	var factorReviewTool *toolpkg.FactorReviewTool
	var dailySettlementTool *toolpkg.DailySettlementTool
	orderCostTool = toolpkg.NewOrderCostTool()
	if duckdbMgr != nil {
		macroSnapshotTool = toolpkg.NewMacroSnapshotTool(duckdbMgr)
		factorReviewTool = toolpkg.NewFactorReviewTool(duckdbMgr, sqliteMgr, tradeablePool, sentimentEngine)
	}
	if portfolioEngine != nil {
		portfolioRiskViewTool = toolpkg.NewPortfolioRiskViewTool(portfolioEngine)
		limitCheckTool = toolpkg.NewLimitCheckTool(portfolioEngine)
		portfolioRiskMetricsTool = toolpkg.NewPortfolioRiskMetricsTool(portfolioEngine, duckdbMgr, "sh000300")
		dailySettlementTool = toolpkg.NewDailySettlementTool(portfolioEngine)
	}

	// 日终组合风险报告：由 main 注入 provider，与风险中心页面共用 riskcenter 引擎并落库（RISK 日终审查主工具）
	var riskReportTool *toolpkg.RiskReportTool
	if riskReportProvider != nil {
		riskReportTool = toolpkg.NewRiskReportTool(riskReportProvider)
	}

	// A档工具：市场状态判定 / Mandate合规校验 / 市场情绪监控 / 止损监控 / 决策日志
	var marketRegimeTool *toolpkg.MarketRegimeDetectTool
	var mandateComplianceTool *toolpkg.MandateComplianceTool
	var sentimentMonitorTool *toolpkg.SentimentMonitorTool
	var stopLossMonitorTool *toolpkg.StopLossMonitorTool
	var decisionLogTool *toolpkg.DecisionLogTool
	var sixdimTool *toolpkg.SixDimTool
	var orderBookTool *toolpkg.OrderBookTool
	if duckdbMgr != nil {
		marketRegimeTool = toolpkg.NewMarketRegimeDetectTool(duckdbMgr, sqliteMgr)
		sentimentMonitorTool = toolpkg.NewSentimentMonitorTool(duckdbMgr)
		sixdimTool = toolpkg.NewSixDimToolWithConfigLoader(AdaptMarketDataStore(duckdbMgr), func() sixdim.SourceConfig {
			return toolpkg.SourceConfigFromAppConfig(getConfig().SixDimSource)
		})
		// 注入外部实时源（北向/两融/涨停池/隔夜外围/同花顺官方情绪面）及活跃实时行情源适配器。
		sixdimTool.SetSnapSource(AdaptSnapSource())
		sixdimTool.SetExternalSources(AdaptMarketExternalSources())
		sixdimTool.SetTHSSource(AdaptTHSSentimentSource())
	}
	orderBookTool = toolpkg.NewOrderBookTool()

	// 市场信息原子数据工具（真实公开数据源，来源已标注，无伪造）
	// 六维拆分的原子工具：技术/广度/量能/资金/涨停板/隔夜外围 + 快讯/公告/龙虎榜/基本面/交易规则
	var indexTechnicalTool *toolpkg.IndexTechnicalTool
	var marketBreadthTool *toolpkg.MarketBreadthTool
	var fundamentalTool *toolpkg.FundamentalTool
	if duckdbMgr != nil {
		indexTechnicalTool = toolpkg.NewIndexTechnicalTool(duckdbMgr)
		marketBreadthTool = toolpkg.NewMarketBreadthTool(duckdbMgr)
		fundamentalTool = toolpkg.NewFundamentalTool(duckdbMgr)
	}
	turnoverTool := toolpkg.NewTurnoverTool()
	capitalFlowTool := toolpkg.NewCapitalFlowTool()
	limitUpBoardTool := toolpkg.NewLimitUpBoardTool()
	externalMarketTool := toolpkg.NewExternalMarketTool()
	marketNewsTool := toolpkg.NewMarketNewsTool()
	stockAnnouncementTool := toolpkg.NewStockAnnouncementTool()
	lhbTool := toolpkg.NewLHBTool()
	marketRulesCheckTool := toolpkg.NewMarketRulesCheckTool()

	// 第二批增强工具（真实数据、确定性计算）
	structureRiskTool := toolpkg.NewStructureRiskTool(duckdbMgr)
	stockFundFlowTool := toolpkg.NewStockFundFlowTool(fundFlowProvider)
	investmentPlanTool := toolpkg.NewInvestmentPlanTool(currentPlanProvider)
	claimAccuracyTool := toolpkg.NewClaimAccuracyTool(claimAccuracyProvider)
	recordAlphaTool := toolpkg.NewRecordAlphaTool(sqliteMgr)
	getAlphaByRegimeTool := toolpkg.NewGetAlphaByRegimeTool(sqliteMgr)
	getAlphaTopTool := toolpkg.NewGetAlphaTopTool(sqliteMgr)
	var generalizationTool *toolpkg.GeneralizationTool
	var strategyRankTool *toolpkg.StrategyRankTool
	var sectorRotationTool *toolpkg.SectorRotationTool
	if duckdbMgr != nil {
		generalizationTool = toolpkg.NewGeneralizationTool(duckdbMgr)
		strategyRankTool = toolpkg.NewStrategyRankTool(duckdbMgr)
		sectorRotationTool = toolpkg.NewSectorRotationTool(duckdbMgr)
	}
	skillCatalogTool := toolpkg.NewSkillCatalogTool()
	var performanceNavTool *toolpkg.PerformanceNavTool
	if portfolioEngine != nil {
		performanceNavTool = toolpkg.NewPerformanceNavTool(portfolioEngine)
	}
	var factorHealthTool *toolpkg.FactorHealthTool
	if sqliteMgr != nil {
		factorHealthTool = toolpkg.NewFactorHealthTool(sqliteMgr)
	}

	// 交易日历工具：联网刷新休市日历 / 交易日检查（CIO收盘确认当日是否交易日）
	tradingCalendarTool := toolpkg.NewTradingCalendarTool()
	tradingDayCheckTool := toolpkg.NewTradingDayCheckTool()
	if portfolioEngine != nil {
		mandateComplianceTool = toolpkg.NewMandateComplianceTool(portfolioEngine)
		stopLossMonitorTool = toolpkg.NewStopLossMonitorTool(portfolioEngine)
	}
	if sqliteMgr != nil {
		decisionLogTool = toolpkg.NewDecisionLogTool(sqliteMgr)
	}

	// 自动化调参工具：Quant每日复盘对策略参数做网格调优（参数存于策略表，不写死）
	var strategyTunerTool *toolpkg.StrategyTunerTool
	if sqliteMgr != nil && duckdbMgr != nil {
		strategyTunerTool = toolpkg.NewStrategyTunerTool(sqliteMgr, duckdbMgr)
		if refreshMetrics != nil {
			strategyTunerTool.SetRefreshMetrics(refreshMetrics)
		}
	}

	// 次日交易策略选择工具：Quant盘后因子复盘后选定次日策略写入 daily_strategy_plans，
	// 操盘手次日按该策略信号执行卖出（如 KDJ 死叉减仓）→ 策略驱动减仓。
	var dailyStrategyTool *toolpkg.DailyStrategyTool
	if sqliteMgr != nil && duckdbMgr != nil {
		dailyStrategyTool = toolpkg.NewDailyStrategyTool(sqliteMgr, duckdbMgr)
	}

	// 精细化仓位管理工具：分批建仓+金字塔加仓+动态减仓/止盈止损+ATR波动率仓位
	// CIO 决策建仓/加仓/减仓时调用生成方案（参数存于 position_manager_configs 表，不写死）
	var positionManagerTool *toolpkg.PositionManagerTool
	if sqliteMgr != nil && duckdbMgr != nil {
		positionManagerTool = toolpkg.NewPositionManagerTool(sqliteMgr, duckdbMgr)
	}

	// 中央工具注册表：用于运行时权限强制校验（Runtime Enforcement）
	toolReg := port.NewToolRegistry()
	registerTeamTool := func(name, desc string, category port.ToolCategory, riskLevel string, roles []port.AgentRole, tool toolpkg.Tool) {
		if tool == nil {
			return
		}
		_ = toolReg.Register(&port.ToolMetadata{
			Name:        name,
			Description: desc,
			Category:    category,
			Roles:       roles,
			RiskLevel:   riskLevel,
			Version:     "1.0.0",
			Tool:        AdaptTool(tool),
		})
	}

	registerTeamTool("get_market_data", "获取历史市场数据（全球市场通用）", port.CategoryMarketData, "low", []port.AgentRole{port.RoleCIO, port.RoleQuant, port.RoleRisk, port.RoleTrader}, marketDataTool)
	registerTeamTool("search_market", "按条件搜索市场股票（全球市场通用）", port.CategoryStockSearch, "low", []port.AgentRole{port.RoleCIO, port.RoleQuant, port.RoleTrader}, searchMarketTool)
	registerTeamTool("optimize_portfolio", "基于风险收益权衡优化投资组合配置（CIO、投资规划师、Quant可使用）", port.CategoryPortfolio, "medium", []port.AgentRole{port.RoleCIO, port.RolePlanner, port.RoleQuant}, portfolioOptimizerTool)
	registerTeamTool("get_market_stats", "获取A股市场统计数据（涨跌家数、涨停跌停）", port.CategoryMarketStats, "low", []port.AgentRole{port.RoleCIO, port.RoleRisk, port.RoleQuant, port.RolePlanner, port.RoleTrader}, marketStatsTool)
	registerTeamTool("run_backtest", "运行策略回测，验证历史表现（仅QUANT可使用）", port.CategoryBacktest, "medium", []port.AgentRole{port.RoleQuant}, backtestTool)
	registerTeamTool("screen_market", "运行选股引擎筛选候选股票（CIO、PLANNER、QUANT可使用）", port.CategoryStockSearch, "medium", []port.AgentRole{port.RoleCIO, port.RolePlanner, port.RoleQuant}, screenMarketTool)
	registerTeamTool("build_stock_pool", "从全市场真实数据做确定性硬过滤+因子打分+行业分散+审计，返回候选股票池（Quant/CIO/Planner使用）", port.CategoryStockSearch, "medium", []port.AgentRole{port.RoleCIO, port.RolePlanner, port.RoleQuant}, stockPoolTool)
	registerTeamTool("get_stock_pool", "查询当前可交易股票池（CIO、RISK、TRADER可使用）", port.CategoryPortfolio, "low", []port.AgentRole{port.RoleCIO, port.RoleRisk, port.RoleTrader}, getStockPoolTool)
	registerTeamTool("get_portfolio_state", "查询投资组合整体状态（CIO、PLANNER、RISK、TRADER可使用）", port.CategoryPortfolio, "low", []port.AgentRole{port.RoleCIO, port.RolePlanner, port.RoleRisk, port.RoleTrader}, portfolioStateTool)
	registerTeamTool("get_positions", "查询当前持仓明细（CIO、QUANT、RISK、TRADER可使用）", port.CategoryPortfolio, "low", []port.AgentRole{port.RoleCIO, port.RoleQuant, port.RoleRisk, port.RoleTrader}, getPositionsTool)
	registerTeamTool("place_trade", "执行买入/卖出订单（仅TRADER可使用，且需合法DecisionObject）", port.CategoryExecution, "high", []port.AgentRole{port.RoleTrader}, placeTradeTool)
	registerTeamTool("order_cost_estimator", "按A股规则估算一笔买卖的佣金/印花税/总成本并校验一手可行性（Trader下单前成本评估）", port.CategoryExecution, "low", []port.AgentRole{port.RoleTrader, port.RoleQuant, port.RoleCIO}, orderCostTool)
	registerTeamTool("get_macro_snapshot", "获取A股当前宏观快照：市场统计(涨跌家数/涨停跌停)+交易时段（Planner/CIO盘前盘中把握环境）", port.CategoryMarketData, "low", []port.AgentRole{port.RolePlanner, port.RoleCIO, port.RoleQuant, port.RoleRisk}, macroSnapshotTool)
	registerTeamTool("portfolio_risk_view", "计算组合风险视图：集中度(HHI)/单票权重/TOP5/现金比例/收益回撤（CIO/Risk做组合层审查）", port.CategoryRisk, "medium", []port.AgentRole{port.RoleCIO, port.RoleRisk, port.RoleQuant}, portfolioRiskViewTool)
	registerTeamTool("limit_check", "按A股规则校验涨跌停价/T+1可卖量/买入一手可行性，拦截不合法订单（Trader下单前校验）", port.CategoryExecution, "medium", []port.AgentRole{port.RoleTrader, port.RoleCIO, port.RoleQuant, port.RoleRisk}, limitCheckTool)
	registerTeamTool("portfolio_risk_metrics", "基于真实组合每日收益序列计算 VaR95/VaR99/CVaR95/年化波动率/最大回撤/Beta(对沪深300)（Risk/CIO风险审查，勿自行估算）", port.CategoryRisk, "medium", []port.AgentRole{port.RoleRisk, port.RoleCIO, port.RoleQuant}, portfolioRiskMetricsTool)
	if riskReportTool != nil {
		registerTeamTool("run_daily_risk_report", "生成日终组合风险报告（与风险中心页面同一引擎并落库）：VaR/CVaR/波动率/回撤/Beta/集中度/相关性/流动性/综合评分与状态、压力测试与合规校验。RISK 日终风险审查主工具，勿自行估算", port.CategoryRisk, "medium", []port.AgentRole{port.RoleRisk, port.RoleCIO}, riskReportTool)
	}
	registerTeamTool("factor_review", "基于真实K线对因子做复盘评价：各因子 IC/RankIC、分组收益差、覆盖率、准确率、稳定性、衰减（Quant复盘因子表现，勿自行估算）", port.CategoryResearch, "low", []port.AgentRole{port.RoleQuant, port.RoleCIO}, factorReviewTool)
	registerTeamTool("daily_settlement", "执行当日组合结算：将当日总资产/累计盈亏/今日盈亏/持仓快照写入数据库并返回结算结果（Trader日终结算调用，勿由模型自行推算）", port.CategoryExecution, "medium", []port.AgentRole{port.RoleTrader, port.RoleRisk, port.RoleCIO}, dailySettlementTool)
	registerTeamTool("tune_strategy_params", "对策略参数做自动化网格调优：基于真实基准指数K线回测遍历参数组合，按夏普/收益/胜率选出更优参数写回策略表config_json并返回对比报告（Quant每日复盘调用，参数存于数据库不写死）", port.CategoryResearch, "medium", []port.AgentRole{port.RoleQuant}, strategyTunerTool)
	registerTeamTool("select_next_day_strategy", "为下一交易日选定交易策略并写入策略日计划(daily_strategy_plans)：按策略表真实回测指标综合分自动选优或显式指定strategy_type，操盘手次日按该策略信号执行卖出(如KDJ死叉)。Quant盘后因子复盘调用，数据来自真实回测严禁伪造", port.CategoryResearch, "medium", []port.AgentRole{port.RoleQuant, port.RoleCIO}, dailyStrategyTool)
	registerTeamTool("position_manager", "精细化仓位管理：基于真实ATR波动率仓位缩放，分批建仓+金字塔加仓、利润分档减仓/移动止盈/止损，并内置交易时间纪律(10:00前只观察/10:00-11:20建底仓/13:30-14:20优化做T/14:40后只止盈减仓，硬止损例外)。CIO建仓/加仓/减仓时调用action=plan，参数与时段规则存于数据库可set_config更新", port.CategoryPortfolio, "medium", []port.AgentRole{port.RoleCIO, port.RoleQuant}, positionManagerTool)

	registerTeamTool("market_regime_detect", "基于上证/深证/创业板指数5/10/20日收益、均线、量能确定性判定市场状态(牛市/熊市/震荡)并给出建议权益仓位(80/50/20%)（CIO决策前置工具）", port.CategoryMarketData, "low", []port.AgentRole{port.RoleCIO, port.RoleQuant, port.RoleRisk, port.RolePlanner}, marketRegimeTool)
	registerTeamTool("mandate_compliance_check", "基于组合实时持仓/现金与Mandate约束做确定性合规校验：单票权重/最低现金/行业集中度/持仓数量与资金规模分档（Planner核心职责）", port.CategoryRisk, "low", []port.AgentRole{port.RolePlanner, port.RoleCIO}, mandateComplianceTool)
	registerTeamTool("sentiment_monitor", "基于真实行情监控A股市场情绪：全市场涨跌家数、采样涨跌停数量、连板高度Top列表（Risk情绪面风险判断，勿自行推断）", port.CategoryMarketData, "low", []port.AgentRole{port.RoleRisk, port.RoleCIO}, sentimentMonitorTool)
	registerTeamTool("stop_loss_monitor", "基于组合真实持仓成本与现价列出浮亏达到阈值(默认-8%)的持仓，返回浮亏比例/金额（Risk止损预警，只算事实不执行交易）", port.CategoryRisk, "low", []port.AgentRole{port.RoleRisk, port.RoleCIO}, stopLossMonitorTool)
	registerTeamTool("decision_log", "查询数据库CIO决策轨迹(市场状态/目标仓位/风险门禁/最终决策)与各Agent决策记录，用于盘后复盘回溯决策链路（CIO盘后复盘）", port.CategoryResearch, "low", []port.AgentRole{port.RoleCIO, port.RoleRisk}, decisionLogTool)
	registerTeamTool("market_sixdim_detect", "基于六维判势评估A股市场环境：技术/广度/量能/资金/情绪/外部六维得分+冲突修正总分+建议仓位系数position_rate+市场标签，策略信号仓位=原始信号×position_rate（CIO盘前决策与盘中监控前置工具，数据来自DuckDB真实行情）", port.CategoryMarketData, "low", []port.AgentRole{port.RoleCIO, port.RolePlanner, port.RoleRisk, port.RoleQuant}, sixdimTool)
	registerTeamTool("market_order_book", "查询个股实时盘口状态：五档买卖挂单(价量)/外盘内盘/换手率/量比/涨停跌停价，判定盘口状态(涨停封死买不进/涨停打开/跌停封死卖不出/跌停打开/正常)与巨量跌停信号。建仓前用可避免在跌停板/封板上接飞刀，减仓前用可判断跌停封死卖不出需保留持仓（数据来自实时行情，无伪造）", port.CategoryMarketData, "low", []port.AgentRole{port.RoleCIO, port.RolePlanner, port.RoleRisk, port.RoleQuant}, orderBookTool)
	registerTeamTool("update_trading_calendar", "联网刷新A股交易日历到外挂配置文件并缓存，返回本次新增的休市日数量。未来节假日/休市安排不明确时调用以更新日历", port.CategoryResearch, "low", []port.AgentRole{port.RoleQuant, port.RolePlanner, port.RoleCIO}, tradingCalendarTool)
	registerTeamTool("check_trading_day", "检查指定日期（默认今天）是否为A股交易日，返回是否交易日/是否休市及前后相邻交易日。CIO日终结算前确认当日是否交易日，避免在非交易日误执行结算", port.CategoryResearch, "low", []port.AgentRole{port.RoleCIO, port.RoleTrader, port.RoleRisk, port.RoleQuant}, tradingDayCheckTool)

	// 六维原子数据工具（真实公开数据源，来源已标注，无伪造）
	registerTeamTool("get_index_technical", "获取A股主要指数(上证/深证成指/创业板指)技术趋势：现价、MA20/MA60、站上与否、5/20日收益与趋势方向。盘前技术面判势（数据DuckDB真实日K）", port.CategoryMarketData, "low", []port.AgentRole{port.RoleCIO, port.RoleQuant, port.RoleRisk, port.RolePlanner}, indexTechnicalTool)
	registerTeamTool("get_market_breadth", "获取A股市场广度：全市场涨跌家数与上涨占比、涨停/跌停家数、20日新高新低(采样)、热点板块数（DuckDB真实统计）", port.CategoryMarketData, "low", []port.AgentRole{port.RoleCIO, port.RoleQuant, port.RoleRisk, port.RolePlanner}, marketBreadthTool)
	registerTeamTool("get_turnover", "获取A股量能与杠杆资金：今日两市实时成交额(腾讯)、近期成交额历史(雪球)、融资余额(东方财富)，判断放量/缩量（真实数据来源已标注）", port.CategoryMarketData, "low", []port.AgentRole{port.RoleCIO, port.RoleQuant, port.RoleRisk}, turnoverTool)
	registerTeamTool("get_capital_flow", "获取A股资金面指标：融资余额及其环比(杠杆资金增减)、两市实时成交额、北向说明(2024-08后无公开实时源)。判断增量/杠杆资金流向（真实数据，来源已标注）", port.CategoryMarketData, "low", []port.AgentRole{port.RoleCIO, port.RoleQuant, port.RoleRisk}, capitalFlowTool)
	registerTeamTool("get_limitup_board", "获取A股涨停板行情(东方财富)：涨停/跌停家数、最高连板、连板梯队与封单金额，判断打板情绪与赚钱效应强度（真实数据，无伪造）", port.CategoryMarketData, "low", []port.AgentRole{port.RoleCIO, port.RoleRisk, port.RoleQuant}, limitUpBoardTool)
	registerTeamTool("get_external_market", "获取隔夜外围市场实时行情(腾讯全球代码)：道指/纳斯达克/标普、恒指/恒生国企、富时A50期货含涨跌幅，判断外盘对A股开盘情绪影响（真实数据，来源已标注）", port.CategoryMarketData, "low", []port.AgentRole{port.RoleCIO, port.RoleRisk, port.RoleQuant, port.RolePlanner}, externalMarketTool)

	// 事件/公告类（补齐「只有行情数字、没有信息事件」的系统盲区）
	registerTeamTool("get_market_news", "获取最新A股财经快讯(东方财富)：政策/资金/行业新闻标题与摘要，盘前了解影响市场的消息面（真实数据，来源已标注）", port.CategoryMarketData, "low", []port.AgentRole{port.RoleCIO, port.RoleQuant, port.RoleRisk, port.RolePlanner}, marketNewsTool)
	registerTeamTool("get_stock_announcement", "获取个股(或全市场)最新公告(东方财富)：停复牌/业绩/重大事项标题与日期，建仓前排查重大利空（真实数据，来源已标注）", port.CategoryResearch, "low", []port.AgentRole{port.RoleQuant, port.RoleCIO, port.RoleRisk}, stockAnnouncementTool)
	registerTeamTool("get_lhb", "获取指定日期龙虎榜明细(东方财富)：上榜个股涨跌幅与买卖净额，反映游资/机构活跃度（真实数据，来源已标注）", port.CategoryMarketData, "low", []port.AgentRole{port.RoleCIO, port.RoleRisk, port.RoleQuant}, lhbTool)
	registerTeamTool("get_fundamental", "查询个股最近已披露财务数据(DuckDB财务报告)：营收/净利润及同比、ROE、毛利率、资产负债率、EPS等，按披露日对齐无未来函数（真实数据）", port.CategoryResearch, "low", []port.AgentRole{port.RoleQuant, port.RoleCIO}, fundamentalTool)
	registerTeamTool("check_market_rules", "查询A股交易与制度规则知识(内置真实规则，非行情)：涨跌停/T+1/交易时段/ST/手续费/整手/交易单位，判断订单合法性避免臆造规则", port.CategoryMarketData, "low", []port.AgentRole{port.RoleCIO, port.RoleRisk, port.RoleQuant, port.RolePlanner, port.RoleTrader}, marketRulesCheckTool)

	// 第二批增强工具（真实数据、确定性计算）
	registerTeamTool("get_stock_fund_flow", "获取单只股票真实资金流向(TDX通达信终端)：主力/大单/小单净流入与占成交比。建仓前判断主力资金动向、排查资金面风险（真实数据来源已标注）", port.CategoryMarketData, "low", []port.AgentRole{port.RoleQuant, port.RoleCIO, port.RoleRisk}, stockFundFlowTool)
	if structureRiskTool != nil {
		registerTeamTool("calculate_structure_risk", "对给定候选/持仓股票集合计算结构风险(DuckDB真实日K)：年化波动率、最大回撤、数据充分性及综合风险等级。Risk审查候选池结构风险使用（确定性）", port.CategoryRisk, "low", []port.AgentRole{port.RoleRisk, port.RoleQuant, port.RoleCIO}, structureRiskTool)
	}
	if performanceNavTool != nil {
		registerTeamTool("get_performance_nav", "读取组合真实结算绩效(sqlite每日结算)：净值、累计/当日收益、最大回撤、总资产/现金/持仓市值/持仓数。CIO复盘与归因核对真实表现使用（真实数据）", port.CategoryPortfolio, "low", []port.AgentRole{port.RoleCIO}, performanceNavTool)
	}
	if investmentPlanTool != nil {
		registerTeamTool("get_investment_plan", "读取当前投资方案(investment_plans数据库)：计划名称/风险等级/目标收益/任务书/股票池及权重。CIO/Planner核对计划与实际持仓一致性使用", port.CategoryPortfolio, "low", []port.AgentRole{port.RoleCIO, port.RolePlanner}, investmentPlanTool)
	}
	if claimAccuracyTool != nil {
		registerTeamTool("get_claim_accuracy", "返回智能体可证伪判断台账命中率(claim_store真实收益对账)：总样本/已验证/命中数/准确率%/命中与未命中平均信心。CIO/Quant盘前复盘自身近期判断准不准、校准信心使用", port.CategoryResearch, "low", []port.AgentRole{port.RoleCIO, port.RoleQuant}, claimAccuracyTool)
	}
	if recordAlphaTool != nil {
		registerTeamTool("record_alpha", "把经回测/因子复盘通过的策略或因子签名写入AlphaStore(标注适用市场regime/指标值)。CIO/Quant在验证有效后沉淀alpha，供日后按市场状态复用", port.CategoryResearch, "low", []port.AgentRole{port.RoleCIO, port.RoleQuant}, recordAlphaTool)
	}
	if getAlphaByRegimeTool != nil {
		registerTeamTool("get_alpha_by_regime", "按当前六维市场标签(regime)从AlphaStore检索历史验证通过的alpha候选(策略/因子及指标)，附近30天全部候选。盘前在相似市场状态下复用历史有效alpha", port.CategoryResearch, "low", []port.AgentRole{port.RoleCIO, port.RoleQuant}, getAlphaByRegimeTool)
	}
	if getAlphaTopTool != nil {
		registerTeamTool("get_alpha_top", "返回AlphaStore中指标值最高的候选Alpha排行，供CIO/Planner构建股票池、复核策略时参考历史最有效alpha", port.CategoryResearch, "low", []port.AgentRole{port.RoleCIO, port.RolePlanner, port.RoleQuant}, getAlphaTopTool)
	}
	if generalizationTool != nil {
		registerTeamTool("estimate_strategy_generalization", "对指定策略做前向泛化检查(训练70%/测试30%分别回测、对比两段夏普)。测试明显低于训练即过拟合。DuckDB真实行情，Quant复盘策略健壮性用", port.CategoryResearch, "low", []port.AgentRole{port.RoleQuant, port.RoleCIO}, generalizationTool)
	}
	if strategyRankTool != nil {
		registerTeamTool("rank_strategy_generalization", "对全部内置策略做前向泛化回测并按测试段夏普排序，输出各策略训练/测试夏普与差距，并对比测试期市场(买入持有)收益作为随机基线。识别当前阶段最有效alpha与最快过拟合的策略", port.CategoryResearch, "low", []port.AgentRole{port.RoleQuant, port.RoleCIO}, strategyRankTool)
	}
	if sectorRotationTool != nil {
		registerTeamTool("get_sector_rotation", "获取板块轮动景气：分行业成分股平均涨跌幅排名/底部、上涨行业占比、集中度与轮动状态(普涨/普跌/结构性轮动/分化)。DuckDB真实最新交易日行情，选股与行业配置参考", port.CategoryMarketData, "low", []port.AgentRole{port.RoleCIO, port.RoleQuant, port.RolePlanner}, sectorRotationTool)
	}
	if skillCatalogTool != nil {
		registerTeamTool("get_skill_catalog", "返回量化投研技能目录：把已注册原子工具按领域组合为可复用技能。智能体按场景挑选技能组合使用", port.CategoryResearch, "low", []port.AgentRole{port.RoleCIO, port.RoleQuant, port.RolePlanner, port.RoleTrader, port.RoleRisk}, skillCatalogTool)
	}
	if factorHealthTool != nil {
		registerTeamTool("get_factor_health_report", "读取最近一次 factor_review 持久化的真实因子质量画像(FactorQuality表 [0,1])并按质量分级。反映当前阶段各因子选股有效度，未复盘则提示先运行 factor_review", port.CategoryResearch, "low", []port.AgentRole{port.RoleQuant, port.RoleCIO}, factorHealthTool)
	}

	// 为每个Agent注入工具注册表（运行时权限强制校验）
	for _, agent := range team {
		if agent != nil {
			agent.SetToolRegistry(toolReg)
		}
	}

	// CIO: 组合优化 + 市场数据 + 选股 + 股票池 + 组合管理
	if agent, ok := team[port.RoleCIO]; ok && agent != nil {
		agent.RegisterTool(AdaptTool(portfolioOptimizerTool))
		agent.RegisterTool(AdaptTool(marketDataTool))
		if screenMarketTool != nil {
			agent.RegisterTool(AdaptTool(screenMarketTool))
		}
		if stockPoolTool != nil {
			agent.RegisterTool(AdaptTool(stockPoolTool))
		}
		if getStockPoolTool != nil {
			agent.RegisterTool(AdaptTool(getStockPoolTool))
		}
		if portfolioStateTool != nil {
			agent.RegisterTool(AdaptTool(portfolioStateTool))
		}
		if getPositionsTool != nil {
			agent.RegisterTool(AdaptTool(getPositionsTool))
		}
		agent.RegisterTool(AdaptTool(orderCostTool))
		if macroSnapshotTool != nil {
			agent.RegisterTool(AdaptTool(macroSnapshotTool))
		}
		if portfolioRiskViewTool != nil {
			agent.RegisterTool(AdaptTool(portfolioRiskViewTool))
		}
		if limitCheckTool != nil {
			agent.RegisterTool(AdaptTool(limitCheckTool))
		}
		if portfolioRiskMetricsTool != nil {
			agent.RegisterTool(AdaptTool(portfolioRiskMetricsTool))
		}
		if factorReviewTool != nil {
			agent.RegisterTool(AdaptTool(factorReviewTool))
		}
		if searchMarketTool != nil {
			agent.RegisterTool(AdaptTool(searchMarketTool))
		}
		agent.RegisterTool(AdaptTool(marketStatsTool))
		if dailySettlementTool != nil {
			agent.RegisterTool(AdaptTool(dailySettlementTool))
		}
		// A档工具：市场状态判定（决策前置）+ Mandate合规复核 + 情绪/止损监控 + 决策日志复盘
		if marketRegimeTool != nil {
			agent.RegisterTool(AdaptTool(marketRegimeTool))
		}
		if mandateComplianceTool != nil {
			agent.RegisterTool(AdaptTool(mandateComplianceTool))
		}
		if sentimentMonitorTool != nil {
			agent.RegisterTool(AdaptTool(sentimentMonitorTool))
		}
		if stopLossMonitorTool != nil {
			agent.RegisterTool(AdaptTool(stopLossMonitorTool))
		}
		if decisionLogTool != nil {
			agent.RegisterTool(AdaptTool(decisionLogTool))
		}
		if sixdimTool != nil {
			agent.RegisterTool(AdaptTool(sixdimTool))
		}
		if orderBookTool != nil {
			agent.RegisterTool(AdaptTool(orderBookTool))
		}
		if dailyStrategyTool != nil {
			agent.RegisterTool(AdaptTool(dailyStrategyTool))
		}
		log.Printf("[Team] CIO tools registered: optimize_portfolio, get_market_data, search_market, get_market_stats, screen_market, build_stock_pool, get_stock_pool, get_portfolio_state, get_positions, order_cost_estimator, get_macro_snapshot, portfolio_risk_view, limit_check, portfolio_risk_metrics, factor_review, daily_settlement, market_regime_detect, mandate_compliance_check, sentiment_monitor, stop_loss_monitor, decision_log, market_sixdim_detect, market_order_book, select_next_day_strategy")
	}

	// Planner: 选股 + 市场搜索 + 股票池 + 组合状态
	if agent, ok := team[port.RolePlanner]; ok && agent != nil {
		if screenMarketTool != nil {
			agent.RegisterTool(AdaptTool(screenMarketTool))
		}
		if stockPoolTool != nil {
			agent.RegisterTool(AdaptTool(stockPoolTool))
		}
		if portfolioStateTool != nil {
			agent.RegisterTool(AdaptTool(portfolioStateTool))
		}
		if macroSnapshotTool != nil {
			agent.RegisterTool(AdaptTool(macroSnapshotTool))
		}
		agent.RegisterTool(AdaptTool(marketStatsTool))
		// 组合优化：与 CIO/Quant 共享同一算法（资产配置核心）
		if portfolioOptimizerTool != nil {
			agent.RegisterTool(AdaptTool(portfolioOptimizerTool))
		}
		// A档工具：Mandate合规校验（核心职责）+ 市场状态判定（资产配置依赖）
		if mandateComplianceTool != nil {
			agent.RegisterTool(AdaptTool(mandateComplianceTool))
		}
		if marketRegimeTool != nil {
			agent.RegisterTool(AdaptTool(marketRegimeTool))
		}
		if sixdimTool != nil {
			agent.RegisterTool(AdaptTool(sixdimTool))
		}
		if orderBookTool != nil {
			agent.RegisterTool(AdaptTool(orderBookTool))
		}
		log.Printf("[Team] Planner tools registered: get_market_stats, screen_market, build_stock_pool, get_portfolio_state, get_macro_snapshot, optimize_portfolio, mandate_compliance_check, market_regime_detect, market_sixdim_detect, market_order_book")
	}

	// Quant: 回测 + 选股 + 市场数据 + 持仓查询
	if agent, ok := team[port.RoleQuant]; ok && agent != nil {
		if backtestTool != nil {
			agent.RegisterTool(AdaptTool(backtestTool))
		}
		if screenMarketTool != nil {
			agent.RegisterTool(AdaptTool(screenMarketTool))
		}
		if stockPoolTool != nil {
			agent.RegisterTool(AdaptTool(stockPoolTool))
		}
		agent.RegisterTool(AdaptTool(marketDataTool))
		if getPositionsTool != nil {
			agent.RegisterTool(AdaptTool(getPositionsTool))
		}
		agent.RegisterTool(AdaptTool(orderCostTool))
		if macroSnapshotTool != nil {
			agent.RegisterTool(AdaptTool(macroSnapshotTool))
		}
		if portfolioRiskViewTool != nil {
			agent.RegisterTool(AdaptTool(portfolioRiskViewTool))
		}
		if limitCheckTool != nil {
			agent.RegisterTool(AdaptTool(limitCheckTool))
		}
		if portfolioRiskMetricsTool != nil {
			agent.RegisterTool(AdaptTool(portfolioRiskMetricsTool))
		}
		if factorReviewTool != nil {
			agent.RegisterTool(AdaptTool(factorReviewTool))
		}
		if portfolioOptimizerTool != nil {
			agent.RegisterTool(AdaptTool(portfolioOptimizerTool))
		}
		if searchMarketTool != nil {
			agent.RegisterTool(AdaptTool(searchMarketTool))
		}
		agent.RegisterTool(AdaptTool(marketStatsTool))
		if marketRegimeTool != nil {
			agent.RegisterTool(AdaptTool(marketRegimeTool))
		}
		if strategyTunerTool != nil {
			agent.RegisterTool(AdaptTool(strategyTunerTool))
		}
		if dailyStrategyTool != nil {
			agent.RegisterTool(AdaptTool(dailyStrategyTool))
		}
		log.Printf("[Team] Quant tools registered: optimize_portfolio, run_backtest, get_market_data, search_market, get_market_stats, screen_market, build_stock_pool, get_positions, order_cost_estimator, get_macro_snapshot, portfolio_risk_view, limit_check, portfolio_risk_metrics, factor_review, market_regime_detect, tune_strategy_params, select_next_day_strategy")
	}

	// Risk: 市场数据 + 市场统计 + 股票池 + 组合管理
	if agent, ok := team[port.RoleRisk]; ok && agent != nil {
		agent.RegisterTool(AdaptTool(marketDataTool))
		agent.RegisterTool(AdaptTool(marketStatsTool))
		if getStockPoolTool != nil {
			agent.RegisterTool(AdaptTool(getStockPoolTool))
		}
		if portfolioStateTool != nil {
			agent.RegisterTool(AdaptTool(portfolioStateTool))
		}
		if getPositionsTool != nil {
			agent.RegisterTool(AdaptTool(getPositionsTool))
		}
		if macroSnapshotTool != nil {
			agent.RegisterTool(AdaptTool(macroSnapshotTool))
		}
		if portfolioRiskViewTool != nil {
			agent.RegisterTool(AdaptTool(portfolioRiskViewTool))
		}
		if limitCheckTool != nil {
			agent.RegisterTool(AdaptTool(limitCheckTool))
		}
		if portfolioRiskMetricsTool != nil {
			agent.RegisterTool(AdaptTool(portfolioRiskMetricsTool))
		}
		if dailySettlementTool != nil {
			agent.RegisterTool(AdaptTool(dailySettlementTool))
		}
		// A档工具：市场情绪监控 + 止损监控（核心风控职责）+ 市场状态 + 决策日志审计
		if sentimentMonitorTool != nil {
			agent.RegisterTool(AdaptTool(sentimentMonitorTool))
		}
		if stopLossMonitorTool != nil {
			agent.RegisterTool(AdaptTool(stopLossMonitorTool))
		}
		if marketRegimeTool != nil {
			agent.RegisterTool(AdaptTool(marketRegimeTool))
		}
		if decisionLogTool != nil {
			agent.RegisterTool(AdaptTool(decisionLogTool))
		}
		if sixdimTool != nil {
			agent.RegisterTool(AdaptTool(sixdimTool))
		}
		if orderBookTool != nil {
			agent.RegisterTool(AdaptTool(orderBookTool))
		}
		log.Printf("[Team] Risk tools registered: get_market_data, get_market_stats, get_stock_pool, get_portfolio_state, get_positions, get_macro_snapshot, portfolio_risk_view, limit_check, portfolio_risk_metrics, daily_settlement, sentiment_monitor, stop_loss_monitor, market_regime_detect, decision_log, market_sixdim_detect, market_order_book")
	}

	// Trader: 市场数据 + 选股 + 股票池 + 组合管理 + 交易执行
	if agent, ok := team[port.RoleTrader]; ok && agent != nil {
		agent.RegisterTool(AdaptTool(marketDataTool))
		agent.RegisterTool(AdaptTool(searchMarketTool))
		if getStockPoolTool != nil {
			agent.RegisterTool(AdaptTool(getStockPoolTool))
		}
		if getPositionsTool != nil {
			agent.RegisterTool(AdaptTool(getPositionsTool))
		}
		if placeTradeTool != nil {
			agent.RegisterTool(AdaptTool(placeTradeTool))
		}
		agent.RegisterTool(AdaptTool(orderCostTool))
		if limitCheckTool != nil {
			agent.RegisterTool(AdaptTool(limitCheckTool))
		}
		agent.RegisterTool(AdaptTool(marketStatsTool))
		if portfolioStateTool != nil {
			agent.RegisterTool(AdaptTool(portfolioStateTool))
		}
		if dailySettlementTool != nil {
			agent.RegisterTool(AdaptTool(dailySettlementTool))
		}
		log.Printf("[Team] Trader tools registered: get_market_data, search_market, get_market_stats, get_stock_pool, get_portfolio_state, get_positions, place_trade, order_cost_estimator, limit_check, daily_settlement")
	}
}
