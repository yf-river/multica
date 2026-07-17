package agent

import (
	"context"
	"log/slog"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

func resetThinkingCacheForTests() {
	thinkingCacheMu.Lock()
	thinkingCache = map[thinkingCacheKey]thinkingCacheEntry{}
	thinkingCacheMu.Unlock()
}

// ── Claude help parsing ──────────────────────────────────────────────

func TestParseClaudeEffortHelp_NewFormat(t *testing.T) {
	t.Parallel()
	// claude 2.1.121 — the newer help adds xhigh.
	help := `Usage: claude [options]

Options:
  --effort <level>    Effort level for the current session (low, medium, high, xhigh, max)
`
	got := parseClaudeEffortHelp(help)
	want := []string{"low", "medium", "high", "xhigh", "max"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseClaudeEffortHelp: got %v, want %v", got, want)
	}
}

func TestParseClaudeEffortHelp_Missing(t *testing.T) {
	t.Parallel()
	help := `Usage: claude [options]

Options:
  --model <model>     Model to use
  --verbose
`
	got := parseClaudeEffortHelp(help)
	if got != nil {
		t.Fatalf("parseClaudeEffortHelp: expected nil, got %v", got)
	}
}

func TestProjectClaudeLevels_PerModelSubset(t *testing.T) {
	t.Parallel()
	superset := []string{"low", "medium", "high", "xhigh", "max"}
	// Sonnet should drop xhigh per claudeModelEffortAllow.
	got := projectClaudeLevels(superset, claudeModelEffortAllow["claude-sonnet-4-6"])
	values := make([]string, 0, len(got))
	for _, lvl := range got {
		values = append(values, lvl.Value)
	}
	want := []string{"low", "medium", "high", "max"}
	if !reflect.DeepEqual(values, want) {
		t.Fatalf("projectClaudeLevels: got %v, want %v", values, want)
	}
	// Opus keeps xhigh.
	got = projectClaudeLevels(superset, claudeModelEffortAllow["claude-opus-4-7"])
	values = values[:0]
	for _, lvl := range got {
		values = append(values, lvl.Value)
	}
	if !reflect.DeepEqual(values, superset) {
		t.Fatalf("projectClaudeLevels for Opus: got %v, want %v", values, superset)
	}
}

// ── Codex discovery argv ────────────────────────────────────────────

func TestCodexDebugModelsArgs_Pinned(t *testing.T) {
	t.Parallel()
	want := []string{"debug", "models", "--bundled"}
	if !reflect.DeepEqual(codexDebugModelsArgs, want) {
		t.Fatalf("codexDebugModelsArgs drifted: got %v, want %v", codexDebugModelsArgs, want)
	}
	for _, arg := range codexDebugModelsArgs {
		if arg == "--output" || arg == "-o" {
			t.Errorf("--output / -o leaked back into argv (codex CLI does not accept it): %v", codexDebugModelsArgs)
		}
	}
}

func TestRunCodexDebugModels_ArgvSeenByBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake binary requires a POSIX shell")
	}
	t.Parallel()

	dir := t.TempDir()
	argvFile := filepath.Join(dir, "argv.txt")
	fake := filepath.Join(dir, "codex")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$@\" > '" + argvFile + "'\n" +
		"echo '{\"models\":[]}'\n"
	// Use the ForkLock-protected helper instead of os.WriteFile: under
	// t.Parallel() with the rest of this package, a sibling test's
	// concurrent fork can inherit our still-open write fd, causing
	// Linux ETXTBSY when we exec the file (Go #22315).
	writeTestExecutable(t, fake, []byte(script))

	raw, err := runCodexDebugModels(context.Background(), fake)
	if err != nil {
		t.Fatalf("runCodexDebugModels: %v (output=%q)", err, raw)
	}

	got := readTestLines(t, argvFile)
	want := []string{"debug", "models", "--bundled"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("fake codex received argv %v, want %v", got, want)
	}
}

// ── Codex debug models JSON parsing ──────────────────────────────────

func TestParseCodexDebugModels(t *testing.T) {
	t.Parallel()
	raw := []byte(`{
		"models": [
			{
				"slug": "gpt-5.5",
				"default_reasoning_level": "medium",
				"supported_reasoning_levels": [
					{"effort": "low", "description": "Fast"},
					{"effort": "medium", "description": "Balanced"},
					{"effort": "high", "description": "Deeper"},
					{"effort": "xhigh", "description": "Maximum"}
				]
			},
			{
				"slug": "gpt-5",
				"default_reasoning_level": "low",
				"supported_reasoning_levels": [
					{"effort": "minimal", "description": "Quick"},
					{"effort": "low", "description": "Fast"}
				]
			},
			{
				"slug": "no-reasoning",
				"supported_reasoning_levels": []
			}
		]
	}`)
	got := parseCodexDebugModels(raw)

	gpt55, ok := got["gpt-5.5"]
	if !ok || gpt55 == nil {
		t.Fatalf("missing gpt-5.5 entry: %+v", got)
	}
	if gpt55.DefaultLevel != "medium" {
		t.Errorf("gpt-5.5 default: got %q, want medium", gpt55.DefaultLevel)
	}
	if len(gpt55.SupportedLevels) != 4 {
		t.Errorf("gpt-5.5 supported count: got %d, want 4", len(gpt55.SupportedLevels))
	}
	// Labels should come from codexEffortLabel mapping, not from raw effort.
	for _, lvl := range gpt55.SupportedLevels {
		if lvl.Value == "xhigh" && lvl.Label != "Extra high" {
			t.Errorf("xhigh label: got %q, want Extra high", lvl.Label)
		}
	}

	gpt5, ok := got["gpt-5"]
	if !ok || gpt5 == nil {
		t.Fatalf("missing gpt-5 entry: %+v", got)
	}
	if gpt5.DefaultLevel != "low" {
		t.Errorf("gpt-5 default: got %q, want low", gpt5.DefaultLevel)
	}

	// Models with empty supported_reasoning_levels should be omitted to
	// keep the wire payload small and avoid rendering empty pickers.
	if _, ok := got["no-reasoning"]; ok {
		t.Errorf("no-reasoning should be omitted, got %+v", got["no-reasoning"])
	}
}

func TestParseCodexDebugModels_Malformed(t *testing.T) {
	t.Parallel()
	got := parseCodexDebugModels([]byte("not json"))
	if len(got) != 0 {
		t.Fatalf("expected empty map on malformed input, got %+v", got)
	}
}

// ── IsKnownThinkingValue (server-side enum gate) ─────────────────────

func TestIsKnownThinkingValue(t *testing.T) {
	t.Parallel()
	tests := []struct {
		provider string
		value    string
		want     bool
	}{
		{"claude", "", true},
		{"claude", "low", true},
		{"claude", "xhigh", true},
		{"claude", "max", true},
		{"claude", "none", false}, // Codex-only token rejected for Claude
		{"codex", "", true},
		{"codex", "none", true},
		{"codex", "minimal", true},
		{"codex", "xhigh", true},
		{"codex", "max", false}, // Claude-only token rejected for Codex
		{"opencode", "", true},
		{"opencode", "max", true},
		{"opencode", "fast-mode", true},  // custom opencode.json variant names are valid
		{"opencode", ".hidden", false},   // reject suspicious / malformed names server-side
		{"opencode", "bad value", false}, // spaces are not valid variant names
		{"hermes", "", true},
		{"hermes", "low", false}, // hermes has no thinking concept
	}
	for _, tc := range tests {
		if got := IsKnownThinkingValue(tc.provider, tc.value); got != tc.want {
			t.Errorf("IsKnownThinkingValue(%q, %q) = %v, want %v",
				tc.provider, tc.value, got, tc.want)
		}
	}
}

// ── ValidateThinkingLevel default-model handling ─────────────────────
//
// Elon's PR1 review called out that an empty model on a default-model
// task must not be misjudged as "unknown model → reject". The fix is to
// resolve empty model to the catalog's default entry inside the
// validator. Both the daemon's per-model guard and the server's API
// layer call this; if it gets default-model wrong, any agent without an
// explicit model set would have its thinking_level dropped silently.

func TestValidateThinkingLevel_EmptyModelResolvesToDefault(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake binary requires a POSIX shell")
	}
	t.Parallel()

	// We need a `claude` whose --help advertises the full superset
	// (low/medium/high/xhigh/max) so per-model projection actually has
	// something to filter. A non-existent path falls back to a conservative
	// [low,medium,high] which would hide the per-model behaviour we're
	// trying to verify.
	fakeClaude := writeFakeClaudeHelpBinary(t)
	resetThinkingCacheForTests()
	defer resetThinkingCacheForTests()

	ctx := context.Background()

	t.Run("valid level on default model passes", func(t *testing.T) {
		// Claude's catalog flags Sonnet 4.6 as Default. Sonnet supports
		// low/medium/high/max (no xhigh) per claudeModelEffortAllow, so
		// "high" must round-trip when model is left empty.
		ok, err := ValidateThinkingLevel(ctx, "claude", fakeClaude, "", "high")
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if !ok {
			t.Errorf("default-model high should be valid for claude; got false")
		}
	})

	t.Run("invalid level on default model fails", func(t *testing.T) {
		// "xhigh" is opus-only; resolving "" to default (sonnet 4.6)
		// should reject it, not silently accept.
		ok, err := ValidateThinkingLevel(ctx, "claude", fakeClaude, "", "xhigh")
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if ok {
			t.Errorf("xhigh should be invalid on sonnet (the default model); got true")
		}
	})

	t.Run("empty value always valid", func(t *testing.T) {
		// Empty value means "use runtime default" — should pass
		// regardless of model resolution.
		ok, err := ValidateThinkingLevel(ctx, "claude", fakeClaude, "", "")
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if !ok {
			t.Errorf("empty value must always be valid")
		}
	})
}

func TestValidateThinkingLevel_ExplicitModel(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake binary requires a POSIX shell")
	}
	t.Parallel()
	fakeClaude := writeFakeClaudeHelpBinary(t)
	resetThinkingCacheForTests()
	defer resetThinkingCacheForTests()

	ctx := context.Background()

	// xhigh IS valid on Opus 4.7.
	ok, err := ValidateThinkingLevel(ctx, "claude", fakeClaude, "claude-opus-4-7", "xhigh")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !ok {
		t.Errorf("xhigh should be valid on opus-4-7; got false")
	}

	// xhigh is NOT valid on Sonnet — should fail.
	ok, err = ValidateThinkingLevel(ctx, "claude", fakeClaude, "claude-sonnet-4-6", "xhigh")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if ok {
		t.Errorf("xhigh must not be valid on sonnet-4-6; got true")
	}

	// An unknown model with a valid token still fails closed (no guess).
	ok, err = ValidateThinkingLevel(ctx, "claude", fakeClaude, "claude-nonexistent", "high")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if ok {
		t.Errorf("unknown model must fail closed; got true")
	}
}

func TestValidateThinkingLevel_OpenCodeEmptyModelUsesAdvertisedVariants(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake binary requires a POSIX shell")
	}

	modelCacheMu.Lock()
	delete(modelCache, "opencode")
	modelCacheMu.Unlock()
	defer func() {
		modelCacheMu.Lock()
		delete(modelCache, "opencode")
		modelCacheMu.Unlock()
	}()

	dir := t.TempDir()
	fake := filepath.Join(dir, "opencode")
	script := `#!/bin/sh
if [ "$1" = "models" ]; then
  cat <<'EOF'
opencode/deepseek-v4
{
  "id": "deepseek-v4",
  "reasoning": true,
  "variants": {
    "high": {},
    "max": {}
  }
}
EOF
  exit 0
fi
echo "opencode 9.9.9"
`
	writeTestExecutable(t, fake, []byte(script))

	ctx := context.Background()
	ok, err := ValidateThinkingLevel(ctx, "opencode", fake, "", "max")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !ok {
		t.Fatalf("expected empty-model opencode max to pass when any advertised model supports it")
	}

	ok, err = ValidateThinkingLevel(ctx, "opencode", fake, "", "xhigh")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if ok {
		t.Fatalf("xhigh should fail when no advertised OpenCode model exposes it")
	}
}

// writeFakeClaudeHelpBinary writes a small shell script that mimics
// `claude --help`, emitting the full effort superset line so per-model
// projection has something to filter. Returns the path to the executable.
func writeFakeClaudeHelpBinary(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "claude")
	script := "#!/bin/sh\n" +
		"cat <<'EOF'\n" +
		"Usage: claude [options]\n" +
		"\n" +
		"Options:\n" +
		"  --model <model>     Model to use\n" +
		"  --effort <level>    Effort level for the current session (low, medium, high, xhigh, max)\n" +
		"EOF\n"
	// Same ForkLock rationale as TestRunCodexDebugModels_ArgvSeenByBinary —
	// the parser tests that consume this helper exec the script in parallel,
	// so a sibling fork can otherwise inherit our write fd and trip ETXTBSY.
	writeTestExecutable(t, path, []byte(script))
	return path
}

// ── Cache key invalidation ───────────────────────────────────────────

func TestThinkingCacheKeyDistinct(t *testing.T) {
	t.Parallel()
	resetThinkingCacheForTests()
	defer resetThinkingCacheForTests()

	a := thinkingCacheKey{provider: "claude", executablePath: "/bin/claude", cliVersion: "2.1.121"}
	b := thinkingCacheKey{provider: "claude", executablePath: "/bin/claude", cliVersion: "2.1.122"}
	c := thinkingCacheKey{provider: "claude", executablePath: "/opt/claude", cliVersion: "2.1.121"}

	thinkingCachePut(a, map[string]*ModelThinking{"x": {DefaultLevel: "a"}})
	thinkingCachePut(b, map[string]*ModelThinking{"x": {DefaultLevel: "b"}})
	thinkingCachePut(c, map[string]*ModelThinking{"x": {DefaultLevel: "c"}})

	if got, _ := thinkingCacheGet(a); got["x"].DefaultLevel != "a" {
		t.Errorf("cache key A: got %q, want a", got["x"].DefaultLevel)
	}
	if got, _ := thinkingCacheGet(b); got["x"].DefaultLevel != "b" {
		t.Errorf("cache key B: got %q, want b", got["x"].DefaultLevel)
	}
	if got, _ := thinkingCacheGet(c); got["x"].DefaultLevel != "c" {
		t.Errorf("cache key C: got %q, want c", got["x"].DefaultLevel)
	}
}

// ── Codex reasoning payloads ────────────────────────────────────────
type codexReasoningCase struct {
	name  string
	level string
}

var codexReasoningCases = []codexReasoningCase{
	{"empty-level-is-noop", ""},
	{"low", "low"},
	{"medium", "medium"},
	{"high", "high"},
	{"xhigh", "xhigh"},
	{"none-codex-only", "none"},
}

func TestApplyCodexReasoningEffort_ThreePoints(t *testing.T) {
	t.Parallel()
	for _, tc := range codexReasoningCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			startParams := map[string]any{
				"model": "gpt-5.5",
				"cwd":   "/work",
			}
			wantStart := map[string]any{"model": "gpt-5.5", "cwd": "/work"}
			if tc.level != "" {
				wantStart["config"] = map[string]any{"model_reasoning_effort": tc.level}
			}
			applyCodexReasoningEffort(startParams, tc.level)
			if !reflect.DeepEqual(startParams, wantStart) {
				t.Fatalf("thread/start = %#v, want %#v", startParams, wantStart)
			}

			resumeParams := map[string]any{
				"threadId": "thr_prior",
				"cwd":      "/work",
				"model":    "gpt-5.5",
			}
			wantResume := map[string]any{"threadId": "thr_prior", "cwd": "/work", "model": "gpt-5.5"}
			if tc.level != "" {
				wantResume["config"] = map[string]any{"model_reasoning_effort": tc.level}
			}
			applyCodexReasoningEffort(resumeParams, tc.level)
			if !reflect.DeepEqual(resumeParams, wantResume) {
				t.Fatalf("thread/resume = %#v, want %#v", resumeParams, wantResume)
			}

			turnParams := map[string]any{
				"threadId": "thr_x",
				"input":    []map[string]any{{"type": "text", "text": "hi"}},
			}
			wantTurn := map[string]any{
				"threadId": "thr_x",
				"input":    []map[string]any{{"type": "text", "text": "hi"}},
			}
			if tc.level != "" {
				wantTurn["effort"] = tc.level
			}
			applyCodexReasoningEffort(turnParams, tc.level)
			if !reflect.DeepEqual(turnParams, wantTurn) {
				t.Fatalf("turn/start = %#v, want %#v", turnParams, wantTurn)
			}
		})
	}
}

func TestApplyCodexReasoningEffort_NilParamsSafe(t *testing.T) {
	t.Parallel()
	applyCodexReasoningEffort(nil, "high")
}

func TestApplyCodexReasoningEffort_PreservesPreExistingConfig(t *testing.T) {
	t.Parallel()
	startParams := map[string]any{
		"model": "gpt-5.5",
		"config": map[string]any{
			"some_future_key": "preserve_me",
		},
	}
	applyCodexReasoningEffort(startParams, "high")
	cfg, _ := startParams["config"].(map[string]any)
	if cfg["some_future_key"] != "preserve_me" {
		t.Errorf("pre-existing config key was clobbered: %+v", cfg)
	}
	if cfg["model_reasoning_effort"] != "high" {
		t.Errorf("reasoning effort not injected: %+v", cfg)
	}
}

// ── End-to-end: build*Args + thinking_level wiring ───────────────────

func TestBuildClaudeArgs_InjectsEffort(t *testing.T) {
	t.Parallel()
	args := buildClaudeArgs(ExecOptions{Model: "claude-opus-4-7", ThinkingLevel: "xhigh"}, slog.Default())
	if argValue(args, "--effort") != "xhigh" {
		t.Errorf("expected --effort xhigh in args: %v", args)
	}
	// Must appear after --model (cosmetic but enforced for log readability).
	modelIdx := argIndex(args, "--model")
	effortIdx := argIndex(args, "--effort")
	if modelIdx < 0 || effortIdx < 0 || modelIdx > effortIdx {
		t.Errorf("expected --model before --effort: %v", args)
	}
}

func TestBuildClaudeArgs_OmitsEffortWhenEmpty(t *testing.T) {
	t.Parallel()
	args := buildClaudeArgs(ExecOptions{Model: "claude-sonnet-4-6"}, slog.Default())
	if argIndex(args, "--effort") >= 0 {
		t.Errorf("expected no --effort when level empty: %v", args)
	}
}

func TestBuildClaudeArgs_BlocksUserEffortOverride(t *testing.T) {
	t.Parallel()
	args := buildClaudeArgs(ExecOptions{
		Model:         "claude-opus-4-7",
		ThinkingLevel: "high",
		CustomArgs:    []string{"--effort", "max", "--keep-me"},
	}, slog.Default())
	// Daemon-injected --effort survives.
	if argValue(args, "--effort") != "high" {
		t.Errorf("daemon-injected --effort high should remain: %v", args)
	}
	// User attempt to override is filtered out: no second --effort,
	// no `max` token.
	if count := argCount(args, "--effort"); count != 1 {
		t.Errorf("expected exactly one --effort, got %d: %v", count, args)
	}
	if argIndex(args, "max") >= 0 {
		t.Errorf("filtered user --effort value still appears: %v", args)
	}
	// Other custom args pass through.
	if argIndex(args, "--keep-me") < 0 {
		t.Errorf("non-blocked custom arg was dropped: %v", args)
	}
}
