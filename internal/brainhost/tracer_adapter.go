package brainhost

import (
	"github.com/quantpilot/quantpilot/internal/port"
	"github.com/quantpilot/quantpilot/internal/transparency"
)

// tracerAdapter 将宿主 internal/transparency.Tracker 适配为决策脑抽象 port.Tracer。
type tracerAdapter struct {
	inner *transparency.Tracker
}

func (a *tracerAdapter) StartSession(sessionID, taskDate string) {
	a.inner.StartSession(sessionID, taskDate)
}

func (a *tracerAdapter) AddDataSource(sessionID, source, status string, items []string) {
	a.inner.AddDataSource(sessionID, source, status, items)
}

func (a *tracerAdapter) AddAlgorithm(sessionID, name, input, output, status string, durationMs int64) {
	a.inner.AddAlgorithm(sessionID, name, input, output, status, durationMs)
}

func (a *tracerAdapter) AddDecision(sessionID, role, action, reason string, dataUsed []string) {
	a.inner.AddDecision(sessionID, role, action, reason, dataUsed)
}

// AdaptTracker 将宿主 internal/transparency.Tracker 适配为决策脑 port.Tracer。
// tracker 为 nil 时返回 nil，调用方同样以 nil 语义处理（表示不启用透明追踪）。
func AdaptTracker(tracker *transparency.Tracker) port.Tracer {
	if tracker == nil {
		return nil
	}
	return &tracerAdapter{inner: tracker}
}
