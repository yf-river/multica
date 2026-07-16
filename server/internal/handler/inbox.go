package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/logger"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type InboxItemResponse struct {
	ID            string          `json:"id"`
	WorkspaceID   string          `json:"workspace_id"`
	RecipientType string          `json:"recipient_type"`
	RecipientID   string          `json:"recipient_id"`
	Type          string          `json:"type"`
	Severity      string          `json:"severity"`
	IssueID       *string         `json:"issue_id"`
	Title         string          `json:"title"`
	Body          *string         `json:"body"`
	Read          bool            `json:"read"`
	Archived      bool            `json:"archived"`
	CreatedAt     string          `json:"created_at"`
	IssueStatus   *string         `json:"issue_status"`
	ActorType     *string         `json:"actor_type"`
	ActorID       *string         `json:"actor_id"`
	Details       json.RawMessage `json:"details"`
}

func inboxToResponse(i db.InboxItem) InboxItemResponse {
	return InboxItemResponse{
		ID:            uuidToString(i.ID),
		WorkspaceID:   uuidToString(i.WorkspaceID),
		RecipientType: i.RecipientType,
		RecipientID:   uuidToString(i.RecipientID),
		Type:          i.Type,
		Severity:      i.Severity,
		IssueID:       uuidToPtr(i.IssueID),
		Title:         i.Title,
		Body:          textToPtr(i.Body),
		Read:          i.Read,
		Archived:      i.Archived,
		CreatedAt:     timestampToString(i.CreatedAt),
		ActorType:     textToPtr(i.ActorType),
		ActorID:       uuidToPtr(i.ActorID),
		Details:       json.RawMessage(i.Details),
	}
}

func inboxRowToResponse(r db.ListInboxItemsRow) InboxItemResponse {
	resp := inboxToResponse(r.InboxItem)
	resp.IssueStatus = textToPtr(r.IssueStatus)
	return resp
}

func enrichInboxResponse(ctx context.Context, queries *db.Queries, resp InboxItemResponse, issueID pgtype.UUID) (InboxItemResponse, error) {
	if !issueID.Valid {
		return resp, nil
	}
	issue, err := queries.GetIssue(ctx, issueID)
	if errors.Is(err, pgx.ErrNoRows) {
		return resp, nil
	}
	if err != nil {
		return InboxItemResponse{}, err
	}
	s := issue.Status
	resp.IssueStatus = &s
	return resp, nil
}

type inboxRecipientScope struct {
	workspaceID string
	workspace   pgtype.UUID
	userID      string
	recipient   pgtype.UUID
}

func requireInboxRecipientScope(w http.ResponseWriter, r *http.Request) (inboxRecipientScope, bool) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return inboxRecipientScope{}, false
	}
	workspaceID := ctxWorkspaceID(r.Context())
	workspace, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return inboxRecipientScope{}, false
	}
	return inboxRecipientScope{
		workspaceID: workspaceID,
		workspace:   workspace,
		userID:      userID,
		recipient:   parseUUID(userID),
	}, true
}

func (h *Handler) ListInbox(w http.ResponseWriter, r *http.Request) {
	scope, ok := requireInboxRecipientScope(w, r)
	if !ok {
		return
	}

	items, err := h.Queries.ListInboxItems(r.Context(), db.ListInboxItemsParams{
		WorkspaceID:   scope.workspace,
		RecipientType: "member",
		RecipientID:   scope.recipient,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list inbox")
		return
	}

	resp := make([]InboxItemResponse, len(items))
	for i, item := range items {
		resp[i] = inboxRowToResponse(item)
	}

	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) MarkInboxRead(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	prev, ok := h.loadInboxItemForUser(w, r, id)
	if !ok {
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to begin mark read")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	queries := h.Queries.WithTx(tx)
	item, err := queries.MarkInboxRead(r.Context(), prev.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to mark read")
		return
	}

	resp, err := enrichInboxResponse(r.Context(), queries, inboxToResponse(item), item.IssueID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load inbox issue")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit mark read")
		return
	}
	userID := requestUserID(r)
	workspaceID := uuidToString(item.WorkspaceID)
	h.publish(protocol.EventInboxRead, workspaceID, "member", userID, map[string]any{
		"item_id":      uuidToString(item.ID),
		"recipient_id": uuidToString(item.RecipientID),
	})
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) ArchiveInboxItem(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	prev, ok := h.loadInboxItemForUser(w, r, id)
	if !ok {
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to begin archive")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	queries := h.Queries.WithTx(tx)
	item, err := queries.ArchiveInboxItem(r.Context(), prev.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to archive")
		return
	}

	// Archive all sibling inbox items for the same issue (issue-level archive)
	if item.IssueID.Valid {
		if _, err := queries.ArchiveInboxByIssue(r.Context(), db.ArchiveInboxByIssueParams{
			WorkspaceID:   item.WorkspaceID,
			RecipientType: item.RecipientType,
			RecipientID:   item.RecipientID,
			IssueID:       item.IssueID,
		}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to archive related inbox items")
			return
		}
	}

	resp, err := enrichInboxResponse(r.Context(), queries, inboxToResponse(item), item.IssueID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load inbox issue")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit archive")
		return
	}
	userID := requestUserID(r)
	workspaceID := uuidToString(item.WorkspaceID)
	h.publish(protocol.EventInboxArchived, workspaceID, "member", userID, map[string]any{
		"item_id":      uuidToString(item.ID),
		"issue_id":     uuidToPtr(item.IssueID),
		"recipient_id": uuidToString(item.RecipientID),
	})
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) CountUnreadInbox(w http.ResponseWriter, r *http.Request) {
	scope, ok := requireInboxRecipientScope(w, r)
	if !ok {
		return
	}

	count, err := h.Queries.CountUnreadInbox(r.Context(), db.CountUnreadInboxParams{
		WorkspaceID:   scope.workspace,
		RecipientType: "member",
		RecipientID:   scope.recipient,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to count unread inbox")
		return
	}

	writeJSON(w, http.StatusOK, map[string]int64{"count": count})
}

type inboxBatchMutation func(context.Context, pgtype.UUID, pgtype.UUID) (int64, error)

func (h *Handler) mutateInboxBatch(
	w http.ResponseWriter,
	r *http.Request,
	action string,
	event string,
	failure string,
	mutate inboxBatchMutation,
) {
	scope, ok := requireInboxRecipientScope(w, r)
	if !ok {
		return
	}

	count, err := mutate(r.Context(), scope.workspace, scope.recipient)
	if err != nil {
		writeError(w, http.StatusInternalServerError, failure)
		return
	}

	slog.Info("inbox: "+action, append(logger.RequestAttrs(r), "user_id", scope.userID, "count", count)...)
	h.publish(event, scope.workspaceID, "member", scope.userID, map[string]any{
		"recipient_id": scope.userID,
		"count":        count,
	})
	writeJSON(w, http.StatusOK, map[string]any{"count": count})
}

func (h *Handler) MarkAllInboxRead(w http.ResponseWriter, r *http.Request) {
	h.mutateInboxBatch(w, r, "mark all read", protocol.EventInboxBatchRead, "failed to mark all inbox read", func(ctx context.Context, workspaceID, recipientID pgtype.UUID) (int64, error) {
		return h.Queries.MarkAllInboxRead(ctx, db.MarkAllInboxReadParams{
			WorkspaceID: workspaceID,
			RecipientID: recipientID,
		})
	})
}

func (h *Handler) ArchiveAllInbox(w http.ResponseWriter, r *http.Request) {
	h.mutateInboxBatch(w, r, "archive all", protocol.EventInboxBatchArchived, "failed to archive all inbox", func(ctx context.Context, workspaceID, recipientID pgtype.UUID) (int64, error) {
		return h.Queries.ArchiveAllInbox(ctx, db.ArchiveAllInboxParams{
			WorkspaceID: workspaceID,
			RecipientID: recipientID,
		})
	})
}

func (h *Handler) ArchiveAllReadInbox(w http.ResponseWriter, r *http.Request) {
	h.mutateInboxBatch(w, r, "archive all read", protocol.EventInboxBatchArchived, "failed to archive all read inbox", func(ctx context.Context, workspaceID, recipientID pgtype.UUID) (int64, error) {
		return h.Queries.ArchiveAllReadInbox(ctx, db.ArchiveAllReadInboxParams{
			WorkspaceID: workspaceID,
			RecipientID: recipientID,
		})
	})
}

func (h *Handler) ArchiveCompletedInbox(w http.ResponseWriter, r *http.Request) {
	h.mutateInboxBatch(w, r, "archive completed", protocol.EventInboxBatchArchived, "failed to archive completed inbox", func(ctx context.Context, workspaceID, recipientID pgtype.UUID) (int64, error) {
		return h.Queries.ArchiveCompletedInbox(ctx, db.ArchiveCompletedInboxParams{
			WorkspaceID: workspaceID,
			RecipientID: recipientID,
		})
	})
}
