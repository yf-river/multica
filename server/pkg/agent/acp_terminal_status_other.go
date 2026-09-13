//go:build !windows

package agent

// 文件职责（非 Windows 平台版本）：从进程退出状态里提取"是被哪个信号杀的"，
// 供 ACP 家族 backend 的失败诊断使用。Windows 没有 Unix 信号语义，所以该平台
// 的实现（acp_terminal_status_windows.go）直接返回 nil。

import (
	"os"
	"syscall"
)

// acpProcessExitSignal 若进程是被信号终止的，返回信号名（如 "killed"）；进程正常
// 退出、state 为空或平台拿不到信号信息时返回 nil。
func acpProcessExitSignal(state *os.ProcessState) *string {
	if state == nil {
		return nil
	}
	// 把平台相关的退出状态断言成 syscall.WaitStatus；只有"确实是被信号杀的"才有
	// 可用的信号名，其它情况（正常退出、被 wait 接口不同实现）一律返回 nil。
	status, ok := state.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() {
		return nil
	}
	signal := status.Signal().String()
	return &signal
}
