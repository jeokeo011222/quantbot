// Package sixdim 实现「量化双六维体系」模块B：市场六维判势 MarketSixDim。
//
// 参考《六维策略.md》模块B：
//   - 盘前/盘中实时运行，评价当日市场环境；
//   - 输出：六维得分(0-100)、原始总分、冲突修正后最终得分、建议仓位系数 position_rate、市场标签；
//   - 策略信号输出后，信号仓位 = 原始信号仓位 × position_rate。
//
// 本包与数据源解耦：Evaluate 等评分函数为纯函数（无 I/O），
// 输入结构 MarketInput 由 Fetcher 从 DuckDB 真实数据填充（缺失源用真实代理指标并标注），
// 保证「严禁伪造数据」——任何无真实数据源的维度都明确标注为中性默认，不猜测。
package sixdim

import "math"

// 维度权重（总和 = 1.0，对应六维策略.md 权重配置）
var DimWeights = map[string]float64{
	"tech":      0.15,
	"breadth":   0.20,
	"volume":    0.15,
	"capital":   0.15,
	"sentiment": 0.25,
	"external":  0.10,
}

// DimOrder 维度固定顺序（保证输出稳定）
var DimOrder = []string{"tech", "breadth", "volume", "capital", "sentiment", "external"}

// DimChinese 维度中文名
var DimChinese = map[string]string{
	"tech":      "技术趋势",
	"breadth":   "市场广度",
	"volume":    "量能流动性",
	"capital":   "资金结构",
	"sentiment": "情绪赚钱效应",
	"external":  "外部约束",
}

// ==================== 输入结构（对应 input_market） ====================

// DimTech 维度1 技术趋势
type DimTech struct {
	IndexAboveMA20 bool // 指数收盘价是否站上 MA20
	IndexAboveMA60 bool // 指数收盘价是否站上 MA60
	TrendState     int  // 1上升，0震荡，-1下降
}

// DimBreadth 维度2 市场广度
type DimBreadth struct {
	RisePct               float64 // 上涨家数占比 0-1
	HighLowRatio          float64 // 新高家数 / 新低家数（>1 多头占优）
	LimitUpLimitDownRatio float64 // 涨跌停比（剔除ST）
	HotlineCount          int     // 有效主线板块数量，0代表盘面混乱一日游
}

// DimVolume 维度3 量能流动性
type DimVolume struct {
	TotalAmtRatio    float64 // 当前全市场成交额 / 20日平均成交额
	PriceVolumeMatch int     // 1价涨量增；0无量；-1价涨缩量/价跌放量
}

// DimCapital 维度4 资金结构
type DimCapital struct {
	NorthContinuous    int  // 北向连续流入天数（负代表连续流出）
	CapitalConcentrate bool // True资金集中主线；False资金到处游击
}

// DimSentiment 维度5 情绪赚钱效应
type DimSentiment struct {
	LimitUpCnt     int     // 涨停家数
	BlowUpRate     float64 // 炸板率 0-1
	NonStLimitDown int     // 非ST跌停家数
}

// DimExternal 维度6 外部约束
type DimExternal struct {
	OvernightUS     int     // 1外围向好 0中性 -1外围利空（由跳空缺口方向推断）
	OvernightGapPct float64 // 上证指数开盘相对前收盘跳空幅度(%)，真实数据，用于连续化评分
	EventRisk       bool    // True存在重大风险事件
}

// MarketInput 六维判势输入（对应 input_market）
type MarketInput struct {
	Dim1Tech      DimTech
	Dim2Breadth   DimBreadth
	Dim3Volume    DimVolume
	Dim4Capital   DimCapital
	Dim5Sentiment DimSentiment
	Dim6External  DimExternal

	// Sources 数据源标注：记录每个维度的真实来源/代理说明，保证透明可审计、不伪造
	Sources map[string]string
}

// ==================== 输出结构（对应 output_market_report） ====================

// MarketReport 六维判势输出
type MarketReport struct {
	DimScores          map[string]float64 `json:"dim_scores"`           // 各维度 0-100
	RawTotalScore      float64            `json:"raw_total_score"`      // 未冲突修正原始总分
	ConflictCount      int                `json:"conflict_count"`       // 互相矛盾维度数量
	AdjustedTotalScore float64            `json:"adjusted_total_score"` // 冲突修正后最终得分
	PositionRate       float64            `json:"position_rate"`        // 建议仓位系数 0.0~1.0
	MarketTag          string             `json:"market_tag"`           // 强势/结构性震荡/偏弱/退潮风险
	Sources            map[string]string  `json:"sources,omitempty"`    // 数据源标注
	AsOf               string             `json:"as_of,omitempty"`      // 数据日期（由调用方填充）
}

// ==================== 维度评分函数（0-100） ====================

// dim1TechScore 技术趋势
func dim1TechScore(t DimTech) float64 {
	s := 50.0
	if t.IndexAboveMA20 {
		s += 15
	} else {
		s -= 15
	}
	if t.IndexAboveMA60 {
		s += 20
	} else {
		s -= 20
	}
	switch t.TrendState {
	case 1:
		s += 15
	case -1:
		s -= 15
	}
	return clampScore(s)
}

// dim2BreadthScore 市场广度
func dim2BreadthScore(b DimBreadth) float64 {
	s := b.RisePct * 60.0 // 上涨占比贡献 0-60
	if b.HighLowRatio > 0 {
		if b.HighLowRatio >= 1 {
			s += math.Min(b.HighLowRatio, 3) * 6 // 新高占优 +0..18
		} else {
			s -= (1 - b.HighLowRatio) * 8 // 新低占优 -0..8
		}
	}
	if b.LimitUpLimitDownRatio > 0 {
		if b.LimitUpLimitDownRatio >= 1 {
			s += math.Min(b.LimitUpLimitDownRatio, 3) * 4 // 涨停强于跌停 +0..12
		} else {
			s -= (1 - b.LimitUpLimitDownRatio) * 8
		}
	}
	s += math.Min(float64(b.HotlineCount), 5) * 3 // 主线板块越多 +0..15
	return clampScore(s)
}

// dim3VolumeScore 量能流动性
func dim3VolumeScore(v DimVolume) float64 {
	s := 50.0
	s += (v.TotalAmtRatio - 1) * 40 // 放量增分、缩量减分（±无量纲）
	switch v.PriceVolumeMatch {
	case 1:
		s += 20
	case -1:
		s -= 20
	}
	return clampScore(s)
}

// dim4CapitalScore 资金结构
func dim4CapitalScore(c DimCapital) float64 {
	s := 50.0
	s += math.Max(-5, math.Min(5, float64(c.NorthContinuous))) * 4 // 连续流入+，流出-
	if c.CapitalConcentrate {
		s += 10 // 资金集中主线，赚钱效应聚焦
	} else {
		s -= 5 // 资金到处游击，持续性差
	}
	return clampScore(s)
}

// dim5SentimentScore 情绪赚钱效应
func dim5SentimentScore(sm DimSentiment) float64 {
	s := 50.0
	s += math.Min(float64(sm.LimitUpCnt), 100) * 0.3  // 涨停越多越热 +0..30
	s -= math.Min(sm.BlowUpRate, 1) * 50              // 炸板率越高越差 -0..50
	s -= math.Min(float64(sm.NonStLimitDown), 10) * 3 // 跌停越多越差 -0..30
	return clampScore(s)
}

// dim6ExternalScore 外部约束
// 以上证指数开盘跳空缺口（真实数据，代理隔夜外围/消息面对A股开盘的压力）连续化评分：
// 缺口 ±2% 映射 ±20 分，避免正常交易日因阈值而恒为中性50；极端缺口再叠加方向强化。
func dim6ExternalScore(ex DimExternal) float64 {
	s := 50.0
	s += clampSigned(ex.OvernightGapPct*20, 20) // 连续缺口贡献 ±20
	s += float64(ex.OvernightUS) * 5            // 方向性微调（由缺口阈值推断，见 overnightProxy）
	if ex.EventRisk {
		s -= 30
	}
	return clampScore(s)
}

// ==================== 冲突修正 / 总分 / 仓位系数 ====================

// CheckDimConflict 统计互相矛盾维度数量：
// 高分(≥75)与低分(≤40)同时出现时，矛盾维度数 = 高分维度数 + 低分维度数；否则为 0。
func CheckDimConflict(scores map[string]float64) int {
	high, low := 0, 0
	for _, k := range DimOrder {
		s := scores[k]
		if s >= 75 {
			high++
		}
		if s <= 40 {
			low++
		}
	}
	if high > 0 && low > 0 {
		return high + low
	}
	return 0
}

// ScoreToPosition 修正后分数 → 仓位系数 + 市场标签（对应六维策略.md 分数-仓位-标签映射表）
func ScoreToPosition(adj float64) (float64, string) {
	switch {
	case adj >= 80:
		r := 0.8 + (adj-80)/20*0.2 // 0.8~1.0
		if r > 1.0 {
			r = 1.0
		}
		return round2(r), "强势"
	case adj >= 60:
		return round2(0.4 + (adj-60)/20*0.2), "结构性震荡" // 0.4~0.6
	case adj >= 40:
		return round2(0.2 + (adj-40)/20*0.1), "偏弱" // 0.2~0.3
	default:
		return round2(adj / 40 * 0.1), "退潮风险" // 0.0~0.1
	}
}

// Evaluate 六维判势统一入口（纯函数，无 I/O）
func Evaluate(in *MarketInput) *MarketReport {
	scores := map[string]float64{
		"tech":      dim1TechScore(in.Dim1Tech),
		"breadth":   dim2BreadthScore(in.Dim2Breadth),
		"volume":    dim3VolumeScore(in.Dim3Volume),
		"capital":   dim4CapitalScore(in.Dim4Capital),
		"sentiment": dim5SentimentScore(in.Dim5Sentiment),
		"external":  dim6ExternalScore(in.Dim6External),
	}

	raw := 0.0
	for _, k := range DimOrder {
		raw += DimWeights[k] * scores[k]
	}

	conflict := CheckDimConflict(scores)
	adjusted := raw
	if conflict >= 2 {
		adjusted = raw * 0.8 // 矛盾显著，折价 20%
	}
	adjusted = clampScore(adjusted)

	positionRate, tag := ScoreToPosition(adjusted)

	return &MarketReport{
		DimScores:          scores,
		RawTotalScore:      round2(raw),
		ConflictCount:      conflict,
		AdjustedTotalScore: round2(adjusted),
		PositionRate:       positionRate,
		MarketTag:          tag,
		Sources:            in.Sources,
	}
}

// ==================== 辅助函数 ====================

func clampScore(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

// clampSigned 将值限制在 [-max, max]（用于以50为中心的连续化增减分项）。
func clampSigned(v, max float64) float64 {
	if v < -max {
		return -max
	}
	if v > max {
		return max
	}
	return v
}

func round2(v float64) float64 {
	return math.Round(v*100) / 100
}
