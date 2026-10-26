//go:build agentintegration

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// 原生 CLI 与监督 MCP 真实执行；Responses 端脚本化，不使用真实模型或业务服务端。
func TestCodexNativeSupervisionToolBoundary(t *testing.T) {
	if os.Getenv("MULTICA_RUN_CODEX_NATIVE_POLICY_SMOKE") != "1" {
		t.Skip("native tool policy with scripted Responses is opt-in")
	}
	executable, home, multica, root := os.Getenv("MULTICA_CODEX_CONTROL_SMOKE_PATH"), os.Getenv("MULTICA_CODEX_CONTROL_SMOKE_HOME"), os.Getenv("MULTICA_SUPERVISION_SMOKE_PATH"), os.Getenv("MULTICA_CODEX_POLICY_SMOKE_ROOT")
	if executable == "" || home == "" || multica == "" || root == "" {
		t.Fatal("explicit CLI, metadata home, MCP binary and private artifact root required")
	}
	version, err := DetectVersion(t.Context(), NewCommand(executable, nil))
	parsed, versionErr := parseSemver(version)
	if err != nil || versionErr != nil || parsed != (semver{Minor: 164}) {
		t.Fatalf("unverified Codex: %s %v", version, err)
	}
	cache, err := os.ReadFile(filepath.Join(home, "models_cache.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, restricted := range []bool{false, true} {
		name := "baseline"
		if restricted {
			name = "restricted"
		}
		t.Run(name, func(t *testing.T) {
			dir, err := os.MkdirTemp(root, "codex-policy-"+name+"-")
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("Retained native policy artifacts: %s", dir)
			work, codexHome, source := filepath.Join(dir, "work"), filepath.Join(dir, "codex-home"), filepath.Join(dir, "source")
			for _, folder := range []string{work, codexHome, source} {
				if err := os.Mkdir(folder, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(codexHome, "models_cache.json"), cache, 0600); err != nil {
				t.Fatal(err)
			}
			const marker = "OWNED_SUPERVISION_SOURCE"
			const roleMarker = "OWNED_SUPERVISOR_ROLE_MARKER"
			const fileMarker = "UNRELATED_WORKDIR_INSTRUCTIONS"
			if err := os.WriteFile(filepath.Join(work, "AGENTS.md"), []byte(fileMarker), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(source, "source.txt"), []byte(marker), 0600); err != nil {
				t.Fatal(err)
			}
			audit, manifest, proof := filepath.Join(dir, "audit.jsonl"), filepath.Join(dir, "manifest.json"), filepath.Join(work, "owned-write-proof.txt")
			body, _ := json.Marshal(map[string]any{"local_audit": audit, "roots": []any{map[string]any{"id": "authorized", "path": source}}})
			if err := os.WriteFile(manifest, body, 0600); err != nil {
				t.Fatal(err)
			}
			var count atomic.Int32
			var mu sync.Mutex
			var requests [][]byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" {
					http.Error(w, "owned endpoint", http.StatusNotFound)
					return
				}
				var input json.RawMessage
				if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<20)).Decode(&input); err != nil {
					http.Error(w, "invalid JSON", 400)
					return
				}
				mu.Lock()
				requests = append(requests, append([]byte(nil), input...))
				mu.Unlock()
				index := count.Add(1)
				var item map[string]any
				switch index {
				case 1:
					patch := "*** Begin Patch\n*** Add File: " + proof + "\n+OWNED_WRITE_CANARY\n*** End Patch"
					encoded, _ := json.Marshal(patch)
					item = map[string]any{"type": "custom_tool_call", "id": "ct_owned", "call_id": "call_owned", "name": "exec", "input": "text(await tools.apply_patch(" + string(encoded) + "));"}
				case 2:
					item = map[string]any{"type": "function_call", "id": "fc_owned_mcp", "call_id": "call_owned_mcp", "namespace": "mcp__supervision", "name": "supervision_read", "arguments": `{"root":"authorized","path":"source.txt","offset":0,"reason":"verify scoped read"}`}
				default:
					item = map[string]any{"type": "message", "id": "msg_owned", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "DONE", "annotations": []any{}}}}
				}
				id := fmt.Sprintf("resp_owned_%d", index)
				response := map[string]any{"id": id, "object": "response", "model": "gpt-6.1-sol", "status": "completed", "output": []any{item}, "usage": map[string]any{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}
				events := []map[string]any{{"type": "response.created", "response": map[string]any{"id": id, "object": "response", "model": "gpt-6.1-sol", "status": "in_progress", "output": []any{}}}, {"type": "response.output_item.added", "output_index": 0, "item": item}, {"type": "response.output_item.done", "output_index": 0, "item": item}, {"type": "response.completed", "response": response}}
				w.Header().Set("Content-Type", "text/event-stream")
				for seq, event := range events {
					event["sequence_number"] = seq
					raw, _ := json.Marshal(event)
					_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event["type"], raw)
					w.(http.Flusher).Flush()
				}
			}))
			defer server.Close()
			config := map[string]any{"model": "gpt-6.1-sol", "model_provider": "owned_capture", "sandbox_mode": "danger-full-access", "approval_policy": "never", "model_providers": map[string]any{"owned_capture": map[string]any{"name": "OpenAI", "base_url": server.URL + "/v1", "wire_api": "responses", "env_key": "OWNED_CODEX_CAPTURE_KEY", "request_max_retries": 0, "stream_max_retries": 0}}, "features": map[string]any{"remote_models": false, "code_mode": map[string]any{"enabled": true}, "tool_registry": map[string]any{"turn_metadata_includes_tool_info": true}}}
			settings, err := toml.Marshal(config)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(codexHome, "config.toml"), settings, 0600); err != nil {
				t.Fatal(err)
			}
			mcp, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{"supervision": map[string]any{"command": multica, "args": []string{"supervision-mcp", manifest}}}})
			backend := &codexBackend{cfg: Config{ExecutablePath: executable, BuiltinRuntime: true, CodexVersion: version, TaskID: "owned-supervision-policy", Logger: slog.Default(), Env: map[string]string{"CODEX_HOME": codexHome, "OWNED_CODEX_CAPTURE_KEY": "owned-local-fake-key"}}}
			ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
			defer cancel()
			session, err := backend.Execute(ctx, "Perform the owned tool boundary check.", ExecOptions{Cwd: work, Model: "gpt-6.1-sol", SystemPrompt: roleMarker, ApprovalPolicy: "never", MCPToolsOnly: restricted, McpConfig: mcp, Timeout: 40 * time.Second})
			if err != nil {
				t.Fatal(err)
			}
			for range session.Messages {
				if restricted && (session.CommandReady() || session.SupplementReady()) {
					t.Error("supervision advertised interactive native controls")
				}
			}
			result := <-session.Result
			mu.Lock()
			recorded := append([][]byte(nil), requests...)
			mu.Unlock()
			for index, request := range recorded {
				if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("request-%d.json", index)), request, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if result.Status != "completed" || result.Output != "DONE" {
				t.Fatalf("native policy run failed: status=%s error=%s output=%q", result.Status, result.Error, result.Output)
			}
			_, writeErr := os.Stat(proof)
			if restricted && !os.IsNotExist(writeErr) || !restricted && writeErr != nil {
				t.Fatalf("write boundary mismatch: restricted=%v error=%v", restricted, writeErr)
			}
			if len(recorded) < 3 {
				t.Fatalf("MCP followup was not observed: requests=%d", len(recorded))
			}
			if restricted && (strings.Count(string(recorded[0]), roleMarker) != 1 || strings.Contains(string(recorded[0]), fileMarker)) {
				t.Fatal("supervision role instructions were missing, duplicated, or mixed with workdir instructions")
			}
			if restricted && !strings.Contains(string(recorded[1]), "No usable execution environment") {
				t.Fatal("write did not fail at the native environment boundary")
			}
			auditBody, err := os.ReadFile(audit)
			if err != nil || !strings.Contains(string(auditBody), marker) {
				t.Fatalf("actual supervision MCP did not read the authorized source: %v", err)
			}
			t.Logf("Codex %s native boundary passed; owned_write=%v dedicated_MCP_read=true Responses=scripted no_real_model=true", version, !restricted)
		})
	}
}
