package toolworker

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/quantpilot/quantpilot/internal/port"
)

// runEnvelope 统一解析 DLL 的 {ok,error,data} 响应。
type runEnvelope struct {
	OK    bool            `json:"ok"`
	Error string          `json:"error"`
	Data  json.RawMessage `json:"data"`
}

// RunAgentCycle 用 host 兑现全部总线请求（拉模式轮询），跑完一次 DLL 每日决策周期。
// 该函数被 cmd/dllrunner 与 App 侧共享；host 可绑定真实实现或保持确定性兜底。
//
//	catalog: 非空则作为 AgentInit.catalog（角色→工具目录），空则 DLL 建空团队（会优雅失败）。
//	date:    目标交易日（如 "2026-09-18"）。
//	llmCfg:  LLM 配置（经 AgentInit.config.llm 传给 DLL，DLL 据此自建 llm.Client）；
//	         空值字段由 DLL 侧按 provider 取默认。
//	deadline:轮询总超时（不含 AgentInit/RunDaily 本身）。
//
// 返回决策脑结果（*port.OrchestratorResult）与 host 兑现统计。任一步硬失败/超时返回 error。
func RunAgentCycle(agent *Agent, host *Host, catalog []map[string]interface{}, date string, llmCfg port.LLMConfig, deadline time.Duration) (*port.OrchestratorResult, map[string]int, error) {
	// 1. AgentInit（携带 config + catalog）
	initReq := map[string]interface{}{
		"config": map[string]interface{}{
			"market": "",
			"date":   date,
			"llm": map[string]interface{}{
				"provider": llmCfg.Provider,
				"api_key":  llmCfg.APIKey,
				"base_url": llmCfg.BaseURL,
				"model":    llmCfg.Model,
			},
		},
		"catalog": catalog,
	}
	initJSON, _ := json.Marshal(initReq)
	initRaw, err := agent.Init(string(initJSON))
	if err != nil {
		return nil, nil, fmt.Errorf("AgentInit 调用失败: %w", err)
	}
	var initEnv runEnvelope
	if err := json.Unmarshal([]byte(initRaw), &initEnv); err != nil {
		return nil, nil, fmt.Errorf("AgentInit 响应解码失败: %v", err)
	}
	if !initEnv.OK {
		return nil, nil, fmt.Errorf("AgentInit 拒绝: %s", initEnv.Error)
	}
	var initData struct {
		Handle int64 `json:"handle"`
		Wired  bool  `json:"wired"`
	}
	_ = json.Unmarshal(initEnv.Data, &initData)
	if initData.Handle <= 0 {
		return nil, nil, fmt.Errorf("AgentInit 未返回有效 handle")
	}
	defer agent.CloseSession(initData.Handle)

	// 2. AgentRunDailyCycle
	runJSON, _ := json.Marshal(map[string]interface{}{"date": date})
	runRaw, err := agent.RunDaily(initData.Handle, string(runJSON))
	if err != nil {
		return nil, nil, fmt.Errorf("AgentRunDailyCycle 调用失败: %w", err)
	}
	var runEnv runEnvelope
	_ = json.Unmarshal([]byte(runRaw), &runEnv)
	if !runEnv.OK {
		return nil, nil, fmt.Errorf("AgentRunDailyCycle 拒绝: %s", runEnv.Error)
	}
	var runData struct {
		Status string `json:"status"`
		Note   string `json:"note"`
	}
	_ = json.Unmarshal(runEnv.Data, &runData)
	if runData.Status == "not_wired" {
		return nil, nil, fmt.Errorf("DLL 未接驳工具 worker (not_wired): %s", runData.Note)
	}

	// 3. 拉模式轮询：awaiting_request → host 兑现 → Respond；done → 解析结果。
	start := time.Now()
	var final *port.OrchestratorResult
	for {
		if time.Since(start) > deadline {
			return nil, nil, fmt.Errorf("DLL 每日周期轮询超时（%s）", deadline)
		}
		pollRaw, err := agent.Poll(initData.Handle)
		if err != nil {
			return nil, nil, fmt.Errorf("AgentPoll 失败: %w", err)
		}
		var pollEnv runEnvelope
		if err := json.Unmarshal([]byte(pollRaw), &pollEnv); err != nil {
			return nil, nil, fmt.Errorf("AgentPoll 响应解码失败: %v", err)
		}
		if !pollEnv.OK {
			return nil, nil, fmt.Errorf("AgentPoll 拒绝: %s", pollEnv.Error)
		}
		var pd struct {
			Status  string `json:"status"`
			Request *struct {
				ID      string          `json:"id"`
				Kind    string          `json:"kind"`
				Payload json.RawMessage `json:"payload"`
			} `json:"request"`
			Result json.RawMessage `json:"result"`
			Error  string          `json:"error"`
		}
		_ = json.Unmarshal(pollEnv.Data, &pd)

		switch pd.Status {
		case "awaiting_request":
			if pd.Request == nil {
				return nil, nil, fmt.Errorf("awaiting_request 但缺少 request")
			}
			result := host.HandleRequest(pd.Request.Kind, string(pd.Request.Payload))
			if _, err := agent.Respond(initData.Handle, pd.Request.ID, result); err != nil {
				return nil, nil, fmt.Errorf("AgentRespond 失败: %w", err)
			}
		case "done":
			if err := json.Unmarshal(pd.Result, &final); err != nil {
				return nil, nil, fmt.Errorf("DLL 决策结果解码失败: %v", err)
			}
			return final, host.Counters(), nil
		case "error":
			return nil, nil, fmt.Errorf("决策脑运行出错: %s", pd.Error)
		case "running":
			time.Sleep(20 * time.Millisecond)
		default:
			return nil, nil, fmt.Errorf("未知轮询状态: %s", pd.Status)
		}
	}
}
