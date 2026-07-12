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

const (
	resourceTypeProject    = "project"
	resourceTypeSquad      = "squad"
	resourceTypeAgent      = "agent"
	resourceTypeSkill      = "skill"
	resourceTypeAttachment = "attachment"
)

var errResourceCreateIdempotencyConflict = errors.New("resource create idempotency conflict")

func loadResourceCreateReplay[T any](
	ctx context.Context,
	queries *db.Queries,
	workspaceID pgtype.UUID,
	actorID pgtype.UUID,
	resourceType string,
	idempotencyKey pgtype.UUID,
	requestHash string,
	isValid func(T) bool,
) (T, bool, error) {
	var response T
	record, err := queries.GetResourceCreateRequest(ctx, db.GetResourceCreateRequestParams{
		WorkspaceID:    workspaceID,
		ActorID:        actorID,
		ResourceType:   resourceType,
		IdempotencyKey: idempotencyKey,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return response, false, nil
	}
	if err != nil {
		return response, false, err
	}
	if record.RequestHash != requestHash {
		return response, false, errResourceCreateIdempotencyConflict
	}
	if len(record.ResponseBody) == 0 || !record.CompletedAt.Valid || !record.ResourceID.Valid {
		return response, false, fmt.Errorf("%s create request is incomplete", resourceType)
	}
	if err := json.Unmarshal(record.ResponseBody, &response); err != nil {
		return response, false, fmt.Errorf("decode %s create replay: %w", resourceType, err)
	}
	if !isValid(response) {
		return response, false, fmt.Errorf("%s create replay has no resource id", resourceType)
	}
	return response, true, nil
}
