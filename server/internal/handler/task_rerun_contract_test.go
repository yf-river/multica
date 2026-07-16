package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
)

func newIssueRerunKey(t *testing.T) string {
	t.Helper()
	key := uuid.NewString()
	t.Cleanup(func() {
		if testPool != nil {
			_, _ = testPool.Exec(context.Background(), `DELETE FROM resource_create_request WHERE resource_type = 'issue_rerun' AND idempotency_key = $1`, key)
		}
	})
	return key
}

func TestRerunIssueRequiresExplicitTarget(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	issueID := createTestIssue(t, "rerun requires explicit target", "medium")
	w := httptest.NewRecorder()
	req := withURLParam(newRequest(http.MethodPost, "/api/issues/"+issueID+"/rerun", nil), "id", issueID)

	testHandler.RerunIssue(w, req)

	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "rerun target is required") {
		t.Fatalf("empty rerun body: expected explicit-target 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRerunIssueReplaysCommittedTask(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	agentID := createWebhookTestAgent(t, "Issue Rerun Replay Agent")
	issueID := createTestIssue(t, "rerun committed task replay", "medium")
	if _, err := testPool.Exec(context.Background(), `
		UPDATE issue SET assignee_type = 'agent', assignee_id = $2 WHERE id = $1
	`, issueID, agentID); err != nil {
		t.Fatal(err)
	}
	requestKey := newIssueRerunKey(t)
	rerun := func() AgentTaskResponse {
		t.Helper()
		w := httptest.NewRecorder()
		req := withURLParam(newRequest(http.MethodPost, "/api/issues/"+issueID+"/rerun", map[string]any{
			"target": "current_assignee",
		}), "id", issueID)
		req.Header.Set("Idempotency-Key", requestKey)
		testHandler.RerunIssue(w, req)
		if w.Code != http.StatusAccepted {
			t.Fatalf("rerun status = %d: %s", w.Code, w.Body.String())
		}
		var response AgentTaskResponse
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return response
	}

	first := rerun()
	replay := rerun()
	if first.ID == "" || replay.ID != first.ID {
		t.Fatalf("rerun replay task = %s, want %s", replay.ID, first.ID)
	}
	conflict := httptest.NewRecorder()
	conflictReq := withURLParam(newRequest(http.MethodPost, "/api/issues/"+issueID+"/rerun", map[string]any{
		"task_id": first.ID,
	}), "id", issueID)
	conflictReq.Header.Set("Idempotency-Key", requestKey)
	testHandler.RerunIssue(conflict, conflictReq)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("changed rerun target with same key = %d %s, want 409", conflict.Code, conflict.Body.String())
	}
}

func TestRerunIssueConcurrentReplayCreatesOneTask(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	agentID := createWebhookTestAgent(t, "Issue Rerun Concurrent Agent")
	issueID := createTestIssue(t, "rerun concurrent replay", "medium")
	if _, err := testPool.Exec(context.Background(), `UPDATE issue SET assignee_type = 'agent', assignee_id = $2 WHERE id = $1`, issueID, agentID); err != nil {
		t.Fatal(err)
	}
	requestKey := newIssueRerunKey(t)

	const workers = 8
	responses := make(chan AgentTaskResponse, workers)
	statuses := make(chan int, workers)
	var start sync.WaitGroup
	start.Add(1)
	var done sync.WaitGroup
	done.Add(workers)
	for range workers {
		go func() {
			defer done.Done()
			start.Wait()
			w := httptest.NewRecorder()
			req := withURLParam(newRequest(http.MethodPost, "/api/issues/"+issueID+"/rerun", map[string]any{"target": "current_assignee"}), "id", issueID)
			req.Header.Set("Idempotency-Key", requestKey)
			testHandler.RerunIssue(w, req)
			var response AgentTaskResponse
			_ = json.Unmarshal(w.Body.Bytes(), &response)
			statuses <- w.Code
			responses <- response
		}()
	}
	start.Done()
	done.Wait()
	close(statuses)
	close(responses)

	for status := range statuses {
		if status != http.StatusAccepted {
			t.Fatalf("concurrent rerun status = %d, want 202", status)
		}
	}
	var taskID string
	for response := range responses {
		if taskID == "" {
			taskID = response.ID
		} else if response.ID != taskID {
			t.Fatalf("concurrent rerun task IDs differ: %s and %s", taskID, response.ID)
		}
	}
	var tasks, requests int
	if err := testPool.QueryRow(context.Background(), `
		SELECT
			(SELECT count(*) FROM agent_task_queue WHERE issue_id = $1),
			(SELECT count(*) FROM resource_create_request WHERE resource_type = 'issue_rerun' AND idempotency_key = $2)
	`, issueID, requestKey).Scan(&tasks, &requests); err != nil {
		t.Fatal(err)
	}
	if tasks != 1 || requests != 1 {
		t.Fatalf("concurrent rerun left tasks=%d requests=%d, want 1/1", tasks, requests)
	}
}

func TestRerunIssueCompletionFailureRollsBackCancellationAndTask(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	agentID := createWebhookTestAgent(t, "Issue Rerun Rollback Agent")
	issueID := createTestIssue(t, "rerun completion rollback", "medium")
	if _, err := testPool.Exec(context.Background(), `UPDATE issue SET assignee_type = 'agent', assignee_id = $2 WHERE id = $1`, issueID, agentID); err != nil {
		t.Fatal(err)
	}
	var existingTaskID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority)
		SELECT id, runtime_id, $2, 'queued', 1 FROM agent WHERE id = $1
		RETURNING id
	`, agentID, issueID).Scan(&existingTaskID); err != nil {
		t.Fatal(err)
	}
	requestKey := newIssueRerunKey(t)
	installResourceCreateCompletionFailure(t, resourceTypeIssueRerun, requestKey)

	w := httptest.NewRecorder()
	req := withURLParam(newRequest(http.MethodPost, "/api/issues/"+issueID+"/rerun", map[string]any{"target": "current_assignee"}), "id", issueID)
	req.Header.Set("Idempotency-Key", requestKey)
	testHandler.RerunIssue(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("forced rerun completion failure = %d %s, want 500", w.Code, w.Body.String())
	}
	var active, requests int
	if err := testPool.QueryRow(context.Background(), `
		SELECT
			(SELECT count(*) FROM agent_task_queue WHERE issue_id = $1 AND id = $2 AND status = 'queued'),
			(SELECT count(*) FROM resource_create_request WHERE resource_type = 'issue_rerun' AND idempotency_key = $3)
	`, issueID, existingTaskID, requestKey).Scan(&active, &requests); err != nil {
		t.Fatal(err)
	}
	if active != 1 || requests != 0 {
		t.Fatalf("failed rerun left original_active=%d requests=%d, want 1/0", active, requests)
	}
}
