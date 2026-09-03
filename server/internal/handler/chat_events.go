package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// buildChatSessionCreatedEvent records the session snapshot before it becomes
// visible to other clients. The session id is the logical identity, so a
// retried notification cannot create a second durable fact.
func buildChatSessionCreatedEvent(session db.ChatSession, actorType, actorID string) events.Event {
	return events.Event{
		Type:           protocol.EventChatSessionCreated,
		IdempotencyKey: "chat-session:created:" + util.UUIDToString(session.ID),
		StreamKey:      "chat:" + util.UUIDToString(session.ID),
		WorkspaceID:    util.UUIDToString(session.WorkspaceID),
		ActorType:      actorType,
		ActorID:        actorID,
		ChatSessionID:  util.UUIDToString(session.ID),
		Payload: protocol.ChatSessionCreatedPayload{
			WorkspaceID:   util.UUIDToString(session.WorkspaceID),
			ChatSessionID: util.UUIDToString(session.ID),
			AgentID:       util.UUIDToString(session.AgentID),
			CreatorID:     util.UUIDToString(session.CreatorID),
			Title:         session.Title,
		},
	}
}

// buildChatSessionUpdatedEvent uses the row returned by the mutation. The
// optional fields let one envelope represent title, project, pin, or status
// changes without inventing another event protocol.
func buildChatSessionUpdatedEvent(session db.ChatSession, actorType, actorID string, payload protocol.ChatSessionUpdatedPayload, action string) events.Event {
	payloadBytes, _ := json.Marshal(payload)
	digest := sha256.Sum256(payloadBytes)
	return events.Event{
		Type:           protocol.EventChatSessionUpdated,
		IdempotencyKey: "chat-session:updated:" + util.UUIDToString(session.ID) + ":" + action + ":" + hex.EncodeToString(digest[:]),
		StreamKey:      "chat:" + util.UUIDToString(session.ID),
		WorkspaceID:    util.UUIDToString(session.WorkspaceID),
		ActorType:      actorType,
		ActorID:        actorID,
		ChatSessionID:  util.UUIDToString(session.ID),
		Payload:        payload,
	}
}

func buildChatSessionDeletedEvent(session db.ChatSession, actorType, actorID string) events.Event {
	return events.Event{
		Type:           protocol.EventChatSessionDeleted,
		IdempotencyKey: "chat-session:deleted:" + util.UUIDToString(session.ID),
		StreamKey:      "chat:" + util.UUIDToString(session.ID),
		WorkspaceID:    util.UUIDToString(session.WorkspaceID),
		ActorType:      actorType,
		ActorID:        actorID,
		ChatSessionID:  util.UUIDToString(session.ID),
		Payload: protocol.ChatSessionDeletedPayload{
			ChatSessionID: util.UUIDToString(session.ID),
		},
	}
}

func buildChatSessionReadEvent(session db.ChatSession, actorType, actorID string) events.Event {
	return events.Event{
		Type:           protocol.EventChatSessionRead,
		IdempotencyKey: "chat-session:read:" + util.UUIDToString(session.ID) + ":" + strconv.FormatInt(session.LastReadAt.Time.UnixNano(), 10),
		StreamKey:      "chat:" + util.UUIDToString(session.ID),
		WorkspaceID:    util.UUIDToString(session.WorkspaceID),
		ActorType:      actorType,
		ActorID:        actorID,
		ChatSessionID:  util.UUIDToString(session.ID),
		Payload: protocol.ChatSessionReadPayload{
			ChatSessionID: util.UUIDToString(session.ID),
		},
	}
}

func buildChatMessageEvent(session db.ChatSession, message db.ChatMessage, task db.AgentTaskQueue, actorType, actorID string) events.Event {
	payload := protocol.ChatMessagePayload{
		ChatSessionID: util.UUIDToString(session.ID),
		MessageID:     util.UUIDToString(message.ID),
		Role:          message.Role,
		Content:       message.Content,
		TaskID:        util.UUIDToString(task.ID),
		CreatedAt:     timestampToString(message.CreatedAt),
	}
	return events.Event{
		Type:           protocol.EventChatMessage,
		IdempotencyKey: "chat-message:created:" + util.UUIDToString(message.ID),
		StreamKey:      "chat:" + util.UUIDToString(session.ID),
		WorkspaceID:    util.UUIDToString(session.WorkspaceID),
		ActorType:      actorType,
		ActorID:        actorID,
		TaskID:         util.UUIDToString(task.ID),
		ChatSessionID:  util.UUIDToString(session.ID),
		Payload:        payload,
	}
}

func recordChatEventTx(ctx context.Context, queries *db.Queries, event events.Event) (events.Event, error) {
	return service.RecordDurableEventTx(ctx, queries, event)
}
