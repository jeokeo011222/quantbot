package cio

import (
	"fmt"
	"sort"

	"github.com/quantpilot/quantpilot/internal/marketsixdim"
)

// MarketConsensus 市场共识提炼：由六维判势的确定性规则派生"共识方向/主要分歧/次日待验证条件"。
// 全部来自真实六维判势数值（DimScores/冲突数/标签），不调用外部观点，不伪造。
type MarketConsensus struct {
	Direction        string   `json:"direction"`         // bullish / neutral / bearish
	Strength         float64  `json:"strength"`          // 共识强度 0~1
	Rationale        string   `json:"rationale"`         // 摘要依据
	Divergences      []string `json:"divergences"`       // 维度间主要分歧
	ValidateTomorrow []string `json:"validate_tomorrow"` // 下一交易日需验证的条件
}

// dimNameZH 六维维度的中文名（用于生成可读的分歧/验证条件文案）。
var dimNameZH = map[string]string{
	"tech":      "技术趋势",
	"breadth":   "市场广度",
	"volume":    "量能流动性",
	"capital":   "资金结构",
	"sentiment": "情绪赚钱效应",
	"external":  "隔夜外围",
}

// ComputeMarketConsensus 由六维判势结果派生市场共识/分歧/次日待验证条件。
// 纯确定性规则：方向由总分与标签映射，强度以偏离中线程度×冲突折减，次条件引用具体维度。
func ComputeMarketConsensus(report *sixdim.MarketReport) *MarketConsensus {
	c := &MarketConsensus{Direction: "neutral", Strength: 0}
	if report == nil {
		return c
	}
	score := report.AdjustedTotalScore
	conflict := report.ConflictCount

	// 方向：标签优先，其次总分阈值
	switch report.MarketTag {
	case "退潮风险":
		c.Direction = "bearish"
	case "强势":
		if score >= 62 {
			c.Direction = "bullish"
		} else {
			c.Direction = "neutral"
		}
	default:
		switch {
		case score >= 65:
			c.Direction = "bullish"
		case score <= 40:
			c.Direction = "bearish"
		default:
			c.Direction = "neutral"
		}
	}

	// 天数不足 1 计数兜底
	minInt := func(a, b int) int {
		if a < b {
			return a
		}
		return b
	}

	// 共识强度：偏离中线程度 × (1 - 冲突折减)
	stretch := (score - 50) / 40
	if stretch < 0 {
		stretch = -stretch
	}
	if stretch > 1 {
		stretch = 1
	}
	c.Strength = stretch * (1 - float64(minInt(conflict, 6))/6*0.5)

	c.Rationale = report.MarketTag
	if score > 0 {
		c.Rationale = report.MarketTag + " (总分 " + fmt.Sprintf("%.1f", score) + ")"
	}

	// 维度间分歧：最强与最弱维度反差过大时记录
	keys := make([]string, 0, len(report.DimScores))
	for k := range report.DimScores {
		keys = append(keys, k)
	}
	if len(keys) >= 2 {
		sort.Slice(keys, func(i, j int) bool { return report.DimScores[keys[i]] < report.DimScores[keys[j]] })
		lowK, highK := keys[0], keys[len(keys)-1]
		lo, hi := report.DimScores[lowK], report.DimScores[highK]
		if hi-lo >= 25 {
			c.Divergences = append(c.Divergences,
				nameDim(lowK)+"偏弱("+fmt.Sprintf("%.1f", lo)+") 与 "+nameDim(highK)+"偏强("+fmt.Sprintf("%.1f", hi)+") 反差明显")
		}
		if conflict >= 2 {
			c.Divergences = append(c.Divergences, "多维信号冲突("+fmt.Sprintf("%d", conflict)+"处)，方向待确认")
		}
	}

	// 次日待验证条件（引用具体维度，供复盘/辩论在下一交易日核验）
	addCond := func(s string) { c.ValidateTomorrow = append(c.ValidateTomorrow, s) }
	if v, ok := report.DimScores["volume"]; ok && v >= 60 {
		addCond("量能保持充沛，验证量价配合是否延续")
	} else if ok && v < 40 {
		addCond("量能萎缩，验证是否确认退潮/弱势预期")
	}
	if v, ok := report.DimScores["sentiment"]; ok && v >= 60 {
		addCond("情绪(涨停/炸板)高位，验证是否出现分歧兑现/走弱")
	} else if ok && v < 35 {
		addCond("情绪修复情况，验证是否出现修复或继续退潮")
	}
	if report.PositionRate < 0.3 {
		addCond("仓位系数偏低，验证是否维持低位/止跌企稳")
	}
	if conflict >= 2 {
		addCond("等待下一交易日方向确认后再提高置信")
	}
	if len(c.ValidateTomorrow) == 0 {
		c.ValidateTomorrow = append(c.ValidateTomorrow, "按基线跟踪各维度变化确认方向")
	}
	return c
}

func nameDim(k string) string {
	if n, ok := dimNameZH[k]; ok {
		return n
	}
	return k
}
