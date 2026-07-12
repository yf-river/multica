package handler

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"
)

type promptEvaluationCandidatePublishFingerprint struct {
	CandidateID string `json:"candidate_id"`
}

type promptEvaluationCandidateRejectFingerprint struct {
	CandidateID string `json:"candidate_id"`
	Reason      string `json:"reason"`
}

func (h *Handler) loadPromptEvaluationCandidatePublishReplay(
	ctx context.Context,
	workspaceID pgtype.UUID,
	actorID pgtype.UUID,
	key pgtype.UUID,
	requestHash string,
) (PublishPromptEvaluationOptimizationCandidateResponse, bool, error) {
	return loadResourceCreateReplay(
		ctx, h.Queries, workspaceID, actorID, resourceTypePromptPublish, key, requestHash,
		func(response PublishPromptEvaluationOptimizationCandidateResponse) bool {
			return response.Candidate.ID != "" && response.Prompt.ID != ""
		},
	)
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

func (h *Handler) loadPromptEvaluationCandidateRejectReplay(
	ctx context.Context,
	workspaceID pgtype.UUID,
	actorID pgtype.UUID,
	key pgtype.UUID,
	requestHash string,
) (PromptEvaluationOptimizationCandidateResponse, bool, error) {
	return loadResourceCreateReplay(
		ctx, h.Queries, workspaceID, actorID, resourceTypePromptReject, key, requestHash,
		func(response PromptEvaluationOptimizationCandidateResponse) bool { return response.ID != "" },
	)
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
