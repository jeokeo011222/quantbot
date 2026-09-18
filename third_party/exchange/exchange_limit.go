package exchange

import (
	"github.com/quant1x/num"
	"github.com/quant1x/x/api"
)

// MarketLimit 涨跌停板限制
func MarketLimit(securityCode string) float64 {
	_, flag, shortCode := DetectMarket(securityCode)
	if flag == MarketBeiJing {
		return 0.30
	}
	if api.StartsWith(shortCode, []string{"30", "68"}) {
		return 0.20
	}
	return 0.10
}

// LimitUp 返回涨停板价格
func LimitUp(securityCode string, price float64) float64 {
	limit := MarketLimit(securityCode)
	lastClose := num.Decimal(price)
	upStopPrice := num.Decimal(lastClose * (1.0000 + limit))
	return upStopPrice
}
