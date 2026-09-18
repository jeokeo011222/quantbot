package broker

import (
	"sync/atomic"
	"time"
)

// SimulatedBroker 模拟成交桥：不下真实单，SubmitOrder 直接返回本地占位单号。
// 这是引擎默认路径——成交仍由 portfolioEngine.Buy/Sell 记账完成。
type SimulatedBroker struct {
	account string
	cash    float64
	live    bool // 模拟模式永远为 false
	counter int64
}

// NewSimulatedBroker 创建模拟成交桥。
// account/cash 仅用于展示，不影响引擎记账（真实资金在 portfolioEngine 内）。
func NewSimulatedBroker(account string, cash float64) *SimulatedBroker {
	return &SimulatedBroker{account: account, cash: cash}
}

// Mode 返回模式（模拟）。
func (b *SimulatedBroker) Mode() Mode { return ModeSimulated }

// IsLive 模拟模式不为实盘。
func (b *SimulatedBroker) IsLive() bool { return false }

// SubmitOrder 模拟下单：直接返回占位单号，不做任何真实委托。
func (b *SimulatedBroker) SubmitOrder(o Order) (string, error) {
	return b.genID(), nil
}

func (b *SimulatedBroker) genID() string {
	n := atomic.AddInt64(&b.counter, 1)
	return time.Now().Format("SIM20060102150405") + "-" + itoa(int(n))
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	buf := make([]byte, 0, 8)
	for v > 0 {
		buf = append([]byte{byte('0' + v%10)}, buf...)
		v /= 10
	}
	if neg {
		buf = append([]byte{'-'}, buf...)
	}
	return string(buf)
}

// QueryPositions 模拟模式无券商持仓，返回空。
func (b *SimulatedBroker) QueryPositions() ([]Position, error) {
	return nil, nil
}

// QueryAsset 模拟模式返回配置的初始资金信息（仅展示）。
func (b *SimulatedBroker) QueryAsset() (Asset, error) {
	return Asset{Cash: b.cash, TotalAssets: b.cash, Available: b.cash}, nil
}

// Status 模拟模式状态。
func (b *SimulatedBroker) Status() Status {
	return Status{Mode: ModeSimulated, Connected: true, Account: b.account, Cash: b.cash,
		Message: "模拟成交模式（未接入真实券商）"}
}

// Close 模拟模式无需释放资源。
func (b *SimulatedBroker) Close() error { return nil }
