package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// openclawNoParseableOutput is the canonical error string surfaced when the
// adapter cannot extract any usable JSON from a run's stdout. The exact
// phrase is depended on by external log-grep / dashboard alerts; do not
// change it without also updating those consumers.
const openclawNoParseableOutput = "openclaw returned no parseable output"

// minOpenclawVersion is the lowest openclaw version that emits its
// --json result on stdout. PR #2101 swapped the adapter from reading
// stderr to stdout; older builds wrote JSON to stderr and now appear
// to silently produce no output. The check in Execute fails fast with
// a hardcoded upgrade hint so users see an actionable message instead
// of "openclaw returned no parseable output".
const minOpenclawVersion = "2026.5.5"

// openclawVersionPattern extracts a three-segment dotted version from
// arbitrary `openclaw --version` output (e.g. "openclaw 2026.5.5",
// "openclaw v2026.5.5 c37871e").
var openclawVersionPattern = regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)`)

// openclawBlockedArgs are flags hardcoded by the daemon that must not be
// overridden by user-configured custom_args.
var openclawBlockedArgs = map[string]blockedArgMode{
	"--local":         blockedStandalone, // local mode for daemon execution
	"--json":          blockedStandalone, // JSON output for daemon communication
	"--session-id":    blockedWithValue,  // managed by daemon for session resumption
	"--message":       blockedWithValue,  // prompt is set by daemon
	"--model":         blockedWithValue,  // openclaw agent does not accept --model; model is bound at registration via `openclaw agents add/update --model`
	"--system-prompt": blockedWithValue,  // openclaw agent does not accept --system-prompt; instructions are injected into --message
}

// openclawBackend implements Backend by spawning `openclaw agent --json` and
// reading its single JSON response from stdout.
type openclawBackend struct {
	cfg Config
}

func (b *openclawBackend) Execute(ctx context.Context, prompt string, opts ExecOptions) (*Session, error) {
	execPath := b.cfg.ExecutablePath
	if execPath == "" {
		execPath = "openclaw"
	}
	if _, err := exec.LookPath(execPath); err != nil {
		return nil, fmt.Errorf("openclaw executable not found at %q: %w", execPath, err)
	}

	if err := checkOpenclawVersion(ctx, execPath); err != nil {
		return nil, err
	}

	sessionID := opts.ResumeSessionID
	if sessionID == "" {
		sessionID = fmt.Sprintf("multica-%d", time.Now().UnixNano())
	}
	args := buildOpenclawArgs(prompt, sessionID, opts, b.cfg.Logger)

	return executeStreamCommand(ctx, opts.Timeout, streamCommandSpec{
		name:       "openclaw",
		executable: execPath,
		args:       args,
		env:        buildEnv(b.cfg.Env),
		cwd:        opts.Cwd,
		waitDelay:  10 * time.Second,
		logger:     b.cfg.Logger,
		model:      opts.Model,
		parse: func(stdout io.Reader, msgCh chan<- Message, _ context.CancelFunc) streamCommandResult {
			scanResult := b.processOutput(stdout, msgCh)

			// Build usage map. Prefer the model openclaw reported in
			// `meta.agentMeta.model` (the actual LLM, e.g. `deepseek-chat`).
			// Fall back to opts.Model — which for openclaw is the agent name
			// passed via `--agent`, not a real model identifier — only when
			// the runtime didn't surface its own model. Last resort is the
			// daemon's `unknown` placeholder.
			var usage map[string]TokenUsage
			u := scanResult.usage
			if u.InputTokens > 0 || u.OutputTokens > 0 || u.CacheReadTokens > 0 || u.CacheWriteTokens > 0 {
				model := scanResult.model
				if model == "" {
					model = opts.Model
				}
				if model == "" {
					model = "unknown"
				}
				usage = map[string]TokenUsage{model: u}
			}

			return streamCommandResult{
				status:    scanResult.status,
				output:    scanResult.output,
				errMsg:    scanResult.errMsg,
				sessionID: scanResult.sessionID,
				usage:     usage,
			}
		},
	})
}

// buildOpenclawArgs assembles the argv for a one-shot `openclaw agent` invocation.
//
// The CLI only accepts --local, --json, --session-id, --timeout, --message (and
// flags like --agent / --channel that users pass through CustomArgs). Notably
// it does NOT accept --model or --system-prompt — model is bound at agent
// registration time via `openclaw agents add/update --model`, and instructions
// must be injected inline into --message because openclaw loads AGENTS.md from
// its own workspace directory, not from cwd.
//
// Routing (issue #3260): `openclaw agent` defaults to Gateway routing; --local
// is the embedded-mode opt-in. The daemon historically forced --local so every
// run executed in-process on the daemon host. When opts.OpenclawMode ==
// "gateway" the daemon drops --local so openclaw dials its configured Gateway
// instead — useful when the daemon host is a lightweight coordinator and the
// real agent work should land on a remote machine running the Gateway.
// --local stays in openclawBlockedArgs so users cannot smuggle it back in via
// custom_args under gateway mode (mode is the single source of truth).
func buildOpenclawArgs(prompt, sessionID string, opts ExecOptions, logger *slog.Logger) []string {
	args := []string{"agent"}
	if opts.OpenclawMode != "gateway" {
		args = append(args, "--local")
	}
	args = append(args, "--json", "--session-id", sessionID)
	if opts.Timeout > 0 {
		args = append(args, "--timeout", fmt.Sprintf("%d", int(opts.Timeout.Seconds())))
	}
	// OpenClaw binds models to pre-registered agents at `openclaw agents
	// add/update --model` time; the daemon selects one at runtime by
	// passing --agent <id>. The model dropdown populates its list from
	// `openclaw agents list`, so opts.Model here is an agent id (see
	// openclawEntriesToModels — the agent's display name lives in the
	// dropdown label, not in opts.Model). Only inject when the user
	// hasn't already set --agent via custom_args — custom_args wins for
	// backward compatibility with existing configs.
	customArgs := filterCustomArgs(opts.CustomArgs, openclawBlockedArgs, logger)
	if opts.Model != "" && !customArgsContains(customArgs, "--agent") {
		args = append(args, "--agent", opts.Model)
	}
	args = append(args, customArgs...)

	if opts.SystemPrompt != "" {
		prompt = opts.SystemPrompt + "\n\n" + prompt
	}
	args = append(args, "--message", prompt)
	return args
}

// customArgsContains reports whether args contains the given flag
// (either as a standalone token "--flag" or in "--flag=value" form).
func customArgsContains(args []string, flag string) bool {
	prefix := flag + "="
	for _, a := range args {
		if a == flag || strings.HasPrefix(a, prefix) {
			return true
		}
	}
	return false
}

// checkOpenclawVersion runs `<execPath> --version` and returns a
// user-facing error when the installed openclaw is older than
// minOpenclawVersion. The returned error becomes the task's failure
// comment, so the message intentionally names the detected version
// and the upgrade command.
func checkOpenclawVersion(ctx context.Context, execPath string) error {
	cmd := exec.CommandContext(ctx, execPath, "--version")
	hideAgentWindow(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("openclaw --version failed: %w", err)
	}
	detected, ok := parseOpenclawVersion(string(out))
	if !ok {
		return fmt.Errorf("could not parse openclaw version from output: %q", strings.TrimSpace(string(out)))
	}
	if compareOpenclawVersion(detected, minOpenclawVersion) < 0 {
		return fmt.Errorf("openclaw %s is below the minimum supported version %s; run `openclaw update` to upgrade and try again", detected, minOpenclawVersion)
	}
	return nil
}

// parseOpenclawVersion extracts the first three-segment dotted version
// from arbitrary `openclaw --version` output. Returns ok=false when no
// match is found.
func parseOpenclawVersion(raw string) (string, bool) {
	m := openclawVersionPattern.FindString(raw)
	if m == "" {
		return "", false
	}
	return m, true
}

// compareOpenclawVersion compares two three-segment dotted versions
// numerically. Returns -1, 0, or +1 like bytes.Compare. Inputs must be
// well-formed (matched by openclawVersionPattern); malformed segments
// compare as zero.
func compareOpenclawVersion(a, b string) int {
	aParts := strings.SplitN(a, ".", 3)
	bParts := strings.SplitN(b, ".", 3)
	for i := 0; i < 3; i++ {
		ai, _ := strconv.Atoi(aParts[i])
		bi, _ := strconv.Atoi(bParts[i])
		if ai < bi {
			return -1
		}
		if ai > bi {
			return 1
		}
	}
	return 0
}

// openclawEventResult is the normalized result of an OpenClaw CLI response.
type openclawEventResult struct {
	status    string
	errMsg    string
	output    string
	sessionID string
	usage     TokenUsage
	// model is the LLM identifier reported by openclaw in its result blob
	// (`meta.agentMeta.model`). Distinct from `opts.Model`,
	// which for the openclaw backend is the openclaw *agent* name passed
	// via `--agent`, not the underlying model.
	model string
}

// processOutput reads the one JSON value emitted by `openclaw agent --json`.
// Local mode emits the result directly; Gateway mode wraps it in `result`.
// OpenClaw reserves stdout for this value and sends diagnostics to stderr, so
// non-JSON stdout is a protocol failure rather than an assistant response.
func (b *openclawBackend) processOutput(r io.Reader, ch chan<- Message) openclawEventResult {
	buf, readErr := io.ReadAll(r)
	if readErr != nil {
		return openclawEventResult{status: "failed", errMsg: fmt.Sprintf("read stdout: %v", readErr)}
	}
	trimmed := strings.TrimSpace(string(buf))
	if trimmed == "" {
		return openclawEventResult{status: "failed", errMsg: openclawNoParseableOutput}
	}

	var response openclawResponse
	if err := json.Unmarshal([]byte(trimmed), &response); err != nil {
		return openclawEventResult{status: "failed", errMsg: openclawNoParseableOutput}
	}

	result := response.openclawResult
	if response.Status != "" || response.Result != nil {
		if response.Status != "ok" {
			errMsg := strings.TrimSpace(response.Summary)
			if errMsg == "" {
				errMsg = fmt.Sprintf("openclaw gateway returned status %q", response.Status)
			}
			return openclawEventResult{status: "failed", errMsg: errMsg}
		}
		if response.Result == nil {
			return buildOpenclawResult(openclawResult{}, ch)
		}
		return buildOpenclawResult(*response.Result, ch)
	}
	if result.Payloads == nil {
		return openclawEventResult{status: "failed", errMsg: openclawNoParseableOutput}
	}
	return buildOpenclawResult(result, ch)
}

func buildOpenclawResult(result openclawResult, ch chan<- Message) openclawEventResult {
	var output strings.Builder
	for _, p := range result.Payloads {
		if p.Text != "" {
			output.WriteString(p.Text)
			trySend(ch, Message{Type: MessageText, Content: p.Text})
		}
	}

	return openclawEventResult{
		status:    "completed",
		output:    output.String(),
		sessionID: result.Meta.AgentMeta.SessionID,
		usage:     result.Meta.AgentMeta.Usage.tokenUsage(),
		model:     strings.TrimSpace(result.Meta.AgentMeta.Model),
	}
}

func (u openclawUsage) tokenUsage() TokenUsage {
	return TokenUsage{
		InputTokens:      u.Input,
		OutputTokens:     u.Output,
		CacheReadTokens:  u.CacheRead,
		CacheWriteTokens: u.CacheWrite,
	}
}

// ── JSON types for `openclaw agent --json` output ──

type openclawResponse struct {
	openclawResult
	Status  string          `json:"status"`
	Summary string          `json:"summary"`
	Result  *openclawResult `json:"result"`
}

type openclawResult struct {
	Payloads []openclawPayload `json:"payloads"`
	Meta     openclawMeta      `json:"meta"`
}

type openclawPayload struct {
	Text string `json:"text"`
}

type openclawMeta struct {
	DurationMs int64             `json:"durationMs"`
	AgentMeta  openclawAgentMeta `json:"agentMeta"`
}

type openclawAgentMeta struct {
	SessionID string        `json:"sessionId"`
	Model     string        `json:"model"`
	Usage     openclawUsage `json:"usage"`
}

type openclawUsage struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cacheRead"`
	CacheWrite int64 `json:"cacheWrite"`
}
