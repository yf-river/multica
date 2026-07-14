package handler

import (
	"net/http"
)

type promptEvaluationReEvalAssetFingerprint struct {
	CandidateID string                                    `json:"candidate_id"`
	Request     PreparePromptEvaluationSkillReEvalRequest `json:"request"`
}

func writePromptEvaluationReEvalAssetReplayError(w http.ResponseWriter, err error) {
	writeResourceCreateReplayError(
		w, err,
		"Idempotency-Key was already used with a different skill re-eval asset request",
		"failed to recover skill re-eval asset request",
	)
}
