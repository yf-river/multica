package agent

// 文件职责：MiniMax Code（mcode）的 ACP v1 Backend 实现。
//
// 运行方式：以 `mcode acp` 启动子进程，经标准 ACP JSON-RPC 2.0（stdio）通信。
// MiniMax Code 自己持有 Runtime/Session/权限确认/问卷等主循环，本适配器只把 ACP
// 事件流映射成 multica 的 Backend 契约。当前公开的 ACP 表面刻意宣告
// loadSession:false、也不暴露会话级选模型，因此：续跑会得到类型化的拒绝，让 daemon
// 换全新会话重试；模型选择由 MiniMax Code 自己管。将来版本若宣告 loadSession，
// 下面的标准 session/load 路径无需兼容性猜测或版本门就能工作。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// mcodeBlockedArgs 保护 daemon 选定的 ACP stdio 传输。公开 CLI 目前在 `mcode acp`
// 上没有可选项；它唯一的子命令是交互式的 `login`，没法在一个已被认领的无头任务里
// 跑。把这些 token 挡在 custom_args 之外，可防止某个 profile/agent 把协议进程换成
// 帮助页或鉴权 UI。
var mcodeBlockedArgs = map[string]blockedArgMode{
	"acp":      blockedStandalone,
	"login":    blockedStandalone,
	"--region": blockedWithValue,
	"-h":       blockedStandalone,
	"--help":   blockedStandalone,
}

// mcodeBackend 以 `mcode acp` 把 MiniMax Code 跑成 ACP v1 agent 服务器。
// MiniMax Code 拥有自己的 Runtime、Session、权限确认与问卷循环；本适配器只负责把
// 共享的 ACP 流映射成 multica 的 Backend 契约。
//
// 现状：当前公开的 ACP 表面故意宣告 loadSession:false、也不暴露会话级选模型。
// 因此续跑会收到类型化的拒绝（好让 daemon 用全新会话重试），模型选择仍由 MiniMax
// Code 自己管理。将来某版本若宣告了 loadSession，下面的标准 session/load 路径就会
// 直接开始工作——不需要兼容性猜测，也不需要按版本号做门禁。
type mcodeBackend struct {
	cfg Config
}

// mcodeReaderDrainGrace 收尾排空 stdout/stderr 的宽限窗口（逻辑见 Execute）。
var mcodeReaderDrainGrace = 2 * time.Second

// mcodeSessionStartupReadyDelay 是在 session/new、session/load 之前等的固定时长：
// MiniMax Code 初始化后会自己起一堆内部协程（session 分发、MCP 启动等），立即发
// 建会话请求可能撞上它还没就绪。
var mcodeSessionStartupReadyDelay = 100 * time.Millisecond

// errMcodeProcessExited 标记"mcode 进程已退出"这一传输层事件，供失败分类使用。
var errMcodeProcessExited = errors.New("mcode process exited")

// mcodeMessageStream 是对消息通道做并发安全关闭的包装：晚到的 send 不 panic，
// 静默丢弃。与 qoder/traecli 的实现一致。
type mcodeMessageStream struct {
	ch     chan Message
	mu     sync.Mutex
	closed bool
}

func newMcodeMessageStream(size int) *mcodeMessageStream {
	return &mcodeMessageStream{ch: make(chan Message, size)}
}

// send 投递消息；通道已关闭则丢弃。
func (s *mcodeMessageStream) send(message Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	trySend(s.ch, message)
}

// close 只关一次底层通道；重复调用安全。
func (s *mcodeMessageStream) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	close(s.ch)
}

// Execute 实现 Backend.Execute：以 `mcode acp` 驱动 MiniMax Code 完成一次 prompt。
// mcode 自己管会话与模型，因此这里不设 session/set_model；续跑能力按 initialize
// 宣告的 loadSession 动态决定（不支持则返回类型化拒绝）。
func (b *mcodeBackend) Execute(ctx context.Context, prompt string, opts ExecOptions) (*Session, error) {
	execPath := b.cfg.ExecutablePath
	if execPath == "" {
		execPath = "mcode"
	}
	if _, err := exec.LookPath(execPath); err != nil {
		return nil, fmt.Errorf("mcode executable not found at %q: %w", execPath, err)
	}

	// 把 mcp_config 翻译成 ACP 期望的数组形态（与其它 ACP backend 相同）。
	mcpServers, err := buildACPMcpServers(opts.McpConfig, b.cfg.Logger)
	if err != nil {
		return nil, fmt.Errorf("mcode: invalid mcp_config: %w", err)
	}

	runCtx, cancel := runContext(ctx, opts.Timeout)
	args := []string{"acp"}
	args = append(args, filterCustomArgs(opts.ExtraArgs, mcodeBlockedArgs, b.cfg.Logger)...)
	args = append(args, filterCustomArgs(opts.CustomArgs, mcodeBlockedArgs, b.cfg.Logger)...)
	cmd := b.cfg.commandAt(execPath).exec(runCtx, args...)
	hideAgentWindow(cmd)
	// mcode 的取消不靠发 cancel 帧（ACP 无此约定），直接在 runCtx 到期时对整组
	// 进程 SIGKILL，避免留下持有终端/管道的残留。
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			signalProcessGroup(cmd, syscall.SIGKILL)
		}
		return nil
	}
	cmd.WaitDelay = mcodeReaderDrainGrace
	b.cfg.logAgentCommand(cmd, newAgentCommandLogArgs(args, trustAgentCommandPositional(0, "acp")))
	if opts.Cwd != "" {
		cmd.Dir = opts.Cwd
	}
	cmd.Env = buildEnv(b.cfg.Env)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("mcode stdout pipe: %w", err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("mcode stdin pipe: %w", err)
	}
	providerErr := newACPProviderErrorSniffer("mcode")
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("mcode stderr pipe: %w", err)
	}
	if err := startOwnedProcessTree(cmd, b.cfg.Logger); err != nil {
		cancel()
		return nil, fmt.Errorf("start mcode: %w", err)
	}

	// stderr 一鱼两吃：进日志 + 喂错误嗅探器。
	stderrSink := io.MultiWriter(newLogWriter(b.cfg.Logger, "[mcode:stderr] "), providerErr)
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		_, _ = io.Copy(stderrSink, stderr)
	}()

	b.cfg.Logger.Info("mcode acp started", "pid", cmd.Process.Pid, "cwd", opts.Cwd)
	msgStream := newMcodeMessageStream(256)
	resCh := make(chan Result, 1)
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
		onMessage: func(message Message) {
			if !streamingCurrentTurn.Load() {
				return
			}
			deliverable.observe(message)
			msgStream.send(message)
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

	// stdout 读取协程：逐行喂给客户端；进程退出后收掉所有挂起 RPC。
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		scanner := newAgentStreamScanner(stdout)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line != "" {
				c.handleLine(line)
			}
		}
		c.closeAllPending(errMcodeProcessExited)
	}()

	// 主流程协程：握手 → 建会话/续跑 → prompt → 收尾。
	go func() {
		defer msgStream.close()
		defer close(resCh)
		defer func() {
			_ = stdin.Close()
			// 取消必须在 Wait 之前仍可达。mcode 或某个工具后代进程可能无视 stdin
			// EOF，所以先 Wait 会让每个提前返回的路径（包括被拒的续跑）都死锁。
			cancel()
			_ = cmd.Wait()
			releaseProcessGroup(cmd)
		}()

		startTime := time.Now()
		finalStatus := "completed"
		var finalError string
		var sessionID string
		var resumeRejected bool

		// 第 1 步：握手，读取 agentCapabilities（尤其 loadSession）。
		initResult, err := c.request(runCtx, "initialize", map[string]any{
			"protocolVersion": 1,
			"clientInfo": map[string]any{
				"name":    "multica-agent-sdk",
				"version": "0.2.0",
			},
			"clientCapabilities": map[string]any{},
		})
		if err != nil {
			finalStatus, finalError = mcodeRequestFailure(runCtx, opts.Timeout, "initialize", err)
			resCh <- Result{Status: finalStatus, Error: finalError, DurationMs: time.Since(startTime).Milliseconds()}
			return
		}

		// 只保留运行时宣称支持的 MCP 传输形态（见 hermes.go 的说明）。
		mcpServers = filterACPMcpServersByCapability(
			mcpServers,
			extractACPMcpCapabilities(initResult),
			"mcode",
			b.cfg,
		)
		cwd := opts.Cwd
		if cwd == "" {
			cwd = "."
		}

		// 第 2 步：续跑或新建会话。
		if opts.ResumeSessionID != "" {
			// mcode 现版本宣告不支持 loadSession，直接给类型化拒绝，让 daemon 用
			// 全新会话重试（而不是拿一个注定失败的续跑去消耗时间）。
			if !mcodeLoadSessionSupported(initResult) {
				finalStatus = "failed"
				finalError = "mcode ACP does not support session loading; retry with a fresh session"
				resumeRejected = true
				resCh <- Result{
					Status:         finalStatus,
					Error:          finalError,
					DurationMs:     time.Since(startTime).Milliseconds(),
					ResumeRejected: true,
				}
				return
			}
			// 会话建/载前先等 mcode 内部就绪（见 mcodeSessionStartupReadyDelay）。
			if err := mcodeWaitForSessionStartup(runCtx); err != nil {
				finalStatus, finalError = mcodeRequestFailure(runCtx, opts.Timeout, "session/load", err)
				resCh <- Result{
					Status:         finalStatus,
					Error:          finalError,
					DurationMs:     time.Since(startTime).Milliseconds(),
					ResumeRejected: resumeRejected,
				}
				return
			}
			result, loadErr := c.request(runCtx, "session/load", map[string]any{
				"cwd":        cwd,
				"sessionId":  opts.ResumeSessionID,
				"mcpServers": mcpServers,
			})
			if loadErr != nil {
				finalStatus, finalError = mcodeRequestFailure(runCtx, opts.Timeout, "session/load", loadErr)
				// 会话找不着 = 续跑对象已死，标 resumeRejected 让 daemon 决定兜底。
				if finalStatus == "failed" && isACPSessionNotFound(loadErr) {
					resumeRejected = true
				}
				resCh <- Result{
					Status:         finalStatus,
					Error:          finalError,
					DurationMs:     time.Since(startTime).Milliseconds(),
					ResumeRejected: resumeRejected,
				}
				return
			}
			sessionID, _ = resolveResumedSessionID(opts.ResumeSessionID, result)
		} else {
			if err := mcodeWaitForSessionStartup(runCtx); err != nil {
				finalStatus, finalError = mcodeRequestFailure(runCtx, opts.Timeout, "session/new", err)
				resCh <- Result{Status: finalStatus, Error: finalError, DurationMs: time.Since(startTime).Milliseconds()}
				return
			}
			result, newErr := c.request(runCtx, "session/new", map[string]any{
				"cwd":        cwd,
				"mcpServers": mcpServers,
			})
			if newErr != nil {
				finalStatus, finalError = mcodeRequestFailure(runCtx, opts.Timeout, "session/new", newErr)
				resCh <- Result{Status: finalStatus, Error: finalError, DurationMs: time.Since(startTime).Milliseconds()}
				return
			}
			sessionID = extractACPSessionID(result)
			if sessionID == "" {
				resCh <- Result{
					Status:     "failed",
					Error:      "mcode session/new returned no session ID",
					DurationMs: time.Since(startTime).Milliseconds(),
				}
				return
			}
		}

		c.sessionID = sessionID
		// mcode 上下文来自 cwd 下的 AGENTS.md；SystemPrompt 传了也不生效，记一条
		// Debug 日志说明（避免调用方误以为已注入）。
		if opts.SystemPrompt != "" {
			b.cfg.Logger.Debug("mcode ignoring ExecOptions.SystemPrompt; using cwd-scoped AGENTS.md", "cwd", opts.Cwd)
		}
		// 第 3 步：发 prompt。闸门在发之前打开，只收本回合通知、丢弃历史回放。
		streamingCurrentTurn.Store(true)
		_, err = c.request(runCtx, "session/prompt", map[string]any{
			"sessionId": sessionID,
			"prompt": []map[string]any{
				{"type": "text", "text": prompt},
			},
		})
		if err != nil {
			finalStatus, finalError = mcodeRequestFailure(runCtx, opts.Timeout, "session/prompt", err)
			// 续跑后 prompt 时才暴露"会话已死"：清掉 id，让 daemon 走全新会话。
			if opts.ResumeSessionID != "" && isACPSessionNotFound(err) {
				resumeRejected = true
				sessionID = ""
			}
		} else {
			select {
			case result := <-promptDone:
				c.mergeUsage(result.usage)
				if result.stopReason == "cancelled" {
					finalStatus = "aborted"
					finalError = "execution cancelled"
				} else if result.stopReason == "max_turn_requests" {
					finalStatus = "failed"
					finalError = "mcode reached its maximum turn requests"
				}
			default:
			}
			// 等通知风暴平静，确保没有漏收消息。
			waitForACPNotificationQuiescence(runCtx, activity, readerDone, acpNotificationQuietTime, mcodeReaderDrainGrace)
		}
		streamingCurrentTurn.Store(false)

		duration := time.Since(startTime)
		_ = stdin.Close()
		cancel()
		<-readerDone
		<-stderrDone

		finalOutput, fullOutput := deliverable.result()
		finalStatus, finalError = promoteACPResultOnProviderError(
			finalStatus,
			finalError,
			fullOutput,
			providerErr,
		)
		var usage map[string]TokenUsage
		if accumulated := c.accumulatedUsage(); acpUsagePresent(accumulated) {
			// mcode 不暴露会话级模型选择，无法确定真实模型名，统一记 unknown。
			usage = map[string]TokenUsage{"unknown": accumulated}
		}
		resCh <- Result{
			Status:         finalStatus,
			Output:         finalOutput,
			Error:          finalError,
			DurationMs:     duration.Milliseconds(),
			SessionID:      sessionID,
			ResumeRejected: resumeRejected,
			Usage:          usage,
		}
	}()

	return &Session{Messages: msgStream.ch, Result: resCh}, nil
}

// mcodeLoadSessionSupported 从 initialize 结果里读 agentCapabilities.loadSession，
// 报告该 mcode 版本是否支持加载（续跑）会话。
func mcodeLoadSessionSupported(result json.RawMessage) bool {
	var payload struct {
		AgentCapabilities struct {
			LoadSession bool `json:"loadSession"`
		} `json:"agentCapabilities"`
	}
	return json.Unmarshal(result, &payload) == nil && payload.AgentCapabilities.LoadSession
}

// mcodeWaitForSessionStartup 干等固定时长（mcodeSessionStartupReadyDelay）或直到
// ctx 取消——给 mcode 完成 initialize 后的内部就绪（session 分发、MCP 启动）留出
// 时间窗。
func mcodeWaitForSessionStartup(ctx context.Context) error {
	timer := time.NewTimer(mcodeSessionStartupReadyDelay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// mcodeRequestFailure 把一次 RPC 失败归类成 multica 的状态/错误文案：超时 →
// timeout，被取消 → aborted，进程在建会话前退出 → 一条带修复指引的 failed。
func mcodeRequestFailure(ctx context.Context, timeout time.Duration, operation string, err error) (string, string) {
	if ctx.Err() == context.DeadlineExceeded {
		return "timeout", fmt.Sprintf("mcode timed out during %s after %s", operation, timeout)
	}
	if ctx.Err() == context.Canceled {
		return "aborted", "execution cancelled"
	}
	if mcodeSessionStartupExited(operation, err) {
		return "failed", "mcode session/new failed after initialize: MiniMax Code ACP exited before starting a session; retry agent creation, and if it persists upgrade MiniMax Code or verify `mcode acp` starts successfully"
	}
	return "failed", fmt.Sprintf("mcode %s failed: %v", operation, err)
}

// mcodeSessionStartupExited 判断 session/new 的失败是否源自"mcode 进程在建会话前
// 就退出"。
func mcodeSessionStartupExited(operation string, err error) bool {
	if operation != "session/new" {
		return false
	}
	// 只信传输层信号。还在跑的 mcode 会把 session/new 失败报成 JSON-RPC 错误；
	// 而 session/new 恰恰是启动 mcpServers 的地方——若在这里对文本做 "process
	// exited"/"broken pipe" 匹配，会把调用方真实的 MCP 启动错误改写成一句
	// "mcode 退出了"，把根因弄丢。
	return errors.Is(err, errMcodeProcessExited) || errors.Is(err, io.ErrClosedPipe) || errors.Is(err, syscall.EPIPE)
}
