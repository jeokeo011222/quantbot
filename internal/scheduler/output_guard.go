package scheduler

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
)

// ============ Output Guard 输出验证机制 v1.0 ============

// ValidationResult 验证结果
type ValidationResult struct {
	Valid      bool     `json:"valid"`
	Reason     string   `json:"reason"`
	Violations []string `json:"violations"`
	Warnings   []string `json:"warnings"`
}

// OutputGuard 输出守卫 - 验证 Agent 输出是否符合任务契约
type OutputGuard struct{}

func NewOutputGuard() *OutputGuard {
	return &OutputGuard{}
}

// ValidateOutput 验证 Agent 输出
func (g *OutputGuard) ValidateOutput(task *AgentTask, output interface{}) ValidationResult {
	result := ValidationResult{Valid: true}

	if task == nil {
		return ValidationResult{Valid: false, Reason: "task is nil", Violations: []string{"TASK_NIL"}}
	}

	outputMap := g.toMap(output)

	g.validateOutputSchema(task, outputMap, &result)
	g.validateForbiddenOutputs(task, outputMap, &result)
	g.validateDecisionBoundary(task, outputMap, &result)

	if len(result.Violations) > 0 {
		result.Valid = false
		result.Reason = fmt.Sprintf("Output validation failed with %d violation(s)", len(result.Violations))
		log.Printf("[OutputGuard] REJECT task=%s agent=%s violations=%v",
			task.ID, task.AgentRole, result.Violations)
	}

	return result
}

// validateOutputSchema 验证输出字段是否符合 Schema
func (g *OutputGuard) validateOutputSchema(task *AgentTask, outputMap map[string]interface{}, result *ValidationResult) {
	if len(task.Contract.OutputSchema) == 0 {
		return
	}

	outputFields := g.extractOutputFields(outputMap)
	schemaSet := make(map[string]bool)
	for _, field := range task.Contract.OutputSchema {
		fieldName := strings.Split(field, "(")[0]
		schemaSet[strings.ToLower(strings.TrimSpace(fieldName))] = true
	}

	matchedCount := 0
	for _, field := range outputFields {
		if schemaSet[strings.ToLower(field)] {
			matchedCount++
		}
	}

	if matchedCount == 0 && len(outputFields) > 0 {
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("OUTPUT_SCHEMA_MISMATCH: no output fields match task %s schema (output has %d fields)", task.ID, len(outputFields)))
	}
}

// validateForbiddenOutputs 检查输出中是否包含禁止的字段
func (g *OutputGuard) validateForbiddenOutputs(task *AgentTask, outputMap map[string]interface{}, result *ValidationResult) {
	forbiddenOutputs := map[string][]string{
		"Planner": {"position", "trade", "order", "buy", "sell", "signal", "factor", "risk_level_decision"},
		"Quant":   {"position", "trade", "order", "buy", "sell", "investment_decision", "risk_veto"},
		"Risk":    {"position", "trade", "order", "buy", "sell", "stock_pick", "alpha"},
		"CIO":     {"position_direct", "trade_execution", "order_direct", "quant_self", "risk_self"},
		"Trader":  {"position", "strategy", "stock_pick", "investment_advice", "decision_change"},
	}

	agentKey := string(task.AgentRole)
	forbidden, exists := forbiddenOutputs[agentKey]
	if !exists {
		return
	}

	outputFields := g.extractOutputFields(outputMap)
	for _, forbiddenField := range forbidden {
		for _, field := range outputFields {
			if strings.Contains(strings.ToLower(field), forbiddenField) {
				result.Violations = append(result.Violations,
					fmt.Sprintf("FORBIDDEN_OUTPUT: %s contains forbidden pattern '%s' for %s",
						field, forbiddenField, task.AgentRole))
			}
		}
	}
}

// validateDecisionBoundary 验证决策边界
func (g *OutputGuard) validateDecisionBoundary(task *AgentTask, outputMap map[string]interface{}, result *ValidationResult) {
	decisionFields := []string{"decision", "action", "recommendation", "order_type"}
	hasDecisionField := false

	for _, field := range g.extractOutputFields(outputMap) {
		for _, df := range decisionFields {
			if strings.Contains(strings.ToLower(field), df) {
				hasDecisionField = true
				break
			}
		}
		if hasDecisionField {
			break
		}
	}

	agent := string(task.AgentRole)
	switch agent {
	case "Risk":
		if hasDecisionField {
			for _, f := range g.extractOutputFields(outputMap) {
				if f == "decision" || f == "action" {
					result.Violations = append(result.Violations,
						"DECISION_BOUNDARY_VIOLATION: Risk agent cannot produce decisions, only PASS/WARNING/VETO")
				}
			}
		}
	case "Quant":
		if hasDecisionField {
			for _, f := range g.extractOutputFields(outputMap) {
				if f == "decision" || f == "recommendation" {
					result.Violations = append(result.Violations,
						"DECISION_BOUNDARY_VIOLATION: Quant agent cannot produce decisions, only signals")
				}
			}
		}
	case "Trader":
		if hasDecisionField {
			for _, f := range g.extractOutputFields(outputMap) {
				if f == "decision" || f == "recommendation" || f == "action" {
					result.Violations = append(result.Violations,
						"DECISION_BOUNDARY_VIOLATION: Trader agent cannot produce decisions, only execute")
				}
			}
		}
	case "Planner":
		if hasDecisionField {
			for _, f := range g.extractOutputFields(outputMap) {
				if f == "decision" || f == "recommendation" || f == "action" {
					result.Violations = append(result.Violations,
						"DECISION_BOUNDARY_VIOLATION: Planner agent cannot produce decisions, only market analysis")
				}
			}
		}
	}
}

// toMap 将任意输出转换为 map
func (g *OutputGuard) toMap(output interface{}) map[string]interface{} {
	switch v := output.(type) {
	case map[string]interface{}:
		return v
	case nil:
		return map[string]interface{}{}
	default:
		data, err := json.Marshal(output)
		if err != nil {
			return map[string]interface{}{"raw": fmt.Sprintf("%v", output)}
		}
		var m map[string]interface{}
		if err := json.Unmarshal(data, &m); err != nil {
			return map[string]interface{}{"raw": string(data)}
		}
		return m
	}
}

// extractOutputFields 提取输出中的所有字段名
func (g *OutputGuard) extractOutputFields(outputMap map[string]interface{}) []string {
	var fields []string
	for k, v := range outputMap {
		fields = append(fields, k)
		if nested, ok := v.(map[string]interface{}); ok {
			for nk := range nested {
				fields = append(fields, fmt.Sprintf("%s.%s", k, nk))
			}
		}
	}
	return fields
}

// ============ ConstraintChecker 任务约束检查器 ============

// ConstraintChecker 在任务执行前检查约束
type ConstraintChecker struct{}

func NewConstraintChecker() *ConstraintChecker {
	return &ConstraintChecker{}
}

// CheckPreExecution 任务执行前检查
func (c *ConstraintChecker) CheckPreExecution(task *AgentTask) ValidationResult {
	result := ValidationResult{Valid: true}

	if task == nil {
		return ValidationResult{Valid: false, Reason: "nil task", Violations: []string{"TASK_NIL"}}
	}

	if len(task.Contract.RequiredActions) == 0 && len(task.Contract.Purpose) == 0 {
		return ValidationResult{
			Valid:      false,
			Reason:     "Task Contract incomplete: missing Purpose and RequiredActions",
			Violations: []string{"CONTRACT_INCOMPLETE"},
		}
	}

	if task.DeliverableType == "" {
		result.Warnings = append(result.Warnings, "NO_DELIVERABLE_TYPE")
	}

	if len(task.Contract.NOActionCode) == 0 {
		result.Warnings = append(result.Warnings, "NO_NO_ACTION_CODE")
	}

	return result
}

// GetNOActionCode 获取任务的 NO_ACTION 状态码
func (c *ConstraintChecker) GetNOActionCode(task *AgentTask) string {
	if task == nil {
		return "UNKNOWN"
	}
	return task.Contract.NOActionCode
}

// ShouldAllowNOAction 判断 NO_ACTION 是否为合法结果
func (c *ConstraintChecker) ShouldAllowNOAction(task *AgentTask, output interface{}) bool {
	if task == nil {
		return false
	}

	outputMap := NewOutputGuard().toMap(output)
	status, ok := outputMap["status"]
	if !ok {
		return false
	}

	return fmt.Sprintf("%v", status) == task.Contract.NOActionCode
}

// GetTaskBrief 获取任务简要信息（用于日志和调试）
func (c *ConstraintChecker) GetTaskBrief(task *AgentTask) string {
	if task == nil {
		return "NIL"
	}
	return fmt.Sprintf("[%s] %s | Agent: %s | Phase: %s | Priority: %s | Contract: %s | NO_ACTION: %s | Stop: %s",
		task.ID, task.TaskName, task.AgentRole, task.Phase, task.Priority,
		task.Contract.Purpose, task.Contract.NOActionCode, task.Contract.StopCondition)
}
