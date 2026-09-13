//go:build windows

package agent

// 文件职责：Windows 上 Copilot CLI 的启动目标特化——直接派生 npm 包内打包的原生
// copilot.exe，保住 buildCopilotArgs 拼出的多行 -p prompt（详见
// copilot_invocation.go 的文件导读）。

import (
	"log/slog"
	"os"
)

// platformCopilotInvocation 在 Windows 上通过派生 npm 包里打包的原生 copilot.exe，
// 保住 buildCopilotArgs 拼出的多行 -p prompt——cmd.exe 的 %* 展开和 PowerShell 的
// $args 重序列化都无法把它重新分词（为什么两个启动器都没法改安全，见
// resolveCopilotNativeFromShim）。
//
// 找不到原生二进制时——安装不全，或 Copilot 版本老到还没有平台包——退回
// `powershell -File copilot.ps1`，它至少避开了更糟的 cmd.exe 层。powerShellLookup
// 与 rewriteCmdToPS1 定义在 cursor_invocation_windows.go。
func platformCopilotInvocation(lookedUp string, args []string, logger *slog.Logger) (string, []string, bool) {
	if native := resolveCopilotNativeFromShim(lookedUp, os.Stat); native != "" {
		if logger != nil {
			logger.Info("copilot: spawning the bundled native binary to keep argv intact",
				"shim", lookedUp,
				"native", native,
			)
		}
		return native, args, true
	}
	return rewriteCmdToPS1("copilot", lookedUp, args, logger)
}
