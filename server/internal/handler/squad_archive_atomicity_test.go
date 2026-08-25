package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDeleteSquadRollsBackAssigneeTransfersWhenAutopilotTransferFails(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	leaderID := createHandlerTestAgent(t, "Atomic Squad Archive Leader", nil)
	suffix := time.Now().UnixNano()
	title := fmt.Sprintf("atomic squad archive %d", suffix)
	var squadID string
	if err := testPool.QueryRow(ctx, `INSERT INTO squad (workspace_id,name,leader_id,creator_id,scope) VALUES ($1,$2,$3,$4,'personal') RETURNING id`, testWorkspaceID, title, leaderID, testUserID).Scan(&squadID); err != nil {
		t.Fatalf("create squad: %v", err)
	}
	issueNumber := nextHandlerTestIssueNumber(t)
	var issueID, autopilotID string
	if err := testPool.QueryRow(ctx, `INSERT INTO issue (workspace_id,creator_type,creator_id,title,assignee_type,assignee_id,number) VALUES ($1,'member',$2,$3,'squad',$4,$5) RETURNING id`, testWorkspaceID, testUserID, title, squadID, issueNumber).Scan(&issueID); err != nil {
		t.Fatalf("create assigned issue: %v", err)
	}
	if err := testPool.QueryRow(ctx, `INSERT INTO autopilot (workspace_id,title,assignee_type,assignee_id,created_by_type,created_by_id) VALUES ($1,$2,'squad',$3,'member',$4) RETURNING id`, testWorkspaceID, title, squadID, testUserID).Scan(&autopilotID); err != nil {
		t.Fatalf("create assigned autopilot: %v", err)
	}
	const functionName = "test_fail_atomic_squad_autopilot_transfer"
	const triggerName = "test_fail_atomic_squad_autopilot_transfer_trigger"
	if _, err := testPool.Exec(ctx, fmt.Sprintf(`
		CREATE OR REPLACE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF OLD.title = %s THEN RAISE EXCEPTION 'injected autopilot transfer failure'; END IF;
			RETURN NEW;
		END $$;
		DROP TRIGGER IF EXISTS %s ON autopilot;
		CREATE TRIGGER %s BEFORE UPDATE ON autopilot FOR EACH ROW EXECUTE FUNCTION %s()
	`, functionName, quoteSQLLiteral(title), triggerName, triggerName, functionName)); err != nil {
		t.Fatalf("install transfer fault: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON autopilot; DROP FUNCTION IF EXISTS %s()`, triggerName, functionName))
		_, _ = testPool.Exec(context.Background(), `DELETE FROM autopilot WHERE id=$1`, autopilotID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id=$1`, issueID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM squad WHERE id=$1`, squadID)
	})

	w := httptest.NewRecorder()
	req := withURLParams(newRequest(http.MethodDelete, "/api/squads/"+squadID, nil), "workspaceId", testWorkspaceID, "id", squadID)
	testHandler.DeleteSquad(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("delete squad: expected 500, got %d: %s", w.Code, w.Body.String())
	}
	var issueType, autopilotType string
	var archived bool
	if err := testPool.QueryRow(ctx, `SELECT assignee_type FROM issue WHERE id=$1`, issueID).Scan(&issueType); err != nil {
		t.Fatal(err)
	}
	if err := testPool.QueryRow(ctx, `SELECT assignee_type FROM autopilot WHERE id=$1`, autopilotID).Scan(&autopilotType); err != nil {
		t.Fatal(err)
	}
	if err := testPool.QueryRow(ctx, `SELECT archived_at IS NOT NULL FROM squad WHERE id=$1`, squadID).Scan(&archived); err != nil {
		t.Fatal(err)
	}
	if issueType != "squad" || autopilotType != "squad" || archived {
		t.Fatalf("failed delete left issue=%s autopilot=%s archived=%t", issueType, autopilotType, archived)
	}
}
