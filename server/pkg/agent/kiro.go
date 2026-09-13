package agent

// 文件职责：本文件实现 Backend 接口中的「Kiro」后端。Kiro 是 xAI 的命令行编程
// agent，走标准的 ACP（Agent Client Protocol）JSON-RPC 2.0 协议：我们拉起
// `kiro-cli acp` 子命令，通过它的 stdin/stdout 收发请求（initialize、session/new、
// session/load、session/set_model、session/prompt），把 ACP 的通知流翻译成系统统一
// 的 Message 事件（文本、思考、工具调用与结果），最后汇总出 Result。
//
// 与 hermes.go / kimi.go 的 ACP 客户端（hermesClient）高度复用——Kiro 声明支持
// loadSession、从 session/new 返回模型、支持 session/set_model，所以同一套 ACP 驱动
// 只需做 provider 特有的启动参数和工具名归一化即可。
//
// 本文件特有的难点：Kiro 在任务实质完成后、若模型没能产出收尾回合，会抛
// session/prompt -32603「failed to generate a response」。这类「收尾工具（goal_complete
// 或 `multica issue comment add`）已成功、但收尾失败」的运行，需要用「最近一次收尾工具
// 结果确为 completed」这一正向证据才能判为 completed，绝不能因为「没有结果」就当成功
// （相关 review：#5511 / MUL-4860）。识别工具调用也是从命令载荷本身下手，而不是依赖
// 适配层给出的工具名（见 #5509 / GH #5975）。

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// kiroReaderDrainGrace 限制回合在 session/prompt 响应之后最多等多久的尾部 ACP 通知。
// 用 var 而不是 const，是为了让测试能把它调短。与 qoderReaderDrainGrace /
// traecliReaderDrainGrace 相互对应。
var kiroReaderDrainGrace = 2 * time.Second

// kiroBlockedArgs 是 daemon 写死的参数清单，用户配置的 custom_args 不得覆盖它们。
// `acp` 是协议子命令；--trust-all-tools 覆盖的是 Kiro 在 CLI 层的工具闸门，而 ACP 层
// 的 session/request_permission 自动放行由 hermesClient 负责。注意 Kiro CLI 2.1.1 里
// `-a` 是 --trust-all-tools 的简写，不是 --agent；--agent 仍放行，让用户能选自定义
// Kiro agent。
var kiroBlockedArgs = map[string]blockedArgMode{
	"acp":               blockedStandalone,
	"-a":                blockedStandalone,
	"--trust-all-tools": blockedStandalone,
	"--trust-tools":     blockedWithValue,
}

// kiroBackend 实现 Backend 接口：通过拉起 `kiro-cli acp` 子进程，用标准的 ACP
// JSON-RPC 2.0 传输（走 stdin/stdout）与它通信。
//
// Kiro CLI 对外声明支持 loadSession、能在 session/new 里返回模型、也支持
// session/set_model，因此现有的 Hermes/Kimi ACP 客户端稍作适配（provider 特有的启动
// 参数 + 工具名归一化）就能驱动它。
type kiroBackend struct {
	cfg Config
}

// Execute 是 Backend 接口的入口：拿一条用户 prompt，起一个 kiro-cli acp 进程，
// 完成一次 ACP 会话并跑完这一轮对话。
//
// 流程：先确认可执行文件存在 → 把 mcp_config（Claude 式「对象套对象」）翻译成 ACP
// session/new、session/load 期望的数组形状 → 起子进程并接管三路 IO（stdout 读响应、
// stdin 写请求、stderr 喂给 provider 错误嗅探器）→ 依次完成 initialize / session 建
// 立（新建或续跑）/ 可选 set_model → 发 session/prompt 进入对话回合，期间把 ACP 通知
// 翻译成 Message 流式发出 → 回合结束后等待尾部通知静默，再依据 stderr / 文本流里的
// 上游 LLM 终态失败信号决定是否把 completed 降级为 failed → 汇总 Result 交给调用方。
//
// 中途失败一律把 Result 直接放进 resCh 返回（状态 failed）；超时/取消会被识别成
// timeout / aborted。新建会话拿不到 sessionId、set_model 失败都会判为失败。
func (b *kiroBackend) Execute(ctx context.Context, prompt string, opts ExecOptions) (*Session, error) {
	execPath := b.cfg.ExecutablePath
	if execPath == "" {
		execPath = "kiro-cli"
	}
	if _, err := exec.LookPath(execPath); err != nil {
		return nil, fmt.Errorf("kiro executable not found at %q: %w", execPath, err)
	}

	// 把 agent 的 mcp_config（Claude 风格的「对象套对象」结构）翻译成 ACP 的
	// session/new 和 session/load 期望的数组形状。格式非法时宁可启动失败，让真正的
	// 错误暴露在启动阶段，也不能静默丢掉全部 MCP 服务器。
	mcpServers, err := buildACPMcpServers(opts.McpConfig, b.cfg.Logger)
	if err != nil {
		return nil, fmt.Errorf("kiro: invalid mcp_config: %w", err)
	}

	timeout := opts.Timeout
	runCtx, cancel := runContext(ctx, timeout)

	kiroArgs := append([]string{"acp", "--trust-all-tools"}, filterCustomArgs(opts.CustomArgs, kiroBlockedArgs, b.cfg.Logger)...)
	cmd := b.cfg.commandAt(execPath).exec(runCtx, kiroArgs...)
	hideAgentWindow(cmd)
	b.cfg.logAgentCommand(cmd, newAgentCommandLogArgs(kiroArgs, trustAgentCommandPositional(0, "acp")))
	if opts.Cwd != "" {
		cmd.Dir = opts.Cwd
	}
	cmd.Env = buildEnv(b.cfg.Env)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("kiro stdout pipe: %w", err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("kiro stdin pipe: %w", err)
	}
	// 这里用 StderrPipe + 显式 copier，能拿到一个先于「把失败升级为最终失败」决策触发
	// 的汇合点（stderrDone）；为什么 io.MultiWriter 的形式会在高负载下与
	// stopReason=end_turn 竞争，见 hermes.go 里对应的注释。
	providerErr := newACPProviderErrorSniffer("kiro")
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("kiro stderr pipe: %w", err)
	}

	if err := startOwnedProcessTree(cmd, b.cfg.Logger); err != nil {
		cancel()
		return nil, fmt.Errorf("start kiro: %w", err)
	}

	stderrSink := io.MultiWriter(newLogWriter(b.cfg.Logger, "[kiro:stderr] "), providerErr)
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		_, _ = io.Copy(stderrSink, stderr)
	}()

	b.cfg.Logger.Info("kiro acp started", "pid", cmd.Process.Pid, "cwd", opts.Cwd)

	msgCh := make(chan Message, 256)
	resCh := make(chan Result, 1)

	// Kiro 把中途的叙述性文本和最终答案都用同一种 AgentMessageChunk 类型流式送出；
	// 这个 tracker 只为 Result.Output 保留「最后一次工具调用之后的文本块」，
	// 同时保留全文供错误检测使用。
	var deliverable acpDeliverableTracker
	var streamingCurrentTurn atomic.Bool
	// 处理 -32603 关闭握手时的「成果保留」状态。
	//
	// Kiro 在任务实际已经干完、但模型没能产出收尾回合时，会抛
	// `session/prompt -32603 "failed to generate a response"`。只有在握有「收尾步骤
	// 确实成功」的正向证据时，我们才把这轮运行判为 completed——绝不能仅凭「没有结果」
	// 就当成功。命令中途崩溃或会话内部失败，产生的同样是「工具用了、没有结果、-32603」
	// 的形状，若把这种也当成功，等于把一个没干完的任务标记成完成（见 #5511 /
	// MUL-4860 的评审）。
	//
	// 本层唯一拿得到的正向证据，是收尾工具（goal_complete 或 `multica issue comment
	// add`）返回了一条终态 ToolResult 且 status=="completed"。我们记录「最近一次」这类
	// 结果的状态（按 CallID 键控、有先后次序），这样后面一条失败的结果能正确覆盖前面
	// 的成功——比如「进度评论完成 → 最终评论失败 → -32603」必须保持失败，而
	// 「第一次尝试失败 → 重试完成 → -32603」才是真完成。
	var goalCompleteCallIDs sync.Map
	var issueCommentCallIDs sync.Map
	var finishingMu sync.Mutex
	var lastFinishingResultStatus string // "", "completed", 或 "failed"

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
				msg.Tool = kiroToolNameFromTitle(msg.Tool)
				if msg.Tool == "goal_complete" && msg.CallID != "" {
					goalCompleteCallIDs.Store(msg.CallID, struct{}{})
				}
				// 按命令载荷识别 `multica issue comment add`，不依赖适配层怎么给工具起名。
				// GPT-5.6 Sol 可能把 shell 工具标成 kiroToolNameFromTitle 归不进
				// "terminal" 的名字（比如 "execute"/"run"），如果这里只拦 msg.Tool==
				// "terminal" 就会整个漏掉（issue #5509）。仍然要求带 CallID，以便把这次
				// 工具调用和它的结果对上号。
				if msg.CallID != "" && isKiroIssueCommentAddTool(msg) {
					issueCommentCallIDs.Store(msg.CallID, struct{}{})
				}
			}
			if msg.Type == MessageToolResult {
				_, isGoal := goalCompleteCallIDs.LoadAndDelete(msg.CallID)
				_, isComment := issueCommentCallIDs.LoadAndDelete(msg.CallID)
				if isGoal || isComment {
					// 记录这个收尾工具的终态结果。最近一次结果胜出，所以守卫逻辑看到的
					// 是最后一次投递的状态：后到的失败能覆盖早先的成功。
					finishingMu.Lock()
					lastFinishingResultStatus = msg.Status
					finishingMu.Unlock()
				}
			}
			deliverable.observe(msg)
			trySend(msgCh, msg)
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
		c.closeAllPending(fmt.Errorf("kiro process exited"))
	}()

	go func() {
		defer cancel()
		defer close(msgCh)
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
		// 仅在「ACP 运行时不认我们请求续跑的那个会话」时置位。只有这种情况可以通过另起
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
			finalError = fmt.Sprintf("kiro initialize failed: %v", err)
			resCh <- Result{Status: finalStatus, Error: finalError, DurationMs: time.Since(startTime).Milliseconds()}
			return
		}

		// 把 mcpServers 里「运行方没对外声明支持远程传输」的条目丢掉。
		// 为什么对只支持 stdio 的 ACP 运行方无条件发 http/sse 会拖垮整个 session/new，
		// 见 hermes.go 里对应的注释。
		mcpServers = filterACPMcpServersByCapability(mcpServers, extractACPMcpCapabilities(initResult), "kiro", b.cfg)

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
				finalError = fmt.Sprintf("kiro session/load failed: %v", err)
				resCh <- Result{Status: finalStatus, Error: finalError, DurationMs: time.Since(startTime).Milliseconds()}
				return
			}
			// 套用与 kimi/hermes 相同的防御性解析：若 kiro 在 session/load 响应里回显了
			// sessionId，就优先用它（那是后端真正承诺的规范 id）。当响应为空或不含
			// sessionId——即 kiro 当前观察到的形态——辅助函数会退回用请求时的 id，
			// 维持现状。在这里一并修正，意味着将来某个 kiro 在「静默状态重置」时返回
			// 了不同 id，也能和 hermes/kimi 一样被正确处理。
			var changed bool
			sessionID, changed = resolveResumedSessionID(opts.ResumeSessionID, result)
			if changed {
				b.cfg.Logger.Warn("agent returned a different session id on resume — original was likely lost; continuing with the new id",
					"backend", "kiro",
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
				finalError = fmt.Sprintf("kiro session/new failed: %v", err)
				resCh <- Result{Status: finalStatus, Error: finalError, DurationMs: time.Since(startTime).Milliseconds()}
				return
			}
			sessionID = extractACPSessionID(result)
			if sessionID == "" {
				finalStatus = "failed"
				finalError = "kiro session/new returned no session ID"
				resCh <- Result{Status: finalStatus, Error: finalError, DurationMs: time.Since(startTime).Milliseconds()}
				return
			}
			if effectiveModel == "" {
				effectiveModel = extractACPCurrentModelID(result)
			}
		}

		c.sessionID = sessionID
		b.cfg.Logger.Info("kiro session created", "session_id", sessionID)

		if opts.Model != "" {
			if _, err := c.request(runCtx, "session/set_model", map[string]any{
				"sessionId": sessionID,
				"modelId":   opts.Model,
			}); err != nil {
				b.cfg.Logger.Warn("kiro set_session_model failed", "error", err, "requested_model", opts.Model)
				finalStatus = "failed"
				finalError = fmt.Sprintf("kiro could not switch to model %q: %v", opts.Model, err)
				if opts.ResumeSessionID != "" && isACPSessionNotFound(err) {
					// 续跑会话且带了模型覆盖时，那个「已死的会话」是在这里暴露，而不是
					// 等到 session/prompt 才暴露。与下面 prompt 路径的处理一样：清空
					// session id，让 daemon 的「续跑失败回退」逻辑用全新会话重试。
					b.cfg.Logger.Warn("resumed session not found at set_model time; clearing session id so the daemon retries fresh",
						"backend", "kiro",
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
			b.cfg.Logger.Info("kiro session model set", "model", opts.Model)
		}

		userText := prompt
		if opts.SystemPrompt != "" {
			userText = opts.SystemPrompt + "\n\n---\n\n" + prompt
		}

		promptBlocks := []map[string]any{
			{"type": "text", "text": userText},
		}
		// Kiro 的公开文档用的是 `content` 字段，而 Kiro CLI 2.1.1 仍然只认标准 ACP 的
		// `prompt` 字段。两个都发，任一线协议形态都能驱动回合。
		// TODO: Kiro 收敛到单一规范载荷后删掉其中一个字段。
		streamingCurrentTurn.Store(true)
		_, err = c.request(runCtx, "session/prompt", map[string]any{
			"sessionId": sessionID,
			"content":   promptBlocks,
			"prompt":    promptBlocks,
		})
		if err != nil {
			if runCtx.Err() == context.DeadlineExceeded {
				finalStatus = "timeout"
				finalError = fmt.Sprintf("kiro timed out after %s", timeout)
			} else if runCtx.Err() == context.Canceled {
				finalStatus = "aborted"
				finalError = "execution cancelled"
			} else {
				finalStatus = "failed"
				finalError = fmt.Sprintf("kiro session/prompt failed: %v", err)
				// 只有「正向证据」才保留 completed：我们观察到的最近一次收尾工具的
				// ToolResult 是 status=="completed"，且搭配的是特定的 -32603 关闭握手。
				// 缺结果不是证据——命令中途崩溃也会产生同样无结果的形状（必修项 #1）——
				// 而且以最近一次结果为准，后到的失败会覆盖先前的成功（必修项 #2）。
				finishingMu.Lock()
				lastFinishing := lastFinishingResultStatus
				finishingMu.Unlock()
				if lastFinishing == "completed" && isKiroGoalCompleteCloseError(err) {
					b.cfg.Logger.Warn("kiro session/prompt failed after a completed finishing-tool result; preserving completed task status", "error", err)
					finalStatus = "completed"
					finalError = ""
				} else if opts.ResumeSessionID != "" && isACPSessionNotFound(err) {
					// 参见 hermes 后端：即使会话已不存在，运行方也会从 session/resume
					// 原样回显请求的 id，于是那个失效 id 只会在 prompt 时刻才报错。把
					// SessionID 清空，daemon 的「续跑失败回退」就能用全新会话重试并保存
					// 替换后的 id。
					b.cfg.Logger.Warn("resumed session not found at prompt time; clearing session id so the daemon retries fresh",
						"backend", "kiro",
						"session_id", sessionID,
					)
					sessionID = ""
					resumeRejected = true
				} else if opts.ResumeSessionID != "" && isKiroOversizedHistoryImage(err) {
					// 续跑会话的历史里若含有一张超出 provider 最大像素尺寸的图片，每次
					// session/prompt 都会重放这张图并被拒，且是在当前回合开跑之前就被拒
					// （GH #5975）。与会话缺失不同，这里的 transcript 还在，所以对续跑
					// 路径是永久性的：只有不带 resume id 的全新会话能恢复。置
					// ResumeRejected 让 daemon 用冷会话重试一次；把（有毒的）session id
					// 保留在结果里便于审计——daemon 依据的是布尔值而不是 id 是否为空来
					// 决定重试，而一次成功的全新重试会用新 id 覆盖它。
					b.cfg.Logger.Warn("resumed session has an oversized historical image the provider rejects; signaling resume rejection so the daemon retries with a fresh session",
						"backend", "kiro",
						"session_id", sessionID,
					)
					resumeRejected = true
				}
			}
		} else {
			select {
			case pr := <-promptDone:
				if pr.stopReason == "cancelled" {
					finalStatus = "aborted"
					finalError = "kiro cancelled the prompt"
				}
				c.mergeUsage(pr.usage)
			default:
			}
			waitForACPNotificationQuiescence(runCtx, activity, readerDone, acpNotificationQuietTime, kiroReaderDrainGrace)
		}

		duration := time.Since(startTime)
		b.cfg.Logger.Info("kiro finished", "pid", cmd.Process.Pid, "status", finalStatus, "duration", duration.Round(time.Millisecond).String())

		stdin.Close()
		cancel()

		<-readerDone
		// 在咨询 provider 错误嗅探器之前，先确保 stderr 拷贝协程已排空；失败模式见
		// hermes.go。
		<-stderrDone

		finalOutput, providerErrorOutput := deliverable.result()

		// 当 stderr 或 agent 文本流里出现了上游 LLM 的终态失败（HTTP 4xx / 限流 /
		// token 过期）时，把 completed 升级为 failed。完整信号集见辅助函数文档；关键的
		// 安全性质是：瞬时的「单次尝试告警后重试成功」仍保持 completed。这里读的是全文
		// 流而不是 deliverable，所以发生在一次工具调用之前、直接放弃的回合也仍然可见。
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

	return &Session{Messages: msgCh, Result: resCh}, nil
}

// isKiroGoalCompleteCloseError 判断 err 是不是 Kiro 那类特殊的 -32603 关闭握手错误：
// 任务是 session/prompt，错误码 -32603，消息归一化为 "Internal error"，且 data 里带
// "failed to generate a response"。命中它说明模型是在工作收尾之后没能产出收尾回合，
// 配合「收尾工具结果已完成」的正向证据即可保住 completed（见 Execute 里的守卫）。
func isKiroGoalCompleteCloseError(err error) bool {
	var rpcErr *acpRPCError
	if !errors.As(err, &rpcErr) {
		return false
	}
	if rpcErr.Method != "session/prompt" || rpcErr.Code != -32603 {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(rpcErr.Message), "Internal error") {
		return false
	}
	return strings.Contains(strings.ToLower(rpcErr.Data), "failed to generate a response")
}

// isKiroOversizedHistoryImage 判断 err 是否为 Kiro/上游拒绝一个续跑会话——原因是会话
// 历史里已烙入的一张图片超过了 provider 允许的最大像素尺寸。Kiro 在 session/prompt
// 时刻把它抛成 -32603，其 `data` 会点名出错的那块 messages[n].content[m].image.
// source.base64.data 和尺寸上限（GH #5975），例如：
//
//	session/prompt: Internal error (code=-32603, data=Encountered an error in
//	the response stream: messages.14.content.0.image.source.base64.data: At
//	least one of the image dimensions exceed max allowed size: 8000 pixels)
//
// 两个特征必须同时出现，才不会把无关的 -32603——比如一次瞬时的 "failed to
// generate a response" 关闭，或同样无结果的命令中途崩溃——误判成永久性的历史不兼容。
// 它与 isACPSessionNotFound 刻意区分：这里的会话是存在的，只是它历史里的多模态内容
// 不可用，所以恢复方式是「不带 resume id 另起全新会话」，而不是清掉一个已死的 id。
func isKiroOversizedHistoryImage(err error) bool {
	var rpcErr *acpRPCError
	if !errors.As(err, &rpcErr) {
		return false
	}
	if rpcErr.Method != "session/prompt" || rpcErr.Code != -32603 {
		return false
	}
	data := strings.ToLower(rpcErr.Data)
	return strings.Contains(data, "image.source.base64.data") &&
		strings.Contains(data, "image dimensions exceed max allowed size")
}

// isKiroIssueCommentAddTool 报告一条工具调用消息是否为 `multica issue comment add`
// 调用。它只看命令载荷，不看归一化后的工具名：GPT-5.6 Sol 适配层给 shell 工具起的
// 名字可能归不进 "terminal"（之前用 msg.Tool=="terminal" 拦截会静默漏掉它们，导致
// 已完成的任务被标成失败——见 #5509）。isKiroIssueCommentAddCommand 足够严格，
// 非 shell 工具的输入不会被误伤命中。
func isKiroIssueCommentAddTool(msg Message) bool {
	command, _ := msg.Input["command"].(string)
	return isKiroIssueCommentAddCommand(command)
}

// isKiroIssueCommentAddCommand 判断命令行字符串本身是不是
// `multica issue comment add`（可能带 `./` 前缀、绝对路径，或前导环境变量）。
func isKiroIssueCommentAddCommand(command string) bool {
	parts := trimLeadingEnvAssignments(strings.Fields(command))
	// 有的运行方会让终端调用绕一层 shell 包装，形如 `sh -c "multica issue comment
	// add ..."`；只拆一层这种包装，让真实的调用仍能被识别出来。
	if len(parts) >= 3 && isPOSIXShellName(parts[0]) && parts[1] == "-c" {
		inner := strings.Trim(strings.Join(parts[2:], " "), "\"'")
		parts = trimLeadingEnvAssignments(strings.Fields(inner))
	}
	if len(parts) < 4 {
		return false
	}
	executable := strings.TrimPrefix(parts[0], "./")
	if executable != "multica" && !strings.HasSuffix(executable, "/multica") {
		return false
	}
	return parts[1] == "issue" && parts[2] == "comment" && parts[3] == "add"
}

// trimLeadingEnvAssignments 丢掉开头的 `KEY=VALUE` 片段，使
// `MULTICA_TOKEN=x multica issue comment add ...` 这类调用仍能被识别。
func trimLeadingEnvAssignments(parts []string) []string {
	for len(parts) > 0 && isEnvAssignment(parts[0]) {
		parts = parts[1:]
	}
	return parts
}

// isEnvAssignment 判断 tok 是否是 `NAME=value` 形式的 shell 环境变量赋值：
// 等号前是合法标识符（字母/下划线开头，可含数字），等号在最前（eq<=0）则不是。
func isEnvAssignment(tok string) bool {
	eq := strings.IndexByte(tok, '=')
	if eq <= 0 {
		return false
	}
	for i := 0; i < eq; i++ {
		c := tok[i]
		switch {
		case c == '_', c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z':
		case i > 0 && c >= '0' && c <= '9':
		default:
			return false
		}
	}
	return true
}

// isPOSIXShellName 判断 tok 是否是接受 `-c <command>` 参数的 shell 程序名；
// 若带路径只取最后一段（basename）参与比对。
func isPOSIXShellName(tok string) bool {
	if i := strings.LastIndexByte(tok, '/'); i >= 0 {
		tok = tok[i+1:]
	}
	switch tok {
	case "sh", "bash", "zsh", "dash":
		return true
	default:
		return false
	}
}

// kiroToolNameFromTitle 把 Kiro/适配层给出的工具「标题」归一化成系统内部统一的工具名。
//
// 归一化步骤：先取冒号前的部分（适配层可能把标题写成「工具名: 意图描述」），再统一小
// 写后与已知别名对照；对不上号的兜底做法是把空格替换成下划线原样返回，例如
// "Read File" → "read_file"。
func kiroToolNameFromTitle(title string) string {
	t := strings.TrimSpace(title)
	if t == "" {
		return ""
	}

	if idx := strings.Index(t, ":"); idx > 0 {
		t = strings.TrimSpace(t[:idx])
	}

	lower := strings.ToLower(t)
	switch lower {
	case "read", "read file":
		return "read_file"
	case "write", "write file":
		return "write_file"
	case "edit", "patch":
		return "edit_file"
	case "shell", "bash", "terminal", "run command", "run shell command":
		return "terminal"
	case "grep", "search", "find":
		return "search_files"
	case "glob":
		return "glob"
	case "code":
		return "code"
	case "web search":
		return "web_search"
	case "fetch", "web fetch":
		return "web_fetch"
	case "todo", "todo write", "todo list", "todo_list":
		return "todo_write"
	}

	return strings.ReplaceAll(lower, " ", "_")
}
