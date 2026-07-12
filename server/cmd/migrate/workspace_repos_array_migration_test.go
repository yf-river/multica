package main

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestWorkspaceReposArrayMigrationNormalizesAndConstrainsShape(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin migration fixture: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `ALTER TABLE workspace DROP CONSTRAINT IF EXISTS workspace_repos_array`); err != nil {
		t.Fatalf("restore unconstrained workspace repos: %v", err)
	}

	slug := "workspace-repos-migration-" + uuid.NewString()
	if _, err := tx.Exec(ctx, `
		INSERT INTO workspace (name, slug, repos)
		VALUES ('Workspace Repos Migration', $1, '{}'::jsonb)
	`, slug); err != nil {
		t.Fatalf("insert object-shaped workspace repos: %v", err)
	}

	migration := readMigrationFile(t, "032_require_workspace_repos_array.up.sql")
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := tx.Exec(ctx, migration); err != nil {
			t.Fatalf("apply workspace repos migration attempt %d: %v", attempt+1, err)
		}
	}

	var repos string
	if err := tx.QueryRow(ctx, `SELECT repos::text FROM workspace WHERE slug = $1`, slug).Scan(&repos); err != nil {
		t.Fatalf("read normalized workspace repos: %v", err)
	}
	if repos != "[]" {
		t.Fatalf("migrated workspace repos = %s, want []", repos)
	}

	var constraintCount int
	if err := tx.QueryRow(ctx, `
		SELECT count(*)::int
		FROM pg_constraint
		WHERE conrelid = 'workspace'::regclass
		  AND conname = 'workspace_repos_array'
	`).Scan(&constraintCount); err != nil {
		t.Fatalf("read workspace repos constraint: %v", err)
	}
	if constraintCount != 1 {
		t.Fatalf("workspace repos constraint count = %d, want 1", constraintCount)
	}

	if _, err := tx.Exec(ctx, `SAVEPOINT invalid_workspace_repos`); err != nil {
		t.Fatalf("create constraint probe savepoint: %v", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE workspace SET repos = '{}'::jsonb WHERE slug = $1`, slug); err == nil {
		t.Fatal("workspace repos constraint accepted an object")
	}
	if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT invalid_workspace_repos`); err != nil {
		t.Fatalf("rollback constraint probe: %v", err)
	}
}
