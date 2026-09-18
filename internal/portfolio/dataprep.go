package portfolio

// 投资组合优化的共享数据准备层。
// 前端"投资组合中心"与 Agent 的 optimize_portfolio 工具共用本函数，
// 保证两者输入同源（DuckDB 真实日K，公共交易日对齐），算法结果一致。

import (
	"context"
	"sort"
	"strings"

	"github.com/quantpilot/quantpilot/internal/data"
)

// AlignedAsset 对齐后的资产：代码/名称/收益序列/日均收益。与输入顺序一致。
type AlignedAsset struct {
	Code, Name string
	Series     []float64 // 按公共交易日对齐的日收益率序列（0-1 小数）
	Mean       float64   // 序列均值（日均收益）
}

// BuildAlignedSeries 拉取各标的历史日K，按公共交易日对齐为收益率序列。
// 数据来自 DuckDB 真实日K，严禁伪造；某资产数据不足时自动跳过。
// ids 与 markets 按位对齐；days 为收益回看窗口。返回至少 2 个有效对齐资产，否则为 nil。
func BuildAlignedSeries(ctx context.Context, dm *data.DuckDBManager, ids, markets []string, days int) []AlignedAsset {
	if dm == nil || len(ids) == 0 {
		return nil
	}
	n := len(ids)
	type perAsset struct {
		asset     AlignedAsset
		dateToIdx map[string]int
		closeVals []float64
		hasData   bool
	}
	per := make([]perAsset, n)
	dateSet := map[string]struct{}{}
	for i := 0; i < n; i++ {
		cur := perAsset{
			dateToIdx: map[string]int{},
			asset:     AlignedAsset{Code: ids[i], Name: resolveName(ctx, dm, ids[i])},
		}
		market := markets[i]
		if market == "" {
			market = "sz"
		}
		if prices, err := dm.GetRecentPrices(ctx, market, ids[i], days); err == nil && len(prices) >= 2 {
			sort.Slice(prices, func(x, y int) bool { return prices[x].TradeDate.Before(prices[y].TradeDate) })
			var dates []string
			closes := map[string]float64{}
			for _, p := range prices {
				ds := p.TradeDate.Format("2006-01-02")
				if _, seen := closes[ds]; !seen {
					dates = append(dates, ds)
				}
				closes[ds] = p.Close
				dateSet[ds] = struct{}{}
			}
			sort.Strings(dates)
			for k, d := range dates {
				cur.dateToIdx[d] = k
			}
			cur.closeVals = make([]float64, len(dates))
			for k, d := range dates {
				cur.closeVals[k] = closes[d]
			}
			cur.hasData = true
		}
		per[i] = cur
	}

	// 公共日期 = 所有有数据资产共同存在的交易日
	var common []string
	for d := range dateSet {
		found := true
		for _, p := range per {
			if p.hasData {
				if _, exists := p.dateToIdx[d]; !exists {
					found = false
					break
				}
			}
		}
		if found {
			common = append(common, d)
		}
	}
	sort.Strings(common)
	if len(common) < 3 {
		return nil
	}

	out := make([]AlignedAsset, 0, n)
	for _, p := range per {
		if !p.hasData {
			continue
		}
		series := make([]float64, 0, len(common)-1)
		sum := 0.0
		for k := 1; k < len(common); k++ {
			cPrev := p.closeVals[p.dateToIdx[common[k-1]]]
			cCur := p.closeVals[p.dateToIdx[common[k]]]
			if cPrev > 0 && cCur > 0 {
				r := (cCur - cPrev) / cPrev
				series = append(series, r)
			}
		}
		if len(series) < 2 {
			continue
		}
		for _, r := range series {
			sum += r
		}
		asset := p.asset
		asset.Series = series
		asset.Mean = sum / float64(len(series))
		out = append(out, asset)
	}
	return out
}

// InferMarket 依据代码推断市场（sh/sz/bj），无法识别时默认 sz。
func InferMarket(code string) string {
	l := strings.ToLower(code)
	if strings.HasPrefix(l, "sh") || strings.HasPrefix(l, "sz") || strings.HasPrefix(l, "bj") {
		return l[:2]
	}
	if strings.HasPrefix(l, "6") || strings.HasPrefix(l, "9") {
		return "sh"
	}
	return "sz"
}

// resolveName 尝试补齐标的名称（失败回退为空串，由调用方用代码兜底）。
func resolveName(ctx context.Context, dm *data.DuckDBManager, code string) string {
	names := map[string]string{"sh000300": "沪深300", "sh000905": "中证500", "sh000001": "上证指数", "sh000852": "中证1000"}
	if v, ok := names[strings.ToLower(code)]; ok {
		return v
	}
	if dm != nil {
		if s, err := dm.GetStockNameFromStock(ctx, code); err == nil && s != "" {
			return s
		}
	}
	return ""
}