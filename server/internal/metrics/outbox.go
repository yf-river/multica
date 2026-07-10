package metrics

import (
	"context"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

type outboxSnapshot struct {
	pending          float64
	retrying         float64
	deadLettered     float64
	oldestPendingAge float64
	takenAt          time.Time
}

type OutboxCollector struct {
	pool         *pgxpool.Pool
	cacheTTL     time.Duration
	queryTimeout time.Duration
	query        func(context.Context) (outboxSnapshot, error)
	now          func() time.Time

	mu       sync.Mutex
	snapshot *outboxSnapshot

	queryErrors prometheus.Counter

	pending          *prometheus.Desc
	retrying         *prometheus.Desc
	deadLettered     *prometheus.Desc
	oldestPendingAge *prometheus.Desc
}

func NewOutboxCollector(pool *pgxpool.Pool) *OutboxCollector {
	if pool == nil {
		return nil
	}
	collector := &OutboxCollector{
		pool:         pool,
		cacheTTL:     8 * time.Second,
		queryTimeout: 500 * time.Millisecond,
		now:          time.Now,
		queryErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "multica_domain_event_outbox_query_errors_total",
			Help: "Total failed database reads while sampling domain event outbox state.",
		}),
		pending: prometheus.NewDesc(
			"multica_domain_event_outbox_pending",
			"Current domain events waiting for delivery.",
			nil,
			nil,
		),
		retrying: prometheus.NewDesc(
			"multica_domain_event_outbox_retrying",
			"Current pending domain events that have failed at least once.",
			nil,
			nil,
		),
		deadLettered: prometheus.NewDesc(
			"multica_domain_event_outbox_dead_lettered",
			"Current terminally failed domain events awaiting operator review or retention expiry.",
			nil,
			nil,
		),
		oldestPendingAge: prometheus.NewDesc(
			"multica_domain_event_outbox_oldest_pending_age_seconds",
			"Age in seconds of the oldest pending domain event.",
			nil,
			nil,
		),
	}
	collector.query = collector.queryDatabase
	return collector
}

func (c *OutboxCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.pending
	ch <- c.retrying
	ch <- c.deadLettered
	ch <- c.oldestPendingAge
	c.queryErrors.Describe(ch)
}

func (c *OutboxCollector) Collect(ch chan<- prometheus.Metric) {
	snapshot := c.maybeRefresh()
	c.queryErrors.Collect(ch)
	if snapshot == nil {
		return
	}
	c.emit(ch, *snapshot)
}

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

func (c *OutboxCollector) emit(ch chan<- prometheus.Metric, snapshot outboxSnapshot) {
	ch <- prometheus.MustNewConstMetric(c.pending, prometheus.GaugeValue, snapshot.pending)
	ch <- prometheus.MustNewConstMetric(c.retrying, prometheus.GaugeValue, snapshot.retrying)
	ch <- prometheus.MustNewConstMetric(c.deadLettered, prometheus.GaugeValue, snapshot.deadLettered)
	ch <- prometheus.MustNewConstMetric(c.oldestPendingAge, prometheus.GaugeValue, snapshot.oldestPendingAge)
}

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
