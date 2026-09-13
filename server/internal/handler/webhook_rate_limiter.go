// 本文件实现 webhook 入口的滑动窗口限流器，分内存版与 Redis 版两种后端。
//
// 用途：webhook 是「外部（如 GitHub/GitLab）主动打过来」的入口，一个配置错乱或
// 恶意的发送方能无限轰炸，因此按「token / 来源 IP」分别设了三档预算（默认值见
// DefaultWebhookRateLimit 等）。它是安全网而非正确性要求，所以历史行为里 Redis
// 出错时 webhook 入口保持放行（fail-open）——只有需要「不消费预算地探测」的调用
// （如邀请准入）才走会暴露后端错误的 CheckWithError 并失败关闭。
//
// 实现要点：Redis 版用有序集合（ZSET）存每次请求的时间戳，score 为纳秒时间戳，
// member 是每请求唯一 id（同一纳秒两条请求不能撞成同一个 member，否则 ZADD 原地
// 更新导致少算）。修剪→计数→插入在单条 Lua 脚本里原子完成，杜绝「两个 pod 同时
// 看到 count=limit-1、同时插入」的竞态。
package handler

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// SlidingWindowRateLimit 配置一个按 key 计的滑动窗口限流器。
type SlidingWindowRateLimit struct {
	Limit  int           // 每个窗口的最大请求数；0 表示关闭该闸门
	Window time.Duration // 滑动窗口时长
}

// WebhookRateLimit 保持 webhook 侧 API 稳定：底层滑动窗口实现与其它准入闸门共用。
type WebhookRateLimit = SlidingWindowRateLimit

// DefaultWebhookRateLimit 是粗粒度的按 token 滑动窗口限流器。
//
// 默认值：60 秒内 60 次请求（持续 1 RPS，允许瞬时冲到 60）。目标是「拦住配置错乱或
// 恶意的发送方无休止轰炸」，而不是「把流量塑造成精确预算」——所以实现追求廉价且
// 够用，而非精确。
func DefaultWebhookRateLimit() WebhookRateLimit {
	return WebhookRateLimit{Limit: 60, Window: time.Minute}
}

// DefaultWebhookIPRateLimit 是在触发查找【之前】施加的按 IP 粗预算。刻意设得比按
// token 的预算低：单个 IP 通常不应该持续每分钟送出超过 30 次 webhook 投递；而一个
// 拿随机 token 乱打的恶意 IP，会在能探测 Postgres 之前先撞上这道闸。
func DefaultWebhookIPRateLimit() WebhookRateLimit {
	return WebhookRateLimit{Limit: 30, Window: time.Minute}
}

// DefaultWebhookAbsoluteIPRateLimit 是对每个 webhook 请求、在 token 查找之前施加的
// 高层紧急天花板。正常合法流量应远低于该预算；与那个较低的「坏凭据欠账」限流器
// 不同，它刻意也把成功投递计入。
func DefaultWebhookAbsoluteIPRateLimit() WebhookRateLimit {
	return WebhookRateLimit{Limit: 600, Window: time.Minute}
}

// SlidingWindowRateLimiter 是内存版与 Redis 版限流器共同实现的契约。
//
// 实现方还可能通过下面的可选接口暴露「不消费预算的检查」与重试提示。Allow 会消费
// 一个单位。
type SlidingWindowRateLimiter interface {
	Allow(ctx context.Context, key string) bool
	// 实现方也可以额外满足 slidingWindowRateLimiterInspector。基础契约只保留 Allow，
	// 是为了兼容 billing 与测试里注入的单方法安全闸。
}

// WebhookRateLimiter 保持 webhook 侧契约稳定。
type WebhookRateLimiter = SlidingWindowRateLimiter

// slidingWindowRateLimiterWithError：实现方可选的「消费并暴露后端错误」能力。
type slidingWindowRateLimiterWithError interface {
	AllowWithError(ctx context.Context, key string) (bool, error)
}

// slidingWindowRateLimiterInspector：实现方可选的「不消费预算的检查 + 重试提示」能力。
type slidingWindowRateLimiterInspector interface {
	Check(ctx context.Context, key string) bool
	RetryAfter(ctx context.Context, key string) time.Duration
}

// slidingWindowRateLimiterCheckWithError：实现方可选的「检查并暴露后端错误」能力。
type slidingWindowRateLimiterCheckWithError interface {
	CheckWithError(ctx context.Context, key string) (bool, error)
}

// slidingWindowLimiterCheck 让 webhook 入口在可选的后端检查不可用时保持放行
// （fail-open）。
func slidingWindowLimiterCheck(ctx context.Context, limiter SlidingWindowRateLimiter, key string) bool {
	allowed, err := slidingWindowLimiterCheckWithError(ctx, limiter, key)
	return err != nil || allowed
}

// slidingWindowLimiterCheckWithError 把检查失败暴露给必须失败关闭且不消费预算的
// 调用方（例如邀请准入）。
func slidingWindowLimiterCheckWithError(ctx context.Context, limiter SlidingWindowRateLimiter, key string) (bool, error) {
	if limiter == nil {
		return true, nil
	}
	if withError, ok := limiter.(slidingWindowRateLimiterCheckWithError); ok {
		return withError.CheckWithError(ctx, key)
	}
	if inspector, ok := limiter.(slidingWindowRateLimiterInspector); ok {
		return inspector.Check(ctx, key), nil
	}
	return true, nil
}

// slidingWindowLimiterRetryAfter 取该 key 的建议重试等待时长；拿不到精确值时保守
// 返回一分钟。
func slidingWindowLimiterRetryAfter(ctx context.Context, limiter SlidingWindowRateLimiter, key string) time.Duration {
	if inspector, ok := limiter.(slidingWindowRateLimiterInspector); ok {
		return inspector.RetryAfter(ctx, key)
	}
	return time.Minute
}

// slidingWindowLimiterAllow 把后端错误暴露给不能安全放行的调用方。旧的 Allow 方法
// 刻意让 webhook 入口在 Redis 出错时保持放行（fail-open）。
func slidingWindowLimiterAllow(ctx context.Context, limiter SlidingWindowRateLimiter, key string) (bool, error) {
	if limiter == nil {
		return true, nil
	}
	if withError, ok := limiter.(slidingWindowRateLimiterWithError); ok {
		return withError.AllowWithError(ctx, key)
	}
	return limiter.Allow(ctx, key), nil
}

// ── 内存实现 ────────────────────────────────────────────────────────────────

// memoryWebhookRateLimiter 把每个 key 的时间戳存成切片，每次调用时修剪过期项。
// 单节点开发/测试够用；多节点生产部署应改用 Redis 版，让配额在 pod 间共享。
type memoryWebhookRateLimiter struct {
	cfg       SlidingWindowRateLimit
	mu        sync.Mutex
	hit       map[string][]time.Time
	lastSweep time.Time
}

// newMemorySlidingWindowRateLimiter 构造内存版限流器（内部通用入口）。
func newMemorySlidingWindowRateLimiter(cfg SlidingWindowRateLimit) SlidingWindowRateLimiter {
	return &memoryWebhookRateLimiter{cfg: cfg, hit: make(map[string][]time.Time)}
}

// NewMemoryWebhookRateLimiter 是内存版按 token 限流器的对外构造函数。
func NewMemoryWebhookRateLimiter(cfg WebhookRateLimit) WebhookRateLimiter {
	return newMemorySlidingWindowRateLimiter(cfg)
}

func (l *memoryWebhookRateLimiter) Allow(ctx context.Context, key string) bool {
	allowed, _ := l.AllowWithError(ctx, key)
	return allowed
}

func (l *memoryWebhookRateLimiter) AllowWithError(_ context.Context, key string) (bool, error) {
	return l.evaluate(key, true), nil
}

func (l *memoryWebhookRateLimiter) Check(ctx context.Context, key string) bool {
	allowed, _ := l.CheckWithError(ctx, key)
	return allowed
}

func (l *memoryWebhookRateLimiter) CheckWithError(_ context.Context, key string) (bool, error) {
	return l.evaluate(key, false), nil
}

// evaluate 是内存版核心：窗口内计数达到上限则拒绝；consume=true 时把本次请求时间
// 计入（Allow），否则只查不记（Check）。
func (l *memoryWebhookRateLimiter) evaluate(key string, consume bool) bool {
	if l.cfg.Limit <= 0 {
		return true
	}
	now := time.Now()
	cutoff := now.Add(-l.cfg.Window)

	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweepExpired(now, cutoff)

	hits := l.hit[key]
	// 修剪已滑出窗口的条目。
	keep := hits[:0]
	for _, t := range hits {
		if t.After(cutoff) {
			keep = append(keep, t)
		}
	}
	if len(keep) >= l.cfg.Limit {
		l.hit[key] = keep
		return false
	}
	if consume {
		keep = append(keep, now)
	}
	l.hit[key] = keep
	return true
}

// sweepExpired 周期性清掉整把已失效的 key，防止 map 无限膨胀。清扫间隔默认取窗口
// 时长（上限一分钟），避免每次 Allow 都全表扫。
func (l *memoryWebhookRateLimiter) sweepExpired(now, cutoff time.Time) {
	sweepInterval := l.cfg.Window
	if sweepInterval <= 0 || sweepInterval > time.Minute {
		sweepInterval = time.Minute
	}
	if !l.lastSweep.IsZero() && now.Sub(l.lastSweep) < sweepInterval {
		return
	}
	for key, hits := range l.hit {
		if len(hits) == 0 || !hits[len(hits)-1].After(cutoff) {
			delete(l.hit, key)
		}
	}
	l.lastSweep = now
}

// RetryAfter 估算该 key 何时能再次放行：取窗口内最早一次命中 + 窗口时长，近似为
// 需要等待的时间。
func (l *memoryWebhookRateLimiter) RetryAfter(_ context.Context, key string) time.Duration {
	if l.cfg.Limit <= 0 {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	hits := l.hit[key]
	if len(hits) == 0 {
		return l.cfg.Window
	}
	retry := time.Until(hits[0].Add(l.cfg.Window))
	if retry <= 0 {
		return time.Second
	}
	return retry
}

// ── Redis 实现 ───────────────────────────────────────────────────────────────

// 三个 key 前缀让不同档位的预算互不干扰。
const (
	webhookLimiterKeyPrefix           = "mul:webhook:rate:"
	webhookIPLimiterKeyPrefix         = "mul:webhook:ip:"
	webhookAbsoluteIPLimiterKeyPrefix = "mul:webhook:absolute-ip:"
)

// webhookLimiterAllowSrc 在 Redis 上原子执行滑动窗口检查（Allow 语义）：
//
//	KEYS[1] = ZSET key
//	ARGV[1] = now（unix 纳秒字符串，作为条目 score）
//	ARGV[2] = cutoff（unix 纳秒字符串）
//	ARGV[3] = limit
//	ARGV[4] = 过期秒数（刷新 TTL，比窗口大）
//	ARGV[5] = 本次请求的唯一 member id
//
// 返回 1 表示放行，0 表示应拒绝。
//
// 先修剪、再计数、最后按需插入——三步放进同一条 Lua 调用，避免经典竞态：「两个
// pod 同时看到 count=limit-1、又同时插入」。
const webhookLimiterAllowSrc = `
local key = KEYS[1]
local now = tonumber(ARGV[1])
local cutoff = tonumber(ARGV[2])
local limit = tonumber(ARGV[3])
local ttl = tonumber(ARGV[4])
local member = ARGV[5]
redis.call('ZREMRANGEBYSCORE', key, '-inf', cutoff)
local count = redis.call('ZCARD', key)
if count >= limit then
    return 0
end
redis.call('ZADD', key, now, member)
redis.call('EXPIRE', key, ttl)
return 1
`

var webhookLimiterAllowScript = redis.NewScript(webhookLimiterAllowSrc)

// webhookLimiterCheckSrc 与 Allow 脚本同构，只是不插入新条目（不消费预算）。
const webhookLimiterCheckSrc = `
local key = KEYS[1]
local cutoff = tonumber(ARGV[1])
local limit = tonumber(ARGV[2])
redis.call('ZREMRANGEBYSCORE', key, '-inf', cutoff)
local count = redis.call('ZCARD', key)
if count >= limit then
    return 0
end
return 1
`

var webhookLimiterCheckScript = redis.NewScript(webhookLimiterCheckSrc)

// webhookLimiterAllowSource 把脚本体暴露给想断言结构不变量（例如修剪在计数前、
// 插入在最后）又不愿起真实 Redis 的测试。故意用小写开头的 "Source" 后缀点明
// 测试专用意图。
func webhookLimiterAllowSource() string { return webhookLimiterAllowSrc }

// redisWebhookRateLimiter 是 Redis 后端的滑动窗口限流器。
type redisWebhookRateLimiter struct {
	cfg       SlidingWindowRateLimit
	rdb       *redis.Client
	keyPrefix string
}

// newRedisSlidingWindowRateLimiter 构造 Redis 版限流器（内部通用入口）。
func newRedisSlidingWindowRateLimiter(rdb *redis.Client, cfg SlidingWindowRateLimit, keyPrefix string) SlidingWindowRateLimiter {
	return &redisWebhookRateLimiter{cfg: cfg, rdb: rdb, keyPrefix: keyPrefix}
}

// NewRedisWebhookRateLimiter 构造按 token 的 Redis 限流器。
func NewRedisWebhookRateLimiter(rdb *redis.Client, cfg WebhookRateLimit) WebhookRateLimiter {
	return newRedisSlidingWindowRateLimiter(rdb, cfg, webhookLimiterKeyPrefix)
}

// NewRedisWebhookIPRateLimiter 是按 IP 的变体：同一份 Lua 脚本，换一个 key 命名空间，
// 让两档预算互不干扰。
func NewRedisWebhookIPRateLimiter(rdb *redis.Client, cfg WebhookRateLimit) WebhookRateLimiter {
	return newRedisSlidingWindowRateLimiter(rdb, cfg, webhookIPLimiterKeyPrefix)
}

// NewRedisWebhookAbsoluteIPRateLimiter 构造高层紧急天花板（按 IP）的 Redis 限流器。
func NewRedisWebhookAbsoluteIPRateLimiter(rdb *redis.Client, cfg WebhookRateLimit) WebhookRateLimiter {
	return newRedisSlidingWindowRateLimiter(rdb, cfg, webhookAbsoluteIPLimiterKeyPrefix)
}

// NewMemoryWebhookIPRateLimiter 是未配置 Redis 时的内存版按 IP 变体。语义与按 token
// 的内存限流器相同——仅单节点可用。
func NewMemoryWebhookIPRateLimiter(cfg WebhookRateLimit) WebhookRateLimiter {
	return NewMemoryWebhookRateLimiter(cfg)
}

// NewMemoryWebhookAbsoluteIPRateLimiter 是未配置 Redis 时的高层天花板内存版变体。
func NewMemoryWebhookAbsoluteIPRateLimiter(cfg WebhookRateLimit) WebhookRateLimiter {
	return NewMemoryWebhookRateLimiter(cfg)
}

func (l *redisWebhookRateLimiter) Allow(ctx context.Context, key string) bool {
	allowed, err := l.AllowWithError(ctx, key)
	if err != nil {
		// webhook 入口保持历史放行（fail-open）行为：这个限流器是安全网，不是
		// 正确性要求。
		return true
	}
	return allowed
}

func (l *redisWebhookRateLimiter) AllowWithError(ctx context.Context, key string) (bool, error) {
	if l.cfg.Limit <= 0 || l.rdb == nil {
		return true, nil
	}
	nowTime := time.Now()
	now := nowTime.UnixNano()
	cutoff := nowTime.Add(-l.cfg.Window).UnixNano()
	ttlSeconds := int64(l.cfg.Window/time.Second) * 2
	if ttlSeconds < 1 {
		ttlSeconds = 1
	}
	prefix := l.keyPrefix
	if prefix == "" {
		prefix = webhookLimiterKeyPrefix
	}
	// 每个请求一个唯一 member：score 携带时间戳供滑动窗口修剪，但同一纳秒的两条
	// 请求不能坍缩成同一个 ZSET member，否则窗口会少算。
	member := uuid.NewString()
	res, err := webhookLimiterAllowScript.Run(
		ctx,
		l.rdb,
		[]string{prefix + key},
		now, cutoff, l.cfg.Limit, ttlSeconds, member,
	).Int()
	if err != nil {
		return false, err
	}
	return res == 1, nil
}

func (l *redisWebhookRateLimiter) Check(ctx context.Context, key string) bool {
	allowed, err := l.CheckWithError(ctx, key)
	if err != nil {
		return true
	}
	return allowed
}

func (l *redisWebhookRateLimiter) CheckWithError(ctx context.Context, key string) (bool, error) {
	if l.cfg.Limit <= 0 || l.rdb == nil {
		return true, nil
	}
	cutoff := time.Now().Add(-l.cfg.Window).UnixNano()
	prefix := l.keyPrefix
	if prefix == "" {
		prefix = webhookLimiterKeyPrefix
	}
	res, err := webhookLimiterCheckScript.Run(
		ctx,
		l.rdb,
		[]string{prefix + key},
		cutoff, l.cfg.Limit,
	).Int()
	if err != nil {
		return false, err
	}
	return res == 1, nil
}

func (l *redisWebhookRateLimiter) RetryAfter(_ context.Context, _ string) time.Duration {
	if l.cfg.Window <= 0 {
		return time.Second
	}
	// 直接回整个窗口偏保守，且在副本之间也始终正确——即使最老的 ZSET member 在
	// 拒绝与本次响应之间变了也没关系。
	return l.cfg.Window
}
