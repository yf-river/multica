package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestResourceCreateRequestRetentionDeletesOnlyExpiredCompletedRows(t *testing.T) {
	pool := integrationPool(t)
	job := ResourceCreateRequestRetentionJob(pool)
	if err := job.validate(); err != nil {
		t.Fatalf("retention job spec: %v", err)
	}
	ctx := context.Background()
	var workspaceID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug) VALUES ('Request retention', $1) RETURNING id
	`, "request-retention-"+uuid.NewString()).Scan(&workspaceID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, workspaceID) })

	insert := func(completed bool, age time.Duration) string {
		t.Helper()
		key := uuid.NewString()
		resourceID := uuid.NewString()
		if completed {
			_, err := pool.Exec(ctx, `
				INSERT INTO resource_create_request (
					workspace_id, actor_id, resource_type, idempotency_key, request_hash,
					resource_id, response_body, created_at, completed_at
				) VALUES ($1, $2, 'comment', $3, $4, $5, '{}'::jsonb,
					now() - ($6 * interval '1 second'), now() - ($6 * interval '1 second'))
			`, workspaceID, uuid.NewString(), key, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", resourceID, int64(age.Seconds()))
			if err != nil {
				t.Fatal(err)
			}
		} else {
			_, err := pool.Exec(ctx, `
				INSERT INTO resource_create_request (
					workspace_id, actor_id, resource_type, idempotency_key, request_hash, created_at
				) VALUES ($1, $2, 'comment', $3, $4, now() - ($5 * interval '1 second'))
			`, workspaceID, uuid.NewString(), key, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", int64(age.Seconds()))
			if err != nil {
				t.Fatal(err)
			}
		}
		return key
	}
	expiredCompleted := insert(true, 40*24*time.Hour)
	freshCompleted := insert(true, 24*time.Hour)
	expiredIncomplete := insert(false, 40*24*time.Hour)

	result, err := job.Handler(ctx, HandlerInput{})
	if err != nil {
		t.Fatal(err)
	}
	if result.RowsAffected != 1 || result.Result["stale_incomplete"] != int64(1) {
		t.Fatalf("result = %#v, want deleted=1 stale_incomplete=1", result)
	}
	for key, want := range map[string]int64{
		expiredCompleted:  0,
		freshCompleted:    1,
		expiredIncomplete: 1,
	} {
		var count int64
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM resource_create_request WHERE idempotency_key = $1`, key).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != want {
			t.Fatalf("request %s count=%d, want %d", key, count, want)
		}
	}
}
