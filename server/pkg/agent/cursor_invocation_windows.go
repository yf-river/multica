//go:build windows

package agent

// 文件职责：Windows 上 cursor-agent 与其它 npm 类 CLI 的启动目标特化——绕开
// cmd.exe 的 %* 重新分词，改以 `powershell -File <name>.ps1` 直连。shared 的
// powerShellLookup / rewriteCmdToPS1 也被 copilot_invocation_windows.go 复用。

import (
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// powerShellLookup 决定用哪个 PowerShell 宿主。测试里可覆盖；生产调用方保持默认
// 即可。
var powerShellLookup = defaultPowerShellLookup

// rewriteCmdToPS1 处理 Windows 上 npm 安装的 agent CLI 的 .cmd → PowerShell 改写。
// 每个 CLI 都带一个 <name>.cmd 启动器，其内容实质上是
//
//	powershell -NoProfile -ExecutionPolicy Bypass -File <name>.ps1 %*
//
// 走 cmd.exe 时 %* 会把原始命令行字符串重新分词，弄坏多行参数（最要命的是 -p
// prompt）。于是我们直接用 `-File <name>.ps1` 调 PowerShell，让 Go 把每个 argv
// 元素当独立 token 传过去。
//
// toolName 同时用于日志与推导规范 ps1 文件名（toolName + ".ps1"，位于 lookedUp
// 同目录）。用规范名而不是启动器的基名，能保证用户自起名的自定义 .bat 包装也能
// 找到正确的脚本。
func rewriteCmdToPS1(toolName, lookedUp string, args []string, logger *slog.Logger) (string, []string, bool) {
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

	full := make([]string, 0, 5+len(args))
	full = append(full, "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", ps1)
	full = append(full, args...)

	if logger != nil {
		logger.Info(toolName+": routing through powershell -File to preserve argv tokens",
			"powershell", psExe,
			"ps1", ps1,
			"original", lookedUp,
		)
	}
	return psExe, full, true
}

// platformCursorInvocation 在 Windows 上把 cursor-agent.cmd 改写成 PowerShell
// -File cursor-agent.ps1，避开 cmd.exe 的 %* 重新分词。
func platformCursorInvocation(lookedUp string, args []string, logger *slog.Logger) (string, []string, bool) {
	return rewriteCmdToPS1("cursor-agent", lookedUp, args, logger)
}

// defaultPowerShellLookup 优先用 PATH 上的 PowerShell（PowerShell 7 的 pwsh.exe
// 或任何用户覆盖的 powershell.exe），找不到再退回 Windows 自带的系统路径。
func defaultPowerShellLookup() (string, bool) {
	for _, name := range []string{"pwsh.exe", "powershell.exe"} {
		if p, err := exec.LookPath(name); err == nil {
			return p, true
		}
	}
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	candidate := filepath.Join(root, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
		return candidate, true
	}
	return "", false
}
