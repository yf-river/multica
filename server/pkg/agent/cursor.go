package agent

// 文件职责：本文件是 Cursor Agent CLI（cursor-agent）的适配后端（Backend 实现），负责把一次
// 任务执行变成“spawn `cursor-agent -p --output-format stream-json` + 逐行读 stdout JSONL 事件
// 流”，再把事件翻译成上层统一的 Message/Result。
//
// 与外部 CLI 的交互：Cursor 的 stream-json 协议与 Claude Code 的相似——事件是“按换行分隔的
// JSON 对象”，每个对象带 "type" 字段；但事件集不同，至少出现 system/assistant/thinking/
// tool_call/tool_use/tool_result/result/error/text/step_finish 等类型。prompt 经 stdin 送达
// （cursor-agent 在 stdin 非 TTY 且无位置 prompt 时读到 EOF 当 prompt），stdin 关闭即表示
// “prompt 结束”。
//
// 关键机制与坑：reasoning 是独立事件流（`thinking` 按 delta 碎片推、completed 收尾），不是
// assistant 里的内容块；工具调用边界由 tool_call 的 started/completed 两个子类型定义，其它子
// 类型绝不合成结果，否则会过早扣减 daemon 的在途工具数、把仍在跑的长工具从工具看门狗换到更短
// 的空闲看门狗上被误杀（MUL-5231）。解析器对不认识的顶层事件类型只计数上报、绝不猜测
// （MUL-5434）；识别到 result 事件即视为协议边界（Cursor 可能会留着 worker 进程不退出）。
//
// 内部组织：先是 backend 的 Execute（主读事件循环 + 失败诊断），中间是事件子类型/顶层类型的
// 观察与统计辅助，然后是从事件里抽文本/工具/用量的处理函数与“Cursor stream-json 类型”，
// 最后是命令行构造（buildCursorArgs）等助手。
import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// cursorBackend 实现 Backend 接口：以 `--output-format stream-json` spawn Cursor Agent CLI
// （cursor-agent）并解析其 JSONL 事件流。协议与 Claude Code 的 stream-json 格式相似：事件是带
// "type" 字段、按换行分隔的 JSON 对象。
type cursorBackend struct {
	cfg Config
}

// Execute 启动一次一次性 cursor-agent 调用并返回流式消息与最终结果。
//
// 流程：解析可执行文件 → 构造 argv（buildCursorArgs）→ 经 chooseCursorInvocation 解析真实
// 调用形式后 spawn → 独立 goroutine 写 prompt（写满即关 stdin 表示结束）→ 主循环逐行读事件并
// 翻译 → 进程结束/读到 result 后归并出最终 Result。
func (b *cursorBackend) Execute(ctx context.Context, prompt string, opts ExecOptions) (*Session, error) {
	execName := b.cfg.ExecutablePath
	if execName == "" {
		execName = "cursor-agent"
	}
	lookedUp, err := exec.LookPath(execName)
	if err != nil {
		return nil, fmt.Errorf("cursor-agent executable not found at %q: %w", execName, err)
	}

	timeout := opts.Timeout
	runCtx, cancel := runContext(ctx, timeout)

	args := buildCursorArgs(opts, b.cfg.Logger)
	cmd, _, _ := b.cfg.commandAt(execName).execVia(runCtx, chooseCursorInvocation, lookedUp, args, b.cfg.Logger)
	hideAgentWindow(cmd)
	b.cfg.logAgentCommand(cmd, newAgentCommandLogArgs(args))
	cmd.WaitDelay = 500 * time.Millisecond
	if opts.Cwd != "" {
		cmd.Dir = opts.Cwd
	}
	cmd.Env = buildEnv(b.cfg.Env)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("cursor stdout pipe: %w", err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("cursor stdin pipe: %w", err)
	}
	var closeStdinOnce sync.Once
	closeStdin := func() { closeStdinOnce.Do(func() { _ = stdin.Close() }) }
	stderrBuf := newStderrTail(newLogWriter(b.cfg.Logger, "[cursor:stderr] "), agentStderrTailBytes)
	cmd.Stderr = stderrBuf

	if err := startOwnedProcessTree(cmd, b.cfg.Logger); err != nil {
		closeStdin()
		cancel()
		return nil, fmt.Errorf("start cursor-agent: %w", err)
	}

	b.cfg.Logger.Info("cursor-agent started", "pid", cmd.Process.Pid, "cwd", opts.Cwd, "model", opts.Model)

	msgCh := make(chan Message, 256)
	resCh := make(chan Result, 1)

	// prompt 经 stdin 送达（见 buildCursorArgs）。要放在独立 goroutine 里写，免得和下面的
	// stdout 读取方互相死锁：比 OS 管道缓冲（~64 KiB）更大的 prompt 会在写到一半时阻塞，直到
	// 子进程排空；而只要我们没在读它的 stdout，它就没法排空。关闭 stdin 就是“prompt 结束”的
	// 信号——cursor-agent 一直读到 EOF——所以成功和失败两条路径上都要关。
	writeErrCh := make(chan error, 1)
	go func() {
		_, err := io.WriteString(stdin, prompt)
		closeStdin()
		writeErrCh <- err
	}()

	go func() {
		defer cancel()
		defer close(msgCh)
		defer close(resCh)

		// 上下文取消时关闭 stdout，让 scanner.Scan() 能解除阻塞。同时关 stdin，释放一个仍被
		// 满管道卡住的 prompt 写（例如子进程在排空前就死了），这样那个 goroutine 不会泄漏。
		go func() {
			<-runCtx.Done()
			closeStdin()
			_ = stdout.Close()
		}()

		startTime := time.Now()
		configuredModel := strings.TrimSpace(opts.Model)
		var output strings.Builder
		var sessionID string
		finalStatus := "completed"
		var finalError string
		var protocolError string
		resultSeen := false
		resultIsError := false
		resultBytes := 0
		eventCount := 0
		invalidEventCount := 0
		assistantEventCount := 0
		toolUseCount := 0
		// unhandledSubtypeCount 记录“thinking/tool_call 事件里我们不认识其 subtype 的事件数”。
		// 它们被忽略（绝不合成消息），但会一次性、以“有界且不含内容”的诊断形式浮出水面，让上游
		// 协议的新增可见而不是悄然消失。
		unhandledSubtypeCount := 0
		// unhandledTypes 记录下面 switch 未处理的顶层事件类型。见 cursorUnhandledTypeTally：
		// 它让“被丢掉的事件”变得可观测，并说明计数能/不能各自证明什么（MUL-5434）。
		var unhandledTypes cursorUnhandledTypeTally
		lastEventType := "none"
		// assistantBytes 只统计模型产出的流式文本字节数。它要与 `output` 分开记录，因为 result
		// 事件也会写进 `output`——若合一，一个“助手完全没流式输出”的运行上报的 last_assistant
		// _bytes 会等于 result_bytes，恰好掩盖了区分这两种情况所需的信号。
		assistantBytes := 0
		// stepUsage 从 "step_finish" 事件按步累计 token 数。resultUsage 持有 "result" 事件里
		// 权威的会话总量。若 result 事件带用量，就只用 resultUsage；否则退回 stepUsage。
		stepUsage := make(map[string]TokenUsage)
		resultUsage := make(map[string]TokenUsage)
		hasResultUsage := false
		var thinking cursorThinkingStream

		scanner := newAgentStreamScanner(stdout)

		for scanner.Scan() {
			raw := scanner.Text()
			line := normalizeCursorStreamLine(raw)
			if line == "" {
				continue
			}

			var evt cursorStreamEvent
			if err := json.Unmarshal([]byte(line), &evt); err != nil {
				invalidEventCount++
				continue
			}
			eventCount++
			lastEventType = observedCursorEventType(evt.Type)

			if sid := evt.readSessionID(); sid != "" {
				sessionID = sid
			}

			switch evt.Type {
			case "system":
				if evt.Subtype == "init" {
					trySend(msgCh, Message{Type: MessageStatus, Status: "running"})
				}
				if evt.Subtype == "error" {
					errMsg := cursorErrorText(&evt)
					if errMsg != "" {
						protocolError = errMsg
						trySend(msgCh, Message{Type: MessageError, Content: errMsg})
					}
				}

			case "assistant":
				assistantEventCount++
				assistantBytes += b.handleCursorAssistant(&evt, msgCh, &output)

			case "thinking":
				// reasoning 是顶层事件流、按 delta 推送，而不是 assistant 消息里的内容块。
				// 显式匹配 subtype：只有 `delta` 携带 reasoning 内容（随到随转发，让 daemon 的
				// 500ms 冲刷能在运行中途显示出来），`completed` 收尾该块。未知 subtype 不会被并进
				// reasoning——静默吸收上游新增正是本修复要消灭的失败模式（MUL-5231）。
				switch evt.Subtype {
				case "delta":
					if content := thinking.delta(evt.Text); content != "" {
						trySend(msgCh, Message{Type: MessageThinking, Content: content})
					}
				case "completed":
					thinking.complete()
				default:
					unhandledSubtypeCount++
				}

			case "tool_call":
				// 只有定义调用边界的两个 subtype 才驱动记录：`started` 打开它、`completed`
				// 关闭它。非终态（例如将来的 `progress`）或缺失 subtype 绝不能合成一个结果——
				// 那会过早扣减 daemon 的在途工具数，把一个仍在运行的“长工具”从工具看门狗换到
				// 更短的空闲看门狗上，可能被误判为卡死而强制停止（MUL-5231 review）。
				switch evt.Subtype {
				case "started":
					call := parseCursorToolCall(&evt)
					toolUseCount++
					trySend(msgCh, Message{
						Type:   MessageToolUse,
						Tool:   call.Name,
						CallID: call.CallID,
						Input:  call.Input,
					})
				case "completed":
					call := parseCursorToolCall(&evt)
					trySend(msgCh, Message{
						Type:   MessageToolResult,
						Tool:   call.Name,
						CallID: call.CallID,
						Output: call.Result,
					})
				default:
					unhandledSubtypeCount++
				}

			case "tool_use":
				toolUseCount++
				var params map[string]any
				if evt.Parameters != nil {
					_ = json.Unmarshal(evt.Parameters, &params)
				}
				trySend(msgCh, Message{
					Type:   MessageToolUse,
					Tool:   evt.ToolName,
					CallID: evt.ToolID,
					Input:  params,
				})

			case "tool_result":
				trySend(msgCh, Message{
					Type:   MessageToolResult,
					CallID: evt.ToolID,
					Output: evt.Output,
				})

			case "result":
				resultSeen = true
				if evt.IsError || evt.Subtype == "error" {
					finalStatus = "failed"
					finalError = cursorErrorText(&evt)
					resultIsError = true
				}
				resultBytes = len(evt.ResultText)
				if evt.ResultText != "" && output.Len() == 0 {
					output.WriteString(evt.ResultText)
					trySend(msgCh, Message{Type: MessageText, Content: evt.ResultText})
				}
				b.accumulateResultUsage(resultUsage, &evt, configuredModel)
				if evt.hasResultUsage() {
					hasResultUsage = true
				}
				// 当前 Cursor Agent 版本可能发完终态 result 事件后仍留着一个 worker 进程活着。
				// 把 result 当作协议边界，好让 daemon 能上报“已完成”。
				cancel()

			case "error":
				errMsg := cursorErrorText(&evt)
				if errMsg != "" {
					protocolError = errMsg
				}
				trySend(msgCh, Message{Type: MessageError, Content: errMsg})

			case "text":
				if evt.Part != nil {
					var part cursorTextPart
					_ = json.Unmarshal(evt.Part, &part)
					if part.Text != "" {
						output.WriteString(part.Text)
						assistantBytes += len(part.Text)
						trySend(msgCh, Message{Type: MessageText, Content: part.Text})
					}
				}

			case "step_finish":
				if evt.Part != nil {
					var part cursorStepFinishPart
					_ = json.Unmarshal(evt.Part, &part)
					model := cursorUsageModel(evt.Model, configuredModel)
					u := stepUsage[model]
					u.InputTokens += int64(part.Tokens.Input)
					u.OutputTokens += int64(part.Tokens.Output)
					u.CacheReadTokens += int64(part.Tokens.Cache.Read)
					stepUsage[model] = u
				}

			default:
				// 一个本解析器不处理的顶层类型。过去落到这里完全是静默的，而正是这份静默才是
				// bug：如果 CLI 把 `tool_call` 或 `thinking` 改名，每一次工具与 reasoning 行
				// 都会消失，运行却照样上报成功、tool_use_count=0，且没有任何诊断能把这种情况
				// 与“本来就无工具的运行”区分开（MUL-5434）。
				//
				// 计数让丢失变得可观测。它本身不能定位原因——见 cursorUnhandledTypeTally 里
				// “非零计数与零计数各能/不能证明什么”的说明。
				//
				// 而且计数是全部作为。一个不认识的类型永远不会被强扭成工具或 reasoning 消息：
				// 猜测上游的新增正是 MUL-5231 已经修过一次的失败模式。
				if !cursorNonTranscriptEventType(evt.Type) {
					unhandledTypes.observe(evt.Type)
				}
			}
		}
		scanErr := scanner.Err()
		if scanErr != nil {
			// scanner 停止消费 stdout 了。在 Wait 前关闭管道，防止一个仍往满 OS 管道里写畸形/
			// 超大事件的子进程卡死；scanErr 仍是首要失败原因。
			_ = stdout.Close()
		}

		// result 事件可用（会话总量）就用它的用量；否则退回按 step_finish 累计的用量。
		if !hasResultUsage {
			resultUsage = stepUsage
		}

		exitErr := cmd.Wait()
		releaseProcessGroup(cmd)
		duration := time.Since(startTime)

		// Wait 已经关过 stdin 管道，所以仍被满管道卡住的 prompt 写此时已返回；writer 只发一次。
		writeErr := <-writeErrCh

		if resultSeen {
			// 解析出的 result 就是协议边界。忽略“为了停掉一个随后滞留的 Cursor worker”而触发
			// 的取消与退出错误。
			if finalStatus == "failed" && finalError == "" {
				finalError = "cursor-agent returned an error result without details"
			}
		} else {
			switch {
			case runCtx.Err() == context.DeadlineExceeded:
				finalStatus = "timeout"
				finalError = fmt.Sprintf("cursor-agent timed out after %s", timeout)
			case runCtx.Err() == context.Canceled:
				finalStatus = "aborted"
				finalError = "execution cancelled"
			case scanErr != nil:
				finalStatus = "failed"
				finalError = fmt.Sprintf("cursor-agent stdout read error: %v", scanErr)
			case protocolError != "":
				finalStatus = "failed"
				finalError = protocolError
			case writeErr != nil:
				// 排在显式 agent 错误之后：一个因自身原因（认证失败、flag 写错）提前退出的子进程
				// 会让我们的写以 EPIPE 作为副作用失败。stderr 尾部会拼到每个 !resultSeen 失败上，
				// 所以无论哪种情况真实原因仍会浮出水面。
				finalStatus = "failed"
				finalError = fmt.Sprintf("cursor-agent prompt write failed: %v", writeErr)
			case exitErr != nil:
				finalStatus = "failed"
				finalError = fmt.Sprintf("cursor-agent exited with error: %v", exitErr)
			default:
				finalStatus = "failed"
				finalError = "cursor-agent stream ended without terminal result"
			}
		}

		if finalError != "" {
			finalError = sanitizeAgentDiagnostic(finalError)
		}
		if finalStatus == "failed" && !resultSeen {
			finalError = cursorFailureDiagnostic(
				finalError,
				exitErr,
				scanErr,
				eventCount,
				invalidEventCount,
				lastEventType,
			)
			finalError = withAgentStderr(finalError, "cursor", sanitizeAgentDiagnostic(stderrBuf.Tail()))
		}

		logStreamProtocolObservation(b.cfg.Logger, streamProtocolObservation{
			provider:                "cursor-agent",
			cliVersion:              b.cfg.CLIVersion,
			model:                   opts.Model,
			exitCode:                streamProcessExitCode(exitErr),
			eventCount:              eventCount,
			invalidEventCount:       invalidEventCount,
			assistantEventCount:     assistantEventCount,
			toolUseCount:            toolUseCount,
			sawResult:               resultSeen,
			resultIsError:           resultIsError,
			resultBytes:             resultBytes,
			lastAssistantBytes:      assistantBytes,
			scannerError:            scanErr != nil && !resultSeen,
			lastEventType:           lastEventType,
			unhandledEventTypeCount: unhandledTypes.total,
			unhandledEventTypes:     unhandledTypes.summary(),
			unhandledSubtypeCount:   unhandledSubtypeCount,
		})

		if unhandledSubtypeCount > 0 {
			// 不含内容、每次运行只发一次：表示 CLI 发来了我们选择忽略的 thinking/tool_call
			// subtype，让协议的新增在没有解析器去猜的情况下仍可诊断。
			b.cfg.Logger.Warn("cursor-agent ignored unhandled event subtypes", "count", unhandledSubtypeCount)
		}

		if unhandledTypes.total > 0 {
			// 同一契约再高一层的、也更广的信号：一个未处理的顶层类型意味着整批事件从未到达
			// 记录里。里面是什么不由这里判定——类型名是排查的起点，不是结论。
			b.cfg.Logger.Warn("cursor-agent ignored unhandled event types",
				"count", unhandledTypes.total,
				"types", unhandledTypes.summary(),
			)
		}

		b.cfg.Logger.Info("cursor-agent finished", "pid", cmd.Process.Pid, "status", finalStatus, "duration", duration.Round(time.Millisecond).String())

		finalOutput := output.String()
		if finalStatus != "completed" {
			// 一份不完整的记录不是最终答案。留在 Messages 里供观测，但失败时绝不当
			// Result.Output 暴露。
			finalOutput = ""
		}

		resCh <- Result{
			Status:     finalStatus,
			Output:     finalOutput,
			Error:      finalError,
			DurationMs: duration.Milliseconds(),
			SessionID:  sessionID,
			Usage:      resultUsage,
		}
	}()

	return &Session{Messages: msgCh, Result: resCh}, nil
}

// cursorIncompleteFinalizationWarning 是拼在失败诊断尾部的固定提示：CLI 没给出终态 result 时，
// 已执行的工具动作可能已经生效，不能当什么都没发生。
const cursorIncompleteFinalizationWarning = "actions completed before finalization may already have taken effect"

// cursorFailureDiagnostic 把一次“没收到终态 result”的失败做成自解释的错误串：附上进程状态与
// 各事件计数，让只看错误文本也能大致判断流在何处断了。
func cursorFailureDiagnostic(message string, exitErr, scanErr error, eventCount, invalidEventCount int, lastEventType string) string {
	return fmt.Sprintf(
		"%s (result_seen=false, exit_code=%d, scanner_error=%t, event_count=%d, invalid_event_count=%d, last_event_type=%s); %s",
		message,
		streamProcessExitCode(exitErr),
		scanErr != nil,
		eventCount,
		invalidEventCount,
		lastEventType,
		cursorIncompleteFinalizationWarning,
	)
}

// observedCursorEventType 保持协议诊断“有界且不含内容”。事件类型是标识符；任意值会被折叠成
// "invalid" 而不是原样抄进 daemon 日志或任务错误。
func observedCursorEventType(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	if len(value) > 64 {
		return "invalid"
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			continue
		}
		return "invalid"
	}
	return value
}

// cursorNonTranscriptEventTypes 是“确知存在、却不携带记录所需内容、因此不作为协议漂移上报”
// 的顶层事件集合。丢掉它们本来就是已上线行为；列在这里只是把它们排除在诊断之外——一个每次
// 任务都会触发的警告没人会看。
//
// 各条目来历不同，复查这份清单时要留意：
//   - `user` —— CLI 把我们自己的 prompt 回显回来。在记录的 2026.07.20 流里确认存在，即每次
//     真实运行都有。
//   - `connection`、`retry` —— 新版 CLI 上报的传输/控制帧（MUL-5434 review）。本地没有复现，
//     所以是防御性列入：某个构建不发它们时条目是惰性的，若发了则我们不能把一个已知的控制帧
//     叫成未处理的协议事件。
//
// 列在这里等于主张“该类型不带任何记录内容”。不要仅仅为了消音警告而加类型。
var cursorNonTranscriptEventTypes = map[string]struct{}{
	"user":       {},
	"connection": {},
	"retry":      {},
}

// cursorNonTranscriptEventType 判断某事件类型是否在“已知无记录内容”名单里。
func cursorNonTranscriptEventType(eventType string) bool {
	_, ok := cursorNonTranscriptEventTypes[strings.TrimSpace(eventType)]
	return ok
}

// cursorUnhandledTypeCardinalityCap 限制统计表最多保留多少个不同的类型名。一个不断冒出陌生
// 类型名（或仍能解析成 JSON 的垃圾）的流不能让这张 map 在常驻 daemon 里无界膨胀；超过上限的
// 名字全部折叠进一个溢出桶。
const cursorUnhandledTypeCardinalityCap = 16

// cursorUnhandledTypeOverflowKey 承载超出基数上限的计数。括号不会与真实类型名冲突：
// observedCursorEventType 只会返回标识符字符、"unknown" 或 "invalid"。
const cursorUnhandledTypeOverflowKey = "(overflow)"

// cursorUnhandledTypeTally 统计本解析器不处理的顶层事件类型，让“丢弃它们”不再静默。它诞生
// 之前，一次被改名的 `tool_call` 会从类型 switch 里漏下去，运行照样上报成功、tool_use_count=0、
// 且毫无诊断（MUL-5434）。
//
// 把它当作证据读，而不是判决——两个方向都是：
//
//   - 非零统计只说明流里出现过我们未处理的顶层事件。具体是哪些事件、它们是否就是记录丢失的
//     部分，仍需从类型名、invalid_event_count、最终落到一条捕获的流上去确认。
//   - 零统计只说明“没观察到未处理的顶层类型”。它并不证明模型没用工具：CLI 可能执行了工具却
//     没把更新交给它的流序列化器，新形状可能嵌套在我们已认识的类型里（例如 assistant 内容块），
//     或者事件可能因畸形帧/scanner 边界而丢失。这些分支在 #6071 上仍是敞开的。
//
// 只保留归一化的标识符与计数，从不保留载荷内容，所以这个统计可以安全地与其余协议摘要一起记
// 日志。
type cursorUnhandledTypeTally struct {
	total  int
	counts map[string]int
}

// observe 记录一个未处理的事件类型；名字超过基数上限后折叠进溢出桶。
func (t *cursorUnhandledTypeTally) observe(eventType string) {
	t.total++
	if t.counts == nil {
		t.counts = make(map[string]int, 4)
	}
	key := observedCursorEventType(eventType)
	if _, seen := t.counts[key]; !seen && len(t.counts) >= cursorUnhandledTypeCardinalityCap {
		key = cursorUnhandledTypeOverflowKey
	}
	t.counts[key]++
}

// summary 把统计渲染成稳定、排序的 "name=count" 列表（例如 "reasoning=9,tool_calls=6"），
// 便于跨运行 grep 与 diff。
func (t *cursorUnhandledTypeTally) summary() string {
	if len(t.counts) == 0 {
		return ""
	}
	keys := make([]string, 0, len(t.counts))
	for key := range t.counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", key, t.counts[key]))
	}
	return strings.Join(parts, ",")
}

// handleCursorAssistant 转发一个 assistant 事件里的内容块，并返回它往 output 追加了多少字节的
// “模型产出文本”。调用方把它与 output.Len() 分开跟踪，因为后者还会吸收终态 result 文本。
func (b *cursorBackend) handleCursorAssistant(evt *cursorStreamEvent, ch chan<- Message, output *strings.Builder) int {
	if evt.Message == nil {
		return 0
	}

	var content cursorAssistantMessage
	if err := json.Unmarshal(evt.Message, &content); err != nil {
		return 0
	}

	// 注：assistant 事件里按消息的用量被刻意忽略。token 用量只取自 "result" 事件（会话总量），
	// 避免重复计数。

	written := 0
	for _, block := range content.Content {
		switch block.Type {
		case "output_text", "text":
			if block.Text != "" {
				output.WriteString(block.Text)
				written += len(block.Text)
				trySend(ch, Message{Type: MessageText, Content: block.Text})
			}
		case "thinking":
			if block.Text != "" {
				trySend(ch, Message{Type: MessageThinking, Content: block.Text})
			}
		case "tool_use":
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
		}
	}
	return written
}

// cursorThinkingStream 把 CLI 的 `thinking` 事件序列转成我们作为 MessageThinking 转发的内容。
// Cursor 把一个 reasoning 块流式推成多个 `subtype:"delta"` 事件（每个带一段文本碎片），并以
// 一个自身不带文本的 `subtype:"completed"` 事件收尾。
//
// delta 随到随转发（daemon 用 ticker 拼接并冲刷，所以 reasoning 会在任务运行期间出现），块与
// 块之间用一个空行隔开，否则同一拼接逻辑会把相邻块粘成一段。
type cursorThinkingStream struct {
	blockOpen bool
	anySent   bool
}

// delta 返回某个 `delta` 事件要转发的内容；碎片为空时返回 ""。新块的第一个碎片会带一个空行
// 前缀，让 daemon 的拼接把各块在视觉上分开。
func (t *cursorThinkingStream) delta(text string) string {
	if text == "" {
		return ""
	}
	if !t.blockOpen && t.anySent {
		text = "\n\n" + text
	}
	t.blockOpen = true
	t.anySent = true
	return text
}

// complete 关闭当前 reasoning 块。在观察到的协议里收尾事件自身不带内容，所以这里什么都不
// 转发；下一个块的第一个 delta 会自动拿到那个分隔空行。
func (t *cursorThinkingStream) complete() {
	t.blockOpen = false
}

// cursorToolCall 是顶层 `tool_call` 事件投影到“daemon 记录所需字段”上的结构。
type cursorToolCall struct {
	Name   string
	CallID string
	Input  map[string]any
	Result string
}

// cursorToolCallKeySuffix 是 Cursor 给“每工具载荷”的命名约定：工具是某个嵌套对象的键而不是
// `name` 字段，所以 `readToolCall` 既标识出一次 read，也持有那次调用的 `args` 与（完成后）
// `result`。
const cursorToolCallKeySuffix = "ToolCall"

// parseCursorToolCall 从 `tool_call` 事件里抽取工具身份、参数与结果：
//
//	{"type":"tool_call","subtype":"started","call_id":"call-…",
//	 "tool_call":{"readToolCall":{"args":{"path":"/x/a.txt"}},"toolCallId":"call-…"}}
//
// 即使载荷解码失败也能拿到调用 ID，保证 started/completed 事件保持配对、记录里为该调用保留
// 一行。
func parseCursorToolCall(evt *cursorStreamEvent) cursorToolCall {
	call := cursorToolCall{CallID: cursorCallID(evt.CallID)}

	var envelope map[string]json.RawMessage
	if len(evt.ToolCall) == 0 || json.Unmarshal(evt.ToolCall, &envelope) != nil {
		return call
	}
	if call.CallID == "" {
		// 事件级 call_id 缺省时，从嵌套的 toolCallId 里取。
		var nestedID string
		if err := json.Unmarshal(envelope["toolCallId"], &nestedID); err == nil {
			call.CallID = cursorCallID(nestedID)
		}
	}

	key := cursorToolPayloadKey(envelope)
	if key == "" {
		return call
	}
	call.Name = strings.TrimSuffix(key, cursorToolCallKeySuffix)

	var payload struct {
		Args   map[string]any  `json:"args"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(envelope[key], &payload); err != nil {
		return call
	}
	call.Input = payload.Args
	if len(payload.Result) > 0 {
		call.Result = string(payload.Result)
	}
	return call
}

// cursorToolPayloadKey 在 tool_call 信封里挑出 `<name>ToolCall` 那个键。观察到的载荷恰好带
// 一个；排序让将来的 CLI 若发多个时选择也保持确定。
func cursorToolPayloadKey(envelope map[string]json.RawMessage) string {
	var keys []string
	for key := range envelope {
		if len(key) > len(cursorToolCallKeySuffix) && strings.HasSuffix(key, cursorToolCallKeySuffix) {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return ""
	}
	sort.Strings(keys)
	return keys[0]
}

// cursorCallID 归一化 CLI 的调用标识符。Cursor 会把两个 id 塞进一个“换行分隔”的字符串
// （"call-…\nfc_…"）；只取第一行——它已足够唯一——同时避免内嵌换行拆散 daemon 日志行。
func cursorCallID(raw string) string {
	id := strings.TrimSpace(raw)
	if idx := strings.IndexByte(id, '\n'); idx >= 0 {
		id = id[:idx]
	}
	return strings.TrimSpace(id)
}

// cursorUsageModel 决定某条用量该记在哪个模型名下：优先事件上报的模型，其次 opts 里配置的
// 模型，两者皆无则记到通用键 "cursor"。
func cursorUsageModel(evtModel, configuredModel string) string {
	if model := strings.TrimSpace(evtModel); model != "" {
		return model
	}
	if model := strings.TrimSpace(configuredModel); model != "" {
		return model
	}
	return "cursor"
}

// accumulateResultUsage 把 result 事件里的 token 用量累进 usage map。
func (b *cursorBackend) accumulateResultUsage(usage map[string]TokenUsage, evt *cursorStreamEvent, configuredModel string) {
	model := cursorUsageModel(evt.Model, configuredModel)
	u := usage[model]

	// Cursor agent 发过多种形状的 token 用量：顶层 camelCase 字段、嵌套 camelCase usage、
	// 以及嵌套的旧 snake_case usage。优先用“顶层 result 总量”（有值时），否则用嵌套的 usage
	// 对象。
	if evt.InputTokens != 0 || evt.OutputTokens != 0 || evt.CacheReadTokens != 0 || evt.CacheWriteTokens != 0 {
		u.InputTokens += evt.InputTokens
		u.OutputTokens += evt.OutputTokens
		u.CacheReadTokens += evt.CacheReadTokens
		u.CacheWriteTokens += evt.CacheWriteTokens
	} else if evt.Usage != nil {
		u.InputTokens += evt.Usage.InputTokens
		u.OutputTokens += evt.Usage.OutputTokens
		u.CacheReadTokens += evt.Usage.CacheReadInputTokens
		u.CacheWriteTokens += evt.Usage.CacheWriteInputTokens
	} else {
		return
	}

	usage[model] = u
}

// ── Cursor stream-json 类型 ──

// cursorStreamEvent 是 cursor-agent stream-json 输出里每一行 JSON 的通用结构；不同事件类型
// 用到不同字段（见下方按用途分组注释）。
type cursorStreamEvent struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	Model     string `json:"model,omitempty"`

	// assistant 字段
	Message json.RawMessage `json:"message,omitempty"`

	// thinking 字段
	Text string `json:"text,omitempty"`

	// tool_call 字段（当前 CLI）：工具是 tool_call 下嵌套对象的键
	ToolCall json.RawMessage `json:"tool_call,omitempty"`
	CallID   string          `json:"call_id,omitempty"`

	// tool_use 字段
	ToolName   string          `json:"tool_name,omitempty"`
	ToolID     string          `json:"tool_id,omitempty"`
	Parameters json.RawMessage `json:"parameters,omitempty"`

	// tool_result 字段
	Output string `json:"output,omitempty"`

	// result 字段。
	//
	// result 事件只上报 token 数；cursor-agent 的 stream-json 不携带每轮成本（没有
	// `total_cost_usd`、没有按步的 `cost`）。这一点已对照真实 CLI（2026.07.20，stream-json 与
	// json 两种输出）和 Cursor 的 CLI 文档核实。所以——不像 Grok 的轮次成本由 xAI 权威上报——
	// Cursor 的花费由下游用静态费率表估算，绝不经由这里传递。只有当真实载荷证明 CLI 确实会发
	// cost 字段时才加。
	ResultText       string       `json:"result,omitempty"`
	IsError          bool         `json:"is_error,omitempty"`
	InputTokens      int64        `json:"inputTokens,omitempty"`
	OutputTokens     int64        `json:"outputTokens,omitempty"`
	CacheReadTokens  int64        `json:"cacheReadTokens,omitempty"`
	CacheWriteTokens int64        `json:"cacheWriteTokens,omitempty"`
	Usage            *cursorUsage `json:"usage,omitempty"`

	// error 字段
	ErrorMsg string `json:"error,omitempty"`
	Detail   string `json:"detail,omitempty"`

	// 旧版兼容：text / step_finish 之类的碎片载荷放在 part 里
	Part json.RawMessage `json:"part,omitempty"`
}

// readSessionID 返回事件的会话 id（去空白后的版本）；为空返回 ""。
func (evt *cursorStreamEvent) readSessionID() string {
	if s := strings.TrimSpace(evt.SessionID); s != "" {
		return s
	}
	return ""
}

// hasResultUsage 判断 result 事件是否携带任何用量数据（嵌套 usage 对象或任意顶层计数）。
func (evt *cursorStreamEvent) hasResultUsage() bool {
	return evt.Usage != nil || evt.InputTokens != 0 || evt.OutputTokens != 0 || evt.CacheReadTokens != 0 || evt.CacheWriteTokens != 0
}

// cursorUsage 是 Cursor 各处的 token 用量对象。JSON 里缓存读字段同时出现过 cached_input_tokens
// / cacheReadTokens / cache_read_input_tokens / cacheReadInputTokens 等拼写，写入字段出现过
// cacheWriteTokens / cache_creation_input_tokens / cacheCreationInputTokens——因此解析走自定义
// UnmarshalJSON，从多个候选拼写里取第一个非零值。
type cursorUsage struct {
	InputTokens           int64 `json:"input_tokens"`
	OutputTokens          int64 `json:"output_tokens"`
	CacheReadInputTokens  int64 `json:"cached_input_tokens"`
	CacheWriteInputTokens int64
}

// UnmarshalJSON 兼容 Cursor 各版本 JSON 字段命名差异：同时声明 snake_case 与 camelCase 的
// 候选，逐个取第一个非零值。
func (u *cursorUsage) UnmarshalJSON(data []byte) error {
	var raw struct {
		InputTokensSnake              int64 `json:"input_tokens"`
		InputTokensCamel              int64 `json:"inputTokens"`
		OutputTokensSnake             int64 `json:"output_tokens"`
		OutputTokensCamel             int64 `json:"outputTokens"`
		CachedInputTokensSnake        int64 `json:"cached_input_tokens"`
		CachedInputTokensCamel        int64 `json:"cachedInputTokens"`
		CacheReadTokensCamel          int64 `json:"cacheReadTokens"`
		CacheReadInputTokensSnake     int64 `json:"cache_read_input_tokens"`
		CacheReadInputTokensCamel     int64 `json:"cacheReadInputTokens"`
		CacheWriteTokensCamel         int64 `json:"cacheWriteTokens"`
		CacheCreationInputTokensSnake int64 `json:"cache_creation_input_tokens"`
		CacheCreationInputTokensCamel int64 `json:"cacheCreationInputTokens"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	u.InputTokens = firstNonZeroInt64(raw.InputTokensSnake, raw.InputTokensCamel)
	u.OutputTokens = firstNonZeroInt64(raw.OutputTokensSnake, raw.OutputTokensCamel)
	u.CacheReadInputTokens = firstNonZeroInt64(
		raw.CachedInputTokensSnake,
		raw.CachedInputTokensCamel,
		raw.CacheReadTokensCamel,
		raw.CacheReadInputTokensSnake,
		raw.CacheReadInputTokensCamel,
	)
	u.CacheWriteInputTokens = firstNonZeroInt64(
		raw.CacheWriteTokensCamel,
		raw.CacheCreationInputTokensSnake,
		raw.CacheCreationInputTokensCamel,
	)
	return nil
}

// firstNonZeroInt64 返回参数列表里第一个非零值；全零则返回 0。用于从多个 JSON 字段候选拼写里
// 挑实际上报的那个。
func firstNonZeroInt64(values ...int64) int64 {
	for _, v := range values {
		if v != 0 {
			return v
		}
	}
	return 0
}

// cursorAssistantMessage 是 assistant 事件的 message 载荷：模型名、内容块列表与按消息的用量。
type cursorAssistantMessage struct {
	Model   string               `json:"model"`
	Content []cursorContentBlock `json:"content"`
	Usage   *cursorUsage         `json:"usage,omitempty"`
}

// cursorContentBlock 是 assistant 内容里的单个块（文本/thinking/工具调用）。
type cursorContentBlock struct {
	Type  string          `json:"type"`
	Text  string          `json:"text,omitempty"`
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
}

// cursorTextPart 是 text 事件的 part 载荷。
type cursorTextPart struct {
	Text string `json:"text"`
}

// cursorStepFinishPart 携带按步的 token 数。cursor-agent 不上报按步成本（见
// cursorStreamEvent 的 result 字段说明），所以这里只解析 token。
type cursorStepFinishPart struct {
	Tokens struct {
		Input  int `json:"input"`
		Output int `json:"output"`
		Cache  struct {
			Read int `json:"read"`
		} `json:"cache"`
	} `json:"tokens"`
}

// ── 助手 ──

// normalizeCursorStreamLine 处理 Cursor CLI 在 stream-json 模式下可能加上的 stdout:/stderr:
// 前缀，返回去空白后的 JSON 行。
func normalizeCursorStreamLine(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	// Cursor CLI 可能给行加 "stdout:" 或 "stderr:" 前缀——剥掉它。
	if idx := cursorStreamPrefixRe.FindStringIndex(trimmed); idx != nil {
		return strings.TrimSpace(trimmed[idx[1]:])
	}
	return trimmed
}

// cursorStreamPrefixRe 匹配行首可选的 stdout:/stderr: 标记（大小写不敏感，允许后接可选的冒号/
// 等号/空白）。
var cursorStreamPrefixRe = regexp.MustCompile(`^(?i)(stdout|stderr)\s*[:=]?\s*`)

// cursorErrorText 从事件里按优先级挑一段可展示的错误文本：error 消息 → detail → 终态文本。
func cursorErrorText(evt *cursorStreamEvent) string {
	if evt.ErrorMsg != "" {
		return evt.ErrorMsg
	}
	if evt.Detail != "" {
		return evt.Detail
	}
	if evt.ResultText != "" {
		return evt.ResultText
	}
	return ""
}

// cursorBlockedArgs 是 daemon 写死的 flag，用户配置的 custom_args 不得覆盖。覆盖这些会破坏
// daemon↔cursor-agent 的通信协议。
var cursorBlockedArgs = map[string]blockedArgMode{
	"-p":              blockedStandalone, // 非交互 print 模式
	"--output-format": blockedWithValue,  // stream-json 协议
	"--yolo":          blockedStandalone, // 自动批准，自主运行所需
}

// buildCursorArgs 组装一次一次性 cursor-agent 调用的 argv。
//
// 用法：cursor-agent -p --output-format stream-json
//
//	--workspace <cwd> --yolo [--model <m>] [--resume <id>]
//
// prompt 刻意不进 argv。cursor-agent 的 -p 是布尔 print-mode 开关，prompt 是位置参数；当没有
// 位置 prompt 且 stdin 不是 TTY 时，CLI 会把 stdin 读到 EOF 当作 prompt。依赖这条路径是因为
// 把用户可控文本放命令行在 Windows 上不安全：官方 cursor-agent.cmd/.ps1 启动器以
// `& node.exe index.js $args` 结尾，PowerShell 会把 $args 重新序列化到 node 的命令行。在
// Windows PowerShell 5.1（以及默认用 Legacy 原生参数传递的 pwsh ≤ 7.2）下，含内嵌双引号的
// 参数不会被重新转义，于是像 `go build -ldflags "-X main.version=foo"` 这样的 prompt 会被重新
// 切词，`-X` 作为一个独立 flag 到达 commander.js："error: unknown option '-X'"（#5649）。
// 让 prompt 走 stdin 就让它远离一切命令行，任何平台上的 shell/启动器都无法重新切词。argv 里
// 只留固定、不含内容的 flag。
func buildCursorArgs(opts ExecOptions, logger *slog.Logger) []string {
	args := []string{
		"-p",
		"--output-format", "stream-json",
		"--yolo",
	}
	if opts.Cwd != "" {
		args = append(args, "--workspace", opts.Cwd)
	}
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}
	// 注意：cursor-agent CLI 不支持 --system-prompt 或 --max-turns。指令改为经 AGENTS.md 和
	// .cursor/skills/ 文件注入。
	if opts.ResumeSessionID != "" {
		args = append(args, "--resume", opts.ResumeSessionID)
	}
	args = append(args, filterCustomArgs(opts.CustomArgs, cursorBlockedArgs, logger)...)
	return args
}
