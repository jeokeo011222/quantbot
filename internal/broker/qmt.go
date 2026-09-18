package broker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// QMTConfig XtQuant/MiniQMT 网关所需配置。
type QMTConfig struct {
	// PythonPath python 解释器路径；为空用 PATH 中的 python。
	PythonPath string
	// ScriptPath xtquant_interface.py 的绝对路径。
	ScriptPath string
	// FolderPath XtQuant config.json 所在目录（写入配置）。
	FolderPath string
	// QtPath QMT 安装目录 userdata_mini 路径（接 connect 时需已登录）。
	QtPath string
	// Account 券商资金账号。
	Account string
	// AccountType STOCK(STOCK) / CREDIT(信用)。
	AccountType string
}

// qmtRequest 提交给 Python 网关的命令。
type qmtRequest struct {
	ID        string  `json:"id"`
	Cmd       string  `json:"cmd"`
	Symbol    string  `json:"symbol,omitempty"`
	Volume    int     `json:"volume,omitempty"`
	Price     float64 `json:"price,omitempty"`
	PriceType int     `json:"price_type,omitempty"`
	OrderID   string  `json:"order_id,omitempty"`
	Strategy  string  `json:"strategy,omitempty"`
}

// qmtResponse 网关返回。
type qmtResponse struct {
	ID        string           `json:"id"`
	Type      string           `json:"type"` // response / event
	OK        bool             `json:"ok"`
	Command   string           `json:"cmd,omitempty"`
	Message   string           `json:"message,omitempty"`
	OrderID   string           `json:"order_id,omitempty"`
	Asset     *json.RawMessage `json:"asset,omitempty"`
	Positions *json.RawMessage `json:"positions,omitempty"`
	EventKind string           `json:"event,omitempty"` // order/trade/asset/disconnected
	// 事件明细
	Symbol string  `json:"symbol,omitempty"`
	Side   string  `json:"side,omitempty"`
	Volume int     `json:"volume,omitempty"`
	Price  float64 `json:"price,omitempty"`
}

// EventMessage 网关推送的异步事件。
type EventMessage struct {
	Kind     string // order / trade / asset / disconnected
	Symbol   string
	Side     Side
	Volume   int
	Price    float64
	External string
}

// WithKeyRequest JSON 打包带 id 的命令。
func makeQMTRequest(id, cmd string, payload map[string]interface{}) ([]byte, error) {
	body := map[string]interface{}{"id": id, "cmd": cmd}
	for k, v := range payload {
		body[k] = v
	}
	return json.Marshal(body)
}

// QMTBroker 实盘成交桥：管理常驻 Python 网关进程，按 JSON 行协议收发命令与回报。
type QMTBroker struct {
	cfg     QMTConfig
	proc    *exec.Cmd
	stdin   io.WriteCloser
	mu      sync.Mutex // 保护命令发出的写 + 等待响应（同一时刻只有一个在途命令）
	pending map[string]chan *qmtResponse
	cancel  context.CancelFunc

	connectedMu sync.RWMutex
	connected   bool
	account     string

	tradeCb   func(Fill)
	tradeCbMu sync.Mutex
	eventCb   func(EventMessage)
	eventCbMu sync.Mutex

	lastAsset   Asset
	lastAssetMu sync.RWMutex
	closeOnce   sync.Once
}

// NewQMTBroker 创建 QMT 实盘桥，但不启动进程（需调用 Connect()）。
func NewQMTBroker(cfg QMTConfig) *QMTBroker {
	return &QMTBroker{cfg: cfg, pending: make(map[string]chan *qmtResponse)}
}

// SetTradeCallback 注册成交回报回调（实盘成交后由网关推送触发）。
func (b *QMTBroker) SetTradeCallback(cb func(Fill)) {
	b.tradeCbMu.Lock()
	b.tradeCb = cb
	b.tradeCbMu.Unlock()
}

// SetEventCallback 注册任意异步事件回调（order/asset/disconnected 等）。
func (b *QMTBroker) SetEventCallback(cb func(EventMessage)) {
	b.eventCbMu.Lock()
	b.eventCb = cb
	b.eventCbMu.Unlock()
}

func (b *QMTBroker) fireTrade(orderID, symbol string, side Side, vol int, price float64) {
	b.tradeCbMu.Lock()
	cb := b.tradeCb
	b.tradeCbMu.Unlock()
	if cb != nil {
		cb(Fill{OrderID: orderID, Symbol: symbol, Side: side, Quantity: vol, Price: price, TradedAt: time.Now()})
	}
}

func (b *QMTBroker) fireEvent(e EventMessage) {
	b.eventCbMu.Lock()
	cb := b.eventCb
	b.eventCbMu.Unlock()
	if cb != nil {
		cb(e)
	}
}

// Connect 写入配置并启动常驻网关进程。
func (b *QMTBroker) Connect() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.proc != nil {
		return fmt.Errorf("网关已启动")
	}
	if err := b.writeConfig(); err != nil {
		return err
	}
	python := b.cfg.PythonPath
	if python == "" {
		python = "python"
	}
	ctx, cancel := context.WithCancel(context.Background())
	b.cancel = cancel
	cmd := exec.CommandContext(ctx, python, b.cfg.ScriptPath, "--serve")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return fmt.Errorf("打开网关 stdin 失败: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return fmt.Errorf("打开网关 stdout 失败: %w", err)
	}
	stderrBuf := &bytes.Buffer{}
	cmd.Stderr = stderrBuf
	if err := cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("启动网关进程失败: %w", err)
	}
	b.proc = cmd
	b.stdin = stdin
	go b.readLoop(stdout)
	return nil
}

// writeConfig 把 QMT 配置写入 XtQuant 目录 config.json，供网关加载。
func (b *QMTBroker) writeConfig() error {
	cfg := map[string]interface{}{
		"path":          b.cfg.QtPath,
		"account":       b.cfg.Account,
		"account_type":  b.cfg.AccountType,
		"mini_qmt":      true,
		"strategy_name": "QuantBot",
		"strategy_path": filepath.Join(b.cfg.FolderPath, "QuantBot"),
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(b.cfg.FolderPath, 0755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(b.cfg.FolderPath, "config.json"), data, 0644)
}

// readLoop 独占读取网关 stdout：按行解析，响应按 id 唤醒等待者，事件分发回调。
func (b *QMTBroker) readLoop(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var resp qmtResponse
		if err := json.Unmarshal([]byte(line), &resp); err != nil {
			continue
		}
		if resp.Type == "event" {
			b.handleEvent(resp)
			continue
		}
		if resp.ID != "" {
			b.mu.Lock()
			ch := b.pending[resp.ID]
			delete(b.pending, resp.ID)
			b.mu.Unlock()
			if ch != nil {
				ch <- &resp
			}
		}
	}
}

// handleEvent 处理网关推送的异步事件。
func (b *QMTBroker) handleEvent(r qmtResponse) {
	switch r.EventKind {
	case "disconnected":
		b.connectedMu.Lock()
		b.connected = false
		b.connectedMu.Unlock()
		b.fireEvent(EventMessage{Kind: "disconnected"})
	case "trade":
		side := Side(strings.ToUpper(r.Side))
		b.fireTrade(r.OrderID, r.Symbol, side, r.Volume, r.Price)
		b.fireEvent(EventMessage{Kind: "trade", Symbol: r.Symbol, Side: side, Volume: r.Volume, Price: r.Price, External: r.OrderID})
	case "asset":
		if r.Asset != nil {
			var a Asset
			if err := json.Unmarshal(*r.Asset, &a); err == nil {
				b.lastAssetMu.Lock()
				b.lastAsset = a
				b.lastAssetMu.Unlock()
			}
		}
		b.fireEvent(EventMessage{Kind: "asset"})
	}
}

// doRequest 发送一条命令并等待响应（带超时）。
func (b *QMTBroker) doRequest(ctx context.Context, id, cmd string, payload map[string]interface{}) (*qmtResponse, error) {
	b.mu.Lock()
	if b.stdin == nil {
		b.mu.Unlock()
		return nil, fmt.Errorf("网关未启动")
	}
	ch := make(chan *qmtResponse, 1)
	b.pending[id] = ch
	raw, err := makeQMTRequest(id, cmd, payload)
	if err != nil {
		delete(b.pending, id)
		b.mu.Unlock()
		return nil, err
	}
	if _, err := b.stdin.Write(append(raw, '\n')); err != nil {
		delete(b.pending, id)
		b.mu.Unlock()
		return nil, fmt.Errorf("向网关写命令失败: %w", err)
	}
	b.mu.Unlock()

	select {
	case resp := <-ch:
		return resp, nil
	case <-ctx.Done():
		b.mu.Lock()
		delete(b.pending, id)
		b.mu.Unlock()
		return nil, fmt.Errorf("等待网关响应超时(%s): %w", cmd, ctx.Err())
	}
}

// MustConnect 发送 connect 命令并刷新连接/资产状态。
func (b *QMTBroker) MustConnect() error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	id := genID()
	resp, err := b.doRequest(ctx, id, "connect", nil)
	if err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("QMT 连接失败: %s", resp.Message)
	}
	b.connectedMu.Lock()
	b.connected = true
	b.account = b.cfg.Account
	b.connectedMu.Unlock()
	if resp.Asset != nil {
		var a Asset
		if json.Unmarshal(*resp.Asset, &a) == nil {
			b.lastAssetMu.Lock()
			b.lastAsset = a
			b.lastAssetMu.Unlock()
		}
	}
	return nil
}

// SubmitOrder 实盘下单：符号转换后发给网关，返回 QMT 委托号。
func (b *QMTBroker) SubmitOrder(o Order) (string, error) {
	if !b.IsLive() {
		return "", fmt.Errorf("QMT 桥未连接")
	}
	qmtSym, err := ToQmtSymbol(o.Symbol)
	if err != nil {
		return "", err
	}
	cmd := "sell"
	priceType := 11 // 最新价
	if o.Side == SideBuy {
		cmd = "buy"
	}
	if o.Price > 0 {
		priceType = 14 // 限价
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	id := genID()
	resp, err := b.doRequest(ctx, id, cmd, map[string]interface{}{
		"symbol":     qmtSym,
		"volume":     o.Quantity,
		"price":      o.Price,
		"price_type": priceType,
		"strategy":   "QuantBot",
	})
	if err != nil {
		return "", fmt.Errorf("QMT 下单失败: %w", err)
	}
	if !resp.OK {
		return "", fmt.Errorf("QMT 拒单: %s", resp.Message)
	}
	return resp.OrderID, nil
}

// QueryPositions 查询 QMT 持仓。
func (b *QMTBroker) QueryPositions() ([]Position, error) {
	if !b.IsLive() {
		return nil, fmt.Errorf("QMT 桥未连接")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	id := genID()
	resp, err := b.doRequest(ctx, id, "position", nil)
	if err != nil {
		return nil, err
	}
	if !resp.OK {
		return nil, fmt.Errorf("查询持仓失败: %s", resp.Message)
	}
	positions := []Position{}
	if resp.Positions != nil {
		if err := json.Unmarshal(*resp.Positions, &positions); err != nil {
			return nil, fmt.Errorf("解析持仓失败: %w", err)
		}
	}
	return positions, nil
}

// QueryAsset 查询 QMT 资金/资产。
func (b *QMTBroker) QueryAsset() (Asset, error) {
	if !b.IsLive() {
		return Asset{}, fmt.Errorf("QMT 桥未连接")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	id := genID()
	resp, err := b.doRequest(ctx, id, "asset", nil)
	if err != nil {
		return Asset{}, err
	}
	if !resp.OK {
		return Asset{}, fmt.Errorf("查询资产失败: %s", resp.Message)
	}
	var a Asset
	if resp.Asset != nil {
		if err := json.Unmarshal(*resp.Asset, &a); err != nil {
			return Asset{}, err
		}
	}
	b.lastAssetMu.Lock()
	b.lastAsset = a
	b.lastAssetMu.Unlock()
	return a, nil
}

// IsLive 仅当网关进程已启动且连接成功时为真。
func (b *QMTBroker) IsLive() bool {
	b.connectedMu.RLock()
	defer b.connectedMu.RUnlock()
	return b.proc != nil && b.connected
}

// Mode 返回实盘模式。
func (b *QMTBroker) Mode() Mode { return ModeLive }

// Status 返回连接/环境状态。
func (b *QMTBroker) Status() Status {
	st := Status{Mode: ModeLive}
	if b.proc == nil {
		st.Message = "网关未启动"
		return st
	}
	// 探测环境（可离线）
	st.PythonOK = pythonOnPath(b.cfg.PythonPath)
	st.XtQuantOK = xtquantLibPresent(b.cfg.PythonPath)
	st.Connected = b.connected
	st.Account = b.cfg.Account
	b.lastAssetMu.RLock()
	a := b.lastAsset
	b.lastAssetMu.RUnlock()
	st.Cash = a.Cash
	st.MarketVal = a.MarketValue
	if st.Connected {
		st.Message = "QMT 已连接（实盘）"
	} else {
		st.Message = "QMT 未连接：请确认 MiniQMT 已登录后执行测试连接"
	}
	return st
}

// Close 停止网关进程。
func (b *QMTBroker) Close() error {
	var err error
	b.closeOnce.Do(func() {
		if b.cancel != nil {
			b.cancel()
		}
		if b.stdin != nil {
			b.stdin.Close()
		}
		if b.proc != nil {
			done := make(chan struct{})
			go func() { b.proc.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				b.proc.Process.Kill()
				b.proc.Wait()
			}
		}
		b.connectedMu.Lock()
		b.connected = false
		b.connectedMu.Unlock()
	})
	return err
}

func genID() string {
	return fmt.Sprintf("q%d", time.Now().UnixNano())
}

func pythonOnPath(python string) bool {
	if python == "" {
		_, err := exec.LookPath("python")
		return err == nil
	}
	_, err := os.Stat(python)
	return err == nil
}

// xtquantLibPresent 探测 XtQuant 库是否可见（尽力而为，不执行 python 以免阻塞）。
func xtquantLibPresent(python string) bool {
	// 常见安装目录判定；最终以 connect 结果为准。
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	candidates := []string{
		filepath.Join(home, "site-packages", "xtquant"),
		filepath.Join(home, "AppData", "Roaming", "Python"),
		filepath.Join(home, "AppData", "Local", "Programs", "Python"),
	}
	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil && info.IsDir() {
			return true
		}
	}
	return false
}
