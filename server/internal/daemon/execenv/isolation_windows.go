//go:build windows

// 进程树控制器（Windows 实现）：用 Job Object（作业对象）托管 helper，
// 设置 KILL_ON_JOB_CLOSE 并支持按整个作业强杀。这样无论 helper 派生出
// 多少子孙进程，都能在取消/收尾时一次性全部终止。

package execenv

import (
	"fmt"
	"os/exec"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// preparationProcessController 持有唯一的 job 句柄；所有进程都塞进这个作业。
type preparationProcessController struct {
	job windows.Handle
}

// jobObjectBasicAccountingInformation 镜像 Windows 的
// JOBOBJECT_BASIC_ACCOUNTING_INFORMATION 结构。x/sys 提供了
// QueryInformationJobObject 与信息类常量，但没有这个结果结构体，
// 所以这里自行声明（字段布局必须与内核定义严格一致）。
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

// newPreparationProcessController 创建一个 Job Object，并设置
// JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE：当最后一个作业句柄关闭时，内核会
// 自动杀死作业内所有仍在运行的进程——这保证了即使漏了显式终止，进程也不会
// 泄漏成孤儿。
func newPreparationProcessController(_ *exec.Cmd) (*preparationProcessController, error) {
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
	return &preparationProcessController{job: job}, nil
}

// attach 把已启动的 helper 进程指派进 job（Windows 无法像 Unix 那样在
// 启动前用标志位完成，只能启动后按 PID 打开并 Assign）。
func (c *preparationProcessController) attach(cmd *exec.Cmd) error {
	process, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE,
		false,
		uint32(cmd.Process.Pid),
	)
	if err != nil {
		return fmt.Errorf("open helper process: %w", err)
	}
	defer windows.CloseHandle(process)
	if err := windows.AssignProcessToJobObject(c.job, process); err != nil {
		return fmt.Errorf("assign helper to job object: %w", err)
	}
	return nil
}

// stop 终止整个作业（含所有后代进程）。若 Terminate 报错但作业里其实已经
// 没有活动进程，则视为已停止。
func (c *preparationProcessController) stop(_ *exec.Cmd) error {
	if err := windows.TerminateJobObject(c.job, 1); err != nil {
		active, queryErr := c.activeProcesses()
		if queryErr == nil && active == 0 {
			return nil
		}
		return err
	}
	return nil
}

// finish 仅在直接 helper 已退出后被调用：先把它遗留的后代进程终止掉，
// 然后一直轮询到 Job Object 报告没有活动成员，daemon 才可以释放目录
// 归属并安全地重试。
func (c *preparationProcessController) finish() error {
	active, err := c.activeProcesses()
	if err != nil {
		return err
	}
	if active > 0 {
		if err := windows.TerminateJobObject(c.job, 1); err != nil {
			return err
		}
	}
	for {
		active, err = c.activeProcesses()
		if err != nil {
			return err
		}
		if active == 0 {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// activeProcesses 查询作业当前"仍活动的进程数"。
func (c *preparationProcessController) activeProcesses() (uint32, error) {
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

// close 关闭 job 句柄。由于设了 KILL_ON_JOB_CLOSE，此刻若作业内还有
// 存活进程，内核会顺带把它们全部终止。
func (c *preparationProcessController) close() {
	if c.job == 0 {
		return
	}
	_ = windows.CloseHandle(c.job)
	c.job = 0
}
