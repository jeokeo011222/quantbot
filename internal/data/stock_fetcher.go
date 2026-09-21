package data

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// StockSnapshot 股票快照
type StockSnapshot struct {
	Code          string  `json:"code"`
	Name          string  `json:"name"`
	Market        string  `json:"market"` // sh/sz
	CurrentPrice  float64 `json:"currentPrice"`
	PrevClose     float64 `json:"prevClose"`
	Open          float64 `json:"open"`
	High          float64 `json:"high"`
	Low           float64 `json:"low"`
	Volume        float64 `json:"volume"`
	Turnover      float64 `json:"turnover"`
	ChangePercent float64 `json:"changePercent"`
	ChangeAmount  float64 `json:"changeAmount"`
	Timestamp     int64   `json:"timestamp"`
	IsMock        bool    `json:"isMock"`

	// ===== 盘口字段（实时行情数据源提供；缺失时为空/0，绝不伪造） =====
	// 五档买卖挂单价量：bid/ask 价格单位元、量单位手（1手=100股）
	BidPrices  []float64 `json:"bidPrices"`
	AskPrices  []float64 `json:"askPrices"`
	BidVolumes []float64 `json:"bidVolumes"`
	AskVolumes []float64 `json:"askVolumes"`
	// 外盘/内盘（手）：主动买入量 / 主动卖出量
	OutVolume float64 `json:"outVolume"`
	InVolume  float64 `json:"inVolume"`
	// 换手率(%) / 量比
	TurnoverRate float64 `json:"turnoverRate"`
	VolumeRatio  float64 `json:"volumeRatio"`
}

// MarketIndexSnapshot 指数快照
type MarketIndexSnapshot struct {
	Code          string  `json:"code"`
	Name          string  `json:"name"`
	Current       float64 `json:"current"`
	Change        float64 `json:"change"`
	ChangePercent float64 `json:"changePercent"`
	IsMock        bool    `json:"isMock"`
}

// 实时行情数据源统一收敛于 datasource.go 的 UnifiedDataSource + GetDataSource()，
// 此处不再定义并行的 MarketDataProvider / 全局提供者注册表。
// TencentMarketDataProvider 已迁移至 internal/tools/tencent_provider.go 统一实现，
// 本包仅保留腾讯行情 HTTP 拉取底层函数（FetchReal* / BuildTencentCodes 等），供各 provider 复用。

// Tencent 腾讯行情 API 字段索引（以 ~ 分隔）
const (
	TENCENT_FIELD_NAME     = 1
	TENCENT_FIELD_CODE     = 2
	TENCENT_FIELD_CURRENT  = 3
	TENCENT_FIELD_PREV     = 4
	TENCENT_FIELD_OPEN     = 5
	TENCENT_FIELD_VOLUME   = 6
	TENCENT_FIELD_HIGH     = 33
	TENCENT_FIELD_LOW      = 34
	TENCENT_FIELD_TURNOVER = 37

	// ===== 盘口字段（qt.gtimg.cn ~ 分隔格式，均为手，1手=100股） =====
	TENCENT_FIELD_OUT_VOL        = 7  // 外盘（主动买）
	TENCENT_FIELD_IN_VOL         = 8  // 内盘（主动卖）
	TENCENT_FIELD_BID_PRICE_BASE = 9  // 买一价（买一~买五价 = 9,11,13,15,17）
	TENCENT_FIELD_BID_VOL_BASE   = 10 // 买一量（买一~买五量 = 10,12,14,16,18）
	TENCENT_FIELD_ASK_PRICE_BASE = 19 // 卖一价（卖一~卖五价 = 19,21,23,25,27）
	TENCENT_FIELD_ASK_VOL_BASE   = 20 // 卖一量（卖一~卖五量 = 20,22,24,26,28）
	TENCENT_FIELD_TURNOVER_RATE  = 38 // 换手率(%)
	TENCENT_FIELD_VOLUME_RATIO   = 49 // 量比
)

// WatchStock 已迁移至 SQLite WatchStock 表（定义在 sqlite.go）
// 保留 detectMarket 函数供内部使用

// detectMarket 根据代码判断市场（上交所/深交所/北交所）。
// 兼容输入：带前缀小写(sh601700)、带前缀大写(SH601700)、纯数字(601700)、
// 指数全代码(sh000001 -> sh)。统一转发到 DetectMarketFromCode，保证全项目推断一致。
func detectMarket(code string) string {
	return DetectMarketFromCode(code)
}

// ============ 股票代码格式统一工具 ============
// 规范：项目内部唯一规范的**股票代码**格式为「带小写市场前缀」，如 sh601700 / sz000001 / bj430047。
// Code 字段一律为 6 位纯数字，Market 字段一律为小写 sh/sz/bj。本组函数收敛全部
// 前缀有无、大小写、市场推断逻辑，取代各处手动拼接/裁剪，避免格式不一致导致的查找 miss。
// 注意：指数代码（如指数全代码 sh000001）与个股代码数字规则不同，推断时按前缀优先。

// DetectMarketFromCode 判断一段股票代码所属市场，返回小写市场代码 sh/sz/bj。
// 优先识别显式前缀，其次按纯数字首位规则判断：
//   - 上交所 6xxxxx / 68xxxx / 50xxxx（sh）
//   - 深交所 0xxxxx / 3xxxxx / 15xxxx（sz）
//   - 北交所 4xxxxx / 83xxxx / 87xxxx / 92xxxx（bj）
func DetectMarketFromCode(code string) string {
	code = strings.TrimSpace(code)
	if code == "" {
		return "sh"
	}
	lower := strings.ToLower(code)

	// 1) 显式市场前缀优先
	if strings.HasPrefix(lower, "sh") {
		return "sh"
	}
	if strings.HasPrefix(lower, "sz") {
		return "sz"
	}
	if strings.HasPrefix(lower, "bj") {
		return "bj"
	}

	// 2) 去掉常见的市场分隔符变体（SH:600519 / sh.600519 / 600519.SH 等）
	pure := trimMarketPrefixForms(code)
	if pure == "" {
		pure = code
	}
	if pure[0] < '0' || pure[0] > '9' {
		// 非数字开头且无已知前缀，无法判断，默认深市以外的沪市
		return "sh"
	}

	// 3) 按纯数字首位规则判断（只对前 6 位判断，忽略数字后的尾缀）
	if len(pure) < 6 {
		return "sh"
	}
	switch pure[0] {
	case '6', '5':
		return "sh"
	case '0', '3', '1', '2':
		// 0xxxxx / 3xxxxx 深市；15xxxx 深基金/深市；2xxxxx(如200326 B股)深市
		return "sz"
	case '8', '4', '9', '7':
		// 8xxxxx/4xxxxx/920xxx 北交所
		if (pure[0] == '4' && pure[1] == '3') || pure[0] == '8' || (pure[0] == '9' && pure[1] == '2') || (pure[0] == '7' && (pure[1] == '3' || pure[1] == '4')) {
			return "bj"
		}
		return "sh"
	}
	return "sh"
}

// trimMarketPrefixForms 去掉代码中的市场前缀及分隔符变体，返回纯净数字部分。
// 支持：sh601700->601700、SH:601700、sh.601700、601700.SH、600519.XSHG 等。
func trimMarketPrefixForms(code string) string {
	lower := strings.ToLower(code)
	// 长度拆分的常见前缀
	for _, p := range []string{"sh", "sz", "bj"} {
		if strings.HasPrefix(lower, p) || strings.HasPrefix(lower, p+".") || strings.HasPrefix(lower, p+":") {
			rest := code[len(p):]
			rest = strings.TrimLeft(rest, ".:/ ")
			return rest
		}
	}
	// 后缀市场代码：603110.SH / 600519.XSHG
	if idx := strings.IndexAny(lower, ".:"); idx > 0 {
		upper := strings.ToUpper(code)
		rest := code[:idx]
		if len(rest) <= 6 {
			return rest
		}
		_ = upper
	}
	return code
}

// PureCodeFromCode 提取股票代码的数字部分（去掉市场前缀与分隔符）。
// 输入 sh601700 / SH:601700 / 601700 / 601700.SH 均返回 "601700"。
func PureCodeFromCode(code string) string {
	code = strings.TrimSpace(code)
	if code == "" {
		return ""
	}
	for _, p := range []string{"sh", "sz", "bj"} {
		lower := strings.ToLower(code)
		if strings.HasPrefix(lower, p) {
			rest := strings.TrimLeft(code[len(p):], ".:/ ")
			return rest
		}
	}
	// 去掉 .SH/.XSHG 后缀
	if idx := strings.IndexAny(code, ".:/ "); idx >= 0 {
		return code[:idx]
	}
	return code
}

// MarketCode 返回股票的规范全代码，即「小写市场前缀 + 6位数字」，如 sh601700。
// 内部统一格式，所有需要规范化股票代码的地方应使用本函数。
func MarketCode(code string) string {
	market := DetectMarketFromCode(code)
	return market + PureCodeFromCode(code)
}

// MarketCodeLower 返回规范全代码并保证为小写（等价于 MarketCode，保留兼容别名）。
func MarketCodeLower(code string) string {
	return strings.ToLower(MarketCode(code))
}

// ToMarketCode 为 MarketCode 的语义别名，返回带小写市场前缀的规范代码。
func ToMarketCode(code string) string { return MarketCode(code) }

// ToPureCode 返回股票的 6 位纯数字代码（不带市场前缀）。
func ToPureCode(code string) string { return PureCodeFromCode(code) }

// FetchRealStockSnapshots 从腾讯财经 API 拉取实时行情（真实数据，非 mock）
// 腾讯行情接口的响应不是标准 JSON，而是 JS 赋值形式：v_sh600519="1~贵州茅台~600519~...";
// 注意：腾讯 API 返回 GBK 编码，需要解码为 UTF-8
func FetchRealStockSnapshots(codes []string) ([]StockSnapshot, error) {
	if len(codes) == 0 {
		return nil, fmt.Errorf("empty codes")
	}

	// 构造腾讯 URL（腾讯行情接口，HTTP 可访问，无需 CORS）
	url := "http://qt.gtimg.cn/q=" + strings.Join(codes, ",")

	client := &http.Client{Timeout: 8 * time.Second}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 QuantBot")
	req.Header.Set("Referer", "http://finance.qq.com")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tencent api request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("tencent api status %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	// GBK → UTF-8 解码
	decodedBody := decodeGBK(body)

	parsed := parseTencentStockResponse(decodedBody)
	if len(parsed) == 0 {
		return nil, fmt.Errorf("tencent api returned empty data")
	}

	return parsed, nil
}

// decodeGBK 将 GBK 编码的字节转换为 UTF-8 字符串
func decodeGBK(body []byte) string {
	decoded, err := simplifiedchinese.GBK.NewDecoder().Bytes(body)
	if err != nil {
		// 解码失败则返回原始字符串（可能已部分可读）
		log.Printf("[StockFetcher] GBK decode error: %v, using raw string", err)
		return string(body)
	}
	return string(decoded)
}

// parseTencentStockResponse 解析腾讯行情接口响应
func parseTencentStockResponse(text string) []StockSnapshot {
	var results []StockSnapshot
	lines := strings.Split(text, ";")
	// 匹配 v_sh600519="1~贵州茅台~600519~..." 格式
	re := regexp.MustCompile(`v_(sh|sz)(\d+)="([^"]*)"`)

	for _, line := range lines {
		matches := re.FindStringSubmatch(line)
		if len(matches) < 4 {
			continue
		}
		market := matches[1]
		code := matches[2]
		dataStr := matches[3]
		if dataStr == "" {
			continue
		}
		fields := strings.Split(dataStr, "~")
		if len(fields) < 10 {
			continue
		}

		name := safeField(fields, TENCENT_FIELD_NAME)
		current := safeFloat(fields, TENCENT_FIELD_CURRENT)
		prev := safeFloat(fields, TENCENT_FIELD_PREV)
		open := safeFloat(fields, TENCENT_FIELD_OPEN)
		high := safeFloat(fields, TENCENT_FIELD_HIGH)
		low := safeFloat(fields, TENCENT_FIELD_LOW)
		vol := safeFloat(fields, TENCENT_FIELD_VOLUME)
		// 腾讯 qt.gtimg.cn 字段37 为当日成交额，单位为「万元」，需换算为「元」后
		// 与持仓/下单的名义金额（元）统一，否则单位差 1e4 会把正常股票误判成薄盘
		// （如：雅克科技日成交额 2.8 亿元被读成 28219 元 → 触发流动性拥挤拦截）。
		turnover := safeFloat(fields, TENCENT_FIELD_TURNOVER) * 10000

		if current <= 0 || prev <= 0 {
			continue
		}

		changeAmount := current - prev
		changePercent := 0.0
		if prev > 0 {
			changePercent = (changeAmount / prev) * 100
		}

		// 盘口五档：买一~买五 / 卖一~卖五（价量成对，缺字段取0，绝不伪造）
		bidPrices := make([]float64, 0, 5)
		bidVolumes := make([]float64, 0, 5)
		askPrices := make([]float64, 0, 5)
		askVolumes := make([]float64, 0, 5)
		for i := 0; i < 5; i++ {
			bp := safeFloat(fields, TENCENT_FIELD_BID_PRICE_BASE+2*i)
			bv := safeFloat(fields, TENCENT_FIELD_BID_VOL_BASE+2*i)
			ap := safeFloat(fields, TENCENT_FIELD_ASK_PRICE_BASE+2*i)
			av := safeFloat(fields, TENCENT_FIELD_ASK_VOL_BASE+2*i)
			if bp > 0 {
				bidPrices = append(bidPrices, bp)
			}
			bidVolumes = append(bidVolumes, bv)
			if ap > 0 {
				askPrices = append(askPrices, ap)
			}
			askVolumes = append(askVolumes, av)
		}

		results = append(results, StockSnapshot{
			Code:          code,
			Name:          name,
			Market:        market,
			CurrentPrice:  current,
			PrevClose:     prev,
			Open:          open,
			High:          high,
			Low:           low,
			Volume:        vol,
			Turnover:      turnover,
			ChangePercent: changePercent,
			ChangeAmount:  changeAmount,
			Timestamp:     time.Now().UnixMilli(),
			IsMock:        false,

			BidPrices:    bidPrices,
			AskPrices:    askPrices,
			BidVolumes:   bidVolumes,
			AskVolumes:   askVolumes,
			OutVolume:    safeFloat(fields, TENCENT_FIELD_OUT_VOL),
			InVolume:     safeFloat(fields, TENCENT_FIELD_IN_VOL),
			TurnoverRate: safeFloat(fields, TENCENT_FIELD_TURNOVER_RATE),
			VolumeRatio:  safeFloat(fields, TENCENT_FIELD_VOLUME_RATIO),
		})
	}
	return results
}

func safeField(fields []string, idx int) string {
	if idx < len(fields) {
		return strings.TrimSpace(fields[idx])
	}
	return ""
}

func safeFloat(fields []string, idx int) float64 {
	if idx >= len(fields) {
		return 0
	}
	s := strings.TrimSpace(fields[idx])
	if s == "" {
		return 0
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return f
}

// BuildTencentCodes 把股票代码统一为腾讯接口需要的「市场前缀小写+数字」格式（如 sh600519）。
// 使用统一的 DetectMarketFromCode/PureCodeFromCode 推断市场，避免对纯 6 位数字一律加 sh
// 导致深市/北交所代码错误。
func BuildTencentCodes(codes []string) []string {
	out := make([]string, 0, len(codes))
	for _, c := range codes {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		// 统一规范格式：市场前缀(小写) + 纯数字
		market := DetectMarketFromCode(c)
		pure := PureCodeFromCode(c)
		if pure == "" {
			continue
		}
		out = append(out, market+pure)
	}
	return out
}

// FetchRealtimeStockSnapshots 获取实时股票快照数据
// 优先使用统一实时行情数据源（用户设置的数据源），失败时回退到腾讯财经
// 严禁返回任何虚假数据，获取失败时返回空切片
func FetchRealtimeStockSnapshots(codes []string) ([]StockSnapshot, bool) {
	// 优先尝试统一活跃数据源（native_tdx / tdx_mcp / tdx_terminal / tencent）
	if ds := GetDataSource(); ds != nil {
		data, err := ds.GetStockSnapshots(codes)
		if err == nil && len(data) > 0 {
			return data, true
		}
		log.Printf("[StockFetcher] unified source %s failed: %v, falling back to Tencent", ds.Source(), err)
	}

	// 回退到腾讯财经
	tencentCodes := BuildTencentCodes(codes)
	data, err := FetchRealStockSnapshots(tencentCodes)
	if err == nil && len(data) > 0 {
		return data, true
	}
	log.Printf("[StockFetcher] Real stock data fetch failed: %v, returning empty (no mock data)", err)
	return make([]StockSnapshot, 0), false
}

// FetchRealIndexSnapshots 拉取指数行情（同样需要 GBK 解码）
func FetchRealIndexSnapshots(codes []string) ([]MarketIndexSnapshot, error) {
	if len(codes) == 0 {
		return nil, fmt.Errorf("empty index codes")
	}
	tencentCodes := BuildTencentCodes(codes)
	url := "http://qt.gtimg.cn/q=" + strings.Join(tencentCodes, ",")

	client := &http.Client{Timeout: 8 * time.Second}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 QuantBot")
	req.Header.Set("Referer", "http://finance.qq.com")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("index api status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	// GBK → UTF-8 解码
	decodedBody := decodeGBK(body)

	stocks := parseTencentStockResponse(decodedBody)
	indices := make([]MarketIndexSnapshot, 0, len(stocks))
	for _, s := range stocks {
		indices = append(indices, MarketIndexSnapshot{
			Code:          s.Code,
			Name:          s.Name,
			Current:       s.CurrentPrice,
			Change:        s.ChangeAmount,
			ChangePercent: s.ChangePercent,
			IsMock:        false,
		})
	}
	return indices, nil
}

// FetchRealtimeIndexSnapshots 获取实时指数快照数据
// 优先使用统一实时行情数据源（用户设置的数据源），失败时回退到腾讯财经
// 严禁返回任何虚假数据，获取失败时返回空切片
func FetchRealtimeIndexSnapshots(codes []string) ([]MarketIndexSnapshot, bool) {
	// 优先尝试统一活跃数据源
	if ds := GetDataSource(); ds != nil {
		data, err := ds.GetIndexSnapshots(codes)
		if err == nil && len(data) > 0 {
			return data, true
		}
		log.Printf("[StockFetcher] unified source %s index failed: %v, falling back to Tencent", ds.Source(), err)
	}

	// 回退到腾讯财经
	data, err := FetchRealIndexSnapshots(codes)
	if err == nil && len(data) > 0 {
		return data, true
	}
	log.Printf("[StockFetcher] Real index data fetch failed: %v, returning empty (no mock data)", err)
	return make([]MarketIndexSnapshot, 0), false
}

// round2 保留 2 位小数
func round2(v float64) float64 {
	return float64(int64(v*100+0.5)) / 100
}

// pseudoRand 基于字符串的伪随机数（保证同一 code 多次调用结果稳定）
func pseudoRand(seed string) float64 {
	h := uint64(0)
	for _, c := range seed {
		h = h*31 + uint64(c)
	}
	return float64(h%10000) / 10000.0
}
