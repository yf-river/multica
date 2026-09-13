package daemon

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/multica-ai/multica/server/pkg/agent"
)

// shellResolveTTL 限制“一次登录 shell 的 PATH 解析结果”在多次 probeAgentCLIs 调用
// 之间能被复用的时长。
//
// 它刻意比 agentDiscoveryInterval 长得多，好让高频的发现轮询保持为纯 exec.LookPath
// 扫描：resolveAgentsViaLoginShell 要 fork 用户的登录 shell 并跑他们的 rc 文件，而
// 几乎总会有至少一个“没装、LookPath 必然 miss”的 provider，所以 TTL 太短会让发现变成
// “每几分钟 fork 一次 shell”，在 daemon 的整个生命周期里持续如此。
//
// 实际效果：在 daemon 自身 PATH 上的 CLI 会在 agentDiscoveryInterval 内被发现；只能经
// 登录 shell 触达的（nvm/fnm 垫片、只有 ~/.zshrc 会加进来的 ~/.local/bin）最长等这么
// 久——仍然不用重启，而这正是以前做不到的部分。
var shellResolveTTL = 30 * time.Minute

var (
	shellResolveMu    sync.Mutex
	shellResolveCache map[string]string
	shellResolveKey   string
	shellResolvedAt   time.Time
)

// shellResolveEnvKey 给“决定登录 shell 解析结果的环境”打指纹。其中任何一个变化都会
// 立刻让缓存失效，与 TTL 无关——缓存的答案属于另一套环境。
func shellResolveEnvKey() string {
	return strings.Join([]string{
		os.Getenv("PATH"),
		os.Getenv("SHELL"),
		os.Getenv("HOME"),
	}, "\x00")
}

// cachedShellResolvedAgents 经用户登录 shell 解析所有标准 agent 命令名；只要
// “解析相关环境”没变，就在 shellResolveTTL 内复用上一次的结果。
//
// resolveAgentsViaLoginShell 要 fork 用户的登录 shell 并跑他们的 rc 文件，所以这里
// 必须做成缓存而不是每次探测都调用：probeAgentCLIs 现在会在活着的 daemon 上周期性
// 运行，而几乎总会有至少一个“没装、LookPath 必然 miss”的 provider。
func cachedShellResolvedAgents() map[string]string {
	shellResolveMu.Lock()
	defer shellResolveMu.Unlock()
	key := shellResolveEnvKey()
	if shellResolveCache != nil && shellResolveKey == key && time.Since(shellResolvedAt) < shellResolveTTL {
		return shellResolveCache
	}
	resolved := resolveAgentsViaLoginShell(defaultAgentCommandNames)
	if resolved == nil {
		// 区分“解析出了空”与“从未解析过”，避免一个失败的 shell 在 TTL 窗口内被
		// 每次探测都重新 fork。
		resolved = map[string]string{}
	}
	shellResolveCache = resolved
	shellResolveKey = key
	shellResolvedAt = time.Now()
	return shellResolveCache
}

// probeAgentCLIs 发现这台机器上装了哪些内置 agent CLI，为每个成功解析出的 provider
// 返回一个 AgentEntry。
//
// 这是纯发现：不做版本检测、没有最低版本门槛（每轮注册时 detectBuiltinRuntimes 负责
// 那些）。因此结果就是机器*可用*集合——正是 /health.agents 上报、`multica daemon
// probe-runtimes` 打印的东西。
//
// 启动时由 LoadConfig 调用一次，周期性的 workspace 同步（refreshAgentAvailability）
// 再调用一次——所以用户在 daemon 运行期间装的 CLI 不用重启就能被发现（MUL-5439）。
// 它读的一切都在进程之外（PATH、MULTICA_*_PATH、MULTICA_*_MODEL），所以重跑是观察到
// 这类安装的唯一途径。
//
// 做成 var，测试就能在不装真实 CLI 的前提下打桩发现逻辑。
var probeAgentCLIs = func() map[string]AgentEntry {
	// 探测可用的 agent CLI。exec.LookPath 是主路径，但在 macOS/Linux 上，由 GUI
	// 拉起的 daemon（Electron、Launchpad）不会继承用户交互 shell 的 PATH——
	// fnm/nvm/volta 的多 shell、Anthropic 原生安装器前缀、每用户 npm 前缀都住在只有
	// ~/.zshrc 或 ~/.bashrc 才会加进 PATH 的目录。shellResolvedAgents 会在“首次 miss”
	// 时惰性询问用户登录 shell，把每个标准 agent 名解析成规范绝对路径，从而找到裸
	// daemon 进程看不见的二进制。细节与约束见 resolveAgentsViaLoginShell。
	//
	// 惰性很重要：快乐路径（每个 agent 都在 daemon 的 PATH 上或被显式 MULTICA_*_PATH
	// 钉住）不该付“拉起用户登录 shell”的代价——那会碰用户的 rc 文件，启动延迟随他们
	// 在里头塞的东西线性增长。只有当裸命令名真的 miss 掉 LookPath 时才 fork shell。
	//
	// 解析结果带 TTL 做进程级缓存（而非每次调用），因为本函数现在也在活的 daemon 上
	// 周期性运行：若用每次调用的 sync.Once，每一轮发现都要 fork 一次登录 shell——几乎
	// 总有一个没装的 provider 会 miss。带 TTL 仍能让装进“只经登录 shell 可见的 PATH
	// 目录”（nvm、fnm、经 ~/.zshrc 的 ~/.local/bin）的 CLI 在不用重启的情况下被发现
	// （MUL-5439）。
	getShellResolved := cachedShellResolvedAgents
	probe := func(envVar, defaultCmd, modelEnv string) (AgentEntry, bool) {
		cmd := envOrDefault(envVar, defaultCmd)
		if path, err := resolveAgentExecutablePath(cmd); err == nil {
			return AgentEntry{
				Path:    path,
				Command: cmd,
				Model:   strings.TrimSpace(os.Getenv(modelEnv)),
			}, true
		}
		// shell 兜底只救裸命令名。运维把 MULTICA_*_PATH 钉到某个不存在、却带 / 或 \
		// 的路径上，应该硬性 miss，而不是悄悄换成另一个二进制。
		if strings.ContainsAny(cmd, "/\\") {
			return AgentEntry{}, false
		}
		if path, ok := getShellResolved()[cmd]; ok {
			return AgentEntry{
				Path:    path,
				Command: cmd,
				Model:   strings.TrimSpace(os.Getenv(modelEnv)),
			}, true
		}
		if defaultCmd == "codex" && cmd == defaultCmd {
			// Codex Desktop 把它的 CLI 打包进 macOS app 内部，而不是装到 PATH 上。
			for _, p := range codexDesktopAppBundlePaths() {
				if _, err := os.Stat(p); err == nil {
					return AgentEntry{
						Path:    p,
						Command: cmd,
						Model:   strings.TrimSpace(os.Getenv(modelEnv)),
					}, true
				}
			}
		}
		return AgentEntry{}, false
	}

	agents := map[string]AgentEntry{}
	if e, ok := probe("MULTICA_CLAUDE_PATH", "claude", "MULTICA_CLAUDE_MODEL"); ok {
		agents["claude"] = e
	}
	if e, ok := probe("MULTICA_CODEX_PATH", "codex", "MULTICA_CODEX_MODEL"); ok {
		agents["codex"] = e
	}
	if e, ok := probe("MULTICA_OPENCODE_PATH", "opencode", "MULTICA_OPENCODE_MODEL"); ok {
		agents["opencode"] = e
	}
	if e, ok := probe("MULTICA_CODEARTS_PATH", "codearts", "MULTICA_CODEARTS_MODEL"); ok {
		agents["codearts"] = e
	} else if strings.TrimSpace(os.Getenv("MULTICA_CODEARTS_PATH")) == "" {
		// 原生 CodeArts 安装器可能只为“将来的终端”更新 PATH。GUI 拉起的 daemon 仍可
		// 发现它稳定的用户级启动器。显式但无效的覆盖仍是硬性 miss，绝不会落到这里。
		home, err := os.UserHomeDir()
		if err == nil {
			for _, name := range []string{"codearts.cmd", "codearts"} {
				candidate := filepath.Join(home, ".codeartsdoer", "installers", name)
				path, resolveErr := resolveAgentExecutablePath(candidate)
				if resolveErr != nil {
					continue
				}
				agents["codearts"] = AgentEntry{
					Path:    path,
					Command: "codearts",
					Model:   strings.TrimSpace(os.Getenv("MULTICA_CODEARTS_MODEL")),
				}
				break
			}
		}
	}
	if e, ok := probe("MULTICA_DEVECO_PATH", "deveco", "MULTICA_DEVECO_MODEL"); ok {
		agents["deveco"] = e
	}
	if e, ok := probe("MULTICA_HERMES_PATH", "hermes", "MULTICA_HERMES_MODEL"); ok {
		agents["hermes"] = e
	}
	if e, ok := probe("MULTICA_PI_PATH", "pi", "MULTICA_PI_MODEL"); ok {
		agents["pi"] = e
	}
	// 内置 runtime 身份（如 omp）来自 server/pkg/agent/builtin_runtimes.go 的描述符
	// 注册表。每个都独立探测各自的 CLI，这样同时装了 pi 和 omp 的主机会得到两个
	// runtime。环境变量前缀与默认命令来自描述符——新增一个分支是“加描述符条目”，不是
	// “改探测代码”。
	for _, desc := range agent.BuiltinRuntimes {
		pathEnv := desc.EnvPrefix + "_PATH"
		modelEnv := desc.EnvPrefix + "_MODEL"
		if e, ok := probe(pathEnv, desc.DefaultCommand, modelEnv); ok {
			agents[desc.ID] = e
		}
	}
	if e, ok := probe("MULTICA_CURSOR_PATH", "cursor-agent", "MULTICA_CURSOR_MODEL"); ok {
		agents["cursor"] = e
	}
	if e, ok := probe("MULTICA_COPILOT_PATH", "copilot", "MULTICA_COPILOT_MODEL"); ok {
		agents["copilot"] = e
	}
	if e, ok := probe("MULTICA_KIMI_PATH", "kimi", "MULTICA_KIMI_MODEL"); ok {
		agents["kimi"] = e
	}
	if e, ok := probe("MULTICA_REASONIX_PATH", "reasonix", "MULTICA_REASONIX_MODEL"); ok {
		agents["reasonix"] = e
	}
	// DSH 只在装了它的 Multica runtime profile 时才注册。裸 dsh 二进制不够：没有那套
	// bundle 就没有 --stdio 协议，每个任务都会在“被宣传成健康”之后失败。
	if e, ok := probe("MULTICA_DSH_PATH", "dsh", "MULTICA_DSH_MODEL"); ok && probeDshMulticaProfile(e.Path) {
		agents["dsh"] = e
	}
	if e, ok := probe("MULTICA_KIRO_PATH", "kiro-cli", "MULTICA_KIRO_MODEL"); ok {
		agents["kiro"] = e
	}
	if e, ok := probe("MULTICA_CODEBUDDY_PATH", "codebuddy", "MULTICA_CODEBUDDY_MODEL"); ok {
		agents["codebuddy"] = e
	}
	// agy 1.0.6 增加了 `--model` 参数（MUL-3125），Antigravity 于是和其它后端一样接收
	// model 环境变量。MULTICA_ANTIGRAVITY_MODEL 作为 daemon 级默认值；它的值必须是
	// `agy models` 的显示串（例如 "Claude Opus 4.6 (Thinking)"），不是 provider/model
	// 的 slug。
	if e, ok := probe("MULTICA_ANTIGRAVITY_PATH", "agy", "MULTICA_ANTIGRAVITY_MODEL"); ok {
		agents["antigravity"] = e
	}
	// Qoder CLI 以 `qodercli` 二进制发布（Qoder Desktop 不把它放 PATH；用户单独装，
	// 常走 npm 全局前缀）。它和其它 provider 一样必须走 probe()，让登录 shell 兜底生
	// 效：GUI/Launchpad 拉起的 daemon 不继承交互 shell 的 PATH，没有兜底时一个好好的
	// qodercli 安装会跨重启一直不可见（MUL-5524）。
	if e, ok := probe("MULTICA_QODER_PATH", "qodercli", "MULTICA_QODER_MODEL"); ok {
		agents["qoder"] = e
	}
	// Qoder CN CLI 用独立的 `qoderclicn` 二进制与账号/配置根，暴露与 Qoder CLI 相同的
	// ACP 传输。把它注册成独立 provider，让装了任一或双版本的主机无需自定义 profile
	// 也能得到匹配的 runtime。
	if e, ok := probe("MULTICA_QODERCLICN_PATH", "qoderclicn", "MULTICA_QODERCLICN_MODEL"); ok {
		agents["qoderclicn"] = e
	}
	// 字节跳动官方 TRAE CLI（https://docs.trae.cn/cli 的 `traecli` 二进制），经
	// `traecli acp serve --yolo` 走 ACP 驱动。MULTICA_TRAECLI_MODEL 作为 daemon 级
	// 默认模型（用户已登录 traecli 目录里的模型 id）。
	if e, ok := probe("MULTICA_TRAECLI_PATH", "traecli", "MULTICA_TRAECLI_MODEL"); ok {
		agents["traecli"] = e
	}
	// xAI Grok Build CLI（`grok`），经 `grok agent --always-approve stdio` 走 ACP
	// 驱动。MULTICA_GROK_MODEL 作为 daemon 级默认（例如 grok-4.5）。
	if e, ok := probe("MULTICA_GROK_PATH", "grok", "MULTICA_GROK_MODEL"); ok {
		agents["grok"] = e
	}
	// Qwen Code（`qwen`）用 -p 与 stream-json 无头运行。它原生的 QWEN.md 与
	// .qwen/skills 任务上下文由 execenv 准备。
	if e, ok := probe("MULTICA_QWEN_PATH", "qwen", "MULTICA_QWEN_MODEL"); ok {
		agents["qwen"] = e
	}
	// QwenPaw（`qwenpaw`）是 QwenPaw CLI agent，经 `qwenpaw acp` 走 ACP 驱动。它不接
	// 收 model 环境变量：后端从不调用 session/set_model（那会改写 QwenPaw 的共享 agent
	// 配置），所以 ExecOptions.Model 被忽略——见 ModelSelectionSupported。在这里读一个
	// 只会宣传一个悄悄什么都不干的旋钮。
	if e, ok := probe("MULTICA_QWENPAW_PATH", "qwenpaw", ""); ok {
		agents["qwenpaw"] = e
	}
	// Dim（`dim`）是 DimCode CLI agent，经 `dim acp` 走 ACP 驱动。
	// MULTICA_DIM_MODEL 作为 daemon 级默认（用户已登录 dim 目录里的模型 id）。
	if e, ok := probe("MULTICA_DIM_PATH", "dim", "MULTICA_DIM_MODEL"); ok {
		agents["dim"] = e
	}
	// MiniMax Code（`mcode`）经 `mcode acp` 暴露 ACP v1 server。模型选择归 MCode
	// runtime 管，所以没有 model 环境变量。
	if e, ok := probe("MULTICA_MCODE_PATH", "mcode", ""); ok {
		agents["mcode"] = e
	}
	// ZeroClaw（`zeroclaw`）是基于 Rust 的通用 agent CLI，经 `zeroclaw acp` 走 ACP
	// 驱动。它不接收 model 环境变量：它的 ACP server 没有 `session/set_model`，也没有
	// 任何 handler 读 model 参数，模型来自 ZeroClaw 自己的 agent profile，
	// ExecOptions.Model 永远无法生效——见 ModelSelectionSupported。在这里读一个只会
	// 宣传一个悄悄什么都不干的旋钮。
	if e, ok := probe("MULTICA_ZEROCLAW_PATH", "zeroclaw", ""); ok {
		agents["zeroclaw"] = e
	}
	return agents
}

// probeDshMulticaProfile 探测 dsh 二进制是否装了 Multica profile（--profile multica
// --probe）：只有装了才有 --stdio 协议可用，广告成一个健康 runtime 才有意义。
func probeDshMulticaProfile(executablePath string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executablePath, "--profile", "multica", "--probe")
	cmd.WaitDelay = time.Second
	output, err := cmd.Output()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(output), "\n") {
		var frame struct {
			Version         int    `json:"v"`
			Type            string `json:"type"`
			Runtime         string `json:"runtime"`
			ProtocolVersion int    `json:"protocol_version"`
		}
		if json.Unmarshal([]byte(line), &frame) == nil && frame.Version == 1 && frame.Type == "probe" && frame.Runtime == "dsh" && frame.ProtocolVersion == 1 {
			return true
		}
	}
	return false
}
