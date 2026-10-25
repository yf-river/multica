package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/service"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func (h *Handler) hydrateTaskSupplementMetadata(ctx context.Context, r *http.Request, workspaceID pgtype.UUID, tasks []db.AgentTaskQueue, resp []AgentTaskResponse) {
	if len(tasks) == 0 || len(tasks) != len(resp) {
		return
	}
	taskIDs := make([]pgtype.UUID, 0, len(tasks))
	for i := range tasks {
		if tasks[i].IssueID.Valid {
			taskIDs = append(taskIDs, tasks[i].ID)
		}
	}
	if len(taskIDs) == 0 {
		return
	}
	rows, err := h.Queries.ListTaskSupplementMetadata(ctx, db.ListTaskSupplementMetadataParams{
		WorkspaceID: workspaceID,
		TaskIds:     taskIDs,
	})
	if err != nil {
		return // metadata is optional and must fail closed
	}
	byTask := make(map[string]db.ListTaskSupplementMetadataRow, len(rows))
	for _, row := range rows {
		byTask[uuidToString(row.TaskID)] = row
	}

	userID := requestUserID(r)
	actorType, _ := h.resolveActor(r, userID, uuidToString(workspaceID))
	permission := make(map[string]bool)
	for i, task := range tasks {
		row, ok := byTask[uuidToString(task.ID)]
		if !ok {
			continue
		}
		resp[i].SupplementCapability = row.Capability
		resp[i].SupplementCommentIDs = uuidsToStrings(row.CommentIds)
		if task.Status != "running" || row.Capability != protocol.DaemonCapabilityTaskSupplementV1 || userID == "" || actorType != "member" {
			continue
		}
		agentID := uuidToString(task.AgentID)
		allowed, checked := permission[agentID]
		if !checked {
			agent, loadErr := h.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{
				ID: task.AgentID, WorkspaceID: workspaceID,
			})
			allowed = loadErr == nil && h.canInvokeAgent(ctx, agent, "member", userID, userID, uuidToString(workspaceID))
			permission[agentID] = allowed
		}
		resp[i].CanSupplement = allowed
	}
}

type taskSupplementReceiptResponse struct {
	TaskID        string  `json:"supplement_task_id"`
	Status        string  `json:"supplement_status"`
	FailureReason *string `json:"supplement_failure_reason,omitempty"`
	DeliveredAt   *string `json:"supplement_delivered_at,omitempty"`
}

func supplementReceipt(s db.TaskSupplement) taskSupplementReceiptResponse {
	return taskSupplementReceiptResponse{
		TaskID:        uuidToString(s.TaskID),
		Status:        s.Status,
		FailureReason: textToPtr(s.FailureReason),
		DeliveredAt:   timestampToPtr(s.DeliveredAt),
	}
}

func applySupplementReceipt(resp *CommentResponse, s db.TaskSupplement) {
	receipt := supplementReceipt(s)
	resp.SupplementTaskID = receipt.TaskID
	resp.SupplementStatus = receipt.Status
	resp.SupplementFailureReason = receipt.FailureReason
	resp.SupplementDeliveredAt = receipt.DeliveredAt
}

func (h *Handler) loadTaskSupplementTarget(w http.ResponseWriter, r *http.Request) (db.Issue, db.AgentTaskQueue, pgtype.UUID, bool) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return db.Issue{}, db.AgentTaskQueue{}, pgtype.UUID{}, false
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return db.Issue{}, db.AgentTaskQueue{}, pgtype.UUID{}, false
	}
	if actorType, _ := h.resolveActor(r, userID, uuidToString(issue.WorkspaceID)); actorType != "member" {
		writeError(w, http.StatusForbidden, "only a workspace member may add guidance")
		return db.Issue{}, db.AgentTaskQueue{}, pgtype.UUID{}, false
	}
	authorID, ok := parseUUIDOrBadRequest(w, userID, "user id")
	if !ok {
		return db.Issue{}, db.AgentTaskQueue{}, pgtype.UUID{}, false
	}
	taskID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "taskId"), "task id")
	if !ok {
		return db.Issue{}, db.AgentTaskQueue{}, pgtype.UUID{}, false
	}
	task, err := h.Queries.GetAgentTask(r.Context(), taskID)
	if err != nil || uuidToString(task.IssueID) != uuidToString(issue.ID) {
		writeError(w, http.StatusNotFound, "task not found")
		return db.Issue{}, db.AgentTaskQueue{}, pgtype.UUID{}, false
	}
	agent, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{
		ID: task.AgentID, WorkspaceID: issue.WorkspaceID,
	})
	if err != nil || !h.canInvokeAgent(r.Context(), agent, "member", userID, userID, uuidToString(issue.WorkspaceID)) {
		writeErrorCode(w, http.StatusForbidden, "invocation_not_allowed", "you cannot run this agent")
		return db.Issue{}, db.AgentTaskQueue{}, pgtype.UUID{}, false
	}
	return issue, task, authorID, true
}

type createTaskSupplementRequest struct {
	Content         string `json:"content"`
	ClientRequestID string `json:"client_request_id"`
}

// CreateTaskSupplement adds one ordinary historical comment to one exact run.
// The SQL statement owns the terminal race: when completion wins, no comment,
// binding, or coverage marker is created.
func (h *Handler) CreateTaskSupplement(w http.ResponseWriter, r *http.Request) {
	issue, task, authorID, ok := h.loadTaskSupplementTarget(w, r)
	if !ok {
		return
	}
	var req createTaskSupplementRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8*maxCommentContentBytes)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.Content = sanitizeNullBytes(req.Content)
	if len(req.Content) > maxCommentContentBytes {
		writeError(w, http.StatusBadRequest, "content is too long")
		return
	}
	if strings.TrimSpace(req.Content) == "" {
		writeError(w, http.StatusBadRequest, "content is required")
		return
	}
	requestID, ok := parseUUIDOrBadRequest(w, req.ClientRequestID, "client_request_id")
	if !ok {
		return
	}
	lookup := db.GetTaskSupplementByRequestParams{
		TaskID: task.ID, WorkspaceID: issue.WorkspaceID, AuthorID: authorID, ClientRequestID: requestID,
	}
	if existing, err := h.Queries.GetTaskSupplementByRequest(r.Context(), lookup); err == nil {
		h.notifyTaskSupplementAvailable(task)
		h.writeExistingTaskSupplement(w, r, existing)
		return
	} else if !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "failed to check additional message")
		return
	}
	if task.Status != "running" {
		writeErrorCode(w, http.StatusConflict, "task_supplement_turn_ended", "this run has ended")
		return
	}
	capability, err := h.Queries.GetTaskSupplementCapability(r.Context(), task.ID)
	if errors.Is(err, pgx.ErrNoRows) || capability.Capability != protocol.DaemonCapabilityTaskSupplementV1 {
		writeErrorCode(w, http.StatusPreconditionFailed, "task_supplement_unsupported", "this run does not support additional messages")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to check additional-message support")
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to begin additional message")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	created, err := qtx.CreateTaskSupplement(r.Context(), db.CreateTaskSupplementParams{
		TaskID: task.ID, IssueID: issue.ID, WorkspaceID: issue.WorkspaceID,
		AuthorID: authorID, Content: req.Content, ClientRequestID: requestID,
	})
	if isUniqueViolation(err) {
		_ = tx.Rollback(r.Context())
		if existing, loadErr := h.Queries.GetTaskSupplementByRequest(r.Context(), lookup); loadErr == nil {
			h.notifyTaskSupplementAvailable(task)
			h.writeExistingTaskSupplement(w, r, existing)
			return
		} else if !errors.Is(loadErr, pgx.ErrNoRows) {
			writeError(w, http.StatusInternalServerError, "failed to check additional message")
			return
		}
		writeError(w, http.StatusConflict, "client_request_id is already in use")
		return
	}
	if errors.Is(err, pgx.ErrNoRows) {
		writeErrorCode(w, http.StatusConflict, "task_supplement_turn_ended", "this run has ended")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create additional message")
		return
	}
	comment := db.Comment{
		ID: created.ID, IssueID: created.IssueID, AuthorType: created.AuthorType,
		AuthorID: created.AuthorID, Content: created.Content, Type: created.Type,
		CreatedAt: created.CreatedAt, UpdatedAt: created.UpdatedAt, ParentID: created.ParentID,
		WorkspaceID: created.WorkspaceID, ResolvedAt: created.ResolvedAt,
		ResolvedByType: created.ResolvedByType, ResolvedByID: created.ResolvedByID,
		SourceTaskID: created.SourceTaskID, QuickActionID: created.QuickActionID,
		ViaPluginID: created.ViaPluginID, Revision: created.Revision,
		RecoverySettledAt: created.RecoverySettledAt, DeletedAt: created.DeletedAt,
	}
	resp := commentToResponse(comment, nil, nil)
	resp.IssueRevision = created.IssueRevision
	resp.SupplementTaskID = uuidToString(created.SupplementTaskID)
	resp.SupplementStatus = created.SupplementStatus
	resp.SupplementFailureReason = textToPtr(created.SupplementFailureReason)
	resp.SupplementDeliveredAt = timestampToPtr(created.SupplementDeliveredAt)
	issue.Revision = created.IssueRevision
	event, err := service.RecordDurableEventTx(r.Context(), qtx, buildCommentCreatedEvent(issue, resp, "member", uuidToString(authorID)))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record additional message")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit additional message")
		return
	}
	h.publishEvent(event)
	h.notifyTaskSupplementAvailable(task)
	writeJSON(w, http.StatusCreated, resp)
}

func (h *Handler) notifyTaskSupplementAvailable(task db.AgentTaskQueue) {
	if task.Status != "running" || !task.RuntimeID.Valid {
		return
	}
	if h.DaemonTaskSupplement != nil {
		h.DaemonTaskSupplement.NotifyTaskSupplementAvailable(uuidToString(task.RuntimeID), uuidToString(task.ID))
	} else if h.DaemonHub != nil {
		h.DaemonHub.NotifyTaskSupplementAvailable(uuidToString(task.RuntimeID), uuidToString(task.ID))
	}
}

func (h *Handler) writeExistingTaskSupplement(w http.ResponseWriter, r *http.Request, existing db.TaskSupplement) {
	comment, err := h.Queries.GetCommentInWorkspace(r.Context(), db.GetCommentInWorkspaceParams{
		ID: existing.CommentID, WorkspaceID: existing.WorkspaceID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load additional message")
		return
	}
	resp := commentToResponse(comment, nil, nil)
	applySupplementReceipt(&resp, existing)
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) RetryTaskSupplement(w http.ResponseWriter, r *http.Request) {
	issue, task, _, ok := h.loadTaskSupplementTarget(w, r)
	if !ok {
		return
	}
	commentID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "commentId"), "comment id")
	if !ok {
		return
	}
	tx, beginErr := h.TxStarter.Begin(r.Context())
	if beginErr != nil {
		writeError(w, http.StatusInternalServerError, "failed to begin receipt update")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	row, err := qtx.RetryTaskSupplement(r.Context(), db.RetryTaskSupplementParams{
		CommentID: commentID, WorkspaceID: issue.WorkspaceID, TaskID: task.ID, IssueID: issue.ID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		existing, loadErr := h.Queries.GetTaskSupplementByComment(r.Context(), db.GetTaskSupplementByCommentParams{
			CommentID: commentID, WorkspaceID: issue.WorkspaceID,
		})
		if loadErr == nil && existing.TaskID == task.ID && existing.Status == "pending" {
			_ = tx.Rollback(r.Context())
			h.notifyTaskSupplementAvailable(task)
			writeJSON(w, http.StatusOK, supplementReceipt(existing))
			return
		}
	}
	if errors.Is(err, pgx.ErrNoRows) {
		writeErrorCode(w, http.StatusConflict, "task_supplement_not_retryable", "this message cannot be retried")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to retry additional message")
		return
	}
	event, eventErr := h.recordTaskSupplementUpdate(r.Context(), qtx, row)
	if eventErr != nil {
		writeError(w, http.StatusInternalServerError, "failed to record receipt update")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit receipt update")
		return
	}
	h.publishEvent(event)
	h.notifyTaskSupplementAvailable(task)
	writeJSON(w, http.StatusOK, supplementReceipt(row))
}

type ackTaskSupplementRequest struct {
	Delivered bool   `json:"delivered"`
	Error     string `json:"error,omitempty"`
}

func (h *Handler) ClaimTaskSupplement(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskId")
	if _, ok := h.requireDaemonTaskAccess(w, r, taskID); !ok {
		return
	}
	row, err := h.Queries.ClaimNextTaskSupplement(r.Context(), parseUUID(taskID))
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to claim additional message")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"comment_id": uuidToString(row.CommentID), "author_name": row.AuthorName, "content": row.Content,
	})
}

func (h *Handler) AckTaskSupplement(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskId")
	if _, ok := h.requireDaemonTaskAccess(w, r, taskID); !ok {
		return
	}
	commentID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "commentId"), "comment id")
	if !ok {
		return
	}
	var req ackTaskSupplementRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	tx, beginErr := h.TxStarter.Begin(r.Context())
	if beginErr != nil {
		writeError(w, http.StatusInternalServerError, "failed to begin receipt update")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	var row db.TaskSupplement
	var err error
	if req.Delivered {
		row, err = qtx.AckTaskSupplementDelivered(r.Context(), db.AckTaskSupplementDeliveredParams{
			TaskID: parseUUID(taskID), CommentID: commentID,
		})
	} else {
		reason := stableTaskSupplementFailureReason(req.Error)
		row, err = qtx.AckTaskSupplementFailed(r.Context(), db.AckTaskSupplementFailedParams{
			TaskID: parseUUID(taskID), CommentID: commentID,
			FailureReason: pgtype.Text{String: reason, Valid: true},
		})
	}
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusConflict, "additional message is no longer deliverable")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to acknowledge additional message")
		return
	}
	event, eventErr := h.recordTaskSupplementUpdate(r.Context(), qtx, row)
	if eventErr != nil {
		writeError(w, http.StatusInternalServerError, "failed to record receipt update")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit receipt update")
		return
	}
	h.publishEvent(event)
	writeJSON(w, http.StatusOK, supplementReceipt(row))
}

func stableTaskSupplementFailureReason(reason string) string {
	reason = strings.TrimSpace(sanitizeNullBytes(reason))
	switch reason {
	case protocol.TaskSupplementFailureTurnNotStarted, protocol.TaskSupplementFailureTimeout,
		protocol.TaskSupplementFailureTurnEnded, protocol.TaskSupplementFailureProviderRejected:
		return reason
	default:
		// Provider and Go errors are private daemon diagnostics. Never persist or
		// rebroadcast them to workspace members, and never byte-truncate UTF-8.
		return protocol.TaskSupplementFailureProviderRejected
	}
}

func (h *Handler) recordTaskSupplementUpdate(ctx context.Context, qtx *db.Queries, row db.TaskSupplement) (events.Event, error) {
	comment, err := qtx.GetCommentInWorkspace(ctx, db.GetCommentInWorkspaceParams{ID: row.CommentID, WorkspaceID: row.WorkspaceID})
	if err != nil {
		return events.Event{}, err
	}
	resp := commentToResponse(comment, nil, nil)
	applySupplementReceipt(&resp, row)
	return service.RecordDurableEventTx(ctx, qtx, events.Event{
		Type: protocol.EventCommentUpdated, WorkspaceID: uuidToString(row.WorkspaceID), ActorType: "system",
		IdempotencyKey: fmt.Sprintf("task-supplement:%s:%d:%s", uuidToString(row.CommentID), row.AttemptCount, row.Status),
		StreamKey:      "issue:" + uuidToString(row.IssueID), Payload: map[string]any{"comment": resp},
	})
}
