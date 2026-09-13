// redis_relay.go 实现 RedisRelay：以 Redis Stream 为中介，把单台机器上的本地广播
// 扩散成「所有副本都能收到」的跨节点广播。思路是把每条消息 XADD 进按作用域划分的
// stream，每个副本只消费「本机有人订阅」的 stream，消费回来后再交给本地 Hub 扇出；
// 全程用 event_id 去重，避免同一条事件既走本地快路径又走 Redis 回环时被推两遍。
// 文件还实现了「双写广播」(DualWriteBroadcaster) 与新老实现灰度共存的辅助逻辑。
package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
	"github.com/redis/go-redis/v9"
)

// 以下三个函数统一生成 Redis 里用到的 key 名，集中定义方便测试探测 key 结构：
// stream 是事件流本身，nodes 是「对本作用域感兴趣的节点」有序集合（存各节点心跳
// 到期时间，用于清理死节点），heartbeat 是单节点的存活标记。
func StreamKey(scopeType, scopeID string) string {
	return fmt.Sprintf("ws:scope:%s:%s:stream", scopeType, scopeID)
}
func NodesKey(scopeType, scopeID string) string {
	return fmt.Sprintf("ws:scope:%s:%s:nodes", scopeType, scopeID)
}
func HeartbeatKey(nodeID string) string {
	return fmt.Sprintf("ws:node:%s:heartbeat", nodeID)
}

const (
	// heartbeatTTL 是节点心跳键的存活时长：超过这么久没续，就认为该节点已死。
	heartbeatTTL = 90 * time.Second
	// heartbeatPeriod 是每个节点刷新自己心跳的间隔（TTL 的三分之一，留足重试余量）。
	heartbeatPeriod = 30 * time.Second
	// consumerIdleGrace 已不再被代码引用，保留为旧常量，注释说明其历史用途：在清理
	// 消费组前允许「有本机订阅但 XREAD 长时间没返回」的消费循环的宽限窗口。
	consumerIdleGrace = 10 * time.Minute
	// legacyStreamScanCount 是清扫旧版 stream 时每次 SCAN 抓取的 key 数量。
	legacyStreamScanCount = 128
)

// envelope 是写进每条 XADD 消息的结构体。它对 Hub 是「不透明」的：relay 把
// payload_json 解出来（并注入 event_id）后才交给 hub 扇出。extra 的 event_type /
// actor_id 只是从原始帧里扒出来供监控用的摘要字段。
type envelope struct {
	EventID     string `json:"event_id"`
	EventType   string `json:"event_type"`
	Scope       string `json:"scope"`
	ScopeID     string `json:"scope_id"`
	WorkspaceID string `json:"workspace_id"`
	ActorID     string `json:"actor_id"`
	CreatedAt   string `json:"created_at"`
	NodeID      string `json:"node_id"`
	PayloadJSON string `json:"payload_json"` // 原始 ws 帧的 JSON 原文
}

// newEnvelope 组装一条信封。exclude 非空时放到 WorkspaceID 字段，表示「推给目标
// 房间里除了该 workspace 之外的连接」（member:added 去重语义）。
func newEnvelope(nodeID, scopeType, scopeID, exclude string, frame []byte, id string) envelope {
	ev := envelope{
		EventID:     id,
		Scope:       scopeType,
		ScopeID:     scopeID,
		NodeID:      nodeID,
		CreatedAt:   time.Now().UTC().Format(time.RFC3339Nano),
		PayloadJSON: string(frame),
	}
	if exclude != "" {
		ev.WorkspaceID = exclude
	}
	if t, a := peekTypeActor(frame); t != "" {
		ev.EventType = t
		ev.ActorID = a
	}
	return ev
}

// envelopeRedisValues 把信封摊成 Redis XADD 用的字段表（顺序无所谓，key 即字段名）。
func envelopeRedisValues(ev envelope) map[string]any {
	return map[string]any{
		"event_id":     ev.EventID,
		"event_type":   ev.EventType,
		"scope":        ev.Scope,
		"scope_id":     ev.ScopeID,
		"workspace_id": ev.WorkspaceID,
		"actor_id":     ev.ActorID,
		"created_at":   ev.CreatedAt,
		"node_id":      ev.NodeID,
		"payload_json": ev.PayloadJSON,
	}
}

// envelopeFromXMessage 把 Redis 读回来的 XMessage 还原成信封。ok=false 表示这条
// 消息缺 payload_json，多半是旧格式或写了一半，直接丢弃。
func envelopeFromXMessage(msg redis.XMessage) (envelope, bool) {
	ev := envelope{
		EventID:     redisString(msg.Values["event_id"]),
		EventType:   redisString(msg.Values["event_type"]),
		Scope:       redisString(msg.Values["scope"]),
		ScopeID:     redisString(msg.Values["scope_id"]),
		WorkspaceID: redisString(msg.Values["workspace_id"]),
		ActorID:     redisString(msg.Values["actor_id"]),
		CreatedAt:   redisString(msg.Values["created_at"]),
		NodeID:      redisString(msg.Values["node_id"]),
		PayloadJSON: redisString(msg.Values["payload_json"]),
	}
	return ev, ev.PayloadJSON != ""
}

// redisString 把 Redis 返回的字段值（可能是 string 或 []byte）规整成 string。
func redisString(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case []byte:
		return string(s)
	default:
		return ""
	}
}

// deliverEnvelope 按信封的作用域类型把消息投给正确的消费方：daemon_runtime 与
// wecom_outbound 走各自专用的 deliverer（只投给持有对应连接的那台节点），普通
// workspace/user/global 交给 hub 扇出。投递前先把 event_id 注入帧内供客户端去重。
func deliverEnvelope(hub *Hub, daemonRuntime DaemonRuntimeDeliverer, wecomOutbound WecomOutboundDeliverer, ev envelope) {
	if ev.PayloadJSON == "" {
		return
	}
	frame := injectEventID([]byte(ev.PayloadJSON), ev.EventID)
	switch ev.Scope {
	case ScopeDaemonRuntime:
		if daemonRuntime != nil {
			daemonRuntime.DeliverDaemonRuntime(ev.ScopeID, frame, ev.EventID)
		}
	case ScopeWecomOutbound:
		if wecomOutbound != nil {
			wecomOutbound.DeliverWecomOutbound(ev.ScopeID, frame, ev.EventID)
		}
	case "global":
		hub.fanoutAllDedup(frame, "", ev.EventID)
	case ScopeUser:
		hub.fanoutUser(ev.ScopeID, frame, ev.WorkspaceID, ev.EventID)
	default:
		hub.BroadcastToScopeDedup(ev.Scope, ev.ScopeID, frame, ev.EventID)
	}
}

// RedisRelay 是一个 Broadcaster 实现：每条消息都会写进「按作用域划分」的 Redis
// Stream；同时只消费「本机存在订阅者」的那些 stream。本机扇出交给内部持有的 *Hub。
//
// 写与读拆成两个 Redis 客户端（readRDB 专用于会长时间阻塞的 XREADGROUP BLOCK），
// 避免长轮询的消费循环把用于 XADD/心跳/XACK 的普通连接池占满。
type RedisRelay struct {
	hub       *Hub
	writeRDB  *redis.Client
	readRDB   *redis.Client
	nodeID    string
	retention StreamRetentionConfig
	ttl       *streamTTLRefresher
	now       func() time.Time

	// consumers 是本机正在跑的「每个作用域一个」的消费循环；mu 保护它和 stopping。
	// stopping 置位后 startConsumer 不再启动新的循环（关停流程中）。
	mu        sync.Mutex
	consumers map[scopeKey]*scopeConsumer
	stopping  bool
	wg        sync.WaitGroup

	// 旧版(无 TTL) stream 的清扫状态：legacyScanCursor 记 SCAN 进度，
	// streamsWithoutTTL / ttlScanSeen 配合判断哪些 stream 还缺 TTL。
	legacyScanCursor  uint64
	ttlStatusMu       sync.Mutex
	streamsWithoutTTL map[string]struct{}
	ttlScanSeen       map[string]struct{}

	daemonRuntime DaemonRuntimeDeliverer
	wecomOutbound WecomOutboundDeliverer
}

// scopeConsumer 记录一个作用域消费循环的取消函数，Stop 时靠它掐断循环。
type scopeConsumer struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// NewRedisRelay 构造一个 relay（读写共用同一个 Redis 客户端）。调用方负责在开始
// 产生消息前先调用 Start。
func NewRedisRelay(hub *Hub, rdb *redis.Client) *RedisRelay {
	return NewRedisRelayWithClients(hub, rdb, rdb)
}

// NewRedisRelayWithClients 用两个独立的 Redis 客户端构造 relay：一个写、一个做阻塞
// 读。读客户端专门留给 XREADGROUP BLOCK 这类长轮询，避免它们把 XADD、心跳、XACK
// 等请求路径操作所用的连接池耗尽。
func NewRedisRelayWithClients(hub *Hub, writeRDB, readRDB *redis.Client) *RedisRelay {
	return NewRedisRelayWithClientsAndConfig(hub, writeRDB, readRDB, DefaultStreamRetentionConfig())
}

// NewRedisRelayWithClientsAndConfig 在构造时注入与分片模式相同的 stream 留存控制
// 参数，保证旧版与双写灰度两种 rollout 的留存策略不会悄悄分叉。
func NewRedisRelayWithClientsAndConfig(hub *Hub, writeRDB, readRDB *redis.Client, retention StreamRetentionConfig) *RedisRelay {
	if readRDB == nil {
		readRDB = writeRDB
	}
	retention = retention.withDefaults()
	return &RedisRelay{
		hub:               hub,
		writeRDB:          writeRDB,
		readRDB:           readRDB,
		nodeID:            ulid.Make().String(),
		retention:         retention,
		ttl:               newStreamTTLRefresher(retention.StreamTTL, retention.TTLRefreshInterval),
		now:               time.Now,
		consumers:         make(map[scopeKey]*scopeConsumer),
		streamsWithoutTTL: make(map[string]struct{}),
		ttlScanSeen:       make(map[string]struct{}),
	}
}

// NodeID 返回本 relay 随机生成的节点标识。
func (r *RedisRelay) NodeID() string { return r.nodeID }

// SetWecomOutboundDeliverer 挂上「wecom 出站投递器」（在别的副本里持有该实例
// socket 时负责真正把回复发出去）。
func (r *RedisRelay) SetWecomOutboundDeliverer(d WecomOutboundDeliverer) {
	r.wecomOutbound = d
}

// SetDaemonRuntimeDeliverer 挂上 daemon 唤醒帧投递器。
func (r *RedisRelay) SetDaemonRuntimeDeliverer(d DaemonRuntimeDeliverer) {
	r.daemonRuntime = d
}

// Wait 阻塞直到 Start 传入的 context 被取消后，所有 relay 拥有的 goroutine 退出。
func (r *RedisRelay) Wait() {
	r.wg.Wait()
}

// Stop 禁止再启动新的作用域消费循环，并取消所有在跑的循环。心跳与清扫循环仍由
// Start 的 context 控制，调用方应先取消它再调 Wait。
func (r *RedisRelay) Stop() {
	r.hub.SetSubscriptionCallbacks(nil, nil)

	r.mu.Lock()
	r.stopping = true
	for _, c := range r.consumers {
		c.cancel()
	}
	r.mu.Unlock()
}

// Start 把 hub 的订阅回调接到 relay 上、启动心跳 goroutine，并为 hub 当前已知的
// 作用域各拉起一个消费循环。ctx 控制所有后台 goroutine：取消它就关停整个 relay。
func (r *RedisRelay) Start(ctx context.Context) {
	M.NodeID.Store(r.nodeID)
	if err := r.writeRDB.Ping(ctx).Err(); err != nil {
		slog.Error("realtime/redis: initial ping failed", "error", err)
		M.RedisConnected.Store(false)
		M.SetRedisLastError(err.Error())
	} else if r.readRDB != r.writeRDB {
		if err := r.readRDB.Ping(ctx).Err(); err != nil {
			slog.Error("realtime/redis: initial read-client ping failed", "error", err)
			M.RedisConnected.Store(false)
			M.SetRedisLastError(err.Error())
		} else {
			M.RedisConnected.Store(true)
		}
	} else {
		M.RedisConnected.Store(true)
	}

	// 订阅回调让 relay 跟着 hub 的订阅变化起停消费循环。
	r.hub.SetSubscriptionCallbacks(
		func(scopeType, scopeID string) { r.startConsumer(ctx, scopeType, scopeID) },
		func(scopeType, scopeID string) { r.stopConsumer(scopeType, scopeID) },
	)

	for _, key := range r.hub.LocalScopes() {
		r.startConsumer(ctx, key.Type, key.ID)
	}

	r.wg.Add(2)
	go func() {
		defer r.wg.Done()
		r.heartbeatLoop(ctx)
	}()
	go func() {
		defer r.wg.Done()
		r.consumerSweeper(ctx)
	}()
}

// BroadcastToScope 把消息发布进该作用域的 Redis stream。信封里带的 event_id 供
// 客户端去重。本机扇出靠本节点 XREADGROUP 读回自己写的内容完成——但在双写配置下，
// 本地 Hub 会被直接调用、不走这条回环。
func (r *RedisRelay) BroadcastToScope(scopeType, scopeID string, message []byte) {
	r.publish(scopeType, scopeID, "", message)
}

// BroadcastToWorkspace / SendToUser / Broadcast 是 Broadcaster 向后兼容部分的实现。
func (r *RedisRelay) BroadcastToWorkspace(workspaceID string, message []byte) {
	r.publish(ScopeWorkspace, workspaceID, "", message)
}
func (r *RedisRelay) SendToUser(userID string, message []byte, excludeWorkspace ...string) {
	exclude := ""
	if len(excludeWorkspace) > 0 {
		exclude = excludeWorkspace[0]
	}
	r.publish(ScopeUser, userID, exclude, message)
}
func (r *RedisRelay) Broadcast(message []byte) {
	// daemon 广播——写进一个特殊的 "global" stream，这样其它节点无论有没有人订阅
	// 具体作用域，都能收到并扇给全体客户端。
	r.publish("global", "all", "", message)
}

// publish 用一条新生成的 ULID 作为事件 id 发布（id 由调用方在稍后的 publishWithID
// 层面使用；这里只是为了统一入口）。
func (r *RedisRelay) publish(scopeType, scopeID, exclude string, frame []byte) {
	_ = r.publishWithID(scopeType, scopeID, exclude, frame, ulid.Make().String())
}

// publishWithID 真正执行 XADD。stream 设了 MaxLen 上限，写满会按近似裁剪，防止单条
// 冷门 stream 无限增长占内存。TTL 启用时顺带（按需）刷新过期时间。
func (r *RedisRelay) publishWithID(scopeType, scopeID, exclude string, frame []byte, id string) error {
	ev := newEnvelope(r.nodeID, scopeType, scopeID, exclude, frame, id)
	stream := StreamKey(scopeType, scopeID)
	args := &redis.XAddArgs{
		Stream: stream,
		MaxLen: r.retention.StreamMaxLen,
		Approx: true,
		Values: envelopeRedisValues(ev),
	}
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := r.writeRDB.XAdd(ctx, args).Err(); err != nil {
		M.RedisXAddErrors.Add(1)
		M.SetRedisLastError(err.Error())
		slog.Warn("realtime/redis: XADD failed", "error", err, "scope", scopeType, "scope_id", scopeID)
		return err
	}
	M.RedisXAddTotal.Add(1)
	M.RedisLastXAddLagMicros.Store(time.Since(start).Microseconds())
	if r.retention.StreamTTLEnabled {
		if err := r.ttl.refreshIfDue(ctx, r.writeRDB, stream); err != nil {
			r.recordRetentionError("PEXPIRE failed", err, "stream", stream)
		}
	}
	return nil
}

// startConsumer 为某个作用域启动一条 XREADGROUP 消费循环（已存在或已在关停中就不动）。
func (r *RedisRelay) startConsumer(parent context.Context, scopeType, scopeID string) {
	key := sk(scopeType, scopeID)
	r.mu.Lock()
	if r.stopping || parent.Err() != nil {
		r.mu.Unlock()
		return
	}
	if _, exists := r.consumers[key]; exists {
		r.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(parent)
	c := &scopeConsumer{cancel: cancel, done: make(chan struct{})}
	r.consumers[key] = c
	r.wg.Add(1)
	r.mu.Unlock()

	go func() {
		defer r.wg.Done()
		r.runConsumer(ctx, c, scopeType, scopeID)
	}()
}

// stopConsumer 取消某个作用域的消费循环（如果它在跑）。
func (r *RedisRelay) stopConsumer(scopeType, scopeID string) {
	key := sk(scopeType, scopeID)
	r.mu.Lock()
	c, ok := r.consumers[key]
	if ok {
		delete(r.consumers, key)
	}
	r.mu.Unlock()
	if !ok {
		return
	}
	c.cancel()
}

// runConsumer 是一条作用域消费循环的主体：确保消费组存在、把自己登记为对该作用域
// 感兴趣的节点，然后反复 XREADGROUP 取新消息、投递给 hub、XACK 确认。
func (r *RedisRelay) runConsumer(ctx context.Context, c *scopeConsumer, scopeType, scopeID string) {
	defer close(c.done)

	stream := StreamKey(scopeType, scopeID)
	// 消费组与消费者都用节点 ID 命名，保证不同节点互不抢占同一条消息。
	group := "node:" + r.nodeID
	consumerName := r.nodeID

	// MKSTREAM 顺带把 stream 建出来。BUSYGROUP 表示组已存在，忽略。
	createCtx, createCancel := context.WithTimeout(ctx, 2*time.Second)
	if err := r.ensureConsumerGroup(createCtx, stream, group, "$"); err != nil {
		slog.Warn("realtime/redis: XGROUP CREATE failed", "error", err, "scope", scopeType, "scope_id", scopeID)
	}
	if r.retention.StreamTTLEnabled {
		if err := r.ttl.refreshIfDue(createCtx, r.writeRDB, stream); err != nil {
			r.recordRetentionError("consumer stream PEXPIRE failed", err, "stream", stream)
		}
	}
	createCancel()

	// 把自己登记进该作用域的 nodes 集合：score 是本次心跳的到期时间戳，供别人
	// 判断这个节点是否还活着。
	regCtx, regCancel := context.WithTimeout(ctx, 2*time.Second)
	r.writeRDB.ZAdd(regCtx, NodesKey(scopeType, scopeID), redis.Z{Score: float64(time.Now().Add(heartbeatTTL).Unix()), Member: r.nodeID})
	regCancel()

	for {
		if ctx.Err() != nil {
			break
		}
		readCtx, readCancel := context.WithTimeout(ctx, 6*time.Second)
		res, err := r.readRDB.XReadGroup(readCtx, &redis.XReadGroupArgs{
			Group:    group,
			Consumer: consumerName,
			Streams:  []string{stream, ">"},
			Count:    32,
			Block:    5 * time.Second,
		}).Result()
		readCancel()
		// 阻塞等待超时或没有新消息(Nil)都属正常，继续循环即可。
		if errors.Is(err, redis.Nil) || (err != nil && (errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled))) {
			continue
		}
		if err != nil {
			// NOGROUP 说明消费组丢失（stream 被 TTL 清理过等）。尝试重建：从 "0-0"
			// 起读会重放已有历史，但此处 stream 通常已空，重放无副作用。
			if strings.Contains(err.Error(), "NOGROUP") {
				repairCtx, repairCancel := context.WithTimeout(ctx, 2*time.Second)
				r.ttl.forget(stream)
				repairErr := r.ensureConsumerGroup(repairCtx, stream, group, "0-0")
				if repairErr == nil && r.retention.StreamTTLEnabled {
					repairErr = r.ttl.refreshIfDue(repairCtx, r.writeRDB, stream)
				}
				repairCancel()
				if repairErr == nil {
					M.RedisRelayStreamMissingTotal.Add(1)
					continue
				}
				r.recordRetentionError("consumer group recovery failed", repairErr, "stream", stream)
			}
			M.RedisXReadErrors.Add(1)
			M.SetRedisLastError(err.Error())
			// 退避一秒，避免连接抖动时在报错循环里空转烧 CPU。
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
			continue
		}
		// 投递 + ACK。先投后 ACK：万一 ACK 失败，消息会滞留在 pending 列表，将来
		// 别的消费者可能重投——但客户端侧有 event_id 去重，最多推重复不会丢。
		for _, s := range res {
			for _, msg := range s.Messages {
				M.RedisXReadTotal.Add(1)
				r.deliverMessage(scopeType, scopeID, msg)
				ackCtx, ackCancel := context.WithTimeout(ctx, time.Second)
				if err := r.writeRDB.XAck(ackCtx, stream, group, msg.ID).Err(); err != nil {
					slog.Debug("realtime/redis: XACK failed", "error", err, "id", msg.ID)
				} else {
					M.RedisAckTotal.Add(1)
				}
				ackCancel()
			}
		}
	}

	// 循环退出后把消费组里的自己删掉，做一次尽力而为的清理（失败不影响关停）。
	cleanCtx, cleanCancel := context.WithTimeout(context.Background(), 2*time.Second)
	r.writeRDB.XGroupDelConsumer(cleanCtx, stream, group, consumerName)
	cleanCancel()
}

// ensureConsumerGroup 确保 stream 与消费组存在（不存在就建）。BUSYGROUP（组已在）
// 不算错误。
func (r *RedisRelay) ensureConsumerGroup(ctx context.Context, stream, group, startID string) error {
	err := r.writeRDB.XGroupCreateMkStream(ctx, stream, group, startID).Err()
	if err != nil && !strings.Contains(err.Error(), "BUSYGROUP") {
		return err
	}
	return nil
}

// deliverMessage 把 Redis 里读回的一条 XMessage 还原成信封并投递；信封里作用域字段
// 缺失时用消费循环自身的作用域兜底（兼容早期只存 payload 的旧消息）。
func (r *RedisRelay) deliverMessage(scopeType, scopeID string, msg redis.XMessage) {
	ev, ok := envelopeFromXMessage(msg)
	if !ok {
		return
	}
	if ev.Scope == "" {
		ev.Scope = scopeType
	}
	if ev.ScopeID == "" {
		ev.ScopeID = scopeID
	}
	deliverEnvelope(r.hub, r.daemonRuntime, r.wecomOutbound, ev)
}

// heartbeatLoop 周期性地刷新本节点心跳（先立即发一次，再按 heartbeatPeriod 定时发）。
func (r *RedisRelay) heartbeatLoop(ctx context.Context) {
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

// heartbeatOnce 做一次心跳：SET 本节点的心跳键（带 TTL，用于故障检测），并把本机
// 当前所有活跃作用域的 nodes 集合刷新一遍，让其它节点知道本节点还对这些作用域感兴趣。
func (r *RedisRelay) heartbeatOnce(ctx context.Context) {
	hbCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := r.writeRDB.Set(hbCtx, HeartbeatKey(r.nodeID), time.Now().UTC().Format(time.RFC3339Nano), heartbeatTTL).Err(); err != nil {
		M.RedisConnected.Store(false)
		M.SetRedisLastError(err.Error())
		return
	}
	M.RedisConnected.Store(true)
	expiry := float64(time.Now().Add(heartbeatTTL).Unix())
	for _, key := range r.hub.LocalScopes() {
		r.writeRDB.ZAdd(hbCtx, NodesKey(key.Type, key.ID), redis.Z{Score: expiry, Member: r.nodeID})
	}
}

// consumerSweeper 周期性地清掉 nodes 集合里的过期节点，并对旧版（无 TTL 时代创建的）
// 按作用域划分的 stream 做一轮有界 SCAN 补 TTL。SCAN 保证不会阻塞 Redis。
func (r *RedisRelay) consumerSweeper(ctx context.Context) {
	t := time.NewTicker(r.retention.MaintenanceInterval)
	defer t.Stop()
	for {
		r.sweepLegacyStreams(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// sweepLegacyStreams 对「本机活跃作用域」与「SCAN 抓到的所有 ws:scope:*:stream」做
// 维护：裁剪超龄消息、补 TTL、并移除 nodes 集合中已经过期的成员。
func (r *RedisRelay) sweepLegacyStreams(ctx context.Context) {
	sweepCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	now := r.now()
	minID := streamMinID(now, r.retention.TrimHorizon)
	localStreams := make(map[string]struct{})

	for _, key := range r.hub.LocalScopes() {
		stream := StreamKey(key.Type, key.ID)
		localStreams[stream] = struct{}{}
		r.observeLegacyScanKey(stream)
		r.maintainLegacyStream(sweepCtx, stream, minID)
		// 把 score 早于当前时间的节点成员全部移除——它们心跳已过期，视为死节点。
		r.writeRDB.ZRemRangeByScore(sweepCtx, NodesKey(key.Type, key.ID), "-inf", fmt.Sprintf("%f", float64(now.Unix())))
	}

	// 用有界 SCAN 找旧版创建的 stream（带游标分页，不一次扫全库）。
	keys, nextCursor, err := r.writeRDB.Scan(sweepCtx, r.legacyScanCursor, "ws:scope:*:stream", legacyStreamScanCount).Result()
	if err != nil {
		r.recordRetentionError("legacy stream SCAN failed", err, "cursor", r.legacyScanCursor)
	} else {
		for _, stream := range keys {
			r.observeLegacyScanKey(stream)
			if _, alreadyMaintained := localStreams[stream]; alreadyMaintained {
				continue
			}
			r.maintainLegacyStream(sweepCtx, stream, minID)
		}
		if sweepCtx.Err() == nil {
			r.legacyScanCursor = nextCursor
			// 游标回到 0 表示一轮 SCAN 扫完，可以做一次「补齐缺失 TTL」的收尾统计。
			if nextCursor == 0 {
				r.completeLegacyTTLScan()
			}
		}
	}
	r.ttl.forgetStale(now.Add(-r.retention.StreamTTL))
}

// maintainLegacyStream 对一条旧版 stream 做留存维护：先按 minID 裁剪超龄消息，再
// 协调补 TTL（启用 TTL 保留时给还没过期的 key 设置/续期）。
func (r *RedisRelay) maintainLegacyStream(ctx context.Context, stream, minID string) {
	if trimmed, err := r.writeRDB.XTrimMinID(ctx, stream, minID).Result(); err != nil && !errors.Is(err, redis.Nil) {
		r.recordRetentionError("XTRIM MINID failed", err, "stream", stream, "min_id", minID)
	} else if trimmed > 0 {
		M.RedisRelayStreamTrimmedTotal.Add(trimmed)
	}
	ttl, err := r.ttl.reconcileTTL(ctx, r.writeRDB, stream, r.retention.StreamTTLEnabled)
	if err == nil || ttl == -1 || ttl == -2 {
		r.observeLegacyTTL(stream, ttl)
	}
	if err != nil {
		r.recordRetentionError("stream TTL repair failed", err, "stream", stream)
	}
}

// observeLegacyTTL 记录一条 stream 的 TTL 观察结果：启用 TTL 保留时，ttl==-1
// （Redis 哨兵值：key 没设过期）意味着这条 stream 缺 TTL，登记进待补集合。
func (r *RedisRelay) observeLegacyTTL(stream string, ttl time.Duration) {
	r.ttlStatusMu.Lock()
	if r.retention.StreamTTLEnabled && ttl == -1 {
		r.streamsWithoutTTL[stream] = struct{}{}
	} else {
		delete(r.streamsWithoutTTL, stream)
	}
	count := int64(len(r.streamsWithoutTTL))
	r.ttlStatusMu.Unlock()
	M.SetRedisStreamsWithoutTTL("legacy", count)
}

// observeLegacyScanKey 把一条 stream 记进「本轮 SCAN 见到过」的集合，供扫完后
// 判断哪些缺 TTL 的 stream 已经不存在了（被删了，不该再算）。
func (r *RedisRelay) observeLegacyScanKey(stream string) {
	r.ttlStatusMu.Lock()
	r.ttlScanSeen[stream] = struct{}{}
	r.ttlStatusMu.Unlock()
}

// completeLegacyTTLScan 在一轮完整 SCAN 结束后，清掉「缺 TTL 但本轮没扫到」的
// stream（说明它已被删除），并刷新进程级缺 TTL 统计。
func (r *RedisRelay) completeLegacyTTLScan() {
	r.ttlStatusMu.Lock()
	for stream := range r.streamsWithoutTTL {
		if _, exists := r.ttlScanSeen[stream]; !exists {
			delete(r.streamsWithoutTTL, stream)
		}
	}
	r.ttlScanSeen = make(map[string]struct{})
	count := int64(len(r.streamsWithoutTTL))
	r.ttlStatusMu.Unlock()
	M.SetRedisStreamsWithoutTTL("legacy", count)
}

// recordRetentionError 统一记录一次留存维护错误：计数 + 记最近错误 + 打 warn 日志。
func (r *RedisRelay) recordRetentionError(message string, err error, attrs ...any) {
	M.RedisRelayRetentionErrors.Add(1)
	M.SetRedisLastError(err.Error())
	attrs = append([]any{"error", err}, attrs...)
	slog.Warn("realtime/redis: "+message, attrs...)
}

// peekTypeActor 只把 ws 帧浅解析一点，取出 event_type / actor_id 填进信封做监控
// 摘要。解析失败返回空串即可——不影响信封正常流转。
func peekTypeActor(frame []byte) (string, string) {
	var probe struct {
		Type    string `json:"type"`
		ActorID string `json:"actor_id"`
	}
	_ = json.Unmarshal(frame, &probe)
	return probe.Type, probe.ActorID
}

// injectEventID 在不重新编码整个 payload 的前提下，往一个 JSON 对象帧里塞入
// event_id 字段。要求 frame 本身是 JSON 对象。
func injectEventID(frame []byte, eventID string) []byte {
	if eventID == "" || len(frame) == 0 || frame[0] != '{' {
		return frame
	}
	// 用「解码再编码」是最简单稳妥的做法，能避开行尾空白/嵌套转义等边界问题；
	// 每条消息多几次内存分配，相对网络开销可以忽略。
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(frame, &obj); err != nil {
		return frame
	}
	if _, exists := obj["event_id"]; exists {
		return frame
	}
	idJSON, _ := json.Marshal(eventID)
	obj["event_id"] = idJSON
	out, err := json.Marshal(obj)
	if err != nil {
		return frame
	}
	return out
}

// DualWriteBroadcaster 把每条消息同时交给本地 hub（立即扇出）和 Redis relay
// （跨节点扇出）。它通过 Client.markSeen 去重，因此当 relay 把同一条消息回环回来时，
// 已经收过本地投递的客户端不会再收第二遍。
type DualWriteBroadcaster struct {
	local *Hub
	relay RelayPublisher
}

// RelayPublisher 是所有能「携带调用方给定的事件 id 发布」的 Redis relay 后端要满足
// 的接口——带 id 是为了本地/Redis 回环两侧能共用同一把去重钥匙。
type RelayPublisher interface {
	PublishWithID(scopeType, scopeID, exclude string, frame []byte, id string) error
}

// NewDualWriteBroadcaster 构造一个双写广播器。
func NewDualWriteBroadcaster(local *Hub, relay RelayPublisher) *DualWriteBroadcaster {
	return newDualWriteBroadcaster(local, relay)
}

func newDualWriteBroadcaster(local *Hub, relay RelayPublisher) *DualWriteBroadcaster {
	return &DualWriteBroadcaster{local: local, relay: relay}
}

// BroadcastToScope 双写一条广播：本地 hub 先扇、Redis 再发。本地扇出带 event_id
// 标记，relay 回环同 id 的消息会被客户端去重逻辑忽略。
func (d *DualWriteBroadcaster) BroadcastToScope(scopeType, scopeID string, message []byte) {
	id := ulid.Make().String()
	frame := injectEventID(message, id)
	// 本地快路径：BroadcastToScopeDedup 会给每个客户端标记「已见过 id」，这样随后
	// Redis 回环里同 id 的消息会被 markSeen 拦下。
	d.local.BroadcastToScopeDedup(scopeType, scopeID, frame, id)
	_ = d.relay.PublishWithID(scopeType, scopeID, "", message, id)
}

func (d *DualWriteBroadcaster) BroadcastToWorkspace(workspaceID string, message []byte) {
	d.BroadcastToScope(ScopeWorkspace, workspaceID, message)
}

func (d *DualWriteBroadcaster) SendToUser(userID string, message []byte, excludeWorkspace ...string) {
	exclude := ""
	if len(excludeWorkspace) > 0 {
		exclude = excludeWorkspace[0]
	}
	id := ulid.Make().String()
	frame := injectEventID(message, id)
	d.local.fanoutUser(userID, frame, exclude, id)
	_ = d.relay.PublishWithID(ScopeUser, userID, exclude, message, id)
}

func (d *DualWriteBroadcaster) Broadcast(message []byte) {
	id := ulid.Make().String()
	frame := injectEventID(message, id)
	d.local.fanoutAllDedup(frame, "", id)
	_ = d.relay.PublishWithID("global", "all", "", message, id)
}

// PublishWithID 与 publish 相同，但使用调用方给定的事件 id，双写路径靠它做去重。
func (r *RedisRelay) PublishWithID(scopeType, scopeID, exclude string, frame []byte, id string) error {
	return r.publishWithID(scopeType, scopeID, exclude, frame, id)
}

// 编译期断言：RedisRelay / DualWriteBroadcaster 满足 Broadcaster，RedisRelay 还满足
// RelayPublisher。
var _ Broadcaster = (*RedisRelay)(nil)
var _ Broadcaster = (*DualWriteBroadcaster)(nil)
var _ RelayPublisher = (*RedisRelay)(nil)
