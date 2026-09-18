package util

import (
	"strconv"
	"strings"
	"time"
)

// 交易时间纪律：与 position_manager 仓位管理工具共用同一套时段规则，
// 供仓位决策、可交易股票池自动买卖等所有自动化交易路径统一遵循。
// 规范（默认，可配置）：
//   10:00前          只观察、不操作，确认日内趋势与支撑压力位
//   10:00-11:20      企稳建底仓，趋势确认小幅加仓
//   13:30-14:20      二次优化仓位，低吸高抛做T降本
//   14:40后          停止开仓加仓，分批止盈、减弱势仓

// TradingWindowConfig 交易时段纪律规则
type TradingWindowConfig struct {
	Name      string `json:"name"`       // 时段名称
	Start     string `json:"start"`      // 开始 HH:MM（含）
	End       string `json:"end"`        // 结束 HH:MM（不含），空表示到当日结束
	AllowBuy  bool   `json:"allow_buy"`  // 允许建仓
	AllowAdd  bool   `json:"allow_add"`  // 允许加仓
	AllowSell bool   `json:"allow_sell"` // 允许减仓/止盈
	Note      string `json:"note"`       // 操作说明
}

// DefaultTradingWindows 默认交易时间纪律（与用户规范一致）
func DefaultTradingWindows() []TradingWindowConfig {
	return []TradingWindowConfig{
		{Name: "观察期", Start: "09:30", End: "10:00", AllowBuy: false, AllowAdd: false, AllowSell: false, Note: "只观察，确认日内趋势与支撑压力位，不操作"},
		{Name: "建仓期", Start: "10:00", End: "11:20", AllowBuy: true, AllowAdd: true, AllowSell: false, Note: "企稳建底仓，趋势确认小幅加仓"},
		{Name: "优化期", Start: "13:30", End: "14:20", AllowBuy: true, AllowAdd: true, AllowSell: true, Note: "二次优化仓位，低吸高抛做T降本"},
		{Name: "收盘期", Start: "14:40", End: "", AllowBuy: false, AllowAdd: false, AllowSell: true, Note: "停止开仓加仓，分批止盈、减弱势仓"},
	}
}

// TradingWindowState 当前所处时段状态
type TradingWindowState struct {
	Name      string // 时段名称
	InWindow  bool   // 是否落在已配置的主动时段
	AllowBuy  bool   // 允许建仓
	AllowAdd  bool   // 允许加仓
	AllowSell bool   // 允许减仓/止盈
	Note      string // 时段操作说明
}

// TradingWindowAt 判断给定时间所处的交易时段（按北京时间）。
// 未落入任何主动时段（如 11:20-13:30 午间、14:20-14:40）时保守处理：
// 不允许开仓/加仓，允许减仓（风险控制优先）。
func TradingWindowAt(windows []TradingWindowConfig, now time.Time) TradingWindowState {
	loc := time.FixedZone("CST", 8*3600)
	beijingTime := now.In(loc)
	nowMin := beijingTime.Hour()*60 + beijingTime.Minute()

	if len(windows) == 0 {
		windows = DefaultTradingWindows()
	}

	for _, w := range windows {
		start, ok := ParseHHMM(w.Start)
		if !ok || nowMin < start {
			continue
		}
		if w.End != "" {
			end, ok := ParseHHMM(w.End)
			if !ok {
				continue
			}
			if nowMin < end {
				return TradingWindowState{Name: w.Name, InWindow: true, AllowBuy: w.AllowBuy, AllowAdd: w.AllowAdd, AllowSell: w.AllowSell, Note: w.Note}
			}
		} else {
			return TradingWindowState{Name: w.Name, InWindow: true, AllowBuy: w.AllowBuy, AllowAdd: w.AllowAdd, AllowSell: w.AllowSell, Note: w.Note}
		}
	}
	// 非主动时段：保守，允许减仓、禁止开仓/加仓
	return TradingWindowState{Name: "非主动时段", InWindow: false, AllowBuy: false, AllowAdd: false, AllowSell: true, Note: "未落入主动交易时段（午间/过渡期），不新开仓不加仓，允许减仓"}
}

// ParseHHMM 解析 "HH:MM" 为分钟数
func ParseHHMM(s string) (int, bool) {
	parts := strings.Split(s, ":")
	if len(parts) != 2 {
		return 0, false
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}
