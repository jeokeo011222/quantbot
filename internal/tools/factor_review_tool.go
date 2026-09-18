package tools

import (
	"context"
	"fmt"
	"log"
	"math"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/quantpilot/quantpilot/internal/data"
	"github.com/quantpilot/quantpilot/internal/factors"
	"github.com/quantpilot/quantpilot/internal/screener"
	"github.com/quantpilot/quantpilot/internal/sentiment"
)

// ==================== FactorReviewTool 因子复盘工具 ====================
// factor_review 供 Quant 复盘阶段量化评价因子表现。
// 职责：基于真实K线，确定性计算各因子在横截面上的 IC / RankIC / 分组表现 /
// 覆盖率 / 准确率 / 稳定性 / 衰减。只计算事实，不做是否调整策略的结论。
// 数据全部来自 DuckDB 真实行情，严禁伪造。

// FactorReviewTool 因子复盘工具
type FactorReviewTool struct {
	duckdbManager   *data.DuckDBManager
	sqliteManager   *data.SQLiteManager
	tradeablePool   *screener.TradeablePool
	sentimentEngine *sentiment.Engine
}

// NewFactorReviewTool 创建因子复盘工具
func NewFactorReviewTool(duckdbManager *data.DuckDBManager, sqliteManager *data.SQLiteManager, tradeablePool *screener.TradeablePool, sentimentEngine *sentiment.Engine) *FactorReviewTool {
	return &FactorReviewTool{duckdbManager: duckdbManager, sqliteManager: sqliteManager, tradeablePool: tradeablePool, sentimentEngine: sentimentEngine}
}

func (t *FactorReviewTool) Name() string { return "factor_review" }

func (t *FactorReviewTool) Description() string {
	return "基于真实K线数据对因子做复盘评价：计算各因子的 IC/RankIC、分组收益差、覆盖率、准确率、稳定性、衰减。Quant复盘时用真实计算结果，勿自行估算因子表现。"
}

func (t *FactorReviewTool) GetDefinition() ToolDefinition {
	return ToolDefinition{
		Type: "function",
		Function: FunctionDef{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"symbols": map[string]interface{}{
						"type":        "array",
						"description": "可选：待复盘股票代码列表（如 sh600519 / 600519）。为空时默认取当前可交易股票池。",
						"items":       map[string]interface{}{"type": "string"},
					},
					"lookback_days": map[string]interface{}{
						"type":        "integer",
						"description": "回看交易日数（默认 120）",
					},
					"forward_days": map[string]interface{}{
						"type":        "integer",
						"description": "主评估的前瞻收益天数（默认 5）",
					},
				},
			},
		},
	}
}

// fSample 一个横截面样本：某只股票某因子的得分与前瞻收益
type fSample struct {
	code   string
	score  float64
	fwdRet float64
}

func (t *FactorReviewTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	if t.duckdbManager == nil {
		return nil, fmt.Errorf("DuckDB 未初始化，无法获取真实K线")
	}

	// 解析参数
	lookback := 120
	if v, ok := args["lookback_days"].(float64); ok && v > 0 {
		lookback = int(v)
	}
	forward := 5
	if v, ok := args["forward_days"].(float64); ok && v > 0 {
		forward = int(v)
	}
	maxHorizon := maxInt(forward, 20)

	// 确定股票宇宙 + 代码规范化
	symbols, names := t.resolveUniverse(ctx, args)
	if len(symbols) == 0 {
		return map[string]interface{}{
			"as_of":       time.Now().Format(time.RFC3339),
			"universe":    0,
			"note":        "未提供股票代码且可交易股票池为空，无法进行因子复盘（真实数据缺失，不推断）",
			"factor_list": []interface{}{},
		}, nil
	}

	// 一次性拉取足够长的K线（前瞻 horizon + 因子回看）
	ohlc := make([]string, 0, len(symbols))
	for _, s := range symbols {
		ohlc = append(ohlc, normalizeOhlcSymbol(s))
	}
	bars, err := t.duckdbManager.BatchGetFactorBars(ctx, ohlc, lookback+maxHorizon+10)
	if err != nil {
		// 股票宇宙整体无有效K线（如最近交易日 K 线未落库、代码缺数据）属数据不足，
		// 按“严禁伪造数据、降级处理”原则返回空复盘结果而非硬失败，避免整个因子复盘任务报错。
		if strings.Contains(err.Error(), "返回空结果") {
			log.Printf("[FactorReview] 股票宇宙无有效K线数据（%d 只），降级返回空复盘：%v", len(symbols), err)
			return map[string]interface{}{
				"as_of":       time.Now().Format(time.RFC3339),
				"universe":    len(symbols),
				"effective":   0,
				"note":        fmt.Sprintf("股票宇宙无任何有效K线数据（真实数据缺失，不推断）：%v", err),
				"factor_list": []interface{}{},
			}, nil
		}
		return nil, fmt.Errorf("获取K线失败: %w", err)
	}
	if len(bars) == 0 {
		return map[string]interface{}{
			"as_of":       time.Now().Format(time.RFC3339),
			"universe":    len(symbols),
			"effective":   0,
			"note":        "股票宇宙无任何有效K线数据（真实数据缺失，不推断）",
			"factor_list": []interface{}{},
		}, nil
	}

	// 预先并发计算每只股票的情绪因子得分（复用 CIO 的共享引擎缓存），
	// 供 sentiment_score 因子参与复盘评价（与选股权重表 factor_name 一致）
	sentiment := t.computeSentimentScores(ctx, symbols)

	// 计算各前瞻 horizon 的横截面样本（用于 IC 与衰减）
	perHorizon := map[int]map[string][]fSample{}
	horizons := uniqueHorizons(forward)
	for _, h := range horizons {
		perHorizon[h] = t.computeCrossSection(bars, names, h, sentiment)
	}
	mainSamples := perHorizon[forward]
	if mainSamples == nil {
		mainSamples = t.computeCrossSection(bars, names, forward, sentiment)
	}

	// 逐因子汇总
	factorList := make([]interface{}, 0, 8)
	for cat, samples := range mainSamples {
		if len(samples) < 6 {
			continue
		}
		item := t.buildFactorItem(cat, samples, perHorizon, len(bars))
		factorList = append(factorList, item)
	}
	sort.SliceStable(factorList, func(i, j int) bool {
		a := factorList[i].(map[string]interface{})
		b := factorList[j].(map[string]interface{})
		av, _ := a["abs_rank_ic"].(float64)
		bv, _ := b["abs_rank_ic"].(float64)
		return av > bv
	})

	// 把本次复盘得出的因子质量画像持久化，供选股阶段做自适应权重（好因子上调、差因子下调）
	t.persistFactorQuality(factorList)

	return map[string]interface{}{
		"as_of":               time.Now().Format(time.RFC3339),
		"universe":            len(symbols),
		"effective":           len(bars),
		"forward_days":        forward,
		"lookback_days":       lookback,
		"primary_rank_ic_avg": avgAbsRankIC(factorList),
		"factor_list":         factorList,
		"note":                "IC/RankIC/分组收益等均基于真实K线确定性计算；高|RankIC|且单调的因子方向性更强，仅描述发生了什么，不代替策略调整决策",
	}, nil
}

// canonicalOhlcSymbol 把可交易股票池的代码规整为 stock.ohlc 的标准小写带市场前缀形式（sh600206/sz002437）。
// 注意：池子入池时 StockCode 被存成 s.Market+s.Code（如 "SHsh603823" 这种双重前缀），而 Market 字段又独立存了 "SH"。
// 若直接 ToLower(Market)+StockCode 会拼出 "shSHsh603823"，永远匹配不上 ohlc 的 symbol，导致因子复盘整池 0 数据。
// 这里从 StockCode 剥离重复的市场前缀，并对缺少前缀的裸代码补上对应市场前缀。
func canonicalOhlcSymbol(market, stockCode string) string {
	code := strings.ToLower(stockCode)
	pref := strings.ToLower(market)
	if pref != "" {
		// 去掉重复前缀（shsh603823 -> sh603823）
		for strings.HasPrefix(code, pref+pref) {
			code = strings.TrimPrefix(code, pref)
		}
		// 裸代码补前缀（603823 -> sh603823）
		if !strings.HasPrefix(code, pref) {
			code = pref + code
		}
	}
	return code
}

// resolveUniverse 解析股票宇宙：优先显式 symbols，否则取可交易股票池
func (t *FactorReviewTool) resolveUniverse(ctx context.Context, args map[string]interface{}) ([]string, map[string]string) {
	names := map[string]string{}
	seen := map[string]bool{}
	var out []string

	if raw, ok := args["symbols"].([]interface{}); ok && len(raw) > 0 {
		for _, r := range raw {
			s, _ := r.(string)
			if s == "" || seen[s] {
				continue
			}
			seen[s] = true
			names[s] = s
			out = append(out, s)
		}
		return out, names
	}

	// 默认：可交易股票池（含待审/已批准/已买入）
	if t.tradeablePool != nil {
		stocks, err := t.tradeablePool.GetAllStocksWithFilter("", "", "", "score", 500)
		if err == nil {
			for _, st := range stocks {
				sym := canonicalOhlcSymbol(st.Market, st.StockCode)
				if seen[sym] {
					continue
				}
				seen[sym] = true
				names[sym] = st.StockName
				out = append(out, sym)
			}
		}
	}
	return out, names
}

// computeCrossSection 对给定 horizon，计算横截面上各因子得分与前瞻收益
func (t *FactorReviewTool) computeCrossSection(bars map[string][]data.FactorBar, names map[string]string, horizon int, sentiment map[string]float64) map[string][]fSample {
	out := map[string][]fSample{}
	for sym, symBars := range bars {
		if len(symBars) < horizon+20 {
			continue
		}
		// 用截至 t-horizon 的历史算因子得分，前瞻收益 = 最后horizon天收益
		lookEnd := len(symBars) - horizon // 因子计算用到第 lookEnd-1 根
		if lookEnd < 20 {
			continue
		}
		his := barsToSnapshots(sym, names[sym], symBars[:lookEnd])
		snap := snapshotFromBar(sym, names[sym], symBars[lookEnd-1])

		sf := factors.ComputeStockFactors(snap, his)
		// 补充 sentiment_score 因子（复用预先并发算好的情绪得分），使其参与复盘评价
		if sc, ok := sentiment[sym]; ok {
			sf.Factors = append(sf.Factors, factors.FactorScore{
				FactorName:  "sentiment_score",
				Category:    factors.CategorySentiment,
				Score:       sc,
				Weight:      0.0,
				Description: "复盘补充的公告情绪因子得分（复用CIO情绪引擎缓存）",
			})
		}
		base := symBars[lookEnd-1].Close
		if base <= 0 {
			continue
		}
		fwdRet := symBars[len(symBars)-1].Close/base - 1
		fwdRet = fwdRet * 100 // 百分比

		for _, fs := range sf.Factors {
			// 按因子名（momentum_20d/ep_ratio/sentiment_score...）分组，与选股默认权重表 GetDefaultFactorWeights 的key一致，
			// 便于将复盘质量分直接映射到各因子的选股权重。
			cat := fs.FactorName
			out[cat] = append(out[cat], fSample{code: sym, score: fs.Score, fwdRet: fwdRet})
		}
	}
	return out
}

// computeSentimentScores 并发计算股票宇宙的情绪因子得分，复用共享情绪引擎（自带1h缓存，线程安全）。
// 任一股票失败仅跳过该股票，不阻塞复盘整体流程。
func (t *FactorReviewTool) computeSentimentScores(ctx context.Context, symbols []string) map[string]float64 {
	out := map[string]float64{}
	if t.sentimentEngine == nil || len(symbols) == 0 {
		return out
	}
	sem := make(chan struct{}, 5)
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, sym := range symbols {
		wg.Add(1)
		go func(code string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			sctx, cancel := context.WithTimeout(ctx, 20*time.Second)
			defer cancel()
			res, err := t.sentimentEngine.ScoreWithContext(sctx, code)
			if err != nil {
				log.Printf("[FactorReview] sentiment 获取失败 %s: %v（该股票不参与情绪因子复盘）", code, err)
				return
			}
			mu.Lock()
			out[code] = res.Score
			mu.Unlock()
		}(sym)
	}
	wg.Wait()
	return out
}

// buildFactorItem 汇总单个因子的 IC/RankIC/分组/覆盖/准确率/稳定/衰减
func (t *FactorReviewTool) buildFactorItem(cat string, samples []fSample, perHorizon map[int]map[string][]fSample, totalUniverse int) map[string]interface{} {
	n := len(samples)
	coverage := float64(n) / float64(maxInt(totalUniverse, 1)) * 100

	pearsonIC := pearsonIC(samples)
	rankIC := spearmanIC(samples)

	// 分组收益：按得分三等分，对比最上1/3 与最下 1/3 前瞻收益
	topRet, bottomRet, spread := groupSpread(samples)
	// 准确率：得分最高的前1/3 中，前瞻收益为正的比例
	accuracy := topAccuracy(samples)
	// 稳定性：因子得分横截面离散度（标准差），越大说明区分度越高
	stability := scoreStd(samples)
	// 衰减：RankIC(20日) - RankIC(1日)，越负说明短周期有效、长周期衰减
	decay := calcDecay(perHorizon, cat)

	// 正负向归一：本引擎所有因子均定义为“高得分=更优”方向，因此 RankIC 直接可比
	direction := "score_high_good"

	// 分半稳定性检验：两半段方向一致且都达到阈值才标记稳定候选（防止假阳性）
	halfIC, halfStable := splitHalfStability(samples)
	halfICKey := ""
	if len(halfIC) == 2 {
		halfICKey = fmt.Sprintf("%.4f/%.4f", halfIC[0], halfIC[1])
	}

	return map[string]interface{}{
		"category":           cat,
		"factor_name":        cat,
		"direction":          direction,
		"sample_count":       n,
		"coverage_pct":       round2(coverage),
		"pearson_ic":         round4(pearsonIC),
		"rank_ic":            round4(rankIC),
		"abs_rank_ic":        round4(absFloat(rankIC)),
		"top_group_ret":      round4(topRet),
		"bottom_group_ret":   round4(bottomRet),
		"long_short_spread":  round4(spread),
		"top_accuracy":       round4(accuracy),
		"stability_std":      round4(stability),
		"decay_20d_minus_1d": round4(decay),
		"split_half_ic":      halfICKey,
		"split_half_stable":  halfStable,
		"interpretation":     factorInterpretation(rankIC, spread, accuracy),
	}
}

// factorCalibrationAlpha EMA 平滑系数：新质量分 = 旧质量分×(1-α) + 本次原始质量分×α。
// 对标 PanWatch factor_calibration.blend（alpha=0.35），防止单日IC噪声导致质量分大幅跳变。
const factorCalibrationAlpha = 0.35

// stableMark 分半稳定性标记（供 Detail 审计展示）
func stableMark(stable bool) string {
	if stable {
		return " ✓稳定"
	}
	return " ✗不稳定"
}

// persistFactorQuality 把本次复盘得出的各因子质量画像持久化到 FactorQuality 表。
// 质量分在 [0,1]，由 |RankIC|、前瞻准确率、区分度(稳定性)、覆盖率、短周期衰减共同加权得出，
// 高值代表该因子近期在横截面上选股效果好。选股时用它对默认因子权重做自适应加权（好因子上调、差因子下调）。
// 校准机制（对标 PanWatch factor_calibration）：
//   - EMA 平滑：质量分 = 旧值×(1-α) + 本次原始值×α，避免单日IC噪声跳变；
//   - pin 覆盖：IsPinned=true 或 AutoCalibrate=false 时跳过质量分更新（只刷新原始观测指标）；
//   - 历史审计：每次实际发生的校准变化写入 FactorCalibrationHistory。
//
// 只回写本次真实计算出来的因子，其余因子质量分保持不变。
func (t *FactorReviewTool) persistFactorQuality(factorList []interface{}) {
	if t.sqliteManager == nil {
		return
	}
	db := t.sqliteManager.GetDB()
	if db == nil {
		return
	}
	now := time.Now()
	for _, raw := range factorList {
		item, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		name, _ := item["factor_name"].(string)
		if name == "" {
			continue
		}
		quality := factorQualityFromItem(item)
		rankIC := item["abs_rank_ic"].(float64)
		accuracy := item["top_accuracy"].(float64)
		stability := item["stability_std"].(float64)
		coverage := item["coverage_pct"].(float64)
		decay := item["decay_20d_minus_1d"].(float64)
		halfStable, _ := item["split_half_stable"].(bool)
		halfIC, _ := item["split_half_ic"].(string)
		detail := fmt.Sprintf("factor_review@%s |RankIC|=%.4f acc=%.2f std=%.3f cov=%.1f%% decay=%.4f half=%s%s",
			now.Format("2006-01-02"), rankIC, accuracy, stability, coverage, decay, halfIC, stableMark(halfStable))

		var existing data.FactorQuality
		if err := db.Where("factor_name = ?", name).First(&existing).Error; err != nil {
			// 首次落库：直接写入原始质量分（无历史可平滑），并记录 seeded 审计
			db.Create(&data.FactorQuality{
				FactorName:      name,
				Quality:         quality,
				Detail:          detail,
				RankIC:          rankIC,
				Accuracy:        accuracy,
				Coverage:        coverage,
				Stability:       stability,
				Decay:           decay,
				SplitHalfStable: halfStable,
				SplitHalfIC:     halfIC,
				AutoCalibrate:   true,
				UpdatedAt:       now,
			})
			db.Create(&data.FactorCalibrationHistory{
				FactorName: name, OldQuality: quality, NewQuality: quality,
				RankIC: rankIC, Accuracy: accuracy, Coverage: coverage,
				Stability: stability, Decay: decay, Reason: "seeded", CreatedAt: now,
			})
			continue
		}

		oldQuality := existing.Quality
		// 始终刷新最近一次真实观测指标（供审计/API展示），质量分是否更新取决于 pin 与 EMA
		existing.RankIC = rankIC
		existing.Accuracy = accuracy
		existing.Coverage = coverage
		existing.Stability = stability
		existing.Decay = decay
		existing.SplitHalfStable = halfStable
		existing.SplitHalfIC = halfIC
		existing.Detail = detail
		existing.UpdatedAt = now

		// pin 覆盖：固定/停用自动校准的因子保留当前质量分，不参与自动校准
		if existing.IsPinned || !existing.AutoCalibrate {
			db.Save(&existing)
			continue
		}

		// EMA 平滑：防单日噪声跳变；变化过小视为无实际调整，不写审计
		newQuality := blendQuality(oldQuality, quality)
		if absFloat(newQuality-oldQuality) < 0.005 {
			db.Save(&existing)
			continue
		}
		existing.Quality = newQuality
		db.Save(&existing)
		// 历史审计：仅记录实际发生的校准变化（对标 PanWatch FactorWeightHistory，避免冷启动期审计噪声）
		db.Create(&data.FactorCalibrationHistory{
			FactorName: name, OldQuality: oldQuality, NewQuality: newQuality,
			RankIC: rankIC, Accuracy: accuracy, Coverage: coverage,
			Stability: stability, Decay: decay, Reason: "auto", CreatedAt: now,
		})
	}
}

// blendQuality EMA 平滑（对标 PanWatch factor_calibration.blend）：new = old×(1-α) + raw×α，clamp 到 [0,1]。
func blendQuality(old, raw float64) float64 {
	return clampFloat(old*(1-factorCalibrationAlpha)+raw*factorCalibrationAlpha, 0, 1)
}

// factorQualityFromItem 从单因子复盘结果计算质量分 [0,1]。
// 权重设计：IC 35%、前瞻准确率 30%、区分度 15%、覆盖率 10%、短周期衰减 10%。
// 诚实下线：分半不稳定（split_half_stable=false）的因子被怀疑为假阳性，IC 贡献减半，
// 其余维度照常，使其整体质量分更接近均值以下，从而在选股权重中被自然降权。
func factorQualityFromItem(item map[string]interface{}) float64 {
	absIC := item["abs_rank_ic"].(float64)
	accuracy := item["top_accuracy"].(float64)
	stability := item["stability_std"].(float64)
	coverage := item["coverage_pct"].(float64)
	decay := item["decay_20d_minus_1d"].(float64)
	halfStable, _ := item["split_half_stable"].(bool)

	// 分半检验的最小可信样本量：样本过少时两半段 IC 主要是噪声，分半「不稳定」结论不可靠，
	// 不应据此武断「诚实下线」。改为——仅当样本量足够(≥30)且分半不稳定时才减半 IC；
	// 样本不足的因子其覆盖率/IC 本来就极低，已被 covScore/icScore 自然压低，无需额外减半。
	sampleCount := 0
	switch v := item["sample_count"].(type) {
	case int:
		sampleCount = v
	case float64:
		sampleCount = int(v)
	}

	// IC 贡献：|RankIC|=0.04 视为满分 0.35；分半真实判定为不稳定(样本量充足前提)时按诚实下线 IC 贡献减半
	icScore := clampFloat(absIC/0.04, 0, 1) * 0.35
	if !halfStable && sampleCount >= 30 {
		icScore *= 0.5
	}
	// 前瞻准确率：前1/3命中率直接入分 0.30
	accScore := clampFloat(accuracy, 0, 1) * 0.30
	// 区分度：scoreStd 越接近 0.15 区分度越理想 0.15
	staScore := (1 - clampFloat(absFloat(stability-0.15)/0.15, 0, 1)) * 0.15
	// 覆盖率：覆盖 80% 宇宙视为满分 0.10
	covScore := clampFloat(coverage/80, 0, 1) * 0.10
	// 短周期衰减：decay 越负说明短周期越有效；decay<=-0.02 满分，>=0.02 归零 0.10
	decScore := (1 - clampFloat((decay+0.02)/0.04, 0, 1)) * 0.10

	return clampFloat(icScore+accScore+staScore+covScore+decScore, 0, 1)
}

//==== 统计辅助 ====

func rankIC(scores, rets []float64) float64 {
	n := len(scores)
	if n < 3 {
		return 0
	}
	sr := rankFloat(scores)
	rr := rankFloat(rets)
	return pearson(sr, rr)
}

func pearsonIC(samples []fSample) float64 {
	n := len(samples)
	if n < 3 {
		return 0
	}
	sc := make([]float64, n)
	rc := make([]float64, n)
	for i, s := range samples {
		sc[i] = s.score
		rc[i] = s.fwdRet
	}
	return pearson(sc, rc)
}

func spearmanIC(samples []fSample) float64 {
	n := len(samples)
	if n < 3 {
		return 0
	}
	sc := make([]float64, n)
	rc := make([]float64, n)
	for i, s := range samples {
		sc[i] = s.score
		rc[i] = s.fwdRet
	}
	return rankIC(sc, rc)
}

// splitHalfStability 分半稳定性检验（诚实「防假阳性」）：
// 将横截面样本按固定种子伪随机两分（交替分配到两半，数量均衡），各自计算半段 RankIC。
// 两半段同号且都达到最小强度阈值才算「稳定候选」（stable=true），返回两半段 IC 供审计。
// 防止少数极端样本或多重比较产生的假阳性因子，与 Augur 的「分半稳定性检验」思路一致。
// 固定种子保证结果可复现；样本不足时视为不稳定（不判定）。
func splitHalfStability(samples []fSample) (halfIC []float64, stable bool) {
	n := len(samples)
	if n < 8 {
		return nil, false
	}
	perm := rand.New(rand.NewSource(20240701)).Perm(n)
	sc1, rt1 := make([]float64, 0, n/2+1), make([]float64, 0, n/2+1)
	sc2, rt2 := make([]float64, 0, n/2+1), make([]float64, 0, n/2+1)
	for i, p := range perm {
		s := samples[p]
		if i%2 == 0 {
			sc1 = append(sc1, s.score)
			rt1 = append(rt1, s.fwdRet)
		} else {
			sc2 = append(sc2, s.score)
			rt2 = append(rt2, s.fwdRet)
		}
	}
	if len(sc1) < 3 || len(sc2) < 3 {
		return nil, false
	}
	ic1 := rankIC(sc1, rt1)
	ic2 := rankIC(sc2, rt2)
	halfIC = []float64{ic1, ic2}
	const minStrength = 0.02
	stable = math.Signbit(ic1) == math.Signbit(ic2) &&
		math.Abs(ic1) >= minStrength && math.Abs(ic2) >= minStrength
	return halfIC, stable
}

// groupSpread 按得分三等分，返回最上/最下分组平均前瞻收益及其差
func groupSpread(samples []fSample) (top, bottom, spread float64) {
	sorted := append([]fSample(nil), samples...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].score > sorted[j].score })
	n := len(sorted)
	third := n / 3
	if third < 1 {
		return 0, 0, 0
	}
	top = meanFwd(sorted[:third])
	bottom = meanFwd(sorted[n-third:])
	spread = top - bottom
	return
}

// topAccuracy 得分最高前1/3中，前瞻收益为正的比例
func topAccuracy(samples []fSample) float64 {
	sorted := append([]fSample(nil), samples...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].score > sorted[j].score })
	n := len(sorted)
	third := n / 3
	if third < 1 {
		return 0
	}
	pos := 0
	for i := 0; i < third; i++ {
		if sorted[i].fwdRet > 0 {
			pos++
		}
	}
	return float64(pos) / float64(third)
}

func scoreStd(samples []fSample) float64 {
	if len(samples) < 2 {
		return 0
	}
	m := 0.0
	for _, s := range samples {
		m += s.score
	}
	m /= float64(len(samples))
	v := 0.0
	for _, s := range samples {
		d := s.score - m
		v += d * d
	}
	return absFloat(sqrtSafe(v / float64(len(samples))))
}

func calcDecay(perHorizon map[int]map[string][]fSample, cat string) float64 {
	ic1 := rankICOfSamples(perHorizon, cat, 1)
	ic20 := rankICOfSamples(perHorizon, cat, 20)
	return ic20 - ic1 // 负值表示短周期有效、长周期衰减
}

func rankICOfSamples(perHorizon map[int]map[string][]fSample, cat string, h int) float64 {
	samples, ok := perHorizon[h][cat]
	if !ok || len(samples) < 3 {
		return 0
	}
	return spearmanIC(samples)
}

func meanFwd(samples []fSample) float64 {
	if len(samples) == 0 {
		return 0
	}
	s := 0.0
	for _, x := range samples {
		s += x.fwdRet
	}
	return s / float64(len(samples))
}

func pearson(a, b []float64) float64 {
	n := len(a)
	if n != len(b) || n < 3 {
		return 0
	}
	ma, mb := meanF(a), meanF(b)
	var cov, va, vb float64
	for i := 0; i < n; i++ {
		da := a[i] - ma
		db := b[i] - mb
		cov += da * db
		va += da * da
		vb += db * db
	}
	if va == 0 || vb == 0 {
		return 0
	}
	return cov / (sqrtSafe(va)*sqrtSafe(vb) + 1e-12)
}

func meanF(a []float64) float64 {
	if len(a) == 0 {
		return 0
	}
	s := 0.0
	for _, v := range a {
		s += v
	}
	return s / float64(len(a))
}

// rankFloat 返回平均秩次（处理并列）
func rankFloat(values []float64) []float64 {
	n := len(values)
	idx := make([]int, n)
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(i, j int) bool { return values[idx[i]] < values[idx[j]] })

	ranks := make([]float64, n)
	for i := 0; i < n; {
		j := i
		sum := 0.0
		for j < n && values[idx[j]] == values[idx[i]] {
			sum += float64(j + 1)
			j++
		}
		avg := sum / float64(j-i)
		for k := i; k < j; k++ {
			ranks[idx[k]] = avg
		}
		i = j
	}
	return ranks
}

func avgAbsRankIC(list []interface{}) float64 {
	if len(list) == 0 {
		return 0
	}
	s := 0.0
	for _, it := range list {
		if m, ok := it.(map[string]interface{}); ok {
			if v, ok := m["abs_rank_ic"].(float64); ok {
				s += v
			}
		}
	}
	return round4(s / float64(len(list)))
}

func factorInterpretation(rankIC, spread, accuracy float64) string {
	if absFloat(rankIC) < 0.05 {
		return "该因子当周期解释力弱（|RankIC|<0.05），横截面区分度有限"
	}
	monotonic := ""
	if (rankIC > 0 && spread > 0) || (rankIC < 0 && spread < 0) {
		monotonic = "方向一致；"
	} else {
		monotonic = "方向存在背离，需谨慎解读；"
	}
	return fmt.Sprintf("RankIC=%.4f，%s 分组收益差 %.2f%%，最上组命中率 %.1f%%", rankIC, monotonic, spread, accuracy*100)
}

func uniqueHorizons(forward int) []int {
	h := map[int]bool{1: true, 5: true, 10: true, 20: true}
	h[forward] = true
	var out []int
	for k := range h {
		out = append(out, k)
	}
	sort.Ints(out)
	return out
}

// normalizeOhlcSymbol 把任意股票代码规范化为 ohlc 前缀形式（sh/sz/bj + 6位代码）
func normalizeOhlcSymbol(symbol string) string {
	s := strings.ToLower(strings.TrimSpace(symbol))
	if len(s) >= 2 {
		p := s[:2]
		if p == "sh" || p == "sz" || p == "bj" {
			return s
		}
	}
	switch s[0] {
	case '6', '5', '9':
		return "sh" + s
	case '0', '2', '3':
		return "sz" + s
	case '4', '8':
		return "bj" + s
	default:
		return "sh" + s
	}
}

// barsToSnapshots 把 K 线序列转换为因子引擎所需的历史快照序列（用收盘价作为价格基准）
func barsToSnapshots(sym, name string, bars []data.FactorBar) []data.StockSnapshot {
	out := make([]data.StockSnapshot, 0, len(bars))
	for _, b := range bars {
		out = append(out, snapshotFromBar(sym, name, b))
	}
	return out
}

// snapshotFromBar 由 FactorBar 构造 FactorEngine 所需的 StockSnapshot
func snapshotFromBar(sym, name string, b data.FactorBar) data.StockSnapshot {
	return data.StockSnapshot{
		Code:         sym,
		Name:         name,
		Market:       normalizeMarket(sym),
		CurrentPrice: b.Close,
		PrevClose:    b.PreClose,
		Open:         b.Open,
		High:         b.High,
		Low:          b.Low,
		Volume:       b.Volume,
		Turnover:     b.Turnover,
	}
}

func normalizeMarket(sym string) string {
	if strings.HasPrefix(sym, "sh") {
		return "sh"
	}
	if strings.HasPrefix(sym, "sz") {
		return "sz"
	}
	if strings.HasPrefix(sym, "bj") {
		return "bj"
	}
	return "sh"
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func absFloat(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

func clampFloat(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func sqrtSafe(v float64) float64 {
	if v < 0 {
		return 0
	}
	z := v
	for i := 0; i < 60; i++ {
		if z == 0 {
			z = v * 0.5
			if z == 0 {
				return 0
			}
		}
		z = (z + v/z) / 2
	}
	return z
}
