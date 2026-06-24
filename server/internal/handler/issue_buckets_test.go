package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestListIssueBucketsReturnsFirstPagePerStatus(t *testing.T) {
	ctx := context.Background()
	suffix := time.Now().UnixNano()

	var projectID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO project (workspace_id, title) VALUES ($1, $2) RETURNING id
	`, testWorkspaceID, fmt.Sprintf("Bucketed Board %d", suffix)).Scan(&projectID); err != nil {
		t.Fatalf("create project: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM issue WHERE project_id = $1`, projectID)
		testPool.Exec(context.Background(), `DELETE FROM project WHERE id = $1`, projectID)
	})

	insertIssue := func(title, status string, position int) string {
		var number int
		if err := testPool.QueryRow(ctx, `
			UPDATE workspace
			SET issue_counter = GREATEST(issue_counter, (SELECT COALESCE(MAX(number), 0) FROM issue WHERE workspace_id = $1)) + 1
			WHERE id = $1 RETURNING issue_counter
		`, testWorkspaceID).Scan(&number); err != nil {
			t.Fatalf("next issue number: %v", err)
		}
		var id string
		if err := testPool.QueryRow(ctx, `
			INSERT INTO issue (workspace_id, title, status, priority, creator_type, creator_id, position, number, project_id)
			VALUES ($1, $2, $3, 'none', 'member', $4, $5, $6, $7) RETURNING id
		`, testWorkspaceID, title, status, testUserID, position, number, projectID).Scan(&id); err != nil {
			t.Fatalf("create issue %q: %v", title, err)
		}
		return id
	}

	firstTodo := insertIssue(fmt.Sprintf("bucket-todo-first-%d", suffix), "todo", 1)
	secondTodo := insertIssue(fmt.Sprintf("bucket-todo-second-%d", suffix), "todo", 2)
	doneIssue := insertIssue(fmt.Sprintf("bucket-done-%d", suffix), "done", 1)

	path := fmt.Sprintf(
		"/api/issues/buckets?workspace_id=%s&project_id=%s&statuses=todo,done,blocked&limit=1&sort=position",
		testWorkspaceID,
		projectID,
	)
	w := httptest.NewRecorder()
	testHandler.ListIssueBuckets(w, newRequest("GET", path, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("ListIssueBuckets: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp ListIssueBucketsResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode buckets response: %v", err)
	}

	if got := resp.ByStatus["todo"].Issues; len(got) != 1 || got[0].ID != firstTodo {
		t.Fatalf("todo first page: want [%s], got %#v", firstTodo, got)
	}
	if total := resp.ByStatus["todo"].Total; total != 2 {
		t.Fatalf("todo total: want 2, got %d", total)
	}
	if got := resp.ByStatus["done"].Issues; len(got) != 1 || got[0].ID != doneIssue {
		t.Fatalf("done first page: want [%s], got %#v", doneIssue, got)
	}
	if got := resp.ByStatus["blocked"].Issues; len(got) != 0 || resp.ByStatus["blocked"].Total != 0 {
		t.Fatalf("blocked empty bucket: got %#v", resp.ByStatus["blocked"])
	}
	if resp.ByStatus["todo"].Issues[0].ID == secondTodo {
		t.Fatalf("limit=1 should not return second todo issue %s", secondTodo)
	}
}

func TestListIssueBucketsRejectsInvalidStatus(t *testing.T) {
	path := fmt.Sprintf("/api/issues/buckets?workspace_id=%s&statuses=todo,active", testWorkspaceID)
	w := httptest.NewRecorder()
	testHandler.ListIssueBuckets(w, newRequest("GET", path, nil))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("ListIssueBuckets invalid status: expected 400, got %d: %s", w.Code, w.Body.String())
	}
}
