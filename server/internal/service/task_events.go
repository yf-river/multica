package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/eventoutbox"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func (s *TaskService) enqueueTaskEvent(
	ctx context.Context,
	queries *db.Queries,
	eventType string,
	task db.AgentTaskQueue,
) (events.Event, error) {
	event, err := s.buildTaskEvent(ctx, queries, eventType, task)
	if err != nil {
		return events.Event{}, err
	}
	// A terminal task token must not outlive the terminal state that invalidates
	// it. Keeping revocation in the same transaction also makes every terminal
	// producer (daemon result, sweeper and bulk cancellation) follow one path.
	if err := queries.DeleteTaskTokensByTask(ctx, task.ID); err != nil {
		return events.Event{}, fmt.Errorf("revoke terminal task tokens: %w", err)
	}
	return eventoutbox.Enqueue(ctx, queries, event)
}

func (s *TaskService) enqueueTaskEvents(
	ctx context.Context,
	queries *db.Queries,
	eventType string,
	tasks []db.AgentTaskQueue,
) ([]events.Event, error) {
	persisted := make([]events.Event, 0, len(tasks))
	for _, task := range tasks {
		event, err := s.enqueueTaskEvent(ctx, queries, eventType, task)
		if err != nil {
			return nil, err
		}
		persisted = append(persisted, event)
	}
	return persisted, nil
}

// EnqueueCancelledTaskEvents persists cancellation events through a caller's
// existing transaction. The caller must publish/finalize them only after the
// surrounding business transaction commits.
func (s *TaskService) EnqueueCancelledTaskEvents(ctx context.Context, queries *db.Queries, tasks []db.AgentTaskQueue) ([]events.Event, error) {
	return s.enqueueTaskEvents(ctx, queries, protocol.EventTaskCancelled, tasks)
}

func (s *TaskService) publishTaskEvents(persisted []events.Event) {
	for _, event := range persisted {
		s.Bus.Publish(event)
	}
}

func (s *TaskService) buildTaskEvent(
	ctx context.Context,
	queries *db.Queries,
	eventType string,
	task db.AgentTaskQueue,
) (events.Event, error) {
	workspaceID, err := s.resolveTaskWorkspaceID(ctx, queries, task)
	if err != nil {
		return events.Event{}, fmt.Errorf("resolve workspace for task %s: %w", util.UUIDToString(task.ID), err)
	}
	if workspaceID == "" {
		return events.Event{}, fmt.Errorf("task %s has no workspace route", util.UUIDToString(task.ID))
	}

	taskID := util.UUIDToString(task.ID)
	streamKey := "task:" + taskID
	if task.IssueID.Valid {
		streamKey = "issue:" + util.UUIDToString(task.IssueID)
	}
	payload := map[string]any{
		"task_id":  taskID,
		"agent_id": util.UUIDToString(task.AgentID),
		"issue_id": util.UUIDToString(task.IssueID),
		"status":   task.Status,
	}
	if task.ChatSessionID.Valid {
		payload["chat_session_id"] = util.UUIDToString(task.ChatSessionID)
	}
	if task.FailureReason.Valid {
		payload["failure_reason"] = task.FailureReason.String
	}
	return events.Event{
		Type:        eventType,
		StreamKey:   streamKey,
		WorkspaceID: workspaceID,
		ActorType:   "system",
		Payload:     payload,
	}, nil
}

func (s *TaskService) failTasksDurably(
	ctx context.Context,
	mutate func(*db.Queries) ([]db.AgentTaskQueue, error),
) ([]db.AgentTaskQueue, error) {
	var failed []db.AgentTaskQueue
	var persistedEvents []events.Event
	var createdRetries []db.AgentTaskQueue
	var sourceSummaries []issueSourceSummaryProjection
	err := s.runInTx(ctx, func(queries *db.Queries) error {
		var err error
		failed, err = mutate(queries)
		if err != nil {
			return err
		}
		for _, task := range failed {
			child, created, err := s.materializeRetryTask(ctx, queries, task)
			if err != nil {
				return fmt.Errorf("materialize retry for failed task %s: %w", util.UUIDToString(task.ID), err)
			}
			if created {
				createdRetries = append(createdRetries, *child)
			}
		}
		persistedEvents, err = s.enqueueTaskEvents(ctx, queries, protocol.EventTaskFailed, failed)
		if err != nil {
			return err
		}
		for _, task := range failed {
			sc, ok := ParseIssueSourceSummaryContext(task)
			if !ok {
				continue
			}
			projection, err := s.projectIssueSourceSummaryTask(ctx, queries, task, sc, nil)
			if err != nil {
				return fmt.Errorf("project failed issue source summary %s: %w", util.UUIDToString(task.ID), err)
			}
			sourceSummaries = append(sourceSummaries, projection)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.publishTaskEvents(persistedEvents)
	for _, projection := range sourceSummaries {
		s.publishIssueSourceSummaryProjection(ctx, projection)
	}
	for _, retry := range createdRetries {
		s.publishRetryTask(ctx, retry)
	}
	return failed, nil
}

func (s *TaskService) cancelTasksDurably(
	ctx context.Context,
	mutate func(*db.Queries) ([]db.AgentTaskQueue, error),
) ([]db.AgentTaskQueue, []events.Event, error) {
	var cancelled []db.AgentTaskQueue
	var persistedEvents []events.Event
	err := s.runInTx(ctx, func(queries *db.Queries) error {
		var err error
		cancelled, err = mutate(queries)
		if err != nil {
			return err
		}
		persistedEvents, err = s.EnqueueCancelledTaskEvents(ctx, queries, cancelled)
		return err
	})
	if err != nil {
		return nil, nil, err
	}
	return cancelled, persistedEvents, nil
}

// PublishCancelledTasks runs the post-commit trace/metrics/reconciliation work
// and emits the exact events that were persisted with the cancellation.
func (s *TaskService) PublishCancelledTasks(ctx context.Context, cancelled []db.AgentTaskQueue, persistedEvents []events.Event) {
	for _, task := range cancelled {
		s.captureTaskCancelled(ctx, task)
	}
	s.reconcileCancelledTaskAgents(ctx, cancelled)
	s.publishTaskEvents(persistedEvents)
}

func (s *TaskService) reconcileCancelledTaskAgents(ctx context.Context, cancelled []db.AgentTaskQueue) {
	agents := make(map[string]pgtype.UUID)
	for _, task := range cancelled {
		agents[util.UUIDToString(task.AgentID)] = task.AgentID
	}
	for _, agentID := range agents {
		s.ReconcileAgentStatus(ctx, agentID)
	}
}

func (s *TaskService) FailTasksForOfflineRuntimes(ctx context.Context) ([]db.AgentTaskQueue, error) {
	return s.failTasksDurably(ctx, func(queries *db.Queries) ([]db.AgentTaskQueue, error) {
		return queries.FailTasksForOfflineRuntimes(ctx)
	})
}

func (s *TaskService) FailStaleTasks(ctx context.Context, params db.FailStaleTasksParams) ([]db.AgentTaskQueue, error) {
	return s.failTasksDurably(ctx, func(queries *db.Queries) ([]db.AgentTaskQueue, error) {
		return queries.FailStaleTasks(ctx, params)
	})
}

func (s *TaskService) ExpireStaleQueuedTasks(ctx context.Context, params db.ExpireStaleQueuedTasksParams) ([]db.AgentTaskQueue, error) {
	return s.failTasksDurably(ctx, func(queries *db.Queries) ([]db.AgentTaskQueue, error) {
		return queries.ExpireStaleQueuedTasks(ctx, params)
	})
}

func (s *TaskService) RecoverOrphanedTasksForRuntime(ctx context.Context, runtimeID pgtype.UUID) ([]db.AgentTaskQueue, error) {
	return s.failTasksDurably(ctx, func(queries *db.Queries) ([]db.AgentTaskQueue, error) {
		return queries.RecoverOrphanedTasksForRuntime(ctx, runtimeID)
	})
}

func (s *TaskService) resolveTaskWorkspaceID(ctx context.Context, queries *db.Queries, task db.AgentTaskQueue) (string, error) {
	if task.IssueID.Valid {
		issue, err := queries.GetIssue(ctx, task.IssueID)
		if err == nil {
			return util.UUIDToString(issue.WorkspaceID), nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return "", err
		}
	}
	if task.ChatSessionID.Valid {
		session, err := queries.GetChatSession(ctx, task.ChatSessionID)
		if err == nil {
			return util.UUIDToString(session.WorkspaceID), nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return "", err
		}
	}
	if task.AutopilotRunID.Valid {
		run, err := queries.GetAutopilotRun(ctx, task.AutopilotRunID)
		if err == nil {
			autopilot, err := queries.GetAutopilot(ctx, run.AutopilotID)
			if err == nil {
				return util.UUIDToString(autopilot.WorkspaceID), nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return "", err
			}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return "", err
		}
	}
	if quickCreate, ok := ParseQuickCreateContext(task); ok {
		return quickCreate.WorkspaceID, nil
	}

	// Every task belongs to an agent, and every agent belongs to a workspace.
	// This is the authoritative fallback for current task kinds that do not own
	// an issue, chat session, autopilot run, or quick-create context.
	agent, err := queries.GetAgent(ctx, task.AgentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return util.UUIDToString(agent.WorkspaceID), nil
}
