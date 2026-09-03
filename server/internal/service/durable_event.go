package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/eventoutbox"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// recordDurableEventTx is the only service-layer entry point for an event that
// describes a committed domain mutation. The caller supplies the transaction
// query handle, so a failure to record the envelope aborts the business
// mutation instead of leaving a state that can never be replayed.
func recordDurableEventTx(ctx context.Context, queries *db.Queries, event events.Event) (events.Event, error) {
	return eventoutbox.Enqueue(ctx, queries, event)
}

// RecordDurableEventTx records an event in the caller's transaction. Handlers
// use this small boundary when the mutation and its notification are owned by
// different packages; keeping the query handle explicit prevents an event from
// being committed after the business write by accident.
func RecordDurableEventTx(ctx context.Context, queries *db.Queries, event events.Event) (events.Event, error) {
	return recordDurableEventTx(ctx, queries, event)
}

func publishCommittedEvent(bus *events.Bus, event events.Event) {
	if bus != nil && event.ID != "" {
		bus.PublishRecovered(event)
	}
}

func publishPersistedTaskEvent(ctx context.Context, queries *db.Queries, bus *events.Bus, eventType string, task db.AgentTaskQueue) error {
	if queries == nil {
		return errors.New("persisted task event: queries are required")
	}
	row, err := queries.GetDomainEventByIdempotencyKey(ctx, "task:"+eventType+":"+util.UUIDToString(task.ID))
	if err != nil {
		return fmt.Errorf("load persisted %s event: %w", eventType, err)
	}
	event := eventoutbox.EventFromRow(row)
	if event.Type != eventType {
		return fmt.Errorf("persisted task event type %q does not match %q", event.Type, eventType)
	}
	publishCommittedEvent(bus, event)
	return nil
}

// PublishCommittedEvent is used by handlers that participate in a larger
// transaction (for example Life cognition output plus task completion). The
// caller must invoke it only after that transaction commits.
func (s *TaskService) PublishCommittedEvent(event events.Event) {
	if s != nil {
		publishCommittedEvent(s.Bus, event)
	}
}

// recordTaskLifecycleEventTx records a task state fact while the caller still
// holds the transaction that changed the task row. The workspace is resolved
// through the same transaction so a concurrent delete cannot produce an event
// that points at a different snapshot.
func recordTaskLifecycleEventTx(ctx context.Context, queries *db.Queries, eventType string, task db.AgentTaskQueue, extra ...map[string]any) (events.Event, error) {
	workspaceID, err := resolveTaskWorkspaceWithQueries(ctx, queries, task)
	if err != nil {
		return events.Event{}, err
	}
	if workspaceID == "" {
		return events.Event{}, fmt.Errorf("task %s has no workspace", util.UUIDToString(task.ID))
	}
	event := taskEvent(eventType, workspaceID, task, extra...)
	event.IdempotencyKey = taskLifecycleIdempotencyKey(eventType, task, event)
	event.StreamKey = "task:" + util.UUIDToString(task.ID)
	return recordDurableEventTx(ctx, queries, event)
}

func taskLifecycleIdempotencyKey(eventType string, task db.AgentTaskQueue, event events.Event) string {
	key := "task:" + eventType + ":" + util.UUIDToString(task.ID)
	// Terminal task events retain their stable key because recovery and
	// receipt code use it to find the committed terminal fact after a
	// transaction has returned. Non-terminal transitions include their
	// transition timestamp below so a later dispatch/start/wait cycle can be
	// represented without collapsing distinct state changes.
	if taskTerminalEventType(eventType) {
		return key
	}
	switch eventType {
	case protocol.EventTaskDispatch:
		if task.DispatchedAt.Valid {
			return key + ":" + task.DispatchedAt.Time.UTC().Format("20060102150405.999999999")
		}
	case protocol.EventTaskRunning:
		if task.StartedAt.Valid {
			return key + ":" + task.StartedAt.Time.UTC().Format("20060102150405.999999999")
		}
	case protocol.EventTaskWaitingLocalDirectory:
		if task.PrepareLeaseExpiresAt.Valid {
			return key + ":" + task.PrepareLeaseExpiresAt.Time.UTC().Format("20060102150405.999999999")
		}
	}
	// A malformed/legacy row may not carry the transition timestamp. The
	// envelope digest still distinguishes different payloads while making a
	// retried identical transition idempotent.
	payload, _ := json.Marshal(event.Payload)
	digest := sha256.Sum256(payload)
	return key + ":" + fmt.Sprintf("%x", digest[:8])
}

// recordTaskTerminalEventTx is the terminal-state spelling used by workers and
// cleanup paths. It intentionally shares the same envelope builder as running
// and waiting transitions so every persisted task lifecycle event has the same
// identity and stream ordering rules.
func recordTaskTerminalEventTx(ctx context.Context, queries *db.Queries, eventType string, task db.AgentTaskQueue, extra ...map[string]any) (events.Event, error) {
	return recordTaskLifecycleEventTx(ctx, queries, eventType, task, extra...)
}

// RecordTaskTerminalEventTx is the shared boundary for background workers that
// settle a task outside TaskService.  The task row and its durable terminal
// event must use the same transaction-scoped query handle.
func RecordTaskTerminalEventTx(ctx context.Context, queries *db.Queries, eventType string, task db.AgentTaskQueue, extra ...map[string]any) (events.Event, error) {
	return recordTaskTerminalEventTx(ctx, queries, eventType, task, extra...)
}

// RecordTaskLifecycleEventTx records a non-terminal task transition such as a
// daemon dispatch, start, or local-directory wait. It is exported for service
// boundaries that perform the state mutation outside TaskService.
func RecordTaskLifecycleEventTx(ctx context.Context, queries *db.Queries, eventType string, task db.AgentTaskQueue, extra ...map[string]any) (events.Event, error) {
	return recordTaskLifecycleEventTx(ctx, queries, eventType, task, extra...)
}

// recordAgentStatusEventTx couples an agent status projection with the row
// update that produced it. Status is an aggregate fact in its own right, so a
// realtime broadcast without this envelope would disappear on restart.
func recordAgentStatusEventTx(ctx context.Context, queries *db.Queries, agent db.Agent) (events.Event, error) {
	event := events.Event{
		Type:           protocol.EventAgentStatus,
		IdempotencyKey: "agent:status:" + util.UUIDToString(agent.ID) + ":" + agent.Status + ":" + agent.UpdatedAt.Time.UTC().Format("20060102150405.999999999"),
		StreamKey:      "agent:" + util.UUIDToString(agent.ID),
		WorkspaceID:    util.UUIDToString(agent.WorkspaceID),
		ActorType:      "system",
		Payload:        map[string]any{"agent": agentToMap(agent)},
	}
	return recordDurableEventTx(ctx, queries, event)
}

// RecordTaskQueuedEventTx records the queue transition in the same
// transaction that creates the task. Background producers such as the Life
// cognition worker do not have a TaskService instance, but they still need
// the same durable lifecycle contract as interactive enqueue paths.
func RecordTaskQueuedEventTx(ctx context.Context, queries *db.Queries, workspaceID string, task db.AgentTaskQueue) (events.Event, error) {
	if strings.TrimSpace(workspaceID) == "" {
		return events.Event{}, errors.New("task queued event: workspace is required")
	}
	event := taskEvent(protocol.EventTaskQueued, workspaceID, task)
	event.IdempotencyKey = "task:queued:" + util.UUIDToString(task.ID)
	event.StreamKey = "task:" + util.UUIDToString(task.ID)
	return recordDurableEventTx(ctx, queries, event)
}

func resolveTaskWorkspaceWithQueries(ctx context.Context, queries *db.Queries, task db.AgentTaskQueue) (string, error) {
	if queries == nil {
		return "", errors.New("task workspace: queries are required")
	}
	if task.IssueID.Valid {
		issue, err := queries.GetIssue(ctx, task.IssueID)
		if err == nil {
			return util.UUIDToString(issue.WorkspaceID), nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return "", fmt.Errorf("load task issue workspace: %w", err)
		}
	}
	if task.ChatSessionID.Valid {
		session, err := queries.GetChatSession(ctx, task.ChatSessionID)
		if err == nil {
			return util.UUIDToString(session.WorkspaceID), nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return "", fmt.Errorf("load task chat workspace: %w", err)
		}
	}
	if task.AutopilotRunID.Valid {
		run, err := queries.GetAutopilotRun(ctx, task.AutopilotRunID)
		if err == nil {
			autopilot, loadErr := queries.GetAutopilot(ctx, run.AutopilotID)
			if loadErr != nil {
				return "", fmt.Errorf("load task autopilot workspace: %w", loadErr)
			}
			return util.UUIDToString(autopilot.WorkspaceID), nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return "", fmt.Errorf("load task autopilot run workspace: %w", err)
		}
	}
	if quickCreate, ok := ParseQuickCreateContext(task); ok {
		if _, err := util.ParseUUID(quickCreate.WorkspaceID); err != nil {
			return "", fmt.Errorf("task quick-create workspace: %w", err)
		}
		return quickCreate.WorkspaceID, nil
	}
	// Tasks created by runtime recovery or an archived-agent cleanup may no
	// longer retain an issue/chat/autopilot link. The agent row is the remaining
	// authoritative workspace owner and is safe to resolve in the same
	// transaction before the terminal event is recorded.
	agent, err := queries.GetAgent(ctx, task.AgentID)
	if err == nil {
		return util.UUIDToString(agent.WorkspaceID), nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return "", fmt.Errorf("load task agent workspace: %w", err)
}

func taskTerminalEventType(eventType string) bool {
	switch eventType {
	case protocol.EventTaskCompleted, protocol.EventTaskFailed, protocol.EventTaskCancelled:
		return true
	default:
		return false
	}
}

func hasPersistedTaskTerminalEvent(ctx context.Context, queries *db.Queries, eventType string, task db.AgentTaskQueue) bool {
	if queries == nil || !taskTerminalEventType(eventType) || !task.ID.Valid {
		return false
	}
	_, err := queries.GetDomainEventByIdempotencyKey(ctx, "task:"+eventType+":"+util.UUIDToString(task.ID))
	return err == nil
}
