package agent

// 文件导读：Pi CLI 的驱动封装（含同源分支 omp）。
//
// 角色定位：本文件把「Pi / omp 编程智能体进程」适配成 daemon 的 Backend 接口。做法是拉起
// 一次性的 `pi -p --mode json --session <路径>` 子进程，把任务 prompt 经 stdin 喂进去，再
// 逐行解析它写到 stdout 的 JSON 事件流（agent_start / message_update / tool_execution_* /
// turn_end / error / auto_retry_end 等），还原出正文、思考过程、工具调用、token 用量与最终
// 成败，通过 Message 通道和 Result 通道上抛给会话层。
//
// 职责边界：只管 Pi 这一类 agent 的进程编排与事件解析，以及把流式文本里夹带的工具调用结构
// （call:名字{...}、<tool_call|> 之类的控制标记）剥干净；Backend/Config/Session 骨架在
// agent.go，进程树/超时等通用机制在 launch.go，运行成败的语义分类在 agent.go 的分类器。
//
// 核心约定：① omp（https://omp.sh）是 Pi 的独立 CLI 同源分支，说同一套 JSON 事件协议，所以
// 同一实现同时服务 "pi" 与 "omp" 两个 provider，靠 providerLabel 区分日志/错误里的显示名、
// 靠 defaultExecutable 决定缺省二进制名；② 事件流是「逐行 JSON」；③ prompt 走 stdin 而不进
// argv，规避 Windows PowerShell 的 npm shim 对命令行的二次解析。
//
// 内部组织：先是文本清洗辅助（把流式文本里散落的工具标记剥掉），接着是主入口 Execute，
// 然后是事件/消息的结构体与解码函数，最后是 argv 组装与会话文件管理两节。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// piBackend 通过拉起 Pi CLI 的非交互 JSON 模式子进程来实现 Backend：
// 执行 `pi -p --mode json --session <path>`，再解析其 stdout 上的事件流。
//
// 它同时支撑 "omp"（oh-my-pi）provider——omp 是独立 CLI（https://omp.sh），
// 是 pi 的同源分支，说同一套 JSON 事件协议。daemon 会探测独立的 `omp` 二进制并注册在
// "omp" 键下；piBackend 借助 defaultExecutable 让缺省二进制名跟随 provider 键
// （pi → "pi"，omp → "omp"），前提是 cfg.ExecutablePath 为空。
type piBackend struct {
	cfg               Config
	defaultExecutable string
	// providerLabel 是日志与错误串里展示给人看的名字（"pi" 或 "omp"）。为空时默认按
	// "pi" 处理，这样直接构造 piBackend 的老调用方（测试）输出保持不变。
	providerLabel string
}

// piControlTokenRE 匹配 Pi 文本流里用于协议控制的标记——形如 `<...|>`、或 `<|` 开头后跟
// 标识符字符的串。它们只服务于结构化协议，不该出现在用户可见文本里，因此所有要展示的
// 文本在发出前都会用该正则再兜底清理一遍。
var (
	piControlTokenRE = regexp.MustCompile(`<\|[A-Za-z0-9_-]+>[A-Za-z0-9_-]*|<[A-Za-z0-9_-]+\|>`)
)

// stripPiToolCallMarkup 从一段文本里去掉所有 Pi 工具调用的残留：先剔除结构化工具标记
// （见 stripPiStructuredToolMarkup），再把散落的控制标记按正则删掉。
func stripPiToolCallMarkup(s string) string {
	s = stripPiStructuredToolMarkup(s)
	return piControlTokenRE.ReplaceAllString(s, "")
}

// drainPiTextBuffer 把新到的文本增量 delta 追加进缓冲区，再尽量多地吐出「可安全展示」的
// 文本，把尾部可能是半个标记的内容暂存在缓冲区，等后续增量到齐再处理。
//
// 为什么需要这样：Pi 的事件流是分片到达的，一个 call:xxx{...} 的 JSON 或 <...|> 控制标记
// 可能被拦腰切断，若把半截直接转发，会把协议结构漏成用户可见的乱码。
func drainPiTextBuffer(buf *strings.Builder, delta string) string {
	buf.WriteString(delta)
	emit, pending := drainPiSanitizedText(buf.String())
	buf.Reset()
	buf.WriteString(pending)
	return emit
}

// flushPiTextBuffer 在事件流收尾时冲刷缓冲区：把剩余内容全部尝试清洗并吐出；末尾仍残留
// 着没补齐的控制标记（说明 Pi 自己就没写完）时按正则直接剥掉，保证不把残缺标记漏给用户。
// 返回本次应转发的文本。
func flushPiTextBuffer(buf *strings.Builder) string {
	s := buf.String()
	buf.Reset()
	emit, pending := drainPiSanitizedText(s)
	emit += piControlTokenRE.ReplaceAllString(pending, "")
	return emit
}

// drainPiSanitizedText 对整段文本做一次「能吐多少吐多少」的清洗，返回 (可发文本, 待定文本)。
//
// 扫描规则：遇到工具标记前缀（"call:" / "response:"）就尝试跨片找到标记结束点；找到则
// 整段跳过（工具调用细节不进用户文本），没找到说明标记不完整，就停在标记起点之前，把
// 其后的整段都退回作 pending。若整段不含工具标记，则用 safePiTextEmitLen 只扣住最末尾那
// 截可能是「控制标记开头」的字符，其余全部吐出。可发文本都会先用控制标记正则清理一遍。
func drainPiSanitizedText(s string) (string, string) {
	var out strings.Builder
	for i := 0; i < len(s); {
		start, prefixLen := nextPiToolMarkupPrefix(s, i)
		if start == -1 {
			safeLen := safePiTextEmitLen(s[i:])
			out.WriteString(s[i : i+safeLen])
			return piControlTokenRE.ReplaceAllString(out.String(), ""), s[i+safeLen:]
		}
		out.WriteString(s[i:start])
		end, ok := scanPiToolMarkupEnd(s, start+prefixLen)
		if !ok {
			return piControlTokenRE.ReplaceAllString(out.String(), ""), s[start:]
		}
		i = end
	}
	return piControlTokenRE.ReplaceAllString(out.String(), ""), ""
}

// stripPiStructuredToolMarkup 去掉文本里完整的结构化工具标记——`call:名字{...}` /
// `response:名字{...}` 这类带大括号 JSON 的片段（含可选的收尾 <tool_call|>）。与
// drainPiSanitizedText 的区别：它不做「停在半截等补齐」的流式处理，而是把能识别出的完整
// 标记全删、识别不出的原文保留。
func stripPiStructuredToolMarkup(s string) string {
	var out strings.Builder
	for i := 0; i < len(s); {
		start, prefixLen := nextPiToolMarkupPrefix(s, i)
		if start == -1 {
			out.WriteString(s[i:])
			break
		}
		out.WriteString(s[i:start])
		end, ok := scanPiToolMarkupEnd(s, start+prefixLen)
		if !ok {
			out.WriteString(s[start:])
			break
		}
		i = end
	}
	return out.String()
}

// safePiTextEmitLen 返回 s 的「安全前缀长度」——即去掉最末尾那截可能是半个标记的内容。
// 工具标记前缀可能只到齐了一部分（如 "call:" 只收到 "al:"），控制标记也可能只到了 '<'
// 开头；若把这半截直接发出，等完整的标记随后到达时已无法整体识别，残余结构就会漏给用户。
func safePiTextEmitLen(s string) int {
	hold := 0
	for _, prefix := range []string{"call:", "response:"} {
		for n := 1; n < len(prefix) && n <= len(s); n++ {
			if strings.HasSuffix(s, prefix[:n]) && n > hold {
				hold = n
			}
		}
	}
	if i := strings.LastIndexByte(s, '<'); i >= 0 && looksLikePiControlTokenPrefix(s[i:]) {
		if len(s)-i > hold {
			hold = len(s) - i
		}
	}
	return len(s) - hold
}

// looksLikePiControlTokenPrefix 判断 s 是否可能是某个控制标记的开头（从 '<' 起的、只含
// 标识符字符的串）。长度上限 64 是防止对普通文本里孤零零的 '<'（如代码片段里的比较符）
// 无休止地扣住缓冲——长得不像标记就按普通文本放行。
func looksLikePiControlTokenPrefix(s string) bool {
	if len(s) == 0 || s[0] != '<' || len(s) > 64 {
		return false
	}
	for i := 1; i < len(s); i++ {
		b := s[i]
		if (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_' || b == '-' || b == '|' || b == '>' {
			continue
		}
		return false
	}
	return true
}

// nextPiToolMarkupPrefix 在 s[from:] 中找下一个工具标记前缀（"call:" 或 "response:"），
// 返回其在 s 中的绝对下标与前缀长度；没有则返回 (-1, 0)。两个前缀同时出现时取更靠前的。
func nextPiToolMarkupPrefix(s string, from int) (int, int) {
	best := -1
	bestLen := 0
	for _, prefix := range []string{"call:", "response:"} {
		if i := strings.Index(s[from:], prefix); i >= 0 {
			abs := from + i
			if best == -1 || abs < best {
				best = abs
				bestLen = len(prefix)
			}
		}
	}
	return best, bestLen
}

// scanPiToolMarkupEnd 从 i（工具标记前缀之后的字符）开始扫描一个完整的工具标记：先是工具
// 名（字母/数字/下划线/连字符），随后必须是大括号包裹的 JSON 参数。Pi 用特殊记号 `<|"|>`
// 表示 JSON 字符串内部的双引号（避免引号干扰外层配对），因此这里用 inQuote 跟踪是否处于
// 字符串里，只有括号深度归零才算结束；结束的 '}' 之后若跟着 <tool_call|> 也一并吞掉。
// 找不到配对结束符则返回 (0, false)，由调用方把剩余内容当作未到齐的半截处理。
func scanPiToolMarkupEnd(s string, i int) (int, bool) {
	nameStart := i
	for i < len(s) && isPiToolNameByte(s[i]) {
		i++
	}
	if i == nameStart || i >= len(s) || s[i] != '{' {
		return 0, false
	}

	const quoteMarker = `<|"|>`
	depth := 0
	inQuote := false
	for i < len(s) {
		if strings.HasPrefix(s[i:], quoteMarker) {
			inQuote = !inQuote
			i += len(quoteMarker)
			continue
		}

		if !inQuote {
			switch s[i] {
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					i++
					if strings.HasPrefix(s[i:], "<tool_call|>") {
						i += len("<tool_call|>")
					}
					return i, true
				}
			}
		}
		i++
	}
	return 0, false
}

// isPiToolNameByte 判断字节能否充当工具名字符（字母/数字/下划线/连字符）。
func isPiToolNameByte(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_' || b == '-'
}

// Execute 把一次任务 prompt 交给 Pi 子进程执行并流式转发作答。
//
// 流程：校验空 prompt → 确定可执行文件 → 准备并锁定会话文件 → 组装参数、拉起子进程 →
// 经 stdin 喂 prompt（写完即关闭 stdin，表示输入结束）→ 逐行解析 stdout 的 JSON 事件并
// 翻译成 Message 消息 → 进程退出后综合退出码、事件状态与超时/取消情况判定最终 Result。
//
// 会话文件路径同时充当会话标识：本次返回的 SessionID 与下次续接要用的 ResumeSessionID
// 是同一个路径，Pi 会把新的事件追加写进该文件。
func (b *piBackend) Execute(ctx context.Context, prompt string, opts ExecOptions) (*Session, error) {
	label := b.providerLabel
	if label == "" {
		label = "pi"
	}
	// Pi 会先去掉管道 stdin 首尾空白再构造它的首条消息。这里直接拒绝空白任务，否则纯
	// 空白输入会变成「进程正常退出、却没有 turn、没有输出」的空会话，看似成功实则无果。
	if strings.TrimSpace(prompt) == "" {
		return nil, fmt.Errorf("%s prompt must not be empty", label)
	}

	execName := b.cfg.ExecutablePath
	if execName == "" {
		execName = b.defaultExecutable
	}
	if execName == "" {
		execName = "pi"
	}
	lookedUp, err := exec.LookPath(execName)
	if err != nil {
		return nil, fmt.Errorf("%s executable not found at %q: %w", label, execName, err)
	}

	timeout := opts.Timeout

	// Pi 的 --session 期望一个「不断追加事件」的文件路径。这个路径同时充当我们的不透明
	// 会话标识：作为 SessionID 返回，并期待下一次执行时作为 ResumeSessionID 传回。
	sessionPath := opts.ResumeSessionID
	if sessionPath == "" {
		p, err := newPiSessionPath()
		if err != nil {
			return nil, fmt.Errorf("%s session path: %w", label, err)
		}
		sessionPath = p
	}
	if err := ensurePiSessionFile(sessionPath); err != nil {
		return nil, fmt.Errorf("%s session file: %w", label, err)
	}
	sessionLock, locked, err := tryLockPiSessionFile(sessionPath)
	if err != nil {
		return nil, fmt.Errorf("%s session lock: %w", label, err)
	}
	if !locked {
		if opts.ResumeSessionID != "" {
			return piSessionBusyResult(label, sessionPath), nil
		}
		return nil, fmt.Errorf("%s session file %q is already in use", label, sessionPath)
	}

	runCtx, cancel := runContext(ctx, timeout)

	args := buildPiArgs(sessionPath, opts, b.cfg.Logger)
	cmd, _, _ := b.cfg.commandAt(execName).execVia(runCtx, choosePiInvocation, lookedUp, args, b.cfg.Logger)
	hideAgentWindow(cmd)
	b.cfg.logAgentCommand(cmd, newAgentCommandLogArgs(args))
	cmd.WaitDelay = 10 * time.Second
	if opts.Cwd != "" {
		cmd.Dir = opts.Cwd
	}
	cmd.Env = buildEnv(b.cfg.Env)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		releasePiSessionFileLock(sessionLock)
		cancel()
		return nil, fmt.Errorf("%s stdout pipe: %w", label, err)
	}
	// Pi 在 print/JSON 模式下会把管道 stdin 读到 EOF 的内容当作首条 prompt。刻意不让用户
	// 可控文本出现在 argv 里，是为了防止 Windows 上 npm 的 PowerShell shim 把 prompt 里的
	// 引号二次拆成 CLI 选项（#6457）。显式关闭 stdin 也是 #2188 的约定：systemd 下曾观察到
	// stdin 不到 EOF 时 Pi 无限期等待。
	stdin, err := cmd.StdinPipe()
	if err != nil {
		releasePiSessionFileLock(sessionLock)
		cancel()
		return nil, fmt.Errorf("%s stdin pipe: %w", label, err)
	}
	var closeStdinOnce sync.Once
	closeStdin := func() { closeStdinOnce.Do(func() { _ = stdin.Close() }) }
	cmd.Stderr = newLogWriter(b.cfg.Logger, "["+label+":stderr] ")

	if err := startOwnedProcessTree(cmd, b.cfg.Logger); err != nil {
		closeStdin()
		releasePiSessionFileLock(sessionLock)
		cancel()
		return nil, fmt.Errorf("start %s: %w", label, err)
	}

	b.cfg.Logger.Info(label+" started", "pid", cmd.Process.Pid, "cwd", opts.Cwd, "model", opts.Model)

	msgCh := make(chan Message, 256)
	resCh := make(chan Result, 1)

	// 写 stdin 与读 stdout 必须并发：prompt 很大时子进程一边写满 stdout、一边还没读完
	// stdin，若串行地「先写完再读」会让两个进程互相阻塞而死锁。写完 prompt 就关闭 stdin，
	// 向 Pi 宣告输入结束。
	writeErrCh := make(chan error, 1)
	go func() {
		_, err := io.WriteString(stdin, prompt)
		closeStdin()
		writeErrCh <- err
	}()

	// context 被取消时同时关闭两个管道：关 stdin 能释放被「子进程不再读」卡住的写方，
	// 关 stdout 能释放被阻塞的事件流扫描器。
	go func() {
		<-runCtx.Done()
		closeStdin()
		_ = stdout.Close()
	}()

	go func() {
		defer func() { releasePiSessionFileLock(sessionLock) }()
		defer cancel()
		defer close(msgCh)
		defer close(resCh)

		startTime := time.Now()
		var output strings.Builder
		finalStatus := "completed"
		var finalError string
		var lastTurnError string
		usage := make(map[string]TokenUsage)

		// Pi 的 message_update 事件可能很大（每次增量都内嵌整条消息的 partial），
		// 共享的流扫描上限已把这种情况考虑进去。
		scanner := newAgentStreamScanner(stdout)
		var textBuffer strings.Builder

		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			var evt piStreamEvent
			if err := json.Unmarshal([]byte(line), &evt); err != nil {
				continue
			}

			switch evt.Type {
			case "agent_start":
				trySend(msgCh, Message{Type: MessageStatus, Status: "running"})

			case "turn_start":
				output.Reset()
				textBuffer.Reset()
				lastTurnError = ""

			case "message_update":
				if evt.AssistantMessageEvent == nil {
					continue
				}
				switch evt.AssistantMessageEvent.Type {
				case "text_delta":
					if d := drainPiTextBuffer(&textBuffer, evt.AssistantMessageEvent.Delta); d != "" {
						output.WriteString(d)
						trySend(msgCh, Message{Type: MessageText, Content: d})
					}
				case "thinking_delta":
					if d := evt.AssistantMessageEvent.Delta; d != "" {
						trySend(msgCh, Message{Type: MessageThinking, Content: d})
					}
				}

			case "tool_execution_start":
				var params map[string]any
				if len(evt.Args) > 0 {
					// 入参解析失败不影响流程：工具名与调用 ID 已经足够上层分类/展示。
					_ = json.Unmarshal(evt.Args, &params)
				}
				trySend(msgCh, Message{
					Type:   MessageToolUse,
					Tool:   evt.ToolName,
					CallID: evt.ToolCallID,
					Input:  params,
				})

			case "tool_execution_end":
				trySend(msgCh, Message{
					Type:   MessageToolResult,
					CallID: evt.ToolCallID,
					Output: decodePiResult(evt.Result),
				})

			case "turn_end":
				msg := decodePiMessage(evt.Message)
				if msg == nil {
					continue
				}
				if msg.Usage != nil {
					model := msg.Model
					if model == "" {
						model = opts.Model
					}
					if model == "" {
						model = "unknown"
					}
					u := usage[model]
					u.InputTokens += msg.Usage.Input
					u.OutputTokens += msg.Usage.Output
					u.CacheReadTokens += msg.Usage.CacheRead
					u.CacheWriteTokens += msg.Usage.CacheWrite
					usage[model] = u
				}
				// Pi 以 error 结束的一轮只有在后面没有下文时才是终态：自动重试前 Pi 也会发
				// 出同样的 stopReason，而 turn_start 会清掉 lastTurnError，所以后续某轮一旦
				// 成功，这里记录的旧错误不会残留。
				if msg.StopReason == "error" {
					lastTurnError = msg.ErrorMessage
					if lastTurnError == "" {
						lastTurnError = label + " ended the turn with an error"
					}
				}

			case "error":
				errText := decodePiString(evt.Message)
				trySend(msgCh, Message{Type: MessageError, Content: errText})
				if finalStatus == "completed" {
					finalStatus = "failed"
					finalError = errText
				}

			case "auto_retry_end":
				if !evt.Success && finalStatus == "completed" {
					finalStatus = "failed"
					if evt.FinalError != "" {
						finalError = evt.FinalError
					} else {
						finalError = label + " exhausted automatic retries"
					}
				}
			}
		}
		if d := flushPiTextBuffer(&textBuffer); d != "" {
			output.WriteString(d)
			trySend(msgCh, Message{Type: MessageText, Content: d})
		}

		waitErr := cmd.Wait()
		releaseProcessGroup(cmd)
		duration := time.Since(startTime)

		// Wait 会关掉进程管道，因此子进程退出时还被卡住的 prompt 写入此刻已经返回。
		// 写方只会发送一次，读这一下不会阻塞。
		writeErr := <-writeErrCh

		if runCtx.Err() == context.DeadlineExceeded {
			finalStatus = "timeout"
			finalError = fmt.Sprintf("%s timed out after %s", label, timeout)
		} else if runCtx.Err() == context.Canceled {
			finalStatus = "aborted"
			finalError = "execution cancelled"
		} else if waitErr != nil && finalStatus == "completed" {
			finalStatus = "failed"
			// 优先用本轮提供的 provider 错误消息，而不是进程退出码：Pi（以及
			// pi-print-clean-exit）在 stopReason=error 后仍会以 1 退出，若只看 Wait() 的
			// 错误，就会丢掉 lastTurnError——分类器只能见到 "exit status 1"，把一次本可重试
			// 的瞬时 LiteLLM/OpenAI "Connection error."/"Request timed out." 误判成
			// 不可重试的 process_failure。退出状态仍作为后缀保留，真正崩溃时依然可见
			// （与 OpenCode 的「空步 + 退出」组合形态一致）。
			if lastTurnError != "" {
				finalError = fmt.Sprintf("%s; %s exited with error: %v", lastTurnError, label, waitErr)
			} else {
				finalError = fmt.Sprintf("%s exited with error: %v", label, waitErr)
			}
		} else if writeErr != nil && finalStatus == "completed" {
			finalStatus = "failed"
			finalError = fmt.Sprintf("%s prompt write failed: %v", label, writeErr)
		} else if lastTurnError != "" && finalStatus == "completed" {
			// Pi 在「一轮没能完成且没有自动重试」时照样以 0 退出，且既不发 error 事件也不发
			// auto_retry_end。没有这个分支，该次运行会被误报为成功且无任何输出。
			finalStatus = "failed"
			finalError = lastTurnError
		}

		b.cfg.Logger.Info(label+" finished", "pid", cmd.Process.Pid, "status", finalStatus, "duration", duration.Round(time.Millisecond).String())

		// 等转录文本就绪后再发最终 Result，保证续接执行一定能读到完整 transcript。
		// 结果通道本身带缓冲，若图省事用 defer 发送，接收方可能抢跑，在 Pi 已退出后仍把
		// 会话误判为「占用中」。
		releasePiSessionFileLock(sessionLock)
		sessionLock = nil
		resCh <- Result{
			Status:     finalStatus,
			Output:     output.String(),
			Error:      finalError,
			DurationMs: duration.Milliseconds(),
			SessionID:  sessionPath,
			Usage:      usage,
		}
	}()

	return &Session{Messages: msgCh, Result: resCh}, nil
}

// piSessionBusyResult 构造一个「会话忙」的失败结果并立即返回。调用场景：会话文件正被另一
// 次执行持锁，而调用方是想续接同一会话（带了 ResumeSessionID）。把「忙」做成带
// ResumeRejectedTransient 标记的一次性失败结果、经通道返回而不是直接报错，让上层能按
// 瞬时拒绝处理（例如稍后重试同一会话）。
func piSessionBusyResult(label, sessionPath string) *Session {
	msgCh := make(chan Message)
	close(msgCh)
	resCh := make(chan Result, 1)
	resCh <- Result{
		Status:                  "failed",
		Error:                   fmt.Sprintf("%s session file %q is already in use by another execution", label, sessionPath),
		ResumeRejectedTransient: true,
	}
	close(resCh)
	return &Session{Messages: msgCh, Result: resCh}
}

// ── Pi 事件类型 ──

// piStreamEvent 是我们从 Pi JSON 事件流中消费的字段合集。不同事件类型携带的字段不同，这里
// 把可能出现的都声明上、按事件类型分组；message、result 这类在不同事件里可能是字符串也
// 可能是对象的字段，用 json.RawMessage 先原样兜住，由各 switch 分支按需解码。
type piStreamEvent struct {
	Type string `json:"type"`

	// message_update 事件专用：助手消息的增量负载。
	AssistantMessageEvent *piAssistantMessageEvent `json:"assistantMessageEvent,omitempty"`

	// tool_execution_start / tool_execution_end 事件专用：工具名、调用 ID、入参与结果。
	ToolCallID string          `json:"toolCallId,omitempty"`
	ToolName   string          `json:"toolName,omitempty"`
	Args       json.RawMessage `json:"args,omitempty"`
	Result     json.RawMessage `json:"result,omitempty"`
	IsError    bool            `json:"isError,omitempty"`

	// error 事件里 Message 是字符串；turn_end 事件里 Message 是对象，故用 RawMessage 兜底。
	Message json.RawMessage `json:"message,omitempty"`

	// auto_retry_end 事件专用：Pi 自动重试结束后报告成功与否，失败时给出最终错误。
	Success    bool   `json:"success,omitempty"`
	FinalError string `json:"finalError,omitempty"`
}

// piAssistantMessageEvent 对应 message_update 事件里的 assistantMessageEvent 负载：
// Type 区分 text_delta（正文增量）与 thinking_delta（思考过程增量），Delta 即增量文本。
type piAssistantMessageEvent struct {
	Type  string `json:"type"`
	Delta string `json:"delta,omitempty"`
}

// piMessage 是 turn_end 事件携带的 turn 终态信息：Role/Model 标记发言者与所用模型，
// Usage 记录 token 用量，StopReason 与 ErrorMessage 共同描述本轮是否失败。
type piMessage struct {
	Role  string   `json:"role,omitempty"`
	Model string   `json:"model,omitempty"`
	Usage *piUsage `json:"usage,omitempty"`

	// turn_end 携带本轮终态。Pi 在模型调用未能完成时把 StopReason 置为 "error"（无论之后
	// 是否自动重试），并把模型侧的错误消息放进 ErrorMessage。
	StopReason   string `json:"stopReason,omitempty"`
	ErrorMessage string `json:"errorMessage,omitempty"`
}

// piUsage 是 Pi 单次 turn 汇报的 token 用量（输入/输出/缓存读/缓存写/合计）。
type piUsage struct {
	Input       int64 `json:"input"`
	Output      int64 `json:"output"`
	CacheRead   int64 `json:"cacheRead"`
	CacheWrite  int64 `json:"cacheWrite"`
	TotalTokens int64 `json:"totalTokens"`
}

// decodePiMessage 把 turn_end 事件的 Message（对象形态）解析成 piMessage；内容为空或解析
// 失败返回 nil，调用方遇到 nil 直接跳过该事件。
func decodePiMessage(raw json.RawMessage) *piMessage {
	if len(raw) == 0 {
		return nil
	}
	var m piMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	return &m
}

// decodePiString 尽量把 RawMessage 读成字符串：JSON 里原本就是字符串的直接反序列化；
// 反序列化失败（个别事件里该字段是别的形态）时退回手工剥掉外层引号。
func decodePiString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return strings.Trim(string(raw), `"`)
}

// decodePiResult 把 tool_execution_end 事件的 result 读成字符串：本就是字符串的直接取用；
// 否则（如结构化 JSON 结果）保留其原始文本形态原样转成串。
func decodePiResult(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return string(raw)
}

// ── 命令行参数组装 ──

// piBlockedArgs 是 daemon 固定写死、不允许用户经 custom_args 覆盖的开关。允许覆盖会破坏
// daemon ↔ Pi 之间的通信协议（例如拿掉 --mode json 就再也收不到事件流）。
var piBlockedArgs = map[string]blockedArgMode{
	"-p":         blockedStandalone, // 非交互模式（无终端）
	"--print":    blockedStandalone, // -p 的别名
	"--mode":     blockedWithValue,  // 固定 "json"：事件流协议
	"--session":  blockedWithValue,  // 会话路径由 daemon 统一管理
	"--thinking": blockedWithValue,  // 思考级别归 agent.thinking_level 管
}

// piCustomArgModes 把 Pi 0.83 内置的参数解析器复刻到「足以区分选项值与位置消息」的粒度，
// 供 filterPiCustomArgs 放行 custom_args 时判断各参数的取值形态。未知长选项被当作扩展选项
// 处理、可能带一个可选值；未知短选项原样放行，交由 Pi 自行报错（维持其原有行为）。
var piCustomArgModes = map[string]blockedArgMode{
	"--help":                 blockedStandalone,
	"-h":                     blockedStandalone,
	"--version":              blockedStandalone,
	"-v":                     blockedStandalone,
	"--continue":             blockedStandalone,
	"-c":                     blockedStandalone,
	"--resume":               blockedStandalone,
	"-r":                     blockedStandalone,
	"--provider":             blockedWithValue,
	"--model":                blockedWithValue,
	"--api-key":              blockedWithValue,
	"--system-prompt":        blockedWithValue,
	"--append-system-prompt": blockedWithValue,
	"--name":                 blockedWithValue,
	"-n":                     blockedWithValue,
	"--no-session":           blockedStandalone,
	"--session-id":           blockedWithValue,
	"--fork":                 blockedWithValue,
	"--session-dir":          blockedWithValue,
	"--models":               blockedWithValue,
	"--no-tools":             blockedStandalone,
	"-nt":                    blockedStandalone,
	"--no-builtin-tools":     blockedStandalone,
	"-nbt":                   blockedStandalone,
	"--tools":                blockedWithValue,
	"-t":                     blockedWithValue,
	"--exclude-tools":        blockedWithValue,
	"-xt":                    blockedWithValue,
	"--thinking":             blockedWithValue,
	"--export":               blockedWithValue,
	"--extension":            blockedWithValue,
	"-e":                     blockedWithValue,
	"--no-extensions":        blockedStandalone,
	"-ne":                    blockedStandalone,
	"--skill":                blockedWithValue,
	"--prompt-template":      blockedWithValue,
	"--theme":                blockedWithValue,
	"--no-skills":            blockedStandalone,
	"-ns":                    blockedStandalone,
	"--no-prompt-templates":  blockedStandalone,
	"-np":                    blockedStandalone,
	"--no-themes":            blockedStandalone,
	"--no-context-files":     blockedStandalone,
	"-nc":                    blockedStandalone,
	"--list-models":          blockedOptionalValue,
	"--verbose":              blockedStandalone,
	"--approve":              blockedStandalone,
	"-a":                     blockedStandalone,
	"--no-approve":           blockedStandalone,
	"-na":                    blockedStandalone,
	"--offline":              blockedStandalone,
}

// buildPiArgs 组装一次一次性 Pi 调用的 argv。
//
// 固定参数：
//
//	-p                         非交互模式（prompt 由 stdin 喂入）
//	--mode json                stdout 每行输出一个 JSON 事件
//	--session <path>           会话日志文件（预先创建，续接会话时复用）
//	--model <selector>         模型选择串，原样透传
//	--thinking <level>         按 agent 覆盖推理级别
//
// prompt 刻意不进 argv。Pi 在 print/JSON 模式下会把非 TTY 的 stdin 当作 prompt 来源；走这条
// 受支持的路径，是为了避免 Windows PowerShell 的 npm shim 把 prompt 内容二次拆解成选项。
func buildPiArgs(sessionPath string, opts ExecOptions, logger *slog.Logger) []string {
	args := []string{
		"-p",
		"--mode", "json",
	}
	if sessionPath != "" {
		args = append(args, "--session", sessionPath)
	}
	// 模型选择串整体走 --model，且绝不合成 --provider：Pi 自己的解析器已能接受我们持有的
	// 各种形态——规范的 `provider/id`、裸 id，以及关键的「id 本身含斜杠」的情况（网关型
	// provider 的模型 id 形如 `claude/claude-opus-5`）。若按第一个斜杠切开去填 --provider，
	// 会得到一个 Pi 从没听过的 provider 名；而未知 --provider 是硬错误（"Unknown provider
	// ..."），无法自愈——--model 单独使用则会回退到把整串当作裸模型 id 匹配。少传反而更
	// 通用（GH #7300）。
	if model := strings.TrimSpace(opts.Model); model != "" {
		args = append(args, "--model", model)
	}
	if opts.ThinkingLevel != "" {
		args = append(args, "--thinking", opts.ThinkingLevel)
	}
	// 注意：这里刻意不传 --tools。省略它会让 Pi 使用完整工具注册表（含用户装的扩展工具）；
	// 一旦传 --tools 就变成了限制性白名单，会静默过滤掉扩展注册的工具（#2379）。
	// 想限制工具的用户可以通过 custom_args 自行传入。
	//
	// SystemPrompt 也刻意不转成 --append-system-prompt 透传：Pi 会自行加载 daemon 写到
	// workdir 里的分任务 AGENTS.md，若再内联同一份运行时简介，会在每一轮重复叠加。
	// 该结论对 Pi 0.67.2 验证过（MUL-5392）。
	args = append(args, filterPiCustomArgs(opts.CustomArgs, logger)...)
	return args
}

// filterPiCustomArgs 把 custom_args 里会污染任务 prompt 的 @file 引用与位置消息剔除。prompt
// 已改走 stdin，Pi 会把第一个位置消息不加分隔地拼到 stdin 内容后面——这些 token 若保留，
// 会悄悄改掉甚至替换掉任务 prompt。带值的选项仍放行，包括扩展注册的未知长选项的值。
func filterPiCustomArgs(args []string, logger *slog.Logger) []string {
	args = filterCustomArgs(args, piBlockedArgs, logger)
	filtered := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, "@") {
			if logger != nil {
				logger.Warn("custom_args: Pi file input would alter the stdin task prompt, skipping")
			}
			continue
		}
		if !strings.HasPrefix(arg, "-") {
			if logger != nil {
				logger.Warn("custom_args: Pi positional input would alter the stdin task prompt, skipping")
			}
			continue
		}

		flag := arg
		hasInlineValue := false
		if idx := strings.Index(arg, "="); idx > 0 {
			flag = arg[:idx]
			hasInlineValue = true
		}
		filtered = append(filtered, arg)
		if hasInlineValue {
			continue
		}

		mode, known := piCustomArgModes[flag]
		if !known {
			if strings.HasPrefix(flag, "--") {
				mode = blockedOptionalValue
			} else {
				continue
			}
		}
		switch mode {
		case blockedWithValue:
			if i+1 < len(args) {
				filtered = append(filtered, args[i+1])
				i++
			}
		case blockedOptionalValue:
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") && !strings.HasPrefix(args[i+1], "@") {
				filtered = append(filtered, args[i+1])
				i++
			}
		}
	}
	return filtered
}

// ── 会话路径 ──

// piSessionDir 返回 Pi 会话 JSONL 文件所在目录。单独抽出来并暴露给外部包（见文件末尾的
// PiSessionDir），是为了让 usage 包的用量扫描器能指向同一位置，不必重复拼这段路径。
func piSessionDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".multica", "pi-sessions"), nil
}

// newPiSessionPath 生成一个新的会话文件路径：位于 piSessionDir() 目录下，文件名是 UTC
// 时间戳（纳秒精度）加 .jsonl，保证并发或重复调用不撞名。
func newPiSessionPath() (string, error) {
	dir, err := piSessionDir()
	if err != nil {
		return "", err
	}
	name := fmt.Sprintf("%s.jsonl", time.Now().UTC().Format("20060102T150405.000000000"))
	return filepath.Join(dir, name), nil
}

// ensurePiSessionFile 确保 path 处存在一个会话文件（必要时创建为空文件）。Pi 在 --session
// 指向不存在的文件时会拒绝启动；已存在（续接的旧会话）则原样保留、不清空。
func ensurePiSessionFile(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	return f.Close()
}

// PiSessionDir 把 piSessionDir 暴露给本模块的其它包使用（如 usage 的用量扫描器）。
func PiSessionDir() (string, error) {
	return piSessionDir()
}
