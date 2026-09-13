package agent

// 文件职责：本文件实现 Backend 接口中的「Grok」后端——针对 xAI 的 Grok Build CLI
// （`grok` 二进制）。后端拉起 `grok --no-auto-update agent --always-approve
// [--effort <level>] stdio`，通过标准 ACP（Agent Client Protocol）JSON-RPC 2.0 走
// stdin/stdout 与它通信。
//
// 与 traecli/kimi/kiro/qoder 一样复用 hermesClient 这一共享 ACP 客户端，差异仅在
// provider 特有的启动参数与工具名归一化（大写标题归成 snake_case）。
//
// 执行流程：Execute 确认可执行文件 → 把 mcp_config 转成 ACP 数组 → 起进程 → 先
// initialize 握手 → Grok 的 ACP 要求显式的 authenticate 步骤（initialize 与任何会话操作
// 之间必须选一个 authMethods 并发送 authenticate）→ 建立会话（续跑 session/load、
// 新建 session/new）→ 可选 session/set_model 切换模型 → session/prompt 进入回合，期间
// 把通知流翻译成 Message → 回合后处理结果（归因模型、合并 xAI 上报的用量）→ 有界
// 排空尾部通知 → 按 stderr/文本流的终态失败信号决定是否降级 completed → 汇总 Result。
//
// 能力注意：续跑时尝试 session/load、请求了模型就尝试 session/set_model；若某个 Grok
// Build 版本缺这两个方法，运行会以明确错误失败，而不是静默地跑在错误的会话/模型上。
// 真实 initialize（0.2.x）声明 loadSession:true 和 mcpCapabilities {http, …}。

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// grokBlockedArgs 是 daemon 写死的参数/子命令清单，用户配置的 custom_args 不得覆盖它们。
// `agent` + `stdio` 选定 ACP 传输；`--always-approve` 归 daemon 所有，保证无头的 Multica
// 运行不会被交互式权限提问卡住。切进 headless/serve/leader/print 等模式会破坏
// daemon↔grok 的 ACP 契约。模型/思考强度由 session/set_model 与 --effort 管理。
var grokBlockedArgs = map[string]blockedArgMode{
	"agent":                    blockedStandalone,
	"stdio":                    blockedStandalone,
	"headless":                 blockedStandalone,
	"serve":                    blockedStandalone,
	"leader":                   blockedStandalone,
	"--always-approve":         blockedStandalone,
	"--yolo":                   blockedStandalone,
	"--no-auto-update":         blockedStandalone,
	"--no-alt-screen":          blockedStandalone,
	"-p":                       blockedStandalone,
	"--print":                  blockedStandalone,
	"--single":                 blockedWithValue,
	"--output-format":          blockedWithValue,
	"--permission-mode":        blockedWithValue,
	"-m":                       blockedWithValue,
	"--model":                  blockedWithValue,
	"--reasoning-effort":       blockedWithValue,
	"--effort":                 blockedWithValue,
	"-r":                       blockedWithValue,
	"--resume":                 blockedWithValue,
	"-c":                       blockedStandalone,
	"--continue":               blockedStandalone,
	"-s":                       blockedWithValue,
	"--session-id":             blockedWithValue,
	"--system-prompt-override": blockedWithValue,
	"--cwd":                    blockedWithValue,
	"-w":                       blockedOptionalValue,
	"--worktree":               blockedOptionalValue,
	"--ref":                    blockedWithValue,
	"--fork-session":           blockedStandalone,
}

// grokBackend 实现 Backend 接口：拉起
// `grok --no-auto-update agent --always-approve [--effort <level>] stdio`
// 子进程，经 stdin/stdout 走标准 ACP（Agent Client Protocol）JSON-RPC 2.0 传输。
//
// 针对 xAI 的 Grok Build CLI（`grok` 二进制）。Grok Build 通过 `grok agent stdio`
// 暴露 ACP（--always-approve、--effort 等 flag 属于 `agent` 命令、要放在传输子命令
// 之前）。与 traecli/kimi/kiro/qoder 相同，复用 hermesClient，只做 provider 特有的
// 启动参数与工具名归一化。
//
// 能力说明：续跑时尝试 session/load、请求模型时尝试 session/set_model；如果某个 Grok
// Build 版本没有这些方法，运行会以清晰错误失败，而不是静默地继续跑在错误的会话/模型
// 上。真实的 initialize（0.2.x）声明 loadSession:true 与 mcpCapabilities {http, …}。
type grokBackend struct {
	cfg Config
}

var (
	grokReaderDrainGrace      = 2 * time.Second
	grokNotificationQuietTime = 250 * time.Millisecond
)

// grokMessageStream 给消息发送与最终关闭上锁串行化，避免迟到的 stdout 读取者往已关闭的
// channel 上发送。与 traecli/qoder 的做法一致。
type grokMessageStream struct {
	ch     chan Message
	mu     sync.Mutex
	closed bool
}

// newGrokMessageStream 创建一个容量为 size 的消息流。
func newGrokMessageStream(size int) *grokMessageStream {
	return &grokMessageStream{ch: make(chan Message, size)}
}

// send 非阻塞地把一条消息投进 channel；流已关闭则直接丢弃。
func (s *grokMessageStream) send(msg Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	trySend(s.ch, msg)
}

// close 关闭消息流；重复调用安全（第二次直接返回）。
func (s *grokMessageStream) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	close(s.ch)
}

// Execute 是 Backend 接口的入口：拿一条用户 prompt，起一个 grok 进程，完成一次 ACP
// 会话并跑完这一轮对话。
//
// 流程：确认可执行文件存在 → 翻译 mcp_config（失败关闭）→ 起进程并接管三路 IO →
// initialize 握手 → 从 initialize 声明的 authMethods 里选一个并发送 authenticate（Grok
// 要求显式鉴权，跳过会被真实 CLI 拒绝所有会话操作）→ 建立会话：续跑 session/load、
// 新建 session/new → 请求了模型就 session/set_model → session/prompt 进入回合，期间把
// ACP 通知翻译成 Message 流式发出（streamingCurrentTurn 闸门挡住回合外杂音）→ 回合
// 结束后有界排空尾部通知 → 按 stderr / 文本流的终态失败信号决定是否把 completed 降级
// 为 failed → 汇总 Result 交给调用方。
func (b *grokBackend) Execute(ctx context.Context, prompt string, opts ExecOptions) (*Session, error) {
	execPath := b.cfg.ExecutablePath
	if execPath == "" {
		execPath = "grok"
	}
	if _, err := exec.LookPath(execPath); err != nil {
		return nil, fmt.Errorf("grok executable not found at %q: %w", execPath, err)
	}

	// 把 agent 的 mcp_config（Claude 式对象套对象）翻译成 ACP session/new 与
	// session/load 期望的数组形状。格式非法时宁可启动失败，让真正错误浮出水面，也不
	// 静默丢掉全部 MCP 服务器。
	mcpServers, err := buildACPMcpServers(opts.McpConfig, b.cfg.Logger)
	if err != nil {
		return nil, fmt.Errorf("grok: invalid mcp_config: %w", err)
	}

	timeout := opts.Timeout
	runCtx, cancel := runContext(ctx, timeout)

	// `grok agent` 的 flag 必须放在传输子命令（`stdio`）之前。思考强度是 agent 命令上
	// 的进程级 flag；模型则在会话创建后用 session/set_model（ACP）设置。
	//
	// `--no-auto-update` 是*全局* flag（在 `agent` 子命令之前）：xAI 推荐 headless/ACP/CI
	// 运行带上它，让后台更新检查永不干扰无人值守的 daemon 任务。它归 daemon 所有
	// （grokBlockedArgs），用户的 custom_args 不能重复加它或剥掉它。
	grokArgs := []string{"--no-auto-update", "agent", "--always-approve"}
	if opts.ThinkingLevel != "" {
		grokArgs = append(grokArgs, "--effort", opts.ThinkingLevel)
	}
	grokArgs = append(grokArgs, filterCustomArgs(opts.CustomArgs, grokBlockedArgs, b.cfg.Logger)...)
	grokArgs = append(grokArgs, "stdio")

	cmd := b.cfg.commandAt(execPath).exec(runCtx, grokArgs...)
	hideAgentWindow(cmd)
	b.cfg.logAgentCommand(cmd, newAgentCommandLogArgs(grokArgs,
		trustAgentCommandPositional(1, "agent"),
		trustAgentCommandPositional(len(grokArgs)-1, "stdio"),
	))
	if opts.Cwd != "" {
		cmd.Dir = opts.Cwd
	}
	childEnv := buildEnv(b.cfg.Env)
	cmd.Env = childEnv

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("grok stdout pipe: %w", err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("grok stdin pipe: %w", err)
	}
	// StderrPipe + 显式 copier 提供先于「失败升级决策」的汇合点（stderrDone）；
	// 为什么 io.MultiWriter 形式会在高负载下与 stopReason=end_turn 竞争，见 hermes.go。
	providerErr := newACPProviderErrorSniffer("grok")
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("grok stderr pipe: %w", err)
	}

	if err := startOwnedProcessTree(cmd, b.cfg.Logger); err != nil {
		cancel()
		return nil, fmt.Errorf("start grok: %w", err)
	}

	stderrSink := io.MultiWriter(newLogWriter(b.cfg.Logger, "[grok:stderr] "), providerErr)
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		_, _ = io.Copy(stderrSink, stderr)
	}()

	b.cfg.Logger.Info("grok acp started", "pid", cmd.Process.Pid, "cwd", opts.Cwd)

	msgStream := newGrokMessageStream(256)
	resCh := make(chan Result, 1)

	// Grok 把中途叙述与最终答案都用同一种 agent_message_chunk 类型流式送出；该 tracker
	// 只为 Result.Output 保留「最后一次工具调用之后的文本块」，同时保留全文供错误检测用。
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
				// 大写标题（"Read file: …"）与 kimi/traecli 一样再归一化一次，
				// 让 UI 看到一致的 snake_case 名。
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
		c.closeAllPending(fmt.Errorf("grok process exited"))
	}()

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
		// 仅在「ACP 运行时不认我们请求续跑的那个会话」时置位。只有这种情况可通过另起
		// 全新会话挽回，所以下面的握手/网络类失败都必须保持它为 false。
		var resumeRejected bool
		effectiveModel := strings.TrimSpace(opts.Model)

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
			finalError = fmt.Sprintf("grok initialize failed: %v", err)
			resCh <- Result{Status: finalStatus, Error: finalError, DurationMs: time.Since(startTime).Milliseconds()}
			return
		}

		// Grok 的 ACP 表面要求在 `initialize` 与任何会话操作之间做显式鉴权握手：读取
		// initialize 声明的 authMethods，挑一个，在 session/new 或 session/load 之前发送
		// `authenticate`。跳过这一步，真实且已登录的 CLI 会拒绝所有会话操作（测试里的
		// 假 ACP 碰巧会接受它们——这正是必须在测试里也断言这点的原因）。xAI 文档化的
		// 偏好是：XAI_API_KEY 已设置且被提供时用 API key，否则用缓存的登录 token。
		// 参考：https://docs.x.ai/build/cli/headless-scripting
		methodID, err := selectGrokAuthMethod(extractACPAuthMethods(initResult), envHasNonEmpty(childEnv, "XAI_API_KEY"))
		if err != nil {
			finalStatus = "failed"
			finalError = fmt.Sprintf("grok authentication setup failed: %v", err)
			resCh <- Result{Status: finalStatus, Error: finalError, DurationMs: time.Since(startTime).Milliseconds()}
			return
		}
		if _, err := c.request(runCtx, "authenticate", map[string]any{
			"methodId": methodID,
			"_meta":    map[string]any{"headless": true},
		}); err != nil {
			finalStatus = "failed"
			finalError = fmt.Sprintf("grok authenticate (%s) failed: %v", methodID, err)
			resCh <- Result{Status: finalStatus, Error: finalError, DurationMs: time.Since(startTime).Milliseconds()}
			return
		}
		b.cfg.Logger.Info("grok authenticated", "method", methodID)

		// 丢掉 mcpServers 里「运行方没对外声明支持远程传输」的条目。
		// 为什么向不支持该传输的运行时发它会让 session/new 失败，见 hermes.go。
		mcpServers = filterACPMcpServersByCapability(mcpServers, extractACPMcpCapabilities(initResult), "grok", b.cfg)

		cwd := opts.Cwd
		if cwd == "" {
			cwd = "."
		}

		if opts.ResumeSessionID != "" {
			result, err := c.request(runCtx, "session/load", map[string]any{
				"cwd":        cwd,
				"sessionId":  opts.ResumeSessionID,
				"mcpServers": mcpServers,
			})
			if err != nil {
				finalStatus = "failed"
				finalError = fmt.Sprintf("grok session/load failed: %v", err)
				resCh <- Result{Status: finalStatus, Error: finalError, DurationMs: time.Since(startTime).Milliseconds()}
				return
			}
			var changed bool
			sessionID, changed = resolveResumedSessionID(opts.ResumeSessionID, result)
			if changed {
				b.cfg.Logger.Warn("agent returned a different session id on resume — original was likely lost; continuing with the new id",
					"backend", "grok",
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
				finalError = fmt.Sprintf("grok session/new failed: %v", err)
				resCh <- Result{Status: finalStatus, Error: finalError, DurationMs: time.Since(startTime).Milliseconds()}
				return
			}
			sessionID = extractACPSessionID(result)
			if sessionID == "" {
				finalStatus = "failed"
				finalError = "grok session/new returned no session ID"
				resCh <- Result{Status: finalStatus, Error: finalError, DurationMs: time.Since(startTime).Milliseconds()}
				return
			}
			if effectiveModel == "" {
				effectiveModel = extractACPCurrentModelID(result)
			}
		}

		c.sessionID = sessionID
		// 尽早把会话 id 发出去（MessageStatus），被取消的运行也能保住续跑指针。
		msgStream.send(Message{Type: MessageStatus, Status: "running", SessionID: sessionID})
		b.cfg.Logger.Info("grok session created", "session_id", sessionID)

		if opts.Model != "" {
			if _, err := c.request(runCtx, "session/set_model", map[string]any{
				"sessionId": sessionID,
				"modelId":   opts.Model,
			}); err != nil {
				b.cfg.Logger.Warn("grok set_session_model failed", "error", err, "requested_model", opts.Model)
				finalStatus = "failed"
				finalError = fmt.Sprintf("grok could not switch to model %q: %v", opts.Model, err)
				if opts.ResumeSessionID != "" && isACPSessionNotFound(err) {
					b.cfg.Logger.Warn("resumed session not found at set_model time; clearing session id so the daemon retries fresh",
						"backend", "grok",
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
			b.cfg.Logger.Info("grok session model set", "model", opts.Model)
		}

		userText := prompt
		if opts.SystemPrompt != "" {
			// Grok also reads AGENTS.md from cwd; inline system prompt covers
			// Multica runtime brief delivery when file injection is not enough.
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
				finalError = fmt.Sprintf("grok timed out after %s", timeout)
			} else if runCtx.Err() == context.Canceled {
				finalStatus = "aborted"
				finalError = "execution cancelled"
			} else {
				finalStatus = "failed"
				finalError = fmt.Sprintf("grok session/prompt failed: %v", err)
				if opts.ResumeSessionID != "" && isACPSessionNotFound(err) {
					b.cfg.Logger.Warn("resumed session not found at prompt time; clearing session id so the daemon retries fresh",
						"backend", "grok",
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
					finalError = "grok cancelled the prompt"
				}
				// `session/load` 不携带模型 id（只有 `session/new` 带），所以「续跑且没配
				// 模型」的会话若不走这里，整笔花费会被归到 "unknown" 桶里——没有价格行
				// 匹配它，会按 $0 计价。回合自带的 `_meta.modelId` 才是权威，用它。
				if effectiveModel == "" {
					effectiveModel = pr.modelID
				}
				// xAI 按回合本身计价，并把结果在这里上报。把它带上，是 ≥200K 长上下文
				// 附加费能进账单的唯一途径——光靠 token 计数还原不出请求命中了哪一档。
				c.mergeUsage(pr.usage)
			default:
			}
			waitForGrokNotificationQuiescence(runCtx, activity, readerDone)
		}

		duration := time.Since(startTime)
		b.cfg.Logger.Info("grok finished", "pid", cmd.Process.Pid, "status", finalStatus, "duration", duration.Round(time.Millisecond).String())

		stdin.Close()
		cancel()

		// Grok ACP 在 session/prompt 返回后可能还让进程（以及 stdout/stderr 管道）多开
		// 一小会儿。给排空设个上界。
		drainCtx, drainCancel := context.WithTimeout(context.Background(), grokReaderDrainGrace)
		select {
		case <-readerDone:
		case <-drainCtx.Done():
		}
		select {
		case <-stderrDone:
		case <-drainCtx.Done():
		}
		drainCancel()
		streamingCurrentTurn.Store(false)

		finalOutput, providerErrorOutput := deliverable.result()

		// 当 stderr 或 agent 文本流出现上游 LLM 的终态失败（鉴权 / 限流 / HTTP 4xx）时，
		// 把 completed 升级为 failed。这里读全文流而不是 deliverable，所以发生在工具调用
		// 之前、直接放弃的回合也仍然可见。
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

// Grok ACP `authenticate` 方法的 id（来自 `initialize`.authMethods）。
// `xai.api_key` 消费环境里的 XAI_API_KEY；`cached_token` 复用 `grok login` 写入的凭据。
const (
	grokAuthMethodAPIKey      = "xai.api_key"
	grokAuthMethodCachedToken = "cached_token"
)

// selectGrokAuthMethod 从已声明的 ACP 鉴权方法里挑一个来用，遵循 xAI 文档化的无头流程：
// 子进程环境里有 XAI_API_KEY 且 CLI 提供了 API key 选项时优先用它，否则退回缓存的登录
// token。Grok 要求显式 authenticate 步骤：方法列表为空或全是未知方法属于协议/鉴权失败，
// 而不是「可以直接继续到 session/new」的许可。
func selectGrokAuthMethod(methods []string, haveAPIKey bool) (string, error) {
	offered := make(map[string]bool, len(methods))
	for _, m := range methods {
		if m = strings.TrimSpace(m); m != "" {
			offered[m] = true
		}
	}
	if haveAPIKey && offered[grokAuthMethodAPIKey] {
		return grokAuthMethodAPIKey, nil
	}
	if offered[grokAuthMethodCachedToken] {
		return grokAuthMethodCachedToken, nil
	}
	if offered[grokAuthMethodAPIKey] {
		return "", fmt.Errorf("Grok advertised only API-key authentication, but XAI_API_KEY is not set")
	}
	advertised := make([]string, 0, len(offered))
	for method := range offered {
		advertised = append(advertised, method)
	}
	sort.Strings(advertised)
	if len(advertised) == 0 {
		return "", fmt.Errorf("Grok advertised no usable authentication methods; set XAI_API_KEY or run `grok login`")
	}
	return "", fmt.Errorf("Grok advertised unsupported authentication methods %q; update Multica or authenticate with XAI_API_KEY / `grok login`", advertised)
}

// waitForGrokNotificationQuiescence 给 ACP stdout 读取者一个有界的时机，去消费 Grok 可能
// 在 session/prompt 响应之后紧接着发出的通知。没有这个窗口，在响应边界取消进程可能截断
// 最终文本或用量更新。
func waitForGrokNotificationQuiescence(ctx context.Context, activity <-chan struct{}, readerDone <-chan struct{}) {
	waitForACPNotificationQuiescence(ctx, activity, readerDone, grokNotificationQuietTime, grokReaderDrainGrace)
}

// envHasNonEmpty 判断一个 os/exec 风格的 env 切片（"KEY=value" 条目）里，key 是否有非空
// 值。后面的条目胜出（与 OS 解析重复键的方式一致），所以从末尾往前扫。
func envHasNonEmpty(env []string, key string) bool {
	prefix := key + "="
	for i := len(env) - 1; i >= 0; i-- {
		if strings.HasPrefix(env[i], prefix) {
			return strings.TrimSpace(env[i][len(prefix):]) != ""
		}
	}
	return false
}
