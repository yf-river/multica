package daemonws

import (
	"log/slog"

	"github.com/oklog/ulid/v2"

	"github.com/multica-ai/multica/server/internal/realtime"
)

// RelayNotifier 把 daemon 唤醒提示发给本地 daemon hub；配置了 Redis 时再通过共享
// realtime relay 发布同一提示，让每个 API 节点都尝试本地投递。
type RelayNotifier struct {
	local *Hub
	relay realtime.RelayPublisher
}

// NewRelayNotifier 构造 RelayNotifier；local/relay 可为 nil（跳过对应投递路径）。
func NewRelayNotifier(local *Hub, relay realtime.RelayPublisher) *RelayNotifier {
	return &RelayNotifier{local: local, relay: relay}
}

// NotifyTaskAvailable 广播"某 runtime 有任务可认领"：先直接通知本地 hub，再经
// relay 发布，事件 ID 用 ULID 保证幂等去重。
func (n *RelayNotifier) NotifyTaskAvailable(runtimeID, taskID string) {
	if runtimeID == "" {
		return
	}
	eventID := ulid.Make().String()
	if n.local != nil {
		n.local.notifyTaskAvailable(runtimeID, taskID, eventID)
	}
	if n.relay == nil {
		return
	}
	frame, err := taskAvailableFrame(runtimeID, taskID)
	if err != nil {
		M.WakeupPublishErrors.Add(1)
		return
	}
	shardKey := taskID
	if shardKey == "" {
		shardKey = eventID
	}
	if err := n.relay.PublishWithID(realtime.ScopeDaemonRuntime, shardKey, "", frame, eventID); err != nil {
		M.WakeupPublishErrors.Add(1)
		slog.Warn("daemon websocket wakeup publish failed", "error", err, "runtime_id", runtimeID, "task_id", taskID)
		return
	}
	M.WakeupPublishedTotal.Add(1)
}

// NotifyRuntimeProfilesChanged 广播"某工作区的自定义运行时 profile 集合变化"
// （增/删/改/禁），让持有该 daemon WS 的节点触发运行时重注册。
func (n *RelayNotifier) NotifyRuntimeProfilesChanged(workspaceID, profileID string) {
	if workspaceID == "" {
		return
	}
	eventID := ulid.Make().String()
	if n.local != nil {
		n.local.notifyRuntimeProfilesChanged(workspaceID, profileID, eventID)
	}
	if n.relay == nil {
		return
	}
	frame, err := runtimeProfilesChangedFrame(workspaceID, profileID)
	if err != nil {
		M.WakeupPublishErrors.Add(1)
		return
	}
	if err := n.relay.PublishWithID(realtime.ScopeDaemonRuntime, workspaceID, "", frame, eventID); err != nil {
		M.WakeupPublishErrors.Add(1)
		slog.Warn("daemon websocket profile refresh publish failed", "error", err, "workspace_id", workspaceID, "runtime_profile_id", profileID)
		return
	}
	M.WakeupPublishedTotal.Add(1)
}

// NotifyWorkspacesChanged 广播"某用户的成员集/工作区列表变化"，让对应 daemon 做
// 一次最小同步。relay 只用 daemon 专用传输 scope；帧类型会告诉 Hub.DeliverDaemonRuntime
// scopeID 是 runtime、workspace 还是 user 键。保持单一传输 scope 能兼容既有 relay
// 消费者，同时由 hub 强制执行用户级投递。
func (n *RelayNotifier) NotifyWorkspacesChanged(userID string) {
	if userID == "" {
		return
	}
	eventID := ulid.Make().String()
	if n.local != nil {
		n.local.notifyWorkspacesChanged(userID, eventID)
	}
	if n.relay == nil {
		return
	}
	frame, err := workspacesChangedFrame()
	if err != nil {
		M.WakeupPublishErrors.Add(1)
		return
	}
	// ScopeDaemonRuntime is the relay's daemon-only transport scope; the frame
	// type tells Hub.DeliverDaemonRuntime whether scopeID is a runtime,
	// workspace, or user key. Keeping one transport scope preserves compatibility
	// with existing relay consumers while the hub enforces user-scoped delivery.
	if err := n.relay.PublishWithID(realtime.ScopeDaemonRuntime, userID, "", frame, eventID); err != nil {
		M.WakeupPublishErrors.Add(1)
		slog.Warn("daemon websocket workspace refresh publish failed", "error", err, "user_id", userID)
		return
	}
	M.WakeupPublishedTotal.Add(1)
}

// NotifyPendingWork 把 runtime 级"立即心跳"提示扇出给本地 hub；配置了 Redis 时再
// 经 relay 发布，让真正持有该 daemon WebSocket 的 API 节点投递（MUL-5444）。
// shard 键是 runtime ID：同一 runtime 的提示彼此保持有序，丢失的提示只让 daemon
// 损失一次常规心跳延迟。
func (n *RelayNotifier) NotifyPendingWork(runtimeID, kind string) {
	if runtimeID == "" {
		return
	}
	eventID := ulid.Make().String()
	if n.local != nil {
		n.local.notifyPendingWork(runtimeID, kind, eventID)
	}
	if n.relay == nil {
		return
	}
	frame, err := pendingWorkFrame(runtimeID, kind)
	if err != nil {
		M.WakeupPublishErrors.Add(1)
		return
	}
	if err := n.relay.PublishWithID(realtime.ScopeDaemonRuntime, runtimeID, "", frame, eventID); err != nil {
		M.WakeupPublishErrors.Add(1)
		slog.Warn("daemon websocket pending work publish failed", "error", err, "runtime_id", runtimeID, "kind", kind)
		return
	}
	M.WakeupPublishedTotal.Add(1)
}
