package main

import (
	"context"
	"testing"
)

func TestUnusedIssueJSONFieldsMigrationDropsLegacyColumns(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin migration fixture: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	down := readMigrationFile(t, "086_remove_unused_issue_json_fields.down.sql")
	if _, err := tx.Exec(ctx, down); err != nil {
		t.Fatalf("restore legacy Issue columns: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE issue
		SET acceptance_criteria = '[{"text":"retired"}]'::jsonb,
		    context_refs = '[{"url":"retired"}]'::jsonb
		WHERE id = (SELECT id FROM issue LIMIT 1)
	`); err != nil {
		t.Fatalf("seed legacy Issue values: %v", err)
	}

	up := readMigrationFile(t, "086_remove_unused_issue_json_fields.up.sql")
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := tx.Exec(ctx, up); err != nil {
			t.Fatalf("apply Issue column removal attempt %d: %v", attempt+1, err)
		}
	}
	var remaining int
	if err := tx.QueryRow(ctx, `
		SELECT count(*)::int
		FROM information_schema.columns
		WHERE table_schema = 'public'
		  AND table_name = 'issue'
		  AND column_name IN ('acceptance_criteria', 'context_refs')
	`).Scan(&remaining); err != nil {
		t.Fatalf("inspect removed Issue columns: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("legacy Issue JSON columns remaining = %d", remaining)
	}
}
