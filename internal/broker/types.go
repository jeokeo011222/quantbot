// Package broker 提供统一的交易执行桥：上层(CIO/操盘手/补确认)通过 Broker 接口下单，
// 实盘模式(QMT)走常驻 Python 网关真实下单并回填成交，模拟模式保持现有记账路径。
package broker

import (
	"fmt"
	"strings"
	"time"
)

// Mode 交易模式标识。
type Mode string

const (
	// ModeSimulated 模拟成交（默认，现有路径：写入 trades 记账，不下真实单）。
	ModeSimulated Mode = "simulated"
	// ModeLive QMT 实盘成交。
	ModeLive Mode = "qmt"
)

// Side 买卖方向。
type Side string

const (
	SideBuy  Side = "BUY"
	SideSell Side = "SELL"
)

// Order 一条提交给券商的下单指令。
type Order struct {
	// Symbol 本工程标准符号（sh600519 / sz000002 / bj430047）。
	Symbol     string
	StockName  string
	Side       Side
	Quantity   int     // 股数
	Price      float64 // 0 表示按最新价(市价/对手价)，>0 表示限价
	DecisionID string  // 关联决策/操作痕迹
	Reason     string
}

// Fill 成交回报。
type Fill struct {
	OrderID    string
	Symbol     string
	Side       Side
	Quantity   int
	Price      float64
	TradedAt   time.Time
	ExternalID string // QMT 委托/成交号
}

// Position 券商侧持仓。
type Position struct {
	Symbol    string
	Quantity  int
	Available int // 可用/可卖数量
	CostPrice float64
	Market    string // 展示用
}

// Asset 券商侧资金与市值。
type Asset struct {
	Cash            float64
	MarketValue     float64
	TotalAssets     float64
	Frozen          float64 // 冻结资金
	Available       float64
	ExternalMessage string
}

// Status 连接/环境状态。
type Status struct {
	Mode       Mode    `json:"mode"`
	Connected  bool    `json:"connected"`
	PythonOK   bool    `json:"python_ok"`
	XtQuantOK  bool    `json:"xtquant_ok"`
	Account    string  `json:"account"`
	Cash       float64 `json:"cash"`
	MarketVal  float64 `json:"market_value"`
	Message    string  `json:"message"`
	PythonPath string  `json:"python_path"`
}

// Broker 交易执行桥接口。实盘(QMT)下 IsLive()==true，模拟下 IsLive()==false。
type Broker interface {
	// Mode 返回当前模式。
	Mode() Mode
	// IsLive 是否实盘真实下单。
	IsLive() bool
	// SubmitOrder 提交订单，返回券商/本地订单引用。
	// 模拟模式返回本地占位单号；实盘模式返回 QMT 委托号。
	SubmitOrder(o Order) (string, error)
	// QueryPositions 查询券商侧当前持仓。
	QueryPositions() ([]Position, error)
	// QueryAsset 查询券商侧资金/资产。
	QueryAsset() (Asset, error)
	// Status 返回连接与环境状态。
	Status() Status
	// Close 释放资源（停网关进程等）。
	Close() error
}

// ToQmtSymbol 把本工程标准符号换算为 XtQuant 股票代码：sh600519 -> 600519.SH。
func ToQmtSymbol(symbol string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(symbol))
	if s == "" {
		return "", fmt.Errorf("空股票代码")
	}
	// 已带交易所后缀则原样返回
	if strings.Contains(s, ".") {
		code := strings.ToUpper(s[:strings.Index(s, ".")])
		suffix := strings.ToUpper(s[strings.Index(s, ".")+1:])
		return code + "." + suffix, nil
	}
	switch {
	case strings.HasPrefix(s, "sh"):
		return strings.ToUpper(s[2:]) + ".SH", nil
	case strings.HasPrefix(s, "sz"):
		return strings.ToUpper(s[2:]) + ".SZ", nil
	case strings.HasPrefix(s, "bj"):
		return strings.ToUpper(s[2:]) + ".BJ", nil
	}
	// 纯数字裸码：无法确定市场，交由调用方传入市场
	return "", fmt.Errorf("无法识别的股票代码(需带 sh/sz/bj 前缀): %s", symbol)
}

// FromQmtCode 由 QMT 代码还原为本工程标准符号：600519.SH -> sh600519。
func FromQmtCode(code string) string {
	c := strings.ToUpper(strings.TrimSpace(code))
	upper := strings.ToUpper(c)
	for _, suf := range []string{".SH", ".SZ", ".BJ"} {
		if strings.HasSuffix(upper, suf) {
			digits := strings.TrimSuffix(upper, suf)
			return strings.ToLower(suf[1:2]) + strings.ToLower(digits)
		}
	}
	return strings.ToLower(c)
}
