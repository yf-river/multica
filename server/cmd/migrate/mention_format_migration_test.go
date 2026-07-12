package main

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestMentionFormatMigrationNormalizesPersistedMarkdown(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin migration fixture: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	suffix := uuid.NewString()
	var userID, workspaceID, runtimeID, agentID, issueID, commentID, sessionID, messageID string
	if err := tx.QueryRow(ctx, `INSERT INTO "user" (name, account) VALUES ('mention migration', $1) RETURNING id`, "mention-"+suffix).Scan(&userID); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO workspace (name, slug, issue_prefix) VALUES ('mention migration', $1, 'MMG') RETURNING id`, "mention-"+suffix).Scan(&workspaceID); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO agent_runtime (workspace_id, name, runtime_mode, provider, owner_id)
		VALUES ($1, 'mention migration', 'local', 'codex', $2) RETURNING id
	`, workspaceID, userID).Scan(&runtimeID); err != nil {
		t.Fatalf("insert runtime: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO agent (workspace_id, name, runtime_mode, runtime_id, scope, owner_id)
		VALUES ($1, 'mention migration', 'local', $2, 'personal', $3) RETURNING id
	`, workspaceID, runtimeID, userID).Scan(&agentID); err != nil {
		t.Fatalf("insert agent: %v", err)
	}

	legacy := `before [@ role="reviewer" label="Alice" id="11111111-1111-4111-8111-111111111111"] middle [@ id="22222222-2222-4222-8222-222222222222" label="Bob"] after [@ broken]`
	want := `before [@Alice](mention://member/11111111-1111-4111-8111-111111111111) middle [@Bob](mention://member/22222222-2222-4222-8222-222222222222) after [@ broken]`
	if err := tx.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, description, creator_type, creator_id, number)
		VALUES ($1, 'mention migration', $2, 'member', $3, 900001) RETURNING id
	`, workspaceID, legacy, userID).Scan(&issueID); err != nil {
		t.Fatalf("insert issue: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO comment (issue_id, workspace_id, author_type, author_id, content)
		VALUES ($1, $2, 'member', $3, $4) RETURNING id
	`, issueID, workspaceID, userID, legacy).Scan(&commentID); err != nil {
		t.Fatalf("insert comment: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO chat_session (workspace_id, agent_id, creator_id, title)
		VALUES ($1, $2, $3, 'mention migration') RETURNING id
	`, workspaceID, agentID, userID).Scan(&sessionID); err != nil {
		t.Fatalf("insert chat session: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO chat_message (chat_session_id, role, content)
		VALUES ($1, 'user', $2) RETURNING id
	`, sessionID, legacy).Scan(&messageID); err != nil {
		t.Fatalf("insert chat message: %v", err)
	}

	up := readMigrationFile(t, "031_normalize_mention_markdown.up.sql")
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := tx.Exec(ctx, up); err != nil {
			t.Fatalf("apply migration attempt %d: %v", attempt+1, err)
		}
	}

	for name, query := range map[string]string{
		"issue":   `SELECT description FROM issue WHERE id = $1`,
		"comment": `SELECT content FROM comment WHERE id = $1`,
		"chat":    `SELECT content FROM chat_message WHERE id = $1`,
	} {
		id := map[string]string{"issue": issueID, "comment": commentID, "chat": messageID}[name]
		var got string
		if err := tx.QueryRow(ctx, query, id).Scan(&got); err != nil {
			t.Fatalf("read migrated %s: %v", name, err)
		}
		if got != want {
			t.Fatalf("%s content:\n got: %q\nwant: %q", name, got, want)
		}
	}
}
