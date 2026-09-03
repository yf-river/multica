package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/eventoutbox"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/logger"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type ReactionResponse struct {
	ID              string `json:"id"`
	CommentID       string `json:"comment_id"`
	ActorType       string `json:"actor_type"`
	ActorID         string `json:"actor_id"`
	Emoji           string `json:"emoji"`
	CreatedAt       string `json:"created_at"`
	CommentRevision *int64 `json:"comment_revision,omitempty"`
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

func addedReactionToResponse(r db.AddReactionRow) ReactionResponse {
	response := ReactionResponse{
		ID:        uuidToString(r.ID),
		CommentID: uuidToString(r.CommentID),
		ActorType: r.ActorType,
		ActorID:   uuidToString(r.ActorID),
		Emoji:     r.Emoji,
		CreatedAt: timestampToString(r.CreatedAt),
	}
	if r.CommentRevision > 0 {
		response.CommentRevision = &r.CommentRevision
	}
	return response
}

func (h *Handler) AddReaction(w http.ResponseWriter, r *http.Request) {
	commentId := chi.URLParam(r, "commentId")

	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	workspaceID := h.resolveWorkspaceID(r)
	commentUUID, ok := parseUUIDOrBadRequest(w, commentId, "comment id")
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	comment, err := h.Queries.GetCommentInWorkspace(r.Context(), db.GetCommentInWorkspaceParams{
		ID:          commentUUID,
		WorkspaceID: wsUUID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "comment not found")
		return
	}

	var req struct {
		Emoji string `json:"emoji"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Emoji == "" {
		writeError(w, http.StatusBadRequest, "emoji is required")
		return
	}

	actorType, actorID := h.resolveActor(r, userID, workspaceID)

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		slog.Warn("begin comment reaction transaction failed", append(logger.RequestAttrs(r), "error", err, "comment_id", commentId)...)
		writeError(w, http.StatusInternalServerError, "failed to add reaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	reaction, err := qtx.AddReaction(r.Context(), db.AddReactionParams{
		CommentID:   comment.ID,
		WorkspaceID: wsUUID,
		ActorType:   actorType,
		ActorID:     parseUUID(actorID),
		Emoji:       req.Emoji,
	})
	if err != nil {
		slog.Warn("add reaction failed", append(logger.RequestAttrs(r), "error", err, "comment_id", commentId)...)
		writeError(w, http.StatusInternalServerError, "failed to add reaction")
		return
	}

	resp := addedReactionToResponse(reaction)

	// Load notification context in the same transaction as the mutation.
	issueID := uuidToString(comment.IssueID)
	issue, err := qtx.GetIssueInWorkspace(r.Context(), db.GetIssueInWorkspaceParams{
		ID: comment.IssueID, WorkspaceID: wsUUID,
	})
	if err != nil {
		slog.Warn("load issue for comment reaction failed", append(logger.RequestAttrs(r), "error", err, "comment_id", commentId)...)
		writeError(w, http.StatusInternalServerError, "failed to add reaction")
		return
	}

	var persisted events.Event
	if reaction.CommentRevision > 0 {
		event := events.Event{
			Type:           protocol.EventReactionAdded,
			IdempotencyKey: "reaction:added:" + uuidToString(reaction.ID),
			StreamKey:      "issue:" + issueID,
			WorkspaceID:    workspaceID,
			ActorType:      actorType,
			ActorID:        actorID,
			Payload: map[string]any{
				"reaction":            resp,
				"issue_id":            issueID,
				"issue_title":         issue.Title,
				"issue_status":        issue.Status,
				"comment_id":          uuidToString(comment.ID),
				"comment_author_type": comment.AuthorType,
				"comment_author_id":   uuidToString(comment.AuthorID),
				"comment_revision":    reaction.CommentRevision,
			},
		}
		persisted, err = eventoutbox.Enqueue(r.Context(), qtx, event)
		if err != nil {
			slog.Warn("enqueue comment reaction event failed", append(logger.RequestAttrs(r), "error", err, "comment_id", commentId)...)
			writeError(w, http.StatusInternalServerError, "failed to add reaction")
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		slog.Warn("commit comment reaction failed", append(logger.RequestAttrs(r), "error", err, "comment_id", commentId)...)
		writeError(w, http.StatusInternalServerError, "failed to add reaction")
		return
	}
	if persisted.ID != "" {
		h.publishEvent(persisted)
	}
	writeJSON(w, http.StatusCreated, resp)
}

func (h *Handler) RemoveReaction(w http.ResponseWriter, r *http.Request) {
	commentId := chi.URLParam(r, "commentId")

	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	workspaceID := h.resolveWorkspaceID(r)
	commentUUID, ok := parseUUIDOrBadRequest(w, commentId, "comment id")
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	comment, err := h.Queries.GetCommentInWorkspace(r.Context(), db.GetCommentInWorkspaceParams{
		ID:          commentUUID,
		WorkspaceID: wsUUID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "comment not found")
		return
	}

	var req struct {
		Emoji string `json:"emoji"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Emoji == "" {
		writeError(w, http.StatusBadRequest, "emoji is required")
		return
	}

	actorType, actorID := h.resolveActor(r, userID, workspaceID)

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		slog.Warn("begin comment reaction removal transaction failed", append(logger.RequestAttrs(r), "error", err, "comment_id", commentId)...)
		writeError(w, http.StatusInternalServerError, "failed to remove reaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	removed, err := qtx.RemoveReaction(r.Context(), db.RemoveReactionParams{
		CommentID: comment.ID,
		ActorType: actorType,
		ActorID:   parseUUID(actorID),
		Emoji:     req.Emoji,
	})
	if err != nil {
		slog.Warn("remove reaction failed", append(logger.RequestAttrs(r), "error", err, "comment_id", commentId)...)
		writeError(w, http.StatusInternalServerError, "failed to remove reaction")
		return
	}

	var persisted events.Event
	if removed.Changed {
		event := events.Event{
			Type:           protocol.EventReactionRemoved,
			IdempotencyKey: "reaction:removed:" + uuidToString(comment.ID) + ":" + actorType + ":" + actorID + ":" + req.Emoji + ":" + strconv.FormatInt(removed.CommentRevision, 10),
			StreamKey:      "issue:" + uuidToString(comment.IssueID),
			WorkspaceID:    workspaceID,
			ActorType:      actorType,
			ActorID:        actorID,
			Payload: map[string]any{
				"comment_id":       uuidToString(comment.ID),
				"issue_id":         uuidToString(comment.IssueID),
				"emoji":            req.Emoji,
				"actor_type":       actorType,
				"actor_id":         actorID,
				"comment_revision": removed.CommentRevision,
			},
		}
		persisted, err = eventoutbox.Enqueue(r.Context(), qtx, event)
		if err != nil {
			slog.Warn("enqueue comment reaction removal event failed", append(logger.RequestAttrs(r), "error", err, "comment_id", commentId)...)
			writeError(w, http.StatusInternalServerError, "failed to remove reaction")
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		slog.Warn("commit comment reaction removal failed", append(logger.RequestAttrs(r), "error", err, "comment_id", commentId)...)
		writeError(w, http.StatusInternalServerError, "failed to remove reaction")
		return
	}
	if persisted.ID != "" {
		h.publishEvent(persisted)
	}
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
