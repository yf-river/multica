// 本文件实现「用户 × 工作区成员身份是否存在」的 Redis 缓存。注意它只回答「是不是成员」，
// 绝不含角色信息——依赖角色的授权判定必须直查数据库，不允许碰这个缓存。
package auth

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

// membershipCachePrefix 成员缓存键前缀，与其它鉴权缓存键空间（mul:auth:pat:、
// mul:auth:daemon:）隔开。
const membershipCachePrefix = "mul:auth:member:"

// MembershipCacheTTL 限定工作区成员查询在回到 Postgres 前能缓存多久。
// 取值折中：短到被移出的成员在几分钟内失去访问权限；长到高频调用方
// （daemon 约每 15s 一次心跳）能把「每请求一次 DB 往返」降到「每个 TTL 窗口一次」。
const MembershipCacheTTL = 5 * time.Minute

// MembershipCache 在 Redis 里缓存「工作区成员身份存在性」的检查结果。
// 只跟踪用户是否属于某工作区——不存角色信息。依赖角色的授权判定
// （requireWorkspaceRole、RequireWorkspaceRoleFromURL）必须始终直查数据库。
//
// 吊销延迟：被移出的成员可能在最多 MembershipCacheTTL（5 分钟）内仍享受缓存的访问
// 权限。叠加 PATCache（10 分钟），最坏吊销延迟是 max(10m, 5m) = 10 分钟——
// 与 PATCache 最初的设计取舍保持一致。
//
// nil 的 *MembershipCache 可安全使用——每个方法要么空操作、要么报缓存 miss，
// 调用方退化为直查数据库。
type MembershipCache struct {
	rdb *redis.Client
}

// NewMembershipCache 返回由 rdb 支撑的缓存。传 nil 表示禁用缓存；返回的
// *MembershipCache 可安全调用，只是永远打不到 Redis。
func NewMembershipCache(rdb *redis.Client) *MembershipCache {
	if rdb == nil {
		return nil
	}
	return &MembershipCache{rdb: rdb}
}

// membershipKey 拼缓存键：前缀 + userID + ":" + workspaceID。
func membershipKey(userID, workspaceID string) string {
	return membershipCachePrefix + userID + ":" + workspaceID
}

// Get 返回该用户是否是工作区的（被缓存过的）成员。miss 或任何 Redis 错误都返回 false，
// 调用方落回 DB 查询。
func (c *MembershipCache) Get(ctx context.Context, userID, workspaceID string) (ok bool) {
	if c == nil {
		return false
	}
	_, err := c.rdb.Get(ctx, membershipKey(userID, workspaceID)).Result()
	if err != nil {
		// redis.Nil = 正常 miss，不告警；其它错误记日志。
		if !errors.Is(err, redis.Nil) {
			slog.Warn("membership_cache: get failed; falling back to DB", "error", err)
		}
		return false
	}
	return true
}

// Set 缓存「该用户 + 工作区 的成员关系存在」。值固定 "1"——存在即缓存，不存在靠
// 缓存 miss 表达，因此不存在性不会进缓存（成员是新增的，不存在性可能很快失效）。
func (c *MembershipCache) Set(ctx context.Context, userID, workspaceID string) {
	if c == nil {
		return
	}
	if err := c.rdb.Set(ctx, membershipKey(userID, workspaceID), "1", MembershipCacheTTL).Err(); err != nil {
		slog.Warn("membership_cache: set failed", "error", err)
	}
}

// Invalidate 删除指定 用户 × 工作区 的缓存条目。成员被移出时调用，让失去访问权限
// 立即生效，而不必等 TTL 自然过期。
func (c *MembershipCache) Invalidate(ctx context.Context, userID, workspaceID string) {
	if c == nil {
		return
	}
	if err := c.rdb.Del(ctx, membershipKey(userID, workspaceID)).Err(); err != nil {
		slog.Warn("membership_cache: invalidate failed", "error", err)
	}
}
