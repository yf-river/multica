package handler

import (
	"errors"
	"net/http"
)

type promptEvaluationReEvalAssetFingerprint struct {
	CandidateID string                                    `json:"candidate_id"`
	Request     PreparePromptEvaluationSkillReEvalRequest `json:"request"`
}

func writePromptEvaluationReEvalAssetReplayError(w http.ResponseWriter, err error) {
	if errors.Is(err, errResourceCreateIdempotencyConflict) {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "Idempotency-Key was already used with a different skill re-eval asset request",
			"code":  "idempotency_conflict",
		})
		return
	}
	writeError(w, http.StatusInternalServerError, "failed to recover skill re-eval asset request")
}
