package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGetSquadClientCanceledReturns499(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := newRequest(http.MethodGet, "/api/workspaces/"+testWorkspaceID+"/squads/11111111-1111-4111-8111-111111111111", nil).WithContext(ctx)
	req = withURLParam(req, "workspaceId", testWorkspaceID)
	req = withURLParam(req, "id", "11111111-1111-4111-8111-111111111111")
	w := httptest.NewRecorder()

	testHandler.GetSquad(w, req)

	if w.Code != 499 {
		t.Fatalf("GetSquad canceled request: expected 499, got %d: %s", w.Code, w.Body.String())
	}
}
