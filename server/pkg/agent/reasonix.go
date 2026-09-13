package agent

// 文件导读：Reasonix CLI 的 ACP 驱动封装。
//
// 角色定位：把 Reasonix（https://github.com/esengine/DeepSeek-Reasonix，DeepSeek 推理系
// 智能体）适配成 daemon 的 Backend。Reasonix 通过 `reasonix acp` 子命令原生支持 ACP
// （Agent Client Protocol，基于 stdin/stdout 的 JSON-RPC 2.0），本文件拉起该子进程并驱动
// 完整的 ACP 会话生命周期（initialize → session/new|resume → session/prompt），把正文、
// 工具调用、权限请求、状态用量等翻译成 Message/Result 上抛。
//
// 职责边界：ACP 传输层直接复用 hermes 后端的 hermesClient（Reasonix 说的是同一套协议），
// 本文件只保留 Reasonix 特有的部分：权限选择策略（自动批准 vs 需要人工的提问/受保护决策）、
// 状态用量的 schema 解析、能力缺失的告警、以及错误语义（session 租约冲突、未配置等）。
//
// 核心约定：① 历史回放不重播——Reasonix 在 session/resume 时会补发前几轮的完整记录，并在
// 我们发出 session/prompt 前就可能冲刷积压的分片，因此用 streamingCurrentTurn 闸门把它们
// 全部丢弃，只有真正进入当前轮后才开始向 UI 转发；② ACP 的权限请求（含普通批准、受保护
// 决策与用户提问三类）在 Reasonix 里统一走 session/request_permission，无人值守任务里凡是
// 需要人工介入的一律拒绝并把问题带进结果；③ stderr 除了进 daemon 日志，还会被嗅探器
// 提取 provider 级错误，用于把「进程正常退出但上游 LLM 实际失败」提升为失败。

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// reasonixBlockedArgs 是 daemon 固定写死、不允许用户经 custom_args 覆盖的开关。其中 `acp`
// 是驱动 Reasonix CLI 的 ACP JSON-RPC 传输所需的协议子命令，覆盖它会破坏 daemon ↔ Reasonix
// 的通信契约。
var reasonixBlockedArgs = map[string]blockedArgMode{
	"acp":               blockedStandalone,
	"--model":           blockedWithValue,
	"--profile":         blockedWithValue,
	"--planner":         blockedWithValue,
	"--sandbox-network": blockedWithValue,
	"--sandbox-bash":    blockedWithValue,
	"--workspace-only":  blockedStandalone,
}

// reasonixACPLaunchArgs 返回 `reasonix acp` 的固定启动参数。
func reasonixACPLaunchArgs() []string {
	// 让 Reasonix 在宿主支持处启用 bash 沙箱；宿主不支持时按它自身的宿主配置降级。
	return []string{
		"acp",
		"--profile", "balanced",
		"--planner", "auto",
		"--sandbox-network", "auto",
		"--sandbox-bash", "auto",
		"--workspace-only",
	}
}

// reasonixReaderDrainGrace 限制轮次在 session/prompt 响应之后还等多久「尾部的 ACP 通知」。
// 用变量而非常量，是为了让测试能缩短它。与 qoderReaderDrainGrace / traecliReaderDrainGrace
// 的用意一致。
var reasonixReaderDrainGrace = 2 * time.Second

// reasonixBackend 通过拉起 `reasonix acp`、经 stdin/stdout 上的 ACP（Agent Client Protocol）
// JSON-RPC 2.0 通信来实现 Backend。
//
// Reasonix（https://github.com/esengine/DeepSeek-Reasonix）通过 `reasonix acp` 子命令原生
// 支持 ACP。这里复用现成的 hermesClient 传输层，但把 Reasonix 特有的权限、状态用量与错误
// 语义都收在本适配器内。
type reasonixBackend struct {
	cfg Config
}

// Execute 驱动一次完整的 Reasonix ACP 会话并返回其产出。
//
// 流程：定位可执行文件、把 mcp_config 翻译成 ACP session/new 期望的数组形态 → 拉起
// `reasonix acp` 子进程 → 后台线程读 stdout（每行一个 JSON-RPC 消息），主 goroutine 按
// initialize → session/new|resume → （可选）session/set_model / effort 覆盖 → session/prompt
// 的顺序驱动会话 → prompt 完成后等通知静默，把 stderr/provider 侧的终态失败信号合并进
// 最终状态 → 汇总正文、用量与 blocked question 生成 Result。
func (b *reasonixBackend) Execute(ctx context.Context, prompt string, opts ExecOptions) (*Session, error) {
	execPath := b.cfg.ExecutablePath
	if execPath == "" {
		execPath = "reasonix"
	}
	if _, err := exec.LookPath(execPath); err != nil {
		return nil, fmt.Errorf("reasonix executable not found at %q: %w", execPath, err)
	}

	// 把 agent 的 mcp_config（Claude 风格的对象套对象）翻译成 ACP `session/new` 期望的
	// 数组形态。畸形 JSON 要直接失败（fail closed），让启动把真实错误暴露出来，而不是静默
	// 丢掉全部 MCP 服务器。
	mcpServers, err := buildACPMcpServers(opts.McpConfig, b.cfg.Logger)
	if err != nil {
		return nil, fmt.Errorf("reasonix: invalid mcp_config: %w", err)
	}

	timeout := opts.Timeout
	runCtx, cancel := runContext(ctx, timeout)

	// Reasonix 把普通工具批准、受保护决策和用户提问都复用 session/request_permission 这一条
	// 通道。下面的适配器收窄了通用自动批准策略；--workspace-only 则独立地把「配置里额外允许
	// 的写根目录」挡在无人值守任务之外。
	reasonixArgs := append(reasonixACPLaunchArgs(), filterCustomArgs(opts.CustomArgs, reasonixBlockedArgs, b.cfg.Logger)...)
	cmd := b.cfg.commandAt(execPath).exec(runCtx, reasonixArgs...)
	hideAgentWindow(cmd)
	b.cfg.logAgentCommand(cmd, newAgentCommandLogArgs(reasonixArgs, trustAgentCommandPositional(0, "acp")))
	if opts.Cwd != "" {
		cmd.Dir = opts.Cwd
	}
	cmd.Env = buildEnv(b.cfg.Env)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("reasonix stdout pipe: %w", err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("reasonix stdin pipe: %w", err)
	}
	// stderr 既要进 daemon 日志，也要被嗅探器提取 provider 级错误，好把它们呈现到任务结果
	// 里——因为 ACP 的 prompt 响应本身不一定保留得下这些完整细节。
	//
	// 用 StderrPipe 加显式 copier 是为了拿到一个 join 点（stderrDone），它保证在「把失败
	// 提升到结果」的决策之前已经排干；为什么 io.MultiWriter 的形式在负载下会和
	// stopReason=end_turn 竞态，见 hermes.go 里的对应注释。
	providerErr := newACPProviderErrorSniffer("reasonix")
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("reasonix stderr pipe: %w", err)
	}

	if err := startOwnedProcessTree(cmd, b.cfg.Logger); err != nil {
		cancel()
		return nil, fmt.Errorf("start reasonix: %w", err)
	}

	stderrSink := io.MultiWriter(newLogWriter(b.cfg.Logger, "[reasonix:stderr] "), providerErr)
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		_, _ = io.Copy(stderrSink, stderr)
	}()

	b.cfg.Logger.Info("reasonix acp started", "pid", cmd.Process.Pid, "cwd", opts.Cwd)

	msgCh := make(chan Message, 256)
	resCh := make(chan Result, 1)

	// Reasonix 把中途叙述与最终答案都作为同一种 agent_message_chunk 类型流出；tracker 只
	// 保留「最后一次工具调用之后」的那段作为 Result.Output，同时保留全文供错误检测用。
	var deliverable acpDeliverableTracker
	// streamingCurrentTurn 闸住所有会话更新：历史回放（Reasonix 在 session/resume 时会补发
	// 前几轮的完整记录，也可能在我们 session/prompt 的响应流出前先冲刷积压分片）一律丢弃，
	// 避免把上一轮答案重复并进 output。只有在 session/prompt 发出后才置为 true。
	var streamingCurrentTurn atomic.Bool
	var blockedQuestion atomic.Value // string；由 stdout 读取方设置
	var statusUsage reasonixStatusUsageTracker

	promptDone := make(chan hermesPromptResult, 1)
	activity := make(chan struct{}, 1)

	// 复用 hermesClient 这个 ACP 传输层——Reasonix 说的是同一套协议。
	c := &hermesClient{
		cfg:          b.cfg,
		stdin:        stdin,
		pending:      make(map[int]*pendingRPC),
		pendingTools: make(map[string]*pendingToolCall),
		acceptNotification: func(string) bool {
			return streamingCurrentTurn.Load()
		},
		selectPermission: func(params json.RawMessage) (string, bool, bool) {
			if reasonixPermissionMetadataMissing(params) {
				b.cfg.Logger.Warn("reasonix permission request is missing trusted metadata; protected-decision detection may be degraded")
			}
			optionID, grant, ok, question := selectReasonixPermissionOption(params)
			if question != "" {
				blockedQuestion.Store(question)
			}
			return optionID, grant, ok
		},
		onNotification: func(method string, params json.RawMessage) {
			if streamingCurrentTurn.Load() {
				statusUsage.observe(method, params)
			}
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
			// hermesClient.handleToolCallStart 已经把原始 ACP title 经 hermesToolNameFromTitle
			// 映射过一遍——它覆盖 hermes 风格的小写 title（"read:"、"patch (replace)" 等），
			// 但不覆盖 Reasonix 风格的大写形式（"Read file: …"、"Run command: …"）。这里再
			// 归一化一次，让 UI 在两个后端看到一致的 snake_case 标识符；名字已是正常形态时
			// 是 no-op（例如已经映射成 "read_file"）。
			if msg.Type == MessageToolUse {
				msg.Tool = reasonixToolNameFromTitle(msg.Tool)
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

	// 后台开始读 stdout。
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			c.handleLine(line)
		}
		c.closeAllPending(fmt.Errorf("reasonix process exited"))
	}()

	// 在 goroutine 里驱动 ACP 会话生命周期。
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
		// 在「ACP 运行时拒绝了我们要求续接的会话」时置位。只有这种情况能靠新建会话治愈，
		// 所以下面遇到的握手/网络类失败必须保持它为 false。
		var resumeRejected bool

		// 1. 握手初始化。
		initResult, err := c.request(runCtx, "initialize", map[string]any{
			"protocolVersion": 1,
			"clientInfo": map[string]any{
				"name":    "multica-agent-sdk",
				"version": "0.2.0",
			},
			"clientCapabilities": map[string]any{},
		})
		if err != nil {
			finalStatus, finalError = reasonixRequestFailure(runCtx, timeout, fmt.Sprintf("reasonix initialize failed: %v", err))
			resCh <- Result{Status: finalStatus, Error: finalError, DurationMs: time.Since(startTime).Milliseconds()}
			return
		}
		warnReasonixCapabilityGaps(b.cfg.Logger, initResult)

		// 丢弃运行时未宣传其远程传输方式的 MCP 条目。原因见 hermes.go 的对应注释：把一个
		// http/sse 条目塞给只支持 stdio 的运行时，会拖垮整个 session/new。
		mcpServers = filterACPMcpServersByCapability(mcpServers, extractACPMcpCapabilities(initResult), "reasonix", b.cfg)

		// 2. 新建或续接会话。
		cwd := opts.Cwd
		if cwd == "" {
			cwd = "."
		}

		// sessionResult 是产生当前会话的那次 session/new 或 session/resume 的返回体。它带着
		// 下面 effort 步骤要读的 configOptions，所以两个分支都得把它留好。
		var sessionResult json.RawMessage

		if opts.ResumeSessionID != "" {
			// 按 ACP Session Setup 约定，session/resume 接受 mcpServers，运行时会在续接时
			// 一并重连它们。不传的话，同一 agent 上新建任务能用的 MCP 工具，续接任务反而
			// 会丢掉。
			result, err := c.request(runCtx, "session/resume", map[string]any{
				"cwd":        cwd,
				"sessionId":  opts.ResumeSessionID,
				"mcpServers": mcpServers,
			})
			if err != nil {
				finalStatus, finalError = reasonixRequestFailure(runCtx, timeout, fmt.Sprintf("reasonix session/resume failed: %v", err))
				if finalStatus == "failed" && isACPSessionNotFound(err) {
					resumeRejected = true
					finalError = fmt.Sprintf("reasonix session/resume rejected the saved session: %v", err)
				} else if finalStatus == "failed" && isReasonixSessionLeaseConflict(err) {
					finalError = fmt.Sprintf("reasonix session is already in use; close the other Reasonix window or process first: %v", err)
				}
				resCh <- Result{
					Status:         finalStatus,
					Error:          finalError,
					DurationMs:     time.Since(startTime).Milliseconds(),
					ResumeRejected: resumeRejected,
				}
				return
			}
			sessionResult = result
			var changed bool
			sessionID, changed = resolveResumedSessionID(opts.ResumeSessionID, result)
			if changed {
				b.cfg.Logger.Warn("agent returned a different session id on resume — original was likely lost; continuing with the new id",
					"backend", "reasonix",
					"requested", opts.ResumeSessionID,
					"actual", sessionID,
				)
			}
		} else {
			result, err := c.request(runCtx, "session/new", map[string]any{
				"cwd":        cwd,
				"mcpServers": mcpServers,
			})
			if err != nil {
				finalStatus, finalError = reasonixRequestFailure(runCtx, timeout, reasonixSessionNewError(err))
				resCh <- Result{Status: finalStatus, Error: finalError, DurationMs: time.Since(startTime).Milliseconds()}
				return
			}
			sessionResult = result
			sessionID = extractACPSessionID(result)
			if sessionID == "" {
				finalStatus = "failed"
				finalError = "reasonix session/new returned no session ID"
				resCh <- Result{Status: finalStatus, Error: finalError, DurationMs: time.Since(startTime).Milliseconds()}
				return
			}
		}

		c.sessionID = sessionID
		b.cfg.Logger.Info("reasonix session created", "session_id", sessionID)

		// 3. 若调用方通过 agent.model（UI 下拉框）选了模型，就在发任何 prompt 前让 reasonix
		// 把会话切到该模型。Reasonix 的 ACP 服务端暴露 `session/set_model`，并经由
		// session/new 返回的 `models.availableModels` 块宣传可用模型——这里把选中的 modelId
		// 原样透传。这一步出错必须让任务失败：静默回退到 reasonix 默认模型，会让用户以为
		// 选择已生效，而任务其实跑在了别的模型上。
		if opts.Model != "" {
			if _, err := c.request(runCtx, "session/set_model", map[string]any{
				"sessionId": sessionID,
				"modelId":   opts.Model,
			}); err != nil {
				b.cfg.Logger.Warn("reasonix set_session_model failed", "error", err, "requested_model", opts.Model)
				finalStatus, finalError = reasonixRequestFailure(runCtx, timeout, fmt.Sprintf("reasonix could not switch to model %q: %v", opts.Model, err))
				if finalStatus == "failed" && opts.ResumeSessionID != "" && isACPSessionNotFound(err) {
					// 续接的会话叠加模型覆盖时，死会话在这里就会暴露（而不是拖到
					// session/prompt）。与下面 prompt 路径同一处理：清掉 id，让 daemon 的
					// 「续接失败」回退逻辑用新会话重试。
					b.cfg.Logger.Warn("resumed session not found at set_model time; clearing session id so the daemon retries fresh",
						"backend", "reasonix",
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
			b.cfg.Logger.Info("reasonix session model set", "model", opts.Model)
		}

		// 3b. 通过本会话宣传出的 effort 选项应用一次持久化的思考级别覆盖。与上面的
		// set_model 不同，这里绝不能让任务失败：一条没能应用上的 effort 只是按运行时默认跑
		// prompt，属于「降级」而非「跑错」。辅助函数会记录实际生效了什么。
		//
		// 一旦上面的 set_model 跑过，sessionResult 就不再描述当前活会话了——reasonix 会按
		// 当前模型派生 effort 目录，而 set_model 不返回任何东西。如实说明这一点，辅助函数
		// 就会以运行时的回答为准，而不是信一份过期的宣传列表。
		applyACPEffortOption(runCtx, c.request, "reasonix", b.cfg.Logger,
			sessionID, sessionResult, opts.ThinkingLevel, opts.Model == "")

		// 4. 发 prompt 并等 PromptResponse。Reasonix 会自行从 cwd 加载 AGENTS.md，因此 daemon
		// 刻意不把运行时简介重复拼进这条用户消息。
		streamingCurrentTurn.Store(true)
		_, err = c.request(runCtx, "session/prompt", map[string]any{
			"sessionId": sessionID,
			"prompt": []map[string]any{
				{"type": "text", "text": prompt},
			},
		})
		if err != nil {
			finalStatus, finalError = reasonixRequestFailure(runCtx, timeout, fmt.Sprintf("reasonix session/prompt failed: %v", err))
			if finalStatus == "failed" {
				if opts.ResumeSessionID != "" && isACPSessionNotFound(err) {
					// 参见 hermes 后端：即使会话已不存在，运行时也会把请求的 id 原样回显
					// （来自 session/resume），所以这个过期 id 只会在 prompt 这一刻才失败。
					// 清空 SessionID 让 daemon 的续接失败回退用新会话重试并保存替换后的 id。
					b.cfg.Logger.Warn("resumed session not found at prompt time; clearing session id so the daemon retries fresh",
						"backend", "reasonix",
						"session_id", sessionID,
					)
					sessionID = ""
					resumeRejected = true
				}
			}
		} else {
			select {
			case pr := <-promptDone:
				switch pr.stopReason {
				case "end_turn":
				case "cancelled":
					finalStatus = "aborted"
					finalError = "reasonix cancelled the prompt"
				case "error":
					finalStatus = "failed"
					finalError = "reasonix ended the prompt with stopReason=error"
				default:
					finalStatus = "failed"
					finalError = fmt.Sprintf("reasonix returned unsupported stopReason %q", pr.stopReason)
				}
				c.mergeUsage(pr.usage)
			default:
				finalStatus = "failed"
				finalError = "reasonix returned no prompt completion result"
			}
			waitForACPNotificationQuiescence(runCtx, activity, readerDone, acpNotificationQuietTime, reasonixReaderDrainGrace)
		}

		duration := time.Since(startTime)
		b.cfg.Logger.Info("reasonix finished", "pid", cmd.Process.Pid, "status", finalStatus, "duration", duration.Round(time.Millisecond).String())

		stdin.Close()
		cancel()

		<-readerDone
		// 先确认 stderr copier 已排干，再去查 provider 错误嗅探器；失败模式见 hermes.go。
		<-stderrDone
		streamingCurrentTurn.Store(false)

		finalOutput, providerErrorOutput := deliverable.result()

		// 当 stderr 或 agent 文本流展现出「上游 LLM 终态失败」（HTTP 4xx / 限流 / token 过期）
		// 时，把 completed 提升为 failed。完整信号集见辅助函数文档；关键安全性质是：瞬时性
		// 的单次尝试告警后面跟了成功重试的，必须保持 "completed"。这里读的是完整文本流而不
		// 是 deliverable，所以「在工具调用前就放弃」的一轮仍然可见。
		finalStatus, finalError = promoteACPResultOnProviderError(finalStatus, finalError, providerErrorOutput, providerErr)
		if question, ok := blockedQuestion.Load().(string); ok && question != "" {
			switch finalStatus {
			case "completed", "failed":
				finalStatus = "failed"
				finalError = question
			}
		}

		u := c.accumulatedUsage()
		statusSnapshot, statusModel := statusUsage.snapshot()
		if !reasonixUsagePresent(u) {
			u = statusSnapshot
		}

		var usageMap map[string]TokenUsage
		if reasonixUsagePresent(u) {
			model := opts.Model
			if model == "" {
				model = statusModel
			}
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

// reasonixToolNameFromTitle 把 Reasonix 的 ACP 服务端发出的工具名归一化成 Multica UI 期望的
// snake_case 标识符。
//
// Reasonix 遵循 ACP 规范，`title` 是简短的人读标签，例如 "Read file: /path/to/foo.go" 或
// "Run command: ls"。上游的 hermesToolNameFromTitle 处理的是 hermes 的小写约定（"read:"、
// "patch (replace)"），覆盖不到 reasonix 的大写格式——所以这里拿到的是 hermes 已经映射过
// 一轮的名字，再修掉漏网之鱼。空输入返回 ""。
func reasonixToolNameFromTitle(title string) string {
	t := strings.TrimSpace(title)
	if t == "" {
		return ""
	}

	// 剥掉第一个冒号之后的所有内容——ACP title 往往形如 "Tool Name: argument detail"，我们
	// 只要工具名。
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
	case "search", "grep", "find":
		return "search_files"
	case "glob":
		return "glob"
	case "web search":
		return "web_search"
	case "fetch", "web fetch":
		return "web_fetch"
	case "todo", "todo write":
		return "todo_write"
	}

	// 兜底：把 title 转成 snake_case，让 UI 拿到稳定标识符。
	return strings.ReplaceAll(lower, " ", "_")
}

// reasonixProtectedPermissionTools 是 Reasonix 标记为「仅人工可决策」、因此无人值守时必须
// 拒绝的工具名集合。命中它们（或元数据标记 fresh）的权限请求不走通用的「allow_once 安全」
// 规则。
var reasonixProtectedPermissionTools = map[string]bool{
	"exit_plan_mode":              true,
	"remember":                    true,
	"forget":                      true,
	"sandbox_escape":              true,
	"config_write":                true,
	"plan_mode_read_only_command": true,
}

// reasonixPermissionRequest 是一次 session/request_permission 请求的解码形态：ToolCall 描述
// 要授权的工具调用（含可选的受信元数据 _meta），Options 给出可选的响应选项。
type reasonixPermissionRequest struct {
	ToolCall struct {
		ToolCallID string                     `json:"toolCallId"`
		Title      string                     `json:"title"`
		Meta       map[string]json.RawMessage `json:"_meta"`
	} `json:"toolCall"`
	Options []acpPermissionOption `json:"options"`
}

// reasonixPermissionMetadata 是 Reasonix 写在请求 _meta["reasonix.io"] 里的受信元数据：
// Tool 给出规范工具名，Fresh 表示该决策被明确标记为「仅当下人工可处理」。
type reasonixPermissionMetadata struct {
	Tool  string `json:"tool"`
	Fresh bool   `json:"fresh"`
}

// selectReasonixPermissionOption 防止两类 Reasonix 特有的请求漏过共享的「allow_once 安全」
// 规则：一类是用户提问，一类是 Reasonix 明确标记为 fresh-human-only 的决策。
func selectReasonixPermissionOption(params json.RawMessage) (optionID string, grant bool, ok bool, blockedQuestion string) {
	var p reasonixPermissionRequest
	if err := json.Unmarshal(params, &p); err != nil {
		return "", false, false, ""
	}

	rejectOnce := func() (string, bool) {
		for _, opt := range p.Options {
			if opt.OptionID != "" && strings.EqualFold(strings.TrimSpace(opt.Kind), acpKindRejectOnce) {
				return opt.OptionID, true
			}
		}
		return "", false
	}

	if reasonixPermissionIsQuestion(p) {
		reason := "Reasonix requested interactive user input, which is unavailable in an unattended Multica task"
		if title := strings.TrimSpace(p.ToolCall.Title); title != "" {
			reason += ": " + clipReasonixPermissionTitle(title)
		}
		if id, found := rejectOnce(); found {
			return id, false, true, reason
		}
		// 拿不到拒绝选项属于协议错误，这里 fail-closed：现版 Reasonix 会把失败的 ask 响应
		// 当作「未作答/已取消」而不是继续执行。
		return "", false, false, reason
	}

	meta, _ := reasonixPermissionMeta(p)
	if meta.Fresh || reasonixProtectedPermissionTools[meta.Tool] {
		if id, found := rejectOnce(); found {
			return id, false, true, ""
		}
		return "", false, false, ""
	}

	id, allowed, selectable := selectACPPermissionOption(params)
	return id, allowed, selectable, ""
}

// reasonixPermissionIsQuestion 判断这次权限请求是不是「向用户提问」而非「请求批准」：工具
// 调用 ID 以 "ask-" 开头，或某个选项以 ":cancel" 结尾（取消/拒绝姿态是提问的特征）。
func reasonixPermissionIsQuestion(p reasonixPermissionRequest) bool {
	if strings.HasPrefix(p.ToolCall.ToolCallID, "ask-") {
		return true
	}
	for _, opt := range p.Options {
		if strings.HasSuffix(opt.OptionID, ":cancel") {
			return true
		}
	}
	return false
}

// reasonixPermissionMeta 从请求的 _meta["reasonix.io"] 解析受信元数据。键缺失、解析失败或
// 工具名为空都视为拿不到元数据（ok=false）。
func reasonixPermissionMeta(p reasonixPermissionRequest) (reasonixPermissionMetadata, bool) {
	var meta reasonixPermissionMetadata
	raw, ok := p.ToolCall.Meta["reasonix.io"]
	if !ok || json.Unmarshal(raw, &meta) != nil {
		return reasonixPermissionMetadata{}, false
	}
	meta.Tool = strings.TrimSpace(meta.Tool)
	return meta, meta.Tool != ""
}

// reasonixPermissionMetadataMissing 报告一次非提问类的权限请求是否缺少受信元数据（此时
// fresh/受保护工具的判断会退化，值得告警）。
func reasonixPermissionMetadataMissing(params json.RawMessage) bool {
	var p reasonixPermissionRequest
	if json.Unmarshal(params, &p) != nil || reasonixPermissionIsQuestion(p) {
		return false
	}
	_, ok := reasonixPermissionMeta(p)
	return !ok
}

// warnReasonixCapabilityGaps 在 Reasonix 未宣传出期望的 ACP 状态能力 schema 时打一条告警，
// 提示用量与成本上报可能不全。
func warnReasonixCapabilityGaps(logger *slog.Logger, result json.RawMessage) {
	statusVersion, updateVersion := reasonixStatusCapabilitySchemas(result)
	if statusVersion == 1 && updateVersion == 1 {
		return
	}
	logger.Warn(
		"reasonix ACP status capabilities are unavailable or incompatible; usage and cost reporting may be incomplete",
		"session_status_schema_version", statusVersion,
		"status_update_schema_version", updateVersion,
	)
}

// reasonixStatusCapabilitySchemas 从 initialize 结果里读出 Reasonix 对状态类通知的 schema
// 版本（session/status 与 session/status_update）。读取不到按 0 处理。
func reasonixStatusCapabilitySchemas(result json.RawMessage) (statusVersion, updateVersion int) {
	var initialized struct {
		AgentCapabilities struct {
			Meta map[string]json.RawMessage `json:"_meta"`
		} `json:"agentCapabilities"`
	}
	if json.Unmarshal(result, &initialized) != nil {
		return 0, 0
	}
	readVersion := func(key string) int {
		var capability struct {
			SchemaVersion int `json:"schemaVersion"`
		}
		if json.Unmarshal(initialized.AgentCapabilities.Meta[key], &capability) != nil {
			return 0
		}
		return capability.SchemaVersion
	}
	return readVersion("_reasonix.io/session/status"), readVersion("_reasonix.io/session/status_update")
}

// clipReasonixPermissionTitle 把权限请求标题截到 240 个字符（按 rune 计，避免截断多字节
// 字符），超长时加省略号，用于塞进告警/错误文本。
func clipReasonixPermissionTitle(title string) string {
	runes := []rune(title)
	if len(runes) <= 240 {
		return title
	}
	return string(runes[:240]) + "…"
}

// isReasonixSessionLeaseConflict 判断错误是否为 Reasonix 的「会话租约冲突」：同一会话正被
// 另一个进程占用。识别特征是该 JSON-RPC 错误（code -32600）的文本同时含 " in use" 与
// "close the other reasonix"。
func isReasonixSessionLeaseConflict(err error) bool {
	var rpcErr *acpRPCError
	if !errors.As(err, &rpcErr) || rpcErr.Code != -32600 {
		return false
	}
	text := strings.ToLower(rpcErr.Message + " " + rpcErr.Data)
	return strings.Contains(text, " in use") && strings.Contains(text, "close the other reasonix")
}

// reasonixSessionNewError 把 session/new 失败翻译成人可操作的错误：属于「未配置/没有默认
// 模型」时，提示运行方先 `reasonix setup` 再重试。
func reasonixSessionNewError(err error) string {
	text := strings.ToLower(err.Error())
	if strings.Contains(text, "not configured") || strings.Contains(text, "no default_model configured") {
		return fmt.Sprintf("reasonix session/new failed: %v; run `reasonix setup` in the runtime owner's environment and retry", err)
	}
	return fmt.Sprintf("reasonix session/new failed: %v", err)
}

// reasonixRequestFailure 把一次 ACP 请求的失败统一归因：context 超时 → timeout、context 被
// 取消 → aborted，否则按普通失败并返回 fallback 文本。
func reasonixRequestFailure(ctx context.Context, timeout time.Duration, fallback string) (string, string) {
	switch ctx.Err() {
	case context.DeadlineExceeded:
		if timeout > 0 {
			return "timeout", fmt.Sprintf("reasonix timed out after %s", timeout)
		}
		return "timeout", "reasonix execution timed out"
	case context.Canceled:
		return "aborted", "execution cancelled"
	default:
		return "failed", fallback
	}
}

// reasonixStatusUsageTracker 从 Reasonix 的 `_reasonix.io/session/status_update` 通知里累积
// token 用量。由于状态更新按 sequence 递增、可能乱序到达，这里只接受比已见 sequence 更新的
// 快照。
type reasonixStatusUsageTracker struct {
	mu       sync.Mutex
	sequence uint64
	usage    TokenUsage
	model    string
}

// observe 记录一条状态更新通知中的用量快照（仅处理 schema 版本 1 的
// `_reasonix.io/session/status_update`）。
func (t *reasonixStatusUsageTracker) observe(method string, params json.RawMessage) {
	if method != "_reasonix.io/session/status_update" {
		return
	}
	var update struct {
		SchemaVersion int    `json:"schemaVersion"`
		Sequence      uint64 `json:"sequence"`
		Status        struct {
			Model string `json:"model"`
			Usage struct {
				Turn struct {
					PromptTokens     int64    `json:"promptTokens"`
					CompletionTokens int64    `json:"completionTokens"`
					CacheHitTokens   int64    `json:"cacheHitTokens"`
					CacheMissTokens  int64    `json:"cacheMissTokens"`
					EstimatedCost    *float64 `json:"estimatedCost"`
					Currency         string   `json:"currency"`
				} `json:"turn"`
			} `json:"usage"`
		} `json:"status"`
	}
	if err := json.Unmarshal(params, &update); err != nil || update.SchemaVersion != 1 {
		return
	}

	turn := update.Status.Usage.Turn
	// promptTokens 是 cacheHit 与 cacheMiss 之和（不同 schema 版本里字段口径不一）：协议直接
	// 给 cacheMiss 时用它当输入数；否则用「总输入减命中」推算，推算出负数（口径异常）时
	// 退回总输入，宁可多算也不要负值。
	input := turn.CacheMissTokens
	if input == 0 {
		input = turn.PromptTokens - turn.CacheHitTokens
		if input < 0 {
			input = turn.PromptTokens
		}
	}
	u := TokenUsage{
		InputTokens:     input,
		OutputTokens:    turn.CompletionTokens,
		CacheReadTokens: turn.CacheHitTokens,
	}
	// 估算成本仅在「币种是美元且数值有限合理」时折算成 USD tick 记入，否则留 0。
	if turn.EstimatedCost != nil && isReasonixUSDCurrency(turn.Currency) &&
		*turn.EstimatedCost >= 0 && !math.IsNaN(*turn.EstimatedCost) && !math.IsInf(*turn.EstimatedCost, 0) &&
		*turn.EstimatedCost <= float64(1<<63-1)/float64(CostUSDTicksPerUSD) {
		u.CostUSDTicks = int64(math.Round(*turn.EstimatedCost * float64(CostUSDTicksPerUSD)))
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if update.Sequence < t.sequence {
		return
	}
	t.sequence = update.Sequence
	t.usage = u
	t.model = strings.TrimSpace(update.Status.Model)
}

// snapshot 返回当前累积的用量与模型名（加锁拷贝，供主 goroutine 安全读取）。
func (t *reasonixStatusUsageTracker) snapshot() (TokenUsage, string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.usage, t.model
}

// isReasonixUSDCurrency 判断币种串是否表示美元（USD / $ / US$，忽略大小写与首尾空白）。
func isReasonixUSDCurrency(currency string) bool {
	switch strings.ToUpper(strings.TrimSpace(currency)) {
	case "USD", "$", "US$":
		return true
	default:
		return false
	}
}

// reasonixUsagePresent 判断这份用量是否「有实质内容」：ACP 累计用量非空，或已折算 USD 成本
// 大于 0。
func reasonixUsagePresent(u TokenUsage) bool {
	return acpTokenUsagePresent(u) || u.CostUSDTicks > 0
}
