package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestWriteValidationLookupErrorPreservesFailureClass(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name       string
		err        error
		wantStatus int
		wantBody   string
	}{
		{name: "missing relation", err: pgx.ErrNoRows, wantStatus: http.StatusBadRequest, wantBody: "prompt_id does not belong to this workspace"},
		{name: "cancelled request", err: context.Canceled, wantStatus: 499, wantBody: "request cancelled"},
		{name: "database failure", err: errors.New("database unavailable"), wantStatus: http.StatusInternalServerError, wantBody: "failed to validate prompt"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPost, "/api/prompt-evaluation-assets/run", nil)

			writeValidationLookupError(
				w,
				r,
				test.err,
				"prompt_id does not belong to this workspace",
				"prompt",
				"prompt_id", "prompt-1",
			)

			if w.Code != test.wantStatus {
				t.Fatalf("status: got %d, want %d: %s", w.Code, test.wantStatus, w.Body.String())
			}
			if !containsJSONError(w.Body.String(), test.wantBody) {
				t.Fatalf("body: got %q, want error %q", w.Body.String(), test.wantBody)
			}
		})
	}
}

func containsJSONError(body, message string) bool {
	return body == "{\"error\":\""+message+"\"}\n"
}
