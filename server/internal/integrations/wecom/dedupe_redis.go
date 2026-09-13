// dedupe_redis.go —— 一条投递经过路由后，用它来保证「至多投一次」（claim）。
//
// 它跑在中继（relay）自身所依赖的同一份 Redis 上，这正是它没有引入新依赖的
// 全部原因：没有 Redis 就没有中继，也就没有跨副本路由，自然没有需要去重的
// 东西。从未需要 Redis 的部署，现在也不需要为它额外引入。
//
// 核心是「SET NX PX」这个原子占位：每个 turn 一把 key，key 用流条目（stream
// entry）携带的同一个事件 id，这样重启后重放的帧、租约切换期间被两个副本各
// 读了一帧——都会撞上同一把 key，从而只生效一次。
package wecom

import (
	"context"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

// redisDedupe 是一种以「每条投递一个 Redis key」为底层的 DedupeStore 实现。
type redisDedupe struct {
	rdb    *redis.Client
	log    *slog.Logger
	budget time.Duration
}

// NewRedisDedupe 构造生产环境用的占位存储。传入 nil client 会返回 nil——
// RelayOutbound 把 nil 理解为「没有跨重启占位能力」；这只在连中继都不存在的
// 情况下才正确。budget 传 0 会取 defaultClaimBudget；测试会把它调小，好让从
// budget 推导出的 outcome 宽限期只值一场测试的时间。
func NewRedisDedupe(rdb *redis.Client, budget time.Duration, log *slog.Logger) DedupeStore {
	if rdb == nil {
		return nil
	}
	if log == nil {
		log = slog.Default()
	}
	if budget <= 0 {
		budget = defaultClaimBudget
	}
	return &redisDedupe{rdb: rdb, log: log, budget: budget}
}

// defaultClaimBudget 给单次往返占位设了上限。分发 worker 等在 Redis 上就是
// 一个 worker 在空转，而帧是可以重放的，所以快速失败、让重放把它捞回来，
// 好过一直占着队列。
const defaultClaimBudget = 2 * time.Second

// ClaimBudget 上报该存储实际生效的上限，RelayOutbound 用它在估算
// outcomeGrace 的大小。
func (d *redisDedupe) ClaimBudget() time.Duration { return d.budget }

// Claim 尝试占下 key（SETNX）。返回 true 表示这次占位成功、投递继续走；
// false 表示已有别的副本在投（或刚投完），这次应放弃。
func (d *redisDedupe) Claim(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, d.budget)
	defer cancel()
	return d.rdb.SetNX(ctx, key, "1", ttl).Result()
}

// Held 报告某个占位是否仍被持有：只做一次 EXISTS 判断，并套用和占位相同的
// 有界超时。调用方正在决定要不要记录一条「回复丢失」，此时 Redis 答不上来
// 绝不能反过来成为「丢了的证据」。
func (d *redisDedupe) Held(ctx context.Context, key string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, d.budget)
	defer cancel()
	n, err := d.rdb.Exists(ctx, key).Result()
	return n > 0, err
}

// Release 为一次「确实没有发生」的投递释放占位。刻意做成尽力而为：Release
// 失败最多让 key 自己到期，代价是重放窗口里少重试一次投递，而不是在别人的
// 会话里出现一条重复消息。
func (d *redisDedupe) Release(ctx context.Context, key string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), d.budget)
	defer cancel()
	if err := d.rdb.Del(ctx, key).Err(); err != nil {
		d.log.WarnContext(ctx, "wecom relay: could not release a delivery claim",
			"error", err, "key", key)
	}
}
