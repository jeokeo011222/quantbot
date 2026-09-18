package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/quantpilot/quantpilot/internal/brain/llm"
)

// Client MCP 客户端
type Client struct {
	serverURL  string
	httpClient *http.Client
	requestID  int
}

// NewClient 创建 MCP 客户端
func NewClient(serverURL string) *Client {
	return &Client{
		serverURL: serverURL + "/mcp/v1",
		httpClient: &http.Client{
			Timeout: 300 * time.Second,
		},
	}
}

// Chat 通过 MCP 调用 LLM 聊天
func (c *Client) Chat(ctx context.Context, messages []llm.Message, tools []llm.Tool) (*llm.ChatResult, error) {
	reqID := c.nextRequestID()

	// 将 llm.Message 转换为通用格式的 []interface{}
	messagesInterface := make([]interface{}, len(messages))
	for i, msg := range messages {
		msgMap := map[string]interface{}{
			"role":    msg.Role,
			"content": msg.Content,
		}
		if msg.ReasoningContent != "" {
			msgMap["reasoning_content"] = msg.ReasoningContent
		}
		if msg.ToolCallID != "" {
			msgMap["tool_call_id"] = msg.ToolCallID
		}
		// 转换 ToolCalls
		if len(msg.ToolCalls) > 0 {
			toolCalls := make([]interface{}, len(msg.ToolCalls))
			for j, tc := range msg.ToolCalls {
				toolCalls[j] = map[string]interface{}{
					"id":   tc.ID,
					"type": tc.Type,
					"function": map[string]interface{}{
						"name":      tc.Function.Name,
						"arguments": tc.Function.Arguments,
					},
				}
			}
			msgMap["tool_calls"] = toolCalls
		}
		messagesInterface[i] = msgMap
	}

	// 构建工具参数
	args := map[string]interface{}{
		"messages": messagesInterface,
	}

	if len(tools) > 0 {
		toolsJSON := make([]interface{}, len(tools))
		for i, t := range tools {
			toolsJSON[i] = map[string]interface{}{
				"type": "function",
				"function": map[string]interface{}{
					"name":        t.Function.Name,
					"description": t.Function.Description,
					"parameters":  t.Function.Parameters,
				},
			}
		}
		args["tools"] = toolsJSON
	}

	// 序列化 args 为 JSON 字符串用于日志
	argsJSON, _ := json.Marshal(args)
	log.Printf("[MCP Client] Sending llm_chat request: messages=%d, tools=%d, args_size=%d bytes",
		len(messages), len(tools), len(argsJSON))

	result, err := c.callTool(ctx, "llm_chat", args, reqID)
	if err != nil {
		log.Printf("[MCP Client] llm_chat failed: %v", err)
		return nil, fmt.Errorf("MCP llm_chat failed: %w", err)
	}

	// 解析结果
	content, ok := result["content"].([]interface{})
	if !ok || len(content) == 0 {
		return nil, fmt.Errorf("MCP llm_chat returned empty content")
	}

	// 从 MCP 响应中提取 LLM 原始响应文本
	textContent := ""
	if textBlock, ok := content[0].(map[string]interface{}); ok {
		textContent, _ = textBlock["text"].(string)
	}

	if textContent == "" {
		return nil, fmt.Errorf("MCP llm_chat returned empty text content")
	}

	log.Printf("[MCP Client] Received LLM response (length: %d)", len(textContent))

	// 解析 LLM 原始 JSON 响应
	var chatResult llm.ChatResult
	if err := json.Unmarshal([]byte(textContent), &chatResult); err != nil {
		log.Printf("[MCP Client] Failed to parse LLM response as JSON: %v, using plain text", err)
		// 如果解析失败，构造一个简单的结果
		return &llm.ChatResult{
			Choices: []struct {
				Index   int `json:"index"`
				Message struct {
					Content          string             `json:"content"`
					Role             string             `json:"role"`
					ReasoningContent string             `json:"reasoning_content,omitempty"`
					ToolCalls        []llm.ToolCallInfo `json:"tool_calls,omitempty"`
				} `json:"message"`
				FinishReason string `json:"finish_reason"`
			}{
				{
					Index: 0,
					Message: struct {
						Content          string             `json:"content"`
						Role             string             `json:"role"`
						ReasoningContent string             `json:"reasoning_content,omitempty"`
						ToolCalls        []llm.ToolCallInfo `json:"tool_calls,omitempty"`
					}{
						Content: textContent,
						Role:    "assistant",
					},
					FinishReason: "stop",
				},
			},
			Model: "",
		}, nil
	}

	// 记录解析结果
	if len(chatResult.Choices) > 0 {
		msg := chatResult.Choices[0].Message
		log.Printf("[MCP Client] Parsed response: content_len=%d, tool_calls=%d, reasoning_content=%v",
			len(msg.Content), len(msg.ToolCalls), msg.ReasoningContent != "")
		for i, tc := range msg.ToolCalls {
			log.Printf("[MCP Client] Tool call %d: name=%s, args_len=%d", i, tc.Function.Name, len(tc.Function.Arguments))
		}
	}

	return &chatResult, nil
}

// StreamChat 通过 MCP 调用 LLM 流式聊天（暂不支持，使用普通调用）
func (c *Client) StreamChat(ctx context.Context, messages []llm.Message, tools []llm.Tool) (*llm.StreamReader, error) {
	// MCP 协议暂不支持流式调用，回退到普通调用
	result, err := c.Chat(ctx, messages, tools)
	if err != nil {
		return nil, err
	}

	// 构造一个简单的流式响应
	resultBytes, _ := json.Marshal(result)
	return llm.NewStreamReader(io.NopCloser(bytes.NewReader(resultBytes))), nil
}

// Embedding 通过 MCP 调用 LLM embedding
func (c *Client) Embedding(ctx context.Context, text string) ([]float64, error) {
	reqID := c.nextRequestID()

	args := map[string]interface{}{
		"text": text,
	}

	result, err := c.callTool(ctx, "llm_embedding", args, reqID)
	if err != nil {
		return nil, fmt.Errorf("MCP llm_embedding failed: %w", err)
	}

	// 解析结果
	content, ok := result["content"].([]interface{})
	if !ok || len(content) == 0 {
		return nil, fmt.Errorf("MCP llm_embedding returned empty content")
	}

	textContent := ""
	if textBlock, ok := content[0].(map[string]interface{}); ok {
		textContent, _ = textBlock["text"].(string)
	}

	// 解析 embedding 响应
	var embedResp struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}

	if err := json.Unmarshal([]byte(textContent), &embedResp); err != nil {
		return nil, fmt.Errorf("Parse embedding response error: %v", err)
	}

	if len(embedResp.Data) == 0 {
		return nil, fmt.Errorf("No embedding returned")
	}

	return embedResp.Data[0].Embedding, nil
}

// HasValidAPIKey 检查 MCP 服务是否可用
// 注意：这个方法检查 MCP 服务器的健康状态，而不是直接检查 API key
// 如果无法连接到服务器，默认返回 true（假设服务器是可用的）
func (c *Client) HasValidAPIKey() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	healthURL := strings.Replace(c.serverURL, "/mcp/v1", "/health", 1)
	req, err := http.NewRequestWithContext(ctx, "GET", healthURL, nil)
	if err != nil {
		log.Printf("[MCP Client] HasValidAPIKey: failed to create request: %v", err)
		return true // 默认返回 true
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		// 无法连接到服务器，可能是服务器未启动
		log.Printf("[MCP Client] HasValidAPIKey: cannot connect to server: %v", err)
		return true // 假设服务器可用
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("[MCP Client] HasValidAPIKey: health check failed with status %d", resp.StatusCode)
		return true // 默认返回 true
	}

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		log.Printf("[MCP Client] HasValidAPIKey: failed to decode response: %v", err)
		return true // 默认返回 true
	}

	configured, ok := result["api_key_configured"].(bool)
	if !ok {
		log.Printf("[MCP Client] HasValidAPIKey: api_key_configured field not found or invalid")
		return true // 默认返回 true
	}

	log.Printf("[MCP Client] HasValidAPIKey: api_key_configured=%v", configured)
	return configured
}

// callTool 调用 MCP 工具
func (c *Client) callTool(ctx context.Context, toolName string, args map[string]interface{}, reqID int) (map[string]interface{}, error) {
	reqBody := MCPRequest{
		JSONRPC: "2.0",
		Method:  "tools/call",
		Params: map[string]interface{}{
			"name":      toolName,
			"arguments": args,
		},
		ID: reqID,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("Marshal request error: %w", err)
	}

	log.Printf("[MCP Client] Calling tool %s: url=%s, body_size=%d bytes", toolName, c.serverURL, len(bodyBytes))

	req, err := http.NewRequestWithContext(ctx, "POST", c.serverURL, bytes.NewReader(bodyBytes))
	if err != nil {
		log.Printf("[MCP Client] Failed to create request: %v", err)
		return nil, fmt.Errorf("Create request error: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		log.Printf("[MCP Client] HTTP request failed: %v", err)
		return nil, fmt.Errorf("HTTP request error: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	log.Printf("[MCP Client] Response from %s: status=%d, body_size=%d bytes", toolName, resp.StatusCode, len(respBody))

	if resp.StatusCode != http.StatusOK {
		errMsg := string(respBody)
		if len(errMsg) > 200 {
			errMsg = errMsg[:200]
		}
		log.Printf("[MCP Client] HTTP error: %s", errMsg)
		return nil, fmt.Errorf("HTTP error (status %d): %s", resp.StatusCode, errMsg)
	}

	var mcpResp MCPResponse
	if err := json.Unmarshal(respBody, &mcpResp); err != nil {
		log.Printf("[MCP Client] Failed to parse MCP response: %v", err)
		return nil, fmt.Errorf("Parse response error: %w", err)
	}

	if mcpResp.Error != nil {
		log.Printf("[MCP Client] MCP error: code=%d, message=%s", mcpResp.Error.Code, mcpResp.Error.Message)
		return nil, fmt.Errorf("MCP error (code %d): %s", mcpResp.Error.Code, mcpResp.Error.Message)
	}

	result, ok := mcpResp.Result.(map[string]interface{})
	if !ok {
		log.Printf("[MCP Client] Invalid result format: type=%T", mcpResp.Result)
		return nil, fmt.Errorf("Invalid result format")
	}

	log.Printf("[MCP Client] Tool %s called successfully", toolName)
	return result, nil
}

// nextRequestID 生成下一个请求 ID
func (c *Client) nextRequestID() int {
	c.requestID++
	return c.requestID
}
