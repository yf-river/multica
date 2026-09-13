package agent

// 文件导读：Dim（dimcode）CLI 的 ACP 驱动封装。
//
// 角色定位：把 Dim（dimcode）适配成 daemon 的 Backend。Dim 通过 `dim acp` 子命令把它的
// agent 循环暴露在 ACP（Agent Client Protocol，stdin/stdout 上的 JSON-RPC 2.0）上，本文件
// 拉起该子进程并驱动完整会话生命周期（initialize → session/new | session/load → 权限/模式
// 配置 → 可选 set_model / effort → session/prompt → session/close），把正文、工具调用等
// 翻译成 Message/Result 上抛。
//
// 职责边界：协议面与共享的 hermesClient ACP 传输一致（Hermes/Kimi/Kiro/Traecli/QwenPaw
// 都在用），本文件只保留 Dim 特有的差异：二进制名、会话引导方式（session/load 跨进程续接，
// 而不是 session/resume）、跨运行续接所需的版本门槛（>= 0.3.10）、以及创建会话后必须把
// 只读权限提升为 full-access。
//
// 与 Dim ACP 服务端约定的几个要点：
//   - `initialize` 不宣传任何 authMethods，所以无需 `authenticate` 步骤；CLI 直接用用户
//     既有的 Dim OAuth 登录态。
//   - `session/new` 要求绝对 cwd（相对路径会被拒）。
//   - Dim 的 ACP 服务端在创建会话时硬编码一套只读权限预设，会静默拒绝一切文件写与进程
//     拉起。因此后端在 session/new 之后立刻发 `session/set_config_option`（permission →
//     full-access、mode → agent），让 Multica agent 能干实事。续接的会话经 `session/load`
//     会保留这些设置，所以 set_config_option 在新旧会话上都要重放（幂等）。模型覆盖走标准
//     的 `session/set_model` RPC；模型目录由 `session/new` 以 models.availableModels /
//     configOptions 宣传。
//   - `session/load` 跨进程续接既有会话：dim 0.3.10+ 在持有进程退出后约 5 秒内释放其
//     每进程会话锁，因此新的 `dim acp` 进程里的后续运行可以接着聊。更早的 0.3.x 会把会话
//     永久绑死在创建它的进程上；那些版本上 load 会失败，靠 ResumeRejected 让 daemon 用新
//     会话重试。
//   - 工具名本身已是小写（"write"、"read"、"exec"）；共享的 kimiToolNameFromTitle 归一化
//     让 UI 标识符保持稳定。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

// dimBlockedArgs 是 daemon 固定写死、不允许用户经 custom_args 覆盖的开关。其中 `acp` 是驱动
// ACP JSON-RPC 传输所需的协议子命令，覆盖它会破坏 daemon ↔ Dim 的通信契约；`--auth-setup`
// 与 `--remote` 会把 CLI 切进永远不会启动 ACP 服务端的模式（交互登录 / 已移除的远端流程）。
var dimBlockedArgs = map[string]blockedArgMode{
	"acp":          blockedStandalone,
	"--auth-setup": blockedStandalone,
	"--remote":     blockedStandalone,
	"--help":       blockedStandalone,
	"-h":           blockedStandalone,
}

// dimBackend 通过拉起 `dim acp`、经 stdin/stdout 上的标准 ACP（Agent Client Protocol）
// JSON-RPC 2.0 通信来实现 Backend。
//
// Dim（dimcode）通过 `dim acp` 子命令把它的 agent 循环暴露在 ACP 上。协议面与共享的
// hermesClient ACP 传输一致（Hermes/Kimi/Kiro/Traecli/QwenPaw 都在用）——只有二进制名、
// 会话引导方式和工具名提取不同。
//
// 与 Dim ACP 服务端约定的要点：
//   - `initialize` 不宣传任何 authMethods，所以无需 `authenticate` 步骤；CLI 直接用用户
//     既有的 Dim OAuth 登录态。
//   - `session/new` 要求绝对 cwd（相对路径会被拒）。
//   - Dim 的 ACP 服务端在创建会话时硬编码一套只读权限预设，会静默拒绝一切文件写与进程
//     拉起。因此后端在 session/new 之后立刻发 `session/set_config_option`（permission →
//     full-access、mode → agent），让 Multica agent 能干实事。续接的会话经 session/load
//     保留这些设置，所以 set_config_option 在新旧会话上都要重放（幂等）。模型覆盖走标准的
//     `session/set_model` RPC；模型目录由 `session/new` 以 models.availableModels /
//     configOptions 宣传。
//   - `session/load` 跨进程续接既有会话：dim 0.3.10+ 在持有进程退出后约 5 秒内释放其每进程
//     会话锁，因此新的 `dim acp` 进程里的后续运行可以接着聊。更早的 0.3.x 会把会话永久
//     绑死在创建它的进程上；那些版本上 load 会失败，靠 ResumeRejected 让 daemon 用新会话
//     重试。
//   - 工具名本身已是小写（"write"、"read"、"exec"）；共享的 kimiToolNameFromTitle 归一化
//     让 UI 标识符保持稳定。
type dimBackend struct {
	cfg Config
}

var (
	// dimReaderDrainGrace 限制轮次在 session/prompt 响应之后还等多久「尾部的 ACP 通知」。
	dimReaderDrainGrace      = 2 * time.Second
	dimNotificationQuietTime = 250 * time.Millisecond
	// dimProcessWaitTimeout 限制 deferred 清理在 cancel+关 stdin 之后等子进程退出的时间，
	// 超时即强杀。一个对两者都不理的子进程不应该能挂住 Result 的投递。
	dimProcessWaitTimeout = 5 * time.Second
	// dimSessionCloseTimeout 限制拆进程前那次尽力而为的 session/close 的耗时。刻意设得很
	// 短，一个卡住的 close 不能拖迟 Result 投递；下一次运行的 session/load 仍然安全，因为
	// dim 会在大约 5 秒后自行释放锁。
	dimSessionCloseTimeout = 2 * time.Second
	// dimSessionLoadRetryAttempts 限制 session/load 报「被另一个进程持有」时的重试次数——那
	// 意味着上一个进程的锁还没释放。有了优雅的 session/close 这很少见（锁会立即释放），但
	// 被强杀的进程可能要等约 5 秒。重试循环覆盖这段窗口，而不是静默新建会话丢掉对话。
	dimSessionLoadRetryAttempts = 3
	dimSessionLoadRetryDelay    = 2 * time.Second
)

// dimMinVersion 是支持经 session/load 跨运行续接会话的最低 dim 版本。更早的构建会把会话
// 永久绑死在创建它的进程上。
const dimMinVersion = "0.3.10"

// extractACPAgentVersion 从 initialize 响应里取 agentInfo.version。字段缺失（例如不上报它的
// 老版本运行时）时返回 ""。
func extractACPAgentVersion(result json.RawMessage) string {
	var resp struct {
		AgentInfo struct {
			Version string `json:"version"`
		} `json:"agentInfo"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		return ""
	}
	return resp.AgentInfo.Version
}

// dimVersionSupported 判断 dim 版本是否达到跨运行会话续接的最低要求。用类 semver 的
// 比较（major.minor.patch）。Fail-closed：空或解析不了的版本直接判不支持（返回 false），
// 而不是假设它支持——一个证明不了自己 >= 0.3.10 的运行时不允许走会话续接（review #3）。
func dimVersionSupported(version string) bool {
	version = strings.TrimPrefix(version, "v")
	parts := strings.Split(version, ".")
	if len(parts) < 3 {
		return false // 解析不了——fail-closed，不假设支持
	}
	var maj, min, pat int
	if _, err := fmt.Sscanf(parts[0], "%d", &maj); err != nil {
		return false
	}
	if _, err := fmt.Sscanf(parts[1], "%d", &min); err != nil {
		return false
	}
	// patch 可能带后缀（如 "10-beta"）；Sscanf 在首个非数字处停下，这正是我们要的。
	if _, err := fmt.Sscanf(parts[2], "%d", &pat); err != nil {
		return false
	}
	var reqMaj, reqMin, reqPat int
	fmt.Sscanf(dimMinVersion, "%d.%d.%d", &reqMaj, &reqMin, &reqPat)
	if maj != reqMaj {
		return maj > reqMaj
	}
	if min != reqMin {
		return min > reqMin
	}
	return pat >= reqPat
}

// dimMessageStream 把消息发送与最终的 close 串行化，防止迟到的 stdout 读取方向已关闭的
// 通道发送。与 grok/traecli/qoder 的做法一致。
type dimMessageStream struct {
	ch     chan Message
	mu     sync.Mutex
	closed bool
}

// newDimMessageStream 构造带容量 size 的消息流。
func newDimMessageStream(size int) *dimMessageStream {
	return &dimMessageStream{ch: make(chan Message, size)}
}

// send 尝试发送一条消息；流已关闭则静默丢弃（幂等）。
func (s *dimMessageStream) send(msg Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	trySend(s.ch, msg)
}

// close 关闭底层通道；重复调用安全。
func (s *dimMessageStream) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	close(s.ch)
}

// Execute 驱动一次完整的 Dim ACP 会话并返回其产出。
//
// 流程：定位可执行文件、把 mcp_config 翻译成 ACP 数组形态 → 拉起 `dim acp` 子进程 → 后台读
// stdout（每行一个 JSON-RPC 消息），主 goroutine 按 initialize →（版本门槛校验）→
// session/new | session/load（带锁重试）→ set_config_option（permission/mode 提升）→ 可选
// set_model / effort → session/prompt → 等尾部通知静默 → session/close → 合并 stderr 失败
// 信号与用量 → 生成最终 Result。退出清理固定顺序：先对整组 SIGKILL → cancel → 关 stdin →
// 有界等待子进程消失。
func (b *dimBackend) Execute(ctx context.Context, prompt string, opts ExecOptions) (*Session, error) {
	execPath := b.cfg.ExecutablePath
	if execPath == "" {
		execPath = "dim"
	}
	if _, err := exec.LookPath(execPath); err != nil {
		return nil, fmt.Errorf("dim executable not found at %q: %w", execPath, err)
	}

	// 把 agent 的 mcp_config（Claude 风格的对象套对象）翻译成 ACP session/new 与
	// session/load 期望的数组形态。畸形 JSON 要直接失败（fail closed），让启动把真实错误
	// 暴露出来，而不是静默丢掉每一个 MCP 服务器。
	mcpServers, err := buildACPMcpServers(opts.McpConfig, b.cfg.Logger)
	if err != nil {
		return nil, fmt.Errorf("dim: invalid mcp_config: %w", err)
	}

	timeout := opts.Timeout
	runCtx, cancel := runContext(ctx, timeout)

	dimArgs := append([]string{"acp"}, filterCustomArgs(opts.CustomArgs, dimBlockedArgs, b.cfg.Logger)...)

	cmd := b.cfg.commandAt(execPath).exec(runCtx, dimArgs...)
	hideAgentWindow(cmd)
	// 接管 context 取消：默认行为会在 runCtx 结束瞬间杀掉整个进程组，让人没有机会先把 ACP
	// 会话关掉。这里改为由下面的 deferred 清理统一驱动对整组的 SIGKILL。把 Cancel 设为 no-op
	// 返回 nil，让 os/exec 不会用自带的 kill 来和我们抢。
	cmd.Cancel = func() error { return nil }
	cmd.WaitDelay = 10 * time.Second
	b.cfg.logAgentCommand(cmd, newAgentCommandLogArgs(dimArgs, trustAgentCommandPositional(0, "acp")))
	if opts.Cwd != "" {
		cmd.Dir = opts.Cwd
	}
	cmd.Env = buildEnv(b.cfg.Env)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("dim stdout pipe: %w", err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("dim stdin pipe: %w", err)
	}
	// 用 StderrPipe 加显式 copier 是为了拿到一个 join 点（stderrDone），它保证在「把失败
	// 提升到结果」的决策之前已经排干；为什么 io.MultiWriter 的形式在负载下会和
	// stopReason=end_turn 竞态，见 hermes.go 里的对应注释。
	providerErr := newACPProviderErrorSniffer("dim")
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("dim stderr pipe: %w", err)
	}

	if err := startOwnedProcessTree(cmd, b.cfg.Logger); err != nil {
		cancel()
		return nil, fmt.Errorf("start dim: %w", err)
	}

	stderrSink := io.MultiWriter(newLogWriter(b.cfg.Logger, "[dim:stderr] "), providerErr)
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		_, _ = io.Copy(stderrSink, stderr)
	}()

	b.cfg.Logger.Info("dim acp started", "pid", cmd.Process.Pid, "cwd", opts.Cwd)

	msgStream := newDimMessageStream(256)
	resCh := make(chan Result, 1)

	// Dim 把中途叙述与最终答案都作为同一种 agent_message_chunk 类型流出；tracker 只保留
	// 「最后一次工具调用之后」的那段作为 Result.Output，同时保留全文供错误检测用。
	var deliverable acpDeliverableTracker

	promptDone := make(chan hermesPromptResult, 1)
	activity := make(chan struct{}, 1)

	c := &hermesClient{
		cfg:          b.cfg,
		stdin:        stdin,
		pending:      make(map[int]*pendingRPC),
		pendingTools: make(map[string]*pendingToolCall),
		onActivity: func() {
			select {
			case activity <- struct{}{}:
			default:
			}
		},
		onMessage: func(msg Message) {
			if msg.Type == MessageToolUse {
				// 像 kimi/traecli 那样把工具标题再归一化一轮，让 UI 看到一致的 snake_case
				// 名字（"write" → "write_file"）。
				msg.Tool = kimiToolNameFromTitle(msg.Tool)
			}
			deliverable.observe(msg)
			msgStream.send(msg)
		},
		onPromptDone: func(result hermesPromptResult) {
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
		c.closeAllPending(fmt.Errorf("dim process exited"))
	}()

	go func() {
		defer cancel()
		defer msgStream.close()
		defer close(resCh)
		// 清理顺序很关键：先 cancel 运行 context，让 exec.CommandContext 在子进程仍活着时
		// 杀掉它，然后再关 stdin、等待。旧顺序（stdin.Close → cmd.Wait → cancel）在子进程
		// 无视 stdin EOF、永不退出时会够不到 cancel()，cmd.Wait() 无限挂起，resCh 永远不关。
		// cmd.Wait 有界，所以仍卡死的子进程也拖不住最终的 Result。
		defer func() {
			// 先对整棵进程组（agent CLI 加它派生的工具子进程）发信号，再关 stdin。exec.
			// CommandContext 的 cancel 已被禁用（cmd.Cancel 返回 nil），所以全部发信号由我们
			// 负责。若先关 stdin，shell 会因 EOF 退出、进程组在我们 SIGKILL 后代之前就解散，
			// 后代成了 PPID=1 的孤儿。
			signalProcessGroup(cmd, syscall.SIGKILL)
			cancel()
			stdin.Close()
			waitDone := make(chan struct{})
			go func() { _ = cmd.Wait(); close(waitDone) }()
			select {
			case <-waitDone:
			case <-time.After(dimProcessWaitTimeout):
			}
			waitProcessGroupGone(cmd, dimProcessWaitTimeout)
			// 释放 Windows Job Object 的所有权句柄（Unix 上是 no-op），让操作系统能收尾任何
			// 挂在它上面的进程。
			releaseProcessGroup(cmd)
		}()

		startTime := time.Now()
		finalStatus := "completed"
		var finalError string
		var sessionID string
		// 在「ACP 运行时拒绝了我们要求续接的会话」时置位。只有这种情况能靠新建会话治愈，
		// 所以下面遇到的握手/网络类失败必须保持它为 false。
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
			finalError = fmt.Sprintf("dim initialize failed: %v", err)
			resCh <- Result{Status: finalStatus, Error: finalError, DurationMs: time.Since(startTime).Milliseconds()}
			return
		}

		// 要求 dim >= 0.3.10（fail-closed）：更早的构建会把会话永久绑死在创建进程上，
		// session/load 会以 "held by another process" 失败、后续运行丢失上下文。0.3.10 在
		// 优雅退出（session/close）或约 5 秒后释放锁。空版本或解析不了的版本一律判不支持，
		// 不让一个证明不了自己 >= 0.3.10 的运行时走会话续接（review #3）。
		dimVersion := extractACPAgentVersion(initResult)
		if !dimVersionSupported(dimVersion) {
			finalStatus = "failed"
			if dimVersion == "" {
				finalError = "dim did not report an agent version: cross-run session resume requires dim >= 0.3.10 (0.3.10+ releases the per-process session lock on exit); please upgrade dimcode"
			} else {
				finalError = fmt.Sprintf("dim %s is too old: cross-run session resume requires dim >= 0.3.10 (0.3.10+ releases the per-process session lock on exit); please upgrade dimcode", dimVersion)
			}
			resCh <- Result{Status: finalStatus, Error: finalError, DurationMs: time.Since(startTime).Milliseconds()}
			return
		}

		// 丢弃运行时未宣传其远程传输方式的 MCP 条目。不支持的传输会拖垮整个 session/new，
		// 原因见 hermes.go。
		mcpServers = filterACPMcpServersByCapability(mcpServers, extractACPMcpCapabilities(initResult), "dim", b.cfg)

		cwd := opts.Cwd
		if cwd == "" {
			cwd = "."
		}

		// Dim（dimcode 0.3.10+）在持有进程退出后约 5 秒内释放每进程会话锁（优雅 close 或
		// SIGTERM 都算），所以新的 `dim acp` 进程里的后续运行可以经标准 ACP `session/load`
		// 续接既有会话。更早的 0.3.x 会把会话永久绑死在创建进程上；那些版本上 load 会以
		// "held by another process" 失败，靠 ResumeRejected 让 daemon 在新会话上重试。
		//
		// 续接的会话会保留它的 permission/mode 配置（已验证：full-access 的会话 load 后仍是
		// full-access），但为了防「一个只配到一半的会话被续接」，set_config_option 在新旧
		// 会话上都会重放（review #4）。
		var freshSession bool
		var sessionResult json.RawMessage
		if opts.ResumeSessionID != "" {
			// 上一个进程的锁还没释放（"held by another process"）时重试 session/load。优雅
			// 的 session/close 会让锁立即释放，但被强杀的进程可能要约 5 秒。重试而不是静默
			// 新建会话丢掉对话（review #1）。
			var result json.RawMessage
			var loadErr error
		loadRetry:
			for attempt := 0; attempt <= dimSessionLoadRetryAttempts; attempt++ {
				result, loadErr = c.request(runCtx, "session/load", map[string]any{
					"cwd":        cwd,
					"sessionId":  opts.ResumeSessionID,
					"mcpServers": mcpServers,
				})
				if loadErr == nil {
					break
				}
				if isACPSessionNotFound(loadErr) {
					break // 会话已消失——没有重试的意义
				}
				if !isACPHeldByProcess(loadErr) {
					break // 其它错误——交给常规失败路径处理
				}
				if attempt < dimSessionLoadRetryAttempts {
					b.cfg.Logger.Warn("dim session/load: lock not yet released, retrying",
						"backend", "dim",
						"attempt", attempt+1,
						"delay", dimSessionLoadRetryDelay.String(),
					)
					select {
					case <-time.After(dimSessionLoadRetryDelay):
					case <-runCtx.Done():
						loadErr = fmt.Errorf("dim session/load cancelled: %w", runCtx.Err())
						break loadRetry
					}
				}
			}
			err := loadErr
			if err != nil {
				if isACPSessionNotFound(err) {
					b.cfg.Logger.Warn("dim resumed session not found; the daemon will retry fresh",
						"backend", "dim",
						"requested_session", opts.ResumeSessionID,
					)
					resumeRejected = true
					resCh <- Result{Status: "failed", Error: fmt.Sprintf("dim session/load: %v", err), DurationMs: time.Since(startTime).Milliseconds(), ResumeRejected: resumeRejected}
					return
				}
				if runCtx.Err() == context.DeadlineExceeded {
					finalStatus = "timeout"
					finalError = fmt.Sprintf("dim timed out during session/load: %v", timeout)
				} else if runCtx.Err() == context.Canceled {
					finalStatus = "aborted"
					finalError = fmt.Sprintf("dim aborted: %v", err)
				} else {
					finalStatus = "failed"
					finalError = fmt.Sprintf("dim session/load failed: %v", err)
				}
				resCh <- Result{Status: finalStatus, Error: finalError, DurationMs: time.Since(startTime).Milliseconds(), ResumeRejected: resumeRejected}
				return
			}
			var changed bool
			sessionID, changed = resolveResumedSessionID(opts.ResumeSessionID, result)
			sessionResult = result
			if changed {
				b.cfg.Logger.Warn("dim returned a different session id on resume — original was likely lost; continuing with the new id",
					"backend", "dim",
					"requested", opts.ResumeSessionID,
					"actual", sessionID,
				)
			}
			if effectiveModel == "" {
				effectiveModel = extractACPCurrentModelID(result)
			}
		} else {
			freshSession = true
			result, err := c.request(runCtx, "session/new", map[string]any{
				"cwd":        cwd,
				"mcpServers": mcpServers,
			})
			if err != nil {
				if runCtx.Err() == context.DeadlineExceeded {
					finalStatus = "timeout"
					finalError = fmt.Sprintf("dim timed out during session/new: %v", timeout)
				} else if runCtx.Err() == context.Canceled {
					finalStatus = "aborted"
					finalError = fmt.Sprintf("dim aborted: %v", err)
				} else {
					finalStatus = "failed"
					finalError = fmt.Sprintf("dim session/new failed: %v", err)
				}
				resCh <- Result{Status: finalStatus, Error: finalError, DurationMs: time.Since(startTime).Milliseconds(), ResumeRejected: resumeRejected}
				return
			}
			sessionID = extractACPSessionID(result)
			sessionResult = result
			if sessionID == "" {
				finalStatus = "failed"
				finalError = "dim session/new returned no session ID"
				resCh <- Result{Status: finalStatus, Error: finalError, DurationMs: time.Since(startTime).Milliseconds(), ResumeRejected: resumeRejected}
				return
			}
			if effectiveModel == "" {
				effectiveModel = extractACPCurrentModelID(result)
			}
		}

		c.sessionID = sessionID
		// 尽早把会话 id 透出给上层，被取消的运行也能保住续接指针。
		msgStream.send(Message{Type: MessageStatus, Status: "running", SessionID: sessionID})
		b.cfg.Logger.Info("dim session ready", "session_id", sessionID, "resumed", !freshSession)

		// Dim 的 ACP 服务端在创建会话时硬编码只读权限预设。这里提升为 full-access 并把模式
		// 固定为 agent。新建与续接会话都要跑（review #4）：一个首次运行中途配崩的会话，会把
		// 半配置状态带进续接。set_config_option 幂等。失败时关闭会话并中止。
		for _, cfgOpt := range []struct {
			id    string
			value string
		}{
			{"permission", "full-access"},
			{"mode", "agent"},
		} {
			if _, err := c.request(runCtx, "session/set_config_option", map[string]any{
				"sessionId": sessionID,
				"configId":  cfgOpt.id,
				"value":     cfgOpt.value,
			}); err != nil {
				finalStatus = "failed"
				finalError = fmt.Sprintf("dim could not set session config %s=%s: %v", cfgOpt.id, cfgOpt.value, err)
				// 尽力关掉会话，别让一个只配了一半的会话留给下次续接继承。
				closeCtx, closeCancel := context.WithTimeout(context.Background(), dimSessionCloseTimeout)
				_, _ = c.request(closeCtx, "session/close", map[string]any{"sessionId": sessionID})
				closeCancel()
				resCh <- Result{Status: finalStatus, Error: finalError, DurationMs: time.Since(startTime).Milliseconds(), SessionID: sessionID, ResumeRejected: resumeRejected}
				return
			}
			b.cfg.Logger.Info("dim session config set", "config", cfgOpt.id, "value", cfgOpt.value, "session_id", sessionID)
		}

		if opts.Model != "" {
			if _, err := c.request(runCtx, "session/set_model", map[string]any{
				"sessionId": sessionID,
				"modelId":   opts.Model,
			}); err != nil {
				b.cfg.Logger.Warn("dim set_session_model failed", "error", err, "requested_model", opts.Model)
				finalStatus = "failed"
				finalError = fmt.Sprintf("dim could not switch to model %q: %v", opts.Model, err)
				// 关闭会话，别让一个只配了一半的会话（permission/mode 已设、模型没设成）
				// 留给下次续接继承。
				closeCtx, closeCancel := context.WithTimeout(context.Background(), dimSessionCloseTimeout)
				_, _ = c.request(closeCtx, "session/close", map[string]any{"sessionId": sessionID})
				closeCancel()
				resCh <- Result{
					Status:         finalStatus,
					Error:          finalError,
					DurationMs:     time.Since(startTime).Milliseconds(),
					SessionID:      sessionID,
					ResumeRejected: resumeRejected,
				}
				return
			}
			b.cfg.Logger.Info("dim session model set", "model", opts.Model)
		}

		// 通过 ACP 会话配置应用思考级别（若有）。Dim 在 session/new 里宣传 `thought_level`
		// 这个 configOption（auto/high/max），所以它进了 acpCatalogThinkingProviders 名单。
		// effort 选项可能依赖当前模型，所以放在 set_model 之后应用。
		applyACPEffortOption(runCtx, c.request, "dim", b.cfg.Logger,
			sessionID, sessionResult, opts.ThinkingLevel, opts.Model == "")

		userText := prompt
		if opts.SystemPrompt != "" {
			userText = opts.SystemPrompt + "\n\n---\n\n" + prompt
		}

		_, err = c.request(runCtx, "session/prompt", map[string]any{
			"sessionId": sessionID,
			"prompt": []map[string]any{
				{"type": "text", "text": userText},
			},
		})
		if err != nil {
			if runCtx.Err() == context.DeadlineExceeded {
				finalStatus = "timeout"
				finalError = fmt.Sprintf("dim timed out after %s", timeout)
			} else if runCtx.Err() == context.Canceled {
				finalStatus = "aborted"
				finalError = "execution cancelled"
			} else {
				finalStatus = "failed"
				finalError = fmt.Sprintf("dim session/prompt failed: %v", err)
			}
		} else {
			// 非阻塞地看看响应解析是否已给出 promptDone（响应是在 c.request 里同步解析的，
			// 所以 promptDone 通常已经就绪）。
			select {
			case pr := <-promptDone:
				if pr.stopReason == "cancelled" {
					finalStatus = "aborted"
					finalError = "dim cancelled the prompt"
				}
				if effectiveModel == "" {
					effectiveModel = pr.modelID
				}
				c.mergeUsage(pr.usage)
			default:
			}
			// 给 stdout 读取方一段有界时间消费 dim 在 session/prompt 返回后可能紧接着发出的
			// 通知（agent_message_chunk、用量更新）。否则在响应边界就关 stdin 会和读取方
			// 竞态、截断末尾文本——这正是 hermes 修过的同一个竞态
			// （TestHermesBackendDrainsLateFinalNotification）。
			waitForACPNotificationQuiescence(runCtx, activity, readerDone, dimNotificationQuietTime, dimReaderDrainGrace)
		}

		duration := time.Since(startTime)
		b.cfg.Logger.Info("dim finished", "pid", cmd.Process.Pid, "status", finalStatus, "duration", duration.Round(time.Millisecond).String())

		// 拆进程前先尽力做一次 session/close：让 dim 及时释放每进程会话锁（下一次运行可以
		// 立即续接），而不是干等 5 秒的空闲释放超时。有界且尽力而为——没发出去只是让下次
		// 运行等锁最多 5 秒。
		if sessionID != "" {
			closeCtx, closeCancel := context.WithTimeout(context.Background(), dimSessionCloseTimeout)
			_, _ = c.request(closeCtx, "session/close", map[string]any{"sessionId": sessionID})
			closeCancel()
		}

		stdin.Close()
		cancel()

		// Dim 的 ACP 服务端在 session/prompt 返回后可能仍让进程——以及 stdout/stderr
		// 管道——短暂保持打开。给排干设一个上界。
		drainCtx, drainCancel := context.WithTimeout(context.Background(), dimReaderDrainGrace)
		select {
		case <-readerDone:
		case <-drainCtx.Done():
		}
		select {
		case <-stderrDone:
		case <-drainCtx.Done():
		}
		drainCancel()

		finalOutput, providerErrorOutput := deliverable.result()

		// 当 stderr 或 agent 文本流展现出「上游 LLM 终态失败」（鉴权 / 限流 / HTTP 4xx）时，
		// 把 completed 提升为 failed。这里读的是完整文本流而不是 deliverable，所以「在工具
		// 调用前就放弃」的一轮仍然可见。
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
