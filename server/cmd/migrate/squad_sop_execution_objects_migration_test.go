package main

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestSquadSOPExecutionObjectsMigrationNormalizesAndConstrainsShapes(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin migration fixture: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
		ALTER TABLE squad DROP CONSTRAINT IF EXISTS squad_sop_profile_object;
		ALTER TABLE squad_sop_run DROP CONSTRAINT IF EXISTS squad_sop_run_profile_is_object;
		ALTER TABLE squad_sop_step_event DROP CONSTRAINT IF EXISTS squad_sop_step_event_evidence_is_object;
	`); err != nil {
		t.Fatalf("restore unconstrained Squad SOP objects: %v", err)
	}

	suffix := uuid.NewString()
	var userID, workspaceID, runtimeID, agentID, issueID, squadID, runID, eventID string
	if err := tx.QueryRow(ctx, `INSERT INTO "user" (name,account) VALUES ('SOP Migration',$1) RETURNING id`, "sop-migration-"+suffix).Scan(&userID); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO workspace (name,slug) VALUES ('SOP Migration',$1) RETURNING id`, "sop-migration-"+suffix).Scan(&workspaceID); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO agent_runtime (workspace_id,name,runtime_mode,provider) VALUES ($1,'SOP Migration','cloud','test') RETURNING id`, workspaceID).Scan(&runtimeID); err != nil {
		t.Fatalf("insert runtime: %v", err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO agent (workspace_id,name,runtime_mode,runtime_id,owner_id) VALUES ($1,'SOP Migration','cloud',$2,$3) RETURNING id`, workspaceID, runtimeID, userID).Scan(&agentID); err != nil {
		t.Fatalf("insert agent: %v", err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO issue (workspace_id,title,creator_type,creator_id) VALUES ($1,'SOP Migration','member',$2) RETURNING id`, workspaceID, userID).Scan(&issueID); err != nil {
		t.Fatalf("insert issue: %v", err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO squad (workspace_id,name,leader_id,creator_id,sop_profile) VALUES ($1,'SOP Migration',$2,$3,'[]') RETURNING id`, workspaceID, agentID, userID).Scan(&squadID); err != nil {
		t.Fatalf("insert invalid squad profile: %v", err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO squad_sop_run (workspace_id,issue_id,squad_id,profile) VALUES ($1,$2,$3,'[]') RETURNING id`, workspaceID, issueID, squadID).Scan(&runID); err != nil {
		t.Fatalf("insert invalid SOP run profile: %v", err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO squad_sop_step_event (run_id,workspace_id,issue_id,squad_id,step_key,event_type,evidence) VALUES ($1,$2,$3,$4,'step','步骤开始','[]') RETURNING id`, runID, workspaceID, issueID, squadID).Scan(&eventID); err != nil {
		t.Fatalf("insert invalid SOP event evidence: %v", err)
	}

	up := readMigrationFile(t, "097_require_squad_sop_execution_objects.up.sql")
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := tx.Exec(ctx, up); err != nil {
			t.Fatalf("apply Squad SOP object migration attempt %d: %v", attempt+1, err)
		}
	}
	for _, test := range []struct{ table, field, id string }{
		{"squad", "sop_profile", squadID},
		{"squad_sop_run", "profile", runID},
		{"squad_sop_step_event", "evidence", eventID},
	} {
		var shape string
		if err := tx.QueryRow(ctx, `SELECT jsonb_typeof(`+test.field+`) FROM `+test.table+` WHERE id=$1`, test.id).Scan(&shape); err != nil {
			t.Fatalf("read %s.%s: %v", test.table, test.field, err)
		}
		if shape != "object" {
			t.Fatalf("%s.%s shape = %s", test.table, test.field, shape)
		}
		if _, err := tx.Exec(ctx, `SAVEPOINT invalid_sop_shape`); err != nil {
			t.Fatalf("create savepoint: %v", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE `+test.table+` SET `+test.field+`='[]'::jsonb WHERE id=$1`, test.id); err == nil {
			t.Fatalf("%s.%s accepted array", test.table, test.field)
		}
		if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT invalid_sop_shape`); err != nil {
			t.Fatalf("restore rejected SOP shape: %v", err)
		}
	}
}
