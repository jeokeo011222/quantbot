package screener

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/quantpilot/quantpilot/internal/data"
)

// FactorID 因子ID
type FactorID string

const (
	FactorValue             FactorID = "value"
	FactorQuality           FactorID = "quality"
	FactorMomentum          FactorID = "momentum"
	FactorLowVolatility     FactorID = "low_volatility"
	FactorEarningsStability FactorID = "earnings_stability"
	FactorLiquidity         FactorID = "liquidity"
	FactorOrderBook         FactorID = "order_book"           // 盘口因子：量比/内外盘/封板状态（order_book_daily 数据，数据不足自动降级）
	FactorCointegration     FactorID = "cointegration_spread" // 协整/配对因子：个股相对市场(沪深300)滚动态残差 z-score，低超卖则高吸引（需市场基准，缺失自动降级）
)

// FactorScore 单只股票的因子得分
type FactorScore struct {
	FactorID    FactorID           `json:"factorId"`
	FactorName  string             `json:"factorName"`
	Score       float64            `json:"score"`       // 0-100
	Contributor string             `json:"contributor"` // 该因子的主要贡献者
	Breakdown   map[string]float64 `json:"breakdown"`   // 子因子得分
}

// StockScore 单只股票的综合评分
type StockScore struct {
	Code         string                   `json:"code"`
	Name         string                   `json:"name"`
	Market       string                   `json:"market"`
	Price        float64                  `json:"price"`
	ChangePct    float64                  `json:"changePct"`
	TotalScore   float64                  `json:"totalScore"` // 0-100
	FactorScores map[FactorID]FactorScore `json:"factorScores"`
	Ranking      int                      `json:"ranking"`
	Reasons      []string                 `json:"reasons"`  // 选股理由（正面）
	Warnings     []string                 `json:"warnings"` // 风险提示
	// Pro版扩展字段
	Momentum1M   float64 `json:"momentum1m"`   // 1月收益率
	Momentum3M   float64 `json:"momentum3m"`   // 3月收益率
	Momentum6M   float64 `json:"momentum6m"`   // 6月收益率
	Momentum12M  float64 `json:"momentum12m"`  // 12月收益率
	Volatility   float64 `json:"volatility"`   // 历史波动率
	Amplitude    float64 `json:"amplitude"`    // 平均振幅
	TurnoverRate float64 `json:"turnoverRate"` // 换手率
	Liquidity    float64 `json:"liquidity"`    // 流动性指标
	// AI评分拆解（对标 PanWatch）：1-10 AI评分 + 利好/风险因子正负拆解
	AIScore       int           `json:"aiScore"`
	FactorExplain FactorExplain `json:"factorExplain"`
}

// factorScoreOrder 因子在前端的展示顺序（与 ui/src/pages/StockScreener.tsx 的 FACTOR_ORDER 保持一致）
var factorScoreOrder = []FactorID{
	FactorValue,
	FactorQuality,
	FactorMomentum,
	FactorLowVolatility,
	FactorEarningsStability,
	FactorLiquidity,
	FactorOrderBook,
	FactorCointegration,
}

// MarshalJSON 自定义序列化：将内部 map[FactorID]FactorScore 输出为有序数组，
// 使前端 stock.factorScores 按数组渲染（前端类型声明为 FactorScore[]）。
// 若直接序列化 Go map，将得到 JSON 对象 {key: {...}}，前端 .map() 会运行时报错。
func (s StockScore) MarshalJSON() ([]byte, error) {
	// 剥离方法避免递归；保持与 StockScore 完全一致的字段（含 FactorScores map）
	type stockScoreWithoutMethod StockScore

	type stockScoreJSON struct {
		*stockScoreWithoutMethod
		FactorScores []FactorScore `json:"factorScores"` // 外层显式字段覆盖内嵌的 map 字段
	}

	aux := stockScoreJSON{
		stockScoreWithoutMethod: (*stockScoreWithoutMethod)(&s),
		FactorScores:            sortedFactorScores(s.FactorScores),
	}
	return json.Marshal(aux)
}

// sortedFactorScores 将因子得分 map 按固定展示顺序转为有序切片
func sortedFactorScores(m map[FactorID]FactorScore) []FactorScore {
	orderIdx := make(map[FactorID]int, len(factorScoreOrder))
	for i, fid := range factorScoreOrder {
		orderIdx[fid] = i
	}
	fs := make([]FactorScore, 0, len(m))
	for _, v := range m {
		fs = append(fs, v)
	}
	sort.Slice(fs, func(i, j int) bool {
		oi, ok1 := orderIdx[fs[i].FactorID]
		oj, ok2 := orderIdx[fs[j].FactorID]
		switch {
		case ok1 && ok2:
			return oi < oj
		case ok1:
			return true
		case ok2:
			return false
		default:
			return string(fs[i].FactorID) < string(fs[j].FactorID)
		}
	})
	return fs
}

// StrategyTemplate 策略模板
type StrategyTemplate struct {
	ID          string               `json:"id"`
	Name        string               `json:"name"`
	Description string               `json:"description"`
	Icon        string               `json:"icon"`
	Weights     map[FactorID]float64 `json:"weights"`
	RiskLevel   string               `json:"riskLevel"`
}

// ScreeningRequest 选股请求
type ScreeningRequest struct {
	StrategyID      string                `json:"strategyId"`                // 策略模板ID
	CustomWeights   map[FactorID]float64  `json:"customWeights,omitempty"`   // 自定义权重
	Market          string                `json:"market"`                    // 市场：all/sh/sz
	MaxResults      int                   `json:"maxResults"`                // 返回数量
	MinScore        float64               `json:"minScore"`                  // 最低分数门槛
	SmallCapital    bool                  `json:"smallCapital"`              // 中小资金(如<50万)时收缩选股宇宙：剔除 ST/北交所/创业板/科创板，缩小到~2000+只以提速并降低风险
	InvestorProfile *data.InvestorProfile `json:"investorProfile,omitempty"` // 用户画像（智能因子组合）
	// DiversifySeed 去同质化种子（>0 时启用）：基于 uid 哈希做确定性权重微扰，
	// 让不同用户命中略不同的选股子集（用户个性化种子 + 多策略池分组），消除"千人一面"。
	DiversifySeed int64 `json:"diversifySeed,omitempty"`
}

// ScreeningResponse 选股结果
type ScreeningResponse struct {
	StrategyName    string                    `json:"strategyName"`
	TotalCount      int                       `json:"totalCount"`
	Results         []StockScore              `json:"results"`
	GeneratedAt     string                    `json:"generatedAt"`
	Formula         string                    `json:"formula"`         // 策略模板公式展示
	DynamicFormula  string                    `json:"dynamicFormula"`  // 实际使用的动态公式
	Tier            string                    `json:"tier"`            // 当前版本
	Upgrades        []string                  `json:"upgrades"`        // 可升级的Pro功能
	DynamicWeights  map[FactorID]float64      `json:"dynamicWeights"`  // 智能动态权重
	TemplateWeights map[FactorID]float64      `json:"templateWeights"` // 策略模板原始权重
	FactorHealth    map[FactorID]FactorHealth `json:"factorHealth"`    // 因子健康度
	MarketState     *MarketState              `json:"marketState"`     // 市场状态
	ProfileSummary  string                    `json:"profileSummary"`  // 用户画像摘要
	UsedSmartEngine bool                      `json:"usedSmartEngine"` // 是否使用了智能引擎
}

// StrategyPreset 预设策略
type StrategyPreset struct {
	Balanced  StrategyTemplate
	Defensive StrategyTemplate
	Growth    StrategyTemplate
}

// DefaultStrategies 默认3个策略模板
func DefaultStrategies() StrategyPreset {
	return StrategyPreset{
		Balanced: StrategyTemplate{
			ID:          "balanced",
			Name:        "均衡配置",
			Description: "适合长期投资、追求稳定增长的投资者，分散配置价值、质量、动量等多个因子",
			Icon:        "balanced",
			RiskLevel:   "中等",
			Weights: map[FactorID]float64{
				FactorValue:             0.20,
				FactorQuality:           0.20,
				FactorMomentum:          0.20,
				FactorLowVolatility:     0.15,
				FactorEarningsStability: 0.15,
				FactorLiquidity:         0.10,
				FactorOrderBook:         0.05, // 盘口因子（量比/内外盘/封板），数据不足自动降级不参与
				FactorCointegration:     0.04, // 协整/配对因子（相对市场超卖吸引），无市场基准时自动降级不参与
			},
		},
		Defensive: StrategyTemplate{
			ID:          "defensive",
			Name:        "红利价值多头",
			Description: "适合风险承受能力低但仍投资股票的投资者（股票私募·防御型多头）：聚焦高股息、低波动、高质量的价值蓝筹股，不配置债券",
			Icon:        "defensive",
			RiskLevel:   "低",
			Weights: map[FactorID]float64{
				FactorValue:             0.15,
				FactorQuality:           0.30,
				FactorMomentum:          0.10,
				FactorLowVolatility:     0.25,
				FactorEarningsStability: 0.20,
				FactorLiquidity:         0.00,
			},
		},
		Growth: StrategyTemplate{
			ID:          "growth",
			Name:        "成长型",
			Description: "适合追求高收益的投资者，重点关注动量、成长、质量因子，接受较高波动",
			Icon:        "growth",
			RiskLevel:   "高",
			Weights: map[FactorID]float64{
				FactorValue:             0.10,
				FactorQuality:           0.25,
				FactorMomentum:          0.30,
				FactorLowVolatility:     0.10,
				FactorEarningsStability: 0.15,
				FactorLiquidity:         0.10,
			},
		},
	}
}

// GetStrategyTemplate 根据ID获取策略模板
func GetStrategyTemplate(id string) *StrategyTemplate {
	presets := DefaultStrategies()
	switch id {
	case "balanced":
		return &presets.Balanced
	case "defensive":
		return &presets.Defensive
	case "growth":
		return &presets.Growth
	default:
		return &presets.Balanced
	}
}

// AllStrategyTemplates 返回所有策略模板
func AllStrategyTemplates() []StrategyTemplate {
	presets := DefaultStrategies()
	return []StrategyTemplate{presets.Balanced, presets.Defensive, presets.Growth}
}

// ResolveTemplateID 将风险等级/风格/策略类型归类到统一的策略模板ID（选股引擎与投资规划共用，单一出处）。
// 接受三类输入（统一归类，避免两套策略模板割裂）：
//  1. 投资规划风险等级：conservative/balanced/growth/aggressive/income/capital_preservation；
//  2. 投资规划策略类型 strategyType：dividend_value/capital_preservation/value_factor/quality_factor/growth_factor/index_tracking/multi_factor；
//  3. 选股因子画像：balanced/defensive/growth。
//
// 返回 screener 模板 ID：defensive | balanced | growth。
func ResolveTemplateID(key string) string {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "defensive", "conservative", "income", "capital_preservation",
		"dividend_value", "value_factor", "quality_factor": // 价值/质分/红利/保本 → 防御
		return "defensive"
	case "growth", "aggressive",
		"growth_factor": // 成长 → 成长
		return "growth"
	case "balanced", "multi_factor", "index_tracking": // 均衡/多因子/指数跟踪 → 均衡
		return "balanced"
	default:
		return "balanced"
	}
}
