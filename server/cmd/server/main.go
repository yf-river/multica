package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/daemonws"
	"github.com/multica-ai/multica/server/internal/eventoutbox"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/handler"
	"github.com/multica-ai/multica/server/internal/logger"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	"github.com/multica-ai/multica/server/internal/realtime"
	"github.com/multica-ai/multica/server/internal/scheduler"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"github.com/redis/go-redis/v9"
)

var (
	version = "dev"
	commit  = "unknown"
)

func newNamedRedisClient(base *redis.Options, suffix string) *redis.Client {
	opts := *base
	opts.ClientName = redisClientName(opts.ClientName, suffix)
	return redis.NewClient(&opts)
}

func redisClientName(existing, suffix string) string {
	if suffix == "" {
		return existing
	}
	if existing != "" {
		return existing + ":" + suffix
	}
	return "multica-api:" + suffix
}

func closeRedisClient(label string, client *redis.Client) {
	if client == nil {
		return
	}
	if err := client.Close(); err != nil {
		slog.Warn("redis client close failed", "client", label, "error", err)
	}
}

func registerDurableEvents(dispatcher *eventoutbox.Dispatcher, name string, consumer eventoutbox.Consumer, eventTypes ...string) error {
	for _, eventType := range eventTypes {
		if err := dispatcher.Register(eventType, name, consumer); err != nil {
			return err
		}
	}
	return nil
}

func envPositiveInteger[T ~int | ~int32 | ~int64](name string, def T) T {
	raw := os.Getenv(name)
	if raw == "" {
		return def
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	converted := T(v)
	if err != nil || v <= 0 || int64(converted) != v {
		slog.Warn("invalid env var, using default", "name", name, "value", raw, "default", def, "error", err)
		return def
	}
	return converted
}

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

func main() {
	logger.Init()

	jwtSecret := os.Getenv("JWT_SECRET")
	if err := auth.ValidateJWTConfiguration(os.Getenv("APP_ENV"), jwtSecret); err != nil {
		slog.Error("invalid production authentication configuration", "error", err)
		os.Exit(1)
	}
	if err := auth.ValidateJWTSecret(jwtSecret); err != nil {
		slog.Warn("insecure JWT configuration allowed outside production", "reason", err)
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://multica:multica@localhost:5432/multica?sslmode=disable"
	}

	// Connect to database
	ctx := context.Background()
	pool, err := newDBPool(ctx, dbURL)
	if err != nil {
		slog.Error("unable to connect to database", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		slog.Error("unable to ping database", "error", err)
		os.Exit(1)
	}
	slog.Info("connected to database")
	logPoolConfig(pool)

	bus := events.New()
	hub := realtime.NewHub()
	go hub.Run()
	daemonHub := daemonws.NewHub()
	var daemonWakeup service.TaskWakeupNotifier = daemonHub

	// When REDIS_URL is set, route fanout through the fixed-shard Redis relay
	// so multiple API nodes can deliver each other's events. Without Redis the
	// local hub is the sole broadcaster and the server runs in single-node mode.
	// Runtime local-skill stores and realtime relay traffic use separate Redis
	// clients so blocking stream consumers cannot starve request-path Redis
	// operations.
	relayCtx, relayCancel := context.WithCancel(context.Background())
	var broadcaster realtime.Broadcaster = hub
	var storeRedis *redis.Client
	var relayWriteRedis *redis.Client
	var relayReadRedis *redis.Client
	var relay *realtime.ShardedStreamRelay
	defer func() {
		if relay != nil {
			relay.Stop()
		}
		relayCancel()
		if relay != nil {
			relay.Wait()
		}
		closeRedisClient("realtime-read", relayReadRedis)
		closeRedisClient("realtime-write", relayWriteRedis)
		closeRedisClient("store", storeRedis)
	}()
	if redisURL := os.Getenv("REDIS_URL"); redisURL != "" {
		opts, err := redis.ParseURL(redisURL)
		if err != nil {
			slog.Error("invalid REDIS_URL — falling back to in-memory hub", "error", err)
		} else {
			storeRedis = newNamedRedisClient(opts, "store")
			relayWriteRedis = newNamedRedisClient(opts, "realtime-write")
			relayReadRedis = newNamedRedisClient(opts, "realtime-read")
			sharded := realtime.NewShardedStreamRelay(hub, relayWriteRedis, relayReadRedis)
			sharded.SetDaemonRuntimeDeliverer(daemonHub)
			relay = sharded
			daemonWakeup = daemonws.NewRelayNotifier(daemonHub, sharded)
			relay.Start(relayCtx)
			broadcaster = realtime.NewDualWriteBroadcaster(hub, relay)
			slog.Info(
				"realtime: Redis relay enabled",
				"node_id", relay.NodeID(),
				"mode", "sharded",
				"store_pool_size", opts.PoolSize,
				"realtime_write_pool_size", opts.PoolSize,
				"realtime_read_pool_size", opts.PoolSize,
			)
		}
	} else {
		slog.Info("realtime: REDIS_URL not set — using in-memory hub (single-node mode)")
	}
	registerListeners(bus, broadcaster)

	analyticsClient := analytics.NewFromEnv()
	defer analyticsClient.Close()

	queries := db.New(pool)
	hub.SetAuthorizer(&dbScopeAuthorizer{q: queries})
	eventDispatcher, err := eventoutbox.NewDispatcher(
		queries,
		pool,
		bus,
		"api-"+uuid.NewString(),
		eventoutbox.DispatcherConfig{},
	)
	if err != nil {
		slog.Error("initialize domain event dispatcher", "error", err)
		os.Exit(1)
	}
	for _, registration := range []struct {
		failureMessage string
		run            func(*eventoutbox.Dispatcher) error
	}{
		{"register durable audience consumers", registerDurableAudienceConsumers},
		{"register durable activity consumers", registerDurableActivityConsumers},
		{"register durable chat consumers", registerDurableChatConsumers},
		{"register durable prompt evaluation consumers", registerDurablePromptEvaluationConsumers},
		{"register durable quick-create consumers", registerDurableQuickCreateConsumers},
		{"register durable autopilot consumers", registerDurableAutopilotConsumers},
		{"register durable reaction consumers", registerDurableReactionConsumers},
	} {
		if err := registration.run(eventDispatcher); err != nil {
			slog.Error(registration.failureMessage, "error", err)
			os.Exit(1)
		}
	}

	metricsConfig := obsmetrics.ConfigFromEnv()
	var metricsServer *http.Server
	var httpMetrics *obsmetrics.HTTPMetrics
	var businessMetrics *obsmetrics.BusinessMetrics
	var samplerPool *pgxpool.Pool
	if metricsConfig.Enabled() {
		// Build a dedicated tiny pool for the BusinessSamplerCollector
		// so a stalled scrape can never starve business traffic. If the
		// pool fails to construct we log and continue without the
		// sampler — the rest of /metrics is still useful.
		var err error
		samplerPool, err = newSamplerDBPool(ctx, dbURL)
		if err != nil {
			slog.Warn("metrics: failed to build sampler pgxpool; sampler disabled", "error", err)
			samplerPool = nil
		}

		metricsRegistry := obsmetrics.NewRegistry(obsmetrics.RegistryOptions{
			Pool:       pool,
			OutboxPool: samplerPool,
			Realtime:   realtime.M,
			DaemonWS:   daemonws.M,
			Version:    version,
			Commit:     commit,
			BusinessSampler: func() *obsmetrics.BusinessSamplerOptions {
				if samplerPool == nil {
					return nil
				}
				return &obsmetrics.BusinessSamplerOptions{Pool: samplerPool}
			}(),
		})
		httpMetrics = metricsRegistry.HTTP
		businessMetrics = metricsRegistry.Business
		// Forward inbound daemon WS frames into the per-kind counter so
		// dashboards can split heartbeat / unknown / invalid traffic.
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
	if samplerPool != nil {
		defer samplerPool.Close()
	}

	// Construct the BatchedHeartbeatScheduler before the router so it can
	// be injected into the Handler. The Run goroutine starts below
	// alongside the sweeper, and Stop is called explicitly during graceful
	// shutdown so any pending bumps are flushed before we exit.
	heartbeatScheduler := handler.NewBatchedHeartbeatScheduler(queries, handler.DefaultHeartbeatBatchInterval)

	r, h, err := NewRouterWithOptions(pool, hub, bus, analyticsClient, storeRedis, RouterOptions{
		HTTPMetrics:        httpMetrics,
		BusinessMetrics:    businessMetrics,
		DaemonHub:          daemonHub,
		DaemonWakeup:       daemonWakeup,
		HeartbeatScheduler: heartbeatScheduler,
		EventDispatcher:    eventDispatcher,
	})
	if err != nil {
		slog.Error("invalid server configuration", "error", err)
		os.Exit(1)
	}

	srv := &http.Server{
		Addr:    ":" + port,
		Handler: r,
	}

	// Start background workers.
	sweepCtx, sweepCancel := context.WithCancel(context.Background())
	autopilotCtx, autopilotCancel := context.WithCancel(context.Background())
	taskSvc := service.NewTaskService(queries, pool, hub, bus, daemonWakeup)
	taskSvc.Analytics = analyticsClient
	taskSvc.Metrics = businessMetrics
	autopilotSvc := service.NewAutopilotService(queries, pool, bus, taskSvc)
	// Analytics stays outside the durable projection transaction.
	bus.Subscribe(protocol.EventAutopilotRunDone, func(event events.Event) {
		autopilotSvc.CaptureAutopilotRunDone(context.Background(), event)
	})

	// Construct a LivenessStore that mirrors the one wired into the HTTP
	// handler. Both the heartbeat write path (handler) and the sweeper read
	// path (here) must agree on the same Redis-or-Noop choice; if they
	// disagree, online runtimes get falsely marked offline.
	liveness := handler.NewNoopLivenessStore()
	if storeRedis != nil {
		liveness = handler.NewRedisLivenessStore(storeRedis)
	}

	// Start background sweeper to mark stale runtimes as offline.
	go runRuntimeSweeper(sweepCtx, queries, liveness, taskSvc, bus)
	go eventDispatcher.Run(sweepCtx)
	go heartbeatScheduler.Run(sweepCtx)
	go runAutopilotScheduler(autopilotCtx, queries, autopilotSvc)
	go runAutopilotFailureMonitor(autopilotCtx, queries, bus, productionFailureMonitorConfig())
	go runDBStatsLogger(sweepCtx, pool)

	// Lark inbound supervisor: holds the §4.4 WS lease per installation
	// and runs the event connector for each. Nil when the Lark master
	// key is unset — self-host deployments that have not opted in to
	// Lark do not pay any goroutine cost. Lifecycle is bound to
	// sweepCtx so the Hub winds down alongside the other long-running
	// workers, AFTER the HTTP server has drained.
	if h.LarkHub != nil {
		go h.LarkHub.Run(sweepCtx)
	}

	// MUL-2957: DB-backed execution scheduler. The scheduler turns the
	// `sys_cron_executions` table into the distributed lease + audit
	// log for internal periodic jobs. Usage rollup and durable request retention
	// share this one scheduling boundary; the rollup SQL additionally holds
	// advisory lock 4246 so manual invocations cannot race the scheduler.
	//
	// Registration failures identify the job by name and do not disable other
	// valid jobs. Once running, the manager
	// surfaces transient errors — DB unreachable, sys_cron_executions
	// missing because of an unusual partial-migration state — by
	// logging them on the tick that fails and retrying on the next
	// cycle, so a temporary outage does not crash the server.
	schedulerMgr := scheduler.NewManager(pool, scheduler.Options{})
	registeredJobs := 0
	for _, job := range []scheduler.JobSpec{
		scheduler.TaskUsageHourlyJob(pool),
		scheduler.ResourceCreateRequestRetentionJob(pool),
	} {
		if err := schedulerMgr.Register(job); err != nil {
			slog.Error("scheduler: failed to register job", "job", job.Name, "error", err)
			continue
		}
		registeredJobs++
	}
	if registeredJobs > 0 {
		go func() {
			_ = schedulerMgr.Run(sweepCtx)
		}()
	}

	if metricsServer != nil {
		go func() {
			slog.Info("metrics server starting", "addr", metricsConfig.Addr)
			if err := metricsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				slog.Error("metrics server disabled after startup error", "error", err)
			}
		}()
	}

	go func() {
		slog.Info("server starting", "port", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	slog.Info("shutting down server")
	autopilotCancel()

	// Order matters: drain in-flight HTTP first so any heartbeat handlers
	// finish calling Schedule() before we stop the scheduler. Otherwise a
	// late heartbeat could enqueue a pending ID after Run has already
	// drained and exited, and Stop() would not flush it.
	apiShutdownCtx, apiShutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := srv.Shutdown(apiShutdownCtx); err != nil {
		apiShutdownCancel()
		slog.Error("server forced to shutdown", "error", err)
		os.Exit(1)
	}
	apiShutdownCancel()

	// HTTP is fully drained — safe to stop the sweeper and flush the
	// final batch of queued heartbeat bumps.
	sweepCancel()
	heartbeatScheduler.Stop()

	// Join the Lark Hub's per-installation supervisor goroutines so the
	// lease renewer can issue a final release before process exit;
	// otherwise the next replica would have to wait the full LeaseTTL
	// before picking up the installation on the other side of the
	// redeploy. The wait is bounded — if a supervisor is wedged (DB
	// pool stalled, a future connector ignoring ctx, etc.)
	// the fallback is the natural LeaseTTL expiry on the other side,
	// which is strictly better than holding shutdown open forever.
	if h.LarkHub != nil {
		if !h.LarkHub.WaitWithTimeout(h.LarkHub.ShutdownTimeout()) {
			slog.Warn("lark hub: supervisors did not exit within shutdown timeout; proceeding",
				"timeout", h.LarkHub.ShutdownTimeout().String(),
			)
		}
	}

	if metricsServer != nil {
		metricsShutdownCtx, metricsShutdownCancel := context.WithTimeout(context.Background(), 3*time.Second)
		if err := metricsServer.Shutdown(metricsShutdownCtx); err != nil {
			slog.Error("metrics server forced to shutdown", "error", err)
		}
		metricsShutdownCancel()
	}
	slog.Info("server stopped")
}
