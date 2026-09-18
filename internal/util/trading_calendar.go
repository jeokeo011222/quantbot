package util

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// holidayCalendarFile 全局交易日历配置文件路径（由 App 启动时通过 SetTradingCalendarFile 注入）
var holidayCalendarFile string

// defaultHolidays A股法定节假日休市兜底日历（仅记录工作日休市日，周末自动休市）
// 数据来源：沪深北交易所年度休市安排公告。程序外部配置文件缺失/损坏时回退到此处。
var defaultHolidays = map[string]bool{
	// 2025年
	"2025-01-01": true, // 元旦
	"2025-01-28": true, // 春节
	"2025-01-29": true,
	"2025-01-30": true,
	"2025-01-31": true,
	"2025-02-03": true,
	"2025-02-04": true,
	"2025-04-04": true, // 清明节
	"2025-05-01": true, // 劳动节
	"2025-05-02": true,
	"2025-05-05": true,
	"2025-06-02": true, // 端午节
	"2025-10-01": true, // 国庆节+中秋节
	"2025-10-02": true,
	"2025-10-03": true,
	"2025-10-06": true,
	"2025-10-07": true,
	"2025-10-08": true,

	// 2026年
	"2026-01-01": true, // 元旦
	"2026-01-02": true,
	"2026-02-16": true, // 春节
	"2026-02-17": true,
	"2026-02-18": true,
	"2026-02-19": true,
	"2026-02-20": true,
	"2026-02-23": true,
	"2026-04-06": true, // 清明节
	"2026-05-01": true, // 劳动节
	"2026-05-04": true,
	"2026-05-05": true,
	"2026-06-19": true, // 端午节
	"2026-09-25": true, // 中秋节
	"2026-10-01": true, // 国庆节
	"2026-10-02": true,
	"2026-10-05": true,
	"2026-10-06": true,
	"2026-10-07": true,
}

// holidaySet 当前生效的休市日历（可被外挂配置文件/联网刷新替换），受 holidayMu 保护。
var (
	holidaySet = copyHolidays(defaultHolidays)
	holidayMu  sync.RWMutex
)

// calendarSource 联网数据源标注
const calendarSource = "timor.tech/api/holiday/year"

// thsCalendarLoc 同花顺交易日以 Asia/Shanghai 自然日为准
const thsCalendarLoc = "Asia/Shanghai"

// SyncTHSTradingHoldays 用同花顺官方近一年 A 股交易日序列刷新 util 休市集合，
// 替代原 timor.tech 逐年抓取休市日（该源常返回 403）。
// THS 端点固定返回 [today-1y, today]（Asia/Shanghai）近一年交易日、无入参。
// 刷新规则：在近一年窗口内，凡周一~周五且在交易日列表中 → 从休市集合移除；
// 周一~周五但不在交易日列表 → 记为休市日（周末恒为休市，无需记录）。
// 窗口外（早于 today-1y）保留原集合/内置兜底，不影响更早年份判断。
// 同时按 calendarSnapshot 写回外挂配置文件，供下次启动 LoadTradingCalendar 直接使用。
// tradingDays 需为 "yyyy-MM-dd" 格式；无该格式输入时仅按工作日推断（等价保留原集合）。
// 返回窗口内新增的休市日数量。
func SyncTHSTradingHoldays(tradingDays []string) (int, error) {
	loc, err := time.LoadLocation(thsCalendarLoc)
	if err != nil {
		loc = time.FixedZone("CST", 8*3600)
	}
	today := time.Now().In(loc)
	windowStart := today.AddDate(-1, 0, 0) // [today-1y, today] 闭区间

	trading := make(map[string]bool, len(tradingDays))
	for _, d := range tradingDays {
		trading[d] = true
	}

	merged := copyHolidays(defaultHolidays)
	for k, v := range getHolidaySet() {
		merged[k] = v
	}

	added := 0
	for day := windowStart; !day.After(today); day = day.AddDate(0, 0, 1) {
		if day.Weekday() == time.Saturday || day.Weekday() == time.Sunday {
			continue
		}
		ds := day.Format("2006-01-02")
		if trading[ds] {
			// THS 明确为交易日：从休市集合移除
			delete(merged, ds)
			continue
		}
		// 工作日但在近一年窗口内非交易日 → 休市日
		if !merged[ds] {
			added++
		}
		merged[ds] = true
	}

	lockSet(merged)

	// 写回外挂配置文件（作为下次启动缓存）
	if holidayCalendarFile != "" {
		snap := calendarSnapshot{
			Source:    "ths:fuyao.aicubes.cn/calendar/trading-days",
			UpdatedAt: time.Now().Format(time.RFC3339),
			Holidays:  merged,
		}
		if data, err := json.MarshalIndent(snap, "", "  "); err == nil {
			if err := os.MkdirAll(filepath.Dir(holidayCalendarFile), 0o755); err == nil {
				if err := os.WriteFile(holidayCalendarFile, data, 0o644); err != nil {
					log.Printf("[TradingCalendar] 写回配置文件失败: %v", err)
				}
			}
		}
	}

	log.Printf("[TradingCalendar] 同花顺近一年交易日历刷新完成: 窗口 %s~%s, 交易日 %d, 新增休市日 %d",
		windowStart.Format("2006-01-02"), today.Format("2006-01-02"), len(trading), added)
	return added, nil
}

// SetTradingCalendarFile 注入交易日历外挂配置文件路径（由 App 启动时调用）
func SetTradingCalendarFile(path string) {
	holidayCalendarFile = path
}

// copyHolidays 深拷贝一份休市日历
func copyHolidays(src map[string]bool) map[string]bool {
	dst := make(map[string]bool, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

// calendarSnapshot 交易日历配置文件结构
type calendarSnapshot struct {
	Source    string          `json:"source"`
	UpdatedAt string          `json:"updated_at"`
	Holidays  map[string]bool `json:"holidays"`
}

// LoadTradingCalendar 从外挂配置文件加载休市日历，替换内存集合。
// 文件缺失/损坏/为空时回退到内建 defaultHolidays，保证开箱即用。
func LoadTradingCalendar(path string) error {
	if path != "" {
		holidayCalendarFile = path
	}
	if holidayCalendarFile == "" {
		lockSet(copyHolidays(defaultHolidays))
		return nil
	}

	data, err := os.ReadFile(holidayCalendarFile)
	if err != nil {
		// 文件不存在或读不了：回退内建
		lockSet(copyHolidays(defaultHolidays))
		return fmt.Errorf("读取交易日历配置文件失败(回退内建): %w", err)
	}

	snap := calendarSnapshot{Holidays: map[string]bool{}}
	if err := json.Unmarshal(data, &snap); err != nil {
		lockSet(copyHolidays(defaultHolidays))
		return fmt.Errorf("解析交易日历配置失败(回退内建): %w", err)
	}
	if len(snap.Holidays) == 0 {
		lockSet(copyHolidays(defaultHolidays))
		return fmt.Errorf("交易日历配置为空(回退内建)")
	}
	lockSet(snap.Holidays)
	log.Printf("[TradingCalendar] 已加载外挂配置: %s (共 %d 个休市日)", holidayCalendarFile, len(snap.Holidays))
	return nil
}

// lockSet 在写锁保护下替换内存休市集合
func lockSet(newSet map[string]bool) {
	holidayMu.Lock()
	holidaySet = newSet
	holidayMu.Unlock()
}

// getHolidaySet 返回当前休市集合（读锁下快照拷贝，避免后续读共享 map）
func getHolidaySet() map[string]bool {
	holidayMu.RLock()
	defer holidayMu.RUnlock()
	return copyHolidays(holidaySet)
}

// TradingCalendarLatestYear 返回当前休市集合中出现的最新年份；集合为空返回 0
func TradingCalendarLatestYear() int {
	s := getHolidaySet()
	latest := 0
	for date := range s {
		if len(date) >= 4 {
			var y int
			fmt.Sscanf(date[:4], "%d", &y)
			if y > latest {
				latest = y
			}
		}
	}
	return latest
}

// TradingCalendarCovers 判断当前休市集合是否包含指定年份的条目
func TradingCalendarCovers(year int) bool {
	s := getHolidaySet()
	prefix := fmt.Sprintf("%04d-", year)
	for date := range s {
		if len(date) >= 5 && date[:5] == prefix {
			return true
		}
	}
	return false
}

// yearHolidayAPI timor.tech 每年休市接口返回结构
// 例: {"code":0,"holiday":{"02-16":{"holiday":true,"date":"2026-02-16","name":"除夕"}}}
type yearHolidayAPI struct {
	Code    int `json:"code"`
	Holiday map[string]struct {
		Holiday bool   `json:"holiday"`
		Date    string `json:"date"`
	} `json:"holiday"`
}

// SyncTradingHolidaysFromWeb 从联网数据源拉取指定年份的A股休市日，合并进当前集合并写回配置文件。
// 返回本次新增的休市日数量。仅写入 holiday==true 且 date 合法(>=2000年)的日期。
// 任一公休日无数据照常处理，取并集：内建 + 已有 + 新拉。
func SyncTradingHolidaysFromWeb(years []int) (int, error) {
	if len(years) == 0 {
		return 0, fmt.Errorf("未指定需同步的年份")
	}

	client := &http.Client{Timeout: 30 * time.Second}
	merged := copyHolidays(defaultHolidays) // 内建兜底
	base := getHolidaySet()
	for k, v := range base {
		merged[k] = v
	}
	added := 0
	syncedYears := 0

	for _, year := range years {
		if year < 2000 {
			continue
		}
		url := fmt.Sprintf("https://timor.tech/api/holiday/year/%d", year)
		resp, err := client.Get(url)
		if err != nil {
			log.Printf("[TradingCalendar] 拉取 %d 年休市数据失败(跳过): %v", year, err)
			continue
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if err != nil {
			log.Printf("[TradingCalendar] 读取 %d 年休市数据失败(跳过): %v", year, err)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			log.Printf("[TradingCalendar] 拉取 %d 年休市数据 HTTP %d(跳过)", year, resp.StatusCode)
			continue
		}
		var api yearHolidayAPI
		if err := json.Unmarshal(body, &api); err != nil {
			log.Printf("[TradingCalendar] 解析 %d 年休市数据失败(跳过): %v", year, err)
			continue
		}
		if api.Code != 0 {
			log.Printf("[TradingCalendar] 拉取 %d 年休市数据 code=%d(跳过)", year, api.Code)
			continue
		}
		syncedYears++
		for _, d := range api.Holiday {
			if !d.Holiday || len(d.Date) < 10 {
				continue
			}
			if _, exists := merged[d.Date]; !exists {
				added++
			}
			merged[d.Date] = true
		}
	}

	if syncedYears == 0 {
		return added, fmt.Errorf("联网刷新交易日历全部失败（共尝试 %d 年）", len(years))
	}

	// 写回外挂配置文件
	if holidayCalendarFile != "" {
		snap := calendarSnapshot{
			Source:    calendarSource,
			UpdatedAt: time.Now().Format(time.RFC3339),
			Holidays:  merged,
		}
		data, err := json.MarshalIndent(snap, "", "  ")
		if err == nil {
			if err := os.MkdirAll(filepath.Dir(holidayCalendarFile), 0o755); err == nil {
				if err := os.WriteFile(holidayCalendarFile, data, 0o644); err != nil {
					log.Printf("[TradingCalendar] 写回配置文件失败: %v", err)
				}
			}
		}
	}

	lockSet(merged)
	log.Printf("[TradingCalendar] 联网刷新完成: 覆盖 %d 个休市日, 新增 %d", len(merged), added)
	return added, nil
}

// IsTradingDay 判断指定日期是否为A股交易日（排除周末和法定节假日）
func IsTradingDay(t time.Time) bool {
	loc := time.FixedZone("CST", 8*3600)
	bt := t.In(loc)

	if bt.Weekday() == time.Saturday || bt.Weekday() == time.Sunday {
		return false
	}

	holidaySetNow := getHolidaySet()
	return !holidaySetNow[bt.Format("2006-01-02")]
}

// IsHoliday 判断指定日期是否为A股法定节假日（含周末）
func IsHoliday(t time.Time) bool {
	return !IsTradingDay(t)
}

// NextTradingDay 返回从指定日期起的下一个交易日
func NextTradingDay(t time.Time) time.Time {
	loc := time.FixedZone("CST", 8*3600)
	bt := t.In(loc)
	day := time.Date(bt.Year(), bt.Month(), bt.Day(), 0, 0, 0, 0, loc)
	for i := 0; i < 30; i++ {
		day = day.AddDate(0, 0, 1)
		if IsTradingDay(day) {
			return day
		}
	}
	return day
}

// PrevTradingDay 返回指定日期之前的最近一个交易日
func PrevTradingDay(t time.Time) time.Time {
	loc := time.FixedZone("CST", 8*3600)
	bt := t.In(loc)
	day := time.Date(bt.Year(), bt.Month(), bt.Day(), 0, 0, 0, 0, loc)
	for i := 0; i < 30; i++ {
		day = day.AddDate(0, 0, -1)
		if IsTradingDay(day) {
			return day
		}
	}
	return day
}

// ExpectedLatestTradeDate 期望的行情数据最新交易日：即「今天的前一个交易日」
// 量化回测/选股等依赖最新真实行情，要求 stock.duckdb 至少更新到该日期才算最新。
// 返回：目标 date（time.Time）与今天是否为交易日。
// 即使今天是交易日的盘中，也只需要覆盖到前一个交易日（当日数据收盘后同步即可）。
func ExpectedLatestTradeDate(now time.Time) (target time.Time, isTradingToday bool) {
	isTradingToday = IsTradingDay(now)
	target = PrevTradingDay(now)
	return target, isTradingToday
}

// IsDataCurrent 判断最新行情日期(tradeDate)是否已达到「今天的前一个交易日」要求。
// tradeDate 形如 "2006-01-02"。达到或晚于则返回 true（数据无需更新）。
func IsDataCurrent(tradeDate string, now time.Time) bool {
	target, _ := ExpectedLatestTradeDate(now)
	return tradeDate >= target.Format("2006-01-02")
}
