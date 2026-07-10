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

func registerDurableChatConsumers(dispatcher *eventoutbox.Dispatcher) error {
	return dispatcher.Register(protocol.EventTaskCompleted, chatCompletionProjectionConsumer, consumeChatCompletionProjection)
}

func consumeChatCompletionProjection(ctx context.Context, queries *db.Queries, event events.Event) ([]events.Event, error) {
	payload, task, exists, err := loadTaskProjectionRow(ctx, queries, event)
	if err != nil || !exists || payload.ChatSessionID == "" {
		return nil, err
	}
	if !task.ChatSessionID.Valid {
		// Deleting a chat session clears the task FK. Its terminal event is then
		// intentionally a no-op because there is no conversation left to update.
		return nil, nil
	}
	if util.UUIDToString(task.ChatSessionID) != payload.ChatSessionID {
		return nil, fmt.Errorf("chat completion projection session mismatch")
	}
	session, err := queries.GetChatSession(ctx, task.ChatSessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load chat completion session: %w", err)
	}
	if util.UUIDToString(session.WorkspaceID) != event.WorkspaceID {
		return nil, fmt.Errorf("chat completion projection workspace mismatch")
	}

	message, err := projectCompletedChatMessage(ctx, queries, task)
	if err != nil {
		return nil, err
	}
	return []events.Event{completedChatEvent(event, task, message)}, nil
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
		WorkspaceID:   event.WorkspaceID,
		ActorType:     "system",
		ChatSessionID: util.UUIDToString(task.ChatSessionID),
		Payload:       payload,
	}
}
