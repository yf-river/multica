package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIssueMutationsRejectInvalidStatusAndPriority(t *testing.T) {
	for _, mutation := range []string{"create", "update", "batch"} {
		for _, field := range []struct {
			name, value, errorTerm string
		}{
			{name: "status", value: "active", errorTerm: "backlog"},
			{name: "priority", value: "P1", errorTerm: "urgent"},
		} {
			t.Run(mutation+" "+field.name, func(t *testing.T) {
				w := httptest.NewRecorder()
				update := map[string]any{field.name: field.value}
				switch mutation {
				case "create":
					update["title"] = "invalid " + field.name + " issue"
					testHandler.CreateIssue(w, newRequest(http.MethodPost, "/api/issues?workspace_id="+testWorkspaceID, update))
				case "update":
					issueID := createTestIssue(t, "update invalid "+field.name+" issue", "none")
					t.Cleanup(func() { deleteTestIssue(t, issueID) })
					req := withURLParam(newRequest(http.MethodPut, "/api/issues/"+issueID, update), "id", issueID)
					testHandler.UpdateIssue(w, req)
				case "batch":
					testHandler.BatchUpdateIssues(w, newRequest(http.MethodPost, "/api/issues/batch-update", map[string]any{
						"issue_ids": []string{"not-needed"}, "updates": update,
					}))
				}
				if w.Code != http.StatusBadRequest {
					t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
				}
				if mutation == "create" && !strings.Contains(w.Body.String(), field.errorTerm) {
					t.Fatalf("error does not list current %s values: %s", field.name, w.Body.String())
				}
			})
		}
	}
}

func TestUpdateIssueRejectsCrossWorkspaceProject(t *testing.T) {
	issueID := createTestIssue(t, "update foreign project issue", "none")
	t.Cleanup(func() { deleteTestIssue(t, issueID) })
	foreignProjectID := createForeignProjectForIssueValidation(t, "update-foreign-project")

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

func TestBatchUpdateIssuesSkipsCrossWorkspaceProject(t *testing.T) {
	issueID := createTestIssue(t, "batch foreign project issue", "none")
	t.Cleanup(func() { deleteTestIssue(t, issueID) })
	foreignProjectID := createForeignProjectForIssueValidation(t, "batch-foreign-project")

	updated := batchUpdateIssueCount(t, map[string]any{
		"issue_ids": []string{issueID},
		"updates":   map[string]any{"project_id": foreignProjectID},
	})
	if updated != 0 {
		t.Fatalf("expected updated=0 for skipped foreign project, got %d", updated)
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

	updated := batchUpdateIssueCount(t, map[string]any{
		"issue_ids": []string{rootID},
		"updates":   map[string]any{"parent_issue_id": deepChildID},
	})
	if updated != 0 {
		t.Fatalf("expected updated=0 for skipped deep cycle, got %d", updated)
	}

	got := getIssueForValidationTest(t, rootID)
	if got.ParentIssueID != nil {
		t.Fatalf("deep cycle batch update persisted parent_issue_id=%v", *got.ParentIssueID)
	}
}

func batchUpdateIssueCount(t *testing.T, body map[string]any) int {
	t.Helper()
	w := httptest.NewRecorder()
	testHandler.BatchUpdateIssues(w, newRequest(http.MethodPost, "/api/issues/batch-update", body))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 batch envelope, got %d: %s", w.Code, w.Body.String())
	}
	var response struct {
		Updated int `json:"updated"`
	}
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatalf("decode batch response: %v", err)
	}
	return response.Updated
}

func createForeignProjectForIssueValidation(t *testing.T, slugSuffix string) string {
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
		mustExec(t, context.Background(), `DELETE FROM workspace WHERE id = $1`, workspaceID)
	})

	var projectID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO project (workspace_id, title, status, priority)
		VALUES ($1, $2, 'planned', 'none')
		RETURNING id
	`, workspaceID, "Issue validation foreign project").Scan(&projectID); err != nil {
		t.Fatalf("insert foreign project: %v", err)
	}
	return projectID
}

func createIssueChainForValidationTest(t *testing.T, count int) []string {
	t.Helper()
	ctx := context.Background()
	ids := make([]string, 0, count)
	var parent any
	for i := 0; i < count; i++ {
		var id string
		number := nextHandlerTestIssueNumber(t)
		if err := testPool.QueryRow(ctx, `
			INSERT INTO issue (
				workspace_id, title, status, priority, creator_type, creator_id, number, position, parent_issue_id
			) VALUES ($1, $2, 'todo', 'none', 'member', $3, $4, $5, $6)
			RETURNING id
		`, testWorkspaceID, "deep parent chain", testUserID, number, i, parent).Scan(&id); err != nil {
			t.Fatalf("insert issue chain row %d: %v", i, err)
		}
		ids = append(ids, id)
		parent = id
	}
	t.Cleanup(func() {
		for i := len(ids) - 1; i >= 0; i-- {
			mustExec(t, context.Background(), `DELETE FROM issue WHERE id = $1`, ids[i])
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
