// Package toolkit 定义决策脑自有的工具契约（纯元数据 + 纯数据 + 执行抽象）。
//
// 该包不依赖任何宿主（开源）包：internal/tools 等真实工具实现由宿主经适配后注入，
// 决策脑（DLL 内）只面向本包定义的抽象，从而实现「工具执行器宿主注册 + DLL 拉模式」。
package toolkit

import "context"

// ToolFunction 函数定义（用于 LLM function calling，语义对齐 internal/tools.FunctionDef）。
type ToolFunction struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Parameters  interface{} `json:"parameters"`
}

// ToolDefinition 工具定义（语义对齐 internal/tools.ToolDefinition）。
type ToolDefinition struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

// ToolExecutor 工具执行接口：方法集 = 决策脑（agents）实际调用 internal/tools.Tool 的方法。
// 宿主将真实工具（internal/tools.*Tool）适配为该接口后注入决策脑。
type ToolExecutor interface {
	Name() string
	Description() string
	GetDefinition() ToolDefinition
	Execute(ctx context.Context, args map[string]interface{}) (interface{}, error)
}

// ToolSpec 工具元数据（纯元数据，供 ToolRegistry 使用）。
type ToolSpec struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Params      map[string]interface{} `json:"params,omitempty"`
	JsonSchema  interface{}            `json:"json_schema,omitempty"`
	Category    string                 `json:"category"`
	Roles       []string               `json:"roles"`
	RiskLevel   string                 `json:"risk_level"`
	Version     string                 `json:"version"`
}