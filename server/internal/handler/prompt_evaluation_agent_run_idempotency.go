package handler

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"
)

type promptEvaluationAgentRunFingerprint struct {
	AssetID string `json:"asset_id"`
}

func (h *Handler) loadPromptEvaluationAgentRunReplay(
	ctx context.Context,
	workspaceID pgtype.UUID,
	actorID pgtype.UUID,
	key pgtype.UUID,
	requestHash string,
) (PromptEvaluationAgentRunResponse, bool, error) {
	return loadResourceCreateReplay(
		ctx, h.Queries, workspaceID, actorID, resourceTypePromptEvaluationRun, key, requestHash,
		func(response PromptEvaluationAgentRunResponse) bool {
			return response.Run.ID != "" && response.TaskID != ""
		},
	)
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
