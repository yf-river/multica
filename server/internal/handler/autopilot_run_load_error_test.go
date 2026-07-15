package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestGetAutopilotRunPreservesReadFailure(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	autopilotID := createWebhookTestAutopilot(t, "active")
	var runID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO autopilot_run (autopilot_id, source, status)
		VALUES ($1, 'manual', 'running')
		RETURNING id
	`, autopilotID).Scan(&runID); err != nil {
		t.Fatalf("create autopilot run fixture: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM autopilot_run WHERE id = $1`, runID)
	})

	h := *testHandler
	h.Queries = db.New(failNamedQueryDB{DBTX: testPool, queryName: "GetAutopilotRun"})
	req := newRequest(http.MethodGet, "/api/autopilots/"+autopilotID+"/runs/"+runID, nil)
	req = withURLParams(req, "id", autopilotID, "runId", runID)
	w := httptest.NewRecorder()

	h.GetAutopilotRun(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("autopilot run query failure: expected 500, got %d: %s", w.Code, w.Body.String())
	}
}
