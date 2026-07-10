package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

func buildCodexArgs(opts ExecOptions, logger *slog.Logger, disableImageGeneration bool) []string {
	args := []string{"app-server", "--listen", "stdio://"}
	extra := filterCustomArgs(opts.ExtraArgs, codexBlockedArgs, logger)
	custom := filterCustomArgs(opts.CustomArgs, codexBlockedArgs, logger)
	// Only claim ownership of the `mcp_servers` namespace when the agent
	// actually has a managed mcp_config in the MCP Tab. Otherwise existing
	// users who configure MCP via `custom_args: ["-c", "mcp_servers.…"]`
	// would silently lose those entries after this PR ships. With managed
	// mcp_config present, daemon-written `$CODEX_HOME/config.toml` is the
	// authoritative source and stray `-c mcp_servers.*` overrides are
	// dropped to keep last-wins from re-shadowing it.
	if hasManagedCodexMcpConfig(opts.McpConfig) {
		extra = filterCodexCustomConfigOverrides(extra, logger)
		custom = filterCodexCustomConfigOverrides(custom, logger)
	}
	args = append(args, extra...)
	args = append(args, custom...)
	if disableImageGeneration {
		args = append(args, "--disable", "image_generation")
	}
	return args
}

func effectiveCodexModelForToolGuard(opts ExecOptions, extraArgs, customArgs []string) string {
	if strings.TrimSpace(opts.Model) != "" {
		return opts.Model
	}
	model := ""
	for _, args := range [][]string{extraArgs, customArgs} {
		if parsed := lastCodexConfigModelArg(args); parsed != "" {
			model = parsed
		}
	}
	return model
}

func lastCodexConfigModelArg(args []string) string {
	model := ""
	for i := 0; i < len(args); i++ {
		arg := strings.TrimSpace(args[i])
		flag, value, hasInlineValue := splitCodexConfigArg(arg)
		if flag != "-c" && flag != "--config" {
			continue
		}
		if !hasInlineValue {
			if i+1 >= len(args) {
				continue
			}
			value = args[i+1]
			i++
		}
		if parsed := parseCodexModelConfigValue(value); parsed != "" {
			model = parsed
		}
	}
	return model
}

func splitCodexConfigArg(arg string) (flag, value string, hasInlineValue bool) {
	if idx := strings.Index(arg, "="); idx > 0 {
		return arg[:idx], arg[idx+1:], true
	}
	return arg, "", false
}

func parseCodexModelConfigValue(value string) string {
	trimmed := strings.TrimSpace(value)
	if !strings.HasPrefix(trimmed, "model") {
		return ""
	}
	rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "model"))
	if rest == "" || rest[0] != '=' {
		return ""
	}
	model := strings.TrimSpace(rest[1:])
	if len(model) >= 2 {
		if (model[0] == '"' && model[len(model)-1] == '"') ||
			(model[0] == '\'' && model[len(model)-1] == '\'') {
			model = model[1 : len(model)-1]
		}
	}
	return strings.TrimSpace(model)
}

func shouldDisableCodexImageGeneration(ctx context.Context, executablePath string, opts ExecOptions, env map[string]string, logger *slog.Logger) bool {
	policy := resolveCodexImageGenerationPolicy(env, logger)
	model := effectiveCodexModelForToolGuard(opts, opts.ExtraArgs, opts.CustomArgs)
	if policy == codexImageGenerationOn {
		return false
	}
	if policy == codexImageGenerationOff {
		return true
	}
	if strings.TrimSpace(model) == "" {
		return true
	}
	models, ok := loadCodexModelCapabilities(ctx, executablePath, env, logger)
	return shouldDisableCodexImageGenerationForCatalog(policy, model, models, ok)
}

func shouldDisableCodexImageGenerationForCatalog(policy codexImageGenerationPolicy, model string, models map[string]codexModelCapability, catalogOK bool) bool {
	if policy == codexImageGenerationOn {
		return false
	}
	if policy == codexImageGenerationOff {
		return true
	}
	model = strings.TrimSpace(model)
	if model == "" || !catalogOK {
		return true
	}
	capability, ok := models[model]
	if !ok {
		return true
	}
	return !codexModelCapabilitySupportsImageGeneration(capability)
}

func resolveCodexImageGenerationPolicy(env map[string]string, logger *slog.Logger) codexImageGenerationPolicy {
	raw := strings.TrimSpace(env["MULTICA_CODEX_IMAGE_GENERATION"])
	if raw == "" {
		raw = strings.TrimSpace(os.Getenv("MULTICA_CODEX_IMAGE_GENERATION"))
	}
	switch strings.ToLower(raw) {
	case "", "auto":
		return codexImageGenerationAuto
	case "on", "true", "1", "enabled", "enable":
		return codexImageGenerationOn
	case "off", "false", "0", "disabled", "disable":
		return codexImageGenerationOff
	default:
		if logger != nil {
			logger.Warn("codex: invalid MULTICA_CODEX_IMAGE_GENERATION value; using auto", "value", raw)
		}
		return codexImageGenerationAuto
	}
}

func codexModelCapabilitySupportsImageGeneration(capability codexModelCapability) bool {
	return containsCaseInsensitive(capability.ExperimentalSupportedTools, "image_generation")
}

func containsCaseInsensitive(values []string, want string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), want) {
			return true
		}
	}
	return false
}

func loadCodexModelCapabilities(ctx context.Context, executablePath string, env map[string]string, logger *slog.Logger) (map[string]codexModelCapability, bool) {
	if executablePath == "" {
		executablePath = "codex"
	}
	version, _ := DetectVersion(ctx, executablePath)
	cacheKey := executablePath + "\x00" + version
	now := time.Now()

	codexModelCapabilityCacheMu.Lock()
	if entry, ok := codexModelCapabilityCache[cacheKey]; ok && now.Before(entry.expiresAt) {
		codexModelCapabilityCacheMu.Unlock()
		return entry.models, entry.ok
	}
	codexModelCapabilityCacheMu.Unlock()

	runCtx, cancel := context.WithTimeout(ctx, codexModelCapabilityTimeout)
	defer cancel()
	raw, err := runCodexDebugModelsLive(runCtx, executablePath, env)
	if err != nil {
		if logger != nil {
			logger.Warn("codex: failed to load live model capability catalog; disabling image_generation by default", "error", err)
		}
		codexModelCapabilityCacheMu.Lock()
		codexModelCapabilityCache[cacheKey] = codexModelCapabilityCacheEntry{models: nil, ok: false, expiresAt: now.Add(modelCacheTTL)}
		codexModelCapabilityCacheMu.Unlock()
		return nil, false
	}
	models := parseCodexModelCapabilities(raw)
	codexModelCapabilityCacheMu.Lock()
	codexModelCapabilityCache[cacheKey] = codexModelCapabilityCacheEntry{models: models, ok: true, expiresAt: now.Add(modelCacheTTL)}
	codexModelCapabilityCacheMu.Unlock()
	return models, true
}

func runCodexDebugModelsLive(ctx context.Context, executablePath string, env map[string]string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, executablePath, "debug", "models")
	hideAgentWindow(cmd)
	cmd.Env = buildEnv(env)
	return cmd.Output()
}

func parseCodexModelCapabilities(raw []byte) map[string]codexModelCapability {
	out := map[string]codexModelCapability{}
	var resp codexDebugModelsResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return out
	}
	for _, m := range resp.Models {
		if m.Slug == "" {
			continue
		}
		capability := codexModelCapability{
			InputModalities:            append([]string(nil), m.InputModalities...),
			ExperimentalSupportedTools: append([]string(nil), m.ExperimentalSupportedTools...),
		}
		if m.SupportsImageDetailOriginal != nil {
			capability.SupportsImageDetailOriginalSet = true
			capability.SupportsImageDetailOriginal = *m.SupportsImageDetailOriginal
		}
		out[m.Slug] = capability
	}
	return out
}

// hasManagedCodexMcpConfig reports whether the agent's mcp_config field is
// "present" in the API three-state sense: a non-null JSON value. Both
// `{}` and `{"mcpServers":{}}` count as present (the admin saved an empty
// managed set — strict mode, no global fallback); only SQL NULL or the
// literal JSON `null` count as absent (CLI default).
func hasManagedCodexMcpConfig(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return false
	}
	if bytes.Equal(trimmed, []byte("null")) {
		return false
	}
	return true
}

// codexManagedMcpConfigKeyRe matches the daemon-managed config namespace
// (`mcp_servers.…`) when it appears as the value of a Codex `-c` /
// `--config` flag. Used by filterCodexCustomConfigOverrides to drop user
// overrides that would otherwise shadow what the MCP Tab writes into
// `$CODEX_HOME/config.toml`.
var codexManagedMcpConfigKeyRe = regexp.MustCompile(`^\s*mcp_servers(?:\s*\.|\s*=|\s*$)`)

// filterCodexCustomConfigOverrides drops `-c mcp_servers.…=` and
// `--config mcp_servers.…=` entries from custom args. Codex's `-c` is
// last-wins (verified against codex-cli 0.132.0), so without this filter a
// user-written `-c mcp_servers.fetch=…` in custom_args would silently
// override whatever the MCP Tab saved into the per-task config.toml. We
// own the `mcp_servers` namespace via the managed block, so user attempts
// to write into it are dropped with a warning rather than allowed to win.
// Other `-c`/`--config` keys (e.g. `-c model="o3"`) pass through unchanged.
func filterCodexCustomConfigOverrides(args []string, logger *slog.Logger) []string {
	if len(args) == 0 {
		return args
	}
	filtered := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		flag := arg
		inlineValue := ""
		hasInlineValue := false
		if idx := strings.Index(arg, "="); idx > 0 {
			flag = arg[:idx]
			inlineValue = arg[idx+1:]
			hasInlineValue = true
		}
		if flag == "-c" || flag == "--config" {
			value := inlineValue
			if !hasInlineValue && i+1 < len(args) {
				value = args[i+1]
			}
			if codexManagedMcpConfigKeyRe.MatchString(value) {
				if logger != nil {
					// Log the key only, never the value — mcp_servers.<name>.env
					// is allowed to carry secrets and the whole point of moving
					// this to config.toml is to keep raw values out of logs/argv.
					key := value
					if eqIdx := strings.Index(value, "="); eqIdx >= 0 {
						key = value[:eqIdx]
					}
					logger.Warn("custom_args: blocked mcp_servers override; daemon manages this via CODEX_HOME/config.toml",
						"flag", flag, "key", strings.TrimSpace(key))
				}
				if !hasInlineValue && i+1 < len(args) {
					i++ // skip the value arg
				}
				continue
			}
		}
		filtered = append(filtered, arg)
	}
	return filtered
}

// Markers delimiting the daemon-managed `[mcp_servers.*]` block in
// `$CODEX_HOME/config.toml`. Match the existing sandbox / multi-agent /
// memory marker pattern so ops can grep all managed blocks consistently.
const (
	multicaCodexMcpBeginMarker = "# BEGIN multica-managed mcp_servers (do not edit; regenerated by daemon)"
	multicaCodexMcpEndMarker   = "# END multica-managed mcp_servers"
)

var codexMcpBlockRe = regexp.MustCompile(
	`(?ms)^` + regexp.QuoteMeta(multicaCodexMcpBeginMarker) +
		`.*?^` + regexp.QuoteMeta(multicaCodexMcpEndMarker) + `\n*`)

// userCodexMcpServersTableHeaderRe matches `[mcp_servers.<name>]` (and its
// quoted-key form `[mcp_servers."<name>"]`) at the start of a line. Used
// to strip user-provided mcp_servers tables from the per-task config when
// the agent has its own mcp_config — mirrors Claude's `--strict-mcp-config`
// model where the daemon's set is authoritative.
var userCodexMcpServersTableHeaderRe = regexp.MustCompile(
	`^\s*\[\s*mcp_servers\s*\.\s*(?:"[^"]*"|[^\]\s]+)\s*\]\s*(?:#.*)?$`)

// ensureCodexMcpConfig writes (or clears) the daemon-managed
// `[mcp_servers.*]` block in `$CODEX_HOME/config.toml`. The block is the
// authoritative source of MCP servers for this run: with mcp_config set
// in the agent UI the daemon also strips any inherited
// `[mcp_servers.*]` tables from the per-task config so the user's global
// `~/.codex/config.toml` doesn't shadow or collide with the managed set.
//
// The file mode is 0o600 because `mcp_servers.<id>.env` values may carry
// secrets (API keys, bearer tokens); the per-task home is owned by the
// daemon's user, so 0o600 keeps secrets out of any world-readable copy
// while still letting the codex child read them.
//
// A malformed mcp_config is returned as an error and the caller decides
// whether to surface or warn — same fail-soft contract the prior argv
// path had.
func ensureCodexMcpConfig(configPath string, mcpConfig json.RawMessage, logger *slog.Logger) error {
	data, err := os.ReadFile(configPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read config.toml: %w", err)
	}
	existing := string(data)

	// Always strip a prior managed block so reruns and clear-config flows
	// converge on a clean state.
	stripped := codexMcpBlockRe.ReplaceAllString(existing, "")

	managed := hasManagedCodexMcpConfig(mcpConfig)
	block, _, renderErr := renderCodexMcpServersBlock(mcpConfig)
	if renderErr != nil {
		return renderErr
	}

	var updated string
	if managed {
		// Agent has a managed MCP set (possibly empty — `{}` /
		// `{"mcpServers":{}}` count as "saved an empty set" in the API's
		// three-state semantics, distinct from nil/null which means
		// "fall back to CLI default"). Strip any user-defined
		// `[mcp_servers.*]` tables inherited from `~/.codex/config.toml`
		// so the managed set is strict — mirrors Claude's
		// `--strict-mcp-config`. Two reasons we cannot mix:
		//   1. TOML rejects redefining the same table; a user table
		//      named `[mcp_servers.fetch]` would crash codex if the
		//      agent also defined `fetch`.
		//   2. An admin saving an explicit list in the MCP Tab would
		//      otherwise see user-global servers silently joined in,
		//      which contradicts the UI affordance.
		stripped = stripCodexUserMcpServerTables(stripped)
		stripped = strings.TrimRight(stripped, "\n")
		// When the managed set is empty we still write the marker
		// block (with no tables between). This pins "managed but
		// empty" on disk so the next run can find and strip the
		// markers, and so the file's intent is grep-able by ops.
		if block == "" {
			block = multicaCodexMcpBeginMarker + "\n" + multicaCodexMcpEndMarker + "\n"
		}
		if stripped == "" {
			updated = block
		} else {
			updated = stripped + "\n\n" + block
		}
	} else {
		// No managed config: just remove any prior managed block and
		// leave inherited user tables alone (CLI default fallback).
		updated = stripped
	}

	if updated == existing {
		return nil
	}
	if err := os.WriteFile(configPath, []byte(updated), 0o600); err != nil {
		return fmt.Errorf("write config.toml: %w", err)
	}
	// os.WriteFile applies the mode only when creating a new file; if the
	// per-task config.toml was already on disk at 0o644 (the default mode
	// used by execenv.copyFile when seeding from ~/.codex/config.toml),
	// the secret-bearing values we just wrote would inherit that wider
	// mode. Chmod unconditionally to keep the secret in the daemon
	// owner's lane regardless of the prior mode.
	if err := os.Chmod(configPath, 0o600); err != nil {
		return fmt.Errorf("chmod config.toml to 0600: %w", err)
	}
	if logger != nil {
		logger.Debug("codex: wrote managed mcp_servers block to config.toml",
			"config_path", configPath, "managed", managed)
	}
	return nil
}

// renderCodexMcpServersBlock renders the agent's mcp_config JSON
// (Claude-style `{"mcpServers": {...}}`) as a TOML block of
// `[mcp_servers.<name>]` tables wrapped in BEGIN/END markers. Returns
// (block, hasServers, err); hasServers=false means the input had no
// servers to render (empty/null mcp_config) and the caller should only
// strip the prior managed block.
//
// Claude-style camelCase keys (`args`, `env`, `command`, `url`) pass
// through verbatim — Codex's config schema happens to use the same
// names today. If they ever diverge, rename here rather than in the UI.
func renderCodexMcpServersBlock(raw json.RawMessage) (string, bool, error) {
	if len(raw) == 0 {
		return "", false, nil
	}
	var parsed struct {
		McpServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", false, fmt.Errorf("parse mcp_config json: %w", err)
	}
	if len(parsed.McpServers) == 0 {
		return "", false, nil
	}

	names := make([]string, 0, len(parsed.McpServers))
	for name := range parsed.McpServers {
		names = append(names, name)
	}
	sort.Strings(names)

	var sb strings.Builder
	sb.WriteString(multicaCodexMcpBeginMarker)
	sb.WriteString("\n")
	for i, name := range names {
		if !isCodexBareTomlKey(name) {
			return "", false, fmt.Errorf("mcp server name %q must be ASCII alphanumeric / _ / - to fit Codex's bare-key requirement", name)
		}
		var serverVal map[string]any
		if err := json.Unmarshal(parsed.McpServers[name], &serverVal); err != nil {
			return "", false, fmt.Errorf("mcp_servers.%s: %w", name, err)
		}
		if serverVal == nil {
			return "", false, fmt.Errorf("mcp_servers.%s must be a JSON object", name)
		}
		if i > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString("[mcp_servers.")
		sb.WriteString(name)
		sb.WriteString("]\n")
		keys := make([]string, 0, len(serverVal))
		for k := range serverVal {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			tomlValue, err := jsonValueToCodexTOMLInline(serverVal[k])
			if err != nil {
				return "", false, fmt.Errorf("mcp_servers.%s.%s: %w", name, k, err)
			}
			sb.WriteString(codexTOMLKey(k))
			sb.WriteString(" = ")
			sb.WriteString(tomlValue)
			sb.WriteString("\n")
		}
	}
	sb.WriteString(multicaCodexMcpEndMarker)
	sb.WriteString("\n")
	return sb.String(), true, nil
}

// stripCodexUserMcpServerTables removes every `[mcp_servers.*]` table
// (header + body lines until the next top-level table header or EOF) from
// a TOML config string. Sub-tables like `[mcp_servers.fetch.env]` count
// as part of the parent table and are dropped along with it.
func stripCodexUserMcpServerTables(content string) string {
	lines := strings.Split(content, "\n")
	out := make([]string, 0, len(lines))
	skipping := false
	for _, line := range lines {
		if userCodexMcpServersTableHeaderRe.MatchString(line) {
			skipping = true
			continue
		}
		if skipping {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "[") {
				// Next table header. If it's still an `mcp_servers.*`
				// table (including a sub-table), keep skipping; otherwise
				// stop and emit this line.
				if userCodexMcpServersTableHeaderRe.MatchString(line) ||
					strings.HasPrefix(trimmed, "[mcp_servers.") ||
					strings.HasPrefix(trimmed, "[ mcp_servers.") {
					continue
				}
				skipping = false
				out = append(out, line)
				continue
			}
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// jsonValueToCodexTOMLInline serialises a JSON value as a TOML inline
// value. Only the subset Codex's `-c` accepts is supported: strings,
// numbers, booleans, arrays, and inline tables. JSON nulls are rejected
// because TOML has no null and silently dropping them would be confusing.
func jsonValueToCodexTOMLInline(v any) (string, error) {
	switch x := v.(type) {
	case nil:
		return "", fmt.Errorf("null is not a valid TOML value")
	case bool:
		if x {
			return "true", nil
		}
		return "false", nil
	case float64:
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10), nil
		}
		return strconv.FormatFloat(x, 'f', -1, 64), nil
	case string:
		return codexTOMLBasicString(x), nil
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			p, err := jsonValueToCodexTOMLInline(e)
			if err != nil {
				return "", err
			}
			parts[i] = p
		}
		return "[" + strings.Join(parts, ", ") + "]", nil
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, len(keys))
		for i, k := range keys {
			p, err := jsonValueToCodexTOMLInline(x[k])
			if err != nil {
				return "", err
			}
			parts[i] = codexTOMLKey(k) + " = " + p
		}
		return "{ " + strings.Join(parts, ", ") + " }", nil
	default:
		return "", fmt.Errorf("unsupported value type %T", v)
	}
}

func codexTOMLBasicString(s string) string {
	var sb strings.Builder
	sb.Grow(len(s) + 2)
	sb.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\':
			sb.WriteString(`\\`)
		case '"':
			sb.WriteString(`\"`)
		case '\b':
			sb.WriteString(`\b`)
		case '\t':
			sb.WriteString(`\t`)
		case '\n':
			sb.WriteString(`\n`)
		case '\f':
			sb.WriteString(`\f`)
		case '\r':
			sb.WriteString(`\r`)
		default:
			if r < 0x20 || r == 0x7f {
				sb.WriteString(fmt.Sprintf(`\u%04x`, r))
			} else {
				sb.WriteRune(r)
			}
		}
	}
	sb.WriteByte('"')
	return sb.String()
}

func codexTOMLKey(s string) string {
	if isCodexBareTomlKey(s) {
		return s
	}
	return codexTOMLBasicString(s)
}

func isCodexBareTomlKey(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_' || r == '-':
		default:
			return false
		}
	}
	return true
}

