package brainhost

import (
	"context"

	"github.com/quantpilot/quantpilot/internal/brain/port"
	"github.com/quantpilot/quantpilot/internal/data"
	"github.com/quantpilot/quantpilot/internal/llmmonitoring"
	"github.com/quantpilot/quantpilot/internal/screener"
)

// ==================== StockScreener 适配 ====================

// screenerAdapter 将宿主 internal/screener.ScreenerService 适配为决策脑抽象 port.StockScreener。
type screenerAdapter struct {
	inner *screener.ScreenerService
}

// AdaptStockScreener 将 screener.ScreenerService 包装为 port.StockScreener。
func AdaptStockScreener(s *screener.ScreenerService) port.StockScreener {
	return &screenerAdapter{inner: s}
}

func (a *screenerAdapter) ScreenStock(req port.ScreeningRequest) (port.ScreeningResponse, error) {
	dataReq, err := portToDataScreeningRequest(req)
	if err != nil {
		return port.ScreeningResponse{}, err
	}
	resp, err := a.inner.ScreenStock(dataReq)
	if err != nil {
		return port.ScreeningResponse{}, err
	}
	return dataToPortScreeningResponse(resp), nil
}

func portToDataScreeningRequest(req port.ScreeningRequest) (screener.ScreeningRequest, error) {
	out := screener.ScreeningRequest{
		StrategyID:    req.StrategyID,
		Market:        req.Market,
		MaxResults:    req.MaxResults,
		MinScore:      req.MinScore,
		SmallCapital:  req.SmallCapital,
		DiversifySeed: req.DiversifySeed,
	}
	if req.CustomWeights != nil {
		out.CustomWeights = make(map[screener.FactorID]float64, len(req.CustomWeights))
		for k, v := range req.CustomWeights {
			out.CustomWeights[screener.FactorID(k)] = v
		}
	}
	if req.InvestorProfile != nil {
		out.InvestorProfile = mapProfileToDataContact(req.InvestorProfile)
	}
	return out, nil
}

func dataToPortScreeningResponse(resp screener.ScreeningResponse) port.ScreeningResponse {
	out := port.ScreeningResponse{
		StrategyName: resp.StrategyName,
		TotalCount:   resp.TotalCount,
		GeneratedAt:  resp.GeneratedAt,
	}
	if resp.MarketState != nil {
		out.MarketState = &port.MarketState{
			Regime:      resp.MarketState.Regime,
			TrendScore:  resp.MarketState.TrendScore,
			Volatility:  resp.MarketState.Volatility,
			Breadth:     resp.MarketState.Breadth,
			Description: resp.MarketState.Description,
		}
	}
	out.Results = make([]port.StockScore, 0, len(resp.Results))
	for _, r := range resp.Results {
		out.Results = append(out.Results, dataToPortStockScore(r))
	}
	return out
}

func dataToPortStockScore(r screener.StockScore) port.StockScore {
	fs := make(map[port.FactorID]port.FactorScore, len(r.FactorScores))
	for fid, sc := range r.FactorScores {
		fs[port.FactorID(fid)] = port.FactorScore{
			FactorID:    port.FactorID(sc.FactorID),
			FactorName:  sc.FactorName,
			Score:       sc.Score,
			Contributor: sc.Contributor,
			Breakdown:   sc.Breakdown,
		}
	}
	return port.StockScore{
		Code:         r.Code,
		Name:         r.Name,
		Market:       r.Market,
		Price:        r.Price,
		ChangePct:    r.ChangePct,
		TotalScore:   r.TotalScore,
		FactorScores: fs,
		Ranking:      r.Ranking,
		Reasons:      r.Reasons,
		Warnings:     r.Warnings,
	}
}

// mapProfileToDataContact 将决策脑画像 DTO 转回宿主 data.InvestorProfile（选股智能权重所需）。
func mapProfileToDataContact(p *port.InvestorProfile) *data.InvestorProfile {
	return portToDataProfile(p)
}

// ==================== StockMeta 适配 ====================

// stockMetaAdapter 将宿主 internal/data.DictLoader 适配为决策脑抽象 port.StockMeta。
type stockMetaAdapter struct {
	inner *data.DictLoader
}

// AdaptStockMeta 将 data.DictLoader 包装为 port.StockMeta。
func AdaptStockMeta(dl *data.DictLoader) port.StockMeta {
	return &stockMetaAdapter{inner: dl}
}

func (a *stockMetaAdapter) GetStockName(code string) string {
	return a.inner.GetStockName(code)
}

func (a *stockMetaAdapter) GetIndustryByStock(code string) string {
	return a.inner.GetIndustryByStock(code)
}

// ==================== CallMetaDecorator 适配 ====================

// AdaptCallMetaDecoratorLlmtrace 将宿主 internal/llmmonitoring.WithCallMeta 适配为决策脑
// port.CallMetaDecorator，使决策脑 LLM 调用仍能为审计功能附加元信息。
func AdaptCallMetaDecoratorLlmtrace() port.CallMetaDecorator {
	return func(ctx context.Context, meta port.CallMeta) context.Context {
		return llmmonitoring.WithCallMeta(ctx, llmmonitoring.CallMeta{
			AgentRole: meta.AgentRole,
			TaskName:  meta.TaskName,
			Phase:     meta.Phase,
		})
	}
}
