package middleware

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func seedOwnerLookupUser(t *testing.T, queries *db.Queries) string {
	t.Helper()
	ctx := context.Background()
	stamp := time.Now().UnixNano()
	user, err := queries.CreateUser(ctx, db.CreateUserParams{
		Name:    "owner-lookup",
		Account: pgtypeUniqueAccount(stamp),
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	t.Cleanup(func() {
		_ = user
	})
	return util.UUIDToString(user.ID)
}

func pgtypeUniqueAccount(stamp int64) string {
	return time.Unix(0, stamp).UTC().Format("20060102T150405.000000000") + "@owner-lookup.test"
}

func TestOwnerLookupFor_NilQueries(t *testing.T) {
	if got := ownerLookupFor(nil); got != nil {
		t.Fatalf("ownerLookupFor(nil) must return nil, got %T", got)
	}
}

func TestOwnerLookupFor_ExistingUser(t *testing.T) {
	pool := openPool(t)
	defer pool.Close()
	queries := db.New(pool)

	userID := seedOwnerLookupUser(t, queries)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM "user" WHERE id = $1`, userID)
	})

	lookup := ownerLookupFor(queries)
	exists, err := lookup(context.Background(), userID)
	if err != nil {
		t.Fatalf("lookup returned error: %v", err)
	}
	if !exists {
		t.Fatalf("expected user to be found")
	}
}

func TestOwnerLookupFor_MissingUser(t *testing.T) {
	pool := openPool(t)
	defer pool.Close()
	queries := db.New(pool)

	lookup := ownerLookupFor(queries)
	exists, err := lookup(context.Background(), "00000000-0000-0000-0000-0000deadbeef")
	if err != nil {
		t.Fatalf("missing user must NOT surface as a lookup error, got %v", err)
	}
	if exists {
		t.Fatalf("expected lookup to report user-not-found")
	}
}

func TestOwnerLookupFor_MalformedOwnerID(t *testing.T) {
	pool := openPool(t)
	defer pool.Close()
	queries := db.New(pool)

	lookup := ownerLookupFor(queries)
	exists, err := lookup(context.Background(), "not-a-uuid")
	if err != nil {
		t.Fatalf("malformed owner_id must NOT surface as a lookup error, got %v", err)
	}
	if exists {
		t.Fatalf("malformed owner_id must report user-not-found")
	}
}

func TestOwnerLookupFor_DBError(t *testing.T) {
	pool := openPool(t)
	queries := db.New(pool)
	pool.Close()

	lookup := ownerLookupFor(queries)
	exists, err := lookup(context.Background(), "00000000-0000-0000-0000-000000000001")
	if err == nil {
		t.Fatal("expected real DB error to surface, got nil")
	}
	if exists {
		t.Fatal("DB error must not report user-found")
	}
	if errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("DB error must not look like ErrNoRows, got %v", err)
	}
}
