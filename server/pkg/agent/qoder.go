package agent

// 文件职责：Qoder CLI（国际版 qodercli / 中国区 qoderclicn，二者共用一个 ACP
// 后端）的 Backend 实现。
//
// 运行方式：以 `<binary> --yolo --acp` 启动子进程，通过 ACP（Agent Communication
// Protocol，JSON-RPC 2.0 over stdin/stdout）通信——与本仓库 hermes/kimi/kiro
// 走同一套 hermesClient 基础设施。本文件负责：把执行参数翻译成 initialize /
// session/new / session/resume / session/set_model / session/prompt 等 RPC，
// 用"流式闸门"（streamingCurrentTurn）防止 session/setup 阶段回放的历史污染
// 实时输出，读完后做一次有界排空再定终态，并处理续跑被拒（resume rejected）
// 的识别与上报。
//
// 主要入口：qoderBackend.Execute；工具函数：qoderDefaultBinary（按 provider 选
// 二进制名）。

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// qoderBlockedArgs 是 daemon 写死的参数，用户配置的 custom_args 一律不得覆盖：
// Qoder 是靠全局 `--acp` 旗标进入 ACP 模式的，允许用户剥掉或重复它会破坏
// daemon↔Qoder 的传输契约；`--yolo` 归 daemon 所有，保证无头 ACP 始终跑在
// "绕过权限确认"模式（见 Qoder ACP 文档）。旧的 `acp` 子命令写法也被禁掉，
// 防止 custom_args 把二进制切换到别的模式。
var qoderBlockedArgs = map[string]blockedArgMode{
	"--acp":  blockedStandalone,
	"acp":    blockedStandalone,
	"--yolo": blockedStandalone,
}

// qoderBackend 为两个 Qoder CLI 二进制实现 Backend：以 `<binary> --yolo --acp`
// 拉起进程，通过 stdin/stdout 上的 ACP（Agent Communication Protocol）
// JSON-RPC 2.0 传输通信。
//
// Qoder CLI 用的是全局旗标（`--yolo`、`--acp`），不是 `acp` 子命令。它像
// Hermes/Kimi/Kiro 一样复用 hermesClient，并沿用它们的"流式闸门"：session/setup
// 阶段冲刷出来的历史回放不会污染流式输出，也不会让 UI 卡在一段过期的
// assistant 片段上。
type qoderBackend struct {
	cfg               Config
	defaultExecutable string // 兜底二进制名（qodercli / qoderclicn），ExecutablePath 为空时使用
}

// qoderDefaultBinary 按 provider 类型返回缺省二进制名：qoderclicn 用 qoderclicn，
// 其余（含 qoder）用 qodercli。
func qoderDefaultBinary(providerType string) string {
	if providerType == "qoderclicn" {
		return "qoderclicn"
	}
	return "qodercli"
}

// qoderReaderDrainGrace 是 prompt 结束后继续排空 stdout/stderr 的宽限窗口：
// Qoder 可能把持有继承 fd 的子进程留在后面，读太久会拖慢收尾（见 Execute）。
var qoderReaderDrainGrace = 2 * time.Second

// qoderMessageStream 是对消息通道做"并发安全关闭"的包装：send/close 都要上锁，
// 保证晚到的 send 不会往已关闭的通道写而 panic，而是被静默丢弃。
type qoderMessageStream struct {
	ch     chan Message
	mu     sync.Mutex
	closed bool
}

func newQoderMessageStream(size int) *qoderMessageStream {
	return &qoderMessageStream{ch: make(chan Message, size)}
}

// send 向通道投递一条消息；通道已关闭时静默丢弃，不 panic。
func (s *qoderMessageStream) send(msg Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	trySend(s.ch, msg)
}

// close 关闭底层通道，只执行一次；重复调用是安全的。
func (s *qoderMessageStream) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	close(s.ch)
}

// Execute 实现 Backend.Execute：以 ACP 协议驱动 qoder 子进程跑完一段 prompt。
// 若 opts 携带会话/模型参数，会先走续跑或建会话、切模型等 RPC，再发起
// session/prompt；结束后经有限排空把最终输出、用量与续跑被拒标记收进 Result。
func (b *qoderBackend) Execute(ctx context.Context, prompt string, opts ExecOptions) (*Session, error) {
	execPath := b.cfg.ExecutablePath
	if execPath == "" {
		execPath = b.defaultExecutable
	}
	if _, err := exec.LookPath(execPath); err != nil {
		return nil, fmt.Errorf("%s executable not found at %q: %w", b.defaultExecutable, execPath, err)
	}

	// 把用户的 mcp_config（Claude 风格，object of objects）翻译成 ACP
	// session/new、session/resume 期望的数组形态。复用共享转换器，让远程 MCP
	// 的 headers（如 Authorization）以 [{name, value}] 结构保留、输出确定。
	// JSON 不合法时直接报错（fail closed），让启动过程露出真实错误，而不是
	// 静默丢掉所有 MCP 服务器。
	mcpServers, err := buildACPMcpServers(opts.McpConfig, b.cfg.Logger)
	if err != nil {
		return nil, fmt.Errorf("qoder: invalid mcp_config: %w", err)
	}

	timeout := opts.Timeout
	runCtx, cancel := runContext(ctx, timeout)

	qoderArgs := append(
		[]string{"--yolo", "--acp"},
		filterCustomArgs(opts.CustomArgs, qoderBlockedArgs, b.cfg.Logger)...,
	)
	cmd := b.cfg.commandAt(execPath).exec(runCtx, qoderArgs...)
	hideAgentWindow(cmd)
	b.cfg.logAgentCommand(cmd, newAgentCommandLogArgs(qoderArgs))
	if opts.Cwd != "" {
		cmd.Dir = opts.Cwd
	}
	cmd.Env = buildEnv(b.cfg.Env)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("qoder stdout pipe: %w", err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("qoder stdin pipe: %w", err)
	}
	// 用 StderrPipe + 显式拷贝协程，能拿到一个先于"是否升级为失败"决策触发的
	// 汇合点（stderrDone）；为什么不能用 io.MultiWriter 形态，见 hermes.go 里
	// 对应注释：高负载下它会与 stopReason=end_turn 竞争——terminal 的 provider
	// 错误可能在我们已经读完 finalOutput、上报 "completed" 之后才到。
	providerErr := newACPProviderErrorSniffer("qoder")
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("qoder stderr pipe: %w", err)
	}

	if err := startOwnedProcessTree(cmd, b.cfg.Logger); err != nil {
		cancel()
		return nil, fmt.Errorf("start qoder: %w", err)
	}

	// stderr 一鱼两吃：既进日志，又喂给 provider 错误嗅探器。
	stderrSink := io.MultiWriter(newLogWriter(b.cfg.Logger, "[qoder:stderr] "), providerErr)
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		_, _ = io.Copy(stderrSink, stderr)
	}()

	b.cfg.Logger.Info("qoder acp started", "pid", cmd.Process.Pid, "cwd", opts.Cwd)

	msgStream := newQoderMessageStream(256)
	resCh := make(chan Result, 1)

	// Qoder 把"过程中旁白"和"最终答案"都发成同一种 ACP AgentMessageChunk；
	// tracker 只保留最后一次工具调用之后的文本块作为 Result.Output，同时保留
	// 全文用于错误检测。
	var deliverable acpDeliverableTracker
	var streamingCurrentTurn atomic.Bool

	promptDone := make(chan hermesPromptResult, 1)
	activity := make(chan struct{}, 1)

	c := &hermesClient{
		cfg:          b.cfg,
		stdin:        stdin,
		pending:      make(map[int]*pendingRPC),
		pendingTools: make(map[string]*pendingToolCall),
		acceptNotification: func(string) bool {
			return streamingCurrentTurn.Load()
		},
		onActivity: func() {
			select {
			case activity <- struct{}{}:
			default:
			}
		},
		onMessage: func(msg Message) {
			if !streamingCurrentTurn.Load() {
				return
			}
			if msg.Type == MessageToolUse {
				msg.Tool = kimiToolNameFromTitle(msg.Tool)
			}
			deliverable.observe(msg)
			msgStream.send(msg)
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

	// stdout 读取协程：逐行喂给 JSON-RPC 客户端；进程退出后把所有挂起的
	// pending RPC 统一以错误收场，避免调用方永远等下去。
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		scanner := newAgentStreamScanner(stdout)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			c.handleLine(line)
		}
		c.closeAllPending(fmt.Errorf("qoder process exited"))
	}()

	// 主流程协程：依次完成握手、会话、prompt，最后收尾产出 Result。
	go func() {
		defer cancel()
		defer msgStream.close()
		defer close(resCh)
		defer func() {
			stdin.Close()
			_ = cmd.Wait()
			releaseProcessGroup(cmd)
		}()

		startTime := time.Now()
		finalStatus := "completed"
		var finalError string
		var sessionID string
		// 仅当 ACP 运行时拒绝了"我们请求续跑的那个会话"时才置位——只有这种失败
		// 才能靠开全新会话治好，所以下面的握手/网络类失败必须让它保持 false。
		var resumeRejected bool
		effectiveModel := strings.TrimSpace(opts.Model)

		// 第 1 步：initialize 握手，确认协议版本与运行时能力。
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
			finalError = fmt.Sprintf("qoder initialize failed: %v", err)
			resCh <- Result{Status: finalStatus, Error: finalError, DurationMs: time.Since(startTime).Milliseconds()}
			return
		}

		// 把"运行时没宣称支持远程传输"的 MCP 条目剔掉。为什么不能无脑把
		// http/sse 发给只支持 stdio 的 ACP 运行时（会整个搞挂 session/new），
		// 见 hermes.go 里的对应注释。
		mcpServers = filterACPMcpServersByCapability(mcpServers, extractACPMcpCapabilities(initResult), "qoder", b.cfg)

		cwd := opts.Cwd
		if cwd == "" {
			cwd = "."
		}

		// 第 2 步：续跑（resume）或新建会话。续跑时若运行时换了会话 id 要告警，
		// 否则后续都钉在旧 id 上会白续。
		if opts.ResumeSessionID != "" {
			result, err := c.request(runCtx, "session/resume", map[string]any{
				"cwd":        cwd,
				"sessionId":  opts.ResumeSessionID,
				"mcpServers": mcpServers,
			})
			if err != nil {
				finalStatus = "failed"
				finalError = fmt.Sprintf("qoder session/resume failed: %v", err)
				resCh <- Result{Status: finalStatus, Error: finalError, DurationMs: time.Since(startTime).Milliseconds()}
				return
			}
			var changed bool
			sessionID, changed = resolveResumedSessionID(opts.ResumeSessionID, result)
			if changed {
				b.cfg.Logger.Warn("agent returned a different session id on resume — original was likely lost; continuing with the new id",
					"backend", "qoder",
					"requested", opts.ResumeSessionID,
					"actual", sessionID,
				)
			}
			if effectiveModel == "" {
				effectiveModel = extractACPCurrentModelID(result)
			}
		} else {
			result, err := c.request(runCtx, "session/new", map[string]any{
				"cwd":        cwd,
				"mcpServers": mcpServers,
			})
			if err != nil {
				finalStatus = "failed"
				finalError = fmt.Sprintf("qoder session/new failed: %v", err)
				resCh <- Result{Status: finalStatus, Error: finalError, DurationMs: time.Since(startTime).Milliseconds()}
				return
			}
			sessionID = extractACPSessionID(result)
			// 会话 id 都没有就无从续跑、无从对账，只能按失败算。
			if sessionID == "" {
				finalStatus = "failed"
				finalError = "qoder session/new returned no session ID"
				resCh <- Result{Status: finalStatus, Error: finalError, DurationMs: time.Since(startTime).Milliseconds()}
				return
			}
			if effectiveModel == "" {
				effectiveModel = extractACPCurrentModelID(result)
			}
		}

		c.sessionID = sessionID
		b.cfg.Logger.Info("qoder session created", "session_id", sessionID)

		// 第 3 步：用户显式指定了模型（opts.Model 非空）时，额外调用
		// session/set_model 切换到目标模型。
		if opts.Model != "" {
			if _, err := c.request(runCtx, "session/set_model", map[string]any{
				"sessionId": sessionID,
				"modelId":   opts.Model,
			}); err != nil {
				b.cfg.Logger.Warn("qoder set_session_model failed", "error", err, "requested_model", opts.Model)
				finalStatus = "failed"
				finalError = fmt.Sprintf("qoder could not switch to model %q: %v", opts.Model, err)
				if opts.ResumeSessionID != "" && isACPSessionNotFound(err) {
					// 续跑的会话若是死的，会在这里（而不是 session/prompt）暴露。
					// 清空 id，让 daemon 的"续跑失败兜底"走全新重试，并入库新会话。
					b.cfg.Logger.Warn("resumed session not found at set_model time; clearing session id so the daemon retries fresh",
						"backend", "qoder",
						"session_id", sessionID,
					)
					sessionID = ""
					resumeRejected = true
				}
				resCh <- Result{
					Status:         finalStatus,
					Error:          finalError,
					DurationMs:     time.Since(startTime).Milliseconds(),
					SessionID:      sessionID,
					ResumeRejected: resumeRejected,
				}
				return
			}
			b.cfg.Logger.Info("qoder session model set", "model", opts.Model)
		}

		// 系统简报在前、用户 prompt 在后，中间用分隔线隔开，让 agent 容易分清。
		userText := prompt
		if opts.SystemPrompt != "" {
			userText = opts.SystemPrompt + "\n\n---\n\n" + prompt
		}

		// 第 4 步：正式发 prompt。闸门在 session/prompt 之前一瞬打开，这样
		// 建会话/续跑时冲刷的历史回放被丢弃；之后本回合的每条通知都会被处理。
		streamingCurrentTurn.Store(true)
		_, err = c.request(runCtx, "session/prompt", map[string]any{
			"sessionId": sessionID,
			"prompt": []map[string]any{
				{"type": "text", "text": userText},
			},
		})
		if err != nil {
			if runCtx.Err() == context.DeadlineExceeded {
				finalStatus = "timeout"
				finalError = fmt.Sprintf("qoder timed out after %s", timeout)
			} else if runCtx.Err() == context.Canceled {
				finalStatus = "aborted"
				finalError = "execution cancelled"
			} else {
				finalStatus = "failed"
				finalError = fmt.Sprintf("qoder session/prompt failed: %v", err)
				if opts.ResumeSessionID != "" && isACPSessionNotFound(err) {
					// 有些运行时在 session/resume 时会把请求的 id 原样回显，
					// 却直到 prompt 时才拒掉它。清空 SessionID，daemon 就会用
					// 全新会话重试，而不是把后续运行都钉在过期 id 上。
					b.cfg.Logger.Warn("resumed session not found at prompt time; clearing session id so the daemon retries fresh",
						"backend", "qoder",
						"session_id", sessionID,
					)
					sessionID = ""
					resumeRejected = true
				}
			}
		} else {
			// prompt 正常返回：看是否有终局结果与用量，然后等通知风暴平静下来
			// （历史/工具通知已经收完），避免漏收消息或提前收尾。
			select {
			case pr := <-promptDone:
				if pr.stopReason == "cancelled" {
					finalStatus = "aborted"
					finalError = "qoder cancelled the prompt"
				}
				c.mergeUsage(pr.usage)
			default:
			}
			waitForACPNotificationQuiescence(runCtx, activity, readerDone, acpNotificationQuietTime, qoderReaderDrainGrace)
		}

		duration := time.Since(startTime)
		b.cfg.Logger.Info("qoder finished", "pid", cmd.Process.Pid, "status", finalStatus, "duration", duration.Round(time.Millisecond).String())

		stdin.Close()
		cancel()

		// Qoder ACP 在 session/prompt 返回后可能一直不关进程——以及 stdout/stderr
		// 管道（它会留下持有继承 fd 的子进程）。prompt 响应已经是终局了，所以把
		// 排空限定在有界窗口内：等 stdout 读取协程与 stderr 拷贝协程，但不超过
		// qoderReaderDrainGrace。真正把进程拆掉的是上面 CommandContext 的取消。
		// 在这里排空 stderr，是为了让下面的"provider 错误升级"在我们定终态前
		// 能看到终止标记。
		drainCtx, drainCancel := context.WithTimeout(context.Background(), qoderReaderDrainGrace)
		select {
		case <-readerDone:
		case <-drainCtx.Done():
		}
		select {
		case <-stderrDone:
		case <-drainCtx.Done():
		}
		drainCancel()
		// 宽限窗口过后 stdout 读取协程可能仍在跑。先关闸门，再让本协程的 defer
		// 去关 msgStream：万一有晚到的 reader 已过了闸门，qoderMessageStream 的
		// 锁会让"发送 vs 关闭"串行化，晚到的发送被丢弃而不是 panic。
		streamingCurrentTurn.Store(false)

		finalOutput, providerErrorOutput := deliverable.result()

		// 当 stderr 或 agent 文本流里出现"上游 LLM 终局失败"（HTTP 4xx / 限流 /
		// token 过期）时，把 completed 升级为 failed。与 hermes/kimi/kiro 行为
		// 一致；不做这一步的话，重试耗尽的运行仍会报 "completed"——因为
		// session/prompt 以 stopReason=end_turn 结束，而 qodercli 已经把终局错误
		// 写进了 stderr。
		finalStatus, finalError = promoteACPResultOnProviderError(finalStatus, finalError, providerErrorOutput, providerErr)

		u := c.accumulatedUsage()

		var usageMap map[string]TokenUsage
		if acpUsagePresent(u) {
			model := effectiveModel
			if model == "" {
				model = "unknown"
			}
			usageMap = map[string]TokenUsage{model: u}
		}

		resCh <- Result{
			Status:         finalStatus,
			Output:         finalOutput,
			Error:          finalError,
			DurationMs:     duration.Milliseconds(),
			SessionID:      sessionID,
			ResumeRejected: resumeRejected,
			Usage:          usageMap,
		}
	}()

	return &Session{Messages: msgStream.ch, Result: resCh}, nil
}
