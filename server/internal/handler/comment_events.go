package handler

import (
	"strconv"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// buildCommentCreatedEvent is the durable envelope for a newly-created
// comment. The issue stream keeps replies and issue mutations ordered for
// subscribers and activity projections.
func buildCommentCreatedEvent(issue db.Issue, comment CommentResponse, actorType, actorID string) events.Event {
	return events.Event{
		Type:        protocol.EventCommentCreated,
		StreamKey:   "issue:" + uuidToString(issue.ID),
		WorkspaceID: uuidToString(issue.WorkspaceID),
		ActorType:   actorType,
		ActorID:     actorID,
		Payload: map[string]any{
			"comment":             comment,
			"issue_title":         issue.Title,
			"issue_assignee_type": textToPtr(issue.AssigneeType),
			"issue_assignee_id":   uuidToPtr(issue.AssigneeID),
			"issue_status":        issue.Status,
			"issue_revision":      comment.IssueRevision,
		},
	}
}

// buildCommentMutationEvent creates the durable envelope for a comment state
// transition. The snapshot is intentionally built from the row returned by
// the mutation, so realtime clients can update immediately while the outbox
// remains the source of truth after a process restart.
func buildCommentMutationEvent(comment db.Comment, eventType, actorType, actorID string, issueRevision int64) events.Event {
	commentID := util.UUIDToString(comment.ID)
	issueID := util.UUIDToString(comment.IssueID)
	payload := map[string]any{
		"comment":    commentToResponse(comment, nil, nil),
		"comment_id": commentID,
		"issue_id":   issueID,
	}
	if issueRevision > 0 {
		payload["issue_revision"] = issueRevision
	}
	return events.Event{
		Type:           eventType,
		IdempotencyKey: "comment:" + eventType + ":" + commentID + ":" + strconv.FormatInt(comment.Revision, 10),
		StreamKey:      "issue:" + issueID,
		WorkspaceID:    util.UUIDToString(comment.WorkspaceID),
		ActorType:      actorType,
		ActorID:        actorID,
		Payload:        payload,
	}
}

// buildCommentDeletedEvent keeps the deleted row's identity and revision in
// the event because the row is unavailable to consumers after commit.
func buildCommentDeletedEvent(comment db.Comment, actorType, actorID string, issueRevision int64) events.Event {
	event := buildCommentMutationEvent(comment, protocol.EventCommentDeleted, actorType, actorID, issueRevision)
	event.Payload = map[string]any{
		"comment_id": commentIDString(comment),
		"issue_id":   util.UUIDToString(comment.IssueID),
	}
	if issueRevision > 0 {
		event.Payload.(map[string]any)["issue_revision"] = issueRevision
	}
	return event
}

func commentIDString(comment db.Comment) string { return util.UUIDToString(comment.ID) }
