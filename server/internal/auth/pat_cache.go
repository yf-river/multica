// 本文件实现个人访问令牌（PAT，mul_）解析结果的 Redis 缓存，以及为 PAT / daemon
// token 两类缓存共用的 TTL 计算工具。缓存把「每请求一次 DB 查 PAT」压成「每个 TTL
// 窗口一次」，代价是吊销后最多延迟一个 TTL 窗口生效。
package auth

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

// AuthCacheTTL 限定 token 哈希查询在回到 Postgres 前能缓存多久。PATCache 与
// DaemonTokenCache 共用此值，让两类 token 遵循同一套「吊销延迟」契约。
// 取值折中：短到即使漏了一次失效通知，吊销滞后也可控；长到高频客户端
// （CLI、daemon）能把「每请求一次 DB 往返」降到「每个 TTL 窗口一次」。
const AuthCacheTTL = 10 * time.Minute

// patCachePrefix 把鉴权缓存键与 realtime relay（ws:*）和本地 skill
// （mul:local_skill:*）的键隔开。
const patCachePrefix = "mul:auth:pat:"

// PATCache 在 Redis 里缓存已解析的 PAT 查询。nil 的 *PATCache 可安全使用——
// 每个方法要么空操作、要么报缓存 miss，鉴权中间件退化为直查数据库。
type PATCache struct {
	rdb *redis.Client
}

// NewPATCache 返回由 rdb 支撑的缓存。传 nil 表示禁用缓存；返回的 *PATCache
// 可安全调用，只是永远打不到 Redis。
func NewPATCache(rdb *redis.Client) *PATCache {
	if rdb == nil {
		return nil
	}
	return &PATCache{rdb: rdb}
}

// patCacheKey 拼缓存键：固定前缀 + token 哈希。
func patCacheKey(hash string) string { return patCachePrefix + hash }

// Get 返回 token 哈希对应的缓存 user_id。缓存 miss 或任何 Redis 错误都返回
// ok=false——死掉的 Redis 绝不能拖垮鉴权。
func (c *PATCache) Get(ctx context.Context, hash string) (userID string, ok bool) {
	if c == nil {
		return "", false
	}
	v, err := c.rdb.Get(ctx, patCacheKey(hash)).Result()
	if err != nil {
		// redis.Nil = 正常 miss，不告警；其它错误记日志（Redis 出问题时能看见）。
		if !errors.Is(err, redis.Nil) {
			slog.Warn("pat_cache: get failed; falling back to DB", "error", err)
		}
		return "", false
	}
	return v, true
}

// Set 按给定 TTL 写入缓存。调用方必须保证 TTL 不超过 token 的剩余有效期——否则缓存
// 条目可能活过 PAT 的 expires_at，让一个已过期的 token 靠缓存命中继续通过鉴权。
// 用 TTLForExpiry 从 token 的 expires_at 计算 TTL。
//
// 写失败只记日志不报错——缓存写失败不是请求失败。
func (c *PATCache) Set(ctx context.Context, hash, userID string, ttl time.Duration) {
	if c == nil || ttl <= 0 {
		return
	}
	if err := c.rdb.Set(ctx, patCacheKey(hash), userID, ttl).Err(); err != nil {
		slog.Warn("pat_cache: set failed", "error", err)
	}
}

// TTLForExpiry 给定 token 的 expires_at，返回应使用的缓存 TTL：
//   - expiresAt 为零值（token 永不过期）→ 用满 AuthCacheTTL；
//   - expiresAt 在未来 → min(AuthCacheTTL, 到过期还剩多久)；
//   - expiresAt 已到期（<= now）→ 0（调用方应跳过缓存；中间件正常到不了这里，
//     因为 SELECT 已经滤掉过期 token，但 SELECT 与 Set 之间仍可能发生 TOCTOU）。
//
// token 无过期时间时传 time.Time{}（pgtype.Timestamptz 的 Valid=false 会映射成
// 零值 Time）。
func TTLForExpiry(now, expiresAt time.Time) time.Duration {
	if expiresAt.IsZero() {
		return AuthCacheTTL
	}
	remaining := expiresAt.Sub(now)
	if remaining <= 0 {
		return 0
	}
	if remaining < AuthCacheTTL {
		return remaining
	}
	return AuthCacheTTL
}

// Invalidate 删除 hash 对应的条目。在 PAT 被吊销时调用，让吊销立即生效，
// 而不必等 TTL 自然过期。
func (c *PATCache) Invalidate(ctx context.Context, hash string) {
	if c == nil {
		return
	}
	if err := c.rdb.Del(ctx, patCacheKey(hash)).Err(); err != nil {
		slog.Warn("pat_cache: invalidate failed; entry will expire on TTL", "error", err)
	}
}
