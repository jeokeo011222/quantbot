package tools

import (
	"context"
	"fmt"
	"time"

	"github.com/quantpilot/quantpilot/internal/config"
	"github.com/quantpilot/quantpilot/internal/marketsixdim"
	"github.com/quantpilot/quantpilot/internal/port"
)

// ==================== SixDimTool ====================
// market_sixdim_detect 市场六维判势工具
// 基于《六维策略.md》模块B MarketSixDim：
//   盘前/盘中实时评估A股市场环境，输出六维得分(0-100)、冲突修正总分、建议仓位系数 position_rate、
//   市场标签(强势/结构性震荡/偏弱/退潮风险)。
// 数据全部来自 DuckDB 真实行情；北向/隔夜外围等无真实源的维度用真实代理指标并标注，严禁伪造。

// SixDimTool 市场六维判势工具
type SixDimTool struct {
	mkt port.MarketDataStore
	// snap/ext/ths 为外部实时源注入（由宿主 brainhost 提供适配器）；nil 时各维度自动回退 DuckDB 历史。
	snap    port.SnapSource
	ext     port.MarketExternalSources
	ths     port.THSSentimentSource
	fetcher *sixdim.Fetcher
	// cfgLoader 数据源配置加载器（非nil时每次判势前从最新配置重建获取器，
	// 使「设置-数据源」页面保存的 sixdim_source 配置立即生效，无需重启）
	cfgLoader func() sixdim.SourceConfig
}

// NewSixDimTool 创建市场六维判势工具（使用默认数据源配置）
func NewSixDimTool(mkt port.MarketDataStore) *SixDimTool {
	t := &SixDimTool{mkt: mkt}
	t.fetcher = sixdim.NewFetcher(mkt)
	t.applySources()
	return t
}

// NewSixDimToolWithConfigLoader 用数据源配置加载器创建工具（每次判势实时读取配置）
func NewSixDimToolWithConfigLoader(mkt port.MarketDataStore, loader func() sixdim.SourceConfig) *SixDimTool {
	t := NewSixDimTool(mkt)
	t.cfgLoader = loader
	return t
}

// SetSnapSource 注入活跃实时行情源适配器（由宿主 brainhost 提供）。
func (t *SixDimTool) SetSnapSource(s port.SnapSource) {
	t.snap = s
	if t.fetcher != nil {
		t.fetcher.SetSnapSource(s)
	}
}

// SetExternalSources 注入外部实时源适配器（北向/两融/涨停池/隔夜外围）。
func (t *SixDimTool) SetExternalSources(s port.MarketExternalSources) {
	t.ext = s
	if t.fetcher != nil {
		t.fetcher.SetExternalSources(s)
	}
}

// SetTHSSource 注入同花顺官方情绪面适配器。
func (t *SixDimTool) SetTHSSource(s port.THSSentimentSource) {
	t.ths = s
	if t.fetcher != nil {
		t.fetcher.SetTHSSource(s)
	}
}

// SourceConfigFromAppConfig 将 config.json 中的六维数据源配置转换为判势引擎配置
func SourceConfigFromAppConfig(sc config.SixDimSourceConfig) sixdim.SourceConfig {
	return sixdim.SourceConfig{
		NorthboundEnabled: sc.Northbound.Enabled, NorthboundPriority: sc.Northbound.Priority,
		MarginEnabled: sc.Margin.Enabled, MarginPriority: sc.Margin.Priority,
		VolumeExpansionEnabled: sc.VolumeExpansion.Enabled, VolumeExpansionPriority: sc.VolumeExpansion.Priority,
		THSBoardsEnabled: sc.THSBoards.Enabled, THSBoardsPriority: sc.THSBoards.Priority, // 同花顺官方涨停/跌停/炸板池+连板天梯
		LimitBoardEnabled: sc.LimitBoard.Enabled, LimitBoardPriority: sc.LimitBoard.Priority,
		FullMarketStatsEnabled: sc.FullMarketStats.Enabled, FullMarketStatsPriority: sc.FullMarketStats.Priority,
		OvernightEnabled: sc.Overnight.Enabled, OvernightPriority: sc.Overnight.Priority,
	}
}

// applySources 将注入的外部实时源同步到当前获取器。
func (t *SixDimTool) applySources() {
	if t.fetcher == nil {
		return
	}
	t.fetcher.SetSnapSource(t.snap)
	t.fetcher.SetExternalSources(t.ext)
	t.fetcher.SetTHSSource(t.ths)
}

// applyConfig 若配置了加载器，则每次执行前用最新配置重建获取器（设置页保存后立即生效）
func (t *SixDimTool) applyConfig() {
	if t.cfgLoader == nil {
		return
	}
	t.fetcher = sixdim.NewFetcherWithConfig(t.mkt, t.cfgLoader())
	t.applySources()
}

func (t *SixDimTool) Name() string { return "market_sixdim_detect" }

func (t *SixDimTool) Description() string {
	return "基于六维判势(MarketSixDim)评估A股市场环境：技术趋势/市场广度/量能流动性/资金结构/情绪赚钱效应/外部约束，" +
		"输出各维度得分、冲突修正后总分、建议仓位系数 position_rate 与市场标签(强势/结构性震荡/偏弱/退潮风险)。" +
		"策略信号仓位 = 原始信号仓位 × position_rate。CIO盘前决策与盘中监控前置工具，技术/广度/量能来自DuckDB真实行情，情绪/资金结构优先东财/同花顺实时源并自动回退。" +
		"action=sources 时不判势，直接返回当前数据源配置与最近运行指标（成功/失败次数、耗时、最近错误），用于观测各外部实时源健康状况。"
}

func (t *SixDimTool) GetDefinition() ToolDefinition {
	return ToolDefinition{
		Type: "function",
		Function: FunctionDef{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"save": map[string]interface{}{
						"type":        "boolean",
						"description": "是否将本次判势结果落库到 market_sixdim_daily 表（默认 false）",
					},
					"action": map[string]interface{}{
						"type":        "string",
						"enum":        []string{"detect", "sources"},
						"description": "默认 detect=执行判势；sources=仅返回数据源配置与运行指标（不判势）",
					},
				},
				"required": []string{},
			},
		},
	}
}

func (t *SixDimTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	// 每次执行前刷新数据源配置（设置-数据源页面保存后立即生效）
	t.applyConfig()

	action, _ := args["action"].(string)
	if action == "" {
		action = "detect"
	}
	// 可观测：直接返回当前数据源配置与各外部源运行指标（成功/失败/耗时/最近错误），
	// 帮助判断「高优先级源是否频繁失败、应否调整优先级或降级」（对标 PanWatch marketdata metrics）。
	if action == "sources" {
		cfg := t.fetcher.CurrentSourceConfig()
		return map[string]interface{}{
			"status": "ok",
			"source_config": map[string]interface{}{
				"northbound":        map[string]interface{}{"enabled": cfg.NorthboundEnabled, "priority": cfg.NorthboundPriority},
				"margin":            map[string]interface{}{"enabled": cfg.MarginEnabled, "priority": cfg.MarginPriority},
				"volume_expansion":  map[string]interface{}{"enabled": cfg.VolumeExpansionEnabled, "priority": cfg.VolumeExpansionPriority},
				"ths_boards":        map[string]interface{}{"enabled": cfg.THSBoardsEnabled, "priority": cfg.THSBoardsPriority},
				"limit_board":       map[string]interface{}{"enabled": cfg.LimitBoardEnabled, "priority": cfg.LimitBoardPriority},
				"full_market_stats": map[string]interface{}{"enabled": cfg.FullMarketStatsEnabled, "priority": cfg.FullMarketStatsPriority},
				"overnight":         map[string]interface{}{"enabled": cfg.OvernightEnabled, "priority": cfg.OvernightPriority},
			},
			"source_stats": sixdim.SourceStatsSnapshot(),
			"note":         "优先级数值越小越优先；source_stats 累计自进程启动以来各外部实时源调用成败/耗时/最近错误，空表示尚未调用。环境变量 SIXDIM_<源名>_ENABLED/PRIORITY 可覆盖默认配置",
		}, nil
	}

	if t.mkt == nil || !t.mkt.HasStockDB() {
		return nil, fmt.Errorf("DuckDB 不可用，无法执行市场六维判势")
	}

	input, err := t.fetcher.Fetch(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取六维判势输入数据失败: %w", err)
	}
	report := sixdim.Evaluate(input)

	save, _ := args["save"].(bool)
	if save {
		if err := sixdim.SaveReport(ctx, t.mkt, sixdim.TradeDateOf(time.Now()), report); err != nil {
			// 落库失败不阻塞本次判势返回
			return nil, fmt.Errorf("六维判势结果落库失败: %w", err)
		}
	}

	return map[string]interface{}{
		"as_of":                time.Now().Format("2006-01-02"),
		"dim_scores":           report.DimScores,
		"dim_chinese":          sixdim.DimChinese,
		"raw_total_score":      report.RawTotalScore,
		"conflict_count":       report.ConflictCount,
		"adjusted_total_score": report.AdjustedTotalScore,
		"position_rate":        report.PositionRate,
		"market_tag":           report.MarketTag,
		"position_advice":      fmt.Sprintf("策略信号仓位 = 原始信号仓位 × %.2f（%s）", report.PositionRate, report.MarketTag),
		"sources":              report.Sources,
		"source_stats":         sixdim.SourceStatsSnapshot(),
		"note":                 "技术/广度/量能来自DuckDB真实行情；情绪优先东财实时涨停/跌停/炸板池(5min缓存)；资金结构优先同花顺北向实时净流入、其次东财融资余额趋势；隔夜外围优先腾讯全球指数真实数据(4s超时+5min缓存)，失败回退指数跳空代理。所有实时源失败均自动回退并标注，无伪造。source_stats 展示各外部实时源累计成功/失败/耗时，可据此判断是否需要调整数据源优先级（action=sources 查看配置）",
	}, nil
}
