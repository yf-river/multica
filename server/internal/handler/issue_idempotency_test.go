package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

func createIssueWithKey(t *testing.T, key string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := newRequest(http.MethodPost, "/api/issues?workspace_id="+testWorkspaceID, body)
	req.Header.Set("Idempotency-Key", key)
	testHandler.CreateIssue(w, req)
	return w
}

func TestCreateIssue_IdempotentReplayAndConflict(t *testing.T) {
	key := uuid.NewString()
	title := "idempotent issue " + uuid.NewString()
	cleanupResourceCreateRequest(t, "issue", key, `DELETE FROM issue WHERE workspace_id = $1 AND title = $2`, testWorkspaceID, title)
	body := map[string]any{"title": title, "priority": "high"}

	first := createIssueWithKey(t, key, body)
	if first.Code != http.StatusCreated {
		t.Fatalf("first create = %d %s", first.Code, first.Body.String())
	}
	replay := createIssueWithKey(t, key, body)
	if replay.Code != http.StatusCreated {
		t.Fatalf("replay = %d %s", replay.Code, replay.Body.String())
	}
	if replay.Body.String() != first.Body.String() {
		t.Fatalf("replay body differs\nfirst:  %s\nreplay: %s", first.Body, replay.Body)
	}

	var issue IssueResponse
	if err := json.Unmarshal(first.Body.Bytes(), &issue); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	var issues int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM issue WHERE workspace_id = $1 AND title = $2`, testWorkspaceID, title).Scan(&issues); err != nil {
		t.Fatalf("count issues: %v", err)
	}
	if issues != 1 {
		t.Fatalf("issues = %d, want 1", issues)
	}

	conflict := createIssueWithKey(t, key, map[string]any{"title": title + " changed"})
	if conflict.Code != http.StatusConflict {
		t.Fatalf("changed replay = %d %s, want 409", conflict.Code, conflict.Body.String())
	}
}

func TestCreateIssue_ConcurrentReplayCreatesOneIssue(t *testing.T) {
	key := uuid.NewString()
	title := "concurrent issue " + uuid.NewString()
	cleanupResourceCreateRequest(t, "issue", key, `DELETE FROM issue WHERE workspace_id = $1 AND title = $2`, testWorkspaceID, title)
	body := map[string]any{"title": title}

	assertConcurrentReplay(t, http.StatusCreated, func() *httptest.ResponseRecorder {
		return createIssueWithKey(t, key, body)
	})
	var issues int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM issue WHERE workspace_id = $1 AND title = $2`, testWorkspaceID, title).Scan(&issues); err != nil {
		t.Fatalf("count concurrent issues: %v", err)
	}
	if issues != 1 {
		t.Fatalf("concurrent issues = %d, want 1", issues)
	}
}

func TestCreateIssue_RequestCompletionFailureRollsBackEverything(t *testing.T) {
	ctx := context.Background()
	key := uuid.NewString()
	title := "issue completion rollback " + uuid.NewString()
	cleanupResourceCreateRequest(t, "issue", key, `DELETE FROM issue WHERE workspace_id = $1 AND title = $2`, testWorkspaceID, title)
	installResourceCreateCompletionFailure(t, resourceTypeIssue, key)

	response := createIssueWithKey(t, key, map[string]any{"title": title})
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("create = %d %s, want 500", response.Code, response.Body.String())
	}
	var issues, requests int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM issue WHERE workspace_id = $1 AND title = $2`, testWorkspaceID, title).Scan(&issues); err != nil {
		t.Fatal(err)
	}
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM resource_create_request WHERE workspace_id = $1 AND resource_type = 'issue' AND idempotency_key = $2`, testWorkspaceID, key).Scan(&requests); err != nil {
		t.Fatal(err)
	}
	if issues != 0 || requests != 0 {
		t.Fatalf("issues=%d requests=%d, want atomic 0/0", issues, requests)
	}
}

func TestCreateIssue_ReplayDoesNotRevalidateChangedRelationships(t *testing.T) {
	key := uuid.NewString()
	title := "stable issue replay " + uuid.NewString()
	cleanupResourceCreateRequest(t, "issue", key, `DELETE FROM issue WHERE workspace_id = $1 AND title = $2`, testWorkspaceID, title)
	ctx := context.Background()
	var projectID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO project (workspace_id, title) VALUES ($1, $2) RETURNING id
	`, testWorkspaceID, "temporary replay project "+uuid.NewString()).Scan(&projectID); err != nil {
		t.Fatal(err)
	}

	body := map[string]any{"title": title, "project_id": projectID}
	first := createIssueWithKey(t, key, body)
	if first.Code != http.StatusCreated {
		t.Fatalf("first create = %d %s", first.Code, first.Body.String())
	}
	if _, err := testPool.Exec(ctx, `DELETE FROM project WHERE id = $1`, projectID); err != nil {
		t.Fatalf("delete project after create: %v", err)
	}
	replay := createIssueWithKey(t, key, body)
	if replay.Code != http.StatusCreated || replay.Body.String() != first.Body.String() {
		t.Fatalf("replay after relationship change = %d %s, want exact %s", replay.Code, replay.Body.String(), first.Body.String())
	}
}
