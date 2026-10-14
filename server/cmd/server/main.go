// main.go — 服务端进程的装配入口。
//
// 职责：把 API 服务器需要的所有零件按正确顺序接起来再启动——日志、JWT 密钥
// 校验、特性开关、数据库连接池（含启动重试）、进程内事件总线、实时 hub、
// 可选的 Redis 实时中继（多节点横向扩容，见 MUL-1138）、企微（WeCom）跨副本
// 派发、outbox 可靠事件派发器及其各类投影消费者、metrics/pprof 服务器、以及
// 一大票后台 worker（runtime 清扫、来源上下文清理、心跳批量落库、autopilot
// 失败监控、DB 定时调度器等）。进程收到 SIGINT/SIGTERM 后按 shutdown.go 里
// 固化的顺序优雅退出。main 保持薄，复杂装配的"为什么"见各处行内注释。
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/daemonws"
	"github.com/multica-ai/multica/server/internal/dbstartup"
	"github.com/multica-ai/multica/server/internal/eventoutbox"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/handler"
	"github.com/multica-ai/multica/server/internal/integrations/wecom"
	"github.com/multica-ai/multica/server/internal/logger"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	"github.com/multica-ai/multica/server/internal/profiling"
	"github.com/multica-ai/multica/server/internal/realtime"
	"github.com/multica-ai/multica/server/internal/scheduler"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/featureflag"
	"github.com/multica-ai/multica/server/pkg/llm"
	"github.com/redis/go-redis/v9"
)

var (
	version = "dev"
	commit  = "unknown"
)

// newNamedRedisClient 基于一份基础配置创建带"命名客户端"（CLIENT SETNAME）的
// Redis 客户端。名字带上 suffix 便于在 Redis 侧区分用途；可用
// REDIS_DISABLE_CLIENT_NAME 关掉（托管 Redis 可能不允许 SETNAME）。
func newNamedRedisClient(base *redis.Options, suffix string) *redis.Client {
	opts := *base
	if envBool("REDIS_DISABLE_CLIENT_NAME", false) {
		opts.ClientName = ""
	} else {
		opts.ClientName = redisClientName(opts.ClientName, suffix)
	}
	return redis.NewClient(&opts)
}

// redisClientName 拼接客户端名：已有名字则追加 ":"+suffix，否则用
// "multica-api:"+suffix。
func redisClientName(existing, suffix string) string {
	if suffix == "" {
		return existing
	}
	if existing != "" {
		return existing + ":" + suffix
	}
	return "multica-api:" + suffix
}

// channelLeaseRedisURLFromEnv 返回频道 WS 租约专用的 Redis 地址：优先
// CHANNEL_WS_LEASE_REDIS_URL，未配则退回普通 REDIS_URL。
func channelLeaseRedisURLFromEnv() string {
	if dedicated := strings.TrimSpace(os.Getenv("CHANNEL_WS_LEASE_REDIS_URL")); dedicated != "" {
		return dedicated
	}
	return strings.TrimSpace(os.Getenv("REDIS_URL"))
}

// realtimeRelayRedisURLFromEnv 返回实时中继专用的 Redis 地址：优先
// REALTIME_RELAY_REDIS_URL，未配则退回普通 REDIS_URL。
func realtimeRelayRedisURLFromEnv() string {
	if dedicated := strings.TrimSpace(os.Getenv("REALTIME_RELAY_REDIS_URL")); dedicated != "" {
		return dedicated
	}
	return strings.TrimSpace(os.Getenv("REDIS_URL"))
}

// closeRedisClient 安全关掉一个 Redis 客户端并记日志（可空）。
func closeRedisClient(label string, client *redis.Client) {
	if client == nil {
		return
	}
	if err := client.Close(); err != nil {
		slog.Warn("redis client close failed", "client", label, "error", err)
	}
}

// shardedRelayConfigFromEnv 从环境变量读取分片中继（Redis Streams）的配置，
// 非法项打 warn 后归一化为安全值。
func shardedRelayConfigFromEnv() realtime.ShardedStreamRelayConfig {
	cfg := realtime.DefaultShardedStreamRelayConfig()
	cfg.Shards = envPositiveInt("REALTIME_RELAY_SHARDS", cfg.Shards)
	cfg.StreamMaxLen = envPositiveInt64("REALTIME_RELAY_STREAM_MAXLEN", cfg.StreamMaxLen)
	cfg.ReadCount = envPositiveInt64("REALTIME_RELAY_XREAD_COUNT", cfg.ReadCount)
	cfg.ReadBlock = envDuration("REALTIME_RELAY_XREAD_BLOCK", cfg.ReadBlock)
	cfg.ReplayGrace = envDuration("REALTIME_RELAY_REPLAY_GRACE", cfg.ReplayGrace)
	cfg.TrimHorizon = envDuration("REALTIME_RELAY_TRIM_HORIZON", 2*cfg.ReplayGrace)
	cfg.StreamTTL = envDuration("REALTIME_RELAY_STREAM_TTL", cfg.TrimHorizon+cfg.ReplayGrace)
	cfg.TTLRefreshInterval = envDuration("REALTIME_RELAY_TTL_REFRESH_INTERVAL", cfg.TTLRefreshInterval)
	cfg.MaintenanceInterval = envDuration("REALTIME_RELAY_MAINTENANCE_INTERVAL", cfg.MaintenanceInterval)
	cfg.StreamTTLEnabled = envBool("REALTIME_RELAY_STREAM_TTL_ENABLED", false)
	if err := cfg.Validate(); err != nil {
		slog.Warn("invalid realtime relay retention config; normalizing to safe values", "error", err)
	}
	return cfg.Normalized()
}

// realtimeRelayModeFromEnv 读取中继模式（sharded/dual/legacy），非法值打 warn
// 回退 sharded。
func realtimeRelayModeFromEnv() string {
	const defaultMode = "sharded"
	raw := strings.ToLower(strings.TrimSpace(os.Getenv("REALTIME_RELAY_MODE")))
	if raw == "" {
		return defaultMode
	}
	switch raw {
	case "sharded", "dual", "legacy":
		return raw
	default:
		slog.Warn("invalid env var, using default", "name", "REALTIME_RELAY_MODE", "value", raw, "default", defaultMode)
		return defaultMode
	}
}

// envPositiveInt 读取正整数环境变量；空值回默认，非法/非正打 warn 回退默认。
func envPositiveInt(name string, def int) int {
	raw := os.Getenv(name)
	if raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v <= 0 {
		slog.Warn("invalid env var, using default", "name", name, "value", raw, "default", def, "error", err)
		return def
	}
	return v
}

// envNonNegativeInt 读取允许为 0 的整数环境变量（0 常用于关功能）。
func envNonNegativeInt(name string, def int) int {
	raw := os.Getenv(name)
	if raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < 0 {
		slog.Warn("invalid env var, using default", "name", name, "value", raw, "default", def, "error", err)
		return def
	}
	return v
}

// maxLLMRetriesLimit caps MULTICA_LLM_MAX_RETRIES. The ceiling is a latency
// budget, not a taste call: SDK backoff is 0.5s doubling to an 8s cap, so 6
// retries spend ~21s and 10 spend ~48s sleeping before the last attempt. Every
// internal caller of pkg/llm runs under a far tighter deadline (8s for chat
// quick actions, 20s for title generation), so a budget past 5 cannot finish —
// it only converts a retryable upstream failure into a deadline-exceeded one.
//
// 中文说明：这是 MULTICA_LLM_MAX_RETRIES 的上限。它是"延迟预算"而不是口味
// 取舍：SDK 退避是 0.5 秒起、翻倍到 8 秒封顶，所以 6 次重试要睡约 21 秒、
// 10 次要睡约 48 秒才轮到最后一次尝试。pkg/llm 的所有内部调用方跑在紧得多
// 的时限里（聊天快捷操作 8 秒、标题生成 20 秒），因此超过 5 的预算根本跑不
// 完——只会把"可重试的上游失败"变成"超时失败"。
const maxLLMRetriesLimit = 5

// parseLLMMaxRetries 把原始 MULTICA_LLM_MAX_RETRIES 值转成 llm.Config.MaxRetries
// 需要的三态：空串→nil（用默认值）；0→llm.Retries(0)（关掉重试）；N→
// llm.Retries(N)（上限 N）。
//
// 与上面那些 envFooInt 助手不同，它返回 error 而不是打 warn 后回退默认值。
// "把重试预算悄悄改成一个运维没要求的值"正是这个旋钮要消除的故障
// （MUL-6364）：打错成 "3x" 或填了负数必须让启动停下，而不是悄悄恢复默认、
// 看起来像配置好了。
func parseLLMMaxRetries(raw string) (*llm.RetryOverride, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return nil, fmt.Errorf("must be an integer, got %q", raw)
	}
	if v > maxLLMRetriesLimit {
		return nil, fmt.Errorf("must be at most %d, got %d", maxLLMRetriesLimit, v)
	}
	// llm.Retries owns the lower bound. It is the boundary that makes a negative
	// budget unrepresentable, and this is the only place the server builds one,
	// so the deployment-specific ceiling above and the type-level floor here
	// cannot disagree.
	//
	// 中文说明：下限由 llm.Retries 的类型自己保证——负的预算在类型层就表达不
	// 了。而这是服务端唯一构造 RetryOverride 的地方，所以上面"按部署定的上限"
	// 和这里"类型级的下限"永远不会打架。
	override, err := llm.Retries(v)
	if err != nil {
		return nil, fmt.Errorf("must not be negative, got %d (use 0 to disable retries)", v)
	}
	return override, nil
}

// envPositiveInt64 读取正 int64 环境变量；空值回默认，非法/非正打 warn 回退。
func envPositiveInt64(name string, def int64) int64 {
	raw := os.Getenv(name)
	if raw == "" {
		return def
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v <= 0 {
		slog.Warn("invalid env var, using default", "name", name, "value", raw, "default", def, "error", err)
		return def
	}
	return v
}

// envDuration 读取正时长环境变量；空值回默认，非法/非正打 warn 回退。
func envDuration(name string, def time.Duration) time.Duration {
	raw := os.Getenv(name)
	if raw == "" {
		return def
	}
	v, err := time.ParseDuration(raw)
	if err != nil || v <= 0 {
		slog.Warn("invalid env var, using default", "name", name, "value", raw, "default", def.String(), "error", err)
		return def
	}
	return v
}

// envNonNegativeDuration 读取允许为 0 的时长环境变量（0 常用来关掉延迟/等待）。
func envNonNegativeDuration(name string, def time.Duration) time.Duration {
	raw := os.Getenv(name)
	if raw == "" {
		return def
	}
	v, err := time.ParseDuration(raw)
	if err != nil || v < 0 {
		slog.Warn("invalid env var, using default", "name", name, "value", raw, "default", def.String(), "error", err)
		return def
	}
	return v
}

// holdBeforeShutdown 收到终止信号后先"按配置等一会儿"再真正退出，给部署系统
// （如滚动发布/健康摘除）留出把实例标记为离线的窗口。等待期间若又来第二个信号
// 则立即结束等待（用户想快速退出）。
func holdBeforeShutdown(sig os.Signal, signals <-chan os.Signal, duration time.Duration) {
	if duration <= 0 {
		return
	}
	slog.Info("termination signal received; holding before shutdown",
		"signal", sig.String(),
		"duration", duration.String(),
	)
	timer := time.NewTimer(duration)
	defer timer.Stop()

	select {
	case <-timer.C:
		slog.Info("shutdown hold complete", "duration", duration.String())
	case interruptSig := <-signals:
		slog.Info("shutdown hold interrupted by signal",
			"signal", interruptSig.String(),
			"configured_duration", duration.String(),
		)
	}
}

// envBool 读取布尔环境变量；空值回默认，非法打 warn 回退。
func envBool(name string, def bool) bool {
	raw := os.Getenv(name)
	if raw == "" {
		return def
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		slog.Warn("invalid env var, using default", "name", name, "value", raw, "default", def, "error", err)
		return def
	}
	return v
}

// backgroundServices 从 Handler 上取后台 worker 需要的两个服务句柄
// （TaskService、AutopilotService）。路由装配时把 EmptyClaim 缓存等注进了
// TaskService，这里必须复用同一个实例，不能另造。
func backgroundServices(h *handler.Handler) (*service.TaskService, *service.AutopilotService) {
	return h.TaskService, h.AutopilotService
}

// jwtSecretBootError 在 JWT_SECRET 与 APP_ENV 的组合不安全时返回非空错误：
// 生产环境绝不允许用空或公开已知的默认密钥启动（auth.ValidateJWTSecret）。
// 非生产环境保留历史 dev 兜底（见 auth.JWTSecret），只打 warn。
func jwtSecretBootError(jwtSecret, appEnv string) error {
	isProduction := strings.EqualFold(strings.TrimSpace(appEnv), "production")
	if !isProduction {
		return nil
	}
	return auth.ValidateJWTSecret(jwtSecret)
}

// newMainHTTPServer 用"生产超时默认值"构建对外 HTTP 服务器。这些值是承重墙
// 级别的安全设置而不是装饰性调参，所以收敛在 main() 与配置回归测试共用的
// 同一个 helper 里。
//
// 给请求头读取设上限是为了挡 Slowloris；IdleTimeout 放得比 metrics/profiling
// 服务器（30s）宽，照顾 keep-alive 密集的 CLI 与 daemon 客户端。ReadTimeout
// 与 WriteTimeout 刻意留 0——这个监听器上的 WebSocket 升级（/ws、
// /api/daemon/ws）不能被读/写超时中途杀掉。
func newMainHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
}

func main() {
	logger.Init()
	// 提醒缺配置：生产环境空/默认 JWT 密钥直接拒绝启动。
	if err := auth.ValidateEnvironmentID(); err != nil {
		slog.Error(err.Error())
		os.Exit(1)
	}
	if err := jwtSecretBootError(os.Getenv("JWT_SECRET"), os.Getenv("APP_ENV")); err != nil {
		slog.Error(
			"refusing to start: "+err.Error()+
				"; generate a strong secret with `openssl rand -hex 32` and set JWT_SECRET (see .env.example)",
			"app_env", os.Getenv("APP_ENV"),
		)
		os.Exit(1)
	}
	if os.Getenv("JWT_SECRET") == "" {
		slog.Warn("JWT_SECRET is not set — using insecure dev default (allowed only because APP_ENV is not production).")
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	shutdownHoldDuration := envNonNegativeDuration("MULTICA_SHUTDOWN_HOLD_DURATION", 0)

	// Feature flags: loaded once at startup from MULTICA_FEATURE_FLAGS_FILE
	// (a YAML rule set) with FF_<KEY> env overrides layered on top.
	// See server/pkg/featureflag for the schema and lifecycle rules.
	//
	// Booting the server without any flag config is intentional: when the
	// env var is unset, every IsEnabled call falls through to the caller's
	// default, so existing code paths are unchanged until someone adds a
	// rule. A misconfigured (malformed / missing) file surfaces as a hard
	// error so operators see misconfig the same way they do for any other
	// MULTICA_*_FILE knob.
	//
	// 中文说明：特性开关只在启动时从 MULTICA_FEATURE_FLAGS_FILE（YAML 规则集）
	// 加载一次，并用 FF_<KEY> 环境变量在其上叠加覆盖（schema 与生命周期见
	// server/pkg/featureflag）。不带任何开关配置启动是有意为之：环境变量没设时，
	// 每个 IsEnabled 调用都落到调用方默认值上，所以现有代码路径在有人加规则前
	// 保持原样。配置错误（文件格式坏/缺失）会以硬错误形式浮现，让运维像看待
	// 其它任何 MULTICA_*_FILE 旋钮一样看到误配置。
	flags, err := featureflag.NewServiceFromEnv(featureflag.WithLogger(slog.Default()))
	if err != nil {
		slog.Error("feature flag configuration failed to load", "error", err)
		os.Exit(1)
	}
	_ = flags // 由路由（opts.FeatureFlags）与服务端各开关点使用；见 server/pkg/featureflag

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://multica:multica@localhost:5432/multica?sslmode=disable"
	}

	startupSettings := dbstartup.SettingsFromEnv()
	pool, err := newDBPool(context.Background(), dbURL, startupSettings.ConnectTimeout)
	if err != nil {
		slog.Error("unable to connect to database", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	// 启动阶段的数据库重试：绑上 Ctrl-C/SIGTERM，可被中断。
	startupCtx, stopStartup := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	retryOptions := startupSettings.RetryOptions()
	retryOptions.ShouldRetry = dbstartup.IsTransientDatabaseError
	retryOptions.OnRetry = func(event dbstartup.RetryEvent) {
		slog.Warn("database unavailable during server startup; retrying",
			"attempt", event.Attempt,
			"retry_in", event.Delay,
			"error", event.Err,
		)
	}
	if err := dbstartup.Retry(startupCtx, retryOptions, pool.Ping); err != nil {
		stopStartup()
		slog.Error("unable to ping database", "error", err)
		os.Exit(1)
	}
	stopStartup()
	slog.Info("connected to database")
	logPoolConfig(pool)

	bus := events.New()
	hub := realtime.NewHub()
	go hub.Run()
	daemonHub := daemonws.NewHub()
	var daemonWakeup service.TaskWakeupNotifier = daemonHub
	// 没有 Redis 中继时这里就是 nil：单副本部署下，同一进程既发布完成事件又
	// 持有连接，不需要跨节点投递。
	var wecomRelay WecomRelay
	// 在这里（而不是 router 里）建 SendersRegistry，是因为中继的 shard 读取器
	// 先于 router 存在，而在它们启动之后才注册的 deliverer 会错过它们开启的
	// 重放窗口。这个注册表只是个无依赖的裸 map，可以这么早就造好往下传。
	wecomSenders := wecom.NewSendersRegistry()
	var wecomRelayOutbound *wecom.RelayOutbound

	// MUL-1138：设置了 REDIS_URL 时，让事件广播走 Redis 中继，使多个 API 节点
	// 能互相投递事件；没配则 hub 是唯一的广播器，服务保持单节点（legacy）。
	// runtime 的本地技能存储与实时中继流量各用独立的 Redis 客户端，阻塞式的
	// 流消费者才不会把请求路径上的 Redis 操作饿死。频道租约单独初始化，便于
	// 生产环境指向一台无驱逐策略的专用 Redis。
	relayCtx, relayCancel := context.WithCancel(context.Background())
	var broadcaster realtime.Broadcaster = hub
	var storeRedis *redis.Client
	var channelLeaseRedis *redis.Client
	var relayWriteRedis *redis.Client
	var relayReadRedis *redis.Client
	var shardedReadRedis *redis.Client
	var legacyReadRedis *redis.Client
	var relay realtime.ManagedRelay
	// stopRelay 停止中继读取器并排空 WeCom 派发器。它在 shutdown BODY 里、channel
	// supervisor 拆除之前被调用——派发器的排空要走 supervisor 拥有的 socket 发送，
	// 而每条受监管连接退出时会清掉自己的 sender，所以在拆除之后再排空，会对每个
	// 安装都看到 ownsSocket=false，把它存在意义（要救的消息）全部丢弃。defer 保证
	// 那些走不到 body 退出序列的提前 return 路径也能再安全调一次（Once 幂等）。
	var stopRelayOnce sync.Once
	stopRelay := func() {
		stopRelayOnce.Do(func() {
			if relay != nil {
				relay.Stop()
			}
			relayCancel()
			if relay != nil {
				relay.Wait()
			}
			// 必须赶在 Redis 客户端被关之前 join 派发器，否则排空会和它依赖的
			// 拆除过程赛跑。
			wecomRelayOutbound.Wait()
		})
	}
	defer func() {
		stopRelay()
		closeRedisClient("realtime-read-legacy", legacyReadRedis)
		closeRedisClient("realtime-read-sharded", shardedReadRedis)
		closeRedisClient("realtime-read", relayReadRedis)
		closeRedisClient("realtime-write", relayWriteRedis)
		closeRedisClient("channel-lease", channelLeaseRedis)
		closeRedisClient("store", storeRedis)
	}()
	sharedRedisURL := strings.TrimSpace(os.Getenv("REDIS_URL"))
	relayRedisURL := realtimeRelayRedisURLFromEnv()
	if (sharedRedisURL != "" || relayRedisURL != "") && envBool("REDIS_DISABLE_CLIENT_NAME", false) {
		slog.Info("redis: CLIENT SETNAME disabled (REDIS_DISABLE_CLIENT_NAME=true) for managed Redis compatibility")
	}
	if sharedRedisURL != "" {
		if opts, err := redis.ParseURL(sharedRedisURL); err != nil {
			slog.Error("invalid REDIS_URL — request-path Redis features disabled", "error", err)
		} else {
			storeRedis = newNamedRedisClient(opts, "store")
		}
	}
	if relayRedisURL != "" {
		opts, err := redis.ParseURL(relayRedisURL)
		if err != nil {
			slog.Error("invalid realtime relay Redis URL — falling back to in-memory hub", "error", err)
		} else {
			relayWriteRedis = newNamedRedisClient(opts, "realtime-write")

			relayMode := realtimeRelayModeFromEnv()
			relayConfig := shardedRelayConfigFromEnv()
			switch relayMode {
			case "legacy":
				relayReadRedis = newNamedRedisClient(opts, "realtime-read")
				relay = realtime.NewRedisRelayWithClientsAndConfig(hub, relayWriteRedis, relayReadRedis, relayConfig.RetentionConfig())
				slog.Info("daemon websocket wakeup: Redis fanout disabled in legacy realtime relay mode")
			case "dual":
				shardedReadRedis = newNamedRedisClient(opts, "realtime-read-sharded")
				legacyReadRedis = newNamedRedisClient(opts, "realtime-read-legacy")
				sharded := realtime.NewShardedStreamRelay(hub, relayWriteRedis, shardedReadRedis, relayConfig)
				sharded.SetDaemonRuntimeDeliverer(daemonHub)
				wecomRelay = sharded
				legacy := realtime.NewRedisRelayWithClientsAndConfig(hub, relayWriteRedis, legacyReadRedis, relayConfig.RetentionConfig())
				relay = realtime.NewMirroredRelay(sharded, legacy)
				daemonWakeup = daemonws.NewRelayNotifier(daemonHub, sharded)
			default:
				relayReadRedis = newNamedRedisClient(opts, "realtime-read")
				sharded := realtime.NewShardedStreamRelay(hub, relayWriteRedis, relayReadRedis, relayConfig)
				sharded.SetDaemonRuntimeDeliverer(daemonHub)
				wecomRelay = sharded
				relay = sharded
				daemonWakeup = daemonws.NewRelayNotifier(daemonHub, sharded)
			}
			if wecomRelay != nil {
				// LeaseSettle 取自 supervisor 自己的旋钮而不是这里的常量：重新
				// 让渡（re-offer）链条存在的意义是熬过租约搬迁，而那段耗时取决于
				// supervisor 的轮询间隔——部署可以调它。
				leaseSettle, err := channelLeasePollInterval()
				if err != nil {
					slog.Warn("wecom relay: unreadable lease poll interval, using the dispatcher default",
						"error", err)
					leaseSettle = 0
				}
				wecomRelayOutbound = wecom.NewRelayOutbound(wecomRelay,
					wecom.NewRedisDedupe(relayWriteRedis, 0, slog.Default()),
					wecom.RelayConfig{
						ReplayGrace: relayConfig.ReplayGrace,
						LeaseSettle: leaseSettle,
					}, slog.Default())
				wecomRelay.SetWecomOutboundDeliverer(wecomRelayOutbound)
				wecomRelayOutbound.Start(relayCtx)
			}
			// 到这一步所有 deliverer 都注册完了。shard 读取器打开的是重放窗口，
			// 在 Start 之后才注册的任何 deliverer 都会静默错过它。
			relay.Start(relayCtx)
			broadcaster = realtime.NewDualWriteBroadcaster(hub, relay)
			storePoolSize := 0
			if storeRedis != nil {
				storePoolSize = storeRedis.Options().PoolSize
			}
			slog.Info(
				"realtime: Redis relay enabled",
				"node_id", relay.NodeID(),
				"mode", relayMode,
				"dedicated_instance", strings.TrimSpace(os.Getenv("REALTIME_RELAY_REDIS_URL")) != "",
				"shards", relayConfig.Shards,
				"stream_max_len", relayConfig.StreamMaxLen,
				"replay_grace", relayConfig.ReplayGrace.String(),
				"trim_horizon", relayConfig.TrimHorizon.String(),
				"stream_ttl", relayConfig.StreamTTL.String(),
				"stream_ttl_enabled", relayConfig.StreamTTLEnabled,
				"xread_count", relayConfig.ReadCount,
				"xread_block", relayConfig.ReadBlock.String(),
				"store_pool_size", storePoolSize,
				"realtime_write_pool_size", opts.PoolSize,
				"realtime_read_pool_size", opts.PoolSize,
			)
		}
	} else {
		slog.Info("realtime: REDIS_URL and REALTIME_RELAY_REDIS_URL are unset — using in-memory hub (single-node mode)")
	}
	if strings.EqualFold(strings.TrimSpace(os.Getenv("CHANNEL_WS_LEASE_BACKEND")), "redis") {
		leaseRedisURL := channelLeaseRedisURLFromEnv()
		if leaseRedisURL == "" {
			slog.Error("channel leases: CHANNEL_WS_LEASE_REDIS_URL and REDIS_URL are unset")
		} else if opts, err := redis.ParseURL(leaseRedisURL); err != nil {
			slog.Error("channel leases: invalid Redis URL; supervisor will fail closed", "error", err)
		} else {
			channelLeaseRedis = newNamedRedisClient(opts, "channel-lease")
		}
	}
	registerListeners(bus, broadcaster)

	analyticsClient := analytics.NewFromEnv()
	defer analyticsClient.Close()

	queries := db.New(pool)
	hub.SetAuthorizer(newScopeAuthorizer(queries))
	// 可靠投影都注册在 outbox dispatcher 上。业务变更在同一个事务里把事件
	// 入队；下面的实时监听器只负责把已提交的结果扇出。
	eventDispatcher, err := eventoutbox.NewDispatcher(queries, pool, bus, fmt.Sprintf("api-%d", os.Getpid()))
	if err != nil {
		slog.Error("event outbox dispatcher unavailable", "error", err)
		os.Exit(1)
	}
	for _, register := range []func(*eventoutbox.Dispatcher) error{
		registerDurableAudienceConsumers,
		registerDurableActivityConsumers,
		registerDurableChatConsumers,
		registerDurableQuickCreateConsumers,
		registerDurableAutopilotConsumers,
		registerDurableReactionConsumers,
		registerDurableTerminalReceipts,
	} {
		if err := register(eventDispatcher); err != nil {
			slog.Error("register durable event consumer failed", "error", err)
			os.Exit(1)
		}
	}

	metricsConfig := obsmetrics.ConfigFromEnv()
	var metricsServer *http.Server
	var httpMetrics *obsmetrics.HTTPMetrics
	var businessMetrics *obsmetrics.BusinessMetrics
	var channelMediaMetrics *obsmetrics.ChannelMediaReconcilerMetrics
	var channelLeaseMetrics *obsmetrics.ChannelLeaseMetrics
	var wecomMetrics *obsmetrics.WecomMetrics
	if metricsConfig.Enabled() {
		metricsRegistry := obsmetrics.NewRegistry(obsmetrics.RegistryOptions{
			Pool:       pool,
			OutboxPool: pool,
			Realtime:   realtime.M,
			DaemonWS:   daemonws.M,
			Version:    version,
			Commit:     commit,
		})
		httpMetrics = metricsRegistry.HTTP
		businessMetrics = metricsRegistry.Business
		channelMediaMetrics = metricsRegistry.ChannelMedia
		channelLeaseMetrics = metricsRegistry.ChannelLease
		wecomMetrics = metricsRegistry.Wecom
		// 把入站 daemon WS 帧按种类计到计数器上，仪表盘就能拆分 heartbeat /
		// unknown / invalid 流量。
		if daemonHub != nil {
			daemonHub.SetMessageKindRecorder(businessMetrics)
		}
		metricsServer = obsmetrics.NewServer(metricsConfig.Addr, metricsRegistry.Gatherer)
		if !obsmetrics.IsLoopbackAddr(metricsConfig.Addr) {
			slog.Warn(
				"metrics listener is not loopback-only; restrict access with private networking, allowlists, or proxy auth",
				"addr", metricsConfig.Addr,
			)
		}
	}
	// BatchedHeartbeatScheduler 要在 router 之前构造，好注入 Handler。Run
	// goroutine 下面和清扫器一起启动；优雅退出时显式调 Stop，把排队的 bump
	// 都冲刷掉再退出。
	heartbeatScheduler := handler.NewBatchedHeartbeatScheduler(queries, handler.DefaultHeartbeatBatchInterval)

	// 在 router 存在之前先校验 LLM 重试预算：运维填了个我们兑现不了的值，就该
	// 像上面格式错误的特性开关文件一样让启动停下来。
	llmMaxRetries, err := parseLLMMaxRetries(os.Getenv("MULTICA_LLM_MAX_RETRIES"))
	if err != nil {
		slog.Error("invalid MULTICA_LLM_MAX_RETRIES", "error", err)
		os.Exit(1)
	}

	r, h := NewRouterWithOptions(pool, hub, bus, analyticsClient, storeRedis, RouterOptions{
		HTTPMetrics:         httpMetrics,
		BusinessMetrics:     businessMetrics,
		ChannelLeaseMetrics: channelLeaseMetrics,
		ChannelLeaseRedis:   channelLeaseRedis,
		WecomMetrics:        wecomMetrics,
		DaemonHub:           daemonHub,
		DaemonWakeup:        daemonWakeup,
		WecomSenders:        wecomSenders,
		WecomRelayOutbound:  wecomRelayOutbound,
		FeatureFlags:        flags,
		HeartbeatScheduler:  heartbeatScheduler,
		LLMMaxRetries:       llmMaxRetries,
	})

	srv := newMainHTTPServer(":"+port, r)
	profilingServer := profiling.NewServer()
	// 同机运行多个环境时分别指定诊断端口；监听地址始终限制在本机。
	if raw := os.Getenv("PPROF_PORT"); raw != "" {
		pprofPort, err := strconv.ParseUint(raw, 10, 16)
		if err != nil || pprofPort == 0 {
			slog.Error("PPROF_PORT must be an integer between 1 and 65535")
			os.Exit(1)
		}
		profilingServer.Addr = fmt.Sprintf("127.0.0.1:%d", pprofPort)
	}

	// 启动后台 worker。
	sweepCtx, sweepCancel := context.WithCancel(context.Background())
	autopilotCtx, autopilotCancel := context.WithCancel(context.Background())
	// 这里复用 router 里的服务。特别是 router 把 EmptyClaim 缓存注进了
	// TaskService；若为定时 autopilot 派发另造一个 TaskService，发送 daemon
	// 唤醒时就不会 bump 那个缓存的版本号——空闲 runtime 会一直拿到空 claim，
	// 直到缓存 TTL 过期。
	taskSvc, autopilotSvc := backgroundServices(h)

	// 构造与 HTTP handler 里一致的那份 LivenessStore。心跳写入路径（handler）
	// 与清扫器读取路径（这里）必须对"Redis 还是 Noop"达成一致；不一致会把
	// 在线 runtime 误标成离线。
	var liveness handler.LivenessStore = handler.NewNoopLivenessStore()
	if storeRedis != nil {
		liveness = handler.NewRedisLivenessStore(storeRedis)
	}

	// 启动后台清扫器：把陈旧 runtime 标离线。
	runtimeReconnectGrace := envDuration("MULTICA_RUNTIME_RECONNECT_GRACE", defaultRuntimeReconnectGrace)
	if runtimeReconnectGrace < minimumRuntimeReconnectGrace {
		slog.Warn("runtime reconnect grace is shorter than heartbeat freshness; clamping",
			"configured", runtimeReconnectGrace,
			"minimum", minimumRuntimeReconnectGrace,
		)
		runtimeReconnectGrace = minimumRuntimeReconnectGrace
	}
	// 排队任务现在与在途任务共用同一个"runtime 存活"信号来判过期，所以没有
	// 单独的队列 TTL 要调：忙碌的 runtime 保住积压，离开的 runtime 一次性清退
	// 它拥有的全部活。
	go runRuntimeSweeper(sweepCtx, queries, liveness, taskSvc, bus, runtimeReconnectGrace)
	// 7 天 runtime 保留清理不共享 30 秒存活 tick：它有界的小事务每小时独立跑
	// 一次，一轮慢 GC 不会拖慢离线检测或任务恢复。
	go runRuntimeGCSweeper(sweepCtx, pool, queries, taskSvc.Metrics, h)
	// 来源上下文清理是对象存储的活，所以给它独立 goroutine，而不是塞进 runtime
	// 清扫 tick。
	go runSourceContextSweeper(sweepCtx, taskSvc)
	go heartbeatScheduler.Run(sweepCtx)
	go runAutopilotFailureMonitorDurable(autopilotCtx, queries, pool, bus, envFailureMonitorConfig())
	go runDBStatsLogger(sweepCtx, pool)
	go eventDispatcher.Run(sweepCtx)
	go runLifeExperimentSweeper(sweepCtx, queries)
	go runLifeCognitionWorker(sweepCtx, queries, pool)
	if h.WebhookDeliveryWorker != nil {
		go h.WebhookDeliveryWorker.Run(sweepCtx)
	}
	if h.TelegramOutbound != nil {
		h.TelegramOutbound.Start(sweepCtx)
	}
	// GitHub PR 卡片 API 快照流水线（MUL-5265）：worker 池 + TTL 清理器。未配置
	// （没有 App 私钥）时是空操作。
	h.PRRefresh.Start(sweepCtx)

	// 频道入站 supervisor（MUL-3620）：为每个安装持有 §4.4 的 WS 租约并驱动每个
	// channel.Channel。它与具体渠道无关（不是 Lark 专属），但租约启动校验失败
	// 时保持 nil（尤其是 Redis fail-closed 的就绪检查）。没有平台注册或没有安装
	// 记录时它就空转。生命周期绑在 sweepCtx 上，随其它长跑 worker 一起收尾——
	// 在 HTTP 服务器排空之后。
	if h.ChannelSupervisor != nil {
		go h.ChannelSupervisor.Run(sweepCtx)
	}

	// 媒体"意图台账"对账器（PR #5580）：结算"已上传但没绑定"的频道媒体对象。
	// 独立 worker，对象存储延迟尖峰才不会饿死其它清扫器的节奏。
	go h.RunSkillAssetCleanup(sweepCtx)
	if h.ChannelMediaReconciler != nil {
		h.ChannelMediaReconciler.Metrics = channelMediaMetrics
		go h.ChannelMediaReconciler.Run(sweepCtx)
	}

	// MUL-2957：DB 支撑的执行调度器。scheduler 把 sys_cron_executions 表变成
	// 内部周期任务的分布式租约 + 审计日志。第一个任务是 rollup_task_usage_hourly，
	// 取代之前运维注册的 pg_cron 条目（并发跑也安全——SQL 函数持有咨询锁 4246）。
	//
	// 注册失败只在"注册"这一步视为致命（重复名字是唯一现实原因，说明是代码
	// bug）。一旦跑起来，manager 对瞬时错误——数据库不可达、或罕见的半迁移状态
	// 下缺 sys_cron_executions——的处理是在失败的那个 tick 记日志、下一个周期
	// 重试，因此短暂故障不会让服务器崩掉。
	schedulerMgr := scheduler.NewManager(pool, scheduler.Options{})
	if err := schedulerMgr.Register(scheduler.TaskUsageHourlyJob(pool)); err != nil {
		slog.Warn("scheduler: failed to register task_usage_hourly rollup job", "error", err)
	}
	// MUL-3551：定时 autopilot 派发跑在同一个 DB 调度器上。该 job 用自己的
	// plan_times（PlansForScope，每个触发器有自己的 cron 表达式，Cadence 规划器
	// 不适用）。崩溃恢复、occurrence 级幂等、租约抢夺、重试全部复用 manager +
	// sys_cron_executions——定时 autopilot 不再有单独的 goroutine。
	if err := schedulerMgr.Register(scheduler.AutopilotScheduleDispatchJob(pool, queries, autopilotSvc)); err != nil {
		slog.Warn("scheduler: failed to register autopilot_schedule_dispatch job", "error", err)
	}
	// Manifest 声明的插件调度共享同一套持久租约与重试机制。plugins_v1 未启用时
	// 该 job 是惰性的。
	if err := schedulerMgr.Register(scheduler.PluginHookScheduleDispatchJob(queries, h.PluginService)); err != nil {
		slog.Warn("scheduler: failed to register plugin_hook_schedule_dispatch job", "error", err)
	}
	go func() {
		_ = schedulerMgr.Run(sweepCtx)
	}()

	if metricsServer != nil {
		go func() {
			slog.Info("metrics server starting", "addr", metricsConfig.Addr)
			if err := metricsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				slog.Error("metrics server disabled after startup error", "error", err)
			}
		}()
	}

	go func() {
		slog.Info("pprof server starting", "addr", profilingServer.Addr)
		if err := profilingServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("pprof server disabled after startup error", "error", err)
		}
	}()

	go func() {
		slog.Info("server starting", "port", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	sig := <-quit
	holdBeforeShutdown(sig, quit, shutdownHoldDuration)
	// 恢复默认信号行为：优雅退出期间再来一个信号仍能终止进程，而不是留在 quit
	// 通道里没人读。
	signal.Stop(quit)

	slog.Info("shutting down server")

	// 下面的顺序就是契约，shutdown.go 是它被写明并钉死的地方。其中不自明的是
	// 一点：WeCom 派发器必须在 sweepCancel 之前排空，而不仅是先于 supervisor
	// 被 join——取消清扫器 context 本身就是让每条受监管连接清空自己 sender 的
	// 动作，过了那个点再排空就找不到 socket 可投了。
	shutdownSequence{
		StopAutopilot: autopilotCancel,
		DrainHTTP: func() {
			apiShutdownCtx, apiShutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
			if err := srv.Shutdown(apiShutdownCtx); err != nil {
				apiShutdownCancel()
				slog.Error("server forced to shutdown", "error", err)
				os.Exit(1)
			}
			apiShutdownCancel()
		},
		StopOutboundRelay: stopRelay,
		CancelWorkers:     sweepCancel,
		StopHeartbeats:    heartbeatScheduler.Stop,
		JoinWebhookWorker: func() {
			if h.WebhookDeliveryWorker != nil && !h.WebhookDeliveryWorker.WaitWithTimeout(5*time.Second) {
				slog.Warn("webhook delivery worker did not exit within shutdown timeout")
			}
		},
		JoinTelegram: func() {
			if h.TelegramOutbound != nil && !h.TelegramOutbound.WaitWithTimeout(5*time.Second) {
				slog.Warn("telegram outbound workers did not exit within shutdown timeout")
			}
		},
		// Join 是为了让租约续期器在退出前发出最后一次释放；否则重新部署后下一
		// 个副本要干等整个 LeaseTTL。有界：卡死的 supervisor 回退到自然过期，
		// 不会把退出一直吊着。
		JoinChannelSupervisor: func() {
			if h.ChannelSupervisor == nil {
				return
			}
			if !h.ChannelSupervisor.WaitWithTimeout(h.ChannelSupervisor.ShutdownTimeout()) {
				slog.Warn("channel supervisor: connections did not exit within shutdown timeout; proceeding",
					"timeout", h.ChannelSupervisor.ShutdownTimeout().String(),
				)
			}
		},
		DrainChannelRouter: func() {
			if h.ChannelSupervisor == nil || h.ChannelRouter == nil {
				return
			}
			drainCtx, drainCancel := context.WithTimeout(context.Background(), 10*time.Second)
			if !h.ChannelRouter.Drain(drainCtx) {
				slog.Warn("channel router: drain deadline reached; deferred media fallback remains durable")
			}
			drainCancel()
		},
		StopMetricsServer: func() {
			if metricsServer == nil {
				return
			}
			metricsShutdownCtx, metricsShutdownCancel := context.WithTimeout(context.Background(), 3*time.Second)
			if err := metricsServer.Shutdown(metricsShutdownCtx); err != nil {
				slog.Error("metrics server forced to shutdown", "error", err)
			}
			metricsShutdownCancel()
		},
		StopProfiling: func() {
			profilingShutdownCtx, profilingShutdownCancel := context.WithTimeout(context.Background(), 3*time.Second)
			if err := profilingServer.Shutdown(profilingShutdownCtx); err != nil {
				slog.Error("pprof server forced to shutdown", "error", err)
			}
			profilingShutdownCancel()
		},
	}.run()
	slog.Info("server stopped")
}
