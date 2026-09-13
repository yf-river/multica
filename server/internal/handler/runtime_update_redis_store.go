// 本文件：UpdateStore（CLI 更新请求）的 Redis 实现。CLI 更新与模型清单、本地技能请求共用
// 同一套 pending 请求形态：前端建请求 → daemon 心跳认领 → daemon 回报终态 → UI 按请求 id
// 轮询。多节点部署下这四次调用可能落在不同的 API 副本上，所以生命周期必须放共享存储。
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// 设计说明：CLI 更新与模型清单、本地技能请求共用同一套 pending 请求形态——前端建请求、
// daemon 心跳认领、daemon 回报终态、UI 按请求 id 轮询。多节点部署下四次调用可能落在不同
// API 副本，所以生命周期必须放共享存储。

const (
	updateKeyPrefix          = "mul:" + runtimePendingRedisHashTag + ":update:req:"
	updatePendingPrefix      = "mul:" + runtimePendingRedisHashTag + ":update:pending:"
	updateActivePrefix       = "mul:" + runtimePendingRedisHashTag + ":update:active:"
	updateRedisPopMaxRetries = 5
)

func updateKey(id string) string               { return updateKeyPrefix + id }
func updatePendingKey(runtimeID string) string { return updatePendingPrefix + runtimeID }
func updateActiveKey(runtimeID string) string  { return updateActivePrefix + runtimeID }

// deleteIfValueScript 仅当 key 的当前值等于给定值时删除它——用于释放「active 占用标记」，
// 避免误删后来者新写入的占用（值不匹配则不动）。
var deleteIfValueScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
    return redis.call('DEL', KEYS[1])
end
return 0
`)

// RedisUpdateStore 把 CLI 更新请求存进 Redis，所有节点共享同一份生命周期状态。
// 各方法语义见 UpdateStore 接口注释。
type RedisUpdateStore struct {
	rdb *redis.Client
}

func NewRedisUpdateStore(rdb *redis.Client) *RedisUpdateStore {
	return &RedisUpdateStore{rdb: rdb}
}

func (s *RedisUpdateStore) Create(ctx context.Context, runtimeID, targetVersion, initiatorUserID string) (*UpdateRequest, error) {
	now := time.Now()
	req := &UpdateRequest{
		ID:              randomID(),
		RuntimeID:       runtimeID,
		InitiatorUserID: initiatorUserID,
		Status:          UpdatePending,
		TargetVersion:   targetVersion,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	data, err := s.marshalRequest(req)
	if err != nil {
		return nil, err
	}

	activeKey := updateActiveKey(runtimeID)
	ok, err := s.rdb.SetNX(ctx, activeKey, req.ID, updateStoreRetention).Result()
	if err != nil {
		return nil, fmt.Errorf("reserve active update: %w", err)
	}
	if !ok {
		return nil, errUpdateInProgress
	}

	// 上面的 active 占用标记已经提供了本次写需要的并发保证。这里用纯 pipeline，让允许
	// SET/ZADD 却拒绝 MULTI 的托管 Redis 也能创建更新请求。Exec 失败也安全——下面这段
	// 补偿会清掉每个半写入的 key 并释放占用标记。
	pipe := s.rdb.Pipeline()
	pipe.Set(ctx, updateKey(req.ID), data, updateStoreRetention)
	pipe.ZAdd(ctx, updatePendingKey(runtimeID), redis.Z{
		Score:  float64(now.UnixNano()),
		Member: req.ID,
	})
	pipe.Expire(ctx, updatePendingKey(runtimeID), updateStoreRetention*2)
	if _, err := pipe.Exec(ctx); err != nil {
		_ = s.clearActiveIfMatches(ctx, runtimeID, req.ID)
		_ = s.rdb.Del(ctx, updateKey(req.ID)).Err()
		_ = s.rdb.ZRem(ctx, updatePendingKey(runtimeID), req.ID).Err()
		return nil, fmt.Errorf("persist update request: %w", err)
	}
	return req, nil
}

func (s *RedisUpdateStore) Get(ctx context.Context, id string) (*UpdateRequest, error) {
	return s.loadRequest(ctx, id)
}

func (s *RedisUpdateStore) loadRequest(ctx context.Context, id string) (*UpdateRequest, error) {
	raw, err := s.rdb.Get(ctx, updateKey(id)).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get update request: %w", err)
	}
	req, err := s.unmarshalRequest(raw)
	if err != nil {
		return nil, err
	}
	if applyUpdateTimeout(req, time.Now()) {
		if err := s.persistRequest(ctx, req); err != nil {
			return nil, err
		}
		if err := s.clearActiveIfMatches(ctx, req.RuntimeID, req.ID); err != nil {
			return nil, err
		}
		s.rdb.ZRem(ctx, updatePendingKey(req.RuntimeID), req.ID)
	}
	return req, nil
}

func (s *RedisUpdateStore) persistRequest(ctx context.Context, req *UpdateRequest) error {
	data, err := s.marshalRequest(req)
	if err != nil {
		return err
	}
	if err := s.rdb.Set(ctx, updateKey(req.ID), data, updateStoreRetention).Err(); err != nil {
		return fmt.Errorf("persist update request: %w", err)
	}
	return nil
}

// redisUpdateEnvelope 与 model/list 两个 store 的信封同理：把 UpdateRequest 里标成
// `json:"-"` 的内部字段（RunStartedAt、InitiatorUserID）在落盘时重新提升出来。
type redisUpdateEnvelope struct {
	Public          *UpdateRequest `json:"r"`
	RunStartedAt    *time.Time     `json:"s,omitempty"`
	InitiatorUserID string         `json:"u,omitempty"`
}

func (s *RedisUpdateStore) marshalRequest(req *UpdateRequest) ([]byte, error) {
	env := redisUpdateEnvelope{
		Public:          req,
		RunStartedAt:    req.RunStartedAt,
		InitiatorUserID: req.InitiatorUserID,
	}
	data, err := json.Marshal(env)
	if err != nil {
		return nil, fmt.Errorf("marshal update request: %w", err)
	}
	return data, nil
}

func (s *RedisUpdateStore) unmarshalRequest(raw []byte) (*UpdateRequest, error) {
	var env redisUpdateEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("decode update request: %w", err)
	}
	if env.Public == nil {
		return nil, fmt.Errorf("decode update request: missing payload")
	}
	env.Public.RunStartedAt = env.RunStartedAt
	env.Public.InitiatorUserID = env.InitiatorUserID
	return env.Public, nil
}

func (s *RedisUpdateStore) HasPending(ctx context.Context, runtimeID string) (bool, error) {
	cnt, err := s.rdb.ZCard(ctx, updatePendingKey(runtimeID)).Result()
	if err != nil {
		return false, fmt.Errorf("zcard pending updates: %w", err)
	}
	return cnt > 0, nil
}

func (s *RedisUpdateStore) PopPending(ctx context.Context, runtimeID string) (*UpdateRequest, error) {
	pendingKey := updatePendingKey(runtimeID)

	for attempt := 0; attempt < updateRedisPopMaxRetries; attempt++ {
		ids, err := s.rdb.ZRange(ctx, pendingKey, 0, 0).Result()
		if err != nil {
			return nil, fmt.Errorf("zrange pending updates: %w", err)
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
			s.rdb.ZRem(ctx, pendingKey, id)
			continue
		}
		if req.Status != UpdatePending {
			s.rdb.ZRem(ctx, pendingKey, id)
			continue
		}

		now := time.Now()
		req.Status = UpdateRunning
		req.RunStartedAt = &now
		req.UpdatedAt = now
		data, err := s.marshalRequest(req)
		if err != nil {
			return nil, err
		}

		result, err := claimPendingScript.Run(
			ctx, s.rdb,
			[]string{pendingKey, updateKey(id)},
			id, data, int(updateStoreRetention.Seconds()),
		).Int64()
		if err != nil {
			return nil, fmt.Errorf("claim pending update: %w", err)
		}
		if result == 0 {
			continue
		}
		return req, nil
	}
	return nil, nil
}

func (s *RedisUpdateStore) Complete(ctx context.Context, id string, output string) error {
	req, err := s.loadRequest(ctx, id)
	if err != nil {
		return err
	}
	if req == nil || updateRequestTerminal(req.Status) {
		return nil
	}
	req.Status = UpdateCompleted
	req.Output = output
	req.UpdatedAt = time.Now()
	if err := s.persistRequest(ctx, req); err != nil {
		return err
	}
	if err := s.clearActiveIfMatches(ctx, req.RuntimeID, req.ID); err != nil {
		return err
	}
	s.rdb.ZRem(ctx, updatePendingKey(req.RuntimeID), req.ID)
	return nil
}

func (s *RedisUpdateStore) Fail(ctx context.Context, id string, errMsg string) error {
	req, err := s.loadRequest(ctx, id)
	if err != nil {
		return err
	}
	if req == nil || updateRequestTerminal(req.Status) {
		return nil
	}
	req.Status = UpdateFailed
	req.Error = errMsg
	req.UpdatedAt = time.Now()
	if err := s.persistRequest(ctx, req); err != nil {
		return err
	}
	if err := s.clearActiveIfMatches(ctx, req.RuntimeID, req.ID); err != nil {
		return err
	}
	s.rdb.ZRem(ctx, updatePendingKey(req.RuntimeID), req.ID)
	return nil
}

func (s *RedisUpdateStore) clearActiveIfMatches(ctx context.Context, runtimeID, id string) error {
	if runtimeID == "" || id == "" {
		return nil
	}
	if err := deleteIfValueScript.Run(ctx, s.rdb, []string{updateActiveKey(runtimeID)}, id).Err(); err != nil {
		return fmt.Errorf("clear active update: %w", err)
	}
	return nil
}
