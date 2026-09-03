package handler

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// buildIssueUpdatedEvent creates the complete snapshot consumed by activity,
// audience, notification and autopilot projections. The revision is part of
// the idempotency key: two legitimate edits of one issue must remain distinct,
// while a retry of the same committed edit returns the existing envelope.
func buildIssueUpdatedEvent(ctx context.Context, h *Handler, updated, previous db.Issue, req UpdateIssueRequest, rawFields map[string]json.RawMessage, actorType, actorID string) events.Event {
	resp := issueToResponse(updated, h.getIssuePrefix(ctx, updated.WorkspaceID))
	assigneeTouched := req.AssigneeType != nil || req.AssigneeID != nil
	if _, ok := rawFields["assignee_type"]; ok {
		assigneeTouched = true
	}
	if _, ok := rawFields["assignee_id"]; ok {
		assigneeTouched = true
	}
	projectTouched := req.ProjectID != nil
	if _, ok := rawFields["project_id"]; ok {
		projectTouched = true
	}
	statusTouched := req.Status != nil
	priorityTouched := req.Priority != nil
	startTouched := req.StartDate != nil
	if _, ok := rawFields["start_date"]; ok {
		startTouched = true
	}
	dueTouched := req.DueDate != nil
	if _, ok := rawFields["due_date"]; ok {
		dueTouched = true
	}
	descriptionTouched := req.Description != nil
	titleTouched := req.Title != nil
	prevDescription := textToPtr(previous.Description)
	prevStart := dateToPtr(previous.StartDate)
	prevDue := dateToPtr(previous.DueDate)
	return events.Event{
		Type:           protocol.EventIssueUpdated,
		IdempotencyKey: "issue:updated:" + util.UUIDToString(updated.ID) + ":" + strconv.FormatInt(updated.Revision, 10),
		StreamKey:      "issue:" + util.UUIDToString(updated.ID),
		WorkspaceID:    util.UUIDToString(updated.WorkspaceID),
		ActorType:      actorType,
		ActorID:        actorID,
		Payload: map[string]any{
			"issue":               resp,
			"assignee_changed":    assigneeTouched && (previous.AssigneeType.String != updated.AssigneeType.String || util.UUIDToString(previous.AssigneeID) != util.UUIDToString(updated.AssigneeID)),
			"status_changed":      statusTouched && previous.Status != updated.Status,
			"priority_changed":    priorityTouched && previous.Priority != updated.Priority,
			"project_changed":     projectTouched && util.UUIDToString(previous.ProjectID) != util.UUIDToString(updated.ProjectID),
			"start_date_changed":  startTouched && datePointersDiffer(prevStart, dateToPtr(updated.StartDate)),
			"due_date_changed":    dueTouched && datePointersDiffer(prevDue, dateToPtr(updated.DueDate)),
			"description_changed": descriptionTouched && optionalTextValuesDiffer(previous.Description, updated.Description),
			"title_changed":       titleTouched && previous.Title != updated.Title,
			"prev_title":          previous.Title,
			"prev_assignee_type":  textToPtr(previous.AssigneeType),
			"prev_assignee_id":    uuidToPtr(previous.AssigneeID),
			"prev_status":         previous.Status,
			"prev_priority":       previous.Priority,
			"prev_start_date":     prevStart,
			"prev_due_date":       prevDue,
			"prev_description":    prevDescription,
			"creator_type":        previous.CreatorType,
			"creator_id":          util.UUIDToString(previous.CreatorID),
		},
	}
}

func datePointersDiffer(a, b *string) bool {
	if a == nil || b == nil {
		return a != b
	}
	return *a != *b
}

func optionalTextValuesDiffer(a, b pgtype.Text) bool {
	if a.Valid != b.Valid {
		return true
	}
	return a.Valid && a.String != b.String
}

func buildIssueDeletedEvent(issue db.Issue, actorType, actorID string) events.Event {
	return events.Event{
		Type:           protocol.EventIssueDeleted,
		IdempotencyKey: "issue:deleted:" + util.UUIDToString(issue.ID),
		StreamKey:      "issue:" + util.UUIDToString(issue.ID),
		WorkspaceID:    util.UUIDToString(issue.WorkspaceID),
		ActorType:      actorType,
		ActorID:        actorID,
		Payload:        map[string]any{"issue_id": util.UUIDToString(issue.ID)},
	}
}

func buildIssueAttachmentsChangedEvent(issue db.Issue, actorType, actorID string) events.Event {
	return events.Event{
		Type:           protocol.EventIssueAttachmentsChanged,
		IdempotencyKey: "issue:attachments:" + util.UUIDToString(issue.ID) + ":" + strconv.FormatInt(issue.Revision, 10),
		StreamKey:      "issue:" + util.UUIDToString(issue.ID),
		WorkspaceID:    util.UUIDToString(issue.WorkspaceID),
		ActorType:      actorType,
		ActorID:        actorID,
		Payload: map[string]any{
			"issue_id":       util.UUIDToString(issue.ID),
			"issue_revision": issue.Revision,
		},
	}
}

func buildIssueDetachedEvent(issue db.Issue, actorType, actorID string) events.Event {
	return events.Event{
		Type:           protocol.EventIssueUpdated,
		IdempotencyKey: "issue:detached:" + util.UUIDToString(issue.ID) + ":" + strconv.FormatInt(issue.Revision, 10),
		StreamKey:      "issue:" + util.UUIDToString(issue.ID),
		WorkspaceID:    util.UUIDToString(issue.WorkspaceID),
		ActorType:      actorType,
		ActorID:        actorID,
		Payload: map[string]any{
			"issue":            issueToResponse(issue, ""),
			"project_changed":  false,
			"parent_changed":   true,
			"status_changed":   false,
			"assignee_changed": false,
		},
	}
}
