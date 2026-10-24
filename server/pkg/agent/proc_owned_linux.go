//go:build linux

package agent

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// reapTaskEscapees 回收"本任务拥有、但已经脱离 CLI 进程组"的后代。
//
// 背景：CLI 只是组首进程，工具可以 setsid 另起进程组（实测 sh/sleep 的 pgid 与
// CLI 不同）。CLI 异常死亡后，对负 pid 发组信号覆盖不到它们，它们会一直跑。
//
// 归属判定必须够硬，否则就是误杀：同 UID + 进程环境里 MULTICA_TASK_ID 正好是本
// 任务 + 启动时刻不早于 CLI。环境变量只在内存里读这两个字段，不打印、不落盘；
// 判不准的进程一律放过。
func reapTaskEscapees(taskID string, cliPID int, logger *slog.Logger) int {
	if taskID == "" || cliPID <= 0 {
		return 0
	}
	// 归属下界用 CLI 自己的原始 startTicks，而不用墙钟时间：墙钟会被 NTP/时区/采样
	// 误差影响，tick 是内核给的、和候选进程同一坐标系。
	cliTicks, ok := processStartTicks(cliPID)
	if !ok {
		return 0
	}
	targets := ownedTaskProcesses(taskID, cliTicks)
	if len(targets) == 0 {
		return 0
	}
	for _, pid := range targets {
		_ = syscall.Kill(pid, syscall.SIGTERM)
	}
	// 有界等待：不拉长 Result/记账的总预算，最多 150ms 就升级到 KILL。
	deadline := time.Now().Add(150 * time.Millisecond)
	for time.Now().Before(deadline) {
		if !anyOwnedAlive(targets) {
			return len(targets)
		}
		time.Sleep(10 * time.Millisecond)
	}
	killed := 0
	for _, pid := range targets {
		if ownedProcessAlive(pid) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			killed++
		}
	}
	if logger != nil {
		logger.Info("reaped task processes that escaped the CLI process group",
			"task_id", taskID, "signalled", len(targets), "escalated", killed)
	}
	return len(targets)
}

func ownedTaskProcesses(taskID string, cliTicks uint64) []int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	euid := os.Geteuid()
	self := os.Getpid()
	var out []int
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 1 || pid == self {
			continue
		}
		if !ownedProcessAlive(pid) {
			continue
		}
		if uid, ok := processUID(pid); !ok || uid != euid {
			continue
		}
		ticks, ok := processStartTicks(pid)
		if !ok || ticks < cliTicks {
			continue
		}
		if !envHasTaskID(pid, taskID) {
			continue
		}
		out = append(out, pid)
	}
	return out
}

func ownedProcessAlive(pid int) bool {
	if syscall.Kill(pid, 0) != nil {
		return false
	}
	// 僵尸进程对 signal 0 仍然"存在"，但它已经跑完了，不能算还活着。
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return true
	}
	idx := bytes.LastIndexByte(raw, ')')
	if idx < 0 || idx+2 > len(raw) {
		return true
	}
	fields := strings.Fields(string(raw[idx+2:]))
	if len(fields) == 0 {
		return true
	}
	return fields[0] != "Z"
}

func anyOwnedAlive(pids []int) bool {
	for _, pid := range pids {
		if ownedProcessAlive(pid) {
			return true
		}
	}
	return false
}

// processUID 从 /proc/<pid>/status 读真实 UID。
func processUID(pid int) (int, bool) {
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "status"))
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "Uid:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0, false
		}
		uid, err := strconv.Atoi(fields[1])
		return uid, err == nil
	}
	return 0, false
}

// processStartTicks 取 /proc/<pid>/stat 第 22 字段：开机后 tick 数，用作进程身份下界。
func processStartTicks(pid int) (uint64, bool) {
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, false
	}
	idx := bytes.LastIndexByte(raw, ')')
	if idx < 0 || idx+2 > len(raw) {
		return 0, false
	}
	fields := strings.Fields(string(raw[idx+2:]))
	if len(fields) < 20 {
		return 0, false
	}
	ticks, err := strconv.ParseUint(fields[19], 10, 64)
	return ticks, err == nil
}

// processStartTime 从 /proc/<pid>/stat 的第 22 个字段取启动时刻（开机后 tick 数）。
func processStartTime(pid int) (time.Time, bool) {
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return time.Time{}, false
	}
	// comm 字段可能含空格和括号，从最后一个 ')' 之后切。
	idx := bytes.LastIndexByte(raw, ')')
	if idx < 0 || idx+2 > len(raw) {
		return time.Time{}, false
	}
	fields := strings.Fields(string(raw[idx+2:]))
	if len(fields) < 20 {
		return time.Time{}, false
	}
	ticks, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	return bootTime().Add(time.Duration(ticks) * (time.Second / time.Duration(clockTicks()))), true
}

func bootTime() time.Time {
	raw, err := os.ReadFile("/proc/stat")
	if err != nil {
		return time.Now()
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "btime ") {
			continue
		}
		secs, err := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(line, "btime ")), 10, 64)
		if err != nil {
			return time.Now()
		}
		return time.Unix(secs, 0)
	}
	return time.Now()
}

func clockTicks() int {
	// Linux 用户态几乎总是 100Hz；这里只用它做"不早于 CLI 启动"的粗筛。
	return 100
}

// envHasTaskID 只在内存里查一个变量，匹配到即返回，绝不复述整个环境。
func envHasTaskID(pid int, taskID string) bool {
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "environ"))
	if err != nil {
		return false
	}
	needle := []byte("MULTICA_TASK_ID=" + taskID)
	for _, kv := range bytes.Split(raw, []byte{0}) {
		if bytes.Equal(kv, needle) {
			return true
		}
	}
	return false
}
