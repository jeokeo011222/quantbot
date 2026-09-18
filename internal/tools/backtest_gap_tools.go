package tools

import (
	"context"
	"fmt"
	"sort"

	"github.com/quantpilot/quantpilot/internal/backtest"
	"github.com/quantpilot/quantpilot/internal/data"
	"github.com/quantpilot/quantpilot/internal/tdx"
)

// walkForwardSharpe 时序前向拆分（train 70% / test 30%，非随机打乱，无前视偏差）分别回测策略，
// 返回训练段/测试段夏普。测试段夏普显著低于训练段即泛化差距大的过拟合信号。
func walkForwardSharpe(bars []tdx.KlineBar, strategy backtest.Strategy) (trainSh, testSh float64, sampleOK bool) {
	if len(bars) < 40 {
		return 0, 0, false
	}
	split := int(float64(len(bars)) * 0.7)
	trainBars := bars[:split]
	testBars := bars[split:]
	const ip = 100000.0
	tr := backtest.RunBacktestWithStrategy(trainBars, strategy, ip, 0, 0)
	te := backtest.RunBacktestWithStrategy(testBars, strategy, ip, 0, 0)
	return tr.SharpeRatio, te.SharpeRatio, true
}

// ==================== estimate_strategy_generalization（P2 泛化差距） ====================

type GeneralizationTool struct {
	duckdb *data.DuckDBManager
}

func NewGeneralizationTool(m *data.DuckDBManager) *GeneralizationTool {
	return &GeneralizationTool{duckdb: m}
}

func (t *GeneralizationTool) Name() string { return "estimate_strategy_generalization" }

func (t *GeneralizationTool) Description() string {
	return "对指定策略做前向(walk-forward)泛化检查：把真实历史K线按时序拆成训练(70%)/测试(30%)两段分别回测，对比两段夏普。测试夏普明显低于训练即存在过拟合(泛化差距大)。数据来自DuckDB真实行情"
}

func (t *GeneralizationTool) GetDefinition() ToolDefinition {
	return ToolDefinition{
		Type: "function",
		Function: FunctionDef{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"strategy_type": map[string]string{"type": "string", "description": "策略类型，如 MACross/KDJ/MACD/RSI/Bollinger 等"},
					"symbol":        map[string]string{"type": "string", "description": "标的K线代码，如 sh000300/sz002437，可选，默认沪深300"},
				},
				"required": []string{"strategy_type"},
			},
		},
	}
}

func (t *GeneralizationTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	if t.duckdb == nil {
		return map[string]interface{}{"status": "unavailable", "message": "DuckDB 未初始化"}, nil
	}
	strategyType, _ := args["strategy_type"].(string)
	symbol, _ := args["symbol"].(string)
	if symbol == "" {
		symbol = "sh000300"
	}
	strategy := backtest.GetStrategyByType(strategyType)
	if strategy == nil {
		return map[string]interface{}{"status": "error", "message": fmt.Sprintf("未知策略类型: %s", strategyType)}, nil
	}
	bars, err := backtest.GetKlineFromDuckDB(t.duckdb, data.ToMarketCode(symbol), 240)
	if err != nil || len(bars) < 40 {
		return map[string]interface{}{"status": "unavailable", "message": fmt.Sprintf("行情数据不足: %v", err)}, nil
	}
	tr, te, ok := walkForwardSharpe(bars, strategy)
	if !ok {
		return map[string]interface{}{"status": "unavailable", "message": "K线样本不足(需≥40根)"}, nil
	}
	gap := te - tr
	verdict := "一般"
	switch {
	case te >= 0 && gap >= -0.5:
		verdict = "泛化较好（测试段为正且与训练段差距小）"
	case te < 0:
		verdict = "过拟合风险高（测试段夏普为负）"
	case gap < -0.5:
		verdict = "过拟合风险较高（测试段明显差于训练段）"
	}
	return map[string]interface{}{
		"status":       "ok",
		"strategy":     strategyType,
		"symbol":       symbol,
		"train_sharpe": round2(tr),
		"test_sharpe":  round2(te),
		"gap":          round2(gap),
		"verdict":      verdict,
		"source":       "DuckDB 真实行情前向拆分回测",
	}, nil
}

// ==================== rank_strategy_generalization（P3 跨策略对比 + 随机基线） ====================

type StrategyRankTool struct {
	duckdb *data.DuckDBManager
}

func NewStrategyRankTool(m *data.DuckDBManager) *StrategyRankTool {
	return &StrategyRankTool{duckdb: m}
}

func (t *StrategyRankTool) Name() string { return "rank_strategy_generalization" }

func (t *StrategyRankTool) Description() string {
	return "对全部内置策略在当前标的上做前向泛化回测(训练70%/测试30%)，输出各策略训练/测试夏普、泛化差距，按测试夏普排序，并对比测试期市场(买入持有)收益作为随机基线。用于识别当前阶段最有效alpha与最快过拟合的策略"
}

func (t *StrategyRankTool) GetDefinition() ToolDefinition {
	return ToolDefinition{
		Type: "function",
		Function: FunctionDef{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"symbol": map[string]string{"type": "string", "description": "标的K线代码，可选，默认沪深300"},
				},
			},
		},
	}
}

func (t *StrategyRankTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	if t.duckdb == nil {
		return map[string]interface{}{"status": "unavailable", "message": "DuckDB 未初始化"}, nil
	}
	symbol, _ := args["symbol"].(string)
	if symbol == "" {
		symbol = "sh000300"
	}
	bars, err := backtest.GetKlineFromDuckDB(t.duckdb, data.ToMarketCode(symbol), 240)
	if err != nil || len(bars) < 40 {
		return map[string]interface{}{"status": "unavailable", "message": fmt.Sprintf("行情数据不足: %v", err)}, nil
	}

	type row struct {
		Strategy string  `json:"strategy"`
		Train    float64 `json:"train_sharpe"`
		Test     float64 `json:"test_sharpe"`
		Gap      float64 `json:"gap"`
	}
	var rows []row
	for _, meta := range backtest.BuiltinStrategies {
		strategy := backtest.GetStrategyByType(meta.StrategyType)
		if strategy == nil {
			continue
		}
		tr, te, ok := walkForwardSharpe(bars, strategy)
		if !ok {
			continue
		}
		rows = append(rows, row{meta.StrategyType, round2(tr), round2(te), round2(te - tr)})
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Test > rows[j].Test })

	// 测试期市场(买入持有)收益，作为随机基线对照
	splitI := int(float64(len(bars)) * 0.7)
	testBars := bars[splitI:]
	buyHold := 0.0
	if len(testBars) >= 2 {
		buyHold = (testBars[len(testBars)-1].Close - testBars[0].Open) / testBars[0].Open * 100
	}

	return map[string]interface{}{
		"status":                 "ok",
		"symbol":                 symbol,
		"test_market_return_pct": round2(buyHold),
		"ranking":                rows,
		"top_by_test_sharpe": func() interface{} {
			if len(rows) > 0 {
				return rows[0].Strategy
			}
			return ""
		}(),
		"source": "DuckDB 真实行情前向拆分回测",
	}, nil
}
