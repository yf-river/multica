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

func completeCommentCreateRequest(
	ctx context.Context,
	queries *db.Queries,
	workspaceID pgtype.UUID,
	actorID pgtype.UUID,
	idempotencyKey pgtype.UUID,
	requestHash string,
	response CommentResponse,
) error {
	body, err := json.Marshal(response)
	if err != nil {
		return fmt.Errorf("encode comment response: %w", err)
	}
	_, err = queries.CompleteResourceCreateRequest(ctx, db.CompleteResourceCreateRequestParams{
		WorkspaceID:    workspaceID,
		ActorID:        actorID,
		ResourceType:   resourceTypeComment,
		IdempotencyKey: idempotencyKey,
		RequestHash:    requestHash,
		ResourceID:     parseUUID(response.ID),
		ResponseBody:   body,
	})
	if err != nil {
		return fmt.Errorf("complete comment request: %w", err)
	}
	return nil
}

func writeCommentCreateReplayError(w http.ResponseWriter, err error) {
	if errors.Is(err, errResourceCreateIdempotencyConflict) {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "Idempotency-Key was already used with a different comment request",
			"code":  "idempotency_conflict",
		})
		return
	}
	writeError(w, http.StatusInternalServerError, "failed to recover comment request")
}
