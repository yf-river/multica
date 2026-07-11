package auth

import (
	"strings"
	"testing"
)

func TestValidateJWTConfiguration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		appEnv  string
		secret  string
		wantErr bool
	}{
		{name: "development fallback", secret: "", wantErr: false},
		{name: "production missing", appEnv: "production", secret: "", wantErr: true},
		{name: "production compose placeholder", appEnv: "production", secret: "change-me-in-production", wantErr: true},
		{name: "production auth fallback", appEnv: "production", secret: defaultJWTSecret, wantErr: true},
		{name: "production short", appEnv: "production", secret: "short-secret", wantErr: true},
		{name: "production surrounding whitespace", appEnv: "production", secret: " 0123456789abcdef0123456789abcdef", wantErr: true},
		{name: "production strong", appEnv: "production", secret: "0123456789abcdef0123456789abcdef", wantErr: false},
		{name: "normalized production env", appEnv: " Production ", secret: "0123456789abcdef0123456789abcdef", wantErr: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateJWTConfiguration(tt.appEnv, tt.secret)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateJWTConfiguration() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestDerivePATTokenIsStableAndOperationScoped(t *testing.T) {
	token := DerivePATToken("user-1", "11111111-1111-4111-8111-111111111111")
	if token != DerivePATToken("user-1", "11111111-1111-4111-8111-111111111111") {
		t.Fatal("same user and request key must derive the same PAT")
	}
	if token == DerivePATToken("user-2", "11111111-1111-4111-8111-111111111111") {
		t.Fatal("different users must not share a derived PAT")
	}
	if token == DerivePATToken("user-1", "22222222-2222-4222-8222-222222222222") {
		t.Fatal("different request keys must not share a derived PAT")
	}
	if !strings.HasPrefix(token, "mul_") || len(token) != 44 {
		t.Fatalf("derived PAT format = %q, want mul_ plus 40 hex characters", token)
	}
}
