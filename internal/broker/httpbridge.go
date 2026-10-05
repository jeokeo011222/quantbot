package broker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ==================== 桥接网关统一协议结构体 ====================
// Ptrade 与 QMT 客户端内策略桥复用同一 HTTP 协议（/status /positions /asset /order /cancel /fills）。

// ptradeRequest HTTP 请求体（POST /order /cancel）。
type ptradeRequest struct {
	Action    string  `json:"action"`               // buy / sell / cancel
	Symbol    string  `json:"symbol,omitempty"`     // 600519.SH
	Volume    int     `json:"volume,omitempty"`     // 股数
	Price     float64 `json:"price,omitempty"`      // 0=市价(对手价) >0=限价
	PriceType int     `json:"price_type,omitempty"` // 可选，覆盖默认价格类型
	OrderID   string  `json:"order_id,omitempty"`   // cancel 时使用
}

// ptradeResponse 桥接网关统一响应。
type ptradeResponse struct {
	OK        bool            `json:"ok"`
	Message   string          `json:"message,omitempty"`
	OrderID   string          `json:"order_id,omitempty"`
	Account   string          `json:"account,omitempty"`
	Connected bool            `json:"connected,omitempty"`
	Positions []ptradePosRaw  `json:"positions,omitempty"`
	Asset     *ptradeAssetRaw `json:"asset,omitempty"`
	NextSeq   int64           `json:"next_seq,omitempty"`
	Fills     []ptradeFillRaw `json:"fills,omitempty"`
}

// ptradePosRaw 桥端持仓原始结构。
type ptradePosRaw struct {
	Symbol    string  `json:"symbol"`
	Quantity  int     `json:"quantity"`
	Available int     `json:"available"`
	CostPrice float64 `json:"cost_price"`
}

// ptradeAssetRaw 桥端资金原始结构。
type ptradeAssetRaw struct {
	Cash        float64 `json:"cash"`
	MarketValue float64 `json:"market_value"`
	TotalAssets float64 `json:"total_assets"`
	Frozen      float64 `json:"frozen"`
	Available   float64 `json:"available"`
}

// ptradeFillRaw 桥端成交回报原始结构。
type ptradeFillRaw struct {
	Seq      int64   `json:"seq"`
	OrderID  string  `json:"order_id"`
	Symbol   string  `json:"symbol"`
	Side     string  `json:"side"` // BUY / SELL
	Volume   int     `json:"volume"`
	Price    float64 `json:"price"`
	TradedAt string  `json:"traded_at"`
}

// httpBridgeConfig 客户端内策略桥共享配置。
type httpBridgeConfig struct {
	mode     Mode          // 桥模式（ModePtrade / ModeLive）
	name     string        // 券商名（状态/错误文案，如 Ptrade / QMT）
	hint     string        // 未连接时的提示语（如「请确认 Ptrade 客户端已运行桥接策略」）
	host     string        // 网关监听地址（默认 127.0.0.1）
	httpPort int           // 网关 HTTP 端口
	account  string        // 券商资金账号
	timeout  time.Duration // 单次 HTTP 请求超时
}

// httpBridge 客户端内策略桥共享核心：Go 宿主 ↔ 客户端内嵌 Python 策略脚本的本地 HTTP 网关。
// Ptrade 与 QMT 复用同一协议；无稳定成交推送，Go 侧启动轮询 goroutine 增量拉取成交回报并分发回调。
type httpBridge struct {
	cfg httpBridgeConfig

	baseURL string
	httpc   *http.Client

	connectedMu sync.RWMutex
	connected   bool
	account     string

	tradeCb   func(Fill)
	tradeCbMu sync.Mutex
	eventCb   func(EventMessage)
	eventCbMu sync.Mutex

	lastAsset   Asset
	lastAssetMu sync.RWMutex

	lastPositions   []Position
	lastPositionsMu sync.RWMutex

	pollCancel context.CancelFunc
	pollDone   chan struct{}
	closeOnce  sync.Once

	seenSeqMu sync.Mutex
	seenSeq   int64 // 已消费的成交 seq（去重）
}

// newHTTPBridge 创建共享 HTTP 桥核心（不发起连接，需调用 Connect()）。
func newHTTPBridge(cfg httpBridgeConfig) *httpBridge {
	host := cfg.host
	if host == "" {
		host = "127.0.0.1"
	}
	port := cfg.httpPort
	if port <= 0 {
		port = 8891
		if cfg.mode == ModeLive {
			port = 8892 // QMT 默认桥接端口
		}
	}
	timeout := cfg.timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	return &httpBridge{
		cfg:     cfg,
		baseURL: fmt.Sprintf("http://%s:%d", host, port),
		httpc:   &http.Client{Timeout: timeout},
	}
}

// SetTradeCallback 注册成交回报回调。
func (b *httpBridge) SetTradeCallback(cb func(Fill)) {
	b.tradeCbMu.Lock()
	b.tradeCb = cb
	b.tradeCbMu.Unlock()
}

// SetEventCallback 注册任意异步事件回调。
func (b *httpBridge) SetEventCallback(cb func(EventMessage)) {
	b.eventCbMu.Lock()
	b.eventCb = cb
	b.eventCbMu.Unlock()
}

func (b *httpBridge) fireTrade(orderID, symbol string, side Side, vol int, price float64) {
	b.tradeCbMu.Lock()
	cb := b.tradeCb
	b.tradeCbMu.Unlock()
	if cb != nil {
		cb(Fill{OrderID: orderID, Symbol: symbol, Side: side, Quantity: vol, Price: price, TradedAt: time.Now(), ExternalID: orderID})
	}
}

func (b *httpBridge) fireEvent(e EventMessage) {
	b.eventCbMu.Lock()
	cb := b.eventCb
	b.eventCbMu.Unlock()
	if cb != nil {
		cb(e)
	}
}

// Connect 探测桥接网关可达性并标记已连接，随后启动成交回报轮询。
func (b *httpBridge) Connect() error {
	b.connectedMu.Lock()
	defer b.connectedMu.Unlock()
	if b.connected {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := b.doGet(ctx, "/status")
	if err != nil {
		return fmt.Errorf("%s 桥接网关不可达（%s）：%w，请确认 %s", b.cfg.name, b.baseURL, err, b.cfg.hint)
	}
	if !resp.OK {
		return fmt.Errorf("%s 桥接网关状态异常：%s", b.cfg.name, resp.Message)
	}
	b.connected = resp.Connected
	b.account = b.cfg.account
	if !b.connected {
		return fmt.Errorf("%s 桥接网关未就绪：%s", b.cfg.name, resp.Message)
	}
	b.startPoll()
	return nil
}

// IsLive 仅当网关可达且已连接时为真。
func (b *httpBridge) IsLive() bool {
	b.connectedMu.RLock()
	defer b.connectedMu.RUnlock()
	return b.connected
}

// Mode 返回桥模式。
func (b *httpBridge) Mode() Mode { return b.cfg.mode }

// SubmitOrder 实盘下单：符号转换后 POST 到桥接网关，返回券商委托号。
func (b *httpBridge) SubmitOrder(o Order) (string, error) {
	if !b.IsLive() {
		return "", fmt.Errorf("%s 桥未连接", b.cfg.name)
	}
	sym, err := ToQmtSymbol(o.Symbol) // Ptrade 与 QMT 同为 600519.SH 格式
	if err != nil {
		return "", err
	}
	action := "sell"
	if o.Side == SideBuy {
		action = "buy"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := b.doPost(ctx, "/order", ptradeRequest{
		Action:    action,
		Symbol:    sym,
		Volume:    o.Quantity,
		Price:     o.Price,
		PriceType: 0, // 桥端根据 price 自动选价格类型
	})
	if err != nil {
		return "", fmt.Errorf("%s 下单失败: %w", b.cfg.name, err)
	}
	if !resp.OK {
		return "", fmt.Errorf("%s 拒单: %s", b.cfg.name, resp.Message)
	}
	return resp.OrderID, nil
}

// CancelOrder 撤销委托。
func (b *httpBridge) CancelOrder(orderID string) error {
	if !b.IsLive() {
		return fmt.Errorf("%s 桥未连接", b.cfg.name)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := b.doPost(ctx, "/cancel", ptradeRequest{Action: "cancel", OrderID: orderID})
	if err != nil {
		return fmt.Errorf("%s 撤单失败: %w", b.cfg.name, err)
	}
	if !resp.OK {
		return fmt.Errorf("%s 撤单失败: %s", b.cfg.name, resp.Message)
	}
	return nil
}

// QueryPositions 查询持仓。
func (b *httpBridge) QueryPositions() ([]Position, error) {
	if !b.IsLive() {
		return nil, fmt.Errorf("%s 桥未连接", b.cfg.name)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := b.doGet(ctx, "/positions")
	if err != nil {
		return nil, err
	}
	if !resp.OK {
		return nil, fmt.Errorf("查询持仓失败: %s", resp.Message)
	}
	positions := make([]Position, 0, len(resp.Positions))
	for _, p := range resp.Positions {
		positions = append(positions, Position{
			Symbol:    p.Symbol,
			Quantity:  p.Quantity,
			Available: p.Available,
			CostPrice: p.CostPrice,
		})
	}
	b.lastPositionsMu.Lock()
	b.lastPositions = positions
	b.lastPositionsMu.Unlock()
	return positions, nil
}

// QueryAsset 查询资金/资产。
func (b *httpBridge) QueryAsset() (Asset, error) {
	if !b.IsLive() {
		return Asset{}, fmt.Errorf("%s 桥未连接", b.cfg.name)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := b.doGet(ctx, "/asset")
	if err != nil {
		return Asset{}, err
	}
	if !resp.OK {
		return Asset{}, fmt.Errorf("查询资产失败: %s", resp.Message)
	}
	var a Asset
	if resp.Asset != nil {
		a = Asset{
			Cash:        resp.Asset.Cash,
			MarketValue: resp.Asset.MarketValue,
			TotalAssets: resp.Asset.TotalAssets,
			Frozen:      resp.Asset.Frozen,
			Available:   resp.Asset.Available,
		}
	}
	b.lastAssetMu.Lock()
	b.lastAsset = a
	b.lastAssetMu.Unlock()
	return a, nil
}

// Status 返回连接状态（含最近一次资金快照）。
func (b *httpBridge) Status() Status {
	b.connectedMu.RLock()
	connected := b.connected
	b.connectedMu.RUnlock()
	st := Status{Mode: b.cfg.mode, Connected: connected, Account: b.cfg.account}
	b.lastAssetMu.RLock()
	a := b.lastAsset
	b.lastAssetMu.RUnlock()
	st.Cash = a.Cash
	st.MarketVal = a.MarketValue
	if connected {
		st.Message = fmt.Sprintf("%s 已连接（实盘）", b.cfg.name)
	} else {
		st.Message = fmt.Sprintf("%s 未连接：请确认 %s", b.cfg.name, b.cfg.hint)
	}
	return st
}

// Close 停止成交回报轮询并标记断开（幂等）。
func (b *httpBridge) Close() error {
	b.closeOnce.Do(func() {
		if b.pollCancel != nil {
			b.pollCancel()
		}
		if b.pollDone != nil {
			<-b.pollDone
		}
		b.connectedMu.Lock()
		b.connected = false
		b.connectedMu.Unlock()
	})
	return nil
}

// startPoll 启动成交回报轮询 goroutine（2s 间隔，增量拉取 /fills 并按 seq 去重）。
func (b *httpBridge) startPoll() {
	ctx, cancel := context.WithCancel(context.Background())
	b.pollCancel = cancel
	b.pollDone = make(chan struct{})
	go func() {
		defer close(b.pollDone)
		defer func() {
			if r := recover(); r != nil {
				// 轮询 goroutine 兜底，避免单次异常导致整个进程退出
			}
		}()
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				b.pollFills(ctx)
			}
		}
	}()
}

// pollFills 单次增量拉取成交回报并分发。
func (b *httpBridge) pollFills(ctx context.Context) {
	b.seenSeqMu.Lock()
	after := b.seenSeq
	b.seenSeqMu.Unlock()
	resp, err := b.doGet(ctx, fmt.Sprintf("/fills?after=%d", after))
	if err != nil || !resp.OK {
		return
	}
	if len(resp.Fills) == 0 {
		return
	}
	b.seenSeqMu.Lock()
	b.seenSeq = resp.NextSeq
	b.seenSeqMu.Unlock()
	for _, f := range resp.Fills {
		side := Side(strings.ToUpper(f.Side))
		b.fireTrade(f.OrderID, f.Symbol, side, f.Volume, f.Price)
		b.fireEvent(EventMessage{Kind: "trade", Symbol: f.Symbol, Side: side, Volume: f.Volume, Price: f.Price, External: f.OrderID})
	}
}

// doGet 发起 GET 请求并解析统一响应。
func (b *httpBridge) doGet(ctx context.Context, path string) (*ptradeResponse, error) {
	return b.doJSON(ctx, http.MethodGet, path, nil)
}

// doPost 发起 POST 请求并解析统一响应。
func (b *httpBridge) doPost(ctx context.Context, path string, body interface{}) (*ptradeResponse, error) {
	return b.doJSON(ctx, http.MethodPost, path, body)
}

// doJSON 通用 HTTP JSON 请求。
func (b *httpBridge) doJSON(ctx context.Context, method, path string, body interface{}) (*ptradeResponse, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, b.baseURL+path, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := b.httpc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out ptradeResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("解析桥接网关响应失败: %w", err)
	}
	return &out, nil
}
