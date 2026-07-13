package main

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestAutopilotEventFiltersMigrationNormalizesAndConstrainsShape(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin migration fixture: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `ALTER TABLE autopilot_trigger DROP CONSTRAINT IF EXISTS autopilot_trigger_event_filters_array`); err != nil {
		t.Fatalf("restore unconstrained event filters: %v", err)
	}
	var workspaceID, userID, runtimeID, agentID, autopilotID, triggerID string
	if err := tx.QueryRow(ctx, `INSERT INTO workspace (name, slug) VALUES ('Event Filter Migration', $1) RETURNING id`, "event-filter-"+uuid.NewString()).Scan(&workspaceID); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO "user" (name, account) VALUES ('Event Filter Migration', $1) RETURNING id`, "event-filter-"+uuid.NewString()).Scan(&userID); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO member (workspace_id, user_id, role) VALUES ($1,$2,'owner')`, workspaceID, userID); err != nil {
		t.Fatalf("insert member: %v", err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO agent_runtime (workspace_id, name, runtime_mode, provider, status) VALUES ($1,'Event Filter Migration','cloud','codex','online') RETURNING id`, workspaceID).Scan(&runtimeID); err != nil {
		t.Fatalf("insert runtime: %v", err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO agent (workspace_id, runtime_id, name, runtime_mode, runtime_config, custom_env, custom_args) VALUES ($1,$2,'Event Filter Migration','cloud','{}','{}','[]') RETURNING id`, workspaceID, runtimeID).Scan(&agentID); err != nil {
		t.Fatalf("insert agent: %v", err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO autopilot (workspace_id,title,assignee_id,created_by_type,created_by_id) VALUES ($1,'Event Filter Migration',$2,'member',$3) RETURNING id`, workspaceID, agentID, userID).Scan(&autopilotID); err != nil {
		t.Fatalf("insert autopilot: %v", err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO autopilot_trigger (autopilot_id,kind,event_filters) VALUES ($1,'webhook','{}') RETURNING id`, autopilotID).Scan(&triggerID); err != nil {
		t.Fatalf("insert invalid trigger: %v", err)
	}

	up := readMigrationFile(t, "092_require_autopilot_event_filters_array.up.sql")
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := tx.Exec(ctx, up); err != nil {
			t.Fatalf("apply event filter migration attempt %d: %v", attempt+1, err)
		}
	}
	var normalized bool
	if err := tx.QueryRow(ctx, `SELECT event_filters IS NULL FROM autopilot_trigger WHERE id=$1`, triggerID).Scan(&normalized); err != nil {
		t.Fatalf("read normalized event filters: %v", err)
	}
	if !normalized {
		t.Fatal("invalid event filters were not normalized to NULL")
	}
	if _, err := tx.Exec(ctx, `UPDATE autopilot_trigger SET event_filters='{}'::jsonb WHERE id=$1`, triggerID); err == nil {
		t.Fatal("event filter constraint accepted object")
	}
}
