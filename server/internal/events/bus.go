// Package events 提供进程内同步发布/订阅的领域事件总线，供 handler / service
// 发布领域事件、按类型订阅消费。
package events

import (
	"log/slog"
	"sync"
)

// Event 表示由 handler 或 service 发布的领域事件。
type Event struct {
	// ID 持久化事件会填充，且在重试间保持稳定；仅临时事件可留空。
	ID string
	// IdempotencyKey 标识跨重试与进程重启的同一逻辑发生。生产者可自带；
	// 缺省时 outbox 会派生一个稳定键。
	IdempotencyKey string
	Type           string // 事件类型，如 "issue:created"、"inbox:new"
	// StreamKey 用于串行化修改同一聚合的持久化事件。键不同或为空的事件可被独立认领。
	StreamKey   string
	WorkspaceID string // 用于路由到正确的 Hub 房间
	ActorType   string // "member"（成员）、"agent"（代理）或 "system"（系统）
	ActorID     string
	Payload     any // 可 JSON 序列化，形状与现有 WS 载荷一致

	// 供 realtime 扇出层使用的可选作用域提示：把事件路由到比
	// `workspace:{WorkspaceID}` 更具体的范围。设置后监听方无需重新反序列化
	// Payload 就知道该发布到哪个 Redis stream / Hub 房间。见 MUL-1138 phase 1。
	TaskID        string
	ChatSessionID string
}

// Handler 是处理单个事件的函数类型。
type Handler func(Event)

// Bus 是进程内同步的发布/订阅事件总线。
type Bus struct {
	mu             sync.RWMutex
	listeners      map[string][]Handler
	globalHandlers []Handler
}

// New 创建一个新的事件总线。
func New() *Bus {
	return &Bus{
		listeners: make(map[string][]Handler),
	}
}

// PublishRecovered 投递一个"已经持久化过"的事件。持久化由创建事件的事务负责；
// 重放时绝不能再次产生 outbox 行。
func (b *Bus) PublishRecovered(e Event) { b.Publish(e) }

// Subscribe 注册某个事件类型的处理器。处理器按注册顺序同步调用。
func (b *Bus) Subscribe(eventType string, h Handler) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.listeners[eventType] = append(b.listeners[eventType], h)
}

// SubscribeAll 注册一个接收"所有类型事件"的处理器。全局处理器在类型专属处理器之后调用。
func (b *Bus) SubscribeAll(h Handler) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.globalHandlers = append(b.globalHandlers, h)
}

// Publish 把事件分发给该类型注册的全部处理器：先跑类型专属的，再跑全局
// （SubscribeAll）的。每个处理器同步调用；单个 handler 的 panic 会被兜底恢复，
// 避免一个失败的 handler 阻止其余 handler 执行。
func (b *Bus) Publish(e Event) {
	b.mu.RLock()
	handlers := b.listeners[e.Type]
	globals := b.globalHandlers
	b.mu.RUnlock()

	dispatch(handlers, e, "event")
	dispatch(globals, e, "global event")
}

// dispatch 依次调用一批处理器，逐个捕获 panic 记日志后继续执行。
func dispatch(handlers []Handler, e Event, scope string) {
	for _, h := range handlers {
		func() {
			defer func() {
				if r := recover(); r != nil {
					slog.Error("panic in "+scope+" listener", "event_type", e.Type, "recovered", r)
				}
			}()
			h(e)
		}()
	}
}
