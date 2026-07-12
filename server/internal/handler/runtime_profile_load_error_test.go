package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestRuntimeProfileMissingContractsRemainDistinct(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	missingID := "11111111-1111-4111-8111-111111111111"
	detailReq := newRequest(http.MethodGet, "/api/workspaces/"+testWorkspaceID+"/runtime-profiles/"+missingID, nil)
	detailReq = withURLParams(detailReq, "id", testWorkspaceID, "profileId", missingID)
	detailW := httptest.NewRecorder()
	testHandler.GetRuntimeProfile(detailW, detailReq)
	if detailW.Code != http.StatusNotFound || !strings.Contains(detailW.Body.String(), "runtime profile not found") {
		t.Fatalf("missing profile detail contract: got %d: %s", detailW.Code, detailW.Body.String())
	}

	registerReq := newDaemonUserRequest(http.MethodPost, "/api/daemon/register", map[string]any{
		"workspace_id": testWorkspaceID,
		"daemon_id":    "missing-runtime-profile",
		"runtimes": []map[string]any{{
			"name":       "missing runtime profile",
			"type":       "codex",
			"profile_id": missingID,
		}},
	}, testWorkspaceID, "missing-runtime-profile")
	registerW := httptest.NewRecorder()
	testHandler.DaemonRegister(registerW, registerReq)
	if registerW.Code != http.StatusBadRequest || !strings.Contains(registerW.Body.String(), "unknown runtime profile: "+missingID) {
		t.Fatalf("missing daemon profile contract: got %d: %s", registerW.Code, registerW.Body.String())
	}
}
