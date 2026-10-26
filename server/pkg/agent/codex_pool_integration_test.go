//go:build agentintegration

package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestCodexRealResidentControls(t *testing.T) {
	if os.Getenv("MULTICA_RUN_REAL_AGENT_SMOKE") != "1" || runtime.GOOS != "linux" {
		t.Skip("real Linux Codex resident control smoke is opt-in")
	}
	path, home := os.Getenv("MULTICA_CODEX_CONTROL_SMOKE_PATH"), os.Getenv("MULTICA_CODEX_CONTROL_SMOKE_HOME")
	if path == "" || home == "" {
		t.Fatal("explicit executable and isolated home required")
	}
	ctx, stop := context.WithTimeout(context.Background(), 75*time.Second)
	defer stop()
	version, err := DetectVersion(ctx, NewCommand(path, nil))
	if err != nil || !CodexResidentSupported(version) {
		t.Fatalf("resident protocol version: %v", err)
	}
	pool, err := NewCodexProcessPool(filepath.Join(t.TempDir(), "pool"), 2, time.Minute, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var priorPID int
	for round := range 3 {
		token := fmt.Sprintf("mat_RESIDENT_TEST_ONLY_%d", round)
		env := map[string]string{"CODEX_HOME": home, "MULTICA_TOKEN": token, "MULTICA_TASK_ID": fmt.Sprintf("resident-test-%d", round)}
		if round == 2 {
			env["CODEX_PROFILE_TEST_CHANGE"] = "changed"
		}
		backend := &codexBackend{cfg: Config{ExecutablePath: path, CodexVersion: version, Env: env, Logger: slog.Default(), TaskID: env["MULTICA_TASK_ID"], CodexPool: pool, ResidentSessionKey: "owned-control-smoke"}}
		session, err := backend.Execute(ctx, "", ExecOptions{Cwd: home, Command: "/goal show", CommandOnly: true, Timeout: 25 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		for range session.Messages {
		}
		result := <-session.Result
		if result.Status != "completed" || len(result.Usage) != 0 {
			t.Fatalf("resident native control: status=%s error=%s models=%d", result.Status, result.Error, len(result.Usage))
		}
		pool.mu.Lock()
		var entry *codexPoolEntry
		for _, candidate := range pool.entries {
			entry = candidate
		}
		pool.mu.Unlock()
		if entry == nil {
			t.Fatal("native process was not retained after verified unload")
		}
		pid := entry.cmd.Process.Pid
		if round == 1 && pid != priorPID {
			t.Fatal("native app-server PID did not survive the next task")
		}
		if round == 2 && pid == priorPID {
			t.Fatal("configuration change retained the old native process")
		}
		environ, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid))
		if err != nil || bytes.Contains(environ, []byte("MULTICA_TOKEN=")) || bytes.Contains(environ, []byte("MULTICA_TASK_ID=")) {
			t.Fatal("native root inherited a task credential")
		}
		config, err := os.ReadFile(filepath.Join(entry.dir, "home", "config.toml"))
		if err != nil || bytes.Contains(config, []byte(token)) {
			t.Fatal("native root configuration retained a task credential")
		}
		t.Logf("round=%d native_pid=%d configuration_changed=%t", round, pid, round == 2)
		priorPID = pid
	}
}

func TestCodexRealResidentGoalPersistence(t *testing.T) {
	if os.Getenv("MULTICA_RUN_REAL_AGENT_SMOKE") != "1" || runtime.GOOS != "linux" {
		t.Skip("real Linux Codex resident persistence smoke is opt-in")
	}
	path, home, thread := os.Getenv("MULTICA_CODEX_CONTROL_SMOKE_PATH"), os.Getenv("MULTICA_CODEX_CONTROL_SMOKE_HOME"), os.Getenv("MULTICA_CODEX_RESIDENT_GOAL_THREAD")
	if path == "" || home == "" || thread == "" {
		t.Fatal("explicit CLI, isolated home, and paused owned native goal required")
	}
	ctx, stop := context.WithTimeout(context.Background(), time.Minute)
	defer stop()
	version, err := DetectVersion(ctx, NewCommand(path, nil))
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "pool")
	for restart := range 2 {
		pool, err := NewCodexProcessPool(root, 1, time.Minute, slog.Default())
		if err != nil {
			t.Fatal(err)
		}
		backend := &codexBackend{cfg: Config{ExecutablePath: path, CodexVersion: version, Env: map[string]string{"CODEX_HOME": home}, Logger: slog.Default(), TaskID: "owned-paused-goal", CodexPool: pool, ResidentSessionKey: "paused-goal"}}
		session, err := backend.Execute(ctx, "", ExecOptions{Cwd: home, Command: "/goal show", CommandOnly: true, ResumeSessionID: thread, Timeout: 25 * time.Second})
		if err != nil {
			pool.Close()
			t.Fatal(err)
		}
		for range session.Messages {
		}
		result := <-session.Result
		pool.Close()
		if result.Status != "completed" || !strings.Contains(result.Output, "Native MCP lifecycle probe; remain paused.") || len(result.Usage) != 0 {
			t.Fatalf("restart=%d paused native goal was lost: status=%s error=%s output=%s models=%d", restart, result.Status, result.Error, result.Output, len(result.Usage))
		}
	}
}

func TestCodexRealResidentTaskEnvironments(t *testing.T) {
	if os.Getenv("MULTICA_RUN_REAL_AGENT_SMOKE") != "1" || os.Getenv("MULTICA_RUN_REAL_GOAL_MODEL_SMOKE") != "1" || runtime.GOOS != "linux" {
		t.Skip("real resident model/environment smoke is opt-in")
	}
	path, home := os.Getenv("MULTICA_CODEX_CONTROL_SMOKE_PATH"), os.Getenv("MULTICA_CODEX_CONTROL_SMOKE_HOME")
	if path == "" || home == "" {
		t.Fatal("explicit executable and isolated account required")
	}
	original, err := os.ReadFile(filepath.Join(home, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.WriteFile(filepath.Join(home, "config.toml"), original, 0600); err != nil {
			t.Error(err)
		}
	})
	ctx, stop := context.WithTimeout(context.Background(), 4*time.Minute)
	defer stop()
	version, err := DetectVersion(ctx, NewCommand(path, nil))
	if err != nil {
		t.Fatal(err)
	}
	pool, err := NewCodexProcessPool(filepath.Join(t.TempDir(), "pool"), 1, time.Minute, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	script := filepath.Join(t.TempDir(), "mcp.py")
	if err := os.WriteFile(script, []byte(`import json,os,sys
from pathlib import Path
Path(sys.argv[1]).write_text(json.dumps({"pid":os.getpid(),"marker":os.getenv("MULTICA_POOL_PROBE","EMPTY"),"task":os.getenv("MULTICA_TASK_ID")}))
for line in sys.stdin:
 try:r=json.loads(line)
 except:continue
 if "id" not in r:continue
 if r.get("method")=="initialize":out={"protocolVersion":"2024-11-05","capabilities":{"tools":{}},"serverInfo":{"name":"owned-env-fixture","version":"1"}}
 elif r.get("method")=="tools/list":out={"tools":[]}
 else:out={}
 print(json.dumps({"jsonrpc":"2.0","id":r["id"],"result":out}),flush=True)
`), 0600); err != nil {
		t.Fatal(err)
	}
	var thread string
	var nativePID int
	for round, marker := range []string{"POOL_FIRST_739521", "POOL_SECOND_864203", "EMPTY"} {
		proof := filepath.Join(t.TempDir(), "proof.json")
		taskID := fmt.Sprintf("owned-resident-model-round-%d", round)
		env := map[string]string{"CODEX_HOME": home, "MULTICA_TOKEN": "mat_TEST_FIXTURE_ONLY", "MULTICA_TASK_ID": taskID}
		if marker != "EMPTY" {
			env["MULTICA_POOL_PROBE"] = marker
		}
		mcp, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{"owned_env_fixture": map[string]any{"command": "python3", "args": []string{script, proof}, "env_vars": []string{"MULTICA_POOL_PROBE", "MULTICA_TOKEN", "MULTICA_TASK_ID"}}}})
		backend := &codexBackend{cfg: Config{ExecutablePath: path, CodexVersion: version, Env: env, Logger: slog.Default(), TaskID: taskID, CodexPool: pool, ResidentSessionKey: "owned-three-turn-model-smoke"}}
		session, err := backend.Execute(ctx, `Run exactly one shell command: printf '%s' "${MULTICA_POOL_PROBE-EMPTY}". Then reply with only the command's output. Do not read files, delegate, create goals, or use any other tools.`, ExecOptions{Cwd: home, Model: "gpt-6.1-sol", ThinkingLevel: "low", ServiceTier: "default", ResumeSessionID: thread, McpConfig: mcp, Timeout: 85 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		readVariable, observedToolOutput := false, false
		for msg := range session.Messages {
			if msg.Type == MessageToolUse {
				command := fmt.Sprint(msg.Input["command"])
				readVariable = readVariable || strings.Contains(command, "MULTICA_POOL_PROBE")
			}
			if msg.Type == MessageToolResult {
				observedToolOutput = observedToolOutput || strings.Contains(msg.Output, marker)
			}
		}
		result := <-session.Result
		if result.Status != "completed" || strings.TrimSpace(result.Output) != marker || !readVariable || !observedToolOutput || len(result.Usage) == 0 {
			t.Fatalf("round=%d environment/model boundary: status=%s error=%s output=%s variable_read=%t tool_output=%t models=%d", round, result.Status, result.Error, result.Output, readVariable, observedToolOutput, len(result.Usage))
		}
		thread = result.SessionID
		var fixture struct {
			PID    int    `json:"pid"`
			Marker string `json:"marker"`
			Task   string `json:"task"`
		}
		data, err := os.ReadFile(proof)
		if err != nil || json.Unmarshal(data, &fixture) != nil || fixture.Marker != marker || fixture.Task != taskID {
			t.Fatalf("round=%d MCP did not receive the current task environment: %v", round, err)
		}
		if ownedProcessAlive(fixture.PID) {
			t.Fatalf("round=%d MCP survived thread unload", round)
		}
		pool.mu.Lock()
		pid := 0
		for _, entry := range pool.entries {
			pid = entry.cmd.Process.Pid
		}
		pool.mu.Unlock()
		if pid == 0 || (round > 0 && pid != nativePID) {
			t.Fatal("native CLI PID was not retained across model turns")
		}
		nativePID = pid
		t.Logf("round=%d native_pid=%d model_usage_reported=true shell_and_mcp_environment_matches=true", round, pid)
	}
}
