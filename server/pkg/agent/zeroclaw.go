package agent

// 文件职责：本文件实现 Backend 接口中的「ZeroClaw」后端。ZeroClaw 是一个 Rust 编写、
// 单二进制的通用 agent 运行时（见 multica-ai/multica#1543），本后端拉起 `zeroclaw acp`
// 子进程，通过标准 ACP（Agent Client Protocol）JSON-RPC 2.0 走 stdin/stdout 与它通信。
//
// 它的 ACP 服务表面与 Hermes/Kimi/Reasonix/Dim/Traecli/Grok/QwenPaw/MCode 一致，所以
// 本文件直接复用共享的 hermesClient ACP 传输——差异只集中在：可执行文件名、会话引导
// 流程、工具名抽取、以及下面的非标准握手细节。真实 ZeroClaw 0.8.4 二进制经 stdio 驱动
// 验证过，握手并非「教科书式 ACP」，要点：
//
//   - 分发表只有 initialize 和 session/{new,load,resume,close,prompt,stop,cancel,
//     event|update}，没有 session/set_model（会回 -32601），也没有 handler 读 model
//     参数；模型归属 ZeroClaw 的 agent 配置档（agents.<alias>.model_provider →
//     providers.models.<type>.<alias> 里的 model），无法按会话指定，所以 ModelSelection
//     能力对 ZeroClaw 关闭。initialize 返回的 defaultModel 是全进程级的，不用于归因。
//   - session/new 只回 {sessionId, workspaceDir}：无模型目录、无 currentModelId。
//   - session/new 要求 agentAlias（除非只配了一个 agent 或设了 default_agent），
//     处理见 takeZeroclawAgentAlias。
//   - 续跑走 session/resume 而不是 session/load：load 会把整段留存 transcript 以
//     session/update 通知重放回来，把历史回合当成这次回合的输出再发一遍。
//   - 没有 handler 读 params.mcpServers：MCP 只能在 ZeroClaw 自己的配置目录里配。
//   - 它的 SESSION_NOT_FOUND 是自定义的 -32000，见 isACPSessionNotFound。
//
// 版本要求 MinVersions 规定 ZeroClaw 0.8.0+（第一个带持久 ACP 会话与 session/resume 的
// 稳定版）。无需注入 permission preset 或单独的 authenticate 步骤：initialize 返回空的
// authMethods，握手不再要求其它东西。

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
	"time"
)

// zeroclawBlockedArgs 是 daemon 写死的参数清单，用户配置的 custom_args 不得覆盖它们。
// `acp` 是驱动 ACP JSON-RPC 传输的协议子命令，覆盖它会破坏 daemon↔ZeroClaw 的通信
// 契约。`--help`/`-h` 与 login/auth 类参数会把 CLI 切进一个「永远不会启动 ACP 服务」的
// 模式。
//
// `--agent` / `--agent-alias` 被拦是因为 `zeroclaw acp` 根本没有这个 flag——clap 会以
// `error: unexpected argument '--agent' found` 直接终止进程。takeZeroclawAgentAlias 会
// 在本次过滤运行之前先把它从 custom_args 里消费掉，并把值转成 session/new 的
// agentAlias 参数；这些条目防止同一份副本经任何其它路径（比如运行时启动前缀）到达
// argv。
var zeroclawBlockedArgs = map[string]blockedArgMode{
	"acp":           blockedStandalone,
	"--help":        blockedStandalone,
	"-h":            blockedStandalone,
	"login":         blockedStandalone,
	"auth":          blockedStandalone,
	"--login":       blockedStandalone,
	"--auth":        blockedStandalone,
	"--agent":       blockedWithValue,
	"--agent-alias": blockedWithValue,
}

// takeZeroclawAgentAlias 从 custom_args 里取出 ZeroClaw 的 agent 别名，返回别名与剩余
// 参数。
//
// ZeroClaw 把每个 ACP 会话都绑到一个 agent 别名上，解析顺序是：session/new 的
// agentAlias 参数 → `[acp].default_agent` → 当配置里恰好只有一条 `[agents.<alias>]` 时
// 自动选择。三者都没有时 session/new 会以 -32602 失败。这个别名无法走 argv 传递（见
// zeroclawBlockedArgs），所以 custom_args 是唯一面向操作员的通道，值在这里被取出。
//
// `--agent x` 与 `--agent=x` 两种写法都接受，`--agent-alias` 是全拼；最后一次出现胜出，
// 这样按 agent 的条目可以覆盖运行时全局的一条。空白值视为未设置：ZeroClaw 同样会
// trim 并丢弃空别名，传一个空的过去反而会丧失它的自动选择。
func takeZeroclawAgentAlias(args []string) (string, []string) {
	if len(args) == 0 {
		return "", args
	}
	alias := ""
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := unshellQuoteArg(args[i])
		flag, value := arg, ""
		hasInlineValue := false
		if idx := strings.Index(arg, "="); idx > 0 {
			flag, value, hasInlineValue = arg[:idx], arg[idx+1:], true
		}
		if flag != "--agent" && flag != "--agent-alias" {
			rest = append(rest, args[i])
			continue
		}
		if !hasInlineValue {
			if i+1 >= len(args) {
				continue
			}
			i++
			value = unshellQuoteArg(args[i])
		}
		alias = strings.TrimSpace(value)
	}
	return alias, rest
}

// zeroclawResumeSupported 读取 ZeroClaw 派生出的能力：其持久会话存储是否成功打开。
// 配置目录只读或不可写时，即使二进制版本够新也不会有 resume 能力。
func zeroclawResumeSupported(result json.RawMessage) bool {
	var r struct {
		AgentCapabilities struct {
			SessionCapabilities struct {
				Resume *struct{} `json:"resume"`
			} `json:"sessionCapabilities"`
		} `json:"agentCapabilities"`
	}
	return json.Unmarshal(result, &r) == nil && r.AgentCapabilities.SessionCapabilities.Resume != nil
}

// selectZeroclawPermissionOption 让普通的一次性工具批准走共享 ACP 策略，但对 ZeroClaw
// 遗留的结构化提问桥 fail-closed：那个桥把每个答案都标成 `choice-N` 且 allow_once，
// 若走通用自动批准，会静默地把每个问题都选成第一个选项。返回 ok=false 会让共享传输发
// 一条协议错误；即便选那个标着 reject_once 的选项，ZeroClaw 仍会把它映射成真实答案，
// 所以「选择」它等于静默选最后一个答案，而不是拒绝问题。
func selectZeroclawPermissionOption(params json.RawMessage) (optionID string, grant bool, ok bool) {
	var p struct {
		Options []acpPermissionOption `json:"options"`
	}
	if json.Unmarshal(params, &p) != nil {
		return "", false, false
	}

	isLegacyChoice := len(p.Options) >= 2
	for _, opt := range p.Options {
		if !strings.HasPrefix(opt.OptionID, "choice-") {
			isLegacyChoice = false
			break
		}
	}
	if !isLegacyChoice {
		return selectACPPermissionOption(params)
	}
	return "", false, false
}

// zeroclawSessionNewErrorMessage 把 ZeroClaw 的「选 agent 失败」翻译成操作员能照着办的
// 提示。
//
// 当配置里不是恰好一个 agent 时（包括零个 agent 的场合——此时它要一个根本不存在的条目
// 的别名），session/new 就强制要求 `agentAlias`，而没有任何 CLI flag 能补这个参数，所以
// 光秃秃的 RPC 错误没有给出任何前进路线。
func zeroclawSessionNewErrorMessage(err error) string {
	base := fmt.Sprintf("zeroclaw session/new failed: %v", err)
	var rpcErr *acpRPCError
	if !errors.As(err, &rpcErr) || !strings.Contains(rpcErr.Message, "agentAlias") {
		return base
	}
	return base + " — ZeroClaw auto-selects an agent only when its config holds exactly one [agents.<alias>] entry." +
		" Configure the agent you want, then name it with `--agent <alias>` in this runtime's custom args" +
		" or set `[acp].default_agent` in ZeroClaw's config."
}

// zeroclawBackend 实现 Backend 接口：拉起 `zeroclaw acp` 子进程，用标准 ACP
// JSON-RPC 2.0（stdin/stdout）与它通信。
//
// ZeroClaw 是 Rust 写的单二进制通用 agent 运行时（见 multica-ai/multica#1543）。其 ACP
// 服务表面与 Hermes/Kimi/Reasonix/Dim/Traecli/Grok/QwenPaw/MCode 一致，因此直接复用
// 共享的 hermesClient ACP 传输——差异仅在二进制名、会话引导与工具名抽取。已用真实
// ZeroClaw 0.8.4 二进制（stdio 驱动）验证，握手并非教科书式 ACP，本文件就围绕那些
// 偏离点构建，细节清单见文件顶部导读。
type zeroclawBackend struct {
	cfg Config
}

var (
	zeroclawReaderDrainGrace      = 2 * time.Second
	zeroclawNotificationQuietTime = 250 * time.Millisecond
)

// zeroclawMessageStream 给消息发送与最终关闭上锁串行化，避免迟到的 stdout 读取者往
// 已关闭的 channel 上发送。与 dim/grok/traecli 的做法一致。
type zeroclawMessageStream struct {
	ch     chan Message
	mu     sync.Mutex
	closed bool
}

// newZeroclawMessageStream 创建一个容量为 size 的消息流。
func newZeroclawMessageStream(size int) *zeroclawMessageStream {
	return &zeroclawMessageStream{ch: make(chan Message, size)}
}

// send 非阻塞地把一条消息投进 channel；流已关闭则直接丢弃。
func (s *zeroclawMessageStream) send(msg Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	trySend(s.ch, msg)
}

// close 关闭消息流；重复调用安全（第二次直接返回）。
func (s *zeroclawMessageStream) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	close(s.ch)
}

// Execute 是 Backend 接口的入口：拿一条用户 prompt，起一个 zeroclaw acp 进程，
// 完成一次 ACP 会话并跑完这一轮对话。
//
// 流程：确认可执行文件存在 → 从 custom_args 里取出 agent 别名 → 起子进程并接管三路
// IO → initialize 握手（顺带探测 resume 能力）→ 建立会话：续跑走 session/resume、
// 新建走 session/new（按需带 agentAlias）→ session/prompt 进入对话回合，期间把 ACP
// 通知翻译成 Message 流式发出，并用 streamingCurrentTurn 闸门挡住本回合之外的杂音 →
// 回合结束后有界等待尾部通知（drain），再依据 stderr / 文本流里的上游 LLM 终态失败
// 信号决定是否把 completed 降级为 failed → 汇总 Result 交给调用方。
//
// ZeroClaw 忽略 Multica 侧传入的 MCP 服务器（见下面警告），只认它自己配置目录里的。
func (b *zeroclawBackend) Execute(ctx context.Context, prompt string, opts ExecOptions) (*Session, error) {
	execPath := b.cfg.ExecutablePath
	if execPath == "" {
		execPath = "zeroclaw"
	}
	if _, err := exec.LookPath(execPath); err != nil {
		return nil, fmt.Errorf("zeroclaw executable not found at %q: %w", execPath, err)
	}

	// ZeroClaw 丢弃客户端提供的 MCP 服务器：它的 session/new、session/load、
	// session/resume 都不读 `params.mcpServers`，而开启 `agents.<alias>.acp_enable_mcp`
	// 初始化的是那个 agent 自己的 `mcp_bundles`，不是我们的。所以 MCP 完全活在
	// ZeroClaw 的配置目录里。providerSupportsMcpConfig 为本运行时隐藏了 MCP 标签页，
	// 因此能走到这里说明是更早存下的值——只警告并继续，别为一个兑现不了的配置把任务
	// 卡死。
	if len(opts.McpConfig) > 0 {
		b.cfg.Logger.Warn("zeroclaw ignores MCP servers supplied by Multica; its ACP server reads MCP only from its own config-dir ([[mcp.servers]] + [mcp_bundles.*] + agents.<alias>.mcp_bundles with acp_enable_mcp = true)",
			"backend", "zeroclaw",
		)
	}

	timeout := opts.Timeout
	runCtx, cancel := runContext(ctx, timeout)

	agentAlias, customArgs := takeZeroclawAgentAlias(opts.CustomArgs)
	zeroclawArgs := append([]string{"acp"}, filterCustomArgs(customArgs, zeroclawBlockedArgs, b.cfg.Logger)...)

	cmd := b.cfg.commandAt(execPath).exec(runCtx, zeroclawArgs...)
	hideAgentWindow(cmd)
	b.cfg.logAgentCommand(cmd, newAgentCommandLogArgs(zeroclawArgs, trustAgentCommandPositional(0, "acp")))
	if opts.Cwd != "" {
		cmd.Dir = opts.Cwd
	}
	cmd.Env = buildEnv(b.cfg.Env)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("zeroclaw stdout pipe: %w", err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("zeroclaw stdin pipe: %w", err)
	}
	// StderrPipe + 显式 copier 提供了先于「失败升级决策」的汇合点（stderrDone）；
	// 为什么 io.MultiWriter 形式会在高负载下与 stopReason=end_turn 竞争，见 hermes.go。
	providerErr := newACPProviderErrorSniffer("zeroclaw")
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("zeroclaw stderr pipe: %w", err)
	}

	if err := startOwnedProcessTree(cmd, b.cfg.Logger); err != nil {
		cancel()
		return nil, fmt.Errorf("start zeroclaw: %w", err)
	}

	stderrSink := io.MultiWriter(newLogWriter(b.cfg.Logger, "[zeroclaw:stderr] "), providerErr)
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		_, _ = io.Copy(stderrSink, stderr)
	}()

	b.cfg.Logger.Info("zeroclaw acp started", "pid", cmd.Process.Pid, "cwd", opts.Cwd)

	msgStream := newZeroclawMessageStream(256)
	resCh := make(chan Result, 1)

	// ZeroClaw 把中途叙述与最终答案都用同一种 agent_message_chunk 类型流式送出；该
	// tracker 只为 Result.Output 保留「最后一次工具调用之后的文本块」，同时保留全文供
	// 错误检测用。
	var deliverable acpDeliverableTracker
	// streamingCurrentTurn 是每一份 session 更新的闸门：运行方在我们回合之外推来的任何
	// 东西都被丢弃，不落进 deliverable。它必须默认 false——ZeroClaw 会在回答触发它的
	// 请求之前，先把这里要紧的帧（比如 session/load 的 transcript 重放）刷出来，所以
	// 闸门从进程一开始就得关着；只在临发 session/prompt 前才翻成 true。
	var streamingCurrentTurn atomic.Bool

	promptDone := make(chan hermesPromptResult, 1)
	activity := make(chan struct{}, 1)

	c := &hermesClient{
		cfg:              b.cfg,
		stdin:            stdin,
		pending:          make(map[int]*pendingRPC),
		pendingTools:     make(map[string]*pendingToolCall),
		selectPermission: selectZeroclawPermissionOption,
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
				// 工具标题与 kimi/traecli/grok/dim 一样再归一化一次，让 UI 看到统一的
				// snake_case 名。
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
		c.closeAllPending(fmt.Errorf("zeroclaw process exited"))
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
		var effectiveModel string

		// 刻意不带 elicitation 能力：这个无头客户端没法收集用户的表单应答。ZeroClaw
		// 遗留的结构化选择题桥由 selectZeroclawPermissionOption 以 fail-closed 兜底。
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
			finalError = fmt.Sprintf("zeroclaw initialize failed: %v", err)
			resCh <- Result{Status: finalStatus, Error: finalError, DurationMs: time.Since(startTime).Milliseconds()}
			return
		}

		cwd := opts.Cwd
		if cwd == "" {
			cwd = "."
		}

		if opts.ResumeSessionID != "" && !zeroclawResumeSupported(initResult) {
			b.cfg.Logger.Warn("zeroclaw persistence is unavailable; the daemon will retry from a rebuilt fresh-session context",
				"backend", "zeroclaw",
				"requested_session", opts.ResumeSessionID,
			)
			resumeRejected = true
			resCh <- Result{
				Status:         "failed",
				Error:          "zeroclaw session/resume unavailable: initialize did not advertise sessionCapabilities.resume",
				DurationMs:     time.Since(startTime).Milliseconds(),
				ResumeRejected: resumeRejected,
			}
			return
		}

		if opts.ResumeSessionID != "" {
			// 用 session/resume 而不是 session/load。两者都会把 transcript 恢复进 agent、
			// 都回一个裸 `{}`；但 load 还会把每条留存的消息以 session/update 通知重放给
			// 我们，于是续跑回合会把上一轮的回答当成自己的输出再发一遍。
			result, err := c.request(runCtx, "session/resume", map[string]any{
				"sessionId": opts.ResumeSessionID,
			})
			if err != nil {
				if isACPSessionNotFound(err) {
					b.cfg.Logger.Warn("zeroclaw resumed session not found; the daemon will retry fresh",
						"backend", "zeroclaw",
						"requested_session", opts.ResumeSessionID,
					)
					resumeRejected = true
					resCh <- Result{Status: "failed", Error: fmt.Sprintf("zeroclaw session/resume failed: %v", err), DurationMs: time.Since(startTime).Milliseconds(), ResumeRejected: resumeRejected}
					return
				}
				finalStatus = "failed"
				finalError = fmt.Sprintf("zeroclaw session/resume failed: %v", err)
				resCh <- Result{Status: finalStatus, Error: finalError, DurationMs: time.Since(startTime).Milliseconds(), ResumeRejected: resumeRejected}
				return
			}
			var changed bool
			sessionID, changed = resolveResumedSessionID(opts.ResumeSessionID, result)
			if changed {
				b.cfg.Logger.Warn("zeroclaw returned a different session id on resume — original was likely lost; continuing with the new id",
					"backend", "zeroclaw",
					"requested", opts.ResumeSessionID,
					"actual", sessionID,
				)
			}
		} else {
			// mcpServers 有意留空：ZeroClaw 从不读它。
			// agentAlias 只有操作员点名时才带上——发一个猜的值会丢掉 ZeroClaw 的
			// 「单 agent 自动选择」，把本来能用的单 agent 安装变成 `Unknown agent`。
			params := map[string]any{
				"cwd":        cwd,
				"mcpServers": []any{},
			}
			if agentAlias != "" {
				params["agentAlias"] = agentAlias
			}
			result, err := c.request(runCtx, "session/new", params)
			if err != nil {
				if runCtx.Err() == context.DeadlineExceeded {
					finalStatus = "timeout"
					finalError = fmt.Sprintf("zeroclaw timed out during session/new: %v", timeout)
				} else if runCtx.Err() == context.Canceled {
					finalStatus = "aborted"
					finalError = fmt.Sprintf("zeroclaw aborted: %v", err)
				} else {
					finalStatus = "failed"
					finalError = zeroclawSessionNewErrorMessage(err)
				}
				resCh <- Result{Status: finalStatus, Error: finalError, DurationMs: time.Since(startTime).Milliseconds()}
				return
			}
			sessionID = extractACPSessionID(result)
			if sessionID == "" {
				finalStatus = "failed"
				finalError = "zeroclaw session/new returned no session ID"
				resCh <- Result{Status: finalStatus, Error: finalError, DurationMs: time.Since(startTime).Milliseconds()}
				return
			}
		}

		c.sessionID = sessionID
		// 尽早把会话 id 作为 MessageStatus 发出去，这样即使运行被取消，续跑指针也已保存。
		msgStream.send(Message{Type: MessageStatus, Status: "running", SessionID: sessionID})
		b.cfg.Logger.Info("zeroclaw session ready", "session_id", sessionID)

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
				finalError = fmt.Sprintf("zeroclaw timed out after %s", timeout)
			} else if runCtx.Err() == context.Canceled {
				finalStatus = "aborted"
				finalError = "execution cancelled"
			} else {
				finalStatus = "failed"
				finalError = fmt.Sprintf("zeroclaw session/prompt failed: %v", err)
				if opts.ResumeSessionID != "" && isACPSessionNotFound(err) {
					b.cfg.Logger.Warn("resumed session not found at prompt time; clearing session id so the daemon retries fresh",
						"backend", "zeroclaw",
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
					finalError = "zeroclaw cancelled the prompt"
				}
				effectiveModel = pr.modelID
				c.mergeUsage(pr.usage)
			default:
			}
			// 给 stdout 读取者一个有界的时机去消费 zeroclaw 可能在 session/prompt 返回
			// 之后紧接着发出的通知（agent_message_chunk、用量更新）。否则在响应边界处关闭
			// stdin 会与读取者竞争，截断最终文本——这正是 hermes/dim/grok 修过的同一个
			// 竞态。
			waitForACPNotificationQuiescence(runCtx, activity, readerDone, zeroclawNotificationQuietTime, zeroclawReaderDrainGrace)
		}

		duration := time.Since(startTime)
		b.cfg.Logger.Info("zeroclaw finished", "pid", cmd.Process.Pid, "status", finalStatus, "duration", duration.Round(time.Millisecond).String())

		stdin.Close()
		cancel()

		// ZeroClaw 的 ACP 服务在 session/prompt 返回后可能还会让进程——连带 stdout/stderr
		// 管道——多开一小会儿。给排空设个上界。
		drainCtx, drainCancel := context.WithTimeout(context.Background(), zeroclawReaderDrainGrace)
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

		// ZeroClaw 0.8.4 在 ACP 上不报 token 计数——它的 prompt 结果只有 {sessionId,
		// stopReason, content}——所以今天 usageMap 保持 nil。若将来的响应报了用量却没有
		// 每回合模型 id，就记到 "unknown" 名下：initialize.defaultModel 是全进程级的，
		// 可能属于另一个 agent 别名，不可靠。
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
