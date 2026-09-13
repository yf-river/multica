// stream_retention.go 统一管理 Redis relay 里各种 stream 的「留存/清理」策略：
// stream 太长就裁剪（TrimHorizon/StreamMaxLen），太久没人写就整体过期删除
// （StreamTTL）。分片模式与旧版 relay 共用同一套配置与 TTL 刷新器，保证灰度切换
// 时运营参数口径一致；文件里还包含把 TTL 设/续期频率限制住的 streamTTLRefresher。
package realtime

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// 留存控制的默认值：这些时长都是「够用就好」的工程折中——保留期要覆盖客户端断线
// 重连的最大窗口，又不能大到让冷门 stream 长期占内存。
const (
	defaultRelayStreamTrimHorizon         = 10 * time.Minute // 比保留期短的裁剪视野
	defaultRelayStreamTTL                 = 15 * time.Minute // stream 整体过期时间
	defaultRelayStreamTTLRefreshInterval  = 30 * time.Second // 活跃 stream 续期间隔
	defaultRelayStreamMaintenanceInterval = time.Minute      // 清扫循环的跑动间隔
)

// StreamRetentionConfig 被分片与旧版两种 relay 模式共用，这样「双模式灰度」期间，
// 同一组运维参数在两边含义完全一致。TTL 刻意做成「默认关闭」：先以关闭 TTL 的方式
// 部署兼容代码，等所有副本都能刷新/移除过期后，再打开它。
type StreamRetentionConfig struct {
	StreamMaxLen        int64         // 单条 stream 允许保留的最大消息数
	TrimHorizon         time.Duration // 裁剪：只留最近这段时间内的消息
	StreamTTL           time.Duration // 整条 stream 的过期时长
	TTLRefreshInterval  time.Duration // 发布路径上对活跃 stream 续期的频率上限
	MaintenanceInterval time.Duration // 清扫循环的运行间隔
	StreamTTLEnabled    bool          // 是否启用 stream 级过期
}

// DefaultStreamRetentionConfig 返回跨模式通用的「安全」留存默认值。
func DefaultStreamRetentionConfig() StreamRetentionConfig {
	return StreamRetentionConfig{
		StreamMaxLen:        defaultShardedRelayStreamMaxLen,
		TrimHorizon:         defaultRelayStreamTrimHorizon,
		StreamTTL:           defaultRelayStreamTTL,
		TTLRefreshInterval:  defaultRelayStreamTTLRefreshInterval,
		MaintenanceInterval: defaultRelayStreamMaintenanceInterval,
		StreamTTLEnabled:    false,
	}
}

// withDefaults 用默认值补齐配置里缺失或不合法的项。约束：StreamTTL 至少要长过
// TrimHorizon（否则消息还没到裁剪线 stream 就整个没了）；刷新/清扫间隔必须小于
// StreamTTL 才有意义，非法就按比例从 StreamTTL 推导一个。
func (c StreamRetentionConfig) withDefaults() StreamRetentionConfig {
	def := DefaultStreamRetentionConfig()
	if c.StreamMaxLen <= 0 {
		c.StreamMaxLen = def.StreamMaxLen
	}
	if c.TrimHorizon <= 0 {
		c.TrimHorizon = def.TrimHorizon
	}
	if c.StreamTTL < c.TrimHorizon {
		c.StreamTTL = c.TrimHorizon + defaultShardedRelayReplayGrace
	}
	if c.TTLRefreshInterval <= 0 || c.TTLRefreshInterval >= c.StreamTTL {
		c.TTLRefreshInterval = retentionSubinterval(c.StreamTTL, def.TTLRefreshInterval)
	}
	if c.MaintenanceInterval <= 0 || c.MaintenanceInterval >= c.StreamTTL {
		c.MaintenanceInterval = retentionSubinterval(c.StreamTTL, def.MaintenanceInterval)
	}
	return c
}

// streamTTLRefresher 在发布路径上「限流」PEXPIRE 调用（同一条 stream 在
// refreshEvery 间隔内最多续一次），同时保证活跃 stream 始终带 TTL——这样即使 Redis
// 用了 volatile-* 淘汰策略，也只会淘汰已经闲置的冷 stream。维护清扫会修补那些因为
// 部分失败而漏设的 TTL。
type streamTTLRefresher struct {
	ttl          time.Duration // 续期时长
	refreshEvery time.Duration // 同一条 key 两次续期之间的最小间隔
	now          func() time.Time

	mu          sync.Mutex
	lastRefresh map[string]time.Time // key → 上次续期时间
}

// newStreamTTLRefresher 构造 TTL 刷新器。
func newStreamTTLRefresher(ttl, refreshEvery time.Duration) *streamTTLRefresher {
	return &streamTTLRefresher{
		ttl:          ttl,
		refreshEvery: refreshEvery,
		now:          time.Now,
		lastRefresh:  make(map[string]time.Time),
	}
}

// refreshIfDue 只有当 key 距上次续期超过 refreshEvery 时才真正执行 PExpire，
// 否则什么都不做直接返回 nil。
func (r *streamTTLRefresher) refreshIfDue(ctx context.Context, client *redis.Client, key string) error {
	now := r.now()
	if !r.claimRefresh(key, now) {
		return nil
	}

	ok, err := client.PExpire(ctx, key, r.ttl).Result()
	if err != nil {
		r.releaseRefresh(key, now)
		return err
	}
	// key 在续期前消失了（返回 false 而非错误），把占用让出去再报错。
	if !ok {
		r.releaseRefresh(key, now)
		return fmt.Errorf("stream %q disappeared before TTL refresh", key)
	}
	return nil
}

// repairMissingTTL 只在「stream 存在但没有 TTL」时给它设一个，健康带 TTL 的
// stream 不会被续期——这样闲置的 stream 才能自然到期被清掉。
func (r *streamTTLRefresher) repairMissingTTL(ctx context.Context, client *redis.Client, key string) (time.Duration, error) {
	return r.reconcileTTL(ctx, client, key, true)
}

// reconcileTTL 在「启用 TTL」时修补缺失的过期时间；在「禁用 TTL」时把之前残留的
// 过期时间移除。禁用路径对应兼容/回滚阶段：等所有新副本都观察到（并执行了移除），
// 老二进制继续写入时就不会因为继承了某个过期时间而把活跃 stream 误删。
func (r *streamTTLRefresher) reconcileTTL(ctx context.Context, client *redis.Client, key string, enabled bool) (time.Duration, error) {
	ttl, err := client.PTTL(ctx, key).Result()
	if err != nil {
		return 0, err
	}
	if !enabled {
		r.forget(key)
		// -2 表示 key 不存在、-1 表示存在但没有过期时间，都无需处理。
		if ttl == -2 || ttl == -1 {
			return ttl, nil
		}
		ok, err := client.Persist(ctx, key).Result()
		if err != nil {
			return ttl, err
		}
		if !ok {
			return -2, nil
		}
		return -1, nil
	}
	switch ttl {
	case -2: // key 不存在
		return ttl, nil
	case -1: // key 存在但没有过期时间——补一个
		ok, err := client.PExpire(ctx, key, r.ttl).Result()
		if err != nil {
			return ttl, err
		}
		if !ok {
			return -2, nil
		}
		r.mu.Lock()
		r.lastRefresh[key] = r.now()
		r.mu.Unlock()
		return r.ttl, nil
	default:
		return ttl, nil
	}
}

// forgetStale 丢掉 lastRefresh 里早于 before 的条目（这些 stream 已闲置到该过期了，
// 不再占用内存）。
func (r *streamTTLRefresher) forgetStale(before time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for key, refreshedAt := range r.lastRefresh {
		if refreshedAt.Before(before) {
			delete(r.lastRefresh, key)
		}
	}
}

// forget 移除某个 key 的续期记录（stream 被删时调用）。
func (r *streamTTLRefresher) forget(key string) {
	r.mu.Lock()
	delete(r.lastRefresh, key)
	r.mu.Unlock()
}

// claimRefresh 尝试「抢占」一次对 key 的续期权：距上次续期够久了就记下现在的时间
// 并返回 true（可以真正执行 PExpire），否则返回 false（别人刚续过，跳过）。
func (r *streamTTLRefresher) claimRefresh(key string, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if last, ok := r.lastRefresh[key]; ok && now.Sub(last) < r.refreshEvery {
		return false
	}
	r.lastRefresh[key] = now
	return true
}

// releaseRefresh 只有在自己抢占的那个时间点仍然有效时，才撤销续期占用。比较时间戳
// 是为了避免「A 抢占后失败释放」误删「B 随后成功抢占」的记录。
func (r *streamTTLRefresher) releaseRefresh(key string, claimedAt time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if current, ok := r.lastRefresh[key]; ok && current.Equal(claimedAt) {
		delete(r.lastRefresh, key)
	}
}

// streamMinID 把「距今 horizon 之前」换算成 Redis XTRIM MINID 需要的消息 ID 下界。
// 消息 ID 由毫秒时间戳 + 序号组成，所以裁剪线形如 "<毫秒>-0"。
func streamMinID(now time.Time, horizon time.Duration) string {
	millis := now.Add(-horizon).UnixMilli()
	if millis < 0 {
		millis = 0
	}
	return fmt.Sprintf("%d-0", millis)
}

// redisTTLMillis 把 PTTL 返回的时长换算成毫秒；-2/-1 两个哨兵值原样透传。
func redisTTLMillis(ttl time.Duration) int64 {
	if ttl == -2 || ttl == -1 {
		return int64(ttl)
	}
	return ttl.Milliseconds()
}

// redisInfoInt64 从 redis INFO 输出里按行找 "key:value" 并解析成 int64。
// 找不到对应行返回 ok=false。
func redisInfoInt64(info, key string) (int64, bool) {
	prefix := key + ":"
	for _, line := range strings.Split(info, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		value, err := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(line, prefix)), 10, 64)
		return value, err == nil
	}
	return 0, false
}
