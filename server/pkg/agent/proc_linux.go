//go:build linux

package agent

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// bindAgentProcessToParent asks the kernel to terminate the runtime process
// when the daemon dies abruptly. Runtime CLIs receive SIGTERM so they can also
// stop tool subprocesses before exiting.
func bindAgentProcessToParent(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Pdeathsig = syscall.SIGTERM
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		for _, pid := range agentProcessDescendants(cmd.Process.Pid) {
			_ = syscall.Kill(pid, syscall.SIGTERM)
		}
		err := cmd.Process.Signal(syscall.SIGTERM)
		if errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
}

func agentProcessDescendants(rootPID int) []int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	all := make([]int, 0, len(entries))
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err == nil {
			all = append(all, pid)
		}
	}
	parents := map[int]bool{rootPID: true}
	descendants := []int{}
	for changed := true; changed; {
		changed = false
		for _, pid := range all {
			if parents[pid] || !parents[processParentPID(pid)] {
				continue
			}
			parents[pid] = true
			descendants = append(descendants, pid)
			changed = true
		}
	}
	return descendants
}

func processParentPID(pid int) int {
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "status"))
	if err != nil {
		return 0
	}
	for line := range strings.SplitSeq(string(raw), "\n") {
		if !strings.HasPrefix(line, "PPid:") {
			continue
		}
		parent, _ := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "PPid:")))
		return parent
	}
	return 0
}
