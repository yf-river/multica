//go:build windows

// update_windows.go 是 CLI 自更新流程在 Windows 平台上的实现。Windows 对正在运行的
// .exe 持有排他句柄、不允许覆盖，只能靠「先把旧的挪开、再把新的换上来」的挪位法，
// 并配合下次启动时清理遗留的 .old 文件。
package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/multica-ai/multica/server/internal/selfexec"
)

// oldBinarySuffix 是新二进制安装期间给「旧可执行文件」加的临时后缀。Windows 拒绝
// 覆盖运行中的 .exe 但允许重命名它，所以要先把正在运行的二进制挪开再放新的进去。
const oldBinarySuffix = ".old"

// replaceBinary 用刚下载好的新二进制替换正在运行的可执行文件。Windows 对运行中的
// .exe 持排他句柄，Unix 那套「直接 rename 覆盖」会报 "Access is denied"。因此改为：
//  1. 先清掉上次更新可能残留的 .old 文件；
//  2. 把正在运行的可执行文件挪到 exePath+".old"；
//  3. 把新二进制 rename 到位；
//  4. 第 3 步若失败，把原文件挪回来，避免用户手里没有可用的 multica.exe。
//
// 遗留的 .old 文件由下次启动时的 CleanupStaleUpdateArtifacts 清理。
func replaceBinary(tmpPath, exePath string) error {
	oldPath := exePath + oldBinarySuffix

	// 尽力清理旧的 .old；如果失败（文件仍被锁住）也没关系，下一次 Rename 会报出
	// 一个有用的错误。
	_ = os.Remove(oldPath)

	if err := os.Rename(exePath, oldPath); err != nil {
		return fmt.Errorf("move running binary aside: %w", err)
	}

	if err := os.Rename(tmpPath, exePath); err != nil {
		// 把旧的挪回来，别让用户落在没有 multica.exe 的境地。
		if rerr := os.Rename(oldPath, exePath); rerr != nil {
			return fmt.Errorf("install new binary: %w (and failed to restore: %v)", err, rerr)
		}
		return fmt.Errorf("install new binary: %w", err)
	}

	return nil
}

// CleanupStaleUpdateArtifacts 清理上次更新遗留的 `.old` 二进制。Windows 删不掉
// 正在运行的 .exe，所以上次更新可能留下一个；等用户重启后，这次调用把空间回收掉。
func CleanupStaleUpdateArtifacts() {
	exePath, err := selfexec.Resolve()
	if err != nil {
		return
	}
	if resolved, err := filepath.EvalSymlinks(exePath); err == nil {
		exePath = resolved
	}
	_ = os.Remove(exePath + oldBinarySuffix)
}
