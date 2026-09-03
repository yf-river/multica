package service

import (
	"encoding/json"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// ProjectedInboxID derives the identity of an inbox projection from its
// source event and recipient. It is stable across consumer retries, while the
// recipient/type fields keep two notifications from one source distinct.
func ProjectedInboxID(source, recipientType, recipientID, notifType, issueID string) pgtype.UUID {
	name := strings.Join([]string{source, recipientType, recipientID, notifType, issueID}, "\x00")
	id := uuid.NewSHA1(uuid.NameSpaceOID, []byte(name))
	return pgtype.UUID{Bytes: [16]byte(id), Valid: true}
}

// ParseQuickCreateContext decodes the context stored on a standalone
// quick-create task. It is shared by durable projections and the task
// service so both paths apply the same shape and type check.
func ParseQuickCreateContext(task db.AgentTaskQueue) (QuickCreateContext, bool) {
	if task.IssueID.Valid || task.ChatSessionID.Valid || task.AutopilotRunID.Valid || len(task.Context) == 0 {
		return QuickCreateContext{}, false
	}
	var context QuickCreateContext
	if err := json.Unmarshal(task.Context, &context); err != nil || context.Type != QuickCreateContextType {
		return QuickCreateContext{}, false
	}
	return context, true
}

// ComputeChatElapsedMs exposes the canonical user-visible chat latency
// calculation to durable projections.
func ComputeChatElapsedMs(task db.AgentTaskQueue) pgtype.Int8 {
	return computeChatElapsedMs(task)
}

// InboxItemFields is the canonical wire projection for a persisted inbox row.
func InboxItemFields(item db.InboxItem) map[string]any {
	return map[string]any{
		"id":             util.UUIDToString(item.ID),
		"workspace_id":   util.UUIDToString(item.WorkspaceID),
		"recipient_type": item.RecipientType,
		"recipient_id":   util.UUIDToString(item.RecipientID),
		"type":           item.Type,
		"severity":       item.Severity,
		"issue_id":       util.UUIDToPtr(item.IssueID),
		"title":          item.Title,
		"body":           util.TextToPtr(item.Body),
		"read":           item.Read,
		"archived":       item.Archived,
		"created_at":     util.TimestampToString(item.CreatedAt),
		"actor_type":     util.TextToPtr(item.ActorType),
		"actor_id":       util.UUIDToPtr(item.ActorID),
		"details":        json.RawMessage(item.Details),
	}
}
