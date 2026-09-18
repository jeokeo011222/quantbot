package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"strings"
	"time"

	"github.com/quantpilot/quantpilot/internal/data"
	"github.com/quantpilot/quantpilot/internal/portfolio"
)

// OptimizePortfolioRequest 投资组合优化请求（"投资组合中心"）。
type OptimizePortfolioRequest struct {
	Method      string    `json:"method"`      // mvo / risk_parity / risk_budget
	Symbols     []string  `json:"symbols"`     // 空 = 使用当前持仓自动填充
	TotalWeight float64   `json:"totalWeight"` // 分配给风险资产的合计比重（默认0.9，其余现金）
	MaxSingle   float64   `json:"maxSingle"`   // 单资产上限（默认0.3）
	Budgets     []float64 `json:"budgets"`     // risk_budget 专用风险预算（可空=等预算）
	Days        int       `json:"days"`        // 收益回看窗口（默认90）
}

// optWeightItem 优化后单个标的的目标权重条目。
type optWeightItem struct {
	Code       string  `json:"code"`
	Name       string  `json:"name"`
	Weight     float64 `json:"weight"`
	RiskPct    float64 `json:"riskPct"`
	SuggestAmt float64 `json:"suggestAmt"`
	AnnualVol  float64 `json:"annualVol"`
}

// OptimizePortfolio 运行组合权重优化：三种算法（mvo/risk_parity/risk_budget）可切换。
// 基于真实持仓/标的日K构造收益协方差，在全局约束（Σw=TotalWeight、单资产上限）下求最优权重，
// 结果落库 portfolio_optimizations 供历史回看。智能体与"投资组合中心"页面共用此入口。
func (a *App) OptimizePortfolio(req OptimizePortfolioRequest) (interface{}, error) {
	if req.Method == "" {
		req.Method = "risk_parity"
	}
	totalW := req.TotalWeight
	if totalW <= 0 || totalW > 1 {
		totalW = 0.9
	}
	maxSingle := req.MaxSingle
	if maxSingle <= 0 || maxSingle > totalW {
		maxSingle = 0.30
	}
	days := req.Days
	if days <= 0 || days > 500 {
		days = 90
	}

	// 1) 决定参与优化的资产（代码 / 市场 / 名称）
	ids, markets, holdNames, isCurrent := a.resolveOptimizeUniverse(req.Symbols)

	// 2) 拉取真实日收益并对齐为公共交易日序列（与 Agent optimize_portfolio 共用）
	assets := portfolio.BuildAlignedSeries(context.Background(), a.duckdbManager, ids, markets, days)
	if len(assets) < 2 {
		return map[string]interface{}{
			"code": "no_data", "message": "标的不足（至少2个且有对齐的K线历史）", "symbols": ids,
		}, nil
	}

	n := len(assets)
	rets := make([][]float64, n)
	expRet := make([]float64, n)
	for i, as := range assets {
		rets[i] = as.Series
		expRet[i] = as.Mean
	}
	logo := "投资组合中心优化"
	if isCurrent {
		logo = "基于当前持仓"
	}
	log.Printf("[Optimize] method=%s assets=%d 窗口=%d天 totalWeight=%.2f 来源=%s", req.Method, n, days, totalW, logo)

	cov := portfolio.EstimateCovariance(rets)

	lo := make([]float64, n)
	hi := make([]float64, n)
	for i := 0; i < n; i++ {
		lo[i] = 0
		hi[i] = maxSingle
	}
	var budgets []float64
	if req.Method == "risk_budget" && len(req.Budgets) == n {
		budgets = req.Budgets
	}

	res, err := portfolio.Optimize(portfolio.OptimizeRequest{
		Method:       portfolio.OptimizeMethod(req.Method),
		Cov:          cov,
		Returns:      expRet,
		MinWeight:    lo,
		MaxWeight:    hi,
		TotalWeight:  totalW,
		Budgets:      budgets,
		RiskAversion: 4.0,
	})
	if err != nil || res == nil {
		return nil, fmt.Errorf("组合优化失败: %v", err)
	}

	// 3) 组装结果（附代码/名称/建议金额）
	capital := 0.0
	if a.portfolioEngine != nil {
		if s := a.portfolioEngine.GetSnapshot(); s != nil {
			capital = s.Cash + s.TotalMarketValue
		}
	}
	items := make([]optWeightItem, 0, n)
	for i := 0; i < n; i++ {
		name := assets[i].Name
		if name == "" && i < len(holdNames) {
			name = holdNames[i] // 优先用当前持仓快照的名称
		}
		if name == "" {
			name = assets[i].Code
		}
		items = append(items, optWeightItem{
			Code:       assets[i].Code,
			Name:       name,
			Weight:     round4(res.Weights[i]),
			RiskPct:    round4(res.RiskCtrPercent[i]),
			SuggestAmt: math.Round(res.Weights[i] * capital),
			AnnualVol:  round4(res.Volatility),
		})
	}

	// 4) 落库
	a.saveOptimization(req.Method, items, totalW, res, isCurrent, logo)

	return map[string]interface{}{
		"code":             "ok",
		"method":           string(res.Method),
		"totalWeight":      round4(totalW),
		"volatility":       round4(res.Volatility),
		"annualReturn":     round4(res.AnnualReturn),
		"sharpe":           round4(res.Sharpe),
		"weights":          items,
		"riskContribution": roundSlice(res.RiskContribution),
		"isCurrentHold":    isCurrent,
		"capital":          math.Round(capital),
		"timestamp":        time.Now().Format(time.RFC3339),
	}, nil
}

// GetPortfolioOptimizations 读取最近 limit 条组合优化记录（倒序）。
func (a *App) GetPortfolioOptimizations(limit int) (interface{}, error) {
	if limit <= 0 {
		limit = 10
	}
	if a.sqliteManager == nil {
		return map[string]interface{}{"limit": limit, "records": []interface{}{}}, nil
	}
	var recs []data.PortfolioOptimization
	if err := a.sqliteManager.GetDB().Order("created_at DESC").Limit(limit).Find(&recs).Error; err != nil {
		return nil, fmt.Errorf("读取优化记录失败: %w", err)
	}
	out := make([]map[string]interface{}, 0, len(recs))
	for _, r := range recs {
		out = append(out, flattenOptimizationRecord(r))
	}
	return map[string]interface{}{"limit": limit, "records": out}, nil
}

// resolveOptimizeUniverse 决定优化资产清单。
func (a *App) resolveOptimizeUniverse(symbols []string) (ids, markets, names []string, isCurrent bool) {
	symbols = cleanNonEmpty(symbols)
	if len(symbols) > 0 {
		for _, s := range symbols {
			ids = append(ids, s)
			markets = append(markets, inferMarket(s))
			names = append(names, "")
		}
		return ids, markets, names, false
	}
	if a.portfolioEngine != nil {
		if snap := a.portfolioEngine.GetSnapshot(); snap != nil {
			for _, p := range snap.Positions {
				if p == nil || p.InstrumentID == "" {
					continue
				}
				ids = append(ids, p.InstrumentID)
				markets = append(markets, p.Market)
				names = append(names, p.StockName)
			}
		}
	}
	if len(ids) == 0 {
		return nil, nil, nil, false
	}
	return ids, markets, names, true
}

// saveOptimization 落库一次优化记录。
func (a *App) saveOptimization(method string, items []optWeightItem, totalW float64, res *portfolio.OptimizeResult, isCurrent bool, remark string) {
	if a.sqliteManager == nil {
		return
	}
	symbolsJSON, _ := json.Marshal(items)
	rec := data.PortfolioOptimization{
		Method:        method,
		Symbols:       string(symbolsJSON),
		Weights:       string(symbolsJSON),
		TotalWeight:   totalW,
		AnnualReturn:  res.AnnualReturn,
		Volatility:    res.Volatility,
		Sharpe:        res.Sharpe,
		IsCurrentHold: isCurrent,
		Remark:        remark,
		CreatedAt:     time.Now(),
	}
	if err := a.sqliteManager.GetDB().Create(&rec).Error; err != nil {
		log.Printf("[Optimize] 落库失败: %v", err)
	}
}

// flattenOptimizationRecord 展开一条优化记录为前端友好 map。
func flattenOptimizationRecord(r data.PortfolioOptimization) map[string]interface{} {
	m := map[string]interface{}{
		"id":              r.ID,
		"method":          r.Method,
		"total_weight":    r.TotalWeight,
		"annual_return":   r.AnnualReturn,
		"volatility":      r.Volatility,
		"sharpe":          r.Sharpe,
		"is_current_hold": r.IsCurrentHold,
		"remark":          r.Remark,
		"created_at":      r.CreatedAt.Format("2006-01-02 15:04"),
	}
	if r.Weights != "" {
		var items []optWeightItem
		if json.Unmarshal([]byte(r.Weights), &items) == nil {
			m["weights"] = items
		}
	}
	return m
}

// ---------- 小工具 ----------

func cleanNonEmpty(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func inferMarket(code string) string {
	l := strings.ToLower(code)
	if strings.HasPrefix(l, "sh") || strings.HasPrefix(l, "sz") || strings.HasPrefix(l, "bj") {
		return l[:2]
	}
	if strings.HasPrefix(l, "6") || strings.HasPrefix(l, "9") {
		return "sh"
	}
	return "sz"
}

func round4(v float64) float64 { return math.Round(v*10000) / 10000 }

func roundSlice(v []float64) []float64 {
	out := make([]float64, len(v))
	for i := range v {
		out[i] = round4(v[i])
	}
	return out
}
