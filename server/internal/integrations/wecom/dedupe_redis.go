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
	// The budgets below are only real if the client applies them to the wire.
	// go-redis does not by default: without ContextTimeoutEnabled a command
	// ignores its context's deadline and waits out the socket timeout instead
	// (baseClient.context), so ClaimBudget would be a number this package
	// states and nothing enforces — and a shutdown drain would overrun the
	// exit budget it promised by however far the socket timeout reaches.
	// Production passes a client built for this (cmd/server: newClaimRedisClient);
	// anything else is a wiring mistake, and a silent one, so it is named here.
	if !rdb.Options().ContextTimeoutEnabled {
		log.Warn("wecom relay: claim store client ignores context deadlines; " +
			"claim budgets and the shutdown drain budget will not be honoured " +
			"(set redis.Options.ContextTimeoutEnabled)")
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

// Every mutation is one Lua operation keyed on the owner token, the same
// shape engine.RedisLeaseStore uses for channel leases: compare and act in the
// server, so a command the client re-sends after a lost response can never
// act on a claim a later owner holds. That is what makes Release safe to
// retry and lets a DEL whose response was lost be answered by the next
// Claim rather than guessed at.
const (
	redisClaimSource = `
local v = redis.call('GET', KEYS[1])
if (not v) then
  redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[2])
  return 1
end
if v == ARGV[1] then
  return 1
end
return 0
`
	redisReleaseSource = `
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('DEL', KEYS[1])
end
return 0
`
	redisSettleSource = `
local v = redis.call('GET', KEYS[1])
if v == ARGV[1] then
  redis.call('SET', KEYS[1], ARGV[2], 'KEEPTTL')
  return 1
end
if v == ARGV[2] then
  return 1
end
return 0
`
	// Resolve reads the state and, for a key still held by a token, fences
	// it as lost in the same operation. Return codes are claimState values.
	//
	// A value that is not token-shaped is a claim from before this scheme —
	// the plain "1" of the SET NX that used to be the whole claim. Its holder
	// recorded its own outcome inline, so it is reported as settled and left
	// alone: fencing it would record a second outcome for a reply that was
	// already counted. Every token carries the "/" tokenFor puts there, so
	// the two are told apart without a version flag or a key migration.
	redisResolveSource = `
local v = redis.call('GET', KEYS[1])
if (not v) then
  return 0
end
if v == ARGV[1] then
  return 2
end
if v == ARGV[2] then
  return 3
end
if not string.find(v, '/', 1, true) then
  return 2
end
redis.call('SET', KEYS[1], ARGV[2], 'KEEPTTL')
return 1
`
	// ClaimState is Resolve WITHOUT the fence: it reads the same key and
	// returns the same claimState codes, but a held claim stays held. That is
	// what an inbox publisher polls while it waits to see whether a replica
	// took the delivery; fencing a holder that is merely mid-delivery would
	// hand the publisher's fallback a second card to send. Return codes are
	// claimState values: 0 absent, 1 held, 2 settled, 3 lost.
	redisClaimStateSource = `
local v = redis.call('GET', KEYS[1])
if (not v) then
  return 0
end
if v == ARGV[1] then
  return 2
end
if v == ARGV[2] then
  return 3
end
if not string.find(v, '/', 1, true) then
  return 2
end
return 1
`
)

var (
	redisClaim      = redis.NewScript(redisClaimSource)
	redisRelease    = redis.NewScript(redisReleaseSource)
	redisSettle     = redis.NewScript(redisSettleSource)
	redisResolve    = redis.NewScript(redisResolveSource)
	redisClaimState = redis.NewScript(redisClaimStateSource)
)

func (d *redisDedupe) Claim(ctx context.Context, key, token string, ttl time.Duration) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, d.budget)
	defer cancel()
	n, err := redisClaim.Run(ctx, d.rdb, []string{key}, token, ttl.Milliseconds()).Int()
	return n == 1, err
}

// bookkeepingBudget bounds one claim-bookkeeping round trip.
//
// Cancellation is dropped on purpose: Release and Settle speak for a frame
// whose delivery has already been decided, and the shutdown that interrupted
// the work must not also erase the record of it.
//
// A DEADLINE is not dropped. A caller that sets one is bounding a whole
// sequence of these calls — drainRemaining gives the entire drain a single
// DrainBudget — so a round trip that helped itself to a fresh budget past that
// point would spend time the shutdown already promised away. The deadline is
// inherited and the store's own budget only ever makes the wait shorter.
//
// Both halves reach the wire only because the client is built to let them:
// see the ContextTimeoutEnabled note in NewRedisDedupe.
func (d *redisDedupe) bookkeepingBudget(ctx context.Context) (context.Context, context.CancelFunc) {
	detached := context.WithoutCancel(ctx)
	own := time.Now().Add(d.budget)
	if deadline, ok := ctx.Deadline(); ok && deadline.Before(own) {
		return context.WithDeadline(detached, deadline)
	}
	return context.WithDeadline(detached, own)
}

// Release is a compare-and-delete on the token. An error means the outcome is
// unknown; the caller reads it as exactly that (RelayOutbound.perform).
func (d *redisDedupe) Release(ctx context.Context, key, token string) (bool, error) {
	ctx, cancel := d.bookkeepingBudget(ctx)
	defer cancel()
	n, err := redisRelease.Run(ctx, d.rdb, []string{key}, token).Int()
	if err != nil {
		d.log.WarnContext(ctx, "wecom relay: claim release outcome unknown",
			"error", err, "key", key)
		return false, err
	}
	return n == 1, nil
}

func (d *redisDedupe) Settle(ctx context.Context, key, token string) (bool, error) {
	ctx, cancel := d.bookkeepingBudget(ctx)
	defer cancel()
	n, err := redisSettle.Run(ctx, d.rdb, []string{key}, token, claimSettledValue).Int()
	return n == 1, err
}

func (d *redisDedupe) Resolve(ctx context.Context, key string) (claimState, error) {
	ctx, cancel := context.WithTimeout(ctx, d.budget)
	defer cancel()
	n, err := redisResolve.Run(ctx, d.rdb, []string{key}, claimSettledValue, claimLostValue).Int()
	if err != nil {
		return claimAbsent, err
	}
	return claimState(n), nil
}

// ClaimState is the read-only half of Resolve: it answers what the key holds
// without fencing a holder. It is a budget-bounded round trip, like Claim;
// its caller polls it inside a window it owns, so it must not outstay one
// call.
func (d *redisDedupe) ClaimState(ctx context.Context, key string) (claimState, error) {
	ctx, cancel := context.WithTimeout(ctx, d.budget)
	defer cancel()
	n, err := redisClaimState.Run(ctx, d.rdb, []string{key}, claimSettledValue, claimLostValue).Int()
	if err != nil {
		return claimAbsent, err
	}
	return claimState(n), nil
}
