package agent

// 文件导读：Kimi Code CLI 的 ACP 驱动封装。
//
// 角色定位：把 Kimi Code CLI（https://github.com/MoonshotAI/kimi-cli）适配成 daemon 的
// Backend。Kimi 通过 `kimi acp` 子命令原生支持 ACP（Agent Client Protocol，stdin/stdout 上的
// JSON-RPC 2.0），本文件拉起该子进程并驱动完整会话生命周期（initialize → session/new|resume
// → 可选 session/set_model / set_config_option → session/prompt），把正文、工具调用、权限
// 批准等翻译成 Message/Result 上抛。
//
// 职责边界：与 reasonix 后端同源——两者都复用 hermesClient 这套 ACP 传输层（同一套协议），
// 本文件只保留 Kimi 特有部分：可执行文件与启动参数、终端（terminal）能力开关、工具名归一化
// （kimiToolNameFromTitle）、思考级别经 session/set_config_option 生效，以及「从 kimi 的
// session wire 日志回读 token 用量」的兜底扫描。
//
// 核心约定：① Kimi 在 api.kimi.com 返回 4xx/5xx 时 session/prompt 仍会报 stopReason=end_turn，
// 因此必须嗅探 stderr 里 provider 级错误来把这类假成功提升为失败；② kimi-code 0.33.0 不通过
// ACP 上报 token 计数（只报上下文占用），计费数据要从它每个会话的 wire.jsonl 日志里按模型
// 汇总（见 scanKimiSessionUsage，MUL-5773 / #6448）；③ 历史回放不重播——同 reasonix 后端，
// 用 streamingCurrentTurn 闸门丢弃 session/resume 时补发的前几轮记录。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

// kimiBlockedArgs 是 daemon 固定写死、不允许用户经 custom_args 覆盖的开关。其中 `acp` 是驱动
// Kimi Code CLI 的 ACP JSON-RPC 传输所需的协议子命令，覆盖它会破坏 daemon ↔ Kimi 的通信契约。
var kimiBlockedArgs = map[string]blockedArgMode{
	"acp": blockedStandalone,
}

// kimiReaderDrainGrace 限制轮次在 session/prompt 响应之后还等多久「尾部的 ACP 通知」。用变量
// 而非常量，是为了让测试能缩短它。与 qoderReaderDrainGrace / traecliReaderDrainGrace 的用意
// 一致。
var kimiReaderDrainGrace = 2 * time.Second

// kimiBackend 通过拉起 `kimi acp`、经 stdin/stdout 上的 ACP（Agent Client Protocol）JSON-RPC
// 2.0 通信来实现 Backend。
//
// Kimi Code CLI（https://github.com/MoonshotAI/kimi-cli）通过 `kimi acp` 子命令原生支持 ACP。
// 这里复用现成的 hermesClient ACP 传输层——两个运行时说同一套协议，只有二进制名、环境与
// 工具名提取方式不同。
type kimiBackend struct {
	cfg Config
}

// Execute 驱动一次完整的 Kimi ACP 会话并返回其产出。
//
// 流程：定位可执行文件、把 mcp_config 翻译成 ACP 数组形态 → 拉起 `kimi acp` 子进程 → 后台
// 读 stdout（每行一个 JSON-RPC 消息），主 goroutine 按 initialize → session/new|resume →
// （可选）session/set_model / thinking 配置覆盖 → session/prompt 的顺序驱动会话 → prompt
// 完成后等通知静默，把 stderr 侧 provider 失败与用量兜底扫描的结果合并 → 生成最终 Result。
func (b *kimiBackend) Execute(ctx context.Context, prompt string, opts ExecOptions) (*Session, error) {
	execPath := b.cfg.ExecutablePath
	if execPath == "" {
		execPath = "kimi"
	}
	if _, err := exec.LookPath(execPath); err != nil {
		return nil, fmt.Errorf("kimi executable not found at %q: %w", execPath, err)
	}

	// 把 agent 的 mcp_config（Claude 风格的对象套对象）翻译成 ACP `session/new` 期望的数组
	// 形态。畸形 JSON 要直接失败（fail closed），让启动把真实错误暴露出来，而不是静默丢掉
	// 全部 MCP 服务器。
	mcpServers, err := buildACPMcpServers(opts.McpConfig, b.cfg.Logger)
	if err != nil {
		return nil, fmt.Errorf("kimi: invalid mcp_config: %w", err)
	}

	timeout := opts.Timeout
	runCtx, cancel := runContext(ctx, timeout)

	// `kimi acp` 会忽略 --yolo / --auto-approve（它们挂在根命令 `kimi` 上，不在 `acp`
	// 子命令上）。daemon 的自动批准改由 hermesClient.handleAgentRequest 完成：对每次
	// session/request_permission 请求，从 agent 提供的选项里挑一个安全的授予项
	// （见 selectACPApprovalOptionID）。
	kimiArgs := append([]string{"acp"}, filterCustomArgs(opts.CustomArgs, kimiBlockedArgs, b.cfg.Logger)...)
	cmd := b.cfg.commandAt(execPath).exec(runCtx, kimiArgs...)
	hideAgentWindow(cmd)
	b.cfg.logAgentCommand(cmd, newAgentCommandLogArgs(kimiArgs, trustAgentCommandPositional(0, "acp")))
	if opts.Cwd != "" {
		cmd.Dir = opts.Cwd
	}
	cmd.Env = buildEnv(b.cfg.Env)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("kimi stdout pipe: %w", err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("kimi stdin pipe: %w", err)
	}
	// stderr 既要进 daemon 日志，也要被嗅探器提取 provider 级错误，好把它们呈现到任务结果
	// 里。Kimi 的 session/prompt 在底层调用 api.kimi.com 返回 4xx/5xx 时仍会报
	// stopReason=end_turn——不嗅探的话 daemon 只会报一句误导性的 "empty output"，而真正可
	// 操作的错误（token 过期、限流、上游 5xx 等）会埋进 daemon 日志。
	//
	// 用 StderrPipe 加显式 copier 是为了拿到一个 join 点（stderrDone），它保证在「把失败
	// 提升到结果」的决策之前已经排干；为什么 io.MultiWriter 的形式在负载下会和
	// stopReason=end_turn 竞态，见 hermes.go 里的对应注释。
	providerErr := newACPProviderErrorSniffer("kimi")
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("kimi stderr pipe: %w", err)
	}

	if err := startOwnedProcessTree(cmd, b.cfg.Logger); err != nil {
		cancel()
		return nil, fmt.Errorf("start kimi: %w", err)
	}

	stderrSink := io.MultiWriter(newLogWriter(b.cfg.Logger, "[kimi:stderr] "), providerErr)
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		_, _ = io.Copy(stderrSink, stderr)
	}()

	b.cfg.Logger.Info("kimi acp started", "pid", cmd.Process.Pid, "cwd", opts.Cwd)

	msgCh := make(chan Message, 256)
	resCh := make(chan Result, 1)

	// Kimi 把中途叙述与最终答案都作为同一种 agent_message_chunk 类型流出；tracker 只保留
	// 「最后一次工具调用之后」的那段作为 Result.Output，同时保留全文供错误检测用。
	var deliverable acpDeliverableTracker
	// streamingCurrentTurn 闸住所有会话更新：历史回放（Kimi 在 session/resume 时会补发前几轮
	// 的完整记录，也可能在我们 session/prompt 的响应流出前先冲刷积压分片）一律丢弃，避免把
	// 上一轮答案重复并进 output。只有在 session/prompt 发出后才置为 true。
	var streamingCurrentTurn atomic.Bool

	promptDone := make(chan hermesPromptResult, 1)
	activity := make(chan struct{}, 1)

	// 复用 hermesClient 这个 ACP 传输层——Kimi 说的是同一套协议。
	c := &hermesClient{
		cfg:             b.cfg,
		stdin:           stdin,
		pending:         make(map[int]*pendingRPC),
		pendingTools:    make(map[string]*pendingToolCall),
		terminalEnabled: true,
		terminalCtx:     runCtx,
		terminalCwd:     opts.Cwd,
		terminalEnv:     buildEnv(b.cfg.Env),
		terminals:       make(map[string]*acpTerminal),
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
			// hermesClient.handleToolCallStart 已经把原始 ACP title 经 hermesToolNameFromTitle
			// 映射过一遍——它覆盖 hermes 风格的小写 title（"read:"、"patch (replace)" 等），
			// 但不覆盖 Kimi 风格的大写形式（"Read file: …"、"Run command: …"）。这里再归一化
			// 一次，让 UI 在两个后端看到一致的 snake_case 标识符；名字已是正常形态时是 no-op
			// （例如已经映射成 "read_file"）。
			if msg.Type == MessageToolUse {
				msg.Tool = kimiToolNameFromTitle(msg.Tool)
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
		scanner := newAgentStreamScanner(stdout)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			c.handleLine(line)
		}
		c.closeAllPending(fmt.Errorf("kimi process exited"))
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

		// 1. 握手初始化。声明 terminal 能力，让运行时知道可以下发终端工具。
		initResult, err := c.request(runCtx, "initialize", map[string]any{
			"protocolVersion": 1,
			"clientInfo": map[string]any{
				"name":    "multica-agent-sdk",
				"version": "0.2.0",
			},
			"clientCapabilities": map[string]any{
				"terminal": true,
			},
		})
		if err != nil {
			finalStatus = "failed"
			finalError = fmt.Sprintf("kimi initialize failed: %v", err)
			resCh <- Result{Status: finalStatus, Error: finalError, DurationMs: time.Since(startTime).Milliseconds()}
			return
		}

		// 丢弃运行时未宣传其远程传输方式的 MCP 条目。原因见 hermes.go 的对应注释：把一个
		// http/sse 条目塞给只支持 stdio 的运行时，会拖垮整个 session/new。
		mcpServers = filterACPMcpServersByCapability(mcpServers, extractACPMcpCapabilities(initResult), "kimi", b.cfg)

		// 2. 新建或续接会话。
		cwd := opts.Cwd
		if cwd == "" {
			cwd = "."
		}

		if opts.ResumeSessionID != "" {
			// 按 ACP Session Setup 约定，session/resume 接受 mcpServers，运行时会在续接时
			// 一并重连它们。不传的话，同一 agent 上新建任务能用的 MCP 工具，续接任务反而会
			// 丢掉。
			result, err := c.request(runCtx, "session/resume", map[string]any{
				"cwd":        cwd,
				"sessionId":  opts.ResumeSessionID,
				"mcpServers": mcpServers,
			})
			if err != nil {
				finalStatus = "failed"
				finalError = fmt.Sprintf("kimi session/resume failed: %v", err)
				resCh <- Result{Status: finalStatus, Error: finalError, DurationMs: time.Since(startTime).Milliseconds()}
				return
			}
			var changed bool
			sessionID, changed = resolveResumedSessionID(opts.ResumeSessionID, result)
			if changed {
				b.cfg.Logger.Warn("agent returned a different session id on resume — original was likely lost; continuing with the new id",
					"backend", "kimi",
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
				finalStatus = "failed"
				finalError = fmt.Sprintf("kimi session/new failed: %v", err)
				resCh <- Result{Status: finalStatus, Error: finalError, DurationMs: time.Since(startTime).Milliseconds()}
				return
			}
			sessionID = extractACPSessionID(result)
			if sessionID == "" {
				finalStatus = "failed"
				finalError = "kimi session/new returned no session ID"
				resCh <- Result{Status: finalStatus, Error: finalError, DurationMs: time.Since(startTime).Milliseconds()}
				return
			}
		}

		c.sessionID = sessionID
		b.cfg.Logger.Info("kimi session created", "session_id", sessionID)

		// 3. 若调用方通过 agent.model（UI 下拉框）选了模型，就在发任何 prompt 前让 kimi 把
		// 会话切到该模型。Kimi 的 ACP 服务端暴露 `session/set_model`，并经由 session/new
		// 返回的 `models.availableModels` 块宣传可用模型——这里把选中的 modelId 原样透传。
		// 这一步出错必须让任务失败：静默回退到 kimi 的默认模型，会让用户以为选择已生效，而
		// 任务其实跑在了别的模型上。
		if opts.Model != "" {
			if _, err := c.request(runCtx, "session/set_model", map[string]any{
				"sessionId": sessionID,
				"modelId":   opts.Model,
			}); err != nil {
				b.cfg.Logger.Warn("kimi set_session_model failed", "error", err, "requested_model", opts.Model)
				finalStatus = "failed"
				finalError = fmt.Sprintf("kimi could not switch to model %q: %v", opts.Model, err)
				if opts.ResumeSessionID != "" && isACPSessionNotFound(err) {
					// 续接的会话叠加模型覆盖时，死会话在这里就会暴露（而不是拖到
					// session/prompt）。与下面 prompt 路径同一处理：清掉 id，让 daemon 的
					// 「续接失败」回退逻辑用新会话重试。
					b.cfg.Logger.Warn("resumed session not found at set_model time; clearing session id so the daemon retries fresh",
						"backend", "kimi",
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
			b.cfg.Logger.Info("kimi session model set", "model", opts.Model)
		}

		// 3b. 发 prompt 前，通过 Kimi 原生的 ACP 配置项应用一次持久化的思考级别覆盖。与其它
		// provider 一样，配置失败不阻断任务：prompt 无论如何都会发出，下面这几条告警是 UI
		// 上显示的级别可能与实际生效级别不一致时仅有的记录。实际生效成哪个级别取决于失败
		// 方式：若请求本身失败，会话维持原有级别（新建会话是 CLI 自带设置，续接会话是上一轮
		// 的级别）；若 Kimi 应答但确认了别的值，会话就停在该值；若它应答却完全没带回
		// `thinking` 的 currentValue，则无法判断实际级别。
		if opts.ThinkingLevel != "" {
			configResult, err := c.request(runCtx, "session/set_config_option", map[string]any{
				"sessionId": sessionID,
				"configId":  "thinking",
				"value":     opts.ThinkingLevel,
			})
			if err != nil {
				b.cfg.Logger.Warn("kimi rejected the thinking level request; sending the prompt anyway",
					"requested_level", opts.ThinkingLevel,
					"effective_level", "unchanged",
					"error", err,
				)
			} else {
				effectiveLevel, confirmed := acpConfigOptionCurrentValue(configResult, "thinking")
				if !confirmed || effectiveLevel != opts.ThinkingLevel {
					if !confirmed {
						effectiveLevel = "unknown"
					}
					b.cfg.Logger.Warn("kimi did not confirm the requested thinking level; sending the prompt anyway",
						"requested_level", opts.ThinkingLevel,
						"effective_level", effectiveLevel,
					)
				} else {
					b.cfg.Logger.Info("kimi session thinking level confirmed", "level", effectiveLevel)
				}
			}
		}

		// 4. 组装 prompt 内容：若有系统提示词，先拼到任务 prompt 前面。
		userText := prompt
		if opts.SystemPrompt != "" {
			userText = opts.SystemPrompt + "\n\n---\n\n" + prompt
		}

		// 5. 发 prompt 并等 PromptResponse。
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
				finalError = fmt.Sprintf("kimi timed out after %s", timeout)
			} else if runCtx.Err() == context.Canceled {
				finalStatus = "aborted"
				finalError = "execution cancelled"
			} else {
				finalStatus = "failed"
				finalError = fmt.Sprintf("kimi session/prompt failed: %v", err)
				if opts.ResumeSessionID != "" && isACPSessionNotFound(err) {
					// 参见 hermes 后端：即使会话已不存在，运行时也会把请求的 id 原样回显
					// （来自 session/resume），所以这个过期 id 只会在 prompt 这一刻才失败。
					// 清空 SessionID 让 daemon 的续接失败回退用新会话重试并保存替换后的 id。
					b.cfg.Logger.Warn("resumed session not found at prompt time; clearing session id so the daemon retries fresh",
						"backend", "kimi",
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
					finalError = "kimi cancelled the prompt"
				}
				// kimi-code 0.33.0 在这条路径上完全不发 usage，所以这行目前不起作用；它留着是
				// 为了让将来某个会填充 ACP 结果的 kimi 能正确计费，而不是悄悄丢掉缓存或成本
				// 字段。当下真正生效的是下面的 scanKimiSessionUsage。
				c.mergeUsage(pr.usage)
			default:
			}
			waitForACPNotificationQuiescence(runCtx, activity, readerDone, acpNotificationQuietTime, kimiReaderDrainGrace)
		}

		duration := time.Since(startTime)
		b.cfg.Logger.Info("kimi finished", "pid", cmd.Process.Pid, "status", finalStatus, "duration", duration.Round(time.Millisecond).String())

		stdin.Close()
		cancel()

		<-readerDone
		// 先确认 stderr copier 已排干，再去查 provider 错误嗅探器；失败模式见 hermes.go。
		<-stderrDone
		streamingCurrentTurn.Store(false)

		finalOutput, providerErrorOutput := deliverable.result()

		// 当 stderr 或 agent 文本流展现出「上游 LLM 终态失败」（HTTP 4xx / 限流 / token 过期）
		// 时，把 completed 提升为 failed。完整信号集见辅助函数文档；关键安全性质是：瞬时性的
		// 单次尝试告警后面跟了成功重试的，必须保持 "completed"。这里读的是完整文本流而不是
		// deliverable，所以「在工具调用前就放弃」的一轮仍然可见。
		finalStatus, finalError = promoteACPResultOnProviderError(finalStatus, finalError, providerErrorOutput, providerErr)

		u := c.accumulatedUsage()

		// 兜底：kimi-code 0.33.0 不通过 ACP 导出任何 token 计数器。它的 session/prompt 结果
		// 只带 stopReason，`usage_update` 通知只带 {used,size}——那是上下文窗口占用，不是计费
		// 数据（已对 Multica 自己 onboarding 装的 CLI 验证过）。没有这段扫描，每条 kimi 任务
		// 在用量看板上都会一行都没有（MUL-5773 / #6448）。
		//
		// 这些计数器确实存在于 kimi 的按会话 wire 日志里，所以像 codex.go 回退读 Codex
		// rollouts 那样从这里读。以 sessionID 为界，保证并发的另一个 kimi 会话不会被记到本
		// 任务的账上。
		fallbackModel := opts.Model
		if fallbackModel == "" {
			fallbackModel = "unknown"
		}

		var usageMap map[string]TokenUsage
		if acpTokenUsagePresent(u) {
			usageMap = map[string]TokenUsage{fallbackModel: u}
		} else {
			// 由扫描本身按模型分桶：一个子 agent 或压缩步骤跑在别的模型上的任务，不能把那个
			// 模型的 token 按主模型的费率计价。
			usageMap = scanKimiSessionUsage(kimiUsageScan{
				startTime:     startTime,
				kimiHome:      b.cfg.Env["KIMI_CODE_HOME"],
				sessionID:     sessionID,
				resumed:       opts.ResumeSessionID != "",
				fallbackModel: fallbackModel,
			})
			// 将来某个 Kimi 构建可能先暴露 provider 成本、后暴露 ACP token 桶。与其丢掉任一
			// 数据源，不如把这份成本并进当前的 wire 日志兜底结果里。
			if u.CostUSDTicks > 0 {
				modelUsage := usageMap[fallbackModel]
				modelUsage.CostUSDTicks = u.CostUSDTicks
				if usageMap == nil {
					usageMap = make(map[string]TokenUsage)
				}
				usageMap[fallbackModel] = modelUsage
			}
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

// kimiToolNameFromTitle 把 Kimi 的 ACP 服务端发出的工具名归一化成 Multica UI 期望的
// snake_case 标识符。
//
// Kimi 遵循 ACP 规范，`title` 是简短的人读标签，例如 "Read file: /path/to/foo.go" 或
// "Run command: ls"。上游的 hermesToolNameFromTitle 处理的是 hermes 的小写约定（"read:"、
// "patch (replace)"），覆盖不到 kimi 的大写格式——所以这里拿到的是 hermes 已经映射过一轮的
// 名字，再修掉漏网之鱼。空输入返回 ""。
func kimiToolNameFromTitle(title string) string {
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

// kimiUsageRecordType 是 kimi-code 在每次 LLM 调用后写进 wire 日志的记录类型。它的孪生事件
// `step.end` 循环里会重复同一组数字，所以只能对这一个类型求和——两个都加等于每轮重复计费。
const kimiUsageRecordType = "usage.record"

// kimiWireUsage 是 kimi 会话 wire 日志里的一条 `usage.record` 记录。
//
// 各桶互斥：在一次验证过的两轮会话里，记录分别是 {inputOther:4844, inputCacheRead:17664,
// output:22} 与 {inputOther:272, inputCacheRead:22272, output:22}，每轮三个字段之和恰好等于
// kimi 经 ACP 上报的 `used` 数（22530 与 22566）。因此 `inputOther` 只是未命中缓存的输入，
// 可以直接映射成 TokenUsage.InputTokens，无需像 ACP 的 `inputTokens` 那样用
// excludeACPCachedInput 去减缓存前缀。
type kimiWireUsage struct {
	Type string `json:"type"`
	// Time 是这条记录的 epoch 毫秒写入时间。它防止续接会话被重复计费：kimi 在多次续接里都
	// 追加写到同一个 wire 日志，所以文件里仍留着上一个任务的记录。
	Time  int64  `json:"time"`
	Model string `json:"model"`
	Usage struct {
		InputOther         int64 `json:"inputOther"`
		Output             int64 `json:"output"`
		InputCacheRead     int64 `json:"inputCacheRead"`
		InputCacheCreation int64 `json:"inputCacheCreation"`
	} `json:"usage"`
}

// kimiUsageScan 参数化一次 wire 日志扫描。
type kimiUsageScan struct {
	startTime time.Time
	kimiHome  string
	sessionID string
	// resumed 标记这一轮续接的是既有 kimi 会话。这类会话会往上一个任务已经计过费的 wire
	// 日志里追加，所以记录要按各自的时间戳过滤，而不是只按文件的 mtime。
	resumed bool
	// fallbackModel 作为没有自带模型名的记录的归属模型。
	fallbackModel string
}

// scanKimiSessionUsage 返回 kimi-code 为本会话记录的用量，按产生各记录的模型分桶。
//
// 记录按次 LLM 调用计、是增量而非累计：一次验证过的两步 turn，每次 `llm.request` 记一条，
// 第二条的未缓存输入因前缀进入缓存而远低于第一条。因此任务总量就是对会话内所有记录直接
// 求和。
//
// 子 agent 的 turn 有自己的 `agents/<name>/wire.jsonl`，且可能跑不同模型——这正是结果用 map
// 而非单一总量的原因：只给 `agents/main` 计费会丢掉委派出去的工作，把每个 agent 都折进
// 一个模型名下则会算错价格。
func scanKimiSessionUsage(scan kimiUsageScan) map[string]TokenUsage {
	root := kimiSessionRoot(scan.kimiHome)
	if root == "" {
		return nil
	}

	usage := make(map[string]TokenUsage)
	// 有些文件系统把 mtime 按整秒向下取整。与同精度的截止线比较，避免本 turn 刚写的日志在
	// 其权威的逐记录时间戳被核验前就被误删。
	mtimeCutoff := scan.startTime.Truncate(time.Second)
	for _, path := range kimiSessionWireLogs(root, scan.sessionID) {
		// 一份显然在 turn 开始前就没再动过的 wire 日志，属于续接会话更早的一次运行。在 turn
		// 边界上，逐记录的过滤仍然是最权威的判据。
		if info, err := os.Stat(path); err != nil || info.ModTime().Before(mtimeCutoff) {
			continue
		}
		accumulateKimiWireUsage(usage, path, scan)
	}
	if len(usage) == 0 {
		return nil
	}
	return usage
}

// kimiSessionWireLogs 返回属于 sessionID 的 wire 日志路径列表。
//
// kimi 用 ACP 会话 id 原样命名会话目录
// （`<root>/<workspace>/<sessionID>/agents/<name>/wire.jsonl`），因此这里按精确路径寻址会话，
// 而不是 glob 出所有会话再事后过滤：会话历史只增不减，而本函数跑在每条任务结束时的计费
// 路径上。拼路径也避免了把 agent 的会话 id 放进 glob 模式——其中的 `*` 或 `[`（以及自定义
// KIMI_CODE_HOME 里出现的）会被当成通配符。
func kimiSessionWireLogs(root, sessionID string) []string {
	sessionID = strings.TrimSpace(sessionID)
	// 防路径穿越：id 来自 agent 侧，加入路径前必须校验。
	if sessionID == "" || sessionID == "." || sessionID == ".." ||
		strings.ContainsRune(sessionID, '/') || strings.ContainsRune(sessionID, os.PathSeparator) {
		return nil
	}

	workspaces, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var paths []string
	for _, workspace := range workspaces {
		if !workspace.IsDir() {
			continue
		}
		agentsDir := filepath.Join(root, workspace.Name(), sessionID, "agents")
		agents, err := os.ReadDir(agentsDir)
		if err != nil {
			continue
		}
		for _, agent := range agents {
			if !agent.IsDir() {
				continue
			}
			paths = append(paths, filepath.Join(agentsDir, agent.Name(), "wire.jsonl"))
		}
	}
	return paths
}

// accumulateKimiWireUsage 把一份 wire 日志里的记录累加进 usage，按各记录点名的模型分桶。
// 畸形或截断的行跳过而不是让扫描失败：日志是活的、边跑边追加，尾部可能是半行写入——能报
// 出读到的记录总比什么都不报强。
func accumulateKimiWireUsage(usage map[string]TokenUsage, path string, scan kimiUsageScan) {
	file, err := os.Open(path)
	if err != nil {
		return
	}
	defer file.Close()

	scanner := newAgentStreamScanner(file)
	for scanner.Scan() {
		line := scanner.Bytes()
		if !bytes.Contains(line, []byte(kimiUsageRecordType)) {
			continue
		}
		var record kimiWireUsage
		if err := json.Unmarshal(line, &record); err != nil || record.Type != kimiUsageRecordType {
			continue
		}
		if !kimiWireRecordInTurn(record.Time, scan.startTime, scan.resumed) {
			continue
		}
		model := record.Model
		if model == "" {
			model = scan.fallbackModel
		}
		total := usage[model]
		total.InputTokens += record.Usage.InputOther
		total.OutputTokens += record.Usage.Output
		total.CacheReadTokens += record.Usage.InputCacheRead
		total.CacheWriteTokens += record.Usage.InputCacheCreation
		usage[model] = total
	}
}

// kimiWireRecordInTurn 判断一条用量记录是否属于 startTime 开始的这一轮。
//
// kimi-code 0.33.0 会给每条用量记录打时间戳，所以「无时间戳」的情况只是对未来格式变化的
// 一道防线。遇到时倾向：新建会话记上（反正记录都是我们的）；续接会话丢弃——给一条无时间戳
// 的记录计费，有让用户为更早任务已报过的 token 付两次钱的风险。
func kimiWireRecordInTurn(recordTimeMillis int64, startTime time.Time, resumed bool) bool {
	if recordTimeMillis <= 0 {
		return !resumed
	}
	if startTime.IsZero() {
		return true
	}
	return recordTimeMillis >= startTime.UnixMilli()
}

// kimiSessionRoot 解析 kimi 的会话目录：优先用显式配置的 home，其次环境里的 KIMI_CODE_HOME，
// 最后回退 ~/.kimi-code。与 codexSessionRoot 的解析顺序一致。
//
// 查环境变量这一步与 cfg.Env 并不重复：kimi 子进程经 buildEnv 继承的是 os.Environ()，所以
// 一个导出给 daemon 的 KIMI_CODE_HOME 会搬走 kimi 的会话，却根本不会出现在 cfg.Env 里。只
// 读 cfg.Env 的话，扫描会在 ~/.kimi-code 里找，而 kimi 其实写到了别处。stat 校验保证一个
// 没有 sessions 目录的 home 不会让扫描直接停摆。
func kimiSessionRoot(kimiHome string) string {
	if kimiHome = strings.TrimSpace(kimiHome); kimiHome == "" {
		kimiHome = strings.TrimSpace(os.Getenv("KIMI_CODE_HOME"))
	}
	if kimiHome != "" {
		dir := filepath.Join(kimiHome, "sessions")
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir
		}
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	dir := filepath.Join(home, ".kimi-code", "sessions")
	if info, err := os.Stat(dir); err == nil && info.IsDir() {
		return dir
	}
	return ""
}
