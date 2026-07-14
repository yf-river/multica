package handler

import (
	"net/http"
)

type promptEvaluationAgentRunFingerprint struct {
	AssetID string `json:"asset_id"`
}

func writePromptEvaluationAgentRunReplayError(w http.ResponseWriter, err error) {
	writeResourceCreateReplayError(
		w, err,
		"Idempotency-Key was already used with a different prompt evaluation agent run",
		"failed to recover prompt evaluation agent run",
	)
}
