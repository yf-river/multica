package agent

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// codexBlockedArgs are flags hardcoded by the daemon that must not be
// overridden by user-configured custom_args. The mcp_servers config keys
// live in the per-task `$CODEX_HOME/config.toml` (written by
// ensureCodexMcpConfig); user-supplied `-c mcp_servers.…` overrides are
// stripped separately by filterCodexCustomConfigOverrides because they
// share the `-c` flag with legitimate non-MCP overrides like `-c model=…`.
var codexBlockedArgs = map[string]blockedArgMode{
	"--listen": blockedWithValue, // stdio:// transport for daemon communication
}

// codexStderrTailBytes bounds the stderr tail captured for inclusion in
// error messages when codex exits before the JSON-RPC handshake (e.g. the
// user supplied a custom_args flag that the `app-server` subcommand
// rejects). Kept as its own constant so bumping codex independently of
// other agents stays easy if codex starts shipping longer failure traces.
const (
	codexStderrTailBytes                   = 2048
	defaultCodexSemanticInactivityTimeout  = 10 * time.Minute
	defaultCodexFirstTurnNoProgressTimeout = 30 * time.Second
	codexVersionDiagnosticTimeout          = 2 * time.Second
	codexModelCapabilityTimeout            = 3 * time.Second
	// codexGracefulShutdownTimeout bounds how long the lifecycle goroutine
	// waits for codex to exit on its own after stdin is closed, before forcing
	// a context-cancel kill. A clean exit lets codex run its shutdown path and
	// flush buffered telemetry — OTEL batch exporters only force-flush on
	// graceful shutdown, so killing it immediately (the prior behavior) drops
	// the task's spans/metrics/logs.
	codexGracefulShutdownTimeout = 10 * time.Second
)

// CodexSemanticInactivityMarker prefixes timeout errors emitted when Codex
// stops making semantic progress while the process is still alive.
const CodexSemanticInactivityMarker = "codex semantic inactivity timeout"

// CodexFirstTurnNoProgressMarker identifies the app-server failure mode where
// Codex accepts a turn and then never emits any item, completion, or error.
const CodexFirstTurnNoProgressMarker = "codex app-server no progress timeout"

const codexModelCatalogRefreshTimeoutSignal = "failed to refresh available models: timeout waiting for child process to exit"

var errCodexProcessExited = errors.New("codex process exited")

type codexImageGenerationPolicy string

const (
	codexImageGenerationAuto codexImageGenerationPolicy = "auto"
	codexImageGenerationOn   codexImageGenerationPolicy = "on"
	codexImageGenerationOff  codexImageGenerationPolicy = "off"
)

type codexModelCapability struct {
	InputModalities                []string
	ExperimentalSupportedTools     []string
	SupportsImageDetailOriginalSet bool
	SupportsImageDetailOriginal    bool
}

type codexModelCapabilityCacheEntry struct {
	models    map[string]codexModelCapability
	ok        bool
	expiresAt time.Time
}

var (
	codexModelCapabilityCacheMu sync.Mutex
	codexModelCapabilityCache   = map[string]codexModelCapabilityCacheEntry{}
)

type codexTimeoutKind int

const (
	codexTimeoutNone codexTimeoutKind = iota
	codexTimeoutSemanticInactivity
	codexTimeoutFirstTurnNoProgress
)

type codexTimeoutDiagnostic struct {
	Kind         codexTimeoutKind
	Timeout      time.Duration
	LastActivity string
	ThreadID     string
	TurnID       string
	Model        string
	CodexVersion string
}

// codexBackend implements Backend by spawning `codex app-server --listen stdio://`
// and communicating via JSON-RPC 2.0 over stdin/stdout.
type codexBackend struct {
	cfg Config
}

func (b *codexBackend) Execute(ctx context.Context, prompt string, opts ExecOptions) (*Session, error) {
	execPath := b.cfg.ExecutablePath
	if execPath == "" {
		execPath = "codex"
	}
	if _, err := exec.LookPath(execPath); err != nil {
		return nil, fmt.Errorf("codex executable not found at %q: %w", execPath, err)
	}

	timeout := opts.Timeout
	semanticInactivityTimeout := opts.SemanticInactivityTimeout
	if semanticInactivityTimeout == 0 {
		semanticInactivityTimeout = defaultCodexSemanticInactivityTimeout
	}
	runCtx, cancel := runContext(ctx, timeout)

	// Materialise the agent's MCP config into the per-task
	// `$CODEX_HOME/config.toml`. Argv would be the simpler path, but
	// `mcp_servers.<id>.env` is allowed to carry secrets (Codex docs:
	// https://developers.openai.com/codex/mcp#configure-with-configtoml)
	// and our UI already treats mcp_config as a redacted-for-non-admins
	// field. Process argv ends up in OS-level `ps` listings and is also
	// echoed into the daemon's `agent command` log line below, so any
	// inline env-bearing TOML would defeat the redaction. Writing through
	// config.toml at 0o600 keeps the secret values out of argv and logs.
	if codexHome := strings.TrimSpace(b.cfg.Env["CODEX_HOME"]); codexHome != "" {
		if err := ensureCodexMcpConfig(filepath.Join(codexHome, "config.toml"), opts.McpConfig, b.cfg.Logger); err != nil {
			// Fail closed when we can't materialise the managed config.
			// Warning-and-launching would silently fall back to the
			// user's global `~/.codex/config.toml` MCP servers and
			// look indistinguishable from "the saved config was
			// applied", which is exactly the surprise the MCP Tab is
			// supposed to remove.
			cancel()
			return nil, fmt.Errorf("apply codex mcp_config: %w", err)
		}
	} else if hasManagedCodexMcpConfig(opts.McpConfig) {
		// Managed mcp_config saved but no CODEX_HOME to anchor it.
		// Same reasoning as above: silently launching would inherit
		// whatever MCP setup the host user has, which is the wrong
		// shape of failure.
		cancel()
		return nil, fmt.Errorf("codex: mcp_config is set but CODEX_HOME env var is not configured; cannot apply managed MCP")
	}

	disableImageGeneration := shouldDisableCodexImageGeneration(runCtx, execPath, opts, b.cfg.Env, b.cfg.Logger)
	codexArgs := buildCodexArgs(opts, b.cfg.Logger, disableImageGeneration)
	cmd := exec.CommandContext(runCtx, execPath, codexArgs...)
	hideAgentWindow(cmd)
	// Bound the wait after the context is cancelled so a stuck child (or an
	// open pipe held by a grandchild) can't hang cmd.Wait() forever. Matches
	// the other long-lived backends (claude, copilot, cursor, …).
	cmd.WaitDelay = 10 * time.Second
	b.cfg.Logger.Info("agent command", "exec", execPath, "args", codexArgs)
	if opts.Cwd != "" {
		cmd.Dir = opts.Cwd
	}
	cmd.Env = buildEnv(b.cfg.Env)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("codex stdout pipe: %w", err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("codex stdin pipe: %w", err)
	}
	stderrBuf := newStderrTail(newLogWriter(b.cfg.Logger, "[codex:stderr] "), codexStderrTailBytes)
	cmd.Stderr = stderrBuf

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("start codex: %w", err)
	}

	b.cfg.Logger.Info("codex started app-server", "pid", cmd.Process.Pid, "cwd", opts.Cwd)

	msgCh := make(chan Message, 256)
	resCh := make(chan Result, 1)
	semanticActivityCh := make(chan string, 256)

	var outputMu sync.Mutex
	var output strings.Builder

	// turnDone is set before starting the reader goroutine so there is no
	// race between the lifecycle goroutine writing and the reader reading.
	turnDone := make(chan bool, 1) // true = aborted

	c := &codexClient{
		cfg:                  b.cfg,
		stdin:                stdin,
		pending:              make(map[int]*pendingRPC),
		processDone:          make(chan struct{}),
		notificationProtocol: "unknown",
		onMessage: func(msg Message) {
			logCodexAgentMessage(b.cfg.Logger, msg)
			if msg.Type == MessageText {
				outputMu.Lock()
				output.WriteString(msg.Content)
				outputMu.Unlock()
			}
			trySend(msgCh, msg)
			trySendString(semanticActivityCh, describeCodexSemanticActivity(msg))
		},
		onSemanticActivity: func(description string) {
			b.cfg.Logger.Debug("codex semantic activity observed", "activity", description)
			trySendString(semanticActivityCh, description)
		},
		onTurnDone: func(aborted bool) {
			select {
			case turnDone <- aborted:
			default:
			}
		},
	}

	// Start reading stdout in background
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
		if err := scanner.Err(); err != nil {
			c.markProcessExited(fmt.Errorf("%w: %v", errCodexProcessExited, err))
			return
		}
		c.markProcessExited(errCodexProcessExited)
	}()

	// drainAndWait closes stdin so codex shuts down, then joins cmd.Wait().
	// cmd.Wait() is the only Go-stdlib-documented synchronization point for
	// os/exec's internal stderr/stdout copy goroutines — until it returns,
	// stderrBuf may not have observed every byte codex wrote before it
	// exited, and stderrBuf.Tail() can come back empty or truncated. Any
	// code that reads stderrBuf.Tail() must call drainAndWait() first.
	// sync.Once makes it safe to call from both error paths and the deferred
	// cleanup.
	var waitOnce sync.Once
	drainAndWait := func() {
		waitOnce.Do(func() {
			if err := stdin.Close(); err != nil {
				b.cfg.Logger.Debug("codex stdin close failed", "error", err)
			}
			_ = cmd.Wait()
		})
	}

	// Drive the session lifecycle in a goroutine.
	// Shutdown sequence: lifecycle goroutine closes stdin + cancels context →
	// codex process exits → reader goroutine's scanner.Scan() returns false →
	// readerDone closes → lifecycle goroutine collects final output and sends Result.
	go func() {
		defer cancel()
		defer close(msgCh)
		defer close(resCh)
		defer drainAndWait()

		startTime := time.Now()
		finalStatus := "completed"
		var finalError string

		// 1. Initialize handshake
		_, err := c.request(runCtx, "initialize", map[string]any{
			"clientInfo": map[string]any{
				"name":    "multica-agent-sdk",
				"title":   "Multica Agent SDK",
				"version": "0.2.0",
			},
			"capabilities": map[string]any{
				"experimentalApi": true,
			},
		})
		if err != nil {
			drainAndWait() // flush os/exec stderr goroutine before sampling Tail
			finalStatus = "failed"
			finalError = codexFailureError(fmt.Sprintf("codex initialize failed: %v", err), stderrBuf.Tail())
			resCh <- Result{Status: finalStatus, Error: finalError, DurationMs: time.Since(startTime).Milliseconds()}
			return
		}
		c.notify("initialized")

		// 2. Start a new thread, or resume the prior one for this issue. When
		// resume fails (thread GCed on the server, schema drift, etc.) we fall
		// back to a fresh thread so the task still makes progress.
		threadID, resumed, err := c.startOrResumeThread(runCtx, opts, b.cfg.Logger)
		if err != nil {
			drainAndWait() // flush os/exec stderr goroutine before sampling Tail
			finalStatus = "failed"
			finalError = codexFailureError(err.Error(), stderrBuf.Tail())
			resCh <- Result{Status: finalStatus, Error: finalError, DurationMs: time.Since(startTime).Milliseconds()}
			return
		}
		c.threadID = threadID
		if resumed {
			b.cfg.Logger.Info("codex thread resumed", "thread_id", threadID)
		} else {
			b.cfg.Logger.Info("codex thread started", "thread_id", threadID)
		}

		// 3. Send turn and wait for completion
		turnParams := map[string]any{
			"threadId": threadID,
			"input": []map[string]any{
				{"type": "text", "text": prompt},
			},
		}
		// Per-turn reasoning override. Mirrors the per-thread injection in
		// startOrResumeThread; keeping both in sync is enforced by the
		// shared `codexReasoningInjection` fixture in codex_test.go (see
		// MUL-2339 — Trump's constraint that the three injection points
		// must not drift independently).
		applyCodexReasoningEffort(turnParams, opts.ThinkingLevel)
		_, err = c.request(runCtx, "turn/start", turnParams)
		if err != nil {
			drainAndWait() // flush os/exec stderr goroutine before sampling Tail
			finalStatus = "failed"
			finalError = codexFailureError(fmt.Sprintf("codex turn/start failed: %v", err), stderrBuf.Tail())
			resCh <- Result{Status: finalStatus, Error: finalError, DurationMs: time.Since(startTime).Milliseconds()}
			return
		}

		lastSemanticActivity := time.Now()
		lastSemanticActivityDescription := "turn/start"
		semanticTimer := time.NewTimer(semanticInactivityTimeout)
		defer semanticTimer.Stop()

		firstTurnNoProgressTimeout := codexFirstTurnNoProgressTimeout(semanticInactivityTimeout)
		var firstTurnNoProgressTimer *time.Timer
		var firstTurnNoProgressTimerC <-chan time.Time
		firstTurnStarted := false
		firstTurnProgressObserved := false
		stopFirstTurnNoProgressTimer := func() {
			if firstTurnNoProgressTimer == nil {
				return
			}
			stopTimer(firstTurnNoProgressTimer)
			firstTurnNoProgressTimerC = nil
		}
		defer stopFirstTurnNoProgressTimer()

		waitingForTurn := true
		var timeoutDiagnostic codexTimeoutDiagnostic
		var processExitErr error
		finishTurn := func(aborted bool) {
			waitingForTurn = false
			switch {
			case aborted:
				finalStatus = "aborted"
				finalError = "turn was aborted"
			default:
				if errMsg := c.getTurnError(); errMsg != "" {
					finalStatus = "failed"
					finalError = errMsg
				}
			}
		}
		finishRunContextDone := func() {
			waitingForTurn = false
			if runCtx.Err() == context.DeadlineExceeded {
				finalStatus = "timeout"
				finalError = fmt.Sprintf("codex timed out after %s", timeout)
			} else {
				finalStatus = "aborted"
				finalError = "execution cancelled"
			}
		}
		for waitingForTurn {
			select {
			case aborted := <-turnDone:
				finishTurn(aborted)
			case activity := <-semanticActivityCh:
				lastSemanticActivity = time.Now()
				lastSemanticActivityDescription = activity
				resetTimer(semanticTimer, semanticInactivityTimeout)
				if activity == "status:running" && !firstTurnStarted {
					firstTurnStarted = true
					firstTurnNoProgressTimer = time.NewTimer(firstTurnNoProgressTimeout)
					firstTurnNoProgressTimerC = firstTurnNoProgressTimer.C
				} else if firstTurnStarted && !firstTurnProgressObserved && isCodexFirstTurnProgressActivity(activity) {
					firstTurnProgressObserved = true
					stopFirstTurnNoProgressTimer()
				}
			case <-firstTurnNoProgressTimerC:
				waitingForTurn = false
				finalStatus = "timeout"
				timeoutDiagnostic = codexTimeoutDiagnostic{
					Kind:         codexTimeoutFirstTurnNoProgress,
					Timeout:      firstTurnNoProgressTimeout,
					LastActivity: lastSemanticActivityDescription,
					ThreadID:     threadID,
					TurnID:       c.turnID,
					Model:        opts.Model,
				}
				b.cfg.Logger.Warn(CodexFirstTurnNoProgressMarker,
					"pid", cmd.Process.Pid,
					"thread_id", threadID,
					"turn_id", c.turnID,
					"timeout", firstTurnNoProgressTimeout.String(),
					"last_activity", lastSemanticActivityDescription,
				)
			case <-semanticTimer.C:
				waitingForTurn = false
				finalStatus = "timeout"
				timeoutDiagnostic = codexTimeoutDiagnostic{
					Kind:         codexTimeoutSemanticInactivity,
					Timeout:      semanticInactivityTimeout,
					LastActivity: lastSemanticActivityDescription,
					ThreadID:     threadID,
					TurnID:       c.turnID,
					Model:        opts.Model,
				}
				b.cfg.Logger.Warn(CodexSemanticInactivityMarker,
					"pid", cmd.Process.Pid,
					"thread_id", threadID,
					"turn_id", c.turnID,
					"timeout", semanticInactivityTimeout.String(),
					"last_activity", lastSemanticActivityDescription,
					"idle_for", time.Since(lastSemanticActivity).Round(time.Millisecond).String(),
				)
			case <-runCtx.Done():
				finishRunContextDone()
			case <-c.processDone:
				select {
				case aborted := <-turnDone:
					finishTurn(aborted)
				default:
					if runCtx.Err() != nil {
						finishRunContextDone()
					} else {
						waitingForTurn = false
						finalStatus = "failed"
						processExitErr = c.getProcessErr()
						if processExitErr == nil {
							processExitErr = errCodexProcessExited
						}
						finalError = processExitErr.Error()
					}
				}
			}
		}

		duration := time.Since(startTime)
		b.cfg.Logger.Info("codex finished", "pid", cmd.Process.Pid, "status", finalStatus, "duration", duration.Round(time.Millisecond).String())

		// Close stdin to signal the app-server to exit. Prefer letting codex
		// shut down on its own: a clean exit runs codex's shutdown path, which
		// force-flushes its OTEL batch exporters — killing it immediately (via
		// cancel → SIGKILL) drops the task's buffered telemetry. Give it a
		// bounded grace period; only force-cancel if it doesn't exit, so the
		// reader goroutine can never block forever on scanner.Scan().
		if err := stdin.Close(); err != nil {
			b.cfg.Logger.Debug("codex stdin close failed", "error", err)
		}
		select {
		case <-readerDone:
			// codex closed stdout on its own — clean shutdown, telemetry flushed.
		case <-time.After(codexGracefulShutdownTimeout):
			b.cfg.Logger.Warn("codex did not exit after stdin close; forcing shutdown",
				"pid", cmd.Process.Pid,
				"grace", codexGracefulShutdownTimeout.String(),
			)
			cancel()
			<-readerDone
		}
		drainAndWait()

		if processExitErr != nil {
			finalError = codexFailureError(processExitErr.Error(), stderrBuf.Tail())
		}
		if timeoutDiagnostic.Kind != codexTimeoutNone {
			timeoutDiagnostic.CodexVersion = detectCodexVersionForDiagnostics(context.Background(), execPath, cmd.Env, b.cfg.Logger)
			finalError = buildCodexTimeoutDiagnosticError(timeoutDiagnostic, stderrBuf.Tail())
		}

		outputMu.Lock()
		finalOutput := output.String()
		outputMu.Unlock()

		// Build usage map from accumulated codex usage.
		// First check JSON-RPC notifications (often empty for Codex).
		var usageMap map[string]TokenUsage
		c.usageMu.Lock()
		u := c.usage
		c.usageMu.Unlock()

		// Fallback: if no usage from JSON-RPC, scan Codex session JSONL logs.
		// Codex writes token_count events to ~/.codex/sessions/YYYY/MM/DD/*.jsonl.
		if u.InputTokens == 0 && u.OutputTokens == 0 {
			if scanned := scanCodexSessionUsage(startTime, b.cfg.Env["CODEX_HOME"]); scanned != nil {
				u = scanned.usage
				if scanned.model != "" && opts.Model == "" {
					opts.Model = scanned.model
				}
			}
		}

		if u.InputTokens > 0 || u.OutputTokens > 0 || u.CacheReadTokens > 0 || u.CacheWriteTokens > 0 {
			model := opts.Model
			if model == "" {
				model = "unknown"
			}
			usageMap = map[string]TokenUsage{model: u}
		}

		resCh <- Result{
			Status:     finalStatus,
			Output:     finalOutput,
			Error:      finalError,
			SessionID:  threadID,
			DurationMs: duration.Milliseconds(),
			Usage:      usageMap,
		}
	}()

	return &Session{Messages: msgCh, Result: resCh}, nil
}

// startOrResumeThread picks between Codex's thread/resume and thread/start
// based on opts.ResumeSessionID. When a prior thread ID is provided it first
// tries thread/resume; recoverable protocol errors (unknown thread, schema
// mismatch) fall back to thread/start so the task still executes, while
// transport/process failures fail fast because the app-server can no longer
// answer a fresh start request. The returned threadID is what subsequent
// turn/start calls must reference, and resumed indicates whether the prior
// thread was picked up (only useful for logging).
