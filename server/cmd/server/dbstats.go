// dbstats.go
//
// 职责：管理 PostgreSQL 连接池（pgxpool）的初始化参数与运行指标日志。
// 文件分三块：一是用一组"符合生产的默认值"（而非 pgx 内置默认值）建连接池，
// 并支持环境变量/URL 参数覆盖；二是周期采样池状态并打日志，方便在日志里
// 直接判断"连接池压力大"还是"数据库本身慢"。之所以在这块这么较真，是因为
// 线上出过一次事故：pgx 默认 MaxConns=max(4, NumCPU)，对小 CPU 的 pod 而言
// 只有 4，被 daemon 高频 claim/heartbeat 打满，导致每个查询都干等 ~900ms。
package main

import (
	"context"
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/dbstartup"
)

const (
	// dbStatsInterval is how often the pool stats are sampled and logged.
	// 15s lines up with the daemon heartbeat cadence so it's easy to
	// correlate with traffic patterns in the prod logs.
	//
	// 中文说明：池状态多久采样并打一次日志。取 15 秒是为了跟 daemon 心跳
	// 节奏对齐，这样在线上日志里容易跟流量走势对上号。
	dbStatsInterval = 15 * time.Second

	// defaultMaxConns / defaultMinConns are the per-pod pgxpool sizing
	// defaults. They replace pgx's built-in default of max(4, NumCPU),
	// which is far too small for our daemon-poll traffic pattern (~3800
	// acquires/s observed in prod) and was the root cause of the 3s+
	// /tasks/claim tail latency.
	//
	// The numbers follow the conventional "small pool, lots of waiters"
	// guidance for Postgres (HikariCP / PG community formula
	// `(core_count * 2) + effective_spindle_count`): 25 leaves headroom
	// for bursts and the occasional long-running query while staying well
	// below typical managed-Postgres `max_connections` ceilings when
	// multiplied across pods. MinConns=5 keeps a warm baseline so cold
	// pods don't pay handshake cost on first traffic.
	//
	// Both values are overridable via DATABASE_MAX_CONNS / DATABASE_MIN_CONNS.
	//
	// 中文说明：这两个是每个 pod 的 pgxpool 大小默认值，用来替换 pgx 内置
	// 的 max(4, NumCPU)。那个内置默认值对我们的 daemon 轮询流量（线上实测
	// 约 3800 次 acquire/秒）太小了，正是 /tasks/claim 尾部延迟超过 3 秒的
	// 根因。数值遵循 Postgres 社区"小池子、多排队"的经典经验公式
	// `(核数 * 2) + 有效磁盘数`：25 既能容纳突发与偶发长查询，又能在多 pod
	// 叠加时远低于托管 Postgres 常见的 max_connections 上限；MinConns=5
	// 保证有一批温热连接，冷启动的 pod 不会在第一批流量上付握手的代价。
	// 两者都可用 DATABASE_MAX_CONNS / DATABASE_MIN_CONNS 环境变量覆盖。
	defaultMaxConns int32 = 25
	defaultMinConns int32 = 5
)

// newDBPool 用一套合理的生产默认值 + 环境变量覆盖来构建 pgxpool。
//
// 之前直接用 pgxpool.New(ctx, url) 会悄悄选 MaxConns = max(4, NumCPU)。
// 我们线上 pod 的 CPU 配额很小，这个值算出来是 4，被 daemon 的 claim/
// heartbeat 流量完全打满，每个查询都要干等约 900ms。
//
// 配置优先级（从高到低）：
//  1. DATABASE_MAX_CONNS / DATABASE_MIN_CONNS 环境变量
//  2. DATABASE_URL 上的 pool_max_conns / pool_min_conns 查询参数
//     （pgxpool.ParseConfig 原生支持）
//  3. 本文件定义的默认值（defaultMaxConns / defaultMinConns）
//
// pgx 自带默认值 max(4, NumCPU) 故意不作为兜底——它正是线上事故的元凶。
func newDBPool(ctx context.Context, dbURL string, connectTimeout time.Duration) (*pgxpool.Pool, error) {
	cfg, err := dbstartup.ParsePoolConfig(dbURL, connectTimeout)
	if err != nil {
		return nil, err
	}

	urlParams := poolParamsFromURL(dbURL)

	// 先算"非环境变量"的兜底值：运维在 URL 里配了 pool_* 参数就用它，
	// 否则用代码默认值。注意：即使环境变量配的是非法值，兜底也是这个
	// 值，绝不会退回 pgx 内置的 4/0——那是引发线上事故的数值。
	maxFallback := defaultMaxConns
	if urlParams["pool_max_conns"] {
		maxFallback = cfg.MaxConns
	}
	cfg.MaxConns = envInt32("DATABASE_MAX_CONNS", maxFallback)

	minFallback := defaultMinConns
	if urlParams["pool_min_conns"] {
		minFallback = cfg.MinConns
	}
	cfg.MinConns = envInt32("DATABASE_MIN_CONNS", minFallback)

	if cfg.MinConns > cfg.MaxConns {
		cfg.MinConns = cfg.MaxConns
	}

	return pgxpool.NewWithConfig(ctx, cfg)
}

// poolParamsFromURL 返回数据库 URL 查询串里出现的 pool_* 参数集合。
// 用来判断运维是否已经通过连接串调过池子大小，避免"没配环境变量的升级"
// 悄悄覆盖掉已有配置。
func poolParamsFromURL(dbURL string) map[string]bool {
	out := map[string]bool{}
	u, err := url.Parse(dbURL)
	if err != nil {
		return out
	}
	for k := range u.Query() {
		out[k] = true
	}
	return out
}

// envInt32 从环境变量读 int32。为空或非法（含非正数）时回退到 def 并打一条
// warn，让配置错误在启动日志里就能看见。
func envInt32(name string, def int32) int32 {
	raw := os.Getenv(name)
	if raw == "" {
		return def
	}
	v, err := strconv.ParseInt(raw, 10, 32)
	if err != nil || v <= 0 {
		slog.Warn("invalid env var, using default",
			"name", name, "value", raw, "default", def, "error", err)
		return def
	}
	return int32(v)
}

// logPoolConfig 启动时打印一次生效的 pgxpool 配置。这很关键，因为 pgxpool
// 默认值小得惊人（MaxConns = max(4, NumCPU)）——日志里看不到实际值的话，
// 很容易把"连接池被打满"误判成"数据库变慢"。
func logPoolConfig(pool *pgxpool.Pool) {
	cfg := pool.Config()
	slog.Info("db pool config",
		"max_conns", cfg.MaxConns,
		"min_conns", cfg.MinConns,
		"max_conn_lifetime", cfg.MaxConnLifetime.String(),
		"max_conn_idle_time", cfg.MaxConnIdleTime.String(),
		"health_check_period", cfg.HealthCheckPeriod.String(),
	)
}

// runDBStatsLogger 周期采样 pool.Stat()。每轮都打一条 INFO，让运维能看到
// 基线压力；只要 EmptyAcquireCount 的增量大于 0 就升级成 WARN——空等 acquire
// （没有空闲连接可用、请求只能排队）正是连接池耗尽的直接症状，也是用来印证
// "慢 /tasks/claim 是池子不够"的实锤指标。
func runDBStatsLogger(ctx context.Context, pool *pgxpool.Pool) {
	ticker := time.NewTicker(dbStatsInterval)
	defer ticker.Stop()

	var (
		lastEmpty      int64
		lastAcquire    int64
		lastAcquireDur time.Duration
		lastCanceled   int64
	)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		s := pool.Stat()
		emptyDelta := s.EmptyAcquireCount() - lastEmpty
		acquireDelta := s.AcquireCount() - lastAcquire
		acquireDurDelta := s.AcquireDuration() - lastAcquireDur
		canceledDelta := s.CanceledAcquireCount() - lastCanceled

		// 上一个采样窗口内每次 acquire 的平均等待时间。单独看累计的
		// AcquireDuration 看不出情况在好转还是恶化，这个窗口均值才有用。
		var avgAcquireMs int64
		if acquireDelta > 0 {
			avgAcquireMs = (acquireDurDelta).Milliseconds() / acquireDelta
		}

		fields := []any{
			"max_conns", s.MaxConns(),
			"total_conns", s.TotalConns(),
			"acquired_conns", s.AcquiredConns(),
			"idle_conns", s.IdleConns(),
			"constructing_conns", s.ConstructingConns(),
			"acquire_count_delta", acquireDelta,
			"empty_acquire_delta", emptyDelta,
			"canceled_acquire_delta", canceledDelta,
			"avg_acquire_ms", avgAcquireMs,
		}

		if emptyDelta > 0 || canceledDelta > 0 {
			slog.Warn("db pool pressure", fields...)
		} else {
			slog.Info("db pool stats", fields...)
		}

		lastEmpty = s.EmptyAcquireCount()
		lastAcquire = s.AcquireCount()
		lastAcquireDur = s.AcquireDuration()
		lastCanceled = s.CanceledAcquireCount()
	}
}
