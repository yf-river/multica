package agent

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// hermesBlockedArgs are flags hardcoded by the daemon that must not be
// overridden by user-configured custom_args. `acp` is the protocol
// subcommand that drives the ACP JSON-RPC transport; overriding it
// would break the daemon↔Hermes communication contract.
var hermesBlockedArgs = map[string]blockedArgMode{
	"acp": blockedStandalone,
}

// hermesBackend implements Backend by spawning `hermes acp` and communicating
// via the ACP (Agent Communication Protocol) JSON-RPC 2.0 over stdin/stdout.
// This is the same pattern as Codex but with the ACP protocol instead of
// the Codex-specific JSON-RPC methods.
type hermesBackend struct {
	cfg Config
}

func (b *hermesBackend) Execute(ctx context.Context, prompt string, opts ExecOptions) (*Session, error) {
	execPath := b.cfg.ExecutablePath
	if execPath == "" {
		execPath = "hermes"
	}
	if _, err := exec.LookPath(execPath); err != nil {
		return nil, fmt.Errorf("hermes executable not found at %q: %w", execPath, err)
	}

	// Translate the agent's mcp_config (Claude-style object of objects)
	// into the array shape ACP `session/new` expects. Fail closed on
	// malformed JSON so the launch surfaces the real error instead of
	// silently dropping all MCP servers.
	mcpServers, err := buildACPMcpServers(opts.McpConfig, b.cfg.Logger)
	if err != nil {
		return nil, fmt.Errorf("hermes: invalid mcp_config: %w", err)
	}

	timeout := opts.Timeout
	runCtx, cancel := runContext(ctx, timeout)

	hermesArgs := append([]string{"acp"}, filterCustomArgs(opts.CustomArgs, hermesBlockedArgs, b.cfg.Logger)...)
	cmd := exec.CommandContext(runCtx, execPath, hermesArgs...)
	hideAgentWindow(cmd)
	b.cfg.Logger.Info("agent command", "exec", execPath, "args", hermesArgs)
	agentsMDPresent := false
	if opts.Cwd != "" {
		cmd.Dir = opts.Cwd
		if _, err := os.Stat(filepath.Join(opts.Cwd, "AGENTS.md")); err == nil {
			agentsMDPresent = true
		}
	}
	b.cfg.Logger.Info("hermes acp starting", "cwd", opts.Cwd, "agents_md_present", agentsMDPresent)
	if opts.SystemPrompt != "" {
		b.cfg.Logger.Debug("hermes ignoring ExecOptions.SystemPrompt; using cwd-scoped context files", "cwd", opts.Cwd)
	}

	env := buildEnv(b.cfg.Env)
	// Enable yolo mode so Hermes auto-approves all tool executions.
	env = append(env, "HERMES_YOLO_MODE=1")
	cmd.Env = env

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("hermes stdout pipe: %w", err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("hermes stdin pipe: %w", err)
	}
	// Forward stderr to the daemon log *and* sniff provider-level
	// errors out of it so we can surface them in the task result.
	// Hermes' session/prompt still reports stopReason=end_turn when
	// the underlying HTTP call to the LLM returns 4xx/5xx, so
	// without this we'd report a misleading "empty output" and hide
	// the real cause (wrong model for the current provider, bad
	// credentials, rate limit, …) in the daemon log.
	//
	// We use StderrPipe + an explicit copier goroutine instead of
	// `cmd.Stderr = io.MultiWriter(...)` so we have a join point
	// (`stderrDone`) before the failure-promotion decision. With the
	// MultiWriter form, exec's internal copy goroutine is only
	// joined by `cmd.Wait()`, which runs in the deferred cleanup —
	// after `promoteACPResultOnProviderError` already consulted the
	// sniffer. That race lost the 429 / usage-limit message under
	// CI load and surfaced as a flaky test
	// (TestHermesBackendPromotesProviderErrorWithNonEmptyOutput).
	providerErr := newACPProviderErrorSniffer("hermes")
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("hermes stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("start hermes: %w", err)
	}

	stderrSink := io.MultiWriter(newLogWriter(b.cfg.Logger, "[hermes:stderr] "), providerErr)
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		_, _ = io.Copy(stderrSink, stderr)
	}()

	b.cfg.Logger.Info("hermes acp started", "pid", cmd.Process.Pid, "cwd", opts.Cwd)

	msgCh := make(chan Message, 256)
	resCh := make(chan Result, 1)

	var outputMu sync.Mutex
	var output strings.Builder
	// streamingCurrentTurn gates all session updates so that history
	// replay (Hermes sends full prior-turn transcripts on session/resume,
	// and may flush queued chunks before our session/prompt response
	// streams) is dropped instead of duplicating the previous answer
	// into output. We flip it to true only after session/prompt is sent.
	var streamingCurrentTurn atomic.Bool

	promptDone := make(chan hermesPromptResult, 1)

	c := &hermesClient{
		cfg:          b.cfg,
		stdin:        stdin,
		pending:      make(map[int]*pendingRPC),
		pendingTools: make(map[string]*pendingToolCall),
		acceptNotification: func(string) bool {
			return streamingCurrentTurn.Load()
		},
		onMessage: func(msg Message) {
			if !streamingCurrentTurn.Load() {
				return
			}
			if msg.Type == MessageText {
				outputMu.Lock()
				output.WriteString(msg.Content)
				outputMu.Unlock()
			}
			trySend(msgCh, msg)
		},
		onPromptDone: func(result hermesPromptResult) {
			if !streamingCurrentTurn.Load() {
				return
			}
			select {
			case promptDone <- result:
			default:
			}
		},
	}

	// Start reading stdout in background.
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			c.handleLine(line)
		}
		c.closeAllPending(fmt.Errorf("hermes process exited"))
	}()

	// Drive the ACP session lifecycle in a goroutine.
	go func() {
		defer cancel()
		defer close(msgCh)
		defer close(resCh)
		defer func() {
			stdin.Close()
			_ = cmd.Wait()
		}()

		startTime := time.Now()
		finalStatus := "completed"
		var finalError string
		var sessionID string
		effectiveModel := strings.TrimSpace(opts.Model)

		// 1. Initialize handshake.
		initResult, err := c.request(runCtx, "initialize", map[string]any{
			"protocolVersion": 1,
			"clientInfo": map[string]any{
				"name":    "multica-agent-sdk",
				"version": "0.2.0",
			},
			"clientCapabilities": map[string]any{},
		})
		if err != nil {
			finalStatus = "failed"
			finalError = fmt.Sprintf("hermes initialize failed: %v", err)
			resCh <- Result{Status: finalStatus, Error: finalError, DurationMs: time.Since(startTime).Milliseconds()}
			return
		}

		// Drop MCP entries whose remote transport the runtime didn't
		// advertise. ACP requires the client to honour
		// agentCapabilities.mcpCapabilities; sending an http/sse entry to
		// a runtime that says it only supports stdio reliably rejects the
		// whole session/new request.
		mcpServers = filterACPMcpServersByCapability(mcpServers, extractACPMcpCapabilities(initResult), "hermes", b.cfg.Logger)

		// 2. Create or resume a session.
		cwd := opts.Cwd
		if cwd == "" {
			cwd = "."
		}

		if opts.ResumeSessionID != "" {
			// Per ACP Session Setup, session/resume accepts mcpServers and
			// the runtime re-connects them as part of the resume. Without
			// this, a resumed Hermes task lost access to MCP tools that a
			// fresh task on the same agent would have.
			result, err := c.request(runCtx, "session/resume", map[string]any{
				"cwd":        cwd,
				"sessionId":  opts.ResumeSessionID,
				"mcpServers": mcpServers,
			})
			if err != nil {
				finalStatus = "failed"
				finalError = fmt.Sprintf("hermes session/resume failed: %v", err)
				resCh <- Result{Status: finalStatus, Error: finalError, DurationMs: time.Since(startTime).Milliseconds()}
				return
			}
			var changed bool
			sessionID, changed = resolveResumedSessionID(opts.ResumeSessionID, result)
			if changed {
				b.cfg.Logger.Warn("agent returned a different session id on resume — original was likely lost; continuing with the new id",
					"backend", "hermes",
					"requested", opts.ResumeSessionID,
					"actual", sessionID,
				)
			}
			if effectiveModel == "" {
				effectiveModel = extractACPCurrentModelID(result)
			}
		} else {
			result, err := c.request(runCtx, "session/new", buildHermesSessionParams(cwd, opts.Model, mcpServers))
			if err != nil {
				finalStatus = "failed"
				finalError = fmt.Sprintf("hermes session/new failed: %v", err)
				resCh <- Result{Status: finalStatus, Error: finalError, DurationMs: time.Since(startTime).Milliseconds()}
				return
			}
			sessionID = extractACPSessionID(result)
			if sessionID == "" {
				finalStatus = "failed"
				finalError = "hermes session/new returned no session ID"
				resCh <- Result{Status: finalStatus, Error: finalError, DurationMs: time.Since(startTime).Milliseconds()}
				return
			}
			if effectiveModel == "" {
				effectiveModel = extractACPCurrentModelID(result)
			}
		}

		c.sessionID = sessionID
		b.cfg.Logger.Info("hermes session created", "session_id", sessionID)

		// 3. If the caller picked a model (via agent.model from the
		// UI dropdown), ask hermes to switch the session to it
		// before we send any prompt. Hermes' _build_model_state
		// exposes modelId as `provider:model` — we pass that
		// through verbatim. This MUST fail the task on error:
		// if we silently fell back to hermes' default model the
		// user would think their pick was honoured while the
		// task actually ran on something else.
		if opts.Model != "" {
			if _, err := c.request(runCtx, "session/set_model", map[string]any{
				"sessionId": sessionID,
				"modelId":   opts.Model,
			}); err != nil {
				b.cfg.Logger.Warn("hermes set_session_model failed", "error", err, "requested_model", opts.Model)
				finalStatus = "failed"
				finalError = fmt.Sprintf("hermes could not switch to model %q: %v", opts.Model, err)
				if opts.ResumeSessionID != "" && isACPSessionNotFound(err) {
					// On a resumed session with a model override, the dead
					// session surfaces here instead of at session/prompt.
					// Same fix as the prompt path below: clear the id so
					// the daemon's resume-failure fallback retries fresh.
					b.cfg.Logger.Warn("resumed session not found at set_model time; clearing session id so the daemon retries fresh",
						"backend", "hermes",
						"session_id", sessionID,
					)
					sessionID = ""
				}
				resCh <- Result{
					Status:     finalStatus,
					Error:      finalError,
					DurationMs: time.Since(startTime).Milliseconds(),
					SessionID:  sessionID,
				}
				return
			}
			b.cfg.Logger.Info("hermes session model set", "model", opts.Model)
		}

		// 4. Send the prompt and wait for PromptResponse.
		//
		// Do NOT prepend opts.SystemPrompt here. Hermes ACP loads project/context
		// files from cwd (AGENTS.md, .agent_context, etc.) itself; duplicating the
		// full runtime brief in the user prompt makes the request much larger and
		// has triggered upstream safety filters on otherwise ordinary tasks.
		// Flip the gate
		// just before the request so any history replay flushed during
		// initialize / session setup stays dropped, but every notification
		// belonging to this turn is processed.
		streamingCurrentTurn.Store(true)
		_, err = c.request(runCtx, "session/prompt", map[string]any{
			"sessionId": sessionID,
			"prompt": []map[string]any{
				{"type": "text", "text": prompt},
			},
		})
		if err != nil {
			// If the request itself failed (not just context cancelled),
			// check if the context was cancelled/timed out.
			if runCtx.Err() == context.DeadlineExceeded {
				finalStatus = "timeout"
				finalError = fmt.Sprintf("hermes timed out after %s", timeout)
			} else if runCtx.Err() == context.Canceled {
				finalStatus = "aborted"
				finalError = "execution cancelled"
			} else {
				finalStatus = "failed"
				finalError = fmt.Sprintf("hermes session/prompt failed: %v", err)
				if opts.ResumeSessionID != "" && isACPSessionNotFound(err) {
					// The agent no longer knows the session we resumed.
					// Hermes echoes the requested id back from
					// session/resume even when the session is gone, so
					// resolveResumedSessionID can't catch this — it only
					// surfaces here, at prompt time. Return an empty
					// SessionID so the daemon's resume-failure fallback
					// retries with a fresh session and stores the
					// replacement id; keeping the stale id makes every
					// future dispatch on this (agent, issue) fail the
					// same way.
					b.cfg.Logger.Warn("resumed session not found at prompt time; clearing session id so the daemon retries fresh",
						"backend", "hermes",
						"session_id", sessionID,
					)
					sessionID = ""
				}
			}
		} else {
			// The prompt completed. Check if we got a promptDone result
			// from the response parsing.
			select {
			case pr := <-promptDone:
				if pr.stopReason == "cancelled" {
					finalStatus = "aborted"
					finalError = "hermes cancelled the prompt"
				}
				// Merge usage from the PromptResponse.
				c.usageMu.Lock()
				c.usage.InputTokens += pr.usage.InputTokens
				c.usage.OutputTokens += pr.usage.OutputTokens
				c.usage.CacheReadTokens += pr.usage.CacheReadTokens
				c.usageMu.Unlock()
			default:
			}
		}

		resCh <- finishACPBackendResult(acpBackendResultParams{
			Provider:    "hermes",
			Logger:      b.cfg.Logger,
			PID:         cmd.Process.Pid,
			StartTime:   startTime,
			Status:      finalStatus,
			Error:       finalError,
			SessionID:   sessionID,
			Model:       effectiveModel,
			Stdin:       stdin,
			Cancel:      cancel,
			ReaderDone:  readerDone,
			StderrDone:  stderrDone,
			OutputMu:    &outputMu,
			Output:      &output,
			Client:      c,
			ProviderErr: providerErr,
		})
	}()

	return &Session{Messages: msgCh, Result: resCh}, nil
}

// ── hermesClient: ACP JSON-RPC 2.0 transport ──

