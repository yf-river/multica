package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCreateIssueInvalidStatusReturns400(t *testing.T) {
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title":  "invalid status issue",
		"status": "active",
	})
	testHandler.CreateIssue(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid status, got %d: %s", w.Code, w.Body.String())
	}
	if body := w.Body.String(); !strings.Contains(body, "backlog") {
		t.Errorf("expected error to list valid statuses, got: %s", body)
	}
}

func TestCreateIssueInvalidPriorityReturns400(t *testing.T) {
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title":    "invalid priority issue",
		"priority": "P1",
	})
	testHandler.CreateIssue(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid priority, got %d: %s", w.Code, w.Body.String())
	}
	if body := w.Body.String(); !strings.Contains(body, "urgent") {
		t.Errorf("expected error to list valid priorities, got: %s", body)
	}
}

func TestUpdateIssueInvalidStatusReturns400(t *testing.T) {
	issueID := createTestIssue(t, "update invalid status issue", "todo", "none")
	t.Cleanup(func() { deleteTestIssue(t, issueID) })

	w := httptest.NewRecorder()
	req := newRequest("PUT", "/api/issues/"+issueID, map[string]any{"status": "active"})
	req = withURLParam(req, "id", issueID)
	testHandler.UpdateIssue(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid status, got %d: %s", w.Code, w.Body.String())
	}
}

func TestUpdateIssueInvalidPriorityReturns400(t *testing.T) {
	issueID := createTestIssue(t, "update invalid priority issue", "todo", "none")
	t.Cleanup(func() { deleteTestIssue(t, issueID) })

	w := httptest.NewRecorder()
	req := newRequest("PUT", "/api/issues/"+issueID, map[string]any{"priority": "P1"})
	req = withURLParam(req, "id", issueID)
	testHandler.UpdateIssue(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid priority, got %d: %s", w.Code, w.Body.String())
	}
}

func TestUpdateIssueRejectsCrossWorkspaceProject(t *testing.T) {
	issueID := createTestIssue(t, "update foreign project issue", "todo", "none")
	t.Cleanup(func() { deleteTestIssue(t, issueID) })
	_, foreignProjectID := createForeignProjectForIssueValidation(t, "update-foreign-project")

	w := httptest.NewRecorder()
	req := newRequest("PUT", "/api/issues/"+issueID, map[string]any{"project_id": foreignProjectID})
	req = withURLParam(req, "id", issueID)
	testHandler.UpdateIssue(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for foreign project, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "project not found in this workspace") {
		t.Fatalf("expected project workspace error, got %s", w.Body.String())
	}

	got := getIssueForValidationTest(t, issueID)
	if got.ProjectID != nil {
		t.Fatalf("foreign project update persisted project_id=%v", *got.ProjectID)
	}
}

func TestUpdateIssueRejectsDeepAncestorCycle(t *testing.T) {
	ids := createIssueChainForValidationTest(t, 12)
	rootID := ids[0]
	deepChildID := ids[len(ids)-1]

	w := httptest.NewRecorder()
	req := newRequest("PUT", "/api/issues/"+rootID, map[string]any{"parent_issue_id": deepChildID})
	req = withURLParam(req, "id", rootID)
	testHandler.UpdateIssue(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for deep cycle, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "circular parent relationship detected") {
		t.Fatalf("expected circular parent error, got %s", w.Body.String())
	}

	got := getIssueForValidationTest(t, rootID)
	if got.ParentIssueID != nil {
		t.Fatalf("deep cycle update persisted parent_issue_id=%v", *got.ParentIssueID)
	}
}

func TestBatchUpdateIssuesInvalidStatusReturns400(t *testing.T) {
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/batch-update", map[string]any{
		"issue_ids": []string{"not-needed"},
		"updates": map[string]any{
			"status": "active",
		},
	})
	testHandler.BatchUpdateIssues(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid status, got %d: %s", w.Code, w.Body.String())
	}
}

func TestBatchUpdateIssuesInvalidPriorityReturns400(t *testing.T) {
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/batch-update", map[string]any{
		"issue_ids": []string{"not-needed"},
		"updates": map[string]any{
			"priority": "P1",
		},
	})
	testHandler.BatchUpdateIssues(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid priority, got %d: %s", w.Code, w.Body.String())
	}
}

func TestBatchUpdateIssuesSkipsCrossWorkspaceProject(t *testing.T) {
	issueID := createTestIssue(t, "batch foreign project issue", "todo", "none")
	t.Cleanup(func() { deleteTestIssue(t, issueID) })
	_, foreignProjectID := createForeignProjectForIssueValidation(t, "batch-foreign-project")

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/batch-update", map[string]any{
		"issue_ids": []string{issueID},
		"updates":   map[string]any{"project_id": foreignProjectID},
	})
	testHandler.BatchUpdateIssues(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Updated int `json:"updated"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Updated != 0 {
		t.Fatalf("expected updated=0 for skipped foreign project, got %d", resp.Updated)
	}

	got := getIssueForValidationTest(t, issueID)
	if got.ProjectID != nil {
		t.Fatalf("foreign project batch update persisted project_id=%v", *got.ProjectID)
	}
}

func TestBatchUpdateIssuesSkipsDeepAncestorCycle(t *testing.T) {
	ids := createIssueChainForValidationTest(t, 12)
	rootID := ids[0]
	deepChildID := ids[len(ids)-1]

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/batch-update", map[string]any{
		"issue_ids": []string{rootID},
		"updates":   map[string]any{"parent_issue_id": deepChildID},
	})
	testHandler.BatchUpdateIssues(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Updated int `json:"updated"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Updated != 0 {
		t.Fatalf("expected updated=0 for skipped deep cycle, got %d", resp.Updated)
	}

	got := getIssueForValidationTest(t, rootID)
	if got.ParentIssueID != nil {
		t.Fatalf("deep cycle batch update persisted parent_issue_id=%v", *got.ParentIssueID)
	}
}

func createForeignProjectForIssueValidation(t *testing.T, slugSuffix string) (string, string) {
	t.Helper()
	ctx := context.Background()
	var workspaceID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, description, issue_prefix)
		VALUES ($1, $2, $3, $4)
		RETURNING id
	`, "Issue validation foreign workspace "+slugSuffix, "issue-validation-"+slugSuffix, "Foreign workspace", "IVF").Scan(&workspaceID); err != nil {
		t.Fatalf("insert foreign workspace: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, workspaceID)
	})

	var projectID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO project (workspace_id, title, status, priority)
		VALUES ($1, $2, 'planned', 'none')
		RETURNING id
	`, workspaceID, "Issue validation foreign project").Scan(&projectID); err != nil {
		t.Fatalf("insert foreign project: %v", err)
	}
	return workspaceID, projectID
}

func createIssueChainForValidationTest(t *testing.T, count int) []string {
	t.Helper()
	ctx := context.Background()
	ids := make([]string, 0, count)
	var parent any
	for i := 0; i < count; i++ {
		var id string
		if err := testPool.QueryRow(ctx, `
			INSERT INTO issue (
				workspace_id, title, status, priority, creator_type, creator_id, number, position, parent_issue_id
			) VALUES ($1, $2, 'todo', 'none', 'member', $3, 91000 + $4, $4, $5)
			RETURNING id
		`, testWorkspaceID, "deep parent chain", testUserID, i, parent).Scan(&id); err != nil {
			t.Fatalf("insert issue chain row %d: %v", i, err)
		}
		ids = append(ids, id)
		parent = id
	}
	t.Cleanup(func() {
		for i := len(ids) - 1; i >= 0; i-- {
			testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, ids[i])
		}
	})
	return ids
}

func getIssueForValidationTest(t *testing.T, id string) IssueResponse {
	t.Helper()
	w := httptest.NewRecorder()
	req := newRequest("GET", "/api/issues/"+id, nil)
	req = withURLParam(req, "id", id)
	testHandler.GetIssue(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GetIssue %s: expected 200, got %d: %s", id, w.Code, w.Body.String())
	}
	var got IssueResponse
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode issue: %v", err)
	}
	return got
}
