package tdxterm

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/windows"
)

// 通达信终端接口 DLL 导出函数。
var (
	procInitConnect   *syscall.Proc // InitConnect 初始化，获取 run_id
	procGetTdxDataStr *syscall.Proc // 通用行情数据接口
	procGetProData    *syscall.Proc // 专业数据（财务等）
	procSetMsgToMain  *syscall.Proc // 发送数据给客户端
	procGetOrderStr   *syscall.Proc // 下单接口
	procCloseConnect  *syscall.Proc // 关闭连接
)

var dllLoaded = false

// resolvedDLLPath 为实际解析出的 TPythClient.dll 完整路径。
// InitConnect 的 dll_path 参数要求传 DLL 文件本身的路径（而非目录），
// 与通达信官方 tqcenter.py 一致。
var resolvedDLLPath string

// LoadLibraryEx 的搜索标志（kernel32 常量，含义见 MSDN）。
const (
	loadLibrarySearchDefaultDirs = 0x00001000 // 遍历默认目录（系统、应用所在目录等）
	loadLibrarySearchUserDirs    = 0x00000400 // 遍历 AddDllDirectory 加入的用户目录
)

// loadDLL 加载并解析 TPythClient.dll。
// dllDir 为 DLL 所在目录；为空则使用默认搜索路径。
func loadDLL(dllDir string) error {
	if dllLoaded {
		return nil
	}

	var dllPath string
	if dllDir != "" {
		// 通达信的 TPythClient.dll 实际位于安装目录的 PYPlugins 子目录下，
		// 兼容旧版直接放于安装目录根的情况（加载失败时自动尝试另一个位置）。
		candidates := []string{
			filepath.Join(dllDir, "PYPlugins", "TPythClient.dll"),
			filepath.Join(dllDir, "TPythClient.dll"),
		}
		for _, c := range candidates {
			if _, err := os.Stat(c); err == nil {
				dllPath = c
				break
			}
		}
		if dllPath == "" {
			dllPath = candidates[0]
		}
	} else {
		dllPath = "TPythClient.dll"
	}

	// TPythClient.dll 会依赖其所在目录及通达信根目录下的其它 DLL。
	// syscall.LoadDLL 内部走 LoadLibrary，默认不会从这些目录搜索依赖，
	// 因而会报 "The specified module could not be found"（0x7E）。
	// 这里先通过 AddDllDirectory 把候选搜索路径加入用户 DLL 目录集，
	// 再用 LoadLibraryEx + LOAD_LIBRARY_SEARCH_USER_DIRS 加载。
	var searchDirs []string
	if dllPath != "" {
		searchDirs = append(searchDirs, filepath.Dir(dllPath))
	}
	if dllDir != "" {
		searchDirs = append(searchDirs, dllDir)
	}
	for _, d := range searchDirs {
		abs, err := filepath.Abs(d)
		if err != nil {
			continue
		}
		ptr, err := windows.UTF16PtrFromString(abs)
		if err == nil {
			if _, err := windows.AddDllDirectory(ptr); err != nil {
				continue // 目录无效时忽略，沿用默认搜索路径
			}
		}
	}

	handle, err := windows.LoadLibraryEx(dllPath, 0, loadLibrarySearchDefaultDirs|loadLibrarySearchUserDirs)
	if err != nil {
		return fmt.Errorf("加载 %s 失败（请确认通达信已安装且主程序已登录）: %w", dllPath, err)
	}
	dll := &syscall.DLL{Name: dllPath, Handle: syscall.Handle(handle)}
	resolvedDLLPath = dllPath

	getProc := func(name string) (*syscall.Proc, error) {
		p, err := dll.FindProc(name)
		if err != nil {
			return nil, err
		}
		return p, nil
	}

	if procInitConnect, err = getProc("InitConnect"); err != nil {
		return fmt.Errorf("InitConnect 导出符号缺失: %w", err)
	}
	if procGetTdxDataStr, err = getProc("GetTdxDataStr"); err != nil {
		return fmt.Errorf("GetTdxDataStr 导出符号缺失: %w", err)
	}
	if procGetProData, err = getProc("GetProDataInStr"); err != nil {
		return fmt.Errorf("GetProDataInStr 导出符号缺失: %w", err)
	}
	if procSetMsgToMain, err = getProc("SetMsgToMain"); err != nil {
		return fmt.Errorf("SetMsgToMain 导出符号缺失: %w", err)
	}
	if procCloseConnect, err = getProc("CloseConnect"); err != nil {
		return fmt.Errorf("CloseConnect 导出符号缺失: %w", err)
	}
	// GetOrderStr 为可选导出（下单），缺失不影响行情读取
	if procGetOrderStr, _ = getProc("GetOrderStr"); procGetOrderStr == nil {
		procGetOrderStr = procGetTdxDataStr // 占位，避免 nil 指针
	}

	dllLoaded = true
	return nil
}
