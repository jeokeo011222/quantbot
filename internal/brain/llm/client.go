package llm

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
)

// Client LLM 客户端接口
type Client interface {
	Chat(ctx context.Context, messages []Message, tools []Tool) (*ChatResult, error)
	StreamChat(ctx context.Context, messages []Message, tools []Tool) (*StreamReader, error)
	Embedding(ctx context.Context, text string) ([]float64, error)
}

// Message 聊天消息
type Message struct {
	Role             string `json:"role"`
	Content          string `json:"content,omitempty"`
	ReasoningContent string `json:"reasoning_content,omitempty"` // deepseek-chat thinking mode
	ToolCalls        []struct {
		ID       string `json:"id"`
		Type     string `json:"type"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	} `json:"tool_calls,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`
}

// ToolFunction 工具函数定义
type ToolFunction struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Parameters  interface{} `json:"parameters"`
}

// Tool 工具定义
type Tool struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

// ChatResult 聊天结果
type ChatResult struct {
	ID      string `json:"id"`
	Choices []struct {
		Index   int `json:"index"`
		Message struct {
			Content          string         `json:"content"`
			Role             string         `json:"role"`
			ReasoningContent string         `json:"reasoning_content,omitempty"` // deepseek-chat thinking mode
			ToolCalls        []ToolCallInfo `json:"tool_calls,omitempty"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
	Model string `json:"model"`
}

// ToolCallInfo 工具调用信息（来自 LLM 响应）
type ToolCallInfo struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// StreamReader 流式响应读取器
type StreamReader struct {
	reader io.ReadCloser
}

func (sr *StreamReader) Close() error {
	if sr.reader != nil {
		return sr.reader.Close()
	}
	return nil
}

// NewStreamReader 创建 StreamReader
func NewStreamReader(r io.ReadCloser) *StreamReader {
	return &StreamReader{reader: r}
}

// DeepSeekClient DeepSeek API 客户端（同时兼容 OpenAI Chat Completions 协议）
type DeepSeekClient struct {
	apiKey     string
	baseURL    string
	model      string
	provider   string
	httpClient *http.Client
}

// Provider 返回客户端对应的 AIProvider 标识（deepseek/openai/custom）
func (c *DeepSeekClient) Provider() string {
	return c.provider
}

// ProviderDefaults 返回各 AIProvider 的默认 baseURL/model（仅当用户未显式指定时生效）
func ProviderDefaults(provider string) (baseURL, model string) {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "openai":
		return "https://api.openai.com", "gpt-4o-mini"
	case "deepseek", "":
		return "https://api.deepseek.com", "deepseek-v4-flash"
	default: // custom / 其他：保留用户传入的 URL/model
		return "", ""
	}
}

// NewClient 按 AIProvider 创建 LLM 客户端工厂。
// - deepseek/openai/custom 均走 Chat Completions 兼容协议（同一数据结构），差异仅在默认 baseURL/model。
// - provider 未指定 baseURL/model 时采用各厂商默认值（custom 时完全由用户提供）。
// - 若后续某 provider 需要使用非兼容协议（如 OpenAI Responses API），在此按 provider 分支返回专用客户端。
func NewClient(provider, apiKey, baseURL, model string) *DeepSeekClient {
	dBase, dModel := ProviderDefaults(provider)
	if baseURL == "" {
		baseURL = dBase
	}
	if model == "" {
		model = dModel
	}
	normalizedProvider := strings.ToLower(strings.TrimSpace(provider))
	c := NewDeepSeekClient(apiKey, baseURL, model)
	c.provider = normalizedProvider
	return c
}

// NewDeepSeekClient 创建 DeepSeek 客户端
// 自动处理API key格式：去除"Bearer "前缀、前后空格
func NewDeepSeekClient(apiKey, baseURL, model string) *DeepSeekClient {
	if baseURL == "" {
		baseURL = "https://api.deepseek.com"
	}
	if model == "" {
		model = "deepseek-v4-flash"
	}
	// 清理API key：去除"Bearer "前缀和前后空格
	apiKey = normalizeAPIKey(apiKey)

	if apiKey == "" {
		log.Printf("[LLM] WARNING: API key is empty! LLM calls will fail with authentication error.")
		log.Printf("[LLM] Please configure your API key in Settings before using AI features.")
	} else {
		log.Printf("[LLM] API key configured successfully (length: %d, prefix: %s...)", len(apiKey), getKeyPrefix(apiKey))
	}

	log.Printf("[LLM] LLM client initialized: baseURL=%s, model=%s", baseURL, model)

	return &DeepSeekClient{
		apiKey:  apiKey,
		baseURL: baseURL,
		model:   model,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// getKeyPrefix 获取API key的前缀（用于日志脱敏显示）
func getKeyPrefix(key string) string {
	if len(key) >= 8 {
		return key[:8]
	}
	return key
}

// HasValidAPIKey 检查是否配置了有效的API key
func (c *DeepSeekClient) HasValidAPIKey() bool {
	return c.apiKey != ""
}

// normalizeAPIKey 标准化API key格式
// - 去除前后空格
// - 去除可能包含的"Bearer "前缀（用户可能直接粘贴了完整header）
func normalizeAPIKey(key string) string {
	key = strings.TrimSpace(key)
	key = strings.TrimPrefix(key, "Bearer ")
	key = strings.TrimPrefix(key, "BEARER ")
	key = strings.TrimSpace(key)
	return key
}

// Chat 发送聊天请求
func (c *DeepSeekClient) Chat(ctx context.Context, messages []Message, tools []Tool) (*ChatResult, error) {
	// 检查API key是否已配置
	if !c.HasValidAPIKey() {
		log.Printf("[LLM] Chat failed: API key not configured")
		return nil, fmt.Errorf("API key not configured. Please set your API key in Settings > AI Configuration")
	}

	log.Printf("[LLM] Chat request: model=%s, messages=%d, tools=%d, baseURL=%s",
		c.model, len(messages), len(tools), c.baseURL)

	reqBody := map[string]interface{}{
		"model":    c.model,
		"messages": messages,
	}

	if len(tools) > 0 {
		reqBody["tools"] = tools
		reqBody["tool_choice"] = "auto"
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		log.Printf("[LLM] Failed to marshal request: %v", err)
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	log.Printf("[LLM] Request body size: %d bytes", len(bodyBytes))

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/chat/completions", bytes.NewReader(bodyBytes))
	if err != nil {
		log.Printf("[LLM] Failed to create request: %v", err)
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	log.Printf("[LLM] Sending request to %s/chat/completions", c.baseURL)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		log.Printf("[LLM] HTTP request failed: %v", err)
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	log.Printf("[LLM] Response received: status=%d, body_size=%d bytes", resp.StatusCode, len(respBody))

	if resp.StatusCode != http.StatusOK {
		errMsg := string(respBody)
		if len(errMsg) > 300 {
			errMsg = errMsg[:300]
		}

		// 检查是否为认证错误
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			log.Printf("[LLM] Authentication failed! Status: %d, Response: %s", resp.StatusCode, errMsg)
			return nil, fmt.Errorf("LLM authentication failed (API key may be invalid or expired). Status: %d, Detail: %s", resp.StatusCode, errMsg)
		}

		log.Printf("[LLM] API error! Status: %d, Response: %s", resp.StatusCode, errMsg)
		return nil, fmt.Errorf("API request failed (status %d): %s", resp.StatusCode, errMsg)
	}

	var result ChatResult
	if err := json.Unmarshal(respBody, &result); err != nil {
		log.Printf("[LLM] Failed to decode response: %v", err)
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	log.Printf("[LLM] Chat completed: choices=%d, usage=%v", len(result.Choices), result.Usage)
	return &result, nil
}

// StreamChat 发送流式聊天请求
func (c *DeepSeekClient) StreamChat(ctx context.Context, messages []Message, tools []Tool) (*StreamReader, error) {
	// 检查API key是否已配置
	if !c.HasValidAPIKey() {
		return nil, fmt.Errorf("API key not configured. Please set your API key in Settings > AI Configuration")
	}

	reqBody := map[string]interface{}{
		"model":    c.model,
		"messages": messages,
		"stream":   true,
	}

	if len(tools) > 0 {
		reqBody["tools"] = tools
		reqBody["tool_choice"] = "auto"
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/chat/completions", bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		errMsg := string(body)

		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			log.Printf("[LLM] Authentication failed (stream)! Status: %d, Response: %s", resp.StatusCode, errMsg)
			return nil, fmt.Errorf("LLM authentication failed (API key may be invalid or expired). Status: %d, Detail: %s", resp.StatusCode, errMsg)
		}

		return nil, fmt.Errorf("API request failed (status %d): %s", resp.StatusCode, errMsg)
	}

	return &StreamReader{reader: resp.Body}, nil
}

// Embedding 获取文本嵌入向量
func (c *DeepSeekClient) Embedding(ctx context.Context, text string) ([]float64, error) {
	// 检查API key是否已配置
	if !c.HasValidAPIKey() {
		return nil, fmt.Errorf("API key not configured. Please set your API key in Settings > AI Configuration")
	}

	reqBody := map[string]interface{}{
		"model": "text-embedding-v3",
		"input": text,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/embeddings", bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		errMsg := string(body)

		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			log.Printf("[LLM] Authentication failed (embedding)! Status: %d, Response: %s", resp.StatusCode, errMsg)
			return nil, fmt.Errorf("LLM authentication failed (API key may be invalid or expired). Status: %d, Detail: %s", resp.StatusCode, errMsg)
		}

		return nil, fmt.Errorf("API request failed (status %d): %s", resp.StatusCode, errMsg)
	}

	var result struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	if len(result.Data) == 0 {
		return nil, fmt.Errorf("no embedding returned")
	}

	return result.Data[0].Embedding, nil
}
