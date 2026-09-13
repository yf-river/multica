// 本文件定义「频道媒体对账器」的 Prometheus 指标。对账器按媒体的「意图账本
// （intent ledger）」工作：删除那些不再被任何消息引用的媒体对象，清理对应的账本行，
// 存储删除失败则进入退避重试。这里的指标观测这一清扫过程的进度与异常。
package metrics

import "github.com/prometheus/client_golang/prometheus"

// ChannelMediaReconcilerMetrics 观测媒体「意图账本」对账器：它删掉了多少无人引用的
// 对象、因为已有持久化附件引用而清掉了多少账本行、有多少次存储删除失败并进入退避重试。
type ChannelMediaReconcilerMetrics struct {
	// ObjectsDeleted 对账器实际删除的「无人引用」媒体对象总数。
	ObjectsDeleted prometheus.Counter
	// RowsReferenced 因「有持久化附件引用着这个对象」而被清掉的账本行数。
	RowsReferenced prometheus.Counter
	// DeleteFailures 对象存储删除失败、被排进重试的次数。
	DeleteFailures prometheus.Counter
	// TombstoneReferenced 统计的是不变量被破坏，不是例行工作：墓碑清除（tombstone pass）
	// 准备删掉某个对象时，发现还有一个附件引用着它。此时对象被保留；该值非零说明
	// 「每条消息的媒体 key」或「绑定的状态保护」失去了约束力，需要排查。
	TombstoneReferenced prometheus.Counter
}

func NewChannelMediaReconcilerMetrics() *ChannelMediaReconcilerMetrics {
	return &ChannelMediaReconcilerMetrics{
		ObjectsDeleted: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "multica",
			Subsystem: "channel_media",
			Name:      "reconciler_objects_deleted_total",
			Help:      "Unreferenced media objects deleted by the reconciler.",
		}),
		RowsReferenced: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "multica",
			Subsystem: "channel_media",
			Name:      "reconciler_rows_referenced_total",
			Help:      "Ledger rows cleared because a durable attachment references the object.",
		}),
		DeleteFailures: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "multica",
			Subsystem: "channel_media",
			Name:      "reconciler_delete_failures_total",
			Help:      "Object-storage deletes that failed and were scheduled for retry.",
		}),
		TombstoneReferenced: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "multica",
			Subsystem: "channel_media",
			Name:      "reconciler_tombstone_referenced_total",
			Help:      "Tombstone passes that found an attachment referencing the object (invariant violation; the object is kept).",
		}),
	}
}

// Collectors 返回本组全部指标，供调用方一次性注册进 prometheus.Registry。
func (m *ChannelMediaReconcilerMetrics) Collectors() []prometheus.Collector {
	return []prometheus.Collector{m.ObjectsDeleted, m.RowsReferenced, m.DeleteFailures, m.TombstoneReferenced}
}
