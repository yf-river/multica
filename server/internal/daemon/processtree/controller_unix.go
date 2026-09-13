//go:build !windows

package processtree

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

const processTreeFinishTimeout = 5 * time.Second

type controller struct{}

// newController 让子进程自领一个进程组（Setpgid），这样组号恒等于领导者 PID，
// 之后对 -pid 发信号即可命中整棵进程树，而不是只杀到 leader 一个进程。
func newController(cmd *exec.Cmd) (*controller, error) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	return &controller{}, nil
}

// attach 在 Unix 上无事可做：进程组在 Start 时已由 Setpgid 建立好了。
func (*controller) attach(_ *exec.Cmd) error { return nil }

func (*controller) interrupt(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return os.ErrProcessDone
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
	return nil
}

// stop 直接 SIGKILL 整个进程组。若组已不存在（ESRCH）按成功算；理论上进程组
// 找不到但 leader 进程本体还在时，退回杀单个进程，避免漏网。
func (*controller) stop(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return os.ErrProcessDone
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return cmd.Process.Kill()
	}
	return nil
}

// finish 收尾：等整组进程真正退出（最多 processTreeFinishTimeout），超时就报错
// 让上层知道还有进程没清掉。
func (c *controller) finish(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	pid := cmd.Process.Pid
	if err := syscall.Kill(-pid, 0); errors.Is(err, syscall.ESRCH) {
		return nil
	}
	// 领导者即使正常退出，仍可能有后裔进程握着继承下来的管道或仓库锁。先把
	// 残留的进程组杀干净，再把仓库归还给其他操作，避免死锁/占用。
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	deadline := time.Now().Add(processTreeFinishTimeout)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(-pid, 0); errors.Is(err, syscall.ESRCH) {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("process group %d still active after %s", pid, processTreeFinishTimeout)
}

func (*controller) close() {}
