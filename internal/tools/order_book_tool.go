package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/quantpilot/quantpilot/internal/data"
	"github.com/quantpilot/quantpilot/internal/orderbook"
)

// ==================== OrderBookTool ====================
// market_order_book 个股盘口状态工具
// 基于实时行情数据源的五档买卖挂单、内外盘、量比，判定每只标的的盘口状态
// （涨停封死/涨停打开/跌停封死/跌停打开/正常），供 CIO/Planner/Risk 在做
// 建仓/减仓决策时感知个股盘口（如百花医药式「巨量开盘跌停」）。
// 数据全部来自真实行情，缺失字段按 0/无法判定处理，严禁伪造。

// OrderBookTool 个股盘口状态工具
type OrderBookTool struct{}

// NewOrderBookTool 创建个股盘口状态工具
func NewOrderBookTool() *OrderBookTool {
	return &OrderBookTool{}
}

func (t *OrderBookTool) Name() string { return "market_order_book" }

func (t *OrderBookTool) Description() string {
	return "查询个股实时盘口状态：五档买卖挂单(价量)、外盘/内盘、换手率、量比、涨停/跌停价，" +
		"并判定盘口状态(涨停封死买不进/涨停打开/跌停封死卖不出/跌停打开/正常)与巨量跌停信号。" +
		"建仓前用可避免在跌停板/封板上接飞刀；减仓前用可判断跌停封死卖不出需保留持仓。数据来自真实行情，无伪造。"
}

func (t *OrderBookTool) GetDefinition() ToolDefinition {
	return ToolDefinition{
		Type: "function",
		Function: FunctionDef{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"codes": map[string]interface{}{
						"type":        "array",
						"items":       map[string]interface{}{"type": "string"},
						"description": "股票代码列表，可带市场前缀(如 sh600519)或纯6位数字(如 600519)",
					},
				},
				"required": []string{"codes"},
			},
		},
	}
}

func (t *OrderBookTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	codes, err := extractCodes(args)
	if err != nil {
		return nil, err
	}
	if len(codes) == 0 {
		return nil, fmt.Errorf("codes list is required")
	}
	if len(codes) > 20 {
		codes = codes[:20]
	}

	snapshots, ok := data.FetchRealtimeStockSnapshots(codes)
	if !ok || len(snapshots) == 0 {
		return map[string]interface{}{
			"codes":  codes,
			"items":  []interface{}{},
			"note":   "实时行情获取失败，无盘口数据（数据真实、无伪造）",
			"source": "realtime",
		}, nil
	}

	items := make([]interface{}, 0, len(snapshots))
	for _, s := range snapshots {
		ob := orderbook.ClassifySnapshot(s)
		items = append(items, map[string]interface{}{
			"code":           s.Code,
			"name":           s.Name,
			"price":          s.CurrentPrice,
			"prev_close":     s.PrevClose,
			"change_pct":     round2(s.ChangePercent),
			"limit_up":       ob.LimitUp,
			"limit_down":     ob.LimitDown,
			"order_book_state": ob.Chinese,
			"state":          string(ob.State),
			"reason":         ob.Reason,
			"at_limit_up":    ob.AtLimitUp,
			"at_limit_down":  ob.AtLimitDown,
			"seal_vol":       ob.SealVol,
			"huge_volume_down": ob.HugeVolumeDown,
			"in_out_ratio":   ob.InOutRatio,
			"volume_ratio":   ob.VolumeRatio,
			"turnover_rate":  s.TurnoverRate,
			"bid_prices":     s.BidPrices,
			"ask_prices":     s.AskPrices,
			"bid_volumes":    s.BidVolumes,
			"ask_volumes":    s.AskVolumes,
			"out_volume":     s.OutVolume,
			"in_volume":      s.InVolume,
		})
	}

	return map[string]interface{}{
		"codes":  codes,
		"items":  items,
		"source": "realtime",
		"note":   "五档买卖挂单/内外盘/量比来自实时行情，封板状态判定规则可追溯；缺失字段为0，无伪造",
	}, nil
}

// extractCodes 从工具参数中提取股票代码列表（支持 []string 数组或逗号分隔字符串）
func extractCodes(args map[string]interface{}) ([]string, error) {
	raw, ok := args["codes"]
	if !ok {
		return nil, fmt.Errorf("codes is required")
	}
	var codes []string
	switch v := raw.(type) {
	case []interface{}:
		for _, c := range v {
			if s, ok := c.(string); ok {
				s = strings.TrimSpace(s)
				if s != "" {
					codes = append(codes, s)
				}
			}
		}
	case []string:
		codes = append(codes, v...)
	case string:
		for _, c := range strings.Split(v, ",") {
			c = strings.TrimSpace(c)
			if c != "" {
				codes = append(codes, c)
			}
		}
	default:
		return nil, fmt.Errorf("codes must be a list of strings")
	}
	return codes, nil
}
