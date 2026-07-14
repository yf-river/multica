package handler

import (
	"errors"
	"net/http"
)

type promptEvaluationCandidatePublishFingerprint struct {
	CandidateID string `json:"candidate_id"`
}

type promptEvaluationCandidateRejectFingerprint struct {
	CandidateID string `json:"candidate_id"`
	Reason      string `json:"reason"`
}

func writePromptEvaluationCandidatePublishReplayError(w http.ResponseWriter, err error) {
	if errors.Is(err, errResourceCreateIdempotencyConflict) {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "Idempotency-Key was already used with a different optimization candidate publish request",
			"code":  "idempotency_conflict",
		})
		return
	}
	writeError(w, http.StatusInternalServerError, "failed to recover optimization candidate publish request")
}

func writePromptEvaluationCandidateRejectReplayError(w http.ResponseWriter, err error) {
	if errors.Is(err, errResourceCreateIdempotencyConflict) {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "Idempotency-Key was already used with a different optimization candidate reject request",
			"code":  "idempotency_conflict",
		})
		return
	}
	writeError(w, http.StatusInternalServerError, "failed to recover optimization candidate reject request")
}
