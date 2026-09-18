package portfolio

import "sort"

// 行业级 Brinson 归因。
// 系统暂无可靠的市值/风格分档数据（仅有行业分类），因此归因仅做行业维度：
// 把「组合持仓」与「基准（等权全候选池）」相对比，将组合相对基准的超额收益拆解为
// 配置效应(Allocation)、选择效应(Selection) 与交互效应(Interaction) 三部分，全部来源真实
// 权重与真实区间收益，严禁伪造。

// BrinsonIndustry 单行业归因明细
type BrinsonIndustry struct {
	Industry         string  // 行业名称
	PortfolioWeight  float64 // 组合在该行业权重(0-1)
	BenchmarkWeight  float64 // 基准在该行业权重(0-1)
	PortfolioReturn  float64 // 组合在该行业的区间收益(0-1)
	BenchmarkReturn  float64 // 基准在该行业的区间收益(0-1)
	Allocation       float64 // 配置效应(0-1)
	Selection        float64 // 选择效应(0-1)
	Interaction      float64 // 交互效应(0-1)
}

// BrinsonResult 行业级 Brinson 归因结果
type BrinsonResult struct {
	BenchmarkReturn float64            // 基准组合区间收益
	PortfolioReturn float64            // 组合区间收益
	ExcessReturn    float64            // 超额收益 = PortfolioReturn - BenchmarkReturn
	Allocation      float64            // 配置效应合计
	Selection       float64            // 选择效应合计
	Interaction     float64            // 交互效应合计
	// IndustryAllocation 行业选择效应表
	IndustryAllocation   map[string]float64
	IndustrySelection    map[string]float64
	IndustryInteraction  map[string]float64
	Industries           []BrinsonIndustry
}

// industryReturn 组合某行业在给定权重下的加权区间收益
func industryWeightedReturn(industry string, weights map[string]float64, returns map[string]float64, industryOf map[string]string) (weight, ret float64) {
	wSum := 0.0
	num := 0.0
	for code, w := range weights {
		if w <= 0 {
			continue
		}
		ind, ok := industryOf[code]
		if !ok || ind == "" {
			ind = "通用"
		}
		if ind != industry {
			continue
		}
		wSum += w
		num += w * returns[code]
	}
	if wSum <= 1e-12 {
		return 0, 0
	}
	return wSum, num / wSum
}

// totalWeightedReturn 组合整体的加权区间收益
func totalWeightedReturn(weights map[string]float64, returns map[string]float64) float64 {
	num := 0.0
	for code, w := range weights {
		if w > 0 {
			num += w * returns[code]
		}
	}
	return num
}

// CalculateBrinsonAttribution 行业级 Brinson 归因。
// portfolioWeights: 组合持仓权重(代码->权重, 0-1)；equalWeights: 等权全候选池(代码->1/N)；
// returns: 每只股票区间收益(0-1)；industryOf: 代码->行业。industryOf 为空时退化为全部归入「通用」。
func CalculateBrinsonAttribution(
	portfolioWeights map[string]float64,
	equalWeights map[string]float64,
	returns map[string]float64,
	industryOf map[string]string,
) BrinsonResult {
	result := BrinsonResult{
		IndustryAllocation:  make(map[string]float64),
		IndustrySelection:   make(map[string]float64),
		IndustryInteraction: make(map[string]float64),
	}

	// 收集参与归因的全部代码（取 portfolio∪equal），仅统计有权重且有收益的
	codeSet := make(map[string]struct{})
	for c := range portfolioWeights {
		codeSet[c] = struct{}{}
	}
	for c := range equalWeights {
		codeSet[c] = struct{}{}
	}

	// 构建行业集合
	industrySet := make(map[string]struct{})
	for c := range codeSet {
		ind, ok := industryOf[c]
		if !ok || ind == "" {
			ind = "通用"
		}
		industrySet[ind] = struct{}{}
	}
	industries := make([]string, 0, len(industrySet))
	for ind := range industrySet {
		industries = append(industries, ind)
	}
	sort.Strings(industries)

	result.BenchmarkReturn = totalWeightedReturn(equalWeights, returns)
	result.PortfolioReturn = totalWeightedReturn(portfolioWeights, returns)
	result.ExcessReturn = result.PortfolioReturn - result.BenchmarkReturn

	for _, ind := range industries {
		portW, portR := industryWeightedReturn(ind, portfolioWeights, returns, industryOf)
		benchW, benchR := industryWeightedReturn(ind, equalWeights, returns, industryOf)

		// Brinson-Fachler：
		// Allocation = (w_p - w_b) * (r_b - r_bench_total)
		alloc := (portW - benchW) * (benchR - result.BenchmarkReturn)
		// Selection = w_b * (r_p - r_b)
		sel := benchW * (portR - benchR)
		// Interaction = (w_p - w_b) * (r_p - r_b)
		iteract := (portW - benchW) * (portR - benchR)

		result.IndustryAllocation[ind] = alloc
		result.IndustrySelection[ind] = sel
		result.IndustryInteraction[ind] = iteract
		result.Allocation += alloc
		result.Selection += sel
		result.Interaction += iteract

		result.Industries = append(result.Industries, BrinsonIndustry{
			Industry:        ind,
			PortfolioWeight: portW,
			BenchmarkWeight: benchW,
			PortfolioReturn: portR,
			BenchmarkReturn: benchR,
			Allocation:      alloc,
			Selection:       sel,
			Interaction:     iteract,
		})
	}

	// 排序以稳定输出
	sort.Slice(result.Industries, func(i, j int) bool {
		return result.Industries[i].Industry < result.Industries[j].Industry
	})

	return result
}