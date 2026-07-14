package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func (h *Handler) loadIssueCreateReplay(
	ctx context.Context,
	workspaceID pgtype.UUID,
	actorID pgtype.UUID,
	idempotencyKey pgtype.UUID,
	requestHash string,
) (IssueResponse, bool, error) {
	return loadResourceCreateReplay(
		ctx, h.Queries, workspaceID, actorID, resourceTypeIssue,
		idempotencyKey, requestHash,
		func(response IssueResponse) bool { return response.ID != "" },
	)
}

func (h *Handler) createIssueWithRecovery(
	ctx context.Context,
	workspaceID pgtype.UUID,
	actorID pgtype.UUID,
	idempotencyKey pgtype.UUID,
	requestHash string,
	params service.IssueCreateParams,
	opts service.IssueCreateOpts,
	prefix string,
	buildAttachments func([]db.Attachment) []AttachmentResponse,
) (service.IssueCreateResult, *IssueResponse, error) {
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return service.IssueCreateResult{}, nil, fmt.Errorf("begin issue request: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := h.Queries.WithTx(tx)
	err = reserveResourceCreateRequest(ctx, queries, workspaceID, actorID, resourceTypeIssue, idempotencyKey, requestHash)
	if errors.Is(err, pgx.ErrNoRows) {
		replay, replayErr := loadReplayAfterReservationConflict(ctx, tx, func() (IssueResponse, bool, error) {
			return h.loadIssueCreateReplay(
				ctx, workspaceID, actorID, idempotencyKey, requestHash,
			)
		})
		if replayErr != nil {
			return service.IssueCreateResult{}, nil, replayErr
		}
		return service.IssueCreateResult{}, &replay, nil
	}
	if err != nil {
		return service.IssueCreateResult{}, nil, fmt.Errorf("reserve issue request: %w", err)
	}

	prepared, err := h.IssueService.PrepareCreateInTx(ctx, tx, queries, params, opts)
	if err != nil {
		return service.IssueCreateResult{}, nil, err
	}
	response := issueToResponse(prepared.Result.Issue, prefix)
	response.Attachments = buildAttachments(prepared.Result.Attachments)
	if err := completeResourceCreateRequest(
		ctx, queries, workspaceID, actorID, resourceTypeIssue,
		idempotencyKey, requestHash, prepared.Result.Issue.ID, response,
	); err != nil {
		return service.IssueCreateResult{}, nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return service.IssueCreateResult{}, nil, fmt.Errorf("commit issue request: %w", err)
	}
	h.IssueService.PublishPreparedCreate(ctx, prepared)
	return prepared.Result, nil, nil
}

func writeIssueCreateReplayError(w http.ResponseWriter, err error) {
	if errors.Is(err, errResourceCreateIdempotencyConflict) {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "Idempotency-Key was already used with a different request",
			"code":  "idempotency_conflict",
		})
		return
	}
	writeError(w, http.StatusInternalServerError, "failed to recover issue request")
}
