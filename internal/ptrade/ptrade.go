// Package ptrade 提供恒生 Ptrade 实盘交易接口的文件夹管理与桥接策略文件生成。
//
// Ptrade 的实盘策略脚本必须运行在 Ptrade 客户端内嵌 Python 引擎中（外部进程不可直连），
// 因此本包在程序目录下创建 Ptrade 文件夹，并生成：
//   - ptrade_bridge.py  桥接策略：内嵌 HTTP 网关，接收 QuantBot 下单/查询，回调 Ptrade API
//   - config.json       接口配置模板（端口 / 账号等）
//   - 实盘接入手册.md    使用说明（随程序发布，不内嵌生成）
//
// 宿主侧 internal/broker.PtradeBroker 通过本地 HTTP 与该桥接网关通信。
package ptrade

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// FolderName Ptrade 文件夹名称（位于可执行文件目录下）
const FolderName = "Ptrade"

// BridgeFileName Ptrade 桥接策略文件名
const BridgeFileName = "ptrade_bridge.py"

// ConfigFileName 接口配置模板文件名
const ConfigFileName = "config.json"

// GetFolderPath 获取 Ptrade 文件夹路径（可执行文件目录下）
func GetFolderPath() string {
	exePath, err := os.Executable()
	if err != nil {
		return FolderName
	}
	return filepath.Join(filepath.Dir(exePath), FolderName)
}

// EnsureFolder 确保 Ptrade 文件夹存在，并生成/更新桥接策略与配置文件。
// 说明文档统一使用文件夹下的「实盘接入手册.md」，故不再生成内嵌 README.md。
func EnsureFolder() (string, error) {
	dir := GetFolderPath()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("创建 Ptrade 文件夹失败: %w", err)
	}
	files := map[string]string{
		BridgeFileName: bridgeScript,
		ConfigFileName: configTemplate,
	}
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			return "", fmt.Errorf("生成接口文件 %s 失败: %w", name, err)
		}
	}
	return dir, nil
}

// WriteConfig 将配置写入 Ptrade 文件夹下的 config.json
func WriteConfig(cfg map[string]interface{}) error {
	dir := GetFolderPath()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, ConfigFileName), data, 0644)
}

// configTemplate Ptrade 桥接策略配置模板
const configTemplate = `{
  "host": "127.0.0.1",
  "port": 8891,
  "account": "",
  "poll_interval": 1
}
`

// EnsureBridgeFiles 确保桥接策略与配置文件存在（供启动时调用）
func EnsureBridgeFiles() error {
	_, err := EnsureFolder()
	return err
}
