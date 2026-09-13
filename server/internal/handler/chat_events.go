// 本文件集中 chat 会话/消息域事件的「构建」小工具：把 db 行 + actor 身份打包成
// events.Event（类型、幂等键、流键、负载等）。IdempotencyKey 让重试的通知不会
// 造成第二条持久事实。recordChatEventTx 是 service.RecordDurableEventTx 的薄别名，
// 供 handler 包内事务化落事件。
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

// buildChatSessionCreatedEvent 在会话对其它客户端可见之前记录会话快照。会话 id
// 是逻辑身份，因此重试的通知不会创建第二条持久事实。
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

// buildChatSessionUpdatedEvent 使用变更语句返回的行。可选字段让同一信封表达
// title / project / pin / status 的变化，而无需发明另一套事件协议。幂等键额外
// 混入动作名与负载 sha256，让不同内容/动作的更新各归其位。
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

// buildChatSessionDeletedEvent 打包会话删除（硬删）事件，通知其它客户端该会话
// 已消失。
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

// buildChatSessionReadEvent 打包「已读」事件（去掉 unread 徽标）。幂等键混入
// LastReadAt 时间戳，同一时刻的多次已读归并为一条事实。
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

// buildChatMessageEvent 打包一条新聊天消息事件：消息本体 + 其派生的任务 id，让
// 各客户端既能渲染消息也能跟踪任务。
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

// recordChatEventTx 在调用方的事务内持久化一条域事件，返回落库后的事件。
func recordChatEventTx(ctx context.Context, queries *db.Queries, event events.Event) (events.Event, error) {
	return service.RecordDurableEventTx(ctx, queries, event)
}
