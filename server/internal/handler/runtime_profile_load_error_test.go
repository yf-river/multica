package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestRuntimeProfileReadsPreserveDatabaseFailures(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	profileID := insertRuntimeProfileFixture(
		t,
		context.Background(),
		"Runtime Profile Read Failure",
		"codex",
		"runtime-profile-read-failure",
	)
	h := *testHandler
	h.Queries = db.New(failNamedQueryDB{DBTX: testPool, queryName: "GetRuntimeProfileForWorkspace"})

	t.Run("profile detail", func(t *testing.T) {
		req := newRequest(http.MethodGet, "/api/workspaces/"+testWorkspaceID+"/runtime-profiles/"+profileID, nil)
		req = withURLParams(req, "id", testWorkspaceID, "profileId", profileID)
		w := httptest.NewRecorder()

		h.GetRuntimeProfile(w, req)

		if w.Code != http.StatusInternalServerError {
			t.Fatalf("runtime profile query failure: expected 500, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("daemon registration", func(t *testing.T) {
		req := newDaemonUserRequest(http.MethodPost, "/api/daemon/register", map[string]any{
			"workspace_id": testWorkspaceID,
			"daemon_id":    "runtime-profile-read-failure",
			"runtimes": []map[string]any{{
				"name":       "runtime profile read failure",
				"type":       "codex",
				"profile_id": profileID,
			}},
		}, testWorkspaceID, "runtime-profile-read-failure")
		w := httptest.NewRecorder()

		h.DaemonRegister(w, req)

		if w.Code != http.StatusInternalServerError {
			t.Fatalf("daemon profile query failure: expected 500, got %d: %s", w.Code, w.Body.String())
		}
	})
}
