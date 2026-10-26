//go:build linux

package agent

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

type codebuddyPoolRecord struct {
	Identity  CLIIdentity `json:"identity"`
	Owner     string      `json:"owner"`
	Hooks     string      `json:"hooks"`
	TaskID    string      `json:"task_id,omitempty"`
	Execution string      `json:"execution,omitempty"`
}

func writeCodebuddyPoolRecord(dir, hooks, owner, task, execution string, pid int) error {
	record := codebuddyPoolRecord{Owner: owner, Hooks: hooks, TaskID: task, Execution: execution}
	if pid > 0 {
		record.Identity = captureCLIIdentity(pid)
		if !record.Identity.Valid {
			return errors.New("cannot verify CodeBuddy resident process identity")
		}
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(dir, "process.json.tmp"), raw, 0600); err != nil {
		return err
	}
	return os.Rename(filepath.Join(dir, "process.json.tmp"), filepath.Join(dir, "process.json"))
}

func recoverCodebuddyPoolProcesses(root string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		raw, err := os.ReadFile(filepath.Join(dir, "process.json"))
		if errors.Is(err, os.ErrNotExist) {
			children, readErr := os.ReadDir(dir)
			if readErr != nil || len(children) != 0 {
				return errors.New("CodeBuddy resident recovery found an incomplete owner record")
			}
			_ = os.Remove(dir)
			continue
		}
		if err != nil {
			return err
		}
		var record codebuddyPoolRecord
		if json.Unmarshal(raw, &record) != nil || len(record.Owner) != 32 || filepath.Clean(record.Hooks) != record.Hooks || filepath.Dir(record.Hooks) != os.TempDir() || !strings.HasPrefix(filepath.Base(record.Hooks), "multica-cb-hooks-") {
			return errors.New("CodeBuddy resident recovery found an invalid owner record")
		}
		if record.Execution != "" {
			if err := reapCodexResidentExecution(record.Execution); err != nil {
				return err
			}
		}
		if err := reapCodexResidentOwner(record.Owner); err != nil {
			return err
		}
		if err := os.RemoveAll(record.Hooks); err != nil {
			return err
		}
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
	}
	return nil
}
