//go:build linux

package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const codexPoolOwnerEnv = "MULTICA_RUNTIME_PROCESS_OWNER"
const codexExecutionOwnerEnv = "MULTICA_RUNTIME_EXECUTION_ID"

type codexPoolProcessRecord struct {
	Identity  CLIIdentity `json:"identity"`
	Owner     string      `json:"owner"`
	Socket    string      `json:"socket"`
	Home      string      `json:"home"`
	TaskID    string      `json:"task_id,omitempty"`
	Execution string      `json:"execution,omitempty"`
}

func lockCodexPoolRoot(root string) (io.Closer, error) {
	file, err := os.OpenFile(filepath.Join(root, ".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, errors.New("Codex process pool already has an owner")
	}
	return file, nil
}

func configureCodexResidentOwner(cmd *exec.Cmd) {
	cmd.SysProcAttr.Pdeathsig = syscall.SIGKILL
}

func writeCodexPoolProcessRecord(dir, socket, home, owner, taskID, execution string, pid int) error {
	record := codexPoolProcessRecord{Owner: owner, Socket: socket, Home: home, TaskID: taskID, Execution: execution}
	if pid > 0 {
		record.Identity = captureCLIIdentity(pid)
		if !record.Identity.Valid {
			return errors.New("cannot verify Codex resident process identity")
		}
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, "process.json")
	if err := os.WriteFile(path+".tmp", data, 0600); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

// 恢复只回收携带本池随机所有权标识的进程；PID 和启动时刻均复核，不能误杀复用 PID。
func recoverCodexPoolProcesses(root string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		data, err := os.ReadFile(filepath.Join(dir, "process.json"))
		if errors.Is(err, os.ErrNotExist) {
			// 只有 mkdir 后、尚未写启动意图时留下的空目录可直接移除。
			children, readErr := os.ReadDir(dir)
			if readErr != nil || len(children) != 0 {
				return errors.New("Codex resident recovery found an incomplete process record")
			}
			_ = os.Remove(dir)
			continue
		}
		if err != nil {
			return err
		}
		var record codexPoolProcessRecord
		if json.Unmarshal(data, &record) != nil || len(record.Owner) != 32 || !validCodexPoolSocket(record.Socket) {
			return errors.New("Codex resident recovery found an invalid process record")
		}
		if record.Execution != "" {
			if err := reapCodexResidentExecution(record.Execution); err != nil {
				return err
			}
		}
		deadline := time.Now().Add(3 * time.Second)
		for {
			targets, err := codexPoolOwnedProcesses(record.Owner)
			if err != nil {
				return err
			}
			if len(targets) == 0 {
				break
			}
			for _, target := range targets {
				if !codexPoolProcessMatches(target, record.Owner) {
					continue
				}
				if target.PID == record.Identity.PID && target.StartTicks == record.Identity.StartTicks {
					_ = syscall.Kill(-target.PID, syscall.SIGKILL)
				} else {
					_ = syscall.Kill(target.PID, syscall.SIGKILL)
				}
			}
			if time.Now().After(deadline) {
				return errors.New("Codex resident recovery could not confirm process cleanup")
			}
			time.Sleep(20 * time.Millisecond)
		}
		if err := os.RemoveAll(filepath.Dir(record.Socket)); err != nil {
			return err
		}
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
	}
	return nil
}

func validCodexPoolSocket(socket string) bool {
	dir := filepath.Dir(socket)
	return filepath.Clean(socket) == socket && filepath.Base(socket) == "rpc.sock" &&
		filepath.Dir(dir) == os.TempDir() && strings.HasPrefix(filepath.Base(dir), "multica-codex-")
}

func codexPoolProcessMatches(identity CLIIdentity, owner string) bool {
	return codexOwnedProcessMatches(identity, codexPoolOwnerEnv, owner)
}

func codexOwnedProcessMatches(identity CLIIdentity, key, owner string) bool {
	current := captureCLIIdentity(identity.PID)
	if !current.Valid || current.StartTicks != identity.StartTicks || current.UID != os.Getuid() || !ownedProcessAlive(identity.PID) {
		return false
	}
	env, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", identity.PID))
	if err != nil {
		return false
	}
	want := []byte(key + "=" + owner)
	for _, field := range bytes.Split(env, []byte{0}) {
		if bytes.Equal(field, want) {
			return true
		}
	}
	return false
}

func codexPoolOwnedProcesses(owner string) ([]CLIIdentity, error) {
	return codexOwnedProcesses(codexPoolOwnerEnv, owner)
}

func codexOwnedProcesses(key, owner string) ([]CLIIdentity, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var identities []CLIIdentity
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		identity := captureCLIIdentity(pid)
		if codexOwnedProcessMatches(identity, key, owner) {
			identities = append(identities, identity)
		}
	}
	return identities, nil
}

// 卸载后仍存活的工具进程只按随机轮次标识回收，不按可复用的任务 ID 推断归属。
func reapCodexResidentExecution(execution string) error {
	return reapCodexOwnedProcesses(codexExecutionOwnerEnv, execution)
}
func reapCodexResidentOwner(owner string) error {
	return reapCodexOwnedProcesses(codexPoolOwnerEnv, owner)
}
func reapCodexOwnedProcesses(key, execution string) error {
	deadline := time.Now().Add(time.Second)
	for {
		targets, err := codexOwnedProcesses(key, execution)
		if err != nil {
			return err
		}
		if len(targets) == 0 {
			return nil
		}
		for _, target := range targets {
			if codexOwnedProcessMatches(target, key, execution) {
				_ = syscall.Kill(target.PID, syscall.SIGKILL)
			}
		}
		if time.Now().After(deadline) {
			return errors.New("Codex execution processes did not exit after unload")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
