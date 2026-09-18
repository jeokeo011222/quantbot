// Package updater 实现 QuantBot 自动升级：检查、下载、校验、应用（自我替换）与回滚。
package updater

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Manifest 升级包清单：仅包含允许替换的程序文件与版本化数据文件
type Manifest struct {
	Version    string     `json:"version"`
	MinVersion string     `json:"min_version"`
	Files      []FileItem `json:"files"`
}

// FileItem 单个文件条目
type FileItem struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// 用户数据目录前缀：升级包中禁止出现（绝对不可触碰）
var forbiddenPrefixes = []string{
	"config",
	"database",
	"log",
	"data" + "/" + "trades",
	"config.json",
}

// 允许随包更新的版本化数据文件白名单（data 目录下默认全部禁止，防误覆盖 duckdb 等）。
// 统一使用正斜杠，与 validatePath 的规范化路径保持一致。
var allowedDataFiles = map[string]bool{
	"data/stock_dict.json": true,
}

// LoadManifest 从文件加载并校验清单。
// 升级包会携带供「全新安装」使用的用户数据模板（config/database/log/data 等），
// 但这些在「升级」时必须被剔除、绝不覆盖已有用户数据，也不应因此中断升级。
// 故先 Sanitize 过滤掉所有用户数据/禁止路径条目，再对保留的程序文件条目做严格校验。
func LoadManifest(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取清单失败: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("解析清单失败: %w", err)
	}
	m.Sanitize()
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// Sanitize 剔除清单中命中禁止路径（config/database/log/data 非白名单等）的条目。
// 这些路径是用户数据驻留地，升级时绝不可覆盖；从应用清单中移除即不参与备份/部署/校验，
// 而非让整个升级因它们而失败。validatePath 仍是最终条目的最后一道安全闸。
func (m *Manifest) Sanitize() {
	kept := m.Files[:0]
	for i := range m.Files {
		if validatePath(m.Files[i].Path) == nil {
			kept = append(kept, m.Files[i])
		}
	}
	m.Files = kept
}

// Validate 校验清单结构与路径安全性
func (m *Manifest) Validate() error {
	if m.Version == "" {
		return fmt.Errorf("清单缺少版本号")
	}
	if len(m.Files) == 0 {
		return fmt.Errorf("清单为空（无待更新文件）")
	}
	for _, f := range m.Files {
		if err := validatePath(f.Path); err != nil {
			return err
		}
		if f.SHA256 == "" {
			return fmt.Errorf("清单文件缺少校验和: %s", f.Path)
		}
	}
	return nil
}

// validatePath 校验相对路径安全性：禁止绝对路径、路径穿越、用户数据目录
func validatePath(rel string) error {
	if rel == "" {
		return fmt.Errorf("清单包含空路径")
	}
	if filepath.IsAbs(rel) {
		return fmt.Errorf("清单禁止绝对路径: %s", rel)
	}
	clean := filepath.ToSlash(filepath.Clean(rel))
	if clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") {
		return fmt.Errorf("清单禁止路径穿越: %s", rel)
	}
	if clean == "." {
		return fmt.Errorf("清单禁止根路径")
	}
	lower := strings.ToLower(clean)
	for _, p := range forbiddenPrefixes {
		pLower := strings.ToLower(filepath.ToSlash(p))
		if lower == pLower || strings.HasPrefix(lower, pLower+"/") {
			return fmt.Errorf("清单禁止用户数据路径: %s", rel)
		}
	}
	// data/ 下除白名单外一律禁止（防误覆盖 duckdb、trades 等）
	if strings.HasPrefix(lower, "data/") && !allowedDataFiles[lower] {
		return fmt.Errorf("清单禁止 data 目录下非白名单文件: %s", rel)
	}
	return nil
}

// FindFile 按路径查找清单条目（大小写不敏感）
func (m *Manifest) FindFile(rel string) (*FileItem, bool) {
	rel = filepath.ToSlash(filepath.Clean(rel))
	for i := range m.Files {
		if strings.EqualFold(filepath.ToSlash(filepath.Clean(m.Files[i].Path)), rel) {
			return &m.Files[i], true
		}
	}
	return nil, false
}

// IsExe 判断清单条目是否为主程序
func (m *Manifest) IsExe(rel string, exeName string) bool {
	return strings.EqualFold(filepath.ToSlash(filepath.Clean(rel)), filepath.ToSlash(exeName))
}

// VerifyDir 校验 dir 下每个清单文件的 SHA256 与大小
func (m *Manifest) VerifyDir(dir string) error {
	for _, f := range m.Files {
		p := filepath.Join(dir, filepath.FromSlash(f.Path))
		info, err := os.Stat(p)
		if err != nil {
			return fmt.Errorf("清单文件缺失: %s: %w", f.Path, err)
		}
		if info.Size() != f.Size {
			return fmt.Errorf("清单文件大小不符: %s（期望 %d，实际 %d）", f.Path, f.Size, info.Size())
		}
		h, err := HashFile(p)
		if err != nil {
			return fmt.Errorf("清单文件校验失败: %s: %w", f.Path, err)
		}
		if !strings.EqualFold(h, f.SHA256) {
			return fmt.Errorf("清单文件校验和不符: %s（期望 %s，实际 %s）", f.Path, f.SHA256, h)
		}
	}
	return nil
}

// HashFile 计算文件 SHA256 十六进制值
func HashFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
