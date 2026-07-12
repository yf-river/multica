package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestGetAutopilotPreservesDatabaseFailures(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	t.Run("entity lookup cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		autopilotID := "11111111-1111-4111-8111-111111111111"
		req := newRequest(http.MethodGet, "/api/autopilots/"+autopilotID, nil).WithContext(ctx)
		req = withURLParam(req, "id", autopilotID)
		w := httptest.NewRecorder()

		testHandler.GetAutopilot(w, req)

		if w.Code != 499 {
			t.Fatalf("canceled autopilot lookup: expected 499, got %d: %s", w.Code, w.Body.String())
		}
	})

	agentID := createWebhookTestAgent(t, "autopilot-read-failure-"+randomID()[:8])
	autopilotID := createWebhookTestAutopilot(t, agentID, "active", "run_only")
	for _, queryName := range []string{"ListAutopilotSubscribers", "ListAutopilotTriggers"} {
		t.Run(queryName, func(t *testing.T) {
			h := *testHandler
			h.Queries = db.New(failNamedQueryDB{DBTX: testPool, queryName: queryName})
			req := newRequest(http.MethodGet, "/api/autopilots/"+autopilotID, nil)
			req = withURLParam(req, "id", autopilotID)
			w := httptest.NewRecorder()

			h.GetAutopilot(w, req)

			if w.Code != http.StatusInternalServerError {
				t.Fatalf("%s failure: expected 500, got %d: %s", queryName, w.Code, w.Body.String())
			}
		})
	}
}
