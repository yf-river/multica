// 文件职责：组装 server 进程的 HTTP 路由器——把全部中间件、健康检查、WebSocket、
// 认证/daemon/公开 API 以及所有业务路由注册到 Chi router 上，并按环境变量有条件地
// 接好各第三方集成的内部组件（Lark/Slack/WeCom/Telegram/DingTalk/Composio 等渠道
// 引擎、对象存储、secretbox 加密盒、Redis 缓存与限流）。入口是 NewRouterWithOptions，
// 它同时返回构造出来的 *handler.Handler（main.go 用它驱动后台生命周期）。
package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/cloudruntime"
	"github.com/multica-ai/multica/server/internal/daemonws"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/featureflags"
	"github.com/multica-ai/multica/server/internal/handler"
	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	composiointeg "github.com/multica-ai/multica/server/internal/integrations/composio"
	"github.com/multica-ai/multica/server/internal/integrations/dingtalk"
	"github.com/multica-ai/multica/server/internal/integrations/lark"
	"github.com/multica-ai/multica/server/internal/integrations/slack"
	"github.com/multica-ai/multica/server/internal/integrations/telegram"
	"github.com/multica-ai/multica/server/internal/integrations/wecom"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/realtime"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/storage"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/internal/util/secretbox"
	composiosdk "github.com/multica-ai/multica/server/pkg/composio"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/featureflag"
	"github.com/multica-ai/multica/server/pkg/llm"
	publicapiv1 "github.com/multica-ai/multica/server/pkg/publicapi/v1"
)

var defaultOrigins = []string{
	"http://localhost:3000", // Next.js 开发服务器
	"http://localhost:5173", // electron-vite 开发服务器
	"http://localhost:5174", // electron-vite 开发服务器（备用端口）
}

// pluginBridgePrefix 是宿主页面中继沙箱插件 surface 调用时用的内部路由前缀。
const pluginBridgePrefix = "/api/plugin-bridge/v1"

// corsAllowedHeaders 必须列出浏览器端会发送的每一个头。缺了哪个，预检（preflight）
// 就失败，请求根本到不了处理函数——症状完全不像「服务器忽略了我的头」。
// X-Client-Capabilities 尤其值得一提：它原本只有 daemon（Go 客户端，从不预检）会发，
// 直到 web 应用开始在取消聊天时上报 chat-draft-restore-v1。
var corsAllowedHeaders = []string{
	"Accept",
	"Authorization",
	"Content-Type",
	"Idempotency-Key",
	"If-Match",
	"X-Workspace-ID",
	"X-Workspace-Slug",
	"X-Request-ID",
	"X-Agent-ID",
	"X-Task-ID",
	"X-CSRF-Token",
	"X-Client-Platform",
	"X-Client-Version",
	"X-Client-OS",
	"X-Client-Capabilities",
	// 宿主页面在转发插件 surface 的 Action API 调用时设置。
	"X-Multica-Plugin-Installation",
}

// corsExposedHeaders 列出允许浏览器端读取的响应头。少了它，跨域请求里自定义响应头
// 对 JS 静默不可读（默认只暴露 CORS 白名单内的少量头）——头其实到了线上却被丢掉了，
// 看起来就像是服务器从没发过。
//
// 直接引用 handler 里的常量而不是重抄字符串，避免将来改名字时悄悄关掉这个信号（MUL-5492）。
var corsExposedHeaders = []string{
	"ETag",
	"X-Request-ID",
	handler.HeaderCommentsTruncated,
	handler.HeaderTimelineTruncated,
}

// registerPluginActionRoutes 把公开 Plugin Action API 的一整套资源路由挂到 r 上：
// 上下文、issue 读写、评论、键值存储。两个调用方共用——版本化的公开 /v1 前缀与
// 浏览器会话中继用的 plugin-bridge 前缀。
func registerPluginActionRoutes(r chi.Router, h *handler.Handler) {
	r.Get(publicapiv1.PathContext, h.GetPluginContext)
	r.Get(publicapiv1.PathIssue, h.GetPluginIssue)
	r.Patch(publicapiv1.PathIssue, h.PatchPluginIssue)
	r.Get(publicapiv1.PathIssueComments, h.ListPluginComments)
	r.Post(publicapiv1.PathIssueComments, h.CreatePluginComment)
	r.Get(publicapiv1.PathStorageScope, h.ListPluginStorage)
	r.Get(publicapiv1.PathStorageValue, h.GetPluginStorage)
	r.Put(publicapiv1.PathStorageValue, h.PutPluginStorage)
	r.Delete(publicapiv1.PathStorageValue, h.DeletePluginStorage)
}

// allowedOrigins 解析 CORS 允许的来源列表。优先读 CORS_ALLOWED_ORIGINS，其次
// FRONTEND_ORIGIN；两者都为空时退回本地开发用的 defaultOrigins。
func allowedOrigins() []string {
	raw := strings.TrimSpace(os.Getenv("CORS_ALLOWED_ORIGINS"))
	if raw == "" {
		raw = strings.TrimSpace(os.Getenv("FRONTEND_ORIGIN"))
	}
	if raw == "" {
		return defaultOrigins
	}

	parts := strings.Split(raw, ",")
	origins := make([]string, 0, len(parts))
	for _, part := range parts {
		origin := strings.TrimSpace(part)
		if origin != "" {
			origins = append(origins, origin)
		}
	}
	if len(origins) == 0 {
		return defaultOrigins
	}
	return origins
}

// appURLFromEnv 解析面向用户的 web 应用地址。优先 MULTICA_APP_URL，退回
// FRONTEND_ORIGIN，与后端他处（handler.daemonSetupURLsFromEnv）和 CLI 登录流程
// （cmd/multica tryResolveAppURL）解析应用地址的方式保持一致。两者都没设时为空串。
func appURLFromEnv() string {
	if v := strings.TrimRight(strings.TrimSpace(os.Getenv("MULTICA_APP_URL")), "/"); v != "" {
		return v
	}
	return strings.TrimRight(strings.TrimSpace(os.Getenv("FRONTEND_ORIGIN")), "/")
}

// pluginActionBaseURL 解析 hook 处理函数回调回去用的、带版本的公开基址。托管部署用
// MULTICA_PLUGIN_API_URL 给 Plugin API 独立域名；自托管/本地部署可以留空，把同一份
// /v1 契约挂在 MULTICA_PUBLIC_URL 上。
func pluginActionBaseURL(publicURL string) string {
	if value := strings.TrimRight(strings.TrimSpace(os.Getenv("MULTICA_PLUGIN_API_URL")), "/"); value != "" {
		return value
	}
	publicURL = strings.TrimRight(strings.TrimSpace(publicURL), "/")
	if publicURL == "" {
		return ""
	}
	return publicURL + publicapiv1.BasePath
}

// parseTrustedProxies 从 MULTICA_TRUSTED_PROXIES 环境变量解析逗号分隔的 CIDR 前缀
// 列表。非法项按「每条一行 warn」丢弃而不是让服务器崩溃——某个 CIDR 打错字不该拖垮
// 整个 API。空输入返回 nil，限流器把 nil 视作「不信任何代理头，只用 RemoteAddr」。
func parseTrustedProxies(raw string) []netip.Prefix {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var out []netip.Prefix
	for _, part := range strings.Split(raw, ",") {
		s := strings.TrimSpace(part)
		if s == "" {
			continue
		}
		p, err := netip.ParsePrefix(s)
		if err != nil {
			slog.Warn("MULTICA_TRUSTED_PROXIES: ignoring invalid CIDR",
				"value", s, "error", err)
			continue
		}
		out = append(out, p)
	}
	return out
}

// normalizeServerVersion 把未打版本戳的默认值 "dev"（即 main.go 的 `version` 变量，
// 二进制未用 -X main.version=<tag> 构建时保持原样）映射为空串。
// handler.Config.ServerVersion 供 /api/config 的 server_version 字段用（带 omitempty），
// 空串能隐藏 Help 弹层里的版本行，而不是给本地 `go build`/`go run` 或未传
// --build-arg VERSION 的自托管 `docker build` 渲染出「Server version dev」。
func normalizeServerVersion(v string) string {
	if v == "dev" {
		return ""
	}
	return v
}

// NewRouter 用默认 RouterOptions 构造完整的 Chi 路由器并返回。
// rdb 可选：非 nil 时，运行时的 local-skill 请求存储会换成 Redis 后端实现，让多个
// API 节点共享同一待处理队列（多节点生产环境必需）。这里应当传「请求路径」用的
// Redis 客户端，而不是 realtime relay 那个阻塞读客户端。nil 则保留默认的内存存储，
// 单节点开发与测试用没问题。
func NewRouter(pool *pgxpool.Pool, hub *realtime.Hub, bus *events.Bus, analyticsClient analytics.Client, rdb *redis.Client) chi.Router {
	r, _ := NewRouterWithOptions(pool, hub, bus, analyticsClient, rdb, RouterOptions{})
	return r
}

// RouterOptions 是 NewRouterWithOptions 的注入参数集合：main.go 把生产环境需要的
// 各种组件（指标接收器、Redis 租约、WeCom 转发器、feature flag、心跳调度器等）
// 塞进来；测试通常留零值，从而得到内存版默认行为。字段零值一律表示「不启用」。
type RouterOptions struct {
	HTTPMetrics         *obsmetrics.HTTPMetrics
	BusinessMetrics     *obsmetrics.BusinessMetrics
	ChannelLeaseMetrics *obsmetrics.ChannelLeaseMetrics
	// ChannelLeaseRedis 是专用于渠道 WebSocket 租约的非阻塞 Redis 客户端/连接池。
	// 仅当 CHANNEL_WS_LEASE_BACKEND=redis 时需要。
	ChannelLeaseRedis *redis.Client
	// WecomRelay 是 realtime relay 按 WeCom 适配器需要的两半来看：把回复发布给其他
	// 副本，以及注册为投递它们所发布帧的消费者。在没配 Redis 的部署上是 nil——那里
	// 也不需要它，因为持有 socket 的副本同时也在发布完成事件。
	// WecomSenders 是进程级的「安装→socket」注册表，在 main 里铸造，好让 relay 的
	// 投递器在分片读循环启动前就注册好。nil 可容忍（测试、嵌入方）：会在本函数里
	// 现铸一个。
	WecomSenders *wecom.SendersRegistry

	// WecomRelayOutbound 是跨副本路由器，已注册到 relay 且在运行。在没配 Redis 的
	// 部署上是 nil——那里也不需要：发布完成事件与持有 socket 的是同一副本。
	WecomRelayOutbound *wecom.RelayOutbound

	// WecomMetrics 是 WeCom 适配器的健康指标接收器。nil 表示丢弃所有计数器，即关闭
	// /metrics 的部署得到的待遇。
	WecomMetrics *obsmetrics.WecomMetrics
	DaemonHub    *daemonws.Hub
	DaemonWakeup service.TaskWakeupNotifier
	FeatureFlags *featureflag.Service
	// HeartbeatScheduler 非 nil 时，替换构造出的 Handler 上默认的同步直通调度器。
	// main.go 在这里注入一个 BatchedHeartbeatScheduler，调用方还可以驱动 Run/Stop；
	// 测试留 nil 得到旧的同步行为。
	HeartbeatScheduler handler.HeartbeatScheduler
	// LLMMaxRetries 携带解析后的 MULTICA_LLM_MAX_RETRIES 预算。与另外三个
	// MULTICA_LLM_* 兄弟不同，它靠注入而非在本函数里读环境变量——非法值必须让启动
	// 失败，而只有 main() 能退出进程；从路由构造函数内部 terminate 进程会把恰好设了
	// 该变量的任何测试也一起杀掉。nil 表示未设置，正是测试与 NewRouter 得到的。
	LLMMaxRetries *llm.RetryOverride
}

// buildChannelSupervisor 构造渠道引擎的 Supervisor：先按环境变量读出租约配置，再
// 决定租约后端——默认 postgres，可选 redis（需 opts.ChannelLeaseRedis + 连通性探测）。
// 任何配置缺失/非法都会记录错误并返回 nil（调用方把 h.ChannelSupervisor 留空，
// 渠道自动运行即停用），而不是带着坏配置半启动。
func buildChannelSupervisor(
	installations engine.InstallationStore,
	postgresLeases engine.LeaseStore,
	registry *channel.Registry,
	inbound channel.InboundHandler,
	opts RouterOptions,
) *engine.Supervisor {
	cfg, err := channelSupervisorConfigFromEnv(opts.ChannelLeaseMetrics)
	if err != nil {
		slog.Error("channel engine: invalid lease configuration; supervisor disabled", "error", err)
		return nil
	}

	backend := strings.ToLower(strings.TrimSpace(os.Getenv("CHANNEL_WS_LEASE_BACKEND")))
	if backend == "" {
		backend = "postgres"
	}
	var leases engine.LeaseStore
	switch backend {
	case "postgres":
		leases = postgresLeases
	case "redis":
		if opts.ChannelLeaseRedis == nil {
			slog.Error("channel engine: Redis lease backend selected but CHANNEL_WS_LEASE_REDIS_URL/REDIS_URL is missing or invalid; supervisor disabled")
			return nil
		}
		namespace := strings.TrimSpace(os.Getenv("CHANNEL_WS_LEASE_NAMESPACE"))
		redisLeases, err := engine.NewRedisLeaseStore(opts.ChannelLeaseRedis, namespace)
		if err != nil {
			slog.Error("channel engine: Redis lease configuration invalid; supervisor disabled", "error", err)
			return nil
		}
		readyCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err = redisLeases.Ready(readyCtx)
		cancel()
		if err != nil {
			slog.Error("channel engine: Redis lease backend unavailable; supervisor disabled", "error", err)
			return nil
		}
		leases = redisLeases
	default:
		slog.Error("channel engine: unsupported CHANNEL_WS_LEASE_BACKEND; supervisor disabled", "backend", backend)
		return nil
	}

	slog.Info("channel engine: lease backend configured",
		"backend", backend,
		"ttl", cfg.LeaseTTL.String(),
		"renew_interval", cfg.LeaseRenewInterval.String(),
		"poll_interval", cfg.PollInterval.String(),
	)
	return engine.NewSupervisor(installations, leases, registry, inbound, cfg)
}

// channelLeasePollInterval 返回 Supervisor 多久扫一次它应当持有的安装，因而也是
// WebSocket 租约转移到另一副本要花的上限时间。
//
// 经由一个函数读取是因为有两处按它定尺寸：Supervisor 本身，以及 WeCom 跨副本
// 分发器的再报价链路——移动中途到达的帧必须保持「可报价」直到移动完成；若让它
// 自成一个常量，任何调节该旋钮的部署都会让两者脱节。
func channelLeasePollInterval() (time.Duration, error) {
	return strictPositiveDurationEnv("CHANNEL_WS_LEASE_POLL_INTERVAL", 30*time.Second)
}

// channelSupervisorConfigFromEnv 从环境变量读取渠道租约的整套定时参数并组装成
// engine.Config：TTL、续租间隔、轮询间隔、错误重试间隔、过期安全裕量。任一环境变量
// 非法都会整体报错（Supervisor 由此停用），不采用部分默认值。
func channelSupervisorConfigFromEnv(leaseMetrics *obsmetrics.ChannelLeaseMetrics) (engine.Config, error) {
	ttl, err := strictPositiveDurationEnv("CHANNEL_WS_LEASE_TTL", 180*time.Second)
	if err != nil {
		return engine.Config{}, err
	}
	renew, err := strictPositiveDurationEnv("CHANNEL_WS_LEASE_RENEW_INTERVAL", 60*time.Second)
	if err != nil {
		return engine.Config{}, err
	}
	poll, err := channelLeasePollInterval()
	if err != nil {
		return engine.Config{}, err
	}
	retry, err := strictPositiveDurationEnv("CHANNEL_WS_LEASE_ERROR_RETRY_INTERVAL", 5*time.Second)
	if err != nil {
		return engine.Config{}, err
	}
	margin, err := strictPositiveDurationEnv("CHANNEL_WS_LEASE_EXPIRY_SAFETY_MARGIN", 5*time.Second)
	if err != nil {
		return engine.Config{}, err
	}
	cfg := engine.Config{
		LeaseTTL:                ttl,
		LeaseRenewInterval:      renew,
		PollInterval:            poll,
		LeaseErrorRetryInterval: retry,
		LeaseExpirySafetyMargin: margin,
		LeaseMetrics:            leaseMetrics,
		Logger:                  slog.Default(),
	}
	if err := cfg.Validate(); err != nil {
		return engine.Config{}, err
	}
	return cfg, nil
}

// strictPositiveDurationEnv 读取一个「必须为正时长」的环境变量：空值用 fallback；
// 解析失败或非正数则报错，点名该变量。
func strictPositiveDurationEnv(name string, fallback time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration (got %q)", name, raw)
	}
	return value, nil
}

// NewRouterWithOptions 构建配置完整的 Chi 路由器，并返回构造它所用的
// *handler.Handler。需要驱动 Handler 所挂服务的后台生命周期的调用方（例如在长运行
// context 下启动 Lark 入站 Hub、在关停时 Wait）用返回的 handler；只需要 HTTP 处理
// 函数的调用方（测试、NewRouter 这个简单外壳）丢掉第二个返回值即可。
func NewRouterWithOptions(pool *pgxpool.Pool, hub *realtime.Hub, bus *events.Bus, analyticsClient analytics.Client, rdb *redis.Client, opts RouterOptions) (chi.Router, *handler.Handler) {
	queries := db.New(pool)
	emailSvc := service.NewEmailService()
	daemonHub := opts.DaemonHub
	if daemonHub == nil {
		daemonHub = daemonws.NewHub()
	}

	// 存储初始化：优先 S3，回退到本地文件系统。
	var store storage.Storage
	s3 := storage.NewS3StorageFromEnv()
	if s3 != nil {
		store = s3
	} else {
		local := storage.NewLocalStorageFromEnv()
		if local != nil {
			store = local
		}
	}

	cfSigner := auth.NewCloudFrontSignerFromEnv()
	origins := allowedOrigins()

	signupConfig := handler.Config{
		AllowSignup:              os.Getenv("ALLOW_SIGNUP") != "false",
		AllowedEmails:            splitAndTrim(os.Getenv("ALLOWED_EMAILS")),
		AllowedEmailDomains:      splitAndTrim(os.Getenv("ALLOWED_EMAIL_DOMAINS")),
		DisableWorkspaceCreation: os.Getenv("DISABLE_WORKSPACE_CREATION") == "true",
		VCSIntegrationEnabled:    os.Getenv("MULTICA_VCS_INTEGRATION_ENABLED") == "true",
		PublicURL:                strings.TrimRight(strings.TrimSpace(os.Getenv("MULTICA_PUBLIC_URL")), "/"),
		AppURL:                   appURLFromEnv(),
		TrustedProxies:           parseTrustedProxies(os.Getenv("MULTICA_TRUSTED_PROXIES")),
		CloudURL:                 strings.TrimSpace(os.Getenv("MULTICA_CLOUD_URL")),
		CloudTimeout:             35 * time.Second,
		AttachmentDownloadMode:   os.Getenv("ATTACHMENT_DOWNLOAD_MODE"),
		AttachmentDownloadURLTTL: envDuration("ATTACHMENT_DOWNLOAD_URL_TTL", 30*time.Minute),
		AttachmentFrameAncestors: origins,
		PluginSurfaceOrigin:      strings.TrimRight(strings.TrimSpace(os.Getenv("MULTICA_PLUGIN_SURFACE_ORIGIN")), "/"),
		LLMAPIKey:                strings.TrimSpace(os.Getenv("MULTICA_LLM_API_KEY")),
		LLMBaseURL:               strings.TrimSpace(os.Getenv("MULTICA_LLM_BASE_URL")),
		LLMDefaultModel:          strings.TrimSpace(os.Getenv("MULTICA_LLM_DEFAULT_MODEL")),
		LLMMaxRetries:            opts.LLMMaxRetries,
		ServerVersion:            normalizeServerVersion(version),
	}
	h := handler.New(queries, pool, hub, bus, emailSvc, store, cfSigner, analyticsClient, signupConfig, daemonHub)
	invitationRateLimits := handler.DefaultInvitationRateLimits()
	invitationRateLimits.Actor.Limit = envNonNegativeInt("RATE_LIMIT_INVITATION_ACTOR_10M", invitationRateLimits.Actor.Limit)
	invitationRateLimits.Workspace.Limit = envNonNegativeInt("RATE_LIMIT_INVITATION_WORKSPACE_24H", invitationRateLimits.Workspace.Limit)
	invitationRateLimits.Recipient.Limit = envNonNegativeInt("RATE_LIMIT_INVITATION_RECIPIENT_24H", invitationRateLimits.Recipient.Limit)
	h.InvitationRateLimiters = handler.NewMemoryInvitationRateLimiters(invitationRateLimits)
	h.Metrics = opts.BusinessMetrics
	h.FeatureFlags = opts.FeatureFlags
	h.TaskService.FeatureFlags = opts.FeatureFlags
	h.TaskService.Metrics = opts.BusinessMetrics
	h.IssueService.Metrics = opts.BusinessMetrics

	if opts.BusinessMetrics != nil {
		// 把 BusinessMetrics 接收器接进 cloud runtime 客户端，让每次出站 Fleet/
		// Gateway 请求都计入 multica_cloudruntime_request_* 直方图。
		if client, ok := h.CloudRuntime.(*cloudruntime.Client); ok {
			client.SetRecorder(opts.BusinessMetrics)
		}
	}
	if opts.DaemonWakeup != nil {
		h.TaskService.Wakeup = opts.DaemonWakeup
		if notifier, ok := opts.DaemonWakeup.(handler.RuntimeProfileRefreshNotifier); ok {
			h.DaemonProfileRefresh = notifier
		}
		if notifier, ok := opts.DaemonWakeup.(handler.WorkspaceSetRefreshNotifier); ok {
			h.DaemonWorkspaceRefresh = notifier
		}
		if notifier, ok := opts.DaemonWakeup.(handler.DaemonPendingWorkNotifier); ok {
			h.DaemonPendingWork = notifier
		}
	}
	if rdb != nil {
		h.UpdateStore = handler.NewRedisUpdateStore(rdb)
		h.ModelListStore = handler.NewRedisModelListStore(rdb)
		h.ModelCatalogCache = handler.NewRedisModelCatalogCache(rdb)
		h.LocalSkillListStore = handler.NewRedisLocalSkillListStore(rdb)
		h.LocalSkillImportStore = handler.NewRedisLocalSkillImportStore(rdb)
		h.LivenessStore = handler.NewRedisLivenessStore(rdb)
		h.WebhookRateLimiter = handler.NewRedisWebhookRateLimiter(rdb, handler.DefaultWebhookRateLimit())
		h.WebhookIPRateLimiter = handler.NewRedisWebhookIPRateLimiter(rdb, handler.DefaultWebhookIPRateLimit())
		h.WebhookAbsoluteIPRateLimiter = handler.NewRedisWebhookAbsoluteIPRateLimiter(rdb, handler.DefaultWebhookAbsoluteIPRateLimit())
		h.InvitationRateLimiters = handler.NewRedisInvitationRateLimiters(rdb, invitationRateLimits)
	}

	// 渠道引擎（MUL-3620）：平台无关的入站运行时。无条件构建——它驱动任何
	// channel.Channel，不只是飞书，所以不能依赖 Lark 的 master key（未来只接 Slack 的
	// 部署没有 Lark key）。平台适配器把自己的 Factory + ResolverSet 注册进来；Supervisor
	// 枚举所有渠道类型的活跃安装，把每个路由到其注册平台的 Factory。channel_type 没有
	// 注册 Factory 的安装会被 Supervisor 跳过——要么没配平台，要么（Slack/B2）该平台在
	// 逐安装 supervisor 之外自己驱动一条部署级连接。Router 是注入每个 Channel 的单一
	// 共享入站处理函数。
	channelRegistry := channel.NewRegistry()
	channelRouter := engine.NewRouter(h.IssueService, h.TaskService, queries, engine.RouterConfig{
		Logger: slog.Default(), Lifecycle: h,
	})
	// 对每次会话的 run 触发做防抖：一梭子消息塌缩成一次 agent run，而不是每条消息
	// 一次（MUL-2968）。
	channelRouter.EnableRunBatching(engine.DefaultChatRunBatchWindow)
	h.ChannelRouter = channelRouter
	// 媒体 intent 台账对账器：处理「已上传但未绑定」的对象。只有存在存储后端时才
	// 构建——store 在未配 S3 且本地上传目录初始化失败时是 nil；对账器拿到 nil
	// Storage 会在处理第一条未引用记录时打爆 worker goroutine（台账行可能来自某次
	// 配置了存储的启动，早已存在）。没有存储时 resolver 会跳过每次上传，不会产生新行，
	// 台账只是在等待一次带可用存储的启动。作为独立 worker 由 main.go 启动。
	if store != nil {
		h.ChannelMediaReconciler = &service.ChannelMediaReconciler{
			Queries: queries,
			Storage: store,
			Logger:  slog.Default(),
		}
	}
	installationStore := lark.NewChannelInstallationStore(queries)
	h.ChannelSupervisor = buildChannelSupervisor(
		installationStore,
		installationStore,
		channelRegistry,
		channelRouter.Handle,
		opts,
	)

	// Lark 集成。仅在设置 MULTICA_LARK_SECRET_KEY 时接线：InstallationService 拒绝
	// 把 app_secret 退回明文存储，BindingTokenService 没有它也铸不出可用令牌。key 缺失
	// 时 Lark 各 handler 返回 503 并带清晰报错，server 其余部分照常启动，让未选择接入
	// Lark 的自托管部署不受影响。飞书把自己的 Factory + ResolverSet 注册进上面的渠道引擎。
	if larkKey, err := secretbox.LoadKey("MULTICA_LARK_SECRET_KEY"); err == nil {
		box, err := secretbox.New(larkKey)
		if err != nil {
			slog.Error("lark: secretbox.New failed; lark integration disabled", "error", err)
		} else {
			installSvc, err := lark.NewInstallationService(queries, box)
			if err != nil {
				slog.Error("lark: InstallationService init failed; lark integration disabled", "error", err)
			} else {
				h.LarkInstallations = installSvc
				h.LarkBindingTokens = lark.NewBindingTokenService(queries, pool)
				slog.Info("lark integration enabled")

				// APIClient：接上真实的 Lark 开放平台 HTTP 客户端（IM v1 发送/编辑 +
				// 绑定提示 + bot 信息）。设置 MULTICA_LARK_SECRET_KEY 即操作者对整条
				// 集成的 opt-in；不再提供单独的「是否启用 HTTP」旋钮——没有出站回应的
				// 入站分发器不是有用的生产形态；而想要避开真实 Lark 流量的 CI / 集成
				// 测试可以把 MULTICA_LARK_HTTP_BASE_URL 指向一个 mock 服务器。
				//
				// MULTICA_LARK_HTTP_BASE_URL 是可选的部署级覆盖。正常运行留空：每次
				// 调用都按安装的地区解析开放平台主机（open.feishu.cn vs
				// open.larksuite.com），一个部署同时服务两个云。只有当想强制所有安装
				// 走同一主机时才设置——代理、测试 mock、或单云 staging。
				larkClient := lark.NewHTTPAPIClient(lark.HTTPClientConfig{
					BaseURL: strings.TrimSpace(os.Getenv("MULTICA_LARK_HTTP_BASE_URL")),
					Logger:  slog.Default(),
				})
				h.LarkAPIClient = larkClient

				// Channel-backed 存储：把 lark 包的 DB 接缝路由到 channel_* 表
				// （MUL-3515）。走接口的消费者（patcher、typing indicator、dispatcher、
				// hub、backfills）直接用它；基于构造函数的服务在内部包装 *db.Queries，
				// 所以它们继续接收 queries。
				cs := lark.NewChannelStore(queries)
				patcher := lark.NewPatcher(cs, installSvc, larkClient, lark.PatcherConfig{})
				patcher.Register(bus)

				// 打字指示：agent 干活时在用户消息上显示「处理中」reaction，回复发出前
				// 移除。尽力而为；失败只记日志。
				typingIndicator := lark.NewTypingIndicatorManager(larkClient, installSvc, cs, slog.Default())
				patcher.SetTypingIndicatorManager(typingIndicator)

				// 入站管线接缝：lark_inbound_audit 日志器与共享的、渠道无关的聊天会话
				// 服务。它们支撑 engine.Router 所跑的飞书 ResolverSet，与支撑 HTTP 的
				// 是同一个 IssueService + TaskService，因此 /issue-created 的 issue 在
				// 计数、去重守卫、项目边界、广播、分析与 agent 入队上和产品其余部分
				// 完全一致。飞书只是共享 engine.ChatSession（按 channel_type 分键）的
				// 又一个消费者；Lark 会话标题保留了切换前的措辞。
				auditLogger := lark.NewAuditLogger(queries)
				feishuSession := engine.NewChatSession(queries, pool, channel.TypeFeishu, engine.SessionTitles{
					Group:    "Lark group chat",
					Direct:   "Lark direct message",
					Fallback: "Lark chat",
				})

				// OutcomeReplier 接出站侧：NeedsBinding / AgentOffline /
				// AgentArchived / issue-created 转成 Lark 侧的回复卡片。需要真实的
				// APIClient 与绑定令牌服务；否则退回 noop replier（结果只记日志、不投递）。
				// 只有当它真能投递时才把它注册到 ResolverSet 上，让「还没上线出站」的
				// 部署不必付出回复 goroutine 的代价。
				replier := lark.NewLarkOutcomeReplier(lark.OutcomeReplierConfig{
					APIClient:   larkClient,
					BindingSvc:  h.LarkBindingTokens,
					Credentials: installSvc,
					Queries:     queries,
					AppURL:      appURLFromEnv(),
					Logger:      slog.Default(),
				})
				var resolverReplier lark.OutcomeReplier
				if larkClient.IsConfigured() {
					resolverReplier = replier
				}

				// 飞书适配器（MUL-3620）：WSLongConnConnector 用 gorilla/websocket 说
				// Lark 长连接协议，并且每次读取都带一个 ctx-cancel 看门狗，使租约丢失/
				// 关停能在有界时间内打断阻塞的 ReadMessage——§4.4 依赖的不变式。若端点
				// 抓取器初始化失败（MULTICA_LARK_CALLBACK_BASE_URL 配错之类），
				// buildLarkConnector 记日志并回退到 NoopConnector，让租约/supervisor
				// 生命周期仍对着真实 DB 行运行——入站消息静默丢弃直到配置修好，启动日志
				// 会标明当前模式为 "noop"。
				//
				// 注册 Factory（connect/send）+ ResolverSet（入站管线接缝）就足以把平台
				// 加进引擎——不需要改引擎本身。
				connector, connectorLabel := buildLarkConnector(installSvc, larkClient)
				lark.RegisterFeishu(channelRegistry, lark.FeishuChannelDeps{
					Connector:   connector,
					APIClient:   larkClient,
					Credentials: installSvc,
					Logger:      slog.Default(),
				})
				mediaResolver := lark.NewFeishuMediaResolver(larkClient, installSvc, store, engine.NewDBMediaIntentLedger(queries), slog.Default())
				channelRouter.Register(channel.TypeFeishu, lark.NewFeishuResolverSet(
					cs, feishuSession, auditLogger, resolverReplier, typingIndicator, mediaResolver,
				))
				slog.Info("lark inbound pipeline wired", "connector", connectorLabel)

				// 为迁移 112 加入 bot_union_id 之前创建的安装做一次性 union_id 回填。
				// 跑在热启动路径之外，慢速的 Lark 往返不会阻塞 HTTP 监听器启动。新安装
				// 在 device-flow 收尾时已写入 bot_union_id，所以这是座桥——在新部署上
				// 找不到要更新的行就退出。MUL-2671。
				go lark.BackfillBotUnionIDs(context.Background(), cs, larkClient, installSvc, slog.Default())

				// 升级修复：为那些在「逐安装地区」机制出现前，一直用部署级 base-URL
				// 覆盖跑在国际版 Lark 上的部署准备的——迁移 116 把它们的行回填成
				// 'feishu'，在操作者清掉覆盖前，把它们改标为 'lark'（真实云）。大陆/全新
				// 部署上无操作。与 union_id 回填一样跑在热启动路径之外。MUL-3083。
				go lark.BackfillRegionFromLegacyOverride(context.Background(), cs,
					strings.TrimSpace(os.Getenv("MULTICA_LARK_HTTP_BASE_URL")),
					strings.TrimSpace(os.Getenv("MULTICA_LARK_CALLBACK_BASE_URL")),
					slog.Default())

				// Device-flow 注册服务：端到端安装管线，向 accounts.feishu.cn 走
				// RFC 8628（扫码握手），随后把得到的 Bot 凭据与安装者的
				// lark_user_binding 放在一个 DB 事务里提交。可选环境变量
				// MULTICA_LARK_REGISTRATION_DOMAIN / _LARK_DOMAIN 为 staging/dev
				// 覆盖协议主机。
				regCfg := lark.RegistrationConfig{
					Domain:     strings.TrimSpace(os.Getenv("MULTICA_LARK_REGISTRATION_DOMAIN")),
					LarkDomain: strings.TrimSpace(os.Getenv("MULTICA_LARK_REGISTRATION_LARK_DOMAIN")),
				}
				regClient := lark.NewRegistrationClient(regCfg)
				regSvc, rerr := lark.NewRegistrationService(
					lark.RegistrationServiceConfig{Logger: slog.Default()},
					regClient,
					larkClient,
					queries,
					pool,
					installSvc,
					h.LarkBindingTokens,
				)
				if rerr != nil {
					slog.Error("lark: RegistrationService init failed; install disabled", "error", rerr)
				} else {
					// 在行提交时发布 lark_installation:created，让每个工作区客户端的
					// 连接徽章都能刷新，而不只是轮询安装状态到成功的那一个标签页。
					regSvc.SetEventBus(bus)
					h.LarkRegistration = regSvc
					slog.Info("lark device-flow install enabled")
				}
			}
		}
	} else {
		slog.Info("lark integration disabled (MULTICA_LARK_SECRET_KEY not set)")
	}

	// Slack 集成。多租户 B2 模式（MUL-3666）：Multica 托管唯一一个 Slack app，工作区
	// 经 OAuth 自助安装，入站跑在单条部署级 Socket Mode 连接上、按 team_id 路由——
	// 取代第三阶段的逐安装连接模型（MUL-3516）。
	//
	// 两个部署级环境变量分别把守两半：
	//   - MULTICA_SLACK_SECRET_KEY 解密存在 channel_installation 行上的逐安装 bot
	//     token（xoxb-）。它把守入站 ResolverSet + 出站回复订阅者，没有它 Slack
	//     就完全不存在。
	//   - MULTICA_SLACK_APP_TOKEN 是授权那单条 Socket Mode 连接的 app 级 token
	//     （xapp-）。它拿不到 OAuth，属于一次性运维配置。没有它入站被禁用
	//     （ResolverSet + 出站仍接好，既有安装的回复继续流动，但不再接收新事件）。
	//
	// ResolverSet/Outbound 与飞书共享同一个 engine.ChatSession、channel_* 表、
	// IssueService 与 TaskService，所以 /issue、去重与 run 触发行为一致。飞书不受影响。
	// 每个 Slack 安装都是自带 app（BYO），自带 app 级 token，因此注册逐安装的 Slack
	// Factory，Supervisor 为每个安装驱动一条 Socket Mode 连接（与飞书相同）。
	if slackKey, err := secretbox.LoadKey("MULTICA_SLACK_SECRET_KEY"); err == nil {
		box, err := secretbox.New(slackKey)
		if err != nil {
			slog.Error("slack: secretbox.New failed; slack integration disabled", "error", err)
		} else {
			// 出站回复器（MUL-3666）：投递 NeedsBinding 提示 / AgentOffline /
			// AgentArchived / issue-created 通知。绑定令牌服务铸出提示内赎回链接里
			// 的一次性令牌；赎回端点（下面注册的公开路由）把 Slack 用户绑定到其
			// Multica 账号。
			slackBindingSvc := slack.NewBindingTokenService(queries, pool)
			h.SlackBindingTokens = slackBindingSvc
			slackReplier := slack.NewOutboundReplier(slack.OutboundReplierConfig{
				Binding: slackBindingSvc,
				Decrypt: box.Open,
				// 绑定链接（/slack/bind）是 web 应用页面，所以必须用应用 URL
				// （MULTICA_APP_URL ?? FRONTEND_ORIGIN），而不是 MULTICA_PUBLIC_URL
				// （后端/API URL）。与 Lark 回复器一致（appURLFromEnv）。
				AppURL:  appURLFromEnv(),
				Queries: queries,
				Logger:  slog.Default(),
			})
			// 打字指示（MUL-3874）：agent 干活时在用户消息上放 👀 reaction，run 完成或
			// 失败时清除。尽力而为；失败只记日志。注册在出站回复订阅者之前，这样
			// EventChatDone 时 reaction 先于回复清除（bus 投递是同步的、按订阅顺序）。
			// 在这里订阅也是清除失败 run reaction 的唯一路径——出站回复器不处理它。
			slackTyping := slack.NewTypingIndicatorManager(queries, box.Open, slog.Default())
			slackTyping.Register(bus)
			// Slack 附件需要对象存储，因为每条聊天附件都指向一个已上传对象。存储被
			// 禁用时让媒体 resolver 保持未设置，把 Slack 消息当纯文本摄入。
			var slackMedia engine.MediaResolver
			if store != nil {
				slackMedia = slack.NewMediaResolver(
					box.Open,
					store,
					engine.NewDBMediaIntentLedger(queries),
					slog.Default(),
				)
			}
			channelRouter.Register(slack.TypeSlack, slack.NewSlackResolverSet(queries, pool, slackReplier, slackTyping, slackMedia))
			slack.NewOutbound(queries, box.Open, slog.Default()).Register(bus)

			// 按需历史读取器，服务统一的 `multica chat history` 命令（MUL-3871）：
			// agent 要求时才拉取该会话的 Slack 对话，而不是每次入站都强制拼装。
			h.SlackHistory = slack.NewHistory(queries, box.Open, slog.Default())

			// `/issue`、`/new`、`/clear` 是真实的 Slack 斜杠命令，走同一条 Socket Mode
			// 连接投递。`/issue` 入队 quick-create；两个会话控制复用共享的 route/context
			// 与任务服务。每个结果都收到一条私有的 ephemeral 确认。
			slackSlash := slack.NewSlashCommandProcessor(slack.SlashCommandConfig{
				Queries: queries,
				Tasks:   h.TaskService,
				Control: slack.NewSlackDMControlStarter(queries, pool, h.TaskService, h),
				Binding: slackBindingSvc,
				AppURL:  appURLFromEnv(),
				Logger:  slog.Default(),
			})

			// 逐安装入站：Supervisor 为每条活跃 Slack 安装构建并照看一条 Socket Mode
			// 连接，用该安装自己的 app 级 token（xapp-，BYO 安装时粘贴）鉴权——
			// 没有部署级 app token，也没有单条连接。
			slack.RegisterSlack(channelRegistry, slack.ChannelDeps{Decrypt: box.Open, Logger: slog.Default(), Slash: slackSlash})

			// BYO 自助安装（粘贴 bot token + app 级 token）。InstallService 只需要
			// 静态加密密钥——没有托管式 OAuth 客户端凭据。
			installSvc, ierr := slack.NewInstallService(queries, pool, box, slog.Default())
			if ierr != nil {
				slog.Error("slack: InstallService init failed; install disabled", "error", ierr)
			} else {
				h.SlackInstall = installSvc
			}
			slog.Info("slack integration enabled (BYO per-installation socket mode)")
		}
	} else {
		slog.Info("slack integration disabled (MULTICA_SLACK_SECRET_KEY not set)")
	}

	// DingTalk 每个 BYO 安装用一条出站 Stream 连接。AppSecret 静态加密，只有配置了
	// 专属部署 key 时集成才起作用。
	if dingtalkKey, err := secretbox.LoadKey("MULTICA_DINGTALK_SECRET_KEY"); err == nil {
		box, err := secretbox.New(dingtalkKey)
		if err != nil {
			slog.Error("dingtalk: secretbox.New failed; integration disabled", "error", err)
		} else {
			dingtalkClient := dingtalk.NewClient(nil, "")
			bindingSvc := dingtalk.NewBindingTokenService(queries, pool)
			h.DingTalkBindingTokens = bindingSvc
			replier := dingtalk.NewOutboundReplier(dingtalk.OutboundReplierConfig{
				Binding: bindingSvc,
				Decrypt: box.Open,
				Client:  dingtalkClient,
				AppURL:  appURLFromEnv(),
				Logger:  slog.Default(),
			})
			ack := dingtalk.NewAckNotifier(dingtalkClient, box.Open, slog.Default())
			var media engine.MediaResolver
			if store != nil {
				media = dingtalk.NewMediaResolver(
					dingtalkClient,
					box.Open,
					store,
					engine.NewDBMediaIntentLedger(queries),
					slog.Default(),
				)
			}
			botNames := dingtalk.NewBotNameResolver(dingtalkClient, box.Open)
			channelRouter.Register(dingtalk.TypeDingTalk, dingtalk.NewDingTalkResolverSet(queries, pool, replier, ack, media, botNames))
			dingtalk.NewOutbound(queries, box.Open, dingtalkClient, slog.Default()).Register(bus)
			dingtalk.RegisterDingTalk(channelRegistry, dingtalk.ChannelDeps{
				Decrypt:  box.Open,
				Client:   dingtalkClient,
				BotNames: botNames,
				Logger:   slog.Default(),
			})
			installSvc, installErr := dingtalk.NewInstallService(queries, pool, box, slog.Default())
			if installErr != nil {
				slog.Error("dingtalk: InstallService init failed; install disabled", "error", installErr)
			} else {
				h.DingTalkInstall = installSvc
			}
			slog.Info("dingtalk integration enabled (BYO per-installation stream mode)")
		}
	} else {
		slog.Info("dingtalk integration disabled (MULTICA_DINGTALK_SECRET_KEY not set)")
	}

	// WeCom 智能机器人集成（aibot）。逐安装的 WebSocket 长连接连到
	// wss://openws.work.weixin.qq.com；Supervisor 为每条活跃 wecom 安装驱动一条连接，
	// 用共享的 ws_lease_token 把关，保证多副本部署里每个 bot 至多一条活跃 socket
	// （WeCom 本身只允许一条）。
	//
	// 由 MULTICA_WECOM_SECRET_KEY 把关。没设时整块跳过，wecom Web-UI 端点返回 503，
	// 既有部署不受影响。智能机器人流程不需要任何公开 HTTP 回调，所以无需向公网暴露
	// 其他东西。
	if wecomKey, err := secretbox.LoadKey("MULTICA_WECOM_SECRET_KEY"); err == nil {
		box, err := secretbox.New(wecomKey)
		if err != nil {
			slog.Error("wecom: secretbox.New failed; wecom integration disabled", "error", err)
		} else {
			credsResolver, err := wecom.NewSecretboxCredentialsResolver(box)
			if err != nil {
				slog.Error("wecom: credentials resolver init failed; wecom integration disabled", "error", err)
			} else {
				wecomStore := wecom.NewStore(queries)
				h.WecomStore = wecomStore
				h.WecomCredentials = credsResolver

				// 绑定令牌支撑发给首次发消息的 WeCom 用户的「关联你的 Multica 账号」
				// 提示。aibot 的 userid 是匿名化的 T 前缀 id，与真实 userid 或邮箱毫无
				// 关系，因此一张显式的绑定表才是唯一正确答案——理由见 wecom/binding.go。
				wecomBinding := wecom.NewBindingTokenService(queries, pool)
				h.WecomBindingTokens = wecomBinding

				// Senders 注册表：wecom OutboundReplier 在这里于启动时创建，但它推送
				// aibot_send_msg 所需的、活的 wsSender 只存在于运行中的 wecomChannel 里。
				// wecom.NewSendersRegistry 铸一张共享 map；ChannelDeps 的写侧与
				// Replier 的读侧都拿到它，每个 Channel.Connect 进入时自注册、退出时清理。
				wecomSenders := opts.WecomSenders
				if wecomSenders == nil {
					wecomSenders = wecom.NewSendersRegistry()
				}

				wecomReplier := wecom.NewOutboundReplier(wecom.OutboundReplierConfig{
					Binding: wecomBinding,
					Senders: wecomSenders,
					AppURL:  appURLFromEnv(),
					Logger:  slog.Default(),
				})

				// WeCom 共享 engine.ChatSession（按 channel_type 分键），使 /issue、
				// 去重与 run 触发跨平台行为一致。会话标题用 wecom 风味措辞（中文产品
				// 口吻——wecom 部署仅限中国区）。
				wecomSession := engine.NewChatSession(queries, pool, wecom.TypeWecom, engine.SessionTitles{
					Group:    "企业微信群聊",
					Direct:   "企业微信单聊",
					Fallback: "企业微信会话",
				})

				wecom.RegisterWecom(channelRegistry, wecom.ChannelDeps{
					Credentials: credsResolver,
					Senders:     wecomSenders,
					Metrics:     wecomMetricsOrNil(opts.WecomMetrics),
					Logger:      slog.Default(),
				})
				// 入站媒体：回调带着一条预签名的 COS URL 和每 URL 一个的 key，所以
				// resolver 不需要任何 WeCom 凭据——只需要一处能放字节的持久存储。没有
				// 对象存储就没有可指向的附件，resolver 保持 nil，附件保留为占位文本。
				// 与上面 DingTalk 的 nil 守卫相同。
				var wecomMedia engine.MediaResolver
				if store != nil {
					wecomMedia = wecom.NewMediaResolver(
						store,
						engine.NewDBMediaIntentLedger(queries),
						wecomSenders,
						slog.Default(),
					)
				}
				channelRouter.Register(wecom.TypeWecom, wecom.NewResolverSet(
					wecomStore, wecomSession, wecomReplier, wecomMedia,
				))

				// EventChatDone 订阅者：把 agent 的聊天回复经入站循环同一条 aibot
				// WebSocket 推回去。与 slack.NewOutbound(...).Register(bus) 对位。
				// 没有它，agent 的回复只会落进 Multica 的 web UI——WeCom 里的用户
				// 看不到任何响应。
				//
				// WithAttachments 追加第二跳：agent 绑定到该回复的文件从对象存储读回，
				// 跟着消息发进聊天。仅当该部署配了存储时才传——没有存储就无物可读，
				// 而这个选项正是投递路径检查的条件。
				//
				// DeclareChannelFileDelivery 是把同一条件说给 agent：只有当这个分支
				// 真的搭起了发送文件那一跳时，一次 run 才会被告知可以发文件。两行故意
				// 放在一起——「有存储的部署」与「agent 被承诺可投递文件的部署」必须是
				// 同一个部署，唯一能保证这一点的方式就是一个 `if` 同时决定两者。
				wecomOutboundOpts := []wecom.OutboundOption{}
				if store != nil {
					wecomOutboundOpts = append(wecomOutboundOpts, wecom.WithAttachments(store))
					h.DeclareChannelFileDelivery(string(wecom.TypeWecom))
				}
				// 出站订阅者把数据报给与连接路径相同的接收器，操作者就能在同一块仪表盘
				// 上既看到「bot 连不上」，也看到「回复在被丢弃、以及为什么」。
				wecomOutboundOpts = append(wecomOutboundOpts,
					wecom.WithOutboundMetrics(wecomMetricsOrNil(opts.WecomMetrics)))
				// 跨副本路由。在这里而非 main 构建，因为它守护的 senders 注册表在这里
				// 创建，且要注册回 relay，让每个节点的读循环都能到达它。见
				// wecom/relay_outbound.go。
				if opts.WecomRelayOutbound != nil {
					opts.WecomRelayOutbound.SetMetrics(wecomMetricsOrNil(opts.WecomMetrics))
					wecomOutboundOpts = append(wecomOutboundOpts, wecom.WithRelay(opts.WecomRelayOutbound))
					slog.Info("wecom integration: cross-replica outbound routing enabled")
				}
				wecomOutbound := wecom.NewOutbound(queries, wecomSenders, slog.Default(), wecomOutboundOpts...)
				wecomOutbound.Register(bus)
				// dispatcher 在本路由存在之前就已在消费；这里才让它知道由谁执行投递。
				// 它此前读到的一切都在自己的队列里等着。
				if opts.WecomRelayOutbound != nil {
					opts.WecomRelayOutbound.Attach(wecomOutbound)
				}

				// 媒体抓取器尽管看似保留地址仍允许拨号的范围。默认空，SSRF 守卫维持
				// 出厂时同样的严格度。在 fake-IP 代理后面的部署需要它：那里每个公网
				// 主机名都会解析进代理的地址池（198.18.0.0/15 最常见），WeCom 自己的
				// COS 主机因此单看地址无法与元数据端点区分，所有附件都会被拒。
				if raw := strings.TrimSpace(os.Getenv("MULTICA_WECOM_MEDIA_ALLOW_CIDRS")); raw != "" {
					for _, err := range wecom.SetMediaAllowedPrefixes(strings.Split(raw, ",")) {
						slog.Error("wecom: ignoring malformed media allow cidr", "error", err)
					}
					slog.Warn("wecom: media guard has an operator allow-list; those ranges are reachable by a URL WeCom supplies",
						"cidrs", raw)
				}

				// 帧追踪：默认关闭，除非操作者要求。它记录消息文本的有界前缀，因此
				// 「它是否开启」必须显现在它正在写入的日志里——否则某个会话会被留成
				// 开启而无人察觉消息内容在累积。
				if wecom.SetTrace(os.Getenv("MULTICA_WECOM_TRACE") == "1") {
					slog.Warn("wecom: frame tracing ON — records message text; unset MULTICA_WECOM_TRACE when done")
				}

				slog.Info("wecom integration enabled (smart bot, long connection)")
				// 单副本约束：WeCom 出站（agent 回复 + inbox 推送）只由持有每个 bot
				// 进程内 WebSocket 租约的那条副本投递。多副本部署中，在另一条副本上
				// 发布的 EventChatDone/EventInboxNew 到不了租约持有者，这些回复会被
				// 丢弃。这里用条件叙述而非依赖副本计数信号：server 在此处没有可靠的
				// 副本数，而 REDIS_URL 表示「配了 Redis」（它也把关限流），不等于
				// 「不止一条副本」。见 wecom/outbound.go 与 SELF_HOSTING.md。待出站
				// 能路由到租约持有者后移除。
				if opts.WecomRelayOutbound != nil {
					slog.Info("wecom integration: cross-replica outbound routing enabled — agent replies and inbox pushes produced on a replica that does not hold the bot's WebSocket lease are forwarded to the lease holder over the realtime relay. A reply produced while NO replica holds a live connection (every one mid-reconnect) is still lost; see wecom/relay_outbound.go.")
				} else {
					slog.Warn("wecom integration: WeCom agent replies and inbox pushes are delivered only by the replica holding each bot's WebSocket lease. If you run more than one backend replica, responses produced on a replica that does not hold the lease will be dropped — run the WeCom-enabled backend as a single replica, or run a sharded/dual realtime relay (REDIS_URL), which enables cross-replica outbound routing.")
				}
			}
		}
	} else {
		slog.Info("wecom integration disabled (MULTICA_WECOM_SECRET_KEY not set)")
	}

	// Telegram 集成。形态与 Slack 相同：BYO bot token 粘贴安装，每条活跃安装由共享的
	// engine.Supervisor 照看一条 getUpdates 长轮询循环，resolver 落在通用 channel_* 表上，
	// 出站经事件总线上节流的 editMessageText 流式发送。由 MULTICA_TELEGRAM_SECRET_KEY
	// （静态 token 加密 key）把关；未设置时 handler 返回 503 且不注册任何 Factory。
	if telegramKey, err := secretbox.LoadKey("MULTICA_TELEGRAM_SECRET_KEY"); err == nil {
		box, err := secretbox.New(telegramKey)
		if err != nil {
			slog.Error("telegram: secretbox.New failed; telegram integration disabled", "error", err)
		} else {
			telegramBindingSvc := telegram.NewBindingTokenService(queries, pool)
			h.TelegramBindingTokens = telegramBindingSvc
			telegramReplier := telegram.NewOutboundReplier(telegram.OutboundReplierConfig{
				Binding: telegramBindingSvc,
				Decrypt: box.Open,
				// 绑定链接（/telegram/bind）是 web 应用页面：应用 URL，不是 API URL。
				// 与 Slack 回复器一致。
				AppURL: appURLFromEnv(),
				Logger: slog.Default(),
			})
			telegramTyping := telegram.NewTypingNotifier(box.Open, "", nil, slog.Default())
			channelRouter.Register(telegram.TypeTelegram, telegram.NewTelegramResolverSet(queries, pool, telegramReplier, telegramTyping))
			telegramOutbound := telegram.NewOutbound(queries, box.Open, "", nil, slog.Default())
			telegramOutbound.Register(bus)
			h.TelegramOutbound = telegramOutbound

			// 逐安装入站：Supervisor 为每条活跃 Telegram 安装构建并照看一条长轮询循环。
			telegram.RegisterTelegram(channelRegistry, telegram.ChannelDeps{Decrypt: box.Open, Logger: slog.Default()})

			installSvc, ierr := telegram.NewInstallService(queries, pool, box, slog.Default())
			if ierr != nil {
				slog.Error("telegram: InstallService init failed; install disabled", "error", ierr)
			} else {
				h.TelegramInstall = installSvc
			}
			slog.Info("telegram integration enabled (per-installation long polling)")
		}
	} else {
		slog.Info("telegram integration disabled (MULTICA_TELEGRAM_SECRET_KEY not set)")
	}

	// Composio 集成（MUL-3720）。由 COMPOSIO_API_KEY 加 composio_mcp_apps 特性开关
	// 把关。该环境变量是独立 SDK 用来向 Composio 鉴权的项目级 key（以 x-api-key 发送；
	// 项目由 key 本身解析出来，因此不配置项目 id）。未设置或被开关禁用时整块跳过，
	// composio 的 HTTP handler 返回 503，既有部署不受影响。操作者通过设置
	// COMPOSIO_API_KEY 加一个回调基址（COMPOSIO_CALLBACK_BASE_URL，回退
	// MULTICA_PUBLIC_URL）接入。toolkit→auth-config 映射不在这里配置——它在请求时
	// 从项目 /auth_configs 动态解析，因此启用某 toolkit 是仪表盘操作，无需重新部署。
	// 状态签名用 COMPOSIO_STATE_SECRET；未设置时用从 JWT_SECRET 派生的 key。
	if composioAPIKey := strings.TrimSpace(os.Getenv("COMPOSIO_API_KEY")); composioAPIKey != "" {
		if !featureflags.ComposioMCPAppsEnabled(context.Background(), opts.FeatureFlags) {
			slog.Info("composio integration disabled (feature flag off)")
		} else {
			sdkClient, err := composiosdk.NewClient(composiosdk.Options{APIKey: composioAPIKey})
			if err != nil {
				slog.Error("composio: SDK client init failed; composio integration disabled", "error", err)
			} else {
				stateSecret := composioStateSecret()
				callbackBase := composioCallbackBaseURL(signupConfig.PublicURL)
				switch {
				case len(stateSecret) == 0:
					slog.Error("composio: no state secret (set COMPOSIO_STATE_SECRET or JWT_SECRET); composio integration disabled")
				case callbackBase == "":
					slog.Error("composio: no callback base url (set COMPOSIO_CALLBACK_BASE_URL or MULTICA_PUBLIC_URL); composio integration disabled")
				default:
					svc, serr := composiointeg.NewService(sdkClient, queries, composiointeg.Config{
						StateSecret:     stateSecret,
						CallbackBaseURL: callbackBase,
						FrontendBaseURL: appURLFromEnv(),
					})
					if serr != nil {
						slog.Error("composio: service init failed; composio integration disabled", "error", serr)
					} else {
						h.Composio = svc
						// Stage 3（MUL-3721）钩子：把逐任务的 MCP overlay 构建器喂给
						// TaskService，让每条 Enqueue* 路径在 daemon 认领前，先把发起人
						// 的 Composio 会话 URL 挂到任务行上。taskSvc 到这里已存在——
						// 它在 NewHandler 内部构建——并且正是为了这类迟到接线而暴露了
						// 自己的 Composio 字段，所以无需 Handler 级改动。
						if h.TaskService != nil {
							h.TaskService.Composio = svc
						}
						slog.Info("composio integration enabled")
					}
				}
			}
		}
	} else {
		slog.Info("composio integration disabled (COMPOSIO_API_KEY not set)")
	}

	if credentialKey, err := secretbox.LoadKey("MULTICA_EXTERNAL_CREDENTIAL_KEY"); err == nil {
		box, boxErr := secretbox.New(credentialKey)
		if boxErr != nil {
			slog.Error("external credentials: secretbox initialization failed", "error", boxErr)
		} else {
			h.ExternalCredentialBox = box
			slog.Info("external credential encryption enabled")
		}
	} else {
		slog.Info("external credential token storage disabled (MULTICA_EXTERNAL_CREDENTIAL_KEY not set)")
	}

	// VCS 静态加密：这个加密盒加密基于 token 的 provider（Forgejo / Gitea / GitLab）
	// 的逐工作区访问令牌与 webhook 密钥。没有它，connect/webhook handler 返回 503
	// （配错的自托管永远不会落明文密钥）。
	if vcsKey, err := secretbox.LoadKey("MULTICA_VCS_SECRET_KEY"); err == nil {
		box, err := secretbox.New(vcsKey)
		if err != nil {
			slog.Error("vcs: secretbox.New failed; vcs integration disabled", "error", err)
		} else {
			h.VCSSecretBox = box
			slog.Info("vcs integration enabled")
		}
	} else {
		slog.Info("vcs integration disabled (MULTICA_VCS_SECRET_KEY not set)")
	}

	// 插件机密用专用部署 key。与 VCS 和渠道机密分开，给操作者一个隔离的轮换与爆炸
	// 半径；没有它，保存 `secret` 型配置字段会失败关闭，而不是落明文。
	if pluginKey, err := secretbox.LoadKey("MULTICA_PLUGIN_SECRET_KEY"); err == nil {
		box, err := secretbox.New(pluginKey)
		if err != nil {
			slog.Error("plugins: secretbox.New failed; Plugin secrets disabled", "error", err)
		} else if h.PluginService != nil {
			h.PluginService.Secrets = box
			// 同一份部署 key 也保留原始形态。封口与签名需要它提供不同的东西：机密配置
			// 值是先封口后开取，而 hook 签名必须按需可复现，加密盒做不到。每个安装的
			// 签名机密都由它派生而非存储，这样任何行都不持有可用的签名机密。
			h.PluginService.DeploymentKey = pluginKey
			h.PluginSurfaceTokens, err = handler.NewPluginSurfaceTokenBox(pluginKey)
			if err != nil {
				slog.Error("plugins: surface token key derivation failed; surfaces disabled", "error", err)
			}
			slog.Info("Plugin secret encryption enabled")
		}
	} else {
		slog.Info("Plugin secrets disabled (MULTICA_PLUGIN_SECRET_KEY not set)")
	}

	// Hook 引擎。事件触发型 hook 从 bus 分发到 worker 池：Bus.Publish 在发布请求的
	// goroutine 上同步跑监听器，若从那里拨任何第三方端点，等于把外部服务器放进了
	// 「创建 issue」的关键路径。
	if h.PluginService != nil {
		h.PluginService.Callbacks = service.NewCallbackTokens()
		// 省略而不是发一个相对地址：第三方 hook 服务器无法调用纯路径 URL，而一个坏了
		// 的绝对 URL 比缺失的更容易诊断。
		if baseURL := pluginActionBaseURL(signupConfig.PublicURL); baseURL != "" {
			h.PluginService.CallbackBaseURL = baseURL
		} else {
			slog.Warn("plugins: MULTICA_PLUGIN_API_URL and MULTICA_PUBLIC_URL are not set; hook callbacks will carry no callback_url")
		}
		// 特性开关只能经服务到达事件路径：worker 没有可读它的请求。
		h.PluginService.FeatureFlags = h.FeatureFlags
		pluginEvents := service.NewPluginEventDispatcher(h.PluginService)
		service.SubscribePluginEvents(bus, pluginEvents)
	}

	if opts.HeartbeatScheduler != nil {
		h.HeartbeatScheduler = opts.HeartbeatScheduler
	}
	// 认证缓存：PAT 缓存在常规 Auth 中间件、DaemonAuth 的 mul_ 回退路径与 revoke
	// handler（invalidate）之间共享。DaemonTokenCache 支撑 DaemonAuth 的 mdt_ 路径。
	// 两个构造函数在 rdb 为 nil 时都返回 nil——每个消费者把 nil 当作「不缓存，总打 DB」。
	patCache := auth.NewPATCache(rdb)
	daemonTokenCache := auth.NewDaemonTokenCache(rdb)
	h.PATCache = patCache
	h.DaemonTokenCache = daemonTokenCache
	h.MembershipCache = auth.NewMembershipCache(rdb)

	// Cloud PAT 校验器：校验 mcn_ token 是否出自 Multica Cloud Fleet。未配置 Cloud
	// URL 时返回 nil——Auth / DaemonAuth 中间件把 nil 当作「不支持 mcn_」并以 401
	// 拒绝，而不是漏进 mul_/JWT 路径。复用 MULTICA_CLOUD_URL（cloud-runtime 代理也用
	// 的同一个 URL），一个部署只有一条权威的 multica-cloud 连接。
	cloudPATVerifier := auth.NewCloudPATVerifier(auth.CloudPATVerifierConfig{
		FleetBaseURL: signupConfig.CloudURL,
		Redis:        rdb,
	})

	// 空声明缓存：最近一次检查已确认 runtime 没有排队任务时，让 daemon 的轮询路径
	// 跳过一次 Postgres 扫描。rdb 为 nil 时返回 nil——TaskService 把它当作「不缓存，
	// 总打 DB」（既有行为）。
	h.TaskService.EmptyClaim = service.NewEmptyClaimCache(rdb)
	// 陈旧派发回收另排一张表，因为「队列空」的裁决无法代表一条已提交后丢失的认领
	// 响应。Redis 状态缺失或失败时保留历史的 PostgreSQL 回退。
	h.TaskService.ReclaimCheck = service.NewReclaimCheckCache(rdb)

	// 在存储定型后再接 WS 心跳，让 WS 路径使用与 HTTP 路径相同的（可能为 Redis
	// 后端）存储。
	daemonHub.SetHeartbeatHandler(h.HandleDaemonWSHeartbeat)
	// WS 优先认领（MUL-4257）：把 daemon:rpc_request 帧（如 tasks.claim）路由到与
	// HTTP 端点相同的处理函数。
	daemonHub.SetRPCHandler(h.DaemonRPCHandler)
	health := newServerHealth(pool)

	r := chi.NewRouter()

	// 全局中间件
	r.Use(chimw.RequestID)
	r.Use(middleware.ClientMetadata)
	r.Use(middleware.RequestLogger)
	if opts.HTTPMetrics != nil {
		r.Use(opts.HTTPMetrics.Middleware)
	}
	r.Use(chimw.Recoverer)
	r.Use(h.PluginSurfaceHostBoundary)
	r.Use(middleware.ContentSecurityPolicy)

	// 把允许的来源共享给 WebSocket 源检查器。
	realtime.SetAllowedOrigins(origins)

	// 共享同一批受信代理 CIDR（MULTICA_TRUSTED_PROXIES），让 WebSocket 源检查只信
	// 来自受信代理的 X-Forwarded-Host——单一配置源，而不是另起一套平行的。
	realtime.SetTrustedProxies(signupConfig.TrustedProxies)

	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   origins,
		AllowedMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders:   corsAllowedHeaders,
		ExposedHeaders:   corsExposedHeaders,
		AllowCredentials: true,
		MaxAge:           300,
	}))

	// 健康/就绪检查
	r.Get("/health", health.liveHandler)
	r.Get("/readyz", health.readyHandler)
	r.Get("/healthz", health.readyHandler)

	// Realtime 子系统指标——连接数、慢客户端驱逐、各事件类型的发送 QPS 计数。以 JSON
	// 暴露，运维可直接抓取或在管理 UI 里展示，无需引入 Prometheus 依赖。见 MUL-1138
	// （阶段 0）。
	//
	// 访问受限（MUL-1342）：设置了 REALTIME_METRICS_TOKEN 时，调用方必须以
	// Authorization: Bearer <token> 出示它。环境变量未设时 handler 只服务 loopback
	// 调用方，本地开发照常可用，又不会把指标暴露在公网监听上。
	r.Get("/health/realtime", realtimeMetricsHandler(os.Getenv("REALTIME_METRICS_TOKEN")))

	// WebSocket
	mc := &membershipChecker{queries: queries}
	pr := &patResolver{queries: queries, cache: patCache}
	slugResolver := realtime.SlugResolver(func(ctx context.Context, slug string) (string, error) {
		ws, err := queries.GetWorkspaceBySlug(ctx, slug)
		if err != nil {
			return "", err
		}
		return util.UUIDToString(ws.ID), nil
	})
	r.Get("/ws", func(w http.ResponseWriter, r *http.Request) {
		realtime.HandleWebSocket(hub, mc, pr, slugResolver, w, r)
	})

	// 本地文件服务（使用本地存储时）。经 handler 服务，让 /uploads/* 与
	// /api/attachments 下载端点携带同样的预览安全头；自托管的分源/同源客户端于是可以
	// 直接从静态路由 iframe 预览 PDF/HTML，而不会撞上全局 frame-ancestors 'none' 的
	// CSP。见 MUL-3821 / #4477。
	if _, ok := store.(*storage.LocalStorage); ok {
		r.Get("/uploads/*", h.ServeLocalUpload)
	}

	// 带能力签名的附件下载（MUL-5292）。公开是必然的：原生下载（Electron 的
	// webContents.downloadURL、跨站 webview <img>）既不带 Authorization 也不带会话
	// cookie，middleware.Auth 无头可读。查询串里那个短期、单附件专用的签名才是凭据，
	// 且它只由通过成员校验的「已认证 GET /api/attachments/{id}」铸造。下方已认证的
	// /api/attachments/{id}/download 路由保持不变——这一条纯粹是追加的。
	r.Get("/api/attachments/{id}/signed-download", h.DownloadAttachmentWithCapability)

	// 头像服务。公开理由与上面能力下载相同：认证 cookie 是 SameSite=Strict，认证门户
	// 下的 URL 没法给 Desktop/移动 webview 或分源自托管 web 应用当原生 <img src>。
	// 路径里的 HMAC 签名就是凭据；它覆盖存储 key，只解析图片 key，对象必须是头像类
	// ——见 server/internal/handler/avatar.go（MUL-5393 / #6024）。
	r.Get("/api/avatars/{sig}/*", h.ServeAvatar)

	// 托管插件文档走能力签名，且有意放在会话中间件之外：配置的内容源必须保持无
	// cookie。加密路径令牌把安装、不可变版本、surface 与一次 bridge challenge 绑在一起。
	r.Get("/plugin-surfaces/{token}", h.ServePluginSurface)

	// 认证（公开）——按 IP 限流。
	if rdb == nil {
		slog.Warn("auth rate limiting disabled: REDIS_URL not configured")
	}
	trustedProxies := middleware.ParseTrustedProxies(os.Getenv("RATE_LIMIT_TRUSTED_PROXIES"))
	authRL := middleware.RateLimit(rdb, envPositiveInt("RATE_LIMIT_AUTH", 5), time.Minute, trustedProxies)
	contactSalesRL := middleware.RateLimit(rdb, envPositiveInt("RATE_LIMIT_CONTACT_SALES", 5), time.Hour, trustedProxies)
	r.With(authRL).Post("/auth/login", h.AccountPasswordLogin)
	r.With(authRL).Post("/auth/google", h.GoogleLogin)
	r.Post("/auth/logout", h.Logout)

	// 公开 API
	r.Get("/api/config", h.GetConfig)
	r.With(contactSalesRL).Post("/api/contact-sales", h.CreateContactSales)
	// 公开分享链接预览——无需认证：展示工作区名称/别名与邀请人，让还没登录的访客
	// 看清自己要加入什么。
	r.Get("/api/share-links/{code}", h.GetShareLinkInfo)

	// autopilot 的 webhook 入口。有意放在认证组之外：URL 路径里的 bearer token 就是
	// 凭据。工作区上下文来自触发器行，从不来自请求头。
	r.Post("/api/webhooks/autopilots/{token}", h.HandleAutopilotWebhook)
	// GitHub App webhook（无 Multica 认证——请求在 handler 内经 HMAC-SHA256 签名
	// 认证）与安装后设置回调。
	r.Post("/api/webhooks/github", h.HandleGitHubWebhook)
	r.Get("/api/github/setup", h.GitHubSetupCallback)
	// Slack OAuth 回调（路径里无 Multica 认证——它被 Slack 的浏览器重定向命中；
	// workspace/agent/发起人从密封的 state 里恢复）。它兑换 code、upsert 安装，再把
	// 浏览器弹回 Settings → Integrations。
	// 基于 token 的 provider（Forgejo / Gitea / GitLab）的 VCS webhook。无 Multica
	// 认证——按连接用 provider 的签名方案鉴权；路径里的连接 id 选出工作区、provider
	// 与解密密钥。
	r.Post("/api/webhooks/vcs/{connectionId}", h.HandleVCSWebhook)
	// Composio OAuth 回调（MUL-3843）。有意不在 Auth 组之下：Composio 在 OAuth 流程
	// 末尾把用户浏览器 302 重定向到这里，而 cookie 会话经常缺失（会话过期、SameSite=
	// Strict / Safari ITP 剥离跨站 cookie、隐私窗口、不同子域上的自托管回调）。身份
	// 不从会话取——它来自 HMAC 签名的 `state` 查询参数，CompleteCallback 在做任何事
	// 之前先校验（签名、过期、重放）。h.Composio == nil 仍返回 503。把它放进 Auth 组
	// 会让缺 cookie 变成硬性 401，恰好破坏上述浏览器的流程；其余四个 composio 端点
	// 仍是会话把关的。
	r.Get("/api/integrations/composio/callback", h.ComposioCallback)

	// Daemon API 路由（需要 daemon token 或合法用户 token）
	r.Route("/api/daemon", func(r chi.Router) {
		r.Use(middleware.DaemonAuth(queries, patCache, daemonTokenCache, cloudPATVerifier))

		r.Post("/register", h.DaemonRegister)
		r.Post("/deregister", h.DaemonDeregister)
		r.Post("/heartbeat", h.DaemonHeartbeat)
		r.Get("/ws", h.DaemonWebSocket)
		r.Get("/workspaces", h.ListDaemonWorkspaces)
		r.Get("/workspaces/{workspaceId}/repos", h.GetDaemonWorkspaceRepos)
		r.Get("/workspaces/{workspaceId}/runtime-profiles", h.DaemonListRuntimeProfiles)

		// agent 触发的插件 hook。daemon 的本地 MCP server 在 agent 选中某个工具时调用
		// 这里；server 负责发签名请求，daemon 从不持有签名机密。
		r.Post("/tasks/{id}/plugin-hooks", h.InvokeAgentPluginHook)
		// broker 在连接时索要某个 mcp hook 的凭据，机密因此从不躺在任务记录里。
		r.Get("/tasks/{id}/plugin-mcp/{contributionId}/credential", h.ResolvePluginMCPCredential)

		r.Post("/runtimes/{runtimeId}/tasks/claim", h.ClaimTaskByRuntime)
		// 规范的机器级批量认领（MUL-4257）。`/claim` 是过渡期别名；daemon 协调器
		// 以规范路径为目标。
		r.Post("/tasks/claim", h.ClaimTasksByRuntime)
		r.Post("/claim", h.ClaimTasksByRuntime)
		r.Post("/runtimes/{runtimeId}/tasks/{taskId}/prepare-lease", h.ExtendTaskPrepareLease)
		r.Post("/runtimes/{runtimeId}/tasks/{taskId}/skill-bundles/resolve", h.ResolveTaskSkillBundles)
		r.Get("/runtimes/{runtimeId}/tasks/{taskId}/skill-files/{fileId}/content", h.GetTaskSkillFileContent)
		r.Get("/runtimes/{runtimeId}/tasks/pending", h.ListPendingTasksByRuntime)
		r.Post("/runtimes/{runtimeId}/update/{updateId}/result", h.ReportUpdateResult)
		r.Post("/runtimes/{runtimeId}/models/{requestId}/result", h.ReportModelListResult)
		r.Post("/runtimes/{runtimeId}/local-skills/{requestId}/result", h.ReportLocalSkillListResult)
		r.Post("/runtimes/{runtimeId}/mcp-checks/{requestId}/result", h.ReportWorkspaceMcpCheck)
		r.Post("/runtimes/{runtimeId}/local-skills/import/{requestId}/result", h.ReportLocalSkillImportResult)

		r.Get("/tasks/{taskId}/status", h.GetTaskStatus)
		r.Post("/tasks/{taskId}/start", h.StartTask)
		r.Post("/tasks/{taskId}/wait-local-directory", h.MarkTaskWaitingLocalDirectory)
		r.Post("/tasks/{taskId}/progress", h.ReportTaskProgress)
		r.Post("/tasks/{taskId}/complete", h.CompleteTask)
		r.Post("/tasks/{taskId}/fail", h.FailTask)
		r.Post("/tasks/{taskId}/usage", h.ReportTaskUsage)
		r.Post("/tasks/{taskId}/traces", h.ReportTaskTraces)
		r.Post("/tasks/{taskId}/messages", h.ReportTaskMessages)
		r.Get("/tasks/{taskId}/messages", h.ListTaskMessages)
		r.Post("/tasks/{taskId}/cancel-ack", h.AckTaskCancelled)
		r.Post("/tasks/{taskId}/release-pause-ack", h.AckReleasePause)

		r.Post("/workspaces/{workspaceId}/issues/gc-check", h.BatchIssueGCCheck)
		r.Get("/issues/{issueId}/gc-check", h.GetIssueGCCheck)
		r.Get("/chat-sessions/{sessionId}/gc-check", h.GetChatSessionGCCheck)
		r.Get("/autopilot-runs/{runId}/gc-check", h.GetAutopilotRunGCCheck)
		r.Get("/tasks/{taskId}/gc-check", h.GetTaskGCCheck)

		r.Post("/runtimes/{runtimeId}/recover-orphans", h.RecoverOrphanedTasks)
		r.Post("/tasks/{taskId}/session", h.PinTaskSession)
	})

	// 公开 Plugin Action API。这是暴露在 Plugin API 源上的、稳定且全局带版本的契约。
	// 只收 mpi_/mpc_ bearer token；浏览器会话走下面的 bridge 组，即使两个主机名都路由
	// 到同一个 Go 服务也无法越过这条信任边界。
	r.Group(func(r chi.Router) {
		r.Use(middleware.PluginBearerOnly)
		r.Use(middleware.PluginRateLimit(rdb, envPositiveInt("RATE_LIMIT_PLUGIN_API", 120), time.Minute))
		r.Route(publicapiv1.BasePath, func(r chi.Router) {
			registerPluginActionRoutes(r, h)
			r.NotFound(publicapiv1.NotFound)
			r.MethodNotAllowed(publicapiv1.MethodNotAllowed)
		})
	})

	// 沙箱插件 surface 的浏览器会话中继。iframe 经 MessagePort 与宿主对话；宿主用
	// 已登录用户的会话和安装头调用这条内部路由。放在 /v1 之外，防止 Public API 接受
	// 会话 cookie。
	r.Group(func(r chi.Router) {
		r.Use(middleware.Auth(queries, patCache, cloudPATVerifier))
		r.Route(pluginBridgePrefix, func(r chi.Router) {
			registerPluginActionRoutes(r, h)
			// 仅 UI/手动触发。`event` 由宿主从事件总线分发；`agent` 走 MCP 而非这条
			// HTTP 端点到达。
			r.Post("/hooks/{key}", h.InvokePluginHook)
		})
	})

	r.Group(func(r chi.Router) {
		r.Use(middleware.Auth(queries, patCache, cloudPATVerifier))
		r.Use(middleware.RefreshCloudFrontCookies(cfSigner))
		r.Route("/api/external-credential-profiles", func(r chi.Router) {
			r.Get("/", h.ListExternalCredentialProfiles)
			r.Post("/", h.CreateExternalCredentialProfile)
			r.Post("/test", h.TestExternalCredentialProfile)
			r.Route("/{id}", func(r chi.Router) {
				r.Get("/", h.GetExternalCredentialProfile)
				r.Put("/", h.UpdateExternalCredentialProfile)
				r.Delete("/", h.DeleteExternalCredentialProfile)
			})
		})

		// Plugin Action API。在 surface 通过 postMessage 桥提出请求后，由宿主页面以
		// 已登录用户会话调用——iframe 不持有任何凭据，永远无法直达这些端点。说话的是
		// 哪个安装，来自宿主设置的一个头；工作区从该安装推导而非信任客户端，成员资格
		// 再对它校验。正因如此它落在用户作用域组里：路径上没有可用于把关的工作区。
		// --- 用户作用域路由（无需工作区上下文） ---
		r.Get("/api/me", h.GetMe)
		r.Patch("/api/me", h.UpdateMe)
		r.Patch("/api/me/onboarding", h.PatchOnboarding)
		r.Post("/api/me/onboarding/complete", h.CompleteOnboarding)
		r.Post("/api/me/onboarding/cloud-waitlist", h.JoinCloudWaitlist)
		// 已废弃——桌面端 < v3 在滚动升级期用的垫片路由。v3 前端经通用的
		// CreateAgent / CreateIssue 创建 Helper agent 与起始 issue，只在这里调用
		// /complete。待 X-Client-Version 遥测确认没有 pre-v3 桌面端仍在调用后移除。
		// handler 在 server/internal/handler/onboarding_shim.go。
		r.Post("/api/cli-token", h.IssueCliToken)
		r.Post("/api/upload-file", h.UploadFile)
		r.With(handler.RequireHumanActor).Post("/api/client-usage", h.UpsertClientUsage)

		// 注（MUL-4309）：通用的 OpenAI 兼容直通端点（POST /api/llm/v1/chat/
		// completions[/stream]）已被有意移除。暴露一个由部署自有 key 背书的一般性 LLM
		// 代理，等于让任何登录用户拿我们的钱跑任意补全。LLM 访问现在只限 server 内部
		//（见 pkg/llm）；web/客户端需要的任何东西都必须走为特定用途而建、在 server 端
		// 固定 prompt/模型的业务端点（例如聊天标题生成）。

		// 附件下载——用户作用域（仅认证），非工作区作用域。handler 从附件行自行解析
		// 工作区并在内部校验成员资格，因此该路由可作为无法附加 X-Workspace-Slug /
		// X-Workspace-ID 头的原生浏览器 <img>/<video> src 调用。把
		// `/api/attachments/<id>/download` 持久化进评论 markdown 依赖于此——见
		// MUL-3130。下面的元数据/删除端点保持工作区作用域，因为它们是 JSON-API 消费方，
		// 总带有工作区上下文。
		r.Get("/api/attachments/{id}/download", h.DownloadAttachment)

		r.Route("/api/workspaces", func(r chi.Router) {
			r.Get("/", h.ListWorkspaces)
			r.Post("/", h.CreateWorkspace)
			r.Route("/{id}", func(r chi.Router) {
				// 成员级访问
				r.Group(func(r chi.Router) {
					r.Use(middleware.RequireWorkspaceMemberFromURL(queries, "id"))
					r.Get("/", h.GetWorkspace)
					r.Get("/members", h.ListMembersWithUser)
					r.Post("/leave", h.LeaveWorkspace)
					r.Get("/invitations", h.ListWorkspaceInvitations)
					// 列出 GitHub 安装对成员可见，让 integrations 标签页对非管理员不再渲染
					// 空白；handler 剥掉管理句柄并加一个 can_manage 提示，UI 据此把关
					// connect/disconnect。
					r.Get("/github/installations", h.ListGitHubInstallations)
					r.Get("/repos/details", h.GetWorkspaceRepositoryCenter)
					r.Get("/repos", h.ListRepositories)
					// VCS 连接（Forgejo / Gitea / GitLab）——与 GitHub 安装同理对成员可见；
					// connect/disconnect 由下面管理组把关。
					r.Get("/vcs/connections", h.ListVCSConnections)
					// 自定义 runtime profile——列出/读取对成员可见（Runtime 页面人人可渲染；
					// 增删改由下面管理组把关）。
					r.Get("/runtime-profiles", h.ListRuntimeProfiles)
					r.Get("/runtime-profiles/{profileId}", h.GetRuntimeProfile)
					// 工作区 MCP 库——对成员可见，让 agent owner 能看到自己 agent 能加什么。
					// 载荷只有名字与传输方式；存储的条目是只写的。
					r.Get("/mcp-servers", h.ListWorkspaceMcpServers)
					// 已安装 Plugin 对成员可见，让成员看到工作区里挂载了什么、持有哪些 scope；
					// 安装/配置/移除保持 admin-only。
					r.Get("/plugins", h.ListPlugins)
					// 一次短命的托管 surface 启动。对成员可见，因为打开 issue 就会触发它；
					// 可执行字节不落在已认证的应用/API 源上。
					r.Get("/plugins/{installationId}/surfaces/{surfaceKey}/launch", h.GetPluginSurfaceLaunch)
				})
				// 管理级访问
				r.Group(func(r chi.Router) {
					r.Use(middleware.RequireWorkspaceRoleFromURL(queries, "id", "owner", "admin"))
					r.Put("/", h.UpdateWorkspace)
					r.Patch("/", h.UpdateWorkspace)
					r.Post("/repos", h.CreateRepository)
					r.Patch("/repos/{repositoryId}", h.UpdateRepository)
					r.Delete("/repos/{repositoryId}", h.DeleteRepository)
					r.Put("/repos/{repositoryId}/locations", h.PutRepositoryLocation)
					r.Delete("/repos/{repositoryId}/locations/{locationId}", h.DeleteRepositoryLocation)
					r.Post("/repos/probe", h.ProbeWorkspaceRepo)
					r.Post("/repos/resolve", h.ResolveWorkspaceRepo)
					r.Post("/members", h.CreateInvitation)
					r.Route("/members/{memberId}", func(r chi.Router) {
						r.Patch("/", h.UpdateMember)
						r.Delete("/", h.DeleteMember)
					})
					r.Delete("/invitations/{invitationId}", h.RevokeInvitation)
					// 策展共享 MCP 库是管理动作。创建条目不绑定任何 agent；agent owner
					// 通过 agent 路由把它加进自己的 agent。
					r.Post("/mcp-servers", h.CreateWorkspaceMcpServer)
					r.Put("/mcp-servers/{serverId}", h.UpdateWorkspaceMcpServer)
					r.Patch("/mcp-servers/{serverId}/metadata", h.UpdateWorkspaceMcpMetadata)
					r.Post("/mcp-servers/{serverId}/checks", h.CreateWorkspaceMcpCheck)
					r.Get("/mcp-servers/{serverId}/checks", h.ListWorkspaceMcpChecks)
					r.Delete("/mcp-servers/{serverId}", h.DeleteWorkspaceMcpServer)
					r.Post("/share-links", h.CreateShareLink)
					r.Delete("/share-links/{linkId}", h.RevokeShareLink)
					r.Get("/share-links", h.ListShareLinks)
					// 自定义 runtime profile 变更（admin-only）。
					r.Post("/runtime-profiles", h.CreateRuntimeProfile)
					r.Patch("/runtime-profiles/{profileId}", h.UpdateRuntimeProfile)
					r.Put("/runtime-profiles/{profileId}", h.UpdateRuntimeProfile)
					r.Delete("/runtime-profiles/{profileId}", h.DeleteRuntimeProfile)
					// 发布。作者上传一个制品包，我们负责存储；版本一经发布即不可变，
					// 所以这里刻意没有 update 路由。
					r.Get("/plugins/packages", h.ListPluginPackages)
					r.Post("/plugins/packages", h.PublishPluginPackage)
					r.Post("/plugins/packages/local", h.PublishLocalPluginPackage)
					r.Delete("/plugins/packages/{packageId}", h.DeletePluginPackage)
					// 安装 Plugin 有意分两步：preview 读已发布版本的 manifest 并返回
					// scope 列表而不写任何东西，让同意页在安装存在之前有可展示的内容。
					// 两步都指名同一个版本 id——这正是让「已批准的 manifest」与「要跑的
					// 代码」是同一制品的关键。
					r.Post("/plugins/preview", h.PreviewPlugin)
					r.Post("/plugins", h.InstallPlugin)
					r.Get("/plugins/{installationId}/invocations", h.ListPluginInvocations)
					r.Post("/plugins/{installationId}/token", h.RotatePluginToken)
					r.Delete("/plugins/{installationId}/token", h.RevokePluginToken)
					// mcp-transport 审批。发现（discovery）什么都不采纳；PUT 才是授权，
					// 它按 schema 摘要钉住工具。
					r.Get("/plugins/{installationId}/mcp/{hookKey}/tools", h.ListPluginMCPTools)
					r.Put("/plugins/{installationId}/mcp/{hookKey}/tools", h.ApprovePluginMCPTools)
					r.Put("/plugins/{installationId}/config", h.ConfigurePlugin)
					r.Post("/plugins/{installationId}/enable", h.EnablePlugin)
					r.Post("/plugins/{installationId}/disable", h.DisablePlugin)
					r.Delete("/plugins/{installationId}", h.UninstallPlugin)
				})
				// 仅 owner 访问
				r.With(middleware.RequireWorkspaceRoleFromURL(queries, "id", "owner")).Delete("/", h.DeleteWorkspace)

				// GitHub 集成——connect/disconnect 保持 admin-only；只读的列表端点在上面
				// 的成员级组里，让非管理员也能看到工作区的连接状态。
				r.Group(func(r chi.Router) {
					r.Use(middleware.RequireWorkspaceRoleFromURL(queries, "id", "owner", "admin"))
					r.Get("/github/connect", h.GitHubConnect)
					r.Get("/github/installations/{installationId}/repositories", h.ListGitHubInstallationRepositories)
					r.Delete("/github/installations/{installationId}", h.DeleteGitHubInstallation)
					// VCS connect/disconnect/webhook 再生成（admin-only）。
					r.Post("/vcs/connections", h.ConnectVCS)
					r.Post("/vcs/connections/{connectionId}/rotate-webhook", h.RotateVCSConnectionWebhook)
					r.Delete("/vcs/connections/{connectionId}", h.DeleteVCSConnection)
				})

				// Lark 集成。这里的每个端点只要路由器层面的工作区成员资格；真正的授权
				// 是逐 agent 的，在每个 handler 内经 canManageAgent（agent owner 或
				// 工作区 owner/admin）执行，因此 agent 的 owner 不必是工作区管理员也能
				// 绑定/管理自己 agent 的 Bot（MUL-4213）。路由器无法自己做这个判断：
				// begin 用 `agent_id` 查询参数识别 agent，revoke 用安装 id，二者都不是
				// 角色中间件能看到的 URL 参数。
				//   - 列表保持成员可见（与 GitHub 同理：Integrations 标签页必须能为
				//     非管理员渲染，让他们看到「谁接的线」）。
				//   - Begin / status / revoke 各自先加载目标 agent 并跑 canManageAgent
				//     （status 还要看会话发起人或管理员）再做任何事。
				r.Group(func(r chi.Router) {
					r.Use(middleware.RequireWorkspaceMemberFromURL(queries, "id"))
					r.Get("/lark/installations", h.ListLarkInstallations)
					r.Delete("/lark/installations/{installationId}", h.RevokeLarkInstallation)
					// Device-flow 扫码安装。Begin 对 Lark 开启一次新的注册会话并返回
					// 二维码 URL；前端对话框随后轮询 /install/{sessionId}/status 直到
					// 成功或终态失败。
					r.Post("/lark/install/begin", h.BeginLarkInstall)
					r.Get("/lark/install/{sessionId}/status", h.GetLarkInstallStatus)
				})

				// Slack 集成（MUL-3666）。与 Lark 相同的 admin/member 切分：列出对
				// 成员可见；OAuth begin + revoke 是 admin-only。OAuth 回调本身是公开
				// 路由（它被 Slack 的浏览器重定向命中，路径里没有工作区），注册在这个
				// 工作区组之外。
				r.Group(func(r chi.Router) {
					r.Use(middleware.RequireWorkspaceMemberFromURL(queries, "id"))
					r.Get("/slack/installations", h.ListSlackInstallations)
					r.Get("/wecom/installations", h.ListWecomInstallations)
				})
				r.Group(func(r chi.Router) {
					r.Use(middleware.RequireWorkspaceRoleFromURL(queries, "id", "owner", "admin"))
					r.Delete("/slack/installations/{installationId}", h.RevokeSlackInstallation)
					r.Post("/slack/install/byo", h.RegisterSlackBYO)
					r.Delete("/wecom/installations/{installationId}", h.RevokeWecomInstallation)
					r.Post("/wecom/install/byo", h.RegisterWecomBYO)
				})

				r.Group(func(r chi.Router) {
					r.Use(middleware.RequireWorkspaceMemberFromURL(queries, "id"))
					r.Get("/dingtalk/installations", h.ListDingTalkInstallations)
					r.Get("/dingtalk/groups", h.ListDingTalkGroups)
					r.Delete("/dingtalk/installations/{installationId}/groups/{conversationId}", h.ForgetDingTalkGroup)
					r.Delete("/dingtalk/installations/{installationId}", h.RevokeDingTalkInstallation)
					r.Post("/dingtalk/install/byo", h.RegisterDingTalkBYO)
				})

				// Telegram 集成。与 Slack 相同的 admin/member 切分：列出对成员可见；
				// 安装 + revoke 是 admin-only。
				r.Group(func(r chi.Router) {
					r.Use(middleware.RequireWorkspaceMemberFromURL(queries, "id"))
					r.Get("/telegram/installations", h.ListTelegramInstallations)
				})
				r.Group(func(r chi.Router) {
					r.Use(middleware.RequireWorkspaceRoleFromURL(queries, "id", "owner", "admin"))
					r.Delete("/telegram/installations/{installationId}", h.RevokeTelegramInstallation)
					r.Post("/telegram/install", h.RegisterTelegramBot)
				})
			})
		})

		// Lark 绑定令牌赎回。非工作区作用域，因为赎回者在有任何工作区上下文之前就命中
		// 它——赎回本身就是铸造其 lark_user_binding 行的动作。身份来自会话；令牌只证明
		// 「这个 open_id 请求过绑定」，与已登录用户结合才形成映射。
		r.Post("/api/lark/binding/redeem", h.RedeemLarkBindingToken)
		// Slack 绑定令牌赎回。与 Lark 同理：非工作区作用域，因为赎回者在有任何工作区
		// 上下文之前命中它——赎回铸造其绑定行。已登录用户（来自会话）被绑定到令牌携带
		// 的 Slack id。
		r.Post("/api/slack/binding/redeem", h.RedeemSlackBindingToken)
		// DingTalk 绑定赎回是用户作用域，理由与 Slack 相同：token 在选择工作区上下文
		// 之前就被赎回。
		r.Post("/api/dingtalk/binding/redeem", h.RedeemDingTalkBindingToken)
		// WeCom 智能机器人绑定令牌赎回。与 Lark/Slack 同理：会话是赎回者 Multica
		// 身份的真源；令牌只携带要绑定的 WeCom userid。
		r.Post("/api/wecom/binding/redeem", h.RedeemWecomBindingToken)
		// Telegram 绑定令牌赎回。同理：非工作区作用域，身份来自会话，令牌只证明
		// 「这个 Telegram 用户 id 请求过绑定」。
		r.Post("/api/telegram/binding/redeem", h.RedeemTelegramBindingToken)

		// Composio 集成（MUL-3720）。用户作用域（无工作区上下文）：连接属于某个用户。
		// 这四个需要已登录会话；OAuth 回调是例外，落在 Auth 组之外（与其他公开
		// OAuth/webhook 路由一起注册在上面——见 MUL-3843）。COMPOSIO_API_KEY 未设时
		// 全部返回 503。
		r.Route("/api/integrations/composio", func(r chi.Router) {
			r.Post("/connect/init", h.ComposioConnectInit)
			r.Get("/toolkits", h.ListComposioToolkits)
			r.Get("/connections", h.ListComposioConnections)
			r.Delete("/connections/{id}", h.DeleteComposioConnection)
		})

		// 用户作用域的邀请路由（无需工作区上下文）
		r.Get("/api/invitations", h.ListMyInvitations)
		r.Get("/api/invitations/{id}", h.GetMyInvitation)
		r.Post("/api/invitations/{id}/accept", h.AcceptInvitation)
		r.Post("/api/invitations/{id}/decline", h.DeclineInvitation)
		r.Post("/api/share-links/join", h.JoinByShareLink)

		r.Route("/api/tokens", func(r chi.Router) {
			r.Get("/", h.ListPersonalAccessTokens)
			r.Post("/", h.CreatePersonalAccessToken)
			r.Post("/current/renew", h.RenewCurrentPersonalAccessToken)
			r.Delete("/{id}", h.RevokePersonalAccessToken)
		})

		// --- 工作区作用域路由（全部要求工作区成员资格） ---
		r.Group(func(r chi.Router) {
			r.Use(middleware.RequireWorkspaceMember(queries))

			// 指派频率
			r.Get("/api/assignee-frequency", h.GetAssigneeFrequency)

			// Issues
			r.Route("/api/issues", func(r chi.Router) {
				r.Post("/table/groups", h.ListIssueTableGroups)
				r.Post("/table/rows", h.ListIssueTableRows)
				r.Post("/table/facets", h.ListIssueTableFacets)
				r.Get("/search", h.SearchIssues)
				r.Get("/child-progress", h.ChildIssueProgress)
				r.Get("/children", h.ListChildrenByParents)
				r.Get("/grouped", h.ListGroupedIssues)
				r.Get("/", h.ListIssues)
				// GET /api/issues 的 POST 孪生，用于超大的过滤器集合
				// （agents-working 的 id facet）——见 QueryIssues。
				r.Post("/query", h.QueryIssues)
				r.Post("/", h.CreateIssue)
				r.Post("/quick-create", h.QuickCreateIssue)
				r.Post("/preview-trigger", h.PreviewIssueTrigger)
				r.Post("/batch-update", h.BatchUpdateIssues)
				r.Post("/batch-delete", h.BatchDeleteIssues)
				r.Route("/{id}", func(r chi.Router) {
					r.Get("/", h.GetIssue)
					r.Put("/", h.UpdateIssue)
					r.Post("/move", h.MoveIssue)
					r.Delete("/", h.DeleteIssue)
					r.Post("/comments/trigger-preview", h.PreviewCommentTriggers)
					r.Post("/comments", h.CreateComment)
					r.Get("/comments", h.ListComments)
					r.Get("/timeline", h.ListTimeline)
					r.Get("/subscribers", h.ListIssueSubscribers)
					r.Post("/subscribe", h.SubscribeToIssue)
					r.Post("/unsubscribe", h.UnsubscribeFromIssue)
					r.Post("/unsubscribe/subtree", h.UnsubscribeFromIssueSubtree)
					r.Get("/active-task", h.GetActiveTaskForIssue)
					r.Post("/tasks/{taskId}/cancel", h.CancelTask)
					r.Post("/rerun", h.RerunIssue)
					r.Post("/quick-actions/{quickActionId}/run", h.RunQuickAction)
					r.Post("/quick-actions/{quickActionId}/render", h.RenderQuickAction)
					r.Get("/task-runs", h.ListTasksByIssue)
					r.Get("/trace", h.ListIssueTaskTraceEvents)
					r.Get("/execution-tree", h.GetIssueExecutionTree)
					r.Get("/usage", h.GetIssueUsage)
					r.Post("/reactions", h.AddIssueReaction)
					r.Delete("/reactions", h.RemoveIssueReaction)
					r.Get("/attachments", h.ListAttachments)
					r.Get("/children", h.ListChildIssues)
					r.Get("/labels", h.ListLabelsForIssue)
					r.Post("/labels", h.AttachLabel)
					r.Delete("/labels/{labelId}", h.DetachLabel)
					r.Get("/metadata", h.ListIssueMetadata)
					r.Put("/metadata/{key}", h.SetIssueMetadataKey)
					r.Delete("/metadata/{key}", h.DeleteIssueMetadataKey)
					r.Put("/properties/{propertyId}", h.SetIssueProperty)
					r.Delete("/properties/{propertyId}", h.DeleteIssueProperty)
					r.Get("/pull-requests", h.ListPullRequestsForIssue)
					r.Post("/pull-requests", h.LinkPullRequestToIssue)
					r.Post("/merge-requests/create", h.CreateMergeRequestForIssue)
					r.Post("/source-fetch", h.RecordIssueSourceFetch)
					r.Get("/delivery-owner", h.ManageDeliveryOwner)
					r.Post("/delivery-owner", h.ManageDeliveryOwner)
					r.Get("/deliveries", h.GetIssueDeliveries)
					r.Post("/deliveries", h.SubmitIssueDelivery)
					r.Put("/delivery-plan", h.RegisterIssuePlan)
					r.Post("/delivery-plan/confirm", h.ConfirmIssuePlan)
					r.Post("/delivery-plan/revoke-release", h.RevokeIssueReleaseAuthorization)
					r.Post("/deliveries/{deliveryId}/action", h.ActIssueDelivery)
					r.Post("/review-verdict", h.SubmitIssueReviewVerdict)
				})
			})

			r.Get("/api/delivery-queue/window", h.ReleaseWindow)
			r.Post("/api/delivery-queue/window", h.ReleaseWindow)
			r.Get("/api/delivery-queue/publication", h.PublicationLease)
			r.Post("/api/delivery-queue/publication", h.PublicationLease)
			r.Get("/api/delivery-queue", h.ListDeliveryQueue)
			r.Post("/api/delivery-queue/advance", h.AdvanceDelivery)
			r.Post("/api/delivery-queue/slots/advance", h.AdvanceVerificationSlot)
			r.Post("/api/delivery-queue/slots/configure", h.ConfigureVerificationSlot)
			r.Get("/api/delivery-queue/slots", h.GetVerificationLease)
			r.Get("/api/delivery-queue/{deliveryId}/authorization", h.CheckDeliveryAuthorization)

			// 审核中列的审查设置：运行时、模型与提示词。
			r.Route("/api/review", func(r chi.Router) {
				r.Get("/settings", h.GetReviewSettings)
				r.Put("/settings", h.UpdateReviewSettings)
			})

			// 任务消息（面向用户，非 daemon 认证）
			r.Route("/api/supervision", func(r chi.Router) {
				r.Get("/", h.GetSupervision)
				r.Get("/instructions", h.GetSupervisionInstructions)
				r.Post("/validate", h.ValidateSupervisionResult)
				r.Post("/investigation", h.SaveSupervisionInvestigation)
				r.Get("/investigation/material", h.ReadSupervisionInvestigationMaterial)
				r.Put("/", h.UpdateSupervision)
				r.Get("/subjects", h.ListSupervisionSubjects)
				r.Get("/evidence/{kind}/{sourceId}", h.GetSupervisionEvidence)
				r.Get("/subjects/{issueId}", h.GetSupervisionSubject)
				r.Get("/batches/{batchId}/task", h.GetSupervisionBatchTask)
				r.Post("/check", h.CheckSupervision)
				r.Patch("/findings/{findingId}", h.UpdateSupervisionFinding)
				r.Post("/findings/{findingId}/feedback", h.FeedbackSupervisionFinding)
			})
			// 上游同步：人类侧读配置与清单，Agent 侧领批次、回传、推进游标。
			r.Route("/api/upstream", func(r chi.Router) {
				r.Get("/", h.GetUpstream)
				r.Put("/", h.UpdateUpstreamConfig)
				r.Get("/items", h.ListUpstreamItems)
				r.With(handler.RequireHumanActor).Post("/items/{itemId}/absorption-task", h.CreateUpstreamAbsorptionTask)
				r.With(handler.RequireHumanActor).Patch("/items/status", h.MarkUpstreamItems)
				r.Get("/runs", h.ListUpstreamRuns)
				r.Post("/runs/start", h.StartUpstreamRun)
				r.Post("/runs/{runId}/prepare", h.PrepareUpstreamRun)
				r.Post("/runs/{runId}/heartbeat", h.HeartbeatUpstreamRun)
				r.Post("/runs/{runId}/items", h.AppendUpstreamItems)
				r.Post("/runs/{runId}/finish", h.FinishUpstreamRun)
				r.Post("/runs/{runId}/fail", h.FailUpstreamRun)
			})
			r.Get("/api/tasks/{taskId}/messages", h.ListTaskMessagesByUser)
			r.With(handler.RequireHumanActor).Post("/api/tasks/{taskId}/retry-source-context", h.RetrySourceContextQuickCreate)

			// Issue quick action（定义；运行在 /api/issues/{id}/quick-actions/{quickActionId}/run 之下）
			r.Route("/api/quick-actions", func(r chi.Router) {
				r.Get("/", h.ListQuickActions)
				r.Post("/", h.CreateQuickAction)
				r.Route("/{id}", func(r chi.Router) {
					r.Patch("/", h.UpdateQuickAction)
					r.Delete("/", h.DeleteQuickAction)
				})
			})

			// 自定义 issue 属性（定义；值在 /api/issues/{id}/properties 下）
			r.Route("/api/properties", func(r chi.Router) {
				r.Get("/", h.ListProperties)
				r.Post("/", h.CreateProperty)
				r.Route("/{id}", func(r chi.Router) {
					r.Get("/", h.GetProperty)
					r.Patch("/", h.UpdateProperty)
				})
			})

			// Labels
			r.Route("/api/labels", func(r chi.Router) {
				r.Get("/", h.ListLabels)
				r.Post("/", h.CreateLabel)
				r.Route("/{id}", func(r chi.Router) {
					r.Get("/", h.GetLabel)
					r.Put("/", h.UpdateLabel)
					r.Delete("/", h.DeleteLabel)
				})
			})

			// Issue 状态目录（MUL-6243）。读对任何成员开放——每个客户端都需要目录来渲染
			// 状态。写在 handler 内把关到工作区 owner/admin。
			r.Route("/api/issue-statuses", func(r chi.Router) {
				r.Get("/", h.ListIssueStatuses)
				r.Post("/", h.CreateIssueStatus)
				r.Patch("/reorder", h.ReorderIssueStatuses)
				r.Route("/{id}", func(r chi.Router) {
					r.Patch("/", h.UpdateIssueStatus)
					r.Delete("/", h.ArchiveIssueStatus)
				})
			})

			// 项目
			r.Route("/api/projects", func(r chi.Router) {
				r.Get("/search", h.SearchProjects)
				r.Get("/", h.ListProjects)
				r.Post("/", h.CreateProject)
				r.Route("/{id}", func(r chi.Router) {
					r.Get("/", h.GetProject)
					r.Put("/", h.UpdateProject)
					r.Delete("/", h.DeleteProject)
					r.Get("/resources", h.ListProjectResources)
					r.Post("/resources", h.CreateProjectResource)
					r.Put("/resources/{resourceId}", h.UpdateProjectResource)
					r.Delete("/resources/{resourceId}", h.DeleteProjectResource)
				})
			})

			// 小组（Squad）
			r.Route("/api/squads", func(r chi.Router) {
				r.Get("/", h.ListSquads)
				r.Post("/", h.CreateSquad)
				r.Route("/{id}", func(r chi.Router) {
					r.Get("/", h.GetSquad)
					r.Put("/", h.UpdateSquad)
					r.Delete("/", h.DeleteSquad)
					r.Get("/members", h.ListSquadMembers)
					r.Get("/members/status", h.ListSquadMemberStatus)
					r.Post("/members", h.AddSquadMember)
					r.Delete("/members", h.RemoveSquadMember)
					r.Patch("/members/role", h.UpdateSquadMemberRole)
				})
			})

			// Squad leader 评估（写 activity_log）
			r.Post("/api/issues/{id}/squad-evaluated", h.RecordSquadLeaderEvaluation)

			// Autopilot
			r.Route("/api/autopilots", func(r chi.Router) {
				r.Get("/", h.ListAutopilots)
				r.Post("/", h.CreateAutopilot)
				r.Get("/cron-preview", h.CronPreview)
				r.Route("/{id}", func(r chi.Router) {
					r.Get("/", h.GetAutopilot)
					r.Patch("/", h.UpdateAutopilot)
					r.Delete("/", h.DeleteAutopilot)
					r.Post("/trigger", h.TriggerAutopilot)
					r.Get("/runs", h.ListAutopilotRuns)
					r.Get("/runs/{runId}", h.GetAutopilotRun)
					r.Get("/deliveries", h.ListAutopilotDeliveries)
					r.Get("/deliveries/{deliveryId}", h.GetAutopilotDelivery)
					r.Post("/deliveries/{deliveryId}/replay", h.ReplayAutopilotDelivery)
					r.Post("/triggers", h.CreateAutopilotTrigger)
					r.Route("/triggers/{triggerId}", func(r chi.Router) {
						r.Patch("/", h.UpdateAutopilotTrigger)
						r.Delete("/", h.DeleteAutopilotTrigger)
						r.Post("/rotate-webhook-token", h.RotateAutopilotTriggerWebhookToken)
						r.Put("/signing-secret", h.SetAutopilotTriggerSigningSecret)
					})
					r.Post("/collaborators", h.AddAutopilotCollaborator)
					r.Delete("/collaborators/{userId}", h.RemoveAutopilotCollaborator)
				})
			})

			// 固定项
			r.Route("/api/pins", func(r chi.Router) {
				r.Get("/", h.ListPins)
				r.Post("/", h.CreatePin)
				r.Put("/reorder", h.ReorderPins)
				r.Delete("/{itemType}/{itemId}", h.DeletePin)
			})

			// 已保存的 issue 视图（MUL-4796）。
			r.Get("/api/issue-view-preferences", h.GetIssueViewPreference)
			r.Put("/api/issue-view-preferences", h.PutIssueViewPreference)
			r.Route("/api/issue-views", func(r chi.Router) {
				r.Get("/", h.ListIssueViews)
				r.Post("/", h.CreateIssueView)
				r.Route("/{id}", func(r chi.Router) {
					r.Get("/", h.GetIssueViewByID)
					r.Patch("/", h.UpdateIssueView)
					r.Delete("/", h.DeleteIssueView)
				})
			})

			// 附件
			r.Get("/api/attachments/{id}", h.GetAttachmentByID)
			// /api/attachments/{id}/download 注册在外层仅认证的组里（见上），让它可以
			// 不带工作区头被原生 <img>/<video> src 加载（MUL-3130）。handler 从附件行
			// 自行解析工作区。
			r.Get("/api/attachments/{id}/content", h.GetAttachmentContent)
			r.Delete("/api/attachments/{id}", h.DeleteAttachment)

			// 评论
			r.Route("/api/comments/{commentId}", func(r chi.Router) {
				r.With(handler.RequireHumanActor).Get("/sub-issue-preview", h.PreviewCommentSubIssue)
				r.With(handler.RequireHumanActor).Post("/sub-issues", h.CreateCommentSubIssue)
				r.Put("/", h.UpdateComment)
				r.Delete("/", h.DeleteComment)
				r.Post("/resolve", h.ResolveComment)
				r.Delete("/resolve", h.UnresolveComment)
				r.Post("/reactions", h.AddReaction)
				r.Delete("/reactions", h.RemoveReaction)
			})

			// Agents
			r.Route("/api/agents", func(r chi.Router) {
				r.Get("/", h.ListAgents)
				r.Post("/", h.CreateAgent)
				// 工作区内置的 Chief of Staff。server 所有：调用方只提供 runtime 与语言，
				// 客户端因此无法铸出带 `system_key`、进而认领系统指令层的 agent。按工作区
				// 幂等。
				r.Post("/mika", h.CreateMikaAgent)
				r.Route("/{id}", func(r chi.Router) {
					r.Get("/", h.GetAgent)
					r.Put("/", h.UpdateAgent)
					r.Post("/archive", h.ArchiveAgent)
					r.Post("/restore", h.RestoreAgent)
					r.Post("/cancel-tasks", h.CancelAgentTasks)
					r.Get("/tasks", h.ListAgentTasks)
					r.Get("/dingtalk/groups", h.ListDingTalkGroupsForAgent)
					r.Get("/skills", h.ListAgentSkills)
					r.Put("/skills", h.SetAgentSkills)
					r.Post("/skills/add", h.AddAgentSkills)
					r.Get("/labels", h.ListLabelsForAgent)
					r.Post("/labels", h.AttachLabelToAgent)
					r.Delete("/labels/{labelId}", h.DetachLabelFromAgent)
					r.Put("/skills/{skillId}/enabled", h.SetAgentSkillEnabled)
					r.Put("/runtime-skills/enabled", h.SetAgentRuntimeSkillEnabled)
					r.Delete("/skills/{skillId}", h.RemoveAgentSkill)
					// 分配给该 agent 的工作区 MCP server。与上面的 skills 路由对位：库条目
					// 在加进这里之前不生效，绑定带自己的启用开关。
					r.Get("/mcp-servers", h.ListAgentMcpServers)
					r.Post("/mcp-servers", h.AddAgentMcpServer)
					r.Put("/mcp-servers/{serverId}/enabled", h.SetAgentMcpServerEnabled)
					r.Delete("/mcp-servers/{serverId}", h.RemoveAgentMcpServer)
					// 专用的 env 管理端点。允许 agent owner 或工作区 owner/admin；agent
					// actor 被拒绝。每次查看/写入都审计进 activity_log。见 MUL-2600、
					// MUL-5438 与 internal/handler/agent_env.go。
					r.Get("/env", h.GetAgentEnv)
					r.Put("/env", h.UpdateAgentEnv)
				})
			})

			r.Route("/api/agent-builder/sessions", func(r chi.Router) {
				// 创建工作台里未完成的草稿。Builder 会话对所有聊天列表不可见（其载体
				// kind='system'），所以这是回到它的唯一路由。
				r.Get("/", h.ListAgentBuilderSessions)
				r.Post("/", h.CreateAgentBuilderSession)
				r.Patch("/{sessionId}/runtime", h.SwitchAgentBuilderRuntime)
				// 自动保存的配置，含用户已敲但未发送的编辑。经上面的列表读回。
				r.Put("/{sessionId}/draft", h.SaveAgentBuilderDraft)
			})

			// Skills
			r.Route("/api/skills", func(r chi.Router) {
				r.Get("/", h.ListSkills)
				r.Post("/", h.CreateSkill)
				r.Get("/search", h.SearchSkills)
				r.Post("/import", h.ImportSkill)
				r.Route("/{id}", func(r chi.Router) {
					r.Get("/", h.GetSkill)
					r.Put("/", h.UpdateSkill)
					r.Delete("/", h.DeleteSkill)
					r.Post("/refresh", h.RefreshSkill)
					r.Get("/labels", h.ListLabelsForSkill)
					r.Post("/labels", h.AttachLabelToSkill)
					r.Delete("/labels/{labelId}", h.DetachLabelFromSkill)
					r.Get("/files", h.ListSkillFiles)
					r.Put("/files", h.UpsertSkillFile)
					r.Post("/files/upload", h.UploadSkillFile)
					r.Put("/bundle", h.ReplaceSkillBundle)
					r.Get("/files/{fileId}/content", h.GetSkillFileContent)
					r.Get("/files/{fileId}/download", h.GetSkillFileContent)
					r.Delete("/files/{fileId}", h.DeleteSkillFile)
				})
			})

			// 仪表盘——"/{slug}/dashboard" 页的工作区级 token 与运行时长汇总。可选
			// ?project_id 过滤器把汇总限定到单个项目。
			r.Route("/api/dashboard", func(r chi.Router) {
				r.Get("/usage/daily", h.GetDashboardUsageDaily)
				r.Get("/usage/by-agent", h.GetDashboardUsageByAgent)
				r.Get("/agent-runtime", h.GetDashboardAgentRunTime)
				r.Get("/runtime/daily", h.GetDashboardRunTimeDaily)
				r.Get("/failures/daily", h.GetDashboardFailuresDaily)
				r.Get("/failures/by-agent", h.GetDashboardFailuresByAgent)
			})

			// Runtimes
			r.Route("/api/runtimes", func(r chi.Router) {
				r.Get("/", h.ListAgentRuntimes)
				r.Route("/{runtimeId}", func(r chi.Router) {
					r.Patch("/", h.UpdateAgentRuntime)
					r.Get("/usage", h.GetRuntimeUsage)
					r.Get("/usage/by-agent", h.GetRuntimeUsageByAgent)
					r.Get("/usage/by-hour", h.GetRuntimeUsageByHour)
					r.Get("/activity", h.GetRuntimeTaskActivity)
					r.Post("/update", h.InitiateUpdate)
					r.Get("/update/{updateId}", h.GetUpdate)
					r.Post("/models", h.InitiateListModels)
					r.Get("/models/{requestId}", h.GetModelListRequest)
					r.Post("/local-skills", h.InitiateListLocalSkills)
					r.Get("/local-skills/{requestId}", h.GetLocalSkillListRequest)
					r.Post("/local-skills/import", h.InitiateImportLocalSkill)
					r.Get("/local-skills/import/{requestId}", h.GetLocalSkillImportRequest)
					r.Delete("/", h.DeleteAgentRuntime)
					// DELETE 的确认变体：解绑绑定到该 runtime 的每个 agent（他们保留配置
					// 与聊天，需要新 runtime 才能再跑）、取消其任务、剥离其任务历史，然后
					// 删除 runtime——全部在一个事务里。DeleteRuntimeDialog 在严格 DELETE
					// 以 `runtime_has_active_agents` 拒绝、用户确认后使用它。
					r.Post("/unbind-agents-and-delete", h.UnbindAgentsAndDeleteRuntime)
					// 为按 archive-and-delete 契约构建的已安装客户端保留的旧路径
					// （MUL-5559 改的是行为名，不只是路由）。同一 handler。
					r.Post("/archive-agents-and-delete", h.UnbindAgentsAndDeleteRuntime)
				})
			})

			// Cloud Runtime fleet 代理。远程服务 URL 只在 SaaS API 节点上配置；
			// 自托管部署返回 503。
			r.Route("/api/cloud-runtime", func(r chi.Router) {
				r.Get("/", h.GetCloudRuntimeService)
				r.Get("/healthz", h.GetCloudRuntimeHealth)
				r.Get("/readyz", h.GetCloudRuntimeReady)
				r.Get("/nodes", h.ListCloudRuntimeNodes)
				r.Post("/nodes", h.CreateCloudRuntimeNode)
				r.Delete("/nodes", h.DeleteCloudRuntimeNode)
				r.Post("/nodes/start", h.StartCloudRuntimeNode)
				r.Post("/nodes/stop", h.StopCloudRuntimeNode)
				r.Post("/nodes/reboot", h.RebootCloudRuntimeNode)
				r.Post("/nodes/status", h.GetCloudRuntimeNodeStatus)
				r.Post("/nodes/exec", h.ExecCloudRuntimeNode)
			})

			// Tasks（面向用户，带归属检查）
			r.Post("/api/tasks/{taskId}/cancel", h.CancelTaskByUser)

			// 用于 presence 推导的工作区级 agent 任务快照：每条活跃任务 + 每个 agent
			// 最近一条终态任务。
			r.Get("/api/agent-task-snapshot", h.ListWorkspaceAgentTaskSnapshot)

			// 独立的、工作区级的列表，支撑 issues 头部的「agents working」芯片及其
			// assignee-id 表格过滤器。
			r.Get("/api/working-agents", h.ListWorkspaceWorkingAgents)

			// 工作区级每日 agent 活动（最近 30 天，锚定 completed_at）。同时支撑
			// Agents 列表迷你走势（近 7 天切片）与 agent 详情「最近 30 天」面板。
			r.Get("/api/agent-activity-30d", h.GetWorkspaceAgentActivity30d)

			// Agents 列表 RUNS 列用的、每个 agent 的工作区级 30 天运行计数。
			r.Get("/api/agent-run-counts", h.GetWorkspaceAgentRunCounts)

			r.Route("/api/chat/sessions", func(r chi.Router) {
				r.Post("/", h.CreateChatSession)
				r.Get("/", h.ListChatSessions)
				r.Route("/{sessionId}", func(r chi.Router) {
					r.Get("/", h.GetChatSession)
					r.Patch("/", h.UpdateChatSession)
					r.Patch("/pin", h.SetChatSessionPinned)
					r.Patch("/archive", h.SetChatSessionArchived)
					r.Delete("/", h.DeleteChatSession)
					r.Post("/messages", h.SendChatMessage)
					// 明确「刷新」某轮的 quick actions：对最新 assistant 回复重跑一次
					// daemon 建议流程（MUL-5149）。
					r.Post("/quick-actions/regenerate", h.RegenerateChatQuickActions)
					r.Get("/messages", h.ListChatMessages)
					r.Get("/messages/page", h.ListChatMessagesPage)
					r.Get("/pending-task", h.GetPendingChatTask)
					r.Delete("/queued-tasks", h.ClearQueuedChatTasks)
					r.Post("/queued-tasks/{taskId}/prioritize", h.PrioritizeQueuedChatTask)
					r.Post("/read", h.MarkChatSessionRead)
					// 延迟取消的草稿恢复（#5219）：仅创建者可取、且幂等地消费。
					r.Get("/draft-restores", h.ListChatDraftRestores)
					r.Delete("/draft-restores/{restoreId}", h.ConsumeChatDraftRestore)
				})
			})
			r.Get("/api/chat/pending-tasks", h.ListPendingChatTasks)
			r.Get("/api/chat/pending-tasks/has-any", h.HasPendingChatTasks)

			r.Get("/api/meals", h.GetMeals)
			r.Post("/api/meals/actions", h.MealAction)

			r.Get("/api/cooking/categories", h.CookingCategories)
			r.Get("/api/health/overview", h.GetHealth)
			r.Get("/api/health/profile", h.GetHealth)
			r.Get("/api/health/observations", h.GetHealth)
			r.Patch("/api/health/profile", h.UpdateHealthProfile)
			r.Post("/api/health/observations", h.SaveHealthObservation)
			r.Get("/api/health/revisions", h.GetHealthRevisions)
			r.Post("/api/health/revisions/restore", h.RestoreHealthRevision)
			r.Get("/api/health/reports", h.ListHealthReports)
			r.Post("/api/health/reports", h.UploadHealthReport)
			r.Get("/api/health/reports/{reportId}", h.GetHealthReport)
			r.Get("/api/health/reports/{reportId}/results", h.HealthReportResults)
			r.Get("/api/health/reports/{reportId}/file", h.HealthReportFile)
			r.Get("/api/health/reports/{reportId}/images/{page}", h.HealthReportFile)
			r.Post("/api/health/reports/{reportId}/process", h.ProcessHealthReport)
			r.Post("/api/health/reports/{reportId}/prepare", h.PrepareHealthReport)
			r.Get("/api/health/reports/{reportId}/pages", h.HealthReportPages)
			r.Post("/api/health/reports/{reportId}/submit", h.SubmitHealthReport)
			r.Post("/api/health/reports/{reportId}/apply", h.ApplyHealthReport)
			r.Get("/api/cooking/recipes", h.CookingList)
			r.Get("/api/cooking/recipes/{id}", h.CookingDetail)
			r.Get("/api/cooking/tips", h.CookingList)
			r.Get("/api/cooking/tips/{id}", h.CookingDetail)
			r.Get("/api/cooking/favorites", h.CookingList)
			r.Put("/api/cooking/favorites/{id}", h.CookingFavorite)
			r.Delete("/api/cooking/favorites/{id}", h.CookingFavorite)

			// Life 陪伴
			r.Route("/api/life", func(r chi.Router) {
				r.Get("/companion", h.GetCompanionProfile)
				r.Put("/companion", h.UpsertCompanionProfile)
				r.Get("/memories", h.ListLifeMemories)
				r.Post("/memories", h.CreateLifeMemory)
				r.Patch("/memories/{memoryId}", h.UpdateLifeMemory)
				r.Delete("/memories/{memoryId}", h.DeleteLifeMemory)
				r.Get("/memories/{memoryId}/revisions", h.ListLifeMemoryRevisions)
				r.Post("/memories/{memoryId}/confirm", h.ConfirmLifeMemory)
				r.Post("/memories/{memoryId}/downgrade", h.DowngradeLifeMemory)
				r.Post("/memories/{memoryId}/archive", h.ArchiveLifeMemory)
				r.Get("/identity/versions", h.ListLifeIdentityVersions)
				r.Post("/identity/versions", h.CreateLifeIdentityVersion)
				r.Post("/identity/versions/{versionId}/activate", h.ActivateLifeIdentityVersion)
				r.Get("/relationship-events", h.ListLifeRelationshipEvents)
				r.Post("/relationship-events/{eventId}/resolve", h.ResolveLifeRelationshipEvent)
				r.Get("/materials", h.ListLifeMaterials)
				r.Post("/materials", h.CreateLifeMaterial)
				r.Get("/topics", h.ListLifeTopics)
				r.Patch("/topics/{topicId}", h.UpdateLifeTopic)
				r.Get("/commitments", h.ListLifeCommitments)
				r.Patch("/commitments/{commitmentId}", h.UpdateLifeCommitment)
				r.Get("/internal-thoughts", h.ListLifeInternalThoughts)
				r.Get("/proposals", h.ListLifeActionProposals)
				r.Post("/proposals", h.CreateLifeActionProposal)
				r.Post("/proposals/{proposalId}/confirm", h.ConfirmLifeActionProposal)
				r.Post("/proposals/{proposalId}/reject", h.RejectLifeActionProposal)
				r.Get("/experiments", h.ListLifeExperiments)
				r.Get("/modules", h.ListLifeModules)
				r.Patch("/modules/{moduleId}", h.UpdateLifeModule)
				r.Post("/experiment-rounds/{roundId}/stop", h.StopLifeExperimentRound)
				r.Post("/experiment-rounds/{roundId}/review", h.ReviewLifeExperimentRound)
				r.Get("/proactive-checks", h.ListLifeProactiveChecks)
				r.Get("/proactive-policy", h.GetLifeProactivePolicy)
				r.Put("/proactive-policy", h.UpdateLifeProactivePolicy)
				r.Get("/observers", h.ListLifeObservers)
				r.Post("/observers", h.CreateLifeObserver)
				r.Patch("/observers/{observerId}", h.UpdateLifeObserver)
				r.Post("/observers/{observerId}/knowledge", h.AddLifeObserverKnowledge)
				r.Post("/observers/{observerId}/versions", h.CreateLifeObserverVersion)
				r.Post("/observers/{observerId}/run", h.RunLifeObserver)
				r.Get("/observation-seat", h.ListLifeObservationSeat)
				r.Patch("/observation-seat/topics/{topicId}", h.UpdateLifeObservationTopic)
				r.Get("/cognition-jobs", h.ListLifeCognitionJobs)
				r.Post("/cognition-jobs/{jobId}/retry", h.RetryLifeCognitionJob)
				r.Get("/upgrade-evaluations", h.ListLifeUpgradeEvaluations)
				r.Post("/upgrade-evaluations", h.CreateLifeUpgradeEvaluation)
				r.Get("/chronicle", h.ListLifeChronicleEntries)
				r.Post("/chronicle", h.CreateLifeChronicleEntry)
				r.Patch("/chronicle/{entryId}/later-understanding", h.UpdateLifeChronicleLaterUnderstanding)
				r.Post("/agent/evidence/resolve", h.ResolveLifeEvidence)
				r.Post("/agent/memory-candidates", h.CreateCompanionMemoryCandidate)
				r.Post("/agent/proposals", h.CreateCompanionActionProposal)
				r.Post("/agent/proposals/{proposalId}/present", h.PresentCompanionActionProposal)
				r.Post("/agent/proactive-checks", h.CreateCompanionProactiveCheck)
				r.Post("/agent/jobs/{jobId}/complete", h.CompleteCompanionCognitionJob)
			})

			// Quick-agent 栏：每用户固定的 agent，一键开新聊天。
			r.Get("/api/chat/pinned-agents", h.ListChatPinnedAgents)
			r.Post("/api/chat/pinned-agents", h.PinChatAgent)
			r.Delete("/api/chat/pinned-agents/{agentId}", h.UnpinChatAgent)

			// agent 面向的渠道读取（MUL-3871）。调用方的作用域化任务 token 解析到它自己
			// 的聊天会话；不传任何 session/channel id，所以 agent 只能读自己的会话。
			// `history` 是渠道概览（顶层消息 + 线程元数据）；`thread` 读某个线程
			// （?id 指定具体线程，否则读该会话所在的线程）。
			r.Get("/api/chat/history", h.GetChatChannelHistory)
			r.Get("/api/chat/thread", h.GetChatThread)

			// Inbox
			r.Route("/api/inbox", func(r chi.Router) {
				r.Get("/", h.ListInbox)
				// 归档通知，服务 inbox 的「Archived」子视图。与 "/" 分开，让主列表保持
				// 自身契约、永不携带无界归档。
				r.Get("/archived", h.ListArchivedInbox)
				r.Get("/unread-count", h.CountUnreadInbox)
				// 跨工作区未读汇总：账号级，按用户分键。支撑工作区切换器里「其他
				// 工作区」的圆点。
				r.Get("/unread-summary", h.UnreadInboxSummary)
				r.Post("/mark-all-read", h.MarkAllInboxRead)
				r.Post("/archive-all", h.ArchiveAllInbox)
				r.Post("/archive-all-read", h.ArchiveAllReadInbox)
				r.Post("/archive-completed", h.ArchiveCompletedInbox)
				r.Post("/{id}/read", h.MarkInboxRead)
				r.Post("/{id}/unread", h.MarkInboxUnread)
				r.Post("/{id}/archive", h.ArchiveInboxItem)
				r.Post("/{id}/unarchive", h.UnarchiveInboxItem)
			})

			// 通知偏好
			r.Route("/api/notification-preferences", func(r chi.Router) {
				r.Get("/", h.GetNotificationPreferences)
				r.Patch("/", h.PatchNotificationPreferences)
				r.Put("/", h.UpdateNotificationPreferences)
			})
		})
	})

	return r, h
}

// buildLarkConnector 接好直接与 /callback/ws/endpoint 通信的真实 WS 长连接连接器
// （app_id/app_secret 直连）。连接器每次读取都包一个 ctx-cancel 看门狗，使租约丢失/
// 关停能在有界时间内打断阻塞的 ReadMessage——§4.4 依赖的不变式。一个连接器实例服务
// 所有安装；其 Run 以安装为参数，feishuChannel 把逐安装的行交给它。
//
// 若端点抓取器初始化失败（典型是 MULTICA_LARK_CALLBACK_BASE_URL 配错），记日志并
// 回退到 NoopConnector，让租约/supervisor 生命周期仍对着真实 DB 行运转。入站消息在
// 配置修好前静默丢弃；启动日志以 "noop" 标注降级状态，使其可见。
//
// 返回连接器加一个给启动日志的短标签：健康时为 "ws-long-conn"，回退时为 "noop"。
func buildLarkConnector(installSvc *lark.InstallationService, apiClient lark.APIClient) (lark.EventConnector, string) {
	endpointFetcher, err := lark.NewHTTPConnectionTokenFetcher(lark.HTTPConnectionTokenConfig{
		BaseURL: strings.TrimSpace(os.Getenv("MULTICA_LARK_CALLBACK_BASE_URL")),
		Logger:  slog.Default(),
	})
	if err != nil {
		slog.Error("lark ws: endpoint fetcher init failed; falling back to noop", "error", err)
		return lark.NewNoopConnector(slog.Default()), "noop"
	}
	decoder := lark.NewLarkJSONFrameDecoder()
	dialer := lark.NewGorillaDialer()
	if proxyURL := strings.TrimSpace(os.Getenv("MULTICA_LARK_WS_PROXY_URL")); proxyURL != "" {
		dialer.ProxyURL = proxyURL
	}
	credsProvider := lark.CredentialsProviderFunc(func(ctx context.Context, inst lark.Installation) (lark.InstallationCredentials, error) {
		secret, err := installSvc.DecryptAppSecret(inst)
		if err != nil {
			return lark.InstallationCredentials{}, err
		}
		creds := lark.InstallationCredentials{
			AppID:     inst.AppID,
			AppSecret: secret,
			Region:    lark.RegionOrDefault(inst.Region),
		}
		if inst.TenantKey.Valid {
			creds.TenantKey = inst.TenantKey.String
		}
		return creds, nil
	})
	// 入站补全器：展开引用的回复/转发包，并在分发前经 IM API 把一段周边群历史
	// （MUL-3084）预取进 agent 的 body。它共享连接器解析出的凭据，并在连接器的
	// EnrichTimeout 下运行，因此不会超出 Lark 长连接的 ACK 预算。
	enricher := lark.NewInboundEnricher(apiClient, lark.InboundEnricherConfig{
		RecentContextSize: lark.DefaultRecentContextSize,
		Logger:            slog.Default(),
	})
	conn, err := lark.NewWSLongConnConnector(lark.WSConnectorConfig{
		Dialer:              dialer,
		EndpointFetcher:     endpointFetcher,
		FrameDecoder:        decoder,
		Enricher:            enricher,
		CredentialsProvider: credsProvider,
		Logger:              slog.Default(),
	})
	if err != nil {
		slog.Error("lark ws: connector init failed; falling back to noop", "error", err)
		return lark.NewNoopConnector(slog.Default()), "noop"
	}
	return conn, "ws-long-conn"
}

// membershipChecker 基于数据库查询实现 realtime.MembershipChecker。
type membershipChecker struct {
	queries *db.Queries
}

func (mc *membershipChecker) IsMember(ctx context.Context, userID, workspaceID string) bool {
	_, err := mc.queries.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{
		UserID:      parseUUID(userID),
		WorkspaceID: parseUUID(workspaceID),
	})
	return err == nil
}

// patResolver 基于数据库查询实现 realtime.PATResolver。
// patCache 与 Auth、DaemonAuth 中间件共享，任何路径的 token 吊销都会使三者缓存同时
// 失效。支持 nil 缓存，退化为直接查 DB。
type patResolver struct {
	queries *db.Queries
	cache   *auth.PATCache
}

func (pr *patResolver) ResolveToken(ctx context.Context, token string) (string, bool) {
	hash := auth.HashToken(token)

	if userID, ok := pr.cache.Get(ctx, hash); ok {
		return userID, true
	}

	pat, err := pr.queries.GetPersonalAccessTokenByHash(ctx, hash)
	if err != nil {
		return "", false
	}

	userID := util.UUIDToString(pat.UserID)

	var expiresAt time.Time
	if pat.ExpiresAt.Valid {
		expiresAt = pat.ExpiresAt.Time
	}
	pr.cache.Set(ctx, hash, userID, auth.TTLForExpiry(time.Now(), expiresAt))

	// Cache miss = first WS auth in this TTL window. Refresh last_used_at;
	// subsequent connects within the window skip the write.
	//
	// 中文说明：缓存未命中 = 本 TTL 窗口内的第一次 WS 鉴权。顺手刷新一下
	// last_used_at；窗口内后续连接会命中缓存，跳过这次写库。
	go pr.queries.UpdatePersonalAccessTokenLastUsed(context.Background(), pat.ID)

	return userID, true
}

// parseUUID 是 util.MustParseUUID 的薄别名。这里的调用点全是"库出身 UUID"的
// 内部往返（例如 issue.ID、e.ActorID），非法值只可能是编程错误，应当响亮地 panic。
func parseUUID(s string) pgtype.UUID {
	return util.MustParseUUID(s)
}

// optionalUUID 对空字符串返回 NULL 的 pgtype.UUID，否则与 parseUUID 行为一致。
// 用于事件里"操作者可能是 system（没有 member/agent 归属）"的 actor ID——
// activity_log 与 inbox_item 两表都允许 actor_id 为 NULL。
func optionalUUID(s string) pgtype.UUID {
	if s == "" {
		return pgtype.UUID{}
	}
	return util.MustParseUUID(s)
}

// splitAndTrim 把逗号分隔的字符串拆成去空白后的非空列表（空输入返回 nil）。
func splitAndTrim(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	res := make([]string, 0, len(parts))
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed != "" {
			res = append(res, trimmed)
		}
	}
	return res
}

// composioStateSecret 解析 connect-state 的 HMAC key：优先显式 COMPOSIO_STATE_SECRET，
// 否则用 SHA-256 从 JWT_SECRET 派生一个 composio 专用 key，保证两个签名域绝不共用
// 同一个 key。两者都没设时返回 nil（composio 保持禁用）。
func composioStateSecret() []byte {
	if v := strings.TrimSpace(os.Getenv("COMPOSIO_STATE_SECRET")); v != "" {
		return []byte(v)
	}
	if v := strings.TrimSpace(os.Getenv("JWT_SECRET")); v != "" {
		sum := sha256.Sum256([]byte("composio-state:" + v))
		return sum[:]
	}
	return nil
}

// composioCallbackBaseURL 解析拼 Composio 回调 URL 用的公开 API 基址：优先
// COMPOSIO_CALLBACK_BASE_URL，其次已解析出的 MULTICA_PUBLIC_URL，再退到应用 URL。
func composioCallbackBaseURL(publicURL string) string {
	if v := strings.TrimRight(strings.TrimSpace(os.Getenv("COMPOSIO_CALLBACK_BASE_URL")), "/"); v != "" {
		return v
	}
	if publicURL != "" {
		return publicURL
	}
	return appURLFromEnv()
}

// WecomRelay 是 WeCom 适配器从实时中继需要的接口两半：在 ScopeWecomOutbound 下发布，
// 以及注册消费"其它副本发布帧"的消费者。*realtime.ShardedStreamRelay 与
// *realtime.RedisRelay 都满足它。
type WecomRelay interface {
	PublishWithID(scopeType, scopeID, exclude string, frame []byte, id string) error
	SetWecomOutboundDeliverer(d realtime.WecomOutboundDeliverer)
}

// wecomMetricsOrNil 防止类型为 nil 的指针进入适配器的接口字段。nil 的
// *WecomMetrics 仍满足 wecom.Metrics，直接赋值会让适配器拿到"装着 nil 指针的非
// nil 接口"——在关闭了 /metrics 的部署上，第一次计数调用就会 panic。
func wecomMetricsOrNil(m *obsmetrics.WecomMetrics) wecom.Metrics {
	if m == nil {
		return nil
	}
	return m
}
