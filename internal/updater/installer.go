package updater

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

// UpdatePlan 升级执行计划（主进程写入，--apply-update 子进程读取）
type UpdatePlan struct {
	NewVersion string `json:"new_version"`
	ParentPID  int    `json:"parent_pid"`
	ExecDir    string `json:"exec_dir"`    // 程序目录（build\bin）
	StagingDir string `json:"staging_dir"` // 解压后的新版本目录
	ExeName    string `json:"exe_name"`    // 主程序文件名（QuantBot.exe）
}

// ExtractZip 安全解压 zip 到 destDir（防路径穿越）
func ExtractZip(zipPath, destDir string) error {
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return err
	}
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("打开升级包失败: %w", err)
	}
	defer r.Close()

	for _, f := range r.File {
		rel := filepath.FromSlash(f.Name)
		if rel == "" || filepath.IsAbs(rel) {
			return fmt.Errorf("升级包包含非法路径: %s", f.Name)
		}
		clean := filepath.Clean(rel)
		if clean == ".." || len(clean) > 3 && clean[:3] == ".."+string(os.PathSeparator) {
			return fmt.Errorf("升级包包含路径穿越: %s", f.Name)
		}
		target := filepath.Join(destDir, clean)

		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		src, err := f.Open()
		if err != nil {
			return err
		}
		dst, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
		if err != nil {
			src.Close()
			return err
		}
		if _, err := io.Copy(dst, src); err != nil {
			dst.Close()
			src.Close()
			return err
		}
		dst.Close()
		src.Close()
	}
	return nil
}

// WritePlan 写入升级计划文件
func WritePlan(planPath string, p *UpdatePlan) error {
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(planPath, data, 0600)
}

// ReadPlan 读取升级计划文件
func ReadPlan(planPath string) (*UpdatePlan, error) {
	data, err := os.ReadFile(planPath)
	if err != nil {
		return nil, err
	}
	var p UpdatePlan
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// FreeDiskSpace 获取路径所在磁盘的可用字节数（Windows）
func FreeDiskSpace(path string) (uint64, error) {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	proc := kernel32.NewProc("GetDiskFreeSpaceExW")
	ptr, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var free uint64
	r1, _, e := proc.Call(
		uintptr(unsafe.Pointer(ptr)),
		uintptr(unsafe.Pointer(&free)),
		0, 0,
	)
	if r1 == 0 {
		return 0, e
	}
	return free, nil
}
