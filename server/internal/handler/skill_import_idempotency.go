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

var errSkillImportIdempotencyConflict = errors.New("skill import idempotency conflict")

func loadSkillImportReplay(
	ctx context.Context,
	queries *db.Queries,
	workspaceID, actorID, key pgtype.UUID,
	requestHash string,
) (storedIdempotencyReplay, bool, error) {
	record, err := queries.GetSkillImportRequest(ctx, db.GetSkillImportRequestParams{
		WorkspaceID: workspaceID, ActorID: actorID, IdempotencyKey: key,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return storedIdempotencyReplay{}, false, nil
	}
	if err != nil {
		return storedIdempotencyReplay{}, false, err
	}
	if record.RequestHash != requestHash {
		return storedIdempotencyReplay{}, false, errSkillImportIdempotencyConflict
	}
	if !record.CompletedAt.Valid || !record.ResponseStatus.Valid || len(record.ResponseBody) == 0 {
		return storedIdempotencyReplay{}, false, errors.New("skill import request is incomplete")
	}
	return storedIdempotencyReplay{Status: int(record.ResponseStatus.Int32), Body: record.ResponseBody}, true, nil
}

func completeSkillImportRequest(
	ctx context.Context,
	queries *db.Queries,
	workspaceID, actorID, key pgtype.UUID,
	requestHash string,
	status int,
	response any,
) error {
	body, err := json.Marshal(response)
	if err != nil {
		return fmt.Errorf("encode skill import response: %w", err)
	}
	_, err = queries.CompleteSkillImportRequest(ctx, db.CompleteSkillImportRequestParams{
		WorkspaceID: workspaceID, ActorID: actorID, IdempotencyKey: key,
		RequestHash: requestHash, ResponseStatus: pgtype.Int4{Int32: int32(status), Valid: true},
		ResponseBody: body,
	})
	return err
}
