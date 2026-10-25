package agent

// 文件职责：本文件实现 Backend 接口中的「CodeBuddy」后端。CodeBuddy 是 Claude Code
// 的分支产品，本后端以 --output-format stream-json 拉起它的 CLI，并通过 stdin/stdout
// 走 SDK 风格的 stream-json 协议：stdout 上每行是一个 JSON 事件（assistant / user /
// system / result / log / control_request 等），prompt 作为一条 user 事件写进 stdin。
//
// 与 claude.go 的执行模型同构：stdin/stdout 并发读写避免管道死锁；写完后不关闭
// stdin，保持开放以在运行中应答 control_request 的自动批准；用 runContext 实现
// 「零超时 = 不设截止时间」的语义。
//
// 执行流程：Execute 拼好参数拉起进程 → 独立 goroutine 先把 prompt 写进 stdin → 主
// goroutine 循环读 stdout 事件：assistant 消息拆出文本/思考/工具调用并推送；user 消
// 息里的 tool_result 回填工具结果；result 事件汇总终态与用量；control_request 一律
// 自动批准 → 进程退出后用 finalizeStreamResult 把各类信号综合成最终状态
// （completed/failed/timeout/aborted），连同会话 ID、token 用量一起放进 Result。
//
// 会话续跑用 --resume；daemon 判定续跑被拒（resumeWasRejected）时会在 Result 里置
// ResumeRejected，请求方会丢弃失效会话 ID 并用全新会话重试。

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

// codebuddyBackend 实现 Backend 接口：以 --output-format stream-json 拉起 CodeBuddy
// CLI（Claude Code 的一个分支）。
// 与 claude.go 的执行模型一致：stdin/stdout 并发读写避免管道死锁；stdin 保持打开以便
// 应答 control_request 的自动批准；用 runContext 实现「零超时 = 无截止时间」的语义。
type codebuddyBackend struct {
	cfg Config
}

// codebuddyBlockedArgs 是 daemon 写死的参数清单，用户配置的 custom_args 不得覆盖它们。
// 覆盖这些参数会破坏 daemon ↔ codebuddy 之间的通信协议，或绕过运行安全约束。
var codebuddyBlockedArgs = map[string]blockedArgMode{
	"-p":                       blockedStandalone, // 非交互模式
	"--output-format":          blockedWithValue,  // stream-json 协议所必需
	"--input-format":           blockedWithValue,  // stream-json 协议所必需
	"--permission-mode":        blockedWithValue,  // 由 bypassPermissions 接管，保证自主运行
	"--mcp-config":             blockedWithValue,  // 由 daemon 依据 agent.mcp_config 注入
	"--tools":                  blockedWithValue,  // 「生命认知」任务可能关掉内置工具
	"--allowedTools":           blockedWithValue,  // 绝不让 custom_args 扩大 Life 的访问面
	"--disallowedTools":        blockedList,
	"--strict-mcp-config":      blockedStandalone,
	"--no-session-persistence": blockedStandalone,
	// `--effort` 归「每个 agent 的 thinking_level 选择器」管，用户自带的 custom_arg
	// 不能静默压过它。
	"--effort": blockedWithValue,
}

// buildCodebuddyArgs 根据 ExecOptions 拼出 codebuddy 的一次性命令行参数。
//
// 固定前缀保证走 stream-json 协议并以 bypassPermissions 无人值守运行；随后按需追加
// 模型、思考强度、最大回合数、系统提示词、续跑会话等；最后把用户 custom_args 与
// ExtraArgs 经 blocked 清单过滤后接上。ExtraArgs 是 daemon 自己注入的扩展参数，
// CustomArgs 是用户配置的，二者分别过滤再拼接。
func buildCodebuddyArgs(opts ExecOptions, logger *slog.Logger) []string {
	args := []string{
		"-p",
		"--output-format", "stream-json",
		"--input-format", "stream-json",
		"--verbose",
		"--permission-mode", "bypassPermissions",
		// CodeBuddy 的交互式工具在 daemon 无头的 stream-json 传输下没有可渲染的 UI。
		// AskUserQuestion 和 ExitPlanMode 都被排除在 CodeBuddy 的 permission-mode 终审
		// 之外，所以 --permission-mode bypassPermissions 并不会自动批准它们——它们总会
		// 走到权限桥，然后卡住回合、等一个永远不会有人给的确认（GitHub #6012）。
		// EnterPlanMode 与它们一起被禁用：留着它，模型会进入一个「进去后没有工具能出
		// 来」的计划模式。计划形态的工作仍然会发生——计划以普通 assistant 输出的形式
		// 写出，而不是挡在一个审批闸门后面。
		//
		// 每个工具名单独传一个值：--disallowedTools 是可变参数，CodeBuddy 用精确比对把
		// 每项与工具名对照（PermissionUtils.matchPermissionRules），所以逗号拼起来的
		// 字符串一条都匹配不上——尽管 CLI 自己的帮助文本声称可以。
		"--disallowedTools", "AskUserQuestion", "EnterPlanMode", "ExitPlanMode",
	}
	if opts.LifeCognition {
		// 后台认知禁用内置工具及用户 MCP，只加载本轮专用 MCP。
		args = append(args, "--tools", "", "--strict-mcp-config", "--no-session-persistence")
	}
	// 注意：--strict-mcp-config 在常规路径上刻意永不传。它的含义是「只用 --mcp-config
	// 里的服务器」，会把 CodeBuddy 的用户级、项目级和本地级配置全部丢掉。对照
	// CodeBuddy 2.x、每一级各注册一个真实 MCP 服务器、用进程启动做基准实测的结果：
	//
	//	仅 --mcp-config ........ 托管级 + 用户级 + 本地级  <- 我们要的
	//	--mcp-config + strict ... 只有托管级
	//	仅 strict .............. 一个都没有
	//
	// 这个并集也正是 mergeRuntimeAndAgentMcpConfig 承诺的效果：新增一个托管服务器不
	// 应禁用用户自己的。CodeBuddy 原生就会做并集，且同名冲突时托管条目已经会赢
	// （已验证），所以 daemon 不再预先合并——见 loadRuntimeMcpServerConfigs 里的
	// codebuddy 说明。
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}
	if opts.ThinkingLevel != "" {
		args = append(args, "--effort", opts.ThinkingLevel)
	}
	if opts.MaxTurns > 0 {
		args = append(args, "--max-turns", fmt.Sprintf("%d", opts.MaxTurns))
	}
	if opts.SystemPrompt != "" {
		args = append(args, "--append-system-prompt", opts.SystemPrompt)
	}
	if opts.ResumeSessionID != "" {
		args = append(args, "--resume", opts.ResumeSessionID)
	}
	args = append(args, filterCustomArgs(opts.ExtraArgs, codebuddyBlockedArgs, logger)...)
	args = append(args, filterCustomArgs(opts.CustomArgs, codebuddyBlockedArgs, logger)...)
	return args
}

// Execute 是 Backend 接口的入口：拿一条用户 prompt，起一个 codebuddy CLI 进程，
// 完成一轮 stream-json 会话并跑完整段对话。
//
// 流程：确认可执行文件存在 → buildCodebuddyArgs 拼参数 → 有托管 MCP 配置就先写成临时
// 文件用 --mcp-config 传入（temp 文件所有权随后转移给读取协程）→ 起进程 → 独立
// goroutine 写 prompt 到 stdin（写失败立即关 stdin）→ 主循环逐行解析 stdout 事件并
// 翻译成 Message / 汇总用量 → 进程退出后由 finalizeStreamResult 综合（是否超时/取消/
// 写 prompt 失败/扫描出错/CLI 有无回 result）得出最终状态 → 再把 stderr 尾部、协议观测
// 日志、续跑被拒判定等收尾，最后把 Result 放进 resCh。结果与流式消息分别经两个
// channel 返回。
func (b *codebuddyBackend) Execute(ctx context.Context, prompt string, opts ExecOptions) (*Session, error) {
	execPath := b.cfg.ExecutablePath
	if execPath == "" {
		execPath = "codebuddy"
	}
	if _, err := exec.LookPath(execPath); err != nil {
		return nil, fmt.Errorf("codebuddy executable not found at %q: %w", execPath, err)
	}

	timeout := opts.Timeout
	runCtx, cancel := runContext(ctx, timeout)

	// 续接一个本地已经没有记录的会话时，CLI 会起来但一个事件都不发（2.125.5 实测：
	// 进程在，stdout 静默，任务一直 running）。这种情况下马上按"续跑被拒"返回，
	// 让 daemon 走既有的换新会话重试，而不是让一个任务挂到超时。
	if opts.ResumeSessionID != "" && codebuddySessionTranscriptMissing(b.cfg.Env, opts.ResumeSessionID) {
		cancel()
		b.cfg.Logger.Info("codebuddy resume target missing locally; asking for a fresh session",
			"session_id", opts.ResumeSessionID)
		messages := make(chan Message)
		close(messages)
		result := make(chan Result, 1)
		result <- Result{
			Status:         "failed",
			Error:          "codebuddy: resume transcript not found locally",
			SessionID:      "",
			ResumeRejected: true,
		}
		close(result)
		return &Session{Messages: messages, Result: result}, nil
	}

	args := buildCodebuddyArgs(opts, b.cfg.Logger)

	// 托管 agent 配置会写到临时文件、用 --mcp-config 传入——它是在 CodeBuddy 从自身
	// 用户级、项目级和本地级 scope 加载的内容之上「追加」；buildCodebuddyArgs 从不过
	// --strict-mcp-config，所以那些 scope 一直保留。同名的托管条目原生就能赢过冲突。
	//
	// 与其他地方一样是三态：JSON 的 `null` 表示「继承运行时配置」、干脆不传该 flag；
	// 而一个显式的空对象是「一份恰好不含服务器的托管集合」。
	var mcpConfigPath string
	var mcpFileCleanup func()
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
	// 若还没到 goroutine 接手就提前 return（例如后续管道/启动失败），先清掉临时文件。
	defer func() {
		if mcpFileCleanup != nil {
			mcpFileCleanup()
		}
	}()

	cmd := b.cfg.commandAt(execPath).exec(runCtx, args...)
	hideAgentWindow(cmd)
	b.cfg.logAgentCommand(cmd, newAgentCommandLogArgs(args))
	cmd.WaitDelay = 10 * time.Second
	if opts.Cwd != "" {
		cmd.Dir = opts.Cwd
	}
	env := make(map[string]string, len(b.cfg.Env)+3)
	for key, value := range b.cfg.Env {
		env[key] = value
	}
	cmd.Env = buildCodebuddyEnv(env)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("codebuddy stdout pipe: %w", err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("codebuddy stdin pipe: %w", err)
	}
	var closeStdinOnce sync.Once
	closeStdin := func() { closeStdinOnce.Do(func() { _ = stdin.Close() }) }

	stderrBuf := newStderrTail(newLogWriter(b.cfg.Logger, "[codebuddy:stderr] "), agentStderrTailBytes)
	cmd.Stderr = stderrBuf

	if err := startOwnedProcessTree(cmd, b.cfg.Logger); err != nil {
		closeStdin()
		cancel()
		return nil, fmt.Errorf("start codebuddy: %w", err)
	}

	b.cfg.Logger.Info("codebuddy started", "pid", cmd.Process.Pid, "cwd", opts.Cwd, "model", opts.Model)
	// 进程一旦 Wait 完，/proc 条目就没了；CLI 身份必须在 Start 之后立刻记下来，
	// 退出后的越组后代回收才有可信下界。
	cliIdentity := captureCLIIdentity(cmd.Process.Pid)

	// cmd.Start() 已成功——临时文件所有权转给下面的 goroutine。
	mcpFileCleanup = nil

	msgCh := make(chan Message, 256)
	resCh := make(chan Result, 1)

	// 把 prompt 放进独立 goroutine 写，防止死锁：CodeBuddy（同 Claude Code）会先把
	// 启动横幅打到 stdout 再去读 stdin；若同步写，一旦管道缓冲填满就会卡死。写完 prompt
	// 后 stdin 保持打开，这样运行中来的 control_request 事件还能被应答。
	writeDone := make(chan error, 1)
	go func() {
		err := writeCodebuddyInput(stdin, prompt)
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
		sawBackgroundTask := false
		var sessionID string
		var requestID string
		usage := make(map[string]TokenUsage)
		usageCalls := make(map[codebuddyUsageCall]TokenUsage)
		var resultUsage map[string]TokenUsage
		eventCount := 0
		invalidEventCount := 0
		assistantEventCount := 0
		toolUseCount := 0
		unreadableAssistantCount := 0

		// 上下文取消时关掉 stdout，让 scanner.Scan() 能解除阻塞返回。
		go func() {
			<-runCtx.Done()
			closeStdin()
			_ = stdout.Close()
		}()

		scanner := newAgentStreamScanner(stdout)

		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}

			var msg codebuddySDKMessage
			if err := json.Unmarshal([]byte(line), &msg); err != nil {
				invalidEventCount++
				continue
			}
			eventCount++

			// `_requestId` 由子进程自己生成并写在 assistant/status/result 事件上，作为 provider 请求标识保存；
			// AgentLens 的 trace/span 对应另行验收。取第一条非空值即可：同一个 CLI 进程只有一个
			// 根请求，后续事件重复的是同一个值。绝不能拿父进程环境变量
			// CODEBUDDY_ROOT_REQUEST_ID 顶替——那是 daemon 自己的请求标识。
			if requestID == "" && msg.ProviderRequestID != "" {
				requestID = msg.ProviderRequestID
			}

			// 初次SDK会话标识绑定根会话；其它信封的不同会话不能悄悄替换它。
			if msg.SessionID != "" {
				if sessionID != "" && msg.SessionID != sessionID {
					b.cfg.Logger.Warn("codebuddy SDK session conflict", "root_session", sessionID, "event_session", msg.SessionID, "event_type", msg.Type)
				}
				if sessionID == "" || msg.Type == "result" {
					sessionID = msg.SessionID
				}
			}

			switch msg.Type {
			case "assistant":
				assistantEventCount++
				turn := b.handleAssistant(msg, msgCh, usage, usageCalls)
				toolUseCount += turn.toolUses
				if !turn.understood {
					unreadableAssistantCount++
				}
				lastAssistantText = turn.resolveFallback(lastAssistantText)
			case "user":
				b.handleUser(msg, msgCh)
			case "system":
				// 无头执行不能在结果回传后继续接收后台任务通知。
				if codebuddySystemIsBackgroundTask(msg.Subtype) {
					sawBackgroundTask = true
				}
				trySend(msgCh, Message{Type: MessageStatus, Status: "running", SessionID: sessionID})
			case "result":
				sawResult = true
				finalResultText = msg.ResultText
				resultIsError = msg.IsError
				resultUsage = codebuddyResultUsage(msg, opts.Model)
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
			// 扫描器已停止消费 stdout；主动关掉它，防止一个畸形/超大的写端在
			// cmd.Wait 时把进程堵住。
			_ = stdout.Close()
		}

		closeStdin()

		// 等待进程退出。
		exitErr := cmd.Wait()
		releaseProcessGroup(cmd)
		duration := time.Since(startTime)
		// writeDone 是有缓冲的（容量 1），且写端总会发送——到 cmd 退出时，prompt 写入
		// 要么已成功、要么遇到 broken pipe、要么被结束 cmd 的 kill 解除阻塞。
		writeErr := <-writeDone

		completionGuardError := ""
		if sawBackgroundTask {
			completionGuardError = "codebuddy emitted a background task system event; Multica-managed runs require foreground execution (set CODEBUDDY_CODE_DISABLE_BACKGROUND_TASKS=1)"
		}
		finalStatus, finalOutput, finalError := finalizeStreamResult(
			"codebuddy",
			timeout,
			runCtx.Err(),
			writeErr,
			exitErr,
			sessionID,
			streamTerminalState{
				lastAssistantText: lastAssistantText,
				finalResultText:   finalResultText,
				sawResult:         sawResult,
				resultIsError:     resultIsError,
				scanErr:           scanErr,
			},
			completionGuardError,
		)

		// cmd.Wait() has returned — stderrBuf.Tail() is complete. Resume
		// rejection phrases often land only on stderr (mirror Claude MUL-4966).
		stderrTail := stderrBuf.Tail()
		if finalError != "" {
			finalError = withAgentStderr(finalError, "codebuddy", stderrTail)
		}
		logStreamProtocolObservation(b.cfg.Logger, streamProtocolObservation{
			provider:                   "codebuddy",
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

		b.cfg.Logger.Info("codebuddy finished", "pid", cmd.Process.Pid, "status", finalStatus, "duration", duration.Round(time.Millisecond).String())

		resumeRejected := resumeWasRejected(opts.ResumeSessionID, sessionID, finalStatus == "failed", finalError, stderrTail)
		reportedSessionID := resolveSessionID(opts.ResumeSessionID, sessionID, finalStatus == "failed", finalError, stderrTail)
		if resumeRejected {
			b.cfg.Logger.Info("codebuddy resume was rejected; dropping session id and signalling fresh-session retry",
				"requested_resume", opts.ResumeSessionID,
				"emitted_session", sessionID,
			)
		}

		// 只在"没正常收尾"时回收越组后代：正常完成的运行可能故意留下后台服务，
		// 不能因为我们看不出意图就杀掉。异常退出、被杀、超时、取消才清理。
		if exitErr != nil || runCtx.Err() != nil || finalStatus != "completed" {
			reapTaskEscapees(b.cfg.Env["MULTICA_TASK_ID"], cliIdentity, b.cfg.Logger)
		}
		usage = codebuddySelectUsage(usage, resultUsage, opts.ResumeSessionID != "", b.cfg.Logger)
		// 子 Agent 的调用记录在父 session 目录下的 subagents/ 里，属于本次执行的 0:N 链路。
		// 只有路径、CID、trace 都唯一时才并入；并入的是各自独立的用量行，不覆盖父链路。
		subUsage, subTraces, subCalls := codebuddyCollectSubagentUsage(b.cfg.Env, reportedSessionID, requestID)
		// 子 Agent 的 assistant 信封也会进 stream，所以同一个调用可能已经被记过一次。
		// 只在"stream 没有提供逐调用身份"或"这个 key 确实没被观测到"时才补用量；
		// 已观测的只补那条独立 trace。判不准就不加，绝不按最大值/差值猜。
		switch {
		case len(subCalls) == 0:
			// 没有可比对的调用集合，子用量未知：保留 stream 已知量，只让 traces 落地。
			if len(subTraces) > 0 {
				b.cfg.Logger.Info("subagent usage stays unknown: no per-call identity in stream",
					"traces", len(subTraces))
			}
		default:
			if usage == nil {
				usage = map[string]TokenUsage{}
			}
			usage = mergeSubagentUsage(usage, usageCalls, subCalls, streamIdentityComplete(usage, usageCalls))
			_ = subUsage // 逐调用合并已经完成，聚合值只用于日志
		}
		// SDK 侧溯源：只在 session 文件唯一、文件内 trace id 唯一时附上；任何歧义
		// 都留空，让上层把这条链路当作"只有 CID 的准确关联"。
		sdkTraceID, sdkSum, sdkSource, sdkFile := codebuddySDKProvenanceWithSource(
			b.cfg.Env, reportedSessionID, opts.Cwd, requestID,
		)
		if sdkTraceID != "" {
			// 文件位置只留在日志与私有证据里；API 字段要用固定标记。
			b.cfg.Logger.Info("codebuddy SDK trace provenance", "cid", requestID, "sdk_trace_id", sdkTraceID, "source_file", sdkFile)
		}
		resCh <- Result{
			Status:               finalStatus,
			Output:               finalOutput,
			Error:                finalError,
			DurationMs:           duration.Milliseconds(),
			SessionID:            reportedSessionID,
			Usage:                usage,
			ProviderTraceID:      requestID,
			SDKTraceID:           sdkTraceID,
			SDKTraceSourceSHA256: sdkSum,
			SDKTraceSourcePath:   sdkSource,
			ExtraTraces:          subTraces,
			ResumeRejected:       resumeRejected,
		}
	}()

	return &Session{Messages: msgCh, Result: resCh}, nil
}

// handleAssistant 处理一条 assistant 事件：解析出内容块（文本 / 思考 / 工具调用），
// 把每一块翻译成对应 Message 推送，按模型累加 token 用量，并返回本回合的归纳
// （assistantTurn：文本、工具调用数、是否完全理解）。
//
// 理解不了正文时（JSON 解析失败或出现未知块类型），返回的 turn.understood 为 false，
// 调用方就不会让旧回合的文本来顶替本回合。
func (b *codebuddyBackend) handleAssistant(msg codebuddySDKMessage, ch chan<- Message, usage map[string]TokenUsage, calls map[codebuddyUsageCall]TokenUsage) assistantTurn {
	var content codebuddyMessageContent
	if err := json.Unmarshal(msg.Message, &content); err != nil {
		// 正文读不懂：understood 保持 false，调用方会丢弃任何 fallback，而不是让更早的
		// 回合来冒充这个回合。
		return assistantTurn{}
	}
	turn := assistantTurn{understood: true}
	var assistantText strings.Builder
	toolUseCount := 0

	// SDK 的 _messageId 标识模型调用；一个调用可拆成多个工具消息，也可更新同一用量快照。
	if msg.ParentToolUseID == "" && content.Usage != nil && content.Model != "" {
		snapshot := TokenUsage{
			InputTokens:      codebuddyUncachedInput(content.Usage.InputTokens, content.Usage.CacheReadInputTokens, content.Usage.CacheCreationInputTokens),
			OutputTokens:     content.Usage.OutputTokens,
			CacheReadTokens:  content.Usage.CacheReadInputTokens,
			CacheWriteTokens: content.Usage.CacheCreationInputTokens,
		}
		if codebuddyUsageHasTokens(snapshot.InputTokens, snapshot.OutputTokens, snapshot.CacheReadTokens, snapshot.CacheWriteTokens) {
			var previous TokenUsage
			key := codebuddyUsageCall{msg.ProviderRequestID, msg.ProviderMessageID, content.Model}
			if key.request == "" || key.message == "" {
				// SDK 响应 ID 只用于流内去重，不冒充可关联外部轨迹的调用身份。
				key = codebuddyUsageCall{message: content.ID, model: content.Model}
			}
			if key.message != "" {
				previous = calls[key]
				calls[key] = snapshot
			}
			u := usage[content.Model]
			u.InputTokens += snapshot.InputTokens - previous.InputTokens
			u.OutputTokens += snapshot.OutputTokens - previous.OutputTokens
			u.CacheReadTokens += snapshot.CacheReadTokens - previous.CacheReadTokens
			u.CacheWriteTokens += snapshot.CacheWriteTokens - previous.CacheWriteTokens
			usage[content.Model] = u
		}
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
			// 一个我们不渲染的块类型，可能正以我们读不懂的形状携带模型的回答，所以绝不
			// 能把本回合当成「沉默」。识别新的无文本块是这里刻意加的一行，不是漏网。
			turn.understood = false
		}
	}
	turn.text = assistantText.String()
	turn.toolUses = toolUseCount
	return turn
}

// handleUser 处理一条 user 事件：把其中的 tool_result 块转成 MessageToolResult
// 推送出去（按 tool_use_id 与之前的工具调用对上号）。
func (b *codebuddyBackend) handleUser(msg codebuddySDKMessage, ch chan<- Message) {
	var content codebuddyMessageContent
	if err := json.Unmarshal(msg.Message, &content); err != nil {
		return
	}

	for _, block := range content.Content {
		if block.Type == "tool_result" {
			resultStr := ""
			if block.Content != nil {
				resultStr = string(block.Content)
			}
			trySend(ch, Message{
				Type:   MessageToolResult,
				CallID: block.ToolUseID,
				Output: resultStr,
			})
		}
	}
}

// handleControlRequest 应答 control_request 事件：在自主/daemon 模式下自动批准所有
// 工具调用——把批准响应写成 JSON 行写回 stdin。
func (b *codebuddyBackend) handleControlRequest(msg codebuddySDKMessage, stdin interface{ Write([]byte) (int, error) }) {
	// 自主/daemon 模式下自动批准所有工具使用。
	var req codebuddyControlRequestPayload
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
	// Do not rewrite run_in_background here: under bypassPermissions most
	// tools never emit control_request, and CodeBuddy does not use Claude's
	// async_launched tool_result shape. Background work is disabled via
	// CODEBUDDY_CODE_DISABLE_BACKGROUND_TASKS before tool execution.

	response := map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype":    "success",
			"request_id": msg.RequestID,
			"response": map[string]any{
				// CodeBuddy 的 SdkPermissionClient 读的是 `allowed`，键缺失即视为拒绝；
				// `behavior` 是 Claude Code 的拼写，这个分支在其它权限路径上仍认它。
				// 两个都发，避免一次「批准」被误读成「静默拒绝」。
				"allowed":      true,
				"behavior":     "allow",
				"updatedInput": inputMap,
			},
		},
	}

	data, err := json.Marshal(response)
	if err != nil {
		b.cfg.Logger.Warn("codebuddy: failed to marshal control response", "error", err)
		return
	}
	data = append(data, '\n')
	if _, err := stdin.Write(data); err != nil {
		b.cfg.Logger.Warn("codebuddy: failed to write control response", "error", err)
	}
}

// writeCodebuddyInput 把用户 prompt 包装成一条 stream-json 的 user 事件（含一条 text
// 内容块）写入 stdin。CodeBuddy CLI 以换行分隔的 JSON 读入，故末尾补一个 '\n'。
func writeCodebuddyInput(w io.Writer, prompt string) error {
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
		return fmt.Errorf("marshal codebuddy input: %w", err)
	}
	data = append(data, '\n')
	if _, err := w.Write(data); err != nil {
		return err
	}
	return nil
}

const codebuddyDisableBackgroundTasksEnv = "CODEBUDDY_CODE_DISABLE_BACKGROUND_TASKS"

// buildCodebuddyEnv merges task env and always forces background tasks off.
// Multica's adapter closes stdin after the first result; CodeBuddy's
// background lifecycle can push task_notification after that point, which we
// cannot observe. The official CLI documents this variable for exactly that
// headless shape (https://www.codebuddy.ai/docs/cli/headless).
//
// Append the forced entry last so os/exec's platform-aware dedup
// (case-insensitive on Windows, last-wins) keeps our "=1" even when the
// inherited or custom_env key differs only by case.
func buildCodebuddyEnv(extra map[string]string) []string {
	return append(buildEnv(extra), codebuddyDisableBackgroundTasksEnv+"=1")
}

// codebuddySystemIsBackgroundTask reports CodeBuddy's real background-task
// system subtypes (not Claude's async_launched tool_result).
func codebuddySystemIsBackgroundTask(subtype string) bool {
	switch strings.TrimSpace(subtype) {
	case "task_started", "task_progress", "task_updated", "task_notification":
		return true
	default:
		return false
	}
}

// ── Codebuddy SDK JSON types ──

// codebuddySDKMessage 是 stdout 上每一行 stream-json 事件的统一信封；不同 type 使用
// 不同的字段子集（assistant/user 用 Message，result 用 result 字段，log 用 Log，
// control_request 用 RequestID + Request）。
type codebuddySDKMessage struct {
	ParentToolUseID string          `json:"parent_tool_use_id,omitempty"`
	Type            string          `json:"type"`
	Message         json.RawMessage `json:"message,omitempty"`
	Subtype         string          `json:"subtype,omitempty"`
	SessionID       string          `json:"session_id,omitempty"`
	Model           string          `json:"model,omitempty"`

	// result 事件的字段
	ResultText string                               `json:"result,omitempty"`
	IsError    bool                                 `json:"is_error,omitempty"`
	DurationMs float64                              `json:"duration_ms,omitempty"`
	NumTurns   int                                  `json:"num_turns,omitempty"`
	Usage      *codebuddyUsage                      `json:"usage,omitempty"`
	ModelUsage map[string]codebuddyResultModelUsage `json:"modelUsage,omitempty"`

	// log 事件的字段
	Log *codebuddyLogEntry `json:"log,omitempty"`

	// control_request 事件的字段
	RequestID string          `json:"request_id,omitempty"`
	Request   json.RawMessage `json:"request,omitempty"`

	// ProviderRequestID 是事件上的 `_requestId`：子进程为本次根请求生成的标识，
	// 外部观测系统的具体解析对应须另行验证。它不是父进程环境里的
	// CODEBUDDY_ROOT_REQUEST_ID（那是调用方的请求），也不是 control_request 的
	// `request_id`（那是权限桥的应答序号）；assistant/status/result 从子进程事件取证。
	ProviderRequestID string `json:"_requestId,omitempty"`
	ProviderMessageID string `json:"_messageId,omitempty"`
}

// codebuddyLogEntry 是 log 事件里的日志条目。
type codebuddyLogEntry struct {
	Level   string `json:"level"`
	Message string `json:"message"`
}

// codebuddyMessageContent 是 assistant/user 事件 message 字段的内容：角色 + 内容块 +
// 可选的用量与模型名。
type codebuddyMessageContent struct {
	ID      string                  `json:"id"`
	Role    string                  `json:"role"`
	Model   string                  `json:"model"`
	Content []codebuddyContentBlock `json:"content"`
	Usage   *codebuddyUsage         `json:"usage,omitempty"`
}

// codebuddyUsage 是内容块内的 token 用量明细（snake_case 拼写）。
type codebuddyUsage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
}

// codebuddyResultModelUsage 是 result 事件上按模型分的用量明细（camelCase 拼写）。
type codebuddyResultModelUsage struct {
	InputTokens              int64 `json:"inputTokens"`
	OutputTokens             int64 `json:"outputTokens"`
	CacheReadInputTokens     int64 `json:"cacheReadInputTokens"`
	CacheCreationInputTokens int64 `json:"cacheCreationInputTokens"`
}

// codebuddyContentBlock 是消息内容里的一个块：文本 / 思考文本 / 工具调用 / 工具结果。
type codebuddyContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
}

// codebuddyControlRequestPayload 是 control_request 事件里的权限请求载荷。
type codebuddyControlRequestPayload struct {
	Subtype  string          `json:"subtype"`
	ToolName string          `json:"tool_name,omitempty"`
	Input    json.RawMessage `json:"input,omitempty"`
}

// codebuddyResultUsage 从收尾的 result 事件里提取 token 用量，转成统一 TokenUsage 图。
// 优先用按模型拆分的 ModelUsage；其次退回用整场 Usage + 模型名（fallbackModel 兜底）。
// 带数字的模型条目才算数（codebuddyUsageHasTokens），全零或缺失就返回 nil。
func codebuddyResultUsage(msg codebuddySDKMessage, fallbackModel string) map[string]TokenUsage {
	if len(msg.ModelUsage) > 0 {
		usage := make(map[string]TokenUsage, len(msg.ModelUsage))
		for model, u := range msg.ModelUsage {
			if model == "" || !codebuddyUsageHasTokens(u.InputTokens, u.OutputTokens, u.CacheReadInputTokens, u.CacheCreationInputTokens) {
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
	if msg.Usage == nil || model == "" || !codebuddyUsageHasTokens(
		msg.Usage.InputTokens,
		msg.Usage.OutputTokens,
		msg.Usage.CacheReadInputTokens,
		msg.Usage.CacheCreationInputTokens,
	) {
		return nil
	}
	return map[string]TokenUsage{
		model: {
			InputTokens:      codebuddyUncachedInput(msg.Usage.InputTokens, msg.Usage.CacheReadInputTokens, msg.Usage.CacheCreationInputTokens),
			OutputTokens:     msg.Usage.OutputTokens,
			CacheReadTokens:  msg.Usage.CacheReadInputTokens,
			CacheWriteTokens: msg.Usage.CacheCreationInputTokens,
		},
	}
}

type codebuddyUsageCall struct {
	request, message, model string
}

// assistant 流属于本轮实际调用；result 摘要可能跨会话轮次累计，不能覆盖已观测流。
func codebuddySelectUsage(stream, summary map[string]TokenUsage, resumed bool, logger *slog.Logger) map[string]TokenUsage {
	if len(stream) == 0 {
		if !resumed || len(summary) == 0 {
			return summary
		}
		logger.Warn("codebuddy usage source conflict", "reason", "resumed_summary_scope_unknown", "stream_usage", stream, "result_usage", summary, "chosen_source", "none", "scope", "session summary cannot establish current run usage")
		return stream
	}
	if len(summary) == 0 {
		return stream
	}
	reason := "result_summary_differs_from_observed_stream"
	equal := len(stream) == len(summary)
	for model, observed := range stream {
		reported, ok := summary[model]
		if !ok {
			reason = "model_identity_conflict"
			equal = false
			break
		}
		if reported != observed {
			equal = false
		}
	}
	if len(stream) != len(summary) {
		reason = "model_identity_conflict"
	}
	if !equal {
		logger.Warn("codebuddy usage source conflict", "reason", reason, "stream_usage", stream, "result_usage", summary, "chosen_source", "assistant_stream_observed", "scope", "observed SDK calls; calls absent from stream remain unknown")
	}
	return stream
}

// codebuddyUncachedInput 把 CodeBuddy 的 input_tokens 归一成"不含缓存的输入"，
// 与 modelUsage.inputTokens 和服务端 task_usage.input_tokens 的口径一致。
//
// 这条通道（copilot.tencent.com）的 usage.input_tokens 是**整段提示词总量**，
// cache_read 与 cache_creation 已经含在里面：实测单次调用 27156(cache_creation)
// + 2816(cache_read) = 29972 = input_tokens，同一请求的 modelUsage 则报
// inputTokens=0、两项缓存各自单列。若把两种口径都按"相加"记账，输入会被算两遍
// （生产库里 5 行能对上 input == cache_read+cache_write 的记录就是这种双计）。
//
// 只在缓存值确实包含在输入里时才相减（input >= 缓存合计）。上游若改成 Anthropic
// 原生的"input 不含缓存"口径，input 会小于缓存合计，此时原样返回。
func codebuddyUncachedInput(input, cacheRead, cacheWrite int64) int64 {
	cached := cacheRead + cacheWrite
	if cached <= 0 || input < cached {
		return input
	}
	return input - cached
}

// codebuddyUsageHasTokens 报告这四项 token 计数里是否至少有一个大于 0——避免把
// 「字段齐全但全零」的用量当有效数据。
func codebuddyUsageHasTokens(input, output, cacheRead, cacheWrite int64) bool {
	return input > 0 || output > 0 || cacheRead > 0 || cacheWrite > 0
}

// streamIdentityComplete 判断 stream 里的用量是否都能被逐调用身份解释完。
// 只有完整时才敢按 key 判断"这个子调用没被观测过"；只要有一个模型对不上，
// 就说明有未知调用混在里面，此时任何增量都可能重复记账。
func streamIdentityComplete(usage map[string]TokenUsage, observed map[codebuddyUsageCall]TokenUsage) bool {
	if len(observed) == 0 || len(usage) == 0 {
		return false
	}
	sum := map[string]TokenUsage{}
	for key, value := range observed {
		if key.request == "" || key.message == "" {
			return false
		}
		merged := sum[key.model]
		merged.InputTokens += value.InputTokens
		merged.OutputTokens += value.OutputTokens
		merged.CacheReadTokens += value.CacheReadTokens
		merged.CacheWriteTokens += value.CacheWriteTokens
		sum[key.model] = merged
	}
	if len(sum) != len(usage) {
		return false
	}
	for model, total := range usage {
		if sum[model] != total {
			return false
		}
	}
	return true
}

// mergeSubagentUsage 把"stream 里还没观测到"的子调用增量并入用量。
//
// 前提是 stream 的逐调用身份完整：只有那时"没在 observed 里"才等于"没被记账"。
// 身份不完整时保留 stream 已知量、不加任何子增量，避免同一调用被记两次。
func mergeSubagentUsage(usage map[string]TokenUsage, observed map[codebuddyUsageCall]TokenUsage, calls []SubagentCall, identityComplete bool) map[string]TokenUsage {
	if len(calls) == 0 || !identityComplete {
		return usage
	}
	if usage == nil {
		usage = map[string]TokenUsage{}
	}
	for _, call := range calls {
		key := codebuddyUsageCall{request: call.CID, message: call.MessageID, model: call.Model}
		if _, seen := observed[key]; seen {
			continue
		}
		merged := usage[call.Model]
		merged.InputTokens += call.Usage.InputTokens
		merged.OutputTokens += call.Usage.OutputTokens
		merged.CacheReadTokens += call.Usage.CacheReadTokens
		merged.CacheWriteTokens += call.Usage.CacheWriteTokens
		usage[call.Model] = merged
	}
	return usage
}
