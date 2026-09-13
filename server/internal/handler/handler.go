// Package handler 是整个 HTTP API 的处理器集合。
//
// 本文件（handler.go）是 handler 包的"地基"：定义 Handler 主结构体
// （聚合数据库、事件总线、daemon、各类服务与限流器等依赖）、New 构造函数，
// 以及跨文件共享的辅助工具（JSON 响应、UUID 解析/转换、演员身份解析、
// workspace 成员/角色校验、issue/agent/inbox 资源装载等）。其余文件按
// 资源划分实现具体接口。
package handler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/cloudruntime"
	"github.com/multica-ai/multica/server/internal/daemonws"
	"github.com/multica-ai/multica/server/internal/entitlement"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	composio "github.com/multica-ai/multica/server/internal/integrations/composio"
	"github.com/multica-ai/multica/server/internal/integrations/dingtalk"
	"github.com/multica-ai/multica/server/internal/integrations/ghsnapshot"
	"github.com/multica-ai/multica/server/internal/integrations/lark"
	"github.com/multica-ai/multica/server/internal/integrations/slack"
	"github.com/multica-ai/multica/server/internal/integrations/telegram"
	"github.com/multica-ai/multica/server/internal/integrations/wecom"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/realtime"
	"github.com/multica-ai/multica/server/internal/seatcapacity"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/storage"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/internal/util/secretbox"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/featureflag"
	"github.com/multica-ai/multica/server/pkg/llm"
)

// randomID 生成随机 16 字节的十六进制字符串，用作各类内存存储
// （模型列表、本地技能、CLI 更新等）的请求 ID。
func randomID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// txStarter 抽象"开启数据库事务"的能力，Handler 用它包裹需要原子性的写操作；
// 生产环境由连接池实现，测试可注入替身。
type txStarter interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// dbExecutor 是直接执行 SQL 所需的最小数据库接口，覆盖
// Exec / Query / QueryRow 三种操作，便于测试替换真实连接。
type dbExecutor interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Config 是启动时注入 handler 的运行配置。多数字段与环境变量一一对应，
// 由 cmd/server 解析后传入；各字段"何时为空、为空时行为如何"都写在
// 对应字段的注释里。
type Config struct {
	AllowSignup         bool
	AllowedEmails       []string
	AllowedEmailDomains []string
	// DisableWorkspaceCreation 为 true 时，POST /api/workspaces 对所有调用者
	// 一律返回 403，且没有任何角色/owner 例外——仓库里没有"平台管理员"概念。
	// 运维先用该 flag 关闭的状态引导创建首个 workspace，再打开它并重启，
	// 此后新用户只能通过邀请加入。/api/config 会透出该 flag，前端据此隐藏
	// 所有"创建工作区"入口——见 #3433。
	DisableWorkspaceCreation bool
	// VCSIntegrationEnabled 在部署层面开关自托管 Git 集成
	// （Forgejo / Gitea / GitLab），与是否配置 MULTICA_VCS_SECRET_KEY 无关。
	// 它是产品边界：该特性只面向自托管版 Multica（Multica 与 Git 实例可共享
	// 网络），托管云上保持关闭——connect / rotate / webhook 处理器在它为 false
	// 时直接拒绝，/api/config 也不返回它，前端因此隐藏整个区块，而不是给云上
	// 用户展示其无法处理的"缺少 key"提示。由 MULTICA_VCS_INTEGRATION_ENABLED
	// 填充；自托管 compose 默认开启。
	VCSIntegrationEnabled bool
	// PublicURL 是公网访问 API 的绝对基础 URL，不带尾部斜杠
	// （如 "https://multica.ai"）。用于拼接 webhook_url 响应与固定的 Remote
	// MCP OAuth 回调地址——绝不用于判定请求身份、路由或 workspace 归属。
	// 未设置时为空，webhook 客户端可回退到自身 origin，OAuth Connect 则会
	// 直接失败（provider 要求精确的回调地址）。刻意不从请求头（Host /
	// X-Forwarded-Host）读取公网主机，以免配错的反向代理诱导服务器铸出指向
	// 攻击者主机的 webhook URL。
	PublicURL string
	// AppURL 是浏览器应用的规范 origin，从 MULTICA_APP_URL 解析（缺省回退到
	// FRONTEND_ORIGIN）。与 PublicURL 分开，因为 app/API 分离部署时二者
	// 的 origin 不同。
	AppURL string
	// TrustedProxies 是来源 IP 受信、可携带 X-Forwarded-For / X-Real-IP 的
	// CIDR 列表。为空表示"谁也不信"：限流器只用 r.RemoteAddr。由环境变量
	// MULTICA_TRUSTED_PROXIES 注入（逗号分隔 CIDR，如 "10.0.0.0/8,
	// 127.0.0.1/32"）。专门用于防止在没有剥离 XFF 头的反向代理时，
	// 按 IP 的 webhook 限流器被伪造的 XFF 绕过。
	TrustedProxies []netip.Prefix
	// CloudURL 设置后启用仅 SaaS 的 multica-cloud 连接。为空时自托管部署
	// 保持显式：Cloud 端点返回 503，而不是尝试拨一个硬编码的私服地址。
	CloudURL                 string
	CloudTimeout             time.Duration
	AttachmentDownloadMode   string
	AttachmentDownloadURLTTL time.Duration
	// AttachmentFrameAncestors 是允许用 iframe 内嵌附件预览响应的受信
	// 浏览器 origin 列表。生产环境应镜像前端/CORS 的 origin 白名单，
	// 让 app/API 分离部署的自托管环境能在自己页面内嵌 API 托管的 PDF，
	// 同时不放行任意第三方站点来 iframe。
	AttachmentFrameAncestors []string
	// PluginSurfaceOrigin 是专用、无 cookie 的浏览器 origin，负责把
	// /plugin-surfaces/* 路由回本服务。它绝不能是 app/API 的 origin：
	// 该路由会把存储的第三方 JavaScript 作为 HTML 返回。
	PluginSurfaceOrigin string
	// LLM* 系列配置基础 LLM API 层（MUL-4238），支撑 pkg/llm 里服务端
	// 内部的 LLM 辅助（如会话标题生成）。通用 OpenAI 兼容透传端点已在
	// MUL-4309 移除，LLM 只限内部使用。LLMAPIKey 与 LLMBaseURL 都为空时
	// 该层被禁用，调用方静默回退（见 maybeGenerateChatTitleAsync）。
	//   - LLMAPIKey       -> MULTICA_LLM_API_KEY
	//   - LLMBaseURL       -> MULTICA_LLM_BASE_URL（OpenAI 或任意兼容网关）
	//   - LLMDefaultModel  -> MULTICA_LLM_DEFAULT_MODEL（请求省略 `model` 时使用）
	//   - LLMMaxRetries    -> MULTICA_LLM_MAX_RETRIES（传输层重试预算）
	LLMAPIKey       string
	LLMBaseURL      string
	LLMDefaultModel string
	// LLMMaxRetries 是解析后的 MULTICA_LLM_MAX_RETRIES 预算。nil 表示未设置
	// （按 llm.DefaultMaxRetries 处理）；llm.Retries(0) 表示关闭重试。该类型
	// 自带校验：只能通过 llm.Retries 构造，cmd/server 还会在启动时对越界值
	// 直接失败。完整语义见 llm.Config.MaxRetries。
	LLMMaxRetries *llm.RetryOverride
	// ServerVersion 是当前 API 二进制的构建版本（main.go 经 -X main.version
	// 注入、并在 /metrics 上报的那个值）。经 /api/config 暴露，便于自托管
	// 运维确认线上部署的是哪个构建。开发构建为空。
	ServerVersion string
}

// cloudRuntimeProxy 抽象到 multica-cloud 后端的调用：Enabled 报告是否已配置
// CloudURL，Do 转发一次 Cloud 请求。便于测试注入替身。
type cloudRuntimeProxy interface {
	Enabled() bool
	Do(ctx context.Context, req cloudruntime.Request) (*cloudruntime.Response, error)
}

// RuntimeProfileRefreshNotifier 通知 daemon：某 workspace/运行时的 profile
// 已变化，需要刷新。
type RuntimeProfileRefreshNotifier interface {
	NotifyRuntimeProfilesChanged(workspaceID, profileID string)
}

// WorkspaceSetRefreshNotifier 通知 daemon：某用户可访问的 workspace 集合已
// 变化，需要刷新其 workspace 列表。
type WorkspaceSetRefreshNotifier interface {
	NotifyWorkspacesChanged(userID string)
}

// DaemonPendingWorkNotifier 向 daemon 推送"现在该心跳了"的提示，使排队在
// 心跳里捎带处理的请求（模型发现、能力发现、本地技能导入）立刻被处理，
// 而不是等 daemon 的下一次定时心跳（MUL-5444）。
// 由 *daemonws.Hub（单节点）与 *daemonws.RelayNotifier（多节点，经 Redis
// 扇出）共同实现。
type DaemonPendingWorkNotifier interface {
	NotifyPendingWork(runtimeID, kind string)
}

// Handler 是所有 HTTP 处理器共享的结构体：把路由层需要的依赖（数据库、
// 事件总线、实时推送、daemon 连接、各类服务与缓存、限流器、外部集成等）
// 聚合到一处，各资源 handler 以它的方法形式组织在各自文件里。
type Handler struct {
	Queries                *db.Queries
	DB                     dbExecutor
	TxStarter              txStarter
	Hub                    *realtime.Hub
	DaemonHub              *daemonws.Hub
	DaemonProfileRefresh   RuntimeProfileRefreshNotifier
	DaemonWorkspaceRefresh WorkspaceSetRefreshNotifier
	Bus                    *events.Bus
	TaskService            *service.TaskService
	PluginService          *service.PluginService
	IssueService           *service.IssueService
	AutopilotService       *service.AutopilotService
	// Entitlements 提供按 workspace 划分的商业能力开关。nil provider 保持
	// 自托管行为，不做额外读取。
	Entitlements entitlement.Provider
	// SeatCapacity 执行 Cloud 侧"预购人类席位"协议。nil 或被禁用时保持
	// 自托管行为。
	SeatCapacity          seatcapacity.Executor
	SeatCapacityLocker    seatcapacity.WorkspaceLocker
	SeatCapacityWorker    *seatcapacity.Worker
	EmailService          *service.EmailService
	UpdateStore           UpdateStore
	ModelListStore        ModelListStore
	LocalSkillListStore   LocalSkillListStore
	LocalSkillImportStore LocalSkillImportStore
	FeatureFlags          *featureflag.Service
	// IssueStatusCatalog 读取 workspace 的状态目录。默认是 Queries；
	// 测试可换成计数包装器来断言一次请求到底做了多少次目录读取——这是区分
	// 当前"一次读取推导"与其替换掉的 N+1 读取的唯一可观察性质（MUL-6243）。
	IssueStatusCatalog issuestatus.Querier
	LivenessStore      LivenessStore
	HeartbeatScheduler HeartbeatScheduler
	Storage            storage.Storage
	ExternalCredentialBox *secretbox.Box
	CFSigner           *auth.CloudFrontSigner
	Analytics          analytics.Client
	// DaemonPendingWork 给排队的心跳捎带请求推送"现在心跳"提示（MUL-5444）。
	// 可选：为 nil 时 requestDaemonPendingWork 回退到本地 DaemonHub——
	// 对单节点部署来说这正是正确的投递范围。
	DaemonPendingWork DaemonPendingWorkNotifier
	// ModelCatalogCache 提供某运行时"最近一次已知可用模型列表"，让模型选择器
	// 不必等 daemon 往返即可渲染（stale-while-revalidate，MUL-5444）。
	// Nil 安全：所有调用点都把 nil 缓存视为永久未命中，回退到完整发现流程。
	ModelCatalogCache ModelCatalogCache
	// Metrics 是 main.go 构建的业务指标收集器。测试或未启用指标监听的
	// 自托管环境中可能为 nil；所有 Record* 方法都是 nil 安全的，
	// obsmetrics.RecordEvent 把 nil Metrics 当作"仅 PostHog"。
	Metrics                      *obsmetrics.BusinessMetrics
	PATCache                     *auth.PATCache
	DaemonTokenCache             *auth.DaemonTokenCache
	MembershipCache              *auth.MembershipCache
	WebhookRateLimiter           WebhookRateLimiter
	WebhookIPRateLimiter         WebhookRateLimiter
	WebhookAbsoluteIPRateLimiter WebhookRateLimiter
	InvitationRateLimiters       InvitationRateLimiters
	WebhookDeliveryWorker        *WebhookDeliveryWorker
	CloudRuntime                 cloudRuntimeProxy
	// Lark 相关集成。MULTICA_LARK_SECRET_KEY 未设置时下面三个字段全为 nil，
	// 对应 HTTP 处理器返回 503——让配置错误的自托管部署暴露明确错误，而不是
	// 静默使用全零 key。在 cmd/server/router.go 中 handler.New 之后接线。
	LarkInstallations *lark.InstallationService
	LarkBindingTokens *lark.BindingTokenService
	// LarkRegistration 负责设备码（device-flow）安装生命周期：在
	// accounts.feishu.cn 开启注册会话、轮询、成功后在同一 DB 事务里写入
	// lark_installation 与安装者的 lark_user_binding。静态 key 未设置或启动
	// 时构造 RegistrationService 失败则为 nil。
	LarkRegistration *lark.RegistrationService
	// LarkAPIClient 是支撑 SendInteractiveCard / PatchInteractiveCard /
	// SendBindingPromptCard / GetBotInfo 的真实传输客户端。设置了
	// MULTICA_LARK_SECRET_KEY 时路由层接上真实 Lark HTTP 客户端；需要
	// no-op 行为的测试可直接换成 `lark.NewStubAPIClient(...)`。UI 用
	// IsConfigured() 决定是否展示安装入口。
	LarkAPIClient lark.APIClient
	// Composio 集成（MUL-3720）。COMPOSIO_API_KEY 未设置时为 nil，
	// composio HTTP 处理器返回 503。在 cmd/server/router.go 中接线。
	Composio *composio.Service
	// ChannelSupervisor 持有每个渠道安装对应的 supervisor goroutine，它们
	// 持有 §4.4 的 WS 租约并驱动各自的 channel.Channel（MUL-3620 把原来
	// 仅限飞书的 Hub 泛化成了这个与渠道无关的引擎）。路由层独立于平台密钥
	// 构造它——它可驱动任意渠道类型，不只是飞书。租约配置不安全或所选
	// Redis 后端启动就绪检查失败时保持 nil；每个平台只在被配置时注册自己的
	// Factory（飞书在设置 MULTICA_LARK_SECRET_KEY 时注册）。路由层不会
	// 调用 Run；进程 owner（main.go）在长生命周期 context 下启动它，优雅停机
	// 期间用 WaitWithTimeout（有界、受 ShutdownTimeout 约束）join，使租约续期
	// 在 DB 健康时干净退出，连接池冻结时也不阻塞进程退出——最坏情况由下一个
	// 副本等满整个 TTL。
	ChannelSupervisor *engine.Supervisor
	// ChannelRouter 是与渠道无关的入站管线（Supervisor 注入到每个 Channel
	// 的共享 handler）。main.go 停机时在 Supervisor 停止投递事件后调用它的
	// Drain，冲刷延迟触发的 run 触发器并 join 进行中的回复 goroutine。
	// 无条件构建（即使没有 Lark 也建）。
	ChannelRouter *engine.Router
	// ChannelMediaReconciler 结算渠道媒体的意图账本（已上传但未绑定对象的
	// 回收）。在具备存储后端的 cmd/server/router.go 中构建；main.go 以独立
	// worker goroutine 启动它。未配置存储后端时为 nil。
	ChannelMediaReconciler *service.ChannelMediaReconciler
	// SlackInstall 负责"自带 Slack 应用"的安装生命周期（注册粘贴的 token /
	// 列表 / 撤销）以及各应用 bot + app token 的静态加密（MUL-3666）。
	// 未设置 MULTICA_SLACK_SECRET_KEY 时为 nil。
	SlackInstall *slack.InstallService
	// SlackBindingTokens 铸造/兑换 "link your Slack account" 提示背后的
	// 用户绑定 token（MUL-3666）。Slack 未配置（MULTICA_SLACK_SECRET_KEY
	// 未设置）时为 nil。
	SlackBindingTokens *slack.BindingTokenService
	// DingTalkInstall 负责"自带 DingTalk 应用"的安装生命周期。
	// 未配置 MULTICA_DINGTALK_SECRET_KEY 时为 nil。
	DingTalkInstall *dingtalk.InstallService
	// DingTalkBindingTokens 铸造并兑换一次性账号绑定 token。
	DingTalkBindingTokens *dingtalk.BindingTokenService
	// SlackHistory 支撑面向 agent 的 `multica chat history` 命令：按需读取
	// 某聊天会话绑定的 Slack 会话内容（MUL-3871）。Slack 未配置时为 nil，
	// 此时 GetChatChannelHistory 报告 "no channel integration"。
	// 未来的平台可用同一个 reader 接口来满足。
	SlackHistory ChatChannelHistoryReader
	// WecomStore 是限定 channel_type='wecom' 的 channel_installation 行上的
	// 读写句柄。nil 禁用 wecom Web-UI 端点（返回 503），并阻止启动时接线
	// 智能机器人 supervisor。
	WecomStore *wecom.Store
	// WecomCredentials 为 WebSocket subscribe 帧解开某 wecom 安装的智能
	// 机器人密钥。nil 禁用 wecom 集成。
	WecomCredentials wecom.CredentialsResolver
	// WecomBindingTokens 铸造/兑换发给首次使用 WeCom 用户的 "link your
	// Multica account" 提示背后的绑定 token（其 aibot userid 是 "T" 开头的
	// 匿名 ID，与真实 userid / 邮箱无关，故必须显式绑定——见
	// wecom/binding.go）。nil 禁用兑换端点（返回 503）及 OutboundReplier
	// 的绑定提示路径。
	WecomBindingTokens WecomBindingRedeemer

	// WecomCredentialProbe 覆盖安装时的连通性控制检查。生产环境为 nil，
	// 使用真实握手探测；测试注入 fake，让安装路径无需真实 socket 即可运行。
	WecomCredentialProbe wecom.CredentialProbe

	// TelegramInstall 负责 Telegram bot 的安装生命周期（注册粘贴的 BotFather
	// token / 列表 / 撤销）及各 bot token 的静态加密。未设置
	// MULTICA_TELEGRAM_SECRET_KEY 时为 nil。
	TelegramInstall *telegram.InstallService
	// TelegramBindingTokens 铸造/兑换 "link your Telegram account" 提示背后的
	// 用户绑定 token。Telegram 未配置时为 nil。
	TelegramBindingTokens *telegram.BindingTokenService
	// TelegramOutbound 拥有异步的终端投递 worker 池。进程 owner 启动并
	// join 它；同步事件总线只入队 EventChatDone 工作。
	TelegramOutbound *telegram.Outbound

	// channelFileDelivery 记录"本部署"里哪些渠道类型能把 agent 产出的文件
	// 送达会话的最后一步。它决定 claim 响应里的 chat_channel_delivers_files，
	// agent 的每轮 prompt 据此把能力说成"执行 `multica attachment upload`"
	// 或"用文字描述这个文件"（daemon/prompt.go）。它不是 brief：brief 是
	// prompt 缓存前缀，而这是每轮临时结论，写进 brief 会让同一会话渲染两份
	// brief（MUL-5377）。
	//
	// 它是部署事实而非渠道类型的属性，这个区别正是它放在这里的原因：
	// 文件能否送达需要两件事——能回头取回已绑定附件的 adapter，以及让它去取
	// 的对象存储。前者是代码属性，后者是配置属性，只有同时接好两者的进程
	// 才知道"二者兼备"。daemon 若只凭渠道类型作答，就会向没有存储的部署
	// 承诺其兑现不了的送达。
	//
	// 启动时由 DeclareChannelFileDelivery 写一次（cmd/server/router.go，
	// 与把存储交给 adapter 的同一分支，二者不会漂移），此后只读。nil 表示
	// 无任何渠道能送达文件——未配置存储的部署正是如此。
	channelFileDelivery map[string]bool
	// LLM 是基础 LLM API 层（MUL-4238）：对 OpenAI Go SDK 的薄封装，支撑
	// 服务端内部的一次性 LLM 辅助（如会话标题生成）。通用透传端点已在
	// MUL-4309 移除，现仅内部使用。恒不为 nil（New 依据 Config 构造）；
	// 未配置时 Enabled() 返回 false，调用方静默回退。
	LLM *llm.Client
	// VCSSecretBox 静态加密/解密各 workspace 的 Git provider 访问 token 与
	// webhook 密钥（Forgejo / Gitea / GitLab）。MULTICA_VCS_SECRET_KEY
	// 未设置时为 nil，此时 connect / webhook 处理器返回 503，让配错的自托管
	// 部署暴露明确错误而不是静默存明文。在 cmd/server/router.go 中 New
	// 之后接线。
	VCSSecretBox *secretbox.Box
	// PluginSurfaceTokens 封存短期有效的 launch claim。nil 禁用 surface
	// launch；启动时用域分离的 MULTICA_PLUGIN_SECRET_KEY 接线。
	PluginSurfaceTokens *secretbox.Box
	// PRRefresh 驱动 PR 卡片用的 GitHub API 快照管线（MUL-5265）：webhook /
	// 页面访问 / TTL 触发 → 认证 GraphQL 抓取 → 按 head-SHA 加锁的原子快照
	// 写入。恒不为 nil，但未配置 GITHUB_APP_ID / GITHUB_APP_PRIVATE_KEY 时
	// 是惰性的（每个触发都是 no-op），特性在无私钥的部署上干净降级。
	// 在 cmd/server/router.go 中 New 之后接线。
	PRRefresh *ghsnapshot.Manager
	cfg       Config
}

// New 组装 Handler 及它依赖的服务/存储/缓存/限流器，是 handler 包的构造
// 入口。它内部会规范化附件下载配置（非法值回退到默认/auto 模式）、构造
// LLM 客户端并在启动日志里报告生效的重试策略。daemonHubs 是可变参数，
// 通常传 0 或 1 个 *daemonws.Hub，用于提供 daemon 侧刷新/通知能力。
func New(queries *db.Queries, txStarter txStarter, hub *realtime.Hub, bus *events.Bus, emailService *service.EmailService, store storage.Storage, cfSigner *auth.CloudFrontSigner, analyticsClient analytics.Client, cfg Config, daemonHubs ...*daemonws.Hub) *Handler {
	var executor dbExecutor
	if candidate, ok := txStarter.(dbExecutor); ok {
		executor = candidate
	}

	if analyticsClient == nil {
		analyticsClient = analytics.NoopClient{}
	}
	if mode, ok := normalizeAttachmentDownloadMode(cfg.AttachmentDownloadMode); ok {
		cfg.AttachmentDownloadMode = string(mode)
	} else {
		slog.Warn("invalid ATTACHMENT_DOWNLOAD_MODE, using auto", "value", cfg.AttachmentDownloadMode)
		cfg.AttachmentDownloadMode = string(attachmentDownloadModeAuto)
	}
	if cfg.AttachmentDownloadURLTTL <= 0 {
		cfg.AttachmentDownloadURLTTL = defaultAttachmentDownloadURLTTL
	}

	var daemonHub *daemonws.Hub
	if len(daemonHubs) > 0 {
		daemonHub = daemonHubs[0]
	}
	var daemonProfileRefresh RuntimeProfileRefreshNotifier
	var daemonWorkspaceRefresh WorkspaceSetRefreshNotifier
	if daemonHub != nil {
		daemonProfileRefresh = daemonHub
		daemonWorkspaceRefresh = daemonHub
	}

	llmClient := llm.New(llm.Config{
		APIKey:       cfg.LLMAPIKey,
		BaseURL:      cfg.LLMBaseURL,
		DefaultModel: cfg.LLMDefaultModel,
		MaxRetries:   cfg.LLMMaxRetries,
	})
	// 把生效的重试策略记进启动日志，让运维只凭日志就能确认一个不听话的
	// 上游会带来什么代价，而不用去猜语义费解的环境变量（MUL-6364）。从
	// 客户端而非 cfg 读取，保证这行不会与 SDK 实际执行的行为漂移。只记
	// 次数与来源枚举——绝不记 key 或 base URL，自托管网关 URL 常内嵌 token。
	llmRetry := llmClient.RetryBudget()
	slog.Info("llm retry policy",
		"max_retries", llmRetry.MaxRetries,
		"source", llmRetry.Source,
		"request_timeout", llmRetry.RequestTimeout,
		"enabled", llmClient.Enabled(),
	)

	taskSvc := service.NewTaskService(queries, txStarter, hub, bus, daemonHub)
	taskSvc.Analytics = analyticsClient
	taskSvc.SourceContextStorage = store
	// 会话跟进建议与自动起标题共用同一个内部 LLM 层。没配置 MULTICA_LLM_*
	// 的部署会拿到禁用状态的客户端，只会关掉该特性而不会报错。
	taskSvc.QuickActions = llmClient
	h := &Handler{
		Queries:                      queries,
		DB:                           executor,
		TxStarter:                    txStarter,
		Hub:                          hub,
		DaemonHub:                    daemonHub,
		DaemonProfileRefresh:         daemonProfileRefresh,
		DaemonWorkspaceRefresh:       daemonWorkspaceRefresh,
		Bus:                          bus,
		TaskService:                  taskSvc,
		PluginService:                service.NewPluginService(queries, txStarter),
		IssueService:                 service.NewIssueService(queries, txStarter, bus, analyticsClient, taskSvc),
		AutopilotService:             service.NewAutopilotService(queries, txStarter, bus, taskSvc),
		EmailService:                 emailService,
		UpdateStore:                  NewInMemoryUpdateStore(),
		ModelListStore:               NewInMemoryModelListStore(),
		ModelCatalogCache:            NewInMemoryModelCatalogCache(),
		LocalSkillListStore:          NewInMemoryLocalSkillListStore(),
		LocalSkillImportStore:        NewInMemoryLocalSkillImportStore(),
		LivenessStore:                NewNoopLivenessStore(),
		HeartbeatScheduler:           NewPassthroughHeartbeatScheduler(queries),
		Storage:                      store,
		CFSigner:                     cfSigner,
		Analytics:                    analyticsClient,
		WebhookRateLimiter:           NewMemoryWebhookRateLimiter(DefaultWebhookRateLimit()),
		WebhookIPRateLimiter:         NewMemoryWebhookIPRateLimiter(DefaultWebhookIPRateLimit()),
		WebhookAbsoluteIPRateLimiter: NewMemoryWebhookAbsoluteIPRateLimiter(DefaultWebhookAbsoluteIPRateLimit()),
		InvitationRateLimiters:       NewMemoryInvitationRateLimiters(DefaultInvitationRateLimits()),
		CloudRuntime: cloudruntime.NewClient(cloudruntime.Config{
			BaseURL: cfg.CloudURL,
			Timeout: cfg.CloudTimeout,
		}),
		LLM: llmClient,
		cfg: cfg,
	}
	h.WebhookDeliveryWorker = NewWebhookDeliveryWorker(h)

	// PR 卡片用的 GitHub API 快照管线（MUL-5265）。无条件构建，但 App 私钥
	// 未配置时惰性运行（每个触发都是 no-op），特性干净降级。main.go 调用
	// h.PRRefresh.Start(ctx) 启动其 worker 池与 TTL 清扫器。
	ghClient, err := ghsnapshot.NewClientFromEnv()
	if err != nil {
		// 私钥格式错误属于运维可处理的问题；管线保持禁用。
		slog.Warn("github: PR snapshot pipeline disabled (invalid App private key)", "err", err)
	}
	h.PRRefresh = ghsnapshot.NewManager(ghClient, queries, txStarter, h.broadcastPRSnapshotApplied)

	return h
}

// writeJSON 把 v 序列化为 JSON 并以 status 状态码写回。它先整体 Marshal 再
// 写入，以便提供准确的 Content-Length 头。
func writeJSON(w http.ResponseWriter, status int, v any) {
	// 提前整体序列化，才能声明准确的 Content-Length：若在 WriteHeader 后
	// 直接流式写 ResponseWriter，net/http 会改用 chunked 传输编码而丢失
	// Content-Length；先缓冲可让客户端（及代理）看到确切的 body 大小。
	body, err := json.Marshal(v)
	if err != nil {
		// 回退到最小、自描述的错误载荷，而不是把客户端晾在一个写了一半的
		// 响应上。
		body = []byte(`{"error":"failed to encode response"}`)
		status = http.StatusInternalServerError
	}
	// 补结尾换行，与 json.Encoder.Encode 历史上追加的换行保持一致。
	body = append(body, '\n')
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// writeMeasuredJSON 与 writeJSON 行为一致，但返回编码后 body 的字节数，
// 供调用方在慢端点诊断中记录 payload 大小。度量的是未压缩 JSON 长度，
// 与传输层压缩无关。
func writeMeasuredJSON(w http.ResponseWriter, status int, v any) (int, error) {
	body, err := json.Marshal(v)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode response")
		return 0, err
	}
	body = append(body, '\n')
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	if _, err := w.Write(body); err != nil {
		return len(body), err
	}
	return len(body), nil
}

// writeError 以 JSON {"error": msg} 形式写一条错误响应。
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// writeErrorCode 是 writeError 加一个稳定的机器可读 code，前端可据此翻译
// 错误，而不是把英文句子直接 toast 给非英文用户。英文句子保留，作为尚未
// 提供翻译文案时的兜底。
func writeErrorCode(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]string{"error": msg, "code": code})
}

// writeRevisionConflict 返回 409：调用方基于旧 revision 加载的资源已被改动，
// 实际 revision 与预期不符，写入应中止。
func writeRevisionConflict(w http.ResponseWriter, resourceType string, resourceID pgtype.UUID, expected, actual int64) {
	writeJSON(w, http.StatusConflict, map[string]any{
		"error":             "resource changed since it was loaded",
		"code":              "revision_conflict",
		"resource_type":     resourceType,
		"resource_id":       uuidToString(resourceID),
		"expected_revision": expected,
		"actual_revision":   actual,
	})
}

// writeEditConflict 返回 409：并发编辑冲突——资源某字段在加载后被改过，
// 基于旧快照的更新被拒绝。
func writeEditConflict(w http.ResponseWriter, resourceType string, resourceID pgtype.UUID) {
	writeJSON(w, http.StatusConflict, map[string]any{
		"error":         "resource field changed since it was loaded",
		"code":          "revision_conflict",
		"resource_type": resourceType,
		"resource_id":   uuidToString(resourceID),
	})
}

// util 包的薄封装。
//
// parseUUID 刻意使用会 panic 的变体：能到这里的 handler 调用点，喂入的
// UUID 要么是 (a) sqlc 从数据库值往返出来的，要么是 (b) 已在上游校验过的
// 原始请求输入。在这里 panic 意味着有未经防护的用户输入串了进来——这是
// 真正的 bug，应当大声暴露（chi 的 middleware.Recoverer 会把它转成 500），
// 而不是用零值 UUID 悄悄污染数据。
//
// 请求边界上未经校验的用户输入请用 parseUUIDOrBadRequest（写 400）——
// 当调用会写数据库时，绝不要直接把裸的 chi.URLParam / body 字符串喂给
// parseUUID。
func parseUUID(s string) pgtype.UUID                { return util.MustParseUUID(s) }
func uuidToString(u pgtype.UUID) string             { return util.UUIDToString(u) }
func textToPtr(t pgtype.Text) *string               { return util.TextToPtr(t) }
func ptrToText(s *string) pgtype.Text               { return util.PtrToText(s) }
func strToText(s string) pgtype.Text                { return util.StrToText(s) }
func timestampToString(t pgtype.Timestamptz) string { return util.TimestampToString(t) }
func timestampToPtr(t pgtype.Timestamptz) *string   { return util.TimestampToPtr(t) }
func timestampToNanoPtr(t pgtype.Timestamptz) *string {
	return util.TimestampToNanoPtr(t)
}
func dateToPtr(d pgtype.Date) *string { return util.DateToPtr(d) }
func uuidToPtr(u pgtype.UUID) *string { return util.UUIDToPtr(u) }

// uuidsToStrings 把 UUID 数组列映射成字符串 ID 列表，跳过 NULL/非法项。
// 无内容可输出时返回 nil（而非空切片），让 `omitempty` JSON 字段干净地
// 不出现（MUL-4195）。
func uuidsToStrings(us []pgtype.UUID) []string {
	if len(us) == 0 {
		return nil
	}
	out := make([]string, 0, len(us))
	for _, u := range us {
		if u.Valid {
			out = append(out, uuidToString(u))
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// uuidStringsOrEmpty 保留"现代权威的空 UUID 数组（[]）"与"旧服务省略该
// 字段"之间的区别。投递回执据此让客户端不会把"投递 0 条评论"误判为
// "回执未知"从而回退到入队时的计划。
func uuidStringsOrEmpty(us []pgtype.UUID) []string {
	out := uuidsToStrings(us)
	if out == nil {
		return []string{}
	}
	return out
}

func int8ToPtr(v pgtype.Int8) *int64 { return util.Int8ToPtr(v) }
func int4ToPtr(v pgtype.Int4) *int32 { return util.Int4ToPtr(v) }
func ptrToInt4(v *int32) pgtype.Int4 { return util.PtrToInt4(v) }

// parseUUIDOrBadRequest 校验来自用户输入的 UUID 字符串（URL 参数、请求体、
// 请求头）。输入非法时写 400 响应并返回 ok=false；调用方必须立刻 return。
//
// 任何坏 UUID 可能到达写查询（DELETE / UPDATE）的地方都应使用它——
// 旧 ParseUUID 的静默零值行为曾造成真实的数据丢失（#1661）。
func parseUUIDOrBadRequest(w http.ResponseWriter, s, fieldName string) (pgtype.UUID, bool) {
	u, err := util.ParseUUID(s)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid "+fieldName)
		return pgtype.UUID{}, false
	}
	return u, true
}

// parseUUIDSliceOrBadRequest 批量校验一组 UUID；任一非法即写 400 并返回
// ok=false，避免解析到一半出错留下残缺结果。
func parseUUIDSliceOrBadRequest(w http.ResponseWriter, ids []string, fieldName string) ([]pgtype.UUID, bool) {
	uuids := make([]pgtype.UUID, len(ids))
	for i, id := range ids {
		u, err := util.ParseUUID(id)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid "+fieldName)
			return nil, false
		}
		uuids[i] = u
	}
	return uuids, true
}

// DeclareChannelFileDelivery 声明"本部署能把 agent 产出的文件送进某
// channelType 会话"。请在启动时、与"把存储交给 adapter"的同一分支调用——
// 关键是一个条件同时产生能力与对外宣告，部署不可能承诺它不具备的送达能力。
//
// 服务开始对外服务后不要再调用：它写入的 map 会在每次 task claim 时被
// 无锁读取。
func (h *Handler) DeclareChannelFileDelivery(channelType string) {
	if channelType == "" {
		return
	}
	if h.channelFileDelivery == nil {
		h.channelFileDelivery = map[string]bool{}
	}
	h.channelFileDelivery[channelType] = true
}

// channelDeliversFiles 为一次 claim 回答 chat_channel_delivers_files。
//
// 任何"不是显式声明"的方向都默认 false：没人声明的渠道、没有对象存储的
// 部署、从未接线的 adapter。false 是安全答案，因为两种回答的代价不同：
// 本可送达却说 false，只丢一次投递（agent 改用文字描述）；送不到却说
// true，agent 会在没有附件的房间里写下"图表已附上"，读者还得四处找。
//
// Web 聊天（空渠道类型）不在此作答：它没有 adapter，也没有所谓最后一步
// ——浏览器直接凭绑定关系渲染附件卡片——prompt 在自己的分支里处理。
func (h *Handler) channelDeliversFiles(channelType string) bool {
	if channelType == "" {
		return false
	}
	return h.channelFileDelivery[channelType]
}

// publish 把领域事件发到事件总线。
func (h *Handler) publish(eventType, workspaceID, actorType, actorID string, payload any) {
	h.Bus.Publish(events.Event{
		Type:        eventType,
		WorkspaceID: workspaceID,
		ActorType:   actorType,
		ActorID:     actorID,
		Payload:     payload,
	})
}

// publishEvent 投递一条信封已写入持久化 outbox 的事件。它刻意绕过任何
// 持久化钩子：重放已提交事件绝不能产生第二条 outbox 记录。
func (h *Handler) publishEvent(event events.Event) {
	if h.Bus != nil {
		h.Bus.PublishRecovered(event)
	}
}

// notifyDaemonWorkspacesChanged 通知 daemon 这些用户的 workspace 集合有变，
// 需要刷新。空 userID 与重复 userID 都会被跳过。
func (h *Handler) notifyDaemonWorkspacesChanged(userIDs ...string) {
	if h.DaemonWorkspaceRefresh == nil {
		return
	}
	seen := make(map[string]struct{}, len(userIDs))
	for _, userID := range userIDs {
		if userID == "" {
			continue
		}
		if _, ok := seen[userID]; ok {
			continue
		}
		seen[userID] = struct{}{}
		h.DaemonWorkspaceRefresh.NotifyWorkspacesChanged(userID)
	}
}

// publishTask 是 publish() 再带一个 TaskID 提示，让实时层把事件路由到
// 单任务作用域而非整个 workspace。
func (h *Handler) publishTask(eventType, workspaceID, actorType, actorID, taskID string, payload any) {
	h.Bus.Publish(events.Event{
		Type:        eventType,
		WorkspaceID: workspaceID,
		ActorType:   actorType,
		ActorID:     actorID,
		TaskID:      taskID,
		Payload:     payload,
	})
}

// publishChat 是 publish() 再带一个 ChatSessionID 提示，让实时层把事件路由
// 到单个聊天会话作用域。
func (h *Handler) publishChat(eventType, workspaceID, actorType, actorID, chatSessionID string, payload any) {
	h.Bus.Publish(events.Event{
		Type:          eventType,
		WorkspaceID:   workspaceID,
		ActorType:     actorType,
		ActorID:       actorID,
		ChatSessionID: chatSessionID,
		Payload:       payload,
	})
}

// isNotFound 报告 err 是否为"查无此行"（pgx.ErrNoRows）。
func isNotFound(err error) bool {
	return errors.Is(err, pgx.ErrNoRows)
}

// isUniqueViolation 报告 err 是否为 PostgreSQL 唯一约束冲突（SQLSTATE 23505）。
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// isCheckViolation 报告 err 是否为 PostgreSQL CHECK 约束违反（SQLSTATE
// 23514）。用于把列级 CHECK 失败翻译成 4xx 而不是笼统的 500。
func isCheckViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23514"
}

// requestUserID 从 X-User-ID 请求头取用户 ID（认证中间件写入）。
func requestUserID(r *http.Request) string {
	return r.Header.Get("X-User-ID")
}

// resolveActor 判定请求来自 agent 进程还是人类成员。
//
// 首要信号：X-Actor-Source 为 "task_token" 表示请求经 `mat_` 任务级 token
// 认证。认证中间件负责设置该头（会先剥离任何客户端自带的值），因此权威
// ——绑定的 (agent_id, task_id) 无法被 agent 进程伪造或剥离。MUL-2600
// 依赖这条路径在仅限 owner 的端点上拒绝 agent 进程流量。
//
// 回退信号（旧 CLI / 成员 token 路径）：请求必须同时带 X-Agent-ID 与合法
// 的 X-Task-ID，且该 task 必须属于声称的 agent，否则一律回退成 "member"。
//
// 单独的 X-Agent-ID 不可信：任何 workspace 成员都能猜到或观察到某个 agent
// 的 UUID，成员自填 X-Agent-ID 就能冒充该 agent、绕过私有 agent 门禁
// （#2359 review）。daemon 总是成对携带两个头，所以要求两者同时满足对
// 合法 agent 调用者没有影响，却封死了冒充路径。
//
// 成功返回 ("agent", agentID)，否则返回 ("member", userID)。
func (h *Handler) resolveActor(r *http.Request, userID, workspaceID string) (actorType, actorID string) {
	if r.Header.Get("X-Actor-Source") == "task_token" {
		// 服务端设置的头——认证中间件也强制从 token 行带出了 X-Agent-ID，
		// 直接信任即可，无需再回查数据库。
		return "agent", r.Header.Get("X-Agent-ID")
	}
	agentID := r.Header.Get("X-Agent-ID")
	if agentID == "" {
		return "member", userID
	}
	taskID := r.Header.Get("X-Task-ID")
	if taskID == "" {
		slog.Debug("resolveActor: X-Agent-ID present but X-Task-ID missing, refusing to trust agent identity", "agent_id", agentID)
		return "member", userID
	}

	agentUUID, err := util.ParseUUID(agentID)
	if err != nil {
		slog.Debug("resolveActor: X-Agent-ID is not a valid UUID, falling back to member", "agent_id", agentID)
		return "member", userID
	}
	// 校验该 agent 确实存在于目标 workspace。
	agent, err := h.Queries.GetAgent(r.Context(), agentUUID)
	if err != nil || uuidToString(agent.WorkspaceID) != workspaceID {
		slog.Debug("resolveActor: X-Agent-ID rejected, agent not found or workspace mismatch", "agent_id", agentID, "workspace_id", workspaceID)
		return "member", userID
	}

	taskUUID, err := util.ParseUUID(taskID)
	if err != nil {
		slog.Debug("resolveActor: X-Task-ID is not a valid UUID, falling back to member", "task_id", taskID)
		return "member", userID
	}
	task, err := h.Queries.GetAgentTask(r.Context(), taskUUID)
	if err != nil || uuidToString(task.AgentID) != agentID {
		slog.Debug("resolveActor: X-Task-ID rejected, task not found or agent mismatch", "agent_id", agentID, "task_id", taskID)
		return "member", userID
	}

	return "agent", agentID
}

// requireUserID 从请求取用户 ID，取不到则写 401 并返回 ok=false。
func requireUserID(w http.ResponseWriter, r *http.Request) (string, bool) {
	userID := requestUserID(r)
	if userID == "" {
		writeError(w, http.StatusUnauthorized, "user not authenticated")
		return "", false
	}
	return userID, true
}

// resolveWorkspaceID 返回该请求的 workspace UUID。委托给
// middleware.ResolveWorkspaceIDFromRequest，让受中间件保护的路由与不受保护
// 的路由（如 /api/upload-file）共享同一套解析逻辑——包括经数据库做
// slug → UUID 的转换。
//
// 请求未带 workspace 标识、或所带 slug 匹配不到任何 workspace 时返回 ""。
func (h *Handler) resolveWorkspaceID(r *http.Request) string {
	return middleware.ResolveWorkspaceIDFromRequest(r, h.Queries)
}

// ctxMember 从 context 取 workspace 成员（由 workspace 中间件放入）。
func ctxMember(ctx context.Context) (db.Member, bool) {
	return middleware.MemberFromContext(ctx)
}

// ctxWorkspaceID 从 context 取 workspace ID（由 workspace 中间件放入）。
func ctxWorkspaceID(ctx context.Context) string {
	return middleware.WorkspaceIDFromContext(ctx)
}

// workspaceIDFromURL 返回请求的 workspace ID：优先 context（中间件解析），
// 回退到 chi URL 参数。
func workspaceIDFromURL(r *http.Request, param string) string {
	if id := middleware.WorkspaceIDFromContext(r.Context()); id != "" {
		return id
	}
	return chi.URLParam(r, param)
}

// workspaceMember 优先返回中间件 context 里的成员；当 handler 被直接调用
// （如测试）而 context 没有成员时，回退到数据库查询。
func (h *Handler) workspaceMember(w http.ResponseWriter, r *http.Request, workspaceID string) (db.Member, bool) {
	if m, ok := ctxMember(r.Context()); ok {
		return m, true
	}
	return h.requireWorkspaceMember(w, r, workspaceID, "workspace not found")
}

// roleAllowed 判断 role 是否命中 roles 白名单之一。
func roleAllowed(role string, roles ...string) bool {
	for _, candidate := range roles {
		if role == candidate {
			return true
		}
	}
	return false
}

// countOwners 统计成员列表中 role 为 owner 的人数。
func countOwners(members []db.Member) int {
	owners := 0
	for _, member := range members {
		if member.Role == "owner" {
			owners++
		}
	}
	return owners
}

// getWorkspaceMember 查某用户在 workspace 里的成员记录。
func (h *Handler) getWorkspaceMember(ctx context.Context, userID, workspaceID string) (db.Member, error) {
	userUUID, err := util.ParseUUID(userID)
	if err != nil {
		return db.Member{}, err
	}
	wsUUID, err := util.ParseUUID(workspaceID)
	if err != nil {
		return db.Member{}, err
	}
	return h.Queries.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{
		UserID:      userUUID,
		WorkspaceID: wsUUID,
	})
}

// requireWorkspaceMember 要求请求者是该 workspace 的成员；校验不过时写
// 错误响应并返回 ok=false。notFoundMsg 是成员不存在的 404 文案。
func (h *Handler) requireWorkspaceMember(w http.ResponseWriter, r *http.Request, workspaceID, notFoundMsg string) (db.Member, bool) {
	if workspaceID == "" {
		writeError(w, http.StatusBadRequest, "workspace_id is required")
		return db.Member{}, false
	}

	userID, ok := requireUserID(w, r)
	if !ok {
		return db.Member{}, false
	}

	member, err := h.getWorkspaceMember(r.Context(), userID, workspaceID)
	if err != nil {
		writeError(w, http.StatusNotFound, notFoundMsg)
		return db.Member{}, false
	}

	return member, true
}

// requireWorkspaceRole 在 requireWorkspaceMember 基础上额外要求成员角色命中
// roles 白名单，否则写 403。
func (h *Handler) requireWorkspaceRole(w http.ResponseWriter, r *http.Request, workspaceID, notFoundMsg string, roles ...string) (db.Member, bool) {
	member, ok := h.requireWorkspaceMember(w, r, workspaceID, notFoundMsg)
	if !ok {
		return db.Member{}, false
	}
	if !roleAllowed(member.Role, roles...) {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return db.Member{}, false
	}
	return member, true
}

// isWorkspaceEntity 按 userType 判断某 user_id 是否属于给定 workspace
// （userType 为 member 时查成员表，agent 时查 agent 表）。
func (h *Handler) isWorkspaceEntity(ctx context.Context, userType, userID, workspaceID string) bool {
	switch userType {
	case "member":
		_, err := h.getWorkspaceMember(ctx, userID, workspaceID)
		return err == nil
	case "agent":
		userUUID, err := util.ParseUUID(userID)
		if err != nil {
			return false
		}
		wsUUID, err := util.ParseUUID(workspaceID)
		if err != nil {
			return false
		}
		_, err = h.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{
			ID:          userUUID,
			WorkspaceID: wsUUID,
		})
		return err == nil
	default:
		return false
	}
}

func (h *Handler) loadIssueForUser(w http.ResponseWriter, r *http.Request, issueID string) (db.Issue, bool) {
	if _, ok := requireUserID(w, r); !ok {
		return db.Issue{}, false
	}

	workspaceID := h.resolveWorkspaceID(r)
	if workspaceID == "" {
		writeError(w, http.StatusBadRequest, "workspace_id is required")
		return db.Issue{}, false
	}

	// 先试标识符格式（如 "JIA-42"）。resolveIssueByIdentifier 对非标识符
	// 字符串静默返回 false，落到下面的 UUID 路径。
	if issue, ok := h.resolveIssueByIdentifier(r.Context(), issueID, workspaceID); ok {
		return issue, true
	}

	issueUUID, err := util.ParseUUID(issueID)
	if err != nil {
		// 不是合法 UUID 也不匹配标识符格式 → 404（与旧版静默零值行为
		// 一致，那同样会得到 404）。
		writeError(w, http.StatusNotFound, "issue not found")
		return db.Issue{}, false
	}
	wsUUID, err := util.ParseUUID(workspaceID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid workspace_id")
		return db.Issue{}, false
	}
	issue, err := h.Queries.GetIssueInWorkspace(r.Context(), db.GetIssueInWorkspaceParams{
		ID:          issueUUID,
		WorkspaceID: wsUUID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "issue not found")
		return db.Issue{}, false
	}
	return issue, true
}

// resolveIssueByIdentifier 尝试按 "PREFIX-NUMBER" 格式查 issue。
//
// 前缀必须与该 workspace 自己的 issue 前缀一致——和 VCS webhook 用的
// lookupIssueByIdentifier 同规则。若不校验前缀，任何前缀都会解析到同一个
// issue 编号，FOO-134 与 TRS-134 就互相可替换了，标识符 URL
// /{ws}/issues/{key} 也就不能作为规范链接使用。
func (h *Handler) resolveIssueByIdentifier(ctx context.Context, id, workspaceID string) (db.Issue, bool) {
	parts := splitIdentifier(id)
	if parts == nil {
		return db.Issue{}, false
	}
	if workspaceID == "" {
		return db.Issue{}, false
	}
	wsUUID, err := util.ParseUUID(workspaceID)
	if err != nil {
		return db.Issue{}, false
	}
	// 大小写不敏感：手敲的 `trs-134` 也应打开 `TRS-134`。
	prefix := h.getIssuePrefix(ctx, wsUUID)
	if prefix == "" || !strings.EqualFold(parts.prefix, prefix) {
		return db.Issue{}, false
	}
	issue, err := h.Queries.GetIssueByNumber(ctx, db.GetIssueByNumberParams{
		WorkspaceID: wsUUID,
		Number:      parts.number,
	})
	if err != nil {
		return db.Issue{}, false
	}
	return issue, true
}

// identifierParts 是解析后的 issue 标识符："前缀-编号"。
type identifierParts struct {
	prefix string
	number int32
}

// splitIdentifier 把 "PREFIX-NUMBER" 形式的字符串拆成前缀与编号两部分；
// 格式不合法（无连字符、编号非纯数字、编号超界等）时返回 nil。
func splitIdentifier(id string) *identifierParts {
	idx := -1
	for i := len(id) - 1; i >= 0; i-- {
		if id[i] == '-' {
			idx = i
			break
		}
	}
	if idx <= 0 || idx >= len(id)-1 {
		return nil
	}
	numStr := id[idx+1:]
	num := 0
	for _, c := range numStr {
		if c < '0' || c > '9' {
			return nil
		}
		num = num*10 + int(c-'0')
		// 保护下面的 int32 转换：UUID 的最后一段若恰好全是数字
		// （如 "…-421234567890"）会走到这里，否则会被截断成一个看似合理的
		// issue 编号。
		if num > math.MaxInt32 {
			return nil
		}
	}
	if num <= 0 {
		return nil
	}
	return &identifierParts{prefix: id[:idx], number: int32(num)}
}

// issuePrefixForWorkspace 求 workspace 行生效的 issue 前缀：取配置值；
// 存储的前缀为空时（如前缀功能上线前创建的 workspace），则从 workspace
// 名称派生。从 getIssuePrefix 拆出，便于已经持有该行的调用方——如 GitHub
// 关闭意图扫描，它不能重读该行——复用同一规则。
//
// 空前缀的回退仍停留在冻结的、按名称派生的老逻辑上，而不是新 workspace
// 使用的基于 slug 的派生（MUL-6050）：标识符在读取时计算，切换这条路径
// 会改写那些老 workspace 里每个 issue 的标识符。新 workspace 在创建时
// 总是持久化前缀，永远不会走到这个分支。
func issuePrefixForWorkspace(ws db.Workspace) string {
	if ws.IssuePrefix != "" {
		return ws.IssuePrefix
	}
	return legacyIssuePrefixFromName(ws.Name)
}

// getIssuePrefix 取某 workspace 生效的 issue 前缀；workspace 行加载失败时
// 返回 ""。
func (h *Handler) getIssuePrefix(ctx context.Context, workspaceID pgtype.UUID) string {
	ws, err := h.Queries.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return ""
	}
	return issuePrefixForWorkspace(ws)
}

// loadAgentForUser 加载当前用户 workspace 下某个人类创建的 agent（kind 为
// "user"）；任何校验失败都会写好错误响应并返回 ok=false。
func (h *Handler) loadAgentForUser(w http.ResponseWriter, r *http.Request, agentID string) (db.Agent, bool) {
	if _, ok := requireUserID(w, r); !ok {
		return db.Agent{}, false
	}

	workspaceID := h.resolveWorkspaceID(r)
	if workspaceID == "" {
		writeError(w, http.StatusBadRequest, "workspace_id is required")
		return db.Agent{}, false
	}

	agentUUID, ok := parseUUIDOrBadRequest(w, agentID, "agent id")
	if !ok {
		return db.Agent{}, false
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return db.Agent{}, false
	}

	agent, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{
		ID:          agentUUID,
		WorkspaceID: wsUUID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "agent not found")
		return db.Agent{}, false
	}
	if agent.Kind != "user" {
		writeError(w, http.StatusNotFound, "agent not found")
		return db.Agent{}, false
	}
	return agent, true
}

// loadInboxItemForUser 加载当前用户收到的某个 inbox 条目；条目不存在、
// 不在该 workspace、或接收者不是该用户时都会写 404 并返回 ok=false。
func (h *Handler) loadInboxItemForUser(w http.ResponseWriter, r *http.Request, itemID string) (db.InboxItem, bool) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return db.InboxItem{}, false
	}

	workspaceID := h.resolveWorkspaceID(r)
	if workspaceID == "" {
		writeError(w, http.StatusBadRequest, "workspace_id is required")
		return db.InboxItem{}, false
	}

	itemUUID, ok := parseUUIDOrBadRequest(w, itemID, "inbox item id")
	if !ok {
		return db.InboxItem{}, false
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return db.InboxItem{}, false
	}

	item, err := h.Queries.GetInboxItemInWorkspace(r.Context(), db.GetInboxItemInWorkspaceParams{
		ID:          itemUUID,
		WorkspaceID: wsUUID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "inbox item not found")
		return db.InboxItem{}, false
	}

	if item.RecipientType != "member" || uuidToString(item.RecipientID) != userID {
		writeError(w, http.StatusNotFound, "inbox item not found")
		return db.InboxItem{}, false
	}
	return item, true
}
