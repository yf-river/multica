package handler

import (
	"context"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/eventoutbox"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type issueUpdateProjection struct {
	issue         db.Issue
	response      IssueResponse
	statusChanged bool
	tasks         issueUpdateTaskProjection
	approval      service.IssueApprovalProjection
	event         events.Event
}

type issueUpdateProjectionFailure struct {
	code    string
	message string
	cause   error
}

func (h *Handler) projectIssueUpdateInTx(
	ctx context.Context,
	r *http.Request,
	queries *db.Queries,
	previous db.Issue,
	issue db.Issue,
	prepared preparedIssueUpdate,
	request UpdateIssueRequest,
	actorType string,
	actorID string,
) (issueUpdateProjection, *issueUpdateProjectionFailure) {
	prefix := h.getIssuePrefix(ctx, issue.WorkspaceID)
	response := issueToResponse(issue, prefix)
	delta := prepared.delta(previous, issue, response, request)
	skipBacklogEnqueue := delta.statusChanged && !delta.assigneeChanged && previous.Status == "backlog" &&
		h.isAssignedAgentRunningOnIssue(ctx, r, actorType, actorID, issue)
	tasks, err := h.reconcileIssueUpdateTasksInTx(
		ctx, queries, previous, issue,
		delta.assigneeChanged, delta.statusChanged, delta.projectChanged,
		skipBacklogEnqueue, actorType, actorID,
	)
	if err != nil {
		return issueUpdateProjection{}, &issueUpdateProjectionFailure{
			code: "task_projection_failed", message: "failed to reconcile issue tasks", cause: err,
		}
	}

	approval := service.IssueApprovalProjection{}
	if delta.statusChanged || delta.projectChanged {
		approval, err = h.IssueService.ReconcileProjectOwnerApprovalInTx(ctx, queries, issue, actorType, parseUUID(actorID))
		if err != nil {
			return issueUpdateProjection{}, &issueUpdateProjectionFailure{
				code: "approval_projection_failed", message: "failed to reconcile project owner approval", cause: err,
			}
		}
	}
	issue = approval.CurrentIssue(issue)
	response = issueToResponse(issue, prefix)
	event, err := eventoutbox.Enqueue(
		ctx,
		queries,
		buildIssueUpdatedEvent(uuidToString(issue.WorkspaceID), actorType, actorID, previous, response, delta.changes),
	)
	if err != nil {
		return issueUpdateProjection{}, &issueUpdateProjectionFailure{
			code: "event_failed", message: "failed to update issue", cause: err,
		}
	}
	return issueUpdateProjection{
		issue:         issue,
		response:      response,
		statusChanged: delta.statusChanged,
		tasks:         tasks,
		approval:      approval,
		event:         event,
	}, nil
}

func (h *Handler) publishIssueUpdateProjection(ctx context.Context, projection issueUpdateProjection, previous db.Issue, actorType, actorID string) {
	h.publishEvent(projection.event)
	h.TaskService.PublishCancelledTasks(ctx, projection.tasks.cancelled, projection.tasks.cancelledEvents)
	for _, task := range projection.tasks.queued {
		if task.IsLeaderTask {
			h.TaskService.PublishMentionTaskEnqueued(ctx, task)
		} else {
			h.TaskService.PublishIssueTaskEnqueued(ctx, task)
		}
	}
	h.IssueService.PublishIssueApprovalProjection(ctx, projection.approval, actorType, actorID)
	if projection.statusChanged {
		h.notifyParentOfChildDone(ctx, previous, projection.issue, actorType, actorID)
	}
}

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
		if agent.ArchivedAt.Valid {
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
