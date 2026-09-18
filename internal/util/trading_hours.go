package util

import (
	"time"
)

// 交易时段定义
// 盘前: 09:15 - 09:30
// 上午交易: 09:30 - 11:30
// 午休: 11:30 - 13:00
// 下午交易: 13:00 - 15:00
// 盘后: 15:00 - 15:15
// 复盘: 15:15 - 18:00

// MarketPhase 市场时段
type MarketPhase string

const (
	PhasePreMarket        MarketPhase = "PRE_MARKET"        // 盘前准备 开盘前-09:15
	PhasePreOpen          MarketPhase = "PRE_OPEN"          // 盘前 09:15-09:30
	PhaseMorningSession   MarketPhase = "MORNING_SESSION"   // 上午交易 09:30-11:30
	PhaseLunch            MarketPhase = "LUNCH"             // 午休 11:30-13:00
	PhaseAfternoonSession MarketPhase = "AFTERNOON_SESSION" // 下午交易 13:00-15:00
	PhasePostMarket       MarketPhase = "POST_MARKET"       // 盘后 15:00-15:15
	PhaseReview           MarketPhase = "REVIEW"            // 复盘 15:15-18:00
	PhaseClosed           MarketPhase = "CLOSED"            // 休市
)

// GetMarketPhase 获取当前市场时段
func GetMarketPhase(now time.Time) MarketPhase {
	loc := time.FixedZone("CST", 8*3600)
	beijingTime := now.In(loc)

	// 非交易日（周末或法定节假日）休市
	if !IsTradingDay(beijingTime) {
		return PhaseClosed
	}

	hour := beijingTime.Hour()
	minute := beijingTime.Minute()
	currentMinutes := hour*60 + minute

	switch {
	// 盘前准备 开盘前 - 09:15
	case currentMinutes < 9*60+15:
		return PhasePreMarket
	// 盘前 09:15 - 09:30
	case currentMinutes >= 9*60+15 && currentMinutes < 9*60+30:
		return PhasePreOpen
	// 上午交易 09:30 - 11:30
	case currentMinutes >= 9*60+30 && currentMinutes < 11*60+30:
		return PhaseMorningSession
	// 午休 11:30 - 13:00
	case currentMinutes >= 11*60+30 && currentMinutes < 13*60:
		return PhaseLunch
	// 下午交易 13:00 - 15:00
	case currentMinutes >= 13*60 && currentMinutes < 15*60:
		return PhaseAfternoonSession
	// 盘后 15:00 - 15:15
	case currentMinutes >= 15*60 && currentMinutes < 15*60+15:
		return PhasePostMarket
	// 复盘 15:15 - 18:00
	case currentMinutes >= 15*60+15 && currentMinutes < 18*60:
		return PhaseReview
	default:
		return PhaseClosed
	}
}

// IsWorkingHour 判断当前是否为工作时段（包含盘前、交易、午休、盘后，不含盘前准备）
func IsWorkingHour(now time.Time) bool {
	phase := GetMarketPhase(now)
	return phase == PhasePreOpen || phase == PhaseMorningSession ||
		phase == PhaseLunch || phase == PhaseAfternoonSession || phase == PhasePostMarket
}

// IsTradingHour 判断当前是否为活跃交易时段（不含盘前盘后）
func IsTradingHour(now time.Time) bool {
	phase := GetMarketPhase(now)
	return phase == PhaseMorningSession || phase == PhaseAfternoonSession
}

// GetLastTradingCloseTime 获取最后一个交易时段的收盘时间
func GetLastTradingCloseTime(now time.Time) time.Time {
	loc := time.FixedZone("CST", 8*3600)
	beijingTime := now.In(loc)

	weekday := beijingTime.Weekday()
	hour := beijingTime.Hour()
	minute := beijingTime.Minute()
	currentMinutes := hour*60 + minute

	// 如果当前是交易时段内，直接返回当前时间
	if IsWorkingHour(now) {
		return now
	}

	// 午休时段，返回上午收盘
	if currentMinutes >= 11*60+30 && currentMinutes < 13*60 && weekday >= time.Monday && weekday <= time.Friday {
		return time.Date(beijingTime.Year(), beijingTime.Month(), beijingTime.Day(), 11, 30, 0, 0, loc)
	}

	// 盘后时段，返回当日收盘
	if currentMinutes >= 15*60 && currentMinutes < 18*60 && weekday >= time.Monday && weekday <= time.Friday {
		return time.Date(beijingTime.Year(), beijingTime.Month(), beijingTime.Day(), 15, 0, 0, 0, loc)
	}

	// 凌晨到9:15之间，返回前一个交易日收盘
	prevDay := beijingTime.AddDate(0, 0, -1)
	prevWeekday := prevDay.Weekday()

	// 处理周末情况
	if weekday == time.Monday && currentMinutes < 9*60+15 {
		prevDay = beijingTime.AddDate(0, 0, -3)
		prevWeekday = prevDay.Weekday()
	} else if weekday == time.Sunday || weekday == time.Saturday {
		daysBack := int(weekday) - int(time.Friday)
		if daysBack < 0 {
			daysBack += 7
		}
		prevDay = beijingTime.AddDate(0, 0, -daysBack)
		prevWeekday = prevDay.Weekday()
	}

	if prevWeekday >= time.Monday && prevWeekday <= time.Friday {
		return time.Date(prevDay.Year(), prevDay.Month(), prevDay.Day(), 15, 0, 0, 0, loc)
	}

	return time.Date(prevDay.Year(), prevDay.Month(), prevDay.Day(), 15, 0, 0, 0, loc)
}

// IsWeekday 检查是否为工作日
func IsWeekday(now time.Time) bool {
	weekday := now.Weekday()
	return weekday >= time.Monday && weekday <= time.Friday
}

// PhaseLabels 时段中文标签
var PhaseLabels = map[MarketPhase]string{
	PhasePreMarket:        "盘前准备 (开盘前)",
	PhasePreOpen:          "盘前 (09:15-09:30)",
	PhaseMorningSession:   "上午交易 (09:30-11:30)",
	PhaseLunch:            "午休 (11:30-13:00)",
	PhaseAfternoonSession: "下午交易 (13:00-15:00)",
	PhasePostMarket:       "盘后 (15:00-15:15)",
	PhaseReview:           "复盘 (15:15-18:00)",
	PhaseClosed:           "休市",
}
