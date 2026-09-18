// Package llmmonitoring 提供可开关的大模型调用输入/输出内容审计记录。
// 通过包装 llm.Client，在调用前后捕获完整输入 messages 与输出内容并回调持久化。
package llmmonitoring

import (
	"context"
	"encoding/json"
	"time"

	llm "github.com/quantpilot/quantpilot/internal/port"
)

// CallMeta 调用来源元信息（通过 context 从上层层层传递，用于标注角色/任务/阶段）
type CallMeta struct {
	AgentRole string
	TaskName  string
	Phase     string
}

type ctxKey struct{}

// WithCallMeta 将调用来源元信息写入 context
func WithCallMeta(ctx context.Context, m CallMeta) context.Context {
	return context.WithValue(ctx, ctxKey{}, m)
}

// CallMetaFrom 从 context 读取调用来源元信息
func CallMetaFrom(ctx context.Context) (CallMeta, bool) {
	if ctx == nil {
		return CallMeta{}, false
	}
	m, ok := ctx.Value(ctxKey{}).(CallMeta)
	return m, ok
}

// Record 一条大模型调用记录
type Record struct {
	TaskDate         string
	AgentRole        string
	TaskName         string
	Phase            string
	Model            string
	InputMessages    string
	OutputContent    string
	FinishReason     string
	ToolCallCount    int
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	DurationMs       int64
	Status           string
	ErrorMessage     string
	CreatedAt        time.Time
}

// Sink 持久化回调
type Sink func(Record)

// Client 包装原始 LLM 客户端，可开关地记录输入/输出内容
type Client struct {
	inner   llm.LLMClient
	enabled func() bool
	sink    Sink
}

// NewClient 创建监控包装客户端
func NewClient(inner llm.LLMClient, enabled func() bool, sink Sink) *Client {
	return &Client{inner: inner, enabled: enabled, sink: sink}
}

func (c *Client) Chat(ctx context.Context, messages []llm.Message, tools []llm.Tool) (*llm.ChatResult, error) {
	start := time.Now()
	result, err := c.inner.Chat(ctx, messages, tools)
	if c.enabled() && c.sink != nil {
		rec := c.buildRecord(ctx, messages, result, err, start)
		// 异步持久化，避免阻塞 LLM 调用返回
		go c.sink(rec)
	}
	return result, err
}

func (c *Client) StreamChat(ctx context.Context, messages []llm.Message, tools []llm.Tool) (*llm.StreamReader, error) {
	return c.inner.StreamChat(ctx, messages, tools)
}

func (c *Client) Embedding(ctx context.Context, text string) ([]float64, error) {
	return c.inner.Embedding(ctx, text)
}

func (c *Client) buildRecord(ctx context.Context, messages []llm.Message, result *llm.ChatResult, err error, start time.Time) Record {
	meta, _ := CallMetaFrom(ctx)

	rec := Record{
		TaskDate:   time.Now().Format("2006-01-02"),
		AgentRole:  meta.AgentRole,
		TaskName:   meta.TaskName,
		Phase:      meta.Phase,
		DurationMs: time.Since(start).Milliseconds(),
		CreatedAt:  time.Now(),
	}

	if in, merr := json.Marshal(messages); merr == nil {
		rec.InputMessages = string(in)
	}

	if err != nil {
		rec.Status = "FAILED"
		rec.ErrorMessage = err.Error()
		return rec
	}

	rec.Status = "SUCCESS"
	if result != nil {
		rec.Model = result.Model
		rec.PromptTokens = result.Usage.PromptTokens
		rec.CompletionTokens = result.Usage.CompletionTokens
		rec.TotalTokens = result.Usage.TotalTokens
		if len(result.Choices) > 0 {
			msg0 := result.Choices[0].Message
			rec.OutputContent = msg0.Content
			if msg0.ReasoningContent != "" {
				rec.OutputContent += "\n【思考过程】\n" + msg0.ReasoningContent
			}
			rec.FinishReason = result.Choices[0].FinishReason
			rec.ToolCallCount = len(msg0.ToolCalls)
		}
	}
	return rec
}
