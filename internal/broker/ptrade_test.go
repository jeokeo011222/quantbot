package broker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// mockPtradeServer 模拟 Ptrade 桥接网关：固定持仓/资金，下单生成成交（seq 递增）。
type mockPtradeServer struct {
	t       *testing.T
	seq     int64
	orderID int64
	fillsMu sync.Mutex
	fills   []ptradeFillRaw
	// 记录最近一次下单请求体
	lastOrderMu sync.Mutex
	lastOrder   ptradeRequest
	lastCancel  string
	statusOK    bool // 是否返回 connected=true
}

func newMockPtrade(t *testing.T) *mockPtradeServer {
	return &mockPtradeServer{t: t, statusOK: true}
}

func (m *mockPtradeServer) addFill(symbol, side string, vol int, price float64) string {
	m.fillsMu.Lock()
	defer m.fillsMu.Unlock()
	m.seq++
	oid := fmt.Sprintf("OID-%d", m.seq)
	m.fills = append(m.fills, ptradeFillRaw{
		Seq: m.seq, OrderID: oid, Symbol: symbol, Side: side, Volume: vol, Price: price,
		TradedAt: time.Now().Format("2006-01-02 15:04:05"),
	})
	return oid
}

func (m *mockPtradeServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, ptradeResponse{OK: true, Connected: m.statusOK, Account: "MOCK", Message: "ok"})
	})
	mux.HandleFunc("/positions", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, ptradeResponse{OK: true, Positions: []ptradePosRaw{
			{Symbol: "600519.SH", Quantity: 100, Available: 100, CostPrice: 1500.0},
		}})
	})
	mux.HandleFunc("/asset", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, ptradeResponse{OK: true, Asset: &ptradeAssetRaw{
			Cash: 100000, MarketValue: 150000, TotalAssets: 250000, Frozen: 0, Available: 100000,
		}})
	})
	mux.HandleFunc("/fills", func(w http.ResponseWriter, r *http.Request) {
		after := 0
		if q := r.URL.Query().Get("after"); q != "" {
			fmt.Sscanf(q, "%d", &after)
		}
		m.fillsMu.Lock()
		var fresh []ptradeFillRaw
		next := int64(after)
		for _, f := range m.fills {
			if f.Seq > int64(after) {
				fresh = append(fresh, f)
				next = f.Seq
			}
		}
		m.fillsMu.Unlock()
		writeJSON(w, ptradeResponse{OK: true, NextSeq: next, Fills: fresh})
	})
	mux.HandleFunc("/order", func(w http.ResponseWriter, r *http.Request) {
		var req ptradeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			m.t.Errorf("decode /order body: %v", err)
			writeJSON(w, ptradeResponse{OK: false, Message: "bad body"})
			return
		}
		m.lastOrderMu.Lock()
		m.lastOrder = req
		m.lastOrderMu.Unlock()
		oid := m.addFill(req.Symbol, strings.ToUpper(req.Action), req.Volume, req.Price)
		writeJSON(w, ptradeResponse{OK: true, OrderID: oid})
	})
	mux.HandleFunc("/cancel", func(w http.ResponseWriter, r *http.Request) {
		var req ptradeRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		m.lastOrderMu.Lock()
		m.lastCancel = req.OrderID
		m.lastOrderMu.Unlock()
		writeJSON(w, ptradeResponse{OK: true})
	})
	return mux
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func newTestPtradeBroker(t *testing.T) (*PtradeBroker, *mockPtradeServer) {
	srv := newMockPtrade(t)
	ts := httptest.NewServer(srv.handler())
	t.Cleanup(ts.Close)
	b := NewPtradeBroker(PtradeConfig{HTTPPort: 8891, Account: "MOCK-ACC"})
	b.baseURL = ts.URL // 白盒：重定向到测试服务
	return b, srv
}

func TestPtradeConnectAndStatus(t *testing.T) {
	b, _ := newTestPtradeBroker(t)
	if b.Mode() != ModePtrade {
		t.Fatalf("Mode = %q, want ptrade", b.Mode())
	}
	if b.IsLive() {
		t.Fatal("IsLive should be false before Connect")
	}
	if err := b.Connect(); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	if !b.IsLive() {
		t.Fatal("IsLive should be true after Connect")
	}
	st := b.Status()
	if !st.Connected || st.Account != "MOCK-ACC" {
		t.Fatalf("Status unexpected: %+v", st)
	}
	// 二次 Connect 幂等
	if err := b.Connect(); err != nil {
		t.Fatalf("re-Connect failed: %v", err)
	}
}

func TestPtradeConnectRefused(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, ptradeResponse{OK: true, Connected: false, Message: "not ready"})
	}))
	t.Cleanup(ts.Close)
	b := NewPtradeBroker(PtradeConfig{})
	b.baseURL = ts.URL
	if err := b.Connect(); err == nil {
		t.Fatal("Connect should fail when gateway reports not connected")
	}
	if b.IsLive() {
		t.Fatal("IsLive should be false after failed Connect")
	}
}

func TestPtradeSubmitOrder(t *testing.T) {
	b, srv := newTestPtradeBroker(t)
	if err := b.Connect(); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	oid, err := b.SubmitOrder(Order{
		Symbol: "sh600519", Side: SideBuy, Quantity: 200, Price: 1500.0, Reason: "test",
	})
	if err != nil {
		t.Fatalf("SubmitOrder failed: %v", err)
	}
	if oid == "" {
		t.Fatal("expected non-empty order id")
	}
	srv.lastOrderMu.Lock()
	req := srv.lastOrder
	srv.lastOrderMu.Unlock()
	if req.Action != "buy" || req.Symbol != "600519.SH" || req.Volume != 200 || req.Price != 1500.0 {
		t.Fatalf("unexpected order request: %+v", req)
	}
	// 未连接时拒单
	b.Close()
	if _, err := b.SubmitOrder(Order{Symbol: "sh600519", Side: SideBuy, Quantity: 100, Price: 1}); err == nil {
		t.Fatal("SubmitOrder should fail after Close")
	}
}

func TestPtradeQuery(t *testing.T) {
	b, _ := newTestPtradeBroker(t)
	if err := b.Connect(); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	pos, err := b.QueryPositions()
	if err != nil {
		t.Fatalf("QueryPositions failed: %v", err)
	}
	if len(pos) != 1 || pos[0].Symbol != "600519.SH" || pos[0].Quantity != 100 || pos[0].Available != 100 {
		t.Fatalf("positions unexpected: %+v", pos)
	}
	a, err := b.QueryAsset()
	if err != nil {
		t.Fatalf("QueryAsset failed: %v", err)
	}
	if a.Cash != 100000 || a.MarketValue != 150000 || a.TotalAssets != 250000 || a.Available != 100000 {
		t.Fatalf("asset unexpected: %+v", a)
	}
	st := b.Status()
	if st.Cash != 100000 || st.MarketVal != 150000 {
		t.Fatalf("status asset unexpected: %+v", st)
	}
}

func TestPtradeCancelOrder(t *testing.T) {
	b, srv := newTestPtradeBroker(t)
	if err := b.Connect(); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	if err := b.CancelOrder("OID-1"); err != nil {
		t.Fatalf("CancelOrder failed: %v", err)
	}
	srv.lastOrderMu.Lock()
	c := srv.lastCancel
	srv.lastOrderMu.Unlock()
	if c != "OID-1" {
		t.Fatalf("cancel called with %q, want OID-1", c)
	}
}

func TestPtradeFillPollingAndDedup(t *testing.T) {
	b, srv := newTestPtradeBroker(t)
	if err := b.Connect(); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	var trades int64
	gotFills := make([]Fill, 0, 2)
	b.SetTradeCallback(func(f Fill) {
		atomic.AddInt64(&trades, 1)
		gotFills = append(gotFills, f)
	})
	srv.addFill("600519.SH", "BUY", 100, 1500.0)
	ctx := context.Background()

	// 第一次轮询：拉到成交，回调 1 次
	b.pollFills(ctx)
	if got := atomic.LoadInt64(&trades); got != 1 {
		t.Fatalf("trades = %d, want 1", got)
	}
	if len(gotFills) != 1 || gotFills[0].Symbol != "600519.SH" || gotFills[0].Quantity != 100 {
		t.Fatalf("unexpected first fill: %+v", gotFills)
	}
	// 第二次轮询：无新成交，回调不重复
	b.pollFills(ctx)
	if got := atomic.LoadInt64(&trades); got != 1 {
		t.Fatalf("trades = %d, want 1 (dedup)", got)
	}
	// 补一个成交后再轮询：回调 2 次
	srv.addFill("000858.SZ", "SELL", 200, 120.0)
	b.pollFills(ctx)
	if got := atomic.LoadInt64(&trades); got != 2 {
		t.Fatalf("trades = %d, want 2", got)
	}
	if len(gotFills) != 2 || gotFills[1].Symbol != "000858.SZ" || gotFills[1].Side != SideSell || gotFills[1].Quantity != 200 {
		t.Fatalf("unexpected second fill: %+v", gotFills[1:])
	}
}

func TestPtradeCloseStopsPolling(t *testing.T) {
	b, _ := newTestPtradeBroker(t)
	if err := b.Connect(); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	if err := b.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	if b.IsLive() {
		t.Fatal("IsLive should be false after Close")
	}
	// Close 幂等
	if err := b.Close(); err != nil {
		t.Fatalf("re-Close failed: %v", err)
	}
}

// TestQMTBrokerHTTPBridge QMT 客户端内策略桥：复用 Ptrade 协议 mock 验证 HTTP 桥核心。
// QMT 与 Ptrade 共用 internal/broker 的 httpBridge，协议完全一致。
func TestQMTBrokerHTTPBridge(t *testing.T) {
	srv := newMockPtrade(t)
	ts := httptest.NewServer(srv.handler())
	t.Cleanup(ts.Close)
	b := NewQMTBroker(QMTConfig{HTTPPort: 8892, Account: "MOCK-ACC"})
	if b.Mode() != ModeLive {
		t.Fatalf("Mode = %q, want qmt", b.Mode())
	}
	if b.IsLive() {
		t.Fatal("IsLive should be false before Connect")
	}
	b.baseURL = ts.URL // 白盒：重定向到测试服务（复用 Ptrade 协议 mock）
	if err := b.Connect(); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	if !b.IsLive() {
		t.Fatal("IsLive should be true after Connect")
	}
	st := b.Status()
	if !st.Connected || st.Account != "MOCK-ACC" {
		t.Fatalf("Status unexpected: %+v", st)
	}
	oid, err := b.SubmitOrder(Order{Symbol: "sh600519", Side: SideBuy, Quantity: 200, Price: 1500.0})
	if err != nil {
		t.Fatalf("SubmitOrder failed: %v", err)
	}
	if oid == "" {
		t.Fatal("expected non-empty order id")
	}
	a, err := b.QueryAsset()
	if err != nil {
		t.Fatalf("QueryAsset failed: %v", err)
	}
	if a.Cash != 100000 || a.TotalAssets != 250000 {
		t.Fatalf("asset unexpected: %+v", a)
	}
	// 未连接时拒单
	if err := b.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	if b.IsLive() {
		t.Fatal("IsLive should be false after Close")
	}
	if _, err := b.SubmitOrder(Order{Symbol: "sh600519", Side: SideBuy, Quantity: 100, Price: 1}); err == nil {
		t.Fatal("SubmitOrder should fail after Close")
	}
}
