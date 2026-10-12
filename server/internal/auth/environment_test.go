package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEnvironmentCookiesAndCSRF(t *testing.T) {
	for _, env := range []string{"", "int", "prod"} {
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
				if c.Name != CookieName(AuthCookieName) && c.Name != CookieName(CSRFCookieName) {
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
