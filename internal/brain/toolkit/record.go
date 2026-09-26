package toolkit

import "time"

// ToolCallRecord 工具调用记录（纯数据，语义对齐 internal/tools.ToolCallRecord）。
type ToolCallRecord struct {
	ToolName   string      `json:"tool_name"`
	ToolCallID string      `json:"tool_call_id"`
	Arguments  interface{} `json:"arguments"`
	Result     interface{} `json:"result,omitempty"`
	Error      string      `json:"error,omitempty"`
	Success    bool        `json:"success"`
	Timestamp  time.Time   `json:"timestamp"`
}