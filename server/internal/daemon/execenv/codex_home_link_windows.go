//go:build windows

package execenv

// 本文件是目录/文件链接的 Windows 实现。Windows 上 os.Symlink 需要开发者模式或
// 管理员权限，于是为目录准备了 junction（mklink /J，普通权限即可用）、为文件准备了
// 「复制」兜底，保证 per-task home 与共享源的链接关系在受限环境也能建立。

import (
	"fmt"
	"os"
	"os/exec"
)

// createDirLink 先试 os.Symlink（Windows 上需开发者模式或管理员）。失败则回退到目录
// junction（mklink /J）——普通权限即可工作、且能跨卷。
func createDirLink(src, dst string) error {
	if err := os.Symlink(src, dst); err == nil {
		return nil
	}
	out, err := exec.Command("cmd", "/c", "mklink", "/J", dst, src).CombinedOutput()
	if err != nil {
		return fmt.Errorf("mklink /J %s %s: %s: %w", dst, src, out, err)
	}
	return nil
}

// createFileLink 先试 os.Symlink；失败则回退为复制文件，保证内容仍可用。
func createFileLink(src, dst string) error {
	if err := os.Symlink(src, dst); err == nil {
		return nil
	}
	return copyFile(src, dst)
}
