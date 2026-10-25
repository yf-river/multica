package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestEnvironmentCookiesAndCSRF(t *testing.T) {
	for _, env := range []string{"", "int", "prod", "verify-1", "verify-2", "verify-3"} {
		t.Run(env, func(t *testing.T) {
			t.Setenv("MULTICA_ENV_ID", env)
			if err := ValidateEnvironmentID(); err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			if err := SetAuthCookies(rec, "test-token"); err != nil {
				t.Fatal(err)
			}
			cookies := rec.Result().Cookies()
			want := "multica_auth"
			if env != "" {
				want += "_" + env
			}
			if cookies[0].Name != want || cookies[0].MaxAge != 30*24*3600 {
				t.Fatalf("unexpected auth cookie: %s", cookies[0].Name)
			}
			req := httptest.NewRequest(http.MethodPost, "/api/me", nil)
			req.AddCookie(cookies[0])
			req.Header.Set("X-CSRF-Token", cookies[1].Value)
			if !ValidateCSRF(req) {
				t.Fatal("same environment CSRF rejected")
			}
			if env != "" {
				cookies[0].Name = "multica_auth"
				other := httptest.NewRequest(http.MethodPost, "/api/me", nil)
				other.AddCookie(cookies[0])
				other.Header.Set("X-CSRF-Token", cookies[1].Value)
				if ValidateCSRF(other) {
					t.Fatal("legacy cookie accepted by isolated environment")
				}
			}
			cleared := httptest.NewRecorder()
			ClearAuthCookies(cleared)
			for _, c := range cleared.Result().Cookies() {
				if c.Name != CookieName(AuthCookieName) && c.Name != CookieName(CSRFCookieName) && c.Name != CookieName(SessionCSRFCookieName) {
					t.Fatal("deleted another environment cookie")
				}
				if c.MaxAge != -1 {
					t.Fatal("cookie not expired")
				}
			}
		})
	}
	t.Setenv("MULTICA_ENV_ID", "bad;cookie")
	if ValidateEnvironmentID() == nil {
		t.Fatal("invalid environment accepted")
	}
}

func TestEnvironmentSessionRenewalKeepsCookieIsolation(t *testing.T) {
	for _, env := range []string{"int", "prod", "verify-1", "verify-2", "verify-3"} {
		t.Run(env, func(t *testing.T) {
			t.Setenv("MULTICA_ENV_ID", env)
			claims := sessionClaims(t, time.Now().Add(AuthRenewThreshold()/2), "same-login")
			claims["account"] = "river"
			original := signSession(t, claims)
			renewed, _, err := RenewSessionToken(claims)
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			if err := SetAuthCookies(rec, renewed); err != nil {
				t.Fatal(err)
			}
			var authCookie *http.Cookie
			var sessionCSRF string
			for _, c := range rec.Result().Cookies() {
				if c.Name == CookieName(AuthCookieName) {
					authCookie = c
				}
				if c.Name == CookieName(SessionCSRFCookieName) {
					sessionCSRF = c.Value
				}
			}
			if authCookie == nil || sessionCSRF == "" {
				t.Fatal("missing isolated session cookies")
			}
			req := httptest.NewRequest(http.MethodPost, "/api/me", nil)
			req.AddCookie(&http.Cookie{Name: CookieName(AuthCookieName), Value: original})
			req.Header.Set(CSRFHeaderName, sessionCSRF)
			if !ValidateCSRF(req) {
				t.Fatal("session-bound CSRF rejected across renewal")
			}
			req.AddCookie(authCookie)
			other := httptest.NewRequest(http.MethodPost, "/api/me", nil)
			other.AddCookie(&http.Cookie{Name: AuthCookieName, Value: renewed})
			other.Header.Set(CSRFHeaderName, sessionCSRF)
			if ValidateCSRF(other) {
				t.Fatal("another environment accepted unscoped cookie")
			}
		})
	}
}
