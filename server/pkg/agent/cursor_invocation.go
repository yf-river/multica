package agent

// 文件职责：cursor-agent 的"启动目标选择"——决定实际派生哪个程序（argv[0]）与
// 整条 argv，重点是在 Windows 上绕开 cmd.exe 对多行 -p prompt 的重新分词。

import "log/slog"

// chooseCursorInvocation 为一次 cursor-agent 运行选定实际程序（argv[0]）与完整
// argv。
//
// 背景：
//   - macOS/Linux 上 cursor-agent 是真二进制，可直接经 os/exec 传 argv——无需改写。
//   - Windows 上官方安装包带的是 cursor-agent.cmd，其内容是
//     "powershell ... -File cursor-agent.ps1 %*"。CreateProcess 跑 .cmd 要走
//     cmd.exe，而 .cmd 批处理里的 %* 是靠对原命令行重新分词来展开的，会把含换行
//     或其它空白的参数（例如多行 `-p` prompt）弄坏。为留在官方启动路径上、同时躲开
//     那次重新分词，我们解析出 .cmd 旁边的 cursor-agent.ps1，用 `-File <ps1>`
//     直接调 PowerShell，让 Go 把每个 argv 当独立 token 传过去。
//
// Windows 特化实现在 cursor_invocation_windows.go；其它平台直接透传。
func chooseCursorInvocation(execName, lookedUp string, args []string, logger *slog.Logger) (string, []string) {
	if argv0, full, ok := platformCursorInvocation(lookedUp, args, logger); ok {
		return argv0, full
	}
	return execName, args
}
