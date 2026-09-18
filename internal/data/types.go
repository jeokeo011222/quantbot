package data

// FactorResult 因子计算结果
type FactorResult struct {
	Code         string  `json:"code"`
	Sector       string  `json:"sector"`
	FactorName   string  `json:"factor_name"`
	Category     string  `json:"category"`
	Score        float64 `json:"score"`
	Percentile   float64 `json:"percentile"`
	ZScore       float64 `json:"z_score"`
	Rank         int     `json:"rank"`
	TotalSamples int     `json:"total_samples"`
	Description  string  `json:"description,omitempty"`
}
