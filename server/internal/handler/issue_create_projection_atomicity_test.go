package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func installApprovalInboxFailureTrigger(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	functionName := "test_approval_inbox_failure_" + suffix
	triggerName := "test_approval_inbox_failure_trigger_" + suffix
	if _, err := testPool.Exec(ctx, fmt.Sprintf(`
		CREATE FUNCTION %s() RETURNS trigger AS $$
		BEGIN
			IF NEW.type = 'project_issue_approval_requested' THEN
				RAISE EXCEPTION 'forced approval inbox projection failure';
			END IF;
			RETURN NEW;
		END;
		$$ LANGUAGE plpgsql;
		CREATE TRIGGER %s BEFORE INSERT ON inbox_item
		FOR EACH ROW EXECUTE FUNCTION %s();
	`, functionName, triggerName, functionName)); err != nil {
		t.Fatalf("install approval inbox failure trigger: %v", err)
	}
	t.Cleanup(func() {
		mustExec(t, ctx, fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON inbox_item`, triggerName))
		mustExec(t, ctx, fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, functionName))
	})
}

func createAtomicityTestProject(t *testing.T, leadType, leadID string) string {
	t.Helper()
	var projectID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO project (workspace_id, title, lead_type, lead_id)
		VALUES ($1, $2, $3, $4) RETURNING id
	`, testWorkspaceID, "atomic issue projection "+uuid.NewString(), leadType, leadID).Scan(&projectID); err != nil {
		t.Fatalf("create project: %v", err)
	}
	t.Cleanup(func() {
		mustExec(t, context.Background(), `DELETE FROM project WHERE id = $1`, projectID)
	})
	return projectID
}

func assertIssueTitleAbsent(t *testing.T, title string) {
	t.Helper()
	var count int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM issue WHERE title = $1`, title).Scan(&count); err != nil {
		t.Fatalf("count issues: %v", err)
	}
	if count != 0 {
		t.Fatalf("issue %q committed despite projection failure", title)
	}
}

func TestCreateIssueRollsBackWhenAssignedAgentTaskFails(t *testing.T) {
	agentID := createHandlerTestAgent(t, "atomic-create-agent-"+uuid.NewString(), nil)
	installIssueTaskFailureTrigger(t, "INSERT", fmt.Sprintf("NEW.agent_id = '%s'::uuid", agentID))
	title := "atomic assigned task " + uuid.NewString()

	w := httptest.NewRecorder()
	req := newRequest(http.MethodPost, "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title":         title,
		"assignee_type": "agent",
		"assignee_id":   agentID,
	})
	testHandler.CreateIssue(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", w.Code, w.Body.String())
	}
	assertIssueTitleAbsent(t, title)
}

func TestCreateIssueRollsBackWhenMemberApprovalInboxFails(t *testing.T) {
	projectID := createAtomicityTestProject(t, "member", testUserID)
	installApprovalInboxFailureTrigger(t)
	title := "atomic member approval " + uuid.NewString()

	w := httptest.NewRecorder()
	req := newRequest(http.MethodPost, "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title":      title,
		"status":     "backlog",
		"project_id": projectID,
	})
	testHandler.CreateIssue(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", w.Code, w.Body.String())
	}
	assertIssueTitleAbsent(t, title)
}

func TestCreateIssueRollsBackWhenAgentApprovalTaskFails(t *testing.T) {
	leadAgentID := createHandlerTestAgent(t, "atomic-approval-agent-"+uuid.NewString(), nil)
	projectID := createAtomicityTestProject(t, "agent", leadAgentID)
	installIssueTaskFailureTrigger(t, "INSERT", fmt.Sprintf("NEW.agent_id = '%s'::uuid", leadAgentID))
	title := "atomic agent approval " + uuid.NewString()

	w := httptest.NewRecorder()
	req := newRequest(http.MethodPost, "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title":      title,
		"status":     "backlog",
		"project_id": projectID,
	})
	testHandler.CreateIssue(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", w.Code, w.Body.String())
	}
	assertIssueTitleAbsent(t, title)
}
