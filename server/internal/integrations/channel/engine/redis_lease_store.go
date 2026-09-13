package engine

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/redis/go-redis/v9"
)

// 角色定位：把"渠道 WS 租约"实现在专门的 Redis 客户端/连接池上——跨副本"每个安装至多
// 一条活跃连接"的分布式互斥。每个变更都是一条 Lua 原子操作，因此"比较 token + 更新/
// 删除过期时间"不会被另一个副本交错执行。
//
// 三条 Lua 脚本的公共语义：租约的键是安装 id，值是持有者的随机 token。只有"当前无租约
// 或持有者就是自己（token 匹配）"时才允许 acquire/renew/release，过期值永远不可能被
// 别人续走或误删——这就是"token 围栏（fencing）"。

const redisLeaseKeyPrefix = "multica:channel-lease:v1:"

// leaseNamespacePattern 限制 namespace 只能由安全的键字符组成，防止把环境变量拼成
// Redis 键时引入分隔符歧义。
var leaseNamespacePattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// redisTryAcquireLeaseSource：键不存在、或值等于本持有者 token（幂等重入）时写新值并
// 设 PX 过期毫秒，返回 1；被别人占着返回 0。
const redisTryAcquireLeaseSource = `
local current = redis.call('GET', KEYS[1])
if (not current) or current == ARGV[1] then
  redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[2])
  return 1
end
return 0
`

// redisRenewLeaseSource：只有值等于本持有者 token 才续期（PEXPIRE），否则返回 0。
const redisRenewLeaseSource = `
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('PEXPIRE', KEYS[1], ARGV[2])
end
return 0
`

// redisReleaseLeaseSource：只有值等于本持有者 token 才删除键（不会误删新持有者的租约）。
const redisReleaseLeaseSource = `
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('DEL', KEYS[1])
end
return 0
`

var redisTryAcquireLease = redis.NewScript(redisTryAcquireLeaseSource)
var redisRenewLease = redis.NewScript(redisRenewLeaseSource)
var redisReleaseLease = redis.NewScript(redisReleaseLeaseSource)

// RedisLeaseStore 在专用 Redis 客户端/连接池上实现 token 围栏式的渠道租约。
type RedisLeaseStore struct {
	client    *redis.Client
	namespace string // 租约键的命名空间段，隔离不同部署/环境
}

// NewRedisLeaseStore 校验 client 非空与 namespace 合法（只允许 [A-Za-z0-9._-]+）。
func NewRedisLeaseStore(client *redis.Client, namespace string) (*RedisLeaseStore, error) {
	if client == nil {
		return nil, errors.New("channel lease redis client is nil")
	}
	if !leaseNamespacePattern.MatchString(namespace) {
		return nil, errors.New("CHANNEL_WS_LEASE_NAMESPACE must match [A-Za-z0-9._-]+")
	}
	return &RedisLeaseStore{client: client, namespace: namespace}, nil
}

// Ready 验证连通性并把三条租约脚本全部预载。Redis-backed 的启动是"失败即关闭"：
// 这一步失败的话，调用方绝不能启动 Supervisor（否则会出现无租约保护的并发连接）。
func (s *RedisLeaseStore) Ready(ctx context.Context) error {
	if err := s.client.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("ping: %w", err)
	}
	for _, item := range []struct {
		name   string
		script *redis.Script
	}{
		{name: "try_acquire", script: redisTryAcquireLease},
		{name: "renew", script: redisRenewLease},
		{name: "release", script: redisReleaseLease},
	} {
		if err := item.script.Load(ctx, s.client).Err(); err != nil {
			return fmt.Errorf("load %s script: %w", item.name, err)
		}
	}
	// 端到端演练一遍三条脚本（用一次性键与只在本进程存在的 token），确保不是"能 Ping 通
	// 但脚本没跑对"。nodeID 拼进键里避免多副本同时演练互相踩键。
	readyKey := redisLeaseKeyPrefix + s.namespace + ":__ready__:" + newNodeID()
	const readyToken = "readiness-check"
	for _, item := range []struct {
		name   string
		script *redis.Script
		args   []any
	}{
		{name: "try_acquire", script: redisTryAcquireLease, args: []any{readyToken, int64(5000)}},
		{name: "renew", script: redisRenewLease, args: []any{readyToken, int64(5000)}},
		{name: "release", script: redisReleaseLease, args: []any{readyToken}},
	} {
		result, err := item.script.Run(ctx, s.client, []string{readyKey}, item.args...).Int64()
		if err != nil {
			return fmt.Errorf("execute %s script: %w", item.name, err)
		}
		if result != 1 {
			return fmt.Errorf("execute %s script: unexpected result %d", item.name, result)
		}
	}
	return nil
}

// ListHeldWSLeases 一次 MGet 查出这批安装里哪些**确实持有**租约（键存在即持有，不看值）。
func (s *RedisLeaseStore) ListHeldWSLeases(ctx context.Context, ids []pgtype.UUID) (map[string]struct{}, error) {
	held := make(map[string]struct{}, len(ids))
	if len(ids) == 0 {
		return held, nil
	}
	keys := make([]string, len(ids))
	for i, id := range ids {
		keys[i] = s.key(id)
	}
	// RedisLeaseStore 刻意用 *redis.Client（standalone/sentinel）而非 ClusterClient：
	// 若未来上集群模式，必须按 hash slot 分组或逐个 GET——跨槽 MGET 是非法操作。
	values, err := s.client.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, err
	}
	for i, value := range values {
		if value != nil {
			held[uuidString(ids[i])] = struct{}{}
		}
	}
	return held, nil
}

func (s *RedisLeaseStore) TryAcquireWSLease(ctx context.Context, arg AcquireLeaseParams) error {
	return s.runLeaseScript(ctx, redisTryAcquireLease, arg)
}

func (s *RedisLeaseStore) RenewWSLease(ctx context.Context, arg AcquireLeaseParams) error {
	return s.runLeaseScript(ctx, redisRenewLease, arg)
}

// ReleaseWSLease 释放租约。token 过期的调用（返回 0）是有意为之的围栏空操作：说明租约
// 已不在自己手上，什么都不用做。
func (s *RedisLeaseStore) ReleaseWSLease(ctx context.Context, arg ReleaseLeaseParams) error {
	_, err := redisReleaseLease.Run(ctx, s.client, []string{s.key(arg.ID)}, arg.Token).Int64()
	if err != nil {
		return err
	}
	// 过期 token 返回 0 是刻意的围栏空操作。
	return nil
}

// runLeaseScript 是 acquire/renew 的共同执行壳：校验 TTL 后跑脚本，结果为 0（被拒）时
// 映射成 ErrLeaseNotAcquired。
func (s *RedisLeaseStore) runLeaseScript(ctx context.Context, script *redis.Script, arg AcquireLeaseParams) error {
	ttlMillis := arg.TTL.Milliseconds()
	if ttlMillis <= 0 {
		return errors.New("channel lease TTL must be at least 1ms")
	}
	result, err := script.Run(ctx, s.client, []string{s.key(arg.ID)}, arg.Token, ttlMillis).Int64()
	if err != nil {
		return err
	}
	if result == 0 {
		return ErrLeaseNotAcquired
	}
	return nil
}

// key 拼出某个安装 id 的租约键：前缀 + 命名空间 + 安装 id。
func (s *RedisLeaseStore) key(id pgtype.UUID) string {
	return redisLeaseKeyPrefix + s.namespace + ":" + uuidString(id)
}

var _ LeaseStore = (*RedisLeaseStore)(nil)
