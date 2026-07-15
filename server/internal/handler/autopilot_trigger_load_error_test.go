package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestUpdateAutopilotTriggerPreservesReadFailure(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	autopilotID := createWebhookTestAutopilot(t, "active")
	trigger := createWebhookTrigger(t, autopilotID)

	h := *testHandler
	h.Queries = db.New(failNamedQueryDB{DBTX: testPool, queryName: "GetAutopilotTrigger"})
	req := newRequest(http.MethodPatch, "/api/autopilots/"+autopilotID+"/triggers/"+trigger.ID, map[string]any{
		"enabled": true,
	})
	req = withURLParams(req, "id", autopilotID, "triggerId", trigger.ID)
	w := httptest.NewRecorder()

	h.UpdateAutopilotTrigger(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("autopilot trigger query failure: expected 500, got %d: %s", w.Code, w.Body.String())
	}
}
