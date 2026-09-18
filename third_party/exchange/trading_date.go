package exchange

import (
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/quant1x/x/api"
	"github.com/quant1x/x/logger"
)

const (
	indexDate = "2006-01-02"  // 索引日期格式
	TimeOnly  = time.TimeOnly // 时分秒的格式
)

// IsHoliday 是否节假日
func IsHoliday(date string) bool {
	dates := readOnlyDates()
	iRet, found := sort.Find(len(dates), func(i int) int {
		return strings.Compare(date, dates[i])
	})
	_ = iRet
	return !found
}

// ToUint32Date 通达信协议日期为YYYYMMDD格式的十进制整型
func ToUint32Date(datetime string) uint32 {
	t, err := api.ParseTime(datetime)
	if err != nil {
		logger.Errorf("datetime: %s", datetime)
		logger.Fatalf("%+v", err)
	}
	y, m, d := t.Date()
	return uint32(y*10000 + int(m)*100 + d)
}

// FixTradeDate 强制修正交易日字符串
//
//	默认格式 YYYY-MM-DD, 支持其它格式
func FixTradeDate(datetime string, format ...string) string {
	dt, err := api.ParseTime(datetime)
	if err != nil {
		logger.Errorf("datetime: %s", datetime)
		logger.Fatalf("%+v", err)
	}
	defaultDateFormat := TradingDayDateFormat
	if len(format) > 0 {
		defaultDateFormat = format[0]
	}
	return dt.Format(defaultDateFormat)
}

// Today 当日, 区别于IndexToday, IndexToday可能存在调整
func Today() string {
	now := time.Now()
	return now.Format(TradingDayDateFormat)
}

// IndexToday 当天
func IndexToday() string {
	now := time.Now()
	return now.Format(indexDate)
}

// TradeRange 输出交易日范围
//
//	默认是线程安全
//
// Deprecated: 推荐使用 TradingDateRange
func TradeRange(start, end string, threadSafe ...bool) []string {
	isSafe := true
	if len(threadSafe) > 0 {
		isSafe = threadSafe[0]
	}
	var tradeDates []string
	if isSafe {
		tradeDates = readOnlyDates()
	} else {
		tradeDates = unsafeDates()
	}

	start = FixTradeDate(start)
	end = FixTradeDate(end)

	is := sort.SearchStrings(tradeDates, start)
	ie := sort.SearchStrings(tradeDates, end)
	// 防止 end 超出日历范围导致越界（sort.SearchStrings 返回插入位置，可能等于 len）
	if ie >= len(tradeDates) {
		ie = len(tradeDates) - 1
	}

	today := IndexToday()
	lastDay := tradeDates[ie]
	if lastDay > today || lastDay > end {
		ie = ie - 1
	}
	if is > ie+1 {
		return nil
	}
	return slices.Clone(tradeDates[is : ie+1])
}

// transactionDateRange 在start和end之间的所有交易日
//
//	start 开始日期
//	end 结束日期
//	threadSafe 是否线程安全
//	skipToday 是否跳过当日
func transactionDateRange(start, end string, threadSafe, skipToday bool) []string {
	start = FixTradeDate(start)
	end = FixTradeDate(end)
	if start > end {
		return nil
	}

	var tradeDates []string
	if threadSafe {
		tradeDates = readOnlyDates()
	} else {
		tradeDates = unsafeDates()
	}

	is := sort.SearchStrings(tradeDates, start)
	ie := sort.SearchStrings(tradeDates, end)
	// 防止 end 超出日历范围导致越界（sort.SearchStrings 返回插入位置，可能等于 len）
	if ie >= len(tradeDates) {
		ie = len(tradeDates) - 1
	}

	if skipToday {
		today := IndexToday()
		lastDay := tradeDates[ie]
		if lastDay > today || lastDay > end {
			ie = ie - 1
		}
	} else {
		for {
			if tradeDates[ie] <= end {
				break
			}
			ie--
		}
	}
	if is > ie+1 {
		return nil
	}
	return slices.Clone(tradeDates[is : ie+1])
}

// DateRange 在start和end闭区间的所有交易日
//
//	start 开始日期
//	end 结束日期
func DateRange(start, end string) []string {
	return transactionDateRange(start, end, true, false)
}

// TradingDateRange 输出交易日范围
//
//	默认是线程安全, 日期区间为左闭右开
func TradingDateRange(start, end string, threadSafe ...bool) []string {
	isSafe := true
	if len(threadSafe) > 0 {
		isSafe = threadSafe[0]
	}
	return transactionDateRange(start, end, isSafe, true)
}

// LastTradeDate 获得最后一个交易日
func LastTradeDate() string {
	today := IndexToday()
	tradeDates := readOnlyDates()
	end := sort.SearchStrings(tradeDates, today)
	lastDay := tradeDates[end]
	if lastDay > today {
		end = end - 1
	}
	return tradeDates[end]
}

// LastNDate 获得指定日期前的N个交易日期数组
func LastNDate(date string, n ...int) []string {
	__opt_end := 0
	if len(n) > 0 {
		__opt_end = n[0]
	}
	r := api.RangeFinite(-__opt_end)
	date = FixTradeDate(date)
	tradeDates := readOnlyDates()
	end := sort.SearchStrings(tradeDates, date)
	lastDay := tradeDates[end]
	if lastDay > date {
		end = end - 1
	}
	date_length := len(tradeDates[0:end])
	s, e, err := r.Limits(date_length)
	if err != nil {
		return nil
	}
	return slices.Clone(tradeDates[s : e+1])
}

// GetFrontTradeDay 获取上一个交易日
func GetFrontTradeDay() string {
	//today := LastTradeDate()
	today := GetCurrentlyDay()
	array := LastNDate(today, 1)
	return array[0]
}

// NextTradeDate 获取指定日期的下一个交易日
func NextTradeDate(date string) string {
	date = FixTradeDate(date)
	tradeDates := readOnlyDates()
	end := sort.SearchStrings(tradeDates, date)
	lastDay := tradeDates[end]
	if lastDay == date {
		end = end + 1
	}
	return tradeDates[end]
}

// DateIsTradingDay date是否交易日?默认是今天
func DateIsTradingDay(date ...string) bool {
	theDay := Today()
	if len(date) > 0 {
		theDay = FixTradeDate(date[0])
	}
	lastDay := LastTradeDate()
	if lastDay == theDay {
		return true
	}
	return false
}

// GetLastDayForUpdate 获取可以更新数据的最后一个交易日
func GetLastDayForUpdate() string {
	now := time.Now()
	today := now.Format(TradingDayDateFormat)
	if CanUpdate(now) {
		return today
	}
	today = LastTradeDate()
	array := LastNDate(today, 1)
	return array[0]
}

// GetCurrentlyDay 获取数据有效的最后一个交易日, 以9点15分划分
func GetCurrentlyDay() (currentlyDay string) {
	today := IndexToday()
	dates := TradeRange(MARKET_CN_FIRST_DATE, today)
	days := len(dates)
	currentlyDay = dates[days-1]
	if today == currentlyDay {
		now := time.Now()
		nowTime := now.Format(CN_SERVERTIME_FORMAT)
		if nowTime < CN_TradingStartTime {
			currentlyDay = dates[days-2]
		}
	}
	return currentlyDay
}

// GetCurrentDate 获取数据有效的最后一个交易日, 以9点整划分
func GetCurrentDate(date ...string) (currentDate string) {
	today := IndexToday()
	targetDate := today
	if len(date) > 0 {
		targetDate = FixTradeDate(date[0])
	}
	dates := TradeRange(MARKET_CN_FIRST_DATE, targetDate)
	days := len(dates)
	currentDate = dates[days-1]
	if today == currentDate {
		now := time.Now()
		nowTime := now.Format(CN_SERVERTIME_FORMAT)
		if nowTime < CN_MarketInitTime {
			currentDate = dates[days-2]
		}
	}
	return currentDate
}
