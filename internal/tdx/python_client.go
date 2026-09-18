package tdx

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// PythonTDXClient 通过 HTTP 调用 Python TDX 服务获取实时数据
type PythonTDXClient struct {
	baseURL    string
	httpClient *http.Client
}

// NewPythonTDXClient 创建 Python TDX 客户端
func NewPythonTDXClient(baseURL string) *PythonTDXClient {
	return &PythonTDXClient{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// Health 检查服务健康状态
func (c *PythonTDXClient) Health() (map[string]interface{}, error) {
	return c.get("/health")
}

// GetQuote 获取单只股票实时快照
func (c *PythonTDXClient) GetQuote(market, code string) (map[string]interface{}, error) {
	url := fmt.Sprintf("/api/quote/%s%s", market, code)
	result, err := c.get(url)
	if err != nil {
		return nil, err
	}
	if data, ok := result["data"].(map[string]interface{}); ok {
		return data, nil
	}
	return result, nil
}

// GetQuotes 批量获取实时快照
func (c *PythonTDXClient) GetQuotes(codes []string) ([]map[string]interface{}, error) {
	url := fmt.Sprintf("/api/quotes?codes=%s", joinCodes(codes))
	result, err := c.get(url)
	if err != nil {
		return nil, err
	}
	if data, ok := result["data"].([]interface{}); ok {
		results := make([]map[string]interface{}, len(data))
		for i, d := range data {
			if m, ok := d.(map[string]interface{}); ok {
				results[i] = m
			}
		}
		return results, nil
	}
	return nil, fmt.Errorf("unexpected response format")
}

// GetKline 获取K线数据
func (c *PythonTDXClient) GetKline(market, code, period string, count int, adjust string) (map[string]interface{}, error) {
	url := fmt.Sprintf("/api/kline?market=%s&code=%s&period=%s&count=%d&adjust=%s",
		market, code, period, count, adjust)
	result, err := c.get(url)
	if err != nil {
		return nil, err
	}
	if data, ok := result["data"].(map[string]interface{}); ok {
		return data, nil
	}
	return result, nil
}

// GetTrades 获取逐笔成交
func (c *PythonTDXClient) GetTrades(market, code string) (map[string]interface{}, error) {
	url := fmt.Sprintf("/api/trades/%s%s", market, code)
	result, err := c.get(url)
	if err != nil {
		return nil, err
	}
	if data, ok := result["data"].(map[string]interface{}); ok {
		return data, nil
	}
	return result, nil
}

// GetF10 获取F10资讯
func (c *PythonTDXClient) GetF10(market, code string) (map[string]interface{}, error) {
	url := fmt.Sprintf("/api/f10/%s%s", market, code)
	result, err := c.get(url)
	if err != nil {
		return nil, err
	}
	if data, ok := result["data"].(map[string]interface{}); ok {
		return data, nil
	}
	return result, nil
}

// SearchStocks 搜索股票
func (c *PythonTDXClient) SearchStocks(keyword, market string) ([]map[string]interface{}, error) {
	url := fmt.Sprintf("/api/search?keyword=%s&market=%s", keyword, market)
	result, err := c.get(url)
	if err != nil {
		return nil, err
	}
	if data, ok := result["data"].([]interface{}); ok {
		results := make([]map[string]interface{}, len(data))
		for i, d := range data {
			if m, ok := d.(map[string]interface{}); ok {
				results[i] = m
			}
		}
		return results, nil
	}
	return nil, fmt.Errorf("unexpected response format")
}

// GetBoardList 获取板块列表
func (c *PythonTDXClient) GetBoardList(boardType string) ([]map[string]interface{}, error) {
	url := fmt.Sprintf("/api/board_list?type=%s", boardType)
	result, err := c.get(url)
	if err != nil {
		return nil, err
	}
	if data, ok := result["data"].([]interface{}); ok {
		results := make([]map[string]interface{}, len(data))
		for i, d := range data {
			if m, ok := d.(map[string]interface{}); ok {
				results[i] = m
			}
		}
		return results, nil
	}
	return nil, fmt.Errorf("unexpected response format")
}

// GetBoardSummary 获取板块行情汇总
func (c *PythonTDXClient) GetBoardSummary(boardCode string, includeMembers bool) (map[string]interface{}, error) {
	url := fmt.Sprintf("/api/board_summary?code=%s&members=%t", boardCode, includeMembers)
	result, err := c.get(url)
	if err != nil {
		return nil, err
	}
	if data, ok := result["data"].(map[string]interface{}); ok {
		return data, nil
	}
	return result, nil
}

// GetFundFlow 获取资金流向
func (c *PythonTDXClient) GetFundFlow(market, code string) ([]map[string]interface{}, error) {
	url := fmt.Sprintf("/api/fund_flow?market=%s&code=%s", market, code)
	result, err := c.get(url)
	if err != nil {
		return nil, err
	}
	if data, ok := result["data"].([]interface{}); ok {
		results := make([]map[string]interface{}, len(data))
		for i, d := range data {
			if m, ok := d.(map[string]interface{}); ok {
				results[i] = m
			}
		}
		return results, nil
	}
	return nil, fmt.Errorf("unexpected response format")
}

// GetCapitalFlow 获取实时资金流向
func (c *PythonTDXClient) GetCapitalFlow(market, code string) (map[string]interface{}, error) {
	url := fmt.Sprintf("/api/capital_flow/%s%s", market, code)
	result, err := c.get(url)
	if err != nil {
		return nil, err
	}
	if data, ok := result["data"].(map[string]interface{}); ok {
		return data, nil
	}
	return result, nil
}

// GetAnnouncement 获取公告
func (c *PythonTDXClient) GetAnnouncement(code string, count int) ([]map[string]interface{}, error) {
	url := fmt.Sprintf("/api/announcement?code=%s&count=%d", code, count)
	result, err := c.get(url)
	if err != nil {
		return nil, err
	}
	if data, ok := result["data"].([]interface{}); ok {
		results := make([]map[string]interface{}, len(data))
		for i, d := range data {
			if m, ok := d.(map[string]interface{}); ok {
				results[i] = m
			}
		}
		return results, nil
	}
	return nil, fmt.Errorf("unexpected response format")
}

// GetMarketStat 获取市场统计
func (c *PythonTDXClient) GetMarketStat() (map[string]interface{}, error) {
	return c.get("/api/market_stat")
}

// GetIndicator 计算技术指标
func (c *PythonTDXClient) GetIndicator(market, code, indicator, period string, count int) (map[string]interface{}, error) {
	url := fmt.Sprintf("/api/indicator?market=%s&code=%s&name=%s&period=%s&count=%d",
		market, code, indicator, period, count)
	return c.get(url)
}

// GetMinuteBars 获取分时数据
func (c *PythonTDXClient) GetMinuteBars(market, code string, count int) ([]map[string]interface{}, error) {
	url := fmt.Sprintf("/api/minute_bars?market=%s&code=%s&count=%d", market, code, count)
	result, err := c.get(url)
	if err != nil {
		return nil, err
	}
	if data, ok := result["data"].([]interface{}); ok {
		results := make([]map[string]interface{}, len(data))
		for i, d := range data {
			if m, ok := d.(map[string]interface{}); ok {
				results[i] = m
			}
		}
		return results, nil
	}
	return nil, fmt.Errorf("unexpected response format")
}

// get 发送 GET 请求
func (c *PythonTDXClient) get(path string) (map[string]interface{}, error) {
	url := c.baseURL + path

	resp, err := c.httpClient.Get(url)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response failed: %w", err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parse response failed: %w", err)
	}

	if status, ok := result["status"].(string); ok && status == "error" {
		msg, _ := result["message"].(string)
		return nil, fmt.Errorf("API error: %s", msg)
	}

	return result, nil
}

// post 发送 POST 请求
func (c *PythonTDXClient) post(path string, payload interface{}) (map[string]interface{}, error) {
	url := c.baseURL + path

	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal payload failed: %w", err)
	}

	resp, err := c.httpClient.Post(url, "application/json", bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response failed: %w", err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parse response failed: %w", err)
	}

	return result, nil
}

func joinCodes(codes []string) string {
	result := ""
	for i, c := range codes {
		if i > 0 {
			result += ","
		}
		result += c
	}
	return result
}
