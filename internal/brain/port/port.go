// Package port 定义决策脑（DLL 内）面向宿主（开源侧）的抽象接口。
//
// 决策脑只依赖本包定义的窄接口，宿主在装配点将真实实现（internal/data、
// internal/tools 等）适配为这些接口后注入，从而实现「决策脑编译为独立 DLL」。

package port

import (
	"context"
	"time"

	toolkit "github.com/quantpilot/quantpilot/internal/brain/toolkit"
)

// ToolExecutor 决策脑使用的工具执行抽象：宿主将 internal/tools.*Tool 适配后注入。
type ToolExecutor = toolkit.ToolExecutor

// ContextProvider 由宿主注入：为决策脑提供大盘行情摘要、组合快照、持仓明细、因子/策略等
// 系统真实上下文。返回空字符串表示无上下文。可为 nil。
type ContextProvider func(ctx context.Context, role string, date string) string

// TaskLog 任务日志条目视图（对齐 data.AgentTaskLog，决策脑仅需 ID 以回填状态）。
type TaskLog struct {
	ID uint
}

// Persistence 决策脑持久化抽象：抽象 data.SQLiteManager / data.AgentTaskLogger。
type Persistence interface {
	// --- 任务日志（对齐 data.AgentTaskLogger）---
	ClearTaskLogs(taskDate string) error
	LogTaskStart(taskDate, taskPhase, agentRole, taskName string, taskOrder int) *TaskLog
	LogTaskComplete(taskID uint, deliverableType, deliverableName string, deliverableData interface{}, summary string)
	LogTaskFailed(taskID uint, errMsg string)

	// --- 审计日志（对齐 runtime.writeAudit → data.AuditLog）---
	WriteAudit(eventID, eventType, userID, userName, action, targetType, targetID, result, detailsJSON string, ts time.Time) error

	// --- CIO 决策落库（对齐 orchestrator.persistOrchestratorCIODecision → data.CIODecisionLog）---
	SaveCIODecision(decisionID, portfolioID, decision, reason, riskApproval, policyStatus, marketState string, marketConfidence float64, ts time.Time) error
}