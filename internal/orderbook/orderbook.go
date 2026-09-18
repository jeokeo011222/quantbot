// Package orderbook 提供 A 股盘口（order book）状态判定纯函数。
//
// 输入来自实时行情数据源（腾讯/TDX）的真实五档买卖挂单价量、内外盘、量比，
// 输出「涨停封死 / 涨停打开 / 跌停封死 / 跌停打开 / 正常」等盘口状态，
// 供交易执行层（portfolio.Engine.Buy/Sell）与 CIO 决策层共同复用。
//
// 判定规则为可追溯的经验参数（封单量阈值、放量阈值等），全部标注说明，
// 严禁伪造任何数据：行情字段缺失时按「无法判定」保守降级并输出说明。
package orderbook

import (
	"fmt"
	"math"

	"github.com/quantpilot/quantpilot/internal/backtest"
	"github.com/quantpilot/quantpilot/internal/data"
)

// State 盘口状态
type State string

const (
	StateNormal          State = "normal"             // 正常（未触及涨跌停）
	StateSealedLimitUp   State = "sealed_limit_up"    // 涨停封死：买一=涨停价且买一量巨大，买不进
	StateOpenedLimitUp   State = "opened_limit_up"    // 涨停打开：触及涨停但封单薄/有卖盘，可成交
	StateSealedLimitDown State = "sealed_limit_down"  // 跌停封死：卖一=跌停价且卖一量巨大，卖不出
	StateOpenedLimitDown State = "opened_limit_down"  // 跌停打开：触及跌停但封单薄（如巨量换手），可卖出离场
)

// StateChinese 盘口状态中文标签
var StateChinese = map[State]string{
	StateNormal:          "正常",
	StateSealedLimitUp:   "涨停封死",
	StateOpenedLimitUp:   "涨停打开",
	StateSealedLimitDown: "跌停封死",
	StateOpenedLimitDown: "跌停打开",
}

// 封单/放量判定阈值（经验参数，可追溯）：
//   - 封单量阈值 = max(MinSealVolHands, 当日成交量(手)×SealVolRatio)，
//     与当日活跃度挂钩，避免小盘/大盘用同一绝对阈值失真；
//   - 巨量跌停：量比 ≥ HugeVolumeRatio 且触及跌停；
//   - 抛压占优：内盘占比 > InVolDominant。
const (
	MinSealVolHands = 1000.0 // 封单量绝对下限：1000 手（10 万股）
	SealVolRatio    = 0.10   // 封单量 ≥ 当日成交量的 10% 视为封死
	HugeVolumeRatio = 2.0    // 量比 ≥ 2 视为放量（巨量）
	InVolDominant   = 0.60   // 内盘占比 > 0.6 视为抛压占优
)

// Result 盘口判定结果（纯数据，无 I/O）
type Result struct {
	State          State   `json:"state"`
	Chinese        string  `json:"chinese"`
	LimitUp        float64 `json:"limitUp"`
	LimitDown      float64 `json:"limitDown"`
	AtLimitUp      bool    `json:"atLimitUp"`
	AtLimitDown    bool    `json:"atLimitDown"`
	SealVol        float64 `json:"sealVol"`        // 封板侧挂单量（手）
	VolumeRatio    float64 `json:"volumeRatio"`    // 量比
	InOutRatio     float64 `json:"inOutRatio"`     // 内盘占比 0~1
	HugeVolumeDown bool    `json:"hugeVolumeDown"` // 巨量跌停（量比放量 + 触及跌停）
	Reason         string  `json:"reason"`
}

// sealThreshold 封单量阈值（手）
func sealThreshold(totalVolumeHands float64) float64 {
	t := MinSealVolHands
	if r := totalVolumeHands * SealVolRatio; r > t {
		t = r
	}
	return t
}

// Classify 盘口状态判定核心（纯函数，无 I/O）。
//
// 参数说明（单位：价格元、量手）：
//   - price / limitUp / limitDown：现价与涨停/跌停价
//   - bid1Price/bid1Vol、ask1Price/ask1Vol：买一/卖一价与量
//   - inVol/outVol：内盘/外盘（主动卖/主动买）
//   - volumeRatio：量比；totalVolumeHands：当日成交量
//
// 行情字段缺失（买一/卖一价为0）时，仅按现价判定涨跌停，封板状态按「打开」降级
// （不误伤正常离场；与项目「行情缺失不拦截」原则一致）。
func Classify(price, limitUp, limitDown, bid1Price, bid1Vol, ask1Price, ask1Vol, inVol, outVol, volumeRatio, totalVolumeHands float64) Result {
	atLimitUp := limitUp > 0 && price >= limitUp-1e-6
	atLimitDown := limitDown > 0 && price <= limitDown+1e-6

	res := Result{
		LimitUp:     round2(limitUp),
		LimitDown:   round2(limitDown),
		AtLimitUp:   atLimitUp,
		AtLimitDown: atLimitDown,
		VolumeRatio: volumeRatio,
	}
	if inVol+outVol > 0 {
		res.InOutRatio = math.Round(inVol/(inVol+outVol)*100) / 100
	}
	res.HugeVolumeDown = atLimitDown && volumeRatio >= HugeVolumeRatio

	// 涨停封死：买一=涨停价且买一量巨大 → 买单排队买不进
	if atLimitUp && bid1Price >= limitUp-1e-6 && bid1Vol >= sealThreshold(totalVolumeHands) {
		res.State = StateSealedLimitUp
		res.SealVol = bid1Vol
		res.Chinese = StateChinese[StateSealedLimitUp]
		res.Reason = fmt.Sprintf("涨停封死：买一¥%.2f(量%.0f手)挂单排队，买不进", bid1Price, bid1Vol)
		return res
	}
	if atLimitUp {
		res.State = StateOpenedLimitUp
		res.SealVol = bid1Vol
		res.Chinese = StateChinese[StateOpenedLimitUp]
		res.Reason = fmt.Sprintf("涨停打开：触及涨停但封单薄(买一量%.0f手)，可成交", bid1Vol)
		return res
	}

	// 跌停封死：卖一=跌停价且卖一量巨大 → 卖单排队卖不出
	if atLimitDown && ask1Price <= limitDown+1e-6 && ask1Vol >= sealThreshold(totalVolumeHands) {
		res.State = StateSealedLimitDown
		res.SealVol = ask1Vol
		res.Chinese = StateChinese[StateSealedLimitDown]
		res.Reason = fmt.Sprintf("跌停封死：卖一¥%.2f(量%.0f手)排队，卖不出", ask1Price, ask1Vol)
		return res
	}
	if atLimitDown {
		res.State = StateOpenedLimitDown
		res.SealVol = ask1Vol
		res.Chinese = StateChinese[StateOpenedLimitDown]
		res.Reason = fmt.Sprintf("跌停打开：卖一¥%.2f(量%.0f手)封单薄，可卖出离场", ask1Price, ask1Vol)
		return res
	}

	res.State = StateNormal
	res.Chinese = StateChinese[StateNormal]
	res.Reason = "正常"
	return res
}

// ClassifySnapshot 便捷入口：由实时快照 + 股票代码/名称自动推断涨跌停价后判定盘口状态。
func ClassifySnapshot(s data.StockSnapshot) Result {
	limitPct := backtest.LimitPctForSymbol(s.Code, s.Name)
	limitUp, limitDown := 0.0, 0.0
	if s.PrevClose > 0 {
		limitUp = math.Round(s.PrevClose*(1+limitPct)*100) / 100
		limitDown = math.Round(s.PrevClose*(1-limitPct)*100) / 100
	}
	return Classify(
		s.CurrentPrice, limitUp, limitDown,
		firstVal(s.BidPrices), firstVal(s.BidVolumes),
		firstVal(s.AskPrices), firstVal(s.AskVolumes),
		s.InVolume, s.OutVolume, s.VolumeRatio, s.Volume,
	)
}

// firstVal 取数组首元素，空数组返回 0
func firstVal(a []float64) float64 {
	if len(a) > 0 {
		return a[0]
	}
	return 0
}

func round2(v float64) float64 {
	return math.Round(v*100) / 100
}
