package updater

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// DownloadFile 下载文件到 dest（带进度回调 downloaded/total 字节数）。
// 先写临时 .part 文件，成功后原子改名，避免留下不完整文件。
func DownloadFile(ctx context.Context, url, dest string, progress func(downloaded, total int64)) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return fmt.Errorf("创建下载目录失败: %w", err)
	}

	part := dest + ".part"
	_ = os.Remove(part)

	out, err := os.Create(part)
	if err != nil {
		return fmt.Errorf("创建临时文件失败: %w", err)
	}
	defer out.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		_ = os.Remove(part)
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/octet-stream")

	client := &http.Client{Timeout: 0} // 大文件不设整体超时，依赖 ctx
	resp, err := client.Do(req)
	if err != nil {
		_ = os.Remove(part)
		return fmt.Errorf("下载请求失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		_ = os.Remove(part)
		return fmt.Errorf("下载返回 %d", resp.StatusCode)
	}

	total := resp.ContentLength
	var written int64
	buf := make([]byte, 128*1024)
	for {
		n, rErr := resp.Body.Read(buf)
		if n > 0 {
			if _, wErr := out.Write(buf[:n]); wErr != nil {
				_ = os.Remove(part)
				return fmt.Errorf("写入文件失败: %w", wErr)
			}
			written += int64(n)
			if progress != nil {
				progress(written, total)
			}
		}
		if rErr == io.EOF {
			break
		}
		if rErr != nil {
			_ = os.Remove(part)
			return fmt.Errorf("读取下载内容失败: %w", rErr)
		}
	}
	_ = out.Close()
	if err := os.Rename(part, dest); err != nil {
		_ = os.Remove(part)
		return fmt.Errorf("完成下载失败: %w", err)
	}
	return nil
}

// DownloadText 下载小文本文件（如 .sha256 校验文件）
func DownloadText(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", userAgent)
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("下载校验文件失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("下载校验文件返回 %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return "", err
	}
	return string(b), nil
}
