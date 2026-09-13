// empty_claim_cache.go —— “该 runtime 当前无排队任务”的 Redis 缓存（带版本标签防竞态），
// 让 daemon 轮询认领在稳态空闲时跳过 Postgres。
package service

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// emptyClaimCacheKey 存放“无排队任务”的结论，并带它被观察时的 per-runtime 版本标签。emptyClaimVersionKey 是
// per-runtime 单调计数器，任何入队路径都会 bump。结论仅在值等于当前版本时被信任，从而关掉“慢认领在入队已
// 失效之后才写入空结论”的竞态：
//
//	T1 认领：  v0 := GET version
//	            SELECT ... -> 空
//	            （慢，例如 GC 停顿）
//	T2 入队：  INSERT row
//	            INCR version  (-> v1)
//	            wakeup
//	T1 认领：  SET empty = v0
//	T3 认领：  v1' := GET version (== v1)
//	            GET empty (== v0) -> v0 != v1，视为 miss -> SELECT
//
// 没有版本标签时 T3 会命中陈旧空键，刚入队的任务将空转到空键 TTL 过期。有了它，剩下的窗口只是每次并发入队
// 每 runtime 多一次 DB SELECT，绝不会任务卡住。
const (
	emptyClaimCachePrefix   = "mul:claim:runtime:empty:"
	emptyClaimVersionPrefix = "mul:claim:runtime:version:"
)

// EmptyClaimCacheTTL 限制缓存 “无排队任务” 结论的可信时长。入队通过 bump per-runtime 版本（在唤醒 daemon 前）
// 使结论失效，因此更长的 TTL 能让稳态空闲轮询路径不碰 Postgres。TTL 仍是错过失效的安全网——例如 Bump 期间
// 一次瞬时 Redis 故障。
const EmptyClaimCacheTTL = 3 * time.Minute

// emptyClaimVersionTTL 让版本计数器活得足够久：轮询稀疏的 runtime 不会在入队 INCR 与下次认领 GET 之间重置为 0
// （那会让陈旧带标签的空键突然看起来有效）。滑动 TTL 在每次 Bump 与 Get 时续期。
const emptyClaimVersionTTL = 24 * time.Hour

// emptyClaimRedisTimeout 限制本缓存的每次 Redis 调用时长。入队路径用后台 context 让缓存活过请求，但卡死的
// Redis 不能无限拖住入队——限定爆炸半径，降级为 “无缓存”。
const emptyClaimRedisTimeout = 250 * time.Millisecond

// EmptyClaimCache 缓存 “该 runtime 当前无排队任务”，使 daemon 轮询认领路径可在命中 Postgres 前短路。只缓存
// 否定结果；肯定结果总是重查 DB，让并发认领者公平竞争 `ClaimAgentTask` 的 `FOR UPDATE SKIP LOCKED`。
//
// 缓存会在每次入队时同步失效（见 TaskService.notifyTaskAvailable）。nil *EmptyClaimCache 可安全使用——
// 每个方法都变 no-op 或报 miss，无 REDIS_URL 的单节点开发/测试因此干净降级为直查 DB。
type EmptyClaimCache struct {
	rdb *redis.Client
}

// NewEmptyClaimCache 返回以 rdb 为后端的缓存。传 nil 以禁用缓存；返回的 *EmptyClaimCache 可安全调用
// 但永不触 Redis。
func NewEmptyClaimCache(rdb *redis.Client) *EmptyClaimCache {
	if rdb == nil {
		return nil
	}
	return &EmptyClaimCache{rdb: rdb}
}

// emptyClaimKey / emptyClaimVersion 拼出结论键与版本键。
func emptyClaimKey(runtimeID string) string     { return emptyClaimCachePrefix + runtimeID }
func emptyClaimVersion(runtimeID string) string { return emptyClaimVersionPrefix + runtimeID }

// bounded 为每次 Redis 调用加上统一短超时。
func (c *EmptyClaimCache) bounded(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, emptyClaimRedisTimeout)
}

// CurrentVersion 返回 runtime 当前失效版本。调用方【必须】在要缓存的那次 DB SELECT 之前读它，再把它回传给
// MarkEmpty，使并发的 Bump 能失效这次将写的缓存。缓存 miss 或任何 Redis 错误时返回 0（视为 “未知”）——
// 调用方落到 DB 路径。
//
// 版本键读取时带短 Expire 刷新，长时间空闲的 runtime 不会让计数器在入队 Bump 与下次认领 MarkEmpty 之间
// 过期重置为 0。
func (c *EmptyClaimCache) CurrentVersion(ctx context.Context, runtimeID string) int64 {
	if c == nil || runtimeID == "" {
		return 0
	}
	bctx, cancel := c.bounded(ctx)
	defer cancel()
	v, err := c.rdb.Get(bctx, emptyClaimVersion(runtimeID)).Int64()
	if err != nil {
		if !errors.Is(err, redis.Nil) {
			slog.Warn("empty_claim_cache: version get failed; falling back to DB", "error", err)
		}
		return 0
	}
	// Refresh TTL so the counter doesn't expire and reset on a low-
	// traffic runtime. Errors here are best-effort.
	c.rdb.Expire(bctx, emptyClaimVersion(runtimeID), emptyClaimVersionTTL)
	return v
}

// IsEmpty returns true only when (a) an empty verdict is cached AND
// (b) it carries the runtime's current version. A stale verdict
// written before a concurrent Bump returns false so the caller falls
// through to the DB.
func (c *EmptyClaimCache) IsEmpty(ctx context.Context, runtimeID string) bool {
	if c == nil || runtimeID == "" {
		return false
	}
	bctx, cancel := c.bounded(ctx)
	defer cancel()
	// MGET 返回 []interface{}，元素为值（string）或 nil。
	vals, err := c.rdb.MGet(bctx, emptyClaimKey(runtimeID), emptyClaimVersion(runtimeID)).Result()
	if err != nil {
		slog.Warn("empty_claim_cache: mget failed; falling back to DB", "error", err)
		return false
	}
	if len(vals) != 2 || vals[0] == nil {
		return false
	}
	emptyVer, ok := vals[0].(string)
	if !ok {
		return false
	}
	// 版本键缺失表示 “从未有入队 bump 过该 runtime”，逻辑上即版本 0——与 CurrentVersion miss 时返回的值相同。
	// 以 v=0 写入的 MarkEmpty 必须在此匹配，否则新 runtime 的快路径永远不会触发。
	curVer := "0"
	if vals[1] != nil {
		if s, ok := vals[1].(string); ok {
			curVer = s
		}
	}
	return emptyVer == curVer
}

// MarkEmpty 存储带 observedVersion 标签的空结论。该结论之后仅在 observedVersion 仍等于当前版本时被信任
// （见 IsEmpty）。传 CurrentVersion 在“确认该 runtime 为空”的 SELECT【之前】返回的值；两者间的一次并发 Bump
// 会让下一位读者拒绝该条目、强制重查 DB。
//
// 错误只记日志并被吞掉——缓存写失败不是请求失败。
func (c *EmptyClaimCache) MarkEmpty(ctx context.Context, runtimeID string, observedVersion int64) {
	if c == nil || runtimeID == "" {
		return
	}
	bctx, cancel := c.bounded(ctx)
	defer cancel()
	if err := c.rdb.Set(bctx, emptyClaimKey(runtimeID), strconv.FormatInt(observedVersion, 10), EmptyClaimCacheTTL).Err(); err != nil {
		slog.Warn("empty_claim_cache: set failed", "error", err)
	}
}

// Bump 递增 runtime 的失效版本。每条入队路径都在 daemon WS 唤醒【之前】调用，使前一版本下写出的任何结论在
// 下次读取时被拒，无需单独 DEL 空键。
//
// 错误只记日志并被吞掉——Redis 抖动不能阻止一次合法入队。空键仍按自身 TTL 过期，最坏停顿因此有界。
func (c *EmptyClaimCache) Bump(ctx context.Context, runtimeID string) {
	if c == nil || runtimeID == "" {
		return
	}
	bctx, cancel := c.bounded(ctx)
	defer cancel()
	pipe := c.rdb.Pipeline()
	pipe.Incr(bctx, emptyClaimVersion(runtimeID))
	pipe.Expire(bctx, emptyClaimVersion(runtimeID), emptyClaimVersionTTL)
	if _, err := pipe.Exec(bctx); err != nil {
		slog.Warn("empty_claim_cache: bump failed; entry will expire on TTL", "error", err)
	}
}
