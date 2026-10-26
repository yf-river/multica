//go:build agentintegration

package agent

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestCodebuddyRealNativeQuestionAnswer(t *testing.T) {
	if os.Getenv("MULTICA_RUN_REAL_GOAL_MODEL_SMOKE") != "1" {
		t.Skip("real native question smoke is opt-in")
	}
	runtime := codebuddyRealSmokeRuntime(t)
	for _, test := range []struct{ name, decision, output string }{{"allow", "allow", "简短"}, {"decline", "deny", "DECLINED"}} {
		t.Run(test.name, func(t *testing.T) {
			var count atomic.Int32
			backend := &codebuddyBackend{cfg: Config{ExecutablePath: runtime.Path, BuiltinRuntime: true, CLIVersion: runtime.Version, Logger: slog.Default(), Env: map[string]string{"HOME": runtime.Home, "CODEBUDDY_CONFIG_DIR": filepath.Join(runtime.Home, "codebuddy")}}}
			session, err := backend.executeNative(t.Context(), "必须调用 AskUserQuestion 原生工具提出一个单选问题：回复要采用哪种长度？选项为简短、详细，各有一句说明。等待用户选择后，只回复所选选项；如果用户拒绝回答，只回复 DECLINED。不要运行命令、搜索或调用其他工具。", ExecOptions{Cwd: t.TempDir(), Model: "deepseek-v4.1-flash", Timeout: 100 * time.Second, RequestInteraction: func(_ context.Context, prompt protocol.InteractionPrompt) (protocol.InteractionAnswer, error) {
				count.Add(1)
				if prompt.Kind != "question" || len(prompt.Questions) != 1 || len(prompt.Questions[0].Options) != 2 {
					t.Errorf("unexpected native question: kind=%s questions=%d title=%.350q", prompt.Kind, len(prompt.Questions), prompt.Title)
					return protocol.InteractionAnswer{Decision: "deny"}, nil
				}
				if test.decision == "deny" {
					return protocol.InteractionAnswer{Decision: "deny"}, nil
				}
				q := prompt.Questions[0]
				label := ""
				for _, option := range q.Options {
					if strings.Contains(option.Label, "简短") {
						label = option.Label
					}
				}
				if label == "" {
					t.Error("native short-answer option missing")
					return protocol.InteractionAnswer{Decision: "deny"}, nil
				}
				return protocol.InteractionAnswer{Decision: "allow", Answers: map[string][]string{q.ID: {label}}}, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			for range session.Messages {
			}
			result := <-session.Result
			if result.Status != "completed" || strings.TrimSpace(result.Output) != test.output || count.Load() != 1 || len(result.Usage) == 0 {
				t.Fatalf("native question failed: status=%s error=%s output=%q questions=%d models=%d", result.Status, result.Error, result.Output, count.Load(), len(result.Usage))
			}
			t.Logf("Real CodeBuddy %s native question decision=%s delivered once; model observed exact outcome; models=%d duration_ms=%d; responder=automated_test", runtime.Version, test.decision, len(result.Usage), result.DurationMs)
		})
	}
}

func TestCodebuddyRealNativeForegroundChild(t *testing.T) {
	if os.Getenv("MULTICA_RUN_REAL_GOAL_MODEL_SMOKE") != "1" {
		t.Skip("real foreground child model smoke is opt-in")
	}
	runtime := codebuddyRealSmokeRuntime(t)
	path, home := runtime.Path, runtime.Home
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

func TestCodebuddyRealNativeManagedHooks(t *testing.T) {
	if os.Getenv("MULTICA_RUN_REAL_GOAL_MODEL_SMOKE") != "1" {
		t.Skip("real managed hook model smoke is opt-in")
	}
	runtime := codebuddyRealSmokeRuntime(t)
	path, home := runtime.Path, runtime.Home
	if path == "" || home == "" {
		t.Fatal("explicit executable and isolated smoke home required")
	}
	for _, goal := range []bool{false, true} {
		name := "foreground-child"
		if goal {
			name = "automatic-goal"
		}
		t.Run(name, func(t *testing.T) {
			work := t.TempDir()
			marker, err := newCodebuddyGeneration()
			if err != nil {
				t.Fatal(err)
			}
			proof := filepath.Join(work, "proof")
			command := `printf '%s' "$MULTICA_NATIVE_HOOK_VALUE" > ` + shellQuote(proof)
			// 启动包装器删去标记，证明实际 Bash 从受管 hook 快照取得本轮值。
			wrapper := filepath.Join(work, "codebuddy-without-task-marker")
			if err := os.WriteFile(wrapper, []byte("#!/bin/sh\nunset MULTICA_NATIVE_HOOK_VALUE\nexec "+shellQuote(path)+" \"$@\"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			backend := &codebuddyBackend{cfg: Config{ExecutablePath: wrapper, BuiltinRuntime: true, CLIVersion: runtime.Version, Logger: slog.Default(), Env: map[string]string{"HOME": home, "CODEBUDDY_CONFIG_DIR": filepath.Join(home, "codebuddy"), "MULTICA_NATIVE_HOOK_VALUE": marker}}}
			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel()
			opts := ExecOptions{Cwd: work, Model: "deepseek-v4.1-flash", Timeout: 110 * time.Second, MaxTurns: 8, SystemPrompt: "Execute only the requested small acceptance check. Do not inspect unrelated files or invoke other tools."}
			prompt := "Use Agent exactly once to delegate one foreground general-purpose subagent. Ask it to use Bash exactly once to execute this command verbatim: " + command + ". Wait for it to finish. Then reply exactly MANAGED_NATIVE_HOOK_OK. Do not use background tasks, teams, other tools, read files, or delegate further."
			if goal {
				prompt = ""
				opts.Command = "/goal Use Bash exactly once to execute this command verbatim: " + command + ". Then emit MANAGED_NATIVE_HOOK_OK. This goal succeeds when the command ran and that text was emitted. Do not use other tools or read files."
			}
			session, err := backend.executeNative(ctx, prompt, opts)
			if err != nil {
				t.Fatal(err)
			}
			for range session.Messages {
			}
			result := <-session.Result
			got, proofErr := os.ReadFile(proof)
			if result.Status != "completed" || proofErr != nil || string(got) != marker || !strings.Contains(result.Output, "MANAGED_NATIVE_HOOK_OK") {
				t.Fatalf("managed hook check failed: status=%s error=%s proof_error=%v matches=%v output=%q", result.Status, result.Error, proofErr, string(got) == marker, result.Output)
			}
			if len(result.Usage) == 0 || !goal && len(result.ExtraTraces) == 0 {
				t.Fatal("managed hook run lost native accounting")
			}
			t.Logf("Managed %s hooks passed; actual Bash environment proof; models=%d child_traces=%d duration_ms=%d", name, len(result.Usage), len(result.ExtraTraces), result.DurationMs)
		})
	}
}

func TestCodebuddyRealNativeHookBriefAcrossResumedTasks(t *testing.T) {
	if os.Getenv("MULTICA_RUN_REAL_GOAL_MODEL_SMOKE") != "1" {
		t.Skip("real native hook brief model smoke is opt-in")
	}
	runtime := codebuddyRealSmokeRuntime(t)
	path, home := runtime.Path, runtime.Home
	if path == "" || home == "" {
		t.Fatal("explicit executable and isolated smoke home required")
	}
	work, sid, previousCID := t.TempDir(), "", ""
	backend := &codebuddyBackend{cfg: Config{ExecutablePath: path, BuiltinRuntime: true, CLIVersion: runtime.Version, Logger: slog.Default(), Env: map[string]string{"HOME": home, "CODEBUDDY_CONFIG_DIR": filepath.Join(home, "codebuddy")}}}
	for round := 0; round < 2; round++ {
		marker, err := newCodebuddyGeneration()
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 70*time.Second)
		opts := ExecOptions{Cwd: work, Model: "deepseek-v4.1-flash", NativeGoal: true, ResumeSessionID: sid, Timeout: 60 * time.Second, MaxTurns: 2, SystemPrompt: "Answer the requested current task marker. Do not use tools.", CodebuddyRuntimeBrief: "The current runtime brief marker is " + marker + ". This is the current task's marker."}
		session, err := backend.Execute(ctx, "Reply only with the marker specified by the current runtime brief. Ignore markers from previous tasks. Do not call tools.", opts)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		for range session.Messages {
		}
		result := <-session.Result
		cancel()
		if result.Status != "completed" || strings.TrimSpace(result.Output) != marker || result.ProviderTraceID == "" || result.ProviderTraceID == previousCID || len(result.Usage) == 0 {
			t.Fatalf("hook brief round %d failed: status=%s matches=%v new_cid=%v usage=%v error=%s", round, result.Status, strings.TrimSpace(result.Output) == marker, result.ProviderTraceID != previousCID, len(result.Usage) > 0, result.Error)
		}
		if sid != "" && result.SessionID != sid {
			t.Fatal("hook brief changed the resumed native session")
		}
		sid, previousCID = result.SessionID, result.ProviderTraceID
		t.Logf("Cold task round %d resumed=%v; fresh hook marker and per-call usage verified; duration_ms=%d", round, round > 0, result.DurationMs)
	}
	if _, err := os.Stat(filepath.Join(work, "CODEBUDDY.md")); !os.IsNotExist(err) {
		t.Fatal("hook brief was also written to the native project memory file")
	}
}

func TestCodebuddyRealNativeGoalControl(t *testing.T) {
	if os.Getenv("MULTICA_RUN_REAL_AGENT_SMOKE") != "1" {
		t.Skip("native runtime smoke is opt-in")
	}
	runtime := codebuddyRealSmokeRuntime(t)
	path, home := runtime.Path, runtime.Home
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
	runtime := codebuddyRealSmokeRuntime(t)
	path, home := runtime.Path, runtime.Home
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
	runtime := codebuddyRealSmokeRuntime(t)
	path, home := runtime.Path, runtime.Home
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
