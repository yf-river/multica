package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
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

func completePromptLibraryCreateRequest(ctx context.Context, queries *db.Queries, workspaceID, actorID pgtype.UUID, resourceType string, key pgtype.UUID, requestHash string, resourceID pgtype.UUID, response any) error {
	body, err := json.Marshal(response)
	if err != nil {
		return fmt.Errorf("encode %s response: %w", resourceType, err)
	}
	_, err = queries.CompleteResourceCreateRequest(ctx, db.CompleteResourceCreateRequestParams{
		WorkspaceID: workspaceID, ActorID: actorID, ResourceType: resourceType,
		IdempotencyKey: key, RequestHash: requestHash, ResourceID: resourceID, ResponseBody: body,
	})
	if err != nil {
		return fmt.Errorf("complete %s request: %w", resourceType, err)
	}
	return nil
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
