package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// 普通 CodeBuddy 运行使用 stream-json；带 Goal 的会话使用原生 ACP 控制和循环。
// 每次执行仍独占自己的进程、MCP 和任务凭据，常驻复用由独立资源生命周期接管。
func (b *codebuddyBackend) executeNative(ctx context.Context, prompt string, opts ExecOptions) (*Session, error) {
	if err := validateNativeControlOnly(opts); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	executable := b.cfg.ExecutablePath
	if executable == "" {
		executable = "codebuddy"
	}
	if _, err := exec.LookPath(executable); err != nil {
		return nil, err
	}
	mcp, err := buildACPMcpServers(opts.McpConfig, b.cfg.Logger)
	if err != nil {
		return nil, err
	}
	runCtx, cancel := runContext(ctx, opts.Timeout)
	processCtx, stopProcess := context.WithCancel(context.WithoutCancel(ctx))
	args := codebuddyNativeArgs(opts, b.cfg)
	cmd := b.cfg.commandAt(executable).exec(processCtx, args...)
	cmd.Env = buildEnv(b.cfg.Env)
	cmd.Dir = opts.Cwd
	hideAgentWindow(cmd)
	cmd.WaitDelay = 5 * time.Second
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		stopProcess()
		return nil, err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		_ = stdout.Close()
		cancel()
		stopProcess()
		return nil, err
	}
	stderr := newStderrTail(newLogWriter(b.cfg.Logger, "[codebuddy:stderr] "), agentStderrTailBytes)
	cmd.Stderr = stderr
	b.cfg.logAgentCommand(cmd, newAgentCommandLogArgs(args))
	if err := startOwnedProcessTree(cmd, b.cfg.Logger); err != nil {
		_ = stdout.Close()
		_ = stdin.Close()
		cancel()
		stopProcess()
		return nil, err
	}
	identity := captureCLIIdentity(cmd.Process.Pid)
	messages := make(chan Message, 256)
	results := make(chan Result, 1)
	var stateMu sync.RWMutex
	var sessionID, requestID string
	requestIDs := make(map[string]bool)
	var output strings.Builder
	var accepting atomic.Bool
	var active atomic.Bool
	var commandReady atomic.Bool
	var commandMu sync.Mutex
	var controlFailure string
	var goalWasActive atomic.Bool
	var humanStoppedGoal atomic.Bool
	var client *hermesClient
	emit := func(msg Message) {
		if msg.Type == MessageText {
			stateMu.Lock()
			output.WriteString(msg.Content)
			stateMu.Unlock()
		}
		sendCodebuddyMessage(runCtx, messages, msg)
	}
	broker := newInteractionBroker("codebuddy", runCtx, runtimeInteractionRequest(opts), emit)
	client = &hermesClient{cfg: b.cfg, stdin: stdin, pending: make(map[int]*pendingRPC), pendingTools: make(map[string]*pendingToolCall), onMessage: emit,
		acceptNotification: func(string) bool { return accepting.Load() }, toolStartCarriesFinalInput: true,
		onRequestSent: func(method string) {
			if method == "session/prompt" {
				commandReady.Store(true)
			}
		},
	}
	client.onAgentRequest = func(method string, id, params json.RawMessage) bool {
		stateMu.RLock()
		sid := sessionID
		stateMu.RUnlock()
		return handleCodebuddyACPInteraction(client, broker, sid, method, id, params)
	}
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		defer client.closeAllPending(errors.New("CodeBuddy ACP process exited"))
		scanner := newAgentStreamScanner(stdout)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			var envelope struct {
				Method string `json:"method"`
				Params struct {
					SessionID string `json:"sessionId"`
					Update    struct {
						SessionUpdate string                     `json:"sessionUpdate"`
						Meta          map[string]json.RawMessage `json:"_meta"`
					} `json:"update"`
				} `json:"params"`
			}
			if json.Unmarshal([]byte(line), &envelope) != nil {
				continue
			}
			stateMu.RLock()
			sid := sessionID
			stateMu.RUnlock()
			if envelope.Method == "session/update" && sid != "" && envelope.Params.SessionID != sid {
				continue
			}
			if envelope.Method == "session/update" && accepting.Load() {
				meta := envelope.Params.Update.Meta
				var observed string
				_ = json.Unmarshal(meta["codebuddy.ai/requestId"], &observed)
				if observed != "" && (envelope.Params.Update.SessionUpdate == "agent_message_chunk" || envelope.Params.Update.SessionUpdate == "agent_thought_chunk" || envelope.Params.Update.SessionUpdate == "tool_call" || envelope.Params.Update.SessionUpdate == "tool_call_update") {
					stateMu.Lock()
					if requestID != observed {
						output.Reset()
					}
					requestID = observed
					requestIDs[observed] = true
					stateMu.Unlock()
					active.Store(true)
				}
				if envelope.Params.Update.SessionUpdate == "turn_end" {
					active.Store(false)
				}
			}
			if envelope.Method == "session/update" && accepting.Load() && envelope.Params.Update.SessionUpdate == "usage_update" {
				emitCodebuddyACPContext(line, emit)
			}
			client.handleLine(line)
		}
	}()
	supplement := func(ctx context.Context, text string) error {
		stateMu.RLock()
		sid, rid := sessionID, requestID
		stateMu.RUnlock()
		if sid == "" || rid == "" || !active.Load() {
			return errors.New("CodeBuddy has no active provider-confirmed turn")
		}
		call, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		raw, err := client.request(call, "session/steer", map[string]any{"sessionId": sid, "expectedRequestId": rid, "contentBlocks": []map[string]string{{"type": "text", "text": text}}})
		if err != nil {
			return err
		}
		var ack struct {
			Steered        bool   `json:"steered"`
			OwnerRequestID string `json:"ownerRequestId"`
		}
		if json.Unmarshal(raw, &ack) != nil || !ack.Steered || ack.OwnerRequestID != rid {
			return errors.New("CodeBuddy did not acknowledge steer for the active turn")
		}
		return nil
	}
	runCommand := func(ctx context.Context, command string) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		commandMu.Lock()
		defer commandMu.Unlock()
		if !commandReady.Load() || runCtx.Err() != nil {
			return "", errors.New("CodeBuddy execution no longer accepts commands")
		}
		stateMu.RLock()
		sid := sessionID
		stateMu.RUnlock()
		call, stop := context.WithTimeout(context.WithoutCancel(runCtx), 5*time.Second)
		defer stop()
		command = strings.TrimSpace(command)
		if command == "/plan" || command == "/plan off" {
			mode, output := "plan", "已进入计划模式。"
			if command == "/plan off" {
				mode, output = codebuddyPermissionMode(ExecOptions{ApprovalPolicy: opts.ApprovalPolicy}), "已退出计划模式。"
			}
			if _, err := client.request(call, "session/set_mode", map[string]any{"sessionId": sid, "modeId": mode}); err != nil {
				return "", err
			}
			emit(runtimeTelemetry("codebuddy", "control", map[string]any{"action": "mode", "mode": mode, "session_id": sid}))
			return output, nil
		}
		action, condition, err := codebuddyGoalAction(command)
		if err != nil {
			return "", err
		}
		goal, err := requestCodebuddyGoal(call, client, sid, action, condition)
		if err != nil {
			var rejected *acpRPCError
			if action != "snapshot" && !errors.As(err, &rejected) {
				verify, cancelVerify := context.WithTimeout(context.WithoutCancel(runCtx), 2*time.Second)
				actual, verifyErr := requestCodebuddyGoal(verify, client, sid, "snapshot", "")
				cancelVerify()
				activeGoal := actual.HasGoal && actual.ActiveGoal != nil && !actual.ActiveGoal.Paused
				verified := verifyErr == nil && ((action == "set" && activeGoal && actual.ActiveGoal.Condition == condition) ||
					(action == "resume" && activeGoal) || (action == "pause" && actual.HasGoal && actual.ActiveGoal != nil && actual.ActiveGoal.Paused) ||
					(action == "clear" && !actual.HasGoal))
				if verified {
					if activeGoal {
						goalWasActive.Store(true)
						humanStoppedGoal.Store(false)
					} else {
						humanStoppedGoal.Store(true)
					}
					emitCodebuddyGoal(client, sid, "snapshot", actual)
				} else {
					controlFailure = "CodeBuddy goal control outcome could not be verified"
					commandReady.Store(false)
					cancel()
				}
			}
			return "", err
		}
		if goal.HasGoal && goal.ActiveGoal != nil && !goal.ActiveGoal.Paused {
			goalWasActive.Store(true)
			humanStoppedGoal.Store(false)
		}
		if action == "pause" || action == "clear" {
			humanStoppedGoal.Store(true)
		}
		emitCodebuddyGoal(client, sid, action, goal)
		return codebuddyGoalSummary(goal), nil
	}
	go func() {
		defer stopProcess()
		defer cancel()
		defer close(messages)
		defer close(results)
		defer broker.stop()
		started := time.Now()
		result := Result{Status: "failed"}
		defer func() {
			commandReady.Store(false)
			commandMu.Lock()
			defer commandMu.Unlock()
			if controlFailure != "" {
				result.Status, result.Error = "failed", controlFailure
			}
			active.Store(false)
			// 取消必须先让原生 Goal 停止续跑，避免恢复同一会话时悄悄重新开始。
			if result.Status != "completed" && result.SessionID != "" {
				cleanup, stop := context.WithTimeout(context.WithoutCancel(runCtx), 2*time.Second)
				_, _ = requestCodebuddyGoal(cleanup, client, result.SessionID, "pause", "")
				stop()
			}
			_ = stdin.Close()
			select {
			case <-readerDone:
			case <-time.After(time.Second):
				stopProcess()
				_ = stdout.Close()
				<-readerDone
			}
			waited := make(chan error, 1)
			go func() { waited <- cmd.Wait() }()
			var waitErr error
			select {
			case waitErr = <-waited:
			case <-time.After(time.Second):
				stopProcess()
				waitErr = <-waited
			}
			releaseProcessGroup(cmd)
			if waitErr != nil || result.Status != "completed" {
				reapTaskEscapees(b.cfg.Env["MULTICA_TASK_ID"], identity, b.cfg.Logger)
			}
			result.DurationMs = time.Since(started).Milliseconds()
			if result.Error != "" {
				result.Error = withAgentStderr(result.Error, "codebuddy", stderr.Tail())
			}
			stateMu.RLock()
			result.ProviderTraceID = requestID
			if result.Output == "" {
				result.Output = output.String()
			}
			stateMu.RUnlock()
			// ACP 的占用快照不作为账单；逐调用用量来自本次流确认过 CID 的 SDK 记录。
			var traces []SubagentTrace
			var calls []SubagentCall
			result.Usage, traces, calls = codebuddyNativeAccounting(b.cfg.Env, result.SessionID, requestIDs)
			for _, trace := range traces {
				if trace.TraceID == result.ProviderTraceID {
					result.SDKTraceID = trace.SDKTraceID
					result.SDKTraceSourceSHA256 = trace.SDKSourceSHA
					result.SDKTraceSourcePath = SDKTraceSourceMarker
				} else {
					result.ExtraTraces = append(result.ExtraTraces, trace)
				}
			}
			for _, call := range calls {
				emit(runtimeTelemetry("codebuddy", "context", map[string]any{"session_id": result.SessionID, "request_id": call.CID, "item_id": call.MessageID, "model": call.Model, "scope": "request_delta", "source": "sdk_log", "input_tokens": call.Usage.InputTokens, "output_tokens": call.Usage.OutputTokens, "cache_read_tokens": call.Usage.CacheReadTokens, "cache_write_tokens": call.Usage.CacheWriteTokens}))
			}
			if goalWasActive.Load() && len(requestIDs) > 0 {
				emit(runtimeTelemetry("codebuddy", "accounting", map[string]any{"scope": "observed_provider_calls", "goal_evaluator_usage": "unknown"}))
			}
			cancel()
			results <- result
		}()
		fail := func(err error) {
			result.Error = err.Error()
			switch runCtx.Err() {
			case context.Canceled:
				result.Status = "aborted"
			case context.DeadlineExceeded:
				result.Status = "timeout"
			}
		}
		handshake := opts.ThreadHandshakeTimeout
		if handshake <= 0 {
			handshake = 45 * time.Second
		}
		setup, stop := context.WithTimeout(runCtx, handshake)
		defer stop()
		if _, err := client.request(setup, "initialize", map[string]any{"protocolVersion": 1, "clientCapabilities": map[string]any{}, "clientInfo": map[string]string{"name": "multica", "version": "1"}}); err != nil {
			fail(err)
			return
		}
		method := "session/new"
		params := map[string]any{"cwd": opts.Cwd, "mcpServers": mcp}
		if opts.ResumeSessionID != "" {
			method = "session/load"
			params["sessionId"] = opts.ResumeSessionID
		}
		raw, err := client.request(setup, method, params)
		if err != nil {
			result.ResumeRejected = opts.ResumeSessionID != "" && isACPSessionNotFound(err)
			fail(err)
			return
		}
		sid := extractACPSessionID(raw)
		if opts.ResumeSessionID != "" {
			var changed bool
			sid, changed = resolveResumedSessionID(opts.ResumeSessionID, raw)
			if changed {
				result.ResumeRejected = true
				fail(errors.New("CodeBuddy resumed a different session"))
				return
			}
		}
		if sid == "" {
			fail(errors.New("CodeBuddy returned no native session identity"))
			return
		}
		stateMu.Lock()
		sessionID = sid
		stateMu.Unlock()
		client.sessionID = sid
		result.SessionID = sid
		emit(Message{Type: MessageStatus, Status: "running", SessionID: sid})
		if opts.Model != "" && extractACPCurrentModelID(raw) != opts.Model {
			if _, err := client.request(setup, "session/set_model", map[string]any{"sessionId": sid, "modelId": opts.Model}); err != nil {
				fail(err)
				return
			}
		}
		mode := codebuddyPermissionMode(opts)
		if _, err := client.request(setup, "session/set_mode", map[string]any{"sessionId": sid, "modeId": mode}); err != nil {
			fail(err)
			return
		}
		accepting.Store(true)
		command := strings.TrimSpace(opts.Command)
		var goal codebuddyGoalSnapshot
		if command == "/goal" || strings.HasPrefix(command, "/goal ") {
			action, condition, err := codebuddyGoalAction(command)
			if err != nil {
				fail(err)
				return
			}
			goal, err = requestCodebuddyGoal(setup, client, sid, action, condition)
			if err != nil {
				fail(err)
				return
			}
			emitCodebuddyGoal(client, sid, action, goal)
			goalWasActive.Store(goal.HasGoal && goal.ActiveGoal != nil && !goal.ActiveGoal.Paused)
			if opts.CommandOnly || action == "snapshot" || action == "pause" || action == "clear" || !goal.HasGoal {
				result.Status = "completed"
				result.Output = codebuddyGoalSummary(goal)
				if command == "/goal complete" {
					result.Output = "已结束目标；CodeBuddy 的原生控制已清除该目标。"
				}
				return
			}
		} else if opts.CommandOnly {
			result.Status = "completed"
			result.Output = "已退出计划模式。"
			if mode == "plan" {
				result.Output = "已进入计划模式。"
			}
			return
		} else {
			before, err := requestCodebuddyGoal(setup, client, sid, "snapshot", "")
			if err != nil {
				fail(err)
				return
			}
			goalWasActive.Store(before.HasGoal && before.ActiveGoal != nil && !before.ActiveGoal.Paused)
			raw, err := client.request(runCtx, "session/prompt", map[string]any{"sessionId": sid, "prompt": []map[string]string{{"type": "text", "text": prompt}}})
			if err != nil {
				fail(err)
				return
			}
			var terminal struct {
				StopReason string `json:"stopReason"`
			}
			_ = json.Unmarshal(raw, &terminal)
			if terminal.StopReason == "cancelled" {
				result.Status = "aborted"
				result.Error = "CodeBuddy native turn cancelled"
				return
			}
			if terminal.StopReason == "refusal" {
				fail(fmt.Errorf("CodeBuddy native turn ended: %s", terminal.StopReason))
				return
			}
		}
		commandReady.Store(true)
		for {
			goal, err = waitCodebuddyGoal(runCtx, client, sid)
			if err != nil {
				fail(err)
				return
			}
			commandMu.Lock()
			// 收尾与用户命令共用一个门，避免刚接受的新目标被旧快照结束。
			poll, stop := context.WithTimeout(runCtx, 5*time.Second)
			latest, snapshotErr := requestCodebuddyGoal(poll, client, sid, "snapshot", "")
			stop()
			if snapshotErr != nil {
				commandMu.Unlock()
				fail(snapshotErr)
				return
			}
			goal = latest
			if goal.RunInFlight || (goal.HasGoal && goal.ActiveGoal != nil && !goal.ActiveGoal.Paused) {
				commandMu.Unlock()
				continue
			}
			commandReady.Store(false)
			commandMu.Unlock()
			break
		}
		if goalWasActive.Load() && !humanStoppedGoal.Load() && goal.Recap != nil && goal.Recap.Latest != nil && !goal.Recap.Latest.OK && !goal.HasGoal {
			fail(fmt.Errorf("CodeBuddy goal could not complete: %s", goal.Recap.Latest.Reason))
			return
		}
		if err := waitCodebuddyNativeTools(runCtx, client); err != nil {
			fail(err)
			return
		}
		result.Status = "completed"
	}()
	return &Session{RunCommand: runCommand, CommandReady: commandReady.Load, Messages: messages, Result: results, WaitingForInput: broker.waiting, Supplement: supplement, SupplementReady: active.Load}, nil
}

func codebuddyNativeArgs(opts ExecOptions, cfg Config) []string {
	source := buildCodebuddyArgs(opts, cfg.Logger)
	args := []string{"--acp"}
	for i := 0; i < len(source); i++ {
		switch source[i] {
		case "-p", "--verbose":
			continue
		case "--input-format", "--output-format", "--resume":
			i++
			continue
		default:
			args = append(args, source[i])
		}
	}
	return args
}

func waitCodebuddyNativeTools(ctx context.Context, c *hermesClient) error {
	deadline, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		c.toolMu.Lock()
		pending := len(c.pendingTools)
		c.toolMu.Unlock()
		if pending == 0 {
			return nil
		}
		select {
		case <-deadline.Done():
			return errors.New("CodeBuddy native turn ended before foreground tools completed")
		case <-ticker.C:
		}
	}
}
