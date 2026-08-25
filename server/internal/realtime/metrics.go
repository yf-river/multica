package realtime

import (
	"sort"
	"sync"
	"sync/atomic"
)

// Metrics collects lightweight counters describing the realtime subsystem.
// Prometheus adapts the same atomics without coupling this package to the
// Prometheus client.
type Metrics struct {
	ConnectsTotal        atomic.Int64
	DisconnectsTotal     atomic.Int64
	ActiveConnections    atomic.Int64
	SlowEvictionsTotal   atomic.Int64
	MessagesSentTotal    atomic.Int64
	MessagesDroppedTotal atomic.Int64

	// Per-event-type send counters keyed by event type string.
	// Value is *atomic.Int64.
	eventSent sync.Map

	// Per-scope subscribe / unsubscribe / deny counters. Keyed by scope
	// type string ("workspace", "user", "task", "chat"). Value is
	// *atomic.Int64. Scope-room gauges follow the same pattern.
	subscribeTotal       sync.Map
	unsubscribeTotal     sync.Map
	subscribeDeniedTotal sync.Map
	scopeRooms           sync.Map

	// Redis relay counters. Zero unless the Redis broadcaster is enabled.
	RedisXAddTotal         atomic.Int64
	RedisXAddErrors        atomic.Int64
	RedisXReadTotal        atomic.Int64
	RedisXReadErrors       atomic.Int64
	RedisLastXAddLagMicros atomic.Int64

	// RedisConnected is set by the relay on startup / reconnect.
	RedisConnected atomic.Bool
	// RedisLastError stores the most recent consumer error message.
	redisLastErrMu sync.RWMutex
	redisLastErr   string

	// NodeID is set once at boot by the relay (or empty in single-node mode).
	NodeID atomic.Value // string
}

// M is the package-level metrics singleton.
var M = &Metrics{}

func loadOrInitCounter(m *sync.Map, key string) *atomic.Int64 {
	if v, ok := m.Load(key); ok {
		return v.(*atomic.Int64)
	}
	c := new(atomic.Int64)
	if existing, loaded := m.LoadOrStore(key, c); loaded {
		return existing.(*atomic.Int64)
	}
	return c
}

// RecordEvent increments the per-event-type send counter.
func (m *Metrics) RecordEvent(eventType string) {
	if eventType == "" {
		return
	}
	loadOrInitCounter(&m.eventSent, eventType).Add(1)
}

func (m *Metrics) setRedisLastError(msg string) {
	m.redisLastErrMu.Lock()
	m.redisLastErr = msg
	m.redisLastErrMu.Unlock()
}

func (m *Metrics) lastRedisErr() string {
	m.redisLastErrMu.RLock()
	defer m.redisLastErrMu.RUnlock()
	return m.redisLastErr
}

func snapshotCounters(s *sync.Map) map[string]int64 {
	out := map[string]int64{}
	s.Range(func(k, v any) bool {
		out[k.(string)] = v.(*atomic.Int64).Load()
		return true
	})
	keys := make([]string, 0, len(out))
	for k := range out {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	ordered := make(map[string]int64, len(out))
	for _, k := range keys {
		ordered[k] = out[k]
	}
	return ordered
}

// Snapshot returns a JSON-friendly copy of the current counter values.
func (m *Metrics) Snapshot() map[string]any {
	nodeID := ""
	if v := m.NodeID.Load(); v != nil {
		nodeID, _ = v.(string)
	}
	return map[string]any{
		"connects_total":         m.ConnectsTotal.Load(),
		"disconnects_total":      m.DisconnectsTotal.Load(),
		"active_connections":     m.ActiveConnections.Load(),
		"slow_evictions_total":   m.SlowEvictionsTotal.Load(),
		"messages_sent_total":    m.MessagesSentTotal.Load(),
		"messages_dropped_total": m.MessagesDroppedTotal.Load(),
		"events_sent_by_type":    snapshotCounters(&m.eventSent),
		"subscribes_total":       snapshotCounters(&m.subscribeTotal),
		"unsubscribes_total":     snapshotCounters(&m.unsubscribeTotal),
		"subscribe_denied_total": snapshotCounters(&m.subscribeDeniedTotal),
		"active_scope_rooms":     snapshotCounters(&m.scopeRooms),
		"redis": map[string]any{
			"connected":            m.RedisConnected.Load(),
			"node_id":              nodeID,
			"xadd_total":           m.RedisXAddTotal.Load(),
			"xadd_errors":          m.RedisXAddErrors.Load(),
			"xread_total":          m.RedisXReadTotal.Load(),
			"xread_errors":         m.RedisXReadErrors.Load(),
			"last_xadd_lag_micros": m.RedisLastXAddLagMicros.Load(),
			"last_error":           m.lastRedisErr(),
		},
	}
}
