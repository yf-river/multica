package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/redis/go-redis/v9"
)

func newRedisTestClient(t *testing.T) *redis.Client {
	t.Helper()
	return testutil.NewRedisTestClient(t, testutil.RedisDBMiddleware)
}

func generateToken(claims jwt.MapClaims, secret []byte) string {
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	s, _ := token.SignedString(secret)
	return s
}

func validClaims() jwt.MapClaims {
	return jwt.MapClaims{
		"sub":     "test-user-id",
		"account": "testuser",
		"exp":     time.Now().Add(time.Hour).Unix(),
	}
}

func authMiddleware(next http.Handler) http.Handler {
	return Auth(nil, nil, nil)(next)
}

func TestAuthRejectsInvalidCredentials(t *testing.T) {
	missingBody := `{"error":"missing authorization"}` + "\n"
	for _, testCase := range []struct {
		name          string
		authorization func() string
		wantBody      string
	}{
		{name: "missing", authorization: func() string { return "" }, wantBody: missingBody},
		{name: "non bearer", authorization: func() string { return "Token some-token" }, wantBody: missingBody},
		{name: "malformed JWT", authorization: func() string { return "Bearer not-a-valid-jwt" }},
		{name: "expired JWT", authorization: func() string {
			claims := validClaims()
			claims["exp"] = time.Now().Add(-time.Hour).Unix()
			return "Bearer " + generateToken(claims, auth.JWTSecret())
		}},
		{name: "wrong JWT secret", authorization: func() string {
			return "Bearer " + generateToken(validClaims(), []byte("wrong-secret"))
		}},
		{name: "unsupported JWT signing method", authorization: func() string {
			token := jwt.NewWithClaims(jwt.SigningMethodNone, validClaims())
			signed, _ := token.SignedString(jwt.UnsafeAllowNoneSignatureType)
			return "Bearer " + signed
		}},
		{name: "missing JWT claims", authorization: func() string {
			return "Bearer " + generateToken(jwt.MapClaims{"exp": time.Now().Add(time.Hour).Unix()}, auth.JWTSecret())
		}},
		{name: "invalid PAT", authorization: func() string { return "Bearer mul_invalid_token_here" }},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			handler := authMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Fatal("next handler should not be called")
			}))
			req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
			if authorization := testCase.authorization(); authorization != "" {
				req.Header.Set("Authorization", authorization)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", w.Code)
			}
			if testCase.wantBody != "" && w.Body.String() != testCase.wantBody {
				t.Fatalf("body = %q, want %q", w.Body.String(), testCase.wantBody)
			}
		})
	}
}

func TestAuth_ValidToken(t *testing.T) {
	var gotUserID, gotAccount string
	handler := authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUserID = r.Header.Get("X-User-ID")
		gotAccount = r.Header.Get("X-User-Account")
		w.WriteHeader(http.StatusOK)
	}))

	token := generateToken(validClaims(), auth.JWTSecret())

	req := httptest.NewRequest("GET", "/api/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if gotUserID != "test-user-id" {
		t.Fatalf("expected X-User-ID 'test-user-id', got '%s'", gotUserID)
	}
	if gotAccount != "testuser" {
		t.Fatalf("expected X-User-Account 'testuser', got '%s'", gotAccount)
	}
}

func TestAuth_StripsClientSuppliedActorSource(t *testing.T) {
	var gotActorSource string
	mw := Auth(nil, nil, nil)
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotActorSource = r.Header.Get("X-Actor-Source")
		w.WriteHeader(http.StatusOK)
	}))

	token := generateToken(validClaims(), auth.JWTSecret())
	req := httptest.NewRequest("GET", "/api/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	// Client tries to forge the actor-source header. The middleware must
	// discard it before the JWT branch runs (which doesn't set it again
	// for human sessions).
	req.Header.Set("X-Actor-Source", "task_token")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if gotActorSource != "" {
		t.Fatalf("X-Actor-Source must be cleared on non-task-token paths, got %q", gotActorSource)
	}
}

func TestAuth_PATCacheHit(t *testing.T) {
	rdb := newRedisTestClient(t)
	cache := auth.NewPATCache(rdb)
	if cache == nil {
		t.Fatal("expected non-nil cache")
	}

	const rawToken = "mul_cache_hit_test_token"
	hash := auth.HashToken(rawToken)
	cache.Set(context.Background(), hash, "cached-user-id", auth.AuthCacheTTL)

	var gotUserID string
	mw := Auth(nil, cache, nil) // nil queries — only safe on cache hit
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUserID = r.Header.Get("X-User-ID")
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/api/me", nil)
	req.Header.Set("Authorization", "Bearer "+rawToken)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on cache hit, got %d", w.Code)
	}
	if gotUserID != "cached-user-id" {
		t.Fatalf("expected cached X-User-ID, got %q", gotUserID)
	}
}

func TestAuth_MCN_NoVerifierConfigured(t *testing.T) {
	mw := Auth(nil, nil, nil)
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next must not be called when verifier is unconfigured")
	}))
	req := httptest.NewRequest("GET", "/api/me", nil)
	req.Header.Set("Authorization", "Bearer mcn_anything")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestAuth_MCN_ValidTokenSetsUserID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"valid": true,
			"owner_id": "01972f7e-7e8d-77ef-a13d-1b0ce3e9c001"
		}`))
	}))
	defer srv.Close()

	verifier := auth.NewCloudPATVerifier(srv.URL, nil)

	var gotUser, gotActorSource string
	mw := Auth(nil, nil, verifier)
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser = r.Header.Get("X-User-ID")
		gotActorSource = r.Header.Get("X-Actor-Source")
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/api/me", nil)
	req.Header.Set("Authorization", "Bearer mcn_x")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if gotUser != "01972f7e-7e8d-77ef-a13d-1b0ce3e9c001" {
		t.Errorf("expected owner_id propagated as X-User-ID, got %q", gotUser)
	}
	// A successful mcn_ verify must stamp X-Actor-Source so downstream
	// handlers can tell a machine credential apart from a human PAT/JWT.
	if gotActorSource != "cloud_pat" {
		t.Errorf("expected X-Actor-Source=cloud_pat, got %q", gotActorSource)
	}
}

func TestAuth_MCN_InvalidReturns401(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"valid":false,"reason":"token_revoked"}`))
	}))
	defer srv.Close()

	verifier := auth.NewCloudPATVerifier(srv.URL, nil)
	mw := Auth(nil, nil, verifier)
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next must not be called when token is invalid")
	}))

	req := httptest.NewRequest("GET", "/api/me", nil)
	req.Header.Set("Authorization", "Bearer mcn_revoked")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestAuth_MCN_FleetUnreachableReturns503(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	verifier := auth.NewCloudPATVerifier(srv.URL, nil)
	mw := Auth(nil, nil, verifier)
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next must not be called when fleet is unavailable")
	}))

	req := httptest.NewRequest("GET", "/api/me", nil)
	req.Header.Set("Authorization", "Bearer mcn_x")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", w.Code)
	}
}
