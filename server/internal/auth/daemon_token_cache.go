// 本文件实现 daemon token（mdt_）解析结果的 Redis 缓存。中间件查一次库把 token 哈希
// 解析成 (workspace_id, daemon_id) 后缓存起来，窗口内免查库。Redis 不可用或条目损坏
// 一律按 cache miss 处理、落回 DB，缓存绝不允许把鉴权拖垮。
package auth

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

// daemonTokenCachePrefix 给 daemon-token 缓存键单独加命名空间，与 PAT
// （mul:auth:pat:*）的键空间隔开——两套键互不撞车，也不会出现「失效一种 token 的
// 缓存误删另一种」。
const daemonTokenCachePrefix = "mul:auth:daemon:"

// DaemonTokenIdentity 是 DaemonAuth 从缓存命中结果里需要的字段——中间件要把
// workspace_id 与 daemon_id 注入请求 context。刻意不缓存 token_hash、expires_at 和
// 行 id：缓存条目应当只泄露最小必要信息。
type DaemonTokenIdentity struct {
	WorkspaceID string `json:"w"`
	DaemonID    string `json:"d"`
}

// DaemonTokenCache 在 Redis 里缓存已解析的 daemon-token（mdt_）查询。
// nil 的 *DaemonTokenCache 也可安全使用——每个方法要么空操作、要么报缓存 miss，
// 这样单机开发/没有 REDIS_URL 的测试会干净地退化为直查数据库。
type DaemonTokenCache struct {
	rdb *redis.Client
}

// NewDaemonTokenCache 返回由 rdb 支撑的缓存。传 nil 表示禁用缓存；返回的
// *DaemonTokenCache 可安全调用，只是永远打不到 Redis。
func NewDaemonTokenCache(rdb *redis.Client) *DaemonTokenCache {
	if rdb == nil {
		return nil
	}
	return &DaemonTokenCache{rdb: rdb}
}

// daemonTokenCacheKey 拼缓存键：固定前缀 + token 哈希。
func daemonTokenCacheKey(hash string) string { return daemonTokenCachePrefix + hash }

// Get 返回 token 哈希对应的缓存身份。缓存 miss 或任何 Redis/解码错误都返回
// ok=false——死掉的 Redis 绝不能拖垮鉴权，调用方应落回 DB。
func (c *DaemonTokenCache) Get(ctx context.Context, hash string) (DaemonTokenIdentity, bool) {
	if c == nil {
		return DaemonTokenIdentity{}, false
	}
	raw, err := c.rdb.Get(ctx, daemonTokenCacheKey(hash)).Bytes()
	if err != nil {
		// redis.Nil = 正常 miss，不告警；其它错误说明 Redis 出问题，记一条日志。
		if !errors.Is(err, redis.Nil) {
			slog.Warn("daemon_token_cache: get failed; falling back to DB", "error", err)
		}
		return DaemonTokenIdentity{}, false
	}
	var id DaemonTokenIdentity
	if err := json.Unmarshal(raw, &id); err != nil {
		slog.Warn("daemon_token_cache: malformed entry; falling back to DB", "error", err)
		return DaemonTokenIdentity{}, false
	}
	return id, true
}

// Set 按给定 TTL 写入缓存。应先用 TTLForExpiry 把 TTL 夹到 token 的剩余有效期内，
// 避免一个快过期（剩余 < AuthCacheTTL）的 daemon token 靠缓存命中活过它的 expires_at。
//
// 写失败只记日志不报错——缓存写失败不是请求失败。
func (c *DaemonTokenCache) Set(ctx context.Context, hash string, id DaemonTokenIdentity, ttl time.Duration) {
	if c == nil || ttl <= 0 {
		return
	}
	raw, err := json.Marshal(id)
	if err != nil {
		slog.Warn("daemon_token_cache: marshal failed", "error", err)
		return
	}
	if err := c.rdb.Set(ctx, daemonTokenCacheKey(hash), raw, ttl).Err(); err != nil {
		slog.Warn("daemon_token_cache: set failed", "error", err)
	}
}

// Invalidate 删除 hash 对应的条目。在 daemon token 被删除时调用，让删除立即生效，
// 而不必等 TTL 自然过期。
func (c *DaemonTokenCache) Invalidate(ctx context.Context, hash string) {
	if c == nil {
		return
	}
	if err := c.rdb.Del(ctx, daemonTokenCacheKey(hash)).Err(); err != nil {
		slog.Warn("daemon_token_cache: invalidate failed; entry will expire on TTL", "error", err)
	}
}
