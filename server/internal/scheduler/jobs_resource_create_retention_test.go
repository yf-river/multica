package scheduler

import (
	"context"
	"fmt"
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
	insertSkillImport := func(completed bool, age time.Duration) string {
		t.Helper()
		key := uuid.NewString()
		if completed {
			_, err := pool.Exec(ctx, `
				INSERT INTO skill_import_request (
					workspace_id, actor_id, idempotency_key, request_hash,
					response_status, response_body, created_at, completed_at
				) VALUES ($1, $2, $3, $4, 201, '{}'::jsonb,
					now() - ($5 * interval '1 second'), now() - ($5 * interval '1 second'))
			`, workspaceID, uuid.NewString(), key, "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", int64(age.Seconds()))
			if err != nil {
				t.Fatal(err)
			}
		} else {
			_, err := pool.Exec(ctx, `
				INSERT INTO skill_import_request (
					workspace_id, actor_id, idempotency_key, request_hash, created_at
				) VALUES ($1, $2, $3, $4, now() - ($5 * interval '1 second'))
			`, workspaceID, uuid.NewString(), key, "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd", int64(age.Seconds()))
			if err != nil {
				t.Fatal(err)
			}
		}
		return key
	}
	skillExpiredCompleted := insertSkillImport(true, 40*24*time.Hour)
	skillFreshCompleted := insertSkillImport(true, 24*time.Hour)
	skillExpiredIncomplete := insertSkillImport(false, 40*24*time.Hour)
	var autopilotID, triggerID string
	actorID := uuid.NewString()
	if err := pool.QueryRow(ctx, `
		INSERT INTO autopilot (
			workspace_id, title, assignee_id, status, execution_mode,
			created_by_type, created_by_id, assignee_type
		) VALUES ($1, 'Retention fixture', $2, 'paused', 'run_only', 'member', $2, 'agent')
		RETURNING id
	`, workspaceID, actorID).Scan(&autopilotID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO autopilot_trigger (autopilot_id, kind, webhook_token)
		VALUES ($1, 'webhook', $2) RETURNING id
	`, autopilotID, "retention-token-"+uuid.NewString()).Scan(&triggerID); err != nil {
		t.Fatal(err)
	}
	insertRotation := func(completed bool, age time.Duration) string {
		t.Helper()
		key := uuid.NewString()
		if completed {
			_, err := pool.Exec(ctx, `
				INSERT INTO autopilot_trigger_rotation_request (
					workspace_id, actor_id, idempotency_key, trigger_id, request_hash,
					response_status, response_body, created_at, completed_at
				) VALUES ($1, $2, $3, $4, $5, 200, '{}'::jsonb,
					now() - ($6 * interval '1 second'), now() - ($6 * interval '1 second'))
			`, workspaceID, actorID, key, triggerID, "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", int64(age.Seconds()))
			if err != nil {
				t.Fatal(err)
			}
		} else {
			_, err := pool.Exec(ctx, `
				INSERT INTO autopilot_trigger_rotation_request (
					workspace_id, actor_id, idempotency_key, trigger_id, request_hash, created_at
				) VALUES ($1, $2, $3, $4, $5, now() - ($6 * interval '1 second'))
			`, workspaceID, actorID, key, triggerID, "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff", int64(age.Seconds()))
			if err != nil {
				t.Fatal(err)
			}
		}
		return key
	}
	rotationExpiredCompleted := insertRotation(true, 40*24*time.Hour)
	rotationFreshCompleted := insertRotation(true, 24*time.Hour)
	rotationExpiredIncomplete := insertRotation(false, 40*24*time.Hour)

	result, err := job.Handler(ctx, HandlerInput{})
	if err != nil {
		t.Fatal(err)
	}
	if result.RowsAffected != 3 || result.Result["stale_incomplete"] != int64(3) {
		t.Fatalf("result = %#v, want deleted=3 stale_incomplete=3", result)
	}
	for table, records := range map[string]map[string]int64{
		"resource_create_request": {
			expiredCompleted: 0, freshCompleted: 1, expiredIncomplete: 1,
		},
		"skill_import_request": {
			skillExpiredCompleted: 0, skillFreshCompleted: 1, skillExpiredIncomplete: 1,
		},
		"autopilot_trigger_rotation_request": {
			rotationExpiredCompleted: 0, rotationFreshCompleted: 1, rotationExpiredIncomplete: 1,
		},
	} {
		for key, want := range records {
			var count int64
			if err := pool.QueryRow(ctx, fmt.Sprintf(`SELECT count(*) FROM %s WHERE idempotency_key = $1`, table), key).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != want {
				t.Fatalf("%s request %s count=%d, want %d", table, key, count, want)
			}
		}
	}
}
