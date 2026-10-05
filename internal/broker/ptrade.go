package broker

import "time"

// PtradeConfig 恒生 Ptrade 桥接策略网关配置。
// Ptrade 策略脚本运行在 Ptrade 客户端内嵌 Python 引擎中，通过本地 HTTP 与 QuantBot 通信。
type PtradeConfig struct {
	// Host 桥接网关监听地址（默认 127.0.0.1）。
	Host string
	// HTTPPort 桥接网关 HTTP 端口（默认 8891）。
	HTTPPort int
	// Account 券商资金账号。
	Account string
	// ScriptPath ptrade_bridge.py 的绝对路径（生成在 exe 目录 Ptrade/ 下）。
	ScriptPath string
	// FolderPath Ptrade 文件夹路径（写入 config.json）。
	FolderPath string
	// Timeout 单次 HTTP 请求超时。
	Timeout time.Duration
}

// PtradeBroker 恒生 Ptrade 实盘桥：与 Ptrade 客户端内嵌桥接策略的本地 HTTP 网关通信。
// 全部 HTTP 桥逻辑由共享 httpBridge 承载（见 httpbridge.go），内嵌后自动满足 Broker 接口。
type PtradeBroker struct {
	*httpBridge
}

// NewPtradeBroker 创建 Ptrade 实盘桥（不发起连接，需调用 Connect()）。
func NewPtradeBroker(cfg PtradeConfig) *PtradeBroker {
	return &PtradeBroker{httpBridge: newHTTPBridge(httpBridgeConfig{
		mode:     ModePtrade,
		name:     "Ptrade",
		hint:     "Ptrade 客户端已运行桥接策略",
		host:     cfg.Host,
		httpPort: cfg.HTTPPort,
		account:  cfg.Account,
		timeout:  cfg.Timeout,
	})}
}
