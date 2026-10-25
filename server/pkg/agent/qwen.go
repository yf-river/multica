package agent

// 文件职责：Qwen Code 原生非交互协议的 Backend 适配器。
//
// 运行方式：以 `qwen --output-format stream-json`（其它参数见 buildQwenArgs）
// 启动子进程，prompt 从 stdin 送入（EOF 表示提示结束），stdout 逐行返回 JSONL
// 事件。本文件负责把事件流翻译成统一的 Message（system/assistant 里的文本与
// thinking、tool_use；user 里的 tool_result；终局 result/error），并在收尾时按
// 事件形态（sawResult/resultIsError 等）裁定最终状态、汇总用量、识别续跑被拒。
// 事件 schema 依据 testdata/qwen-code-0.20.0-stream-json.jsonl 里 Qwen Code
// 0.20.0 的真实抓包。
//
// 主要入口：qwenBackend.Execute；配套子文件处理跨平台启动（qwen_invocation*）。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// qwenBackend 驱动 Qwen Code 原生非交互 JSONL 协议：
// 运行 `qwen --output-format stream-json`，prompt 走 stdin。
// 事件 schema 依据 testdata/qwen-code-0.20.0-stream-json.jsonl 里 Qwen Code
// 0.20.0 的真实输出抓包。
type qwenBackend struct {
	cfg Config
}

// qwenBlockedArgs 归 Multica 所有、不允许 custom_args 覆盖的参数。Qwen 把任务
// prompt 与流协议都做成旗标，因此这些不能被人替换；模型/会话也由 Multica 选定，
// 而 safe mode 会禁掉 QWEN.md 上下文文件。--yolo/-y、--approval-mode 与
// --core-tools 是 daemon 拥有的权限旗标：用户不能从 custom_args 关掉绕过模式、
// 也不能收窄核心工具清单（真想禁某个工具请改用 --exclude-tools）。
var qwenBlockedArgs = map[string]blockedArgMode{
	"-p":                   blockedWithValue,
	"--prompt":             blockedWithValue,
	"-i":                   blockedWithValue,
	"--prompt-interactive": blockedWithValue,
	"-o":                   blockedWithValue,
	"--output-format":      blockedWithValue,
	"-m":                   blockedWithValue,
	"--model":              blockedWithValue,
	"-r":                   blockedWithValue,
	"--resume":             blockedWithValue,
	"-c":                   blockedStandalone,
	"--continue":           blockedStandalone,
	"--chat-recording":     blockedWithValue,
	"--mcp-config":         blockedWithValue,
	"--safe-mode":          blockedStandalone,
	"--yolo":               blockedStandalone,
	"-y":                   blockedStandalone,
	"--approval-mode":      blockedWithValue,
	"--core-tools":         blockedWithValue,
}

// buildQwenArgs 组装一次一次性 qwen 调用的 argv。
//
// prompt 故意不放进 argv。Qwen Code 的无头模式在没给 -p 时从 stdin 读非交互
// prompt；而把（可任意大、受用户影响的）prompt 文本放命令行在 Windows 上不安全：
// 即便绕过 qwen.cmd 直接走 qwen.ps1（见 qwen_invocation_windows.go），PowerShell
// 对参数的重序列化也扛不住"值里嵌了双引号"——cursor-agent 把 prompt 挪到 stdin
// 之前踩的就是同一类坑（#5649）。本实现就是那项修复在 qwen 上的对应（#6082）。
// argv 里只留固定的、不含内容的旗标；--mcp-config 的负载同样因为这一原因走
// 临时文件，而不是内联 JSON。
func buildQwenArgs(opts ExecOptions, logger *slog.Logger) []string {
	args := []string{"--output-format", "stream-json"}
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}
	if opts.ResumeSessionID != "" {
		args = append(args, "--resume", opts.ResumeSessionID)
	}
	// --yolo 归 daemon 所有：Qwen Code 的非交互模式会滤掉需要审批的工具
	// （run_shell_command、edit、write_file 等），除非开了绕过模式。其它适配器
	// 都有等价机制；这里保证 Qwen 无头运行与它们一致（MUL-5134）。
	args = append(args, "--yolo")
	args = append(args, filterCustomArgs(opts.ExtraArgs, qwenBlockedArgs, logger)...)
	args = append(args, filterCustomArgs(opts.CustomArgs, qwenBlockedArgs, logger)...)
	return args
}

// Execute 实现 Backend.Execute：跑一次 qwen stream-json，prompt 从 stdin 送。
// 若 opts 携带 --mcp-config 的管理型配置，会先落到 0600 临时文件、进程退出后
// 清理，避免配置出现在 argv 或日志里。
func (b *qwenBackend) Execute(ctx context.Context, prompt string, opts ExecOptions) (*Session, error) {
	execName := b.cfg.ExecutablePath
	if execName == "" {
		execName = "qwen"
	}
	lookedUp, err := exec.LookPath(execName)
	if err != nil {
		return nil, fmt.Errorf("qwen executable not found at %q: %w", execName, err)
	}
	timeout := opts.Timeout
	runCtx, cancel := runContext(ctx, timeout)
	args := buildQwenArgs(opts, b.cfg.Logger)

	// Qwen Code 0.20.0 通过 --mcp-config 接受 JSON 字符串或文件路径。把受管配置
	// 落成 0600 临时文件，避免出现在 argv 或日志里，进程退出时删除。
	var mcpConfigPath string
	var mcpFileCleanup func()
	if hasManagedMcpConfig(opts.McpConfig) {
		path, err := writeMcpConfigToTemp(opts.McpConfig)
		if err != nil {
			cancel()
			return nil, fmt.Errorf("write qwen mcp_config: %w", err)
		}
		mcpConfigPath = path
		mcpFileCleanup = func() { cleanupMcpConfigTemp(mcpConfigPath) }
		args = append(args, "--mcp-config", mcpConfigPath)
	}
	// 若后续启动步骤在结果协程接管清理之前就返回，这里兜底清理临时文件。
	defer func() {
		if mcpFileCleanup != nil {
			mcpFileCleanup()
		}
	}()
	// 按平台选启动方式（cmd 包装 / ps1 等），再真正派生进程。
	cmd, _, _ := b.cfg.commandAt(execName).execVia(runCtx, chooseQwenInvocation, lookedUp, args, b.cfg.Logger)
	hideAgentWindow(cmd)
	b.cfg.logAgentCommand(cmd, newAgentCommandLogArgs(args))
	cmd.WaitDelay = 10 * time.Second
	if opts.Cwd != "" {
		cmd.Dir = opts.Cwd
	}
	cmd.Env = buildEnv(b.cfg.Env)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("qwen stdout pipe: %w", err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("qwen stdin pipe: %w", err)
	}
	// 关 stdin 的动作可能来自多处（写线程收尾、runCtx 到期兜底），用 Once 保证
	// 只真正关一次，避免二次 Close 报错。
	var closeStdinOnce sync.Once
	closeStdin := func() { closeStdinOnce.Do(func() { _ = stdin.Close() }) }
	stderrBuf := newStderrTail(newLogWriter(b.cfg.Logger, "[qwen:stderr] "), agentStderrTailBytes)
	cmd.Stderr = stderrBuf
	if err := startOwnedProcessTree(cmd, b.cfg.Logger); err != nil {
		closeStdin()
		cancel()
		return nil, fmt.Errorf("start qwen: %w", err)
	}
	// cmd.Start 已成功，临时文件的清理责任移交给结果协程。
	mcpFileCleanup = nil
	b.cfg.Logger.Info("qwen started", "pid", cmd.Process.Pid, "cwd", opts.Cwd, "model", opts.Model)

	// prompt 走 stdin 交付（见 buildQwenArgs）。用独立 goroutine 写，避免与下方
	// stdout 读取协程互相死锁：比 OS 管道缓冲还大的 prompt 会在写到一半时阻塞，
	// 直到子进程排空；而子进程在我们没读它 stdout 之前无法排空。关闭 stdin 是
	// "提示结束"的信号——qwen 一直读到 EOF——所以无论成败都会关。
	writeErrCh := make(chan error, 1)
	go func() {
		_, err := io.WriteString(stdin, prompt)
		closeStdin()
		writeErrCh <- err
	}()

	msgCh := make(chan Message, 256)
	resCh := make(chan Result, 1)
	go func() {
		defer cancel()
		defer close(msgCh)
		defer close(resCh)
		if mcpConfigPath != "" {
			defer cleanupMcpConfigTemp(mcpConfigPath)
		}

		started := time.Now()
		state := qwenStreamState{model: opts.Model, usage: make(map[string]TokenUsage), resumed: opts.ResumeSessionID != ""}
		go func() {
			<-runCtx.Done()
			// 关掉 stdin 能放掉仍阻塞在满管道上的 prompt 写（例如子进程在排空之前
			// 就死了），这样那个写协程不会泄漏。
			closeStdin()
			_ = stdout.Close()
		}()

		// 解码循环：逐行读 stdout 事件，翻译推送；坏行只计数跳过。
		scanner := newAgentStreamScanner(stdout)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			var event qwenStreamEvent
			if err := json.Unmarshal([]byte(line), &event); err != nil {
				state.invalidEventCount++
				continue
			}
			state.eventCount++
			state.lastEventType = event.Type
			handleQwenEvent(event, msgCh, &state)
		}
		scanErr := scanner.Err()
		if scanErr != nil {
			_ = stdout.Close()
		}
		exitErr := cmd.Wait()
		releaseProcessGroup(cmd)
		duration := time.Since(started)

		// cmd.Wait 已经关掉 stdin 管道，所以原本阻塞在满管道上的 prompt 写此刻
		// 必然已返回；写协程只发一次结果，这里读它不会挂起。
		writeErr := <-writeErrCh

		// 按"最后助手文本/终局结果/错误形态"统一裁定状态、输出与错误文本。
		status, output, errMsg := finalizeStreamResult("qwen", timeout, runCtx.Err(), writeErr, exitErr, state.sessionID, streamTerminalState{
			lastAssistantText: state.lastAssistantText,
			finalResultText:   state.finalResultText,
			sawResult:         state.sawResult,
			resultIsError:     state.resultIsError,
			scanErr:           scanErr,
		}, "")
		if errMsg != "" {
			errMsg = withAgentStderr(errMsg, "qwen", stderrBuf.Tail())
		}
		logStreamProtocolObservation(b.cfg.Logger, streamProtocolObservation{
			provider: "qwen", cliVersion: b.cfg.CLIVersion, model: state.model,
			exitCode: streamProcessExitCode(exitErr), eventCount: state.eventCount,
			invalidEventCount: state.invalidEventCount, assistantEventCount: state.assistantEventCount,
			toolUseCount: state.toolUseCount, sawResult: state.sawResult, resultIsError: state.resultIsError,
			resultBytes: len(state.finalResultText), lastAssistantBytes: len(state.lastAssistantText),
			scannerError: scanErr != nil, lastEventType: state.lastEventType,
			unreadableAssistantCount: state.unreadableAssistantCount,
		})
		b.cfg.Logger.Info("qwen finished", "pid", cmd.Process.Pid, "status", status, "duration", duration.Round(time.Millisecond).String())
		resCh <- Result{
			Status: status, Output: output, Error: errMsg, DurationMs: duration.Milliseconds(),
			SessionID: resolveSessionID(opts.ResumeSessionID, state.sessionID, status == "failed", errMsg), Usage: state.usage,
			ResumeRejected: resumeWasRejected(opts.ResumeSessionID, state.sessionID, status == "failed", errMsg),
		}
	}()
	return &Session{Messages: msgCh, Result: resCh}, nil
}

// qwenStreamEvent 是 stream-json 事件行的外壳；按 Type 分支取字段。
type qwenStreamEvent struct {
	Type      string          `json:"type"`              // system / assistant / user / result / error
	Subtype   string          `json:"subtype,omitempty"` // result 事件的细分（error/failed 等）
	SessionID string          `json:"session_id,omitempty"`
	Model     string          `json:"model,omitempty"`
	Message   json.RawMessage `json:"message,omitempty"` // assistant/user 事件：消息正文（结构见 qwenMessage）
	Result    string          `json:"result,omitempty"`  // result 事件：最终结果文本
	IsError   bool            `json:"is_error,omitempty"`
	Usage     *qwenUsage      `json:"usage,omitempty"` // result 事件：整轮用量
	Error     json.RawMessage `json:"error,omitempty"` // result 失败时的错误体
}

// qwenMessage 是 assistant/user 事件里消息正文的骨架：模型名、内容块列表、用量。
type qwenMessage struct {
	ID      string             `json:"id,omitempty"`
	Model   string             `json:"model,omitempty"`
	Content []qwenContentBlock `json:"content"`
	Usage   *qwenUsage         `json:"usage,omitempty"`
}

// qwenUsage 是 Qwen 上报的 token 用量。
type qwenUsage struct {
	InputTokens          int64 `json:"input_tokens"`
	OutputTokens         int64 `json:"output_tokens"`
	CacheReadInputTokens int64 `json:"cache_read_input_tokens"` // 命中缓存读入
}

// qwenContentBlock 是消息里的一个内容块；思考/文本/工具调用/工具结果共用此结构。
type qwenContentBlock struct {
	Type      string          `json:"type"`                  // thinking / text / tool_use / tool_result
	Thinking  string          `json:"thinking,omitempty"`    // thinking 块的推理文本
	Text      string          `json:"text,omitempty"`        // text 块的文本
	ID        string          `json:"id,omitempty"`          // tool_use 的调用 id
	Name      string          `json:"name,omitempty"`        // tool_use 的工具名
	Input     json.RawMessage `json:"input,omitempty"`       // tool_use 的入参 JSON
	ToolUseID string          `json:"tool_use_id,omitempty"` // tool_result 对应的调用 id
	Content   json.RawMessage `json:"content,omitempty"`     // tool_result 的出参
}

// qwenStreamState 是单次执行的流状态，跨事件累计会话/模型/文本/用量统计。
type qwenStreamState struct {
	sessionID, model, lastAssistantText, finalResultText, lastEventType string
	sawResult, resultIsError                                            bool // 是否见过终局事件、其失败与否
	usage                                                               map[string]TokenUsage
	fallbackUsage                                                       map[string]TokenUsage
	seenUsageMessageIDs                                                 map[string]struct{}
	anonymousUsage                                                      map[string]TokenUsage
	hasResultUsage                                                      bool
	resumed                                                             bool
	eventCount, invalidEventCount, assistantEventCount, toolUseCount    int
	unreadableAssistantCount                                            int
}

// handleQwenEvent 把单个事件落进 state 并视需要推送统一 Message；顺带累计
// 会话 id、模型名等跨事件信息。
func handleQwenEvent(event qwenStreamEvent, ch chan<- Message, state *qwenStreamState) {
	if event.SessionID != "" {
		state.sessionID = event.SessionID
	}
	if event.Model != "" {
		state.model = event.Model
	}
	switch event.Type {
	case "system":
		// 会话启动信号：先让 UI 进入 running 状态，并把会话 id 尽早暴露。
		trySend(ch, Message{Type: MessageStatus, Status: "running", SessionID: state.sessionID})
	case "assistant":
		state.assistantEventCount++
		turn, model := handleQwenAssistant(event.Message, ch, state)
		if model != "" {
			state.model = model
		}
		state.toolUseCount += turn.toolUses
		if !turn.understood {
			state.unreadableAssistantCount++
		}
		state.lastAssistantText = turn.resolveFallback(state.lastAssistantText)
	case "user":
		handleQwenUser(event.Message, ch)
	case "result":
		state.sawResult = true
		state.resultIsError = event.IsError || event.Subtype == "error" || event.Subtype == "failed"
		if state.resultIsError {
			// Qwen 0.20.0 的失败 result 不带 result 文本，可操作的细节在
			// error.message 里。
			state.finalResultText = qwenErrorText(event)
		} else {
			state.finalResultText = event.Result
		}
		// Qwen restores historical telemetry on resume, so result.usage is a
		// session total, not this invocation's usage. Only fresh runs can use
		// it. Resumed runs retain the deduplicated assistant-message totals;
		// these are best effort and may omit internal calls absent from the
		// stream. Without a run-scoped total or a baseline, the cumulative
		// result cannot safely fill those gaps (including a result-only run).
		if usage := qwenResultUsage(event.Usage, state.model); !state.resumed && len(usage) > 0 {
			state.usage = usage
			state.hasResultUsage = true
		}
	case "error":
		// 对未来 Qwen 版本发出终局 error 事件的情况采取 fail-closed：视为失败终局。
		state.sawResult = true
		state.resultIsError = true
		state.finalResultText = qwenErrorText(event)
	}
}

func handleQwenAssistant(raw json.RawMessage, ch chan<- Message, state *qwenStreamState) (assistantTurn, string) {
	var message qwenMessage
	if json.Unmarshal(raw, &message) != nil {
		// 正文解析不了：understood 保持 false，调用方就不拿旧回合来顶替这一条，
		// 而不是让过期的文本冒充本次输出。
		return assistantTurn{}, ""
	}
	turn := assistantTurn{understood: true}
	if message.Usage != nil && message.Model != "" {
		state.accumulateAssistantUsage(message)
	}
	var text strings.Builder
	tools := 0
	for _, block := range message.Content {
		switch block.Type {
		case "thinking":
			if block.Thinking != "" {
				trySend(ch, Message{Type: MessageThinking, Content: block.Thinking})
			}
		case "text":
			if block.Text != "" {
				text.WriteString(block.Text)
				trySend(ch, Message{Type: MessageText, Content: block.Text})
			}
		case "tool_use":
			tools++
			var input map[string]any
			if len(block.Input) > 0 {
				_ = json.Unmarshal(block.Input, &input)
			}
			trySend(ch, Message{Type: MessageToolUse, Tool: block.Name, CallID: block.ID, Input: input})
		default:
			// 我们不渲染的块类型可能正以读不懂的形状携带模型的回答，所以不能宣称
			// 这个回合是沉默的。将来认出新的"无文本"块时要在这里显式加一行，而
			// 不是靠 fall-through 碰巧得到。
			turn.understood = false
		}
	}
	turn.text = text.String()
	turn.toolUses = tools
	return turn, message.Model
}

func (s *qwenStreamState) accumulateAssistantUsage(message qwenMessage) {
	if s.fallbackUsage == nil {
		s.fallbackUsage = make(map[string]TokenUsage)
		s.seenUsageMessageIDs = make(map[string]struct{})
		s.anonymousUsage = make(map[string]TokenUsage)
	}
	current := qwenTokenUsage(message.Usage)
	var previous TokenUsage
	if message.ID != "" {
		// Qwen emits full assistant messages at finalization. Count the first
		// non-empty snapshot once by ID, keeping its counters and model together.
		if current.InputTokens == 0 && current.OutputTokens == 0 && current.CacheReadTokens == 0 {
			return
		}
		if _, seen := s.seenUsageMessageIDs[message.ID]; seen {
			return
		}
		s.seenUsageMessageIDs[message.ID] = struct{}{}
	} else {
		// Without an ID, preserve the existing latest-per-model best effort.
		previous = s.anonymousUsage[message.Model]
		s.anonymousUsage[message.Model] = current
	}
	total := s.fallbackUsage[message.Model]
	total.InputTokens += current.InputTokens - previous.InputTokens
	total.OutputTokens += current.OutputTokens - previous.OutputTokens
	total.CacheReadTokens += current.CacheReadTokens - previous.CacheReadTokens
	s.fallbackUsage[message.Model] = total
	if !s.hasResultUsage {
		s.usage = s.fallbackUsage
	}
}

func handleQwenUser(raw json.RawMessage, ch chan<- Message) {
	var message qwenMessage
	if json.Unmarshal(raw, &message) != nil {
		return
	}
	for _, block := range message.Content {
		if block.Type == "tool_result" {
			trySend(ch, Message{Type: MessageToolResult, CallID: block.ToolUseID, Output: qwenToolResultOutput(block.Content)})
		}
	}
}

// qwenTokenUsage 把 qwen 的用量结构换算成统一的 TokenUsage。
func qwenTokenUsage(usage *qwenUsage) TokenUsage {
	// Qwen includes cache reads in input_tokens on both assistant and result
	// messages. Keep the shared input and cache buckets mutually exclusive.
	return TokenUsage{
		InputTokens:     max(0, usage.InputTokens-usage.CacheReadInputTokens),
		OutputTokens:    usage.OutputTokens,
		CacheReadTokens: usage.CacheReadInputTokens,
	}
}

// qwenResultUsage 在确实有可上报用量时产出按模型索引的用量表；全零/缺模型返回 nil，
// 避免上报一条无意义的 0 用量记录。
func qwenResultUsage(usage *qwenUsage, model string) map[string]TokenUsage {
	if usage == nil || model == "" || (usage.InputTokens == 0 && usage.OutputTokens == 0 && usage.CacheReadInputTokens == 0) {
		return nil
	}
	return map[string]TokenUsage{model: qwenTokenUsage(usage)}
}

// qwenToolResultOutput 提取工具结果文本：内容可能是字符串也可能不是，先按字符串
// 解，解不动就原样返回。
func qwenToolResultOutput(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	return string(raw)
}

// qwenErrorText 从失败事件里榨取最可读的错误文本：优先 result，其次 error.message，
// 再退到 error 原始体，最后给兜底文案。
func qwenErrorText(event qwenStreamEvent) string {
	if event.Result != "" {
		return event.Result
	}
	var body struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(event.Error, &body) == nil && body.Message != "" {
		return body.Message
	}
	if len(event.Error) > 0 {
		return string(event.Error)
	}
	return "qwen returned an error event without details"
}
