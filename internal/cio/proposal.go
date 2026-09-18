// 决策主张层：让 LLM 大模型在盘前决策时真正「参与决策」，而不只是对规则结果做末端否决。
//
// 设计原则（与 CIO-001 六维判势一致的「严禁伪造、真实数据」约束）：
//  1. LLM 主张输入全部来自真实数据（注入的市场状态/六维判势/组合快照/因子候选/回撤/风控），
//     并把这些采集到的证据随决策一并落库（CIODecision.Evidence），形成可审计证据链。
//  2. 规则风控门：LLM 的主张是「偏置/主张」，绝不覆盖规则硬约束。仓位系数取
//     min(六维判势仓位系数, clamp(LLM主张仓位, 0.2, 1.0)) —— 更严格的 Governor 永远获胜，
//     保证 LLM 最多只能「更保守」，不能越过后台的风控与可执行性规则。
//  3. 硬回退：LLM 未配置 / 调用失败 / 超时 / 输出无法解析 → 完全退回原规则决策，绝不让
//     LLM 单点故障阻断或扭曲盘前交易。
//
// 本层为单向增强，不影响盘中监控（盘中不经过 makeDecision），不影响策略卖出与回撤熔断等高优先级动作。
package cio

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/quantpilot/quantpilot/internal/factors"
	sixdim "github.com/quantpilot/quantpilot/internal/marketsixdim"
	agents "github.com/quantpilot/quantpilot/internal/port"
	"github.com/quantpilot/quantpilot/internal/portfolio"
)

// rankByPreferred 在因子排序前优先提升 LLM 偏好的标的（仅影响候选排序，不绕过后续风控/可执行性门）。
func rankByPreferred(list []factors.StockFactors, prefs []prefStock) []factors.StockFactors {
	if len(prefs) == 0 || len(list) == 0 {
		return list
	}
	set := make(map[string]bool, len(prefs))
	for _, p := range prefs {
		set[pureCode(p.Code)] = true
	}
	sort.SliceStable(list, func(i, j int) bool {
		pi, pj := set[pureCode(list[i].Code)], set[pureCode(list[j].Code)]
		if pi != pj {
			return pi
		}
		return list[i].CompositeScore > list[j].CompositeScore
	})
	return list
}

// pureCode 提取纯6位证券代码，兼容 sz600519/sh000300 等带前缀格式
func pureCode(code string) string {
	var b []byte
	for i := 0; i < len(code); i++ {
		if c := code[i]; c >= '0' && c <= '9' {
			b = append(b, c)
			if len(b) == 6 {
				break
			}
		}
	}
	return string(b)
}

// candidatesSummary 将多因子候选 top-N 压缩为 LLM 可读的一段证据文本
func candidatesSummary(list []factors.StockFactors, n int) string {
	if len(list) == 0 {
		return ""
	}
	if n > len(list) {
		n = len(list)
	}
	parts := make([]string, 0, n)
	for i := 0; i < n; i++ {
		parts = append(parts, fmt.Sprintf("%s(%.0f分,rank%d)",
			list[i].Code, list[i].CompositeScore, list[i].Rank))
	}
	return strings.Join(parts, ", ")
}

// maxTopCandidates 注入多因子候选的证据条数（控制 LLM 上下文长度）
const maxTopCandidates = 20

// portfolioEvidence 组合快照摘要（全部来自真实组合状态）
func portfolioEvidence(s *portfolio.PortfolioSnapshot) string {
	if s == nil {
		return "无"
	}
	return fmt.Sprintf("总资产=%.0f 现金=%.0f 持仓市值=%.0f 持仓数=%d 累计收益=%+.2f%%",
		s.Cash+s.TotalMarketValue, s.Cash, s.TotalMarketValue, len(s.Positions), s.TotalReturn)
}

// drawdownEvidence 净值回撤摘要
func drawdownEvidence(ddPct float64, ddLevel string) string {
	switch ddLevel {
	case "stop":
		return fmt.Sprintf("已触发止损熔断，回撤=%.2f%%", ddPct)
	case "warn":
		return fmt.Sprintf("已触发回撤预警，回撤=%.2f%%", ddPct)
	default:
		return "无回撤预警"
	}
}

// riskEvidence 风控报告摘要
func riskEvidence(r *RiskReport) string {
	if r == nil {
		return "无"
	}
	v := strings.Join(r.Violations, "；")
	if v == "" {
		v = "无违规"
	}
	return fmt.Sprintf("decision=%s 置信度=%.0f 违规=%s", r.Decision, r.Confidence*100, v)
}

// sixDimEvidence 六维判势摘要（真实仓位系数/总分/标签）
func sixDimEvidence(r *sixdim.MarketReport) string {
	if r == nil {
		return "无(DuckDB不可用)"
	}
	return fmt.Sprintf("总分=%.1f(修正%.1f) 冲突=%d 仓位系数=%.2f 标签=%s",
		r.RawTotalScore, r.AdjustedTotalScore, r.ConflictCount, r.PositionRate, r.MarketTag)
}

// marketConsensusEvidence A3 市场共识摘要：由六维判势确定性派生"方向/强度/分歧/次日待验证条件"。
// 供主张/辩论 Agent 在决策时参考共识与待验证条件；六维不可用时返回 "无"。
func marketConsensusEvidence(r *sixdim.MarketReport) string {
	c := ComputeMarketConsensus(r)
	if r == nil || c.Direction == "neutral" && len(c.Divergences) == 0 && len(c.ValidateTomorrow) == 0 {
		return "无"
	}
	parts := []string{"方向=" + c.Direction, "强度=" + fmt.Sprintf("%.2f", c.Strength), c.Rationale}
	if len(c.Divergences) > 0 {
		parts = append(parts, "分歧: "+strings.Join(c.Divergences, "; "))
	}
	if len(c.ValidateTomorrow) > 0 {
		parts = append(parts, "次日待验证: "+strings.Join(c.ValidateTomorrow, "; "))
	}
	return strings.Join(parts, " | ")
}

// proposalInput 决策主张 Agent 的注入上下文（全部来自真实数据） 系统提示词。
// 要求：先依据注入证据给出态度与仓位主张，再输出结构化 JSON；不得编造数据。
const proposalSystemPrompt = `你是A股量化基金的 CIO 决策主张官。系统会注入当天真实的市场状态、六维判势、组合快照、
多因子候选股票、回撤与风控信息作为证据链。你的职责是「基于证据给出带有个人判断的投资主张」，而不是机械复述规则。
请先给出你的态度判断（方向定性），再量化主张：
1. 市场方向（BULLISH/BEARISH/NEUTRAL）与一句话解读；
2. 目标权益仓位 target_position_rate（0~1，代表你建议买入订单使用的仓位系数；越看多可越接近1，越谨慎越接近0，但不得<0.2或>1）；
3. 你偏好的 1~5 只股票代码（仅从注入的候选池里选，须给出理由；可留空表示无偏好）;
4. confidence（0~100）代表你对自己判断的信心，以及 conclusion 总结。
要求：边看证据边推理，结论必须能对应到证据；数据不充分时宁可保守也不臆测。
必须严格只输出一个 JSON 对象，不要输出任何其他文字或代码块标记，格式：
{"market_direction":"BULLISH|BEARISH|NEUTRAL","market_read":"<一句话解读>","target_position_rate":0~1,"preferred_stocks":[{"code":"600519","reason":"<理由>"}],"confidence":0~100,"conclusion":"<不超过120字总结>"}`

// llmDecisionProposal LLM 决策主张结构化输出
type llmDecisionProposal struct {
	MarketDirection    string      `json:"market_direction"`
	MarketRead         string      `json:"market_read"`
	TargetPositionRate float64     `json:"target_position_rate"`
	PreferredStocks    []prefStock `json:"preferred_stocks"`
	Confidence         int         `json:"confidence"`
	Conclusion         string      `json:"conclusion"`
}

// prefStock 主张偏好个股
type prefStock struct {
	Code   string `json:"code"`
	Reason string `json:"reason"`
}

// decisionProposalPayload 随决策落库的主张+证据链负载
type decisionProposalPayload struct {
	MarketDirection       string      `json:"market_direction"`
	MarketRead            string      `json:"market_read"`
	SuggestedPositionRate float64     `json:"suggested_position_rate"`
	EffectivePositionRate float64     `json:"effective_position_rate"`
	GovernedBySixDim      bool        `json:"governed_by_sixdim"`
	PreferredStocks       []prefStock `json:"preferred_stocks"`
	Confidence            int         `json:"confidence"`
	Conclusion            string      `json:"conclusion"`
	// SkepticVerdict/SkepticConcerns 对抗评审（Devil's advocate 质疑者）意见；未启用时为 ""
	SkepticVerdict  string   `json:"skeptic_verdict,omitempty"`
	SkepticConcerns []string `json:"skeptic_concerns,omitempty"`
	// Debate 多空辩论结果（方向/多空论据/共识度）；未启用时为 nil
	Debate *debateVerdict `json:"debate,omitempty"`
	// KellyScale Kelly 仓位缩放系数 [0,1]；无辩论缓存时默认为 1.0（不缩放）
	KellyScale float64 `json:"kelly_position_scale,omitempty"`
	// Ensemble 多提案集成投票（多来源方向多数 + 净多分）；无可投票来源时为 nil
	Ensemble *ensembleVerdict `json:"ensemble,omitempty"`
	// Feedback 决策前注入的历史归因反馈（最近结算日真实收益），供审计查看影响输入的上下文
	Feedback    string   `json:"feedback,omitempty"`
	Evidence    []string `json:"evidence"`
	GeneratedAt string   `json:"generated_at"`
}

// genDecisionProposal 采集证据 → 调用 LLM 生成决策主张。
// 返回 nil 表示不可用/失败（调用方应完全回退规则，不作任何变更）。
func (c *CIOEngine) genDecisionProposal(ctx context.Context, proposalInput proposalInput) *llmDecisionProposal {
	if c.agent == nil || c.agent.LLM == nil {
		return nil
	}
	prompt := buildProposalPrompt(proposalInput)
	ctx2, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	resp, err := c.agent.LLM.Chat(ctx2, []agents.Message{
		{Role: "system", Content: proposalSystemPrompt},
		{Role: "user", Content: prompt},
	}, nil)
	if err != nil {
		log.Printf("[CIO] 决策主张跳过(LLM调用失败): %v", err)
		return nil
	}
	if len(resp.Choices) == 0 || strings.TrimSpace(resp.Choices[0].Message.Content) == "" {
		log.Printf("[CIO] 决策主张跳过(无输出)")
		return nil
	}
	prop := c.parseDecisionProposal(resp.Choices[0].Message.Content)
	if prop == nil {
		log.Printf("[CIO] 决策主张跳过(输出无法解析): %s", truncateStr(resp.Choices[0].Message.Content, 120))
		return nil
	}
	if prop.TargetPositionRate <= 0 {
		prop.TargetPositionRate = 1.0 // 未给出仓位主张时默认不干预
	}
	log.Printf("[CIO] 决策主张: 方向=%s 建议仓位=%.2f 偏好%d只 置信度=%d",
		prop.MarketDirection, prop.TargetPositionRate, len(prop.PreferredStocks), prop.Confidence)
	return prop
}

// proposalGateResult 决策主张层的门控结果：主张 + 对抗评审 + 多空辩论 + 多提案集成 + 证据链 + 规则风控门后的有效仓位系数。
type proposalGateResult struct {
	Proposal   *llmDecisionProposal // LLM 决策主张；nil 表示不可用/失败（完全回退规则）
	Skeptic    *skepticVerdict      // Phase2 对抗评审（质疑者）意见；nil 表示未启用/不可用
	Debate     *debateVerdict       // Phase3 多空辩论结果；nil 表示未启用/不可用
	KellyScale float64              // Phase4 Kelly 仓位缩放系数 [0,1]（Debate 可用时 <1，否则 1.0）
	Ensemble   *ensembleVerdict     // Phase5 多提案集成投票：多来源方向多数校验；nil 表示无可投票来源
	Evidence   []string             // 决策前采集的证据链（六维/组合快照/回撤/风控/候选/反馈）
	Effective  float64              // 规则风控门后的最终生效仓位系数（min 六维 vs 主张 vs 质疑 vs Kelly）
}

// skepticVerdict 对抗评审（Devil's advocate）输出：对主张给出挑剔性意见与更保守的建议仓位。
type skepticVerdict struct {
	Verdict       string   `json:"verdict"` // AGREE|CAUTION|RISK
	SuggestedRate float64  `json:"suggested_rate"`
	Concerns      []string `json:"concerns"`
	Summary       string   `json:"summary"`
}

// skepticSystemPrompt 对抗评审 Agent 系统提示词：故意持有异议立场，找主张的漏洞。
const skepticSystemPrompt = `你是A股量化基金的异议审查官（Devil's advocate）。决策主张官刚给出一个投资主张，你的职责是
「故意挑刺」：从风控集中度、情绪过热、流动性、追高、利好出尽、回撤韧性等角度找出该主张可能犯错的地方，并据此给出
一个更保守或更合理的建议仓位 suggested_rate（0~1）。不要无脑附和，也不要做人身否定；挑刺要落在具体风险点上。
要求：先定性 verdict（AGREE=无明显风险可认可 / CAUTION=存在可注意风险建议更保守 / RISK=存在明显风险强烈建议降仓），
再给 1~3 条具体 concerns（每条一句话），并给出你会执行的 suggested_rate。必须严格只输出一个 JSON 对象，格式：
{"verdict":"AGREE|CAUTION|RISK","suggested_rate":0~1,"concerns":["<风险1>","<风险2>"],"summary":"<不超过60字>"}`

// debateVerdict 多空辩论（Phase3）输出：同一标的/市场看多与看空双方论据 + 共识度。
type debateVerdict struct {
	Direction string   `json:"direction"` // BULLISH|BEARISH|NEUTRAL
	Bull      []string `json:"bull"`
	Bear      []string `json:"bear"`
	Agreement int      `json:"agreement"` // 0~100 多空共识度（0=完全分歧，100=完全一致）
	Summary   string   `json:"summary"`
}

// debateSystemPrompt 多空辩论裁决官系统提示词：就该主张同时摆出看多与看空论据，并给出共识度。
const debateSystemPrompt = `你是A股量化基金的辩论裁决官。决策主张官已给出一个投资主张，证据已注入。你的职责是「组织一场多空辩论」：
1. 站在多头 角度列出 1~3 条最有力的看多论据 bull（每条一句话、落在具体证据上）；
2. 站在空头 角度列出 1~3 条最有力的看空论据 bear（每条一句话、落在具体风险上）；
3. 给出方向 direction（BULLISH/BEARISH/NEUTRAL）与共识度 agreement（0~100：论据更偏一致则越高、分歧越大则越低）；
4. summary 一句话总结这场辩论的净结论。
要求：论据必须对应注入证据，不得编造；必须严格只输出一个 JSON 对象，不要输出任何其他文字，格式：
{"direction":"BULLISH|BEARISH|NEUTRAL","bull":["<多头论据1>","<多头论据2>"],"bear":["<空头论据1>","<空头论据2>"],"agreement":0~100,"summary":"<不超过80字>"}`

// kellyPositionScale 用「LLM 主张置信度 × 多空共识度」计算 Kelly 仓位缩放系数。
// certainty = conf/100 × agreement/100 ∈ [0,1]；scale = 0.4 + 0.6×certainty ∈ [0.4,1]。
// 只在信心或多空共识不足时下调买入仓位，绝不放大到超出规则门（符合「主张/异议只能更保守」原则）。
func kellyPositionScale(conf, agreement int) float64 {
	if conf <= 0 || agreement <= 0 {
		return 1.0
	}
	certainty := clampF(float64(conf)/100*float64(agreement)/100, 0, 1)
	return clampF(0.4+0.6*certainty, 0.4, 1.0)
}

// debateProposal 组织「多空辩论」；不可用/调用失败/解析失败 → 返回 nil（不影响门控）。
func (c *CIOEngine) debateProposal(ctx context.Context, in proposalInput, prop *llmDecisionProposal) *debateVerdict {
	if c.agent == nil || c.agent.LLM == nil || prop == nil {
		return nil
	}
	prompt := fmt.Sprintf(`【主张官给出的决策主张】
- 市场方向：%s
- 市场解读：%s
- 建议仓位系数：%.2f
- 偏好个股：%s
- 信心：%d/100
- 结论：%s

【对应证据】%s`,
		prop.MarketDirection, prop.MarketRead, prop.TargetPositionRate, prefStocksText(prop.PreferredStocks),
		prop.Confidence, prop.Conclusion, strings.Join(buildEvidenceLines(in), "\n"))

	ctx2, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	resp, err := c.agent.LLM.Chat(ctx2, []agents.Message{
		{Role: "system", Content: debateSystemPrompt},
		{Role: "user", Content: prompt},
	}, nil)
	if err != nil {
		log.Printf("[CIO] 多空辩论跳过(调用失败): %v", err)
		return nil
	}
	if len(resp.Choices) == 0 || strings.TrimSpace(resp.Choices[0].Message.Content) == "" {
		log.Printf("[CIO] 多空辩论跳过(无输出)")
		return nil
	}
	deb := c.parseDebateVerdict(resp.Choices[0].Message.Content)
	if deb == nil {
		log.Printf("[CIO] 多空辩论跳过(输出无法解析): %s", truncateStr(resp.Choices[0].Message.Content, 120))
		return nil
	}
	return deb
}

// parseDebateVerdict 解析多空辩论 JSON 输出；非法则返回 nil。
func (c *CIOEngine) parseDebateVerdict(content string) *debateVerdict {
	block := extractJSONBlock(content)
	if block == "" {
		return nil
	}
	var deb debateVerdict
	if err := json.Unmarshal([]byte(block), &deb); err != nil {
		return nil
	}
	if deb.Direction == "" || len(deb.Bull) == 0 && len(deb.Bear) == 0 {
		return nil
	}
	if deb.Agreement < 0 || deb.Agreement > 100 {
		deb.Agreement = 0
	}
	return &deb
}

func (c *CIOEngine) attachProposal(decision *agents.CIODecision, r *proposalGateResult, ruleRate float64, governedBySixDim bool) {
	if r == nil {
		return
	}
	suggested := ruleRate
	var dir, read, conclusion string
	var prefs []prefStock
	conf := 0
	if r.Proposal != nil {
		suggested = r.Proposal.TargetPositionRate
		dir = r.Proposal.MarketDirection
		read = r.Proposal.MarketRead
		conclusion = r.Proposal.Conclusion
		prefs = r.Proposal.PreferredStocks
		conf = r.Proposal.Confidence
	}
	var skVerdict string
	var skConcerns []string
	if r.Skeptic != nil {
		skVerdict = r.Skeptic.Verdict
		skConcerns = r.Skeptic.Concerns
	}
	decision.Evidence = decisionProposalPayload{
		MarketDirection:       dir,
		MarketRead:            read,
		SuggestedPositionRate: suggested,
		EffectivePositionRate: r.Effective,
		GovernedBySixDim:      governedBySixDim,
		PreferredStocks:       prefs,
		Confidence:            conf,
		Conclusion:            conclusion,
		SkepticVerdict:        skVerdict,
		SkepticConcerns:       skConcerns,
		Debate:                r.Debate,
		KellyScale:            r.KellyScale,
		Ensemble:              r.Ensemble,
		Feedback:              feedbackFromEvidence(r.Evidence),
		Evidence:              r.Evidence,
		GeneratedAt:           time.Now().Format(time.RFC3339),
	}

	// 主张结论注入决策理由，便于盘后归因与人工查看
	sb := strings.Builder{}
	if r.Proposal != nil {
		if r.Proposal.MarketRead != "" {
			sb.WriteString(fmt.Sprintf("【LLM主张】%s", r.Proposal.MarketRead))
		}
		if r.Proposal.Conclusion != "" {
			if sb.Len() > 0 {
				sb.WriteString("；")
			}
			sb.WriteString(r.Proposal.Conclusion)
		}
		if len(r.Proposal.PreferredStocks) > 0 {
			codes := make([]string, 0, len(r.Proposal.PreferredStocks))
			for _, p := range r.Proposal.PreferredStocks {
				codes = append(codes, p.Code)
			}
			sb.WriteString(fmt.Sprintf("（偏好: %s）", strings.Join(codes, "、")))
		}
		if r.Skeptic != nil {
			sb.WriteString(fmt.Sprintf("；【异议评审=%s】%s", r.Skeptic.Verdict, r.Skeptic.Summary))
		}
		if r.Debate != nil {
			sb.WriteString(fmt.Sprintf("；【多空辩论=%s 共识%d%%】%s", r.Debate.Direction, r.Debate.Agreement, r.Debate.Summary))
			if r.KellyScale > 0 && r.KellyScale < 1 {
				sb.WriteString(fmt.Sprintf("（Kelly仓位×%.2f）", r.KellyScale))
			}
		}
		if r.Ensemble != nil {
			sb.WriteString(fmt.Sprintf("；【多提案集成=%s 净多%.0f%%】", r.Ensemble.Direction, r.Ensemble.NetScore*100))
		}
	}
	if sb.Len() > 0 {
		if decision.Reason != "" {
			decision.Reason = decision.Reason + "\n" + sb.String()
		} else {
			decision.Reason = sb.String()
		}
	}
}

// feedbackFromEvidence 从证据链中提取「历史反馈」段，供落库展示
func feedbackFromEvidence(evidence []string) string {
	for _, l := range evidence {
		if strings.HasPrefix(l, "历史反馈:") {
			return strings.TrimPrefix(l, "历史反馈:")
		}
	}
	return ""
}

// decideProposalGate 决策主张层的统一入口：
//  1. 采集证据链（Phase1b 强制取证）；
//  2. LLM 生成决策主张；不可用/失败 → 完全回退规则（Effective=ruleRate）；
//  3. Phase2 对抗评审：质疑者挑刺，只能让结果更保守；
//  4. 规则风控门：有效仓位 = min(ruleRate, clamp(主张,0.2,1), clamp(质疑,0.2,1))，绝不让 LLM 越过后台风控。
//
// ruleRate 为规则层给出的仓位基准（六维判势 position_rate；六维不可用时为 1.0）。
func (c *CIOEngine) decideProposalGate(ctx context.Context, in proposalInput, ruleRate float64) *proposalGateResult {
	evidence := buildEvidenceLines(in)
	res := &proposalGateResult{Evidence: evidence, Effective: ruleRate, KellyScale: 1.0}

	prop := c.genDecisionProposal(ctx, in) // Phase1 主张
	if prop == nil {
		log.Printf("[CIO] 决策主张不可用，完全回退规则决策（有效仓位=%.2f）", ruleRate)
		return res
	}
	res.Proposal = prop

	gate := math.Min(ruleRate, clampF(prop.TargetPositionRate, 0.2, 1.0)) // 规则风控门：主张只能更保守

	if ver := c.challengeProposal(ctx, in, prop); ver != nil { // Phase2 对抗评审
		res.Skeptic = ver
		gate = math.Min(gate, clampF(ver.SuggestedRate, 0.2, 1.0)) // 质疑者也只能更保守，绝不突破规则下限
		log.Printf("[CIO] 异议评审=%s 建议仓位=%.2f 质疑%d条: %v", ver.Verdict, ver.SuggestedRate, len(ver.Concerns), ver.Concerns)
	}

	// Phase3 多空辩论 + Phase4 Kelly 缩放：共识度/信心不足时进一步下调买入仓位。
	// Kelly 只可能让仓位更保守（≤1），绝不放大到越过已有规则门。
	res.KellyScale = 1.0
	if db := c.debateProposal(ctx, in, prop); db != nil {
		res.Debate = db
		res.KellyScale = math.Min(1.0, kellyPositionScale(prop.Confidence, db.Agreement))
		gate = math.Min(gate, res.KellyScale) // 共识不足 → 整体仓位更容易被下调；已受 clamp 下限保护
		log.Printf("[CIO] 多空辩论=%s 共识=%d 论据(多%d/空%d) Kelly缩放=%.2f", db.Direction, db.Agreement, len(db.Bull), len(db.Bear), res.KellyScale)
	}

	// Phase5 多提案集成投票：汇总主张/异议/辩论/规则对方向的多数，作为对单一主张方向的稳健校验。
	// 规则：仅当净多分为负（整体偏空）时进一步收敛仓位，且只降不升，绝不放大地越过既有规则门。
	res.Ensemble = ensembleVote(prop, res.Skeptic, res.Debate, in.Regime)
	if res.Ensemble != nil {
		if res.Ensemble.NetScore < 0 {
			if res.Ensemble.NetScore <= -0.5 {
				gate = math.Min(gate, 0.5) // 明显偏空 → 仓位收敛到 ≤0.5
			} else {
				gate = math.Min(gate, 0.75) // 轻度偏空 → 仓位收敛到 ≤0.75
			}
		}
		log.Printf("[CIO] 多提案集成=%s 净多=%.2f 来源=%v", res.Ensemble.Direction, res.Ensemble.NetScore, res.Ensemble.Votes)
	} else {
		log.Printf("[CIO] 多提案集成跳过（无可投票来源）")
	}

	res.Effective = gate
	log.Printf("[CIO] 主张门控: 规则=%.3f 主张=%.2f 异议=%.2f 辩论=%.2f 集成=%.2f 生效=%.3f",
		ruleRate, prop.TargetPositionRate, skepticRate(res.Skeptic), res.KellyScale, ensembleScore(res.Ensemble), res.Effective)
	return res
}

// ensembleScore 返回集成净多分（负数表示整体偏空）；无可投票来源时为 -1 用于日志展示。
func ensembleScore(v *ensembleVerdict) float64 {
	if v == nil {
		return -1
	}
	return v.NetScore
}

func skepticRate(v *skepticVerdict) float64 {
	if v == nil {
		return -1
	}
	return v.SuggestedRate
}

// challengeProposal 调用对抗评审 Agent 对主张挑刺一轮；不可用/失败 → 返回 nil（不影响门控）。
func (c *CIOEngine) challengeProposal(ctx context.Context, in proposalInput, prop *llmDecisionProposal) *skepticVerdict {
	if c.agent == nil || c.agent.LLM == nil || prop == nil {
		return nil
	}
	prompt := fmt.Sprintf(`【主张官给出的决策主张】
- 市场方向：%s
- 市场解读：%s
- 建议仓位系数：%.2f
- 偏好个股：%s
- 信心：%d/100
- 结论：%s

【对应证据】%s
请从异议角度审查上述主张，找出可能冒进或忽略的风险，并给出你的最终建议仓位。`,
		prop.MarketDirection, prop.MarketRead, prop.TargetPositionRate, prefStocksText(prop.PreferredStocks),
		prop.Confidence, prop.Conclusion, strings.Join(buildEvidenceLines(in), "\n"))

	ctx2, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	resp, err := c.agent.LLM.Chat(ctx2, []agents.Message{
		{Role: "system", Content: skepticSystemPrompt},
		{Role: "user", Content: prompt},
	}, nil)
	if err != nil {
		log.Printf("[CIO] 异议评审跳过(调用失败): %v", err)
		return nil
	}
	if len(resp.Choices) == 0 || strings.TrimSpace(resp.Choices[0].Message.Content) == "" {
		log.Printf("[CIO] 异议评审跳过(无输出)")
		return nil
	}
	ver := c.parseSkepticVerdict(resp.Choices[0].Message.Content)
	if ver == nil {
		log.Printf("[CIO] 异议评审跳过(输出无法解析): %s", truncateStr(resp.Choices[0].Message.Content, 120))
		return nil
	}
	return ver
}

func prefStocksText(prefs []prefStock) string {
	if len(prefs) == 0 {
		return "无偏好"
	}
	parts := make([]string, 0, len(prefs))
	for _, p := range prefs {
		parts = append(parts, fmt.Sprintf("%s(%s)", p.Code, p.Reason))
	}
	return strings.Join(parts, "；")
}

// parseSkepticVerdict 解析对抗评审 JSON 输出；非法则返回 nil。
func (c *CIOEngine) parseSkepticVerdict(content string) *skepticVerdict {
	block := extractJSONBlock(content)
	if block == "" {
		return nil
	}
	var v skepticVerdict
	if err := json.Unmarshal([]byte(block), &v); err != nil {
		return nil
	}
	switch strings.ToUpper(v.Verdict) {
	case "AGREE", "CAUTION", "RISK":
		v.Verdict = strings.ToUpper(v.Verdict)
	default:
		v.Verdict = "CAUTION"
	}
	if v.SuggestedRate <= 0 {
		v.SuggestedRate = 1.0
	}
	return &v
}

// buildEvidenceLines 将主张输入上下文整理为证据链文本（Phase1b 强制取证后随决策落库）。
func buildEvidenceLines(in proposalInput) []string {
	lines := []string{"日期: " + orEmpty(in.Date, "-")}
	if s := strings.TrimSpace(in.SixDim); s != "" {
		lines = append(lines, "六维判势: "+s)
	}
	if s := strings.TrimSpace(in.Portfolio); s != "" {
		lines = append(lines, "组合快照: "+s)
	}
	if s := strings.TrimSpace(in.Drawdown); s != "" {
		lines = append(lines, "回撤: "+s)
	}
	if s := strings.TrimSpace(in.Risk); s != "" {
		lines = append(lines, "风控: "+s)
	}
	if s := strings.TrimSpace(in.Candidates); s != "" {
		lines = append(lines, "因子候选: "+s)
	}
	if s := strings.TrimSpace(in.Feedback); s != "" {
		lines = append(lines, "历史反馈: "+s)
	}
	return lines
}

// proposalInput 决策主张 Agent 的注入上下文（全部来自真实数据）
type proposalInput struct {
	Date       string
	Regime     string
	Confidence float64
	Portfolio  string // 组合快照摘要
	Drawdown   string // 回撤等级摘要
	Risk       string // 风控摘要
	SixDim     string // 六维判势摘要（可为空）
	Candidates string // 多因子候选 top-N 摘要
	Feedback   string // Phase3 历史归因反馈（最近结算日真实收益）
	Consensus  string // A3 市场共识/分歧/次日待验证条件摘要（可为空）
}

// buildProposalPrompt 将证据拼装为输入提示
func buildProposalPrompt(in proposalInput) string {
	return fmt.Sprintf(`【当前日期】%s
【市场状态】regime=%s 置信度=%.2f
【六维判势】%s
【组合快照】%s
【回撤】%s
【风控】%s
【多因子候选股票】%s
【市场共识与次日待验证条件】%s
请基于以上真实证据，给出你的决策主张（方向、建议仓位、偏好个股、信心），只输出 JSON。`,
		in.Date, in.Regime, in.Confidence, orEmpty(in.SixDim, "无"),
		orEmpty(in.Portfolio, "无"), orEmpty(in.Drawdown, "无"), orEmpty(in.Risk, "无"),
		orEmpty(in.Candidates, "无"), orEmpty(in.Consensus, "无"))
}

func orEmpty(s, d string) string {
	if strings.TrimSpace(s) == "" {
		return d
	}
	return s
}

// parseDecisionProposal 解析 LLM 输出的主张 JSON；非法则返回 nil
func (c *CIOEngine) parseDecisionProposal(content string) *llmDecisionProposal {
	block := extractJSONBlock(content)
	if block == "" {
		return nil
	}
	var p llmDecisionProposal
	if err := json.Unmarshal([]byte(block), &p); err != nil {
		return nil
	}
	// 规范化方向枚举
	switch strings.ToUpper(p.MarketDirection) {
	case "BULLISH", "BEARISH", "NEUTRAL":
		p.MarketDirection = strings.ToUpper(p.MarketDirection)
	default:
		p.MarketDirection = "NEUTRAL"
	}
	if p.Confidence < 0 {
		p.Confidence = 0
	}
	if p.Confidence > 100 {
		p.Confidence = 100
	}
	return &p
}

// lastDecisionFeedback Phase3 归因反馈：读取最近结算日的组合真实收益（PortfolioDailyStat），
// 生成一段「历史战绩归因」注入下一次决策主张的上下文，使 LLM 能参考自己近期判断的实际效果并自适应调整。
// 数据全部来自真实结算记录，读不到时返回空串（不影响决策）。
func (c *CIOEngine) lastDecisionFeedback() string {
	if c.portfolio == nil {
		return ""
	}
	hist, err := c.portfolio.GetProfitHistory(6)
	if err != nil {
		log.Printf("[CIO] 读取归因反馈失败(忽略): %v", err)
		return ""
	}
	if len(hist) == 0 {
		return ""
	}
	last := hist[len(hist)-1]
	totalReturn, _ := last["totalReturn"].(float64)
	lastDaily, _ := last["dailyReturn"].(float64)
	lastAssets, _ := last["totalAssets"].(float64)
	firstTotal, _ := hist[0]["totalReturn"].(float64)

	return fmt.Sprintf("最近%d个结算日区间累计收益%+.2f%%, 期末最近一日收益%+.2f%%, 期末总资产%.2f",
		len(hist), totalReturn-firstTotal, lastDaily, lastAssets)
}

// extractJSONBlock 从文本中提取首个完整 JSON 对象（容忍 ```json 代码块包裹及前后多余文字）。
func extractJSONBlock(s string) string {
	text := strings.TrimSpace(s)
	start := strings.IndexByte(text, '{')
	if start < 0 {
		return ""
	}
	text = text[start:]
	depth := 0
	inStr := false
	escaped := false
	for i := 0; i < len(text); i++ {
		ch := text[i]
		if inStr {
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == '"' {
				inStr = false
			}
			continue
		}
		switch ch {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return text[:i+1]
			}
		}
	}
	return ""
}
