package agent

// 文件导读：OpenCode CLI 的驱动封装。
//
// 角色定位：把 OpenCode（anomalyco/opencode，通用编程智能体 CLI）适配成 daemon 的 Backend。
// 执行方式是一次性 `opencode run --format json` 子进程：任务 prompt 走 stdin 喂入，agent 的
// 正文、工具调用、token 用量等以「每行一个 JSON 事件」输出在 stdout，本文件把这些事件翻译成
// Message/Result 交给上层会话——与 Claude 后端同一条「流式 JSON 事件」路线。
//
// 职责边界：只管 OpenCode 这一个 agent。CodeArts 与 DevEco 后端都从本文件派生（命令面、事件
// 形态一致），对事件解析/终态判定的改动会影响它们，需一并留意。DevEco 有自己的 fork（dsh/
// deveco.go 等）。
//
// 核心约定：① prompt 永远走 stdin、不进 argv，规避 Windows 命令行长度上限与二次解析
// （#6538）；② `--dir`/PWD 双管齐下把 OpenCode 的 AGENTS.md / .opencode/skills 发现锚定到
// 任务 workdir，否则自托管部署里它会解析到用户 shell 的工作目录、静默绕过每任务的 workdir；
// ③ 事件流没有终态 result 事件，「没报错」不等于「跑完了」，判定成功须依赖 step 括号闭合
// 等正向终态证据（见 processEvents）；④ 取消/超时时先对整棵进程树 SIGTERM→SIGKILL、再关
// stdout 读端，避免孤儿子进程写已关闭管道空转（#4533）。

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
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// opencodeTerminateGraceNanos 可按纳秒覆盖「取消 opencode 进程后的宽限期」：进程收到 SIGTERM
// 后允许它优雅退出多久，超时再连同整棵进程组一起 SIGKILL。值为 0 表示用默认宽限。用原子类型
// 是为了让测试能缩短宽限期，而不必与读取它的取消 goroutine 竞争。关于「为什么必须先终止进程
// 再关 stdout 管道」见 Execute 里的取消处理（#4533）。
var opencodeTerminateGraceNanos atomic.Int64

// opencodeTerminateGrace 返回取消后的终止宽限期：测试覆盖了则用覆盖值，否则默认 5 秒。
func opencodeTerminateGrace() time.Duration {
	if n := opencodeTerminateGraceNanos.Load(); n > 0 {
		return time.Duration(n)
	}
	return 5 * time.Second
}

// opencodeBlockedArgs 是 daemon 固定写死、不允许用户经 custom_args 覆盖的开关。
var opencodeBlockedArgs = map[string]blockedArgMode{
	"--format":                       blockedWithValue,  // 与 daemon 通信用的 json 输出格式
	"--dir":                          blockedWithValue,  // 任务 workdir 锚点：skills / AGENTS.md 发现
	"--variant":                      blockedWithValue,  // 归 agent.thinking_level 管
	"--dangerously-skip-permissions": blockedStandalone, // 非交互权限提示由 daemon 统一处理
}

// opencodeBackend 通过拉起 `opencode run --format json`、从 stdout 读流式 JSON 事件来实现
// Backend——与 Claude 后端同一模式。
type opencodeBackend struct {
	cfg Config
}

// opencodeSeparatesReasoning recognizes the released OpenCode 1.x wire contract.
// v1.3.15 included reasoning in output; v1.3.16 separated it in upstream #21047:
// https://github.com/anomalyco/opencode/pull/21047
// Use the daemon's already-resolved version, never an extra per-run probe.
// Unknown/dev versions and custom commands keep the existing output count:
// their version string does not establish which usage convention they speak.
func opencodeSeparatesReasoning(cfg Config) bool {
	if !cfg.BuiltinRuntime {
		return false
	}
	raw := strings.TrimSpace(cfg.CLIVersion)
	if raw == "" || versionRe.FindString(raw) != raw {
		return false
	}
	version, err := parseSemver(raw)
	return err == nil && version.Major == 1 && !version.lessThan(semver{Major: 1, Minor: 3, Patch: 16})
}

func (b *opencodeBackend) Execute(ctx context.Context, prompt string, opts ExecOptions) (*Session, error) {
	execPath := b.cfg.ExecutablePath
	if execPath == "" {
		execPath = "opencode"
	}
	resolved, err := exec.LookPath(execPath)
	if err != nil {
		return nil, fmt.Errorf("opencode executable not found at %q: %w", execPath, err)
	}
	if runtime.GOOS == "windows" {
		if native := resolveOpenCodeNativeFromShim(resolved, os.Stat); native != "" {
			b.cfg.Logger.Info("opencode resolved to native binary to avoid .cmd shim argv truncation", "shim", resolved, "native", native)
			resolved = native
		}
	}
	execPath = resolved

	timeout := opts.Timeout
	runCtx, cancel := runContext(ctx, timeout)

	args := []string{"run", "--format", "json", "--dangerously-skip-permissions"}
	// 把 OpenCode 的项目发现（向上找 AGENTS.md + .opencode/skills/ 项目配置扫描）锚定到任务
	// workdir。不做这一步，OpenCode 会回退到 PWD（继承自 daemon 进程）或 process.cwd()——
	// 自托管部署里这俩可能解析到用户 shell 的工作目录，静默绕过每任务的 workdir，agent 就看
	// 不到分配给它的 skills 与 AGENTS.md 指令。下面的 PWD 也要覆盖：缺 `--dir` 时 OpenCode
	// 优先用 PWD 而不是 cwd，并以它为后续一切路径解析的起点。
	if opts.Cwd != "" {
		args = append(args, "--dir", opts.Cwd)
	}
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}
	if opts.ThinkingLevel != "" {
		args = append(args, "--variant", opts.ThinkingLevel)
	}
	// OpenCode 的 `run` 子命令没有 --prompt 选项——传了会让 CLI 在发出任何内容前就退出 1 并
	// 吐一段 usage（对 OpenCode 1.17.7 验证过）。因此 SystemPrompt 也从不透传：运行时简介经
	// daemon 写入 workdir 的分任务 AGENTS.md 到达 agent，OpenCode 会自行加载它（MUL-5392）。
	// 与从本文件 fork 出去的 DevEco 后端受同一约束。
	if opts.MaxTurns > 0 {
		b.cfg.Logger.Warn("opencode does not support --max-turns; ignoring", "maxTurns", opts.MaxTurns)
	}
	if opts.ResumeSessionID != "" {
		args = append(args, "--session", opts.ResumeSessionID)
	}
	args = append(args, filterCustomArgs(opts.CustomArgs, opencodeBlockedArgs, b.cfg.Logger)...)
	// 任务 prompt 只走 stdin、绝不进 argv（stdin 管道的接法见下）。`opencode run` 会把它的
	// 变长参数 [message..] 位置消息与管道输入合并，因此不传任何位置消息时，管道内容就是完整
	// 的运行消息。若改为内联进参数，在 Windows 上会硬性失败：CreateProcess 把 lpCommandLine
	// 限制在 32,767 字符（经 .cmd shim 由 cmd.exe 转发时只有 8,191），而一条带着工作区模型与
	// 技能清单的 prompt 自己就轻松超过——进程根本起不来，Go 还只会抛一句误导性的
	// "The filename or extension is too long"（#6538）。让 prompt 不进 argv 同时也让它不出现在
	// 系统进程列表里；共享的命令 logger 会另行对 argv 里的敏感值做脱敏。

	cmd := b.cfg.commandAt(execPath).exec(runCtx, args...)
	hideAgentWindow(cmd)
	// 接管 context 取消逻辑。默认行为会在 runCtx 结束瞬间杀死整个进程组；这里改为由下面的
	// 取消 goroutine 驱动「先对整组 SIGTERM、宽限期后再 SIGKILL」的优雅终止，并且只在进程树
	// 已被发信号之后才关闭 stdout 读端——否则一次被取消的运行可能留下继续空转的后代
	// （#4533）。把 Cancel 设为 no-op 返回 nil，让 os/exec 不会用自带的 kill 来和我们抢；
	// WaitDelay 仍作为最后的兜底。
	cmd.Cancel = func() error { return nil }
	b.cfg.logAgentCommandWithPrompt(cmd, newAgentCommandLogArgs(args, trustAgentCommandPositional(0, "run")), len(prompt))
	cmd.WaitDelay = 10 * time.Second
	if opts.Cwd != "" {
		cmd.Dir = opts.Cwd
	}

	env := buildEnv(b.cfg.Env)
	// 让 daemon 模式保持非交互，而不依赖 OPENCODE_PERMISSION。OpenCode 会把这个环境覆盖
	// 深度合并进用户配置且保留原有键序，因此一条既有的 permission.question 键可能后面跟着
	// 通配符 allow，从而绕过本意的「提问即拒绝」。当前 OpenCode run 会话会在 agent 配置之后
	// 注入自己的 question/plan 拒绝规则；这个开关只负责回答那些绕过显式拒绝后仍存活的提问。
	// 覆盖 PWD 让子进程 OpenCode 把它的发现根目录解析到任务 workdir。光设 cmd.Dir 不够：
	// OpenCode 计算它向上找 AGENTS.md / .opencode/skills 的目录时，先读 PWD（继承自父 daemon）
	// 才回退到 process.cwd()。参见上游源码 packages/opencode/src/cli/cmd/run.ts。
	if opts.Cwd != "" {
		env = append(env, "PWD="+opts.Cwd)
	}
	// 把 agent.mcp_config 经 OPENCODE_CONFIG_CONTENT 注入 OpenCode——这是 OpenCode 通用的
	// 内联配置注入通道，会以 "local" 作用域并入（在项目配置遍历之后、远端/托管配置之前）。
	// 目前只有 MCP 走这条通道；将来若有别的 Multica 字段需要同一条通道，就在这次 env 拼接
	// 前先组装一份合并的 OpenCode 配置片段。
	//
	// 刻意不改动 <workdir>/opencode.json：同一 (agent, issue) 的 workdir 会在多轮之间复用，
	// 其中由 agent 或用户写下的模型/工具/权限设置必须跨运行存活。
	mcpContent, err := buildOpenCodeMCPConfigContent(opts.McpConfig)
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
		return nil, fmt.Errorf("opencode stdout pipe: %w", err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("opencode stdin pipe: %w", err)
	}
	var closeStdinOnce sync.Once
	closeStdin := func() { closeStdinOnce.Do(func() { _ = stdin.Close() }) }
	cmd.Stderr = newLogWriter(b.cfg.Logger, "[opencode:stderr] ")

	if err := startOwnedProcessTree(cmd, b.cfg.Logger); err != nil {
		closeStdin()
		cancel()
		return nil, fmt.Errorf("start opencode: %w", err)
	}

	b.cfg.Logger.Info("opencode started", "pid", cmd.Process.Pid, "cwd", opts.Cwd, "model", opts.Model)

	msgCh := make(chan Message, 256)
	resCh := make(chan Result, 1)

	// procDone 在 cmd.Wait() 返回后关闭，让取消处理器能跳过已经退出的进程、不对已死的 pid
	// 发信号。
	procDone := make(chan struct{})

	// prompt 由独立 goroutine 写，避免与下面的 stdout 读取互相死锁：prompt 超过系统管道缓冲
	// （约 64 KiB）时，写方会阻塞到 OpenCode 排空为止，而 OpenCode 在没有人在读它的 stdout
	// 时也无法继续。关闭 stdin 即宣告 prompt 结束——OpenCode 会一直读到 EOF
	// （`await Bun.stdin.text()`），stdin 不关会让运行永远挂起。成功与出错路径都要关。
	writeErrCh := make(chan error, 1)
	go func() {
		_, err := io.WriteString(stdin, prompt)
		closeStdin()
		writeErrCh <- err
	}()

	// 取消/超时时，先终止 opencode（及其派生的工具子进程），再去放开扫描器。旧实现会立即
	// 关闭 stdout 读端，让 opencode 继续往已关闭的管道写：每次写都返回 EPIPE，据
	// anomalyco/opencode#33653，还可能在孤儿进程里以 100% CPU 空转。这里改为对整棵进程组
	// SIGTERM，给一段宽限期让其干净退出，再 SIGKILL。SIGKILL 不可被捕获，一旦送达，组内任何
	// 成员都无法再运行（或再写），只有这时才适合关掉 stdout 读端——作为对「被一个卡死的后代
	// 继续攥着的扫描器」的最后解锁手段。WaitDelay 是最终兜底（#4533）。
	go func() {
		select {
		case <-procDone:
			return // 自行正常结束，无需终止
		case <-runCtx.Done():
		}
		// 先释放仍被写满的 stdin 管道卡住的 prompt 写入——若 OpenCode 在排空前就停止读取，
		// 那个 goroutine 会随着 daemon 一直滞留。
		closeStdin()
		if cmd.Process != nil {
			signalProcessGroup(cmd, syscall.SIGTERM)
			select {
			case <-procDone: // 在宽限期内退出了
			case <-time.After(opencodeTerminateGrace()):
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

		// Wait 会关掉进程管道，因此 OpenCode 退出时还被卡住的 prompt 写入此刻已返回。
		// 写方只发送一次，读这一下不会阻塞。
		writeErr := <-writeErrCh

		if runCtx.Err() == context.DeadlineExceeded {
			scanResult.status = "timeout"
			scanResult.errMsg = fmt.Sprintf("opencode timed out after %s", timeout)
		} else if runCtx.Err() == context.Canceled {
			scanResult.status = "aborted"
			scanResult.errMsg = "execution cancelled"
		} else if exitErr != nil && scanResult.status == "completed" {
			scanResult.status = "failed"
			scanResult.errMsg = fmt.Sprintf("opencode exited with error: %v", exitErr)
		} else if exitErr != nil && scanResult.noTerminalSignal {
			// 状态已由「无终态信号」守卫置为 failed；这里再补上进程退出细节，让中途崩溃仍能
			// 呈现真正杀死它的信号/退出码。
			scanResult.errMsg = fmt.Sprintf("%s; opencode exited with error: %v", scanResult.errMsg, exitErr)
		} else if writeErr != nil && !scanResult.sawTerminalSignal {
			// prompt 写入失败只有在「已确证运行结束」时才可视为无害：OpenCode 干任何活之前
			// 都会先把 stdin 读到 EOF，所以一个能走到终态信号的运行必然收全了整个 prompt；
			// 之后才记下的 EPIPE 只是管道在收尾关闭，若据此判失败会丢弃一个成功结果。
			//
			// 「没失败」并不等于「跑完了」。status 初始为 "completed"，processEvents 只在拿到
			// 结构性证据时才 fail-closed，因此一个什么都不输出、退出码却是 0 的子进程仍会报
			// "completed"。若 prompt 根本没送到，那恰恰是绝不能冒充干净成功的运行——所以要
			// 以 sawTerminalSignal 为准。用追加而不是覆盖，保留事件流自己的诊断。
			if scanResult.errMsg == "" {
				scanResult.errMsg = fmt.Sprintf("opencode prompt write failed: %v", writeErr)
			} else {
				scanResult.errMsg = fmt.Sprintf("%s; opencode prompt write failed: %v", scanResult.errMsg, writeErr)
			}
			scanResult.status = "failed"
		}

		b.cfg.Logger.Info("opencode finished", "pid", cmd.Process.Pid, "status", scanResult.status, "duration", duration.Round(time.Millisecond).String())

		// 组装 usage map。OpenCode 不按步上报模型名，因此把全部用量记到配置的模型（或
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

// eventResult 汇总一次事件流处理过程中累积下来的状态。
type eventResult struct {
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
func (b *opencodeBackend) processEvents(r io.Reader, ch chan<- Message) eventResult {
	var output strings.Builder
	var sessionID string
	var usage TokenUsage
	separateReasoning := opencodeSeparatesReasoning(b.cfg)
	finalStatus := "completed"
	var finalError string

	// 用 step 的「括号配对」来记账，避免把一条停在半路的流误当成干净完成。OpenCode 的 JSON
	// 流没有终态 result 事件（不像 Claude 的 type:"result"），所以「没看到错误」不足以证明
	// 运行结束。opencode 只在终态（completed 或 error）发 tool_use，因此悬空的工具调用意味着
	// 有一个未闭合的 step——step 括号闭合才是正向的终态信号。工具自身已恢复的错误
	// （state.status == "error"）在健康运行里很常见，不得影响状态判定。
	//
	// 单靠 step 括号还不够：step_finish 带 reason（FinishReason: "stop"、"tool-calls" 等），
	// 而仍有工具结果待回喂的运行通常以 reason "tool-calls" 闭合当前 step、再进下一个
	// step_start。不过有些 provider 明明发了工具调用却回 "stop"，且 OpenCode 在「必须把由非
	// provider 执行的工具结果回喂给模型」时仍会刻意继续这类运行。所以要同时跟踪两个信号：
	// 只要在任一种「续接间隙」里遇到 EOF 就 fail-closed。reason 缺失时保留旧版 step 括号
	// 行为，以兼容协议。
	openStep := false                // 处于某次 step_start 与对应 step_finish 之间
	stepHasContinuationTool := false // 当前 step 有一个需要 OpenCode 回喂的本地工具结果
	awaitingContinuation := false    // 最近一次 step_finish 之后还需要另一个 step
	sawStepFinish := false           // 至少合上过一个 step；参见 eventResult.sawTerminalSignal

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

		var event opencodeEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}

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
			// 只有「需要回喂给模型的工具结果」才构成续接信号；provider 自己执行完的工具不
			// 产生续接 step。
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
				if separateReasoning && t.Reasoning > 0 {
					usage.OutputTokens += t.Reasoning
				}
				if t.Cache != nil {
					usage.CacheReadTokens += t.Cache.Read
					usage.CacheWriteTokens += t.Cache.Write
				}
			}
			if stepReportedUsage(&event.Part) {
				stepProducedOutput = true
			}
			lastStepVoid = !stepProducedOutput
		}
	}

	// 检查扫描器错误（如管道破裂、读取错误）。
	if scanErr := scanner.Err(); scanErr != nil {
		b.cfg.Logger.Warn("opencode stdout scanner error", "error", scanErr)
		if finalStatus == "completed" {
			finalStatus = "failed"
			finalError = fmt.Sprintf("stdout read error: %v", scanErr)
		}
	}

	// 要求拿到正向终态信号。以下情况属于运行没跑完，却撞上了干净的 EOF：step 仍开着（刚合上
	// 一个 reason 为 "tool-calls"、续接 step 却没开始的 step），或刚合上的是个什么都没带的
	// step——provider 的流已死，而 `opencode run` 退出时连 error 事件都没发。对这种结构性
	// 证据要 fail-closed，而不是报一个假绿的完成。
	noTerminalSignal := false
	if finalStatus == "completed" {
		switch {
		case openStep:
			finalStatus = "failed"
			finalError = "opencode stream ended without a terminal signal (step still open at EOF)"
			noTerminalSignal = true
		case awaitingContinuation:
			finalStatus = "failed"
			finalError = "opencode stream ended without a terminal signal (last step required a continuation that never started)"
			noTerminalSignal = true
		case lastStepVoid:
			finalStatus = "failed"
			finalError = "opencode stream ended on an empty step (no text, no tool call, no reported usage) — the provider produced nothing"
			noTerminalSignal = true
		}
	}

	return eventResult{
		status:            finalStatus,
		errMsg:            finalError,
		output:            output.String(),
		sessionID:         sessionID,
		usage:             usage,
		noTerminalSignal:  noTerminalSignal,
		sawTerminalSignal: sawStepFinish && !noTerminalSignal,
	}
}

// stepReportedUsage 报告某次 step_finish 的 part 是否带有「provider 往返确实发生过」的证据。
//
// OpenCode 协议把 reasoning 与合计放在独立的字段里（与 input/output/cache 并列），成本
// （cost）则是整个 token 块的兄弟字段——一个 step 完全可能 reasoning 或 cost 为正而
// input/output 都是 0。若只查 input/output，会把这种 step 判成空洞、进而让一次健康运行
// 失败，所以协议上报的每个字段都算数；只有全部为 0 才意味着没有发生模型调用。
//
// reasoning 与 total 计数只作为证据读取，刻意不折进 TokenUsage：total 是派生量（加进去会
// 重复计费），而 TokenUsage 又没有 reasoning 桶——把两者记进这里会改动计费数字而不是修好
// 这个 bug。
func stepReportedUsage(part *opencodeEventPart) bool {
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
func (b *opencodeBackend) handleTextEvent(event opencodeEvent, ch chan<- Message, output *strings.Builder) {
	text := event.Part.Text
	if text != "" {
		output.WriteString(text)
		trySend(ch, Message{Type: MessageText, Content: text})
	}
}

// handleToolUseEvent 处理 opencode 的 "tool_use" 事件。当工具到达终态（state.status 为
// "completed" 或 "error"）时，单条 tool_use 事件会同时携带调用与结果（都在 part.state 里）。
func (b *opencodeBackend) handleToolUseEvent(event opencodeEvent, ch chan<- Message) {
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
		outputStr := extractToolOutput(state.Output)
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

// handleErrorEvent 处理 opencode 的 "error" 事件。OpenCode 出错时（例如非法模型）退出码仍
// 可能是 0，所以 error 事件才是判定失败最可靠的信号。
func (b *opencodeBackend) handleErrorEvent(event opencodeEvent, ch chan<- Message, finalStatus, finalError *string) {
	errMsg := ""
	if event.Error != nil {
		errMsg = event.Error.Message()
	}
	if errMsg == "" {
		errMsg = "unknown opencode error"
	}

	b.cfg.Logger.Warn("opencode error event", "error", errMsg)
	trySend(ch, Message{Type: MessageError, Content: errMsg})

	*finalStatus = "failed"
	*finalError = errMsg
}

// resolveOpenCodeNativeFromShim 给定 PATH 在 Windows 上找到的 npm `opencode.cmd` shim 路径，
// 返回该 npm 包内捆绑的原生 OpenCode 可执行文件路径。shim 不是以 `.cmd` 结尾、或没有任何
// npm 平台包带捆绑原生二进制时返回 ""。
//
// Windows 经 `%*` 的批处理参数转发不保留换行，所以多行的位置 argv 在 shim 转交 JS 入口前就
// 被截断到第一个换行。daemon 的 prompt 可以带字面换行（系统提示词 + 用户消息），会导致 agent
// 只看到第一行。绕过 cmd.exe 层、直接 spawn 原生二进制则没有这个问题。
//
// 经 `npm install -g opencode-ai` 安装时的布局：
//
//	<prefix>\opencode.cmd                                                                       (shim)
//	<prefix>\node_modules\opencode-ai\node_modules\opencode-windows-{x64,x64-baseline,arm64}\bin\opencode.exe (原生)
//
// `opencode-windows-x64-baseline` 面向没有 AVX2 的老 CPU；`opencode-windows-arm64` 面向
// Surface / Copilot+ PC 等 ARM 宿主。候选按 GOARCH 优先的顺序逐个尝试，让当前宿主最可能命中
// 的包排在最前。
//
// statFn 是注入进来的，这样能在非 Windows 宿主上测试本函数。
func resolveOpenCodeNativeFromShim(shimPath string, statFn func(string) (os.FileInfo, error)) string {
	if !strings.EqualFold(filepath.Ext(shimPath), ".cmd") {
		return ""
	}
	prefix := filepath.Dir(shimPath)
	for _, pkg := range opencodeWindowsPackageCandidates(runtime.GOARCH) {
		candidate := filepath.Join(prefix, "node_modules", "opencode-ai", "node_modules", pkg, "bin", "opencode.exe")
		if _, err := statFn(candidate); err == nil {
			return candidate
		}
	}
	return ""
}

// opencodeWindowsPackageCandidates 返回 Windows 上可能装着捆绑 `opencode.exe` 的 npm 平台包
// 名，顺序按给定 GOARCH 下「最可能命中」排列：ARM64 宿主先试 arm64 构建；其它宿主先 x64、
// 再面向老 CPU 的 baseline x64，最后把 arm64 当最终兜底。代价只是 GOARCH 优先的包没装时，
// 每次未命中多一次 statFn 调用。
func opencodeWindowsPackageCandidates(goarch string) []string {
	switch goarch {
	case "arm64":
		return []string{"opencode-windows-arm64", "opencode-windows-x64", "opencode-windows-x64-baseline"}
	default:
		return []string{"opencode-windows-x64", "opencode-windows-x64-baseline", "opencode-windows-arm64"}
	}
}

// extractToolOutput 把工具 state 的输出（可能是字符串或结构化对象）转成字符串。
func extractToolOutput(output any) string {
	if output == nil {
		return ""
	}
	if s, ok := output.(string); ok {
		return s
	}
	data, _ := json.Marshal(output)
	return string(data)
}

// ── `opencode run --format json` 的 stdout 事件 JSON 类型 ──

// opencodeEvent 代表 `opencode run --format json` 输出的一行 JSON 事件。
//
// 真实输出里观察到的类型：
//
//	"step_start"  — agent 一步开始
//	"text"        — agent 的文本输出（part.text）
//	"tool_use"    — 工具调用，含调用与结果（part.tool、part.callID、part.state）
//	"error"       — opencode 报错（error.name、error.data.message）
//	"step_finish" — agent 一步完成（含 token 用量）
type opencodeEvent struct {
	Type      string            `json:"type"`
	Timestamp int64             `json:"timestamp,omitempty"`
	SessionID string            `json:"sessionID,omitempty"`
	Part      opencodeEventPart `json:"part"`
	Error     *opencodeError    `json:"error,omitempty"`
}

// opencodeEventPart 代表 opencode 事件里的 part 字段。
type opencodeEventPart struct {
	ID        string `json:"id,omitempty"`
	MessageID string `json:"messageID,omitempty"`
	SessionID string `json:"sessionID,omitempty"`
	Type      string `json:"type,omitempty"`

	// 文本事件
	Text string `json:"text,omitempty"`

	// 工具调用事件
	Tool   string             `json:"tool,omitempty"`
	CallID string             `json:"callID,omitempty"`
	State  *opencodeToolState `json:"state,omitempty"`
	// OpenCode 在决定「某工具结果是否还需要再走一个模型 step」时会排除 provider 自己执行的
	// 工具。
	Metadata *opencodePartMetadata `json:"metadata,omitempty"`

	// step_finish 的 token 用量
	Tokens *opencodeTokens `json:"tokens,omitempty"`

	// step_finish 的成本：它是 token 块的兄弟字段而不是成员。stepReportedUsage 只把它当作
	// 「往返发生」的证据读；opencode 的计费数字来自上面的 token 计数。
	Cost float64 `json:"cost,omitempty"`

	// step_finish 的 reason（FinishReason: "stop"、"tool-calls" 等）。更老的 opencode 版本里
	// step_finish 部分早于该字段，因此可能缺失。
	Reason string `json:"reason,omitempty"`
}

// opencodePartMetadata 承载工具调用是否有 provider 代执行（providerExecuted）的元数据。
type opencodePartMetadata struct {
	ProviderExecuted bool `json:"providerExecuted,omitempty"`
}

// opencodeTokens 表示 step_finish 事件里的 token 用量。Reasoning 与 Total 在协议中是独立
// 计数器，不是 Input/Output 的组成部分——一个 step 完全可以只上报其中某一项而 Input/Output
// 都为 0；它们被解析出来是为了让 stepReportedUsage 能看到。
type opencodeTokens struct {
	Input     int64                `json:"input"`
	Output    int64                `json:"output"`
	Reasoning int64                `json:"reasoning,omitempty"`
	Total     int64                `json:"total,omitempty"`
	Cache     *opencodeCacheTokens `json:"cache,omitempty"`
}

// opencodeCacheTokens 表示缓存命中的 token 用量（读/写）。
type opencodeCacheTokens struct {
	Read  int64 `json:"read"`
	Write int64 `json:"write"`
}

// opencodeToolState 表示一次工具调用的状态：终态判定看 Status，Input/Output/Error 分别是
// 调用入参、输出与错误。
type opencodeToolState struct {
	Status string          `json:"status,omitempty"`
	Input  json.RawMessage `json:"input,omitempty"`
	Output any             `json:"output,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// opencodeError 表示 opencode 的 error 事件。
type opencodeError struct {
	Name string           `json:"name,omitempty"`
	Data *opencodeErrData `json:"data,omitempty"`
}

// Message 返回人可读的错误消息：优先用 data.message，其次用 name，都没有则返回空串。
func (e *opencodeError) Message() string {
	if e.Data != nil && e.Data.Message != "" {
		return e.Data.Message
	}
	if e.Name != "" {
		return e.Name
	}
	return ""
}

// opencodeErrData 是 error 事件里携带具体错误消息的结构。
type opencodeErrData struct {
	Message string `json:"message,omitempty"`
}
