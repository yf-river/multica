package main

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestAgentCustomStateMigrationNormalizesAndConstrainsShapes(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin migration fixture: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
		ALTER TABLE agent
		DROP CONSTRAINT IF EXISTS agent_custom_env_string_object,
		DROP CONSTRAINT IF EXISTS agent_custom_args_string_array
	`); err != nil {
		t.Fatalf("restore unconstrained agent custom state: %v", err)
	}

	var workspaceID, runtimeID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO workspace (name, slug)
		VALUES ('Agent Custom State Migration', $1)
		RETURNING id
	`, "agent-custom-state-migration-"+uuid.NewString()).Scan(&workspaceID); err != nil {
		t.Fatalf("insert migration workspace: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO agent_runtime (workspace_id, name, runtime_mode, provider, status)
		VALUES ($1, 'Agent Custom State Migration', 'cloud', 'codex', 'online')
		RETURNING id
	`, workspaceID).Scan(&runtimeID); err != nil {
		t.Fatalf("insert migration runtime: %v", err)
	}

	var agentID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO agent (
			workspace_id, runtime_id, name, runtime_mode, runtime_config,
			custom_env, custom_args
		) VALUES (
			$1, $2, 'Agent Custom State Migration', 'cloud', '{}'::jsonb,
			'{"VALID":"value","INVALID":1}'::jsonb, '["valid",1]'::jsonb
		) RETURNING id
	`, workspaceID, runtimeID).Scan(&agentID); err != nil {
		t.Fatalf("insert invalid agent custom state: %v", err)
	}

	migration := readMigrationFile(t, "036_require_agent_custom_state_shapes.up.sql")
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := tx.Exec(ctx, migration); err != nil {
			t.Fatalf("apply agent custom state migration attempt %d: %v", attempt+1, err)
		}
	}

	var customEnv, customArgs string
	if err := tx.QueryRow(ctx, `
		SELECT custom_env::text, custom_args::text FROM agent WHERE id = $1
	`, agentID).Scan(&customEnv, &customArgs); err != nil {
		t.Fatalf("read normalized agent custom state: %v", err)
	}
	if customEnv != "{}" || customArgs != "[]" {
		t.Fatalf("migrated custom state = env:%s args:%s, want {} and []", customEnv, customArgs)
	}

	var constraintCount int
	if err := tx.QueryRow(ctx, `
		SELECT count(*)::int FROM pg_constraint
		WHERE conrelid = 'agent'::regclass
		  AND conname IN ('agent_custom_env_string_object', 'agent_custom_args_string_array')
	`).Scan(&constraintCount); err != nil {
		t.Fatalf("read agent custom state constraints: %v", err)
	}
	if constraintCount != 2 {
		t.Fatalf("agent custom state constraint count = %d, want 2", constraintCount)
	}

	for _, probe := range []struct {
		name string
		sql  string
	}{
		{name: "env container", sql: `UPDATE agent SET custom_env = '[]'::jsonb WHERE id = $1`},
		{name: "env value", sql: `UPDATE agent SET custom_env = '{"KEY":1}'::jsonb WHERE id = $1`},
		{name: "args container", sql: `UPDATE agent SET custom_args = '{}'::jsonb WHERE id = $1`},
		{name: "args value", sql: `UPDATE agent SET custom_args = '[1]'::jsonb WHERE id = $1`},
	} {
		if _, err := tx.Exec(ctx, `SAVEPOINT invalid_agent_custom_state`); err != nil {
			t.Fatalf("%s: create savepoint: %v", probe.name, err)
		}
		if _, err := tx.Exec(ctx, probe.sql, agentID); err == nil {
			t.Fatalf("%s constraint accepted invalid state", probe.name)
		}
		if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT invalid_agent_custom_state`); err != nil {
			t.Fatalf("%s: rollback savepoint: %v", probe.name, err)
		}
	}
}
