package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGetWorkspaceClientCanceledReturns499(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := newRequest(http.MethodGet, "/api/workspaces/"+testWorkspaceID, nil).WithContext(ctx)
	req = withURLParam(req, "id", testWorkspaceID)
	w := httptest.NewRecorder()

	testHandler.GetWorkspace(w, req)

	if w.Code != 499 {
		t.Fatalf("GetWorkspace canceled request: expected 499, got %d: %s", w.Code, w.Body.String())
	}
}
