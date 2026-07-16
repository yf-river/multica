package agent

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestOpenclawProcessOutputCurrentContracts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		input     string
		wantState string
		wantText  string
		wantError string
	}{
		{
			name:      "local",
			input:     `{"payloads":[{"text":"Hello "},{"text":"OpenClaw"}],"meta":{"agentMeta":{"sessionId":"ses-local","model":"claude-sonnet","usage":{"input":100,"output":50,"cacheRead":10,"cacheWrite":5}}}}`,
			wantState: "completed",
			wantText:  "Hello OpenClaw",
		},
		{
			name:      "gateway",
			input:     `{"runId":"run-1","status":"ok","result":{"payloads":[{"text":"Gateway reply"}],"meta":{"agentMeta":{"sessionId":"ses-gateway","model":"gpt-5","usage":{"input":12,"output":8}}}}}`,
			wantState: "completed",
			wantText:  "Gateway reply",
		},
		{
			name:      "gateway success without reply",
			input:     `{"runId":"run-2","status":"ok"}`,
			wantState: "completed",
		},
		{
			name:      "gateway failure",
			input:     `{"status":"error","summary":"Gateway unavailable","result":{}}`,
			wantState: "failed",
			wantError: "Gateway unavailable",
		},
		{
			name:      "non JSON stdout",
			input:     "plain assistant text",
			wantState: "failed",
			wantError: openclawNoParseableOutput,
		},
		{
			name:      "empty stdout",
			input:     "",
			wantState: "failed",
			wantError: openclawNoParseableOutput,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			b := &openclawBackend{cfg: Config{Logger: slog.Default()}}
			ch := make(chan Message, 8)
			result := b.processOutput(strings.NewReader(tt.input), ch)
			if result.status != tt.wantState || result.output != tt.wantText || result.errMsg != tt.wantError {
				t.Fatalf("got status=%q output=%q error=%q", result.status, result.output, result.errMsg)
			}
			if tt.name == "local" {
				if result.sessionID != "ses-local" || result.model != "claude-sonnet" {
					t.Fatalf("unexpected metadata: %+v", result)
				}
				wantUsage := TokenUsage{InputTokens: 100, OutputTokens: 50, CacheReadTokens: 10, CacheWriteTokens: 5}
				if result.usage != wantUsage {
					t.Fatalf("usage = %+v, want %+v", result.usage, wantUsage)
				}
			}
			close(ch)
		})
	}
}

func TestOpenclawProcessOutputReadError(t *testing.T) {
	t.Parallel()
	b := &openclawBackend{cfg: Config{Logger: slog.Default()}}
	result := b.processOutput(&ioErrReader{}, make(chan Message, 1))
	if result.status != "failed" || !strings.Contains(result.errMsg, "read stdout") {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestOpenclawProcessOutputRecordedFixture(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("testdata/openclaw-2026.5.5-stdout.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	b := &openclawBackend{cfg: Config{Logger: slog.Default()}}
	ch := make(chan Message, 8)
	result := b.processOutput(strings.NewReader(string(data)), ch)
	if result.status != "completed" || result.output != "hi" || result.sessionID == "" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if result.model != "anthropic/claude-opus-4.7" {
		t.Fatalf("model = %q", result.model)
	}
	wantUsage := TokenUsage{InputTokens: 34620, OutputTokens: 6, CacheWriteTokens: 46482}
	if result.usage != wantUsage {
		t.Fatalf("usage = %+v, want %+v", result.usage, wantUsage)
	}
	close(ch)
}

func TestBuildOpenclawArgs(t *testing.T) {
	t.Parallel()

	t.Run("minimal local", func(t *testing.T) {
		args := buildOpenclawArgs("do work", "ses-1", ExecOptions{}, slog.Default())
		want := []string{"agent", "--local", "--json", "--session-id", "ses-1", "--message", "do work"}
		if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
			t.Fatalf("args = %v, want %v", args, want)
		}
	})

	t.Run("model maps to agent and system prompt joins message", func(t *testing.T) {
		args := buildOpenclawArgs("task", "ses-2", ExecOptions{
			Model:        "research-agent",
			SystemPrompt: "Be precise.",
		}, slog.Default())
		if indexOf(args, "--model") >= 0 || indexOf(args, "--system-prompt") >= 0 {
			t.Fatalf("unsupported flag leaked into %v", args)
		}
		if valueAfter(args, "--agent") != "research-agent" {
			t.Fatalf("agent selection missing: %v", args)
		}
		if valueAfter(args, "--message") != "Be precise.\n\ntask" {
			t.Fatalf("message mismatch: %v", args)
		}
	})

	t.Run("daemon flags cannot be overridden", func(t *testing.T) {
		args := buildOpenclawArgs("task", "ses-4", ExecOptions{Model: "selected-agent", CustomArgs: []string{
			"--agent", "wrong", "--model", "wrong", "--system-prompt", "wrong", "--session-id", "wrong", "--message", "wrong",
		}}, slog.Default())
		if valueAfter(args, "--agent") != "selected-agent" || countOccurrences(args, "--agent") != 1 {
			t.Fatalf("agent selection was overridden: %v", args)
		}
		if indexOf(args, "--model") >= 0 || indexOf(args, "--system-prompt") >= 0 {
			t.Fatalf("blocked args survived: %v", args)
		}
		if countOccurrences(args, "--session-id") != 1 || countOccurrences(args, "--message") != 1 {
			t.Fatalf("managed args duplicated: %v", args)
		}
	})

	t.Run("gateway mode", func(t *testing.T) {
		args := buildOpenclawArgs("task", "ses-5", ExecOptions{
			OpenclawMode: "gateway",
			CustomArgs:   []string{"--local"},
			Timeout:      90 * time.Second,
		}, slog.Default())
		if indexOf(args, "--local") >= 0 {
			t.Fatalf("gateway mode retained --local: %v", args)
		}
		if valueAfter(args, "--timeout") != "90" {
			t.Fatalf("timeout missing: %v", args)
		}
	})
}

func indexOf(args []string, value string) int {
	for i, arg := range args {
		if arg == value {
			return i
		}
	}
	return -1
}

func valueAfter(args []string, flag string) string {
	index := indexOf(args, flag)
	if index < 0 || index+1 >= len(args) {
		return ""
	}
	return args[index+1]
}

func countOccurrences(args []string, value string) int {
	count := 0
	for _, arg := range args {
		if arg == value {
			count++
		}
	}
	return count
}

func TestParseOpenclawVersion(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input string
		want  string
		ok    bool
	}{
		{"2026.5.5", "2026.5.5", true},
		{"openclaw v2026.5.5 c37871e", "2026.5.5", true},
		{"openclaw 2026.5", "", false},
		{"openclaw build info", "", false},
	}
	for _, tt := range tests {
		got, ok := parseOpenclawVersion(tt.input)
		if got != tt.want || ok != tt.ok {
			t.Errorf("parseOpenclawVersion(%q) = %q, %v", tt.input, got, ok)
		}
	}
}

func TestCompareOpenclawVersion(t *testing.T) {
	t.Parallel()
	tests := []struct {
		a, b string
		want int
	}{
		{"2026.5.5", "2026.5.5", 0},
		{"2026.5.4", "2026.5.5", -1},
		{"2026.5.6", "2026.5.5", 1},
		{"2027.0.0", "2026.99.99", 1},
	}
	for _, tt := range tests {
		if got := compareOpenclawVersion(tt.a, tt.b); got != tt.want {
			t.Errorf("compareOpenclawVersion(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestOpenclawExecuteRejectsOldVersion(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fixture is POSIX-only")
	}
	fakePath := filepath.Join(t.TempDir(), "openclaw")
	writeTestExecutable(t, fakePath, []byte("#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo 'openclaw 2026.4.9'; exit 0; fi\nexit 99\n"))
	backend, err := New("openclaw", Config{ExecutablePath: fakePath, Logger: slog.Default()})
	if err != nil {
		t.Fatalf("new backend: %v", err)
	}
	_, err = backend.Execute(context.Background(), "prompt", ExecOptions{Timeout: time.Second})
	if err == nil {
		t.Fatal("expected version error")
	}
	for _, want := range []string{"2026.4.9", minOpenclawVersion, "openclaw update"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q: %v", want, err)
		}
	}
}

func TestOpenclawExecuteAllowsCurrentVersion(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fixture is POSIX-only")
	}
	fakePath := filepath.Join(t.TempDir(), "openclaw")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"--version\" ]; then echo 'openclaw 2026.5.5'; exit 0; fi\n" +
		"echo '{\"payloads\":[{\"text\":\"ok\"}],\"meta\":{}}'\n"
	writeTestExecutable(t, fakePath, []byte(script))
	backend, err := New("openclaw", Config{ExecutablePath: fakePath, Logger: slog.Default()})
	if err != nil {
		t.Fatalf("new backend: %v", err)
	}
	session, err := backend.Execute(context.Background(), "prompt", ExecOptions{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	go func() {
		for range session.Messages {
		}
	}()
	select {
	case result := <-session.Result:
		if result.Status != "completed" || result.Output != "ok" {
			t.Fatalf("unexpected result: %+v", result)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for result")
	}
}
