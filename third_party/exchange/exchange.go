package exchange

import (
	"sync"
	"time"
)

// Operator 操作员接口
type Operator interface {
	// Kind 取得ms所在时段的操作类别和序号
	//	ms是nil的话, 即默认值, 取当前时间戳的毫秒数
	Kind(ms ...int64) (kind TimeKind, index int)
}

var onceMarketHoursOperator sync.Once
var mapExchangeMarketHours = map[string]MarketHours{}

type MarketHoursOperator struct {
}

var marketHoursOperator = MarketHoursOperator{}

// Kind 实现了Operator接口的Kind方法
func (o MarketHoursOperator) Kind(ms ...int64) (kind TimeKind, index int) {
	var timestamp int64
	if len(ms) > 0 {
		timestamp = ms[0]
	} else {
		timestamp = time.Now().UnixNano() / int64(time.Millisecond)
	}

	marketHours := mapExchangeMarketHours[CN]
	kind, index, _ = marketHours.GetTimeKind(timestamp)

	return kind, index
}

// GetOperator 获取一个Operator实例
func GetOperator() Operator {
	return marketHoursOperator
}

//const (
//	MarketShangHai string = "sh" // 上海
//	MarketShenZhen string = "sz" // 深圳
//	MarketBeiJing  string = "bj" // 北京
//	MarketHongKong string = "hk" // 香港
//	MarketUSA      string = "us" // 美国
//)

const (
	CN = "cn" // A股
	HK = "hk" // 港股
	US = "us" // 美股
)

type exchanges struct {
	sessions map[string]TradingSession `name:"交易时段" yaml:"sessions"`
}

var (
	onceExchange sync.Once
	mapExchange  = map[string]TradingSession{}
)

func lazyLoadExchangeMarketHours() {
	text := "CAAC|09:15:00-09:20:00, CA|09:20:00-09:25:00, TAC|09:30:00-11:30:00, TAC|13:00:00-14:57:00, CAAT|14:57:00-15:00:00"
	var marketHours MarketHours
	marketHours.Parse(text)

	mapExchangeMarketHours[CN] = marketHours
}

func GetExchangeMarketHours(name string) MarketHours {
	onceMarketHoursOperator.Do(lazyLoadExchangeMarketHours)
	return mapExchangeMarketHours[name]
}

// 加载配置文件
func lazyLoadExchanges() {
	cn := ExchangeSessions(
		ExchangeTime(CallAuctionAndCancel, "09:15:00", "09:20:00"), // 竞价, 可撤单
		ExchangeTime(CallAuction, "09:20:00", "09:25:00"),          // 竞价, 不可撤单
		ExchangeTime(TradingAndCancel, "09:30:00", "11:30:00"),     // 交易, 可撤单
		ExchangeTime(TradingAndCancel, "13:00:00", "14:57:00"),     // 交易, 可撤单
		ExchangeTime(CallAuction|Trading, "14:57:00", "15:00:00"),  // 竞价, 交易, 不可撤单
	)
	mapExchange[CN] = cn
}

func GetExchange(name string) TradingSession {
	onceExchange.Do(lazyLoadExchanges)
	return mapExchange[name]
}
