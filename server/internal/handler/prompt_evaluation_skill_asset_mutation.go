package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func executePromptEvaluationSkillAssetMutation[Result, Response any](
	w http.ResponseWriter,
	r *http.Request,
	h *Handler,
	asset db.PromptEvaluationAsset,
	resourceType string,
	operation string,
	request any,
	isValid func(Response) bool,
	build func() (Result, error),
	mutatePayload func(map[string]any, Result),
	buildResponse func(db.PromptEvaluationAsset, Result) Response,
) {
	actorID, idempotencyKey, requestHash, ok := promptEvaluationSkillAssetMutationScope(
		w, r, asset, resourceType, request,
	)
	if !ok {
		return
	}
	replay, found, err := loadResourceCreateReplay(
		r.Context(), h.Queries, asset.WorkspaceID, actorID, resourceType, idempotencyKey, requestHash, isValid,
	)
	if err != nil {
		writePromptEvaluationSkillAssetMutationError(w, err, operation)
		return
	}
	if found {
		writeJSON(w, http.StatusCreated, replay)
		return
	}
	result, err := build()
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	response, err := persistPromptEvaluationSkillAssetMutation(
		r.Context(), h, asset, actorID, resourceType, idempotencyKey, requestHash, isValid,
		func(payload map[string]any) { mutatePayload(payload, result) },
		func(updated db.PromptEvaluationAsset) Response { return buildResponse(updated, result) },
	)
	if err != nil {
		writePromptEvaluationSkillAssetMutationError(w, err, operation)
		return
	}
	writeJSON(w, http.StatusCreated, response)
}

func promptEvaluationSkillAssetMutationScope(
	w http.ResponseWriter,
	r *http.Request,
	asset db.PromptEvaluationAsset,
	resourceType string,
	request any,
) (pgtype.UUID, pgtype.UUID, string, bool) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return pgtype.UUID{}, pgtype.UUID{}, "", false
	}
	idempotencyKey, ok := optionalIdempotencyKey(w, r)
	if !ok {
		return pgtype.UUID{}, pgtype.UUID{}, "", false
	}
	requestHash, err := hashRequestFingerprint(struct {
		AssetID string `json:"asset_id"`
		Request any    `json:"request"`
	}{AssetID: uuidToString(asset.ID), Request: request})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to fingerprint "+resourceType+" request")
		return pgtype.UUID{}, pgtype.UUID{}, "", false
	}
	return parseUUID(userID), idempotencyKey, requestHash, true
}

func writePromptEvaluationSkillAssetMutationError(w http.ResponseWriter, err error, operation string) {
	writeResourceCreateReplayError(
		w, err,
		"Idempotency-Key was already used with a different "+operation+" request",
		"failed to persist "+operation,
	)
}

func persistPromptEvaluationSkillAssetMutation[T any](
	ctx context.Context,
	h *Handler,
	asset db.PromptEvaluationAsset,
	actorID pgtype.UUID,
	resourceType string,
	idempotencyKey pgtype.UUID,
	requestHash string,
	isValid func(T) bool,
	mutatePayload func(map[string]any),
	buildResponse func(db.PromptEvaluationAsset) T,
) (T, error) {
	var zero T
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return zero, fmt.Errorf("start %s transaction: %w", resourceType, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := h.Queries.WithTx(tx)
	err = reserveResourceCreateRequest(ctx, qtx, asset.WorkspaceID, actorID, resourceType, idempotencyKey, requestHash)
	if errors.Is(err, pgx.ErrNoRows) {
		replay, replayErr := loadReplayAfterReservationConflict(ctx, tx, func() (T, bool, error) {
			return loadResourceCreateReplay(
				ctx, h.Queries, asset.WorkspaceID, actorID, resourceType, idempotencyKey, requestHash, isValid,
			)
		})
		if replayErr != nil {
			return zero, replayErr
		}
		return replay, nil
	}
	if err != nil {
		return zero, fmt.Errorf("reserve %s request: %w", resourceType, err)
	}
	lockedAsset, err := qtx.LockPromptEvaluationAsset(ctx, db.LockPromptEvaluationAssetParams{
		ID: asset.ID, WorkspaceID: asset.WorkspaceID,
	})
	if err != nil {
		return zero, fmt.Errorf("lock prompt evaluation asset: %w", err)
	}
	payload := decodePayloadObject(lockedAsset.Payload)
	mutatePayload(payload)
	updated, err := updatePromptEvaluationAssetPayload(ctx, qtx, lockedAsset, payload)
	if err != nil {
		return zero, err
	}
	response := buildResponse(updated)
	if err := completeResourceCreateRequest(
		ctx, qtx, asset.WorkspaceID, actorID, resourceType,
		idempotencyKey, requestHash, asset.ID, response,
	); err != nil {
		return zero, err
	}
	if err := tx.Commit(ctx); err != nil {
		return zero, fmt.Errorf("commit %s request: %w", resourceType, err)
	}
	return response, nil
}

func updatePromptEvaluationAssetPayload(
	ctx context.Context,
	queries *db.Queries,
	asset db.PromptEvaluationAsset,
	payload map[string]any,
) (db.PromptEvaluationAsset, error) {
	updated, err := queries.UpdatePromptEvaluationAsset(
		ctx,
		promptEvaluationAssetPayloadUpdateParams(asset, mustJSONBytes(payload)),
	)
	if err != nil {
		return db.PromptEvaluationAsset{}, fmt.Errorf("update prompt evaluation asset payload: %w", err)
	}
	return updated, nil
}
