package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

var errAutopilotTriggerRotationIdempotencyConflict = errors.New("autopilot trigger rotation idempotency conflict")

type autopilotTriggerRotationReplay struct {
	Status int
	Body   []byte
}

func loadAutopilotTriggerRotationReplay(ctx context.Context, queries *db.Queries, workspaceID, actorID, key pgtype.UUID, requestHash string) (autopilotTriggerRotationReplay, bool, error) {
	record, err := queries.GetAutopilotTriggerRotationRequest(ctx, db.GetAutopilotTriggerRotationRequestParams{
		WorkspaceID: workspaceID, ActorID: actorID, IdempotencyKey: key,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return autopilotTriggerRotationReplay{}, false, nil
	}
	if err != nil {
		return autopilotTriggerRotationReplay{}, false, err
	}
	if record.RequestHash != requestHash {
		return autopilotTriggerRotationReplay{}, false, errAutopilotTriggerRotationIdempotencyConflict
	}
	if !record.CompletedAt.Valid || !record.ResponseStatus.Valid || len(record.ResponseBody) == 0 {
		return autopilotTriggerRotationReplay{}, false, errors.New("autopilot trigger rotation request is incomplete")
	}
	return autopilotTriggerRotationReplay{Status: int(record.ResponseStatus.Int32), Body: record.ResponseBody}, true, nil
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

func writeAutopilotTriggerRotationReplay(w http.ResponseWriter, replay autopilotTriggerRotationReplay) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Idempotency-Replayed", "true")
	w.WriteHeader(replay.Status)
	_, _ = w.Write(replay.Body)
}
