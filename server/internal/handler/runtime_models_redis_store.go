// 本文件：ModelListStore 的 Redis 实现。线上布局与 runtime_local_skills_redis_store.go
// （解决技能清单/导入的同一个多节点派发问题）一致，运维口径完全相同：带命名空间的 key、
// ZSET 支撑的 pending 队列、共享 Lua 脚本的原子认领。
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// 设计说明（见 runtime_local_skills_redis_store.go 的同款布局与原子认领论证）：
//
// Key 布局：
//
//   mul:{runtime_pending}:model_list:req:<request_id>           → JSON 编码的 ModelListRequest，TTL = 保留时长
//   mul:{runtime_pending}:model_list:pending:<runtime_id>       → ZSET { member = request_id, score = created_at UnixNano }
//                                                                  TTL = 保留时长*2（活得够久，让 PopPending 能惰性清扫）
//
// PopPending 用 claimPendingScript（定义在 runtime_local_skills_redis_store.go）原子执行
// ZREM（移除 pending 项）+ SET（把记录置为 "running"）——这两次写若拆开，一旦中间 Redis
// 抖动，请求就会被困在半途。

const (
	// 命名空间统一挂在 mul:*:model_list:* 下，避免与实时中继（ws:*）和本地技能存储的 key 撞车。
	modelListKeyPrefix          = "mul:" + runtimePendingRedisHashTag + ":model_list:req:"
	modelListPendingPrefix      = "mul:" + runtimePendingRedisHashTag + ":model_list:pending:"
	modelListRedisPopMaxRetries = 5
)

func modelListKey(id string) string               { return modelListKeyPrefix + id }
func modelListPendingKey(runtimeID string) string { return modelListPendingPrefix + runtimeID }

// RedisModelListStore 把模型清单请求存进 Redis，让所有 API 节点对同一份 pending/running/
// 终态达成一致。各方法语义见 ModelListStore 接口注释。
type RedisModelListStore struct {
	rdb *redis.Client
}

func NewRedisModelListStore(rdb *redis.Client) *RedisModelListStore {
	return &RedisModelListStore{rdb: rdb}
}

func (s *RedisModelListStore) Create(ctx context.Context, runtimeID string) (*ModelListRequest, error) {
	now := time.Now()
	req := &ModelListRequest{
		ID:        randomID(),
		RuntimeID: runtimeID,
		Status:    ModelListPending,
		Supported: true,
		CreatedAt: now,
		UpdatedAt: now,
	}
	data, err := s.marshalRequest(req)
	if err != nil {
		return nil, err
	}

	requestKey := modelListKey(req.ID)
	pendingKey := modelListPendingKey(runtimeID)
	// 创建不需要 Redis 事务：请求在进入 pending 集合前对派发方不可见。纯 pipeline 在禁
	// MULTI 的托管 Redis 上也能跑。
	pipe := s.rdb.Pipeline()
	pipe.Set(ctx, requestKey, data, modelListStoreRetention)
	pipe.ZAdd(ctx, pendingKey, redis.Z{
		Score:  float64(now.UnixNano()),
		Member: req.ID,
	})
	// 让 pending zset 活过单条记录的保留时长，这样过期成员可由 PopPending 惰性清扫。
	pipe.Expire(ctx, pendingKey, modelListStoreRetention*2)
	if _, err := pipe.Exec(ctx); err != nil {
		_ = s.rdb.Del(ctx, requestKey).Err()
		_ = s.rdb.ZRem(ctx, pendingKey, req.ID).Err()
		return nil, fmt.Errorf("persist model list request: %w", err)
	}
	return req, nil
}

func (s *RedisModelListStore) Get(ctx context.Context, id string) (*ModelListRequest, error) {
	return s.loadRequest(ctx, id)
}

// loadRequest 读取单条记录；存储状态超过阈值就推进超时转换并持久化，让兄弟节点也观察到
// 同样的终态。
func (s *RedisModelListStore) loadRequest(ctx context.Context, id string) (*ModelListRequest, error) {
	raw, err := s.rdb.Get(ctx, modelListKey(id)).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get model list request: %w", err)
	}
	req, err := s.unmarshalRequest(raw)
	if err != nil {
		return nil, err
	}
	if applyModelListTimeout(req, time.Now()) {
		if err := s.persistRequest(ctx, req); err != nil {
			return nil, err
		}
		// 转终态时把它从 pending zset 摘掉。PopPending 本来也会做，但在这里做能让从未调
		// PopPending 的读取路径也把集合保持干净。
		s.rdb.ZRem(ctx, modelListPendingKey(req.RuntimeID), req.ID)
	}
	return req, nil
}

func (s *RedisModelListStore) persistRequest(ctx context.Context, req *ModelListRequest) error {
	data, err := s.marshalRequest(req)
	if err != nil {
		return err
	}
	if err := s.rdb.Set(ctx, modelListKey(req.ID), data, modelListStoreRetention).Err(); err != nil {
		return fmt.Errorf("persist model list request: %w", err)
	}
	return nil
}

// ModelListRequest 把 RunStartedAt 标成 `json:"-"`，让这个服务端记账字段不泄漏到 HTTP
// 响应里（UI 轮询只需要 Status / UpdatedAt）。但 Redis 持久化必须保留它，否则 running 超时
// 这个逃生通道会在多节点间悄悄失效——每个读者都看到 RunStartedAt=nil，applyModelListTimeout
// 的 running 分支变成 no-op。因此用内部信封把它在落盘时重新提升出来。
type redisModelListEnvelope struct {
	Public       *ModelListRequest `json:"r"`
	RunStartedAt *time.Time        `json:"s,omitempty"`
}

func (s *RedisModelListStore) marshalRequest(req *ModelListRequest) ([]byte, error) {
	env := redisModelListEnvelope{Public: req, RunStartedAt: req.RunStartedAt}
	data, err := json.Marshal(env)
	if err != nil {
		return nil, fmt.Errorf("marshal model list request: %w", err)
	}
	return data, nil
}

func (s *RedisModelListStore) unmarshalRequest(raw []byte) (*ModelListRequest, error) {
	var env redisModelListEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("decode model list request: %w", err)
	}
	if env.Public == nil {
		return nil, fmt.Errorf("decode model list request: missing payload")
	}
	env.Public.RunStartedAt = env.RunStartedAt
	return env.Public, nil
}

// HasPending 是廉价只读 ZCARD 探测，心跳热路径用它判断要不要调用有副作用的 PopPending。
func (s *RedisModelListStore) HasPending(ctx context.Context, runtimeID string) (bool, error) {
	cnt, err := s.rdb.ZCard(ctx, modelListPendingKey(runtimeID)).Result()
	if err != nil {
		return false, fmt.Errorf("zcard pending: %w", err)
	}
	return cnt > 0, nil
}

func (s *RedisModelListStore) PopPending(ctx context.Context, runtimeID string) (*ModelListRequest, error) {
	pendingKey := modelListPendingKey(runtimeID)

	for attempt := 0; attempt < modelListRedisPopMaxRetries; attempt++ {
		ids, err := s.rdb.ZRange(ctx, pendingKey, 0, 0).Result()
		if err != nil {
			return nil, fmt.Errorf("zrange pending: %w", err)
		}
		if len(ids) == 0 {
			return nil, nil
		}
		id := ids[0]

		req, err := s.loadRequest(ctx, id)
		if err != nil {
			return nil, err
		}
		if req == nil {
			// 记录已过期但 zset 还引用着它——摘掉后重试。
			s.rdb.ZRem(ctx, pendingKey, id)
			continue
		}
		if req.Status != ModelListPending {
			// loadRequest 里超时了，或已被别的节点取走。从 pending 集合断开并重试。
			s.rdb.ZRem(ctx, pendingKey, id)
			continue
		}

		now := time.Now()
		req.Status = ModelListRunning
		req.RunStartedAt = &now
		req.UpdatedAt = now
		data, err := s.marshalRequest(req)
		if err != nil {
			return nil, err
		}

		result, err := claimPendingScript.Run(
			ctx, s.rdb,
			[]string{pendingKey, modelListKey(id)},
			id, data, int(modelListStoreRetention.Seconds()),
		).Int64()
		if err != nil {
			return nil, fmt.Errorf("claim pending: %w", err)
		}
		if result == 0 {
			// 别的节点赢了竞态，记录归赢家所有；重试认领队列里剩下的（或没有）。
			continue
		}
		return req, nil
	}
	return nil, nil
}

func (s *RedisModelListStore) Complete(ctx context.Context, id string, models []ModelEntry, unavailable []UnavailableModelEntry, supported bool) error {
	req, err := s.loadRequest(ctx, id)
	if err != nil {
		return err
	}
	if req == nil {
		return nil
	}
	req.Status = ModelListCompleted
	req.Models = models
	req.UnavailableModels = unavailable
	req.Supported = supported
	req.UpdatedAt = time.Now()
	return s.persistRequest(ctx, req)
}

func (s *RedisModelListStore) Fail(ctx context.Context, id string, errMsg string) error {
	req, err := s.loadRequest(ctx, id)
	if err != nil {
		return err
	}
	if req == nil {
		return nil
	}
	req.Status = ModelListFailed
	req.Error = errMsg
	req.UpdatedAt = time.Now()
	return s.persistRequest(ctx, req)
}
