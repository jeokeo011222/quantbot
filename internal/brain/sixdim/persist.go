// 六维判势结果持久化：market_sixdim_daily 表（DuckDB，日期为主键）。
// 对应《六维策略.md》DuckDB存储表设计：每日判势结果表，存储 output_market_report 全部字段。
package sixdim

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/quantpilot/quantpilot/internal/brain/port"
)

// SaveReport 将当日六维判势结果写入 market_sixdim_daily（按 trade_date 覆盖/插入）
func SaveReport(ctx context.Context, mkt port.MarketDataStore, tradeDate string, report *MarketReport) error {
	if mkt == nil {
		return fmt.Errorf("DuckDB 不可用，无法保存六维判势结果")
	}
	dimJSON, _ := json.Marshal(report.DimScores)
	srcJSON, _ := json.Marshal(report.Sources)
	return mkt.SaveMarketSixDimRow(ctx, tradeDate, string(dimJSON), string(srcJSON),
		report.RawTotalScore, report.AdjustedTotalScore, report.ConflictCount,
		report.PositionRate, report.MarketTag)
}

// LatestReports 读取最近 N 日六维判势历史记录（供前端/复盘展示）
func LatestReports(ctx context.Context, mkt port.MarketDataStore, limit int) ([]map[string]interface{}, error) {
	if mkt == nil {
		return nil, fmt.Errorf("DuckDB 不可用")
	}
	rows, err := mkt.GetMarketSixDimRows(ctx, limit)
	if err != nil {
		return nil, err
	}
	var result []map[string]interface{}
	for _, r := range rows {
		var dims map[string]float64
		json.Unmarshal([]byte(r.DimScoresJSON), &dims)
		var sources map[string]string
		json.Unmarshal([]byte(r.SourcesJSON), &sources)
		result = append(result, map[string]interface{}{
			"trade_date":           r.TradeDate,
			"dim_scores":           dims,
			"raw_total_score":      r.RawTotalScore,
			"conflict_count":       r.ConflictCount,
			"adjusted_total_score": r.AdjustedTotalScore,
			"position_rate":        r.PositionRate,
			"market_tag":           r.MarketTag,
			"sources":              sources,
			"created_at":           r.CreatedAt.Format("2006-01-02 15:04:05"),
		})
	}
	return result, nil
}

// TradeDateOf 返回六维判势落库使用的交易日（当前时间格式化为 YYYY-MM-DD）
func TradeDateOf(t time.Time) string {
	return t.Format("2006-01-02")
}
