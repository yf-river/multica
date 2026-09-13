//go:build !windows

package agent

// 文件职责：非 Windows 平台下"以进程组方式拥有并清理 agent 子进程树"的实现。
//
// 思路：进程诞生前就由 newRuntimeCmd（launch.go 中构造运行时进程的唯一地点）把它
// 放进自己的进程组（Setpgid，组 id == 子进程 pid）；之后对"负 pid"发信号即可一次
// 命中整棵树——agent CLI 加它派生的所有工具子进程——而不是只杀直接子进程、留下
// 继续跑的后代（opencode 的后果是后代在 EPIPE 上空转）。signalProcessGroup 负责
// 发信号，waitProcessGroupGone 用 kill(pid,0) 探测组是否已消失。
//
// 本文件与 proc_windows.go 互斥编译；Windows 没有进程组，改用 Job Object。

import (
	"errors"
	"log/slog"
	"os/exec"
	"syscall"
	"time"
)

// hideAgentWindow 在非 Windows 平台上为空操作。
func hideAgentWindow(cmd *exec.Cmd) {}

// configureProcessGroup 把子进程放进它自己的进程组（它成为组首进程，组 id 等于
// 子进程 pid）。这让 daemon 能一次信号整棵树——agent CLI 加上它派生的工具子进程
// ——而不是只杀直接子进程、漏掉任务取消或 daemon 重启后仍继续跑（对 opencode 还
// 会在 EPIPE 上空转）的后代。见 signalProcessGroup。
//
// 由 launch.go 里的 newRuntimeCmd 调用——那里是构造运行时进程的唯一地点。没有
// backend 直接调它：每个运行时进程都必须有进程组，而"按 backend opt-in"做不到
// 这一点（GH #7522）。
func configureProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// startOwnedProcessTree 在非 Windows 平台上就是普通 Start：newRuntimeCmd 早在
// 子进程存在之前就把它放进自己的进程组了，进程跑起来后没有剩余可认领的东西。
// logger 在这里用不上；Windows 需要它来上报"拥有权降级"。
//
// 它仍是本包启动长生命周期运行时进程的唯一入口——两个平台由此在每个 backend
// 共享同一处调用点。
func startOwnedProcessTree(cmd *exec.Cmd, _ *slog.Logger) error { return cmd.Start() }

// releaseProcessGroup 在非 Windows 平台上为空操作：进程组不需要句柄，成员都没了
// 它自然消失。
func releaseProcessGroup(cmd *exec.Cmd) {}

// codexInitializeRetrySupported 在 Unix 上恒为 true：信号发给整个进程组后，用
// waitProcessGroupGone 就能确证后代已终止。
func codexInitializeRetrySupported() bool { return true }

// signalProcessGroup 向 cmd 所属进程组的全体进程发送 sig（当它以
// configureProcessGroup 启动时），组发送失败则退化为只发单进程。目标是组（负
// pid），能打中 agent 派生的后代，而不只是组首进程自己。
func signalProcessGroup(cmd *exec.Cmd, sig syscall.Signal) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	if err := syscall.Kill(-cmd.Process.Pid, sig); err != nil {
		_ = cmd.Process.Signal(sig)
	}
}

// waitProcessGroupGone 轮询"给整组发信号 0"（探活，不发真信号）直到超时，报告
// 进程组是否已消失。出现 ESRCH（没有这样的进程组）即视为已消失。
func waitProcessGroupGone(cmd *exec.Cmd, timeout time.Duration) bool {
	if cmd == nil || cmd.Process == nil {
		return false
	}
	deadline := time.Now().Add(timeout)
	for {
		err := syscall.Kill(-cmd.Process.Pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
}
