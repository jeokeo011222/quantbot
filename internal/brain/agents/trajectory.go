package agents

import (
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"
)

// Trajectory 轨迹追踪：记录 Agent 的完整决策过程
// 类似 Harness 的 append-only Session Log，全链路可回放

// Trajectory 一次 Agent 运行的完整轨迹
type Trajectory struct {
	ID          string              `json:"id"`
	AgentID     string              `json:"agent_id"`
	AgentName   string              `json:"agent_name"`
	AgentRole   AgentRole           `json:"agent_role"`
	SkillID     string              `json:"skill_id,omitempty"`
	StartTime   time.Time           `json:"start_time"`
	EndTime     time.Time           `json:"end_time"`
	Status      string              `json:"status"`
	Mode        AgentMode           `json:"mode"`
	Input       string              `json:"input"`
	Output      string              `json:"output"`
	Steps       []TrajectoryStep    `json:"steps"`
	SubAgents   []string            `json:"sub_agents,omitempty"`
	ParentID    string              `json:"parent_id,omitempty"`
	Confidence  float64             `json:"confidence"`
	TokenUsage  TokenUsage          `json:"token_usage"`
	Metadata    map[string]interface{} `json:"metadata,omitempty"`
}

// TrajectoryStep 轨迹中的单个步骤
type TrajectoryStep struct {
	Step        int           `json:"step"`
	Timestamp   time.Time     `json:"timestamp"`
	Type        StepType      `json:"type"`
	Thought     string        `json:"thought,omitempty"`
	Action      string        `json:"action,omitempty"`
	ActionInput interface{}   `json:"action_input,omitempty"`
	Observation interface{}   `json:"observation,omitempty"`
	Result      string        `json:"result,omitempty"`
	Duration    time.Duration `json:"duration"`
	TokenDelta  int           `json:"token_delta"`
}

// StepType 步骤类型
type StepType string

const (
	StepThink       StepType = "THINK"        // 思考
	StepToolCall    StepType = "TOOL_CALL"    // 工具调用
	StepToolResult  StepType = "TOOL_RESULT"  // 工具返回
	StepSubAgent    StepType = "SUB_AGENT"    // 子Agent调用
	StepCritique    StepType = "CRITIQUE"     // 反思/批评
	StepFinalAnswer StepType = "FINAL_ANSWER" // 最终回答
	StepError       StepType = "ERROR"        // 错误
)

// AgentMode Agent运行模式
type AgentMode string

const (
	ModeStandard AgentMode = "STANDARD" // 标准ReAct模式
	ModePTC      AgentMode = "PTC"      // Plan-Tool-Critique 模式
	ModeMinimal  AgentMode = "MINIMAL" // 最小模式（无工具）
	ModeCreator  AgentMode = "CREATOR"  // 创意模式
)

// TokenUsage Token用量统计
type TokenUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	EstimatedCost    float64 `json:"estimated_cost"`
}

// NewTrajectory 创建轨迹
func NewTrajectory(agentID, agentName string, role AgentRole, mode AgentMode, input string) *Trajectory {
	return &Trajectory{
		ID:        fmt.Sprintf("traj_%s_%d", agentID, time.Now().UnixNano()),
		AgentID:   agentID,
		AgentName: agentName,
		AgentRole: role,
		StartTime: time.Now(),
		Status:    "running",
		Mode:      mode,
		Input:     input,
		Steps:     make([]TrajectoryStep, 0),
		Metadata:  make(map[string]interface{}),
	}
}

// AddStep 添加步骤
func (t *Trajectory) AddStep(step TrajectoryStep) {
	t.Steps = append(t.Steps, step)
}

// Complete 完成轨迹
func (t *Trajectory) Complete(output string, status string) {
	t.EndTime = time.Now()
	t.Output = output
	t.Status = status
}

// Duration 计算总耗时
func (t *Trajectory) Duration() time.Duration {
	return t.EndTime.Sub(t.StartTime)
}

// ToJSON 序列化为JSON
func (t *Trajectory) ToJSON() string {
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return "{}"
	}
	return string(data)
}

// Summary 生成摘要
func (t *Trajectory) Summary() map[string]interface{} {
	toolCalls := 0
	toolResults := 0
	for _, s := range t.Steps {
		if s.Type == StepToolCall {
			toolCalls++
		} else if s.Type == StepToolResult {
			toolResults++
		}
	}

	return map[string]interface{}{
		"id":         t.ID,
		"agent":      t.AgentName,
		"role":       string(t.AgentRole),
		"mode":       string(t.Mode),
		"status":     t.Status,
		"duration":   t.Duration().String(),
		"steps":      len(t.Steps),
		"tool_calls": toolCalls,
		"tool_results": toolResults,
		"confidence": t.Confidence,
		"tokens":     t.TokenUsage.TotalTokens,
		"has_output": t.Output != "",
	}
}

// TrajectoryStore 轨迹存储（内存，可扩展为SQLite/DuckDB持久化）
type TrajectoryStore struct {
	mu          sync.RWMutex
	trajectories map[string]*Trajectory
	maxSize     int
}

// NewTrajectoryStore 创建轨迹存储
func NewTrajectoryStore(maxSize int) *TrajectoryStore {
	if maxSize <= 0 {
		maxSize = 1000
	}
	return &TrajectoryStore{
		trajectories: make(map[string]*Trajectory),
		maxSize:     maxSize,
	}
}

// Save 保存轨迹
func (s *TrajectoryStore) Save(traj *Trajectory) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 如果超出最大容量，删除最旧的
	if len(s.trajectories) >= s.maxSize {
		var oldestID string
		var oldestTime time.Time
		first := true
		for id, t := range s.trajectories {
			if first || t.StartTime.Before(oldestTime) {
				oldestID = id
				oldestTime = t.StartTime
				first = false
			}
		}
		delete(s.trajectories, oldestID)
		log.Printf("[TrajectoryStore] Evicted oldest trajectory: %s", oldestID)
	}

	s.trajectories[traj.ID] = traj
}

// Get 获取轨迹
func (s *TrajectoryStore) Get(id string) (*Trajectory, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.trajectories[id]
	return t, ok
}

// ListByAgent 按Agent列出轨迹
func (s *TrajectoryStore) ListByAgent(agentID string, limit int) []*Trajectory {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []*Trajectory
	for _, t := range s.trajectories {
		if t.AgentID == agentID {
			result = append(result, t)
		}
	}

	// 按时间排序（最新的在前）
	sortTrajectories(result)

	if limit > 0 && len(result) > limit {
		result = result[:limit]
	}
	return result
}

// ListByRole 按角色列出轨迹
func (s *TrajectoryStore) ListByRole(role AgentRole, limit int) []*Trajectory {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []*Trajectory
	for _, t := range s.trajectories {
		if t.AgentRole == role {
			result = append(result, t)
		}
	}

	sortTrajectories(result)

	if limit > 0 && len(result) > limit {
		result = result[:limit]
	}
	return result
}

// ListAll 列出所有轨迹
func (s *TrajectoryStore) ListAll(limit int) []*Trajectory {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]*Trajectory, 0, len(s.trajectories))
	for _, t := range s.trajectories {
		result = append(result, t)
	}

	sortTrajectories(result)

	if limit > 0 && len(result) > limit {
		result = result[:limit]
	}
	return result
}

// Clear 清空所有轨迹
func (s *TrajectoryStore) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.trajectories = make(map[string]*Trajectory)
}

// Stats 获取统计信息
func (s *TrajectoryStore) Stats() map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()

	byRole := make(map[string]int)
	totalTokens := 0
	for _, t := range s.trajectories {
		byRole[string(t.AgentRole)]++
		totalTokens += t.TokenUsage.TotalTokens
	}

	return map[string]interface{}{
		"total":        len(s.trajectories),
		"by_role":      byRole,
		"total_tokens": totalTokens,
		"capacity":     s.maxSize,
	}
}

// sortTrajectories 按开始时间倒序排序
func sortTrajectories(trajs []*Trajectory) {
	for i := 0; i < len(trajs); i++ {
		for j := i + 1; j < len(trajs); j++ {
			if trajs[j].StartTime.After(trajs[i].StartTime) {
				trajs[i], trajs[j] = trajs[j], trajs[i]
			}
		}
	}
}
