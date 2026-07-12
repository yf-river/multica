package handler

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func (h *Handler) loadWorkspaceCreateReplay(
	ctx context.Context,
	workspaceID pgtype.UUID,
	actorID pgtype.UUID,
	key pgtype.UUID,
	requestHash string,
) (WorkspaceResponse, bool, error) {
	return loadResourceCreateReplay(
		ctx, h.Queries, workspaceID, actorID, resourceTypeWorkspace, key, requestHash,
		func(response WorkspaceResponse) bool { return response.ID != "" },
	)
}

func (h *Handler) writeWorkspaceCreateReplay(
	w http.ResponseWriter,
	ctx context.Context,
	workspaceID pgtype.UUID,
	actorID pgtype.UUID,
	response WorkspaceResponse,
) {
	_, err := h.Queries.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{
		WorkspaceID: workspaceID,
		UserID:      actorID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusForbidden, "workspace access denied")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to verify workspace access")
		return
	}
	writeJSON(w, http.StatusCreated, response)
}

func writeWorkspaceCreateReplayError(w http.ResponseWriter, err error) {
	if errors.Is(err, errResourceCreateIdempotencyConflict) {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "Idempotency-Key was already used with a different workspace request",
			"code":  "idempotency_conflict",
		})
		return
	}
	writeError(w, http.StatusInternalServerError, "failed to recover workspace request")
}
