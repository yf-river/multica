package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGetAttachmentByIDClientCanceledReturns499(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	attachmentID := "11111111-1111-4111-8111-111111111111"
	req := newRequest(http.MethodGet, "/api/attachments/"+attachmentID, nil).WithContext(ctx)
	req = withURLParam(req, "id", attachmentID)
	w := httptest.NewRecorder()

	testHandler.GetAttachmentByID(w, req)

	if w.Code != 499 {
		t.Fatalf("GetAttachmentByID canceled request: expected 499, got %d: %s", w.Code, w.Body.String())
	}
}
