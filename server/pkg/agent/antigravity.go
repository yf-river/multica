package agent

// 文件职责：本文件实现 Backend 接口中的「Antigravity」后端——Google 的 Antigravity CLI
// （`agy` 二进制）。后端用一次性 prompt 模式拉起它（`agy -p <prompt>`）：尽管 flag 名带
// print 字样，当前 agy print 模式仍能运行 Antigravity 工具；daemon 只能用这个模式，因为
// `agy -i` 需要附加的 TTY。
//
// 与 Claude/Codex/Cursor/Gemini 不同，Antigravity CLI 不暴露结构化事件流——stdout 是
// 纯 assistant 文本（中间的 "I will run X" 行与最终回复交织在一起）。所以后端把 stdout
// 按行流式发成 MessageText，并把同样的文本累积成最终 Result.Output。
//
// 本文件的几个关键设计都围绕「agy 日志」（daemon 通过 --log-file 指定的临时文件）做兜底：
//
//   - 会话续跑用 `--conversation <id>`；会话 id 不印在 stdout 上，而是靠扫 glog 日志行里
//     printmode.go 在消息发送时写的 `conversation=<uuid>` token 抓取。
//   - agy 1.0.14 的 print 模式有个回归：一个回合可能跑了工具、出了最终回复，却往 stdout
//     输出 ZERO 字节（日志里是 "PlannerResponse without ModifiedResponse encountered"），
//     退出码仍为 0、无错误，于是「空白但 completed」会到达 daemon（MUL-3726 / #4595）。
//     因此 stdout 为空时，后端会从 agy 持久化写入的会话 transcript 里找回 assistant 文本。
//   - agy 自己的 print 超时（--print-timeout）与终态 provider 错误也只落日志、stdout 与
//     退出码可能一切正常，同样需要扫日志发现（MUL-3570 / antigravityProviderError）。

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// antigravityBackend 实现 Backend 接口：用一次性 prompt 拉起 Google 的 Antigravity CLI
// （`agy -p <prompt>`）。尽管上游 flag 名如此，当前 agy print 模式仍能运行 Antigravity
// 工具；这是与 daemon 兼容的模式，因为 `agy -i` 要求附加 TTY。与 Claude / Codex /
// Cursor / Gemini 不同，Antigravity CLI 不暴露结构化事件流——stdout 是纯 assistant 文本
// （中间的 "I will run X" 行与最终回复交错）。因此后端把 stdout 逐行流式发成
// MessageText，并把同样文本累积成最终 Result.Output。
//
// agy 1.0.14 的 print 模式回归了这个 stdout 契约：一个回合可能跑了工具、产出了最终回复，
// 却往 stdout 输出 ZERO 字节（日志里是 "PlannerResponse without ModifiedResponse
// encountered"）。退出码 0 且无错误日志，于是「空白但 completed」的运行到达 daemon，
// 用户看到空结果尽管工作其实发生了（MUL-3726 / #4595）。所以 stdout 空而回合其它一切
// 正常时，后端会从 agy 持久写入的会话 transcript 里找回 assistant 文本（见
// readAntigravityTranscriptOutput）。
//
// 会话续跑用 `--conversation <id>`。会话 id 不印在 stdout 上；我们把 `--log-file` 指到
// 临时文件，再扫它的 glog 格式行找 printmode.go 在消息发送时记录的
// `conversation=<uuid>` token 来捕获。
type antigravityBackend struct {
	cfg Config
}

// Execute 是 Backend 接口的入口：拿一条用户 prompt，起一个 agy print 模式进程跑完一整轮。
//
// 流程：确认可执行文件存在 → 指定了模型就先拿 `agy models` 目录做前置校验（agy 对认不
// 出的模型会静默 no-op：退出 0、输出空，校验失败能让这种「completed 但空」提前暴露）→
// 建临时日志文件 → 拼参数起进程 → 逐行流式读 stdout（按行发 MessageText、同时累积成
// output）→ 进程退出后判最终状态：超时/取消/进程错误，并扫日志识别 agy 自己的
// --print-timeout 耗尽与终态 provider 错误 → 若「completed 但 output 全空」则从 transcript
// 找回文本 → 汇总 Result。会话 id 从日志里抓，token 用量留空（agy 不上报）。
func (b *antigravityBackend) Execute(ctx context.Context, prompt string, opts ExecOptions) (*Session, error) {
	execPath := b.cfg.ExecutablePath
	if execPath == "" {
		execPath = "agy"
	}
	if _, err := exec.LookPath(execPath); err != nil {
		return nil, fmt.Errorf("agy executable not found at %q: %w", execPath, err)
	}

	// 防 agy 对认不出的 --model 静默 no-op：它会退出 0、输出空，否则会被当成一个
	// 「completed 但内容空」的任务。opts.Model 是 agent.model 与 daemon 全局
	// MULTICA_ANTIGRAVITY_MODEL 默认值（在 daemon.go 解析）的唯一漏斗，在这里校验能
	// 覆盖每个来源——UI 自由文本、API、持久化的值、环境变量默认值。装上 CLI 后确定
	// 不提供的非空模型会被拒绝，带可操作的错误提示。校验是 fail-OPEN：若 `agy models`
	// 目录查不出来，就放 agy 自己去解析这个值，别让一次发现故障卡死整次运行（见
	// antigravityModelError）。
	if opts.Model != "" {
		catalog, _ := ListModels(ctx, "antigravity", b.cfg.commandAt(execPath))
		if err := antigravityModelError(opts.Model, catalog.Models); err != nil {
			return nil, err
		}
	}

	timeout := opts.Timeout
	runCtx, cancel := runContext(ctx, timeout)

	logFile, err := os.CreateTemp("", "multica-agy-log-*.log")
	if err != nil {
		cancel()
		return nil, fmt.Errorf("create agy log file: %w", err)
	}
	logPath := logFile.Name()
	_ = logFile.Close()

	args := buildAntigravityArgs(prompt, logPath, timeout, opts, b.cfg.Logger)

	cmd := b.cfg.commandAt(execPath).exec(runCtx, args...)
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
		_ = os.Remove(logPath)
		return nil, fmt.Errorf("agy stdout pipe: %w", err)
	}
	stderrBuf := newStderrTail(newLogWriter(b.cfg.Logger, "[agy:stderr] "), agentStderrTailBytes)
	cmd.Stderr = stderrBuf

	if err := startOwnedProcessTree(cmd, b.cfg.Logger); err != nil {
		cancel()
		_ = os.Remove(logPath)
		return nil, fmt.Errorf("start agy: %w", err)
	}

	b.cfg.Logger.Info("agy started", "pid", cmd.Process.Pid, "cwd", opts.Cwd, "model", opts.Model)

	msgCh := make(chan Message, 256)
	resCh := make(chan Result, 1)

	go func() {
		<-runCtx.Done()
		_ = stdout.Close()
	}()

	go func() {
		defer cancel()
		defer close(msgCh)
		defer close(resCh)
		defer os.Remove(logPath)

		startTime := time.Now()
		var output strings.Builder
		finalStatus := "completed"
		var finalError string

		scanner := newAgentStreamScanner(stdout)

		trySend(msgCh, Message{Type: MessageStatus, Status: "running"})

		for scanner.Scan() {
			line := scanner.Text()
			// daemon 拼接流式 MessageText 时不加分隔符（pendingText.WriteString），
			// 所以流出的文本必须自带换行。这里镜像 output 的构造方式——首行之后每行都
			// 前缀换行、空行也照发——让持久化的 task_message 文本能精确还原 output。
			// 若发裸行且跳过空行，会丢掉所有换行，把块级 markdown（标题、列表）在聊天里
			// 挤成一行（#6149）。
			chunk := line
			if output.Len() > 0 {
				output.WriteByte('\n')
				chunk = "\n" + line
			}
			output.WriteString(line)
			if chunk != "" {
				trySend(msgCh, Message{Type: MessageText, Content: chunk})
			}
		}
		if err := scanner.Err(); err != nil {
			b.cfg.Logger.Warn("agy stdout scanner error", "err", err)
		}

		waitErr := cmd.Wait()
		releaseProcessGroup(cmd)
		duration := time.Since(startTime)

		sessionID := readAntigravityConversationID(logPath)

		if runCtx.Err() == context.DeadlineExceeded {
			finalStatus = "timeout"
			finalError = fmt.Sprintf("agy timed out after %s", timeout)
		} else if runCtx.Err() == context.Canceled {
			finalStatus = "aborted"
			finalError = "execution cancelled"
		} else if waitErr != nil && finalStatus == "completed" {
			finalStatus = "failed"
			finalError = fmt.Sprintf("agy exited with error: %v", waitErr)
		} else if finalStatus == "completed" && antigravityPrintTimedOut(logPath) {
			// agy 撞上了自己的 --print-timeout：它往 stdout 打了 "Error: timed out
			// waiting for response" 然后 EXIT 0，于是 runCtx 从未触发、waitErr 为 nil——
			// 上面的检查会把回合留成 "completed"。这里把它标成真超时，而不是让用户无法
			// 区分于已完成任务的截断成功（MUL-3570）。
			finalStatus = "timeout"
			finalError = fmt.Sprintf(
				"agy --print-timeout elapsed after %s waiting for the agent response; a long-running command likely outlived the print timeout",
				antigravityPrintTimeout(timeout),
			)
		} else if providerErr := antigravityProviderError(logPath); finalStatus == "completed" && providerErr != "" {
			// agy 还可能把终态的模型/provider 失败只写进每轮日志、stdout 空着退出 0。
			// 不提升这个标记的话，daemon 会把一次失败回合记成空成功。
			finalStatus = "failed"
			finalError = fmt.Sprintf("agy provider error: %s", providerErr)
		}
		if finalError != "" {
			finalError = withAgentStderr(finalError, "agy", stderrBuf.Tail())
		}

		finalOutput := output.String()
		if finalStatus == "completed" && strings.TrimSpace(finalOutput) == "" {
			// agy 1.0.14 print 模式可能正常跑完回合（工具执行、回复产出）却什么也没往
			// stdout 写，留下一个上面所有守卫都没抓住的「空白但 completed」运行
			// （MUL-3726）。从 agy 持久化到会话 transcript 的 assistant 文本里找回答案，
			// 让用户看到真实回答而不是空结果。同时补发一条 MessageText，让任务 transcript
			// 也跟上；否则 Result.Output 只在合成的最终评论里可见，执行 transcript 却
			// 一直空白。
			if recovered := readAntigravityTranscriptOutput(logPath, sessionID); recovered != "" {
				finalOutput = recovered
				trySend(msgCh, Message{Type: MessageText, Content: recovered})
				b.cfg.Logger.Info("agy recovered empty stdout from transcript", "bytes", len(recovered))
			}
		}

		b.cfg.Logger.Info("agy finished", "pid", cmd.Process.Pid, "status", finalStatus, "duration", duration.Round(time.Millisecond).String())

		resCh <- Result{
			Status:     finalStatus,
			Output:     finalOutput,
			Error:      finalError,
			DurationMs: duration.Milliseconds(),
			SessionID:  sessionID,
			// Antigravity CLI 目前不上报每回合的 token 用量；留空 Usage，而不是在一个
			// 猜的模型名下报误导性的 0。
			Usage: map[string]TokenUsage{},
		}
	}()

	return &Session{Messages: msgCh, Result: resCh}, nil
}

// antigravityConversationIDRe 匹配 printmode.go 在 CLI 派发用户消息时写的 glog 行——
// 这是日志里唯一对「新建与续跑回合」都可靠暴露会话 UUID 的地方。
//
// 示例：`I0528 13:36:23.318877 73304 printmode.go:130] Print mode:
// conversation=b8b263a4-4b2f-4339-acc9-78b248e2b606, sending message`
var antigravityConversationIDRe = regexp.MustCompile(
	`conversation=([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})`,
)

// antigravityPrintTimeoutRe 匹配 agy 的 printmode.go 在其 print 模式墙钟预算
// （--print-timeout）耗尽、agent 还没产出最终响应时写的 glog 行。agy 随后往 stdout 打
// "Error: timed out waiting for response" 并 EXIT 0——runCtx 永不触发、cmd.Wait 返回 nil
// ——没有这个信号，daemon 会把被截断的回合记成成功的 "completed"（MUL-3570）。
//
// 示例：`E0623 17:17:59.017212 65926 printmode.go:289] Print mode: timed out
// after 100 polls (printed=3)`
var antigravityPrintTimeoutRe = regexp.MustCompile(`Print mode: timed out after \d+ polls`)

// antigravityProviderErrorRe 抓 agy 日志里的终态 agent executor 错误行。
var antigravityProviderErrorRe = regexp.MustCompile(`agent executor error:\s*(.+)`)

// antigravityPrintTimedOut 报告每轮日志是否显示 agy 撞上了自己的 print 模式超时。
// Best-effort：日志缺失或上游改格式时返回 false，此时运行退回按退出状态分类。
func antigravityPrintTimedOut(logPath string) bool {
	if logPath == "" {
		return false
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		return false
	}
	return antigravityPrintTimeoutRe.Match(data)
}

// antigravityProviderError 提取 agy 只写进日志、不一定打到 stdout 或反映到退出码里的
// 终态上游/模型错误。
func antigravityProviderError(logPath string) string {
	if logPath == "" {
		return ""
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		return ""
	}
	matches := antigravityProviderErrorRe.FindAllSubmatch(data, -1)
	if len(matches) == 0 {
		return ""
	}
	return strings.TrimSpace(string(matches[len(matches)-1][1]))
}

// readAntigravityConversationID 扫每轮日志文件找会话 UUID。Best-effort：日志缺失、
// CLI 还没派发就退出、或上游改了格式时返回 ""。
func readAntigravityConversationID(logPath string) string {
	if logPath == "" {
		return ""
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		return ""
	}
	matches := antigravityConversationIDRe.FindAllSubmatch(data, -1)
	if len(matches) == 0 {
		return ""
	}
	// CLI 在一个回合里会反复记会话 id（每个派发的消息一条，外加 stream-update 行）。
	// 文件里任何非空 UUID 都指向同一会话，所以取最后一条匹配——这正是下一轮
	// `--conversation` 该钉住的 id。
	return string(matches[len(matches)-1][1])
}

// antigravityAppDataDirRe 匹配 agy 启动时写的、指名其 CLI app data 目录的 glog 行——
// 每会话 transcript 就在这个根目录下面。从日志（daemon 经 --log-file 拥有它）读路径比
// 猜 $HOME 更稳，也能跟着 agy 走自定义 data dir。
//
// 示例：`I0630 14:19:40.582492 88197 common.go:156] CLI app data directory:
// /Users/me/.gemini/antigravity-cli`
var antigravityAppDataDirRe = regexp.MustCompile(`CLI app data directory:\s*(.+)`)

// antigravityTranscriptRecord 是 agy 每会话 transcript.jsonl 中一行的最小形状。回合以
// USER_INPUT 记录开始；assistant 的回复是 PLANNER_RESPONSE 记录，带 source=MODEL、（尘埃
// 落定后）status=DONE。Content 放文本，纯工具步骤里是 JSON null——它是 RawMessage，所以
// null 或非字符串值会被跳过而不是让整行解析失败。
type antigravityTranscriptRecord struct {
	Type    string          `json:"type"`
	Source  string          `json:"source"`
	Status  string          `json:"status"`
	Content json.RawMessage `json:"content"`
}

// readAntigravityTranscriptOutput 当 stdout 什么都没带出来时，从 agy 的每会话 transcript
// 里找回 assistant 文本。agy 1.0.14 的 print 模式可能正常跑完回合（工具执行、最终回复
// 产出）却往 stdout 输出零字节，留给 daemon 一个「空白但 completed」的运行（MUL-3726 /
// #4595）。完整回复仍被持久写到：
//
//	<appDataDir>/brain/<conversation-id>/.system_generated/logs/transcript.jsonl
//
// 以 PLANNER_RESPONSE / source=MODEL 记录的形式存在。
//
// transcript 是每会话一份、跨续跑回合累积（daemon 通过 --conversation / ResumeSessionID
// 复用它），所以必须只返回当前回合的回复——否则以后某次空 stdout 回合会把先前回合的
// 回答再发一遍。每个回合以一条 USER_INPUT 记录开场，于是我们在每个 USER_INPUT 处清零，
// 只保留紧随其后、最后一条之后的模型文本。另外要求 status=DONE，跳过任何未来的
// 流式/部分 planner 记录。剩余文本按序拼接（中间叙述 + 最终回复），镜像 stdout 本来会为
// 这个回合流出的内容。Best-effort：app data dir 或会话 id 未知、transcript 缺失、或当前
// 回合没有模型文本时返回 ""。
func readAntigravityTranscriptOutput(logPath, conversationID string) string {
	if logPath == "" || conversationID == "" {
		return ""
	}
	appDataDir := readAntigravityAppDataDir(logPath)
	if appDataDir == "" {
		return ""
	}
	transcriptPath := filepath.Join(
		appDataDir, "brain", conversationID, ".system_generated", "logs", "transcript.jsonl",
	)
	f, err := os.Open(transcriptPath)
	if err != nil {
		return ""
	}
	defer f.Close()

	var parts []string
	scanner := newAgentStreamScanner(f)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec antigravityTranscriptRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}
		if rec.Type == "USER_INPUT" {
			// 新回合边界：清掉为先前回合收集的内容，保证续跑的会话只产出当前回合的
			// 回复。
			parts = parts[:0]
			continue
		}
		if rec.Type != "PLANNER_RESPONSE" || rec.Source != "MODEL" || rec.Status != "DONE" {
			continue
		}
		var text string
		// Content 在纯工具步骤里是 JSON null；unmarshal 后 text 为 ""。
		// 非字符串值（对象）会解析失败并被跳过。
		if err := json.Unmarshal(rec.Content, &text); err != nil {
			continue
		}
		if strings.TrimSpace(text) != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n\n")
}

// readAntigravityAppDataDir 从每轮日志里提取 agy 的 CLI app data 目录。
// Best-effort：日志缺失或上游改格式时返回 ""。
func readAntigravityAppDataDir(logPath string) string {
	if logPath == "" {
		return ""
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		return ""
	}
	m := antigravityAppDataDirRe.FindSubmatch(data)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(string(m[1]))
}

// antigravityBlockedArgs 是 daemon 写死的参数清单，用户配置的 custom_args 不得覆盖它们。
// 覆盖这些会破坏非交互运行或 daemon 的会话续跑记账。
var antigravityBlockedArgs = map[string]blockedArgMode{
	"-p":                             blockedWithValue,
	"--print":                        blockedWithValue,
	"--prompt":                       blockedWithValue,
	"-i":                             blockedStandalone, // 交互模式要 TTY，daemon 下跑不了
	"--prompt-interactive":           blockedStandalone,
	"-c":                             blockedStandalone, // 续跑用 --conversation，不是 --continue
	"--continue":                     blockedStandalone,
	"--conversation":                 blockedWithValue, // 由 ExecOptions.ResumeSessionID 管理
	"--model":                        blockedWithValue, // 由 ExecOptions.Model / agent.model 管理
	"--print-timeout":                blockedWithValue,
	"--dangerously-skip-permissions": blockedStandalone, // daemon 模式常开
	"--log-file":                     blockedWithValue,  // daemon 要靠它抓会话 id
	"--settings":                     blockedWithValue,  // Claude Code 专属 flag；agy 会拒绝
}

// buildAntigravityArgs 拼出与 daemon 兼容的一次性 agy 调用的 argv。
//
//	agy -p <prompt> --dangerously-skip-permissions [--model <catalog id>]
//	    --print-timeout <duration> --log-file <tmp>
//	    [--conversation <id>] [--add-dir <cwd>]
//
// agy 1.0.6 加了 `--model` flag（MUL-3125），opts.Model 有值时现在会透传。值就是
// parseAntigravityModels 从 `agy models` 读出的目录标识：agy 1.1.11+ 是 slug（如
// "gemini-3.6-flash-high"），旧输出是原样的单列值（如 "Claude Opus 4.6 (Thinking)"）。
// 两种形态都作为单个 exec 参数传，空格括号无需 shell 转义。agy 至今没有 --system-prompt；
// runtime 指令经任务工作目录里的 AGENTS.md 送达。
//
// agy 对认不出的模型串会静默 no-op（空输出、退出 0），所以 Execute 会先用 `agy models`
// 目录校验 opts.Model、前置拒绝未知值（见 antigravityModelError）——走到拼 argv 这步，
// 值要么为空要么已知可用。opts.Model 为空时省略该 flag，让 agy 自己解默认值。
func buildAntigravityArgs(prompt, logPath string, timeout time.Duration, opts ExecOptions, logger *slog.Logger) []string {
	args := []string{
		"-p", prompt,
		"--dangerously-skip-permissions",
	}
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}
	// agy 的 --print-timeout 没有「禁用」值，且省略 flag 时默认 5 分钟——所以「不设上限」
	// 没法用省略来表达，那会让每个回合都被 5 分钟静默腰斩，杀掉所有构建/测试超过预算的
	// 运行（MUL-3570）。因此总是传该 flag：配置了正向墙钟上限就传它，否则传一个大到
	// agy 自己的超时永远不会先于 daemon 的空闲/工具看门狗（它们会回收真正卡死的运行）
	// 触发（见 antigravityPrintTimeout）。
	args = append(args, "--print-timeout", antigravityFormatTimeout(antigravityPrintTimeout(timeout)))
	args = append(args, "--log-file", logPath)
	if opts.ResumeSessionID != "" {
		args = append(args, "--conversation", opts.ResumeSessionID)
	}
	if opts.Cwd != "" {
		args = append(args, "--add-dir", filepath.Clean(opts.Cwd))
	}
	args = append(args, filterCustomArgs(opts.ExtraArgs, antigravityBlockedArgs, logger)...)
	args = append(args, filterCustomArgs(opts.CustomArgs, antigravityBlockedArgs, logger)...)
	return args
}

// antigravityModelError 在 `model` 非空且确定不在 `available`（`agy models` 目录）里时
// 返回可操作的错误；否则返回 nil。空的 `available` 表示发现没跑出目录（agy 缺失、瞬时
// 故障）——这里 fail OPEN，让 agy 自己去解析值，别让一次发现故障卡死运行。匹配是精确
// 的，因为 agy 的 --model 要的就是目录里的精确标识；近似值（多空格、丢后缀）应被拒绝，
// 反正 agy 遇到它也会静默 no-op。
func antigravityModelError(model string, available []Model) error {
	if model == "" || len(available) == 0 {
		return nil
	}
	ids := make([]string, 0, len(available))
	for _, m := range available {
		if m.ID == model {
			return nil
		}
		ids = append(ids, m.ID)
	}
	return fmt.Errorf(
		"antigravity model %q is not available from `agy models`; pick one of: %s",
		model, strings.Join(ids, ", "),
	)
}

// antigravityNoCapPrintTimeout 是「daemon 不配墙钟上限（opts.Timeout <= 0）」时传给
// --print-timeout 的值。agy 的 --print-timeout 没有「禁用」哨兵、省略时回落到 5 分钟
// 默认，所以「不设上限」必须是一个大到 agy 自己的铡刀永远不会在 daemon 的空闲看门狗
// （它回收真正卡死的运行）之前触发。24h 对任何真实回合都等于无界，同时仍是 agy 能解析
// 的有限时长；即使操作员把 MULTICA_AGENT_IDLE_WATCHDOG 抬到远超默认值，它也仍然高于
// 看门狗预算。
const antigravityNoCapPrintTimeout = 24 * time.Hour

// antigravityPrintTimeout 解析交给 agy --print-timeout 的墙钟预算：daemon 配了正向上限
// 就用它，否则用上面的 no-cap 哨兵。这是 buildAntigravityArgs（设 flag）与 Execute（把
// print 模式超时标出来）共享的唯一真源。
func antigravityPrintTimeout(timeout time.Duration) time.Duration {
	if timeout > 0 {
		return timeout
	}
	return antigravityNoCapPrintTimeout
}

// antigravityFormatTimeout 把 Go 的 duration 渲染成 agy CLI 接受的 `<n>m<n>s` 形态
// （例如 20m0s）。亚秒级超时向上取整到 1s，免得 CLI 拒绝该 flag。
func antigravityFormatTimeout(d time.Duration) string {
	if d < time.Second {
		d = time.Second
	}
	// time.Duration.String() 产出的正是 "20m0s" / "1h30m0s" 这类形状，agy 在接收端用 Go
	// 标准库的 flag.Duration 解析。
	return d.String()
}
