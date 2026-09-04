package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCollectionReadsTreatCancelledRequestAsClientClosed(t *testing.T) {
	tests := []struct {
		name string
		path string
		call func(http.ResponseWriter, *http.Request)
	}{
		{
			name: "workspaces",
			path: "/api/workspaces",
			call: testHandler.ListWorkspaces,
		},
		{
			name: "issues",
			path: "/api/issues?workspace_id=" + testWorkspaceID,
			call: testHandler.ListIssues,
		},
		{
			name: "grouped issues",
			path: "/api/issues/grouped?workspace_id=" + testWorkspaceID,
			call: testHandler.ListGroupedIssues,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := newRequest(http.MethodGet, tt.path, nil)
			ctx, cancel := context.WithCancel(req.Context())
			cancel()
			req = req.WithContext(ctx)
			w := httptest.NewRecorder()

			tt.call(w, req)

			if w.Code != 499 {
				t.Fatalf("expected 499, got %d: %s", w.Code, w.Body.String())
			}
		})
	}
}
