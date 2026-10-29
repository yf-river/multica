package handler

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	appmiddleware "github.com/multica-ai/multica/server/internal/middleware"
)

func TestOpenVikingStudioIframePolicyAndPrivateCredentials(t *testing.T) {
	const key = "private-test-only-studio-key"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != "" {
			t.Error("browser placeholder must not reach native key validation")
		}
		if r.Header.Get("Authorization") != "Bearer "+key || r.Header.Get("X-OpenViking-Account") != "default" || r.Header.Get("X-OpenViking-Role") != "" {
			t.Error("native proxy identity missing")
		}
		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
		io.WriteString(w, "<html><head></head><body>Studio</body></html>")
	}))
	defer upstream.Close()
	t.Setenv("MULTICA_OPENVIKING_URL", upstream.URL)
	t.Setenv("MULTICA_OPENVIKING_API_KEY", key)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest("GET", "/studio", nil)
	request.Header.Set("X-API-Key", "multica-session")
	appmiddleware.ContentSecurityPolicy(http.HandlerFunc(testHandler.OpenVikingStudio)).ServeHTTP(recorder, request)
	if recorder.Code != 200 {
		t.Fatalf("Studio failed: %d", recorder.Code)
	}
	for _, policy := range recorder.Header().Values("Content-Security-Policy") {
		if strings.Contains(policy, "frame-ancestors 'none'") || !strings.Contains(policy, "frame-ancestors 'self'") || !strings.Contains(policy, "'nonce-") {
			t.Fatalf("iframe/bootstrap blocked by policy: %s", policy)
		}
	}
	html := recorder.Body.String()
	if strings.Contains(html, key) || !strings.Contains(html, "<script nonce=\"") {
		t.Fatal("bootstrap must use CSP nonce without exposing credentials")
	}
}

func TestOpenVikingOriginalHTMLDownloadIsNotBootstrapped(t *testing.T) {
	const original = "<html><head></head><body>Original user document</body></html>"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, original)
	}))
	defer upstream.Close()
	t.Setenv("MULTICA_OPENVIKING_URL", upstream.URL)
	w := httptest.NewRecorder()
	testHandler.OpenVikingStudio(w, httptest.NewRequest("GET", "/api/openviking/api/v1/content/download?uri=viking%3A%2F%2Fresources%2Fdocument.html", nil))
	if w.Code != 200 || w.Body.String() != original {
		t.Fatal("proxy modified an original HTML attachment")
	}
}
