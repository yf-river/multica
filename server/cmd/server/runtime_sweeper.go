// runtime_sweeper.go
//
// 职责：周期巡检"运行环境（runtime）/任务"的存活与干净状态。
// 这是守护进程（daemon）崩溃、被杀、或任务悬在非终态时兜底的"扫地机"：按 30
// 秒节奏依次处理——把心跳超时的 runtime 标记离线、把离线过久 runtime 上的任务
// 判失败、清理悬挂的重连重试、判掉卡在 dispatched/running 的陈旧任务、让过期
// 的排队任务失败、重放委派失败的恢复、以及收尾取消后迟迟没定论的聊天任务。
// 另外每整点单独跑一轮"运行时 GC"（7 天保留期的离线 runtime 清理），避免脏活
// 拖累延迟敏感的存活判断路径。判失败后的副作用（事件广播、agent 状态对账、
// Issue 回滚、自动重试）统一收敛在 TaskService.HandleFailedTasks 里。
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/handler"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"github.com/multica-ai/multica/server/pkg/redact"
)

const (
	// sweepInterval is how often we check for stale runtimes and tasks.
	// 中文说明：检查陈旧 runtime / 任务的主节奏，每 30 秒一轮。
	sweepInterval = 30 * time.Second
	// runtimeGCSweepInterval keeps seven-day retention cleanup off the
	// latency-sensitive 30-second liveness path. GC remains independently
	// bounded by runtimeGCTickTimeout once each hourly round begins.
	//
	// 中文说明：把"7 天保留期清理"挪出对延迟敏感的 30 秒存活路径，独立每小时
	// 跑一次；单轮之内还受 runtimeGCTickTimeout 约束。
	runtimeGCSweepInterval = time.Hour
	// staleThresholdSeconds marks runtimes offline if no heartbeat for this
	// long. The heartbeat timing derivation lives with the shared service
	// constant so every task release path uses the same eligibility window.
	//
	// 中文说明：runtime 超过这么久没心跳就被判离线。心跳时序的推导放在共享的
	// service 常量里，保证所有"释放任务"的路径用的是同一把尺子。
	staleThresholdSeconds = service.RuntimeClaimFreshnessSeconds
	// defaultRuntimeReconnectGrace keeps locally-running work alive through a
	// sustained API/network partition. Runtime state still flips offline after
	// staleThresholdSeconds so the UI remains truthful; only task termination is
	// delayed. A daemon that actually restarts bypasses this grace through the
	// explicit RecoverOrphanedTasksForRuntime path.
	//
	// 中文说明：默认重连宽限——当 API/网络长时间分区时，让"本机还在跑"的活
	// 先留着。runtime 状态仍会在 staleThresholdSeconds 后翻成离线（UI 保持真
	// 实），只是任务的终止被推迟。真正重启过的 daemon 会走显式的
	// RecoverOrphanedTasksForRuntime 路径绕开这个宽限。
	defaultRuntimeReconnectGrace = 3 * time.Hour
	// A reconnect grace below the heartbeat freshness window would allow the
	// stale-task backstop to fail work while the same runtime was still eligible
	// to claim new work. Clamp configuration to keep those policies ordered.
	//
	// 中文说明：重连宽限若小于心跳新鲜窗口，就可能出现"同一 runtime 一边还在
	// 认领新活、一边它的旧任务被兜底判失败"。把配置夹住，保证两条策略的先后
	// 逻辑不乱。
	minimumRuntimeReconnectGrace = time.Duration(staleThresholdSeconds) * time.Second
	// offlineTaskFailBatchSize bounds in-flight tasks failed for long-offline
	// runtimes in one tick.
	// 中文说明：每轮最多把多少"长期离线 runtime 的在途任务"判失败。
	offlineTaskFailBatchSize = 500
	// reconnectRetryExpireBatchSize bounds deferred recovery retries that reach
	// their terminal reconnect deadline in one tick.
	// 中文说明：每轮最多给多少"走到重连终点的延迟恢复重试"判失败。
	reconnectRetryExpireBatchSize = 500
	// offlineRuntimeTTLSeconds deletes offline runtimes with no active agents
	// after this duration. 7 days gives users plenty of time to restart daemons.
	// 中文说明：离线且没有活跃 agent 的 runtime，超过这个时长后删除。7 天给
	// 用户足够时间去重启 daemon。
	offlineRuntimeTTLSeconds = 7 * 24 * 3600.0
	// runtimeGCBatchSize bounds both the candidate scan and the number of
	// per-runtime transactions one sweeper tick may open. At the hourly cadence,
	// 500 preserves a theoretical capacity of 12,000 candidates per day; the
	// round timeout remains the hard bound on actual work.
	//
	// 中文说明：既限制候选扫描规模，也限制单轮巡检能开的逐 runtime 事务数。
	// 按小时节奏，500 理论上每天能处理 1.2 万候选；实际工作的硬上限还是轮内
	// 超时。
	runtimeGCBatchSize = 500
	// runtimeGCTickTimeout bounds each independent hourly GC round so lock
	// contention cannot occupy its worker indefinitely.
	// 中文说明：每轮小时级 GC 的总超时，防止锁竞争无限占用它的 worker。
	runtimeGCTickTimeout = 15 * time.Second
	// runtimeGCOperationTimeout prevents one contended or unhealthy runtime from
	// stalling every later GC candidate indefinitely.
	// 中文说明：单个 runtime 的操作超时，防止一个争用/不健康的 runtime 把后面
	// 所有 GC 候选无限拖住。
	runtimeGCOperationTimeout = 5 * time.Second
	// dispatchTimeoutSeconds fails tasks stuck in 'dispatched' beyond this.
	// The dispatched→running transition should be near-instant, so 5 minutes
	// means something went wrong (e.g. StartTask API call failed silently).
	//
	// 中文说明：卡在 dispatched 超过这个秒数的任务判失败。dispatched→running
	// 的切换本应瞬时完成，卡 5 分钟说明出事了（例如 StartTask API 静默失败）。
	dispatchTimeoutSeconds = 300.0
	// runningTimeoutSeconds fails tasks stuck in 'running' beyond this. It is a
	// coarse server-side backstop keyed on started_at, AND-gated by daemon
	// liveness (agent_runtime.last_seen_at freshness within
	// staleThresholdSeconds): a running task whose runtime is still
	// heartbeating is NEVER killed by this wall clock, even after the timeout
	// elapses. This is what lets healthy multi-hour research / training runs
	// survive on self-hosted deployments (MUL-4107) — the daemon itself
	// decides stuck-vs-long-running via its inactivity watchdogs (idle/tool),
	// so the server-side wall clock is only a defensive backstop for the
	// pathological case where a runtime row somehow retains status='online'
	// with a stale DB heartbeat for longer than this timeout. The primary
	// "daemon died" path is `sweepStaleRuntimes` in the same tick (Redis
	// liveness + DB stale + FailTasksForOfflineRuntimes), which typically
	// reclaims orphaned tasks within ~180s.
	//
	// 中文说明：卡在 running 超过这个秒数的任务判失败。这是一道以 started_at
	// 为基准的粗粒度服务端兜底，并用 daemon 存活作为"与门"（agent_runtime
	// 的 last_seen_at 是否在 staleThresholdSeconds 内新鲜）：只要 runtime 还在
	// 心跳，running 中的任务绝不被这个墙钟杀死——哪怕超时。这正是自托管部署里
	// 健康的多小时研究/训练任务能活下来的原因（MUL-4107）。判断"卡死 vs 长跑"
	// 由 daemon 自己的空闲/工具看门狗负责；服务端墙钟只防御那种病态情况：
	// runtime 行的 DB 心跳明明已陈旧、却还保持 online 超过该超时。主要的
	// "daemon 死了"路径是同 tick 里的 sweepStaleRuntimes（Redis 存活 + DB 陈旧
	// + FailTasksForOfflineRuntimes），通常约 180 秒内就能回收孤儿任务。
	runningTimeoutSeconds = 9000.0
	// queuedExpireBatchSize caps how many queued rows a single sweeper tick
	// transitions to failed. Keeps the sweep transaction short even when
	// the historical backlog is large (~89k at MUL-1899 baseline). At 30s
	// ticks and 500 rows/tick we drain 60k rows/hour worst case — plenty
	// of headroom for the documented backlog without monopolising DB CPU.
	//
	// 中文说明：单轮最多把多少排队行翻成失败。即使历史积压巨大（MUL-1899 基线
	// 约 8.9 万行）也保持事务短小。按 30 秒一轮、每轮 500 行，最坏一小时可消化
	// 6 万行——对文档里的积压绰绰有余，又不会独占数据库 CPU。
	queuedExpireBatchSize = 500
	// chatFinalizeGraceSeconds is how long a cancelled chat task's deferred
	// empty/non-empty judgment (#5219) waits for the daemon's cancel-ack
	// before the sweeper settles it. Covers the daemon's 5s cancellation
	// poll plus the bounded 10s+12s transcript drain wait (#5210) plus
	// network slack; past this the daemon is presumed dead or partitioned.
	//
	// 中文说明：被取消的聊天任务把"空/非空判定"（#5219）推迟执行，本常量是它
	// 等待 daemon 取消确认的时间，超出后由清扫器收尾。它要盖住 daemon 5 秒的
	// 取消轮询 + 有界的 10s+12s 会话记录排空等待（#5210）+ 网络余量；超过即可
	// 认为 daemon 已死或网络已分区。
	chatFinalizeGraceSeconds = 60.0
	// chatFinalizeBatchSize caps deferred finalizations per tick.
	// 中文说明：每轮最多收尾多少条延迟定论的取消任务。
	chatFinalizeBatchSize = 100
	// delegatedFailureRecoveryBatchSize bounds the durable recovery-outbox
	// replay so a historical backlog cannot monopolise the runtime sweep tick.
	// 中文说明：限制可靠恢复 outbox 的每轮重放量，防止历史积压独占 runtime
	// 巡检 tick。
	delegatedFailureRecoveryBatchSize = 100
)

// runtimeGCTxStarter 抽象"开事务"，让 GC 能对每个 runtime 用独立事务处理，
// 也便于测试注入。
type runtimeGCTxStarter interface {
	Begin(context.Context) (pgx.Tx, error)
}

// runtimeGCEventPublisher 抽象 GC 之后要发布的事件（teardown 细节 + 列表刷新）。
type runtimeGCEventPublisher interface {
	PublishRuntimeTeardown(context.Context, service.RuntimeTeardownResult, string, string, string, string, bool)
	PublishRuntimeRefresh(string, string, string, string)
	NotifyRuntimeGone(string)
}

// runtimeSweepStageStats 是一次巡检阶段的观测数据：扫到多少候选、实际改变了
// 多少。
type runtimeSweepStageStats struct {
	candidates int
	changed    int
}

// taskServiceMetrics 安全取 TaskService 上的业务指标句柄（可能为空）。
func taskServiceMetrics(taskSvc *service.TaskService) *obsmetrics.BusinessMetrics {
	if taskSvc == nil {
		return nil
	}
	return taskSvc.Metrics
}

// observeRuntimeSweepStage 给某个巡检阶段记录耗时与扫到/改动的数量。
func observeRuntimeSweepStage(metrics *obsmetrics.BusinessMetrics, stage string, startedAt time.Time, stats runtimeSweepStageStats) {
	metrics.ObserveRuntimeSweepStage(stage, time.Since(startedAt), stats.candidates, stats.changed)
}

// runPeriodicSweep 把每轮巡检串行化：上一轮没跑完时，ticker 触发的事件直接
// 丢弃。这保住了"前后不重叠"的旧行为，同时让低频维护任务与 runtime 存活检查
// 互不阻塞（各跑各的循环）。
func runPeriodicSweep(ctx context.Context, interval time.Duration, sweep func()) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sweep()
		}
	}
}

// runRuntimeSweeper 周期把"last_seen_at 超过陈旧阈值"的 runtime 标记离线，并
// 判失败孤儿任务。覆盖 daemon 崩溃、被杀死时没调注销接口、或任务悬在非终态
// 等场景。
//
// 翻转任何候选到离线前都会先咨询 liveness：LivenessStore 可用且报告该 runtime
// 还活着时，即使它的 DB last_seen_at 已陈旧也跳过（热心跳路径以 Redis 为准，
// DB 允许最多滞后 runtimeHeartbeatDBFlushInterval）。liveness 不可用或报错时，
// 退回信任 DB 的陈旧窗口——这是最初的行为。
func runRuntimeSweeper(ctx context.Context, queries *db.Queries, liveness handler.LivenessStore, taskSvc *service.TaskService, bus *events.Bus, reconnectGrace time.Duration) {
	runPeriodicSweep(ctx, sweepInterval, func() {
		// 下面这些阶段保持 PR1 里既有的节奏与顺序，让这次上线不改变任何业务
		// 判定或恢复语义。runtime GC 是唯一的例外：它的 7 天保留清理挪到独立
		// 的小时循环里，绝不能拖慢这条存活路径。
		sweepStaleRuntimes(ctx, queries, liveness, taskSvc, bus)
		sweepOfflineRuntimeTasks(ctx, queries, taskSvc, reconnectGrace)
		sweepExpiredRuntimeReconnectRetries(ctx, queries, taskSvc, reconnectGrace)
		sweepStaleTasks(ctx, queries, taskSvc, bus, reconnectGrace)
		sweepExpiredQueuedTasks(ctx, queries, taskSvc, reconnectGrace)
		sweepPendingDelegatedFailureRecoveries(ctx, taskSvc)
		sweepDeferredChatFinalizations(ctx, queries, taskSvc)
	})
}

// runRuntimeGCSweeper 独立每小时跑一轮"运行时 GC"（见 runPeriodicSweep 说明）。
func runRuntimeGCSweeper(ctx context.Context, txStarter runtimeGCTxStarter, queries *db.Queries, metrics *obsmetrics.BusinessMetrics, publisher runtimeGCEventPublisher) {
	runPeriodicSweep(ctx, runtimeGCSweepInterval, func() {
		gcRuntimes(ctx, txStarter, queries, metrics, publisher)
	})
}

// sweepPendingDelegatedFailureRecoveries 重试那些"没被可执行任务认领"的可靠
// 协调者交接。即使本轮没发现陈旧任务它也照跑——这正是修复"服务器重启前丢失
// 的恢复派发"的关键。
func sweepPendingDelegatedFailureRecoveries(ctx context.Context, taskSvc *service.TaskService) (stats runtimeSweepStageStats) {
	startedAt := time.Now()
	defer func() {
		observeRuntimeSweepStage(taskServiceMetrics(taskSvc), obsmetrics.RuntimeSweepStageDelegatedFailureRecovery, startedAt, stats)
	}()

	result, err := taskSvc.RecoverPendingDelegatedFailures(ctx, delegatedFailureRecoveryBatchSize)
	stats.candidates = result.Scanned
	stats.changed = result.Replayed + result.Exhausted
	if err != nil {
		slog.Warn("delegated failure recovery sweeper: replay failed",
			"replayed", result.Replayed,
			"exhausted", result.Exhausted,
			"error", err,
		)
		return
	}
	if result.Replayed > 0 {
		slog.Info("delegated failure recovery sweeper: replayed pending recoveries", "count", result.Replayed)
	}
	if result.Exhausted > 0 {
		slog.Warn("delegated failure recovery sweeper: automatic attempts exhausted", "count", result.Exhausted)
	}
	return
}

// sweepStaleRuntimes 把没心跳的 runtime 标记离线。任务的终止是另一个每轮都跑
// 的阶段，这样"重连宽限"的计时独立于 runtime 首次翻离线的那个 tick。
func sweepStaleRuntimes(ctx context.Context, queries *db.Queries, liveness handler.LivenessStore, taskSvc *service.TaskService, bus *events.Bus) (stats runtimeSweepStageStats) {
	startedAt := time.Now()
	defer func() {
		observeRuntimeSweepStage(taskServiceMetrics(taskSvc), obsmetrics.RuntimeSweepStageLiveness, startedAt, stats)
	}()

	candidates, err := queries.SelectStaleOnlineRuntimes(ctx, staleThresholdSeconds)
	if err != nil {
		slog.Warn("runtime sweeper: failed to list stale online runtimes", "error", err)
		return
	}
	stats.candidates = len(candidates)
	if len(candidates) == 0 {
		return
	}

	toOffline := filterStaleRuntimesByLiveness(ctx, candidates, liveness)
	if len(toOffline) == 0 {
		return
	}

	staleRows, err := queries.MarkRuntimesOfflineByIDs(ctx, db.MarkRuntimesOfflineByIDsParams{
		Ids:          toOffline,
		StaleSeconds: staleThresholdSeconds,
	})
	if err != nil {
		slog.Warn("runtime sweeper: failed to mark stale runtimes offline", "error", err)
		return
	}
	stats.changed = len(staleRows)
	if len(staleRows) == 0 {
		// 所有被过滤的候选在 SELECT 与 UPDATE 之间都被抢先进了非 online 状态，
		// 没什么可广播的。
		return
	}
	if taskSvc != nil && taskSvc.Analytics != nil {
		for _, row := range staleRows {
			obsmetrics.RecordEvent(taskSvc.Analytics, taskSvc.Metrics, analytics.RuntimeOffline(
				util.UUIDToString(row.OwnerID),
				util.UUIDToString(row.WorkspaceID),
				util.UUIDToString(row.ID),
				row.DaemonID.String,
				row.Provider,
			))
		}
	}

	// 收集受影响的去重工作区集合，稍后逐个通知刷新列表。
	workspaces := make(map[string]bool)
	for _, row := range staleRows {
		wsID := util.UUIDToString(row.WorkspaceID)
		workspaces[wsID] = true
	}

	// 把已确认离线的 runtime 的 liveness 记录清掉，避免以后的 MGET 巡检看到
	// 一条游离的 key 就以为它还活着。TTL 迟早会回收它们，但显式清理更便宜、
	// 更清楚。
	if liveness.Available() {
		for _, row := range staleRows {
			liveness.Forget(ctx, util.UUIDToString(row.ID))
		}
	}

	slog.Info("runtime sweeper: marked stale runtimes offline", "count", len(staleRows), "workspaces", len(workspaces))

	// 通知前端客户端重新拉取 runtime 列表。
	for wsID := range workspaces {
		bus.Publish(events.Event{
			Type:        protocol.EventDaemonRegister,
			WorkspaceID: wsID,
			ActorType:   "system",
			Payload: map[string]any{
				"action": "stale_sweep",
			},
		})
	}
	return
}

// sweepOfflineRuntimeTasks 只在 runtime 的最后一次心跳超过"有界重连宽限"之后
// 才终止它身上的活。每轮都跑这点很关键：宽限通常是在 sweepStaleRuntimes 完成
// 那次一次性的 online→offline 翻转很久之后才到期。
func sweepOfflineRuntimeTasks(ctx context.Context, queries *db.Queries, taskSvc *service.TaskService, reconnectGrace time.Duration) (stats runtimeSweepStageStats) {
	startedAt := time.Now()
	defer func() {
		observeRuntimeSweepStage(taskServiceMetrics(taskSvc), obsmetrics.RuntimeSweepStageOfflineTasks, startedAt, stats)
	}()

	failedTasks, err := taskSvc.FailTasksForOfflineRuntimes(ctx, db.FailTasksForOfflineRuntimesParams{
		ReconnectGraceSecs: reconnectGrace.Seconds(),
		MaxPerTick:         offlineTaskFailBatchSize,
	})
	if err != nil {
		slog.Warn("runtime sweeper: failed to clean up long-offline tasks", "error", err)
		return
	}
	stats.candidates = len(failedTasks)
	stats.changed = len(failedTasks)
	if len(failedTasks) == 0 {
		return
	}

	slog.Info("runtime sweeper: failed tasks beyond reconnect grace", "count", len(failedTasks))
	taskSvc.HandleFailedTasks(ctx, failedTasks)
	return
}

// sweepExpiredRuntimeReconnectRetries 给"受健康门控的重试等待"一条有界的终点
// 路径。没有这个阶段，一个永远不回来的 daemon 会让 Issue 一直保持 active，
// 它的 runtime 也永远不具备被 GC 的资格。
func sweepExpiredRuntimeReconnectRetries(ctx context.Context, queries *db.Queries, taskSvc *service.TaskService, reconnectGrace time.Duration) (stats runtimeSweepStageStats) {
	startedAt := time.Now()
	defer func() {
		observeRuntimeSweepStage(taskServiceMetrics(taskSvc), obsmetrics.RuntimeSweepStageReconnectRetries, startedAt, stats)
	}()

	failedTasks, err := taskSvc.FailExpiredRuntimeReconnectRetries(ctx, db.FailExpiredRuntimeReconnectRetriesParams{
		ReconnectGraceSecs: reconnectGrace.Seconds(),
		RuntimeStaleSecs:   staleThresholdSeconds,
		MaxPerTick:         reconnectRetryExpireBatchSize,
	})
	if err != nil {
		slog.Warn("runtime sweeper: failed to expire reconnect retries", "error", err)
		return
	}
	stats.candidates = len(failedTasks)
	stats.changed = len(failedTasks)
	if len(failedTasks) == 0 {
		return
	}

	slog.Info("runtime sweeper: expired reconnect retries", "count", len(failedTasks))
	taskSvc.HandleFailedTasks(ctx, failedTasks)
	return
}

// filterStaleRuntimesByLiveness 把"SELECT 出来的陈旧候选"收窄成"真正该翻离线
// 的集合"。liveness 可用且报告候选还活着时跳过它（DB 只是滞后）；store 不可用
// 或报错时信任 DB 的陈旧窗口——即全部候选都翻转，与旧版 MarkStaleRuntimesOffline
// 行为一致。
func filterStaleRuntimesByLiveness(ctx context.Context, candidates []db.SelectStaleOnlineRuntimesRow, liveness handler.LivenessStore) []pgtype.UUID {
	ids := make([]pgtype.UUID, 0, len(candidates))
	if !liveness.Available() {
		for _, c := range candidates {
			ids = append(ids, c.ID)
		}
		return ids
	}
	idStrs := make([]string, len(candidates))
	for i, c := range candidates {
		idStrs[i] = util.UUIDToString(c.ID)
	}
	alive, ok := liveness.IsAliveBatch(ctx, idStrs)
	if !ok {
		// 存储抖动：本 tick 降级为只看 DB 的行为。
		for _, c := range candidates {
			ids = append(ids, c.ID)
		}
		return ids
	}
	for i, c := range candidates {
		if alive[idStrs[i]] {
			continue
		}
		ids = append(ids, c.ID)
	}
	return ids
}

// gcRuntimes 删除陈旧的离线 runtime——但不依赖遗留的
// agent_task_queue.runtime_id ON DELETE CASCADE。候选发现是有界的；每个
// runtime 走独立事务，一个坏行不会让整轮清理报废。
func gcRuntimes(ctx context.Context, txStarter runtimeGCTxStarter, queries *db.Queries, metrics *obsmetrics.BusinessMetrics, publisher runtimeGCEventPublisher) (stats runtimeSweepStageStats) {
	startedAt := time.Now()
	defer func() {
		observeRuntimeSweepStage(metrics, obsmetrics.RuntimeSweepStageGC, startedAt, stats)
	}()
	return gcRuntimesWithBudget(ctx, txStarter, queries, metrics, publisher, runtimeGCTickTimeout)
}

// gcRuntimesWithBudget 实现 gcRuntimes 并在整轮外套上 budget 超时。列出候选
// 用短超时（listCtx），逐个删除也用短超时（runtimeCtx），预算耗尽就提前收工
// 记日志，保证锁竞争或慢对象存储最多延迟清理、不会让 GC worker 无限占用。
func gcRuntimesWithBudget(ctx context.Context, txStarter runtimeGCTxStarter, queries *db.Queries, metrics *obsmetrics.BusinessMetrics, publisher runtimeGCEventPublisher, budget time.Duration) (stats runtimeSweepStageStats) {
	gcCtx, cancelGC := context.WithTimeout(ctx, budget)
	defer cancelGC()

	listCtx, cancelList := context.WithTimeout(gcCtx, runtimeGCOperationTimeout)
	candidates, err := queries.ListStaleOfflineRuntimeGCCandidates(listCtx, db.ListStaleOfflineRuntimeGCCandidatesParams{
		StaleSeconds: offlineRuntimeTTLSeconds,
		MaxPerTick:   runtimeGCBatchSize,
	})
	cancelList()
	if err != nil {
		slog.Warn("runtime GC: failed to list stale offline runtimes", "error", err)
		metrics.RecordRuntimeGCFailed()
		return
	}
	stats.candidates = len(candidates)
	if len(candidates) == 0 {
		return
	}

	gcWorkspaces := make(map[string]bool)
	deleted := 0
	for i, runtimeID := range candidates {
		if gcCtx.Err() != nil {
			slog.Info("runtime GC: tick budget exhausted",
				"deleted", deleted, "remaining_candidates", len(candidates)-i)
			break
		}
		runtimeCtx, cancelRuntime := context.WithTimeout(gcCtx, runtimeGCOperationTimeout)
		result, err := gcRuntime(runtimeCtx, txStarter, queries, runtimeID)
		cancelRuntime()
		if err != nil {
			if gcCtx.Err() != nil {
				slog.Info("runtime GC: tick budget exhausted",
					"deleted", deleted, "remaining_candidates", len(candidates)-i)
				break
			}
			slog.Warn("runtime GC: failed to delete stale offline runtime",
				"runtime_id", util.UUIDToString(runtimeID), "error", err)
			metrics.RecordRuntimeGCFailed()
			continue
		}
		if result.skipReason != "" {
			metrics.RecordRuntimeGCSkipped(result.skipReason)
			slog.Warn("runtime GC: candidate no longer safe to delete; skipping",
				"runtime_id", util.UUIDToString(runtimeID), "reason", result.skipReason)
			continue
		}
		if !result.deleted {
			continue
		}
		deleted++
		stats.changed++
		metrics.RecordRuntimeGCDeleted()
		gcWorkspaces[result.workspaceID] = true
		if publisher != nil {
			publisher.NotifyRuntimeGone(util.UUIDToString(runtimeID))
			publisher.PublishRuntimeTeardown(gcCtx, result.teardown, result.workspaceID, "system", "", "runtime_gc", false)
		}
	}
	if deleted == 0 {
		return
	}

	slog.Info("runtime GC: deleted stale offline runtimes", "count", deleted, "workspaces", len(gcWorkspaces))

	for wsID := range gcWorkspaces {
		if publisher != nil {
			publisher.PublishRuntimeRefresh(wsID, "system", "", "runtime_gc")
		}
	}
	return
}

// runtimeGCResult 是单个 runtime GC 尝试的结果：附带被删对象所在工作区、
// teardown 结果，以及没删成的原因（skipReason 非空表示"现在删不安全"）。
type runtimeGCResult struct {
	workspaceID string
	teardown    service.RuntimeTeardownResult
	deleted     bool
	skipReason  string
}

// gcRuntime 在 runtime 行的 FOR UPDATE 锁下复检并删除一个候选。任务所有权写入
// 走 lock_task_owner_rows 的同行 FOR KEY SHARE 锁，所以并发的入队要么在下面
// 的复检前提交并挡住删除，要么等删除提交后再看到"它的 runtime 已不存在"。
func gcRuntime(ctx context.Context, txStarter runtimeGCTxStarter, queries *db.Queries, runtimeID pgtype.UUID) (runtimeGCResult, error) {
	var result runtimeGCResult
	tx, err := txStarter.Begin(ctx)
	if err != nil {
		return result, fmt.Errorf("begin transaction: %w", err)
	}
	defer func() {
		// 操作上下文可能已经因超时被取消。给回滚自己一个短窗口，别让池子里
		// 留着一次失败的 GC 所遗留的未提交事务/连接。
		rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), runtimeGCOperationTimeout)
		defer cancel()
		_ = tx.Rollback(rollbackCtx)
	}()
	qtx := queries.WithTx(tx)

	runtime, err := qtx.LockAgentRuntime(ctx, runtimeID)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, nil
	}
	if err != nil {
		return result, fmt.Errorf("lock runtime: %w", err)
	}
	result.workspaceID = util.UUIDToString(runtime.WorkspaceID)

	lockedAgents, err := qtx.ListUserAgentsByRuntimeForUpdate(ctx, runtimeID)
	if err != nil {
		return result, fmt.Errorf("lock runtime agents: %w", err)
	}

	eligible, err := qtx.IsAgentRuntimeEligibleForGC(ctx, db.IsAgentRuntimeEligibleForGCParams{
		ID:           runtimeID,
		StaleSeconds: offlineRuntimeTTLSeconds,
	})
	if err != nil {
		return result, fmt.Errorf("re-check eligibility: %w", err)
	}
	if !eligible {
		result.skipReason = obsmetrics.RuntimeGCSkipEligibilityChanged
		return result, nil
	}
	if err := service.ValidateRuntimeAgentWorkspaces(runtime, lockedAgents); err != nil {
		if errors.Is(err, service.ErrRuntimeWorkspaceMismatch) {
			result.skipReason = obsmetrics.RuntimeGCSkipWorkspaceMismatch
			return result, nil
		}
		return result, fmt.Errorf("validate runtime agent workspaces: %w", err)
	}

	lockedAgentIDs := make([]pgtype.UUID, len(lockedAgents))
	for i, agent := range lockedAgents {
		lockedAgentIDs[i] = agent.ID
	}

	undrained, err := qtx.CountUndrainedTasksByRuntimeOrAgent(ctx, db.CountUndrainedTasksByRuntimeOrAgentParams{
		RuntimeIds: []pgtype.UUID{runtimeID},
		AgentIds:   lockedAgentIDs,
	})
	if err != nil {
		return result, fmt.Errorf("count non-terminal tasks: %w", err)
	}
	if undrained > 0 {
		result.skipReason = obsmetrics.RuntimeGCSkipNonTerminalTask
		return result, nil
	}

	teardown, err := service.TeardownRuntime(ctx, qtx, runtimeID, service.RuntimeTeardownOptions{CancelNonTerminalTasks: false})
	if err != nil {
		if errors.Is(err, service.ErrRuntimeNotDrained) {
			result.skipReason = obsmetrics.RuntimeGCSkipNonTerminalTask
			return result, nil
		}
		if errors.Is(err, service.ErrRuntimeWorkspaceMismatch) {
			result.skipReason = obsmetrics.RuntimeGCSkipWorkspaceMismatch
			return result, nil
		}
		return result, fmt.Errorf("teardown runtime: %w", err)
	}
	if err := qtx.DeleteAgentRuntime(ctx, runtimeID); err != nil {
		return result, fmt.Errorf("delete runtime: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return result, fmt.Errorf("commit transaction: %w", err)
	}

	result.teardown = teardown
	result.deleted = true
	return result, nil
}

// sweepStaleTasks 判失败卡在 dispatched/running 过久的任务——即使 runtime 行
// 层面还 online。每个分支都把墙钟与"适合该状态"的存活信号配对，从而保住健康
// 的长任务：
//   - dispatched：排除带活跃 prepare_lease 的行（daemon 在 claim 与 StartTask
//     之间持续续约它）；
//   - running：排除"runtime 仍 online 且 last_seen_at 新鲜"的行（daemon 心跳
//     约每 15 秒续一次）。
//
// "daemon 已死"的主场景由同 tick 里更靠前的 sweepStaleRuntimes 处理；本函数
// 只是对残余边界的一种防御性兜底——runtime 行带着陈旧心跳熬过墙钟还赖着
// online（MUL-4107）。
func sweepStaleTasks(ctx context.Context, queries *db.Queries, taskSvc *service.TaskService, bus *events.Bus, reconnectGrace time.Duration) (stats runtimeSweepStageStats) {
	startedAt := time.Now()
	defer func() {
		observeRuntimeSweepStage(taskServiceMetrics(taskSvc), obsmetrics.RuntimeSweepStageStaleTasks, startedAt, stats)
	}()

	failedTasks, err := taskSvc.FailStaleTasks(ctx, db.FailStaleTasksParams{
		DispatchTimeoutSecs: dispatchTimeoutSeconds,
		RunningTimeoutSecs:  runningTimeoutSeconds,
		// Reuse the runtime stale window so the running-task backstop
		// exactly matches what sweepStaleRuntimes considers "not alive".
		//
		// 中文说明：复用 runtime 的陈旧窗口，让"running 任务兜底"与
		// sweepStaleRuntimes 判"不存活"的口径完全一致。
		RuntimeStaleSecs:          staleThresholdSeconds,
		RuntimeReconnectGraceSecs: reconnectGrace.Seconds(),
	})
	if err != nil {
		slog.Warn("task sweeper: failed to clean up stale tasks", "error", err)
		return
	}
	stats.candidates = len(failedTasks)
	stats.changed = len(failedTasks)
	if len(failedTasks) == 0 {
		return
	}

	slog.Info("task sweeper: failed stale tasks", "count", len(failedTasks))
	taskSvc.CaptureLeaseExpiredTasks(ctx, failedTasks)
	taskSvc.HandleFailedTasks(ctx, failedTasks)
	return
}

// sweepExpiredQueuedTasks 判失败那些"所属 runtime 已不再证明自己存活"的排队
// 任务。它是 MUL-1899 里派发时准入闸门的搭档：那个闸门阻止"注定失败的入队"，
// 这个则把已经排上队、但 runtime 随后消失的任务清退。刻意不按"排队时长"过期
// ——还在心跳的 runtime 只是忙，不是死了；MUL-6558 曾证明用墙钟杀健康任务会
// 误伤排在慢队列后面的正常活。每轮上限 queuedExpireBatchSize，大积压也垄断
// 不了数据库。
func sweepExpiredQueuedTasks(ctx context.Context, queries *db.Queries, taskSvc *service.TaskService, reconnectGrace time.Duration) (stats runtimeSweepStageStats) {
	startedAt := time.Now()
	defer func() {
		observeRuntimeSweepStage(taskServiceMetrics(taskSvc), obsmetrics.RuntimeSweepStageQueuedExpiry, startedAt, stats)
	}()

	failedTasks, err := taskSvc.ExpireStaleQueuedTasks(ctx, db.ExpireStaleQueuedTasksParams{
		ReconnectGraceSecs: reconnectGrace.Seconds(),
		MaxPerTick:         queuedExpireBatchSize,
	})
	if err != nil {
		slog.Warn("task sweeper: failed to expire stale queued tasks", "error", err)
		return
	}
	stats.candidates = len(failedTasks)
	stats.changed = len(failedTasks)
	if len(failedTasks) == 0 {
		return
	}

	slog.Info("task sweeper: expired stale queued tasks", "count", len(failedTasks))
	taskSvc.CaptureQueuedExpiredTasks(ctx, failedTasks)
	taskSvc.HandleFailedTasks(ctx, failedTasks)
	return
}

// sweepDeferredChatFinalizations 收尾"被取消的聊天任务"里推迟的 空/非空 判定
// （#5219）：若在宽限期内一直没等到 daemon 的取消确认——daemon 死了、网络分区、
// 或确认丢失——就在这里落定。FinalizeDeferredCancelledChat 会原子地认领标记，
// 所以与迟到的确认赛跑也无害。
func sweepDeferredChatFinalizations(ctx context.Context, queries *db.Queries, taskSvc *service.TaskService) (stats runtimeSweepStageStats) {
	startedAt := time.Now()
	defer func() {
		observeRuntimeSweepStage(taskServiceMetrics(taskSvc), obsmetrics.RuntimeSweepStageDeferredChatFinalization, startedAt, stats)
	}()

	rows, err := queries.ListChatFinalizeDeferredExpired(ctx, db.ListChatFinalizeDeferredExpiredParams{
		GraceSecs:  chatFinalizeGraceSeconds,
		MaxPerTick: chatFinalizeBatchSize,
	})
	if err != nil {
		slog.Warn("chat finalize sweeper: list deferred failed", "error", err)
		return
	}
	stats.candidates = len(rows)
	if len(rows) == 0 {
		return
	}
	for _, t := range rows {
		if taskSvc.FinalizeDeferredCancelledChat(ctx, t.ID) {
			stats.changed++
		}
	}
	slog.Info("chat finalize sweeper: settled deferred cancellations", "count", len(rows))
	return
}

// broadcastFailedTasks 作为薄壳保留给本包的集成测试用。新代码请直接调用
// TaskService.HandleFailedTasks，让副作用（事件广播、agent 对账、Issue 回滚、
// 自动重试）在同一个地方得到保证。
func broadcastFailedTasks(ctx context.Context, queries *db.Queries, taskSvc *service.TaskService, bus *events.Bus, tasks []db.AgentTaskQueue) {
	if taskSvc != nil {
		taskSvc.HandleFailedTasks(ctx, tasks)
		return
	}
	// 没有构造 TaskService 的测试走的兜底路径：发布带工作区 ID 的 task:failed
	// 事件，并把卡住的相关 Issue 状态复位。
	processedIssues := make(map[string]bool)
	affectedAgents := make(map[string]pgtype.UUID)
	for _, t := range tasks {
		failureReason := "agent_error"
		if t.FailureReason.Valid && t.FailureReason.String != "" {
			failureReason = t.FailureReason.String
		}
		workspaceID := ""
		if t.IssueID.Valid {
			if issue, err := queries.GetIssue(ctx, t.IssueID); err == nil {
				workspaceID = util.UUIDToString(issue.WorkspaceID)
				issueKey := util.UUIDToString(t.IssueID)
				// Only issues whose status means "an agent is actively working"
				// get reset. in_review and blocked are deliberately excluded —
				// they mean a human or an external dependency owns the issue
				// now, and resetting those to todo would re-trigger an agent on
				// work someone else is holding. A custom status resolves to the
				// canonical status it inherits, so a custom review gate is
				// excluded for the same reason In Review is. (MUL-6243)
				//
				// 中文说明：只有状态表示"agent 正在干活"的 Issue 才复位。in_review
				// 与 blocked 刻意排除——那意味着现在由人/外部依赖持有该 Issue，
				// 把它们复位成 todo 会让 agent 在别人正攥着的活上重新触发。自定义
				// 状态会解析成它继承的规范状态，所以自定义的"评审门"也因同样理由
				// 被排除。（MUL-6243）
				effectiveStatus := issuestatus.Effective(ctx, queries, issue.WorkspaceID, issue.Status)
				if effectiveStatus == "in_progress" && !processedIssues[issueKey] {
					processedIssues[issueKey] = true
					if hasActive, herr := queries.HasActiveTaskForIssue(ctx, t.IssueID); herr == nil && !hasActive {
						queries.UpdateIssueStatus(ctx, db.UpdateIssueStatusParams{ID: t.IssueID, Status: "todo", WorkspaceID: issue.WorkspaceID})
					}
				}
			}
		}
		payload := map[string]any{
			"task_id":        util.UUIDToString(t.ID),
			"agent_id":       util.UUIDToString(t.AgentID),
			"issue_id":       util.UUIDToString(t.IssueID),
			"status":         "failed",
			"failure_reason": failureReason,
			"retry_pending":  false,
		}
		if t.Error.Valid && t.Error.String != "" {
			payload["error"] = redact.Text(t.Error.String)
		}
		e := events.Event{
			Type:        protocol.EventTaskFailed,
			WorkspaceID: workspaceID,
			ActorType:   "system",
			TaskID:      util.UUIDToString(t.ID),
			Payload:     payload,
		}
		if t.ChatSessionID.Valid {
			e.ChatSessionID = util.UUIDToString(t.ChatSessionID)
			payload["chat_session_id"] = e.ChatSessionID
		}
		bus.Publish(e)
		affectedAgents[util.UUIDToString(t.AgentID)] = t.AgentID
	}
	for _, agentID := range affectedAgents {
		reconcileAgentStatus(ctx, queries, bus, agentID)
	}
}

// reconcileAgentStatus 依据当前在跑任务集刷新 agent 状态。若状态没变化则不
// 返回行，兜底路径也就不会发出多余的状态事件。仅供上面 broadcastFailedTasks
// 的测试兜底路径使用。
func reconcileAgentStatus(ctx context.Context, queries *db.Queries, bus *events.Bus, agentID pgtype.UUID) {
	agent, err := queries.RefreshAgentStatusFromTasks(ctx, agentID)
	if err != nil {
		return
	}
	bus.Publish(events.Event{
		Type:        protocol.EventAgentStatus,
		WorkspaceID: util.UUIDToString(agent.WorkspaceID),
		ActorType:   "system",
		Payload:     map[string]any{"agent_id": util.UUIDToString(agent.ID), "status": agent.Status},
	})
}
