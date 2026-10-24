//go:build !linux && !windows

package agent

import "log/slog"

// reapTaskEscapees 在非 Linux 平台上没有 /proc 可枚举，保持原行为：
// 只靠进程组信号，脱离组后代不回收（未知归属不杀）。
func reapTaskEscapees(string, CLIIdentity, *slog.Logger) int { return 0 }

// captureCLIIdentity 在非 Linux 平台上没有可信身份来源，返回无效值（不回收）。
func captureCLIIdentity(int) CLIIdentity { return CLIIdentity{} }
