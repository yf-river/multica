package handler

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"
)

type promptEvaluationLocalRunFingerprint struct {
	Operation   string `json:"operation"`
	AssetID     string `json:"asset_id,omitempty"`
	CandidateID string `json:"candidate_id,omitempty"`
}

func loadPromptEvaluationLocalRunReplay[T any](
	h *Handler,
	ctx context.Context,
	workspaceID pgtype.UUID,
	actorID pgtype.UUID,
	key pgtype.UUID,
	requestHash string,
	isValid func(T) bool,
) (T, bool, error) {
	return loadResourceCreateReplay(
		ctx, h.Queries, workspaceID, actorID, resourceTypePromptLocalRun, key, requestHash, isValid,
	)
}

func writePromptEvaluationLocalRunReplayError(w http.ResponseWriter, err error) {
	if errors.Is(err, errResourceCreateIdempotencyConflict) {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "Idempotency-Key was already used with a different prompt evaluation local run",
			"code":  "idempotency_conflict",
		})
		return
	}
	writeError(w, http.StatusInternalServerError, "failed to recover prompt evaluation local run")
}
