//go:build windows

package agent

// 文件职责（Windows 平台版本）：ACP 家族进程退出信号的平台占位实现。Windows
// 没有 Unix 意义上的"进程被信号终止"，这里一律返回 nil，让诊断逻辑走"拿不到
// 信号信息"分支。

import "os"

func acpProcessExitSignal(*os.ProcessState) *string {
	return nil
}
