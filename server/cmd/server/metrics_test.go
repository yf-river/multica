package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/handler"
	"github.com/multica-ai/multica/server/internal/realtime"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestMainRouterDoesNotExposePrometheusMetrics(t *testing.T) {
	router, _, err := NewRouterWithOptions(nil, realtime.NewHub(), events.New(), analytics.NoopClient{}, nil, RouterOptions{
		HeartbeatScheduler: handler.NewPassthroughHeartbeatScheduler(db.New(nil)),
	})
	if err != nil {
		t.Fatalf("construct router: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("main API /metrics status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}
