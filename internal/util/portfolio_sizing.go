package util

// PortfolioSizing 根据投资金额综合测算组合持仓规模。
// 投资金额越大，可分散的持仓数越多，单标的预算占比越低；同一套分档规则
// 同时用于「投资规划(Planner)」与「CIO 盘中自动建仓」，确保两者口径一致：
//
//	3万以下   → 4  个候选，单标的约 25%
//	3-20万    → 5  个候选，单标的约 20%（20万以内（含20万）最多5支）
//	20-50万   → 8  个候选，单标的约 12%
//	50万以上  → 10 个候选，单标的约 10%
//
// 这样小资金聚焦少数标的以避免单票一手过重，大资金自然分散更多标的。
// 返回: 最大候选(持仓)数量, 单标的预算金额
func PortfolioSizing(totalAssets float64) (maxCandidates int, perStockBudget float64) {
	switch {
	case totalAssets < 30000:
		maxCandidates = 4
		perStockBudget = totalAssets * 0.25
	case totalAssets <= 200000:
		maxCandidates = 5
		perStockBudget = totalAssets * 0.20
	case totalAssets < 500000:
		maxCandidates = 8
		perStockBudget = totalAssets * 0.12
	default:
		maxCandidates = 10
		perStockBudget = totalAssets * 0.10
	}
	if maxCandidates < 1 {
		maxCandidates = 1
	}
	return
}
