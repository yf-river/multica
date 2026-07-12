package handler

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"
)

type CreatePromptLibraryVersionResponse struct {
	Item    PromptLibraryItemResponse    `json:"item"`
	Version PromptLibraryVersionResponse `json:"version"`
}

func (h *Handler) loadPromptLibraryItemCreateReplay(ctx context.Context, workspaceID, actorID, key pgtype.UUID, requestHash string) (PromptLibraryItemResponse, bool, error) {
	return loadResourceCreateReplay(ctx, h.Queries, workspaceID, actorID, resourceTypePromptLibraryItem, key, requestHash,
		func(response PromptLibraryItemResponse) bool { return response.ID != "" })
}

func (h *Handler) loadPromptLibraryVersionCreateReplay(ctx context.Context, workspaceID, actorID, key pgtype.UUID, requestHash string) (CreatePromptLibraryVersionResponse, bool, error) {
	return loadResourceCreateReplay(ctx, h.Queries, workspaceID, actorID, resourceTypePromptLibraryVersion, key, requestHash,
		func(response CreatePromptLibraryVersionResponse) bool {
			return response.Item.ID != "" && response.Version.ID != ""
		})
}

func writePromptLibraryCreateReplayError(w http.ResponseWriter, resource string, err error) {
	if errors.Is(err, errResourceCreateIdempotencyConflict) {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "Idempotency-Key was already used with a different " + resource + " request",
			"code":  "idempotency_conflict",
		})
		return
	}
	writeError(w, http.StatusInternalServerError, "failed to recover "+resource+" request")
}
