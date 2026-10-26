//go:build agentintegration

package agent

import (
	"context"
	"encoding/json"
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

func TestCodexRealNativeGoalLoop(t *testing.T) {
	if os.Getenv("MULTICA_RUN_REAL_GOAL_MODEL_SMOKE") != "1" {
		t.Skip("real goal model smoke is opt-in")
	}
	executable, home, cwd := os.Getenv("MULTICA_CODEX_CONTROL_SMOKE_PATH"), os.Getenv("MULTICA_CODEX_CONTROL_SMOKE_HOME"), os.Getenv("MULTICA_CODEX_CONTROL_SMOKE_CWD")
	if executable == "" || home == "" || cwd == "" {
		t.Fatal("explicit executable, isolated account home and work directory required")
	}
	b := &codexBackend{cfg: Config{ExecutablePath: executable, Env: map[string]string{"CODEX_HOME": home, "MULTICA_CODEX_MULTI_AGENT": "false"}, Logger: slog.Default()}}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
	defer cancel()
	objective := "Emit the exact final text CODEX_NATIVE_GOAL_OK, then mark this goal complete using update_goal. Do not call any file or command tools and do not delegate."
	session, err := b.Execute(ctx, objective, ExecOptions{Cwd: cwd, Model: "gpt-6.1-sol", ThinkingLevel: "low", ServiceTier: "default", Command: "/goal " + objective, ExtraArgs: []string{"--disable", "multi_agent"}, Timeout: 85 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	goalComplete := false
	for msg := range session.Messages {
		if msg.Type == MessageRuntime {
			var event struct {
				Kind string `json:"kind"`
				Goal struct {
					Status string `json:"status"`
				} `json:"goal"`
			}
			if json.Unmarshal([]byte(msg.Content), &event) == nil && event.Kind == "goal" && event.Goal.Status == "complete" {
				goalComplete = true
			}
		}
	}
	result := <-session.Result
	if result.Status != "completed" || !strings.Contains(result.Output, "CODEX_NATIVE_GOAL_OK") {
		t.Fatalf("native goal failed: status=%s error=%s output=%s", result.Status, result.Error, result.Output)
	}
	if !goalComplete {
		t.Fatal("task ended before native goal completed")
	}
	if len(result.Usage) == 0 {
		t.Fatal("goal execution has no reported usage")
	}
	t.Logf("Native Codex goal completed; reported_models=%d duration_ms=%d", len(result.Usage), result.DurationMs)
}

func TestCodexRealPlanModeControl(t *testing.T) {
	if os.Getenv("MULTICA_RUN_REAL_AGENT_SMOKE") != "1" {
		t.Skip("native control smoke is opt-in")
	}
	path := os.Getenv("MULTICA_CODEX_CONTROL_SMOKE_PATH")
	if path == "" {
		t.Fatal("explicit executable required")
	}
	backend := &codexBackend{cfg: Config{ExecutablePath: path, Env: map[string]string{"CODEX_HOME": t.TempDir()}, Logger: slog.Default()}}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	thread := ""
	cwd := t.TempDir()
	for _, command := range []string{"/plan", "/plan off"} {
		model := ""
		if command == "/plan off" {
			model = "gpt-6.1-sol"
		}
		session, err := backend.Execute(ctx, "", ExecOptions{Command: command, CommandOnly: true, Cwd: cwd, Model: model, ThinkingLevel: "low", ResumeSessionID: thread, Timeout: 25 * time.Second, ExtraArgs: []string{"--disable", "multi_agent"}})
		if err != nil {
			t.Fatal(err)
		}
		for range session.Messages {
		}
		result := <-session.Result
		if result.Status != "completed" || len(result.Usage) != 0 {
			t.Fatalf("native plan control failed: status=%s error=%s", result.Status, result.Error)
		}
		if thread != "" && result.SessionID != thread {
			t.Fatal("plan control lost native thread")
		}
		thread = result.SessionID
	}
}
