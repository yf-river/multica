package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func quickCreateResponseStatus(response QuickCreateIssueResponse) int {
	if response.TaskID != "" {
		return http.StatusAccepted
	}
	return http.StatusCreated
}

func completeQuickCreateRequest(
	ctx context.Context,
	queries *db.Queries,
	workspaceID pgtype.UUID,
	actorID pgtype.UUID,
	idempotencyKey pgtype.UUID,
	requestHash string,
	response QuickCreateIssueResponse,
) error {
	resourceIDRaw := response.TaskID
	if resourceIDRaw == "" {
		resourceIDRaw = response.IssueID
	}
	resourceID, err := util.ParseUUID(resourceIDRaw)
	if err != nil {
		return fmt.Errorf("quick-create response has invalid resource id: %w", err)
	}
	return completeResourceCreateRequest(
		ctx, queries, workspaceID, actorID, resourceTypeQuickCreate,
		idempotencyKey, requestHash, resourceID, response,
	)
}

func (h *Handler) recoverQuickCreateResource(
	ctx context.Context,
	workspaceID pgtype.UUID,
	requesterID pgtype.UUID,
	requestID pgtype.UUID,
	requestHash string,
) (QuickCreateIssueResponse, bool, error) {
	task, err := h.Queries.GetAgentTask(ctx, requestID)
	if err == nil {
		var payload struct {
			Type        string `json:"type"`
			WorkspaceID string `json:"workspace_id"`
			RequesterID string `json:"requester_id"`
			RequestHash string `json:"request_hash"`
		}
		if json.Unmarshal(task.Context, &payload) != nil ||
			payload.Type != "quick_create" ||
			payload.WorkspaceID != uuidToString(workspaceID) ||
			payload.RequesterID != uuidToString(requesterID) ||
			payload.RequestHash != requestHash {
			return QuickCreateIssueResponse{}, false, errResourceCreateIdempotencyConflict
		}
		return QuickCreateIssueResponse{TaskID: uuidToString(task.ID)}, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return QuickCreateIssueResponse{}, false, fmt.Errorf("load quick-create task: %w", err)
	}

	issue, err := h.Queries.GetIssueByOrigin(ctx, db.GetIssueByOriginParams{
		WorkspaceID: workspaceID,
		OriginType:  pgtype.Text{String: "quick_create", Valid: true},
		OriginID:    requestID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return QuickCreateIssueResponse{}, false, nil
	}
	if err != nil {
		return QuickCreateIssueResponse{}, false, fmt.Errorf("load quick-create issue: %w", err)
	}
	var metadata map[string]json.RawMessage
	if json.Unmarshal(issue.Metadata, &metadata) != nil {
		return QuickCreateIssueResponse{}, false, errors.New("decode quick-create issue metadata")
	}
	storedHash, _ := metadataString(metadata, "quick_create_request_hash")
	if storedHash != requestHash {
		return QuickCreateIssueResponse{}, false, errResourceCreateIdempotencyConflict
	}
	status, _ := metadataString(metadata, "source_fetch_status")
	return QuickCreateIssueResponse{
		IssueID:           uuidToString(issue.ID),
		Identifier:        issueToResponse(issue, h.getIssuePrefix(ctx, workspaceID)).Identifier,
		SourceFetchStatus: status,
	}, true, nil
}

func writeQuickCreateReplayError(w http.ResponseWriter, err error) {
	if errors.Is(err, errResourceCreateIdempotencyConflict) {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "Idempotency-Key was already used with a different quick-create request",
			"code":  "idempotency_conflict",
		})
		return
	}
	writeError(w, http.StatusInternalServerError, "failed to recover quick-create request")
}
