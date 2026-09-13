//go:build !windows

package agent

// 文件职责：非 Windows 上 Copilot CLI 启动目标的平台占位——binstub 经 shebang 直接
// 调 node，os/exec 能原样传 argv，无需改写。

import "log/slog"

// platformCopilotInvocation 在非 Windows 平台上是空操作：Copilot CLI 的 binstub
// 经 shebang 直接调 node，Go 的 os/exec 可以原样传 argv。
func platformCopilotInvocation(_ string, _ []string, _ *slog.Logger) (string, []string, bool) {
	return "", nil, false
}
