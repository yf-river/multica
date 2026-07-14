package handler

import (
	"net/http"
)

type promptEvaluationCandidateCreateFingerprint struct {
	RunID string `json:"run_id"`
}

func writePromptEvaluationCandidateCreateReplayError(w http.ResponseWriter, err error) {
	writeResourceCreateReplayError(
		w, err,
		"Idempotency-Key was already used with a different optimization candidate request",
		"failed to recover optimization candidate request",
	)
}
