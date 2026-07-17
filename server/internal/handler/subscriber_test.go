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

func newSubscriberTestIssue(t *testing.T) string {
	t.Helper()
	issueID := createTestIssue(t, "Subscriber test issue", "medium")
	t.Cleanup(func() { deleteTestIssue(t, issueID) })
	return issueID
}

func changeIssueSubscription(t *testing.T, issueID string, subscribe bool, body any, prepare func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	action := "unsubscribe"
	handler := testHandler.UnsubscribeFromIssue
	if subscribe {
		action = "subscribe"
		handler = testHandler.SubscribeToIssue
	}
	req := withURLParam(newRequest(http.MethodPost, "/api/issues/"+issueID+"/"+action, body), "id", issueID)
	if prepare != nil {
		prepare(req)
	}
	w := httptest.NewRecorder()
	handler(w, req)
	return w
}

func listIssueSubscribersForTest(t *testing.T, issueID string) []subscriberResponse {
	t.Helper()
	req := withURLParam(newRequest(http.MethodGet, "/api/issues/"+issueID+"/subscribers", nil), "id", issueID)
	w := httptest.NewRecorder()
	testHandler.ListIssueSubscribers(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("ListIssueSubscribers: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var subscribers []subscriberResponse
	if err := json.NewDecoder(w.Body).Decode(&subscribers); err != nil {
		t.Fatalf("decode subscribers response: %v", err)
	}
	return subscribers
}

func TestSubscriberAPI(t *testing.T) {
	ctx := context.Background()

	t.Run("Subscribe", func(t *testing.T) {
		issueID := newSubscriberTestIssue(t)
		w := changeIssueSubscription(t, issueID, true, nil, nil)
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
		issueID := newSubscriberTestIssue(t)
		w := changeIssueSubscription(t, issueID, true, nil, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("SubscribeToIssue (1st): expected 200, got %d: %s", w.Code, w.Body.String())
		}

		w = changeIssueSubscription(t, issueID, true, nil, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("SubscribeToIssue (2nd): expected 200, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("RejectMalformedRequestBodies", func(t *testing.T) {
		issueID := newSubscriberTestIssue(t)

		for _, action := range []struct {
			name      string
			subscribe bool
		}{
			{name: "subscribe", subscribe: true},
			{name: "unsubscribe"},
		} {
			t.Run(action.name, func(t *testing.T) {
				w := changeIssueSubscription(t, issueID, action.subscribe, json.RawMessage(`{"user_id":`), nil)
				if w.Code != http.StatusBadRequest {
					t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
				}
			})
		}
	})

	t.Run("ListSubscribers", func(t *testing.T) {
		issueID := newSubscriberTestIssue(t)
		w := changeIssueSubscription(t, issueID, true, nil, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("SubscribeToIssue: expected 200, got %d: %s", w.Code, w.Body.String())
		}

		subscribers := listIssueSubscribersForTest(t, issueID)
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
		issueID := newSubscriberTestIssue(t)
		w := changeIssueSubscription(t, issueID, true, nil, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("SubscribeToIssue: expected 200, got %d: %s", w.Code, w.Body.String())
		}

		w = changeIssueSubscription(t, issueID, false, nil, nil)
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
		issueID := newSubscriberTestIssue(t)

		foreignUserID := "00000000-0000-0000-0000-000000000099"
		w := changeIssueSubscription(t, issueID, true, map[string]any{
			"user_id":   foreignUserID,
			"user_type": "member",
		}, nil)
		if w.Code != http.StatusForbidden {
			t.Fatalf("SubscribeToIssue with cross-workspace user: expected 403, got %d: %s", w.Code, w.Body.String())
		}

		if issueSubscriberExists(t, ctx, issueID, "member", foreignUserID) {
			t.Fatal("cross-workspace user should NOT be subscribed in DB")
		}
	})

	t.Run("UnsubscribeCrossWorkspaceUser", func(t *testing.T) {
		issueID := newSubscriberTestIssue(t)

		foreignUserID := "00000000-0000-0000-0000-000000000099"
		w := changeIssueSubscription(t, issueID, false, map[string]any{
			"user_id":   foreignUserID,
			"user_type": "member",
		}, nil)
		if w.Code != http.StatusForbidden {
			t.Fatalf("UnsubscribeFromIssue with cross-workspace user: expected 403, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("AgentCallerSubscribesItself", func(t *testing.T) {
		issueID := newSubscriberTestIssue(t)

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
		prepare := func(req *http.Request) { setTaskTokenActor(req, agentID, agentTask) }
		w := changeIssueSubscription(t, issueID, true, nil, prepare)
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

		w = changeIssueSubscription(t, issueID, false, nil, prepare)
		if w.Code != http.StatusOK {
			t.Fatalf("UnsubscribeFromIssue (agent caller): expected 200, got %d: %s", w.Code, w.Body.String())
		}

		agentSubscribed = issueSubscriberExists(t, ctx, issueID, "agent", agentID)
		if agentSubscribed {
			t.Fatal("expected agent to be unsubscribed in DB when X-Agent-ID is set")
		}
	})

	t.Run("ListAfterUnsubscribe", func(t *testing.T) {
		issueID := newSubscriberTestIssue(t)
		changeIssueSubscription(t, issueID, true, nil, nil)
		changeIssueSubscription(t, issueID, false, nil, nil)
		subscribers := listIssueSubscribersForTest(t, issueID)
		if len(subscribers) != 0 {
			t.Fatalf("ListIssueSubscribers: expected 0 subscribers after unsubscribe, got %d", len(subscribers))
		}
	})
}
