package main

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestTaskTraceMetadataMigrationNormalizesAndConstrainsShape(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin migration fixture: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `ALTER TABLE task_trace_event DROP CONSTRAINT IF EXISTS task_trace_event_metadata_is_object`); err != nil {
		t.Fatalf("restore unconstrained trace metadata: %v", err)
	}
	var workspaceID, runtimeID, agentID, taskID, traceID string
	if err := tx.QueryRow(ctx, `INSERT INTO workspace (name, slug) VALUES ('Trace Metadata Migration', $1) RETURNING id`, "trace-metadata-"+uuid.NewString()).Scan(&workspaceID); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO agent_runtime (workspace_id, name, runtime_mode, provider, status) VALUES ($1, 'Trace Metadata Migration', 'cloud', 'codex', 'online') RETURNING id`, workspaceID).Scan(&runtimeID); err != nil {
		t.Fatalf("insert runtime: %v", err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO agent (workspace_id, runtime_id, name, runtime_mode, runtime_config, custom_env, custom_args) VALUES ($1,$2,'Trace Metadata Migration','cloud','{}','{}','[]') RETURNING id`, workspaceID, runtimeID).Scan(&agentID); err != nil {
		t.Fatalf("insert agent: %v", err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO agent_task_queue (agent_id, runtime_id) VALUES ($1,$2) RETURNING id`, agentID, runtimeID).Scan(&taskID); err != nil {
		t.Fatalf("insert task: %v", err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO task_trace_event (workspace_id, task_id, agent_id, event_type, event_name, metadata) VALUES ($1,$2,$3,'test','test','[]') RETURNING id`, workspaceID, taskID, agentID).Scan(&traceID); err != nil {
		t.Fatalf("insert invalid trace metadata: %v", err)
	}

	up := readMigrationFile(t, "091_require_task_trace_metadata_object.up.sql")
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := tx.Exec(ctx, up); err != nil {
			t.Fatalf("apply trace metadata migration attempt %d: %v", attempt+1, err)
		}
	}
	var invalidCount int
	if err := tx.QueryRow(ctx, `SELECT count(*)::int FROM task_trace_event WHERE jsonb_typeof(metadata) <> 'object'`).Scan(&invalidCount); err != nil {
		t.Fatalf("count invalid trace metadata: %v", err)
	}
	if invalidCount != 0 {
		t.Fatalf("invalid trace metadata rows remaining = %d", invalidCount)
	}
	if _, err := tx.Exec(ctx, `UPDATE task_trace_event SET metadata = '[]'::jsonb WHERE id = $1`, traceID); err == nil {
		t.Fatal("trace metadata constraint accepted array")
	}
}
