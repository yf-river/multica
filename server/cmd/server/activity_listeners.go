package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/eventoutbox"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// registerActivityListeners retains task lifecycle projections until those
// producers move to the durable outbox. Issue activity is registered through
// registerDurableActivityConsumers below.
func registerActivityListeners(bus *events.Bus, queries *db.Queries) {
	ctx := context.Background()

	// task:completed — record "task_completed" activity
	bus.Subscribe(protocol.EventTaskCompleted, func(e events.Event) {
		handleTaskActivity(ctx, bus, queries, e, "task_completed")
	})

	// task:failed — record "task_failed" activity
	bus.Subscribe(protocol.EventTaskFailed, func(e events.Event) {
		handleTaskActivity(ctx, bus, queries, e, "task_failed")
	})
}

func registerDurableActivityConsumers(dispatcher *eventoutbox.Dispatcher) error {
	if err := dispatcher.Register(protocol.EventIssueCreated, "activity_log", consumeIssueCreatedActivity); err != nil {
		return err
	}
	return dispatcher.Register(protocol.EventIssueUpdated, "activity_log", consumeIssueUpdatedActivities)
}

func consumeIssueCreatedActivity(ctx context.Context, queries *db.Queries, event events.Event) ([]events.Event, error) {
	payload, ok := decodeIssueEvent(event)
	if !ok {
		return nil, fmt.Errorf("decode issue-created activity payload")
	}
	exists, err := issueExistsForActivity(ctx, queries, payload.Issue)
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
	exists, err := issueExistsForActivity(ctx, queries, issue)
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

func issueExistsForActivity(ctx context.Context, queries *db.Queries, issue eventIssue) (bool, error) {
	issueID, err := util.ParseUUID(issue.ID)
	if err != nil {
		return false, fmt.Errorf("activity event has invalid issue ID: %w", err)
	}
	workspaceID, err := util.ParseUUID(issue.WorkspaceID)
	if err != nil {
		return false, fmt.Errorf("activity event has invalid workspace ID: %w", err)
	}
	if _, err := queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{
		ID:          issueID,
		WorkspaceID: workspaceID,
	}); errors.Is(err, pgx.ErrNoRows) {
		// The issue was deleted after the primary transaction committed. There
		// is no visible timeline left to project, so completing this consumer is
		// correct; retrying an unavoidable foreign-key failure would poison the
		// queue forever.
		return false, nil
	} else if err != nil {
		return false, fmt.Errorf("load issue before activity projection: %w", err)
	}
	return true, nil
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

// handleTaskActivity records an activity for task:completed or task:failed events.
func handleTaskActivity(ctx context.Context, bus *events.Bus, queries *db.Queries, e events.Event, action string) {
	payload, ok := decodeTaskEvent(e)
	if !ok {
		return
	}
	agentID := payload.AgentID
	issueID := payload.IssueID
	if issueID == "" {
		return
	}

	// Look up issue to get workspace_id
	issue, err := queries.GetIssue(ctx, parseUUID(issueID))
	if err != nil {
		slog.Error("activity: failed to get issue for task event",
			"issue_id", issueID, "action", action, "error", err)
		return
	}

	activity, err := queries.CreateActivity(ctx, db.CreateActivityParams{
		WorkspaceID: issue.WorkspaceID,
		IssueID:     parseUUID(issueID),
		ActorType:   util.StrToText("agent"),
		ActorID:     parseUUID(agentID),
		Action:      action,
		Details:     []byte("{}"),
	})
	if err != nil {
		slog.Error("activity: failed to record task activity",
			"issue_id", issueID, "action", action, "error", err)
		return
	}

	publishActivityEvent(bus, e, activity)
}

// publishActivityEvent sends an activity:created event for WS broadcasting.
// Payload matches frontend ActivityCreatedPayload: { issue_id, entry: TimelineEntry }
func publishActivityEvent(bus *events.Bus, original events.Event, activity db.ActivityLog) {
	bus.Publish(activityCreatedEvent(original, activity))
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
