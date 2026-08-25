package main

import (
	"testing"
)

// applyPoolSizing mirrors the env+URL precedence logic in newDBPool but
// without actually opening a connection, so the resolution rules can be
// asserted in unit tests.
func applyPoolSizing(t *testing.T, dbURL string, envMax, envMin string) (max, min int32) {
	t.Helper()
	if envMax != "" {
		t.Setenv("DATABASE_MAX_CONNS", envMax)
	}
	if envMin != "" {
		t.Setenv("DATABASE_MIN_CONNS", envMin)
	}
	cfg, err := dbPoolConfig(dbURL)
	if err != nil {
		t.Fatalf("dbPoolConfig: %v", err)
	}
	return cfg.MaxConns, cfg.MinConns
}

func TestPoolSizing(t *testing.T) {
	baseURL := "postgres://u:p@h/db?sslmode=disable"
	tests := []struct {
		name    string
		dbURL   string
		envMax  string
		envMin  string
		wantMax int32
		wantMin int32
	}{
		{name: "defaults", dbURL: baseURL, wantMax: defaultMaxConns, wantMin: defaultMinConns},
		{name: "URL values", dbURL: baseURL + "&pool_max_conns=40&pool_min_conns=8", wantMax: 40, wantMin: 8},
		{name: "environment overrides URL", dbURL: baseURL + "&pool_max_conns=40&pool_min_conns=8", envMax: "100", envMin: "20", wantMax: 100, wantMin: 20},
		{name: "partial URL", dbURL: baseURL + "&pool_max_conns=40", wantMax: 40, wantMin: defaultMinConns},
		{name: "invalid environment uses default", dbURL: baseURL, envMax: "not-a-number", wantMax: defaultMaxConns, wantMin: defaultMinConns},
		{name: "invalid environment uses URL", dbURL: baseURL + "&pool_max_conns=40", envMax: "not-a-number", wantMax: 40, wantMin: defaultMinConns},
		{name: "minimum clamps to maximum", dbURL: baseURL, envMax: "10", envMin: "50", wantMax: 10, wantMin: 10},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			max, min := applyPoolSizing(t, tt.dbURL, tt.envMax, tt.envMin)
			if max != tt.wantMax || min != tt.wantMin {
				t.Fatalf("pool size = %d/%d, want %d/%d", max, min, tt.wantMax, tt.wantMin)
			}
		})
	}
}
