//go:build agentintegration && linux

package execenv

import (
	"bufio"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/multica-ai/multica/server/pkg/agent"
)

func TestCodexRealGoalAcrossTaskHomes(t *testing.T) {
	if os.Getenv("MULTICA_RUN_REAL_AGENT_SMOKE") != "1" {
		t.Skip("real native Goal migration is opt-in")
	}
	executable, account := os.Getenv("MULTICA_CODEX_CONTROL_SMOKE_PATH"), os.Getenv("MULTICA_CODEX_CONTROL_SMOKE_HOME")
	if executable == "" || account == "" {
		t.Fatal("explicit CLI and isolated account are required")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	version, err := agent.DetectVersion(ctx, agent.NewCommand(executable, nil))
	if err != nil || !agent.CodexResidentSupported(version) {
		t.Fatalf("native version: %v", err)
	}
	source := t.TempDir()
	copyCodexNativeSmokeAccount(t, account, source)
	secret, thread := "", ""
	if os.Getenv("MULTICA_RUN_REAL_GOAL_MODEL_SMOKE") == "1" {
		secret = rand.Text()
		backend, err := agent.ResolveBackend("codex", agent.Config{ExecutablePath: executable, CodexVersion: version, Logger: slog.Default(), Env: map[string]string{"CODEX_HOME": source, "CODEX_SQLITE_HOME": source}})
		if err != nil {
			t.Fatal(err)
		}
		session, err := backend.Execute(ctx, "The secret code for the next turn is "+secret+". Remember it in this conversation. Reply only STORED. Do not invoke tools or delegate.", agent.ExecOptions{Cwd: source, Model: "gpt-6.1-sol", ThinkingLevel: "low", ServiceTier: "default", Timeout: time.Minute})
		if err != nil {
			t.Fatal(err)
		}
		for range session.Messages {
		}
		result := <-session.Result
		if result.Status != "completed" || strings.TrimSpace(result.Output) != "STORED" || result.SessionID == "" || len(result.Usage) == 0 {
			t.Fatalf("original native model turn: status=%s error=%s", result.Status, result.Error)
		}
		thread = result.SessionID
	}
	thread = createCodexGoalFixture(t, ctx, executable, source, thread, "paused")
	store, poolRoot, cwd := t.TempDir(), filepath.Join(t.TempDir(), "pool"), t.TempDir()
	pool, err := agent.NewCodexProcessPool(poolRoot, 1, time.Minute, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	priorPID := 0
	for round := range 2 {
		var transcript *os.File
		if directory := os.Getenv("MULTICA_CODEX_MIGRATION_EVIDENCE_DIR"); directory != "" {
			var err error
			transcript, err = os.OpenFile(filepath.Join(directory, fmt.Sprintf("native-migration-round-%d.jsonl", round)), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				t.Fatal(err)
			}
			defer transcript.Close()
		}
		home := t.TempDir()
		copyCodexNativeSmokeAccount(t, account, home)
		state, err := PrepareCodexNativeState(ctx, home, source, store, "owned-runtime", thread, pool.RetireHome)
		if err != nil || state.ResumePath == "" {
			t.Fatalf("round=%d prepare native state: %v", round, err)
		}
		if round == 0 {
			if err := os.RemoveAll(source); err != nil {
				t.Fatal(err)
			}
			source = ""
		}
		backend, err := agent.ResolveBackend("codex", agent.Config{ExecutablePath: executable, CodexVersion: version, TaskID: fmt.Sprintf("owned-native-state-migration-%d", round), Logger: slog.Default(), CodexPool: pool, ResidentSessionKey: "owned-native-state-migration", CodexResumePath: state.ResumePath, Env: map[string]string{"CODEX_HOME": home, "CODEX_SQLITE_HOME": state.SQLiteHome}})
		if err != nil {
			t.Fatal(err)
		}
		opts := agent.ExecOptions{Cwd: cwd, Command: "/goal show", CommandOnly: true, ResumeSessionID: thread, Timeout: 25 * time.Second}
		prompt := ""
		if secret != "" && round == 1 {
			opts.Command, opts.CommandOnly = "", false
			opts.Model, opts.ThinkingLevel, opts.ServiceTier, opts.Timeout = "gpt-6.1-sol", "low", "default", 2*time.Minute
			prompt = "Reply only with the secret code from the earlier user message in this conversation. Do not use tools or delegate."
		}
		session, err := backend.Execute(ctx, prompt, opts)
		if err != nil {
			t.Fatal(err)
		}
		for message := range session.Messages {
			if transcript != nil {
				if err := json.NewEncoder(transcript).Encode(message); err != nil {
					t.Error(err)
				}
			}
		}
		result := <-session.Result
		if transcript != nil {
			if err := json.NewEncoder(transcript).Encode(result); err != nil {
				t.Error(err)
			}
		}
		if result.Status != "completed" || (prompt == "" && (!strings.Contains(result.Output, "Native MCP lifecycle probe; remain paused.") || len(result.Usage) != 0)) || (prompt != "" && (strings.TrimSpace(result.Output) != secret || len(result.Usage) == 0)) {
			t.Fatalf("round=%d native Goal lost: status=%s error=%s output=%s", round, result.Status, result.Error, result.Output)
		}
		records, _ := filepath.Glob(filepath.Join(poolRoot, "*", "process.json"))
		if len(records) != 1 {
			t.Fatal("native process did not survive the task boundary")
		}
		var record struct {
			Identity agent.CLIIdentity `json:"identity"`
		}
		data, err := os.ReadFile(records[0])
		if err != nil || json.Unmarshal(data, &record) != nil || !record.Identity.Valid {
			t.Fatal("native process identity is unavailable")
		}
		if round == 1 && record.Identity.PID != priorPID {
			t.Fatal("fresh task home restarted the native app-server")
		}
		priorPID = record.Identity.PID
		t.Logf("round=%d native_pid=%d original_home_removed=true native_goal_preserved=true", round, priorPID)
	}
}

// 原生 API 建立暂停目标，没有 turn/start，也没有模型调用或人工修改原生数据库。
func createCodexGoalFixture(t *testing.T, ctx context.Context, executable, home, thread, status string) string {
	t.Helper()
	cmd := exec.CommandContext(ctx, executable, "app-server", "--listen", "stdio://", "-c", "thread_unload_delay_secs=0")
	cmd.Env = append(os.Environ(), "CODEX_HOME="+home, "CODEX_SQLITE_HOME="+home)
	cmd.Dir = home
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = input.Close()
		// 原生状态已确认写入；模拟进程退出。wait 前 PID 尚未释放，进程组不会被复用。
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
	}()
	reader, writer := bufio.NewScanner(output), json.NewEncoder(input)
	reader.Buffer(make([]byte, 4096), 4<<20)
	request := func(id int, method string, params map[string]any) json.RawMessage {
		t.Helper()
		if err := writer.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
			t.Fatal(err)
		}
		for reader.Scan() {
			var response struct {
				ID     int             `json:"id"`
				Result json.RawMessage `json:"result"`
				Error  json.RawMessage `json:"error"`
			}
			if json.Unmarshal(reader.Bytes(), &response) == nil && response.ID == id {
				if len(response.Error) != 0 {
					t.Fatalf("native fixture %s failed: %s", method, response.Error)
				}
				return response.Result
			}
		}
		t.Fatalf("native fixture %s ended before ACK: %v", method, reader.Err())
		return nil
	}
	request(1, "initialize", map[string]any{"clientInfo": map[string]any{"name": "owned-state-migration-fixture", "version": "1"}, "capabilities": map[string]any{"experimentalApi": true}})
	_ = writer.Encode(map[string]any{"jsonrpc": "2.0", "method": "initialized", "params": map[string]any{}})
	if thread == "" {
		raw := request(2, "thread/start", map[string]any{"cwd": home, "config": map[string]any{"features.multi_agent": false, "features.multi_agent_v2": false, "agents.enabled": false}})
		var started struct {
			Thread struct {
				ID string `json:"id"`
			} `json:"thread"`
		}
		if json.Unmarshal(raw, &started) != nil || started.Thread.ID == "" {
			t.Fatal("native fixture has no thread identity")
		}
		thread = started.Thread.ID
	}
	request(3, "thread/goal/set", map[string]any{"threadId": thread, "objective": "Native MCP lifecycle probe; remain paused.", "status": "paused", "origin": "user"})
	request(4, "thread/unsubscribe", map[string]any{"threadId": thread})
	for id := 5; id < 205; id++ {
		raw := request(id, "thread/loaded/list", map[string]any{})
		var loaded struct {
			Data *[]string `json:"data"`
		}
		if json.Unmarshal(raw, &loaded) != nil || loaded.Data == nil {
			t.Fatal("native fixture returned invalid loaded threads")
		}
		if len(*loaded.Data) == 0 {
			if status != "paused" {
				request(id+1, "thread/goal/set", map[string]any{"threadId": thread, "status": status, "origin": "user"})
			}
			return thread
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("native fixture did not unload its source thread")
	return thread
}

func copyCodexNativeSmokeAccount(t *testing.T, account, home string) {
	t.Helper()
	for _, name := range []string{"config.toml", "auth.json"} {
		data, err := os.ReadFile(filepath.Join(account, name))
		if err != nil {
			t.Fatalf("read isolated account %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(home, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCodexRealStoredActiveGoalQueryDoesNotRunOrPause(t *testing.T) {
	if os.Getenv("MULTICA_RUN_REAL_AGENT_SMOKE") != "1" {
		t.Skip("real native active Goal query is opt-in")
	}
	executable, account := os.Getenv("MULTICA_CODEX_CONTROL_SMOKE_PATH"), os.Getenv("MULTICA_CODEX_CONTROL_SMOKE_HOME")
	if executable == "" || account == "" {
		t.Fatal("explicit CLI and isolated account required")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	version, err := agent.DetectVersion(ctx, agent.NewCommand(executable, nil))
	if err != nil {
		t.Fatal(err)
	}
	source, home, store := t.TempDir(), t.TempDir(), t.TempDir()
	copyCodexNativeSmokeAccount(t, account, source)
	copyCodexNativeSmokeAccount(t, account, home)
	thread := createCodexGoalFixture(t, ctx, executable, source, "", "active")
	pool, err := agent.NewCodexProcessPool(filepath.Join(t.TempDir(), "pool"), 1, time.Minute, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	state, err := PrepareCodexNativeState(ctx, home, source, store, "owned-runtime", thread, pool.RetireHome)
	if err != nil {
		t.Fatal(err)
	}
	backend, err := agent.ResolveBackend("codex", agent.Config{ExecutablePath: executable, CodexVersion: version, TaskID: "owned-active-goal-query", Logger: slog.Default(), CodexPool: pool, ResidentSessionKey: "owned-active-goal-query", CodexResumePath: state.ResumePath, Env: map[string]string{"CODEX_HOME": home, "CODEX_SQLITE_HOME": state.SQLiteHome}})
	if err != nil {
		t.Fatal(err)
	}
	session, err := backend.Execute(ctx, "", agent.ExecOptions{Cwd: home, Command: "/goal show", CommandOnly: true, ResumeSessionID: thread, Timeout: 25 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	for range session.Messages {
	}
	result := <-session.Result
	if result.Status != "completed" || result.SessionID != thread || !strings.Contains(result.Output, "进行中") || len(result.Usage) != 0 {
		t.Fatalf("active Goal query changed its lifecycle: status=%s error=%s output=%s", result.Status, result.Error, result.Output)
	}
	database, err := sql.Open("sqlite", filepath.Join(state.SQLiteHome, "goals_1.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var status string
	if err := database.QueryRowContext(ctx, "SELECT status FROM thread_goals WHERE thread_id=?", thread).Scan(&status); err != nil || status != "active" {
		t.Fatalf("query paused or replaced the original Goal: %s %v", status, err)
	}
	for _, control := range []struct{ command, status string }{{"/goal pause", "paused"}, {"/goal complete", "complete"}, {"/goal clear", ""}} {
		session, err := backend.Execute(ctx, "", agent.ExecOptions{Cwd: home, Command: control.command, CommandOnly: true, ResumeSessionID: thread, Timeout: 15 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		for range session.Messages {
		}
		result := <-session.Result
		if result.Status != "completed" || result.SessionID != thread || len(result.Usage) != 0 {
			t.Fatalf("stored control %s failed: %s %s", control.command, result.Status, result.Error)
		}
		err = database.QueryRowContext(ctx, "SELECT status FROM thread_goals WHERE thread_id=?", thread).Scan(&status)
		if control.status == "" {
			if err != sql.ErrNoRows {
				t.Fatalf("clear retained a native Goal: %v", err)
			}
		} else if err != nil || status != control.status {
			t.Fatalf("stored control %s status=%s error=%v", control.command, status, err)
		}
	}
	history, err := sql.Open("sqlite", filepath.Join(state.SQLiteHome, "thread_history_1.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer history.Close()
	var turns int
	if err := history.QueryRowContext(ctx, "SELECT count(*) FROM thread_turns WHERE thread_id=?", thread).Scan(&turns); err != nil || turns != 0 {
		t.Fatalf("query started native model turns: %d %v", turns, err)
	}
	t.Log("query_preserved_active=true pause_complete_clear_confirmed=true native_turns=0 model_usage_entries=0 resident_retained=true")
}

func TestCodexRealCompressedNativeSessionResume(t *testing.T) {
	if os.Getenv("MULTICA_RUN_REAL_AGENT_SMOKE") != "1" {
		t.Skip("real compressed native session smoke is opt-in")
	}
	executable, account := os.Getenv("MULTICA_CODEX_CONTROL_SMOKE_PATH"), os.Getenv("MULTICA_CODEX_CONTROL_SMOKE_HOME")
	if executable == "" || account == "" {
		t.Fatal("explicit CLI and isolated account required")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	version, err := agent.DetectVersion(ctx, agent.NewCommand(executable, nil))
	if err != nil {
		t.Fatal(err)
	}
	source, home, store := t.TempDir(), t.TempDir(), t.TempDir()
	copyCodexNativeSmokeAccount(t, account, source)
	copyCodexNativeSmokeAccount(t, account, home)
	thread := createCodexGoalFixture(t, ctx, executable, source, "", "paused")
	paths := findCodexRollouts(filepath.Join(source, "sessions"), thread)
	if len(paths) != 1 {
		t.Fatal("source has no unique rollout")
	}
	input, err := os.Open(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	output, err := os.OpenFile(paths[0]+".zst", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		_ = input.Close()
		t.Fatal(err)
	}
	encoder, err := zstd.NewWriter(output, zstd.WithEncoderConcurrency(1))
	if err != nil {
		_ = input.Close()
		_ = output.Close()
		t.Fatal(err)
	}
	_, copyErr := io.Copy(encoder, input)
	if err := errors.Join(copyErr, input.Close(), encoder.Close(), output.Close()); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(paths[0]); err != nil {
		t.Fatal(err)
	}
	pool, err := agent.NewCodexProcessPool(filepath.Join(t.TempDir(), "pool"), 1, time.Minute, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	state, err := PrepareCodexNativeState(ctx, home, source, store, "owned-runtime", thread, pool.RetireHome)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(source); err != nil {
		t.Fatal(err)
	}
	backend, err := agent.ResolveBackend("codex", agent.Config{ExecutablePath: executable, CodexVersion: version, TaskID: "owned-compressed-resume", Logger: slog.Default(), CodexPool: pool, ResidentSessionKey: "owned-compressed-resume", CodexResumePath: state.ResumePath, Env: map[string]string{"CODEX_HOME": home, "CODEX_SQLITE_HOME": state.SQLiteHome}})
	if err != nil {
		t.Fatal(err)
	}
	session, err := backend.Execute(ctx, "", agent.ExecOptions{Cwd: home, Command: "/plan", CommandOnly: true, ResumeSessionID: thread, Model: "gpt-6.1-sol", ThinkingLevel: "low", Timeout: 25 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	for range session.Messages {
	}
	result := <-session.Result
	if result.Status != "completed" || result.SessionID != thread || !strings.Contains(result.Output, "计划模式") || len(result.Usage) != 0 {
		t.Fatalf("compressed native resume: status=%s error=%s output=%s", result.Status, result.Error, result.Output)
	}
	plain := strings.TrimSuffix(state.ResumePath, ".zst")
	if _, err := os.Stat(plain); err != nil {
		t.Fatalf("native resume did not materialize a writable plain rollout: %v", err)
	}
	next := t.TempDir()
	copyCodexNativeSmokeAccount(t, account, next)
	if _, err := PrepareCodexNativeState(ctx, next, "", store, "owned-runtime", thread, pool.RetireHome); err != nil {
		t.Fatalf("next task could not reuse the decompressed native session: %v", err)
	}
	t.Log("compressed_source_removed=true native_resume_and_plan_update_confirmed=true model_usage_entries=0")
}
