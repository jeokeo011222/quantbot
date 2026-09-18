package updater

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/quantpilot/quantpilot/internal/config"
	"github.com/quantpilot/quantpilot/internal/version"
)

// State 更新状态
type State string

const (
	StateIdle        State = "idle"
	StateChecking    State = "checking"
	StateDownloading State = "downloading"
	StateReady       State = "ready"
	StateApplying    State = "applying"
	StateError       State = "error"
)

// UpdateInfo 检查结果
type UpdateInfo struct {
	HasUpdate      bool   `json:"has_update"`
	CurrentVersion string `json:"current_version"`
	LatestVersion  string `json:"latest_version"`
	Changelog      string `json:"changelog"`
	Size           int64  `json:"size"`
	PublishedAt    string `json:"published_at"`
}

// UpdateService 更新服务（状态机）
type UpdateService struct {
	mu      sync.Mutex
	state   State
	errMsg  string
	execDir string
	cfg     config.AppConfig

	onProgress func(downloaded, total int64)

	info       *UpdateInfo
	zipPath    string
	stagingDir string
	manifest   *Manifest
}

// NewUpdateService 创建更新服务
func NewUpdateService(cfg config.AppConfig, execDir string) *UpdateService {
	return &UpdateService{
		state:   StateIdle,
		execDir: execDir,
		cfg:     cfg,
	}
}

// SetProgress 设置下载进度回调（App 层注入用于推送前端事件）
func (s *UpdateService) SetProgress(fn func(downloaded, total int64)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onProgress = fn
}

// Check 检查 GitHub 最新版本
func (s *UpdateService) Check(ctx context.Context) (*UpdateInfo, error) {
	s.mu.Lock()
	s.state = StateChecking
	s.errMsg = ""
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if s.state == StateChecking {
			s.state = StateIdle
		}
		s.mu.Unlock()
	}()

	gh := NewGitHubClient(s.cfg.UpdateRepoOwner, s.cfg.UpdateRepoName)
	rel, err := gh.GetLatestRelease(ctx)
	if err != nil {
		return nil, err
	}

	tag := strings.TrimPrefix(rel.TagName, "v")
	cmp, err := version.Compare(tag, version.Version)
	if err != nil {
		return nil, fmt.Errorf("版本号解析失败: %w", err)
	}

	info := &UpdateInfo{
		CurrentVersion: version.Version,
		LatestVersion:  tag,
		Changelog:      rel.Body,
		PublishedAt:    rel.PublishedAt,
	}
	if zipAsset, ok := rel.FindZipAsset(); ok {
		info.Size = zipAsset.Size
	}
	info.HasUpdate = cmp > 0

	s.mu.Lock()
	s.info = info
	s.mu.Unlock()
	return info, nil
}

// Download 下载升级包、校验并解压，就绪后状态为 ready
func (s *UpdateService) Download(ctx context.Context) error {
	s.mu.Lock()
	if s.info == nil || !s.info.HasUpdate {
		s.mu.Unlock()
		return fmt.Errorf("暂无可用更新")
	}
	s.state = StateDownloading
	s.errMsg = ""
	s.mu.Unlock()

	gh := NewGitHubClient(s.cfg.UpdateRepoOwner, s.cfg.UpdateRepoName)
	rel, err := gh.GetLatestRelease(ctx)
	if err != nil {
		s.fail(err)
		return err
	}
	zipAsset, ok := rel.FindZipAsset()
	if !ok {
		err := fmt.Errorf("最新 Release 缺少升级包附件")
		s.fail(err)
		return err
	}
	// 以本次实际拉取的 Release 为准更新版本信息，避免 Check 之后又发布新版导致目录名/版本与下载包不一致
	tag := strings.TrimPrefix(rel.TagName, "v")
	s.mu.Lock()
	s.info.LatestVersion = tag
	s.info.Changelog = rel.Body
	s.info.PublishedAt = rel.PublishedAt
	s.info.Size = zipAsset.Size
	s.mu.Unlock()

	updDir := filepath.Join(s.execDir, ".update")
	if err := os.MkdirAll(updDir, 0755); err != nil {
		s.fail(err)
		return err
	}
	_ = os.RemoveAll(filepath.Join(updDir, tag))

	zipPath := filepath.Join(updDir, zipAsset.Name)
	s.zipPath = zipPath

	if err := DownloadFile(ctx, zipAsset.BrowserDownloadURL, zipPath, s.progress); err != nil {
		s.fail(err)
		return err
	}
	if err := s.verifyZip(ctx, gh, rel, zipAsset, zipPath); err != nil {
		s.fail(err)
		return err
	}

	staging := filepath.Join(updDir, tag, "staging")
	if err := ExtractZip(zipPath, staging); err != nil {
		s.fail(err)
		return err
	}
	man, err := LoadManifest(filepath.Join(staging, "manifest.json"))
	if err != nil {
		s.fail(err)
		return err
	}
	if err := man.VerifyDir(staging); err != nil {
		s.fail(err)
		return err
	}

	// 磁盘空间预检（需要约包体积 ×2）
	if free, fErr := FreeDiskSpace(s.execDir); fErr == nil && free < uint64(zipAsset.Size*2) {
		err := fmt.Errorf("磁盘空间不足：需要约 %d MB，可用 %d MB",
			zipAsset.Size*2/1024/1024, free/1024/1024)
		s.fail(err)
		return err
	}

	s.mu.Lock()
	s.stagingDir = staging
	s.manifest = man
	s.state = StateReady
	s.errMsg = ""
	s.mu.Unlock()
	return nil
}

// verifyZip 用 Release 的 .sha256 附件校验整包；缺失时依赖解压后的逐文件清单校验
func (s *UpdateService) verifyZip(ctx context.Context, gh *GitHubClient, rel *GitHubRelease, asset *GitHubAsset, zipPath string) error {
	shaAsset, ok := rel.FindSha256Asset(asset.Name)
	if !ok {
		log.Println("[Updater] 未找到 .sha256 附件，将依赖包内 manifest 逐文件校验")
		return nil
	}
	text, err := DownloadText(ctx, shaAsset.BrowserDownloadURL)
	if err != nil {
		return err
	}
	fields := strings.Fields(text)
	if len(fields) == 0 || strings.TrimSpace(fields[0]) == "" {
		log.Println("[Updater] .sha256 附件为空或格式异常，退回包内 manifest 逐文件校验")
		return nil
	}
	expected := fields[0]
	actual, err := HashFile(zipPath)
	if err != nil {
		return err
	}
	if !strings.EqualFold(expected, actual) {
		return fmt.Errorf("升级包 SHA256 校验失败")
	}
	log.Println("[Updater] 升级包 SHA256 校验通过")
	return nil
}

// Apply 写入升级计划并拉起 --apply-update 子进程；调用方随后退出主进程
func (s *UpdateService) Apply() error {
	s.mu.Lock()
	if s.state != StateReady || s.info == nil {
		s.mu.Unlock()
		return fmt.Errorf("当前状态不可应用更新")
	}
	exeName := filepath.Base(mustExecutable())
	plan := &UpdatePlan{
		NewVersion: s.info.LatestVersion,
		ParentPID:  os.Getpid(),
		ExecDir:    s.execDir,
		StagingDir: s.stagingDir,
		ExeName:    exeName,
	}
	s.state = StateApplying
	s.mu.Unlock()

	planPath := filepath.Join(s.execDir, ".update", "plan.json")
	if err := WritePlan(planPath, plan); err != nil {
		s.fail(err)
		return err
	}

	self, err := os.Executable()
	if err != nil {
		s.fail(err)
		return err
	}
	cmd := exec.Command(self, "--apply-update", planPath)
	cmd.Dir = s.execDir
	if err := cmd.Start(); err != nil {
		s.fail(err)
		return err
	}
	log.Printf("[Updater] 已拉起升级子进程，主进程即将退出（升级到 %s）", plan.NewVersion)
	return nil
}

// Status 返回当前更新状态（供前端轮询）
func (s *UpdateService) Status() map[string]interface{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := map[string]interface{}{
		"state": string(s.state),
		"error": s.errMsg,
	}
	if s.info != nil {
		st["has_update"] = s.info.HasUpdate
		st["current_version"] = s.info.CurrentVersion
		st["latest_version"] = s.info.LatestVersion
		st["changelog"] = s.info.Changelog
		st["size"] = s.info.Size
		st["published_at"] = s.info.PublishedAt
	}
	return st
}

// CleanupAfterUpdate 新版首次正常启动时清理升级残留（staging / 备份 / 旧 exe / 升级包），
// 同时清理历史升级遗留的过期货件，避免 .update 目录随升级次数无限膨胀。
func CleanupAfterUpdate(execDir string) {
	updDir := filepath.Join(execDir, ".update")
	if _, err := os.Stat(updDir); err != nil {
		return
	}
	cleanupUpdateArtifacts(updDir)

	pending := filepath.Join(updDir, "pending_apply.json")
	if _, err := os.Stat(pending); err != nil {
		return // 非升级后启动：仅清理过期货件即可
	}
	if err := os.Remove(pending); err != nil {
		log.Printf("[Updater] 清理升级标记失败: %v", err)
		return
	}
	_ = os.RemoveAll(filepath.Join(updDir, "backup"))
	exeName := filepath.Base(mustExecutable())
	_ = os.Remove(filepath.Join(execDir, exeName+".old"))
	log.Println("[Updater] 新版启动成功，已清理升级残留")
}

// cleanupUpdateArtifacts 清理 .update 下的 staging 版本目录、升级包 zip 与临时 .part 文件。
// backup 目录仅在确认升级成功（pending 标记存在）时由调用方删除。
func cleanupUpdateArtifacts(updDir string) {
	entries, err := os.ReadDir(updDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		full := filepath.Join(updDir, name)
		if e.IsDir() {
			if name != "backup" {
				_ = os.RemoveAll(full)
			}
			continue
		}
		lower := strings.ToLower(name)
		if strings.HasSuffix(lower, ".zip") || strings.HasSuffix(lower, ".part") {
			_ = os.Remove(full)
		}
	}
}

func (s *UpdateService) fail(err error) {
	s.mu.Lock()
	s.state = StateError
	s.errMsg = err.Error()
	s.mu.Unlock()
}

func (s *UpdateService) progress(downloaded, total int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.onProgress != nil {
		s.onProgress(downloaded, total)
	}
}

func mustExecutable() string {
	p, err := os.Executable()
	if err != nil {
		return "QuantBot.exe"
	}
	return p
}
