//go:build windows

package util

import "syscall"

var (
	kernel32          = syscall.NewLazyDLL("kernel32.dll")
	user32            = syscall.NewLazyDLL("user32.dll")
	procAllocConsole  = kernel32.NewProc("AllocConsole")
	procGetConsoleWnd = kernel32.NewProc("GetConsoleWindow")
	procShowWindow    = user32.NewProc("ShowWindow")
)

// swHide 传给 ShowWindow，用于隐藏控制台窗口。
const swHide = 0

// EnsureHiddenConsole 保证 daemon 拥有一个用户看不到的控制台。之后每个子进程
// （git、cmd 等）都会自动继承这个隐藏控制台，无需在每次调用时做 SysProcAttr 体操。
//
// 必须在 daemon 启动早期、任何子进程被拉起之前调用——继承的隐藏控制台只保护
// 本函数返回之后启动的子进程。之前就拉起的子进程会自己申请可见的控制台窗口，
// 重新引入 #2357 的弹窗闪烁回归。
func EnsureHiddenConsole() {
	if hwnd, _, _ := procGetConsoleWnd.Call(); hwnd != 0 {
		return // 已有控制台
	}
	if r, _, _ := procAllocConsole.Call(); r == 0 {
		return // AllocConsole 失败
	}
	if hwnd, _, _ := procGetConsoleWnd.Call(); hwnd != 0 {
		procShowWindow.Call(hwnd, swHide)
	}
}
