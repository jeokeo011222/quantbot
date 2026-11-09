// Package guardrails 提供各类执行熔断护栏。
//
//	核心目标：防止 AI 评审/调参循环无限抖动。
//
// 设计借鉴 dsh-superagent 的"自动评审者最多 3 轮"规则：
//  - 同一问题的 AI 自审循环最多 N 轮（默认 3）
//  - 超过后自动熔断：要么降级为人工决策，要么返回当前最佳结果并标注 "review_circuit_broken"
//  - 熔断必须是显式状态，不能静默
package guardrails

import (
	"fmt"
	"sync"
	"time"
)

// CircuitBreaker 评审/调参循环熔断。
//
// 用法示例（PTC / Creator / 策略调参）：
//
//	cb := NewCircuitBreaker(3, "strategy_tuning")
//	for cb.Next() {
//	    // ... 一轮评审/调参
//	    if cb.Exceeded() { return Best, ErrCircuitBroken }
//	}
type CircuitBreaker struct {
	mu sync.Mutex

	// MaxRounds 允许的最大轮次（含初始轮；通常 3）
	MaxRounds int
	// Name 熔断点名称（日志/审计）
	Name string

	Round     int       // 当前轮次（从 0 计）
	StartedAt time.Time // 首次开启时间

	broken   bool   // 是否已触发熔断
	lastErr  error  // 触发熔断的原因
}

// DefaultMaxRounds 默认最大评审轮次（3）。
const DefaultMaxRounds = 3

// ErrCircuitBroken 熔断触发时的哨兵错误。
type ErrCircuitBroken struct {
	Name      string
	Rounds    int
	LastError error
}

func (e ErrCircuitBroken) Error() string {
	return fmt.Sprintf("circuit breaker [%s] tripped after %d rounds: %v", e.Name, e.Rounds, e.LastError)
}

// NewCircuitBreaker 创建熔断护栏。
func NewCircuitBreaker(maxRounds int, name string) *CircuitBreaker {
	if maxRounds <= 0 {
		maxRounds = DefaultMaxRounds
	}
	return &CircuitBreaker{
		MaxRounds: maxRounds,
		Name:      name,
		StartedAt: time.Now(),
	}
}

// Next 进入下一轮。第一次调用返回 true；之后每次 round++ 直到 MaxRounds 后返回 false 并标记 broken。
// 若 IsBroken 已为 true，Next 永远返回 false。
func (cb *CircuitBreaker) Next() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	if cb.broken {
		return false
	}
	if cb.Round >= cb.MaxRounds {
		cb.broken = true
		cb.lastErr = ErrCircuitBroken{
			Name:   cb.Name,
			Rounds: cb.Round,
		}
		return false
	}
	cb.Round++
	return true
}

// RecordFailure 记录一轮失败原因（用于熔断时携带上下文）。
func (cb *CircuitBreaker) RecordFailure(err error) {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	if err != nil {
		cb.lastErr = err
	}
}

// Exceeded 返回是否已触发熔断或已耗尽轮次。
func (cb *CircuitBreaker) Exceeded() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	if cb.broken {
		return true
	}
	return cb.Round > cb.MaxRounds
}

// IsBroken 返回熔断是否已激活。
func (cb *CircuitBreaker) IsBroken() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.broken
}

// CurrentRound 当前轮次（1-based：首次 Next 后为 1）。
func (cb *CircuitBreaker) CurrentRound() int {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.Round
}

// Reset 重置熔断（用于新一轮任务）。
func (cb *CircuitBreaker) Reset() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.Round = 0
	cb.broken = false
	cb.lastErr = nil
	cb.StartedAt = time.Now()
}

// Error 当前熔断的错误原因；未熔断返回 nil。
func (cb *CircuitBreaker) Error() error {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	if cb.broken {
		if cb.lastErr != nil {
			return cb.lastErr
		}
		return ErrCircuitBroken{Name: cb.Name, Rounds: cb.Round}
	}
	return nil
}

// Trip 手动触发熔断（用于 Agent 主动放弃时）。
func (cb *CircuitBreaker) Trip(reason error) {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.broken = true
	cb.lastErr = reason
}

// ---- 便捷工具 ----

// Guarded 包裹一个会返回 (result, tryAgain) 的循环体，最多跑 maxRounds 轮，
// 超过返回 circuit broken error 和最后一次结果。
func Guarded[T any](maxRounds int, name string, fn func(round int) (T, bool, error)) (T, error) {
	cb := NewCircuitBreaker(maxRounds, name)
	var (
		best   T
		lastErr error
	)
	for cb.Next() {
		res, again, err := fn(cb.Round)
		best = res
		if err != nil {
			lastErr = err
			cb.RecordFailure(err)
			if !again {
				return best, nil
			}
			continue
		}
		if !again {
			return best, nil
		}
	}
	if err := cb.Error(); err != nil {
		return best, err
	}
	return best, lastErr
}
