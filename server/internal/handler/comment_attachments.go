package handler

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

var errCommentAttachmentsUnavailable = errors.New("one or more attachments are unavailable for this comment")

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

// linkAttachmentsToNewComment links and verifies all requested attachments in
// the caller's transaction. The SQL update intentionally ignores unavailable
// rows; comparing the resulting set converts a partial claim into a rollbackable
// validation error.
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
