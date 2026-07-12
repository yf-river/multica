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

func (h *Handler) loadPromptLibraryTrialReplay(ctx context.Context, workspaceID, actorID, key pgtype.UUID, requestHash string) (PromptLibraryTrialResponse, bool, error) {
	return loadResourceCreateReplay(ctx, h.Queries, workspaceID, actorID, resourceTypePromptLibraryTrial, key, requestHash,
		func(response PromptLibraryTrialResponse) bool { return response.ID != "" })
}

func completePromptLibraryTrialRequest(ctx context.Context, queries *db.Queries, workspaceID, actorID, key pgtype.UUID, requestHash string, response PromptLibraryTrialResponse) error {
	body, err := json.Marshal(response)
	if err != nil {
		return fmt.Errorf("encode prompt library trial response: %w", err)
	}
	_, err = queries.CompleteResourceCreateRequest(ctx, db.CompleteResourceCreateRequestParams{
		WorkspaceID: workspaceID, ActorID: actorID, ResourceType: resourceTypePromptLibraryTrial,
		IdempotencyKey: key, RequestHash: requestHash, ResourceID: parseUUID(response.ID), ResponseBody: body,
	})
	if err != nil {
		return fmt.Errorf("complete prompt library trial request: %w", err)
	}
	return nil
}

func writePromptLibraryTrialReplayError(w http.ResponseWriter, err error) {
	if errors.Is(err, errResourceCreateIdempotencyConflict) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "Idempotency-Key was already used with a different prompt trial request", "code": "idempotency_conflict"})
		return
	}
	writeError(w, http.StatusInternalServerError, "failed to recover prompt library trial request")
}
