//go:build linux

package agent

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

// startEscapee 起一个"另起进程组"的后代，模拟工具 setsid 之后脱离 CLI 组的情形。
func startEscapee(t *testing.T, taskID string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("setsid", "sleep", "60")
	cmd.Env = append(os.Environ(), "MULTICA_TASK_ID="+taskID)
	if err := cmd.Start(); err != nil {
		t.Skipf("setsid unavailable: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
	return cmd
}

func waitGone(pid int, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if !ownedProcessAlive(pid) {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

func TestReapTaskEscapeesTerminatesOwnedEscapee(t *testing.T) {
	cli := startEscapee(t, "task-cli")
	identity := captureCLIIdentity(cli.Process.Pid)
	if !identity.Valid {
		t.Fatal("cli identity must be captured while the process is alive")
	}
	time.Sleep(25 * time.Millisecond)
	owned := startEscapee(t, "task-owned")
	// 让它真正跑起来再判定。
	time.Sleep(50 * time.Millisecond)

	n := reapTaskEscapees("task-owned", identity, nil)
	if n == 0 {
		t.Fatal("owned escapee should have been reaped")
	}
	if !waitGone(owned.Process.Pid, 2*time.Second) {
		t.Fatalf("owned escapee %d survived", owned.Process.Pid)
	}
}

// 关键控制：同一个 UID、但属于别的任务的进程，绝不能被杀。
func TestReapTaskEscapeesLeavesOtherTasksAlone(t *testing.T) {
	cli := startEscapee(t, "task-cli")
	identity := captureCLIIdentity(cli.Process.Pid)
	if !identity.Valid {
		t.Fatal("cli identity must be captured while the process is alive")
	}
	time.Sleep(25 * time.Millisecond)
	other := startEscapee(t, "task-other")
	time.Sleep(50 * time.Millisecond)

	reapTaskEscapees("task-owned", identity, nil)
	time.Sleep(200 * time.Millisecond)
	if !ownedProcessAlive(other.Process.Pid) {
		t.Fatalf("process of another task was killed: %d", other.Process.Pid)
	}
}

// 比 CLI 更早启动的同任务进程不属于本次运行，不能被回收。
func TestReapTaskEscapeesLeavesOlderProcessesAlone(t *testing.T) {
	older := startEscapee(t, "task-owned")
	time.Sleep(30 * time.Millisecond)
	cli := startEscapee(t, "task-cli")
	identity := captureCLIIdentity(cli.Process.Pid)
	if !identity.Valid {
		t.Fatal("identity must be readable")
	}
	reapTaskEscapees("task-owned", identity, nil)
	time.Sleep(150 * time.Millisecond)
	if !ownedProcessAlive(older.Process.Pid) {
		t.Fatalf("process started before the CLI must survive: %d", older.Process.Pid)
	}
}
