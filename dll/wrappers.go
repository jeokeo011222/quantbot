package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/quantpilot/quantpilot/internal/brain/port"
	"github.com/quantpilot/quantpilot/internal/brain/toolkit"
)

// -------- 远程包装：实现 port / toolkit 接口，但把每次调用发到 RPC 总线并阻塞等宿主兑现 ----
// （LLM 已归 DLL 自持：决策脑按 AgentInit 传入的配置自建 llm.Client，无远程包装。）

// remotePersistence 实现 port.Persistence，每 OP 各发一条 kind=store 的总线请求。
type remotePersistence struct {
	s *brainSession
}

func newRemotePersistence(s *brainSession) *remotePersistence { return &remotePersistence{s: s} }

func (r *remotePersistence) ClearTaskLogs(taskDate string) error {
	_, err := r.s.sendRequest(context.Background(), kindStore, struct {
		Op   string `json:"op"`
		Date string `json:"date"`
	}{Op: "clear_task_logs", Date: taskDate})
	return err
}

func (r *remotePersistence) LogTaskStart(taskDate, taskPhase, agentRole, taskName string, taskOrder int) *port.TaskLog {
	raw, err := r.s.sendRequest(context.Background(), kindStore, struct {
		Op        string `json:"op"`
		TaskDate  string `json:"task_date"`
		TaskPhase string `json:"task_phase"`
		AgentRole string `json:"agent_role"`
		TaskName  string `json:"task_name"`
		TaskOrder int    `json:"task_order"`
	}{Op: "log_task_start", TaskDate: taskDate, TaskPhase: taskPhase, AgentRole: agentRole, TaskName: taskName, TaskOrder: taskOrder})
	if err != nil {
		return nil
	}
	var resp struct {
		TaskID uint `json:"task_id"`
	}
	if json.Unmarshal(raw, &resp) != nil {
		return nil
	}
	return &port.TaskLog{ID: resp.TaskID}
}

func (r *remotePersistence) LogTaskComplete(taskID uint, deliverableType, deliverableName string, deliverableData interface{}, summary string) {
	_, _ = r.s.sendRequest(context.Background(), kindStore, struct {
		Op              string      `json:"op"`
		TaskID          uint        `json:"task_id"`
		DeliverableType string      `json:"deliverable_type"`
		DeliverableName string      `json:"deliverable_name"`
		DeliverableData interface{} `json:"deliverable_data"`
		Summary         string      `json:"summary"`
	}{Op: "log_task_complete", TaskID: taskID, DeliverableType: deliverableType, DeliverableName: deliverableName, DeliverableData: deliverableData, Summary: summary})
}

func (r *remotePersistence) LogTaskFailed(taskID uint, errMsg string) {
	_, _ = r.s.sendRequest(context.Background(), kindStore, struct {
		Op     string `json:"op"`
		TaskID uint   `json:"task_id"`
		ErrMsg string `json:"err_msg"`
	}{Op: "log_task_failed", TaskID: taskID, ErrMsg: errMsg})
}

func (r *remotePersistence) WriteAudit(eventID, eventType, userID, userName, action, targetType, targetID, result, detailsJSON string, ts time.Time) error {
	_, err := r.s.sendRequest(context.Background(), kindStore, struct {
		Op          string `json:"op"`
		EventID     string `json:"event_id"`
		EventType   string `json:"event_type"`
		UserID      string `json:"user_id"`
		UserName    string `json:"user_name"`
		Action      string `json:"action"`
		TargetType  string `json:"target_type"`
		TargetID    string `json:"target_id"`
		Result      string `json:"result"`
		DetailsJSON string `json:"details_json"`
		TS          string `json:"ts"`
	}{Op: "write_audit", EventID: eventID, EventType: eventType, UserID: userID, UserName: userName,
		Action: action, TargetType: targetType, TargetID: targetID, Result: result, DetailsJSON: detailsJSON,
		TS: ts.Format(time.RFC3339)})
	return err
}

func (r *remotePersistence) SaveCIODecision(decisionID, portfolioID, decision, reason, riskApproval, policyStatus, marketState string, marketConfidence float64, ts time.Time) error {
	_, err := r.s.sendRequest(context.Background(), kindStore, struct {
		Op               string  `json:"op"`
		DecisionID       string  `json:"decision_id"`
		PortfolioID      string  `json:"portfolio_id"`
		Decision         string  `json:"decision"`
		Reason           string  `json:"reason"`
		RiskApproval     string  `json:"risk_approval"`
		PolicyStatus     string  `json:"policy_status"`
		MarketState      string  `json:"market_state"`
		MarketConfidence float64 `json:"market_confidence"`
		TS               string  `json:"ts"`
	}{Op: "save_cio_decision", DecisionID: decisionID, PortfolioID: portfolioID, Decision: decision, Reason: reason,
		RiskApproval: riskApproval, PolicyStatus: policyStatus, MarketState: marketState,
		MarketConfidence: marketConfidence, TS: ts.Format(time.RFC3339)})
	return err
}

// remoteTool 实现 toolkit.ToolExecutor。Name/Description/GetDefinition 由目录静态给出，
// Execute 发送 kind=tool 请求 {name, args} 并阻塞等待宿主结果。
type remoteTool struct {
	s           *brainSession
	name        string
	description string
	parameters  interface{}
}

func newRemoteTool(s *brainSession, name, description string, parameters json.RawMessage) *remoteTool {
	var p interface{}
	if len(parameters) > 0 {
		if err := json.Unmarshal(parameters, &p); err != nil {
			p = parameters
		}
	}
	return &remoteTool{s: s, name: name, description: description, parameters: p}
}

func (t *remoteTool) Name() string { return t.name }

func (t *remoteTool) Description() string { return t.description }

func (t *remoteTool) GetDefinition() toolkit.ToolDefinition {
	return toolkit.ToolDefinition{
		Type: "function",
		Function: toolkit.ToolFunction{
			Name:        t.name,
			Description: t.description,
			Parameters:  t.parameters,
		},
	}
}

func (t *remoteTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	raw, err := t.s.sendRequest(ctx, kindTool, struct {
		Name string                 `json:"name"`
		Args map[string]interface{} `json:"args"`
	}{Name: t.name, Args: args})
	if err != nil {
		return nil, err
	}
	var result interface{}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("无法解码宿主工具结果: %w", err)
	}
	return result, nil
}
