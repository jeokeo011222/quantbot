package exchange

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/quant1x/pkg/yaml"
	"github.com/quant1x/x/exception"
	"github.com/quant1x/x/logger"
	"github.com/quant1x/x/timestamp"
)

// 值范围正则表达式
var (
	stringRangePattern = "[~-]\\s*"
	stringRangeRegexp  = regexp.MustCompile(stringRangePattern)
)

var (
	ErrTimeFormat     = exception.New(errnoConfig+2, "时间范围格式错误")
	formatOfTimestamp = time.TimeOnly
)

// TimeKind 时段类型
type TimeKind uint64

const (
	CallAuction TimeKind = 1 << iota // 集合竞价
	CanCancel                        // 可撤单
	Trading                          // 交易时段
)

const (
	CallAuctionAndCancel  TimeKind = CallAuction | CanCancel // 集合竞价可撤单
	TradingAndCancel      TimeKind = Trading | CanCancel     // 交易可撤单
	CallAuctionAndTrading TimeKind = CallAuction | Trading   // 集合竞价可撤单

)

// TimeInterval 时间范围
//
//	左闭右开[begin, end)
type TimeInterval struct {
	kind    TimeKind // 时间类型
	begin   string   // 开始时间
	end     string   // 结束时间
	tmBegin int64    // 开始的毫秒数
	tmEnd   int64    // 结束的毫秒数
}

func ExchangeTime(kind TimeKind, begin, end string) TimeInterval {
	tmBegin, err := time.ParseInLocation(time.TimeOnly, begin, time.Local)
	if err != nil {
		logger.Fatalf("%+v", err)
	}
	tmEnd, err := time.ParseInLocation(time.TimeOnly, end, time.Local)
	if err != nil {
		logger.Fatalf("%+v", err)
	}
	tr := TimeInterval{
		kind:    kind,
		begin:   begin,
		end:     end,
		tmBegin: timestamp.Since(tmBegin),
		tmEnd:   timestamp.Since(tmEnd),
	}
	if tr.begin > tr.end {
		tr.begin, tr.end = tr.end, tr.begin
		tr.tmBegin, tr.tmEnd = tr.tmEnd, tr.tmBegin
	}
	return tr
}

func (this TimeInterval) Minutes() int {
	if (this.kind & Trading) == 0 {
		return 0
	}
	seconds := (this.tmEnd - this.tmBegin) / timestamp.MillisecondsPerSecond
	minutes := seconds / 60
	remaining := seconds % 60
	if remaining > 0 {
		minutes++
	}
	return int(minutes)
}

func (this TimeInterval) String() string {
	return fmt.Sprintf("{begin: %s, end: %s}", this.begin, this.end)
}

func (this TimeInterval) v2String() string {
	return fmt.Sprintf("%s~%s", this.begin, this.end)
}

// Parse 解析文本, 覆盖属性
func (this *TimeInterval) Parse(text string) error {
	text = strings.TrimSpace(text)
	arr := stringRangeRegexp.Split(text, -1)
	if len(arr) != 2 {
		return ErrTimeFormat
	}
	this.begin = strings.TrimSpace(arr[0])
	this.end = strings.TrimSpace(arr[1])
	if this.begin > this.end {
		this.begin, this.end = this.end, this.begin
	}
	return nil
}

// UnmarshalText 设置默认值调用
//
//	由于begin和end字段不可访问, 默认值调用实际无效
func (this *TimeInterval) UnmarshalText(text []byte) error {
	_ = text
	panic("implement me")
}

// UnmarshalYAML YAML自定义解析
func (this *TimeInterval) UnmarshalYAML(node *yaml.Node) error {
	var key, value string
	if len(node.Content) == 0 {
		value = node.Value
	} else if len(node.Content) == 2 {
		key = node.Content[0].Value
		value = node.Content[1].Value
	}
	_ = key
	return this.Parse(value)
}

func (this *TimeInterval) IsTrading(milliseconds int64) bool {
	tm := timestamp.SinceZero(milliseconds)
	if tm >= this.tmBegin && tm <= this.tmEnd {
		return true
	}
	return false
}
