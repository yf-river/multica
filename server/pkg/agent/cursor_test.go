package agent

import (
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestBuildCursorArgs(t *testing.T) {
	t.Parallel()

	args := buildCursorArgs("do something", ExecOptions{
		Cwd:   "/tmp/work",
		Model: "composer-1.5",
	}, slog.Default())

	expected := []string{
		"-p", "do something",
		"--output-format", "stream-json",
		"--force",
		"--model", "composer-1.5",
	}

	if len(args) != len(expected) {
		t.Fatalf("expected %d args, got %d: %v", len(expected), len(args), args)
	}
	for i, want := range expected {
		if args[i] != want {
			t.Errorf("args[%d] = %q, want %q", i, args[i], want)
		}
	}
}

func TestBuildCursorArgsWithResume(t *testing.T) {
	t.Parallel()

	args := buildCursorArgs("continue", ExecOptions{
		ResumeSessionID: "sess-123",
	}, slog.Default())

	hasResume := false
	for i, a := range args {
		if a == "--resume" && i+1 < len(args) && args[i+1] == "sess-123" {
			hasResume = true
		}
	}
	if !hasResume {
		t.Fatalf("expected --resume sess-123, got %v", args)
	}
}

func TestBuildCursorArgsMinimal(t *testing.T) {
	t.Parallel()

	args := buildCursorArgs("hello", ExecOptions{}, slog.Default())
	expected := []string{"-p", "hello", "--output-format", "stream-json", "--force"}

	if len(args) != len(expected) {
		t.Fatalf("expected %d args, got %d: %v", len(expected), len(args), args)
	}
}

func TestBuildCursorArgsIgnoresSystemPromptAndMaxTurns(t *testing.T) {
	t.Parallel()

	// cursor-agent CLI does not support --system-prompt or --max-turns;
	// verify they are NOT emitted even when set in ExecOptions.
	args := buildCursorArgs("task", ExecOptions{
		SystemPrompt: "You are helpful",
		MaxTurns:     5,
	}, slog.Default())

	for _, a := range args {
		if a == "--system-prompt" {
			t.Fatalf("unexpected --system-prompt in args: %v", args)
		}
		if a == "--max-turns" {
			t.Fatalf("unexpected --max-turns in args: %v", args)
		}
	}
}

func TestBuildCursorArgsCustomArgs(t *testing.T) {
	t.Parallel()

	args := buildCursorArgs("task", ExecOptions{
		CustomArgs: []string{"--extra", "val", "--force", "--output-format", "text"},
	}, slog.Default())

	// --extra val should be present; --force and --output-format should be filtered out.
	hasExtra := false
	hasBlockedForce := false
	hasBlockedFormat := false
	for i, a := range args {
		if a == "--extra" && i+1 < len(args) && args[i+1] == "val" {
			hasExtra = true
		}
	}
	// Count occurrences of --force (should be exactly 1 — the hardcoded one).
	forceCount := 0
	for _, a := range args {
		if a == "--force" {
			forceCount++
		}
		if a == "text" {
			hasBlockedFormat = true
		}
	}
	if forceCount > 1 {
		hasBlockedForce = true
	}
	if !hasExtra {
		t.Fatalf("expected --extra val in args, got %v", args)
	}
	if hasBlockedForce {
		t.Fatalf("--force from custom args should be filtered, got %v", args)
	}
	if hasBlockedFormat {
		t.Fatalf("--output-format from custom args should be filtered, got %v", args)
	}
}

func TestCursorHandleAssistantText(t *testing.T) {
	t.Parallel()

	b := &cursorBackend{cfg: Config{Logger: slog.Default()}}
	ch := make(chan Message, 10)
	var output strings.Builder

	evt := &cursorStreamEvent{
		Type: "assistant",
		Message: mustMarshal(t, cursorAssistantMessage{
			Content: []cursorContentBlock{
				{Type: "text", Text: "Hello from Cursor"},
			},
		}),
	}

	b.handleCursorAssistant(evt, ch, &output)

	if output.String() != "Hello from Cursor" {
		t.Fatalf("expected output 'Hello from Cursor', got %q", output.String())
	}

	select {
	case m := <-ch:
		if m.Type != MessageText || m.Content != "Hello from Cursor" {
			t.Fatalf("unexpected message: %+v", m)
		}
	default:
		t.Fatal("expected message on channel")
	}
}

func TestCursorToolCallMessages(t *testing.T) {
	t.Parallel()

	started := cursorStreamEvent{
		Type:     "tool_call",
		Subtype:  "started",
		CallID:   "call-42",
		ToolCall: json.RawMessage(`{"readToolCall":{"args":{"path":"README.md"}}}`),
	}
	msg, ok := cursorToolCallMessage(&started)
	if !ok || msg.Type != MessageToolUse || msg.Tool != "read" || msg.CallID != "call-42" || msg.Input["path"] != "README.md" {
		t.Fatalf("unexpected started message: %+v, ok=%v", msg, ok)
	}

	completed := cursorStreamEvent{
		Type:     "tool_call",
		Subtype:  "completed",
		CallID:   "call-42",
		ToolCall: json.RawMessage(`{"readToolCall":{"args":{"path":"README.md"},"result":{"success":{"content":"hello"}}}}`),
	}
	msg, ok = cursorToolCallMessage(&completed)
	if !ok || msg.Type != MessageToolResult || msg.Tool != "read" || msg.CallID != "call-42" || !strings.Contains(msg.Output, `"hello"`) {
		t.Fatalf("unexpected completed message: %+v, ok=%v", msg, ok)
	}
}

func TestCursorErrorText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		evt  cursorStreamEvent
		want string
	}{
		{"error field", cursorStreamEvent{ErrorMsg: "bad request"}, "bad request"},
		{"detail field", cursorStreamEvent{Detail: "not found"}, "not found"},
		{"result field", cursorStreamEvent{ResultText: "failed"}, "failed"},
		{"empty", cursorStreamEvent{}, ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := cursorErrorText(&tc.evt)
			if got != tc.want {
				t.Errorf("cursorErrorText = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCursorUsageModelFallback(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		evtModel        string
		configuredModel string
		want            string
	}{
		{"event model wins", "gpt-5.3-codex", "composer-2.5", "gpt-5.3-codex"},
		{"configured model fallback", "", "composer-2.5", "composer-2.5"},
		{"default cursor", "", "", "cursor"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := cursorUsageModel(tc.evtModel, tc.configuredModel)
			if got != tc.want {
				t.Fatalf("cursorUsageModel(%q, %q) = %q, want %q", tc.evtModel, tc.configuredModel, got, tc.want)
			}
		})
	}
}

func TestCursorAccumulateResultUsageUsesConfiguredModel(t *testing.T) {
	t.Parallel()

	b := &cursorBackend{cfg: Config{Logger: slog.Default()}}
	usage := make(map[string]TokenUsage)
	evt := &cursorStreamEvent{
		Usage: &cursorUsage{InputTokens: 400, OutputTokens: 200},
	}
	b.accumulateResultUsage(usage, evt, "composer-2.5")
	u := usage["composer-2.5"]
	if u.InputTokens != 400 || u.OutputTokens != 200 {
		t.Fatalf("unexpected usage: %+v", u)
	}
	if _, ok := usage["cursor"]; ok {
		t.Fatalf("expected configured model key, got cursor fallback: %+v", usage)
	}
}

func TestCursorAccumulateResultUsage(t *testing.T) {
	t.Parallel()

	b := &cursorBackend{cfg: Config{Logger: slog.Default()}}

	t.Run("current_nested_usage", func(t *testing.T) {
		usage := make(map[string]TokenUsage)
		evt := &cursorStreamEvent{
			Model: "gpt-5.3",
			Usage: &cursorUsage{
				InputTokens:      200,
				OutputTokens:     100,
				CacheReadTokens:  50,
				CacheWriteTokens: 25,
			},
		}
		b.accumulateResultUsage(usage, evt, "")
		u := usage["gpt-5.3"]
		if u.InputTokens != 200 || u.OutputTokens != 100 || u.CacheReadTokens != 50 || u.CacheWriteTokens != 25 {
			t.Fatalf("unexpected usage: %+v", u)
		}
	})

	// No usage at all — early return, map unchanged.
	t.Run("no_usage", func(t *testing.T) {
		usage := make(map[string]TokenUsage)
		evt := &cursorStreamEvent{
			Model: "gpt-5.3",
		}
		b.accumulateResultUsage(usage, evt, "")
		if _, ok := usage["gpt-5.3"]; ok {
			t.Fatalf("expected no entry, got %+v", usage["gpt-5.3"])
		}
	})

	// Empty model defaults to "cursor".
	t.Run("default_model", func(t *testing.T) {
		usage := make(map[string]TokenUsage)
		evt := &cursorStreamEvent{
			Usage: &cursorUsage{InputTokens: 50, OutputTokens: 25},
		}
		b.accumulateResultUsage(usage, evt, "")
		u := usage["cursor"]
		if u.InputTokens != 50 || u.OutputTokens != 25 {
			t.Fatalf("unexpected usage: %+v (want input=50 output=25)", u)
		}
	})
}

func TestCursorUsageOnlyFromResult(t *testing.T) {
	t.Parallel()

	b := &cursorBackend{cfg: Config{Logger: slog.Default()}}
	ch := make(chan Message, 10)
	var output strings.Builder

	evt := &cursorStreamEvent{
		Type: "assistant",
		Message: mustMarshal(t, cursorAssistantMessage{
			Content: []cursorContentBlock{
				{Type: "text", Text: "hello"},
			},
		}),
	}

	b.handleCursorAssistant(evt, ch, &output)

	if output.String() != "hello" {
		t.Fatalf("expected 'hello', got %q", output.String())
	}

}

// TestCursorStreamEventUnmarshalNestedCamelCase verifies the stream-json shape
// emitted by the locally installed cursor-agent version.
func TestCursorStreamEventUnmarshalNestedCamelCase(t *testing.T) {
	t.Parallel()

	raw := `{"type":"result","subtype":"success","duration_ms":10606,"duration_api_ms":10606,"is_error":false,"result":"pong","session_id":"b729a81b-9825-471d-812d-377c547b91e4","request_id":"4126abbe-dbc7-4ea4-a83e-7fab284c559c","usage":{"inputTokens":26640,"outputTokens":40,"cacheReadTokens":467,"cacheWriteTokens":12}}`

	var evt cursorStreamEvent
	if err := json.Unmarshal([]byte(raw), &evt); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if evt.Usage == nil {
		t.Fatal("usage should be non-nil")
	}
	if evt.Usage.InputTokens != 26640 || evt.Usage.OutputTokens != 40 || evt.Usage.CacheReadTokens != 467 || evt.Usage.CacheWriteTokens != 12 {
		t.Fatalf("usage = %+v, want input=26640 output=40 cache_read=467 cache_write=12", evt.Usage)
	}

	b := &cursorBackend{cfg: Config{Logger: slog.Default()}}
	usage := make(map[string]TokenUsage)
	b.accumulateResultUsage(usage, &evt, "")
	u := usage["cursor"]
	if u.InputTokens != 26640 || u.OutputTokens != 40 || u.CacheReadTokens != 467 || u.CacheWriteTokens != 12 {
		t.Fatalf("accumulated usage = %+v, want input=26640 output=40 cache_read=467 cache_write=12", u)
	}
}
