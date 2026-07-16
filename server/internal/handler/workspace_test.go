package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/middleware"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestCreateWorkspaceRecoversTheExactCommittedResult(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	slug := "workspace-replay-" + uuid.NewString()[:8]
	key := uuid.NewString()
	body := map[string]any{"name": "Workspace Replay", "slug": slug, "description": "current request"}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM workspace WHERE slug = $1`, slug)
	})
	create := func() *httptest.ResponseRecorder {
		req := newRequest(http.MethodPost, "/api/workspaces", body)
		req.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		testHandler.CreateWorkspace(w, req)
		return w
	}
	first := create()
	replay := create()
	if first.Code != http.StatusCreated {
		t.Fatalf("first create = %d %s", first.Code, first.Body.String())
	}
	if replay.Code != http.StatusCreated || replay.Body.String() != first.Body.String() {
		t.Fatalf("workspace replay = %d %s, want exact %s", replay.Code, replay.Body.String(), first.Body.String())
	}
	responses := make(chan *httptest.ResponseRecorder, 8)
	var wait sync.WaitGroup
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			responses <- create()
		}()
	}
	wait.Wait()
	close(responses)
	for response := range responses {
		if response.Code != http.StatusCreated || response.Body.String() != first.Body.String() {
			t.Fatalf("concurrent replay = %d %s, want exact", response.Code, response.Body.String())
		}
	}
	changedReq := newRequest(http.MethodPost, "/api/workspaces", map[string]any{
		"name": "Changed Workspace", "slug": slug,
	})
	changedReq.Header.Set("Idempotency-Key", key)
	changed := httptest.NewRecorder()
	testHandler.CreateWorkspace(changed, changedReq)
	if changed.Code != http.StatusConflict {
		t.Fatalf("changed replay = %d %s, want 409", changed.Code, changed.Body.String())
	}
	var workspaces, owners int
	if err := testPool.QueryRow(context.Background(), `SELECT
		(SELECT count(*) FROM workspace WHERE slug = $1),
		(SELECT count(*) FROM member m JOIN workspace w ON w.id=m.workspace_id WHERE w.slug=$1 AND m.role='owner')
	`, slug).Scan(&workspaces, &owners); err != nil {
		t.Fatal(err)
	}
	if workspaces != 1 || owners != 1 {
		t.Fatalf("workspace writes = %d workspaces, %d owners; want 1/1", workspaces, owners)
	}
	if _, err := testPool.Exec(context.Background(), `DELETE FROM member WHERE workspace_id=(SELECT id FROM workspace WHERE slug=$1)`, slug); err != nil {
		t.Fatal(err)
	}
	denied := create()
	if denied.Code != http.StatusForbidden {
		t.Fatalf("replay after access removal = %d %s, want 403", denied.Code, denied.Body.String())
	}
}

func TestCreateWorkspaceCompletionFailureRollsBackWorkspaceOwnerAndRequest(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	key := uuid.NewString()
	slug := "workspace-rollback-" + uuid.NewString()[:8]
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM workspace WHERE slug = $1`, slug)
	})
	installResourceCreateCompletionFailure(t, resourceTypeWorkspace, key)
	req := newRequest(http.MethodPost, "/api/workspaces", map[string]any{"name": "Rollback Workspace", "slug": slug})
	req.Header.Set("Idempotency-Key", key)
	w := httptest.NewRecorder()
	testHandler.CreateWorkspace(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("completion failure = %d %s, want 500", w.Code, w.Body.String())
	}
	var workspaces, members, requests int
	if err := testPool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM workspace WHERE slug=$1),
		(SELECT count(*) FROM member WHERE workspace_id=$2),
		(SELECT count(*) FROM resource_create_request WHERE resource_type='workspace' AND idempotency_key=$2)
	`, slug, key).Scan(&workspaces, &members, &requests); err != nil {
		t.Fatal(err)
	}
	if workspaces != 0 || members != 0 || requests != 0 {
		t.Fatalf("failed create left writes: workspaces=%d members=%d requests=%d", workspaces, members, requests)
	}
}

func TestCreateMemberRecoversTheExactCommittedResult(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	key := uuid.NewString()
	account := "member-replay-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	body := map[string]any{
		"account": account, "name": "Replay Member", "password": "ReplayMember1!", "role": "member",
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM resource_create_request WHERE workspace_id=$1 AND actor_id=$2 AND resource_type='workspace_member' AND idempotency_key=$3`, testWorkspaceID, testUserID, key)
		_, _ = testPool.Exec(ctx, `DELETE FROM "user" WHERE account = $1`, account)
	})
	create := func(payload map[string]any) *httptest.ResponseRecorder {
		req := newRequest(http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/members", payload)
		req.Header.Set("X-Workspace-ID", testWorkspaceID)
		req.Header.Set("Idempotency-Key", key)
		req = withURLParam(req, "id", testWorkspaceID)
		w := httptest.NewRecorder()
		testHandler.CreateMember(w, req)
		return w
	}
	first := create(body)
	replay := create(body)
	if first.Code != http.StatusCreated {
		t.Fatalf("first create = %d %s", first.Code, first.Body.String())
	}
	if replay.Code != http.StatusCreated || replay.Body.String() != first.Body.String() {
		t.Fatalf("member replay = %d %s, want exact %s", replay.Code, replay.Body.String(), first.Body.String())
	}
	responses := make(chan *httptest.ResponseRecorder, 8)
	var wait sync.WaitGroup
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			responses <- create(body)
		}()
	}
	wait.Wait()
	close(responses)
	for response := range responses {
		if response.Code != http.StatusCreated || response.Body.String() != first.Body.String() {
			t.Fatalf("concurrent member replay = %d %s, want exact", response.Code, response.Body.String())
		}
	}
	changed := create(map[string]any{
		"account": account, "name": "Changed Member", "password": "ReplayMember1!", "role": "admin",
	})
	if changed.Code != http.StatusConflict {
		t.Fatalf("changed replay = %d %s, want 409", changed.Code, changed.Body.String())
	}
	var users, members int
	var requestHash string
	var responseBody []byte
	if err := testPool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM "user" WHERE account=$1),
		(SELECT count(*) FROM member m JOIN "user" u ON u.id=m.user_id WHERE u.account=$1 AND m.workspace_id=$2),
		r.request_hash, r.response_body
		FROM resource_create_request r
		WHERE r.workspace_id=$2 AND r.actor_id=$3 AND r.resource_type='workspace_member' AND r.idempotency_key=$4
	`, account, testWorkspaceID, testUserID, key).Scan(&users, &members, &requestHash, &responseBody); err != nil {
		t.Fatal(err)
	}
	if users != 1 || members != 1 {
		t.Fatalf("member writes = %d users, %d members; want 1/1", users, members)
	}
	if len(requestHash) != 64 || strings.Contains(string(responseBody), "ReplayMember1!") {
		t.Fatalf("member request persisted sensitive input: hash_len=%d response=%s", len(requestHash), responseBody)
	}
}

func TestCreateMemberFailureDoesNotLeaveLoginCapableOrphanUser(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	account := "member-rollback-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	functionName := "member_create_fail_fn_" + suffix
	triggerName := "member_create_fail_" + suffix
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON member; DROP FUNCTION IF EXISTS %s()`, triggerName, functionName))
		_, _ = testPool.Exec(ctx, `DELETE FROM "user" WHERE account = $1`, account)
	})
	if _, err := testPool.Exec(ctx, fmt.Sprintf(`
		CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF EXISTS (SELECT 1 FROM "user" WHERE id=NEW.user_id AND account='%s') THEN
				RAISE EXCEPTION 'forced member insert failure';
			END IF;
			RETURN NEW;
		END;
		$$;
		CREATE TRIGGER %s BEFORE INSERT ON member
		FOR EACH ROW EXECUTE FUNCTION %s();
	`, functionName, account, triggerName, functionName)); err != nil {
		t.Fatal(err)
	}

	req := newRequest(http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/members", map[string]any{
		"account": account, "name": "Rollback Member", "password": "RollbackMember1!", "role": "member",
	})
	req.Header.Set("X-Workspace-ID", testWorkspaceID)
	req.Header.Set("Idempotency-Key", uuid.NewString())
	req = withURLParam(req, "id", testWorkspaceID)
	w := httptest.NewRecorder()
	middleware.RequireWorkspaceRoleFromURL(testHandler.Queries, "id", "owner", "admin")(
		http.HandlerFunc(testHandler.CreateMember),
	).ServeHTTP(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("member failure = %d %s, want 500", w.Code, w.Body.String())
	}
	var users, loginCapable int
	if err := testPool.QueryRow(ctx, `SELECT
		count(*), count(*) FILTER (WHERE password_hash IS NOT NULL AND password_hash <> '')
		FROM "user" WHERE account=$1
	`, account).Scan(&users, &loginCapable); err != nil {
		t.Fatal(err)
	}
	if users != 0 || loginCapable != 0 {
		t.Fatalf("failed member create left users=%d login_capable=%d; want 0/0", users, loginCapable)
	}
}

func TestCreateMemberRejectsPlainWorkspaceMember(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	requesterAccount := "plain-requester-" + suffix
	targetAccount := "plain-created-admin-" + suffix
	requestKey := uuid.NewString()
	var requesterID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO "user" (name, account) VALUES ('Plain Requester', $1) RETURNING id
	`, requesterAccount).Scan(&requesterID); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `
		INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, 'member')
	`, testWorkspaceID, requesterID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM resource_create_request WHERE resource_type='workspace_member' AND idempotency_key=$1`, requestKey)
		_, _ = testPool.Exec(ctx, `DELETE FROM "user" WHERE account IN ($1, $2)`, requesterAccount, targetAccount)
	})

	req := newRequest(http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/members", map[string]any{
		"account": targetAccount, "name": "Unauthorized Admin", "password": "UnauthorizedAdmin1!", "role": "admin",
	})
	req = withTestWorkspaceMember(req, testWorkspaceID, requesterID)
	req.Header.Set("Idempotency-Key", requestKey)
	req = withURLParam(req, "id", testWorkspaceID)
	w := httptest.NewRecorder()
	middleware.RequireWorkspaceRoleFromURL(testHandler.Queries, "id", "owner", "admin")(
		http.HandlerFunc(testHandler.CreateMember),
	).ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("plain member create = %d %s, want 403", w.Code, w.Body.String())
	}
	var targetUsers int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM "user" WHERE account=$1`, targetAccount).Scan(&targetUsers); err != nil {
		t.Fatal(err)
	}
	if targetUsers != 0 {
		t.Fatalf("unauthorized member create wrote %d target users", targetUsers)
	}
}

func TestCreateWorkspace_RejectsReservedSlug(t *testing.T) {
	// Drive the test off the actual reservedSlugs map so the test can never
	// drift from the source of truth. New entries are covered automatically.
	reserved := make([]string, 0, len(reservedSlugs))
	for slug := range reservedSlugs {
		reserved = append(reserved, slug)
	}
	sort.Strings(reserved) // deterministic test order

	for _, slug := range reserved {
		t.Run(slug, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := newRequest("POST", "/api/workspaces", map[string]any{
				"name": fmt.Sprintf("Test %s", slug),
				"slug": slug,
			})
			testHandler.CreateWorkspace(w, req)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("slug %q: expected 400, got %d: %s", slug, w.Code, w.Body.String())
			}
		})
	}
}

func TestWorkspaceToResponse_RejectsNonArrayRepos(t *testing.T) {
	tests := []struct {
		name string
		raw  []byte
	}{
		{name: "nil", raw: nil},
		{name: "object", raw: []byte(`{}`)},
		{name: "invalid", raw: []byte(`not-json`)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			workspace := db.Workspace{Settings: []byte(`{"github_enabled":true,"github_pr_sidebar_enabled":true,"co_authored_by_enabled":true}`), Repos: tc.raw}
			if _, err := workspaceToResponse(workspace); err == nil {
				t.Fatalf("workspaceToResponse(%s) expected an error", tc.raw)
			}
		})
	}
}

func TestWorkspaceToResponse_AcceptsRepositoryArray(t *testing.T) {
	workspace := db.Workspace{
		Settings: []byte(`{"github_enabled":true,"github_pr_sidebar_enabled":true,"co_authored_by_enabled":true}`),
		Repos:    []byte(`[{"url":"https://git.example.com/repo.git"}]`),
	}
	response, err := workspaceToResponse(workspace)
	if err != nil {
		t.Fatalf("workspaceToResponse: %v", err)
	}
	if len(response.Repos) != 1 {
		t.Fatalf("workspaceToResponse repos = %#v, want one repository", response.Repos)
	}
}

func TestWorkspaceToResponse_RejectsNonObjectSettings(t *testing.T) {
	workspace := db.Workspace{Settings: []byte(`[]`), Repos: []byte(`[]`)}
	if _, err := workspaceToResponse(workspace); err == nil {
		t.Fatal("workspaceToResponse expected an error for array settings")
	}
}

func TestCanonicalizeWorkspaceSettingsEnforcesCurrentGitHubShape(t *testing.T) {
	settings := map[string]any{
		"github_enabled":            false,
		"github_pr_sidebar_enabled": true,
		"co_authored_by_enabled":    true,
		"custom":                    "kept",
	}
	canonical, err := canonicalizeWorkspaceSettings(settings)
	if err != nil {
		t.Fatalf("canonicalize workspace settings: %v", err)
	}
	if canonical["github_enabled"] != false || canonical["custom"] != "kept" {
		t.Fatalf("explicit settings changed: %#v", canonical)
	}

	canonical, err = canonicalizeWorkspaceSettings(map[string]any{"github_enabled": true})
	if err != nil {
		t.Fatalf("canonicalize partial workspace settings: %v", err)
	}
	for key := range workspaceBooleanSettingDefaults {
		if canonical[key] != true {
			t.Fatalf("%s = %#v, want true", key, canonical[key])
		}
	}

	invalid := make(map[string]any, len(settings))
	for key, value := range settings {
		invalid[key] = value
	}
	invalid["github_enabled"] = nil
	if _, err := canonicalizeWorkspaceSettings(invalid); err == nil {
		t.Fatal("expected a non-boolean current GitHub setting to be rejected")
	}
}

// TestCreateWorkspace_DisabledByConfig guards the self-host gate added by
// #3433: when DisableWorkspaceCreation is true on the handler config, every
// caller — even an already-authenticated user — must receive 403 and the
// workspace row must not be written.
func TestCreateWorkspace_DisabledByConfig(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	const slug = "handler-tests-disabled-create"
	ctx := context.Background()
	_, _ = testPool.Exec(ctx, `DELETE FROM workspace WHERE slug = $1`, slug)
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM workspace WHERE slug = $1`, slug)
	})

	prev := testHandler.cfg
	testHandler.cfg = Config{
		AllowSignup:              prev.AllowSignup,
		DisableWorkspaceCreation: true,
	}
	t.Cleanup(func() { testHandler.cfg = prev })

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/workspaces", map[string]any{
		"name": "Disabled Create",
		"slug": slug,
	})
	testHandler.CreateWorkspace(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("CreateWorkspace: expected 403 with flag on, got %d: %s", w.Code, w.Body.String())
	}

	var count int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM workspace WHERE slug = $1`, slug).Scan(&count); err != nil {
		t.Fatalf("count workspaces: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no workspace row to be written when gate fires, found %d", count)
	}
}

// TestDeleteWorkspace_RequiresOwner exercises the owner-only route boundary.
func TestDeleteWorkspace_RequiresOwner(t *testing.T) {
	ctx := context.Background()

	const slug = "handler-tests-delete-403"
	_, _ = testPool.Exec(ctx, `DELETE FROM workspace WHERE slug = $1`, slug)

	var wsID string
	if err := testPool.QueryRow(ctx, `
INSERT INTO workspace (name, slug, description)
VALUES ($1, $2, $3)
RETURNING id
`, "Handler Test Delete 403", slug, "DeleteWorkspace handler permission test").Scan(&wsID); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, wsID)
	})

	if _, err := testPool.Exec(ctx, `
INSERT INTO member (workspace_id, user_id, role)
VALUES ($1, $2, 'admin')
`, wsID, testUserID); err != nil {
		t.Fatalf("create admin member: %v", err)
	}

	w := httptest.NewRecorder()
	req := newRequest("DELETE", "/api/workspaces/"+wsID, nil)
	req = withURLParam(req, "id", wsID)
	middleware.RequireWorkspaceRoleFromURL(testHandler.Queries, "id", "owner")(
		http.HandlerFunc(testHandler.DeleteWorkspace),
	).ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 from DeleteWorkspace handler for admin (non-owner), got %d: %s", w.Code, w.Body.String())
	}

	var exists bool
	if err := testPool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM workspace WHERE id = $1)`, wsID).Scan(&exists); err != nil {
		t.Fatalf("verify workspace: %v", err)
	}
	if !exists {
		t.Fatal("workspace was deleted despite the owner-only route boundary")
	}
}

// TestDeleteWorkspace_OwnerSucceeds is the positive route-boundary counterpart.
func TestDeleteWorkspace_OwnerSucceeds(t *testing.T) {
	ctx := context.Background()

	const slug = "handler-tests-delete-ok"
	_, _ = testPool.Exec(ctx, `DELETE FROM workspace WHERE slug = $1`, slug)

	var wsID string
	if err := testPool.QueryRow(ctx, `
INSERT INTO workspace (name, slug, description)
VALUES ($1, $2, $3)
RETURNING id
`, "Handler Test Delete OK", slug, "DeleteWorkspace handler owner test").Scan(&wsID); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, wsID)
	})

	if _, err := testPool.Exec(ctx, `
INSERT INTO member (workspace_id, user_id, role)
VALUES ($1, $2, 'owner')
`, wsID, testUserID); err != nil {
		t.Fatalf("create owner member: %v", err)
	}
	if _, err := testPool.Exec(ctx, `
INSERT INTO github_pending_check_suite (
	workspace_id, installation_id, repo_owner, repo_name, pr_number,
	suite_id, head_sha, app_id, status, suite_updated_at
)
VALUES ($1, 123456789, 'multica-ai', 'multica', 3366, 987654321, 'abc123', 15368, 'completed', now())
`, wsID); err != nil {
		t.Fatalf("create pending check suite: %v", err)
	}

	w := httptest.NewRecorder()
	req := newRequest("DELETE", "/api/workspaces/"+wsID, nil)
	req = withURLParam(req, "id", wsID)
	middleware.RequireWorkspaceRoleFromURL(testHandler.Queries, "id", "owner")(
		http.HandlerFunc(testHandler.DeleteWorkspace),
	).ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204 from DeleteWorkspace handler for owner, got %d: %s", w.Code, w.Body.String())
	}

	var exists bool
	if err := testPool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM workspace WHERE id = $1)`, wsID).Scan(&exists); err != nil {
		t.Fatalf("verify workspace: %v", err)
	}
	if exists {
		t.Fatal("workspace still exists after owner DELETE")
	}

	var pendingCount int
	if err := testPool.QueryRow(ctx, `SELECT COUNT(*) FROM github_pending_check_suite WHERE workspace_id = $1`, wsID).Scan(&pendingCount); err != nil {
		t.Fatalf("verify pending check suites: %v", err)
	}
	if pendingCount != 0 {
		t.Fatalf("pending check suites were not cleaned up for deleted workspace: %d", pendingCount)
	}
}

// TestUpdateWorkspace_AvatarURL covers the avatar_url field added to
// UpdateWorkspaceRequest: a PATCH with avatar_url is persisted and surfaced
// back on the response, and partial updates leave other fields untouched.
// Route-level authorization (owner/admin) is enforced by middleware in
// router.go; the handler test calls UpdateWorkspace directly to verify the
// payload wiring.
func TestUpdateWorkspace_AvatarURL(t *testing.T) {
	ctx := context.Background()

	const slug = "handler-tests-avatar-url"
	_, _ = testPool.Exec(ctx, `DELETE FROM workspace WHERE slug = $1`, slug)

	var wsID string
	if err := testPool.QueryRow(ctx, `
INSERT INTO workspace (name, slug, description)
VALUES ($1, $2, $3)
RETURNING id
`, "Handler Test Avatar URL", slug, "UpdateWorkspace avatar_url test").Scan(&wsID); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, wsID)
	})

	if _, err := testPool.Exec(ctx, `
INSERT INTO member (workspace_id, user_id, role)
VALUES ($1, $2, 'owner')
`, wsID, testUserID); err != nil {
		t.Fatalf("create owner member: %v", err)
	}

	const avatarURL = "https://cdn.example.com/workspaces/abc/logo.png"

	w := httptest.NewRecorder()
	req := newRequest("PATCH", "/api/workspaces/"+wsID, map[string]any{
		"avatar_url": avatarURL,
	})
	req = withTestWorkspaceMember(req, wsID, testUserID)
	req = withURLParam(req, "id", wsID)
	testHandler.UpdateWorkspace(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 from UpdateWorkspace, got %d: %s", w.Code, w.Body.String())
	}

	var resp protocol.WorkspaceResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.AvatarURL == nil || *resp.AvatarURL != avatarURL {
		t.Fatalf("expected avatar_url %q in response, got %v", avatarURL, resp.AvatarURL)
	}
	if resp.Name != "Handler Test Avatar URL" {
		t.Fatalf("name should be unchanged by avatar-only update, got %q", resp.Name)
	}

	var dbAvatar *string
	if err := testPool.QueryRow(ctx, `SELECT avatar_url FROM workspace WHERE id = $1`, wsID).Scan(&dbAvatar); err != nil {
		t.Fatalf("read avatar_url back: %v", err)
	}
	if dbAvatar == nil || *dbAvatar != avatarURL {
		t.Fatalf("expected avatar_url %q persisted, got %v", avatarURL, dbAvatar)
	}

	// A follow-up update that doesn't include avatar_url must leave it alone.
	w2 := httptest.NewRecorder()
	req2 := newRequest("PATCH", "/api/workspaces/"+wsID, map[string]any{
		"description": "new description",
	})
	req2 = withTestWorkspaceMember(req2, wsID, testUserID)
	req2 = withURLParam(req2, "id", wsID)
	testHandler.UpdateWorkspace(w2, req2)

	if w2.Code != http.StatusOK {
		t.Fatalf("expected 200 from second UpdateWorkspace, got %d: %s", w2.Code, w2.Body.String())
	}

	var resp2 protocol.WorkspaceResponse
	if err := json.Unmarshal(w2.Body.Bytes(), &resp2); err != nil {
		t.Fatalf("decode second response: %v", err)
	}
	if resp2.AvatarURL == nil || *resp2.AvatarURL != avatarURL {
		t.Fatalf("avatar_url should be preserved by partial update, got %v", resp2.AvatarURL)
	}
}

func TestUpdateWorkspace_ReposValidation(t *testing.T) {
	ctx := context.Background()

	const slug = "handler-tests-repos-validation"
	_, _ = testPool.Exec(ctx, `DELETE FROM workspace WHERE slug = $1`, slug)

	var wsID string
	if err := testPool.QueryRow(ctx, `
INSERT INTO workspace (name, slug, description)
VALUES ($1, $2, $3)
RETURNING id
`, "Handler Test Repos Validation", slug, "UpdateWorkspace repos validation test").Scan(&wsID); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, wsID)
	})

	if _, err := testPool.Exec(ctx, `
INSERT INTO member (workspace_id, user_id, role)
VALUES ($1, $2, 'owner')
`, wsID, testUserID); err != nil {
		t.Fatalf("create owner member: %v", err)
	}

	t.Run("rejects non-object settings without persisting", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := newRequest("PATCH", "/api/workspaces/"+wsID, map[string]any{
			"settings": []any{"not", "an", "object"},
		})
		req = withTestWorkspaceMember(req, wsID, testUserID)
		req = withURLParam(req, "id", wsID)
		testHandler.UpdateWorkspace(w, req)

		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 from non-object settings update, got %d: %s", w.Code, w.Body.String())
		}

		var raw []byte
		if err := testPool.QueryRow(ctx, `SELECT settings FROM workspace WHERE id = $1`, wsID).Scan(&raw); err != nil {
			t.Fatalf("read settings: %v", err)
		}
		var settings map[string]any
		if err := json.Unmarshal(raw, &settings); err != nil {
			t.Fatalf("decode settings: %v", err)
		}
		for key, defaultValue := range workspaceBooleanSettingDefaults {
			if settings[key] != defaultValue {
				t.Fatalf("invalid settings update changed %s: %#v", key, settings[key])
			}
		}
	})

	t.Run("rejects invalid repo URLs without persisting", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := newRequest("PATCH", "/api/workspaces/"+wsID, map[string]any{
			"repos": []map[string]any{
				{"url": "not-a-url"},
			},
		})
		req = withTestWorkspaceMember(req, wsID, testUserID)
		req = withURLParam(req, "id", wsID)
		testHandler.UpdateWorkspace(w, req)

		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 from invalid repos update, got %d: %s", w.Code, w.Body.String())
		}

		var raw []byte
		if err := testPool.QueryRow(ctx, `SELECT repos FROM workspace WHERE id = $1`, wsID).Scan(&raw); err != nil {
			t.Fatalf("read repos: %v", err)
		}
		if string(raw) != "[]" {
			t.Fatalf("invalid repos update should not persist, got %s", raw)
		}
	})

	t.Run("normalizes valid repos", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := newRequest("PATCH", "/api/workspaces/"+wsID, map[string]any{
			"repos": []map[string]any{
				{
					"url":         "  https://github.com/multica-ai/multica.git  ",
					"description": "  main monorepo  ",
				},
				{
					"url": "https://github.com/multica-ai/multica.git",
				},
				{
					"url": "git@github.com:multica-ai/multica-cloud.git",
				},
			},
		})
		req = withTestWorkspaceMember(req, wsID, testUserID)
		req = withURLParam(req, "id", wsID)
		testHandler.UpdateWorkspace(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 from valid repos update, got %d: %s", w.Code, w.Body.String())
		}

		var raw []byte
		if err := testPool.QueryRow(ctx, `SELECT repos FROM workspace WHERE id = $1`, wsID).Scan(&raw); err != nil {
			t.Fatalf("read repos: %v", err)
		}
		var repos []workspaceRepoRef
		if err := json.Unmarshal(raw, &repos); err != nil {
			t.Fatalf("decode repos: %v", err)
		}
		if len(repos) != 2 {
			t.Fatalf("expected duplicate URL to be deduped, got %d repos: %s", len(repos), raw)
		}
		if repos[0].URL != "https://github.com/multica-ai/multica.git" || repos[0].Description != "main monorepo" {
			t.Fatalf("first repo not normalized: %+v", repos[0])
		}
		if repos[1].URL != "git@github.com:multica-ai/multica-cloud.git" {
			t.Fatalf("second repo not preserved: %+v", repos[1])
		}
	})

	t.Run("rejects incomplete or mismatched Gongfeng repos", func(t *testing.T) {
		cases := []map[string]any{
			{
				"url": "https://git.code.tencent.com/ChainWeaver/ida/user-center",
			},
			{
				"url":            "https://git.code.tencent.com/ChainWeaver/ida/user-center",
				"project_path":   "ChainWeaver/ida/gateway",
				"default_branch": "main",
			},
		}
		for _, repo := range cases {
			w := httptest.NewRecorder()
			req := newRequest("PATCH", "/api/workspaces/"+wsID, map[string]any{
				"repos": []map[string]any{repo},
			})
			req = withTestWorkspaceMember(req, wsID, testUserID)
			req = withURLParam(req, "id", wsID)
			testHandler.UpdateWorkspace(w, req)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("incomplete Gongfeng repo should return 400, got %d: %s", w.Code, w.Body.String())
			}
		}
	})

	t.Run("keeps gongfeng project resources backed by project_path", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := newRequest("PATCH", "/api/workspaces/"+wsID, map[string]any{
			"repos": []map[string]any{
				{
					"url":            "https://git.code.tencent.com/ChainWeaver/ida/user-center/commits/v5.0.0_dev",
					"provider":       "gongfeng",
					"project_path":   "ChainWeaver/ida/user-center",
					"default_branch": "v5.0.0_dev",
				},
				{
					"url":            "https://git.code.tencent.com/ChainWeaver/ida/user-center/-/tree/release",
					"provider":       "gongfeng",
					"project_path":   "ChainWeaver/ida/user-center",
					"default_branch": "release",
				},
			},
		})
		req = withTestWorkspaceMember(req, wsID, testUserID)
		req = withURLParam(req, "id", wsID)
		testHandler.UpdateWorkspace(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("seed repos: expected 200, got %d: %s", w.Code, w.Body.String())
		}

		w = httptest.NewRecorder()
		req = newRequest("POST", "/api/projects?workspace_id="+wsID, map[string]any{
			"title": "Workspace repo removal guard",
		})
		req = withTestWorkspaceMember(req, wsID, testUserID)
		testHandler.CreateProject(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("CreateProject: expected 201, got %d: %s", w.Code, w.Body.String())
		}
		var project projectResponse
		if err := json.NewDecoder(w.Body).Decode(&project); err != nil {
			t.Fatalf("decode CreateProject: %v", err)
		}
		t.Cleanup(func() {
			r := newRequest("DELETE", "/api/projects/"+project.ID, nil)
			r = withTestWorkspaceMember(r, wsID, testUserID)
			r = withURLParam(r, "id", project.ID)
			testHandler.DeleteProject(httptest.NewRecorder(), r)
		})

		w = httptest.NewRecorder()
		req = newRequest("POST", "/api/projects/"+project.ID+"/resources", map[string]any{
			"resource_type": "gongfeng_repo",
			"resource_ref": map[string]any{
				"url": "https://git.code.tencent.com/ChainWeaver/ida/user-center/commits/v5.0.0_dev",
			},
		})
		req = withTestWorkspaceMember(req, wsID, testUserID)
		req = withURLParam(req, "id", project.ID)
		testHandler.CreateProjectResource(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("CreateProjectResource: expected 201, got %d: %s", w.Code, w.Body.String())
		}

		w = httptest.NewRecorder()
		req = newRequest("PATCH", "/api/workspaces/"+wsID, map[string]any{
			"repos": []map[string]any{
				{
					"url":            "https://git.code.tencent.com/ChainWeaver/ida/user-center/-/tree/release",
					"provider":       "gongfeng",
					"project_path":   "ChainWeaver/ida/user-center",
					"default_branch": "release",
				},
			},
		})
		req = withTestWorkspaceMember(req, wsID, testUserID)
		req = withURLParam(req, "id", wsID)
		testHandler.UpdateWorkspace(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("remove one duplicate project_path repo: expected 200, got %d: %s", w.Code, w.Body.String())
		}

		w = httptest.NewRecorder()
		req = newRequest("PATCH", "/api/workspaces/"+wsID, map[string]any{
			"repos": []map[string]any{},
		})
		req = withTestWorkspaceMember(req, wsID, testUserID)
		req = withURLParam(req, "id", wsID)
		testHandler.UpdateWorkspace(w, req)
		if w.Code != http.StatusConflict {
			t.Fatalf("remove last project_path repo: expected 409, got %d: %s", w.Code, w.Body.String())
		}
	})
}

// revocationFixture is a minimal workspace/member/runtime/agent/queued-task
// bundle used to drive the revocation tests.
type revocationFixture struct {
	WorkspaceID  string
	TargetUserID string
	MemberID     string
	RuntimeID    string
	AgentID      string
	TaskID       string
}

func setupRevocationFixture(t *testing.T, slug, daemonID string) revocationFixture {
	t.Helper()
	ctx := context.Background()

	_, _ = testPool.Exec(ctx, `DELETE FROM workspace WHERE slug = $1`, slug)

	var wsID string
	if err := testPool.QueryRow(ctx, `
INSERT INTO workspace (name, slug, description, issue_prefix)
VALUES ($1, $2, $3, $4)
RETURNING id
`, "Revocation "+slug, slug, "revocation test", "REV").Scan(&wsID); err != nil {
		t.Fatalf("create workspace: %v", err)
	}

	// Requester (= testUserID) is always an owner so DeleteMember authorization
	// passes. Two owners total so LeaveWorkspace doesn't trip the "must keep
	// at least one owner" guard.
	if _, err := testPool.Exec(ctx, `
INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, 'owner')
`, wsID, testUserID); err != nil {
		t.Fatalf("create requester member: %v", err)
	}

	targetAccount := fmt.Sprintf("revocation-%s@multica", slug)
	var targetUserID string
	if err := testPool.QueryRow(ctx, `
INSERT INTO "user" (name, account) VALUES ($1, $2) RETURNING id
`, "Revocation Target "+slug, targetAccount).Scan(&targetUserID); err != nil {
		t.Fatalf("create target user: %v", err)
	}

	// Cleanup ordering: workspace first (cascade clears agent_runtime,
	// agent and member), then user (whose deletion would
	// otherwise be blocked by agent.owner_id / agent_runtime.owner_id FKs).
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, wsID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM "user" WHERE id = $1`, targetUserID)
	})

	var memberID string
	if err := testPool.QueryRow(ctx, `
INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, 'owner') RETURNING id
`, wsID, targetUserID).Scan(&memberID); err != nil {
		t.Fatalf("create target member: %v", err)
	}

	var runtimeID string
	if err := testPool.QueryRow(ctx, `
INSERT INTO agent_runtime (
    workspace_id, daemon_id, name, runtime_mode, provider, status,
    device_info, metadata, owner_id, last_seen_at
)
VALUES ($1, $2, 'Target Runtime', 'local', 'multica_daemon', 'online', '', '{}'::jsonb, $3, now())
RETURNING id
`, wsID, daemonID, targetUserID).Scan(&runtimeID); err != nil {
		t.Fatalf("insert runtime: %v", err)
	}

	var agentID string
	if err := testPool.QueryRow(ctx, `
INSERT INTO agent (
    workspace_id, name, description, runtime_mode, runtime_config,
    runtime_id, scope, max_concurrent_tasks, owner_id
)
VALUES ($1, 'Target Agent', '', 'local', '{}'::jsonb, $2, 'workspace', 1, $3)
RETURNING id
`, wsID, runtimeID, targetUserID).Scan(&agentID); err != nil {
		t.Fatalf("insert agent: %v", err)
	}

	var taskID string
	if err := testPool.QueryRow(ctx, `
INSERT INTO agent_task_queue (agent_id, runtime_id, status, priority)
VALUES ($1, $2, 'queued', 0)
RETURNING id
`, agentID, runtimeID).Scan(&taskID); err != nil {
		t.Fatalf("insert task: %v", err)
	}

	return revocationFixture{
		WorkspaceID:  wsID,
		TargetUserID: targetUserID,
		MemberID:     memberID,
		RuntimeID:    runtimeID,
		AgentID:      agentID,
		TaskID:       taskID,
	}
}

func assertRevoked(t *testing.T, fx revocationFixture) {
	t.Helper()
	ctx := context.Background()

	var memberExists bool
	if err := testPool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM member WHERE id = $1)`, fx.MemberID).Scan(&memberExists); err != nil {
		t.Fatalf("query member: %v", err)
	}
	if memberExists {
		t.Fatal("member row was not deleted")
	}

	var runtimeStatus string
	if err := testPool.QueryRow(ctx, `SELECT status FROM agent_runtime WHERE id = $1`, fx.RuntimeID).Scan(&runtimeStatus); err != nil {
		t.Fatalf("query runtime: %v", err)
	}
	if runtimeStatus != "offline" {
		t.Fatalf("expected runtime offline, got %q", runtimeStatus)
	}

	var archivedAt *string
	if err := testPool.QueryRow(ctx, `SELECT archived_at::text FROM agent WHERE id = $1`, fx.AgentID).Scan(&archivedAt); err != nil {
		t.Fatalf("query agent: %v", err)
	}
	if archivedAt == nil {
		t.Fatal("agent was not archived")
	}

	var taskStatus string
	if err := testPool.QueryRow(ctx, `SELECT status FROM agent_task_queue WHERE id = $1`, fx.TaskID).Scan(&taskStatus); err != nil {
		t.Fatalf("query task: %v", err)
	}
	if taskStatus != "cancelled" {
		t.Fatalf("expected task cancelled, got %q", taskStatus)
	}
}

// TestDeleteMember_RevokesTargetRuntimes verifies that when an admin removes
// another member from a workspace, every runtime owned by the removed member
// has its agents archived, its in-flight tasks cancelled, its row flipped
// offline — all atomically with the member row deletion.
func TestDeleteMember_RevokesTargetRuntimes(t *testing.T) {
	fx := setupRevocationFixture(t, "handler-tests-revoke-kick", "daemon-revoke-kick")

	w := httptest.NewRecorder()
	req := newRequest("DELETE", "/api/workspaces/"+fx.WorkspaceID+"/members/"+fx.MemberID, nil)
	req = withTestWorkspaceMember(req, fx.WorkspaceID, testUserID)
	req = withURLParams(req, "id", fx.WorkspaceID, "memberId", fx.MemberID)
	testHandler.DeleteMember(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("DeleteMember: expected 204, got %d: %s", w.Code, w.Body.String())
	}

	assertRevoked(t, fx)
}

// TestLeaveWorkspace_RevokesOwnRuntimes is the self-removal counterpart: when
// a member leaves a workspace voluntarily, their own runtimes are revoked
// with the same atomic write set as DeleteMember.
func TestLeaveWorkspace_RevokesOwnRuntimes(t *testing.T) {
	fx := setupRevocationFixture(t, "handler-tests-revoke-leave", "daemon-revoke-leave")

	// Re-target the request from the leaving member's perspective: the
	// leaver is the request actor, not the workspace owner.
	w := httptest.NewRecorder()
	req := newRequest("DELETE", "/api/workspaces/"+fx.WorkspaceID+"/leave", nil)
	req = withTestWorkspaceMember(req, fx.WorkspaceID, fx.TargetUserID)
	req = withURLParam(req, "id", fx.WorkspaceID)
	testHandler.LeaveWorkspace(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("LeaveWorkspace: expected 204, got %d: %s", w.Code, w.Body.String())
	}

	assertRevoked(t, fx)
}

// TestDeleteMember_CancelsTasksFromAgentReassignment covers a subtle
// case: an agent's runtime_id can be changed via UpdateAgent, but
// agent_task_queue.runtime_id keeps the value from when the task was
// queued. So after a leaving member is removed, an agent currently bound
// to their runtime gets archived — but tasks that agent queued under a
// PRIOR runtime (still owned by another active member) keep their old
// runtime_id and would not be caught by a runtime-only sweep. Because
// ClaimAgentTask does not gate on agent.archived_at, those orphaned
// queued tasks would remain claimable.
func TestDeleteMember_CancelsTasksFromAgentReassignment(t *testing.T) {
	fx := setupRevocationFixture(t, "handler-tests-revoke-reassign", "daemon-revoke-reassign")
	ctx := context.Background()

	// Create a SECOND runtime in the workspace owned by the requester
	// (not the leaving member). The agent originally lived here.
	var otherRuntimeID string
	if err := testPool.QueryRow(ctx, `
INSERT INTO agent_runtime (
    workspace_id, daemon_id, name, runtime_mode, provider, status,
    device_info, metadata, owner_id, last_seen_at
)
VALUES ($1, $2, 'Other Runtime', 'local', 'multica_daemon', 'online', '', '{}'::jsonb, $3, now())
RETURNING id
`, fx.WorkspaceID, "daemon-revoke-reassign-other", testUserID).Scan(&otherRuntimeID); err != nil {
		t.Fatalf("insert other runtime: %v", err)
	}

	// Queue a task on the agent while it was still pinned to the OTHER
	// runtime (simulating a task created before the agent was reassigned
	// to the leaving member's runtime).
	var orphanTaskID string
	if err := testPool.QueryRow(ctx, `
INSERT INTO agent_task_queue (agent_id, runtime_id, status, priority)
VALUES ($1, $2, 'queued', 0)
RETURNING id
`, fx.AgentID, otherRuntimeID).Scan(&orphanTaskID); err != nil {
		t.Fatalf("insert orphan task: %v", err)
	}

	w := httptest.NewRecorder()
	req := newRequest("DELETE", "/api/workspaces/"+fx.WorkspaceID+"/members/"+fx.MemberID, nil)
	req = withTestWorkspaceMember(req, fx.WorkspaceID, testUserID)
	req = withURLParams(req, "id", fx.WorkspaceID, "memberId", fx.MemberID)
	testHandler.DeleteMember(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("DeleteMember: expected 204, got %d: %s", w.Code, w.Body.String())
	}

	assertRevoked(t, fx)

	// The orphan task — same agent, different runtime — must also be
	// cancelled. Without the by-agent leg in CancelAgentTasksByRuntimeOrAgent
	// this stays 'queued' and would be picked up by the other runtime.
	var orphanStatus string
	if err := testPool.QueryRow(ctx, `SELECT status FROM agent_task_queue WHERE id = $1`, orphanTaskID).Scan(&orphanStatus); err != nil {
		t.Fatalf("query orphan task: %v", err)
	}
	if orphanStatus != "cancelled" {
		t.Fatalf("expected orphan task cancelled (archived agent leftover on other runtime), got %q", orphanStatus)
	}

	// And the OTHER runtime — owned by an active member — must still be
	// online: revocation is scoped to the leaving member's owned runtimes.
	var otherStatus string
	if err := testPool.QueryRow(ctx, `SELECT status FROM agent_runtime WHERE id = $1`, otherRuntimeID).Scan(&otherStatus); err != nil {
		t.Fatalf("query other runtime: %v", err)
	}
	if otherStatus != "online" {
		t.Fatalf("expected other-member runtime to stay online, got %q", otherStatus)
	}
}

// TestDeleteMember_NoRuntimes_DeletesMember covers the empty-revocation
// path: a member with no owned runtimes should still have their member row
// deleted by the same atomic transaction, with no spurious archive/cancel
// writes.
func TestDeleteMember_NoRuntimes_DeletesMember(t *testing.T) {
	ctx := context.Background()
	const slug = "handler-tests-revoke-no-runtimes"
	_, _ = testPool.Exec(ctx, `DELETE FROM workspace WHERE slug = $1`, slug)

	var wsID string
	if err := testPool.QueryRow(ctx, `
INSERT INTO workspace (name, slug, description, issue_prefix)
VALUES ($1, $2, $3, $4)
RETURNING id
`, "Revocation no runtimes", slug, "revocation no-runtimes test", "REV").Scan(&wsID); err != nil {
		t.Fatalf("create workspace: %v", err)
	}

	if _, err := testPool.Exec(ctx, `
INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, 'owner')
`, wsID, testUserID); err != nil {
		t.Fatalf("create requester member: %v", err)
	}

	var targetUserID string
	if err := testPool.QueryRow(ctx, `
INSERT INTO "user" (name, account) VALUES ($1, $2) RETURNING id
`, "Revocation No Runtimes Target", "revocation-no-runtimes@multica").Scan(&targetUserID); err != nil {
		t.Fatalf("create target user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, wsID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM "user" WHERE id = $1`, targetUserID)
	})

	var memberID string
	if err := testPool.QueryRow(ctx, `
INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, 'admin') RETURNING id
`, wsID, targetUserID).Scan(&memberID); err != nil {
		t.Fatalf("create target member: %v", err)
	}

	w := httptest.NewRecorder()
	req := newRequest("DELETE", "/api/workspaces/"+wsID+"/members/"+memberID, nil)
	req = withTestWorkspaceMember(req, wsID, testUserID)
	req = withURLParams(req, "id", wsID, "memberId", memberID)
	testHandler.DeleteMember(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("DeleteMember: expected 204, got %d: %s", w.Code, w.Body.String())
	}

	var memberExists bool
	if err := testPool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM member WHERE id = $1)`, memberID).Scan(&memberExists); err != nil {
		t.Fatalf("query member: %v", err)
	}
	if memberExists {
		t.Fatal("member row was not deleted")
	}
}
