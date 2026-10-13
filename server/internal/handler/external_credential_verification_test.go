package handler

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestGongfengCredentialSaveAndRetestUsePlatformVerification(t *testing.T) {
	var reject atomic.Bool
	var requests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/api/v3/user" || r.Header.Get("PRIVATE-TOKEN") != "fixture-token" {
			t.Errorf("unexpected credential verification request")
		}
		if reject.Load() {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":1}`))
	}))
	defer upstream.Close()
	t.Setenv("GONGFENG_API_BASE", upstream.URL+"/api/v3")
	t.Setenv("GONGFENG_PRIVATE_TOKEN", "fixture-token")
	key := uuid.NewString()
	create := newRequest("POST", "/api/external-credential-profiles", map[string]any{"provider": "gongfeng", "name": "Repository verification fixture", "secret_ref": "env:GONGFENG_PRIVATE_TOKEN", "verify_now": true})
	create.Header.Set("Idempotency-Key", key)
	var profile externalCredentialProfileResponse
	testutil.Call(t, testHandler.CreateExternalCredentialProfile, create).Want(http.StatusCreated).JSON(&profile)
	t.Cleanup(func() {
		req := withURLParam(newRequest("DELETE", "/api/external-credential-profiles/"+profile.ID, nil), "id", profile.ID)
		testutil.Call(t, testHandler.DeleteExternalCredentialProfile, req).Want(http.StatusNoContent)
	})
	if profile.Status != "verified" {
		t.Fatalf("saved credential status = %q, want verified", profile.Status)
	}
	if requests.Load() != 1 {
		t.Fatalf("verification requests = %d, want 1", requests.Load())
	}
	reject.Store(true)
	update := withURLParam(newRequest("PUT", "/api/external-credential-profiles/"+profile.ID, map[string]any{"verify_now": true}), "id", profile.ID)
	testutil.Call(t, testHandler.UpdateExternalCredentialProfile, update).Want(http.StatusOK).JSON(&profile)
	if profile.Status != "failed" {
		t.Fatalf("retested credential status = %q, want failed", profile.Status)
	}
	if requests.Load() != 2 {
		t.Fatalf("verification requests = %d, want 2", requests.Load())
	}
	reject.Store(false)
	update = withURLParam(newRequest("PUT", "/api/external-credential-profiles/"+profile.ID, map[string]any{"verify_now": true}), "id", profile.ID)
	testutil.Call(t, testHandler.UpdateExternalCredentialProfile, update).Want(http.StatusOK).JSON(&profile)
	if profile.Status != "verified" {
		t.Fatalf("recovered credential status = %q, want verified", profile.Status)
	}
}
