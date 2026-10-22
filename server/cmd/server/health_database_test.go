package main

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http"
	"net/url"
	"os"
	"testing"
	"time"
)

func TestServerHealthChecksSnapshotAndLaterMigrationsInRealDatabase(t *testing.T) {
	raw := os.Getenv("READINESS_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("requires explicit isolated readiness database")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() != "127.0.0.1" || u.Path != "/verification_full_test" {
		t.Fatal("readiness test requires isolated verification_full_test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	h := newServerHealth(pool)
	h.cacheTTL = 0
	if response, status := h.readiness(ctx); status != http.StatusOK {
		t.Fatalf("snapshot plus deltas not ready: %d %+v", status, response)
	}
	const version = "573_verification_slot_delivery"
	var applied time.Time
	if err = pool.QueryRow(ctx, `SELECT applied_at FROM schema_migrations WHERE version=$1`, version).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	// 仅在本次新建的空白验证库模拟缺少后续版本，验证 000 不会掩盖增量缺失。
	if _, err = pool.Exec(ctx, `DELETE FROM schema_migrations WHERE version=$1`, version); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := pool.Exec(ctx, `INSERT INTO schema_migrations(version,applied_at) VALUES($1,$2)`, version, applied); err != nil {
			t.Error(err)
		}
	}()
	if response, status := h.readiness(ctx); status != http.StatusServiceUnavailable || response.Checks.Migrations != "out_of_date" {
		t.Fatalf("missing post-snapshot migration accepted: %d %+v", status, response)
	}
}
