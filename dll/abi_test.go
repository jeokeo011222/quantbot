package main

import (
	"encoding/json"
	"testing"
)

// 纯 Go 校验 ABI 背后的逻辑（注册表 / config / 响应封装 / 清单）。
// 说明：dll 包含 cgo //export，无法被 go test 直接承载；导出符号的跨进程形态
// 由 `go build -buildmode=c-shared` 生成的 agent.dll + agent.h 保证（agent.h 列出全部导出函数）。

func TestJSONResponseScheme(t *testing.T) {
	// 统一响应封装：{ok,error,data}
	s := jsonResponse(true, map[string]interface{}{"a": 1}, "")
	var m struct {
		OK  bool                   `json:"ok"`
		Err string                 `json:"error"`
		Dat map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("jsonResponse 非合法 JSON: %v", err)
	}
	if !m.OK || m.Err != "" || m.Dat["a"].(float64) != 1 {
		t.Fatalf("ok 路径不符: %+v", m)
	}
	e := jsonResponse(false, map[string]interface{}{}, "boom")
	var em struct {
		OK  bool   `json:"ok"`
		Err string `json:"error"`
	}
	_ = json.Unmarshal([]byte(e), &em)
	if em.OK || em.Err != "boom" {
		t.Fatalf("error 路径不符: %+v", em)
	}
}

func TestDecodeConfig(t *testing.T) {
	cfg, err := decodeConfig(`{"market":"sh000300","date":"2026-09-18","timeout_minutes":10}`)
	if err != nil || cfg.Market != "sh000300" || cfg.Date != "2026-09-18" || cfg.TimeoutMinutes != 10 {
		t.Fatalf("正常 config 不符: %+v err=%v", cfg, err)
	}
	if _, err := decodeConfig(`{"market":123}`); err == nil {
		t.Fatal("期望 market 类型不符时报错")
	}
	if _, err := decodeConfig("not-json"); err == nil {
		t.Fatal("期望非 JSON 报错")
	}
	empty, err := decodeConfig("")
	if err != nil || empty.Market != "" || empty.Date != "" {
		t.Fatalf("空 config 应为零值: %+v err=%v", empty, err)
	}
}

func TestRegistryLifecycle(t *testing.T) {
	a := registry.create(abiConfig{Market: "m1"})
	b := registry.create(abiConfig{})
	if a == b || a <= 0 || b <= 0 {
		t.Fatalf("句柄应唯一递增: a=%d b=%d", a, b)
	}
	if s, ok := registry.get(a); !ok || s.Config.Market != "m1" {
		t.Fatalf("get(a) 不符: ok=%v s=%+v", ok, s)
	}
	if !registry.close(a) {
		t.Fatal("首次 close(a) 应成功")
	}
	if registry.close(a) {
		t.Fatal("二次 close(a) 应失败")
	}
	if _, ok := registry.get(a); ok {
		t.Fatal("close 后 get(a) 应失败")
	}
	if !registry.close(b) {
		t.Fatal("close(b) 应成功")
	}
}

func TestABIConstants(t *testing.T) {
	if abiVersion != "3" {
		t.Fatalf("期望 abi=3, got %q", abiVersion)
	}
	if apiVersion != "1.0.0" {
		t.Fatalf("期望 api_version=1.0.0, got %q", apiVersion)
	}
	if runNotWired != "not_wired" {
		t.Fatalf("runNotWired 约定变更: %q", runNotWired)
	}
	if len(buildBrainManifest()) == 0 {
		t.Fatal("brain manifest 不应为空")
	}
}
