package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/quantpilot/quantpilot/internal/backtest"
	"github.com/quantpilot/quantpilot/internal/data"
	"github.com/quantpilot/quantpilot/internal/tdx"
	"gorm.io/gorm"
)

// ==================== StrategyTunerTool 自动化调参工具 ====================
// tune_strategy_params 供 Quant 智能体在每日复盘阶段调用，对策略参数做自动化网格调优。
//
// 设计原则：
//  1. 参数不写死在代码中：当前生效参数存于 strategies.config_json，
//     可调参数扫描范围存于 strategies.tune_ranges_json（均由策略表作为单一数据源）。
//  2. 调优基于真实基准指数K线（沪深300，回退上证指数），复用回测引擎统一算法。
//  3. 输出旧/新参数与指标对比报告，可选把更优参数写回策略表（apply=true）。
//
// 禁止：伪造数据、调参不落库却声称生效。

// StrategyTunerTool 自动化调参工具
type StrategyTunerTool struct {
	sqliteManager  *data.SQLiteManager
	duckdbManager  *data.DuckDBManager
	refreshMetrics func() error // 调优完成后统一刷新所有策略最新指标并写入（由智能体调参后自动写入）
}

// NewStrategyTunerTool 创建自动化调参工具
func NewStrategyTunerTool(sqliteManager *data.SQLiteManager, duckdbManager *data.DuckDBManager) *StrategyTunerTool {
	return &StrategyTunerTool{sqliteManager: sqliteManager, duckdbManager: duckdbManager}
}

// SetRefreshMetrics 设置调优完成后的指标刷新回调
func (t *StrategyTunerTool) SetRefreshMetrics(fn func() error) {
	t.refreshMetrics = fn
}

func (t *StrategyTunerTool) Name() string { return "tune_strategy_params" }

func (t *StrategyTunerTool) Description() string {
	return "对策略参数做自动化网格调优：基于真实基准指数K线遍历参数组合并回测，按夏普/收益/胜率选出更优参数写回策略表(apply=true)。Quant每日复盘调用，参数来自数据库config_json与tune_ranges_json，不写死在代码中。"
}

func (t *StrategyTunerTool) GetDefinition() ToolDefinition {
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
						"description": "可选：指定要调优的策略类型（如 ma_cross / ml_model）。为空时调优全部内置策略。",
					},
					"optimize_metric": map[string]interface{}{
						"type":        "string",
						"description": "优化目标指标（默认 sharpe）：sharpe=夏普比率, return=总收益率, win_rate=胜率",
						"enum":        []string{"sharpe", "return", "win_rate"},
					},
					"min_trades": map[string]interface{}{
						"type":        "integer",
						"description": "最少交易次数门槛（默认 8，过滤样本过少导致胜率失真的组合）",
					},
					"max_combos": map[string]interface{}{
						"type":        "integer",
						"description": "单个策略最大参数组合数（默认 400，超出则均匀降采样）",
					},
					"apply": map[string]interface{}{
						"type":        "boolean",
						"description": "是否把更优参数写回策略表（默认 true；false 仅评估不落库）",
					},
				},
			},
		},
	}
}

// tuneResult 单个策略的调优结果
type tuneResult struct {
	StrategyID   uint                   `json:"strategy_id"`
	Name         string                 `json:"name"`
	StrategyType string                 `json:"strategy_type"`
	Applied      bool                   `json:"applied"`
	Improved     bool                   `json:"improved"`
	Reason       string                 `json:"reason"`
	OldConfig    map[string]interface{} `json:"old_config,omitempty"`
	NewConfig    map[string]interface{} `json:"new_config,omitempty"`
	Old          *strategyMetrics       `json:"old,omitempty"`
	New          *strategyMetrics       `json:"new,omitempty"`
}

// strategyMetrics 一次回测的指标快照
type strategyMetrics struct {
	SharpeRatio float64 `json:"sharpe"`
	TotalReturn float64 `json:"return"`
	MaxDrawdown float64 `json:"max_drawdown"`
	WinRate     float64 `json:"win_rate"`
	Trades      int     `json:"trades"`
}

func (t *StrategyTunerTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	if t.sqliteManager == nil || t.duckdbManager == nil {
		return nil, fmt.Errorf("SQLite/DuckDB 未初始化，无法调参")
	}
	if !t.duckdbManager.HasStockDB() {
		return nil, fmt.Errorf("行情数据库不可用，无法获取真实基准K线")
	}

	// 解析参数
	strategyTypeFilter, _ := args["strategy_type"].(string)
	metric := "sharpe"
	if v, ok := args["optimize_metric"].(string); ok && v != "" {
		metric = v
	}
	minTrades := 8
	if v, ok := args["min_trades"].(float64); ok && v > 0 {
		minTrades = int(v)
	}
	maxCombos := 400
	if v, ok := args["max_combos"].(float64); ok && v > 0 {
		maxCombos = int(v)
	}
	apply := true
	if v, ok := args["apply"].(bool); ok {
		apply = v
	}

	// 基准K线 + 基准资金（保证能买1手指数）
	bars, benchmarkID, err := t.getBenchmarkBars(ctx)
	if err != nil {
		return nil, err
	}
	capital := benchmarkCapital(bars)
	log.Printf("[StrategyTuner] 基准=%s bars=%d capital=%.0f metric=%s min_trades=%d apply=%v",
		benchmarkID, len(bars), capital, metric, minTrades, apply)

	// 读取策略列表
	db := t.sqliteManager.GetDB()
	var strategies []data.Strategy
	q := db.Model(&data.Strategy{}).Where("is_builtin = ?", 1)
	if strategyTypeFilter != "" {
		q = q.Where("strategy_type = ?", strategyTypeFilter)
	}
	if err := q.Order("id ASC").Find(&strategies).Error; err != nil {
		return nil, fmt.Errorf("查询内置策略失败: %w", err)
	}

	report := map[string]interface{}{
		"benchmark":     benchmarkID,
		"bars":          len(bars),
		"capital":       capital,
		"metric":        metric,
		"min_trades":    minTrades,
		"apply":         apply,
		"results":       []interface{}{},
		"tuned_count":   0,
		"applied_count": 0,
		"timestamp":     time.Now().Format("2006-01-02 15:04:05"),
	}

	var results []tuneResult
	tuned := 0
	applied := 0
	for i := range strategies {
		st := &strategies[i]
		if !backtest.IsBuiltinStrategyType(st.StrategyType) {
			continue
		}

		r := t.tuneOne(st, bars, benchmarkID, capital, metric, minTrades, maxCombos, apply)
		results = append(results, r)
		if r.Applied {
			applied++
		}
		if r.New != nil {
			tuned++
		}
	}

	// 按是否改善排序，改善的在前
	sort.Slice(results, func(a, b int) bool {
		return results[a].Improved && !results[b].Improved
	})
	report["results"] = results
	report["tuned_count"] = tuned
	report["applied_count"] = applied

	// 调优完成后统一刷新所有策略最新指标并写入（智能体执行完参数调优后自动写入，
	// 替代启动时刷新，确保策略表指标始终为最新真实回测数据）
	if t.refreshMetrics != nil {
		if err := t.refreshMetrics(); err != nil {
			log.Printf("[StrategyTuner] 调优后刷新策略指标失败: %v", err)
		} else {
			log.Printf("[StrategyTuner] 调优完成，已刷新全部策略最新指标")
		}
	}
	return report, nil
}

// tuneOne 对单个策略做网格扫描并(可选)写回
func (t *StrategyTunerTool) tuneOne(st *data.Strategy, bars []tdx.KlineBar, benchmarkID string, capital float64, metric string, minTrades, maxCombos int, apply bool) tuneResult {
	res := tuneResult{
		StrategyID:   st.ID,
		Name:         st.Name,
		StrategyType: st.StrategyType,
		Applied:      false,
	}

	stopLoss, takeProfit := st.StopLossPct, st.TakeProfitPct
	if stopLoss <= 0 {
		stopLoss = 0.05
	}
	if takeProfit <= 0 {
		takeProfit = 0.20
	}

	// 解析当前配置与调参范围
	baseParams := backtest.ParseConfigJSON(st.ConfigJSON)
	ranges := parseTuneRanges(st.TuneRangesJSON)
	if len(ranges) == 0 {
		ranges = backtest.GetDefaultTuneRanges(st.StrategyType)
	}
	if len(ranges) == 0 {
		res.Reason = "无可调参数范围"
		return res
	}

	// 当前参数回测（baseline）
	baseStrategy := backtest.BuildStrategyFromConfig(st.StrategyType, baseParams)
	if baseStrategy == nil {
		res.Reason = "策略无法构造"
		return res
	}
	baseResult := backtest.RunBacktestWithLimitFin(bars, baseStrategy, capital, stopLoss, takeProfit, benchmarkID, "", backtest.NewDuckDBFinancialProvider(t.duckdbManager))
	oldMetrics := toMetrics(baseResult, capital)
	res.Old = oldMetrics
	res.OldConfig = baseParams

	// 生成参数组合并逐一回测
	combos := backtest.GenerateParamCombinations(ranges, maxCombos)
	var candidates []struct {
		combo   map[string]interface{}
		metrics *strategyMetrics
	}
	for _, combo := range combos {
		params := cloneParams(baseParams)
		for k, v := range combo {
			params[k] = v
		}
		strategy := backtest.BuildStrategyFromConfig(st.StrategyType, params)
		if strategy == nil {
			continue
		}
		r := backtest.RunBacktestWithLimitFin(bars, strategy, capital, stopLoss, takeProfit, benchmarkID, "", backtest.NewDuckDBFinancialProvider(t.duckdbManager))
		m := toMetrics(r, capital)
		if m.Trades < minTrades {
			continue
		}
		candidates = append(candidates, struct {
			combo   map[string]interface{}
			metrics *strategyMetrics
		}{combo: cloneParams(combo), metrics: m})
	}

	if len(candidates) == 0 {
		res.Reason = fmt.Sprintf("无满足最少交易数(%d)的组合", minTrades)
		return res
	}

	// 按优化目标排序
	sort.Slice(candidates, func(a, b int) bool {
		return metricScore(candidates[a].metrics, metric) > metricScore(candidates[b].metrics, metric)
	})
	best := candidates[0]

	// 判断是否更优：最优指标需不低于当前（当前交易不足时直接采纳最优）
	baseScore := metricScore(oldMetrics, metric)
	bestScore := metricScore(best.metrics, metric)
	res.New = best.metrics
	res.NewConfig = best.combo

	if bestScore < baseScore {
		res.Reason = fmt.Sprintf("扫描最优 %s=%.2f 未超过当前 %.2f，保持不变",
			metric, bestScore, baseScore)
		return res
	}

	// 写回策略表
	res.Improved = true
	if apply {
		newConfig := cloneParams(baseParams)
		for k, v := range best.combo {
			newConfig[k] = v
		}
		newConfigJSON, err := json.Marshal(newConfig)
		if err != nil {
			res.Reason = fmt.Sprintf("序列化配置失败: %v", err)
			return res
		}
		now := time.Now()
		if err := t.sqliteManager.GetDB().Model(&data.Strategy{}).Where("id = ?", st.ID).Updates(map[string]interface{}{
			"config_json":   string(newConfigJSON),
			"sharpe_ratio":  best.metrics.SharpeRatio,
			"max_drawdown":  best.metrics.MaxDrawdown,
			"total_return":  best.metrics.TotalReturn,
			"win_rate":      best.metrics.WinRate,
			"last_tuned_at": now,
			"updated_at":    now,
		}).Error; err != nil {
			res.Reason = fmt.Sprintf("写回策略表失败: %v", err)
			return res
		}
		res.Applied = true
		res.Reason = fmt.Sprintf("%s %.2f→%.2f (交易 %d→%d)",
			metric, baseScore, bestScore, oldMetrics.Trades, best.metrics.Trades)
	} else {
		res.Reason = fmt.Sprintf("apply=false，仅评估：%s %.2f→%.2f (交易 %d→%d)",
			metric, baseScore, bestScore, oldMetrics.Trades, best.metrics.Trades)
	}

	log.Printf("[StrategyTuner] %s(%s): %s", st.Name, st.StrategyType, res.Reason)
	return res
}

// getBenchmarkBars 获取基准指数K线（优先沪深300，回退上证指数），返回正序K线
func (t *StrategyTunerTool) getBenchmarkBars(ctx context.Context) ([]tdx.KlineBar, string, error) {
	for _, idx := range []string{"sh000300", "sh000001"} {
		raw, err := t.duckdbManager.GetIndexKlineFromStock(ctx, idx, 500)
		if err != nil || len(raw) == 0 {
			continue
		}
		result := make([]tdx.KlineBar, len(raw))
		for i, bar := range raw {
			result[i] = tdx.KlineBar{
				Date:   bar.Date.Format("2006-01-02"),
				Open:   bar.Open,
				High:   bar.High,
				Low:    bar.Low,
				Close:  bar.Close,
				Volume: int64(bar.Volume),
				Amount: bar.Amount,
			}
		}
		for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
			result[i], result[j] = result[j], result[i]
		}
		return result, idx, nil
	}
	return nil, "", fmt.Errorf("无可用基准指数数据（sh000300/sh000001）")
}

// parseTuneRanges 解析策略表的调参范围JSON
func parseTuneRanges(jsonStr string) backtest.TuneRanges {
	if jsonStr == "" || jsonStr == "{}" {
		return nil
	}
	var ranges backtest.TuneRanges
	if err := json.Unmarshal([]byte(jsonStr), &ranges); err != nil {
		return nil
	}
	return ranges
}

// toMetrics 将回测结果转为指标快照
func toMetrics(r *backtest.BacktestResult, capital float64) *strategyMetrics {
	totalReturn := 0.0
	if capital > 0 && r != nil {
		totalReturn = (r.FinalCapital - capital) / capital * 100
	}
	return &strategyMetrics{
		SharpeRatio: r.SharpeRatio,
		TotalReturn: totalReturn,
		MaxDrawdown: r.MaxDrawdown,
		WinRate:     r.WinRate,
		Trades:      r.TotalTrades,
	}
}

// metricScore 按优化目标打分
func metricScore(m *strategyMetrics, metric string) float64 {
	if m == nil {
		return -1e9
	}
	switch metric {
	case "return":
		return m.TotalReturn
	case "win_rate":
		return m.WinRate
	default:
		return m.SharpeRatio
	}
}

// benchmarkCapital 计算能买入基准指数最高价1手的基准资金
func benchmarkCapital(bars []tdx.KlineBar) float64 {
	maxClose := 0.0
	for _, b := range bars {
		if b.Close > maxClose {
			maxClose = b.Close
		}
	}
	capital := 100.0 * maxClose * 1.05
	if capital < 100000 {
		capital = 100000
	}
	return capital
}

// cloneParams 深拷贝参数map
func cloneParams(params map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(params))
	for k, v := range params {
		out[k] = v
	}
	return out
}

// ==================== 个股滚动调参（按持仓自身K线） ====================
// tune_strategy_params 的指数级调参用全局基准指数K线，适合"选出策略表参数"，但当个股与指数
// 偏离较大时，那套参数套在该股上未必最优。TuneHeldStocks 改为对每只持仓用其自身近期日K线
// 为所有活跃策略各调一套"适配该股"的参数，写入 strategy_stock_params 覆盖表；操盘手之后在该股上
// 计算策略信号时优先取覆盖参数（无覆盖时回退全局 config_json）。Coverage 为用户选定的"所有活跃策略"。

// TuneHeldStocks 个股滚动调参入口：codes 为已规范化的持仓符号（如 sh600519），
// windowDays 为回看交易日窗口（默认250）。成功写入覆盖表返回匹配对数。
func (t *StrategyTunerTool) TuneHeldStocks(codes []string, windowDays int) (int, error) {
	if t.sqliteManager == nil || t.duckdbManager == nil || !t.duckdbManager.HasStockDB() {
		return 0, fmt.Errorf("SQLite/DuckDB 不可用，无法进行个股滚动调参")
	}
	if windowDays < 100 {
		windowDays = 250
	}
	db := t.sqliteManager.GetDB()

	var strategies []data.Strategy
	if err := db.Where("is_active = 1").Order("id ASC").Find(&strategies).Error; err != nil {
		return 0, fmt.Errorf("查询活跃策略失败: %w", err)
	}
	if len(strategies) == 0 {
		return 0, nil
	}

	const (
		metric    = "sharpe"
		minTrades = 8
		maxCombos = 100
	)

	done := 0
	for _, raw := range codes {
		code := strings.ToLower(strings.TrimSpace(raw))
		if code == "" {
			continue
		}
		bars, err := backtest.GetKlineFromDuckDB(t.duckdbManager, code, windowDays)
		if err != nil || len(bars) < 50 {
			log.Printf("[StrategyTuner] 个股策略绑定调参跳过 %s: K线数据不足", code)
			continue
		}
		capital := benchmarkCapital(bars)
		// 对每股在全部活跃策略内各自调参，选个股上表现最佳的一套作为该股的绑定策略，写一行。
		var best *data.StrategyStockParam
		for i := range strategies {
			sp := t.tuneStockOne(code, &strategies[i], bars, capital, metric, minTrades, maxCombos)
			if sp == nil {
				continue
			}
			if best == nil || sp.SharpeRatio > best.SharpeRatio {
				best = sp
			}
		}
		if best == nil {
			log.Printf("[StrategyTuner] 个股策略绑定跳 %s: 无任何策略在个股上可选", code)
			continue
		}
		if err := t.upsertStockParam(db, best); err != nil {
			log.Printf("[StrategyTuner] 写个股绑定失败 %s/%s: %v", code, best.StrategyType, err)
			continue
		}
		done++
		log.Printf("[StrategyTuner] 个股策略绑定 %s -> %s (夏普=%.3f)", code, best.StrategyType, best.SharpeRatio)
	}
	log.Printf("[StrategyTuner] 个股策略绑定完成：%d 只开盘个股已完成（输入 %d 只）", done, len(codes))
	return done, nil
}

// tuneStockOne 对单只个股×单套策略，用该股自身K线网格扫描最优参数；仅当最优参数在该股上
// 不劣于全局参数表现时返回覆盖参数，否则返回 nil（保持已有覆盖或回退全局）。
func (t *StrategyTunerTool) tuneStockOne(code string, st *data.Strategy, bars []tdx.KlineBar, capital float64, metric string, minTrades, maxCombos int) *data.StrategyStockParam {
	res := &data.StrategyStockParam{
		Code:         code,
		StrategyType: st.StrategyType,
		StrategyName: st.Name,
		WindowDays:   len(bars),
	}

	stopLoss := st.StopLossPct
	if stopLoss <= 0 {
		stopLoss = 0.05
	}
	takeProfit := st.TakeProfitPct
	if takeProfit <= 0 {
		takeProfit = 0.20
	}

	baseParams := backtest.ParseConfigJSON(st.ConfigJSON)
	ranges := parseTuneRanges(st.TuneRangesJSON)
	if len(ranges) == 0 {
		ranges = backtest.GetDefaultTuneRanges(st.StrategyType)
	}
	if len(ranges) == 0 {
		return nil
	}

	// 全局参数在该股上的表现（baseline）
	baseStrategy := backtest.BuildStrategyFromConfig(st.StrategyType, baseParams)
	if baseStrategy == nil {
		return nil
	}
	baseResult := backtest.RunBacktestWithLimitFin(bars, baseStrategy, capital, stopLoss, takeProfit, code, "", backtest.NewDuckDBFinancialProvider(t.duckdbManager))
	baseScore := metricScore(toMetrics(baseResult, capital), metric)

	combos := backtest.GenerateParamCombinations(ranges, maxCombos)
	type cand struct {
		combo map[string]interface{}
		m     *strategyMetrics
	}
	var best *cand
	for _, combo := range combos {
		params := cloneParams(baseParams)
		for k, v := range combo {
			params[k] = v
		}
		strategy := backtest.BuildStrategyFromConfig(st.StrategyType, params)
		if strategy == nil {
			continue
		}
		r := backtest.RunBacktestWithLimitFin(bars, strategy, capital, stopLoss, takeProfit, code, "", backtest.NewDuckDBFinancialProvider(t.duckdbManager))
		m := toMetrics(r, capital)
		if m.Trades < minTrades {
			continue
		}
		if best == nil || metricScore(m, metric) > metricScore(best.m, metric) {
			clone := cloneParams(combo)
			best = &cand{combo: clone, m: m}
		}
	}
	if best == nil {
		return nil
	}
	// 仅当该股最优参数不劣于全局参数在该股上的表现时才覆盖，避免劣化
	if metricScore(best.m, metric) < baseScore {
		return nil
	}

	newConfig := cloneParams(baseParams)
	for k, v := range best.combo {
		newConfig[k] = v
	}
	cfgJSON, err := json.Marshal(newConfig)
	if err != nil {
		return nil
	}
	res.ParamsJSON = string(cfgJSON)
	res.SharpeRatio = best.m.SharpeRatio
	res.TotalReturn = best.m.TotalReturn
	res.MaxDrawdown = best.m.MaxDrawdown
	res.WinRate = best.m.WinRate
	res.Trades = best.m.Trades
	res.UpdatedAt = time.Now()
	return res
}

// upsertStockParam 按 code 唯一键写个股绑定（每次覆盖当日为每股选出的最佳策略绑定）
func (t *StrategyTunerTool) upsertStockParam(db *gorm.DB, sp *data.StrategyStockParam) error {
	var existing data.StrategyStockParam
	err := db.Where("code = ?", sp.Code).First(&existing).Error
	if err == nil {
		sp.ID = existing.ID
		return db.Save(sp).Error
	}
	return db.Create(sp).Error
}
