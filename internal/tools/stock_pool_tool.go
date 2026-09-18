package tools

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/quantpilot/quantpilot/internal/data"
	"github.com/quantpilot/quantpilot/internal/screener"
)

// StockPoolTool 选股工具（StockPoolEngine）
// 职责：根据 Agent 提供的 Selection Specification，从股票全集（DuckDB 真实数据）中
// 确定性过滤、因子打分、行业分散、排序后返回候选股票池。
// 不负责最终选股、建仓、仓位、买卖——那些交由 Agent 决策。
type StockPoolTool struct {
	screenerService *screener.ScreenerService
	tradeablePool   *screener.TradeablePool
}

// NewStockPoolTool 创建选股工具
func NewStockPoolTool(ss *screener.ScreenerService, tp *screener.TradeablePool) *StockPoolTool {
	return &StockPoolTool{screenerService: ss, tradeablePool: tp}
}

func (t *StockPoolTool) Name() string { return "build_stock_pool" }

func (t *StockPoolTool) Description() string {
	return "从全市场真实行情数据中做确定性硬过滤、因子打分、行业分散与排序，返回候选股票池（含选股审计）。供 Quant/CIO/Planner 依据投资规范挑选候选标的。"
}

func (t *StockPoolTool) GetDefinition() ToolDefinition {
	return ToolDefinition{
		Type: "function",
		Function: FunctionDef{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"universe": map[string]interface{}{
						"type":        "string",
						"description": "股票宇宙：all=全A；sh=沪市；sz=深市；large_mid=大中盘（暂按全A+质量过滤）",
						"enum":        []string{"all", "sh", "sz", "large_mid"},
					},
					"factor_profile": map[string]interface{}{
						"type":        "string",
						"description": "因子画像（决定各因子权重）：balanced=均衡；defensive=防御(质量/低波为主)；growth=成长(动量/成长为主)",
						"enum":        []string{"balanced", "defensive", "growth"},
					},
					"min_score": map[string]interface{}{
						"type":        "number",
						"description": "最低综合分数（0-100），低于此分的剔除，默认 0",
					},
					"max_results": map[string]interface{}{
						"type":        "integer",
						"description": "返回候选数量（默认 100，仅返回池，不代最终选股）",
					},
					"max_price": map[string]interface{}{
						"type":        "number",
						"description": "最大股价上限（元），用于小资金低价股筛选（如5万以下需 股价×100≤单标的资金，可传 50），>0 才生效",
					},
					"max_industry_weight": map[string]interface{}{
						"type":        "number",
						"description": "单行业最多入选占比(0-1)，用于行业分散，默认 0.25",
					},
					"submit_to_pool": map[string]interface{}{
						"type":        "boolean",
						"description": "是否将结果提交到可交易股票池供审批，默认 false",
					},
				},
				"required": []string{"universe"},
			},
		},
	}
}

// Execute 执行选股
func (t *StockPoolTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	if t.screenerService == nil {
		return nil, fmt.Errorf("选股服务未初始化")
	}

	universe, _ := args["universe"].(string)
	if universe == "" {
		universe = "all"
	}
	factorProfile, _ := args["factor_profile"].(string)
	if factorProfile == "" {
		factorProfile = "balanced"
	}
	minScore := 0.0
	if ms, ok := args["min_score"].(float64); ok && ms > 0 {
		minScore = ms
	}
	maxResults := 100
	if mr, ok := args["max_results"].(float64); ok && mr > 0 {
		maxResults = int(mr)
	}
	maxPrice := 0.0
	if mp, ok := args["max_price"].(float64); ok && mp > 0 {
		maxPrice = mp
	}
	maxIndustryWeight := 0.25
	if mi, ok := args["max_industry_weight"].(float64); ok && mi > 0 {
		maxIndustryWeight = mi
	}
	submitToPool := false
	if sp, ok := args["submit_to_pool"].(bool); ok {
		submitToPool = sp
	}

	// market 参数映射：全市场用 "all"
	market := universe
	if universe == "large_mid" {
		market = "all"
	}
	if market != "all" && market != "sh" && market != "sz" {
		return nil, fmt.Errorf("不支持的股票宇宙: %s", universe)
	}

	selectionID := fmt.Sprintf("SEL-%s-%03d", time.Now().Format("20060102"),
		(time.Now().UnixNano()/1e6)%1000)

	// 因子画像 -> 统一策略模板ID（与投资规划共用 screener 模板解析，保持两套策略模板一致）
	tpl := screener.GetStrategyTemplate(screener.ResolveTemplateID(factorProfile))
	if tpl == nil {
		return nil, fmt.Errorf("无效的因子画像: %s", factorProfile)
	}
	strategyID := tpl.ID

	req := screener.ScreeningRequest{
		StrategyID: strategyID,
		Market:     market,
		MaxResults: maxResults,
		MinScore:   minScore,
	}
	result, err := t.screenerService.ScreenStock(req)
	if err != nil {
		return nil, fmt.Errorf("选股失败: %w", err)
	}

	// 行业分散过滤：按行业占比上限剔除尾部
	diversified := result.Results
	if maxIndustryWeight > 0 {
		diversified = applyIndustryCap(result.Results, maxIndustryWeight)
	}

	// 低价股筛选：满足"股价×100≤单标的分配资金"的小资金规则（如股价≤50元）
	if maxPrice > 0 {
		filtered := diversified[:0]
		for _, s := range diversified {
			if s.Price > 0 && s.Price <= maxPrice {
				filtered = append(filtered, s)
			}
		}
		diversified = filtered
	}

	var submittedCount int
	if submitToPool && t.tradeablePool != nil {
		sc := result
		sc.Results = diversified
		submittedCount, _ = t.tradeablePool.SubmitScreenerResult(&sc, "stock_pool_tool", "StockPoolEngine")
	}

	// 组装候选列表（结构化，便于 Quant/CIO 解释）
	candidates := make([]map[string]interface{}, 0, len(diversified))
	for _, r := range diversified {
		factorScores := map[string]interface{}{}
		for fid, fs := range r.FactorScores {
			factorScores[string(fid)] = fs.Score
		}
		candidates = append(candidates, map[string]interface{}{
			"symbol":        r.Code,
			"market":        r.Market,
			"name":          r.Name,
			"price":         r.Price,
			"total_score":   r.TotalScore,
			"factor_scores": factorScores,
			"reasons":       r.Reasons,
			"warnings":      r.Warnings,
		})
	}

	// 选股审计
	audit := map[string]interface{}{
		"selection_id":    selectionID,
		"generated_at":    time.Now().Format(time.RFC3339),
		"universe":        universe,
		"factor_profile":  factorProfile,
		"strategy":        result.StrategyName,
		"market":          market,
		"min_score":       minScore,
		"max_results":     maxResults,
		"max_price":       maxPrice,
		"max_industry":    maxIndustryWeight,
		"screened_count":  result.TotalCount,
		"diversified_to":  len(diversified),
		"candidate_count": len(candidates),
		"data_guard":      result.Tier,
	}

	return map[string]interface{}{
		"universe":          universe,
		"factor_profile":    factorProfile,
		"screened_count":    result.TotalCount,
		"candidate_count":   len(candidates),
		"candidates":        candidates,
		"selection_audit":   audit,
		"submitted_to_pool": submitToPool,
		"submitted_count":   submittedCount,
	}, nil
}

// applyIndustryCap 行业分散：同一行业入选数量不超过 maxIndustryWeight×总数
func applyIndustryCap(stocks []screener.StockScore, maxIndustryWeight float64) []screener.StockScore {
	if len(stocks) == 0 {
		return stocks
	}
	loader := data.GetDictLoader()
	industryCount := map[string]int{}
	for _, s := range stocks {
		ind := loader.GetIndustryByStock(s.Code)
		if ind == "" || ind == "通用" {
			ind = "其他"
		}
		industryCount[ind]++
	}
	// 合法地按分数从高到低排序，再逐只按行业上限保留
	sorted := make([]screener.StockScore, len(stocks))
	copy(sorted, stocks)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].TotalScore > sorted[j].TotalScore })

	capN := int(float64(len(sorted))*maxIndustryWeight + 0.5)
	if capN < 1 {
		capN = 1
	}
	kept := []screener.StockScore{}
	indCount := map[string]int{}
	for _, s := range sorted {
		ind := loader.GetIndustryByStock(s.Code)
		if ind == "" || ind == "通用" {
			ind = "其他"
		}
		if indCount[ind] >= capN {
			continue
		}
		indCount[ind]++
		kept = append(kept, s)
	}
	if len(kept) == 0 {
		return stocks
	}
	return kept
}
