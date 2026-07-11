package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
)

func TestTaskUsageUpdatedAtMigrationNormalizesLegacyRows(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin migration fixture: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
		ALTER TABLE task_usage ALTER COLUMN updated_at DROP NOT NULL;
		CREATE INDEX IF NOT EXISTS idx_task_usage_created_at_legacy
			ON task_usage (created_at) WHERE updated_at IS NULL;
	`); err != nil {
		t.Fatalf("restore legacy task_usage shape: %v", err)
	}

	suffix := uuid.NewString()
	var workspaceID, runtimeID, agentID, taskID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, issue_prefix)
		VALUES ('Task Usage Migration Test', $1, 'TUM') RETURNING id
	`, "task-usage-migration-"+suffix).Scan(&workspaceID); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO agent_runtime (workspace_id, name, runtime_mode, provider, status)
		VALUES ($1, 'Task Usage Migration Runtime', 'cloud', 'codex', 'online')
		RETURNING id
	`, workspaceID).Scan(&runtimeID); err != nil {
		t.Fatalf("insert runtime: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO agent (workspace_id, name, runtime_mode, runtime_config, runtime_id)
		VALUES ($1, 'Task Usage Migration Agent', 'cloud', '{}'::jsonb, $2)
		RETURNING id
	`, workspaceID, runtimeID).Scan(&agentID); err != nil {
		t.Fatalf("insert agent: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id)
		VALUES ($1, $2) RETURNING id
	`, agentID, runtimeID).Scan(&taskID); err != nil {
		t.Fatalf("insert task: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO task_usage (task_id, provider, model, created_at, updated_at)
		VALUES ($1, 'codex', 'gpt-5.4', '2026-07-01T02:03:04Z', NULL)
	`, taskID); err != nil {
		t.Fatalf("insert nullable task usage: %v", err)
	}

	migration, err := os.ReadFile(filepath.Join("..", "..", "migrations", "018_require_task_usage_updated_at.up.sql"))
	if err != nil {
		t.Fatalf("read task usage migration: %v", err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := tx.Exec(ctx, string(migration)); err != nil {
			t.Fatalf("apply task usage migration attempt %d: %v", attempt+1, err)
		}
	}

	var normalized bool
	if err := tx.QueryRow(ctx, `
		SELECT updated_at = created_at FROM task_usage WHERE task_id = $1
	`, taskID).Scan(&normalized); err != nil {
		t.Fatalf("read normalized task usage: %v", err)
	}
	if !normalized {
		t.Fatal("nullable task usage row was not normalized to created_at")
	}

	var nullable string
	if err := tx.QueryRow(ctx, `
		SELECT is_nullable FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'task_usage' AND column_name = 'updated_at'
	`).Scan(&nullable); err != nil {
		t.Fatalf("read updated_at nullability: %v", err)
	}
	if nullable != "NO" {
		t.Fatalf("task_usage.updated_at is_nullable = %q, want NO", nullable)
	}

	var legacyIndexCount, rollupLegacyBranch, windowLegacyBranch int
	if err := tx.QueryRow(ctx, `
		SELECT
			(SELECT count(*)::int FROM pg_indexes
			 WHERE schemaname = 'public' AND tablename = 'task_usage'
			   AND indexname = 'idx_task_usage_created_at_legacy'),
			position('updated_at IS NULL' in pg_get_functiondef(
				'public.rollup_task_usage_hourly()'::regprocedure)),
			position('updated_at IS NULL' in pg_get_functiondef(
				'public.rollup_task_usage_hourly_window(timestamp with time zone,timestamp with time zone)'::regprocedure))
	`).Scan(&legacyIndexCount, &rollupLegacyBranch, &windowLegacyBranch); err != nil {
		t.Fatalf("read migrated rollup shape: %v", err)
	}
	if legacyIndexCount != 0 || rollupLegacyBranch != 0 || windowLegacyBranch != 0 {
		t.Fatalf(
			"legacy rollup surface remains: index=%d rollup=%d window=%d",
			legacyIndexCount,
			rollupLegacyBranch,
			windowLegacyBranch,
		)
	}
}
