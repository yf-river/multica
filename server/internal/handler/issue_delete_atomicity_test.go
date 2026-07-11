package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

func TestDeleteIssue_DeleteFailureRollsBackTaskCancellation(t *testing.T) {
	assertIssueDeleteRollback(t, false)
}

func TestBatchDeleteIssues_ReportsFailureAndRollsBackTaskCancellation(t *testing.T) {
	assertIssueDeleteRollback(t, true)
}

func assertIssueDeleteRollback(t *testing.T, batch bool) {
	t.Helper()
	ctx := context.Background()
	issueID := createTestIssue(t, "delete rollback "+uuid.NewString(), "todo", "medium")
	var agentID string
	if err := testPool.QueryRow(ctx, `
		SELECT id::text FROM agent WHERE workspace_id = $1 AND runtime_id IS NOT NULL
		ORDER BY created_at LIMIT 1
	`, testWorkspaceID).Scan(&agentID); err != nil {
		t.Fatalf("load agent: %v", err)
	}
	var taskID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority)
		VALUES ($1, $2, $3, 'dispatched', 0)
		RETURNING id
	`, agentID, testRuntimeID, issueID).Scan(&taskID); err != nil {
		t.Fatalf("create task: %v", err)
	}

	suffix := uuid.NewString()
	functionName := quoteIdentifier("fail_issue_delete_" + suffix)
	triggerName := quoteIdentifier("fail_issue_delete_trigger_" + suffix)
	if _, err := testPool.Exec(ctx, fmt.Sprintf(`
		CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF OLD.id = %s::uuid THEN
				RAISE EXCEPTION 'forced issue delete failure';
			END IF;
			RETURN OLD;
		END $$;
		CREATE TRIGGER %s BEFORE DELETE ON issue
		FOR EACH ROW EXECUTE FUNCTION %s();
	`, functionName, quoteSQLLiteral(issueID), triggerName, functionName)); err != nil {
		t.Fatalf("install failure trigger: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON issue`, triggerName))
		_, _ = testPool.Exec(context.Background(), fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, functionName))
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, issueID)
	})

	w := httptest.NewRecorder()
	if batch {
		req := newRequest(http.MethodPost, "/api/issues/batch-delete?workspace_id="+testWorkspaceID, map[string]any{
			"issue_ids": []string{issueID},
		})
		testHandler.BatchDeleteIssues(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("batch delete = %d %s, want 200 with item failure", w.Code, w.Body.String())
		}
		var response struct {
			Deleted int `json:"deleted"`
			Failed  []struct {
				IssueID string `json:"issue_id"`
				Code    string `json:"code"`
			} `json:"failed"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode batch response: %v", err)
		}
		if response.Deleted != 0 || len(response.Failed) != 1 || response.Failed[0].IssueID != issueID || response.Failed[0].Code != "delete_failed" {
			t.Fatalf("batch response = %#v", response)
		}
	} else {
		req := withURLParam(newRequest(http.MethodDelete, "/api/issues/"+issueID, nil), "id", issueID)
		testHandler.DeleteIssue(w, req)
		if w.Code != http.StatusInternalServerError {
			t.Fatalf("delete = %d %s, want 500", w.Code, w.Body.String())
		}
	}

	var issues int
	var taskStatus string
	var cancelledEvents int
	_ = testPool.QueryRow(ctx, `SELECT count(*) FROM issue WHERE id = $1`, issueID).Scan(&issues)
	_ = testPool.QueryRow(ctx, `SELECT status FROM agent_task_queue WHERE id = $1`, taskID).Scan(&taskStatus)
	_ = testPool.QueryRow(ctx, `
		SELECT count(*) FROM domain_event_outbox
		WHERE event_type = 'task:cancelled' AND task_id = $1
	`, taskID).Scan(&cancelledEvents)
	if issues != 1 || taskStatus != "dispatched" || cancelledEvents != 0 {
		t.Fatalf("partial delete: issues=%d task=%s cancelled_events=%d", issues, taskStatus, cancelledEvents)
	}
}
