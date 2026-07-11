package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/eventoutbox"
	"github.com/multica-ai/multica/server/internal/logger"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type ReactionResponse struct {
	ID        string `json:"id"`
	CommentID string `json:"comment_id"`
	ActorType string `json:"actor_type"`
	ActorID   string `json:"actor_id"`
	Emoji     string `json:"emoji"`
	CreatedAt string `json:"created_at"`
}

func reactionToResponse(r db.CommentReaction) ReactionResponse {
	return ReactionResponse{
		ID:        uuidToString(r.ID),
		CommentID: uuidToString(r.CommentID),
		ActorType: r.ActorType,
		ActorID:   uuidToString(r.ActorID),
		Emoji:     r.Emoji,
		CreatedAt: timestampToString(r.CreatedAt),
	}
}

type reactionRequest struct {
	commentID   string
	workspaceID string
	workspace   pgtype.UUID
	comment     db.Comment
	emoji       string
	actorType   string
	actorID     string
}

func (h *Handler) loadReactionRequest(w http.ResponseWriter, r *http.Request) (reactionRequest, bool) {
	commentID := chi.URLParam(r, "commentId")
	userID, ok := requireUserID(w, r)
	if !ok {
		return reactionRequest{}, false
	}
	workspaceID := h.resolveWorkspaceID(r)
	commentUUID, ok := parseUUIDOrBadRequest(w, commentID, "comment id")
	if !ok {
		return reactionRequest{}, false
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return reactionRequest{}, false
	}
	comment, err := h.Queries.GetCommentInWorkspace(r.Context(), db.GetCommentInWorkspaceParams{
		ID:          commentUUID,
		WorkspaceID: wsUUID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "comment not found")
		return reactionRequest{}, false
	}
	var req struct {
		Emoji string `json:"emoji"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return reactionRequest{}, false
	}
	if req.Emoji == "" {
		writeError(w, http.StatusBadRequest, "emoji is required")
		return reactionRequest{}, false
	}
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	return reactionRequest{
		commentID:   commentID,
		workspaceID: workspaceID,
		workspace:   wsUUID,
		comment:     comment,
		emoji:       req.Emoji,
		actorType:   actorType,
		actorID:     actorID,
	}, true
}

func (h *Handler) AddReaction(w http.ResponseWriter, r *http.Request) {
	reactionReq, ok := h.loadReactionRequest(w, r)
	if !ok {
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		slog.Warn("begin comment reaction transaction failed", append(logger.RequestAttrs(r), "error", err, "comment_id", reactionReq.commentID)...)
		writeError(w, http.StatusInternalServerError, "failed to add reaction")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	queries := h.Queries.WithTx(tx)
	reaction, err := queries.AddReaction(r.Context(), db.AddReactionParams{
		CommentID:   reactionReq.comment.ID,
		WorkspaceID: reactionReq.workspace,
		ActorType:   reactionReq.actorType,
		ActorID:     parseUUID(reactionReq.actorID),
		Emoji:       reactionReq.emoji,
	})
	if err != nil {
		slog.Warn("add reaction failed", append(logger.RequestAttrs(r), "error", err, "comment_id", reactionReq.commentID)...)
		writeError(w, http.StatusInternalServerError, "failed to add reaction")
		return
	}

	resp := reactionToResponse(reaction)

	// Load the issue inside the same transaction so the event cannot carry a
	// partial notification context after a concurrent delete or database error.
	issueID := uuidToString(reactionReq.comment.IssueID)
	issue, err := queries.GetIssueInWorkspace(r.Context(), db.GetIssueInWorkspaceParams{
		ID:          reactionReq.comment.IssueID,
		WorkspaceID: reactionReq.workspace,
	})
	if err != nil {
		slog.Warn("load issue for comment reaction failed", append(logger.RequestAttrs(r), "error", err, "comment_id", reactionReq.commentID)...)
		writeError(w, http.StatusInternalServerError, "failed to add reaction")
		return
	}

	event := domainEvent(protocol.EventReactionAdded, reactionReq.workspaceID, reactionReq.actorType, reactionReq.actorID, map[string]any{
		"reaction":            resp,
		"issue_id":            issueID,
		"issue_title":         issue.Title,
		"issue_status":        issue.Status,
		"comment_id":          uuidToString(reactionReq.comment.ID),
		"comment_author_type": reactionReq.comment.AuthorType,
		"comment_author_id":   uuidToString(reactionReq.comment.AuthorID),
	})
	event.StreamKey = "issue:" + issueID
	event, err = eventoutbox.Enqueue(r.Context(), queries, event)
	if err != nil {
		slog.Warn("enqueue comment reaction event failed", append(logger.RequestAttrs(r), "error", err, "comment_id", reactionReq.commentID)...)
		writeError(w, http.StatusInternalServerError, "failed to add reaction")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		slog.Warn("commit comment reaction failed", append(logger.RequestAttrs(r), "error", err, "comment_id", reactionReq.commentID)...)
		writeError(w, http.StatusInternalServerError, "failed to add reaction")
		return
	}
	h.publishEvent(event)
	writeJSON(w, http.StatusCreated, resp)
}

func (h *Handler) RemoveReaction(w http.ResponseWriter, r *http.Request) {
	reactionReq, ok := h.loadReactionRequest(w, r)
	if !ok {
		return
	}

	if err := h.Queries.RemoveReaction(r.Context(), db.RemoveReactionParams{
		CommentID: reactionReq.comment.ID,
		ActorType: reactionReq.actorType,
		ActorID:   parseUUID(reactionReq.actorID),
		Emoji:     reactionReq.emoji,
	}); err != nil {
		slog.Warn("remove reaction failed", append(logger.RequestAttrs(r), "error", err, "comment_id", reactionReq.commentID)...)
		writeError(w, http.StatusInternalServerError, "failed to remove reaction")
		return
	}

	h.publish(protocol.EventReactionRemoved, reactionReq.workspaceID, reactionReq.actorType, reactionReq.actorID, map[string]any{
		"comment_id": uuidToString(reactionReq.comment.ID),
		"issue_id":   uuidToString(reactionReq.comment.IssueID),
		"emoji":      reactionReq.emoji,
		"actor_type": reactionReq.actorType,
		"actor_id":   reactionReq.actorID,
	})
	w.WriteHeader(http.StatusNoContent)
}

// groupReactions fetches reactions for the given comment IDs and groups them by comment_id.
func (h *Handler) groupReactions(r *http.Request, commentIDs []pgtype.UUID) map[string][]ReactionResponse {
	if len(commentIDs) == 0 {
		return nil
	}
	reactions, err := h.Queries.ListReactionsByCommentIDs(r.Context(), commentIDs)
	if err != nil {
		return nil
	}
	grouped := make(map[string][]ReactionResponse, len(commentIDs))
	for _, rx := range reactions {
		cid := uuidToString(rx.CommentID)
		grouped[cid] = append(grouped[cid], reactionToResponse(rx))
	}
	return grouped
}
