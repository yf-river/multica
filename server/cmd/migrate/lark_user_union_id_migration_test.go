package main

import (
	"context"
	"testing"
)

func TestLarkUserUnionIDMigrationDropsUnusedColumn(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin migration fixture: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	down := readMigrationFile(t, "090_remove_unused_lark_user_union_id.down.sql")
	if _, err := tx.Exec(ctx, down); err != nil {
		t.Fatalf("restore Lark user union ID: %v", err)
	}

	up := readMigrationFile(t, "090_remove_unused_lark_user_union_id.up.sql")
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := tx.Exec(ctx, up); err != nil {
			t.Fatalf("apply Lark user union ID removal attempt %d: %v", attempt+1, err)
		}
	}
	var exists bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_schema = 'public'
			  AND table_name = 'lark_user_binding'
			  AND column_name = 'union_id'
		)
	`).Scan(&exists); err != nil {
		t.Fatalf("inspect removed Lark user union ID: %v", err)
	}
	if exists {
		t.Fatal("lark_user_binding.union_id still exists")
	}
}
