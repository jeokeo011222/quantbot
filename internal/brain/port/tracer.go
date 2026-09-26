package port

// Tracer 决策脑透明追踪抽象：宿主将 internal/transparency.Tracker 适配后注入。
// Agent 在运行时通过本接口记录数据源读取、算法执行与决策步骤，供「执行力」/模型可解释性使用。
type Tracer interface {
	// StartSession 开始一个追踪会话（align transparency.Tracker.StartSession）。
	StartSession(sessionID, taskDate string)
	// AddDataSource 记录数据源读取（sessionID 由 Agent 注入）。
	AddDataSource(sessionID, source, status string, items []string)
	// AddAlgorithm 记录算法执行（sessionID 由 Agent 注入）。
	AddAlgorithm(sessionID, name, input, output, status string, durationMs int64)
	// AddDecision 记录决策步骤（sessionID 由 Agent 注入）。
	AddDecision(sessionID, role, action, reason string, dataUsed []string)
}