package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
)

func TestSOPAgentRoleKeyMigrationBackfillsOnce(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin migration fixture: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	suffix := uuid.NewString()
	var userID, workspaceID, runtimeID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO "user" (name, account)
		VALUES ('SOP Migration Test', $1) RETURNING id
	`, "sop-role-migration-"+suffix+"@test.invalid").Scan(&userID); err != nil {
		t.Fatalf("insert migration user: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, issue_prefix)
		VALUES ('SOP Migration Test', $1, 'SRM') RETURNING id
	`, "sop-role-migration-"+suffix).Scan(&workspaceID); err != nil {
		t.Fatalf("insert migration workspace: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, name, runtime_mode, provider, status, owner_id
		) VALUES ($1, 'SOP Migration Runtime', 'cloud', 'codebuddy', 'online', $2)
		RETURNING id
	`, workspaceID, userID).Scan(&runtimeID); err != nil {
		t.Fatalf("insert migration runtime: %v", err)
	}

	for _, fixture := range []struct {
		name   string
		config string
	}{
		{name: "01-需求澄清", config: `{"internal_squad":{"template_key":"old-sop"},"keep":"yes"}`},
		{name: "02-方案设计", config: `{"internal_squad":{"role_key":"custom-role"}}`},
		{name: "Ordinary Agent", config: `{}`},
	} {
		if _, err := tx.Exec(ctx, `
			INSERT INTO agent (
				workspace_id, name, runtime_mode, runtime_config, runtime_id, owner_id
			) VALUES ($1, $2, 'cloud', $3::jsonb, $4, $5)
		`, workspaceID, fixture.name, fixture.config, runtimeID, userID); err != nil {
			t.Fatalf("insert agent %q: %v", fixture.name, err)
		}
	}

	migration, err := os.ReadFile(filepath.Join("..", "..", "migrations", "016_backfill_sop_agent_role_keys.up.sql"))
	if err != nil {
		t.Fatalf("read SOP role migration: %v", err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := tx.Exec(ctx, string(migration)); err != nil {
			t.Fatalf("apply SOP role migration attempt %d: %v", attempt+1, err)
		}
	}

	var roleKey, templateKey, keep string
	if err := tx.QueryRow(ctx, `
		SELECT
			runtime_config->'internal_squad'->>'role_key',
			runtime_config->'internal_squad'->>'template_key',
			runtime_config->>'keep'
		FROM agent WHERE workspace_id = $1 AND name = '01-需求澄清'
	`, workspaceID).Scan(&roleKey, &templateKey, &keep); err != nil {
		t.Fatalf("read migrated SOP agent: %v", err)
	}
	if roleKey != "01-clarify" || templateKey != "old-sop" || keep != "yes" {
		t.Fatalf("migrated config role=%q template=%q keep=%q", roleKey, templateKey, keep)
	}

	if err := tx.QueryRow(ctx, `
		SELECT runtime_config->'internal_squad'->>'role_key'
		FROM agent WHERE workspace_id = $1 AND name = '02-方案设计'
	`, workspaceID).Scan(&roleKey); err != nil {
		t.Fatalf("read explicit role agent: %v", err)
	}
	if roleKey != "custom-role" {
		t.Fatalf("migration overwrote explicit role_key: %q", roleKey)
	}

	var hasInternalSquad bool
	if err := tx.QueryRow(ctx, `
		SELECT runtime_config ? 'internal_squad'
		FROM agent WHERE workspace_id = $1 AND name = 'Ordinary Agent'
	`, workspaceID).Scan(&hasInternalSquad); err != nil {
		t.Fatalf("read ordinary agent: %v", err)
	}
	if hasInternalSquad {
		t.Fatal("migration changed an unrelated agent")
	}
}
