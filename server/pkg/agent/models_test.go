package agent

import (
	"context"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func modelsByID(models []Model) map[string]Model {
	indexed := make(map[string]Model, len(models))
	for _, model := range models {
		indexed[model.ID] = model
	}
	return indexed
}

func TestListModelsStaticProviders(t *testing.T) {
	ctx := context.Background()
	for _, provider := range []string{"claude", "codex", "gemini", "cursor"} {
		got, err := ListModels(ctx, provider, "")
		if err != nil {
			t.Fatalf("ListModels(%q) error: %v", provider, err)
		}
		if len(got) == 0 {
			t.Errorf("ListModels(%q) returned no models", provider)
		}
		for i, m := range got {
			if m.ID == "" {
				t.Errorf("ListModels(%q)[%d] has empty ID", provider, i)
			}
			if m.Label == "" {
				t.Errorf("ListModels(%q)[%d] has empty Label", provider, i)
			}
		}
	}
}

func TestListModelsCopilotFallsBackToStatic(t *testing.T) {
	// Copilot uses dynamic ACP discovery, but with no `copilot`
	// binary on PATH (the discovery LookPath fails) it must fall
	// back to copilotStaticModels() so the UI dropdown stays
	// populated. This is the "binary missing on the daemon host"
	// path we care about for self-hosted runtimes.
	ctx := context.Background()
	modelCacheMu.Lock()
	delete(modelCache, "copilot")
	modelCacheMu.Unlock()

	got, err := ListModels(ctx, "copilot", "/nonexistent/copilot-cli")
	if err != nil {
		t.Fatalf("ListModels(copilot) error: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("expected static fallback models, got empty list")
	}
	ids := modelsByID(got)
	_, hasGPT := ids["gpt-5.4"]
	_, hasClaude := ids["claude-sonnet-4.6"]
	if !hasGPT || !hasClaude {
		t.Errorf("static fallback missing expected models: %+v", got)
	}
}

func TestClaudeStaticModelsExposesFable5(t *testing.T) {
	models := claudeStaticModels()
	ids := modelsByID(models)
	defaults := 0
	for _, m := range models {
		if m.Default {
			defaults++
		}
	}

	fable, ok := ids["claude-fable-5"]
	if !ok {
		t.Fatalf("missing Claude Fable 5 in: %+v", models)
	}
	if fable.Label != "Claude Fable 5" || fable.Provider != "anthropic" || fable.Default {
		t.Errorf("unexpected Fable entry: %+v", fable)
	}
	if defaults != 1 || !ids["claude-sonnet-4-6"].Default {
		t.Errorf("expected Sonnet 4.6 to remain the sole default, got defaults=%d models=%+v", defaults, models)
	}
}

func TestGeminiStaticModelsExposesAliasesAndGemini3(t *testing.T) {
	models := geminiStaticModels()
	ids := modelsByID(models)
	for _, want := range []string{
		"auto", "auto-gemini-2.5",
		"pro", "flash", "flash-lite",
		"gemini-3-pro-preview", "gemini-3-flash-preview",
		"gemini-2.5-pro", "gemini-2.5-flash", "gemini-2.5-flash-lite",
	} {
		if _, ok := ids[want]; !ok {
			t.Errorf("missing expected Gemini model %q in: %+v", want, models)
		}
	}
	auto, ok := ids["auto"]
	if !ok || !auto.Default {
		t.Errorf("expected `auto` to be the default Gemini entry, got %+v", auto)
	}
	for _, m := range models {
		if m.Provider != "google" {
			t.Errorf("all Gemini entries must carry Provider=google, got %+v", m)
		}
	}
}

func TestCodexStaticModelsExposesGPT55(t *testing.T) {
	models := codexStaticModels()
	ids := modelsByID(models)
	for _, want := range []string{
		"gpt-5.5", "gpt-5.5-mini",
		"gpt-5.4", "gpt-5.4-mini",
		"gpt-5.3-codex-spark", "gpt-5.3-codex", "gpt-5",
		"o3", "o3-mini",
	} {
		if _, ok := ids[want]; !ok {
			t.Errorf("missing expected Codex model %q in: %+v", want, models)
		}
	}
	latest, ok := ids["gpt-5.5"]
	if !ok || !latest.Default {
		t.Errorf("expected `gpt-5.5` to be the default Codex entry, got %+v", latest)
	}
	defaults := 0
	for _, m := range models {
		if m.Default {
			defaults++
		}
		if m.Provider != "openai" {
			t.Errorf("all Codex entries must carry Provider=openai, got %+v", m)
		}
	}
	if defaults != 1 {
		t.Errorf("expected exactly one default Codex entry, got %d", defaults)
	}
}

func TestModelKnownIncompatibleWithProvider(t *testing.T) {
	cases := []struct {
		name     string
		provider string
		model    string
		want     bool
	}{
		{
			name:     "claude model is incompatible with codex",
			provider: "codex",
			model:    "claude-sonnet-4-6",
			want:     true,
		},
		{
			name:     "codex model is compatible with codex",
			provider: "codex",
			model:    "gpt-5.5",
			want:     false,
		},
		{
			name:     "codex model is incompatible with claude",
			provider: "claude",
			model:    "o3",
			want:     true,
		},
		{
			name:     "exact claude model is compatible with claude",
			provider: "claude",
			model:    "claude-opus-4-7",
			want:     false,
		},
		{
			name:     "provider-prefixed openai model is incompatible with codex",
			provider: "codex",
			model:    "openai/gpt-4o",
			want:     true,
		},
		{
			name:     "provider-prefixed anthropic model is incompatible with claude",
			provider: "claude",
			model:    "anthropic/claude-opus-4.7",
			want:     true,
		},
		{
			name:     "known openai-looking model outside codex catalog is incompatible",
			provider: "codex",
			model:    "gpt-99",
			want:     true,
		},
		{
			name:     "unknown custom model is not classified",
			provider: "codex",
			model:    "private-lab-model",
			want:     false,
		},
		{
			name:     "unknown target provider does not clear",
			provider: "opencode",
			model:    "claude-sonnet-4-6",
			want:     false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ModelKnownIncompatibleWithProvider(tc.provider, tc.model); got != tc.want {
				t.Fatalf("ModelKnownIncompatibleWithProvider(%q, %q) = %v, want %v", tc.provider, tc.model, got, tc.want)
			}
		})
	}
}

func TestInferCopilotProvider(t *testing.T) {
	cases := map[string]string{
		"gpt-5.5":             "openai",
		"gpt-5.4-mini":        "openai",
		"gpt-5.3-codex-spark": "openai",
		"gpt-5.3-codex":       "openai",
		"gpt-4.1":             "openai",
		"o1":                  "openai",
		"o3":                  "openai",
		"o3-mini":             "openai",
		"o4-mini":             "openai",
		"o5":                  "openai", // future-proof: any o<digit>+
		"o6-mini-high":        "openai",
		"claude-opus-4.7":     "anthropic",
		"claude-sonnet-4.6":   "anthropic",
		"claude-haiku-4.5":    "anthropic",
		"gemini-3-pro":        "google",
		"grok-code-fast-1":    "xai",
		"auto":                "",
		"raptor-mini":         "",
		// negative cases: must not be misidentified as OpenAI
		// reasoning series even though they start with `o`.
		"opus-fake": "",
		"omni":      "",
		"o":         "",
	}
	for id, want := range cases {
		if got := inferCopilotProvider(id); got != want {
			t.Errorf("inferCopilotProvider(%q) = %q, want %q", id, got, want)
		}
	}
}

func TestCopilotStaticModelsExposesFullCatalog(t *testing.T) {
	models := copilotStaticModels()
	ids := modelsByID(models)
	for _, want := range []string{
		"gpt-5.5", "gpt-5.4", "gpt-5.4-mini",
		"gpt-5.3-codex-spark", "gpt-5.3-codex", "gpt-5.2-codex", "gpt-5.2",
		"gpt-5-mini", "gpt-4.1",
		"claude-opus-4.7", "claude-sonnet-4.6",
		"claude-sonnet-4.5", "claude-haiku-4.5",
	} {
		if _, ok := ids[want]; !ok {
			t.Errorf("missing expected Copilot model %q in: %+v", want, models)
		}
	}
	for _, m := range models {
		switch m.Provider {
		case "openai", "anthropic":
		default:
			t.Errorf("Copilot entry %q has unexpected Provider %q", m.ID, m.Provider)
		}
		if m.Default {
			t.Errorf("Copilot entries should not set Default; account routing decides. got %+v", m)
		}
	}
}

func TestListModelsDynamicProviderWithoutBinary(t *testing.T) {
	for _, provider := range []string{"hermes", "kiro"} {
		t.Run(provider, func(t *testing.T) {
			modelCacheMu.Lock()
			delete(modelCache, provider)
			modelCacheMu.Unlock()

			got, err := ListModels(context.Background(), provider, "/nonexistent/"+provider)
			if err != nil {
				t.Fatalf("ListModels(%s) error: %v", provider, err)
			}
			if got == nil {
				t.Error("expected non-nil slice when binary is missing")
			}
		})
	}
}

func TestListModelsUnknownProvider(t *testing.T) {
	ctx := context.Background()
	_, err := ListModels(ctx, "nonexistent", "")
	if err == nil {
		t.Fatal("ListModels(unknown) expected error")
	}
}

func TestStaticCatalogsHaveAtMostOneDefault(t *testing.T) {
	// Each catalog should tag at most one entry as the display
	// default so the UI badge is unambiguous. More than one
	// usually means a copy/paste slip when adding new models.
	catalogs := map[string][]Model{
		"claude":  claudeStaticModels(),
		"codex":   codexStaticModels(),
		"gemini":  geminiStaticModels(),
		"cursor":  cursorStaticModels(),
		"copilot": copilotStaticModels(),
	}
	for provider, models := range catalogs {
		count := 0
		for _, m := range models {
			if m.Default {
				count++
			}
		}
		if count > 1 {
			t.Errorf("%s: %d models marked Default, want 0 or 1", provider, count)
		}
	}
}

func TestParseOpenCodeModels(t *testing.T) {
	input := `openai/gpt-4o
anthropic/claude-sonnet-4-6
openai/gpt-4o
nonprefixed-line
`
	models := parseOpenCodeModels(input)
	if len(models) != 2 {
		t.Fatalf("expected 2 models (duplicate deduped, non-slash skipped), got %d: %+v", len(models), models)
	}
	if models[0].ID != "openai/gpt-4o" || models[0].Provider != "openai" {
		t.Errorf("unexpected first model: %+v", models[0])
	}
	if models[1].ID != "anthropic/claude-sonnet-4-6" || models[1].Provider != "anthropic" {
		t.Errorf("unexpected second model: %+v", models[1])
	}
}

func TestParseOpenCodeModelsVerboseVariants(t *testing.T) {
	input := `openai/gpt-5
{
  "id": "gpt-5",
  "name": "GPT-5",
  "reasoning": true,
  "variants": {
    "high": { "reasoningEffort": "high" },
    "low": { "reasoningEffort": "low" },
    "xhigh": { "reasoningEffort": "xhigh" },
    "fast-mode": { "reasoningEffort": "low" },
    "disabled": { "disabled": true }
  }
}
anthropic/claude-sonnet-4-6
{
  "id": "claude-sonnet-4-6",
  "reasoning": true,
  "variants": {
    "max": { "thinking": { "type": "enabled", "budgetTokens": 32000 } },
    "high": { "thinking": { "type": "enabled", "budgetTokens": 16000 } }
  }
}
`
	models := parseOpenCodeModels(input)
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d: %+v", len(models), models)
	}
	if models[0].Thinking == nil {
		t.Fatalf("expected first model to expose thinking variants")
	}
	got := make([]string, 0, len(models[0].Thinking.SupportedLevels))
	for _, lvl := range models[0].Thinking.SupportedLevels {
		got = append(got, lvl.Value)
		if lvl.Value == "xhigh" && lvl.Label != "Extra high" {
			t.Errorf("xhigh label: got %q, want Extra high", lvl.Label)
		}
		if lvl.Value == "fast-mode" && lvl.Label != "Fast Mode" {
			t.Errorf("custom variant label: got %q, want Fast Mode", lvl.Label)
		}
	}
	want := []string{"low", "high", "xhigh", "fast-mode"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("variant order/values: got %v, want %v", got, want)
	}
	if models[1].Thinking == nil || len(models[1].Thinking.SupportedLevels) != 2 {
		t.Fatalf("expected second model variants, got %+v", models[1].Thinking)
	}
}

func TestParseOpenCodeModelsMalformedVerboseBlockKeepsFollowingModels(t *testing.T) {
	input := `openai/gpt-5
{
  "id": "gpt-5",
  "reasoning": true,
  "variants": {
    "high": {}
  }
anthropic/claude-sonnet-4-6
{
  "id": "claude-sonnet-4-6",
  "reasoning": true,
  "variants": {
    "high": {},
    "max": {}
  }
}
`
	models := parseOpenCodeModels(input)
	if len(models) != 2 {
		t.Fatalf("expected both model rows to survive malformed JSON, got %d: %+v", len(models), models)
	}
	if models[0].ID != "openai/gpt-5" {
		t.Fatalf("unexpected first model: %+v", models[0])
	}
	if models[0].Thinking != nil {
		t.Fatalf("malformed first JSON block should not annotate thinking: %+v", models[0].Thinking)
	}
	if models[1].ID != "anthropic/claude-sonnet-4-6" {
		t.Fatalf("unexpected second model: %+v", models[1])
	}
	if models[1].Thinking == nil || len(models[1].Thinking.SupportedLevels) != 2 {
		t.Fatalf("valid following JSON block should still annotate thinking: %+v", models[1].Thinking)
	}
}

func TestDiscoverOpenCodeModelsReturnsCommandFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake binary requires a POSIX shell")
	}

	fake := filepath.Join(t.TempDir(), "opencode")
	writeTestExecutable(t, fake, []byte("#!/bin/sh\nexit 23\n"))

	models, err := discoverOpenCodeModels(context.Background(), fake)
	if err == nil {
		t.Fatalf("models = %+v, want command failure", models)
	}
	if !strings.Contains(err.Error(), "discover OpenCode models") {
		t.Fatalf("error = %q, want discovery context", err)
	}
}

func TestCachedDiscoveryDoesNotCacheEmpty(t *testing.T) {
	const emptyKey, nonEmptyKey = "test-cache-empty", "test-cache-nonempty"
	// modelCache is a package-level global; clear our keys up front and on
	// cleanup so the test stays hermetic under `go test -count=N` (a leftover
	// non-empty entry from a prior run would otherwise skip the callback).
	resetCache := func() {
		modelCacheMu.Lock()
		delete(modelCache, emptyKey)
		delete(modelCache, nonEmptyKey)
		modelCacheMu.Unlock()
	}
	resetCache()
	t.Cleanup(resetCache)

	emptyCalls := 0
	empty := func() ([]Model, error) {
		emptyCalls++
		return []Model{}, nil
	}
	for i := 0; i < 2; i++ {
		got, err := cachedDiscovery(emptyKey, empty)
		if err != nil {
			t.Fatalf("cachedDiscovery: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("expected empty result, got %+v", got)
		}
	}
	if emptyCalls != 2 {
		t.Fatalf("empty result must not be cached: expected fn called 2x, got %d", emptyCalls)
	}

	nonEmptyCalls := 0
	nonEmpty := func() ([]Model, error) {
		nonEmptyCalls++
		return []Model{{ID: "provider/model"}}, nil
	}
	for i := 0; i < 2; i++ {
		if _, err := cachedDiscovery(nonEmptyKey, nonEmpty); err != nil {
			t.Fatalf("cachedDiscovery: %v", err)
		}
	}
	if nonEmptyCalls != 1 {
		t.Fatalf("non-empty result must be cached: expected fn called 1x, got %d", nonEmptyCalls)
	}
}

func TestParsePiModelsTableFormat(t *testing.T) {
	input := `provider             model                   context  max-out  thinking  images
bailian-coding-plan  glm-4.7                 202.8K   16.4K    no        no
bailian-coding-plan  qwen3.6-plus            1M       65.5K    no        yes
opencode             claude-sonnet-4-6       1M       64K      yes       yes
opencode             claude-sonnet-4-6:exp   1M       64K      yes       yes
opencode             claude-sonnet-4-6       1M       64K      yes       yes
bareword-only-line
`
	models := parsePiModels(input)
	if len(models) != 4 {
		t.Fatalf("expected 4 models (header skipped, duplicate deduped, bareword skipped), got %d: %+v", len(models), models)
	}
	if models[0].ID != "bailian-coding-plan/glm-4.7" || models[0].Provider != "bailian-coding-plan" {
		t.Errorf("unexpected first model: %+v", models[0])
	}
	if models[1].ID != "bailian-coding-plan/qwen3.6-plus" || models[1].Provider != "bailian-coding-plan" {
		t.Errorf("unexpected second model: %+v", models[1])
	}
	if models[2].ID != "opencode/claude-sonnet-4-6" || models[2].Provider != "opencode" {
		t.Errorf("unexpected third model: %+v", models[2])
	}
	// A colon inside the model column is part of the current model ID.
	if models[3].ID != "opencode/claude-sonnet-4-6:exp" || models[3].Provider != "opencode" {
		t.Errorf("expected ':' inside table-format model name to be preserved: %+v", models[3])
	}
}

func TestDiscoverPiModelsNonZeroExit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake pi binary is a /bin/sh script")
	}

	const table = "provider         model        context  max-out  thinking  images\n" +
		"glm-coding-plan  glm-4.7      202.8K   16.4K    no        no"
	// The unmatched-pattern warning is emitted on stderr while the table
	// remains on stdout.
	const prefixed = `Warning: No models match pattern "opencode-go/mimo-v2-omni"`
	fakePath := filepath.Join(t.TempDir(), "pi")
	writeTestExecutable(t, fakePath, []byte("#!/bin/sh\n"+
		"cat <<'EOF'\n"+table+"\nEOF\n"+
		"echo "+strconv.Quote(prefixed)+" >&2\n"+
		"exit 1\n"))

	models, err := discoverPiModels(context.Background(), fakePath)
	if err != nil {
		t.Fatalf("discoverPiModels: %v", err)
	}
	if len(models) != 1 || models[0].ID != "glm-coding-plan/glm-4.7" {
		t.Fatalf("expected exactly [glm-coding-plan/glm-4.7] despite non-zero exit, got %+v", models)
	}
}

func TestParseOpenclawAgentsJSONArray(t *testing.T) {
	input := []byte(`[
	{"id": "deepseek", "name": "DeepSeek", "model": "deepseek-v4"},
	{"id": "claude", "name": "Claude", "model": "claude-sonnet-4-6"},
	{"id": "deepseek", "name": "Duplicate", "model": "ignored"},
	{"name": "Missing ID", "model": "ignored"}
]`)
	models, ok := parseOpenclawAgentsJSON(input)
	if !ok {
		t.Fatal("expected parseOpenclawAgentsJSON to accept an array")
	}
	if len(models) != 2 {
		t.Fatalf("got %d, want 2: %+v", len(models), models)
	}
	if models[0].ID != "deepseek" || models[0].Label != "DeepSeek (deepseek-v4)" {
		t.Errorf("unexpected first entry: %+v", models[0])
	}
}

func TestOpenclawEntriesToModelsUsesIDOverName(t *testing.T) {
	// When both id and name are present, Model.ID should use the id field
	// because openclaw resolves --agent by id. Names with spaces (e.g.
	// "Sub2API OPS") would be mangled by openclaw's normalizeAgentId.
	input := []byte(`[{"id": "sub2api", "name": "Sub2API OPS", "model": "gpt-4o"}]`)
	models, ok := parseOpenclawAgentsJSON(input)
	if !ok {
		t.Fatal("expected parseOpenclawAgentsJSON to accept array")
	}
	if len(models) != 1 {
		t.Fatalf("got %d models, want 1", len(models))
	}
	if models[0].ID != "sub2api" {
		t.Errorf("Model.ID = %q, want %q (should use id, not name)", models[0].ID, "sub2api")
	}
	if models[0].Label != "Sub2API OPS (gpt-4o)" {
		t.Errorf("Model.Label = %q, want %q (should use name for display)", models[0].Label, "Sub2API OPS (gpt-4o)")
	}
}

func TestParseOpenclawAgentsJSONRejectsGarbage(t *testing.T) {
	for _, input := range []string{"not json", `{"agents":[]}`, "null"} {
		if _, ok := parseOpenclawAgentsJSON([]byte(input)); ok {
			t.Errorf("expected ok=false for %q", input)
		}
	}
}

func TestParseCursorModels(t *testing.T) {
	input := `Available models

auto - Auto
composer-2-fast - Composer 2 Fast (current, default)
composer-2 - Composer 2
claude-4.6-sonnet-medium - Sonnet 4.6 1M
claude-opus-4-7-high - Opus 4.7 1M
gemini-3.1-pro - Gemini 3.1 Pro
`
	models := parseCursorModels(input)
	if len(models) != 6 {
		t.Fatalf("expected 6 models, got %d: %+v", len(models), models)
	}
	ids := modelsByID(models)
	for _, want := range []string{"auto", "composer-2-fast", "composer-2", "claude-4.6-sonnet-medium", "claude-opus-4-7-high", "gemini-3.1-pro"} {
		if _, ok := ids[want]; !ok {
			t.Errorf("missing expected model %q in: %+v", want, models)
		}
	}
	if def := ids["composer-2-fast"]; !def.Default {
		t.Errorf("composer-2-fast should be marked default, got %+v", def)
	}
	if def := ids["composer-2-fast"]; def.Label != "Composer 2 Fast" {
		t.Errorf("default label should be stripped of parenthetical, got %q", def.Label)
	}
	// Non-default entry should not carry Default=true.
	if auto := ids["auto"]; auto.Default {
		t.Errorf("non-default entry should not be flagged default: %+v", auto)
	}
}

func TestParseCursorModelsSkipsHeaderAndBlankLines(t *testing.T) {
	input := `Available models

composer-2 - Composer 2
`
	models := parseCursorModels(input)
	if len(models) != 1 || models[0].ID != "composer-2" {
		t.Fatalf("unexpected: %+v", models)
	}
}

func TestParseHermesSessionNewModels(t *testing.T) {
	// Mirrors the real shape emitted by hermes'
	// acp_adapter/server.py _build_model_state -> SessionModelState.
	raw := []byte(`{
      "sessionId": "ses_123",
      "models": {
        "availableModels": [
          {"modelId": "nous:moonshotai/kimi-k2.5", "name": "moonshotai/kimi-k2.5", "description": "Provider: Nous"},
          {"modelId": "nous:anthropic/claude-opus-4.7", "name": "anthropic/claude-opus-4.7", "description": "Provider: Nous • current"},
          {"modelId": "nous:moonshotai/kimi-k2.5", "name": "duplicate", "description": "dup"}
        ],
        "currentModelId": "nous:anthropic/claude-opus-4.7"
      }
    }`)
	models := parseACPSessionNewModels(raw)
	if len(models) != 2 {
		t.Fatalf("expected 2 models (duplicate deduped), got %d: %+v", len(models), models)
	}
	if models[0].ID != "nous:moonshotai/kimi-k2.5" || models[0].Provider != "nous" {
		t.Errorf("unexpected first model: %+v", models[0])
	}
	if models[0].Default {
		t.Errorf("non-current entry must not be marked default: %+v", models[0])
	}
	if !models[1].Default {
		t.Errorf("current entry must be marked default: %+v", models[1])
	}
	if models[1].ID != "nous:anthropic/claude-opus-4.7" {
		t.Errorf("expected current model second: %+v", models[1])
	}
}

func TestParseHermesSessionNewModelsPreservesCustomModelIDsWithColons(t *testing.T) {
	raw := []byte(`{
      "sessionId": "ses_123",
      "models": {
        "availableModels": [
          {"modelId": "custom:lfm2.5:8b", "name": "lfm2.5:8b", "description": "Provider: Custom"}
        ],
        "currentModelId": "custom:lfm2.5:8b"
      }
    }`)
	models := parseACPSessionNewModels(raw)
	if len(models) != 1 {
		t.Fatalf("expected 1 model, got %d: %+v", len(models), models)
	}
	if models[0].ID != "custom:lfm2.5:8b" {
		t.Errorf("model id must be preserved verbatim, got %+v", models[0])
	}
	if models[0].Provider != "custom" {
		t.Errorf("provider should be derived from the first colon only, got %+v", models[0])
	}
	if !models[0].Default {
		t.Errorf("current custom model should be marked default: %+v", models[0])
	}
}

func TestParseHermesSessionNewModelsUnknownNames(t *testing.T) {
	raw := []byte(`{
	  "sessionId": "ses_123",
	  "models": {
	    "availableModels": [
	      {"modelId": "nous:moonshotai/kimi-k2.6", "name": "Unknown", "description": "Provider: Nous"},
	      {"modelId": "nous:anthropic/claude-sonnet-4.6", "name": "unknown", "description": "Provider: Nous"}
	    ],
	    "currentModelId": "nous:moonshotai/kimi-k2.6"
	  }
	}`)
	models := parseACPSessionNewModels(raw)
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d: %+v", len(models), models)
	}
	if models[0].Label != "nous:moonshotai/kimi-k2.6" {
		t.Errorf("Unknown label should fall back to model id, got %+v", models[0])
	}
	if !models[0].Default {
		t.Errorf("current model should be marked default: %+v", models[0])
	}
	if models[1].Label != "nous:anthropic/claude-sonnet-4.6" {
		t.Errorf("lowercase unknown label should fall back to model id, got %+v", models[1])
	}
}

func TestParseHermesSessionNewModelsMissingField(t *testing.T) {
	// session/new without the models field — for example when the runtime
	// cannot build its model state — should yield nil so the caller
	// can distinguish "no catalog" from "empty catalog".
	raw := []byte(`{"sessionId": "ses_123"}`)
	if got := parseACPSessionNewModels(raw); len(got) != 0 {
		t.Errorf("expected nil/empty, got %+v", got)
	}
}

func TestParseHermesSessionNewModelsGarbage(t *testing.T) {
	if got := parseACPSessionNewModels([]byte("not json")); got != nil {
		t.Errorf("expected nil for non-JSON, got %+v", got)
	}
}

// TestParseAntigravityModels covers the `agy models` line-per-name format:
// each non-blank line becomes a Model whose ID and Label are the verbatim
// display string `--model` expects, duplicates collapse, and blanks drop.
func TestParseAntigravityModels(t *testing.T) {
	t.Parallel()

	out := strings.Join([]string{
		"Gemini 3.5 Flash (Medium)",
		"Claude Opus 4.6 (Thinking)",
		"", // blank line — skipped
		"GPT-OSS 120B (Medium)",
		"Claude Opus 4.6 (Thinking)", // duplicate — collapsed
	}, "\n")

	got := parseAntigravityModels(out)
	want := []Model{
		{ID: "Gemini 3.5 Flash (Medium)", Label: "Gemini 3.5 Flash (Medium)", Provider: "antigravity"},
		{ID: "Claude Opus 4.6 (Thinking)", Label: "Claude Opus 4.6 (Thinking)", Provider: "antigravity"},
		{ID: "GPT-OSS 120B (Medium)", Label: "GPT-OSS 120B (Medium)", Provider: "antigravity"},
	}
	if len(got) != len(want) {
		t.Fatalf("parseAntigravityModels len = %d, want %d (%+v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("model[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestParseAntigravityModelsEmpty pins that empty / whitespace-only output
// yields no models (so cachedDiscovery treats it as a transient miss and
// retries rather than caching a blank catalog).
func TestParseAntigravityModelsEmpty(t *testing.T) {
	t.Parallel()
	if got := parseAntigravityModels("   \n\t\n"); len(got) != 0 {
		t.Errorf("expected no models for blank output, got %+v", got)
	}
}

func TestCachedDiscovery(t *testing.T) {
	calls := 0
	fn := func() ([]Model, error) {
		calls++
		return []Model{{ID: "x", Label: "x"}}, nil
	}
	// First call populates the cache; reset for isolation.
	modelCacheMu.Lock()
	delete(modelCache, "testkey")
	modelCacheMu.Unlock()

	if _, err := cachedDiscovery("testkey", fn); err != nil {
		t.Fatal(err)
	}
	if _, err := cachedDiscovery("testkey", fn); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("expected 1 underlying call due to cache, got %d", calls)
	}
}
