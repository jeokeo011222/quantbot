package main

/*
#include <stdlib.h>
#include <string.h>
*/
import "C"

import (
	"context"
	"encoding/json"
	"time"
	"unsafe"

	"github.com/quantpilot/quantpilot/internal/brain/agents"
)

// API 版本与 ABI 版本：宿主据此判断兼容性。
// abiVersion "3" 新增：AgentInit 支持 catalog（角色→工具目录装配决策脑团队），
// AgentRunDailyCycle 真正驱动 Orchestrator.RunDailyCycle，并新增 AgentPoll / AgentRespond
// 拉模式 RPC 总线交互（宿主兑现工具/持久化/上下文/LLM）。
const (
	apiVersion = "1.0.0"
	abiVersion = "3"
)

// jsonIn 把 *C.char 安全拷贝为 Go 字符串（拷贝而非引用，避免跨运行时滞留 C 内存）。
func goStr(c *C.char) string {
	if c == nil {
		return ""
	}
	return C.GoString(c)
}

// jsonOut 分配一块 C 字符串并返回；调用方（宿主）负责用 AgentFree 释放。
// 返回内容为一个 JSON 字符串（出参统一封装，见 jsonResponse）。
func jsonOut(s string) *C.char {
	return C.CString(s)
}

// jsonResponse 统一封装响应，便于宿主解析错误与结果。
func jsonResponse(ok bool, data interface{}, errMsg string) string {
	b, _ := json.Marshal(map[string]interface{}{
		"ok":    ok,
		"error": errMsg,
		"data":  data,
	})
	return string(b)
}

// AgentVersion 返回决策脑版本与 ABI 版本。无入参，出参为 JSON 字符串。
//
//export AgentVersion
func AgentVersion() *C.char {
	return jsonOut(jsonResponse(true, map[string]string{
		"version": apiVersion,
		"abi":     abiVersion,
	}, ""))
}

// AgentPing 联通性自检：回显入参原样返回（DLL 侧可正常编解码 JSON 即通过）。
//
//export AgentPing
func AgentPing(msg *C.char) *C.char {
	text := goStr(msg)
	var echo interface{}
	_ = json.Unmarshal([]byte(text), &echo) // 仅做一次 JSON 语法自检
	return jsonOut(jsonResponse(true, map[string]interface{}{
		"echo":   text,
		"parsed": echo != nil,
	}, ""))
}

// AgentFree 释放在 C 侧分配的字符串（宿主调用，回收由本 DLL 分配的 C.CString 内存）。
//
//export AgentFree
func AgentFree(p *C.char) {
	if p == nil {
		return
	}
	C.free(unsafe.Pointer(p))
}

// -------- 决策脑 ABI 固化部分（abiVersion "3"）--------
//
// 对外契约（跨进程边界只传 *C.char JSON，严禁传含指针的 Go 内存）：
//
//	agent_init         req {"config":{market?,date?,timeout_minutes?,llm:{provider?,api_key?,base_url?,model?}},"catalog":[{role,tools:[{name,description,parameters}]}]}
//	                   resp {ok,error,data:{handle:int64,config:{...},wired:true,abi:"3"}}
//	agent_run_daily    req {"date"?}      resp {ok,error,data:{handle,date,status:"started"}}
//	agent_poll         req {"handle"}     resp {ok,error,data:{status,running,request?,result?,error?}}
//	agent_respond      req {"handle","id","result"}  resp {ok,error,data:{handle,id,delivered}}
//	agent_close        req {"handle"}     resp {ok,error,data:{handle,closed:true}}
//	agent_abi          req (空)           resp {ok,error,data:{abi,api_version,brain,operations}}
//	agent_version / agent_ping / agent_free：见上方既有函数。
//
// 说明：决策脑核心（agents/llm/port/toolkit）已编译入 agent.dll，宿主通过拉模式 RPC 总线
// 兑现工具/持久化/上下文，LLM 由决策脑自持（配置经 AgentInit.config.llm 传入后自建 llm.Client），
// 从而「决策脑在 DLL 内、数据由宿主侧注入」。

// AgentAbi 返回本 DLL 的完整 ABI 清单（操作 + JSON schema + 已编译脑核心子包）。
//
//export AgentAbi
func AgentAbi() *C.char {
	ops := []map[string]string{
		{"name": "agent_version", "method": "AgentVersion", "response": "{ok,error,data:{version,abi}}"},
		{"name": "agent_ping", "method": "AgentPing", "request": "text(any json)", "response": "{ok,error,data:{echo,parsed}}"},
		{"name": "agent_init", "method": "AgentInit", "request": "{config:{market?,date?,timeout_minutes?,llm:{provider?,api_key?,base_url?,model?}},catalog:[{role,tools:[{name,description,parameters}]}]}", "response": "{ok,error,data:{handle,config,wired,abi}}"},
		{"name": "agent_run_daily", "method": "AgentRunDailyCycle", "request": "{date?}", "response": "{ok,error,data:{handle,date,status}}"},
		{"name": "agent_poll", "method": "AgentPoll", "request": "{handle}", "response": "{ok,error,data:{status,request?,result?,error?}}"},
		{"name": "agent_respond", "method": "AgentRespond", "request": "{handle,id,result}", "response": "{ok,error,data:{handle,id,delivered}}"},
		{"name": "agent_close", "method": "AgentClose", "request": "{handle}", "response": "{ok,error,data:{handle,closed}}"},
		{"name": "agent_abi", "method": "AgentAbi", "response": "{ok,error,data:{abi,api_version,brain,operations}}"},
	}
	return jsonOut(jsonResponse(true, map[string]interface{}{
		"abi":         abiVersion,
		"api_version": apiVersion,
		"brain":       buildBrainManifest(),
		"operations":  ops,
	}, ""))
}

// initRequest AgentInit 请求体：config（可选）+ catalog（角色→工具目录）。
type initRequest struct {
	Config  abiConfig    `json:"config"`
	Catalog []busCatalog `json:"catalog"`
}

// AgentInit 建立一次决策脑会话并装配决策脑团队，返回不透明句柄。
//
//export AgentInit
func AgentInit(configJSON *C.char) *C.char {
	var req initRequest
	raw := goStr(configJSON)
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &req); err != nil {
			return jsonOut(jsonResponse(false, map[string]interface{}{"abi": abiVersion}, "请求体不是合法 JSON: "+err.Error()))
		}
	}
	// 兜底兼容：宿主若直接传 abiConfig（无 config/catalog 包裹），将其视为 config。
	if len(req.Catalog) == 0 && !isWrappedConfig(raw) {
		if cfg, err := decodeConfig(raw); err == nil {
			req.Config = cfg
		}
	}
	if req.Config.Date == "" {
		req.Config.Date = time.Now().Format("2006-01-02")
	}
	handle := registry.create(req.Config)
	session, ok := registry.get(handle)
	if !ok {
		return jsonOut(jsonResponse(false, map[string]interface{}{"abi": abiVersion}, "会话创建失败"))
	}
	session.setupTeam(req.Catalog)
	return jsonOut(jsonResponse(true, map[string]interface{}{
		"handle": handle,
		"config": session.Config,
		"wired":  session.ToolWired,
		"abi":    abiVersion,
	}, ""))
}

// isWrappedConfig 粗略判断入参是否带 {"config":...} 包裹（决定是否兜底直接解析为 config）。
func isWrappedConfig(raw string) bool {
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return false
	}
	_, hasConfig := m["config"]
	_, hasCatalog := m["catalog"]
	return hasConfig || hasCatalog
}

// AgentRunDailyCycle 启动后台 goroutine 真正驱动 Orchestrator.RunDailyCycle（经拉模式总线）。
// 立即返回 status="started"；完成态通过 AgentPoll 拉取。
//
//export AgentRunDailyCycle
func AgentRunDailyCycle(handleID int64, runJSON *C.char) *C.char {
	session, ok := registry.get(handleID)
	if !ok {
		return jsonOut(jsonResponse(false, map[string]interface{}{}, "句柄无效或已关闭"))
	}
	var req struct {
		Date string `json:"date,omitempty"`
	}
	_ = json.Unmarshal([]byte(goStr(runJSON)), &req)
	date := req.Date
	if date == "" {
		date = session.Config.Date
	}
	if !session.ToolWired {
		return jsonOut(jsonResponse(true, map[string]interface{}{
			"handle": handleID, "date": date, "status": runNotWired, "note": notWiredNote,
		}, ""))
	}
	if session.orch == nil {
		return jsonOut(jsonResponse(false, map[string]interface{}{"handle": handleID, "date": date},
			"决策脑团队未装配（AgentInit 失败）"))
	}

	session.busMu.Lock()
	if session.running {
		session.busMu.Unlock()
		return jsonOut(jsonResponse(true, map[string]interface{}{
			"handle": handleID, "date": date, "status": "already_running",
		}, ""))
	}
	session.running = true
	session.done = false
	session.result = nil
	session.err = nil
	orch := session.orch
	ctx, cancel := context.WithCancel(context.Background())
	session.cancel = cancel
	session.busMu.Unlock()

	go func() {
		defer cancel()
		result, err := orch.RunDailyCycle(ctx)
		session.busMu.Lock()
		session.result = result
		session.err = err
		session.done = true
		session.running = false
		session.busMu.Unlock()
	}()

	return jsonOut(jsonResponse(true, map[string]interface{}{
		"handle": handleID,
		"date":   date,
		"status": "started",
	}, ""))
}

// AgentPoll 拉取一次会话的可投递状态：done / error / awaiting_request / running。
//
//export AgentPoll
func AgentPoll(handleID int64) *C.char {
	session, ok := registry.get(handleID)
	if !ok {
		return jsonOut(jsonResponse(false, map[string]interface{}{}, "句柄无效或已关闭"))
	}
	res := session.poll()
	// poll 内部 status: awaiting_request / running / done / error；统一编码进 data。
	var data interface{}
	switch res.Status {
	case "awaiting_request":
		data = map[string]interface{}{
			"status":  "awaiting_request",
			"running": session.isRunningUnsafe(),
			"request": res.Request,
		}
	case "done":
		data = map[string]interface{}{
			"status":  "done",
			"running": session.isRunningUnsafe(),
			"result":  json.RawMessage(res.Result),
		}
	case "error":
		data = map[string]interface{}{
			"status":  "error",
			"running": session.isRunningUnsafe(),
			"error":   res.Error,
		}
	default:
		data = map[string]interface{}{
			"status":  "running",
			"running": session.isRunningUnsafe(),
		}
	}
	return jsonOut(jsonResponse(true, data, ""))
}

// isRunningUnsafe 读取 running 标记（供 AgentPoll 输出展示；无锁读取可容忍瞬时偏差）。
func (s *brainSession) isRunningUnsafe() bool {
	s.busMu.Lock()
	defer s.busMu.Unlock()
	return s.running
}

// respondRequestJSON AgentRespond 的请求体。
type respondRequestJSON struct {
	Handle int64  `json:"handle"`
	ID     string `json:"id"`
	Result string `json:"result"`
}

// AgentRespond 把宿主兑现的结果推回被阻塞的脑 goroutine。
//
//export AgentRespond
func AgentRespond(handleID int64, id *C.char, resultJSON *C.char) *C.char {
	requestStr := goStr(id)
	resultStr := goStr(resultJSON)
	session, ok := registry.get(handleID)
	if !ok {
		return jsonOut(jsonResponse(false, map[string]interface{}{}, "句柄无效或已关闭"))
	}
	delivered := session.respondRequest(requestStr, resultStr)
	return jsonOut(jsonResponse(true, map[string]interface{}{
		"handle":    handleID,
		"id":        requestStr,
		"delivered": delivered,
	}, ""))
}

// AgentClose 取消并释放一次会话句柄。句柄无效或已关闭时 ok=false 并给出提示。
//
//export AgentClose
func AgentClose(handleID int64) *C.char {
	session, ok := registry.get(handleID)
	if !ok {
		return jsonOut(jsonResponse(false, map[string]interface{}{"handle": handleID}, "句柄无效或已关闭"))
	}
	session.busMu.Lock()
	if session.cancel != nil {
		session.cancel()
	}
	session.busMu.Unlock()
	if !registry.close(handleID) {
		return jsonOut(jsonResponse(false, map[string]interface{}{"handle": handleID}, "句柄无效或已关闭"))
	}
	return jsonOut(jsonResponse(true, map[string]interface{}{"handle": handleID, "closed": true}, ""))
}

// ensureAgentsLinked 仅在 export.go 引用 agents 包，保证决策脑装配类型被链接（供 buildBrainManifest 使用）。
var _ = agents.NewOrchestrator
