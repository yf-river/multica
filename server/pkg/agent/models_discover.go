package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
)

func discoverOpenCodeModels(ctx context.Context, executablePath string) ([]Model, error) {
	if executablePath == "" {
		executablePath = "opencode"
	}
	if _, err := exec.LookPath(executablePath); err != nil {
		return []Model{}, nil
	}
	// Newer opencode (1.15+) syncs its hosted free-model catalog over the
	// network on `opencode models`, which can take ~6s; the previous 5s cap
	// timed out and returned an empty list, so the runtime showed online but
	// the model picker was empty. See multica-ai/multica#3627.
	runCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(runCtx, executablePath, "models", "--verbose")
	hideAgentWindow(cmd)
	// Parse whatever the command printed, even on a non-zero exit — a stale
	// config entry can make discovery fail after listing resolvable models.
	out, err := cmd.Output()
	models := parseOpenCodeModels(string(out))
	if len(models) == 0 && err != nil {
		return nil, fmt.Errorf("discover OpenCode models: %w", err)
	}
	if len(models) == 0 {
		return []Model{}, nil
	}
	return models, nil
}

// parseOpenCodeModels accepts the `opencode models --verbose` output and
// extracts IDs. Each ID is followed by a pretty-printed JSON object; when
// that object contains `variants`, each enabled variant becomes a thinking
// level that the backend later passes through `opencode run --variant`.
func parseOpenCodeModels(output string) []Model {
	lines := strings.Split(output, "\n")
	var models []Model
	indexByID := map[string]int{}
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		id := parseOpenCodeModelIDLine(line)
		if id == "" {
			continue
		}
		idx, seen := indexByID[id]
		if !seen {
			provider := ""
			if slash := strings.Index(id, "/"); slash > 0 {
				provider = id[:slash]
			}
			idx = len(models)
			indexByID[id] = idx
			models = append(models, Model{ID: id, Label: id, Provider: provider})
		}

		next := i + 1
		for next < len(lines) && strings.TrimSpace(lines[next]) == "" {
			next++
		}
		if next >= len(lines) || !strings.HasPrefix(strings.TrimSpace(lines[next]), "{") {
			continue
		}
		raw, resumeAt := collectOpenCodeModelJSON(lines, next)
		if json.Valid(raw) {
			annotateOpenCodeModelMetadata(&models[idx], raw)
		}
		i = resumeAt - 1
	}
	return models
}

func parseOpenCodeModelIDLine(line string) string {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return ""
	}
	id := fields[0]
	if strings.HasPrefix(id, `"`) || strings.HasPrefix(id, "{") || strings.HasPrefix(id, "[") {
		return ""
	}
	if !strings.Contains(id, "/") {
		return ""
	}
	return id
}

func collectOpenCodeModelJSON(lines []string, start int) ([]byte, int) {
	var b strings.Builder
	for i := start; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if i > start && parseOpenCodeModelIDLine(line) != "" {
			return []byte(b.String()), i
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(lines[i])
		if json.Valid([]byte(b.String())) {
			return []byte(b.String()), i + 1
		}
	}
	return []byte(b.String()), len(lines)
}

type opencodeModelMetadata struct {
	Reasoning bool                            `json:"reasoning"`
	Variants  map[string]opencodeModelVariant `json:"variants"`
}

type opencodeModelVariant struct {
	Disabled        bool            `json:"disabled"`
	ReasoningEffort string          `json:"reasoningEffort"`
	Thinking        json.RawMessage `json:"thinking"`
}

var opencodeVariantLabel = map[string]string{
	"none":    "None",
	"minimal": "Minimal",
	"low":     "Low",
	"medium":  "Medium",
	"high":    "High",
	"xhigh":   "Extra high",
	"max":     "Max",
}

var opencodeVariantOrder = map[string]int{
	"none":    0,
	"minimal": 1,
	"low":     2,
	"medium":  3,
	"high":    4,
	"xhigh":   5,
	"max":     6,
}

func annotateOpenCodeModelMetadata(model *Model, raw []byte) {
	var meta opencodeModelMetadata
	if err := json.Unmarshal(raw, &meta); err != nil {
		return
	}
	if !meta.Reasoning && !openCodeVariantsLookReasoning(meta.Variants) {
		return
	}
	levels := openCodeThinkingLevelsFromVariants(meta.Variants)
	if len(levels) == 0 {
		return
	}
	model.Thinking = &ModelThinking{SupportedLevels: levels}
}

func openCodeVariantsLookReasoning(variants map[string]opencodeModelVariant) bool {
	for name, variant := range variants {
		if _, known := opencodeVariantOrder[name]; known {
			return true
		}
		if variant.ReasoningEffort != "" || len(variant.Thinking) > 0 {
			return true
		}
	}
	return false
}

func openCodeThinkingLevelsFromVariants(variants map[string]opencodeModelVariant) []ThinkingLevel {
	if len(variants) == 0 {
		return nil
	}
	values := make([]string, 0, len(variants))
	for value, variant := range variants {
		if value == "" || variant.Disabled {
			continue
		}
		values = append(values, value)
	}
	sort.Slice(values, func(i, j int) bool {
		left, leftKnown := opencodeVariantOrder[values[i]]
		right, rightKnown := opencodeVariantOrder[values[j]]
		if leftKnown && rightKnown {
			return left < right
		}
		if leftKnown != rightKnown {
			return leftKnown
		}
		return values[i] < values[j]
	})
	levels := make([]ThinkingLevel, 0, len(values))
	for _, value := range values {
		label, ok := opencodeVariantLabel[value]
		if !ok {
			label = strings.Title(strings.ReplaceAll(value, "-", " ")) //nolint:staticcheck
		}
		levels = append(levels, ThinkingLevel{Value: value, Label: label})
	}
	return levels
}

// discoverPiModels runs `pi --list-models` and parses its stdout table.
func discoverPiModels(ctx context.Context, executablePath string) ([]Model, error) {
	if executablePath == "" {
		executablePath = "pi"
	}
	if _, err := exec.LookPath(executablePath); err != nil {
		return []Model{}, nil
	}
	// Newer pi fetches its catalog from each configured provider over the
	// network, so discovery time scales with provider count — a multi-provider
	// setup measured ~4.6-4.8s, right at the old 5s cap. When jitter pushed it
	// over, the daemon killed the command before it printed anything and the
	// model picker came back empty while the runtime stayed online. 15s matches
	// the opencode discovery cap (see #3729, same class as #3627).
	runCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(runCtx, executablePath, "--list-models")
	hideAgentWindow(cmd)
	stdout, err := cmd.Output()
	if err != nil && len(stdout) == 0 {
		return []Model{}, nil
	}
	return parsePiModels(string(stdout)), nil
}

// parsePiModels accepts the current `pi --list-models` table
// (`provider  model  context …`) and normalizes each row to the
// `provider/model` convention used by OpenCode and the UI.
func parsePiModels(output string) []Model {
	scanner := bufio.NewScanner(strings.NewReader(output))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var models []Model
	seen := map[string]bool{}
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		// pi interleaves human-readable diagnostics with the catalog when an
		// agent config references stale patterns — e.g.
		//   Warning: No models match pattern "opencode-go/mimo-v2-omni"
		// Skip them before field-splitting; otherwise prose tokens are coined
		// into bogus models like `No/models` or `Warning/`. See #3729.
		if isPiDiscoveryNoise(line) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		first := fields[0]
		if strings.EqualFold(first, "provider") {
			continue
		}
		if len(fields) < 2 {
			continue
		}
		id := first + "/" + fields[1]
		// A real id has a non-empty provider and model on both sides of the
		// slash. Drop anything that doesn't (e.g. a stray `something:` token),
		// a cheap structural backstop on top of the diagnostic filter above.
		if slash := strings.Index(id, "/"); slash <= 0 || slash == len(id)-1 {
			continue
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		provider := ""
		if i := strings.Index(id, "/"); i > 0 {
			provider = id[:i]
		}
		models = append(models, Model{ID: id, Label: id, Provider: provider})
	}
	return models
}

// isPiDiscoveryNoise reports whether a `pi --list-models` line is a diagnostic
// message rather than a catalog row. pi prints these alongside the table when
// an agent config references stale provider/model patterns, e.g.
//
//	Warning: No models match pattern "opencode-go/mimo-v2-omni"
//
// The `Warning:` prefix is not guaranteed across versions, so the unmatched-
// pattern message is also matched on its own. These are prose, not
// `provider model` rows; without skipping them the field splitter coins bogus
// models like `No/models`. See #3729.
func isPiDiscoveryNoise(line string) bool {
	lower := strings.ToLower(line)
	if strings.Contains(lower, "no models match pattern") {
		return true
	}
	return strings.HasPrefix(lower, "warning:") ||
		strings.HasPrefix(lower, "error:") ||
		strings.HasPrefix(lower, "info:")
}

// discoverHermesModels spins up a throwaway `hermes acp` process,
// drives just enough of the protocol to receive the model list
// advertised in the `session/new` response, and shuts it down. The
// list and the `current` flag both come from hermes' own
// `_build_model_state` so whatever ~/.hermes/config.yaml resolves
// to at runtime is exactly what the UI shows.
//
// Failure modes (hermes missing, no credentials, config resolution
// error) all return an empty list so the UI falls back to the
// creatable manual-entry input instead of blocking the form.
func discoverHermesModels(ctx context.Context, executablePath string) ([]Model, error) {
	return discoverACPModels(ctx, executablePath, acpDiscoveryProvider{
		defaultBin:   "hermes",
		clientName:   "multica-model-discovery",
		extraEnv:     []string{"HERMES_YOLO_MODE=1"},
		tmpdirPrefix: "multica-hermes-discovery-",
	})
}

// discoverKimiModels spins up a throwaway `kimi acp` process and
// drives the same minimal ACP handshake as Hermes to surface the
// model catalog advertised by Kimi's `session/new` response. Kimi's
// ACPServer.new_session returns a `models` block of the same shape
// (`availableModels`/`currentModelId`) so the parsing path is shared.
//
// Failure modes (kimi missing, not logged in, config error) all
// return an empty list so the UI falls back to manual entry.
func discoverKimiModels(ctx context.Context, executablePath string) ([]Model, error) {
	return discoverACPModels(ctx, executablePath, acpDiscoveryProvider{
		defaultBin:   "kimi",
		clientName:   "multica-model-discovery",
		tmpdirPrefix: "multica-kimi-discovery-",
	})
}

// discoverKiroModels spins up a throwaway `kiro-cli acp` process and parses
// the models block Kiro returns from session/new.
func discoverKiroModels(ctx context.Context, executablePath string) ([]Model, error) {
	return discoverACPModels(ctx, executablePath, acpDiscoveryProvider{
		defaultBin:   "kiro-cli",
		clientName:   "multica-model-discovery",
		tmpdirPrefix: "multica-kiro-discovery-",
	})
}

// discoverCopilotModels spins up `copilot --acp` and reads the
// `availableModels` block from session/new. The catalog is keyed
// off the user's GitHub account, so this is the only way to know
// which IDs they actually have access to (Pro vs Pro+ vs
// Enterprise vs evaluation models).
//
// Falls back to copilotStaticModels() when the binary is missing
// or when the ACP handshake fails (auth missing, network down,
// etc.) so the UI dropdown always has something to show.
//
// We also tag each entry with a vendor in the Provider field —
// the Copilot ACP payload doesn't include one, but the UI groups
// by Provider, so deriving it from the ID prefix keeps OpenAI /
// Anthropic / Gemini sections distinct.
//
// No extra env or permission flags are needed: discovery only
// drives `initialize` + `session/new`, neither of which triggers
// a tool-permission prompt — the model catalog is part of the
// session/new response itself.
func discoverCopilotModels(ctx context.Context, executablePath string) ([]Model, error) {
	models, err := discoverACPModels(ctx, executablePath, acpDiscoveryProvider{
		defaultBin:   "copilot",
		clientName:   "multica-model-discovery",
		tmpdirPrefix: "multica-copilot-discovery-",
		acpArgs:      []string{"--acp"},
	})
	if err != nil || len(models) == 0 {
		return copilotStaticModels(), nil
	}
	for i := range models {
		if models[i].Provider == "" {
			models[i].Provider = inferCopilotProvider(models[i].ID)
		}
	}
	return models, nil
}

// acpDiscoveryProvider configures how discoverACPModels launches an
// ACP-speaking agent CLI. The shared helper drives every CLI in
// the same way (initialize → session/new → parse models block) — the
// per-provider differences are which binary to spawn, which env
// vars suppress interactive prompts during init, what argv puts
// the binary into ACP server mode (most use `acp`, Copilot uses
// `--acp`), and what to label temporary work directories so they're
// easy to identify in logs.
type acpDiscoveryProvider struct {
	defaultBin   string
	clientName   string
	extraEnv     []string
	tmpdirPrefix string
	// acpArgs is the argv passed to the binary to start it in ACP
	// server mode. Defaults to []string{"acp"} when nil/empty.
	acpArgs []string
}

// discoverACPModels runs the ACP handshake for any agent CLI that
// implements the standard `initialize` + `session/new` flow and
// advertises its model catalog in the response under
// `models.availableModels` / `models.currentModelId`. This covers
// Hermes and Kimi today; future ACP backends can plug in by adding
// an acpDiscoveryProvider entry instead of duplicating the loop.
func discoverACPModels(ctx context.Context, executablePath string, p acpDiscoveryProvider) ([]Model, error) {
	if executablePath == "" {
		executablePath = p.defaultBin
	}
	if _, err := exec.LookPath(executablePath); err != nil {
		return []Model{}, nil
	}
	runCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	cmdArgs := p.acpArgs
	if len(cmdArgs) == 0 {
		cmdArgs = []string{"acp"}
	}
	cmd := exec.CommandContext(runCtx, executablePath, cmdArgs...)
	hideAgentWindow(cmd)
	if len(p.extraEnv) > 0 {
		cmd.Env = append(os.Environ(), p.extraEnv...)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return []Model{}, nil
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return []Model{}, nil
	}
	// Discard stderr; noisy logs here don't help us and we don't
	// want them bleeding into the daemon log every 60s.
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return []Model{}, nil
	}
	// Ensure the child process is always reaped.
	defer func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	writeACP := func(id int, method string, params map[string]any) error {
		msg := map[string]any{
			"jsonrpc": "2.0",
			"id":      id,
			"method":  method,
			"params":  params,
		}
		data, err := json.Marshal(msg)
		if err != nil {
			return err
		}
		data = append(data, '\n')
		_, err = stdin.Write(data)
		return err
	}

	// Send initialize + session/new.
	if err := writeACP(1, "initialize", map[string]any{
		"protocolVersion":    1,
		"clientInfo":         map[string]any{"name": p.clientName, "version": "0.1.0"},
		"clientCapabilities": map[string]any{},
	}); err != nil {
		return []Model{}, nil
	}

	// session/new requires a valid cwd — use a temp directory we
	// clean up afterwards, not the daemon's workdir (which might
	// be in the middle of another task's worktree).
	tmp, err := os.MkdirTemp("", p.tmpdirPrefix)
	if err != nil {
		return []Model{}, nil
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	if err := writeACP(2, "session/new", map[string]any{
		"cwd":        tmp,
		"mcpServers": []any{},
	}); err != nil {
		return []Model{}, nil
	}

	// Read responses until we see the one for id=2 (session/new).
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 1024*1024), 4*1024*1024)
	deadline := time.After(12 * time.Second)
	done := make(chan []Model, 1)
	go func() {
		defer close(done)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			var env struct {
				ID     json.Number     `json:"id"`
				Result json.RawMessage `json:"result"`
			}
			if err := json.Unmarshal([]byte(line), &env); err != nil {
				continue
			}
			if env.ID.String() != "2" || len(env.Result) == 0 {
				continue
			}
			done <- parseACPSessionNewModels(env.Result)
			return
		}
	}()

	select {
	case models := <-done:
		if models == nil {
			return []Model{}, nil
		}
		return models, nil
	case <-deadline:
		return []Model{}, nil
	case <-runCtx.Done():
		return []Model{}, nil
	}
}

// parseACPSessionNewModels extracts the model catalog from an ACP
// `session/new` response. Both Hermes and Kimi (and any other ACP
// agent that follows the standard schema) emit:
//
//	{
//	  "sessionId": "...",
//	  "models": {
//	    "availableModels": [
//	      {"modelId": "...", "name": "...", "description": "..."}
//	    ],
//	    "currentModelId": "..."
//	  }
//	}
//
// Returns nil (not an empty slice) when the payload is missing so
// the caller can distinguish "parsed with no models" (valid but
// empty catalog) from "couldn't find the structure at all".
func parseACPSessionNewModels(raw json.RawMessage) []Model {
	type acpModelInfo struct {
		ModelID      string `json:"modelId"`
		ModelIDSnake string `json:"model_id"`
		Name         string `json:"name"`
		Description  string `json:"description"`
	}
	var resp struct {
		Models struct {
			AvailableModels      []acpModelInfo `json:"availableModels"`
			AvailableModelsSnake []acpModelInfo `json:"available_models"`
			CurrentModelID       string         `json:"currentModelId"`
			CurrentModelIDSnake  string         `json:"current_model_id"`
		} `json:"models"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil
	}
	availableModels := resp.Models.AvailableModels
	if len(availableModels) == 0 && resp.Models.AvailableModelsSnake != nil {
		availableModels = resp.Models.AvailableModelsSnake
	}
	currentModelID := strings.TrimSpace(resp.Models.CurrentModelID)
	if currentModelID == "" {
		currentModelID = strings.TrimSpace(resp.Models.CurrentModelIDSnake)
	}
	models := make([]Model, 0, len(availableModels))
	seen := map[string]bool{}
	for _, m := range availableModels {
		modelID := strings.TrimSpace(m.ModelID)
		if modelID == "" {
			modelID = strings.TrimSpace(m.ModelIDSnake)
		}
		if modelID == "" || seen[modelID] {
			continue
		}
		seen[modelID] = true
		label := acpModelLabel(m.Name, modelID)
		provider := ""
		if idx := strings.Index(modelID, ":"); idx > 0 {
			provider = modelID[:idx]
		}
		models = append(models, Model{
			ID:       modelID,
			Label:    label,
			Provider: provider,
			Default:  modelID == currentModelID,
		})
	}
	return models
}

func acpModelLabel(name, modelID string) string {
	label := strings.TrimSpace(name)
	if label == "" || strings.EqualFold(label, "unknown") {
		return modelID
	}
	return label
}

// discoverAntigravityModels runs `agy models` and returns the catalog the
// installed Antigravity CLI advertises (one display name per line).
//
// Unlike cursor / pi / opencode there is deliberately NO static fallback.
// agy's `--model` takes the exact human display string (e.g.
// "Claude Opus 4.6 (Thinking)") and silently no-ops on any value it doesn't
// recognise — empty output, exit 0 — so a guessed static list would risk
// offering a model the installed CLI can't honour, turning a typo into a
// "successful" empty run. On any discovery failure we return an empty
// catalog instead; agent.model stays unset and agy resolves its own
// default. cachedDiscovery never caches empty results, so this retries on
// the next request once the cause clears.
func discoverAntigravityModels(ctx context.Context, executablePath string) ([]Model, error) {
	if executablePath == "" {
		executablePath = "agy"
	}
	if _, err := exec.LookPath(executablePath); err != nil {
		return nil, nil
	}
	// `agy models` is a local enumeration (no network round-trip), so a
	// short cap is plenty; keep it generous enough to absorb cold starts.
	runCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(runCtx, executablePath, "models")
	hideAgentWindow(cmd)
	out, err := cmd.Output()
	if err != nil && len(out) == 0 {
		return nil, nil
	}
	return parseAntigravityModels(string(out)), nil
}

// parseAntigravityModels turns `agy models` output — one model display name
// per line — into Model entries. The display string IS the value `--model`
// expects, so ID and Label are identical and the daemon ships opts.Model
// verbatim. Blank and duplicate lines are skipped.
func parseAntigravityModels(output string) []Model {
	scanner := bufio.NewScanner(strings.NewReader(output))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var models []Model
	seen := map[string]bool{}
	for scanner.Scan() {
		name := strings.TrimSpace(scanner.Text())
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		models = append(models, Model{
			ID:       name,
			Label:    name,
			Provider: "antigravity",
		})
	}
	return models
}

// discoverCursorModels runs `cursor-agent --list-models` and parses
// the `id - Label` rows. Cursor's catalog changes often and ships
// many variants of the same base model (thinking / fast / max
// suffixes) — static baking would be obsolete within weeks. On any
// failure we fall back to the minimal static catalog so the UI
// stays usable when cursor-agent isn't installed on the daemon host.
func discoverCursorModels(ctx context.Context, executablePath string) ([]Model, error) {
	if executablePath == "" {
		executablePath = "cursor-agent"
	}
	if _, err := exec.LookPath(executablePath); err != nil {
		return cursorStaticModels(), nil
	}
	// 15s to match the other network-backed discovery paths (pi/opencode/ACP);
	// cursor-agent fetches its frequently-changing catalog, so a tight cap can
	// time out and fall back to the minimal static list. See #3729.
	runCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(runCtx, executablePath, "--list-models")
	hideAgentWindow(cmd)
	out, err := cmd.Output()
	if err != nil && len(out) == 0 {
		return cursorStaticModels(), nil
	}
	models := parseCursorModels(string(out))
	if len(models) == 0 {
		return cursorStaticModels(), nil
	}
	return models, nil
}

// parseCursorModels extracts model IDs from `cursor-agent --list-models`.
// Output format (as of cursor-agent 2026.04):
//
//	Available models
//	<blank>
//	auto - Auto
//	composer-2-fast - Composer 2 Fast (current, default)
//	composer-2 - Composer 2
//	…
//
// The model tagged `(default)` is surfaced as Default=true so the
// UI badge points at cursor's own recommendation rather than a
// hard-coded guess from our catalog.
func parseCursorModels(output string) []Model {
	scanner := bufio.NewScanner(strings.NewReader(output))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var models []Model
	seen := map[string]bool{}
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		// Row format: "<id> - <label>". Skip the "Available models" header.
		idx := strings.Index(line, " - ")
		if idx <= 0 {
			continue
		}
		id := strings.TrimSpace(line[:idx])
		label := strings.TrimSpace(line[idx+3:])
		if !isCursorModelIdentifier(id) {
			continue
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		isDefault := strings.Contains(label, "default")
		// Strip the "(current, default)" suffix from the display
		// label since we surface that through the Default flag.
		if paren := strings.Index(label, "("); paren > 0 {
			label = strings.TrimSpace(label[:paren])
		}
		if label == "" {
			label = id
		}
		models = append(models, Model{
			ID:       id,
			Label:    label,
			Provider: "cursor",
			Default:  isDefault,
		})
	}
	return models
}

// discoverOpenclawAgents enumerates the pre-registered OpenClaw agents. Model
// selection lives on those agents, and the current CLI exposes the catalog as
// a JSON array through `agents list --json`.
func discoverOpenclawAgents(ctx context.Context, executablePath string) ([]Model, error) {
	if executablePath == "" {
		executablePath = "openclaw"
	}
	if _, err := exec.LookPath(executablePath); err != nil {
		return []Model{}, nil
	}
	runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(runCtx, executablePath, "agents", "list", "--json")
	hideAgentWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return []Model{}, nil
	}
	models, ok := parseOpenclawAgentsJSON(out)
	if !ok {
		return []Model{}, nil
	}
	return models, nil
}

// openclawAgentEntry is the shape parseOpenclawAgentsJSON expects
// from `openclaw agents list --json`. `id` is the routing key
// passed to `openclaw agent --agent <id>`; `name` is the human
// display label set via `openclaw agents set-identity --name` and
// is only used to enrich the dropdown label. The two are not
// interchangeable — see openclawEntriesToModels for the mapping.
// `model` is optional and only used to enrich the dropdown label.
type openclawAgentEntry struct {
	Name  string `json:"name"`
	ID    string `json:"id"`
	Model string `json:"model"`
}

// parseOpenclawAgentsJSON accepts the current top-level array returned by
// `openclaw agents list --json`.
func parseOpenclawAgentsJSON(raw []byte) ([]Model, bool) {
	var entries []openclawAgentEntry
	if err := json.Unmarshal(raw, &entries); err != nil || entries == nil {
		return nil, false
	}
	return openclawEntriesToModels(entries), true
}

func openclawEntriesToModels(entries []openclawAgentEntry) []Model {
	models := make([]Model, 0, len(entries))
	seen := map[string]bool{}
	for _, e := range entries {
		// Use ID as the model identifier because openclaw resolves
		// --agent by id, not by display name. Names may contain spaces
		// (e.g. "Sub2API OPS") which openclaw's normalizeAgentId would
		// mangle into a different string ("sub2api-ops"), causing a
		// lookup miss and "no parseable output" errors.
		id := strings.TrimSpace(e.ID)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		displayName := strings.TrimSpace(e.Name)
		if displayName == "" {
			displayName = id
		}
		label := displayName
		if e.Model != "" {
			label = displayName + " (" + e.Model + ")"
		}
		models = append(models, Model{ID: id, Label: label, Provider: "openclaw"})
	}
	return models
}

// isCursorModelIdentifier rejects headers and decoration in Cursor's
// human-readable model list.
func isCursorModelIdentifier(s string) bool {
	if s == "" || strings.HasSuffix(s, ":") {
		return false
	}
	first := s[0]
	if (first < 'a' || first > 'z') && (first < 'A' || first > 'Z') {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == '.' || r == '/':
		default:
			return false
		}
	}
	return true
}

// ── CodeBuddy model discovery ──

// discoverCodebuddyModels returns the current CodeBuddy model catalog. An
// operator-provided catalog takes precedence, then ACP discovery, then the
// maintained static catalog.
func discoverCodebuddyModels(ctx context.Context, executablePath string) ([]Model, error) {
	if models := codebuddyModelsFromEnv(); len(models) > 0 {
		return models, nil
	}
	if executablePath == "" {
		executablePath = "codebuddy"
	}
	if _, err := exec.LookPath(executablePath); err != nil {
		return codebuddyStaticModels(), nil
	}
	models, err := discoverCodebuddyACPModels(ctx, executablePath)
	if err == nil && len(models) > 0 {
		return models, nil
	}
	return codebuddyStaticModels(), nil
}

func discoverCodebuddyACPModels(ctx context.Context, executablePath string) ([]Model, error) {
	models, err := discoverACPModels(ctx, executablePath, acpDiscoveryProvider{
		defaultBin:   "codebuddy",
		clientName:   "multica-model-discovery",
		tmpdirPrefix: "multica-codebuddy-discovery-",
		acpArgs:      []string{"--acp"},
	})
	if err != nil || len(models) == 0 {
		return models, err
	}
	for i := range models {
		if models[i].Provider == "" {
			models[i].Provider = codebuddyModelProvider(models[i].ID)
		}
	}
	return models, nil
}

func codebuddyModelsFromEnv() []Model {
	return parseCodebuddyModelList(os.Getenv("MULTICA_CODEBUDDY_MODELS"))
}

func parseCodebuddyModelList(rawList string) []Model {
	raw := strings.FieldsFunc(rawList, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r' || r == '\t' || r == ' '
	})
	var models []Model
	seen := map[string]bool{}
	for _, s := range raw {
		id := strings.TrimSpace(s)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		models = append(models, Model{
			ID:       id,
			Label:    codebuddyModelLabel(id),
			Provider: codebuddyModelProvider(id),
			Default:  len(models) == 0,
		})
	}
	return models
}

// codebuddyModelProvider infers a provider name from a model ID prefix.
func codebuddyModelProvider(id string) string {
	switch {
	case strings.HasPrefix(id, "claude-"):
		return "anthropic"
	case strings.HasPrefix(id, "gemini-"):
		return "google"
	case strings.HasPrefix(id, "gpt-"):
		return "openai"
	case strings.HasPrefix(id, "glm-"):
		return "zhipu"
	case strings.HasPrefix(id, "minimax-"):
		return "minimax"
	case strings.HasPrefix(id, "kimi-"):
		return "kimi"
	case len(id) >= 3 && id[0] == 'h' && id[1] == 'y' && id[2] >= '0' && id[2] <= '9':
		return "hunyuan"
	case strings.HasPrefix(id, "deepseek-"):
		return "deepseek"
	default:
		return ""
	}
}

// codebuddyModelLabel generates a human-readable label from a model ID.
// Capitalizes each dash-separated part; special-cases GPT/GLM to uppercase
// and rewrites the "-ioa" suffix as "IOA".
func codebuddyModelLabel(id string) string {
	parts := strings.Split(id, "-")
	for i, p := range parts {
		if strings.EqualFold(p, "gpt") || strings.EqualFold(p, "glm") {
			parts[i] = strings.ToUpper(p)
		} else if strings.EqualFold(p, "ioa") {
			parts[i] = "IOA"
		} else if len(p) > 0 {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, " ")
}

// codebuddyStaticModels is the fallback catalog when dynamic discovery
// fails (binary missing, parse error, timeout).
func codebuddyStaticModels() []Model {
	return parseCodebuddyModelList(strings.Join([]string{
		"claude-sonnet-4.6",
		"claude-sonnet-4.6-1m",
		"claude-opus-4.8",
		"claude-opus-4.8-1m",
		"claude-opus-4.7",
		"claude-opus-4.7-1m",
		"claude-opus-4.6",
		"claude-opus-4.6-1m",
		"claude-haiku-4.5",
		"gemini-3.1-pro",
		"gemini-3.5-flash",
		"gemini-2.5-pro",
		"gpt-5.5",
		"gpt-5.4",
		"gpt-5.3-codex",
		"gpt-5.1-codex",
		"gpt-5.1-codex-mini",
		"glm-5.2-ioa",
		"glm-5v-turbo-ioa",
		"glm-5.0-ioa",
		"glm-4.7-ioa",
		"minimax-m3-ioa",
		"minimax-m2.7-ioa",
		"minimax-m2.5-ioa",
		"kimi-k2.6-ioa",
		"kimi-k2.7-ioa",
		"hy3-preview-agent-ioa",
		"echo",
		"deepseek-v4-pro-ioa",
		"deepseek-v4-flash-ioa",
		"deepseek-v3-2-volc-ioa",
	}, ","))
}
