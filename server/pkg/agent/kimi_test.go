package agent

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func TestKimiToolNameFromTitle(t *testing.T) {
	t.Parallel()
	tests := []struct {
		title string
		want  string
	}{
		{"Read file: /tmp/foo.go", "read_file"},
		{"read", "read_file"},
		{"Write: /tmp/bar.go", "write_file"},
		{"Edit", "edit_file"},
		{"Patch: /tmp/x", "edit_file"},
		{"Shell: ls -la", "terminal"},
		{"Bash", "terminal"},
		{"Run command: pwd", "terminal"},
		{"Search: foo", "search_files"},
		{"Glob: *.go", "glob"},
		{"Web search: golang acp", "web_search"},
		{"Fetch: https://example.com", "web_fetch"},
		{"Todo Write", "todo_write"},
		{"Todo List", "todo_list"},
		{"Custom Thing", "custom_thing"},
		{"", ""},
	}
	for _, tt := range tests {
		got := kimiToolNameFromTitle(tt.title)
		if got != tt.want {
			t.Errorf("kimiToolNameFromTitle(%q) = %q, want %q", tt.title, got, tt.want)
		}
	}
}

func fakeKimiACPScript() string {
	return `#!/bin/sh
if [ -n "$KIMI_ARGS_FILE" ]; then
  for arg in "$@"; do
    printf '%s\n' "$arg" >> "$KIMI_ARGS_FILE"
  done
fi
while IFS= read -r line; do
  id=$(printf '%s' "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
  case "$line" in
    *'"method":"initialize"'*)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":1,"agentCapabilities":{}}}\n' "$id"
      ;;
    *'"method":"session/new"'*)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"sessionId":"ses_fake"}}\n' "$id"
      ;;
    *'"method":"session/set_model"'*)
      printf '{"jsonrpc":"2.0","id":%s,"error":{"code":-32602,"message":"model not available: bogus-model"}}\n' "$id"
      exit 0
      ;;
  esac
done
`
}

func TestKimiBackendSetModelFailureFailsTask(t *testing.T) {
	t.Parallel()

	result := executeBackendScript(t, "kimi", "kimi", fakeKimiACPScript(), ExecOptions{
		Model:   "bogus-model",
		Timeout: 5 * time.Second,
	})
	assertACPModelFailure(t, result, "ses_fake")
}

// fakeKimiACPStaleResumeSetModelScript impersonates kimi-cli when a
// resumed session is gone and the caller picked a model:
// session/resume echoes the requested sessionId back, then
// session/set_model rejects the unknown session the way kimi-cli
// actually does — RequestError.invalid_params (-32602) with
// {"session_id": "Session not found"} in data
// (src/kimi_cli/acp/server.py, set_session_model).
func fakeKimiACPStaleResumeSetModelScript() string {
	return `#!/bin/sh
while IFS= read -r line; do
  id=$(printf '%s' "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
  case "$line" in
    *'"method":"initialize"'*)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":1,"agentCapabilities":{}}}\n' "$id"
      ;;
    *'"method":"session/resume"'*)
      sid=$(printf '%s' "$line" | sed -n 's/.*"sessionId":"\([^"]*\)".*/\1/p')
      printf '{"jsonrpc":"2.0","id":%s,"result":{"sessionId":"%s"}}\n' "$id" "$sid"
      ;;
    *'"method":"session/set_model"'*)
      printf '{"jsonrpc":"2.0","id":%s,"error":{"code":-32602,"message":"Invalid params","data":{"session_id":"Session not found"}}}\n' "$id"
      exit 0
      ;;
  esac
done
`
}

func TestKimiBackendClearsSessionIDWhenSetModelSessionNotFound(t *testing.T) {
	t.Parallel()

	result := executeBackendScript(t, "kimi", "kimi", fakeKimiACPStaleResumeSetModelScript(), ExecOptions{
		Timeout:         5 * time.Second,
		ResumeSessionID: "ses_stale",
		Model:           "kimi-for-coding",
	})

	assertStaleSessionModelFailure(t, result, "kimi-for-coding")
}

func TestKimiBackendInvokesACPSubcommand(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	argsFile := filepath.Join(tempDir, "argv.txt")

	executeBackendScript(t, "kimi", "kimi", fakeKimiACPScript(), ExecOptions{
		Model:   "bogus-model",
		Timeout: 5 * time.Second,
	}, func(cfg *Config) {
		cfg.Env = map[string]string{"KIMI_ARGS_FILE": argsFile}
	})

	lines := readTestLines(t, argsFile)
	if len(lines) < 1 {
		t.Fatalf("expected at least 1 arg (acp), got %d: %q", len(lines), lines)
	}
	if lines[0] != "acp" {
		t.Errorf("expected first arg to be acp, got %q (full: %q)", lines[0], lines)
	}
	for _, l := range lines {
		switch l {
		case "--yolo", "--auto-approve", "--yes", "-y":
			t.Errorf("kimi acp doesn't accept %q; auto-approval is handled in hermesClient.handleAgentRequest", l)
		}
	}
}

func TestKimiResumeIncludesMcpServers(t *testing.T) {
	t.Parallel()

	recordPath := filepath.Join(t.TempDir(), "frames.jsonl")
	executeBackendScript(t, "kimi", "kimi", fakeACPRecordingScript(recordPath, "ses_resume", `{}`), ExecOptions{
		Timeout:         5 * time.Second,
		ResumeSessionID: "ses_resume",
		McpConfig:       json.RawMessage(`{"mcpServers":{"fetch":{"command":"uvx"}}}`),
	})

	frame := findRecordedFrame(t, recordPath, "session/resume")
	params := frame["params"].(map[string]any)
	servers, ok := params["mcpServers"].([]any)
	if !ok {
		t.Fatalf("session/resume.mcpServers: got %T, want []any", params["mcpServers"])
	}
	if len(servers) != 1 || servers[0].(map[string]any)["name"] != "fetch" {
		t.Fatalf("session/resume.mcpServers: got %v, want one entry named fetch", servers)
	}
}
