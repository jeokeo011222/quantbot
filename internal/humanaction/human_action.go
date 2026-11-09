// Package humanaction 定义结构化的人类问题（human_action）。
//
//	核心原则（来自 dsh-superagent philosophy）：
//	  - 一次只问一个具体问题
//	  - 结构化字段提问，拒绝从自由文本里猜"是不是在等人"
//	  - Executor 只负责把问题通过此字段交给上层，由上层决定如何呈现
//	  - 审批、追问、资源请求都是 HumanAction 的具体类型
//
//	这取代了之前决策日志/审批弹窗里随处可见的自由文本 reason 字段作为程序输入。
//	Reason string 仍然保留做人类可读兜底，但判断条件、路由、审计都走 ReasonSet。
package humanaction

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ActionType 人类动作请求类型。
type ActionType string

const (
	TypeApproveReject ActionType = "approve_or_reject" // 批准或拒绝（交易审批主路径）
	TypeDecision      ActionType = "decision"           // 二选一/多选一决策
	TypeConfirmation  ActionType = "confirmation"       // 确认/取消
	TypeResource      ActionType = "resource"           // 资源请求（凭证、权限、数据）
	TypePauseOrStop   ActionType = "pause_or_stop"      // 暂停/停止自动动作
	TypeClarification ActionType = "clarification"      // 语义澄清（AI 需要用户给出结构化输入）
)

// Option 选择题选项。
type Option struct {
	// Key 稳定键（程序识别用，不能改）
	Key string `json:"key"`
	// Label 中文标签（UI 显示）
	Label string `json:"label"`
	// Description 可选的详细解释
	Description string `json:"description,omitempty"`
}

// HumanAction 结构化的人类问题请求。
//
// 设计约束：
//  1. 一条 HumanAction 必须且只能携带一个具体问题（one question at a time）
//  2. 必须指定 Type，Type 决定 UI 渲染方式和接受的 Answer 形态
//  3. Question 是自然语言，但它只做展示，不被程序解析
//  4. Options 对选择题必填；对 confirmation 默认提供 [是, 否]
//  5. Priority 提示上层是否需要高亮（交易审批永远高优）
type HumanAction struct {
	// ActionID 唯一标识（幂等 + 跨轮次匹配回答用）
	ActionID string `json:"action_id"`

	// Type 问题类型
	Type ActionType `json:"type"`

	// Question 自然语言提问（展示用，禁止程序解析）
	Question string `json:"question"`

	// Options 选择题选项（对非选择题可为 nil）
	Options []Option `json:"options,omitempty"`

	// DefaultKey 推荐选项的 Key（默认方案，不是自动确认）
	DefaultKey string `json:"default_key,omitempty"`

	// Topic 归类标签（"trade_approval"/"risk_check"/"strategy_tuning"/...）
	Topic string `json:"topic,omitempty"`

	// Context 给 UI 展示的结构化上下文（如 symbol, amount, decision_id）
	Context map[string]interface{} `json:"context,omitempty"`

	// Priority 优先级：high=阻塞决策（必须等回答），low=非阻塞（后台等待）
	Priority string `json:"priority,omitempty"` // "high" | "low"

	// Source 发起者（agent role / executor / external source）
	Source string `json:"source,omitempty"`

	// RelatedDecisionID 关联的决策 ID（可空）
	RelatedDecisionID string `json:"related_decision_id,omitempty"`
}

// Answer 对 HumanAction 的结构化回答。
// 禁止从自由文本解析用户意图——必须走 Answer.Key 或 Answer.Text（后者仅用于 clarification 类型）。
type Answer struct {
	// ActionID 被回答的 HumanAction ID（必填，幂等匹配）
	ActionID string `json:"action_id"`

	// Key 选择的选项 key（选择题必填）
	Key string `json:"key,omitempty"`

	// Text 自由文本回答（仅 clarification / resource 类型允许）
	Text string `json:"text,omitempty"`

	// Confirmed 是否最终确认（用户确认弹窗）
	Confirmed bool `json:"confirmed,omitempty"`
}

// ---- 构建器 ----

// New 最简创建。
func New(actionID string, t ActionType, question string) HumanAction {
	return HumanAction{
		ActionID: actionID,
		Type:     t,
		Question: question,
	}
}

// TradeApproval 一键生成交易审批类型的 HumanAction。
// symbol/stockName/action/quantity/price/decisionID 都进 Context。
func TradeApproval(
	actionID, action, symbol, stockName string,
	quantity int, price float64,
	reasonCodes []string, decisionID string,
) HumanAction {
	return HumanAction{
		ActionID: actionID,
		Type:     TypeApproveReject,
		Question: fmt.Sprintf("请确认 %s %s %s %d 股 @ %.2f：%s",
			action, stockName, symbol, quantity, price,
			formatCodes(reasonCodes)),
		Options: []Option{
			{Key: "approved", Label: "批准", Description: "执行本次交易"},
			{Key: "rejected", Label: "拒绝", Description: "放弃本次交易"},
		},
		DefaultKey: "approved",
		Topic:      "trade_approval",
		Priority:   "high",
		Source:     "CIO",
		RelatedDecisionID: decisionID,
		Context: map[string]interface{}{
			"action":     action,
			"symbol":     symbol,
			"stock_name": stockName,
			"quantity":   quantity,
			"price":      price,
			"decision_id": decisionID,
			"reason_codes": reasonCodes,
			"amount":    float64(quantity) * price,
		},
	}
}

// Confirmation 生成二选一确认。
func Confirmation(actionID, question string, defaultYes bool) HumanAction {
	defaultKey := "no"
	if defaultYes {
		defaultKey = "yes"
	}
	return HumanAction{
		ActionID:  actionID,
		Type:      TypeConfirmation,
		Question:  question,
		Options:   []Option{
			{Key: "yes", Label: "是"},
			{Key: "no", Label: "否"},
		},
		DefaultKey: defaultKey,
		Priority:    "high",
	}
}

// ---- 辅助 ----

// Validate 静态校验：类型必填、选择题必须有 options、action_id 非空。
func (h HumanAction) Validate() error {
	if h.ActionID == "" {
		return fmt.Errorf("human_action.action_id is required")
	}
	if h.Type == "" {
		return fmt.Errorf("human_action.type is required")
	}
	switch h.Type {
	case TypeApproveReject, TypeDecision:
		if len(h.Options) == 0 {
			return fmt.Errorf("human_action.type=%s requires non-empty options", h.Type)
		}
	}
	return nil
}

// IsBlocking 是否阻塞决策（必须等人类回答）。
func (h HumanAction) IsBlocking() bool {
	return h.Priority == "high" || h.Type == TypeApproveReject
}

// ToJSON 序列化。
func (h HumanAction) ToJSON() ([]byte, error) {
	return json.Marshal(h)
}

func formatCodes(codes []string) string {
	if len(codes) == 0 {
		return ""
	}
	return "触发：" + strings.Join(codes, ", ")
}
