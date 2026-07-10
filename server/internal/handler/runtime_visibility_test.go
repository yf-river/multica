package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// TestCanUseRuntimeForAgent_Pure exercises the membership and visibility gate
// shared by CreateAgent and UpdateAgent. Scope compatibility is a separate
// invariant: a personal agent and its personal runtime must have the same
// owner even when a workspace owner or admin can administer that runtime.
func TestCanUseRuntimeForAgent_Pure(t *testing.T) {
	ownerUserID := "11111111-1111-1111-1111-111111111111"
	otherUserID := "22222222-2222-2222-2222-222222222222"

	personalRT := db.AgentRuntime{
		OwnerID: util.MustParseUUID(ownerUserID),
		Scope:   "personal",
	}
	workspaceRT := db.AgentRuntime{
		OwnerID: util.MustParseUUID(ownerUserID),
		Scope:   "workspace",
	}

	cases := []struct {
		name   string
		userID string
		role   string
		rt     db.AgentRuntime
		want   bool
	}{
		// workspace owner / admin override
		{"workspace owner on personal runtime owned by another", otherUserID, "owner", personalRT, true},
		{"workspace admin on personal runtime owned by another", otherUserID, "admin", personalRT, true},
		// runtime owner
		{"runtime owner on own personal runtime", ownerUserID, "member", personalRT, true},
		{"runtime owner on own workspace runtime", ownerUserID, "member", workspaceRT, true},
		// workspace runtime allows anyone in workspace
		{"plain member on someone else's workspace runtime", otherUserID, "member", workspaceRT, true},
		// the hole the issue closes
		{"plain member on someone else's personal runtime", otherUserID, "member", personalRT, false},
		{"plain member with empty role on personal runtime", otherUserID, "", personalRT, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			member := db.Member{
				UserID: util.MustParseUUID(tc.userID),
				Role:   tc.role,
			}
			got := canUseRuntimeForAgent(member, tc.rt)
			if got != tc.want {
				t.Fatalf("canUseRuntimeForAgent(role=%s, scope=%s, owner=%s, caller=%s) = %v; want %v",
					tc.role, tc.rt.Scope, ownerUserID, tc.userID, got, tc.want)
			}
		})
	}
}

// runtimeVisibilityFixture builds the three-actor world the gate needs to
// exercise: a personal runtime owned by a non-admin member, a separate plain
// member in the same workspace, and the workspace owner (testUserID). The
// runtime is registered through agent_runtime directly so the test doesn't
// depend on the daemon-registration code path. Returns runtime id, runtime
// owner user id, and the plain member's user id.
func runtimeVisibilityFixture(t *testing.T) (runtimeID, runtimeOwnerID, plainMemberID string) {
	t.Helper()
	ctx := context.Background()

	if err := testPool.QueryRow(ctx, `
		INSERT INTO "user" (name, account)
		VALUES ('Runtime Owner', 'runtime-owner@multica.test')
		RETURNING id
	`).Scan(&runtimeOwnerID); err != nil {
		t.Fatalf("create runtime owner user: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(),
			`DELETE FROM "user" WHERE account = 'runtime-owner@multica.test'`)
	})

	if _, err := testPool.Exec(ctx, `
		INSERT INTO member (workspace_id, user_id, role)
		VALUES ($1, $2, 'member')
	`, testWorkspaceID, runtimeOwnerID); err != nil {
		t.Fatalf("add runtime owner as member: %v", err)
	}

	if err := testPool.QueryRow(ctx, `
		INSERT INTO "user" (name, account)
		VALUES ('Plain Runtime Member', 'plain-runtime-member@multica.test')
		RETURNING id
	`).Scan(&plainMemberID); err != nil {
		t.Fatalf("create plain member user: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(),
			`DELETE FROM "user" WHERE account = 'plain-runtime-member@multica.test'`)
	})

	if _, err := testPool.Exec(ctx, `
		INSERT INTO member (workspace_id, user_id, role)
		VALUES ($1, $2, 'member')
	`, testWorkspaceID, plainMemberID); err != nil {
		t.Fatalf("add plain member: %v", err)
	}

	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider, status,
			device_info, metadata, owner_id, scope, last_seen_at
		)
		VALUES ($1, NULL, 'Visibility Test Runtime', 'cloud', 'visibility_test_provider', 'online', 'scope test', '{}'::jsonb, $2, 'personal', now())
		RETURNING id
	`, testWorkspaceID, runtimeOwnerID).Scan(&runtimeID); err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(),
			`DELETE FROM agent_runtime WHERE id = $1`, runtimeID)
	})

	return runtimeID, runtimeOwnerID, plainMemberID
}

// TestCreateAgent_RequiresPersonalRuntimeOwnerMatch walks both gates
// end-to-end. A workspace owner can administer another member's personal
// runtime, but cannot create a personal agent owned by themselves on it. The
// runtime owner can create the matching personal agent; a plain third party
// is rejected before the scope compatibility check.
func TestCreateAgent_RequiresPersonalRuntimeOwnerMatch(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	runtimeID, runtimeOwnerID, plainMemberID := runtimeVisibilityFixture(t)

	t.Cleanup(func() {
		testPool.Exec(context.Background(),
			`DELETE FROM agent WHERE workspace_id = $1 AND name LIKE 'runtime-scope-test-%'`,
			testWorkspaceID)
	})

	body := func(name string) map[string]any {
		return map[string]any{
			"name":                 name,
			"description":          "",
			"runtime_id":           runtimeID,
			"scope":                "personal",
			"max_concurrent_tasks": 1,
		}
	}

	// Workspace owner: runtime access is allowed, but the requested personal
	// agent would have a different owner from the personal runtime.
	w := httptest.NewRecorder()
	testHandler.CreateAgent(w, newRequest(http.MethodPost, "/api/agents", body("runtime-scope-test-admin")))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("CreateAgent as workspace owner: expected 400, got %d: %s", w.Code, w.Body.String())
	}

	// Runtime owner: allowed because they own the runtime.
	w = httptest.NewRecorder()
	testHandler.CreateAgent(w, newRequestAs(runtimeOwnerID, http.MethodPost, "/api/agents", body("runtime-scope-test-runtime-owner")))
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateAgent as runtime owner: expected 201, got %d: %s", w.Code, w.Body.String())
	}

	// Plain member: this is the hole MUL-2062 closes — must be 403.
	w = httptest.NewRecorder()
	testHandler.CreateAgent(w, newRequestAs(plainMemberID, http.MethodPost, "/api/agents", body("runtime-scope-test-plain-member")))
	if w.Code != http.StatusForbidden {
		t.Fatalf("CreateAgent as plain member on personal runtime: expected 403, got %d: %s", w.Code, w.Body.String())
	}
}

// TestCreateAgent_AllowsWorkspaceRuntimeForPlainMember verifies the "workspace"
// half of the scope predicate: once the runtime owner flips it to
// workspace, any workspace member can create workspace agents on it.
func TestCreateAgent_AllowsWorkspaceRuntimeForPlainMember(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	runtimeID, _, plainMemberID := runtimeVisibilityFixture(t)
	ctx := context.Background()
	if _, err := testPool.Exec(ctx,
		`UPDATE agent_runtime SET scope = 'workspace' WHERE id = $1`, runtimeID,
	); err != nil {
		t.Fatalf("flip runtime to workspace: %v", err)
	}

	t.Cleanup(func() {
		testPool.Exec(context.Background(),
			`DELETE FROM agent WHERE workspace_id = $1 AND name = 'runtime-scope-test-workspace-runtime'`,
			testWorkspaceID)
	})

	body := map[string]any{
		"name":                 "runtime-scope-test-workspace-runtime",
		"description":          "",
		"runtime_id":           runtimeID,
		"scope":                "workspace",
		"max_concurrent_tasks": 1,
	}
	w := httptest.NewRecorder()
	testHandler.CreateAgent(w, newRequestAs(plainMemberID, http.MethodPost, "/api/agents", body))
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateAgent as plain member on workspace runtime: expected 201, got %d: %s", w.Code, w.Body.String())
	}
}

// TestUpdateAgent_RejectsRebindToPersonalRuntime is the regression for the
// "update can bypass create" backdoor — without this gate a plain member
// could create an agent on a workspace runtime, then re-bind it onto someone
// else's personal runtime via UpdateAgent.
func TestUpdateAgent_RejectsRebindToPersonalRuntime(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	personalRuntimeID, _, plainMemberID := runtimeVisibilityFixture(t)

	ctx := context.Background()
	// Create a workspace runtime that the plain member can legitimately own
	// an agent on, then we try to move the agent onto the personal runtime.
	var workspaceRuntimeID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider, status,
			device_info, metadata, owner_id, scope, last_seen_at
		)
		VALUES ($1, NULL, 'Workspace Runtime', 'cloud', 'visibility_test_workspace_provider', 'online', 'workspace', '{}'::jsonb, $2, 'workspace', now())
		RETURNING id
	`, testWorkspaceID, plainMemberID).Scan(&workspaceRuntimeID); err != nil {
		t.Fatalf("create workspace runtime: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent_runtime WHERE id = $1`, workspaceRuntimeID)
	})

	var agentID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent (
			workspace_id, name, description, runtime_mode, runtime_config,
			runtime_id, scope, max_concurrent_tasks, owner_id,
			instructions, custom_env, custom_args
		)
		VALUES ($1, 'rebind-test-agent', '', 'cloud', '{}'::jsonb,
		        $2, 'workspace', 1, $3, '', '{}'::jsonb, '[]'::jsonb)
		RETURNING id
	`, testWorkspaceID, workspaceRuntimeID, plainMemberID).Scan(&agentID); err != nil {
		t.Fatalf("create agent on workspace runtime: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent WHERE id = $1`, agentID)
	})

	body := map[string]any{
		"runtime_id": personalRuntimeID,
	}
	w := httptest.NewRecorder()
	req := newRequestAs(plainMemberID, http.MethodPut, "/api/agents/"+agentID, body)
	req = withURLParam(req, "id", agentID)
	testHandler.UpdateAgent(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("UpdateAgent rebinding to personal runtime: expected 403, got %d: %s", w.Code, w.Body.String())
	}
}

// TestUpdateAgentRuntime_ScopePatchApplies pins the invariant that
// a PATCH carrying `scope` correctly updates the runtime.
func TestUpdateAgentRuntime_ScopePatchApplies(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	runtimeID, runtimeOwnerID, _ := runtimeVisibilityFixture(t)

	w := httptest.NewRecorder()
	req := newRequestAs(runtimeOwnerID, http.MethodPatch, "/api/runtimes/"+runtimeID, map[string]any{
		"scope": "workspace",
	})
	req = withURLParam(req, "runtimeId", runtimeID)
	testHandler.UpdateAgentRuntime(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("PATCH scope: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp AgentRuntimeResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Scope != "workspace" {
		t.Fatalf("scope patch: got %q, want workspace", resp.Scope)
	}
}

// TestUpdateAgentRuntime_IgnoresTimezoneField guards the RFC migration that
// dropped `timezone` from UpdateAgentRuntimeRequest: a PATCH body still
// carrying `timezone` must not error, must not echo a `timezone` key back,
// and must still apply the recognised `scope` field. Timezone is now a
// user-level preference, not a per-runtime one.
func TestUpdateAgentRuntime_IgnoresTimezoneField(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	runtimeID, runtimeOwnerID, _ := runtimeVisibilityFixture(t)

	w := httptest.NewRecorder()
	req := newRequestAs(runtimeOwnerID, http.MethodPatch, "/api/runtimes/"+runtimeID, map[string]any{
		"timezone": "Asia/Tokyo",
		"scope":    "workspace",
	})
	req = withURLParam(req, "runtimeId", runtimeID)
	testHandler.UpdateAgentRuntime(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("PATCH with stray timezone: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// The response must carry no `timezone` key — runtimes have no such field.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if _, present := raw["timezone"]; present {
		t.Errorf("response unexpectedly contains a timezone key: %s", w.Body.String())
	}

	// `scope` was still applied.
	var resp AgentRuntimeResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Scope != "workspace" {
		t.Errorf("scope patch: got %q, want workspace", resp.Scope)
	}
}

// TestUpdateAgentRuntime_InvalidScopeReturns400 verifies that an invalid
// scope value is rejected with 400 before any mutation runs.
func TestUpdateAgentRuntime_InvalidScopeReturns400(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	runtimeID, runtimeOwnerID, _ := runtimeVisibilityFixture(t)

	w := httptest.NewRecorder()
	req := newRequestAs(runtimeOwnerID, http.MethodPatch, "/api/runtimes/"+runtimeID, map[string]any{
		"scope": "everyone",
	})
	req = withURLParam(req, "runtimeId", runtimeID)
	testHandler.UpdateAgentRuntime(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("PATCH with invalid scope: expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

// TestUpdateAgentRuntime_ScopeToggle covers the PATCH endpoint:
// runtime owner / workspace admin can flip personal↔workspace; plain members
// cannot; an unknown value is rejected with 400.
func TestUpdateAgentRuntime_ScopeToggle(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	runtimeID, runtimeOwnerID, plainMemberID := runtimeVisibilityFixture(t)

	patch := func(actorID string, scope string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := newRequestAs(actorID, http.MethodPatch, "/api/runtimes/"+runtimeID, map[string]any{
			"scope": scope,
		})
		req = withURLParam(req, "runtimeId", runtimeID)
		testHandler.UpdateAgentRuntime(w, req)
		return w
	}

	// Runtime owner flips personal → workspace.
	if w := patch(runtimeOwnerID, "workspace"); w.Code != http.StatusOK {
		t.Fatalf("UpdateAgentRuntime as runtime owner → workspace: expected 200, got %d: %s", w.Code, w.Body.String())
	} else {
		var resp AgentRuntimeResponse
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if resp.Scope != "workspace" {
			t.Fatalf("expected scope=workspace, got %q", resp.Scope)
		}
	}

	// Workspace owner (testUserID) flips it back.
	if w := patch(testUserID, "personal"); w.Code != http.StatusOK {
		t.Fatalf("UpdateAgentRuntime as workspace owner → personal: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Plain member: forbidden, regardless of intent.
	if w := patch(plainMemberID, "workspace"); w.Code != http.StatusForbidden {
		t.Fatalf("UpdateAgentRuntime as plain member: expected 403, got %d: %s", w.Code, w.Body.String())
	}

	// Bad value from the owner: 400.
	if w := patch(runtimeOwnerID, "everyone"); w.Code != http.StatusBadRequest {
		t.Fatalf("UpdateAgentRuntime with invalid scope: expected 400, got %d: %s", w.Code, w.Body.String())
	}
}
