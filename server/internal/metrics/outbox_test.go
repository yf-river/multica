package metrics

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

func newOutboxTestCollector(t *testing.T) *OutboxCollector {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), "postgres://disabled@127.0.0.1:1/none?sslmode=disable")
	if err != nil {
		t.Fatalf("create dummy pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return NewOutboxCollector(pool)
}

func TestOutboxCollectorExposesOperationalState(t *testing.T) {
	collector := newOutboxTestCollector(t)
	collector.query = func(context.Context) (outboxSnapshot, error) {
		return outboxSnapshot{pending: 7, retrying: 2, deadLettered: 1, oldestPendingAge: 42}, nil
	}
	body := gatherOutboxMetrics(t, collector)
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

func TestOutboxCollectorRetainsLastGoodSnapshotOnReadFailure(t *testing.T) {
	collector := newOutboxTestCollector(t)
	now := time.Unix(100, 0)
	collector.now = func() time.Time { return now }
	collector.query = func(context.Context) (outboxSnapshot, error) { return outboxSnapshot{pending: 3}, nil }
	registry := prometheus.NewRegistry()
	registry.MustRegister(collector)
	_ = gatherOutboxMetricsFromRegistry(t, registry)
	now = now.Add(collector.cacheTTL)
	collector.query = func(context.Context) (outboxSnapshot, error) {
		return outboxSnapshot{}, errors.New("database unavailable")
	}
	body := gatherOutboxMetricsFromRegistry(t, registry)
	if !strings.Contains(body, "multica_domain_event_outbox_pending 3") || !strings.Contains(body, "multica_domain_event_outbox_query_errors_total 1") {
		t.Fatalf("stale snapshot/error counter missing\n%s", body)
	}
}

func gatherOutboxMetrics(t *testing.T, collector *OutboxCollector) string {
	t.Helper()
	registry := prometheus.NewRegistry()
	registry.MustRegister(collector)
	return gatherOutboxMetricsFromRegistry(t, registry)
}

func gatherOutboxMetricsFromRegistry(t *testing.T, registry *prometheus.Registry) string {
	t.Helper()
	recorder := httptest.NewRecorder()
	NewHandler(registry).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /metrics status = %d: %s", recorder.Code, recorder.Body.String())
	}
	return recorder.Body.String()
}
