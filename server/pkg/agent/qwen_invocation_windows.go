//go:build windows

package agent

// 文件职责：Windows 上 Qwen Code 启动目标特化——qwen.cmd → PowerShell -File
// qwen.ps1，避开 cmd.exe 的 %* 重新分词（见 qwen_invocation.go 导读）。

import "log/slog"

// platformQwenInvocation 在 Windows 上把 qwen.cmd 改写成 `PowerShell -File
// qwen.ps1`，避免 cmd.exe 对受管 argv 的 %* 重新分词（见 #6082）。prompt 本身走
// stdin。rewriteCmdToPS1 定义在 cursor_invocation_windows.go。
func platformQwenInvocation(lookedUp string, args []string, logger *slog.Logger) (string, []string, bool) {
	return rewriteCmdToPS1("qwen", lookedUp, args, logger)
}
