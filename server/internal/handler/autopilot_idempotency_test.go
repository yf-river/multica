package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTriggerAutopilotRequiresIdempotencyKey(t *testing.T) {
	h := &Handler{}
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/autopilots/test/trigger", nil)

	h.TriggerAutopilot(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
	}
	if body := w.Body.String(); body != "{\"code\":\"idempotency_key_required\",\"error\":\"Idempotency-Key header is required\"}\n" {
		t.Fatalf("body = %q", body)
	}
}
