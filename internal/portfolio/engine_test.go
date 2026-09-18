package portfolio

import (
	"strings"
	"testing"
)

// TestEmergencyStopGateBlocksTrades 验证最终执行层紧急停止门控：
// 紧急停止期间 Buy/Sell 在触碰 db/持仓前直接拒绝，
// 即使绕过 CIO 编排/审批补确认等入口，也无法产生任何真实成交。
func TestEmergencyStopGateBlocksTrades(t *testing.T) {
	eng := &Engine{}
	eng.SetEmergencyGate(func() bool { return true })

	if _, err := eng.Buy("sh600000", "浦发银行", "sh", 100, 10.0, "test", ""); err == nil {
		t.Fatal("Buy should be blocked during emergency stop")
	} else if !strings.Contains(err.Error(), "紧急停止") {
		t.Fatalf("Buy error should mention 紧急停止, got: %v", err)
	}

	if _, err := eng.Sell("sh600000", 100, 10.0, "test", ""); err == nil {
		t.Fatal("Sell should be blocked during emergency stop")
	} else if !strings.Contains(err.Error(), "紧急停止") {
		t.Fatalf("Sell error should mention 紧急停止, got: %v", err)
	}

	// 恢复：清除门控后 emergencyStopped() 返回 false，且不再返回"紧急停止"错误
	eng.SetEmergencyGate(nil)
	if eng.emergencyStopped() {
		t.Fatal("emergencyStopped() should be false after gate cleared")
	}
}
