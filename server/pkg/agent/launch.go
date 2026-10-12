package agent

// 文件职责：本文件是全包的「进程启动边界」——所有 runtime CLI（codebuddy、codex、
// claude、hermes 等各家 agent 的命令行程序）在本文件的唯一入口处被构造成 *exec.Cmd。
// 它统一解决三个横切问题：
//
//  1. 启动前缀（launch prefix）：自定义 runtime（MUL-3284）以 `command_name` + 固定
//     fixed_args 描述，其中某些 token 属于「程序自身」而非某次调用（例如包装器
//     `ccms start q36` 必须先让 `start q36` 被解析掉、才会把后续协议参数交给真正的
//     Claude 二进制，GH #7046）。Command 类型把「可执行文件 + 该前缀」固化成一体。
//  2. 进程生命周期：所有被拉起的进程默认进独立进程组、取消即整组 SIGKILL
//     （newRuntimeCmd），避免 CLI 的子进程（MCP 服务、shell 等）成为逃逸的孤儿
//     （GH #7522、#5918）；探测类调用（如 --version）通过 runOwned/outputOwned/
//     combinedOutputOwned 获得与正式任务同样的树所有权与有界等待。
//  3. 日志脱敏：logAgentCommand 是唯一允许记录进程参数的边界，只保留语法上合理的
//     flag 名与「有来源证明」的适配层子命令，一切值一律替换成 <redacted>。
//
// 另外本文件还负责按 agent 类型过滤启动前缀中的协议关键 flag（filterLaunchPrefix），
// 并缓存模型发现 key（cacheKey）。与 agent.go 等业务编排文件不同，这里不面向任何单一
// CLI 的解析细节，而是给它们共用的地基。

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// redactedAgentCommandArg 是命令日志里用来替换敏感参数值的占位符。
const redactedAgentCommandArg = "<redacted>"

// maxLoggedAgentCommandFlagLen 上限：记录到日志里的长 flag 名最多这么长，防止把一长串
// 内联秘密（例如 `--token=<超长值>` 的前半）误当成 flag 名整体记下来。
const maxLoggedAgentCommandFlagLen = 64

// agentCommandLogArgs 描述「由适配层拼好的 argv」在提交给启动边界时的样子。
// trustedPositionals 是 invocationArgs 中属于适配层自有字面子命令的索引集合；除此之外
// 的每个位置参数一律脱敏。用「索引 + 字面值」的断言而不是全局字符串白名单，是为了让
// 信任跟着参数来源走——同一个 token 若是经 custom_args 进来，仍会被当作敏感信息。
type agentCommandLogArgs struct {
	invocationArgs     []string
	trustedPositionals []trustedAgentCommandPositional
}

// trustedAgentCommandPositional 标记 invocationArgs 中第 index 个参数的字面值确为 value，
// 即它是适配层写死的子命令、可以放心记日志。
type trustedAgentCommandPositional struct {
	index int
	value string
}

// trustAgentCommandPositional 构造一条受信任位置参数断言。
func trustAgentCommandPositional(index int, value string) trustedAgentCommandPositional {
	return trustedAgentCommandPositional{index: index, value: value}
}

// newAgentCommandLogArgs 组装一次调用要记的 argv 快照及其中受信任的子命令位置。
func newAgentCommandLogArgs(invocationArgs []string, trustedPositionals ...trustedAgentCommandPositional) agentCommandLogArgs {
	return agentCommandLogArgs{
		invocationArgs:     invocationArgs,
		trustedPositionals: append([]trustedAgentCommandPositional(nil), trustedPositionals...),
	}
}

// Command 是一次 runtime CLI 的身份：Multica 要拉起的可执行文件，加上「属于命令自身而
// 不属于任何单次调用」的 argv 前缀。
//
// 自定义 runtime 配置（MUL-3284）由 `command_name` + `fixed_args` 组成；对 `ccms start
// q36` 这类包装器而言，这两个 fixed_args token 是「这个程序是什么」的一部分，而不是
// 传给它的选项。包装器只有在它的 `start` 子命令被选中之后才会到达真正的 Claude 二进制，
// 所以在前缀被消费之前，`-p` 及 stream-json 协议族其余参数毫无意义——Commander 风格
// 的解析器会直接拒绝它们（GH #7046）。
//
// 因此前缀紧跟在可执行文件之后、位于 backend 拼出的任何参数之前：
//
//	<Path> <Prefix...> <protocol args...> <ExtraArgs...> <CustomArgs...>
//
// 子命令式前缀只有在这个位置才成立。flag 式前缀通常放在哪都一样——`agent --model
// composer-2.5 -p …` 与 `agent -p … --model composer-2.5` 对多数解析器等价——所以「前缀
// 在前」是两种顺序里更通用的一种，但并非对所有人都安全：一个区分「全局 flag」与「子命令
// flag」的 CLI 仍可能在意 flag 落在哪里。正是这个残余风险，使该位置变化要在 runtime
// 文档里显式说明，而不是当作无感变更。
//
// 零值 Command 表示「裸可执行文件、无前缀」，这正是所有内置 runtime 使用的形态。
type Command struct {
	// Path 是要拉起的可执行文件——PATH 查找名或绝对路径。
	Path string
	// Prefix 是命令自带的 argv 前缀，已由 FilterLaunchPrefix 过滤过。切勿就地修改某个
	// Command 的 Prefix；Argv 会精确复制它，让调用方无法取别名。
	Prefix []string
	// logger 在构造进程时上报前缀/参数冲突。可选：零值 Command 不打任何日志。
	logger *slog.Logger
}

// NewCommand 由「已解析的可执行文件路径 + 启动前缀」构造一个 Command。包内调用方通常
// 走 Config.commandAt 来继承 daemon 为 runtime 配置的前缀。
//
// 前缀应当已通过 FilterLaunchPrefix；只有在没有协议通道需要保护的地方——比如一次性
// `--version` 探测——才允许直接传入原始 fixed_args 列表。
func NewCommand(path string, prefix []string) Command {
	return Command{Path: path, Prefix: append([]string(nil), prefix...)}
}

// Argv 返回一次调用的完整参数向量：命令自带前缀再接上 args。结果不会与任一入参共享
// 底层数组，调用方可以放心往里追加。
func (c Command) Argv(args ...string) []string {
	argv := make([]string, 0, len(c.Prefix)+len(args))
	argv = append(argv, c.Prefix...)
	argv = append(argv, args...)
	return argv
}

// exec 构造用 args 运行本命令的 *exec.Cmd。
//
// 这是包内唯一构造 runtime CLI 进程的地方。daemon 启动或探测的一切——任务执行、
// `--version` 探测、模型发现——都经由这里或 execVia，因此新 backend 不可能忘记启动前缀
// 而静默复现 GH #7046。TestOnlyLaunchGoSpawnsRuntimeProcesses 强制保障这一点。
func (c Command) exec(ctx context.Context, args ...string) *exec.Cmd {
	warnLaunchPrefixOverlap(c.Prefix, args, c.logger)
	return newRuntimeCmd(exec.CommandContext(ctx, c.Path, c.Argv(args...)...))
}

// newRuntimeCmd 给本包每个 runtime 进程套上「进程生命周期」的默认值。它在构造时就生效，
// 因为两项设置都必须在进程存在之前就位。
//
// 这两项曾经都是可选的，而「可选」正是 GH #7522 的根源：本包启动进程的 27 个地方里，
// 只有 8 个主动要了进程组，其余把自己的 CLI 留在 daemon 的进程组里，组级信号够不着它。
// os/exec 默认的 Cancel 是同一漏洞的另一条路：它只杀组长，于是被取消任务的工具子进程
// ——MCP 服务器、shell，agent 拉起的任何东西——都活了下来。在 Linux 上是 #5918；在
// Windows 上组长常常是个 cmd.exe 垫片、真正的 CLI 已经是孙进程，一个被取消的 agent
// 能继续跑 40 分钟。
//
// 想要优雅停机而非立即 kill 的 backend 可在构造后自行覆盖 cmd.Cancel 并获胜：claude、
// dsh、deveco 就是自己驱动 SIGTERM → 宽限期 → SIGKILL。
func newRuntimeCmd(cmd *exec.Cmd) *exec.Cmd {
	configureProcessGroup(cmd)
	cmd.Cancel = func() error {
		signalProcessGroup(cmd, syscall.SIGKILL)
		return nil
	}
	return cmd
}

// runOwned 是在「受管进程树」之上的 Run()：启动、等待、放弃所有权。
//
// os/exec 的 Run/Output/CombinedOutput 会自己调 Start，所以用它们写的探测永远到不了
// startOwnedProcessTree，在 Windows 上一无所属——那里 `--version` 探测的直接子进程通常
// 是垫片而非 CLI。这正是 GH #7522 当初被报告的漏洞形态，只是换了一条没人盯的路径：
// detectCLIVersion 自己的注释已经描述过「坏 CLI 留下孙进程」。这三个辅助函数让同步探测
// 获得与任务启动相同的所有权。
func runOwned(cmd *exec.Cmd, logger *slog.Logger) error {
	// 不设上界的话，它会永远等自己的清理、永不返回：继承了输出管道的后代进程在组长退出
	// 后仍握着管道，cmd.Wait 会一直等到拷贝 goroutine 看到 EOF；而能关掉这些管道的，正
	// 是 Wait 之后才跑的 release。WaitDelay 天生就是为这个场景定义的——「子进程退出了却
	// 不关 I/O 管道」——取消也帮不上忙，因为 checkforking CLIVersion 这类探测跑在调用方的
	// context 上，任何任务超时都还没建立。
	//
	// 调用方自己设了上界就保留；detectCLIVersion 自 MUL-3812 起就为这个形状设了。
	if cmd.WaitDelay == 0 {
		cmd.WaitDelay = probeWaitDelay
	}
	if err := startOwnedProcessTree(cmd, logger); err != nil {
		return err
	}
	err := cmd.Wait()
	// 探针在组长退出的瞬间就该结束：它拉起的任何东西都不应比答案活得更久。在 release
	// 之前先发信号覆盖 Unix（那里 release 进程组是空操作）；在 Windows 上关闭 Job Object
	// 本身就会带走整棵树。
	signalProcessGroup(cmd, syscall.SIGKILL)
	releaseProcessGroup(cmd)
	return err
}

// probeWaitDelay 限制一个已结束的探测在等待「后代留下的输出管道」上花多久。它与
// detectCLIVersion 手工设定的上界一致。计时只在子进程已退出或 context 结束后才开始，
// 所以健康的探测永远付不出这个代价。
const probeWaitDelay = 2 * time.Second

// outputOwned 是受管进程树之上的 cmd.Output()。它遵循标准库契约——成功返回 stdout，
// 失败运行的 stderr 挂到 *exec.ExitError 上——唯一的差别是 stderr 样本取尾部
// probeStderrSampleBytes，而不是标准库的「头尾各留一段」；CLI 真正的失败信息通常就在
// 尾部。
func outputOwned(cmd *exec.Cmd, logger *slog.Logger) ([]byte, error) {
	if cmd.Stdout != nil {
		return nil, errors.New("exec: Stdout already set")
	}
	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	// 调用方自己设的 Stderr 就尊重它；只有没人旁观时才填样本缓冲，与 Output() 的行为
	// 一致。
	var stderr *tailBuffer
	if cmd.Stderr == nil {
		stderr = &tailBuffer{max: probeStderrSampleBytes}
		cmd.Stderr = stderr
	}

	err := runOwned(cmd, logger)
	var exitErr *exec.ExitError
	if stderr != nil && errors.As(err, &exitErr) {
		exitErr.Stderr = stderr.Bytes()
	}
	return stdout.Bytes(), err
}

// combinedOutputOwned 是受管进程树之上的 cmd.CombinedOutput()。Stdout 与 Stderr 用同一个
// writer 值，这正是 os/exec 给它们一根管道、从而有一种交错顺序的原因，与标准库一致。
func combinedOutputOwned(cmd *exec.Cmd, logger *slog.Logger) ([]byte, error) {
	if cmd.Stdout != nil {
		return nil, errors.New("exec: Stdout already set")
	}
	if cmd.Stderr != nil {
		return nil, errors.New("exec: Stderr already set")
	}
	var combined bytes.Buffer
	cmd.Stdout = &combined
	cmd.Stderr = &combined
	err := runOwned(cmd, logger)
	return combined.Bytes(), err
}

// probeStderrSampleBytes 限制失败探测的错误里保留多少 stderr。os/exec 把同样的样本限制
// 在 32 KiB；一个陷在日志循环里的 CLI 不应能借一次 `--version` 调用撑大 daemon 堆。
const probeStderrSampleBytes = 32 << 10

// tailBuffer 只保留写进来的最后 max 字节、丢弃其余。它总是回报「全部写成功」，因此子
// 进程绝不会被这个上限挡住或截短。
type tailBuffer struct {
	buf []byte
	max int
}

// Write 追加数据并裁掉超出 max 的旧头部，永远向调用方报告完整写入。
func (t *tailBuffer) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

// Bytes 返回当前保留下来的尾部内容。
func (t *tailBuffer) Bytes() []byte { return t.buf }

// invocationChooser 是「按工具定制的平台启动改写」的函数形状（chooseCursorInvocation、
// chooseCopilotInvocation、choosePiInvocation）。在 Windows 上它们用 PowerShell 宿主或
// 随附的原生二进制替换 argv[0]，并包装 CLI 自己的 argv；其它平台原样透传。
type invocationChooser func(execName, lookedUp string, args []string, logger *slog.Logger) (string, []string)

// execVia 为「启动要走平台调用选择器」的 runtime CLI 构造 *exec.Cmd。
//
// 前缀先应用到 CLI 自己的 argv 上、再交给选择器运行，绝不作用到最终 argv[0]：Windows
// 上选择器可能前置自己的 token（`-NoProfile -ExecutionPolicy Bypass -File
// cursor-agent.ps1`），那些必须待在最前。`ccms start q36` 要落在 PowerShell 前导之后、
// 落在被包装的 CLI 上，而不是排在 PowerShell 自己的 flag 之前。
func (c Command) execVia(ctx context.Context, choose invocationChooser, lookedUp string, args []string, logger *slog.Logger) (*exec.Cmd, string, []string) {
	warnLaunchPrefixOverlap(c.Prefix, args, logger)
	argv0, cmdArgs := choose(c.Path, lookedUp, c.Argv(args...), logger)
	return newRuntimeCmd(exec.CommandContext(ctx, argv0, cmdArgs...)), argv0, cmdArgs
}

// withFilteredPrefix 返回一个前缀已经过 fn 处理的新 Command 副本。
//
// 把前缀挪到最前，会让它丢掉常规「后者胜出」的 flag 冲突——这正是目的。但有少数设置不
// 由 argv 顺序决定：Codex 的 `-c key=value` 从任何位置都能压过 daemon 写下的 config.toml，
// 所以 fixed_args 里的 `-c mcp_servers.…` 无论放前放后都会遮蔽托管 MCP。这类设置需要和
// ExtraArgs / CustomArgs 一样被移除，而只有 backend 自己知道是哪些。
func (c Command) withFilteredPrefix(fn func([]string) []string) Command {
	c.Prefix = fn(c.Prefix)
	return c
}

// cacheKey 标识命令，用于模型发现备忘录的键。前缀是键的一部分，因为两个 profile 可以共
// 享同一个二进制却枚举出不同的目录——`ccms start q36` 与 `ccms start opus` 是同一可执行
// 文件、背后是不同的模型——只按路径作键会把第一个的目录服务给第二个（见 discoveryCacheKey）。
func (c Command) cacheKey() string {
	if len(c.Prefix) == 0 {
		return c.Path
	}
	return c.Path + "\x00" + strings.Join(c.Prefix, "\x00")
}

// String 把命令渲染成用户在 Runtimes UI 里写下的样子，用于日志与错误消息。
func (c Command) String() string {
	if len(c.Prefix) == 0 {
		return c.Path
	}
	return c.Path + " " + strings.Join(c.Prefix, " ")
}

// commandAt 把一个已解析的可执行文件路径与当前 runtime 的启动前缀配成 Command。
// Backend 在自己完成 PATH 解析与默认二进制回退之后再调用它——这正是路径要作为参数而
// 不是读 Config.ExecutablePath 的原因。
func (c Config) commandAt(path string) Command {
	return Command{Path: path, Prefix: c.LaunchPrefix, logger: c.Logger}
}

// logAgentCommand 是唯一允许记录 runtime 进程参数的边界。它从最终的 exec.Cmd 出发，使
// 启动前缀与平台改写都被如实呈现，但绝不记录参数值：flag 名与适配层自有的字面子命令对
// 诊断仍有价值会保留，内联值被剥掉，其余每个位置/值 token 一律替换成占位标记。
func (c Config) logAgentCommand(cmd *exec.Cmd, source agentCommandLogArgs) {
	c.logAgentCommandFields(cmd, source, 0, false)
}

// logAgentCommandWithPrompt 只在安全命令日志里加一个「带类型的 prompt 字节数」。它刻意
// 与 logAgentCommand 分开，避免开一个无界的可选字段通道、意外把 prompt 内容带进日志。
func (c Config) logAgentCommandWithPrompt(cmd *exec.Cmd, source agentCommandLogArgs, promptBytes int) {
	c.logAgentCommandFields(cmd, source, promptBytes, true)
}

func (c Config) logAgentCommandFields(cmd *exec.Cmd, source agentCommandLogArgs, promptBytes int, includePromptBytes bool) {
	if cmd == nil {
		return
	}
	logger := c.Logger
	if logger == nil {
		logger = slog.Default()
	}

	args := []string{}
	if len(cmd.Args) > 1 {
		args = cmd.Args[1:]
	}
	trustedPositionals := c.trustedAgentCommandPositionals(args, source)
	fields := []any{
		"provider", c.provider,
		"exec", cmd.Path,
		"args", redactAgentCommandArgs(args, trustedPositionals),
		"arg_count", len(args),
	}
	if includePromptBytes {
		fields = append(fields, "prompt_bytes", promptBytes)
	}
	logger.Info("agent command", fields...)
}

// trustedAgentCommandPositionals 把 source 里的索引映射到最终 exec.Cmd 的 argv 上。平台
// 启动改写可能前置 PowerShell 宿主与包装参数，所以「原始启动前缀 + 适配层 argv」必须与
// 最终后缀完全吻合，任何一个位置参数才配得上受信任。不匹配则一律失败关闭。
func (c Config) trustedAgentCommandPositionals(finalArgs []string, source agentCommandLogArgs) map[int]struct{} {
	originalLen := len(c.LaunchPrefix) + len(source.invocationArgs)
	start := len(finalArgs) - originalLen
	if start < 0 {
		return nil
	}
	for i, arg := range c.LaunchPrefix {
		if finalArgs[start+i] != arg {
			return nil
		}
	}
	invocationStart := start + len(c.LaunchPrefix)
	for i, arg := range source.invocationArgs {
		if finalArgs[invocationStart+i] != arg {
			return nil
		}
	}

	trusted := make(map[int]struct{}, len(source.trustedPositionals))
	for _, positional := range source.trustedPositionals {
		if positional.index < 0 || positional.index >= len(source.invocationArgs) ||
			source.invocationArgs[positional.index] != positional.value {
			continue
		}
		trusted[invocationStart+positional.index] = struct{}{}
	}
	return trusted
}

// redactAgentCommandArgs 只保留「语法上说得通的 flag 名」与「有来源证明的适配层子命令」，
// 剥掉一切值。单横线 flag 必须是单个 ASCII 字母——这防止把
// "-sTk9xQZ-secretvalue" 之类的 token 误当 flag。长 flag 有长度上限并剥掉内联值。
// 其余所有 token 变成 <redacted>。
func redactAgentCommandArgs(args []string, trustedPositionals map[int]struct{}) []string {
	redacted := make([]string, len(args))
	for i, arg := range args {
		if _, ok := trustedPositionals[i]; ok {
			redacted[i] = arg
			continue
		}
		if flag, ok := safeAgentCommandFlagName(arg); ok {
			redacted[i] = flag
			continue
		}
		redacted[i] = redactedAgentCommandArg
	}
	return redacted
}

// safeAgentCommandFlagName 判断 arg 是否值得保留成 flag 名：单横线 + 单个 ASCII 字母，
// 或长度受限、由字母开头、仅含字母数字._- 的双横线长 flag；否则拒绝并去掉内联的 `=值`。
func safeAgentCommandFlagName(arg string) (string, bool) {
	flag := arg
	if equals := strings.IndexByte(flag, '='); equals > 0 {
		flag = flag[:equals]
	}
	if len(flag) == 2 && flag[0] == '-' && isASCIIAlpha(flag[1]) {
		return flag, true
	}
	if len(flag) < 3 || len(flag) > maxLoggedAgentCommandFlagLen || !strings.HasPrefix(flag, "--") || !isASCIIAlpha(flag[2]) {
		return "", false
	}
	for i := 3; i < len(flag); i++ {
		ch := flag[i]
		if !isASCIIAlpha(ch) && (ch < '0' || ch > '9') && ch != '.' && ch != '_' && ch != '-' {
			return "", false
		}
	}
	return flag, true
}

// isASCIIAlpha 判断字节是否为 ASCII 大小写字母。
func isASCIIAlpha(ch byte) bool {
	return ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z'
}

// launchPrefixBlockedArgs 把「协议族」映射到「启动前缀不许设置的 flag」。它的存在是为了
// 让 New 在唯一知道协议族的那一点上过滤一次前缀，而不是让每个 backend 各自记住。
//
// 只有真的会拒绝 flag 的协议族出现在这里；backend 没有拦截参数策略的协议族接受任何前缀。
var launchPrefixBlockedArgs = map[string]map[string]blockedArgMode{
	"antigravity": antigravityBlockedArgs,
	"claude":      claudeBlockedArgs,
	"codebuddy":   codebuddyBlockedArgs,
	"codearts":    codeartsBlockedArgs,
	"codex":       codexBlockedArgs,
	"copilot":     copilotBlockedArgs,
	"cursor":      cursorBlockedArgs,
	"deveco":      devecoBlockedArgs,
	"grok":        grokBlockedArgs,
	"hermes":      hermesBlockedArgs,
	"kimi":        kimiBlockedArgs,
	"kiro":        kiroBlockedArgs,
	"opencode":    opencodeBlockedArgs,
	"pi":          piBlockedArgs,
	"qoder":       qoderBlockedArgs,
	"qoderclicn":  qoderBlockedArgs,
	"qwen":        qwenBlockedArgs,
	"qwenpaw":     qwenpawBlockedArgs,
	"reasonix":    reasonixBlockedArgs,
	"traecli":     traecliBlockedArgs,
	"dim":         dimBlockedArgs,
	"zeroclaw":    zeroclawBlockedArgs,
}

// FilterLaunchPrefix 是给包外调用方（daemon）用的导出形式——daemon 会为那些从不经过 New
// 的 CLI 探测构造 Command。没有它，一次 `--version` 探测与一次任务启动对「runtime 前缀是
// 什么」就会各说各话。
func FilterLaunchPrefix(agentType string, prefix []string, logger *slog.Logger) []string {
	return filterLaunchPrefix(prefix, agentType, logger)
}

// filterLaunchPrefix 从启动前缀里去掉协议关键 flag，同时放行位置参数。
//
// 两种 token 回答的是两个不同问题。位置 token 是命令的身份——`start`、`q36`——daemon
// 对它没有意见。flag token 则与 daemon 拥有的协议参数竞争，而前缀位置会让它赢：
// 把 `--output-format text` 放进 fixed_args 会破坏 daemon↔CLI 的 stream-json 通道，
// 与把它放进 custom_args（那已经被拒绝）效果相同。
//
// 这里刻意不委托给 filterCustomArgs，虽然两者共享黑名单。那些 map 也带位置 token——
// Hermes 与 QwenPaw 拦 `acp`，TRAE 拦 `acp` 和 `serve`，Grok 拦 `agent`、`stdio`、
// `serve`——因为在 custom_args 里裸 `acp` 确实是在重发 backend 自己的子命令；而在启动
// 前缀里同一个 token 是命令身份的一部分：`hermes-wrapper acp tenant` 命名了一个程序，
// 从中去掉 `acp` 等于静默地把命令改写成操作员从未配置过的东西。这里只有以 `-` 开头的
// token 才有资格进黑名单。
//
// 被拦的 flag 仍然会带走它独立的值 token（无论是不是位置 token），所以
// `--permission-mode ask` 不会留下一个孤零零的 `ask` 被读成子命令。
func filterLaunchPrefix(prefix []string, agentType string, logger *slog.Logger) []string {
	if len(prefix) == 0 {
		return nil
	}
	blocked, ok := launchPrefixBlockedArgs[agentType]
	if !ok {
		return append([]string(nil), prefix...)
	}
	if logger == nil {
		logger = slog.Default()
	}
	filtered := make([]string, 0, len(prefix))
	for i := 0; i < len(prefix); i++ {
		arg := unshellQuoteArg(prefix[i])
		flag, isFlag := launchPrefixFlagName(arg)
		if !isFlag {
			// 命令自身的身份。即使与某个 backend 子命令名撞了也绝不过滤。
			filtered = append(filtered, arg)
			continue
		}
		mode, isBlocked := blocked[flag]
		if !isBlocked {
			filtered = append(filtered, arg)
			continue
		}
		logger.Warn("custom runtime fixed_args: blocked protocol-critical flag, skipping", "flag", flag)
		hasInlineValue := strings.Contains(arg, "=")
		if mode == blockedWithValue && !hasInlineValue {
			i++
		} else if mode == blockedOptionalValue && !hasInlineValue && i+1 < len(prefix) &&
			!strings.HasPrefix(unshellQuoteArg(prefix[i+1]), "-") {
			i++
		}
	}
	return filtered
}

// warnLaunchPrefixOverlap 记录启动前缀与「daemon 为本次运行拼的参数」重叠的 flag。
//
// 重叠不算错误：前缀排在前，daemon 自己的值在后，按所有受支持 CLI 都用的「后者胜出」
// 解析它赢。这正是想要的优先级——显式选择的按 agent 模型应该压过运行时全局默认——但在
// GH #7046 之前前缀排在最后并悄悄获胜：被钉死 `--model composer-2.5` 的 runtime 会覆盖
// 成员在 UI 里选的模型。过去依赖那行为的人现在能在日志里看到指名道姓的一行，而不是一次
// 静默变更。
func warnLaunchPrefixOverlap(prefix, args []string, logger *slog.Logger) {
	if len(prefix) == 0 || len(args) == 0 || logger == nil {
		return
	}
	later := make(map[string]bool, len(args))
	for _, a := range args {
		if flag, ok := launchPrefixFlagName(a); ok {
			later[flag] = true
		}
	}
	for _, p := range prefix {
		flag, ok := launchPrefixFlagName(p)
		if !ok || !later[flag] {
			continue
		}
		logger.Warn("custom runtime fixed_args set a flag the runtime also sets; the runtime's value wins",
			"flag", flag)
	}
}

// launchPrefixFlagName 从一个 argv token 里抽出 flag 名，对位置参数返回 false。
// `--model=o3` 与 `--model o3` 都得到 "--model"，两种写法因此比较相等。
func launchPrefixFlagName(arg string) (string, bool) {
	arg = unshellQuoteArg(arg)
	if !strings.HasPrefix(arg, "-") || arg == "-" || arg == "--" {
		return "", false
	}
	if idx := strings.Index(arg, "="); idx > 0 {
		return arg[:idx], true
	}
	return arg, true
}
