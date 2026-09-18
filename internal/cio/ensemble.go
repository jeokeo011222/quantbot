// 多提案集成投票（对齐 AgentQuant v5 的多智能体投票思想，收敛为一个轻量实现）。
//
// 背景：决策主张层此前是单向门控——主张官提主张，异议评审、多空辩论逐级「只能更保守」地
// 收敛仓位。但市场方向的最终判定仍高度依赖主张官单一来源，缺少「把多个独立视角对方向
// 的表态合并成一个稳健多数方向」的机制。
//
// 本文件为决策层新增一个「方向集成投票」环节：把决策过程中多个独立来源——主张官、异议
// 评审（多空辩论）、规则/六维 regime 信号——各自对市场方向（BULLISH/NEUTRAL/BEARISH）
// 的表态折成净多分，再取多数方向，作为对单一主张方向的稳健校验。
//
// 设计原则（与 proposal.go 一致）：
//  1. 只降不升：集成结果只用于「方向校验 + 更保守的仓位收敛」，绝不放大到越过既有规则风控门；
//  2. 可审计：每个来源的表态、净多分、最终方向随决策一并落库，供盘后追因；
//  3. 可降级：任一来源缺失（LLM 不可用）时按剩余来源投票，永不因单点缺失而阻断决策。
package cio

import (
	"fmt"
	"math"
	"strings"
)

// ensembleVerdict 多提案集成投票结果：各来源方向表态 + 净多分 + 多数方向。
type ensembleVerdict struct {
	Direction string            `json:"direction"` // BULLISH|BEARISH|NEUTRAL（净多分归一后的多数方向）
	Votes     map[string]string `json:"votes"`     // 来源 → 表态：{主张:BULLISH, 异议:RISK, 辩论:NEUTRAL, 规则:NEUTRAL}
	NetScore  float64           `json:"net_score"` // 净多分 [-1,1]：正=整体偏多，0=多空均势，负=整体偏空
	Summary   string            `json:"summary"`   // 一句话解释净多分如何收敛出方向
}

// dirScoreStr 把单一来源的方向表态折成数值分：BULLISH=+1，NEUTRAL=0，BEARISH=-1。
func dirScoreStr(d string) float64 {
	switch strings.ToUpper(d) {
	case "BULLISH":
		return 1
	case "BEARISH":
		return -1
	default:
		return 0
	}
}

// ensembleVote 把决策过程中各独立来源对市场方向的表态折成净多分，取多数方向。
//
//   - prop: 主张官方向（按 confidence 加权，信心越高越加重该方向的分量）
//   - sk:   异议评审 verdict（AGREE 偏多、CAUTION 偏空阈小、RISK 明显偏空）
//   - db:   多空辩论方向（按 agreement 加权）
//   - regime: 六维判势标签，用气象词做规则信号（强势→偏多、退潮/弱势→偏空）
//
// 任一来源为 nil 都照常参与剩余项投票，永不 panic、永不阻断。
func ensembleVote(prop *llmDecisionProposal, sk *skepticVerdict, db *debateVerdict, regime string) *ensembleVerdict {
	votes := make(map[string]string)
	net := 0.0

	if prop != nil {
		votes["主张"] = prop.MarketDirection
		net += dirScoreStr(prop.MarketDirection) * clampF(float64(prop.Confidence)/100, 0, 1)
	}

	if sk != nil {
		st := "CAUTION"
		switch strings.ToUpper(sk.Verdict) {
		case "AGREE":
			st = "AGREE"
			net += 0.3
		case "RISK":
			st = "RISK"
			net -= 0.7
		default:
			st = "CAUTION"
			net -= 0.2
		}
		votes["异议"] = st
	}

	if db != nil {
		votes["辩论"] = db.Direction
		net += dirScoreStr(db.Direction) * clampF(float64(db.Agreement)/100, 0, 1)
	}

	if rt := strings.TrimSpace(regime); rt != "" {
		rule := "NEUTRAL"
		if strings.Contains(rt, "强势") {
			rule = "BULLISH"
			net += 0.3
		} else if strings.Contains(rt, "退潮") || strings.Contains(rt, "弱势") {
			rule = "BEARISH"
			net -= 0.3
		}
		votes["规则"] = rule
	}

	net = clampF(net, -1, 1)
	dir := "NEUTRAL"
	if net > 0.25 {
		dir = "BULLISH"
	} else if net < -0.25 {
		dir = "BEARISH"
	}
	summary := fmt.Sprintf("多来源集成净多=%.0f%%，方向=%s（来源:%d）",
		net*100, dir, len(votes))

	return &ensembleVerdict{Direction: dir, Votes: votes, NetScore: math.Round(net*100) / 100, Summary: summary}
}