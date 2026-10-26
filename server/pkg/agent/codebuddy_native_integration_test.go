//go:build agentintegration

package agent

import (
	"context"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCodebuddyRealNativeForegroundChild(t *testing.T) {
	if os.Getenv("MULTICA_RUN_REAL_GOAL_MODEL_SMOKE") != "1" {
		t.Skip("real foreground child model smoke is opt-in")
	}
	path, home := os.Getenv("MULTICA_CODEBUDDY_CONTROL_SMOKE_PATH"), os.Getenv("MULTICA_CODEBUDDY_CONTROL_SMOKE_HOME")
	if path == "" || home == "" {
		t.Fatal("explicit executable and isolated smoke home required")
	}
	work := t.TempDir()
	wrapper := filepath.Join(work, "codebuddy-foreground-only")
	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\nexec "+shellQuote(path)+" --tools Agent,Bash --strict-mcp-config \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	backend := &codebuddyBackend{cfg: Config{ExecutablePath: wrapper, Logger: slog.Default(), Env: map[string]string{"HOME": home, "CODEBUDDY_CONFIG_DIR": filepath.Join(home, "codebuddy"), codebuddyDisableBackgroundTasksEnv: "0"}}}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	session, err := backend.executeNative(ctx, "Use Agent exactly once to delegate one foreground general-purpose subagent. Ask it to use Bash exactly once to run printf NATIVE_CHILD_SCOPE_OK and return that output. Wait for the child to finish. Then your final answer must be exactly NATIVE_PARENT_SCOPE_OK. Do not use background tasks, teams, other tools, files, or further delegation.", ExecOptions{Cwd: work, Model: "deepseek-v4.1-flash", Timeout: 110 * time.Second, MaxTurns: 8})
	if err != nil {
		t.Fatal(err)
	}
	childVisible := false
	for msg := range session.Messages {
		if (msg.Type == MessageText && strings.Contains(msg.Content, "NATIVE_CHILD_SCOPE_OK")) || (msg.Type == MessageToolResult && strings.Contains(msg.Output, "NATIVE_CHILD_SCOPE_OK")) {
			childVisible = true
		}
	}
	result := <-session.Result
	if result.Status != "completed" || !childVisible || !strings.Contains(result.Output, "NATIVE_PARENT_SCOPE_OK") || strings.Contains(result.Output, "NATIVE_CHILD_SCOPE_OK") {
		t.Fatalf("foreground child or parent answer failed: child=%v status=%s error=%s output=%q", childVisible, result.Status, result.Error, result.Output)
	}
	if result.ProviderTraceID == "" || len(result.Usage) == 0 || len(result.ExtraTraces) == 0 {
		t.Fatalf("missing parent/child accounting: root=%v models=%d extra_traces=%d", result.ProviderTraceID != "", len(result.Usage), len(result.ExtraTraces))
	}
	t.Logf("Foreground child complete; separate parent answer; models=%d extra_traces=%d duration_ms=%d", len(result.Usage), len(result.ExtraTraces), result.DurationMs)
}

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

func TestCodebuddyRealNativePlanApproval(t *testing.T) {
	if os.Getenv("MULTICA_RUN_REAL_GOAL_MODEL_SMOKE") != "1" {
		t.Skip("real native plan model smoke is opt-in")
	}
	path, home := os.Getenv("MULTICA_CODEBUDDY_CONTROL_SMOKE_PATH"), os.Getenv("MULTICA_CODEBUDDY_CONTROL_SMOKE_HOME")
	if path == "" || home == "" {
		t.Fatal("explicit executable and isolated smoke home required")
	}
	backend := &codebuddyBackend{cfg: Config{ExecutablePath: path, Logger: slog.Default(), Env: map[string]string{"HOME": home, "CODEBUDDY_CONFIG_DIR": home + "/codebuddy", "CODEBUDDY_CODE_DISABLE_BACKGROUND_TASKS": "1"}}}
	ctx, cancel := context.WithTimeout(context.Background(), 85*time.Second)
	defer cancel()
	observed := make(chan protocol.InteractionPrompt, 10)
	plan := true
	session, err := backend.executeNative(ctx, "Write a one sentence plan to explain how two plus two equals four. Include the exact marker NATIVE_PLAN_REVIEW_OK in the plan file, then call ExitPlanMode to request approval. Do not read or change other files, run commands, search, or delegate. If approval is denied, stop and say the plan was not approved.", ExecOptions{Model: "deepseek-v4.1-flash", Cwd: home, PlanMode: &plan, Timeout: 70 * time.Second, MaxTurns: 3, RequestInteraction: func(_ context.Context, prompt protocol.InteractionPrompt) (protocol.InteractionAnswer, error) {
		observed <- prompt
		return protocol.InteractionAnswer{Decision: "deny"}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	for range session.Messages {
	}
	result := <-session.Result
	if result.Status != "completed" && result.Status != "aborted" {
		t.Fatalf("native plan failed: status=%s error=%s", result.Status, result.Error)
	}
	found := false
	for len(observed) > 0 {
		prompt := <-observed
		if prompt.Kind == "plan" && strings.Contains(prompt.Title, "NATIVE_PLAN_REVIEW_OK") {
			found = true
		}
	}
	if !found {
		t.Fatal("native plan artifact did not reach the approval bridge")
	}
	t.Logf("Native plan artifact reached approval; isolated test response denied execution; duration_ms=%d", result.DurationMs)
}
