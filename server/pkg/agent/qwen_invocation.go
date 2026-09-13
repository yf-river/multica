package agent

// 文件职责：Qwen Code 的"启动目标选择"——决定实际派生哪个程序（argv[0]）与整条
// argv，重点是在 Windows 上绕开 cmd.exe 对受管 argv 的重新分词。prompt 走 stdin、
// 从不进 argv，所以 argv 里只剩固定旗标，改写风险低。

import "log/slog"

// chooseQwenInvocation 为一次 Qwen Code 运行选定实际程序（argv[0]）与完整 argv。
//
// 背景：
//   - macOS/Linux 上 npm 装的 `qwen` binstub 是 shebang 脚本，exec 时把 JS 入口交给
//     node，argv 原样穿透。
//   - Windows 上 npm 改发 qwen.cmd。CreateProcess 跑 .cmd 要走 cmd.exe，而 .cmd 里
//     的 %* 是靠重新分词原命令行来展开的。为把流协议和自定义旗标挡在重新分词路径
//     之外，我们解析出 .cmd 旁边的 qwen.ps1，用 `-File <ps1>` 直接调 PowerShell，
//     让 Go 把每个 argv 当独立 token 传过去。任务 prompt 本身走 stdin 交付、绝不
//     出现在 argv 里（#6082）。
//
// Windows 特化实现在 qwen_invocation_windows.go；其它平台直接透传。
func chooseQwenInvocation(execName, lookedUp string, args []string, logger *slog.Logger) (string, []string) {
	if argv0, full, ok := platformQwenInvocation(lookedUp, args, logger); ok {
		return argv0, full
	}
	return execName, args
}
