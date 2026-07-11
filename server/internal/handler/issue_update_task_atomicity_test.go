package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func installIssueTaskFailureTrigger(t *testing.T, operation, predicate string) {
	t.Helper()
	ctx := context.Background()
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	functionName := "test_issue_task_failure_" + suffix
	triggerName := "test_issue_task_failure_trigger_" + suffix
	if _, err := testPool.Exec(ctx, fmt.Sprintf(`
		CREATE FUNCTION %s() RETURNS trigger AS $$
		BEGIN
			IF %s THEN
				RAISE EXCEPTION 'forced issue task projection failure';
			END IF;
			RETURN NEW;
		END;
		$$ LANGUAGE plpgsql;
		CREATE TRIGGER %s BEFORE %s ON agent_task_queue
		FOR EACH ROW EXECUTE FUNCTION %s();
	`, functionName, predicate, triggerName, operation, functionName)); err != nil {
		t.Fatalf("install issue task failure trigger: %v", err)
	}
	t.Cleanup(func() {
		mustExec(t, ctx, fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON agent_task_queue`, triggerName))
		mustExec(t, ctx, fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, functionName))
	})
}

func TestUpdateIssueRollsBackWhenTaskEnqueueFails(t *testing.T) {
	issueID := createTestIssue(t, "atomic task enqueue", "todo", "medium")
	t.Cleanup(func() { deleteTestIssue(t, issueID) })
	agentID := createHandlerTestAgent(t, "atomic-task-enqueue-agent-"+uuid.NewString(), nil)
	installIssueTaskFailureTrigger(t, "INSERT", fmt.Sprintf("NEW.issue_id = '%s'::uuid", issueID))

	w := httptest.NewRecorder()
	req := newRequest(http.MethodPut, "/api/issues/"+issueID, map[string]any{
		"assignee_type": "agent",
		"assignee_id":   agentID,
	})
	req = withURLParam(req, "id", issueID)
	testHandler.UpdateIssue(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", w.Code, w.Body.String())
	}

	var assigneeType, assigneeID *string
	if err := testPool.QueryRow(context.Background(), `
		SELECT assignee_type, assignee_id::text FROM issue WHERE id = $1
	`, issueID).Scan(&assigneeType, &assigneeID); err != nil {
		t.Fatalf("reload issue: %v", err)
	}
	if assigneeType != nil || assigneeID != nil {
		t.Fatalf("assignment committed despite task failure: type=%v id=%v", assigneeType, assigneeID)
	}
	assertNoIssueUpdateEvent(t, issueID)
}

func TestUpdateIssueRollsBackWhenTaskCancellationFails(t *testing.T) {
	issueID := createTestIssue(t, "atomic task cancellation", "todo", "medium")
	t.Cleanup(func() { deleteTestIssue(t, issueID) })
	agentID := createHandlerTestAgent(t, "atomic-task-cancel-agent-"+uuid.NewString(), nil)

	var taskID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status)
		VALUES ($1, $2, $3, 'queued') RETURNING id
	`, agentID, handlerTestRuntimeID(t), issueID).Scan(&taskID); err != nil {
		t.Fatalf("seed issue task: %v", err)
	}
	t.Cleanup(func() { mustExec(t, context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, taskID) })
	installIssueTaskFailureTrigger(t, "UPDATE", fmt.Sprintf("NEW.id = '%s'::uuid AND NEW.status = 'cancelled'", taskID))

	w := httptest.NewRecorder()
	req := newRequest(http.MethodPut, "/api/issues/"+issueID, map[string]any{"status": "cancelled"})
	req = withURLParam(req, "id", issueID)
	testHandler.UpdateIssue(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", w.Code, w.Body.String())
	}

	var issueStatus, taskStatus string
	if err := testPool.QueryRow(context.Background(), `SELECT status FROM issue WHERE id = $1`, issueID).Scan(&issueStatus); err != nil {
		t.Fatalf("reload issue: %v", err)
	}
	if err := testPool.QueryRow(context.Background(), `SELECT status FROM agent_task_queue WHERE id = $1`, taskID).Scan(&taskStatus); err != nil {
		t.Fatalf("reload task: %v", err)
	}
	if issueStatus != "todo" || taskStatus != "queued" {
		t.Fatalf("partial cancellation committed: issue=%s task=%s", issueStatus, taskStatus)
	}
	assertNoIssueUpdateEvent(t, issueID)
}

func TestBatchUpdateReportsTaskProjectionFailureWithoutCommitting(t *testing.T) {
	issueID := createTestIssue(t, "atomic batch task enqueue", "todo", "medium")
	t.Cleanup(func() { deleteTestIssue(t, issueID) })
	agentID := createHandlerTestAgent(t, "atomic-batch-agent-"+uuid.NewString(), nil)
	installIssueTaskFailureTrigger(t, "INSERT", fmt.Sprintf("NEW.issue_id = '%s'::uuid", issueID))

	w := httptest.NewRecorder()
	req := newRequest(http.MethodPost, "/api/issues/batch-update", map[string]any{
		"issue_ids": []string{issueID},
		"updates": map[string]any{
			"assignee_type": "agent",
			"assignee_id":   agentID,
		},
	})
	testHandler.BatchUpdateIssues(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 batch envelope, got %d: %s", w.Code, w.Body.String())
	}
	var response struct {
		Updated int `json:"updated"`
		Failed  []struct {
			IssueID string `json:"issue_id"`
			Code    string `json:"code"`
		} `json:"failed"`
	}
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatalf("decode batch response: %v", err)
	}
	if response.Updated != 0 || len(response.Failed) != 1 || response.Failed[0].IssueID != issueID || response.Failed[0].Code != "task_projection_failed" {
		t.Fatalf("unexpected batch result: %+v", response)
	}

	var assigneeID *string
	if err := testPool.QueryRow(context.Background(), `SELECT assignee_id::text FROM issue WHERE id = $1`, issueID).Scan(&assigneeID); err != nil {
		t.Fatalf("reload issue: %v", err)
	}
	if assigneeID != nil {
		t.Fatalf("batch assignment committed despite task failure: %v", assigneeID)
	}
	assertNoIssueUpdateEvent(t, issueID)
}

func assertNoIssueUpdateEvent(t *testing.T, issueID string) {
	t.Helper()
	var count int
	if err := testPool.QueryRow(context.Background(), `
		SELECT count(*) FROM domain_event_outbox
		WHERE event_type = 'issue:updated' AND stream_key = 'issue:' || $1
	`, issueID).Scan(&count); err != nil {
		t.Fatalf("count issue update events: %v", err)
	}
	if count != 0 {
		t.Fatalf("durable issue update event committed despite task failure: %d", count)
	}
}
