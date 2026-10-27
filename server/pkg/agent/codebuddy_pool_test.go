package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCodebuddyPoolFixtureProcess(t *testing.T) {
	if os.Getenv("CODEBUDDY_POOL_FIXTURE") != "1" {
		return
	}
	allowTaskEnv := os.Getenv("CODEBUDDY_POOL_ALLOW_TASK_ENV") == "1"
	if allowTaskEnv && os.Getenv("MULTICA_TOKEN") != os.Getenv("CODEBUDDY_POOL_EXPECT_TASK_TOKEN") {
		os.Exit(4)
	}
	for _, pair := range os.Environ() {
		key, _, _ := strings.Cut(pair, "=")
		if !allowTaskEnv && strings.HasPrefix(strings.ToUpper(key), "MULTICA_") && key != codexPoolOwnerEnv {
			os.Exit(3)
		}
	}
	scanner, encoder := bufio.NewScanner(os.Stdin), json.NewEncoder(os.Stdout)
	count := 0
	var lastCID, promptCID, sid string
	var waitingPrompt json.RawMessage
	notify := func(cid, text string) {
		_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": sid, "update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]string{"type": "text", "text": text}, "_meta": map[string]string{"codebuddy.ai/requestId": cid}}}})
	}
	respond := func(id json.RawMessage, result any) {
		_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	}
	for scanner.Scan() {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				SessionID string            `json:"sessionId"`
				Meta      map[string]string `json:"_meta"`
				Prompt    []struct {
					Text string `json:"text"`
				} `json:"prompt"`
			} `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			continue
		}
		file, err := os.OpenFile(os.Getenv("CODEBUDDY_POOL_FIXTURE_LOG"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err == nil {
			_, _ = fmt.Fprintln(file, request.Method)
			_ = file.Close()
		}
		result := any(map[string]any{})
		switch request.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": 1}
		case "session/new":
			count++
			sid = fmt.Sprintf("session-%d", count)
			result = map[string]any{"sessionId": sid}
		case "session/load":
			sid = request.Params.SessionID
			result = map[string]any{}
		case codebuddyGoalControlMethod:
			result = map[string]any{"ok": true, "hasGoal": false, "runInFlight": false}
		case "session/prompt":
			promptCID = request.Params.Meta["codebuddy.ai/conversationRequestId"]
			if lastCID != "" {
				notify(lastCID, "OLD_LEASE_OUTPUT")
			}
			lastCID = promptCID
			if len(request.Params.Prompt) > 0 && request.Params.Prompt[0].Text == "question" {
				waitingPrompt = append(json.RawMessage(nil), request.ID...)
				_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": "native-question-id", "method": "_codebuddy.ai/question", "params": map[string]any{"sessionId": sid, "inputType": "question", "schema": map[string]any{"questions": []any{map[string]any{"id": "q", "question": "Continue?", "options": []any{map[string]string{"label": "Yes"}}}}}}})
				continue
			}
			notify(promptCID, "fresh answer")
			result = map[string]any{"stopReason": "end_turn"}
		case "":
			if string(request.ID) == `"native-question-id"` && len(waitingPrompt) > 0 {
				notify(promptCID, "question answered")
				respond(waitingPrompt, map[string]any{"stopReason": "end_turn"})
				waitingPrompt = nil
			}
			continue
		}
		respond(request.ID, result)
	}
	os.Exit(0)
}

func TestCodebuddyResidentFingerprintSeparatesCacheAndAccount(t *testing.T) {
	cfg := codebuddyFixtureConfig(t)
	opts := ExecOptions{Cwd: t.TempDir()}
	storage := filepath.Join(cfg.Env["CODEBUDDY_CONFIG_DIR"], "local_storage")
	auth := filepath.Join(cfg.Env["HOME"], ".local", "share", "CodeBuddyExtension", "Data", "Public", "auth")
	if err := os.MkdirAll(storage, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(auth, 0700); err != nil {
		t.Fatal(err)
	}
	write := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(auth, "fixture.info"), "account-first")
	ab := filepath.Join(storage, "entry_4348632e75ae0692960c04ccaf0a17df.info")
	write(ab, `{"productFeatures":{"flag":true,"other":false},"productFeaturesConfig":{},"updatedAt":1}`)
	first, err := codebuddyResidentFingerprint(cfg, opts)
	if err != nil {
		t.Fatal(err)
	}
	write(ab, `{"productFeatures":{"other":false,"flag":true},"productFeaturesConfig":{},"updatedAt":2}`)
	write(filepath.Join(storage, "entry_604f48c944053e01d9546675443286c1.info"), `"new-product-cache"`)
	write(filepath.Join(storage, "entry_d43e96994f944cfb77961c2ea7d04605.info"), `[{"ts":2}]`)
	second, err := codebuddyResidentFingerprint(cfg, opts)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("native derived cache invalidated resident configuration")
	}
	write(ab, `{"productFeatures":{"flag":false},"productFeaturesConfig":{},"updatedAt":2}`)
	featureChanged, err := codebuddyResidentFingerprint(cfg, opts)
	if err != nil || featureChanged == second {
		t.Fatal("native feature change did not invalidate resident process")
	}
	write(filepath.Join(auth, "fixture.info"), "account-second")
	accountChanged, err := codebuddyResidentFingerprint(cfg, opts)
	if err != nil || accountChanged == featureChanged {
		t.Fatal("native account change did not invalidate resident process")
	}
}

func TestCodebuddyNativeMCPScopeKeepsTaskProcess(t *testing.T) {
	for _, name := range []string{".mcp.json", ".codebuddy.json"} {
		t.Run(name, func(t *testing.T) {
			cfg := codebuddyFixtureConfig(t)
			cfg.Env["CODEBUDDY_POOL_ALLOW_TASK_ENV"], cfg.Env["CODEBUDDY_POOL_EXPECT_TASK_TOKEN"] = "1", cfg.Env["MULTICA_TOKEN"]
			file := filepath.Join(cfg.Env["CODEBUDDY_CONFIG_DIR"], name)
			content := `{"mcpServers":{"native-user":{"command":"user-defined-command"}}}`
			if err := os.WriteFile(file, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			pool, err := NewCodebuddyProcessPool(filepath.Join(t.TempDir(), "pool"), 1, time.Minute, slog.Default())
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			cfg.CodebuddyPool = pool
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			session, err := (&codebuddyBackend{cfg: cfg}).Execute(ctx, "reply", ExecOptions{Cwd: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			for range session.Messages {
			}
			result := <-session.Result
			if result.Status != "completed" || result.Output != "fresh answer" {
				t.Fatalf("native scope lost its task environment: %+v", result)
			}
			if len(pool.entries) != 0 {
				t.Fatal("unmanaged MCP scope entered token-free resident process")
			}
			got, err := os.ReadFile(file)
			if err != nil || string(got) != content {
				t.Fatal("native MCP configuration was modified")
			}
		})
	}
}

func TestCodebuddyLocalMCPConfigurationChangesResidentIdentity(t *testing.T) {
	cfg := codebuddyFixtureConfig(t)
	opts := ExecOptions{Cwd: t.TempDir()}
	path := filepath.Join(cfg.Env["CODEBUDDY_CONFIG_DIR"], ".codebuddy.json")
	write := func(document string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(document), 0600); err != nil {
			t.Fatal(err)
		}
	}
	fingerprint := func() string {
		t.Helper()
		value, err := codebuddyResidentFingerprint(cfg, opts)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	write(`{"projects":{}}`)
	empty := fingerprint()
	if codebuddyNeedsTaskProcess(cfg, opts.Cwd) {
		t.Fatal("empty local metadata prevented resident execution")
	}
	write(`{"projects":{"/owned-project":{"mcpServers":{"local":{"command":"first-command"}}}}}`)
	first := fingerprint()
	if !codebuddyNeedsTaskProcess(cfg, opts.Cwd) || first == empty {
		t.Fatal("local MCP addition did not isolate and invalidate resident execution")
	}
	write(`{"projects":{"/owned-project":{"mcpServers":{"local":{"command":"second-command"}}}}}`)
	if fingerprint() == first {
		t.Fatal("local MCP configuration change did not invalidate resident execution")
	}
}

func TestCodebuddyResidentAuthRotationKeepsAccountBoundary(t *testing.T) {
	cfg := codebuddyFixtureConfig(t)
	opts := ExecOptions{Cwd: t.TempDir()}
	root := filepath.Join(cfg.Env["HOME"], ".local", "share", "CodeBuddyExtension", "Data", "Public", "auth")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "Tencent-Cloud.coding-copilot.info")
	write := func(uid, tenant, token string) {
		t.Helper()
		data, _ := json.Marshal(map[string]any{"account": map[string]any{"uid": uid, "enterpriseId": tenant, "type": "cloud", "isAdmin": false}, "auth": map[string]any{"accessToken": token, "refreshToken": token, "tokenType": "Bearer", "domain": "fixture.example", "lastRefreshTime": time.Now().UnixNano()}})
		if err := os.WriteFile(file, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("first-user", "first-tenant", "first-token")
	first, err := codebuddyResidentFingerprint(cfg, opts)
	if err != nil {
		t.Fatal(err)
	}
	write("first-user", "first-tenant", "rotated-token")
	rotated, err := codebuddyResidentFingerprint(cfg, opts)
	if err != nil || first != rotated {
		t.Fatal("native ticket rotation changed process identity")
	}
	write("first-user", "different-tenant", "rotated-token")
	tenant, err := codebuddyResidentFingerprint(cfg, opts)
	if err != nil || tenant == rotated {
		t.Fatal("organization switch kept the old process identity")
	}
	write("second-user", "different-tenant", "rotated-token")
	account, err := codebuddyResidentFingerprint(cfg, opts)
	if err != nil || account == tenant {
		t.Fatal("account switch kept the old process identity")
	}
}

func codebuddyFixtureConfig(t *testing.T) Config {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("resident process ownership requires Linux")
	}
	root := t.TempDir()
	config := filepath.Join(root, "codebuddy")
	if err := os.Mkdir(config, 0700); err != nil {
		t.Fatal(err)
	}
	return Config{ExecutablePath: os.Args[0], LaunchPrefix: []string{"-test.run=^TestCodebuddyPoolFixtureProcess$", "--"}, BuiltinRuntime: true, CLIVersion: "2.176.0", TaskID: "task", ResidentSessionKey: "workspace/runtime/agent/chat", Logger: slog.Default(), Env: map[string]string{"HOME": root, "CODEBUDDY_POOL_FIXTURE": "1", "CODEBUDDY_POOL_FIXTURE_LOG": filepath.Join(root, "calls"), "CODEBUDDY_CONFIG_DIR": config, "MULTICA_TASK_ID": "task", "MULTICA_TOKEN": "fixture-task-secret"}}
}

func TestCodebuddyResidentTasksReuseProcessAndFenceOldFrames(t *testing.T) {
	cfg := codebuddyFixtureConfig(t)
	var pins atomic.Int32
	cfg.PinResidentHome = func() func() { pins.Add(1); return func() { pins.Add(-1) } }
	pool, err := NewCodebuddyProcessPool(filepath.Join(t.TempDir(), "pool"), 1, time.Minute, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	cfg.CodebuddyPool = pool
	work, sid, firstPID, previousCID := t.TempDir(), "", 0, ""
	for round := 0; round < 3; round++ {
		cfg.TaskID = fmt.Sprintf("task-%d", round)
		cfg.Env["MULTICA_TASK_ID"], cfg.Env["MULTICA_TOKEN"] = cfg.TaskID, fmt.Sprintf("token-%d", round)
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		session, err := (&codebuddyBackend{cfg: cfg}).Execute(ctx, "reply", ExecOptions{Cwd: work, ResumeSessionID: sid})
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		var text strings.Builder
		for msg := range session.Messages {
			if msg.Type == MessageText {
				text.WriteString(msg.Content)
			}
		}
		result := <-session.Result
		cancel()
		if result.Status != "completed" || result.Output != "fresh answer" || text.String() != "fresh answer" || result.ProviderTraceID == "" || result.ProviderTraceID == previousCID {
			t.Fatalf("lease %d failed: result=%+v text=%q", round, result, text.String())
		}
		if sid != "" && result.SessionID != sid {
			t.Fatal("resident lease changed the native session")
		}
		sid, previousCID = result.SessionID, result.ProviderTraceID
		pool.mu.Lock()
		if len(pool.entries) != 1 {
			pool.mu.Unlock()
			t.Fatal("healthy lease did not retain process")
		}
		var entry *codebuddyPoolEntry
		for _, e := range pool.entries {
			entry = e
		}
		if round == 0 {
			firstPID = entry.cmd.Process.Pid
		} else if entry.cmd.Process.Pid != firstPID {
			pool.mu.Unlock()
			t.Fatal("native process was restarted")
		}
		if entry.busy || entry.hooks.active || len(entry.hooks.leaseCIDs) == 0 {
			pool.mu.Unlock()
			t.Fatal("lease did not quiesce")
		}
		pool.mu.Unlock()
	}
	calls, err := os.ReadFile(cfg.Env["CODEBUDDY_POOL_FIXTURE_LOG"])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(calls), "initialize\n") != 1 || strings.Count(string(calls), "session/new\n") != 1 || pins.Load() != 1 {
		t.Fatalf("unexpected startup or idle churn: calls=%q pins=%d", calls, pins.Load())
	}
	pool.Close()
	if pins.Load() != 0 {
		t.Fatal("resident root remained pinned")
	}
}

func TestCodebuddyResidentPoolSerializesCapacityAndRecoversOwnedProcess(t *testing.T) {
	cfg := codebuddyFixtureConfig(t)
	root := filepath.Join(t.TempDir(), "pool")
	pool, err := NewCodebuddyProcessPool(root, 1, time.Minute, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	opts := ExecOptions{Cwd: t.TempDir()}
	lease, err := pool.acquire(t.Context(), cfg, opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{cfg.ResidentSessionKey, "another-conversation"} {
		waiting := cfg
		waiting.ResidentSessionKey = key
		ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
		_, err := pool.acquire(ctx, waiting, opts)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("busy process admitted another lease: %v", err)
		}
	}
	pid := lease.entry.cmd.Process.Pid
	// 测试仅释放文件锁，模拟失联所有者；恢复只允许回收这份记录的随机进程标识。
	_ = pool.lock.Close()
	recovered, err := NewCodebuddyProcessPool(root, 1, time.Minute, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	select {
	case <-lease.entry.done:
	case <-time.After(3 * time.Second):
		t.Fatalf("owned process %d survived recovery", pid)
	}
	pool.Close()
}

func TestCodebuddyResidentReverseRequestPreservesStringID(t *testing.T) {
	cfg := codebuddyFixtureConfig(t)
	pool, err := NewCodebuddyProcessPool(filepath.Join(t.TempDir(), "pool"), 1, time.Minute, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	cfg.CodebuddyPool = pool
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	session, err := (&codebuddyBackend{cfg: cfg}).Execute(ctx, "question", ExecOptions{Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	for range session.Messages {
	}
	result := <-session.Result
	if result.Status != "completed" || result.Output != "question answered" {
		t.Fatalf("reverse request did not settle: %+v", result)
	}
}
