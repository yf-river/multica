// 本文件把 realtime 包内部的原子计数器/观测快照，翻译成 multica_realtime_*
// Prometheus 指标。realtime 用标准库原子计数来避免依赖 prometheus；本文件是
// 适配层，顺带把 per-stream 的采样观测（条目数/内存/TTL）拆成带 stream 标签的指标。
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/multica-ai/multica/server/internal/realtime"
)

// RealtimeCollector 把 realtime.Metrics 转成 Prometheus 指标。
// 前段 Desc 都是无标签的固定指标；三组「带标签」的特殊指标：
//   - redis_mirror_errors_total 按 target 拆 primary/secondary；
//   - redis_stream_entries / _memory_bytes / _pttl_milliseconds 按 stream 名各一条，
//     反映每根 relay 流的最新一次低频采样。
type RealtimeCollector struct {
	metrics *realtime.Metrics

	connectsTotal          *prometheus.Desc
	disconnectsTotal       *prometheus.Desc
	activeConnections      *prometheus.Desc
	slowEvictionsTotal     *prometheus.Desc
	messagesSentTotal      *prometheus.Desc
	messagesDropped        *prometheus.Desc
	inboundTooLarge        *prometheus.Desc
	redisConnected         *prometheus.Desc
	redisXAddTotal         *prometheus.Desc
	redisXAddErrors        *prometheus.Desc
	redisXReadTotal        *prometheus.Desc
	redisXReadErrors       *prometheus.Desc
	redisAckTotal          *prometheus.Desc
	redisMirrorErrors      *prometheus.Desc
	redisMirrorDiverged    *prometheus.Desc
	redisStreamTrimmed     *prometheus.Desc
	redisStreamMissing     *prometheus.Desc
	redisRetentionErrors   *prometheus.Desc
	redisStreamsWithoutTTL *prometheus.Desc
	redisUsedMemoryBytes   *prometheus.Desc
	redisMaxMemoryBytes    *prometheus.Desc
	redisEvictedKeys       *prometheus.Desc
	redisStreamEntries     *prometheus.Desc
	redisStreamMemory      *prometheus.Desc
	redisStreamPTTL        *prometheus.Desc
}

// NewRealtimeCollector 基于 realtime 侧指标对象创建采集器。m 为 nil 时 Collect 直接
// 返回。三个带标签 Desc 在此显式 new：因为它们的标签不是固定列，无法走 newRealtimeDesc
// 的「无标签统一前缀」快捷路径。redis 前缀的指标在未启用 Redis 中转时恒为 0。
func NewRealtimeCollector(m *realtime.Metrics) *RealtimeCollector {
	return &RealtimeCollector{
		metrics: m,

		connectsTotal:          newRealtimeDesc("connects_total", "Total realtime WebSocket connections opened."),
		disconnectsTotal:       newRealtimeDesc("disconnects_total", "Total realtime WebSocket connections closed."),
		activeConnections:      newRealtimeDesc("active_connections", "Current realtime WebSocket connections."),
		slowEvictionsTotal:     newRealtimeDesc("slow_evictions_total", "Total realtime clients evicted for slow consumption."),
		messagesSentTotal:      newRealtimeDesc("messages_sent_total", "Total realtime messages sent."),
		messagesDropped:        newRealtimeDesc("messages_dropped_total", "Total realtime messages dropped."),
		inboundTooLarge:        newRealtimeDesc("inbound_too_large_total", "Total realtime connections closed for exceeding the inbound message size limit."),
		redisConnected:         newRealtimeDesc("redis_connected", "Whether the realtime Redis relay is connected."),
		redisXAddTotal:         newRealtimeDesc("redis_xadd_total", "Total Redis XADD operations by the realtime relay."),
		redisXAddErrors:        newRealtimeDesc("redis_xadd_errors_total", "Total Redis XADD errors by the realtime relay."),
		redisXReadTotal:        newRealtimeDesc("redis_xread_total", "Total Redis XREAD operations by the realtime relay."),
		redisXReadErrors:       newRealtimeDesc("redis_xread_errors_total", "Total Redis XREAD errors by the realtime relay."),
		redisAckTotal:          newRealtimeDesc("redis_ack_total", "Total Redis stream acknowledgements by the realtime relay."),
		redisMirrorErrors:      prometheus.NewDesc("multica_realtime_redis_mirror_errors_total", "Total Redis mirror write errors by the realtime relay.", []string{"target"}, nil),
		redisMirrorDiverged:    newRealtimeDesc("redis_mirror_divergence_total", "Total Redis mirror divergence events by the realtime relay."),
		redisStreamTrimmed:     newRealtimeDesc("redis_stream_trimmed_entries_total", "Total Redis Stream entries removed by retention maintenance."),
		redisStreamMissing:     newRealtimeDesc("redis_stream_missing_total", "Total observed relay stream disappearance transitions, including eviction and expiry."),
		redisRetentionErrors:   newRealtimeDesc("redis_retention_errors_total", "Total Redis relay retention maintenance errors."),
		redisStreamsWithoutTTL: newRealtimeDesc("redis_streams_without_ttl", "Current number of observed relay streams missing an expiry while TTL protection is enabled."),
		redisUsedMemoryBytes:   newRealtimeDesc("redis_used_memory_bytes", "Redis used_memory sampled by the realtime relay."),
		redisMaxMemoryBytes:    newRealtimeDesc("redis_maxmemory_bytes", "Redis maxmemory sampled by the realtime relay; zero means unlimited."),
		redisEvictedKeys:       newRealtimeDesc("redis_evicted_keys", "Redis instance evicted_keys sampled by the realtime relay."),
		redisStreamEntries:     prometheus.NewDesc("multica_realtime_redis_stream_entries", "Current entry count of a sampled relay stream.", []string{"stream"}, nil),
		redisStreamMemory:      prometheus.NewDesc("multica_realtime_redis_stream_memory_bytes", "Sampled memory usage of a relay stream.", []string{"stream"}, nil),
		redisStreamPTTL:        prometheus.NewDesc("multica_realtime_redis_stream_pttl_milliseconds", "Remaining relay stream TTL in milliseconds; -1 means no TTL and -2 means missing.", []string{"stream"}, nil),
	}
}

// newRealtimeDesc 统一生成无标签 realtime 指标的描述符，指标名固定带 multica_realtime_ 前缀。
func newRealtimeDesc(name, help string) *prometheus.Desc {
	return prometheus.NewDesc("multica_realtime_"+name, help, nil, nil)
}

// Describe 告诉 Prometheus 本采集器会发射哪些指标（Collector 接口约定）。
func (c *RealtimeCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, desc := range []*prometheus.Desc{
		c.connectsTotal,
		c.disconnectsTotal,
		c.activeConnections,
		c.slowEvictionsTotal,
		c.messagesSentTotal,
		c.messagesDropped,
		c.inboundTooLarge,
		c.redisConnected,
		c.redisXAddTotal,
		c.redisXAddErrors,
		c.redisXReadTotal,
		c.redisXReadErrors,
		c.redisAckTotal,
		c.redisMirrorErrors,
		c.redisMirrorDiverged,
		c.redisStreamTrimmed,
		c.redisStreamMissing,
		c.redisRetentionErrors,
		c.redisStreamsWithoutTTL,
		c.redisUsedMemoryBytes,
		c.redisMaxMemoryBytes,
		c.redisEvictedKeys,
		c.redisStreamEntries,
		c.redisStreamMemory,
		c.redisStreamPTTL,
	} {
		ch <- desc
	}
}

// Collect 读取 realtime 各原子计数器并发射；metrics 为 nil 直接返回。
// 几点口径说明：
//   - redis_connected 用 boolFloat 把布尔转成 1/0 的 Gauge；
//   - redis_mirror_errors_total 拆成 target=primary / target=secondary 两条；
//     主备镜像任一条失败都算 divergence（两后端结果不一致），单独有 mirror_divergence 指标；
//   - 末尾遍历 RedisStreamObservations()（内部持锁取的副本）给每根流发一组
//     entries/memory/PTTL 三条 Gauge。PTTL 沿用 Redis 语义：-1 无过期、-2 键不存在。
func (c *RealtimeCollector) Collect(ch chan<- prometheus.Metric) {
	if c.metrics == nil {
		return
	}
	m := c.metrics
	ch <- prometheus.MustNewConstMetric(c.connectsTotal, prometheus.CounterValue, float64(m.ConnectsTotal.Load()))
	ch <- prometheus.MustNewConstMetric(c.disconnectsTotal, prometheus.CounterValue, float64(m.DisconnectsTotal.Load()))
	ch <- prometheus.MustNewConstMetric(c.activeConnections, prometheus.GaugeValue, float64(m.ActiveConnections.Load()))
	ch <- prometheus.MustNewConstMetric(c.slowEvictionsTotal, prometheus.CounterValue, float64(m.SlowEvictionsTotal.Load()))
	ch <- prometheus.MustNewConstMetric(c.messagesSentTotal, prometheus.CounterValue, float64(m.MessagesSentTotal.Load()))
	ch <- prometheus.MustNewConstMetric(c.messagesDropped, prometheus.CounterValue, float64(m.MessagesDroppedTotal.Load()))
	ch <- prometheus.MustNewConstMetric(c.inboundTooLarge, prometheus.CounterValue, float64(m.InboundTooLargeTotal.Load()))
	ch <- prometheus.MustNewConstMetric(c.redisConnected, prometheus.GaugeValue, boolFloat(m.RedisConnected.Load()))
	ch <- prometheus.MustNewConstMetric(c.redisXAddTotal, prometheus.CounterValue, float64(m.RedisXAddTotal.Load()))
	ch <- prometheus.MustNewConstMetric(c.redisXAddErrors, prometheus.CounterValue, float64(m.RedisXAddErrors.Load()))
	ch <- prometheus.MustNewConstMetric(c.redisXReadTotal, prometheus.CounterValue, float64(m.RedisXReadTotal.Load()))
	ch <- prometheus.MustNewConstMetric(c.redisXReadErrors, prometheus.CounterValue, float64(m.RedisXReadErrors.Load()))
	ch <- prometheus.MustNewConstMetric(c.redisAckTotal, prometheus.CounterValue, float64(m.RedisAckTotal.Load()))
	ch <- prometheus.MustNewConstMetric(c.redisMirrorErrors, prometheus.CounterValue, float64(m.RedisMirrorPrimaryErrors.Load()), "primary")
	ch <- prometheus.MustNewConstMetric(c.redisMirrorErrors, prometheus.CounterValue, float64(m.RedisMirrorSecondaryErrors.Load()), "secondary")
	ch <- prometheus.MustNewConstMetric(c.redisMirrorDiverged, prometheus.CounterValue, float64(m.RedisMirrorDivergenceTotal.Load()))
	ch <- prometheus.MustNewConstMetric(c.redisStreamTrimmed, prometheus.CounterValue, float64(m.RedisRelayStreamTrimmedTotal.Load()))
	ch <- prometheus.MustNewConstMetric(c.redisStreamMissing, prometheus.CounterValue, float64(m.RedisRelayStreamMissingTotal.Load()))
	ch <- prometheus.MustNewConstMetric(c.redisRetentionErrors, prometheus.CounterValue, float64(m.RedisRelayRetentionErrors.Load()))
	ch <- prometheus.MustNewConstMetric(c.redisStreamsWithoutTTL, prometheus.GaugeValue, float64(m.RedisRelayStreamsWithoutTTL.Load()))
	ch <- prometheus.MustNewConstMetric(c.redisUsedMemoryBytes, prometheus.GaugeValue, float64(m.RedisUsedMemoryBytes.Load()))
	ch <- prometheus.MustNewConstMetric(c.redisMaxMemoryBytes, prometheus.GaugeValue, float64(m.RedisMaxMemoryBytes.Load()))
	ch <- prometheus.MustNewConstMetric(c.redisEvictedKeys, prometheus.GaugeValue, float64(m.RedisEvictedKeys.Load()))
	for stream, observation := range m.RedisStreamObservations() {
		ch <- prometheus.MustNewConstMetric(c.redisStreamEntries, prometheus.GaugeValue, float64(observation.Entries), stream)
		ch <- prometheus.MustNewConstMetric(c.redisStreamMemory, prometheus.GaugeValue, float64(observation.MemoryBytes), stream)
		ch <- prometheus.MustNewConstMetric(c.redisStreamPTTL, prometheus.GaugeValue, float64(observation.PTTLMillis), stream)
	}
}

// boolFloat 把布尔值转成指标可用的浮点：true=1、false=0。用于把「是否连接」这类
// 状态表达成 0/1 Gauge。
func boolFloat(v bool) float64 {
	if v {
		return 1
	}
	return 0
}
