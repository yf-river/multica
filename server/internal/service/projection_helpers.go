// projection_helpers.go —— durable 投影与 service 共享的纯辅助函数：inbox 投影 ID、
// quick-create 上下文解析、聊天耗时与 inbox 行线协议投影。
package service

import (
	"encoding/json"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// ProjectedInboxID 从源事件与接收者推导 inbox 投影的身份（确定性 UUID）。它跨消费者重试稳定，
// 而 recipient/type 字段让同一来源的两条通知彼此区分。
func ProjectedInboxID(source, recipientType, recipientID, notifType, issueID string) pgtype.UUID {
	name := strings.Join([]string{source, recipientType, recipientID, notifType, issueID}, "\x00")
	id := uuid.NewSHA1(uuid.NameSpaceOID, []byte(name))
	return pgtype.UUID{Bytes: [16]byte(id), Valid: true}
}

// ParseQuickCreateContext 解码独立 quick-create 任务 context 中存储的载荷。durable 投影与任务服务共用，
// 使两侧应用相同的形态与类型检查。
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

// ComputeChatElapsedMs 向 durable 投影暴露规范的“用户可见聊天耗时”计算。
func ComputeChatElapsedMs(task db.AgentTaskQueue) pgtype.Int8 {
	return computeChatElapsedMs(task)
}

// InboxItemFields 是持久化收件箱行的规范线协议投影（map）。
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
