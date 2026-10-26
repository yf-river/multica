//go:build agentintegration

package agent

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCodebuddyRealSupervisionMCPOnly(t *testing.T) {
	if os.Getenv("MULTICA_RUN_REAL_GOAL_MODEL_SMOKE") != "1" {
		t.Skip("real supervision MCP smoke is opt-in")
	}
	runtime := codebuddyRealSmokeRuntime(t)
	executable, home, multica := runtime.Path, runtime.Home, os.Getenv("MULTICA_SUPERVISION_SMOKE_PATH")
	if executable == "" || home == "" || multica == "" {
		t.Fatal("explicit CLI, isolated account home and Multica MCP binary required")
	}
	work, source := t.TempDir(), t.TempDir()
	marker, err := newCodebuddyGeneration()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "source.txt"), []byte(marker), 0600); err != nil {
		t.Fatal(err)
	}
	human := filepath.Join(work, "human-config")
	if err := os.Mkdir(human, 0700); err != nil {
		t.Fatal(err)
	}
	hookProof := filepath.Join(work, "human-hook-must-not-run")
	settings, _ := json.Marshal(map[string]any{"model": "deepseek-v4.1-flash", "hooks": map[string]any{"SessionStart": []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "touch " + shellQuote(hookProof)}}}}}})
	if err := os.WriteFile(filepath.Join(human, "settings.json"), settings, 0600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"HOME": home, "CODEBUDDY_CONFIG_DIR": human}
	directory, err := PrepareCodebuddySupervisionConfig(work, env)
	if err != nil {
		t.Fatal(err)
	}
	env["CODEBUDDY_CONFIG_DIR"] = directory
	audit, manifest := filepath.Join(work, "audit.jsonl"), filepath.Join(work, "manifest.json")
	data, _ := json.Marshal(map[string]any{"local_audit": audit, "roots": []any{map[string]any{"id": "authorized", "path": source}}})
	if err := os.WriteFile(manifest, data, 0600); err != nil {
		t.Fatal(err)
	}
	mcp, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{"supervision": map[string]any{"command": multica, "args": []string{"supervision-mcp", manifest}, "alwaysLoad": true}}})
	backend := &codebuddyBackend{cfg: Config{ExecutablePath: executable, BuiltinRuntime: true, CLIVersion: runtime.Version, Env: env, Logger: slog.Default()}}
	session, err := backend.Execute(t.Context(), "Use supervision_read exactly once with root=authorized, path=source.txt, offset=0, reason=verify the authorized source. Then reply only with the exact text returned by that tool. Use no other tools.", ExecOptions{Cwd: work, MCPToolsOnly: true, ApprovalPolicy: "never", Timeout: 100 * time.Second, McpConfig: mcp, SystemPrompt: "Inspect only the authorized source through the dedicated supervision MCP. Return the requested source text exactly."})
	if err != nil {
		t.Fatal(err)
	}
	tools := 0
	for msg := range session.Messages {
		if msg.Type == MessageToolUse {
			tools++
			if msg.Tool != "mcp__supervision__supervision_read" {
				t.Errorf("supervision used an unrelated tool: %s", msg.Tool)
			}
		}
	}
	result := <-session.Result
	if result.Status != "completed" || strings.TrimSpace(result.Output) != marker || tools != 1 || len(result.Usage) == 0 {
		t.Fatalf("supervision check failed: status=%s error=%s source_matches=%v tools=%d models=%d", result.Status, result.Error, strings.TrimSpace(result.Output) == marker, tools, len(result.Usage))
	}
	if _, err := os.Stat(hookProof); !os.IsNotExist(err) {
		t.Fatal("supervision executed the human startup hook")
	}
	raw, err := os.ReadFile(audit)
	if err != nil || !strings.Contains(string(raw), marker) || !strings.Contains(string(raw), "supervision_read") {
		t.Fatal("actual scoped MCP did not persist the source read audit")
	}
	t.Logf("Real dedicated supervision MCP passed; native configured model retained; startup hook isolated; source read audited; models=%d duration_ms=%d", len(result.Usage), result.DurationMs)
}
