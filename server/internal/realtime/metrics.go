// metrics.go 定义 realtime 子系统的轻量运行计数器：连接数、收发消息数、订阅/退订
// 次数、Redis relay 的健康数字等，统一暴露成一个包级单例 M，供健康检查/调试接口
// 打快照。刻意只依赖标准库 atomic/sync.Map，不引 Prometheus 依赖，把埋点成本压到
// 最低；将来要上监控体系时再把同一批数字重新导出即可。
package realtime

import (
	"sort"
	"sync"
	"sync/atomic"
)

// Metrics 收集描述实时子系统的轻量计数器。
//
// 一期改造（MUL-1138）在 phase-0 的计数集合上补充了订阅、Redis、按作用域房间
// 三类计数。仍用标准库原子计数而不是 Prometheus 依赖；后续阶段可以只换导出层。
type Metrics struct {
	ConnectsTotal        atomic.Int64
	DisconnectsTotal     atomic.Int64
	ActiveConnections    atomic.Int64
	SlowEvictionsTotal   atomic.Int64
	MessagesSentTotal    atomic.Int64
	MessagesDroppedTotal atomic.Int64

	// InboundTooLargeTotal 统计「因对端发来单条超限消息（超过 inboundReadLimit）
	// 而被关掉的连接数」，鉴权前/鉴权后两条读取路径都算。
	InboundTooLargeTotal atomic.Int64

	// eventSent：按事件类型字符串分桶的发送计数。值为 *atomic.Int64。
	eventSent sync.Map

	// 按作用域类型字符串("workspace"/"user"/"task"/"chat")分桶的订阅/退订/被拒
	// 计数，以及「某类型作用域当前活跃房间数」的仪表值。value 都是 *atomic.Int64。
	subscribeTotal       sync.Map
	unsubscribeTotal     sync.Map
	subscribeDeniedTotal sync.Map
	scopeRooms           sync.Map

	// Redis relay 的计数器。Redis 广播没启用时全是零。
	RedisXAddTotal               atomic.Int64
	RedisXAddErrors              atomic.Int64
	RedisXReadTotal              atomic.Int64
	RedisXReadErrors             atomic.Int64
	RedisAckTotal                atomic.Int64
	RedisLastXAddLagMicros       atomic.Int64
	RedisMirrorPrimaryErrors     atomic.Int64
	RedisMirrorSecondaryErrors   atomic.Int64
	RedisMirrorDivergenceTotal   atomic.Int64
	RedisRelayStreamTrimmedTotal atomic.Int64
	RedisRelayStreamMissingTotal atomic.Int64
	RedisRelayRetentionErrors    atomic.Int64
	RedisRelayStreamsWithoutTTL  atomic.Int64
	RedisUsedMemoryBytes         atomic.Int64
	RedisMaxMemoryBytes          atomic.Int64
	RedisEvictedKeys             atomic.Int64

	redisStreamsMu sync.RWMutex
	redisStreams   map[string]RedisStreamObservation

	redisTTLStatusMu              sync.Mutex
	redisStreamsWithoutTTLByRelay map[string]int64

	// RedisConnected 由 relay 在启动/重连成功时置 true。
	RedisConnected atomic.Bool
	// RedisLastError 记录最近一次消费端错误信息（下方 redisLastErr 字段的读写锁与缓存）。
	redisLastErrMu sync.RWMutex
	redisLastErr   string

	// NodeID 在启动时由 relay 设置一次（单机模式保持为空字符串）。
	NodeID atomic.Value // string
}

// RedisStreamObservation 是某条 relay stream 最近一次低频留存抽样的结果。
// PTTLMillis 采用 Redis 哨兵值：-1 表示该 key 没设过期，-2 表示 key 已不存在。
type RedisStreamObservation struct {
	Entries     int64 `json:"entries"`
	MemoryBytes int64 `json:"memory_bytes"`
	PTTLMillis  int64 `json:"pttl_millis"`
}

// M 是包级指标单例，realtime 各处（hub/relay/retention）都往它上面累加。
var M = &Metrics{}

// loadOrInitCounter 从 sync.Map 里取 key 对应的计数器；没有就新建一个放进去
// （LoadOrStore 保证并发下也只会有一个实例生效，不会互相覆盖）。
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

// RecordEvent 让「按事件类型」的发送计数 +1。
func (m *Metrics) RecordEvent(eventType string) {
	if eventType == "" {
		return
	}
	loadOrInitCounter(&m.eventSent, eventType).Add(1)
}

// SubscribesTotal 返回某作用域类型下「成功订阅次数」的计数器。
func (m *Metrics) SubscribesTotal(scopeType string) *atomic.Int64 {
	return loadOrInitCounter(&m.subscribeTotal, scopeType)
}

// UnsubscribesTotal 返回某作用域类型下「退订次数」的计数器。
func (m *Metrics) UnsubscribesTotal(scopeType string) *atomic.Int64 {
	return loadOrInitCounter(&m.unsubscribeTotal, scopeType)
}

// SubscribeDeniedTotal 返回某作用域类型下「订阅被拒次数」的计数器。
func (m *Metrics) SubscribeDeniedTotal(scopeType string) *atomic.Int64 {
	return loadOrInitCounter(&m.subscribeDeniedTotal, scopeType)
}

// IncRoom / DecRoom 增减某作用域类型的活跃房间数仪表值。
func (m *Metrics) IncRoom(scopeType string) { loadOrInitCounter(&m.scopeRooms, scopeType).Add(1) }
func (m *Metrics) DecRoom(scopeType string) { loadOrInitCounter(&m.scopeRooms, scopeType).Add(-1) }

// SetRedisLastError 把 msg 记成最近一次 Redis 消费端错误。传空串会清掉旧错误。
func (m *Metrics) SetRedisLastError(msg string) {
	m.redisLastErrMu.Lock()
	m.redisLastErr = msg
	m.redisLastErrMu.Unlock()
}

func (m *Metrics) lastRedisErr() string {
	m.redisLastErrMu.RLock()
	defer m.redisLastErrMu.RUnlock()
	return m.redisLastErr
}

// ObserveRedisStream 用一份新的采样替换 stream 的最新统计。
func (m *Metrics) ObserveRedisStream(stream string, entries, memoryBytes, pttlMillis int64) {
	m.redisStreamsMu.Lock()
	defer m.redisStreamsMu.Unlock()
	if m.redisStreams == nil {
		m.redisStreams = make(map[string]RedisStreamObservation)
	}
	m.redisStreams[stream] = RedisStreamObservation{
		Entries:     entries,
		MemoryBytes: memoryBytes,
		PTTLMillis:  pttlMillis,
	}
}

// RedisStreamObservations 返回一份可供采集端安全读取的副本。
func (m *Metrics) RedisStreamObservations() map[string]RedisStreamObservation {
	m.redisStreamsMu.RLock()
	defer m.redisStreamsMu.RUnlock()
	out := make(map[string]RedisStreamObservation, len(m.redisStreams))
	for stream, observation := range m.redisStreams {
		out[stream] = observation
	}
	return out
}

// SetRedisStreamsWithoutTTL 更新某一 relay 模式的「无 TTL stream 数」并暴露进程级
// 总和。按模式分开存，是为了避免双模式采集时，后跑完的那轮维护循环覆盖另一模式
// 的数据、把总数算错。
func (m *Metrics) SetRedisStreamsWithoutTTL(relay string, count int64) {
	m.redisTTLStatusMu.Lock()
	defer m.redisTTLStatusMu.Unlock()
	if m.redisStreamsWithoutTTLByRelay == nil {
		m.redisStreamsWithoutTTLByRelay = make(map[string]int64)
	}
	m.redisStreamsWithoutTTLByRelay[relay] = count
	var total int64
	for _, relayCount := range m.redisStreamsWithoutTTLByRelay {
		total += relayCount
	}
	m.RedisRelayStreamsWithoutTTL.Store(total)
}

// snapshotCounters 把 sync.Map 里的计数器导成字符串→数值的普通 map，并按 key 排序，
// 保证同一时刻多次打快照输出顺序稳定、便于肉眼比对。
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

// Snapshot 返回当前各计数器的 JSON 友好副本，供健康检查/调试输出。
func (m *Metrics) Snapshot() map[string]any {
	nodeID := ""
	if v := m.NodeID.Load(); v != nil {
		nodeID, _ = v.(string)
	}
	return map[string]any{
		"connects_total":          m.ConnectsTotal.Load(),
		"disconnects_total":       m.DisconnectsTotal.Load(),
		"active_connections":      m.ActiveConnections.Load(),
		"slow_evictions_total":    m.SlowEvictionsTotal.Load(),
		"messages_sent_total":     m.MessagesSentTotal.Load(),
		"messages_dropped_total":  m.MessagesDroppedTotal.Load(),
		"inbound_too_large_total": m.InboundTooLargeTotal.Load(),
		"events_sent_by_type":     snapshotCounters(&m.eventSent),
		"subscribes_total":        snapshotCounters(&m.subscribeTotal),
		"unsubscribes_total":      snapshotCounters(&m.unsubscribeTotal),
		"subscribe_denied_total":  snapshotCounters(&m.subscribeDeniedTotal),
		"active_scope_rooms":      snapshotCounters(&m.scopeRooms),
		"redis": map[string]any{
			"connected":               m.RedisConnected.Load(),
			"node_id":                 nodeID,
			"xadd_total":              m.RedisXAddTotal.Load(),
			"xadd_errors":             m.RedisXAddErrors.Load(),
			"xread_total":             m.RedisXReadTotal.Load(),
			"xread_errors":            m.RedisXReadErrors.Load(),
			"ack_total":               m.RedisAckTotal.Load(),
			"last_xadd_lag_micros":    m.RedisLastXAddLagMicros.Load(),
			"mirror_primary_errors":   m.RedisMirrorPrimaryErrors.Load(),
			"mirror_secondary_errors": m.RedisMirrorSecondaryErrors.Load(),
			"mirror_divergence_total": m.RedisMirrorDivergenceTotal.Load(),
			"stream_trimmed_total":    m.RedisRelayStreamTrimmedTotal.Load(),
			"stream_missing_total":    m.RedisRelayStreamMissingTotal.Load(),
			"retention_errors":        m.RedisRelayRetentionErrors.Load(),
			"streams_without_ttl":     m.RedisRelayStreamsWithoutTTL.Load(),
			"used_memory_bytes":       m.RedisUsedMemoryBytes.Load(),
			"max_memory_bytes":        m.RedisMaxMemoryBytes.Load(),
			"evicted_keys":            m.RedisEvictedKeys.Load(),
			"streams":                 m.RedisStreamObservations(),
			"last_error":              m.lastRedisErr(),
		},
	}
}

// Reset 把所有计数器清零。仅供测试用。
func (m *Metrics) Reset() {
	m.ConnectsTotal.Store(0)
	m.DisconnectsTotal.Store(0)
	m.ActiveConnections.Store(0)
	m.SlowEvictionsTotal.Store(0)
	m.MessagesSentTotal.Store(0)
	m.MessagesDroppedTotal.Store(0)
	m.InboundTooLargeTotal.Store(0)
	m.eventSent.Range(func(k, _ any) bool { m.eventSent.Delete(k); return true })
	m.subscribeTotal.Range(func(k, _ any) bool { m.subscribeTotal.Delete(k); return true })
	m.unsubscribeTotal.Range(func(k, _ any) bool { m.unsubscribeTotal.Delete(k); return true })
	m.subscribeDeniedTotal.Range(func(k, _ any) bool { m.subscribeDeniedTotal.Delete(k); return true })
	m.scopeRooms.Range(func(k, _ any) bool { m.scopeRooms.Delete(k); return true })
	m.RedisXAddTotal.Store(0)
	m.RedisXAddErrors.Store(0)
	m.RedisXReadTotal.Store(0)
	m.RedisXReadErrors.Store(0)
	m.RedisAckTotal.Store(0)
	m.RedisLastXAddLagMicros.Store(0)
	m.RedisMirrorPrimaryErrors.Store(0)
	m.RedisMirrorSecondaryErrors.Store(0)
	m.RedisMirrorDivergenceTotal.Store(0)
	m.RedisRelayStreamTrimmedTotal.Store(0)
	m.RedisRelayStreamMissingTotal.Store(0)
	m.RedisRelayRetentionErrors.Store(0)
	m.RedisRelayStreamsWithoutTTL.Store(0)
	m.redisTTLStatusMu.Lock()
	m.redisStreamsWithoutTTLByRelay = nil
	m.redisTTLStatusMu.Unlock()
	m.RedisUsedMemoryBytes.Store(0)
	m.RedisMaxMemoryBytes.Store(0)
	m.RedisEvictedKeys.Store(0)
	m.redisStreamsMu.Lock()
	m.redisStreams = nil
	m.redisStreamsMu.Unlock()
	m.RedisConnected.Store(false)
	m.SetRedisLastError("")
}
