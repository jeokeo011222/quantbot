package tdx

import (
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// TDXClient TDX 行情客户端
type TDXClient struct {
	conn        net.Conn
	host        string
	port        int
	encryptKey  []byte
	seqNum      uint32
	mu          sync.Mutex
	connected   atomic.Bool
	heartbeatCh chan struct{}
	done        chan struct{}
}

// NewTDXClient 创建 TDX 客户端
func NewTDXClient(host string, port int) *TDXClient {
	return &TDXClient{
		host:        host,
		port:        port,
		encryptKey:  make([]byte, 0),
		heartbeatCh: make(chan struct{}),
		done:        make(chan struct{}),
	}
}

// Connect 连接 TDX 服务器
func (c *TDXClient) Connect() error {
	if c.connected.Load() {
		return nil
	}

	addr := net.JoinHostPort(c.host, strconv.Itoa(c.port))
	conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		return fmt.Errorf("connect to %s failed: %w", addr, err)
	}

	c.conn = conn
	c.connected.Store(true)

	// 发送登录包
	if err := c.login(); err != nil {
		c.Close()
		return fmt.Errorf("login failed: %w", err)
	}

	// 启动心跳
	go c.heartbeat()

	log.Printf("[TDX] Connected to %s", addr)
	return nil
}

// Close 关闭连接
func (c *TDXClient) Close() {
	if !c.connected.Load() {
		return
	}

	c.connected.Store(false)
	close(c.done)

	if c.conn != nil {
		c.conn.Close()
	}
}

// IsConnected 检查连接状态
func (c *TDXClient) IsConnected() bool {
	return c.connected.Load()
}

// login 发送登录请求
func (c *TDXClient) login() error {
	// 登录体：客户端版本号 + 未知字段
	loginBody := make([]byte, 12)
	binary.LittleEndian.PutUint32(loginBody[0:], 1) // client version
	binary.LittleEndian.PutUint32(loginBody[4:], 0) // unknown
	binary.LittleEndian.PutUint32(loginBody[8:], 0) // unknown

	resp, err := c.sendRequestInternal(CmdLogin, 0x00, loginBody, true)
	if err != nil {
		return err
	}

	// 解析登录响应
	if len(resp) > 0 {
		// 服务器返回加密密钥（通常是 4 字节）
		keyLen := len(resp)
		if keyLen >= 4 {
			c.encryptKey = make([]byte, keyLen)
			copy(c.encryptKey, resp)
		}
	}

	// 如果服务器未返回密钥，使用默认密钥
	if len(c.encryptKey) == 0 {
		c.encryptKey = []byte{0x00, 0x00, 0x00, 0x00}
	}

	log.Printf("[TDX] Login successful, encryption key: %v", c.encryptKey)
	return nil
}

// heartbeat 心跳保持
func (c *TDXClient) heartbeat() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if !c.connected.Load() {
				return
			}
			err := c.sendHeartbeat()
			if err != nil {
				log.Printf("[TDX] Heartbeat failed: %v, reconnecting...", err)
				c.reconnect()
			}
		case <-c.done:
			return
		}
	}
}

// sendHeartbeat 发送心跳包
func (c *TDXClient) sendHeartbeat() error {
	resp, err := c.sendRequest(CmdHeartbeat, 0x00, nil)
	if err != nil {
		return err
	}
	_ = resp
	return nil
}

// reconnect 重新连接（调用方触发于心跳失败，本函数持 c.mu 以独占重建过程；
// 登录收发改用 sendRequestLocked（不重复加锁），避免原 sendRequestInternal 二次加锁导致死锁）。
func (c *TDXClient) reconnect() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn != nil {
		c.conn.Close()
	}
	c.connected.Store(false)

	addr := net.JoinHostPort(c.host, strconv.Itoa(c.port))
	for i := 0; i < 3; i++ {
		conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
		if err != nil {
			log.Printf("[TDX] Reconnect attempt %d failed: %v", i+1, err)
			time.Sleep(time.Duration(i+1) * time.Second)
			continue
		}
		c.conn = conn
		c.connected.Store(true)

		// 重新登录
		loginBody := make([]byte, 12)
		binary.LittleEndian.PutUint32(loginBody[0:], 1)
		resp, err := c.sendRequestLocked(CmdLogin, 0x00, loginBody, true)
		if err == nil && len(resp) > 0 {
			c.encryptKey = make([]byte, len(resp))
			copy(c.encryptKey, resp)
		}

		log.Printf("[TDX] Reconnected to %s", addr)
		return
	}

	log.Printf("[TDX] Reconnection failed after 3 attempts")
}

// sendRequest 发送请求并接收响应（公开接口）
func (c *TDXClient) sendRequest(cmd, subCmd byte, body []byte) ([]byte, error) {
	return c.sendRequestInternal(cmd, subCmd, body, false)
}

// sendRequestInternal 发送请求并接收响应（公开入口，加锁）
func (c *TDXClient) sendRequestInternal(cmd, subCmd byte, body []byte, isLogin bool) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sendRequestLocked(cmd, subCmd, body, isLogin)
}

// sendRequestLocked 发送请求并接收响应（调用方必须已持有 c.mu，内部不再加锁。
// 供 reconnect 在持锁重建连接时复用登录收发，避免同锁二次加锁导致死锁）。
func (c *TDXClient) sendRequestLocked(cmd, subCmd byte, body []byte, isLogin bool) ([]byte, error) {
	if !c.connected.Load() {
		return nil, fmt.Errorf("not connected")
	}

	seq := atomic.AddUint32(&c.seqNum, 1)

	frame := EncodeFrame(cmd, subCmd, body, seq, c.encryptKey, isLogin)
	if _, err := c.conn.Write(frame); err != nil {
		return nil, fmt.Errorf("write failed: %w", err)
	}

	// 读取响应 - 先读头 (type + cmd + subCmd + bodyLen + seqNum + checksum)
	// TDX 响应帧格式: [type(1)][cmd(1)][subcmd(1)][bodyLen(2) LE][seqNum(4) LE][body(N)][checksum(2) LE]
	// 最小头部: 1 + 1 + 1 + 2 + 4 + 2 = 11 字节 (当 bodyLen=0 时)
	header := make([]byte, 9) // type + cmd + subcmd + bodyLen + seqNum
	if _, err := io.ReadFull(c.conn, header); err != nil {
		return nil, fmt.Errorf("read header failed: %w", err)
	}

	respType := header[0]
	respCmd := header[1]
	bodyLen := binary.LittleEndian.Uint16(header[3:5])

	// 读取响应体
	respBody := make([]byte, bodyLen)
	if bodyLen > 0 {
		if _, err := io.ReadFull(c.conn, respBody); err != nil {
			return nil, fmt.Errorf("read body failed: %w", err)
		}
	}

	// 读取校验和（2 字节）
	checksumBuf := make([]byte, 2)
	if _, err := io.ReadFull(c.conn, checksumBuf); err != nil {
		return nil, fmt.Errorf("read checksum failed: %w", err)
	}

	// 解密响应
	var decryptedData []byte
	if bodyLen > 0 {
		// 如果响应帧类型为 0x01，说明响应是加密的
		if respType == 0x01 && len(c.encryptKey) > 0 {
			decryptedData = xorDecrypt(respBody, c.encryptKey)
		} else {
			decryptedData = make([]byte, bodyLen)
			copy(decryptedData, respBody)
		}
	}

	_ = respCmd
	return decryptedData, nil
}

// ============== 数据获取方法 ==============

// GetBars 获取K线数据
func (c *TDXClient) GetBars(market, code string, category uint16, count uint16) ([]KlineBar, error) {
	if !c.connected.Load() {
		return nil, fmt.Errorf("not connected")
	}

	body, err := BuildKlineRequest(market, code, category, 0, count)
	if err != nil {
		return nil, err
	}

	resp, err := c.sendRequest(CmdGetSecurityBars, 0x00, body)
	if err != nil {
		return nil, err
	}

	return parseKlineResponse(resp, category), nil
}

// GetMinuteBars 获取分时数据
func (c *TDXClient) GetMinuteBars(market, code string, count uint16) ([]MinuteBar, error) {
	if !c.connected.Load() {
		return nil, fmt.Errorf("not connected")
	}

	body, err := BuildMinuteBarsRequest(market, code, 0, count)
	if err != nil {
		return nil, err
	}

	resp, err := c.sendRequest(CmdGetMinuteBars, 0x00, body)
	if err != nil {
		return nil, err
	}

	return parseMinuteBarsResponse(resp), nil
}

// GetQuotes 获取实时行情
func (c *TDXClient) GetQuotes(market string, codes []string) ([]Quote, error) {
	if !c.connected.Load() {
		return nil, fmt.Errorf("not connected")
	}

	body, err := BuildQuotesRequest(market, codes)
	if err != nil {
		return nil, err
	}

	resp, err := c.sendRequest(CmdGetSecurityQuotes, 0x00, body)
	if err != nil {
		return nil, err
	}

	return parseQuotesResponse(resp, codes), nil
}

// GetTransactions 获取逐笔成交
func (c *TDXClient) GetTransactions(market, code string, count uint16) ([]Transaction, error) {
	if !c.connected.Load() {
		return nil, fmt.Errorf("not connected")
	}

	body, err := BuildTransactionRequest(market, code, 0, count)
	if err != nil {
		return nil, err
	}

	resp, err := c.sendRequest(CmdGetTransactionData, 0x00, body)
	if err != nil {
		return nil, err
	}

	return parseTransactionsResponse(resp), nil
}

// GetSecurityInfo 获取证券信息
func (c *TDXClient) GetSecurityInfo(market, code string) (*SecurityInfo, error) {
	if !c.connected.Load() {
		return nil, fmt.Errorf("not connected")
	}

	body, err := BuildSecurityInfoRequest(market, code)
	if err != nil {
		return nil, err
	}

	resp, err := c.sendRequest(CmdGetSecurityInfo, 0x00, body)
	if err != nil {
		return nil, err
	}

	return parseSecurityInfoResponse(resp), nil
}

// GetSecurityCount 获取证券数量
func (c *TDXClient) GetSecurityCount(market uint8) (uint16, error) {
	if !c.connected.Load() {
		return 0, fmt.Errorf("not connected")
	}

	body := BuildSecurityCountRequest(market)
	resp, err := c.sendRequest(CmdGetSecurityCount, 0x00, body)
	if err != nil {
		return 0, err
	}

	if len(resp) >= 2 {
		return binary.LittleEndian.Uint16(resp[:2]), nil
	}
	return 0, nil
}

// GetSecurityList 获取证券列表
func (c *TDXClient) GetSecurityList(market uint8, start, count uint16) ([]SecurityListItem, error) {
	if !c.connected.Load() {
		return nil, fmt.Errorf("not connected")
	}

	body := BuildSecurityListRequest(market, start, count)
	resp, err := c.sendRequest(CmdGetSecurityList, 0x00, body)
	if err != nil {
		return nil, err
	}

	return parseSecurityListResponse(resp), nil
}

// GetMarketStat 获取市场统计
func (c *TDXClient) GetMarketStat(market uint8) (*MarketStat, error) {
	if !c.connected.Load() {
		return nil, fmt.Errorf("not connected")
	}

	body := BuildMarketStatRequest(market)
	resp, err := c.sendRequest(CmdGetMarketStat, 0x00, body)
	if err != nil {
		return nil, err
	}

	return parseMarketStatResponse(resp), nil
}
