package handler

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func (h *Handler) BatchUpdateIssues(w http.ResponseWriter, r *http.Request) {
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read request body")
		return
	}

	var req BatchUpdateIssuesRequest
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if len(req.IssueIDs) == 0 {
		writeError(w, http.StatusBadRequest, "issue_ids is required")
		return
	}

	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	// Detect which fields in "updates" were explicitly set (including null).
	var rawTop map[string]json.RawMessage
	if err := json.Unmarshal(bodyBytes, &rawTop); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	var rawUpdates map[string]json.RawMessage
	if raw, exists := rawTop["updates"]; exists {
		if err := json.Unmarshal(raw, &rawUpdates); err != nil {
			writeError(w, http.StatusBadRequest, "updates must be an object")
			return
		}
	}

	hasMutation := req.Updates.Title != nil || req.Updates.Description != nil || req.Updates.Status != nil || req.Updates.Priority != nil || req.Updates.Position != nil
	for _, field := range []string{"assignee_type", "assignee_id", "start_date", "due_date", "parent_issue_id", "project_id"} {
		if _, ok := rawUpdates[field]; ok {
			hasMutation = true
			break
		}
	}
	if !hasMutation {
		writeJSON(w, http.StatusOK, map[string]any{"updated": 0})
		return
	}
	if req.Updates.Status != nil {
		if !validateIssueEnum(w, "status", *req.Updates.Status, validIssueStatuses) {
			return
		}
	}
	if req.Updates.Priority != nil {
		if !validateIssueEnum(w, "priority", *req.Updates.Priority, validIssuePriorities) {
			return
		}
	}

	workspaceID := h.resolveWorkspaceID(r)
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	updated := 0
	type batchDoneBlockedIssue struct {
		IssueID            string                         `json:"issue_id"`
		Identifier         string                         `json:"identifier"`
		Title              string                         `json:"title"`
		IncompleteChildren []IncompleteChildIssueResponse `json:"incomplete_children"`
	}
	blocked := make([]batchDoneBlockedIssue, 0)
	type batchUpdateFailure struct {
		IssueID string `json:"issue_id"`
		Code    string `json:"code"`
	}
	failed := make([]batchUpdateFailure, 0)
	recordFailure := func(issueID, code string) {
		failed = append(failed, batchUpdateFailure{IssueID: issueID, Code: code})
	}
	for _, issueID := range req.IssueIDs {
		issueUUID, err := util.ParseUUID(issueID)
		if err != nil {
			recordFailure(issueID, "invalid_id")
			continue
		}
		prevIssue, err := h.Queries.GetIssueInWorkspace(r.Context(), db.GetIssueInWorkspaceParams{
			ID:          issueUUID,
			WorkspaceID: wsUUID,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				recordFailure(issueID, "not_found")
			} else {
				recordFailure(issueID, "lookup_failed")
			}
			continue
		}

		prepared, inputErr := h.prepareIssueUpdate(r.Context(), r, prevIssue, req.Updates, rawUpdates)
		if inputErr != nil {
			if inputErr.assigneeCause != nil {
				writeAssigneeValidationError(w, r, inputErr.assigneeCause)
				return
			}
			recordFailure(issueID, inputErr.failureCode)
			continue
		}
		params := prepared.params

		if params.Status.Valid && params.Status.String == "done" {
			incomplete, err := h.incompleteChildrenBlockingDone(r.Context(), prevIssue)
			if err != nil {
				slog.Warn("batch check child issue done gate failed", "issue_id", issueID, "error", err)
				recordFailure(issueID, "child_check_failed")
				continue
			}
			if len(incomplete) > 0 {
				prefix := h.getIssuePrefix(r.Context(), prevIssue.WorkspaceID)
				blocked = append(blocked, batchDoneBlockedIssue{
					IssueID:            uuidToString(prevIssue.ID),
					Identifier:         prefix + "-" + strconv.Itoa(int(prevIssue.Number)),
					Title:              prevIssue.Title,
					IncompleteChildren: incomplete,
				})
				continue
			}
		}

		tx, err := h.TxStarter.Begin(r.Context())
		if err != nil {
			slog.Warn("batch begin issue update transaction failed", "issue_id", issueID, "error", err)
			recordFailure(issueID, "transaction_failed")
			continue
		}
		qtx := h.Queries.WithTx(tx)
		issue, err := qtx.UpdateIssue(r.Context(), params)
		if err != nil {
			_ = tx.Rollback(r.Context())
			slog.Warn("batch update issue failed", "issue_id", issueID, "error", err)
			recordFailure(issueID, "update_failed")
			continue
		}

		actorType, actorID := resolveActor(r, userID)
		projection, failure := h.projectIssueUpdateInTx(r.Context(), r, qtx, prevIssue, issue, prepared, req.Updates, actorType, actorID)
		if failure != nil {
			_ = tx.Rollback(r.Context())
			slog.Warn("batch project issue update failed", "stage", failure.code, "issue_id", issueID, "error", failure.cause)
			recordFailure(issueID, failure.code)
			continue
		}
		if err := tx.Commit(r.Context()); err != nil {
			slog.Warn("batch commit issue update failed", "issue_id", issueID, "error", err)
			recordFailure(issueID, "transaction_failed")
			continue
		}
		h.publishIssueUpdateProjection(r.Context(), projection, prevIssue, actorType, actorID)

		updated++
	}

	slog.Info("batch update issues", append(logger.RequestAttrs(r), "count", updated)...)
	resp := map[string]any{"updated": updated}
	if len(blocked) > 0 {
		resp["blocked"] = blocked
		resp["blocked_reason"] = "child_issues_not_done"
	}
	if len(failed) > 0 {
		resp["failed"] = failed
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) BatchDeleteIssues(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IssueIDs []string `json:"issue_ids"`
	}
	if !decodeRequiredJSON(w, r, &req) {
		return
	}

	if len(req.IssueIDs) == 0 {
		writeError(w, http.StatusBadRequest, "issue_ids is required")
		return
	}

	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	workspaceID := h.resolveWorkspaceID(r)
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	deleted := 0
	type batchDeleteFailure struct {
		IssueID string `json:"issue_id"`
		Code    string `json:"code"`
	}
	failed := make([]batchDeleteFailure, 0)
	for _, issueID := range req.IssueIDs {
		issueUUID, err := util.ParseUUID(issueID)
		if err != nil {
			failed = append(failed, batchDeleteFailure{IssueID: issueID, Code: "invalid_id"})
			continue
		}
		issue, err := h.Queries.GetIssueInWorkspace(r.Context(), db.GetIssueInWorkspaceParams{
			ID:          issueUUID,
			WorkspaceID: wsUUID,
		})
		if err != nil {
			code := "lookup_failed"
			if errors.Is(err, pgx.ErrNoRows) {
				code = "not_found"
			}
			failed = append(failed, batchDeleteFailure{IssueID: issueID, Code: code})
			continue
		}

		result, err := h.deleteIssueAtomically(r.Context(), issue)
		if err != nil {
			slog.Warn("batch delete issue failed", "issue_id", issueID, "error", err)
			failed = append(failed, batchDeleteFailure{IssueID: issueID, Code: "delete_failed"})
			continue
		}

		h.TaskService.PublishCancelledTasks(r.Context(), result.cancelledTasks, result.cancelledEvents)
		h.deleteStorageObjects(r.Context(), result.attachmentURLs)

		// Always emit the resolved UUID — frontend caches key by UUID.
		actorType, actorID := resolveActor(r, userID)
		h.publish(protocol.EventIssueDeleted, workspaceID, actorType, actorID, map[string]any{"issue_id": uuidToString(issue.ID)})
		deleted++
	}

	slog.Info("batch delete issues", append(logger.RequestAttrs(r), "count", deleted)...)
	response := map[string]any{"deleted": deleted}
	if len(failed) > 0 {
		response["failed"] = failed
	}
	writeJSON(w, http.StatusOK, response)
}
