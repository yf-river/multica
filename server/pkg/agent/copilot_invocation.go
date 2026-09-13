package agent

// 文件职责：Copilot CLI 的"启动目标选择"——决定实际派生哪个程序（argv[0]）与
// 整条 argv，重点是让含多行/引号的 -p prompt 不被中间层二次分词弄坏。
//
// macOS/Linux 上 npm 的 binstub 是指向 npm-loader.js 的符号链接、带
// `#!/usr/bin/env node` shebang，execve 把 argv 原样交给 node，node 的 spawnSync
// 又原样转给打包的原生二进制，无需处理。Windows 没有 shebang，npm 分发
// copilot.cmd/copilot.ps1 启动器，中间的每一层都会把 argv 重序列化到新命令行上，
// 两个启动器对 buildCopilotArgs 拼出的多行、带引号 -p prompt 都有损（症状是
// Copilot 自家那句 "It looks like your prompt was not quoted..." 报错）。因此
// Windows 改写（copilot_invocation_windows.go）跳过启动器，直接派生打包的
// copilot.exe。

import (
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// chooseCopilotInvocation 为一次 Copilot CLI 运行选定实际程序（argv[0]）与完整
// argv。平台特化返回 ok 就用它，否则退化为 (execName, args) 直通。
func chooseCopilotInvocation(execName, lookedUp string, args []string, logger *slog.Logger) (string, []string) {
	if argv0, full, ok := platformCopilotInvocation(lookedUp, args, logger); ok {
		return argv0, full
	}
	return execName, args
}

// resolveCopilotNativeFromShim 给定 Windows 上 PATH 找到的 npm `copilot.cmd` shim
// 路径，返回 npm 包里打包的原生 copilot 可执行文件路径。shim 不以 `.cmd` 结尾、
// 或任何已知位置都没有候选平台包提供二进制时返回 ""，调用方随即退回 PowerShell
// 启动器。
//
// 为什么要绕过启动器而不是更使劲地加引号：Go 这边没有任何办法让它们无损。两跳
// 都会按一套与 Go os/exec 所针对的 CRT 规则不同的规则重新解析命令行。
//
//   - copilot.cmd 用 `%*` 转发，cmd.exe 对原始命令行重新分词来展开它——-p prompt
//     里的换行和引号活不过这一跳。
//   - copilot.ps1 以 `& node.exe npm-loader.js $args` 结尾，PowerShell 把 $args
//     重序列化到 node 的命令行上。Windows PowerShell 5.1（以及默认走 Legacy 原生
//     参数传递的 pwsh <= 7.2）下内嵌双引号不会被重新转义，含引号的 prompt 会被
//     重新分词——cursor-agent 在 buildCursorArgs 里早已记录过的同一个缺陷
//     （#5649）。
//
// Copilot 没有 stdin prompt 通道（那是 cursor-agent 的逃生门），所以 prompt 必须
// 留在命令行上、启动器必须绕开。直接派生 copilot.exe 只剩一跳——Go → 原生二进制
// ——而 Go 的 syscall.EscapeArg 正是该二进制所用 CRT 解析的精确逆运算。
//
// 经 `npm install -g @github/copilot` 安装时的目录结构（对照
// @github/copilot 1.0.77 核实）：
//
//	<prefix>\copilot.cmd                                                                     (shim)
//	<prefix>\node_modules\@github\copilot\npm-loader.js                                      (JS 入口)
//	<prefix>\node_modules\@github\copilot\node_modules\@github\copilot-win32-x64\copilot.exe (原生)
//
// npm 把平台包嵌在父包下面；更老的 npm 版本与其它包管理器会把它提升到顶层
// node_modules，所以两处都探测。
//
// statFn 是可注入的，方便在非 Windows 主机上测试。
func resolveCopilotNativeFromShim(shimPath string, statFn func(string) (os.FileInfo, error)) string {
	if !strings.EqualFold(filepath.Ext(shimPath), ".cmd") {
		return ""
	}
	prefix := filepath.Dir(shimPath)
	for _, pkg := range copilotWindowsPackageCandidates(runtime.GOARCH) {
		candidates := []string{
			// npm 当前布局：可选平台依赖嵌在父包下面。
			filepath.Join(prefix, "node_modules", "@github", "copilot", "node_modules", "@github", pkg, "copilot.exe"),
			// 更老 npm 版本与其它安装器使用的提升布局。
			filepath.Join(prefix, "node_modules", "@github", pkg, "copilot.exe"),
		}
		for _, candidate := range candidates {
			if _, err := statFn(candidate); err == nil {
				return candidate
			}
		}
	}
	return ""
}

// copilotWindowsPackageCandidates 返回可能装着 copilot.exe 的 npm 平台包名，
// 排序让"给定 GOARCH 最可能命中"的排第一：ARM64 主机先试 arm64 构建；其余先试
// x64——因为 ARM64 主机上在仿真下跑的 x64 Node 装的是 x64 平台包。代价只是
// GOARCH 首选包没装时每次 miss 多一次 statFn 调用。
func copilotWindowsPackageCandidates(goarch string) []string {
	switch goarch {
	case "arm64":
		return []string{"copilot-win32-arm64", "copilot-win32-x64"}
	default:
		return []string{"copilot-win32-x64", "copilot-win32-arm64"}
	}
}
