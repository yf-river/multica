//go:build !windows

package util

// EnsureHiddenConsole 在非 Windows 平台上为空操作（无需隐藏控制台窗口）。
func EnsureHiddenConsole() {}
