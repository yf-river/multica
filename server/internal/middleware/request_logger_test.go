package middleware

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

var defaultLoggerMu sync.Mutex

func withCapturedLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	defaultLoggerMu.Lock()
	buf := &bytes.Buffer{}
	orig := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() {
		slog.SetDefault(orig)
		defaultLoggerMu.Unlock()
	})
	return buf
}

func runRequestLogger(t *testing.T, status int, body string) *bytes.Buffer {
	t.Helper()
	logs := withCapturedLogs(t)
	handler := RequestLogger(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	req := httptest.NewRequest(http.MethodPost, "/api/daemon/heartbeat", nil).
		WithContext(context.Background())
	handler.ServeHTTP(httptest.NewRecorder(), req)
	return logs
}

func requireLogLevel(t *testing.T, logs *bytes.Buffer, want string) {
	t.Helper()
	out := logs.String()
	if !strings.Contains(out, "level="+want) {
		t.Fatalf("expected level=%s in logs, got:\n%s", want, out)
	}
	for _, level := range []string{"INFO", "WARN", "ERROR"} {
		if level != want && strings.Contains(out, "level="+level) {
			t.Fatalf("did not expect level=%s in logs, got:\n%s", level, out)
		}
	}
}

func TestRequestLoggerLevels(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		status      int
		body, level string
	}{
		{name: "runtime not found", status: http.StatusNotFound, body: `{"error":"runtime not found"}`, level: "INFO"},
		{name: "task not found", status: http.StatusNotFound, body: `{"error":"task not found"}`, level: "INFO"},
		{name: "generic not found", status: http.StatusNotFound, body: `{"error":"not found"}`, level: "WARN"},
		{name: "bad request", status: http.StatusBadRequest, body: `{"error":"bad input"}`, level: "WARN"},
		{name: "server error", status: http.StatusInternalServerError, body: `{"error":"boom"}`, level: "ERROR"},
		{name: "success", status: http.StatusOK, body: `{"ok":true}`, level: "INFO"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			requireLogLevel(t, runRequestLogger(t, testCase.status, testCase.body), testCase.level)
		})
	}
}

func TestRequestLogger_HealthEndpointIsSkipped(t *testing.T) {
	logs := withCapturedLogs(t)
	handler := RequestLogger(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	handler.ServeHTTP(httptest.NewRecorder(), req)
	if logs.Len() != 0 {
		t.Fatalf("/health should not be logged, got:\n%s", logs.String())
	}
}

func TestRequestLogger_BodyStillReachesClient(t *testing.T) {
	rec := httptest.NewRecorder()
	handler := RequestLogger(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"runtime not found"}`))
	}))
	_ = withCapturedLogs(t)
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/daemon/heartbeat", nil))
	if got := rec.Body.String(); got != `{"error":"runtime not found"}` {
		t.Fatalf("response body lost or mutated: got %q", got)
	}
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestRequestLogger_LargeBodyBeyondCaptureLimit(t *testing.T) {
	prefix := strings.Repeat("x", softNotFoundBodyCaptureLimit+8)
	logs := runRequestLogger(t, http.StatusNotFound, prefix+`{"error":"runtime not found"}`)
	requireLogLevel(t, logs, "WARN")
}

func TestRedactWebhookPath(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, want string
	}{
		{"/api/webhooks/autopilots/awt_secret", "/api/webhooks/autopilots/[redacted]"},
		{"/api/webhooks/autopilots/awt_secret/", "/api/webhooks/autopilots/[redacted]/"},
		{"/api/webhooks/autopilots/", "/api/webhooks/autopilots/"},
		{"/api/webhooks/github", "/api/webhooks/github"},
		{"/api/runtimes/abc", "/api/runtimes/abc"},
		{"/", "/"},
	}
	for _, tc := range cases {
		if got := redactWebhookPath(tc.in); got != tc.want {
			t.Errorf("redactWebhookPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestRequestLogger_RedactsWebhookTokenInPath(t *testing.T) {
	logs := withCapturedLogs(t)
	handler := RequestLogger(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodPost, "/api/webhooks/autopilots/awt_supersecret", nil)
	handler.ServeHTTP(httptest.NewRecorder(), req)
	out := logs.String()
	if strings.Contains(out, "awt_supersecret") {
		t.Fatalf("token leaked into logs:\n%s", out)
	}
	if !strings.Contains(out, "[redacted]") {
		t.Fatalf("expected [redacted] in logs:\n%s", out)
	}
}

func TestRequestLogger_IncludesWebhookTriggerIDFromContext(t *testing.T) {
	logs := withCapturedLogs(t)
	handler := RequestLogger(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		SetWebhookTriggerID(r, "trigger-abc")
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodPost, "/api/webhooks/autopilots/awt_supersecret", nil)
	handler.ServeHTTP(httptest.NewRecorder(), req)
	out := logs.String()
	if !strings.Contains(out, "webhook_trigger_id=trigger-abc") {
		t.Fatalf("expected webhook_trigger_id in logs, got:\n%s", out)
	}
	if strings.Contains(out, "awt_supersecret") {
		t.Fatalf("token leaked into logs:\n%s", out)
	}
}

func TestIsSoftNotFound(t *testing.T) {
	t.Parallel()

	cases := []struct {
		body string
		want bool
	}{
		{`{"error":"runtime not found"}`, true},
		{`{"error":"task not found"}`, true},
		{`{"error":"Runtime Not Found"}`, true},
		{`{"error":"not found"}`, false},
		{`{"error":"workspace not found"}`, false},
		{"", false},
	}
	for _, tc := range cases {
		if got := isSoftNotFound([]byte(tc.body)); got != tc.want {
			t.Errorf("isSoftNotFound(%q) = %v, want %v", tc.body, got, tc.want)
		}
	}
}
