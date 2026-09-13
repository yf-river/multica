package agent

// 角色定位：本文件是 Hermes 的适配层，实现 Backend 接口：拉起 `hermes acp` 子进程，
// 通过 stdin/stdout 上的 ACP（Agent Communication Protocol）JSON-RPC 2.0 与它对话。
// ACP 全家（kimi/kiro/grok/dim/…）复用的传输、解析与工具缓冲逻辑多数也以本文件为宿主。

// 职责边界：管「profile 参数解析、进程与 ACP 会话生命周期、回合内容流、权限自动应答、
// 供应商错误嗅探」整条链；不管某家供应商的会话持久化（在 daemon）与目录能力声明
// （thinking.go/models.go）。

// 内部组织：开头是 -p/--profile 解析与启动 argv 归一化（overlay 播种要用）；随后
// hermesBackend.Execute 的进程编排与两阶段排空；再往下的 hermesClient 承载 ACP 的
// JSON-RPC 传输、session/new→prompt 生命周期、工具调用缓冲（kimi 流式 args 特判）、
// 权限自动选择（绝不在 no-overlay 上永久授权）；末尾是一批 ACP 后端共享的辅助函数与
// 供应商错误嗅探器。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// hermesBlockedArgs 是 daemon 硬编码、绝不允许被用户 custom_args 覆盖的启动参数。
// `acp` 是驱动 ACP JSON-RPC 传输的协议子命令，覆盖它会破坏 daemon↔Hermes 的通信契约。
//
// `-p`/`--profile` 不做无条件剥离：不带 skill 的 Hermes 任务没有 overlay，它的 profile
// 选择必须原样透传给 Hermes。daemon 只有在真正构建了任务级 overlay 时，才用
// StripHermesProfileArgs 剥离选中的那次出现（见 daemon 侧启动参数处理）——这样既能防止
// 该参数把 HERMES_HOME 重新指到 overlay 之外，又不影响无 overlay 任务的行为。
var hermesBlockedArgs = map[string]blockedArgMode{
	"acp": blockedStandalone,
}

// hermesArgProfileRe 复刻 Hermes hermes_cli.main._apply_profile_override 第 1b 步的
// 空格形式守卫：`-p <value>` 的值若不匹配 profile-id 形态（如 pytest 的 `-p no:xdist`）
// 就根本不是 profile 选择，应忽略而不是消费。内联 `--profile=<value>` 形式在这里不设防
// ——Hermes 会原样转发给 resolve_profile_env，后者对非法值校验并硬失败；那套校验在
// daemon 侧解析器（execenv.ResolveHermesProfile）里。
var hermesArgProfileRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// hermesValueFlags 与 hermesOptionalValueFlags 复刻 Hermes 在扫描 argv 找 -p/--profile
// 时要跳过的取值型参数，这样 `-m coder -p research` 里 `coder` 这个值不会被误当成
// profile。与 _apply_profile_override 的 value_flags / optional_value_flags 保持同步。
var hermesValueFlags = map[string]struct{}{
	"-z": {}, "--oneshot": {}, "-m": {}, "--model": {}, "--provider": {},
	"-t": {}, "--toolsets": {}, "-r": {}, "--resume": {}, "-s": {},
	"--skills": {}, "--usage-file": {},
}
var hermesOptionalValueFlags = map[string]struct{}{"-c": {}, "--continue": {}}

// HermesProfileSelection 是 ParseHermesProfileArgs 从 custom_args 里解析出的 profile
// 选择。它携带需要消费掉的精确 argv 出现位置，让 daemon 侧解析器与启动参数剥离基于同
// 一次权威解析行事，而不是各自近似 Hermes 的 argv 处理。
type HermesProfileSelection struct {
	Name    string // 选中的值；空内联 `--profile=` 值为 ""
	Found   bool   // 匹配到带值的 -p/--profile 选择
	Inline  bool   // 匹配的是 `--profile=<value>` 形式（值在下游校验）
	ArgFrom int    // 第一个要剥离的 token 下标；无匹配时为 -1
	ArgLen  int    // 要剥的 token 数：`-p <value>` 为 2，`--profile=<value>` 为 1
}

// ParseHermesProfileArgs 在 custom_args 里找第一个 Hermes profile 选择，复刻
// hermes_cli.main._apply_profile_override 第 1/1b 步：扫描第一个 `-p`/`--profile <value>`
// 或 `--profile=<value>`，跳过取值型参数，并在 `--` 哨兵或 `mcp add --args` 命令 argv
// 透传区停下来。空格形式的值若不符合 profile-id 形态则忽略（与 Hermes 丢弃它的行为一致）。
// 参数用与 filterCustomArgs 相同的 helper 去引号，保证引号处理口径一致。
func ParseHermesProfileArgs(args []string) HermesProfileSelection {
	none := HermesProfileSelection{ArgFrom: -1}
	i := 0
	for i < len(args) {
		arg := unshellQuoteArg(args[i])
		if arg == "--" {
			break
		}
		if arg == "--args" && hermesInsideMcpAdd(args, i) {
			break
		}
		if arg == "-p" || arg == "--profile" {
			if i+1 < len(args) {
				val := unshellQuoteArg(args[i+1])
				if !hermesArgProfileRe.MatchString(val) {
					return none // 第 1b 步：不是合法 profile 值，忽略
				}
				return HermesProfileSelection{Name: val, Found: true, ArgFrom: i, ArgLen: 2}
			}
			return none // 结尾处没有值的裸 flag
		}
		if v, ok := strings.CutPrefix(arg, "--profile="); ok {
			return HermesProfileSelection{Name: v, Found: true, Inline: true, ArgFrom: i, ArgLen: 1}
		}
		if _, ok := hermesValueFlags[arg]; ok && i+1 < len(args) {
			i += 2
			continue
		}
		if _, ok := hermesOptionalValueFlags[arg]; ok && i+1 < len(args) &&
			!strings.HasPrefix(unshellQuoteArg(args[i+1]), "-") {
			i += 2
			continue
		}
		i++
	}
	return none
}

// hermesInsideMcpAdd 判断 argv 位置 index 是否落在 `mcp add ... --args <子命令 argv>`
// 透传区里——那里的 flag 属于子 MCP 命令，绝不能当成 Hermes 自己的 profile 选择器读。
func hermesInsideMcpAdd(args []string, index int) bool {
	mcp := -1
	for j := 0; j < index; j++ {
		if unshellQuoteArg(args[j]) == "mcp" {
			mcp = j
			break
		}
	}
	if mcp < 0 {
		return false
	}
	for j := mcp + 1; j < index; j++ {
		if unshellQuoteArg(args[j]) == "add" {
			return true
		}
	}
	return false
}

// hermesACPSubcommand 是后端启动时总会带上的子命令。它位于 runtime 启动前缀与 agent
// custom args 之间，对 Hermes 自己的解析器来说只是普通 argv token——这正是 daemon 在
// 解析 profile 选择时不能少了它的原因。
const hermesACPSubcommand = "acp"

// hermesCLIArgsFrom 从已过滤的 custom args 组装后端在可执行文件与其启动前缀之后要传的
// argv。
func hermesCLIArgsFrom(filteredCustomArgs []string) []string {
	args := make([]string, 0, 1+len(filteredCustomArgs))
	args = append(args, hermesACPSubcommand)
	return append(args, filteredCustomArgs...)
}

// hermesCLIArgs 是 hermesBackend.Execute 交给启动边界的 argv。
func hermesCLIArgs(customArgs []string, logger *slog.Logger) []string {
	return hermesCLIArgsFrom(filterCustomArgs(customArgs, hermesBlockedArgs, logger))
}

// HermesLaunchArgv 返回 Hermes 将要解析的确切 argv：runtime 启动前缀、`acp`、然后经过与
// 后端相同的 blocked-flag 过滤后的 custom args。
//
// daemon 从这份 argv 而非手拼的近似值来解析 profile 选择。光把前缀与 custom args 拼起来
// 会静默地偏离真实命令行，因为 `acp` 这个 token 参与 Hermes 的扫描：fixed_args `--model`
// 配 custom_args `-p research` 时，近似解析会把 `-p` 读成 `--model` 的值、找不到选择；
// 而真实的 `--model acp -p research` 会跳过 `--model acp` 并选中 `research`。结果就是
// overlay 从默认 home 播种，进程却跑在另一个 profile 的配置下。
func HermesLaunchArgv(launchPrefix, customArgs []string, logger *slog.Logger) []string {
	return Command{Prefix: launchPrefix}.Argv(hermesCLIArgs(customArgs, logger)...)
}

// StripHermesProfileSelectors 把 Hermes 将要解析的 argv 里每个 profile 选择都移除，
// 并把幸存的 token 按来源区归还——启动前缀或 custom args。
//
// daemon 只有在构建了任务级 overlay 时才调用它：此时 overlay 的 HERMES_HOME 是权威的，
// 命令行上不允许任何东西把 HERMES_HOME 重新指到 overlay 之外。
//
// 它作用于拼装后的 argv 而不是各自区域单独处理，有两个原因（若忽略则都会留下一个活着的
// 选择器）：
//
//   - 一次选择可能跨区域边界。以裸 `-p` 结尾的启动前缀会把后端自己的 `acp` token 当作
//     它的值，而任何单区域都不包含一个完整的选择可供剥离。
//   - 去掉一个选择会「提拔」下一个。Hermes 只认第一个、忽略其余，所以单遍处理可能把
//     job 交给后面出现的那个——而前缀与 custom args 分开配置时，出现两个选择是常见配置
//     而非用户失误。
//
// Hermes 自己丢弃的 token——非法的 profile 值会使 ParseHermesProfileArgs 报「没找到」——
// 原样保留：它们不重定向任何东西。`acp` token 从不被移除，因为后端启动时会重新加上；
// 真正破坏选择的是丢掉捕获它的那个 flag。
func StripHermesProfileSelectors(launchPrefix, customArgs []string, logger *slog.Logger) ([]string, []string) {
	prefix := append([]string(nil), launchPrefix...)
	custom := append([]string(nil), filterCustomArgs(customArgs, hermesBlockedArgs, logger)...)
	for {
		sel := ParseHermesProfileArgs(Command{Prefix: prefix}.Argv(hermesCLIArgsFrom(custom)...))
		if !sel.Found {
			return prefix, custom
		}
		acpIndex := len(prefix)
		removed := false
		// 从后往前删，保证删除时靠前的下标仍然有效。
		for i := sel.ArgFrom + sel.ArgLen - 1; i >= sel.ArgFrom; i-- {
			switch {
			case i < acpIndex:
				prefix = append(prefix[:i], prefix[i+1:]...)
				removed = true
			case i == acpIndex:
				// 后端所有的；启动时会重新加。
			default:
				if j := i - acpIndex - 1; j < len(custom) {
					custom = append(custom[:j], custom[j+1:]...)
					removed = true
				}
			}
		}
		if !removed {
			// 防御：一次选择必含两个区域之一里的一个 flag，所以不可能死循环。
			// 直接返回而不是空转。
			return prefix, custom
		}
	}
}

// StripHermesProfileArgs 精确移除 ParseHermesProfileArgs 选中的那次 argv 出现。
// daemon 只有在构建了任务级 overlay 时才调用它，于是 Hermes 用 overlay 的 HERMES_HOME
// 而不是重新解析 profile——而无 skill 的任务参数原样不动。
func StripHermesProfileArgs(args []string, sel HermesProfileSelection) []string {
	if !sel.Found || sel.ArgFrom < 0 || sel.ArgLen <= 0 {
		return args
	}
	end := sel.ArgFrom + sel.ArgLen
	if end > len(args) {
		end = len(args)
	}
	out := make([]string, 0, len(args)-(end-sel.ArgFrom))
	out = append(out, args[:sel.ArgFrom]...)
	out = append(out, args[end:]...)
	return out
}

// hermesBackend 实现 Backend 接口：拉起 `hermes acp` 子进程，通过 stdin/stdout 上的
// ACP（Agent Communication Protocol）JSON-RPC 2.0 与它通信。与 Codex 是同一种模式，
// 只是协议换成 ACP 而非 Codex 专属的 JSON-RPC 方法。
//
// opts.ThinkingLevel 经下方 applyACPEffortOption 应用，具体取决于会话对外公布的 effort
// 选项。本供应商覆盖两个能力截然相反、互不相关的二进制——Hermes Agent 与 jcode——只有
// 目录才能诚实区分两者；谁是谁见 thinking.go 里的 acpCatalogThinkingProviders。
type hermesBackend struct {
	cfg Config
}

var (
	hermesReaderDrainGrace      = 2 * time.Second
	hermesNotificationQuietTime = 250 * time.Millisecond
)

func (b *hermesBackend) Execute(ctx context.Context, prompt string, opts ExecOptions) (*Session, error) {
	execPath := b.cfg.ExecutablePath
	if execPath == "" {
		execPath = "hermes"
	}
	if _, err := exec.LookPath(execPath); err != nil {
		return nil, fmt.Errorf("hermes executable not found at %q: %w", execPath, err)
	}

	// 把 agent 的 mcp_config（Claude 风格的「对象的对象」）翻译成 ACP `session/new`
	// 期望的数组形态。JSON 畸形就关门失败，让启动把真实错误浮出来，而不是静默丢掉所有
	// MCP 服务器。
	mcpServers, err := buildACPMcpServers(opts.McpConfig, b.cfg.Logger)
	if err != nil {
		return nil, fmt.Errorf("hermes: invalid mcp_config: %w", err)
	}

	timeout := opts.Timeout
	runCtx, cancel := runContext(ctx, timeout)

	// 与 HermesLaunchArgv 为 daemon 复刻的拼装一致，保证 overlay 从哪个 profile 播种、
	// 这份 argv 就真的选中哪个 profile。
	hermesArgs := hermesCLIArgs(opts.CustomArgs, b.cfg.Logger)
	cmd := b.cfg.commandAt(execPath).exec(runCtx, hermesArgs...)
	hideAgentWindow(cmd)
	b.cfg.logAgentCommand(cmd, newAgentCommandLogArgs(hermesArgs, trustAgentCommandPositional(0, hermesACPSubcommand)))
	agentsMDPresent := false
	if opts.Cwd != "" {
		cmd.Dir = opts.Cwd
		if _, err := os.Stat(filepath.Join(opts.Cwd, "AGENTS.md")); err == nil {
			agentsMDPresent = true
		}
	}
	b.cfg.Logger.Info("hermes acp starting", "cwd", opts.Cwd, "agents_md_present", agentsMDPresent)
	if opts.SystemPrompt != "" {
		b.cfg.Logger.Debug("hermes ignoring ExecOptions.SystemPrompt; using cwd-scoped context files", "cwd", opts.Cwd)
	}

	env := buildEnv(b.cfg.Env)
	// 开 yolo 模式，让 Hermes 自动批准所有工具执行。
	env = append(env, "HERMES_YOLO_MODE=1")
	cmd.Env = env

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("hermes stdout pipe: %w", err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("hermes stdin pipe: %w", err)
	}
	// 把 stderr 转发进 daemon 日志，同时从里面嗅探供应商级错误，以便把它们暴露到任务
	// 结果里。Hermes 的 session/prompt 在底层对 LLM 的 HTTP 调用返回 4xx/5xx 时仍会报
	// stopReason=end_turn，没有这一步我们就会报出误导性的「空输出」，把真实原因（对当前
	// 供应商选错模型、凭据错误、限流……）藏进 daemon 日志。
	//
	// 这里用 StderrPipe + 显式拷贝 goroutine，而不是 `cmd.Stderr = io.MultiWriter(...)`：
	// 需要一个 join 点（stderrDone）位于「把失败升级为结果」这个决策之前。用 MultiWriter
	// 形式时，exec 的内部拷贝 goroutine 只能由 `cmd.Wait()` join，而后者跑在 deferred
	// 清理里——那时 promoteACPResultOnProviderError 早已查过嗅探器。这个竞态在 CI 负载下
	// 会丢掉 429 / 用量超限的消息，表现为偶发测试失败
	// （TestHermesBackendPromotesProviderErrorWithNonEmptyOutput）。
	providerErr := newACPProviderErrorSniffer("hermes")
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("hermes stderr pipe: %w", err)
	}

	if err := startOwnedProcessTree(cmd, b.cfg.Logger); err != nil {
		cancel()
		return nil, fmt.Errorf("start hermes: %w", err)
	}

	stderrSink := io.MultiWriter(newLogWriter(b.cfg.Logger, "[hermes:stderr] "), providerErr)
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		_, _ = io.Copy(stderrSink, stderr)
	}()

	b.cfg.Logger.Info("hermes acp started", "pid", cmd.Process.Pid, "cwd", opts.Cwd)

	msgCh := make(chan Message, 256)
	resCh := make(chan Result, 1)

	// Hermes 把过程叙述与最终答案流成同一种 agent_message_chunk 类型；tracker 只为
	// Result.Output 保留工具调用后的那段文本，同时为错误检测保留完整文本。
	var deliverable acpDeliverableTracker
	// streamingCurrentTurn 拦住所有会话更新，使历史重放（Hermes 在 session/resume 时会
	// 重发上一整段回合 transcript，也可能在我们的 session/prompt 响应流入前冲刷积压的
	// chunk）被丢弃而不是把上一轮答案重复进输出。只有在 session/prompt 发出后才置 true。
	var streamingCurrentTurn atomic.Bool
	// turnActivity 统计当前回合被接受的会话更新数。零表示 agent 什么都没产出——没有文本、
	// 没有思考、没有工具调用——这正是「死会话拒绝」与「模型确实拒绝了请求」的分界。见
	// hermesResumeSessionLost。
	var turnActivity atomic.Int64

	promptDone := make(chan hermesPromptResult, 1)
	activity := make(chan struct{}, 1)

	c := &hermesClient{
		cfg:                        b.cfg,
		stdin:                      stdin,
		pending:                    make(map[int]*pendingRPC),
		pendingTools:               make(map[string]*pendingToolCall),
		toolStartCarriesFinalInput: b.cfg.BuiltinRuntime,
		acceptNotification: func(string) bool {
			return streamingCurrentTurn.Load()
		},
		onActivity: func() {
			select {
			case activity <- struct{}{}:
			default:
			}
		},
		onMessage: func(msg Message) {
			if !streamingCurrentTurn.Load() {
				return
			}
			turnActivity.Add(1)
			deliverable.observe(msg)
			trySend(msgCh, msg)
		},
		onPromptDone: func(result hermesPromptResult) {
			if !streamingCurrentTurn.Load() {
				return
			}
			select {
			case promptDone <- result:
			default:
			}
		},
	}

	// 后台开始读 stdout。
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
		c.closeAllPending(fmt.Errorf("hermes process exited"))
	}()

	// 用 goroutine 驱动 ACP 会话生命周期。
	go func() {
		defer close(msgCh)
		defer close(resCh)
		defer func() {
			stdin.Close()
			// 取消必须在 Wait 之前可达。病态子进程可能关闭 stdout/stderr（于是管道排空
			// 成功）却还活着；先等会一直等到任务超时，让后面的 deferred 取消失效。
			cancel()
			_ = cmd.Wait()
			releaseProcessGroup(cmd)
		}()

		startTime := time.Now()
		finalStatus := "completed"
		var finalError string
		var sessionID string
		// ACP runtime 拒绝我们要求 resume 的会话时置位。只有这种情况能靠新开会话治好，
		// 所以下面握手/网络失败必须让它保持 false。
		var resumeRejected bool
		// 仅当 session/resume 真的落在我们要求的会话上时为 true。Hermes 对自己 state.db
		// 已不持有的会话做 resume 时，会静默新建一个（acp_adapter/server.py 的
		// resume_session："not found, creating new"），所以正是这个决定回合要不要带连续性
		// 提示——见下方 prompt 拼装。
		var resumeLanded bool
		// session/prompt 在真的应答时上报的 stop reason。等回合完全落定后再读一次——
		// 见下面供应商错误升级之后的 resumed-session 检查。
		var promptStopReason string
		effectiveModel := strings.TrimSpace(opts.Model)
		// runtime 在 session/new 或 session/resume 之后立刻上报的当前模型 id。用于跳过
		// 一次多余的 session/set_model——否则我们会重选会话当前已经在用的模型
		// （见下面的 set_model 门）。
		var sessionCurrentModel string

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
			finalError = fmt.Sprintf("hermes initialize failed: %v", err)
			resCh <- Result{Status: finalStatus, Error: finalError, DurationMs: time.Since(startTime).Milliseconds()}
			return
		}

		// 丢弃 runtime 没有公布的、走远程传输的 MCP 条目。ACP 要求客户端尊重
		// agentCapabilities.mcpCapabilities；把 http/sse 条目发给声明只支持 stdio 的
		// runtime，session/new 请求大概率整批被拒。
		mcpServers = filterACPMcpServersByCapability(mcpServers, extractACPMcpCapabilities(initResult), "hermes", b.cfg)

		// 2. 创建或 resume 会话。
		cwd := opts.Cwd
		if cwd == "" {
			cwd = "."
		}

		// sessionResult 是 session/new 或 session/resume 二者之一产出的会话。它携带
		// 下方 effort 步骤要读的 configOptions，所以两个分支都要把它留住。
		var sessionResult json.RawMessage

		if opts.ResumeSessionID != "" {
			// 按 ACP Session Setup，session/resume 接受 mcpServers，runtime 会作为 resume
			// 的一部分重新连接它们。没有这个，resumed 的 Hermes 任务会失去同一 agent 上新
			// 任务本来会有的 MCP 工具访问权。
			result, err := c.request(runCtx, "session/resume", map[string]any{
				"cwd":        cwd,
				"sessionId":  opts.ResumeSessionID,
				"mcpServers": mcpServers,
			})
			if err != nil {
				finalStatus = "failed"
				finalError = fmt.Sprintf("hermes session/resume failed: %v", err)
				resCh <- Result{Status: finalStatus, Error: finalError, DurationMs: time.Since(startTime).Milliseconds()}
				return
			}
			sessionResult = result
			sessionID, resumeLanded = resolveHermesResumedSessionID(opts.ResumeSessionID, result)
			if !resumeLanded {
				b.cfg.Logger.Warn("agent returned a different session id on resume — original was likely lost; continuing with the new id",
					"backend", "hermes",
					"requested", opts.ResumeSessionID,
					"actual", sessionID,
				)
			}
			sessionCurrentModel = extractACPCurrentModelID(result)
			if effectiveModel == "" {
				effectiveModel = sessionCurrentModel
			}
		} else {
			result, err := c.request(runCtx, "session/new", buildHermesSessionParams(cwd, opts.Model, mcpServers))
			if err != nil {
				finalStatus = "failed"
				finalError = fmt.Sprintf("hermes session/new failed: %v", err)
				resCh <- Result{Status: finalStatus, Error: finalError, DurationMs: time.Since(startTime).Milliseconds()}
				return
			}
			sessionResult = result
			sessionID = extractACPSessionID(result)
			if sessionID == "" {
				finalStatus = "failed"
				finalError = "hermes session/new returned no session ID"
				resCh <- Result{Status: finalStatus, Error: finalError, DurationMs: time.Since(startTime).Milliseconds()}
				return
			}
			sessionCurrentModel = extractACPCurrentModelID(result)
			if effectiveModel == "" {
				effectiveModel = sessionCurrentModel
			}
		}

		c.sessionID = sessionID
		b.cfg.Logger.Info("hermes session created", "session_id", sessionID)
		// 中途 pin：daemon 的 PinTaskSession 按 MessageStatus+SessionID 定位。
		trySend(msgCh, Message{Type: MessageStatus, Status: "running", SessionID: sessionID})

		// 3. 调用方若选了模型（agent.model，来自 UI 下拉框），在发任何 prompt 之前先请
		// hermes 把会话切过去。Hermes 的 _build_model_state 把 modelId 暴露成
		// `provider:model`——我们原样透传。这一步出错必须让任务失败：若静默回退到 hermes
		// 默认模型，用户会以为自己的选择被采纳，任务实际却跑在别的模型上。
		//
		// 当会话已上报就是这个模型时跳过该调用。Hermes 的 set_model 会对 model id 重新跑
		// 一遍供应商自动探测；对 `provider:model` 形态、且解析出的 provider 与会话当前
		// provider 相同的情况，可能误路由到别的供应商（如 custom:deepseek-v4-pro →
		// OpenRouter）并以 auth 错误失败。重选一个会话已在用的模型是纯亏。sessionCurrentModel
		// 为空（老 runtime 或状态不可解析）则照旧发 set_model，保持旧行为。见 MUL-5029 /
		// NousResearch/hermes-agent#59089。
		if opts.Model != "" && effectiveModel == sessionCurrentModel {
			b.cfg.Logger.Info("hermes session already on requested model; skipping redundant set_model",
				"model", opts.Model,
				"session_id", sessionID,
			)
		} else if opts.Model != "" {
			if _, err := c.request(runCtx, "session/set_model", map[string]any{
				"sessionId": sessionID,
				"modelId":   opts.Model,
			}); err != nil {
				b.cfg.Logger.Warn("hermes set_session_model failed", "error", err, "requested_model", opts.Model)
				finalStatus = "failed"
				finalError = fmt.Sprintf("hermes could not switch to model %q: %v", opts.Model, err)
				if opts.ResumeSessionID != "" && isACPSessionNotFound(err) {
					// 带模型覆盖的 resumed 会话，死会话在这里暴露，而不是等 session/prompt。
					// 与下方 prompt 路径同一修法：清掉 id，让 daemon 的 resume 失败兜底以新会话重试。
					b.cfg.Logger.Warn("resumed session not found at set_model time; clearing session id so the daemon retries fresh",
						"backend", "hermes",
						"session_id", sessionID,
					)
					sessionID = ""
					resumeRejected = true
				}
				resCh <- Result{
					Status:         finalStatus,
					Error:          finalError,
					DurationMs:     time.Since(startTime).Milliseconds(),
					SessionID:      sessionID,
					ResumeRejected: resumeRejected,
				}
				return
			}
			b.cfg.Logger.Info("hermes session model set", "model", opts.Model)
		}

		// 3b. 通过本会话公布的某个 effort 选项，应用持久化的 thinking 覆盖。由哪个
		// 二进制应答决定是否真的发生：jcode 公布 `reasoning_effort` 并把它织进供应商
		// 请求；Hermes Agent 根本不公布 configOptions，于是该 helper 空转。与上面
		// set_model 不同，这里绝不能弄挂任务——应用不了的 effort 只是让 prompt 跑在
		// runtime 自己的默认上。
		//
		// set_model 跑过之后，sessionResult 就不再描述当前会话了，因为 ACP 的 effort
		// 选项可能依赖当前模型。
		applyACPEffortOption(runCtx, c.request, "hermes", b.cfg.Logger,
			sessionID, sessionResult, opts.ThinkingLevel, opts.Model == "")

		// 4. 发送 prompt 并等待 PromptResponse。
		//
		// 这里不要前置 opts.SystemPrompt。Hermes ACP 自己会从 cwd 加载项目/上下文文件
		// （AGENTS.md、.agent_context 等）；把完整 runtime 简报复制进用户 prompt 会让请求
		// 大很多，还曾在普通任务上触发过上游安全过滤器。
		// 在请求前翻转 gate：让 initialize / 会话建立期间冲刷出的任何历史重放保持被丢弃，
		// 但本回合的每条通知都被处理。
		streamingCurrentTurn.Store(true)
		_, err = c.request(runCtx, "session/prompt", map[string]any{
			"sessionId": sessionID,
			"prompt": []map[string]any{
				{"type": "text", "text": hermesTurnText(prompt, opts.ResumeExpected, resumeLanded, opts.ResumeContinuityNotice)},
			},
		})
		if err != nil {
			// 请求本身失败（不只是 context 取消）时，检查 context 是否被取消/超时。
			if runCtx.Err() == context.DeadlineExceeded {
				finalStatus = "timeout"
				finalError = fmt.Sprintf("hermes timed out after %s", timeout)
			} else if runCtx.Err() == context.Canceled {
				finalStatus = "aborted"
				finalError = "execution cancelled"
			} else {
				finalStatus = "failed"
				finalError = fmt.Sprintf("hermes session/prompt failed: %v", err)
				if opts.ResumeSessionID != "" && isACPSessionNotFound(err) {
					// agent 已不认识我们 resume 的那个会话。即使会话已没，Hermes 也会把请求的
					// id 从 session/resume 原样回显，所以 resolveResumedSessionID 拦不住——它只
					// 在 prompt 时刻在这里暴露。返回空 SessionID，让 daemon 的 resume 失败兜底
					// 用新会话重试并存储替代 id；保留过期 id 会让以后每次在该（agent, issue）上的
					// 调度都以同样方式失败。
					b.cfg.Logger.Warn("resumed session not found at prompt time; clearing session id so the daemon retries fresh",
						"backend", "hermes",
						"session_id", sessionID,
					)
					sessionID = ""
					resumeRejected = true
				}
			}
		} else {
			// prompt 已完成。检查响应解析是否得到 promptDone 结果。
			select {
			case pr := <-promptDone:
				promptStopReason = pr.stopReason
				if pr.stopReason == "cancelled" {
					finalStatus = "aborted"
					finalError = "hermes cancelled the prompt"
				}
				c.mergeUsage(pr.usage)
			default:
			}
			waitForHermesNotificationQuiescence(runCtx, activity, readerDone)
		}

		duration := time.Since(startTime)
		b.cfg.Logger.Info("hermes finished", "pid", cmd.Process.Pid, "status", finalStatus, "duration", duration.Round(time.Millisecond).String())

		// 先关 stdin，让 Hermes 观测到 EOF 并干净退出。stdout/stderr 排空期间保持进程
		// 存活；在 prompt 响应边界立即取消会截断刚好在响应后到达的收尾通知。
		stdin.Close()

		// 等 stdout 读取方与 stderr 拷贝方，保证所有输出已累积、供应商错误嗅探器拿到
		// 子进程写下的每一个字节后，我们再咨询它做失败升级。跳过这一步会留一个小竞态：
		// stopReason=end_turn 经 stdout 到达时，stderr 的 429 / 用量超限行还在路上，
		// 导致升级后的错误消息落到合成的 agent 文本兜底里。若 Hermes 在有界时间内没有
		// 尊重 stdin EOF，则取消它并在访问其缓冲区之前 join 两个读取方。
		if !waitForHermesPipeDrain(readerDone, stderrDone, hermesReaderDrainGrace) {
			b.cfg.Logger.Warn("hermes did not close output pipes after stdin EOF; forcing shutdown",
				"pid", cmd.Process.Pid,
				"grace", hermesReaderDrainGrace.String(),
			)
			cancel()
			<-readerDone
			<-stderrDone
		}
		streamingCurrentTurn.Store(false)

		finalOutput, providerErrorOutput := deliverable.result()

		// Hermes 在上游 LLM 调用最终失败时（HTTP 429 限流、token 过期……）也会报
		// stopReason=end_turn。promoteACPResultOnProviderError 在以下任一情况把状态翻成
		// "failed"：stderr 嗅探器看到 *终态* 失败标记（不只是单次尝试的瞬时告警）；agent
		// 文本流里含适配器放弃时注入的合成「API call failed after N retries...」回合；
		// 或者没有可回退的输出。它读完整文本流而不是交付物，所以落在工具调用之前的放弃
		// 回合仍然可见。
		finalStatus, finalError = promoteACPResultOnProviderError(finalStatus, finalError, providerErrorOutput, providerErr)

		// Hermes 无法重建的 resumed 会话。
		//
		// 在这里而不是上面静默边界处判定：安静窗口关闭并不代表回合结束，stdin EOF 与管道
		// 排空才算；Hermes 完全可能在那个间隙里投递回合的最后一个 chunk——
		// TestHermesBackendDrainsLateFinalNotificationAfterPromptResponse 就是为此存在。
		// 提前判定会在真实答案还在路上时冻结 turnActivity == 0，把一个只是应答慢的健康
		// 会话丢掉（还重跑该回合）。此刻 streamingCurrentTurn 已关、每个被接受的更新都已
		// 计数，所以读数是终局。
		//
		// 刻意跑在上面升级之后：当嗅探器捕获了重建失败的原因（如会话持久化的供应商身份
		// 已解析不了），那段消息对用户远比这里通用的那句有用，所以只在没有别的原因时才补
		// 一个。没有它，这样的回合会以「completed」+ 空输出上报——一个静默什么都没干的
		// 任务。
		if hermesResumeSessionLost(opts.ResumeSessionID, promptStopReason, turnActivity.Load()) {
			b.cfg.Logger.Warn("resumed session refused with no agent activity; treating it as gone and clearing the session id so the daemon retries fresh",
				"backend", "hermes",
				"session_id", sessionID,
			)
			if finalStatus == "completed" {
				finalStatus = "failed"
				finalError = hermesResumeLostError
			}
			sessionID = ""
			resumeRejected = true
		}

		// Build usage map.
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

	return &Session{Messages: msgCh, Result: resCh}, nil
}

// waitForHermesNotificationQuiescence 给 stdout 读取方一个有界机会去消费 session/prompt
// 返回后刚发出的会话更新。Hermes 可能在该响应之后投递最后的 agent_message_chunk；立即
// 在此边界关 stdin 或取消会丢掉用户可见的答案。
func waitForHermesNotificationQuiescence(ctx context.Context, activity <-chan struct{}, readerDone <-chan struct{}) {
	waitForACPNotificationQuiescence(ctx, activity, readerDone, hermesNotificationQuietTime, hermesReaderDrainGrace)
}

// acpNotificationQuietTime 是共享排空逻辑在断定 ACP agent 已停止发通知前要等的默认静默
// 时长。它是协议级启发而非某后端的特性，所以没有理由区分的后端共用它；硬上限仍按后端
// 各自定义。
const acpNotificationQuietTime = 250 * time.Millisecond

// waitForACPNotificationQuiescence 给共享 ACP stdout 读取方一个有界机会去消费后端在其
// session/prompt 响应返回后可能刚发出的通知。否则在响应边界就关 stdin、取消 context，会
// 与读取方竞态，静默截掉最后的文本或用量更新。
//
// 以下任一情况发生即返回：quiet 内无新通知、读取方结束、硬上限耗尽、或 ctx 被取消——
// 所以永远握着 stdout 不松的 agent 无法拖住该回合。
func waitForACPNotificationQuiescence(ctx context.Context, activity <-chan struct{}, readerDone <-chan struct{}, quiet, hard time.Duration) {
	quietTimer := time.NewTimer(quiet)
	defer quietTimer.Stop()
	hardTimer := time.NewTimer(hard)
	defer hardTimer.Stop()

	for {
		select {
		case <-activity:
			if !quietTimer.Stop() {
				select {
				case <-quietTimer.C:
				default:
				}
			}
			quietTimer.Reset(quiet)
		case <-quietTimer.C:
			return
		case <-readerDone:
			return
		case <-hardTimer.C:
			return
		case <-ctx.Done():
			return
		}
	}
}

func waitForHermesPipeDrain(readerDone, stderrDone <-chan struct{}, timeout time.Duration) bool {
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for readerDone != nil || stderrDone != nil {
		select {
		case <-readerDone:
			readerDone = nil
		case <-stderrDone:
			stderrDone = nil
		case <-timer.C:
			return false
		}
	}
	return true
}

// ── hermesClient：ACP JSON-RPC 2.0 传输层 ──

type hermesPromptResult struct {
	stopReason string
	usage      acpUsageSnapshot
	// modelID 是 agent 实际把本回合用量记到的那具模型，来自 `result._meta.modelId`。
	// 不上报它的 agent 为空。会话握手没有给出模型 id 时，后端用它归因用量（见 grok.go）。
	modelID string
}

type hermesClient struct {
	cfg          Config
	stdin        interface{ Write([]byte) (int, error) }
	writeMu      sync.Mutex // 串行化跨 goroutine 的 stdin.Write 调用
	mu           sync.Mutex
	nextID       int
	pending      map[int]*pendingRPC
	sessionID    string
	onMessage    func(Message)
	onPromptDone func(hermesPromptResult)
	// selectPermission 让某 ACP 方言收窄通用的 headless 权限策略。Reasonix 用它拒绝
	// 同时携带 allow_once 选项的用户提问与 fresh-human 审批。nil 则对既有后端保留共享
	// ACP 策略。
	selectPermission func(json.RawMessage) (optionID string, grant bool, ok bool)
	// onNotification 观察非 session/update 的厂商通知。既有后端留 nil；回调必须自己做
	// method 与生命周期过滤。
	onNotification func(method string, params json.RawMessage)
	// onActivity 观察被接受的 ACP 会话更新。Hermes 与 Grok 用它保留一个短促的响应后
	// 排空窗口；其它 ACP 后端留 nil，保持既有生命周期行为。
	onActivity func()
	// acceptNotification 可在分发到会改动客户端状态（如用量或 pending 工具调用）的
	// handler 之前丢弃 ACP 会话更新。
	acceptNotification func(updateType string) bool
	// toolStartCarriesFinalInput 标记一种方言：其 tool_call 起始帧是该调用的 input 唯一
	// 出现的地方，所以 MessageToolUse 可以在调用一开始就发出，而不用一直憋到它完成。
	// 对这种方言，等只会等不出更多 input，却让运行失去在途可见性。
	//
	// 这是经厂商验证的兼容例外，所以 hermes 后端与 acpToleratesOmittedMcpCapabilities
	// 一样把它限定在 Config.BuiltinRuntime：`protocol_family: hermes` + `command_name:
	// jcode` 会以 "hermes" 身份到达本后端，却是不相关的实现；只有真正的 Hermes Agent
	// 二进制被证实如此——acp_adapter/tools.py 的 build_tool_call 传
	// `raw_input=None if tool_name in _POLISHED_TOOLS else arguments`，而
	// build_tool_complete 什么都不传。置 false 意味着延后发出，于是自定义 hermes 家族
	// runtime 若在更晚的 update 上提供 rawInput，仍能被记录。
	//
	// 其它后端也留 false——Kimi 的 args 是跨 update 流式到达的，对它来说起始帧确实不完整。
	toolStartCarriesFinalInput bool

	// pendingTools 为 input 跨多条 ACP tool_call_update 消息流入的工具调用缓冲 args
	// （kimi 如此——LLM 的 token 一个接一个到达，每条 update 携带到当前为止的累积 args
	// JSON）。我们推迟发出 MessageToolUse，直到看到 status=completed/failed 或攒到完整
	// 的 args 集合，UI 才不会看到 `{"comma` 这种写了一半的命令。
	toolMu       sync.Mutex
	pendingTools map[string]*pendingToolCall

	usageMu sync.Mutex
	usage   acpUsageAccumulator

	// terminalEnabled 只对客户端侧终端调用已在本文件实现的 ACP runtime 置位。保持
	// opt-in，避免向不需要它的 hermes 家族 runtime 公布一个我们并没有的能力。
	terminalEnabled bool
	terminalCtx     context.Context
	terminalCwd     string
	terminalEnv     []string
	terminalMu      sync.Mutex
	terminals       map[string]*acpTerminal
	nextTerminalID  int
}

// pendingToolCall 缓冲一个工具调用在其 args 流式到达期间的状态。每条 ACP toolCallId
// 一个条目。
type pendingToolCall struct {
	toolName string         // 已经 hermesToolNameFromTitle 映射
	input    map[string]any // agent 前置发来时的 rawInput（hermes）
	argsText string         // 累积的 `content[].text` args（kimi，累积式）
	emitted  bool           // 是否已发出 MessageToolUse
}

// writeLine 串行化并发的 JSON-RPC 写入，让 request()（主 goroutine）与
// handleAgentRequest()（读取 goroutine）不交错帧。管道对小写入本身是原子的，但并发下
// 我们还要确定性顺序。
func (c *hermesClient) writeLine(data []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err := c.stdin.Write(data)
	return err
}

func (c *hermesClient) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	id := c.nextID
	c.nextID++
	pr := &pendingRPC{ch: make(chan rpcResult, 1), method: method}
	c.pending[id] = pr
	c.mu.Unlock()

	msg := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}
	data, err := json.Marshal(msg)
	if err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, err
	}
	data = append(data, '\n')
	if err := c.writeLine(data); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, fmt.Errorf("write %s: %w", method, err)
	}

	select {
	case res := <-pr.ch:
		return res.result, res.err
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, ctx.Err()
	}
}

func (c *hermesClient) closeAllPending(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, pr := range c.pending {
		pr.ch <- rpcResult{err: err}
		delete(c.pending, id)
	}
}

func (c *hermesClient) handleLine(line string) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		return
	}

	// agent → client 的请求：带 id + method（还没有 result / error）。
	// Kimi 与 Hermes 都用 session/request_permission；不应答的话 agent 会阻塞到它自己的
	// 内部超时，任务挂死。HERMES_YOLO_MODE=1 只压制 Hermes 的危险 shell 命令提示
	// （tools/approval.py）；它的 ACP 编辑审批守卫（acp_adapter/edit_approval.py）仍会在
	// 每次写文件前询问，所以 Hermes 也必须处理这些请求。
	if _, hasID := raw["id"]; hasID {
		if _, hasResult := raw["result"]; hasResult {
			c.handleResponse(raw)
			return
		}
		if _, hasError := raw["error"]; hasError {
			c.handleResponse(raw)
			return
		}
		if _, hasMethod := raw["method"]; hasMethod {
			c.handleAgentRequest(raw)
			return
		}
	}

	// 通知（无 id、有 method）——来自 Hermes 的会话更新。
	if _, hasMethod := raw["method"]; hasMethod {
		c.handleNotification(raw)
	}
}

// handleAgentRequest 应答 agent 发给我们的 JSON-RPC 请求（agent → client 方向）。
// Kimi 的 ACP 终端能力与权限请求处理都在这里实现：daemon 无头运行、无法真正向用户提问，
// 所以我们自己应答权限请求——提供安全选项就授权，否则拒绝这一次动作或关门失败（见下）。
//
// 应答必须选中 agent 实际给出的某个 optionId 之一——ACP 权限契约是「从这些选项里挑」，
// 给一个 agent 从未提供的 id 会被当作拒绝。Hermes 的编辑审批路径只提供
// ["allow_once","deny"]，除恰好 "allow_once" 外一律拒绝（acp_adapter/edit_approval.py），
// 所以旧的硬编码 "approve_for_session" 曾静默拦掉 Hermes ACP runtime 上的每一次文件写
// （GitHub multica#5300）。selectACPPermissionOption 挑选 agent 给过的选项——有安全授予
// 就授，否则用 agent 提供的单次拒绝来只拒掉这次动作——并在请求没有任何可安全选中的选项
// 时以协议错误关门失败，绝不永久授权或整回合 "cancelled"。
func (c *hermesClient) handleAgentRequest(raw map[string]json.RawMessage) {
	var method string
	_ = json.Unmarshal(raw["method"], &method)

	rawID, ok := raw["id"]
	if !ok {
		return
	}
	if method == "terminal/wait_for_exit" && c.terminalEnabled {
		// wait_for_exit 有意长驻。ACP 读取方必须保持可用，去处理 Kimi 在该请求挂起期间
		// 发来的输出轮询与 terminal/kill 请求。
		id := append(json.RawMessage(nil), rawID...)
		params := append(json.RawMessage(nil), raw["params"]...)
		go func() {
			result, err := c.acpTerminalResponse(method, params)
			if err != nil {
				c.writeAgentRequestResponse(method, map[string]any{
					"jsonrpc": "2.0",
					"id":      id,
					"error": map[string]any{
						"code":    -32602,
						"message": err.Error(),
					},
				})
				return
			}
			c.writeAgentRequestResponse(method, map[string]any{
				"jsonrpc": "2.0",
				"id":      id,
				"result":  result,
			})
		}()
		return
	}

	var resp map[string]any
	switch method {
	case "terminal/create":
		if !c.terminalEnabled {
			resp = map[string]any{
				"jsonrpc": "2.0",
				"id":      json.RawMessage(rawID),
				"error": map[string]any{
					"code":    -32601,
					"message": "terminal capability is not enabled",
				},
			}
			break
		}
		result, err := c.acpTerminalCreate(raw["params"])
		if err != nil {
			resp = map[string]any{
				"jsonrpc": "2.0",
				"id":      json.RawMessage(rawID),
				"error": map[string]any{
					"code":    -32602,
					"message": err.Error(),
				},
			}
			break
		}
		resp = map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(rawID), "result": result}
	case "terminal/output", "terminal/wait_for_exit", "terminal/kill", "terminal/release":
		if !c.terminalEnabled {
			resp = map[string]any{
				"jsonrpc": "2.0",
				"id":      json.RawMessage(rawID),
				"error": map[string]any{
					"code":    -32601,
					"message": "terminal capability is not enabled",
				},
			}
			break
		}
		result, err := c.acpTerminalResponse(method, raw["params"])
		if err != nil {
			resp = map[string]any{
				"jsonrpc": "2.0",
				"id":      json.RawMessage(rawID),
				"error": map[string]any{
					"code":    -32602,
					"message": err.Error(),
				},
			}
			break
		}
		resp = map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(rawID), "result": result}
	case "session/request_permission":
		selector := c.selectPermission
		if selector == nil {
			selector = selectACPPermissionOption
		}
		optionID, grant, ok := selector(raw["params"])
		if ok {
			// 选中一个提供的选项——要么是安全授予（approve），要么没有安全授予时选提供的
			// reject_once（拒绝本次动作）。两者都是 ACP 的 "selected" 结果；这里刻意不回
			// "cancelled"——那意味着整个 prompt 回合被取消，共享此客户端的其它 ACP 后端
			// （kimi、kiro……）会中止整个任务，而不只是这次动作。
			resp = map[string]any{
				"jsonrpc": "2.0",
				"id":      json.RawMessage(rawID),
				"result": map[string]any{
					"outcome": map[string]any{
						"outcome":  "selected",
						"optionId": optionID,
					},
				},
			}
			if grant {
				c.cfg.Logger.Debug("auto-approved agent permission request", "method", method, "optionId", optionID)
			} else {
				c.cfg.Logger.Warn("no safe grant offered; selecting offered reject option", "method", method, "optionId", optionID)
			}
		} else {
			// 该请求没有提供我们能安全选中的东西：既无安全授予，也无单次 reject_once
			// （空、畸形、只有永久、或只有 reject_always）。返回协议错误，而不是伪造一个
			// agent 没给过的 id 或整回合 "cancelled"。
			resp = map[string]any{
				"jsonrpc": "2.0",
				"id":      json.RawMessage(rawID),
				"error": map[string]any{
					"code":    -32603,
					"message": "no auto-selectable permission option offered",
				},
			}
			c.cfg.Logger.Warn("no safely selectable permission option offered; returning error", "method", method)
		}
	default:
		// 未知的 agent→client 方法——回标准「method not found」，避免 agent 一直等我们。
		// 好过沉默：agent 至少能自行决定下一步。
		resp = map[string]any{
			"jsonrpc": "2.0",
			"id":      json.RawMessage(rawID),
			"error": map[string]any{
				"code":    -32601,
				"message": "method not found: " + method,
			},
		}
		c.cfg.Logger.Debug("unhandled agent→client request", "method", method)
	}

	c.writeAgentRequestResponse(method, resp)
}

func (c *hermesClient) writeAgentRequestResponse(method string, resp map[string]any) {
	data, err := json.Marshal(resp)
	if err != nil {
		logger := c.cfg.Logger
		if logger == nil {
			logger = slog.Default()
		}
		logger.Warn("marshal agent-request response", "method", method, "error", err)
		return
	}
	data = append(data, '\n')
	if err := c.writeLine(data); err != nil {
		logger := c.cfg.Logger
		if logger == nil {
			logger = slog.Default()
		}
		logger.Warn("write agent-request response", "method", method, "error", err)
	}
}

// acpPermissionOption 是 session/request_permission 请求 `options` 数组里的一条。
// `kind` 是 ACP 层分类（allow_once / allow_always / reject_once / reject_always）；
// `optionId` 是 agent 自定义的不透明字符串，我们把它原样回显以选中该选项。
type acpPermissionOption struct {
	OptionID string `json:"optionId"`
	Kind     string `json:"kind"`
}

// ACP v1 的 PermissionOptionKind 取值。只有两种 allow 类授权；其它或未知 kind 一律按
// 不授权处理，让未来或异常的 kind 永远不可能被自动批准。reject_once 只拒单次动作
// （唯一我们会自动选中的 reject——reject_always 会把拒绝像 allow_always 把授予一样
// 永久化）。
// https://agentclientprotocol.com/protocol/v1/schema#permissionoptionkind
const (
	acpKindAllowOnce   = "allow_once"
	acpKindAllowAlways = "allow_always"
	acpKindRejectOnce  = "reject_once"
)

// acpSessionScopedOptionIDs 是已知「仅对当前会话授予、不持久化决策」的 optionId。
// Hermes 的 "allow_session" 与其永久的 "allow_always" 选项都带 ACP kind "allow_always"
// （ACP 没有会话级 kind），所以单凭 kind 分不开——我们用 id 识别会话级的那些。
// "approve_for_session" 是其它 ACP 后端使用的等价 id。
var acpSessionScopedOptionIDs = []string{"allow_session", "approve_for_session"}

// selectACPPermissionOption 决定如何自动应答 session/request_permission。返回要选中的
// 已提供 optionId、该选择是授权（true）还是拒绝（false）本次动作，以及请求没有可安全
// 选中的选项时 ok=false（调用方此时返回协议错误，而不是编造一个结果）。
//
// 它只会返回 agent 实际提供过的 id。据 GitHub multica#5300 的 review，它拒绝自动选中
// 永久的 "allow_always" 授予——在 Hermes 上这会持久化到 runtime 所有者的磁盘白名单，
// 活得比任务还久（ACP v1 allow_always 会「记住这个选择」）。授权与否只看显式的 ACP
// kind，从不看不透明的 optionId，未知 kind 一律关门失败。偏好顺序：
//
//  1. 已知的会话级授权 id；
//  2. 任一单次（kind=allow_once）授予；
//  3. 提供的单次 reject_once——只拒绝本次动作，而不是回 "cancelled"（其它 ACP 后端
//     会把它读成取消整个 prompt 回合）。
func selectACPPermissionOption(params json.RawMessage) (optionID string, grant bool, ok bool) {
	var p struct {
		Options []acpPermissionOption `json:"options"`
	}
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return "", false, false
		}
	}

	// 1. 已知的会话级授权 id，且确实以授权 kind 提供时。
	for _, want := range acpSessionScopedOptionIDs {
		for _, opt := range p.Options {
			if opt.OptionID == want && isACPGrantKind(opt.Kind) {
				return opt.OptionID, true, true
			}
		}
	}
	// 2. 任一单次授权。kind=allow_once 天然只作用于这一次动作，所以无论（不透明的）
	// optionId 是什么都安全——也覆盖使用非标准 option id 的 agent。
	for _, opt := range p.Options {
		if opt.OptionID != "" && strings.EqualFold(strings.TrimSpace(opt.Kind), acpKindAllowOnce) {
			return opt.OptionID, true, true
		}
	}
	// 3. 没有安全授权：选中一个提供的 reject_once 来拒绝本次动作。
	for _, opt := range p.Options {
		if opt.OptionID != "" && strings.EqualFold(strings.TrimSpace(opt.Kind), acpKindRejectOnce) {
			return opt.OptionID, false, true
		}
	}
	// 4. 没有可安全选中的选项（空、畸形、只有永久、或只有 reject_always）。让调用方
	// 返回协议错误。
	return "", false, false
}

// isACPGrantKind 判断某个 ACP PermissionOptionKind 是否授权该动作。只有当前两种 allow
// 类够格；其它或未知值一律不授权——授权检测对未知值关门失败。
func isACPGrantKind(kind string) bool {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case acpKindAllowOnce, acpKindAllowAlways:
		return true
	default:
		return false
	}
}

// acpRPCError 是 agent 进程返回的 JSON-RPC 错误帧。它渲染出来的样子与 handleResponse
// 过去用 fmt.Errorf 拼的扁平字符串完全一致，日志与被上浮的任务错误保持不变，但同时把
// code/message 结构化保留，让调用方可按错误类别分支（见 isACPSessionNotFound），而不是
// 解析文本。
type acpRPCError struct {
	Method  string
	Code    int
	Message string
	Data    string
}

func (e *acpRPCError) Error() string {
	if e.Data != "" {
		return fmt.Sprintf("%s: %s (code=%d, data=%s)", e.Method, e.Message, e.Code, e.Data)
	}
	return fmt.Sprintf("%s: %s (code=%d)", e.Method, e.Message, e.Code)
}

// isACPSessionNotFound 判断 err 是不是 agent 拒绝了一个它已不认识的会话 id。各 runtime
// 用不同 code 与措辞上报：Hermes 在 -32603（Internal error）下说 "Session not found"；
// Kiro 在 -32603 的 `data` 里放 "No session found with id ..."；kimi-cli 对每条未知
// 会话路径都在 `data` 里带 {"session_id": "Session not found"} 抛 invalid_params
// (-32602)（src/kimi_cli/acp/server.py）；Reasonix 在 -32602 下说 "session/resume:
// unknown session <id>"；ZeroClaw 在实现定义区间里自定义 SESSION_NOT_FOUND = -32000
// （zeroclaw-api/src/jsonrpc.rs）——所以单凭 code 或某家 runtime 的确切措辞都无法判别，
// 于是全部匹配。措辞检查仍承担决策：-32000 是通用 server-error code，绝不能让它的瞬时
// 失败被当成丢会话。
func isACPSessionNotFound(err error) bool {
	var rpcErr *acpRPCError
	if !errors.As(err, &rpcErr) {
		return false
	}
	if rpcErr.Code != -32603 && rpcErr.Code != -32602 && rpcErr.Code != -32002 && rpcErr.Code != -32000 {
		return false
	}
	text := strings.ToLower(rpcErr.Message + " " + rpcErr.Data)
	return strings.Contains(text, "session not found") ||
		strings.Contains(text, "no session found") ||
		strings.Contains(text, "unknown session")
}

// isACPHeldByProcess 判断 session/load 错误是否意味着该会话仍被某个尚未释放它的旧进程
// 锁着。Dim < 0.3.10 永久这样报；0.3.10+ 会在优雅退出或约 5s 后释放。调用方在放弃前
// 重试有限次数。
func isACPHeldByProcess(err error) bool {
	var rpcErr *acpRPCError
	if !errors.As(err, &rpcErr) {
		return false
	}
	text := strings.ToLower(rpcErr.Message + " " + rpcErr.Data)
	return strings.Contains(text, "held by another process")
}

// hermesResumeLostError 是 Hermes 无法重建的 resumed 会话的回退原因，只在没有捕获到更
// 具体的原因时使用。
const hermesResumeLostError = "hermes could not restore the resumed session; it refused the turn without running the agent"

// hermesResumeSessionLost 判断一次 *成功* 的 session/prompt 响应是否意味着我们 resume
// 的会话在 agent 侧已经不在了。
//
// isACPSessionNotFound 回答不了这个问题，因为 Hermes 从不把未知会话报成 JSON-RPC 错误。
// 它的 ACP 适配器对一个加载不了的会话，用带 stopReason=refusal 的普通成功帧应答
// `session/prompt`（acp_adapter/server.py，唯一发该 reason 的地方），而 `session/resume`
// 无论发生什么都回普通成功帧——ACP 的 ResumeSessionResponse schema 压根没有 sessionId
// 字段，与 NewSessionResponse 不同。整个交互没有一处是错误，这就是为什么 set_model 与
// prompt 时刻的 isACPSessionNotFound 分支对这个 runtime 永不触发，同一 (agent, issue)
// 对上的后续每次调度都在死会话上打转（GH #6150）。
//
// 它与 resolveHermesResumedSessionID 并存是必要的：后者读 `_meta.hermes.sessionProvenance`
// 在 resume 时刻捕捉「改绑到另一个会话」；本函数覆盖「用我们要求的那个会话应答、然后拒绝
// 运行它」。
//
// 两个条件缺一不可。单有 stopReason=refusal 是合法的模型拒绝；resumed 回合在真实工作
// 之后被拒绝也不代表丢会话——只有拒绝且完全没有 agent 活动（无文本、无思考、无工具
// 调用）才是 Hermes 在告诉我们它从未持有该会话。这个判断开启的 fresh-session 重试，
// 本身在 shouldRetryWithFreshSession 里以 tools == 0 为前提，所以动过手的回合绝不会
// 重跑。
func hermesResumeSessionLost(resumeSessionID, stopReason string, turnActivity int64) bool {
	return resumeSessionID != "" && stopReason == "refusal" && turnActivity == 0
}

func (c *hermesClient) handleResponse(raw map[string]json.RawMessage) {
	var id int
	if err := json.Unmarshal(raw["id"], &id); err != nil {
		// 退而求其次用 float 解析（JSON 数字默认解析成 float）。
		var fid float64
		if err := json.Unmarshal(raw["id"], &fid); err != nil {
			return
		}
		id = int(fid)
	}

	c.mu.Lock()
	pr, ok := c.pending[id]
	if ok {
		delete(c.pending, id)
	}
	c.mu.Unlock()

	if !ok {
		return
	}

	if errData, hasErr := raw["error"]; hasErr {
		var rpcErr struct {
			Code    int             `json:"code"`
			Message string          `json:"message"`
			Data    json.RawMessage `json:"data"`
		}
		_ = json.Unmarshal(errData, &rpcErr)
		// JSON-RPC 的 `data` 携带供应商专属原因（如 Kiro 对 code=-32603 返回
		// "No session found with id"）。把它暴露进包裹后的错误，daemon 日志/UI 就能展示
		// agent 为什么失败，而不是干巴巴的 "Internal error"。`data` 可以是任意 JSON 值：
		// 字符串去引号渲染，其它一律按原始 JSON。
		detail := ""
		if len(rpcErr.Data) > 0 && string(rpcErr.Data) != "null" {
			var s string
			if err := json.Unmarshal(rpcErr.Data, &s); err == nil {
				detail = s
			} else {
				detail = string(rpcErr.Data)
			}
		}
		pr.ch <- rpcResult{err: &acpRPCError{Method: pr.method, Code: rpcErr.Code, Message: rpcErr.Message, Data: detail}}
	} else {
		// 若这是 prompt 响应，提取用量与 stop reason。
		if pr.method == "session/prompt" {
			c.extractPromptResult(raw["result"])
		}
		pr.ch <- rpcResult{result: raw["result"]}
	}
}

func (c *hermesClient) extractPromptResult(data json.RawMessage) {
	var resp struct {
		StopReason string          `json:"stopReason"`
		Usage      json.RawMessage `json:"usage"`
		Meta       json.RawMessage `json:"_meta"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return
	}

	pr := hermesPromptResult{
		stopReason: resp.StopReason,
		modelID:    parseACPModelIDFromMeta(resp.Meta),
	}
	var usage acpUsageSnapshot
	if len(resp.Usage) > 0 && string(resp.Usage) != "null" {
		usage = parseACPTokenUsageSnapshot(resp.Usage)
	}
	// 有些 agent（特别是 xAI Grok Build）把按回合计量放在 result._meta 里，而不是标准
	// 顶层 usage 字段（或两者都有）。调和两种形态，避免部分镜像漏掉某个缓存桶或供应商
	// 上报的成本。
	pr.usage = usage.withFallback(parseACPTokenUsageSnapshotFromMeta(resp.Meta))

	if c.onPromptDone != nil {
		c.onPromptDone(pr)
	}
}

// acpTokenUsagePresent 判断任一 token 计数器是否非零。
func acpTokenUsagePresent(u TokenUsage) bool {
	return u.InputTokens > 0 || u.OutputTokens > 0 || u.CacheReadTokens > 0 || u.CacheWriteTokens > 0
}

func acpUsagePresent(u TokenUsage) bool {
	return acpTokenUsagePresent(u) || u.CostUSDTicks > 0
}

// parseACPModelIDFromMeta 从 ACP 结果的 `_meta` 对象里取模型 id。Grok Build 在每回合
// 盖上 `_meta.modelId`，这是该回合被记到哪具模型名下的唯一权威说法——会话握手只在
// `session/new` 上报模型 id，`session/load` 不上报，所以 resumed 会话没有别的来源。
func parseACPModelIDFromMeta(meta json.RawMessage) string {
	if len(meta) == 0 || string(meta) == "null" {
		return ""
	}
	var r struct {
		ModelID      string `json:"modelId"`
		ModelIDSnake string `json:"model_id"`
	}
	if err := json.Unmarshal(meta, &r); err != nil {
		return ""
	}
	if id := strings.TrimSpace(r.ModelID); id != "" {
		return id
	}
	return strings.TrimSpace(r.ModelIDSnake)
}

func (c *hermesClient) handleNotification(raw map[string]json.RawMessage) {
	var method string
	_ = json.Unmarshal(raw["method"], &method)
	if c.onNotification != nil {
		c.onNotification(method, raw["params"])
	}

	if method != "session/update" && method != "session/notification" {
		return
	}

	var params struct {
		SessionID string          `json:"sessionId"`
		Update    json.RawMessage `json:"update"`
	}
	if p, ok := raw["params"]; ok {
		_ = json.Unmarshal(p, &params)
	}
	if len(params.Update) == 0 {
		return
	}

	updateType, updateData := normalizeACPUpdate(params.Update)
	if c.acceptNotification != nil && !c.acceptNotification(updateType) {
		return
	}
	if c.onActivity != nil {
		c.onActivity()
	}

	switch updateType {
	case "agent_message_chunk":
		c.handleAgentMessage(updateData)
	case "agent_thought_chunk":
		c.handleAgentThought(updateData)
	case "tool_call":
		c.handleToolCallStart(updateData)
	case "tool_call_update":
		c.handleToolCallUpdate(updateData)
	case "usage_update":
		c.handleUsageUpdate(updateData)
	case "turn_end":
		c.extractPromptResult(updateData)
	}
}

func normalizeACPUpdate(data json.RawMessage) (string, json.RawMessage) {
	var updateType struct {
		SessionUpdate string `json:"sessionUpdate"`
		Type          string `json:"type"`
	}
	_ = json.Unmarshal(data, &updateType)
	if updateType.SessionUpdate != "" {
		return normalizeACPUpdateType(updateType.SessionUpdate), data
	}
	if updateType.Type != "" {
		return normalizeACPUpdateType(updateType.Type), data
	}

	// 有些 ACP 实现把枚举变体序列化成外部标记对象：{"agentMessageChunk": {"content": ...}}。
	var wrapper map[string]json.RawMessage
	if err := json.Unmarshal(data, &wrapper); err == nil && len(wrapper) == 1 {
		for k, v := range wrapper {
			return normalizeACPUpdateType(k), v
		}
	}

	return "", data
}

func normalizeACPUpdateType(t string) string {
	key := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(t), "_", ""), "-", ""))
	switch key {
	case "agentmessagechunk":
		return "agent_message_chunk"
	case "agentthoughtchunk":
		return "agent_thought_chunk"
	case "toolcall":
		return "tool_call"
	case "toolcallupdate":
		return "tool_call_update"
	case "usageupdate":
		return "usage_update"
	case "turnend", "endturn":
		return "turn_end"
	default:
		return ""
	}
}

func (c *hermesClient) handleAgentMessage(data json.RawMessage) {
	var msg struct {
		Content struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(data, &msg); err != nil || msg.Content.Text == "" {
		return
	}
	if c.onMessage != nil {
		c.onMessage(Message{Type: MessageText, Content: msg.Content.Text})
	}
}

func (c *hermesClient) handleAgentThought(data json.RawMessage) {
	var msg struct {
		Content struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(data, &msg); err != nil || msg.Content.Text == "" {
		return
	}
	if c.onMessage != nil {
		c.onMessage(Message{Type: MessageThinking, Content: msg.Content.Text})
	}
}

func (c *hermesClient) handleToolCallStart(data json.RawMessage) {
	var msg struct {
		ToolCallID string            `json:"toolCallId"`
		Name       string            `json:"name"`
		Title      string            `json:"title"`
		Kind       string            `json:"kind"`
		RawInput   map[string]any    `json:"rawInput"`
		Input      map[string]any    `json:"input"`
		Parameters map[string]any    `json:"parameters"`
		Content    []json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(data, &msg); err != nil {
		return
	}

	toolName := hermesToolNameFromTitle(msg.Title, msg.Kind)
	if toolName == "" {
		toolName = msg.Name
	}
	rawInput := msg.RawInput
	if rawInput == nil {
		rawInput = msg.Input
	}
	if rawInput == nil {
		rawInput = msg.Parameters
	}

	// Hermes 在初始 tool_call 上就预填了 rawInput——立即发出 MessageToolUse，让 UI 能
	// 实时展示这次工具调用。记下已发出，避免 handleToolCallUpdate 在完成时再发一次。
	if rawInput != nil {
		c.trackTool(msg.ToolCallID, &pendingToolCall{
			toolName: toolName,
			input:    rawInput,
			emitted:  true,
		})
		if c.onMessage != nil {
			c.onMessage(Message{
				Type:   MessageToolUse,
				Tool:   toolName,
				CallID: msg.ToolCallID,
				Input:  rawInput,
			})
		}
		return
	}

	// 没有 rawInput：起始帧携带的东西都在 content blocks 里。
	argsText := extractACPToolCallText(msg.Content)

	// 在起始帧就发出，是在途工具得以可见的全部手段：否则要到 tool_call_update 完成才有
	// 东西发出，卡住的工具会渲染成没有可见工具调用的空白运行——这正是 GH#6583 的症状——
	// 而且因为 daemon 的在途工具计数器只在 MessageToolUse 上推进，这样的调用永远不会记到
	// AgentToolWatchdog 头上，反而被短得多的 AgentIdleWatchdog 判定——于是一次合理的长
	// 工具调用被过早强停。
	//
	// 只有永远不会在稍后补发输入的方言才能这么做而不丢输入——见
	// toolStartCarriesFinalInput。
	if c.toolStartCarriesFinalInput {
		// content blocks 是显示输出而非输入，所以只有当它确实就是调用本身的那一种形态时
		// 才用作 Input。其余一律不上报 input，而不是把一段渲染文本（"Preparing write to
		// <path>"）冒充成调用的参数。
		var input map[string]any
		if acpToolCallStartCarriesInvocation(toolName, argsText) {
			input = parseToolArgsJSON(argsText)
		}
		c.trackTool(msg.ToolCallID, &pendingToolCall{
			toolName: toolName,
			argsText: argsText,
			input:    input,
			emitted:  true,
		})
		if c.onMessage != nil {
			c.onMessage(Message{
				Type:   MessageToolUse,
				Tool:   toolName,
				CallID: msg.ToolCallID,
				Input:  input,
			})
		}
		return
	}

	// Kimi 的 args 是逐 token 跨 tool_call_update 消息流式到达的；初始 tool_call 常常
	// 带空 content block。先把工具缓冲起来、推迟 MessageToolUse 的发出，避免 UI 看到
	// `{""` 这种写了一半的命令。
	c.trackTool(msg.ToolCallID, &pendingToolCall{
		toolName: toolName,
		argsText: argsText,
		emitted:  false,
	})
}

// acpToolCallStartCarriesInvocation 判断 tool_call 起始帧的 content 能否作为该调用的
// input 上报，而不是当作显示输出。
//
// 它刻意只认一种形态：`terminal` 调用、其 content 就是 `$ <command>` 这一行。ACP 里
// `content` 是工具调用产生的显示输出，`rawInput` 才是输入，把任意 content 当 input 会
// 记错调用。Hermes 对其整个「polished」工具集抑制 rawInput，并对每个工具渲染出不同的
// 内容：`terminal` 渲染 `$ <command>`（即调用本体），`write_file` 与 `patch` 渲染
// "Preparing write to <path>" 这类叙述，browser/web/media 工具渲染各自的摘要。只有
// terminal 的渲染即命令本身，其余一律不上报 input。
func acpToolCallStartCarriesInvocation(toolName, argsText string) bool {
	if toolName != "terminal" {
		return false
	}
	return strings.HasPrefix(strings.TrimSpace(argsText), "$ ")
}

func (c *hermesClient) handleToolCallUpdate(data json.RawMessage) {
	var msg struct {
		ToolCallID string            `json:"toolCallId"`
		Status     string            `json:"status"`
		Name       string            `json:"name"`
		Title      string            `json:"title"`
		Kind       string            `json:"kind"`
		RawInput   map[string]any    `json:"rawInput"`
		Input      map[string]any    `json:"input"`
		Parameters map[string]any    `json:"parameters"`
		RawOutput  json.RawMessage   `json:"rawOutput"`
		Output     json.RawMessage   `json:"output"`
		Content    []json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(data, &msg); err != nil {
		return
	}

	rawInput := msg.RawInput
	if rawInput == nil {
		rawInput = msg.Input
	}
	if rawInput == nil {
		rawInput = msg.Parameters
	}
	title := msg.Title
	if title == "" {
		title = msg.Name
	}

	// 进行中：只缓存更新。kimi 对一次工具调用会发很多条这样的 update，每条携带到当前
	// 为止的累积 args JSON。
	if msg.Status != "completed" && msg.Status != "failed" {
		if pending := c.getPendingTool(msg.ToolCallID); pending != nil && !pending.emitted {
			if text := extractACPToolCallText(msg.Content); text != "" {
				// kimi 每条帧都流整份累积 args；覆盖而不是拼接。
				pending.argsText = text
			}
		}
		return
	}

	// 完成：先发出任何延后的 MessageToolUse，再发结果。
	pending := c.takePendingTool(msg.ToolCallID)
	c.emitDeferredToolUse(pending, msg.ToolCallID, title, msg.Kind, rawInput)

	output := acpRawText(msg.RawOutput)
	if output == "" {
		output = acpRawText(msg.Output)
	}
	if output == "" {
		output = extractACPToolCallText(msg.Content)
	}
	if c.onMessage != nil {
		c.onMessage(Message{
			Type:   MessageToolResult,
			CallID: msg.ToolCallID,
			Output: output,
			Status: msg.Status,
		})
	}
}

// trackTool 为某 callID 存放 pending-tool 状态。懒初始化 map，让零值 hermesClient
// （测试里常见）在第一次工具调用时不至于 panic。
func (c *hermesClient) trackTool(callID string, p *pendingToolCall) {
	c.toolMu.Lock()
	defer c.toolMu.Unlock()
	if c.pendingTools == nil {
		c.pendingTools = make(map[string]*pendingToolCall)
	}
	c.pendingTools[callID] = p
}

// getPendingTool 返回 pending 条目（可能为 nil），不删除。零值 hermesClient 上也安全。
func (c *hermesClient) getPendingTool(callID string) *pendingToolCall {
	c.toolMu.Lock()
	defer c.toolMu.Unlock()
	if c.pendingTools == nil {
		return nil
	}
	return c.pendingTools[callID]
}

// takePendingTool 移除并返回 pending 条目；没有记录则返回 nil（例如工具在我们看到其
// start 之前就完成，或我们漏了 start 帧）。
func (c *hermesClient) takePendingTool(callID string) *pendingToolCall {
	c.toolMu.Lock()
	defer c.toolMu.Unlock()
	if c.pendingTools == nil {
		return nil
	}
	p := c.pendingTools[callID]
	delete(c.pendingTools, callID)
	return p
}

// emitDeferredToolUse 在对应的 MessageToolResult 之前发出缓冲的 MessageToolUse。
// 处理三种情况：
//   - hermes 工具：tool_call 时已发出 → 跳过
//   - 带流式 args 的 kimi 工具 → 把累积的 JSON 解析成 Input
//   - 未知工具（没看到 start 帧就收到 completed）→ 用 update 自己的字段合成最小信息
func (c *hermesClient) emitDeferredToolUse(
	p *pendingToolCall,
	callID, updateTitle, updateKind string,
	updateRawInput map[string]any,
) {
	if p != nil && p.emitted {
		return
	}

	var toolName string
	var input map[string]any

	switch {
	case p != nil && p.input != nil:
		// 已缓冲 rawInput 的路径——不该发生，因为那种情况我们置了 emitted=true，
		// 但防御性处理。
		toolName = p.toolName
		input = p.input
	case p != nil:
		toolName = p.toolName
		// update 上的 rawInput 是该调用的真实输入，所以它压过起始帧渲染进 content 的
		// 任何东西。ACP 允许起始帧只带显示文本（"Preparing write to <path>"）而把
		// rawInput 延后提供；不这样做的话，显示文本会被记成调用、真实输入被静默丢弃。
		if updateRawInput != nil {
			input = updateRawInput
		} else {
			input = parseToolArgsJSON(p.argsText)
		}
	default:
		// 没有 start 帧记录——回退用 update 自己的 title/kind/rawInput，让 UI 至少
		// 看到工具名。
		toolName = hermesToolNameFromTitle(updateTitle, updateKind)
		input = updateRawInput
	}

	if c.onMessage == nil {
		return
	}
	c.onMessage(Message{
		Type:   MessageToolUse,
		Tool:   toolName,
		CallID: callID,
		Input:  input,
	})
}

// parseToolArgsJSON 把 kimi 的累积 args 字符串转成 UI 期望出现在 Message.Input 下的
// 结构化 map。Kimi 把 args 作为 JSON 编码对象发送（`{"command":"echo hi"}`），完整
// JSON 解析能还原原始工具参数形态。畸形输入（流式抖动、非 JSON 工具）时把原始文本放在
// `text` 键下保留，UI 至少有东西可渲染。
func parseToolArgsJSON(argsText string) map[string]any {
	argsText = strings.TrimSpace(argsText)
	if argsText == "" {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(argsText), &m); err == nil {
		return m
	}
	return map[string]any{"text": argsText}
}

// extractACPToolCallText 拼接 tool_call / tool_call_update `content` 数组里每个 ACP
// block 的渲染文本。
//
// 处理 kimi 发出的两种 block：
//   - {type:"content", content:{type:"text", text:"..."}}——纯文本（shell 输出、工具
//     args）。文本原样拼接。
//   - {type:"diff", path, oldText, newText}——FileEdit 输出。渲染成极简 unified-diff
//     头，让 UI 无需 diff 查看器也能区分写与读。
//
// acpRawText 渲染可能以 JSON 字符串或结构化值到达的 ACP 输出字段（rawOutput / output）。
// 某些模型适配器——尤其 Kiro 的 GPT-5.6 Sol 路径——把完成的 tool_call_update 的
// rawOutput 发成对象（如 {"items":[{"Json":{...}}]}）而非字符串。把该字段声明为 Go
// string 会让 json.Unmarshal 失败，使 handleToolCallUpdate 提前返回并静默 DROP 整条
// update——包括它的 status:"completed"——完成信号丢失，任务被错误标成 failed
// （issue #5509 / MUL-4860）。两种形态都接受。
func acpRawText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	// 非字符串（对象 / 数组 / 数字）：把原始 JSON 当文本保留，输出不丢失而是保留。
	return string(raw)
}

// Terminal blocks ({type:"terminal", terminalId}) 引用一个经 terminal/output 提供输出的
// 终端。这里文本抽取器没有可复制的载荷；ACP 传输层会保留终端输出。
func extractACPToolCallText(blocks []json.RawMessage) string {
	var b strings.Builder
	appendPiece := func(piece string) {
		if piece == "" {
			return
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(piece)
	}
	for _, raw := range blocks {
		var kind struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(raw, &kind); err != nil {
			continue
		}
		switch kind.Type {
		case "content":
			var outer struct {
				Content json.RawMessage `json:"content"`
			}
			if err := json.Unmarshal(raw, &outer); err != nil || len(outer.Content) == 0 {
				continue
			}
			var inner struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			if err := json.Unmarshal(outer.Content, &inner); err != nil {
				continue
			}
			if inner.Type != "text" {
				continue
			}
			appendPiece(inner.Text)
		case "diff":
			var diff struct {
				Path    string `json:"path"`
				OldText string `json:"oldText"`
				NewText string `json:"newText"`
			}
			if err := json.Unmarshal(raw, &diff); err != nil || diff.Path == "" {
				continue
			}
			// 保持极小——完整 unified diff 可能巨大，而这里只想记录「该工具写了这个
			// 文件」。UI 需要真实内容时可以自己去重读文件。
			var piece strings.Builder
			piece.WriteString("--- ")
			piece.WriteString(diff.Path)
			piece.WriteString("\n+++ ")
			piece.WriteString(diff.Path)
			if diff.OldText == "" {
				piece.WriteString("\n(new file, ")
				piece.WriteString(strconv.Itoa(len(diff.NewText)))
				piece.WriteString(" bytes)")
			} else {
				piece.WriteString("\n(edited: ")
				piece.WriteString(strconv.Itoa(len(diff.OldText)))
				piece.WriteString(" → ")
				piece.WriteString(strconv.Itoa(len(diff.NewText)))
				piece.WriteString(" bytes)")
			}
			appendPiece(piece.String())
		default:
			// terminal blocks、image blocks、未知的未来类型——忽略。我们没办法内联渲染它们。
		}
	}
	return b.String()
}

func (c *hermesClient) handleUsageUpdate(data json.RawMessage) {
	var msg struct {
		Usage json.RawMessage `json:"usage"`
	}
	if err := json.Unmarshal(data, &msg); err != nil {
		return
	}
	c.mergeUsage(parseACPTokenUsageSnapshot(msg.Usage))
}

func (c *hermesClient) mergeUsage(usage acpUsageSnapshot) {
	c.usageMu.Lock()
	c.usage.merge(usage)
	c.usageMu.Unlock()
}

func (c *hermesClient) accumulatedUsage() TokenUsage {
	c.usageMu.Lock()
	defer c.usageMu.Unlock()
	return c.usage.TokenUsage
}

// ── 辅助函数 ──

// extractACPSessionID 从 session/new 或 session/resume 响应里取 `sessionId`。所有 ACP
// 后端共用（hermes、kimi、kiro 以及任何遵循标准 ACP schema 的其它后端）。
func extractACPSessionID(result json.RawMessage) string {
	var r struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(result, &r); err != nil {
		return ""
	}
	return r.SessionID
}

// extractACPAuthMethods 按 agent 列出的顺序返回 ACP `initialize` 响应公布的
// `authMethods` ids。需要认证的 agent（如 xAI 的 Grok Build）在此枚举可接受的方法；按
// ACP 流程，客户端必须在 `session/new` / `session/load` 之前用其中一个 id 发送
// `authenticate`。无需显式认证的 agent 会省略该字段，所以空切片 = 「跳过 authenticate」。
// 畸形响应退化为空切片（解析失败时放行，免得把从未需要这步的 agent 卡住）。
func extractACPAuthMethods(result json.RawMessage) []string {
	var r struct {
		AuthMethods []struct {
			ID string `json:"id"`
		} `json:"authMethods"`
	}
	if err := json.Unmarshal(result, &r); err != nil {
		return nil
	}
	ids := make([]string, 0, len(r.AuthMethods))
	for _, m := range r.AuthMethods {
		if id := strings.TrimSpace(m.ID); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

// extractACPCurrentModelID 从 session/new 或 session/resume 响应里取 ACP runtime 选中
// 的模型。Hermes 用自己的默认模型时返回它，这样即使 Multica 没传显式 agent.model
// 覆盖，token 用量仍能归到真实模型上。
func extractACPCurrentModelID(result json.RawMessage) string {
	var r struct {
		Models struct {
			CurrentModelID      string `json:"currentModelId"`
			CurrentModelIDSnake string `json:"current_model_id"`
		} `json:"models"`
		CurrentModelID      string `json:"currentModelId"`
		CurrentModelIDSnake string `json:"current_model_id"`
	}
	if err := json.Unmarshal(result, &r); err != nil {
		return ""
	}
	for _, candidate := range []string{
		r.Models.CurrentModelID,
		r.Models.CurrentModelIDSnake,
		r.CurrentModelID,
		r.CurrentModelIDSnake,
	} {
		if model := strings.TrimSpace(candidate); model != "" {
			return model
		}
	}
	return ""
}

// resolveResumedSessionID 决定一次 `session/resume` 往返之后，该把哪个会话 id 当作存活。
// Hermes（及其它 ACP server）在响应里返回权威 sessionId——当本地 state.db 被清掉时，
// server 静默新建一个全新会话并返回它的新 id，而不是失败。若那种情况下我们继续用请求的
// id，之后每条 session/prompt 都发给 server 不认识的会话并以 JSON-RPC -32603 失败。
// 返回 (chosenID, changed)。响应畸形或省略 sessionId 时回退到请求的 id，让快乐路径在
// 旧/不合规 server 上继续工作。
func resolveResumedSessionID(requested string, response json.RawMessage) (string, bool) {
	got := extractACPSessionID(response)
	if got == "" {
		return requested, false
	}
	return got, got != requested
}

// resolveHermesResumedSessionID 是 session/resume 响应的 Hermes 专属解读：返回存活的
// 会话 id，以及 resume 是否真的落在了我们请求的会话上。
//
// 上面的共享解析器在这里不够用，因为 ACP 的 ResumeSessionResponse 根本没有 sessionId
// 字段（acp/schema.py ResumeSessionResponse），与 NewSessionResponse 不同。Hermes 对自己
// state.db 已不持有的会话做 resume 时，静默新建一个（acp_adapter/server.py resume_session:
// "not found, creating new"）并以普通成功帧应答，所以过去「改绑」无法与真实 resume 区分：
// daemon 每回合重发同一个死 id，用户得到一段从零重来、却没有任何报错的对话（GH #6806）。
//
// Hermes 会在 `_meta.hermes.sessionProvenance` 上报它实际用哪个会话服务了该请求——这是
// 唯一可用的信号。两种形态都不带的响应回退到请求的 id 并报 landed=true，让什么都不报的
// runtime 保持旧行为，而不是宣布每回合历史都丢了。
//
// 那个残余盲区在 daemon 侧有界：resume 门只把会话 id 交给会话存储里确实持有 transcript
// 的运行（sessionHomeReachable），所以静默 runtime 顶多误报过期会话，绝不会误报不存在的。
func resolveHermesResumedSessionID(requested string, response json.RawMessage) (string, bool) {
	got := extractACPSessionID(response)
	if got == "" {
		got = extractACPProvenanceSessionID(response)
	}
	if got == "" {
		return requested, true
	}
	return got, got == requested
}

// extractACPProvenanceSessionID 取 Hermes 在 `_meta.hermes.sessionProvenance.acpSessionId`
// 上报的会话 id——server 实际用哪个 ACP 面 id 服务了该请求。`currentHermesSessionId` 是
// 同一会话的 agent 内部 id，刻意不读：Hermes 把会话压缩成后继者时两者会分叉，而我们之后
// 的 prompt 只寻址 ACP id。
func extractACPProvenanceSessionID(result json.RawMessage) string {
	var r struct {
		Meta struct {
			Hermes struct {
				SessionProvenance struct {
					ACPSessionID string `json:"acpSessionId"`
				} `json:"sessionProvenance"`
			} `json:"hermes"`
		} `json:"_meta"`
	}
	if err := json.Unmarshal(result, &r); err != nil {
		return ""
	}
	return strings.TrimSpace(r.Meta.Hermes.SessionProvenance.ACPSessionID)
}

// hermesTurnText 在任务本要延续对话、但 resume 却落在全新会话上时，前置调用方的
// 连续性提示。与 codex 后端的 codexTurnInput 同一契约：daemon 自己的 prompt 已带提示时
// 提示为空，回合绝不会为同一段话付两次。
func hermesTurnText(prompt string, resumeExpected, resumeLanded bool, notice string) string {
	if resumeExpected && !resumeLanded {
		return notice + prompt
	}
	return prompt
}

// buildHermesSessionParams 构造 ACP `session/new` 请求的 params map。`model` 字段只在
// 非空时包含，让 Hermes 仅在没配显式模型时回退到默认。
//
// mcpServers 应是 buildACPMcpServers 从 agent 的 mcp_config 生成的 ACP 形态数组；
// nil 切片归一化成空数组，让线上请求总是携带该字段（ACP 要求）。
func buildHermesSessionParams(cwd, model string, mcpServers []any) map[string]any {
	if mcpServers == nil {
		mcpServers = []any{}
	}
	params := map[string]any{
		"cwd":        cwd,
		"mcpServers": mcpServers,
	}
	if model != "" {
		params["model"] = model
	}
	return params
}

// buildACPMcpServers 把 agent 的 Claude 风格 mcp_config（`{"mcpServers": {"<name>":
// {...}}}`）翻译成 ACP `session/new` 与 `session/load` 请求期望的数组形态。
//
// 每条 Claude 风格条目映射成下面之一：
//
//   - Stdio：`{name, command, args, env: [{name,value}, ...]}`——条目带 `command` 字段
//     时。不输出 `type` 字段；ACP 把未打标条目当 stdio。
//   - HTTP / SSE：`{type, name, url, headers: [{name,value}, ...]}`——条目带 `url` 字段
//     时。`type` 默认 "http"；Claude 的 "sse" 与 "streamable-http" / "http_streamable"
//     别名都可接受。
//
// 空 / null 输入返回空切片——启动不带 MCP 服务器继续（ACP 后端既有默认）。顶层 JSON
// 畸形返回错误让启动关门失败，对照 codex 的 `renderCodexMcpServersBlock` 契约。既无
// `command` 也无 `url` 的个别条目带警告跳过，而不是让整个启动失败——单个坏条目不应弄死
// agent。
//
// 输出条目按名字排序、每条内的 env / headers 按键排序，线上请求跨重跑确定——对测试、
// 日志 diff 与可复现性都有用。
func buildACPMcpServers(raw json.RawMessage, logger *slog.Logger) ([]any, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return []any{}, nil
	}
	var parsed struct {
		McpServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(trimmed, &parsed); err != nil {
		return nil, fmt.Errorf("parse mcp_config json: %w", err)
	}
	if len(parsed.McpServers) == 0 {
		warnNonCanonicalMcpConfigKey(trimmed, logger)
		return []any{}, nil
	}

	names := make([]string, 0, len(parsed.McpServers))
	for name := range parsed.McpServers {
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]any, 0, len(names))
	for _, name := range names {
		entry, err := convertACPMcpServer(name, parsed.McpServers[name])
		if err != nil {
			if logger != nil {
				logger.Warn("skipping invalid mcp_config entry", "name", name, "error", err)
			}
			continue
		}
		out = append(out, entry)
	}
	return out, nil
}

// acpAltMcpConfigKeys 是 runtime 原生 MCP 配置文件用、而非 Multica 权威 `mcpServers`
// 键的顶层键：jcode 与 Kiro 嵌在 `servers` 下，OpenCode 在 `mcp` 下，Codex 的 TOML
// 在 `mcp_servers` 下。
var acpAltMcpConfigKeys = []string{"servers", "mcp", "mcp_servers"}

// warnNonCanonicalMcpConfigKey 在 mcp_config 没有 `mcpServers` 键、却在一个 runtime
// 原生键下持有服务器时记日志。
//
// mcp_config 以不透明 JSON 存储，所以直接粘贴某个 runtime 自己的配置文件既容易又——
// 在这个警告之前——完全静默：配置保存成功、daemon 转发空数组、agent 跑起来却没有 MCP
// 工具，且任何地方都没有日志（#6540）。这里只警告不采纳，因为周边条目的形态未必匹配
// ACP，猜测着收编有把半懂不懂的配置转发出去的风险。
func warnNonCanonicalMcpConfigKey(raw json.RawMessage, logger *slog.Logger) {
	if logger == nil {
		return
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return
	}
	if _, ok := top["mcpServers"]; ok {
		return
	}
	for _, key := range acpAltMcpConfigKeys {
		nested, ok := top[key]
		if !ok {
			continue
		}
		var entries map[string]json.RawMessage
		if err := json.Unmarshal(nested, &entries); err != nil || len(entries) == 0 {
			continue
		}
		logger.Warn("mcp_config has no \"mcpServers\" key, so no MCP servers will be sent to the runtime",
			"found_key", key,
			"server_count", len(entries),
			"hint", `mcp_config must be shaped {"mcpServers": {"<name>": {...}}}; a runtime's own config file is not accepted verbatim`)
		return
	}
}

// convertACPMcpServer 把一条 Claude 风格条目转成 ACP 的 McpServer 线上形态。既无
// command 也无 url 的条目返回错误。
func convertACPMcpServer(name string, raw json.RawMessage) (map[string]any, error) {
	var entry struct {
		Type    string            `json:"type"`
		Command string            `json:"command"`
		Args    []string          `json:"args"`
		Env     map[string]string `json:"env"`
		URL     string            `json:"url"`
		Headers map[string]string `json:"headers"`
	}
	if err := json.Unmarshal(raw, &entry); err != nil {
		return nil, fmt.Errorf("parse entry: %w", err)
	}

	command := strings.TrimSpace(entry.Command)
	url := strings.TrimSpace(entry.URL)

	if command != "" {
		args := entry.Args
		if args == nil {
			args = []string{}
		}
		envArr := make([]map[string]any, 0, len(entry.Env))
		for _, k := range sortedStringMapKeys(entry.Env) {
			envArr = append(envArr, map[string]any{
				"name":  k,
				"value": entry.Env[k],
			})
		}
		return map[string]any{
			"name":    name,
			"command": command,
			"args":    args,
			"env":     envArr,
		}, nil
	}

	if url != "" {
		t := strings.ToLower(strings.TrimSpace(entry.Type))
		switch t {
		case "sse":
			t = "sse"
		case "", "http", "streamable-http", "http_streamable":
			t = "http"
		default:
			// 未知的远程传输——降级成 "http" 而不是失败。不认该类型的 ACP server 会拒绝
			// session/new 请求，把真实错误亮给用户。
			t = "http"
		}
		headerArr := make([]map[string]any, 0, len(entry.Headers))
		for _, k := range sortedStringMapKeys(entry.Headers) {
			headerArr = append(headerArr, map[string]any{
				"name":  k,
				"value": entry.Headers[k],
			})
		}
		return map[string]any{
			"type":    t,
			"name":    name,
			"url":     url,
			"headers": headerArr,
		}, nil
	}

	return nil, fmt.Errorf("entry has neither command nor url")
}

func sortedStringMapKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// acpMcpTransportCapabilities 报告 ACP runtime 在 `initialize` 响应里公布的远程 MCP
// 传输。stdio 永远受支持（它是基线传输，spec 不设门禁），所以这里不表示。
// acpMcpCapabilityDeclaration 把 ACP `initialize` 响应在远程 MCP 传输上告诉我们的事
// 分类。ACP v1 下三种状态过滤口径一致——未声明的传输即不支持——但「省略能力」例外只能
// 以「真正的沉默」为钥匙，所以「runtime 什么也没说」必须与「我们读不了 runtime 说的」
// 保持可区分。
type acpMcpCapabilityDeclaration int

const (
	// acpMcpCapabilitiesInvalid 刻意是零值：我们解析不了的响应、非对象 block、或字段
	// 类型错误的响应。任何进入此状态的东西都关门失败，偶然的零值永远不会扩大访问。
	acpMcpCapabilitiesInvalid acpMcpCapabilityDeclaration = iota
	// acpMcpCapabilitiesOmitted 是形态良好、却完全没有 mcpCapabilities block 的响应——
	// 例外唯一接受的状态。
	acpMcpCapabilitiesOmitted
	// acpMcpCapabilitiesDeclared 是可用的 block，其 HTTP/SSE 字段权威（包括两者皆 false）。
	acpMcpCapabilitiesDeclared
)

type acpMcpTransportCapabilities struct {
	Declaration acpMcpCapabilityDeclaration
	HTTP        bool
	SSE         bool
}

// extractACPMcpCapabilities 从 ACP `initialize` 响应里读
// `agentCapabilities.mcpCapabilities.http` 与 `.sse`。
//
// 按 ACP v1 的能力协商，"Clients and Agents MUST treat all capabilities omitted in
// the initialize request as UNSUPPORTED"，且 `http` / `sse` 除 false 外没有默认。
// 因此每个状态都解析成「两种传输都不支持」；这里的分类只决定「省略能力」例外是否可以
// 适用。
//
// 每一层都用原始 JSON 解码而不是直接解进目标 struct，因为 encoding/json 对非指针目标
// 遇到 `null` 会原样不动、且不报错。直接一次 Unmarshal 会把 `null`、`{"agentCapabilities":null}`
// 和真正沉默的响应读成同一种东西，让不可读的响应也能吃到例外。ACP 的 InitializeResponse
// 是对象、`agentCapabilities` 不可为空，所以前两者是畸形而非沉默。
//
// 见 https://agentclientprotocol.com/protocol/v1/initialization#capabilities
// 与 https://pkg.go.dev/encoding/json#Unmarshal 关于 null 的规则。
func extractACPMcpCapabilities(result json.RawMessage) acpMcpTransportCapabilities {
	invalid := acpMcpTransportCapabilities{Declaration: acpMcpCapabilitiesInvalid}
	omitted := acpMcpTransportCapabilities{Declaration: acpMcpCapabilitiesOmitted}

	var top map[string]json.RawMessage
	if err := json.Unmarshal(result, &top); err != nil || top == nil {
		return invalid
	}
	rawAgentCaps, ok := top["agentCapabilities"]
	if !ok {
		// 形态良好、但完全不声明能力的响应。
		return omitted
	}
	var agentCaps map[string]json.RawMessage
	if err := json.Unmarshal(rawAgentCaps, &agentCaps); err != nil || agentCaps == nil {
		return invalid
	}
	rawMcp, ok := agentCaps["mcpCapabilities"]
	if !ok {
		// 真实 hermes 0.18.2 的形态：声明了能力，这个 block 确实缺失。只有这个状态
		// 能走到例外。
		return omitted
	}
	rawMcp = bytes.TrimSpace(rawMcp)
	if bytes.Equal(rawMcp, []byte("null")) {
		return invalid
	}
	var caps struct {
		HTTP bool `json:"http"`
		SSE  bool `json:"sse"`
	}
	// 畸形 block（字段类型错误、非对象）不可用。不可用的声明不是沉默，所以绝不能放它进
	// 「省略能力」例外——它关门失败。
	if err := json.Unmarshal(rawMcp, &caps); err != nil {
		return invalid
	}
	return acpMcpTransportCapabilities{
		Declaration: acpMcpCapabilitiesDeclared,
		HTTP:        caps.HTTP,
		SSE:         caps.SSE,
	}
}

// acpRuntimesToleratingOmittedMcpCapabilities 列出这些 ACP 供应商：其随附二进制经证实
// 即使 initialize 响应不带 mcpCapabilities block，session/new 上也接受 http/sse 的
// McpServer 条目。
//
// ACP v1 要求把省略的能力读成不支持，所以这是对 spec 默认刻意的、窄的例外，而非替代。
// hermes 0.18.2 不声明 mcpCapabilities 却接受两种远程传输，在那里套默认值会静默丢掉
// 用户配置的每个远程 MCP 服务器，而且丢失只在 daemon 日志里可见（#6540）。
//
// 只有带着真实二进制证据才能往这里加供应商——一个既省略能力又拒绝远程条目的 ACP 实现，
// 会把「少个工具」变成「整个会话失败」，所以关门失败的默认必须仍是所有未验证者的规则。
var acpRuntimesToleratingOmittedMcpCapabilities = map[string]bool{
	"hermes": true,
}

// acpToleratesOmittedMcpCapabilities 判断本次启动是否可以回退到「省略能力」例外。
//
// 单凭供应商名回答不了这个问题。自定义 runtime profile 会把 protocol family 保留为
// 供应商、只换掉自己的 command，所以 `protocol_family: hermes` + `command_name: jcode`
// 以 "hermes" 身份到达本后端、却是没人验证过的二进制——正是白名单要排除的那种未验证
// 实现。Config.BuiltinRuntime 是 daemon 对「这是哪种情况」的报告，且默认 false，
// 让任何不设置它的调用方关门失败。
func acpToleratesOmittedMcpCapabilities(backend string, cfg Config) bool {
	return cfg.BuiltinRuntime && acpRuntimesToleratingOmittedMcpCapabilities[backend]
}

// filterACPMcpServersByCapability 丢弃传输未被 runtime 在 initialize 响应里公布的远程
// MCP 条目。stdio 条目（无 `type` 字段）总是放行。
//
// 把 http/sse 条目发给不支持它的 runtime 是 ACP spec 意义上的协议违规，且可能让整个
// session/new 请求以 JSON-RPC 错误被拒。丢掉违规条目让会话的其余部分能启动，而不是
// 让该 agent 上的每个任务都翻车。
//
// 唯一例外是 acpToleratesOmittedMcpCapabilities 接受、且其响应完全没有声明能力的启动；
// 为何要开这个口、为何保持很窄，见那两处。我们读不了的响应不是沉默，永远不够格。
func filterACPMcpServersByCapability(
	servers []any,
	caps acpMcpTransportCapabilities,
	backend string,
	cfg Config,
) []any {
	logger := cfg.Logger
	if len(servers) == 0 {
		return servers
	}
	if caps.Declaration == acpMcpCapabilitiesOmitted && acpToleratesOmittedMcpCapabilities(backend, cfg) {
		if logger != nil && acpHasRemoteMcpEntry(servers) {
			logger.Info("runtime declared no mcpCapabilities; forwarding remote MCP servers under this runtime's verified exception",
				"backend", backend)
		}
		return servers
	}
	filtered := make([]any, 0, len(servers))
	for _, raw := range servers {
		entry, ok := raw.(map[string]any)
		if !ok {
			filtered = append(filtered, raw)
			continue
		}
		transport, _ := entry["type"].(string)
		switch transport {
		case "http":
			if !caps.HTTP {
				if logger != nil {
					logger.Warn("dropping http MCP server: runtime did not advertise mcpCapabilities.http",
						"backend", backend, "name", entry["name"])
				}
				continue
			}
		case "sse":
			if !caps.SSE {
				if logger != nil {
					logger.Warn("dropping sse MCP server: runtime did not advertise mcpCapabilities.sse",
						"backend", backend, "name", entry["name"])
				}
				continue
			}
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

// acpHasRemoteMcpEntry 判断是否存在任一使用远程传输的条目，即能力问题对该配置是否
// 相关。
func acpHasRemoteMcpEntry(servers []any) bool {
	for _, raw := range servers {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if transport, _ := entry["type"].(string); transport == "http" || transport == "sse" {
			return true
		}
	}
	return false
}

// hermesToolNameFromTitle 从 ACP 工具调用标题里提取工具名。Hermes ACP 标题形如
// "terminal: ls -la"、"read: /path/to/file" 等。有些标题没有冒号（如 "execute code"）。
func hermesToolNameFromTitle(title string, kind string) string {
	// 先匹配精确标题（无冒号）。
	switch title {
	case "execute code":
		return "execute_code"
	}

	// 尝试取第一个冒号之前的工具名。
	if idx := strings.Index(title, ":"); idx > 0 {
		name := strings.TrimSpace(title[:idx])
		// 把常见 ACP 标题前缀映射回工具名。
		// 有些标题带模式信息，如 "patch (replace)"，所以用前缀匹配。
		switch {
		case name == "terminal":
			return "terminal"
		case name == "read":
			return "read_file"
		case name == "write":
			return "write_file"
		case strings.HasPrefix(name, "patch"):
			return "patch"
		case name == "search":
			return "search_files"
		case name == "web search":
			return "web_search"
		case name == "extract":
			return "web_extract"
		case name == "delegate":
			return "delegate_task"
		case name == "analyze image":
			return "vision_analyze"
		}
		return name
	}

	// 回退到 kind。
	switch kind {
	case "read":
		return "read_file"
	case "edit":
		return "write_file"
	case "execute":
		return "terminal"
	case "search":
		return "search_files"
	case "fetch":
		return "web_search"
	case "think":
		return "thinking"
	default:
		// 分类不了时保留非空标题：kimi 会发 "Shell" 或 "Read file" 这种裸标题、不带
		// `kind`，在这里返回空串会在 kimiToolNameFromTitle 能映射之前就把工具名整个丢
		// 掉。Hermes 标题总带冒号，所以 hermes 永远不会带着非空标题走到这个分支。
		if title != "" {
			return title
		}
		return kind
	}
}

// ── 供应商错误嗅探 ──
//
// ACP agent（hermes、kimi……）都有同一个失败形态：即使对已配置 LLM 端点的底层 HTTP
// 调用出错，session/prompt 也报 stopReason=end_turn——可操作细节只出现在 stderr（如
// `⚠️ API call failed (attempt 1/3): BadRequestError [HTTP 400]` 与 `Error: HTTP 400:
// Error code: 400 - {'detail': "The '...' model is not supported when using Codex with
// a ChatGPT account."}`）。嗅探器扫这些模式，让 daemon 能浮出真实失败，而不是泛泛的
// "empty output"。
//
// 以供应商名做参数，让 hermes 与 kimi 共用同一传输：这些正则匹配两个 runtime 都会发
// 的格式级信号（HTTP 状态、错误类型标签、"API call failed" 横幅）。
//
// 嗅探器区分 *瞬时* 单次告警（如 "API call failed (attempt 1/3): RateLimitError
// [HTTP 429]"——后面跟着一次成功重试）与 *终态* 放弃失败（如 "API call failed after
// 3 retries: ..." 或 "❌ ... Non-retryable"）：`message()` 返回最后看到的那条，而
// `terminalMessage()` 只在命中终态失败标记时返回非空。升级为 status="failed" 必须用
// `terminalMessage()`，否则早前单次告警后的一次成功重试会被误标成 failed。
type acpProviderErrorSniffer struct {
	provider string
	mu       sync.Mutex
	remains  []byte   // 跨写入缓冲不完整尾行的缓冲区
	lines    []string // 捕获的错误行，有界
	seen     map[string]bool
	terminal bool // sticky：至少一行命中 acpTerminalErrorRe
	// echoJSON 追踪来自 Python INFO/DEBUG root-logger 记录的、尚未完成的结构化载荷。
	// JSON 扫描状态跨 Write 调用持久化，只有真正的载荷续行会被跳过。
	echoJSON     bool
	echoDepth    int
	echoInString bool
	echoEscaped  bool
}

// acpErrorHeaderRe 匹配 API 错误块的第一行。ACP agent 通常用 ⚠️ / ❌ 作前缀并带上
// HTTP 状态码或不可重试错误标签。
var acpErrorHeaderRe = regexp.MustCompile(`(?:⚠️|❌|\[ERROR\]).*(?:BadRequestError|AuthenticationError|RateLimitError|HTTP [0-9]{3}|Non-retryable|API call failed)`)

// acpErrorDetailRe 从错误块后续行里抓最有用的单行消息（带 "Error:" 或 "Details:" 标签、
// 真正讲明发生了什么的那行）。
var acpErrorDetailRe = regexp.MustCompile(`(?:Error:|detail:|Details:)\s*(.+)`)

// acpTerminalErrorRe 匹配只在适配器对上游调用*放弃*时才出现的标记——要么重试耗尽
// （"after N retries"），要么错误一开始就被归类为不可重试（Non-retryable、BadRequest /
// Authentication 错误、❌ / [ERROR] 日志级别）。单次尝试的告警（"(attempt 1/3)"）
// 刻意不匹配本模式。
var acpTerminalErrorRe = regexp.MustCompile(`(?:❌|\[ERROR\]|after \d+ retr|Non-retryable|BadRequestError|AuthenticationError)`)

// acpAgentOutputTerminalRe 匹配 hermes 风格 ACP 适配器在对上游 LLM 重试耗尽时注入的
// 合成 agent 文本回合（"API call failed after 3 retries: HTTP 429..."），它经
// session/update agent_message_chunk 浮出并落进最终输出缓冲。单次尝试告警（只走 stderr、
// 用 "(attempt N/M)" 措辞）不会命中。
var acpAgentOutputTerminalRe = regexp.MustCompile(`API call failed after \d+ retr(?:y|ies)`)

const acpMaxErrorLines = 8

// acpLogRecordPrefixRe 匹配 Hermes 写到 stderr 的 Python `logging` 记录开头：
// "YYYY-MM-DD HH:MM:SS[,mmm] [LEVEL] logger: ..."。捕获组为 level、logger、载荷。
// 匹配所有 logger 很重要：非 root 的 ERROR 记录也会结束前面那条 root INFO 记录，且
// 必须继续对供应商错误匹配器可见。
var acpLogRecordPrefixRe = regexp.MustCompile(
	`^[0-9]{4}-[0-9]{2}-[0-9]{2} [0-9]{2}:[0-9]{2}:[0-9]{2}(?:,[0-9]+)? \[([A-Z]+)\] ([A-Za-z0-9_.-]+):\s*(.*)$`,
)

// acpEchoLogLevels 是 Hermes 纯粹用来向 stderr 回显对话与工具数据的 Python root-logger
// 级别。这些记录是任意载荷、绝不是供应商错误，即使内嵌 "❌"、"Error:"、"HTTP 429"、
// "API call failed" 等字符串。ERROR / WARNING / CRITICAL 记录是真实诊断，必须继续匹配。
var acpEchoLogLevels = map[string]bool{"INFO": true, "DEBUG": true}

// acpMaxErrorLineLen 限制持久化供应商错误摘要的长度。真实的 ACP 供应商错误行很短（一条
// 错误头加一行 "Error: ..." 细节——见 TestHermesProviderErrorSniffer，约 150 字节），但
// 溜过日志记录过滤器的超大回显可能存下几十 KB。该上限只在 *构造* 存储消息时施加，绝不
// 是 *归类* 一行是否为错误的先决条件：单行恰好超过此长度的真实供应商失败仍必须让运行
// 失败，而不是被静默丢弃（GitHub multica#5862——不要以长度作为匹配门槛）。
const acpMaxErrorLineLen = 4096

// newACPProviderErrorSniffer 返回一个把消息打上给定供应商名（如 "hermes"、"kimi"）标签
// 的嗅探器，让失败字符串一眼看出是哪个 runtime 产出的错误。
func newACPProviderErrorSniffer(provider string) *acpProviderErrorSniffer {
	return &acpProviderErrorSniffer{provider: provider, seen: map[string]bool{}}
}

// Write 实现 io.Writer，让嗅探器能坐在普通 stderr 日志转发器旁边的 io.MultiWriter 后面。
func (s *acpProviderErrorSniffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	data := append(s.remains, p...)
	// 把最后不完整的行（没有结尾换行）留给下一次写入，避免多行错误块被切碎。
	nl := strings.LastIndexByte(string(data), '\n')
	var complete string
	if nl < 0 {
		s.remains = append(s.remains[:0], data...)
		return len(p), nil
	}
	complete = string(data[:nl])
	s.remains = append(s.remains[:0], data[nl+1:]...)

	for _, rawLine := range strings.Split(complete, "\n") {
		rawLine = strings.TrimSuffix(rawLine, "\r")
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		// INFO/DEBUG root 记录是对话/工具回显，永远到不了错误匹配器。其载荷是结构化
		// JSON 时，跨物理行跟踪花括号深度，只跳到该 JSON 闭合为止。于是完整单行回显
		// 无法藏起后面一条裸的供应商错误。任何 Python logger 前缀也开启一条新记录，
		// 非 root 的 ERROR 诊断因此保持可见（GitHub multica#5862）。
		if m := acpLogRecordPrefixRe.FindStringSubmatch(line); m != nil {
			s.resetEchoJSON()
			if acpEchoLogLevels[m[1]] && m[2] == "root" {
				s.startEchoJSON(m[3])
				continue
			}
		} else if s.echoJSON {
			// 不带缩进的强供应商错误头是一条新的裸错误块，即使畸形回显从未闭合。
			// 缩进过的、看起来像错误的内容仍是结构化回显的一部分。回显在 JSON 字符串
			// 内部被截断时同理：未转义的物理换行不可能合法出现在那个字符串里。
			indented := rawLine[0] == ' ' || rawLine[0] == '\t'
			bareError := strings.HasPrefix(line, "⚠️") ||
				strings.HasPrefix(line, "❌") ||
				strings.HasPrefix(line, "[ERROR]")
			if !indented && bareError && acpErrorHeaderRe.MatchString(line) {
				s.resetEchoJSON()
			} else {
				s.consumeEchoJSON(rawLine)
				continue
			}
		}
		if !(acpErrorHeaderRe.MatchString(line) || acpErrorDetailRe.MatchString(line)) {
			continue
		}
		if acpTerminalErrorRe.MatchString(line) {
			s.terminal = true
		}
		if s.seen[line] {
			continue
		}
		s.seen[line] = true
		s.lines = append(s.lines, line)
		if len(s.lines) > acpMaxErrorLines {
			s.lines = s.lines[len(s.lines)-acpMaxErrorLines:]
		}
	}
	return len(p), nil
}

func (s *acpProviderErrorSniffer) startEchoJSON(payload string) {
	payload = strings.TrimSpace(payload)
	if payload == "" || (payload[0] != '{' && payload[0] != '[') {
		return
	}
	s.echoJSON = true
	s.consumeEchoJSON(payload)
}

// consumeEchoJSON 只跟踪结构性的 JSON 字节。它避免缓存任意的对话/工具载荷，同时仍能
// 认出它们精确的多行边界——包括带引号字符串里的花括号。
func (s *acpProviderErrorSniffer) consumeEchoJSON(fragment string) {
	for i := 0; i < len(fragment); i++ {
		switch {
		case s.echoEscaped:
			s.echoEscaped = false
		case s.echoInString:
			switch fragment[i] {
			case '\\':
				s.echoEscaped = true
			case '"':
				s.echoInString = false
			}
		default:
			switch fragment[i] {
			case '"':
				s.echoInString = true
			case '{', '[':
				s.echoDepth++
			case '}', ']':
				if s.echoDepth > 0 {
					s.echoDepth--
				}
			}
		}
	}
	if s.echoDepth == 0 {
		s.resetEchoJSON()
	}
}

func (s *acpProviderErrorSniffer) resetEchoJSON() {
	s.echoJSON = false
	s.echoDepth = 0
	s.echoInString = false
	s.echoEscaped = false
}

// message 返回适合放进任务错误字段的单行摘要。优先用最具体的 "Error:" / "detail:"
// 片段；回退到捕获到的第一条错误头行；没看到有用内容时返回空。
//
// 注意：非空 message() 可能描述一条 *瞬时* 单次告警——其后跟着一次成功重试。把任务状态
// 翻成 "failed" 的代码必须改用 terminalMessage()——见上方类型文档。
func (s *acpProviderErrorSniffer) message() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.messageLocked()
}

// terminalMessage 返回与 message() 相同的单行摘要，但只在嗅探器至少看到一行命中
// acpTerminalErrorRe 时——即适配器已放弃重试。这是调用方判断要不要把运行从 "completed"
// 提升成 "failed" 该用的信号。捕获的行全是瞬时重试告警时返回空。
func (s *acpProviderErrorSniffer) terminalMessage() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.terminal {
		return ""
	}
	return s.messageLocked()
}

// messageLocked 是 message() 与 terminalMessage() 共享的持锁实现。调用方必须持有 s.mu。
func (s *acpProviderErrorSniffer) messageLocked() string {
	prefix := s.provider + " provider error: "
	for _, line := range s.lines {
		if m := acpErrorDetailRe.FindStringSubmatch(line); m != nil {
			detail := strings.TrimSpace(m[1])
			if detail != "" {
				return acpTruncateError(prefix + detail)
			}
		}
	}
	for _, line := range s.lines {
		if acpErrorHeaderRe.MatchString(line) {
			return acpTruncateError(prefix + line)
		}
	}
	return ""
}

// acpTruncateError 把持久化的供应商错误摘要限制在 acpMaxErrorLineLen 字节内、落在合法
// UTF-8 边界上，并对截断做标记。运行到这一步时归类早已发生；它只限制超长消息存多少，
// 从不决定运行是否失败。
func acpTruncateError(msg string) string {
	if len(msg) <= acpMaxErrorLineLen {
		return msg
	}
	return strings.ToValidUTF8(msg[:acpMaxErrorLineLen], "") + "…(truncated)"
}

// promoteACPResultOnProviderError 在任一条件成立时把 finalStatus 翻成 "failed"：
// (a) stderr 嗅探器捕获到终态失败标记；(b) 适配器往 agent 文本流注入了合成的 "API call
// failed after N retries..." 回合；或 (c) 输出为空且嗅探器捕获到任何东西（没有真实结果
// 可回退，即使只是一连串瞬时告警）。返回更新后的 (status, error)；调用方应以此覆盖自己
// 的局部变量。
//
// 这是 hermes/kimi/kiro 共享的后处理步骤。没有它，对上游 LLM 重试耗尽（HTTP 429、token
// 过期……）的运行会静默报成 "completed"，因为 session/prompt 仍以 stopReason=end_turn
// 收场——见 GitHub multica#1952。
func promoteACPResultOnProviderError(finalStatus, finalError, finalOutput string, sniffer *acpProviderErrorSniffer) (string, string) {
	if finalStatus != "completed" {
		return finalStatus, finalError
	}
	if msg := sniffer.terminalMessage(); msg != "" {
		return "failed", msg
	}
	if acpAgentOutputTerminalRe.MatchString(finalOutput) {
		msg := sniffer.message()
		if msg == "" {
			msg = sniffer.provider + " provider error: " + acpAgentOutputTerminalRe.FindString(finalOutput)
		}
		return "failed", msg
	}
	if finalOutput == "" {
		if msg := sniffer.message(); msg != "" {
			return "failed", msg
		}
	}
	return finalStatus, finalError
}
