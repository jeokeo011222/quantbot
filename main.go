package main

import (
	"embed"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime/debug"
	"time"

	"github.com/quantpilot/quantpilot/internal/config"
	"github.com/quantpilot/quantpilot/internal/updater"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

//go:embed all:ui/build
var assets embed.FS

func main() {
	logFile := setupLogging()
	if logFile != nil {
		defer logFile.Close()
	}

	// 升级子进程模式：执行文件替换并重启新版，不进入正常 UI 流程
	if idx := argIndex("--apply-update"); idx >= 0 && idx+1 < len(os.Args) {
		planPath := os.Args[idx+1]
		log.Println("[QuantBot] 升级子进程模式，开始应用更新...")
		if err := updater.ApplyUpdate(planPath); err != nil {
			log.Printf("[QuantBot] 升级失败: %v", err)
		}
		os.Exit(0)
	}

	// 正常启动：清理上次升级残留（staging / 备份 / 旧 exe）
	updater.CleanupAfterUpdate(GetExecDir())

	log.Println("[QuantBot] ========================================")
	log.Println("[QuantBot] Starting QuantBot AI量化机器人")
	log.Println("[QuantBot] ========================================")

	// 单实例检查
	isFirst, mutexHandle := SingleInstance()
	if !isFirst {
		log.Println("[QuantBot] Another instance is already running, exiting...")
		println("错误：程序已在运行中，请勿重复启动。")
		println("请关闭已打开的 QuantBot 窗口后再试。")
		// 在单独的 goroutine 中显示对话框，避免阻塞进程退出
		go showAlreadyRunningDialog()
		// 立即退出进程
		log.Println("[QuantBot] Exiting duplicate instance...")
		os.Exit(0)
	}
	defer ReleaseMutex(mutexHandle)

	exePath, _ := os.Executable()
	if exePath != "" {
		log.Printf("[QuantBot] Executable path: %s", exePath)
	}

	app := NewApp()

	err := wails.Run(&options.App{
		Title:         "QuantBot AI量化机器人",
		Width:         1280,
		Height:        800,
		MinWidth:      800,
		MinHeight:     600,
		Frameless:     true,
		DisableResize: false,
		StartHidden:   false,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		OnStartup:  app.startup,
		OnShutdown: app.shutdown,
		Debug: options.Debug{
			OpenInspectorOnStartup: false,
		},
		Bind: []interface{}{
			app,
		},
	})

	if err != nil {
		log.Printf("[QuantBot] Fatal error: %v", err)
		log.Println("[QuantBot] Application exited with error")
		println("Error:", err.Error())
	}
}

// setupLogging 设置日志文件
// 仅使用可执行文件目录下的log文件夹
func setupLogging() *os.File {
	exePath, err := os.Executable()
	if err != nil {
		log.Printf("[QuantBot] 无法获取可执行文件路径: %v", err)
		return nil
	}
	exeDir := filepath.Dir(exePath)

	// 日志文件开关：仅用户手动修改 config/config.json 的 enable_log_file 字段控制
	// （前端程序不提供修改入口），默认启用。
	configPath := filepath.Join(exeDir, "config", "config.json")
	if !config.FileLogEnabled(configPath) {
		println("日志文件已关闭 (config.json: enable_log_file=false)，本次运行不生成 log/ 日志文件")
		log.Println("[QuantBot] enable_log_file=false，日志文件已关闭，本次运行不生成 log/ 日志文件")
		return nil
	}

	logDir := filepath.Join(exeDir, "log")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		log.Printf("[QuantBot] 无法创建日志目录: %v", err)
		return nil
	}
	println("Log directory:", logDir)

	timestamp := time.Now().Format("2006-01-02_15-04-05")
	logPath := filepath.Join(logDir, "quantbot_"+timestamp+".log")

	SetLogPath(logPath)

	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		println("Warning: Failed to open log file:", err.Error())
		return nil
	}

	var writers []io.Writer
	writers = append(writers, logFile)

	if os.Stdout != nil && os.Getenv("WAILS") == "" {
		writers = append(writers, os.Stdout)
	}

	if len(writers) > 1 {
		log.SetOutput(io.MultiWriter(writers...))
	} else if len(writers) == 1 {
		log.SetOutput(writers[0])
	}

	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)

	println("Log file:", logPath)

	// 捕获未被 recover 的致命 panic：Go 运行时把崩溃堆栈直接写到 os.Stderr，
	// 而 GUI(windowsgui/wails) 程序没有 stderr，panic 输出会静默丢失，表现为无从查证的闪退。
	// 用 debug.SetCrashOutput 把它重定向到独立崩溃输出文件，任何致命 panic 都留有完整堆栈。
	crashPath := filepath.Join(logDir, "crash_dump.txt")
	if cf, cerr := os.OpenFile(crashPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0666); cerr == nil {
		// SetCrashOutput 持有该文件供运行时写入，运行时负责其生命周期，勿手动 Close。
		debug.SetCrashOutput(cf, debug.CrashOptions{})
		println("Crash output:", crashPath)
	}

	// 立即写入第一条日志验证
	log.Println("[QuantBot] Logging system initialized successfully")
	return logFile
}

// GetExecDir 获取可执行文件所在目录
func GetExecDir() string {
	exePath, err := os.Executable()
	if err != nil {
		return "."
	}
	return filepath.Dir(exePath)
}

// appRecover 后台 goroutine 的 panic 兜底：把 panic 与完整堆栈写入日志，
// 避免个别后台任务（调度/行情/组合预热等）异常导致整个进程闪退。
// 用法：在每个 go func 体内首行插入 defer appRecover("任务名")。
func appRecover(name string) {
	if r := recover(); r != nil {
		log.Printf("[QuantBot] [PANIC] %s: %v\n%s", name, r, debug.Stack())
	}
}

// argIndex 查找命令行参数位置，未找到返回 -1
func argIndex(name string) int {
	for i, a := range os.Args {
		if a == name {
			return i
		}
	}
	return -1
}
