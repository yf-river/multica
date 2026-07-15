package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	localSkillListKeyPrefix       = "mul:local_skill:list:"
	localSkillListPendingPrefix   = "mul:local_skill:list:pending:"
	localSkillImportKeyPrefix     = "mul:local_skill:import:"
	localSkillImportPendingPrefix = "mul:local_skill:import:pending:"
)

type RedisLocalSkillListStore struct {
	*redisRuntimeAsyncStore[RuntimeLocalSkillListRequest]
}

func NewRedisLocalSkillListStore(rdb *redis.Client) *RedisLocalSkillListStore {
	return &RedisLocalSkillListStore{newRedisRuntimeAsyncStore(
		rdb,
		localSkillListKeyPrefix,
		localSkillListPendingPrefix,
		runtimeLocalSkillStoreRetention,
		func(request *RuntimeLocalSkillListRequest) *runtimeAsyncRequestState {
			return &request.runtimeAsyncRequestState
		},
		func(request *RuntimeLocalSkillListRequest) ([]byte, error) {
			data, err := json.Marshal(request)
			if err != nil {
				return nil, fmt.Errorf("marshal list request: %w", err)
			}
			return data, nil
		},
		func(raw []byte) (*RuntimeLocalSkillListRequest, error) {
			var request RuntimeLocalSkillListRequest
			if err := json.Unmarshal(raw, &request); err != nil {
				return nil, fmt.Errorf("decode list request: %w", err)
			}
			return &request, nil
		},
		applyLocalSkillTimeout,
		"list request",
	)}
}

func (s *RedisLocalSkillListStore) Create(ctx context.Context, runtimeID, requestID string) (*RuntimeLocalSkillListRequest, error) {
	now := time.Now()
	request := &RuntimeLocalSkillListRequest{
		runtimeAsyncRequestState: runtimeAsyncRequestState{
			ID: requestID, RuntimeID: runtimeID, Status: runtimeAsyncPending,
			CreatedAt: now, UpdatedAt: now,
		},
		Supported: true,
	}
	return s.create(ctx, request, func(existing *RuntimeLocalSkillListRequest) error {
		if existing.RuntimeID != runtimeID {
			return errRuntimeAsyncRequestConflict
		}
		return nil
	}, "local skill list request disappeared")
}

func (s *RedisLocalSkillListStore) Get(ctx context.Context, id string) (*RuntimeLocalSkillListRequest, error) {
	return s.load(ctx, id)
}

func (s *RedisLocalSkillListStore) HasPending(ctx context.Context, runtimeID string) (bool, error) {
	return s.hasPending(ctx, runtimeID)
}

func (s *RedisLocalSkillListStore) PopPending(ctx context.Context, runtimeID string) (*RuntimeLocalSkillListRequest, error) {
	return s.popPending(ctx, runtimeID)
}

func (s *RedisLocalSkillListStore) Complete(ctx context.Context, id string, skills []RuntimeLocalSkillSummary, supported bool) error {
	return s.update(ctx, id, func(request *RuntimeLocalSkillListRequest, state *runtimeAsyncRequestState, now time.Time) {
		state.Status = runtimeAsyncCompleted
		request.Skills = skills
		request.Supported = supported
		state.UpdatedAt = now
	})
}

func (s *RedisLocalSkillListStore) Fail(ctx context.Context, id string, message string) error {
	return s.fail(ctx, id, message)
}

// redisImportEnvelope keeps internal idempotency and timeout fields in Redis
// while RuntimeLocalSkillImportRequest omits them from HTTP responses.
type redisImportEnvelope struct {
	Public       *RuntimeLocalSkillImportRequest `json:"r"`
	CreatorID    string                          `json:"c"`
	RequestHash  string                          `json:"h"`
	RunStartedAt *time.Time                      `json:"s"`
}

type redisLocalSkillImportStore struct {
	*redisRuntimeAsyncStore[RuntimeLocalSkillImportRequest]
}

func NewRedisLocalSkillImportStore(rdb *redis.Client) *redisLocalSkillImportStore {
	return &redisLocalSkillImportStore{newRedisRuntimeAsyncStore(
		rdb,
		localSkillImportKeyPrefix,
		localSkillImportPendingPrefix,
		runtimeLocalSkillStoreRetention,
		func(request *RuntimeLocalSkillImportRequest) *runtimeAsyncRequestState {
			return &request.runtimeAsyncRequestState
		},
		func(request *RuntimeLocalSkillImportRequest) ([]byte, error) {
			data, err := json.Marshal(redisImportEnvelope{
				Public:       request,
				CreatorID:    request.CreatorID,
				RequestHash:  request.RequestHash,
				RunStartedAt: request.RunStartedAt,
			})
			if err != nil {
				return nil, fmt.Errorf("marshal import request: %w", err)
			}
			return data, nil
		},
		func(raw []byte) (*RuntimeLocalSkillImportRequest, error) {
			var envelope redisImportEnvelope
			if err := json.Unmarshal(raw, &envelope); err != nil {
				return nil, fmt.Errorf("decode import request: %w", err)
			}
			if envelope.Public == nil {
				return nil, fmt.Errorf("decode import request: missing payload")
			}
			envelope.Public.CreatorID = envelope.CreatorID
			envelope.Public.RequestHash = envelope.RequestHash
			envelope.Public.RunStartedAt = envelope.RunStartedAt
			return envelope.Public, nil
		},
		applyLocalSkillTimeout,
		"import request",
	)}
}

func (s *redisLocalSkillImportStore) Create(ctx context.Context, input LocalSkillImportRequestInput) (*RuntimeLocalSkillImportRequest, error) {
	now := time.Now()
	request := &RuntimeLocalSkillImportRequest{
		runtimeAsyncRequestState: runtimeAsyncRequestState{
			ID: input.RequestID, RuntimeID: input.RuntimeID, Status: runtimeAsyncPending,
			CreatedAt: now, UpdatedAt: now,
		},
		SkillKey:      input.SkillKey,
		Name:          input.Name,
		Description:   input.Description,
		Action:        input.Action,
		TargetSkillID: input.TargetSkillID,
		CreatorID:     input.CreatorID,
		RequestHash:   input.RequestHash,
	}
	return s.create(ctx, request, func(existing *RuntimeLocalSkillImportRequest) error {
		if existing.RequestHash != input.RequestHash {
			return errLocalSkillImportRequestConflict
		}
		return nil
	}, "local skill import request disappeared")
}

func (s *redisLocalSkillImportStore) Get(ctx context.Context, id string) (*RuntimeLocalSkillImportRequest, error) {
	return s.load(ctx, id)
}

func (s *redisLocalSkillImportStore) HasPending(ctx context.Context, runtimeID string) (bool, error) {
	return s.hasPending(ctx, runtimeID)
}

func (s *redisLocalSkillImportStore) PopPendingBatch(ctx context.Context, runtimeID string, limit int) ([]*RuntimeLocalSkillImportRequest, error) {
	return s.popPendingBatch(ctx, runtimeID, limit)
}

func (s *redisLocalSkillImportStore) Complete(ctx context.Context, id string, skill SkillResponse) error {
	return s.update(ctx, id, func(request *RuntimeLocalSkillImportRequest, state *runtimeAsyncRequestState, now time.Time) {
		state.Status = runtimeAsyncCompleted
		request.Skill = &skill
		state.UpdatedAt = now
	})
}

func (s *redisLocalSkillImportStore) Conflict(ctx context.Context, id string, info LocalSkillImportConflict) error {
	return s.update(ctx, id, func(request *RuntimeLocalSkillImportRequest, state *runtimeAsyncRequestState, now time.Time) {
		state.Status = runtimeAsyncConflict
		conflict := info
		request.Conflict = &conflict
		state.UpdatedAt = now
	})
}

func (s *redisLocalSkillImportStore) Fail(ctx context.Context, id string, message string) error {
	return s.fail(ctx, id, message)
}
