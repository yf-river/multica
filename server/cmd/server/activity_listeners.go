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
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func registerDurableActivityConsumers(dispatcher *eventoutbox.Dispatcher) error {
	if err := dispatcher.Register(protocol.EventActivityCreated, "activity_receipt", consumeActivityCreatedReceipt); err != nil {
		return err
	}
	if err := dispatcher.Register(protocol.EventIssueCreated, "activity_log", consumeIssueCreatedActivity); err != nil {
		return err
	}
	if err := dispatcher.Register(protocol.EventIssueUpdated, "activity_log", consumeIssueUpdatedActivities); err != nil {
		return err
	}
	return registerDurableEvents(dispatcher, "task_issue_projection", consumeTaskTerminalIssueProjection,
		protocol.EventTaskCompleted, protocol.EventTaskFailed, protocol.EventTaskCancelled)
}

// consumeActivityCreatedReceipt acknowledges an activity event whose activity
// row was already written by the originating transaction. The row is a
// durable witness for the post-commit realtime hint; no second activity is
// projected. Deleting the issue before delivery is a valid terminal outcome
// because the activity row is deleted with it.
func consumeActivityCreatedReceipt(ctx context.Context, queries *db.Queries, event events.Event) ([]events.Event, error) {
	if event.Type != protocol.EventActivityCreated {
		return nil, fmt.Errorf("activity-created receipt has unexpected event type %q", event.Type)
	}
	payload, ok := decodeActivityCreatedEvent(event)
	if !ok {
		return nil, fmt.Errorf("decode activity-created receipt payload")
	}
	issueID, err := util.ParseUUID(payload.IssueID)
	if err != nil {
		return nil, fmt.Errorf("activity-created receipt has invalid issue ID: %w", err)
	}
	if _, err := util.ParseUUID(payload.Entry.ID); err != nil {
		return nil, fmt.Errorf("activity-created receipt has invalid activity ID: %w", err)
	}
	workspaceID, err := util.ParseUUID(event.WorkspaceID)
	if err != nil {
		return nil, fmt.Errorf("activity-created receipt has invalid workspace ID: %w", err)
	}
	issue, err := queries.GetIssue(ctx, issueID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load issue for activity-created receipt: %w", err)
	}
	if util.UUIDToString(issue.WorkspaceID) != util.UUIDToString(workspaceID) {
		return nil, fmt.Errorf("activity-created receipt workspace mismatch")
	}
	return nil, nil
}

func consumeIssueCreatedActivity(ctx context.Context, queries *db.Queries, event events.Event) ([]events.Event, error) {
	payload, ok := decodeIssueEvent(event)
	if !ok {
		return nil, fmt.Errorf("decode issue-created activity payload")
	}
	_, exists, err := getIssueForProjection(ctx, queries, payload.Issue)
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
	_, exists, err := getIssueForProjection(ctx, queries, issue)
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
	appendChange(payload.StartDateChanged, "start_date_changed", util.ValueOrEmpty(payload.PrevStartDate), util.ValueOrEmpty(issue.StartDate))
	appendChange(payload.DueDateChanged, "due_date_changed", util.ValueOrEmpty(payload.PrevDueDate), util.ValueOrEmpty(issue.DueDate))
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
		ID:          dbid.NewV7(),
		WorkspaceID: util.MustParseUUID(issue.WorkspaceID),
		IssueID:     util.MustParseUUID(issue.ID),
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

	issue, err := queries.GetIssue(ctx, util.MustParseUUID(issueID))
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
		ID:          dbid.NewV7(),
		WorkspaceID: issue.WorkspaceID,
		IssueID:     util.MustParseUUID(issueID),
		ActorType:   util.StrToText("agent"),
		ActorID:     util.MustParseUUID(payload.AgentID),
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
	return events.Event{
		Type:           protocol.EventActivityCreated,
		IdempotencyKey: "activity:" + util.UUIDToString(activity.ID),
		StreamKey:      "issue:" + util.UUIDToString(activity.IssueID),
		WorkspaceID:    original.WorkspaceID,
		ActorType:      original.ActorType,
		ActorID:        original.ActorID,
		Payload: map[string]any{
			"issue_id": util.UUIDToString(activity.IssueID),
			"entry": map[string]any{
				"type":       "activity",
				"id":         util.UUIDToString(activity.ID),
				"actor_type": actorType,
				"actor_id":   util.UUIDToString(activity.ActorID),
				"action":     activity.Action,
				"details":    json.RawMessage(activity.Details),
				"created_at": util.TimestampToString(activity.CreatedAt),
			},
		},
	}
}
