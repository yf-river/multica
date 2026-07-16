package handler

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"github.com/redis/go-redis/v9"
)

func newRedisTestClient(t *testing.T) *redis.Client {
	t.Helper()
	return testutil.NewRedisTestClient(t, testutil.RedisDBHandler)
}

func assertSingleConcurrentPopWinner[T any](t *testing.T, wantID string, pop func() (*T, error), requestID func(*T) string) {
	t.Helper()
	const n = 8

	var wg sync.WaitGroup
	results := make(chan *T, n)
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			popped, err := pop()
			if err != nil {
				errs <- err
				return
			}
			results <- popped
		}()
	}
	wg.Wait()
	close(results)
	close(errs)

	for err := range errs {
		t.Fatalf("concurrent pop error: %v", err)
	}

	winners := 0
	for popped := range results {
		if popped != nil {
			winners++
			if requestID(popped) != wantID {
				t.Fatalf("winner popped wrong id: %s", requestID(popped))
			}
		}
	}
	if winners != 1 {
		t.Fatalf("expected exactly one winner, got %d", winners)
	}
}

type redisSingleRequestTestHarness[T any] struct {
	store           *redisRuntimeAsyncStore[T]
	create          func(context.Context, string, string) (*T, error)
	get             func(context.Context, string) (*T, error)
	pop             func(context.Context, string) (*T, error)
	complete        func(context.Context, string) error
	assertCompleted func(*testing.T, *T)
}

func assertRedisSingleRequestStoreContract[T any](
	t *testing.T,
	pendingTimeout time.Duration,
	newHarness func(*redis.Client) redisSingleRequestTestHarness[T],
) {
	t.Helper()

	t.Run("create get complete", func(t *testing.T) {
		ctx := context.Background()
		harness := newHarness(newRedisTestClient(t))
		request, err := harness.create(ctx, "runtime-1", randomID())
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if harness.store.state(request).Status != runtimeAsyncPending {
			t.Fatalf("initial status = %s", harness.store.state(request).Status)
		}

		got, err := harness.get(ctx, harness.store.state(request).ID)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if got == nil || harness.store.state(got).ID != harness.store.state(request).ID {
			t.Fatalf("round trip lost request: got=%v", got)
		}
		if err := harness.complete(ctx, harness.store.state(request).ID); err != nil {
			t.Fatalf("complete: %v", err)
		}

		got, err = harness.get(ctx, harness.store.state(request).ID)
		if err != nil {
			t.Fatalf("get after complete: %v", err)
		}
		if harness.store.state(got).Status != runtimeAsyncCompleted {
			t.Fatalf("status after complete = %s", harness.store.state(got).Status)
		}
		harness.assertCompleted(t, got)
	})

	t.Run("idempotent replay", func(t *testing.T) {
		ctx := context.Background()
		harness := newHarness(newRedisTestClient(t))
		const requestID = "runtime-request-replay"
		first, err := harness.create(ctx, "runtime-replay", requestID)
		if err != nil {
			t.Fatal(err)
		}
		replay, err := harness.create(ctx, "runtime-replay", requestID)
		if err != nil {
			t.Fatal(err)
		}
		firstState, replayState := harness.store.state(first), harness.store.state(replay)
		if replayState.ID != firstState.ID || !replayState.CreatedAt.Equal(firstState.CreatedAt) {
			t.Fatalf("replay state = %+v, want %+v", replayState, firstState)
		}
		if got := harness.store.rdb.ZCard(ctx, harness.store.pendingKey("runtime-replay")).Val(); got != 1 {
			t.Fatalf("pending count = %d, want 1", got)
		}
		if _, err := harness.create(ctx, "runtime-changed", requestID); !errors.Is(err, errRuntimeAsyncRequestConflict) {
			t.Fatalf("changed runtime error = %v, want conflict", err)
		}
	})

	t.Run("cross instance atomic claim", func(t *testing.T) {
		ctx := context.Background()
		rdb := newRedisTestClient(t)
		nodeA, nodeB := newHarness(rdb), newHarness(rdb)
		request, err := nodeA.create(ctx, "runtime-cross", randomID())
		if err != nil {
			t.Fatalf("node A create: %v", err)
		}

		popped, err := nodeB.pop(ctx, "runtime-cross")
		if err != nil {
			t.Fatalf("node B pop: %v", err)
		}
		if popped == nil || nodeB.store.state(popped).ID != nodeA.store.state(request).ID {
			t.Fatalf("node B popped wrong request: %+v", popped)
		}
		state := nodeB.store.state(popped)
		if state.Status != runtimeAsyncRunning || state.RunStartedAt == nil {
			t.Fatalf("popped state = %+v, want running with start time", state)
		}
		again, err := nodeB.pop(ctx, "runtime-cross")
		if err != nil {
			t.Fatalf("node B second pop: %v", err)
		}
		if again != nil {
			t.Fatalf("expected no more pending, got %+v", again)
		}
	})

	t.Run("concurrent claim has one winner", func(t *testing.T) {
		ctx := context.Background()
		harness := newHarness(newRedisTestClient(t))
		request, err := harness.create(ctx, "runtime-race", randomID())
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		assertSingleConcurrentPopWinner(t, harness.store.state(request).ID, func() (*T, error) {
			return harness.pop(ctx, "runtime-race")
		}, func(request *T) string {
			return harness.store.state(request).ID
		})
	})

	t.Run("pending timeout cannot be claimed", func(t *testing.T) {
		ctx := context.Background()
		harness := newHarness(newRedisTestClient(t))
		request, err := harness.create(ctx, "runtime-timeout", randomID())
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		state := harness.store.state(request)
		state.CreatedAt = time.Now().Add(-pendingTimeout - time.Second)
		if err := harness.store.persist(ctx, request); err != nil {
			t.Fatalf("persist rewound: %v", err)
		}

		got, err := harness.get(ctx, state.ID)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if harness.store.state(got).Status != runtimeAsyncTimeout {
			t.Fatalf("status = %s, want timeout", harness.store.state(got).Status)
		}
		popped, err := harness.pop(ctx, "runtime-timeout")
		if err != nil {
			t.Fatalf("pop after timeout: %v", err)
		}
		if popped != nil {
			t.Fatalf("expected no pending after timeout, got %+v", popped)
		}
	})
}

func TestRedisLocalSkillListStore_SharedLifecycle(t *testing.T) {
	assertRedisSingleRequestStoreContract(t, runtimeLocalSkillPendingTimeout, func(rdb *redis.Client) redisSingleRequestTestHarness[RuntimeLocalSkillListRequest] {
		store := NewRedisLocalSkillListStore(rdb)
		return redisSingleRequestTestHarness[RuntimeLocalSkillListRequest]{
			store:  store.redisRuntimeAsyncStore,
			create: store.Create,
			get:    store.Get,
			pop:    store.PopPending,
			complete: func(ctx context.Context, id string) error {
				return store.Complete(ctx, id, []protocol.RuntimeLocalSkillSummary{{
					Key: "review-helper", Name: "Review Helper", Description: "Review PRs",
					SourcePath: "~/.claude/skills/review-helper", Provider: "claude", FileCount: 2,
				}}, true)
			},
			assertCompleted: func(t *testing.T, request *RuntimeLocalSkillListRequest) {
				if len(request.Skills) != 1 || request.Skills[0].Key != "review-helper" {
					t.Fatalf("skills not persisted: %+v", request.Skills)
				}
			},
		}
	})
}

func TestRedisLocalSkillImportStore_PreservesCreatorID(t *testing.T) {
	rdb := newRedisTestClient(t)
	ctx := context.Background()
	store := NewRedisLocalSkillImportStore(rdb)

	name := "Review Helper"
	desc := "Desc"
	req, err := store.Create(ctx, LocalSkillImportRequestInput{
		RequestID: randomID(), RequestHash: randomID(),
		RuntimeID:     "runtime-1",
		CreatorID:     "user-42",
		SkillKey:      "review-helper",
		Name:          &name,
		Description:   &desc,
		Action:        LocalSkillImportActionOverwrite,
		TargetSkillID: "target-skill-99",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if req.CreatorID != "user-42" {
		t.Fatalf("creator id lost on create")
	}

	got, err := store.Get(ctx, req.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	// CreatorID is `json:"-"` on the public struct — verify the Redis envelope
	// restores it, otherwise ReportLocalSkillImportResult can't attribute the
	// created Skill to anyone.
	if got.CreatorID != "user-42" {
		t.Fatalf("creator id lost round trip: %q", got.CreatorID)
	}
	if got.Name == nil || *got.Name != name {
		t.Fatalf("name lost: %v", got.Name)
	}
	if got.Description == nil || *got.Description != desc {
		t.Fatalf("description lost: %v", got.Description)
	}
	// The overwrite intent must survive the round trip — it is consumed at
	// report time, not delivered to the daemon.
	if got.Action != LocalSkillImportActionOverwrite {
		t.Fatalf("action lost round trip: %q", got.Action)
	}
	if got.TargetSkillID != "target-skill-99" {
		t.Fatalf("target_skill_id lost round trip: %q", got.TargetSkillID)
	}
}

func TestRedisLocalSkillImportStore_ReplaysOnePendingRequest(t *testing.T) {
	rdb := newRedisTestClient(t)
	store := NewRedisLocalSkillImportStore(rdb)
	ctx := context.Background()
	input := LocalSkillImportRequestInput{
		RequestID: "redis-import-replay", RequestHash: "hash-a",
		RuntimeID: "runtime-replay", CreatorID: "creator-replay", SkillKey: "review-helper",
	}
	first, err := store.Create(ctx, input)
	if err != nil {
		t.Fatalf("create import request: %v", err)
	}
	replay, err := store.Create(ctx, input)
	if err != nil {
		t.Fatalf("replay import request: %v", err)
	}
	if replay.ID != first.ID || !replay.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("replay = id %s created %s, want id %s created %s", replay.ID, replay.CreatedAt, first.ID, first.CreatedAt)
	}
	if got := rdb.ZCard(ctx, store.pendingKey(input.RuntimeID)).Val(); got != 1 {
		t.Fatalf("pending request count = %d, want 1", got)
	}
	changed := input
	changed.RequestHash = "hash-b"
	if _, err := store.Create(ctx, changed); !errors.Is(err, errLocalSkillImportRequestConflict) {
		t.Fatalf("changed replay error = %v, want conflict", err)
	}
}

func TestRedisLocalSkillImportStore_PreservesConflict(t *testing.T) {
	rdb := newRedisTestClient(t)
	ctx := context.Background()
	store := NewRedisLocalSkillImportStore(rdb)

	req, err := store.Create(ctx, LocalSkillImportRequestInput{
		RequestID: randomID(), RequestHash: randomID(),
		RuntimeID: "runtime-1",
		CreatorID: "user-1",
		SkillKey:  "review-helper",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	info := LocalSkillImportConflict{ExistingSkillID: "skill-7", ExistingCreatedBy: "user-2", CanOverwrite: false}
	if err := store.Conflict(ctx, req.ID, info); err != nil {
		t.Fatalf("conflict: %v", err)
	}

	got, err := store.Get(ctx, req.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != runtimeAsyncConflict {
		t.Fatalf("status = %s, want conflict", got.Status)
	}
	if got.Conflict == nil {
		t.Fatalf("conflict metadata lost round trip")
	}
	if got.Conflict.ExistingSkillID != "skill-7" || got.Conflict.ExistingCreatedBy != "user-2" || got.Conflict.CanOverwrite {
		t.Fatalf("conflict metadata corrupted: %+v", got.Conflict)
	}
}

func TestRedisLocalSkillImportStore_PopPendingAcrossInstances(t *testing.T) {
	rdb := newRedisTestClient(t)
	ctx := context.Background()

	nodeA := NewRedisLocalSkillImportStore(rdb)
	nodeB := NewRedisLocalSkillImportStore(rdb)

	req, err := nodeA.Create(ctx, LocalSkillImportRequestInput{
		RequestID: randomID(), RequestHash: randomID(),
		RuntimeID: "runtime-import",
		CreatorID: "user-1",
		SkillKey:  "review-helper",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	poppedBatch, err := nodeB.PopPendingBatch(ctx, "runtime-import", 1)
	if err != nil {
		t.Fatalf("pop: %v", err)
	}
	var popped *RuntimeLocalSkillImportRequest
	if len(poppedBatch) > 0 {
		popped = poppedBatch[0]
	}
	if popped == nil || popped.ID != req.ID {
		t.Fatalf("cross-node pop failed: got %+v", popped)
	}
	if popped.Status != runtimeAsyncRunning {
		t.Fatalf("popped status = %s", popped.Status)
	}
	if popped.SkillKey != "review-helper" {
		t.Fatalf("skill_key lost: %q", popped.SkillKey)
	}
}

// Smoke test: make sure the runtime-local-skill store keys don't collide
// across runtimes — PopPending for runtime A must not see B's pending.
func TestRedisLocalSkillListStore_PerRuntimeIsolation(t *testing.T) {
	rdb := newRedisTestClient(t)
	ctx := context.Background()
	store := NewRedisLocalSkillListStore(rdb)

	if _, err := store.Create(ctx, "runtime-A", randomID()); err != nil {
		t.Fatalf("create A: %v", err)
	}
	reqB, err := store.Create(ctx, "runtime-B", randomID())
	if err != nil {
		t.Fatalf("create B: %v", err)
	}

	popped, err := store.PopPending(ctx, "runtime-B")
	if err != nil {
		t.Fatalf("pop B: %v", err)
	}
	if popped == nil || popped.ID != reqB.ID {
		t.Fatalf("pop returned wrong request: %+v", popped)
	}

	// A's request is still pending.
	ids, err := rdb.ZRange(ctx, store.pendingKey("runtime-A"), 0, -1).Result()
	if err != nil {
		t.Fatalf("zrange A: %v", err)
	}
	if len(ids) != 1 {
		t.Fatalf("expected 1 pending for A after pop(B), got %d: %v", len(ids), ids)
	}
}

// TestRedisLocalSkillListStore_PopPendingAtomicClaim pins the PR-1557 review
// fix: the claim (ZREM pending + persist running record) MUST land as one
// atomic unit. If the old two-step ordering came back ("ZRem first, SET
// second") a transient error between the two would strand the request — not
// in pending, still serialised as "pending" on disk, never re-dispatched.
//
// We verify the happy-path invariant end-to-end: after one PopPending the
// record is in "running" state AND a second PopPending on the same runtime
// returns nothing (i.e. the pending zset no longer references the id).
func TestRedisLocalSkillListStore_PopPendingAtomicClaim(t *testing.T) {
	rdb := newRedisTestClient(t)
	ctx := context.Background()
	store := NewRedisLocalSkillListStore(rdb)

	req, err := store.Create(ctx, "runtime-atomic", randomID())
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	popped, err := store.PopPending(ctx, "runtime-atomic")
	if err != nil {
		t.Fatalf("pop: %v", err)
	}
	if popped == nil || popped.ID != req.ID {
		t.Fatalf("pop returned wrong request: %+v", popped)
	}

	got, err := store.Get(ctx, req.ID)
	if err != nil {
		t.Fatalf("get after pop: %v", err)
	}
	if got.Status != runtimeAsyncRunning {
		t.Fatalf("record status = %s, want running", got.Status)
	}

	// The pending queue must no longer reference the claimed id — exposed
	// via PopPending rather than poking the zset directly.
	again, err := store.PopPending(ctx, "runtime-atomic")
	if err != nil {
		t.Fatalf("second pop: %v", err)
	}
	if again != nil {
		t.Fatalf("second pop should be empty, got %+v", again)
	}
}

func TestRedisLocalSkillImportStore_PopPendingBatch(t *testing.T) {
	rdb := newRedisTestClient(t)
	ctx := context.Background()
	store := NewRedisLocalSkillImportStore(rdb)

	// Create 5 pending imports.
	ids := make([]string, 5)
	for i := range ids {
		req, err := store.Create(ctx, LocalSkillImportRequestInput{
			RequestID: randomID(), RequestHash: randomID(),
			RuntimeID: "runtime-batch",
			CreatorID: "user-1",
			SkillKey:  fmt.Sprintf("skill-%d", i),
		})
		if err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
		ids[i] = req.ID
	}

	// Pop batch of 3 — should return 3 in creation order.
	batch, err := store.PopPendingBatch(ctx, "runtime-batch", 3)
	if err != nil {
		t.Fatalf("pop batch: %v", err)
	}
	if len(batch) != 3 {
		t.Fatalf("expected 3, got %d", len(batch))
	}
	for _, req := range batch {
		if req.Status != runtimeAsyncRunning {
			t.Fatalf("batch item status = %s, want running", req.Status)
		}
	}

	// Pop remaining — should get 2.
	rest, err := store.PopPendingBatch(ctx, "runtime-batch", 10)
	if err != nil {
		t.Fatalf("pop rest: %v", err)
	}
	if len(rest) != 2 {
		t.Fatalf("expected 2 remaining, got %d", len(rest))
	}

	// Pop again — nothing left.
	empty, err := store.PopPendingBatch(ctx, "runtime-batch", 10)
	if err != nil {
		t.Fatalf("pop empty: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("expected 0, got %d", len(empty))
	}
}

// Compile-time assertions: the stores MUST satisfy the lifecycle contracts so
// NewRouter's assignment stays type-safe.
var (
	_ runtimeListRequestStore[RuntimeLocalSkillListRequest, protocol.RuntimeLocalSkillSummary] = NewRedisLocalSkillListStore(nil)
	_ LocalSkillImportStore                                                                    = (*redisLocalSkillImportStore)(nil)
	_ runtimeListRequestStore[RuntimeLocalSkillListRequest, protocol.RuntimeLocalSkillSummary] = NewInMemoryLocalSkillListStore()
	_ LocalSkillImportStore                                                                    = (*inMemoryLocalSkillImportStore)(nil)
)
