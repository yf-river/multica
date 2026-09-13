package agent

// 文件职责：本文件实现 Backend 接口中的「GitHub Copilot」后端。它在本机拉起 copilot
// CLI（用 --output-format json 的 -p 一次性管道模式），把进程打印到 stdout 的 JSONL
// 事件流逐行解析，翻译成系统统一的 Message 事件（thinking / text / 工具调用与结果 /
// 日志 / 状态），并在进程结束后汇总出 Result（最终状态、最终文本、token 用量、会话 ID）。
//
// 数据流：Execute 拿到用户 prompt → 拼出命令行参数并启动进程 → 后台 goroutine 逐行
// 读 stdout，每行是一个 JSON 信封事件（copilotEvent，type 为点分事件名），交给
// handleCopilotEvent 一边更新 copilotEventState、一边产出零到多条 Message 经 msgCh 推给
// 调用方（daemon/前端做流式展示）→ 进程退出后把最终解析结果放进 resCh。
//
// 事件格式约定：Copilot CLI v1.0.28+ 在 --output-format json 下输出 JSONL，事件形如
// { "type": "assistant.message", "data": {...}, "id": "...", ... }，最后一行是 CLI 合成的
// "result" 事件（sessionId/exitCode 在顶层）。各事件载荷类型见本文件后半部的 copilotXxx 系列。

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"time"
)

// copilotBackend 实现 Backend 接口：通过拉起 GitHub Copilot CLI（带
// --output-format json）并解析它输出的 JSONL 事件流来驱动一次 Copilot 会话。
//
// v1 集成用的是 -p（pipe 管道）模式，这是 Copilot CLI 面向自动化/CI 的稳定通道；
// prompt 作为命令行参数传入（不是 stdin）。进程在 stdout 上按行输出 JSON 事件，
// 信封格式为 { "type": "点分事件名", "data": {...}, ... }。
type copilotBackend struct {
	cfg Config
}

// copilotEventState 累积解析 JSONL 事件流过程中的可变状态。生产路径（Execute）与测试
// 都通过 handleCopilotEvent 共享同一份解析逻辑，避免两处重复。
type copilotEventState struct {
	// output 保存的是最近一次「完整」的 assistant 回合内容，而不是把每个回合拼起来：
	// Result.Output 要求是「后端挑选出的最终面向用户的输出」（见 agent.go）。一个会调
	// 工具的回合里，前面几轮文本只是围绕工作的叙述，不是交付物（见 GH #6006）。但每个
	// 回合的文本仍会作为 MessageText 事件发出去，完整进到 transcript 里。
	output strings.Builder
	// pendingDelta 缓冲当前正在流式输出的回合片段。当该回合权威的 assistant.message
	// 事件落地时会被清空，因此它非空只可能发生在「进程中途死掉」这一种场景——此时
	// 残缺的当前回合比上一回合的完整文本更值得当结果，partial 胜过 complete。
	pendingDelta strings.Builder
	sessionID    string
	activeModel  string
	finalStatus  string
	finalError   string

	// token 用量最多会从三类不同事件里来，所以各自独立累加、最后在 resolveUsage 里
	// 只挑一个口径：
	//
	//	session.shutdown  按模型汇总的整个会话总量，字段最全
	//	assistant.usage   每次模型调用的完整明细
	//	assistant.message 只有 outputTokens——老版本 CLI 才有的遗留字段
	//
	// 三者描述的是同一批 token，绝不能相加，否则会重复计费。
	callUsage     map[string]TokenUsage
	msgUsage      map[string]TokenUsage
	shutdownUsage map[string]TokenUsage
	// resumed 标记这是一次续跑（接着已有 Copilot 会话继续）。续跑会使得
	// session.shutdown 的会话级总量不可信，原因见 resolveUsage。
	resumed bool
}

// resolveUsage 从本次运行可能出现的多个用量来源里挑出「最完整的那一个」返回。
//
// 全新会话时，session.shutdown 是 CLI 对整场会话（在这里就等于本次运行）的最终结算，
// 是最完整的来源；个别模型调用没上报不会连累它。但续跑（resumed）时它不可用：CLI 会
// 从 checkpoint 恢复累加器，于是它把之前每一轮的 token 都带上了，若再报一遍等于把同一
// 批 token 按轮重复计费。少报好过双报，所以续跑会跳过它、继续往下看。
//
// assistant.usage 是「按模型调用」计量的，无论是否续跑都能精确覆盖本次运行。
// assistant.message 排最后：它只带 outputTokens，若让它压过带完整明细的来源，
// 输入 token 与缓存档位就会被静默丢掉。
func (st *copilotEventState) resolveUsage() map[string]TokenUsage {
	if !st.resumed && hasTokens(st.shutdownUsage) {
		return st.shutdownUsage
	}
	if hasTokens(st.callUsage) {
		return st.callUsage
	}
	if hasTokens(st.msgUsage) {
		return st.msgUsage
	}
	return map[string]TokenUsage{}
}

// hasTokens 报告一个用量来源里是否带有真实数字。光有「键存在」不够：assistant.usage
// 里每个 token 字段在上游都是可选的，一个只报了模型名、没有数字的用量事件若被当成
// 「已填充」，就会把真正有 token 的来源给遮住。
func hasTokens(usage map[string]TokenUsage) bool {
	for _, u := range usage {
		if u.InputTokens > 0 || u.OutputTokens > 0 || u.CacheReadTokens > 0 || u.CacheWriteTokens > 0 {
			return true
		}
	}
	return false
}

// addUsage 把一条用量记录按 model 归并进 dst；完全不携带 token 的记录会被忽略，
// 避免一个空记录凭空制造出条目。
//
// Copilot 上报时把缓存 token 也算在 inputTokens 里面（CLI 自己的事件日志 reduce 逻辑
// 是用 inputTokens - cacheRead - cacheWrite 推出未命中缓存的输入量），而 TokenUsage.
// InputTokens 语义是「按输入价计费的未命中缓存部分」。所以这里要先减掉再存，否则缓存
// 档会被重复计费。reasoningTokens 被有意忽略：它们已经含在 outputTokens 里了。
func addUsage(dst map[string]TokenUsage, model string, input, output, cacheRead, cacheWrite int64) {
	cacheRead = nonNegativeTokens(cacheRead)
	cacheWrite = nonNegativeTokens(cacheWrite)
	output = nonNegativeTokens(output)
	uncachedInput := nonNegativeTokens(input - cacheRead - cacheWrite)
	if uncachedInput == 0 && output == 0 && cacheRead == 0 && cacheWrite == 0 {
		return
	}
	u := dst[model]
	u.InputTokens += uncachedInput
	u.OutputTokens += output
	u.CacheReadTokens += cacheRead
	u.CacheWriteTokens += cacheWrite
	dst[model] = u
}

func nonNegativeTokens(n int64) int64 {
	if n < 0 {
		return 0
	}
	return n
}

// finalOutput 是最终要填进 Result.Output 的内容：取最后一个「完整」的 assistant
// 回合；若进程是在某回合流式输出中途死掉的，则返回那段残缺的流式文本。
func (st *copilotEventState) finalOutput() string {
	if st.pendingDelta.Len() > 0 {
		return st.pendingDelta.String()
	}
	return st.output.String()
}

// newCopilotEventState 构造事件状态机：seedModel 是会话首个回合的模型名兜底值
// （真实模型会在后续事件里覆盖它），resumed 表示这次运行是否续跑已有会话。
func newCopilotEventState(seedModel string, resumed bool) *copilotEventState {
	return &copilotEventState{
		activeModel:   seedModel,
		finalStatus:   "completed",
		callUsage:     make(map[string]TokenUsage),
		msgUsage:      make(map[string]TokenUsage),
		shutdownUsage: make(map[string]TokenUsage),
		resumed:       resumed,
	}
}

// handleCopilotEvent 处理单条已解析的 copilotEvent：更新事件状态机，并返回需要对外
// 发送的零到多条 Message。把它抽成独立函数是为了让测试也能走完全相同的分支逻辑，
// 而不必复制一份 switch 主体。
func handleCopilotEvent(evt copilotEvent, st *copilotEventState) []Message {
	var msgs []Message

	switch evt.Type {
	case "session.start":
		var ss copilotSessionStart
		if err := json.Unmarshal(evt.Data, &ss); err == nil {
			if ss.SelectedModel != "" {
				st.activeModel = ss.SelectedModel
			}
			// 这里也顺手从 session.start 里抓 sessionId：那个合成的 "result" 事件可能
			// 根本不会来（超时、取消、崩溃，或在 result 之前先来了 session.error）。
			// 不抓的话 daemon 上报的 SessionID 就是空的，聊天会话的「继续」指针会漂到
			// 一个过期回合上。若之后 result 真来了，仍以 result 为准覆盖。
			if ss.SessionID != "" {
				st.sessionID = ss.SessionID
			}
		}

	case "assistant.message_delta":
		var delta copilotMessageDelta
		if err := json.Unmarshal(evt.Data, &delta); err == nil && delta.DeltaContent != "" {
			// 先把增量文本缓冲一份，作为纵深防御：万一进程在本次回合的 assistant.message
			// 事件到达前就被杀掉，我们手里仍有这段文本可用。
			st.pendingDelta.WriteString(delta.DeltaContent)
			msgs = append(msgs, Message{Type: MessageText, Content: delta.DeltaContent})
		}

	case "assistant.message":
		var msg copilotAssistantMessage
		if err := json.Unmarshal(evt.Data, &msg); err != nil {
			return nil
		}
		// assistant.message 携带的是该回合完整内容，会同时取代两样东西：流式输出的增量，
		// 以及此前回合存下的 output。比如一个回合先叙述、再调工具、最后才回答，交付物应
		// 是「回答」，而不是把三者拼在一起。
		if msg.Content != "" {
			st.output.Reset()
			st.output.WriteString(msg.Content)
		}
		// 无条件清空增量缓冲——本事件本身就是回合边界。纯工具回合的 content 是空串
		// （真正的内容都在下面的 ToolRequests 里），若不清掉残留的增量，进程恰好在下一
		// 个回合流式中途死掉时，这段残留会被缝进下一回合的残缺文本里。
		st.pendingDelta.Reset()
		// 消息里带着产出它的模型名。没有它，第一个回合的 token 会被记到 seed model 名下
		// ——没配置模型时就是字面字符串 "copilot"，而价格表里没有这个模型，于是明明报了
		// token 的运行估算出来却是 $0.00。
		if msg.Model != "" {
			st.activeModel = msg.Model
		}
		if msg.ReasoningText != "" {
			msgs = append(msgs, Message{Type: MessageThinking, Content: msg.ReasoningText})
		}
		if msg.OutputTokens > 0 {
			addUsage(st.msgUsage, st.activeModel, 0, msg.OutputTokens, 0, 0)
		}
		for _, tr := range msg.ToolRequests {
			var input map[string]any
			if tr.Arguments != nil {
				_ = json.Unmarshal(tr.Arguments, &input)
			}
			msgs = append(msgs, Message{
				Type:   MessageToolUse,
				Tool:   tr.Name,
				CallID: tr.ToolCallID,
				Input:  input,
			})
		}

	case "assistant.usage":
		// 每次模型 API 调用一条记录，带完整 token 明细；这是唯一会报输入 token 与缓存
		// token 的事件。
		var u copilotUsageData
		if err := json.Unmarshal(evt.Data, &u); err != nil {
			return nil
		}
		model := u.Model
		// CLI 在认不出模型名时写的是 "unknown"。这里不另开一行假的用量记录，而是沿用
		// 会话当前已解析出的模型名。
		if model == "" || model == "unknown" {
			model = st.activeModel
		} else {
			st.activeModel = model
		}
		addUsage(st.callUsage, model, u.InputTokens, u.OutputTokens, u.CacheReadTokens, u.CacheWriteTokens)

	case "session.shutdown":
		// 按模型汇总的整场会话总量，随会话拆除时发一条。
		var sd copilotShutdownData
		if err := json.Unmarshal(evt.Data, &sd); err != nil {
			return nil
		}
		for model, m := range sd.ModelMetrics {
			if model == "" {
				model = st.activeModel
			}
			addUsage(st.shutdownUsage, model, m.Usage.InputTokens, m.Usage.OutputTokens, m.Usage.CacheReadTokens, m.Usage.CacheWriteTokens)
		}

	case "assistant.reasoning", "assistant.reasoning_delta":
		// 流式思考内容——可能是完整一段，也可能是增量片段。
		var r copilotReasoning
		if err := json.Unmarshal(evt.Data, &r); err == nil {
			text := r.Content
			if text == "" {
				text = r.DeltaContent
			}
			if text != "" {
				msgs = append(msgs, Message{Type: MessageThinking, Content: text})
			}
		}

	case "tool.execution_complete":
		var tc copilotToolExecComplete
		if err := json.Unmarshal(evt.Data, &tc); err != nil {
			return nil
		}
		if tc.Model != "" {
			st.activeModel = tc.Model
		}
		resultContent := ""
		if tc.Success && tc.Result != nil {
			resultContent = tc.Result.Content
		} else if !tc.Success {
			// 工具执行失败：优先把错误信息作为结果返回，让调用方（模型侧）能看到失败原因。
			if tc.Error != nil {
				resultContent = "Error: " + tc.Error.Message
			} else if tc.Result != nil {
				resultContent = tc.Result.Content
			}
		}
		msgs = append(msgs, Message{
			Type:   MessageToolResult,
			CallID: tc.ToolCallID,
			Output: resultContent,
		})

	case "assistant.turn_start":
		msgs = append(msgs, Message{Type: MessageStatus, Status: "running"})

	case "session.error":
		var se copilotSessionError
		if err := json.Unmarshal(evt.Data, &se); err == nil {
			st.finalStatus = "failed"
			st.finalError = se.Message
			msgs = append(msgs, Message{Type: MessageLog, Level: "error", Content: se.Message})
		}

	case "session.warning":
		var sw copilotSessionWarning
		if err := json.Unmarshal(evt.Data, &sw); err == nil {
			msgs = append(msgs, Message{Type: MessageLog, Level: "warn", Content: sw.Message})
		}

	case "result":
		// 合成的收尾事件：CLI 正常结束时会发它，携带权威的会话 ID 与退出码。
		// 退出码非 0 意味着整场运行失败，把退出码并进错误信息；但若此前已有更具体
		// 的错误（如 session.error）则保留原信息、只追加退出码后缀。
		if evt.SessionID != "" {
			st.sessionID = evt.SessionID
		}
		if evt.ExitCode != 0 {
			st.finalStatus = "failed"
			st.finalError = withCopilotExitCode(st.finalError, evt.ExitCode)
		}
	}

	return msgs
}

// withCopilotExitCode 把「copilot exited with code N」这段退出信息并入错误消息 msg：
// 若消息为空直接以退出信息为准；若已含同样内容则不重复追加；否则用分号拼接在末尾。
func withCopilotExitCode(msg string, exitCode int) string {
	exitMsg := fmt.Sprintf("copilot exited with code %d", exitCode)
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return exitMsg
	}
	if strings.Contains(msg, exitMsg) {
		return msg
	}
	return msg + "; " + exitMsg
}

// Execute 是 Backend 接口的入口：拿一条用户 prompt 起一个 copilot 进程跑到结束。
//
// 流程：先确认可执行文件存在 → 构造带超时的运行上下文 → 拼命令行参数（-p 传 prompt、
// --output-format json）→ 启动进程并接管它的 stdout（按 JSONL 流式读事件）/stderr（只
// 保留尾部）→ 主 goroutine 循环解析事件、翻译成 Message 经 msgCh 发出 → 等进程退出后
// 依据退出原因（超时/取消/正常结束）敲定 finalStatus，汇总 token 用量，把 Session/Result
// 交给调用方。Result 与 Message 通过两个 channel 返回，支持调用方边等边收流式事件。
func (b *copilotBackend) Execute(ctx context.Context, prompt string, opts ExecOptions) (*Session, error) {
	execName := b.cfg.ExecutablePath
	if execName == "" {
		execName = "copilot"
	}
	lookedUp, err := exec.LookPath(execName)
	if err != nil {
		return nil, fmt.Errorf("copilot executable not found at %q: %w", execName, err)
	}

	timeout := opts.Timeout
	runCtx, cancel := runContext(ctx, timeout)

	args := buildCopilotArgs(prompt, opts, b.cfg.Logger)
	cmd, _, _ := b.cfg.commandAt(execName).execVia(runCtx, chooseCopilotInvocation, lookedUp, args, b.cfg.Logger)
	hideAgentWindow(cmd)
	b.cfg.logAgentCommand(cmd, newAgentCommandLogArgs(args))
	cmd.WaitDelay = 10 * time.Second
	if opts.Cwd != "" {
		cmd.Dir = opts.Cwd
	}
	cmd.Env = buildEnv(b.cfg.Env)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("copilot stdout pipe: %w", err)
	}
	stderrBuf := newStderrTail(newLogWriter(b.cfg.Logger, "[copilot:stderr] "), agentStderrTailBytes)
	cmd.Stderr = stderrBuf

	if err := startOwnedProcessTree(cmd, b.cfg.Logger); err != nil {
		cancel()
		return nil, fmt.Errorf("start copilot: %w", err)
	}

	b.cfg.Logger.Info("copilot started", "pid", cmd.Process.Pid, "cwd", opts.Cwd, "model", opts.Model)

	msgCh := make(chan Message, 256)
	resCh := make(chan Result, 1)

	go func() {
		defer cancel()
		defer close(msgCh)
		defer close(resCh)

		startTime := time.Now()
		seedModel := opts.Model
		if seedModel == "" {
			seedModel = "copilot"
		}
		st := newCopilotEventState(seedModel, opts.ResumeSessionID != "")

		go func() {
			<-runCtx.Done()
			_ = stdout.Close()
		}()

		scanner := newAgentStreamScanner(stdout)

		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}

			var evt copilotEvent
			if err := json.Unmarshal([]byte(line), &evt); err != nil {
				slog.Warn("copilot event parse failed", "err", err, "line", line)
				continue
			}

			for _, m := range handleCopilotEvent(evt, st) {
				trySend(msgCh, m)
			}
		}
		if err := scanner.Err(); err != nil {
			slog.Warn("copilot stdout scanner error", "err", err)
		}

		exitErr := cmd.Wait()
		releaseProcessGroup(cmd)
		duration := time.Since(startTime)

		if runCtx.Err() == context.DeadlineExceeded {
			st.finalStatus = "timeout"
			st.finalError = fmt.Sprintf("copilot timed out after %s", timeout)
		} else if runCtx.Err() == context.Canceled {
			st.finalStatus = "aborted"
			st.finalError = "execution cancelled"
		} else if exitErr != nil && st.finalStatus == "completed" {
			st.finalStatus = "failed"
			st.finalError = fmt.Sprintf("copilot exited with error: %v", exitErr)
		}
		if st.finalError != "" {
			st.finalError = withAgentStderr(st.finalError, "copilot", stderrBuf.Tail())
		}

		b.cfg.Logger.Info("copilot finished", "pid", cmd.Process.Pid, "status", st.finalStatus, "duration", duration.Round(time.Millisecond).String())

		usage := st.resolveUsage()
		// 有产出却没有任何 token 上报，是一个无声的计费漏洞：daemon 会跳过空用量的
		// 上报，于是下游任何地方都看不到它。Copilot CLI 1.0.77 目前就是这状态（见
		// copilotUsageData 上的说明），而且一直没被发现，直到有用户反馈——所以这里打
		// 一条日志，让下一次回归光看 daemon 日志就能露头。
		if len(usage) == 0 && st.finalStatus == "completed" {
			b.cfg.Logger.Warn("copilot reported no token usage",
				"session", st.sessionID,
				"model", st.activeModel,
				"hint", "Copilot CLI filters assistant.usage and session.shutdown out of --output-format json; only assistant.message.outputTokens remains, and newer CLIs no longer populate it")
		}

		resCh <- Result{
			Status:     st.finalStatus,
			Output:     st.finalOutput(),
			Error:      st.finalError,
			DurationMs: duration.Milliseconds(),
			SessionID:  st.sessionID,
			Usage:      usage,
		}
	}()

	return &Session{Messages: msgCh, Result: resCh}, nil
}

// ── Copilot CLI JSONL 事件类型 ──
//
// Copilot CLI v1.0.28+ 配合 --output-format json 会在 stdout 上输出 JSONL。
// 每行是一个 JSON 对象，结构如下：
//
//	{ "type": "点分事件名", "data": {...}, "id": "...",
//	  "timestamp": "...", "parentId": "...", "ephemeral": bool }
//
// 最后一行是程序合成的 "result" 事件，字段放在顶层：
//
//	{ "type": "result", "sessionId": "...", "exitCode": 0, "usage": {...} }

// copilotEvent 是所有 Copilot JSONL 事件的通用信封。
type copilotEvent struct {
	Type      string          `json:"type"`
	Data      json.RawMessage `json:"data,omitempty"`
	ID        string          `json:"id,omitempty"`
	Timestamp string          `json:"timestamp,omitempty"`
	ParentID  string          `json:"parentId,omitempty"`
	Ephemeral bool            `json:"ephemeral,omitempty"`

	// 下面这些只出现在合成的 "result" 事件上（顶层字段）。
	SessionID string              `json:"sessionId,omitempty"`
	ExitCode  int                 `json:"exitCode,omitempty"`
	Usage     *copilotResultUsage `json:"usage,omitempty"`
}

// copilotSessionStart 是 "session.start" 的 data 载荷。
type copilotSessionStart struct {
	SessionID     string `json:"sessionId"`
	SelectedModel string `json:"selectedModel"`
}

// copilotAssistantMessage 是 "assistant.message" 的 data 载荷。
//
// OutputTokens 在 CLI 自己的事件 schema 里就是可选的，并且是唯一能活着流到
// `--output-format json` 输出里的 token 字段——详见 copilotUsageData 上被过滤掉的说明。
type copilotAssistantMessage struct {
	MessageID     string               `json:"messageId"`
	Model         string               `json:"model"`
	Content       string               `json:"content"`
	ToolRequests  []copilotToolRequest `json:"toolRequests"`
	OutputTokens  int64                `json:"outputTokens"`
	InteractionID string               `json:"interactionId"`
	ReasoningText string               `json:"reasoningText,omitempty"`
}

// copilotUsageData 是 "assistant.usage" 的 data 载荷：每次模型 API 调用一条记录，
// 携带 CLI 能产出的唯一一份完整 token 明细。
//
// InputTokens 是「包含」缓存档位的；addUsage 里会把它们减回去。载荷里还带
// reasoningTokens，这里有意不解析：它们是 outputTokens 的子集，算上就会把思考部分
// 重复计费。
//
// 注意（Copilot CLI 1.0.77）：CLI 的 JSONL 写入器会在写 stdout 前丢弃一组固定的事件
// 类型，"assistant.usage" 和 "session.shutdown" 都在名单上——所以当前版本的 CLI 下两者
// 都到不了我们手里，Copilot 运行干脆一份 token 都没有。这里的解析逻辑依然保留，是因为
// 更老的 CLI、以及任何将来不再过滤这些事件的版本，恰恰是通过这些事件送出真实数字的。
// 我们已解析的 `result` 事件带的是 premiumRequests 和时长，不含 token 计数。
type copilotUsageData struct {
	Model            string `json:"model"`
	InputTokens      int64  `json:"inputTokens"`
	OutputTokens     int64  `json:"outputTokens"`
	CacheReadTokens  int64  `json:"cacheReadTokens"`
	CacheWriteTokens int64  `json:"cacheWriteTokens"`
}

// copilotShutdownData 是 "session.shutdown" 的 data 载荷：按模型键控的整场会话总量。
type copilotShutdownData struct {
	ModelMetrics map[string]copilotShutdownModelMetric `json:"modelMetrics"`
}

// copilotShutdownModelMetric 是 shutdown 载荷里每个模型条目下再包一层的用量结构。
type copilotShutdownModelMetric struct {
	Usage copilotUsageData `json:"usage"`
}

// copilotToolRequest 是 assistant.message 里的一次工具调用。
type copilotToolRequest struct {
	ToolCallID       string          `json:"toolCallId"`
	Name             string          `json:"name"`
	Arguments        json.RawMessage `json:"arguments"`
	Type             string          `json:"type"`
	IntentionSummary string          `json:"intentionSummary,omitempty"`
}

// copilotMessageDelta 是 "assistant.message_delta" 的 data 载荷（流式增量）。
type copilotMessageDelta struct {
	MessageID    string `json:"messageId"`
	DeltaContent string `json:"deltaContent"`
}

// copilotToolExecComplete 是 "tool.execution_complete" 的 data 载荷：工具执行完毕的回报，
// 成功时带 Result，失败时带 Error。
type copilotToolExecComplete struct {
	ToolCallID    string             `json:"toolCallId"`
	Model         string             `json:"model"`
	InteractionID string             `json:"interactionId"`
	Success       bool               `json:"success"`
	Result        *copilotToolResult `json:"result,omitempty"`
	Error         *copilotToolError  `json:"error,omitempty"`
}

// copilotToolResult 是一次成功工具调用的返回内容；DetailedContent 为可选的长格式说明。
type copilotToolResult struct {
	Content         string `json:"content"`
	DetailedContent string `json:"detailedContent,omitempty"`
}

// copilotToolError 是一次失败工具调用的错误信息。
type copilotToolError struct {
	Message string `json:"message"`
}

// copilotReasoning 是 "assistant.reasoning" / "assistant.reasoning_delta" 的 data 载荷：
// Content 是完整一段思考、DeltaContent 是流式增量，二者二选一。
type copilotReasoning struct {
	Content      string `json:"content,omitempty"`
	DeltaContent string `json:"deltaContent,omitempty"`
}

// copilotSessionError 是 "session.error" 的 data 载荷。
type copilotSessionError struct {
	ErrorType string `json:"errorType"`
	Message   string `json:"message"`
}

// copilotSessionWarning 是 "session.warning" 的 data 载荷。
type copilotSessionWarning struct {
	WarningType string `json:"warningType"`
	Message     string `json:"message"`
}

// copilotResultUsage 是收尾 "result" 事件上的用量信息：只含请求档位与耗时，没有 token 计数。
type copilotResultUsage struct {
	PremiumRequests    float64             `json:"premiumRequests"`
	TotalAPIDurationMs int64               `json:"totalApiDurationMs"`
	SessionDurationMs  int64               `json:"sessionDurationMs"`
	CodeChanges        *copilotCodeChanges `json:"codeChanges,omitempty"`
}

// copilotCodeChanges 是 result 事件里关于本次改动的统计。
type copilotCodeChanges struct {
	LinesAdded    int      `json:"linesAdded"`
	LinesRemoved  int      `json:"linesRemoved"`
	FilesModified []string `json:"filesModified"`
}

// ── 参数构建 ──

// copilotBlockedArgs 是 daemon 写死的参数清单，用户配置的 custom_args 不得覆盖它们——
// 否则用户可能把模式、权限开关这些安全关键的参数改掉。
var copilotBlockedArgs = map[string]blockedArgMode{
	"-p":                blockedWithValue,
	"--output-format":   blockedWithValue,
	"--allow-all":       blockedStandalone, // 放开全部工具 + 路径 + URL 的权限
	"--allow-all-tools": blockedStandalone,
	"--allow-all-paths": blockedStandalone,
	"--allow-all-urls":  blockedStandalone,
	"--yolo":            blockedStandalone,
	"--no-ask-user":     blockedStandalone,
	"--resume":          blockedWithValue,  // 会话续跑走 ExecOptions.ResumeSessionID 统一管理
	"--acp":             blockedStandalone, // 防止切到 ACP 模式
}

// buildCopilotArgs 拼出一次「跑完即退」的 copilot 调用的 argv。
//
// 目标是：
//
//	copilot -p "<prompt>" --output-format json --allow-all --no-ask-user
//	        [--resume <session-id>] [--model <model>]
//
// 其中自定义参数需经 filterCustomArgs 过滤，剔除上面被 daemon 锁定不允许覆盖的项。
func buildCopilotArgs(prompt string, opts ExecOptions, logger *slog.Logger) []string {
	args := []string{
		"-p", prompt,
		"--output-format", "json",
		"--allow-all", // 工具 + 路径 + URL 全放行 —— 完整无人值守模式
		"--no-ask-user",
	}
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}
	if opts.ResumeSessionID != "" {
		args = append(args, "--resume", opts.ResumeSessionID)
	}
	args = append(args, filterCustomArgs(opts.CustomArgs, copilotBlockedArgs, logger)...)
	return args
}
