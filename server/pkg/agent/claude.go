package agent

// 文件职责：本文件是 Claude Code CLI 的适配后端（Backend 实现），负责把一次任务执行变成
// “spawn `claude` 子进程 + 按流式协议收发 JSON 帧”的过程，再把 CLI 的流式输出翻译成上层
// 统一的 Message/Result 结构。
//
// 与外部 CLI 的交互：以非交互方式启动 claude，固定携带 `-p --output-format stream-json
// --input-format stream-json --verbose --permission-mode bypassPermissions`；向 stdin 写一条
// 初始 user 帧，逐行读 stdout 上的流式事件（assistant/user/system/result/log/
// control_request），遇到 control_request 自动回一个“放行”的 control_response。所有工具调用
// 走免确认模式，MCP 服务器由受管配置（--mcp-config）显式传入。
//
// 关键机制与坑：进程取消走“组内 SIGTERM→宽限期→SIGKILL”三级，绝不能在子进程还写 stdout 时
// 关读端（#5918）；stdin 在首条消息后保持打开，因为协议中途可能还要写 control_response
// （Claude stream-json 的怪癖）；stderr 同时进 daemon 日志和一个有界尾部缓冲，异常退出时把
// 最后几 KB 拼进 Result.Error，避免“只剩退出码、无法定位 V8 abort / Bun panic”的局面。
//
// 本文件后半部分是各类共享助手：命令行参数过滤（filterCustomArgs，保护 daemon↔CLI 通信协议
// flag）、--version 探测（detectCLIVersion）、MCP 临时配置文件的写与清理等。
//
// 内部组织：先是 backend 实现与 Execute（读 stdout 事件的主循环），然后是一组事件处理小函数
// 与“Claude SDK JSON 类型”（stream-json 各帧的反序列化结构），再往后是命令行/输入构造、
// resume 会话判定、环境变量过滤、参数过滤、版本探测等共享助手。
import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/multica-ai/multica/server/pkg/taskfailure"
)

// claudeTerminateGraceNanos 可选地覆盖“被取消的 claude 进程组在 SIGTERM 后被强杀(SIGKILL)前
// 的宽限期”，单位纳秒。测试里通过原子写注入；零值走默认 5s。
var claudeTerminateGraceNanos atomic.Int64

// claudeTerminateGrace 返回取消时的宽限期时长：测试注入了非零值就用它，否则默认 5 秒。
func claudeTerminateGrace() time.Duration {
	if n := claudeTerminateGraceNanos.Load(); n > 0 {
		return time.Duration(n)
	}
	return 5 * time.Second
}

// claudeBackend 实现 Backend 接口：以 `--output-format stream-json` 模式 spawn Claude Code
// CLI 并与之对话。
type claudeBackend struct {
	cfg Config
}

// Execute 启动一个 claude 子进程执行单轮提示，并返回流式消息通道与最终结果通道。
//
// 主要步骤：解析可执行路径 → 构造命令行参数（buildClaudeArgs，含 MCP 受管配置）→ 接管取消
// 语义（组内优雅关停而非立刻杀整组）→ 起 stdout 扫描 goroutine 边读边翻译事件 → 进程结束后
// 归并出最终 Result（含是否因 resume 被拒而应开新会话）。
func (b *claudeBackend) Execute(ctx context.Context, prompt string, opts ExecOptions) (*Session, error) {
	execPath := b.cfg.ExecutablePath
	if execPath == "" {
		execPath = "claude"
	}
	if _, err := exec.LookPath(execPath); err != nil {
		return nil, fmt.Errorf("claude executable not found at %q: %w", execPath, err)
	}

	timeout := opts.Timeout
	runCtx, cancel := runContext(ctx, timeout)

	args := buildClaudeArgs(opts, b.cfg.Logger)

	// 如果调用方给了 MCP 配置，就把它写进临时文件并传 --mcp-config <path>，让 agent 使用一套
	// 受控的 MCP 服务器，而不是继承外层 Claude Code 会话里的那些。
	var mcpConfigPath string
	var mcpFileCleanup func() // 本函数持有临时文件期间非 nil
	if hasManagedMcpConfig(opts.McpConfig) {
		path, err := writeMcpConfigToTemp(opts.McpConfig)
		if err != nil {
			cancel()
			return nil, err
		}
		mcpConfigPath = path
		mcpFileCleanup = func() { cleanupMcpConfigTemp(mcpConfigPath) }
		args = append(args, "--mcp-config", mcpConfigPath)
	}
	// 若在 goroutine 接管所有权之前就返回，这里负责清掉临时文件。
	defer func() {
		if mcpFileCleanup != nil {
			mcpFileCleanup()
		}
	}()

	cmd := b.cfg.commandAt(execPath).exec(runCtx, args...)
	hideAgentWindow(cmd)
	// 接管上下文取消：os/exec 默认会在 runCtx 一结束就杀掉整组进程。这里改由下面的取消
	// goroutine 驱动“组内 SIGTERM→SIGKILL”，并且只在整个进程树都被信号处理完后才关闭 stdout
	// 读端。把 cmd.Cancel 设为空函数，是防止 os/exec 用自己的 kill 和我们抢跑；WaitDelay 仍是
	// 兜底的后墙。
	cmd.Cancel = func() error { return nil }
	b.cfg.logAgentCommand(cmd, newAgentCommandLogArgs(args))
	cmd.WaitDelay = 10 * time.Second
	if opts.Cwd != "" {
		cmd.Dir = opts.Cwd
	}
	cmd.Env = buildEnv(b.cfg.Env)
	if err := claudeRootSudoPreflight(args, cmd.Env); err != nil {
		cancel()
		return nil, err
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("claude stdout pipe: %w", err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("claude stdin pipe: %w", err)
	}
	var closeStdinOnce sync.Once
	closeStdin := func() { closeStdinOnce.Do(func() { _ = stdin.Close() }) }
	// stderr 同时进 daemon 日志（和以前一样）和一个有界尾部缓冲：claude 意外退出时，能把这
	// 最后几 KB 拼进 Result.Error。没有这个尾部，一个只有退出码的失败看起来就是
	// "claude exited with error: exit status 3"——对定位 V8 abort、Bun panic 或任何 CLI 侧崩溃
	// 毫无帮助。
	stderrBuf := newStderrTail(newLogWriter(b.cfg.Logger, "[claude:stderr] "), agentStderrTailBytes)
	cmd.Stderr = stderrBuf

	if err := startOwnedProcessTree(cmd, b.cfg.Logger); err != nil {
		closeStdin()
		cancel()
		return nil, fmt.Errorf("start claude: %w", err)
	}

	b.cfg.Logger.Info("claude started", "pid", cmd.Process.Pid, "cwd", opts.Cwd, "model", opts.Model)

	// 进程已经起来了——把临时文件的清理权移交给 goroutine。
	mcpFileCleanup = nil

	msgCh := make(chan Message, 256)
	resCh := make(chan Result, 1)

	// procDone 在 cmd.Wait() 返回后关闭，让取消处理器能跳过“已经退出的进程”，避免对一个
	// 已死/被复用的 pid 发信号。
	procDone := make(chan struct{})

	// writeClaudeInput 在自己的 goroutine 里跑，这样它不会和 stdout 读取方互相死锁。
	// 在 `--verbose --output-format stream-json` 下，CLI 在读第一条 stdin 帧之前会先打一段
	// 启动横幅；如果 stdout 没人排空时我们就写 prompt，claude 会阻塞在写 stdout、永不读
	// stdin，而我们的 Write 会一直阻塞到 runCtx 触发。现场症状是恰好在该任务超时时出现
	// "write |1: The pipe has been ended."——kill 让仍然阻塞的管道失效。
	//
	// 首条 user 消息之后 stdin 保持打开。Claude 的 stream-json 协议可能在运行中途发
	// control_request 事件，期待在同一输入流上收到配对的 control_response 帧；在这里关闭
	// stdin 会让子进程一直卡在等响应，直到它自己的兜底超时。
	writeDone := make(chan error, 1)
	go func() {
		err := writeClaudeInput(stdin, prompt)
		if err != nil {
			closeStdin()
		}
		writeDone <- err
	}()

	go func() {
		defer cancel()
		defer close(msgCh)
		defer close(resCh)
		if mcpConfigPath != "" {
			defer cleanupMcpConfigTemp(mcpConfigPath)
		}

		startTime := time.Now()
		var lastAssistantText string
		var finalResultText string
		sawResult := false
		resultIsError := false
		terminalReasonError := ""
		var sessionID string
		sawAsyncLaunch := false
		usage := make(map[string]TokenUsage)
		eventCount := 0
		invalidEventCount := 0
		assistantEventCount := 0
		toolUseCount := 0
		unreadableAssistantCount := 0

		// 取消/超时时：先让 claude（以及它 spawn 的每个 MCP server、工具子进程）退出，再解除
		// scanner 的阻塞。做法是 EOF stdin 催它干净退出 → 对整组发 SIGTERM → 给一段宽限期 →
		// 组里还有成员存活就 SIGKILL 整组。SIGKILL 不可捕获，一旦送达组内任何成员都无法再写，
		// 只有到这时才适合关闭 stdout 读端——这是对一个仍被卡死后代占着 stdout 的 scanner 的
		// 最后一招解锁。WaitDelay 是最终兜底（#5918）。
		go func() {
			select {
			case <-procDone:
				return // 进程自己跑完了，无需终止
			case <-runCtx.Done():
			}
			closeStdin()
			if cmd.Process != nil {
				signalProcessGroup(cmd, syscall.SIGTERM)
				// 除非整个进程组在宽限期内退出，否则升级为组级 SIGKILL。这里必须以进程组（而非
				// procDone）为判据：procDone 只表示组长 cmd.Wait() 已返回，一个忽略 SIGTERM、
				// 又不持有 claude stdout 的后代可以让组长先退出、关上 procDone、从而跳过
				// SIGKILL——正好泄漏掉本修复要消灭的那个孤儿。waitProcessGroupGone 在组一空就
				// 返回，所以顺利退出时这步不增加延迟。
				if !waitProcessGroupGone(cmd, claudeTerminateGrace()) {
					signalProcessGroup(cmd, syscall.SIGKILL)
				}
			}
			_ = stdout.Close()
		}()

		scanner := newAgentStreamScanner(stdout)

		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}

			var msg claudeSDKMessage
			if err := json.Unmarshal([]byte(line), &msg); err != nil {
				invalidEventCount++
				continue
			}
			eventCount++

			switch msg.Type {
			case "assistant":
				assistantEventCount++
				turn := b.handleAssistant(msg, msgCh, usage)
				toolUseCount += turn.toolUses
				if !turn.understood {
					unreadableAssistantCount++
				}
				lastAssistantText = turn.resolveFallback(lastAssistantText)
			case "user":
				if b.handleUser(msg, msgCh) {
					sawAsyncLaunch = true
				}
			case "system":
				if msg.SessionID != "" {
					sessionID = msg.SessionID
				}
				trySend(msgCh, Message{Type: MessageStatus, Status: "running", SessionID: sessionID})
			case "result":
				sawResult = true
				finalResultText = msg.ResultText
				resultIsError = msg.IsError
				terminalReasonError = claudeTerminalReasonFailure(msg.TerminalReason, msg.ResultText)
				sessionID = msg.SessionID
				if resultUsage := claudeResultUsage(msg, opts.Model); len(resultUsage) > 0 {
					usage = resultUsage
				}
				closeStdin()
			case "log":
				if msg.Log != nil {
					trySend(msgCh, Message{
						Type:    MessageLog,
						Level:   msg.Log.Level,
						Content: msg.Log.Message,
					})
				}
			case "control_request":
				b.handleControlRequest(msg, stdin)
			}
		}
		scanErr := scanner.Err()
		if scanErr != nil {
			// scanner 停止消费 stdout 了。在 Wait 前关闭管道，防止仍有一个子进程在往写满的
			// OS 管道里写畸形/超大事件而卡死；scanErr 仍是首要失败原因。
			_ = stdout.Close()
		}

		closeStdin()

		// 等进程退出，然后放开取消处理器。
		exitErr := cmd.Wait()
		close(procDone)
		// 组长已回收，放弃所有权。在 Windows 上这会关闭 Job Object，杀死里面一切仍在运行的
		// 东西——对“比 CLI 活得还久的后代”来说这正是应得的下场（GH #7522）。
		releaseProcessGroup(cmd)
		duration := time.Since(startTime)
		// writeDone 是带缓冲的（容量 1）且 writer 一定会发送——cmd 退出时，prompt 写入要么已
		// 成功、要么打到坏管道、要么被终结 cmd 的那次 kill 解除了阻塞。
		writeErr := <-writeDone

		completionGuardError := ""
		if sawAsyncLaunch {
			completionGuardError = "claude launched an async background task; Multica-managed runs require foreground execution"
		}
		finalStatus, finalOutput, finalError := finalizeStreamResult(
			"claude",
			timeout,
			runCtx.Err(),
			writeErr,
			exitErr,
			sessionID,
			streamTerminalState{
				lastAssistantText:   lastAssistantText,
				finalResultText:     finalResultText,
				sawResult:           sawResult,
				resultIsError:       resultIsError,
				scanErr:             scanErr,
				terminalReasonError: terminalReasonError,
			},
			completionGuardError,
		)

		// cmd.Wait() 已返回——os/exec 的 stderr 拷贝 goroutine 已观察到 claude 退出前写向
		// stderr 的每一个字节，所以现在取 stderrBuf.Tail() 是安全的。把尾部拼到非空的失败消息
		// 上；上游调用方会把它展示成任务的 error 字段，那是用户唯一能看到它的地方。
		stderrTail := stderrBuf.Tail()
		if finalError != "" {
			finalError = withAgentStderr(finalError, "claude", stderrTail)
		}
		logStreamProtocolObservation(b.cfg.Logger, streamProtocolObservation{
			provider:                   "claude",
			cliVersion:                 b.cfg.CLIVersion,
			model:                      opts.Model,
			exitCode:                   streamProcessExitCode(exitErr),
			eventCount:                 eventCount,
			invalidEventCount:          invalidEventCount,
			assistantEventCount:        assistantEventCount,
			toolUseCount:               toolUseCount,
			sawResult:                  sawResult,
			resultIsError:              resultIsError,
			resultBytes:                len(finalResultText),
			lastAssistantBytes:         len(lastAssistantText),
			scannerError:               scanErr != nil,
			unreadableAssistantCount:   unreadableAssistantCount,
			anthropicBaseURLConfigured: strings.TrimSpace(b.cfg.Env["ANTHROPIC_BASE_URL"]) != "",
		})

		b.cfg.Logger.Info("claude finished", "pid", cmd.Process.Pid, "status", finalStatus, "duration", duration.Round(time.Millisecond).String())

		// “账号绑定”那个 400 会出现在 result 事件里（finalError）；"no conversation found"
		// 则打印到 stderr。两个来源都要查。
		resumeRejected := resumeWasRejected(opts.ResumeSessionID, sessionID, finalStatus == "failed", finalError, stderrTail)
		reportedSessionID := resolveSessionID(opts.ResumeSessionID, sessionID, finalStatus == "failed", finalError, stderrTail)
		if resumeRejected {
			b.cfg.Logger.Info("claude resume was rejected; dropping session id and signalling fresh-session retry",
				"requested_resume", opts.ResumeSessionID,
				"emitted_session", sessionID,
			)
		}

		resCh <- Result{
			Status:         finalStatus,
			Output:         finalOutput,
			Error:          finalError,
			DurationMs:     duration.Milliseconds(),
			SessionID:      reportedSessionID,
			Usage:          usage,
			ResumeRejected: resumeRejected,
		}
	}()

	return &Session{Messages: msgCh, Result: resCh}, nil
}

// handleAssistant 把一条 assistant 帧翻译成对外消息：正文文本 → MessageText、thinking 文本 →
// MessageThinking、tool_use 块 → MessageToolUse，并按模型累计 token 用量。返回的 assistantTurn
// 记录本回合是否读懂（understood）、正文、工具调用数。
func (b *claudeBackend) handleAssistant(msg claudeSDKMessage, ch chan<- Message, usage map[string]TokenUsage) assistantTurn {
	var content claudeMessageContent
	if err := json.Unmarshal(msg.Message, &content); err != nil {
		// 正文读不懂：让 understood 保持 false，这样调用方会丢弃兜底逻辑，而不会让更早的一
		// 回合顶替这一回合。
		return assistantTurn{}
	}
	turn := assistantTurn{understood: true}
	var assistantText strings.Builder
	toolUseCount := 0

	// 按模型累计 token 用量。
	if content.Usage != nil && content.Model != "" {
		u := usage[content.Model]
		u.InputTokens += content.Usage.InputTokens
		u.OutputTokens += content.Usage.OutputTokens
		u.CacheReadTokens += content.Usage.CacheReadInputTokens
		u.CacheWriteTokens += content.Usage.CacheCreationInputTokens
		usage[content.Model] = u
	}

	for _, block := range content.Content {
		switch block.Type {
		case "text":
			if block.Text != "" {
				assistantText.WriteString(block.Text)
				trySend(ch, Message{Type: MessageText, Content: block.Text})
			}
		case "thinking":
			if block.Text != "" {
				trySend(ch, Message{Type: MessageThinking, Content: block.Text})
			}
		case "tool_use":
			toolUseCount++
			var input map[string]any
			if block.Input != nil {
				_ = json.Unmarshal(block.Input, &input)
			}
			trySend(ch, Message{
				Type:   MessageToolUse,
				Tool:   block.Name,
				CallID: block.ID,
				Input:  input,
			})
		default:
			// 一个我们不渲染的块类型，可能正以一种我们读不懂的形状承载着模型的答案，所以不能
			// 声称这一回合是沉默的。识别新的“无文本”块是有意加在这里的一行逻辑，而不是 fall
			// through 的偶然产物。
			turn.understood = false
		}
	}
	turn.text = assistantText.String()
	turn.toolUses = toolUseCount
	return turn
}

// handleUser 把一条 user 帧翻译成工具结果消息（MessageToolResult）；若结果里出现异步任务
// 启动标记则返回 true（调用方据此判定该运行不应继续）。
func (b *claudeBackend) handleUser(msg claudeSDKMessage, ch chan<- Message) bool {
	var content claudeMessageContent
	if err := json.Unmarshal(msg.Message, &content); err != nil {
		return false
	}

	sawAsyncLaunch := false
	for _, block := range content.Content {
		if block.Type == "tool_result" {
			resultStr := ""
			if block.Content != nil {
				resultStr = string(block.Content)
				if claudeToolResultHasAsyncLaunch(block.Content) {
					sawAsyncLaunch = true
				}
			}
			trySend(ch, Message{
				Type:   MessageToolResult,
				CallID: block.ToolUseID,
				Output: resultStr,
			})
		}
	}
	return sawAsyncLaunch
}

// handleControlRequest 应答一条 control_request 帧：在自主/daemon 模式下自动放行（allow）所有
// 工具调用，并把强制前台执行的工具输入改写一并回传。
func (b *claudeBackend) handleControlRequest(msg claudeSDKMessage, stdin interface{ Write([]byte) (int, error) }) {
	var req claudeControlRequestPayload
	if err := json.Unmarshal(msg.Request, &req); err != nil {
		return
	}

	var inputMap map[string]any
	if req.Input != nil {
		_ = json.Unmarshal(req.Input, &inputMap)
	}
	if inputMap == nil {
		inputMap = map[string]any{}
	}
	if forceClaudeToolInputForeground(inputMap) {
		b.cfg.Logger.Info("claude: forced foreground tool execution",
			"request_id", msg.RequestID,
			"tool", req.ToolName,
		)
	}

	response := map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype":    "success",
			"request_id": msg.RequestID,
			"response": map[string]any{
				"behavior":     "allow",
				"updatedInput": inputMap,
			},
		},
	}

	data, err := json.Marshal(response)
	if err != nil {
		b.cfg.Logger.Warn("claude: failed to marshal control response", "error", err)
		return
	}
	data = append(data, '\n')
	if _, err := stdin.Write(data); err != nil {
		b.cfg.Logger.Warn("claude: failed to write control response", "error", err)
	}
}

// forceClaudeToolInputForeground 把工具输入里的 run_in_background 强制改回 false：Multica
// 托管运行要求前台执行，不能让工具自己转后台。返回是否改写过。
func forceClaudeToolInputForeground(input map[string]any) bool {
	if runInBackground, ok := input["run_in_background"].(bool); ok && runInBackground {
		input["run_in_background"] = false
		return true
	}
	return false
}

// claudeToolResultHasAsyncLaunch 判断一段工具结果里是否出现了“异步任务已启动”的标记
// （status=="async_launched"）。结果可能是对象、对象数组或带 content 的对象，这里递归地找。
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

// claudeArrayHasAsyncLaunchStatus 在一组元素里找 status=="async_launched" 的对象。
func claudeArrayHasAsyncLaunchStatus(values []any) bool {
	for _, value := range values {
		if item, ok := value.(map[string]any); ok && claudeMapHasAsyncLaunchStatus(item) {
			return true
		}
	}
	return false
}

// claudeMapHasAsyncLaunchStatus 判断单个对象是否带 status=="async_launched" 标记。
func claudeMapHasAsyncLaunchStatus(value map[string]any) bool {
	status, ok := value["status"].(string)
	return ok && status == "async_launched"
}

// ── Claude SDK JSON 类型（stream-json 协议帧）──

// claudeSDKMessage 是 claude `--output-format stream-json` 输出里每一行 JSON 的通用外壳。
// 实际语义由 Type 决定，不同帧类型用到的字段也不一样（见下方按用途分组注释）。
type claudeSDKMessage struct {
	Type      string          `json:"type"`
	Message   json.RawMessage `json:"message,omitempty"`
	Subtype   string          `json:"subtype,omitempty"`
	SessionID string          `json:"session_id,omitempty"`
	Model     string          `json:"model,omitempty"`

	// result 帧字段
	ResultText string `json:"result,omitempty"`
	IsError    bool   `json:"is_error,omitempty"`
	// TerminalReason 是 Claude Code 对自己为何结束这一轮的结构化陈述。要与 IsError 分开读，
	// 因为 CLI 是独立计算这两个值的——见 claudeTerminalReasonFailure。
	TerminalReason string                            `json:"terminal_reason,omitempty"`
	DurationMs     float64                           `json:"duration_ms,omitempty"`
	NumTurns       int                               `json:"num_turns,omitempty"`
	Usage          *claudeUsage                      `json:"usage,omitempty"`
	ModelUsage     map[string]claudeResultModelUsage `json:"modelUsage,omitempty"`

	// log 帧字段
	Log *claudeLogEntry `json:"log,omitempty"`

	// control request 帧字段
	RequestID string          `json:"request_id,omitempty"`
	Request   json.RawMessage `json:"request,omitempty"`
}

// claudeLogEntry 是 log 帧的载荷：一条带级别的日志消息。
type claudeLogEntry struct {
	Level   string `json:"level"`
	Message string `json:"message"`
}

// claudeMessageContent 是 assistant/user 帧里 `message` 字段的结构：一组内容块，外加角色、
// 模型与该消息的 token 用量。
type claudeMessageContent struct {
	Role    string               `json:"role"`
	Model   string               `json:"model"`
	Content []claudeContentBlock `json:"content"`
	Usage   *claudeUsage         `json:"usage,omitempty"`
}

// claudeUsage 是助手消息自带的 token 用量（stream-json 帧内嵌的 usage 对象，字段为
// snake_case）。缓存 token 分读/写两部分。
type claudeUsage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
}

// claudeResultModelUsage 是 result 帧按模型列出的 token 用量（camelCase 版本）。
type claudeResultModelUsage struct {
	InputTokens              int64 `json:"inputTokens"`
	OutputTokens             int64 `json:"outputTokens"`
	CacheReadInputTokens     int64 `json:"cacheReadInputTokens"`
	CacheCreationInputTokens int64 `json:"cacheCreationInputTokens"`
}

// claudeTerminalReasonFailure 当 Claude Code 的结构化 terminal_reason 表示“这一轮并没有真正
// 产出答案”时把它变成错误字符串；否则返回 ""。
//
// 只认 prompt_too_long，而且是有意为之。CLI 能上报的其余 terminal reason，要么本来就带着
// is_error 到达（api_error、model_error、error_max_turns…），要么就是一次正常完成——放宽这个
// 判断等于去质疑一个本来就正常工作的契约；而 prompt_too_long 是观察到的“明明该失败却以干净
// 成功状态到达平台”的唯一案例（GH #6402）。CLI 用“它渲染的最后一条消息是不是 API 错误”来算
// is_error、用“为什么这一轮停下”来算 terminal_reason；当压缩（compaction）失败、而运行又恰好
// 结束在一条本身没被打标记的消息上时，两者不一致，只有 terminal_reason 说的是真话。
//
// 返回文本里引用枚举 token，这样 taskfailure.Classify 能把它归类到
// agent_error.context_overflow，而不依赖 CLI 的散文——后者随版本变化、某些形态下还是空的。
// 这个 reason 在 resume 黑名单上（GetLastTaskSession / GetLastChatTaskSession），于是塞满的
// 会话被退役，issue 上的下一个任务从干净状态重新开始——这正是 #6402 报告的“存档记录”变通
// 方案的自动化形态。
func claudeTerminalReasonFailure(terminalReason, resultText string) string {
	if strings.TrimSpace(terminalReason) != taskfailure.TerminalReasonPromptTooLong {
		return ""
	}
	msg := "claude ended the turn with terminal_reason=" + taskfailure.TerminalReasonPromptTooLong +
		": the session's context window is exhausted and compaction could not recover it"
	if detail := strings.TrimSpace(resultText); detail != "" {
		msg += " (" + detail + ")"
	}
	return msg
}

// claudeResultUsage 从 result 帧抽取按模型归类的用量。优先用帧里的 ModelUsage（按模型分开、
// 且可能含多个模型）；缺失或全空时退回消息自带的 Usage，用 msg.Model（缺省再退回调用方给的
// fallbackModel）做键。
func claudeResultUsage(msg claudeSDKMessage, fallbackModel string) map[string]TokenUsage {
	if len(msg.ModelUsage) > 0 {
		usage := make(map[string]TokenUsage, len(msg.ModelUsage))
		for model, u := range msg.ModelUsage {
			if model == "" || !claudeUsageHasTokens(u.InputTokens, u.OutputTokens, u.CacheReadInputTokens, u.CacheCreationInputTokens) {
				continue
			}
			usage[model] = TokenUsage{
				InputTokens:      u.InputTokens,
				OutputTokens:     u.OutputTokens,
				CacheReadTokens:  u.CacheReadInputTokens,
				CacheWriteTokens: u.CacheCreationInputTokens,
			}
		}
		if len(usage) > 0 {
			return usage
		}
	}

	model := msg.Model
	if model == "" {
		model = fallbackModel
	}
	if msg.Usage == nil || model == "" || !claudeUsageHasTokens(
		msg.Usage.InputTokens,
		msg.Usage.OutputTokens,
		msg.Usage.CacheReadInputTokens,
		msg.Usage.CacheCreationInputTokens,
	) {
		return nil
	}
	return map[string]TokenUsage{
		model: {
			InputTokens:      msg.Usage.InputTokens,
			OutputTokens:     msg.Usage.OutputTokens,
			CacheReadTokens:  msg.Usage.CacheReadInputTokens,
			CacheWriteTokens: msg.Usage.CacheCreationInputTokens,
		},
	}
}

// claudeUsageHasTokens 判断一组用量里是否至少有一个维度 >0，用来筛掉全零的“空用量”帧。
func claudeUsageHasTokens(input, output, cacheRead, cacheWrite int64) bool {
	return input > 0 || output > 0 || cacheRead > 0 || cacheWrite > 0
}

// claudeContentBlock 是消息内容里的单个块：文本、thinking 或工具调用（tool_use / tool_result）
// 都共用一个结构，靠 Type 区分、按需取字段。
type claudeContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
}

// claudeControlRequestPayload 是 control_request 帧请求体的结构：声明请求的子类型、涉及的
// 工具名与（可能被改写后放行的）工具输入。
type claudeControlRequestPayload struct {
	Subtype  string          `json:"subtype"`
	ToolName string          `json:"tool_name,omitempty"`
	Input    json.RawMessage `json:"input,omitempty"`
}

// ── 共享助手 ──

// trySend 尝试往通道发一条消息；通道已满（消费者跟不上）就丢弃。Result.Output 是独立定稿的，
// 所以受影响只限于“实时会话转播”的接收方。
func trySend(ch chan<- Message, msg Message) {
	select {
	case ch <- msg:
	default:
	}
}

// claudeBlockedArgs 是 daemon 写死的 flag，用户配置的 custom_args 不得覆盖。覆盖这些会破坏
// daemon↔Claude 之间的通信协议。
var claudeBlockedArgs = map[string]blockedArgMode{
	"-p":                       blockedStandalone, // 非交互模式
	"--output-format":          blockedWithValue,  // stream-json 协议
	"--input-format":           blockedWithValue,  // stream-json 协议
	"--permission-mode":        blockedWithValue,  // bypassPermissions，自主运行所需
	"--mcp-config":             blockedWithValue,  // 由 daemon 按 agent.mcp_config 设置
	"--tools":                  blockedWithValue,
	"--allowedTools":           blockedWithValue,
	"--disallowedTools":        blockedList,
	"--strict-mcp-config":      blockedStandalone,
	"--no-session-persistence": blockedStandalone,
	// `--effort` 归“按 agent 的 thinking_level 选择器”管，用户塞在 custom_args 里的同名单参数
	// 不能悄悄把它否决。daemon 只在 opts.ThinkingLevel 设置时才注入 --effort；即使用户仍然在
	// custom_args 里写了它，我们也丢弃重复值并记一条警告，而不是让 CLI 收到两个互相冲突的
	// --effort。
	"--effort": blockedWithValue,
}

// buildClaudeArgs 把 ExecOptions 组装成 claude 的命令行参数，并过滤掉被协议占用的 flag。
func buildClaudeArgs(opts ExecOptions, logger *slog.Logger) []string {
	args := []string{
		"-p",
		"--output-format", "stream-json",
		"--input-format", "stream-json",
		"--verbose",
		"--permission-mode", "bypassPermissions",
		// AskUserQuestion 是 Claude Code 内置的交互式提问工具。daemon 以非交互的 stream-json
		// 模式运行 Claude，没有供 prompt 渲染的 UI，所以一次调用只会得到一个空答案，agent 会在
		// 用户毫不知情的情况下“自行推断”（见 GitHub #2588）。面向用户的澄清应放进 issue 评论。
		"--disallowedTools", "AskUserQuestion",
	}
	if opts.LifeCognition {
		// 生命周期认知（Life cognition）模式不得继承编码工具或 provider 会话状态。MCP 仍可
		// 通过显式受管的 Life server 使用。
		args = append(args, "--tools", "", "--strict-mcp-config", "--no-session-persistence")
	}
	if hasManagedMcpConfig(opts.McpConfig) {
		// 已保存的 agent 级配置是权威的，哪怕它是一个显式的空对象。没有受管配置时不要开
		// strict 模式，让 Claude 可以继承用户本地运行时配置的 MCP 服务器。
		args = append(args, "--strict-mcp-config")
	}
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}
	if opts.ThinkingLevel != "" {
		// 紧挨着 --model 放，让“按会话的 effort”作用于参数所宣传的同一个模型选择；CLI 本身
		// 接受任意顺序，但这种排法让 `agent command` 日志里的启动行更易读。
		args = append(args, "--effort", opts.ThinkingLevel)
	}
	if opts.MaxTurns > 0 {
		args = append(args, "--max-turns", fmt.Sprintf("%d", opts.MaxTurns))
	}
	// SystemPrompt 刻意不转成 --append-system-prompt 转发：Claude Code 会加载 daemon 写进
	// 工作目录的按任务 CLAUDE.md，再把同一份运行时简报内联进去，等于每一轮都重复一遍。已对
	// Claude Code 2.1.220 验证（MUL-5392）。
	if opts.ResumeSessionID != "" {
		args = append(args, "--resume", opts.ResumeSessionID)
	}
	blockedArgs := claudeBlockedArgs
	if opts.ClaudeSettingsPath != "" {
		// daemon 持有的 --settings 文件是“禁用被继承 skills”的执行层。只在策略生效时剔除与它
		// 竞争的按 agent/默认 flag，然后把受管文件放到最后。
		blockedArgs = make(map[string]blockedArgMode, len(claudeBlockedArgs)+1)
		for key, mode := range claudeBlockedArgs {
			blockedArgs[key] = mode
		}
		blockedArgs["--settings"] = blockedWithValue
	}
	args = append(args, filterCustomArgs(opts.ExtraArgs, blockedArgs, logger)...)
	args = append(args, filterCustomArgs(opts.CustomArgs, blockedArgs, logger)...)
	if opts.ClaudeSettingsPath != "" {
		args = append(args, "--settings", opts.ClaudeSettingsPath)
	}
	return args
}

// writeClaudeInput 构造并写入 claude 的 stdin 初始帧。
func writeClaudeInput(w io.Writer, prompt string) error {
	data, err := buildClaudeInput(prompt)
	if err != nil {
		return err
	}
	if _, err := w.Write(data); err != nil {
		return err
	}
	return nil
}

// buildClaudeInput 把用户提示打包成 stream-json 的 user 帧：{"type":"user","message":{...}}。
func buildClaudeInput(prompt string) ([]byte, error) {
	payload := map[string]any{
		"type": "user",
		"message": map[string]any{
			"role": "user",
			"content": []map[string]string{
				{
					"type": "text",
					"text": prompt,
				},
			},
		},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal claude input: %w", err)
	}
	return append(data, '\n'), nil
}

// resumeRejectedPhrases 是能正面判定“resume 被拒”的 provider 消息片段——区别于“碰巧在续跑
// 期间发生的失败”。stream-json 后端（claude、codebuddy、qwen）共用这份清单；ACP 后端则改用
// isACPSessionNotFound 匹配结构化错误码。
//
// 匹配必须保持严格：误报会让 daemon 丢掉一个可恢复的会话指针并重跑任务，所以任何有歧义的东西
// 都不该进这份清单。
var resumeRejectedPhrases = []string{
	// 已核实：--resume 指向的记录不存在时 claude 打印这句。TestResolveSessionID 现有的
	// stderr fixture 覆盖到它。
	"no conversation found",
	// 已核实：qwen-code 0.20.0，捕获于
	// testdata/qwen-code-0.20.0-resume-not-found.stderr.txt —— "No saved
	// session found with ID <id>. Run `qwen --resume` without an ID to
	// choose from existing sessions."
	"no saved session found",
	// 在 multica-ai/multica#5704 里对 Claude Code 2.1.207（zh-CN）逐字上报：
	// "400 此 session 已绑定另外的ai账号，请执行 /new 开启新 session"。这正是该信号要拦截的
	// “账号切换”护栏。
	"已绑定另外",
	// 推断而来、尚未观察到：同一条护栏的英文措辞还没有被捕获到。这些是根据 zh-CN 文案猜的，
	// 等有真实英文样本后应修正。这里漏判会退化成“带着 provider 原始错误的终态失败”，仍然可
	// 诊断——它不会悄悄把运行带错方向。
	"bound to another account",
	"bound to a different account",
}

// resumeWasRejected 判断一次失败运行的 --resume 是否本身被拒绝。它是 Result.ResumeRejected
// 背后的“正向证据”谓词：只有 resume 被拒才值得用全新会话重跑一次来治愈。
//
// texts 应包含 provider 可能写下原因的所有位置——最终错误串与 stderr 尾部——因为“账号绑定”
// 400 出现在 stream-json 的 result 事件里，而 "no conversation found" 打印在 stderr。
func resumeWasRejected(requestedResume, emitted string, failed bool, texts ...string) bool {
	if !failed || requestedResume == "" {
		return false
	}
	for _, text := range texts {
		lower := strings.ToLower(text)
		for _, phrase := range resumeRejectedPhrases {
			if strings.Contains(lower, phrase) {
				return true
			}
		}
	}
	// Claude 回答的会话与我们要它继续的会话不同：说明被请求的那份记录没有加载成功。
	return emitted != "" && emitted != requestedResume
}

// resolveSessionID 决定在 Result 上报哪个会话 id。已知已死的会话绝不能当 resume 指针持久化，
// 所以 resume 被拒时无论 claude 回显了什么，这里都上报 ""。
//
// 它刻意不再是 daemon 判断“这次运行为什么失败”所读的信号——那是 Result.ResumeRejected 的
// 职责。空 SessionID 也会由“首条流消息之前就失败”（401、二进制缺失）产生，这正是过去从空
// SessionID 推断“resume 被拒”两个方向都错的原因。
func resolveSessionID(requestedResume, emitted string, failed bool, texts ...string) string {
	if resumeWasRejected(requestedResume, emitted, failed, texts...) {
		return ""
	}
	return emitted
}

// buildEnv 由 daemon 自带环境 + 任务级额外变量组成子进程环境。
func buildEnv(extra map[string]string) []string {
	return mergeEnv(os.Environ(), extra)
}

// claudeRootSudoPreflight 在 root/sudo 下拦下“以 bypassPermissions 权限模式运行 Claude”的
// 尝试：Claude Code 在这种权限组合下会拒绝启动。若在真正的容器/沙箱里（IS_SANDBOX=1）则放行。
func claudeRootSudoPreflight(args, env []string) error {
	if !argsRequestBypassPermissions(args) || os.Geteuid() != 0 || envHasSandbox(env) {
		return nil
	}
	return fmt.Errorf("Claude Code refuses bypassPermissions under root/sudo privileges. Run the Multica daemon as a non-root user, or set IS_SANDBOX=1 if running in a genuine container/sandbox")
}

// argsRequestBypassPermissions 判断命令行是否请求了免确认权限模式
// （--dangerously-skip-permissions 或 --permission-mode bypassPermissions）。
func argsRequestBypassPermissions(args []string) bool {
	for i, arg := range args {
		if arg == "--dangerously-skip-permissions" {
			return true
		}
		if arg == "--permission-mode" && i+1 < len(args) && args[i+1] == "bypassPermissions" {
			return true
		}
	}
	return false
}

// envHasSandbox 判断环境里 IS_SANDBOX 是否被置为真值（1/true/yes/on，大小写不敏感）。
func envHasSandbox(env []string) bool {
	for i := len(env) - 1; i >= 0; i-- {
		key, value, ok := strings.Cut(env[i], "=")
		if key != "IS_SANDBOX" {
			continue
		}
		if !ok {
			return false
		}
		switch strings.ToLower(value) {
		case "1", "true", "yes", "on":
			return true
		default:
			return false
		}
	}
	return false
}

// mergeEnv 合并基础环境与额外变量：基础环境里被过滤的内部键与 MULTICA_* 命名空间会被剔除，
// 额外变量直接追加（覆盖同名项）。
func mergeEnv(base []string, extra map[string]string) []string {
	env := make([]string, 0, len(base)+len(extra))
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		// daemon 自己环境里的 MULTICA_* 不是任务上下文。对所有后端都丢掉继承下来的这一
		// 命名空间，只追加下面 daemon.go 为这个任务显式拼好的值。
		if isFilteredChildEnvKey(key) || strings.HasPrefix(strings.ToUpper(key), "MULTICA_") {
			continue
		}
		env = append(env, entry)
	}
	for k, v := range extra {
		env = append(env, k+"="+v)
	}
	return env
}

// isFilteredChildEnvKey 判断一个继承来的环境变量是不是“内部 Claude Code 运行时/会话标记”，
// 是则绝不能漏给 spawn 出来的子进程（否则子进程会把自己误认成嵌套/续跑会话，或继承父进程的
// exec 路径/传输方式）。
//
// 它绝不能剥掉用户可见的 CLAUDE_CODE_* 配置命名空间（CLAUDE_CODE_GIT_BASH_PATH、
// CLAUDE_CODE_USE_BEDROCK、CLAUDE_CODE_USE_VERTEX、CLAUDE_CODE_MAX_OUTPUT_TOKENS、
// CLAUDE_CODE_TMPDIR、…）：那些是用户刻意设置的、子进程需要它们。当年一刀切剥掉整个前缀就
// 把 Windows 弄坏过——CLAUDE_CODE_GIT_BASH_PATH 被悄悄移除，Claude Code 找不到 bash.exe、
// 立刻退出。这里按精确名字剥内部标记，其余 CLAUDE_CODE_* 一律放行。
//
// 黑名单只放未写入文档的、按进程的运行时标记。公开 env-var 参考
// （https://code.claude.com/docs/en/env-vars）里出现的任何东西都是用户配置，不得进这份名单
// ——包括 CLAUDE_CODE_TMPDIR，一个有文档的临时目录覆盖项，Claude Code 会在其下自建按会话的
// 子目录，继承它没有危害。
func isFilteredChildEnvKey(key string) bool {
	switch key {
	case "CLAUDECODE", // 值为 "1" 时表示“正在 Claude Code 内运行”
		"CLAUDE_CODE_ENTRYPOINT", // 入口点标记（cli/sdk-cli/…）
		"CLAUDE_CODE_EXECPATH",   // 当前运行中 CLI 二进制的路径
		"CLAUDE_CODE_SESSION_ID", // 按会话的标识符
		"CLAUDE_CODE_SSE_PORT":   // IDE 扩展的传输端口
		return true
	}
	// CLAUDECODE_*（CLAUDE 与 CODE 之间没有下划线）完全内部，继续剥。用户可见的配置命名空间是
	// CLAUDE_CODE_*。
	return strings.HasPrefix(key, "CLAUDECODE_")
}

// blockedArgMode 指明一个被拦的 flag 是带值参数还是独立开关。
type blockedArgMode int

const (
	blockedWithValue     blockedArgMode = iota // flag 带值（下个参数或 =value 形式）
	blockedStandalone                          // flag 是布尔开关，不带值
	blockedOptionalValue                       // flag 可选带值：可能是下个非 flag 参数或 =value
	blockedList                                // flag 吞掉其后所有非 flag 的值
)

// filterCustomArgs 从用户配置的 custom args 里剔掉协议关键 flag，防止破坏 daemon↔agent 通信。
// 每个后端定义自己的拦阻集（它硬编码的 flag）。这里刻意收得窄——只拦会破坏通信协议的参数，
// 而不是每个可能的危险 flag。工作区成员被信任能合理配置 agent，与 custom_env 同理。
//
// 处理每个参数前先剥掉 shell 引号：用户常用 shell 语法在配置字段里写 custom_args（例如
// --deny-tool='write'）。daemon 不经 shell 直接 spawn 进程，若不剥引号，这些引号会被逐字传给
// 子进程，而后者通常会当作不认识的 flag 值拒收。
func filterCustomArgs(args []string, blocked map[string]blockedArgMode, logger *slog.Logger) []string {
	if len(args) == 0 {
		return args
	}
	filtered := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		raw := args[i]
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
			if mode == blockedList && !hasInlineValue {
				for i+1 < len(args) && !strings.HasPrefix(unshellQuoteArg(args[i+1]), "-") {
					i++
				}
			} else if mode == blockedWithValue && !hasInlineValue {
				// 下一个参数就是这个 flag 的值——一并跳过。
				i++
			} else if mode == blockedOptionalValue && !hasInlineValue && i+1 < len(args) &&
				!strings.HasPrefix(unshellQuoteArg(args[i+1]), "-") {
				// 可选值只在“下一个 token 不是另一个 flag”时才被消费，这样布尔形态不会吞掉
				// 一个无关选项。
				i++
			}
			continue
		}
		filtered = append(filtered, arg)
	}
	return filtered
}

// unshellQuoteArg 从参数上剥掉一层 shell 风格的单/双引号。处理两种形态：
//
//   - --flag='value' 或 --flag="value" → --flag=value
//   - 'standalone' 或 "standalone"     → standalone
//
// 只有 flag 形态（`-x=…`、`--flag=…`）会做内联值去引号。像 `model="o3"` 这种普通赋值语法
// 不去动，因为引号对子进程可能是有语义的（例如 Codex `-c model="o3"`）。只剥匹配的外层引号，
// 不做任何转义处理。
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

// stripSurroundingQuotes 若 s 以同一对单/双引号开头和结尾，剥掉并返回 (去引号结果, true)；
// 否则返回 (s, false)。
func stripSurroundingQuotes(s string) (string, bool) {
	if len(s) >= 2 {
		if (s[0] == '\'' && s[len(s)-1] == '\'') || (s[0] == '"' && s[len(s)-1] == '"') {
			return s[1 : len(s)-1], true
		}
	}
	return s, false
}

// writeMcpConfigToTemp 把 MCP 配置 JSON 写进临时文件并返回其路径。调用方负责通过
// cleanupMcpConfigTemp 删除。
func writeMcpConfigToTemp(raw json.RawMessage) (string, error) {
	dir, err := os.MkdirTemp("", "multica-mcp-*")
	if err != nil {
		return "", fmt.Errorf("create mcp config temp dir: %w", err)
	}
	data, err := hardenBrowserMcpConfig(raw, dir)
	if err != nil {
		cleanupMcpConfigTemp(filepath.Join(dir, "mcp-config.json"))
		return "", err
	}
	path := filepath.Join(dir, "mcp-config.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		cleanupMcpConfigTemp(path)
		return "", fmt.Errorf("write mcp config temp file: %w", err)
	}
	return path, nil
}

// cleanupMcpConfigTemp 删除 MCP 配置临时文件；若它位于我们自己的 multica-mcp-* 临时目录里，
// 直接整目录删除（含目录里由 hardenBrowserMcpConfig 落地的其他文件）。
func cleanupMcpConfigTemp(path string) {
	if path == "" {
		return
	}
	dir := filepath.Dir(path)
	if strings.HasPrefix(filepath.Base(dir), "multica-mcp-") {
		_ = os.RemoveAll(dir)
		return
	}
	_ = os.Remove(path)
}

// detectVersionTimeout 约束单次 `<cli> --version` 探测。版本探测跑在 daemon 的阻塞式预检里
// （registerRuntimesForWorkspace），所以一个对 `--version` 有去无回的 CLI——例如被 bun 回归
// 卡死的 brew 版 claude（MUL-3812）——会把整个注册循环拖停，daemon 的 /health 永远停在
// "starting" 到不了 "running"，宿主机上*每个*运行时都会显示为断开。真实的 `--version` 即使在
// 冷缓存或 Windows 杀软扫描下也远低于这个上限；这个超时存在的唯一意义，是让卡死的探测快速、
// 且各自独立地失败，其余运行时照常注册。用 var（而非 const）是为了让测试不用等真实上限就能
// 把它调小。
var detectVersionTimeout = 10 * time.Second

// detectCLIVersion 运行 `<cli> --version` 探测并解析版本行。
func detectCLIVersion(ctx context.Context, runtimeCmd Command) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, detectVersionTimeout)
	defer cancel()
	if runtime.GOOS == "windows" {
		// Windows 上 npm 安装的 CLI shim 指向的真正二进制（例如 codearts）才认 --version，
		// 先解析一下。
		if native := resolveCodeArtsNativeFromShim(runtimeCmd.Path, os.Stat); native != "" {
			runtimeCmd.Path = native
		}
	}

	// outputOwned 拥有这次探测。一个坏掉的 CLI（node/bun shim）可能留下继承并仍持有我们
	// stdout 管道的孙进程，而 os/exec 的 Wait 要等那些管道到 EOF 才返回——这正是这个调用要带
	// WaitDelay 兜底、上面的 deadline 也必须有意义的原因。
	//
	// 这个分支早前用过 collector，让 Wait 在*直接子进程*一退出就返回、不管后代还握着什么。
	// 在这条路径上是错的：包装器可能成功退出，而真正该报版本的 CLI——它的后代——还欠我们
	// 输出。Review 实测过：包装器退出 0，它的子进程 500ms 后才打印版本；把“组长退出”当完成
	// 会杀掉子进程、返回一个空版本加 nil 错误，而 outputOwned 能返回版本。管道 EOF 才是唯一
	// 表示“不会再有输出了”的信号；组长退出不是。
	//
	// 这个分支在这里仍然修的是*上报*：一个滞留的后代会让 Wait 撞上 WaitDelay、上报
	// exec.ErrWaitDelay——尽管版本已经到了——而一次失败的 --version 探测会直接跳过运行时注册
	// （#6084 曾因这点回退过一个 WaitDelay 兜底）。所以当且仅当“答案本身在场”时才丢弃该错误。
	// 见 salvageProbeAnswer。
	//
	// 经 runtimeCmd.exec 构造，这样自定义运行时的 fixed_args 前缀仍会被应用（MUL-6260）。
	cmd := runtimeCmd.exec(ctx, "--version")
	hideAgentWindow(cmd)
	cmd.WaitDelay = 2 * time.Second
	data, err := outputOwned(cmd, runtimeCmd.logger)
	version, recognised := extractVersionLine(string(data))
	if err != nil {
		// 判断用 recognised 而不是 `version != ""`。两者恰好在该关键处不同：extractVersionLine
		// 在没有一行带 semver token 时会退回“去空白的原始输出”，所以非空只表示“CLI 打印了
		// 点什么”——在这条路径上，那可能是包装器在真正版本出现之前打的一段横幅。Review 实测
		// 过：一个桩打印 "initializing plugins"、退出 0，而版本在 WaitDelay 之后才到——于是
		// 把 "initializing plugins" 当版本登记下来、还带 nil 错误。做“是否抢救”的决定需要更
		// 强的问题，所以要问“版本是否真的被识别出来”。
		//
		// 兜底在成功路径上仍然成立：一个正常退出 0、但版本格式不寻常的 CLI 确实是在报版本，
		// 把它丢成空会是一次回归（#2516）。它只是不能当作“一次有界、不完整的读取已包含答案”
		// 的证据。
		if salvaged := salvageProbeAnswer(runtimeCmd, "--version", recognised, err); salvaged {
			return version, nil
		}
		// 所有探测共用一个 provider 无关的边界：DetectVersion 让每个 provider 都走这里，所以
		// 在此处加一条 ENOEXEC 诊断，能到达 daemon 为“被跳过的运行时”上报的原因（MUL-6164）。
		return "", fmt.Errorf("detect version for %s: %w", runtimeCmd, ExplainExecError(err))
	}
	return version, nil
}

// salvageProbeAnswer 决定“一次一次性探测已经产出了答案、但 os/exec 报了错误”时能否忽略该
// 错误，并记录这个决定。
//
// 它只为一种形态而存在：CLI 打印了我们问的东西，然后它的*生命周期*出了问题——一个后代在
// cmd.WaitDelay 之后仍握着输出管道（exec.ErrWaitDelay），于是 Wait 对一个已经在缓冲里的答案
// 报了失败。只有解析器正面识别出答案时才允许使用它；一段任意的、不完整的横幅不是证据。
//
// 刻意收窄，因为“我们有输出”绝不能和“我们有答案”混为一谈：
//
//   - answered 是调用方自己对答案的*识别*，不是长度检查、也不是“退回接受任何东西”的解析。
//     版本只有 extractVersionLine 命中“版本形态的行”才被抢救——它的“去空白原始输出”兜底不
//     算，因为包装器的横幅就能满足它；目录只有解析成功才算。
//   - 只有 ErrWaitDelay 合格。非零退出仍然算失败：一个打印了点东西然后退出 1 的 CLI 是在
//     报告问题，它的 stderr 才是诊断。取消与超时同样算失败——撞上 deadline 意味着 CLI 根本没
//     跑完，而这里不是决定“超时是否可以接受”的那一层。
func salvageProbeAnswer(runtimeCmd Command, probe string, answered bool, err error) bool {
	if !answered || !errors.Is(err, exec.ErrWaitDelay) {
		return false
	}
	if runtimeCmd.logger != nil {
		runtimeCmd.logger.Warn("agent: CLI answered but left its output pipes open; "+
			"using the answer and reaping the tree",
			"command", runtimeCmd.String(), "probe", probe, "err", err)
	}
	return true
}

// extractVersionLine 从 `<cli> --version` 的输出里抽出版本行，丢弃开头的 shell 噪声。在
// Windows 上，npm 安装的 CLI shim（尤其是 gemini 的）会在真正版本到达 stdout 前打印 `chcp`
// 输出，例如 `Active code page: 65001`；过去原始拼接会被直接持久化成运行时版本（见 #2516）。
//
// 启发式：返回第一条含 semver 形态 token（命中 versionRe）的非空行。像 "2.1.5 (Claude Code)"
// 或 "codex-cli 0.118.0" 这种完整版本串原样保留，因为返回的是整条命中的行。若没有任何行带
// semver token，退回去空白的原始输出，避免把不寻常的版本格式悄悄丢成空。
//
// 第二个返回值报告这次扫描是否真的命中，即第一个返回值是“被识别的版本”还是“只是兜底”。它
// 存在是因为兜底接受*任意*非空文本——CLI 干净退出时这没问题（它说了它说的话），但当调用方
// 需要判断“一次不完整的读取是否已含答案”时，它不能当作证据——见 salvageProbeAnswer。
func extractVersionLine(raw string) (string, bool) {
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if versionRe.MatchString(line) {
			return line, true
		}
	}
	return strings.TrimSpace(raw), false
}

// logWriter 把 *slog.Logger 适配成 io.Writer，用于把子进程 stderr 转成日志行。
type logWriter struct {
	logger *slog.Logger
	prefix string
}

// newLogWriter 构造一个给每行日志加 prefix 的 logWriter。
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
