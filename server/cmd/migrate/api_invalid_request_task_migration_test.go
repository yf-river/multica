package main

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestAPIInvalidRequestTaskMigrationClassifiesOnlyMatchingLegacyRows(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin migration fixture: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	suffix := uuid.NewString()
	var userID, workspaceID, runtimeID, agentID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO "user" (name, account)
		VALUES ('API Invalid Request Migration Test', $1) RETURNING id
	`, "api-invalid-request-migration-"+suffix+"@test.invalid").Scan(&userID); err != nil {
		t.Fatalf("insert migration user: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, issue_prefix)
		VALUES ('API Invalid Request Migration Test', $1, 'AIR') RETURNING id
	`, "api-invalid-request-migration-"+suffix).Scan(&workspaceID); err != nil {
		t.Fatalf("insert migration workspace: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO agent_runtime (workspace_id, name, runtime_mode, provider, status, owner_id)
		VALUES ($1, 'API Invalid Request Migration Runtime', 'cloud', 'claude', 'online', $2)
		RETURNING id
	`, workspaceID, userID).Scan(&runtimeID); err != nil {
		t.Fatalf("insert migration runtime: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO agent (workspace_id, name, runtime_mode, runtime_config, runtime_id, owner_id)
		VALUES ($1, 'API Invalid Request Migration Agent', 'cloud', '{}'::jsonb, $2, $3)
		RETURNING id
	`, workspaceID, runtimeID, userID).Scan(&agentID); err != nil {
		t.Fatalf("insert migration agent: %v", err)
	}

	insertTask := func(errorText string) string {
		t.Helper()
		var id string
		if err := tx.QueryRow(ctx, `
			INSERT INTO agent_task_queue (
				agent_id, runtime_id, status, started_at, completed_at,
				failure_reason, error
			) VALUES ($1, $2, 'failed', now(), now(), 'agent_error', $3)
			RETURNING id
		`, agentID, runtimeID, errorText).Scan(&id); err != nil {
			t.Fatalf("insert migration task: %v", err)
		}
		return id
	}
	poisonedID := insertTask(`API Error: 400 {"error":{"type":"invalid_request_error"}}`)
	benignID := insertTask("tool execution failed: connection refused")

	migration := readMigrationFile(t, "027_classify_legacy_api_invalid_request_tasks.up.sql")
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := tx.Exec(ctx, migration); err != nil {
			t.Fatalf("apply migration attempt %d: %v", attempt+1, err)
		}
	}

	var reason string
	if err := tx.QueryRow(ctx, `SELECT failure_reason FROM agent_task_queue WHERE id = $1`, poisonedID).Scan(&reason); err != nil {
		t.Fatalf("read classified task: %v", err)
	}
	if reason != "api_invalid_request" {
		t.Fatalf("classified task reason = %q", reason)
	}
	if err := tx.QueryRow(ctx, `SELECT failure_reason FROM agent_task_queue WHERE id = $1`, benignID).Scan(&reason); err != nil {
		t.Fatalf("read benign task: %v", err)
	}
	if reason != "agent_error" {
		t.Fatalf("benign task reason changed to %q", reason)
	}
}
