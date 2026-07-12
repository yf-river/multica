package main

import (
	"context"
	"testing"
)

func TestLarkBindingTimestampMigrationDropsUnusedColumn(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin migration fixture: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	down := readMigrationFile(t, "088_remove_unused_lark_binding_timestamp.down.sql")
	if _, err := tx.Exec(ctx, down); err != nil {
		t.Fatalf("restore Lark binding timestamp: %v", err)
	}

	up := readMigrationFile(t, "088_remove_unused_lark_binding_timestamp.up.sql")
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := tx.Exec(ctx, up); err != nil {
			t.Fatalf("apply Lark binding timestamp removal attempt %d: %v", attempt+1, err)
		}
	}
	var exists bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_schema = 'public'
			  AND table_name = 'lark_user_binding'
			  AND column_name = 'bound_at'
		)
	`).Scan(&exists); err != nil {
		t.Fatalf("inspect removed Lark binding timestamp: %v", err)
	}
	if exists {
		t.Fatal("lark_user_binding.bound_at still exists")
	}
}
