package handler

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type issueUpdateTaskProjection struct {
	cancelled       []db.AgentTaskQueue
	cancelledEvents []events.Event
	queued          []db.AgentTaskQueue
}

func (h *Handler) reconcileIssueUpdateTasksInTx(ctx context.Context, queries *db.Queries, prevIssue, issue db.Issue, assigneeChanged, statusChanged, projectChanged, skipBacklogEnqueue bool, actorType, actorID string) (issueUpdateTaskProjection, error) {
	projection := issueUpdateTaskProjection{}
	shouldCancelTasks := assigneeChanged || statusChanged && (issue.Status == "cancelled" || issue.Status == "backlog") || projectChanged && issue.Status == "backlog"
	if shouldCancelTasks {
		cancelled, persistedEvents, err := h.TaskService.CancelTasksForIssueInTx(ctx, queries, issue.ID)
		if err != nil {
			return projection, fmt.Errorf("cancel issue tasks: %w", err)
		}
		projection.cancelled = cancelled
		projection.cancelledEvents = persistedEvents
	}
	if issue.Status == "cancelled" || issue.Status == "done" {
		return projection, nil
	}
	shouldEnqueue := assigneeChanged && issue.Status != "backlog" || statusChanged && !assigneeChanged && prevIssue.Status == "backlog" && !skipBacklogEnqueue
	if !shouldEnqueue {
		return projection, nil
	}
	if issue.AssigneeType.Valid && issue.AssigneeType.String == "agent" && issue.AssigneeID.Valid {
		agent, err := queries.GetAgent(ctx, issue.AssigneeID)
		if err != nil {
			return projection, fmt.Errorf("load assigned agent: %w", err)
		}
		if agent.ArchivedAt.Valid || !agent.RuntimeID.Valid {
			return projection, nil
		}
		task, err := h.TaskService.CreateIssueTaskInTx(ctx, queries, issue, pgtype.UUID{}, false)
		if err != nil {
			return projection, fmt.Errorf("create assigned agent task: %w", err)
		}
		projection.queued = append(projection.queued, task)
		return projection, nil
	}
	if issue.AssigneeType.Valid && issue.AssigneeType.String == "squad" && issue.AssigneeID.Valid {
		task, err := h.createSquadLeaderTaskInTx(ctx, queries, issue, pgtype.UUID{}, actorType, actorID)
		if err != nil {
			return projection, err
		}
		if task != nil {
			projection.queued = append(projection.queued, *task)
		}
	}
	return projection, nil
}

func (h *Handler) publishIssueUpdateTaskProjection(ctx context.Context, projection issueUpdateTaskProjection) {
	h.TaskService.PublishCancelledTasks(ctx, projection.cancelled, projection.cancelledEvents)
	for _, task := range projection.queued {
		if task.IsLeaderTask {
			h.TaskService.PublishMentionTaskEnqueued(ctx, task)
		} else {
			h.TaskService.PublishIssueTaskEnqueued(ctx, task)
		}
	}
}
