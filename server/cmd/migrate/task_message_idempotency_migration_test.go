package main

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestTaskMessageIdempotencyMigrationRoundTrip(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin migration fixture: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	down := readMigrationFile(t, "021_deduplicate_task_messages.down.sql")
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := tx.Exec(ctx, down); err != nil {
			t.Fatalf("apply down migration attempt %d: %v", attempt+1, err)
		}
	}
	if taskMessageIndexIsUnique(t, ctx, tx) {
		t.Fatal("task message index remained unique after down migration")
	}

	up := readMigrationFile(t, "021_deduplicate_task_messages.up.sql")
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := tx.Exec(ctx, up); err != nil {
			t.Fatalf("apply up migration attempt %d: %v", attempt+1, err)
		}
	}
	if !taskMessageIndexIsUnique(t, ctx, tx) {
		t.Fatal("task message index is not unique after up migration")
	}
}

func taskMessageIndexIsUnique(t *testing.T, ctx context.Context, tx pgx.Tx) bool {
	t.Helper()
	var unique bool
	if err := tx.QueryRow(ctx, `
		SELECT i.indisunique
		FROM pg_index i
		JOIN pg_class c ON c.oid = i.indexrelid
		WHERE c.relname = 'idx_task_message_task_id_seq'
	`).Scan(&unique); err != nil {
		t.Fatalf("read task message index: %v", err)
	}
	return unique
}
