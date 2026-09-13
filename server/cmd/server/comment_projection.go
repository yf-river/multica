// comment_projection.go
//
// 职责：读取评论事件，为"评论投影表"（comment projection）加载原始数据。
// 项目里把领域事件同步成面向查询的宽表（类似物化视图）叫投影：收到一条
// "评论已创建/更新"事件后，先从事件里解出评论 ID 与工作区 ID，再去库里
// 查出这条评论的真实当前状态，供投影逻辑写入。之所以要"回查"而不是直接
// 信事件里的数据，是为了跟主库最新状态对齐，也为了发现事件乱序或评论已被删
// 的情况。本文件只负责加载与校验，真正写投影的 SQL 在别处。
package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// loadCommentProjection 根据一条评论领域事件加载投影所需的评论数据。
//
// 返回值 (payload, found, err) 的含义：
//   - payload：从事件里解出的评论负载（即使没查到库也带回来，解不出则报错）；
//   - found：库里是否还存在这条评论（被删了就返回 false，调用方应视为"无需投影"，
//     而不是当作错误重试）；
//   - err：解析或查库失败。
//
// 这里还顺带做了两层校验：评论 ID/工作区 ID 必须是合法 UUID，且库里评论的
// IssueID 必须与事件里声明的保持一致——防止把评论投影到错误的 Issue 上。
func loadCommentProjection(ctx context.Context, queries *db.Queries, event events.Event) (commentEventPayload, bool, error) {
	payload, ok := decodeCommentEvent(event)
	if !ok {
		return commentEventPayload{}, false, fmt.Errorf("decode comment-created projection payload")
	}
	commentID, err := util.ParseUUID(payload.Comment.ID)
	if err != nil {
		return commentEventPayload{}, false, fmt.Errorf("projection event has invalid comment ID: %w", err)
	}
	workspaceID, err := util.ParseUUID(event.WorkspaceID)
	if err != nil {
		return commentEventPayload{}, false, fmt.Errorf("projection event has invalid workspace ID: %w", err)
	}
	comment, err := queries.GetCommentInWorkspace(ctx, db.GetCommentInWorkspaceParams{
		ID:          commentID,
		WorkspaceID: workspaceID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return payload, false, nil
	}
	if err != nil {
		return commentEventPayload{}, false, fmt.Errorf("load comment before projection: %w", err)
	}
	if util.UUIDToString(comment.IssueID) != payload.Comment.IssueID {
		return commentEventPayload{}, false, fmt.Errorf("comment projection issue mismatch")
	}
	return payload, true, nil
}
