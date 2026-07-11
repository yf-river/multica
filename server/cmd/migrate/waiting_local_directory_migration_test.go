package main

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestWaitingLocalDirectoryMigrationRequeuesAndRemovesState(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin migration fixture: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
		ALTER TABLE agent_task_queue ADD COLUMN IF NOT EXISTS wait_reason text;
		ALTER TABLE agent_task_queue DROP CONSTRAINT IF EXISTS agent_task_queue_status_check;
		ALTER TABLE agent_task_queue ADD CONSTRAINT agent_task_queue_status_check
			CHECK (status IN ('queued', 'dispatched', 'running', 'waiting_local_directory', 'completed', 'failed', 'cancelled'));
		ALTER TABLE agent_playground_result DROP CONSTRAINT IF EXISTS agent_playground_result_status_check;
		ALTER TABLE agent_playground_result ADD CONSTRAINT agent_playground_result_status_check
			CHECK (status IN ('pending', 'queued', 'dispatched', 'running', 'waiting_local_directory', 'completed', 'failed', 'cancelled'));
		ALTER TABLE agent_playground_judgement DROP CONSTRAINT IF EXISTS agent_playground_judgement_status_check;
		ALTER TABLE agent_playground_judgement ADD CONSTRAINT agent_playground_judgement_status_check
			CHECK (status IN ('pending', 'queued', 'dispatched', 'running', 'waiting_local_directory', 'completed', 'failed', 'cancelled'));
	`); err != nil {
		t.Fatalf("restore removed waiting state: %v", err)
	}

	suffix := uuid.NewString()
	var workspaceID, runtimeID, agentID, taskID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, issue_prefix)
		VALUES ('Waiting State Migration', $1, 'WSM') RETURNING id
	`, "waiting-state-migration-"+suffix).Scan(&workspaceID); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO agent_runtime (workspace_id, name, runtime_mode, provider, status)
		VALUES ($1, 'Waiting State Runtime', 'cloud', 'codex', 'online') RETURNING id
	`, workspaceID).Scan(&runtimeID); err != nil {
		t.Fatalf("insert runtime: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO agent (workspace_id, name, runtime_mode, runtime_config, runtime_id)
		VALUES ($1, 'Waiting State Agent', 'cloud', '{}'::jsonb, $2) RETURNING id
	`, workspaceID, runtimeID).Scan(&agentID); err != nil {
		t.Fatalf("insert agent: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO agent_task_queue (
			agent_id, runtime_id, status, dispatched_at, started_at, wait_reason
		) VALUES ($1, $2, 'waiting_local_directory', now(), now(), 'stale path lock')
		RETURNING id
	`, agentID, runtimeID).Scan(&taskID); err != nil {
		t.Fatalf("insert waiting task: %v", err)
	}

	up := readMigrationFile(t, "022_remove_waiting_local_directory.up.sql")
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := tx.Exec(ctx, up); err != nil {
			t.Fatalf("apply migration attempt %d: %v", attempt+1, err)
		}
	}

	var status, gotRuntimeID string
	var dispatchedAtNil, startedAtNil bool
	if err := tx.QueryRow(ctx, `
		SELECT status, runtime_id::text, dispatched_at IS NULL, started_at IS NULL
		FROM agent_task_queue WHERE id = $1
	`, taskID).Scan(&status, &gotRuntimeID, &dispatchedAtNil, &startedAtNil); err != nil {
		t.Fatalf("read normalized task: %v", err)
	}
	if status != "queued" || gotRuntimeID != runtimeID || !dispatchedAtNil || !startedAtNil {
		t.Fatalf("normalized task = status %q runtime %q dispatched_nil %v started_nil %v", status, gotRuntimeID, dispatchedAtNil, startedAtNil)
	}

	var waitReasonColumns int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'agent_task_queue' AND column_name = 'wait_reason'
	`).Scan(&waitReasonColumns); err != nil {
		t.Fatalf("check wait_reason removal: %v", err)
	}
	if waitReasonColumns != 0 {
		t.Fatal("agent_task_queue.wait_reason still exists")
	}

	for _, constraint := range []string{
		"agent_task_queue_status_check",
		"agent_playground_result_status_check",
		"agent_playground_judgement_status_check",
	} {
		var definition string
		if err := tx.QueryRow(ctx, `SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conname = $1`, constraint).Scan(&definition); err != nil {
			t.Fatalf("read %s: %v", constraint, err)
		}
		if strings.Contains(definition, "waiting_local_directory") {
			t.Fatalf("%s still accepts removed state: %s", constraint, definition)
		}
	}
}
