package brainutil

import (
	"log"
	"time"
)

// SafeGo 安全地启动一个goroutine，内置panic恢复
// 参数name用于日志标识，fn为要执行的函数
func SafeGo(name string, fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[Goroutine] Panic recovered in %s: %v", name, r)
			}
		}()
		fn()
	}()
}

// RetryWithBackoff 带指数退避的重试机制
// 参数: name为操作标识，maxRetries为最大重试次数，fn为要执行的函数（返回error）
func RetryWithBackoff(name string, maxRetries int, fn func() error) error {
	var lastErr error
	for i := 0; i < maxRetries; i++ {
		if i > 0 {
			backoff := time.Duration(1<<uint(i-1)) * 100 * time.Millisecond
			log.Printf("[Retry] %s: attempt %d/%d, waiting %v", name, i+1, maxRetries, backoff)
			time.Sleep(backoff)
		}

		if err := fn(); err != nil {
			lastErr = err
			log.Printf("[Retry] %s: attempt %d/%d failed: %v", name, i+1, maxRetries, err)
			continue
		}

		return nil
	}

	return lastErr
}

// SafeGoWithRetry 安全启动goroutine并带重试
func SafeGoWithRetry(name string, maxRetries int, fn func() error) {
	SafeGo(name, func() {
		if err := RetryWithBackoff(name, maxRetries, fn); err != nil {
			log.Printf("[Goroutine] %s failed after %d retries: %v", name, maxRetries, err)
		}
	})
}