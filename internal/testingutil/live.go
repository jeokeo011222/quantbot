// Package testingutil 提供测试辅助函数，供各包测试复用。
package testingutil

import (
	"os"
	"testing"
)

// envLiveTest 是否以"实时数据源测试"模式运行。
// 与 easy-stock 的 A_STOCK_LIVE_TEST 约定一致：真实外呼/真实读盘的测试必须在设置了
// TEST_LIVE=1 时才执行，普通 `go test`/CI 不触碰公网数据源或真实数据库，避免误触限流、
// 把上游字段波动误判为本地代码回归。
const envLiveTest = "TEST_LIVE"

// SkipUnlessLive 门控：当未设置 TEST_LIVE=1 时跳过测试，并提示运行方式。
// 用于任何会真实访问外部数据源（东方财富/同花顺/新浪等）或真实读盘库的测试。
//
// 用法：
//
//	func TestLive(t *testing.T) {
//		testingutil.SkipUnlessLive(t, "拉取真实行情")
//	    ...
//	}
func SkipUnlessLive(t testing.TB, reason string) {
	t.Helper()
	if os.Getenv(envLiveTest) != "1" {
		t.Skipf("实时数据源测试（%s）被跳过：设置 TEST_LIVE=1 后再运行", reason)
	}
}
