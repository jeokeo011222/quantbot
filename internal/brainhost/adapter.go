package brainhost

import (
	"context"

	"github.com/quantpilot/quantpilot/internal/brain/port"
	"github.com/quantpilot/quantpilot/internal/brain/toolkit"
	toolpkg "github.com/quantpilot/quantpilot/internal/tools"
)

// toolAdapter 将宿主真实工具实现（internal/tools.Tool）适配为决策脑抽象 port.ToolExecutor。
// internal/tools.*Tool 的 GetDefinition 返回 tools.ToolDefinition，与 toolkit.ToolDefinition
// 结构相同但为不同具名类型，故需在宿主装配侧统一转换后注入决策脑（保持决策脑不依赖宿主包）。
type toolAdapter struct {
	inner toolpkg.Tool
}

func (a *toolAdapter) Name() string {
	return a.inner.Name()
}

func (a *toolAdapter) Description() string {
	return a.inner.Description()
}

func (a *toolAdapter) GetDefinition() toolkit.ToolDefinition {
	d := a.inner.GetDefinition()
	return toolkit.ToolDefinition{
		Type: d.Type,
		Function: toolkit.ToolFunction{
			Name:        d.Function.Name,
			Description: d.Function.Description,
			Parameters:  d.Function.Parameters,
		},
	}
}

func (a *toolAdapter) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	return a.inner.Execute(ctx, args)
}

// AdaptTool 将任意 internal/tools.Tool 实现适配为 port.ToolExecutor。
// 宿主（internal/harness 等）在装配点调用本函数，把真实工具注入决策脑。
func AdaptTool(t toolpkg.Tool) port.ToolExecutor {
	return &toolAdapter{inner: t}
}