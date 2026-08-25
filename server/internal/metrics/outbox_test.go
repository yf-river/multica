package metrics

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

func TestOutboxCollectorExposesOperationalState(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "postgres://disabled@127.0.0.1:1/none?sslmode=disable")
	if err != nil {
		t.Fatalf("create dummy pool: %v", err)
	}
	t.Cleanup(pool.Close)
	collector := NewOutboxCollector(pool)
	collector.query = func(context.Context) (outboxSnapshot, error) {
		return outboxSnapshot{pending: 7, retrying: 2, deadLettered: 1, oldestPendingAge: 42}, nil
	}

	registry := prometheus.NewRegistry()
	registry.MustRegister(collector)
	recorder := httptest.NewRecorder()
	NewHandler(registry).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := recorder.Body.String()
	for _, want := range []string{
		"multica_domain_event_outbox_pending 7",
		"multica_domain_event_outbox_retrying 2",
		"multica_domain_event_outbox_dead_lettered 1",
		"multica_domain_event_outbox_oldest_pending_age_seconds 42",
		"multica_domain_event_outbox_query_errors_total 0",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics body missing %q\n%s", want, body)
		}
	}
}

func TestOutboxCollectorKeepsMetricsAvailableWhenRefreshFails(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "postgres://disabled@127.0.0.1:1/none?sslmode=disable")
	if err != nil {
		t.Fatalf("create dummy pool: %v", err)
	}
	t.Cleanup(pool.Close)
	collector := NewOutboxCollector(pool)
	now := time.Unix(100, 0)
	collector.now = func() time.Time { return now }
	collector.query = func(context.Context) (outboxSnapshot, error) {
		return outboxSnapshot{pending: 3}, nil
	}

	registry := prometheus.NewRegistry()
	registry.MustRegister(collector)
	gatherMetrics(t, registry)

	now = now.Add(collector.cacheTTL)
	collector.query = func(context.Context) (outboxSnapshot, error) {
		return outboxSnapshot{}, errors.New("database unavailable")
	}
	body := gatherMetrics(t, registry)
	for _, want := range []string{
		"multica_domain_event_outbox_pending 3",
		"multica_domain_event_outbox_query_errors_total 1",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics body missing %q\n%s", want, body)
		}
	}
}

func TestOutboxCollectorDoesNotFailScrapeBeforeFirstSuccessfulRead(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "postgres://disabled@127.0.0.1:1/none?sslmode=disable")
	if err != nil {
		t.Fatalf("create dummy pool: %v", err)
	}
	t.Cleanup(pool.Close)
	collector := NewOutboxCollector(pool)
	collector.query = func(context.Context) (outboxSnapshot, error) {
		return outboxSnapshot{}, errors.New("database unavailable")
	}

	registry := prometheus.NewRegistry()
	registry.MustRegister(collector)
	body := gatherMetrics(t, registry)
	if strings.Contains(body, "multica_domain_event_outbox_pending ") {
		t.Fatalf("pending gauge must be absent without a successful snapshot\n%s", body)
	}
	if !strings.Contains(body, "multica_domain_event_outbox_query_errors_total 1") {
		t.Fatalf("metrics body missing query failure counter\n%s", body)
	}
}

func TestOutboxCollectorReadsLivePostgres(t *testing.T) {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		t.Skip("DATABASE_URL not set; skipping live-Postgres outbox metrics test")
	}
	pool, err := pgxpool.New(context.Background(), dbURL)
	if err != nil {
		t.Fatalf("create live pool: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(context.Background()); err != nil {
		t.Fatalf("ping live database: %v", err)
	}

	collector := NewOutboxCollector(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	snapshot, err := collector.queryDatabase(ctx)
	if err != nil {
		t.Fatalf("query live outbox state: %v", err)
	}
	for name, value := range map[string]float64{
		"pending":            snapshot.pending,
		"retrying":           snapshot.retrying,
		"dead_lettered":      snapshot.deadLettered,
		"oldest_pending_age": snapshot.oldestPendingAge,
	} {
		if value < 0 {
			t.Fatalf("%s = %v, want non-negative", name, value)
		}
	}
}

func gatherMetrics(t *testing.T, registry *prometheus.Registry) string {
	t.Helper()
	recorder := httptest.NewRecorder()
	NewHandler(registry).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /metrics status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	return recorder.Body.String()
}
