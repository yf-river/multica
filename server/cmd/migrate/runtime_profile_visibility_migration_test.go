package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestRuntimeProfileVisibilityMigrationRoundTrip(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin migration fixture: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Recreate the exact legacy surface inside the transaction. Existing rows
	// may contain private because the old database constraint allowed it, even
	// though supported create paths never did.
	if _, err := tx.Exec(ctx, `
		ALTER TABLE runtime_profile
			ADD COLUMN IF NOT EXISTS visibility text NOT NULL DEFAULT 'workspace'
			CHECK (visibility IN ('workspace', 'private'));
	`); err != nil {
		t.Fatalf("restore legacy runtime profile shape: %v", err)
	}

	up := readMigrationFile(t, "019_remove_runtime_profile_visibility.up.sql")
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := tx.Exec(ctx, up); err != nil {
			t.Fatalf("apply up migration attempt %d: %v", attempt+1, err)
		}
	}
	if columnExists(t, ctx, tx, "runtime_profile", "visibility") {
		t.Fatal("runtime_profile.visibility still exists after up migration")
	}

	down := readMigrationFile(t, "019_remove_runtime_profile_visibility.down.sql")
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := tx.Exec(ctx, down); err != nil {
			t.Fatalf("apply down migration attempt %d: %v", attempt+1, err)
		}
	}
	if !columnExists(t, ctx, tx, "runtime_profile", "visibility") {
		t.Fatal("runtime_profile.visibility was not restored by down migration")
	}
}

func readMigrationFile(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "migrations", name))
	if err != nil {
		t.Fatalf("read migration %s: %v", name, err)
	}
	return string(raw)
}

func columnExists(t *testing.T, ctx context.Context, tx pgx.Tx, table, column string) bool {
	t.Helper()
	var exists bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_schema = 'public' AND table_name = $1 AND column_name = $2
		)
	`, table, column).Scan(&exists); err != nil {
		t.Fatalf("check %s.%s existence: %v", table, column, err)
	}
	return exists
}
