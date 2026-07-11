package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

func createApprovalProjectionIssue(t *testing.T, projectID, status, assigneeAgentID string) IssueResponse {
	t.Helper()
	body := map[string]any{
		"title":      "approval projection " + uuid.NewString(),
		"project_id": projectID,
		"status":     status,
	}
	if assigneeAgentID != "" {
		body["assignee_type"] = "agent"
		body["assignee_id"] = assigneeAgentID
	}
	w := httptest.NewRecorder()
	req := newRequest(http.MethodPost, "/api/issues?workspace_id="+testWorkspaceID, body)
	testHandler.CreateIssue(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create issue: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var issue IssueResponse
	if err := json.NewDecoder(w.Body).Decode(&issue); err != nil {
		t.Fatalf("decode issue: %v", err)
	}
	t.Cleanup(func() {
		mustExec(t, context.Background(), `DELETE FROM inbox_item WHERE issue_id = $1`, issue.ID)
		mustExec(t, context.Background(), `DELETE FROM agent_task_queue WHERE issue_id = $1`, issue.ID)
		mustExec(t, context.Background(), `DELETE FROM issue WHERE id = $1`, issue.ID)
	})
	return issue
}

func assertIssueAndTaskStatus(t *testing.T, issueID, agentID, wantIssue, wantTask string) {
	t.Helper()
	var issueStatus, taskStatus string
	if err := testPool.QueryRow(context.Background(), `SELECT status FROM issue WHERE id = $1`, issueID).Scan(&issueStatus); err != nil {
		t.Fatalf("reload issue: %v", err)
	}
	if err := testPool.QueryRow(context.Background(), `
		SELECT status FROM agent_task_queue
		WHERE issue_id = $1 AND agent_id = $2 ORDER BY created_at DESC LIMIT 1
	`, issueID, agentID).Scan(&taskStatus); err != nil {
		t.Fatalf("reload task: %v", err)
	}
	if issueStatus != wantIssue || taskStatus != wantTask {
		t.Fatalf("issue/task status = %s/%s, want %s/%s", issueStatus, taskStatus, wantIssue, wantTask)
	}
}

func countActiveApprovalInbox(t *testing.T, issueID, recipientID string) int {
	t.Helper()
	var count int
	if err := testPool.QueryRow(context.Background(), `
		SELECT count(*) FROM inbox_item
		WHERE issue_id = $1 AND recipient_id = $2
		  AND type = 'project_issue_approval_requested' AND archived = false
	`, issueID, recipientID).Scan(&count); err != nil {
		t.Fatalf("count active approval inbox: %v", err)
	}
	return count
}

func TestUpdateIssueMovesExecutionToBacklogAtomically(t *testing.T) {
	projectID := createAtomicityTestProject(t, "member", testUserID)
	agentID := createHandlerTestAgent(t, "backlog-cancel-agent-"+uuid.NewString(), nil)
	issue := createApprovalProjectionIssue(t, projectID, "todo", agentID)

	w := httptest.NewRecorder()
	req := withURLParam(newRequest(http.MethodPut, "/api/issues/"+issue.ID, map[string]any{"status": "backlog"}), "id", issue.ID)
	testHandler.UpdateIssue(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("update issue: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	assertIssueAndTaskStatus(t, issue.ID, agentID, "backlog", "cancelled")
	if got := countActiveApprovalInbox(t, issue.ID, testUserID); got != 1 {
		t.Fatalf("active approval inbox = %d, want 1", got)
	}
}

func TestUpdateIssueRollsBackBacklogAndCancellationWhenApprovalFails(t *testing.T) {
	projectID := createAtomicityTestProject(t, "member", testUserID)
	agentID := createHandlerTestAgent(t, "backlog-rollback-agent-"+uuid.NewString(), nil)
	issue := createApprovalProjectionIssue(t, projectID, "todo", agentID)
	installApprovalInboxFailureTrigger(t)

	w := httptest.NewRecorder()
	req := withURLParam(newRequest(http.MethodPut, "/api/issues/"+issue.ID, map[string]any{"status": "backlog"}), "id", issue.ID)
	testHandler.UpdateIssue(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", w.Code, w.Body.String())
	}
	assertIssueAndTaskStatus(t, issue.ID, agentID, "todo", "queued")
	if got := countActiveApprovalInbox(t, issue.ID, testUserID); got != 0 {
		t.Fatalf("approval inbox survived rollback: %d", got)
	}
	assertNoIssueUpdateEvent(t, issue.ID)
}

func TestUpdateBacklogProjectReplacesPriorApprovalSurfaces(t *testing.T) {
	oldLeadID := createHandlerTestAgent(t, "old-approval-agent-"+uuid.NewString(), nil)
	oldProjectID := createAtomicityTestProject(t, "agent", oldLeadID)
	newProjectID := createAtomicityTestProject(t, "member", testUserID)
	issue := createApprovalProjectionIssue(t, oldProjectID, "backlog", "")

	w := httptest.NewRecorder()
	req := withURLParam(newRequest(http.MethodPut, "/api/issues/"+issue.ID, map[string]any{"project_id": newProjectID}), "id", issue.ID)
	testHandler.UpdateIssue(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("update project: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	assertIssueAndTaskStatus(t, issue.ID, oldLeadID, "backlog", "cancelled")
	if got := countActiveApprovalInbox(t, issue.ID, testUserID); got != 1 {
		t.Fatalf("new approval inbox = %d, want 1", got)
	}
	var metadata map[string]json.RawMessage
	if err := testPool.QueryRow(context.Background(), `SELECT metadata FROM issue WHERE id = $1`, issue.ID).Scan(&metadata); err != nil {
		t.Fatalf("load issue metadata: %v", err)
	}
	for _, key := range projectOwnerApprovalMetadataKeysForTest {
		if _, exists := metadata[key]; exists {
			t.Fatalf("stale approval metadata %q remained after member project switch", key)
		}
	}
}

func TestBatchUpdateCreatesBacklogApprovalProjection(t *testing.T) {
	projectID := createAtomicityTestProject(t, "member", testUserID)
	agentID := createHandlerTestAgent(t, "batch-backlog-agent-"+uuid.NewString(), nil)
	issue := createApprovalProjectionIssue(t, projectID, "todo", agentID)

	w := httptest.NewRecorder()
	req := newRequest(http.MethodPost, "/api/issues/batch-update", map[string]any{
		"issue_ids": []string{issue.ID},
		"updates":   map[string]any{"status": "backlog"},
	})
	testHandler.BatchUpdateIssues(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("batch update: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var response struct {
		Updated int `json:"updated"`
	}
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil || response.Updated != 1 {
		t.Fatalf("batch response = %s, decode error = %v", w.Body.String(), err)
	}
	assertIssueAndTaskStatus(t, issue.ID, agentID, "backlog", "cancelled")
	if got := countActiveApprovalInbox(t, issue.ID, testUserID); got != 1 {
		t.Fatalf("batch approval inbox = %d, want 1", got)
	}
}

func TestBatchUpdateRollsBackWhenBacklogApprovalFails(t *testing.T) {
	projectID := createAtomicityTestProject(t, "member", testUserID)
	agentID := createHandlerTestAgent(t, "batch-backlog-rollback-agent-"+uuid.NewString(), nil)
	issue := createApprovalProjectionIssue(t, projectID, "todo", agentID)
	installApprovalInboxFailureTrigger(t)

	w := httptest.NewRecorder()
	req := newRequest(http.MethodPost, "/api/issues/batch-update", map[string]any{
		"issue_ids": []string{issue.ID},
		"updates":   map[string]any{"status": "backlog"},
	})
	testHandler.BatchUpdateIssues(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("batch update: expected 200 envelope, got %d: %s", w.Code, w.Body.String())
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
	if response.Updated != 0 || len(response.Failed) != 1 || response.Failed[0].IssueID != issue.ID || response.Failed[0].Code != "approval_projection_failed" {
		t.Fatalf("unexpected batch response: %+v", response)
	}
	assertIssueAndTaskStatus(t, issue.ID, agentID, "todo", "queued")
	if got := countActiveApprovalInbox(t, issue.ID, testUserID); got != 0 {
		t.Fatalf("batch approval inbox survived rollback: %d", got)
	}
	assertNoIssueUpdateEvent(t, issue.ID)
}

var projectOwnerApprovalMetadataKeysForTest = []string{
	"project_owner_approval_status",
	"project_owner_approval_mode",
	"project_owner_reviewer_type",
	"project_owner_reviewer_id",
	"project_owner_review_task_id",
}
