package main

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestAgentRuntimeMetadataMigrationNormalizesAndConstrainsObject(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin migration fixture: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `ALTER TABLE agent_runtime DROP CONSTRAINT IF EXISTS agent_runtime_metadata_object`); err != nil {
		t.Fatalf("restore unconstrained runtime metadata: %v", err)
	}

	var workspaceID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO workspace (name, slug)
		VALUES ('Runtime Metadata Migration', $1)
		RETURNING id
	`, "runtime-metadata-migration-"+uuid.NewString()).Scan(&workspaceID); err != nil {
		t.Fatalf("insert migration workspace: %v", err)
	}

	var runtimeID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, name, runtime_mode, provider, status, metadata
		) VALUES ($1, 'Runtime Metadata Migration', 'cloud', 'codex', 'online', '["invalid"]'::jsonb)
		RETURNING id
	`, workspaceID).Scan(&runtimeID); err != nil {
		t.Fatalf("insert array runtime metadata: %v", err)
	}

	migration := readMigrationFile(t, "037_require_agent_runtime_metadata_object.up.sql")
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := tx.Exec(ctx, migration); err != nil {
			t.Fatalf("apply runtime metadata migration attempt %d: %v", attempt+1, err)
		}
	}

	var metadata string
	if err := tx.QueryRow(ctx, `SELECT metadata::text FROM agent_runtime WHERE id = $1`, runtimeID).Scan(&metadata); err != nil {
		t.Fatalf("read normalized runtime metadata: %v", err)
	}
	if metadata != "{}" {
		t.Fatalf("migrated runtime metadata = %s, want {}", metadata)
	}

	var constraintCount int
	if err := tx.QueryRow(ctx, `
		SELECT count(*)::int FROM pg_constraint
		WHERE conrelid = 'agent_runtime'::regclass
		  AND conname = 'agent_runtime_metadata_object'
	`).Scan(&constraintCount); err != nil {
		t.Fatalf("read runtime metadata constraint: %v", err)
	}
	if constraintCount != 1 {
		t.Fatalf("runtime metadata constraint count = %d, want 1", constraintCount)
	}

	if _, err := tx.Exec(ctx, `SAVEPOINT invalid_runtime_metadata`); err != nil {
		t.Fatalf("create constraint probe savepoint: %v", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE agent_runtime SET metadata = '[]'::jsonb WHERE id = $1`, runtimeID); err == nil {
		t.Fatal("runtime metadata constraint accepted an array")
	}
	if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT invalid_runtime_metadata`); err != nil {
		t.Fatalf("rollback constraint probe: %v", err)
	}
}
