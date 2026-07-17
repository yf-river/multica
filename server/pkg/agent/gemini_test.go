package agent

import (
	"log/slog"
	"slices"
	"strings"
	"testing"
)

func TestBuildGeminiArgs(t *testing.T) {
	t.Parallel()

	base := []string{"-p", "hi", "--yolo", "-o", "stream-json"}
	tests := []struct {
		name string
		opts ExecOptions
		want []string
	}{
		{name: "managed baseline", want: base},
		{name: "model", opts: ExecOptions{Model: "gemini-2.5-pro"}, want: append(slices.Clone(base), "-m", "gemini-2.5-pro")},
		{name: "resume", opts: ExecOptions{ResumeSessionID: "3"}, want: append(slices.Clone(base), "-r", "3")},
		{name: "custom", opts: ExecOptions{CustomArgs: []string{"--sandbox"}}, want: append(slices.Clone(base), "--sandbox")},
		{name: "managed output wins", opts: ExecOptions{CustomArgs: []string{"-o", "text", "--sandbox"}}, want: append(slices.Clone(base), "--sandbox")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := buildGeminiArgs("hi", tt.opts, slog.Default()); !slices.Equal(got, tt.want) {
				t.Fatalf("args = %v, want %v", got, tt.want)
			}
		})
	}
}

// envLookup returns the value of key in an env slice, or ("", false) if absent.
// When the key appears multiple times the last occurrence wins, mirroring how
// libc's getenv resolves duplicates on the daemon's supported platforms — the
// caller-supplied override therefore takes precedence over our default.
func envLookup(env []string, key string) (string, bool) {
	prefix := key + "="
	var value string
	var found bool
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			value = strings.TrimPrefix(entry, prefix)
			found = true
		}
	}
	return value, found
}

func TestBuildGeminiEnv(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		extra map[string]string
		want  map[string]string
	}{
		{name: "default trust", want: map[string]string{"GEMINI_CLI_TRUST_WORKSPACE": "true"}},
		{name: "explicit trust", extra: map[string]string{"GEMINI_CLI_TRUST_WORKSPACE": "false"}, want: map[string]string{"GEMINI_CLI_TRUST_WORKSPACE": "false"}},
		{name: "other extras", extra: map[string]string{"GEMINI_API_KEY": "secret"}, want: map[string]string{"GEMINI_CLI_TRUST_WORKSPACE": "true", "GEMINI_API_KEY": "secret"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			env := buildGeminiEnv(tt.extra)
			for key, want := range tt.want {
				if got, ok := envLookup(env, key); !ok || got != want {
					t.Fatalf("%s = %q, %v; want %q", key, got, ok, want)
				}
			}
		})
	}
}
