package handler

import (
	"errors"
	"net/http"
)

type promptEvaluationCandidateCreateFingerprint struct {
	RunID string `json:"run_id"`
}

func writePromptEvaluationCandidateCreateReplayError(w http.ResponseWriter, err error) {
	if errors.Is(err, errResourceCreateIdempotencyConflict) {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "Idempotency-Key was already used with a different optimization candidate request",
			"code":  "idempotency_conflict",
		})
		return
	}
	writeError(w, http.StatusInternalServerError, "failed to recover optimization candidate request")
}
