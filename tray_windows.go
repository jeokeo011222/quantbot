//go:build windows

package main

import (
	"log"
	"os"
	"runtime"
	"syscall"
	"unsafe"
)

// ==================== 原生 Win32 系统托盘 ====================
// Wails v2.14 未提供 Windows 侧的 SystemTray API（仅 macOS 内部支持），
// 因此这里用 Go syscall 直接调用 Shell_NotifyIcon 实现系统托盘：
//   - 隐藏窗口接收托盘消息（左键抬起或 NIN_SELECT → 显示主界面）
//   - 右键弹出系统菜单（显示主界面 / 退出）
//   - 监听 TaskbarCreated，资源管理器重启后自动重建托盘图标
// 该文件为自包含实现，无第三方依赖。

const (
	_nimAdd    = 0x00000000
	_nimModify = 0x00000001
	_nimDelete = 0x00000002

	_nifMessage = 0x00000001
	_nifIcon    = 0x00000002
	_nifTip     = 0x00000004

	_wmTray      = 0x00008001 // WM_APP + 1，托盘回调消息
	_wmLButtonUp = 0x00000202
	_wmRButtonUp = 0x00000205

	_tipCbSizeMax = 128 // szTip 最大字符数

	_mfString  = 0x00000000
	_mfSep     = 0x00000800
	_tpmReturn = 0x00000100
	_tpmRBtn   = 0x00000002

	_idiApplication = 32512
)

var (
	u32                 = syscall.NewLazyDLL("user32.dll")
	trayKernel32        = syscall.NewLazyDLL("kernel32.dll")
	shell32             = syscall.NewLazyDLL("shell32.dll")
	procShellNotifyIcon = shell32.NewProc("Shell_NotifyIconW")
	procCreateWindowEx  = u32.NewProc("CreateWindowExW")
	procUpdateWindow    = u32.NewProc("UpdateWindow")
	procRegClassExW     = u32.NewProc("RegisterClassExW")
	procDefWindowProc   = u32.NewProc("DefWindowProcW")
	procGetModHandle    = trayKernel32.NewProc("GetModuleHandleW") // 注意：GetModuleHandleW 位于 kernel32，而非 user32
	procLoadIcon        = u32.NewProc("LoadIconW")
	procCreatePopup     = u32.NewProc("CreatePopupMenu")
	procAppendMenu      = u32.NewProc("AppendMenuW")
	procTrackPopup      = u32.NewProc("TrackPopupMenu")
	procDestroyMenu     = u32.NewProc("DestroyMenu")
	procGetCursorPos    = u32.NewProc("GetCursorPos")
	procSetForeground   = u32.NewProc("SetForegroundWindow")
	procRegMsgW         = u32.NewProc("RegisterWindowMessageW")
	procGetMessageW     = u32.NewProc("GetMessageW")
	procTranslateMsg    = u32.NewProc("TranslateMessage")
	procDispatchMsgW    = u32.NewProc("DispatchMessageW")
	procExtractIconExW  = shell32.NewProc("ExtractIconExW")
)

// notifyIconDataW 对应 Windows NOTIFYICONDATAW（64 位布局，字段顺序与 C 一一对应，
// Go 与 MSVC 对于「指针/整数」的对齐规则一致，unsafe.Sizeof 即 C 的 sizeof）。
type notifyIconDataW struct {
	cbSize       uint32
	hWnd         uintptr
	uID          uint32
	uFlags       uint32
	uCallbackMsg uint32
	hIcon        uintptr
	szTip        [_tipCbSizeMax]uint16
	dwState      uint32
	dwStateMask  uint32
	szInfo       [256]uint16
	uVersion     uint32
	szInfoTitle  [64]uint16
	dwInfoFlags  uint32
	guidItem     [16]byte
	hBalloon     uintptr
}

type wndClassExW struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     uintptr
	hIcon         uintptr
	hCursor       uintptr
	hbrBackground uintptr
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       uintptr
}

type point struct {
	x int32
	y int32
}

// msgT 对应 Win32 MSG（64 位布局；message 后由编译器按 8 字节对齐 wParam）。
type msgT struct {
	hwnd     uintptr
	message  uint32
	wParam   uintptr
	lParam   uintptr
	time     uint32
	pt       point
	lPrivate uint32
}

// 托盘状态（进程内包级单例；窗口/图标由主线程创建，配合 Wails 消息泵派发托盘消息）
var (
	trayHWnd       uintptr
	trayIconID     uint32  = 1
	trayClassName          = "QuantBotTrayWindow"
	trayProcRef    uintptr // 保持 wndproc 回调存活，避免被 GC 回收
	onTrayShow     func()
	onTrayQuit     func()
	taskbarCreated uint32
)

// startTray 初始化系统托盘（在含消息泵的线程上调用）。
// onShow：左键单击托盘图标恢复主界面；onQuit：菜单/图标「退出」。
func startTray(onShow, onQuit func()) error {
	onTrayShow = onShow
	onTrayQuit = onQuit

	hInst, _, _ := procGetModHandle.Call(0)

	// 登记 TaskbarCreated：资源管理器重启后系统广播该消息，用于重建托盘图标
	namePtr, _ := syscall.UTF16PtrFromString("TaskbarCreated")
	r1, _, _ := procRegMsgW.Call(uintptr(unsafe.Pointer(namePtr)))
	taskbarCreated = uint32(r1)

	// 1) 注册托盘消息窗口类
	trayProcRef = syscall.NewCallback(trayWndProc)
	clsNamePtr, _ := syscall.UTF16PtrFromString(trayClassName)
	wc := wndClassExW{
		cbSize:        uint32(unsafe.Sizeof(wndClassExW{})),
		lpfnWndProc:   trayProcRef,
		hInstance:     hInst,
		lpszClassName: clsNamePtr,
	}
	if errno, ok := lastErrno(procRegClassExW.Call(uintptr(unsafe.Pointer(&wc)))); ok && errno != 0 {
		log.Printf("[Tray] RegisterClassExW 失败: %v", errno)
		return errno
	}

	// 2) 创建隐藏窗口（HWND_MESSAGE：不占用任务栏/不显示）
	const hwndMessage uintptr = ^uintptr(2) // HWND_MESSAGE = -3
	hwnd, _, err := procCreateWindowEx.Call(
		0,
		uintptr(unsafe.Pointer(clsNamePtr)),
		uintptr(unsafe.Pointer(clsNamePtr)),
		0, 0, 0, 0, 0,
		hwndMessage, 0, hInst, 0,
	)
	if hwnd == 0 {
		log.Printf("[Tray] CreateWindowExW 失败: %v", err)
		return syscall.Errno(1)
	}
	trayHWnd = hwnd
	procUpdateWindow.Call(hwnd)

	// 3) 添加托盘图标（用应用自身的图标资源，回退到系统默认图标）
	icon := loadAppIcon(hInst)
	if err := trayNotifyAdd(icon); err != nil {
		log.Printf("[Tray] 添加托盘图标失败: %v", err)
		return err
	}
	log.Printf("[Tray] 系统托盘已就绪 hwnd=%d", hwnd)
	return nil
}

// runTrayAsync 在独立且锁定到 OS 线程的 goroutine 上初始化托盘并运行专属消息泵。
// 关键点：Shell_NotifyIcon 的回调（WM_TRAY）只发给“创建该窗口的线程”的消息队列，
// 而 Wails 的 startup 常在非主线程 goroutine 上执行、且不带自己的消息泵，
// 若不在此单独取消息，左/右键消息将永远无人派发 → 托盘菜单无法弹出、无法退出。
// 故必须：LockOSThread 固定线程 → 在同线程创建窗口 → 同线程跑 GetMessage 循环。
func runTrayAsync(onShow, onQuit func()) error {
	errCh := make(chan error, 1)
	go func() {
		defer appRecover("托盘消息循环")
		runtime.LockOSThread()
		if err := startTray(onShow, onQuit); err != nil {
			errCh <- err
			return
		}
		close(errCh)
		runTrayMessageLoop()
	}()
	return <-errCh
}

// runTrayMessageLoop 托盘线程的消息泵：派发 WM_TRAY 到 trayWndProc。
// GetMessage 收到 WM_QUIT 时返回 0 退出；返回 -1 表示出错退出。
func runTrayMessageLoop() {
	var msg msgT
	for {
		ret, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		switch ret {
		case 0, ^uintptr(0): // 0=WM_QUIT，^0=-1=错误
			return
		}
		procTranslateMsg.Call(uintptr(unsafe.Pointer(&msg)))
		procDispatchMsgW.Call(uintptr(unsafe.Pointer(&msg)))
	}
}

// stopTray 删除托盘图标并销毁隐藏窗口（进程退出前调用，幂等）。
func stopTray() {
	if trayHWnd != 0 {
		nid := trayNotifyData(_nimDelete)
		procShellNotifyIcon.Call(_nimDelete, uintptr(unsafe.Pointer(&nid)))
	}
}

// trayNotifyAdd 组装并提交 ADD 请求
func trayNotifyAdd(icon uintptr) error {
	nid := trayNotifyData(_nimAdd)
	nid.hIcon = icon
	copy(nid.szTip[:], syscall.StringToUTF16("QuantBot AI量化机器人"))
	if errno, ok := lastErrno(procShellNotifyIcon.Call(_nimAdd, uintptr(unsafe.Pointer(&nid)))); ok && errno != 0 {
		return errno
	}
	return nil
}

// lastErrno 把 LazyProc.Call 的多返回值（uintptr,uintptr,error）统一转换为
// syscall.Errno 判断；返回 (errno, ok)，ok=false 表示错误非 Errno 类型（无需按错误码处理）。
func lastErrno(_, _ uintptr, err error) (syscall.Errno, bool) {
	errno, ok := err.(syscall.Errno)
	return errno, ok
}

// trayNotifyData 构建基础 NOTIFYICONDATAW（消息+图标+提示 三项）
func trayNotifyData(msgType int) notifyIconDataW {
	nid := notifyIconDataW{
		cbSize:       uint32(unsafe.Sizeof(notifyIconDataW{})),
		hWnd:         trayHWnd,
		uID:          trayIconID,
		uFlags:       _nifMessage | _nifIcon | _nifTip,
		uCallbackMsg: _wmTray,
	}
	return nid
}

// loadAppIcon 加载应用图标：从当前运行的 exe 提取其嵌入图标（无需猜资源 ID）。
// 通知区（托盘）以 16px 小图标渲染，若直接塞大图标(如256x256首帧)会因缩放+透明边距显得比别家小，
// 故优先取 ExtractIconExW 的「小图标」句柄；取不到时回退大图标，再到系统默认图标。
func loadAppIcon(hInst uintptr) uintptr {
	exePath, err := os.Executable()
	if err == nil {
		exePtr, _ := syscall.UTF16PtrFromString(exePath)
		var hSmall, hLarge uintptr
		n, _, _ := procExtractIconExW.Call(
			uintptr(unsafe.Pointer(exePtr)),
			0, // 图标组索引 0 = exe 第一个图标组（主图标）
			uintptr(unsafe.Pointer(&hLarge)),
			uintptr(unsafe.Pointer(&hSmall)), // 小图标（16x16，贴合通知区尺寸）
			1,
		)
		if n > 0 {
			if hSmall != 0 {
				return hSmall
			}
			if hLarge != 0 {
				return hLarge
			}
		}
	}
	// 兜底：从模块资源尝试，再到系统默认图标
	if hIcon, _, _ := procLoadIcon.Call(hInst, _idiApplication); hIcon != 0 {
		return hIcon
	}
	hIcon, _, _ := procLoadIcon.Call(0, _idiApplication)
	return hIcon
}

// rebuildTrayIcon 资源管理器重启后重建托盘图标
func rebuildTrayIcon() {
	nid := trayNotifyData(_nimAdd)
	hInst, _, _ := procGetModHandle.Call(0)
	nid.hIcon = loadAppIcon(hInst)
	procShellNotifyIcon.Call(_nimDelete, uintptr(unsafe.Pointer(&nid)))
	procShellNotifyIcon.Call(_nimAdd, uintptr(unsafe.Pointer(&nid)))
}

// showTrayContextMenu 弹出右键菜单，返回选中的命令 id（0=未选择）
func showTrayContextMenu() uint32 {
	var pt point
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	procSetForeground.Call(trayHWnd)

	hMenu, _, _ := procCreatePopup.Call()
	if hMenu == 0 {
		return 0
	}
	defer procDestroyMenu.Call(hMenu)

	showPtr, _ := syscall.UTF16PtrFromString("显示主界面")
	quitPtr, _ := syscall.UTF16PtrFromString("退出")
	procAppendMenu.Call(hMenu, _mfString, 1, uintptr(unsafe.Pointer(showPtr)))
	procAppendMenu.Call(hMenu, _mfSep, 0, 0)
	procAppendMenu.Call(hMenu, _mfString, 2, uintptr(unsafe.Pointer(quitPtr)))

	cmd, _, _ := procTrackPopup.Call(
		hMenu,
		_tpmReturn|_tpmRBtn,
		uintptr(pt.x),
		uintptr(pt.y),
		0,
		trayHWnd,
		0,
	)
	return uint32(cmd)
}

// trayWndProc 托盘消息窗口过程（运行于 Wails 主线程消息泵中）
func trayWndProc(hwnd, msg, wparam, lparam uintptr) uintptr {
	switch msg {
	case _wmTray:
		// 左键单击/双击 → 显示主界面
		if lparam == _wmLButtonUp {
			if onTrayShow != nil {
				onTrayShow()
			}
			return 0
		}
		// 右键 → 上下文菜单
		if lparam == _wmRButtonUp {
			switch showTrayContextMenu() {
			case 1:
				if onTrayShow != nil {
					onTrayShow()
				}
			case 2:
				if onTrayQuit != nil {
					onTrayQuit()
				}
			}
			return 0
		}
		return 0
	case uintptr(taskbarCreated):
		// 资源管理器重启
		rebuildTrayIcon()
		return 0
	}
	// 默认处理
	r, _, _ := procDefWindowProc.Call(hwnd, msg, wparam, lparam)
	return r
}
