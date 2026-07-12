package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestReplayAutopilotDeliveryPreservesTriggerReadFailures(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	agentID := createWebhookTestAgent(t, "Replay Trigger Read Failure")
	autopilotID := createWebhookTestAutopilot(t, agentID, "active", "run_only")
	trigger := createWebhookTriggerViaHandler(t, autopilotID)
	original := postWebhook(t, *trigger.WebhookToken, map[string]any{"event": "trigger.read.failure"}, nil)
	if original.Code != http.StatusOK {
		t.Fatalf("create original delivery: %d: %s", original.Code, original.Body.String())
	}
	var response struct {
		DeliveryID string `json:"delivery_id"`
	}
	if err := json.Unmarshal(original.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode original delivery: %v", err)
	}

	h := *testHandler
	h.Queries = db.New(failNamedQueryDB{DBTX: testPool, queryName: "GetAutopilotTrigger"})
	w := httptest.NewRecorder()
	req := newRequest(http.MethodPost, fmt.Sprintf("/api/autopilots/%s/deliveries/%s/replay", autopilotID, response.DeliveryID), nil)
	req = withURLParams(req, "id", autopilotID, "deliveryId", response.DeliveryID)

	h.ReplayAutopilotDelivery(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("trigger lookup failure: expected 500, got %d: %s", w.Code, w.Body.String())
	}
}
