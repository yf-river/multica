package main

import "testing"

func TestAllowedOriginsIncludesConfiguredLoopbackFrontendPort(t *testing.T) {
	t.Setenv("FRONTEND_PORT", "13680")
	t.Setenv("FRONTEND_ORIGIN", "")
	t.Setenv("CORS_ALLOWED_ORIGINS", "")

	origins := allowedOrigins()
	for _, want := range []string{"http://localhost:13680", "http://127.0.0.1:13680"} {
		if !containsString(origins, want) {
			t.Fatalf("allowedOrigins() missing %s: %#v", want, origins)
		}
	}
}

func TestAllowedOriginsExtendsExplicitOriginsWithLoopbackFrontendPort(t *testing.T) {
	t.Setenv("FRONTEND_PORT", "13680")
	t.Setenv("CORS_ALLOWED_ORIGINS", "http://9.134.129.162:13680")

	origins := allowedOrigins()
	for _, want := range []string{
		"http://9.134.129.162:13680",
		"http://localhost:13680",
		"http://127.0.0.1:13680",
	} {
		if !containsString(origins, want) {
			t.Fatalf("allowedOrigins() missing %s: %#v", want, origins)
		}
	}
}

func TestCloudFleetURLUsesCanonicalEnvironmentName(t *testing.T) {
	t.Setenv("MULTICA_CLOUD_FLEET_URL", "https://fleet.example.test")

	if got := cloudFleetURLFromEnv(); got != "https://fleet.example.test" {
		t.Fatalf("cloudFleetURLFromEnv() = %q", got)
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
