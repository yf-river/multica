//go:build linux

package execenv

import (
	"io"
	"os"
	"os/exec"
	"testing"
)

func TestCodexNativeStateRejectsDeclaredExternalOwner(t *testing.T) {
	home, other := t.TempDir(), t.TempDir()
	cmd := exec.CommandContext(t.Context(), "/bin/sh", "-c", "printf ready; read value")
	cmd.Env = append(os.Environ(), "CODEX_HOME="+home)
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = input.Close(); _ = cmd.Wait() }()
	if _, err := io.ReadFull(output, make([]byte, 5)); err != nil {
		t.Fatal(err)
	}
	if err := ensureCodexNativeStateIdle(home); err == nil {
		t.Fatal("migration accepted an external process using its source home")
	}
	if err := ensureCodexNativeStateIdle(other); err != nil {
		t.Fatal("unrelated process blocked another task home")
	}
}
