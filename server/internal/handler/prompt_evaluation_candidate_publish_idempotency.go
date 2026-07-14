package handler

import (
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
	writeResourceCreateReplayError(
		w, err,
		"Idempotency-Key was already used with a different optimization candidate publish request",
		"failed to recover optimization candidate publish request",
	)
}

func writePromptEvaluationCandidateRejectReplayError(w http.ResponseWriter, err error) {
	writeResourceCreateReplayError(
		w, err,
		"Idempotency-Key was already used with a different optimization candidate reject request",
		"failed to recover optimization candidate reject request",
	)
}
