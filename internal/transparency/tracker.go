package transparency

import (
	"log"
	"sync"
	"time"
)

// DataSource 数据源记录
type DataSource struct {
	Source   string   `json:"source"`    // 数据源名称
	Status   string   `json:"status"`    // 状态（成功/失败）
	Items    []string `json:"items"`     // 读取的数据项
	ReadTime string   `json:"read_time"` // 读取时间
}

// AlgorithmRecord 算法执行记录
type AlgorithmRecord struct {
	Name       string `json:"name"`        // 算法名称
	Input      string `json:"input"`       // 输入
	Output     string `json:"output"`      // 输出
	Status     string `json:"status"`      // 状态
	RunTime    string `json:"run_time"`    // 运行时间
	DurationMs int64  `json:"duration_ms"` // 耗时
}

// DecisionStep 决策步骤
type DecisionStep struct {
	Step      int      `json:"step"`      // 步骤编号
	Agent     string   `json:"agent"`     // 智能体角色
	Action    string   `json:"action"`    // 执行的动作
	Reason    string   `json:"reason"`    // 决策原因
	DataUsed  []string `json:"data_used"` // 使用的数据
	Timestamp string   `json:"timestamp"` // 时间戳
}

// SessionTransparency 会话透明度数据
type SessionTransparency struct {
	SessionID   string            `json:"session_id"`
	TaskDate    string            `json:"task_date"`
	DataSources []DataSource      `json:"data_sources"`
	Algorithms  []AlgorithmRecord `json:"algorithms"`
	Decisions   []DecisionStep    `json:"decisions"`
	UpdatedAt   string            `json:"updated_at"`
}

// Tracker 透明度追踪器
type Tracker struct {
	mu       sync.RWMutex
	sessions map[string]*SessionTransparency // sessionID -> transparency data
}

// NewTracker 创建追踪器
func NewTracker() *Tracker {
	return &Tracker{
		sessions: make(map[string]*SessionTransparency),
	}
}

// StartSession 开始一个新的追踪会话
func (t *Tracker) StartSession(sessionID, taskDate string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.sessions[sessionID] = &SessionTransparency{
		SessionID:   sessionID,
		TaskDate:    taskDate,
		DataSources: make([]DataSource, 0),
		Algorithms:  make([]AlgorithmRecord, 0),
		Decisions:   make([]DecisionStep, 0),
		UpdatedAt:   time.Now().Format("2006-01-02 15:04:05"),
	}

	log.Printf("[Transparency] Session started: %s (date: %s)", sessionID, taskDate)
}

// AddDataSource 添加数据源记录
func (t *Tracker) AddDataSource(sessionID string, source, status string, items []string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	session, ok := t.sessions[sessionID]
	if !ok {
		log.Printf("[Transparency] Session not found: %s, creating new session", sessionID)
		session = &SessionTransparency{
			SessionID:   sessionID,
			DataSources: make([]DataSource, 0),
			Algorithms:  make([]AlgorithmRecord, 0),
			Decisions:   make([]DecisionStep, 0),
			UpdatedAt:   time.Now().Format("2006-01-02 15:04:05"),
		}
		t.sessions[sessionID] = session
	}

	session.DataSources = append(session.DataSources, DataSource{
		Source:   source,
		Status:   status,
		Items:    items,
		ReadTime: time.Now().Format("2006-01-02 15:04:05"),
	})
	session.UpdatedAt = time.Now().Format("2006-01-02 15:04:05")

	log.Printf("[Transparency] Data source added: %s (%d items)", source, len(items))
}

// AddAlgorithm 添加算法执行记录
func (t *Tracker) AddAlgorithm(sessionID, name, input, output, status string, durationMs int64) {
	t.mu.Lock()
	defer t.mu.Unlock()

	session, ok := t.sessions[sessionID]
	if !ok {
		session = &SessionTransparency{
			SessionID:   sessionID,
			DataSources: make([]DataSource, 0),
			Algorithms:  make([]AlgorithmRecord, 0),
			Decisions:   make([]DecisionStep, 0),
			UpdatedAt:   time.Now().Format("2006-01-02 15:04:05"),
		}
		t.sessions[sessionID] = session
	}

	session.Algorithms = append(session.Algorithms, AlgorithmRecord{
		Name:       name,
		Input:      input,
		Output:     output,
		Status:     status,
		RunTime:    time.Now().Format("2006-01-02 15:04:05"),
		DurationMs: durationMs,
	})
	session.UpdatedAt = time.Now().Format("2006-01-02 15:04:05")

	log.Printf("[Transparency] Algorithm added: %s (status: %s, duration: %dms)", name, status, durationMs)
}

// AddDecision 添加决策步骤
func (t *Tracker) AddDecision(sessionID, agent, action, reason string, dataUsed []string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	session, ok := t.sessions[sessionID]
	if !ok {
		session = &SessionTransparency{
			SessionID:   sessionID,
			DataSources: make([]DataSource, 0),
			Algorithms:  make([]AlgorithmRecord, 0),
			Decisions:   make([]DecisionStep, 0),
			UpdatedAt:   time.Now().Format("2006-01-02 15:04:05"),
		}
		t.sessions[sessionID] = session
	}

	step := len(session.Decisions) + 1
	session.Decisions = append(session.Decisions, DecisionStep{
		Step:      step,
		Agent:     agent,
		Action:    action,
		Reason:    reason,
		DataUsed:  dataUsed,
		Timestamp: time.Now().Format("2006-01-02 15:04:05"),
	})
	session.UpdatedAt = time.Now().Format("2006-01-02 15:04:05")

	log.Printf("[Transparency] Decision added: step %d, agent: %s, action: %s", step, agent, action)
}

// GetSession 获取会话透明度数据
func (t *Tracker) GetSession(sessionID string) *SessionTransparency {
	t.mu.RLock()
	defer t.mu.RUnlock()

	session, ok := t.sessions[sessionID]
	if !ok {
		return nil
	}

	// 返回副本
	result := *session
	return &result
}

// GetLatestSession 获取最新的会话
func (t *Tracker) GetLatestSession() *SessionTransparency {
	t.mu.RLock()
	defer t.mu.RUnlock()

	var latest *SessionTransparency
	for _, session := range t.sessions {
		if latest == nil || session.UpdatedAt > latest.UpdatedAt {
			latest = session
		}
	}

	return latest
}

// GetSessionsByDate 获取指定日期的所有会话
func (t *Tracker) GetSessionsByDate(taskDate string) []*SessionTransparency {
	t.mu.RLock()
	defer t.mu.RUnlock()

	var result []*SessionTransparency
	for _, session := range t.sessions {
		if session.TaskDate == taskDate {
			result = append(result, session)
		}
	}

	return result
}

// ClearSession 清除会话数据
func (t *Tracker) ClearSession(sessionID string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	delete(t.sessions, sessionID)
	log.Printf("[Transparency] Session cleared: %s", sessionID)
}

// ClearAll 清除所有会话数据
func (t *Tracker) ClearAll() {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.sessions = make(map[string]*SessionTransparency)
	log.Printf("[Transparency] All sessions cleared")
}

// GetStats 获取统计信息
func (t *Tracker) GetStats() map[string]interface{} {
	t.mu.RLock()
	defer t.mu.RUnlock()

	totalSessions := len(t.sessions)
	totalDataSources := 0
	totalAlgorithms := 0
	totalDecisions := 0

	for _, session := range t.sessions {
		totalDataSources += len(session.DataSources)
		totalAlgorithms += len(session.Algorithms)
		totalDecisions += len(session.Decisions)
	}

	return map[string]interface{}{
		"total_sessions":     totalSessions,
		"total_data_sources": totalDataSources,
		"total_algorithms":   totalAlgorithms,
		"total_decisions":    totalDecisions,
	}
}
