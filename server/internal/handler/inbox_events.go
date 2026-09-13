// Package handler 是整个 HTTP API 的处理器集合。
//
// 本文件（inbox_events.go）负责构造与持久化 inbox（收件箱）相关的事件：
// 单条变更事件带幂等键（防重放），批量事件带计数，均落持久化 outbox。
package handler

import (
	"context"
	"strconv"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// buildInboxMutationEvent 构造一条 inbox 单条变更事件，并带幂等键
// （inbox 前缀+事件类型+条目 ID+创建时间+动作），确保同一条变更重复投递时
// 下游只处理一次。
func buildInboxMutationEvent(item db.InboxItem, eventType, actorType, actorID, action string, extra map[string]any) events.Event {
	payload := map[string]any{
		"item_id":      util.UUIDToString(item.ID),
		"recipient_id": util.UUIDToString(item.RecipientID),
		"action":       action,
	}
	for key, value := range extra {
		payload[key] = value
	}
	return events.Event{
		Type:           eventType,
		IdempotencyKey: "inbox:" + eventType + ":" + util.UUIDToString(item.ID) + ":" + strconv.FormatInt(item.CreatedAt.Time.UnixNano(), 10) + ":" + action,
		StreamKey:      "inbox:" + util.UUIDToString(item.RecipientID),
		WorkspaceID:    util.UUIDToString(item.WorkspaceID),
		ActorType:      actorType,
		ActorID:        actorID,
		Payload:        payload,
	}
}

// buildInboxBatchEvent 构造一条"批量收件箱操作"事件（如一键已读），
// 载荷里带受影响条目数 count。
func buildInboxBatchEvent(workspaceID, recipientID, actorType, actorID, eventType, action string, count int64) events.Event {
	return events.Event{
		Type:        eventType,
		StreamKey:   "inbox:" + recipientID,
		WorkspaceID: workspaceID,
		ActorType:   actorType,
		ActorID:     actorID,
		Payload: map[string]any{
			"recipient_id": recipientID,
			"count":        count,
			"action":       action,
		},
	}
}

// recordInboxEventTx 把事件信封写入持久化 outbox（与业务写入同事务），
// 保证事件不丢。
func recordInboxEventTx(ctx context.Context, queries *db.Queries, event events.Event) (events.Event, error) {
	return service.RecordDurableEventTx(ctx, queries, event)
}
