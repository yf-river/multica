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

func TestListIssues_ScheduledFilter(t *testing.T) {
	ctx := context.Background()
	suffix := time.Now().UnixNano()

	var projectID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO project (workspace_id, title) VALUES ($1, $2) RETURNING id
	`, testWorkspaceID, fmt.Sprintf("Gantt Scheduled %d", suffix)).Scan(&projectID); err != nil {
		t.Fatalf("create project: %v", err)
	}
	t.Cleanup(func() { mustExec(t, context.Background(), `DELETE FROM project WHERE id = $1`, projectID) })

	insertIssue := func(title string, startDate, dueDate *time.Time) string {
		var id string
		if err := testPool.QueryRow(ctx, `
			INSERT INTO issue (workspace_id, title, status, priority, creator_type, creator_id, position, number, project_id, start_date, due_date)
			VALUES ($1, $2, 'todo', 'none', 'member', $3, 0, $4, $5, $6, $7) RETURNING id
		`, testWorkspaceID, title, testUserID, nextHandlerTestIssueNumber(t), projectID, startDate, dueDate).Scan(&id); err != nil {
			t.Fatalf("create issue %q: %v", title, err)
		}
		t.Cleanup(func() { mustExec(t, context.Background(), `DELETE FROM issue WHERE id = $1`, id) })
		return id
	}

	start := time.Now().UTC().Truncate(24 * time.Hour)
	due := start.Add(72 * time.Hour)
	withStart := insertIssue(fmt.Sprintf("with-start-%d", suffix), &start, nil)
	withDue := insertIssue(fmt.Sprintf("with-due-%d", suffix), nil, &due)
	withBoth := insertIssue(fmt.Sprintf("with-both-%d", suffix), &start, &due)
	noDates := insertIssue(fmt.Sprintf("no-dates-%d", suffix), nil, nil)

	list := func(query string) (ids []string, total int64) {
		path := fmt.Sprintf("/api/issues?workspace_id=%s&project_id=%s&limit=500%s",
			testWorkspaceID, projectID, query)
		w := httptest.NewRecorder()
		testHandler.ListIssues(w, newRequest("GET", path, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("ListIssues: expected 200, got %d: %s", w.Code, w.Body.String())
		}
		var resp struct {
			Issues []IssueResponse `json:"issues"`
			Total  int64           `json:"total"`
		}
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("decode list response: %v", err)
		}
		for _, iss := range resp.Issues {
			ids = append(ids, iss.ID)
		}
		return ids, resp.Total
	}

	allIDs, allTotal := list("")
	for _, want := range []string{withStart, withDue, withBoth, noDates} {
		if !containsIssueID(allIDs, want) {
			t.Fatalf("baseline list missing %s — all=%v", want, allIDs)
		}
	}
	if allTotal != 4 {
		t.Fatalf("baseline total: want 4, got %d", allTotal)
	}

	scheduledIDs, scheduledTotal := list("&scheduled=true")
	for _, want := range []string{withStart, withDue, withBoth} {
		if !containsIssueID(scheduledIDs, want) {
			t.Fatalf("scheduled list missing %s — got %v", want, scheduledIDs)
		}
	}
	if containsIssueID(scheduledIDs, noDates) {
		t.Fatalf("scheduled list unexpectedly includes undated issue %s", noDates)
	}
	if scheduledTotal != 3 {
		t.Fatalf("scheduled total: want 3, got %d", scheduledTotal)
	}
}
