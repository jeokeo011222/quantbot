// Package guardrails 护栏模式——防止 AI 评审/调参/自审循环无限抖动。
//
// 本文件是**跨文件模式模板**：展示了 guardrails + reasoning 组合的标准落地方式。
// 新增多轮循环时请以此为起点，不要手写裸 for 循环。
//
// 同时提供了反模式对照（见 PatternAntiPattern）和 vet 规则（cmd/guardvet）。
//
// 设计借鉴 dsh-superagent 的三个原则：
//  1. 自动评审者最多 3 轮——防止 AI 反复拒绝自己的结果导致无限抖动
//  2. 熔断是显式状态——不能静默，必须记录并返回当前最佳结果
//  3. 熔断携带 ReasonCode——供审计追踪和前端渲染
package guardrails

import (
	"fmt"
	"log"

	"github.com/quantpilot/quantpilot/internal/reasoning"
)

// ==================== 模式：单个多轮动作 ====================
//
// 当你有一个"反复尝试直到满意"的循环（AI 评审、LLM 反思、迭代调参、
// 自审 loop 等），请按以下骨架编码。不要手写裸 for 循环。

// PatternSingleLoop 标准骨架——迭代尝试直到满足收敛条件。
//   - 第一轮尝试（result, ok, err）
//   - 若 !ok && err==nil → 下一轮；连续 maxRounds 次 !ok → 熔断
//   - 若 !ok && err!=nil → 熔断（硬错误不重试）
//   - 若 ok → 收敛，立即返回
//
// 用法（未来新增任何多轮循环都复制这段骨架）：
//
//	func (agent *SomeAgent) review(ctx context.Context, input string) (*Result, error) {
//	    cb := guardrails.NewCircuitBreaker(3, fmt.Sprintf("%s:review", agent.Name))
//	    for cb.Next() {
//	        result, satisfied, err := agent.tryOne(ctx, input)
//	        if err != nil {
//	            cb.RecordFailure(err)
//	            continue // 软失败可重试；硬错误直接熔断 → cb.Trip(err)
//	        }
//	        if satisfied {
//	            cb.Reset()
//	            return result, nil
//	        }
//	    }
//	    // 熔断触发：显式记录 reason code，返回当前最佳
//	    return agent.lastBest, cb.Error()
//	}
type PatternSingleLoop struct {
	// MaxRounds 最大轮次（通常 3）
	MaxRounds int
	// Name 熔断点名称（审计用，建议 "owner:action" 格式）
	Name string
}

// NewPattern 构造标准骨架。
func NewPattern(owner, action string) PatternSingleLoop {
	return PatternSingleLoop{
		MaxRounds: DefaultMaxRounds,
		Name:      fmt.Sprintf("%s:%s", owner, action),
	}
}

// RunPattern 执行多轮循环，遵循 PatternSingleLoop 骨架。
// tryFunc 返回 (result, satisfied, softErr)：
//   - satisfied=true → 收敛，立即返回 result
//   - softErr!=nil → 记录失败，继续下一轮
//   - softErr==nil && satisfied=false → 继续下一轮（结果不满意但可重试）
// 超过 MaxRounds → 返回最后一次 result 和 ErrCircuitBroken。
//
// 注：不能写成 PatternSingleLoop.Run[T] —— Go 1.27 才支持泛型方法；
// 顶层泛型函数从 Go 1.18 起就可用，兼容 go.mod 的 go 1.25。
func RunPattern[T any](p PatternSingleLoop, tryFunc func(round int) (T, bool, error)) (T, error) {
	cb := NewCircuitBreaker(p.MaxRounds, p.Name)
	var best T
	var lastErr error
	for cb.Next() {
		res, satisfied, err := tryFunc(cb.CurrentRound())
		if err != nil {
			cb.RecordFailure(err)
			lastErr = err
			continue
		}
		best = res
		if satisfied {
			cb.Reset()
			return res, nil
		}
	}
	if cb.Error() != nil {
		return best, cb.Error()
	}
	return best, lastErr
}

// ==================== 模式：带 ReasonCode 注入 ====================
//
// 熔断/收敛时同时注入 ReasonCodes 供审计，确保后端审计日志和前端面板
// 都能稳定复现循环的走向（"第几轮触发、为什么触发、为什么停止"）。
//
// 用法（把 ReasonSet 当作 loop 状态机的一部分）：
//
//	reasonSet := make(reasoning.ReasonSet, 0, 4)
//	loopResult, err := guardrails.Guarded[T](3, "quant:tune_params", func(round int) (T, bool, error) {
//	    res, improved, e := tryTune()
//	    if e != nil {
//	        reasonSet = append(reasonSet, reasoning.NewReasonValues(reasoning.CodeExecutionFailed,
//	            map[string]interface{}{"round": round, "error": e.Error()}))
//	        return res, false, e
//	    }
//	    if improved {
//	        reasonSet = append(reasonSet, reasoning.NewReasonValues(reasoning.CodeSignalStrong,
//	            map[string]interface{}{"round": round, "delta": res.delta}))
//	        return res, true, nil
//	    }
//	    return res, false, nil
//	})
//	if err != nil {
//	    reasonSet = append(reasonSet, reasoning.NewReasonValues(reasoning.CodeDuplicate,
//	        map[string]interface{}{"guard_name": "quant:tune_params", "reason": err.Error()}))
//	    log.Printf("[GuardPattern] 熔断 reason=%s", reasonSet.JoinLabels(" | "))
//	}

// ==================== 反模式 ====================
//
// PatternAntiPattern 记录最常见的护栏误用（给 vet 规则做参考）。
// 新增代码时 vet 会扫描这些反模式。
type PatternAntiPattern struct {
	// AntiPattern 反模式描述
	AntiPattern string
	// GoAST 特征：vet 规则会识别这些 AST 节点组合
	GoAST []string
	// 修复模式：应该用什么替换
	FixWith string
}

// AntiPatterns 最常见的 6 种反模式。
var AntiPatterns = []PatternAntiPattern{
	{
		AntiPattern: "裸 for 循环无 guardrails",
		GoAST:       []string{"*ast.ForStmt 迭代变量名含 round/iter/review/attempt/step", "循环体内无 guardrails 调用"},
		FixWith:     "用 guardrails.Guarded 或 guardrails.NewCircuitBreaker 包装",
	},
	{
		AntiPattern: "for...range 全部 combos 无熔断",
		GoAST:       []string{"*ast.RangeStmt 遍历 combos/param_candidates", "循环内无 CircuitBreaker"},
		FixWith:     "在 range 外先计算 baseline，循环用 Guarded 包装并在命中改善时 early-exit",
	},
	{
		AntiPattern: "MaxIterations 硬编码 > 3",
		GoAST:       []string{"字面量 > 3 赋值给 MaxIterations/MaxRounds"},
		FixWith:     "用 guardrails.DefaultMaxRounds (3) 或 guardrails.NewCircuitBreaker(maxRounds, name)",
	},
	{
		AntiPattern: "熔断后静默丢弃结果",
		GoAST:       []string{"CircuitBreaker.Trip 后直接 return nil/空 struct 不携带 best 结果"},
		FixWith:     "熔断时必须携带最后一次 best 结果 + ErrCircuitBroken",
	},
	{
		AntiPattern: "熔断不注入 ReasonCodes",
		GoAST:       []string{"cb.Error() != nil 分支内无 reasoning.NewReason / reasoning.ReasonSet"},
		FixWith:     "熔断时 reasonSet += reasoning.NewReasonValues(CodeDuplicate/CodeRetry, ...)",
	},
	{
		AntiPattern: "vet 规则未覆盖",
		GoAST:       []string{"新增 cmd/*/main.go 未用 guardvet 检查"},
		FixWith:     "Makefile: guard-check 目标 + cmd/guardvet 集成",
	},
}

// ==================== 辅助：ReasonCode 同步 ====================

// GuardTripReason 熔断时附加到 ReasonSet 的推荐 code（避免每次重复写）。
func GuardTripReason(name string, rounds int, err error) reasoning.Reason {
	values := map[string]interface{}{
		"guard_name": name,
		"rounds":     rounds,
	}
	if err != nil {
		values["error"] = err.Error()
	}
	return reasoning.NewReasonValues(reasoning.CodeDuplicate, values)
}

// GuardTripReasonSet 直接返回 ReasonSet，方便一行 append。
func GuardTripReasonSet(name string, rounds int, err error) reasoning.ReasonSet {
	return reasoning.ReasonSet{GuardTripReason(name, rounds, err)}
}

// logTrip 熔断时的标准日志格式（和 reasonSet.JoinLabels 对齐）。
func logTrip(name string, rounds int, err error, rs reasoning.ReasonSet) {
	labels := rs.JoinLabels(" | ")
	log.Printf("[GuardTrip] %s after %d rounds: err=%v reason=%s", name, rounds, err, labels)
}
