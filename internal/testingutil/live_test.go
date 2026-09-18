package testingutil

import (
	"testing"
)

// TestSkipUnlessLive_RunsWithEnv:设置 TEST_LIVE=1 时门控放行，继续执行。
func TestSkipUnlessLive_RunsWithEnv(t *testing.T) {
	t.Setenv(envLiveTest, "1")
	SkipUnlessLive(t, "拉取真实行情")
	// 走到这里说明未被跳过 => 门控放行逻辑正确
}

// TestSkipUnlessLive_RealTB:该测试运行时会因未设置 TEST_LIVE 被 SkipUnlessLive 跳过，
// 表现为 SKIP 而非 FAIL。若门控失效，本测试会走到 t.Fatal 而失败。
func TestSkipUnlessLive_RealTB(t *testing.T) {
	t.Setenv(envLiveTest, "")
	SkipUnlessLive(t, "验证门控（无 TEST_LIVE 时应被跳过）")
	t.Fatal("不应通过：未设置 TEST_LIVE 时门控应跳过本测试")
}
