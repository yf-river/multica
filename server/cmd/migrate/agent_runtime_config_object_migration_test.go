package main

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestAgentRuntimeConfigObjectMigrationNormalizesAndConstrainsShape(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin migration fixture: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `ALTER TABLE agent DROP CONSTRAINT IF EXISTS agent_runtime_config_object`); err != nil {
		t.Fatalf("restore unconstrained agent runtime config: %v", err)
	}

	var workspaceID, runtimeID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO workspace (name, slug)
		VALUES ('Agent Runtime Config Migration', $1)
		RETURNING id
	`, "agent-runtime-config-migration-"+uuid.NewString()).Scan(&workspaceID); err != nil {
		t.Fatalf("insert migration workspace: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO agent_runtime (workspace_id, name, runtime_mode, provider, status)
		VALUES ($1, 'Agent Runtime Config Migration', 'cloud', 'codex', 'online')
		RETURNING id
	`, workspaceID).Scan(&runtimeID); err != nil {
		t.Fatalf("insert migration runtime: %v", err)
	}

	var agentID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO agent (workspace_id, runtime_id, name, runtime_mode, runtime_config)
		VALUES ($1, $2, 'Agent Runtime Config Migration', 'cloud', '["invalid"]'::jsonb)
		RETURNING id
	`, workspaceID, runtimeID).Scan(&agentID); err != nil {
		t.Fatalf("insert array-shaped agent runtime config: %v", err)
	}

	migration := readMigrationFile(t, "035_require_agent_runtime_config_object.up.sql")
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := tx.Exec(ctx, migration); err != nil {
			t.Fatalf("apply agent runtime config migration attempt %d: %v", attempt+1, err)
		}
	}

	var config string
	if err := tx.QueryRow(ctx, `SELECT runtime_config::text FROM agent WHERE id = $1`, agentID).Scan(&config); err != nil {
		t.Fatalf("read normalized agent runtime config: %v", err)
	}
	if config != "{}" {
		t.Fatalf("migrated agent runtime config = %s, want {}", config)
	}

	var constraintCount int
	if err := tx.QueryRow(ctx, `
		SELECT count(*)::int
		FROM pg_constraint
		WHERE conrelid = 'agent'::regclass
		  AND conname = 'agent_runtime_config_object'
	`).Scan(&constraintCount); err != nil {
		t.Fatalf("read agent runtime config constraint: %v", err)
	}
	if constraintCount != 1 {
		t.Fatalf("agent runtime config constraint count = %d, want 1", constraintCount)
	}

	if _, err := tx.Exec(ctx, `SAVEPOINT invalid_agent_runtime_config`); err != nil {
		t.Fatalf("create constraint probe savepoint: %v", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE agent SET runtime_config = '[]'::jsonb WHERE id = $1`, agentID); err == nil {
		t.Fatal("agent runtime config constraint accepted an array")
	}
	if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT invalid_agent_runtime_config`); err != nil {
		t.Fatalf("rollback constraint probe: %v", err)
	}
}
