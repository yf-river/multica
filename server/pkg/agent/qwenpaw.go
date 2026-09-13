package agent

// 文件职责：QwenPaw 的 ACP v1 Backend 实现。
//
// 运行方式：以 `qwenpaw acp` 启动子进程，经标准 ACP JSON-RPC 2.0（stdio）通信。
// QwenPaw 的 ACP 服务器与 Hermes/Kimi/Kiro/Traecli 用同一套协议，因此直接复用
// hermesClient 传输层，只差二进制名、环境与工具名提取。模型选择不受支持（调用
// session/set_model 会把设置写到用户共享的 agent.json，故干脆不调），用量一律记到
// "unknown"。v2.0.1 起支持在 session/new、session/load 里传 _meta 键
// qwenpaw.coding_project_dir 以启用 Coding Mode。
//
// 支持的最低版本：v2.0.1（当前稳定版），走 v2.0 ACP API；v1.x 说不同协议，不支持。
// 主要入口：qwenpawBackend.Execute。

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// qwenpawBlockedArgs 是 daemon 写死、不允许 custom_args 覆盖的参数：
// `acp` 是驱动 ACP JSON-RPC 传输的协议子命令，覆盖它会破坏 daemon↔QwenPaw 的
// 通信契约；`--workspace` 由 daemon 按任务设置，用于技能隔离。
var qwenpawBlockedArgs = map[string]blockedArgMode{
	"acp":         blockedStandalone,
	"--workspace": blockedWithValue,
}

// qwenpawBackend 通过派生 `qwenpaw acp` 实现 Backend，并经 stdin/stdout 上的 ACP
// （Agent Client Protocol）JSON-RPC 2.0 通信。
//
// QwenPaw 的 ACP 服务器（`qwenpaw acp`）支持与 Hermes/Kimi/Kiro/Traecli 相同的
// 协议，所以复用 hermesClient ACP 传输——只有二进制、环境与工具名提取不同。
//
// 支持的最低 QwenPaw 版本：v2.0.1（当前稳定版）。下面执行路径依赖的每个 ACP 特性
// 它都具备；集成检查见 TestQwenpawRealACPSmoke。代码面向 v2.0 ACP API 表面——
// v1.x 说的是另一套协议，不支持。
//
// 与 QwenPaw v2.0.1 的几个显著约定：
//   - `session/new` 与 `session/load` 接受 `_meta["qwenpaw.coding_project_dir"]`
//     来启用 Coding Mode（qwenpaw.coding_project_dir meta 键）。
//   - 不调用 `session/set_model`：它会以 agent 作用域（而非会话作用域）写进
//     agent.json，调用会改动用户共享的 agent 配置。因此模型覆盖被声明为不支持
//     （见 ModelSelectionSupported），ListModels 直接返回空目录、不起探测子进程。
//     QwenPaw 在 v2.1.0-beta.1 里确实给 session/new 加了 `models` 字段
//     （agentscope-ai/QwenPaw#6531），但选模型都不支持时，那份目录没有消费者。
//   - 技能（Skills）从工作区的 `skills` 目录发现（不是 `skill_pool`）——
//     见 execenv/qwenpaw_workspace.go。
type qwenpawBackend struct {
	cfg Config
}

// Execute 实现 Backend.Execute：以 `qwenpaw acp` 驱动 QwenPaw 完成一次 prompt。
func (b *qwenpawBackend) Execute(ctx context.Context, prompt string, opts ExecOptions) (*Session, error) {
	execPath := b.cfg.ExecutablePath
	if execPath == "" {
		execPath = "qwenpaw"
	}
	if _, err := exec.LookPath(execPath); err != nil {
		return nil, fmt.Errorf("qwenpaw executable not found at %q: %w", execPath, err)
	}

	mcpServers, err := buildACPMcpServers(opts.McpConfig, b.cfg.Logger)
	if err != nil {
		return nil, fmt.Errorf("qwenpaw: invalid mcp_config: %w", err)
	}

	timeout := opts.Timeout
	runCtx, cancel := runContext(ctx, timeout)

	// `qwenpaw acp` 在 stdio 上跑 ACP agent 主循环。权限放行由 daemon 在
	// hermesClient.handleAgentRequest 里完成——对每个 session/request_permission
	// 请求挑一个安全放行项。
	//
	// ExtraArgs（MULTICA_QWENPAW_ARGS，daemon 级）在前，CustomArgs（按 agent）
	// 在后，与文档声明的优先级及其它同时接受两者的 backend 一致。
	qwenpawArgs := []string{"acp"}
	qwenpawArgs = append(qwenpawArgs, filterCustomArgs(opts.ExtraArgs, qwenpawBlockedArgs, b.cfg.Logger)...)
	qwenpawArgs = append(qwenpawArgs, filterCustomArgs(opts.CustomArgs, qwenpawBlockedArgs, b.cfg.Logger)...)

	// 注入 --workspace 做按任务隔离。它已在 qwenpawBlockedArgs 里被禁，用户自定义
	// custom_args 覆盖不了它；值由 daemon 经 opts.QwenpawWorkspace 传入。
	if opts.QwenpawWorkspace != "" {
		qwenpawArgs = append(qwenpawArgs, "--workspace", opts.QwenpawWorkspace)
	}

	cmd := b.cfg.commandAt(execPath).exec(runCtx, qwenpawArgs...)
	hideAgentWindow(cmd)
	b.cfg.logAgentCommand(cmd, newAgentCommandLogArgs(qwenpawArgs, trustAgentCommandPositional(0, "acp")))
	if opts.Cwd != "" {
		cmd.Dir = opts.Cwd
	}
	cmd.Env = buildEnv(b.cfg.Env)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("qwenpaw stdout pipe: %w", err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("qwenpaw stdin pipe: %w", err)
	}

	providerErr := newACPProviderErrorSniffer("qwenpaw")
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("qwenpaw stderr pipe: %w", err)
	}

	if err := startOwnedProcessTree(cmd, b.cfg.Logger); err != nil {
		cancel()
		return nil, fmt.Errorf("start qwenpaw: %w", err)
	}

	// stderr 同时进日志与错误嗅探器。
	stderrSink := io.MultiWriter(newLogWriter(b.cfg.Logger, "[qwenpaw:stderr] "), providerErr)
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		_, _ = io.Copy(stderrSink, stderr)
	}()

	b.cfg.Logger.Info("qwenpaw acp started", "pid", cmd.Process.Pid, "cwd", opts.Cwd)

	msgCh := make(chan Message, 256)
	resCh := make(chan Result, 1)

	// 汇总所有文本消息作为 Result.Output（QwenPaw 的最终回答与旁白同构，简单拼接）。
	var outputMu sync.Mutex
	var output strings.Builder

	promptDone := make(chan hermesPromptResult, 1)

	c := &hermesClient{
		cfg:          b.cfg,
		stdin:        stdin,
		pending:      make(map[int]*pendingRPC),
		pendingTools: make(map[string]*pendingToolCall),
		onMessage: func(msg Message) {
			// hermesClient 用 hermesToolNameFromTitle 规整 ACP 工具名；QwenPaw 的
			// 工具名已是标准形态，无需再归一化。
			if msg.Type == MessageText {
				outputMu.Lock()
				output.WriteString(msg.Content)
				outputMu.Unlock()
			}
			trySend(msgCh, msg)
		},
		onPromptDone: func(result hermesPromptResult) {
			select {
			case promptDone <- result:
			default:
			}
		},
	}

	// stdout 读取协程：逐行喂给客户端；进程退出后收掉挂起 RPC。
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
		c.closeAllPending(fmt.Errorf("qwenpaw process exited"))
	}()

	// 主流程协程：握手 → 建会话/续跑 → prompt → 收尾。
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
		var resumeRejected bool

		// 1. initialize 握手。
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
			finalError = fmt.Sprintf("qwenpaw initialize failed: %v", err)
			resCh <- Result{Status: finalStatus, Error: finalError, DurationMs: time.Since(startTime).Milliseconds()}
			return
		}

		// 只保留运行时宣称支持的 MCP 传输形态。
		mcpServers = filterACPMcpServersByCapability(mcpServers, extractACPMcpCapabilities(initResult), "qwenpaw", b.cfg)

		// 2. 续跑或新建会话。cwd 非 "." 时附带 Coding Mode 的 _meta 键。
		cwd := opts.Cwd
		if cwd == "" {
			cwd = "."
		}

		if opts.ResumeSessionID != "" {
			loadParams := map[string]any{
				"cwd":        cwd,
				"sessionId":  opts.ResumeSessionID,
				"mcpServers": mcpServers,
			}
			if cwd != "." {
				loadParams["_meta"] = map[string]any{
					"qwenpaw.coding_project_dir": cwd,
				}
			}
			result, err := c.request(runCtx, "session/load", loadParams)
			if err != nil {
				finalStatus = "failed"
				finalError = fmt.Sprintf("qwenpaw session/load failed: %v", err)
				// 会话不存在 = 续跑对象已死，标记 rejected 供 daemon 决定兜底。
				if isACPSessionNotFound(err) {
					sessionID = ""
					resumeRejected = true
				}
				resCh <- Result{Status: finalStatus, Error: finalError, DurationMs: time.Since(startTime).Milliseconds(), ResumeRejected: resumeRejected}
				return
			}
			var changed bool
			sessionID, changed = resolveResumedSessionID(opts.ResumeSessionID, result)
			if changed {
				b.cfg.Logger.Warn("agent returned a different session id on resume — original was likely lost; continuing with the new id",
					"backend", "qwenpaw",
					"requested", opts.ResumeSessionID,
					"actual", sessionID,
				)
			}
		} else {
			newParams := map[string]any{
				"cwd":        cwd,
				"mcpServers": mcpServers,
			}
			if cwd != "." {
				newParams["_meta"] = map[string]any{
					"qwenpaw.coding_project_dir": cwd,
				}
			}
			result, err := c.request(runCtx, "session/new", newParams)
			if err != nil {
				// 按 runCtx 状态细分：超时/取消/普通失败，让上层能区分对待。
				if runCtx.Err() == context.DeadlineExceeded {
					finalStatus = "timeout"
					finalError = fmt.Sprintf("qwenpaw timed out during session/new: %v", timeout)
				} else if runCtx.Err() == context.Canceled {
					finalStatus = "aborted"
					finalError = fmt.Sprintf("qwenpaw aborted: %v", err)
				} else {
					finalStatus = "failed"
					finalError = fmt.Sprintf("qwenpaw session/new failed: %v", err)
				}
				resCh <- Result{Status: finalStatus, Error: finalError, DurationMs: time.Since(startTime).Milliseconds()}
				return
			}
			sessionID = extractACPSessionID(result)
			if sessionID == "" {
				finalStatus = "failed"
				finalError = "qwenpaw session/new returned no session ID"
				resCh <- Result{Status: finalStatus, Error: finalError, DurationMs: time.Since(startTime).Milliseconds()}
				return
			}
		}

		if sessionID == "" {
			// 没有会话就无从继续（含续跑被拒后清空 id 的情况）。
			finalStatus = "failed"
			finalError = "qwenpaw session ID is empty"
			resCh <- Result{Status: finalStatus, Error: finalError, DurationMs: time.Since(startTime).Milliseconds(), ResumeRejected: resumeRejected}
			return
		}

		c.sessionID = sessionID
		b.cfg.Logger.Info("qwenpaw session created", "session_id", sessionID)

		// 3. 组装 prompt：有系统简报时拼在前、用分隔线隔开。
		userText := prompt
		if opts.SystemPrompt != "" {
			userText = opts.SystemPrompt + "\n\n---\n\n" + prompt
		}

		// 4. 发 prompt 并等 PromptResponse。
		_, err = c.request(runCtx, "session/prompt", map[string]any{
			"sessionId": sessionID,
			"prompt": []map[string]any{
				{"type": "text", "text": userText},
			},
		})
		if err != nil {
			if runCtx.Err() == context.DeadlineExceeded {
				finalStatus = "timeout"
				finalError = fmt.Sprintf("qwenpaw timed out after %s", timeout)
			} else if runCtx.Err() == context.Canceled {
				finalStatus = "aborted"
				finalError = "execution cancelled"
			} else {
				finalStatus = "failed"
				finalError = fmt.Sprintf("qwenpaw session/prompt failed: %v", err)
				// 续跑时 prompt 阶段才暴露会话已死：清空 id + 标记 rejected。
				if opts.ResumeSessionID != "" && isACPSessionNotFound(err) {
					sessionID = ""
					resumeRejected = true
				}
			}
		} else {
			select {
			case pr := <-promptDone:
				if pr.stopReason == "cancelled" {
					duration := time.Since(startTime)
					b.cfg.Logger.Info("qwenpaw prompt cancelled", "stopReason", pr.stopReason, "duration", duration.Round(time.Millisecond).String())
				}
				c.mergeUsage(pr.usage)
			default:
			}
		}

		duration := time.Since(startTime)
		b.cfg.Logger.Info("qwenpaw finished", "pid", cmd.Process.Pid, "status", finalStatus, "duration", duration.Round(time.Millisecond).String())

		stdin.Close()
		cancel()

		<-readerDone
		<-stderrDone

		outputMu.Lock()
		finalOutput := output.String()
		outputMu.Unlock()

		// stderr/文本流里出现终局 provider 错误时把 completed 升级为 failed。
		finalStatus, finalError = promoteACPResultOnProviderError(finalStatus, finalError, finalOutput, providerErr)

		u := c.accumulatedUsage()

		var usageMap map[string]TokenUsage
		if acpUsagePresent(u) {
			// QwenPaw 不支持模型选择——backend 从不把 opts.Model 发给 agent。
			// 用量固定记到 "unknown"，而不是一个从未生效的模型名下。
			usageMap = map[string]TokenUsage{"unknown": u}
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
