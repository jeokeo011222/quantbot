// Package agentdll 在宿主持载决策脑动态库 agent.dll（开源封装层）。
//
// 该包是公开仓库中对闭源 agent.dll 的唯一接入点：负责 LoadLibrary、
// 解析 C ABI 导出函数、并做 JSON 出入参的封装。跨边界内存全部拷贝为 Go 值，
// 不持有返回的 C 指针以外的任何跨运行时引用。
package agentdll

import (
	"encoding/json"
	"fmt"
	"runtime"
	"syscall"
	"unsafe"
)

// Agent 封装对 agent.dll 的加载与调用。
type Agent struct {
	dll *syscall.DLL

	// proc 缓存
	version *syscall.Proc
	ping    *syscall.Proc
	free    *syscall.Proc
}

// Load 加载指定路径（或 DLL 名字）的 agent.dll 并解析全部导出函数。
func Load(path string) (*Agent, error) {
	dll, err := syscall.LoadDLL(path)
	if err != nil {
		return nil, fmt.Errorf("agentdll: 加载 %s: %w", path, err)
	}
	a := &Agent{dll: dll}
	procs := map[string]**syscall.Proc{
		"AgentVersion": &a.version,
		"AgentPing":    &a.ping,
		"AgentFree":    &a.free,
	}
	missing := make([]string, 0)
	for name, dst := range procs {
		p, err := dll.FindProc(name)
		if err != nil {
			missing = append(missing, name)
			continue
		}
		*dst = p
	}
	if len(missing) > 0 {
		_ = a.Close()
		return nil, fmt.Errorf("agentdll: 缺少导出符号 %v", missing)
	}
	return a, nil
}

// Close 释放 DLL 句柄。
func (a *Agent) Close() error {
	if a.dll != nil {
		return a.dll.Release()
	}
	return nil
}

// call 调用一个入参为 *C.char、出参为 *C.char 的导出函数。
// in 为 nil 表示无入参。
func call(p *syscall.Proc, in *byte) (*byte, error) {
	var r0, _, e1 = p.Call(uintptr(unsafe.Pointer(in)))
	// 注意：此处不使用 e1 直接判错（部分导出函数即便成功也可能返回非零），
	// 由调用方解析 JSON 的 ok 字段判断业务成败。
	_ = e1
	if r0 == 0 {
		return nil, fmt.Errorf("agentdll: 调用返回空指针")
	}
	return (*byte)(unsafe.Pointer(r0)), nil
}

func bytePtr(s string) *byte {
	if s == "" {
		return nil
	}
	// 拷贝并以 '\0' 结尾，交由 C.CString 语义；这里直接用 syscall。BytePtrFromString。
	p, err := syscall.BytePtrFromString(s)
	if err != nil {
		return nil
	}
	return p
}

func goBytes(p *byte) string {
	if p == nil {
		return ""
	}
	return cstrToString(p)
}

// cstrToString 读取以 '\0' 结尾的字节串为 Go string（会拷贝）。
func cstrToString(p *byte) string {
	if p == nil {
		return ""
	}
	ptr := unsafe.Pointer(p)
	n := 0
	for {
		// 读取单字节；依赖 DLL 侧保证 '\0' 结尾。
		b := *(*byte)(unsafe.Pointer(uintptr(ptr) + uintptr(n)))
		if b == 0 {
			break
		}
		n++
	}
	out := make([]byte, n)
	copy(out, unsafe.Slice(p, n))
	return string(out)
}

// Version 调用 AgentVersion 返回 (version, abi)。
func (a *Agent) Version() (string, string, error) {
	raw, err := call(a.version, nil)
	if err != nil {
		return "", "", err
	}
	jsonStr := goBytes(raw)
	a.freeResult(raw)
	return parseVersion(jsonStr)
}

func (a *Agent) freeResult(p *byte) {
	if a.free == nil || p == nil {
		return
	}
	a.free.Call(uintptr(unsafe.Pointer(p)))
	runtime.KeepAlive(p)
}

// Ping 调用 AgentPing，返回 DLL 侧回显的原始 JSON 字符串。
func (a *Agent) Ping(msgJSON string) (string, error) {
	raw, err := call(a.ping, bytePtr(msgJSON))
	if err != nil {
		return "", err
	}
	jsonStr := goBytes(raw)
	a.freeResult(raw)
	return jsonStr, nil
}

// ---- 内部 JSON 解析 ----

type versionResp struct {
	OK    bool           `json:"ok"`
	Error string         `json:"error"`
	Data  map[string]any `json:"data,omitempty"`
}

func parseVersion(s string) (string, string, error) {
	var r versionResp
	if err := json.Unmarshal([]byte(s), &r); err != nil {
		return "", "", fmt.Errorf("agentdll: 解析版本响应失败: %w", err)
	}
	if !r.OK {
		return "", "", fmt.Errorf("agentdll: 版本调用失败: %s", r.Error)
	}
	v, _ := r.Data["version"].(string)
	a, _ := r.Data["abi"].(string)
	return v, a, nil
}
