package handler

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"
)

type promptEvaluationReEvalAssetFingerprint struct {
	CandidateID string                                    `json:"candidate_id"`
	Request     PreparePromptEvaluationSkillReEvalRequest `json:"request"`
}

func (h *Handler) loadPromptEvaluationReEvalAssetReplay(
	ctx context.Context,
	workspaceID pgtype.UUID,
	actorID pgtype.UUID,
	key pgtype.UUID,
	requestHash string,
) (PromptEvaluationSkillReEvalAssetResponse, bool, error) {
	return loadResourceCreateReplay(
		ctx, h.Queries, workspaceID, actorID, resourceTypePromptReEvalAsset, key, requestHash,
		func(response PromptEvaluationSkillReEvalAssetResponse) bool { return response.Asset.ID != "" },
	)
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
