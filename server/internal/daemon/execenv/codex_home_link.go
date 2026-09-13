//go:build !windows

package execenv

// 本文件是目录/文件链接的 Unix（非 Windows）实现：直接 os.Symlink。
// Windows 版见 codex_home_link_windows.go（那里 symlink 需要开发者模式/管理员权限，
// 因此带 junction / 复制回退）。

import "os"

// createDirLink 用符号链接把 dst 目录指向 src（Unix 无权限门槛）。
func createDirLink(src, dst string) error {
	return os.Symlink(src, dst)
}

// createFileLink 用符号链接把 dst 文件指向 src。
func createFileLink(src, dst string) error {
	return os.Symlink(src, dst)
}
