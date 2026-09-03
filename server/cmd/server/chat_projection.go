package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/eventoutbox"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"github.com/multica-ai/multica/server/pkg/redact"
)

const chatCompletionProjectionConsumer = "chat_completion_projection"

const chatFailureProjectionConsumer = "chat_failure_projection"

func registerDurableChatConsumers(dispatcher *eventoutbox.Dispatcher) error {
	if err := dispatcher.Register(protocol.EventTaskCompleted, chatCompletionProjectionConsumer, consumeChatCompletionProjection); err != nil {
		return err
	}
	return dispatcher.Register(protocol.EventTaskFailed, chatFailureProjectionConsumer, consumeChatFailureProjection)
}

func consumeChatCompletionProjection(ctx context.Context, queries *db.Queries, event events.Event) ([]events.Event, error) {
	task, exists, err := loadChatTerminalTask(ctx, queries, event)
	if err != nil || !exists {
		return nil, err
	}
	message, err := projectCompletedChatMessage(ctx, queries, task)
	if err != nil {
		return nil, err
	}
	chatEvent := completedChatEvent(event, task, message)
	// The chat:done event itself is durable. Channel-specific relays consume
	// that event through their normal listener; enqueueing a second nested event
	// here would duplicate delivery and make retries non-idempotent.
	return []events.Event{chatEvent}, nil
}

func consumeChatFailureProjection(ctx context.Context, queries *db.Queries, event events.Event) ([]events.Event, error) {
	task, exists, err := loadChatTerminalTask(ctx, queries, event)
	if err != nil || !exists {
		return nil, err
	}
	hasRetry, err := queries.HasRetryTaskForParent(ctx, task.ID)
	if err != nil {
		return nil, fmt.Errorf("load chat failure retry decision: %w", err)
	}
	if hasRetry {
		return nil, nil
	}
	message, err := projectFailedChatMessage(ctx, queries, task)
	if err != nil {
		return nil, err
	}
	return []events.Event{failedChatMessageEvent(event, task, message)}, nil
}

func loadChatTerminalTask(
	ctx context.Context,
	queries *db.Queries,
	event events.Event,
) (db.AgentTaskQueue, bool, error) {
	payload, task, exists, err := loadTaskProjectionRow(ctx, queries, event)
	if err != nil || !exists || payload.ChatSessionID == "" {
		return db.AgentTaskQueue{}, false, err
	}
	if !task.ChatSessionID.Valid {
		// Deleting a chat session clears the task FK. Its terminal event is then
		// intentionally a no-op because there is no conversation left to update.
		return db.AgentTaskQueue{}, false, nil
	}
	if util.UUIDToString(task.ChatSessionID) != payload.ChatSessionID {
		return db.AgentTaskQueue{}, false, fmt.Errorf("chat terminal projection session mismatch")
	}
	session, err := queries.GetChatSession(ctx, task.ChatSessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.AgentTaskQueue{}, false, nil
	}
	if err != nil {
		return db.AgentTaskQueue{}, false, fmt.Errorf("load chat terminal session: %w", err)
	}
	if util.UUIDToString(session.WorkspaceID) != event.WorkspaceID {
		return db.AgentTaskQueue{}, false, fmt.Errorf("chat terminal projection workspace mismatch")
	}
	return task, true, nil
}

func projectCompletedChatMessage(ctx context.Context, queries *db.Queries, task db.AgentTaskQueue) (*db.ChatMessage, error) {
	var result protocol.TaskCompletedPayload
	if err := json.Unmarshal(task.Result, &result); err != nil || result.Output == "" {
		// Empty output may still be represented by the synchronous completion
		// path as a no-response row. Check that row before deciding there is no
		// transcript outcome to project.
		message, lookupErr := queries.GetChatMessageByTaskAssistant(ctx, task.ID)
		if errors.Is(lookupErr, pgx.ErrNoRows) {
			return nil, nil
		}
		if lookupErr != nil {
			return nil, fmt.Errorf("load existing completed chat message: %w", lookupErr)
		}
		return &message, nil
	}
	if existing, lookupErr := queries.GetChatMessageByTaskAssistant(ctx, task.ID); lookupErr == nil {
		return &existing, nil
	} else if !errors.Is(lookupErr, pgx.ErrNoRows) {
		return nil, fmt.Errorf("load existing completed chat message: %w", lookupErr)
	}

	message, err := queries.CreateChatMessage(ctx, db.CreateChatMessageParams{
		ID:            dbid.NewV7(),
		ChatSessionID: task.ChatSessionID,
		Role:          "assistant",
		Content:       redact.Text(util.UnescapeBackslashEscapes(result.Output)),
		TaskID:        task.ID,
		ElapsedMs:     service.ComputeChatElapsedMs(task),
	})
	if err != nil {
		return nil, fmt.Errorf("persist completed chat message: %w", err)
	}
	return &message, nil
}

func projectFailedChatMessage(ctx context.Context, queries *db.Queries, task db.AgentTaskQueue) (db.ChatMessage, error) {
	if existing, err := queries.GetChatMessageByTaskAssistant(ctx, task.ID); err == nil {
		return existing, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return db.ChatMessage{}, fmt.Errorf("load existing failed chat message: %w", err)
	}
	message := ""
	if task.Error.Valid {
		message = redact.Text(task.Error.String)
	}
	created, err := queries.CreateChatMessage(ctx, db.CreateChatMessageParams{
		ID:            dbid.NewV7(),
		ChatSessionID: task.ChatSessionID,
		Role:          "assistant",
		Content:       message,
		TaskID:        task.ID,
		FailureReason: task.FailureReason,
		ElapsedMs:     service.ComputeChatElapsedMs(task),
	})
	if err != nil {
		return db.ChatMessage{}, fmt.Errorf("persist failed chat message: %w", err)
	}
	return created, nil
}

func completedChatEvent(event events.Event, task db.AgentTaskQueue, message *db.ChatMessage) events.Event {
	payload := protocol.ChatDonePayload{
		ChatSessionID:       util.UUIDToString(task.ChatSessionID),
		TaskID:              util.UUIDToString(task.ID),
		QuickActionsPending: decodeTaskQuickActionsPending(event),
	}
	if message != nil {
		payload.MessageID = util.UUIDToString(message.ID)
		payload.Content = message.Content
		if message.CreatedAt.Valid {
			payload.CreatedAt = message.CreatedAt.Time.UTC().Format(time.RFC3339Nano)
		}
		if message.ElapsedMs.Valid {
			payload.ElapsedMs = message.ElapsedMs.Int64
		}
	}
	return events.Event{
		Type:          protocol.EventChatDone,
		StreamKey:     "chat:" + util.UUIDToString(task.ChatSessionID),
		WorkspaceID:   event.WorkspaceID,
		ActorType:     "system",
		TaskID:        util.UUIDToString(task.ID),
		ChatSessionID: util.UUIDToString(task.ChatSessionID),
		Payload:       payload,
	}
}

func decodeTaskQuickActionsPending(event events.Event) bool {
	payload, ok := decodeEventPayload[taskEventPayload](event)
	return ok && payload.QuickActionsPending
}

func failedChatMessageEvent(event events.Event, task db.AgentTaskQueue, message db.ChatMessage) events.Event {
	createdAt := ""
	if message.CreatedAt.Valid {
		createdAt = message.CreatedAt.Time.UTC().Format(time.RFC3339Nano)
	}
	return events.Event{
		Type:          protocol.EventChatMessage,
		WorkspaceID:   event.WorkspaceID,
		ActorType:     "system",
		ChatSessionID: util.UUIDToString(task.ChatSessionID),
		Payload: protocol.ChatMessagePayload{
			ChatSessionID: util.UUIDToString(task.ChatSessionID),
			MessageID:     util.UUIDToString(message.ID),
			Role:          message.Role,
			Content:       message.Content,
			TaskID:        util.UUIDToString(task.ID),
			CreatedAt:     createdAt,
		},
	}
}
