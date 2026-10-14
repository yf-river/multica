package handler

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWorkspaceMcpCheckIdentity(t *testing.T) {
	rt := db.AgentRuntime{OwnerID: parseUUID("11111111-1111-1111-1111-111111111111"), DaemonID: pgtype.Text{String: "daemon-a", Valid: true}}
	for _, tc := range []struct {
		daemon, user string
		want         bool
	}{{"daemon-a", "", true}, {"daemon-b", "", false}, {"", "11111111-1111-1111-1111-111111111111", true}, {"", "22222222-2222-2222-2222-222222222222", false}, {"", "", false}} {
		if got := canReceiveMcpCheck(rt, tc.daemon, tc.user); got != tc.want {
			t.Fatalf("unexpected identity authorization: %v", got)
		}
	}
}
func TestWorkspaceMcpMetadataPreservesConfig(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	id := createWorkspaceMcpServerForTest(t, "metadata-check", workspaceMcpTestEntry)
	req := withURLParams(newRequest("PATCH", "/", json.RawMessage(`{"name":"metadata-renamed","description":"中文用途"}`)), "id", testWorkspaceID, "serverId", id)
	w := httptest.NewRecorder()
	testHandler.UpdateWorkspaceMcpMetadata(w, req)
	if w.Code != 200 {
		t.Fatalf("metadata status %d: %s", w.Code, w.Body.String())
	}
	s, err := testHandler.Queries.GetWorkspaceMcpServer(context.Background(), db.GetWorkspaceMcpServerParams{ID: parseUUID(id), WorkspaceID: parseUUID(testWorkspaceID)})
	if err != nil || s.Description != "中文用途" || s.ConfigVersion != 1 || !strings.Contains(string(s.Config), workspaceMcpTestSecret) {
		t.Fatal("metadata update changed connection")
	}
	if strings.Contains(w.Body.String(), workspaceMcpTestSecret) {
		t.Fatal("secret returned")
	}
	req = withURLParams(newRequest("PATCH", "/", json.RawMessage(`{"name":"bad","config":{}}`)), "id", testWorkspaceID, "serverId", id)
	w = httptest.NewRecorder()
	testHandler.UpdateWorkspaceMcpMetadata(w, req)
	if w.Code != 400 {
		t.Fatal("metadata accepted config")
	}
}
func TestWorkspaceMcpCheckLifecycle(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	id := createWorkspaceMcpServerForTest(t, "lifecycle-check", workspaceMcpTestEntry)
	t.Cleanup(func() { testPool.Exec(ctx, "DELETE FROM workspace_mcp_check WHERE server_id=$1", id) })
	_, err := testPool.Exec(ctx, `UPDATE agent_runtime SET metadata='{"capabilities":["workspace-mcp-check-v1"]}',last_seen_at=now() WHERE id=$1`, testRuntimeID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { testPool.Exec(ctx, "UPDATE agent_runtime SET metadata='{}' WHERE id=$1", testRuntimeID) })
	create := func() []db.ListWorkspaceMcpChecksRow {
		t.Helper()
		req := withURLParams(newRequest("POST", "/", json.RawMessage(`{"runtime_id":"`+testRuntimeID+`"}`)), "id", testWorkspaceID, "serverId", id)
		w := httptest.NewRecorder()
		testHandler.CreateWorkspaceMcpCheck(w, req)
		if w.Code != 200 {
			t.Fatalf("create check: %d %s", w.Code, w.Body.String())
		}
		var rows []db.ListWorkspaceMcpChecksRow
		if json.Unmarshal(w.Body.Bytes(), &rows) != nil || len(rows) != 1 {
			t.Fatal("missing result")
		}
		return rows
	}
	first := create()[0]
	if repeat := create()[0]; repeat.ID != first.ID {
		t.Fatal("duplicate request not merged")
	}
	claim := testHandler.pendingWorkspaceMcpCheck(ctx, db.AgentRuntime{ID: parseUUID(testRuntimeID), WorkspaceID: parseUUID(testWorkspaceID)})
	if claim == nil || claim.ID != uuidToString(first.ID) {
		t.Fatal("check not delivered")
	}
	_, err = testPool.Exec(ctx, "UPDATE workspace_mcp_check SET started_at=now()-interval '70 seconds' WHERE id=$1", first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = testHandler.Queries.ExpireWorkspaceMcpChecks(ctx, parseUUID(testWorkspaceID)); err != nil {
		t.Fatal(err)
	}
	n, err := testHandler.Queries.CompleteWorkspaceMcpCheck(ctx, db.CompleteWorkspaceMcpCheckParams{ID: first.ID, RuntimeID: parseUUID(testRuntimeID), WorkspaceID: parseUUID(testWorkspaceID), Status: "completed", ToolCount: 5})
	if err != nil || n != 0 {
		t.Fatal("late result overwrote timeout")
	}
	second := create()[0]
	if second.ID == first.ID {
		t.Fatal("retry did not create new request")
	}
	_, err = testPool.Exec(ctx, "UPDATE workspace_mcp_server SET config_version=config_version+1 WHERE id=$1", id)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := testHandler.Queries.ListWorkspaceMcpChecks(ctx, db.ListWorkspaceMcpChecksParams{WorkspaceID: parseUUID(testWorkspaceID), ServerID: parseUUID(id)})
	if err != nil || !rows[0].Stale {
		t.Fatal("changed configuration not stale")
	}
	if testHandler.pendingWorkspaceMcpCheck(ctx, db.AgentRuntime{ID: parseUUID(testRuntimeID), WorkspaceID: parseUUID(testWorkspaceID)}) != nil {
		t.Fatal("obsolete check delivered")
	}
}
