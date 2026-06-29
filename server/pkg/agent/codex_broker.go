package agent

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// CodexBrokerBackend keeps one Codex app-server process alive for a runtime
// and runs turns through it serially. Task-specific credentials must not be
// present in cfg.Env; the daemon provides them through cwd-local task context.
type CodexBrokerBackend struct {
	cfgMu sync.RWMutex
	cfg   Config

	sem chan struct{}

	procMu sync.Mutex
	proc   *codexBrokerProcess
	key    string
}

func NewCodexBrokerBackend(cfg Config) *CodexBrokerBackend {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &CodexBrokerBackend{cfg: cfg, sem: make(chan struct{}, 1)}
}

func (b *CodexBrokerBackend) UpdateConfig(cfg Config) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	b.cfgMu.Lock()
	b.cfg = cfg
	b.cfgMu.Unlock()
}

func (b *CodexBrokerBackend) Execute(ctx context.Context, prompt string, opts ExecOptions) (*Session, error) {
	msgCh := make(chan Message, 256)
	resCh := make(chan Result, 1)
	go func() {
		defer close(msgCh)
		defer close(resCh)
		select {
		case b.sem <- struct{}{}:
			defer func() { <-b.sem }()
		case <-ctx.Done():
			resCh <- Result{Status: "cancelled", Error: "execution cancelled before codex broker slot was available"}
			return
		}
		result := b.executeSerial(ctx, prompt, opts, msgCh)
		resCh <- result
	}()
	return &Session{Messages: msgCh, Result: resCh}, nil
}

func (b *CodexBrokerBackend) executeSerial(ctx context.Context, prompt string, opts ExecOptions, msgCh chan<- Message) Result {
	cfg := b.snapshotConfig()
	startTime := time.Now()
	timeout := opts.Timeout
	semanticInactivityTimeout := opts.SemanticInactivityTimeout
	if semanticInactivityTimeout == 0 {
		semanticInactivityTimeout = defaultCodexSemanticInactivityTimeout
	}
	runCtx, cancel := runContext(ctx, timeout)
	defer cancel()

	proc, err := b.ensureProcess(runCtx, cfg, opts)
	if err != nil {
		return Result{Status: "failed", Error: err.Error(), DurationMs: time.Since(startTime).Milliseconds()}
	}

	result, poison := b.runTurn(runCtx, proc, prompt, opts, msgCh, startTime, semanticInactivityTimeout, timeout)
	if poison {
		b.restartProcess("turn failed or timed out")
	}
	return result
}

func (b *CodexBrokerBackend) snapshotConfig() Config {
	b.cfgMu.RLock()
	defer b.cfgMu.RUnlock()
	cfg := b.cfg
	cfg.Env = cloneEnvMap(cfg.Env)
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return cfg
}

func (b *CodexBrokerBackend) ensureProcess(ctx context.Context, cfg Config, opts ExecOptions) (*codexBrokerProcess, error) {
	execPath := cfg.ExecutablePath
	if execPath == "" {
		execPath = "codex"
	}
	if _, err := exec.LookPath(execPath); err != nil {
		return nil, fmt.Errorf("codex executable not found at %q: %w", execPath, err)
	}
	if codexHome := strings.TrimSpace(cfg.Env["CODEX_HOME"]); codexHome != "" {
		if err := ensureCodexMcpConfig(filepath.Join(codexHome, "config.toml"), opts.McpConfig, cfg.Logger); err != nil {
			return nil, fmt.Errorf("apply codex mcp_config: %w", err)
		}
	} else if hasManagedCodexMcpConfig(opts.McpConfig) {
		return nil, fmt.Errorf("codex: mcp_config is set but CODEX_HOME env var is not configured; cannot apply managed MCP")
	}
	disableImageGeneration := shouldDisableCodexImageGeneration(ctx, execPath, opts, cfg.Env, cfg.Logger)
	args := buildCodexArgs(opts, cfg.Logger, disableImageGeneration)
	key := codexBrokerKey(execPath, args, cfg.Env, opts.McpConfig)

	b.procMu.Lock()
	defer b.procMu.Unlock()
	if b.proc != nil && b.key == key && b.proc.alive() {
		cfg.Logger.Info("codex broker reused app-server", "pid", b.proc.pid())
		return b.proc, nil
	}
	if b.proc != nil {
		b.proc.close("broker config changed")
		b.proc = nil
	}
	proc, err := startCodexBrokerProcess(ctx, cfg, execPath, args)
	if err != nil {
		return nil, err
	}
	b.proc = proc
	b.key = key
	return proc, nil
}

func (b *CodexBrokerBackend) restartProcess(reason string) {
	b.procMu.Lock()
	defer b.procMu.Unlock()
	if b.proc != nil {
		b.proc.close(reason)
		b.proc = nil
	}
	b.key = ""
}

func (b *CodexBrokerBackend) runTurn(ctx context.Context, proc *codexBrokerProcess, prompt string, opts ExecOptions, msgCh chan<- Message, startTime time.Time, semanticInactivityTimeout, timeout time.Duration) (Result, bool) {
	cfg := proc.cfg
	semanticActivityCh := make(chan string, 256)
	turnDone := make(chan bool, 1)
	var outputMu sync.Mutex
	var output strings.Builder

	c := proc.client
	c.beginBrokerTurn(
		func(msg Message) {
			logCodexAgentMessage(cfg.Logger, msg)
			if msg.Type == MessageText {
				outputMu.Lock()
				output.WriteString(msg.Content)
				outputMu.Unlock()
			}
			trySend(msgCh, msg)
			trySendString(semanticActivityCh, describeCodexSemanticActivity(msg))
		},
		func(description string) {
			cfg.Logger.Debug("codex semantic activity observed", "activity", description)
			trySendString(semanticActivityCh, description)
		},
		func(aborted bool) {
			select {
			case turnDone <- aborted:
			default:
			}
		},
	)
	defer c.endBrokerTurn()

	finalStatus := "completed"
	var finalError string
	var timeoutDiagnostic codexTimeoutDiagnostic
	var processExitErr error
	poison := false

	threadID, resumed, err := c.startOrResumeThread(ctx, opts, cfg.Logger)
	if err != nil {
		poison = isCodexTransportError(err)
		return Result{Status: "failed", Error: codexFailureError(err.Error(), proc.stderrTail()), DurationMs: time.Since(startTime).Milliseconds()}, poison
	}
	c.threadID = threadID
	if resumed {
		cfg.Logger.Info("codex broker thread resumed", "thread_id", threadID, "pid", proc.pid())
	} else {
		cfg.Logger.Info("codex broker thread started", "thread_id", threadID, "pid", proc.pid())
	}

	turnParams := map[string]any{
		"threadId": threadID,
		"input": []map[string]any{
			{"type": "text", "text": prompt},
		},
	}
	applyCodexReasoningEffort(turnParams, opts.ThinkingLevel)
	if _, err := c.request(ctx, "turn/start", turnParams); err != nil {
		poison = true
		return Result{Status: "failed", Error: codexFailureError(fmt.Sprintf("codex turn/start failed: %v", err), proc.stderrTail()), DurationMs: time.Since(startTime).Milliseconds()}, poison
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
				poison = true
			}
		}
	}
	finishRunContextDone := func() {
		waitingForTurn = false
		poison = true
		if ctx.Err() == context.DeadlineExceeded {
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
			poison = true
			finalStatus = "timeout"
			timeoutDiagnostic = codexTimeoutDiagnostic{Kind: codexTimeoutFirstTurnNoProgress, Timeout: firstTurnNoProgressTimeout, LastActivity: lastSemanticActivityDescription, ThreadID: threadID, TurnID: c.turnID, Model: opts.Model}
			cfg.Logger.Warn(CodexFirstTurnNoProgressMarker, "pid", proc.pid(), "thread_id", threadID, "turn_id", c.turnID, "timeout", firstTurnNoProgressTimeout.String(), "last_activity", lastSemanticActivityDescription)
		case <-semanticTimer.C:
			waitingForTurn = false
			poison = true
			finalStatus = "timeout"
			timeoutDiagnostic = codexTimeoutDiagnostic{Kind: codexTimeoutSemanticInactivity, Timeout: semanticInactivityTimeout, LastActivity: lastSemanticActivityDescription, ThreadID: threadID, TurnID: c.turnID, Model: opts.Model}
			cfg.Logger.Warn(CodexSemanticInactivityMarker, "pid", proc.pid(), "thread_id", threadID, "turn_id", c.turnID, "timeout", semanticInactivityTimeout.String(), "last_activity", lastSemanticActivityDescription, "idle_for", time.Since(lastSemanticActivity).Round(time.Millisecond).String())
		case <-ctx.Done():
			finishRunContextDone()
		case <-c.processDone:
			select {
			case aborted := <-turnDone:
				finishTurn(aborted)
			default:
				if ctx.Err() != nil {
					finishRunContextDone()
				} else {
					waitingForTurn = false
					poison = true
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

	if processExitErr != nil {
		finalError = codexFailureError(processExitErr.Error(), proc.stderrTail())
	}
	if timeoutDiagnostic.Kind != codexTimeoutNone {
		timeoutDiagnostic.CodexVersion = detectCodexVersionForDiagnostics(context.Background(), proc.execPath, proc.env, cfg.Logger)
		finalError = buildCodexTimeoutDiagnosticError(timeoutDiagnostic, proc.stderrTail())
	}

	outputMu.Lock()
	finalOutput := output.String()
	outputMu.Unlock()

	var usageMap map[string]TokenUsage
	c.usageMu.Lock()
	u := c.usage
	c.usageMu.Unlock()
	if u.InputTokens == 0 && u.OutputTokens == 0 {
		if scanned := scanCodexSessionUsage(startTime, cfg.Env["CODEX_HOME"]); scanned != nil {
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
	duration := time.Since(startTime)
	cfg.Logger.Info("codex broker turn finished", "pid", proc.pid(), "thread_id", threadID, "status", finalStatus, "duration", duration.Round(time.Millisecond).String(), "poison", poison)
	return Result{Status: finalStatus, Output: finalOutput, Error: finalError, SessionID: threadID, DurationMs: duration.Milliseconds(), Usage: usageMap}, poison
}

type codexBrokerProcess struct {
	cfg      Config
	execPath string
	env      []string
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	stderr   *stderrTail
	client   *codexClient

	readerDone chan struct{}
	waitOnce   sync.Once
}

func startCodexBrokerProcess(ctx context.Context, cfg Config, execPath string, args []string) (*codexBrokerProcess, error) {
	cmd := exec.Command(execPath, args...)
	hideAgentWindow(cmd)
	cmd.WaitDelay = 10 * time.Second
	cmd.Env = buildEnv(cfg.Env)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("codex stdout pipe: %w", err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("codex stdin pipe: %w", err)
	}
	stderrBuf := newStderrTail(newLogWriter(cfg.Logger, "[codex:stderr] "), codexStderrTailBytes)
	cmd.Stderr = stderrBuf
	cfg.Logger.Info("agent command", "exec", execPath, "args", args, "broker", true)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start codex broker: %w", err)
	}
	proc := &codexBrokerProcess{
		cfg:        cfg,
		execPath:   execPath,
		env:        cmd.Env,
		cmd:        cmd,
		stdin:      stdin,
		stderr:     stderrBuf,
		readerDone: make(chan struct{}),
	}
	c := &codexClient{
		cfg:                  cfg,
		stdin:                stdin,
		pending:              make(map[int]*pendingRPC),
		processDone:          make(chan struct{}),
		notificationProtocol: "unknown",
	}
	proc.client = c
	go func() {
		defer close(proc.readerDone)
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line != "" {
				c.handleLine(line)
			}
		}
		if err := scanner.Err(); err != nil {
			c.markProcessExited(fmt.Errorf("%w: %v", errCodexProcessExited, err))
			return
		}
		c.markProcessExited(errCodexProcessExited)
	}()
	cfg.Logger.Info("codex broker started app-server", "pid", proc.pid(), "code_home", cfg.Env["CODEX_HOME"])
	if _, err := c.request(ctx, "initialize", map[string]any{
		"clientInfo":   map[string]any{"name": "multica-agent-sdk", "title": "Multica Agent SDK", "version": "0.2.0"},
		"capabilities": map[string]any{"experimentalApi": true},
	}); err != nil {
		proc.close("initialize failed")
		return nil, fmt.Errorf("codex initialize failed: %s", codexFailureError(err.Error(), proc.stderrTail()))
	}
	c.notify("initialized")
	return proc, nil
}

func (p *codexBrokerProcess) pid() int {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}

func (p *codexBrokerProcess) alive() bool {
	return p != nil && p.client != nil && p.client.getProcessErr() == nil
}

func (p *codexBrokerProcess) close(reason string) {
	if p == nil {
		return
	}
	p.cfg.Logger.Info("codex broker closing app-server", "pid", p.pid(), "reason", reason)
	p.waitOnce.Do(func() {
		if p.stdin != nil {
			_ = p.stdin.Close()
		}
		select {
		case <-p.readerDone:
		case <-time.After(codexGracefulShutdownTimeout):
			if p.cmd != nil && p.cmd.Process != nil {
				_ = p.cmd.Process.Kill()
			}
			<-p.readerDone
		}
		if p.cmd != nil {
			_ = p.cmd.Wait()
		}
	})
}

func (p *codexBrokerProcess) stderrTail() string {
	if p == nil || p.stderr == nil {
		return ""
	}
	return p.stderr.Tail()
}

func (c *codexClient) beginBrokerTurn(onMessage func(Message), onSemanticActivity func(string), onTurnDone func(bool)) {
	c.threadID = ""
	c.turnID = ""
	c.turnStarted = false
	c.completedTurnIDs = map[string]bool{}
	c.usageMu.Lock()
	c.usage = TokenUsage{}
	c.usageMu.Unlock()
	c.turnErrorMu.Lock()
	c.turnError = ""
	c.turnErrorMu.Unlock()
	c.onMessage = onMessage
	c.onSemanticActivity = onSemanticActivity
	c.onTurnDone = onTurnDone
}

func (c *codexClient) endBrokerTurn() {
	c.onMessage = nil
	c.onSemanticActivity = nil
	c.onTurnDone = nil
}

func cloneEnvMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func codexBrokerKey(execPath string, args []string, env map[string]string, mcpConfig []byte) string {
	h := sha256.New()
	h.Write([]byte(execPath))
	h.Write([]byte{0})
	for _, arg := range args {
		h.Write([]byte(arg))
		h.Write([]byte{0})
	}
	keys := make([]string, 0, len(env))
	for key := range env {
		if isCodexBrokerKeyEnv(key) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		h.Write([]byte(key))
		h.Write([]byte("="))
		h.Write([]byte(env[key]))
		h.Write([]byte{0})
	}
	h.Write(mcpConfig)
	return hex.EncodeToString(h.Sum(nil))
}

func isCodexBrokerKeyEnv(key string) bool {
	if key == "CODEX_HOME" || key == "MULTICA_CODEX_HOME" || key == "MULTICA_CODEX_BROKER_SKILLS_HASH" || key == "MULTICA_CODEX_IMAGE_GENERATION" {
		return true
	}
	return strings.HasSuffix(key, "_PROXY") || strings.HasSuffix(key, "_proxy") || key == "NO_PROXY" || key == "no_proxy"
}
