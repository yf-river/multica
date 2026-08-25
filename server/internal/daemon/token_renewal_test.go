package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func captureLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

type preflightAuthFixture struct {
	daemon     *Daemon
	logs       *bytes.Buffer
	syncCalled *atomic.Bool
}

func newPreflightAuthFixture(t *testing.T, renewHandler http.HandlerFunc) preflightAuthFixture {
	t.Helper()
	var syncCalled atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tokens/current/renew":
			renewHandler(w, r)
		case "/api/workspaces":
			syncCalled.Store(true)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[]`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	var buf bytes.Buffer
	d := &Daemon{client: NewClient(srv.URL), logger: captureLogger(&buf)}
	d.client.SetToken("mul_healthy")
	return preflightAuthFixture{daemon: d, logs: &buf, syncCalled: &syncCalled}
}

func newTokenRenewalDaemon(t *testing.T, status int, body, profile string) (*Daemon, *bytes.Buffer) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	var logs bytes.Buffer
	return &Daemon{
		client: NewClient(srv.URL),
		logger: captureLogger(&logs),
		cfg:    Config{Profile: profile},
	}, &logs
}

func TestClient_RenewToken_PostsToCorrectEndpoint(t *testing.T) {
	var called atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called.Add(1)
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/api/tokens/current/renew" {
			t.Errorf("expected /api/tokens/current/renew, got %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer mul_abc" {
			t.Errorf("expected Bearer mul_abc, got %q", got)
		}
		// Body must be valid JSON — postJSON marshals an empty object when
		// reqBody is a non-nil map[string]any{}.
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"expires_at": "2099-01-02T03:04:05Z",
			"renewed":    true,
		})
	}))
	t.Cleanup(srv.Close)

	c := NewClient(srv.URL)
	c.SetToken("mul_abc")

	resp, err := c.RenewToken(context.Background())
	if err != nil {
		t.Fatalf("RenewToken: %v", err)
	}
	if called.Load() != 1 {
		t.Fatalf("expected 1 server call, got %d", called.Load())
	}
	if !resp.Renewed {
		t.Fatal("expected renewed=true")
	}
	if resp.ExpiresAt != "2099-01-02T03:04:05Z" {
		t.Fatalf("expected expires_at to round-trip, got %q", resp.ExpiresAt)
	}
}

func TestTryRenewTokenLogging(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		body        string
		profile     string
		contains    []string
		notContains []string
	}{
		{name: "renewed", status: http.StatusOK, body: `{"expires_at":"2099-01-02T03:04:05Z","renewed":true}`, contains: []string{"auth token renewed", "2099-01-02T03:04:05Z"}},
		{name: "not eligible", status: http.StatusOK, body: `{"expires_at":"2099-01-02T03:04:05Z","renewed":false}`, notContains: []string{"WARN"}},
		{name: "unauthorized", status: http.StatusUnauthorized, body: `{"error":"invalid token"}`, contains: []string{"level=WARN", "multica login"}},
		{name: "unauthorized profile", status: http.StatusUnauthorized, body: `{"error":"invalid token"}`, profile: "staging", contains: []string{"--profile staging"}},
		{name: "transient failure", status: http.StatusInternalServerError, body: `{"error":"db down"}`, contains: []string{"token renewal failed"}, notContains: []string{"level=WARN"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, logs := newTokenRenewalDaemon(t, tt.status, tt.body, tt.profile)
			d.tryRenewToken(context.Background())
			for _, want := range tt.contains {
				if !strings.Contains(logs.String(), want) {
					t.Errorf("log missing %q: %s", want, logs.String())
				}
			}
			for _, unwanted := range tt.notContains {
				if strings.Contains(logs.String(), unwanted) {
					t.Errorf("log contains %q: %s", unwanted, logs.String())
				}
			}
		})
	}
}

// Renewal must precede workspace sync so an expired token produces an actionable warning.
func TestPreflightAuth_RenewsBeforeWorkspaceSyncOnExpiredToken(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.URL.Path)
		mu.Unlock()
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid token"}`))
	}))
	t.Cleanup(srv.Close)

	var buf bytes.Buffer
	d := &Daemon{client: NewClient(srv.URL), logger: captureLogger(&buf)}
	d.client.SetToken("mul_already_revoked")

	err := d.preflightAuth(context.Background())
	if err == nil {
		t.Fatal("expected workspace sync to fail with 401")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seen) < 2 {
		t.Fatalf("expected both endpoints to be called; got %v", seen)
	}
	if seen[0] != "/api/tokens/current/renew" {
		t.Fatalf("renew must be the first API call so the WARN fires before the sync 401s; got order %v", seen)
	}
	if seen[1] != "/api/workspaces" {
		t.Fatalf("workspace sync should follow renew; got order %v", seen)
	}
	out := buf.String()
	if !strings.Contains(out, "level=WARN") {
		t.Fatalf("expected re-login WARN, got: %s", out)
	}
	if !strings.Contains(out, "multica login") {
		t.Fatalf("expected the actionable 'run multica login' hint in the WARN, got: %s", out)
	}
}

func TestPreflightAuthBestEffortRenewal(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{name: "not eligible", status: http.StatusOK, body: `{"expires_at":"2099-01-02T03:04:05Z","renewed":false}`},
		{name: "transient failure", status: http.StatusInternalServerError, body: `{"error":"db down"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fx := newPreflightAuthFixture(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			})
			if err := fx.daemon.preflightAuth(context.Background()); err != nil {
				t.Fatalf("preflightAuth must not surface best-effort renewal result: %v", err)
			}
			if !fx.syncCalled.Load() {
				t.Fatal("best-effort renewal must not skip workspace sync")
			}
			if strings.Contains(fx.logs.String(), "level=WARN") {
				t.Fatalf("best-effort renewal must not emit re-login warning: %s", fx.logs.String())
			}
		})
	}
}

func TestTryRenewToken_RespectsContextTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
		_, _ = io.Copy(io.Discard, r.Body)
	}))
	t.Cleanup(srv.Close)

	var buf bytes.Buffer
	d := &Daemon{client: NewClient(srv.URL), logger: captureLogger(&buf)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() {
		d.tryRenewToken(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("tryRenewToken did not return after context cancellation")
	}
}
