package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestCreateRuntimeProfile_ReplaysCommittedResponse(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	key := uuid.NewString()
	create := func() (int, map[string]any) {
		w := httptest.NewRecorder()
		req := newRequest("POST", "/api/workspaces/"+testWorkspaceID+"/runtime-profiles", map[string]any{
			"display_name":    "Idempotent Runtime " + key,
			"protocol_family": "codex",
			"command_name":    "idempotent-codex",
		})
		req = withURLParam(req, "id", testWorkspaceID)
		req.Header.Set("Idempotency-Key", key)
		testHandler.CreateRuntimeProfile(w, req)
		var body map[string]any
		if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
			t.Fatalf("decode create response: %v", err)
		}
		return w.Code, body
	}

	firstStatus, first := create()
	t.Cleanup(func() {
		mustExec(t, context.Background(), `DELETE FROM runtime_profile WHERE id = $1`, first["id"])
		mustExec(t, context.Background(), `DELETE FROM resource_create_request WHERE resource_type = 'runtime_profile' AND idempotency_key = $1`, key)
	})
	secondStatus, second := create()
	if firstStatus != http.StatusCreated || secondStatus != http.StatusCreated {
		t.Fatalf("create statuses = (%d, %d), want (201, 201); second=%v", firstStatus, secondStatus, second)
	}
	if first["id"] != second["id"] {
		t.Fatalf("replay IDs differ: first=%v second=%v", first["id"], second["id"])
	}

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/workspaces/"+testWorkspaceID+"/runtime-profiles", map[string]any{
		"display_name":    "Different Runtime " + key,
		"protocol_family": "codex",
		"command_name":    "different-codex",
	})
	req = withURLParam(req, "id", testWorkspaceID)
	req.Header.Set("Idempotency-Key", key)
	testHandler.CreateRuntimeProfile(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("different replay: expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func insertRuntimeProfileFixture(t *testing.T, ctx context.Context, displayName, commandName string) string {
	t.Helper()
	var profileID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO runtime_profile (workspace_id, display_name, protocol_family, command_name, created_by)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id
	`, testWorkspaceID, displayName, "codex", commandName, testUserID).Scan(&profileID); err != nil {
		t.Fatalf("insert runtime_profile fixture: %v", err)
	}
	t.Cleanup(func() {
		mustExec(t, context.Background(), `DELETE FROM runtime_profile WHERE id = $1`, profileID)
	})
	return profileID
}

func insertProfileRuntimeFixture(t *testing.T, ctx context.Context, profileID, name, provider string) string {
	t.Helper()
	var runtimeID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider, status,
			device_info, metadata, owner_id, profile_id, last_seen_at
		)
		VALUES ($1, NULL, $2, 'local', $3, 'online', $4, '{}'::jsonb, $5, $6, now())
		RETURNING id
	`, testWorkspaceID, name, provider, name+" device", testUserID, profileID).Scan(&runtimeID); err != nil {
		t.Fatalf("insert profile runtime fixture: %v", err)
	}
	t.Cleanup(func() {
		mustExec(t, context.Background(), `DELETE FROM agent WHERE runtime_id = $1`, runtimeID)
		mustExec(t, context.Background(), `DELETE FROM agent_runtime WHERE id = $1`, runtimeID)
	})
	return runtimeID
}

func TestRuntimeProfileToResponseRejectsCorruptFixedArgs(t *testing.T) {
	for _, raw := range [][]byte{nil, []byte(`null`), []byte(`{}`), []byte(`[1]`)} {
		if _, err := runtimeProfileToResponse(db.RuntimeProfile{FixedArgs: raw}); err == nil {
			t.Fatalf("fixed_args=%s expected an error", raw)
		}
	}
}

func TestDeleteRuntimeProfile_ArchivedAgentCascade(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	profileID := insertRuntimeProfileFixture(t, ctx, "Cascade Profile Archived", "company-codex-arch")
	runtimeID := insertProfileRuntimeFixture(t, ctx, profileID, "Cascade Profile Runtime", "codex")
	agentID := createHandlerTestPersonalCloudAgent(t, ctx, runtimeID, "Cascade Profile Archived Agent")

	if _, err := testPool.Exec(ctx, `UPDATE agent SET archived_at = now() WHERE id = $1`, agentID); err != nil {
		t.Fatalf("archive agent: %v", err)
	}

	w := httptest.NewRecorder()
	req := newRequest("DELETE", "/api/workspaces/"+testWorkspaceID+"/runtime-profiles/"+profileID, nil)
	req = withURLParams(req, "id", testWorkspaceID, "profileId", profileID)
	testHandler.DeleteRuntimeProfile(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", w.Code, w.Body.String())
	}

	var profileRows, rtRows, agentRows int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM runtime_profile WHERE id = $1`, profileID).Scan(&profileRows); err != nil {
		t.Fatalf("count profile rows: %v", err)
	}
	if profileRows != 0 {
		t.Fatalf("expected profile deleted, found %d", profileRows)
	}
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM agent_runtime WHERE id = $1`, runtimeID).Scan(&rtRows); err != nil {
		t.Fatalf("count runtime rows: %v", err)
	}
	if rtRows != 0 {
		t.Fatalf("expected runtime row deleted by cascade, found %d", rtRows)
	}
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM agent WHERE id = $1`, agentID).Scan(&agentRows); err != nil {
		t.Fatalf("count agent rows: %v", err)
	}
	if agentRows != 0 {
		t.Fatalf("expected archived agent hard-deleted by cascade, found %d", agentRows)
	}
}

func TestDeleteRuntimeProfile_ActiveAgentBlocks(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	profileID := insertRuntimeProfileFixture(t, ctx, "Cascade Profile Active", "company-codex-active")
	runtimeID := insertProfileRuntimeFixture(t, ctx, profileID, "Cascade Profile Active Runtime", "codex")
	_ = createHandlerTestPersonalCloudAgent(t, ctx, runtimeID, "Cascade Profile Active Agent")

	w := httptest.NewRecorder()
	req := newRequest("DELETE", "/api/workspaces/"+testWorkspaceID+"/runtime-profiles/"+profileID, nil)
	req = withURLParams(req, "id", testWorkspaceID, "profileId", profileID)
	testHandler.DeleteRuntimeProfile(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}

	var profileRows, rtRows int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM runtime_profile WHERE id = $1`, profileID).Scan(&profileRows); err != nil {
		t.Fatalf("count profile rows: %v", err)
	}
	if profileRows != 1 {
		t.Fatalf("expected profile to survive 409, found %d", profileRows)
	}
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM agent_runtime WHERE id = $1`, runtimeID).Scan(&rtRows); err != nil {
		t.Fatalf("count runtime rows: %v", err)
	}
	if rtRows != 1 {
		t.Fatalf("expected runtime to survive 409, found %d", rtRows)
	}
}

func TestCreateRuntimeProfileRejectsUnknownFields(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/workspaces/"+testWorkspaceID+"/runtime-profiles", map[string]any{
		"display_name":      "Unknown Field",
		"protocol_family":   "codex",
		"command_name":      "unknown-field-codex",
		"unsupported_field": true,
	})
	req = withURLParam(req, "id", testWorkspaceID)
	testHandler.CreateRuntimeProfile(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestUpdateRuntimeProfileRejectsUnknownFields(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	profileID := insertRuntimeProfileFixture(
		t,
		context.Background(),
		"Unknown Update Field",
		"unknown-update-field",
	)

	w := httptest.NewRecorder()
	req := newRequest("PATCH", "/api/workspaces/"+testWorkspaceID+"/runtime-profiles/"+profileID, map[string]any{
		"unsupported_field": true,
	})
	req = withURLParams(req, "id", testWorkspaceID, "profileId", profileID)
	testHandler.UpdateRuntimeProfile(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}
