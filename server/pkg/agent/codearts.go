package agent

// 文件导读：华为云 CodeArts CLI 的驱动封装。
//
// 角色定位：把 CodeArts（华为云基于 OpenCode 分叉出的编程智能体 CLI）适配成 daemon 的
// Backend。执行方式是一次性 `codearts run --format json --auto` 子进程：任务 prompt 走
// stdin 喂入，agent 的正文、工具调用、token 用量等以「每行一个 JSON 事件」的格式输出在
// stdout，本文件负责把这些事件翻译成 Message/Result 交给上层会话。
//
// 职责边界：只管 CodeArts 这一个 agent。CodeArts 虽由 OpenCode 派生，但它的命令面、启动
// 环境、鉴权行为和事件演化都由本文件独立维护（见 codeartsBackend 注释），避免 OpenCode
// 后端的改动悄悄改变 CodeArts 的行为；MCP 配置的 schema 翻译则与 OpenCode 共享。
//
// 核心约定：① prompt 永远走 stdin、不进 argv，规避 Windows 命令行长度上限与二次解析
// （#6538）；② 事件流没有「最终结果」类事件，因此「没报错」不等于「跑完了」，判定成功
// 必须依赖 step 括号闭合等正向终态证据（见 processEvents）；③ 取消/超时时先对整棵进程树
// SIGTERM→SIGKILL、再关 stdout 读端，避免子进程写已关闭管道空转（#4533）。

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
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// codeartsTerminateGraceNanos 可按纳秒覆盖「取消 codearts 进程后的宽限期」：进程收到 SIGTERM
// 后允许它优雅退出多久，超时再连同整棵进程组一起 SIGKILL。值为 0 表示用默认宽限。用原子
// 类型是为了让测试能缩短宽限期，而不必与读取它的取消 goroutine 竞争。关于「为什么必须先
// 终止进程再关 stdout 管道」见 Execute 里的取消处理（#4533）。
var codeartsTerminateGraceNanos atomic.Int64

// codeartsTerminateGrace 返回取消后的终止宽限期：测试覆盖了则用覆盖值，否则默认 5 秒。
func codeartsTerminateGrace() time.Duration {
	if n := codeartsTerminateGraceNanos.Load(); n > 0 {
		return time.Duration(n)
	}
	return 5 * time.Second
}

// codeartsBlockedArgs 是 daemon 固定写死、不允许用户经 custom_args 覆盖的开关。
var codeartsBlockedArgs = map[string]blockedArgMode{
	"--format":                       blockedWithValue,  // 与 daemon 通信用的 JSON 输出格式
	"--auto":                         blockedStandalone, // daemon 独有的非交互权限模式
	"--sandbox":                      blockedStandalone, // daemon 独有的沙箱策略
	"--dir":                          blockedWithValue,  // CodeArts 不支持；workdir 归 cmd.Dir 管
	"--variant":                      blockedWithValue,  // CodeArts 不支持
	"--dangerously-skip-permissions": blockedStandalone, // 仅 OpenCode 有的权限开关
}

// codeartsBackend 是华为云 CodeArts CLI 的独立适配器。CodeArts 派生于 OpenCode，但它的命令
// 面、启动器环境、鉴权行为与事件演进都由本文件独立维护，这样 OpenCode 后端的改动不会静默
// 改变 CodeArts 的执行行为。
type codeartsBackend struct {
	cfg Config
}

// newCodeArtsBackend 构造 CodeArts 后端；未配置 Logger 时回退到默认 logger。
func newCodeArtsBackend(cfg Config) (Backend, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &codeartsBackend{cfg: cfg}, nil
}

// Execute 把一次任务 prompt 交给 CodeArts 子进程执行并流式转发作答。
//
// 流程：定位可执行文件（Windows 下优先绕开 .cmd shim、直取同目录原生 exe）→ 组装
// `run --format json --auto` 参数 → 以独立进程组拉起（便于取消时整树回收）→ 后台 goroutine
// 写 stdin、取消 goroutine 负责优雅终止 → 主 goroutine 逐行解析 stdout 事件并累加状态 →
// 进程退出后综合退出码、事件状态与超时/取消情况，把 scanResult 加工成最终 Result。
func (b *codeartsBackend) Execute(ctx context.Context, prompt string, opts ExecOptions) (*Session, error) {
	execPath := b.cfg.ExecutablePath
	if execPath == "" {
		execPath = "codearts"
	}
	resolved, err := exec.LookPath(execPath)
	if err != nil {
		return nil, fmt.Errorf("codearts executable not found at %q: %w", execPath, err)
	}
	if runtime.GOOS == "windows" {
		if native := resolveCodeArtsNativeFromShim(resolved, os.Stat); native != "" {
			b.cfg.Logger.Info("codearts resolved to native binary to avoid .cmd shim argv truncation", "shim", resolved, "native", native)
			resolved = native
		}
	}
	execPath = resolved

	timeout := opts.Timeout
	runCtx, cancel := runContext(ctx, timeout)

	args := []string{"run", "--format", "json", "--auto"}
	// CodeArts 不提供 OpenCode 的 --dir 选项；cmd.Dir 与下面的 PWD 各自独立地把项目与技能
	// 发现锚定到任务 workdir。
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}
	if opts.ThinkingLevel != "" {
		b.cfg.Logger.Warn("codearts does not support thinking-level overrides; ignoring", "thinkingLevel", opts.ThinkingLevel)
	}
	// CodeArts 的 `run` 子命令没有 --prompt 选项。运行时简介（runtime brief）通过 daemon
	// 写入 workdir 的分任务 AGENTS.md 到达 agent；任务 prompt 走下面的 stdin。
	if opts.MaxTurns > 0 {
		b.cfg.Logger.Warn("codearts does not support --max-turns; ignoring", "maxTurns", opts.MaxTurns)
	}
	if opts.ResumeSessionID != "" {
		args = append(args, "--session", opts.ResumeSessionID)
	}
	args = append(args, filterCustomArgs(opts.CustomArgs, codeartsBlockedArgs, b.cfg.Logger)...)
	// 任务 prompt 只走 stdin、绝不进 argv（stdin 管道的接法见下）。`codearts run` 会把它的
	// 变长参数 [message..] 位置消息与管道输入合并，因此不传任何位置消息时，管道内容就是完整
	// 的运行消息。若改为内联进参数，在 Windows 上会硬性失败：CreateProcess 把 lpCommandLine
	// 限制在 32,767 字符（经 .cmd shim 由 cmd.exe 转发时只有 8,191），而一条带着工作区模型
	// 与技能清单的 prompt 自己就轻松超过——进程根本起不来，Go 还只会抛一句误导性的
	// "The filename or extension is too long"（#6538）。让 prompt 不进 argv 同时也让它不出现在
	// 系统进程列表里；共享的命令 logger 会另行对 argv 里的敏感值做脱敏。

	runtimeCmd := b.cfg.commandAt(execPath)
	cmd := runtimeCmd.exec(runCtx, args...)
	hideAgentWindow(cmd)
	// 把 codearts 放进独立的进程组，取消时才能覆盖整棵树（codearts 及其派生的工具子进程），
	// 而不只是直接子进程——否则一次被取消/重启的运行可能遗留下继续空转的后代（#4533）。
	configureProcessGroup(cmd)
	// 接管 context 取消逻辑。默认的 CommandContext 行为是在 runCtx 结束瞬间只对主进程
	// SIGKILL；这里改为由下面的取消 goroutine 驱动「先对整组 SIGTERM、宽限期后再 SIGKILL」
	// 的优雅终止，并且只在进程树已被发信号之后才关闭 stdout 读端。把 Cancel 设为 no-op 返回
	// nil，让 os/exec 不会用自带的 kill 来和我们抢；WaitDelay 仍作为最后的兜底。
	cmd.Cancel = func() error { return nil }
	b.cfg.logAgentCommandWithPrompt(cmd, newAgentCommandLogArgs(args, trustAgentCommandPositional(0, "run")), len(prompt))
	cmd.WaitDelay = 10 * time.Second
	if opts.Cwd != "" {
		cmd.Dir = opts.Cwd
	}

	env := buildCodeArtsEnv(b.cfg.Env)
	// 除了 cmd.Dir 之外还要覆盖 PWD：底层 CodeArts 引擎解析项目配置与技能时会查 PWD。
	if opts.Cwd != "" {
		env = append(env, "PWD="+opts.Cwd)
	}
	// 把 agent.mcp_config 经 OPENCODE_CONFIG_CONTENT 注入 CodeArts——这是 CodeArts 通用的
	// 内联配置注入通道，会以 "local" 作用域并入（在项目配置遍历之后、远端/托管配置之前）。
	// 目前只有 MCP 走这条通道；将来若有别的 Multica 字段需要同一条通道，就在这次 env 拼接
	// 前先组装一份合并的 CodeArts 配置片段。
	//
	// 刻意不改动项目配置本身：同一 (agent, issue) 的 workdir 会在多轮之间复用。
	mcpContent, err := buildCodeArtsMCPConfigContent(opts.McpConfig)
	if err != nil {
		cancel()
		return nil, err
	}
	if mcpContent != "" {
		if _, dup := b.cfg.Env["OPENCODE_CONFIG_CONTENT"]; dup {
			b.cfg.Logger.Warn("agent.custom_env sets OPENCODE_CONFIG_CONTENT but agent.mcp_config takes precedence and overrides it")
		}
		env = append(env, "OPENCODE_CONFIG_CONTENT="+mcpContent)
	}
	cmd.Env = env

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("codearts stdout pipe: %w", err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("codearts stdin pipe: %w", err)
	}
	var closeStdinOnce sync.Once
	closeStdin := func() { closeStdinOnce.Do(func() { _ = stdin.Close() }) }
	cmd.Stderr = newLogWriter(b.cfg.Logger, "[codearts:stderr] ")

	if err := startOwnedProcessTree(cmd, b.cfg.Logger); err != nil {
		closeStdin()
		cancel()
		return nil, fmt.Errorf("start codearts: %w", err)
	}

	b.cfg.Logger.Info("codearts started", "pid", cmd.Process.Pid, "cwd", opts.Cwd, "model", opts.Model)

	msgCh := make(chan Message, 256)
	resCh := make(chan Result, 1)

	// procDone 在 cmd.Wait() 返回后关闭，让取消处理器能跳过已经退出的进程、不对已死的 pid
	// 发信号。
	procDone := make(chan struct{})

	// prompt 由独立 goroutine 写，避免与下面的 stdout 读取互相死锁：prompt 超过系统管道缓冲
	// （约 64 KiB）时，写方会阻塞到 CodeArts 排空为止，而 CodeArts 在没有人在读它的 stdout
	// 时也无法继续。关闭 stdin 即宣告 prompt 结束——CodeArts 会一直读到 EOF
	// （`await Bun.stdin.text()`），stdin 不关会让运行永远挂起。成功与出错路径都要关。
	writeErrCh := make(chan error, 1)
	go func() {
		_, err := io.WriteString(stdin, prompt)
		closeStdin()
		writeErrCh <- err
	}()

	// 取消/超时时，先终止 codearts（及其派生的工具子进程），再去放开扫描器。旧实现会立即
	// 关闭 stdout 读端，可能让子进程向已关闭的管道继续写、在 EPIPE 上空转。这里改为对整棵
	// 进程组 SIGTERM，给一段宽限期让其干净退出，再 SIGKILL。SIGKILL 不可被捕获，一旦送达，
	// 组内任何成员都无法再运行（或再写），只有这时才适合关掉 stdout 读端——作为对「被一个
	// 卡死的后代继续攥着的扫描器」的最后解锁手段。WaitDelay 是最终兜底（#4533）。
	go func() {
		select {
		case <-procDone:
			return // 自行正常结束，无需终止
		case <-runCtx.Done():
		}
		// 先释放仍被写满的 stdin 管道卡住的 prompt 写入——若 CodeArts 在排空前就停止读取，
		// 那个 goroutine 会随着 daemon 一直滞留。
		closeStdin()
		if cmd.Process != nil {
			signalProcessGroup(cmd, syscall.SIGTERM)
			select {
			case <-procDone: // 在宽限期内退出了
			case <-time.After(codeartsTerminateGrace()):
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

		// 等待进程退出，然后放开取消处理器。
		exitErr := cmd.Wait()
		close(procDone)
		releaseProcessGroup(cmd)
		duration := time.Since(startTime)

		// Wait 会关掉进程管道，因此 CodeArts 退出时还被卡住的 prompt 写入此刻已返回。
		// 写方只发送一次，读这一下不会阻塞。
		writeErr := <-writeErrCh

		if runCtx.Err() == context.DeadlineExceeded {
			scanResult.status = "timeout"
			scanResult.errMsg = fmt.Sprintf("codearts timed out after %s", timeout)
		} else if runCtx.Err() == context.Canceled {
			scanResult.status = "aborted"
			scanResult.errMsg = "execution cancelled"
		} else if exitErr != nil && scanResult.status == "completed" {
			scanResult.status = "failed"
			scanResult.errMsg = fmt.Sprintf("codearts exited with error: %v", exitErr)
		} else if exitErr != nil && scanResult.noTerminalSignal {
			// 状态已由「无终态信号」守卫置为 failed；这里再补上进程退出细节，让中途崩溃
			// 仍能呈现真正杀死它的信号/退出码。
			scanResult.errMsg = fmt.Sprintf("%s; codearts exited with error: %v", scanResult.errMsg, exitErr)
		} else if writeErr != nil && !scanResult.sawTerminalSignal {
			// prompt 写入失败只有在「已确证运行结束」时才可视为无害：CodeArts 干任何活之前
			// 都会先把 stdin 读到 EOF，所以一个能走到终态信号的运行必然收全了整个 prompt；
			// 之后才记下的 EPIPE 只是管道在收尾关闭，若据此判失败会丢弃一个成功结果。
			//
			// 「没失败」并不等于「跑完了」。status 初始为 "completed"，processEvents 只在拿到
			// 结构性证据时才 fail-closed，因此一个什么都不输出、退出码却是 0 的子进程仍会报
			// "completed"。若 prompt 根本没送到，那恰恰是绝不能冒充干净成功的运行——所以要
			// 以 sawTerminalSignal 为准。用追加而不是覆盖，保留事件流自己的诊断。
			if scanResult.errMsg == "" {
				scanResult.errMsg = fmt.Sprintf("codearts prompt write failed: %v", writeErr)
			} else {
				scanResult.errMsg = fmt.Sprintf("%s; codearts prompt write failed: %v", scanResult.errMsg, writeErr)
			}
			scanResult.status = "failed"
		}

		b.cfg.Logger.Info("codearts finished", "pid", cmd.Process.Pid, "status", scanResult.status, "duration", duration.Round(time.Millisecond).String())

		// 组装 usage map。CodeArts 不按步上报模型名，因此把全部用量记到配置的模型（或
		// "unknown"）名下。
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

// ── 事件处理器 ──

// codeArtsEventResult 汇总一次事件流处理过程中累积下来的状态。
type codeArtsEventResult struct {
	status           string
	errMsg           string
	output           string
	sessionID        string
	usage            TokenUsage // 所有 step 累积起来的 token 用量
	noTerminalSignal bool       // 守卫触发：事件流结束，却没有证据表明运行真的跑完了
	// sawTerminalSignal 是「运行真的完成」的正向证据：某次 step_finish 合上了最后一个 step，
	// 既无待续接的后续、又有产出可展示。它不是 noTerminalSignal 的取反——一个完全没有事件
	// 的流两者都不置位，因为既没有可 fail-closed 的对象、也没有能证明完成的东西。需要「本次
	// 运行确实完成」语义的调用方必须检查本字段；status 默认是 "completed"，本身承载不了这层
	// 含义。
	sawTerminalSignal bool
}

// processEvents 从 r 逐行读取 JSON 事件、分发给 ch，并返回累积结果。这是核心的扫描循环，
// 单独抽出是为了可测试性。
func (b *codeartsBackend) processEvents(r io.Reader, ch chan<- Message) codeArtsEventResult {
	var output strings.Builder
	var unparsedOutput strings.Builder
	var sessionID string
	var usage TokenUsage
	parsedEvents := 0
	finalStatus := "completed"
	var finalError string

	// 用 step 的「括号配对」来记账，避免把一条停在半路的流误当成干净完成。CodeArts 的 JSON
	// 流没有终态 result 事件（不像 Claude 的 type:"result"），所以「没看到错误」不足以证明
	// 运行结束。codearts 只在终态（completed 或 error）发 tool_use，因此悬空的工具调用意味着
	// 有一个未闭合的 step——step 括号闭合才是正向的终态信号。工具自身已恢复的错误
	// （state.status == "error"）在健康运行里很常见，不得影响状态判定。
	//
	// 单靠 step 括号还不够：step_finish 带 reason（FinishReason: "stop"、"tool-calls" 等），
	// 而仍有工具结果待回喂的运行通常以 reason "tool-calls" 闭合当前 step、再进下一个
	// step_start。不过有些 provider 明明发了工具调用却回 "stop"，且 CodeArts 在「必须把由非
	// provider 执行的工具结果回喂给模型」时仍会刻意继续这类运行。所以要同时跟踪两个信号：
	// 只要在任一种「续接间隙」里遇到 EOF 就 fail-closed。reason 缺失时保留旧版 step 括号
	// 行为，以兼容协议。
	openStep := false                // 处于某次 step_start 与对应 step_finish 之间
	stepHasContinuationTool := false // 当前 step 有一个需要 CodeArts 回喂的本地工具结果
	awaitingContinuation := false    // 最近一次 step_finish 之后还需要另一个 step
	sawStepFinish := false           // 至少合上过一个 step；参见 codeArtsEventResult.sawTerminalSignal

	// step 括号仍漏掉第三种形态：一个 step 干净地开了又合，却什么都没带——没有文本、没有
	// 工具调用、也没有任何上报的用量（#6522，观察到的形态是 step_finish reason 为 "unknown"，
	// 所有 token 计数与 cost 全为 0）。没有用量说明 provider 的那次往返根本没发生，这种 step
	// 是一条披着干净收尾外衣的死流，让运行终结于它同样是假绿完成。
	//
	// 判据刻意定为「这个 step 什么也没产出」，而不是「这次运行没有文本」：一个交付物仅是
	// 工具副作用的任务是正当的，必须保持绿灯。任何一项生命迹象——文本、工具调用、或协议
	// 上报的任意用量字段——都让 step 保持「有产出」。这也是不去读 reason 本身的原因：
	// 缺失或无法识别的 reason 出于兼容仍按终态处理（见下面的兼容回归），而「空洞」与它是
	// 正交的两件事。
	stepProducedOutput := false // 当前 step 发过文本、工具调用，或上报过用量
	lastStepVoid := false       // 最近闭合的 step 什么也没产出

	scanner := newAgentStreamScanner(r)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var event codeartsEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			// 不是合法 JSON（可能夹带了 CLI 的非事件输出）。截断收集起来，一旦整条流解析不出
			// 任何事件，可以用它拼出更有用的报错（上限 4096 字节，避免失控）。
			if unparsedOutput.Len() < 4096 {
				if unparsedOutput.Len() > 0 {
					unparsedOutput.WriteByte('\n')
				}
				remaining := 4096 - unparsedOutput.Len()
				if len(line) > remaining {
					line = line[:remaining]
				}
				unparsedOutput.WriteString(line)
			}
			continue
		}
		parsedEvents++

		if event.SessionID != "" {
			sessionID = event.SessionID
		}

		switch event.Type {
		case "text":
			b.handleTextEvent(event, ch, &output)
			if event.Part.Text != "" {
				stepProducedOutput = true
			}
		case "tool_use":
			b.handleToolUseEvent(event, ch)
			stepProducedOutput = true
			// 只有「需要回喂给模型的工具结果」才构成续接信号；provider 自己执行完的工具
			// 不产生续接 step。
			if event.Part.Metadata == nil || !event.Part.Metadata.ProviderExecuted {
				stepHasContinuationTool = true
			}
		case "error":
			b.handleErrorEvent(event, ch, &finalStatus, &finalError)
		case "step_start":
			openStep = true
			stepHasContinuationTool = false
			awaitingContinuation = false
			stepProducedOutput = false
			trySend(ch, Message{Type: MessageStatus, Status: "running"})
		case "step_finish":
			openStep = false
			sawStepFinish = true
			awaitingContinuation = event.Part.Reason == "tool-calls" ||
				(event.Part.Reason != "" && stepHasContinuationTool)
			stepHasContinuationTool = false
			// 从 step_finish 事件累积 token 用量。TokenUsage 只计模型计费的字段；上报的每个
			// 字段都额外作为「provider 往返确实发生过」的证据——正是这一点把有产出的 step
			// 挡在下面「空洞 step」守卫之外。
			if t := event.Part.Tokens; t != nil {
				usage.InputTokens += t.Input
				usage.OutputTokens += t.Output
				if t.Cache != nil {
					usage.CacheReadTokens += t.Cache.Read
					usage.CacheWriteTokens += t.Cache.Write
				}
			}
			if codeArtsStepReportedUsage(&event.Part) {
				stepProducedOutput = true
			}
			lastStepVoid = !stepProducedOutput
		}
	}

	// 检查扫描器错误（如管道破裂、读取错误）。
	if scanErr := scanner.Err(); scanErr != nil {
		b.cfg.Logger.Warn("codearts stdout scanner error", "error", scanErr)
		if finalStatus == "completed" {
			finalStatus = "failed"
			finalError = fmt.Sprintf("stdout read error: %v", scanErr)
		}
	}

	// 要求拿到正向终态信号。以下情况属于运行没跑完，却撞上了干净的 EOF：step 仍开着（刚合上
	// 一个 reason 为 "tool-calls"、续接 step 却没开始的 step），或刚合上的是个什么都没带的
	// step——provider 的流已死，而 `codearts run` 退出时连 error 事件都没发。对这种结构性
	// 证据要 fail-closed，而不是报一个假绿的完成。
	noTerminalSignal := false
	if finalStatus == "completed" && parsedEvents == 0 {
		finalStatus = "failed"
		finalError = "codearts returned no parseable JSON events"
		if detail := sanitizeCLIOutput(unparsedOutput.String()); detail != "" {
			finalError += ": " + detail
		}
		noTerminalSignal = true
	}
	if finalStatus == "completed" {
		switch {
		case openStep:
			finalStatus = "failed"
			finalError = "codearts stream ended without a terminal signal (step still open at EOF)"
			noTerminalSignal = true
		case awaitingContinuation:
			finalStatus = "failed"
			finalError = "codearts stream ended without a terminal signal (last step required a continuation that never started)"
			noTerminalSignal = true
		case lastStepVoid:
			finalStatus = "failed"
			finalError = "codearts stream ended on an empty step (no text, no tool call, no reported usage) — the provider produced nothing"
			noTerminalSignal = true
		}
	}

	return codeArtsEventResult{
		status:            finalStatus,
		errMsg:            finalError,
		output:            output.String(),
		sessionID:         sessionID,
		usage:             usage,
		noTerminalSignal:  noTerminalSignal,
		sawTerminalSignal: sawStepFinish && !noTerminalSignal,
	}
}

// codeArtsStepReportedUsage 报告某次 step_finish 的 part 是否带有「provider 往返确实发生过」
// 的证据。
//
// CodeArts 协议把 reasoning 与合计放在独立的字段里（与 input/output/cache 并列），成本
// （cost）则是整个 token 块的兄弟字段——一个 step 完全可能 reasoning 或 cost 为正而
// input/output 都是 0。若只查 input/output，会把这种 step 判成空洞、进而让一次健康运行
// 失败，所以协议上报的每个字段都算数；只有全部为 0 才意味着没有发生模型调用。
//
// reasoning 与 total 计数只作为证据读取，刻意不折进 TokenUsage：total 是派生量（加进去会
// 重复计费），而 TokenUsage 又没有 reasoning 桶——把两者记进这里会改动计费数字而不是修好
// 这个 bug。
func codeArtsStepReportedUsage(part *codeartsEventPart) bool {
	if part.Cost > 0 {
		return true
	}
	t := part.Tokens
	if t == nil {
		return false
	}
	if t.Input > 0 || t.Output > 0 || t.Reasoning > 0 || t.Total > 0 {
		return true
	}
	return t.Cache != nil && (t.Cache.Read > 0 || t.Cache.Write > 0)
}

// handleTextEvent 处理 "text" 事件：把非空文本追加进 output 并作为 MessageText 转发。
func (b *codeartsBackend) handleTextEvent(event codeartsEvent, ch chan<- Message, output *strings.Builder) {
	text := event.Part.Text
	if text != "" {
		output.WriteString(text)
		trySend(ch, Message{Type: MessageText, Content: text})
	}
}

// handleToolUseEvent 处理 codearts 的 "tool_use" 事件。当工具到达终态（state.status 为
// "completed" 或 "error"）时，单条 tool_use 事件会同时携带调用与结果（都在 part.state 里）。
func (b *codeartsBackend) handleToolUseEvent(event codeartsEvent, ch chan<- Message) {
	// 从 state.input（工具调用参数）解析入参。
	var input map[string]any
	if event.Part.State != nil && event.Part.State.Input != nil {
		_ = json.Unmarshal(event.Part.State.Input, &input)
	}

	// 发出工具调用消息。
	trySend(ch, Message{
		Type:   MessageToolUse,
		Tool:   event.Part.Tool,
		CallID: event.Part.CallID,
		Input:  input,
	})

	// 为每个到达终态的工具调用配对一条工具结果。daemon 用这对「调用/结果」跟踪在飞工具，
	// 丢掉错误结果会让它的计数一直偏高、压住正常的空闲看门狗。
	state := event.Part.State
	if state != nil && (state.Status == "completed" || state.Status == "error") {
		outputStr := codeArtsExtractToolOutput(state.Output)
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

// handleErrorEvent 处理 codearts 的 "error" 事件。CodeArts 出错时（例如非法模型）退出码仍
// 可能是 0，所以 error 事件才是判定失败最可靠的信号。
func (b *codeartsBackend) handleErrorEvent(event codeartsEvent, ch chan<- Message, finalStatus, finalError *string) {
	errMsg := ""
	if event.Error != nil {
		errMsg = event.Error.Message()
	}
	if errMsg == "" {
		errMsg = "unknown codearts error"
	}

	b.cfg.Logger.Warn("codearts error event", "error", errMsg)
	trySend(ch, Message{Type: MessageError, Content: errMsg})

	*finalStatus = "failed"
	*finalError = errMsg
}

// resolveCodeArtsNativeFromShim 返回 Windows 启动器旁捆绑的原生可执行文件。启动器本身是
// 批处理文件，prompt 里带换行或超大负载时 daemon 无法可靠地拉起它。
func resolveCodeArtsNativeFromShim(shimPath string, statFn func(string) (os.FileInfo, error)) string {
	if !strings.EqualFold(filepath.Base(shimPath), "codearts.cmd") {
		return ""
	}
	candidate := filepath.Join(filepath.Dir(shimPath), "bin", "codearts.exe")
	if _, err := statFn(candidate); err != nil {
		return ""
	}
	return candidate
}

// codeArtsExtractToolOutput 把工具 state 的输出（可能是字符串或结构化对象）转成字符串。
func codeArtsExtractToolOutput(output any) string {
	if output == nil {
		return ""
	}
	if s, ok := output.(string); ok {
		return s
	}
	data, _ := json.Marshal(output)
	return string(data)
}

// ── `codearts run --format json` 的 stdout 事件 JSON 类型 ──

// codeartsEvent 代表 `codearts run --format json` 输出的一行 JSON 事件。
//
// 真实输出里观察到的类型：
//
//	"step_start"  — agent 一步开始
//	"text"        — agent 的文本输出（part.text）
//	"tool_use"    — 工具调用，含调用与结果（part.tool、part.callID、part.state）
//	"error"       — codearts 报错（error.name、error.data.message）
//	"step_finish" — agent 一步完成（含 token 用量）
type codeartsEvent struct {
	Type      string            `json:"type"`
	Timestamp int64             `json:"timestamp,omitempty"`
	SessionID string            `json:"sessionID,omitempty"`
	Part      codeartsEventPart `json:"part"`
	Error     *codeartsError    `json:"error,omitempty"`
}

// codeartsEventPart 代表 codearts 事件里的 part 字段。
type codeartsEventPart struct {
	ID        string `json:"id,omitempty"`
	MessageID string `json:"messageID,omitempty"`
	SessionID string `json:"sessionID,omitempty"`
	Type      string `json:"type,omitempty"`

	// 文本事件
	Text string `json:"text,omitempty"`

	// 工具调用事件
	Tool   string             `json:"tool,omitempty"`
	CallID string             `json:"callID,omitempty"`
	State  *codeartsToolState `json:"state,omitempty"`
	// CodeArts 在决定「某工具结果是否还需要再走一个模型 step」时会排除 provider 自己执行
	// 的工具。
	Metadata *codeartsPartMetadata `json:"metadata,omitempty"`

	// step_finish 的 token 用量
	Tokens *codeartsTokens `json:"tokens,omitempty"`

	// step_finish 的成本：它是 token 块的兄弟字段而不是成员。codeArtsStepReportedUsage 只把
	// 它当作「往返发生」的证据读；codearts 的计费数字来自上面的 token 计数。
	Cost float64 `json:"cost,omitempty"`

	// step_finish 的 reason（FinishReason: "stop"、"tool-calls" 等）。更老的 codearts 版本里
	// step_finish 部分早于该字段，因此可能缺失。
	Reason string `json:"reason,omitempty"`
}

// codeartsPartMetadata 承载工具调用是否有 provider 代执行（providerExecuted）的元数据。
type codeartsPartMetadata struct {
	ProviderExecuted bool `json:"providerExecuted,omitempty"`
}

// codeartsTokens 表示 step_finish 事件里的 token 用量。Reasoning 与 Total 在协议中是独立
// 计数器，不是 Input/Output 的组成部分——一个 step 完全可以只上报其中某一项而 Input/Output
// 都为 0；它们被解析出来是为了让 codeArtsStepReportedUsage 能看到。
type codeartsTokens struct {
	Input     int64                `json:"input"`
	Output    int64                `json:"output"`
	Reasoning int64                `json:"reasoning,omitempty"`
	Total     int64                `json:"total,omitempty"`
	Cache     *codeartsCacheTokens `json:"cache,omitempty"`
}

// codeartsCacheTokens 表示缓存命中的 token 用量（读/写）。
type codeartsCacheTokens struct {
	Read  int64 `json:"read"`
	Write int64 `json:"write"`
}

// codeartsToolState 表示一次工具调用的状态：终态判定看 Status，Input/Output/Error 分别是
// 调用入参、输出与错误。
type codeartsToolState struct {
	Status string          `json:"status,omitempty"`
	Input  json.RawMessage `json:"input,omitempty"`
	Output any             `json:"output,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// codeartsError 表示 codearts 的 error 事件。
type codeartsError struct {
	Name string           `json:"name,omitempty"`
	Data *codeartsErrData `json:"data,omitempty"`
}

// Message 返回人可读的错误消息：优先用 data.message，其次用 name，都没有则返回空串。
func (e *codeartsError) Message() string {
	if e.Data != nil && e.Data.Message != "" {
		return e.Data.Message
	}
	if e.Name != "" {
		return e.Name
	}
	return ""
}

// codeartsErrData 是 error 事件里携带具体错误消息的结构。
type codeartsErrData struct {
	Message string `json:"message,omitempty"`
}

// buildCodeArtsEnv 复现官方 CodeArts 启动器设置的环境变量（用于 daemon 绕开其 Windows
// 批处理 shim 的场景）。agent 的 custom_env 优先于这些默认值，与其它后端契约保持一致。
func buildCodeArtsEnv(extra map[string]string) []string {
	defaults := map[string]string{
		"package_tag":                    "undefined",
		"CODEAGENT_USE_MESSAGE_FEEDBACK": "false",
		"OMO_SEND_ANONYMOUS_TELEMETRY":   "0",
		"SCENARIO":                       "codeartsdoer",
		"OPENCODE_CHANNEL":               "latest",
		"OPENCODE_CONFIG_FILE":           "codearts_cli.json,codearts_cli.jsonc",
		"OPENCODE_MODE":                  "tui",
		"PLUGIN_ENV":                     "hc",
		"OPENCODE_DISABLE_MODELS_FETCH":  "1",
		"OPENCODE_DISABLE_AUTOUPDATE":    "true",
		"OPENCODE_ALWAYS_NOTIFY_UPDATE":  "false",
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		configDir := filepath.Join(home, ".codeartsdoer")
		defaults["KERNEL_DATA_DIR"] = filepath.Join(configDir, "cli-data")
		defaults["KERNEL_CONFIG_DIR"] = configDir
		defaults["OPENCODE_CONFIG"] = filepath.Join(configDir, "codearts_cli.json")
	}
	for key, value := range extra {
		defaults[key] = value
	}
	return mergeEnv(os.Environ(), defaults)
}

// buildCodeArtsMCPConfigContent 使用 CodeArts 引擎与 OpenCode 共享的配置 schema 组装 MCP
// 注入内容。共享的只有这一处 schema 翻译；CodeArts 的进程与事件适配仍是独立的。
func buildCodeArtsMCPConfigContent(raw json.RawMessage) (string, error) {
	return buildOpenCodeMCPConfigContent(raw)
}

// discoverCodeArtsModels 用 CodeArts 暴露的模型目录命令探测可用模型。CodeArts 不暴露
// OpenCode 的 variant 维度，因此把 variant 元数据去掉。
func discoverCodeArtsModels(ctx context.Context, runtimeCmd Command) ([]Model, error) {
	if runtimeCmd.Path == "" {
		runtimeCmd.Path = "codearts"
	}
	resolved, err := exec.LookPath(runtimeCmd.Path)
	if err != nil {
		return []Model{}, nil
	}
	runtimeCmd.Path = resolved
	if runtime.GOOS == "windows" {
		if native := resolveCodeArtsNativeFromShim(runtimeCmd.Path, os.Stat); native != "" {
			runtimeCmd.Path = native
		}
	}

	runCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	run := func(args ...string) []Model {
		cmd := runtimeCmd.exec(runCtx, args...)
		hideAgentWindow(cmd)
		cmd.Env = buildCodeArtsEnv(nil)
		out, _ := outputOwned(cmd, runtimeCmd.logger)
		models := parseOpenCodeModels(string(out))
		for i := range models {
			models[i].Thinking = nil
		}
		return models
	}

	models := run("models", "--verbose")
	if len(models) == 0 {
		models = run("models")
	}
	return models, nil
}

// ansiControlSequenceRe 匹配 ANSI 转义控制序列（ESC 开头的 CSI/OSC 序列），用于清洗 CLI
// 输出里的着色/光标控制字符。
var ansiControlSequenceRe = regexp.MustCompile(`\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07]*(?:\x07|\x1b\\))`)

// sanitizeCLIOutput 把可能夹带 ANSI 转义与回车符的原始 CLI 输出清洗成干净的纯文本。
func sanitizeCLIOutput(raw string) string {
	raw = ansiControlSequenceRe.ReplaceAllString(raw, "")
	raw = strings.ReplaceAll(raw, "\r", "")
	return strings.TrimSpace(raw)
}
