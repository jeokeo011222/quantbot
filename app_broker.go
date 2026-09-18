package main

import (
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/quantpilot/quantpilot/internal/broker"
	"github.com/quantpilot/quantpilot/internal/xtquant"
)

// initBroker 按交易模式与 QMT 配置初始化交易执行桥。
//   - 模拟模式（默认）：SimulatedBroker，不接真实券商，成交仍由 portfolioEngine 记账。
//   - 实盘模式 + 已启用 QMT：QMTBroker，真实下单；成交回报经 onBrokerFill 记入账本。
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
			ScriptPath:  xtquant.GetFolderPath() + "/xtquant_interface.py",
			FolderPath:  xtquant.GetFolderPath(),
			QtPath:      cfg.QMTPath,
			Account:     cfg.QMTAccount,
			AccountType: acctType,
		})
		qb.SetTradeCallback(a.onBrokerFill)
		a.broker = qb
		log.Printf("[QuantBot] 交易执行桥: QMT 实盘 (account=%s, path=%s)", cfg.QMTAccount, cfg.QMTPath)
		return
	}
	a.broker = broker.NewSimulatedBroker(cfg.QMTAccount, 0)
	log.Printf("[QuantBot] 交易执行桥: 模拟成交")
}

// submitOrderLive 实盘模式下把订单发往 QMT 网关。
// 仅在“确为实盘模式且已连接”时放行；模拟模式 / 未连接一律报错，避免误记模拟成交。
func (a *App) submitOrderLive(symbol string, side broker.Side, qty int, price float64, reason, decisionID string) (string, error) {
	if a.broker == nil || a.broker.Mode() != broker.ModeLive {
		return "", fmt.Errorf("当前非实盘模式，拒绝 QMT 下单")
	}
	if !a.broker.IsLive() {
		return "", fmt.Errorf("QMT 实盘桥未连接，拒绝下单：请先在“交易接口”执行测试连接")
	}
	return a.broker.SubmitOrder(broker.Order{
		Symbol: symbol, Side: side, Quantity: qty, Price: price,
		Reason: reason, DecisionID: decisionID,
	})
}

// onBrokerFill QMT 成交回报回调：把真实成交记入 portfolioEngine 账本（实盘下账目权威来源）。
func (a *App) onBrokerFill(f broker.Fill) {
	if a.portfolioEngine == nil {
		return
	}
	sym := broker.FromQmtCode(f.Symbol)
	market := marketOfSymbol(sym)
	var err error
	if f.Side == broker.SideBuy {
		_, err = a.portfolioEngine.Buy(sym, "", market, f.Quantity, f.Price, "QMT 实盘成交", f.ExternalID)
	} else {
		_, err = a.portfolioEngine.Sell(sym, f.Quantity, f.Price, "QMT 实盘成交", f.ExternalID)
	}
	if err != nil {
		log.Printf("[QuantBot] 记录 QMT 实盘成交失败 %s %d@%.2f: %v", sym, f.Quantity, f.Price, err)
	} else {
		log.Printf("[QuantBot] QMT 实盘成交已记账 %s %d@%.2f", sym, f.Quantity, f.Price)
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

var brokerAPIMu sync.Mutex // 串行化连接/断开，避免并发改进程状态

// GetBrokerStatus 获取当前交易执行桥状态（实盘/模拟、连接、环境）。
func (a *App) GetBrokerStatus() (broker.Status, error) {
	if a.broker == nil {
		return broker.Status{}, fmt.Errorf("交易执行桥未初始化")
	}
	return a.broker.Status(), nil
}

// ConnectQMT 启动并连接 QMT 网关（真实 connect：需 MiniQMT 已登录）。
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
	if err := qb.MustConnect(); err != nil {
		return fmt.Errorf("QMT 连接失败：%w", err)
	}
	log.Printf("[QuantBot] QMT 实盘桥已连接")
	return nil
}

// DisconnectQMT 断开并停止 QMT 网关。
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
