package handler

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"
)

type promptEvaluationCandidateCreateFingerprint struct {
	RunID string `json:"run_id"`
}

func (h *Handler) loadPromptEvaluationCandidateCreateReplay(
	ctx context.Context,
	workspaceID pgtype.UUID,
	actorID pgtype.UUID,
	key pgtype.UUID,
	requestHash string,
) (PromptEvaluationOptimizationCandidateResponse, bool, error) {
	return loadResourceCreateReplay(
		ctx, h.Queries, workspaceID, actorID, resourceTypePromptCandidate, key, requestHash,
		func(response PromptEvaluationOptimizationCandidateResponse) bool { return response.ID != "" },
	)
}

func writePromptEvaluationCandidateCreateReplayError(w http.ResponseWriter, err error) {
	if errors.Is(err, errResourceCreateIdempotencyConflict) {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "Idempotency-Key was already used with a different optimization candidate request",
			"code":  "idempotency_conflict",
		})
		return
	}
	writeError(w, http.StatusInternalServerError, "failed to recover optimization candidate request")
}
