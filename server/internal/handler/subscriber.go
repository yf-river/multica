package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// subscriberResponse is the JSON shape returned for each issue subscriber.
type subscriberResponse struct {
	IssueID   string `json:"issue_id"`
	UserType  string `json:"user_type"`
	UserID    string `json:"user_id"`
	Reason    string `json:"reason"`
	CreatedAt string `json:"created_at"`
}

type subscriberTarget struct {
	callerType string
	callerID   string
	userType   string
	userID     string
}

func subscriberToResponse(s db.IssueSubscriber) subscriberResponse {
	return subscriberResponse{
		IssueID:   uuidToString(s.IssueID),
		UserType:  s.UserType,
		UserID:    uuidToString(s.UserID),
		Reason:    s.Reason,
		CreatedAt: timestampToString(s.CreatedAt),
	}
}

func (h *Handler) resolveSubscriberTarget(w http.ResponseWriter, r *http.Request, workspaceID string) (subscriberTarget, bool) {
	callerType, callerID := resolveActor(r, requestUserID(r))
	target := subscriberTarget{
		callerType: callerType,
		callerID:   callerID,
		userType:   callerType,
		userID:     callerID,
	}
	var req struct {
		UserID   *string `json:"user_id"`
		UserType *string `json:"user_type"`
	}
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return subscriberTarget{}, false
		}
	}
	if req.UserID != nil && *req.UserID != "" {
		target.userID = *req.UserID
	}
	if req.UserType != nil && *req.UserType != "" {
		target.userType = *req.UserType
	}
	exists, err := h.workspaceEntity(r.Context(), target.userType, target.userID, workspaceID)
	if err != nil {
		writeWorkspaceEntityLookupError(w, r, err)
		return subscriberTarget{}, false
	}
	if !exists {
		writeError(w, http.StatusForbidden, "target user is not a member of this workspace")
		return subscriberTarget{}, false
	}
	return target, true
}

// ListIssueSubscribers returns all subscribers for an issue.
func (h *Handler) ListIssueSubscribers(w http.ResponseWriter, r *http.Request) {
	issueID := chi.URLParam(r, "id")
	issue, ok := h.loadIssueForUser(w, r, issueID)
	if !ok {
		return
	}

	subscribers, err := h.Queries.ListIssueSubscribers(r.Context(), issue.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list subscribers")
		return
	}

	resp := make([]subscriberResponse, len(subscribers))
	for i, s := range subscribers {
		resp[i] = subscriberToResponse(s)
	}

	writeJSON(w, http.StatusOK, resp)
}

type issueSubscriberMutation func(context.Context, db.Issue, subscriberTarget) error

func (h *Handler) mutateIssueSubscriber(
	w http.ResponseWriter,
	r *http.Request,
	event, failure string,
	subscribed bool,
	mutate issueSubscriberMutation,
) {
	issueID := chi.URLParam(r, "id")
	issue, ok := h.loadIssueForUser(w, r, issueID)
	if !ok {
		return
	}

	workspaceID := uuidToString(issue.WorkspaceID)
	target, ok := h.resolveSubscriberTarget(w, r, workspaceID)
	if !ok {
		return
	}

	if err := mutate(r.Context(), issue, target); err != nil {
		writeError(w, http.StatusInternalServerError, failure)
		return
	}

	payload := map[string]any{
		"issue_id":  issueID,
		"user_type": target.userType,
		"user_id":   target.userID,
	}
	if subscribed {
		payload["reason"] = "manual"
	}
	h.publish(event, workspaceID, target.callerType, target.callerID, payload)
	writeJSON(w, http.StatusOK, map[string]bool{"subscribed": subscribed})
}

func (h *Handler) SubscribeToIssue(w http.ResponseWriter, r *http.Request) {
	h.mutateIssueSubscriber(w, r, protocol.EventSubscriberAdded, "failed to subscribe", true, func(ctx context.Context, issue db.Issue, target subscriberTarget) error {
		return h.Queries.AddIssueSubscriber(ctx, db.AddIssueSubscriberParams{
			IssueID: issue.ID, UserType: target.userType, UserID: parseUUID(target.userID), Reason: "manual",
		})
	})
}

func (h *Handler) UnsubscribeFromIssue(w http.ResponseWriter, r *http.Request) {
	h.mutateIssueSubscriber(w, r, protocol.EventSubscriberRemoved, "failed to unsubscribe", false, func(ctx context.Context, issue db.Issue, target subscriberTarget) error {
		return h.Queries.RemoveIssueSubscriber(ctx, db.RemoveIssueSubscriberParams{
			IssueID: issue.ID, UserType: target.userType, UserID: parseUUID(target.userID),
		})
	})
}
