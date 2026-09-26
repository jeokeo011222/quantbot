package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/quantpilot/quantpilot/internal/brain/brainutil"
	"github.com/quantpilot/quantpilot/internal/brain/llm"
	"github.com/quantpilot/quantpilot/internal/brain/port"
	"github.com/quantpilot/quantpilot/internal/brain/toolkit"
)

// BaseAgent 基础 Agent 实现（升级版：多模式 + Skill + Trajectory + 子Agent）
type BaseAgent struct {
	ID          string
	Name        string
	Type        string
	Description string
	Role        AgentRole

	SystemPrompt string
	Tools        []port.ToolExecutor
	LLMClient    llm.Client
	ToolRegistry *ToolRegistry

	// 新特性
	Mode           AgentMode
	Skill          *Skill
	Trajectory     *Trajectory
	SubAgents      map[string]*BaseAgent
	MaxIterations  int
	MaxContextMsgs int // 上下文压缩阈值

	session   *SessionState
	history   []llm.Message
	iteration int
}

// SessionState 会话状态
type SessionState struct {
	ID           string
	StartTime    time.Time
	EndTime      time.Time
	Confidence   float64
	Decision     interface{}
	DecisionType string
	ToolCalls    []toolkit.ToolCallRecord
	Steps        []DecisionStep
	Status       string
	TrajectoryID string
	TokenUsage   TokenUsage
}

// DecisionStep 决策步骤
type DecisionStep struct {
	Step       int         `json:"step"`
	Type       string      `json:"type"`
	Content    string      `json:"content"`
	Timestamp  time.Time   `json:"timestamp"`
	ToolName   string      `json:"tool_name,omitempty"`
	ToolArgs   interface{} `json:"tool_args,omitempty"`
	ToolResult interface{} `json:"tool_result,omitempty"`
	Thought    string      `json:"thought,omitempty"`
	DurationMs int64       `json:"duration_ms,omitempty"`
}

// NewBaseAgent 创建基础 Agent
func NewBaseAgent(id, name, agentType, systemPrompt string) *BaseAgent {
	agent := &BaseAgent{
		ID:             id,
		Name:           name,
		Type:           agentType,
		SystemPrompt:   systemPrompt,
		Mode:           ModeStandard,
		MaxIterations:  5,
		MaxContextMsgs: 20,
		SubAgents:      make(map[string]*BaseAgent),
		history:        make([]llm.Message, 0),
	}

	agent.history = append(agent.history, llm.Message{
		Role:    "system",
		Content: systemPrompt,
	})

	return agent
}

// NewBaseAgentWithRole 创建带角色的基础Agent
func NewBaseAgentWithRole(id string, role AgentRole, systemPrompt string) *BaseAgent {
	agent := NewBaseAgent(id, agentName(role), string(role), systemPrompt)
	agent.Role = role
	return agent
}

// LoadSkill 加载技能（从Skill初始化Agent）
func (a *BaseAgent) LoadSkill(skill *Skill) {
	a.Skill = skill
	a.SystemPrompt = skill.BuildSystemPrompt()
	a.Tools = skill.Tools
	a.history = []llm.Message{
		{Role: "system", Content: a.SystemPrompt},
	}
	log.Printf("[BaseAgent] Loaded skill: %s v%s", skill.Name, skill.Version)
}

// SetMode 设置运行模式
func (a *BaseAgent) SetMode(mode AgentMode) {
	a.Mode = mode
	switch mode {
	case ModeMinimal:
		a.MaxIterations = 1
	case ModeStandard:
		a.MaxIterations = 5
	case ModePTC:
		a.MaxIterations = 7
	case ModeCreator:
		a.MaxIterations = 10
	}
}

// RegisterTool 注册工具
func (a *BaseAgent) RegisterTool(tool port.ToolExecutor) {
	a.Tools = append(a.Tools, tool)
}

// SetLLMClient 设置 LLM 客户端
func (a *BaseAgent) SetLLMClient(client llm.Client) {
	a.LLMClient = client
}

// SetToolRegistry 设置中央工具注册表（用于运行时权限强制校验）
func (a *BaseAgent) SetToolRegistry(registry *ToolRegistry) {
	a.ToolRegistry = registry
}

// RegisterSubAgent 注册子Agent
func (a *BaseAgent) RegisterSubAgent(name string, sub *BaseAgent) {
	a.SubAgents[name] = sub
}

// GetAvailableTools 获取可用工具列表
func (a *BaseAgent) GetAvailableTools() []llm.Tool {
	tools := make([]llm.Tool, 0)
	for _, t := range a.Tools {
		td := t.GetDefinition()
		tools = append(tools, llm.Tool{
			Type: "function",
			Function: llm.ToolFunction{
				Name:        td.Function.Name,
				Description: td.Function.Description,
				Parameters:  td.Function.Parameters,
			},
		})
	}
	return tools
}

// Run 启动 Agent 会话（主入口）
func (a *BaseAgent) Run(ctx context.Context, input string) (*SessionState, error) {
	switch a.Mode {
	case ModePTC:
		return a.runPTC(ctx, input)
	case ModeMinimal:
		return a.runMinimal(ctx, input)
	case ModeCreator:
		return a.runCreator(ctx, input)
	default:
		return a.runStandard(ctx, input)
	}
}

// RunWithTrajectory 启动会话并记录轨迹
func (a *BaseAgent) RunWithTrajectory(ctx context.Context, input string) (*SessionState, *Trajectory, error) {
	traj := NewTrajectory(a.ID, a.Name, a.Role, a.Mode, input)
	if a.Skill != nil {
		traj.SkillID = a.Skill.ID
	}
	a.Trajectory = traj

	session, err := a.Run(ctx, input)

	if err != nil {
		traj.Complete("", "failed")
		traj.AddStep(TrajectoryStep{
			Step:      len(traj.Steps) + 1,
			Timestamp: time.Now(),
			Type:      StepError,
			Result:    err.Error(),
		})
	} else {
		traj.Complete(fmt.Sprintf("%v", session.Decision), "completed")
		traj.Confidence = session.Confidence
		traj.TokenUsage = session.TokenUsage
	}

	session.TrajectoryID = traj.ID
	return session, traj, err
}

// ==================== 标准ReAct模式 ====================

func (a *BaseAgent) runStandard(ctx context.Context, input string) (*SessionState, error) {
	if a.LLMClient == nil {
		return nil, fmt.Errorf("LLM client not set for agent %s", a.ID)
	}

	session := a.initSession(input)

	a.history = append(a.history, llm.Message{Role: "user", Content: input})

	for a.iteration < a.MaxIterations {
		a.iteration++

		// 上下文压缩检查
		a.maybeCompressContext()

		tools := a.GetAvailableTools()
		response, err := a.LLMClient.Chat(ctx, a.history, tools)
		if err != nil {
			return a.failSession(session, err)
		}

		if len(response.Choices) == 0 {
			continue
		}

		assistantMsg := response.Choices[0].Message

		// 记录token使用
		session.TokenUsage.PromptTokens += response.Usage.PromptTokens
		session.TokenUsage.CompletionTokens += response.Usage.CompletionTokens
		session.TokenUsage.TotalTokens += response.Usage.TotalTokens

		// 构建 assistant 消息（含 tool_calls）
		assistantHistoryMsg := a.buildAssistantMsg(assistantMsg)
		a.history = append(a.history, assistantHistoryMsg)

		// 记录trajectory步骤
		if a.Trajectory != nil {
			a.Trajectory.AddStep(TrajectoryStep{
				Step:       a.iteration,
				Timestamp:  time.Now(),
				Type:       StepThink,
				Thought:    assistantMsg.Content,
				TokenDelta: response.Usage.TotalTokens,
			})
		}

		// 提取工具调用
		toolCalls := extractToolCallsFromResponse(response)
		if len(toolCalls) > 0 {
			a.handleToolCalls(ctx, toolCalls, session)
		} else {
			return a.completeSession(session, assistantMsg.Content)
		}
	}

	return a.maxIterationSession(session)
}

// ==================== PTC模式（Plan-Tool-Critique） ====================

func (a *BaseAgent) runPTC(ctx context.Context, input string) (*SessionState, error) {
	if a.LLMClient == nil {
		return nil, fmt.Errorf("LLM client not set for agent %s", a.ID)
	}

	session := a.initSession(input)

	// Phase 1: 规划
	planPrompt := fmt.Sprintf("请分析以下任务并制定执行计划：\n\n%s\n\n请以JSON格式输出计划，包含：\n1. 目标\n2. 关键步骤\n3. 需要的工具\n4. 预期结果", input)
	a.history = append(a.history, llm.Message{Role: "user", Content: planPrompt})

	planResponse, err := a.LLMClient.Chat(ctx, a.history, nil)
	if err != nil {
		return a.failSession(session, err)
	}
	planContent := planResponse.Choices[0].Message.Content
	session.TokenUsage.add(planResponse.Usage)

	if a.Trajectory != nil {
		a.Trajectory.AddStep(TrajectoryStep{
			Step:      1,
			Timestamp: time.Now(),
			Type:      StepThink,
			Thought:   planContent,
		})
	}

	// Phase 2: 执行（最多MaxIterations-1次工具调用）
	a.history = append(a.history, llm.Message{Role: "user", Content: input})
	tools := a.GetAvailableTools()

	for a.iteration < a.MaxIterations {
		a.iteration++

		a.maybeCompressContext()

		response, err := a.LLMClient.Chat(ctx, a.history, tools)
		if err != nil {
			return a.failSession(session, err)
		}

		assistantMsg := response.Choices[0].Message
		session.TokenUsage.add(response.Usage)
		a.history = append(a.history, a.buildAssistantMsg(assistantMsg))

		if a.Trajectory != nil {
			a.Trajectory.AddStep(TrajectoryStep{
				Step:       a.iteration + 1,
				Timestamp:  time.Now(),
				Type:       StepThink,
				Thought:    assistantMsg.Content,
				TokenDelta: response.Usage.TotalTokens,
			})
		}

		toolCalls := extractToolCallsFromResponse(response)
		if len(toolCalls) > 0 {
			a.handleToolCalls(ctx, toolCalls, session)
		} else {
			// Phase 3: 反思/批评
			critiquePrompt := fmt.Sprintf("请审查以下结果是否满足计划要求：\n\n计划：%s\n\n结果：%s\n\n如果满意请回答SUCCESS，如果不满意请继续尝试。",
				planContent, assistantMsg.Content)

			critiqueResponse, err := a.LLMClient.Chat(ctx,
				append(a.history, llm.Message{Role: "user", Content: critiquePrompt}), nil)
			if err != nil {
				return a.completeSession(session, assistantMsg.Content)
			}

			critiqueText := critiqueResponse.Choices[0].Message.Content
			session.TokenUsage.add(critiqueResponse.Usage)

			if a.Trajectory != nil {
				a.Trajectory.AddStep(TrajectoryStep{
					Step:      len(a.Trajectory.Steps) + 1,
					Timestamp: time.Now(),
					Type:      StepCritique,
					Thought:   critiqueText,
				})
			}

			if strings.Contains(strings.ToUpper(critiqueText), "SUCCESS") {
				return a.completeSession(session, assistantMsg.Content)
			}
			// 不满意则继续循环
			a.history = append(a.history, llm.Message{Role: "user", Content: critiquePrompt})
		}
	}

	return a.maxIterationSession(session)
}

// ==================== 最小模式（纯LLM，无工具） ====================

func (a *BaseAgent) runMinimal(ctx context.Context, input string) (*SessionState, error) {
	if a.LLMClient == nil {
		return nil, fmt.Errorf("LLM client not set for agent %s", a.ID)
	}

	session := a.initSession(input)
	a.history = append(a.history, llm.Message{Role: "user", Content: input})

	response, err := a.LLMClient.Chat(ctx, a.history, nil)
	if err != nil {
		return a.failSession(session, err)
	}

	session.TokenUsage.add(response.Usage)

	if len(response.Choices) > 0 {
		assistantMsg := response.Choices[0].Message

		if a.Trajectory != nil {
			a.Trajectory.AddStep(TrajectoryStep{
				Step:      1,
				Timestamp: time.Now(),
				Type:      StepFinalAnswer,
				Thought:   assistantMsg.Content,
			})
		}

		return a.completeSession(session, assistantMsg.Content)
	}

	return a.maxIterationSession(session)
}

// ==================== Creator模式（创意生成，更多迭代） ====================

func (a *BaseAgent) runCreator(ctx context.Context, input string) (*SessionState, error) {
	if a.LLMClient == nil {
		return nil, fmt.Errorf("LLM client not set for agent %s", a.ID)
	}

	session := a.initSession(input)
	a.history = append(a.history, llm.Message{Role: "user", Content: input})

	// 创意模式：多轮生成+筛选
	bestResponse := ""
	bestScore := 0.0
	tools := a.GetAvailableTools()

	for a.iteration < a.MaxIterations {
		a.iteration++

		a.maybeCompressContext()

		response, err := a.LLMClient.Chat(ctx, a.history, tools)
		if err != nil {
			return a.failSession(session, err)
		}

		session.TokenUsage.add(response.Usage)

		if len(response.Choices) == 0 {
			continue
		}

		assistantMsg := response.Choices[0].Message
		a.history = append(a.history, a.buildAssistantMsg(assistantMsg))

		if a.Trajectory != nil {
			a.Trajectory.AddStep(TrajectoryStep{
				Step:      a.iteration,
				Timestamp: time.Now(),
				Type:      StepThink,
				Thought:   assistantMsg.Content,
			})
		}

		toolCalls := extractToolCallsFromResponse(response)
		if len(toolCalls) > 0 {
			a.handleToolCalls(ctx, toolCalls, session)
			continue
		}

		// 创意评分
		score := scoreResponse(assistantMsg.Content)
		if score > bestScore {
			bestScore = score
			bestResponse = assistantMsg.Content
		}

		// 要求继续优化
		if a.iteration < a.MaxIterations-2 {
			a.history = append(a.history, llm.Message{
				Role:    "user",
				Content: fmt.Sprintf("请进一步优化以上方案，考虑更多角度和可能性。当前最佳评分: %.2f/1.0", bestScore),
			})
		}
	}

	if bestResponse != "" {
		return a.completeSession(session, bestResponse)
	}
	return a.maxIterationSession(session)
}

// ==================== 子Agent调度 ====================

// DispatchSubAgent 派遣子Agent执行任务
func (a *BaseAgent) DispatchSubAgent(ctx context.Context, name string, task string) (*SessionState, error) {
	sub, ok := a.SubAgents[name]
	if !ok {
		return nil, fmt.Errorf("sub-agent %s not found", name)
	}

	log.Printf("[BaseAgent] %s dispatching to sub-agent: %s", a.Name, name)

	if a.Trajectory != nil {
		a.Trajectory.AddStep(TrajectoryStep{
			Step:      len(a.Trajectory.Steps) + 1,
			Timestamp: time.Now(),
			Type:      StepSubAgent,
			Action:    name,
			Thought:   task,
		})
		a.Trajectory.SubAgents = append(a.Trajectory.SubAgents, name)
	}

	result, err := sub.Run(ctx, task)
	if err != nil {
		return nil, fmt.Errorf("sub-agent %s failed: %w", name, err)
	}

	// 把子Agent结果注入到当前上下文
	resultJSON, _ := json.Marshal(result.Decision)
	a.history = append(a.history, llm.Message{
		Role:    "user",
		Content: fmt.Sprintf("[子Agent %s 返回结果]: %s", name, string(resultJSON)),
	})

	return result, nil
}

// ==================== 上下文压缩 ====================

func (a *BaseAgent) maybeCompressContext() {
	// System prompt + history 消息
	userAssistantCount := 0
	for _, msg := range a.history {
		if msg.Role == "user" || msg.Role == "assistant" {
			userAssistantCount++
		}
	}

	if userAssistantCount > a.MaxContextMsgs {
		a.compressContext()
	}
}

func (a *BaseAgent) compressContext() {
	// 保留：system prompt + 最后N轮对话
	// 压缩：中间的工具调用结果
	if len(a.history) <= a.MaxContextMsgs {
		return
	}

	// 找最后一个user消息的位置
	lastUserIdx := -1
	for i := len(a.history) - 1; i >= 0; i-- {
		if a.history[i].Role == "user" {
			lastUserIdx = i
			break
		}
	}

	if lastUserIdx <= 1 {
		return // 无需压缩
	}

	// 简化的压缩策略：保留system + 最近10条消息
	// 远期可升级为LLM摘要压缩
	systemMsgs := make([]llm.Message, 0)
	recentMsgs := make([]llm.Message, 0)

	for _, msg := range a.history {
		if msg.Role == "system" {
			systemMsgs = append(systemMsgs, msg)
		}
	}

	// 取最近10条非system消息
	nonSystem := make([]llm.Message, 0)
	for _, msg := range a.history {
		if msg.Role != "system" {
			nonSystem = append(nonSystem, msg)
		}
	}

	startIdx := 0
	if len(nonSystem) > 10 {
		startIdx = len(nonSystem) - 10
	}
	recentMsgs = nonSystem[startIdx:]

	a.history = append(systemMsgs, recentMsgs...)

	log.Printf("[BaseAgent] Context compressed: %d → %d messages",
		len(a.history)+(len(nonSystem)-len(recentMsgs)), len(a.history))
}

// ==================== 内部辅助方法 ====================

func (a *BaseAgent) initSession(input string) *SessionState {
	session := &SessionState{
		ID:         fmt.Sprintf("session_%s_%d", a.ID, time.Now().UnixNano()),
		StartTime:  time.Now(),
		Status:     "running",
		TokenUsage: TokenUsage{},
	}
	a.session = session
	a.iteration = 0
	return session
}

// responseMessageType 对应 ChatResult.Choices[0].Message 的匿名结构
type responseMessageType struct {
	Content          string             `json:"content"`
	Role             string             `json:"role"`
	ReasoningContent string             `json:"reasoning_content,omitempty"`
	ToolCalls        []llm.ToolCallInfo `json:"tool_calls,omitempty"`
}

func (a *BaseAgent) buildAssistantMsg(msg responseMessageType) llm.Message {
	assistantHistoryMsg := llm.Message{
		Role:             "assistant",
		Content:          msg.Content,
		ReasoningContent: msg.ReasoningContent,
	}

	if len(msg.ToolCalls) > 0 {
		for _, tc := range msg.ToolCalls {
			newCall := struct {
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			}{
				ID:   tc.ID,
				Type: tc.Type,
			}
			newCall.Function.Name = tc.Function.Name
			newCall.Function.Arguments = tc.Function.Arguments
			assistantHistoryMsg.ToolCalls = append(assistantHistoryMsg.ToolCalls, newCall)
		}
	}

	return assistantHistoryMsg
}

func (a *BaseAgent) handleToolCalls(ctx context.Context, toolCalls []toolCallInfo, session *SessionState) {
	for _, tc := range toolCalls {
		startTime := time.Now()
		toolResult, err := a.executeToolCall(ctx, tc)

		toolCallRecord := toolkit.ToolCallRecord{
			ToolName:  tc.Function.Name,
			Arguments: tc.Function.Arguments,
			Timestamp: time.Now(),
			Success:   err == nil,
		}

		if err != nil {
			toolCallRecord.Error = err.Error()
			log.Printf("[BaseAgent] Tool %s failed: %v", tc.Function.Name, err)

			if a.Trajectory != nil {
				a.Trajectory.AddStep(TrajectoryStep{
					Step:      len(a.Trajectory.Steps) + 1,
					Timestamp: time.Now(),
					Type:      StepError,
					Action:    tc.Function.Name,
					Result:    err.Error(),
				})
			}
		} else {
			toolCallRecord.Result = toolResult

			if a.Trajectory != nil {
				a.Trajectory.AddStep(TrajectoryStep{
					Step:        len(a.Trajectory.Steps) + 1,
					Timestamp:   time.Now(),
					Type:        StepToolCall,
					Action:      tc.Function.Name,
					ActionInput: tc.Function.Arguments,
					Observation: toolResult,
					Duration:    time.Since(startTime),
				})
			}
		}

		session.ToolCalls = append(session.ToolCalls, toolCallRecord)

		resultJSON, _ := json.Marshal(toolResult)
		a.history = append(a.history, llm.Message{
			Role:       "tool",
			Content:    string(resultJSON),
			ToolCallID: tc.ID,
		})

		session.Steps = append(session.Steps, DecisionStep{
			Step:       a.iteration,
			Type:       "tool_call",
			Content:    fmt.Sprintf("Executed tool: %s", tc.Function.Name),
			ToolName:   tc.Function.Name,
			ToolArgs:   tc.Function.Arguments,
			ToolResult: toolResult,
			Timestamp:  startTime,
			DurationMs: time.Since(startTime).Milliseconds(),
		})
	}
}

func (a *BaseAgent) completeSession(session *SessionState, content string) (*SessionState, error) {
	cleanedContent := brainutil.ExtractJSON(content)

	session.Decision = cleanedContent
	session.DecisionType = "final"
	session.Confidence = calculateConfidence(a.iteration, len(session.ToolCalls))
	session.Status = "completed"
	session.EndTime = time.Now()

	session.Steps = append(session.Steps, DecisionStep{
		Step:      a.iteration,
		Type:      "decide",
		Content:   cleanedContent,
		Timestamp: time.Now(),
	})

	if a.Trajectory != nil {
		a.Trajectory.AddStep(TrajectoryStep{
			Step:      len(a.Trajectory.Steps) + 1,
			Timestamp: time.Now(),
			Type:      StepFinalAnswer,
			Thought:   cleanedContent,
		})
	}

	a.logCompletion(session)
	return session, nil
}

func (a *BaseAgent) failSession(session *SessionState, err error) (*SessionState, error) {
	session.Status = "failed"
	session.EndTime = time.Now()
	session.DecisionType = "error"
	log.Printf("[Agent] %s failed: %v", a.Name, err)
	return session, err
}

func (a *BaseAgent) maxIterationSession(session *SessionState) (*SessionState, error) {
	session.Status = "completed"
	session.EndTime = time.Now()
	session.DecisionType = "max_iterations_reached"
	a.logCompletion(session)
	return session, nil
}

func (a *BaseAgent) logCompletion(session *SessionState) {
	log.Printf("[Agent] %s completed: %d iterations, %d tool calls, confidence %.2f, tokens %d",
		a.Name, a.iteration, len(session.ToolCalls), session.Confidence, session.TokenUsage.TotalTokens)
}

// ==================== 工具调用相关 ====================

type toolCallInfo struct {
	ID       string
	Function struct {
		Name      string
		Arguments string
	}
}

func extractToolCallsFromResponse(response *llm.ChatResult) []toolCallInfo {
	var calls []toolCallInfo

	if len(response.Choices) == 0 {
		return calls
	}

	apiToolCalls := response.Choices[0].Message.ToolCalls
	for _, tc := range apiToolCalls {
		calls = append(calls, toolCallInfo{
			ID: tc.ID,
		})
		calls[len(calls)-1].Function.Name = tc.Function.Name
		calls[len(calls)-1].Function.Arguments = tc.Function.Arguments
	}

	if len(calls) == 0 {
		content := response.Choices[0].Message.Content
		var toolCallStruct struct {
			ToolCalls []toolCallInfo `json:"tool_calls"`
		}
		if err := json.Unmarshal([]byte(content), &toolCallStruct); err == nil && len(toolCallStruct.ToolCalls) > 0 {
			return toolCallStruct.ToolCalls
		}
	}

	return calls
}

func (a *BaseAgent) executeToolCall(ctx context.Context, tc toolCallInfo) (interface{}, error) {
	toolName := tc.Function.Name
	var args map[string]interface{}

	if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
		return nil, fmt.Errorf("failed to parse tool arguments: %w", err)
	}

	// Runtime Enforcement: 权限强制校验
	if a.ToolRegistry != nil {
		allowed, err := a.ToolRegistry.CheckToolPermission(a.Role, toolName)
		if err != nil {
			log.Printf("[BaseAgent] ACTION_DENIED: %s (%s) 尝试调用工具 %s 失败: %v", a.Name, a.Role, toolName, err)
			return nil, fmt.Errorf("ACTION_DENIED: Agent %s (%s) 无权使用工具 %s: %v", a.Name, a.Role, toolName, err)
		}
		if !allowed {
			log.Printf("[BaseAgent] ACTION_DENIED: %s (%s) 尝试调用工具 %s 被拒绝", a.Name, a.Role, toolName)
			return nil, fmt.Errorf("ACTION_DENIED: Agent %s (%s) 无权使用工具 %s", a.Name, a.Role, toolName)
		}
	}

	for _, tool := range a.Tools {
		td := tool.GetDefinition()
		if td.Function.Name == toolName {
			return tool.Execute(ctx, args)
		}
	}

	return nil, fmt.Errorf("tool not found: %s", toolName)
}

// ==================== 辅助函数 ====================

func calculateConfidence(iterations, toolCalls int) float64 {
	baseConfidence := 0.5
	iterationBonus := 0.05 * float64(iterations)
	toolCallBonus := 0.02 * float64(toolCalls)

	confidence := baseConfidence + iterationBonus + toolCallBonus
	if confidence > 1.0 {
		confidence = 1.0
	}
	return confidence
}

func (tu *TokenUsage) add(usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}) {
	tu.PromptTokens += usage.PromptTokens
	tu.CompletionTokens += usage.CompletionTokens
	tu.TotalTokens += usage.TotalTokens
}

// scoreResponse 对响应进行简单创意评分
func scoreResponse(content string) float64 {
	score := 0.5
	length := len(content)

	if length > 100 {
		score += 0.1
	}
	if length > 300 {
		score += 0.1
	}
	if length > 500 {
		score += 0.1
	}

	// 结构化内容加分
	if strings.Contains(content, "1.") || strings.Contains(content, "一、") {
		score += 0.05
	}

	// 包含具体数字加分
	if containsDigit(content) {
		score += 0.05
	}

	if score > 1.0 {
		score = 1.0
	}
	return score
}

func containsDigit(s string) bool {
	for _, c := range s {
		if c >= '0' && c <= '9' {
			return true
		}
	}
	return false
}
