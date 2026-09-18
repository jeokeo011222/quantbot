package data

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/quantpilot/quantpilot/internal/thssdk"
)

// ==================== 同花顺官方财务数据拉取器（财务同步源切换） ====================
//
// 通过 thssdk 拉取官方利润表/资产负债表/现金流量表，合并为内部 FinancialReport，
// 供 StartFinancialSync(source="ths") 使用。数据全部来自同花顺官方真实报表，严禁伪造。
//
// 与东财/通达信源的差异（官方报表接口字段限制，缺失字段置 0 表示未披露）：
//   - 不提供 总股本/流通股本 → TotalShares/FloatShares = 0
//   - 不提供 每股净资产 → BPS = 0（可用「归母净资产/股本」需股本数据，暂不推导）
//   - 不提供 扣非净利润 → NetProfitDed = 0
//   - ROE 用「归母净利润/归母净资产×100」口径（摊薄），毛利率/净利率/资产负债率由报表派生
//   - 同比(RevenueYOY/ProfitYOY)由相邻年度同一季度报告期计算（无则 0）

// thsF 解引用可空浮点字段（null 视为 0，表示未披露）。
func thsF(v *float64) float64 {
	if v == nil {
		return 0
	}
	return *v
}

// thsDate ms 时间戳 → YYYY-MM-DD（<=0 返回空串）。
func thsDate(ms int64) string {
	if ms <= 0 {
		return ""
	}
	return time.UnixMilli(ms).Format("2006-01-02")
}

// NewTHSFinancialFetcher 构建同花顺官方财务数据拉取器（FinancialFetcher 接口实现）。
// 需先启用并配置 ths_source.APIKey（thssdk.Configure）；未配置时返回错误提示，不会伪造数据。
func NewTHSFinancialFetcher() FinancialFetcher {
	return func(ctx context.Context, symbol, startReport string) ([]FinancialReport, error) {
		c := thssdk.Default()
		if c == nil {
			return nil, fmt.Errorf("同花顺官方数据源未配置（请在设置-数据源中启用并填写 API Key）")
		}
		thscode := thssdk.ToTHSCode(symbol)

		income, err := c.IncomeStatements(ctx, thscode, "quarterly", 20)
		if err != nil {
			var ae *thssdk.APIError
			// 官方不识别该代码（已退市/停牌/无产品数据，code=1002）→ 视为该股无财务数据，直接跳过而非报错，
			// 避免拖慢全市场同步并耗尽同步总超时（Context deadline exceeded）。
			if errors.As(err, &ae) && ae.Code == 1002 {
				return nil, nil
			}
			return nil, fmt.Errorf("同花顺利润表拉取失败(%s): %w", thscode, err)
		}
		balance, err := c.BalanceSheets(ctx, thscode, "quarterly", 20)
		if err != nil {
			return nil, fmt.Errorf("同花顺资产负债表拉取失败(%s): %w", thscode, err)
		}
		cashflow, err := c.CashFlowStatements(ctx, thscode, "quarterly", 20)
		if err != nil {
			return nil, fmt.Errorf("同花顺现金流量表拉取失败(%s): %w", thscode, err)
		}

		balByDate := make(map[string]thssdk.BalanceSheet, len(balance))
		for _, b := range balance {
			if d := thsDate(b.PeriodEndMs); d != "" {
				balByDate[d] = b
			}
		}
		cashByDate := make(map[string]thssdk.CashFlowStatement, len(cashflow))
		for _, cf := range cashflow {
			if d := thsDate(cf.PeriodEndMs); d != "" {
				cashByDate[d] = cf
			}
		}

		var reports []FinancialReport
		for _, is := range income {
			rd := thsDate(is.PeriodEndMs)
			if rd == "" {
				continue
			}
			if startReport != "" && rd < startReport {
				continue
			}
			rep := FinancialReport{
				Symbol:     symbol,
				ReportDate: rd,
				AnnDate:    thsDate(is.ReportDateMs),
				EPS:        thsF(is.BasicEPS),
			}
			// 利润表：营收/归母净利/毛利净利率
			rep.TotalRevenue = thsF(is.OperatingIncome)
			rep.NetProfit = thsF(is.ParentHolderNetProfit)
			opIncome := thsF(is.OperatingIncome)
			if opIncome > 0 {
				rep.GrossMargin = (opIncome - thsF(is.OperatingCosts)) / opIncome * 100
				rep.NetMargin = rep.NetProfit / opIncome * 100
			}
			// 资产负债表：总资产/总负债/资产负债率/ROE（摊薄口径）
			if b, ok := balByDate[rd]; ok {
				rep.TotalAssets = thsF(b.AssetsTotal)
				rep.TotalLiab = thsF(b.TotalDebt)
				if thsF(b.AssetsTotal) > 0 {
					rep.DebtRatio = thsF(b.TotalDebt) / thsF(b.AssetsTotal) * 100
				}
				if thsF(b.HolderEquityTotal) > 0 {
					rep.Roe = rep.NetProfit / thsF(b.HolderEquityTotal) * 100
				}
			}
			// 现金流量表：经营现金流净额
			if cf, ok := cashByDate[rd]; ok {
				rep.OperCashflow = thsF(cf.ActCashFlowNet)
			}
			reports = append(reports, rep)
		}

		// 按报告期升序，便于计算同比（与 4 个季度前的同一报告期比较）
		sort.Slice(reports, func(i, j int) bool { return reports[i].ReportDate < reports[j].ReportDate })
		for i := range reports {
			if i >= 4 {
				prev := reports[i-4]
				if prev.TotalRevenue > 0 {
					reports[i].RevenueYOY = (reports[i].TotalRevenue - prev.TotalRevenue) / prev.TotalRevenue * 100
				}
				if prev.NetProfit > 0 {
					reports[i].ProfitYOY = (reports[i].NetProfit - prev.NetProfit) / prev.NetProfit * 100
				}
			}
		}
		return reports, nil
	}
}
