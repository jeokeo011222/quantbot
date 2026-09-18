package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/quantpilot/quantpilot/internal/config"
	llm "github.com/quantpilot/quantpilot/internal/port"
)

// Server MCP 服务器
type Server struct {
	config     *config.AppConfig
	httpServer *http.Server
	mu         sync.RWMutex
	running    bool
	readyCh    chan struct{}
}

// NewServer 创建 MCP 服务器
func NewServer(cfg *config.AppConfig) *Server {
	return &Server{
		config:  cfg,
		readyCh: make(chan struct{}),
	}
}

// Start 启动 MCP 服务器
func (s *Server) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.running {
		return fmt.Errorf("MCP server already running")
	}

	// 解析 MCP URL，提取 host:port
	parsedURL, err := url.Parse(s.config.MCPURL)
	if err != nil {
		return fmt.Errorf("invalid MCP URL: %v", err)
	}
	addr := parsedURL.Host
	if addr == "" {
		addr = "127.0.0.1:8765"
	}
	log.Printf("[MCP] Server address: %s", addr)

	// 测试端口是否可用
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("port %s already in use or unavailable: %v", addr, err)
	}
	listener.Close()

	mux := http.NewServeMux()

	// MCP HTTP 端点
	mux.HandleFunc("/mcp/v1", s.handleMCP)

	// 健康检查
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":             "ok",
			"api_key_configured": s.config.AIAPIKey != "",
			"model":              s.config.AIModel,
		})
	})

	s.httpServer = &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	s.running = true

	go func() {
		log.Printf("[MCP] Server starting on %s", addr)
		log.Printf("[MCP] API Key configured: %v", s.config.AIAPIKey != "")
		log.Printf("[MCP] Model: %s", s.config.AIModel)

		// 启动服务器
		go func() {
			if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Printf("[MCP] Server error: %v", err)
				s.mu.Lock()
				s.running = false
				s.mu.Unlock()
				s.readyCh = make(chan struct{})
			}
		}()

		// 等待服务器就绪
		time.Sleep(50 * time.Millisecond)
		close(s.readyCh)
		log.Printf("[MCP] Server ready")
	}()

	return nil
}

// Ready 等待服务器就绪
func (s *Server) Ready() {
	s.mu.RLock()
	ch := s.readyCh
	s.mu.RUnlock()
	<-ch
}

// WaitReady 等待服务器就绪，带超时
func (s *Server) WaitReady(timeout time.Duration) bool {
	s.mu.RLock()
	ch := s.readyCh
	s.mu.RUnlock()

	select {
	case <-ch:
		return true
	case <-time.After(timeout):
		return false
	}
}

// Stop 停止 MCP 服务器
func (s *Server) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	s.running = false
	return s.httpServer.Shutdown(ctx)
}

// IsRunning 检查服务器是否运行中
func (s *Server) IsRunning() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.running
}

// UpdateConfig 更新配置
func (s *Server) UpdateConfig(cfg *config.AppConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.config = cfg
}

// handleMCP 处理 MCP 请求
func (s *Server) handleMCP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var request MCPRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeMCPError(w, nil, -32700, "Parse error")
		return
	}

	log.Printf("[MCP] Received request: method=%s, id=%v", request.Method, request.ID)

	switch request.Method {
	case "initialize":
		s.handleInitialize(w, &request)
	case "notifications/initialized":
		w.WriteHeader(http.StatusNoContent)
	case "tools/list":
		s.handleToolsList(w, &request)
	case "tools/call":
		s.handleToolsCall(w, &request)
	default:
		writeMCPError(w, request.ID, -32601, fmt.Sprintf("Method not found: %s", request.Method))
	}
}

// handleInitialize 处理初始化请求
func (s *Server) handleInitialize(w http.ResponseWriter, req *MCPRequest) {
	response := MCPResponse{
		ID:      req.ID,
		JSONRPC: "2.0",
		Result: map[string]interface{}{
			"protocolVersion": "2024-11-05",
			"capabilities": map[string]interface{}{
				"tools": map[string]interface{}{},
			},
			"serverInfo": map[string]interface{}{
				"name":    "quantpilot-llm-mcp",
				"version": "1.0.0",
			},
		},
	}

	writeMCPResponse(w, response)
}

// handleToolsList 处理工具列表请求
func (s *Server) handleToolsList(w http.ResponseWriter, req *MCPRequest) {
	tools := []ToolDefinition{
		{
			Name:        "llm_chat",
			Description: "Call LLM chat API with messages and optional tools",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"messages": map[string]interface{}{
						"type":        "array",
						"description": "Chat messages array",
						"items": map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"role": map[string]interface{}{
									"type":        "string",
									"description": "Message role (system, user, assistant, tool)",
								},
								"content": map[string]interface{}{
									"type":        "string",
									"description": "Message content",
								},
								"reasoning_content": map[string]interface{}{
									"type":        "string",
									"description": "Reasoning content from previous thinking mode (optional)",
								},
								"tool_calls": map[string]interface{}{
									"type":        "array",
									"description": "Tool calls from assistant (optional)",
								},
								"tool_call_id": map[string]interface{}{
									"type":        "string",
									"description": "Tool call ID for tool response (optional)",
								},
							},
						},
					},
					"tools": map[string]interface{}{
						"type":        "array",
						"description": "Available tools for the LLM (optional)",
					},
				},
				"required": []string{"messages"},
			},
		},
		{
			Name:        "llm_embedding",
			Description: "Get text embedding vector",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"text": map[string]interface{}{
						"type":        "string",
						"description": "Text to embed",
					},
				},
				"required": []string{"text"},
			},
		},
	}

	response := MCPResponse{
		ID:      req.ID,
		JSONRPC: "2.0",
		Result: map[string]interface{}{
			"tools": tools,
		},
	}

	writeMCPResponse(w, response)
}

// handleToolsCall 处理工具调用请求
func (s *Server) handleToolsCall(w http.ResponseWriter, req *MCPRequest) {
	params, ok := req.Params.(map[string]interface{})
	if !ok {
		writeMCPError(w, req.ID, -32602, "Invalid params")
		return
	}

	toolName, _ := params["name"].(string)
	arguments, _ := params["arguments"].(map[string]interface{})

	log.Printf("[MCP] Tool call: name=%s", toolName)

	switch toolName {
	case "llm_chat":
		s.handleLLMChat(w, req, arguments)
	case "llm_embedding":
		s.handleLLMEmbedding(w, req, arguments)
	default:
		writeMCPError(w, req.ID, -32602, fmt.Sprintf("Unknown tool: %s", toolName))
	}
}

// handleLLMChat 处理 LLM 聊天请求
func (s *Server) handleLLMChat(w http.ResponseWriter, req *MCPRequest, args map[string]interface{}) {
	s.mu.RLock()
	cfg := s.config
	s.mu.RUnlock()

	if cfg.AIAPIKey == "" {
		writeMCPError(w, req.ID, -32603, "AI API Key not configured")
		return
	}

	// 健壮地解析 messages 参数
	var messages []interface{}
	switch v := args["messages"].(type) {
	case []interface{}:
		messages = v
	case []llm.Message:
		// 直接传递 llm.Message 结构体的情况
		log.Printf("[MCP Server] Received llm.Message array, converting...")
		for _, msg := range v {
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
			messages = append(messages, msgMap)
		}
	default:
		log.Printf("[MCP Server] Invalid messages format: type=%T, value=%v", args["messages"], args["messages"])
		writeMCPError(w, req.ID, -32602, fmt.Sprintf("Invalid messages format: expected array, got %T", args["messages"]))
		return
	}

	log.Printf("[MCP Server] LLM chat request: messages=%d, has_tools=%v, model=%s", len(messages), args["tools"] != nil, cfg.AIModel)

	// 记录消息摘要
	for i, msg := range messages {
		if msgMap, ok := msg.(map[string]interface{}); ok {
			role, _ := msgMap["role"].(string)
			content, _ := msgMap["content"].(string)
			log.Printf("[MCP Server] Message %d: role=%s, content_len=%d", i, role, len(content))
		}
	}

	reqBody := map[string]interface{}{
		"model":    cfg.AIModel,
		"messages": messages,
	}

	if tools, ok := args["tools"].([]interface{}); ok && len(tools) > 0 {
		reqBody["tools"] = tools
		reqBody["tool_choice"] = "auto"
		log.Printf("[MCP Server] Tools: %d", len(tools))
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		writeMCPError(w, req.ID, -32603, fmt.Sprintf("Marshal error: %v", err))
		return
	}

	log.Printf("[MCP Server] Request body size: %d bytes, baseURL: %s", len(bodyBytes), cfg.AIBaseURL)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, "POST", cfg.AIBaseURL+"/chat/completions", bytes.NewReader(bodyBytes))
	if err != nil {
		writeMCPError(w, req.ID, -32603, fmt.Sprintf("Request error: %v", err))
		return
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+cfg.AIAPIKey)

	log.Printf("[MCP Server] Calling LLM API: POST %s/chat/completions", cfg.AIBaseURL)
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		log.Printf("[MCP Server] API call failed: %v", err)
		writeMCPError(w, req.ID, -32603, fmt.Sprintf("API call error: %v", err))
		return
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	log.Printf("[MCP Server] LLM API response: status=%d, body_size=%d", resp.StatusCode, len(respBody))

	if resp.StatusCode != http.StatusOK {
		errMsg := string(respBody)
		if len(errMsg) > 300 {
			errMsg = errMsg[:300]
		}
		log.Printf("[MCP] API error: status=%d, body=%s", resp.StatusCode, errMsg)
		writeMCPError(w, req.ID, -32603, fmt.Sprintf("API error (status %d): %s", resp.StatusCode, errMsg))
		return
	}

	// 解析 LLM 响应以获取摘要信息
	var llmResult map[string]interface{}
	if err := json.Unmarshal(respBody, &llmResult); err != nil {
		log.Printf("[MCP] Failed to parse LLM response for logging: %v", err)
	} else {
		// 提取 choices 信息
		if choices, ok := llmResult["choices"].([]interface{}); ok && len(choices) > 0 {
			if choice, ok := choices[0].(map[string]interface{}); ok {
				if msg, ok := choice["message"].(map[string]interface{}); ok {
					contentLen := 0
					if content, ok := msg["content"].(string); ok {
						contentLen = len(content)
					}
					toolCalls := 0
					if tc, ok := msg["tool_calls"].([]interface{}); ok {
						toolCalls = len(tc)
					}
					log.Printf("[MCP Server] LLM response: content_len=%d, tool_calls=%d, finish_reason=%v",
						contentLen, toolCalls, choice["finish_reason"])
				}
			}
		}
	}

	response := MCPResponse{
		ID:      req.ID,
		JSONRPC: "2.0",
		Result: map[string]interface{}{
			"content": []interface{}{
				map[string]interface{}{
					"type": "text",
					"text": string(respBody),
				},
			},
		},
	}

	writeMCPResponse(w, response)
}

// handleLLMEmbedding 处理 LLM embedding 请求
func (s *Server) handleLLMEmbedding(w http.ResponseWriter, req *MCPRequest, args map[string]interface{}) {
	s.mu.RLock()
	cfg := s.config
	s.mu.RUnlock()

	if cfg.AIAPIKey == "" {
		writeMCPError(w, req.ID, -32603, "AI API Key not configured")
		return
	}

	text, _ := args["text"].(string)
	if text == "" {
		writeMCPError(w, req.ID, -32602, "Text is required")
		return
	}

	reqBody := map[string]interface{}{
		"model": "text-embedding-v3",
		"input": text,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		writeMCPError(w, req.ID, -32603, fmt.Sprintf("Marshal error: %v", err))
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, "POST", cfg.AIBaseURL+"/embeddings", bytes.NewReader(bodyBytes))
	if err != nil {
		writeMCPError(w, req.ID, -32603, fmt.Sprintf("Request error: %v", err))
		return
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+cfg.AIAPIKey)

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		writeMCPError(w, req.ID, -32603, fmt.Sprintf("API call error: %v", err))
		return
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		errMsg := string(respBody)
		if len(errMsg) > 200 {
			errMsg = errMsg[:200]
		}
		writeMCPError(w, req.ID, -32603, fmt.Sprintf("API error (status %d): %s", resp.StatusCode, errMsg))
		return
	}

	response := MCPResponse{
		ID:      req.ID,
		JSONRPC: "2.0",
		Result: map[string]interface{}{
			"content": []interface{}{
				map[string]interface{}{
					"type": "text",
					"text": string(respBody),
				},
			},
		},
	}

	writeMCPResponse(w, response)
}

// ==================== MCP 协议结构 ====================

// MCPRequest MCP 请求
type MCPRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
	ID      interface{} `json:"id,omitempty"`
}

// MCPResponse MCP 响应
type MCPResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	Result  interface{} `json:"result,omitempty"`
	Error   *MCPError   `json:"error,omitempty"`
	ID      interface{} `json:"id,omitempty"`
}

// MCPError MCP 错误
type MCPError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

// ToolDefinition 工具定义
type ToolDefinition struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"inputSchema"`
}

// ==================== 辅助函数 ====================

func writeMCPResponse(w http.ResponseWriter, resp MCPResponse) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func writeMCPError(w http.ResponseWriter, id interface{}, code int, message string) {
	resp := MCPResponse{
		JSONRPC: "2.0",
		Error: &MCPError{
			Code:    code,
			Message: message,
		},
		ID: id,
	}
	writeMCPResponse(w, resp)
}
