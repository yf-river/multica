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

func TestCodebuddyRealNativeGoalControl(t *testing.T) {
	if os.Getenv("MULTICA_RUN_REAL_AGENT_SMOKE") != "1" {
		t.Skip("native runtime smoke is opt-in")
	}
	path, home := os.Getenv("MULTICA_CODEBUDDY_CONTROL_SMOKE_PATH"), os.Getenv("MULTICA_CODEBUDDY_CONTROL_SMOKE_HOME")
	if path == "" || home == "" {
		t.Fatal("explicit executable and isolated smoke home required")
	}
	backend := &codebuddyBackend{cfg: Config{ExecutablePath: path, Logger: slog.Default(), Env: map[string]string{"HOME": home, "CODEBUDDY_CONFIG_DIR": home + "/codebuddy", "CODEBUDDY_CODE_DISABLE_BACKGROUND_TASKS": "1"}}}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	id := ""
	for _, command := range []string{"/goal show", "/goal pause", "/goal clear"} {
		session, err := backend.executeNative(ctx, "", ExecOptions{Command: command, CommandOnly: true, ResumeSessionID: id, Model: "deepseek-v4.1-flash", Cwd: home, Timeout: 30 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		for range session.Messages {
		}
		result := <-session.Result
		if result.Status != "completed" || !strings.Contains(result.Output, "没有目标") || result.SessionID == "" {
			t.Fatalf("control %s failed: %+v", command, result)
		}
		if id != "" && id != result.SessionID {
			t.Fatal("control resume changed session")
		}
		id = result.SessionID
	}
}

func TestCodebuddyRealNativeGoalLoop(t *testing.T) {
	if os.Getenv("MULTICA_RUN_REAL_GOAL_MODEL_SMOKE") != "1" {
		t.Skip("real goal model smoke is opt-in")
	}
	path, home := os.Getenv("MULTICA_CODEBUDDY_CONTROL_SMOKE_PATH"), os.Getenv("MULTICA_CODEBUDDY_CONTROL_SMOKE_HOME")
	if path == "" || home == "" {
		t.Fatal("explicit executable and isolated smoke home required")
	}
	backend := &codebuddyBackend{cfg: Config{ExecutablePath: path, Logger: slog.Default(), Env: map[string]string{"HOME": home, "CODEBUDDY_CONFIG_DIR": home + "/codebuddy", "CODEBUDDY_CODE_DISABLE_BACKGROUND_TASKS": "1"}}}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	session, err := backend.executeNative(ctx, "", ExecOptions{Command: "/goal Produce the exact final text MULTICA_NATIVE_GOAL_OK. This goal succeeds once you have emitted that text. Do not call any tools.", Model: "deepseek-v4.1-flash", Cwd: home, Timeout: 70 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	runtimeEvents := 0
	for msg := range session.Messages {
		if msg.Type == MessageRuntime {
			runtimeEvents++
		}
	}
	result := <-session.Result
	if result.Status != "completed" || !strings.Contains(result.Output, "MULTICA_NATIVE_GOAL_OK") {
		t.Fatalf("native goal loop failed: status=%s error=%s output=%s runtime_events=%d", result.Status, result.Error, result.Output, runtimeEvents)
	}
	if len(result.Usage) == 0 {
		t.Fatal("native goal completed without correlated per-call accounting")
	}
	t.Logf("Native goal completed; runtime_events=%d reported_models=%d", runtimeEvents, len(result.Usage))
}
