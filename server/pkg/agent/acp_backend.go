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

// acpRuntimeSpec contains only the wire and launch differences between the
// Hermes, Kimi, and Kiro ACP runtimes. The process lifecycle, ACP handshake,
// session management, cancellation, and result assembly are one shared path.
type acpRuntimeSpec struct {
	provider                 string
	defaultExecutable        string
	baseArgs                 []string
	blockedArgs              map[string]blockedArgMode
	resumeMethod             string
	extraEnv                 []string
	gateHistoryReplay        bool
	passModelOnSessionNew    bool
	inferModelFromSession    bool
	prependSystemPrompt      bool
	duplicatePromptAsContent bool
	normalizeToolName        func(string) string
	mergeCacheReadUsage      bool
	usesCWDContextFiles      bool
}

func executeACPBackend(ctx context.Context, prompt string, opts ExecOptions, cfg Config, spec acpRuntimeSpec) (*Session, error) {
	execPath := cfg.ExecutablePath
	if execPath == "" {
		execPath = spec.defaultExecutable
	}
	if _, err := exec.LookPath(execPath); err != nil {
		return nil, fmt.Errorf("%s executable not found at %q: %w", spec.provider, execPath, err)
	}

	mcpServers, err := buildACPMcpServers(opts.McpConfig, cfg.Logger)
	if err != nil {
		return nil, fmt.Errorf("%s: invalid mcp_config: %w", spec.provider, err)
	}

	timeout := opts.Timeout
	runCtx, cancel := runContext(ctx, timeout)
	args := append(append([]string(nil), spec.baseArgs...), filterCustomArgs(opts.CustomArgs, spec.blockedArgs, cfg.Logger)...)
	cmd := exec.CommandContext(runCtx, execPath, args...)
	hideAgentWindow(cmd)
	cfg.Logger.Info("agent command", "exec", execPath, "args", args)
	if opts.Cwd != "" {
		cmd.Dir = opts.Cwd
	}
	if spec.usesCWDContextFiles {
		_, agentsErr := os.Stat(filepath.Join(opts.Cwd, "AGENTS.md"))
		cfg.Logger.Info(spec.provider+" acp starting", "cwd", opts.Cwd, "agents_md_present", opts.Cwd != "" && agentsErr == nil)
		if opts.SystemPrompt != "" {
			cfg.Logger.Debug(spec.provider+" ignoring ExecOptions.SystemPrompt; using cwd-scoped context files", "cwd", opts.Cwd)
		}
	}
	cmd.Env = append(buildEnv(cfg.Env), spec.extraEnv...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("%s stdout pipe: %w", spec.provider, err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("%s stdin pipe: %w", spec.provider, err)
	}
	providerErr := newACPProviderErrorSniffer(spec.provider)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("%s stderr pipe: %w", spec.provider, err)
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("start %s: %w", spec.provider, err)
	}

	stderrSink := io.MultiWriter(newLogWriter(cfg.Logger, "["+spec.provider+":stderr] "), providerErr)
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		_, _ = io.Copy(stderrSink, stderr)
	}()
	cfg.Logger.Info(spec.provider+" acp started", "pid", cmd.Process.Pid, "cwd", opts.Cwd)

	msgCh := make(chan Message, 256)
	resCh := make(chan Result, 1)
	var outputMu sync.Mutex
	var output strings.Builder
	var currentTurn atomic.Bool
	acceptCurrentTurn := func() bool {
		return !spec.gateHistoryReplay || currentTurn.Load()
	}
	promptDone := make(chan hermesPromptResult, 1)
	c := &hermesClient{
		cfg:          cfg,
		stdin:        stdin,
		pending:      make(map[int]*pendingRPC),
		pendingTools: make(map[string]*pendingToolCall),
		acceptNotification: func(string) bool {
			return acceptCurrentTurn()
		},
		onMessage: func(msg Message) {
			if !acceptCurrentTurn() {
				return
			}
			if msg.Type == MessageToolUse && spec.normalizeToolName != nil {
				msg.Tool = spec.normalizeToolName(msg.Tool)
			}
			if msg.Type == MessageText {
				outputMu.Lock()
				output.WriteString(msg.Content)
				outputMu.Unlock()
			}
			trySend(msgCh, msg)
		},
		onPromptDone: func(result hermesPromptResult) {
			if !acceptCurrentTurn() {
				return
			}
			select {
			case promptDone <- result:
			default:
			}
		},
	}

	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line != "" {
				c.handleLine(line)
			}
		}
		c.closeAllPending(fmt.Errorf("%s process exited", spec.provider))
	}()

	go func() {
		defer cancel()
		defer close(msgCh)
		defer close(resCh)
		defer func() {
			_ = stdin.Close()
			_ = cmd.Wait()
		}()

		startTime := time.Now()
		finalStatus := "completed"
		var finalError string
		var sessionID string
		effectiveModel := opts.Model
		if spec.inferModelFromSession {
			effectiveModel = strings.TrimSpace(effectiveModel)
		}

		initResult, err := c.request(runCtx, "initialize", map[string]any{
			"protocolVersion": 1,
			"clientInfo": map[string]any{
				"name":    "multica-agent-sdk",
				"version": "0.2.0",
			},
			"clientCapabilities": map[string]any{},
		})
		if err != nil {
			resCh <- failedACPSetupResult(spec.provider, "initialize", err, startTime)
			return
		}
		mcpServers = filterACPMcpServersByCapability(mcpServers, extractACPMcpCapabilities(initResult), spec.provider, cfg.Logger)

		cwd := opts.Cwd
		if cwd == "" {
			cwd = "."
		}
		if opts.ResumeSessionID != "" {
			result, err := c.request(runCtx, spec.resumeMethod, map[string]any{
				"cwd":        cwd,
				"sessionId":  opts.ResumeSessionID,
				"mcpServers": mcpServers,
			})
			if err != nil {
				resCh <- failedACPSetupResult(spec.provider, spec.resumeMethod, err, startTime)
				return
			}
			var changed bool
			sessionID, changed = resolveResumedSessionID(opts.ResumeSessionID, result)
			if changed {
				cfg.Logger.Warn("agent returned a different session id on resume — original was likely lost; continuing with the new id",
					"backend", spec.provider, "requested", opts.ResumeSessionID, "actual", sessionID)
			}
			if spec.inferModelFromSession && effectiveModel == "" {
				effectiveModel = extractACPCurrentModelID(result)
			}
		} else {
			params := map[string]any{"cwd": cwd, "mcpServers": mcpServers}
			if spec.passModelOnSessionNew {
				params = buildHermesSessionParams(cwd, opts.Model, mcpServers)
			}
			result, err := c.request(runCtx, "session/new", params)
			if err != nil {
				resCh <- failedACPSetupResult(spec.provider, "session/new", err, startTime)
				return
			}
			sessionID = extractACPSessionID(result)
			if sessionID == "" {
				resCh <- Result{Status: "failed", Error: spec.provider + " session/new returned no session ID", DurationMs: time.Since(startTime).Milliseconds()}
				return
			}
			if spec.inferModelFromSession && effectiveModel == "" {
				effectiveModel = extractACPCurrentModelID(result)
			}
		}

		c.sessionID = sessionID
		cfg.Logger.Info(spec.provider+" session created", "session_id", sessionID)
		if opts.Model != "" {
			if _, err := c.request(runCtx, "session/set_model", map[string]any{"sessionId": sessionID, "modelId": opts.Model}); err != nil {
				cfg.Logger.Warn(spec.provider+" set_session_model failed", "error", err, "requested_model", opts.Model)
				if opts.ResumeSessionID != "" && isACPSessionNotFound(err) {
					cfg.Logger.Warn("resumed session not found at set_model time; clearing session id so the daemon retries fresh",
						"backend", spec.provider, "session_id", sessionID)
					sessionID = ""
				}
				resCh <- Result{
					Status:     "failed",
					Error:      fmt.Sprintf("%s could not switch to model %q: %v", spec.provider, opts.Model, err),
					DurationMs: time.Since(startTime).Milliseconds(),
					SessionID:  sessionID,
				}
				return
			}
			cfg.Logger.Info(spec.provider+" session model set", "model", opts.Model)
		}

		userText := prompt
		if spec.prependSystemPrompt && opts.SystemPrompt != "" {
			userText = opts.SystemPrompt + "\n\n---\n\n" + prompt
		}
		promptBlocks := []map[string]any{{"type": "text", "text": userText}}
		promptParams := map[string]any{"sessionId": sessionID, "prompt": promptBlocks}
		if spec.duplicatePromptAsContent {
			promptParams["content"] = promptBlocks
		}
		currentTurn.Store(true)
		_, err = c.request(runCtx, "session/prompt", promptParams)
		if err != nil {
			switch runCtx.Err() {
			case context.DeadlineExceeded:
				finalStatus = "timeout"
				finalError = fmt.Sprintf("%s timed out after %s", spec.provider, timeout)
			case context.Canceled:
				finalStatus = "aborted"
				finalError = "execution cancelled"
			default:
				finalStatus = "failed"
				finalError = fmt.Sprintf("%s session/prompt failed: %v", spec.provider, err)
				if opts.ResumeSessionID != "" && isACPSessionNotFound(err) {
					cfg.Logger.Warn("resumed session not found at prompt time; clearing session id so the daemon retries fresh",
						"backend", spec.provider, "session_id", sessionID)
					sessionID = ""
				}
			}
		} else {
			select {
			case result := <-promptDone:
				if result.stopReason == "cancelled" {
					finalStatus = "aborted"
					finalError = spec.provider + " cancelled the prompt"
				}
				c.usageMu.Lock()
				c.usage.InputTokens += result.usage.InputTokens
				c.usage.OutputTokens += result.usage.OutputTokens
				if spec.mergeCacheReadUsage {
					c.usage.CacheReadTokens += result.usage.CacheReadTokens
				}
				c.usageMu.Unlock()
			default:
			}
		}

		resCh <- finishACPBackendResult(acpBackendResultParams{
			Provider: spec.provider, Logger: cfg.Logger, PID: cmd.Process.Pid,
			StartTime: startTime, Status: finalStatus, Error: finalError,
			SessionID: sessionID, Model: effectiveModel, Stdin: stdin,
			Cancel: cancel, ReaderDone: readerDone, StderrDone: stderrDone,
			OutputMu: &outputMu, Output: &output, Client: c, ProviderErr: providerErr,
		})
	}()

	return &Session{Messages: msgCh, Result: resCh}, nil
}

func failedACPSetupResult(provider, operation string, err error, started time.Time) Result {
	return Result{
		Status:     "failed",
		Error:      fmt.Sprintf("%s %s failed: %v", provider, operation, err),
		DurationMs: time.Since(started).Milliseconds(),
	}
}
