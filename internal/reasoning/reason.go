// Package reasoning 提供结构化原因码体系：
//
//	每个决策/风控事件/审批请求的原因 = code + values + 可选文本解释。
//	面板按 code 渲染多语言文案，禁止从自由文本里解析判断条件。
//
// 设计借鉴 dsh-superagent 的 Reasons 体系（src/core/reasons.ts）：
// 核心原则：
//  1. 每条 reason 必须有稳定 code，不能只有自由文本
//  2. values 带结构化数据（阈值、实际值、标的），用于审计复盘
//  3. 自由文本仅做兜底展示，不作为程序输入
//  4. 一个决策可以同时携带多条 reason（多因触发）
package reasoning

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// ReasonCode 稳定原因码 —— 全系统唯一、可被审计追踪、可被前端渲染多语言。
// 新增 code 必须同步更新 CodeLabels 映射表。
type ReasonCode string

const (
	// ==== 决策原因 ====
	// Alpha 信号
	CodeAlphaBuy             ReasonCode = "ALPHA_BUY"             // AlphaScore 超买入阈值
	CodeAlphaSell            ReasonCode = "ALPHA_SELL"            // AlphaScore 超卖出阈值
	CodeAlphaWeak            ReasonCode = "ALPHA_WEAK"            // AlphaScore 未达阈值 → HOLD
	CodeSignalStrong         ReasonCode = "SIGNAL_STRONG"         // 信号强度加分
	CodeSignalWeak           ReasonCode = "SIGNAL_WEAK"           // 信号强度不够

	// 风控拦截
	CodeMaxDrawdownExceeded  ReasonCode = "MAX_DRAWDOWN_EXCEEDED" // 回撤超限
	CodeVaRHigh              ReasonCode = "VAR_HIGH"              // VaR 偏高
	CodeHighVolatility       ReasonCode = "HIGH_VOLATILITY"       // 波动率过高
	CodeLowLiquidity         ReasonCode = "LOW_LIQUIDITY"         // 流动性不足
	CodeHighConcentration    ReasonCode = "HIGH_CONCENTRATION"    // 集中度过高
	CodeRiskStatusCritical   ReasonCode = "CRITICAL_RISK_STATUS"  // 风控状态 CRITICAL → 暂停

	// 投资指引 / Mandate 限制
	CodeMandateDrawdown      ReasonCode = "MANDATE_DRAWDOWN"      // 回撤约束
	CodeMandateRiskLevel     ReasonCode = "MANDATE_RISK_LEVEL"    // 风险等级不匹配
	CodeMandateCashRatio     ReasonCode = "MANDATE_CASH_RATIO"    // 现金比例约束
	CodeMandateSectorLimit   ReasonCode = "MANDATE_SECTOR_LIMIT"  // 行业集中度约束
	CodeMandateSinglePos     ReasonCode = "MANDATE_SINGLE_POS"    // 单标的仓位上限
	CodeOverallRiskTooHigh   ReasonCode = "OVERALL_RISK_TOO_HIGH" // 组合整体风险超阈值 → BUY 降级 WAIT

	// 执行侧
	CodeApprovedByUser       ReasonCode = "APPROVED_BY_USER"      // 用户审批通过
	CodeRejectedByUser       ReasonCode = "REJECTED_BY_USER"      // 用户审批拒绝
	CodeApprovalTimeout      ReasonCode = "APPROVAL_TIMEOUT"      // 审批超时（排队中）
	CodeExpiredAtClose       ReasonCode = "EXPIRED_AT_CLOSE"      // 15:00 收盘未确认 → 失败
	CodeExecutionFailed      ReasonCode = "EXECUTION_FAILED"      // 执行失败（涨停/跌停/资金）
	CodeBelowConfidence      ReasonCode = "BELOW_CONFIDENCE"      // 置信度过低 → 建议谨慎
	CodeAboveTargetWeight    ReasonCode = "ABOVE_TARGET_WEIGHT"  // 目标权重偏高 → 集中度风险

	// 熔断护栏
	CodeSellGuardDedup       ReasonCode = "SELL_GUARD_DEDUP"      // 同日去重：已卖出过该股
	CodeSellGuardHysteresis  ReasonCode = "SELL_GUARD_HYSTERESIS" // 滞回灰区：盈亏未到确认区
	CodeSellGuardStreak      ReasonCode = "SELL_GUARD_STREAK"     // 连续 N 次确认未满足
	CodeDrawdownWarn         ReasonCode = "DRAWDOWN_WARN"         // 回撤触及预警线
	CodeDrawdownStop         ReasonCode = "DRAWDOWN_STOP"         // 回撤触及熔断线

	// 通用
	CodeDuplicate            ReasonCode = "DUPLICATE"             // 重复事件去重
	CodeRetry                ReasonCode = "RETRY"                 // 重试后成功
	CodeManualOverride       ReasonCode = "MANUAL_OVERRIDE"       // 人工覆盖自动决策
	CodeUnknown              ReasonCode = "UNKNOWN"               // 兜底：无结构化码
)

// CodeLabels code → 中文标签（前端渲染 / 日志展示用）。
// 新增 code 必须在此加一行。
var CodeLabels = map[ReasonCode]string{
	CodeAlphaBuy:             "AlphaScore 触发买入阈值",
	CodeAlphaSell:            "AlphaScore 触发卖出阈值",
	CodeAlphaWeak:            "AlphaScore 未达阈值（持有观望）",
	CodeSignalStrong:         "信号强度加分",
	CodeSignalWeak:           "信号强度不足",
	CodeMaxDrawdownExceeded:  "组合回撤超过投资指引上限",
	CodeVaRHigh:              "VaR99 偏高",
	CodeHighVolatility:       "波动率过高",
	CodeLowLiquidity:         "流动性不足",
	CodeHighConcentration:    "集中度风险过高",
	CodeRiskStatusCritical:   "风控状态 CRITICAL",
	CodeMandateDrawdown:      "投资指引回撤约束",
	CodeMandateRiskLevel:     "投资指引风险等级不匹配",
	CodeMandateCashRatio:     "投资指引现金比例约束",
	CodeMandateSectorLimit:   "投资指引行业集中度约束",
	CodeMandateSinglePos:     "投资指引单标的仓位上限",
	CodeOverallRiskTooHigh:   "组合整体风险超阈值（买入降级为等待）",
	CodeApprovedByUser:       "用户审批通过",
	CodeRejectedByUser:       "用户审批拒绝",
	CodeApprovalTimeout:      "用户审批超时（订单排队中）",
	CodeExpiredAtClose:       "15:00 收盘未获确认",
	CodeExecutionFailed:      "交易执行失败",
	CodeBelowConfidence:      "置信度偏低，建议谨慎操作",
	CodeAboveTargetWeight:    "目标权重偏高，注意集中度风险",
	CodeSellGuardDedup:       "卖出护栏：同日去重（该股今日已卖出）",
	CodeSellGuardHysteresis:  "卖出护栏：滞回灰区（盈亏未进入确认区）",
	CodeSellGuardStreak:      "卖出护栏：连续 N 次确认未满足",
	CodeDrawdownWarn:         "净值回撤触及预警线",
	CodeDrawdownStop:         "净值回撤触及熔断线",
	CodeDuplicate:            "重复事件已去重",
	CodeRetry:                "重试后成功",
	CodeManualOverride:       "人工覆盖自动决策",
	CodeUnknown:              "未知原因（请查看原始文本）",
}

// Reason 单条结构化原因。
type Reason struct {
	// Code 稳定原因码；必须是已知常量或 "UNKNOWN"
	Code ReasonCode `json:"code"`
	// Values 结构化数据：阈值、实际值、标的、规则版本…… 用于审计复盘。
	// 例：{"threshold":0.15,"actual":0.18,"symbol":"600519"}
	Values map[string]interface{} `json:"values,omitempty"`
	// Text 可选的自由文本解释；仅做兜底展示，不做程序输入。
	Text string `json:"text,omitempty"`
}

// ReasonSet 有序原因集合。一个决策可同时带多条原因（多因触发）。
type ReasonSet []Reason

// NewReason 创建一条结构化原因的最简形态（仅 code）。
func NewReason(code ReasonCode) Reason {
	return Reason{Code: code}
}

// NewReasonValues 创建带 values 的结构化原因。
func NewReasonValues(code ReasonCode, values map[string]interface{}) Reason {
	return Reason{Code: code, Values: values}
}

// NewReasonText 创建带自由文本的原因（兜底用，程序不应依赖 text 字段做判断）。
func NewReasonText(code ReasonCode, text string) Reason {
	return Reason{Code: code, Text: text}
}

// Build 链式构建器，方便一行写多条原因。
//
//	rs := Build(CodeAlphaBuy).V("symbol","600519").V("threshold",0.15).R(CodeSignalStrong).Done()
type ReasonBuilder struct {
	rs ReasonSet
}

func Build(code ReasonCode) *ReasonBuilder {
	return &ReasonBuilder{rs: ReasonSet{{Code: code}}}
}

// V 给最后一条 reason 追加一个 values 键值对。
func (b *ReasonBuilder) V(key string, val interface{}) *ReasonBuilder {
	if len(b.rs) > 0 {
		last := b.rs[len(b.rs)-1]
		if last.Values == nil {
			last.Values = make(map[string]interface{})
		}
		last.Values[key] = val
		b.rs[len(b.rs)-1] = last
	}
	return b
}

// T 给最后一条 reason 设置兜底自由文本。
func (b *ReasonBuilder) T(text string) *ReasonBuilder {
	if len(b.rs) > 0 {
		b.rs[len(b.rs)-1].Text = text
	}
	return b
}

// R 追加一条新 reason（仅 code），后续 V/T 作用于这条新的。
func (b *ReasonBuilder) R(code ReasonCode) *ReasonBuilder {
	b.rs = append(b.rs, Reason{Code: code})
	return b
}

// Done 结束构建，返回 ReasonSet。
func (b *ReasonBuilder) Done() ReasonSet {
	return b.rs
}

// ---- ReasonSet 辅助方法 ----

// HasCode 判断集合中是否包含某 code。
func (rs ReasonSet) HasCode(code ReasonCode) bool {
	for _, r := range rs {
		if r.Code == code {
			return true
		}
	}
	return false
}

// Filter 只保留指定 code 的 reason。
func (rs ReasonSet) Filter(codes ...ReasonCode) ReasonSet {
	allowed := make(map[ReasonCode]bool, len(codes))
	for _, c := range codes {
		allowed[c] = true
	}
	var out ReasonSet
	for _, r := range rs {
		if allowed[r.Code] {
			out = append(out, r)
		}
	}
	return out
}

// Labels 把全部 reason 转为人类可读的标签列表（去重、保序）。
func (rs ReasonSet) Labels() []string {
	seen := make(map[ReasonCode]bool)
	var labels []string
	for _, r := range rs {
		if seen[r.Code] {
			continue
		}
		seen[r.Code] = true
		labels = append(labels, LabelOf(r.Code))
	}
	return labels
}

// JoinLabels 用分隔符拼接所有 code 的中文标签。
func (rs ReasonSet) JoinLabels(sep string) string {
	return strings.Join(rs.Labels(), sep)
}

// String 实现 fmt.Stringer：按 code 排序后拼成可读行，适合日志。
func (rs ReasonSet) String() string {
	sorted := make(ReasonSet, len(rs))
	copy(sorted, rs)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Code < sorted[j].Code })
	var parts []string
	for _, r := range sorted {
		label := LabelOf(r.Code)
		if len(r.Values) > 0 {
			parts = append(parts, fmt.Sprintf("%s(%s)", label, formatValues(r.Values)))
		} else {
			parts = append(parts, label)
		}
	}
	return strings.Join(parts, " | ")
}

// MarshalText / UnmarshalText 让 ReasonSet 在 JSON 中既可存对象数组也可存纯 code 字符串数组。
// 但为了简洁我们只输出对象数组；前端按 code 渲染。
func (rs ReasonSet) MarshalJSON() ([]byte, error) {
	if len(rs) == 0 {
		return []byte("null"), nil
	}
	type alias ReasonSet
	return json.Marshal(alias(rs))
}

// LabelOf 查询某 code 的中文标签；未知码返回其字符串形式。
func LabelOf(code ReasonCode) string {
	if lbl, ok := CodeLabels[code]; ok {
		return lbl
	}
	return string(code)
}

// ---- Pairs：快速构建多条 code-only 的集合 ----

// Pairs 把 [code1, code2, ...] 转为 ReasonSet，每条仅带 code。
func Pairs(codes ...ReasonCode) ReasonSet {
	rs := make(ReasonSet, 0, len(codes))
	for _, c := range codes {
		rs = append(rs, Reason{Code: c})
	}
	return rs
}

func formatValues(m map[string]interface{}) string {
	// 简单 JSON 化 values 展示；生产可替换为更紧凑的 KV 拼接
	b, _ := json.Marshal(m)
	return string(b)
}
