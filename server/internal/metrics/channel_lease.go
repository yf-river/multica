// 本文件定义「频道租约（channel lease）交接」相关的 Prometheus 指标。租约是抢占式的
// Redis 锁：某进程抢到某个频道安装的租约后才对它做推送/控制。这些指标用于观测租约是否
// 从旧进程平稳交接给新进程，同时刻意不把「安装 ID」写进指标标签——安装 ID 是高基数数据，
// 塞进标签会让指标爆炸。
package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// ChannelLeaseMetrics 汇总频道租约交接需要观测的指标。这些指标用于验证 Redis
// 租约交接是否成功完成，同时不把「安装 ID」放进指标标签（安装 ID 千变万化，会撑爆
// 标签基数）。
type ChannelLeaseMetrics struct {
	// Operations 各类租约操作（acquire/steal/renew/release……）× 结果（ok/failed）的计数。
	Operations *prometheus.CounterVec
	// ActiveOwners 当前由本进程持有的频道安装租约数（Gauge，随抢到/释放实时升降）。
	ActiveOwners prometheus.Gauge
	// OwnersWithRenewalErrors 最近一次续租失败的持有者数量（Gauge）。租约必须周期性
	// 续约才不会被 Redis 上的 TTL 过期回收，这个值 >0 说明有持有者随时可能丢租约。
	OwnersWithRenewalErrors prometheus.Gauge
	// LastSuccessfulRenew 最近一次成功续租的 Unix 时间戳（秒）。若停在这个值不再前进，
	// 说明续租已经卡死。
	LastSuccessfulRenew prometheus.Gauge
	// TakeoverLatency 从「第一次观测到租约被别的进程占着」到「本进程最终抢到租约」的耗时。
	// 竞态发生时它衡量新进程接管的速度；竞态没发生时不会产生样本。
	TakeoverLatency prometheus.Histogram
}

// NewChannelLeaseMetrics 创建并注册名系列的所有租约指标。桶界按「秒」取 1/5/15/30/60/120/180/300，
// 覆盖从亚秒级竞争到几分钟级抢不到租约的典型接管时长分布。
func NewChannelLeaseMetrics() *ChannelLeaseMetrics {
	return &ChannelLeaseMetrics{
		Operations: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "multica",
			Subsystem: "channel_lease",
			Name:      "operations_total",
			Help:      "Channel lease operations by operation and outcome.",
		}, []string{"operation", "outcome"}),
		ActiveOwners: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "multica",
			Subsystem: "channel_lease",
			Name:      "active_owners",
			Help:      "Channel installations currently owned by this process.",
		}),
		OwnersWithRenewalErrors: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "multica",
			Subsystem: "channel_lease",
			Name:      "owners_with_renewal_errors",
			Help:      "Channel lease owners whose latest renewal attempt failed.",
		}),
		LastSuccessfulRenew: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "multica",
			Subsystem: "channel_lease",
			Name:      "last_successful_renewal_timestamp_seconds",
			Help:      "Unix timestamp of the most recent successful channel lease renewal.",
		}),
		TakeoverLatency: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: "multica",
			Subsystem: "channel_lease",
			Name:      "takeover_latency_seconds",
			Help:      "Time from first observed contention until this process acquires the lease.",
			Buckets:   []float64{1, 5, 15, 30, 60, 120, 180, 300},
		}),
	}
}

// RecordLeaseOperation 记一笔租约操作。operation 是动作名，outcome 是结果。
// 接收者可能为 nil（指标未初始化时调用方常传 nil 表示「不记录」），方法内部兜底跳过。
func (m *ChannelLeaseMetrics) RecordLeaseOperation(operation, outcome string) {
	if m != nil {
		m.Operations.WithLabelValues(operation, outcome).Inc()
	}
}

// SetActiveLeaseOwners 把「当前由本进程持有的租约数」设置为 count。
func (m *ChannelLeaseMetrics) SetActiveLeaseOwners(count float64) {
	if m != nil {
		m.ActiveOwners.Set(count)
	}
}

// SetOwnersWithRenewalErrors 把「续租失败的持有者数量」设置为 count。
func (m *ChannelLeaseMetrics) SetOwnersWithRenewalErrors(count float64) {
	if m != nil {
		m.OwnersWithRenewalErrors.Set(count)
	}
}

// SetLastSuccessfulRenewal 记录最近一次成功续租的时刻 at，内部换算成 Unix 秒。
func (m *ChannelLeaseMetrics) SetLastSuccessfulRenewal(at time.Time) {
	if m != nil {
		m.LastSuccessfulRenew.Set(float64(at.Unix()))
	}
}

// ObserveTakeoverLatency 记录一次租约接管耗时 delay（直方图样本）。
func (m *ChannelLeaseMetrics) ObserveTakeoverLatency(delay time.Duration) {
	if m != nil {
		m.TakeoverLatency.Observe(delay.Seconds())
	}
}

// Collectors 返回本组全部指标，供调用方一次性注册进 prometheus.Registry。
func (m *ChannelLeaseMetrics) Collectors() []prometheus.Collector {
	return []prometheus.Collector{m.Operations, m.ActiveOwners, m.OwnersWithRenewalErrors, m.LastSuccessfulRenew, m.TakeoverLatency}
}
