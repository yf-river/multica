package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/google/uuid"
)

func newFeedbackRequest(body createFeedbackRequest) *http.Request {
	req := newRequest("POST", "/api/feedback", body)
	req.Header.Set("Idempotency-Key", uuid.NewString())
	return req
}

func TestCreateFeedbackHappyPath(t *testing.T) {
	clearFeedbackForTestUser(t)

	req := newFeedbackRequest(createFeedbackRequest{
		Message: "Love the product, dark mode flashes on startup",
		Kind:    "general",
	})
	w := httptest.NewRecorder()
	testHandler.CreateFeedback(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var resp feedbackResponse
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

	req := newFeedbackRequest(createFeedbackRequest{
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

func TestCreateFeedbackReplaysCommittedResponse(t *testing.T) {
	clearFeedbackForTestUser(t)
	key := "d2b7cb04-1c8f-4e67-8587-e420e4141de2"
	create := func() feedbackResponse {
		req := newRequest("POST", "/api/feedback", createFeedbackRequest{
			Message: "same submission after an unknown response",
			Kind:    "general",
		})
		req.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		testHandler.CreateFeedback(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("create feedback: got %d: %s", w.Code, w.Body.String())
		}
		var response feedbackResponse
		if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
			t.Fatalf("decode feedback response: %v", err)
		}
		return response
	}

	first, replay := create(), create()
	if replay != first {
		t.Fatalf("replayed response = %+v, want exact %+v", replay, first)
	}
	var count int
	if err := testPool.QueryRow(context.Background(),
		`SELECT count(*) FROM feedback WHERE user_id = $1 AND message = $2`,
		parseUUID(testUserID), "same submission after an unknown response").Scan(&count); err != nil {
		t.Fatalf("count replayed feedback: %v", err)
	}
	if count != 1 {
		t.Fatalf("same request persisted %d feedback rows, want 1", count)
	}

	changed := newRequest("POST", "/api/feedback", createFeedbackRequest{
		Message: "same submission after an unknown response",
		Kind:    "bug",
	})
	changed.Header.Set("Idempotency-Key", key)
	w := httptest.NewRecorder()
	testHandler.CreateFeedback(w, changed)
	if w.Code != http.StatusConflict {
		t.Fatalf("changed request with reused key: got %d: %s", w.Code, w.Body.String())
	}
}

func TestCreateFeedbackEmptyMessage(t *testing.T) {
	req := newFeedbackRequest(createFeedbackRequest{Message: "   "})
	w := httptest.NewRecorder()
	testHandler.CreateFeedback(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCreateFeedbackRequiresCurrentKind(t *testing.T) {
	clearFeedbackForTestUser(t)
	req := newFeedbackRequest(createFeedbackRequest{Message: "missing current kind"})
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
		req := newFeedbackRequest(createFeedbackRequest{
			Message: "feedback #" + strconv.Itoa(i),
			Kind:    "general",
		})
		w := httptest.NewRecorder()
		testHandler.CreateFeedback(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("iteration %d: expected 201, got %d: %s", i, w.Code, w.Body.String())
		}
	}
	req := newFeedbackRequest(createFeedbackRequest{Message: "one too many", Kind: "general"})
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
