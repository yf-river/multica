package daemonws

import "sync/atomic"

// Metrics 汇总 daemon WebSocket hub 的生命周期与唤醒提示指标，全部用原子计数，
// 供 /metrics 或诊断端点快照读取，无需加锁。
type Metrics struct {
	ConnectsTotal      atomic.Int64
	DisconnectsTotal   atomic.Int64
	ActiveConnections  atomic.Int64
	SlowEvictionsTotal atomic.Int64

	WakeupPublishedTotal atomic.Int64
	WakeupPublishErrors  atomic.Int64
	WakeupReceivedTotal  atomic.Int64
	WakeupDeliveredHit   atomic.Int64
	WakeupDeliveredMiss  atomic.Int64
}

// M 是 daemonws 的进程级共享指标实例。
var M = &Metrics{}

// Snapshot 把全部指标读成带下划线键名的 map（供 JSON 输出或日志使用）。
func (m *Metrics) Snapshot() map[string]any {
	return map[string]any{
		"connects_total":              m.ConnectsTotal.Load(),
		"disconnects_total":           m.DisconnectsTotal.Load(),
		"active_connections":          m.ActiveConnections.Load(),
		"slow_evictions_total":        m.SlowEvictionsTotal.Load(),
		"wakeup_published_total":      m.WakeupPublishedTotal.Load(),
		"wakeup_publish_errors":       m.WakeupPublishErrors.Load(),
		"wakeup_received_total":       m.WakeupReceivedTotal.Load(),
		"wakeup_delivered_hit_total":  m.WakeupDeliveredHit.Load(),
		"wakeup_delivered_miss_total": m.WakeupDeliveredMiss.Load(),
	}
}

// Reset 把所有指标清零（供测试隔离用）。
func (m *Metrics) Reset() {
	m.ConnectsTotal.Store(0)
	m.DisconnectsTotal.Store(0)
	m.ActiveConnections.Store(0)
	m.SlowEvictionsTotal.Store(0)
	m.WakeupPublishedTotal.Store(0)
	m.WakeupPublishErrors.Store(0)
	m.WakeupReceivedTotal.Store(0)
	m.WakeupDeliveredHit.Store(0)
	m.WakeupDeliveredMiss.Store(0)
}
