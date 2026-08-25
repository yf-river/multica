package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/integrations/lark"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Lark-handler unit tests focus on the no-config short-circuits —
// verifying that a self-host deployment without MULTICA_LARK_SECRET_KEY
// does NOT serve revoke / redeem / install, and that list degrades
// gracefully to an empty response so the Integrations tab still
// renders. Happy-path flows (begin device-flow + poll status; token
// mint + redeem) need a real DB and land alongside the WS hub
// integration tests in a follow-up commit.

func TestRevokeLarkInstallation_NotConfigured(t *testing.T) {
	h := &Handler{}
	req := httptest.NewRequest(http.MethodDelete, "/api/workspaces/x/lark/installations/y", nil)
	w := httptest.NewRecorder()
	h.RevokeLarkInstallation(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", w.Code)
	}
}

func TestRedeemLarkBindingToken_NotConfigured(t *testing.T) {
	h := &Handler{}
	req := httptest.NewRequest(http.MethodPost, "/api/lark/binding/redeem", strings.NewReader(`{"token":"x"}`))
	w := httptest.NewRecorder()
	h.RedeemLarkBindingToken(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", w.Code)
	}
}

func TestLarkBindingTokenReplayReturnsCommittedBinding(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	agentID := createHandlerTestAgent(t, "lark binding replay agent", nil)
	installation, err := testHandler.Queries.UpsertLarkInstallation(ctx, db.UpsertLarkInstallationParams{
		WorkspaceID:        parseUUID(testWorkspaceID),
		AgentID:            parseUUID(agentID),
		AppID:              "binding-replay-" + time.Now().UTC().Format("20060102150405.000000000"),
		AppSecretEncrypted: []byte("test-encrypted-secret"),
		BotOpenID:          "binding-replay-bot",
		InstallerUserID:    parseUUID(testUserID),
		Region:             "feishu",
	})
	if err != nil {
		t.Fatalf("create Lark installation: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM lark_installation WHERE id = $1`, installation.ID)
	})

	service := lark.NewBindingTokenService(testHandler.Queries, testPool)
	token, err := service.Mint(ctx, installation.WorkspaceID, installation.ID, lark.OpenID("binding-replay-user"))
	if err != nil {
		t.Fatalf("mint binding token: %v", err)
	}
	first, err := service.RedeemAndBind(ctx, token.Raw, parseUUID(testUserID))
	if err != nil {
		t.Fatalf("first redemption: %v", err)
	}
	second, err := service.RedeemAndBind(ctx, token.Raw, parseUUID(testUserID))
	if err != nil {
		t.Fatalf("retry after committed response loss: %v", err)
	}
	if second != first {
		t.Fatalf("retry response = %+v, want exact committed result %+v", second, first)
	}

	otherUserID := createWorkspaceMemberUser(
		t, "Lark binding replay attacker", "lark-binding-replay-attacker@multica.test",
	)
	if _, err := service.RedeemAndBind(ctx, token.Raw, parseUUID(otherUserID)); !errors.Is(err, lark.ErrBindingTokenInvalid) {
		t.Fatalf("different user replay error = %v, want opaque invalid token", err)
	}

	concurrentToken, err := service.Mint(
		ctx, installation.WorkspaceID, installation.ID, lark.OpenID("binding-concurrent-user"),
	)
	if err != nil {
		t.Fatalf("mint concurrent binding token: %v", err)
	}
	const callers = 8
	results := make(chan lark.RedeemedBindingToken, callers)
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := service.RedeemAndBind(ctx, concurrentToken.Raw, parseUUID(testUserID))
			results <- result
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent redemption: %v", err)
		}
	}
	var concurrentResult *lark.RedeemedBindingToken
	for result := range results {
		if concurrentResult == nil {
			concurrentResult = &result
			continue
		}
		if result != *concurrentResult {
			t.Fatalf("concurrent replay result = %+v, want %+v", result, *concurrentResult)
		}
	}

	expiredToken, err := service.Mint(
		ctx, installation.WorkspaceID, installation.ID, lark.OpenID("binding-expired-user"),
	)
	if err != nil {
		t.Fatalf("mint expiring binding token: %v", err)
	}
	if _, err := testPool.Exec(ctx, `
UPDATE lark_binding_token
SET expires_at = now() - interval '1 second'
WHERE installation_id = $1 AND lark_open_id = 'binding-expired-user'
`, installation.ID); err != nil {
		t.Fatalf("expire binding token: %v", err)
	}
	if _, err := service.RedeemAndBind(ctx, expiredToken.Raw, parseUUID(testUserID)); !errors.Is(err, lark.ErrBindingTokenInvalid) {
		t.Fatalf("expired first redemption error = %v, want invalid token", err)
	}
	var consumed bool
	if err := testPool.QueryRow(ctx, `
SELECT consumed_at IS NOT NULL
FROM lark_binding_token
WHERE installation_id = $1 AND lark_open_id = 'binding-expired-user'
`, installation.ID).Scan(&consumed); err != nil {
		t.Fatalf("read expired binding token: %v", err)
	}
	if consumed {
		t.Fatal("expired token must remain unconsumed")
	}
}

func TestBeginLarkInstall_NotConfigured(t *testing.T) {
	// When the device-flow registration service is nil (no at-rest
	// key or no real API client), the begin
	// endpoint must short-circuit to 503 — silently returning a
	// "configured: false" envelope would hide a real misconfiguration
	// from the operator. The UI hides the bind button in that case
	// so this should not be reached through the normal flow.
	h := &Handler{}
	req := httptest.NewRequest(http.MethodPost, "/api/workspaces/x/lark/install/begin?agent_id=y", nil)
	w := httptest.NewRecorder()
	h.BeginLarkInstall(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestBeginLarkInstall_RequiresExplicitRegion(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	original := testHandler.LarkRegistration
	testHandler.LarkRegistration = &lark.RegistrationService{}
	t.Cleanup(func() { testHandler.LarkRegistration = original })

	req := newRequest(http.MethodPost,
		"/api/workspaces/"+testWorkspaceID+"/lark/install/begin?agent_id=11111111-1111-1111-1111-111111111111", nil)
	req = withURLParam(req, "id", testWorkspaceID)
	w := httptest.NewRecorder()
	testHandler.BeginLarkInstall(w, req)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "region must be") {
		t.Fatalf("missing region = %d %s, want 400", w.Code, w.Body.String())
	}
}

func TestBeginLarkInstallClientCanceledReturns499(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	original := testHandler.LarkRegistration
	testHandler.LarkRegistration = &lark.RegistrationService{}
	t.Cleanup(func() { testHandler.LarkRegistration = original })

	agentID := createHandlerTestAgent(t, "lark canceled agent", nil)
	req := newRequest(
		http.MethodPost,
		"/api/workspaces/"+testWorkspaceID+"/lark/install/begin?agent_id="+agentID+"&region=feishu",
		nil,
	)
	ctx, cancel := context.WithCancel(req.Context())
	cancel()
	req = req.WithContext(ctx)
	req = withURLParam(req, "id", testWorkspaceID)
	w := httptest.NewRecorder()

	testHandler.BeginLarkInstall(w, req)

	if w.Code != 499 {
		t.Fatalf("canceled Lark agent lookup: expected 499, got %d: %s", w.Code, w.Body.String())
	}
}

func TestGetLarkInstallStatus_NotConfigured(t *testing.T) {
	h := &Handler{}
	req := httptest.NewRequest(http.MethodGet, "/api/workspaces/x/lark/install/sess_y/status", nil)
	w := httptest.NewRecorder()
	h.GetLarkInstallStatus(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", w.Code)
	}
}

func TestListLarkInstallations_NotConfiguredReturnsEmpty(t *testing.T) {
	// Listing is intentionally a "soft" endpoint: when lark is not
	// configured we return an empty list + configured:false rather
	// than a 503, so the Integrations tab renders normally with a
	// "not connected" empty state instead of an error banner.
	h := &Handler{}
	req := httptest.NewRequest(http.MethodGet, "/api/workspaces/x/lark/installations", nil)
	w := httptest.NewRecorder()
	h.ListLarkInstallations(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Installations    []any `json:"installations"`
		Configured       bool  `json:"configured"`
		InstallSupported bool  `json:"install_supported"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Configured {
		t.Fatalf("configured should be false when LarkInstallations is nil")
	}
	if resp.InstallSupported {
		t.Fatalf("install_supported should be false when LarkInstallations is nil")
	}
	if len(resp.Installations) != 0 {
		t.Fatalf("expected empty installations list, got %d", len(resp.Installations))
	}
}

// TestListLarkInstallations_NotConfigured_HardCodedInstallSupportedFalse
// pins the invariant for the early-return branch: when
// LarkInstallations is nil (the deployment has no at-rest encryption
// key wired), the response MUST return both configured:false AND
// install_supported:false regardless of what APIClient is in place.
// A real APIClient on a not-configured deployment must not flip
// install_supported via the APIClient path — that path is not
// consulted in the early-return branch.
func TestListLarkInstallations_NotConfigured_HardCodedInstallSupportedFalse(t *testing.T) {
	h := &Handler{
		LarkInstallations: nil, // triggers the not-configured early return.
		LarkAPIClient:     lark.NewHTTPAPIClient(lark.HTTPClientConfig{}),
	}
	req := httptest.NewRequest(http.MethodGet, "/api/workspaces/x/lark/installations", nil)
	w := httptest.NewRecorder()
	h.ListLarkInstallations(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp struct {
		Configured       bool `json:"configured"`
		InstallSupported bool `json:"install_supported"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Configured {
		t.Fatalf("configured must be false when LarkInstallations is nil")
	}
	if resp.InstallSupported {
		t.Fatalf("install_supported must be false in the early-return branch even with a non-nil APIClient")
	}
}
