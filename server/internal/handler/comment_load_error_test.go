package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDeleteCommentClientCanceledReturns499(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := newRequest(http.MethodDelete, "/api/comments/11111111-1111-4111-8111-111111111111", nil).WithContext(ctx)
	req = withURLParam(req, "commentId", "11111111-1111-4111-8111-111111111111")
	w := httptest.NewRecorder()

	testHandler.DeleteComment(w, req)

	if w.Code != 499 {
		t.Fatalf("DeleteComment canceled request: expected 499, got %d: %s", w.Code, w.Body.String())
	}
}
