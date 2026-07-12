package main

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestSquadSOPProfileMigrationNormalizesAndConstrainsObject(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin migration fixture: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `ALTER TABLE squad DROP CONSTRAINT IF EXISTS squad_sop_profile_object`); err != nil {
		t.Fatalf("restore unconstrained SOP profile: %v", err)
	}
	var creatorID string
	if err := tx.QueryRow(ctx, `INSERT INTO "user" (name, account) VALUES ('Squad SOP Migration',$1) RETURNING id`, "squad-sop-migration-"+uuid.NewString()).Scan(&creatorID); err != nil {
		t.Fatalf("insert migration user: %v", err)
	}
	var workspaceID string
	if err := tx.QueryRow(ctx, `INSERT INTO workspace (name, slug) VALUES ('Squad SOP Migration',$1) RETURNING id`, "squad-sop-migration-"+uuid.NewString()).Scan(&workspaceID); err != nil {
		t.Fatalf("insert migration workspace: %v", err)
	}
	var runtimeID string
	if err := tx.QueryRow(ctx, `INSERT INTO agent_runtime (workspace_id,name,runtime_mode,provider,status) VALUES ($1,'Squad SOP Migration','cloud','codex','online') RETURNING id`, workspaceID).Scan(&runtimeID); err != nil {
		t.Fatalf("insert migration runtime: %v", err)
	}
	var leaderID string
	if err := tx.QueryRow(ctx, `INSERT INTO agent (workspace_id,runtime_id,name,runtime_mode,owner_id) VALUES ($1,$2,'Squad SOP Migration','cloud',$3) RETURNING id`, workspaceID, runtimeID, creatorID).Scan(&leaderID); err != nil {
		t.Fatalf("insert migration leader: %v", err)
	}
	var squadID string
	if err := tx.QueryRow(ctx, `INSERT INTO squad (workspace_id, name, leader_id, creator_id, sop_profile) VALUES ($1,$2,$3,$4,'null'::jsonb) RETURNING id`, workspaceID, "SOP migration "+uuid.NewString(), leaderID, creatorID).Scan(&squadID); err != nil {
		t.Fatalf("insert null SOP profile: %v", err)
	}
	migration := readMigrationFile(t, "039_require_squad_sop_profile_object.up.sql")
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := tx.Exec(ctx, migration); err != nil {
			t.Fatalf("apply migration attempt %d: %v", attempt+1, err)
		}
	}
	var profile string
	if err := tx.QueryRow(ctx, `SELECT sop_profile::text FROM squad WHERE id=$1`, squadID).Scan(&profile); err != nil {
		t.Fatalf("read profile: %v", err)
	}
	if profile != "{}" {
		t.Fatalf("migrated profile = %s, want {}", profile)
	}
	if _, err := tx.Exec(ctx, `SAVEPOINT invalid_sop_profile`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE squad SET sop_profile='[]'::jsonb WHERE id=$1`, squadID); err == nil {
		t.Fatal("constraint accepted array")
	}
	if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT invalid_sop_profile`); err != nil {
		t.Fatal(err)
	}
}
