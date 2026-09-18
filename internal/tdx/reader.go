package tdx

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/quantpilot/quantpilot/internal/data"
)

// DayBar TDX 日线数据记录
type DayBar struct {
	Date   int     `json:"date"`   // YYYYMMDD
	Open   float64 `json:"open"`   // 开盘价
	High   float64 `json:"high"`   // 最高价
	Low    float64 `json:"low"`    // 最低价
	Close  float64 `json:"close"`  // 收盘价
	Amount float64 `json:"amount"` // 成交金额(元)
	Volume int64   `json:"volume"` // 成交量(股)
}

// TDXReader 通达信数据读取器
type TDXReader struct {
	basePath string // TDX 安装目录，如 D:\tdx
}

// NewTDXReader 创建 TDX 读取器
func NewTDXReader(tdxPath string) *TDXReader {
	return &TDXReader{basePath: tdxPath}
}

// Exchange 市场类型
type Exchange string

const (
	ExchangeSH Exchange = "sh" // 上海证券交易所
	ExchangeSZ Exchange = "sz" // 深圳证券交易所
	ExchangeBJ Exchange = "bj" // 北京证券交易所/新三板
)

// ReadDayFile 读取单个 .day 文件
func (r *TDXReader) ReadDayFile(exchange Exchange, code string) ([]DayBar, error) {
	// 构建文件路径: vipdoc/{exchange}/lday/{exchange}{code}.day
	filename := fmt.Sprintf("%s%s.day", exchange, code)
	filePath := filepath.Join(r.basePath, "vipdoc", string(exchange), "lday", filename)

	return r.readDayFile(filePath)
}

// ReadDayFileByPath 根据完整路径读取
func (r *TDXReader) ReadDayFileByPath(path string) ([]DayBar, error) {
	return r.readDayFile(path)
}

// readDayFile 内部读取实现
func (r *TDXReader) readDayFile(path string) ([]DayBar, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open %s: %w", path, err)
	}
	defer f.Close()

	// 每条记录 32 字节
	const recordSize = 32
	var bars []DayBar

	header := make([]byte, recordSize)
	for {
		n, err := io.ReadFull(f, header)
		if err == io.EOF || (err != nil && n == 0) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("error reading %s: %w", path, err)
		}

		bar, parseErr := parseDayBar(header)
		if parseErr != nil {
			continue
		}
		if bar != nil {
			bars = append(bars, *bar)
		}
	}

	if len(bars) == 0 {
		return nil, fmt.Errorf("no valid data in %s", path)
	}

	// 按日期排序
	sort.Slice(bars, func(i, j int) bool {
		return bars[i].Date < bars[j].Date
	})

	return bars, nil
}

// parseDayBar 解析单条日线记录（32字节）
// 通达信日线格式 (参考 pytdx / RiskOS-cn 实现):
// - 日期: uint32 (4字节) - YYYYMMDD 格式 (如 20250129)
// - 开盘价: uint32 (4字节) - 以"分"为单位的整数，需除以100
// - 最高价: uint32 (4字节)
// - 最低价: uint32 (4字节)
// - 收盘价: uint32 (4字节)
// - 成交额: uint32 (4字节) - 单位：元
// - 成交量: uint32 (4字节) - 单位：股
// - 保留: 4字节
// 总计: 32字节
func parseDayBar(data []byte) (*DayBar, error) {
	if len(data) < 32 {
		return nil, fmt.Errorf("record too short: %d bytes", len(data))
	}

	// 解析日期 (uint32, 偏移 0) - YYYYMMDD 格式
	dateInt := int(binary.LittleEndian.Uint32(data[0:4]))

	// 解析价格 (uint32, 偏移 4-20) - 以"分"为单位，需除以100
	open := float64(binary.LittleEndian.Uint32(data[4:8])) / 100.0
	high := float64(binary.LittleEndian.Uint32(data[8:12])) / 100.0
	low := float64(binary.LittleEndian.Uint32(data[12:16])) / 100.0
	close := float64(binary.LittleEndian.Uint32(data[16:20])) / 100.0

	// 解析成交金额 (uint32, 偏移 20) - 单位：元
	amount := float64(binary.LittleEndian.Uint32(data[20:24]))

	// 解析成交量 (uint32, 偏移 24) - 单位：股
	volume := int64(binary.LittleEndian.Uint32(data[24:28]))

	// 验证有效性
	if close <= 0 && open <= 0 && high <= 0 && low <= 0 {
		return nil, nil // 无效记录
	}

	// 验证日期合理性 (1990-2035)
	if dateInt < 19900101 || dateInt > 20351231 {
		return nil, nil // 无效日期
	}

	return &DayBar{
		Date:   dateInt,
		Open:   roundTo2(open),
		High:   roundTo2(high),
		Low:    roundTo2(low),
		Close:  roundTo2(close),
		Amount: roundTo2(amount),
		Volume: volume,
	}, nil
}

// roundTo2 保留2位小数
func roundTo2(v float64) float64 {
	return float64(int64(v*100+0.5)) / 100
}

// GetRecentBars 获取最近N根K线
func (r *TDXReader) GetRecentBars(exchange Exchange, code string, days int) ([]DayBar, error) {
	bars, err := r.ReadDayFile(exchange, code)
	if err != nil {
		return nil, err
	}

	if len(bars) <= days {
		return bars, nil
	}
	return bars[len(bars)-days:], nil
}

// SearchStocks 搜索股票（根据代码或名称）
func (r *TDXReader) SearchStocks(exchange Exchange, query string, limit int) ([]StockInfo, error) {
	ldayPath := filepath.Join(r.basePath, "vipdoc", string(exchange), "lday")

	entries, err := os.ReadDir(ldayPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %w", ldayPath, err)
	}

	var results []StockInfo
	pattern := regexp.MustCompile(`^([shszbj])(\d{6})\.day$`)

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		matches := pattern.FindStringSubmatch(name)
		if matches == nil {
			continue
		}

		ex := Exchange(matches[1])
		code := matches[2]

		// 过滤
		if query != "" && !strings.Contains(strings.ToLower(code), strings.ToLower(query)) {
			continue
		}

		// 读取最新数据
		bars, err := r.ReadDayFile(ex, code)
		if err != nil || len(bars) == 0 {
			continue
		}

		lastBar := bars[len(bars)-1]
		results = append(results, StockInfo{
			Code:         code,
			Exchange:     string(ex),
			LastClose:    lastBar.Close,
			LastDate:     formatDate(lastBar.Date),
			TotalBars:    len(bars),
			LatestVolume: lastBar.Volume,
		})

		if len(results) >= limit {
			break
		}
	}

	return results, nil
}

// StockInfo 股票基本信息
type StockInfo struct {
	Code         string  `json:"code"`
	Exchange     string  `json:"exchange"`
	LastClose    float64 `json:"last_close"`
	LastDate     string  `json:"last_date"`
	TotalBars    int     `json:"total_bars"`
	LatestVolume int64   `json:"latest_volume"`
}

// GetStockList 获取指定市场的所有股票列表
func (r *TDXReader) GetStockList(exchange Exchange) ([]StockInfo, error) {
	return r.SearchStocks(exchange, "", 9999)
}

// CalculateIndicators 从日线数据计算技术指标
func CalculateIndicators(bars []DayBar) *TDXIndicators {
	if len(bars) < 20 {
		return nil
	}

	closes := make([]float64, len(bars))
	for i, b := range bars {
		closes[i] = b.Close
	}

	indicators := &TDXIndicators{}

	// MA5 / MA10 / MA20 / MA60
	indicators.MA5 = roundTo2(sma(closes, 5))
	indicators.MA10 = roundTo2(sma(closes, 10))
	indicators.MA20 = roundTo2(sma(closes, 20))
	if len(closes) >= 60 {
		indicators.MA60 = roundTo2(sma(closes, 60))
	}

	// RSI(14)
	indicators.RSI14 = roundTo2(rsi(closes, 14))

	// MACD
	dif, dea, macd := macd(closes, 12, 26, 9)
	indicators.MACD_DIF = roundTo2(dif)
	indicators.MACD_DEA = roundTo2(dea)
	indicators.MACD = roundTo2(macd)

	// 波动率 (20日)
	returns := make([]float64, len(closes)-1)
	for i := 1; i < len(closes); i++ {
		if closes[i-1] != 0 {
			returns[i-1] = closes[i]/closes[i-1] - 1
		}
	}
	if len(returns) >= 20 {
		indicators.Volatility20 = roundTo2(stddev(returns[len(returns)-20:]) * sqrt(252))
	}

	// 涨跌幅
	if len(closes) >= 2 {
		indicators.ChangePct = roundTo2((closes[len(closes)-1]/closes[len(closes)-2] - 1) * 100)
	}
	if len(closes) >= 5 {
		indicators.Change5d = roundTo2((closes[len(closes)-1]/closes[len(closes)-5] - 1) * 100)
	}
	if len(closes) >= 20 {
		indicators.Change20d = roundTo2((closes[len(closes)-1]/closes[len(closes)-20] - 1) * 100)
	}

	// 最高/最低
	highs := make([]float64, len(bars))
	lows := make([]float64, len(bars))
	for i, b := range bars {
		highs[i] = b.High
		lows[i] = b.Low
	}
	indicators.High20 = roundTo2(max(highs[len(highs)-20:]))
	indicators.Low20 = roundTo2(min(lows[len(lows)-20:]))

	// 成交量统计
	volumes := make([]float64, len(bars))
	for i, b := range bars {
		volumes[i] = float64(b.Volume)
	}
	if len(bars) >= 20 {
		indicators.AvgVolume20 = int64(avg(volumes[len(volumes)-20:]))
		if len(bars) >= 1 {
			currentVol := float64(bars[len(bars)-1].Volume)
			if indicators.AvgVolume20 > 0 {
				indicators.VolumeRatio = roundTo2(currentVol / float64(indicators.AvgVolume20))
			}
		}
	}

	return indicators
}

// TDXIndicators 技术指标
type TDXIndicators struct {
	MA5          float64 `json:"ma5"`
	MA10         float64 `json:"ma10"`
	MA20         float64 `json:"ma20"`
	MA60         float64 `json:"ma60,omitempty"`
	RSI14        float64 `json:"rsi_14"`
	MACD_DIF     float64 `json:"macd_dif"`
	MACD_DEA     float64 `json:"macd_dea"`
	MACD         float64 `json:"macd"`
	Volatility20 float64 `json:"volatility_20"`
	ChangePct    float64 `json:"change_pct"`
	Change5d     float64 `json:"change_5d"`
	Change20d    float64 `json:"change_20d"`
	High20       float64 `json:"high_20"`
	Low20        float64 `json:"low_20"`
	AvgVolume20  int64   `json:"avg_volume_20"`
	VolumeRatio  float64 `json:"volume_ratio"`
}

// ========== 技术指标计算辅助函数 ==========

func sma(values []float64, period int) float64 {
	if len(values) < period {
		return 0
	}
	return avg(values[len(values)-period:])
}

func avg(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range values {
		sum += v
	}
	return sum / float64(len(values))
}

func stddev(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	m := avg(values)
	variance := 0.0
	for _, v := range values {
		d := v - m
		variance += d * d
	}
	return sqrt(variance / float64(len(values)))
}

func sqrt(x float64) float64 {
	if x <= 0 {
		return 0
	}
	z := x
	for i := 0; i < 100; i++ {
		z = (z + x/z) / 2
	}
	return z
}

func max(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	m := values[0]
	for _, v := range values[1:] {
		if v > m {
			m = v
		}
	}
	return m
}

func min(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	m := values[0]
	for _, v := range values[1:] {
		if v < m {
			m = v
		}
	}
	return m
}

func rsi(closes []float64, period int) float64 {
	if len(closes) <= period {
		return 50
	}
	var gains, losses float64
	for i := len(closes) - period; i < len(closes); i++ {
		diff := closes[i] - closes[i-1]
		if diff > 0 {
			gains += diff
		} else {
			losses -= diff
		}
	}
	if gains == 0 && losses == 0 {
		return 50
	}
	avgGain := gains / float64(period)
	avgLoss := losses / float64(period)
	if avgLoss == 0 {
		return 100
	}
	rs := avgGain / avgLoss
	return 100 - 100/(1+rs)
}

func ema(values []float64, period int) []float64 {
	if len(values) == 0 {
		return nil
	}
	k := 2.0 / float64(period+1)
	result := make([]float64, len(values))
	result[0] = values[0]
	for i := 1; i < len(values); i++ {
		result[i] = values[i]*k + result[i-1]*(1-k)
	}
	return result
}

func macd(closes []float64, fast, slow, signal int) (dif, dea, macdVal float64) {
	if len(closes) < slow+signal {
		return 0, 0, 0
	}
	fastEMA := ema(closes, fast)
	slowEMA := ema(closes, slow)

	difs := make([]float64, len(closes))
	for i := range closes {
		difs[i] = fastEMA[i] - slowEMA[i]
	}

	deas := ema(difs, signal)

	dif = difs[len(difs)-1]
	dea = deas[len(deas)-1]
	macdVal = (dif - dea) * 2

	return
}

func formatDate(dateInt int) string {
	s := strconv.Itoa(dateInt)
	if len(s) == 8 {
		return s[:4] + "-" + s[4:6] + "-" + s[6:8]
	}
	return strconv.Itoa(dateInt)
}

// GetMarketOverview 获取市场概览（涨跌幅统计）
func (r *TDXReader) GetMarketOverview(exchange Exchange, limit int) (*MarketOverview, error) {
	stocks, err := r.GetStockList(exchange)
	if err != nil {
		return nil, err
	}

	if limit > 0 && len(stocks) > limit {
		stocks = stocks[:limit]
	}

	overview := &MarketOverview{
		Exchange:    string(exchange),
		TotalStocks: len(stocks),
		Advancers:   0,
		Decliners:   0,
		Flat:        0,
		LimitUp:     0, // 涨停
		LimitDown:   0, // 跌停
		Stocks:      make([]StockDetail, 0),
	}

	for _, s := range stocks {
		bars, err := r.GetRecentBars(exchange, s.Code, 60)
		if err != nil || len(bars) < 2 {
			continue
		}

		latest := bars[len(bars)-1]
		prevClose := bars[len(bars)-2].Close

		var changePct float64
		if prevClose > 0 {
			changePct = (latest.Close/prevClose - 1) * 100
		}

		switch {
		case changePct > 0:
			overview.Advancers++
		case changePct < 0:
			overview.Decliners++
		default:
			overview.Flat++
		}

		// A股涨跌停判断（主板10%，创业板/科创板20%）
		limitUp := false
		limitDown := false
		if changePct >= 9.9 {
			limitUp = true
			overview.LimitUp++
		} else if changePct <= -9.9 {
			limitDown = true
			overview.LimitDown++
		}
		// 创业板/科创板 (300xxx, 688xxx) 涨跌幅20%
		if (strings.HasPrefix(s.Code, "300") || strings.HasPrefix(s.Code, "688")) && changePct >= 19.9 {
			limitUp = true
			overview.LimitUp++
		}

		detail := StockDetail{
			Code:      s.Code,
			LastClose: latest.Close,
			ChangePct: roundTo2(changePct),
			Volume:    latest.Volume,
			Amount:    latest.Amount,
			High:      latest.High,
			Low:       latest.Low,
			LimitUp:   limitUp,
			LimitDown: limitDown,
		}

		indicators := CalculateIndicators(bars)
		if indicators != nil {
			detail.MA5 = indicators.MA5
			detail.MA20 = indicators.MA20
			detail.RSI14 = indicators.RSI14
			detail.VolumeRatio = indicators.VolumeRatio
		}

		overview.Stocks = append(overview.Stocks, detail)
	}

	return overview, nil
}

// MarketOverview 市场概览
type MarketOverview struct {
	Exchange    string        `json:"exchange"`
	TotalStocks int           `json:"total_stocks"`
	Advancers   int           `json:"advancers"`
	Decliners   int           `json:"decliners"`
	Flat        int           `json:"flat"`
	LimitUp     int           `json:"limit_up"`
	LimitDown   int           `json:"limit_down"`
	Stocks      []StockDetail `json:"stocks"`
}

// StockDetail 股票详情
type StockDetail struct {
	Code        string  `json:"code"`
	LastClose   float64 `json:"last_close"`
	ChangePct   float64 `json:"change_pct"`
	Volume      int64   `json:"volume"`
	Amount      float64 `json:"amount"`
	High        float64 `json:"high"`
	Low         float64 `json:"low"`
	LimitUp     bool    `json:"limit_up"`
	LimitDown   bool    `json:"limit_down"`
	MA5         float64 `json:"ma5,omitempty"`
	MA20        float64 `json:"ma20,omitempty"`
	RSI14       float64 `json:"rsi_14,omitempty"`
	VolumeRatio float64 `json:"volume_ratio,omitempty"`
}

// GetSectorPerformance 获取板块表现（按代码前缀简单分类）
func (r *TDXReader) GetSectorPerformance(exchange Exchange) ([]SectorPerf, error) {
	stocks, err := r.GetStockList(exchange)
	if err != nil {
		return nil, err
	}

	// 按代码前缀分组（简化行业分类）
	sectors := map[string]*sectorData{
		"银行":     {prefixes: []string{"600", "601", "603", "000", "001", "605"}},
		"券商":     {prefixes: []string{"600", "601"}},
		"保险":     {prefixes: []string{"601", "602"}},
		"白酒/消费":  {prefixes: []string{"600", "000", "002", "300"}},
		"医药":     {prefixes: []string{"600", "000", "300"}},
		"科技/半导体": {prefixes: []string{"688", "300", "002"}},
		"新能源":    {prefixes: []string{"300", "002", "601"}},
		"地产":     {prefixes: []string{"600", "000", "002"}},
		"基建":     {prefixes: []string{"600", "601", "000"}},
	}

	// 简化版：按主要板块代码前缀分组
	sectorMap := make(map[string]*sectorAgg)
	for _, s := range stocks {
		bars, err := r.GetRecentBars(exchange, s.Code, 20)
		if err != nil || len(bars) < 2 {
			continue
		}
		latest := bars[len(bars)-1]
		prevClose := bars[len(bars)-2].Close

		var changePct float64
		if prevClose > 0 {
			changePct = (latest.Close/prevClose - 1) * 100
		}

		sector := classifySector(s.Code)
		if sectorMap[sector] == nil {
			sectorMap[sector] = &sectorAgg{Name: sector, Changes: make([]float64, 0)}
		}
		sectorMap[sector].Changes = append(sectorMap[sector].Changes, changePct)
	}

	var results []SectorPerf
	for _, agg := range sectorMap {
		if len(agg.Changes) == 0 {
			continue
		}
		perf := SectorPerf{
			Name:       agg.Name,
			StockCount: len(agg.Changes),
			AvgChange:  roundTo2(avg(agg.Changes)),
			MaxChange:  roundTo2(max(agg.Changes)),
			MinChange:  roundTo2(min(agg.Changes)),
		}
		results = append(results, perf)
	}

	// 按平均涨幅排序
	sort.Slice(results, func(i, j int) bool {
		return results[i].AvgChange > results[j].AvgChange
	})

	_ = sectors
	return results, nil
}

// SectorPerf 板块表现
type SectorPerf struct {
	Name       string  `json:"name"`
	StockCount int     `json:"stock_count"`
	AvgChange  float64 `json:"avg_change"`
	MaxChange  float64 `json:"max_change"`
	MinChange  float64 `json:"min_change"`
}

type sectorData struct {
	prefixes []string
}

type sectorAgg struct {
	Name    string
	Changes []float64
}

// classifySector 简单行业分类
func classifySector(code string) string {
	if len(code) < 3 {
		return "其他"
	}
	prefix := code[:3]

	switch {
	case prefix >= "600" && prefix < "606":
		return "上海主板"
	case prefix >= "688" && prefix < "689":
		return "科创板"
	case prefix >= "000" && prefix < "003":
		return "深圳主板"
	case prefix >= "300" && prefix < "302":
		return "创业板"
	case prefix >= "8" && prefix < "9":
		return "北交所/新三板"
	default:
		return "其他"
	}
}

// ========== 额外的代码映射辅助函数 ==========

// CommonAStockCodes 获取A股常见股票代码列表（名称从字典动态加载，非硬编码）
func CommonAStockCodes() []AStockInfo {
	codes := []struct{ Code, Exchange string }{
		{"600519", "sh"},
		{"000858", "sz"},
		{"601318", "sh"},
		{"600036", "sh"},
		{"601398", "sh"},
		{"000001", "sz"},
		{"600276", "sh"},
		{"000333", "sz"},
		{"002594", "sz"},
		{"300750", "sz"},
		{"600900", "sh"},
		{"601166", "sh"},
		{"600887", "sh"},
		{"000651", "sz"},
		{"600030", "sh"},
		{"601888", "sh"},
		{"600000", "sh"},
		{"000002", "sz"},
		{"601012", "sh"},
		{"300059", "sz"},
	}

	loader := data.GetDictLoader()
	result := make([]AStockInfo, len(codes))
	for i, c := range codes {
		result[i] = AStockInfo{
			Code:     c.Code,
			Name:     loader.GetStockName(c.Code), // 从字典动态获取名称
			Exchange: c.Exchange,
		}
	}
	return result
}

// AStockInfo A股基本信息
type AStockInfo struct {
	Code     string `json:"code"`
	Name     string `json:"name"`
	Exchange string `json:"exchange"`
}
