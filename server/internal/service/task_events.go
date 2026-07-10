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
	return eventoutbox.Enqueue(ctx, queries, event)
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
	err := s.runInTx(ctx, func(queries *db.Queries) error {
		var err error
		failed, err = mutate(queries)
		if err != nil {
			return err
		}
		persistedEvents = make([]events.Event, 0, len(failed))
		for _, task := range failed {
			event, err := s.enqueueTaskEvent(ctx, queries, protocol.EventTaskFailed, task)
			if err != nil {
				return err
			}
			persistedEvents = append(persistedEvents, event)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, event := range persistedEvents {
		s.Bus.Publish(event)
	}
	return failed, nil
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
	if quickCreate, ok := s.parseQuickCreateContext(task); ok {
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
