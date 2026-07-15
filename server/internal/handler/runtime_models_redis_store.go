package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	modelListKeyPrefix     = "mul:model_list:req:"
	modelListPendingPrefix = "mul:model_list:pending:"
)

// redisModelListEnvelope persists RunStartedAt without exposing that internal
// timeout field through the public ModelListRequest JSON contract.
type redisModelListEnvelope struct {
	Public       *ModelListRequest `json:"r"`
	RunStartedAt *time.Time        `json:"s,omitempty"`
}

type redisModelListStore struct {
	*redisRuntimeAsyncStore[ModelListRequest]
}

func NewRedisModelListStore(rdb *redis.Client) *redisModelListStore {
	return &redisModelListStore{newRedisRuntimeAsyncStore(
		rdb,
		modelListKeyPrefix,
		modelListPendingPrefix,
		modelListStoreRetention,
		func(request *ModelListRequest) *runtimeAsyncRequestState {
			return &request.runtimeAsyncRequestState
		},
		func(request *ModelListRequest) ([]byte, error) {
			data, err := json.Marshal(redisModelListEnvelope{
				Public:       request,
				RunStartedAt: request.RunStartedAt,
			})
			if err != nil {
				return nil, fmt.Errorf("marshal model list request: %w", err)
			}
			return data, nil
		},
		func(raw []byte) (*ModelListRequest, error) {
			var envelope redisModelListEnvelope
			if err := json.Unmarshal(raw, &envelope); err != nil {
				return nil, fmt.Errorf("decode model list request: %w", err)
			}
			if envelope.Public == nil {
				return nil, fmt.Errorf("decode model list request: missing payload")
			}
			envelope.Public.RunStartedAt = envelope.RunStartedAt
			return envelope.Public, nil
		},
		applyModelListTimeout,
		"model list request",
	)}
}

func (s *redisModelListStore) Create(ctx context.Context, runtimeID, requestID string) (*ModelListRequest, error) {
	now := time.Now()
	request := &ModelListRequest{
		runtimeAsyncRequestState: runtimeAsyncRequestState{
			ID: requestID, RuntimeID: runtimeID, Status: runtimeAsyncPending,
			CreatedAt: now, UpdatedAt: now,
		},
		Supported: true,
	}
	return s.create(ctx, request, func(existing *ModelListRequest) error {
		if existing.RuntimeID != runtimeID {
			return errRuntimeAsyncRequestConflict
		}
		return nil
	}, "model list request disappeared")
}

func (s *redisModelListStore) Get(ctx context.Context, id string) (*ModelListRequest, error) {
	return s.load(ctx, id)
}

func (s *redisModelListStore) HasPending(ctx context.Context, runtimeID string) (bool, error) {
	return s.hasPending(ctx, runtimeID)
}

func (s *redisModelListStore) PopPending(ctx context.Context, runtimeID string) (*ModelListRequest, error) {
	return s.popPending(ctx, runtimeID)
}

func (s *redisModelListStore) Complete(ctx context.Context, id string, models []ModelEntry, supported bool) error {
	return s.update(ctx, id, func(request *ModelListRequest, state *runtimeAsyncRequestState, now time.Time) {
		state.Status = runtimeAsyncCompleted
		request.Models = models
		request.Supported = supported
		state.UpdatedAt = now
	})
}

func (s *redisModelListStore) Fail(ctx context.Context, id string, message string) error {
	return s.fail(ctx, id, message)
}
