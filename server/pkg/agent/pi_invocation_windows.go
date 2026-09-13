//go:build windows

package agent

// 文件职责：Windows 上 Pi 启动目标特化——pi.cmd → PowerShell -Command pi.ps1。
// 选 -Command 而不选 -File，是因为只有它原样转发 stdin（详见 pi_invocation.go
// 导读与 rewriteCmdToPS1Command）。

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// platformPiInvocation 在 Windows 上把 pi.cmd 改写成 `PowerShell -Command
// pi.ps1`。-Command + @args 是唯一保留 stdin 字节的路线：-File 会在控制台 ANSI
// 代码页下重编码 stdin、毁掉非 ASCII 输入（#7355）。它还顺带避开 cmd.exe 的 %*
// 重新分词（#3306）。powerShellLookup 与 rewriteCmdToPS1 定义在
// cursor_invocation_windows.go。
func platformPiInvocation(lookedUp string, args []string, logger *slog.Logger) (string, []string, bool) {
	return rewriteCmdToPS1Command("pi", lookedUp, args, logger)
}

// rewriteCmdToPS1Command 与 rewriteCmdToPS1 相同，但以 `-Command & '<name>.ps1'
// @args` 收尾（而不是 `-File`），用于必须保住 stdin 原始字节的 CLI（Pi）。
func rewriteCmdToPS1Command(toolName, lookedUp string, args []string, logger *slog.Logger) (string, []string, bool) {
	ext := strings.ToLower(filepath.Ext(lookedUp))
	if ext != ".cmd" && ext != ".bat" {
		return "", nil, false
	}
	ps1 := filepath.Join(filepath.Dir(lookedUp), toolName+".ps1")
	if st, err := os.Stat(ps1); err != nil || st.IsDir() {
		return "", nil, false
	}

	psExe, ok := powerShellLookup()
	if !ok {
		return "", nil, false
	}

	full := piPowerShellCommandArgs(ps1, args)

	if logger != nil {
		logger.Info(toolName+": routing through powershell -Command to preserve stdin",
			"powershell", psExe,
			"ps1", ps1,
			"original", lookedUp,
		)
	}
	return psExe, full, true
}
