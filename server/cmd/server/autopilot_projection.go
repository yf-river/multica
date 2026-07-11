package main

import (
	"context"
	"fmt"

	"github.com/multica-ai/multica/server/internal/eventoutbox"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func registerDurableAutopilotConsumers(dispatcher *eventoutbox.Dispatcher) error {
	for _, eventType := range []string{
		protocol.EventIssueUpdated,
		protocol.EventTaskCompleted,
		protocol.EventTaskFailed,
		protocol.EventTaskCancelled,
	} {
		if err := dispatcher.Register(eventType, "autopilot_run_projection", consumeAutopilotRunProjection); err != nil {
			return err
		}
	}
	return nil
}

func consumeAutopilotRunProjection(ctx context.Context, queries *db.Queries, event events.Event) ([]events.Event, error) {
	var projected *events.Event
	var err error

	switch event.Type {
	case protocol.EventIssueUpdated:
		payload, ok := decodeIssueEvent(event)
		if !ok {
			return nil, fmt.Errorf("decode autopilot issue projection payload")
		}
		if !payload.StatusChanged || !isAutopilotTerminalIssueStatus(payload.Issue.Status) {
			return nil, nil
		}
		if err := validateIssueProjectionScope(event, payload.Issue); err != nil {
			return nil, err
		}
		issue, exists, loadErr := getIssueForProjection(ctx, queries, payload.Issue)
		if loadErr != nil || !exists {
			return nil, loadErr
		}
		// The event snapshot is authoritative for this ordered stream entry;
		// the row may already have advanced to a later status by delivery time.
		issue.Status = payload.Issue.Status
		projected, err = service.ProjectAutopilotRunFromIssue(ctx, queries, issue)
	case protocol.EventTaskCompleted, protocol.EventTaskFailed, protocol.EventTaskCancelled:
		_, task, exists, loadErr := loadTaskProjectionRow(ctx, queries, event)
		if loadErr != nil || !exists {
			return nil, loadErr
		}
		projected, err = service.ProjectAutopilotRunFromTask(ctx, queries, task)
	default:
		return nil, fmt.Errorf("unsupported autopilot projection event %q", event.Type)
	}
	if err != nil {
		return nil, err
	}
	if projected == nil {
		return nil, nil
	}
	return []events.Event{*projected}, nil
}

func isAutopilotTerminalIssueStatus(status string) bool {
	switch status {
	case "done", "in_review", "cancelled", "blocked":
		return true
	default:
		return false
	}
}
