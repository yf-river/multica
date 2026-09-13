package agent

// 文件职责：TRAE（字节跳动官方 TRAE CLI，即 `traecli` 二进制）的 ACP Backend
// 实现。
//
// 运行方式：以 `traecli acp serve --yolo` 启动子进程，通过标准 ACP（Agent
// Client Protocol）JSON-RPC 2.0（stdin/stdout）通信。与 hermes/kimi/kiro/qoder
// 复用同一套 hermesClient 客户端，只需按 provider 调整启动参数与工具名归一化。
// 它原生支持 ACP 的 session/load（续跑）、session/set_model、session/prompt；
// initialize 会宣告 loadSession:true 与 mcpCapabilities{http,sse}，模型目录在
// session/new 里返回。
//
// 主要入口：traecliBackend.Execute。

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

// traecliBlockedArgs 是 daemon 写死、不允许用户 custom_args 覆盖的参数：
// `acp`、`serve` 是进入 ACP 模式的子命令/动作；`-y`/`--yolo` 归 daemon 所有，
// 保证无头 ACP 始终跑在绕过权限确认模式（否则官方 traecli 会对非只读工具弹权限
// 确认——见 https://docs.trae.cn/cli_permission-mode）；`--print`/`-p` 和
// `--output-format` 会把二进制从 ACP 切成 print 模式，破坏 daemon↔traecli 的
// 传输；`--permission-mode` 由 daemon 通过 --yolo 代为控制。
var traecliBlockedArgs = map[string]blockedArgMode{
	"acp":               blockedStandalone,
	"serve":             blockedStandalone,
	"-y":                blockedStandalone,
	"--yolo":            blockedStandalone,
	"-p":                blockedStandalone,
	"--print":           blockedStandalone,
	"--output-format":   blockedWithValue,
	"--permission-mode": blockedWithValue,
}

// traecliBackend 通过派生 `traecli acp serve --yolo` 子进程实现 Backend，并经
// stdin/stdout 上的标准 ACP（Agent Client Protocol）JSON-RPC 2.0 传输通信。
//
// 目标是字节跳动官方 TRAE CLI（文档在 https://docs.trae.cn/cli 的那个
// `traecli` 二进制——不是开源 bytedance/trae-agent 的 `trae-cli`，后者没有 ACP
// 传输）。traecli 原生走 ACP：initialize 就宣告 `loadSession: true` 与
// `mcpCapabilities: {http, sse}`，模型目录由 session/new 返回，支持
// session/load（续跑）、session/set_model、session/prompt。因此现有
// Hermes/Kimi/Kiro/Qoder 共用的 ACP 客户端（hermesClient）只需改启动参数与
// 工具名归一化就能驱动它。
//
// 上面 initialize 的能力是从真实 traecli v0.120.42 二进制抓的；流式闸门与其它
// ACP backend 一致，防止 session 建立时冲刷的历史回放污染流式输出。
type traecliBackend struct {
	cfg Config
}

// traecliReaderDrainGrace 是 prompt 结束后继续排空 stdout/stderr 的宽限窗口
// （traecli 收尾时可能短暂不关进程/管道，见 Execute）。
var traecliReaderDrainGrace = 2 * time.Second

// traecliMessageStream 串行化"发送"与"最终关闭"，让晚到的 stdout 读取协程
// （traecli 在 session/prompt 返回后可能仍短暂持有进程与管道）不至于往已关闭的
// 通道发送而 panic。与 qoder 的实现一致。
type traecliMessageStream struct {
	ch     chan Message
	mu     sync.Mutex
	closed bool
}

func newTraecliMessageStream(size int) *traecliMessageStream {
	return &traecliMessageStream{ch: make(chan Message, size)}
}

// send 向通道投递消息；已关闭则静默丢弃。
func (s *traecliMessageStream) send(msg Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	trySend(s.ch, msg)
}

// close 只关闭底层通道一次；重复调用安全。
func (s *traecliMessageStream) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	close(s.ch)
}

// Execute 实现 Backend.Execute：以 ACP 驱动 traecli 子进程完成一次 prompt。
func (b *traecliBackend) Execute(ctx context.Context, prompt string, opts ExecOptions) (*Session, error) {
	execPath := b.cfg.ExecutablePath
	if execPath == "" {
		execPath = "traecli"
	}
	if _, err := exec.LookPath(execPath); err != nil {
		return nil, fmt.Errorf("traecli executable not found at %q: %w", execPath, err)
	}

	// 把用户的 mcp_config（Claude 风格，object of objects）翻译成 ACP
	// session/new 与 session/load 期望的数组形态。JSON 不合法时直接报错
	// （fail closed），让启动暴露真实错误，而不是静默丢掉所有 MCP 服务器。
	mcpServers, err := buildACPMcpServers(opts.McpConfig, b.cfg.Logger)
	if err != nil {
		return nil, fmt.Errorf("traecli: invalid mcp_config: %w", err)
	}

	timeout := opts.Timeout
	runCtx, cancel := runContext(ctx, timeout)

	// 先拼固定的 ACP 入口（acp serve --yolo），再拼用户 custom_args；日志里前两个
	// 位置参数（acp、serve）是可信的，用于脱敏展示。
	traecliArgs := append(
		[]string{"acp", "serve", "--yolo"},
		filterCustomArgs(opts.CustomArgs, traecliBlockedArgs, b.cfg.Logger)...,
	)
	cmd := b.cfg.commandAt(execPath).exec(runCtx, traecliArgs...)
	hideAgentWindow(cmd)
	b.cfg.logAgentCommand(cmd, newAgentCommandLogArgs(traecliArgs,
		trustAgentCommandPositional(0, "acp"),
		trustAgentCommandPositional(1, "serve"),
	))
	if opts.Cwd != "" {
		cmd.Dir = opts.Cwd
	}
	cmd.Env = buildEnv(b.cfg.Env)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("traecli stdout pipe: %w", err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("traecli stdin pipe: %w", err)
	}
	// StderrPipe + 显式拷贝协程能拿到先于"是否升级为失败"决策触发的汇合点
	// （stderrDone）；为什么 io.MultiWriter 形态在高负载下会与 stopReason=end_turn
	// 竞争，见 hermes.go 对应注释。
	providerErr := newACPProviderErrorSniffer("traecli")
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("traecli stderr pipe: %w", err)
	}

	if err := startOwnedProcessTree(cmd, b.cfg.Logger); err != nil {
		cancel()
		return nil, fmt.Errorf("start traecli: %w", err)
	}

	// stderr 同时进日志和错误嗅探器。
	stderrSink := io.MultiWriter(newLogWriter(b.cfg.Logger, "[traecli:stderr] "), providerErr)
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		_, _ = io.Copy(stderrSink, stderr)
	}()

	b.cfg.Logger.Info("traecli acp started", "pid", cmd.Process.Pid, "cwd", opts.Cwd)

	msgStream := newTraecliMessageStream(256)
	resCh := make(chan Result, 1)

	// traecli 把"过程旁白"和"最终答案"都发成同一种 agent_message_chunk；
	// tracker 只保留最后一次工具调用之后的文本块作 Result.Output，同时保留全文
	// 用于错误检测。
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

	// stdout 读取协程：逐行喂给 JSON-RPC 客户端；进程退出后把挂起的 RPC 统一
	// 收尾，避免调用方永远等待。
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
		c.closeAllPending(fmt.Errorf("traecli process exited"))
	}()

	// 主流程协程：握手 → 建会话/续跑 →（可选）切模型 → prompt → 收尾。
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
		// 仅当 ACP 运行时拒绝"我们请求续跑的会话"时置位——只有这种失败能靠全新
		// 会话治好，所以下面的握手/网络类失败必须让它保持 false。
		var resumeRejected bool
		effectiveModel := strings.TrimSpace(opts.Model)

		// 第 1 步：initialize 握手，确认识别与能力。
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
			finalError = fmt.Sprintf("traecli initialize failed: %v", err)
			resCh <- Result{Status: finalStatus, Error: finalError, DurationMs: time.Since(startTime).Milliseconds()}
			return
		}

		// 剔掉运行时没宣称支持的远程 MCP 传输条目（traecli 宣告
		// mcpCapabilities{http,sse}）。为什么把不支持的传输发过去会搞挂整个
		// session/new，见 hermes.go 里的注释。
		mcpServers = filterACPMcpServersByCapability(mcpServers, extractACPMcpCapabilities(initResult), "traecli", b.cfg)

		cwd := opts.Cwd
		if cwd == "" {
			cwd = "."
		}

		// 第 2 步：续跑或新建会话。
		if opts.ResumeSessionID != "" {
			// traecli 宣告 loadSession:true，所以续跑走标准 ACP session/load
			// （与 Kiro 相同）。
			result, err := c.request(runCtx, "session/load", map[string]any{
				"cwd":        cwd,
				"sessionId":  opts.ResumeSessionID,
				"mcpServers": mcpServers,
			})
			if err != nil {
				finalStatus = "failed"
				finalError = fmt.Sprintf("traecli session/load failed: %v", err)
				resCh <- Result{Status: finalStatus, Error: finalError, DurationMs: time.Since(startTime).Milliseconds()}
				return
			}
			var changed bool
			sessionID, changed = resolveResumedSessionID(opts.ResumeSessionID, result)
			if changed {
				b.cfg.Logger.Warn("agent returned a different session id on resume — original was likely lost; continuing with the new id",
					"backend", "traecli",
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
				finalError = fmt.Sprintf("traecli session/new failed: %v", err)
				resCh <- Result{Status: finalStatus, Error: finalError, DurationMs: time.Since(startTime).Milliseconds()}
				return
			}
			sessionID = extractACPSessionID(result)
			// 没有会话 id 就无法续跑、无法对账，按失败处理。
			if sessionID == "" {
				finalStatus = "failed"
				finalError = "traecli session/new returned no session ID"
				resCh <- Result{Status: finalStatus, Error: finalError, DurationMs: time.Since(startTime).Milliseconds()}
				return
			}
			if effectiveModel == "" {
				effectiveModel = extractACPCurrentModelID(result)
			}
		}

		c.sessionID = sessionID
		b.cfg.Logger.Info("traecli session created", "session_id", sessionID)

		// 第 3 步：用户显式指定了模型就切换过去；若续跑的会话已死，会在这里（而
		// 不是 prompt 时）暴露，清空 id 并标记 resumeRejected，让 daemon 走全新
		// 会话兜底。
		if opts.Model != "" {
			if _, err := c.request(runCtx, "session/set_model", map[string]any{
				"sessionId": sessionID,
				"modelId":   opts.Model,
			}); err != nil {
				b.cfg.Logger.Warn("traecli set_session_model failed", "error", err, "requested_model", opts.Model)
				finalStatus = "failed"
				finalError = fmt.Sprintf("traecli could not switch to model %q: %v", opts.Model, err)
				if opts.ResumeSessionID != "" && isACPSessionNotFound(err) {
					b.cfg.Logger.Warn("resumed session not found at set_model time; clearing session id so the daemon retries fresh",
						"backend", "traecli",
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
			b.cfg.Logger.Info("traecli session model set", "model", opts.Model)
		}

		userText := prompt
		if opts.SystemPrompt != "" {
			userText = opts.SystemPrompt + "\n\n---\n\n" + prompt
		}

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
				finalError = fmt.Sprintf("traecli timed out after %s", timeout)
			} else if runCtx.Err() == context.Canceled {
				finalStatus = "aborted"
				finalError = "execution cancelled"
			} else {
				finalStatus = "failed"
				finalError = fmt.Sprintf("traecli session/prompt failed: %v", err)
				if opts.ResumeSessionID != "" && isACPSessionNotFound(err) {
					b.cfg.Logger.Warn("resumed session not found at prompt time; clearing session id so the daemon retries fresh",
						"backend", "traecli",
						"session_id", sessionID,
					)
					sessionID = ""
					resumeRejected = true
				}
			}
		} else {
			select {
			case pr := <-promptDone:
				if pr.stopReason == "cancelled" {
					finalStatus = "aborted"
					finalError = "traecli cancelled the prompt"
				}
				c.mergeUsage(pr.usage)
			default:
			}
			waitForACPNotificationQuiescence(runCtx, activity, readerDone, acpNotificationQuietTime, traecliReaderDrainGrace)
		}

		duration := time.Since(startTime)
		b.cfg.Logger.Info("traecli finished", "pid", cmd.Process.Pid, "status", finalStatus, "duration", duration.Round(time.Millisecond).String())

		stdin.Close()
		cancel()

		// traecli ACP 在 session/prompt 返回后可能仍短暂持有进程与 stdout/stderr
		// 管道。prompt 响应已是终局，所以把排空限定在宽限窗口内：等 stdout 读取
		// 协程与 stderr 拷贝协程（CommandContext 的取消负责真正拆进程）。在这里
		// 排空 stderr，是为了让下面"provider 错误升级"在定终态前能看到终止标记。
		drainCtx, drainCancel := context.WithTimeout(context.Background(), traecliReaderDrainGrace)
		select {
		case <-readerDone:
		case <-drainCtx.Done():
		}
		select {
		case <-stderrDone:
		case <-drainCtx.Done():
		}
		drainCancel()
		// 在 defer 关 msgStream 之前先关闸门；若晚到的读取协程已经过了闸门，
		// traecliMessageStream 的锁会把发送与关闭串行化，晚到的发送被丢弃而不是
		// panic。
		streamingCurrentTurn.Store(false)

		finalOutput, providerErrorOutput := deliverable.result()

		// 当 stderr 或 agent 文本流出现"上游 LLM 终局失败"（HTTP 4xx / 限流 /
		// token 过期）时把 completed 升级为 failed。与 hermes/kimi/kiro/qoder
		// 一致，并读取完整文本流，让"在工具调用之前就放弃"的回合仍然可见。
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
