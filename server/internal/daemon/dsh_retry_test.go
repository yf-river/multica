package daemon

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/multica-ai/multica/server/pkg/dshprofile"
)

func TestDshExplicitRetryRetainsCandidateAndWakesDiscovery(t *testing.T) {
	root := t.TempDir()
	t.Setenv(dshprofile.StateEnv, root)
	t.Setenv(dshprofile.ArtifactEnv, t.TempDir())
	t.Setenv("MULTICA_DSH_MANAGED_PROFILE", "")
	status := dshSupplyStatus{Status: "degraded", Identity: "fixed-candidate", Error: "credential missing"}
	if err := dshprofile.WriteJSON(root, "status.json", status); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{agentDiscoveryKick: make(chan struct{}, 1)}
	request := httptest.NewRequest(http.MethodPost, "/dsh-profile/retry", bytes.NewReader(nil))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	d.dshRetryHandler().ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatalf("retry refused: %d %s", response.Code, response.Body.String())
	}
	next := readDshSupplyStatus(root)
	if next.Status != "pending" || next.Identity != status.Identity || next.Error != "" {
		t.Fatalf("candidate identity changed: %#v", next)
	}
	select {
	case <-d.agentDiscoveryKick:
	default:
		t.Fatal("discovery was not woken")
	}
	response = httptest.NewRecorder()
	d.dshRetryHandler().ServeHTTP(response, request)
	if response.Code != http.StatusConflict {
		t.Fatal("nonfailed candidate retried")
	}
}

func TestDshExplicitRetryRejectsBrowserRequest(t *testing.T) {
	d := &Daemon{}
	for _, origin := range []string{"", "https://example.com"} {
		request := httptest.NewRequest(http.MethodPost, "/dsh-profile/retry", nil)
		if origin != "" {
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Origin", origin)
		}
		response := httptest.NewRecorder()
		d.dshRetryHandler().ServeHTTP(response, request)
		if response.Code != http.StatusForbidden {
			t.Fatalf("browser retry accepted: %d", response.Code)
		}
	}
}
