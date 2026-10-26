//go:build agentintegration

package agent

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"
)

// 真实 CLI 控制协议验收使用独立 home，不调用模型或共享可写账号目录。
func TestCodexRealGoalControl(t *testing.T) {
	if os.Getenv("MULTICA_RUN_REAL_AGENT_SMOKE") != "1" {
		t.Skip("set MULTICA_RUN_REAL_AGENT_SMOKE=1 to run native Codex control smoke")
	}
	executable := os.Getenv("MULTICA_CODEX_CONTROL_SMOKE_PATH")
	if executable == "" {
		t.Fatal("MULTICA_CODEX_CONTROL_SMOKE_PATH is required")
	}
	home, cwd := t.TempDir(), t.TempDir()
	b := &codexBackend{cfg: Config{ExecutablePath: executable, Env: map[string]string{"CODEX_HOME": home}, Logger: slog.Default()}}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	thread := ""
	for _, command := range []string{"/goal control smoke objective", "/goal show", "/goal pause", "/goal show", "/goal clear"} {
		session, err := b.Execute(ctx, "", ExecOptions{
			Cwd: cwd, Model: "gpt-6.1-sol", Command: command, CommandOnly: true, ResumeSessionID: thread, Timeout: 30 * time.Second,
		})
		if err != nil {
			t.Fatal(err)
		}
		for range session.Messages {
		}
		result := <-session.Result
		if result.Status != "completed" {
			t.Fatalf("%s: status=%s error=%s", command, result.Status, result.Error)
		}
		if result.SessionID == "" || (thread != "" && result.SessionID != thread) {
			t.Fatalf("thread continuity lost: %q -> %q", thread, result.SessionID)
		}
		thread = result.SessionID
		if command == "/goal pause" && !strings.Contains(result.Output, "已暂停") {
			t.Fatalf("goal was not paused: %s", result.Output)
		}
		if len(result.Usage) != 0 {
			t.Fatalf("command-only control unexpectedly invoked a model: %+v", result.Usage)
		}
	}
}
