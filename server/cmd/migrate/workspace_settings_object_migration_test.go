package main

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestWorkspaceSettingsObjectMigrationNormalizesAndConstrainsShape(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin migration fixture: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `ALTER TABLE workspace DROP CONSTRAINT IF EXISTS workspace_settings_object`); err != nil {
		t.Fatalf("restore unconstrained workspace settings: %v", err)
	}

	slug := "workspace-settings-migration-" + uuid.NewString()
	if _, err := tx.Exec(ctx, `
		INSERT INTO workspace (name, slug, settings)
		VALUES ('Workspace Settings Migration', $1, '["invalid"]'::jsonb)
	`, slug); err != nil {
		t.Fatalf("insert array-shaped workspace settings: %v", err)
	}

	migration := readMigrationFile(t, "033_require_workspace_settings_object.up.sql")
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := tx.Exec(ctx, migration); err != nil {
			t.Fatalf("apply workspace settings migration attempt %d: %v", attempt+1, err)
		}
	}

	var settings string
	if err := tx.QueryRow(ctx, `SELECT settings::text FROM workspace WHERE slug = $1`, slug).Scan(&settings); err != nil {
		t.Fatalf("read normalized workspace settings: %v", err)
	}
	if settings != "{}" {
		t.Fatalf("migrated workspace settings = %s, want {}", settings)
	}

	var constraintCount int
	if err := tx.QueryRow(ctx, `
		SELECT count(*)::int
		FROM pg_constraint
		WHERE conrelid = 'workspace'::regclass
		  AND conname = 'workspace_settings_object'
	`).Scan(&constraintCount); err != nil {
		t.Fatalf("read workspace settings constraint: %v", err)
	}
	if constraintCount != 1 {
		t.Fatalf("workspace settings constraint count = %d, want 1", constraintCount)
	}

	if _, err := tx.Exec(ctx, `SAVEPOINT invalid_workspace_settings`); err != nil {
		t.Fatalf("create constraint probe savepoint: %v", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE workspace SET settings = '[]'::jsonb WHERE slug = $1`, slug); err == nil {
		t.Fatal("workspace settings constraint accepted an array")
	}
	if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT invalid_workspace_settings`); err != nil {
		t.Fatalf("rollback constraint probe: %v", err)
	}
}
