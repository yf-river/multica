package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

func TestBatchUpdateNoMutationReturnsZero(t *testing.T) {
	a := createTestIssue(t, "BU-no-mut A", "low")
	b := createTestIssue(t, "BU-no-mut B", "low")
	t.Cleanup(func() { deleteTestIssue(t, a) })
	t.Cleanup(func() { deleteTestIssue(t, b) })

	cases := []struct {
		desc string
		body map[string]any
	}{
		{
			desc: "updates_missing",
			body: map[string]any{"issue_ids": []string{a, b}, "status": "in_progress"},
		},
		{
			desc: "updates_empty_object",
			body: map[string]any{"issue_ids": []string{a, b}, "updates": map[string]any{}},
		},
		{
			desc: "updates_misnamed",
			body: map[string]any{"issue_ids": []string{a, b}, "update": map[string]any{"status": "done"}},
		},
		{
			desc: "updates_unknown_field_only",
			body: map[string]any{"issue_ids": []string{a, b}, "updates": map[string]any{"foo": "bar"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := newRequest("POST", "/api/issues/batch-update", tc.body)
			testHandler.BatchUpdateIssues(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
			}
			var resp struct {
				Updated int `json:"updated"`
			}
			if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
				t.Fatalf("decode batch response: %v", err)
			}
			if resp.Updated != 0 {
				t.Errorf("expected updated=0 when no mutation field present, got %d", resp.Updated)
			}

			for _, id := range []string{a, b} {
				if got := getIssueStatus(t, id); got != "todo" {
					t.Errorf("issue %s: status changed to %q despite no-mutation request", id, got)
				}
			}
		})
	}
}

func TestBatchUpdateValidUpdatesPersistAndCount(t *testing.T) {
	a := createTestIssue(t, "BU-ok A", "low")
	b := createTestIssue(t, "BU-ok B", "low")
	t.Cleanup(func() { deleteTestIssue(t, a) })
	t.Cleanup(func() { deleteTestIssue(t, b) })

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/batch-update", map[string]any{
		"issue_ids": []string{a, b},
		"updates":   map[string]any{"status": "in_progress"},
	})
	testHandler.BatchUpdateIssues(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Updated int `json:"updated"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode batch response: %v", err)
	}
	if resp.Updated != 2 {
		t.Errorf("expected updated=2, got %d", resp.Updated)
	}
	for _, id := range []string{a, b} {
		if got := getIssueStatus(t, id); got != "in_progress" {
			t.Errorf("issue %s: expected status=in_progress, got %q", id, got)
		}
		var eventCount int
		if err := testPool.QueryRow(context.Background(), `
			SELECT count(*)
			FROM domain_event_outbox
			WHERE event_type = 'issue:updated'
			  AND stream_key = 'issue:' || $1
			  AND payload->>'status_changed' = 'true'
			  AND payload->>'prev_status' = 'todo'
		`, id).Scan(&eventCount); err != nil {
			t.Fatalf("count batch issue event: %v", err)
		}
		if eventCount != 1 {
			t.Errorf("issue %s: durable update events = %d, want 1", id, eventCount)
		}
	}
}

func TestBatchUpdateReportsEverySkippedItem(t *testing.T) {
	issueID := createTestIssue(t, "BU-partial", "low")
	t.Cleanup(func() { deleteTestIssue(t, issueID) })
	missingID := uuid.NewString()

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/batch-update", map[string]any{
		"issue_ids": []string{issueID, "not-a-uuid", missingID},
		"updates":   map[string]any{"priority": "high"},
	})
	testHandler.BatchUpdateIssues(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var response struct {
		Updated int `json:"updated"`
		Failed  []struct {
			IssueID string `json:"issue_id"`
			Code    string `json:"code"`
		} `json:"failed"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Updated != 1 || len(response.Failed) != 2 {
		t.Fatalf("response = %#v", response)
	}
	if response.Failed[0].IssueID != "not-a-uuid" || response.Failed[0].Code != "invalid_id" {
		t.Fatalf("invalid-id failure = %#v", response.Failed[0])
	}
	if response.Failed[1].IssueID != missingID || response.Failed[1].Code != "not_found" {
		t.Fatalf("missing-id failure = %#v", response.Failed[1])
	}
}

func TestBatchUpdateReportsInvalidFieldCodes(t *testing.T) {
	issueID := createTestIssue(t, "BU-invalid-fields", "low")
	t.Cleanup(func() { deleteTestIssue(t, issueID) })

	cases := []struct {
		name    string
		updates map[string]any
		code    string
	}{
		{name: "assignee", updates: map[string]any{"assignee_id": "invalid"}, code: "invalid_assignee"},
		{name: "start date", updates: map[string]any{"start_date": "2026-13-01"}, code: "invalid_start_date"},
		{name: "due date", updates: map[string]any{"due_date": "not-a-date"}, code: "invalid_due_date"},
		{name: "parent", updates: map[string]any{"parent_issue_id": "invalid"}, code: "invalid_parent"},
		{name: "project", updates: map[string]any{"project_id": "invalid"}, code: "invalid_project"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := newRequest(http.MethodPost, "/api/issues/batch-update", map[string]any{
				"issue_ids": []string{issueID},
				"updates":   tc.updates,
			})
			testHandler.BatchUpdateIssues(w, req)
			assertBatchIssueFailure(t, w, issueID, tc.code)
		})
	}
}

func createTestIssue(t *testing.T, title, priority string) string {
	t.Helper()
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title":    title,
		"status":   "todo",
		"priority": priority,
	})
	testHandler.CreateIssue(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateIssue %q: expected 201, got %d: %s", title, w.Code, w.Body.String())
	}
	var issue IssueResponse
	if err := json.NewDecoder(w.Body).Decode(&issue); err != nil {
		t.Fatalf("decode issue response: %v", err)
	}
	return issue.ID
}

func deleteTestIssue(t *testing.T, id string) {
	t.Helper()
	w := httptest.NewRecorder()
	req := newRequest("DELETE", "/api/issues/"+id, nil)
	req = withURLParam(req, "id", id)
	testHandler.DeleteIssue(w, req)
	mustExec(t, context.Background(), `DELETE FROM domain_event_outbox WHERE stream_key = 'issue:' || $1`, id)
}
