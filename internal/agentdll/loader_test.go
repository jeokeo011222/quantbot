package agentdll

import (
	"encoding/json"
	"path/filepath"
	"runtime"
	"testing"
)

// dllPath 定位模块根 bin/agent.dll（测试进程工作目录为包目录 internal/agentdll）。
func dllPath(t *testing.T) string {
	_, file, _, _ := runtime.Caller(0)
	abs, err := filepath.Abs(filepath.Join(filepath.Dir(file), "..", "..", "bin", "agent.dll"))
	if err != nil {
		t.Fatalf("解析路径失败: %v", err)
	}
	return abs
}

// TestLoadPingVersion 验证：宿主 Go 运行时可 LoadLibrary 加载 agent.dll，
// 并跨运行时调用 AgentPing / AgentVersion（双运行时共存校验）。
func TestLoadPingVersion(t *testing.T) {
	a, err := Load(dllPath(t))
	if err != nil {
		t.Fatalf("Load 失败: %v", err)
	}
	defer a.Close()

	// 1. Version
	v, abi, err := a.Version()
	if err != nil {
		t.Fatalf("Version 失败: %v", err)
	}
	t.Logf("version=%s abi=%s", v, abi)
	if v == "" {
		t.Fatal("version 为空")
	}

	// 2. Ping 回显
	pingJSON := `{"a":1,"b":"中文测试"}`
	raw, err := a.Ping(pingJSON)
	if err != nil {
		t.Fatalf("Ping 失败: %v", err)
	}
	var resp struct {
		OK    bool        `json:"ok"`
		Error string      `json:"error"`
		Data  interface{} `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("解析 Ping 响应失败(%v): %s", err, raw)
	}
	if !resp.OK {
		t.Fatalf("Ping 业务失败: %s", resp.Error)
	}
	t.Logf("Ping 响应: %s", raw)
}