package main

import (
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"

	// 显式链接决策脑核心：agents（Orchestrator/NewDailyCycle）与 llm 编译进 agent.dll，
	// 使 DLL 内部真正包含决策脑。此三者均为 internal/brain 内的包，不依赖任何宿主包
	// （internal/data、internal/tools 等），从而 DLL 保持宿主无关，数据在将来由宿主
	// 通过工具 worker 注入。
	_ "github.com/quantpilot/quantpilot/internal/brain/agents"
	_ "github.com/quantpilot/quantpilot/internal/brain/llm"

	// 供宿主判断决策脑是否随包编译进来（见 abiConfig.BrainLinked）
	_ "github.com/quantpilot/quantpilot/internal/brain/port"
	_ "github.com/quantpilot/quantpilot/internal/brain/toolkit"
)

//-------- 决策脑句柄：把一次初始化得到的会话状态藏在不透明 int64 后，跨 C 边界传递 ---------
//
// brainSession 定义见 bus.go（含决策脑团队与拉模式 RPC 总线状态）。

// abiConfig AgentInit 请求的可选会话配置（JSON schema 见 ExportAbiManifest >> agent_init）。
type abiConfig struct {
	Market         string `json:"market,omitempty"`          // 基准市场/指数，如 "sh000300"
	Date           string `json:"date,omitempty"`            // 目标交易日，如 "2026-09-18"；空=自动
	TimeoutMinutes int    `json:"timeout_minutes,omitempty"` // 运行超时（分钟）

	// LLM 配置（由宿主经 AgentInit.config.llm 传入）：决策脑据此自建 llm.Client，
	// 空字段按 provider 取默认（ProviderDefaults）。LLM 归 DLL 自持，不再经总线代理。
	Provider string `json:"provider,omitempty"` // 如 "deepseek" / "openai" / "custom"
	APIKey   string `json:"api_key,omitempty"`  // 明文 API key
	BaseURL  string `json:"base_url,omitempty"` // 空则按 provider 取默认
	Model    string `json:"model,omitempty"`    // 空则按 provider 取默认
}

type abiRegistry struct {
	mu   sync.Mutex
	next int64
	sess map[int64]*brainSession
}

var registry = &abiRegistry{sess: make(map[int64]*brainSession)}

func (r *abiRegistry) create(cfg abiConfig) int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	id := atomic.AddInt64(&r.next, 1)
	r.sess[id] = &brainSession{
		ID:        id,
		Config:    cfg,
		ToolWired: false,
		waiters:   make(map[string]chan busReply),
	}
	return id
}

func (r *abiRegistry) get(id int64) (*brainSession, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sess[id]
	return s, ok
}

func (r *abiRegistry) close(id int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.sess[id]; !ok {
		return false
	}
	delete(r.sess, id)
	return true
}

// buildBrainManifest 汇总本次编译进 DLL 的决策脑子包，供宿主校验二进制是否完整。
func buildBrainManifest() []string {
	return []string{"agents", "llm", "port", "toolkit"}
}

// runRole 当前可运行的语义角色标签：决策脑核心已编译入 DLL，但宿主工具 worker 尚未接驳。
const runNotWired = "not_wired"

// notWiredNote 一个稳定、对外的可读说明，作为拉模式工具 worker 落地前的诚实占位。
const notWiredNote = "决策脑核心已编译入 agent.dll；宿主工具 worker 接驳在完整 M3 落地，" +
	"当前构建下 App 仍直连 brainhost 运行。"

// decodeConfig 反序列化 AgentInit 请求体（JSON schema 见 ABI 注释）。
func decodeConfig(raw string) (abiConfig, error) {
	var cfg abiConfig
	if raw == "" {
		return cfg, nil
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return cfg, fmt.Errorf("config 不是合法 JSON: %v", err)
	}
	return cfg, nil
}
