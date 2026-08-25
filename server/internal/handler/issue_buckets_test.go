package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
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
		mustExec(t, context.Background(), `DELETE FROM issue WHERE project_id = $1`, projectID)
		mustExec(t, context.Background(), `DELETE FROM project WHERE id = $1`, projectID)
	})

	insertIssue := func(title, status string, position int, assignee bool) string {
		var number int
		if err := testPool.QueryRow(ctx, `
			UPDATE workspace
			SET issue_counter = GREATEST(issue_counter, (SELECT COALESCE(MAX(number), 0) FROM issue WHERE workspace_id = $1)) + 1
			WHERE id = $1 RETURNING issue_counter
		`, testWorkspaceID).Scan(&number); err != nil {
			t.Fatalf("next issue number: %v", err)
		}
		var id string
		var assigneeType any
		var assigneeID any
		if assignee {
			assigneeType = "member"
			assigneeID = testUserID
		}
		if err := testPool.QueryRow(ctx, `
			INSERT INTO issue (workspace_id, title, status, priority, assignee_type, assignee_id, creator_type, creator_id, position, number, project_id)
			VALUES ($1, $2, $3, 'none', $4, $5, 'member', $6, $7, $8, $9) RETURNING id
		`, testWorkspaceID, title, status, assigneeType, assigneeID, testUserID, position, number, projectID).Scan(&id); err != nil {
			t.Fatalf("create issue %q: %v", title, err)
		}
		return id
	}

	firstTodo := insertIssue(fmt.Sprintf("bucket-todo-first-%d", suffix), "todo", 1, true)
	secondTodo := insertIssue(fmt.Sprintf("bucket-todo-second-%d", suffix), "todo", 2, false)
	doneIssue := insertIssue(fmt.Sprintf("bucket-done-%d", suffix), "done", 1, false)
	var agentID string
	var runtimeID pgtype.UUID
	if err := testPool.QueryRow(ctx, `
		SELECT a.id, a.runtime_id
		  FROM agent a
		 WHERE a.workspace_id = $1
		 ORDER BY a.created_at
		 LIMIT 1
	`, testWorkspaceID).Scan(&agentID, &runtimeID); err != nil {
		t.Fatalf("load test agent: %v", err)
	}
	if _, err := testPool.Exec(ctx, `
		INSERT INTO agent_task_queue (agent_id, issue_id, runtime_id, status, priority)
		VALUES ($1, $2, $3, 'running', 0)
	`, agentID, firstTodo, runtimeID); err != nil {
		t.Fatalf("create running task: %v", err)
	}
	if err := testPool.QueryRow(ctx, `
		UPDATE workspace
		SET issue_counter = GREATEST(issue_counter, (SELECT COALESCE(MAX(number), 0) FROM issue WHERE workspace_id = $1)) + 1
		WHERE id = $1 RETURNING issue_counter
	`, testWorkspaceID).Scan(new(int)); err != nil {
		t.Fatalf("next child issue number: %v", err)
	}
	if _, err := testPool.Exec(ctx, `
		INSERT INTO issue (workspace_id, title, status, priority, creator_type, creator_id, position, number, project_id, parent_issue_id)
		VALUES ($1, $2, 'done', 'none', 'member', $3, 99, (SELECT issue_counter FROM workspace WHERE id = $1), $4, $5)
	`, testWorkspaceID, fmt.Sprintf("bucket-child-%d", suffix), testUserID, projectID, firstTodo); err != nil {
		t.Fatalf("create child issue: %v", err)
	}

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
	if summary := resp.ByStatus["todo"].Issues[0].Assignee; summary == nil || summary.Type != "member" || summary.ID != testUserID || summary.Name == "" {
		t.Fatalf("todo assignee summary missing: %#v", summary)
	}
	if project := resp.ByStatus["todo"].Issues[0].Project; project == nil || project.ID != projectID || project.Title == "" {
		t.Fatalf("todo project summary missing: %#v", project)
	}
	if progress := resp.ByStatus["todo"].Issues[0].ChildProgress; progress == nil || progress.Done != 1 || progress.Total != 1 {
		t.Fatalf("todo child progress mismatch: %#v", progress)
	}
	if activity := resp.ByStatus["todo"].Issues[0].AgentActivity; activity == nil || activity.RunningCount != 1 || activity.QueuedCount != 0 || len(activity.AgentIDs) != 1 || activity.AgentIDs[0] != agentID {
		t.Fatalf("todo agent activity summary mismatch: %#v", activity)
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
