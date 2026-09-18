package backtest

import (
	"log"

	"github.com/quantpilot/quantpilot/internal/data"
)

// ==================== 财务数据提供者（DuckDB 适配） ====================
// DuckDBFinancialProvider 实现 FinancialProvider 接口，把 data 包财务表查询
// 适配为 backtest 所需的 FinancialBar（按披露日对齐，无未来函数）。

// DuckDBFinancialProvider 基于 DuckDB stock.financial_report 的财务数据提供者
type DuckDBFinancialProvider struct {
	duckdbMgr *data.DuckDBManager
}

// NewDuckDBFinancialProvider 创建 DuckDB 财务数据提供者
func NewDuckDBFinancialProvider(duckdbMgr *data.DuckDBManager) *DuckDBFinancialProvider {
	return &DuckDBFinancialProvider{duckdbMgr: duckdbMgr}
}

// FinancialAsOf 返回 symbol 在 asOfDate 时点已披露的最新财务数据；无数据返回 nil。
// 复用 DuckDB GetFinancialAsOf（report_date<=D AND ann_date<=D 取最新），保证无未来函数。
func (p *DuckDBFinancialProvider) FinancialAsOf(symbol, asOfDate string) (*FinancialBar, error) {
	if p.duckdbMgr == nil || !p.duckdbMgr.HasStockDB() {
		return nil, nil
	}
	rep, err := p.duckdbMgr.GetFinancialAsOf(symbol, asOfDate)
	if err != nil || rep == nil {
		return nil, err
	}
	return &FinancialBar{
		ReportDate:   rep.ReportDate,
		AnnDate:      rep.AnnDate,
		TotalRevenue: rep.TotalRevenue,
		RevenueYOY:   rep.RevenueYOY,
		NetProfit:    rep.NetProfit,
		ProfitYOY:    rep.ProfitYOY,
		NetProfitDed: rep.NetProfitDed,
		Roe:          rep.Roe,
		GrossMargin:  rep.GrossMargin,
		NetMargin:    rep.NetMargin,
		TotalAssets:  rep.TotalAssets,
		TotalLiab:    rep.TotalLiab,
		DebtRatio:    rep.DebtRatio,
		OperCashflow: rep.OperCashflow,
		TotalShares:  rep.TotalShares,
		FloatShares:  rep.FloatShares,
		EPS:          rep.EPS,
		BPS:          rep.BPS,
	}, nil
}

// financialProviderFor 若 DuckDB 可用则返回财务提供者，否则返回 nil（不注入财务数据）。
// 仅基本面因子策略真正需要财务数据，其他策略注入后也不读取，开销可忽略。
func financialProviderFor(duckdbMgr *data.DuckDBManager) FinancialProvider {
	if duckdbMgr == nil || !duckdbMgr.HasStockDB() {
		log.Printf("[Backtest] DuckDB 不可用，跳过财务数据注入（基本面因子策略将无信号）")
		return nil
	}
	return NewDuckDBFinancialProvider(duckdbMgr)
}
