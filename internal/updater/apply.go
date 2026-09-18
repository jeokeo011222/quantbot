package updater

import (
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

// ApplyUpdate 升级子进程入口：等待主进程退出 → 备份 → 替换 → 重启新版。
// 由 main.go 在检测到 --apply-update 参数时调用。
func ApplyUpdate(planPath string) error {
	plan, err := ReadPlan(planPath)
	if err != nil {
		return fmt.Errorf("读取升级计划失败: %w", err)
	}

	// 1. 等待主进程完全退出（释放 exe 文件句柄与单实例互斥体）
	if plan.ParentPID > 0 {
		deadline := time.Now().Add(90 * time.Second)
		for time.Now().Before(deadline) {
			if !processAlive(plan.ParentPID) {
				break
			}
			time.Sleep(300 * time.Millisecond)
		}
	}

	// 2. 加载并校验清单
	man, err := LoadManifest(filepath.Join(plan.StagingDir, "manifest.json"))
	if err != nil {
		return err
	}
	if err := man.VerifyDir(plan.StagingDir); err != nil {
		return err
	}

	exePath := filepath.Join(plan.ExecDir, plan.ExeName)
	exeOld := exePath + ".old"
	backupDir := filepath.Join(plan.ExecDir, ".update", "backup")
	_ = os.RemoveAll(backupDir)

	// 3. 备份旧文件（exe 走改名流程，其余文件复制到 backup 目录），并记录新版本新增文件
	var newFiles []string
	for _, f := range man.Files {
		if man.IsExe(f.Path, plan.ExeName) {
			continue
		}
		src := filepath.Join(plan.ExecDir, filepath.FromSlash(f.Path))
		if _, err := os.Stat(src); err != nil {
			newFiles = append(newFiles, f.Path) // 旧版本不存在 = 新版本新增文件，失败回滚时需清理
			continue
		}
		if err := copyFile(src, filepath.Join(backupDir, filepath.FromSlash(f.Path))); err != nil {
			return rollback(plan, man, backupDir, exeOld, exePath, newFiles, fmt.Errorf("备份 %s 失败: %w", f.Path, err))
		}
	}

	// 4. 备份主程序（Windows 允许改名运行中的 exe，改名后原路径即可被新文件占用）
	_ = os.Remove(exeOld)
	if err := os.Rename(exePath, exeOld); err != nil {
		return fmt.Errorf("备份主程序失败: %w", err)
	}

	// 5. 部署新文件
	for _, f := range man.Files {
		src := filepath.Join(plan.StagingDir, filepath.FromSlash(f.Path))
		dst := filepath.Join(plan.ExecDir, filepath.FromSlash(f.Path))
		if err := copyFile(src, dst); err != nil {
			return rollback(plan, man, backupDir, exeOld, exePath, newFiles, fmt.Errorf("部署 %s 失败: %w", f.Path, err))
		}
	}

	// 6. 写入待确认标记：新版首次成功启动后由 CleanupAfterUpdate 清理并删除 .old
	pendingPath := filepath.Join(plan.ExecDir, ".update", "pending_apply.json")
	if err := os.WriteFile(pendingPath, []byte(fmt.Sprintf(`{"version":%q,"time":%q}`,
		plan.NewVersion, time.Now().Format("2006-01-02 15:04:05"))), 0600); err != nil {
		return rollback(plan, man, backupDir, exeOld, exePath, newFiles, fmt.Errorf("写入升级标记失败: %w", err))
	}

	// 7. 启动新版
	cmd := exec.Command(exePath)
	cmd.Dir = plan.ExecDir
	if err := cmd.Start(); err != nil {
		return rollback(plan, man, backupDir, exeOld, exePath, newFiles, fmt.Errorf("启动新版失败: %w", err))
	}

	log.Printf("[Updater] 升级到 %s 完成，已启动新版", plan.NewVersion)
	return nil
}

// rollback 升级失败时从备份恢复旧文件、清理新版本新增文件并重启旧版
func rollback(plan *UpdatePlan, man *Manifest, backupDir, exeOld, exePath string, newFiles []string, cause error) error {
	log.Printf("[Updater] 升级失败，执行回滚: %v", cause)

	// 恢复普通文件
	for _, f := range man.Files {
		if man.IsExe(f.Path, plan.ExeName) {
			continue
		}
		backup := filepath.Join(backupDir, filepath.FromSlash(f.Path))
		dst := filepath.Join(plan.ExecDir, filepath.FromSlash(f.Path))
		if _, err := os.Stat(backup); err == nil {
			_ = copyFile(backup, dst)
		}
	}

	// 清理新版本新增、旧版本不存在的文件（无备份 = 旧版本没有 = 属新增文件）
	for _, rel := range newFiles {
		_ = os.Remove(filepath.Join(plan.ExecDir, filepath.FromSlash(rel)))
	}

	// 恢复主程序
	if _, err := os.Stat(exeOld); err == nil {
		_ = os.Rename(exeOld, exePath)
	}

	// 重启旧版
	if _, err := os.Stat(exePath); err == nil {
		cmd := exec.Command(exePath)
		cmd.Dir = plan.ExecDir
		_ = cmd.Start()
	}
	return fmt.Errorf("升级失败已回滚: %w", cause)
}

// processAlive 判断进程是否存活（Windows：通过 OpenProcess 探测）
func processAlive(pid int) bool {
	const processQueryLimitedInfo = 0x1000 // PROCESS_QUERY_LIMITED_INFORMATION
	handle, err := syscall.OpenProcess(processQueryLimitedInfo, false, uint32(pid))
	if err != nil {
		return false
	}
	_ = syscall.CloseHandle(handle)
	return true
}

// copyFile 复制文件（自动创建目标目录）
func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
