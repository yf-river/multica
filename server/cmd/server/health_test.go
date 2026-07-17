package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

type stubReadinessDB struct {
	pingErr      error
	queryErr     error
	appliedCount int
	pingCalls    atomic.Int32
	queryCalls   atomic.Int32
}

func (s *stubReadinessDB) Ping(context.Context) error {
	s.pingCalls.Add(1)
	return s.pingErr
}

func (s *stubReadinessDB) QueryRow(context.Context, string, ...any) pgx.Row {
	s.queryCalls.Add(1)
	return stubRow{appliedCount: s.appliedCount, err: s.queryErr}
}

type stubRow struct {
	appliedCount int
	err          error
}

func (r stubRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	*(dest[0].(*int)) = r.appliedCount
	return nil
}

func readReadyResponse(t *testing.T, h *serverHealth, wantStatus int) readinessResponse {
	t.Helper()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	h.readyHandler(rec, req)

	if rec.Code != wantStatus {
		t.Fatalf("expected status %d, got %d", wantStatus, rec.Code)
	}

	var resp readinessResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return resp
}

func TestServerHealthReadyHandlerFailures(t *testing.T) {
	tests := []struct {
		name               string
		db                 *stubReadinessDB
		requiredMigrations []string
		wantDB             string
		wantMigrations     string
	}{
		{name: "database unavailable", db: &stubReadinessDB{pingErr: errors.New("db unavailable")}, requiredMigrations: []string{"056_example"}, wantDB: "error", wantMigrations: "unknown"},
		{name: "migration missing", db: &stubReadinessDB{}, requiredMigrations: []string{"056_example"}, wantDB: "ok", wantMigrations: "out_of_date"},
		{name: "migration partially applied", db: &stubReadinessDB{appliedCount: 2}, requiredMigrations: []string{"120_a", "120_b", "121_c"}, wantDB: "ok", wantMigrations: "out_of_date"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &serverHealth{db: tt.db, requiredMigrations: tt.requiredMigrations}
			resp := readReadyResponse(t, h, http.StatusServiceUnavailable)
			if resp.Status != "not_ready" || resp.Checks.DB != tt.wantDB || resp.Checks.Migrations != tt.wantMigrations {
				t.Fatalf("readiness = %+v, want status=not_ready db=%s migrations=%s", resp, tt.wantDB, tt.wantMigrations)
			}
		})
	}
}

func TestServerHealthReadinessCachesResult(t *testing.T) {
	db := &stubReadinessDB{appliedCount: 1}
	h := &serverHealth{
		db:                 db,
		requiredMigrations: []string{"056_example"},
		cacheTTL:           time.Minute,
	}

	resp1, status1 := h.readiness(context.Background())
	resp2, status2 := h.readiness(context.Background())

	if status1 != http.StatusOK || status2 != http.StatusOK {
		t.Fatalf("expected cached readiness status 200, got %d and %d", status1, status2)
	}
	if resp1.Status != "ok" || resp2.Status != "ok" {
		t.Fatalf("expected cached readiness status ok, got %q and %q", resp1.Status, resp2.Status)
	}
	if got := db.pingCalls.Load(); got != 1 {
		t.Fatalf("Ping calls = %d, want 1", got)
	}
	if got := db.queryCalls.Load(); got != 1 {
		t.Fatalf("QueryRow calls = %d, want 1", got)
	}
}
