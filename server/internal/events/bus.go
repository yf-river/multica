package events

import (
	"log/slog"
	"sync"
)

// Event represents a domain event published by handlers or services.
type Event struct {
	ID          string // durable event identifier when persisted
	Type        string // e.g. "issue:created", "inbox:new"
	WorkspaceID string // routes to correct Hub room
	StreamKey   string // optional ordering key for durable delivery
	ActorType   string // "member", "agent", or "system"
	ActorID     string
	Payload     any // JSON-serializable, same shape as current WS payloads

	// Optional scope hints used by the realtime fanout layer to route the
	// event to a more specific scope than `workspace:{WorkspaceID}`. When set
	// these tell the listener which Redis stream / Hub room to publish on
	// without re-deserializing Payload. See MUL-1138 phase 1.
	TaskID        string
	ChatSessionID string
}

// Handler is a function that processes an event.
type Handler func(Event)

// Bus is an in-process synchronous pub/sub event bus.
type Bus struct {
	mu             sync.RWMutex
	listeners      map[string][]Handler
	globalHandlers []Handler
	persister      func(Event)
}

// SetPersister enables durable recording of published domain events. The
// callback is deliberately optional so unit-test buses remain in-memory.
func (b *Bus) SetPersister(persist func(Event)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.persister = persist
}

// PublishRecovered replays a persisted event without writing it back to the
// outbox, preventing recovery loops.
func (b *Bus) PublishRecovered(e Event) { b.publish(e, false) }

// New creates a new event bus.
func New() *Bus {
	return &Bus{
		listeners: make(map[string][]Handler),
	}
}

// Subscribe registers a handler for a given event type.
// Handlers are called synchronously in registration order.
func (b *Bus) Subscribe(eventType string, h Handler) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.listeners[eventType] = append(b.listeners[eventType], h)
}

// SubscribeAll registers a handler that receives ALL events regardless of type.
// Global handlers are called after type-specific handlers.
func (b *Bus) SubscribeAll(h Handler) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.globalHandlers = append(b.globalHandlers, h)
}

// Publish dispatches an event to all registered handlers for that event type.
// Type-specific handlers run first, then global (SubscribeAll) handlers.
// Each handler is called synchronously. Panics in individual handlers are
// recovered so one failing handler does not prevent others from executing.
func (b *Bus) Publish(e Event) {
	b.publish(e, true)
}

func (b *Bus) publish(e Event, persist bool) {
	b.mu.RLock()
	handlers := b.listeners[e.Type]
	globals := b.globalHandlers
	persister := b.persister
	b.mu.RUnlock()
	if persist && persister != nil {
		persister(e)
	}

	for _, h := range handlers {
		func() {
			defer func() {
				if r := recover(); r != nil {
					slog.Error("panic in event listener", "event_type", e.Type, "recovered", r)
				}
			}()
			h(e)
		}()
	}

	for _, h := range globals {
		func() {
			defer func() {
				if r := recover(); r != nil {
					slog.Error("panic in global event listener", "event_type", e.Type, "recovered", r)
				}
			}()
			h(e)
		}()
	}
}
