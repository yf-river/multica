//go:build agentintegration && linux

package execenv

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/pkg/agent"
)

// 使用任务环境的真实生产者；每轮创建新 home，续接依赖会话存储而非复用任务目录。
func TestCodexRealPreparedTaskStartupComparison(t *testing.T) {
	if os.Getenv("MULTICA_RUN_REAL_STARTUP_BENCHMARK") != "1" {
		t.Skip("eight real model calls with prepared task environments are opt-in")
	}
	executable := os.Getenv("MULTICA_CODEX_CONTROL_SMOKE_PATH")
	account := os.Getenv("MULTICA_CODEX_CONTROL_SMOKE_HOME")
	destination := os.Getenv("MULTICA_CODEX_POOL_BENCH_RESULT")
	role := os.Getenv("MULTICA_PAIR_ROLE")
	if executable == "" || account == "" || destination == "" || role != "candidate" {
		t.Fatal("explicit CLI, isolated account, candidate role and private result path required")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Minute)
	defer cancel()
	version, err := agent.DetectVersion(ctx, agent.NewCommand(executable, nil))
	if err != nil || !agent.CodexResidentSupported(version) {
		t.Fatalf("native protocol version: %s %v", version, err)
	}
	fixture, err := os.MkdirTemp(filepath.Dir(destination), "task-fixture-")
	if err != nil {
		t.Fatal(err)
	}
	shared := filepath.Join(fixture, "shared")
	if err := os.Mkdir(shared, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"auth.json", "config.toml", "models_cache.json", "config.json", "instructions.md"} {
		raw, err := os.ReadFile(filepath.Join(account, name))
		if os.IsNotExist(err) && (name == "config.json" || name == "instructions.md") {
			continue
		}
		if err != nil {
			t.Fatalf("read isolated metadata %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(shared, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"skills", "plugins", "rules"} {
		source := filepath.Join(account, name)
		if _, err := os.Stat(source); os.IsNotExist(err) {
			continue
		} else if err != nil {
			t.Fatal(err)
		}
		if err := os.CopyFS(filepath.Join(shared, name), os.DirFS(source)); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("CODEX_HOME", shared)
	workspace, err := os.MkdirTemp("", "multica-owned-prepared-benchmark-")
	if err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(workspace, "project")
	if err := os.Mkdir(work, 0700); err != nil {
		t.Fatal(err)
	}
	pool, err := agent.NewCodexProcessPool(filepath.Join(workspace, ".runtime-processes", "codex"), 1, time.Minute, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	type sample struct {
		Cold          bool                        `json:"cold"`
		PID           int                         `json:"pid"`
		Home          string                      `json:"task_home"`
		SessionID     string                      `json:"session_id"`
		EnvironmentMs int64                       `json:"environment_ms"`
		PreparationMs int64                       `json:"preparation_and_handshake_ms"`
		DurationMs    int64                       `json:"duration_ms"`
		BackendWallMs int64                       `json:"backend_wall_ms"`
		WallMs        int64                       `json:"wall_ms"`
		Tokens        int64                       `json:"observed_tokens"`
		Usage         map[string]agent.TokenUsage `json:"usage"`
	}
	var samples []sample
	sid, previousPID := "", 0
	homes := make(map[string]bool)
	for index := 0; index < 8; index++ {
		if barrier := os.Getenv("MULTICA_PAIR_BARRIER"); barrier != "" {
			waitPreparedBenchmarkRound(t, ctx, filepath.Join(barrier, fmt.Sprintf("%s-%d", role, index)))
		}
		cold := index < 5
		chatIndex := min(index, 4)
		if cold {
			sid = ""
		}
		logPath := filepath.Join(filepath.Dir(destination), fmt.Sprintf("codex-startup-%d.jsonl", index))
		logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		defer logFile.Close()
		logger := slog.New(slog.NewJSONHandler(logFile, nil))
		started := time.Now()
		task := TaskContextForEnv{AgentID: "owned-prepared-agent", ChatSessionID: fmt.Sprintf("owned-chat-%d", chatIndex)}
		store := CodexSessionStorePath("owned-prepared-benchmark", task)
		writer, err := AcquireCodexSessionWriterLock(ctx, store, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(writer.Release)
		taskID := uuid.NewString()
		env, err := Prepare(PrepareParams{WorkspacesRoot: workspace, WorkspaceID: "owned-prepared-workspace", TaskID: taskID, AgentName: "Owned benchmark", Provider: "codex", Profile: "owned-prepared-benchmark", LocalWorkDir: work, CodexVersion: version, Task: task}, logger)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(env.ReleaseLock)
		if homes[env.CodexHome] {
			t.Fatal("task home was reused across task IDs")
		}
		homes[env.CodexHome] = true
		state, err := PrepareCodexNativeState(ctx, env.CodexHome, "", store, "owned-prepared-runtime", sid, pool.RetireHome)
		if err != nil {
			t.Fatal(err)
		}
		cfg := agent.Config{ExecutablePath: executable, BuiltinRuntime: true, CodexVersion: version, TaskID: taskID, Logger: logger, CodexPool: pool, ResidentSessionKey: task.ChatSessionID, CodexResumePath: state.ResumePath, Env: map[string]string{"CODEX_HOME": env.CodexHome, "CODEX_SQLITE_HOME": state.SQLiteHome, "MULTICA_TASK_ID": taskID}}
		backend, err := agent.ResolveBackend("codex", cfg)
		if err != nil {
			t.Fatal(err)
		}
		backendStarted := time.Now()
		entry := sample{Cold: cold, Home: env.CodexHome, EnvironmentMs: backendStarted.Sub(started).Milliseconds(), PreparationMs: -1}
		marker := rand.Text()
		session, err := backend.Execute(ctx, "Do not use tools or delegate. Output exactly the following marker, with no punctuation or explanation:\n"+marker, agent.ExecOptions{Cwd: env.WorkDir, Model: "gpt-6.1-sol", ThinkingLevel: "low", ServiceTier: "default", ResumeSessionID: sid, Timeout: 80 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		tools := 0
		for msg := range session.Messages {
			if msg.Type == agent.MessageToolUse {
				tools++
			}
		}
		result := <-session.Result
		entry.BackendWallMs, entry.WallMs = time.Since(backendStarted).Milliseconds(), time.Since(started).Milliseconds()
		_, modelMatches := result.Usage["gpt-6.1-sol"]
		if result.Status != "completed" || strings.TrimSpace(result.Output) != marker || !modelMatches || tools != 0 || result.SessionID == "" || !cold && result.SessionID != sid {
			t.Fatalf("round=%d status=%s error=%s exact_reply=%t actual_model=%t tools=%d same_thread=%t", index, result.Status, result.Error, strings.TrimSpace(result.Output) == marker, modelMatches, tools, cold || result.SessionID == sid)
		}
		rawLog, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(strings.TrimSpace(string(rawLog)), "\n") {
			var event struct {
				Time        time.Time `json:"time"`
				Message     string    `json:"msg"`
				Phase       string    `json:"phase"`
				PID         int       `json:"pid"`
				LauncherPID int       `json:"launcher_pid"`
			}
			if err := json.Unmarshal([]byte(line), &event); err != nil {
				t.Fatal(err)
			}
			if event.Phase == "spawn" {
				entry.PID = event.PID
			}
			if event.Message == "codex resident lease" {
				entry.PID = event.LauncherPID
			}
			if event.Message == "codex turn/start sent" {
				entry.PreparationMs = event.Time.Sub(started).Milliseconds()
			}
		}
		if entry.PID == 0 || entry.PreparationMs < 0 || !cold && entry.PID != previousPID {
			t.Fatal("prepared task did not retain its resident launcher or readiness evidence")
		}
		entry.SessionID, entry.DurationMs, entry.Usage = result.SessionID, result.DurationMs, result.Usage
		for _, usage := range result.Usage {
			entry.Tokens += usage.InputTokens + usage.OutputTokens + usage.CacheReadTokens + usage.CacheWriteTokens
		}
		sid, previousPID = result.SessionID, entry.PID
		writer.Release()
		env.ReleaseLock()
		samples = append(samples, entry)
		raw, err := json.MarshalIndent(map[string]any{"runtime": "codex", "cli": version, "model": "gpt-6.1-sol", "thinking_level": "low", "service_tier": "default", "case": "actual execenv Prepare; fresh task home per round; five new conversations then three continued turns", "samples": samples}, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(destination, raw, 0600); err != nil {
			t.Fatal(err)
		}
		t.Logf("round=%d cold=%t launcher_pid=%d environment_ms=%d ready_ms=%d backend_wall_ms=%d wall_ms=%d tokens=%d", index, cold, entry.PID, entry.EnvironmentMs, entry.PreparationMs, entry.BackendWallMs, entry.WallMs, entry.Tokens)
	}
}

func waitPreparedBenchmarkRound(t *testing.T, ctx context.Context, path string) {
	t.Helper()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(120 * time.Second)
	defer deadline.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-deadline.C:
			t.Fatal("paired driver did not release prepared task round")
		case <-ticker.C:
		}
	}
}
