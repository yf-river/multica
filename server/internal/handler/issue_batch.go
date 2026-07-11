package handler

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/eventoutbox"
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

	// Short-circuit when no mutation field is present in `updates`. Without
	// this, the loop below runs N no-op UPDATEs (every if-guard skips, every
	// COALESCE preserves the existing value) and reports `{"updated": N}` —
	// the response cheerfully claims success while nothing changed. Most
	// real-world cases that hit this path are caller mistakes (status placed
	// at the top level, "update" misspelled as singular). Telling the truth
	// here — `{"updated": 0}` — keeps the wire shape stable while making the
	// count match reality. See multica-ai/multica#1660.
	hasMutation := req.Updates.Title != nil ||
		req.Updates.Description != nil ||
		req.Updates.Status != nil ||
		req.Updates.Priority != nil ||
		req.Updates.Position != nil
	if !hasMutation {
		for _, k := range []string{"assignee_type", "assignee_id", "start_date", "due_date", "parent_issue_id", "project_id"} {
			if _, ok := rawUpdates[k]; ok {
				hasMutation = true
				break
			}
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

		params := db.UpdateIssueParams{
			ID:            prevIssue.ID,
			AssigneeType:  prevIssue.AssigneeType,
			AssigneeID:    prevIssue.AssigneeID,
			StartDate:     prevIssue.StartDate,
			DueDate:       prevIssue.DueDate,
			ParentIssueID: prevIssue.ParentIssueID,
			ProjectID:     prevIssue.ProjectID,
		}

		if req.Updates.Title != nil {
			params.Title = pgtype.Text{String: *req.Updates.Title, Valid: true}
		}
		if req.Updates.Description != nil {
			params.Description = pgtype.Text{String: *req.Updates.Description, Valid: true}
		}
		if req.Updates.Status != nil {
			params.Status = pgtype.Text{String: *req.Updates.Status, Valid: true}
		}
		if req.Updates.Priority != nil {
			params.Priority = pgtype.Text{String: *req.Updates.Priority, Valid: true}
		}
		if req.Updates.Position != nil {
			params.Position = pgtype.Float8{Float64: *req.Updates.Position, Valid: true}
		}
		if _, ok := rawUpdates["assignee_type"]; ok {
			if req.Updates.AssigneeType != nil {
				params.AssigneeType = pgtype.Text{String: *req.Updates.AssigneeType, Valid: true}
			} else {
				params.AssigneeType = pgtype.Text{Valid: false}
			}
		}
		if _, ok := rawUpdates["assignee_id"]; ok {
			if req.Updates.AssigneeID != nil {
				assigneeUUID, err := util.ParseUUID(*req.Updates.AssigneeID)
				if err != nil {
					recordFailure(issueID, "invalid_assignee")
					continue
				}
				params.AssigneeID = assigneeUUID
			} else {
				params.AssigneeID = pgtype.UUID{Valid: false}
			}
		}
		if _, ok := rawUpdates["start_date"]; ok {
			if req.Updates.StartDate != nil && *req.Updates.StartDate != "" {
				d, err := util.ParseCalendarDate(*req.Updates.StartDate)
				if err != nil {
					recordFailure(issueID, "invalid_start_date")
					continue
				}
				params.StartDate = d
			} else {
				params.StartDate = pgtype.Date{Valid: false}
			}
		}
		if _, ok := rawUpdates["due_date"]; ok {
			if req.Updates.DueDate != nil && *req.Updates.DueDate != "" {
				d, err := util.ParseCalendarDate(*req.Updates.DueDate)
				if err != nil {
					recordFailure(issueID, "invalid_due_date")
					continue
				}
				params.DueDate = d
			} else {
				params.DueDate = pgtype.Date{Valid: false}
			}
		}

		if _, ok := rawUpdates["parent_issue_id"]; ok {
			if req.Updates.ParentIssueID != nil {
				newParentID, err := util.ParseUUID(*req.Updates.ParentIssueID)
				if err != nil {
					recordFailure(issueID, "invalid_parent")
					continue
				}
				if err := h.validateIssueParentInWorkspace(r.Context(), prevIssue, newParentID); err != nil {
					recordFailure(issueID, "invalid_parent")
					continue
				}
				params.ParentIssueID = newParentID
			} else {
				params.ParentIssueID = pgtype.UUID{Valid: false}
			}
		}
		if _, ok := rawUpdates["project_id"]; ok {
			if req.Updates.ProjectID != nil {
				projectUUID, err := util.ParseUUID(*req.Updates.ProjectID)
				if err != nil {
					recordFailure(issueID, "invalid_project")
					continue
				}
				if err := h.validateProjectInWorkspace(r.Context(), prevIssue.WorkspaceID, projectUUID); err != nil {
					recordFailure(issueID, "invalid_project")
					continue
				}
				params.ProjectID = projectUUID
			} else {
				params.ProjectID = pgtype.UUID{Valid: false}
			}
		}

		// Validate the resulting assignee pair when this batch update touches
		// either assignee field. Skip the issue silently on failure.
		_, batchTouchedType := rawUpdates["assignee_type"]
		_, batchTouchedID := rawUpdates["assignee_id"]
		if batchTouchedType || batchTouchedID {
			if status, _ := h.validateAssigneePair(r.Context(), r, workspaceID, params.AssigneeType, params.AssigneeID); status != 0 {
				recordFailure(issueID, "invalid_assignee")
				continue
			}
		}

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

		prefix := h.getIssuePrefix(r.Context(), issue.WorkspaceID)
		resp := issueToResponse(issue, prefix)
		actorType, actorID := h.resolveActor(r, userID, workspaceID)

		assigneeChanged := (batchTouchedType || batchTouchedID) &&
			(prevIssue.AssigneeType.String != issue.AssigneeType.String || uuidToString(prevIssue.AssigneeID) != uuidToString(issue.AssigneeID))
		statusChanged := req.Updates.Status != nil && prevIssue.Status != issue.Status
		priorityChanged := req.Updates.Priority != nil && prevIssue.Priority != issue.Priority
		_, touchedStartDate := rawUpdates["start_date"]
		_, touchedDueDate := rawUpdates["due_date"]
		changes := issueUpdateChanges{
			Assignee:    assigneeChanged,
			Status:      statusChanged,
			Priority:    priorityChanged,
			StartDate:   touchedStartDate && optionalStringChanged(dateToPtr(prevIssue.StartDate), resp.StartDate),
			DueDate:     touchedDueDate && optionalStringChanged(dateToPtr(prevIssue.DueDate), resp.DueDate),
			Description: req.Updates.Description != nil && optionalStringChanged(textToPtr(prevIssue.Description), resp.Description),
			Title:       req.Updates.Title != nil && prevIssue.Title != issue.Title,
		}
		skipBacklogEnqueue := statusChanged && !assigneeChanged && prevIssue.Status == "backlog" &&
			h.isAssignedAgentRunningOnIssue(r.Context(), r, actorType, actorID, issue)
		taskProjection, err := h.reconcileIssueUpdateTasksInTx(r.Context(), qtx, prevIssue, issue, assigneeChanged, statusChanged, skipBacklogEnqueue, actorType, actorID)
		if err != nil {
			_ = tx.Rollback(r.Context())
			slog.Warn("batch project issue update tasks failed", "issue_id", issueID, "error", err)
			recordFailure(issueID, "task_projection_failed")
			continue
		}
		updatedEvent := buildIssueUpdatedEvent(workspaceID, actorType, actorID, prevIssue, resp, changes)
		updatedEvent, err = eventoutbox.Enqueue(r.Context(), qtx, updatedEvent)
		if err != nil {
			_ = tx.Rollback(r.Context())
			slog.Warn("batch enqueue issue update event failed", "issue_id", issueID, "error", err)
			recordFailure(issueID, "event_failed")
			continue
		}
		if err := tx.Commit(r.Context()); err != nil {
			slog.Warn("batch commit issue update failed", "issue_id", issueID, "error", err)
			recordFailure(issueID, "transaction_failed")
			continue
		}
		h.publishEvent(updatedEvent)
		h.publishIssueUpdateTaskProjection(r.Context(), taskProjection)
		if statusChanged {
			h.notifyParentOfChildDone(r.Context(), prevIssue, issue, actorType, actorID)
		}

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

type BatchDeleteIssuesRequest struct {
	IssueIDs []string `json:"issue_ids"`
}

func (h *Handler) BatchDeleteIssues(w http.ResponseWriter, r *http.Request) {
	var req BatchDeleteIssuesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
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
		actorType, actorID := h.resolveActor(r, userID, workspaceID)
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
