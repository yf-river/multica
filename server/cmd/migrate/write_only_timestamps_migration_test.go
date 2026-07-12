package main

import (
	"context"
	"testing"
)

func TestWriteOnlyTimestampMigrationDropsUnusedColumns(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin migration fixture: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	down := readMigrationFile(t, "087_remove_unused_write_only_timestamps.down.sql")
	if _, err := tx.Exec(ctx, down); err != nil {
		t.Fatalf("restore write-only timestamps: %v", err)
	}

	up := readMigrationFile(t, "087_remove_unused_write_only_timestamps.up.sql")
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := tx.Exec(ctx, up); err != nil {
			t.Fatalf("apply timestamp removal attempt %d: %v", attempt+1, err)
		}
	}
	var remaining int
	if err := tx.QueryRow(ctx, `
		SELECT count(*)::int
		FROM information_schema.columns
		WHERE table_schema = 'public'
		  AND (table_name, column_name) IN (
		    ('issue_pull_request', 'linked_at'),
		    ('lark_outbound_card_message', 'last_patched_at')
		  )
	`).Scan(&remaining); err != nil {
		t.Fatalf("inspect removed timestamp columns: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("write-only timestamp columns remaining = %d", remaining)
	}
}
