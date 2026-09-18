package tools

import (
	"fmt"

	"github.com/quantpilot/quantpilot/internal/data"
)

// TencentMarketDataProvider 以腾讯财经为实时行情主数据源的提供者，
// 直接请求 qt.gtimg.cn 获取真实实时行情（非 TDX、非模拟、非伪造），
// 完整实现统一数据源接口 UnifiedDataSource（快照/报价/日线/K线）。
//
// 数据源调用规范：本 provider 仅由 data.SetDataSource(...) 在用户选择"腾讯财经"时由
// 编排层注册，业务方一律通过 data.GetDataSource().Xxx() 调用，禁止直接 new 本类型。
type TencentMarketDataProvider struct{}

// NewTencentMarketDataProvider 创建腾讯财经行情数据提供者
func NewTencentMarketDataProvider() *TencentMarketDataProvider {
	return &TencentMarketDataProvider{}
}

// Source 返回数据源名称
func (p *TencentMarketDataProvider) Source() string { return data.DataProviderTencentName }

// GetStockSnapshots 直接从腾讯财经获取股票实时快照
func (p *TencentMarketDataProvider) GetStockSnapshots(codes []string) ([]data.StockSnapshot, error) {
	if len(codes) == 0 {
		return nil, fmt.Errorf("empty codes")
	}
	return data.FetchRealStockSnapshots(data.BuildTencentCodes(codes))
}

// GetIndexSnapshots 直接从腾讯财经获取指数实时快照
func (p *TencentMarketDataProvider) GetIndexSnapshots(codes []string) ([]data.MarketIndexSnapshot, error) {
	if len(codes) == 0 {
		return nil, fmt.Errorf("empty index codes")
	}
	return data.FetchRealIndexSnapshots(codes)
}

// GetQuote 直接从腾讯财经获取单只实时报价
func (p *TencentMarketDataProvider) GetQuote(code string) (data.RealTimeQuote, error) {
	if code == "" {
		return data.RealTimeQuote{}, fmt.Errorf("empty code")
	}
	snaps, err := data.FetchRealStockSnapshots(data.BuildTencentCodes([]string{code}))
	if err != nil {
		return data.RealTimeQuote{}, err
	}
	if len(snaps) == 0 {
		return data.RealTimeQuote{}, fmt.Errorf("tencent quote for %s is empty", code)
	}
	s := snaps[0]
	change := s.ChangeAmount
	changePct := s.ChangePercent
	if s.PrevClose > 0 {
		change = s.CurrentPrice - s.PrevClose
		changePct = (change / s.PrevClose) * 100
	}
	return data.RealTimeQuote{
		Code:       s.Code,
		Name:       s.Name,
		Price:      s.CurrentPrice,
		Open:       s.Open,
		High:       s.High,
		Low:        s.Low,
		PrevClose:  s.PrevClose,
		Volume:     s.Volume,
		Amount:     s.Turnover,
		Change:     change,
		ChangePct:  changePct,
		Source:     data.DataProviderTencentName,
		IsRealtime: true,
	}, nil
}

// GetStockData 腾讯日线（含技术指标）
func (p *TencentMarketDataProvider) GetStockData(exchange, code string, days int, includeIndicators bool) (map[string]interface{}, error) {
	return tencentStockDataMap(exchange, code, days)
}

// GetKline 腾讯K线（day/week/month/m5/m15/m30/m60）
func (p *TencentMarketDataProvider) GetKline(exchange, code, period string, count int, adjust string) (map[string]interface{}, error) {
	return tencentKlineMap(exchange, code, period, count, adjust)
}
