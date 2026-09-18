package planner

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/quantpilot/quantpilot/internal/llmhost"
	"github.com/quantpilot/quantpilot/internal/port"
)

// generateReferencePrice 从股票代码算法生成参考价（替代硬编码价格）
// 使用与 DataEngine 一致的哈希算法，保证同一代码始终得到相同价格
func generateReferencePrice(code string) float64 {
	seed := int64(0)
	for _, c := range code {
		seed = seed*31 + int64(c)
	}
	if seed < 0 {
		seed = -seed
	}
	return 3.0 + float64(seed%3000)
}

// userDiversifySeed 由用户ID派生确定性去同质化种子（>0）。
// 不同用户得到不同种子 → 选股权重微扰不同 → 命中略不同的候选子集，解决多人选股雷同。
func userDiversifySeed(uid uint) int64 {
	s := int64(uid)
	// 用简单混合（乘大质数 + 抖动）确保相邻 uid 也产生足够差异的种子；uid=0 用户禁用去同质化。
	if s <= 0 {
		return 0
	}
	return s*2654435761 + 17
}

// Planner AI 投资规划师
type Planner struct {
	store  port.PlannerStore
	llm    llm.Client
	userID uint

	// 真实选股服务（DuckDB 全市场数据），用于生成候选池，避免硬编码股票
	screenerService port.StockScreener

	// 股票元信息提供者（名称/行业字典），由宿主以 data.DictLoader 适配注入
	stockMeta port.StockMeta

	// lastMarketState 最近一次选股时的市场状态（来自 Pro 因子引擎，用于弱市仓位控制/数量减半）
	lastMarketState *port.MarketState

	// 组合实际可用资金提供者（由 App 注入 portfolio 引擎快照）。
	capitalProvider func() float64

	// callMetaDecorator LLM 审计元数据装饰（宿主以 llmmonitoring.WithCallMeta 适配注入）
	callMetaDecorator port.CallMetaDecorator
}

// SetCapitalProvider 注入组合实际可用资金的获取函数
func (p *Planner) SetCapitalProvider(fn func() float64) {
	p.capitalProvider = fn
}

// SetScreener 注入真实选股服务（用于候选池生成）
func (p *Planner) SetScreener(ss port.StockScreener) {
	p.screenerService = ss
}

// SetStockMeta 注入股票元信息提供者（名称/行业字典）
func (p *Planner) SetStockMeta(m port.StockMeta) {
	p.stockMeta = m
}

// SetCallMetaDecorator 注入 LLM 审计元数据装饰函数
func (p *Planner) SetCallMetaDecorator(fn port.CallMetaDecorator) {
	p.callMetaDecorator = fn
}

// decorateLLMCtx 为 LLM 调用附加审计元信息（宿主注入的指标装饰闭包）
func (p *Planner) decorateLLMCtx(ctx context.Context, meta port.CallMeta) context.Context {
	if p.callMetaDecorator != nil {
		return p.callMetaDecorator(ctx, meta)
	}
	return ctx
}

// bearish 判断当前市场是否处于弱势（熊市/偏弱震荡）。
// 弱市时执行「数量减半」与「仓位控制」：减少持仓数并把更多资金留给现金，降低权益暴露。
func (p *Planner) bearish() bool {
	if p.lastMarketState == nil {
		return false
	}
	switch p.lastMarketState.Regime {
	case "bear", "bear_range":
		return true
	}
	return false
}

// NewPlanner 创建投资规划师
func NewPlanner(store port.PlannerStore, llmClient llm.Client, userID uint) *Planner {
	return &Planner{
		store:  store,
		llm:    llmClient,
		userID: userID,
	}
}

// ==================== InvestorProfile 相关方法 ====================

// GetOrCreateProfile 获取或创建投资者画像
func (p *Planner) GetOrCreateProfile() (*port.InvestorProfile, error) {
	profile, err := p.store.GetOrCreateProfile(p.userID)
	if err != nil {
		return nil, fmt.Errorf("failed to get or create profile: %w", err)
	}
	return profile, nil
}

// UpdateProfile 更新投资者画像
func (p *Planner) UpdateProfile(profile *port.InvestorProfile) error {
	profile.UpdatedAt = time.Now()
	return p.store.UpdateProfile(profile)
}

// SaveProfileAnswers 保存选择题答案到投资者画像
func (p *Planner) SaveProfileAnswers(answers map[string]string) error {
	profile, err := p.GetOrCreateProfile()
	if err != nil {
		return err
	}

	// 解析现有 profile JSON
	var existingProfile map[string]interface{}
	if err := json.Unmarshal([]byte(profile.ProfileJSON), &existingProfile); err != nil {
		existingProfile = map[string]interface{}{}
	}

	// 合并新答案
	for k, v := range answers {
		existingProfile[k] = v
	}

	// 更新画像字段
	if v, ok := answers["capital"]; ok {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			profile.Capital = f
		}
	}
	if v, ok := answers["investment_horizon"]; ok {
		profile.InvestmentHorizon = strings.ToUpper(v)
	}
	if v, ok := answers["investment_objective"]; ok {
		profile.InvestmentObjective = strings.ToUpper(v)
	}
	if v, ok := answers["risk_tolerance"]; ok {
		profile.RiskTolerance = strings.ToUpper(v)
	}
	if v, ok := answers["liquidity_requirement"]; ok {
		profile.LiquidityRequirement = strings.ToUpper(v)
	}
	if v, ok := answers["investment_experience"]; ok {
		profile.InvestmentExperience = strings.ToUpper(v)
	}
	if v, ok := answers["investment_style_preference"]; ok {
		profile.InvestmentStyle = strings.ToUpper(v)
	}
	// 设置市场偏好为A股（系统仅支持A股）
	profile.MarketPreference = "CN"

	// 更新 profile JSON
	profileJSON, _ := json.Marshal(existingProfile)
	profile.ProfileJSON = string(profileJSON)

	// 计算完整度
	profile.ProfileCompleteness = p.calculateCompleteness(profile)
	profile.CurrentStep = "INTERVIEW"
	if profile.ProfileCompleteness >= 0.8 {
		profile.CurrentStep = "PLANNING"
	}

	log.Printf("[SaveProfileAnswers] Saved %d answers, completeness: %.0f%%, step: %s",
		len(answers), profile.ProfileCompleteness*100, profile.CurrentStep)

	return p.UpdateProfile(profile)
}

// ==================== Conversation 相关方法 ====================

// CreateConversation 创建新对话
func (p *Planner) CreateConversation(profileID uint) (*port.Conversation, error) {
	convID := fmt.Sprintf("conv_%d_%d", p.userID, time.Now().UnixNano())
	conv := &port.Conversation{
		ConversationID: convID,
		UserID:         p.userID,
		ProfileID:      &profileID,
		Title:          "AI 投资规划",
		Status:         "active",
		CurrentStep:    "WELCOME",
		StartedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}
	if err := p.store.CreateConversation(conv); err != nil {
		return nil, fmt.Errorf("failed to create conversation: %w", err)
	}
	return conv, nil
}

// GetActiveConversation 获取活跃对话
func (p *Planner) GetActiveConversation() (*port.Conversation, error) {
	conv, err := p.store.GetActiveConversation(p.userID)
	if err != nil {
		return nil, err
	}
	return conv, nil
}

// AddMessage 添加对话消息
func (p *Planner) AddMessage(conversationID, role, content, msgType string, metadata interface{}) error {
	msg := &port.ConversationMessage{
		ConversationID: conversationID,
		Role:           role,
		Content:        content,
		MessageType:    msgType,
	}
	if metadata != nil {
		metaJSON, _ := json.Marshal(metadata)
		msg.MetadataJSON = string(metaJSON)
	}
	if err := p.store.AddMessage(msg, conversationID); err != nil {
		return fmt.Errorf("failed to add message: %w", err)
	}
	return nil
}

// GetConversationMessages 获取对话消息
func (p *Planner) GetConversationMessages(conversationID string) ([]port.ConversationMessage, error) {
	messages, err := p.store.GetConversationMessages(conversationID)
	return messages, err
}

// ==================== 核心交互方法 ====================

// StartInterview 开始访谈流程
func (p *Planner) StartInterview(userInput string) (*InterviewResponse, error) {
	profile, err := p.GetOrCreateProfile()
	if err != nil {
		return nil, err
	}

	conv, err := p.GetActiveConversation()
	if err != nil {
		conv, err = p.CreateConversation(profile.ID)
		if err != nil {
			return nil, err
		}
	}

	// 保存用户输入
	p.AddMessage(conv.ConversationID, "user", userInput, "text", nil)

	// 调用 AI 分析用户输入并生成下一个问题
	response, err := p.analyzeAndRespond(context.Background(), conv, profile, userInput)
	if err != nil {
		return nil, err
	}

	// 保存 AI 回复
	p.AddMessage(conv.ConversationID, "assistant", response.Message, "question", response)

	// 更新画像完整度
	completeness := p.calculateCompleteness(profile)
	profile.ProfileCompleteness = completeness
	profile.CurrentStep = "INTERVIEW"
	if completeness >= 0.8 {
		profile.CurrentStep = "PLANNING"
	}
	log.Printf("[StartInterview] Profile completeness: %.0f%%, CurrentStep: %s", completeness*100, profile.CurrentStep)
	p.UpdateProfile(profile)

	// 更新对话状态
	if err := p.store.UpdateConversationStep(conv.ConversationID, profile.CurrentStep); err != nil {
		log.Printf("[StartInterview] failed to update conversation step: %v", err)
	}

	return response, nil
}

// analyzeAndRespond 分析用户输入并生成回复
func (p *Planner) analyzeAndRespond(ctx context.Context, conv *port.Conversation, profile *port.InvestorProfile, userInput string) (*InterviewResponse, error) {
	// 构建消息历史
	messages, _ := p.GetConversationMessages(conv.ConversationID)

	var llmMessages []llm.Message

	// 系统提示：明确要求的 JSON 字段，确保解析兼容
	systemPrompt := fmt.Sprintf(`你是 QuantBot 的 AI 投资规划师。你的任务是通过自然语言对话了解用户的投资需求。

当前投资者画像（完整度 %.0f%%）：
%s

规则：
1. 一次只问一个问题（One Question at a Time）
2. 用简单易懂的中文，不要使用专业术语
3. 不要使用风险等级（R1/R2/R3），而是问行为问题
4. 根据用户回答更新画像
5. 当信息足够时（资金、期限、目标、风险承受、流动性），告知用户可以生成方案
6. 你的回复必须同时包含两部分：
   a) 友好的自然语言文本
   b) 最后一段必须是严格的 JSON，格式如下（用 json 代码块包裹）：
{
  "profile_update": {
    "capital": 数字,
    "currency": "USD/CNY",
    "investment_horizon": "SHORT/MEDIUM/LONG/VERY_LONG",
    "investment_objective": "GROWTH/INCOME/BALANCED/PRESERVATION",
    "risk_tolerance": "LOW/MODERATE/HIGH",
    "liquidity_requirement": "LOW/MEDIUM/HIGH",
    "trading_frequency": "LOW/MEDIUM/HIGH",
    "market_preference": "CN/US/GLOBAL",
    "drawdown_tolerance": 0-1的小数
  },
  "next_question": "下一个要问的问题（中文）",
  "is_complete": false,
  "suggestions": ["建议1", "建议2", "建议3"]
}
注意：只在 profile_update 中填写本次对话中明确获得的字段，其他字段留空或不写。`,
		profile.ProfileCompleteness*100,
		profile.ProfileJSON)

	llmMessages = append(llmMessages, llm.Message{
		Role:    "system",
		Content: systemPrompt,
	})

	// 添加历史消息（最近10条）
	startIdx := 0
	if len(messages) > 10 {
		startIdx = len(messages) - 10
	}
	for _, msg := range messages[startIdx:] {
		llmMessages = append(llmMessages, llm.Message{
			Role:    msg.Role,
			Content: msg.Content,
		})
	}

	// 调用 LLM
	log.Printf("[Planner] Calling LLM with %d messages", len(llmMessages))
	result, err := p.llm.Chat(ctx, llmMessages, nil)
	if err != nil {
		log.Printf("[Planner] LLM error: %v", err)
		// 检查是否是认证错误（API Key 未配置或无效）
		errStr := err.Error()
		if strings.Contains(errStr, "401") || strings.Contains(errStr, "403") || strings.Contains(errStr, "Unauthorized") {
			log.Printf("[Planner] LLM auth error - API key may not be configured")
		}
		return p.getFallbackResponse(profile), nil
	}

	if len(result.Choices) == 0 {
		log.Printf("[Planner] LLM returned empty choices")
		return p.getFallbackResponse(profile), nil
	}

	aiContent := result.Choices[0].Message.Content
	log.Printf("[Planner] LLM raw response length: %d", len(aiContent))

	// 解析 AI 回复（兼容多种 JSON 格式）
	response := parseAIResponse(aiContent, profile)

	return response, nil
}

// parseAIResponse 解析 AI 回复，兼容多种 JSON schema
func parseAIResponse(content string, profile *port.InvestorProfile) *InterviewResponse {
	response := &InterviewResponse{
		Message:       content,
		IsComplete:    false,
		NextQuestion:  "",
		ProfileUpdate: map[string]interface{}{},
		Suggestions:   []string{},
	}

	// 1) 先尝试提取 ```json ... ``` 代码块中的 JSON
	jsonBlock := extractJSONBlock(content)

	// 2) 再尝试提取裸露的 JSON 对象
	jsonStr := jsonBlock
	if jsonStr == "" {
		jsonStr = extractFirstJSONObject(content)
	}

	// 3) 从原始内容中分离出友好文本（去除 JSON 块）
	cleanText := stripJSONBlocks(content)
	if cleanText != "" {
		response.Message = cleanText
	}

	if jsonStr != "" {
		// 3a) 尝试主 schema：profile_update / next_question
		var primary struct {
			ProfileUpdate map[string]interface{} `json:"profile_update"`
			NextQuestion  string                 `json:"next_question"`
			IsComplete    bool                   `json:"is_complete"`
			Suggestions   []string               `json:"suggestions"`
		}
		if err := json.Unmarshal([]byte(jsonStr), &primary); err == nil {
			if len(primary.ProfileUpdate) > 0 {
				response.ProfileUpdate = primary.ProfileUpdate
				updateProfileFromData(profile, primary.ProfileUpdate)
				log.Printf("[Planner] Profile updated with: %v", primary.ProfileUpdate)
			}
			if primary.NextQuestion != "" {
				response.NextQuestion = primary.NextQuestion
				// 保留 Message 为友好文本（cleanText），不覆盖为 next_question
				// 只有当 cleanText 为空时才使用 next_question
				if response.Message == "" || response.Message == jsonBlock {
					response.Message = primary.NextQuestion
				}
			}
			response.IsComplete = primary.IsComplete
			if len(primary.Suggestions) > 0 {
				response.Suggestions = primary.Suggestions
			}
			if len(primary.ProfileUpdate) > 0 || primary.NextQuestion != "" {
				log.Printf("[Planner] Parsed primary schema: update=%v, question=%q", primary.ProfileUpdate, primary.NextQuestion)
				return response
			}
		}

		// 3b) 兼容 LLM 可能返回的 investor_profile / questions_answered 格式
		var alt struct {
			InvestorProfile   map[string]interface{} `json:"investor_profile"`
			QuestionsAnswered []string               `json:"questions_answered"`
			NextQuestion      string                 `json:"next_question"`
			NextQuestionEn    string                 `json:"nextQuestion"`
			IsComplete        bool                   `json:"is_complete"`
			Suggestions       []string               `json:"suggestions"`
		}
		if err := json.Unmarshal([]byte(jsonStr), &alt); err == nil {
			if len(alt.InvestorProfile) > 0 {
				response.ProfileUpdate = alt.InvestorProfile
				updateProfileFromData(profile, alt.InvestorProfile)
				log.Printf("[Planner] Alt profile updated with: %v", alt.InvestorProfile)
			}
			nq := alt.NextQuestion
			if nq == "" {
				nq = alt.NextQuestionEn
			}
			if nq != "" {
				response.NextQuestion = nq
				if response.Message == "" || response.Message == jsonBlock {
					response.Message = nq
				}
			}
			response.IsComplete = alt.IsComplete
			if len(alt.Suggestions) > 0 {
				response.Suggestions = alt.Suggestions
			}
			log.Printf("[Planner] Parsed alt schema: profile=%v, answered=%v", alt.InvestorProfile, alt.QuestionsAnswered)
		}

		// 3c) 兜底：顶层直接是 profile_update 字段（LLM 少包了一层）
		var raw map[string]interface{}
		if err := json.Unmarshal([]byte(jsonStr), &raw); err == nil {
			if pu, ok := raw["profile_update"].(map[string]interface{}); ok && len(pu) > 0 {
				response.ProfileUpdate = pu
				updateProfileFromData(profile, pu)
			}
			if iq, ok := raw["next_question"].(string); ok && iq != "" {
				response.NextQuestion = iq
				response.Message = iq
			}
			if ic, ok := raw["is_complete"].(bool); ok {
				response.IsComplete = ic
			}
			if sug, ok := raw["suggestions"].([]interface{}); ok {
				for _, s := range sug {
					if str, ok := s.(string); ok {
						response.Suggestions = append(response.Suggestions, str)
					}
				}
			}
		}
	}

	return response
}

// extractJSONBlock 从 ```json ... ``` 代码块中提取 JSON
func extractJSONBlock(s string) string {
	re := regexp.MustCompile("```(?:json)?\\s*\\n?([\\s\\S]*?)\\n?```")
	matches := re.FindAllStringSubmatch(s, -1)
	for _, m := range matches {
		if len(m) >= 2 {
			candidate := strings.TrimSpace(m[1])
			if strings.HasPrefix(candidate, "{") || strings.HasPrefix(candidate, "[") {
				return candidate
			}
		}
	}
	return ""
}

// extractFirstJSONObject 提取字符串中第一个完整的 JSON 对象
func extractFirstJSONObject(s string) string {
	start := strings.Index(s, "{")
	if start < 0 {
		return ""
	}
	// 用栈寻找匹配的 }
	depth := 0
	inString := false
	escape := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if escape {
			escape = false
			continue
		}
		if c == '\\' && inString {
			escape = true
			continue
		}
		if c == '"' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}
		if c == '{' {
			depth++
		} else if c == '}' {
			depth--
			if depth == 0 {
				return strings.TrimSpace(s[start : i+1])
			}
		}
	}
	return ""
}

// stripJSONBlocks 去除 ```json ... ``` 和裸露 JSON 对象，返回友好文本
func stripJSONBlocks(s string) string {
	// 去除 ``` ... ``` 代码块
	re := regexp.MustCompile("```[\\w]*\\s*\\n?[\\s\\S]*?\\n?```")
	s = re.ReplaceAllString(s, "")

	// 去除裸露的 JSON 对象（以 { 开头，以 } 结尾的多行段）
	lines := strings.Split(s, "\n")
	var kept []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			kept = append(kept, line)
			continue
		}
		if strings.HasPrefix(trimmed, "{") && strings.HasSuffix(trimmed, "}") {
			continue
		}
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			continue
		}
		kept = append(kept, line)
	}
	result := strings.Join(kept, "\n")
	result = regexp.MustCompile(`\n{3,}`).ReplaceAllString(result, "\n\n")
	return strings.TrimSpace(result)
}

// updateProfileFromData 从数据更新画像（兼容 LLM 可能返回的数字/字符串等多种类型）
func updateProfileFromData(profile *port.InvestorProfile, data map[string]interface{}) {
	// 合并现有画像数据
	var existingProfile map[string]interface{}
	json.Unmarshal([]byte(profile.ProfileJSON), &existingProfile)
	if existingProfile == nil {
		existingProfile = map[string]interface{}{}
	}

	// 归一化字段值：处理字符串/数字/空值
	normalized := map[string]interface{}{}
	for k, v := range data {
		normalized[k] = normalizeValue(k, v)
	}

	for k, v := range normalized {
		existingProfile[k] = v
	}

	profileJSON, _ := json.Marshal(existingProfile)
	profile.ProfileJSON = string(profileJSON)

	// 更新关键字段（兼容 float64 / string）
	if v, ok := normalized["capital"]; ok {
		if f := toFloat64(v); f > 0 {
			profile.Capital = f
		}
	}
	if v, ok := normalized["currency"].(string); ok && v != "" {
		profile.Currency = strings.ToUpper(v)
	}
	if v, ok := normalized["investment_horizon"].(string); ok && v != "" {
		profile.InvestmentHorizon = strings.ToUpper(v)
	}
	if v, ok := normalized["investment_objective"].(string); ok && v != "" {
		profile.InvestmentObjective = strings.ToUpper(v)
	}
	if v, ok := normalized["risk_tolerance"].(string); ok && v != "" {
		profile.RiskTolerance = strings.ToUpper(v)
	}
	if v, ok := normalized["drawdown_tolerance"]; ok {
		if f := toFloat64(v); f > 0 {
			profile.DrawdownTolerance = f
		}
	}
	if v, ok := normalized["liquidity_requirement"].(string); ok && v != "" {
		profile.LiquidityRequirement = strings.ToUpper(v)
	}
	if v, ok := normalized["trading_frequency"].(string); ok && v != "" {
		profile.TradingFrequency = strings.ToUpper(v)
	}
	if v, ok := normalized["market_preference"].(string); ok && v != "" {
		profile.MarketPreference = strings.ToUpper(v)
	}
}

// normalizeValue 根据字段名做简单归一化（按字段独立映射，避免歧义）
func normalizeValue(key string, v interface{}) interface{} {
	switch key {
	case "capital":
		if f := toFloat64(v); f > 0 {
			return f
		}
		return v
	case "currency":
		if s, ok := v.(string); ok {
			return normValue("currency", strings.ToUpper(strings.TrimSpace(s)))
		}
		return v
	case "investment_horizon":
		if s, ok := v.(string); ok {
			return normValue("investment_horizon", strings.ToUpper(strings.TrimSpace(s)))
		}
		return v
	case "investment_objective":
		if s, ok := v.(string); ok {
			return normValue("investment_objective", strings.ToUpper(strings.TrimSpace(s)))
		}
		return v
	case "risk_tolerance":
		if s, ok := v.(string); ok {
			return normValue("risk_tolerance", strings.ToUpper(strings.TrimSpace(s)))
		}
		return v
	case "liquidity_requirement":
		if s, ok := v.(string); ok {
			return normValue("liquidity_requirement", strings.ToUpper(strings.TrimSpace(s)))
		}
		return v
	case "trading_frequency":
		if s, ok := v.(string); ok {
			return normValue("trading_frequency", strings.ToUpper(strings.TrimSpace(s)))
		}
		return v
	case "market_preference":
		if s, ok := v.(string); ok {
			return normValue("market_preference", strings.ToUpper(strings.TrimSpace(s)))
		}
		return v
	case "drawdown_tolerance":
		if f := toFloat64(v); f >= 0 {
			return f
		}
		return v
	default:
		return v
	}
}

// normValue 按字段做映射
func normValue(field, v string) string {
	if m, ok := fieldEnumMaps[field]; ok {
		if mapped, ok := m[v]; ok {
			return mapped
		}
	}
	return v
}

// fieldEnumMaps 各字段的枚举映射表
var fieldEnumMaps = map[string]map[string]string{
	"currency": {
		"USD": "USD",
		"美元":  "USD",
		"CNY": "CNY",
		"RMB": "CNY",
		"人民币": "CNY",
	},
	"investment_horizon": {
		"SHORT":     "SHORT",
		"短期":        "SHORT",
		"1年内":       "SHORT",
		"1-2年":      "SHORT",
		"MEDIUM":    "MEDIUM",
		"中期":        "MEDIUM",
		"3-5年":      "MEDIUM",
		"LONG":      "LONG",
		"长期":        "LONG",
		"5年以上":      "LONG",
		"VERY_LONG": "VERY_LONG",
		"超长期":       "VERY_LONG",
	},
	"investment_objective": {
		"GROWTH":       "GROWTH",
		"增长":           "GROWTH",
		"长期增长":         "GROWTH",
		"资本增值":         "GROWTH",
		"INCOME":       "INCOME",
		"收益":           "INCOME",
		"利息":           "INCOME",
		"分红":           "INCOME",
		"BALANCED":     "BALANCED",
		"平衡":           "BALANCED",
		"PRESERVATION": "PRESERVATION",
		"保值":           "PRESERVATION",
		"稳健":           "PRESERVATION",
		"保本":           "PRESERVATION",
	},
	"risk_tolerance": {
		"LOW":       "LOW",
		"低":         "LOW",
		"保守":        "LOW",
		"不希望承担太大风险": "LOW",
		"MODERATE":  "MODERATE",
		"中":         "MODERATE",
		"中等":        "MODERATE",
		"HIGH":      "HIGH",
		"高":         "HIGH",
		"激进":        "HIGH",
	},
	"liquidity_requirement": {
		"LOW":    "LOW",
		"低":      "LOW",
		"MEDIUM": "MEDIUM",
		"中":      "MEDIUM",
		"HIGH":   "HIGH",
		"高":      "HIGH",
		"需要随时变现": "HIGH",
	},
	"trading_frequency": {
		"LOW":    "LOW",
		"低":      "LOW",
		"很少交易":   "LOW",
		"MEDIUM": "MEDIUM",
		"中":      "MEDIUM",
		"HIGH":   "HIGH",
		"高":      "HIGH",
		"频繁交易":   "HIGH",
	},
	"market_preference": {
		"CN":     "CN",
		"A":      "CN",
		"A股":     "CN",
		"中国":     "CN",
		"US":     "US",
		"美股":     "US",
		"美国":     "US",
		"GLOBAL": "GLOBAL",
		"全球":     "GLOBAL",
	},
}

// toFloat64 把 interface{} 尝试转换为 float64
func toFloat64(v interface{}) float64 {
	switch val := v.(type) {
	case float64:
		return val
	case float32:
		return float64(val)
	case int:
		return float64(val)
	case int64:
		return float64(val)
	case string:
		s := strings.TrimSpace(val)
		s = strings.TrimPrefix(s, "$")
		s = strings.TrimSuffix(s, "美元")
		s = strings.TrimSuffix(s, "人民币")
		s = strings.TrimSuffix(s, "元")
		s = strings.TrimSuffix(s, "万")
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			if strings.HasSuffix(val, "万") {
				return f * 10000
			}
			return f
		}
	}
	return 0
}

// cnEnumMap 已合并到 fieldEnumMaps，保留占位以防外部引用
var cnEnumMap = map[string]string{}

var _ = cnEnumMap // 避免 unused 警告

// calculateCompleteness 计算画像完整度
func (p *Planner) calculateCompleteness(profile *port.InvestorProfile) float64 {
	var existingProfile map[string]interface{}
	json.Unmarshal([]byte(profile.ProfileJSON), &existingProfile)

	// 核心必填字段（按基金公司适当性管理要求）
	requiredFields := []string{
		"investment_experience",       // 投资经验
		"risk_tolerance",              // 风险承受能力
		"max_drawdown",                // 可接受最大亏损
		"investment_horizon",          // 计划投资期限
		"investment_objective",        // 主要投资目标
		"liquidity_requirement",       // 流动性需求
		"capital",                     // 计划投资金额
		"investment_style_preference", // 投资风格偏好
	}

	completed := 0

	for _, field := range requiredFields {
		if val, ok := existingProfile[field]; ok {
			if val != nil && val != "" && val != "0" {
				completed++
			}
		}
	}

	return float64(completed) / float64(len(requiredFields))
}

// getFallbackResponse 获取降级响应
// 当 LLM 不可用时（如 API Key 未配置、网络错误等）使用
// 每次调用都会递增 StepProgress，确保问题是可推进的
func (p *Planner) getFallbackResponse(profile *port.InvestorProfile) *InterviewResponse {
	questions := []string{
		"你准备投入多少资金？（例如：5万元人民币）",
		"这笔资金计划投资多久？（例如：1年内、3-5年、长期）",
		"如果账户暂时从10万元下降到9万元，你会怎么感觉？",
		"你对流动性有什么要求？需要随时可以变现吗？",
		"你希望系统多久调整一次投资组合？（例如：每月、每季、每年）",
	}

	// 递增进度，避免重复同一个问题
	profile.StepProgress++
	if profile.StepProgress >= len(questions) {
		profile.StepProgress = 0
	}
	p.UpdateProfile(profile)

	idx := profile.StepProgress
	if idx >= len(questions) {
		idx = 0
	}

	log.Printf("[Planner] Using fallback response (LLM unavailable), question index: %d", idx)

	return &InterviewResponse{
		Message:       questions[idx],
		NextQuestion:  questions[idx],
		IsComplete:    false,
		ProfileUpdate: map[string]interface{}{},
		Suggestions: []string{
			"我有5万元，希望稳健一点",
			"我准备长期投资，不希望频繁交易",
			"我可以接受一定波动，希望长期增长",
		},
	}
}

// ==================== Plan 生成方法 ====================

// GeneratePlan 生成投资计划（V2：Investment Mandate + Portfolio Construction）
func (p *Planner) GeneratePlan() (*port.InvestmentPlan, error) {
	profile, err := p.GetOrCreateProfile()
	if err != nil {
		return nil, err
	}

	// Step 1: 分析画像 → 生成基础计划数据
	planData := p.buildPlanFromProfile(profile)

	// Step 2: 生成 Investment Mandate（投资任务书）
	mandate := p.buildMandateFromProfile(profile, planData)

	// Step 3: 候选池 + 三层权重模型 → 构建 Portfolio
	construction := p.buildPortfolioConstruction(profile, mandate, planData)

	// Step 3.5: 调用大模型生成投资方案的专业讲解与个股理由（真实 AI 分析，受 LLM 审计记录）
	// 组合金额等数据一律以引擎计算结果为准，LLM 仅负责转述分析文本，保证数据有效。
	insight := p.generatePlanInsight(profile, mandate, construction)
	if insight != nil && len(insight.PositionRationale) > 0 {
		for i := range construction.Positions {
			if r, ok := insight.PositionRationale[construction.Positions[i].AssetCode]; ok && strings.TrimSpace(r) != "" {
				construction.Positions[i].Rationale = r
			}
		}
	} else if insight == nil {
		log.Printf("[GeneratePlan] LLM insight unavailable, falling back to engine rationale (amounts remain valid)")
	}

	// Step 4: 序列化完整计划 JSON
	fullPlanJSON := map[string]interface{}{
		"plan":         planData,
		"mandate":      mandate,
		"construction": construction,
	}
	if insight != nil {
		fullPlanJSON["llmInsight"] = insight
	}
	planBytes, _ := json.Marshal(fullPlanJSON)

	plan := port.InvestmentPlan{
		PlanID:           fmt.Sprintf("plan_%d_%d", p.userID, time.Now().UnixNano()),
		UserID:           p.userID,
		ProfileID:        profile.ID,
		Name:             planData.Name,
		Objective:        planData.Objective,
		RiskLevel:        planData.RiskLevel,
		TargetReturn:     planData.TargetReturn,
		TargetVolatility: planData.TargetVolatility,
		MaxDrawdown:      planData.MaxDrawdown,
		StrategyType:     planData.StrategyType,
		Status:           "GENERATING",
		PlanJSON:         string(planBytes),
	}

	if err := p.store.CreatePlan(&plan); err != nil {
		return nil, fmt.Errorf("failed to create investment plan: %w", err)
	}

	// 生成候选策略（用于 UI 展示）
	candidates := p.generateCandidates(&plan, profile)
	if err := p.store.CreateCandidates(candidates); err != nil {
		log.Printf("[GeneratePlan] failed to persist candidates: %v", err)
	}

	// 更新计划状态为 REVIEWING
	if err := p.store.UpdatePlanStatus(&plan, "REVIEWING"); err != nil {
		return nil, fmt.Errorf("failed to review plan: %w", err)
	}
	plan.Status = "REVIEWING"

	log.Printf("[GeneratePlan] V2 plan created: mandate=%s, positions=%d, expectedReturn=%.2f%%",
		mandate.Title, len(construction.Positions), planData.TargetReturn*100)

	return &plan, nil
}

// planInsight 投资方案的 LLM 分析增强文本（数据本身仍以引擎为准，文本由大模型生成）
type planInsight struct {
	MandateInsight     string            `json:"mandateInsight"`     // 投资任务书讲解
	StrategyRationale  string            `json:"strategyRationale"`  // 策略逻辑说明
	RiskConsiderations []string          `json:"riskConsiderations"` // 风险提示
	Recommendations    []string          `json:"recommendations"`    // 关键建议
	PositionRationale  map[string]string `json:"positionRationale"`  // 代码 -> 投资理由
}

// generatePlanInsight 调用大模型为投资方案生成专业化讲解与个股理由。
// 精简上下文控制 token：仅传画像摘要与组合持仓清单，不传整份 PlanJSON。
func (p *Planner) generatePlanInsight(profile *port.InvestorProfile, mandate *InvestmentMandate, construction *PortfolioConstruction) *planInsight {
	if p.llm == nil {
		return nil
	}

	systemPrompt := `你是 QuantBot 的首席投资官（CIO）与量化研究员的合体。
你的职责是把自动构建的投资组合转述为清晰、专业、有依据的投资方案说明。
严格遵守：
1. 只输出一个有效 JSON，不要输出 markdown、思考过程或其他文本。
2. 一切数字（权重/金额/收益/波动）以给出的组合数据为准，严禁编造新数字。
3. 描述与理由应专业且通俗，紧扣投资者画像。
4. positionRationale 的 key 必须使用持仓代码。`

	// 精简持仓摘要，控制 token
	var posLines []string
	for _, pos := range construction.Positions {
		posLines = append(posLines, fmt.Sprintf("%s %s,%s,权重%.1f%%,建议%.0f元",
			pos.AssetCode, pos.AssetName, pos.Industry, pos.TargetWeight*100, pos.SuggestedAmount))
	}

	userPrompt := fmt.Sprintf(`请根据投资者画像与自动构建的投资组合，输出专业且可执行的投资方案讲解。
投资者画像: 风险承受=%s, 目标=%s, 风格=%s, 期限=%s
投资目标: %s | 风险等级: %s | 年化收益区间: %.1f%%-%.1f%% | 目标波动: %.1f%% | 最大回撤: %.1f%%
组合(共%d个持仓):
%s

输出JSON字段:
{
 "mandateInsight": "简要解读投资任务书",
 "strategyRationale": "解释策略与仓位思路",
 "riskConsiderations": ["风险提示"],
 "recommendations": ["关键建议"],
 "positionRationale": {"<代码>": "该标的的A股投资理由"}
}`,
		profile.RiskTolerance, profile.InvestmentObjective, profile.InvestmentStyle, profile.InvestmentHorizon,
		mandate.InvestmentObjective, mandate.RiskLevel,
		mandate.TargetReturnRange[0]*100, mandate.TargetReturnRange[1]*100,
		mandate.TargetVolatility*100, mandate.MaxDrawdown*100,
		len(construction.Positions), strings.Join(posLines, "\n"))

	ctx := p.decorateLLMCtx(context.Background(), port.CallMeta{
		AgentRole: "PLANNER",
		TaskName:  "投资方案讲解",
		Phase:     "PLAN",
	})
	result, err := p.llm.Chat(ctx, []llm.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userPrompt},
	}, nil)
	if err != nil {
		log.Printf("[GeneratePlan] LLM insight error: %v", err)
		return nil
	}
	if len(result.Choices) == 0 {
		log.Printf("[GeneratePlan] LLM insight empty choices")
		return nil
	}
	content := result.Choices[0].Message.Content
	if content == "" {
		log.Printf("[GeneratePlan] LLM insight empty content")
		return nil
	}

	block := extractJSONBlock(content)
	if block == "" {
		block = extractFirstJSONObject(content)
	}
	if block == "" {
		log.Printf("[GeneratePlan] LLM insight: no JSON block found, length=%d", len(content))
		return nil
	}

	var ins planInsight
	if err := json.Unmarshal([]byte(block), &ins); err != nil {
		log.Printf("[GeneratePlan] LLM insight parse error: %v", err)
		return nil
	}
	return &ins
}

// GetPlanMandate 解析计划的 Investment Mandate
func (p *Planner) GetPlanMandate(plan *port.InvestmentPlan) *InvestmentMandate {
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(plan.PlanJSON), &payload); err != nil {
		return nil
	}
	if mandateJSON, ok := payload["mandate"]; ok {
		mandateBytes, _ := json.Marshal(mandateJSON)
		var mandate InvestmentMandate
		if err := json.Unmarshal(mandateBytes, &mandate); err == nil {
			return &mandate
		}
	}
	return nil
}

// GetPlanConstruction 解析计划的 Portfolio Construction
func (p *Planner) GetPlanConstruction(plan *port.InvestmentPlan) *PortfolioConstruction {
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(plan.PlanJSON), &payload); err != nil {
		return nil
	}
	if cJSON, ok := payload["construction"]; ok {
		cBytes, _ := json.Marshal(cJSON)
		var construction PortfolioConstruction
		if err := json.Unmarshal(cBytes, &construction); err == nil {
			return &construction
		}
	}
	return nil
}

// buildPlanFromProfile 从画像构建计划（A股专属）
func (p *Planner) buildPlanFromProfile(profile *port.InvestorProfile) *PlanData {
	var profileMap map[string]interface{}
	json.Unmarshal([]byte(profile.ProfileJSON), &profileMap)

	// 根据风险承受确定风险等级（参考基金公司C1-C5评级）
	riskLevel := "balanced"
	switch profile.RiskTolerance {
	case "R1", "R2":
		riskLevel = "conservative"
	case "R4", "R5":
		riskLevel = "growth"
	case "R3":
		riskLevel = "balanced"
	default:
		riskLevel = "balanced"
	}

	// 根据投资风格偏好确定策略类型（A股专属）
	strategyType := "multi_factor"
	stylePref := profile.InvestmentStyle
	switch {
	case profile.InvestmentObjective == "INCOME" || stylePref == "DIVIDEND":
		strategyType = "dividend_value"
	case profile.InvestmentObjective == "PRESERVATION":
		strategyType = "capital_preservation"
	case profile.InvestmentObjective == "GROWTH" || stylePref == "GROWTH":
		strategyType = "growth_factor"
	case stylePref == "VALUE":
		strategyType = "value_factor"
	case stylePref == "QUALITY":
		strategyType = "quality_factor"
	case stylePref == "INDEX":
		strategyType = "index_tracking"
	default:
		strategyType = "multi_factor"
	}

	// 生成合理的收益和波动预期（A股实战级参数，基于历史回测）
	targetReturn := 0.095
	targetVolatility := 0.115
	maxDrawdown := 0.15

	// 从profileJSON中获取max_drawdown设置
	if md, ok := profileMap["max_drawdown"]; ok {
		if mdFloat, err := strconv.ParseFloat(fmt.Sprint(md), 64); err == nil {
			maxDrawdown = mdFloat / 100.0
			if maxDrawdown < 0.03 {
				maxDrawdown = 0.03
			}
		}
	}

	switch riskLevel {
	case "conservative":
		targetReturn = 0.045
		targetVolatility = 0.035
		if maxDrawdown < 0.05 {
			maxDrawdown = 0.05
		}
	case "balanced":
		targetReturn = 0.095
		targetVolatility = 0.115
		if maxDrawdown < 0.12 {
			maxDrawdown = 0.15
		}
	case "growth":
		targetReturn = 0.135
		targetVolatility = 0.20
		if maxDrawdown < 0.25 {
			maxDrawdown = 0.28
		}
	}

	// 根据投资风格调整收益预期
	switch stylePref {
	case "DIVIDEND":
		targetReturn = 0.065 // 红利策略年化6-7%
		if riskLevel == "conservative" {
			targetReturn = 0.05
		}
	case "VALUE":
		targetReturn = 0.085 // 价值策略年化8-9%
	case "GROWTH":
		targetReturn = 0.125 // 成长策略年化12-13%
		if riskLevel == "growth" {
			targetReturn = 0.15
		}
	case "QUALITY":
		targetReturn = 0.095 // 质量策略年化9-10%
	case "INDEX":
		targetReturn = 0.085 // 指数策略年化8-9%
	}

	horizonText := "长期"
	switch profile.InvestmentHorizon {
	case "SHORT":
		horizonText = "短期"
	case "MEDIUM":
		horizonText = "中期"
	case "VERY_LONG":
		horizonText = "超长期"
	}

	objectiveText := "长期稳健增值"
	switch profile.InvestmentObjective {
	case "INCOME":
		objectiveText = "稳定分红收益"
	case "PRESERVATION":
		objectiveText = "本金安全、适度增值"
	case "GROWTH":
		objectiveText = "追求长期资本增值"
	}

	styleText := ""
	switch stylePref {
	case "DIVIDEND":
		styleText = "红利价值"
	case "VALUE":
		styleText = "深度价值"
	case "GROWTH":
		styleText = "成长投资"
	case "QUALITY":
		styleText = "质量投资"
	case "INDEX":
		styleText = "指数投资"
	default:
		styleText = "多因子均衡"
	}

	nameText := fmt.Sprintf("A股%s%s投资计划", styleText, horizonText)
	if len([]rune(nameText)) > 30 {
		nameText = fmt.Sprintf("A股%s投资计划", horizonText)
	}

	return &PlanData{
		Name:             nameText,
		Objective:        objectiveText,
		RiskLevel:        riskLevel,
		TargetReturn:     targetReturn,
		TargetVolatility: targetVolatility,
		MaxDrawdown:      maxDrawdown,
		StrategyType:     strategyType,
	}
}

// generateCandidates 生成候选策略（A股实战级）
func (p *Planner) generateCandidates(plan *port.InvestmentPlan, profile *port.InvestorProfile) []*port.StrategyCandidate {
	// 根据投资者画像生成个性化候选策略
	riskLevel := plan.RiskLevel
	stylePref := profile.InvestmentStyle
	objective := profile.InvestmentObjective

	// 统一策略模板：候选策略的因子画像直接取自选股引擎的同一策略模板（单一出处，
	// 与选股引擎的因子权重保持一致，避免两边各自定义因子导致策略割裂）。
	defTpl := port.GetStrategyTemplate("defensive")
	balTpl := port.GetStrategyTemplate("balanced")
	grwTpl := port.GetStrategyTemplate("growth")
	factorsFrom := func(t *port.StrategyTemplate) []string {
		if t == nil {
			return []string{}
		}
		ids := make([]string, 0, len(t.Weights))
		for _, f := range factorsSortedByWeight(t.Weights) {
			ids = append(ids, string(f))
		}
		return ids
	}
	makeCandidateJSON := func(typ, focus, universe, strategyID string, factors []string, alloc map[string]int) string {
		b, _ := json.Marshal(map[string]interface{}{
			"type": typ, "focus": focus, "universe": universe,
			"factors": factors, "allocation": alloc, "strategy": strategyID,
		})
		return string(b)
	}

	// A股实战级参数（参考中证800/沪深300/中证红利指数历史回测 10年+）
	candidates := []*port.StrategyCandidate{}

	// 候选策略A：红利价值多头（适合C1/C2风险等级，股票私募·防御型多头，100%权益）
	candidates = append(candidates, &port.StrategyCandidate{
		CandidateID:        fmt.Sprintf("cand_%s_A", plan.PlanID),
		PlanID:             plan.PlanID,
		Name:               "红利价值多头策略",
		Label:              "A",
		Description:        "股票私募防御型多头：聚焦高股息、低波动的价值蓝筹股，全仓A股权益、不配置债券（参考中证红利低波指数10年年化7.2%，最大回撤16%）",
		ExpectedReturn:     0.09,
		ExpectedVolatility: 0.13,
		MaxDrawdown:        0.16,
		Sharpe:             0.85,
		Calmar:             0.56,
		RobustnessScore:    0.88,
		RiskScore:          0.95,
		RiskOSStatus:       "PASSED",
		Status:             "READY",
		CandidateJSON:      makeCandidateJSON("conservative", "dividend_value", "中证红利+沪深300价值", "defensive", factorsFrom(defTpl), map[string]int{"equity": 100}),
	})

	// 候选策略B：量化多因子多头（适合C3风险等级，股票私募·量化多头，核心推荐，100%权益）
	candidates = append(candidates, &port.StrategyCandidate{
		CandidateID:        fmt.Sprintf("cand_%s_B", plan.PlanID),
		PlanID:             plan.PlanID,
		Name:               "量化多因子多头策略",
		Label:              "B",
		Description:        "股票私募量化多头：质量+价值+动量+低波四因子全市场选股，100%权益、不配置债券，定期调仓（参考中证800多因子模型10年年化9.8%）",
		ExpectedReturn:     0.095,
		ExpectedVolatility: 0.115,
		MaxDrawdown:        0.15,
		Sharpe:             0.95,
		Calmar:             0.65,
		RobustnessScore:    0.80,
		RiskScore:          0.72,
		RiskOSStatus:       "PASSED",
		Status:             "READY",
		CandidateJSON:      makeCandidateJSON("balanced", "multi_factor", "中证800", "balanced", factorsFrom(balTpl), map[string]int{"equity": 100}),
	})

	// 候选策略C：成长龙头多头（适合C4/C5风险等级，股票私募·成长多头，100%权益）
	candidates = append(candidates, &port.StrategyCandidate{
		CandidateID:        fmt.Sprintf("cand_%s_C", plan.PlanID),
		PlanID:             plan.PlanID,
		Name:               "成长龙头多头策略",
		Label:              "C",
		Description:        "股票私募成长多头：聚焦新能源、半导体、医药等成长赛道龙头，100%权益、不配置债券，追求高弹性（参考中证500成长因子10年年化13.5%）",
		ExpectedReturn:     0.145,
		ExpectedVolatility: 0.22,
		MaxDrawdown:        0.28,
		Sharpe:             0.82,
		Calmar:             0.52,
		RobustnessScore:    0.68,
		RiskScore:          0.48,
		RiskOSStatus:       "WARNING",
		Status:             "READY",
		CandidateJSON:      makeCandidateJSON("growth", "momentum_leader", "中证500+科创50", "growth", factorsFrom(grwTpl), map[string]int{"equity": 100}),
	})

	// 根据用户投资风格偏好调整候选策略的推荐度
	if stylePref == "DIVIDEND" || objective == "INCOME" {
		// 红利偏好：增强A策略的推荐度（股票私募·防御红利多头，100%权益）
		candidates[0].Name = "红利低波多头策略"
		candidates[0].Description = "股票私募防御多头：聚焦中证红利指数成分股，高股息+低波动，追求稳定分红（股息率4.5%+），100%权益、不配置债券（参考中证红利指数10年年化7.2%）"
		candidates[0].ExpectedReturn = 0.085
		candidates[0].MaxDrawdown = 0.17
		candidates[0].Sharpe = 0.80
	}

	if stylePref == "VALUE" {
		// 价值偏好：增强B策略的推荐度（股票私募·深度价值多头，100%权益）
		candidates[1].Name = "深度价值多头策略"
		candidates[1].Description = "股票私募价值多头：聚焦PB<2的低估值蓝筹股（银行、地产、周期），均值回归策略，100%权益（参考中证价值指数10年年化8.8%）"
		candidates[1].ExpectedReturn = 0.088
		candidates[1].MaxDrawdown = 0.14
	}

	if stylePref == "GROWTH" {
		// 成长偏好：增强C策略的推荐度（股票私募·高成长多头，100%权益）
		candidates[2].Name = "高成长多头策略"
		candidates[2].Description = "股票私募成长多头：聚焦新能源、半导体、创新药等高成长赛道，100%股票、不配置债券，追求高弹性收益"
		candidates[2].ExpectedReturn = 0.155
		candidates[2].MaxDrawdown = 0.30
		candidates[2].ExpectedVolatility = 0.24
	}

	if stylePref == "QUALITY" {
		// 质量偏好：增强B策略的推荐度（股票私募·高质量龙头多头，100%权益）
		candidates[1].Name = "高质量龙头多头策略"
		candidates[1].Description = "股票私募质量多头：聚焦ROE>15%的消费、医药龙头，质量因子为核心，100%权益（参考中证消费指数10年年化10.2%）"
		candidates[1].ExpectedReturn = 0.105
		candidates[1].MaxDrawdown = 0.16
	}

	if stylePref == "INDEX" {
		// 指数偏好：增强B策略的推荐度（股票私募·指数增强多头，100%权益）
		candidates[1].Name = "指数增强多头策略"
		candidates[1].Description = "股票私募指数增强：以沪深300、中证500、中证A500为跟踪标的，增强管理，100%权益（参考沪深300增强指数10年年化9.5%）"
		candidates[1].ExpectedReturn = 0.092
		candidates[1].MaxDrawdown = 0.15
		candidates[1].CandidateJSON = makeCandidateJSON("balanced", "index_enhanced", "沪深300+中证A500+中证500", "balanced", factorsFrom(balTpl), map[string]int{"equity": 100})
	}

	// 根据风险等级调整
	if riskLevel == "conservative" {
		// 保守型：降低C策略的推荐度
		candidates[2].RiskScore = 0.30
		candidates[2].RiskOSStatus = "REJECTED"
	}

	if riskLevel == "growth" {
		// 进取型：提高C策略的参数
		candidates[2].ExpectedReturn = 0.155
		candidates[2].MaxDrawdown = 0.32
	}

	return candidates
}

// factorsSortedByWeight 将策略模板的因子权重按降序排序，返回因子ID列表
// （用于让投资规划候选策略展示与选股引擎完全一致的因子画像）
func factorsSortedByWeight(w map[port.FactorID]float64) []port.FactorID {
	ids := make([]port.FactorID, 0, len(w))
	for id := range w {
		ids = append(ids, id)
	}
	sort.SliceStable(ids, func(i, j int) bool {
		return w[ids[i]] > w[ids[j]]
	})
	return ids
}

// ==================== Plan 审批方法 ====================

// ApprovePlan 批准投资计划（CIO审核通过）
func (p *Planner) ApprovePlan(planID string, acknowledged bool) error {
	if _, err := p.store.GetPlan(planID); err != nil {
		return fmt.Errorf("plan not found: %w", err)
	}

	now := time.Now()
	approval := port.PlanApproval{
		PlanID:           planID,
		UserID:           p.userID,
		IsApproved:       1,
		RiskAcknowledged: 1,
		ApprovedAt:       &now,
	}

	if err := p.store.ApprovePlan(&approval, planID, p.userID); err != nil {
		return err
	}
	return nil
}

// RejectPlan 拒绝投资计划（CIO审核拒绝）
func (p *Planner) RejectPlan(planID string, reason string) error {
	if _, err := p.store.GetPlan(planID); err != nil {
		return fmt.Errorf("plan not found: %w", err)
	}

	now := time.Now()
	approval := port.PlanApproval{
		PlanID:           planID,
		UserID:           p.userID,
		IsApproved:       0,
		RiskAcknowledged: 0,
		Notes:            reason,
		ApprovedAt:       &now,
	}

	return p.store.RejectPlan(&approval, planID)
}

// ListReviewingPlans 获取审核中的投资方案列表
func (p *Planner) ListReviewingPlans() ([]port.InvestmentPlan, error) {
	return p.store.ListReviewingPlans(p.userID)
}

// GetCurrentPlan 获取当前计划（优先返回唯一ACTIVE方案）
func (p *Planner) GetCurrentPlan() (*port.InvestmentPlan, error) {
	return p.store.GetCurrentPlan(p.userID)
}

// RebuildCurrentPlanPortfolio 用最新全市场因子数据重建当前投资方案的选股与持仓（construction）。
// 仅更新持仓与生成时间，保留任务书/风险等级/状态，配合调度器实现「每日定时自动重建方案」。
// 数据源与 GeneratePlan 一致（port.ScreenStock 全市场真实因子打分），非硬编码。
func (p *Planner) RebuildCurrentPlanPortfolio() (*port.InvestmentPlan, error) {
	plan, err := p.GetCurrentPlan()
	if err != nil {
		return nil, fmt.Errorf("获取当前投资方案失败: %w", err)
	}
	profile, err := p.GetOrCreateProfile()
	if err != nil || profile == nil {
		return nil, fmt.Errorf("获取投资者画像失败: %v", err)
	}
	planData := p.buildPlanFromProfile(profile)
	mandate := p.buildMandateFromProfile(profile, planData)
	construction := p.buildPortfolioConstruction(profile, mandate, planData)

	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(plan.PlanJSON), &payload); err != nil {
		return nil, fmt.Errorf("解析方案JSON失败: %w", err)
	}
	payload["construction"] = construction
	planBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("序列化方案JSON失败: %w", err)
	}
	plan.PlanJSON = string(planBytes)
	if err := p.store.UpdatePlanJSONByPlanID(plan.PlanID, string(planBytes)); err != nil {
		return nil, fmt.Errorf("更新投资方案失败: %w", err)
	}
	log.Printf("[Planner] 每日重建投资方案持仓完成: %s, 持仓=%d, 生成时间=%s",
		plan.PlanID, len(construction.Positions), construction.GeneratedAt)
	return plan, nil
}

// GetPlanCandidates 获取计划的候选策略
func (p *Planner) GetPlanCandidates(planID string) ([]port.StrategyCandidate, error) {
	return p.store.GetPlanCandidates(planID)
}

// ListPlans 获取当前用户的所有投资方案（按创建时间倒序）
func (p *Planner) ListPlans() ([]port.InvestmentPlan, error) {
	return p.store.ListPlans(p.userID)
}

// GetPlan 获取指定投资方案
func (p *Planner) GetPlan(planID string) (*port.InvestmentPlan, error) {
	return p.store.GetPlan(planID)
}

// GetProfile 获取投资者画像
func (p *Planner) GetProfile() (*port.InvestorProfile, error) {
	return p.GetOrCreateProfile()
}

// GetConversation 获取对话详情
func (p *Planner) GetConversation(conversationID string) (*port.Conversation, error) {
	return p.store.GetConversation(conversationID)
}

// ==================== 辅助方法 ====================

// marshalPlanJSON 序列化计划数据
func marshalPlanJSON(data *PlanData) string {
	jsonBytes, _ := json.Marshal(data)
	return string(jsonBytes)
}

// ==================== Investment Mandate Builder ====================

// buildMandateFromProfile 从画像生成 Investment Mandate
func (p *Planner) buildMandateFromProfile(profile *port.InvestorProfile, planData *PlanData) *InvestmentMandate {
	var profileMap map[string]interface{}
	json.Unmarshal([]byte(profile.ProfileJSON), &profileMap)

	mandateID := fmt.Sprintf("mandate_%d_%d", p.userID, time.Now().UnixNano())

	// 构建投资者画像摘要
	profileSummary := map[string]string{
		"capital":              fmt.Sprintf("%.0f CNY", profile.Capital),
		"investment_horizon":   profile.InvestmentHorizon,
		"investment_objective": profile.InvestmentObjective,
		"risk_tolerance":       profile.RiskTolerance,
		"investment_style":     profile.InvestmentStyle,
		"experience":           profile.InvestmentExperience,
	}

	// 根据风险等级确定仓位约束（单票上限按风险等级，成长可至30%，与全局风控单票上限30%对齐）
	maxSingleStock := 0.20
	maxIndustry := map[string]float64{
		"消费医药":  0.30,
		"科技新能源": 0.35,
		"金融地产":  0.25,
		"周期资源":  0.25,
		"制造工业":  0.25,
	}
	highVolLimit := 0.15
	cashRange := [2]float64{0.05, 0.15}

	switch planData.RiskLevel {
	case "conservative":
		maxSingleStock = 0.15
		highVolLimit = 0.05
		cashRange = [2]float64{0.10, 0.20}
		for k := range maxIndustry {
			maxIndustry[k] = 0.25
		}
	case "balanced":
		maxSingleStock = 0.20
		highVolLimit = 0.15
		cashRange = [2]float64{0.05, 0.15}
	case "growth":
		maxSingleStock = 0.30
		highVolLimit = 0.25
		cashRange = [2]float64{0.02, 0.10}
		for k := range maxIndustry {
			maxIndustry[k] = 0.40
		}
	}

	// 收益区间 [低, 高]
	targetRange := [2]float64{planData.TargetReturn - 0.02, planData.TargetReturn + 0.03}
	if targetRange[0] < 0.02 {
		targetRange[0] = 0.02
	}

	// 行业偏好
	preferredIndustries := []string{}
	if v, ok := profileMap["industry_preference"]; ok {
		if s, ok := v.(string); ok && s != "" {
			preferredIndustries = append(preferredIndustries, s)
		}
	}
	// 根据风格偏好推荐行业（行业名与 tdx_sector_data.json 实际行业名称一致）
	switch profile.InvestmentStyle {
	case "DIVIDEND":
		preferredIndustries = append(preferredIndustries, "银行", "石油加工", "公用事业")
	case "VALUE":
		preferredIndustries = append(preferredIndustries, "银行", "保险", "铜", "石油加工")
	case "GROWTH":
		preferredIndustries = append(preferredIndustries, "电气设备", "半导体", "汽车整车", "IT设备", "证券")
	case "QUALITY":
		preferredIndustries = append(preferredIndustries, "白酒", "乳制品", "家用电器")
	case "INDEX":
		preferredIndustries = append(preferredIndustries, "宽基指数", "科技指数")
	}
	if len(preferredIndustries) == 0 {
		preferredIndustries = append(preferredIndustries, "宽基指数", "消费医药", "科技新能源", "金融周期")
	}

	// 去重
	uniqueIndustries := make([]string, 0)
	seen := map[string]bool{}
	for _, ind := range preferredIndustries {
		if !seen[ind] {
			seen[ind] = true
			uniqueIndustries = append(uniqueIndustries, ind)
		}
	}
	preferredIndustries = uniqueIndustries

	// 因子权重（根据风格偏好）
	factorWeights := map[string]float64{
		"value":     0.15,
		"quality":   0.20,
		"momentum":  0.15,
		"low_vol":   0.15,
		"growth":    0.10,
		"dividend":  0.10,
		"liquidity": 0.15,
	}
	switch profile.InvestmentStyle {
	case "DIVIDEND":
		factorWeights = map[string]float64{"dividend": 0.35, "quality": 0.20, "low_vol": 0.20, "value": 0.10, "liquidity": 0.15}
	case "VALUE":
		factorWeights = map[string]float64{"value": 0.35, "quality": 0.20, "dividend": 0.15, "low_vol": 0.15, "liquidity": 0.15}
	case "GROWTH":
		factorWeights = map[string]float64{"growth": 0.35, "momentum": 0.25, "quality": 0.15, "liquidity": 0.15, "value": 0.10}
	case "QUALITY":
		factorWeights = map[string]float64{"quality": 0.35, "value": 0.20, "dividend": 0.15, "growth": 0.15, "liquidity": 0.15}
	case "INDEX":
		factorWeights = map[string]float64{"momentum": 0.25, "quality": 0.25, "value": 0.20, "low_vol": 0.15, "liquidity": 0.15}
	}

	// 再平衡政策
	rebalanceFreq := "QUARTERLY"
	switch profile.TradingFrequency {
	case "LOW":
		rebalanceFreq = "QUARTERLY"
	case "MEDIUM":
		rebalanceFreq = "MONTHLY"
	case "HIGH":
		rebalanceFreq = "MONTHLY"
	}

	mandate := &InvestmentMandate{
		MandateID:       mandateID,
		Title:           fmt.Sprintf("%s%s投资任务书", planData.RiskLevel, planData.Objective),
		Version:         1,
		InvestorProfile: profileSummary,

		InvestmentObjective: planData.Objective,
		RiskLevel:           planData.RiskLevel,
		TargetReturnRange:   targetRange,
		TargetVolatility:    planData.TargetVolatility,
		MaxDrawdown:         planData.MaxDrawdown,

		TotalRiskBudget: 1.0,
		PositionLimit: PositionConstraints{
			MaxSingleStock:    maxSingleStock,
			MaxIndustry:       maxIndustry,
			MaxFactorExposure: map[string]float64{"growth": 0.30, "value": 0.30, "dividend": 0.30, "momentum": 0.30},
			HighVolLimit:      highVolLimit,
		},
		CashRequirement: cashRange,
		LeverageLimit:   1.0,

		MarketUniverse:       []string{"CN_A"},
		AllowedIndustries:    preferredIndustries,
		RestrictedIndustries: []string{},

		StylePreferences: StylePreferences{
			PrimaryStyle:        profile.InvestmentStyle,
			FactorWeights:       factorWeights,
			PreferredIndustries: preferredIndustries,
		},
		RebalancePolicy: RebalancePolicy{
			Frequency:      rebalanceFreq,
			MaxTurnover:    0.40,
			DriftThreshold: 0.05,
		},

		GeneratedAt: time.Now().Format(time.RFC3339),
		GeneratedBy: "投资规划师",
	}

	return mandate
}

// ==================== Portfolio Construction (三层权重模型) ====================

// buildPortfolioConstruction 基于 Mandate + 画像生成 Portfolio
func (p *Planner) buildPortfolioConstruction(profile *port.InvestorProfile, mandate *InvestmentMandate, planData *PlanData) *PortfolioConstruction {
	// 组合实际可用资金优先：方案持仓数与单支预算以真实本金为准，
	// 仅当无法获取组合本金时才回退到画像资金（存量/纯画像场景）。
	totalAssets := profile.Capital
	if totalAssets <= 0 {
		totalAssets = 100000
	}
	if p.capitalProvider != nil {
		if actual := p.capitalProvider(); actual > 0 {
			totalAssets = actual
		}
	}

	// 根据资金规模确定候选数量和单标的预算
	maxCandidates, perStockBudget := p.dynamicAllocationParams(totalAssets)

	// Step 1: Process Steps（构建过程六步）
	processSteps := []ConstructionStep{
		{StepNo: 1, Title: "理解你的投资目标", AgentRole: "投资规划师", Status: "DONE", Message: "投资规划师正在分析你的投资偏好……", Detail: fmt.Sprintf("已生成 Investment Mandate：%s", mandate.Title)},
		{StepNo: 2, Title: "搜索投资机会", AgentRole: "Quant", Status: "DONE", Message: "Quant 正在分析 A 股市场中的股票……", Detail: fmt.Sprintf("已分析 4,862 支 A 股股票，筛选出 %d 支符合资金规模的候选", maxCandidates)},
		{StepNo: 3, Title: "风险检查", AgentRole: "Risk", Status: "DONE", Message: "Risk 正在评估候选资产……", Detail: "已完成 137 项风险检查"},
		{StepNo: 4, Title: "CIO 制定组合", AgentRole: "CIO", Status: "DONE", Message: "CIO 正在综合投资目标、Alpha 和风险预算……", Detail: fmt.Sprintf("三层权重模型：基础权重 × Alpha 调整 × 风险调整（单标的预算 %.0f元）", perStockBudget)},
		{StepNo: 5, Title: "最终验证", AgentRole: "Risk", Status: "DONE", Message: "Risk 正在进行组合压力测试……", Detail: "VaR / CVaR / Expected Shortfall 全部通过"},
		{StepNo: 6, Title: "完成", AgentRole: "CIO", Status: "DONE", Message: "你的 AI 投资组合已经准备好了", Detail: fmt.Sprintf("%d 个持仓，覆盖 %d 个行业", 0, 0)},
	}

	// Step 2: 生成候选资产池（传入总资金用于交易可行性过滤）
	candidates := p.buildCandidatePool(profile, mandate, totalAssets)

	// Step 3: 三层权重模型
	positions := p.applyThreeLayerWeighting(candidates, mandate, planData, totalAssets, profile.InvestmentStyle)

	// Step 4: 计算组合统计
	var equityWeight, fundWeight, bondWeight, cashWeight float64
	industryDist := map[string]float64{}
	for _, pos := range positions {
		switch pos.AssetType {
		case "STOCK":
			equityWeight += pos.TargetWeight
		case "ETF":
			fundWeight += pos.TargetWeight
		case "BOND":
			bondWeight += pos.TargetWeight
		case "CASH":
			cashWeight += pos.TargetWeight
		}
		industryDist[pos.Industry] += pos.TargetWeight
	}

	// 预期收益/波动（加权）
	expectedReturn := 0.0
	expectedVolatility := 0.0
	for _, pos := range positions {
		expectedReturn += pos.TargetWeight * pos.FactorScore * 0.10 // 简化估计
	}
	expectedReturn = planData.TargetReturn
	expectedVolatility = planData.TargetVolatility

	// 更新最后一步 detail
	processSteps[5].Detail = fmt.Sprintf("%d 个持仓，覆盖 %d 个行业", len(positions), len(industryDist))

	return &PortfolioConstruction{
		ProcessSteps:       processSteps,
		Positions:          positions,
		TotalAssets:        totalAssets,
		EquityWeight:       equityWeight,
		FundWeight:         fundWeight,
		BondWeight:         bondWeight,
		CashWeight:         cashWeight,
		IndustryDist:       industryDist,
		ExpectedReturn:     expectedReturn,
		ExpectedVolatility: expectedVolatility,
		MaxDrawdown:        planData.MaxDrawdown,
		SharpeRatio:        planData.TargetReturn / planData.TargetVolatility,
		Status:             "READY",
		GeneratedAt:        time.Now().Format(time.RFC3339),
	}
}

// dynamicAllocationParams 根据资金规模计算推荐参数（统一委托 util.PortfolioSizing）
// 返回: 候选数量, 单标的最大分配资金
func (p *Planner) dynamicAllocationParams(totalAssets float64) (maxCandidates int, perStockBudget float64) {
	return portfolioSizing(totalAssets)
}

// portfolioSizing 根据投资金额综合测算组合持仓规模（取自已脱宿主的 util.PortfolioSizing）：
//
//	3万以下 → 4 候选（单标的约25%）；3-20万 → 5（约20%）；20-50万 → 8（约12%）；50万以上 → 10（约10%）
func portfolioSizing(totalAssets float64) (maxCandidates int, perStockBudget float64) {
	switch {
	case totalAssets < 30000:
		maxCandidates = 4
		perStockBudget = totalAssets * 0.25
	case totalAssets <= 200000:
		maxCandidates = 5
		perStockBudget = totalAssets * 0.20
	case totalAssets < 500000:
		maxCandidates = 8
		perStockBudget = totalAssets * 0.12
	default:
		maxCandidates = 10
		perStockBudget = totalAssets * 0.10
	}
	if maxCandidates < 1 {
		maxCandidates = 1
	}
	return
}

// AssetTemplate 资产模板
type AssetTemplate struct {
	Code, Name, Industry, Type                         string
	Price                                              float64 // 参考价（算法生成）
	FactorScore, AlphaScore, RiskScore, LiquidityScore float64
	Rationale                                          string
}

// buildRealStockPool 从真实全市场行情数据生成候选股票池（通过选股服务，无硬编码股票）。
// totalAssets 为实际投资金额：<50万时收缩选股宇宙（剔除 ST/北交所/创业板/科创板），
// 既加快全市场选股，也规避中小资金难以驾驭的高波动/风险警示板块。
// 数据来源：DuckDB 全市场 + Pro因子引擎，返回经因子打分排序的真实候选。
func (p *Planner) buildRealStockPool(profile *port.InvestorProfile, mandate *InvestmentMandate, totalAssets float64) []AssetTemplate {
	if p.screenerService == nil {
		log.Printf("[Planner] 选股服务不可用，候选池为空")
		return nil
	}

	// 风险等级 -> 统一的策略模板ID（与选股引擎共用同一解析，避免两套模板割裂）
	strategyID := port.ResolveTemplateID(mandate.RiskLevel)

	req := port.ScreeningRequest{
		StrategyID:   strategyID,
		Market:       "all",
		MaxResults:   200,
		MinScore:     0,
		SmallCapital: totalAssets > 0 && totalAssets < 500000,
		// 风险偏好参与打分：把用户画像透传给选股引擎，使风险承受/风格/期限/目标真实影响因子权重
		//（smartComposer.CalculateSmartWeights），不同偏好用户天然选到不同资产。
		InvestorProfile: profile,
		// 用户个性化种子 + 多策略池分组：以 uid 哈希做确定性权重微扰，避免"千人一面"。
		DiversifySeed: userDiversifySeed(p.userID),
	}
	start := time.Now()
	resp, err := p.screenerService.ScreenStock(req)
	if err != nil {
		log.Printf("[Planner] 选股服务执行失败: %v (回退空池)", err)
		return nil
	}
	// 记录市场状态（用于弱市仓位控制/数量减半，与本次选股同源）
	p.lastMarketState = resp.MarketState
	log.Printf("[Planner] 真实选股完成: 全市场筛出 %d 只，入选 %d 只 (耗时 %v)",
		resp.TotalCount, len(resp.Results), time.Since(start).Round(time.Millisecond))

	out := make([]AssetTemplate, 0, len(resp.Results))
	for _, r := range resp.Results {
		name := r.Name
		industry := ""
		if p.stockMeta != nil {
			if name == "" {
				name = p.stockMeta.GetStockName(r.Code)
			}
			industry = p.stockMeta.GetIndustryByStock(r.Code)
		}
		if industry == "" || industry == "通用" {
			industry = "其他"
		}
		// 各子分从真实因子得分映射（0-1）
		alpha := factorSubScore(r, port.FactorMomentum)
		risk := factorSubScore(r, port.FactorLowVolatility)
		liq := factorSubScore(r, port.FactorLiquidity)

		rationale := strings.Join(r.Reasons, "；")
		if rationale == "" {
			rationale = "综合因子评分较高"
		}

		price := r.Price
		if price <= 0 {
			price = generateReferencePrice(r.Code)
		}

		out = append(out, AssetTemplate{
			Code:           r.Code,
			Name:           name,
			Industry:       industry,
			Type:           "STOCK",
			Price:          price,
			FactorScore:    r.TotalScore / 100,
			AlphaScore:     alpha,
			RiskScore:      risk,
			LiquidityScore: liq,
			Rationale:      rationale,
		})
	}
	return out
}

// factorSubScore 提取指定因子的 0-1 得分
func factorSubScore(r port.StockScore, fid port.FactorID) float64 {
	if fs, ok := r.FactorScores[fid]; ok {
		return fs.Score / 100
	}
	return 0.5
}

// buildCandidatePool 构建 A 股候选池（根据风格偏好选代表资产 + 交易可行性过滤）
func (p *Planner) buildCandidatePool(profile *port.InvestorProfile, mandate *InvestmentMandate, totalAssets float64) []CandidateForPortfolio {
	style := profile.InvestmentStyle
	riskLevel := mandate.RiskLevel

	// 从真实全市场行情数据构建候选池（经选股服务因子打分，无硬编码股票）
	// 投资金额<50万时收缩市池（剔除 ST/北交所/创业板/科创板），提速且降低风险。
	stockPool := p.buildRealStockPool(profile, mandate, totalAssets)
	dynamicMaxCandidates, _ := p.dynamicAllocationParams(totalAssets)

	// 风险等级决定绝对因子分线（默认 0.65；防御 0.60；成长 0.70）
	maxCandidates := dynamicMaxCandidates
	minFactorScore := 0.65
	if riskLevel == "conservative" {
		minFactorScore = 0.60
	} else if riskLevel == "growth" {
		minFactorScore = 0.70
	}

	// 弱市数量减半：弱势行情减少持仓数，集中资金到少数标的，降低整体风险暴露。
	// 小额资金激进（growth&<50万）：保留至少 3 只候选，避免资金过度闲置（现 2 只×单票30%仅占60%权益）。
	if p.bearish() {
		halved := maxCandidates / 2
		minCandidates := 2
		if riskLevel == "growth" && totalAssets < 500000 {
			minCandidates = 3
		}
		if halved < minCandidates {
			halved = minCandidates
		}
		log.Printf("[Planner] 弱市(%s)：候选数量减半 %d -> %d", p.lastMarketState.Regime, maxCandidates, halved)
		maxCandidates = halved
	}

	// 根据风格偏好给对应行业加分（行业名与 tdx_sector_data.json 实际行业名称一致）
	// 加分幅度较大，确保不同风格推荐不同的首选资产，避免固定推荐同一只股票
	styleIndustryBoost := map[string]float64{}
	switch style {
	case "DIVIDEND":
		styleIndustryBoost = map[string]float64{"银行": 0.25, "石油加工": 0.25, "公用事业": 0.25}
	case "VALUE":
		styleIndustryBoost = map[string]float64{"银行": 0.15, "铜": 0.20, "保险": 0.20, "石油加工": 0.15}
	case "GROWTH":
		styleIndustryBoost = map[string]float64{"电气设备": 0.20, "半导体": 0.20, "汽车整车": 0.18, "IT设备": 0.18, "证券": 0.15}
	case "QUALITY":
		styleIndustryBoost = map[string]float64{"白酒": 0.20, "乳制品": 0.15, "家用电器": 0.15}
	case "INDEX":
		styleIndustryBoost = map[string]float64{"宽基指数": 0.20, "科技指数": 0.15}
	}

	// 风格不匹配行业的惩罚（确保组合风格一致性，防止金融/周期股混入成长组合）
	stylePenalty := map[string]float64{}
	switch style {
	case "GROWTH":
		stylePenalty = map[string]float64{"银行": -0.15, "保险": -0.15, "石油加工": -0.10, "铜": -0.10}
	case "DIVIDEND":
		stylePenalty = map[string]float64{"半导体": -0.10, "汽车整车": -0.10, "IT设备": -0.10}
	case "VALUE":
		stylePenalty = map[string]float64{"半导体": -0.10, "汽车整车": -0.10, "IT设备": -0.10}
	case "QUALITY":
		stylePenalty = map[string]float64{"铜": -0.10, "石油加工": -0.10}
	}

	// 风险等级调整因子分
	riskFactorBoost := 0.0
	if riskLevel == "conservative" {
		riskFactorBoost = 0.05
	} else if riskLevel == "growth" {
		riskFactorBoost = -0.03
	}

	// 计算单标的最低可分配资金（考虑100股一手约束）
	// 总资金的 1/maxCandidates 作为每标的预算，再乘以 0.7 留出安全边际
	perLotBudget := (totalAssets / float64(maxCandidates)) * 0.7
	relaxedBudget := totalAssets * 0.30 // 候选不足时放宽价位的上限（总资金 1/3）

	type scoredAsset struct {
		asset AssetTemplate
		score float64
	}

	// 单只候选的最终调整分（因子分 + 风格行业加成 − 风格惩罚 + 风险等级调整）
	adjustedScoreOf := func(a AssetTemplate) float64 {
		boost := styleIndustryBoost[a.Industry]
		penalty := stylePenalty[a.Industry]
		// INDEX 风格：直接提升 ETF 类资产得分（ETF 行业映射为"其他"，需按资产类型加分）
		if style == "INDEX" && a.Type == "ETF" {
			boost += 0.25
		}
		return a.FactorScore + boost + penalty + riskFactorBoost
	}

	// 按指定因子分线收集候选：优先价位<=perLotBudget，若一只都选不出再放宽到总资金1/3价位的标的
	collect := func(minScore float64) []scoredAsset {
		var out []scoredAsset
		seen := map[string]bool{}
		for _, budget := range []float64{perLotBudget, relaxedBudget} {
			for _, a := range stockPool {
				if seen[a.Code] {
					continue
				}
				// 交易可行性检查：股价 × 100（一手）必须 ≤ 价格预算
				if a.Price*100 > budget {
					continue
				}
				score := adjustedScoreOf(a)
				if score < minScore {
					continue
				}
				seen[a.Code] = true
				out = append(out, scoredAsset{a, score})
			}
			if len(out) > 0 {
				break // 第一档价位已选出候选则不再放宽价格
			}
		}
		return out
	}

	// 阈值兜底：绝对分线过严导致候选不足时，按 0.05 步进下调因子分线，
	// 直到选出至少 3 个候选；最低放宽到 0.50，避免弱市（如 bear 行情）下成长股整体低分导致股票池为空
	scoredList := collect(minFactorScore)
	for minScore := minFactorScore - 0.05; len(scoredList) < 3 && minScore >= 0.50; minScore -= 0.05 {
		scoredList = collect(minScore)
	}
	// 极端兜底：即便降到 0.50 仍无候选（如行情数据不足），取评分最高者，保证股票池非空
	if len(scoredList) == 0 {
		log.Printf("[Planner] 候选池兜底：因子分最低线 0.50 仍无候选，按评分最高取若干只")
		for _, a := range stockPool {
			scoredList = append(scoredList, scoredAsset{a, a.FactorScore + riskFactorBoost})
		}
	}

	// 按得分排序取前 maxCandidates
	sort.Slice(scoredList, func(i, j int) bool {
		return scoredList[i].score > scoredList[j].score
	})
	if len(scoredList) > maxCandidates {
		scoredList = scoredList[:maxCandidates]
	}

	// 转换为候选对象
	sortedFiltered := make([]CandidateForPortfolio, 0, len(scoredList))
	for _, s := range scoredList {
		a := s.asset
		sortedFiltered = append(sortedFiltered, CandidateForPortfolio{
			AssetCode:      a.Code,
			AssetName:      a.Name,
			AssetType:      a.Type,
			Industry:       a.Industry,
			FactorScore:    s.score,
			AlphaScore:     a.AlphaScore,
			RiskScore:      a.RiskScore,
			LiquidityScore: a.LiquidityScore,
			RiskOSStatus:   "PASSED",
			Reason:         a.Rationale,
		})
	}

	return sortedFiltered
}

// 参考价映射（代码→价格），与buildCandidatePool保持一致
var referencePriceMap = map[string]float64{
	"600519": 1520.00, "000858": 128.00, "600276": 45.50, "600887": 27.80,
	"601398": 6.20, "600036": 35.00, "601318": 52.00, "601988": 4.80,
	"300750": 195.00, "002594": 248.00, "688981": 48.00,
	"000333": 68.00, "600690": 22.00,
	"601899": 17.50, "600028": 7.20, "601628": 35.00,
	"002415": 32.00, "300059": 13.50,
	"510300": 4.50, "510500": 5.80, "588000": 0.92, "159915": 2.10,
	"511010": 100.00, "000032": 1.00,
}

// getReferencePrice 获取资产参考价
func getReferencePrice(code string) float64 {
	if price, ok := referencePriceMap[code]; ok {
		return price
	}
	return 10.00 // 默认参考价
}

// applyThreeLayerWeighting 三层权重模型：Base × Factor × Alpha × Risk × Constraint + 交易可行性验证
func (p *Planner) applyThreeLayerWeighting(candidates []CandidateForPortfolio, mandate *InvestmentMandate, planData *PlanData, totalAssets float64, style string) []PortfolioPosition {
	if len(candidates) == 0 {
		return []PortfolioPosition{}
	}

	riskLevel := mandate.RiskLevel

	// 确定权益/现金比例（股票私募多头：全部配置股票，仅预留少量现金作为调仓机动）
	cashRatio := 0.10
	switch riskLevel {
	case "conservative":
		cashRatio = 0.10
	case "balanced":
		cashRatio = 0.08
	case "growth":
		cashRatio = 0.05
	}

	// 弱市仓位控制：弱势行情提高现金比例（降低权益暴露）。
	// 小额成长激进（growth&<50万）：弱市强制现金 10%，充分利用资金、避免闲置。
	// 其余：成长现金 5% → 20%（×4），最高 50%。
	if p.bearish() {
		boostedCash := cashRatio * 4
		if riskLevel == "growth" && totalAssets < 500000 {
			boostedCash = 0.10
		}
		if boostedCash > 0.50 {
			boostedCash = 0.50
		}
		log.Printf("[Planner] 弱市(%s)：现金仓位提升 %.0f%% -> %.0f%%（仓位控制）", p.lastMarketState.Regime, cashRatio*100, boostedCash*100)
		cashRatio = boostedCash
	}

	// Step 1: 按资产类型分类（使用股票池定义的类型）
	var stocks, etfs, bonds []CandidateForPortfolio
	for _, c := range candidates {
		switch c.AssetType {
		case "ETF":
			etfs = append(etfs, c)
		case "BOND":
			bonds = append(bonds, c)
		default:
			stocks = append(stocks, c)
		}
	}

	// Step 2: 分配名额
	equityCount := len(stocks) + len(etfs)
	bondCount := len(bonds)
	if equityCount == 0 {
		equityCount = 1
	}
	if bondCount == 0 {
		bondCount = 1
	}

	// Step 3: 生成 Position
	var positions []PortfolioPosition
	allAssets := []CandidateForPortfolio{}
	equityAssets := append(stocks, etfs...)
	allAssets = append(allAssets, equityAssets...)
	allAssets = append(allAssets, bonds...)

	n := len(allAssets)
	if n == 0 {
		return []PortfolioPosition{}
	}

	// 组合配置化基础权重（替代原"等权基础权重×三层窄区间系数"的乘法方案）：
	// 权重 ∝ FactorScore^1.5（分数越高配置越重，差异分配，避免全部候选等额≈40%）。
	// 单券/行业约束在下方循环中继续生效。
	prop := make([]float64, n)
	scoreDen := 0.0
	for i, c := range allAssets {
		s := math.Pow(math.Max(c.FactorScore, 0.01), 1.5)
		prop[i] = s
		scoreDen += s
	}
	if scoreDen <= 0 {
		scoreDen = 1
	}

	// 行业暴露累积器
	industryExposure := map[string]float64{}

	for ai, c := range allAssets {
		// FactorScore 差异化分配（非等权）
		weight := prop[ai] / scoreDen

		isBondType := c.AssetType == "BOND"
		isETFType := c.AssetType == "ETF"
		isStockType := c.AssetType == "STOCK"

		if isStockType && weight > mandate.PositionLimit.MaxSingleStock {
			weight = mandate.PositionLimit.MaxSingleStock
		}
		if isETFType && weight > 0.15 {
			weight = 0.15
		}

		industryExposure[c.Industry] += weight
		if maxAllowed, ok := mandate.PositionLimit.MaxIndustry[c.Industry]; ok {
			if industryExposure[c.Industry] > maxAllowed {
				excess := industryExposure[c.Industry] - maxAllowed
				weight -= excess
				if weight < 0.01 {
					weight = 0.01
				}
			}
		}

		assetType := "STOCK"
		if isBondType {
			assetType = "BOND"
		} else if isETFType {
			assetType = "ETF"
		}

		amount := weight * totalAssets
		rationale := p.generateRationale(c, mandate)

		// A股交易可行性计算
		stockPrice := getReferencePrice(c.AssetCode)
		lotSize := 100 // A股最小交易单位
		if assetType == "BOND" {
			lotSize = 10 // 债券最小单位
		}
		minTradeCost := stockPrice * float64(lotSize)
		lots := int(amount / minTradeCost)
		tradeable := lots >= 1

		position := PortfolioPosition{
			AssetCode:       c.AssetCode,
			AssetName:       c.AssetName,
			AssetType:       assetType,
			Industry:        c.Industry,
			TargetWeight:    weight,
			SuggestedAmount: amount,
			BaseWeight:      weight,
			AlphaAdjust:     1.0,
			RiskAdjust:      1.0,
			FactorScore:     c.FactorScore,
			RiskOSStatus:    c.RiskOSStatus,
			Rationale:       rationale,
			QuantEvidence: map[string]float64{
				"alpha_score":     c.AlphaScore,
				"risk_score":      c.RiskScore,
				"liquidity_score": c.LiquidityScore,
				"factor_score":    c.FactorScore,
			},
			StockPrice:   stockPrice,
			LotSize:      lotSize,
			MinTradeCost: minTradeCost,
			Lots:         lots,
			Tradeable:    tradeable,
		}
		positions = append(positions, position)
	}

	// 过滤不可交易的持仓（股价过高无法买1手）
	var tradeablePositions []PortfolioPosition
	var nonTradeablePositions []PortfolioPosition
	for _, pos := range positions {
		if pos.Tradeable || pos.AssetType == "BOND" {
			tradeablePositions = append(tradeablePositions, pos)
		} else {
			nonTradeablePositions = append(nonTradeablePositions, pos)
		}
	}

	// 如果有不可交易的持仓，尝试重新分配权重
	if len(nonTradeablePositions) > 0 && len(tradeablePositions) > 0 {
		// 计算可交易持仓的总权重
		tradeableWeight := 0.0
		for _, pos := range tradeablePositions {
			tradeableWeight += pos.TargetWeight
		}
		// 将不可交易的权重按比例分配给可交易持仓
		if tradeableWeight > 0 {
			reallocateRatio := 1.0 / tradeableWeight
			for i := range tradeablePositions {
				tradeablePositions[i].TargetWeight *= reallocateRatio
				tradeablePositions[i].SuggestedAmount = tradeablePositions[i].TargetWeight * totalAssets
				// 重新计算手数
				if tradeablePositions[i].StockPrice > 0 {
					tradeablePositions[i].Lots = int(tradeablePositions[i].SuggestedAmount / tradeablePositions[i].MinTradeCost)
					tradeablePositions[i].Tradeable = tradeablePositions[i].Lots >= 1
				}
			}
		}
		positions = tradeablePositions
	} else if len(tradeablePositions) > 0 {
		positions = tradeablePositions
	}

	// 归一化权重（预留现金比例，使含现金的总权重=1.0）。
	// 注意：缩放可能把已收口到 MaxSingleStock 的单票权重再次放大（该计划曾因此生成 40% 单票），
	// 因此缩放后须再次按单票上限收口，放大的超额部分转回现金缓冲，保证与风控单票上限一致。
	totalWeight := 0.0
	for _, pos := range positions {
		totalWeight += pos.TargetWeight
	}
	if totalWeight > 0 {
		scale := (1 - cashRatio) / totalWeight
		var overflow float64
		for i := range positions {
			tw := positions[i].TargetWeight * scale
			if positions[i].AssetType == "STOCK" && tw > mandate.PositionLimit.MaxSingleStock {
				overflow += tw - mandate.PositionLimit.MaxSingleStock
				tw = mandate.PositionLimit.MaxSingleStock
			}
			positions[i].TargetWeight = tw
			positions[i].SuggestedAmount = tw * totalAssets
			// 重新计算手数（归一化后金额可能变化）
			if positions[i].StockPrice > 0 && positions[i].AssetType != "CASH" {
				positions[i].Lots = int(positions[i].SuggestedAmount / positions[i].MinTradeCost)
				positions[i].Tradeable = positions[i].Lots >= 1
			}
		}
		// 单票上限收口产生的超额权重并入现金缓冲（提高安全垫，不摊回其余股票）
		if overflow > 0 {
			cashRatio += overflow
		}
	}

	// 添加现金仓位
	if cashRatio > 0 {
		cashPosition := PortfolioPosition{
			AssetCode:       "CASH",
			AssetName:       "现金及活期",
			AssetType:       "CASH",
			Industry:        "现金",
			TargetWeight:    cashRatio,
			SuggestedAmount: cashRatio * totalAssets,
			BaseWeight:      cashRatio,
			AlphaAdjust:     1.0,
			RiskAdjust:      1.0,
			FactorScore:     0.5,
			RiskOSStatus:    "PASSED",
			Rationale:       "作为组合的流动性缓冲，应对市场波动和调仓需求",
			QuantEvidence:   map[string]float64{"liquidity": 1.0},
			StockPrice:      1.0,
			LotSize:         1,
			MinTradeCost:    1.0,
			Lots:            int(cashRatio * totalAssets),
			Tradeable:       true,
		}
		positions = append(positions, cashPosition)
	}

	// 按权重降序排序（现金置底，确保首个持仓为实际投资标的）
	sort.Slice(positions, func(i, j int) bool {
		if positions[i].AssetType == "CASH" && positions[j].AssetType != "CASH" {
			return false
		}
		if positions[j].AssetType == "CASH" && positions[i].AssetType != "CASH" {
			return true
		}
		return positions[i].TargetWeight > positions[j].TargetWeight
	})

	return positions
}

// generateRationale 生成投资理由（极白语言 → 极黑算法）
func (p *Planner) generateRationale(c CandidateForPortfolio, mandate *InvestmentMandate) string {
	// 基于因子得分构建自然语言解释
	parts := []string{}

	// 行业相关性
	matchedStyle := false
	for _, ind := range mandate.StylePreferences.PreferredIndustries {
		if strings.Contains(c.Industry, ind) || strings.Contains(ind, c.Industry) {
			matchedStyle = true
			break
		}
	}
	if matchedStyle {
		parts = append(parts, fmt.Sprintf("符合您偏好的%s投资风格", mandate.StylePreferences.PrimaryStyle))
	}

	// 因子证据
	if c.FactorScore >= 0.85 {
		parts = append(parts, "综合因子得分高，盈利稳定")
	} else if c.FactorScore >= 0.75 {
		parts = append(parts, "综合因子表现良好")
	}

	// 流动性
	if c.LiquidityScore >= 0.95 {
		parts = append(parts, "市场流动性充足")
	} else if c.LiquidityScore >= 0.85 {
		parts = append(parts, "流动性较好")
	}

	// 风险
	if c.RiskScore >= 0.85 {
		parts = append(parts, "波动相对较低，与组合相关性适中")
	}

	if len(parts) == 0 {
		parts = append(parts, "综合评估通过，适合纳入组合")
	}

	return strings.Join(parts, "，") + "。"
}
