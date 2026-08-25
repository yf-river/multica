package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

var errAutopilotTriggerRotationIdempotencyConflict = errors.New("autopilot trigger rotation idempotency conflict")

func loadAutopilotTriggerRotationReplay(ctx context.Context, queries *db.Queries, workspaceID, actorID, key pgtype.UUID, requestHash string) (storedIdempotencyReplay, bool, error) {
	record, err := queries.GetAutopilotTriggerRotationRequest(ctx, db.GetAutopilotTriggerRotationRequestParams{
		WorkspaceID: workspaceID, ActorID: actorID, IdempotencyKey: key,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return storedIdempotencyReplay{}, false, nil
	}
	if err != nil {
		return storedIdempotencyReplay{}, false, err
	}
	if record.RequestHash != requestHash {
		return storedIdempotencyReplay{}, false, errAutopilotTriggerRotationIdempotencyConflict
	}
	if !record.CompletedAt.Valid || !record.ResponseStatus.Valid || len(record.ResponseBody) == 0 {
		return storedIdempotencyReplay{}, false, errors.New("autopilot trigger rotation request is incomplete")
	}
	return storedIdempotencyReplay{Status: int(record.ResponseStatus.Int32), Body: record.ResponseBody}, true, nil
}

func completeAutopilotTriggerRotationRequest(ctx context.Context, queries *db.Queries, workspaceID, actorID, key, triggerID pgtype.UUID, requestHash string, status int, response AutopilotTriggerResponse) error {
	body, err := json.Marshal(response)
	if err != nil {
		return fmt.Errorf("encode autopilot trigger rotation response: %w", err)
	}
	_, err = queries.CompleteAutopilotTriggerRotationRequest(ctx, db.CompleteAutopilotTriggerRotationRequestParams{
		WorkspaceID: workspaceID, ActorID: actorID, IdempotencyKey: key, TriggerID: triggerID,
		RequestHash: requestHash, ResponseStatus: pgtype.Int4{Int32: int32(status), Valid: true}, ResponseBody: body,
	})
	return err
}
