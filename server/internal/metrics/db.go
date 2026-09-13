// 本文件实现一个自定义 Prometheus Collector，把 pgx 连接池的运行时统计转成
// multica_db_pool_* 系列指标。因为 pgx 没提供现成的 Prometheus 适配，这里用
// pool.Stat() 的快照逐项发射指标。
package metrics

import (
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

// DBCollector 把 pgx 连接池的状态暴露成 Prometheus 指标。每个字段是一个指标描述符，
// 定义指标名与含义；真正取值在 Collect 里通过 pool.Stat() 拿到。
type DBCollector struct {
	pool *pgxpool.Pool

	acquiredConns         *prometheus.Desc
	idleConns             *prometheus.Desc
	maxConns              *prometheus.Desc
	totalConns            *prometheus.Desc
	constructingConns     *prometheus.Desc
	acquireCount          *prometheus.Desc
	acquireDuration       *prometheus.Desc
	emptyAcquireCount     *prometheus.Desc
	emptyAcquireWaitTime  *prometheus.Desc
	canceledAcquireCount  *prometheus.Desc
	newConnsCount         *prometheus.Desc
	maxIdleDestroyCount   *prometheus.Desc
	maxLifetimeDestroyCnt *prometheus.Desc
}

// NewDBCollector 基于指定连接池创建采集器。pool 不可为 nil；
// 为空池仍可创建，但 Collect 阶段不会发射任何指标（见 Collect 里的空判断）。
func NewDBCollector(pool *pgxpool.Pool) *DBCollector {
	return &DBCollector{
		pool: pool,

		acquiredConns:         newDBDesc("acquired_conns", "Currently acquired PostgreSQL connections."),
		idleConns:             newDBDesc("idle_conns", "Currently idle PostgreSQL connections."),
		maxConns:              newDBDesc("max_conns", "Maximum PostgreSQL connections allowed by the pool."),
		totalConns:            newDBDesc("total_conns", "Total PostgreSQL connections currently in the pool."),
		constructingConns:     newDBDesc("constructing_conns", "PostgreSQL connections currently being established."),
		acquireCount:          newDBDesc("acquire_count", "Total successful PostgreSQL connection acquires."),
		acquireDuration:       newDBDesc("acquire_duration_seconds_total", "Total time spent acquiring PostgreSQL connections."),
		emptyAcquireCount:     newDBDesc("empty_acquire_count", "Total acquires that waited because the PostgreSQL pool was empty."),
		emptyAcquireWaitTime:  newDBDesc("empty_acquire_wait_seconds_total", "Total time spent waiting for PostgreSQL connections when the pool was empty."),
		canceledAcquireCount:  newDBDesc("canceled_acquire_count", "Total canceled PostgreSQL connection acquires."),
		newConnsCount:         newDBDesc("new_conns_count", "Total PostgreSQL connections created by the pool."),
		maxIdleDestroyCount:   newDBDesc("max_idle_destroy_count", "Total PostgreSQL connections destroyed due to idle limits."),
		maxLifetimeDestroyCnt: newDBDesc("max_lifetime_destroy_count", "Total PostgreSQL connections destroyed due to max lifetime."),
	}
}

// newDBDesc 统一生成 DB 指标的描述符：指标名固定带 multica_db_pool_ 前缀。
func newDBDesc(name, help string) *prometheus.Desc {
	return prometheus.NewDesc("multica_db_pool_"+name, help, nil, nil)
}

// Describe 告诉 Prometheus 本采集器会发射哪些指标，是 Collector 接口约定的一部分。
func (c *DBCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, desc := range []*prometheus.Desc{
		c.acquiredConns,
		c.idleConns,
		c.maxConns,
		c.totalConns,
		c.constructingConns,
		c.acquireCount,
		c.acquireDuration,
		c.emptyAcquireCount,
		c.emptyAcquireWaitTime,
		c.canceledAcquireCount,
		c.newConnsCount,
		c.maxIdleDestroyCount,
		c.maxLifetimeDestroyCnt,
	} {
		ch <- desc
	}
}

// Collect 抓一次 pgx 连接池统计快照并逐个发射。连接池为 nil 时直接返回，
// 不发任何样本（兼容「没配 DB 也能跑」的场景）。注意 pool.Stat() 本身返回的
// 已是快照，因此并发抓取安全。
func (c *DBCollector) Collect(ch chan<- prometheus.Metric) {
	if c.pool == nil {
		return
	}
	stat := c.pool.Stat()
	ch <- prometheus.MustNewConstMetric(c.acquiredConns, prometheus.GaugeValue, float64(stat.AcquiredConns()))
	ch <- prometheus.MustNewConstMetric(c.idleConns, prometheus.GaugeValue, float64(stat.IdleConns()))
	ch <- prometheus.MustNewConstMetric(c.maxConns, prometheus.GaugeValue, float64(stat.MaxConns()))
	ch <- prometheus.MustNewConstMetric(c.totalConns, prometheus.GaugeValue, float64(stat.TotalConns()))
	ch <- prometheus.MustNewConstMetric(c.constructingConns, prometheus.GaugeValue, float64(stat.ConstructingConns()))
	ch <- prometheus.MustNewConstMetric(c.acquireCount, prometheus.CounterValue, float64(stat.AcquireCount()))
	ch <- prometheus.MustNewConstMetric(c.acquireDuration, prometheus.CounterValue, stat.AcquireDuration().Seconds())
	ch <- prometheus.MustNewConstMetric(c.emptyAcquireCount, prometheus.CounterValue, float64(stat.EmptyAcquireCount()))
	ch <- prometheus.MustNewConstMetric(c.emptyAcquireWaitTime, prometheus.CounterValue, stat.EmptyAcquireWaitTime().Seconds())
	ch <- prometheus.MustNewConstMetric(c.canceledAcquireCount, prometheus.CounterValue, float64(stat.CanceledAcquireCount()))
	ch <- prometheus.MustNewConstMetric(c.newConnsCount, prometheus.CounterValue, float64(stat.NewConnsCount()))
	ch <- prometheus.MustNewConstMetric(c.maxIdleDestroyCount, prometheus.CounterValue, float64(stat.MaxIdleDestroyCount()))
	ch <- prometheus.MustNewConstMetric(c.maxLifetimeDestroyCnt, prometheus.CounterValue, float64(stat.MaxLifetimeDestroyCount()))
}
