package tools

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/quantpilot/quantpilot/internal/data"
	"github.com/quantpilot/quantpilot/internal/portfolio"
	"github.com/quantpilot/quantpilot/internal/util"
)

// ==================== MarketRegimeDetectTool ====================
// market_regime_detect 市场状态判定工具（CIO 决策前置）
// 复用 CIO detectMarketRegime 的确定性打分逻辑：基于上证/深证/创业板指数的
// 5/10/20 日收益、MA5/MA20、量能缩量信号，判定市场状态(牛/熊/震荡)并给出建议权益仓位。
// 数据全部来自 DuckDB 真实指数K线，严禁伪造。

// MarketRegimeDetectTool 市场状态判定工具
type MarketRegimeDetectTool struct {
	duckdbManager *data.DuckDBManager
	sqliteManager *data.SQLiteManager
}

// NewMarketRegimeDetectTool 创建市场状态判定工具
func NewMarketRegimeDetectTool(duckdbManager *data.DuckDBManager, sqliteManager *data.SQLiteManager) *MarketRegimeDetectTool {
	return &MarketRegimeDetectTool{duckdbManager: duckdbManager, sqliteManager: sqliteManager}
}

func (t *MarketRegimeDetectTool) Name() string { return "market_regime_detect" }

func (t *MarketRegimeDetectTool) Description() string {
	return "基于上证/深证/创业板指数的5/10/20日收益、均线、量能，确定性判定A股市场状态(BULLISH牛市/BEARISH熊市/NEUTRAL震荡)并给出建议权益仓位(80/50/20%)。CIO所有仓位决策的前置工具。"
}

func (t *MarketRegimeDetectTool) GetDefinition() ToolDefinition {
	return ToolDefinition{
		Type: "function",
		Function: FunctionDef{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"lookback_days": map[string]interface{}{
						"type":        "integer",
						"description": "指数K线回看交易日数，默认 60",
					},
				},
			},
		},
	}
}

// regimeIndexScore 单只指数的牛熊打分结果
type regimeIndexScore struct {
	Code      string
	Name      string
	Day5Ret   float64
	Day10Ret  float64
	Day20Ret  float64
	BullScore float64
	BearScore float64
}

func (t *MarketRegimeDetectTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	if t.duckdbManager == nil || !t.duckdbManager.HasStockDB() {
		return nil, fmt.Errorf("DuckDB不可用，无法判定市场状态")
	}

	lookback := 60
	if v, ok := args["lookback_days"].(float64); ok && v > 0 {
		lookback = int(v)
	}

	// 获取活跃市场指数（数据库优先，缺省三大指数）
	indices := []data.MarketIndex{
		{Code: "sh000001", Name: "上证指数"},
		{Code: "sz399001", Name: "深证成指"},
		{Code: "sz399006", Name: "创业板指"},
	}
	if t.sqliteManager != nil {
		if active := data.GetActiveMarketIndices(t.sqliteManager.GetDB()); len(active) > 0 {
			indices = active
		}
	}

	bullScore, bearScore := 0.0, 0.0
	indexStats := make([]map[string]interface{}, 0, len(indices))
	for _, idx := range indices {
		bars, err := t.duckdbManager.GetKlineFromStock(ctx, idx.Code, lookback)
		if err != nil || len(bars) < 10 {
			continue
		}
		n := len(bars)
		closes := make([]float64, n)
		for i, bar := range bars {
			closes[i] = bar.Close
		}
		if closes[1] <= 0 {
			continue
		}

		day5Ret, day10Ret, day20Ret := 0.0, 0.0, 0.0
		if n > 5 && closes[5] > 0 {
			day5Ret = (closes[0] - closes[5]) / closes[5] * 100
		}
		if n > 10 && closes[10] > 0 {
			day10Ret = (closes[0] - closes[10]) / closes[10] * 100
		}
		if n > 20 && closes[20] > 0 {
			day20Ret = (closes[0] - closes[20]) / closes[20] * 100
		}

		ma5 := 0.0
		if n >= 5 {
			for i := 0; i < 5; i++ {
				ma5 += closes[i]
			}
			ma5 /= 5
		}
		ma20 := 0.0
		if n >= 20 {
			for i := 0; i < 20; i++ {
				ma20 += closes[i]
			}
			ma20 /= 20
		}

		avgVol20 := 0.0
		if n >= 20 {
			for i := 0; i < 20; i++ {
				avgVol20 += bars[i].Volume
			}
			avgVol20 /= 20
		}
		volRatio := 1.0
		if avgVol20 > 0 && bars[0].Volume > 0 {
			volRatio = bars[0].Volume / avgVol20
		}
		volShrink := volRatio < 0.8

		weight := 1.0
		switch idx.Code {
		case "sh000001", "000300":
			weight = 1.5
		case "sz399001":
			weight = 1.2
		}

		idxBull, idxBear := 0.0, 0.0
		if day5Ret > 1.0 {
			idxBull += 2.0
		} else if day5Ret > 0 {
			idxBull += 0.5
		} else if day5Ret < -1.0 {
			idxBear += 2.0
		} else if day5Ret < 0 {
			idxBear += 0.5
		}
		if day10Ret > 2.0 {
			idxBull += 1.5
		} else if day10Ret > 0 {
			idxBull += 0.5
		} else if day10Ret < -2.0 {
			idxBear += 1.5
		} else if day10Ret < 0 {
			idxBear += 0.5
		}
		if day20Ret > 3.0 {
			idxBull += 1.5
		} else if day20Ret < -3.0 {
			idxBear += 1.5
		}
		if closes[0] > ma5 {
			idxBull += 0.5
		} else {
			idxBear += 0.5
		}
		if closes[0] > ma20 {
			idxBull += 0.5
		} else {
			idxBear += 0.5
		}
		// 缩量下跌 = 强烈看跌信号
		if day5Ret < 0 && volShrink {
			idxBear += 2.0
		}
		if day5Ret > 0 && volShrink {
			idxBear += 0.5
		}

		bullScore += weight * idxBull
		bearScore += weight * idxBear

		name := idx.Name
		if name == "" {
			name = idx.Code
		}
		indexStats = append(indexStats, map[string]interface{}{
			"code":        idx.Code,
			"name":        name,
			"day5_ret_pct":  round2(day5Ret),
			"day10_ret_pct": round2(day10Ret),
			"day20_ret_pct": round2(day20Ret),
			"close_vs_ma5":  closes[0] > ma5,
			"close_vs_ma20": closes[0] > ma20,
			"vol_ratio":     round2(volRatio),
		})
	}

	if len(indexStats) == 0 {
		return map[string]interface{}{
			"status": "TASK_BLOCKED",
			"reason": "指数K线数据不足，无法判定市场状态",
			"message": "行情数据缺失，禁止AI推断。请先通过数据维护模块同步指数行情数据。",
		}, nil
	}

	regime := "NEUTRAL"
	switch {
	case bullScore > bearScore*1.3:
		regime = "BULLISH"
	case bearScore > bullScore*1.3:
		regime = "BEARISH"
	}

	// 建议权益仓位（牛市80% / 震荡50% / 熊市20%），按分数差距给置信度
	equityRatio := 0.50
	switch regime {
	case "BULLISH":
		equityRatio = 0.80
	case "BEARISH":
		equityRatio = 0.20
	}
	confidence := 0.5
	total := bullScore + bearScore
	if total > 0 {
		confidence = 0.5 + absF(bullScore-bearScore)/total*0.5
	}
	if confidence > 0.95 {
		confidence = 0.95
	}

	desc := ""
	switch regime {
	case "BULLISH":
		desc = "多头占优：指数中期趋势向上、量价配合，可提高权益仓位进攻"
	case "BEARISH":
		desc = "空头占优：指数中期趋势向下或缩量下行，应降低权益仓位防守"
	case "NEUTRAL":
		desc = "震荡格局：多空力量均衡，维持中性仓位、等待方向明确"
	}

	return map[string]interface{}{
		"as_of":              time.Now().Format(time.RFC3339),
		"regime":             regime,
		"bull_score":         round2(bullScore),
		"bear_score":         round2(bearScore),
		"confidence":         round2(confidence),
		"suggested_equity_ratio": round2(equityRatio),
		"suggested_cash_ratio":   round2(1 - equityRatio),
		"description":        desc,
		"index_details":      indexStats,
	}, nil
}

func absF(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

// ==================== MandateComplianceTool ====================
// mandate_compliance_check Mandate 合规校验工具（Planner 核心职责）
// 基于组合实时状态（真实持仓/现金）与 Mandate 约束做确定性校验：
// 单票≤上限、现金≥下限、行业集中度、持仓数量与资金规模分档。
// 工具只计算合规事实，Planner 据此给出调整建议。

// MandateComplianceTool Mandate 合规校验工具
type MandateComplianceTool struct {
	engine *portfolio.Engine
}

// NewMandateComplianceTool 创建 Mandate 合规校验工具
func NewMandateComplianceTool(engine *portfolio.Engine) *MandateComplianceTool {
	return &MandateComplianceTool{engine: engine}
}

func (t *MandateComplianceTool) Name() string { return "mandate_compliance_check" }

func (t *MandateComplianceTool) Description() string {
	return "基于组合实时持仓/现金与 Mandate 约束做确定性合规校验：单票权重上限、最低现金比例、行业集中度上限、持仓数量与资金规模分档。返回各项 OK/WARNING/VIOLATION。Planner 据此做合规复盘与调仓建议。"
}

func (t *MandateComplianceTool) GetDefinition() ToolDefinition {
	return ToolDefinition{
		Type: "function",
		Function: FunctionDef{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"max_single_position_pct": map[string]interface{}{
						"type":        "number",
						"description": "单票权重上限(%)，默认 30",
					},
					"min_cash_ratio": map[string]interface{}{
						"type":        "number",
						"description": "最低现金比例(%)，默认 10",
					},
					"sector_limit_pct": map[string]interface{}{
						"type":        "number",
						"description": "单行业权重上限(%)，默认 25",
					},
				},
			},
		},
	}
}

// mandateCheckItem 单条合规检查结果
type mandateCheckItem struct {
	Item       string  `json:"item"`
	Status     string  `json:"status"` // OK / WARNING / VIOLATION
	Current    float64 `json:"current"`
	Limit      float64 `json:"limit"`
	Detail     string  `json:"detail"`
	Suggestion string  `json:"suggestion"`
}

func (t *MandateComplianceTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	if t.engine == nil {
		return nil, fmt.Errorf("组合引擎未初始化")
	}

	maxSinglePct := 30.0
	if v, ok := args["max_single_position_pct"].(float64); ok && v > 0 {
		maxSinglePct = v
	}
	minCashRatio := 10.0
	if v, ok := args["min_cash_ratio"].(float64); ok && v > 0 {
		minCashRatio = v
	}
	sectorLimitPct := 25.0
	if v, ok := args["sector_limit_pct"].(float64); ok && v > 0 {
		sectorLimitPct = v
	}

	snap := t.engine.GetSnapshot()
	if snap == nil {
		return nil, fmt.Errorf("组合快照不可用")
	}

	totalAssets := snap.TotalCapital
	if totalAssets <= 0 {
		totalAssets = snap.Cash + snap.TotalMarketValue
	}
	cashRatio := 0.0
	if totalAssets > 0 {
		cashRatio = snap.Cash / totalAssets * 100
	}

	loader := data.GetDictLoader()
	var checks []mandateCheckItem

	// 1. 单票集中度
	overMax := []map[string]interface{}{}
	for _, p := range snap.Positions {
		if p == nil || p.Weight <= 0 {
			continue
		}
		if p.Weight > maxSinglePct {
			overMax = append(overMax, map[string]interface{}{
				"code": p.InstrumentID, "name": p.StockName,
				"weight_pct": round2(p.Weight), "limit_pct": maxSinglePct,
			})
		}
	}
	status := "OK"
	detail := fmt.Sprintf("单票最大权重 %.2f%% ≤ 上限 %.0f%%", maxSingleWeight(snap.Positions), maxSinglePct)
	if len(overMax) > 0 {
		status = "VIOLATION"
		detail = fmt.Sprintf("有 %d 只持仓超过单票上限 %.0f%%", len(overMax), maxSinglePct)
	}
	checks = append(checks, mandateCheckItem{
		Item: "单票集中度", Status: status, Current: maxSingleWeight(snap.Positions),
		Limit: maxSinglePct, Detail: detail,
		Suggestion: "对超限标的减仓至上限以内",
	})

	// 2. 现金比例
	status = "OK"
	detail = fmt.Sprintf("现金比例 %.2f%% ≥ 下限 %.0f%%", cashRatio, minCashRatio)
	if cashRatio < minCashRatio {
		status = "WARNING"
		detail = fmt.Sprintf("现金比例 %.2f%% 低于下限 %.0f%%，机动资金不足", cashRatio, minCashRatio)
	}
	checks = append(checks, mandateCheckItem{
		Item: "最低现金比例", Status: status, Current: round2(cashRatio),
		Limit: minCashRatio, Detail: detail,
		Suggestion: "适当保留现金以应对补仓与回撤",
	})

	// 3. 行业集中度
	industryWeight := map[string]float64{}
	for _, p := range snap.Positions {
		if p == nil || p.Weight <= 0 {
			continue
		}
		ind := loader.GetIndustryByStock(p.InstrumentID)
		industryWeight[ind] += p.Weight
	}
	overIndustry := []map[string]interface{}{}
	for ind, w := range industryWeight {
		if w > sectorLimitPct {
			overIndustry = append(overIndustry, map[string]interface{}{
				"industry": ind, "weight_pct": round2(w), "limit_pct": sectorLimitPct,
			})
		}
	}
	sort.SliceStable(overIndustry, func(i, j int) bool {
		return overIndustry[i]["weight_pct"].(float64) > overIndustry[j]["weight_pct"].(float64)
	})
	status = "OK"
	detail = fmt.Sprintf("最大行业权重 %.2f%% ≤ 上限 %.0f%%", maxIndustryWeight(industryWeight), sectorLimitPct)
	if len(overIndustry) > 0 {
		status = "VIOLATION"
		detail = fmt.Sprintf("有 %d 个行业超过上限 %.0f%%", len(overIndustry), sectorLimitPct)
	}
	checks = append(checks, mandateCheckItem{
		Item: "行业集中度", Status: status, Current: round2(maxIndustryWeight(industryWeight)),
		Limit: sectorLimitPct, Detail: detail,
		Suggestion: "对超限行业的持仓进行分散再平衡",
	})

	// 4. 持仓数量 vs 资金规模分档
	maxCandidates, _ := util.PortfolioSizing(totalAssets)
	holdCount := 0
	for _, p := range snap.Positions {
		if p != nil && p.Weight > 0 {
			holdCount++
		}
	}
	status = "OK"
	detail = fmt.Sprintf("当前持仓 %d 只 ≤ 资金规模建议上限 %d 只(总资产 ¥%.0f)", holdCount, maxCandidates, totalAssets)
	if holdCount > maxCandidates {
		status = "WARNING"
		detail = fmt.Sprintf("当前持仓 %d 只超过资金规模建议 %d 只(总资产 ¥%.0f)，过度分散", holdCount, maxCandidates, totalAssets)
	}
	checks = append(checks, mandateCheckItem{
		Item: "持仓数量与规模匹配", Status: status, Current: float64(holdCount),
		Limit: float64(maxCandidates), Detail: detail,
		Suggestion: "按资金规模分档聚焦核心持仓",
	})

	// 汇总
	hasViolation := false
	hasWarning := false
	for _, c := range checks {
		if c.Status == "VIOLATION" {
			hasViolation = true
		}
		if c.Status == "WARNING" {
			hasWarning = true
		}
	}
	overall := "COMPLIANT"
	if hasViolation {
		overall = "VIOLATION"
	} else if hasWarning {
		overall = "WARNING"
	}

	return map[string]interface{}{
		"as_of":                time.Now().Format(time.RFC3339),
		"mandate":              map[string]interface{}{
			"max_single_position_pct": maxSinglePct,
			"min_cash_ratio":          minCashRatio,
			"sector_limit_pct":        sectorLimitPct,
		},
		"portfolio": map[string]interface{}{
			"total_assets":    round2(totalAssets),
			"cash":            round2(snap.Cash),
			"cash_ratio_pct":  round2(cashRatio),
			"holdings_count":  holdCount,
			"max_candidates":  maxCandidates,
		},
		"overall_status": overall,
		"checks":         checks,
		"over_limit_positions": overMax,
		"over_limit_industries": overIndustry,
	}, nil
}

func maxSingleWeight(positions []*portfolio.PositionState) float64 {
	m := 0.0
	for _, p := range positions {
		if p != nil && p.Weight > m {
			m = p.Weight
		}
	}
	return m
}

func maxIndustryWeight(m map[string]float64) float64 {
	mx := 0.0
	for _, v := range m {
		if v > mx {
			mx = v
		}
	}
	return mx
}

// ==================== SentimentMonitorTool ====================
// sentiment_monitor 市场情绪监控工具（Risk 盘后/盘中情绪判断）
// 真实数据来源：DuckDB 全市场涨跌家数 + 采样股票的涨跌停/连板高度。
// 涨停判定按板块涨跌幅口径（主板10%/创业板科创板20%/北交所30%），ST股(5%)未单独区分。

// SentimentMonitorTool 市场情绪监控工具
type SentimentMonitorTool struct {
	duckdbManager *data.DuckDBManager
}

// NewSentimentMonitorTool 创建市场情绪监控工具
func NewSentimentMonitorTool(duckdbManager *data.DuckDBManager) *SentimentMonitorTool {
	return &SentimentMonitorTool{duckdbManager: duckdbManager}
}

func (t *SentimentMonitorTool) Name() string { return "sentiment_monitor" }

func (t *SentimentMonitorTool) Description() string {
	return "基于真实行情数据监控A股市场情绪：全市场涨跌家数、采样涨跌停数量、连板高度(Top列表)。Risk做情绪面风险判断(如判断过热/冰点)。数据真实来自DuckDB。"
}

func (t *SentimentMonitorTool) GetDefinition() ToolDefinition {
	return ToolDefinition{
		Type: "function",
		Function: FunctionDef{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"market": map[string]interface{}{
						"type":        "string",
						"description": "市场：all(默认全A)/sh/sz",
						"enum":        []string{"all", "sh", "sz"},
					},
					"sample_size": map[string]interface{}{
						"type":        "integer",
						"description": "采样股票数量用于统计涨跌停/连板（默认300，上限500）",
					},
				},
			},
		},
	}
}

// limitRatioFor 按板块涨跌幅口径返回涨跌停比例
func limitRatioFor(code string) float64 {
	num := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(code)), "sh")
	num = strings.TrimPrefix(num, "sz")
	num = strings.TrimPrefix(num, "bj")
	num = strings.TrimPrefix(num, ".")
	if len(num) >= 3 {
		prefix := num[:3]
		if prefix == "688" || prefix == "300" || prefix == "301" {
			return 0.20
		}
		if strings.HasPrefix(num, "4") || strings.HasPrefix(num, "8") || strings.HasPrefix(num, "92") {
			return 0.30
		}
	}
	return 0.10
}

func (t *SentimentMonitorTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	if t.duckdbManager == nil || !t.duckdbManager.HasStockDB() {
		return nil, fmt.Errorf("DuckDB不可用，无法监控市场情绪")
	}

	market, _ := args["market"].(string)
	if market == "" {
		market = "all"
	}
	sampleSize := 300
	if v, ok := args["sample_size"].(float64); ok && v > 0 {
		sampleSize = int(v)
	}
	if sampleSize > 500 {
		sampleSize = 500
	}

	// 1. 全市场涨跌家数（真实、全量、廉价）
	breadth, err := t.duckdbManager.GetMarketBreadth(ctx, market)
	if err != nil {
		return nil, fmt.Errorf("获取市场涨跌家数失败: %w", err)
	}

	// 2. 采样股票涨跌停/连板高度
	symbols, err := t.duckdbManager.ListAllSymbolsFromStock(ctx, market, sampleSize)
	if err != nil || len(symbols) == 0 {
		return map[string]interface{}{
			"status": "TASK_BLOCKED",
			"reason": "无法获取采样股票列表",
			"message": "行情数据缺失，禁止AI推断。请先同步行情数据。",
		}, nil
	}

	codes := make([]string, 0, len(symbols))
	nameMap := make(map[string]string, len(symbols))
	for _, s := range symbols {
		codes = append(codes, s.Symbol)
		nameMap[s.Symbol] = s.Name
	}

	barsMap, err := t.duckdbManager.BatchGetFactorBars(ctx, codes, 8)
	if err != nil {
		return nil, fmt.Errorf("批量获取因子K线失败: %w", err)
	}

	loader := data.GetDictLoader()
	limitUpCount, limitDownCount := 0, 0
	type streakItem struct {
		code  string
		name  string
		streak int
		lastChg float64
	}
	streaks := []streakItem{}
	asOf := ""

	for _, code := range codes {
		bars := barsMap[code]
		if len(bars) < 2 {
			continue
		}
		// 升序排列（BatchGetFactorBars 返回 date DESC）
		sort.SliceStable(bars, func(i, j int) bool { return bars[i].Date.Before(bars[j].Date) })
		last := bars[len(bars)-1]
		if asOf == "" || last.Date.After(time.Time{}) {
			asOf = last.Date.Format("2006-01-02")
		}
		ratio := limitRatioFor(code)

		// 今日涨跌停（收盘价触及涨/跌停价）
		if last.Close >= last.PreClose*(1+ratio)-0.001 && last.PreClose > 0 {
			limitUpCount++
		}
		if last.Close <= last.PreClose*(1-ratio)+0.001 && last.PreClose > 0 {
			limitDownCount++
		}

		// 连板高度：从最新往前数连续涨停天数
		streak := 0
		for i := len(bars) - 1; i >= 1; i-- {
			b := bars[i]
			if b.PreClose <= 0 {
				break
			}
			if b.Close >= b.PreClose*(1+ratio)-0.001 {
				streak++
			} else {
				break
			}
		}
		if streak >= 2 {
			name := nameMap[code]
			if name == "" {
				name = loader.GetStockName(code)
			}
			streaks = append(streaks, streakItem{code: code, name: name, streak: streak, lastChg: last.PctChg})
		}
	}

	sort.SliceStable(streaks, func(i, j int) bool { return streaks[i].streak > streaks[j].streak })

	topStreaks := make([]map[string]interface{}, 0, len(streaks))
	for _, s := range streaks {
		topStreaks = append(topStreaks, map[string]interface{}{
			"code": s.code, "name": s.name,
			"consecutive_limit_up": s.streak, "last_pct_chg": round2(s.lastChg),
		})
		if len(topStreaks) >= 10 {
			break
		}
	}

	// 3. 情绪综合指数（0-100）：涨跌家数比 + 涨跌停差
	sentiment := 50.0
	if breadth.Total > 0 {
		advRatio := float64(breadth.Advancers) / float64(breadth.Total) * 100
		sentiment = advRatio
	}
	if limitUpCount+limitDownCount > 0 {
		limitDelta := float64(limitUpCount-limitDownCount) / float64(limitUpCount+limitDownCount) * 20
		sentiment += limitDelta
	}
	if sentiment < 0 {
		sentiment = 0
	}
	if sentiment > 100 {
		sentiment = 100
	}

	emotion := "中性"
	switch {
	case sentiment >= 70:
		emotion = "偏热（情绪亢奋，谨防高位风险）"
	case sentiment <= 30:
		emotion = "偏冷（情绪冰点，关注超跌机会）"
	}

	return map[string]interface{}{
		"as_of":          time.Now().Format(time.RFC3339),
		"data_date":      asOf,
		"market":         market,
		"sample_size":    len(codes),
		"market_breadth": map[string]interface{}{
			"total": breadth.Total, "advancers": breadth.Advancers,
			"decliners": breadth.Decliners, "flat": breadth.Flat,
		},
		"limit_up_count":   limitUpCount,
		"limit_down_count": limitDownCount,
		"top_consecutive_limit_up": topStreaks,
		"sentiment_score":  round2(sentiment),
		"emotion":          emotion,
		"note":             "涨跌停与连板基于采样股票按板块涨跌幅口径判定(主板10%/创业板科创板20%/北交所30%)，ST股(5%)未单独区分，仅供参考",
	}, nil
}

// ==================== StopLossMonitorTool ====================
// stop_loss_monitor 止损监控工具（Risk 实时风控）
// 基于组合真实持仓成本与现价，确定性列出浮亏超过阈值的持仓。只计算事实，不执行交易。

// StopLossMonitorTool 止损监控工具
type StopLossMonitorTool struct {
	engine *portfolio.Engine
}

// NewStopLossMonitorTool 创建止损监控工具
func NewStopLossMonitorTool(engine *portfolio.Engine) *StopLossMonitorTool {
	return &StopLossMonitorTool{engine: engine}
}

func (t *StopLossMonitorTool) Name() string { return "stop_loss_monitor" }

func (t *StopLossMonitorTool) Description() string {
	return "基于组合真实持仓成本与现价，确定性列出浮亏达到阈值的持仓(默认-8%)，返回每只的浮亏比例/浮亏金额/市值。Risk做止损预警，只计算事实不执行交易。"
}

func (t *StopLossMonitorTool) GetDefinition() ToolDefinition {
	return ToolDefinition{
		Type: "function",
		Function: FunctionDef{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"threshold_pct": map[string]interface{}{
						"type":        "number",
						"description": "浮亏阈值(%)，默认 -8，即浮亏达到该比例即提示",
					},
				},
			},
		},
	}
}

func (t *StopLossMonitorTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	if t.engine == nil {
		return nil, fmt.Errorf("组合引擎未初始化")
	}
	threshold := -8.0
	if v, ok := args["threshold_pct"].(float64); ok && v < 0 {
		threshold = v
	}

	snap := t.engine.GetSnapshot()
	if snap == nil {
		return nil, fmt.Errorf("组合快照不可用")
	}

	type hit struct {
		Code   string
		Name   string
		Qty    int
		Cost   float64
		Price  float64
		RetPct float64
		PnL    float64
		Value  float64
	}
	hits := []hit{}
	for _, p := range snap.Positions {
		if p == nil || p.AvgCost <= 0 || p.CurrentPrice <= 0 {
			continue
		}
		retPct := (p.CurrentPrice - p.AvgCost) / p.AvgCost * 100
		if retPct <= threshold {
			hits = append(hits, hit{
				Code: p.InstrumentID, Name: p.StockName, Qty: p.Quantity,
				Cost: p.AvgCost, Price: p.CurrentPrice,
				RetPct: retPct, PnL: p.UnrealizedPnL, Value: p.MarketValue,
			})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].RetPct < hits[j].RetPct })

	result := make([]map[string]interface{}, 0, len(hits))
	for _, h := range hits {
		result = append(result, map[string]interface{}{
			"code": h.Code, "name": h.Name, "quantity": h.Qty,
			"avg_cost": round2(h.Cost), "current_price": round2(h.Price),
			"loss_pct": round2(h.RetPct), "loss_amount": round2(h.PnL),
			"market_value": round2(h.Value),
		})
	}

	return map[string]interface{}{
		"as_of":         time.Now().Format(time.RFC3339),
		"threshold_pct": threshold,
		"hit_count":     len(result),
		"positions_at_stop_loss": result,
		"note":          "浮亏=(现价-成本)/成本×100%，达到阈值即预警，实际是否止损由CIO决策",
	}, nil
}

// ==================== DecisionLogTool ====================
// decision_log 决策日志回溯工具（CIO 盘后复盘）
// 查询数据库中的决策轨迹(DecisionTrace)与智能体决策(AgentDecision)记录，供复盘回溯。

// DecisionLogTool 决策日志工具
type DecisionLogTool struct {
	sqliteManager *data.SQLiteManager
}

// NewDecisionLogTool 创建决策日志工具
func NewDecisionLogTool(sqliteManager *data.SQLiteManager) *DecisionLogTool {
	return &DecisionLogTool{sqliteManager: sqliteManager}
}

func (t *DecisionLogTool) Name() string { return "decision_log" }

func (t *DecisionLogTool) Description() string {
	return "查询数据库中CIO决策轨迹(市场状态/目标仓位/风险门禁/最终决策)与各Agent决策记录(决策类型/置信度/结论)，用于盘后复盘回溯决策链路。"
}

func (t *DecisionLogTool) GetDefinition() ToolDefinition {
	return ToolDefinition{
		Type: "function",
		Function: FunctionDef{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"limit": map[string]interface{}{
						"type":        "integer",
						"description": "返回最近记录数，默认 20，上限 100",
					},
					"agent_id": map[string]interface{}{
						"type":        "string",
						"description": "按Agent过滤(如 agent-cio/agent-risk)，可选",
					},
				},
			},
		},
	}
}

func (t *DecisionLogTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	if t.sqliteManager == nil {
		return nil, fmt.Errorf("数据库未初始化")
	}
	limit := 20
	if v, ok := args["limit"].(float64); ok && v > 0 {
		limit = int(v)
	}
	if limit > 100 {
		limit = 100
	}
	agentID, _ := args["agent_id"].(string)

	db := t.sqliteManager.GetDB()

	// 决策轨迹（CIO 层面）
	var traces []data.DecisionTrace
	q := db.Order("created_at DESC").Limit(limit)
	if err := q.Find(&traces).Error; err != nil {
		return nil, fmt.Errorf("查询决策轨迹失败: %w", err)
	}
	traceList := make([]map[string]interface{}, 0, len(traces))
	for _, tr := range traces {
		traceList = append(traceList, map[string]interface{}{
			"trace_id": tr.TraceID, "decision_date": tr.DecisionDate.Format("2006-01-02 15:04:05"),
			"market_regime": tr.MarketRegime, "market_confidence": tr.MarketConfidence,
			"target_exposure_pct": tr.TargetExposurePct,
			"risk_gate_passed":    tr.RiskGatePassed == 1,
			"final_decision":      tr.FinalDecision,
			"created_at":          tr.CreatedAt.Format("2006-01-02 15:04:05"),
		})
	}

	// Agent 决策记录
	var agentDecisions []data.AgentDecision
	q2 := db.Order("created_at DESC").Limit(limit)
	if agentID != "" {
		q2 = q2.Where("agent_id = ?", agentID)
	}
	if err := q2.Find(&agentDecisions).Error; err != nil {
		return nil, fmt.Errorf("查询Agent决策失败: %w", err)
	}
	decisionList := make([]map[string]interface{}, 0, len(agentDecisions))
	for _, d := range agentDecisions {
		decisionList = append(decisionList, map[string]interface{}{
			"agent_id": d.AgentID, "decision_type": d.DecisionType,
			"confidence": d.Confidence, "interpretation": d.Interpretation,
			"decision_trace_id": d.DecisionTraceID,
			"created_at":        d.CreatedAt.Format("2006-01-02 15:04:05"),
		})
	}

	log.Printf("[DecisionLogTool] 返回 %d 条决策轨迹, %d 条Agent决策", len(traceList), len(decisionList))

	return map[string]interface{}{
		"as_of":               time.Now().Format(time.RFC3339),
		"decision_traces":     traceList,
		"agent_decisions":     decisionList,
		"trace_count":         len(traceList),
		"agent_decision_count": len(decisionList),
	}, nil
}
