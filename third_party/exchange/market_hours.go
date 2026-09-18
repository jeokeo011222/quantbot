package exchange

import (
	"errors"
	"slices"
	"strings"

	"github.com/quant1x/pkg/yaml"
	"github.com/quant1x/x/api"
	"github.com/quant1x/x/timestamp"
)

// MarketHours 交易时段
type MarketHours struct {
	sessions []TradingTimeRange
}

func ExchangeMarketHours(sessions ...TradingTimeRange) MarketHours {
	return MarketHours{sessions: sessions}
}

func (this MarketHours) String() string {
	builder := strings.Builder{}
	builder.WriteByte('[')
	var arr []string
	for _, timeRange := range this.sessions {
		arr = append(arr, timeRange.String())
	}
	builder.WriteString(strings.Join(arr, ","))
	builder.WriteByte(']')
	return builder.String()
}

func (this MarketHours) v2String() string {
	builder := strings.Builder{}
	//builder.WriteByte('[')
	var arr []string
	for _, timeRange := range this.sessions {
		arr = append(arr, timeRange.String())
	}
	builder.WriteString(strings.Join(arr, ","))
	//builder.WriteByte(']')
	return builder.String()
}

func (this MarketHours) Minutes() int {
	minutes := 0
	for _, v := range this.sessions {
		minutes += v.Minutes()
	}
	return minutes
}

func (this *MarketHours) Parse(text string) error {
	var sessions []TradingTimeRange
	text = strings.TrimSpace(text)
	arr := arrayRegexp.Split(text, -1)
	for _, v := range arr {
		var tr TradingTimeRange
		err := tr.Parse(v)
		if err != nil {
			return err
		}
		sessions = append(sessions, tr)
	}

	if len(sessions) == 0 {
		return ErrTimeFormat
	}

	slices.SortFunc(sessions, func(a, b TradingTimeRange) int {
		if a.begin < b.begin {
			return -1
		} else if a.begin > b.begin {
			return 1
		} else if a.end < b.end {
			return -1
		} else if a.end == b.end {
			return 0
		} else {
			return 1
		}
	})

	this.sessions = sessions
	return nil
}

func (this MarketHours) MarshalText() (text []byte, err error) {
	str := this.String()
	return api.String2Bytes(str), nil
}

// UnmarshalYAML YAML自定义解析
func (this *MarketHours) UnmarshalYAML(node *yaml.Node) error {
	var value string
	if len(node.Content) == 0 {
		value = node.Value
	} else if len(node.Content) == 2 {
		value = node.Content[1].Value
	} else {
		return ErrRangeFormat
	}

	return this.Parse(value)
}

// UnmarshalText 设置默认值调用
func (this *MarketHours) UnmarshalText(text []byte) error {
	return this.Parse(api.Bytes2String(text))
}

// Size 获取时段总数
func (this *MarketHours) Size() int {
	return len(this.sessions)
}

// Index 判断timestamp是第几个交易时段
func (this *MarketHours) Index(milliseconds ...int64) int {
	var tm int64
	if len(milliseconds) > 0 {
		tm = milliseconds[0]
	} else {
		tm = timestamp.Now()
	}
	for i, timeRange := range this.sessions {
		if timeRange.IsTrading(tm) {
			return i
		}
	}
	return -1
}

// IsTrading 是否交易时段
func (this *MarketHours) IsTrading(milliseconds ...int64) bool {
	index := this.Index(milliseconds...)
	if index < 0 {
		return false
	}
	return true
}

// IsTodayLastSession 当前时段是否今天最后一个交易时段
//
//	备选函数名 IsTodayFinalSession
func (this *MarketHours) IsTodayLastSession(milliseconds ...int64) bool {
	n := this.Size()
	index := this.Index(milliseconds...)
	if index+1 < n {
		return false
	}
	return true
}

// CanStopLoss 当前时段是否可以进行止损操作
//
//	如果是3个时段, 止损操作在第2时段, 如果是4个时段, 止损在第3个
//	如果是2个时段, 则是第2个时段, 也就是最后一个时段
func (this *MarketHours) CanStopLoss(milliseconds ...int64) bool {
	n := this.Size()
	index := this.Index(milliseconds...)
	// 1个时段, 立即止损
	c1 := n == 1
	// 2个时段, 在第二个时间止损
	c2 := n == 2 && index == 1
	// 3个以上时段, 在倒数第2个时段止损
	c3 := n >= 3 && index+2 == n
	if c1 || c2 || c3 {
		return true
	}
	return false
}

// CanTakeProfit 当前时段是否可以止盈
func (this *MarketHours) CanTakeProfit(milliseconds ...int64) bool {
	_ = milliseconds
	return true
}

func (this *MarketHours) GetTimeKind(milliseconds int64) (TimeKind, int, error) {
	for index, session := range this.sessions {
		if session.IsTrading(milliseconds) {
			return session.kind, index, nil
		}
	}
	return 0, -1, errors.New("未能找到对应的TimeKind")
}
