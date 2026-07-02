package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/logger"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type IssueReactionResponse struct {
	ID        string `json:"id"`
	IssueID   string `json:"issue_id"`
	ActorType string `json:"actor_type"`
	ActorID   string `json:"actor_id"`
	Emoji     string `json:"emoji"`
	CreatedAt string `json:"created_at"`
}

func issueReactionToResponse(r db.IssueReaction) IssueReactionResponse {
	return IssueReactionResponse{
		ID:        uuidToString(r.ID),
		IssueID:   uuidToString(r.IssueID),
		ActorType: r.ActorType,
		ActorID:   uuidToString(r.ActorID),
		Emoji:     r.Emoji,
		CreatedAt: timestampToString(r.CreatedAt),
	}
}

type issueReactionRequest struct {
	issueID     string
	workspaceID string
	issue       db.Issue
	emoji       string
	actorType   string
	actorID     string
}

func (h *Handler) loadIssueReactionRequest(w http.ResponseWriter, r *http.Request) (issueReactionRequest, bool) {
	issueID := chi.URLParam(r, "id")
	issue, ok := h.loadIssueForUser(w, r, issueID)
	if !ok {
		return issueReactionRequest{}, false
	}

	userID, ok := requireUserID(w, r)
	if !ok {
		return issueReactionRequest{}, false
	}

	var req struct {
		Emoji string `json:"emoji"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return issueReactionRequest{}, false
	}
	if req.Emoji == "" {
		writeError(w, http.StatusBadRequest, "emoji is required")
		return issueReactionRequest{}, false
	}

	workspaceID := uuidToString(issue.WorkspaceID)
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	return issueReactionRequest{
		issueID:     issueID,
		workspaceID: workspaceID,
		issue:       issue,
		emoji:       req.Emoji,
		actorType:   actorType,
		actorID:     actorID,
	}, true
}

func (h *Handler) AddIssueReaction(w http.ResponseWriter, r *http.Request) {
	reactionReq, ok := h.loadIssueReactionRequest(w, r)
	if !ok {
		return
	}

	reaction, err := h.Queries.AddIssueReaction(r.Context(), db.AddIssueReactionParams{
		IssueID:     reactionReq.issue.ID,
		WorkspaceID: reactionReq.issue.WorkspaceID,
		ActorType:   reactionReq.actorType,
		ActorID:     parseUUID(reactionReq.actorID),
		Emoji:       reactionReq.emoji,
	})
	if err != nil {
		slog.Warn("add issue reaction failed", append(logger.RequestAttrs(r), "error", err, "issue_id", reactionReq.issueID)...)
		writeError(w, http.StatusInternalServerError, "failed to add reaction")
		return
	}

	resp := issueReactionToResponse(reaction)
	h.publish(protocol.EventIssueReactionAdded, reactionReq.workspaceID, reactionReq.actorType, reactionReq.actorID, map[string]any{
		"reaction":     resp,
		"issue_id":     uuidToString(reactionReq.issue.ID),
		"issue_title":  reactionReq.issue.Title,
		"issue_status": reactionReq.issue.Status,
		"creator_type": reactionReq.issue.CreatorType,
		"creator_id":   uuidToString(reactionReq.issue.CreatorID),
	})
	writeJSON(w, http.StatusCreated, resp)
}

func (h *Handler) RemoveIssueReaction(w http.ResponseWriter, r *http.Request) {
	reactionReq, ok := h.loadIssueReactionRequest(w, r)
	if !ok {
		return
	}

	if err := h.Queries.RemoveIssueReaction(r.Context(), db.RemoveIssueReactionParams{
		IssueID:   reactionReq.issue.ID,
		ActorType: reactionReq.actorType,
		ActorID:   parseUUID(reactionReq.actorID),
		Emoji:     reactionReq.emoji,
	}); err != nil {
		slog.Warn("remove issue reaction failed", append(logger.RequestAttrs(r), "error", err, "issue_id", reactionReq.issueID)...)
		writeError(w, http.StatusInternalServerError, "failed to remove reaction")
		return
	}

	h.publish(protocol.EventIssueReactionRemoved, reactionReq.workspaceID, reactionReq.actorType, reactionReq.actorID, map[string]any{
		"issue_id":   uuidToString(reactionReq.issue.ID),
		"emoji":      reactionReq.emoji,
		"actor_type": reactionReq.actorType,
		"actor_id":   reactionReq.actorID,
	})
	w.WriteHeader(http.StatusNoContent)
}
