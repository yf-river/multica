package main

import (
	"context"
	"testing"
)

func TestStarterContentStateMigrationDropsRetiredColumn(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin migration fixture: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	down := readMigrationFile(t, "089_remove_starter_content_state.down.sql")
	if _, err := tx.Exec(ctx, down); err != nil {
		t.Fatalf("restore starter content state: %v", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE "user" SET starter_content_state = 'retired' WHERE id = (SELECT id FROM "user" LIMIT 1)`); err != nil {
		t.Fatalf("seed retired starter content state: %v", err)
	}

	up := readMigrationFile(t, "089_remove_starter_content_state.up.sql")
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := tx.Exec(ctx, up); err != nil {
			t.Fatalf("apply starter content state removal attempt %d: %v", attempt+1, err)
		}
	}
	var exists bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_schema = 'public'
			  AND table_name = 'user'
			  AND column_name = 'starter_content_state'
		)
	`).Scan(&exists); err != nil {
		t.Fatalf("inspect removed starter content state: %v", err)
	}
	if exists {
		t.Fatal("user.starter_content_state still exists")
	}
}
