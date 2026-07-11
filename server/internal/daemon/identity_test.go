package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestEnsureDaemonID_Persists(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	first, err := EnsureDaemonID()
	if err != nil {
		t.Fatalf("EnsureDaemonID first call: %v", err)
	}
	if _, err := uuid.Parse(first); err != nil {
		t.Fatalf("EnsureDaemonID returned non-UUID: %q", first)
	}

	path := filepath.Join(home, ".multica", "daemon.id")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("daemon.id not written: %v", err)
	}
	if strings.TrimSpace(string(data)) != first {
		t.Fatalf("file contents %q differ from returned UUID %q", data, first)
	}

	second, err := EnsureDaemonID()
	if err != nil {
		t.Fatalf("EnsureDaemonID second call: %v", err)
	}
	if second != first {
		t.Fatalf("UUID changed on second call: %q → %q", first, second)
	}
}

func TestEnsureDaemonID_IgnoresProfileScopedFiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	profileID := uuid.Must(uuid.NewV7()).String()
	profileDir := filepath.Join(home, ".multica", "profiles", "staging")
	if err := os.MkdirAll(profileDir, 0o755); err != nil {
		t.Fatalf("mkdir profile: %v", err)
	}
	profileFile := filepath.Join(profileDir, "daemon.id")
	if err := os.WriteFile(profileFile, []byte(profileID+"\n"), 0o600); err != nil {
		t.Fatalf("seed profile id: %v", err)
	}

	got, err := EnsureDaemonID()
	if err != nil {
		t.Fatalf("EnsureDaemonID: %v", err)
	}
	if got == profileID {
		t.Fatalf("machine identity must not be sourced from profile file %s", profileFile)
	}

	data, err := os.ReadFile(filepath.Join(home, ".multica", "daemon.id"))
	if err != nil {
		t.Fatalf("read canonical file: %v", err)
	}
	if strings.TrimSpace(string(data)) != got {
		t.Fatalf("canonical file %q != generated id %q", data, got)
	}
}

func TestEnsureDaemonID_RegeneratesCorruptFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	dir := filepath.Join(home, ".multica")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, "daemon.id")
	if err := os.WriteFile(path, []byte("not-a-uuid"), 0o600); err != nil {
		t.Fatalf("seed corrupt file: %v", err)
	}

	id, err := EnsureDaemonID()
	if err != nil {
		t.Fatalf("EnsureDaemonID: %v", err)
	}
	if _, err := uuid.Parse(id); err != nil {
		t.Fatalf("expected valid UUID, got %q", id)
	}

	data, _ := os.ReadFile(path)
	if strings.TrimSpace(string(data)) != id {
		t.Fatalf("file not rewritten with new UUID")
	}
}
