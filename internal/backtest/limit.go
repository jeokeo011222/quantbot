package backtest

import "strings"

// LimitPctForSymbol 由标的代码与名称推断 A 股涨跌停幅度。
// 规则（与上交/深交/北交现行交易规则对齐）：
//   - 名称含 ST / *ST（退市风险警示）→ ±5%
//   - 科创板（688/689，沪市）→ ±20%
//   - 创业板（300/301，深市）→ ±20%
//   - 北交所（8 开头 / 920 / 430）→ ±30%
//   - 其余主板（沪 600/601/603/605，深 000/001/002/003）→ ±10%
//
// code 可为带市场前缀（sh601700 / SH:600519 / 600519.SH）或纯 6 位数字；name 可为空。
func LimitPctForSymbol(code, name string) float64 {
	if strings.Contains(strings.ToUpper(name), "ST") {
		return 0.05
	}
	pure := digitsOfCode(code)
	if len(pure) < 3 {
		return 0.10
	}
	// 科创板 688/689
	if pure[:3] == "688" || pure[:3] == "689" {
		return 0.20
	}
	// 创业板 300/301
	if pure[:3] == "300" || pure[:3] == "301" {
		return 0.20
	}
	// 北交所 8 开头 / 920 / 430
	if pure[0] == '8' || pure[:3] == "920" || pure[:3] == "430" {
		return 0.30
	}
	return 0.10
}

// digitsOfCode 提取标的代码中的连续数字，取前 6 位作为纯净证券代码；
// 宽容处理 "sh601700" / "600000.SH" / "SH:600519" 等常见格式。
func digitsOfCode(code string) string {
	var b []byte
	for i := 0; i < len(code) && len(b) < 6; i++ {
		c := code[i]
		if c >= '0' && c <= '9' {
			b = append(b, c)
		}
	}
	return string(b)
}