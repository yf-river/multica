package handler

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/multica-ai/multica/server/pkg/agent"
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

func NewRedisModelListStore(rdb *redis.Client) *redisRuntimeListStore[ModelListRequest, agent.Model] {
	return newRedisRuntimeListStore(
		newRedisRuntimeAsyncStore(
			rdb,
			modelListKeyPrefix,
			modelListPendingPrefix,
			modelListStoreRetention,
			(*ModelListRequest).asyncRequestState,
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
			applyRuntimeListTimeout,
			"model list request",
		),
		func(runtimeID, requestID string, now time.Time) *ModelListRequest {
			return &ModelListRequest{
				runtimeAsyncRequestState: runtimeAsyncRequestState{
					ID: requestID, RuntimeID: runtimeID, Status: runtimeAsyncPending,
					CreatedAt: now, UpdatedAt: now,
				},
				Supported: true,
			}
		},
		func(request *ModelListRequest, models []agent.Model, supported bool) {
			request.Models = models
			request.Supported = supported
		},
		"model list request disappeared",
	)
}
