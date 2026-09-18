package screener

import (
	"context"
	"fmt"
	"log"
	"math"
	"time"

	"github.com/quantpilot/quantpilot/internal/data"
)

// ScreenerService 选股服务
// 统一版本：全市场选股，基于 DuckDB 真实数据
type ScreenerService struct {
	duckdbMgr *data.DuckDBManager
}

// NewScreenerService 创建选股服务
func NewScreenerService(duckdbMgr *data.DuckDBManager) *ScreenerService {
	return &ScreenerService{
		duckdbMgr: duckdbMgr,
	}
}

// ScreenStock 执行选股（主入口）
// 统一版本：始终使用全市场选股 + Pro因子引擎
func (s *ScreenerService) ScreenStock(req ScreeningRequest) (ScreeningResponse, error) {
	if s.duckdbMgr == nil {
		return ScreeningResponse{}, fmt.Errorf("选股引擎未初始化，DuckDB 管理器为空")
	}

	if !s.duckdbMgr.HasStockDB() {
		return ScreeningResponse{}, fmt.Errorf("行情数据库(stock.duckdb)未挂载，请确认数据文件位于程序目录的data文件夹下")
	}

	return s.ScreenStockPro(req)
}

// ScreenStockPro 全市场选股（使用DuckDB真实数据和Pro因子引擎）
func (s *ScreenerService) ScreenStockPro(req ScreeningRequest) (ScreeningResponse, error) {
	if s.duckdbMgr == nil {
		return ScreeningResponse{}, fmt.Errorf("DuckDB 未初始化")
	}

	// 数据新鲜度检查：数据过期时禁止生成新的股票池（Data Integrity Guard）
	guard := data.NewDataIntegrityGuard(s.duckdbMgr)
	marketStatus := guard.GetMarketDataStatus(context.Background())
	if !marketStatus.OK {
		return ScreeningResponse{}, fmt.Errorf("TASK_BLOCKED: %s", marketStatus.Reason)
	}

	// 获取全市场股票列表
	symbols, err := s.duckdbMgr.ListAllSymbolsFromStock(context.Background(), req.Market, 10000)
	if err != nil {
		return ScreeningResponse{}, fmt.Errorf("获取股票列表失败: %w", err)
	}

	log.Printf("[Screener] 加载 %d 只股票 (市场: %s)", len(symbols), req.Market)

	// 全市场禁止买入 ST / *ST 风险警示股：无论资金规模，选股宇宙一律剔除 ST。
	if before := len(symbols); before > 0 {
		keep := symbols[:0]
		for _, s := range symbols {
			if data.IsSTName(s.Name) {
				continue
			}
			keep = append(keep, s)
		}
		symbols = keep
		if removed := before - len(symbols); removed > 0 {
			log.Printf("[Screener] 已剔除 %d 只 ST/*ST 风险警示股（全局禁止买入）", removed)
		}
	}

	// 中小资金（如<50万）聚合：收缩选股宇宙，剔除 北交所/创业板/科创板，
	// 从全市场收敛到沪深主板，显著降低因子打分耗时并过滤高波动板块。
	if req.SmallCapital {
		before := len(symbols)
		symbols = FilterSmallCapitalUniverse(symbols)
		log.Printf("[Screener] 中小资金聚合市池: %d -> %d 只（剔除 北交所/创业板/科创板）", before, len(symbols))
	}

	if len(symbols) == 0 {
		log.Printf("[Screener] 股票池为空 (market=%s), 返回空结果", req.Market)
		return ScreeningResponse{
			StrategyName: req.StrategyID,
			TotalCount:   0,
			Results:      []StockScore{},
			GeneratedAt:  time.Now().Format(time.RFC3339),
		}, nil
	}

	// 统一策略模板：有用户画像且未自定义权重时，以画像为单一出处推导基础策略模板
	// （与投资规划共用 ResolveTemplateID，避免成长/均衡/防御风格错配）。
	effectiveStrategyID := req.StrategyID
	if req.CustomWeights == nil && req.InvestorProfile != nil {
		if id := ResolveTemplateID(req.InvestorProfile.InvestmentStyle); id != "" {
			effectiveStrategyID = id
		} else {
			effectiveStrategyID = ResolveTemplateID(req.InvestorProfile.RiskTolerance)
		}
	}
	if effectiveStrategyID == "" {
		effectiveStrategyID = "balanced"
	}
	styleName, riskName := "", ""
	if req.InvestorProfile != nil {
		styleName = req.InvestorProfile.InvestmentStyle
		riskName = req.InvestorProfile.RiskTolerance
	}
	log.Printf("[Screener] 策略模板解析: profile_style=%s, profile_risk=%s, strategyID=%s",
		styleName, riskName, effectiveStrategyID)

	// 构建选股请求
	proReq := ProScreeningRequest{
		StrategyID:       effectiveStrategyID,
		CustomWeights:    req.CustomWeights,
		Market:           req.Market,
		MaxResults:       req.MaxResults,
		MinScore:         req.MinScore,
		LookbackDays:     252,
		UseDynamicWeight: true,
		InvestorProfile:  req.InvestorProfile,
	}

	// 使用Pro因子引擎
	engine := NewProFactorEngine(s.duckdbMgr)
	proResp, err := engine.RunProScreening(proReq, symbols)
	if err != nil {
		return ScreeningResponse{}, fmt.Errorf("选股引擎执行失败: %w", err)
	}

	// 流动性硬下限：换手/成交额不足的“僵尸股”即便高分也直接剔除（不降权、不保留）。
	// 小基金最怕“买了跑不掉”，流动性必须是拒买门槛而非只是一个打分因子。
	if before := len(proResp.Results); before > 0 {
		proResp.Results = applyLiquidityFloor(proResp.Results)
		if removed := before - len(proResp.Results); removed > 0 {
			log.Printf("[Screener] 流动性硬下限过滤：剔除 %d 只流动性不足股票 (%d->%d)", removed, before, len(proResp.Results))
		}
	}

	log.Printf("[Screener] 选股完成: %d 只候选股票, %d 只入选", len(symbols), len(proResp.Results))

	return s.convertProResponse(proResp), nil
}

// FilterSmallCapitalUniverse 过滤中小资金（如<50万）不适合参与的股票：
// 剔除 ST（风险警示）、北交所、创业板、科创板，仅保留沪深主板，缩小选股宇宙以提速并降低风险。
// 板块判定复用统一的证券分类器 data.ClassifySecurity，保证口径一致。
func FilterSmallCapitalUniverse(symbols []data.StockSymbolInfo) []data.StockSymbolInfo {
	keep := make([]data.StockSymbolInfo, 0, len(symbols))
	for _, s := range symbols {
		// 剔除 ST / *ST 风险警示股票（与全局禁止买入口径一致）
		if data.IsSTName(s.Name) {
			continue
		}
		switch data.ClassifySecurity(s.Symbol) {
		case data.SecurityStockGrowth, data.SecurityStockSciTech, data.SecurityStockBJ:
			// 创业板 / 科创板 / 北交所：中小资金不参与
			continue
		case data.SecurityStockMain:
			// 沪深主板A股保留
		default:
			// 非普通股票（指数/基金/可转债等）由选股宇宙层面已剔除，此处兜底跳过
			continue
		}
		keep = append(keep, s)
	}
	return keep
}

// applyLiquidityFloor 流动性硬下限过滤：剔除流动性（FactorLiquidity 因子分，0-100，50 为中性）过低的股票。
// 与“流动性作为打分因子”（低权重降分）不同，这里是**拒买硬门槛**：小基金买入后要能顺利离场，
// 流动性因子分过低的“僵尸股”即便总高分也直接剔除。仅当过滤后非空时生效，避免把选股池过滤空。
func applyLiquidityFloor(results []ProStockScore) []ProStockScore {
	const minLiquidityScore = 30.0 // 流动性因子分硬下限：低于此（约均值下方 ~0.5σ）视为流动性不足剔除
	keep := make([]ProStockScore, 0, len(results))
	for _, r := range results {
		s := r.FactorScores[FactorLiquidity].Score
		if s >= minLiquidityScore {
			keep = append(keep, r)
		}
	}
	// 兜底：全部被过滤时退回原始结果，宁可不做硬过滤也不让用户面对空池。
	if len(keep) == 0 {
		return results
	}
	return keep
}

// convertProResponse 将Pro响应转换为标准响应（保留所有Pro版扩展字段）
func (s *ScreenerService) convertProResponse(proResp ProScreeningResponse) ScreeningResponse {
	results := make([]StockScore, len(proResp.Results))
	for i, r := range proResp.Results {
		results[i] = StockScore{
			Code:         r.Code,
			Name:         r.Name,
			Market:       r.Market,
			Price:        r.Price,
			ChangePct:    r.ChangePct,
			TotalScore:   r.TotalScore,
			FactorScores: r.FactorScores,
			Ranking:      r.Ranking,
			Reasons:      r.Reasons,
			Warnings:     r.Warnings,
			// Pro版扩展字段 - 收益率和风险指标
			Momentum1M:   r.Momentum1M,
			Momentum3M:   r.Momentum3M,
			Momentum6M:   r.Momentum6M,
			Momentum12M:  r.Momentum12M,
			Volatility:   r.Volatility,
			Amplitude:    r.Amplitude,
			TurnoverRate: r.TurnoverRate,
			Liquidity:    r.Liquidity,
			// AI评分拆解（对标 PanWatch AI Score：1-10评分 + 利好/风险因子）
			AIScore:       r.AIScore,
			FactorExplain: r.FactorExplain,
		}
	}

	return ScreeningResponse{
		StrategyName:    proResp.StrategyName,
		TotalCount:      proResp.TotalCount,
		Results:         results,
		GeneratedAt:     proResp.GeneratedAt,
		Formula:         proResp.Formula,
		DynamicFormula:  proResp.DynamicFormula,
		Tier:            "unified",
		Upgrades:        make([]string, 0),
		DynamicWeights:  proResp.DynamicWeights,
		TemplateWeights: proResp.TemplateWeights,
		FactorHealth:    proResp.FactorHealth,
		MarketState:     proResp.MarketState,
		ProfileSummary:  proResp.ProfileSummary,
		UsedSmartEngine: proResp.UsedSmartEngine,
	}
}

// GetStrategyList 获取所有策略模板列表
func (s *ScreenerService) GetStrategyList() []StrategyTemplate {
	return AllStrategyTemplates()
}

// ValidateCustomWeights 验证自定义因子权重
func (s *ScreenerService) ValidateCustomWeights(weights map[FactorID]float64) error {
	// 权重总和校验
	var total float64
	for _, w := range weights {
		total += w
	}

	if total < 0.99 || total > 1.01 {
		return fmt.Errorf("权重总和必须为 1.0，当前为 %.2f", total)
	}

	// 非负校验
	for id, w := range weights {
		if w < 0 {
			return fmt.Errorf("因子 %s 权重不能为负数", id)
		}
	}

	return nil
}

// GenerateFactorHealthReport 生成因子健康度报告
func (s *ScreenerService) GenerateFactorHealthReport() (interface{}, error) {
	// 获取市场统计数据
	stats, err := s.duckdbMgr.GetMarketStats(context.Background(), "all")
	if err != nil {
		return nil, fmt.Errorf("获取市场统计数据失败: %w", err)
	}

	// 基于真实数据计算因子健康度
	factorHealth := make([]map[string]interface{}, 0)
	for _, stat := range stats {
		health := 50.0
		if stat.TotalVolume > 0 {
			health = 60.0
			if stat.AvgPrice > 0 {
				health = 70.0
			}
			if stat.StockCount > 10 {
				health = 85.0
			}
		}
		factorHealth = append(factorHealth, map[string]interface{}{
			"sector":       stat.Sector,
			"health":       health,
			"avg_price":    stat.AvgPrice,
			"total_volume": stat.TotalVolume,
			"stock_count":  stat.StockCount,
			"status":       "HEALTHY",
		})
	}

	return map[string]interface{}{
		"total_sectors": len(factorHealth),
		"factors":       factorHealth,
		"timestamp":     "real_data",
	}, nil
}

// CalculateStructureRisk 计算结构风险
func (s *ScreenerService) CalculateStructureRisk(stockCodes []string) (interface{}, error) {
	if len(stockCodes) == 0 {
		return nil, fmt.Errorf("股票代码列表不能为空")
	}

	// 获取每只股票的历史数据并计算风险指标
	risks := make([]map[string]interface{}, 0)
	for _, code := range stockCodes {
		prices, err := s.duckdbMgr.GetRecentPrices(context.Background(), "cn", code, 60)
		if err != nil || len(prices) < 5 {
			log.Printf("[Screener] 获取 %s 行情数据失败: %v", code, err)
			continue
		}

		// 计算简单风险指标
		returns := make([]float64, 0)
		for i := 1; i < len(prices); i++ {
			if prices[i-1].Close > 0 {
				ret := (prices[i].Close - prices[i-1].Close) / prices[i-1].Close
				returns = append(returns, ret)
			}
		}

		volatility := 0.0
		if len(returns) > 1 {
			mean := 0.0
			for _, r := range returns {
				mean += r
			}
			mean /= float64(len(returns))

			variance := 0.0
			for _, r := range returns {
				diff := r - mean
				variance += diff * diff
			}
			volatility = math.Sqrt(variance / float64(len(returns)-1))
		}

		risks = append(risks, map[string]interface{}{
			"code":        code,
			"volatility":  volatility,
			"data_points": len(prices),
		})
	}

	return map[string]interface{}{
		"total_stocks": len(risks),
		"risk_details": risks,
		"timestamp":    "real_data",
	}, nil
}

// OptimizePortfolio 组合优化
func (s *ScreenerService) OptimizePortfolio(stockCodes []string, targetReturn float64) (interface{}, error) {
	if len(stockCodes) < 2 {
		return nil, fmt.Errorf("至少需要2只股票进行组合优化")
	}

	// 获取各股票的历史数据
	pricesMap := make(map[string][]data.PriceData)
	for _, code := range stockCodes {
		prices, err := s.duckdbMgr.GetRecentPrices(context.Background(), "cn", code, 252)
		if err != nil || len(prices) < 20 {
			log.Printf("[Screener] 跳过 %s: 数据不足", code)
			continue
		}
		pricesMap[code] = prices
	}

	if len(pricesMap) < 2 {
		return nil, fmt.Errorf("有效股票数据不足，无法进行优化")
	}

	// 计算各股票的收益率和波动率
	returns := make(map[string][]float64)
	for code, prices := range pricesMap {
		ret := make([]float64, 0)
		for i := 1; i < len(prices); i++ {
			if prices[i-1].Close > 0 {
				r := (prices[i].Close - prices[i-1].Close) / prices[i-1].Close
				ret = append(ret, r)
			}
		}
		returns[code] = ret
	}

	// 计算预期年化收益率
	expectedReturns := make(map[string]float64)
	for code, ret := range returns {
		if len(ret) > 0 {
			avg := 0.0
			for _, r := range ret {
				avg += r
			}
			expectedReturns[code] = (avg / float64(len(ret))) * 252
		}
	}

	// 简化优化：按预期收益率排序，分配权重
	type assetScore struct {
		code  string
		score float64
	}
	var scores []assetScore
	for code, er := range expectedReturns {
		scores = append(scores, assetScore{code: code, score: math.Abs(er)})
	}

	// 按得分分配权重
	weights := make(map[string]float64)
	totalScore := 0.0
	for _, s := range scores {
		totalScore += s.score
	}
	if totalScore > 0 {
		for _, s := range scores {
			weights[s.code] = s.score / totalScore
		}
	} else {
		// 等权重
		n := float64(len(scores))
		for _, s := range scores {
			weights[s.code] = 1.0 / n
		}
	}

	return map[string]interface{}{
		"weights":       weights,
		"target_return": targetReturn,
		"assets_count":  len(weights),
		"timestamp":     "real_data",
	}, nil
}

// RunAIAgent AI Agent
func (s *ScreenerService) RunAIAgent(query string) (interface{}, error) {
	if query == "" {
		return nil, fmt.Errorf("查询内容不能为空")
	}

	return map[string]interface{}{
		"status":     "needs_llm",
		"query":      query,
		"message":    "AI Agent功能需要LLM服务支持，请确保Agent系统已正确配置",
		"suggestion": "建议使用CIO/Planner等Agent的ProcessTask接口进行智能查询",
	}, nil
}
