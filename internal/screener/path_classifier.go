package screener

// StockPath 个股路径分类：将单只股票归入五种可解释路径之一。
// 借鉴 easy-stock 的「个股多路径分析」思想：先归路径再差异化解读，让选股结果可解释、可针对路径评估。
type StockPath string

const (
	PathMomentumLimitUp StockPath = "情绪连板" // 短线情绪/连板路径
	PathTrendCapacity   StockPath = "趋势容量" // 趋势容量（大资金/流动性强）
	PathTrendGrowth     StockPath = "趋势成长" // 趋势成长（中长趋势向上）
	PathOscillation     StockPath = "震荡观察" // 震荡整理，等待方向
	PathWeakRisk        StockPath = "弱势风险" // 弱势/风险，回避
)

// PathInput 路径分类输入（动量/波动/流动性均为真实量化指标，0~100 分；Volatility 为历史波动率值）。
type PathInput struct {
	Momentum1M  float64 // 1月动量
	Momentum3M  float64 // 3月动量
	Momentum12M float64 // 12月动量
	Volatility  float64 // 历史波动率（高分=波动大，对应短线投机属性）
	Liquidity   float64 // 流动性（高分=成交活跃、容纳资金大）
	ChangePct   float64 // 当日涨跌幅(%)
}

// PathResult 路径分类结果（路径 + 一句可读依据）。
type PathResult struct {
	Path   StockPath `json:"path"`
	Reason string    `json:"reason"`
}

// ClassifyStockPath 按规则将个股归入路径（纯确定性函数，无外部依赖）。
// 规则示意：长期动量太弱→弱势；短线动量极强+高波动→情绪连板；
// 中长动能持续向上→趋势成长/趋势容量（按流动性区分）；其余→震荡观察。
func ClassifyStockPath(in PathInput) PathResult {
	switch {
	case in.Momentum12M < 35 && in.Momentum3M < 45:
		return PathResult{PathWeakRisk, "长期(12M)与中期(3M)动量均偏弱，属弱势/风险路径"}
	case in.Momentum1M >= 70 && in.Volatility >= 25:
		return PathResult{PathMomentumLimitUp, "短线(1M)动量极强且波动较大，偏情绪连板路径"}
	case in.Momentum12M >= 55 && in.Momentum3M >= 60:
		if in.Liquidity >= 60 {
			return PathResult{PathTrendCapacity, "中长趋势向上且流动性充足，偏趋势容量路径"}
		}
		return PathResult{PathTrendGrowth, "中长趋势向上，偏趋势成长路径"}
	case in.Momentum3M >= 55:
		return PathResult{PathTrendGrowth, "中期动量较稳，处于趋势成长路径"}
	default:
		return PathResult{PathOscillation, "动量与波动未出明显方向，震荡观察路径"}
	}
}
