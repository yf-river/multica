package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func issueSubscriberExists(t *testing.T, ctx context.Context, issueID, userType, userID string) bool {
	t.Helper()
	subscribers, err := testHandler.Queries.ListIssueSubscribers(ctx, parseUUID(issueID))
	if err != nil {
		t.Fatalf("ListIssueSubscribers: %v", err)
	}
	wantUserID := parseUUID(userID)
	for _, subscriber := range subscribers {
		if subscriber.UserType == userType && subscriber.UserID == wantUserID {
			return true
		}
	}
	return false
}

func TestSubscriberAPI(t *testing.T) {
	ctx := context.Background()

	// Helper: create an issue for subscriber tests
	createIssue := func(t *testing.T) string {
		t.Helper()
		w := httptest.NewRecorder()
		req := newRequest("POST", "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
			"title": "Subscriber test issue",
		})
		testHandler.CreateIssue(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("CreateIssue: expected 201, got %d: %s", w.Code, w.Body.String())
		}
		var issue IssueResponse
		if err := json.NewDecoder(w.Body).Decode(&issue); err != nil {
			t.Fatalf("decode issue response: %v", err)
		}
		return issue.ID
	}

	// Helper: delete an issue
	deleteIssue := func(t *testing.T, issueID string) {
		t.Helper()
		w := httptest.NewRecorder()
		req := newRequest("DELETE", "/api/issues/"+issueID, nil)
		req = withURLParam(req, "id", issueID)
		testHandler.DeleteIssue(w, req)
	}

	t.Run("Subscribe", func(t *testing.T) {
		issueID := createIssue(t)
		defer deleteIssue(t, issueID)

		w := httptest.NewRecorder()
		req := newRequest("POST", "/api/issues/"+issueID+"/subscribe", nil)
		req = withURLParam(req, "id", issueID)
		testHandler.SubscribeToIssue(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("SubscribeToIssue: expected 200, got %d: %s", w.Code, w.Body.String())
		}

		var resp map[string]bool
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("decode subscriber response: %v", err)
		}
		if !resp["subscribed"] {
			t.Fatal("SubscribeToIssue: expected subscribed=true")
		}

		// Verify in DB
		if !issueSubscriberExists(t, ctx, issueID, "member", testUserID) {
			t.Fatal("expected user to be subscribed in DB")
		}
	})

	t.Run("SubscribeIdempotent", func(t *testing.T) {
		issueID := createIssue(t)
		defer deleteIssue(t, issueID)

		// Subscribe first time
		w := httptest.NewRecorder()
		req := newRequest("POST", "/api/issues/"+issueID+"/subscribe", nil)
		req = withURLParam(req, "id", issueID)
		testHandler.SubscribeToIssue(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("SubscribeToIssue (1st): expected 200, got %d: %s", w.Code, w.Body.String())
		}

		// Subscribe second time — should also succeed
		w = httptest.NewRecorder()
		req = newRequest("POST", "/api/issues/"+issueID+"/subscribe", nil)
		req = withURLParam(req, "id", issueID)
		testHandler.SubscribeToIssue(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("SubscribeToIssue (2nd): expected 200, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("RejectMalformedRequestBodies", func(t *testing.T) {
		issueID := createIssue(t)
		defer deleteIssue(t, issueID)

		for _, action := range []struct {
			name   string
			path   string
			handle func(http.ResponseWriter, *http.Request)
		}{
			{name: "subscribe", path: "/subscribe", handle: testHandler.SubscribeToIssue},
			{name: "unsubscribe", path: "/unsubscribe", handle: testHandler.UnsubscribeFromIssue},
		} {
			t.Run(action.name, func(t *testing.T) {
				w := httptest.NewRecorder()
				req := newRequest("POST", "/api/issues/"+issueID+action.path, json.RawMessage(`{"user_id":`))
				req = withURLParam(req, "id", issueID)
				action.handle(w, req)
				if w.Code != http.StatusBadRequest {
					t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
				}
			})
		}
	})

	t.Run("ListSubscribers", func(t *testing.T) {
		issueID := createIssue(t)
		defer deleteIssue(t, issueID)

		// Subscribe first
		w := httptest.NewRecorder()
		req := newRequest("POST", "/api/issues/"+issueID+"/subscribe", nil)
		req = withURLParam(req, "id", issueID)
		testHandler.SubscribeToIssue(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("SubscribeToIssue: expected 200, got %d: %s", w.Code, w.Body.String())
		}

		// List
		w = httptest.NewRecorder()
		req = newRequest("GET", "/api/issues/"+issueID+"/subscribers", nil)
		req = withURLParam(req, "id", issueID)
		testHandler.ListIssueSubscribers(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("ListIssueSubscribers: expected 200, got %d: %s", w.Code, w.Body.String())
		}

		var subscribers []SubscriberResponse
		if err := json.NewDecoder(w.Body).Decode(&subscribers); err != nil {
			t.Fatalf("decode subscribers response: %v", err)
		}
		if len(subscribers) == 0 {
			t.Fatal("ListIssueSubscribers: expected at least 1 subscriber")
		}
		found := false
		for _, s := range subscribers {
			if s.UserID == testUserID && s.UserType == "member" && s.Reason == "manual" {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("ListIssueSubscribers: expected to find test user subscriber, got %+v", subscribers)
		}
	})

	t.Run("Unsubscribe", func(t *testing.T) {
		issueID := createIssue(t)
		defer deleteIssue(t, issueID)

		// Subscribe first
		w := httptest.NewRecorder()
		req := newRequest("POST", "/api/issues/"+issueID+"/subscribe", nil)
		req = withURLParam(req, "id", issueID)
		testHandler.SubscribeToIssue(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("SubscribeToIssue: expected 200, got %d: %s", w.Code, w.Body.String())
		}

		// Unsubscribe
		w = httptest.NewRecorder()
		req = newRequest("POST", "/api/issues/"+issueID+"/unsubscribe", nil)
		req = withURLParam(req, "id", issueID)
		testHandler.UnsubscribeFromIssue(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("UnsubscribeFromIssue: expected 200, got %d: %s", w.Code, w.Body.String())
		}

		var resp map[string]bool
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("decode subscriber response: %v", err)
		}
		if resp["subscribed"] {
			t.Fatal("UnsubscribeFromIssue: expected subscribed=false")
		}

		// Verify in DB
		if issueSubscriberExists(t, ctx, issueID, "member", testUserID) {
			t.Fatal("expected user to NOT be subscribed in DB")
		}
	})

	t.Run("SubscribeCrossWorkspaceUser", func(t *testing.T) {
		issueID := createIssue(t)
		defer deleteIssue(t, issueID)

		foreignUserID := "00000000-0000-0000-0000-000000000099"
		w := httptest.NewRecorder()
		req := newRequest("POST", "/api/issues/"+issueID+"/subscribe", map[string]any{
			"user_id":   foreignUserID,
			"user_type": "member",
		})
		req = withURLParam(req, "id", issueID)
		testHandler.SubscribeToIssue(w, req)
		if w.Code != http.StatusForbidden {
			t.Fatalf("SubscribeToIssue with cross-workspace user: expected 403, got %d: %s", w.Code, w.Body.String())
		}

		if issueSubscriberExists(t, ctx, issueID, "member", foreignUserID) {
			t.Fatal("cross-workspace user should NOT be subscribed in DB")
		}
	})

	t.Run("UnsubscribeCrossWorkspaceUser", func(t *testing.T) {
		issueID := createIssue(t)
		defer deleteIssue(t, issueID)

		foreignUserID := "00000000-0000-0000-0000-000000000099"
		w := httptest.NewRecorder()
		req := newRequest("POST", "/api/issues/"+issueID+"/unsubscribe", map[string]any{
			"user_id":   foreignUserID,
			"user_type": "member",
		})
		req = withURLParam(req, "id", issueID)
		testHandler.UnsubscribeFromIssue(w, req)
		if w.Code != http.StatusForbidden {
			t.Fatalf("UnsubscribeFromIssue with cross-workspace user: expected 403, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("AgentCallerSubscribesItself", func(t *testing.T) {
		issueID := createIssue(t)
		defer deleteIssue(t, issueID)

		// Look up the agent created by the handler test fixture.
		var agentID string
		err := testPool.QueryRow(ctx,
			`SELECT id FROM agent WHERE workspace_id = $1 AND name = $2`,
			testWorkspaceID, "Handler Test Agent",
		).Scan(&agentID)
		if err != nil {
			t.Fatalf("failed to find test agent: %v", err)
		}

		// Subscribe as the task-token agent — no body, so the handler defaults
		// to the agent itself rather than the member behind X-User-ID.
		agentTask := createHandlerTestTaskForAgent(t, agentID)
		w := httptest.NewRecorder()
		req := newRequest("POST", "/api/issues/"+issueID+"/subscribe", nil)
		req = withURLParam(req, "id", issueID)
		setTaskTokenActor(req, agentID, agentTask)
		testHandler.SubscribeToIssue(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("SubscribeToIssue (agent caller): expected 200, got %d: %s", w.Code, w.Body.String())
		}

		agentSubscribed := issueSubscriberExists(t, ctx, issueID, "agent", agentID)
		if !agentSubscribed {
			t.Fatal("expected agent to be subscribed in DB when X-Agent-ID is set")
		}

		memberSubscribed := issueSubscriberExists(t, ctx, issueID, "member", testUserID)
		if memberSubscribed {
			t.Fatal("member must not be auto-subscribed when caller is an agent")
		}

		// Unsubscribe through the same task-token actor.
		w = httptest.NewRecorder()
		req = newRequest("POST", "/api/issues/"+issueID+"/unsubscribe", nil)
		req = withURLParam(req, "id", issueID)
		setTaskTokenActor(req, agentID, agentTask)
		testHandler.UnsubscribeFromIssue(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("UnsubscribeFromIssue (agent caller): expected 200, got %d: %s", w.Code, w.Body.String())
		}

		agentSubscribed = issueSubscriberExists(t, ctx, issueID, "agent", agentID)
		if agentSubscribed {
			t.Fatal("expected agent to be unsubscribed in DB when X-Agent-ID is set")
		}
	})

	t.Run("ListAfterUnsubscribe", func(t *testing.T) {
		issueID := createIssue(t)
		defer deleteIssue(t, issueID)

		// Subscribe
		w := httptest.NewRecorder()
		req := newRequest("POST", "/api/issues/"+issueID+"/subscribe", nil)
		req = withURLParam(req, "id", issueID)
		testHandler.SubscribeToIssue(w, req)

		// Unsubscribe
		w = httptest.NewRecorder()
		req = newRequest("POST", "/api/issues/"+issueID+"/unsubscribe", nil)
		req = withURLParam(req, "id", issueID)
		testHandler.UnsubscribeFromIssue(w, req)

		// List should be empty
		w = httptest.NewRecorder()
		req = newRequest("GET", "/api/issues/"+issueID+"/subscribers", nil)
		req = withURLParam(req, "id", issueID)
		testHandler.ListIssueSubscribers(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("ListIssueSubscribers: expected 200, got %d: %s", w.Code, w.Body.String())
		}

		var subscribers []SubscriberResponse
		if err := json.NewDecoder(w.Body).Decode(&subscribers); err != nil {
			t.Fatalf("decode subscribers response: %v", err)
		}
		if len(subscribers) != 0 {
			t.Fatalf("ListIssueSubscribers: expected 0 subscribers after unsubscribe, got %d", len(subscribers))
		}
	})
}
