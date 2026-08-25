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

func TestListIssuesPaginationBounds(t *testing.T) {
	const seeded = 101
	ctx := context.Background()
	suffix := time.Now().UnixNano()

	var projectID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO project (workspace_id, title) VALUES ($1, $2) RETURNING id
	`, testWorkspaceID, fmt.Sprintf("Pagination Bounds %d", suffix)).Scan(&projectID); err != nil {
		t.Fatalf("create project: %v", err)
	}
	t.Cleanup(func() {
		mustExec(t, context.Background(), `DELETE FROM issue WHERE project_id = $1`, projectID)
		mustExec(t, context.Background(), `DELETE FROM project WHERE id = $1`, projectID)
	})

	if _, err := testPool.Exec(ctx, `
		WITH reserved AS (
			UPDATE workspace
			SET issue_counter = GREATEST(
				issue_counter,
				(SELECT COALESCE(MAX(number), 0) FROM issue WHERE workspace_id = $1)
			) + $2
			WHERE id = $1
			RETURNING issue_counter - $2 + 1 AS first_number
		)
		INSERT INTO issue (
			workspace_id, title, status, priority, creator_type,
			creator_id, position, number, project_id
		)
		SELECT $1, $3 || '-' || n, 'todo', 'none', 'member',
			$4, 0, reserved.first_number + n, $5
		FROM reserved, generate_series(0, $2 - 1) AS n
	`, testWorkspaceID, seeded, fmt.Sprintf("pagination-%d", suffix), testUserID, projectID); err != nil {
		t.Fatalf("seed issues: %v", err)
	}

	type listResponse struct {
		Issues []IssueResponse `json:"issues"`
		Total  int64           `json:"total"`
	}
	requestPage := func(t *testing.T, query string) listResponse {
		t.Helper()
		path := fmt.Sprintf(
			"/api/issues?workspace_id=%s&project_id=%s%s",
			testWorkspaceID,
			projectID,
			query,
		)
		w := httptest.NewRecorder()
		testHandler.ListIssues(w, newRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("ListIssues(%q) = %d: %s", query, w.Code, w.Body.String())
		}
		var response listResponse
		if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
			t.Fatalf("decode ListIssues(%q): %v", query, err)
		}
		return response
	}

	for _, tc := range []struct {
		name  string
		query string
		want  int
	}{
		{name: "default", want: 100},
		{name: "negative limit uses default", query: "&limit=-1", want: 100},
		{name: "negative offset uses zero", query: "&offset=-1", want: 100},
		{name: "negative limit and offset", query: "&limit=-1&offset=-1", want: 100},
		{name: "invalid limit uses default", query: "&limit=abc", want: 100},
		{name: "invalid offset uses zero", query: "&offset=abc", want: 100},
		{name: "small limit", query: "&limit=1", want: 1},
		{name: "middle limit", query: "&limit=50", want: 50},
		{name: "clamp boundary", query: "&limit=100", want: 100},
		{name: "huge limit is clamped", query: "&limit=100000000", want: 100},
		{name: "offset reaches final row", query: "&limit=2&offset=100", want: 1},
		{name: "clamp composes with offset", query: "&limit=200&offset=50", want: 51},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := requestPage(t, tc.query)
			if got := len(response.Issues); got != tc.want {
				t.Fatalf("len(issues) = %d, want %d", got, tc.want)
			}
			if response.Total != seeded {
				t.Fatalf("total = %d, want %d", response.Total, seeded)
			}
		})
	}
}
