package main

import "testing"

func TestAllowedOriginsIncludesConfiguredFrontendPort(t *testing.T) {
	for _, explicit := range []string{"", "http://9.134.129.162:13680"} {
		t.Run(explicit, func(t *testing.T) {
			t.Setenv("FRONTEND_PORT", "13680")
			t.Setenv("FRONTEND_ORIGIN", "")
			t.Setenv("CORS_ALLOWED_ORIGINS", explicit)

			origins := allowedOrigins()
			want := []string{"http://localhost:13680", "http://127.0.0.1:13680"}
			if explicit != "" {
				want = append(want, explicit)
			}
			for _, origin := range want {
				if !containsString(origins, origin) {
					t.Fatalf("allowedOrigins() missing %s: %#v", origin, origins)
				}
			}
		})
	}
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
