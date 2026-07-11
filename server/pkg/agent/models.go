package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Model describes a single LLM model exposed by an agent provider.
// The dropdown groups by Provider when the ID uses the
// `provider/model` form (e.g. "openai/gpt-4o" from opencode).
// Default is a *display* hint: the UI badges the entry the
// runtime advertises as its preferred pick (e.g. Claude Code's
// shipped default, or hermes' currentModelId). It has no effect
// at execution time — when agent.model is empty the daemon passes
// "" to the backend so each provider's own CLI resolves its own
// default, which is always closer to what the user's account /
// environment actually supports than a static guess here.
type Model struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Provider string `json:"provider,omitempty"`
	Default  bool   `json:"default,omitempty"`
	// Thinking advertises the runtime's reasoning/effort catalog for this
	// model. nil means the runtime/model has no thinking-level control
	// (or the daemon couldn't discover one); the UI hides its picker. The
	// catalog is per-model because Codex's `codex debug models` is itself
	// per-model and Claude's `--effort` superset has known per-model gaps
	// (`xhigh` is Opus-only, `max` is session-only). See MUL-2339.
	Thinking *ModelThinking `json:"thinking,omitempty"`
}

// ModelThinking carries the per-model reasoning/effort catalog
// surfaced by an agent runtime. Values are runtime-native — Codex
// emits "none|minimal|low|medium|high|xhigh"; Claude emits
// "low|medium|high|xhigh|max". The frontend renders SupportedLevels
// as-is so what users see matches each CLI's own UI.
type ModelThinking struct {
	SupportedLevels []ThinkingLevel `json:"supported_levels"`
	// DefaultLevel is the value the runtime picks when no override is
	// provided. Empty means "the runtime picks, we don't know" — the
	// UI shows "Default" as a generic option.
	DefaultLevel string `json:"default_level,omitempty"`
}

// ThinkingLevel is one entry in a ModelThinking.SupportedLevels list.
// Value is the literal token passed to the CLI (Claude `--effort <value>`
// or Codex `model_reasoning_effort=<value>`); Label is a display string;
// Description is optional helper copy lifted from the upstream catalog
// when available (Codex's `description` field).
type ThinkingLevel struct {
	Value       string `json:"value"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// modelCache memoizes dynamic discovery calls so repeated UI loads
// don't re-shell the agent CLI. Entries expire after cacheTTL.
type modelCacheEntry struct {
	models    []Model
	expiresAt time.Time
}

var (
	modelCacheMu sync.Mutex
	modelCache   = map[string]modelCacheEntry{}
)

const modelCacheTTL = 60 * time.Second

// ListModels returns the models supported by the given agent provider.
// For providers with a known static catalog it returns the baked-in
// list; for providers with a CLI discovery mechanism (opencode, pi,
// openclaw) it shells out with caching and falls back to the static
// list on failure.
//
// For claude, codex, and opencode, the catalog is augmented with per-model
// thinking-level options discovered from the local CLI. Discovery failures
// silently leave Thinking == nil on each entry, which the UI treats as
// "no picker for this model" rather than blocking model selection.
//
// executablePath lets the caller point at a non-default binary; pass
// "" to use the provider's default name on PATH.
func ListModels(ctx context.Context, providerType, executablePath string) ([]Model, error) {
	switch providerType {
	case "claude":
		models := claudeStaticModels()
		annotateClaudeThinking(ctx, models, executablePath)
		return models, nil
	case "codex":
		models := codexStaticModels()
		annotateCodexThinking(ctx, models, executablePath)
		return models, nil
	case "gemini":
		return geminiStaticModels(), nil
	case "antigravity":
		// agy 1.0.6 added a `--model` flag plus an `agy models` catalog
		// command (MUL-3125). Enumerate it on demand like the other
		// dynamic-discovery backends.
		return cachedDiscovery(providerType, func() ([]Model, error) {
			return discoverAntigravityModels(ctx, executablePath)
		})
	case "cursor":
		return cachedDiscovery(providerType, func() ([]Model, error) {
			return discoverCursorModels(ctx, executablePath)
		})
	case "copilot":
		return cachedDiscovery(providerType, func() ([]Model, error) {
			return discoverCopilotModels(ctx, executablePath)
		})
	case "hermes":
		return cachedDiscovery(providerType, func() ([]Model, error) {
			return discoverHermesModels(ctx, executablePath)
		})
	case "kimi":
		return cachedDiscovery(providerType, func() ([]Model, error) {
			return discoverKimiModels(ctx, executablePath)
		})
	case "kiro":
		return cachedDiscovery(providerType, func() ([]Model, error) {
			return discoverKiroModels(ctx, executablePath)
		})
	case "opencode":
		return cachedDiscovery(discoveryCacheKey(providerType, executablePath), func() ([]Model, error) {
			return discoverOpenCodeModels(ctx, executablePath)
		})
	case "pi":
		return cachedDiscovery(providerType, func() ([]Model, error) {
			return discoverPiModels(ctx, executablePath)
		})
	case "openclaw":
		return cachedDiscovery(providerType, func() ([]Model, error) {
			return discoverOpenclawAgents(ctx, executablePath)
		})
	case "codebuddy":
		return cachedDiscovery(providerType, func() ([]Model, error) {
			models, err := discoverCodebuddyModels(ctx, executablePath)
			if err != nil {
				return nil, err
			}
			annotateCodebuddyThinking(ctx, models, executablePath)
			return models, nil
		})
	default:
		return nil, fmt.Errorf("unknown agent type: %q", providerType)
	}
}

// ModelSelectionSupported reports whether setting `agent.model` has
// any effect for the given provider. Every built-in provider now honours
// `opts.Model` end-to-end — Hermes routes it through the ACP
// `session/set_model` RPC before each prompt; Claude / Codex / Cursor /
// Gemini / Copilot / Kimi / Kiro / OpenCode / OpenClaw / Pi / Antigravity
// pass it via flag or session config (Antigravity gained `--model` in agy
// 1.0.6 — MUL-3125).
//
// The hook is retained — rather than inlining `true` at the call sites — so
// a future model-less runtime can opt out in one place, which makes the UI
// render a disabled "Managed by runtime" picker instead of an empty
// dropdown plus a silently-ignored manual-entry field.
func ModelSelectionSupported(providerType string) bool {
	return true
}

// ModelKnownIncompatibleWithProvider reports whether a saved model is a known
// mismatch for a target runtime provider. For first-party providers with
// maintained static catalogs, compatibility is exact: the model must be one of
// the IDs that runtime advertises. Unknown/custom model strings still return
// false because the UI and CLI allow manual entries and the server should not
// erase values it cannot confidently classify.
func ModelKnownIncompatibleWithProvider(providerType, model string) bool {
	model = strings.TrimSpace(model)
	if model == "" {
		return false
	}

	accepted, ok := acceptedModelIDsForProvider(providerType)
	if !ok {
		return false
	}
	if accepted[model] {
		return false
	}
	return isRuntimeSpecificModelID(model)
}

func acceptedModelIDsForProvider(providerType string) (map[string]bool, bool) {
	switch providerType {
	case "claude":
		return modelIDSet(claudeStaticModels()), true
	case "codex":
		return modelIDSet(codexStaticModels()), true
	case "gemini":
		return modelIDSet(geminiStaticModels()), true
	default:
		return nil, false
	}
}

func modelIDSet(models []Model) map[string]bool {
	out := make(map[string]bool, len(models))
	for _, m := range models {
		out[m.ID] = true
	}
	return out
}

func isRuntimeSpecificModelID(model string) bool {
	if strings.Contains(model, "/") {
		return true
	}
	return modelHasKnownPrefix(model) ||
		modelIDSet(claudeStaticModels())[model] ||
		modelIDSet(codexStaticModels())[model] ||
		modelIDSet(geminiStaticModels())[model]
}

func modelHasKnownPrefix(model string) bool {
	return strings.HasPrefix(model, "claude-") ||
		strings.HasPrefix(model, "gpt-") ||
		strings.HasPrefix(model, "gemini-") ||
		strings.HasPrefix(model, "auto-gemini-") ||
		isOpenAIReasoningSeriesID(model)
}

// cachedDiscovery invokes fn and caches the result for modelCacheTTL.
// The cache is keyed on providerType only; callers that need to
// distinguish discovery by host/user should include that in the key
// if we ever introduce such a mode.
func cachedDiscovery(key string, fn func() ([]Model, error)) ([]Model, error) {
	modelCacheMu.Lock()
	if entry, ok := modelCache[key]; ok && time.Now().Before(entry.expiresAt) {
		out := entry.models
		modelCacheMu.Unlock()
		return out, nil
	}
	modelCacheMu.Unlock()

	models, err := fn()
	if err != nil {
		return nil, err
	}

	// Don't cache an empty result. Zero models is almost always a transient
	// failure (discovery CLI timeout, not-logged-in, network blip) rather than
	// a runtime that genuinely has no models; caching it would keep the picker
	// blank for the full TTL even after the cause clears. Skipping the cache
	// lets the next request retry immediately. See #3729.
	if len(models) == 0 {
		return models, nil
	}

	modelCacheMu.Lock()
	modelCache[key] = modelCacheEntry{models: models, expiresAt: time.Now().Add(modelCacheTTL)}
	modelCacheMu.Unlock()
	return models, nil
}

func discoveryCacheKey(providerType, executablePath string) string {
	if executablePath == "" {
		return providerType
	}
	return providerType + ":" + executablePath
}

// ── Static catalogs ──

// claudeStaticModels reflects the Claude Code CLI's accepted --model
// values. Keep this list short and current; stale entries here
// mislead users more than they help. Default = Sonnet because it's
// the everyday workhorse (Opus is reserved for advisor-style flows).
func claudeStaticModels() []Model {
	return []Model{
		{ID: "claude-sonnet-4-6", Label: "Claude Sonnet 4.6", Provider: "anthropic", Default: true},
		{ID: "claude-fable-5", Label: "Claude Fable 5", Provider: "anthropic"},
		{ID: "claude-opus-4-8", Label: "Claude Opus 4.8", Provider: "anthropic"},
		{ID: "claude-opus-4-7", Label: "Claude Opus 4.7", Provider: "anthropic"},
		{ID: "claude-haiku-4-5-20251001", Label: "Claude Haiku 4.5", Provider: "anthropic"},
		{ID: "claude-opus-4-6", Label: "Claude Opus 4.6", Provider: "anthropic"},
		{ID: "claude-sonnet-4-5", Label: "Claude Sonnet 4.5", Provider: "anthropic"},
	}
}

func codexStaticModels() []Model {
	return []Model{
		{ID: "gpt-5.5", Label: "GPT-5.5", Provider: "openai", Default: true},
		{ID: "gpt-5.5-mini", Label: "GPT-5.5 mini", Provider: "openai"},
		{ID: "gpt-5.4", Label: "GPT-5.4", Provider: "openai"},
		{ID: "gpt-5.4-mini", Label: "GPT-5.4 mini", Provider: "openai"},
		{ID: "gpt-5.3-codex-spark", Label: "GPT-5.3 Codex Spark", Provider: "openai"},
		{ID: "gpt-5.3-codex", Label: "GPT-5.3 Codex", Provider: "openai"},
		{ID: "gpt-5", Label: "GPT-5", Provider: "openai"},
		{ID: "o3", Label: "o3", Provider: "openai"},
		{ID: "o3-mini", Label: "o3-mini", Provider: "openai"},
	}
}

// geminiStaticModels lists the values we pass via `gemini -m`. Gemini
// CLI has no `models list` subcommand, so dynamic discovery isn't
// possible; the next best thing is to expose the CLI's own aliases
// (auto / pro / flash / flash-lite and the `auto-gemini-*` family)
// alongside a few explicit version pins. Aliases track whatever the
// installed CLI considers current (see `resolveModel` in the CLI's
// packages/core/src/config/models.ts), so new Gemini releases light
// up without a Multica redeploy. Default is `auto` to match Google's
// recommendation — the CLI picks Pro vs Flash per task and falls back
// when quota is exhausted.
func geminiStaticModels() []Model {
	return []Model{
		{ID: "auto", Label: "Auto (Gemini 3)", Provider: "google", Default: true},
		{ID: "auto-gemini-2.5", Label: "Auto (Gemini 2.5)", Provider: "google"},
		{ID: "pro", Label: "Pro", Provider: "google"},
		{ID: "flash", Label: "Flash", Provider: "google"},
		{ID: "flash-lite", Label: "Flash Lite", Provider: "google"},
		{ID: "gemini-3-pro-preview", Label: "Gemini 3 Pro (preview)", Provider: "google"},
		{ID: "gemini-3-flash-preview", Label: "Gemini 3 Flash (preview)", Provider: "google"},
		{ID: "gemini-2.5-pro", Label: "Gemini 2.5 Pro", Provider: "google"},
		{ID: "gemini-2.5-flash", Label: "Gemini 2.5 Flash", Provider: "google"},
		{ID: "gemini-2.5-flash-lite", Label: "Gemini 2.5 Flash Lite", Provider: "google"},
	}
}

// cursorStaticModels is a minimal fallback used when
// `cursor-agent --list-models` isn't available (binary missing,
// offline, etc). The real catalog is fetched dynamically because
// Cursor's model IDs shift (e.g. `composer-2-fast`,
// `claude-4.6-sonnet-medium`, `gemini-3.1-pro`) and any static
// list we ship goes stale fast.
func cursorStaticModels() []Model {
	return []Model{
		{ID: "auto", Label: "Auto", Provider: "cursor", Default: true},
	}
}

// copilotStaticModels — fallback used when GitHub Copilot CLI is
// missing on PATH or the user hasn't logged in. Normal operation
// goes through discoverCopilotModels(), which speaks ACP to the
// CLI and gets the live catalog (including which IDs the user's
// account actually has access to). This list is just a safety net
// so the UI dropdown still has reasonable options when the live
// query fails.
//
// Source: https://docs.github.com/en/copilot/reference/ai-models/supported-models
// IDs use the dotted form `copilot --model <id>` actually accepts.
func copilotStaticModels() []Model {
	return []Model{
		// OpenAI
		{ID: "gpt-5.5", Label: "GPT-5.5", Provider: "openai"},
		{ID: "gpt-5.4", Label: "GPT-5.4", Provider: "openai"},
		{ID: "gpt-5.4-mini", Label: "GPT-5.4 mini", Provider: "openai"},
		{ID: "gpt-5.3-codex-spark", Label: "GPT-5.3-Codex-Spark", Provider: "openai"},
		{ID: "gpt-5.3-codex", Label: "GPT-5.3-Codex", Provider: "openai"},
		{ID: "gpt-5.2-codex", Label: "GPT-5.2-Codex", Provider: "openai"},
		{ID: "gpt-5.2", Label: "GPT-5.2", Provider: "openai"},
		{ID: "gpt-5-mini", Label: "GPT-5 mini", Provider: "openai"},
		{ID: "gpt-4.1", Label: "GPT-4.1", Provider: "openai"},
		// Anthropic
		{ID: "claude-opus-4.7", Label: "Claude Opus 4.7", Provider: "anthropic"},
		{ID: "claude-sonnet-4.6", Label: "Claude Sonnet 4.6", Provider: "anthropic"},
		{ID: "claude-sonnet-4.5", Label: "Claude Sonnet 4.5", Provider: "anthropic"},
		{ID: "claude-haiku-4.5", Label: "Claude Haiku 4.5", Provider: "anthropic"},
	}
}

// inferCopilotProvider tags Copilot model IDs with a vendor name so
// the UI can group them. The Copilot CLI's ACP `availableModels`
// payload exposes only `modelId`/`name`; the vendor is implicit in
// the prefix. Returning "" leaves the entry ungrouped, which
// matches what other ACP discovery paths (hermes/kimi) do for
// non-prefixed IDs.
//
// The OpenAI reasoning series (`o1`, `o3`, `o3-mini`, `o4-mini`,
// future `o5`/`o6`/…) is matched by the generic `o<digit>…`
// pattern so we don't have to chase every new generation.
func inferCopilotProvider(modelID string) string {
	switch {
	case strings.HasPrefix(modelID, "gpt-") || isOpenAIReasoningSeriesID(modelID):
		return "openai"
	case strings.HasPrefix(modelID, "claude-"):
		return "anthropic"
	case strings.HasPrefix(modelID, "gemini-"):
		return "google"
	case strings.HasPrefix(modelID, "grok-"):
		return "xai"
	default:
		return ""
	}
}

// isOpenAIReasoningSeriesID matches IDs in OpenAI's `o`-prefixed
// reasoning family: lowercase `o` followed by at least one digit
// and then either end-of-string or a `-` separator (e.g. `o3`,
// `o3-mini`, `o4-mini-high`). Avoids false positives like
// `opus-…` or random IDs that happen to start with `o`.
func isOpenAIReasoningSeriesID(id string) bool {
	if len(id) < 2 || id[0] != 'o' {
		return false
	}
	i := 1
	for i < len(id) && id[i] >= '0' && id[i] <= '9' {
		i++
	}
	if i == 1 {
		return false
	}
	return i == len(id) || id[i] == '-'
}

// ── Dynamic discovery ──

// discoverOpenCodeModels runs `opencode models --verbose` and parses its
// output. The CLI prints `provider/model` rows, followed by JSON metadata
// when verbose mode is enabled; we emit IDs verbatim so what the user sees
// matches what `--model` accepts, and project any model `variants` into the
// thinking-level picker because OpenCode's `run --variant` flag is its
// provider-specific reasoning-effort surface.
// On any failure (CLI missing, parse error, timeout) we fall back to
// an empty list so the creatable UI still works.
