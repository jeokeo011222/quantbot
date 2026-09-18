// Package thssdk 同花顺官方金融数据服务（hithink-finance / fuyao.aicubes.cn）Go 客户端。
//
// 提供 A 股特色数据（涨停/跌停/炸板池、连板天梯、热股榜、龙虎榜）、财务报表与财务指标、
// 估值快照、集合竞价快照、全市场数据导出（Parquet）等官方数据能力。
//
// 设计原则：
//   - 统一 X-api-key 认证；业务错误（code!=0）返回 *APIError；
//   - 包级默认客户端（Configure 注入，供六维判势/工具共用），也可 NewClient 独立使用；
//   - 外部调用统一 TTL 缓存（按数据类型不同有效期）+ 最小请求间隔限流，避免撞官方额度；
//   - 所有数据均为官方真实数据，严禁伪造；nullable 数值用 *float64，null 表示未披露。
package thssdk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// DefaultBaseURL 官方服务地址。
const DefaultBaseURL = "https://fuyao.aicubes.cn"

// APIError 官方接口业务错误（code != 0）。
type APIError struct {
	Code      int    `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
}

func (e *APIError) Error() string {
	return fmt.Sprintf("thssdk api error code=%d: %s (request_id=%s)", e.Code, e.Message, e.RequestID)
}

// apiResp 统一响应包装。
type apiResp struct {
	Code      int             `json:"code"`
	Message   string          `json:"message"`
	Data      json.RawMessage `json:"data"`
	RequestID string          `json:"request_id"`
}

// Client 同花顺官方数据服务客户端。
type Client struct {
	baseURL  string
	apiKey   string
	http     *http.Client
	throttle *throttler
}

// NewClient 创建客户端；baseURL 为空时使用官方默认地址。
func NewClient(apiKey, baseURL string) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{
		baseURL:  strings.TrimRight(baseURL, "/"),
		apiKey:   apiKey,
		http:     &http.Client{Timeout: 15 * time.Second},
		throttle: newThrottler(),
	}
}

// get 发起 GET 请求并解码到 out（业务层 data 已解包）。
func (c *Client) get(ctx context.Context, path string, query url.Values, out interface{}) error {
	if c == nil || c.apiKey == "" {
		return fmt.Errorf("thssdk 未配置（缺少 API Key）")
	}
	// 官方付费服务：HTTP 429 / 5xx 偶发限流或服务波动，做有限指数退避重试
	const maxAttempts = 4
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(1<<(attempt-1)) * 500 * time.Millisecond):
			}
		}
		lastErr = c.getOnce(ctx, path, query, out)
		if lastErr == nil {
			return nil
		}
		var ae *APIError
		// 仅对真正的临时性波动做退避重试：HTTP 限流(429)、标准服务端错误(500~599)、
		// 以及业务码 5003（Data source unavailable，官方数据源临时不可用，属瞬时波动，会自行恢复）。
		// 其余业务错误码（如 1002 Unknown thscode）代表数据不存在等确定性结果，直接返回不做无谓重试。
		if errors.As(lastErr, &ae) &&
			(ae.Code == http.StatusTooManyRequests ||
				(ae.Code >= http.StatusInternalServerError && ae.Code < 600) ||
				ae.Code == 5003) {
			// 429 / 5xx / 5003：退避后重试
			lastErr = fmt.Errorf("thssdk %d 重试%d次后仍失败: %w", ae.Code, maxAttempts, ae)
			continue
		}
		return lastErr
	}
	return lastErr
}

func (c *Client) getOnce(ctx context.Context, path string, query url.Values, out interface{}) error {
	if c == nil || c.apiKey == "" {
		return fmt.Errorf("thssdk 未配置（缺少 API Key）")
	}
	if err := c.throttle.acquire(ctx); err != nil {
		return err
	}
	defer c.throttle.release()

	u := c.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-api-key", c.apiKey)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("thssdk 请求失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		// 非 2xx（含 429/5xx）统一转为 *APIError{Code: statusCode}，供上层降频重试
		return &APIError{Code: resp.StatusCode, Message: strings.TrimSpace(string(b)), RequestID: ""}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return fmt.Errorf("thssdk 读取响应失败: %w", err)
	}
	var wr apiResp
	if err := json.Unmarshal(body, &wr); err != nil {
		return fmt.Errorf("thssdk 响应解析失败: %w", err)
	}
	if wr.Code != 0 {
		return &APIError{Code: wr.Code, Message: wr.Message, RequestID: wr.RequestID}
	}
	if out != nil && len(wr.Data) > 0 {
		if err := json.Unmarshal(wr.Data, out); err != nil {
			return fmt.Errorf("thssdk data 解析失败(%s): %w", path, err)
		}
	}
	return nil
}

// ==================== 限流（官方付费服务，并发 + 全局 QPS 令牌桶） ====================
// 原实现为全局串行最小间隔（300ms/请求，约 3.3 req/s），全市场财务同步（5000 只 × 3 表）耗时过长。
// 现改为「有限并发 + 总速率令牌桶」：既提升吞吐（约 4 倍），又通过全局 QPS 上限避免瞬时过猛触发 429，
// 配合 get 中的 429/5xx 指数退避重试，保证不撞官方额度。

const (
	maxInflight = 5  // 并发请求上限：控制瞬时并发数
	maxRPS      = 15 // 全局请求速率上限（req/s）
)

type throttler struct {
	inflight chan struct{} // 并发槽位
	mu       sync.Mutex
	tokens   float64 // 令牌桶当前令牌数
	last     time.Time
}

func newThrottler() *throttler {
	return &throttler{
		inflight: make(chan struct{}, maxInflight),
		tokens:   float64(maxRPS),
		last:     time.Now(),
	}
}

// acquire 占用一个并发槽位，并按全局 QPS 令牌桶限流；ctx 取消时返回错误（槽位自动释放）。
func (t *throttler) acquire(ctx context.Context) error {
	select {
	case t.inflight <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := t.waitToken(ctx); err != nil {
		<-t.inflight
		return err
	}
	return nil
}

// release 释放并发槽位。
func (t *throttler) release() { <-t.inflight }

// waitToken 令牌桶：速率 maxRPS/s、容量 maxRPS，令牌不足则阻塞等待（可被 ctx 取消）。
func (t *throttler) waitToken(ctx context.Context) error {
	for {
		t.mu.Lock()
		now := time.Now()
		elapsed := now.Sub(t.last).Seconds()
		t.last = now
		t.tokens += elapsed * float64(maxRPS)
		if t.tokens > float64(maxRPS) {
			t.tokens = float64(maxRPS)
		}
		if t.tokens >= 1 {
			t.tokens--
			t.mu.Unlock()
			return nil
		}
		need := (1 - t.tokens) / float64(maxRPS) // 距下一个令牌的等待秒数
		t.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(need*1000)*time.Millisecond + time.Millisecond):
		}
	}
}

// ==================== 代码规范转换 ====================

// ToTHSCode 规范代码(sh600519/sz000001/bj830799) → 官方 thscode(600519.SH/000001.SZ/830799.BJ)。
func ToTHSCode(symbol string) string {
	pure := PureCode(symbol)
	switch {
	case strings.HasPrefix(symbol, "sz"):
		return pure + ".SZ"
	case strings.HasPrefix(symbol, "bj"):
		return pure + ".BJ"
	default:
		return pure + ".SH"
	}
}

// FromTHSCode 官方 thscode(600519.SH) → 规范代码(sh600519)。
func FromTHSCode(thscode string) string {
	thscode = strings.ToUpper(strings.TrimSpace(thscode))
	if !strings.Contains(thscode, ".") {
		return strings.ToLower(thscode)
	}
	parts := strings.SplitN(thscode, ".", 2)
	switch parts[1] {
	case "SZ":
		return "sz" + parts[0]
	case "BJ":
		return "bj" + parts[0]
	default:
		return "sh" + parts[0]
	}
}

// PureCode 提取 6 位纯数字代码。
func PureCode(symbol string) string {
	var b strings.Builder
	for _, r := range symbol {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ==================== 特色数据：涨停/跌停/炸板池 ====================

// BoardItem 涨停/跌停池条目（涨停池含连板/封单/涨停原因等）。
type BoardItem struct {
	THSCode          string  `json:"thscode"`
	Ticker           string  `json:"ticker"`
	Name             string  `json:"name"`
	IsST             bool    `json:"is_st"`
	IsNew            bool    `json:"is_new"`
	LastPrice        float64 `json:"last_price"`
	PriceChangePct   float64 `json:"price_change_ratio_pct"`
	LimitUpTime      string  `json:"limit_up_time"`
	LimitUpReason    string  `json:"limit_up_reason"`
	ContinueDayText  string  `json:"continue_day_text"`
	ContinueDayCnt   int     `json:"continue_day_cnt"`
	SealMoney        float64 `json:"seal_money"`
	MaxSealMoney     float64 `json:"max_seal_money"`
	FirstLimitTime   string  `json:"first_limit_time"`
	LastLimitTime    string  `json:"last_limit_time"`
	TurnoverRatioPct float64 `json:"turnover_ratio_pct"`
	OpenTimes        int     `json:"open_times"`
	Turnover         float64 `json:"turnover"`
}

// BoardPage 涨停/跌停/炸板池分页响应。
type BoardPage struct {
	Timestamp  int64       `json:"timestamp"`
	Pagination Pagination  `json:"pagination"`
	Item       []BoardItem `json:"item"`
}

// Pagination 分页信息。
type Pagination struct {
	Total int `json:"total"`
	Pages int `json:"pages"`
	Size  int `json:"size"`
	Page  int `json:"page"`
}

// LimitUpPool 涨停股票池（size 1-200）。
func (c *Client) LimitUpPool(ctx context.Context, size int) (*BoardPage, error) {
	if size <= 0 || size > 200 {
		size = 200
	}
	q := url.Values{}
	q.Set("size", fmt.Sprintf("%d", size))
	var out BoardPage
	if err := c.get(ctx, "/api/a-share/special-data/limit-up-pool", q, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// LimitDownPool 跌停股票池。
func (c *Client) LimitDownPool(ctx context.Context, size int) (*BoardPage, error) {
	if size <= 0 || size > 200 {
		size = 200
	}
	q := url.Values{}
	q.Set("size", fmt.Sprintf("%d", size))
	var out BoardPage
	if err := c.get(ctx, "/api/a-share/special-data/limit-down-pool", q, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// LimitBreakPool 涨停炸板股票池（曾触板已开板）。
func (c *Client) LimitBreakPool(ctx context.Context, size int) (*BoardPage, error) {
	if size <= 0 || size > 200 {
		size = 200
	}
	q := url.Values{}
	q.Set("size", fmt.Sprintf("%d", size))
	var out BoardPage
	if err := c.get(ctx, "/api/a-share/special-data/limit-break-pool", q, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ==================== 特色数据：连板天梯 ====================

// LadderStock 连板天梯中单只股票。
type LadderStock struct {
	THSCode     string  `json:"thscode"`
	Ticker      string  `json:"ticker"`
	Name        string  `json:"name"`
	BoardNum    int     `json:"board_num"`
	SignLevel   string  `json:"sign_level"`
	SealNextday *string `json:"seal_nextday"`
}

// LadderBoards 各梯队。
type LadderBoards struct {
	TwoBoard   []LadderStock `json:"two_board"`
	ThreeBoard []LadderStock `json:"three_board"`
	FourBoard  []LadderStock `json:"four_board"`
	FiveBoard  []LadderStock `json:"five_board"`
	SixBoard   []LadderStock `json:"six_board"`
	SevenOver  []LadderStock `json:"seven_over"`
}

// LadderDay 单日连板矩阵。
type LadderDay struct {
	Date   string       `json:"date"`
	Boards LadderBoards `json:"boards"`
}

// LadderResp 连板天梯响应。
type LadderResp struct {
	Timestamp int64        `json:"timestamp"`
	Window    LadderWindow `json:"window"`
	Item      []LadderDay  `json:"item"`
}

// LadderWindow 窗口信息。
type LadderWindow struct {
	Length    int      `json:"length"`
	DateList  []string `json:"date_list"`
	BoardCaps []int    `json:"board_caps"`
}

// MaxBoardHeight 连板天梯最近交易日最高连板高度（0 表示无连板）。
func (r *LadderResp) MaxBoardHeight() int {
	if r == nil || len(r.Item) == 0 {
		return 0
	}
	last := r.Item[len(r.Item)-1]
	return maxBoardHeight(last.Boards)
}

func maxBoardHeight(b LadderBoards) int {
	for h := 7; h >= 2; h-- {
		switch h {
		case 7:
			if len(b.SevenOver) > 0 {
				return 7
			}
		case 6:
			if len(b.SixBoard) > 0 {
				return 6
			}
		case 5:
			if len(b.FiveBoard) > 0 {
				return 5
			}
		case 4:
			if len(b.FourBoard) > 0 {
				return 4
			}
		case 3:
			if len(b.ThreeBoard) > 0 {
				return 3
			}
		case 2:
			if len(b.TwoBoard) > 0 {
				return 2
			}
		}
	}
	return 0
}

// LimitUpLadder 近 30 交易日连板梯队矩阵。
func (c *Client) LimitUpLadder(ctx context.Context) (*LadderResp, error) {
	var out LadderResp
	if err := c.get(ctx, "/api/a-share/special-data/limit-up-ladder", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ==================== 特色数据：热股榜 ====================

// HotItem 热股榜/飙升榜条目。
type HotItem struct {
	THSCode    string  `json:"thscode"`
	Ticker     string  `json:"ticker"`
	Name       string  `json:"name"`
	Rank       int     `json:"rank"`
	Heat       float64 `json:"heat"`
	RankChange int     `json:"rank_change"`
	RankTrend  string  `json:"rank_trend"`
}

// HotStockList 热股榜（period=day 24小时榜 / hour 实时榜）。
func (c *Client) HotStockList(ctx context.Context, period string) ([]HotItem, error) {
	if period == "" {
		period = "day"
	}
	q := url.Values{}
	q.Set("period", period)
	var out struct {
		Timestamp int64     `json:"timestamp"`
		Item      []HotItem `json:"item"`
	}
	if err := c.get(ctx, "/api/a-share/special-data/hot-stock-list", q, &out); err != nil {
		return nil, err
	}
	return out.Item, nil
}

// ==================== 特色数据：龙虎榜 ====================

// DragonTigerStock 龙虎榜个股明细。
type DragonTigerStock struct {
	THSCode        string   `json:"thscode"`
	Ticker         string   `json:"ticker"`
	Name           string   `json:"name"`
	ConceptList    []string `json:"concept_list"`
	Change         float64  `json:"change"`
	BuyValue       float64  `json:"buy_value"`
	SellValue      float64  `json:"sell_value"`
	NetValue       float64  `json:"net_value"`
	NetRate        float64  `json:"net_rate"`
	OrgNetValue    float64  `json:"org_net_value"`
	HotMoneyNetVal float64  `json:"hot_money_net_value"`
	HotRank        int      `json:"hot_rank"`
	RangeDays      int      `json:"range_days"`
	LimitReason    string   `json:"limit_reason"`
}

// HotMoneyItem 游资明细。
type HotMoneyItem struct {
	Name   string             `json:"name"`
	Buying float64            `json:"buying"`
	Rows   []DragonTigerStock `json:"rows"`
}

// DragonTigerResp 龙虎榜响应。
type DragonTigerResp struct {
	Timestamp     int64              `json:"timestamp"`
	BoardType     string             `json:"board_type"`
	TradeDate     string             `json:"trade_date"`
	Count         int                `json:"count"`
	StockCount    int                `json:"stock_count"`
	StockItems    []DragonTigerStock `json:"stock_items"`
	HotMoneyItems []HotMoneyItem     `json:"hot_money_items"`
}

// DragonTigerList 龙虎榜（boardType=all/org/hot_money）。
func (c *Client) DragonTigerList(ctx context.Context, boardType string) (*DragonTigerResp, error) {
	if boardType == "" {
		boardType = "all"
	}
	q := url.Values{}
	q.Set("board_type", boardType)
	var out DragonTigerResp
	if err := c.get(ctx, "/api/a-share/special-data/dragon-tiger-list", q, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ==================== 财务报表与指标 ====================

// IncomeStatement 利润表单期。
type IncomeStatement struct {
	THSCode               string   `json:"thscode"`
	PeriodEndMs           int64    `json:"period_end_ms"`
	ReportDateMs          int64    `json:"report_date_ms"`
	FiscalYear            int      `json:"fiscal_year"`
	FiscalPeriod          string   `json:"fiscal_period"`
	BasicEPS              *float64 `json:"basic_eps"`
	OperatingIncome       *float64 `json:"operating_income"`
	OperatingCosts        *float64 `json:"operating_costs"`
	OperatingExpenses     *float64 `json:"operating_expenses"`
	OperatingProfit       *float64 `json:"operating_profit"`
	ProfitTotal           *float64 `json:"profit_total"`
	NetProfit             *float64 `json:"net_profit"`
	ParentHolderNetProfit *float64 `json:"parent_holder_net_profit"`
	IncomeTaxExpense      *float64 `json:"income_tax_expense"`
	InterestExpenses      *float64 `json:"interest_expenses"`
	ManageFee             *float64 `json:"manage_fee"`
	SalesFee              *float64 `json:"sales_fee"`
	RDExpenses            *float64 `json:"research_and_development_expenses"`
}

// BalanceSheet 资产负债表单期。
type BalanceSheet struct {
	THSCode             string   `json:"thscode"`
	PeriodEndMs         int64    `json:"period_end_ms"`
	ReportDateMs        int64    `json:"report_date_ms"`
	FiscalYear          int      `json:"fiscal_year"`
	FiscalPeriod        string   `json:"fiscal_period"`
	TotalCurrentAssets  *float64 `json:"total_current_assets"`
	NonCurrentNetsTotal *float64 `json:"non_current_nets_total"`
	AssetsTotal         *float64 `json:"assets_total"`
	TotalDebt           *float64 `json:"total_debt"`
	HolderEquityTotal   *float64 `json:"holder_equity_total"`
	Cash                *float64 `json:"cash"`
	AccountsReceivable  *float64 `json:"accounts_receivable"`
}

// CashFlowStatement 现金流量表单期。
type CashFlowStatement struct {
	THSCode                     string   `json:"thscode"`
	PeriodEndMs                 int64    `json:"period_end_ms"`
	ReportDateMs                int64    `json:"report_date_ms"`
	FiscalYear                  int      `json:"fiscal_year"`
	FiscalPeriod                string   `json:"fiscal_period"`
	ActCashFlowNet              *float64 `json:"act_cash_flow_net"`
	InvestCashFlowNet           *float64 `json:"invest_cash_flow_net"`
	FinancingCashFlowNet        *float64 `json:"financing_cash_flow_net"`
	CashEquivalentsNetAddition  *float64 `json:"cash_equivalents_net_addition"`
	PayDividendsProfitsInterest *float64 `json:"pay_dividends_profits_interest_cash"`
	PayFixedAssetsEtcCash       *float64 `json:"pay_fixed_assets_etc_cash"`
}

// IncomeStatements 利润表多期序列（period=annual/quarterly，limit 1-20）。
// 官方返回 data 为对象 { timestamp, item[] }，此处取 item 数组。
func (c *Client) IncomeStatements(ctx context.Context, thscode, period string, limit int) ([]IncomeStatement, error) {
	var d struct {
		Item []IncomeStatement `json:"item"`
	}
	if err := c.statementItems(ctx, "/api/a-share/financials/income-statements", thscode, period, limit, &d); err != nil {
		return nil, err
	}
	return d.Item, nil
}

// BalanceSheets 资产负债表多期序列。
func (c *Client) BalanceSheets(ctx context.Context, thscode, period string, limit int) ([]BalanceSheet, error) {
	var d struct {
		Item []BalanceSheet `json:"item"`
	}
	if err := c.statementItems(ctx, "/api/a-share/financials/balance-sheets", thscode, period, limit, &d); err != nil {
		return nil, err
	}
	return d.Item, nil
}

// CashFlowStatements 现金流量表多期序列。
func (c *Client) CashFlowStatements(ctx context.Context, thscode, period string, limit int) ([]CashFlowStatement, error) {
	var d struct {
		Item []CashFlowStatement `json:"item"`
	}
	if err := c.statementItems(ctx, "/api/a-share/financials/cash-flow-statements", thscode, period, limit, &d); err != nil {
		return nil, err
	}
	return d.Item, nil
}

// statementItems 通用报表序列拉取。
func (c *Client) statementItems(ctx context.Context, path, thscode, period string, limit int, out interface{}) error {
	if limit <= 0 || limit > 20 {
		limit = 8
	}
	if period == "" {
		period = "quarterly"
	}
	q := url.Values{}
	q.Set("thscode", thscode)
	q.Set("period", period)
	q.Set("limit", fmt.Sprintf("%d", limit))
	return c.get(ctx, path, q, out)
}

// AuctionBenchmarkItem 短线风向标竞价基准行。
type AuctionBenchmarkItem struct {
	THSCode    string   `json:"thscode"`
	Ticker     string   `json:"ticker"`
	Name       string   `json:"name"`
	AuctionPct *float64 `json:"auction_pct"`
	Tags       []string `json:"tags"`
}

// AuctionBenchmark 短线风向标竞价基准。
func (c *Client) AuctionBenchmark(ctx context.Context, date string) ([]AuctionBenchmarkItem, error) {
	q := url.Values{}
	if date != "" {
		q.Set("date", date)
	}
	var out struct {
		Timestamp int64                  `json:"timestamp"`
		Date      string                 `json:"date"`
		DateMs    int64                  `json:"date_ms"`
		Item      []AuctionBenchmarkItem `json:"item"`
	}
	if err := c.get(ctx, "/api/a-share/auction/short-term-benchmark", q, &out); err != nil {
		return nil, err
	}
	return out.Item, nil
}

// ==================== 交易日历 ====================

// TradingDay 一个交易日（A 股）。
type TradingDay struct {
	DateMs int64  `json:"date_ms"` // 交易日，Asia/Shanghai 00:00:00 毫秒时间戳
	Date   string `json:"date"`    // 可读日期，yyyyMMdd（如 20250701）
}

// TradingCalendarTradingDays 获取近一年 A 股交易日序列（无入参，按日期升序）。
// 窗口固定为 [今日-1年, 今日]（Asia/Shanghai）；非交易日不在列表中属正常行为。
// data = {timestamp, item[]}。
func (c *Client) TradingCalendarTradingDays(ctx context.Context) ([]TradingDay, error) {
	var out struct {
		Timestamp int64        `json:"timestamp"`
		Item      []TradingDay `json:"item"`
	}
	if err := c.get(ctx, "/api/a-share/calendar/trading-days", nil, &out); err != nil {
		return nil, err
	}
	return out.Item, nil
}

// ==================== 全市场数据导出（Parquet 预签名 URL） ====================

// DumpType 数据导出类型。
type DumpType string

const (
	DumpDailyK     DumpType = "daily-k"
	DumpDailyK10d  DumpType = "daily-k-10d"
	DumpAdjFactors DumpType = "adjustment-factors"
)

// DumpDownloadURL 获取全市场 Parquet 预签名下载地址（有效期约 5 分钟，须立即下载）。
func (c *Client) DumpDownloadURL(ctx context.Context, dump DumpType) (string, error) {
	var out struct {
		PresignedURL       string `json:"presigned_url"`
		PresignedURLEndsAt string `json:"presigned_url_expires_at"`
	}
	if err := c.get(ctx, "/api/dump/market-dumps/"+string(dump)+"/download-url", nil, &out); err != nil {
		return "", err
	}
	if out.PresignedURL == "" {
		return "", fmt.Errorf("thssdk 导出地址为空")
	}
	return out.PresignedURL, nil
}

// Download 下载文件（预签名 URL）到本地路径。
func (c *Client) Download(ctx context.Context, url, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("thssdk 下载失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("thssdk 下载 HTTP %d", resp.StatusCode)
	}
	return writeFileAtomic(dest, resp.Body)
}
