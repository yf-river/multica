// 本文件把 daemonws 包内部用原子计数器维护的统计，翻译成 multica_daemonws_*
// Prometheus 指标。daemonws 侧只存数字不引入 prometheus 依赖，本文件充当两边
// 的适配器，避免指标库渗透进连接层模块。
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/multica-ai/multica/server/internal/daemonws"
)

// DaemonWSCollector 从 daemonws.Metrics 的原子计数快照发射指标。
// 每个 Desc 字段对应一个指标名与含义；wakeup_delivered_total 特殊地按 result
// 标签拆成 hit/miss 两路。
type DaemonWSCollector struct {
	metrics *daemonws.Metrics

	connectsTotal        *prometheus.Desc
	disconnectsTotal     *prometheus.Desc
	activeConnections    *prometheus.Desc
	slowEvictionsTotal   *prometheus.Desc
	wakeupPublishedTotal *prometheus.Desc
	wakeupPublishErrors  *prometheus.Desc
	wakeupReceivedTotal  *prometheus.Desc
	wakeupDeliveredTotal *prometheus.Desc
	runtimeGonePublished *prometheus.Desc
	runtimeGoneErrors    *prometheus.Desc
	runtimeGoneReceived  *prometheus.Desc
	runtimeGoneDelivered *prometheus.Desc
}

// NewDaemonWSCollector 基于 daemonws 侧指标对象创建采集器。m 为 nil 时仍可创建，
// Collect 阶段会直接返回（见 Collect 空判断）。注意 wakeup_delivered_total 在
// 其它指标统一用无标签 Desc 的情况下单独带了 result 标签，因为它要拆 hit/miss。
func NewDaemonWSCollector(m *daemonws.Metrics) *DaemonWSCollector {
	return &DaemonWSCollector{
		metrics: m,

		connectsTotal:        newDaemonWSDesc("connects_total", "Total daemon WebSocket connections opened."),
		disconnectsTotal:     newDaemonWSDesc("disconnects_total", "Total daemon WebSocket connections closed."),
		activeConnections:    newDaemonWSDesc("active_connections", "Current daemon WebSocket connections."),
		slowEvictionsTotal:   newDaemonWSDesc("slow_evictions_total", "Total daemon WebSocket clients evicted for slow consumption."),
		wakeupPublishedTotal: newDaemonWSDesc("wakeup_published_total", "Total daemon wakeups published to the Redis relay."),
		wakeupPublishErrors:  newDaemonWSDesc("wakeup_publish_errors_total", "Total daemon wakeup Redis publish errors."),
		wakeupReceivedTotal:  newDaemonWSDesc("wakeup_received_total", "Total daemon wakeups received from the Redis relay."),
		wakeupDeliveredTotal: prometheus.NewDesc("multica_daemonws_wakeup_delivered_total", "Total daemon wakeup local delivery attempts.", []string{"result"}, nil),
		runtimeGonePublished: newDaemonWSDesc("runtime_gone_published_total", "Total runtime-gone notifications published to the Redis relay."),
		runtimeGoneErrors:    newDaemonWSDesc("runtime_gone_publish_errors_total", "Total runtime-gone Redis publish errors."),
		runtimeGoneReceived:  newDaemonWSDesc("runtime_gone_received_total", "Total runtime-gone notifications received from the Redis relay."),
		runtimeGoneDelivered: prometheus.NewDesc("multica_daemonws_runtime_gone_delivered_total", "Total runtime-gone local delivery attempts.", []string{"result"}, nil),
	}
}

// newDaemonWSDesc 统一生成 daemonws 指标的描述符，指标名固定带 multica_daemonws_ 前缀。
func newDaemonWSDesc(name, help string) *prometheus.Desc {
	return prometheus.NewDesc("multica_daemonws_"+name, help, nil, nil)
}

// Describe 告诉 Prometheus 本采集器会发射哪些指标（Collector 接口约定）。
func (c *DaemonWSCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, desc := range []*prometheus.Desc{
		c.connectsTotal,
		c.disconnectsTotal,
		c.activeConnections,
		c.slowEvictionsTotal,
		c.wakeupPublishedTotal,
		c.wakeupPublishErrors,
		c.wakeupReceivedTotal,
		c.wakeupDeliveredTotal,
		c.runtimeGonePublished,
		c.runtimeGoneErrors,
		c.runtimeGoneReceived,
		c.runtimeGoneDelivered,
	} {
		ch <- desc
	}
}

// Collect 读取 daemonws 各原子计数器的当前值并发射。metrics 为 nil 时直接返回。
// 唤醒投递分两路发：hit 表示本地找到了在线的 daemon 连接、成功唤醒；miss 表示
// 没有在线连接可投递（daemon 会靠下次自己轮询补上）。两路都用同一个 Desc、只换
// result 标签值，属于同一个指标序列。
func (c *DaemonWSCollector) Collect(ch chan<- prometheus.Metric) {
	if c.metrics == nil {
		return
	}
	m := c.metrics
	ch <- prometheus.MustNewConstMetric(c.connectsTotal, prometheus.CounterValue, float64(m.ConnectsTotal.Load()))
	ch <- prometheus.MustNewConstMetric(c.disconnectsTotal, prometheus.CounterValue, float64(m.DisconnectsTotal.Load()))
	ch <- prometheus.MustNewConstMetric(c.activeConnections, prometheus.GaugeValue, float64(m.ActiveConnections.Load()))
	ch <- prometheus.MustNewConstMetric(c.slowEvictionsTotal, prometheus.CounterValue, float64(m.SlowEvictionsTotal.Load()))
	ch <- prometheus.MustNewConstMetric(c.wakeupPublishedTotal, prometheus.CounterValue, float64(m.WakeupPublishedTotal.Load()))
	ch <- prometheus.MustNewConstMetric(c.wakeupPublishErrors, prometheus.CounterValue, float64(m.WakeupPublishErrors.Load()))
	ch <- prometheus.MustNewConstMetric(c.wakeupReceivedTotal, prometheus.CounterValue, float64(m.WakeupReceivedTotal.Load()))
	ch <- prometheus.MustNewConstMetric(c.wakeupDeliveredTotal, prometheus.CounterValue, float64(m.WakeupDeliveredHit.Load()), "hit")
	ch <- prometheus.MustNewConstMetric(c.wakeupDeliveredTotal, prometheus.CounterValue, float64(m.WakeupDeliveredMiss.Load()), "miss")
	ch <- prometheus.MustNewConstMetric(c.runtimeGonePublished, prometheus.CounterValue, float64(m.RuntimeGonePublishedTotal.Load()))
	ch <- prometheus.MustNewConstMetric(c.runtimeGoneErrors, prometheus.CounterValue, float64(m.RuntimeGonePublishErrors.Load()))
	ch <- prometheus.MustNewConstMetric(c.runtimeGoneReceived, prometheus.CounterValue, float64(m.RuntimeGoneReceivedTotal.Load()))
	ch <- prometheus.MustNewConstMetric(c.runtimeGoneDelivered, prometheus.CounterValue, float64(m.RuntimeGoneDeliveredHit.Load()), "hit")
	ch <- prometheus.MustNewConstMetric(c.runtimeGoneDelivered, prometheus.CounterValue, float64(m.RuntimeGoneDeliveredMiss.Load()), "miss")
}
