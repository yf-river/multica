package daemon

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestWorkspaceCoAuthoredByEnabled(t *testing.T) {
	cases := []struct {
		name     string
		register bool
		settings string
		want     bool
	}{
		{"unknown workspace is disabled", false, "", false},
		{"registered workspace without settings is disabled", true, "", false},
		{"both settings enabled", true, `{"github_enabled":true,"co_authored_by_enabled":true}`, true},
		{"co-authored-by disabled", true, `{"github_enabled":true,"co_authored_by_enabled":false}`, false},
		{
			"master off forces hook off even when co_authored_by true",
			true,
			`{"github_enabled":false,"co_authored_by_enabled":true}`,
			false,
		},
		{
			"master on lets co_authored_by decide",
			true,
			`{"github_enabled":true,"co_authored_by_enabled":false}`,
			false,
		},
		{"malformed settings is disabled", true, `not json`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := &Daemon{workspaces: make(map[string]*workspaceState)}
			if tc.register {
				var raw json.RawMessage
				if tc.settings != "" {
					raw = json.RawMessage(tc.settings)
				}
				d.workspaces["ws"] = newWorkspaceState("ws", nil, nil, raw)
			}
			if got := d.workspaceCoAuthoredByEnabled("ws"); got != tc.want {
				t.Fatalf("workspaceCoAuthoredByEnabled(%q) = %v, want %v",
					tc.settings, got, tc.want)
			}
		})
	}
}

func TestSyncWorkspacesRefreshesSettingsOnExistingWorkspace(t *testing.T) {
	t.Parallel()

	const workspaceID = "ws-1"

	var settingsPayload atomic.Value
	settingsPayload.Store(json.RawMessage(`{"github_enabled":true,"co_authored_by_enabled":true}`))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/workspaces":
			_ = json.NewEncoder(w).Encode([]protocol.WorkspaceResponse{{ID: workspaceID, Name: "ws"}})
		case "/api/daemon/workspaces/" + workspaceID + "/repos":
			raw, _ := settingsPayload.Load().(json.RawMessage)
			_ = json.NewEncoder(w).Encode(protocol.DaemonWorkspaceReposResponse{
				WorkspaceID:  workspaceID,
				Repos:        []protocol.TaskRepository{},
				ReposVersion: "v1",
				Settings:     raw,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	d := &Daemon{
		client:       NewClient(srv.URL),
		logger:       slog.Default(),
		workspaces:   make(map[string]*workspaceState),
		runtimeIndex: make(map[string]Runtime),
		runtimeSet:   newRuntimeSetWatcher(),
	}
	d.workspaces[workspaceID] = newWorkspaceState(
		workspaceID,
		[]string{"rt-1"},
		nil,
		json.RawMessage(`{"github_enabled":true,"co_authored_by_enabled":true}`),
	)

	if !d.workspaceCoAuthoredByEnabled(workspaceID) {
		t.Fatalf("precondition: expected co-author hook to start enabled")
	}

	settingsPayload.Store(json.RawMessage(`{"github_enabled":false,"co_authored_by_enabled":true}`))

	if err := d.syncWorkspacesFromAPI(context.Background()); err != nil {
		t.Fatalf("syncWorkspacesFromAPI: %v", err)
	}

	if d.workspaceCoAuthoredByEnabled(workspaceID) {
		t.Fatalf("expected co-author hook disabled after toggle; daemon is still using stale cached settings")
	}

	settingsPayload.Store(json.RawMessage(`{"github_enabled":true,"co_authored_by_enabled":true}`))
	if err := d.syncWorkspacesFromAPI(context.Background()); err != nil {
		t.Fatalf("syncWorkspacesFromAPI (re-enable): %v", err)
	}
	if !d.workspaceCoAuthoredByEnabled(workspaceID) {
		t.Fatalf("expected co-author hook re-enabled after toggling back on")
	}
}
