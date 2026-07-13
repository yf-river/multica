package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
)

// claudeBackend implements Backend by spawning the Claude Code CLI
// with --output-format stream-json.
type claudeBackend struct {
	cfg Config
}

func (b *claudeBackend) Execute(ctx context.Context, prompt string, opts ExecOptions) (*Session, error) {
	return executeClaudeStreamBackend(ctx, prompt, opts, b.cfg, claudeStreamBackendSpec{
		provider: "claude", defaultExecutable: "claude",
		buildArgs: buildClaudeArgs, rejectAsyncTools: true,
	})
}

func forceClaudeToolInputForeground(input map[string]any) bool {
	if runInBackground, ok := input["run_in_background"].(bool); ok && runInBackground {
		input["run_in_background"] = false
		return true
	}
	return false
}

func claudeToolResultHasAsyncLaunch(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return false
	}
	switch v := value.(type) {
	case map[string]any:
		if claudeMapHasAsyncLaunchStatus(v) {
			return true
		}
		if content, ok := v["content"].([]any); ok {
			return claudeArrayHasAsyncLaunchStatus(content)
		}
	case []any:
		return claudeArrayHasAsyncLaunchStatus(v)
	}
	return false
}

func claudeArrayHasAsyncLaunchStatus(values []any) bool {
	for _, value := range values {
		if item, ok := value.(map[string]any); ok && claudeMapHasAsyncLaunchStatus(item) {
			return true
		}
	}
	return false
}

func claudeMapHasAsyncLaunchStatus(value map[string]any) bool {
	status, ok := value["status"].(string)
	return ok && status == "async_launched"
}

// ── Shared helpers ──

func trySend(ch chan<- Message, msg Message) {
	select {
	case ch <- msg:
	default:
		// Channel full — drop message. Final output is accumulated separately
		// in Result.Output, so only streaming consumers are affected.
	}
}

// claudeBlockedArgs are flags hardcoded by the daemon that must not be
// overridden by user-configured custom_args. Overriding these would break
// the daemon↔Claude communication protocol.
var claudeBlockedArgs = map[string]blockedArgMode{
	"-p":                blockedStandalone, // non-interactive mode
	"--output-format":   blockedWithValue,  // stream-json protocol
	"--input-format":    blockedWithValue,  // stream-json protocol
	"--permission-mode": blockedWithValue,  // bypassPermissions for autonomous operation
	"--mcp-config":      blockedWithValue,  // set by daemon from agent.mcp_config
	"--tools":           blockedWithValue,  // execution-policy owned built-in tool envelope
	"--allowedTools":    blockedWithValue,  // execution-policy owned built-in tool envelope
	"--allowed-tools":   blockedWithValue,  // execution-policy owned built-in tool envelope
	// `--effort` is owned by the per-agent thinking_level picker so a
	// user-supplied custom_arg cannot silently outvote it. The daemon
	// injects --effort only when opts.ThinkingLevel is set; if a user
	// nevertheless writes it in custom_args we drop the duplicate and
	// log a warning rather than letting the CLI receive two conflicting
	// --effort values.
	"--effort": blockedWithValue,
}

func buildClaudeArgs(opts ExecOptions, logger *slog.Logger) []string {
	return buildClaudeStreamArgs(opts, claudeBlockedArgs, logger)
}

func buildClaudeStreamArgs(opts ExecOptions, blockedArgs map[string]blockedArgMode, logger *slog.Logger) []string {
	disallowedTools := append([]string{"AskUserQuestion"}, opts.DisallowedTools...)
	permissionMode := strings.TrimSpace(opts.PermissionMode)
	if permissionMode == "" {
		permissionMode = "bypassPermissions"
	}
	args := []string{
		"-p",
		"--output-format", "stream-json",
		"--input-format", "stream-json",
		"--verbose",
		"--strict-mcp-config",
		"--permission-mode", permissionMode,
		// AskUserQuestion is Claude Code's built-in interactive question tool.
		// The daemon runs Claude in non-interactive stream-json mode and has
		// no UI for the prompt to render in, so a call returns an empty
		// answer and the agent ends up "inferring" silently — the user
		// never sees the question (see GitHub #2588). User-facing
		// clarification belongs in an issue comment instead.
		"--disallowedTools", strings.Join(dedupeToolNames(disallowedTools), ","),
	}
	if len(opts.AllowedBuiltinTools) > 0 {
		args = append(args, "--tools", strings.Join(dedupeToolNames(opts.AllowedBuiltinTools), ","))
	}
	if len(opts.AllowedTools) > 0 {
		args = append(args, "--allowedTools", strings.Join(dedupeToolNames(opts.AllowedTools), ","))
	}
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}
	if opts.ThinkingLevel != "" {
		// Slotted right after --model so the per-session effort runs
		// against the same model selection the args advertise; the CLI
		// itself accepts the flag in any order but this ordering makes
		// the launch line readable in `agent command` logs.
		args = append(args, "--effort", opts.ThinkingLevel)
	}
	if opts.MaxTurns > 0 {
		args = append(args, "--max-turns", fmt.Sprintf("%d", opts.MaxTurns))
	}
	if opts.SystemPrompt != "" {
		args = append(args, "--append-system-prompt", opts.SystemPrompt)
	}
	if opts.ResumeSessionID != "" {
		args = append(args, "--resume", opts.ResumeSessionID)
	}
	args = append(args, filterCustomArgs(opts.ExtraArgs, blockedArgs, logger)...)
	args = append(args, filterCustomArgs(opts.CustomArgs, blockedArgs, logger)...)
	return args
}

func buildClaudeInput(prompt string) ([]byte, error) {
	return buildClaudeStreamInput(prompt, "claude")
}

// resolveSessionID decides which session id to report on the Result. When the
// caller requested --resume but claude emitted a fresh, different session id
// AND the run failed, the resume did not land (claude prints
// "No conversation found with session ID: ..." to stderr, generates a fresh
// session, and exits). Returning "" in that case keeps the daemon's
// retry-with-fresh-session fallback able to trigger, instead of silently
// persisting a brand-new id as if resume had succeeded.
func resolveSessionID(requestedResume, emitted string, failed bool) string {
	if failed && requestedResume != "" && emitted != "" && emitted != requestedResume {
		return ""
	}
	return emitted
}

func buildEnv(extra map[string]string) []string {
	return mergeEnv(os.Environ(), extra)
}

func mergeEnv(base []string, extra map[string]string) []string {
	env := make([]string, 0, len(base)+len(extra))
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		if isFilteredChildEnvKey(key) {
			continue
		}
		env = append(env, entry)
	}
	for k, v := range extra {
		env = append(env, k+"="+v)
	}
	return env
}

// isFilteredChildEnvKey reports whether an inherited env var is an internal
// Claude Code runtime/session marker that must NOT leak into the spawned child
// (otherwise the child mistakes itself for a nested or resumed session, or
// inherits the parent's exec path / transport).
//
// It must NOT strip the user-facing CLAUDE_CODE_* configuration namespace
// (CLAUDE_CODE_GIT_BASH_PATH, CLAUDE_CODE_USE_BEDROCK, CLAUDE_CODE_USE_VERTEX,
// CLAUDE_CODE_MAX_OUTPUT_TOKENS, CLAUDE_CODE_TMPDIR, ...): users set those
// deliberately and the child needs them. Blanket-stripping the whole prefix is
// what broke Windows — CLAUDE_CODE_GIT_BASH_PATH was silently removed, so Claude
// Code could not find bash.exe and exited immediately. Strip internal markers by
// exact name and let every other CLAUDE_CODE_* var through.
//
// The denylist holds only undocumented, per-process runtime markers. Anything in
// the public env-vars reference (https://code.claude.com/docs/en/env-vars) is
// user config and stays out of this list — including CLAUDE_CODE_TMPDIR, a
// documented temp-dir override under which Claude Code creates its own
// per-session subdir, so inheriting it is harmless.
func isFilteredChildEnvKey(key string) bool {
	switch key {
	case "CLAUDECODE", // "1" when running inside Claude Code
		"CLAUDE_CODE_ENTRYPOINT", // entrypoint marker (cli/sdk-cli/...)
		"CLAUDE_CODE_EXECPATH",   // path to the running CLI binary
		"CLAUDE_CODE_SESSION_ID", // per-session identifier
		"CLAUDE_CODE_SSE_PORT":   // IDE-extension transport port
		return true
	}
	// CLAUDECODE_* (no underscore between CLAUDE and CODE) is wholly internal;
	// keep stripping it. The user-facing config namespace is CLAUDE_CODE_*.
	return strings.HasPrefix(key, "CLAUDECODE_")
}

// blockedArgMode specifies whether a blocked arg takes a value or is standalone.
type blockedArgMode int

const (
	blockedWithValue  blockedArgMode = iota // flag takes a value (next arg or =value)
	blockedStandalone                       // flag is boolean, no value
)

func dedupeToolNames(tools []string) []string {
	if len(tools) == 0 {
		return nil
	}
	out := make([]string, 0, len(tools))
	seen := make(map[string]struct{}, len(tools))
	for _, tool := range tools {
		tool = strings.TrimSpace(tool)
		if tool == "" {
			continue
		}
		key := strings.ToLower(tool)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, tool)
	}
	return out
}

// filterCustomArgs removes protocol-critical flags from user-configured custom
// args to prevent breaking daemon↔agent communication. Each backend defines its
// own blocked set (the flags it hardcodes). This is intentionally narrow — we
// only block args that would break the communication protocol, not every
// possible dangerous flag. Workspace members are trusted to configure agents
// sensibly, same as with custom_env.
//
// Shell quoting is stripped from each arg before processing: users commonly
// type custom_args in config fields using shell syntax (e.g.
// --deny-tool='write'). Since the daemon spawns processes directly without a
// shell, those quotes would otherwise be passed literally to the child process,
// which typically rejects them as unrecognised flag values.
func filterCustomArgs(args []string, blocked map[string]blockedArgMode, logger *slog.Logger) []string {
	if len(args) == 0 {
		return args
	}
	filtered := make([]string, 0, len(args))
	skip := false
	for _, raw := range args {
		if skip {
			skip = false
			continue
		}
		arg := unshellQuoteArg(raw)
		flag := arg
		hasInlineValue := false
		if idx := strings.Index(arg, "="); idx > 0 {
			flag = arg[:idx]
			hasInlineValue = true
		}
		mode, isBlocked := blocked[flag]
		if isBlocked {
			logger.Warn("custom_args: blocked protocol-critical flag, skipping", "flag", flag)
			if mode == blockedWithValue && !hasInlineValue {
				// The next arg is the value for this flag — skip it too.
				skip = true
			}
			continue
		}
		filtered = append(filtered, arg)
	}
	return filtered
}

// unshellQuoteArg strips a single layer of shell-style single or double quotes
// from an argument. It handles two forms:
//
//   - --flag='value' or --flag="value" → --flag=value
//   - 'standalone' or "standalone"     → standalone
//
// Only flag-style args (`-x=…`, `--flag=…`) get inline value unquoting. Plain
// assignment syntax like `model="o3"` is left alone because the quotes may be
// semantic for the child process (for example Codex `-c model="o3"`). Only
// matching outer quotes are stripped; no escape processing is done.
func unshellQuoteArg(arg string) string {
	if strings.HasPrefix(arg, "-") {
		if idx := strings.Index(arg, "="); idx > 0 {
			value := arg[idx+1:]
			if unquoted, ok := stripSurroundingQuotes(value); ok {
				return arg[:idx+1] + unquoted
			}
			return arg
		}
	}
	if unquoted, ok := stripSurroundingQuotes(arg); ok {
		return unquoted
	}
	return arg
}

// stripSurroundingQuotes removes a matching outer pair of single or double
// quotes from s and returns (unquoted, true). Returns (s, false) if s does not
// start and end with the same quote character.
func stripSurroundingQuotes(s string) (string, bool) {
	if len(s) >= 2 {
		if (s[0] == '\'' && s[len(s)-1] == '\'') || (s[0] == '"' && s[len(s)-1] == '"') {
			return s[1 : len(s)-1], true
		}
	}
	return s, false
}

// writeMcpConfigToTemp writes raw MCP config JSON to a temporary file and returns
// its path. The caller is responsible for removing the file when done.
func writeMcpConfigToTemp(raw json.RawMessage) (string, error) {
	f, err := os.CreateTemp("", "multica-mcp-*.json")
	if err != nil {
		return "", fmt.Errorf("create mcp config temp file: %w", err)
	}
	if _, err := f.Write(raw); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("write mcp config temp file: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("close mcp config temp file: %w", err)
	}
	return f.Name(), nil
}

func detectCLIVersion(ctx context.Context, execPath string) (string, error) {
	cmd := exec.CommandContext(ctx, execPath, "--version")
	hideAgentWindow(cmd)
	data, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("detect version for %s: %w", execPath, err)
	}
	return extractVersionLine(string(data)), nil
}

// extractVersionLine pulls the version line out of a `<cli> --version` capture,
// discarding leading shell noise. On Windows, npm-installed CLI shims (notably
// gemini's) emit `chcp` output like `Active code page: 65001` before the real
// version reaches stdout, and the raw concatenation was being persisted as the
// runtime version (see #2516).
//
// The heuristic: return the first non-empty line that contains a semver-shaped
// token (matches versionRe). Full version strings like "2.1.5 (Claude Code)"
// or "codex-cli 0.118.0" survive unchanged because the whole matching line is
// returned. If no line carries a semver token, fall back to the trimmed raw
// output so unusual version formats aren't silently dropped to empty.
func extractVersionLine(raw string) string {
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if versionRe.MatchString(line) {
			return line
		}
	}
	return strings.TrimSpace(raw)
}

// logWriter adapts a *slog.Logger to an io.Writer for capturing stderr.
type logWriter struct {
	logger *slog.Logger
	prefix string
}

func newLogWriter(logger *slog.Logger, prefix string) *logWriter {
	return &logWriter{logger: logger, prefix: prefix}
}

func (w *logWriter) Write(p []byte) (int, error) {
	text := strings.TrimSpace(string(p))
	if text != "" {
		w.logger.Debug(w.prefix + text)
	}
	return len(p), nil
}
