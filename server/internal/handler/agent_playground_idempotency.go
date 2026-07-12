package handler

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"
)

func (h *Handler) loadAgentPlaygroundCreateReplay(
	ctx context.Context,
	workspaceID pgtype.UUID,
	actorID pgtype.UUID,
	key pgtype.UUID,
	requestHash string,
) (AgentPlaygroundDetailResponse, bool, error) {
	return loadResourceCreateReplay(
		ctx, h.Queries, workspaceID, actorID, resourceTypeAgentPlayground, key, requestHash,
		func(response AgentPlaygroundDetailResponse) bool { return response.Experiment.ID != "" },
	)
}

func writeAgentPlaygroundCreateReplayError(w http.ResponseWriter, err error) {
	if errors.Is(err, errResourceCreateIdempotencyConflict) {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "Idempotency-Key was already used with a different agent playground request",
			"code":  "idempotency_conflict",
		})
		return
	}
	writeError(w, http.StatusInternalServerError, "failed to recover agent playground request")
}
