package handler

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// SubscriberResponse is the JSON shape returned for each issue subscriber.
type SubscriberResponse struct {
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

func subscriberToResponse(s db.IssueSubscriber) SubscriberResponse {
	return SubscriberResponse{
		IssueID:   uuidToString(s.IssueID),
		UserType:  s.UserType,
		UserID:    uuidToString(s.UserID),
		Reason:    s.Reason,
		CreatedAt: timestampToString(s.CreatedAt),
	}
}

func (h *Handler) resolveSubscriberTarget(w http.ResponseWriter, r *http.Request, workspaceID string) (subscriberTarget, bool) {
	callerType, callerID := h.resolveActor(r, requestUserID(r), workspaceID)
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
	if !h.isWorkspaceEntity(r.Context(), target.userType, target.userID, workspaceID) {
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

	resp := make([]SubscriberResponse, len(subscribers))
	for i, s := range subscribers {
		resp[i] = subscriberToResponse(s)
	}

	writeJSON(w, http.StatusOK, resp)
}

// SubscribeToIssue subscribes a user to an issue with reason "manual".
// If request body contains user_id, subscribes that user; otherwise subscribes the caller.
func (h *Handler) SubscribeToIssue(w http.ResponseWriter, r *http.Request) {
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

	err := h.Queries.AddIssueSubscriber(r.Context(), db.AddIssueSubscriberParams{
		IssueID:  issue.ID,
		UserType: target.userType,
		UserID:   parseUUID(target.userID),
		Reason:   "manual",
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to subscribe")
		return
	}

	h.publish(protocol.EventSubscriberAdded, workspaceID, target.callerType, target.callerID, map[string]any{
		"issue_id":  issueID,
		"user_type": target.userType,
		"user_id":   target.userID,
		"reason":    "manual",
	})

	writeJSON(w, http.StatusOK, map[string]bool{"subscribed": true})
}

// UnsubscribeFromIssue removes a user's subscription from an issue.
// If request body contains user_id, unsubscribes that user; otherwise unsubscribes the caller.
func (h *Handler) UnsubscribeFromIssue(w http.ResponseWriter, r *http.Request) {
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

	err := h.Queries.RemoveIssueSubscriber(r.Context(), db.RemoveIssueSubscriberParams{
		IssueID:  issue.ID,
		UserType: target.userType,
		UserID:   parseUUID(target.userID),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to unsubscribe")
		return
	}

	h.publish(protocol.EventSubscriberRemoved, workspaceID, target.callerType, target.callerID, map[string]any{
		"issue_id":  issueID,
		"user_type": target.userType,
		"user_id":   target.userID,
	})

	writeJSON(w, http.StatusOK, map[string]bool{"subscribed": false})
}
