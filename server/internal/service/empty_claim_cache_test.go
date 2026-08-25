package service

import (
	"context"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/redis/go-redis/v9"
)

func newRedisTestClient(t *testing.T) *redis.Client {
	t.Helper()
	return testutil.NewRedisTestClient(t, testutil.RedisDBService)
}

func TestEmptyClaimCache_NilSafe(t *testing.T) {
	var c *EmptyClaimCache // nil
	ctx := context.Background()

	if c.IsEmpty(ctx, "any-runtime") {
		t.Fatal("nil cache must report not-empty (cache miss)")
	}
	if v := c.CurrentVersion(ctx, "any-runtime"); v != 0 {
		t.Fatalf("nil cache CurrentVersion must be 0, got %d", v)
	}
	c.MarkEmpty(ctx, "any-runtime", 0)
	c.Bump(ctx, "any-runtime")
}

func TestNewEmptyClaimCache_NilRedisReturnsNil(t *testing.T) {
	if c := NewEmptyClaimCache(nil); c != nil {
		t.Fatalf("NewEmptyClaimCache(nil) must return nil, got %#v", c)
	}
}

func TestEmptyClaimCache_EmptyRuntimeIDIsNoOp(t *testing.T) {
	rdb := newRedisTestClient(t)
	c := NewEmptyClaimCache(rdb)
	ctx := context.Background()

	c.MarkEmpty(ctx, "", 0)
	if c.IsEmpty(ctx, "") {
		t.Fatal("empty runtime ID must not hit cache")
	}
	c.Bump(ctx, "")
}

func TestEmptyClaimCache_MarkAndIsEmptyVersionMatched(t *testing.T) {
	rdb := newRedisTestClient(t)
	c := NewEmptyClaimCache(rdb)
	ctx := context.Background()

	if c.IsEmpty(ctx, "rt-1") {
		t.Fatal("expected miss before mark")
	}
	v0 := c.CurrentVersion(ctx, "rt-1")
	c.MarkEmpty(ctx, "rt-1", v0)
	if !c.IsEmpty(ctx, "rt-1") {
		t.Fatal("expected hit when MarkEmpty version matches current")
	}
}

// Bump invalidates an empty verdict written under an earlier version.
func TestEmptyClaimCache_BumpInvalidatesPriorMark(t *testing.T) {
	rdb := newRedisTestClient(t)
	c := NewEmptyClaimCache(rdb)
	ctx := context.Background()

	v0 := c.CurrentVersion(ctx, "rt-bump")
	c.MarkEmpty(ctx, "rt-bump", v0)
	if !c.IsEmpty(ctx, "rt-bump") {
		t.Fatal("precondition: empty verdict tagged with current version should hit")
	}

	c.Bump(ctx, "rt-bump")
	if c.IsEmpty(ctx, "rt-bump") {
		t.Fatal("Bump must invalidate the prior empty verdict")
	}
}

// A mark written after its sampled version becomes stale must not be trusted.
func TestEmptyClaimCache_StaleMarkRejected(t *testing.T) {
	rdb := newRedisTestClient(t)
	c := NewEmptyClaimCache(rdb)
	ctx := context.Background()

	v0 := c.CurrentVersion(ctx, "rt-race")
	c.Bump(ctx, "rt-race")
	c.MarkEmpty(ctx, "rt-race", v0)

	if c.IsEmpty(ctx, "rt-race") {
		t.Fatal("MarkEmpty written under a pre-Bump version must be rejected on read")
	}
}

func TestEmptyClaimCache_TTL(t *testing.T) {
	rdb := newRedisTestClient(t)
	c := NewEmptyClaimCache(rdb)
	ctx := context.Background()

	c.MarkEmpty(ctx, "rt-ttl", 0)
	ttl, err := rdb.TTL(ctx, emptyClaimKey("rt-ttl")).Result()
	if err != nil {
		t.Fatalf("TTL: %v", err)
	}
	if ttl <= 0 || ttl > emptyClaimCacheTTL+time.Second {
		t.Fatalf("unexpected empty-key TTL %v (want ~%v)", ttl, emptyClaimCacheTTL)
	}
}

func TestEmptyClaimCache_RuntimeIsolation(t *testing.T) {
	rdb := newRedisTestClient(t)
	c := NewEmptyClaimCache(rdb)
	ctx := context.Background()

	vA := c.CurrentVersion(ctx, "rt-A")
	c.MarkEmpty(ctx, "rt-A", vA)
	if c.IsEmpty(ctx, "rt-B") {
		t.Fatal("marking rt-A must not affect rt-B")
	}
	c.Bump(ctx, "rt-A")
	vB := c.CurrentVersion(ctx, "rt-B")
	c.MarkEmpty(ctx, "rt-B", vB)
	if c.IsEmpty(ctx, "rt-A") {
		t.Fatal("marking rt-B must not affect rt-A")
	}
}
