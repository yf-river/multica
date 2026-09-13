//go:build !windows

package agent

// 文件职责：非 Windows 上 Pi 启动目标的平台占位——binstub 经 shebang 直接调
// node，os/exec 能原样传 argv。

import "log/slog"

// platformPiInvocation 在非 Windows 平台上是空操作：Pi 的 binstub 经 shebang
// 直接调 node，Go 的 os/exec 可以原样传 argv。
func platformPiInvocation(_ string, _ []string, _ *slog.Logger) (string, []string, bool) {
	return "", nil, false
}
