package main

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestSkillConfigObjectMigrationNormalizesAndConstrainsShape(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin migration fixture: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `ALTER TABLE skill DROP CONSTRAINT IF EXISTS skill_config_object`); err != nil {
		t.Fatalf("restore unconstrained skill config: %v", err)
	}

	var workspaceID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO workspace (name, slug)
		VALUES ('Skill Config Migration', $1)
		RETURNING id
	`, "skill-config-migration-"+uuid.NewString()).Scan(&workspaceID); err != nil {
		t.Fatalf("insert migration workspace: %v", err)
	}

	var skillID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO skill (workspace_id, name, config)
		VALUES ($1, 'Skill Config Migration', '["invalid"]'::jsonb)
		RETURNING id
	`, workspaceID).Scan(&skillID); err != nil {
		t.Fatalf("insert array-shaped skill config: %v", err)
	}

	migration := readMigrationFile(t, "034_require_skill_config_object.up.sql")
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := tx.Exec(ctx, migration); err != nil {
			t.Fatalf("apply skill config migration attempt %d: %v", attempt+1, err)
		}
	}

	var config string
	if err := tx.QueryRow(ctx, `SELECT config::text FROM skill WHERE id = $1`, skillID).Scan(&config); err != nil {
		t.Fatalf("read normalized skill config: %v", err)
	}
	if config != "{}" {
		t.Fatalf("migrated skill config = %s, want {}", config)
	}

	var constraintCount int
	if err := tx.QueryRow(ctx, `
		SELECT count(*)::int
		FROM pg_constraint
		WHERE conrelid = 'skill'::regclass
		  AND conname = 'skill_config_object'
	`).Scan(&constraintCount); err != nil {
		t.Fatalf("read skill config constraint: %v", err)
	}
	if constraintCount != 1 {
		t.Fatalf("skill config constraint count = %d, want 1", constraintCount)
	}

	if _, err := tx.Exec(ctx, `SAVEPOINT invalid_skill_config`); err != nil {
		t.Fatalf("create constraint probe savepoint: %v", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE skill SET config = '[]'::jsonb WHERE id = $1`, skillID); err == nil {
		t.Fatal("skill config constraint accepted an array")
	}
	if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT invalid_skill_config`); err != nil {
		t.Fatalf("rollback constraint probe: %v", err)
	}
}
