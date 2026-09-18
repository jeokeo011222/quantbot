package tdx

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// MCPClient MCP 协议客户端
type MCPClient struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
	mu         sync.Mutex
	sessionID  string
	requestID  int
}

// NewMCPClient 创建 MCP 客户端
func NewMCPClient(baseURL, apiKey string) *MCPClient {
	return &MCPClient{
		baseURL: baseURL,
		apiKey:  apiKey,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// ListTools 获取可用工具列表
func (c *MCPClient) ListTools() ([]MCPTool, error) {
	result, err := c.call("tools/list", nil)
	if err != nil {
		return nil, err
	}

	toolsArray, ok := result["tools"].([]interface{})
	if !ok {
		return nil, fmt.Errorf("unexpected response format: missing tools")
	}

	var tools []MCPTool
	for _, t := range toolsArray {
		if toolMap, ok := t.(map[string]interface{}); ok {
			tool := MCPTool{
				Name:        getString(toolMap, "name"),
				Description: getString(toolMap, "description"),
			}
			if schema, ok := toolMap["inputSchema"].(map[string]interface{}); ok {
				tool.Schema = schema
			}
			tools = append(tools, tool)
		}
	}

	return tools, nil
}

// CallTool 调用 MCP 工具
func (c *MCPClient) CallTool(name string, arguments map[string]interface{}) (map[string]interface{}, error) {
	params := map[string]interface{}{
		"name":      name,
		"arguments": arguments,
	}

	result, err := c.call("tools/call", params)
	if err != nil {
		return nil, err
	}

	// 解析 content 字段
	if content, ok := result["content"].([]interface{}); ok {
		var textParts []string
		for _, item := range content {
			if itemMap, ok := item.(map[string]interface{}); ok {
				textParts = append(textParts, getString(itemMap, "text"))
			}
		}
		return map[string]interface{}{
			"tool":    name,
			"content": textParts,
			"raw":     result,
		}, nil
	}

	return result, nil
}

// GetTDXQuote 通过 MCP 获取实时快照
func (c *MCPClient) GetTDXQuote(market, code string) (map[string]interface{}, error) {
	return c.CallTool("tdx_get_quote", map[string]interface{}{
		"market": market,
		"code":   code,
	})
}

// GetTDXKline 通过 MCP 获取K线
func (c *MCPClient) GetTDXKline(market, code, period string, count int, adjust string) (map[string]interface{}, error) {
	return c.CallTool("tdx_get_kline", map[string]interface{}{
		"market": market,
		"code":   code,
		"period": period,
		"count":  count,
		"adjust": adjust,
	})
}

// GetTDXTrades 通过 MCP 获取逐笔成交
func (c *MCPClient) GetTDXTrades(market, code string) (map[string]interface{}, error) {
	return c.CallTool("tdx_get_trades", map[string]interface{}{
		"market": market,
		"code":   code,
	})
}

// GetTDXF10 通过 MCP 获取F10
func (c *MCPClient) GetTDXF10(market, code string) (map[string]interface{}, error) {
	return c.CallTool("tdx_get_f10", map[string]interface{}{
		"market": market,
		"code":   code,
	})
}

// call 发送 JSON-RPC 请求
func (c *MCPClient) call(method string, params interface{}) (map[string]interface{}, error) {
	c.mu.Lock()
	c.requestID++
	reqID := c.requestID
	c.mu.Unlock()

	reqBody := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      reqID,
		"method":  method,
	}
	if params != nil {
		reqBody["params"] = params
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request failed: %w", err)
	}

	req, err := http.NewRequest("POST", c.baseURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request failed: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	if c.sessionID != "" {
		req.Header.Set("Mcp-Session-Id", c.sessionID)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response failed: %w", err)
	}

	var respJSON map[string]interface{}
	if err := json.Unmarshal(respBody, &respJSON); err != nil {
		return nil, fmt.Errorf("parse response failed: %w", err)
	}

	// 检查错误响应
	if error, ok := respJSON["error"].(map[string]interface{}); ok {
		code, _ := error["code"].(float64)
		msg, _ := error["message"].(string)
		return nil, fmt.Errorf("MCP error %d: %s", int(code), msg)
	}

	// 保存 session ID
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		c.sessionID = sid
	}

	if result, ok := respJSON["result"].(map[string]interface{}); ok {
		return result, nil
	}

	return respJSON, nil
}

// MCPTool MCP 工具定义
type MCPTool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Schema      map[string]interface{} `json:"schema,omitempty"`
}

func getString(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}
