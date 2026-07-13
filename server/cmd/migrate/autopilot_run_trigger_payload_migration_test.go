package main

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestAutopilotRunTriggerPayloadMigrationNormalizesAndConstrainsObject(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin migration fixture: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `ALTER TABLE autopilot_run DROP CONSTRAINT IF EXISTS autopilot_run_trigger_payload_is_object`); err != nil {
		t.Fatalf("restore unconstrained trigger payload: %v", err)
	}

	suffix := uuid.NewString()
	var userID, workspaceID, runtimeID, agentID, autopilotID, runID string
	if err := tx.QueryRow(ctx, `INSERT INTO "user" (name,account) VALUES ('Autopilot Payload Migration',$1) RETURNING id`, "autopilot-payload-"+suffix).Scan(&userID); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO workspace (name,slug) VALUES ('Autopilot Payload Migration',$1) RETURNING id`, "autopilot-payload-"+suffix).Scan(&workspaceID); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO agent_runtime (workspace_id,name,runtime_mode,provider) VALUES ($1,'Autopilot Payload Migration','cloud','test') RETURNING id`, workspaceID).Scan(&runtimeID); err != nil {
		t.Fatalf("insert runtime: %v", err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO agent (workspace_id,name,runtime_mode,runtime_id,owner_id) VALUES ($1,'Autopilot Payload Migration','cloud',$2,$3) RETURNING id`, workspaceID, runtimeID, userID).Scan(&agentID); err != nil {
		t.Fatalf("insert agent: %v", err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO autopilot (workspace_id,title,assignee_id,created_by_type,created_by_id) VALUES ($1,'Autopilot Payload Migration',$2,'member',$3) RETURNING id`, workspaceID, agentID, userID).Scan(&autopilotID); err != nil {
		t.Fatalf("insert autopilot: %v", err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO autopilot_run (autopilot_id,source,status,trigger_payload) VALUES ($1,'manual','running','[]') RETURNING id`, autopilotID).Scan(&runID); err != nil {
		t.Fatalf("insert invalid trigger payload: %v", err)
	}

	up := readMigrationFile(t, "099_require_autopilot_run_trigger_payload_object.up.sql")
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := tx.Exec(ctx, up); err != nil {
			t.Fatalf("apply trigger payload migration attempt %d: %v", attempt+1, err)
		}
	}
	var normalizedIsNull bool
	if err := tx.QueryRow(ctx, `SELECT trigger_payload IS NULL FROM autopilot_run WHERE id=$1`, runID).Scan(&normalizedIsNull); err != nil {
		t.Fatalf("read normalized trigger payload: %v", err)
	}
	if !normalizedIsNull {
		t.Fatal("invalid trigger payload was not normalized to NULL")
	}
	if _, err := tx.Exec(ctx, `UPDATE autopilot_run SET trigger_payload='[]'::jsonb WHERE id=$1`, runID); err == nil {
		t.Fatal("trigger payload constraint accepted array")
	}
}
