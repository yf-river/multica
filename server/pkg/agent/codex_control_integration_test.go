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
	for _, command := range []string{"/goal show", "/goal clear"} {
		session, err := b.Execute(ctx, "", ExecOptions{
			Cwd: cwd, Model: "gpt-6.1-sol", Command: command, CommandOnly: true, Timeout: 30 * time.Second,
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
		if result.SessionID != "" {
			t.Fatal("pure control published an unmaterialized thread")
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

func TestCodexRealLiveGoalLostAck(t *testing.T) {
	if os.Getenv("MULTICA_RUN_REAL_GOAL_MODEL_SMOKE") != "1" || os.Getenv("MULTICA_CODEX_FAULT_TRANSPORT") != "1" {
		t.Skip("real model transport fault injection is opt-in")
	}
	path, home, cwd := os.Getenv("MULTICA_CODEX_CONTROL_SMOKE_PATH"), os.Getenv("MULTICA_CODEX_CONTROL_SMOKE_HOME"), os.Getenv("MULTICA_CODEX_CONTROL_SMOKE_CWD")
	if path == "" || home == "" || cwd == "" {
		t.Fatal("explicit fault transport, isolated account home and cwd required")
	}
	backend := &codexBackend{cfg: Config{ExecutablePath: path, Env: map[string]string{
		"CODEX_HOME":                    home,
		"MULTICA_CODEX_FAULT_REAL_PATH": os.Getenv("MULTICA_CODEX_FAULT_REAL_PATH"),
		"MULTICA_CODEX_FAULT_TRACE":     os.Getenv("MULTICA_CODEX_FAULT_TRACE"),
	}, Logger: slog.Default()}}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
	defer cancel()
	session, err := backend.Execute(ctx, "Explain in 250 words how binary search works. Do not use tools, read files, run commands, or delegate.", ExecOptions{Cwd: cwd, Model: "gpt-6.1-sol", ThinkingLevel: "low", ServiceTier: "default", ExtraArgs: []string{"--disable", "multi_agent"}, Timeout: 85 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	drained := make(chan struct{})
	goalComplete := false
	var streamed strings.Builder
	go func() {
		defer close(drained)
		for msg := range session.Messages {
			if msg.Type == MessageText {
				streamed.WriteString(msg.Content)
			}
			var event struct {
				Kind string `json:"kind"`
				Goal struct {
					Status string `json:"status"`
				} `json:"goal"`
			}
			if msg.Type == MessageRuntime && json.Unmarshal([]byte(msg.Content), &event) == nil && event.Kind == "goal" && event.Goal.Status == "complete" {
				goalComplete = true
			}
		}
	}()
	for !session.CommandReady() {
		select {
		case <-ctx.Done():
			t.Fatal("native command gate did not open")
		case <-drained:
			t.Fatalf("native execution ended before control: %+v", <-session.Result)
		case <-time.After(5 * time.Millisecond):
		}
	}
	_, err = session.RunCommand(ctx, "/goal Emit the exact text NATIVE_LOST_ACK_GOAL_OK, then mark this goal complete using update_goal. Do not use other tools or delegate.")
	if err == nil {
		t.Fatal("transport did not lose the native goal acknowledgement")
	}
	<-drained
	result := <-session.Result
	if result.Status != "completed" || !goalComplete || !strings.Contains(streamed.String(), "NATIVE_LOST_ACK_GOAL_OK") {
		t.Fatalf("lost acknowledgement broke the native Goal lifecycle: status=%s complete=%t error=%s", result.Status, goalComplete, result.Error)
	}
	t.Logf("Native Goal completed after lost control acknowledgement; duration_ms=%d reported_models=%d", result.DurationMs, len(result.Usage))
}
