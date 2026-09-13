// 文件职责：`daemon start` 在非 Windows 平台（Unix 系）上的平台差异实现——
// 后台化 spawn 属性、关闭信号、stdio 是否需要重指向、日志 tail 用系统 tail 命令。
// Windows 对应实现见 cmd_daemon_windows.go。
//go:build !windows

package main

import (
	"context"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"syscall"
)

// daemonSysProcAttr 返回 spawn 后台 daemon 时的进程属性。withBreakaway 参数只为
// 与 Windows 版共享同一签名（那边它控制 CREATE_BREAKAWAY_FROM_JOB）；Unix 下
// Setsid 一个就够把子进程从父会话与进程组里摘出来。
func daemonSysProcAttr(_ bool) *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

// isAccessDeniedSpawnErr 在 Unix 上恒为 false。Windows 版拿它识别
// ERROR_ACCESS_DENIED（父 Job 不允许 breakaway）从而触发关掉 breakaway 的重试；
// 那个重试在 Unix 上是 no-op。
func isAccessDeniedSpawnErr(_ error) bool { return false }

// notifyShutdownContext 在父 ctx 上监听 SIGINT/SIGTERM，任一信号触发取消。
func notifyShutdownContext(parent context.Context) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(parent, syscall.SIGINT, syscall.SIGTERM)
}

// repointStdioToErrLog 在 Unix 上是 no-op。滚动写的 daemon.log writer 在轮转时
// 改名当前活动文件；POSIX 下已打开的文件描述符——包括旧版 self-update launcher
// 继承到本进程 stdout/stderr 上的那个——从不阻塞改名或 unlink，所以不重指向标准
// 句柄轮转也能工作。只有 Windows 需要释放继承的句柄（原因见 Windows 版）。
func repointStdioToErrLog(_ string) {}

// tailLogFile 用系统 tail 追日志：默认打印最后 lines 行，follow 时持续跟进。
func tailLogFile(logPath string, lines int, follow bool) error {
	args := []string{"-n", strconv.Itoa(lines)}
	if follow {
		// 用 -F（按名跟进）而不是 -f（按描述符跟进）：轮转把 daemon.log 改名腾走后
		// 再开新文件时，-F 会按路径重开新文件，而 -f 会一直贴在被轮转走的 inode 上
		// 悄悄不再输出。GNU 与 BSD/macOS 的 tail 都支持 -F。
		args = append(args, "-F")
	}
	args = append(args, logPath)

	tail := exec.Command("tail", args...)
	tail.Stdout = os.Stdout
	tail.Stderr = os.Stderr
	return tail.Run()
}
