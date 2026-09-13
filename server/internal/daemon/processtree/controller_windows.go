//go:build windows

package processtree

import (
	"fmt"
	"os/exec"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const processTreeFinishTimeout = 5 * time.Second

type controller struct {
	job windows.Handle
}

type jobObjectBasicAccountingInformation struct {
	TotalUserTime             int64
	TotalKernelTime           int64
	ThisPeriodTotalUserTime   int64
	ThisPeriodTotalKernelTime int64
	TotalPageFaultCount       uint32
	TotalProcesses            uint32
	ActiveProcesses           uint32
	TotalTerminatedProcesses  uint32
}

// newController 建一个带 KILL_ON_JOB_CLOSE 的 Job Object：job 句柄一旦关闭，
// 系统会连带杀光 job 内所有进程——这把"进程树随命令一起消亡"的保证交给了内核。
func newController(cmd *exec.Cmd) (*controller, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		windows.CloseHandle(job)
		return nil, fmt.Errorf("set KILL_ON_JOB_CLOSE: %w", err)
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	return &controller{job: job}, nil
}

func (c *controller) attach(cmd *exec.Cmd) error {
	process, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE,
		false,
		uint32(cmd.Process.Pid),
	)
	if err != nil {
		return fmt.Errorf("open process: %w", err)
	}
	defer windows.CloseHandle(process)
	if err := windows.AssignProcessToJobObject(c.job, process); err != nil {
		return fmt.Errorf("assign process to job object: %w", err)
	}
	return nil
}

func (c *controller) stop(_ *exec.Cmd) error {
	if err := windows.TerminateJobObject(c.job, 1); err != nil {
		active, queryErr := c.activeProcesses()
		if queryErr == nil && active == 0 {
			return nil
		}
		return err
	}
	return nil
}

// Windows 的 Job Object 没有 SIGTERM 这种东西：终止 Job 既当"优雅尝试"也用
// 作"硬停"，二者是一回事。
func (c *controller) interrupt(cmd *exec.Cmd) error { return c.stop(cmd) }

func (c *controller) finish(_ *exec.Cmd) error {
	active, err := c.activeProcesses()
	if err != nil {
		return err
	}
	if active > 0 {
		if err := windows.TerminateJobObject(c.job, 1); err != nil {
			return err
		}
	}
	deadline := time.Now().Add(processTreeFinishTimeout)
	for time.Now().Before(deadline) {
		active, err = c.activeProcesses()
		if err != nil {
			return err
		}
		if active == 0 {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("process job still active after %s", processTreeFinishTimeout)
}

// activeProcesses 查询 Job 内当前仍存活的进程数，供 stop/finish 判断是否已清干净。
func (c *controller) activeProcesses() (uint32, error) {
	var info jobObjectBasicAccountingInformation
	if err := windows.QueryInformationJobObject(
		c.job,
		windows.JobObjectBasicAccountingInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
		nil,
	); err != nil {
		return 0, fmt.Errorf("query job object members: %w", err)
	}
	return info.ActiveProcesses, nil
}

func (c *controller) close() {
	if c.job == 0 {
		return
	}
	_ = windows.CloseHandle(c.job)
	c.job = 0
}
