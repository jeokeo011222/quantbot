package exchange

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/quant1x/pkg/yaml"
	"github.com/quant1x/x/logger"
	"github.com/quant1x/x/timestamp"
)

// 值范围正则表达式

var stringToTimeKind = map[string]TimeKind{
	"CA":   CallAuction,
	"CC":   CanCancel,
	"T":    Trading,
	"CAAC": CallAuctionAndCancel,
	"TAC":  TradingAndCancel,
	"CAAT": CallAuctionAndTrading,
}

func StringToTimeKind(timeKindString string) TimeKind {
	if strings.HasSuffix(timeKindString, "|") {
		timeKindString = strings.TrimSuffix(timeKindString, "|")
	}

	timeKind, ok := stringToTimeKind[timeKindString]
	if ok {
		return timeKind
	}
	return TradingAndCancel
}

// TradingTimeRange 时间范围
//
//	左闭右开[begin, end)
type TradingTimeRange struct {
	kind    TimeKind // 时间类型
	begin   string   // 开始时间
	end     string   // 结束时间
	tmBegin int64    // 开始的毫秒数
	tmEnd   int64    // 结束的毫秒数
}

func ExchangeTradingTimeRange(kind TimeKind, begin, end string) TradingTimeRange {
	tmBegin, err := time.ParseInLocation(time.TimeOnly, begin, time.Local)
	if err != nil {
		logger.Fatalf("%+v", err)
	}
	tmEnd, err := time.ParseInLocation(time.TimeOnly, end, time.Local)
	if err != nil {
		logger.Fatalf("%+v", err)
	}
	tr := TradingTimeRange{
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

func (this TradingTimeRange) Minutes() int {
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

func (this TradingTimeRange) String() string {
	return fmt.Sprintf("{begin: %s, end: %s}", this.begin, this.end)
}

func (this TradingTimeRange) v2String() string {
	return fmt.Sprintf("%s~%s", this.begin, this.end)
}

func (this *TradingTimeRange) timeStringToTodayTimeStamp(timeString string) (int64, error) {
	dateTimeString := fmt.Sprintf("%s %s", time.Now().Format(TradingDayDateFormat), timeString)
	parsedTime, err := time.ParseInLocation(TimeStampSecond, dateTimeString, time.Local)
	if err != nil {
		return 0, err
	}

	return parsedTime.UnixMilli(), nil
}

func (this *TradingTimeRange) parseTradingTimeRangeString(timeKind string, startTime string, endTime string) error {
	this.begin = startTime
	this.end = endTime
	if this.begin > this.end {
		this.begin, this.end = this.end, this.begin
	}

	this.kind = StringToTimeKind(timeKind)

	timeBegin, err := this.timeStringToTodayTimeStamp(this.begin)
	if err != nil {
		return err
	}
	this.tmBegin = timestamp.SinceZero(timeBegin)

	timeEnd, err := this.timeStringToTodayTimeStamp(this.end)
	if err != nil {
		return err
	}
	this.tmEnd = timestamp.SinceZero(timeEnd)

	return nil
}

// Parse 解析文本, 覆盖属性
func (this *TradingTimeRange) Parse(text string) error {
	text = strings.ReplaceAll(text, " ", "")
	match := tradingTimeRangePatternRegexp.FindStringSubmatch(text)

	if len(match) == 4 {
		err := this.parseTradingTimeRangeString(match[1], match[2], match[3])
		if err != nil {
			return err
		}
		//sessions = append(sessions, tr)
	} else {
		return errors.New("无法匹配到有效的TradingTimeRange")
	}

	return nil
}

// UnmarshalText 设置默认值调用
//
//	由于begin和end字段不可访问, 默认值调用实际无效
func (this *TradingTimeRange) UnmarshalText(text []byte) error {
	_ = text
	panic("implement me")
}

// UnmarshalYAML YAML自定义解析
func (this *TradingTimeRange) UnmarshalYAML(node *yaml.Node) error {
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

func (this *TradingTimeRange) IsTrading(milliseconds int64) bool {
	tm := timestamp.SinceZero(milliseconds)
	if tm >= this.tmBegin && tm <= this.tmEnd {
		return true
	}
	return false
}
