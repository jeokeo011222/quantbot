// Package toolworker 宿主侧封装 agent.dll 的拉模式 RPC 总线，并承载确定性的
// 工具/持久化/上下文兑现 handler，用于在无 API key / 无数据库环境下证明
// 「DLL 内决策脑的每次面板副作用都经总线由宿主兑现」。
//
// 本包属于开源宿主侧，只依赖开源 internal/port 契约，绝不 import dll 包
// （避免任何宿主包进入 DLL binary）。LLM 由 DLL 自持，宿主不再经总线兑现。
package toolworker

import (
	"fmt"
	"runtime"
	"strings"
	"syscall"
	"unsafe"
)

// Agent 是对 agent.dll 导出 C ABI 的细粒度封装。跨边界仅传 *C.char JSON。
type Agent struct {
	Debug bool

	dll              *syscall.DLL
	pVersion, pPing  *syscall.Proc
	pAbi, pInit      *syscall.Proc
	pRun, pPoll      *syscall.Proc
	pRespond, pClose *syscall.Proc
	pFree            *syscall.Proc
}

// Load 加载 agent.dll 并解析全部导出符号。
func Load(dllPath string) (*Agent, error) {
	handle, err := syscall.LoadLibrary(dllPath)
	if err != nil {
		return nil, fmt.Errorf("加载 %s 失败: %w", dllPath, err)
	}
	dll := &syscall.DLL{Name: dllPath, Handle: handle}
	a := &Agent{dll: dll}
	procs := []struct {
		name string
		dst  **syscall.Proc
	}{
		{"AgentVersion", &a.pVersion},
		{"AgentPing", &a.pPing},
		{"AgentAbi", &a.pAbi},
		{"AgentInit", &a.pInit},
		{"AgentRunDailyCycle", &a.pRun},
		{"AgentPoll", &a.pPoll},
		{"AgentRespond", &a.pRespond},
		{"AgentClose", &a.pClose},
		{"AgentFree", &a.pFree},
	}
	for _, p := range procs {
		proc, err := dll.FindProc(p.name)
		if err != nil {
			return nil, fmt.Errorf("找不到导出 %s: %w", p.name, err)
		}
		*p.dst = proc
	}
	return a, nil
}

// Close 释放线程资源（不会真正卸载 DLL；宿主进程生命周期内复用）。
func (a *Agent) Close() {
	if a.dll != nil {
		a.dll.Release()
	}
}

// call 调用一个 Proc，并把返回的 *C.char 转为 Go string（由 DLL 侧 AgentFree 释放）。
func (a *Agent) call(proc *syscall.Proc, args ...uintptr) (string, error) {
	r1, _, callErr := proc.Call(args...)
	if callErr != nil && callErr != syscall.Errno(0) {
		return "", callErr
	}
	if r1 == 0 {
		return "", fmt.Errorf("proc(%s) 返回空指针", proc.Name)
	}
	s := gostring(r1)
	a.pFree.Call(r1)
	return s, nil
}

// gostring 将 DLL 返回的 C 字符串指针逐字节拷贝为 Go string。
func gostring(ptr uintptr) string {
	if ptr == 0 {
		return ""
	}
	var b []byte
	for {
		ch := *(*byte)(unsafe.Pointer(ptr))
		if ch == 0 {
			break
		}
		b = append(b, ch)
		ptr++
	}
	return string(b)
}

// intPtr 返回一个指向 handle 的 uintptr（跨 C 边界仅传非指针 int64，这里直接传值更省）。
func intArg(v int64) uintptr { return uintptr(v) }

// Version 调用 AgentVersion。
func (a *Agent) Version() (string, error) {
	return a.call(a.pVersion)
}

// Ping 调用 AgentPing。
func (a *Agent) Ping(msg string) (string, error) {
	in := append([]byte(msg), 0)
	s, err := a.call(a.pPing, uintptr(unsafe.Pointer(&in[0])))
	runtime.KeepAlive(in)
	return s, err
}

// Abi 调用 AgentAbi。
func (a *Agent) Abi() (string, error) {
	return a.call(a.pAbi)
}

// Init 调用 AgentInit(configJSON)。
func (a *Agent) Init(configJSON string) (string, error) {
	in := append([]byte(configJSON), 0)
	s, err := a.call(a.pInit, uintptr(unsafe.Pointer(&in[0])))
	runtime.KeepAlive(in)
	return s, err
}

// RunDaily 调用 AgentRunDailyCycle(handle, runJSON)。
func (a *Agent) RunDaily(handle int64, runJSON string) (string, error) {
	in := append([]byte(runJSON), 0)
	s, err := a.call(a.pRun, intArg(handle), uintptr(unsafe.Pointer(&in[0])))
	runtime.KeepAlive(in)
	return s, err
}

// Poll 调用 AgentPoll(handle)。
func (a *Agent) Poll(handle int64) (string, error) {
	return a.call(a.pPoll, intArg(handle))
}

// Respond 调用 AgentRespond(handle, id, resultJSON)。
func (a *Agent) Respond(handle int64, id, resultJSON string) (string, error) {
	idIn := append([]byte(id), 0)
	resIn := append([]byte(resultJSON), 0)
	s, err := a.call(a.pRespond, intArg(handle), uintptr(unsafe.Pointer(&idIn[0])), uintptr(unsafe.Pointer(&resIn[0])))
	runtime.KeepAlive(idIn)
	runtime.KeepAlive(resIn)
	return s, err
}

// CloseSession 调用 AgentClose(handle)。
func (a *Agent) CloseSession(handle int64) (string, error) {
	return a.call(a.pClose, intArg(handle))
}

// normalizeErr 统一把非 ok 的响应 JSON 转为 error。
func normalizeErr(raw string, err error) error {
	if err != nil {
		return err
	}
	if strings.Contains(raw, `"ok":false`) {
		return fmt.Errorf("DLL 返回错误: %s", raw)
	}
	return nil
}
