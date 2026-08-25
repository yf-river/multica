package agent

import (
	"log/slog"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestBuildPiArgs(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		prompt  string
		session string
		opts    ExecOptions
		want    []string
	}{
		{name: "full registry by default", prompt: "test prompt", session: "/tmp/session.jsonl", want: []string{"-p", "--mode", "json", "--session", "/tmp/session.jsonl", "test prompt"}},
		{name: "model and system prompt", prompt: "hello world", session: "/tmp/s.jsonl", opts: ExecOptions{Model: "anthropic/claude-sonnet-4-20250514", SystemPrompt: "be helpful"}, want: []string{"-p", "--mode", "json", "--session", "/tmp/s.jsonl", "--provider", "anthropic", "--model", "claude-sonnet-4-20250514", "--append-system-prompt", "be helpful", "hello world"}},
		{name: "custom tool restriction", prompt: "prompt", session: "/tmp/s.jsonl", opts: ExecOptions{CustomArgs: []string{"--tools", "read,bash"}}, want: []string{"-p", "--mode", "json", "--session", "/tmp/s.jsonl", "--tools", "read,bash", "prompt"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := buildPiArgs(tc.prompt, tc.session, tc.opts, slog.Default()); !slices.Equal(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// TestPiExecuteAttachesStdinPipe verifies that the Pi backend spawns the
// child with an explicit stdin pipe (FIFO) instead of leaving cmd.Stdin
// nil. Without an explicit pipe, Pi has been observed to block under
// systemd waiting for stdin events (#2188); attaching and immediately
// closing a pipe delivers a clean EOF on a FIFO and unblocks Pi.
//
// The probe is structural rather than behavioral: a shell script in
// place of `pi` inspects /proc/self/fd/0 and only emits a valid event
// stream if stdin is a FIFO. If the fix regresses (stdin nil → /dev/null
// char device), the fake exits non-zero and the test fails.
func TestPiExecuteAttachesStdinPipe(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "linux" {
		// /proc/self/fd/0 is Linux-specific; skipping elsewhere keeps
		// the assertion portable without losing CI coverage.
		t.Skip("stdin fd inspection relies on /proc/self/fd/0")
	}

	script := "#!/bin/sh\n" +
		"kind=$(stat -c '%F' -L /proc/self/fd/0 2>/dev/null || echo unknown)\n" +
		"case \"$kind\" in\n" +
		"  fifo|*pipe*)\n" +
		"    printf '%s\\n' '{\"type\":\"agent_start\"}'\n" +
		"    printf '%s\\n' '{\"type\":\"turn_end\",\"message\":{\"role\":\"assistant\",\"model\":\"test\",\"usage\":{\"input\":1,\"output\":1,\"cacheRead\":0,\"cacheWrite\":0,\"totalTokens\":2}}}'\n" +
		"    exit 0\n" +
		"    ;;\n" +
		"esac\n" +
		"printf 'stdin was %s; expected fifo\\n' \"$kind\" >&2\n" +
		"exit 1\n"
	result := executeBackendScript(t, "pi", "pi", script, ExecOptions{Timeout: 5 * time.Second})
	if result.Status != "completed" {
		t.Fatalf("expected status=completed (stdin attached as fifo), got %q (error=%q)", result.Status, result.Error)
	}
}

func TestDrainPiTextBufferRemovesSplitProtocolTokens(t *testing.T) {
	for _, tc := range []struct {
		name   string
		chunks []string
	}{
		{name: "tool call", chunks: []string{"before ca", `ll:bash{command:<|"|>ls -R repo/path`, `/roles/example<|"|>}`, " after"}},
		{name: "control token", chunks: []string{"before <|tu", "rn>model after"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf strings.Builder
			var got strings.Builder
			for _, chunk := range tc.chunks {
				got.WriteString(drainPiTextBuffer(&buf, chunk))
			}
			got.WriteString(flushPiTextBuffer(&buf))
			if got.String() != "before  after" {
				t.Fatalf("unexpected streamed text: %q", got.String())
			}
		})
	}
}

func TestFlushPiTextBufferKeepsUnmatchedToolPrefixes(t *testing.T) {
	tests := []string{
		"plain response: see below",
		"plain call: see below",
		`plain call:bash{command:<|"|>unterminated`,
	}
	for _, want := range tests {
		var buf strings.Builder
		got := drainPiTextBuffer(&buf, want)
		got += flushPiTextBuffer(&buf)
		if got != want {
			t.Fatalf("unexpected flushed text: %q, want %q", got, want)
		}
	}
}
