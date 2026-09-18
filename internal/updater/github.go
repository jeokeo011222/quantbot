package updater

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	githubAPIBase = "https://api.github.com"
	userAgent     = "QuantBot-Updater"
	zipPrefix     = "QuantBot-v"
	zipSuffix     = ".zip"
)

// GitHubRelease GitHub Release 元数据
type GitHubRelease struct {
	TagName     string        `json:"tag_name"`
	Body        string        `json:"body"`
	PublishedAt string        `json:"published_at"`
	Assets      []GitHubAsset `json:"assets"`
}

// GitHubAsset Release 附件
type GitHubAsset struct {
	Name               string `json:"name"`
	Size               int64  `json:"size"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// GitHubClient 访问 GitHub Releases API（公开仓库免鉴权）
type GitHubClient struct {
	owner  string
	repo   string
	client *http.Client
}

// NewGitHubClient 创建 GitHub 客户端
func NewGitHubClient(owner, repo string) *GitHubClient {
	return &GitHubClient{
		owner: owner,
		repo:  repo,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// GetLatestRelease 获取最新 Release（GitHub 默认排除预发布与草稿）
func (g *GitHubClient) GetLatestRelease(ctx context.Context) (*GitHubRelease, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/releases/latest", githubAPIBase, g.owner, g.repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", userAgent)

	resp, err := g.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 GitHub 失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("仓库 %s/%s 暂无 Release（404）", g.owner, g.repo)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("GitHub API 返回 %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var rel GitHubRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, fmt.Errorf("解析 GitHub 响应失败: %w", err)
	}
	return &rel, nil
}

// FindZipAsset 在 Release 附件中查找升级包 zip
func (r *GitHubRelease) FindZipAsset() (*GitHubAsset, bool) {
	for i := range r.Assets {
		if strings.HasPrefix(r.Assets[i].Name, zipPrefix) && strings.HasSuffix(r.Assets[i].Name, zipSuffix) {
			return &r.Assets[i], true
		}
	}
	return nil, false
}

// FindSha256Asset 在 Release 附件中查找配套 sha256 校验文件
func (r *GitHubRelease) FindSha256Asset(zipName string) (*GitHubAsset, bool) {
	target := zipName + ".sha256"
	for i := range r.Assets {
		if r.Assets[i].Name == target {
			return &r.Assets[i], true
		}
	}
	return nil, false
}
