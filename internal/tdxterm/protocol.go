package tdxterm

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// marketNumBySuffix 市场后缀到通达信编号的映射（0=深圳, 1=上海, 2=北京）。
var marketNumBySuffix = map[string]int{
	"SZ": 0,
	"SH": 1,
	"BJ": 2,
}

// convertTimeFormat 将 "YYYYMMDD" 或 "YYYYMMDDHHMMSS" 转为 "YYYY-MM-DD HH:MM:SS"。
// 空串或非法输入返回空串（等价于不传时间参数）。
func convertTimeFormat(t string) string {
	s := strings.TrimSpace(t)
	if s == "" {
		return ""
	}
	var layout, out string
	switch len(s) {
	case 8:
		layout, out = "20060102", "2006-01-02"
	case 14:
		layout, out = "20060102150405", "2006-01-02 15:04:05"
	default:
		return ""
	}
	dt, err := time.Parse(layout, s)
	if err != nil {
		return ""
	}
	return dt.Format(out)
}

// normalizeCode 将形如 "600000.SH" / "000001.sz" / "600000" 的代码转为通达信 "1#600000"。
// 已含 "#" 的原始代码原样返回。无后缀时依据代码前缀推断市场（沪市 5/6/7/9 开头，其余深市）。
func normalizeCode(code string) string {
	c := strings.TrimSpace(code)
	if c == "" {
		return c
	}
	if strings.Contains(c, "#") {
		if _, err := strconv.Atoi(strings.SplitN(c, "#", 2)[0]); err == nil {
			return c
		}
	}
	upper := strings.ToUpper(c)
	mkt := 0 // 默认深市
	if i := strings.Index(upper, "."); i >= 0 {
		c = upper[:i]
		if n, ok := marketNumBySuffix[upper[i+1:]]; ok {
			mkt = n
		}
	} else {
		switch {
		case strings.HasPrefix(c, "5"), strings.HasPrefix(c, "6"),
			strings.HasPrefix(c, "7"), strings.HasPrefix(c, "9"):
			mkt = 1 // 沪市：A股6、基金5、B股9、可转债/配股7
		default:
			mkt = 0
		}
	}
	return fmt.Sprintf("%d#%s", mkt, c)
}

// normalizeCodeList 批量归一化证券代码。
func normalizeCodeList(codes []string) []string {
	out := make([]string, 0, len(codes))
	for _, c := range codes {
		out = append(out, normalizeCode(c))
	}
	return out
}

// toInt 将 JSON 数字（float64）或字符串安全转为 int。
func toInt(v any, def int) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case string:
		if i, err := strconv.Atoi(n); err == nil {
			return i
		}
	}
	return def
}

// toBool 将 JSON 布尔/数字安全转为 bool。
func toBool(v any) bool {
	switch b := v.(type) {
	case bool:
		return b
	case float64:
		return b != 0
	case string:
		return strings.EqualFold(b, "true") || b == "1"
	}
	return false
}

// toString 将 JSON 值安全转为字符串。
func toString(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case nil:
		return ""
	default:
		if b, err := json.Marshal(v); err == nil {
			return string(b)
		}
		return fmt.Sprintf("%v", v)
	}
}

// cloneMap 深拷贝 map 的顶层键值（值为共享引用，仅浅拷贝）。
func cloneMap(src map[string]any) map[string]any {
	dst := make(map[string]any, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

// callTPythPaged 调用 RPC 接口并自动拼接 TPythPaged 分页返回。
// 若返回中不含 TPythPaged 字段，则原样返回首包。
func (c *Client) callTPythPaged(proc *syscall.Proc, payload map[string]any, timeoutMs int) (map[string]any, error) {
	first, err := c.call(proc, payload, timeoutMs)
	if err != nil {
		return nil, err
	}
	if _, ok := first["TPythPaged"]; !ok {
		return first, nil
	}

	token := toString(first["page_token"])
	totalPages := toInt(first["total_pages"], 1)
	if totalPages <= 1 {
		return first, nil
	}

	chunks := []string{toString(first["page_data"])}
	for i := 1; i < totalPages; i++ {
		pageObj, err := c.call(proc, map[string]any{
			"_tpyth_page_token": token,
			"_tpyth_page_index": i,
		}, timeoutMs)
		if err != nil {
			return nil, err
		}
		if _, ok := pageObj["TPythPaged"]; !ok {
			return nil, fmt.Errorf("分页数据格式错误")
		}
		chunks = append(chunks, toString(pageObj["page_data"]))
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(strings.Join(chunks, "")), &result); err != nil {
		return nil, fmt.Errorf("合并分页数据解析失败: %v", err)
	}
	return result, nil
}

// callKlineAllPages 调用 K 线接口并按股票游标自动拼接（KlinePaged）。
func (c *Client) callKlineAllPages(proc *syscall.Proc, payload map[string]any, timeoutMs int) (map[string]any, error) {
	firstReq := cloneMap(payload)
	firstReq["stock_page_index"] = 0
	if toInt(firstReq["stock_page_size"], 0) <= 0 {
		firstReq["stock_page_size"] = 100
	}

	first, err := c.callTPythPaged(proc, firstReq, timeoutMs)
	if err != nil {
		return nil, err
	}
	if _, ok := first["KlinePaged"]; !ok {
		return first, nil
	}

	result := cloneMap(first)
	result["Value"] = map[string]any{}
	delete(result, "KlineTotal")
	errors := map[string]any{}

	pageObj := first
	curIdx := toInt(firstReq["stock_page_index"], 0)
	nextIdx := 0
	for {
		if value, ok := pageObj["Value"].(map[string]any); ok {
			for k, v := range value {
				result["Value"].(map[string]any)[k] = v
			}
		}
		if errs, ok := pageObj["Errors"].(map[string]any); ok {
			for k, v := range errs {
				errors[k] = v
			}
		}

		hasMore := toBool(pageObj["has_more"])
		nextIdx = toInt(pageObj["next_stock_page_index"], 0)
		if !hasMore {
			break
		}
		if nextIdx <= curIdx {
			return nil, fmt.Errorf("K线分页游标未前进（TPyth 与协议版本是否一致？）")
		}

		req := cloneMap(payload)
		req["stock_page_index"] = nextIdx
		req["stock_page_size"] = firstReq["stock_page_size"]
		curIdx = nextIdx
		pageObj, err = c.callTPythPaged(proc, req, timeoutMs)
		if err != nil {
			return nil, err
		}
	}

	if len(errors) > 0 {
		result["Errors"] = errors
	}
	result["has_more"] = false
	result["next_stock_page_index"] = nextIdx
	return result, nil
}

// callProDataAllPages 调用专业数据接口并按股票页自动拼接（ProDataPaged）。
func (c *Client) callProDataAllPages(proc *syscall.Proc, payload map[string]any, timeoutMs int) (map[string]any, error) {
	firstReq := cloneMap(payload)
	firstReq["stock_page_index"] = 0

	first, err := c.callTPythPaged(proc, firstReq, timeoutMs)
	if err != nil {
		return nil, err
	}
	if _, ok := first["ProDataPaged"]; !ok {
		return first, nil
	}

	result := cloneMap(first)
	result["Value"] = map[string]any{}
	errors := map[string]any{}
	stockPages := toInt(first["stock_total_pages"], 1)

	for i := 0; i < stockPages; i++ {
		var pageObj map[string]any
		if i == 0 {
			pageObj = first
		} else {
			req := cloneMap(payload)
			req["stock_page_index"] = i
			pageObj, err = c.callTPythPaged(proc, req, timeoutMs)
			if err != nil {
				return nil, err
			}
		}
		if value, ok := pageObj["Value"].(map[string]any); ok {
			for k, v := range value {
				result["Value"].(map[string]any)[k] = v
			}
		}
		if errs, ok := pageObj["Errors"].(map[string]any); ok {
			for k, v := range errs {
				errors[k] = v
			}
		}
	}

	if len(errors) > 0 {
		result["Errors"] = errors
	}
	return result, nil
}
