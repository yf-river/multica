package supervision

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 1. Exercise transport budgets and provider failures without contacting live accounts.
func TestLensRequestFailurePolicy(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		body     string
		requests int
		kind     string
	}{
		{"success", 200, `{"code":0,"data":{"trace_detail":{"trace_id":"t"}}}`, 1, ""},
		{"auth", 200, `{"code":220002,"msg":"private-token","data":{}}`, 1, "provider_error_220002"},
		{"permission", 200, `{"code":-1,"msg":"private account"}`, 1, "provider_error_-1"},
		{"parameter", 200, `{"code":"ErrParam"}`, 1, "provider_error_ErrParam"},
		{"rate_limit", 429, `private-token`, 3, "http_429"},
		{"backend", 503, `private-token`, 3, "http_503"},
		{"fallback", 200, `{"code":0,"data":{"fallback_text":"private spaces"}}`, 1, "space_unavailable"},
		{"malformed", 200, `{"code":0,"data":[]}`, 1, "unknown_response_format"},
		{"redirect", 302, ``, 1, "http_302"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Header.Get("X-Agentlens-Token") != "private-token" {
					t.Error("missing controlled credential")
				}
				if r.URL.Path != "/traceDetail" {
					t.Errorf("wrong method %s", r.URL.Path)
				}
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			client := agentLensClient{Scope: AgentLensScope{Workspace: "w", Token: "private-token"}, endpoint: server.URL + "/"}
			budget := lensBudget{}
			_, err := client.query(context.Background(), &budget, "traceDetail", map[string]any{"trace_id": "t"})
			if calls != tc.requests || budget.Requests != tc.requests {
				t.Fatalf("calls=%d budget=%d", calls, budget.Requests)
			}
			if tc.kind == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.kind) || strings.Contains(err.Error(), "private") {
				t.Fatalf("unsafe or unexpected diagnostic %v", err)
			}
		})
	}
}

// 2. Retries count against the same preparation budget; cancellation never starts a request.
func TestLensBudgetAndCancellation(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(503) }))
	defer server.Close()
	client := agentLensClient{Scope: AgentLensScope{Token: "test"}, endpoint: server.URL + "/"}
	for _, budget := range []lensBudget{{Requests: 40}, {Started: time.Now().Add(-121 * time.Second)}} {
		if _, err := client.query(context.Background(), &budget, "traceDetail", nil); err == nil {
			t.Fatal("exhausted budget accepted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.query(ctx, &lensBudget{}, "traceDetail", nil); err == nil {
		t.Fatal("cancelled request accepted")
	}
	if calls != 0 {
		t.Fatal("exhausted call reached network")
	}
	budget := lensBudget{Requests: 39}
	if _, err := client.query(context.Background(), &budget, "traceDetail", nil); err == nil {
		t.Fatal("503 accepted")
	}
	if calls != 1 || budget.Requests != 40 {
		t.Fatalf("retry exceeded budget: %d/%d", calls, budget.Requests)
	}
	if _, err := client.query(context.Background(), &lensBudget{}, "http://untrusted/", nil); err == nil {
		t.Fatal("arbitrary path accepted")
	}
}

// 3. Scoped credentials rotate by rereading the private file and cannot be reused across workspaces.
func TestLensCredentialScope(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credential.json")
	if err := os.WriteFile(path, []byte(`{"workspace_id":"owned","token":"first"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadAgentLensScope(path, "foreign"); err == nil {
		t.Fatal("cross-workspace credential accepted")
	}
	scope, err := loadAgentLensScope(path, "owned")
	if err != nil || scope.Token != "first" {
		t.Fatalf("load: %v", err)
	}
	if err = os.WriteFile(path, []byte(`{"workspace_id":"owned","token":"rotated"}`), 0600); err != nil {
		t.Fatal(err)
	}
	scope, err = loadAgentLensScope(path, "owned")
	if err != nil || scope.Token != "rotated" {
		t.Fatalf("rotation: %v", err)
	}
	if err = os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err = loadAgentLensScope(path, "owned"); err == nil {
		t.Fatal("world-readable credential accepted")
	}
}

// 4. Root identity and secret removal are independent of token magnitude or timestamp proximity.
func TestLensOwnershipAndRedaction(t *testing.T) {
	clean := sanitizeTraceJSON([]byte(`{"Authorization":"sensitive","input_tokens":99,"attributes":[{"password":"sensitive","message":"credential-value"}]}`), "credential-value")
	if strings.Contains(clean, "sensitive") || strings.Contains(clean, "credential-value") || !strings.Contains(clean, `"input_tokens":99`) {
		t.Fatalf("redaction failed: %s", clean)
	}
}

// 5. Provider retry delays cannot be shortened or reset the preparation deadline.
func TestLensRetryAfter(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	for _, tc := range []struct {
		value string
		want  time.Duration
	}{
		{"2", 2 * time.Second},
		{now.Add(3 * time.Second).Format(http.TimeFormat), 3 * time.Second},
		{"-1", 0}, {"invalid", 0},
		{now.Add(-time.Second).Format(http.TimeFormat), 0},
	} {
		if got := lensRetryAfter(tc.value, now); got != tc.want {
			t.Fatalf("%q: %s, want %s", tc.value, got, tc.want)
		}
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Retry-After", "121")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()
	client := agentLensClient{Scope: AgentLensScope{Token: "test"}, endpoint: server.URL + "/"}
	budget := lensBudget{}
	_, err := client.query(context.Background(), &budget, "traceDetail", nil)
	if err == nil || !strings.Contains(err.Error(), "budget_exhausted") || calls != 1 || budget.Requests != 1 {
		t.Fatalf("backoff exceeded budget: calls=%d budget=%d err=%v", calls, budget.Requests, err)
	}
}

// 6. A valid Retry-After must actually delay a subsequent request, within the shared cap.
func TestLensRetryAfterWait(t *testing.T) {
	var previous time.Time
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			previous = time.Now()
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(429)
			return
		}
		if time.Since(previous) < time.Second {
			t.Error("provider backoff shortened")
		}
		fmt.Fprint(w, `{"code":0,"data":{"trace_detail":{"trace_id":"t"}}}`)
	}))
	defer server.Close()
	client := agentLensClient{Scope: AgentLensScope{Token: "test"}, endpoint: server.URL + "/"}
	budget := lensBudget{}
	if _, err := client.query(context.Background(), &budget, "traceDetail", nil); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || budget.Requests != 2 {
		t.Fatalf("calls=%d budget=%d", calls, budget.Requests)
	}
}
