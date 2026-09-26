package agents

import (
	"fmt"
	"log"
	"sync"

	"github.com/quantpilot/quantpilot/internal/brain/port"
)

// ToolCategory 工具分类
type ToolCategory string

const (
	CategoryMarketData  ToolCategory = "market_data"  // 市场数据
	CategoryStockSearch ToolCategory = "stock_search" // 股票搜索
	CategoryMarketStats ToolCategory = "market_stats" // 市场统计
	CategoryBacktest    ToolCategory = "backtest"     // 回测
	CategoryRisk        ToolCategory = "risk"         // 风控
	CategoryExecution   ToolCategory = "execution"    // 交易执行
	CategoryPortfolio   ToolCategory = "portfolio"    // 组合管理
	CategoryResearch    ToolCategory = "research"     // 研究分析
	CategorySystem      ToolCategory = "system"       // 系统工具
)

// ToolMetadata 工具元数据
type ToolMetadata struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Category    ToolCategory      `json:"category"`
	Roles       []AgentRole       `json:"roles"`      // 哪些角色可以使用
	RiskLevel   string            `json:"risk_level"` // "low", "medium", "high"
	Enabled     bool              `json:"enabled"`
	Version     string            `json:"version"`
	Tool        port.ToolExecutor `json:"-"`
}

// ToolRegistry 工具注册表（中央工具管理）
type ToolRegistry struct {
	mu    sync.RWMutex
	tools map[string]*ToolMetadata
}

// NewToolRegistry 创建工具注册表
func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{
		tools: make(map[string]*ToolMetadata),
	}
}

// Register 注册工具
func (r *ToolRegistry) Register(meta *ToolMetadata) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.tools[meta.Name]; exists {
		return fmt.Errorf("tool %s already registered", meta.Name)
	}

	meta.Enabled = true
	r.tools[meta.Name] = meta
	log.Printf("[ToolRegistry] Registered: %s [%s] category=%s", meta.Name, meta.Version, meta.Category)
	return nil
}

// Unregister 注销工具
func (r *ToolRegistry) Unregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.tools, name)
	log.Printf("[ToolRegistry] Unregistered: %s", name)
}

// Enable 启用工具
func (r *ToolRegistry) Enable(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if meta, ok := r.tools[name]; ok {
		meta.Enabled = true
	}
}

// Disable 禁用工具
func (r *ToolRegistry) Disable(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if meta, ok := r.tools[name]; ok {
		meta.Enabled = false
	}
}

// Get 获取工具元数据
func (r *ToolRegistry) Get(name string) (*ToolMetadata, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	m, ok := r.tools[name]
	return m, ok
}

// GetTool 获取工具实例
func (r *ToolRegistry) GetTool(name string) (port.ToolExecutor, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	m, ok := r.tools[name]
	if !ok || !m.Enabled {
		return nil, false
	}
	return m.Tool, true
}

// List 列出所有工具
func (r *ToolRegistry) List() []*ToolMetadata {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]*ToolMetadata, 0, len(r.tools))
	for _, m := range r.tools {
		result = append(result, m)
	}
	return result
}

// ListEnabled 列出所有启用的工具
func (r *ToolRegistry) ListEnabled() []*ToolMetadata {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []*ToolMetadata
	for _, m := range r.tools {
		if m.Enabled {
			result = append(result, m)
		}
	}
	return result
}

// ListByCategory 按分类列出工具
func (r *ToolRegistry) ListByCategory(category ToolCategory) []*ToolMetadata {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []*ToolMetadata
	for _, m := range r.tools {
		if m.Category == category && m.Enabled {
			result = append(result, m)
		}
	}
	return result
}

// ListByRole 按角色列出可用工具
func (r *ToolRegistry) ListByRole(role AgentRole) []*ToolMetadata {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []*ToolMetadata
	for _, m := range r.tools {
		if !m.Enabled {
			continue
		}
		for _, allowedRole := range m.Roles {
			if allowedRole == role {
				result = append(result, m)
				break
			}
		}
	}
	return result
}

// CheckToolPermission 运行时强制校验：角色是否有权使用指定工具
// 返回 (是否允许, 错误)。工具未注册时返回错误（视为不允许）。
func (r *ToolRegistry) CheckToolPermission(role AgentRole, toolName string) (bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	meta, ok := r.tools[toolName]
	if !ok {
		return false, fmt.Errorf("tool %s not registered in ToolRegistry", toolName)
	}
	if !meta.Enabled {
		return false, nil
	}
	for _, allowedRole := range meta.Roles {
		if allowedRole == role {
			return true, nil
		}
	}
	return false, nil
}

// GetToolsForRole 获取角色可用的工具实例列表
func (r *ToolRegistry) GetToolsForRole(role AgentRole) []port.ToolExecutor {
	tools := r.ListByRole(role)
	result := make([]port.ToolExecutor, 0, len(tools))
	for _, m := range tools {
		result = append(result, m.Tool)
	}
	return result
}

// GetToolsByNames 按名称获取工具实例
func (r *ToolRegistry) GetToolsByNames(names []string) []port.ToolExecutor {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]port.ToolExecutor, 0)
	for _, name := range names {
		if m, ok := r.tools[name]; ok && m.Enabled {
			result = append(result, m.Tool)
		}
	}
	return result
}

// Categories 列出所有分类
func (r *ToolRegistry) Categories() map[ToolCategory][]string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make(map[ToolCategory][]string)
	for _, m := range r.tools {
		if !m.Enabled {
			continue
		}
		result[m.Category] = append(result[m.Category], m.Name)
	}
	return result
}

// Stats 获取统计信息
func (r *ToolRegistry) Stats() map[string]interface{} {
	r.mu.RLock()
	defer r.mu.RUnlock()

	enabled := 0
	byCategory := make(map[string]int)
	for _, m := range r.tools {
		if m.Enabled {
			enabled++
		}
		byCategory[string(m.Category)]++
	}

	return map[string]interface{}{
		"total":       len(r.tools),
		"enabled":     enabled,
		"disabled":    len(r.tools) - enabled,
		"by_category": byCategory,
	}
}

// ==================== 预置A-Share工具注册 ====================

// RegisterAShareTools 注册A股专用工具
func RegisterAShareTools(registry *ToolRegistry,
	tdxDataTool port.ToolExecutor,
	tdxSearchTool port.ToolExecutor,
	tdxMarketTool port.ToolExecutor,
	tdxCommonTool port.ToolExecutor,
	backtestTool port.ToolExecutor,
	portfolioTool port.ToolExecutor,
	marketDataTool port.ToolExecutor,
	searchMarketTool port.ToolExecutor,
	cninfoTool port.ToolExecutor,
) {
	tools := []*ToolMetadata{
		{
			Name:        "get_tdx_stock_data",
			Description: "获取A股个股K线数据和技术指标（仅QUANT和RISK可使用）",
			Category:    CategoryMarketData,
			Roles:       []AgentRole{RoleQuant, RoleRisk},
			RiskLevel:   "low",
			Version:     "1.0.0",
			Tool:        tdxDataTool,
		},
		{
			Name:        "search_tdx_stocks",
			Description: "按条件筛选A股股票（仅CIO和Quant可使用）",
			Category:    CategoryStockSearch,
			Roles:       []AgentRole{RoleQuant, RoleCIO},
			RiskLevel:   "low",
			Version:     "1.0.0",
			Tool:        tdxSearchTool,
		},
		{
			Name:        "get_tdx_market_stats",
			Description: "获取A股市场统计数据（涨跌家数、涨停跌停）",
			Category:    CategoryMarketStats,
			Roles:       []AgentRole{RoleCIO, RoleRisk, RoleQuant, RolePlanner, RoleTrader},
			RiskLevel:   "low",
			Version:     "1.0.0",
			Tool:        tdxMarketTool,
		},
		{
			Name:        "get_tdx_common_stocks",
			Description: "获取A股蓝筹股/热门股票列表",
			Category:    CategoryMarketData,
			Roles:       []AgentRole{RoleCIO, RoleQuant},
			RiskLevel:   "low",
			Version:     "1.0.0",
			Tool:        tdxCommonTool,
		},
		// 新增：回测工具
		{
			Name:        "run_backtest",
			Description: "运行策略回测，验证历史表现（仅QUANT可使用）",
			Category:    "Backtest",
			Roles:       []AgentRole{RoleQuant},
			RiskLevel:   "medium",
			Version:     "1.0.0",
			Tool:        backtestTool,
		},
		// 新增：投资组合优化工具
		{
			Name:        "optimize_portfolio",
			Description: "基于风险收益权衡优化投资组合配置（CIO、投资规划师、Quant可使用）",
			Category:    "Portfolio",
			Roles:       []AgentRole{RoleCIO, RolePlanner, RoleQuant},
			RiskLevel:   "medium",
			Version:     "1.0.0",
			Tool:        portfolioTool,
		},
		// 新增：通用市场数据工具
		{
			Name:        "get_market_data",
			Description: "获取历史市场数据（全球市场通用）",
			Category:    CategoryMarketData,
			Roles:       []AgentRole{RoleCIO, RoleQuant, RoleRisk},
			RiskLevel:   "low",
			Version:     "1.0.0",
			Tool:        marketDataTool,
		},
		// 新增：通用市场搜索工具
		{
			Name:        "search_market",
			Description: "按条件搜索市场股票（全球市场通用）",
			Category:    CategoryStockSearch,
			Roles:       []AgentRole{RoleCIO, RoleQuant},
			RiskLevel:   "low",
			Version:     "1.0.0",
			Tool:        searchMarketTool,
		},
		// 新增：巨潮资讯网信息披露工具（中国证监会指定上市公司信息披露平台）
		{
			Name:        "get_cninfo_info",
			Description: "从巨潮资讯网查询A股上市公司公告/资讯（PLANNER、QUANT、RISK、CIO可使用）",
			Category:    CategoryResearch,
			Roles:       []AgentRole{RolePlanner, RoleQuant, RoleRisk, RoleCIO},
			RiskLevel:   "low",
			Version:     "1.0.0",
			Tool:        cninfoTool,
		},
	}

	for _, t := range tools {
		if err := registry.Register(t); err != nil {
			log.Printf("[ToolRegistry] Failed to register %s: %v", t.Name, err)
		}
	}
}
