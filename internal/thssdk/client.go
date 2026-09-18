// 包级默认客户端与数据源 TTL 缓存（供六维判势/工具共用）。
package thssdk

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

var (
	globalMu     sync.RWMutex
	globalClient *Client // 由 Configure 注入（启动时从配置读取 API Key）
)

// Configure 配置包级默认客户端（baseURL 为空使用官方默认地址）。apiKey 为空则禁用官方源。
func Configure(apiKey, baseURL string) {
	globalMu.Lock()
	defer globalMu.Unlock()
	if apiKey == "" {
		globalClient = nil
		return
	}
	globalClient = NewClient(apiKey, baseURL)
}

// Client 返回包级默认客户端；未配置或未启用时返回 nil。
func Default() *Client {
	globalMu.RLock()
	defer globalMu.RUnlock()
	return globalClient
}

// Enabled 官方数据源是否已配置可用。
func Enabled() bool {
	return Default() != nil
}

// ==================== 通用 TTL 缓存（按数据类型不同有效期） ====================

// Cache 简单并发安全 TTL 缓存。
type Cache[T any] struct {
	mu  sync.Mutex
	ttl time.Duration
	ok  bool
	val T
	at  time.Time
}

// NewCache 创建 TTL 缓存。
func NewCache[T any](ttl time.Duration) *Cache[T] { return &Cache[T]{ttl: ttl} }

// Get 未过期命中返回 true。
func (c *Cache[T]) Get() (T, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ok && time.Since(c.at) < c.ttl {
		return c.val, true
	}
	var zero T
	return zero, false
}

// Set 写入缓存。
func (c *Cache[T]) Set(v T) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.val, c.ok, c.at = v, true, time.Now()
}

// ==================== 文件工具 ====================

// writeFileAtomic 原子写入文件（先写临时文件再改名，避免半截文件）。
func writeFileAtomic(dest string, r io.Reader) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	tmp := dest + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dest)
}

// ==================== 业务侧常用组合接口 ====================

// SentimentSnapshot 情绪面快照：由官方涨停/跌停/炸板池聚合（真实数据）。
type SentimentSnapshot struct {
	LimitUpCnt     int     // 涨停家数
	LimitDownCnt   int     // 跌停家数
	BlowUpCnt      int     // 炸板家数（曾触板已开板）
	BlowUpRate     float64 // 炸板率 = 炸板/(涨停+炸板)
	MaxBoardHeight int     // 连板天梯最高连板高度
	AsOf           time.Time
}

// FetchSentimentSnapshot 一次性聚合情绪面数据（3 个池 + 连板天梯），任一失败按字段降级。
func FetchSentimentSnapshot(ctx context.Context, c *Client) (*SentimentSnapshot, error) {
	if c == nil {
		return nil, fmt.Errorf("thssdk 未配置")
	}
	snap := &SentimentSnapshot{AsOf: time.Now()}
	// 涨停池
	if p, err := c.LimitUpPool(ctx, 200); err == nil {
		snap.LimitUpCnt = len(p.Item)
		if p.Timestamp > 0 {
			snap.AsOf = time.UnixMilli(p.Timestamp)
		}
	}
	// 跌停池
	if p, err := c.LimitDownPool(ctx, 200); err == nil {
		snap.LimitDownCnt = len(p.Item)
	}
	// 炸板池
	if p, err := c.LimitBreakPool(ctx, 200); err == nil {
		snap.BlowUpCnt = len(p.Item)
	}
	// 连板天梯（最高连板高度）
	if l, err := c.LimitUpLadder(ctx); err == nil {
		snap.MaxBoardHeight = l.MaxBoardHeight()
	}
	if snap.LimitUpCnt+snap.BlowUpCnt > 0 {
		snap.BlowUpRate = float64(snap.BlowUpCnt) / float64(snap.LimitUpCnt+snap.BlowUpCnt)
	}
	return snap, nil
}
