package agent

// 文件职责：Pi 的"启动目标选择"——决定实际派生哪个程序（argv[0]）与整条 argv，
// 重点是 Windows 上既要绕开 cmd.exe 的 %* 重新分词（#3306），又要保证 stdin 字节
// 原封不动（#7355）。

import (
	"log/slog"
	"strings"
)

// piPowerShellCommandArgs 拼 -Command 路线的 argv。-Command 是唯一不碰 stdin
// 字节的 PowerShell 路线（#7355）。
func piPowerShellCommandArgs(ps1 string, args []string) []string {
	// PowerShell 单引号字符串里的单引号靠翻倍转义。
	quotedPS1 := strings.ReplaceAll(ps1, "'", "''")
	full := make([]string, 0, 6+len(args))
	full = append(full, "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", "& '"+quotedPS1+"' @args")
	return append(full, args...)
}

// choosePiInvocation 为一次 Pi 运行选定实际程序（argv[0]）与完整 argv。
//
// 背景：
//   - macOS/Linux 上 npm 装的 `pi` binstub 是 shebang 脚本，把 JS 入口直接交给
//     node，argv 原样穿透。
//   - Windows 上 npm 安装包带 `pi.cmd`，内容是
//     "powershell ... -File pi.ps1 %*"。CreateProcess 跑 .cmd 要走 cmd.exe，而
//     .cmd 里的 %* 靠重新分词原命令行来展开，会弄坏含换行或其它空白的参数——对
//     Pi 来说就是 buildPiArgs 传的多行位置 prompt。症状：Pi 会话 JSONL 只记录到
//     prompt 的第一行（#3306）。我们解析出 .cmd 旁边的 pi.ps1，用
//     `-Command & 'pi.ps1' @args` 调 PowerShell。prompt 在 #6457 挪到 stdin 之后，
//     -File 会在控制台 ANSI 代码页下重编码 stdin，毁掉每个非 ASCII 字节（#7355）
//     ——所以每次 Pi 调用都走 -Command 路线，它把 stdin 原封不动转发。#3306 残留
//     的暴露面只剩"多行的自定义选项值"，而 @args 仍会把这样的值当单个 token 转发。
//
// Windows 特化实现在 pi_invocation_windows.go；其它平台直接透传。
func choosePiInvocation(execName, lookedUp string, args []string, logger *slog.Logger) (string, []string) {
	if argv0, full, ok := platformPiInvocation(lookedUp, args, logger); ok {
		return argv0, full
	}
	return execName, args
}
