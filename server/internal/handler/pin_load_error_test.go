package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCreatePinClientCanceledReturns499(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	for _, itemType := range []string{"issue", "project"} {
		t.Run(itemType, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			req := newRequest(http.MethodPost, "/api/pins", map[string]any{
				"item_type": itemType,
				"item_id":   "11111111-1111-4111-8111-111111111111",
			}).WithContext(ctx)
			w := httptest.NewRecorder()

			testHandler.CreatePin(w, req)

			if w.Code != 499 {
				t.Fatalf("CreatePin %s canceled request: expected 499, got %d: %s", itemType, w.Code, w.Body.String())
			}
		})
	}
}
