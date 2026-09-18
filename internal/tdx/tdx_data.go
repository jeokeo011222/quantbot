package tdx

import (
	"encoding/binary"
	"fmt"
	"strings"
	"time"
)

// ========== 数据结构 ==========

// KlineBar K线数据
type KlineBar struct {
	Date   string  `json:"date"`
	Open   float64 `json:"open"`
	High   float64 `json:"high"`
	Low    float64 `json:"low"`
	Close  float64 `json:"close"`
	Volume int64   `json:"volume"`
	Amount float64 `json:"amount"`
}

// MinuteBar 分时数据
type MinuteBar struct {
	Time     string  `json:"time"`
	Price    float64 `json:"price"`
	Volume   int64   `json:"volume"`
	AvgPrice float64 `json:"avg_price"`
}

// Quote 实时行情
type Quote struct {
	Code       string    `json:"code"`
	Price      float64   `json:"price"`
	LastClose  float64   `json:"last_close"`
	Open       float64   `json:"open"`
	High       float64   `json:"high"`
	Low        float64   `json:"low"`
	Volume     int64     `json:"volume"`
	Amount     float64   `json:"amount"`
	BidVolumes []int64   `json:"bid_volumes"`
	AskVolumes []int64   `json:"ask_volumes"`
	BidPrices  []float64 `json:"bid_prices"`
	AskPrices  []float64 `json:"ask_prices"`
}

// Transaction 逐笔成交
type Transaction struct {
	Price  float64 `json:"price"`
	Volume int64   `json:"volume"`
	Time   string  `json:"time"`
	BS     string  `json:"bs"`
}

// SecurityInfo 证券信息
type SecurityInfo struct {
	Name    string `json:"name"`
	Code    string `json:"code"`
	Company string `json:"company"`
}

// SecurityListItem 证券列表项
type SecurityListItem struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

// MarketStat 市场统计
type MarketStat struct {
	UpCount   int `json:"up_count"`
	DownCount int `json:"down_count"`
	FlatCount int `json:"flat_count"`
	LimitUp   int `json:"limit_up"`
	LimitDown int `json:"limit_down"`
}

// ========== 响应解析 ==========

// parseKlineResponse 解析K线响应
func parseKlineResponse(data []byte, category uint16) []KlineBar {
	if len(data) < 6 {
		return nil
	}

	barSize := klineBarSize(category)
	if barSize == 0 {
		return nil
	}

	var bars []KlineBar
	pos := 2 // 跳过前缀

	for pos+barSize <= len(data) {
		bar := parseKlineBar(data[pos:pos+barSize], category)
		if bar != nil {
			bars = append(bars, *bar)
		}
		pos += barSize
	}

	return bars
}

func klineBarSize(category uint16) int {
	switch category {
	case KlineCategoryDay, KlineCategoryWeek, KlineCategoryMonth:
		return 32
	case KlineCategoryMin1, KlineCategoryMin5, KlineCategoryMin15, KlineCategoryMin30, KlineCategoryMin60:
		return 32
	default:
		return 32
	}
}

func parseKlineBar(data []byte, category uint16) *KlineBar {
	if len(data) < 32 {
		return nil
	}

	bar := &KlineBar{}

	// 日期时间
	dateBytes := binary.LittleEndian.Uint32(data[0:4])
	timeBytes := binary.LittleEndian.Uint32(data[4:8])

	if category >= KlineCategoryDay {
		// 日线/周线/月线：date 是 YYYYMMDD
		bar.Date = formatDateInt(int(dateBytes))
	} else {
		// 分钟线：date 是 YYYYMMDD，time 是 HHMMSS
		dateStr := formatDateInt(int(dateBytes))
		timeInt := int(timeBytes)
		hour := timeInt / 10000
		minute := (timeInt % 10000) / 100
		second := timeInt % 100
		bar.Date = fmt.Sprintf("%s %02d:%02d:%02d", dateStr, hour, minute, second)
	}

	// 价格（整数，除以100）
	bar.Open = float64(int32(binary.LittleEndian.Uint32(data[8:12]))) / 100.0
	bar.High = float64(int32(binary.LittleEndian.Uint32(data[12:16]))) / 100.0
	bar.Low = float64(int32(binary.LittleEndian.Uint32(data[16:20]))) / 100.0
	bar.Close = float64(int32(binary.LittleEndian.Uint32(data[20:24]))) / 100.0

	// 成交额
	bar.Amount = float64(int32(binary.LittleEndian.Uint32(data[24:28])))

	// 成交量
	bar.Volume = int64(binary.LittleEndian.Uint32(data[28:32]))

	if bar.Close <= 0 {
		return nil
	}

	return bar
}

// parseMinuteBarsResponse 解析分时响应
func parseMinuteBarsResponse(data []byte) []MinuteBar {
	if len(data) < 6 {
		return nil
	}

	barSize := 20
	var bars []MinuteBar
	pos := 2

	for pos+barSize <= len(data) {
		bar := parseMinuteBar(data[pos : pos+barSize])
		if bar != nil {
			bars = append(bars, *bar)
		}
		pos += barSize
	}

	return bars
}

func parseMinuteBar(data []byte) *MinuteBar {
	if len(data) < 20 {
		return nil
	}

	dateInt := int(binary.LittleEndian.Uint32(data[0:4]))
	timeInt := int(binary.LittleEndian.Uint32(data[4:8]))
	price := float64(int32(binary.LittleEndian.Uint32(data[8:12]))) / 100.0
	volume := int64(binary.LittleEndian.Uint32(data[12:16]))
	avgPrice := float64(int32(binary.LittleEndian.Uint32(data[16:20]))) / 100.0

	if price <= 0 {
		return nil
	}

	dateStr := formatDateInt(dateInt)
	hour := timeInt / 10000
	minute := (timeInt % 10000) / 100
	second := timeInt % 100

	return &MinuteBar{
		Time:     fmt.Sprintf("%s %02d:%02d:%02d", dateStr, hour, minute, second),
		Price:    price,
		Volume:   volume,
		AvgPrice: avgPrice,
	}
}

// parseQuotesResponse 解析行情响应
func parseQuotesResponse(data []byte, codes []string) []Quote {
	if len(data) < 6 {
		return nil
	}

	var quotes []Quote
	pos := 2

	for _, code := range codes {
		if pos+60 > len(data) {
			break
		}

		q := parseQuote(data[pos:pos+60], code)
		if q != nil {
			quotes = append(quotes, *q)
		}
		pos += 60 // 每只股票 60 字节
	}

	return quotes
}

func parseQuote(data []byte, code string) *Quote {
	if len(data) < 60 {
		return nil
	}

	// 市场 + 代码
	mkt := data[0]
	_ = mkt

	// 名称 (4字节) - 省略

	q := &Quote{Code: code}

	// 成交价
	q.Price = float64(int32(binary.LittleEndian.Uint32(data[8:12]))) / 100.0
	// 昨收
	q.LastClose = float64(int32(binary.LittleEndian.Uint32(data[12:16]))) / 100.0
	// 开盘
	q.Open = float64(int32(binary.LittleEndian.Uint32(data[16:20]))) / 100.0
	// 最高
	q.High = float64(int32(binary.LittleEndian.Uint32(data[20:24]))) / 100.0
	// 最低
	q.Low = float64(int32(binary.LittleEndian.Uint32(data[24:28]))) / 100.0
	// 成交量
	q.Volume = int64(binary.LittleEndian.Uint32(data[28:32]))
	// 成交额
	q.Amount = float64(int32(binary.LittleEndian.Uint32(data[32:36])))

	// 五档价格和数量
	for i := 0; i < 5; i++ {
		offset := 36 + i*4
		if offset+4 <= len(data) {
			q.BidPrices = append(q.BidPrices, float64(int32(binary.LittleEndian.Uint32(data[offset:offset+4])))/100.0)
		}
	}
	for i := 0; i < 5; i++ {
		offset := 56 + i*4
		if offset+4 <= len(data) {
			q.AskPrices = append(q.AskPrices, float64(int32(binary.LittleEndian.Uint32(data[offset:offset+4])))/100.0)
		}
	}

	return q
}

// parseTransactionsResponse 解析逐笔成交响应
func parseTransactionsResponse(data []byte) []Transaction {
	if len(data) < 6 {
		return nil
	}

	var transactions []Transaction
	pos := 2

	for pos+16 <= len(data) {
		t := parseTransaction(data[pos : pos+16])
		if t != nil {
			transactions = append(transactions, *t)
		}
		pos += 16
	}

	return transactions
}

func parseTransaction(data []byte) *Transaction {
	if len(data) < 16 {
		return nil
	}

	t := &Transaction{
		Price:  float64(int32(binary.LittleEndian.Uint32(data[0:4]))) / 100.0,
		Volume: int64(binary.LittleEndian.Uint32(data[4:8])),
	}

	// 时间
	dateInt := int(binary.LittleEndian.Uint32(data[8:12]))
	timeInt := int(binary.LittleEndian.Uint32(data[12:16]))
	dateStr := formatDateInt(dateInt)
	hour := timeInt / 10000
	minute := (timeInt % 10000) / 100
	second := timeInt % 100
	t.Time = fmt.Sprintf("%s %02d:%02d:%02d", dateStr, hour, minute, second)

	if t.Price <= 0 {
		return nil
	}

	return t
}

// parseSecurityInfoResponse 解析证券信息响应
func parseSecurityInfoResponse(data []byte) *SecurityInfo {
	info := &SecurityInfo{}

	if len(data) >= 100 {
		// 名称: 50 字节
		nameBytes := data[0:50]
		info.Name = strings.TrimSpace(string(nameBytes))

		// 代码: 10 字节
		codeBytes := data[50:60]
		info.Code = strings.TrimSpace(string(codeBytes))

		// 公司信息: 剩余部分
		if len(data) > 60 {
			end := 100
			if len(data) < end {
				end = len(data)
			}
			info.Company = strings.TrimSpace(string(data[60:end]))
		}
	}

	return info
}

// parseSecurityListResponse 解析证券列表响应
func parseSecurityListResponse(data []byte) []SecurityListItem {
	if len(data) < 6 {
		return nil
	}

	itemSize := 8
	var items []SecurityListItem
	pos := 2

	for pos+itemSize <= len(data) {
		item := SecurityListItem{
			Code: fmt.Sprintf("%06d", binary.LittleEndian.Uint32(data[pos+1:pos+5])),
		}
		nameBytes := data[pos+5 : pos+8]
		item.Name = strings.TrimSpace(string(nameBytes[:3]))
		items = append(items, item)
		pos += itemSize
	}

	return items
}

// parseMarketStatResponse 解析市场统计响应
func parseMarketStatResponse(data []byte) *MarketStat {
	stat := &MarketStat{}
	if len(data) >= 20 {
		stat.UpCount = int(binary.LittleEndian.Uint32(data[0:4]))
		stat.DownCount = int(binary.LittleEndian.Uint32(data[4:8]))
		stat.FlatCount = int(binary.LittleEndian.Uint32(data[8:12]))
		stat.LimitUp = int(binary.LittleEndian.Uint32(data[12:16]))
		stat.LimitDown = int(binary.LittleEndian.Uint32(data[16:20]))
	}
	return stat
}

// formatDateInt 将 YYYYMMDD 整数转为日期字符串
func formatDateInt(date int) string {
	s := fmt.Sprintf("%08d", date)
	if len(s) == 8 {
		return s[:4] + "-" + s[4:6] + "-" + s[6:8]
	}
	return s
}

// FormatDateInt 导出版本：将 YYYYMMDD 整数转为日期字符串
func FormatDateInt(date int) string {
	return formatDateInt(date)
}

// timeNow 辅助
func timeNowStr() string {
	return time.Now().Format("15:04:05")
}
