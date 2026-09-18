package backtest

import (
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/quantpilot/quantpilot/internal/data"
	"github.com/quantpilot/quantpilot/internal/tdx"
)

// ==================== 策略参数配置（自动化调参支撑） ====================
// 策略参数不写死在策略实现中：当前生效参数存于 strategies.config_json，
// 可调参数的扫描范围存于 strategies.tune_ranges_json，由策略表作为单一数据源。
// 本文件提供：DB配置 → 策略实例的构造（BuildStrategyFromConfig）、
// 参数网格候选值生成（GenerateParamValues）、按配置回测入口（RunBacktestWithStrategy）。

// ParamRange 单个可调参数的扫描范围
type ParamRange struct {
	Type string  `json:"type"` // "int" | "float"
	Min  float64 `json:"min"`
	Max  float64 `json:"max"`
	Step float64 `json:"step"`
}

// TuneRanges 策略全部可调参数的扫描范围（对应 strategies.tune_ranges_json）
type TuneRanges map[string]ParamRange

// strategyParamFieldMap JSON配置字段名 → 策略struct字段名
// 与 GetStrategyByType / BuiltinStrategies.ConfigJSON 保持一致（统一策略表单一来源）
var strategyParamFieldMap = map[string]map[string]string{
	"ma_cross":           {"fast_ma": "FastPeriod", "slow_ma": "SlowPeriod"},
	"expma_cross":        {"fast_ema": "FastPeriod", "slow_ema": "SlowPeriod"},
	"bollinger_breakout": {"period": "Period", "std_dev": "K"},
	"rsi_reversal":       {"period": "Period", "oversold": "OverSold", "overbought": "Overbought"},
	"kdj_golden":         {"k_period": "KPeriod", "d_period": "DPeriod", "j_oversold": "JOverSold", "j_overbought": "JOverbought"},
	"turtle_breakout":    {"entry_period": "EntryPeriod", "exit_period": "ExitPeriod"},
	"mfi_volume":         {"period": "Period", "oversold": "OverSold", "overbought": "Overbought"},
	"mtm_momentum":       {"mtm_ma_period": "Period"},
	"dmi_trend":          {"adx_period": "Period"},
	"cci_breakout":       {"period": "Period"},
	"trix_cross":         {"period": "Period"},
	"zhuoyao_momentum":   {"short_period": "ShortPeriod", "mid_period": "MidPeriod", "long_period": "LongPeriod", "trend_period": "TrendPeriod"},
	"ml_model":           {"buy_threshold": "BuyThreshold", "sell_threshold": "SellThreshold", "model_names": "ModelNames", "ensemble_method": "EnsembleMethod"},
	"super_trend":        {"period": "Period", "multiplier": "Multiplier"},
	"aroon":              {"period": "Period"},
	"hma":                {"period": "Period"},
	"cmf":                {"period": "Period", "buy_threshold": "BuyThreshold", "sell_threshold": "SellThreshold"},
}

// defaultTuneRanges 每个策略默认的可调参数扫描范围（播种时写入 tune_ranges_json）
var defaultTuneRanges = map[string]TuneRanges{
	"ma_cross": {
		"fast_ma": {Type: "int", Min: 3, Max: 21, Step: 2},
		"slow_ma": {Type: "int", Min: 20, Max: 140, Step: 20},
	},
	"expma_cross": {
		"fast_ema": {Type: "int", Min: 5, Max: 25, Step: 5},
		"slow_ema": {Type: "int", Min: 30, Max: 150, Step: 15},
	},
	"bollinger_breakout": {
		"period":  {Type: "int", Min: 10, Max: 30, Step: 5},
		"std_dev": {Type: "float", Min: 1.5, Max: 3.0, Step: 0.5},
	},
	"rsi_reversal": {
		"period":     {Type: "int", Min: 5, Max: 20, Step: 3},
		"oversold":   {Type: "float", Min: 15, Max: 35, Step: 5},
		"overbought": {Type: "float", Min: 65, Max: 85, Step: 5},
	},
	"kdj_golden": {
		"k_period":     {Type: "int", Min: 5, Max: 15, Step: 2},
		"d_period":     {Type: "int", Min: 2, Max: 5, Step: 1},
		"j_oversold":   {Type: "float", Min: 15, Max: 35, Step: 5},
		"j_overbought": {Type: "float", Min: 65, Max: 85, Step: 5},
	},
	"turtle_breakout": {
		"entry_period": {Type: "int", Min: 20, Max: 60, Step: 10},
		"exit_period":  {Type: "int", Min: 5, Max: 20, Step: 5},
	},
	"mfi_volume": {
		"period":     {Type: "int", Min: 10, Max: 20, Step: 2},
		"oversold":   {Type: "float", Min: 20, Max: 40, Step: 5},
		"overbought": {Type: "float", Min: 60, Max: 80, Step: 5},
	},
	"mtm_momentum": {
		"mtm_ma_period": {Type: "int", Min: 10, Max: 30, Step: 5},
	},
	"dmi_trend": {
		"adx_period": {Type: "int", Min: 10, Max: 20, Step: 2},
	},
	"cci_breakout": {
		"period": {Type: "int", Min: 10, Max: 20, Step: 2},
	},
	"trix_cross": {
		"period": {Type: "int", Min: 10, Max: 20, Step: 2},
	},
	"zhuoyao_momentum": {
		"short_period": {Type: "int", Min: 10, Max: 30, Step: 5},
		"mid_period":   {Type: "int", Min: 40, Max: 120, Step: 20},
		"long_period":  {Type: "int", Min: 90, Max: 240, Step: 30},
		"trend_period": {Type: "int", Min: 10, Max: 30, Step: 5},
	},
	"ml_model": {
		"buy_threshold":  {Type: "float", Min: 0.45, Max: 0.65, Step: 0.05},
		"sell_threshold": {Type: "float", Min: 0.25, Max: 0.45, Step: 0.05},
	},
	"super_trend": {
		"period":     {Type: "int", Min: 8, Max: 20, Step: 2},
		"multiplier": {Type: "float", Min: 2.0, Max: 4.0, Step: 0.5},
	},
	"aroon": {
		"period": {Type: "int", Min: 15, Max: 40, Step: 5},
	},
	"hma": {
		"period": {Type: "int", Min: 10, Max: 40, Step: 5},
	},
	"cmf": {
		"period":         {Type: "int", Min: 10, Max: 30, Step: 5},
		"buy_threshold":  {Type: "float", Min: 0.03, Max: 0.10, Step: 0.01},
		"sell_threshold": {Type: "float", Min: -0.10, Max: -0.03, Step: 0.01},
	},
}

// GetDefaultTuneRanges 返回策略默认调参范围（未配置时使用）
func GetDefaultTuneRanges(strategyType string) TuneRanges {
	ranges := defaultTuneRanges[strategyType]
	if ranges == nil {
		return TuneRanges{}
	}
	return ranges
}

// GetParamFieldMap 返回策略参数映射（JSON字段→struct字段）
func GetParamFieldMap(strategyType string) map[string]string {
	return strategyParamFieldMap[strategyType]
}

// BuildStrategyFromConfig 按DB配置参数构造策略实例（参数不写死，从策略表 config_json 读取）
func BuildStrategyFromConfig(strategyType string, params map[string]interface{}) Strategy {
	st := GetStrategyByType(strategyType)
	if st == nil {
		return nil
	}
	applyStrategyParams(st, strategyType, params)
	return st
}

// ParseConfigJSON 解析策略配置JSON为 map（空配置返回nil）
func ParseConfigJSON(configJSON string) map[string]interface{} {
	if configJSON == "" {
		return nil
	}
	var params map[string]interface{}
	if err := json.Unmarshal([]byte(configJSON), &params); err != nil {
		return nil
	}
	return params
}

// applyStrategyParams 通过参数映射表将 config 中的参数写入策略实例字段
func applyStrategyParams(st Strategy, strategyType string, params map[string]interface{}) {
	fieldMap := strategyParamFieldMap[strategyType]
	if len(fieldMap) == 0 || len(params) == 0 {
		return
	}
	v := reflect.ValueOf(st)
	if v.Kind() != reflect.Ptr || v.IsNil() {
		return
	}
	elem := v.Elem()
	if elem.Kind() != reflect.Struct {
		return
	}
	for jsonKey, goField := range fieldMap {
		raw, ok := params[jsonKey]
		if !ok {
			continue
		}
		f := elem.FieldByName(goField)
		if !f.IsValid() || !f.CanSet() {
			continue
		}
		switch f.Kind() {
		case reflect.Int, reflect.Int32, reflect.Int64:
			switch n := raw.(type) {
			case float64:
				f.SetInt(int64(n))
			case int:
				f.SetInt(int64(n))
			case string:
				var iv int
				if _, err := fmt.Sscanf(n, "%d", &iv); err == nil {
					f.SetInt(int64(iv))
				}
			}
		case reflect.Float32, reflect.Float64:
			switch n := raw.(type) {
			case float64:
				f.SetFloat(n)
			case int:
				f.SetFloat(float64(n))
			case string:
				var fv float64
				if _, err := fmt.Sscanf(n, "%f", &fv); err == nil {
					f.SetFloat(fv)
				}
			}
		case reflect.String:
			if n, ok := raw.(string); ok {
				f.SetString(n)
			}
		case reflect.Slice:
			if f.Type().Elem().Kind() == reflect.String {
				if arr, ok := raw.([]interface{}); ok {
					vals := make([]string, 0, len(arr))
					for _, item := range arr {
						if s, ok := item.(string); ok {
							vals = append(vals, s)
						}
					}
					f.Set(reflect.ValueOf(vals))
				}
			}
		}
	}
}

// GenerateParamValues 生成单个参数的一维网格候选值（按步长）
func GenerateParamValues(r ParamRange) []float64 {
	var vals []float64
	if r.Step <= 0 {
		return vals
	}
	for v := r.Min; v <= r.Max+1e-9; v += r.Step {
		vals = append(vals, v)
	}
	return vals
}

// GenerateParamCombinations 生成参数组合的笛卡尔积候选（限制最大组合数，超出则对每个参数均匀抽样）
func GenerateParamCombinations(ranges TuneRanges, maxCombos int) []map[string]interface{} {
	if len(ranges) == 0 {
		return nil
	}
	// 先计算总组合数
	total := 1
	paramCounts := make(map[string]int)
	for name, r := range ranges {
		cnt := len(GenerateParamValues(r))
		if cnt == 0 {
			return nil
		}
		paramCounts[name] = cnt
		total *= cnt
	}
	// 若组合数过多，按 maxCombos^(1/len) 对每维降采样
	names := make([]string, 0, len(ranges))
	for name := range ranges {
		names = append(names, name)
	}
	stepEach := make(map[string]int)
	if total > maxCombos {
		// 每维候选数的目标步长：使最终组合数≈maxCombos
		perDim := int(float64(maxCombos) / float64(len(names)))
		if perDim < 1 {
			perDim = 1
		}
		for _, name := range names {
			cnt := paramCounts[name]
			stepEach[name] = cnt / perDim
			if stepEach[name] < 1 {
				stepEach[name] = 1
			}
		}
	} else {
		for _, name := range names {
			stepEach[name] = 1
		}
	}

	var combos []map[string]interface{}
	var dfs func(idx int, cur map[string]interface{})
	dfs = func(idx int, cur map[string]interface{}) {
		if idx == len(names) {
			combo := make(map[string]interface{}, len(cur))
			for k, v := range cur {
				combo[k] = v
			}
			combos = append(combos, combo)
			return
		}
		name := names[idx]
		vals := GenerateParamValues(ranges[name])
		step := stepEach[name]
		for i := 0; i < len(vals); i += step {
			cur[name] = paramValue(ranges[name], vals[i])
			dfs(idx+1, cur)
		}
	}
	dfs(0, make(map[string]interface{}))
	return combos
}

// paramValue 按参数类型把 float64 候选值转成对应 Go 值
func paramValue(r ParamRange, v float64) interface{} {
	if r.Type == "int" {
		return int(v)
	}
	return v
}

// RunBacktestWithStrategy 使用自定义策略实例运行回测（参数来自策略表配置）。
// 涨跌停幅度默认按主板 ±10% 处理；如需按标的市场精确推断，请使用 RunBacktestWithLimit。
func RunBacktestWithStrategy(bars []tdx.KlineBar, strategy Strategy, initialCapital, stopLoss, takeProfit float64) *BacktestResult {
	return RunBacktestWithLimit(bars, strategy, initialCapital, stopLoss, takeProfit, "", "")
}

// RunBacktestWithLimit 在 RunBacktestWithStrategy 基础上，按标的代码/名称自动推断 A 股涨跌停幅度
// （主板±10%、创业板/科创板±20%、北交所±30%、ST±5%），使回测符合真实涨跌停成交约束。
// code/name 为空时回退默认 ±10%。
func RunBacktestWithLimit(bars []tdx.KlineBar, strategy Strategy, initialCapital, stopLoss, takeProfit float64, code, name string) *BacktestResult {
	engine := NewBacktestEngine(bars, strategy, initialCapital)
	// 消除前视偏差：买卖信号以次日开盘价成交。
	engine.ExecutionModel = ExecNextOpen
	engine.LimitPct = LimitPctForSymbol(code, name)
	if stopLoss > 0 {
		engine.StopLoss = stopLoss
	}
	if takeProfit > 0 {
		engine.TakeProfit = takeProfit
	}
	return engine.Run()
}

// RunBacktestWithLimitFin 在 RunBacktestWithLimit 基础上，注入财务数据提供者与标的代码，
// 使基本面因子策略在回测中能按披露日对齐读取真实财务数据（无未来函数）。
// provider 为 nil 时等价于 RunBacktestWithLimit（不注入财务数据）。
func RunBacktestWithLimitFin(bars []tdx.KlineBar, strategy Strategy, initialCapital, stopLoss, takeProfit float64, code, name string, provider FinancialProvider) *BacktestResult {
	engine := NewBacktestEngine(bars, strategy, initialCapital)
	// 消除前视偏差：买卖信号以次日开盘价成交（ExecNextOpen），避免"当日收盘信号当日收盘成交"的乐观偏差。
	engine.ExecutionModel = ExecNextOpen
	engine.LimitPct = LimitPctForSymbol(code, name)
	if stopLoss > 0 {
		engine.StopLoss = stopLoss
	}
	if takeProfit > 0 {
		engine.TakeProfit = takeProfit
	}
	if provider != nil {
		engine.FinancialProvider = provider
		engine.FinancialSymbol = code
	}
	return engine.Run()
}

// RunBacktestWithParams 使用 K 线数据、自定义止损止盈运行回测（默认策略参数）
func RunBacktestWithParams(
	bars []tdx.KlineBar,
	strategyType string,
	initialCapital, stopLoss, takeProfit float64,
) *BacktestResult {
	strategy := GetStrategyByType(strategyType)
	if strategy == nil {
		return nil
	}
	return RunBacktestWithStrategy(bars, strategy, initialCapital, stopLoss, takeProfit)
}

// RunBacktestWithParamsFin 在 RunBacktestWithParams 基础上注入财务数据提供者与标的代码，
// 使基本面因子策略在回测中能按披露日对齐读取真实财务数据（无未来函数）。
// duckdbMgr 为 nil 或不可用时回退普通回测（不注入财务数据）。
func RunBacktestWithParamsFin(
	bars []tdx.KlineBar,
	strategyType string,
	initialCapital float64,
	duckdbMgr *data.DuckDBManager,
	code string,
) *BacktestResult {
	strategy := GetStrategyByType(strategyType)
	if strategy == nil {
		return nil
	}
	return RunBacktestWithLimitFin(bars, strategy, initialCapital, 0.05, 0.20, code, "", financialProviderFor(duckdbMgr))
}

// ==================== 次日交易策略选择（策略驱动减仓共用） ====================
// 量化分析师盘后做因子复盘后，从策略表选定次日交易策略写入 daily_strategy_plans 表；
// 操盘手次日盘前/盘中按该策略信号执行（如 KDJ 死叉卖出）。本文件提供纯函数评分与
// 选择逻辑，供 tools.DailyStrategyTool 与 CIO 共用，杜绝两处逻辑不一致。

// StrategySelectionScore 对单个策略行按回测真实指标计算综合分（确定性规则）：
//   - 有回测指标时：综合分 = 0.4×总收益率% + 0.3×夏普比率 + 0.3×胜率%；
//   - 无任何回测指标时返回 0（不会入选）。
//
// 指标全部来自策略服务每日真实回测写入（严禁伪造）。
func StrategySelectionScore(st *data.Strategy) float64 {
	if st == nil {
		return 0
	}
	if st.TotalReturn == 0 && st.SharpeRatio == 0 && st.WinRate == 0 {
		return 0
	}
	return 0.4*st.TotalReturn + 0.3*st.SharpeRatio + 0.3*st.WinRate
}

// SelectBestStrategy 从策略列表中按综合分选出最优策略行（仅活跃、有回测指标的策略）。
// 返回选中的策略行与类型；无候选时返回空行与空类型。
func SelectBestStrategy(list []data.Strategy) (data.Strategy, string) {
	var best data.Strategy
	bestScore := -1e18
	for i := range list {
		st := list[i]
		if st.StrategyType == "" || st.IsActive != 1 {
			continue
		}
		if st.TotalReturn == 0 && st.SharpeRatio == 0 && st.WinRate == 0 {
			continue
		}
		score := StrategySelectionScore(&st)
		if score > bestScore {
			bestScore = score
			best = st
		}
	}
	if best.StrategyType == "" {
		return data.Strategy{}, ""
	}
	return best, best.StrategyType
}
