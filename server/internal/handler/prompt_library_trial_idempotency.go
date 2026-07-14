package handler

import (
	"errors"
	"net/http"
)

func writePromptLibraryTrialReplayError(w http.ResponseWriter, err error) {
	if errors.Is(err, errResourceCreateIdempotencyConflict) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "Idempotency-Key was already used with a different prompt trial request", "code": "idempotency_conflict"})
		return
	}
	writeError(w, http.StatusInternalServerError, "failed to recover prompt library trial request")
}
