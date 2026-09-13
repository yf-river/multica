//go:build windows

package agent

// 文件职责：Windows 平台下"以进程组方式拥有并清理 agent 子进程树"的实现。
//
// Unix 靠进程组 + 信号，Windows 没有对应物，改用 Job Object：进程以 CREATE_SUSPENDED
// 挂起启动，赶在它执行任何一条指令前被塞进 Job Object（Windows 只把"创建晚于
// 分配"的进程算作成员、绝不追溯），再恢复运行。这样整棵树共享 KILL_ON_JOB_CLOSE
// 生命周期：清理时 TerminateJobObject 一次杀光，daemon 崩溃也不会让 agent 后代
// 进程残留。startOwnedProcessTree 是本包启动运行时进程的唯一入口（有个测试专门
// 守着它，防止新 backend 偷偷改回 cmd.Start 而复活 GH #7522）。
//
// 平台差异：SIGTERM/SIGKILL 语义在 Windows 上不存在，signalProcessGroup 因此忽略
// 信号参数、直接终止 Job Object。本文件与 proc_other.go 互斥编译。

import (
	"fmt"
	"log/slog"
	"os/exec"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// createNewConsole 给子进程分配一个全新的控制台。配合 HideWindow=true
// （STARTF_USESHOWWINDOW + SW_HIDE）窗口不显示；关键的是 agent 再派生的孙进程
// （bash、cmd、netstat、findstr 等工具子进程）会继承这个隐藏控制台，而不是各自
// 再开一个看得见的窗口。
//
// 这里不能用 CREATE_NO_WINDOW——那会把控制台整个去掉，结果凡是没有自带
// CREATE_NO_WINDOW 的控制台子系统孙进程，Windows 都会强制给它们各自新开一个
// 可见控制台——正是 #1521 报告的那场弹窗风暴。
const createNewConsole = 0x00000010

// createSuspended 让子进程以"初始线程挂起"状态启动，从而可以在它执行任何一条
// 指令之前放进 Job Object。
const createSuspended = 0x00000004

// hideAgentWindow 配置 cmd 在 Windows 上隐藏控制台窗口，同时仍给后代进程一个
// 可继承的隐藏控制台。cmd.StdoutPipe/StdinPipe 设的 stdio 管道照常工作，因为
// STARTF_USESTDHANDLES 优先于新控制台的 stdio。
func hideAgentWindow(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= createNewConsole
}

// configureProcessGroup 在 Windows 上是空操作：没有 Setpgid 的对应物，进程存在
// 之前也无法请求加入 Job。拥有关系由 startOwnedProcessTree 承担——每个 backend
// 都会走到它，因为它是本包启动运行时进程的唯一入口。
func configureProcessGroup(cmd *exec.Cmd) {}

// ownedProcessTree 是 Unix 进程组在 Windows 上的对应物：一个 agent 所属的 Job
// Object，它其后创建的每个进程都会继承成员身份。
type ownedProcessTree struct {
	job windows.Handle
}

// ownedProcessTrees 以一次启动的 *exec.Cmd 为键（绝不拿 pid 当键）。pid 只在
// 进程存活期间唯一，拿"活过自己进程的一次启动"去按 pid 反查，可能查到并杀错
// 一个恰好继承了该编号的无关任务。
var (
	ownedProcessTreesMu sync.Mutex
	ownedProcessTrees   = map[*exec.Cmd]ownedProcessTree{}
)

// jobObjectBasicAccountingInformation 镜像 JOBOBJECT_BASIC_ACCOUNTING_INFORMATION。
// x/sys/windows 导出了该信息类编号，但没有对应的结构体。
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

// startOwnedProcessTree 启动 cmd，并接管它其后创建的所有进程。它是本包启动
// 运行时进程的唯一入口；TestOnlyOwnedProcessTreesAreStarted 守在这里，防止某个
// 新 backend 改走 cmd.Start 而悄悄复活 GH #7522。
//
// 子进程以挂起状态创建，先塞进 Job Object、再开始运行。这个顺序正是全部要点：
// Windows 只把"分配之后才创建"的进程计入成员，绝不追溯。用普通 Start 之后再分配，
// 会留出时间窗让直接子进程——通常是包着 .cmd shim 的 cmd.exe——在 job 之外就把
// 真正的 agent 拉起来。那比"什么都不拥有"更糟：job 里只会有一个立刻退出的包装
// 进程，记账系统会以为树是空的、而实际跑着的 app-server 还活着，清理也被误报为
// 已确认。
//
// 子进程绝不会一直挂着。拿不到拥有权就照样恢复运行、以无主状态跑（与引入 Job
// Object 之前一样），并把原因写进日志。只有"恢复运行失败"不可挽救：把该子进程
// 杀掉并返回错误，让调用方判启动失败，而不是对着一个永远不会跑的进程干等。
func startOwnedProcessTree(cmd *exec.Cmd, logger *slog.Logger) error {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= createSuspended

	if err := cmd.Start(); err != nil {
		return err
	}

	if err := ownProcessTree(cmd); err != nil && logger != nil {
		// 刻意"失败即放行"。若因此判启动失败，等于让整台宿主陪葬而不是降级；
		// 而且现实中的成因多半是环境性的——外层 job 禁止再分配、或安全策略拒绝
		// PROCESS_SET_QUOTA——不是某个任务的问题。后果写进消息里，因为这是运维
		// 能收到的唯一信号：本机上的取消又退化回"只杀直接子进程"了。
		logger.Warn("could not take ownership of the agent process tree; cancelling or timing out this "+
			"process will kill only the direct child and can leave its descendants running",
			"error", err, "pid", cmd.Process.Pid, "executable", cmd.Path)
	}

	if err := resumeProcess(cmd.Process.Pid); err != nil {
		// 子进程跑不起来，后面干什么都不会成功。先丢拥有权：关掉 job 会终止那个
		// 挂起的子进程；Kill 覆盖"从未拿到拥有权"的情况。
		releaseProcessGroup(cmd)
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return fmt.Errorf("resume suspended child: %w", err)
	}
	return nil
}

// ownProcessTree 创建 Job Object 并把（仍挂起的）子进程分配进去。进程句柄只在
// 分配时用得到，随即关闭：终止与记账都走 job。
func ownProcessTree(cmd *exec.Cmd) error {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return fmt.Errorf("create job object: %w", err)
	}
	// KILL_ON_JOB_CLOSE 让整棵树随最后一个句柄一起死——daemon 崩溃不会抛下 agent
	// 后代。代价是 releaseProcessGroup 必须等树用完才能跑。
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		_ = windows.CloseHandle(job)
		return fmt.Errorf("set KILL_ON_JOB_CLOSE: %w", err)
	}
	process, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE,
		false,
		uint32(cmd.Process.Pid),
	)
	if err != nil {
		_ = windows.CloseHandle(job)
		return fmt.Errorf("open process: %w", err)
	}
	defer windows.CloseHandle(process)
	if err := windows.AssignProcessToJobObject(job, process); err != nil {
		_ = windows.CloseHandle(job)
		return fmt.Errorf("assign process to job object: %w", err)
	}

	ownedProcessTreesMu.Lock()
	defer ownedProcessTreesMu.Unlock()
	ownedProcessTrees[cmd] = ownedProcessTree{job: job}
	return nil
}

// resumeProcess 释放 CREATE_SUSPENDED 子进程的初始线程。新建进程恰好只有一个
// 线程；快照按属主 pid 过滤，避免并发枚举时把别人的线程也恢复掉。
func resumeProcess(pid int) error {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return fmt.Errorf("snapshot threads: %w", err)
	}
	defer windows.CloseHandle(snapshot)

	var entry windows.ThreadEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	resumed := 0
	for err = windows.Thread32First(snapshot, &entry); err == nil; err = windows.Thread32Next(snapshot, &entry) {
		if entry.OwnerProcessID != uint32(pid) {
			continue
		}
		thread, openErr := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
		if openErr != nil {
			return fmt.Errorf("open thread %d: %w", entry.ThreadID, openErr)
		}
		_, resumeErr := windows.ResumeThread(thread)
		_ = windows.CloseHandle(thread)
		if resumeErr != nil {
			return fmt.Errorf("resume thread %d: %w", entry.ThreadID, resumeErr)
		}
		resumed++
	}
	if resumed == 0 {
		return fmt.Errorf("no threads found for pid %d", pid)
	}
	return nil
}

// releaseProcessGroup 放弃一棵已结束进程树的拥有权。关掉 job 句柄就是杀掉仍在
// 里面的任何进程，所以它必须只在调用方已收尸之后执行——绝不能走在可能仍在服务
// 存活 agent 的路径上。
func releaseProcessGroup(cmd *exec.Cmd) {
	ownedProcessTreesMu.Lock()
	tree, ok := ownedProcessTrees[cmd]
	delete(ownedProcessTrees, cmd)
	ownedProcessTreesMu.Unlock()
	if ok {
		_ = windows.CloseHandle(tree.job)
	}
}

func lookupProcessTree(cmd *exec.Cmd) (ownedProcessTree, bool) {
	ownedProcessTreesMu.Lock()
	defer ownedProcessTreesMu.Unlock()
	tree, ok := ownedProcessTrees[cmd]
	return tree, ok
}

// activeProcesses 查询 job 里当前还活着的进程数（记账信息）。
func (t ownedProcessTree) activeProcesses() (uint32, error) {
	var info jobObjectBasicAccountingInformation
	if err := windows.QueryInformationJobObject(
		t.job,
		windows.JobObjectBasicAccountingInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
		nil,
	); err != nil {
		return 0, fmt.Errorf("query job object members: %w", err)
	}
	return info.ActiveProcesses, nil
}

// codexInitializeRetrySupported 报告"后代进程是否已被终止"能否被确证。把树拥有在
// Job Object 里就能确证；没拿到拥有权时 waitProcessGroupGone 返回 false，调用方的
// cleanup_confirmed 门也会把重试按掉。
func codexInitializeRetrySupported() bool { return true }

// signalProcessGroup 终止整棵被拥有的进程树。Windows 没有 SIGTERM/SIGKILL 之分、
// 也没有进程组信号，所以忽略信号参数、改为终止 Job Object；调用方的宽限窗口仍然
// 在（直到带 SIGKILL 调它之前都有效）。没有拥有树时退化为只杀直接子进程。
func signalProcessGroup(cmd *exec.Cmd, _ syscall.Signal) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	if tree, ok := lookupProcessTree(cmd); ok {
		if err := windows.TerminateJobObject(tree.job, 1); err == nil {
			return
		}
	}
	_ = cmd.Process.Kill()
}

// waitProcessGroupGone 报告被拥有的树是否已全部消失——轮询 job 的记账信息直到
// 超时。没有拥有树就没什么可观察的，返回 false——调用方把它当"清理未能确证"，
// 而不是成功。
func waitProcessGroupGone(cmd *exec.Cmd, timeout time.Duration) bool {
	if cmd == nil || cmd.Process == nil {
		return false
	}
	tree, ok := lookupProcessTree(cmd)
	if !ok {
		return false
	}
	deadline := time.Now().Add(timeout)
	for {
		active, err := tree.activeProcesses()
		if err != nil {
			return false
		}
		if active == 0 {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
}
