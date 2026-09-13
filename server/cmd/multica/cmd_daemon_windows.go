// 文件职责：`daemon start` 的 Windows 平台实现——处理 Windows 特有的大坑：
// 进程 Job Object 与 KILL_ON_JOB_CLOSE、无 FILE_SHARE_DELETE 时的日志轮转阻塞、
// 旧 launcher 继承句柄等。Unix 对应实现见 cmd_daemon_unix.go。
//go:build windows

package main

import (
	"context"
	"errors"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

const (
	// detachedProcess 切断继承的控制台：关闭父 cmd/PowerShell 窗口后不再向 daemon
	// 传播 CTRL_CLOSE_EVENT。
	detachedProcess = 0x00000008
	// createBreakawayFromJob 让 daemon 逃出父 shell 的 Job Object。现代 Windows
	// Terminal / cmd.exe / PowerShell 会把它们拉起的进程放进开了 KILL_ON_JOB_CLOSE
	// 的 Job Object：父 shell 一退出，内核杀掉该 job 内所有进程——包括我们只用
	// detachedProcess 尝试「脱钩」的子进程。detachedProcess 只切断控制台，切不断
	// Job 继承。加上 createBreakawayFromJob 后 CreateProcess 把新进程放进父 Job
	// 之外，daemon 才能活过父 shell 退出。
	//
	// 若父 Job 没授予 BREAKAWAY_OK，CreateProcess 返回 ERROR_ACCESS_DENIED。此时
	// 调用方退回只用 detachedProcess——daemon 回到修复前「听凭父 Job 生命周期」
	// 的行为。
	createBreakawayFromJob = 0x01000000
	sigBreak               = syscall.Signal(0x15)
)

// daemonSysProcAttr 返回 spawn 后台 daemon 时的进程属性。默认是 detachedProcess +
// createBreakawayFromJob，让 daemon 在父控制台关闭与父 Job Object 关闭后都存活。
// daemon 的 stdout/stderr 在 Start() 前就已重定向到日志文件，丢掉控制台无妨；
// `daemon stop` 走 HTTP /shutdown 而不是 GenerateConsoleCtrlEvent，丢掉进程组也
// 无妨。
//
// withBreakaway 参数让调用方在 CreateProcess 以 ERROR_ACCESS_DENIED 失败时
// （父 Job 不允许 breakaway）用 withBreakaway=false 重试一次。
func daemonSysProcAttr(withBreakaway bool) *syscall.SysProcAttr {
	flags := uint32(detachedProcess)
	if withBreakaway {
		flags |= createBreakawayFromJob
	}
	return &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: flags,
	}
}

// isAccessDeniedSpawnErr 判断 (*exec.Cmd).Start() 返回的错误是不是 Windows 的
// ERROR_ACCESS_DENIED——即请求了 CREATE_BREAKAWAY_FROM_JOB、但父 Job Object 没开
// JOB_OBJECT_LIMIT_BREAKAWAY_OK 时 CreateProcess 返回的错误。
func isAccessDeniedSpawnErr(err error) bool {
	return errors.Is(err, syscall.ERROR_ACCESS_DENIED)
}

// notifyShutdownContext 在父 ctx 上监听 os.Interrupt 与 SIGBREAK，任一信号触发取消。
func notifyShutdownContext(parent context.Context) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(parent, os.Interrupt, sigBreak)
}

// repointStdioToErrLog 释放旧版 self-update launcher 可能继承到本进程
// stdout/stderr 上的 daemon.log 句柄，再把有界的崩溃 sink 设为进程标准输出/错误。
// Go 打开文件不带 FILE_SHARE_DELETE，只要那个继承句柄还开着，轮转 writer 的
// 改名-on-rotate 就永久失败——直到下次干净重启。关闭 os.Stdout/os.Stderr 会放下
// 继承句柄；SetStdHandle 随后重绑标准句柄，让 panic 与裸写仍落进 daemon.err.log。
// 尽力而为：任一步失败就把 stdio 保持继承原状。
func repointStdioToErrLog(errLogPath string) {
	f, err := openBoundedErrLog(errLogPath)
	if err != nil {
		return
	}
	_ = os.Stdout.Close()
	_ = os.Stderr.Close()
	h := windows.Handle(f.Fd())
	_ = windows.SetStdHandle(windows.STD_OUTPUT_HANDLE, h)
	_ = windows.SetStdHandle(windows.STD_ERROR_HANDLE, h)
	os.Stdout = f
	os.Stderr = f
}

// openLogForTail 带 FILE_SHARE_DELETE 打开日志供读取（Go 的 os.Open 不带这个）。
// 没有删除共享，长时间存活的读者——`daemon logs -f`——会挡住轮转器的改名，
// 在 Windows 上把轮转悄悄冻住。
func openLogForTail(path string) (*os.File, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(
		p,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(h), path), nil
}

func tailLogFile(logPath string, lines int, follow bool) error {
	f, err := openLogForTail(logPath)
	if err != nil {
		return err
	}
	defer func() { f.Close() }()

	fi, err := f.Stat()
	if err != nil {
		return err
	}
	size := fi.Size()

	// 从 EOF 反向扫描定位最后 N 行的起点。
	var tailStart int64
	if size > 0 {
		scanBuf := make([]byte, 8192)
		nlCount := 0
		pos := size
	scan:
		for pos > 0 {
			chunk := int64(len(scanBuf))
			if chunk > pos {
				chunk = pos
			}
			pos -= chunk
			f.ReadAt(scanBuf[:chunk], pos)
			for i := chunk - 1; i >= 0; i-- {
				if scanBuf[i] == '\n' {
					nlCount++
					if nlCount > lines {
						tailStart = pos + i + 1
						break scan
					}
				}
			}
		}
	}

	if _, err := f.Seek(tailStart, io.SeekStart); err != nil {
		return err
	}
	if _, err := io.Copy(os.Stdout, f); err != nil {
		return err
	}

	if !follow {
		return nil
	}

	offset, _ := f.Seek(0, io.SeekCurrent)
	buf := make([]byte, 4096)
	for {
		time.Sleep(500 * time.Millisecond)
		// 探测轮转：轮转器把我们的文件改名腾走、又在同一路径建了个更小的新文件。
		// 我们的句柄还指向被轮转走的文件（永远 EOF），于是按路径重开、从头再来——
		// 镜像 Unix `tail -F` 的行为，也对应桌面版 tail 按「尺寸变小」复位的逻辑。
		if fi, statErr := os.Stat(logPath); statErr == nil && fi.Size() < offset {
			if nf, reopenErr := openLogForTail(logPath); reopenErr == nil {
				f.Close()
				f = nf
				offset = 0
			}
		}
		n, readErr := f.Read(buf)
		if n > 0 {
			os.Stdout.Write(buf[:n])
			offset += int64(n)
		}
		if readErr != nil && readErr != io.EOF {
			return readErr
		}
	}
}
