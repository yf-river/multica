package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestBuildCodebuddyArgs_Basic(t *testing.T) {
	t.Parallel()

	args := buildCodebuddyArgs(ExecOptions{
		Model:        "claude-sonnet-4-20250514",
		MaxTurns:     25,
		SystemPrompt: "You are an agent.",
	}, slog.Default())

	expected := []string{
		"-p",
		"--output-format", "stream-json",
		"--input-format", "stream-json",
		"--verbose",
		"--strict-mcp-config",
		"--permission-mode", "bypassPermissions",
		"--disallowedTools", "AskUserQuestion",
		"--model", "claude-sonnet-4-20250514",
		"--max-turns", "25",
		"--append-system-prompt", "You are an agent.",
	}

	if len(args) != len(expected) {
		t.Fatalf("expected %d args, got %d: %v", len(expected), len(args), args)
	}
	for i, want := range expected {
		if args[i] != want {
			t.Fatalf("args[%d] = %q, want %q\nfull args: %v", i, args[i], want, args)
		}
	}
}

func TestBuildCodebuddyArgs_InjectsEffort(t *testing.T) {
	t.Parallel()

	args := buildCodebuddyArgs(ExecOptions{
		ThinkingLevel: "high",
	}, slog.Default())

	if argValue(args, "--effort") != "high" {
		t.Fatalf("expected --effort high in args: %v", args)
	}
}

func TestBuildCodebuddyArgs_OmitsEffortWhenEmpty(t *testing.T) {
	t.Parallel()

	args := buildCodebuddyArgs(ExecOptions{}, slog.Default())

	if argIndex(args, "--effort") >= 0 {
		t.Fatalf("--effort should not appear when ThinkingLevel is empty: %v", args)
	}
}

func TestBuildCodebuddyArgs_BlocksUserEffortOverride(t *testing.T) {
	t.Parallel()

	args := buildCodebuddyArgs(ExecOptions{
		ThinkingLevel: "medium",
		CustomArgs:    []string{"--effort", "max"},
	}, slog.Default())

	if got := argValue(args, "--effort"); got != "medium" {
		t.Fatalf("expected --effort medium, got %q", got)
	}
	if count := argCount(args, "--effort"); count != 1 {
		t.Fatalf("expected exactly 1 --effort, got %d in: %v", count, args)
	}
}

func TestBuildCodebuddyArgs_ExtraArgsBeforeCustomArgs(t *testing.T) {
	t.Parallel()

	args := buildCodebuddyArgs(ExecOptions{
		ExtraArgs:  []string{"--output-format", "text", "--max-budget-usd", "1.00"},
		CustomArgs: []string{"--max-budget-usd", "2.00", "--permission-mode", "plan"},
	}, slog.Default())
	assertFilteredArgsPreserveLayerOrder(t, args)
}

func TestBuildCodebuddyArgs_Resume(t *testing.T) {
	t.Parallel()

	args := buildCodebuddyArgs(ExecOptions{
		ResumeSessionID: "sess-abc123",
	}, slog.Default())

	if argValue(args, "--resume") != "sess-abc123" {
		t.Fatalf("expected --resume sess-abc123 in args: %v", args)
	}
}

func TestBuildCodebuddyArgs_AppliesToolEnvelope(t *testing.T) {
	t.Parallel()

	args := buildCodebuddyArgs(ExecOptions{
		AllowedBuiltinTools: []string{"Bash"},
		AllowedTools:        []string{"Bash(multica:*)"},
		DisallowedTools:     []string{"TaskCreate", "Agent", "Read", "TaskCreate"},
		CustomArgs:          []string{"--tools", "default", "--allowedTools", "Read,Edit"},
	}, slog.Default())

	if got := argValue(args, "--tools"); got != "Bash" {
		t.Fatalf("expected --tools Bash, got %q in %v", got, args)
	}
	if got := argValue(args, "--permission-mode"); got != "bypassPermissions" {
		t.Fatalf("expected default --permission-mode bypassPermissions, got %q in %v", got, args)
	}
	if got := argValue(args, "--disallowedTools"); got != "AskUserQuestion,TaskCreate,Agent,Read" {
		t.Fatalf("unexpected --disallowedTools %q in %v", got, args)
	}
	if got := argValue(args, "--allowedTools"); got != "Bash(multica:*)" {
		t.Fatalf("unexpected --allowedTools %q in %v", got, args)
	}
	if strings.Contains(strings.Join(args, " "), "--tools default") || strings.Contains(strings.Join(args, " "), "--allowedTools Read,Edit") {
		t.Fatalf("custom tool envelope args should be filtered: %v", args)
	}
}

func TestBuildCodebuddyArgs_AppliesPermissionMode(t *testing.T) {
	t.Parallel()

	args := buildCodebuddyArgs(ExecOptions{PermissionMode: "default"}, slog.Default())

	if got := argValue(args, "--permission-mode"); got != "default" {
		t.Fatalf("expected --permission-mode default, got %q in %v", got, args)
	}
}

func TestCodebuddyExecute_Success(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fixture is POSIX-only")
	}

	fakePath := filepath.Join(t.TempDir(), "codebuddy")
	script := "#!/bin/sh\n" +
		"IFS= read -r _\n" +
		`printf '%s\n' '{"type":"system","session_id":"sess-cb-001"}'` + "\n" +
		`printf '%s\n' '{"type":"assistant","message":{"role":"assistant","model":"claude-sonnet-4-20250514","content":[{"type":"text","text":"Hello from codebuddy"}]}}'` + "\n" +
		`printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"session_id":"sess-cb-001","result":"Hello from codebuddy","modelUsage":{"claude-sonnet-4-20250514":{"inputTokens":100,"outputTokens":50,"cacheReadInputTokens":10,"cacheCreationInputTokens":5}}}'` + "\n"
	writeTestExecutable(t, fakePath, []byte(script))

	b := &codebuddyBackend{cfg: Config{ExecutablePath: fakePath, Logger: slog.Default()}}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	session, err := b.Execute(ctx, "say hello", ExecOptions{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	// Drain messages.
	var gotText bool
	for msg := range session.Messages {
		if msg.Type == MessageText && msg.Content == "Hello from codebuddy" {
			gotText = true
		}
	}
	if !gotText {
		t.Fatal("expected text message 'Hello from codebuddy'")
	}

	select {
	case result, ok := <-session.Result:
		if !ok {
			t.Fatal("result channel closed without a value")
		}
		if result.Status != "completed" {
			t.Fatalf("expected status=completed, got %q (error=%q)", result.Status, result.Error)
		}
		if result.Output != "Hello from codebuddy" {
			t.Fatalf("expected output 'Hello from codebuddy', got %q", result.Output)
		}
		if result.SessionID != "sess-cb-001" {
			t.Fatalf("expected session_id=sess-cb-001, got %q", result.SessionID)
		}
		usage, ok := result.Usage["claude-sonnet-4-20250514"]
		if !ok {
			t.Fatalf("expected usage for claude-sonnet-4-20250514, got %#v", result.Usage)
		}
		if usage.InputTokens != 100 || usage.OutputTokens != 50 || usage.CacheReadTokens != 10 || usage.CacheWriteTokens != 5 {
			t.Fatalf("unexpected usage: %+v", usage)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for result")
	}
}

func TestCodebuddyExecute_NotFound(t *testing.T) {
	t.Parallel()

	b := &codebuddyBackend{cfg: Config{ExecutablePath: "/nonexistent/path/codebuddy", Logger: slog.Default()}}

	ctx := context.Background()
	_, err := b.Execute(ctx, "prompt", ExecOptions{})
	if err == nil {
		t.Fatal("expected error for missing executable")
	}
	if !strings.Contains(err.Error(), "codebuddy executable not found") {
		t.Fatalf("expected 'codebuddy executable not found' in error, got %q", err.Error())
	}
}

func TestCodebuddyExecuteSurfacesStderr(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fixture is POSIX-only")
	}

	script := "#!/bin/sh\n" +
		"IFS= read -r _\n" +
		"echo \"FATAL ERROR: segfault in codebuddy runtime\" >&2\n" +
		"exit 1\n"
	result := executeBackendScript(t, "codebuddy", "codebuddy", script, ExecOptions{Timeout: 5 * time.Second})
	if result.Status != "failed" {
		t.Fatalf("expected status=failed, got %q (error=%q)", result.Status, result.Error)
	}
	if !strings.Contains(result.Error, "codebuddy exited with error") {
		t.Fatalf("expected error to mention exit, got %q", result.Error)
	}
	if !strings.Contains(result.Error, "segfault in codebuddy runtime") {
		t.Fatalf("expected error to include stderr content, got %q", result.Error)
	}
	if !strings.Contains(result.Error, "codebuddy stderr:") {
		t.Fatalf("expected stderr label in error, got %q", result.Error)
	}
}

func TestWriteCodebuddyInput(t *testing.T) {
	t.Parallel()

	var buf strings.Builder
	err := writeClaudeStreamInput(&buf, "hello world", "codebuddy")
	if err != nil {
		t.Fatalf("writeClaudeStreamInput: %v", err)
	}

	data := buf.String()
	if len(data) == 0 || data[len(data)-1] != '\n' {
		t.Fatalf("expected newline-terminated payload, got %q", data)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(data)), &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload["type"] != "user" {
		t.Fatalf("expected type user, got %v", payload["type"])
	}

	message, ok := payload["message"].(map[string]any)
	if !ok {
		t.Fatalf("expected message object, got %T", payload["message"])
	}
	if message["role"] != "user" {
		t.Fatalf("expected role user, got %v", message["role"])
	}

	content, ok := message["content"].([]any)
	if !ok || len(content) != 1 {
		t.Fatalf("expected one content block, got %v", message["content"])
	}
	block, ok := content[0].(map[string]any)
	if !ok {
		t.Fatalf("expected content block object, got %T", content[0])
	}
	if block["type"] != "text" || block["text"] != "hello world" {
		t.Fatalf("unexpected content block: %v", block)
	}
}

func TestCodebuddyHandleAssistantText(t *testing.T) {
	t.Parallel()

	ch := make(chan Message, 10)
	var output strings.Builder

	msg := claudeStreamMessage{
		Type: "assistant",
		Message: mustMarshal(t, claudeStreamMessageContent{
			Role: "assistant",
			Content: []claudeStreamContentBlock{
				{Type: "text", Text: "codebuddy says hi"},
			},
		}),
	}

	handleClaudeStreamAssistant(msg, ch, &output, make(map[string]TokenUsage))

	if output.String() != "codebuddy says hi" {
		t.Fatalf("expected output 'codebuddy says hi', got %q", output.String())
	}
	select {
	case m := <-ch:
		if m.Type != MessageText || m.Content != "codebuddy says hi" {
			t.Fatalf("unexpected message: %+v", m)
		}
	default:
		t.Fatal("expected message on channel")
	}
}

func TestDiscoverCodebuddyModels_UsesACPModelCatalog(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fixture is POSIX-only")
	}

	fakePath := filepath.Join(t.TempDir(), "codebuddy")
	script := `#!/bin/sh
cat >/dev/null &
printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1}}'
printf '%s\n' '{"jsonrpc":"2.0","id":2,"result":{"sessionId":"s","models":{"availableModels":[{"modelId":"glm-5.2-ioa","name":"GLM-5.2"},{"modelId":"deepseek-v4-pro-ioa","name":"Deepseek-V4-Pro"},{"modelId":"echo","name":"Echo"}],"currentModelId":"deepseek-v4-pro-ioa"}}}'
sleep 1
`
	writeTestExecutable(t, fakePath, []byte(script))

	models := discoverCodebuddyModels(context.Background(), fakePath)
	if len(models) != 3 {
		t.Fatalf("expected 3 ACP models, got %d: %+v", len(models), models)
	}
	byID := map[string]Model{}
	for _, m := range models {
		byID[m.ID] = m
	}
	if byID["glm-5.2-ioa"].Provider != "zhipu" {
		t.Fatalf("glm provider = %q, want zhipu", byID["glm-5.2-ioa"].Provider)
	}
	if got := byID["deepseek-v4-pro-ioa"]; got.Provider != "deepseek" || !got.Default {
		t.Fatalf("deepseek model = %+v, want provider=deepseek default=true", got)
	}
	if byID["echo"].Provider != "" {
		t.Fatalf("echo provider = %q, want empty", byID["echo"].Provider)
	}
}

func TestParseCodebuddyModelList_DedupesAndGroups(t *testing.T) {
	t.Parallel()
	models := parseCodebuddyModelList("gpt-5.5, gpt-5.5\nkimi-k2.6-ioa hy3-preview-ioa")
	if len(models) != 3 {
		t.Fatalf("expected 3 unique models, got %d: %+v", len(models), models)
	}
	if !models[0].Default {
		t.Fatal("first parsed model should be default")
	}
	if models[1].Provider != "kimi" {
		t.Fatalf("second provider = %q, want kimi", models[1].Provider)
	}
	if models[2].Provider != "hunyuan" {
		t.Fatalf("third provider = %q, want hunyuan", models[2].Provider)
	}
}

func TestDiscoverCodebuddyModelsUsesEnv(t *testing.T) {
	t.Setenv("MULTICA_CODEBUDDY_MODELS", "glm-5.1-ioa, minimax-m2.7-ioa")

	models := discoverCodebuddyModels(context.Background(), filepath.Join(t.TempDir(), "missing-codebuddy"))
	if len(models) != 2 {
		t.Fatalf("expected 2 models from MULTICA_CODEBUDDY_MODELS, got %d: %+v", len(models), models)
	}
	if models[0].ID != "glm-5.1-ioa" || models[0].Provider != "zhipu" || !models[0].Default {
		t.Fatalf("unexpected first env model: %+v", models[0])
	}
	if models[1].ID != "minimax-m2.7-ioa" || models[1].Provider != "minimax" {
		t.Fatalf("unexpected second env model: %+v", models[1])
	}
}

func TestCodebuddyStaticModels_ExpandedFallback(t *testing.T) {
	t.Parallel()
	models := codebuddyStaticModels()
	providers := map[string]bool{}
	ids := map[string]bool{}
	for _, m := range models {
		providers[m.Provider] = true
		ids[m.ID] = true
	}
	for _, provider := range []string{"anthropic", "google", "openai", "zhipu", "minimax", "kimi", "hunyuan", "deepseek"} {
		if !providers[provider] {
			t.Fatalf("fallback missing provider %q: %+v", provider, models)
		}
	}
	for _, id := range []string{"glm-5.2-ioa", "deepseek-v4-pro-ioa", "minimax-m3-ioa", "claude-opus-4.8"} {
		if !ids[id] {
			t.Fatalf("fallback missing model %q: %+v", id, models)
		}
	}
}

func TestIsKnownThinkingValue_Codebuddy(t *testing.T) {
	t.Parallel()
	cases := []struct {
		value string
		want  bool
	}{
		{"", true},
		{"low", true},
		{"medium", true},
		{"high", true},
		{"xhigh", true},
		{"max", false},
		{"none", false},
	}
	for _, tc := range cases {
		got := IsKnownThinkingValue("codebuddy", tc.value)
		if got != tc.want {
			t.Errorf("IsKnownThinkingValue(codebuddy, %q) = %v, want %v", tc.value, got, tc.want)
		}
	}
}

func TestCodebuddyHandleUserToolResult(t *testing.T) {
	t.Parallel()

	ch := make(chan Message, 10)

	msg := claudeStreamMessage{
		Type: "user",
		Message: mustMarshal(t, claudeStreamMessageContent{
			Role: "user",
			Content: []claudeStreamContentBlock{
				{
					Type:      "tool_result",
					ToolUseID: "call-cb-1",
					Content:   mustMarshal(t, "tool output here"),
				},
			},
		}),
	}

	handleClaudeStreamUser(msg, ch, false)

	select {
	case m := <-ch:
		if m.Type != MessageToolResult || m.CallID != "call-cb-1" {
			t.Fatalf("unexpected message: %+v", m)
		}
	default:
		t.Fatal("expected message on channel")
	}
}
