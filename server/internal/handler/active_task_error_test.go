package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestGetActiveTaskForIssueDoesNotTurnCanceledReadIntoEmptySuccess(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	var issueID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO issue (workspace_id, title, status, priority, creator_id, creator_type)
		VALUES ($1, 'active-task-read-cancel', 'todo', 'medium', $2, 'member')
		RETURNING id
	`, testWorkspaceID, testUserID).Scan(&issueID); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	t.Cleanup(func() {
		mustExec(t, context.Background(), `DELETE FROM issue WHERE id = $1`, issueID)
	})

	tx, err := testPool.Begin(context.Background())
	if err != nil {
		t.Fatalf("begin blocker: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	if _, err := tx.Exec(context.Background(), `LOCK TABLE agent_task_queue IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatalf("lock task table: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	time.AfterFunc(100*time.Millisecond, cancel)
	req := newRequest(http.MethodGet, "/api/issues/"+issueID+"/active-task", nil).WithContext(ctx)
	req = withURLParam(req, "id", issueID)
	w := httptest.NewRecorder()
	testHandler.GetActiveTaskForIssue(w, req)

	if w.Code != 499 {
		t.Fatalf("canceled active-task read = %d %s, want 499 instead of an empty 200", w.Code, w.Body.String())
	}
}

func TestListTaskMessagesByUserPreservesCanceledTaskLookup(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agentID := createHandlerTestAgent(t, "task-message-cancel-agent", nil)
	taskID := createHandlerTestTaskForAgent(t, agentID)

	tx, err := testPool.Begin(context.Background())
	if err != nil {
		t.Fatalf("begin blocker: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	if _, err := tx.Exec(context.Background(), `LOCK TABLE agent_task_queue IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatalf("lock task table: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	time.AfterFunc(100*time.Millisecond, cancel)
	req := newRequest(http.MethodGet, "/api/tasks/"+taskID+"/messages", nil).WithContext(ctx)
	req = withURLParam(req, "taskId", taskID)
	w := httptest.NewRecorder()
	testHandler.ListTaskMessagesByUser(w, req)

	if w.Code != 499 {
		t.Fatalf("canceled task lookup = %d %s, want 499 instead of false not-found", w.Code, w.Body.String())
	}
}

func TestCancelTaskRejectsMalformedTaskIDWithoutPanicking(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	var issueID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO issue (workspace_id, title, status, priority, creator_id, creator_type)
		VALUES ($1, 'cancel-malformed-task-id', 'todo', 'medium', $2, 'member')
		RETURNING id
	`, testWorkspaceID, testUserID).Scan(&issueID); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	t.Cleanup(func() { mustExec(t, context.Background(), `DELETE FROM issue WHERE id = $1`, issueID) })

	req := newRequest(http.MethodPost, "/api/issues/"+issueID+"/tasks/not-a-uuid/cancel", nil)
	req = withURLParam(req, "id", issueID)
	req = withURLParam(req, "taskId", "not-a-uuid")
	w := httptest.NewRecorder()
	testHandler.CancelTask(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("malformed task ID = %d %s, want 400", w.Code, w.Body.String())
	}
}
