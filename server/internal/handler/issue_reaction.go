package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/eventoutbox"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/logger"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type IssueReactionResponse struct {
	ID            string `json:"id"`
	IssueID       string `json:"issue_id"`
	ActorType     string `json:"actor_type"`
	ActorID       string `json:"actor_id"`
	Emoji         string `json:"emoji"`
	CreatedAt     string `json:"created_at"`
	IssueRevision *int64 `json:"issue_revision,omitempty"`
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

func addedIssueReactionToResponse(r db.AddIssueReactionRow) IssueReactionResponse {
	response := IssueReactionResponse{
		ID:        uuidToString(r.ID),
		IssueID:   uuidToString(r.IssueID),
		ActorType: r.ActorType,
		ActorID:   uuidToString(r.ActorID),
		Emoji:     r.Emoji,
		CreatedAt: timestampToString(r.CreatedAt),
	}
	if r.IssueRevision > 0 {
		response.IssueRevision = &r.IssueRevision
	}
	return response
}

func (h *Handler) AddIssueReaction(w http.ResponseWriter, r *http.Request) {
	issueID := chi.URLParam(r, "id")
	issue, ok := h.loadIssueForUser(w, r, issueID)
	if !ok {
		return
	}

	userID, ok := requireUserID(w, r)
	if !ok {
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

	workspaceID := uuidToString(issue.WorkspaceID)
	actorType, actorID := h.resolveActor(r, userID, workspaceID)

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		slog.Warn("begin issue reaction transaction failed", append(logger.RequestAttrs(r), "error", err, "issue_id", issueID)...)
		writeError(w, http.StatusInternalServerError, "failed to add reaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	reaction, err := qtx.AddIssueReaction(r.Context(), db.AddIssueReactionParams{
		IssueID:     issue.ID,
		WorkspaceID: issue.WorkspaceID,
		ActorType:   actorType,
		ActorID:     parseUUID(actorID),
		Emoji:       req.Emoji,
	})
	if err != nil {
		slog.Warn("add issue reaction failed", append(logger.RequestAttrs(r), "error", err, "issue_id", issueID)...)
		writeError(w, http.StatusInternalServerError, "failed to add reaction")
		return
	}

	resp := addedIssueReactionToResponse(reaction)
	var persisted events.Event
	if reaction.IssueRevision > 0 {
		event := events.Event{
			Type:           protocol.EventIssueReactionAdded,
			IdempotencyKey: "issue-reaction:added:" + uuidToString(reaction.ID),
			StreamKey:      "issue:" + uuidToString(issue.ID),
			WorkspaceID:    workspaceID,
			ActorType:      actorType,
			ActorID:        actorID,
			Payload: map[string]any{
				"reaction":       resp,
				"issue_id":       uuidToString(issue.ID),
				"issue_title":    issue.Title,
				"issue_status":   issue.Status,
				"creator_type":   issue.CreatorType,
				"creator_id":     uuidToString(issue.CreatorID),
				"issue_revision": reaction.IssueRevision,
			},
		}
		persisted, err = eventoutbox.Enqueue(r.Context(), qtx, event)
		if err != nil {
			slog.Warn("enqueue issue reaction event failed", append(logger.RequestAttrs(r), "error", err, "issue_id", issueID)...)
			writeError(w, http.StatusInternalServerError, "failed to add reaction")
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		slog.Warn("commit issue reaction failed", append(logger.RequestAttrs(r), "error", err, "issue_id", issueID)...)
		writeError(w, http.StatusInternalServerError, "failed to add reaction")
		return
	}
	if persisted.ID != "" {
		h.publishEvent(persisted)
	}
	writeJSON(w, http.StatusCreated, resp)
}

func (h *Handler) RemoveIssueReaction(w http.ResponseWriter, r *http.Request) {
	issueID := chi.URLParam(r, "id")
	issue, ok := h.loadIssueForUser(w, r, issueID)
	if !ok {
		return
	}

	userID, ok := requireUserID(w, r)
	if !ok {
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

	workspaceID := uuidToString(issue.WorkspaceID)
	actorType, actorID := h.resolveActor(r, userID, workspaceID)

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		slog.Warn("begin issue reaction removal transaction failed", append(logger.RequestAttrs(r), "error", err, "issue_id", issueID)...)
		writeError(w, http.StatusInternalServerError, "failed to remove reaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	removed, err := qtx.RemoveIssueReaction(r.Context(), db.RemoveIssueReactionParams{
		IssueID:   issue.ID,
		ActorType: actorType,
		ActorID:   parseUUID(actorID),
		Emoji:     req.Emoji,
	})
	if err != nil {
		slog.Warn("remove issue reaction failed", append(logger.RequestAttrs(r), "error", err, "issue_id", issueID)...)
		writeError(w, http.StatusInternalServerError, "failed to remove reaction")
		return
	}

	var persisted events.Event
	if removed.Changed {
		event := events.Event{
			Type:           protocol.EventIssueReactionRemoved,
			IdempotencyKey: "issue-reaction:removed:" + uuidToString(issue.ID) + ":" + actorType + ":" + actorID + ":" + req.Emoji + ":" + strconv.FormatInt(removed.IssueRevision, 10),
			StreamKey:      "issue:" + uuidToString(issue.ID),
			WorkspaceID:    workspaceID,
			ActorType:      actorType,
			ActorID:        actorID,
			Payload: map[string]any{
				"issue_id":       uuidToString(issue.ID),
				"emoji":          req.Emoji,
				"actor_type":     actorType,
				"actor_id":       actorID,
				"issue_revision": removed.IssueRevision,
			},
		}
		persisted, err = eventoutbox.Enqueue(r.Context(), qtx, event)
		if err != nil {
			slog.Warn("enqueue issue reaction removal event failed", append(logger.RequestAttrs(r), "error", err, "issue_id", issueID)...)
			writeError(w, http.StatusInternalServerError, "failed to remove reaction")
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		slog.Warn("commit issue reaction removal failed", append(logger.RequestAttrs(r), "error", err, "issue_id", issueID)...)
		writeError(w, http.StatusInternalServerError, "failed to remove reaction")
		return
	}
	if persisted.ID != "" {
		h.publishEvent(persisted)
	}
	w.WriteHeader(http.StatusNoContent)
}
