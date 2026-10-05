package main

import (
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/quantpilot/quantpilot/internal/broker"
	"github.com/quantpilot/quantpilot/internal/xtquant"
)

// initBroker 按交易模式与券商配置初始化交易执行桥。
//   - 模拟模式（默认）：SimulatedBroker，不接真实券商，成交仍由 portfolioEngine 记账。
//   - 实盘模式 + 已启用 QMT：QMTBroker，真实下单；成交回报经 onBrokerFill 记入账本。
//   - 实盘模式 + 已启用 Ptrade：PtradeBroker，经本地 HTTP 桥接 Ptrade 客户端策略。
func (a *App) initBroker() {
	if a.configManager == nil {
		a.broker = broker.NewSimulatedBroker("", 0)
		return
	}
	cfg := a.configManager.GetConfig()
	if cfg.TradingMode == "live" && cfg.QMTEnabled && strings.TrimSpace(cfg.QMTPath) != "" {
		acctType := cfg.QMTAccountType
		if acctType == "" {
			acctType = "STOCK"
		}
		qb := broker.NewQMTBroker(broker.QMTConfig{
			HTTPPort:    cfg.QmtHTTPPort,
			ScriptPath:  xtquant.GetFolderPath() + "/qmt_bridge.py",
			FolderPath:  xtquant.GetFolderPath(),
			Account:     cfg.QMTAccount,
			AccountType: acctType,
		})
		qb.SetTradeCallback(a.onBrokerFill)
		a.broker = qb
		log.Printf("[QuantBot] 交易执行桥: QMT 实盘 (account=%s, path=%s, port=%d)", cfg.QMTAccount, cfg.QMTPath, cfg.QmtHTTPPort)
		return
	}
	if cfg.TradingMode == "live" && cfg.PtradeEnabled && cfg.PtradeHTTPPort > 0 {
		pb := broker.NewPtradeBroker(broker.PtradeConfig{
			HTTPPort: cfg.PtradeHTTPPort,
			Account:  cfg.PtradeAccount,
		})
		pb.SetTradeCallback(a.onBrokerFill)
		a.broker = pb
		log.Printf("[QuantBot] 交易执行桥: Ptrade 实盘 (account=%s, port=%d)", cfg.PtradeAccount, cfg.PtradeHTTPPort)
		return
	}
	a.broker = broker.NewSimulatedBroker(cfg.QMTAccount, 0)
	log.Printf("[QuantBot] 交易执行桥: 模拟成交")
}

// submitOrderLive 实盘模式下把订单发往券商网关（QMT / Ptrade）。
// 仅在“确为实盘模式且已连接”时放行；模拟模式 / 未连接一律报错，避免误记模拟成交。
func (a *App) submitOrderLive(symbol string, side broker.Side, qty int, price float64, reason, decisionID string) (string, error) {
	if a.broker == nil || !broker.IsLiveMode(a.broker.Mode()) {
		return "", fmt.Errorf("当前非实盘模式，拒绝券商下单")
	}
	if !a.broker.IsLive() {
		return "", fmt.Errorf("实盘桥未连接，拒绝下单：请先在“交易接口”执行测试连接")
	}
	return a.broker.SubmitOrder(broker.Order{
		Symbol: symbol, Side: side, Quantity: qty, Price: price,
		Reason: reason, DecisionID: decisionID,
	})
}

// onBrokerFill 实盘成交回报回调（QMT / Ptrade）：把真实成交记入 portfolioEngine 账本。
func (a *App) onBrokerFill(f broker.Fill) {
	if a.portfolioEngine == nil {
		return
	}
	sym := broker.FromQmtCode(f.Symbol)
	market := marketOfSymbol(sym)
	var err error
	if f.Side == broker.SideBuy {
		_, err = a.portfolioEngine.Buy(sym, "", market, f.Quantity, f.Price, "实盘成交", f.ExternalID)
	} else {
		_, err = a.portfolioEngine.Sell(sym, f.Quantity, f.Price, "实盘成交", f.ExternalID)
	}
	if err != nil {
		log.Printf("[QuantBot] 记录实盘成交失败 %s %d@%.2f: %v", sym, f.Quantity, f.Price, err)
	} else {
		log.Printf("[QuantBot] 实盘成交已记账 %s %d@%.2f", sym, f.Quantity, f.Price)
	}
}

// marketOfSymbol 由内部符号(如 sh600519)推导市场代码。
func marketOfSymbol(sym string) string {
	s := strings.ToLower(sym)
	switch {
	case strings.HasPrefix(s, "sh"):
		return "SH"
	case strings.HasPrefix(s, "sz"):
		return "SZ"
	case strings.HasPrefix(s, "bj"):
		return "BJ"
	}
	return ""
}

// ==================== 前端可调用 API ====================

// getQMTBroker 返回底层 QMT 实盘桥（若非实盘模式返回 nil）。
func (a *App) getQMTBroker() *broker.QMTBroker {
	if a.broker == nil || a.broker.Mode() != broker.ModeLive {
		return nil
	}
	if qb, ok := a.broker.(*broker.QMTBroker); ok {
		return qb
	}
	return nil
}

// getPtradeBroker 返回底层 Ptrade 实盘桥（若非 Ptrade 模式返回 nil）。
func (a *App) getPtradeBroker() *broker.PtradeBroker {
	if a.broker == nil || a.broker.Mode() != broker.ModePtrade {
		return nil
	}
	if pb, ok := a.broker.(*broker.PtradeBroker); ok {
		return pb
	}
	return nil
}

var brokerAPIMu sync.Mutex // 串行化连接/断开，避免并发改进程状态

// GetBrokerStatus 获取当前交易执行桥状态（实盘/模拟、连接、环境）。
func (a *App) GetBrokerStatus() (broker.Status, error) {
	if a.broker == nil {
		return broker.Status{}, fmt.Errorf("交易执行桥未初始化")
	}
	return a.broker.Status(), nil
}

// ConnectQMT 连接 QMT 客户端内策略桥（纯 HTTP 探测：需 QMT 客户端已运行 qmt_bridge.py 策略）。
func (a *App) ConnectQMT() error {
	brokerAPIMu.Lock()
	defer brokerAPIMu.Unlock()
	qb := a.getQMTBroker()
	if qb == nil {
		return fmt.Errorf("当前非实盘模式，或未启用 QMT：请先在“设置→交易接口”启用 QMT 并保存")
	}
	if qb.IsLive() {
		return nil
	}
	if err := qb.Connect(); err != nil {
		return err
	}
	log.Printf("[QuantBot] QMT 实盘桥已连接")
	return nil
}

// DisconnectQMT 断开 QMT 客户端内策略桥（停止成交回报轮询）。
func (a *App) DisconnectQMT() error {
	brokerAPIMu.Lock()
	defer brokerAPIMu.Unlock()
	qb := a.getQMTBroker()
	if qb == nil {
		return nil
	}
	if err := qb.Close(); err != nil {
		return err
	}
	return nil
}

// QueryQMTAsset 查询 QMT 资金/资产（需已连接）。
func (a *App) QueryQMTAsset() (broker.Asset, error) {
	qb := a.getQMTBroker()
	if qb == nil {
		return broker.Asset{}, fmt.Errorf("当前非实盘模式或未启用 QMT")
	}
	return qb.QueryAsset()
}

// QueryQMTPositions 查询 QMT 当前持仓（需已连接）。
func (a *App) QueryQMTPositions() ([]broker.Position, error) {
	qb := a.getQMTBroker()
	if qb == nil {
		return nil, fmt.Errorf("当前非实盘模式或未启用 QMT")
	}
	return qb.QueryPositions()
}

// ConnectPtrade 连接 Ptrade 桥接网关（需 Ptrade 客户端已运行桥接策略）。
func (a *App) ConnectPtrade() error {
	brokerAPIMu.Lock()
	defer brokerAPIMu.Unlock()
	pb := a.getPtradeBroker()
	if pb == nil {
		return fmt.Errorf("当前非 Ptrade 模式，或未启用 Ptrade：请先在“设置→交易接口”启用 Ptrade 并保存")
	}
	if pb.IsLive() {
		return nil
	}
	if err := pb.Connect(); err != nil {
		return err
	}
	log.Printf("[QuantBot] Ptrade 实盘桥已连接")
	return nil
}

// DisconnectPtrade 断开并停止 Ptrade 桥接。
func (a *App) DisconnectPtrade() error {
	brokerAPIMu.Lock()
	defer brokerAPIMu.Unlock()
	pb := a.getPtradeBroker()
	if pb == nil {
		return nil
	}
	return pb.Close()
}

// QueryPtradeAsset 查询 Ptrade 资金/资产（需已连接）。
func (a *App) QueryPtradeAsset() (broker.Asset, error) {
	pb := a.getPtradeBroker()
	if pb == nil {
		return broker.Asset{}, fmt.Errorf("当前非 Ptrade 模式或未启用 Ptrade")
	}
	return pb.QueryAsset()
}

// QueryPtradePositions 查询 Ptrade 当前持仓（需已连接）。
func (a *App) QueryPtradePositions() ([]broker.Position, error) {
	pb := a.getPtradeBroker()
	if pb == nil {
		return nil, fmt.Errorf("当前非 Ptrade 模式或未启用 Ptrade")
	}
	return pb.QueryPositions()
}

// CancelBrokerOrder 撤单：实盘模式下按券商委托号撤单（QMT / Ptrade）。
func (a *App) CancelBrokerOrder(orderID string) error {
	if a.broker == nil || !broker.IsLiveMode(a.broker.Mode()) {
		return fmt.Errorf("当前非实盘模式，无法撤单")
	}
	return a.broker.CancelOrder(orderID)
}
