package main

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestPlaygroundVariablesMigrationNormalizesAndConstrainsObject(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `ALTER TABLE agent_playground_input DROP CONSTRAINT IF EXISTS agent_playground_input_variables_object`); err != nil {
		t.Fatal(err)
	}
	var workspaceID string
	if err := tx.QueryRow(ctx, `INSERT INTO workspace (name,slug) VALUES ('Playground Variables Migration',$1) RETURNING id`, "playground-variables-"+uuid.NewString()).Scan(&workspaceID); err != nil {
		t.Fatal(err)
	}
	var experimentID string
	if err := tx.QueryRow(ctx, `INSERT INTO agent_playground_experiment (workspace_id,name) VALUES ($1,'Playground Variables Migration') RETURNING id`, workspaceID).Scan(&experimentID); err != nil {
		t.Fatal(err)
	}
	var inputID string
	if err := tx.QueryRow(ctx, `INSERT INTO agent_playground_input (experiment_id,workspace_id,row_index,input,variables) VALUES ($1,$2,0,'input','null'::jsonb) RETURNING id`, experimentID, workspaceID).Scan(&inputID); err != nil {
		t.Fatal(err)
	}
	migration := readMigrationFile(t, "041_require_playground_variables_object.up.sql")
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := tx.Exec(ctx, migration); err != nil {
			t.Fatalf("apply attempt %d: %v", attempt+1, err)
		}
	}
	var variables string
	if err := tx.QueryRow(ctx, `SELECT variables::text FROM agent_playground_input WHERE id=$1`, inputID).Scan(&variables); err != nil {
		t.Fatal(err)
	}
	if variables != "{}" {
		t.Fatalf("migrated variables=%s, want {}", variables)
	}
	if _, err := tx.Exec(ctx, `SAVEPOINT invalid_playground_variables`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE agent_playground_input SET variables='[]'::jsonb WHERE id=$1`, inputID); err == nil {
		t.Fatal("constraint accepted array")
	}
	if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT invalid_playground_variables`); err != nil {
		t.Fatal(err)
	}
}
