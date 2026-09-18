package tools

import (
	"context"
	"fmt"
	"time"

	"github.com/quantpilot/quantpilot/internal/portfolio"
)

// ==================== DailySettlementTool 每日结算工具 ====================
// daily_settlement 供 Trader(日终结算)/Risk(日终风险审查)/CIO(日报) 调用。
// 一次性完成当日结算（写入 PortfolioDailyStat 与持仓快照），返回结算后的组合数据。
// 职责：完成确定性结算与记录，不代做任何分析/建议。

// DailySettlementTool 每日结算工具
type DailySettlementTool struct {
	engine *portfolio.Engine
}

// NewDailySettlementTool 创建每日结算工具
func NewDailySettlementTool(engine *portfolio.Engine) *DailySettlementTool {
	return &DailySettlementTool{engine: engine}
}

func (t *DailySettlementTool) Name() string { return "daily_settlement" }

func (t *DailySettlementTool) Description() string {
	return "执行当日组合结算：将当日总资产/累计盈亏/今日盈亏/持仓快照写入数据库，并返回结算结果。日终结算任务请调用本工具完成确定性结算与记录，勿由模型自行推算。"
}

func (t *DailySettlementTool) GetDefinition() ToolDefinition {
	return ToolDefinition{
		Type: "function",
		Function: FunctionDef{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		},
	}
}

func (t *DailySettlementTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	if t.engine == nil {
		return nil, fmt.Errorf("组合引擎未初始化，无法结算")
	}

	// 刷新计价：交易时段用实时行情；非交易时段用最近结算收盘价（与 GetPortfolioState 一致）
	t.engine.RefreshPrices()

	// 执行当日结算（幂等：同日期 upsert）
	if err := t.engine.RecordDailySnapshot(); err != nil {
		return nil, fmt.Errorf("当日结算失败: %w", err)
	}

	// 读取结算结果用于返回
	stat, err := t.engine.GetLatestDailyStat()
	if err != nil {
		return nil, fmt.Errorf("结算完成但读取统计失败: %w", err)
	}

	return map[string]interface{}{
		"date":                  stat.StatDate,
		"total_assets":          round2(stat.TotalAssets),
		"cash":                  round2(stat.Cash),
		"market_value":          round2(stat.MarketValue),
		"total_pnl":             round2(stat.TotalPnL),
		"total_return_pct":      round4(stat.TotalReturn * 100),
		"daily_pnl":             round2(stat.DailyPnL),
		"daily_return_pct":      round4(stat.DailyReturn * 100),
		"positions_count":       stat.PositionsCount,
		"total_daily_volume":    round2(stat.TotalDailyVolume),
		"trade_count":           stat.TradeCount,
		"settled_at":            time.Now().Format(time.RFC3339),
		"note":                  "已完成当日结算，数据已写入数据库（总资产=现金+持仓价值；累计盈亏=总资产-期初资产；今日盈亏=总资产-昨日结算总资产）",
	}, nil
}