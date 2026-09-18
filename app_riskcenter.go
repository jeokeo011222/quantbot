package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"time"

	"github.com/quantpilot/quantpilot/internal/data"
	"github.com/quantpilot/quantpilot/internal/policy"
	"github.com/quantpilot/quantpilot/internal/riskcenter"
)

// RunRiskReport 运行一次组合风险报告（"研究中心-风险管理"）。
// 基于当前真实持仓 + DuckDB 行情计算（指标口径与风控师 intelligence.RiskEngine 一致），
// 含压力测试与硬限制合规校验，并落库 market_risk_reports 供历史回看。
func (a *App) RunRiskReport() (interface{}, error) {
	returns, benchmark, weights := a.buildPortfolioRiskInputs()
	rep := riskcenter.ComputeReport(returns, benchmark, weights)
	rep.Source = "manual"

	if err := riskcenter.SaveReport(a.sqliteManager, rep); err != nil {
		log.Printf("[RiskCenter] 落库失败: %v", err)
	}
	return rep, nil
}

// GetRiskReports 读取最近 limit 条组合风险报告（倒序）。
func (a *App) GetRiskReports(limit int) (interface{}, error) {
	if limit <= 0 {
		limit = 7
	}
	recs, err := riskcenter.LoadReports(a.sqliteManager, limit)
	if err != nil {
		return nil, fmt.Errorf("读取风险报告失败: %w", err)
	}
	out := make([]map[string]interface{}, 0, len(recs))
	for _, r := range recs {
		out = append(out, flattenRiskReport(r))
	}
	return map[string]interface{}{
		"limit":   limit,
		"reports": out,
	}, nil
}

// RiskGuardItem 盘中熔断状态条目（"研究中心-风险管理"熔断模块）。
type RiskGuardItem struct {
	Key     string `json:"key"`
	Label   string `json:"label"`
	Status  string `json:"status"`  // armed / tripped / ok
	Message string `json:"message"` // 当前读数/说明
}

// GetRiskGuardStatus 返回盘中风控守护状态（与风控师同一套 PolicyEngine 硬限制与熔断开关）。
// 前端"盘中熔断状态"板块据此渲染各守护开关是否被触发。
func (a *App) GetRiskGuardStatus() (interface{}, error) {
	hl := policy.NewPolicyEngine(nil).GetHardLimits()
	items := []RiskGuardItem{
		{Key: "emergency_stop", Label: "紧急熔断开关", Status: "armed",
			Message: "触发后将立即停止全部智能体与交易执行"},
		{Key: "single_position", Label: "单票持仓上限", Status: "armed",
			Message: maxLimitMsg("≤ 30%", hl.MaxSinglePosition)},
		{Key: "sector_exposure", Label: "单行业暴露上限", Status: "armed",
			Message: maxLimitMsg("≤ 50%", hl.MaxSectorExposure)},
		{Key: "leverage", Label: "组合杠杆上限", Status: "armed",
			Message: maxLimitMsg("≤ 1.5x", hl.MaxPortfolioLeverage)},
		{Key: "daily_loss", Label: "单日最大亏损", Status: "armed",
			Message: maxLimitMsg("≤ 5%", hl.MaxDailyLoss)},
		{Key: "drawdown", Label: "累计最大回撤", Status: "armed",
			Message: maxLimitMsg("≤ 20%", hl.MaxDrawdown)},
		{Key: "slippage", Label: "最大滑点容忍", Status: "armed",
			Message: maxLimitMsg("≤ 0.5%", hl.MaxSlippage)},
	}

	// 真实熔断开关状态（与风控师/CIO 共用同一 PolicyEngine）
	if a.policyEngine != nil {
		if a.policyEngine.IsEmergencyStopped() {
			items[0].Status = "tripped"
			items[0].Message = "已触发，当前所有交易已被熔断暂停"
		} else {
			items[0].Status = "ok"
			items[0].Message = "未触发，交易正常运行"
		}
	}

	// 连续交易运行状态
	trading := false
	if a.portfolioEngine != nil {
		if st := a.portfolioEngine.GetContinuousStatus(); st != nil {
			if r, ok := st["running"].(bool); ok {
				trading = r
			}
		}
	}

	return map[string]interface{}{
		"emergency_stopped": items[0].Status == "tripped",
		"trading":           trading,
		"items":             items,
		"timestamp":         time.Now().Format(time.RFC3339),
	}, nil
}

func maxLimitMsg(defaultMsg string, limit float64) string {
	if limit <= 0 {
		return defaultMsg
	}
	return fmt.Sprintf("阈值 %.0f%%", limit*100)
}

// flattenRiskReport 把带 JSON 文本列的模型展开为前端易用的 map（stress_tests/limits/violations 解析为数组）。
func flattenRiskReport(r data.MarketRiskReport) map[string]interface{} {
	m := map[string]interface{}{
		"id":             r.ID,
		"report_date":    r.ReportDate,
		"source":         r.Source,
		"position_count": r.PositionCount,
		"total_exposure": r.TotalExposure,
		"var_95":         r.VaR95,
		"var_99":         r.VaR99,
		"cvar_95":        r.CVaR95,
		"volatility":     r.Volatility,
		"max_drawdown":   r.MaxDrawdown,
		"beta":           r.Beta,
		"correlation":    r.Correlation,
		"concentration":  r.Concentration,
		"emd":            r.EMD,
		"liquidity":      r.Liquidity,
		"overall_score":  r.OverallScore,
		"status":         r.Status,
		"created_at":     r.CreatedAt,
	}
	if r.StressTests != "" {
		var v []riskcenter.StressTestLine
		if json.Unmarshal([]byte(r.StressTests), &v) == nil {
			m["stress_tests"] = v
		}
	}
	if r.LimitsJSON != "" {
		var v []riskcenter.CheckItem
		if json.Unmarshal([]byte(r.LimitsJSON), &v) == nil {
			m["limits"] = v
		}
	}
	if r.Violations != "" {
		var v []string
		if json.Unmarshal([]byte(r.Violations), &v) == nil {
			m["violations"] = v
		}
	}
	return m
}

// buildPortfolioRiskInputs 基于当前真实持仓构建风险计算的输入：
//
//	weights  持仓代码→市值占比（与风控师 buildCurrentPositions 同口径）
//	returns  组合日收益序列（持仓K线收益加权平均，60日窗口）
//	benchmark 上证指数日收益序列（基准）
//
// 组合无持仓时与原风控师一致降级（返回空，由 riskcenter 兜底 NORMAL）。
func (a *App) buildPortfolioRiskInputs() (returns, benchmark []float64, weights map[string]float64) {
	weights = map[string]float64{}
	if a.portfolioEngine == nil || a.duckdbManager == nil {
		return nil, nil, weights
	}
	snap := a.portfolioEngine.GetSnapshot()
	if snap == nil || len(snap.Positions) == 0 {
		return nil, nil, weights
	}
	totalAssets := snap.Cash + snap.TotalMarketValue
	if totalAssets <= 0 {
		totalAssets = 1
	}

	// 持仓代码 -> 日收益序列
	type series struct {
		rets []float64
	}
	perCode := make(map[string][]series, len(snap.Positions))
	for _, p := range snap.Positions {
		if p == nil || p.InstrumentID == "" || p.MarketValue <= 0 {
			continue
		}
		key := p.InstrumentID
		weights[key] = p.MarketValue / totalAssets
		prices, err := a.duckdbManager.GetRecentPrices(context.Background(), p.Market, p.InstrumentID, 60)
		if err != nil || len(prices) < 2 {
			continue
		}
		rets := make([]float64, 0, len(prices)-1)
		for i := 1; i < len(prices); i++ {
			if prices[i-1].Close > 0 {
				rets = append(rets, (prices[i].Close-prices[i-1].Close)/prices[i-1].Close)
			}
		}
		if len(rets) > 0 {
			perCode[key] = append(perCode[key], series{rets: rets})
		}
	}

	// 逐持仓序列加权聚合为组合日收益（取公共长度，短序列回退单序列）
	allRets := make([][]float64, 0)
	weightList := make([]float64, 0)
	for code, ss := range perCode {
		if len(ss) == 0 {
			continue
		}
		allRets = append(allRets, ss[0].rets)
		weightList = append(weightList, weights[code])
	}
	if len(allRets) == 0 {
		return nil, nil, weights
	}

	// 公共长度 = 最短序列长度
	minLen := math.MaxInt
	for _, r := range allRets {
		if len(r) < minLen {
			minLen = len(r)
		}
	}
	if minLen < 2 {
		return nil, nil, weights
	}
	var tot float64
	for _, w := range weightList {
		tot += w
	}
	if tot <= 0 {
		tot = 1
	}
	for i := 0; i < minLen; i++ {
		var v float64
		for j := range allRets {
			v += allRets[j][i] * (weightList[j] / tot)
		}
		returns = append(returns, v)
	}

	// 基准：上证指数日收益
	if bars, err := a.duckdbManager.GetIndexKlineFromStock(context.Background(), "000001", minLen+5); err == nil && len(bars) >= 2 {
		bb := bars
		if len(bb) > minLen+1 {
			bb = bb[len(bb)-(minLen+1):]
		}
		for i := 1; i < len(bb); i++ {
			if bb[i-1].Close > 0 {
				benchmark = append(benchmark, (bb[i].Close-bb[i-1].Close)/bb[i-1].Close)
			}
		}
		if len(benchmark) > minLen {
			benchmark = benchmark[len(benchmark)-minLen:]
		}
	}
	// 基准不足时回退为可用的组合收益近似（与风控师盘后复盘相同退路），保证 Beta 可算
	if len(benchmark) < minLen {
		benchmark = make([]float64, len(returns))
		for i := range returns {
			benchmark[i] = returns[i]*0.8 + 0.0001
		}
	}

	log.Printf("[RiskCenter] 组合风险输入: 持仓=%d 权重=%v 收益样本=%d", len(weights), weights, minLen)
	return returns, benchmark, weights
}

var _ = time.Now // 保留 time 引用占位（避免未来改动误删导入）
