package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func consumeTaskTerminalIssueProjection(ctx context.Context, queries *db.Queries, event events.Event) ([]events.Event, error) {
	payload, exists, err := loadTaskProjection(ctx, queries, event)
	if err != nil || !exists || payload.IssueID == "" {
		return nil, err
	}

	action := ""
	switch event.Type {
	case protocol.EventTaskCompleted:
		action = "task_completed"
	case protocol.EventTaskFailed:
		action = "task_failed"
	case protocol.EventTaskCancelled:
		return nil, nil
	default:
		return nil, fmt.Errorf("unsupported terminal task projection event %q", event.Type)
	}

	emitted, err := projectTaskActivity(ctx, queries, event, payload, action)
	if err != nil {
		return nil, err
	}
	if event.Type == protocol.EventTaskFailed {
		notifications, err := projectTaskFailedNotifications(ctx, queries, event, payload)
		if err != nil {
			return nil, err
		}
		emitted = append(emitted, notifications...)
	}
	return emitted, nil
}

func loadTaskProjection(ctx context.Context, queries *db.Queries, event events.Event) (taskEventPayload, bool, error) {
	payload, _, exists, err := loadTaskProjectionRow(ctx, queries, event)
	return payload, exists, err
}

func loadTaskProjectionRow(ctx context.Context, queries *db.Queries, event events.Event) (taskEventPayload, db.AgentTaskQueue, bool, error) {
	payload, ok := decodeTaskEvent(event)
	if !ok || payload.TaskID == "" {
		return taskEventPayload{}, db.AgentTaskQueue{}, false, fmt.Errorf("decode terminal task projection payload")
	}
	taskID, err := util.ParseUUID(payload.TaskID)
	if err != nil {
		return taskEventPayload{}, db.AgentTaskQueue{}, false, fmt.Errorf("projection event has invalid task ID: %w", err)
	}
	task, err := queries.GetAgentTask(ctx, taskID)
	if errors.Is(err, pgx.ErrNoRows) {
		return payload, db.AgentTaskQueue{}, false, nil
	}
	if err != nil {
		return taskEventPayload{}, db.AgentTaskQueue{}, false, fmt.Errorf("load task before projection: %w", err)
	}
	if payload.AgentID != util.UUIDToString(task.AgentID) || payload.IssueID != util.UUIDToString(task.IssueID) {
		return taskEventPayload{}, db.AgentTaskQueue{}, false, fmt.Errorf("task projection identity mismatch")
	}
	if payload.Status != task.Status {
		return taskEventPayload{}, db.AgentTaskQueue{}, false, fmt.Errorf("task projection status mismatch: event=%s row=%s", payload.Status, task.Status)
	}
	expectedStatus := map[string]string{
		protocol.EventTaskCompleted: "completed",
		protocol.EventTaskFailed:    "failed",
		protocol.EventTaskCancelled: "cancelled",
	}[event.Type]
	if expectedStatus == "" || task.Status != expectedStatus {
		return taskEventPayload{}, db.AgentTaskQueue{}, false, fmt.Errorf("task projection event %s cannot project row status %s", event.Type, task.Status)
	}
	return payload, task, true, nil
}
