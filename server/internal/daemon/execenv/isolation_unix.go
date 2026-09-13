//go:build !windows

// 进程树控制器（Unix 实现）：利用"进程组"把 helper 连同它派生的 CLI
// 一起强杀。结构体本身为空，因为 Unix 侧只需在启动前设置 Setpgid。

package execenv

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

type preparationProcessController struct{}

// newPreparationProcessController 在启动前把子进程放进一个新的进程组
// （Setpgid），这样 stop 就能用 Kill(-pgid) 一次杀掉整棵进程树。
func newPreparationProcessController(cmd *exec.Cmd) (*preparationProcessController, error) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	return &preparationProcessController{}, nil
}

// attach 在 Unix 上无事可做：进程组归属在启动前就已通过 Setpgid 定好。
func (*preparationProcessController) attach(_ *exec.Cmd) error { return nil }

// stop 向整个进程组（负 PID 即组 ID）发 SIGKILL。即使 helper 正阻塞在内核
// 文件系统调用里，SIGKILL 已挂起后，等该调用最终返回时它也无法再回到 Go
// 侧做下一次写入。
func (*preparationProcessController) stop(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return os.ErrProcessDone
	}
	err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		// 进程组已不存在（进程已退出），视为成功停止。
		return nil
	}
	return err
}

func (*preparationProcessController) finish() error { return nil }
func (*preparationProcessController) close()        {}
