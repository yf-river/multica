package agent

// 文件职责：本文件把 DevEco Code 实现成一个「完全自包含、独立」的 Backend agent。
// DevEco Code（`deveco` CLI，华为鸿蒙的编程 agent CLI，
// https://gitcode.com/openharmony-sig/deveco-code）是另一家公司维护的独立产品。它刻意
// 与 OpenCode 后端解耦：自己拥有独立的 backend struct、事件类型、blocked-args 表和进程
// 生命周期，任何一方的改动都影响不到另一方。两者只共享包级通用进程辅助函数
// （configureProcessGroup、filterCustomArgs、trySend 等，本包每个 backend 都会复用）。
//
// DevEco 讲的是与上游 OpenCode 相同的 `run --format json` 协议、emit 相同的 NDJSON 事件
// 流（它构建在 OpenCode 引擎之上），所以下面的事件 schema 复刻那个形状。与 OpenCode
// 集成有两处刻意的差异：
//
//  1. 没有 `--prompt` flag——DevEco 的 `run` 子命令不暴露它（用 `deveco run --help`
//     验证过）。系统上下文因此靠 daemon 写入的每任务 AGENTS.md 到达 CLI，与生产上
//     OpenCode 依赖的同一条「基于文件」的通道。
//
//  2. 还没有内联 MCP 注入——DevEco 从 DEVECO_CONFIG_CONTENT（它对 OPENCODE_CONFIG_
//     CONTENT 的 DEVECO_ 前缀镜像）读 MCP，但把 agent.mcp_config 灌进去推迟到后续版本，
//     让本 backend 保持自包含。这期间 UI 对 deveco 隐藏 MCP 标签页
//     （见 packages/core/agents/mcp-support.ts）。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

// devecoTerminateGraceNanos 可选地以纳秒覆盖「被取消的 deveco 进程收到 SIGTERM 后，在被
// （连同整个进程组）SIGKILL 之前」享有的退出宽限时长。0 表示用默认值。它是原子量，测试
// 可以缩短宽限而不与读取它的取消 goroutine 竞争。
var devecoTerminateGraceNanos atomic.Int64

// devecoTerminateGrace 返回取消时的实际宽限时长：默认 5 秒，测试可用
// devecoTerminateGraceNanos 覆盖。
func devecoTerminateGrace() time.Duration {
	if n := devecoTerminateGraceNanos.Load(); n > 0 {
		return time.Duration(n)
	}
	return 5 * time.Second
}

// devecoBlockedArgs 是 daemon 写死的参数清单，用户配置的 custom_args 不得覆盖它们。
// DevEco 的 `run` 子命令暴露的 daemon 管理 flag 与 OpenCode 相同。
var devecoBlockedArgs = map[string]blockedArgMode{
	"--format":                       blockedWithValue,  // 与 daemon 通信所需的 json 输出格式
	"--dir":                          blockedWithValue,  // 任务工作目录锚点，供 skill / AGENTS.md 发现
	"--variant":                      blockedWithValue,  // 归 agent.thinking_level 所有
	"--dangerously-skip-permissions": blockedStandalone, // daemon 管理非交互权限确认
}

// devecoBackend 实现 Backend 接口：拉起 `deveco run --format json`，从 stdout 读流式
// JSON 事件。
type devecoBackend struct {
	cfg Config
}

// Execute 是 Backend 接口的入口：拿一条用户 prompt，起一个 deveco run 进程跑完一整轮。
//
// 流程：确认可执行文件（Windows 下尽量绕过 .cmd 垫片直启原生二进制，避免 argv 截断）→
// 拼参数（run --format json；无 --prompt flag，prompt 作为尾随位置参数传入；系统上下文
// 靠 daemon 写的 AGENTS.md；--dir 与 PWD 都锚定到任务工作目录）→ 接管 context 取消，
// 改为由专门 goroutine 驱动整组的 SIGTERM→宽限→SIGKILL（cmd.Cancel 置空避免 os/exec
// 与我们竞争，WaitDelay 兜底）→ 逐行读 stdout 事件并转 Message → 进程退出后依超时/
// 取消/退出错误定终态 → 汇总 Result。DevEco 不按步上报模型，用量统一归到配置的模型
// （或 "unknown"）。
func (b *devecoBackend) Execute(ctx context.Context, prompt string, opts ExecOptions) (*Session, error) {
	execPath := b.cfg.ExecutablePath
	if execPath == "" {
		execPath = "deveco"
	}
	resolved, err := exec.LookPath(execPath)
	if err != nil {
		return nil, fmt.Errorf("deveco executable not found at %q: %w", execPath, err)
	}
	if runtime.GOOS == "windows" {
		if native := resolveDevecoNativeFromShim(resolved, os.Stat); native != "" {
			b.cfg.Logger.Info("deveco resolved to native binary to avoid .cmd shim argv truncation", "shim", resolved, "native", native)
			resolved = native
		}
	}
	execPath = resolved

	timeout := opts.Timeout
	runCtx, cancel := runContext(ctx, timeout)

	args := []string{"run", "--format", "json", "--dangerously-skip-permissions"}
	// 把 DevEco 的项目发现（AGENTS.md 向上逐层找 + .deveco/skills/ 项目配置扫描）锚定到
	// 任务工作目录，与 OpenCode 的锚点对齐：没有 --dir，DevEco 会回落到 PWD（从 daemon
	// 继承）或 process.cwd()，自托管部署里那可能是用户的 shell 工作目录，静默绕过每任务
	// 工作目录。下面的 PWD 覆盖也出于同一原因。
	if opts.Cwd != "" {
		args = append(args, "--dir", opts.Cwd)
	}
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}
	if opts.ThinkingLevel != "" {
		args = append(args, "--variant", opts.ThinkingLevel)
	}
	// DevEco 的 `run` 子命令没有 --prompt flag，所以 SystemPrompt 有意不转发；系统上下文
	// 靠 daemon 为 deveco 写入的每任务 AGENTS.md 送达。
	if opts.MaxTurns > 0 {
		b.cfg.Logger.Warn("deveco does not support --max-turns; ignoring", "maxTurns", opts.MaxTurns)
	}
	if opts.ResumeSessionID != "" {
		args = append(args, "--session", opts.ResumeSessionID)
	}
	args = append(args, filterCustomArgs(opts.CustomArgs, devecoBlockedArgs, b.cfg.Logger)...)
	args = append(args, prompt)

	cmd := b.cfg.commandAt(execPath).exec(runCtx, args...)
	hideAgentWindow(cmd)
	// 接管 context 取消：由下面的取消 goroutine 驱动整组的 SIGTERM→SIGKILL，并在整棵树
	// 被信号处理过之后才关闭 stdout 读端。这里返回 nil，是让 os/exec 别用自己的 kill 与
	// 我们竞争；WaitDelay 作为硬兜底。
	cmd.Cancel = func() error { return nil }
	b.cfg.logAgentCommand(cmd, newAgentCommandLogArgs(args, trustAgentCommandPositional(0, "run")))
	cmd.WaitDelay = 10 * time.Second
	if opts.Cwd != "" {
		cmd.Dir = opts.Cwd
	}

	env := buildEnv(b.cfg.Env)
	// 覆盖 PWD，让子 DevEco 进程把发现根解析到任务工作目录。光设 cmd.Dir 不够：DevEco
	// 在计算向上找 AGENTS.md / .deveco/skills 的目录时，会先读 PWD（从父 daemon 继承），
	// 再回落 process.cwd()。
	if opts.Cwd != "" {
		env = append(env, "PWD="+opts.Cwd)
	}
	cmd.Env = env

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("deveco stdout pipe: %w", err)
	}
	cmd.Stderr = newLogWriter(b.cfg.Logger, "[deveco:stderr] ")

	if err := startOwnedProcessTree(cmd, b.cfg.Logger); err != nil {
		cancel()
		return nil, fmt.Errorf("start deveco: %w", err)
	}

	b.cfg.Logger.Info("deveco started", "pid", cmd.Process.Pid, "cwd", opts.Cwd, "model", opts.Model)

	msgCh := make(chan Message, 256)
	resCh := make(chan Result, 1)

	// procDone 在 cmd.Wait() 返回时关闭，让取消处理器能跳过「已经退出」的进程，避免向
	// 一个已死的 pid 发信号。
	procDone := make(chan struct{})

	// 取消/超时时，在解除 scanner 阻塞【之前】先终止 deveco（连同它拉起的工具子进程）。
	// 立即关 stdout 读端会让 deveco 往已关闭的管道里写（EPIPE），可能把孤儿进程转到
	// 100% CPU 空转。改为先给整个进程组发 SIGTERM、留一段宽限期让它干净退出，再 SIGKILL。
	// 只有在这之后，才能关 stdout 读端作为最后手段，解除仍被某个卡死的后代握着的 scanner。
	go func() {
		select {
		case <-procDone:
			return // 自己正常结束了；无需终止
		case <-runCtx.Done():
		}
		if cmd.Process != nil {
			signalProcessGroup(cmd, syscall.SIGTERM)
			select {
			case <-procDone: // 在宽限窗口内退出了
			case <-time.After(devecoTerminateGrace()):
				signalProcessGroup(cmd, syscall.SIGKILL)
			}
		}
		_ = stdout.Close()
	}()

	go func() {
		defer cancel()
		defer close(msgCh)
		defer close(resCh)

		startTime := time.Now()
		scanResult := b.processEvents(stdout, msgCh)

		exitErr := cmd.Wait()
		close(procDone)
		releaseProcessGroup(cmd)
		duration := time.Since(startTime)

		if runCtx.Err() == context.DeadlineExceeded {
			scanResult.status = "timeout"
			scanResult.errMsg = fmt.Sprintf("deveco timed out after %s", timeout)
		} else if runCtx.Err() == context.Canceled {
			scanResult.status = "aborted"
			scanResult.errMsg = "execution cancelled"
		} else if exitErr != nil && scanResult.status == "completed" {
			scanResult.status = "failed"
			scanResult.errMsg = fmt.Sprintf("deveco exited with error: %v", exitErr)
		}

		b.cfg.Logger.Info("deveco finished", "pid", cmd.Process.Pid, "status", scanResult.status, "duration", duration.Round(time.Millisecond).String())

		// 组用量表。DevEco 不按步上报模型，所以全部用量归到配置的模型（或 "unknown"）。
		var usage map[string]TokenUsage
		u := scanResult.usage
		if u.InputTokens > 0 || u.OutputTokens > 0 || u.CacheReadTokens > 0 || u.CacheWriteTokens > 0 {
			model := opts.Model
			if model == "" {
				model = "unknown"
			}
			usage = map[string]TokenUsage{model: u}
		}

		resCh <- Result{
			Status:     scanResult.status,
			Output:     scanResult.output,
			Error:      scanResult.errMsg,
			DurationMs: duration.Milliseconds(),
			SessionID:  scanResult.sessionID,
			Usage:      usage,
		}
	}()

	return &Session{Messages: msgCh, Result: resCh}, nil
}

// resolveDevecoNativeFromShim 给定 Windows 上 PATH 找到的 npm `deveco.cmd` 垫片路径，
// 返回该 npm 包内捆绑的原生 DevEco 可执行文件路径。若垫片不以 `.cmd` 结尾、或找不到候选
// 原生二进制，返回 ""（此时调用方沿用原垫片路径）。
//
// 这镜像了 OpenCode 的修复（resolveOpenCodeNativeFromShim）——DevEco 以同样方式经 npm
// 分发，所以继承了同样的缺陷：Windows 批处理经 `%*` 转发参数会丢弃第一个换行之后的一切，
// 因此多行的位置参数 prompt（daemon 系统上下文 + 用户消息）在垫片交给 JS 入口前就被截断
// 了。直接拉起原生二进制则完全跳过 cmd.exe 这一层。两个 resolver 各自独立（不共用），这样
// DevEco 的独立 backend 不会因 OpenCode 的改动而坏、反之亦然；下面的包布局差异正是原因。
//
// DevEco 的 npm 布局与 OpenCode 不同（对照真实 `@deveco/deveco-code` 0.1.2 tarball
// 验证）：
//
//   - 包是带 scope 的（`@deveco/deveco-code`），所以位于
//     `node_modules\@deveco\deveco-code\...` 而不是平铺的名字。
//   - 原生二进制名为 `deveco.exe`，每个平台子包（`@deveco/deveco-code-windows-x64
//     {,-baseline}`）在 `bin\deveco.exe` 暴露它。
//   - `postinstall.mjs` 会把按 CPU 变体选中的二进制复制进主包自己的 `bin\deveco.exe`，
//     所以那份拷贝是最可靠的目标（已反映 AVX2/baseline 决策），最先尝试。
//   - 没有 windows-arm64 包（DevEco 只发 Windows x64），候选列表里也就没有它。
//
// statFn 是注入的，让非 Windows 主机也能测这个函数。
func resolveDevecoNativeFromShim(shimPath string, statFn func(string) (os.FileInfo, error)) string {
	if !strings.EqualFold(filepath.Ext(shimPath), ".cmd") {
		return ""
	}
	prefix := filepath.Dir(shimPath)
	scope := filepath.Join(prefix, "node_modules", "@deveco")
	candidates := []string{
		// postinstall 把选中的原生二进制复制到这里；最可靠。
		filepath.Join(scope, "deveco-code", "bin", "deveco.exe"),
		// 若那份拷贝不存在，直接回退到平台子包。
		filepath.Join(scope, "deveco-code-windows-x64", "bin", "deveco.exe"),
		filepath.Join(scope, "deveco-code-windows-x64-baseline", "bin", "deveco.exe"),
	}
	for _, candidate := range candidates {
		if _, err := statFn(candidate); err == nil {
			return candidate
		}
	}
	return ""
}

// ── 事件处理 ──

// devecoEventResult 汇总处理事件流过程中累积出的状态。
type devecoEventResult struct {
	status    string
	errMsg    string
	output    string
	sessionID string
	usage     TokenUsage // 所有步骤累计的 token 用量
}

// processEvents 从 r 逐行读 JSON 事件、派发给 ch，返回累积结果。这是核心扫描循环，
// 抽出来便于测试。
func (b *devecoBackend) processEvents(r io.Reader, ch chan<- Message) devecoEventResult {
	var output strings.Builder
	var sessionID string
	var usage TokenUsage
	finalStatus := "completed"
	var finalError string

	scanner := newAgentStreamScanner(r)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var event devecoEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}

		if event.SessionID != "" {
			sessionID = event.SessionID
		}

		switch event.Type {
		case "text":
			b.handleTextEvent(event, ch, &output)
		case "tool_use":
			b.handleToolUseEvent(event, ch)
		case "error":
			b.handleErrorEvent(event, ch, &finalStatus, &finalError)
		case "step_start":
			trySend(ch, Message{Type: MessageStatus, Status: "running"})
		case "step_finish":
			// 从 step_finish 事件累计 token 用量。
			if t := event.Part.Tokens; t != nil {
				usage.InputTokens += t.Input
				usage.OutputTokens += t.Output
				if t.Cache != nil {
					usage.CacheReadTokens += t.Cache.Read
					usage.CacheWriteTokens += t.Cache.Write
				}
			}
		}
	}

	if scanErr := scanner.Err(); scanErr != nil {
		b.cfg.Logger.Warn("deveco stdout scanner error", "error", scanErr)
		if finalStatus == "completed" {
			finalStatus = "failed"
			finalError = fmt.Sprintf("stdout read error: %v", scanErr)
		}
	}

	return devecoEventResult{
		status:    finalStatus,
		errMsg:    finalError,
		output:    output.String(),
		sessionID: sessionID,
		usage:     usage,
	}
}

// handleTextEvent 处理 "text" 事件：文本累进 output（最终 Result.Output 用），同时作为
// MessageText 流式推送。
func (b *devecoBackend) handleTextEvent(event devecoEvent, ch chan<- Message, output *strings.Builder) {
	text := event.Part.Text
	if text != "" {
		output.WriteString(text)
		trySend(ch, Message{Type: MessageText, Content: text})
	}
}

// handleToolUseEvent 处理 "tool_use" 事件。当工具进入终态（state.status 为 "completed"
// 或 "error"）时，一条 tool_use 事件在 part.state 里同时含调用与结果。
func (b *devecoBackend) handleToolUseEvent(event devecoEvent, ch chan<- Message) {
	var input map[string]any
	if event.Part.State != nil && event.Part.State.Input != nil {
		_ = json.Unmarshal(event.Part.State.Input, &input)
	}

	trySend(ch, Message{
		Type:   MessageToolUse,
		Tool:   event.Part.Tool,
		CallID: event.Part.CallID,
		Input:  input,
	})

	// 把每个终态工具使用配一条工具结果。daemon 用这对消息跟踪在飞工具；漏掉 error 结果
	// 会让它的计数器一直偏高、压掉正常的空闲看门狗。
	state := event.Part.State
	if state != nil && (state.Status == "completed" || state.Status == "error") {
		outputStr := extractDevecoToolOutput(state.Output)
		if state.Status == "error" && state.Error != "" {
			outputStr = state.Error
		}
		trySend(ch, Message{
			Type:   MessageToolResult,
			Tool:   event.Part.Tool,
			CallID: event.Part.CallID,
			Output: outputStr,
		})
	}
}

// handleErrorEvent 处理 "error" 事件。DevEco 即使出错（如非法模型）也可能以 RC=0 退出，
// 所以 error 事件才是可靠的失败信号。
func (b *devecoBackend) handleErrorEvent(event devecoEvent, ch chan<- Message, finalStatus, finalError *string) {
	errMsg := ""
	if event.Error != nil {
		errMsg = event.Error.Message()
	}
	if errMsg == "" {
		errMsg = "unknown deveco error"
	}

	b.cfg.Logger.Warn("deveco error event", "error", errMsg)
	trySend(ch, Message{Type: MessageError, Content: errMsg})

	*finalStatus = "failed"
	*finalError = errMsg
}

// extractDevecoToolOutput 把工具状态输出（可能是字符串，也可能是结构化对象）转成字符串。
func extractDevecoToolOutput(output any) string {
	if output == nil {
		return ""
	}
	if s, ok := output.(string); ok {
		return s
	}
	data, _ := json.Marshal(output)
	return string(data)
}

// ── `deveco run --format json` stdout 事件的 JSON 类型 ──
//
// DevEco 输出与上游 OpenCode 相同的 NDJSON 事件 schema。真实输出里见过的类型：
//
//	"step_start"  — agent 步骤开始
//	"text"        — agent 的文本输出（part.text）
//	"tool_use"    — 工具调用，含调用与结果（part.tool、part.callID、part.state）
//	"error"       — deveco 的错误（error.name、error.data.message）
//	"step_finish" — agent 步骤完成（含 token 用量）
type devecoEvent struct {
	Type      string          `json:"type"`
	Timestamp int64           `json:"timestamp,omitempty"`
	SessionID string          `json:"sessionID,omitempty"`
	Part      devecoEventPart `json:"part"`
	Error     *devecoError    `json:"error,omitempty"`
}

// devecoEventPart 是 deveco 事件里的 part 字段。
type devecoEventPart struct {
	ID        string `json:"id,omitempty"`
	MessageID string `json:"messageID,omitempty"`
	SessionID string `json:"sessionID,omitempty"`
	Type      string `json:"type,omitempty"`

	// text 事件用
	Text string `json:"text,omitempty"`

	// tool use 事件用
	Tool   string           `json:"tool,omitempty"`
	CallID string           `json:"callID,omitempty"`
	State  *devecoToolState `json:"state,omitempty"`

	// step_finish 的 token 用量
	Tokens *devecoTokens `json:"tokens,omitempty"`
}

// devecoTokens 是 step_finish 事件里的 token 用量。
type devecoTokens struct {
	Input  int64              `json:"input"`
	Output int64              `json:"output"`
	Cache  *devecoCacheTokens `json:"cache,omitempty"`
}

// devecoCacheTokens 是缓存命中档位上的 token 用量。
type devecoCacheTokens struct {
	Read  int64 `json:"read"`
	Write int64 `json:"write"`
}

// devecoToolState 是一次工具调用的状态。
type devecoToolState struct {
	Status string          `json:"status,omitempty"`
	Input  json.RawMessage `json:"input,omitempty"`
	Output any             `json:"output,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// devecoError 是 deveco 的 error 事件。
type devecoError struct {
	Name string         `json:"name,omitempty"`
	Data *devecoErrData `json:"data,omitempty"`
}

// Message 返回人类可读的错误消息：优先 data.message，其次 name。
func (e *devecoError) Message() string {
	if e.Data != nil && e.Data.Message != "" {
		return e.Data.Message
	}
	if e.Name != "" {
		return e.Name
	}
	return ""
}

// devecoErrData 是 error 事件 data 字段的载荷。
type devecoErrData struct {
	Message string `json:"message,omitempty"`
}
