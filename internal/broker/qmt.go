package broker

// EventMessage 网关推送的异步事件。
type EventMessage struct {
	Kind     string // order / trade / asset / disconnected
	Symbol   string
	Side     Side
	Volume   int
	Price    float64
	External string
}

// QMTConfig 迅投 QMT 客户端内策略桥配置。
// QMT 策略脚本运行在 QMT 客户端「策略交易」面板内置 Python 引擎中，通过本地 HTTP 与 QuantBot 通信，
// 无需 miniQMT / 外部 XtQuant 库。
type QMTConfig struct {
	// HTTPPort 桥接网关 HTTP 端口（默认 8892）。
	HTTPPort int
	// ScriptPath qmt_bridge.py 的绝对路径（生成在 exe 目录 XtQuant/ 下）。
	ScriptPath string
	// FolderPath XtQuant 文件夹路径（写入 config.json）。
	FolderPath string
	// Account 券商资金账号。
	Account string
	// AccountType STOCK(STOCK) / CREDIT(信用)。
	AccountType string
}

// QMTBroker QMT 实盘桥：与 QMT 客户端内嵌桥接策略的本地 HTTP 网关通信。
// 全部 HTTP 桥逻辑由共享 httpBridge 承载（见 httpbridge.go），内嵌后自动满足 Broker 接口。
type QMTBroker struct {
	*httpBridge
}

// NewQMTBroker 创建 QMT 实盘桥（不发起连接，需调用 Connect()）。
func NewQMTBroker(cfg QMTConfig) *QMTBroker {
	return &QMTBroker{httpBridge: newHTTPBridge(httpBridgeConfig{
		mode:     ModeLive,
		name:     "QMT",
		hint:     "QMT 客户端已运行桥接策略",
		host:     "127.0.0.1",
		httpPort: cfg.HTTPPort,
		account:  cfg.Account,
	})}
}
