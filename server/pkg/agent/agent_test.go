package agent

import (
	"context"
	"reflect"
	"testing"
	"time"
)

func TestSupportedTypesMatchFactoryAndSchema(t *testing.T) {
	t.Parallel()

	want := map[string]reflect.Type{
		"claude":      reflect.TypeOf((*claudeBackend)(nil)),
		"codebuddy":   reflect.TypeOf((*codebuddyBackend)(nil)),
		"codex":       reflect.TypeOf((*CodexBrokerBackend)(nil)),
		"copilot":     reflect.TypeOf((*copilotBackend)(nil)),
		"opencode":    reflect.TypeOf((*opencodeBackend)(nil)),
		"hermes":      reflect.TypeOf((*hermesBackend)(nil)),
		"gemini":      reflect.TypeOf((*geminiBackend)(nil)),
		"pi":          reflect.TypeOf((*piBackend)(nil)),
		"cursor":      reflect.TypeOf((*cursorBackend)(nil)),
		"kimi":        reflect.TypeOf((*kimiBackend)(nil)),
		"kiro":        reflect.TypeOf((*kiroBackend)(nil)),
		"antigravity": reflect.TypeOf((*antigravityBackend)(nil)),
	}

	if len(SupportedTypes) != len(want) {
		t.Fatalf("SupportedTypes has %d entries, current schema/factory contract has %d", len(SupportedTypes), len(want))
	}
	for _, agentType := range SupportedTypes {
		wantType, ok := want[agentType]
		if !ok {
			t.Errorf("SupportedTypes contains %q outside the current schema contract", agentType)
			continue
		}
		if !IsSupportedType(agentType) {
			t.Errorf("IsSupportedType(%q) = false", agentType)
		}
		backend, err := New(agentType, Config{})
		if err != nil {
			t.Errorf("New(%q): %v", agentType, err)
			continue
		}
		if gotType := reflect.TypeOf(backend); gotType != wantType {
			t.Errorf("New(%q) returned %v, want %v", agentType, gotType, wantType)
		}
	}

	const unknown = "definitely-not-a-real-backend"
	if IsSupportedType(unknown) {
		t.Errorf("IsSupportedType(%q) = true", unknown)
	}
	if _, err := New(unknown, Config{}); err == nil {
		t.Errorf("New(%q) succeeded", unknown)
	}
}

func TestNewDefaultsLogger(t *testing.T) {
	t.Parallel()
	b, _ := New("claude", Config{})
	cb := b.(*claudeBackend)
	if cb.cfg.Logger == nil {
		t.Fatal("expected non-nil logger")
	}
}

func TestDetectVersionFailsForMissingBinary(t *testing.T) {
	t.Parallel()
	_, err := DetectVersion(context.Background(), "/nonexistent/binary")
	if err == nil {
		t.Fatal("expected error for missing binary")
	}
}

func TestLaunchHeaderCoversAllSupportedBackends(t *testing.T) {
	t.Parallel()

	// The factory in New() enumerates every supported agent type; LaunchHeader
	// must stay in sync so the UI preview never shows an empty skeleton for a
	// runtime the daemon actually spawns. If a new backend is added, add an
	// entry to launchHeaders in agent.go and extend this list.
	supported := []string{
		"antigravity", "claude", "codebuddy", "codex", "copilot", "cursor", "gemini",
		"hermes", "kimi", "kiro", "opencode", "pi",
	}
	for _, t_ := range supported {
		if header := LaunchHeader(t_); header == "" {
			t.Errorf("LaunchHeader(%q) returned empty string — add it to launchHeaders", t_)
		}
	}
}

func TestLaunchHeaderReturnsEmptyForUnknownType(t *testing.T) {
	t.Parallel()
	if header := LaunchHeader("made-up-agent"); header != "" {
		t.Errorf("expected empty header for unknown type, got %q", header)
	}
}

func TestRunContextZeroTimeoutHasNoDeadline(t *testing.T) {
	t.Parallel()
	// A zero (or negative) timeout must NOT impose a wall-clock deadline:
	// liveness is delegated to the daemon's inactivity watchdog so an actively
	// streaming long-running session is never killed merely for running long
	// (MUL-3064).
	for _, d := range []time.Duration{0, -time.Second} {
		ctx, cancel := runContext(context.Background(), d)
		if _, ok := ctx.Deadline(); ok {
			cancel()
			t.Fatalf("runContext(%s) imposed a deadline; want none", d)
		}
		cancel()
		if ctx.Err() == nil {
			t.Fatalf("runContext(%s): context should be cancelled after cancel()", d)
		}
	}
}

func TestRunContextPositiveTimeoutHasDeadline(t *testing.T) {
	t.Parallel()
	// A positive timeout keeps the hard wall-clock deadline (the opt-in
	// absolute cap operators can still set via MULTICA_AGENT_TIMEOUT).
	ctx, cancel := runContext(context.Background(), time.Hour)
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("runContext(1h) should impose a deadline")
	}
	if remaining := time.Until(deadline); remaining <= 0 || remaining > time.Hour+time.Minute {
		t.Fatalf("unexpected deadline remaining: %s", remaining)
	}
}
