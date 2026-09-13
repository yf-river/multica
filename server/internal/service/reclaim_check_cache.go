// reclaim_check_cache.go —— 认领（reclaim）检查的 Redis 缓存：
// 批量/单次认领的热路径在做“过期派发 UPDATE”之前，先问这个缓存“现在有没有任务
// 可能已可被回收”；缓存说没有就省掉那趟数据库查询。缓存只负责削减负载，永远不会
// 对任务恢复拍板——键缺失或 Redis 出错一律当作“去查 PostgreSQL”。
package service

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	reclaimCheckSchedulePrefix = "mul:claim:runtime:reclaim-schedule:"
	reclaimCheckBackstopPrefix = "mul:claim:runtime:reclaim-backstop:"
	reclaimCheckRetryBatchSize = 256

	// 兜底间隔保持在 PostgreSQL 恢复窗口之内（或等于它），这样派发侧漏写一次
	// Redis 也不至于把恢复拖过一个恢复窗口。
	ReclaimCheckBackstopInterval = claimResponseRecoveryWindow
	// 已到期的提示如果没收回任何行，就在 daemon 下一次正常轮询节奏里重试，而不是
	// 一直躺到兜底时刻。
	ReclaimCheckRetryInterval = 30 * time.Second
	// Redis 提示在 PostgreSQL 派发命令之前的瞬间写入。留一点余量给那条命令去提交，
	// 免得提示刚好赶在数据库“严格恢复窗口”判定之前触发。
	ReclaimCheckHintSafetyMargin = 5 * time.Second
	// 新任务的第一个提示定在“一个恢复窗口 + 安全余量”之后。让所在的 ZSET 再多活
	// 一个完整窗口，确保提示扛得过自己的截止时间和若干次有界重试。
	ReclaimCheckScheduleTTL = 2*claimResponseRecoveryWindow + ReclaimCheckHintSafetyMargin
)

// 下面的 Lua 脚本只用 Redis 2.6 时代的原语。尤其避免用 ZADD GT（Redis 6.2+），
// 这样自托管的部署在较老的 Redis 上也不会悄悄丢掉“延后调度”这个优化。
const reclaimCheckTrackLaterScript = `
local current = redis.call('ZSCORE', KEYS[1], ARGV[1])
if not current then
    return 0
end
if tonumber(current) < tonumber(ARGV[2]) then
    redis.call('ZADD', KEYS[1], ARGV[2], ARGV[1])
end
redis.call('PEXPIRE', KEYS[1], ARGV[3])
return 1
`

const reclaimCheckDeferDueScript = `
local members = redis.call('ZRANGEBYSCORE', KEYS[1], '-inf', ARGV[1], 'LIMIT', '0', ARGV[3])
for _, member in ipairs(members) do
    redis.call('ZADD', KEYS[1], ARGV[2], member)
end
return #members
`

// ReclaimCheckCache 让批量/单次认领的热路径在“确定还没有任务可被回收”时，免掉那
// 趟注定无果的“过期派发 UPDATE”。
//
// 每个 runtime 各持一个有序集合（sorted set），元素是任务 ID，分值是该任务“已知
// 最早可回收时刻”。另有一个“上次已检查”时间戳，强制周期性回库检查一遍，兜住缓存
// 丢失、部署前的旧行或漏写。任务分值和“上次已检查”都是应用侧时钟的值；真正查库
// 时仍以 PostgreSQL 为准判定是否够格。键缺失和 Redis 出错一律表示“现在就去查库”，
// 所以这个缓存只能减少负载，永远不能替任务恢复做决定。
type ReclaimCheckCache struct {
	rdb *redis.Client
}

// NewReclaimCheckCache 包一层 Redis 客户端；rdb 为 nil 时返回 nil（调用方据此走
// “没有缓存”的降级路径）。
func NewReclaimCheckCache(rdb *redis.Client) *ReclaimCheckCache {
	if rdb == nil {
		return nil
	}
	return &ReclaimCheckCache{rdb: rdb}
}

func reclaimCheckScheduleKey(runtimeID string) string {
	return reclaimCheckSchedulePrefix + runtimeID
}

func reclaimCheckBackstopKey(runtimeID string) string {
	return reclaimCheckBackstopPrefix + runtimeID
}

// bounded 给单条 Redis 命令加上统一超时，防止慢 Redis 拖住认领热路径。
func (c *ReclaimCheckCache) bounded(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, emptyClaimRedisTimeout)
}

// DueRuntimeIDs 只返回“有到期任务提示、或周期性兜底已到”的 runtime。它保留输入中
// 非空 ID 的相对顺序。MarkChecked 会把“已被一趟完整查库看过的提示”推进到一个有界
// 重试截止时间，所以一个没被收回的任务会被再试一次，而不会每次轮询都重新触发。
// 计划 ZSET 的 TTL 仍是过期清理提示的硬性上限。
//
// 缓存为 nil、兜底键缺失/损坏、或 Redis 出错时都“放行”（fail open）：批量调用方
// 把任一 runtime 到期当作“把完整 runtime 集合查一遍”的理由。
func (c *ReclaimCheckCache) DueRuntimeIDs(ctx context.Context, runtimeIDs []string, now time.Time) []string {
	if len(runtimeIDs) == 0 {
		return nil
	}
	validRuntimeIDs := make([]string, 0, len(runtimeIDs))
	for _, runtimeID := range runtimeIDs {
		if runtimeID == "" {
			continue
		}
		validRuntimeIDs = append(validRuntimeIDs, runtimeID)
	}
	if len(validRuntimeIDs) == 0 {
		return nil
	}
	if c == nil {
		return validRuntimeIDs
	}

	bctx, cancel := c.bounded(ctx)
	defer cancel()
	backstopKeys := make([]string, 0, len(validRuntimeIDs))
	for _, runtimeID := range validRuntimeIDs {
		backstopKeys = append(backstopKeys, reclaimCheckBackstopKey(runtimeID))
	}
	backstops, err := c.rdb.MGet(bctx, backstopKeys...).Result()
	if err != nil {
		slog.Warn("reclaim_check_cache: backstop read failed; falling back to DB", "error", err)
		return validRuntimeIDs
	}
	if len(backstops) != len(validRuntimeIDs) {
		slog.Warn("reclaim_check_cache: incomplete backstop read; falling back to DB")
		return validRuntimeIDs
	}

	nowMillis := now.UnixMilli()
	type scheduleCommand struct {
		index int
		count *redis.IntCmd
	}
	due := make([]bool, len(validRuntimeIDs))
	pipe := c.rdb.Pipeline()
	scheduleCommands := make([]scheduleCommand, 0, len(validRuntimeIDs))
	for i, runtimeID := range validRuntimeIDs {
		backstop, ok := backstops[i].(string)
		if !ok {
			// 值缺失或不是字符串，说明这个 runtime 没有可信的“已成功检查”标记。
			due[i] = true
			continue
		}
		checkedMillis, err := strconv.ParseInt(backstop, 10, 64)
		if err != nil {
			due[i] = true
			continue
		}
		nextBackstop := checkedMillis + ReclaimCheckBackstopInterval.Milliseconds()
		// 加法溢出或兜底时刻已到：都算到期。
		if nextBackstop < checkedMillis || nextBackstop <= nowMillis {
			due[i] = true
			continue
		}
		scheduleCommands = append(scheduleCommands, scheduleCommand{
			index: i,
			count: pipe.ZCount(
				bctx,
				reclaimCheckScheduleKey(runtimeID),
				"-inf",
				strconv.FormatInt(nowMillis, 10),
			),
		})
	}
	if len(scheduleCommands) > 0 {
		if _, err := pipe.Exec(bctx); err != nil {
			slog.Warn("reclaim_check_cache: schedule read failed; falling back to DB", "error", err)
			return validRuntimeIDs
		}
		for _, command := range scheduleCommands {
			count, err := command.count.Result()
			if err != nil {
				slog.Warn("reclaim_check_cache: schedule result failed; falling back to DB", "error", err)
				return validRuntimeIDs
			}
			if count > 0 {
				due[command.index] = true
			}
		}
	}
	dueRuntimeIDs := make([]string, 0, len(validRuntimeIDs))
	for i, runtimeID := range validRuntimeIDs {
		if due[i] {
			dueRuntimeIDs = append(dueRuntimeIDs, runtimeID)
		}
	}
	return dueRuntimeIDs
}

// Track 记录某个已派发任务“已知最早可回收时刻”。一次新的派发/回收后，ZADD 会用
// 新分值覆盖该任务；同一 runtime 上的多个任务互不影响，不会因为只记一个
// runtime 级时间戳而丢掉第二个任务更早的恢复截止时间。
func (c *ReclaimCheckCache) Track(ctx context.Context, runtimeID, taskID string, checkAfter time.Time) {
	c.track(ctx, runtimeID, taskID, checkAfter, false)
}

// TrackLater 只在“prepare 租约续期”把任务保护到超过其当前恢复截止时间时，才把
// 已有提示往后推。它刻意不创建缺失成员：若没有最初的派发截止时间，一次短暂的租约
// 可能排出一趟过早的失败检查，把恢复拖到下一个兜底。缺失状态本来就由那个有界兜底
// 放行处理。
func (c *ReclaimCheckCache) TrackLater(ctx context.Context, runtimeID, taskID string, checkAfter time.Time) {
	c.track(ctx, runtimeID, taskID, checkAfter, true)
}

func (c *ReclaimCheckCache) track(ctx context.Context, runtimeID, taskID string, checkAfter time.Time, onlyLater bool) {
	if c == nil || runtimeID == "" || taskID == "" || checkAfter.IsZero() {
		return
	}
	bctx, cancel := c.bounded(ctx)
	defer cancel()
	key := reclaimCheckScheduleKey(runtimeID)
	if onlyLater {
		// 只有当前分值更早时才 ZADD 到新时刻（Lua 内判断），并顺带刷新 TTL。
		if err := c.rdb.Eval(
			bctx,
			reclaimCheckTrackLaterScript,
			[]string{key},
			taskID,
			checkAfter.UnixMilli(),
			ReclaimCheckScheduleTTL.Milliseconds(),
		).Err(); err != nil {
			slog.Warn("reclaim_check_cache: extend hint failed; DB fallback remains active", "error", err)
		}
		return
	}
	// 普通 Track：无条件写入分值 + 刷新 TTL。
	pipe := c.rdb.Pipeline()
	member := redis.Z{Score: float64(checkAfter.UnixMilli()), Member: taskID}
	pipe.ZAdd(bctx, key, member)
	pipe.Expire(bctx, key, ReclaimCheckScheduleTTL)
	if _, err := pipe.Exec(bctx); err != nil {
		slog.Warn("reclaim_check_cache: track failed; DB fallback remains active", "error", err)
	}
}

// Forget 移除一个“不可能再被回收”的任务（已启动、已重新排队、在等本地目录、或已
// 终结）。失败无妨：过期提示最多引发一次多余的查库，绝不可能让一个不够格的任务跑
// 起来。
func (c *ReclaimCheckCache) Forget(ctx context.Context, runtimeID, taskID string) {
	if c == nil || runtimeID == "" || taskID == "" {
		return
	}
	bctx, cancel := c.bounded(ctx)
	defer cancel()
	if err := c.rdb.ZRem(bctx, reclaimCheckScheduleKey(runtimeID), taskID).Err(); err != nil {
		slog.Warn("reclaim_check_cache: forget failed; stale hint will expire", "error", err)
	}
}

// MarkChecked 记录“对机器级轮询集合里的每个 runtime，都完成了一趟成功的 PostgreSQL
// 回收检查”。真正触发这趟检查的提示被原子地移到 retryAfter，而不是删除或置为无效：
// SKIP LOCKED 和 runtime 健康谓词意味着“查不到结果”并不能证明那些任务不再需要
// 回收。比 checkedThrough 更新的并发提示保持原样。有界批量防止病态 ZSET 独占 Redis；
// 溢出的到期提示保持到期状态，放行到另一趟查库。
func (c *ReclaimCheckCache) MarkChecked(ctx context.Context, runtimeIDs []string, checkedThrough, retryAfter time.Time) {
	if c == nil || len(runtimeIDs) == 0 {
		return
	}
	if retryAfter.IsZero() || !retryAfter.After(checkedThrough) {
		retryAfter = checkedThrough.Add(ReclaimCheckRetryInterval)
	}
	bctx, cancel := c.bounded(ctx)
	defer cancel()
	checkedMillis := strconv.FormatInt(checkedThrough.UnixMilli(), 10)
	retryMillis := strconv.FormatInt(retryAfter.UnixMilli(), 10)
	pipe := c.rdb.Pipeline()
	for _, runtimeID := range runtimeIDs {
		if runtimeID == "" {
			continue
		}
		pipe.Eval(
			bctx,
			reclaimCheckDeferDueScript,
			[]string{reclaimCheckScheduleKey(runtimeID)},
			checkedMillis,
			retryMillis,
			reclaimCheckRetryBatchSize,
		)
		pipe.Set(bctx, reclaimCheckBackstopKey(runtimeID), checkedMillis, ReclaimCheckBackstopInterval)
	}
	if _, err := pipe.Exec(bctx); err != nil {
		slog.Warn("reclaim_check_cache: mark checked failed; falling back to DB", "error", err)
	}
}
