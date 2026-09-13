package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/mattn/go-shellwords"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/pkg/agent"
)

const (
	DefaultServerURL         = "ws://localhost:8080/ws"
	DefaultPollInterval      = 30 * time.Second
	DefaultHeartbeatInterval = 15 * time.Second
	// DefaultAgentTimeout 是单次 agent 运行的可选绝对墙钟上限。0 = 不设上限：一次运行
	// 只受不活跃看门狗（DefaultAgentIdleWatchdog）约束，因此“一直发事件”的会话绝不会
	// 仅仅因为跑得久就被杀（MUL-3064）。想要成本/资源硬上限的运维可设
	// MULTICA_AGENT_TIMEOUT。
	DefaultAgentTimeout                   = 0
	DefaultCodexSemanticInactivityTimeout = 10 * time.Minute
	DefaultCodexHandshakeTimeout          = 30 * time.Second
	DefaultCodexThreadHandshakeTimeout    = 60 * time.Second
	// DefaultOpenCodeIdleWatchdog 缩短 OpenCode 运行在“未执行工具”时的无消息预算。
	// OpenCode 增量地流式输出文本与工具事件，因此这里一段完全静默的间隔同时覆盖“首个
	// 模型 token 缺失”与“响应流卡住”。通用 AgentIdleWatchdog 仍是总开关。
	DefaultOpenCodeIdleWatchdog = 10 * time.Minute
	// DefaultAgentIdleWatchdog 是每任务安全网：后端在这么长时间内没有任何消息、且消息
	// 队列为空时强制停掉一次运行。Claude Code 这类后端可能无限期卡在卡死的子进程上
	// （例如对着冻结的 dockerd 跑 `docker ps`），此时 `cmd.Wait()` 永远不返回。在无墙
	// 钟上限（DefaultAgentTimeout = 0）下，这样的运行会永远停在 "running"，所以本看门
	// 狗是它唯一的存活网。
	//
	// 预算只回答一个问题：最长合法“静默步骤”可以有多长？不是“我们多想早点发现卡死”。
	// 两者代价极不对称——强停一个健康运行会丢掉工作、把 issue 翻成 blocked，而晚点发现
	// 卡死只是占着一个并发槽。所以这个值跟随合法端点走：一条 RFC 长度的 assistant 消息
	// （MUL-2300 把它从 5 分钟挪到 30 分钟）、一个子 agent、一整轮测试套件。30 分钟仍
	// 会腰斩真实工作，所以现在是 2h。
	//
	// 设 MULTICA_AGENT_IDLE_WATCHDOG=0 可关掉整套看门狗。
	DefaultAgentIdleWatchdog              = 2 * time.Hour
	DefaultRuntimeName                    = "Local Agent"
	DefaultWorkspaceBootstrapSyncInterval = 30 * time.Second
	DefaultWorkspaceLegacySyncInterval    = 5 * time.Minute
	DefaultWorkspaceSyncInterval          = 30 * time.Minute
	DefaultWorkspaceSyncMaxBackoff        = 30 * time.Minute
	DefaultHealthPort                     = 19514
	DefaultMaxConcurrentTasks             = 20
	DefaultGCInterval                     = 2 * time.Hour
	DefaultGCTTL                          = 24 * time.Hour      // 1 天——AI 编码 issue 很少长期开着
	DefaultGCCompletedTaskTTLCloud        = 14 * 24 * time.Hour // 14 天——Multica Cloud 默认限制已完成 issue 任务环境的保留期；见 defaultGCCompletedTaskTTL
	DefaultGCCompletedTaskTTLSelfHost     = 0                   // 禁用——自托管保留每个已完成环境直到其 issue 终态，除非运维显式开启
	DefaultGCOrphanTTL                    = 72 * time.Hour      // 3 天——无 meta 的孤儿目录（崩溃、GC 前遗留）
	DefaultGCArtifactTTL                  = 12 * time.Hour      // 12h——任务完成这么久后丢弃可再生 artifact
	DefaultGCCodexSessionTTL              = 14 * 24 * time.Hour // 14 天——回收这么久未动的“每 issue”Codex session store
	DefaultGCHermesMemoryTTL              = 90 * 24 * time.Hour // 90 天——回收这么久未动的“每 agent”Hermes memory store（长：回收这些等于可见的失忆，而且只是几个 markdown 文件）
	DefaultGCHermesSessionTTL             = 14 * 24 * time.Hour // 14 天——回收这么久未动的“每会话”Hermes session store（与 Codex 一致：装 transcript，丢一个闲置的只是重开线程而非丢 agent 笔记）
	DefaultGCRepoTTL                      = 30 * 24 * time.Hour // 30 天——驱逐这么久没有任务 checkout 过的裸仓库缓存
	// DefaultGCTaskTempLegacyTTL 是 0——禁用。早于“临时目录执行锁”的 daemon 留下的
	// 每任务临时目录完全不带活性信号，年龄也无法提供：任务合法地可能跑数周
	// （DefaultAgentTimeout 是 0），另一个 profile 上的 pre-lock daemon 可能还在跑它，
	// 而机器上每个 daemon 共享同一个 temp 基。因此回收它们是运维的显式决定——在确认本
	// 机没有 pre-lock daemon 在跑时做。带锁的目录按活性回收，永远不碰这个开关。
	DefaultGCTaskTempLegacyTTL     = time.Duration(0)
	DefaultAutoUpdateCheckInterval = 6 * time.Hour // daemon 多久轮询一次 GitHub 找更新的 CLI 版本
)

// DefaultGCArtifactPatterns 列出 GC 循环视为“可再生构建产物”的 basename 匹配项。
// 保持保守：只收“总是便宜能重建”的目录（`pnpm install`、`next build`、
// `turbo build`）。`dist/`、`build/`、`.cache/` 或 `.venv/` 之类在某些仓库可能合法地
// 装着源码或发布产物，默认*不*包含——按部署用 MULTICA_GC_ARTIFACT_PATTERNS 扩展列表。
var DefaultGCArtifactPatterns = []string{"node_modules", ".next", ".turbo"}

// Config 持有 daemon 的全部配置。
type Config struct {
	ServerBaseURL                  string
	DaemonID                       string
	LegacyDaemonIDs                []string // 这台机器历史上可能注册过的 daemon_id；注册时上报，让服务器能合并旧的 runtime 行
	DeviceName                     string
	RuntimeName                    string
	CLIVersion                     string                // multica CLI 版本（如 "0.1.13"）
	LaunchedBy                     string                // 被 Electron app 拉起时为 "desktop"，独立启动为空
	Profile                        string                // profile 名（空 = 默认）
	Agents                         map[string]AgentEntry // 按 provider 为键；含 agent.BuiltinRuntimes 里的内置 runtime 身份
	WorkspacesRoot                 string                // 执行环境的基础路径（缺省：~/multica_workspaces）
	KeepEnvAfterTask               bool                  // 任务后保留环境供调试
	HealthPort                     int                   // 健康检查本地 HTTP 端口（缺省：19514）
	MaxConcurrentTasks             int                   // 并行运行的最大任务数（缺省：20）
	GCEnabled                      bool                  // 启用周期 workspace 垃圾回收（缺省：true）
	GCInterval                     time.Duration         // GC 循环多久跑一次（缺省：2h）
	GCTTL                          time.Duration         // 清理“issue 已 done/cancelled 且 updated_at 早于 now()-TTL”的目录（缺省：24h）
	GCCompletedTaskTTL             time.Duration         // 完全清理“完成至少这么久的不活跃 issue 任务环境”，无论父 issue 状态（缺省：Multica Cloud 14d，其它 0/禁用；local_directory 环境绝不整删）
	GCOrphanTTL                    time.Duration         // 清理“无 meta 的孤儿目录，或 issue gc-check 返回 404 的目录”，超过该年龄即清（缺省：72h）。404 路径用同一个 TTL——被缩小权限的 token 无法瞬间抹掉活 workspace。
	GCArtifactTTL                  time.Duration         // 任务完成至少这么久后，丢弃可再生 artifact：父记录保住目录时（开着的 issue）删按模式命中的构建产物；对每种任务都删确切的 daemon 受管 Codex 缓存（缺省：12h，设 0 同时禁用两者）
	GCArtifactPatterns             []string              // artifact 清理时删除其子树的 basename 模式（缺省：node_modules, .next, .turbo）
	GCRepoTTL                      time.Duration         // 没有任务用某个 .repos 下缓存裸仓库创建 worktree 这么久、它也没有剩余 worktree、且不再挂接任何受管 workspace 时驱逐它（缺省：30d，设 0 禁用）
	GCRepoMaintenanceEnabled       bool                  // 删掉过期 agent 引用后跑 reflog expire 与 git gc（缺省：true；可独立禁用作为运维急停开关）
	GCCodexSessionTTL              time.Duration         // 回收至少这么久未动的“每 issue”Codex session store（~/.codex/multica-sessions/<agent>/<issue>），让 done/被放弃 issue 的对话历史不会无限累积（缺省：14d，设 0 禁用）
	GCHermesMemoryTTL              time.Duration         // 回收至少这么久未动的“每 agent”Hermes memory store（<profile dir>/hermes-state/<agent>/<profile>），让被删 agent 的记忆不会永远留在磁盘（缺省：90d，设 0 禁用）
	GCHermesSessionTTL             time.Duration         // 回收至少这么久未动的“每会话”Hermes session store（<profile dir>/hermes-sessions/<agent>/<profile>/<conversation>），让 done 或被放弃对话的 transcript 不会无限累积（缺省：14d，设 0 禁用）
	GCTaskTempLegacyTTL            time.Duration         // 回收“不带执行锁”（即早于该锁的 daemon 留下）的每任务临时目录（<temp base>/multica-task-*），一旦里面没什么被触碰这么久。带锁的目录按活性回收、从不按年龄，所以此开关与它们无关。它也不回收“不含任何任务内容”的目录——旧空遗留，或“创建目录后、发布锁前就死了”的 daemon 留下的 shell——因为“正被发布”的目录长得正是“没内容”（缺省：0，禁用——见 DefaultGCTaskTempLegacyTTL）
	AutoUpdateEnabled              bool                  // 周期性检查更新的 CLI 版本并在空闲时自更新（缺省：Multica Cloud true，自托管 false）
	AutoUpdateCheckInterval        time.Duration         // 自动更新循环多久轮询一次新版本（缺省：6h）
	AutoReloadEnabled              bool                  // 磁盘上的 multica 二进制不再匹配运行版本时重启（缺省：CLI 拉起的 daemon true）
	PollInterval                   time.Duration
	HeartbeatInterval              time.Duration
	AgentTimeout                   time.Duration
	CodexSemanticInactivityTimeout time.Duration
	// CodexFirstTurnNoProgressTimeout 是 Codex“首轮无进展”上限的显式覆盖
	// （MULTICA_CODEX_FIRST_TURN_TIMEOUT）。0 表示未设置：后端保留默认上限，
	// CodexSemanticInactivityTimeout 只能把它收窄。正值直接抬高（或压低）那个上限，供
	// “首个事件确实慢”的 app-server 使用（GH #3262）。
	CodexFirstTurnNoProgressTimeout time.Duration
	CodexHandshakeTimeout           time.Duration
	CodexThreadHandshakeTimeout     time.Duration
	OpenCodeIdleWatchdog            time.Duration // OpenCode 专用无消息窗口；0 回退到 AgentIdleWatchdog，大于它的值不能突破全局上限
	AgentIdleWatchdog               time.Duration // 后端静默这么久且队列为空时强制停一次运行（0 = 禁用）
	AgentToolWatchdog               time.Duration // 单个工具调用在途（静默）这么久时强制停一次运行（0 = 工具调用期间绝不强停）；缺省等于 AgentIdleWatchdog，让运维只调一个数，除非故意想要更宽的工具预算
	ClaudeArgs                      []string
	CodexArgs                       []string
	CodebuddyArgs                   []string
	QwenArgs                        []string
	QwenpawArgs                     []string

	// ProfileCommandOverrides 把自定义 runtime profile_id 映射到“本机该 profile 要用的
	// 可执行文件绝对路径”（MUL-3284）。来自本地 CLI 配置
	// （cli.CLIConfig.ProfileCommandOverrides，由 `multica runtime profile set-path`
	// 写入）。appendProfileRuntimes 优先用“匹配且可执行”的覆盖，而不是在 PATH 上解析
	// profile 的 command_name。nil/空表示“一律经 PATH 解析”。
	ProfileCommandOverrides map[string]string
}

// Overrides 允许 CLI 参数覆盖环境变量与缺省值。零值被忽略、改用 env/缺省值。
type Overrides struct {
	ServerURL         string
	WorkspacesRoot    string
	PollInterval      time.Duration
	HeartbeatInterval time.Duration
	// AgentTimeout 是指针，这样显式 `--agent-timeout 0`（不设上限）能与“没传参数”
	// 区分开。nil = 用 env/缺省。
	AgentTimeout                   *time.Duration
	CodexSemanticInactivityTimeout time.Duration
	CodexHandshakeTimeout          time.Duration
	MaxConcurrentTasks             int
	DaemonID                       string
	DeviceName                     string
	RuntimeName                    string
	Profile                        string // profile 名（空 = 默认）
	HealthPort                     int    // 健康检查端口（0 = 用缺省）
	// AllowNoAgents 保留给只读的本地配置探测。daemon 启动保持默认 false，没有 agent
	// CLI 仍拒绝运行。
	AllowNoAgents bool
	// DisableAutoUpdate 为 true 时强制关掉自动更新轮询。没有对称的 "force on" 覆盖，
	// 因为 env/缺省本来就解析成启用；这个参数让用户能经 CLI 退出。
	DisableAutoUpdate       bool
	AutoUpdateCheckInterval time.Duration // 0 = 用 env/缺省
	// DisableAutoReload 为 true 时强制关掉磁盘版本监视器。单向，理由与
	// DisableAutoUpdate 相同：env/缺省本来就解析成启用。
	DisableAutoReload bool
}

// LoadConfig 从环境变量与可选的 CLI 参数覆盖构建 daemon 配置。
func LoadConfig(overrides Overrides) (Config, error) {
	// Server URL：覆盖 > env > 缺省
	rawServerURL := envOrDefault("MULTICA_SERVER_URL", DefaultServerURL)
	if overrides.ServerURL != "" {
		rawServerURL = overrides.ServerURL
	}
	serverBaseURL, err := NormalizeServerBaseURL(rawServerURL)
	if err != nil {
		return Config{}, err
	}

	var profileCommandOverrides map[string]string
	if cliCfg, err := cli.LoadCLIConfigForProfile(overrides.Profile); err != nil {
		slog.Warn("could not load CLI config for backend overrides; proceeding without",
			"profile", overrides.Profile, "err", err)
	} else {
		// 每机器的自定义 runtime 命令路径覆盖（MUL-3284）。拷进自己的 map，这样之后对
		// 已加载配置的改动不会别名 daemon 状态，空 map 也能归一化成 nil。
		if len(cliCfg.ProfileCommandOverrides) > 0 {
			profileCommandOverrides = make(map[string]string, len(cliCfg.ProfileCommandOverrides))
			for id, path := range cliCfg.ProfileCommandOverrides {
				if id == "" || strings.TrimSpace(path) == "" {
					continue
				}
				profileCommandOverrides[id] = path
			}
		}
	}

	// 发现已装的 agent CLI。抽出来让周期 workspace 同步能在活 daemon 上重跑同一发现
	// （MUL-5439）。
	agents := probeAgentCLIs()
	if len(agents) == 0 && !overrides.AllowNoAgents {
		return Config{}, fmt.Errorf("no agent CLI found: install a supported agent CLI and ensure it is on PATH")
	}

	claudeArgs, err := shellArgsFromEnv("MULTICA_CLAUDE_ARGS")
	if err != nil {
		return Config{}, err
	}
	codexArgs, err := shellArgsFromEnv("MULTICA_CODEX_ARGS")
	if err != nil {
		return Config{}, err
	}
	codebuddyArgs, err := shellArgsFromEnv("MULTICA_CODEBUDDY_ARGS")
	if err != nil {
		return Config{}, err
	}
	qwenArgs, err := shellArgsFromEnv("MULTICA_QWEN_ARGS")
	if err != nil {
		return Config{}, err
	}
	qwenpawArgs, err := shellArgsFromEnv("MULTICA_QWENPAW_ARGS")
	if err != nil {
		return Config{}, err
	}

	// 主机信息
	host, err := os.Hostname()
	if err != nil || strings.TrimSpace(host) == "" {
		host = "local-machine"
	}

	// 时长：覆盖 > env > 缺省
	pollInterval, err := durationFromEnv("MULTICA_DAEMON_POLL_INTERVAL", DefaultPollInterval)
	if err != nil {
		return Config{}, err
	}
	if overrides.PollInterval > 0 {
		pollInterval = overrides.PollInterval
	}

	heartbeatInterval, err := durationFromEnv("MULTICA_DAEMON_HEARTBEAT_INTERVAL", DefaultHeartbeatInterval)
	if err != nil {
		return Config{}, err
	}
	if overrides.HeartbeatInterval > 0 {
		heartbeatInterval = overrides.HeartbeatInterval
	}

	agentTimeout, err := durationFromEnv("MULTICA_AGENT_TIMEOUT", DefaultAgentTimeout)
	if err != nil {
		return Config{}, err
	}
	if overrides.AgentTimeout != nil {
		agentTimeout = *overrides.AgentTimeout
	}

	// MULTICA_AGENT_IDLE_WATCHDOG=0 禁用每任务 idle 看门狗。我们把 0 也经
	// durationFromEnv 送进，让运维不改二进制就能退出；任何正时长覆盖
	// DefaultAgentIdleWatchdog。
	agentIdleWatchdog, err := durationFromEnv("MULTICA_AGENT_IDLE_WATCHDOG", DefaultAgentIdleWatchdog)
	if err != nil {
		return Config{}, err
	}
	// MULTICA_OPENCODE_IDLE_WATCHDOG 收窄 OpenCode 流式模型响应的无消息窗口。零移除
	// provider 专用覆盖、回退到 MULTICA_AGENT_IDLE_WATCHDOG；正值不能突破全局上限，
	// 全局为零仍禁用整套机制。
	openCodeIdleWatchdog, err := durationFromEnv("MULTICA_OPENCODE_IDLE_WATCHDOG", DefaultOpenCodeIdleWatchdog)
	if err != nil {
		return Config{}, err
	}

	// 在途工具预算缺省等于 idle 预算：工具窗口之所以存在，只是因为 30 分钟对一次真实
	// build/install/test 太短；而既然 idle 预算已按“最长合法静默步骤”定尺寸，它已覆盖
	// 那些场景。
	//
	// 推导双向跟随——这正是把它们收敛成一个数的意义。抬高
	// MULTICA_AGENT_IDLE_WATCHDOG 不再会让工具调用静默钉在旧上限——而调低它现在也会
	// 调低工具预算，过去缩短 idle 窗口只把工具留在另一个更大的 2h。后一方向对任何“刻意
	// 缩短过 idle 窗口”的人是真实行为变化；下面的覆盖是让两者分开的办法。
	//
	// MULTICA_AGENT_TOOL_WATCHDOG 仍可覆盖“刻意让工具跑得比模型思考久”的情形，且 0
	// 保持原意：工具在途时绝不强停。
	agentToolWatchdog, err := durationFromEnv("MULTICA_AGENT_TOOL_WATCHDOG", agentIdleWatchdog)
	if err != nil {
		return Config{}, err
	}

	// Codex 在 app-server 协议内部跑自己的语义不活跃计时器；与 daemon 看门狗不同，它
	// 对工具不感知：整窗口不发任何东西的 commandExecution 也会触发它，哪怕明明有工具
	// 在途。因此那个计时器要替 Codex 运行上的*两个* daemon 看门狗站岗，尺寸必须按两者
	// 较大者来——否则一段安静的测试套件或输出缓冲的 `docker build` 会先死在 Codex 上限
	// 上，远早于本应保护它的 daemon 预算；“我们把预算抬到 2h”对 Codex 用户就是不成立。
	//
	// 工具预算 0 表示“工具在途绝不强停”，而这个计时器表达不了——它没有看工具的手段。
	// 它回退到 idle 预算而不是无限跑，这是保守读法。
	//
	// 整套看门狗被禁用（idle = 0）时，Codex 保留它自己内置的缺省。禁用 daemon 看门狗
	// 从未禁用过这个计时器，在这里悄悄把它变成 "unbounded" 会是一个大得多的改动。
	codexSemanticDefault := agentIdleWatchdog
	if agentToolWatchdog > codexSemanticDefault {
		codexSemanticDefault = agentToolWatchdog
	}
	if codexSemanticDefault <= 0 {
		codexSemanticDefault = DefaultCodexSemanticInactivityTimeout
	}
	codexSemanticInactivityTimeout, err := durationFromEnv("MULTICA_CODEX_SEMANTIC_INACTIVITY_TIMEOUT", codexSemanticDefault)
	if err != nil {
		return Config{}, err
	}
	if overrides.CodexSemanticInactivityTimeout > 0 {
		codexSemanticInactivityTimeout = overrides.CodexSemanticInactivityTimeout
	}

	// 0 = 未设置：codex 后端保留默认首轮上限。正值是运维的显式覆盖（GH #3262 / #5959）。
	codexFirstTurnNoProgressTimeout, err := durationFromEnv("MULTICA_CODEX_FIRST_TURN_TIMEOUT", 0)
	if err != nil {
		return Config{}, err
	}
	// 首轮覆盖只抬高首轮看门狗，抬不动同一 status:running 武装的并发语义不活跃计时器
	// ——而且语义计时器先武装（codex.go 在创建首轮计时器前先重置它）。覆盖 >= 语义超时
	// 时，有效首轮等待被截断到语义超时：等长时语义截止先到，即便两者同时触发，Go 的
	// select 也不确定性偏向首轮分支。于是失败会被归类成“语义不活跃”而非“首轮无进展”，
	// 进而禁用瞬态模型目录启动重试（GH #3291）。除非语义超时严格高于首轮超时，否则给
	// 出警告。
	if codexFirstTurnNoProgressTimeout > 0 {
		effectiveSemanticTimeout := codexSemanticInactivityTimeout
		if effectiveSemanticTimeout <= 0 {
			effectiveSemanticTimeout = DefaultCodexSemanticInactivityTimeout
		}
		if codexFirstTurnNoProgressTimeout >= effectiveSemanticTimeout {
			slog.Warn("MULTICA_CODEX_FIRST_TURN_TIMEOUT is greater than or equal to the semantic-inactivity timeout; the effective first-turn wait is truncated to the semantic timeout and the model-catalog startup retry is disabled. Because the semantic timer is armed first and equal durations do not deterministically favour the first-turn deadline, set MULTICA_CODEX_SEMANTIC_INACTIVITY_TIMEOUT strictly above MULTICA_CODEX_FIRST_TURN_TIMEOUT (with some margin) to preserve it.",
				"first_turn_timeout", codexFirstTurnNoProgressTimeout.String(),
				"semantic_inactivity_timeout", effectiveSemanticTimeout.String(),
			)
		}
	}

	codexHandshakeTimeout, err := durationFromEnv("MULTICA_CODEX_HANDSHAKE_TIMEOUT", DefaultCodexHandshakeTimeout)
	if err != nil {
		return Config{}, err
	}
	if codexHandshakeTimeout <= 0 {
		codexHandshakeTimeout = DefaultCodexHandshakeTimeout
	}
	if overrides.CodexHandshakeTimeout > 0 {
		codexHandshakeTimeout = overrides.CodexHandshakeTimeout
	}
	// 保留旧版全局覆盖语义，同时给重量级线程 RPC 一个更宽的缺省。CLI 把参数、env 与
	// 持久化配置值解析进 Overrides；内嵌调用方可能直接拿环境调 LoadConfig。
	codexThreadHandshakeTimeout := DefaultCodexThreadHandshakeTimeout
	if raw, ok := os.LookupEnv("MULTICA_CODEX_HANDSHAKE_TIMEOUT"); ok && strings.TrimSpace(raw) != "" {
		if parsed, parseErr := parseFlexDuration(strings.TrimSpace(raw)); parseErr == nil && parsed > 0 {
			codexThreadHandshakeTimeout = codexHandshakeTimeout
		}
	}
	if overrides.CodexHandshakeTimeout > 0 {
		codexThreadHandshakeTimeout = overrides.CodexHandshakeTimeout
	}

	maxConcurrentTasks, err := intFromEnv("MULTICA_DAEMON_MAX_CONCURRENT_TASKS", DefaultMaxConcurrentTasks)
	if err != nil {
		return Config{}, err
	}
	if overrides.MaxConcurrentTasks > 0 {
		maxConcurrentTasks = overrides.MaxConcurrentTasks
	}

	// Profile
	profile := overrides.Profile

	// daemon_id 解析：覆盖 > env > 磁盘上的持久 UUID。
	// 持久 UUID 一次性写到 `<profile-dir>/daemon.id` 之后永远复用，让主机名漂移
	// （.local 后缀、系统改名、mDNS 状态、profile 切换）不再铸造新的 runtime 身份。
	// 调用方仍可经 MULTICA_DAEMON_ID 或覆盖字段钉住特定 id（例如测试或内嵌环境）。
	daemonID := strings.TrimSpace(os.Getenv("MULTICA_DAEMON_ID"))
	if overrides.DaemonID != "" {
		daemonID = overrides.DaemonID
	}
	if daemonID == "" {
		persisted, err := EnsureDaemonID(profile)
		if err != nil {
			return Config{}, fmt.Errorf("ensure daemon id: %w", err)
		}
		daemonID = persisted
	}
	// 由当前主机名/profile 推导出的历史 daemon_id。服务器注册时用它们把本机“UUID 化
	// 之前”的 runtime 行并入新的 UUID 键行并删掉旧的。
	legacyDaemonIDs := LegacyDaemonIDs(host, profile)
	// 变更（#1220）之前的 daemon 身份按 profile 存储，意味着同一台机器可能残留多个
	// daemon.id 文件——例如 ~/.multica/daemon.id（默认）加 ~/.multica/profiles/<x>/
	// daemon.id。把这些 UUID 暴露出来，让服务器能把它们的 runtime 行并入规范机器 UUID。
	// 无致命错误：坏掉的 profiles 目录不该挡启动。
	if uuids, err := LegacyDaemonUUIDs(); err == nil {
		legacyDaemonIDs = append(legacyDaemonIDs, uuids...)
	}
	// 剔除任何与解析出的 daemon_id 冲突的项（例如用户显式钉了
	// MULTICA_DAEMON_ID=<hostname>，或规范 id 本身就是从变更前的 profile 文件升上来的）。
	legacyDaemonIDs = filterLegacyIDs(legacyDaemonIDs, daemonID)

	deviceName := envOrDefault("MULTICA_DAEMON_DEVICE_NAME", host)
	if overrides.DeviceName != "" {
		deviceName = overrides.DeviceName
	}

	runtimeName := envOrDefault("MULTICA_AGENT_RUNTIME_NAME", DefaultRuntimeName)
	if overrides.RuntimeName != "" {
		runtimeName = overrides.RuntimeName
	}

	// Workspaces 根：覆盖 > env > 缺省（~/multica_workspaces 或 ~/multica_workspaces_<profile>）
	workspacesRoot, err := ResolveWorkspacesRoot(profile, overrides.WorkspacesRoot)
	if err != nil {
		return Config{}, err
	}

	// 健康端口：覆盖 > 缺省
	healthPort := DefaultHealthPort
	if overrides.HealthPort > 0 {
		healthPort = overrides.HealthPort
	}

	// 任务后保留环境：env > 缺省（false）
	keepEnv := os.Getenv("MULTICA_KEEP_ENV_AFTER_TASK") == "true" || os.Getenv("MULTICA_KEEP_ENV_AFTER_TASK") == "1"

	// GC 配置：env > 缺省
	gcEnabled := true
	if v := os.Getenv("MULTICA_GC_ENABLED"); v == "false" || v == "0" {
		gcEnabled = false
	}
	gcInterval, err := durationFromEnv("MULTICA_GC_INTERVAL", DefaultGCInterval)
	if err != nil {
		return Config{}, err
	}
	gcTTL, err := durationFromEnv("MULTICA_GC_TTL", DefaultGCTTL)
	if err != nil {
		return Config{}, err
	}
	gcCompletedTaskTTL, err := durationFromEnv("MULTICA_GC_COMPLETED_TASK_TTL", defaultGCCompletedTaskTTL(serverBaseURL))
	if err != nil {
		return Config{}, err
	}
	gcOrphanTTL, err := durationFromEnv("MULTICA_GC_ORPHAN_TTL", DefaultGCOrphanTTL)
	if err != nil {
		return Config{}, err
	}
	gcArtifactTTL, err := durationFromEnv("MULTICA_GC_ARTIFACT_TTL", DefaultGCArtifactTTL)
	if err != nil {
		return Config{}, err
	}
	gcCodexSessionTTL, err := durationFromEnv("MULTICA_GC_CODEX_SESSION_TTL", DefaultGCCodexSessionTTL)
	if err != nil {
		return Config{}, err
	}
	gcTaskTempLegacyTTL, err := durationFromEnv("MULTICA_GC_TASK_TEMP_LEGACY_TTL", DefaultGCTaskTempLegacyTTL)
	if err != nil {
		return Config{}, err
	}
	gcHermesMemoryTTL, err := durationFromEnv("MULTICA_GC_HERMES_MEMORY_TTL", DefaultGCHermesMemoryTTL)
	if err != nil {
		return Config{}, err
	}
	gcHermesSessionTTL, err := durationFromEnv("MULTICA_GC_HERMES_SESSION_TTL", DefaultGCHermesSessionTTL)
	if err != nil {
		return Config{}, err
	}
	gcRepoTTL, err := durationFromEnv("MULTICA_GC_REPO_TTL", DefaultGCRepoTTL)
	if err != nil {
		return Config{}, err
	}
	gcRepoMaintenanceEnabled := boolFromEnv("MULTICA_GC_REPO_MAINTENANCE_ENABLED", true)
	gcArtifactPatterns := patternsFromEnv("MULTICA_GC_ARTIFACT_PATTERNS", DefaultGCArtifactPatterns)

	// 自动更新配置：缺省 -> env 覆盖 -> CLI 覆盖。
	//
	// 缺省在 Multica Cloud（api.multica.ai）上默认开、自托管默认关。自托管运维常跑一个
	// 带自己补丁的 fork，悄悄把 daemon 升级到上游 GitHub release 会冲掉那些成果；他们也
	// 常停留在旧版服务器构建上，新 CLI 可能已不兼容。自托管默认关掉自动更新能同时避免
	// 这两个坑（MUL-2381）。两侧运维都可用 MULTICA_DAEMON_AUTO_UPDATE 翻转缺省。
	autoUpdateEnabled := boolFromEnv("MULTICA_DAEMON_AUTO_UPDATE", isOfficialCloudServer(serverBaseURL))
	if overrides.DisableAutoUpdate {
		autoUpdateEnabled = false
	}
	autoUpdateInterval, err := durationFromEnv("MULTICA_DAEMON_AUTO_UPDATE_INTERVAL", DefaultAutoUpdateCheckInterval)
	if err != nil {
		return Config{}, err
	}
	if overrides.AutoUpdateCheckInterval > 0 {
		autoUpdateInterval = overrides.AutoUpdateCheckInterval
	}

	// 自动重载刻意不 gate 在 autoUpdateEnabled 上。"不从 GitHub 拉新版本"与"跟随我自己
	// 替换的二进制"是两件不同的事，而自托管把前者默认关掉的理据（别冲掉我的 fork）恰好
	// 对后者指向相反方向：手工装了构建的运维，就是要 daemon 去跑它。
	// 对每个 CLI 拉起的 daemon 默认开；Desktop 在循环处退出。
	autoReloadEnabled := boolFromEnv("MULTICA_DAEMON_AUTO_RELOAD", true)
	if overrides.DisableAutoReload {
		autoReloadEnabled = false
	}

	return Config{
		ServerBaseURL:                   serverBaseURL,
		DaemonID:                        daemonID,
		LegacyDaemonIDs:                 legacyDaemonIDs,
		DeviceName:                      deviceName,
		RuntimeName:                     runtimeName,
		Profile:                         profile,
		Agents:                          agents,
		WorkspacesRoot:                  workspacesRoot,
		KeepEnvAfterTask:                keepEnv,
		GCEnabled:                       gcEnabled,
		GCInterval:                      gcInterval,
		GCTTL:                           gcTTL,
		GCCompletedTaskTTL:              gcCompletedTaskTTL,
		GCOrphanTTL:                     gcOrphanTTL,
		GCArtifactTTL:                   gcArtifactTTL,
		GCArtifactPatterns:              gcArtifactPatterns,
		GCRepoTTL:                       gcRepoTTL,
		GCRepoMaintenanceEnabled:        gcRepoMaintenanceEnabled,
		GCCodexSessionTTL:               gcCodexSessionTTL,
		GCHermesMemoryTTL:               gcHermesMemoryTTL,
		GCHermesSessionTTL:              gcHermesSessionTTL,
		GCTaskTempLegacyTTL:             gcTaskTempLegacyTTL,
		AutoUpdateEnabled:               autoUpdateEnabled,
		AutoUpdateCheckInterval:         autoUpdateInterval,
		AutoReloadEnabled:               autoReloadEnabled,
		HealthPort:                      healthPort,
		MaxConcurrentTasks:              maxConcurrentTasks,
		PollInterval:                    pollInterval,
		HeartbeatInterval:               heartbeatInterval,
		AgentTimeout:                    agentTimeout,
		CodexSemanticInactivityTimeout:  codexSemanticInactivityTimeout,
		CodexFirstTurnNoProgressTimeout: codexFirstTurnNoProgressTimeout,
		CodexHandshakeTimeout:           codexHandshakeTimeout,
		CodexThreadHandshakeTimeout:     codexThreadHandshakeTimeout,
		OpenCodeIdleWatchdog:            openCodeIdleWatchdog,
		AgentIdleWatchdog:               agentIdleWatchdog,
		AgentToolWatchdog:               agentToolWatchdog,
		ClaudeArgs:                      claudeArgs,
		CodexArgs:                       codexArgs,
		CodebuddyArgs:                   codebuddyArgs,
		QwenArgs:                        qwenArgs,
		QwenpawArgs:                     qwenpawArgs,
		ProfileCommandOverrides:         profileCommandOverrides,
	}, nil
}

// officialCloudHost 是 Multica 托管云的主机名。它是我们唯一当作自动更新默认值
// "official" 的来源——staging、preview 与任何将来的 *.multica.ai 子域都被刻意排除，
// 让它们继承更安全的自托管缺省，直到显式开启。
const officialCloudHost = "api.multica.ai"

// isOfficialCloudServer 报告解析出的服务器基地址是否指向 Multica 托管云。用于挑选
// “在我们运维的基础设施上安全、在别人那不安全”的缺省：自动更新（云用户跑一个发布配套
// CLI 版本的服务器，因此自更新开安全；自托管用户可能跑 fork 或钉住老服务器）与已完成
// 任务保留 TTL（见 defaultGCCompletedTaskTTL）。匹配只看主机且不区分大小写——端口与
// 路径忽略。
func isOfficialCloudServer(baseURL string) bool {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Hostname(), officialCloudHost)
}

// defaultGCCompletedTaskTTL 按部署类型挑选“已完成任务保留”缺省，方式与自动更新缺省
// 相同。
//
// 完全删除一个已完成任务环境不可逆：它带走检出、.git（包括 agent 留下未提交的工作）、
// output/ 与 logs/。在 Multica Cloud 上这笔交易该我们做——节点是我们运营的，磁盘满
// 是我们的事故而非用户的，无界保留又没有运维在盯。自托管上同样的缺省会把一次例行
// daemon 升级变成“删除运维从没同意放弃的数据”的静默事故，所以保持禁用直到他们自己设
// MULTICA_GC_COMPLETED_TASK_TTL。任一侧都可向任一方向覆盖；云用显式 0 再次禁用它。
//
// 非生产云来源（staging、previews）与 officialCloudHost 排除的一切一起落到自托管值，
// 想要该上限就显式开启。
func defaultGCCompletedTaskTTL(serverBaseURL string) time.Duration {
	if isOfficialCloudServer(serverBaseURL) {
		return DefaultGCCompletedTaskTTLCloud
	}
	return DefaultGCCompletedTaskTTLSelfHost
}

// NormalizeServerBaseURL 把 WebSocket 或 HTTP URL 转成 HTTP 基地址 URL。
func NormalizeServerBaseURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("invalid MULTICA_SERVER_URL: %w", err)
	}
	switch u.Scheme {
	case "ws":
		u.Scheme = "http"
	case "wss":
		u.Scheme = "https"
	case "http", "https":
	default:
		return "", fmt.Errorf("MULTICA_SERVER_URL must use ws, wss, http, or https")
	}
	if u.Path == "/ws" {
		u.Path = ""
	}
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""
	return strings.TrimRight(u.String(), "/"), nil
}

// TaskWorkspacesRootEnv 把“拥有某个受管任务的 daemon 的 workspaces 根”带进该任务的
// 环境。任务模式的 `daemon disk-usage` 只读它、不读别的：ResolveWorkspacesRoot 从
// $HOME 与 --profile 名推导缺省，因此由命名 profile daemon 托管的任务若不如此会去扫
// 默认根、悄悄报错一棵树。
const TaskWorkspacesRootEnv = "MULTICA_TASK_WORKSPACES_ROOT"

// ResolveWorkspacesRoot 返回 daemon 与 CLI 应视为 workspaces 根的绝对路径。解析顺序：
// 显式覆盖 > MULTICA_WORKSPACES_ROOT env > 缺省（$HOME/multica_workspaces，命名
// profile 为 $HOME/multica_workspaces_<profile>）。只读调用方（如 `multica daemon
// disk-usage`）直接用这个，从而选中与运行中 daemon 相同的目录。在受管任务内部请改用
// TaskWorkspacesRootEnv——见 resolveDiskUsageRoot。
func ResolveWorkspacesRoot(profile, override string) (string, error) {
	root := strings.TrimSpace(os.Getenv("MULTICA_WORKSPACES_ROOT"))
	if override != "" {
		root = override
	}
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w (set MULTICA_WORKSPACES_ROOT to override)", err)
		}
		if profile != "" {
			root = filepath.Join(home, "multica_workspaces_"+profile)
		} else {
			root = filepath.Join(home, "multica_workspaces")
		}
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve absolute workspaces root: %w", err)
	}
	return abs, nil
}

// ArtifactPatternsFromEnv 返回配置的 artifact 模式集合——与 GC 循环跑“仅 artifact”
// 清理时查询的同一列表。disk-usage CLI 用这个确保它上报的 "artifact size" 与 GC 实际
// 能回收的一致。
func ArtifactPatternsFromEnv() []string {
	return patternsFromEnv("MULTICA_GC_ARTIFACT_PATTERNS", DefaultGCArtifactPatterns)
}

// patternsFromEnv 从 env 读逗号分隔列表。含路径分隔符的模式被静默丢弃——GC artifact
// 清理只匹配目录 basename、绝不匹配路径，所以像 "foo/bar" 的模式没有意义，接受它只是
// 埋个雷。
func patternsFromEnv(name string, defaults []string) []string {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		out := make([]string, len(defaults))
		copy(out, defaults)
		return out
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" || strings.ContainsAny(p, "/\\") {
			continue
		}
		out = append(out, p)
	}
	return out
}

// shellArgsFromEnv 把 env 里的命令参数字符串按 shell 词法拆成切片。
func shellArgsFromEnv(name string) ([]string, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return nil, nil
	}
	args, err := shellwords.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid %s: %w", name, err)
	}
	return args, nil
}

// resolveAgentExecutablePath 返回 daemon 应为某个 agent 命令保留的可执行入口点。裸
// 命令名会钉到启动时解析出的路径，让之后的 PATH 变化无法改道任务启动。普通可执行文件
// 钉到具体目标；由“按名分发的垫片”所有的入口点保留管理器选择正确包所需的命令名（见
// discoveredExecutablePath）。
// Windows 上它刻意保留稳定的 discovered junction 路径；resolveAgentEntryWithHeal 每次
// 启动都跟随它，让“重定向仍存活 junction”的安装器升级无需重启 daemon 即生效。
// 当 ~/.multica/hooks 遮蔽了真实 agent 二进制时，跳过那个 hooks 目录：早先生成的钩子
// 包装器会执行同一命令名，若 daemon 记录或拉起的是包装器，就会无限递归。
func resolveAgentExecutablePath(cmd string) (string, error) {
	resolved, err := exec.LookPath(cmd)
	if err != nil {
		return "", err
	}
	if strings.ContainsAny(cmd, "/\\") {
		return canonicalConfiguredExecutablePath(resolved), nil
	}
	if isInMulticaHooksDir(resolved) {
		if unshadowed, err := lookPathExcludingMulticaHooks(cmd); err == nil {
			return unshadowed, nil
		}
	}
	return discoveredExecutablePath(resolved), nil
}

// agentExecutablePresent 报告 path 当前是否解析成一个可运行的可执行文件，使用 agent
// 后端启动时应用的同一检查（exec.LookPath）。钉住的 AgentEntry.Path 若通不过这个检查，
// 说明它已从磁盘消失——典型是版本管理器做了原地升级、删掉了路径指向的旧版本目录
// （MUL-4486）。
func agentExecutablePresent(path string) bool {
	if path == "" {
		return false
	}
	_, err := exec.LookPath(path)
	return err == nil
}

// reresolveAgentCommand 对单个 agent 命令名重跑启动时的解析，返回新解析出的绝对路径。
// 它镜像 LoadConfig 里 probe() 的顺序：先 exec.LookPath（经 resolveAgentExecutablePath
// 保留 ~/.multica/hooks 排除），再对“GUI 拉起的 daemon 在自己 PATH 上看不到的裸命令
// 名”做登录 shell 兜底。它只在 miss 路径被调用——先前钉住的路径已消失——因此登录
// shell 代价很少付，正常启动从不付。
func reresolveAgentCommand(cmd string) (string, bool) {
	if cmd == "" {
		return "", false
	}
	if path, err := resolveAgentExecutablePath(cmd); err == nil {
		return path, true
	}
	// daemon 自身 PATH 看不到的裸命令名：像启动探测对 fnm/nvm/native-installer 前缀
	// 那样，经用户登录 shell 重试。绝对/相对覆盖跳过这里——运维钉的 MULTICA_*_PATH
	// 若已不存在应保持硬 miss，而不是悄悄解析出另一个二进制。
	if !strings.ContainsAny(cmd, "/\\") {
		if path, ok := resolveAgentsViaLoginShell([]string{cmd})[cmd]; ok {
			return path, true
		}
	}
	return "", false
}

// lookPathExcludingMulticaHooks 手工走 PATH、跳过 ~/.multica/hooks 目录找第一个匹配
// 的可执行文件，用于摘掉自生成钩子包装器对真实二进制的遮蔽。
func lookPathExcludingMulticaHooks(cmd string) (string, error) {
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" {
			dir = "."
		}
		if isMulticaHooksDir(dir) {
			continue
		}
		candidate := filepath.Join(dir, cmd)
		if isExecutableFile(candidate) {
			return discoveredExecutablePath(candidate), nil
		}
	}
	return "", exec.ErrNotFound
}

func isInMulticaHooksDir(path string) bool {
	if path == "" {
		return false
	}
	return isMulticaHooksDir(filepath.Dir(path))
}

func isMulticaHooksDir(dir string) bool {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return false
	}
	return samePathDir(dir, filepath.Join(home, ".multica", "hooks"))
}

func samePathDir(a, b string) bool {
	absA, err := filepath.Abs(a)
	if err != nil {
		return false
	}
	absB, err := filepath.Abs(b)
	if err != nil {
		return false
	}
	absA = filepath.Clean(absA)
	absB = filepath.Clean(absB)
	if realA, err := filepath.EvalSymlinks(absA); err == nil {
		absA = realA
	}
	if realB, err := filepath.EvalSymlinks(absB); err == nil {
		absB = realB
	}
	return absA == absB
}

// canonicalExecutablePath 把配置路径尽量解析成“真实文件”的规范绝对路径；解析失败时
// 降级保留原路径。
func canonicalExecutablePath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		slog.Debug("make agent executable path absolute failed; keeping configured path", "path", path, "error", err)
		return path
	}
	if real, err := canonicalPath(abs); err == nil {
		return real
	} else {
		slog.Debug("canonicalize agent executable path failed; keeping absolute path", "path", abs, "error", err)
	}
	return abs
}

func isExecutableFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return info.Mode()&0o111 != 0
}

// defaultAgentCommandNames 列出 agent 探测循环在应用任何 MULTICA_*_PATH 覆盖之前要试
// 的命令名。与 LoadConfig 里的 `probe(...)` 调用保持同步——shell 兜底解析器用这份列表
// 在单次 shell 调用里为每个已知 agent 预取规范路径，而不是“每次 miss 付一次钱”。
//
// 内置 runtime 身份命令（如 "omp"）从描述符注册表（agent.BuiltinRuntimeCommands）
// 追加，因此新增一个分支不必手工改这份列表。
var defaultAgentCommandNames = append([]string{
	"claude", "codex", "opencode", "codearts", "deveco", "hermes",
	"pi", "cursor-agent", "copilot", "kimi", "reasonix", "dsh", "kiro-cli", "codebuddy", "agy", "qodercli", "qoderclicn", "traecli", "grok", "qwen", "qwenpaw", "mcode", "dim", "zeroclaw",
}, agent.BuiltinRuntimeCommands()...)

// codexDesktopAppBundlePaths 返回内置 Codex CLI 的候选 macOS app-bundle 位置。OpenAI
// 把 Desktop app 从 Codex.app 迁到 ChatGPT.app（#5205）。候选先按安装位置排序（系统
// /Applications 在用户 ~/Applications 前）；每个位置内先试新的 ChatGPT.app 路径再试
// 旧的 Codex.app 路径，于是更新过的安装获胜，旧安装仍能解析。
var codexDesktopAppBundlePaths = func() []string {
	paths := []string{
		"/Applications/ChatGPT.app/Contents/Resources/codex",
		"/Applications/Codex.app/Contents/Resources/codex",
	}
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths,
			filepath.Join(home, "Applications", "ChatGPT.app", "Contents", "Resources", "codex"),
			filepath.Join(home, "Applications", "Codex.app", "Contents", "Resources", "codex"),
		)
	}
	return paths
}

// loginShellResolveTimeout 限制 daemon 等用户登录 shell 打印规范 agent 路径的时间上限。
// 坏掉的 rc 文件不该挡启动——shell 超过这个时间就让步，不采用 shell 解析兜底，daemon
// 回退到与本代码加入前相同的行为。
const loginShellResolveTimeout = 3 * time.Second

// loginShellResolveWaitDelay 是 *在* loginShellResolveTimeout 过后、`CommandContext`
// 已示意 shell 退出之后再跑的硬上限。context 杀掉 shell 进程本身，但真实世界的 rc 文件
// 经常后台化“继承了 stdout”的东西（`nvm` 垫片、`direnv hook`、`eval $(starship
// init)`、裸 `&`）。这些幸存者一直握着 stdout 管道，`cmd.Output()` 会为 EOF 阻塞到它们
// 结束。Cmd.WaitDelay（Go 1.20+）在该延迟过后强制关闭管道并返回，因此病态 rc 文件造成
// 的总启动代价上界是 `timeout + waitDelay`，而不是“用户后台进程恰好跑多久”。
const loginShellResolveWaitDelay = 2 * time.Second

// supportedLoginShells 限制我们会以 `<shell> -ilc <script>` 调用的解释器。只支持
// POSIX 兼容 shell，让下面的解析脚本原样可用。刻意缺席：fish（用 `command -s`，命令
// 替换语法不同）。
var supportedLoginShells = map[string]struct{}{
	"bash": {},
	"zsh":  {},
	"sh":   {},
	"dash": {},
	"ksh":  {},
}

// resolveAgentsViaLoginShell 请用户的登录 shell 为 `names` 里的每个名字打印一个绝对、
// 可安全调用的路径。返回 shell 能找到的 name → path map；shell 不可用/不支持/超时/
// 无可用的输出时返回空 map。
//
// 为什么需要它：
//
// macOS/Linux 上的 daemon 式进程不继承用户交互 PATH。Terminal.app 里 `claude
// --version` 能用，并不保证从 Launchpad、Electron app 或 `launchctl` 拉起的二进制里
// exec.LookPath("claude") 也能用。最常见的元凶是 fnm/nvm/volta 的 "multishell" 前缀
// 目录（每 shell、临时）与 Anthropic 原生安装器（`~/.claude/local/`）——两者都把二进制
// 放在只有 `.zshrc` 知道的路径上。
//
// 实现要点：
//
//   - 用 `$SHELL -ilc <script>`，同时带 -i（交互）与 -l（登录），以便同时拾取
//     ~/.zshrc / ~/.bashrc 或 ~/.zprofile / ~/.bash_profile 里设的 PATH。真实用户两个
//     都会放。
//   - 脚本在拉起的 shell 还活着时经 `cd "$dirname" && pwd -P` 解析符号链接。
//     fnm/nvm 的 "multishell" 目录在 shell 退出时消失，所以规范路径必须在 stdout 回到
//     Go 之前捕获——到那时原路径已经没了。
//   - 只信“看起来是绝对路径且从 daemon 视角重新 exec.LookPath 仍过”的输出。这能滤掉
//     别名（对别名 `command -v` 打印的是别名定义而非路径）与 shell 没完全规范化的
//     per-shell 路径。
//   - agent 名限制为 defaultAgentCommandNames 里的裸集合（只含 `[A-Za-z0-9._-]`）；
//     不加引号内联进脚本以保持可读。自定义 MULTICA_*_PATH 值永不进这个解析器——它们
//     直接走 exec.LookPath。
//
// 做成 var，测试就能不 fork 真实登录 shell 来打桩。
var resolveAgentsViaLoginShell = func(names []string) map[string]string {
	out := map[string]string{}
	if len(names) == 0 {
		return out
	}
	shell := strings.TrimSpace(os.Getenv("SHELL"))
	if shell == "" {
		return out
	}
	if _, ok := supportedLoginShells[filepath.Base(shell)]; !ok {
		return out
	}

	safe := make([]string, 0, len(names))
	for _, n := range names {
		if isSafeAgentName(n) {
			safe = append(safe, n)
		}
	}
	if len(safe) == 0 {
		return out
	}

	ctx, cancel := context.WithTimeout(context.Background(), loginShellResolveTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, shell, "-ilc", buildLoginShellResolveScript(safe))
	cmd.WaitDelay = loginShellResolveWaitDelay
	raw, err := cmd.Output()
	if err != nil {
		return out
	}

	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			continue
		}
		name, path := parts[0], strings.TrimSpace(parts[1])
		if !filepath.IsAbs(path) {
			continue
		}
		// 最终现实检查：shell 给我们的路径此刻从 daemon 视角必须仍可执行。fnm
		// multishell 是典型例子——辅助 shell 里 pwd -P 可能冲不出每会话 bin 目录，
		// 我们宁可报 "not found" 也不交还一条“在探测与执行之间就消失”的路径。
		if _, err := exec.LookPath(path); err != nil {
			continue
		}
		out[name] = path
	}
	return out
}

// buildLoginShellResolveScript 返回 resolveAgentsViaLoginShell 在 `$SHELL -ilc`
// 里运行的 shell 脚本。脚本：
//
//  1. 遍历给定的命令名，
//  2. 剥掉同名“本地定义的别名与 shell 函数”，让 `command -v` 穿透到 PATH 上的真实
//     二进制（见下），
//  3. 用 POSIX `command -v` 在交互 PATH 上找每个名字，
//  4. 拒绝非绝对路径的结果（纵深防御——万一 unalias/unset -f 这对没生效，`command -v`
//     仍会打印别名/函数定义，我们宁丢不交回垃圾），
//  5. 经 `cd ... && pwd -P` 规范化目录，让符号链接前缀目录（fnm/nvm/volta）塌缩成
//     稳定路径，同时保留被调用的命令名，
//  6. 若解析出的路径在 ~/.multica/hooks，就在同一份 shell 展开的 PATH 里找 hooks 目录
//     外的第一个可执行文件，
//  7. 对调用方按 `<name>\t<canonical_path>` 每行一条打印。
//
// 第 2 步为什么重要——以及为什么本 PR 的第一版漏了 #2512：动机案例是 ~/.zshrc 里
// 有 `alias claude=...`，PATH 更深处还有 fnm 的真实 claude 二进制。开了 -i，别名加载，
// `command -v claude` 返回 `claude: aliased to ...`（zsh）或 `alias claude='...'`
// （bash）——都不以 `/` 开头，被第 4 步丢弃，循环再也不会回头找 PATH。在同一个 shell
// 里 unalias 让 `command -v` 回落到 daemon 真正想要的 PATH 搜索。bash/zsh 里 shell
// 函数有同样的遮蔽行为，故 `unset -f`。两个调用都用 `2>/dev/null` 包着，让无害的
// "no such alias" 错误永不进 stderr。
//
// 所有输入名在到达本函数前都经 isSafeAgentName 审查，因此把它们不加引号内联进 for
// 循环词表是安全的。
func buildLoginShellResolveScript(names []string) string {
	var b strings.Builder
	b.WriteString("for n in")
	for _, n := range names {
		b.WriteByte(' ')
		b.WriteString(n)
	}
	b.WriteString("; do\n")
	b.WriteString("  unalias \"$n\" 2>/dev/null\n")
	b.WriteString("  unset -f \"$n\" 2>/dev/null\n")
	b.WriteString("  p=$(command -v \"$n\" 2>/dev/null) || continue\n")
	b.WriteString("  [ -n \"$p\" ] || continue\n")
	b.WriteString("  case \"$p\" in /*) ;; *) continue ;; esac\n")
	b.WriteString("  d=$(dirname \"$p\") && f=$(basename \"$p\") && c=$(cd \"$d\" 2>/dev/null && pwd -P) || continue\n")
	b.WriteString("  hc=\"\"\n")
	b.WriteString("  if [ -n \"${HOME:-}\" ]; then hd=\"$HOME/.multica/hooks\"; hc=$(cd \"$hd\" 2>/dev/null && pwd -P) || hc=\"\"; fi\n")
	b.WriteString("  if [ -n \"$hc\" ] && [ \"$c\" = \"$hc\" ]; then\n")
	b.WriteString("    oldIFS=$IFS; IFS=:\n")
	b.WriteString("    for d2 in $PATH; do\n")
	b.WriteString("      [ -n \"$d2\" ] || d2=.\n")
	b.WriteString("      c2=$(cd \"$d2\" 2>/dev/null && pwd -P) || continue\n")
	b.WriteString("      [ \"$c2\" = \"$hc\" ] && continue\n")
	b.WriteString("      if [ -f \"$c2/$n\" ] && [ -x \"$c2/$n\" ]; then c=\"$c2\"; f=\"$n\"; break; fi\n")
	b.WriteString("    done\n")
	b.WriteString("    IFS=$oldIFS\n")
	b.WriteString("  fi\n")
	b.WriteString("  printf '%s\\t%s\\n' \"$n\" \"$c/$f\"\n")
	b.WriteString("done\n")
	return b.String()
}

// isSafeAgentName 检查 `s` 是否为“只含可安全内联进 shell 脚本的字符”（ASCII 字母、
// 数字、点、横线、下划线）的裸命令名。daemon 随附的 agent 名都满足该谓词；它防的是
// 将来漂移，不是约束运维提供的路径（那些永不进 shell 解析器）。
func isSafeAgentName(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == '.':
		default:
			return false
		}
	}
	return true
}
