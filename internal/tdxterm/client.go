// Package tdxterm 实现通达信「终端接口」客户端。
//
// 该包通过 syscall 直接加载通达信安装目录下的 TPythClient.dll，
// 以纯 Go（无需 cgo / C 编译器）的方式对接运行中的通达信主程序，
// 协议为「JSON 进、JSON 出」，与通达信官方 Python tqcenter.py 等价。
//
// 前置条件：通达信主程序（TdxW.exe）必须已启动并登录，TPythClient.dll
// 会通过 RPC 与主程序通信。第三方调用方不要求安装任何 C 编译环境。
package tdxterm

import (
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"sync"
	"syscall"
	"unsafe"
)

const (
	errFatal = "20" // 致命错误，需终止
)

// Client 通达信终端接口客户端。
type Client struct {
	mu       sync.Mutex
	dllPath  string // TPythClient.dll 所在目录（可为空，使用默认搜索路径）
	initPath string // InitConnect 的 file_name 标识（通常为策略文件路径，宜唯一）
	runID    int64  // InitConnect 返回的 run_id
	runMode  int    // 运行模式，-1 表示自动
	init     bool   // 是否已初始化成功
	reInit   bool   // 是否重新初始化（重置初始化的状态）
	initErr  error  // 最近一次初始化错误
	closing  bool   // 是否正在关闭
}

// NewClient 创建终端接口客户端。dllDir 为 TPythClient.dll 所在目录
// （可传空串使用 Windows 默认 DLL 搜索路径）；initPath 为 InitConnect
// 的 file_name 标识参数，建议传唯一脚本名，避免与同名策略冲突。
func NewClient(dllDir, initPath string) *Client {
	return &Client{
		dllPath:  dllDir,
		initPath: initPath,
		runMode:  -1,
	}
}

// connect 与通达信主程序握手，获取 run_id。
func (c *Client) connect() error {
	if c.init {
		return nil
	}

	// InitConnect(file_name, dll_path, run_mode, python_version, re_initialized) -> char*
	// dll_path 必须传 DLL 文件本身的完整路径（见 loadDLL 解析出的 resolvedDLLPath），
	// 与官方 tqcenter.py 的 global_dll_path 一致；传安装目录会导致握手失败。
	fileName := append([]byte(c.initPath), 0)
	dll := resolvedDLLPath
	if dll == "" {
		dll = c.dllPath
	}
	dllPath := append([]byte(dll), 0)
	r1, _, err := procInitConnect.Call(
		uintptr(unsafe.Pointer(&fileName[0])),
		uintptr(unsafe.Pointer(&dllPath[0])),
		uintptr(c.runMode),
		uintptr(313), // python_version 标识，接线用
		uintptr(boolToInt(c.reInit)),
	)
	// 要点：Windows 的 GetLastError 在成功调用后也可能残留非零值，
	// 不能仅凭 err 判定失败。必须以返回指针是否为空（r1==0）为准。
	if r1 == 0 && err != nil {
		c.invalidate()
		return fmt.Errorf("InitConnect failed: %w", err)
	}
	raw := cstrBytes(r1)
	if len(raw) == 0 {
		c.init = false
		return fmt.Errorf("InitConnect returned empty (通达信主程序是否已登录？)")
	}

	var resp struct {
		ErrorID string `json:"ErrorId"`
		Error   string `json:"Error"`
		RunID   string `json:"run_id"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		c.init = false
		return fmt.Errorf("InitConnect parse failed: %v; raw=%s", err, string(raw))
	}

	// ErrorId 0=成功, 12=已有同名策略运行（仍可复用 run_id）
	if resp.ErrorID == "0" || resp.ErrorID == "12" {
		if resp.ErrorID == "12" {
			log.Printf("[tdxterm] 终端接口: %s", resp.Error)
		}
		// 通达信把 run_id 以字符串形式返回（如 "0"），需单独解析为整数。
		rid, perr := strconv.ParseInt(resp.RunID, 10, 64)
		if perr != nil {
			c.init = false
			return fmt.Errorf("InitConnect run_id 解析失败: %v; run_id=%q", perr, resp.RunID)
		}
		c.reInit = false
		c.runID = rid
		if c.runID < 0 {
			c.init = false
			return fmt.Errorf("InitConnect run_id<0, 初始化失败：%s", resp.Error)
		}
		c.init = true
		log.Printf("[tdxterm] 终端接口初始化成功 run_id=%d", c.runID)
		return nil
	}

	c.init = false
	return fmt.Errorf("InitConnect failed (ErrorId=%s): %s", resp.ErrorID, resp.Error)
}

// ensureInit 确保已初始化（未连接时自动重连）。
func (c *Client) ensureInit() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.init {
		return nil
	}
	if err := c.connect(); err != nil {
		c.initErr = err
		return err
	}
	return nil
}

// Close 关闭与主程序的连接。
func (c *Client) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.init {
		_, _, _ = procCloseConnect.Call(uintptr(c.runID), uintptr(c.runMode))
		c.init = false
		log.Printf("[tdxterm] 终端接口连接已关闭")
	}
}

// Connected 是否已初始化。
func (c *Client) Connected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.init
}

// call 调用指定 DLL 函数，发送 JSON 请求（自动追加 NUL 终止符），解析返回 JSON。
func (c *Client) call(proc *syscall.Proc, payload map[string]any, timeoutMs int) (map[string]any, error) {
	if err := c.ensureInit(); err != nil {
		return nil, err
	}
	return c.callLocked(proc, payload, timeoutMs)
}

// callLocked 假定已持有锁时调用 DLL 函数。
func (c *Client) callLocked(proc *syscall.Proc, payload map[string]any, timeoutMs int) (map[string]any, error) {
	// 统一注入 run_id（调用方无需关心）
	if _, ok := payload["id"]; !ok {
		payload["id"] = c.runID
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal request failed: %w", err)
	}
	// JSON 为 UTF-8，DLL 以 c_char_p（NUL 终止）读取
	body = append(body, 0)
	r1, _, callErr := proc.Call(
		uintptr(c.runID),
		uintptr(unsafe.Pointer(&body[0])),
		uintptr(timeoutMs),
	)
	if callErr != nil && callErr != syscall.Errno(0) {
		c.invalidate()
		return nil, fmt.Errorf("DLL call failed: %w", callErr)
	}
	raw := cstrBytes(r1)
	if len(raw) == 0 {
		return nil, fmt.Errorf("DLL returned empty response")
	}

	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, fmt.Errorf("parse response failed: %v; raw=%s", err, string(raw))
	}

	// ErrorId=20 为致命错误，终止程序
	if errorID := fmt.Sprintf("%v", obj["ErrorId"]); errorID == errFatal {
		c.invalidate()
		return nil, fmt.Errorf("fatal error (ErrorId=20): %v", obj["Error"])
	}

	if err := c.handleErrorID(obj); err != nil {
		return nil, err
	}
	return obj, nil
}

// handleErrorID 统一处理返回中的 ErrorId。
func (c *Client) handleErrorID(obj map[string]any) error {
	errorID := fmt.Sprintf("%v", obj["ErrorId"])
	switch errorID {
	case "0":
		return nil
	case "6", "7":
		// 连接过期/需重连，标记为未初始化，下次调用自动重连
		c.invalidate()
		return fmt.Errorf("需重新连接 (ErrorId=%s): %v", errorID, obj["Error"])
	default:
		return fmt.Errorf("ErrorId=%s: %v", errorID, obj["Error"])
	}
}

// invalidate 将连接标记为需重新初始化。
func (c *Client) invalidate() {
	c.init = false
	c.reInit = true
}

// boolToInt 将 bool 转为 int（0/1）。
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// cstrBytes 将 NUL 结尾的 C 字符串读为 []byte。
func cstrBytes(ptr uintptr) []byte {
	if ptr == 0 {
		return nil
	}
	p := unsafe.Pointer(ptr)
	var buf []byte
	for i := 0; ; i++ {
		cur := *(*byte)(unsafe.Add(p, uintptr(i)))
		if cur == 0 {
			break
		}
		buf = append(buf, cur)
		if i > (1 << 24) { // 安全护栏：上限 16MB
			break
		}
	}
	return buf
}
