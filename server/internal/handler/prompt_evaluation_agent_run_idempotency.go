package handler

import (
	"errors"
	"net/http"
)

type promptEvaluationAgentRunFingerprint struct {
	AssetID string `json:"asset_id"`
}

func writePromptEvaluationAgentRunReplayError(w http.ResponseWriter, err error) {
	if errors.Is(err, errResourceCreateIdempotencyConflict) {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "Idempotency-Key was already used with a different prompt evaluation agent run",
			"code":  "idempotency_conflict",
		})
		return
	}
	writeError(w, http.StatusInternalServerError, "failed to recover prompt evaluation agent run")
}
