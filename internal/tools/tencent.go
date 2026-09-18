package tools

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/quantpilot/quantpilot/internal/tdx"
)

// 腾讯财经 K 线与日线数据获取，供 tencent 数据源在 agent 工具/回测等路径使用。

// isTencentMinutePeriod 判断是否为分钟周期（m1/m5/m15/m30/m60）
func isTencentMinutePeriod(period string) bool {
	p := strings.ToLower(period)
	if len(p) < 2 || p[0] != 'm' {
		return false
	}
	if _, err := strconv.Atoi(p[1:]); err == nil {
		return true
	}
	return false
}

// fetchTencentBars 从腾讯财经获取K线，返回统一的 bar map 列表（date/open/high/low/close/volume）
func fetchTencentBars(code, period string, count int, adjust string) ([]map[string]interface{}, error) {
	period = strings.ToLower(period)
	if period == "" {
		period = "day"
	}
	code = strings.ToLower(code)

	var url, key string
	if isTencentMinutePeriod(period) {
		// 分钟K线
		url = fmt.Sprintf("https://web.ifzq.gtimg.cn/appstock/app/kline/mkline?param=%s,%s,,%d", code, period, count)
		key = period
	} else {
		// 日/周/月K线
		adj := ""
		if adjust == "qfq" || adjust == "1" || adjust == "" {
			adj = "qfq"
		}
		url = fmt.Sprintf("https://web.ifzq.gtimg.cn/appstock/app/fqkline/get?param=%s,%s,,,%d,%s", code, period, count, adj)
		key = adj + period
		if adj == "" {
			key = period
		}
	}
	return parseTencentBars(url, code, key)
}

// parseTencentBars 解析腾讯 K 线 JSON 响应
func parseTencentBars(url, sym, key string) ([]map[string]interface{}, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 QuantBot")
	req.Header.Set("Referer", "https://gu.qq.com/")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tencent kline status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var payload struct {
		Code int             `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	if payload.Code != 0 {
		return nil, fmt.Errorf("tencent kline code %d", payload.Code)
	}

	var dataMap map[string]json.RawMessage
	if err := json.Unmarshal(payload.Data, &dataMap); err != nil {
		return nil, err
	}
	stockRaw, ok := dataMap[sym]
	if !ok {
		return nil, fmt.Errorf("tencent kline no data for %s", sym)
	}
	var stock map[string]json.RawMessage
	if err := json.Unmarshal(stockRaw, &stock); err != nil {
		return nil, err
	}
	barsRaw, ok := stock[key]
	if !ok {
		// 回退：有些调整参数下 key 不同，尝试 day/week/month 原key
		alt := strings.TrimPrefix(key, "qfq")
		barsRaw, ok = stock[alt]
		if !ok {
			return nil, fmt.Errorf("tencent kline key %s missing for %s", key, sym)
		}
	}
	var rows []json.RawMessage
	if err := json.Unmarshal(barsRaw, &rows); err != nil {
		return nil, err
	}

	out := make([]map[string]interface{}, 0, len(rows))
	for _, r := range rows {
		var vals []float64
		if err := json.Unmarshal(r, &vals); err != nil {
			// 兼容数字带引号等情况下逐字段解析
			var rawVals []interface{}
			_ = json.Unmarshal(r, &rawVals)
			if len(rawVals) < 6 {
				continue
			}
			dateStr, _ := rawVals[0].(string)
			f := func(v interface{}) float64 {
				switch x := v.(type) {
				case float64:
					return x
				case string:
					fv, _ := strconv.ParseFloat(x, 64)
					return fv
				}
				return 0
			}
			out = append(out, map[string]interface{}{
				"date":   dateStr,
				"open":   f(rawVals[1]),
				"high":   f(rawVals[3]),
				"low":    f(rawVals[4]),
				"close":  f(rawVals[2]),
				"volume": f(rawVals[5]),
				"amount": 0.0,
			})
			continue
		}
		if len(vals) < 6 {
			continue
		}
		out = append(out, map[string]interface{}{
			"date":   fmt.Sprintf("%.0f", vals[0]),
			"open":   vals[1],
			"high":   vals[3],
			"low":    vals[4],
			"close":  vals[2],
			"volume": vals[5],
			"amount": 0.0,
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("tencent kline empty for %s", sym)
	}
	return out, nil
}

// tencentKlineMap 腾讯 K 线（GetKline 的 tencent 分支；包级共享，供腾讯 provider 复用）
func tencentKlineMap(exchange, code, period string, count int, adjust string) (map[string]interface{}, error) {
	if count <= 0 {
		count = 300
	}
	bars, err := fetchTencentBars(exchange+code, period, count, adjust)
	if err != nil {
		return nil, err
	}
	klines := make([]interface{}, len(bars))
	for i, b := range bars {
		klines[i] = b
	}
	return map[string]interface{}{
		"exchange": exchange,
		"code":     code,
		"period":   period,
		"count":    len(bars),
		"adjust":   adjust,
		"klines":   klines,
		"source":   "tencent",
	}, nil
}

// tencentStockDataMap 腾讯日线（GetStockData 的 tencent 分支，含技术指标；包级共享）
func tencentStockDataMap(exchange, code string, days int) (map[string]interface{}, error) {
	if days <= 0 {
		days = 120
	}
	bars, err := fetchTencentBars(exchange+code, "day", days, "qfq")
	if err != nil {
		return nil, err
	}

	dataPoints := make([]map[string]interface{}, len(bars))
	tdBars := make([]tdx.DayBar, 0, len(bars))
	for i, b := range bars {
		dateStr, _ := b["date"].(string)
		dataPoints[i] = b
		tdBars = append(tdBars, tdx.DayBar{
			Date:   parseDateToInt(dateStr),
			Open:   b["open"].(float64),
			High:   b["high"].(float64),
			Low:    b["low"].(float64),
			Close:  b["close"].(float64),
			Amount: b["amount"].(float64),
			Volume: int64(b["volume"].(float64)),
		})
	}

	result := map[string]interface{}{
		"exchange": exchange,
		"code":     code,
		"days":     len(bars),
		"data":     dataPoints,
		"source":   "tencent",
	}
	if len(tdBars) >= 20 {
		if indicators := tdx.CalculateIndicators(tdBars); indicators != nil {
			result["indicators"] = map[string]interface{}{
				"ma5":           indicators.MA5,
				"ma10":          indicators.MA10,
				"ma20":          indicators.MA20,
				"ma60":          indicators.MA60,
				"rsi_14":        indicators.RSI14,
				"macd_dif":      indicators.MACD_DIF,
				"macd_dea":      indicators.MACD_DEA,
				"macd":          indicators.MACD,
				"volatility_20": indicators.Volatility20,
				"change_pct":    indicators.ChangePct,
				"change_5d":     indicators.Change5d,
				"change_20d":    indicators.Change20d,
				"high_20":       indicators.High20,
				"low_20":        indicators.Low20,
				"volume_ratio":  indicators.VolumeRatio,
			}
		}
	}
	return result, nil
}

// tencentQuoteMap 腾讯单只实时报价（GetRealTimeQuote 的 tencent 分支；包级共享，供腾讯 provider 复用）
func tencentQuoteMap(exchange, code string) (map[string]interface{}, error) {
	q, err := NewTencentMarketDataProvider().GetQuote(exchange + code)
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{
		"price":       q.Price,
		"open":        q.Open,
		"high":        q.High,
		"low":         q.Low,
		"volume":      q.Volume,
		"amount":      q.Amount,
		"last_close":  q.PrevClose,
		"change_pct":  q.ChangePct,
		"name":        q.Name,
		"is_realtime": true,
		"source":      "tencent",
	}, nil
}
