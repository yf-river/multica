// relay_lifecycle.go 定义「可管理生命周期」的 relay 抽象与一个临时的双写(镜像)
// relay 壳：上线新 relay 实现时，先用 MirroredRelay 把同一批事件同时写给新旧两套
// relay，观察一致性与指标，确认无问题后再切走老实现。
package realtime

import (
	"context"
	"errors"
	"log/slog"

	"github.com/oklog/ulid/v2"
)

// ManagedRelay 是基于 Redis 的实时 relay，且具备显式的 goroutine 生命周期管理
// （Start 启动后台消费循环、Stop 发出停止信号、Wait 等它们真正退出）。
type ManagedRelay interface {
	RelayPublisher
	Broadcaster

	NodeID() string
	Start(context.Context)
	Stop()
	Wait()
}

// MirroredRelay 是「灰度上线」用的临时辅助：同时启动两套 relay 后端，两边都读，
// 每次发布都给两边发同一事件 id。客户端侧（订阅连接）的去重机制保证这种回环双发
// 是幂等的，不会推重复。观察一段时间的指标（见 PublishWithID 里的计数）后即可下线。
type MirroredRelay struct {
	primary ManagedRelay
	mirror  ManagedRelay
}

// NewMirroredRelay 用主、副两套 relay 构造镜像壳。primary 是要验证的新实现（或
// 要保留的线上实现），mirror 是另一套对照实现。
func NewMirroredRelay(primary, mirror ManagedRelay) *MirroredRelay {
	return &MirroredRelay{primary: primary, mirror: mirror}
}

// NodeID 对外暴露主实现的节点 ID（镜像模式下对外身份只认 primary）。
func (r *MirroredRelay) NodeID() string {
	return r.primary.NodeID()
}

// SetDaemonRuntimeDeliverer 把投递器同时交给两套后端。哪套没实现该扩展方法就跳过。
func (r *MirroredRelay) SetDaemonRuntimeDeliverer(d DaemonRuntimeDeliverer) {
	if setter, ok := r.primary.(interface{ SetDaemonRuntimeDeliverer(DaemonRuntimeDeliverer) }); ok {
		setter.SetDaemonRuntimeDeliverer(d)
	}
	if setter, ok := r.mirror.(interface{ SetDaemonRuntimeDeliverer(DaemonRuntimeDeliverer) }); ok {
		setter.SetDaemonRuntimeDeliverer(d)
	}
}

// Start 同时启动主、副两套 relay 的后台循环，并登记节点 ID。
func (r *MirroredRelay) Start(ctx context.Context) {
	r.primary.Start(ctx)
	r.mirror.Start(ctx)
	M.NodeID.Store(r.NodeID())
}

// Stop 同时通知两套 relay 停止消费循环。
func (r *MirroredRelay) Stop() {
	r.primary.Stop()
	r.mirror.Stop()
}

// Wait 阻塞直到两套 relay 的后台循环全部退出。
func (r *MirroredRelay) Wait() {
	r.primary.Wait()
	r.mirror.Wait()
}

// BroadcastToScope 发一条带新生成 ULID 的广播。镜像模式用「同一事件 id 双写」
// 而非「两条不同 id 各自推一遍」，好让客户端按 id 去重。
func (r *MirroredRelay) BroadcastToScope(scopeType, scopeID string, message []byte) {
	_ = r.PublishWithID(scopeType, scopeID, "", message, ulid.Make().String())
}

// BroadcastToWorkspace 是「发往某 workspace 全体成员」的镜像广播简写。
func (r *MirroredRelay) BroadcastToWorkspace(workspaceID string, message []byte) {
	r.BroadcastToScope(ScopeWorkspace, workspaceID, message)
}

// SendToUser 给指定用户的所有连接镜像发一条消息（可排除某个 workspace 的连接）。
func (r *MirroredRelay) SendToUser(userID string, message []byte, excludeWorkspace ...string) {
	exclude := ""
	if len(excludeWorkspace) > 0 {
		exclude = excludeWorkspace[0]
	}
	_ = r.PublishWithID(ScopeUser, userID, exclude, message, ulid.Make().String())
}

// Broadcast 向所有客户端镜像广播一条全局消息（daemon 事件专用通道）。
func (r *MirroredRelay) Broadcast(message []byte) {
	_ = r.PublishWithID("global", "all", "", message, ulid.Make().String())
}

// PublishWithID 把同一事件同时发布给主、副两套 relay，并各自记录成败。
//
// 特殊规则：daemon_runtime 作用域只发主实现、不回环到镜像——它走的是专用通道，
// 双发没有意义反而可能造成唤醒帧重复。
func (r *MirroredRelay) PublishWithID(scopeType, scopeID, exclude string, frame []byte, id string) error {
	primaryErr := r.primary.PublishWithID(scopeType, scopeID, exclude, frame, id)
	if scopeType == ScopeDaemonRuntime {
		return primaryErr
	}
	mirrorErr := r.mirror.PublishWithID(scopeType, scopeID, exclude, frame, id)

	if primaryErr != nil {
		M.RedisMirrorPrimaryErrors.Add(1)
		slog.Warn("realtime/redis mirror: primary publish failed", "error", primaryErr, "scope", scopeType, "scope_id", scopeID, "event_id", id)
	}
	if mirrorErr != nil {
		M.RedisMirrorSecondaryErrors.Add(1)
		slog.Warn("realtime/redis mirror: secondary publish failed", "error", mirrorErr, "scope", scopeType, "scope_id", scopeID, "event_id", id)
	}
	// 只有一边成功说明两套 relay 行为开始分叉——灰度期间这是最需要警惕的信号。
	if (primaryErr == nil) != (mirrorErr == nil) {
		M.RedisMirrorDivergenceTotal.Add(1)
		slog.Warn(
			"realtime/redis mirror: divergent publish result",
			"primary_error", primaryErr,
			"secondary_error", mirrorErr,
			"scope", scopeType,
			"scope_id", scopeID,
			"event_id", id,
		)
	}
	return errors.Join(primaryErr, mirrorErr)
}

// 编译期断言：三种 relay 实现都满足 ManagedRelay 接口。
var _ ManagedRelay = (*RedisRelay)(nil)
var _ ManagedRelay = (*ShardedStreamRelay)(nil)
var _ ManagedRelay = (*MirroredRelay)(nil)
