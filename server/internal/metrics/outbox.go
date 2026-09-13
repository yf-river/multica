// 本文件实现 outbox（发件箱）状态的 Prometheus 采集。domain_event_outbox 表是
// 「领域事件先落库、后台再投递」的可靠消息实现；这里周期性地 SQL 统计它的积压、
// 重试、死信情况。关键设计：抓取结果带短缓存 + 查询带超时，避免一次慢查询把
// Prometheus 抓取（以及业务请求）拖死。
package metrics

import (
	"context"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

// outboxSnapshot 一次数据库统计的快照。刻意只保留「数量」这类轻量字段：抓取时绝不能
// 去遍历事件正文，也不能跟分发器抢长时间持有的锁，否则指标抓取本身会拖垮业务。
type outboxSnapshot struct {
	pending          float64 // 待投递（既没处理也没死信）的事件数
	retrying         float64 // 待投递中至少失败过一次（last_error 非空）的事件数
	deadLettered     float64 // 已死信（终态失败，等人排查或等保留期过期）的事件数
	oldestPendingAge float64 // 最早那条待投递事件已等待的秒数
	takenAt          time.Time
}

// OutboxCollector 观测 outbox 的运行状态。设计上指标只做「观测」、不做数据的第二真源；
// 内部缓存一份快照，让慢数据库不至于阻塞每一次抓取。
type OutboxCollector struct {
	pool         *pgxpool.Pool
	cacheTTL     time.Duration // 快照缓存有效期：此期间内的抓取直接复用旧快照，不再查库
	queryTimeout time.Duration // 单次统计查询超时：超时算查询失败，返回上一份快照
	query        func(context.Context) (outboxSnapshot, error)
	now          func() time.Time // 可注入时钟，方便测试

	mu       sync.Mutex // 保护 snapshot 的并发读写（Prometheus 可能并发调用 Collect）
	snapshot *outboxSnapshot

	queryErrors      prometheus.Counter
	pending          *prometheus.Desc
	retrying         *prometheus.Desc
	deadLettered     *prometheus.Desc
	oldestPendingAge *prometheus.Desc
}

// NewOutboxCollector 基于连接池创建 outbox 采集器。pool 为 nil（没配 DB）时返回 nil，
// 调用方据此决定不注册该采集器。缓存 TTL 8 秒（Prometheus 通常 15 秒抓一次，够覆盖），
// 查询超时 500ms，防止抓取过程长时间占着池里的连接。
func NewOutboxCollector(pool *pgxpool.Pool) *OutboxCollector {
	if pool == nil {
		return nil
	}
	c := &OutboxCollector{
		pool: pool, cacheTTL: 8 * time.Second, queryTimeout: 500 * time.Millisecond,
		now: time.Now,
		queryErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "multica_domain_event_outbox_query_errors_total",
			Help: "Total failed database reads while sampling domain event outbox state.",
		}),
		pending:          prometheus.NewDesc("multica_domain_event_outbox_pending", "Current domain events waiting for delivery.", nil, nil),
		retrying:         prometheus.NewDesc("multica_domain_event_outbox_retrying", "Current pending domain events that have failed at least once.", nil, nil),
		deadLettered:     prometheus.NewDesc("multica_domain_event_outbox_dead_lettered", "Current terminally failed domain events awaiting review or retention expiry.", nil, nil),
		oldestPendingAge: prometheus.NewDesc("multica_domain_event_outbox_oldest_pending_age_seconds", "Age in seconds of the oldest pending domain event.", nil, nil),
	}
	c.query = c.queryDatabase
	return c
}

// Describe 告诉 Prometheus 本采集器会发射哪些指标（Collector 接口约定）。
func (c *OutboxCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.pending
	ch <- c.retrying
	ch <- c.deadLettered
	ch <- c.oldestPendingAge
	c.queryErrors.Describe(ch)
}

// Collect 发射一份 outbox 快照。先尝试取缓存或刷新，再把各数字发射成 Gauge。
// 刷新失败时仍会上报 queryErrors 计数并返回旧快照，保证抓取侧永远能拿到一版数据。
func (c *OutboxCollector) Collect(ch chan<- prometheus.Metric) {
	snapshot := c.maybeRefresh()
	c.queryErrors.Collect(ch)
	if snapshot == nil {
		return
	}
	ch <- prometheus.MustNewConstMetric(c.pending, prometheus.GaugeValue, snapshot.pending)
	ch <- prometheus.MustNewConstMetric(c.retrying, prometheus.GaugeValue, snapshot.retrying)
	ch <- prometheus.MustNewConstMetric(c.deadLettered, prometheus.GaugeValue, snapshot.deadLettered)
	ch <- prometheus.MustNewConstMetric(c.oldestPendingAge, prometheus.GaugeValue, snapshot.oldestPendingAge)
}

// maybeRefresh 取快照：缓存还没过期就直接返回旧快照；过期则持锁查一次库刷新。
// 查询失败不算致命——返回上一份快照并让 queryErrors 计数暴露问题。
// 用互斥锁包住刷新，保证并发抓取下同一时刻只跑一个统计查询。
func (c *OutboxCollector) maybeRefresh() *outboxSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if c.snapshot != nil && now.Sub(c.snapshot.takenAt) < c.cacheTTL {
		return c.snapshot
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.queryTimeout)
	defer cancel()
	snapshot, err := c.query(ctx)
	if err != nil {
		c.queryErrors.Inc()
		return c.snapshot
	}
	snapshot.takenAt = now
	c.snapshot = &snapshot
	return c.snapshot
}

// queryDatabase 执行 outbox 统计 SQL，一次返回四项：
//   - pending：processed_at 与 dead_lettered_at 都为空（还在等投递）；
//   - retrying：pending 里 last_error 非空（投过但失败了，将重试）；
//   - deadLettered：dead_lettered_at 非空（终态失败，进死信）；
//   - oldestPendingAge：最早的 pending 事件已等了多少秒；一条都没有时为 0。
func (c *OutboxCollector) queryDatabase(ctx context.Context) (outboxSnapshot, error) {
	var snapshot outboxSnapshot
	err := c.pool.QueryRow(ctx, `
		SELECT
			count(*) FILTER (WHERE processed_at IS NULL AND dead_lettered_at IS NULL),
			count(*) FILTER (WHERE processed_at IS NULL AND dead_lettered_at IS NULL AND last_error IS NOT NULL),
			count(*) FILTER (WHERE dead_lettered_at IS NOT NULL),
			COALESCE(EXTRACT(EPOCH FROM now() - min(created_at) FILTER (
				WHERE processed_at IS NULL AND dead_lettered_at IS NULL
			)), 0)
		FROM domain_event_outbox
	`).Scan(&snapshot.pending, &snapshot.retrying, &snapshot.deadLettered, &snapshot.oldestPendingAge)
	return snapshot, err
}
