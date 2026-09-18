package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/quantpilot/quantpilot/internal/data"
	"github.com/quantpilot/quantpilot/internal/marketinfo"
)

// ==================== get_index_technical（技术趋势-多指数） ====================

// IndexTechnicalTool 基于上证/深证/创业板指数真实K线计算技术趋势（MA20/MA60/短期收益）。
type IndexTechnicalTool struct {
	duckDB *data.DuckDBManager
}

func NewIndexTechnicalTool(dm *data.DuckDBManager) *IndexTechnicalTool {
	return &IndexTechnicalTool{duckDB: dm}
}

func (t *IndexTechnicalTool) Name() string { return "get_index_technical" }

func (t *IndexTechnicalTool) Description() string {
	return "获取A股主要指数(上证/深证成指/创业板指)的技术趋势：现价、MA20/MA60、站上与否、5/20日收益与趋势方向(多/空/震荡)。数据来自DuckDB真实日K与实时行情，用于盘前技术面判势。"
}

func (t *IndexTechnicalTool) GetDefinition() ToolDefinition {
	return ToolDefinition{
		Type: "function",
		Function: FunctionDef{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"indices": map[string]interface{}{
						"type":        "array",
						"items":       map[string]interface{}{"type": "string"},
						"description": "要查询的指数代码（可选，默认 sh000001,sz399001,sz399006）",
					},
				},
			},
		},
	}
}

type indexTechResult struct {
	Code      string  `json:"code"`
	Name      string  `json:"name"`
	Current   float64 `json:"current"`
	PrevClose float64 `json:"prev_close"`
	MA20      float64 `json:"ma20"`
	MA60      float64 `json:"ma60"`
	AboveMA20 bool    `json:"above_ma20"`
	AboveMA60 bool    `json:"above_ma60"`
	Ret5D     float64 `json:"ret_5d"`
	Ret20D    float64 `json:"ret_20d"`
	Trend     string  `json:"trend"` // 多/空/震荡
}

func (t *IndexTechnicalTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	if t.duckDB == nil || !t.duckDB.HasStockDB() {
		return nil, fmt.Errorf("DuckDB 不可用，无法获取指数技术指标")
	}
	defs := []struct {
		Code string
		Name string
	}{
		{"sh000001", "上证指数"},
		{"sz399001", "深证成指"},
		{"sz399006", "创业板指"},
	}
	var out []indexTechResult
	for _, d := range defs {
		bars, err := t.duckDB.GetKlineFromStock(ctx, d.Code, 70)
		if err != nil || len(bars) < 20 {
			continue
		}
		closes := make([]float64, len(bars))
		for i, b := range bars {
			closes[i] = b.Close
		}
		latest := closes[0]
		ma20 := meanTool(closes[:20])
		n60 := len(closes)
		if n60 > 60 {
			n60 = 60
		}
		ma60 := meanTool(closes[:n60])
		r5, r20 := 0.0, 0.0
		if len(closes) > 5 && closes[5] > 0 {
			r5 = (latest - closes[5]) / closes[5] * 100
		}
		if len(closes) > 20 && closes[20] > 0 {
			r20 = (latest - closes[20]) / closes[20] * 100
		}
		trend := "震荡"
		if r5 > 0.5 && r20 > 1 {
			trend = "多头"
		} else if r5 < -0.5 && r20 < -1 {
			trend = "空头"
		}
		prev := 0.0
		if len(bars) > 1 {
			prev = bars[1].Close
		}
		out = append(out, indexTechResult{
			Code: d.Code, Name: d.Name, Current: round2(latest), PrevClose: round2(prev),
			MA20: round2(ma20), MA60: round2(ma60), AboveMA20: latest > ma20, AboveMA60: latest > ma60,
			Ret5D: round2(r5), Ret20D: round2(r20), Trend: trend,
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("DuckDB 指数K线不足，无法计算技术指标")
	}
	return map[string]interface{}{
		"indices": out,
		"source":  "DuckDB stock.ohlc 真实日K(MA/收益) + 收盘价",
		"as_of":   time.Now().Format("2006-01-02"),
	}, nil
}

// ==================== get_market_breadth（市场广度） ====================

// MarketBreadthTool 基于DuckDB全市场真实统计计算市场广度。
type MarketBreadthTool struct {
	duckDB *data.DuckDBManager
}

func NewMarketBreadthTool(dm *data.DuckDBManager) *MarketBreadthTool {
	return &MarketBreadthTool{duckDB: dm}
}

func (t *MarketBreadthTool) Name() string { return "get_market_breadth" }

func (t *MarketBreadthTool) Description() string {
	return "获取A股市场广度：全市场涨跌家数与上涨占比、涨停/跌停家数、20日新高/新低、热点板块数。全部来自DuckDB真实行情统计，用于判断市场赚钱广度与赚钱效应。"
}

func (t *MarketBreadthTool) GetDefinition() ToolDefinition {
	return ToolDefinition{
		Type: "function",
		Function: FunctionDef{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters:  map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		},
	}
}

func (t *MarketBreadthTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	if t.duckDB == nil || !t.duckDB.HasStockDB() {
		return nil, fmt.Errorf("DuckDB 不可用，无法获取市场广度")
	}
	res := map[string]interface{}{"source": "DuckDB stock 全市场真实统计"}
	if b, err := t.duckDB.GetMarketBreadth(ctx, "all"); err == nil && b.Total > 0 {
		res["total"] = b.Total
		res["advancers"] = b.Advancers
		res["rise_pct"] = round2(float64(b.Advancers) / float64(b.Total) * 100)
	}
	if ls, err := t.duckDB.GetMarketLimitStats(ctx); err == nil {
		res["limit_up"] = ls.LimitUp
		res["limit_down"] = ls.LimitDown
		res["touched_limit_up"] = ls.TouchedLimitUp
		res["sealed_limit_up"] = ls.SealedLimitUp
		if ls.TouchedLimitUp > 0 {
			res["blow_up_rate"] = round2(float64(ls.BlownUp) / float64(ls.TouchedLimitUp) * 100)
		}
	}
	if sectors, err := t.duckDB.GetSectorChangeStats(ctx); err == nil {
		hot := 0
		for _, s := range sectors {
			if s.AvgChgPct > 0 && s.StockCount >= 3 {
				hot++
			}
		}
		res["hotline_sectors"] = hot
	}
	return res, nil
}

// ==================== get_turnover（量能/成交额/两融） ====================

type TurnoverTool struct{}

func NewTurnoverTool() *TurnoverTool { return &TurnoverTool{} }

func (t *TurnoverTool) Name() string { return "get_turnover" }

func (t *TurnoverTool) Description() string {
	return "获取A股成交量能与杠杆资金：今日两市实时成交额(腾讯)、近期成交额历史趋势(雪球)、融资余额(东方财富)。用于判断市场量能放大/萎缩与资金活跃度。数据真实，来源已标注。"
}

func (t *TurnoverTool) GetDefinition() ToolDefinition {
	return ToolDefinition{
		Type: "function",
		Function: FunctionDef{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"days": map[string]interface{}{"type": "integer", "description": "成交额历史天数(默认15)"},
				},
			},
		},
	}
}

func (t *TurnoverTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	days, _ := args["days"].(int)
	if days <= 0 {
		days = 15
	}
	out := map[string]interface{}{}
	if cur, err := marketinfo.FetchCurrentTurnover(ctx); err == nil {
		out["current_turnover_yi"] = round2(cur)
		out["source_current"] = "tencent(qt.gtimg.cn) 实时"
	} else {
		out["current_turnover_yi"] = "unavailable(" + simpleErr(err) + ")"
	}
	if hist, err := marketinfo.FetchTurnoverHistory(ctx, days); err == nil && len(hist) > 0 {
		total := 0.0
		for _, p := range hist {
			total += p.Amount
		}
		out["avg_turnover_yi"] = round2(total / float64(len(hist)))
		out["latest_turnover_yi"] = round2(hist[len(hist)-1].Amount)
		out["source_history"] = "xueqiu 指数成交额"
	} else {
		out["avg_turnover_yi"] = "unavailable"
	}
	if margin, src, err := marketinfo.FetchMarginHistory(ctx, 5); err == nil {
		out["margin_balance_yi"] = round2(margin[len(margin)-1].Balance)
		out["source_margin"] = src
	} else {
		out["margin_balance_yi"] = "unavailable(" + simpleErr(err) + ")"
	}
	return out, nil
}

// ==================== get_capital_flow（资金结构） ====================

type CapitalFlowTool struct{}

func NewCapitalFlowTool() *CapitalFlowTool { return &CapitalFlowTool{} }

func (t *CapitalFlowTool) Name() string { return "get_capital_flow" }

func (t *CapitalFlowTool) Description() string {
	return "获取A股资金面指标：融资余额及其环比(杠杆资金增减)、两市实时成交额(主力活跃)、北向资金当日实时净流入(同花顺hsgtApi，非交易时段无数据)。用于判断增量/杠杆资金流向。数据真实，来源已标注。"
}

func (t *CapitalFlowTool) GetDefinition() ToolDefinition {
	return ToolDefinition{
		Type: "function",
		Function: FunctionDef{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters:  map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		},
	}
}

func (t *CapitalFlowTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	out := map[string]interface{}{}
	if margin, src, err := marketinfo.FetchMarginHistory(ctx, 8); err == nil && len(margin) >= 2 {
		last, prev := margin[len(margin)-1], margin[len(margin)-2]
		delta := last.Balance - prev.Balance
		out["margin_balance_yi"] = round2(last.Balance)
		out["margin_delta_yi"] = round2(delta)
		out["margin_date"] = last.Date
		out["source_margin"] = src
	} else {
		out["margin_balance_yi"] = "unavailable(" + simpleErr(err) + ")"
	}
	if cur, err := marketinfo.FetchCurrentTurnover(ctx); err == nil {
		out["current_turnover_yi"] = round2(cur)
		out["source_turnover"] = "tencent(qt.gtimg.cn) 实时"
	} else {
		out["current_turnover_yi"] = "unavailable"
	}
	// 北向资金实时净流入（同花顺 hsgtApi，非交易时段/节假日无数据则标注不可用）
	if net, src, err := marketinfo.FetchNorthboundRealtime(ctx); err == nil {
		out["northbound_net_yi"] = round2(net)
		out["source_northbound"] = src
	} else {
		out["northbound_net_yi"] = "unavailable(" + simpleErr(err) + ")"
	}
	return out, nil
}

// ==================== get_limitup_board（涨停板/连板/炸板） ====================

type LimitUpBoardTool struct{}

func NewLimitUpBoardTool() *LimitUpBoardTool { return &LimitUpBoardTool{} }

func (t *LimitUpBoardTool) Name() string { return "get_limitup_board" }

func (t *LimitUpBoardTool) Description() string {
	return "获取A股涨停板行情(东方财富真实数据)：涨停家数、跌停家数、最高连板、连板梯队与封单金额。用于判断市场打板情绪与赚钱效应强度。数据真实，来源已标注。"
}

func (t *LimitUpBoardTool) GetDefinition() ToolDefinition {
	return ToolDefinition{
		Type: "function",
		Function: FunctionDef{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters:  map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		},
	}
}

func (t *LimitUpBoardTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	board, src, err := marketinfo.FetchLimitUpBoard(ctx)
	if err != nil {
		return map[string]interface{}{
			"status":  "unavailable",
			"message": fmt.Sprintf("涨停板行情不可用: %v", simpleErr(err)),
			"source":  src,
		}, nil
	}
	topItems := board.Items
	if len(topItems) > 20 {
		topItems = topItems[:20]
	}
	return map[string]interface{}{
		"status":         "ok",
		"date":           board.Date,
		"limit_up_cnt":   board.LimitUpCnt,
		"limit_down_cnt": board.LimitDownCnt,
		"lianban_top":    board.LianbanTop,
		"zhaban_cnt":     board.ZhabanCnt,
		"top_items":      topItems,
		"source":         src,
	}, nil
}

// ==================== get_external_market（隔夜外围） ====================

type ExternalMarketTool struct{}

func NewExternalMarketTool() *ExternalMarketTool { return &ExternalMarketTool{} }

func (t *ExternalMarketTool) Name() string { return "get_external_market" }

func (t *ExternalMarketTool) Description() string {
	return "获取隔夜外围市场实时行情(腾讯全球代码)：道指/纳斯达克/标普、恒生指数/国企、富时A50期货，含涨跌幅。用于判断隔夜外盘对A股开盘的情绪影响。数据真实，来源已标注。"
}

func (t *ExternalMarketTool) GetDefinition() ToolDefinition {
	return ToolDefinition{
		Type: "function",
		Function: FunctionDef{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters:  map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		},
	}
}

func (t *ExternalMarketTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	pts, src, err := marketinfo.FetchExternalMarkets(ctx)
	if err != nil {
		return map[string]interface{}{
			"status":  "unavailable",
			"message": fmt.Sprintf("外部市场行情不可用: %v", simpleErr(err)),
		}, nil
	}
	return map[string]interface{}{
		"status":  "ok",
		"markets": pts,
		"source":  src,
		"as_of":   time.Now().Format(time.RFC3339),
	}, nil
}

// ==================== get_market_news（财经快讯） ====================

type MarketNewsTool struct{}

func NewMarketNewsTool() *MarketNewsTool { return &MarketNewsTool{} }

func (t *MarketNewsTool) Name() string { return "get_market_news" }

func (t *MarketNewsTool) Description() string {
	return "获取最新A股财经快讯(东方财富)：政策/资金/行业新闻标题与摘要。用于盘前了解可能影响市场的消息面。数据真实，来源已标注。"
}

func (t *MarketNewsTool) GetDefinition() ToolDefinition {
	return ToolDefinition{
		Type: "function",
		Function: FunctionDef{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"size": map[string]interface{}{"type": "integer", "description": "返回条数(默认15)"},
				},
			},
		},
	}
}

func (t *MarketNewsTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	size, _ := args["size"].(int)
	items, src, err := marketinfo.FetchMarketNews(ctx, size)
	if err != nil {
		return map[string]interface{}{"status": "unavailable", "message": fmt.Sprintf("快讯不可用: %v", simpleErr(err)), "source": src}, nil
	}
	return map[string]interface{}{"status": "ok", "news": items, "source": src}, nil
}

// ==================== get_stock_announcement（个股公告） ====================

type StockAnnouncementTool struct{}

func NewStockAnnouncementTool() *StockAnnouncementTool { return &StockAnnouncementTool{} }

func (t *StockAnnouncementTool) Name() string { return "get_stock_announcement" }

func (t *StockAnnouncementTool) Description() string {
	return "获取个股(或全市场)最新公告(东方财富)：停复牌/业绩/重大事项等标题与日期。建仓前排查重大利空使用。数据真实，来源已标注。"
}

func (t *StockAnnouncementTool) GetDefinition() ToolDefinition {
	return ToolDefinition{
		Type: "function",
		Function: FunctionDef{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"stock": map[string]interface{}{"type": "string", "description": "股票代码或名称(可选，缺省返回全市场最新公告)"},
					"size":  map[string]interface{}{"type": "integer", "description": "返回条数(默认10)"},
				},
			},
		},
	}
}

func (t *StockAnnouncementTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	stock, _ := args["stock"].(string)
	size, _ := args["size"].(int)
	items, src, err := marketinfo.FetchAnnouncements(ctx, stock, size)
	if err != nil {
		return map[string]interface{}{"status": "unavailable", "message": fmt.Sprintf("公告不可用: %v", simpleErr(err)), "source": src}, nil
	}
	return map[string]interface{}{"status": "ok", "announcements": items, "source": src}, nil
}

// ==================== get_lhb（龙虎榜） ====================

type LHBTool struct{}

func NewLHBTool() *LHBTool { return &LHBTool{} }

func (t *LHBTool) Name() string { return "get_lhb" }

func (t *LHBTool) Description() string {
	return "获取指定日期龙虎榜明细(东方财富)：上榜个股涨跌幅与买卖净额，反映游资/机构活跃度。数据真实，来源已标注。"
}

func (t *LHBTool) GetDefinition() ToolDefinition {
	return ToolDefinition{
		Type: "function",
		Function: FunctionDef{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"date": map[string]interface{}{"type": "string", "description": "日期 YYYY-MM-DD(可选，默认最近交易日)"},
					"size": map[string]interface{}{"type": "integer", "description": "返回条数(默认25)"},
				},
			},
		},
	}
}

func (t *LHBTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	date, _ := args["date"].(string)
	size, _ := args["size"].(int)
	items, src, err := marketinfo.FetchLHB(ctx, date, size)
	if err != nil {
		return map[string]interface{}{"status": "unavailable", "message": fmt.Sprintf("龙虎榜不可用: %v", simpleErr(err)), "source": src}, nil
	}
	return map[string]interface{}{"status": "ok", "lhb": items, "source": src, "date": date}, nil
}

// ==================== get_fundamental（基本面） ====================

type FundamentalTool struct {
	duckDB *data.DuckDBManager
}

func NewFundamentalTool(dm *data.DuckDBManager) *FundamentalTool {
	return &FundamentalTool{duckDB: dm}
}

func (t *FundamentalTool) Name() string { return "get_fundamental" }

func (t *FundamentalTool) Description() string {
	return "查询个股最近已披露的财务数据(DuckDB财务报告)：营收/净利润及同比、ROE、毛利率、资产负债率、EPS等。按披露日对齐无未来函数。数据真实。"
}

func (t *FundamentalTool) GetDefinition() ToolDefinition {
	return ToolDefinition{
		Type: "function",
		Function: FunctionDef{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"symbol": map[string]interface{}{"type": "string", "description": "股票代码(纯数字或带市场前缀，如600519)"},
				},
				"required": []string{"symbol"},
			},
		},
	}
}

func (t *FundamentalTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	if t.duckDB == nil || !t.duckDB.HasStockDB() {
		return nil, fmt.Errorf("DuckDB 不可用，无法查询财务数据")
	}
	symbol, _ := args["symbol"].(string)
	if symbol == "" {
		return nil, fmt.Errorf("缺少 symbol 参数")
	}
	rep, err := t.duckDB.GetFinancialAsOf(symbol, time.Now().Format("2006-01-02"))
	if err != nil || rep == nil {
		return map[string]interface{}{
			"status":  "unavailable",
			"symbol":  symbol,
			"message": fmt.Sprintf("该股票暂无已披露财务数据: %v", simpleErr(err)),
		}, nil
	}
	return map[string]interface{}{
		"status":        "ok",
		"symbol":        symbol,
		"report_date":   rep.ReportDate,
		"ann_date":      rep.AnnDate,
		"total_revenue": rep.TotalRevenue,
		"revenue_yoy":   rep.RevenueYOY,
		"net_profit":    rep.NetProfit,
		"profit_yoy":    rep.ProfitYOY,
		"roe":           rep.Roe,
		"gross_margin":  rep.GrossMargin,
		"net_margin":    rep.NetMargin,
		"debt_ratio":    rep.DebtRatio,
		"eps":           rep.EPS,
		"bps":           rep.BPS,
		"source":        "DuckDB stock.financial_report(按披露日对齐)",
	}, nil
}

// ==================== check_market_rules（交易规则知识） ====================

type MarketRulesCheckTool struct{}

func NewMarketRulesCheckTool() *MarketRulesCheckTool { return &MarketRulesCheckTool{} }

func (t *MarketRulesCheckTool) Name() string { return "check_market_rules" }

func (t *MarketRulesCheckTool) Description() string {
	return "查询A股交易与制度规则知识(内置真实规则，非行情)：涨跌停限制、T+1、交易时段、ST制度、整手交易、手续费(佣金/印花税)等。用于智能体判断订单合法性，避免臆造规则。"
}

func (t *MarketRulesCheckTool) GetDefinition() ToolDefinition {
	return ToolDefinition{
		Type: "function",
		Function: FunctionDef{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"topic": map[string]interface{}{"type": "string", "description": "可选规则主题：涨跌停/T+1/交易时段/ST/手续费/整手/交易单位 之一，缺省返回全量"},
				},
			},
		},
	}
}

func (t *MarketRulesCheckTool) Execute(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	topic, _ := args["topic"].(string)
	rules := map[string]string{
		"涨跌停":  "主板±10%，创业板/科创板(30/68/300开头)±20%，北交所±30%；ST主板±5%。首日新股不设涨跌幅(注册制除外)。",
		"T+1":  "A股实行T+1：当日买入股票当日不可卖出，T+1日方可卖出；当日卖出所得资金当日可用于买入，但T+1日方可转出。",
		"交易时段": "集合竞价9:15-9:25(9:20-9:25不可撤单)，连续竞价9:30-11:30/13:00-15:00，收盘集合竞价14:57-15:00(深市)。",
		"ST":   "ST/ST*涨跌停±5%，ST*为退市风险警示；单日买入量与风险提示另有要求。",
		"手续费":  "买入仅收佣金(默认万三、最低5元)；卖出收佣金+印花税(现行0.05%，2023-08起减半)。过户费万分之零点一。",
		"整手":   "买卖数量为100股(1手)整数倍，卖出不足1手可一次性申报；科创板可按1股递增，但买入仍以200股起(超200按1股递增)。",
		"交易单位": "一手=100股；限价申报数量盘中不可撤；涨跌停价以外申报无效。",
	}
	target := rules
	if topic != "" {
		if v, ok := rules[topic]; ok {
			target = map[string]string{topic: v}
		} else {
			return map[string]interface{}{"status": "error", "message": fmt.Sprintf("未知规则主题: %s，可选：%s", topic, strings.Join(sortedKeys(rules), "/"))}, nil
		}
	}
	return map[string]interface{}{"status": "ok", "rules": target, "source": "内置A股交易规则(交易所公开制度)"}, nil
}

func sortedKeys(m map[string]string) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// mean 保留工具包内均值辅助
func meanTool(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	var s float64
	for _, x := range v {
		s += x
	}
	return s / float64(len(v))
}

// simpleErr 截断错误信息为短文本，用于工具返回值（避免把完整错误堆栈塞进结果）。
func simpleErr(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if len(msg) > 80 {
		return msg[:80] + "…"
	}
	return msg
}
