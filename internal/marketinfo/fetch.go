// Package marketinfo 提供从公开网络数据源抓取真实市场信息的原子能力，
// 供智能体工具（get_external_market / get_capital_flow / get_limitup_board /
// get_market_news / get_stock_announcement / get_lhb 等）复用。
//
// 数据来源原则（对应项目硬约束「严禁伪造数据」）：
//   - 一律请求真实公开接口（腾讯财经 / 东方财富 / 雪球），解析失败或网络异常时
//     返回 error，绝不猜测、绝不填充编造数值；
//   - 每个抓取器返回明确的数据来源（Source）供上层标注，保证可审计。
package marketinfo

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/text/encoding/simplifiedchinese"
)

const (
	httpTimeout = 12 * time.Second
	userAgent   = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
)

var httpClient = &http.Client{Timeout: httpTimeout}

// httpGet 带一次快速重试的 GET（应对外部数据源偶发抖动）。
func httpGet(ctx context.Context, url string, headers map[string]string) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(400 * time.Millisecond):
			}
		}
		body, err := doGet(ctx, url, headers)
		if err == nil {
			return body, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	return nil, lastErr
}

func doGet(ctx context.Context, url string, headers map[string]string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Referer", "https://finance.qq.com")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}
	return io.ReadAll(resp.Body)
}

// decodeGBK 将 GBK 编码字节转换为 UTF-8 字符串（腾讯财经等旧接口为 GBK）。
func decodeGBK(body []byte) string {
	if decoded, err := simplifiedchinese.GBK.NewDecoder().Bytes(body); err == nil {
		return string(decoded)
	}
	return string(body)
}

// ==================== 外部市场（隔夜外围） ====================

// GlobalMarketPoint 一根外部市场标的（指数/期货/汇率）快照。
type GlobalMarketPoint struct {
	Code      string  `json:"code"`
	Name      string  `json:"name"`
	Current   float64 `json:"current"`
	ChangePct float64 `json:"change_pct"`
	Change    float64 `json:"change"`
}

// FetchExternalMarkets 拉取隔夜外围市场快照（腾讯财经全球代码）：
//   - 美股：usDJI(道指) / usIXIC(纳指) / usINX(标普)
//   - 港股：hkHSI(恒生) / hkHSCEI(国企)
//   - 富时A50期货：hkCHA50CFD
//
// 解析失败仅跳过对应标的，至少返回一个元素才视为成功。
func FetchExternalMarkets(ctx context.Context) ([]GlobalMarketPoint, string, error) {
	codes := map[string]string{
		"usDJI":      "道指",
		"usIXIC":     "纳斯达克",
		"usINX":      "标普500",
		"hkHSI":      "恒生指数",
		"hkHSCEI":    "恒生国企",
		"hkCHA50CFD": "富时A50期货",
	}
	keys := make([]string, 0, len(codes))
	for c := range codes {
		keys = append(keys, c)
	}
	url := "http://qt.gtimg.cn/q=" + strings.Join(keys, ",")
	body, err := httpGet(ctx, url, nil)
	if err != nil {
		return nil, "", fmt.Errorf("外部市场行情获取失败(%s): %w", "tencent", err)
	}
	txt := decodeGBK(body)
	re := regexp.MustCompile(`v_([A-Za-z0-9_]+)="([^"]*)"`)
	points := make([]GlobalMarketPoint, 0, len(keys))
	for _, m := range re.FindAllStringSubmatch(txt, -1) {
		code := m[1]
		name, ok := codes[code]
		if !ok {
			continue
		}
		f := strings.Split(m[2], "~")
		if len(f) < 6 {
			continue
		}
		cur, _ := strconv.ParseFloat(strings.TrimSpace(f[3]), 64)
		prev, _ := strconv.ParseFloat(strings.TrimSpace(f[4]), 64)
		changePct := 0.0
		change := 0.0
		if prev > 0 {
			change = cur - prev
			changePct = change / prev * 100
		}
		points = append(points, GlobalMarketPoint{
			Code:      code,
			Name:      name,
			Current:   cur,
			ChangePct: round2(changePct),
			Change:    round2(change),
		})
	}
	if len(points) == 0 {
		return nil, "", fmt.Errorf("外部市场行情未解析到数据(腾讯全球代码)")
	}
	return points, "tencent(qt.gtimg.cn) 全球指数实时", nil
}

// ==================== 涨停/跌停/连板（打板情绪数据） ====================

// LimitUpItem 涨停池单个标的。
type LimitUpItem struct {
	Code      string  `json:"code"`
	Name      string  `json:"name"`
	ChangePct float64 `json:"change_pct"`
	LimitPct  float64 `json:"limit_pct"`  // 涨幅%(涨停价)
	Lianban   int     `json:"lianban"`    // 连板数
	FirstTime string  `json:"first_time"` // 首次封板时间 HH:MM
	LastTime  string  `json:"last_time"`  // 最后封板时间
	SealMoney float64 `json:"seal_money"` // 封单资金(亿)
	Amount    float64 `json:"amount"`     // 成交额(亿)
}

// LimitUpBoard 涨停行情（东方财富涨停股池 + 水池）。
type LimitUpBoard struct {
	Date         string        `json:"date"`
	LimitUpCnt   int           `json:"limit_up_cnt"`
	LimitDownCnt int           `json:"limit_down_cnt"`
	ZhabanCnt    int           `json:"zhaban_cnt"`
	LianbanTop   int           `json:"lianban_top"`
	Items        []LimitUpItem `json:"items"`
}

// FetchLimitUpBoard 从东方财富涨停股池(zp)/跌停水池(dt)/炸板池(zp)拉取真实行情。
func FetchLimitUpBoard(ctx context.Context) (*LimitUpBoard, string, error) {
	board := &LimitUpBoard{Date: time.Now().Format("2006-01-02"), ZhabanCnt: -1}
	source := "eastmoney(push2ex) 涨停/跌停/炸板股池"
	date := time.Now().Format("20060102")

	if zup, ztop, err := FetchZTPool(ctx, "getTopicZTPool", date); err == nil {
		board.LimitUpCnt = len(zup)
		board.Items = zup
		board.LianbanTop = ztop
	} else {
		source += fmt.Sprintf("(涨停池失败:%s)", briefErr(err))
	}
	if ddown, _, err := FetchZTPool(ctx, "getTopicDTPool", date); err == nil {
		board.LimitDownCnt = len(ddown)
	}
	if zhaban, _, err := FetchZTPool(ctx, "getPrisonBreakPool", date); err == nil {
		board.ZhabanCnt = len(zhaban)
	}

	if board.LimitUpCnt == 0 && len(board.Items) == 0 {
		return nil, source, fmt.Errorf("涨停池未取到数据")
	}
	return board, source, nil
}

type ztPoolResp struct {
	Data struct {
		Pools []struct {
			C      string  `json:"c"`
			N      string  `json:"n"`
			ZdPct  float64 `json:"zdp"`
			Lbc    int     `json:"lbc"`
			Fbt    string  `json:"fbt"`
			Lbt    string  `json:"lbt"`
			Fund   float64 `json:"fund"`
			Amount float64 `json:"amount"`
		} `json:"pools"`
	} `json:"data"`
}

// FetchZTPool 拉取单个涨停/跌停/炸板股池，返回标的行为及其最高连板数。
func FetchZTPool(ctx context.Context, api, date string) ([]LimitUpItem, int, error) {
	url := fmt.Sprintf("https://push2ex.eastmoney.com/%s?ut=7eea3edcaed734bea9cbfc24409ed989&dpt=wz.ztzt&Pageindex=0&pagesize=300&sort=fbt%%3Aasc&date=%s", api, date)
	body, err := httpGet(ctx, url, map[string]string{"Referer": "https://quote.eastmoney.com/"})
	if err != nil {
		return nil, 0, fmt.Errorf("eastmoney %s 失败: %w", api, err)
	}
	var j ztPoolResp
	if err := json.Unmarshal(body, &j); err != nil {
		return nil, 0, fmt.Errorf("eastmoney %s JSON解析失败: %w", api, err)
	}
	items := make([]LimitUpItem, 0, len(j.Data.Pools))
	top := 0
	for _, p := range j.Data.Pools {
		items = append(items, LimitUpItem{
			Code:      p.C,
			Name:      p.N,
			ChangePct: p.ZdPct,
			Lianban:   p.Lbc,
			FirstTime: p.Fbt,
			LastTime:  p.Lbt,
			SealMoney: p.Fund / 1e8,
			Amount:    p.Amount / 1e8,
		})
		if p.Lbc > top {
			top = p.Lbc
		}
	}
	return items, top, nil
}

// ==================== 资金流（两融余额） ====================

// MarginBalance 两融（融资余额）史数据点，亿元。
type MarginPoint struct {
	Date    string  `json:"date"`
	Balance float64 `json:"balance"` // 亿元
}

// FetchMarginHistory 从东方财富数据中心获取三市融资余额历史（亿元），作为真实杠杆资金流向指标。
func FetchMarginHistory(ctx context.Context, days int) ([]MarginPoint, string, error) {
	if days <= 0 {
		days = 30
	}
	url := "https://datacenter-web.eastmoney.com/api/data/v1/get?reportName=RPT_MARGIN_DATASTATISTICS&columns=ALL&filter=(MARKET%3D%22%E4%B8%89%E5%B8%82%22)&pageNumber=1&pageSize=" +
		strconv.Itoa(days) + "&sortTypes=-1&sortColumns=TRADE_DATE&source=DataCenter&client=WAP"
	body, err := httpGet(ctx, url, map[string]string{"Referer": "https://data.eastmoney.com/"})
	if err != nil {
		return nil, "", fmt.Errorf("东方财富融资余额获取失败: %w", err)
	}
	var j struct {
		Result *struct {
			Data []struct {
				TradeDate  string  `json:"TRADE_DATE"`
				FinBalance float64 `json:"FIN_BALANCE"`
			} `json:"data"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &j); err != nil {
		return nil, "", fmt.Errorf("东方财富融资余额解析失败: %w", err)
	}
	if j.Result == nil || len(j.Result.Data) == 0 {
		return nil, "", fmt.Errorf("东方财富融资余额返回空数据")
	}
	pts := make([]MarginPoint, 0, len(j.Result.Data))
	for _, r := range j.Result.Data {
		if r.FinBalance <= 0 {
			continue
		}
		pts = append(pts, MarginPoint{Date: strings.Split(r.TradeDate, " ")[0], Balance: r.FinBalance / 1e8})
	}
	if len(pts) == 0 {
		return nil, "", fmt.Errorf("东方财富融资余额无有效数据")
	}
	return pts, "eastmoney(datacenter) 融资余额", nil
}

// ==================== 北向资金（沪深股通） ====================

// FetchNorthboundRealtime 从同花顺 hsgtApi 拉取当日沪深股通实时累计净流入（亿元，沪股通+深股通）。
// 参考 tradex-hub northbound.py：data.hexin.cn/market/hsgtApi/method/dayChart/ 返回分钟级
// HGT/SGT 累计净买入序列，取最后一根即当日累计；非交易时段/节假日无数据时返回 error。
func FetchNorthboundRealtime(ctx context.Context) (float64, string, error) {
	url := "https://data.hexin.cn/market/hsgtApi/method/dayChart/"
	body, err := httpGet(ctx, url, map[string]string{
		"Host":    "data.hexin.cn",
		"Referer": "https://data.hexin.cn/",
	})
	if err != nil {
		return 0, "", fmt.Errorf("同花顺北向资金获取失败: %w", err)
	}
	var j struct {
		Time []string  `json:"time"`
		Hgt  []float64 `json:"hgt"`
		Sgt  []float64 `json:"sgt"`
	}
	if err := json.Unmarshal(body, &j); err != nil {
		return 0, "", fmt.Errorf("同花顺北向资金解析失败: %w", err)
	}
	if len(j.Hgt) == 0 || len(j.Sgt) == 0 {
		return 0, "", fmt.Errorf("同花顺北向资金无实时数据(非交易时段或节假日)")
	}
	return j.Hgt[len(j.Hgt)-1] + j.Sgt[len(j.Sgt)-1], "同花顺 hsgtApi(沪深股通)", nil
}

// ==================== 市场快讯 ====================

// NewsItem 一条财经快讯。
type NewsItem struct {
	Title   string `json:"title"`
	Summary string `json:"summary"`
	Date    string `json:"date"`
	WapURL  string `json:"wap_url"`
}

// FetchMarketNews 从东方财富快讯列表拉取最新财经资讯（尽力而为，失败返回 error）。
func FetchMarketNews(ctx context.Context, size int) ([]NewsItem, string, error) {
	if size <= 0 {
		size = 15
	}
	url := fmt.Sprintf("https://np-listapi.eastmoney.com/comm/web/getNewsByColumns?client=web&biz=web_news_col&column=BK0016&order=1&needInteractData=0&page_index=1&page_size=%d", size)
	body, err := httpGet(ctx, url, map[string]string{"Referer": "https://finance.eastmoney.com/"})
	if err != nil {
		return nil, "", fmt.Errorf("东方财富快讯获取失败: %w", err)
	}
	var j struct {
		Codes  string `json:"codes"`
		Result *struct {
			Data []struct {
				Title    string `json:"title"`
				Content  string `json:"content"`
				ShowTime string `json:"showTime"`
				Url      string `json:"url"`
				WapUrl   string `json:"wapUrl"`
			} `json:"data"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &j); err != nil {
		return nil, "", fmt.Errorf("东方财富快讯解析失败: %w", err)
	}
	if j.Result == nil || len(j.Result.Data) == 0 {
		return nil, "", fmt.Errorf("东方财富快讯返回空数据")
	}
	items := make([]NewsItem, 0, len(j.Result.Data))
	for _, d := range j.Result.Data {
		if d.Title == "" {
			continue
		}
		wap := d.WapUrl
		if wap == "" {
			wap = d.Url
		}
		items = append(items, NewsItem{Title: d.Title, Summary: truncate(d.Content, 120), Date: d.ShowTime, WapURL: wap})
	}
	if len(items) == 0 {
		return nil, "", fmt.Errorf("东方财富快讯无有效条目")
	}
	return items, "eastmoney(np-listapi) 财经快讯", nil
}

// ==================== 个股公告 ====================

// Announcement 一条个股公告。
type Announcement struct {
	Code  string `json:"code"`
	Name  string `json:"name"`
	Title string `json:"title"`
	Date  string `json:"date"`
	URL   string `json:"url"`
}

// FetchAnnouncements 从东方财富个股公告接口拉取指定股票（或全市场）最新公告。
func FetchAnnouncements(ctx context.Context, stockLookup string, size int) ([]Announcement, string, error) {
	if size <= 0 {
		size = 10
	}
	url := fmt.Sprintf("https://np-anotice-stock.eastmoney.com/api/security/ann?sr=-1&page_size=%d&page_index=1&ann_type=A&client_source=web", size)
	if stockLookup != "" {
		url += "&stock_list=" + stockLookup
	}
	body, err := httpGet(ctx, url, map[string]string{"Referer": "https://data.eastmoney.com/"})
	if err != nil {
		return nil, "", fmt.Errorf("东方财富公告获取失败: %w", err)
	}
	var j struct {
		Data struct {
			List []struct {
				ArtCode    string `json:"art_code"`
				Title      string `json:"title"`
				NoticeDate string `json:"notice_date"`
				StockCode  string `json:"stock_code"`
				StockName  string `json:"stock_name"`
			} `json:"list"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &j); err != nil {
		return nil, "", fmt.Errorf("东方财富公告解析失败: %w", err)
	}
	if len(j.Data.List) == 0 {
		return nil, "", fmt.Errorf("东方财富公告返回空数据")
	}
	items := make([]Announcement, 0, len(j.Data.List))
	for _, a := range j.Data.List {
		if a.Title == "" {
			continue
		}
		items = append(items, Announcement{
			Code:  a.StockCode,
			Name:  a.StockName,
			Title: a.Title,
			Date:  strings.Split(a.NoticeDate, " ")[0],
			URL:   "https://data.eastmoney.com/notices/detail/" + a.StockCode + "/" + a.ArtCode + ".html",
		})
	}
	return items, "eastmoney(np-anotice) 公告", nil
}

// ==================== 龙虎榜 ====================

// LHBDetail 龙虎榜一条上榜明细。
type LHBDetail struct {
	Code      string  `json:"code"`
	Name      string  `json:"name"`
	ChangePct float64 `json:"change_pct"`
	NetBuy    float64 `json:"net_buy"` // 净买入(万)
	BuyAmt    float64 `json:"buy_amt"`
	SellAmt   float64 `json:"sell_amt"`
}

// FetchLHB 从东方财富数据中心拉取指定日期的龙虎榜明细。
func FetchLHB(ctx context.Context, date string, size int) ([]LHBDetail, string, error) {
	if date == "" {
		date = time.Now().Format("2006-01-02")
	}
	if size <= 0 {
		size = 25
	}
	url := "https://datacenter-web.eastmoney.com/api/data/v1/get?reportName=RPT_DAILYBILLBOARD_DETAILSNEW&columns=ALL&filter=(TRADE_DATE%3D%3D%27" +
		date + "%27)&pageNumber=1&pageSize=" + strconv.Itoa(size) + "&sortTypes=-1&sortColumns=NET_BUY_AMT&source=DataCenter&client=WAP"
	body, err := httpGet(ctx, url, map[string]string{"Referer": "https://data.eastmoney.com/"})
	if err != nil {
		return nil, "", fmt.Errorf("东方财富龙虎榜获取失败: %w", err)
	}
	var j struct {
		Result *struct {
			Data []struct {
				SECURITY_CODE      string  `json:"SECURITY_CODE"`
				SECURITY_NAME      string  `json:"SECURITY_NAME"`
				CHANGE_RATE        float64 `json:"CHANGE_RATE"`
				NET_BUY_AMT        float64 `json:"NET_BUY_AMT"`
				BILLBOARD_BUY_AMT  float64 `json:"BILLBOARD_BUY_AMT"`
				BILLBOARD_SELL_AMT float64 `json:"BILLBOARD_SELL_AMT"`
			} `json:"data"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &j); err != nil {
		return nil, "", fmt.Errorf("东方财富龙虎榜解析失败: %w", err)
	}
	if j.Result == nil || len(j.Result.Data) == 0 {
		return nil, "", fmt.Errorf("东方财富龙虎榜 %s 返回空数据", date)
	}
	items := make([]LHBDetail, 0, len(j.Result.Data))
	for _, r := range j.Result.Data {
		items = append(items, LHBDetail{
			Code:      r.SECURITY_CODE,
			Name:      r.SECURITY_NAME,
			ChangePct: r.CHANGE_RATE,
			NetBuy:    r.NET_BUY_AMT / 1e4,
			BuyAmt:    r.BILLBOARD_BUY_AMT / 1e4,
			SellAmt:   r.BILLBOARD_SELL_AMT / 1e4,
		})
	}
	return items, "eastmoney(datacenter) 龙虎榜", nil
}

// ==================== 个股资金流向（东财 push2his） ====================

// FundFlowItem 单个交易日资金净流入快照（元）。
type FundFlowItem struct {
	Date       string  `json:"date"`
	Main       float64 `json:"main_net_inflow"`        // 主力净流入
	SuperLarge float64 `json:"super_large_net_inflow"` // 超大单净流入
	Large      float64 `json:"large_net_inflow"`       // 大单净流入
	Medium     float64 `json:"medium_net_inflow"`      // 中单净流入
	Small      float64 `json:"small_net_inflow"`       // 小单净流入
	MainPct    float64 `json:"main_net_inflow_pct"`    // 主力净占比%
}

// FetchEastMoneyFundFlow 个股资金流向（东方财富 push2his 日级资金流 K 线，公开接口）。
// 返回最近 days 个交易日资金净流入。非法代码/网络失败返回 error，绝不伪造。
func FetchEastMoneyFundFlow(ctx context.Context, symbol string, days int) (map[string]interface{}, string, error) {
	source := "eastmoney(push2his) 个股资金流向"
	code := extractDigits(symbol)
	if code == "" {
		return nil, source, fmt.Errorf("非法证券代码: %s", symbol)
	}
	mk := "0" // 0=深/北市场, 1=沪市场
	if strings.HasPrefix(code, "6") {
		mk = "1"
	}
	if days <= 0 {
		days = 5
	}
	url := fmt.Sprintf("https://push2his.eastmoney.com/api/qt/stock/fflow/daykline/get?lmt=0&klt=101&fields1=f1,f2,f3,f7&fields2=f51,f52,f53,f54,f55,f56,f57,f58,f59,f60,f61,f62,f63,f64,f65,f66,f67,f68,f69,f70,f71,f72,f73,f74,f75,f76&secid=%s.%s&ut=b2884a393a59ad64002292a3e90d46a5", mk, code)
	body, err := httpGet(ctx, url, map[string]string{"Referer": "https://quote.eastmoney.com/", "User-Agent": userAgent})
	if err != nil {
		return nil, source, fmt.Errorf("eastmoney 资金流请求失败: %w", err)
	}
	var j struct {
		Data struct {
			Name   string   `json:"name"`
			Klines []string `json:"klines"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &j); err != nil {
		return nil, source, fmt.Errorf("eastmoney 资金流JSON解析失败: %w", err)
	}
	if len(j.Data.Klines) == 0 {
		return nil, source, fmt.Errorf("eastmoney 资金流无数据")
	}
	start := len(j.Data.Klines) - days
	if start < 0 {
		start = 0
	}
	items := make([]FundFlowItem, 0, len(j.Data.Klines)-start)
	for i := start; i < len(j.Data.Klines); i++ {
		parts := strings.Split(j.Data.Klines[i], ",")
		if len(parts) < 7 {
			continue
		}
		// klines 顺序: 日期,主力,小单,中单,大单,超大单,主力占比,...
		items = append(items, FundFlowItem{
			Date:       parts[0],
			Main:       num(parts[1]),
			Small:      num(parts[2]),
			Medium:     num(parts[3]),
			Large:      num(parts[4]),
			SuperLarge: num(parts[5]),
			MainPct:    num(parts[6]),
		})
	}
	if len(items) == 0 {
		return nil, source, fmt.Errorf("eastmoney 资金流数据解析为空")
	}
	return map[string]interface{}{
		"name":        j.Data.Name,
		"code":        code,
		"unit":        "元",
		"recent_flow": items,
	}, source, nil
}

// ==================== 辅助 ====================

func round2(v float64) float64 { return float64(int64(v*100+0.5)) / 100 }

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func briefErr(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if len(msg) > 60 {
		return msg[:60] + "…"
	}
	return msg
}

func num(s string) float64 {
	f, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return f
}

func extractDigits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ==================== 两市成交额（量能，get_turnover / get_capital_flow 复用） ====================

// TurnoverPoint 单日两市成交额（亿元）。
type TurnoverPoint struct {
	Date   string  `json:"date"`
	Amount float64 `json:"amount"` // 亿元
}

// FetchCurrentTurnover 返回当前两市（沪市上证 + 深市深证成指）实时成交额（亿元）。
// 数据源：腾讯财经 qt.gtimg.cn（真实实时行情，non-trading 时段返回最近收盘/无数据）。
func FetchCurrentTurnover(ctx context.Context) (float64, error) {
	body, err := httpGet(ctx, "http://qt.gtimg.cn/q=sh000001,sz399001", nil)
	if err != nil {
		return 0, fmt.Errorf("腾讯实时成交额获取失败: %w", err)
	}
	txt := decodeGBK(body)
	re := regexp.MustCompile(`v_(sh000001|sz399001)="([^"]*)"`)
	totalYi := 0.0
	found := false
	for _, m := range re.FindAllStringSubmatch(txt, -1) {
		f := strings.Split(m[2], "~")
		if len(f) < 38 {
			continue
		}
		// 腾讯 qt.gtimg 报价第 38 个字段（索引 37）为成交额，单位万元。
		amtWan, err := strconv.ParseFloat(strings.TrimSpace(f[37]), 64)
		if err != nil || amtWan <= 0 {
			continue
		}
		totalYi += amtWan / 1e4 // 万元 -> 亿元
		found = true
	}
	if !found || totalYi <= 0 {
		return 0, fmt.Errorf("腾讯指数成交额解析为空")
	}
	return round2(totalYi), nil
}

// FetchTurnoverHistory 返回上证指数最近 days 个交易日的日成交额历史（亿元）。
// 数据源：雪球 xueqiu kline（含 amount 字段）。解析失败返回 error（上层降级为 unavailable）。
func FetchTurnoverHistory(ctx context.Context, days int) ([]TurnoverPoint, error) {
	if days <= 0 {
		days = 15
	}
	begin := time.Now().UnixMilli()
	url := fmt.Sprintf("https://stock.xueqiu.com/v5/stock/chart/kline.json?symbol=SH000001&begin=%d&period=day&type=before&count=-%d&indicator=kline,amount", begin, days)
	body, err := httpGet(ctx, url, map[string]string{
		"Referer": "https://xueqiu.com/",
		"Accept":  "application/json",
	})
	if err != nil {
		return nil, fmt.Errorf("雪球指数成交额获取失败: %w", err)
	}
	var j struct {
		Data *struct {
			Columns []string          `json:"columns"`
			Items   [][]interface{}   `json:"items"`
			Item    []json.RawMessage `json:"item"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &j); err != nil {
		return nil, fmt.Errorf("雪球指数成交额解析失败: %w", err)
	}
	rows := j.Data.Items
	if len(rows) == 0 {
		// 部分雪球版本返回单条 item 数组
		if len(j.Data.Item) > 0 {
			items := make([][]interface{}, 0, len(j.Data.Item))
			for _, raw := range j.Data.Item {
				var it []interface{}
				if json.Unmarshal(raw, &it) == nil {
					items = append(items, it)
				}
			}
			rows = items
		}
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("雪球指数成交额无数据")
	}

	// 定位 amount 与 timestamp 列索引
	amtIdx, tsIdx := -1, -1
	for i, c := range j.Data.Columns {
		switch c {
		case "amount":
			amtIdx = i
		case "timestamp":
			tsIdx = i
		}
	}
	if amtIdx < 0 || amtIdx >= len(rows[0]) {
		return nil, fmt.Errorf("雪球指数成交额缺少 amount 列")
	}

	pts := make([]TurnoverPoint, 0, len(rows))
	for _, r := range rows {
		amtV := 0.0
		if tsIdx < len(r) {
			amtV = toFloat(r[amtIdx])
		}
		if amtV <= 0 {
			continue
		}
		date := ""
		if tsIdx >= 0 && tsIdx < len(r) {
			date = time.UnixMilli(int64(toFloat(r[tsIdx]))).Format("2006-01-02")
		}
		pts = append(pts, TurnoverPoint{Date: date, Amount: round2(amtV / 1e8)}) // 元 -> 亿元
	}
	if len(pts) == 0 {
		return nil, fmt.Errorf("雪球指数成交额无有效数据")
	}
	// 升序返回
	for i := 0; i < len(pts)-1; i++ {
		for k := i + 1; k < len(pts); k++ {
			if pts[k].Date < pts[i].Date {
				pts[i], pts[k] = pts[k], pts[i]
			}
		}
	}
	return pts, nil
}

func toFloat(v interface{}) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case json.Number:
		f, _ := t.Float64()
		return f
	case string:
		f, _ := strconv.ParseFloat(strings.TrimSpace(t), 64)
		return f
	default:
		return 0
	}
}
