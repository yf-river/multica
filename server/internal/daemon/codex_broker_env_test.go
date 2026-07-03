package daemon

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func TestPrepareCodexBrokerProcessEnvUsesTaskCodexHome(t *testing.T) {
	runtimeHome := t.TempDir()
	taskHome := t.TempDir()
	if err := os.MkdirAll(filepath.Join(runtimeHome, "skills", "runtime-skill"), 0o755); err != nil {
		t.Fatalf("create runtime skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(runtimeHome, "skills", "runtime-skill", "SKILL.md"), []byte("runtime"), 0o644); err != nil {
		t.Fatalf("write runtime skill: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(taskHome, "skills", "task-skill"), 0o755); err != nil {
		t.Fatalf("create task skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(taskHome, "skills", "task-skill", "SKILL.md"), []byte("task"), 0o644); err != nil {
		t.Fatalf("write task skill: %v", err)
	}
	t.Setenv("MULTICA_CODEX_HOME", runtimeHome)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	env, err := prepareCodexBrokerProcessEnv(map[string]string{
		"CODEX_HOME":      "old",
		"MULTICA_TOKEN":   "secret",
		"MULTICA_TASK_ID": "task",
	}, taskHome, logger)
	if err != nil {
		t.Fatalf("prepareCodexBrokerProcessEnv: %v", err)
	}
	if got := env["CODEX_HOME"]; got != taskHome {
		t.Fatalf("CODEX_HOME = %q, want task home %q", got, taskHome)
	}
	if got := env["MULTICA_CODEX_HOME"]; got != taskHome {
		t.Fatalf("MULTICA_CODEX_HOME = %q, want task home %q", got, taskHome)
	}
	if env["MULTICA_TOKEN"] != "" || env["MULTICA_TASK_ID"] != "" {
		t.Fatalf("task-scoped env leaked into broker env: %#v", env)
	}
	if env["MULTICA_CODEX_BROKER_SKILLS_HASH"] == "" {
		t.Fatal("expected task skills hash")
	}
	if _, err := os.Stat(filepath.Join(runtimeHome, "skills", "runtime-skill", "SKILL.md")); err != nil {
		t.Fatalf("runtime skills were modified: %v", err)
	}
}
