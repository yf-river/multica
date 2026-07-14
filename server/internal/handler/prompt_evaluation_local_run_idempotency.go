package handler

import (
	"errors"
	"net/http"
)

type promptEvaluationLocalRunFingerprint struct {
	Operation   string `json:"operation"`
	AssetID     string `json:"asset_id,omitempty"`
	CandidateID string `json:"candidate_id,omitempty"`
}

func writePromptEvaluationLocalRunReplayError(w http.ResponseWriter, err error) {
	if errors.Is(err, errResourceCreateIdempotencyConflict) {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "Idempotency-Key was already used with a different prompt evaluation local run",
			"code":  "idempotency_conflict",
		})
		return
	}
	writeError(w, http.StatusInternalServerError, "failed to recover prompt evaluation local run")
}
