package handler

import (
	"context"
	"strconv"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

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

func recordInboxEventTx(ctx context.Context, queries *db.Queries, event events.Event) (events.Event, error) {
	return service.RecordDurableEventTx(ctx, queries, event)
}
