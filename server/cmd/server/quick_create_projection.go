package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/eventoutbox"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"github.com/multica-ai/multica/server/pkg/redact"
)

const quickCreateProjectionConsumer = "quick_create_projection"

type quickCreateProjection struct {
	task            db.AgentTaskQueue
	request         service.QuickCreateContext
	workspaceID     pgtype.UUID
	requesterID     pgtype.UUID
	requesterActive bool
}

func registerDurableQuickCreateConsumers(dispatcher *eventoutbox.Dispatcher) error {
	for _, eventType := range []string{protocol.EventTaskCompleted, protocol.EventTaskFailed} {
		if err := dispatcher.Register(eventType, quickCreateProjectionConsumer, consumeQuickCreateTerminalProjection); err != nil {
			return err
		}
	}
	return nil
}

func consumeQuickCreateTerminalProjection(ctx context.Context, queries *db.Queries, event events.Event) ([]events.Event, error) {
	projection, exists, err := loadQuickCreateProjection(ctx, queries, event)
	if err != nil || !exists {
		return nil, err
	}

	switch event.Type {
	case protocol.EventTaskCompleted:
		return projectQuickCreateCompleted(ctx, queries, event, projection)
	case protocol.EventTaskFailed:
		if !projection.requesterActive {
			return nil, nil
		}
		message := ""
		if projection.task.Error.Valid {
			message = projection.task.Error.String
		}
		return projectQuickCreateFailed(ctx, queries, event, projection, message)
	default:
		return nil, fmt.Errorf("unsupported quick-create terminal event %q", event.Type)
	}
}

func loadQuickCreateProjection(
	ctx context.Context,
	queries *db.Queries,
	event events.Event,
) (quickCreateProjection, bool, error) {
	_, task, exists, err := loadTaskProjectionRow(ctx, queries, event)
	if err != nil || !exists {
		return quickCreateProjection{}, exists, err
	}
	quickCreate, ok := service.ParseQuickCreateContext(task)
	if !ok {
		return quickCreateProjection{}, false, nil
	}
	workspaceID, err := util.ParseUUID(quickCreate.WorkspaceID)
	if err != nil {
		return quickCreateProjection{}, false, fmt.Errorf("quick-create projection has invalid workspace ID: %w", err)
	}
	requesterID, err := util.ParseUUID(quickCreate.RequesterID)
	if err != nil {
		return quickCreateProjection{}, false, fmt.Errorf("quick-create projection has invalid requester ID: %w", err)
	}
	if event.WorkspaceID != quickCreate.WorkspaceID {
		return quickCreateProjection{}, false, fmt.Errorf("quick-create projection workspace mismatch")
	}
	agent, err := queries.GetAgent(ctx, task.AgentID)
	if err != nil {
		return quickCreateProjection{}, false, fmt.Errorf("load quick-create agent: %w", err)
	}
	if util.UUIDToString(agent.WorkspaceID) != quickCreate.WorkspaceID {
		return quickCreateProjection{}, false, fmt.Errorf("quick-create agent workspace mismatch")
	}
	projection := quickCreateProjection{
		task:        task,
		request:     quickCreate,
		workspaceID: workspaceID,
		requesterID: requesterID,
	}
	_, err = queries.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{
		UserID:      requesterID,
		WorkspaceID: workspaceID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return projection, true, nil
	}
	if err != nil {
		return quickCreateProjection{}, false, fmt.Errorf("load quick-create requester membership: %w", err)
	}
	projection.requesterActive = true
	return projection, true, nil
}

func projectQuickCreateCompleted(
	ctx context.Context,
	queries *db.Queries,
	event events.Event,
	projection quickCreateProjection,
) ([]events.Event, error) {
	issue, err := queries.GetIssueByOrigin(ctx, db.GetIssueByOriginParams{
		WorkspaceID: projection.workspaceID,
		OriginType:  pgtype.Text{String: "quick_create", Valid: true},
		OriginID:    projection.task.ID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		if !projection.requesterActive {
			return nil, nil
		}
		return projectQuickCreateFailed(ctx, queries, event, projection, "agent finished without creating an issue")
	}
	if err != nil {
		return nil, fmt.Errorf("load quick-created issue: %w", err)
	}
	if err := queries.LinkTaskToIssue(ctx, db.LinkTaskToIssueParams{ID: projection.task.ID, IssueID: issue.ID}); err != nil {
		return nil, fmt.Errorf("link quick-create task to issue: %w", err)
	}
	linked, err := queries.GetAgentTask(ctx, projection.task.ID)
	if err != nil {
		return nil, fmt.Errorf("reload linked quick-create task: %w", err)
	}
	if !linked.IssueID.Valid || util.UUIDToString(linked.IssueID) != util.UUIDToString(issue.ID) {
		return nil, fmt.Errorf("quick-create task link was not applied")
	}
	if !projection.requesterActive {
		return nil, nil
	}
	if err := queries.AddIssueSubscriber(ctx, db.AddIssueSubscriberParams{
		IssueID:  issue.ID,
		UserType: "member",
		UserID:   projection.requesterID,
		Reason:   "creator",
	}); err != nil {
		return nil, fmt.Errorf("subscribe quick-create requester: %w", err)
	}
	workspace, err := queries.GetWorkspace(ctx, projection.workspaceID)
	if err != nil {
		return nil, fmt.Errorf("load quick-create workspace: %w", err)
	}
	details, _ := json.Marshal(map[string]any{
		"task_id":         util.UUIDToString(projection.task.ID),
		"agent_id":        util.UUIDToString(projection.task.AgentID),
		"issue_id":        util.UUIDToString(issue.ID),
		"identifier":      fmt.Sprintf("%s-%d", workspace.IssuePrefix, issue.Number),
		"original_prompt": projection.request.Prompt,
	})
	item, err := queries.CreateInboxItem(ctx, db.CreateInboxItemParams{
		WorkspaceID:   projection.workspaceID,
		RecipientType: "member",
		RecipientID:   projection.requesterID,
		Type:          "quick_create_done",
		Severity:      "info",
		IssueID:       issue.ID,
		Title:         issue.Title,
		ActorType:     pgtype.Text{String: "agent", Valid: true},
		ActorID:       projection.task.AgentID,
		Details:       details,
	})
	if err != nil {
		return nil, fmt.Errorf("create quick-create completion inbox: %w", err)
	}
	return []events.Event{
		quickCreateSubscriberEvent(event, projection, issue),
		quickCreateInboxEvent(event, projection.task, item, issue.Status),
	}, nil
}

func projectQuickCreateFailed(
	ctx context.Context,
	queries *db.Queries,
	event events.Event,
	projection quickCreateProjection,
	message string,
) ([]events.Event, error) {
	message = strings.TrimSpace(message)
	if message == "" {
		message = "Quick create did not finish successfully"
	}
	message = redact.Text(message)
	details, _ := json.Marshal(map[string]any{
		"task_id":         util.UUIDToString(projection.task.ID),
		"agent_id":        util.UUIDToString(projection.task.AgentID),
		"original_prompt": projection.request.Prompt,
		"error":           message,
	})
	item, err := queries.CreateInboxItem(ctx, db.CreateInboxItemParams{
		WorkspaceID:   projection.workspaceID,
		RecipientType: "member",
		RecipientID:   projection.requesterID,
		Type:          "quick_create_failed",
		Severity:      "action_required",
		Title:         "Quick create failed",
		Body:          pgtype.Text{String: message, Valid: true},
		ActorType:     pgtype.Text{String: "agent", Valid: true},
		ActorID:       projection.task.AgentID,
		Details:       details,
	})
	if err != nil {
		return nil, fmt.Errorf("create quick-create failure inbox: %w", err)
	}
	return []events.Event{quickCreateInboxEvent(event, projection.task, item, "")}, nil
}

func quickCreateSubscriberEvent(event events.Event, projection quickCreateProjection, issue db.Issue) events.Event {
	return events.Event{
		Type:        protocol.EventSubscriberAdded,
		WorkspaceID: event.WorkspaceID,
		ActorType:   "agent",
		ActorID:     util.UUIDToString(projection.task.AgentID),
		Payload: map[string]any{
			"issue_id":  util.UUIDToString(issue.ID),
			"user_type": "member",
			"user_id":   projection.request.RequesterID,
			"reason":    "creator",
		},
	}
}

func quickCreateInboxEvent(event events.Event, task db.AgentTaskQueue, item db.InboxItem, issueStatus string) events.Event {
	response := inboxItemToResponse(item)
	response["issue_status"] = issueStatus
	return events.Event{
		Type:        protocol.EventInboxNew,
		WorkspaceID: event.WorkspaceID,
		ActorType:   "agent",
		ActorID:     util.UUIDToString(task.AgentID),
		Payload:     map[string]any{"item": response},
	}
}
