package main

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestInternalSquadAgentConfigMigrationNormalizesCurrentTemplatesOnce(t *testing.T) {
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
		VALUES ('Internal Squad Config Migration Test', $1) RETURNING id
	`, "internal-squad-config-"+suffix+"@test.invalid").Scan(&userID); err != nil {
		t.Fatalf("insert migration user: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, issue_prefix)
		VALUES ('Internal Squad Config Migration Test', $1, 'ISC') RETURNING id
	`, "internal-squad-config-"+suffix).Scan(&workspaceID); err != nil {
		t.Fatalf("insert migration workspace: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, name, runtime_mode, provider, status, owner_id
		) VALUES ($1, 'Internal Squad Config Migration Runtime', 'cloud', 'codebuddy', 'online', $2)
		RETURNING id
	`, workspaceID, userID).Scan(&runtimeID); err != nil {
		t.Fatalf("insert migration runtime: %v", err)
	}

	insertAgent := func(name, scope, config string) string {
		t.Helper()
		var id string
		if err := tx.QueryRow(ctx, `
			INSERT INTO agent (
				workspace_id, name, runtime_mode, runtime_config, runtime_id,
				owner_id, scope
			) VALUES ($1, $2, 'cloud', $3::jsonb, $4, $5, $6)
			RETURNING id
		`, workspaceID, name, config, runtimeID, userID, scope).Scan(&id); err != nil {
			t.Fatalf("insert agent %q: %v", name, err)
		}
		return id
	}

	legacyID := insertAgent(
		"Legacy SOP verifier",
		"personal",
		`{"用途":"pm-v2","模板":"user-center-sop-flow-v2","角色":"05-验证测试","keep":"yes"}`,
	)
	currentID := insertAgent(
		"Current coding developer",
		"workspace",
		`{"用途":"Multica 编码小队","模板":"multica-coding","角色":"开发者","internal_squad":{"role_key":"custom-developer","custom":"kept"}}`,
	)
	unknownID := insertAgent(
		"Unknown template agent",
		"workspace",
		`{"模板":"user-defined-template","角色":"开发者"}`,
	)

	migration := readMigrationFile(t, "026_normalize_internal_squad_agent_config.up.sql")
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := tx.Exec(ctx, migration); err != nil {
			t.Fatalf("apply migration attempt %d: %v", attempt+1, err)
		}
	}

	var templateKey, roleKey, squadScope, agentScope, ownerID, keep string
	var hasPurpose, hasRole, hasTemplate bool
	if err := tx.QueryRow(ctx, `
		SELECT
			runtime_config->'internal_squad'->>'template_key',
			runtime_config->'internal_squad'->>'role_key',
			runtime_config->'internal_squad'->>'squad_scope',
			runtime_config->'internal_squad'->>'agent_scope',
			runtime_config->'internal_squad'->>'owner_id',
			runtime_config->>'keep',
			runtime_config ? '用途', runtime_config ? '角色', runtime_config ? '模板'
		FROM agent WHERE id = $1
	`, legacyID).Scan(&templateKey, &roleKey, &squadScope, &agentScope, &ownerID, &keep, &hasPurpose, &hasRole, &hasTemplate); err != nil {
		t.Fatalf("read normalized legacy config: %v", err)
	}
	if templateKey != "user-center-sop-flow-v2" || roleKey != "05-verify" ||
		squadScope != "personal" || agentScope != "personal" || ownerID != userID || keep != "yes" {
		t.Fatalf("normalized legacy config template=%q role=%q squad=%q agent=%q owner=%q keep=%q", templateKey, roleKey, squadScope, agentScope, ownerID, keep)
	}
	if hasPurpose || hasRole || hasTemplate {
		t.Fatal("normalized legacy config retained duplicate presentation keys")
	}

	var custom string
	if err := tx.QueryRow(ctx, `
		SELECT runtime_config->'internal_squad'->>'role_key',
		       runtime_config->'internal_squad'->>'custom'
		FROM agent WHERE id = $1
	`, currentID).Scan(&roleKey, &custom); err != nil {
		t.Fatalf("read normalized current config: %v", err)
	}
	if roleKey != "custom-developer" || custom != "kept" {
		t.Fatalf("existing structured config overwritten: role=%q custom=%q", roleKey, custom)
	}

	if err := tx.QueryRow(ctx, `
		SELECT runtime_config ? '模板', runtime_config->>'模板'
		FROM agent WHERE id = $1
	`, unknownID).Scan(&hasTemplate, &templateKey); err != nil {
		t.Fatalf("read unknown-template config: %v", err)
	}
	if !hasTemplate || templateKey != "user-defined-template" {
		t.Fatal("migration changed an unknown template")
	}
}
