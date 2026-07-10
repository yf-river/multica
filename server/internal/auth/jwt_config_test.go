package auth

import "testing"

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
