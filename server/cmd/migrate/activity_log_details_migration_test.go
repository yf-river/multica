package main

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestActivityLogDetailsMigrationNormalizesAndConstrainsObject(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin migration fixture: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `ALTER TABLE activity_log DROP CONSTRAINT IF EXISTS activity_log_details_is_object`); err != nil {
		t.Fatalf("restore unconstrained activity details: %v", err)
	}
	var workspaceID, activityID string
	if err := tx.QueryRow(ctx, `INSERT INTO workspace (name,slug) VALUES ('Activity Details Migration',$1) RETURNING id`, "activity-details-"+uuid.NewString()).Scan(&workspaceID); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO activity_log (workspace_id,actor_type,action,details) VALUES ($1,'system','migration_test','[]') RETURNING id`, workspaceID).Scan(&activityID); err != nil {
		t.Fatalf("insert invalid activity details: %v", err)
	}
	up := readMigrationFile(t, "098_require_activity_log_details_object.up.sql")
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := tx.Exec(ctx, up); err != nil {
			t.Fatalf("apply activity details migration attempt %d: %v", attempt+1, err)
		}
	}
	var shape string
	if err := tx.QueryRow(ctx, `SELECT jsonb_typeof(details) FROM activity_log WHERE id=$1`, activityID).Scan(&shape); err != nil {
		t.Fatalf("read normalized activity details: %v", err)
	}
	if shape != "object" {
		t.Fatalf("normalized activity details shape = %s", shape)
	}
	if _, err := tx.Exec(ctx, `UPDATE activity_log SET details='[]'::jsonb WHERE id=$1`, activityID); err == nil {
		t.Fatal("activity details constraint accepted array")
	}
}
