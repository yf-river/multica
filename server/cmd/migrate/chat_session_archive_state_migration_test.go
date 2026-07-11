package main

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestChatSessionArchiveStateMigrationPreservesConversations(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin migration fixture: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
		ALTER TABLE chat_session ADD COLUMN IF NOT EXISTS status text NOT NULL DEFAULT 'active';
		ALTER TABLE chat_session DROP CONSTRAINT IF EXISTS chat_session_status_check;
		ALTER TABLE chat_session ADD CONSTRAINT chat_session_status_check
			CHECK (status IN ('active', 'archived'));
	`); err != nil {
		t.Fatalf("restore retired archive state: %v", err)
	}

	suffix := uuid.NewString()
	var userID, workspaceID, runtimeID, agentID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO "user" (name, account)
		VALUES ('Chat Session Migration', $1) RETURNING id
	`, "chat-session-migration-"+suffix+"@test.invalid").Scan(&userID); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, issue_prefix)
		VALUES ('Chat Session Migration', $1, 'CSM') RETURNING id
	`, "chat-session-migration-"+suffix).Scan(&workspaceID); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO agent_runtime (workspace_id, name, runtime_mode, provider, status, owner_id)
		VALUES ($1, 'Chat Session Migration Runtime', 'cloud', 'codex', 'online', $2)
		RETURNING id
	`, workspaceID, userID).Scan(&runtimeID); err != nil {
		t.Fatalf("insert runtime: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO agent (workspace_id, name, runtime_mode, runtime_config, runtime_id, owner_id)
		VALUES ($1, 'Chat Session Migration Agent', 'cloud', '{}'::jsonb, $2, $3)
		RETURNING id
	`, workspaceID, runtimeID, userID).Scan(&agentID); err != nil {
		t.Fatalf("insert agent: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO chat_session (workspace_id, agent_id, creator_id, title, status)
		VALUES ($1, $2, $3, 'active conversation', 'active'),
		       ($1, $2, $3, 'retained conversation', 'archived')
	`, workspaceID, agentID, userID); err != nil {
		t.Fatalf("insert chat sessions: %v", err)
	}

	up := readMigrationFile(t, "024_remove_chat_session_archive_state.up.sql")
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := tx.Exec(ctx, up); err != nil {
			t.Fatalf("apply migration attempt %d: %v", attempt+1, err)
		}
	}

	var sessions, statusColumns int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM chat_session WHERE workspace_id = $1`, workspaceID).Scan(&sessions); err != nil {
		t.Fatalf("count preserved sessions: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'chat_session' AND column_name = 'status'
	`).Scan(&statusColumns); err != nil {
		t.Fatalf("check status column removal: %v", err)
	}
	if sessions != 2 || statusColumns != 0 {
		t.Fatalf("sessions=%d status_columns=%d, want 2/0", sessions, statusColumns)
	}
}
