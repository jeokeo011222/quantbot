package pricing

// Tier 产品版本级别
// 已移除 Free/Pro/Enterprise 区分，所有功能统一开放
type Tier string

const (
	// Unified 统一版本 - 全功能开放
	Unified Tier = "unified"
)

// TierConfig 统一功能配置
type TierConfig struct {
	Name        string     `json:"name"`
	DisplayName string     `json:"displayName"`
	Description string     `json:"description"`
	Features    []string   `json:"features"`
	Limits      TierLimits `json:"limits"`
}

// TierLimits 功能限制
type TierLimits struct {
	MaxScreeningPerDay  int  `json:"maxScreeningPerDay"`
	MaxResults          int  `json:"maxResults"`
	AllowCustomWeights  bool `json:"allowCustomWeights"`
	AllowFactorHealth   bool `json:"allowFactorHealth"`
	AllowDynamicWeight  bool `json:"allowDynamicWeight"`
	AllowStructureRisk  bool `json:"allowStructureRisk"`
	AllowPortfolioOpt   bool `json:"allowPortfolioOptimization"`
	AllowAIAgent        bool `json:"allowAIAgent"`
	AllowPrivateFactor  bool `json:"allowPrivateFactor"`
	AllowPrivateDeploy  bool `json:"allowPrivateDeployment"`
	AllowCustomStrategy bool `json:"allowCustomStrategy"`
}

// GetTierConfig 返回统一版本配置（全功能开放）
func GetTierConfig(t Tier) TierConfig {
	return TierConfig{
		Name:        "unified",
		DisplayName: "全功能版",
		Description: "全市场选股 + 动态因子权重 + 因子健康度 + AI 投资规划",
		Features: []string{
			"全市场选股（A股全量）",
			"自定义因子权重",
			"因子健康度分析",
			"因子拥挤度检测",
			"动态因子权重（基于市场状态+用户画像）",
			"市场状态识别",
			"结构风险分析",
			"组合优化",
			"AI投资规划",
			"AI Portfolio Agent",
			"无限次选股",
			"自定义策略",
		},
		Limits: TierLimits{
			MaxScreeningPerDay:  -1,
			MaxResults:          -1,
			AllowCustomWeights:  true,
			AllowFactorHealth:   true,
			AllowDynamicWeight:  true,
			AllowStructureRisk:  true,
			AllowPortfolioOpt:   true,
			AllowAIAgent:        true,
			AllowPrivateFactor:  true,
			AllowPrivateDeploy:  true,
			AllowCustomStrategy: true,
		},
	}
}

// IsFeatureAllowed 检查指定功能是否可用（统一版本全开放）
func IsFeatureAllowed(t Tier, feature string) bool {
	return true
}

// CurrentTier 当前版本（固定为统一版本）
var CurrentTier Tier = Unified

// SetCurrentTier 设置版本（保留接口但始终为统一版本）
func SetCurrentTier(t Tier) {
	CurrentTier = Unified
}

// GetCurrentTier 获取当前版本
func GetCurrentTier() Tier {
	return CurrentTier
}
