// 职责定位：把上传的附件绑定到新评论的辅助逻辑：在调用方事务里完成绑定，并把「部分认领
// 失败」转成可回滚的校验错误。
package handler

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// errCommentAttachmentsUnavailable 表示请求里至少有一个附件对该评论不可用（不存在/被占用/
// 归属错误）。
var errCommentAttachmentsUnavailable = errors.New("one or more attachments are unavailable for this comment")

// uniqueValidAttachmentIDs 校验入参全为合法 UUID 并去重；出现非法 id 时返回 false。
func uniqueValidAttachmentIDs(ids []pgtype.UUID) ([]pgtype.UUID, bool) {
	unique := make([]pgtype.UUID, 0, len(ids))
	seen := make(map[[16]byte]struct{}, len(ids))
	for _, id := range ids {
		if !id.Valid {
			return nil, false
		}
		if _, ok := seen[id.Bytes]; ok {
			continue
		}
		seen[id.Bytes] = struct{}{}
		unique = append(unique, id)
	}
	return unique, true
}

// linkAttachmentsToNewComment 在调用方事务里绑定并核验请求的所有附件。SQL 更新有意忽略不可用
// 行；随后对比实际集合，把「部分认领」变成可回滚的校验错误。
func linkAttachmentsToNewComment(ctx context.Context, q *db.Queries, comment db.Comment, ids []pgtype.UUID) ([]db.Attachment, error) {
	unique, ok := uniqueValidAttachmentIDs(ids)
	if !ok {
		return nil, errCommentAttachmentsUnavailable
	}
	if len(unique) > 0 {
		if err := q.LinkAttachmentsToComment(ctx, db.LinkAttachmentsToCommentParams{
			CommentID: comment.ID,
			IssueID:   comment.IssueID,
			Column3:   unique,
		}); err != nil {
			return nil, fmt.Errorf("link attachments to comment: %w", err)
		}
	}
	attachments, err := q.ListAttachmentsByComment(ctx, db.ListAttachmentsByCommentParams{
		CommentID:   comment.ID,
		WorkspaceID: comment.WorkspaceID,
	})
	if err != nil {
		return nil, fmt.Errorf("list comment attachments: %w", err)
	}
	if len(attachments) != len(unique) {
		return nil, errCommentAttachmentsUnavailable
	}
	linked := make(map[[16]byte]struct{}, len(attachments))
	for _, attachment := range attachments {
		linked[attachment.ID.Bytes] = struct{}{}
	}
	for _, id := range unique {
		if _, ok := linked[id.Bytes]; !ok {
			return nil, errCommentAttachmentsUnavailable
		}
	}
	return attachments, nil
}
