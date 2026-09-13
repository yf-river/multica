//go:build !windows

package agent

// 文件职责：非 Windows 上 cursor-agent 启动目标的平台占位——它是原生二进制，
// os/exec 能原样传 argv，无需改写。

import "log/slog"

// platformCursorInvocation 在非 Windows 平台上是空操作：cursor-agent 是原生
// 二进制，Go 的 os/exec 可以原样传 argv。
func platformCursorInvocation(_ string, _ []string, _ *slog.Logger) (string, []string, bool) {
	return "", nil, false
}
