package exchange

type TradeDate string

func NewTradeDate(date string) TradeDate {
	tradeDate := FixTradeDate(date)
	return TradeDate(tradeDate)
}

func (t TradeDate) String() string {
	return string(t)
}
