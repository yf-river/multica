package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

func TestCreateFeedbackHappyPath(t *testing.T) {
	clearFeedbackForTestUser(t)

	req := newRequest("POST", "/api/feedback", CreateFeedbackRequest{
		Message: "Love the product, dark mode flashes on startup",
		Kind:    "general",
	})
	w := httptest.NewRecorder()
	testHandler.CreateFeedback(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var resp FeedbackResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.ID == "" {
		t.Fatal("expected feedback id in response")
	}
	var kind string
	if err := testPool.QueryRow(context.Background(),
		`SELECT metadata->>'kind' FROM feedback WHERE id = $1`, resp.ID).Scan(&kind); err != nil {
		t.Fatalf("read persisted feedback kind: %v", err)
	}
	if kind != "general" {
		t.Fatalf("persisted feedback kind = %q, want general", kind)
	}
}

func TestCreateFeedbackRejectsWorkspaceOutsideCallerMembership(t *testing.T) {
	clearFeedbackForTestUser(t)
	ctx := context.Background()
	var workspaceID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, description, issue_prefix)
		VALUES ('Feedback ACL', 'feedback-acl-' || gen_random_uuid(), '', 'FBA')
		RETURNING id
	`).Scan(&workspaceID); err != nil {
		t.Fatalf("create inaccessible workspace: %v", err)
	}
	t.Cleanup(func() { mustExec(t, context.Background(), `DELETE FROM workspace WHERE id = $1`, workspaceID) })

	req := newRequest("POST", "/api/feedback", CreateFeedbackRequest{
		Message:     "must not be attributed across tenants",
		Kind:        "bug",
		WorkspaceID: &workspaceID,
	})
	w := httptest.NewRecorder()
	testHandler.CreateFeedback(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected inaccessible workspace to return 404, got %d: %s", w.Code, w.Body.String())
	}
	var count int
	if err := testPool.QueryRow(ctx,
		`SELECT count(*) FROM feedback WHERE user_id = $1 AND workspace_id = $2`,
		parseUUID(testUserID), workspaceID).Scan(&count); err != nil {
		t.Fatalf("count cross-workspace feedback: %v", err)
	}
	if count != 0 {
		t.Fatalf("cross-workspace request persisted %d feedback rows", count)
	}
}

func TestCreateFeedbackEmptyMessage(t *testing.T) {
	req := newRequest("POST", "/api/feedback", CreateFeedbackRequest{Message: "   "})
	w := httptest.NewRecorder()
	testHandler.CreateFeedback(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCreateFeedbackRequiresCurrentKind(t *testing.T) {
	clearFeedbackForTestUser(t)
	req := newRequest("POST", "/api/feedback", CreateFeedbackRequest{Message: "missing current kind"})
	w := httptest.NewRecorder()

	testHandler.CreateFeedback(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("missing kind: expected 400, got %d: %s", w.Code, w.Body.String())
	}
	var count int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM feedback WHERE user_id = $1`, parseUUID(testUserID)).Scan(&count); err != nil {
		t.Fatalf("count invalid feedback writes: %v", err)
	}
	if count != 0 {
		t.Fatalf("missing kind persisted %d feedback rows", count)
	}
}

func TestCreateFeedbackRateLimit(t *testing.T) {
	clearFeedbackForTestUser(t)

	for i := 0; i < feedbackHourlyRateLimit; i++ {
		req := newRequest("POST", "/api/feedback", CreateFeedbackRequest{
			Message: "feedback #" + strconv.Itoa(i),
			Kind:    "general",
		})
		w := httptest.NewRecorder()
		testHandler.CreateFeedback(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("iteration %d: expected 201, got %d: %s", i, w.Code, w.Body.String())
		}
	}
	req := newRequest("POST", "/api/feedback", CreateFeedbackRequest{Message: "one too many", Kind: "general"})
	w := httptest.NewRecorder()
	testHandler.CreateFeedback(w, req)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d: %s", w.Code, w.Body.String())
	}
}

// clearFeedbackForTestUser wipes all feedback rows for the shared test user
// at both test start (fresh state) and test end (via t.Cleanup), so tests
// in this file don't interfere with each other or with the hourly rate-limit
// window when run in sequence.
func clearFeedbackForTestUser(t *testing.T) {
	t.Helper()
	if _, err := testPool.Exec(context.Background(), `DELETE FROM feedback WHERE user_id = $1`, parseUUID(testUserID)); err != nil {
		t.Fatalf("clear feedback: %v", err)
	}
	t.Cleanup(func() {
		mustExec(t, context.Background(), `DELETE FROM feedback WHERE user_id = $1`, parseUUID(testUserID))
	})
}
