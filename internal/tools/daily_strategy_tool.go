package tools

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/quantpilot/quantpilot/internal/backtest"
	"github.com/quantpilot/quantpilot/internal/data"
	"github.com/quantpilot/quantpilot/internal/util"
)

// ==================== DailyStrategyTool 次日交易策略选择工具 ====================
// select_next_day_strategy 供 Quant 智能体在每日收盘后做因子复盘时调用：
// 为下一交易日选定交易策略并写入 daily_strategy_plans 表（策略日计划表）。
// 操盘手次日盘前/盘中按该策略信号执行（如 KDJ 死叉卖出），从而实现“策略驱动减仓”。
//
// 设计原则：
//  1. 策略选择基于策略表真实回测指标（收益/夏普/胜率，由每日策略调优/指标刷新写入），
//     严禁伪造；无指标的策略不会入选。
//  2. 同一交易日只有一行计划：重复选择会覆盖更新（trade_date 唯一索引）。
//  3. 允许量化分析师显式指定 strategy_type（如今天用 KDJ 策略）；不指定时按综合分自动选出最优。
func (t *DailyStrategyTool) Name() string { return "select_next_day_strategy" }

func (t *DailyStrategyTool) Description() string {
	return "为下一交易日选定交易策略并写入策略日计划(daily_strategy_plans)：按策略表真实回测指标(收益/夏普/胜率)综合分自动选出最优，或显式指定strategy_type。操盘手次日按该策略信号执行卖出(如KDJ死叉)。Quant每日收盘后因子复盘时调用，数据来自真实回测，严禁伪造。"
}

func (t *DailyStrategyTool) GetDefinition() ToolDefinition {
	return ToolDefinition{
		Type: "function",
		Function: FunctionDef{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"strategy_type": map[string]interface{}{
						"type":        "string",
						"description": "可选：显式指定次日采用的策略类型（如 kdj_golden / ma_cross / ml_model）。为空时按回测指标综合分自动选出最优策略。",
					},
					"reason": map[string]interface{}{
						"type":        "string",
						"description": "选择理由（因子复盘摘要，供操盘手溯源）",
					},
				},
			},
		},
	}
}

// DailyStrategyTool 次日交易策略选择工具
type DailyStrategyTool struct {
	sqliteManager *data.SQLiteManager
	duckdbManager *data.DuckDBManager
}

// NewDailyStrategyTool 创建次日交易策略选择工具
func NewDailyStrategyTool(sqliteManager *data.SQLiteManager, duckdbManager *data.DuckDBManager) *DailyStrategyTool {
	return &DailyStrategyTool{sqliteManager: sqliteManager, duckdbManager: duckdbManager}
}

// Execute 选择次日交易策略并写入策略日计划表
func (t *DailyStrategyTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	if t.sqliteManager == nil || t.sqliteManager.GetDB() == nil {
		return nil, fmt.Errorf("数据库不可用，无法选择次日策略")
	}
	db := t.sqliteManager.GetDB()

	nextDate := util.NextTradingDay(time.Now()).Format("2006-01-02")
	chosenType, _ := args["strategy_type"].(string)
	reason, _ := args["reason"].(string)

	// 读取活跃策略列表
	var strategies []data.Strategy
	if err := db.Where("is_active = 1").Find(&strategies).Error; err != nil {
		return nil, fmt.Errorf("读取策略列表失败: %v", err)
	}
	if len(strategies) == 0 {
		return nil, fmt.Errorf("策略表为空，无法选择次日策略")
	}

	var chosen data.Strategy
	if chosenType != "" {
		// 显式指定：按策略类型匹配
		var found bool
		for i := range strategies {
			if strategies[i].StrategyType == chosenType {
				chosen = strategies[i]
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("未找到策略类型 %s 的活跃策略，可用: %v", chosenType, strategyTypeNames(strategies))
		}
	} else {
		// 自动：按回测指标综合分选最优
		best, stype := backtest.SelectBestStrategy(strategies)
		if stype == "" {
			return nil, fmt.Errorf("策略表无有效回测指标，无法自动选择；请先运行策略调优/指标刷新")
		}
		chosen = best
	}

	// 校验策略实例可构建
	params := backtest.ParseConfigJSON(chosen.ConfigJSON)
	strategy := backtest.BuildStrategyFromConfig(chosen.StrategyType, params)
	if strategy == nil {
		return nil, fmt.Errorf("策略类型 %s 无法构建策略实例", chosen.StrategyType)
	}

	if reason == "" {
		reason = fmt.Sprintf("因子复盘后选定策略「%s」，综合分=%.2f(收益%.2f%%/夏普%.2f/胜率%.2f%%)",
			chosen.Name, backtest.StrategySelectionScore(&chosen), chosen.TotalReturn, chosen.SharpeRatio, chosen.WinRate)
	}

	// 写入策略日计划（同一交易日唯一，覆盖更新）
	plan := &data.DailyStrategyPlan{
		TradeDate:    nextDate,
		StrategyType: chosen.StrategyType,
		StrategyName: chosen.Name,
		Reason:       reason,
		Status:       "active",
		CreatedBy:    "quant",
		Source:       "quant",
	}
	var existing data.DailyStrategyPlan
	if err := db.Where("trade_date = ?", nextDate).First(&existing).Error; err == nil {
		plan.ID = existing.ID
		plan.CreatedAt = existing.CreatedAt
		if err := db.Save(plan).Error; err != nil {
			return nil, fmt.Errorf("更新策略日计划失败: %v", err)
		}
	} else {
		if err := db.Create(plan).Error; err != nil {
			return nil, fmt.Errorf("写入策略日计划失败: %v", err)
		}
	}

	log.Printf("[DailyStrategy] 已为 %s 选定策略: %s (%s)", nextDate, chosen.Name, chosen.StrategyType)
	return map[string]interface{}{
		"trade_date":    nextDate,
		"strategy_type": chosen.StrategyType,
		"strategy_name": chosen.Name,
		"reason":        reason,
		"total_return":  chosen.TotalReturn,
		"sharpe_ratio":  chosen.SharpeRatio,
		"win_rate":      chosen.WinRate,
		"note":          "操盘手次日盘前/盘中将按该策略信号执行卖出（如 KDJ 死叉清仓）。",
	}, nil
}

// strategyTypeNames 提取策略类型列表（用于错误提示）
func strategyTypeNames(list []data.Strategy) []string {
	names := make([]string, 0, len(list))
	for i := range list {
		names = append(names, list[i].StrategyType)
	}
	return names
}
