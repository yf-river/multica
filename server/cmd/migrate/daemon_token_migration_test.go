package main

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestDaemonTokenMigrationRoundTrip(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin migration fixture: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	down := readMigrationFile(t, "020_remove_unused_daemon_tokens.down.sql")
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := tx.Exec(ctx, down); err != nil {
			t.Fatalf("apply down migration attempt %d: %v", attempt+1, err)
		}
	}
	if !tableExists(t, ctx, tx, "daemon_token") {
		t.Fatal("daemon_token was not restored by down migration")
	}

	up := readMigrationFile(t, "020_remove_unused_daemon_tokens.up.sql")
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := tx.Exec(ctx, up); err != nil {
			t.Fatalf("apply up migration attempt %d: %v", attempt+1, err)
		}
	}
	if tableExists(t, ctx, tx, "daemon_token") {
		t.Fatal("daemon_token still exists after up migration")
	}
}

func tableExists(t *testing.T, ctx context.Context, tx pgx.Tx, table string) bool {
	t.Helper()
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT to_regclass('public.' || $1) IS NOT NULL`, table).Scan(&exists); err != nil {
		t.Fatalf("check table %s existence: %v", table, err)
	}
	return exists
}
