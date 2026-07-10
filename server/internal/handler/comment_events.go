package handler

import (
	"github.com/multica-ai/multica/server/internal/events"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func buildCommentCreatedEvent(issue db.Issue, comment CommentResponse, actorType, actorID string) events.Event {
	event := domainEvent(protocol.EventCommentCreated, uuidToString(issue.WorkspaceID), actorType, actorID, map[string]any{
		"comment":             comment,
		"issue_title":         issue.Title,
		"issue_assignee_type": textToPtr(issue.AssigneeType),
		"issue_assignee_id":   uuidToPtr(issue.AssigneeID),
		"issue_status":        issue.Status,
	})
	event.StreamKey = "issue:" + uuidToString(issue.ID)
	return event
}
