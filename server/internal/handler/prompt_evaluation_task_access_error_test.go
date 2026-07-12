package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPromptEvaluationTaskAccessClientCanceledReturns499(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := newRequest(http.MethodPost, "/api/prompt-evaluation-runs/cancel", nil).WithContext(ctx)
	w := httptest.NewRecorder()

	ok := testHandler.canCancelPromptEvaluationTask(
		w,
		req,
		testUserID,
		testWorkspaceID,
		parseUUID(testWorkspaceID),
		parseUUID("11111111-1111-4111-8111-111111111111"),
	)

	if ok {
		t.Fatal("canceled task access lookup unexpectedly succeeded")
	}
	if w.Code != 499 {
		t.Fatalf("canceled task access lookup: expected 499, got %d: %s", w.Code, w.Body.String())
	}
}
