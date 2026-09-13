// sharded_stream_relay.go 实现分片版 Redis relay（ShardedStreamRelay）：所有实时
// 事件均匀散列进「固定数量」的 Redis Stream 分片，每台 API 节点对每个分片跑一条
// XREAD BLOCK 消费循环，读到事件后再按本机 hub 的订阅情况过滤/扇出。相比「每个
// 作用域一条 stream + 一个消费者」的旧方案，它的阻塞连接数只与「节点数×分片数」
// 成正比，而不是与活跃作用域数量成正比——作用域很多时不会把 Redis 连接池耗尽。
package realtime

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/oklog/ulid/v2"
	"github.com/redis/go-redis/v9"
)

// 分片版 relay 的默认参数。这些值针对「单条 relay 条目约 1 KiB」的实测基线取：
// 每条分片 stream 保留 2000 条，8 个分片合计约 16 MiB，而不是旧默认值近 800 MiB；
// 运营者可根据线上条目大小再调整 StreamMaxLen。
const (
	defaultShardedRelayShards       = 8
	defaultShardedRelayStreamMaxLen = 2000
	defaultShardedRelayReadCount    = 128
	defaultShardedRelayReadBlock    = 5 * time.Second
	defaultShardedRelayReplayGrace  = 5 * time.Minute
)

// ShardedStreamKey 返回某个固定分片对应的 Redis Stream 键名。
func ShardedStreamKey(shard int) string {
	return fmt.Sprintf("ws:relay:shard:%d", shard)
}

// ShardedStreamRelayConfig 控制分片版（固定消费者）Redis Stream relay 的行为。
type ShardedStreamRelayConfig struct {
	Shards       int
	StreamMaxLen int64
	ReadCount    int64
	ReadBlock    time.Duration
	// ReplayGrace 是启动时的回看窗口：分片读循环从 (now - ReplayGrace) 这个位置开始
	// 消费，而不是从 "$"（只收新消息）开始，这样本 pod 停机期间发布的事件也会被重放。
	// 回看量受 stream 的 MAXLEN 限制，下游消费方必须能幂等处理重复。
	ReplayGrace         time.Duration
	TrimHorizon         time.Duration
	StreamTTL           time.Duration
	TTLRefreshInterval  time.Duration
	MaintenanceInterval time.Duration
	StreamTTLEnabled    bool
}

// DefaultShardedStreamRelayConfig 返回适合生产的默认值：每个 pod 每分片一个固定阻塞
// 读者、有界的 stream 留存、批量读取。
func DefaultShardedStreamRelayConfig() ShardedStreamRelayConfig {
	retention := DefaultStreamRetentionConfig()
	return ShardedStreamRelayConfig{
		Shards:              defaultShardedRelayShards,
		StreamMaxLen:        retention.StreamMaxLen,
		ReadCount:           defaultShardedRelayReadCount,
		ReadBlock:           defaultShardedRelayReadBlock,
		ReplayGrace:         defaultShardedRelayReplayGrace,
		TrimHorizon:         retention.TrimHorizon,
		StreamTTL:           retention.StreamTTL,
		TTLRefreshInterval:  retention.TTLRefreshInterval,
		MaintenanceInterval: retention.MaintenanceInterval,
		StreamTTLEnabled:    retention.StreamTTLEnabled,
	}
}

// RetentionConfig 把分片模式的留存参数收敛成与模式无关的 StreamRetentionConfig，
// 供旧版 relay 复用同一套口径。
func (c ShardedStreamRelayConfig) RetentionConfig() StreamRetentionConfig {
	c = c.withDefaults()
	return StreamRetentionConfig{
		StreamMaxLen:        c.StreamMaxLen,
		TrimHorizon:         c.TrimHorizon,
		StreamTTL:           c.StreamTTL,
		TTLRefreshInterval:  c.TTLRefreshInterval,
		MaintenanceInterval: c.MaintenanceInterval,
		StreamTTLEnabled:    c.StreamTTLEnabled,
	}
}

// withDefaults 用默认值补齐缺项，并修复不安全的留存关系（详见 Validate 的约束）。
func (c ShardedStreamRelayConfig) withDefaults() ShardedStreamRelayConfig {
	def := DefaultShardedStreamRelayConfig()
	if c.Shards <= 0 {
		c.Shards = def.Shards
	}
	if c.StreamMaxLen <= 0 {
		c.StreamMaxLen = def.StreamMaxLen
	}
	if c.ReadCount <= 0 {
		c.ReadCount = def.ReadCount
	}
	if c.ReadBlock <= 0 {
		c.ReadBlock = def.ReadBlock
	}
	if c.ReplayGrace <= 0 {
		c.ReplayGrace = def.ReplayGrace
	}
	// 裁剪视野必须比回看窗口大，否则「重放」还没来得及读完就被裁剪掉。
	if c.TrimHorizon <= c.ReplayGrace {
		c.TrimHorizon = 2 * c.ReplayGrace
	}
	if c.StreamTTL < c.TrimHorizon {
		c.StreamTTL = c.TrimHorizon + c.ReplayGrace
	}
	if c.TTLRefreshInterval <= 0 || c.TTLRefreshInterval >= c.StreamTTL {
		c.TTLRefreshInterval = retentionSubinterval(c.StreamTTL, def.TTLRefreshInterval)
	}
	if c.MaintenanceInterval <= 0 || c.MaintenanceInterval >= c.StreamTTL {
		c.MaintenanceInterval = retentionSubinterval(c.StreamTTL, def.MaintenanceInterval)
	}
	return c
}

// retentionSubinterval 求「更偏好 preferred，但必须严格小于 ttl」的子间隔：preferred
// 可行就用它，否则退回 ttl/3（ttl 太小连三等分都做不了时，用 1ns 兜底避免除零）。
func retentionSubinterval(ttl, preferred time.Duration) time.Duration {
	if preferred > 0 && preferred < ttl {
		return preferred
	}
	interval := ttl / 3
	if interval <= 0 {
		return time.Nanosecond
	}
	return interval
}

// Normalized 返回补齐了缺项、修复了不安全留存关系的配置副本。
func (c ShardedStreamRelayConfig) Normalized() ShardedStreamRelayConfig {
	return c.withDefaults()
}

// Validate 校验关键的留存约束链：裁剪必须落在「回看窗口之外」（保证待重放的消息还在），
// 同时 stream 仍能安全过期（空闲的 key 到期被清）。
func (c ShardedStreamRelayConfig) Validate() error {
	if c.ReplayGrace <= 0 {
		return errors.New("ReplayGrace must be positive")
	}
	if c.TrimHorizon <= c.ReplayGrace {
		return fmt.Errorf("TrimHorizon (%s) must be greater than ReplayGrace (%s)", c.TrimHorizon, c.ReplayGrace)
	}
	if c.StreamTTL < c.TrimHorizon {
		return fmt.Errorf("StreamTTL (%s) must be at least TrimHorizon (%s)", c.StreamTTL, c.TrimHorizon)
	}
	if c.TTLRefreshInterval <= 0 || c.TTLRefreshInterval >= c.StreamTTL {
		return fmt.Errorf("TTLRefreshInterval (%s) must be positive and less than StreamTTL (%s)", c.TTLRefreshInterval, c.StreamTTL)
	}
	if c.MaintenanceInterval <= 0 || c.MaintenanceInterval >= c.StreamTTL {
		return fmt.Errorf("MaintenanceInterval (%s) must be positive and less than StreamTTL (%s)", c.MaintenanceInterval, c.StreamTTL)
	}
	return nil
}

// ShardedStreamRelay 把全部实时事件写进一组固定数量的 Redis Stream。每台 API 节点
// 对每个分片跑一个 XREAD BLOCK 消费循环，并按本地 hub 订阅情况过滤事件。这样被
// 阻塞占用的 Redis 连接数上界是 pod_count×shard_count，而不是 active_scope_count。
type ShardedStreamRelay struct {
	hub      *Hub
	writeRDB *redis.Client
	readRDB  *redis.Client
	nodeID   string
	config   ShardedStreamRelayConfig
	now      func() time.Time
	ttl      *streamTTLRefresher

	mu       sync.Mutex
	stopping bool
	wg       sync.WaitGroup

	// 按分片索引记录状态：streamSeen 表示该分片 stream 是否见过写入；
	// streamGeneration 在「stream 曾经存在后来消失」时 +1，让读取循环跳回回看起点，
	// 以免对着一条空 stream 用旧游标读、把重建后新写入的事件漏掉。
	streamSeen       []atomic.Bool
	streamGeneration []atomic.Uint64

	daemonRuntime DaemonRuntimeDeliverer
	wecomOutbound WecomOutboundDeliverer
}

// NewShardedStreamRelay 构造分片版 relay（写、读可分两个 Redis 客户端）。
func NewShardedStreamRelay(hub *Hub, writeRDB, readRDB *redis.Client, config ShardedStreamRelayConfig) *ShardedStreamRelay {
	if readRDB == nil {
		readRDB = writeRDB
	}
	config = config.withDefaults()
	return &ShardedStreamRelay{
		hub:              hub,
		writeRDB:         writeRDB,
		readRDB:          readRDB,
		nodeID:           ulid.Make().String(),
		config:           config,
		now:              time.Now,
		ttl:              newStreamTTLRefresher(config.StreamTTL, config.TTLRefreshInterval),
		streamSeen:       make([]atomic.Bool, config.Shards),
		streamGeneration: make([]atomic.Uint64, config.Shards),
	}
}

// NodeID 返回本 relay 随机生成的节点标识。
func (r *ShardedStreamRelay) NodeID() string { return r.nodeID }

// SetWecomOutboundDeliverer 挂上 wecom 出站投递器。
func (r *ShardedStreamRelay) SetWecomOutboundDeliverer(d WecomOutboundDeliverer) {
	r.wecomOutbound = d
}

// SetDaemonRuntimeDeliverer 挂上 daemon 唤醒帧投递器。
func (r *ShardedStreamRelay) SetDaemonRuntimeDeliverer(d DaemonRuntimeDeliverer) {
	r.daemonRuntime = d
}

// Start 启动心跳、留存维护与「每分片一条」的读取循环，并登记节点 ID。
func (r *ShardedStreamRelay) Start(ctx context.Context) {
	M.NodeID.Store(r.nodeID)
	if err := r.writeRDB.Ping(ctx).Err(); err != nil {
		slog.Error("realtime/sharded-redis: initial ping failed", "error", err)
		M.RedisConnected.Store(false)
		M.SetRedisLastError(err.Error())
	} else if r.readRDB != r.writeRDB {
		if err := r.readRDB.Ping(ctx).Err(); err != nil {
			slog.Error("realtime/sharded-redis: initial read-client ping failed", "error", err)
			M.RedisConnected.Store(false)
			M.SetRedisLastError(err.Error())
		} else {
			M.RedisConnected.Store(true)
		}
	} else {
		M.RedisConnected.Store(true)
	}

	r.wg.Add(2 + r.config.Shards)
	go func() {
		defer r.wg.Done()
		r.heartbeatLoop(ctx)
	}()
	go func() {
		defer r.wg.Done()
		r.retentionLoop(ctx)
	}()
	for shard := 0; shard < r.config.Shards; shard++ {
		shard := shard
		go func() {
			defer r.wg.Done()
			r.readShard(ctx, shard)
		}()
	}
}

// Stop 置位停止标记，让读取循环在下一轮退出（不主动掐断阻塞中的 XREAD，等其超时返回）。
func (r *ShardedStreamRelay) Stop() {
	r.mu.Lock()
	r.stopping = true
	r.mu.Unlock()
}

// Wait 阻塞直到所有后台 goroutine 退出。
func (r *ShardedStreamRelay) Wait() {
	r.wg.Wait()
}

// BroadcastToScope 把消息按作用域散列进某个分片后发布。
func (r *ShardedStreamRelay) BroadcastToScope(scopeType, scopeID string, message []byte) {
	_ = r.PublishWithID(scopeType, scopeID, "", message, ulid.Make().String())
}

// BroadcastToWorkspace 是「发给某 workspace 全部连接」的简写。
func (r *ShardedStreamRelay) BroadcastToWorkspace(workspaceID string, message []byte) {
	r.BroadcastToScope(ScopeWorkspace, workspaceID, message)
}

// SendToUser 给某用户的所有连接发消息（可排除指定 workspace）。
func (r *ShardedStreamRelay) SendToUser(userID string, message []byte, excludeWorkspace ...string) {
	exclude := ""
	if len(excludeWorkspace) > 0 {
		exclude = excludeWorkspace[0]
	}
	_ = r.PublishWithID(ScopeUser, userID, exclude, message, ulid.Make().String())
}

// Broadcast 向全局分片发一条 daemon 级广播。
func (r *ShardedStreamRelay) Broadcast(message []byte) {
	_ = r.PublishWithID("global", "all", "", message, ulid.Make().String())
}

// PublishWithID 把信封 XADD 进由 (scopeType, scopeID) 散列确定的分片 stream。
func (r *ShardedStreamRelay) PublishWithID(scopeType, scopeID, exclude string, frame []byte, id string) error {
	ev := newEnvelope(r.nodeID, scopeType, scopeID, exclude, frame, id)
	shard := r.shardFor(scopeType, scopeID)
	stream := ShardedStreamKey(shard)
	args := &redis.XAddArgs{
		Stream: stream,
		MaxLen: r.config.StreamMaxLen,
		Approx: true,
		Values: envelopeRedisValues(ev),
	}

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := r.writeRDB.XAdd(ctx, args).Err(); err != nil {
		M.RedisXAddErrors.Add(1)
		M.SetRedisLastError(err.Error())
		slog.Warn("realtime/sharded-redis: XADD failed", "error", err, "scope", scopeType, "scope_id", scopeID, "stream", stream)
		return err
	}
	M.RedisXAddTotal.Add(1)
	M.RedisLastXAddLagMicros.Store(time.Since(start).Microseconds())
	r.streamSeen[shard].Store(true)
	if r.config.StreamTTLEnabled {
		if err := r.ttl.refreshIfDue(ctx, r.writeRDB, stream); err != nil {
			r.recordRetentionError("PEXPIRE failed", err, "stream", stream)
		}
	}
	return nil
}

// shardFor 用 FNV 哈希把 (scopeType, scopeID) 稳定映射到 0..Shards-1 的一个分片；
// 中间补一个 0x00 分隔字节，避免 "ab"+"c" 与 "a"+"bc" 这类拼接歧义撞到同一哈希。
func (r *ShardedStreamRelay) shardFor(scopeType, scopeID string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(scopeType))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(scopeID))
	return int(h.Sum32() % uint32(r.config.Shards))
}

// replayStartID 返回锚定在 (now - ReplayGrace) 的 stream ID。新起的读取循环从这里
// 开始，就只会重放最近这段回看窗口内的消息，而不是把整条保留 stream 从头读一遍。
// 末尾的 "-0" 会匹配该毫秒内的任意序号。
func (r *ShardedStreamRelay) replayStartID() string {
	return streamMinID(r.now(), r.config.ReplayGrace)
}

// readShard 是单个分片的常驻读取循环：不断 XREAD，读到新消息就投递；当检测到该
// 分片 stream 曾被删重建（generation 变化）时，把游标重置回回看起点，避免漏读重建
// 后新写入的消息。
func (r *ShardedStreamRelay) readShard(ctx context.Context, shard int) {
	stream := ShardedStreamKey(shard)
	// 起点用有界的回看窗口而不是 "$"，目的是重放本 pod 停机期间产生的事件。窗口
	// 足够短，重放量可控；下游消费方（daemon 唤醒、客户端重连）本来就是幂等的。
	lastID := r.replayStartID()
	generation := r.streamGeneration[shard].Load()
	for {
		if ctx.Err() != nil || r.isStopping() {
			return
		}
		if current := r.streamGeneration[shard].Load(); current != generation {
			lastID = r.replayStartID()
			generation = current
		}
		if !r.readShardOnce(ctx, shard, stream, &lastID) {
			return
		}
	}
}

// retentionLoop 周期性地对全部分片 stream 做留存维护（先立即跑一轮再定时）。
func (r *ShardedStreamRelay) retentionLoop(ctx context.Context) {
	r.maintainStreams(ctx)
	ticker := time.NewTicker(r.config.MaintenanceInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.maintainStreams(ctx)
		}
	}
}

// maintainStreams 对每个分片 stream：确认存在性、裁剪超龄消息、补/修 TTL、采样
// 长度与内存指标；不存在时更新分片存在状态（触发 generation 变化让读取循环回跳），
// 最后把缺 TTL 统计与 Redis 服务器级指标刷进 M。
func (r *ShardedStreamRelay) maintainStreams(ctx context.Context) {
	maintCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	minID := streamMinID(r.now(), r.config.TrimHorizon)
	withoutTTL := int64(0)

	for shard := 0; shard < r.config.Shards; shard++ {
		if maintCtx.Err() != nil {
			return
		}
		stream := ShardedStreamKey(shard)
		exists, err := r.writeRDB.Exists(maintCtx, stream).Result()
		if err != nil {
			r.recordRetentionError("EXISTS failed", err, "stream", stream)
			continue
		}
		// 分片 stream 不存在：更新存在状态（会触发 generation 变化），并按「不存在」
		// 打采样（PTTL 哨兵值 -2）。
		if exists == 0 {
			r.updateStreamPresence(shard, false)
			M.ObserveRedisStream(stream, 0, 0, -2)
			continue
		}
		r.updateStreamPresence(shard, true)

		trimmed, err := r.writeRDB.XTrimMinID(maintCtx, stream, minID).Result()
		if err != nil && !errors.Is(err, redis.Nil) {
			r.recordRetentionError("XTRIM MINID failed", err, "stream", stream, "min_id", minID)
		} else if trimmed > 0 {
			M.RedisRelayStreamTrimmedTotal.Add(trimmed)
		}

		ttl, err := r.ttl.reconcileTTL(maintCtx, r.writeRDB, stream, r.config.StreamTTLEnabled)
		if r.config.StreamTTLEnabled && ttl == -1 {
			withoutTTL++
		}
		if err != nil {
			r.recordRetentionError("stream TTL repair failed", err, "stream", stream)
		}

		length, err := r.writeRDB.XLen(maintCtx, stream).Result()
		if err != nil && !errors.Is(err, redis.Nil) {
			r.recordRetentionError("XLEN failed", err, "stream", stream)
			continue
		}
		memoryBytes, err := r.writeRDB.MemoryUsage(maintCtx, stream).Result()
		if err != nil && !errors.Is(err, redis.Nil) {
			r.recordRetentionError("MEMORY USAGE failed", err, "stream", stream)
			memoryBytes = 0
		}
		M.ObserveRedisStream(stream, length, memoryBytes, redisTTLMillis(ttl))
	}
	M.SetRedisStreamsWithoutTTL("sharded", withoutTTL)
	r.observeRedisServer(maintCtx)
}

// updateStreamPresence 记录某个分片 stream 的存在状态。由存在变为不存在（且此前确实
// 存在过）时，把 generation +1 并清掉它的 TTL 缓存，让读取循环重置回看游标。
func (r *ShardedStreamRelay) updateStreamPresence(shard int, exists bool) {
	if exists {
		r.streamSeen[shard].Store(true)
		return
	}
	if r.streamSeen[shard].Swap(false) {
		r.streamGeneration[shard].Add(1)
		r.ttl.forget(ShardedStreamKey(shard))
		M.RedisRelayStreamMissingTotal.Add(1)
	}
}

// observeRedisServer 用 redis INFO 打点 Redis 的内存与驱逐指标。
func (r *ShardedStreamRelay) observeRedisServer(ctx context.Context) {
	memoryInfo, err := r.writeRDB.Info(ctx, "memory").Result()
	if err == nil {
		if used, ok := redisInfoInt64(memoryInfo, "used_memory"); ok {
			M.RedisUsedMemoryBytes.Store(used)
		}
		if max, ok := redisInfoInt64(memoryInfo, "maxmemory"); ok {
			M.RedisMaxMemoryBytes.Store(max)
		}
	}
	statsInfo, err := r.writeRDB.Info(ctx, "stats").Result()
	if err == nil {
		if evicted, ok := redisInfoInt64(statsInfo, "evicted_keys"); ok {
			M.RedisEvictedKeys.Store(evicted)
		}
	}
}

// recordRetentionError 统一记录一次留存维护错误：计数 + 记最近错误 + 打 warn 日志。
func (r *ShardedStreamRelay) recordRetentionError(message string, err error, attrs ...any) {
	M.RedisRelayRetentionErrors.Add(1)
	M.SetRedisLastError(err.Error())
	attrs = append([]any{"error", err}, attrs...)
	slog.Warn("realtime/sharded-redis: "+message, attrs...)
}

// readShardOnce 对某个分片做一轮 XREAD。返回 true 表示调用方应继续读；返回 false
// 表示 context 已结束、循环该退出。lastID 会被推进到已读消息的最新 id 之后。
func (r *ShardedStreamRelay) readShardOnce(ctx context.Context, shard int, stream string, lastID *string) bool {
	readCtx, cancel := context.WithTimeout(ctx, r.config.ReadBlock+time.Second)
	res, err := r.readRDB.XRead(readCtx, &redis.XReadArgs{
		Streams: []string{stream, *lastID},
		Count:   r.config.ReadCount,
		Block:   r.config.ReadBlock,
	}).Result()
	cancel()

	// 阻塞超时 / 无新消息都属于正常等待，继续循环即可。
	if errors.Is(err, redis.Nil) || (err != nil && (errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled))) {
		return true
	}
	if err != nil {
		M.RedisXReadErrors.Add(1)
		M.SetRedisLastError(err.Error())
		slog.Warn("realtime/sharded-redis: XREAD failed", "error", err, "shard", shard, "stream", stream)
		// 退避一秒，避免连接抖动时在报错循环里空转。
		select {
		case <-ctx.Done():
			return false
		case <-time.After(time.Second):
		}
		return true
	}

	for _, s := range res {
		for _, msg := range s.Messages {
			*lastID = msg.ID
			M.RedisXReadTotal.Add(1)
			r.deliverMessage(msg)
		}
	}
	return true
}

// deliverMessage 把读回的一条消息还原成信封并投递；信封缺作用域字段就丢弃（分片
// 模式消息里一定带 scope，缺了说明格式异常）。
func (r *ShardedStreamRelay) deliverMessage(msg redis.XMessage) {
	ev, ok := envelopeFromXMessage(msg)
	if !ok || ev.Scope == "" || ev.ScopeID == "" {
		return
	}
	deliverEnvelope(r.hub, r.daemonRuntime, r.wecomOutbound, ev)
}

// heartbeatLoop 周期性地刷新本节点心跳键（先立即发一次再定时）。
func (r *ShardedStreamRelay) heartbeatLoop(ctx context.Context) {
	t := time.NewTicker(heartbeatPeriod)
	defer t.Stop()
	for {
		r.heartbeatOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// heartbeatOnce 刷一次心跳键。分片模式没有按作用域维护的 nodes 集合，所以只需要
// 证明「本节点活着」即可，不用逐个作用域 ZAdd。
func (r *ShardedStreamRelay) heartbeatOnce(ctx context.Context) {
	hbCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := r.writeRDB.Set(hbCtx, HeartbeatKey(r.nodeID), time.Now().UTC().Format(time.RFC3339Nano), heartbeatTTL).Err(); err != nil {
		M.RedisConnected.Store(false)
		M.SetRedisLastError(err.Error())
		return
	}
	M.RedisConnected.Store(true)
}

func (r *ShardedStreamRelay) isStopping() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stopping
}

// 编译期断言：ShardedStreamRelay 同时满足 Broadcaster 与 RelayPublisher。
var _ Broadcaster = (*ShardedStreamRelay)(nil)
var _ RelayPublisher = (*ShardedStreamRelay)(nil)
