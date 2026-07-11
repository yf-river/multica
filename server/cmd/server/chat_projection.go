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
	"github.com/multica-ai/multica/server/pkg/protocol"
	"github.com/multica-ai/multica/server/pkg/redact"
)

const chatCompletionProjectionConsumer = "chat_completion_projection"

const chatFailureProjectionConsumer = "chat_failure_projection"

type chatTerminalProjection struct {
	task db.AgentTaskQueue
}

func registerDurableChatConsumers(dispatcher *eventoutbox.Dispatcher) error {
	if err := dispatcher.Register(protocol.EventTaskCompleted, chatCompletionProjectionConsumer, consumeChatCompletionProjection); err != nil {
		return err
	}
	return dispatcher.Register(protocol.EventTaskFailed, chatFailureProjectionConsumer, consumeChatFailureProjection)
}

func consumeChatCompletionProjection(ctx context.Context, queries *db.Queries, event events.Event) ([]events.Event, error) {
	projection, exists, err := loadChatTerminalProjection(ctx, queries, event)
	if err != nil || !exists {
		return nil, err
	}
	message, err := projectCompletedChatMessage(ctx, queries, projection.task)
	if err != nil {
		return nil, err
	}
	chatEvent := completedChatEvent(event, projection.task, message)
	if message != nil && message.Content != "" {
		_, bindingErr := queries.GetLarkChatSessionBindingBySession(ctx, projection.task.ChatSessionID)
		switch {
		case bindingErr == nil:
			if _, err := eventoutbox.Enqueue(ctx, queries, chatEvent); err != nil {
				return nil, fmt.Errorf("enqueue durable Lark chat reply: %w", err)
			}
		case errors.Is(bindingErr, pgx.ErrNoRows):
			// Web/Desktop-only session: realtime delivery is sufficient.
		default:
			return nil, fmt.Errorf("lookup Lark chat binding for reply: %w", bindingErr)
		}
	}
	return []events.Event{chatEvent}, nil
}

func consumeChatFailureProjection(ctx context.Context, queries *db.Queries, event events.Event) ([]events.Event, error) {
	projection, exists, err := loadChatTerminalProjection(ctx, queries, event)
	if err != nil || !exists {
		return nil, err
	}
	_, err = queries.GetRetryTaskForParent(ctx, projection.task.ID)
	if err == nil {
		return nil, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("load chat failure retry decision: %w", err)
	}
	message, err := projectFailedChatMessage(ctx, queries, projection.task)
	if err != nil {
		return nil, err
	}
	return []events.Event{failedChatMessageEvent(event, projection.task, message)}, nil
}

func loadChatTerminalProjection(
	ctx context.Context,
	queries *db.Queries,
	event events.Event,
) (chatTerminalProjection, bool, error) {
	payload, task, exists, err := loadTaskProjectionRow(ctx, queries, event)
	if err != nil || !exists || payload.ChatSessionID == "" {
		return chatTerminalProjection{}, false, err
	}
	if !task.ChatSessionID.Valid {
		// Deleting a chat session clears the task FK. Its terminal event is then
		// intentionally a no-op because there is no conversation left to update.
		return chatTerminalProjection{}, false, nil
	}
	if util.UUIDToString(task.ChatSessionID) != payload.ChatSessionID {
		return chatTerminalProjection{}, false, fmt.Errorf("chat terminal projection session mismatch")
	}
	session, err := queries.GetChatSession(ctx, task.ChatSessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return chatTerminalProjection{}, false, nil
	}
	if err != nil {
		return chatTerminalProjection{}, false, fmt.Errorf("load chat terminal session: %w", err)
	}
	if util.UUIDToString(session.WorkspaceID) != event.WorkspaceID {
		return chatTerminalProjection{}, false, fmt.Errorf("chat terminal projection workspace mismatch")
	}
	return chatTerminalProjection{task: task}, true, nil
}

func projectCompletedChatMessage(ctx context.Context, queries *db.Queries, task db.AgentTaskQueue) (*db.ChatMessage, error) {
	var result protocol.TaskCompletedPayload
	if err := json.Unmarshal(task.Result, &result); err != nil || result.Output == "" {
		return nil, nil
	}

	message, err := queries.CreateChatMessage(ctx, db.CreateChatMessageParams{
		ChatSessionID: task.ChatSessionID,
		Role:          "assistant",
		Content:       redact.Text(util.UnescapeBackslashEscapes(result.Output)),
		TaskID:        task.ID,
		ElapsedMs:     service.ComputeChatElapsedMs(task),
	})
	if err != nil {
		return nil, fmt.Errorf("persist completed chat message: %w", err)
	}
	if err := queries.SetUnreadSinceIfNull(ctx, task.ChatSessionID); err != nil {
		return nil, fmt.Errorf("mark completed chat session unread: %w", err)
	}
	return &message, nil
}

func projectFailedChatMessage(ctx context.Context, queries *db.Queries, task db.AgentTaskQueue) (db.ChatMessage, error) {
	message := ""
	if task.Error.Valid {
		message = redact.Text(task.Error.String)
	}
	created, err := queries.CreateChatMessage(ctx, db.CreateChatMessageParams{
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
	if err := queries.SetUnreadSinceIfNull(ctx, task.ChatSessionID); err != nil {
		return db.ChatMessage{}, fmt.Errorf("mark failed chat session unread: %w", err)
	}
	return created, nil
}

func completedChatEvent(event events.Event, task db.AgentTaskQueue, message *db.ChatMessage) events.Event {
	payload := protocol.ChatDonePayload{
		ChatSessionID: util.UUIDToString(task.ChatSessionID),
		TaskID:        util.UUIDToString(task.ID),
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
