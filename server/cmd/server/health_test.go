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
	pingErr    error
	queryErr   error
	matches    bool
	version    int
	hash       string
	pingCalls  atomic.Int32
	queryCalls atomic.Int32
}

func (s *stubReadinessDB) Ping(context.Context) error {
	s.pingCalls.Add(1)
	return s.pingErr
}

func (s *stubReadinessDB) QueryRow(context.Context, string, ...any) pgx.Row {
	s.queryCalls.Add(1)
	return stubRow{matches: s.matches, version: s.version, hash: s.hash, err: s.queryErr}
}

type stubRow struct {
	matches bool
	version int
	hash    string
	err     error
}

func (r stubRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	*(dest[0].(*bool)) = r.matches
	*(dest[1].(*int)) = r.version
	*(dest[2].(*string)) = r.hash
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

func TestServerHealthReadyHandlerDBPingFailure(t *testing.T) {
	db := &stubReadinessDB{pingErr: errors.New("db unavailable")}
	h := &serverHealth{db: db}

	resp := readReadyResponse(t, h, http.StatusServiceUnavailable)
	if resp.Status != "not_ready" {
		t.Fatalf("status = %q, want %q", resp.Status, "not_ready")
	}
	if resp.Checks.DB != "error" {
		t.Fatalf("db check = %q, want %q", resp.Checks.DB, "error")
	}
	if resp.Checks.Schema != "unknown" {
		t.Fatalf("schema check = %q, want %q", resp.Checks.Schema, "unknown")
	}
}

func TestServerHealthReadyHandlerSchemaMismatch(t *testing.T) {
	db := &stubReadinessDB{hash: "wrong"}
	h := &serverHealth{db: db}

	resp := readReadyResponse(t, h, http.StatusServiceUnavailable)
	if resp.Status != "not_ready" {
		t.Fatalf("status = %q, want %q", resp.Status, "not_ready")
	}
	if resp.Checks.DB != "ok" {
		t.Fatalf("db check = %q, want %q", resp.Checks.DB, "ok")
	}
	if resp.Checks.Schema != "error" {
		t.Fatalf("schema check = %q, want %q", resp.Checks.Schema, "error")
	}
}

func TestServerHealthReadyHandlerSchemaQueryFailure(t *testing.T) {
	db := &stubReadinessDB{queryErr: errors.New("schema marker unavailable")}
	h := &serverHealth{db: db}

	resp := readReadyResponse(t, h, http.StatusServiceUnavailable)
	if resp.Status != "not_ready" {
		t.Fatalf("status = %q, want %q", resp.Status, "not_ready")
	}
	if resp.Checks.Schema != "error" {
		t.Fatalf("schema check = %q, want %q", resp.Checks.Schema, "error")
	}
}

func TestServerHealthReadinessCachesResult(t *testing.T) {
	db := &stubReadinessDB{matches: true}
	h := &serverHealth{
		db:       db,
		cacheTTL: time.Minute,
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
