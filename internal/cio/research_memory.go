package cio

import (
	"context"
	"log"
	"sort"

	"github.com/quantpilot/quantpilot/internal/brain/sixdim"
	brainhost "github.com/quantpilot/quantpilot/internal/brainhost"
	"github.com/quantpilot/quantpilot/internal/data"
)

// ResearchSixDim 单日六维判势（跨日研究记忆的定量骨架）。
type ResearchSixDim struct {
	TradeDate     string             `json:"trade_date"`
	MarketTag     string             `json:"market_tag"`
	AdjustedScore float64            `json:"adjusted_total_score"`
	PositionRate  float64            `json:"position_rate"`
	Dims          map[string]float64 `json:"dim_scores,omitempty"`
}

// ResearchClaim 单日判断主张及其实盘验证结果（对应 ClaimRecord 表）。
type ResearchClaim struct {
	ClaimDate    string  `json:"claim_date"`
	Direction    string  `json:"direction"`     // bullish / bearish / neutral
	Confidence   float64 `json:"confidence"`    // 0~100
	Statement    string  `json:"statement"`     // 主张陈述
	Verified     bool    `json:"verified"`      // 是否已用真实收益验证
	Match        bool    `json:"match"`         // 方向判断是否命中
	ActualReturn float64 `json:"actual_return"` // 验证日实际市场收益(%)
}

// ResearchDay 跨日研究时间轴中的一天：把当日六维判势与当日判断主张(及次日验证结果)关联，
// 供复盘/前端回看"历史预期 → 次日验证"闭环（借鉴 easy-stock 的研究飞轮，本机保留判断历史）。
type ResearchDay struct {
	TradeDate     string             `json:"trade_date"`
	MarketTag     string             `json:"market_tag"`
	AdjustedScore float64            `json:"adjusted_total_score"`
	PositionRate  float64            `json:"position_rate"`
	Dims          map[string]float64 `json:"dim_scores,omitempty"`
	Claim         *ResearchClaim     `json:"claim,omitempty"`
}

func toString(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func toFloat(v interface{}) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	}
	return 0
}

func toDims(v interface{}) map[string]float64 {
	if m, ok := v.(map[string]float64); ok {
		return m
	}
	return nil
}

// mergeResearchTimeline 纯拼接：将六维判势序列与判断主张按日期合并为研究时间轴。
// 输入均须已按日期排序（保持一致；通常为最新在前）。某日无主张时为 nil Claim。
func mergeResearchTimeline(six []ResearchSixDim, claims []ResearchClaim) []ResearchDay {
	claimByDate := make(map[string]*ResearchClaim, len(claims))
	for i := range claims {
		claimByDate[claims[i].ClaimDate] = &claims[i]
	}
	out := make([]ResearchDay, 0, len(six))
	for _, s := range six {
		day := ResearchDay{
			TradeDate:     s.TradeDate,
			MarketTag:     s.MarketTag,
			AdjustedScore: s.AdjustedScore,
			PositionRate:  s.PositionRate,
			Dims:          s.Dims,
		}
		if c, ok := claimByDate[s.TradeDate]; ok {
			day.Claim = c
		}
		out = append(out, day)
	}
	return out
}

// statTimelineSummary 纯函数：对研究时间轴做紧凑统计，供复盘/前端快速概览。
func statTimelineSummary(days []ResearchDay) map[string]interface{} {
	verified, agree := 0, 0
	var hitRate float64
	if len(days) > 0 {
		for _, d := range days {
			if d.Claim != nil && d.Claim.Verified {
				verified++
				if d.Claim.Match {
					agree++
				}
			}
		}
		if verified > 0 {
			hitRate = float64(agree) / float64(verified) * 100
		}
	}
	return map[string]interface{}{
		"days":                   len(days),
		"verified":               verified,
		"agreed":                 agree,
		"direction_hit_rate_pct": hitRate,
	}
}

// GetResearchTimeline 读取最近 N 日的跨日研究记忆时间轴：
// 六维判势（DuckDB market_sixdim_daily） + 当日判断主张及验证结果（SQLite ClaimRecord）。
// 供每日复盘、前端"研究记忆"页回看历史预期→次日验证。
func (c *CIOEngine) GetResearchTimeline(ctx context.Context, days int) ([]ResearchDay, map[string]interface{}, error) {
	if days <= 0 {
		days = 30
	}
	if days > 90 {
		days = 90 // 防御性上限：避免前端误传超大 LIMIT 拖慢/放大查询
	}
	var six []ResearchSixDim
	if c.duckDB != nil {
		rows, err := sixdim.LatestReports(ctx, brainhost.AdaptMarketDataStore(c.duckDB), days)
		if err != nil {
			log.Printf("[CIO] GetResearchTimeline 读六维历史失败: %v", err)
		}
		for _, m := range rows {
			six = append(six, ResearchSixDim{
				TradeDate:     toString(m["trade_date"]),
				MarketTag:     toString(m["market_tag"]),
				AdjustedScore: toFloat(m["adjusted_total_score"]),
				PositionRate:  toFloat(m["position_rate"]),
				Dims:          toDims(m["dim_scores"]),
			})
		}
	}

	var claims []ResearchClaim
	if c.db != nil {
		var recs []data.ClaimRecord
		if err := c.db.GetDB().Order("claim_date DESC").Limit(days).Find(&recs).Error; err != nil {
			log.Printf("[CIO] GetResearchTimeline 读主张失败: %v", err)
		}
		for _, r := range recs {
			claims = append(claims, ResearchClaim{
				ClaimDate:    r.ClaimDate,
				Direction:    r.Direction,
				Confidence:   r.Confidence,
				Statement:    r.Statement,
				Verified:     r.Verified,
				Match:        r.Match,
				ActualReturn: r.ActualReturn,
			})
		}
	}

	// 按日期升序（时间轴阅读顺序：旧→新）
	if len(six) > 0 {
		sort.SliceStable(six, func(i, j int) bool { return six[i].TradeDate < six[j].TradeDate })
	}
	days2 := mergeResearchTimeline(six, claims)
	summary := statTimelineSummary(days2)
	return days2, summary, nil
}
