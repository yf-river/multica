// comment_thread.go —— 评论线程辅助：在调用方事务内“回复即重新打开已解决线程”，
// 返回的事件须在该事务提交后再发布。
package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// UnresolveThreadOnReply 在调用方事务内重新打开已解决的线程：若线程根已解决则清除 resolved_at 并
// 构造 comment:unresolved 事件返回（供提交后发布）；根为空或本未解决时返回 (空事件,false,nil)。
func UnresolveThreadOnReply(ctx context.Context, queries *db.Queries, root *db.Comment, workspaceID, actorType, actorID string) (events.Event, bool, error) {
	if root == nil {
		return events.Event{}, false, nil
	}
	updated, err := queries.UnresolveCommentIfResolved(ctx, root.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return events.Event{}, false, nil
	}
	if err != nil {
		return events.Event{}, false, fmt.Errorf("unresolve thread root %s: %w", util.UUIDToString(root.ID), err)
	}
	event := events.Event{
		Type:           protocol.EventCommentUnresolved,
		IdempotencyKey: "comment:unresolved:" + util.UUIDToString(updated.ID) + ":" + fmt.Sprint(updated.Revision),
		StreamKey:      "issue:" + util.UUIDToString(updated.IssueID),
		WorkspaceID:    workspaceID,
		ActorType:      actorType,
		ActorID:        actorID,
		Payload: map[string]any{
			"comment": map[string]any{
				"id":               util.UUIDToString(updated.ID),
				"issue_id":         util.UUIDToString(updated.IssueID),
				"author_type":      updated.AuthorType,
				"author_id":        util.UUIDToString(updated.AuthorID),
				"content":          updated.Content,
				"type":             updated.Type,
				"parent_id":        util.UUIDToPtr(updated.ParentID),
				"created_at":       util.TimestampToString(updated.CreatedAt),
				"updated_at":       util.TimestampToString(updated.UpdatedAt),
				"resolved_at":      util.TimestampToPtr(updated.ResolvedAt),
				"resolved_by_type": util.TextToPtr(updated.ResolvedByType),
				"resolved_by_id":   util.UUIDToPtr(updated.ResolvedByID),
				"revision":         updated.Revision,
			},
		},
	}
	return event, true, nil
}
