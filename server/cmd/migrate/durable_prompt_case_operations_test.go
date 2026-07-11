package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
)

func TestDurablePromptCaseOperationMigrationBackfillsOnce(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin migration fixture: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	suffix := uuid.NewString()
	var userID, workspaceID, assetID, operationID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO "user" (name, account) VALUES ('Migration Test', $1) RETURNING id
	`, "durable-case-operation-"+suffix+"@test.invalid").Scan(&userID); err != nil {
		t.Fatalf("insert migration user: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, issue_prefix)
		VALUES ('Migration Test', $1, 'MIG') RETURNING id
	`, "durable-case-operation-"+suffix).Scan(&workspaceID); err != nil {
		t.Fatalf("insert migration workspace: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO prompt_evaluation_asset (workspace_id, name, asset_type, created_by)
		VALUES ($1, 'Migration Asset', '数据集', $2) RETURNING id
	`, workspaceID, userID).Scan(&assetID); err != nil {
		t.Fatalf("insert migration asset: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO prompt_evaluation_case_operation (
			workspace_id, asset_id, operation_type, filter, input, created_by, status, started_at
		) VALUES (
			$1, $2, '批量追加标签', '{"limit":50}'::jsonb,
			'{"mode":"追加","tags":["after"]}'::jsonb, $3, '运行中', now()
		) RETURNING id
	`, workspaceID, assetID, userID).Scan(&operationID); err != nil {
		t.Fatalf("insert running operation: %v", err)
	}

	migration, err := os.ReadFile(filepath.Join("..", "..", "migrations", "014_durable_prompt_case_operations.up.sql"))
	if err != nil {
		t.Fatalf("read durable operation migration: %v", err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := tx.Exec(ctx, string(migration)); err != nil {
			t.Fatalf("apply durable operation migration attempt %d: %v", attempt+1, err)
		}
	}

	var status string
	var startedAtIsNull bool
	if err := tx.QueryRow(ctx, `
		SELECT status, started_at IS NULL
		FROM prompt_evaluation_case_operation WHERE id = $1
	`, operationID).Scan(&status, &startedAtIsNull); err != nil {
		t.Fatalf("load migrated operation: %v", err)
	}
	var events int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM domain_event_outbox
		WHERE event_type = 'prompt_evaluation_case_operation:requested'
		  AND payload->>'operation_id' = $1
	`, operationID).Scan(&events); err != nil {
		t.Fatalf("count migrated operation events: %v", err)
	}
	if status != "已入队" || !startedAtIsNull || events != 1 {
		t.Fatalf("migration result: status=%q started_null=%v events=%d", status, startedAtIsNull, events)
	}
}
