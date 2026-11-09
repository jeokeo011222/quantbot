// Package agentroles 定义 Agent 角色 + 方法子集裁剪。
//
// 设计借鉴 dsh-superagent 的 RoleInterface 模式：
//  - 调用方只能看见"自己角色"被裁剪后的方法子集
//  - 底层仍是同一个实现，但上层接口按角色暴露不同方法
//  - 防止 Agent 通过接口泄露越权能力（尤其重要：LLM 不能直接做权限判断）
//
//	注：工具权限已经有 ToolRegistry.CheckToolPermission 做强制校验（base_agent.go L783）。
//	这里叠加一层接口裁剪，形成双重防线：
//	  1. 接口层：Agent 根本看不到不属于自己角色的方法（编译期挡）
//	  2. ToolRegistry：即便绕过接口直接调用，工具注册中心仍会拒掉（运行时挡）
//
//	同时提供 RoleCapabilities 表，集中声明每个 Agent 角色能调用哪些能力族，
//	便于审计和 UI 渲染"这个角色有哪些能力"。
package agentroles

import (
	"fmt"
	"sort"
	"strings"
)

// AgentRole 智能体角色（与 base_agent.go 的 AgentRole 保持一致但独立定义）。
type AgentRole string

const (
	RoleOrchestrator AgentRole = "orchestrator"
	RolePlanner      AgentRole = "planner"
	RoleQuant        AgentRole = "quant"
	RoleRisk         AgentRole = "risk"
	RoleCIO          AgentRole = "cio"
	RoleTrader       AgentRole = "trader"
	RoleAuditor      AgentRole = "auditor"
	RoleStrategy     AgentRole = "strategy"
	RoleGeneral      AgentRole = "general" // 未限定角色的通用 Agent
)

// Capability 能力族声明——一个角色能做什么，而不是一个方法一个方法列。
// 目的是让审计/UI 用可读的"能力组"来展示，而不是列出一堆方法名。
type Capability struct {
	// Key 稳定键（与 ToolRegistry 里 tool 的 group 对齐）
	Key string
	// Label 中文展示
	Label string
	// Methods 该能力组包含的具体方法/工具名（供 ToolRegistry 做双重校验）
	Methods []string
}

// RoleCapabilities 角色 → 能力组表。
// 新增角色或修改权限只改这里，ToolRegistry 自动校验。
var RoleCapabilities = map[AgentRole][]Capability{
	RoleOrchestrator: {
		{Key: "orchestration", Label: "任务编排与分派", Methods: []string{"dispatch_agent", "register_agent", "run_daily_cycle", "pause", "resume"}},
		{Key: "evaluation", Label: "研判与评估", Methods: []string{"evaluate", "route", "assess"}},
		{Key: "policy", Label: "策略/风控策略读写", Methods: []string{"read_policy", "write_policy"}},
		{Key: "market_read", Label: "只读行情", Methods: []string{"get_snapshot", "get_quote"}},
	},
	RolePlanner: {
		{Key: "planning", Label: "组合规划", Methods: []string{"build_plan", "review_plan"}},
		{Key: "market_read", Label: "只读行情", Methods: []string{"get_snapshot", "get_quote", "get_sector"}},
		{Key: "factor_read", Label: "只读因子", Methods: []string{"read_factors"}},
	},
	RoleQuant: {
		{Key: "signal", Label: "Alpha 信号生成", Methods: []string{"generate_alpha", "score_alpha"}},
		{Key: "backtest_read", Label: "只读回测结果", Methods: []string{"read_backtest", "read_ensemble"}},
		{Key: "market_read", Label: "只读行情", Methods: []string{"get_daily_k", "get_factor"}},
	},
	RoleRisk: {
		{Key: "risk", Label: "风险度量与报告", Methods: []string{"measure_risk", "generate_risk_report", "stress_test"}},
		{Key: "market_read", Label: "只读行情", Methods: []string{"get_var", "get_volatility"}},
		{Key: "portfolio_read", Label: "只读组合", Methods: []string{"read_positions", "read_nav"}},
	},
	RoleCIO: {
		{Key: "decision", Label: "交易决策", Methods: []string{"make_decision", "place_trade"}},
		{Key: "approval", Label: "审批请求", Methods: []string{"request_approval"}},
		{Key: "portfolio_read", Label: "只读组合", Methods: []string{"read_positions", "read_nav"}},
		{Key: "market_read", Label: "只读行情", Methods: []string{"get_quote", "get_limit_price"}},
		{Key: "risk_read", Label: "只读风控结果", Methods: []string{"read_risk_report"}},
	},
	RoleTrader: {
		{Key: "execution", Label: "交易执行", Methods: []string{"execute_trade", "place_order", "cancel_order", "query_order"}},
		{Key: "broker_read", Label: "只读券商状态", Methods: []string{"query_positions", "query_cash"}},
	},
	RoleAuditor: {
		{Key: "audit", Label: "审计与追溯", Methods: []string{"read_decision_log", "read_trade_log", "read_audit_trail"}},
		{Key: "market_read", Label: "只读行情", Methods: []string{"get_hist_k"}},
		{Key: "portfolio_read", Label: "只读组合", Methods: []string{"read_history", "read_pnl"}},
	},
	RoleStrategy: {
		{Key: "tuning", Label: "策略/因子调参", Methods: []string{"tune_strategy", "tune_factor", "grid_search"}},
		{Key: "backtest_read", Label: "只读回测", Methods: []string{"run_backtest", "read_ensemble"}},
	},
	RoleGeneral: {
		{Key: "read_only", Label: "只读", Methods: []string{"read_*"}},
	},
}

// ---- 查询 ----

// HasMethod 判断某角色能否调用某个具体方法/工具。
func HasMethod(role AgentRole, method string) bool {
	caps, ok := RoleCapabilities[role]
	if !ok {
		return false
	}
	for _, c := range caps {
		for _, m := range c.Methods {
			if m == method || m == "read_*" && strings.HasPrefix(method, "read_") {
				return true
			}
		}
	}
	return false
}

// HasCapability 判断某角色是否拥有某个能力组。
func HasCapability(role AgentRole, capKey string) bool {
	for _, c := range RoleCapabilities[role] {
		if c.Key == capKey {
			return true
		}
	}
	return false
}

// AllMethods 返回某角色的全部可调用方法集合（去重、排序）。
func AllMethods(role AgentRole) []string {
	seen := map[string]bool{}
	for _, c := range RoleCapabilities[role] {
		for _, m := range c.Methods {
			seen[m] = true
		}
	}
	out := make([]string, 0, len(seen))
	for m := range seen {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

// AllRoles 返回所有已声明角色。
func AllRoles() []AgentRole {
	roles := make([]AgentRole, 0, len(RoleCapabilities))
	for r := range RoleCapabilities {
		roles = append(roles, r)
	}
	sort.Slice(roles, func(i, j int) bool { return roles[i] < roles[j] })
	return roles
}

// Summary 人类可读的角色权限概览。
func Summary(role AgentRole) string {
	caps, ok := RoleCapabilities[role]
	if !ok {
		return fmt.Sprintf("unknown role: %s", role)
	}
	var parts []string
	for _, c := range caps {
		parts = append(parts, fmt.Sprintf("%s [%s]: %s", c.Label, c.Key, strings.Join(c.Methods, ", ")))
	}
	return strings.Join(parts, "\n")
}
