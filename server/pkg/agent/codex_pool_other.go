//go:build !linux

package agent

import (
	"errors"
	"io"
	"os/exec"
)

const codexPoolOwnerEnv = "MULTICA_RUNTIME_PROCESS_OWNER"
const codexExecutionOwnerEnv = "MULTICA_RUNTIME_EXECUTION_ID"

func lockCodexPoolRoot(string) (io.Closer, error) {
	return nil, errors.New("resident Codex processes require Linux process ownership")
}
func configureCodexResidentOwner(*exec.Cmd)  {}
func recoverCodexPoolProcesses(string) error { return nil }
func writeCodexPoolProcessRecord(string, string, string, string, string, string, int) error {
	return errors.New("resident Codex processes require Linux process ownership")
}

func reapCodexResidentExecution(string) error {
	return errors.New("resident Codex execution requires Linux")
}

func reapCodexResidentOwner(string) error {
	return errors.New("resident Codex execution requires Linux")
}
