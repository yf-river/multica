package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
)

func TestPromptEvaluationAgentNameMigrationUnifiesLegacyRows(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin migration fixture: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	suffix := uuid.NewString()
	var userID, runtimeID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO "user" (name, account)
		VALUES ('Prompt Evaluation Migration Test', $1) RETURNING id
	`, "prompt-evaluation-agent-migration-"+suffix+"@test.invalid").Scan(&userID); err != nil {
		t.Fatalf("insert migration user: %v", err)
	}

	createWorkspace := func(slug string) string {
		t.Helper()
		var workspaceID string
		if err := tx.QueryRow(ctx, `
			INSERT INTO workspace (name, slug, issue_prefix)
			VALUES ('Prompt Evaluation Migration Test', $1, 'PEM') RETURNING id
		`, slug).Scan(&workspaceID); err != nil {
			t.Fatalf("insert migration workspace: %v", err)
		}
		return workspaceID
	}
	createRuntime := func(workspaceID string) string {
		t.Helper()
		var id string
		if err := tx.QueryRow(ctx, `
			INSERT INTO agent_runtime (
				workspace_id, name, runtime_mode, provider, status, owner_id
			) VALUES ($1, 'Prompt Evaluation Migration Runtime', 'cloud', 'codebuddy', 'online', $2)
			RETURNING id
		`, workspaceID, userID).Scan(&id); err != nil {
			t.Fatalf("insert migration runtime: %v", err)
		}
		return id
	}
	createAgent := func(workspaceID, runtimeID, name string) string {
		t.Helper()
		var id string
		if err := tx.QueryRow(ctx, `
			INSERT INTO agent (
				workspace_id, name, runtime_mode, runtime_config, runtime_id,
				owner_id, scope
			) VALUES ($1, $2, 'cloud', '{}'::jsonb, $3, $4, 'workspace')
			RETURNING id
		`, workspaceID, name, runtimeID, userID).Scan(&id); err != nil {
			t.Fatalf("insert agent %q: %v", name, err)
		}
		return id
	}

	legacyOnlyWorkspace := createWorkspace("prompt-eval-agent-legacy-" + suffix)
	runtimeID = createRuntime(legacyOnlyWorkspace)
	legacyOnlyID := createAgent(legacyOnlyWorkspace, runtimeID, "Multica 训练评估 Agent")

	conflictWorkspace := createWorkspace("prompt-eval-agent-conflict-" + suffix)
	runtimeID = createRuntime(conflictWorkspace)
	currentID := createAgent(conflictWorkspace, runtimeID, "Multica 训练评估智能体")
	legacyConflictID := createAgent(conflictWorkspace, runtimeID, "Multica 训练评估 Agent")

	migration, err := os.ReadFile(filepath.Join("..", "..", "migrations", "017_unify_prompt_evaluation_agent_name.up.sql"))
	if err != nil {
		t.Fatalf("read prompt evaluation agent migration: %v", err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := tx.Exec(ctx, string(migration)); err != nil {
			t.Fatalf("apply prompt evaluation agent migration attempt %d: %v", attempt+1, err)
		}
	}

	var name string
	var archived bool
	if err := tx.QueryRow(ctx, `
		SELECT name, archived_at IS NOT NULL FROM agent WHERE id = $1
	`, legacyOnlyID).Scan(&name, &archived); err != nil {
		t.Fatalf("read migrated legacy-only agent: %v", err)
	}
	if name != "Multica 训练评估智能体" || archived {
		t.Fatalf("legacy-only agent name=%q archived=%v", name, archived)
	}

	if err := tx.QueryRow(ctx, `
		SELECT name, archived_at IS NOT NULL FROM agent WHERE id = $1
	`, legacyConflictID).Scan(&name, &archived); err != nil {
		t.Fatalf("read migrated conflicting legacy agent: %v", err)
	}
	if name != "Multica 训练评估智能体" || !archived {
		t.Fatalf("conflicting legacy agent name=%q archived=%v", name, archived)
	}

	if err := tx.QueryRow(ctx, `
		SELECT name, archived_at IS NOT NULL FROM agent WHERE id = $1
	`, currentID).Scan(&name, &archived); err != nil {
		t.Fatalf("read current agent: %v", err)
	}
	if name != "Multica 训练评估智能体" || archived {
		t.Fatalf("current agent name=%q archived=%v", name, archived)
	}
}
