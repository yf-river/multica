package handler

import (
	"context"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/agent"
	"github.com/redis/go-redis/v9"
)

func TestRedisModelListStore_EnvelopePersistsRunStartedAt(t *testing.T) {
	store := NewRedisModelListStore(nil)
	now := time.Now().UTC().Truncate(time.Microsecond) // JSON loses sub-µs precision
	req := &ModelListRequest{
		runtimeAsyncRequestState: runtimeAsyncRequestState{
			ID: "id-1", RuntimeID: "rt-1", Status: runtimeAsyncRunning,
			CreatedAt: now.Add(-time.Second), UpdatedAt: now, RunStartedAt: &now,
		},
		Supported: true,
	}
	data, err := store.encode(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got, err := store.decode(data)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.RunStartedAt == nil {
		t.Fatal("RunStartedAt lost on round trip — running timeout would never fire across nodes")
	}
	if !got.RunStartedAt.Equal(now) {
		t.Errorf("RunStartedAt drifted: got %s, want %s", got.RunStartedAt, now)
	}
	if got.Status != runtimeAsyncRunning {
		t.Errorf("Status lost: got %s", got.Status)
	}
	if got.ID != "id-1" || got.RuntimeID != "rt-1" {
		t.Errorf("identifiers lost: %+v", got)
	}
}

func TestRedisModelListStore_SharedLifecycle(t *testing.T) {
	assertRedisSingleRequestStoreContract(t, runtimeListPendingTimeout, func(rdb *redis.Client) redisSingleRequestTestHarness[ModelListRequest] {
		store := NewRedisModelListStore(rdb)
		return redisSingleRequestTestHarness[ModelListRequest]{
			store:  store.redisRuntimeAsyncStore,
			create: store.Create,
			get:    store.Get,
			pop:    store.PopPending,
			complete: func(ctx context.Context, id string) error {
				return store.Complete(ctx, id, []agent.Model{
					{ID: "claude-sonnet-4-6", Label: "Claude Sonnet 4.6", Provider: "anthropic", Default: true},
					{ID: "claude-opus-4-7", Label: "Claude Opus 4.7", Provider: "anthropic"},
				}, true)
			},
			assertCompleted: func(t *testing.T, request *ModelListRequest) {
				if len(request.Models) != 2 || !request.Models[0].Default || !request.Supported {
					t.Fatalf("model completion not persisted: %+v", request)
				}
			},
		}
	})
}

func TestRedisModelListStore_RunningTimeout(t *testing.T) {
	rdb := newRedisTestClient(t)
	ctx := context.Background()
	store := NewRedisModelListStore(rdb)

	req, err := store.Create(ctx, "runtime-running-timeout", randomID())
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	popped, err := store.PopPending(ctx, "runtime-running-timeout")
	if err != nil {
		t.Fatalf("pop: %v", err)
	}
	if popped == nil || popped.Status != runtimeAsyncRunning {
		t.Fatalf("expected running, got %+v", popped)
	}

	aged := time.Now().Add(-runtimeAsyncRunningTimeout - time.Second)
	popped.RunStartedAt = &aged
	if err := store.persist(ctx, popped); err != nil {
		t.Fatalf("persist rewound: %v", err)
	}

	got, err := store.Get(ctx, req.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != runtimeAsyncTimeout {
		t.Fatalf("status = %s, want timeout", got.Status)
	}
}

func TestRedisModelListStore_HasPending(t *testing.T) {
	rdb := newRedisTestClient(t)
	ctx := context.Background()
	store := NewRedisModelListStore(rdb)

	if has, err := store.HasPending(ctx, "rt-empty"); err != nil || has {
		t.Fatalf("empty store should not report pending: has=%v err=%v", has, err)
	}

	if _, err := store.Create(ctx, "rt-1", randomID()); err != nil {
		t.Fatalf("create: %v", err)
	}
	if has, err := store.HasPending(ctx, "rt-1"); err != nil || !has {
		t.Fatalf("expected pending=true after Create: has=%v err=%v", has, err)
	}
	if has, err := store.HasPending(ctx, "rt-other"); err != nil || has {
		t.Fatalf("expected pending=false for unrelated runtime: has=%v err=%v", has, err)
	}

	if _, err := store.PopPending(ctx, "rt-1"); err != nil {
		t.Fatalf("pop: %v", err)
	}
	if has, err := store.HasPending(ctx, "rt-1"); err != nil || has {
		t.Fatalf("expected pending=false after PopPending: has=%v err=%v", has, err)
	}
}
