package agent

// 文件职责：DSH（Multica DSH bundle）运行时的适配器。
//
// DSH 自带 agent 主循环、会话存储、模型目录、工具与 MCP 客户端，只通过一套带
// 版本号（v=1）的 JSONL stdio 协议对外服务。本文件不实现任何 agent 逻辑，只做
// 两件事：把下发的执行命令（execute/cancel）编码成协议帧，把上游返回的协议帧
// （ready/session/text/thinking/tool_call/tool_result/usage/result 等）翻译成
// multica 统一的 Backend 事件流与最终 Result，并负责子进程生命周期管理（启动、
// 分级取消：先发 cancel 帧 → SIGTERM → SIGKILL）与按模型累加 token 用量。
// 它与 ACP 系列适配器刻意相互独立。
//
// 主要入口：dshBackend.Execute（提交一次执行）；discoverDshModels（拉模型目录）。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	dshProfile         = "multica"       // 启动 dsh 时固定使用的 profile 名
	DshProtocolVersion = 1               // 本适配器认的协议版本；帧 v 与它不符即为协议错误
	dshCancelGrace     = 3 * time.Second // 发完 cancel 帧后，等进程自行退出的宽限时间
	dshTerminateGrace  = 2 * time.Second // 发完 SIGTERM 后，等进程组消失的宽限时间，超了就 SIGKILL
)

// dshBackend 通过 DSH 带版本号的 JSONL stdio 协议驱动 Multica DSH bundle。
// 适配器刻意不依赖 ACP：agent 主循环、会话存储、模型目录、工具与 MCP 客户端都由
// DSH 自己负责，本包只把这些事件翻译成 multica 的 Backend 契约。
type dshBackend struct {
	cfg Config
}

// dshModelSelection 描述一次执行要用的模型，随 execute 帧下发；reasoning_effort
// 为推理投入档位，与 ThinkingLevel 对应。
type dshModelSelection struct {
	Provider        string `json:"provider"`                   // provider 名（如 "openai"），做了 URL 转义
	ID              string `json:"id"`                         // 模型 id，做了 URL 转义（provider 与 id 里可能有 "/"）
	ReasoningEffort string `json:"reasoning_effort,omitempty"` // 推理投入档位；空表示用默认
}

// dshMCPServer 是发给 dsh 的 MCP 服务器描述。Transport 二选一：stdio
// （本地命令 + 参数）或 streamable-http（远程 URL + 请求头）。
type dshMCPServer struct {
	Name              string            `json:"name"`                           // 服务器名，MCP 协议里用于标识
	Transport         string            `json:"transport"`                      // "stdio" 或 "streamable-http"
	Command           string            `json:"command,omitempty"`              // stdio 形态：本地命令路径
	Args              []string          `json:"args,omitempty"`                 // stdio 形态：命令参数
	Env               map[string]string `json:"env,omitempty"`                  // stdio 形态：子进程环境变量
	Cwd               string            `json:"cwd,omitempty"`                  // stdio 形态：子进程工作目录
	URL               string            `json:"url,omitempty"`                  // streamable-http 形态：服务器地址
	Headers           map[string]string `json:"headers,omitempty"`              // streamable-http 形态：附加请求头
	ToolCallTimeoutMS int               `json:"tool_call_timeout_ms,omitempty"` // 单次工具调用超时（毫秒）
}

// dshExecuteCommand 是下发"执行"的入站命令帧。
type dshExecuteCommand struct {
	Version         int                `json:"v"`
	Type            string             `json:"type"`                        // 固定 "execute"
	RequestID       string             `json:"request_id"`                  // 本次请求 id，用于把结果对回任务
	Cwd             string             `json:"cwd"`                         // 工作目录
	Prompt          string             `json:"prompt"`                      // 提示词正文
	ResumeSessionID string             `json:"resume_session_id,omitempty"` // 非空表示续跑某会话
	Model           *dshModelSelection `json:"model,omitempty"`             // 选定模型；空用默认
	ReasoningEffort string             `json:"reasoning_effort,omitempty"`
	MCPServers      []dshMCPServer     `json:"mcp_servers"` // 本次执行要挂载的 MCP 服务器
}

// dshCancelCommand 是下发"取消"的入站命令帧（优雅取消，先于信号）。
type dshCancelCommand struct {
	Version   int    `json:"v"`
	Type      string `json:"type"` // 固定 "cancel"
	RequestID string `json:"request_id"`
}

// dshWireError 是结果帧里携带的结构化错误。
type dshWireError struct {
	Code    string `json:"code"`    // 机器可读错误码
	Message string `json:"message"` // 人类可读描述
}

// dshFrame 是 DSH 出站协议帧的通用载体，按 Type 区分语义、只取相关字段。
type dshFrame struct {
	Version          int    `json:"v"`                 // 协议版本，应与 DshProtocolVersion 一致
	Type             string `json:"type"`              // 帧类型：ready/session/text/thinking/tool_call/tool_result/usage/protocol_error/result/models
	Runtime          string `json:"runtime,omitempty"` // ready 帧里自报的运行时名，需为 "dsh"
	ProtocolVersion  int    `json:"protocol_version,omitempty"`
	RequestID        string `json:"request_id,omitempty"`
	SessionID        string `json:"session_id,omitempty"`
	Resumed          bool   `json:"resumed,omitempty"`   // 是否为续跑后的会话
	Content          string `json:"content,omitempty"`   // text/thinking 帧的正文
	CallID           string `json:"call_id,omitempty"`   // 工具调用 id
	Name             string `json:"name,omitempty"`      // 工具名
	Arguments        string `json:"arguments,omitempty"` // 工具入参（JSON 字符串）
	Output           string `json:"output,omitempty"`    // 工具出参 / 最终输出
	IsError          bool   `json:"is_error,omitempty"`
	Provider         string `json:"provider,omitempty"` // usage 帧：provider 名
	Model            string `json:"model,omitempty"`    // usage 帧：模型名
	InputTokens      int64  `json:"input_tokens,omitempty"`
	OutputTokens     int64  `json:"output_tokens,omitempty"`
	CacheReadTokens  int64  `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int64  `json:"cache_write_tokens,omitempty"`
	ReasoningTokens  int64  `json:"reasoning_tokens,omitempty"`
	// Runtime telemetry frames (context / compaction). The adapter forwards them
	// to the daemon as MessageRuntime JSON instead of model-visible content.
	Seq             int64  `json:"seq,omitempty"`
	At              string `json:"at,omitempty"`
	DurationMs      int64  `json:"duration_ms,omitempty"`
	ContextWindow   int64  `json:"context_window,omitempty"`
	PressureTokens  int64  `json:"pressure_tokens,omitempty"`
	ProjectedTokens int64  `json:"projected_tokens,omitempty"`
	Phase           string `json:"phase,omitempty"`
	BeforeTokens    int64  `json:"before_tokens,omitempty"`
	AfterTokens     int64  `json:"after_tokens,omitempty"`
	Reason          string `json:"reason,omitempty"`
	Breakdown       *struct {
		SystemTokens  int64 `json:"system_tokens"`
		ToolsTokens   int64 `json:"tools_tokens"`
		MessageTokens int64 `json:"message_tokens"`
	} `json:"breakdown,omitempty"`
	Status         string          `json:"status,omitempty"` // result 帧：完成状态
	StopReason     string          `json:"stop_reason,omitempty"`
	ResumeRejected bool            `json:"resume_rejected,omitempty"` // result 帧：续跑被永久拒绝
	Error          *dshWireError   `json:"error,omitempty"`           // result/protocol_error 帧：结构化错误
	Code           string          `json:"code,omitempty"`
	Message        string          `json:"message,omitempty"`
	Models         []dshModelFrame `json:"models,omitempty"` // models 帧：模型目录
}

// dshThinkingFrame 描述某个模型支持的思考档位。
type dshThinkingFrame struct {
	SupportedLevels []ThinkingLevel `json:"supported_levels"`        // 支持的档位（复用 multica 的档位类型）
	DefaultLevel    string          `json:"default_level,omitempty"` // 默认档位
}

// dshModelFrame 是模型目录里的单个条目。
type dshModelFrame struct {
	ID       string            `json:"id"`
	Label    string            `json:"label"` // 展示名
	Provider string            `json:"provider,omitempty"`
	Default  bool              `json:"default,omitempty"`  // 是否默认模型
	Thinking *dshThinkingFrame `json:"thinking,omitempty"` // 思考能力描述；nil 表示不支持
}

// dshLaunchArgs 返回启动 dsh 的固定参数：用 multica profile、走 stdio。
func dshLaunchArgs() []string {
	return []string{"--profile", dshProfile, "--stdio"}
}

// parseDshModelID 把形如 "provider/模型id" 的字符串解析成 dshModelSelection。
//
// 约定：DSH 广告的模型 id 里，provider 段和模型段都可能包含 "/"，所以协议约定
// 这两段都做 URL 转义（PathUnescape 还原），并以第一个 "/" 切分。空字符串返回
// (nil, nil)，表示"用默认模型"。
func parseDshModelID(value string) (*dshModelSelection, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	providerPart, modelPart, ok := strings.Cut(value, "/")
	if !ok || providerPart == "" || modelPart == "" {
		return nil, fmt.Errorf("dsh model must use the provider/model ID advertised by DSH")
	}
	// 还原转义：provider 或模型名里若真有 "/"，传输时会被转义成 "%2F"，
	// 不能凭裸 "/" 切，否则切错。
	provider, err := url.PathUnescape(providerPart)
	if err != nil {
		return nil, fmt.Errorf("decode dsh model provider: %w", err)
	}
	if strings.TrimSpace(provider) == "" {
		return nil, errors.New("dsh model provider is empty")
	}
	model, err := url.PathUnescape(modelPart)
	if err != nil {
		return nil, fmt.Errorf("decode dsh model ID: %w", err)
	}
	if strings.TrimSpace(model) == "" {
		return nil, errors.New("dsh model ID is empty")
	}
	return &dshModelSelection{Provider: provider, ID: model}, nil
}

// buildDshMCPServers 把 multica 通用的 MCP 配置（json.RawMessage，结构见
// buildACPMcpServers）转成 dsh 认识的 []dshMCPServer：有本地 command 的走
// stdio，只有远程 url 的走 streamable-http；其余无法识别的条目跳过并告警。
func buildDshMCPServers(raw json.RawMessage, logger interface {
	Warn(string, ...any)
}) ([]dshMCPServer, error) {
	// 先复用 ACP 的解析逻辑做结构归一，得到 []any（每项是 map[string]any）。
	servers, err := buildACPMcpServers(raw, nil)
	if err != nil {
		return nil, err
	}
	out := make([]dshMCPServer, 0, len(servers))
	for _, rawServer := range servers {
		server, ok := rawServer.(map[string]any)
		if !ok {
			continue
		}
		name, _ := server["name"].(string)
		// 形态一：本地命令 → stdio 传输。env/args 在通用结构里是"键值对列表"
		// （[]map[string]any），在这里摊平成 map。
		if command, _ := server["command"].(string); command != "" {
			entry := dshMCPServer{Name: name, Transport: "stdio", Command: command, Args: []string{}, Env: map[string]string{}}
			if args, ok := server["args"].([]string); ok {
				entry.Args = args
			}
			if pairs, ok := server["env"].([]map[string]any); ok {
				for _, pair := range pairs {
					key, _ := pair["name"].(string)
					value, _ := pair["value"].(string)
					if key != "" {
						entry.Env[key] = value
					}
				}
			}
			out = append(out, entry)
			continue
		}

		// 形态二：远程服务器。SSE 不被 DSH 支持，直接报错让调用方换协议；
		// 没有 url 的条目既不是 stdio 也不是 http，属于配置错误，跳过并告警。
		transport, _ := server["type"].(string)
		if transport == "sse" {
			return nil, fmt.Errorf("dsh MCP server %q uses SSE, but the DSH runtime supports stdio and streamable HTTP only", name)
		}
		remoteURL, _ := server["url"].(string)
		if remoteURL == "" {
			if logger != nil {
				logger.Warn("skipping invalid DSH MCP server", "name", name)
			}
			continue
		}
		entry := dshMCPServer{Name: name, Transport: "streamable-http", URL: remoteURL, Headers: map[string]string{}}
		if pairs, ok := server["headers"].([]map[string]any); ok {
			for _, pair := range pairs {
				key, _ := pair["name"].(string)
				value, _ := pair["value"].(string)
				if key != "" {
					entry.Headers[key] = value
				}
			}
		}
		out = append(out, entry)
	}
	return out, nil
}

// Execute 实现 Backend.Execute：启动一个 dsh 子进程，写入 execute 命令帧，
// 返回一对通道供调用方流式读取事件与最终结果。
//
// 取消/超时路径分三级：runCtx 到期先发 cancel 帧给 dsh 优雅收尾；宽限
// dshCancelGrace 后进程还活着就 SIGTERM；再宽限 dshTerminateGrace 后仍未消失
// 则 SIGKILL（见下方第一个 goroutine）。结果通道永远只收一个值。
func (b *dshBackend) Execute(ctx context.Context, prompt string, opts ExecOptions) (*Session, error) {
	execPath := b.cfg.ExecutablePath
	if execPath == "" {
		execPath = "dsh"
	}
	if _, err := exec.LookPath(execPath); err != nil {
		return nil, fmt.Errorf("dsh executable not found at %q: %w", execPath, err)
	}
	model, err := parseDshModelID(opts.Model)
	if err != nil {
		return nil, err
	}
	mcpServers, err := buildDshMCPServers(opts.McpConfig, b.cfg.Logger)
	if err != nil {
		return nil, fmt.Errorf("dsh: invalid mcp_config: %w", err)
	}

	runCtx, cancel := runContext(ctx, opts.Timeout)
	args := dshLaunchArgs()
	cmd := b.cfg.commandAt(execPath).exec(runCtx, args...)
	hideAgentWindow(cmd)
	// 取消不走 exec.Command 默认的杀进程路径，而是走"cancel 帧→SIGTERM→SIGKILL"
	// 的分级流程，所以这里禁用默认 Cancel。
	cmd.Cancel = func() error { return nil }
	cmd.WaitDelay = 10 * time.Second
	if opts.Cwd != "" {
		cmd.Dir = opts.Cwd
	}
	cmd.Env = buildEnv(b.cfg.Env)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("dsh stdout pipe: %w", err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("dsh stdin pipe: %w", err)
	}
	stderrBuf := newStderrTail(newLogWriter(b.cfg.Logger, "[dsh:stderr] "), agentStderrTailBytes)
	cmd.Stderr = stderrBuf
	if err := startOwnedProcessTree(cmd, b.cfg.Logger); err != nil {
		cancel()
		return nil, fmt.Errorf("start dsh: %w", err)
	}

	requestID := b.cfg.TaskID
	if requestID == "" {
		requestID = "multica-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	command := dshExecuteCommand{
		Version: DshProtocolVersion, Type: "execute", RequestID: requestID,
		Cwd: opts.Cwd, Prompt: prompt, ResumeSessionID: opts.ResumeSessionID,
		Model: model, ReasoningEffort: opts.ThinkingLevel, MCPServers: mcpServers,
	}
	// json.Encoder 不保证并发安全；主流程写 execute 帧、下方取消 goroutine 可能写
	// cancel 帧，因此所有写入都经 writeFrame 串行加锁。
	var writeMu sync.Mutex
	encoder := json.NewEncoder(stdin)
	writeFrame := func(value any) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return encoder.Encode(value)
	}
	// execute 帧都发不出去说明进程侧已经坏了，直接整组杀掉，不留孤儿进程。
	if err := writeFrame(command); err != nil {
		signalProcessGroup(cmd, syscall.SIGKILL)
		_ = cmd.Wait()
		releaseProcessGroup(cmd)
		cancel()
		return nil, fmt.Errorf("send dsh execute command: %w", err)
	}

	b.cfg.Logger.Info("dsh started", "pid", cmd.Process.Pid, "cwd", opts.Cwd, "model", opts.Model)
	msgCh := make(chan Message, 256)
	resCh := make(chan Result, 1)
	procDone := make(chan struct{})

	// 取消看门狗：runCtx 一到期（外部取消或超时）就先发 cancel 帧请求 dsh 优雅
	// 收尾；超过 dshCancelGrace 还没退出就 SIGTERM；再超过 dshTerminateGrace
	// 仍未消失就 SIGKILL。procDone 关闭表示解码循环已退出，立即停止进一步操作。
	go func() {
		select {
		case <-procDone:
			return
		case <-runCtx.Done():
		}
		_ = writeFrame(dshCancelCommand{Version: DshProtocolVersion, Type: "cancel", RequestID: requestID})
		timer := time.NewTimer(dshCancelGrace)
		defer timer.Stop()
		select {
		case <-procDone:
			return
		case <-timer.C:
		}
		signalProcessGroup(cmd, syscall.SIGTERM)
		if !waitProcessGroupGone(cmd, dshTerminateGrace) {
			signalProcessGroup(cmd, syscall.SIGKILL)
		}
		_ = stdout.Close()
	}()

	// 解码循环：逐行读 stdout 上的 JSONL 帧，翻译成统一 Message 推送；帧流结束后
	// 收尸（cmd.Wait）、按上下文/扫描/退出原因拼出最终 Result 并发送。
	go func() {
		defer cancel()
		defer close(msgCh)
		defer close(resCh)
		defer func() { _ = stdin.Close() }()
		started := time.Now()
		state := dshRunState{usage: make(map[string]TokenUsage)}
		scanner := newAgentStreamScanner(stdout)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			var frame dshFrame
			// 坏行只计数跳过：DSH 可能在帧间混入日志之类非协议输出，不能因单行
			// 损坏就放弃整个会话。
			if err := json.Unmarshal([]byte(line), &frame); err != nil {
				state.invalidFrames++
				continue
			}
			state.frameCount++
			handleDshFrame(frame, requestID, msgCh, &state)
		}
		scanErr := scanner.Err()
		if scanErr != nil {
			_ = stdout.Close()
		}
		exitErr := cmd.Wait()
		close(procDone)
		releaseProcessGroup(cmd)

		result := state.result
		if result == nil {
			// 进程结束仍没收到 result 帧：按失败归类，越具体的原因越优先。
			result = &Result{Status: "failed", SessionID: state.sessionID, Usage: state.usage}
			switch {
			case errors.Is(runCtx.Err(), context.DeadlineExceeded):
				result.Status = "timeout"
				result.Error = fmt.Sprintf("dsh timed out after %s", opts.Timeout)
			case errors.Is(runCtx.Err(), context.Canceled):
				result.Status = "cancelled"
				result.Error = "dsh execution cancelled"
			case scanErr != nil:
				result.Error = fmt.Sprintf("read dsh event stream: %v", scanErr)
			case state.protocolError != "":
				result.Error = state.protocolError
			case !state.ready:
				result.Error = "dsh exited before the runtime protocol became ready"
			case exitErr != nil:
				result.Error = fmt.Sprintf("dsh exited with error: %v", exitErr)
			default:
				result.Error = "dsh exited without a terminal result"
			}
		}
		// 报错时附上 stderr 尾部，帮用户/日志看到进程侧的真实报错。
		if result.Error != "" {
			result.Error = withAgentStderr(result.Error, "dsh", stderrBuf.Tail())
		}
		result.DurationMs = time.Since(started).Milliseconds()
		b.cfg.Logger.Info("dsh finished", "pid", cmd.Process.Pid, "status", result.Status,
			"duration", time.Since(started).Round(time.Millisecond).String(), "frames", state.frameCount,
			"invalid_frames", state.invalidFrames)
		resCh <- *result
	}()

	return &Session{Messages: msgCh, Result: resCh}, nil
}

// dshRunState 是单次执行的帧解码累积状态，跨帧汇总会话/错误/用量信息。
type dshRunState struct {
	ready         bool                  // 是否收到过合法的 ready 帧（运行时就绪）
	sessionID     string                // 从 session 帧拿到的会话 id
	protocolError string                // 协议层错误文本（版本不符 / protocol_error 帧）
	frameCount    int                   // 收到的协议帧总数（日志用）
	invalidFrames int                   // 解析失败被跳过的行数（日志用）
	usage         map[string]TokenUsage // 按 "provider/model" 累加的用量
	result        *Result               // 收到 result 帧后存放最终结果；nil 表示还没结束
}

// handleDshFrame 把单个协议帧落进 state 并视需要向 ch 推送统一 Message。
//
// 版本不符或 request_id 不属于本次请求的帧一律忽略——前者记入 protocolError
// 以便最终结果给出明确原因，后者是上游还在回放别的任务的残留。
func handleDshFrame(frame dshFrame, requestID string, ch chan<- Message, state *dshRunState) {
	if frame.Version != DshProtocolVersion {
		state.protocolError = fmt.Sprintf("dsh returned unsupported protocol version %d", frame.Version)
		return
	}
	if frame.RequestID != "" && frame.RequestID != requestID {
		return
	}
	switch frame.Type {
	case "ready":
		// 就绪帧带运行时自报；必须是 dsh 本尊才算数，避免误连其它同协议的 CLI。
		state.ready = frame.Runtime == "dsh"
	case "session":
		state.sessionID = frame.SessionID
		trySend(ch, Message{Type: MessageStatus, Status: "running", SessionID: frame.SessionID})
	case "text":
		if frame.Content != "" {
			trySend(ch, Message{Type: MessageText, Content: frame.Content})
		}
	case "thinking":
		if frame.Content != "" {
			trySend(ch, Message{Type: MessageThinking, Content: frame.Content})
		}
	case "tool_call":
		// 入参是 JSON 字符串；能解析就结构化，解析失败就原样包一层 raw 兜底，
		// 避免把参数弄丢。
		input := map[string]any{}
		if frame.Arguments != "" && json.Unmarshal([]byte(frame.Arguments), &input) != nil {
			input = map[string]any{"raw": frame.Arguments}
		}
		trySend(ch, Message{Type: MessageToolUse, Tool: frame.Name, CallID: frame.CallID, Input: input})
	case "tool_result":
		trySend(ch, Message{Type: MessageToolResult, Tool: frame.Name, CallID: frame.CallID, Output: frame.Output})
	case "usage":
		// 同一模型可能分多条 usage 帧上报，按 key 累加而不是覆盖。
		key := frame.Model
		if frame.Provider != "" {
			key = frame.Provider + "/" + frame.Model
		}
		if key != "" {
			usage := state.usage[key]
			usage.InputTokens += frame.InputTokens
			usage.OutputTokens += frame.OutputTokens
			usage.CacheReadTokens += frame.CacheReadTokens
			usage.CacheWriteTokens += frame.CacheWriteTokens
			state.usage[key] = usage
		}
	case "context", "compaction":
		// Runtime telemetry is stored as one JSON task message per frame. The
		// payload keeps the frame type so the server can aggregate context and
		// compaction series without a second protocol version.
		payload := map[string]any{
			"kind":          frame.Type,
			"request_id":    frame.RequestID,
			"session_id":    frame.SessionID,
			"seq":           frame.Seq,
			"at":            frame.At,
			"provider":      frame.Provider,
			"model":         frame.Model,
			"duration_ms":   frame.DurationMs,
			"input_tokens":  frame.InputTokens,
			"output_tokens": frame.OutputTokens,
		}
		if frame.CacheReadTokens > 0 {
			payload["cache_read_tokens"] = frame.CacheReadTokens
		}
		if frame.CacheWriteTokens > 0 {
			payload["cache_write_tokens"] = frame.CacheWriteTokens
		}
		if frame.ReasoningTokens > 0 {
			payload["reasoning_tokens"] = frame.ReasoningTokens
		}
		if frame.ContextWindow > 0 {
			payload["context_window"] = frame.ContextWindow
		}
		if frame.PressureTokens > 0 {
			payload["pressure_tokens"] = frame.PressureTokens
		}
		if frame.ProjectedTokens > 0 {
			payload["projected_tokens"] = frame.ProjectedTokens
		}
		if frame.Breakdown != nil {
			payload["breakdown"] = map[string]any{
				"system_tokens":  frame.Breakdown.SystemTokens,
				"tools_tokens":   frame.Breakdown.ToolsTokens,
				"message_tokens": frame.Breakdown.MessageTokens,
			}
		}
		if frame.Phase != "" {
			payload["phase"] = frame.Phase
		}
		if frame.BeforeTokens > 0 {
			payload["before_tokens"] = frame.BeforeTokens
		}
		if frame.AfterTokens > 0 {
			payload["after_tokens"] = frame.AfterTokens
		}
		if frame.Reason != "" {
			payload["reason"] = frame.Reason
		}
		if raw, err := json.Marshal(payload); err == nil {
			trySend(ch, Message{Type: MessageRuntime, Content: string(raw)})
		}
	case "protocol_error":
		state.protocolError = strings.TrimSpace(frame.Code + ": " + frame.Message)
	case "result":
		errorText := ""
		if frame.Error != nil {
			errorText = strings.TrimSpace(frame.Error.Code + ": " + frame.Error.Message)
		}
		state.result = &Result{
			Status: frame.Status, Output: frame.Output, Error: errorText,
			SessionID: frame.SessionID, Usage: state.usage, ResumeRejected: frame.ResumeRejected,
		}
		// 部分实现只在 session 帧给 id、result 帧不给，回退到已记录的会话 id。
		if state.result.SessionID == "" {
			state.result.SessionID = state.sessionID
		}
	}
}

// discoverDshModels 运行 dsh 的 --list-models 探测模型目录，把返回的 models 帧
// 翻译成 multica 的 Model 列表。探测用独立的短暂进程，不带 MCP 配置。
func discoverDshModels(ctx context.Context, runtimeCmd Command) ([]Model, error) {
	if runtimeCmd.Path == "" {
		runtimeCmd.Path = "dsh"
	}
	cmd := runtimeCmd.exec(ctx, "--profile", dshProfile, "--list-models")
	hideAgentWindow(cmd)
	cmd.WaitDelay = time.Second
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = io.Discard
	if err := startOwnedProcessTree(cmd, runtimeCmd.logger); err != nil {
		return nil, err
	}
	var models []Model
	scanner := newAgentStreamScanner(stdout)
	for scanner.Scan() {
		// 只认版本正确、类型为 models 的帧；其余行（如日志）直接跳过。
		var frame dshFrame
		if json.Unmarshal(scanner.Bytes(), &frame) != nil || frame.Version != DshProtocolVersion || frame.Type != "models" {
			continue
		}
		for _, item := range frame.Models {
			model := Model{ID: item.ID, Label: item.Label, Provider: item.Provider, Default: item.Default}
			if item.Thinking != nil && len(item.Thinking.SupportedLevels) > 0 {
				model.Thinking = &ModelThinking{SupportedLevels: item.Thinking.SupportedLevels, DefaultLevel: item.Thinking.DefaultLevel}
			}
			models = append(models, model)
		}
	}
	scanErr := scanner.Err()
	exitErr := cmd.Wait()
	releaseProcessGroup(cmd)
	if scanErr != nil {
		return nil, scanErr
	}
	if exitErr != nil {
		return nil, exitErr
	}
	// 空目录说明 DSH 自己没发现任何模型，视为探测失败而不是"合法空列表"，
	// 避免上层拿着空列表继续往后走。
	if len(models) == 0 {
		return nil, errors.New("dsh returned an empty model catalog")
	}
	return models, nil
}
