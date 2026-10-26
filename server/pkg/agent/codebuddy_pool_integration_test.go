//go:build agentintegration

package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestCodebuddyRealResidentTaskGenerations(t *testing.T) {
	if os.Getenv("MULTICA_RUN_REAL_GOAL_MODEL_SMOKE") != "1" {
		t.Skip("real resident model smoke is opt-in")
	}
	executable, home := os.Getenv("MULTICA_CODEBUDDY_CONTROL_SMOKE_PATH"), os.Getenv("MULTICA_CODEBUDDY_CONTROL_SMOKE_HOME")
	if executable == "" || home == "" {
		t.Fatal("explicit executable and isolated account home required")
	}
	work := t.TempDir()
	logger := slog.Default()
	if destination := os.Getenv("MULTICA_CODEBUDDY_POOL_SMOKE_TRACE"); destination != "" {
		file, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		logger = slog.New(slog.NewJSONHandler(file, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	probe := filepath.Join(work, "mcp.py")
	const script = `import os,sys,json,hashlib
with open(sys.argv[1],"w") as f:json.dump({"pid":os.getpid(),"marker":os.environ.get("MULTICA_POOL_PROBE"),"token_sha256":hashlib.sha256(os.environ.get("MULTICA_TOKEN","").encode()).hexdigest()},f)
for line in sys.stdin:
 r=json.loads(line)
 if "id" not in r:continue
 method=r.get("method")
 if method=="initialize":result={"protocolVersion":"2024-11-05","capabilities":{"tools":{}},"serverInfo":{"name":"multica-resident-probe","version":"1"}}
 elif method=="tools/list":result={"tools":[]}
 else:result={}
 print(json.dumps({"jsonrpc":"2.0","id":r["id"],"result":result}),flush=True)
`
	if err := os.WriteFile(probe, []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	humanProof := filepath.Join(work, "human-hook-proof")
	humanSettings, _ := json.Marshal(map[string]any{
		"permissions": map[string]any{"deny": []string{"Read"}},
		"hooks":       map[string]any{"PreToolUse": []any{map[string]any{"matcher": "Bash", "hooks": []any{map[string]any{"type": "command", "command": "printf 'human-hook\\n' >> " + shellQuote(humanProof)}}}}},
	})
	if err := os.WriteFile(filepath.Join(work, "human-settings.json"), humanSettings, 0600); err != nil {
		t.Fatal(err)
	}
	taskSettings := filepath.Join(work, "task-settings.json")
	if err := os.WriteFile(taskSettings, []byte(`{"permissions":{"deny":["Write"]},"skillOverrides":{"owned-disabled-probe":"off"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	pool, err := NewCodebuddyProcessPool(filepath.Join(t.TempDir(), "pool"), 1, time.Minute, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	sid, firstPID := "", 0
	for round := 0; round < 3; round++ {
		marker, err := newCodebuddyGeneration()
		if err != nil {
			t.Fatal(err)
		}
		briefMarker, err := newCodebuddyGeneration()
		if err != nil {
			t.Fatal(err)
		}
		token := fmt.Sprintf("owned-smoke-token-%d", round)
		proof, mcpProof := filepath.Join(work, fmt.Sprintf("proof-%d", round)), filepath.Join(work, fmt.Sprintf("mcp-%d.json", round))
		cfg := Config{ExecutablePath: executable, BuiltinRuntime: true, CLIVersion: "2.176.0", CodebuddyPool: pool, ResidentSessionKey: "owned-resident-three-turn-smoke", TaskID: fmt.Sprintf("owned-warm-%d", round), Logger: logger, Env: map[string]string{"HOME": home, "CODEBUDDY_CONFIG_DIR": filepath.Join(home, "codebuddy"), "MULTICA_POOL_PROBE": marker, "MULTICA_TOKEN": token, "MULTICA_TASK_ID": fmt.Sprintf("owned-warm-%d", round)}}
		mcp, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{"resident-proof": map[string]any{"command": "python3", "args": []string{probe, mcpProof}}}})
		opts := ExecOptions{Cwd: work, Model: "deepseek-v4.1-flash", ResumeSessionID: sid, Timeout: 80 * time.Second, McpConfig: mcp, SystemPrompt: "Follow the current task brief. Use only the requested single Bash command. Do not read files or delegate.", CodebuddyRuntimeBrief: "Current task brief marker: " + briefMarker + ". Ignore markers in prior task briefs."}
		opts.CustomArgs = []string{"--settings", "human-settings.json"}
		opts.CodebuddySettingsPath = taskSettings
		before := codebuddyFingerprintProbe(t, cfg, opts)
		prompt := `Use Bash exactly once to execute this command verbatim: printf '%s' "${MULTICA_POOL_PROBE-UNSET}" > ` + shellQuote(proof) + ". Then reply only with the current task brief marker. Do not read files or call other tools."
		ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
		session, err := (&codebuddyBackend{cfg: cfg}).Execute(ctx, prompt, opts)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		for range session.Messages {
		}
		result := <-session.Result
		cancel()
		after := codebuddyFingerprintProbe(t, cfg, opts)
		var changed []string
		for name, value := range after {
			if before[name] != value {
				changed = append(changed, name)
			}
		}
		for name := range before {
			if _, exists := after[name]; !exists {
				changed = append(changed, name)
			}
		}
		sort.Strings(changed)
		t.Logf("Native resource changes during round %d: %v", round, changed)
		got, proofErr := os.ReadFile(proof)
		if result.Status != "completed" || proofErr != nil || string(got) != marker || strings.TrimSpace(result.Output) != briefMarker || len(result.Usage) == 0 {
			t.Fatalf("resident round %d failed: status=%s error=%s shell_matches=%v brief_matches=%v usage=%v", round, result.Status, result.Error, string(got) == marker, strings.TrimSpace(result.Output) == briefMarker, len(result.Usage) > 0)
		}
		humanOutput, err := os.ReadFile(humanProof)
		if err != nil || bytes.Count(humanOutput, []byte("human-hook\n")) != round+1 {
			t.Fatalf("native human hook was not retained for round %d: %v", round, err)
		}
		if sid != "" && result.SessionID != sid {
			t.Fatal("native session changed between leases")
		}
		sid = result.SessionID
		raw, err := os.ReadFile(mcpProof)
		if err != nil {
			t.Fatal(err)
		}
		var info struct {
			PID      int    `json:"pid"`
			Marker   string `json:"marker"`
			TokenSHA string `json:"token_sha256"`
		}
		if err := json.Unmarshal(raw, &info); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256([]byte(token))
		if info.Marker != marker || info.TokenSHA != hex.EncodeToString(digest[:]) {
			t.Fatal("MCP inherited the wrong task generation")
		}
		pool.mu.Lock()
		if len(pool.entries) != 1 {
			pool.mu.Unlock()
			t.Fatal("completed task did not retain resident process")
		}
		var entry *codebuddyPoolEntry
		for _, e := range pool.entries {
			entry = e
		}
		pid := entry.cmd.Process.Pid
		if round == 0 {
			firstPID = pid
		} else if pid != firstPID {
			pool.mu.Unlock()
			t.Fatal("native PID changed between warm task leases")
		}
		pool.mu.Unlock()
		rootEnv, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid))
		if err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"MULTICA_TOKEN=", "MULTICA_TASK_ID=", "MULTICA_POOL_PROBE=", codexExecutionOwnerEnv + "="} {
			for _, field := range bytes.Split(rootEnv, []byte{0}) {
				if bytes.HasPrefix(field, []byte(key)) {
					t.Fatal("resident root retained task credentials")
				}
			}
		}
		deadline := time.Now().Add(time.Second)
		for ownedProcessAlive(info.PID) && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if ownedProcessAlive(info.PID) {
			t.Fatal("task MCP survived lease release")
		}
		t.Logf("Resident round %d passed; pid=%d original_session=true shell_and_brief_current=true mcp_credentials_current=true task_mcp_stopped=true models=%d duration_ms=%d", round, pid, len(result.Usage), result.DurationMs)
	}
}

func codebuddyFingerprintProbe(t *testing.T, cfg Config, opts ExecOptions) map[string]string {
	t.Helper()
	result := map[string]string{}
	hash := func(path, name string) {
		_ = filepath.WalkDir(path, func(file string, entry os.DirEntry, err error) error {
			if os.IsNotExist(err) {
				return nil
			}
			if err != nil {
				t.Fatal(err)
			}
			if entry.IsDir() {
				return nil
			}
			rel, _ := filepath.Rel(path, file)
			body, readErr := os.ReadFile(file)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if entry.Name() == "entry_4348632e75ae0692960c04ccaf0a17df.info" {
				var x map[string]any
				if json.Unmarshal(body, &x) == nil {
					delete(x, "updatedAt")
					body, _ = json.Marshal(x)
				}
			}
			digest := sha256.Sum256(body)
			result[name+"/"+rel] = hex.EncodeToString(digest[:])
			return nil
		})
	}
	root := resolveSDKConfigDir(cfg.Env)
	put := func(name string, value any) {
		body, _ := json.Marshal(value)
		digest := sha256.Sum256(body)
		result[name] = hex.EncodeToString(digest[:])
	}
	rootEnv := codexResidentEnvironment(cfg.Env)
	sort.Strings(rootEnv)
	put("root-env", rootEnv)
	put("native-args", codebuddyNativeArgs(opts, cfg))
	if info, err := os.Stat(cfg.ExecutablePath); err == nil {
		put("executable-stat", fmt.Sprintf("%d/%d/%d", info.Size(), info.ModTime().UnixNano(), info.Mode()))
	}
	authFile := filepath.Join(cfg.Env["HOME"], ".local", "share", "CodeBuddyExtension", "Data", "Public", "auth", "Tencent-Cloud.coding-copilot.info")
	if info, err := os.Stat(authFile); err == nil {
		put("auth-file-mode", info.Mode().Perm())
	}
	if raw, err := os.ReadFile(authFile); err == nil {
		var document map[string]any
		if json.Unmarshal(raw, &document) == nil {
			if account, ok := document["account"].(map[string]any); ok {
				for key, value := range account {
					put("auth-account-field/"+key, value)
				}
			}
			if auth, ok := document["auth"].(map[string]any); ok {
				for key, value := range auth {
					put("auth-ticket-field/"+key, value)
				}
			}
		}
	}
	hash(filepath.Join(cfg.Env["HOME"], ".local", "share", "CodeBuddyExtension", "Data", "Public", "auth"), "account/auth")
	if fingerprint, err := codebuddyResidentFingerprint(cfg, opts); err == nil {
		result["effective-fingerprint"] = fingerprint
	} else {
		t.Fatal(err)
	}
	for _, name := range []string{"settings.json", ".mcp.json", "mcp.json", "skills", "plugins", "rules", "local_storage"} {
		hash(filepath.Join(root, name), "account/"+name)
	}
	for dir := opts.Cwd; dir != ""; dir = filepath.Dir(dir) {
		for _, name := range []string{"CODEBUDDY.md", ".codebuddy/settings.json", ".codebuddy/settings.local.json", ".codebuddy/skills", ".mcp.json"} {
			hash(filepath.Join(dir, name), "project/"+dir+"/"+name)
		}
		if dir == filepath.Dir(dir) {
			break
		}
	}
	return result
}
