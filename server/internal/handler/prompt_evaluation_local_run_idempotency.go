package handler

import (
	"net/http"
)

type promptEvaluationLocalRunFingerprint struct {
	Operation   string `json:"operation"`
	AssetID     string `json:"asset_id,omitempty"`
	CandidateID string `json:"candidate_id,omitempty"`
}

func writePromptEvaluationLocalRunReplayError(w http.ResponseWriter, err error) {
	writeResourceCreateReplayError(
		w, err,
		"Idempotency-Key was already used with a different prompt evaluation local run",
		"failed to recover prompt evaluation local run",
	)
}
