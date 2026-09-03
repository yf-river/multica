package main

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// This integration check is opt-in because it performs an irreversible data
// deletion against the database named by DATABASE_URL. It exercises the same
// hook used by migration 456 and proves that an unrelated project/issue stays
// intact while the retired squad graph is removed.
func TestRemoveRetiredBuiltinsIntegration(t *testing.T) {
	if os.Getenv("MULTICA_MIGRATION_INTEGRATION") != "1" {
		t.Skip("set MULTICA_MIGRATION_INTEGRATION=1 to run against DATABASE_URL")
	}
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		t.Fatal("DATABASE_URL is required")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	workspaceID, userID, runtimeID := uuid.New(), uuid.New(), uuid.New()
	// The integration database is intentionally reusable between runs. Keep the
	// globally unique user email unique as well, otherwise a successful prior
	// run makes the next seed fail before the cleanup hook is exercised.
	userEmail := "cleanup-test-" + userID.String() + "@example.invalid"
	oldAgentID, oldSquadID, oldIssueID := uuid.New(), uuid.New(), uuid.New()
	keepAgentID, keepIssueID := uuid.New(), uuid.New()
	seed := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO "user" (id, name, email) VALUES ($1, 'cleanup-test', $2)`, []any{userID, userEmail}},
		{`INSERT INTO workspace (id, name, slug) VALUES ($1, 'cleanup-test', $2)`, []any{workspaceID, "cleanup-test-" + workspaceID.String()}},
		{`INSERT INTO member (id, workspace_id, user_id, role) VALUES ($1, $2, $3, 'owner')`, []any{uuid.New(), workspaceID, userID}},
		{`INSERT INTO agent_runtime (id, workspace_id, name, runtime_mode, provider) VALUES ($1, $2, 'cleanup-runtime', 'local', 'codebuddy')`, []any{runtimeID, workspaceID}},
		{`INSERT INTO agent (id, workspace_id, name, runtime_mode, runtime_id, owner_id, system_key) VALUES ($1, $2, 'retired coordinator cleanup', 'local', $3, $4, 'pm-v2')`, []any{oldAgentID, workspaceID, runtimeID, userID}},
		{`INSERT INTO agent (id, workspace_id, name, runtime_mode, owner_id) VALUES ($1, $2, 'ordinary agent', 'local', $3)`, []any{keepAgentID, workspaceID, userID}},
		{`INSERT INTO squad (id, workspace_id, name, leader_id, creator_id) VALUES ($1, $2, ' PM ', $3, $4)`, []any{oldSquadID, workspaceID, oldAgentID, userID}},
		{`INSERT INTO squad_member (id, squad_id, member_type, member_id, role) VALUES ($1, $2, 'agent', $3, 'leader')`, []any{uuid.New(), oldSquadID, oldAgentID}},
		{`INSERT INTO issue (id, workspace_id, title, creator_type, creator_id, assignee_type, assignee_id, number) VALUES ($1, $2, 'retired issue', 'agent', $3, 'agent', $3, 901)`, []any{oldIssueID, workspaceID, oldAgentID}},
		{`INSERT INTO issue (id, workspace_id, title, creator_type, creator_id, number) VALUES ($1, $2, 'keep issue', 'member', $3, 902)`, []any{keepIssueID, workspaceID, userID}},
	}
	for _, item := range seed {
		if _, err := pool.Exec(ctx, item.sql, item.args...); err != nil {
			t.Fatalf("seed cleanup graph: %v", err)
		}
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := removeRetiredBuiltinsWithTx(ctx, tx); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("cleanup hook: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM squad WHERE id=$1`, oldSquadID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("retired squad count=%d err=%v", count, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent WHERE id=$1`, oldAgentID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("retired agent count=%d err=%v", count, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM issue WHERE id=$1`, oldIssueID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("retired issue count=%d err=%v", count, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM issue WHERE id=$1`, keepIssueID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("ordinary issue count=%d err=%v", count, err)
	}
}
