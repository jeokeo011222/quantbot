package agents

import (
	"context"
	"testing"
	"time"
)

// TestEmergencyStopBlocksProcessTask 验证紧急停止后 Agent 拒绝一切任务（返回 BLOCKED），恢复后不再拦截
func TestEmergencyStopBlocksProcessTask(t *testing.T) {
	agent := NewAgent("test_001", RoleQuant, nil, nil, nil)
	ctx := context.Background()

	task := AgentMessage{
		MessageID: "MSG-test-1",
		From:      "CIO",
		To:        "QUANT",
		Type:      "RESEARCH",
		Priority:  "HIGH",
		Timestamp: time.Now(),
	}

	// 初始状态：正常
	if agent.IsStopped() {
		t.Fatal("agent should NOT be stopped initially")
	}

	// 紧急停止
	agent.EmergencyStop()
	if !agent.IsStopped() {
		t.Fatal("agent should be stopped after EmergencyStop")
	}

	// 停止状态下 ProcessTask 立即返回 BLOCKED，不进入任何处理流程
	resp := agent.ProcessTask(ctx, task)
	if resp.Type != "BLOCKED" {
		t.Fatalf("expected BLOCKED response when stopped, got %q", resp.Type)
	}
	ctxMap, ok := resp.Context.(map[string]interface{})
	if !ok {
		t.Fatalf("expected Context to be a map, got %T", resp.Context)
	}
	if status, _ := ctxMap["status"].(string); status != "EMERGENCY_STOP" {
		t.Fatalf("expected EMERGENCY_STOP status, got %v", ctxMap["status"])
	}

	// 恢复工作
	agent.Resume()
	if agent.IsStopped() {
		t.Fatal("agent should NOT be stopped after Resume")
	}

	// 恢复后 ProcessTask 不再返回 BLOCKED（nil LLM 下会进入处理流程，返回其他类型）
	resp = agent.ProcessTask(ctx, task)
	if resp.Type == "BLOCKED" {
		t.Fatal("agent should process tasks after Resume, not BLOCKED")
	}
}

// TestEmergencyStopRepeat 验证重复紧急停止/恢复是幂等的，不会崩溃或翻转状态错误
func TestEmergencyStopRepeat(t *testing.T) {
	agent := NewAgent("test_002", RoleRisk, nil, nil, nil)

	agent.EmergencyStop()
	agent.EmergencyStop()
	if !agent.IsStopped() {
		t.Fatal("agent should remain stopped after repeated EmergencyStop")
	}

	agent.Resume()
	agent.Resume()
	if agent.IsStopped() {
		t.Fatal("agent should remain running after repeated Resume")
	}
}
