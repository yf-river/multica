// Package agent 提供通过各类编码智能体（coding agent）执行提示词（prompt）的统一接口，
// 覆盖 Claude Code、CodeBuddy、Codex、Copilot、OpenCode、DevEco Code、Hermes、Pi、
// Oh-My-Pi、Cursor、Kimi、Reasonix、Kiro、Antigravity、Qoder、Trae、Grok、
// Qwen Code、QwenPaw、MiniMax Code。
//
// 职责定位：上层（daemon / service）不关心具体调的是哪家 CLI——只向 Backend 提交一段
// prompt 与执行选项，然后从统一的 Session 通道里收取结构化事件（文本/思考/工具调用/
// 状态等），最终拿到一个 Result。各家 CLI 的差异（启动参数、输出协议、会话恢复能力、
// 计费上报）都被隔离在各个 backend 实现里。
//
// 本包刻意复刻 happy-cli 的 AgentBackend 模式，但用符合 Go 习惯的方式表达。
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

// Backend 是通过编码智能体执行提示词的统一接口。
//
// 所有 agent 类型（见 New / SupportedTypes）都要实现它，让上层代码无需关心
// 具体跑的是哪家 CLI。
type Backend interface {
	// Execute 执行一段 prompt，返回一个用于流式读取结果的 Session。
	// 调用方可以（可选地）从 Session.Messages 消费事件，并等待
	// Session.Result 拿到最终结果。
	Execute(ctx context.Context, prompt string, opts ExecOptions) (*Session, error)
}

// ExecOptions 配置单次执行（execute）的参数。
type ExecOptions struct {
	EnableTaskSupplement bool   // 已协商的运行内追加输入能力
	Cwd                  string // 工作目录：agent 子进程在哪里启动
	// Command is a runtime-native slash command (for example /plan or /goal).
	// When set, the backend runs it against the session instead of a model turn.
	Command            string
	PlanMode           *bool // 聊天控制面期望的计划模式；nil 表示没有显式选择。
	NativeGoal         bool  // CodeBuddy Goal 会话使用原生控制协议，普通会话沿用 stream-json。
	CommandOnly        bool  // 纯控制命令不启动模型生成。
	RequestInteraction func(context.Context, protocol.InteractionPrompt) (protocol.InteractionAnswer, error)
	Model              string // 期望使用的模型名；留空表示用运行时/CLI 自己的默认模型
	// SystemPrompt 携带 Multica 的运行时简报（runtime brief），仅对少数无法
	// 从磁盘读取该简报的 provider 有效。daemon 对其它所有 provider 都把它留空
	// （见 daemon.providerNeedsInlineSystemPrompt），因为那份简报已经以"每次任务的
	// 上下文文件"形式放在工作目录里了——具体叫 CLAUDE.md、AGENTS.md、CODEBUDDY.md
	// 还是 QWEN.md，取决于运行的是哪个 runtime。
	//
	// 因此 backend 绝不能假设该字段一定有值：新增一个只读 SystemPrompt 的 backend，
	// 会悄无声息地什么也拿不到。
	SystemPrompt              string
	ThreadName                string        // 会话/线程名，用于命名 agent 侧的会话
	MaxTurns                  int           // 最多允许的回合数；0 表示不限
	Timeout                   time.Duration // 整体执行超时（墙钟时间）
	TurnInterruptTimeout      time.Duration // Codex 中断回合并等待计量收尾的预算，默认两秒
	SemanticInactivityTimeout time.Duration // 语义无进展超时：长时间没有任何实质产出的上限
	// FirstTurnNoProgressTimeout 可选地覆盖 Codex 的"首回合无进展"上限——即在
	// app-server 上报 turn/started 之后、看门狗判定失败之前，允许一个回合完全静默的
	// 时长窗口。零值保持 provider 默认行为，也与既有语义一致：SemanticInactivityTimeout
	// 只能收缩这个上限；正值则显式设定它，包括向上放宽。
	// 它回答的问题与 SemanticInactivityTimeout 不同（"进程到底开始产出没有" vs
	// "一个进行中的回合是不是变安静了"），所以两者各自独立变化。
	// 目前只有 codex backend 使用它（GH #3262）。
	FirstTurnNoProgressTimeout time.Duration
	// IdleWatchdogTimeout 可选地针对本次执行收窄 daemon 的通用"长时间无消息"看门狗。
	// 零值沿用 daemon 级窗口；即使设一个大于该窗口的值，也不能突破全局安全上限。
	// daemon 级的零值仍然会让看门狗整体失效；进行中的工具调用继续使用独立的
	// 工具看门狗预算。
	IdleWatchdogTimeout time.Duration
	// HandshakeTimeout 为使用长连接协议传输的 provider 限定启动握手 RPC 的超时时间。
	// 目前由 Codex app-server 消费；零值表示采用 provider 默认值，而不是禁用该约束。
	HandshakeTimeout time.Duration
	// ThreadHandshakeTimeout 可选地为 Codex 更重的 thread/start、thread/resume RPC
	// 提供比 initialize、turn/start 更宽的预算。零值保留给那些显式设置了
	// HandshakeTimeout 的调用方的旧行为；两者都为零时 Codex 使用各自独立的内置默认值。
	ThreadHandshakeTimeout time.Duration
	ResumeSessionID        string // 非空表示要续跑（resume）之前某次 agent 会话
	// ResumeExpected 记录本次任务"本意是要延续一段之前的对话"，与 ResumeSessionID
	// 相互独立（ResumeSessionID 可能被兜底重试清空）。当它为 true、但 backend 最终
	// 落到一个全新线程上时——比如续跑 RPC 被拒绝、或传输故障被迫全新重试——backend
	// 要给出"连续性提示"，而不是静默地从头开始。目前只有 codex backend 支持
	// （MUL-4424）。
	ResumeExpected bool
	// ResumeContinuityNotice 是当 ResumeExpected 成立、但 backend 仍然落到全新线程时，
	// 需要拼接到首个回合开头的提示文本。措辞归调用方负责，因为只有它知道"表面上丢了
	// 什么"：issue 的评论和 Slack 频道的历史仍在、可以重新读回，而网页聊天的和飞书
	// 频道的不可恢复——这个差别决定了 agent 是否应该向用户说明任何事（MUL-5722）。
	//
	// 空字符串表示什么都不说；当调用方自己的 prompt 已经带上了提示时，调用方必须留空。
	// 正是这一条避免同一段话被付两遍钱：daemon 在它已经知道续跑没了的时候注入，
	// backend 只覆盖 daemon 看不见的场景——运行中途续跑 RPC 被拒绝。
	ResumeContinuityNotice string
	// ExtraArgs 只对主动读取它的 backend 生效，其余 backend 忽略。这里故意不逐个
	// 枚举——历史上列出的清单随 backend 增加而过期，MULTICA_QWENPAW_ARGS 就是这样
	// 明明接通了却没人消费。想了解当下的集合请 grep ExtraArgs。
	ExtraArgs        []string        // daemon 级默认 CLI 参数，追加在 CustomArgs 之前
	CustomArgs       []string        // 每个 agent 自己的 CLI 参数，追加在 ExtraArgs 之后
	QwenpawWorkspace string          // 本次任务 qwenpaw 的工作目录（作为 --workspace 传给 qwenpaw acp）；不适用时为空
	McpConfig        json.RawMessage // 非 nil 时，要通过 --mcp-config 传给 agent 的 MCP 服务器配置
	// ThinkingLevel 是运行时原生的"推理/投入程度"取值（例如 Claude 的
	// "low|medium|high|xhigh|max"、Codex 的 "none|minimal|low|medium|high|xhigh"、
	// OpenCode 的模型变体名）。空值表示"用运行时/模型的默认值"——
	// 凡是消费该字段的 backend 都会跳过自己的 --effort / reasoning_effort 注入，
	// 让上游 CLI 自身的默认值生效。目前 claude、codex、opencode、codebuddy、dsh 和
	// grok（ACP 里 `grok agent` 的 `--effort`）这些 backend 支持它；其它 backend 选择
	// 忽略而不报错（这样 MUL-2339 可以增量扩展运行时支持，而不破坏无关的 agent）。
	ThinkingLevel string
	// ServiceTier 是 Codex 原生的执行档位（例如 "priority"，界面显示为 Fast）。
	// "default" 显式选择标准路由；空值表示继承本地 Codex 配置。其它 provider 忽略该字段。
	ServiceTier string
	// ClaudeSettingsPath 是 daemon 拥有、按任务隔离的设置文件，通过 Claude Code 的
	// --settings 参数传入。目前只携带限制性的运行时技能覆盖项；其它 provider 忽略它。
	ClaudeSettingsPath string
	// LifeCognition 标记本次执行是否属于生命认知（life cognition）流水线。
	// Provider 据此套用生命专属的上下文与输出契约。
	LifeCognition bool
}

// runContext 根据配置的单次运行超时，为 agent 子进程派生出执行上下文。
//
// 超时为正数时施加一个硬性的墙钟截止时间；为 0（或负数）时不施加任何截止，
// 存活与否完全交给 daemon 的"无活动看门狗"——这样只要会话还在持续产出事件，
// 就绝不会仅仅因为跑得久而被杀掉（MUL-3064）。
//
// 调用方拥有返回的 CancelFunc，用完必须调用以释放资源。
func runContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout > 0 {
		return context.WithTimeout(ctx, timeout)
	}
	return context.WithCancel(ctx)
}

// Session 表示一次正在运行中的 agent 执行，由两条只读通道构成。
type Session struct {
	WaitingForInput func() bool
	// Supplement delivers an additional human instruction to the currently
	// active provider turn. Nil means the backend cannot safely do so.
	// Adapters bound their own transport calls; a hook-based adapter may wait
	// for the next provider boundary until ctx or the execution is cancelled.
	Supplement func(context.Context, string) error
	// SupplementReady reports whether Supplement currently targets a live,
	// provider-confirmed turn. The daemon must not claim durable input before
	// this becomes true: claiming during provider initialization would convert
	// a transient startup window into a user-visible delivery failure. Nil is
	// fail-closed and means no run-scoped input may be claimed.
	SupplementReady func() bool
	// Messages streams events as the agent works. The channel is closed
	// when the agent finishes (before Result is sent).
	// Messages 流式推送 agent 工作过程中的事件。agent 结束（Result 发出之前）
	// 时该通道会被关闭。
	Messages <-chan Message
	// Result 恰好收到一个值——最终结果——然后关闭。
	Result <-chan Result
}

// MessageType 标识 Message 的种类。
type MessageType string

const (
	MessageText       MessageType = "text"        // 纯文本输出
	MessageThinking   MessageType = "thinking"    // 思考过程（思维链）
	MessageToolUse    MessageType = "tool-use"    // agent 发起一次工具调用
	MessageToolResult MessageType = "tool-result" // 一次工具调用的返回结果
	MessageStatus     MessageType = "status"      // 状态变更（会话 id 等）
	MessageError      MessageType = "error"       // 出错事件
	MessageLog        MessageType = "log"         // 运行时日志
	// MessageRuntime carries runtime-native telemetry (context usage, compaction,
	// subagent lifecycle) as a JSON payload. It is not model-visible content; the
	// daemon persists it as a task message and the run view renders it separately.
	MessageRuntime MessageType = "runtime"
)

// Message 是 agent 在执行期间向外发射的统一事件。
type Message struct {
	Type      MessageType
	Content   string         // 文本内容（Text、Error、Log 用）
	Tool      string         // 工具名（ToolUse、ToolResult 用）
	CallID    string         // 工具调用 ID（ToolUse、ToolResult 用）
	Input     map[string]any // 工具入参（ToolUse 用）
	Output    string         // 工具出参（ToolResult 用）
	Status    string         // agent 状态字符串（Status 用）
	Level     string         // 日志级别（Log 用）
	SessionID string         // backend 会话 id（Status 用），用于尽早钉住恢复指针
}

// TokenUsage 记录单个模型的 token 消耗。
type TokenUsage struct {
	InputTokens      int64 // 输入 token 数
	OutputTokens     int64 // 输出 token 数
	CacheReadTokens  int64 // 命中缓存读入的 token 数
	CacheWriteTokens int64 // 写入缓存的 token 数
	// CostUSDTicks 是 provider 自己声称的本次用量成本，单位为 1e-10 美元的 tick。
	// 0 表示"没有上报"——只有少数 agent 会返回它（例如 xAI Grok Build 通过
	// `_meta.usage.costUsdTicks` 上报）。
	//
	// 它有价值是因为"token 数 × 单价"的估算无法复刻请求级的计价规则：xAI 对 prompt
	// 达到 200K token 的请求按 2 倍计费，而一条用量记录聚合了一个回合里的每一次模型
	// 调用——所以存储的 token 数无法说明某个单条请求命中哪一档。provider 自己的数字
	// 已经把这种档位差异算进去了。
	CostUSDTicks int64
}

// CostUSDTicksPerUSD 是 provider 上报成本单位的换算比例：xAI 上报的是整数的
// 1e-10 美元 tick，用 int64 把"不足一美分"的回合成本精确保存到数据库，
// 避免经过 float64 产生漂移。
const CostUSDTicksPerUSD = 10_000_000_000

// Result 是 agent 会话结束后的最终结果。
type Result struct {
	Status     string                // "completed"、"failed"、"aborted"、"timeout"、"cancelled"
	Output     string                // backend 挑出来的、面向用户的最终输出
	Error      string                // 失败时的错误信息
	DurationMs int64                 // 本次运行耗时（毫秒）
	SessionID  string                // 后端会话 id
	Usage      map[string]TokenUsage // 按模型名索引的 token 用量
	// ProviderTraceID 是本次子进程自己声明的根请求 id（CodeBuddy 为 stream-json
	// 事件上的 `_requestId`），不是父进程环境变量里继承来的值。与外部观测系统的
	// trace/span 对应需另行验证。一次 CLI 进程
	// 只对应一个根请求；续跑会话会换新的根请求，所以它是"每次执行"级别的标识，
	// 不能挂在会话或 issue 上。为空表示该 backend 不暴露，或本次运行没有产生请求。
	ProviderTraceID string
	// SDKTraceID 是 SDK 自己日志里、属于本轮请求的那条 trace id（取自记录
	// providerData.traceId）。它与 ProviderTraceID（子进程自报的根请求 id）是两次
	// 独立取值，正常情况下本就不相等；它与外部观测系统（AgentLens）trace/span 的
	// 对应关系尚未实测，不能假定相同或不同。只有本轮 CID 在 SDK 日志里能唯一定位
	// 到一条 trace id 时才填，其余留空。
	SDKTraceID string
	// SDKTraceSourceSHA256 是那份 SDK 日志文件快照的 sha256，用来复核"当时读到的
	// 就是这一份文件"，而不是事后被改写的文件。
	SDKTraceSourceSHA256 string
	// SDKTraceSourcePath 是上报字段里的来源标记（固定 providerData.traceId），说明 SDK id
	// 取自记录中的哪个字段；它**不是**本地文件路径。真实文件路径只进日志与私有证据。
	SDKTraceSourcePath string
	// ExtraTraces 是本轮产生的、父链路之外的子 Agent 调用链路。一次执行 0:N，
	// 每条有自己的 CID/session/SDK trace，不能并进父链路，也不能算成同一次请求。
	ExtraTraces []SubagentTrace
	// ResumeRejected 是"本次运行请求的续跑被永久拒绝"的确凿证据——要么会话记录
	// 已经没了，要么该会话属于另一个 provider 账号，要么会话还在但它的历史已经无法
	// 再重放给 provider（例如 GH #5975：存储的图片现在超过了 provider 的最大尺寸，
	// 导致每次续跑会话/prompt 在回合真正运行前就被拒绝）。这些情况的共同点是：
	// 续跑不可能继续，只有从头再来才能解决——所以它是 daemon 的"全新会话兜底"
	// 优先检查的信号。注意最后一种情况下 SessionID 仍然非空（id 是真的，只是它的
	// 历史不可用）——daemon 以这个布尔值为准，而不是以 id 是否为空为准。
	//
	// false 并不等于"反面的证据"。对列入 ResumeRejectionUndetectable 的 backend，
	// 它表示"分辨不出来"；对其它 backend 它表示"查过了，这不算拒绝"。daemon
	// 需要知道 provider 名才能区分这两种情况——见 internal/daemon 里的
	// shouldRetryWithFreshSession。
	//
	// backend 绝不能为"换新会话也治不好的失败"设置它：网络中断、限流、配额、
	// provider 5xx、鉴权错误等。这些失败要保留会话指针，让平台自己的重试可以续跑
	// 被截断的对话（见 internal/service/task.go 里的 retryableReasons）。
	//
	// 上面"鉴权错误排除在外"的规则，连那种"换新会话确实能治好"的鉴权错误也成立——
	// 即续跑的会话其持久化的 provider 身份再也解析不出凭据的情况（GH #6777）。
	// 仅凭报错信息，adapter 无法把它和真正的坏凭据区分开，所以这个判断只在
	// shouldRetryWithFreshSession 里做一次、且与 provider 无关——那里已经知道
	// "这次运行是不是续跑"了。不要在本字段编码这个判断。
	ResumeRejected bool
	// ResumeRejectedTransient 是"本次运行请求的续跑此刻无法继续，但会话本身仍然
	// 健康"的确凿证据。daemon 这一回合可以用全新会话，但绝不能把这个请求的会话
	// 从以后的查询里退役。backend 不能把它与 ResumeRejected 同时置位；
	// 后者表示该会话已永久不可用。
	ResumeRejectedTransient bool
	// codexInitializeRetrySafe 是 provider 内部使用的证据：在语义活动开始前、
	// 且进程树已被回收之后，发生过一次 initialize 超时。
	// 它故意不进入公开的结果契约。
	codexInitializeRetrySafe bool
	// codexStartupRefreshRetrySafe 是 provider 内部使用的证据：首个回合没有产生
	// 任何语义进展，是因为 Codex 加载不了自己的模型目录，并且事后进程树已被回收。
	// 与 codexInitializeRetrySafe 一样，它不属于公开契约。
	codexStartupRefreshRetrySafe bool
}

// Config 配置一个 Backend 实例。
type Config struct {
	ExecutablePath string            // CLI 可执行文件路径
	CLIVersion     string            // 与 ExecutablePath 配套检测到的版本；内置提供方按已发布协议归一化用量
	Env            map[string]string // 额外的环境变量
	Logger         *slog.Logger
	TaskID         string
	RuntimeID      string
	DaemonVersion  string
	CodexVersion   string
	// BuiltinRuntime 表示 ExecutablePath 是 provider 自己发现的内置二进制，
	// 而不是某个自定义运行时 profile 的命令。自定义 profile 仍以其协议族
	// （protocol_family）作为 provider 身份，所以仅靠 provider 名区分不了两者：
	// 例如 `protocol_family: hermes` 配上 `command_name: jcode`，会以 "hermes"
	// 出现，实现上却是毫不相关的另一个东西。backend 用它来界定"仅针对某厂商
	// 特定二进制验证过"的兼容性例外；默认 false，让未设置该字段的调用方以
	// 保守失败的方式落到标准行为上。
	BuiltinRuntime bool
	// provider 是安全启动日志里使用的"运行时/provider 身份"。New 从协议族
	// 填它；NewRuntime 则保留具体的内置运行时身份（例如 omp 而不是 pi）。
	provider string
	// LaunchPrefix 是属于 ExecutablePath 自身的 argv 前缀——即自定义运行时
	// profile 的 fixed_args。它被直接拼在可执行文件之后、backend 构建的每个
	// 参数之前，因为包装器（wrapper）的子命令必须先被消费掉，被包装 CLI 自己的
	// 参数才有意义（如 `ccms start q36` 之后才能接 `-p …`，GH #7046）。
	//
	// 与 ExtraArgs 不同，它不是"opt-in"：New 只过滤一次，之后 Command 边界会
	// 把它应用到本包派生的每个进程上，无论是任务启动还是 CLI 探测。backend
	// 从不直接读取它。
	LaunchPrefix []string
}

// SupportedTypes 是"可以用来支撑自定义运行时 profile"的 agent 类型官方白名单。
// 它必须与数据库里 runtime_profile.protocol_family 的 CHECK 约束保持同步
// （migration 120 建立，migration 134 加 qoder、136 加 traecli、175 加 deveco、
// 179 加 grok、202 加 qwen、242 加 qoderclicn、253 加 qwenpaw、254 加 reasonix、
// 313 加 dsh、342 加 mcode、370 加 dim、403 加 zeroclaw、441 加 codearts）：
// 自定义运行时 profile 只能建立在 Multica 官方支持的 backend 之上。
//
// 背景说明：qoder 与 qoderclicn 共享同一个 ACP backend；保留两个 provider key，
// 让 daemon 能各自独立地自动发现并注册国际版与中国版二进制。traecli（Trae）本来
// 就有 New backend、启动头与 provider 品牌，却一直漏在白名单外，导致按协议族
// 挑选时把它拒了（#4945）。grok 是 xAI Grok Build CLI 的 ACP backend
// （`grok agent --always-approve stdio`）。qwen 是 Qwen Code 原生的
// `qwen -p <prompt> --output-format stream-json` backend。
var SupportedTypes = []string{
	"claude",
	"codebuddy",
	"codex",
	"copilot",
	"opencode",
	"codearts",
	"deveco",
	"hermes",
	"pi",
	"cursor",
	"kimi",
	"reasonix",
	"dsh",
	"kiro",
	"antigravity",
	"qoder",
	"qoderclicn",
	"traecli",
	"grok",
	"qwen",
	"qwenpaw",
	"mcode",
	"dim",
	"zeroclaw",
}

// IsSupportedType 报告 agentType 是否在 SupportedTypes 白名单里。
// 在持久化或注册自定义运行时 profile 的 protocol_family 之前，用它做合法性校验。
func IsSupportedType(agentType string) bool {
	for _, t := range SupportedTypes {
		if t == agentType {
			return true
		}
	}
	return false
}

// resumeRejectionUndetectable 列出那些完全无法产出 Result.ResumeRejected 的
// backend。它们从流式输出里抠出 SessionID，但没有拒绝检测手段：没有短语匹配、
// 没有结构化错误码、没有内部重启。copilot 自己的注释记录了这个窟窿（session.error
// 在 session.start 之前到达会把 SessionID 留空）；antigravity 的会话 id 读取器在
// CLI 还没派发会话就退出时一律返回空字符串。
//
// 这里的成员名单是刻意"opt-in"的。不在本 map 里的 backend 一律视为有能力检测，
// 这样新 backend 采用保守失败策略——它不报拒绝、也就得不到兜底重试——而不是静默
// 继承一套靠猜的重试。某个 backend 一旦学会上报拒绝，就立刻把它的条目删掉。
var resumeRejectionUndetectable = map[string]bool{
	"antigravity": true,
	"copilot":     true,
	"cursor":      true,
	"deveco":      true,
	"opencode":    true,
	"codearts":    true,
}

// ResumeRejectionUndetectable 报告 agentType 是否为"无法把被拒绝的续跑与其它启动
// 失败区分开"的 backend。调用方用它来正确解读 Result.ResumeRejected 的 false：
// 对这些 backend 表示"分辨不出来"，对其它所有 backend 表示"查过了，不算拒绝"。
func ResumeRejectionUndetectable(agentType string) bool {
	return resumeRejectionUndetectable[agentType]
}

// New 为给定的 agent 类型创建一个 Backend 实例；支持的 agent 类型见
// SupportedTypes 白名单。agentType 不在白名单内时返回错误。
func New(agentType string, cfg Config) (Backend, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.provider == "" {
		cfg.provider = agentType
	}
	// 在这里——唯一同时知道前缀和协议族的地方——过滤启动前缀。如果放到各个
	// backend 里各自做，就会重蹈 ExtraArgs 那种"opt-in 被遗忘"的覆辙：某个协议族
	// 忘了调用，就会收下 fixed_args 里的 `--output-format text`，把它自己的
	// stream-json 输出通道弄坏。
	cfg.LaunchPrefix = filterLaunchPrefix(cfg.LaunchPrefix, agentType, cfg.Logger)

	switch agentType {
	case "claude":
		return &claudeBackend{cfg: cfg}, nil
	case "codebuddy":
		return &codebuddyBackend{cfg: cfg}, nil
	case "codex":
		return &codexBackend{cfg: cfg}, nil
	case "copilot":
		return &copilotBackend{cfg: cfg}, nil
	case "opencode":
		return &opencodeBackend{cfg: cfg}, nil
	case "codearts":
		return newCodeArtsBackend(cfg)
	case "deveco":
		return &devecoBackend{cfg: cfg}, nil
	case "hermes":
		return &hermesBackend{cfg: cfg}, nil
	case "pi":
		return &piBackend{cfg: cfg}, nil
	case "cursor":
		return &cursorBackend{cfg: cfg}, nil
	case "kimi":
		return &kimiBackend{cfg: cfg}, nil
	case "reasonix":
		return &reasonixBackend{cfg: cfg}, nil
	case "dsh":
		return &dshBackend{cfg: cfg}, nil
	case "dim":
		return &dimBackend{cfg: cfg}, nil
	case "kiro":
		return &kiroBackend{cfg: cfg}, nil
	case "antigravity":
		return &antigravityBackend{cfg: cfg}, nil
	case "qoder", "qoderclicn":
		return &qoderBackend{cfg: cfg, defaultExecutable: qoderDefaultBinary(agentType)}, nil
	case "traecli":
		return &traecliBackend{cfg: cfg}, nil
	case "grok":
		return &grokBackend{cfg: cfg}, nil
	case "qwen":
		return &qwenBackend{cfg: cfg}, nil
	case "qwenpaw":
		return &qwenpawBackend{cfg: cfg}, nil
	case "mcode":
		return &mcodeBackend{cfg: cfg}, nil
	case "zeroclaw":
		return &zeroclawBackend{cfg: cfg}, nil
	default:
		return nil, fmt.Errorf("unknown agent type: %q (supported: %s)", agentType, strings.Join(SupportedTypes, ", "))
	}
}

// DetectVersion 运行 agent CLI 的 --version 并返回其输出。
//
// cmd 携带运行时的启动前缀，所以自定义 profile 是以"它真正被启动的方式"来探测的：
// `ccms start q36 --version` 报的是包装器实际 exec 的那个 CLI 的版本；而裸的
// `ccms --version` 只会报包装器自己的版本，把运行时钉到错误的兼容性策略上。
func DetectVersion(ctx context.Context, cmd Command) (string, error) {
	return detectCLIVersion(ctx, cmd)
}

// launchHeaders 把每种受支持的 agent 类型映射到"追加任何 custom_args 之前"、
// daemon 实际会启动的用户可见命令骨架。这里刻意只保留最小形态——只有命令 +
// 子命令（没有子命令时用一段简短的模式标签）。内部参数、传输值与环境变量一律
// 不出现，让这串字符只提示用户"你在扩展什么命令"，而不是把整条命令行倾倒出来。
var launchHeaders = map[string]string{
	"antigravity": "agy -p (non-interactive)",
	"claude":      "claude (stream-json)",
	"codebuddy":   "codebuddy (stream-json)",
	"codex":       "codex app-server",
	"copilot":     "copilot (json)",
	"cursor":      "cursor-agent (stream-json)",
	"codearts":    "codearts run (json)",
	"deveco":      "deveco run (json)",
	"hermes":      "hermes acp",
	"kimi":        "kimi acp",
	"reasonix":    "reasonix acp",
	"dsh":         "dsh --profile multica (stdio)",
	"kiro":        "kiro-cli acp",
	"opencode":    "opencode run (json)",
	"pi":          "pi (json mode)",
	"qoder":       "qodercli --acp",
	"qoderclicn":  "qoderclicn --acp",
	"traecli":     "traecli acp serve",
	"grok":        "grok agent stdio",
	"qwen":        "qwen -p (stream-json)",
	"qwenpaw":     "qwenpaw acp",
	"dim":         "dim acp",
	"mcode":       "mcode acp",
	"zeroclaw":    "zeroclaw acp",
}

// LaunchHeader 返回 agentType 的用户可见启动骨架；类型未知时返回空字符串。
// 调用方把它渲染成预览，让用户明白自己的 custom_args 会被追加到哪条命令后面。
func LaunchHeader(agentType string) string {
	if h := launchHeaders[agentType]; h != "" {
		return h
	}
	// 内置运行时身份的启动头来自其描述符（descriptor），而不是协议族映射表。
	if desc, ok := BuiltinRuntimeByID(agentType); ok {
		return desc.LaunchHeader
	}
	return ""
}
