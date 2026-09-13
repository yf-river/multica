// 本文件是 LocalSkillListStore / LocalSkillImportStore 的 Redis 实现（多节点共享状态用的
// 存储），文件头下面的注释说明了它的存储布局与认领原子性设计。
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// 设计说明（list 与 import 两条流程共用，只是 key 前缀不同）：
//
// 存储布局：
//   <prefix>:<request_id>                 → JSON 编码的请求，TTL = 保留时长
//   <prefix>:pending:<runtime_id>         → ZSET { member = request_id, score = created_at UnixNano }
//                                           TTL = 保留时长，Create 时刷新
//
// PopPending 是多节点下最关键的原语，必须原子完成两件事：
//  1. 挑出这台 runtime 最老的 pending 请求 id
//  2. 认领它（从 pending zset 移除）并同时把记录置为 "running"——否则两步之间一旦进程
//     崩溃或 Redis 抖动，请求就被困在中间态（不再 pending、记录却仍写着 pending），
//     任何节点都不会再派发它。
//
// 拆成两次往返有竞态，所以用 Lua 脚本让 Redis 在服务端原子执行 ZREM + SET。若 ZREM
// 返回 0（已被别的节点认领）就跳过 SET。这正是 PR-1557 评审发现的「Redis 抖动下请求
// 消失」路径的修复。

const (
	// 哈希标签（hash tag）让这些 key 都落在同一个 Redis 槽上，保证多 key 的原子操作可用。
	runtimePendingRedisHashTag = "{runtime_pending}"

	// 加命名空间前缀，避免和实时中继（realtime relay）的 ws:* key 撞车。
	localSkillListKeyPrefix       = "mul:" + runtimePendingRedisHashTag + ":local_skill:list:"
	localSkillListPendingPrefix   = "mul:" + runtimePendingRedisHashTag + ":local_skill:list:pending:"
	localSkillImportKeyPrefix     = "mul:" + runtimePendingRedisHashTag + ":local_skill:import:"
	localSkillImportPendingPrefix = "mul:" + runtimePendingRedisHashTag + ":local_skill:import:pending:"
	localSkillRedisPopMaxRetries  = 5
)

// claimPendingScript 原子地认领一个 pending 请求：
//
//	KEYS[1] = pending zset    ARGV[1] = 要认领的 request id
//	KEYS[2] = 记录 key         ARGV[2] = 新记录 JSON（status=running）
//	                           ARGV[3] = 记录 TTL（秒）
//
// 返回 1 表示本调用方赢得了认领（zset 项已移除、记录已更新），0 表示该请求已被别的节点
// 抢先认领。ZREM 与 SET 要么都发生要么都不发生——Redis 把 Lua 脚本当单一原子单元执行。
var claimPendingScript = redis.NewScript(`
local removed = redis.call('ZREM', KEYS[1], ARGV[1])
if removed == 0 then
    return 0
end
redis.call('SET', KEYS[2], ARGV[2], 'EX', tonumber(ARGV[3]))
return 1
`)

func localSkillListKey(id string) string { return localSkillListKeyPrefix + id }
func localSkillListPendingKey(runtimeID string) string {
	return localSkillListPendingPrefix + runtimeID
}
func localSkillImportKey(id string) string { return localSkillImportKeyPrefix + id }
func localSkillImportPendingKey(runtimeID string) string {
	return localSkillImportPendingPrefix + runtimeID
}

// RedisLocalSkillListStore 把 pending/running/completed 的清单请求存在 Redis 里，
// 让所有 API 节点对同一份状态达成一致。各方法语义见 LocalSkillListStore 接口注释。
type RedisLocalSkillListStore struct {
	rdb *redis.Client
}

func NewRedisLocalSkillListStore(rdb *redis.Client) *RedisLocalSkillListStore {
	return &RedisLocalSkillListStore{rdb: rdb}
}

func (s *RedisLocalSkillListStore) Create(ctx context.Context, runtimeID string) (*RuntimeLocalSkillListRequest, error) {
	now := time.Now()
	req := &RuntimeLocalSkillListRequest{
		ID:        randomID(),
		RuntimeID: runtimeID,
		Status:    RuntimeLocalSkillPending,
		Supported: true,
		CreatedAt: now,
		UpdatedAt: now,
	}
	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal list request: %w", err)
	}

	requestKey := localSkillListKey(req.ID)
	pendingKey := localSkillListPendingKey(runtimeID)
	// 创建不需要 Redis 事务：请求在进入 pending 集合之前对派发方不可见。纯 pipeline
	// 在禁 MULTI 的托管 Redis 上也能跑。
	pipe := s.rdb.Pipeline()
	pipe.Set(ctx, requestKey, data, runtimeLocalSkillStoreRetention)
	pipe.ZAdd(ctx, pendingKey, redis.Z{
		Score:  float64(now.UnixNano()),
		Member: req.ID,
	})
	// 让 pending ZSET 的存活时间比单条请求更长，这样仍滞留在 zset 里的过期成员可以由
	// PopPending 惰性清扫，不会阻塞创建路径做删除。
	pipe.Expire(ctx, pendingKey, runtimeLocalSkillStoreRetention*2)
	if _, err := pipe.Exec(ctx); err != nil {
		_ = s.rdb.Del(ctx, requestKey).Err()
		_ = s.rdb.ZRem(ctx, pendingKey, req.ID).Err()
		return nil, fmt.Errorf("persist list request: %w", err)
	}
	return req, nil
}

func (s *RedisLocalSkillListStore) Get(ctx context.Context, id string) (*RuntimeLocalSkillListRequest, error) {
	return s.loadListRequest(ctx, id)
}

// loadListRequest 读取单条记录；若存储状态已超过阈值就推进超时转换，并把转换结果持久化，
// 让其它节点也能观察到同样的终态。
func (s *RedisLocalSkillListStore) loadListRequest(ctx context.Context, id string) (*RuntimeLocalSkillListRequest, error) {
	raw, err := s.rdb.Get(ctx, localSkillListKey(id)).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get list request: %w", err)
	}
	var req RuntimeLocalSkillListRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, fmt.Errorf("decode list request: %w", err)
	}
	if applyLocalSkillListTimeout(&req, time.Now()) {
		// 持久化超时态，让之后任何节点上的 Get / PopPending 都能看到终态。同时把该 id 从
		// pending zset 里移除——PopPending 本来也会做，但在这里做能让从未调 PopPending 的
		// 读取路径也把集合保持干净。
		if err := s.persistListRequest(ctx, &req); err != nil {
			return nil, err
		}
		s.rdb.ZRem(ctx, localSkillListPendingKey(req.RuntimeID), req.ID)
	}
	return &req, nil
}

func (s *RedisLocalSkillListStore) persistListRequest(ctx context.Context, req *RuntimeLocalSkillListRequest) error {
	data, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal list request: %w", err)
	}
	if err := s.rdb.Set(ctx, localSkillListKey(req.ID), data, runtimeLocalSkillStoreRetention).Err(); err != nil {
		return fmt.Errorf("persist list request: %w", err)
	}
	return nil
}

// HasPending 是廉价只读探测（ZCARD），热路径用它判断要不要调用有副作用的 PopPending。
// 它不清扫过期/已被认领的项——偶尔误报 "true" 没关系，后续的 PopPending 本身能正确处理
// 这种竞态。
func (s *RedisLocalSkillListStore) HasPending(ctx context.Context, runtimeID string) (bool, error) {
	cnt, err := s.rdb.ZCard(ctx, localSkillListPendingKey(runtimeID)).Result()
	if err != nil {
		return false, fmt.Errorf("zcard pending: %w", err)
	}
	return cnt > 0, nil
}

func (s *RedisLocalSkillListStore) PopPending(ctx context.Context, runtimeID string) (*RuntimeLocalSkillListRequest, error) {
	pendingKey := localSkillListPendingKey(runtimeID)

	for attempt := 0; attempt < localSkillRedisPopMaxRetries; attempt++ {
		ids, err := s.rdb.ZRange(ctx, pendingKey, 0, 0).Result()
		if err != nil {
			return nil, fmt.Errorf("zrange pending: %w", err)
		}
		if len(ids) == 0 {
			return nil, nil
		}
		id := ids[0]

		req, err := s.loadListRequest(ctx, id)
		if err != nil {
			return nil, err
		}
		if req == nil {
			// 记录已过期但 zset 还引用着它——摘掉后重试。
			s.rdb.ZRem(ctx, pendingKey, id)
			continue
		}
		if req.Status != RuntimeLocalSkillPending {
			// 要么 loadListRequest 里触发了超时，要么别的节点已经取走了。两种情况都把它从
			// pending 集合断开，继续看下一条。
			s.rdb.ZRem(ctx, pendingKey, id)
			continue
		}

		now := time.Now()
		req.Status = RuntimeLocalSkillRunning
		req.RunStartedAt = &now
		req.UpdatedAt = now
		data, err := json.Marshal(req)
		if err != nil {
			return nil, fmt.Errorf("marshal list request: %w", err)
		}

		result, err := claimPendingScript.Run(
			ctx, s.rdb,
			[]string{pendingKey, localSkillListKey(id)},
			id, data, int(runtimeLocalSkillStoreRetention.Seconds()),
		).Int64()
		if err != nil {
			return nil, fmt.Errorf("claim pending: %w", err)
		}
		if result == 0 {
			// 别的节点赢了竞态。记录仍写着 pending，归赢家所有；我们继续重试认领队列里
			// 剩下的（或没有）。
			continue
		}
		return req, nil
	}
	return nil, nil
}

func (s *RedisLocalSkillListStore) Complete(ctx context.Context, id string, skills []RuntimeLocalSkillSummary, supported bool, mcpServers []RuntimeLocalMcpServerSummary, mcpSupported bool) error {
	req, err := s.loadListRequest(ctx, id)
	if err != nil {
		return err
	}
	if req == nil {
		return nil
	}
	req.Status = RuntimeLocalSkillCompleted
	req.Skills = skills
	req.Supported = supported
	req.McpServers = mcpServers
	req.McpSupported = mcpSupported
	req.UpdatedAt = time.Now()
	return s.persistListRequest(ctx, req)
}

func (s *RedisLocalSkillListStore) Fail(ctx context.Context, id string, errMsg string) error {
	req, err := s.loadListRequest(ctx, id)
	if err != nil {
		return err
	}
	if req == nil {
		return nil
	}
	req.Status = RuntimeLocalSkillFailed
	req.Error = errMsg
	req.UpdatedAt = time.Now()
	return s.persistListRequest(ctx, req)
}

// RedisLocalSkillImportStore 与 RedisLocalSkillListStore 同思路，面向导入请求。单独保留
// 类型（不做成泛型）是因为请求结构带导入专属字段（skill_key、可选改名、creator id），
// 而两种实现下泛型的收益不大。
type RedisLocalSkillImportStore struct {
	rdb *redis.Client
}

func NewRedisLocalSkillImportStore(rdb *redis.Client) *RedisLocalSkillImportStore {
	return &RedisLocalSkillImportStore{rdb: rdb}
}

func (s *RedisLocalSkillImportStore) Create(ctx context.Context, input LocalSkillImportRequestInput) (*RuntimeLocalSkillImportRequest, error) {
	now := time.Now()
	req := &RuntimeLocalSkillImportRequest{
		ID:               randomID(),
		RuntimeID:        input.RuntimeID,
		SkillKey:         input.SkillKey,
		Name:             input.Name,
		Description:      input.Description,
		Action:           input.Action,
		TargetSkillID:    input.TargetSkillID,
		SupportsConflict: input.SupportsConflict,
		Status:           RuntimeLocalSkillPending,
		CreatedAt:        now,
		UpdatedAt:        now,
		CreatorID:        input.CreatorID,
	}
	data, err := s.marshalImport(req)
	if err != nil {
		return nil, err
	}

	requestKey := localSkillImportKey(req.ID)
	pendingKey := localSkillImportPendingKey(input.RuntimeID)
	// 与另一个请求存储一致：创建只需要有序批量写，避开 MULTI 也让这条路径兼容受限的托管 Redis。
	pipe := s.rdb.Pipeline()
	pipe.Set(ctx, requestKey, data, runtimeLocalSkillStoreRetention)
	pipe.ZAdd(ctx, pendingKey, redis.Z{
		Score:  float64(now.UnixNano()),
		Member: req.ID,
	})
	pipe.Expire(ctx, pendingKey, runtimeLocalSkillStoreRetention*2)
	if _, err := pipe.Exec(ctx); err != nil {
		_ = s.rdb.Del(ctx, requestKey).Err()
		_ = s.rdb.ZRem(ctx, pendingKey, req.ID).Err()
		return nil, fmt.Errorf("persist import request: %w", err)
	}
	return req, nil
}

func (s *RedisLocalSkillImportStore) Get(ctx context.Context, id string) (*RuntimeLocalSkillImportRequest, error) {
	return s.loadImportRequest(ctx, id)
}

func (s *RedisLocalSkillImportStore) loadImportRequest(ctx context.Context, id string) (*RuntimeLocalSkillImportRequest, error) {
	raw, err := s.rdb.Get(ctx, localSkillImportKey(id)).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get import request: %w", err)
	}
	req, err := s.unmarshalImport(raw)
	if err != nil {
		return nil, err
	}
	if applyLocalSkillImportTimeout(req, time.Now()) {
		if err := s.persistImportRequest(ctx, req); err != nil {
			return nil, err
		}
		s.rdb.ZRem(ctx, localSkillImportPendingKey(req.RuntimeID), req.ID)
	}
	return req, nil
}

func (s *RedisLocalSkillImportStore) persistImportRequest(ctx context.Context, req *RuntimeLocalSkillImportRequest) error {
	data, err := s.marshalImport(req)
	if err != nil {
		return err
	}
	if err := s.rdb.Set(ctx, localSkillImportKey(req.ID), data, runtimeLocalSkillStoreRetention).Err(); err != nil {
		return fmt.Errorf("persist import request: %w", err)
	}
	return nil
}

// RuntimeLocalSkillImportRequest 类型把 CreatorID / RunStartedAt 标成了 `json:"-"`，
// 这两个字段因此不会在 HTTP 响应里泄露内部状态。但 Redis 持久化需要它们，所以用一个
// 内部信封把它们重新提升出来。
type redisImportEnvelope struct {
	Public       *RuntimeLocalSkillImportRequest `json:"r"`
	CreatorID    string                          `json:"c"`
	RunStartedAt *time.Time                      `json:"s"`
}

func (s *RedisLocalSkillImportStore) marshalImport(req *RuntimeLocalSkillImportRequest) ([]byte, error) {
	env := redisImportEnvelope{
		Public:       req,
		CreatorID:    req.CreatorID,
		RunStartedAt: req.RunStartedAt,
	}
	data, err := json.Marshal(env)
	if err != nil {
		return nil, fmt.Errorf("marshal import request: %w", err)
	}
	return data, nil
}

func (s *RedisLocalSkillImportStore) unmarshalImport(raw []byte) (*RuntimeLocalSkillImportRequest, error) {
	var env redisImportEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("decode import request: %w", err)
	}
	if env.Public == nil {
		return nil, fmt.Errorf("decode import request: missing payload")
	}
	env.Public.CreatorID = env.CreatorID
	env.Public.RunStartedAt = env.RunStartedAt
	return env.Public, nil
}

// HasPending 与 RedisLocalSkillListStore.HasPending 相同——廉价的 ZCARD 探测，供热路径做闸门。
func (s *RedisLocalSkillImportStore) HasPending(ctx context.Context, runtimeID string) (bool, error) {
	cnt, err := s.rdb.ZCard(ctx, localSkillImportPendingKey(runtimeID)).Result()
	if err != nil {
		return false, fmt.Errorf("zcard pending: %w", err)
	}
	return cnt > 0, nil
}

func (s *RedisLocalSkillImportStore) PopPending(ctx context.Context, runtimeID string) (*RuntimeLocalSkillImportRequest, error) {
	pendingKey := localSkillImportPendingKey(runtimeID)

	for attempt := 0; attempt < localSkillRedisPopMaxRetries; attempt++ {
		ids, err := s.rdb.ZRange(ctx, pendingKey, 0, 0).Result()
		if err != nil {
			return nil, fmt.Errorf("zrange pending: %w", err)
		}
		if len(ids) == 0 {
			return nil, nil
		}
		id := ids[0]

		req, err := s.loadImportRequest(ctx, id)
		if err != nil {
			return nil, err
		}
		if req == nil {
			s.rdb.ZRem(ctx, pendingKey, id)
			continue
		}
		if req.Status != RuntimeLocalSkillPending {
			s.rdb.ZRem(ctx, pendingKey, id)
			continue
		}

		now := time.Now()
		req.Status = RuntimeLocalSkillRunning
		req.RunStartedAt = &now
		req.UpdatedAt = now
		data, err := s.marshalImport(req)
		if err != nil {
			return nil, err
		}

		result, err := claimPendingScript.Run(
			ctx, s.rdb,
			[]string{pendingKey, localSkillImportKey(id)},
			id, data, int(runtimeLocalSkillStoreRetention.Seconds()),
		).Int64()
		if err != nil {
			return nil, fmt.Errorf("claim pending: %w", err)
		}
		if result == 0 {
			continue
		}
		return req, nil
	}
	return nil, nil
}

func (s *RedisLocalSkillImportStore) PopPendingBatch(ctx context.Context, runtimeID string, limit int) ([]*RuntimeLocalSkillImportRequest, error) {
	pendingKey := localSkillImportPendingKey(runtimeID)

	// 从有序集合里取最多 limit 个候选 id。
	ids, err := s.rdb.ZRange(ctx, pendingKey, 0, int64(limit)-1).Result()
	if err != nil {
		return nil, fmt.Errorf("zrange pending batch: %w", err)
	}
	if len(ids) == 0 {
		return nil, nil
	}

	// 逐个用现有的原子 Lua 脚本认领每个候选。多节点竞争下也安全：每个 ZREM 都是原子的，
	// 两个节点不可能认领到同一个请求。
	var result []*RuntimeLocalSkillImportRequest
	for _, id := range ids {
		req, err := s.loadImportRequest(ctx, id)
		if err != nil {
			return result, err
		}
		if req == nil {
			s.rdb.ZRem(ctx, pendingKey, id)
			continue
		}
		if req.Status != RuntimeLocalSkillPending {
			s.rdb.ZRem(ctx, pendingKey, id)
			continue
		}

		now := time.Now()
		req.Status = RuntimeLocalSkillRunning
		req.RunStartedAt = &now
		req.UpdatedAt = now
		data, err := s.marshalImport(req)
		if err != nil {
			return result, err
		}

		claimed, err := claimPendingScript.Run(
			ctx, s.rdb,
			[]string{pendingKey, localSkillImportKey(id)},
			id, data, int(runtimeLocalSkillStoreRetention.Seconds()),
		).Int64()
		if err != nil {
			return result, fmt.Errorf("claim pending batch: %w", err)
		}
		if claimed == 1 {
			result = append(result, req)
		}
	}
	return result, nil
}

func (s *RedisLocalSkillImportStore) Complete(ctx context.Context, id string, skill SkillWithFilesResponse) error {
	req, err := s.loadImportRequest(ctx, id)
	if err != nil {
		return err
	}
	if req == nil {
		return nil
	}
	req.Status = RuntimeLocalSkillCompleted
	req.Skill = &skill
	req.UpdatedAt = time.Now()
	return s.persistImportRequest(ctx, req)
}

func (s *RedisLocalSkillImportStore) Conflict(ctx context.Context, id string, info LocalSkillImportConflict) error {
	req, err := s.loadImportRequest(ctx, id)
	if err != nil {
		return err
	}
	if req == nil {
		return nil
	}
	req.Status = RuntimeLocalSkillConflict
	conflict := info
	req.Conflict = &conflict
	req.UpdatedAt = time.Now()
	return s.persistImportRequest(ctx, req)
}

func (s *RedisLocalSkillImportStore) Fail(ctx context.Context, id string, errMsg string) error {
	req, err := s.loadImportRequest(ctx, id)
	if err != nil {
		return err
	}
	if req == nil {
		return nil
	}
	req.Status = RuntimeLocalSkillFailed
	req.Error = errMsg
	req.UpdatedAt = time.Now()
	return s.persistImportRequest(ctx, req)
}
