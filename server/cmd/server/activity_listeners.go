package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/eventoutbox"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func registerDurableActivityConsumers(dispatcher *eventoutbox.Dispatcher) error {
	if err := dispatcher.Register(protocol.EventIssueCreated, "activity_log", consumeIssueCreatedActivity); err != nil {
		return err
	}
	if err := dispatcher.Register(protocol.EventIssueUpdated, "activity_log", consumeIssueUpdatedActivities); err != nil {
		return err
	}
	if err := dispatcher.Register(protocol.EventTaskCompleted, "task_issue_projection", consumeTaskTerminalIssueProjection); err != nil {
		return err
	}
	if err := dispatcher.Register(protocol.EventTaskFailed, "task_issue_projection", consumeTaskTerminalIssueProjection); err != nil {
		return err
	}
	return dispatcher.Register(protocol.EventTaskCancelled, "task_issue_projection", consumeTaskTerminalIssueProjection)
}

func consumeIssueCreatedActivity(ctx context.Context, queries *db.Queries, event events.Event) ([]events.Event, error) {
	payload, ok := decodeIssueEvent(event)
	if !ok {
		return nil, fmt.Errorf("decode issue-created activity payload")
	}
	exists, err := issueExistsForProjection(ctx, queries, payload.Issue)
	if err != nil || !exists {
		return nil, err
	}
	created, err := createIssueActivity(ctx, queries, event, payload.Issue, "created", []byte("{}"))
	if err != nil {
		return nil, err
	}
	return []events.Event{created}, nil
}

type activitySpec struct {
	action  string
	details []byte
}

func consumeIssueUpdatedActivities(ctx context.Context, queries *db.Queries, event events.Event) ([]events.Event, error) {
	payload, ok := decodeIssueEvent(event)
	if !ok {
		return nil, fmt.Errorf("decode issue-updated activity payload")
	}
	issue := payload.Issue
	exists, err := issueExistsForProjection(ctx, queries, issue)
	if err != nil || !exists {
		return nil, err
	}
	specs := make([]activitySpec, 0, 7)
	appendChange := func(changed bool, action string, from, to string) {
		if !changed {
			return
		}
		details, _ := json.Marshal(map[string]string{"from": from, "to": to})
		specs = append(specs, activitySpec{action: action, details: details})
	}
	appendChange(payload.StatusChanged, "status_changed", payload.PrevStatus, issue.Status)
	appendChange(payload.PriorityChanged, "priority_changed", payload.PrevPriority, issue.Priority)
	appendChange(payload.StartDateChanged, "start_date_changed", valueOrEmpty(payload.PrevStartDate), valueOrEmpty(issue.StartDate))
	appendChange(payload.DueDateChanged, "due_date_changed", valueOrEmpty(payload.PrevDueDate), valueOrEmpty(issue.DueDate))
	appendChange(payload.TitleChanged, "title_changed", payload.PrevTitle, issue.Title)
	if payload.AssigneeChanged {
		details := map[string]string{}
		setOptionalDetail(details, "from_type", payload.PrevAssigneeType)
		setOptionalDetail(details, "from_id", payload.PrevAssigneeID)
		setOptionalDetail(details, "to_type", issue.AssigneeType)
		setOptionalDetail(details, "to_id", issue.AssigneeID)
		raw, _ := json.Marshal(details)
		specs = append(specs, activitySpec{action: "assignee_changed", details: raw})
	}
	if payload.DescriptionChanged {
		specs = append(specs, activitySpec{action: "description_updated", details: []byte("{}")})
	}

	emitted := make([]events.Event, 0, len(specs))
	for _, spec := range specs {
		created, err := createIssueActivity(ctx, queries, event, issue, spec.action, spec.details)
		if err != nil {
			return nil, err
		}
		emitted = append(emitted, created)
	}
	return emitted, nil
}

func createIssueActivity(ctx context.Context, queries *db.Queries, event events.Event, issue eventIssue, action string, details []byte) (events.Event, error) {
	activity, err := queries.CreateActivity(ctx, db.CreateActivityParams{
		WorkspaceID: parseUUID(issue.WorkspaceID),
		IssueID:     parseUUID(issue.ID),
		ActorType:   util.StrToText(event.ActorType),
		ActorID:     optionalUUID(event.ActorID),
		Action:      action,
		Details:     details,
	})
	if err != nil {
		return events.Event{}, fmt.Errorf("record %s activity for issue %s: %w", action, issue.ID, err)
	}
	return activityCreatedEvent(event, activity), nil
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func setOptionalDetail(details map[string]string, key string, value *string) {
	if value != nil {
		details[key] = *value
	}
}

func projectTaskActivity(ctx context.Context, queries *db.Queries, event events.Event, payload taskEventPayload, action string) ([]events.Event, error) {
	issueID := payload.IssueID
	if issueID == "" {
		return nil, nil
	}

	issue, err := queries.GetIssue(ctx, parseUUID(issueID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load issue %s for %s activity: %w", issueID, action, err)
	}
	if util.UUIDToString(issue.WorkspaceID) != event.WorkspaceID {
		return nil, fmt.Errorf("task activity workspace mismatch")
	}

	activity, err := queries.CreateActivity(ctx, db.CreateActivityParams{
		WorkspaceID: issue.WorkspaceID,
		IssueID:     parseUUID(issueID),
		ActorType:   util.StrToText("agent"),
		ActorID:     parseUUID(payload.AgentID),
		Action:      action,
		Details:     []byte("{}"),
	})
	if err != nil {
		return nil, fmt.Errorf("record %s activity for issue %s: %w", action, issueID, err)
	}
	return []events.Event{activityCreatedEvent(event, activity)}, nil
}

func activityCreatedEvent(original events.Event, activity db.ActivityLog) events.Event {
	actorType := ""
	if activity.ActorType.Valid {
		actorType = activity.ActorType.String
	}
	action := activity.Action
	return events.Event{
		Type:        protocol.EventActivityCreated,
		WorkspaceID: original.WorkspaceID,
		ActorType:   original.ActorType,
		ActorID:     original.ActorID,
		Payload: map[string]any{
			"issue_id": util.UUIDToString(activity.IssueID),
			"entry": map[string]any{
				"type":       "activity",
				"id":         util.UUIDToString(activity.ID),
				"actor_type": actorType,
				"actor_id":   util.UUIDToString(activity.ActorID),
				"action":     action,
				"details":    json.RawMessage(activity.Details),
				"created_at": util.TimestampToString(activity.CreatedAt),
			},
		},
	}
}
